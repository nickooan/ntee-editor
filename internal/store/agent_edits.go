package store

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
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
	AgentEndedAt time.Time       `json:"agentEndedAt"` // every agent sent end (or the user stopped waiting); zero until then
	EndedAt      time.Time       `json:"endedAt"`      // applied or discarded; zero while pending
	Outcome      string          `json:"outcome"`
	Files        []AgentEditFile `json:"files"`
	// Agents are the agents contributing to the session, in joining order.
	// A record from before agents were tracked loads with one anonymous
	// agent (WithAgents).
	Agents []AgentEditParticipant `json:"agents"`
	// Checks: once every agent has ended, the session finalises — its
	// agents build and test it in a sandbox and report — before the user is
	// asked (ReadyAt). Older records (false) are ready once their agents end.
	Checks  bool      `json:"checks"`
	ReadyAt time.Time `json:"readyAt"` // every check is in (or the user stopped waiting); zero until then
	// ChangeRequests is every time the user asked the agents for changes
	// from the review, oldest first; stored as [] when there are none.
	ChangeRequests []AgentEditChangeRequest `json:"changeRequests"`
	// GitFollowUp is what the user asked the agent to do in git once the
	// session was applied; the editor itself never runs git for it.
	GitFollowUp AgentEditGitFollowUp `json:"gitFollowUp"`
	// GitBranches is each repo's checked-out branch when the session was
	// applied with a git follow-up, by repo name: the agent hears it.
	GitBranches map[string]string `json:"gitBranches,omitempty"`
	// GitReport is what the agent said it did about GitFollowUp; nil until
	// it reports.
	GitReport *AgentEditGitReport `json:"gitReport,omitempty"`
}

// AgentEditGitReport is the agent's word on the git work the user asked of
// it: done (with the commit or pull request), blocked (it needs the user,
// e.g. its harness asked them to confirm) or declined.
type AgentEditGitReport struct {
	Result      AgentEditGitReportResult `json:"result"`
	Note        string                   `json:"note,omitempty"`
	Commit      string                   `json:"commit,omitempty"`
	PullRequest string                   `json:"pullRequest,omitempty"`
	At          time.Time                `json:"at"`
}

type AgentEditGitReportResult string

const (
	GitReportDone     AgentEditGitReportResult = "done"
	GitReportBlocked  AgentEditGitReportResult = "blocked"
	GitReportDeclined AgentEditGitReportResult = "declined"
)

// AgentEditGitFollowUp is the git work the user asks of the agent along
// with applying a session.
type AgentEditGitFollowUp string

const (
	GitFollowUpNone        AgentEditGitFollowUp = "none"         // apply only: the agent leaves git alone
	GitFollowUpCommit      AgentEditGitFollowUp = "commit"       // commit the applied files on the current branch
	GitFollowUpPullRequest AgentEditGitFollowUp = "pull-request" // a new branch (name agreed with the user), commit, push, open a PR
)

// AgentEditChangeRequest is the user asking a ready session's agents for
// changes: the session went back to editing. It is answered by the next edit
// or end one of its agents sends.
type AgentEditChangeRequest struct {
	At         time.Time `json:"at"`
	AnsweredAt time.Time `json:"answeredAt"` // zero until an agent edits or ends
	AnsweredBy string    `json:"answeredBy"` // the agent that answered; "" = unnamed
}

// OpenChangeRequest is the user's change request no agent has answered yet.
func (session AgentEditSession) OpenChangeRequest() (AgentEditChangeRequest, bool) {
	if len(session.ChangeRequests) == 0 {
		return AgentEditChangeRequest{}, false
	}
	last := session.ChangeRequests[len(session.ChangeRequests)-1]
	return last, last.AnsweredAt.IsZero()
}

// WithStoredDefaults fills in what a record from before change requests and
// git follow-ups were kept lacks: an empty log and no follow-up.
func (session AgentEditSession) WithStoredDefaults() AgentEditSession {
	if session.ChangeRequests == nil {
		session.ChangeRequests = []AgentEditChangeRequest{}
	}
	if session.GitFollowUp == "" {
		session.GitFollowUp = GitFollowUpNone
	}
	return session
}

