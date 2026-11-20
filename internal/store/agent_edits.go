package store

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	nteedb "github.com/nickooan/ntee-db/nteedb-core"
)

// AgentEditStatus is where an agent edit session stands.
type AgentEditStatus string

const (
	AgentEditPending       AgentEditStatus = "pending"        // staged; nothing written to the files yet
	AgentEditApplied       AgentEditStatus = "applied"        // every file was written
	AgentEditPartlyApplied AgentEditStatus = "partly-applied" // some files were written, some were not
	AgentEditNotApplied    AgentEditStatus = "not-applied"    // applying wrote no file (all conflicted or skipped)
	AgentEditDiscarded     AgentEditStatus = "discarded"      // dropped by the user or the agent
)

// AgentEditFileResult is what applying did with one staged file.
type AgentEditFileResult string

const (
	AgentEditFileApplied    AgentEditFileResult = "applied"
	AgentEditFileConflicted AgentEditFileResult = "conflicted" // the file changed on disk after it was staged
	AgentEditFileSkipped    AgentEditFileResult = "skipped"    // unsaved edits in the editor, or a write failure
)

// AgentEditSession is one agent edit session, kept whole in a single record:
// every file's content before and after, and each edit in order. It stays
// after the session finishes, so which file contents an agent changed — and
// what became of them — can be traced later.
type AgentEditSession struct {
	Session      string          `json:"session"`
	Status       AgentEditStatus `json:"status"`
	Policy       string          `json:"policy"` // the policy that finished it ("review" or "apply"); "" while pending
	StartedAt    time.Time       `json:"startedAt"`
	AgentEndedAt time.Time       `json:"agentEndedAt"` // the agent sent end; zero until then
	EndedAt      time.Time       `json:"endedAt"`      // applied or discarded; zero while pending
	Outcome      string          `json:"outcome"`
	Files        []AgentEditFile `json:"files"`
}

// AgentEditFile is one staged file: the disk content when it was first
// staged (BaseContent, the conflict check's reference), the staged content,
// and the edits that led from one to the other.
type AgentEditFile struct {
	Rel         string `json:"rel"` // workspace-relative
	BaseExists  bool   `json:"baseExists"`
	BaseContent string `json:"baseContent"`
	// BaseSession is the pending session whose staged copy this file was
	// built on (BaseContent is that copy); "" = the disk. Empty is the
	// meaning older records already have.
	BaseSession string              `json:"baseSession,omitempty"`
	Content     string              `json:"content"`
	Result      AgentEditFileResult `json:"result"` // "" while pending
	ResultNote  string              `json:"resultNote"`
	Edits       []AgentEdit         `json:"edits"`
}

// AgentEdit is one edit as the agent sent it. A write keeps only its line
// stats — the file's Content holds the text.
type AgentEdit struct {
	Op       string    `json:"op"` // "write" or "replace"
	Old      string    `json:"old,omitempty"`
	New      string    `json:"new,omitempty"`
	All      bool      `json:"all,omitempty"`
	LineFrom int       `json:"lineFrom"` // 1-based lines of the change in the content after it
	LineTo   int       `json:"lineTo"`
	Added    int       `json:"added"`
	Removed  int       `json:"removed"`
	At       time.Time `json:"at"`
}

// Finished reports whether the session is over (applied or discarded).
func (session AgentEditSession) Finished() bool {
	return session.Status != AgentEditPending && session.Status != ""
}

// AgentEditLogStore keeps a project's agent edit sessions: the pending ones,
// and the latest finished ones up to a limit. Implementations are safe for
// concurrent use — the socket goroutine saves edits while the UI finishes
// sessions from tea.Cmd goroutines.
type AgentEditLogStore interface {
	// SavePendingAgentEdit writes a pending session's record, replacing its
	// previous one.
	SavePendingAgentEdit(session AgentEditSession) error
	// FinishAgentEdit moves a finished session to the finished records and
	// keeps only the newest keep of them.
	FinishAgentEdit(session AgentEditSession, keep int) error
	// LoadPendingAgentEdits returns the pending sessions, oldest first.
	LoadPendingAgentEdits() ([]AgentEditSession, error)
	// LoadFinishedAgentEdits returns the finished sessions, newest first.
	LoadFinishedAgentEdits() ([]AgentEditSession, error)
	// TrimAgentEdits deletes the oldest finished sessions beyond keep.
	TrimAgentEdits(keep int) error
}

const (
	agentEditPendingPrefix = "agentedit:pending:"
	agentEditDonePrefix    = "agentedit:done:"
)

// Keys embed zero-padded nanoseconds so they sort by time: pending by start,
// finished by end, which makes trimming the oldest finished a key-order cut.
func agentEditPendingKey(session AgentEditSession) string {
	return fmt.Sprintf("%s%020d:%s", agentEditPendingPrefix, session.StartedAt.UnixNano(), session.Session)
}

func agentEditDoneKey(session AgentEditSession) string {
	return fmt.Sprintf("%s%020d:%s", agentEditDonePrefix, session.EndedAt.UnixNano(), session.Session)
}

// AgentEditLog is the ntee-db-backed AgentEditLogStore, next to the external
// history. Like it, it opens the db per call.
type AgentEditLog struct {
	dir string
}

