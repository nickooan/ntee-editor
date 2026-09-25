package store

import "strings"

// Scoped returns a view of inner for a nested repo the editor is rooted at
// (Ctrl+W). The editor sees repo-relative paths while rooted there, but file
// records stay keyed by their workspace-relative path: every path argument is
// prefixed with "repo/" on the way in and stripped on the way out. That keeps
// one history per file — undo snapshots, drafts, and recents are the same
// whether the file is reached from the workspace or from inside its repo, and
// records written before the repo was ever selected show up automatically.
//
// Tabs and the search index are whole-root singletons, so they live under the
// repo's own keys instead (SaveTabsFor/SaveCorpusFor). Session, maintenance,
// and Close pass straight through: the app keeps the session at workspace
// level, and the view owns no resources of its own.
//
// repo "" returns inner unchanged.
func Scoped(inner Backend, repo string) Backend {
	repo = strings.Trim(repo, "/")
	if repo == "" {
		return inner
	}
	return &scopedBackend{Backend: inner, repo: repo, prefix: repo + "/"}
}

type scopedBackend struct {
	Backend // pass-through for session, maintenance, snapshot delete, Close
	repo    string
	prefix  string
}

func (s *scopedBackend) wrap(path string) string { return s.prefix + path }

func (s *scopedBackend) unwrap(path string) string { return strings.TrimPrefix(path, s.prefix) }

func (s *scopedBackend) TouchOpened(f OpenedFile) error {
	f.Path = s.wrap(f.Path)
	return s.Backend.TouchOpened(f)
}

// RecentFiles filters the workspace's recents to this repo. The limit applies
// after filtering, so the inner list is read whole (it is bounded by the
// project's file count, and the store sorts in memory anyway).
func (s *scopedBackend) RecentFiles(limit int) []OpenedFile {
	all := s.Backend.RecentFiles(0)
	out := make([]OpenedFile, 0, len(all))
	for _, f := range all {
		if !strings.HasPrefix(f.Path, s.prefix) {
			continue
		}
		f.Path = s.unwrap(f.Path)
		out = append(out, f)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out
}

func (s *scopedBackend) DeleteOpenedUnder(rel string) error {
	return s.Backend.DeleteOpenedUnder(s.wrap(rel))
}

func (s *scopedBackend) SnapshotPut(path string, seq int64, kind, content string) error {
	return s.Backend.SnapshotPut(s.wrap(path), seq, kind, content)
}

func (s *scopedBackend) SnapshotGet(seq int64) (Snapshot, bool) {
	snap, ok := s.Backend.SnapshotGet(seq)
	if ok {
		snap.Path = s.unwrap(snap.Path)
	}
	return snap, ok
}

func (s *scopedBackend) LastSave(path string) (Snapshot, bool) {
	snap, ok := s.Backend.LastSave(s.wrap(path))
	if ok {
		snap.Path = s.unwrap(snap.Path)
	}
	return snap, ok
}

func (s *scopedBackend) SaveDraft(d Draft) error {
	d.Path = s.wrap(d.Path)
	return s.Backend.SaveDraft(d)
}

func (s *scopedBackend) LoadDraft(path string) (Draft, bool) {
	d, ok := s.Backend.LoadDraft(s.wrap(path))
	if ok {
		d.Path = s.unwrap(d.Path)
	}
	return d, ok
}

func (s *scopedBackend) DeleteDraft(path string) error {
	return s.Backend.DeleteDraft(s.wrap(path))
}

func (s *scopedBackend) SaveTabs(t Tabs) error { return s.Backend.SaveTabsFor(s.repo, t) }

func (s *scopedBackend) LoadTabs() (Tabs, bool) { return s.Backend.LoadTabsFor(s.repo) }

func (s *scopedBackend) SaveCorpus(c CorpusIndex) error {
	return s.Backend.SaveCorpusFor(s.repo, c)
}

func (s *scopedBackend) LoadCorpus() (CorpusIndex, bool) { return s.Backend.LoadCorpusFor(s.repo) }