// AgentEditPhase is where a pending session stands.
type AgentEditPhase string

const (
	PhaseEditing    AgentEditPhase = "editing"    // agents stage edits; others may join
	PhaseFinalising AgentEditPhase = "finalising" // every agent ended: they check the edits; no one may join
	PhaseReady      AgentEditPhase = "ready"      // the user's to review
)

// Phase is the pending session's stage.
func (session AgentEditSession) Phase() AgentEditPhase {
	switch {
	case session.AgentEndedAt.IsZero():
		return PhaseEditing
	case !session.Checks || !session.ReadyAt.IsZero():
		return PhaseReady
	}
	return PhaseFinalising
}

// AgentEditCheckResult is what an agent's check of the finalising session
// found.
type AgentEditCheckResult string

const (
	CheckPassed  AgentEditCheckResult = "passed"
	CheckFailed  AgentEditCheckResult = "failed"
	CheckSkipped AgentEditCheckResult = "skipped"
)

// CheckingAgents lists the agents whose check of a finalising session is
// still to come.
func (session AgentEditSession) CheckingAgents() []string {
	var checking []string
	for _, participant := range session.Agents {
		if !participant.Stopped && participant.Check == "" {
			checking = append(checking, participant.Agent)
		}
	}
	return checking
}

// AgentEditParticipant is one agent's part in a session: "" is an agent
// that gave no name. Stopped marks an agent still editing when the user
// stopped waiting for it.
type AgentEditParticipant struct {
	Agent    string    `json:"agent"`
	JoinedAt time.Time `json:"joinedAt"`
	EndedAt  time.Time `json:"endedAt"` // zero while the agent is editing
	Stopped  bool      `json:"stopped"`
	// Check is the agent's report on the finalising session, "" until it
	// sends one; the note says what it ran and found.
	Check     AgentEditCheckResult `json:"check"`
	CheckNote string               `json:"checkNote"`
	CheckedAt time.Time            `json:"checkedAt"`
}

// Editing reports whether the agent has not sent end yet.
func (participant AgentEditParticipant) Editing() bool {
	return participant.EndedAt.IsZero()
}

// WithAgents is the session with its agents filled in for a record from
// before agents were tracked: one anonymous agent, joined at the start and
// ended when the session's agent did.
func (session AgentEditSession) WithAgents() AgentEditSession {
	if len(session.Agents) == 0 && len(session.Files) > 0 {
		session.Agents = []AgentEditParticipant{{JoinedAt: session.StartedAt, EndedAt: session.AgentEndedAt}}
	}
	return session
}

// EditingAgents lists the agents that have not sent end yet.
func (session AgentEditSession) EditingAgents() []string {
	var editing []string
	for _, participant := range session.Agents {
		if participant.Editing() {
			editing = append(editing, participant.Agent)
		}
	}
	return editing
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
	Agent    string    `json:"agent"`   // the agent that sent it; "" = unnamed
	Version  string    `json:"version"` // the file's content after it (staging.Version)
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
	// SavePendingAgentEditFiles is SavePendingAgentEdit when only the files
	// named in changed (none: only the session's own fields) differ from
	// the record last saved — an edit rewrites one file, not the session.
	SavePendingAgentEditFiles(session AgentEditSession, changed []string) error
	// FinishAgentEdit moves a finished session to the finished records and
	// keeps only the newest keep of them.
	FinishAgentEdit(session AgentEditSession, keep int) error
	// SaveFinishedAgentEdit rewrites a finished session's record (and its
	// summary) in place, e.g. once the agent reports its git work.
	SaveFinishedAgentEdit(session AgentEditSession) error
	// LoadPendingAgentEdits returns the pending sessions, oldest first.
	LoadPendingAgentEdits() ([]AgentEditSession, error)
	// LoadFinishedAgentEdits returns the finished sessions, newest first.
	LoadFinishedAgentEdits() ([]AgentEditSession, error)
	// LoadFinishedAgentEditSummaries is LoadFinishedAgentEdits without the
	// files' contents and edits (SummarizeAgentEdit) — what a list needs.
	LoadFinishedAgentEditSummaries() ([]AgentEditSession, error)
	// LoadFinishedAgentEdit returns one finished session whole, for tracing.
	LoadFinishedAgentEdit(session string) (AgentEditSession, bool, error)
	// TrimAgentEdits deletes the oldest finished sessions beyond keep.
	TrimAgentEdits(keep int) error
	// Tidy reclaims the space dead records take, when that is worth it.
	Tidy() (TidyResult, error)
}

