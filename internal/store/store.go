// Package store persists editor state in ntee-db: recently opened files, edit
// snapshots (the undo timeline's content), and the per-project session. The
// store lives under ~/.ntee-editor/stores/<hash(projectRoot)>/ — per-project so
// ntee-db's single-writer flock only clashes when the same project is opened
// twice (Backend falls back to Memory then).
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	nteedb "github.com/nickooan/ntee-db/nteedb-core"
)

// OpenedFile records one file the user opened, for the recents list.
type OpenedFile struct {
	Path         string `json:"path"` // relative to the project root
	LastOpenedAt int64  `json:"lastOpenedAt"`
	CursorLine   int    `json:"cursorLine"`
	ScrollY      int    `json:"scrollY"`
}

// Snapshot is one full-content undo checkpoint.
type Snapshot struct {
	Path    string `json:"path"` // relative to the project root
	Seq     int64  `json:"seq"`
	Kind    string `json:"kind"` // "edit" | "save"
	Content string `json:"content"`
	At      int64  `json:"at"`
}

// Session is the state restored on relaunch. Command is the confirmed query
// path (drives sidebar expansion). Expanded/TreeIndex are legacy fields from
// the cursor-tree UI, kept so old session records still parse.
type Session struct {
	LastFile  string   `json:"lastFile"`
	Command   string   `json:"command"`
	Expanded  []string `json:"expanded,omitempty"`
	TreeIndex int      `json:"treeIndex,omitempty"`
}

// DraftStep is one undo checkpoint carried inside a Draft.
type DraftStep struct {
	Kind    string `json:"kind"` // "edit" | "save"
	Content string `json:"content"`
}

// Draft is a file's unsaved edit state, stashed when the user switches away and
// restored on reopen. Steps (oldest→newest, capped app-side) carry the undo
// history inline so a draft is self-contained — versions: records can be
// evicted by the file index's MaxPerValue, drafts must not be.
type Draft struct {
	Path    string      `json:"path"` // relative to the project root
	Content string      `json:"content"`
	Cx      int         `json:"cx"`
	Cy      int         `json:"cy"`
	ScrollY int         `json:"scrollY"`
	Steps   []DraftStep `json:"steps"`
	At      int64       `json:"at"`
}

// TabCursor is a tab's last cursor position (rune line/column).
type TabCursor struct {
	Cy int `json:"cy"`
	Cx int `json:"cx"`
}

// Tabs is the persisted open-tab list. Cursors remembers each tab's last cursor
// so revisiting a tab restores the position (keyed by root-relative path).
type Tabs struct {
	Paths   []string             `json:"paths"` // relative to the project root, display order
	Active  int                  `json:"active"`
	Cursors map[string]TabCursor `json:"cursors,omitempty"`
}

// CorpusVersion is bumped when the CorpusIndex format changes, so a stale
// persisted index is discarded instead of misread.
const CorpusVersion = 1

// CorpusIndex is the persisted search corpus plus a directory-mtime signature.
// On boot the app stat-sweeps DirMtimes; if every directory still matches, the
// cached Files are reused and the full walk is skipped. Stored as a singleton
// (one record per project store), so it self-caps at one — no eviction needed.
type CorpusIndex struct {
	Version   int              `json:"version"`
	Files     []string         `json:"files"`     // relative paths, the pruned corpus
	DirMtimes map[string]int64 `json:"dirMtimes"` // rel dir path ("" = root) → mtime unix-nano
	Truncated bool             `json:"truncated"` // the walk hit MaxIndexFiles
}

// Backend is the persistence surface the app depends on. Store (ntee-db) and
// Memory (fallback when the store's flock is held) both satisfy it.
type Backend interface {
	TouchOpened(f OpenedFile) error
	RecentFiles(limit int) []OpenedFile
	DeleteOpenedUnder(rel string) error
	SnapshotPut(path string, seq int64, kind, content string) error
	SnapshotGet(seq int64) (Snapshot, bool)
	SnapshotDelete(seqs []int64)
	LastSave(path string) (Snapshot, bool)
	SaveSession(s Session) error
	LoadSession() (Session, bool)
	SaveDraft(d Draft) error
	LoadDraft(path string) (Draft, bool)
	DeleteDraft(path string) error
	SaveTabs(t Tabs) error
	LoadTabs() (Tabs, bool)
	SaveCorpus(c CorpusIndex) error
	LoadCorpus() (CorpusIndex, bool)
	Close() error
}

const (
	openedPrefix  = "opened:"
	versionPrefix = "versions:"
	draftPrefix   = "draft:"
	sessionKey    = "session:current"
	tabsKey       = "tabs:current"
	corpusKey     = "corpus:current"
)

func versionKey(seq int64) string { return fmt.Sprintf("%s%016d", versionPrefix, seq) }

// Store is the ntee-db-backed Backend.
type Store struct {
	db *nteedb.DB
}

// Dir returns the store directory for a project root.
func Dir(projectRoot string) (string, error) {
	abs, err := filepath.Abs(projectRoot)
	if err != nil {
		return "", err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(abs))
	return filepath.Join(home, ".ntee-editor", "stores", hex.EncodeToString(sum[:])[:16]), nil
}

