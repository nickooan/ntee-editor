package store

import "sort"

// Memory is the in-process fallback Backend used when the project's ntee-db
// store cannot be opened (typically: another editor instance holds the flock).
// Undo still works for the session; nothing survives exit.
type Memory struct {
	opened    map[string]OpenedFile
	snapshots map[int64]Snapshot
	session   *Session
}

func NewMemory() *Memory {
	return &Memory{
		opened:    map[string]OpenedFile{},
		snapshots: map[int64]Snapshot{},
	}
}

func (m *Memory) Close() error { return nil }

func (m *Memory) TouchOpened(f OpenedFile) error {
	m.opened[f.Path] = f
	return nil
}

func (m *Memory) RecentFiles(limit int) []OpenedFile {
	out := make([]OpenedFile, 0, len(m.opened))
	for _, f := range m.opened {
		out = append(out, f)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].LastOpenedAt > out[b].LastOpenedAt })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (m *Memory) SnapshotPut(path string, seq int64, kind, content string) error {
	m.snapshots[seq] = Snapshot{Path: path, Seq: seq, Kind: kind, Content: content, At: seq}
	return nil
}

func (m *Memory) SnapshotGet(seq int64) (Snapshot, bool) {
	snap, ok := m.snapshots[seq]
	return snap, ok
}

func (m *Memory) SnapshotDelete(seqs []int64) {
	for _, seq := range seqs {
		delete(m.snapshots, seq)
	}
}

func (m *Memory) LastSave(path string) (Snapshot, bool) {
	var best Snapshot
	found := false
	for _, snap := range m.snapshots {
		if snap.Path == path && snap.Kind == "save" && (!found || snap.Seq > best.Seq) {
			best, found = snap, true
		}
	}
	return best, found
}

func (m *Memory) SaveSession(s Session) error {
	m.session = &s
	return nil
}

func (m *Memory) LoadSession() (Session, bool) {
	if m.session == nil {
		return Session{}, false
	}
	return *m.session, true
}