// TidyResult is what a Tidy did: whether the main log was compacted and
// the blobs relieved, and the store's size before and after.
type TidyResult struct {
	Compacted, Relieved bool
	Before, After       int64
}

// tidyMinBytes is the size below which a log is never rewritten: too small
// to be worth it. A variable, so tests can lower it.
var tidyMinBytes int64 = 1 << 20

const (
	agentEditPendingPrefix     = "agentedit:pending:"
	agentEditPendingFilePrefix = "agentedit:pendingfile:"
	agentEditDonePrefix        = "agentedit:done:"
	agentEditSummaryPrefix     = "agentedit:donesum:"
)

// SummarizeAgentEdit is session without its files' contents and edits: the
// status, outcome, agents and each file's path and result.
func SummarizeAgentEdit(session AgentEditSession) AgentEditSession {
	files := make([]AgentEditFile, len(session.Files))
	for index, file := range session.Files {
		files[index] = AgentEditFile{Rel: file.Rel, BaseExists: file.BaseExists, BaseSession: file.BaseSession, Result: file.Result, ResultNote: file.ResultNote, Edits: []AgentEdit{}}
	}
	session.Files = files
	return session
}

// agentEditSummaryKey is a finished session's summary, beside its done key.
func agentEditSummaryKey(doneKey string) string {
	return agentEditSummaryPrefix + strings.TrimPrefix(doneKey, agentEditDonePrefix)
}

// pendingAgentEditHeader is how the db keeps a pending session: its own
// fields under the pending key, with each staged file under its own key
// (agentEditPendingFileKey) so an edit rewrites only its file. FileRels
// names them in order, stored as [] when there are none; a record from
// before files were kept apart has no fileRels and its files inline.
type pendingAgentEditHeader struct {
	AgentEditSession
	FileRels []string `json:"fileRels"`
}

// agentEditPendingFileKey is one staged file of a pending session. The
// session id is length-prefixed, so no id or path can run into another's.
func agentEditPendingFileKey(session AgentEditSession, rel string) string {
	return fmt.Sprintf("%s%020d:%d:%s:%s", agentEditPendingFilePrefix, session.StartedAt.UnixNano(), len(session.Session), session.Session, rel)
}

// Keys embed zero-padded nanoseconds so they sort by time: pending by start,
// finished by end, which makes trimming the oldest finished a key-order cut.
func agentEditPendingKey(session AgentEditSession) string {
	return fmt.Sprintf("%s%020d:%s", agentEditPendingPrefix, session.StartedAt.UnixNano(), session.Session)
}

func agentEditDoneKey(session AgentEditSession) string {
	return fmt.Sprintf("%s%020d:%s", agentEditDonePrefix, session.EndedAt.UnixNano(), session.Session)
}

// AgentEditLog is the ntee-db-backed AgentEditLogStore, next to the external
// history. Its connection is a session lease: open while sessions are being
// staged, closed when the last one finishes or it sat idle.
type AgentEditLog struct {
	dir string

	// apart holds the pending keys whose every file is stored under its own
	// key, as this process last saved or loaded them. A session not in it
	// (new, or a legacy record with inline files) gets all its files written
	// on its next save, so a header never names a file that is not there.
	apartMu sync.Mutex
	apart   map[string]bool
}

// NewAgentEditLog is the agent edit log of the project at projectRoot.
func NewAgentEditLog(projectRoot string) (*AgentEditLog, error) {
	dir, err := ExternalDir(projectRoot)
	if err != nil {
		return nil, err
	}
	return &AgentEditLog{dir: filepath.Join(dir, "edits"), apart: map[string]bool{}}, nil
}

