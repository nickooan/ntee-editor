package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
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
	// AgentEditPolicy is what happens when an agent ends an edit session:
	// AgentEditPolicyReview opens the staged edits for the user to apply or
	// discard; AgentEditPolicyApply writes them all at once.
	AgentEditPolicy string `json:"agentEditPolicy"`
	// AgentEditKeep is how many finished agent edit sessions each project
	// keeps for tracing back.
	AgentEditKeep int `json:"agentEditKeep"`
	// SessionAutoStart starts an agent's session (review, note or diff
	// session, or staged edits to review) at once with a notice, instead of
	// asking the user first.
	SessionAutoStart bool `json:"sessionAutoStart"`
}

const (
	DefaultExternalHistoryLimit = 10
	MinExternalHistoryLimit     = 1
	MaxExternalHistoryLimit     = 100

	AgentEditPolicyReview = "review"
	AgentEditPolicyApply  = "apply"

	DefaultAgentEditKeep = 20
	MinAgentEditKeep     = 1
	MaxAgentEditKeep     = 100
)

// DefaultExternalSettings: off until the user turns it on; agent edits wait
// for the user's review; an agent's session asks before it starts.
var DefaultExternalSettings = ExternalSettings{
	Enabled:          false,
	HistoryLimit:     DefaultExternalHistoryLimit,
	AgentEditPolicy:  AgentEditPolicyReview,
	AgentEditKeep:    DefaultAgentEditKeep,
	SessionAutoStart: false,
}

func (settings ExternalSettings) withDefaults() ExternalSettings {
	if settings.HistoryLimit == 0 {
		settings.HistoryLimit = DefaultExternalHistoryLimit
	}
	settings.HistoryLimit = ClampExternalHistoryLimit(settings.HistoryLimit)
	if settings.AgentEditPolicy != AgentEditPolicyApply {
		settings.AgentEditPolicy = AgentEditPolicyReview
	}
	if settings.AgentEditKeep == 0 {
		settings.AgentEditKeep = DefaultAgentEditKeep
	}
	settings.AgentEditKeep = ClampAgentEditKeep(settings.AgentEditKeep)
	return settings
}

