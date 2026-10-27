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

// ExternalSettings are the global switches for external messages (the
// per-project unix socket). Both fields are always written; a missing
// record loads as DefaultExternalSettings.
type ExternalSettings struct {
	Enabled      bool `json:"enabled"`
	HistoryLimit int  `json:"historyLimit"`
}

const (
	DefaultExternalHistoryLimit = 10
	MinExternalHistoryLimit     = 1
	MaxExternalHistoryLimit     = 100
)

// DefaultExternalSettings: off until the user turns it on.
var DefaultExternalSettings = ExternalSettings{Enabled: false, HistoryLimit: DefaultExternalHistoryLimit}

func (settings ExternalSettings) withDefaults() ExternalSettings {
	if settings.HistoryLimit == 0 {
		settings.HistoryLimit = DefaultExternalHistoryLimit
	}
	settings.HistoryLimit = ClampExternalHistoryLimit(settings.HistoryLimit)
	return settings
}

// ClampExternalHistoryLimit bounds a history limit to the allowed range.
func ClampExternalHistoryLimit(limit int) int {
	return min(max(limit, MinExternalHistoryLimit), MaxExternalHistoryLimit)
}

// ExternalSettingsStore persists ExternalSettings. Implementations are safe
// for concurrent use: the socket goroutine reads them per message while the
// UI saves them from tea.Cmd goroutines.
type ExternalSettingsStore interface {
	LoadExternalSettings() (ExternalSettings, error)
	SaveExternalSettings(settings ExternalSettings) error
}

const externalSettingsKey = "extmsg:settings"

// GlobalExternalSettings keeps the settings in the global db shared by every
// project (the op-commands store's directory), so a toggle in one window
// applies to all of them.
type GlobalExternalSettings struct {
	dir string
}

func NewGlobalExternalSettings(dir string) *GlobalExternalSettings {
	return &GlobalExternalSettings{dir: dir}
}

func (g *GlobalExternalSettings) LoadExternalSettings() (ExternalSettings, error) {
	settings := DefaultExternalSettings
	err := withDBAt(nteedb.Options{Dir: g.dir}, func(db *nteedb.DB) error {
		data, found, err := db.Get(externalSettingsKey)
		if err != nil || !found {
			return err
		}
		return json.Unmarshal(data, &settings)
	})
	return settings.withDefaults(), err
}

func (g *GlobalExternalSettings) SaveExternalSettings(settings ExternalSettings) error {
	data, err := json.Marshal(settings.withDefaults())
	if err != nil {
		return err
	}
	return withDBAt(nteedb.Options{Dir: g.dir}, func(db *nteedb.DB) error {
		return db.Put(externalSettingsKey, data)
	})
}

// MemoryExternalSettings is the in-process ExternalSettingsStore (tests, and
// the fallback when the global store's directory can't be resolved).
type MemoryExternalSettings struct {
	mu       sync.Mutex
	settings ExternalSettings
}

func NewMemoryExternalSettings(settings ExternalSettings) *MemoryExternalSettings {
	return &MemoryExternalSettings{settings: settings.withDefaults()}
}

func (m *MemoryExternalSettings) LoadExternalSettings() (ExternalSettings, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settings, nil
}

func (m *MemoryExternalSettings) SaveExternalSettings(settings ExternalSettings) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.settings = settings.withDefaults()
	return nil
}

// ExternalStatus is what became of a received external message.
type ExternalStatus string

const (
	ExternalPending    ExternalStatus = "pending"    // waiting for the user to confirm
	ExternalOpened     ExternalStatus = "opened"     // an open request was carried out
	ExternalReviewed   ExternalStatus = "reviewed"   // a review request was carried out
	ExternalIgnored    ExternalStatus = "ignored"    // the user declined it
	ExternalSuperseded ExternalStatus = "superseded" // a newer request replaced it unanswered
	ExternalFailed     ExternalStatus = "error"      // it could not be carried out
)