func (log *AgentEditLog) withDB(run func(db *nteedb.DB) error) error {
	return log.withDBUse(connectionUse{}, run)
}

func (log *AgentEditLog) withDBUse(use connectionUse, run func(db *nteedb.DB) error) error {
	return leaseDBAt(nteedb.Options{Dir: log.dir}, use, run)
}

// Connections reports the edit log's connection.
func (log *AgentEditLog) Connections() []ConnectionStatus {
	return []ConnectionStatus{connectionStatusAt(log.dir, "agent edits", ConnectionSessionLease)}
}

func (log *AgentEditLog) SavePendingAgentEdit(session AgentEditSession) error {
	rels := make([]string, len(session.Files))
	for index, file := range session.Files {
		rels[index] = file.Rel
	}
	return log.SavePendingAgentEditFiles(session, rels)
}

func (log *AgentEditLog) SavePendingAgentEditFiles(session AgentEditSession, changed []string) error {
	pendingKey := agentEditPendingKey(session)
	log.apartMu.Lock()
	allFiles := !log.apart[pendingKey]
	log.apartMu.Unlock()
	header := pendingAgentEditHeader{AgentEditSession: session, FileRels: make([]string, len(session.Files))}
	header.Files = []AgentEditFile{}
	files := map[string][]byte{}
	for index, file := range session.Files {
		header.FileRels[index] = file.Rel
		if !allFiles && !slices.Contains(changed, file.Rel) {
			continue
		}
		data, err := json.Marshal(file)
		if err != nil {
			return err
		}
		files[agentEditPendingFileKey(session, file.Rel)] = data
	}
	headerData, err := json.Marshal(header)
	if err != nil {
		return err
	}
	err = log.withDBUse(connectionUse{begin: pendingKey}, func(db *nteedb.DB) error {
		// Files first: a header never names a file it cannot find.
		for key, data := range files {
			if err := db.Put(key, data); err != nil {
				return err
			}
		}
		return db.Put(pendingKey, headerData)
	})
	if err == nil {
		log.markApart(pendingKey, true)
	}
	return err
}

func (log *AgentEditLog) markApart(pendingKey string, apart bool) {
	log.apartMu.Lock()
	defer log.apartMu.Unlock()
	if log.apart == nil {
		log.apart = map[string]bool{}
	}
	if apart {
		log.apart[pendingKey] = true
	} else {
		delete(log.apart, pendingKey)
	}
}

func (log *AgentEditLog) FinishAgentEdit(session AgentEditSession, keep int) error {
	data, err := json.Marshal(session)
	if err != nil {
		return err
	}
	summary, err := json.Marshal(SummarizeAgentEdit(session))
	if err != nil {
		return err
	}
	return log.withDBUse(connectionUse{end: agentEditPendingKey(session)}, func(db *nteedb.DB) error {
		if err := db.Put(agentEditDoneKey(session), data); err != nil {
			return err
		}
		if err := db.Put(agentEditSummaryKey(agentEditDoneKey(session)), summary); err != nil {
			return err
		}
		if err := db.Delete(agentEditPendingKey(session)); err != nil {
			return err
		}
		for _, file := range session.Files {
			if err := db.Delete(agentEditPendingFileKey(session, file.Rel)); err != nil {
				return err
			}
		}
		log.markApart(agentEditPendingKey(session), false)
		if err := trimAgentEditKeys(db, keep); err != nil {
			return err
		}
		// Housekeeping: a failed tidy leaves the dead data for the next one,
		// it does not fail the finish.
		_, _ = tidyAgentEditDB(db)
		return nil
	})
}

func (log *AgentEditLog) SaveFinishedAgentEdit(session AgentEditSession) error {
	data, err := json.Marshal(session)
	if err != nil {
		return err
	}
	summary, err := json.Marshal(SummarizeAgentEdit(session))
	if err != nil {
		return err
	}
	return log.withDB(func(db *nteedb.DB) error {
		_, ok, err := db.Get(agentEditDoneKey(session))
		switch {
		case err != nil:
			return err
		case !ok:
			return fmt.Errorf("no finished edit session %s", session.Session)
		}
		if err := db.Put(agentEditDoneKey(session), data); err != nil {
			return err
		}
		return db.Put(agentEditSummaryKey(agentEditDoneKey(session)), summary)
	})
}