// ClampAgentEditKeep bounds the finished agent edit sessions kept.
func ClampAgentEditKeep(keep int) int {
	return min(max(keep, MinAgentEditKeep), MaxAgentEditKeep)
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

// Connections reports the global db's connection, opened per call: every
// window of every project shares the db, and ntee-db lets one process at a
// time hold it.
func (g *GlobalExternalSettings) Connections() []ConnectionStatus {
	return []ConnectionStatus{connectionStatusAt(g.dir, "global", ConnectionPerCall)}
}

// nteeDBMainLog is ntee-db's main log file: every write appends to it.
const nteeDBMainLog = "main.jsonl"

// changeStamp is the global db's main log size and modification time. ok is
// false when it can't be read; a missing log (nothing written yet) is a
// valid, empty stamp.
func (g *GlobalExternalSettings) changeStamp() (stamp fileStamp, ok bool) {
	info, err := os.Stat(filepath.Join(g.dir, nteeDBMainLog))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fileStamp{}, true
	case err != nil:
		return fileStamp{}, false
	}
	return fileStamp{size: info.Size(), modified: info.ModTime()}, true
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

// ExternalHistory is the ntee-db-backed ExternalHistoryStore. Its connection
// is a lease that stays open through a burst of requests and closes once
// they stop. The history cap is the index's MaxPerValue, which can only be
// set at open: the connection reopens when a write brings a new limit, so it
// takes effect on that write. Reads and status updates pass the limit last
// given, so they never reopen it.
type ExternalHistory struct {
	dir string

	limitMu sync.Mutex
	limit   int // the limit last given; 0 is none yet
}

// NewExternalHistory is the history store of the project at projectRoot.
func NewExternalHistory(projectRoot string) (*ExternalHistory, error) {
	dir, err := ExternalDir(projectRoot)
	if err != nil {
		return nil, err
	}
	return &ExternalHistory{dir: filepath.Join(dir, "history")}, nil
}

// Connections reports the history's connection.
func (h *ExternalHistory) Connections() []ConnectionStatus {
	return []ConnectionStatus{connectionStatusAt(h.dir, "message history", ConnectionSessionLease)}
}

// rememberLimit keeps limit as the cap later reads open with.
func (h *ExternalHistory) rememberLimit(limit int) int {
	limit = ClampExternalHistoryLimit(limit)
	h.limitMu.Lock()
	h.limit = limit
	h.limitMu.Unlock()
	return limit
}

func (h *ExternalHistory) lastLimit() int {
	h.limitMu.Lock()
	defer h.limitMu.Unlock()
	if h.limit == 0 {
		return MaxExternalHistoryLimit
	}
	return h.limit
}

func (h *ExternalHistory) withDB(limit int, run func(db *nteedb.DB) error) error {
	return leaseDBAt(nteedb.Options{
		Dir: h.dir,
		// Every record shares one index value, so MaxPerValue caps the whole
		// history and evicts the lowest (oldest) key on overflow.
		Indexes: []nteedb.IndexDef{{
			Name:        externalHistoryIndex,
			Kind:        nteedb.KindString,
			MaxPerValue: ClampExternalHistoryLimit(limit),
		}},
	}, connectionUse{}, run)
}

func (h *ExternalHistory) AppendExternal(record ExternalRecord, limit int) error {
	data, err := json.Marshal(record.withDefaults())
	if err != nil {
		return err
	}
	return h.withDB(h.rememberLimit(limit), func(db *nteedb.DB) error {
		return db.PutIndexed(record.Key, data, nteedb.IndexValues{externalHistoryIndex: externalHistoryValue})
	})
}

func (h *ExternalHistory) LoadExternal() ([]ExternalRecord, error) {
	var records []ExternalRecord
	err := h.withDB(h.lastLimit(), func(db *nteedb.DB) error {
		var err error
		records, err = decodePrefix[ExternalRecord](db, externalRecordPrefix)
		for index := range records {
			records[index] = records[index].withDefaults()
		}
		return err
	})
	sortExternalNewestFirst(records)
	return records, err
}

func (h *ExternalHistory) UpdateExternalStatus(key string, status ExternalStatus, note string) error {
	return h.withDB(h.lastLimit(), func(db *nteedb.DB) error {
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
	limit = h.rememberLimit(limit)
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

const (
	// externalSettingsCacheAge is how long CachedExternalSettings answers
	// from memory when it can't tell whether the db changed.
	externalSettingsCacheAge = 2 * time.Second
	// externalSettingsStampedAge bounds a cache the db's change stamp vouches
	// for — a safety net for a change the stamp could miss.
	externalSettingsStampedAge = 60 * time.Second
)

// fileStamp is a file's size and modification time.
type fileStamp struct {
	size     int64
	modified time.Time
}

// changeStamper is a settings store that can tell, with one stat, whether
// its db may have changed.
type changeStamper interface {
	changeStamp() (fileStamp, bool)
}

// CachedExternalSettings answers loads from memory: every socket request
// reads the settings, and the global db is opened (with its lock retries)
// per call. Over a db it can stamp, it reloads only once the db's main log
// changed, so another window's toggle shows on the very next load; otherwise
// it reloads after maxAge. Saves go straight through and refresh the cache.
type CachedExternalSettings struct {
	inner      ExternalSettingsStore
	maxAge     time.Duration
	stampedAge time.Duration
	mu         sync.Mutex
	cached     ExternalSettings
	loadedAt   time.Time
	stamp      fileStamp
	stamped    bool
	valid      bool
}

func NewCachedExternalSettings(inner ExternalSettingsStore) *CachedExternalSettings {
	return &CachedExternalSettings{inner: inner, maxAge: externalSettingsCacheAge, stampedAge: externalSettingsStampedAge}
}

func (c *CachedExternalSettings) LoadExternalSettings() (ExternalSettings, error) {
	// Stamped before the load: a write landing in between shows as a changed
	// stamp next time, never as a stale cache.
	stamp, stamped := c.currentStamp()
	c.mu.Lock()
	if c.fresh(stamp, stamped) {
		settings := c.cached
		c.mu.Unlock()
		return settings, nil
	}
	c.mu.Unlock()
	settings, err := c.inner.LoadExternalSettings()
	if err != nil {
		return settings, err
	}
	c.remember(settings, stamp, stamped)
	return settings, nil
}

func (c *CachedExternalSettings) currentStamp() (fileStamp, bool) {
	if stamper, ok := c.inner.(changeStamper); ok {
		return stamper.changeStamp()
	}
	return fileStamp{}, false
}

func (c *CachedExternalSettings) fresh(stamp fileStamp, stamped bool) bool {
	age := time.Since(c.loadedAt)
	switch {
	case !c.valid:
		return false
	case stamped && c.stamped:
		return stamp.size == c.stamp.size && stamp.modified.Equal(c.stamp.modified) && age < c.stampedAge
	default:
		return age < c.maxAge
	}
}

// Connections reports the db under the cache, with whether the cache is
// fresh.
func (c *CachedExternalSettings) Connections() []ConnectionStatus {
	reporter, ok := c.inner.(ConnectionReporter)
	if !ok {
		return nil
	}
	statuses := reporter.Connections()
	stamp, stamped := c.currentStamp()
	c.mu.Lock()
	fresh := c.fresh(stamp, stamped)
	c.mu.Unlock()
	note := "settings cache stale — reloads on the next request"
	if fresh {
		note = "settings cache fresh"
	}
	for index := range statuses {
		statuses[index].Note = note
	}
	return statuses
}

func (c *CachedExternalSettings) SaveExternalSettings(settings ExternalSettings) error {
	if err := c.inner.SaveExternalSettings(settings); err != nil {
		c.mu.Lock()
		c.valid = false
		c.mu.Unlock()
		return err
	}
	stamp, stamped := c.currentStamp()
	c.remember(settings.withDefaults(), stamp, stamped) // what a load of it would answer
	return nil
}

func (c *CachedExternalSettings) remember(settings ExternalSettings, stamp fileStamp, stamped bool) {
	c.mu.Lock()
	c.cached, c.loadedAt, c.valid = settings, time.Now(), true
	c.stamp, c.stamped = stamp, stamped
	c.mu.Unlock()
}