// ExternalRecord is one received external message. Key orders records by
// arrival (it embeds the receive time), so the lowest key is the oldest.
type ExternalRecord struct {
	Key        string         `json:"key"`
	Command    string         `json:"command"`
	Args       []string       `json:"args"`
	Base       string         `json:"base"` // the revision compared with the current files; ExternalDefaultBase when none was sent
	Paths      []string       `json:"paths"`
	Raw        string         `json:"raw"`
	ReceivedAt time.Time      `json:"receivedAt"`
	Status     ExternalStatus `json:"status"`
	Note       string         `json:"note"`     // what became of the request (written with the status)
	LineNote   string         `json:"lineNote"` // open: the sender's note shown next to the line, "" = none
	Session    string         `json:"session"`  // the guided session the request is a step of, "" = none
	// PathNotes are per-file notes, one per Paths entry ("" = none): a review
	// session's files each carry the agent's note.
	PathNotes []string `json:"pathNotes"`
}

// ExternalDefaultBase is the stored base of a request that named no revision.
const ExternalDefaultBase = "HEAD"

// withDefaults fills in fields a record saved before they existed lacks: the
// base (a review's was its single argument, anything else HEAD), and one
// empty note per path.
func (record ExternalRecord) withDefaults() ExternalRecord {
	if record.Base == "" {
		record.Base = ExternalDefaultBase
		if record.Command == "review" && len(record.Args) == 1 {
			record.Base = record.Args[0]
		}
	}
	if len(record.PathNotes) < len(record.Paths) {
		notes := make([]string, len(record.Paths))
		copy(notes, record.PathNotes)
		record.PathNotes = notes
	}
	return record
}

// NewExternalRecordKey is a record key for a message received at time at.
// Zero-padded nanoseconds sort lexically in arrival order.
func NewExternalRecordKey(at time.Time) string {
	return fmt.Sprintf("%s%020d", externalRecordPrefix, at.UnixNano())
}

const (
	externalRecordPrefix = "msg:"
	externalHistoryIndex = "all"
	externalHistoryValue = "msg"
)

// ExternalHistoryStore keeps the latest external messages of one project.
// Implementations are safe for concurrent use.
type ExternalHistoryStore interface {
	// AppendExternal saves record, evicting the oldest records beyond limit.
	AppendExternal(record ExternalRecord, limit int) error
	// LoadExternal returns the saved records, newest first.
	LoadExternal() ([]ExternalRecord, error)
	UpdateExternalStatus(key string, status ExternalStatus, note string) error
	// TrimExternal deletes the oldest records beyond limit at once (a
	// lowered limit; appends trim through the cap by themselves).
	TrimExternal(limit int) error
}

// ExternalDir is the per-project directory for external messages: it holds
// the socket and the history db.
func ExternalDir(projectRoot string) (string, error) {
	return projectDir("external", projectRoot)
}

// ExternalSocketPath is the unix socket a running editor listens on for
// projectRoot.
func ExternalSocketPath(projectRoot string) (string, error) {
	dir, err := ExternalDir(projectRoot)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ntee.sock"), nil
}

// ExternalHistory is the ntee-db-backed ExternalHistoryStore. It opens the db
// per call: writes come from the socket goroutine and the UI alike, and the
// history cap is the index's MaxPerValue, which can only be set at open — so
// a changed limit takes effect on the very next write.
type ExternalHistory struct {
	dir string
}

// NewExternalHistory is the history store of the project at projectRoot.
func NewExternalHistory(projectRoot string) (*ExternalHistory, error) {
	dir, err := ExternalDir(projectRoot)
	if err != nil {
		return nil, err
	}
	return &ExternalHistory{dir: filepath.Join(dir, "history")}, nil
}

func (h *ExternalHistory) withDB(limit int, run func(db *nteedb.DB) error) error {
	return withDBAt(nteedb.Options{
		Dir: h.dir,
		// Every record shares one index value, so MaxPerValue caps the whole
		// history and evicts the lowest (oldest) key on overflow.
		Indexes: []nteedb.IndexDef{{
			Name:        externalHistoryIndex,
			Kind:        nteedb.KindString,
			MaxPerValue: ClampExternalHistoryLimit(limit),
		}},
	}, run)
}