func (log *AgentEditLog) LoadPendingAgentEdits() ([]AgentEditSession, error) {
	var sessions []AgentEditSession
	err := log.withDB(func(db *nteedb.DB) error {
		keys, err := db.PrefixScan(agentEditPendingPrefix)
		if err != nil || len(keys) == 0 {
			return err
		}
		sort.Strings(keys)
		values, found, err := db.GetMany(keys)
		if err != nil {
			return err
		}
		for index := range keys {
			var header pendingAgentEditHeader
			if !found[index] || json.Unmarshal(values[index], &header) != nil {
				continue
			}
			session := header.AgentEditSession
			if header.FileRels != nil {
				if session.Files, err = loadPendingAgentEditFiles(db, session, header.FileRels); err != nil {
					return err
				}
				log.markApart(keys[index], true)
			}
			sessions = append(sessions, session.WithAgents().WithStoredDefaults())
		}
		return nil
	})
	return sessions, err
}

// loadPendingAgentEditFiles reads a pending session's files kept apart, in
// the header's order.
func loadPendingAgentEditFiles(db *nteedb.DB, session AgentEditSession, rels []string) ([]AgentEditFile, error) {
	keys := make([]string, len(rels))
	for index, rel := range rels {
		keys[index] = agentEditPendingFileKey(session, rel)
	}
	values, found, err := db.GetMany(keys)
	if err != nil {
		return nil, err
	}
	files := make([]AgentEditFile, 0, len(rels))
	for index, rel := range rels {
		var file AgentEditFile
		if !found[index] || json.Unmarshal(values[index], &file) != nil {
			return nil, fmt.Errorf("edit session %s: the staged copy of %s is missing from the log", session.Session, rel)
		}
		files = append(files, file)
	}
	return files, nil
}

func (log *AgentEditLog) LoadFinishedAgentEdits() ([]AgentEditSession, error) {
	var sessions []AgentEditSession
	err := log.withDB(func(db *nteedb.DB) error {
		var err error
		sessions, err = decodePrefix[AgentEditSession](db, agentEditDonePrefix)
		for index := range sessions {
			sessions[index] = sessions[index].WithAgents().WithStoredDefaults()
		}
		return err
	})
	reverseAgentEdits(sessions)
	return sessions, err
}

// LoadFinishedAgentEditSummaries reads the summaries, newest first. A
// session finished before summaries were kept gets one now, from its whole
// record, once.
func (log *AgentEditLog) LoadFinishedAgentEditSummaries() ([]AgentEditSession, error) {
	var sessions []AgentEditSession
	err := log.withDB(func(db *nteedb.DB) error {
		doneKeys, err := db.PrefixScan(agentEditDonePrefix)
		if err != nil || len(doneKeys) == 0 {
			return err
		}
		sort.Strings(doneKeys)
		summaryKeys := make([]string, len(doneKeys))
		for index, key := range doneKeys {
			summaryKeys[index] = agentEditSummaryKey(key)
		}
		values, found, err := db.GetMany(summaryKeys)
		if err != nil {
			return err
		}
		for index, key := range doneKeys {
			var session AgentEditSession
			if found[index] && json.Unmarshal(values[index], &session) == nil {
				sessions = append(sessions, session.WithAgents().WithStoredDefaults())
				continue
			}
			whole, ok, err := db.Get(key)
			if err != nil || !ok || json.Unmarshal(whole, &session) != nil {
				continue
			}
			session = SummarizeAgentEdit(session)
			if summary, err := json.Marshal(session); err == nil {
				_ = db.Put(summaryKeys[index], summary) // best effort: it is rebuilt next time otherwise
			}
			sessions = append(sessions, session.WithAgents().WithStoredDefaults())
		}
		return nil
	})
	reverseAgentEdits(sessions)
	return sessions, err
}