// Open opens (creating if needed) the project's store. maxSnapshotsPerFile caps
// the per-file version history via the secondary index's MaxPerValue eviction.
func Open(projectRoot string, maxSnapshotsPerFile int) (*Store, error) {
	dir, err := Dir(projectRoot)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	db, err := nteedb.Open(nteedb.Options{
		Dir: dir,
		Indexes: []nteedb.IndexDef{
			{Name: "file", Kind: nteedb.KindString, MaxPerValue: maxSnapshotsPerFile},
		},
	})
	if err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) TouchOpened(f OpenedFile) error {
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return s.db.Put(openedPrefix+f.Path, data)
}

// RecentFiles returns opened-file records, most recent first. It prefix-scans
// rather than relying on secondary-index recency semantics: the record count is
// bounded by the project's file count, and sorting in memory is unambiguous.
func (s *Store) RecentFiles(limit int) []OpenedFile {
	keys, err := s.db.PrefixScan(openedPrefix)
	if err != nil || len(keys) == 0 {
		return nil
	}
	values, found, err := s.db.GetMany(keys)
	if err != nil {
		return nil
	}
	out := make([]OpenedFile, 0, len(keys))
	for i := range keys {
		if !found[i] {
			continue
		}
		var f OpenedFile
		if json.Unmarshal(values[i], &f) == nil {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].LastOpenedAt > out[b].LastOpenedAt })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// DeleteOpenedUnder drops the recent-visit records for rel and everything
// beneath it — called when a file or directory is removed from disk, so the
// dead paths don't linger in the store. The scan prefix alone is not enough:
// "lib" must not also match "library/…", hence the path-boundary filter.
func (s *Store) DeleteOpenedUnder(rel string) error {
	keys, err := s.db.PrefixScan(openedPrefix + rel)
	if err != nil {
		return err
	}
	exact, dirPrefix := openedPrefix+rel, openedPrefix+rel+"/"
	for _, k := range keys {
		if k == exact || strings.HasPrefix(k, dirPrefix) {
			_ = s.db.Delete(k)
		}
	}
	return nil
}

func (s *Store) SnapshotPut(path string, seq int64, kind, content string) error {
	snap := Snapshot{Path: path, Seq: seq, Kind: kind, Content: content, At: seq}
	data, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	return s.db.PutIndexed(versionKey(seq), data, nteedb.IndexValues{"file": path})
}

func (s *Store) SnapshotGet(seq int64) (Snapshot, bool) {
	data, ok, err := s.db.Get(versionKey(seq))
	if err != nil || !ok {
		return Snapshot{}, false
	}
	var snap Snapshot
	if json.Unmarshal(data, &snap) != nil {
		return Snapshot{}, false
	}
	return snap, true
}

func (s *Store) SnapshotDelete(seqs []int64) {
	for _, seq := range seqs {
		_ = s.db.Delete(versionKey(seq))
	}
}

// LastSave returns the newest snapshot of path with Kind "save" — powers :revert.
func (s *Store) LastSave(path string) (Snapshot, bool) {
	keys, err := s.db.ByIndex("file", path, -1000) // newest-first
	if err != nil {
		return Snapshot{}, false
	}
	for _, key := range keys {
		data, ok, err := s.db.Get(key)
		if err != nil || !ok {
			continue
		}
		var snap Snapshot
		if json.Unmarshal(data, &snap) == nil && snap.Kind == "save" {
			return snap, true
		}
	}
	return Snapshot{}, false
}

func (s *Store) SaveSession(sess Session) error {
	data, err := json.Marshal(sess)
	if err != nil {
		return err
	}
	return s.db.Put(sessionKey, data)
}

func (s *Store) LoadSession() (Session, bool) {
	data, ok, err := s.db.Get(sessionKey)
	if err != nil || !ok {
		return Session{}, false
	}
	var sess Session
	if json.Unmarshal(data, &sess) != nil {
		return Session{}, false
	}
	return sess, true
}

// Drafts use plain (non-indexed) keys on purpose: the file index's MaxPerValue
// eviction must never delete a stashed draft.
func (s *Store) SaveDraft(d Draft) error {
	data, err := json.Marshal(d)
	if err != nil {
		return err
	}
	return s.db.Put(draftPrefix+d.Path, data)
}

func (s *Store) LoadDraft(path string) (Draft, bool) {
	data, ok, err := s.db.Get(draftPrefix + path)
	if err != nil || !ok {
		return Draft{}, false
	}
	var d Draft
	if json.Unmarshal(data, &d) != nil {
		return Draft{}, false
	}
	return d, true
}

func (s *Store) DeleteDraft(path string) error {
	return s.db.Delete(draftPrefix + path)
}

func (s *Store) SaveTabs(t Tabs) error {
	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return s.db.Put(tabsKey, data)
}

func (s *Store) LoadTabs() (Tabs, bool) {
	data, ok, err := s.db.Get(tabsKey)
	if err != nil || !ok {
		return Tabs{}, false
	}
	var t Tabs
	if json.Unmarshal(data, &t) != nil {
		return Tabs{}, false
	}
	return t, true
}

// Corpus is a singleton (fixed key): each SaveCorpus overwrites the previous,
// so the store holds exactly one index. A large Files/DirMtimes JSON (≥64 KiB)
// auto-offloads to ntee-db's blob side-file, out of the heap.
func (s *Store) SaveCorpus(c CorpusIndex) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return s.db.Put(corpusKey, data)
}

func (s *Store) LoadCorpus() (CorpusIndex, bool) {
	data, ok, err := s.db.Get(corpusKey)
	if err != nil || !ok {
		return CorpusIndex{}, false
	}
	var c CorpusIndex
	if json.Unmarshal(data, &c) != nil {
		return CorpusIndex{}, false
	}
	return c, true
}