// NewAgentEditLog is the agent edit log of the project at projectRoot.
func NewAgentEditLog(projectRoot string) (*AgentEditLog, error) {
	dir, err := ExternalDir(projectRoot)
	if err != nil {
		return nil, err
	}
	return &AgentEditLog{dir: filepath.Join(dir, "edits")}, nil
}

func (log *AgentEditLog) withDB(run func(db *nteedb.DB) error) error {
	return withDBAt(nteedb.Options{Dir: log.dir}, run)
}

func (log *AgentEditLog) SavePendingAgentEdit(session AgentEditSession) error {
	data, err := json.Marshal(session)
	if err != nil {
		return err
	}
	return log.withDB(func(db *nteedb.DB) error {
		return db.Put(agentEditPendingKey(session), data)
	})
}

func (log *AgentEditLog) FinishAgentEdit(session AgentEditSession, keep int) error {
	data, err := json.Marshal(session)
	if err != nil {
		return err
	}
	return log.withDB(func(db *nteedb.DB) error {
		if err := db.Put(agentEditDoneKey(session), data); err != nil {
			return err
		}
		if err := db.Delete(agentEditPendingKey(session)); err != nil {
			return err
		}
		return trimAgentEditKeys(db, keep)
	})
}

func (log *AgentEditLog) LoadPendingAgentEdits() ([]AgentEditSession, error) {
	return log.load(agentEditPendingPrefix)
}

func (log *AgentEditLog) LoadFinishedAgentEdits() ([]AgentEditSession, error) {
	sessions, err := log.load(agentEditDonePrefix)
	reverseAgentEdits(sessions)
	return sessions, err
}

// load returns the sessions under prefix in key (time) order.
func (log *AgentEditLog) load(prefix string) ([]AgentEditSession, error) {
	var sessions []AgentEditSession
	err := log.withDB(func(db *nteedb.DB) error {
		keys, err := db.PrefixScan(prefix)
		if err != nil || len(keys) == 0 {
			return err
		}
		sort.Strings(keys)
		values, found, err := db.GetMany(keys)
		if err != nil {
			return err
		}
		for index := range keys {
			var session AgentEditSession
			if found[index] && json.Unmarshal(values[index], &session) == nil {
				sessions = append(sessions, session)
			}
		}
		return nil
	})
	return sessions, err
}

func (log *AgentEditLog) TrimAgentEdits(keep int) error {
	return log.withDB(func(db *nteedb.DB) error { return trimAgentEditKeys(db, keep) })
}

func trimAgentEditKeys(db *nteedb.DB, keep int) error {
	keys, err := db.PrefixScan(agentEditDonePrefix)
	if err != nil {
		return err
	}
	sort.Strings(keys)
	for _, key := range keys[:max(0, len(keys)-ClampAgentEditKeep(keep))] {
		if err := db.Delete(key); err != nil {
			return err
		}
	}
	return nil
}

func reverseAgentEdits(sessions []AgentEditSession) {
	for left, right := 0, len(sessions)-1; left < right; left, right = left+1, right-1 {
		sessions[left], sessions[right] = sessions[right], sessions[left]
	}
}

// MemoryAgentEditLog is the in-process AgentEditLogStore (tests, and the
// fallback when the project's directory can't be resolved).
type MemoryAgentEditLog struct {
	mu      sync.Mutex
	records map[string]AgentEditSession
}

func NewMemoryAgentEditLog() *MemoryAgentEditLog {
	return &MemoryAgentEditLog{records: map[string]AgentEditSession{}}
}

func (m *MemoryAgentEditLog) SavePendingAgentEdit(session AgentEditSession) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records[agentEditPendingKey(session)] = session
	return nil
}

func (m *MemoryAgentEditLog) FinishAgentEdit(session AgentEditSession, keep int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.records, agentEditPendingKey(session))
	m.records[agentEditDoneKey(session)] = session
	m.trimLocked(keep)
	return nil
}

func (m *MemoryAgentEditLog) LoadPendingAgentEdits() ([]AgentEditSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loadLocked(agentEditPendingPrefix), nil
}

func (m *MemoryAgentEditLog) LoadFinishedAgentEdits() ([]AgentEditSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sessions := m.loadLocked(agentEditDonePrefix)
	reverseAgentEdits(sessions)
	return sessions, nil
}

func (m *MemoryAgentEditLog) TrimAgentEdits(keep int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.trimLocked(keep)
	return nil
}

func (m *MemoryAgentEditLog) keysLocked(prefix string) []string {
	var keys []string
	for key := range m.records {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func (m *MemoryAgentEditLog) loadLocked(prefix string) []AgentEditSession {
	keys := m.keysLocked(prefix)
	sessions := make([]AgentEditSession, 0, len(keys))
	for _, key := range keys {
		sessions = append(sessions, m.records[key])
	}
	return sessions
}

func (m *MemoryAgentEditLog) trimLocked(keep int) {
	keys := m.keysLocked(agentEditDonePrefix)
	for _, key := range keys[:max(0, len(keys)-ClampAgentEditKeep(keep))] {
		delete(m.records, key)
	}
}