func (log *AgentEditLog) LoadFinishedAgentEdit(id string) (AgentEditSession, bool, error) {
	var session AgentEditSession
	found := false
	err := log.withDB(func(db *nteedb.DB) error {
		keys, err := db.PrefixScan(agentEditDonePrefix)
		if err != nil {
			return err
		}
		for _, key := range keys {
			if !strings.HasSuffix(key, ":"+id) {
				continue
			}
			data, ok, err := db.Get(key)
			if err != nil {
				return err
			}
			if ok && json.Unmarshal(data, &session) == nil && session.Session == id {
				session, found = session.WithAgents().WithStoredDefaults(), true
				return nil
			}
		}
		return nil
	})
	return session, found, err
}

func (log *AgentEditLog) TrimAgentEdits(keep int) error {
	return log.withDB(func(db *nteedb.DB) error {
		if err := trimAgentEditKeys(db, keep); err != nil {
			return err
		}
		_, _ = tidyAgentEditDB(db)
		return nil
	})
}

// Tidy reclaims the edit log's dead space when that is worth it — every
// staged edit rewrites its session's whole record, and ntee-db only
// appends. Run at editor start, in the background.
func (log *AgentEditLog) Tidy() (TidyResult, error) {
	var result TidyResult
	err := log.withDB(func(db *nteedb.DB) error {
		var err error
		result, err = tidyAgentEditDB(db)
		return err
	})
	return result, err
}

// tidyAgentEditDB compacts the main log once it is past tidyMinBytes and
// less than half live, and relieves the blobs once they are past it and at
// least half orphaned (which compacts the main log in the same pass). The
// checks read sizes ntee-db keeps in memory, plus one pass over the live
// records' lines for the blobs; a small or healthy log is left alone.
func tidyAgentEditDB(db *nteedb.DB) (TidyResult, error) {
	stats := db.Stats()
	result := TidyResult{Before: stats.MainBytes + stats.BlobBytes}
	result.After = result.Before
	if stats.BlobBytes >= tidyMinBytes {
		usage, err := db.BlobUsage()
		if err != nil {
			return result, err
		}
		if usage.OrphanedBytes*2 >= usage.TotalBytes {
			if err := db.BlobsRelieve(); err != nil {
				return result, err
			}
			result.Relieved = true
		}
	}
	if !result.Relieved && stats.MainBytes >= tidyMinBytes && db.LiveBytes()*2 < stats.MainBytes {
		if err := db.Compact(); err != nil {
			return result, err
		}
		result.Compacted = true
	}
	after := db.Stats()
	result.After = after.MainBytes + after.BlobBytes
	return result, nil
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
		if err := db.Delete(agentEditSummaryKey(key)); err != nil {
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

// SavePendingAgentEditFiles keeps the whole record: memory has no write
// cost to save.
func (m *MemoryAgentEditLog) SavePendingAgentEditFiles(session AgentEditSession, _ []string) error {
	return m.SavePendingAgentEdit(session)
}

func (m *MemoryAgentEditLog) FinishAgentEdit(session AgentEditSession, keep int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.records, agentEditPendingKey(session))
	m.records[agentEditDoneKey(session)] = session
	m.trimLocked(keep)
	return nil
}

func (m *MemoryAgentEditLog) SaveFinishedAgentEdit(session AgentEditSession) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.records[agentEditDoneKey(session)]; !ok {
		return fmt.Errorf("no finished edit session %s", session.Session)
	}
	m.records[agentEditDoneKey(session)] = session
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

func (m *MemoryAgentEditLog) LoadFinishedAgentEditSummaries() ([]AgentEditSession, error) {
	sessions, err := m.LoadFinishedAgentEdits()
	for index := range sessions {
		sessions[index] = SummarizeAgentEdit(sessions[index])
	}
	return sessions, err
}

func (m *MemoryAgentEditLog) LoadFinishedAgentEdit(id string) (AgentEditSession, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, session := range m.loadLocked(agentEditDonePrefix) {
		if session.Session == id {
			return session, true, nil
		}
	}
	return AgentEditSession{}, false, nil
}

// Tidy has nothing to reclaim in memory.
func (m *MemoryAgentEditLog) Tidy() (TidyResult, error) { return TidyResult{}, nil }

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