func (h *ExternalHistory) AppendExternal(record ExternalRecord, limit int) error {
	data, err := json.Marshal(record.withDefaults())
	if err != nil {
		return err
	}
	return h.withDB(limit, func(db *nteedb.DB) error {
		return db.PutIndexed(record.Key, data, nteedb.IndexValues{externalHistoryIndex: externalHistoryValue})
	})
}

func (h *ExternalHistory) LoadExternal() ([]ExternalRecord, error) {
	var records []ExternalRecord
	err := h.withDB(MaxExternalHistoryLimit, func(db *nteedb.DB) error {
		keys, err := db.PrefixScan(externalRecordPrefix)
		if err != nil || len(keys) == 0 {
			return err
		}
		values, found, err := db.GetMany(keys)
		if err != nil {
			return err
		}
		for index := range keys {
			var record ExternalRecord
			if found[index] && json.Unmarshal(values[index], &record) == nil {
				records = append(records, record.withDefaults())
			}
		}
		return nil
	})
	sortExternalNewestFirst(records)
	return records, err
}

func (h *ExternalHistory) UpdateExternalStatus(key string, status ExternalStatus, note string) error {
	return h.withDB(MaxExternalHistoryLimit, func(db *nteedb.DB) error {
		data, found, err := db.Get(key)
		if err != nil || !found {
			return err
		}
		var record ExternalRecord
		if err := json.Unmarshal(data, &record); err != nil {
			return err
		}
		record.Status, record.Note = status, note
		updated, err := json.Marshal(record.withDefaults())
		if err != nil {
			return err
		}
		// Overwriting an existing key never evicts, so the cap passed here
		// doesn't matter.
		return db.PutIndexed(key, updated, nteedb.IndexValues{externalHistoryIndex: externalHistoryValue})
	})
}

func (h *ExternalHistory) TrimExternal(limit int) error {
	limit = ClampExternalHistoryLimit(limit)
	return h.withDB(limit, func(db *nteedb.DB) error {
		keys, err := db.PrefixScan(externalRecordPrefix)
		if err != nil {
			return err
		}
		for _, key := range keys[:max(0, len(keys)-limit)] {
			if err := db.Delete(key); err != nil {
				return err
			}
		}
		return nil
	})
}

// MemoryExternalHistory is the in-process ExternalHistoryStore (tests, and
// the fallback when the history directory can't be resolved).
type MemoryExternalHistory struct {
	mu      sync.Mutex
	records map[string]ExternalRecord
}

func NewMemoryExternalHistory() *MemoryExternalHistory {
	return &MemoryExternalHistory{records: map[string]ExternalRecord{}}
}

func (m *MemoryExternalHistory) AppendExternal(record ExternalRecord, limit int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records[record.Key] = record.withDefaults()
	m.trimLocked(limit)
	return nil
}

func (m *MemoryExternalHistory) LoadExternal() ([]ExternalRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	records := make([]ExternalRecord, 0, len(m.records))
	for _, record := range m.records {
		records = append(records, record.withDefaults())
	}
	sortExternalNewestFirst(records)
	return records, nil
}

func (m *MemoryExternalHistory) UpdateExternalStatus(key string, status ExternalStatus, note string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if record, ok := m.records[key]; ok {
		record.Status, record.Note = status, note
		m.records[key] = record
	}
	return nil
}

func (m *MemoryExternalHistory) TrimExternal(limit int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.trimLocked(limit)
	return nil
}

func (m *MemoryExternalHistory) trimLocked(limit int) {
	keys := make([]string, 0, len(m.records))
	for key := range m.records {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys[:max(0, len(keys)-ClampExternalHistoryLimit(limit))] {
		delete(m.records, key)
	}
}

func sortExternalNewestFirst(records []ExternalRecord) {
	sort.Slice(records, func(a, b int) bool { return strings.Compare(records[a].Key, records[b].Key) > 0 })
}
