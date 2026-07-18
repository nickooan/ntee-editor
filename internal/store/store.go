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

// Backend is the persistence surface the app depends on. Store (ntee-db) and
// Memory (fallback when the store's flock is held) both satisfy it.
type Backend interface {
	TouchOpened(f OpenedFile) error
	RecentFiles(limit int) []OpenedFile
	SnapshotPut(path string, seq int64, kind, content string) error
	SnapshotGet(seq int64) (Snapshot, bool)
	SnapshotDelete(seqs []int64)
	LastSave(path string) (Snapshot, bool)
	SaveSession(s Session) error
	LoadSession() (Session, bool)
	Close() error
}

const (
	openedPrefix  = "opened:"
	versionPrefix = "versions:"
	sessionKey    = "session:current"
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
