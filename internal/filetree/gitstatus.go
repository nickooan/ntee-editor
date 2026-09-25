package filetree

import (
	"path/filepath"
	"strings"

	"github.com/nickooan/ntee-editor/internal/gitcmd"
)

// GitDirtySet reports the working tree's uncommitted paths as a set for O(1)
// tree lookups: every dirty file (modified, staged, or untracked) plus every
// ancestor directory of one, so a collapsed directory can reflect changes
// buried anywhere beneath it. ok is false when root is not a git repository or
// git is unavailable/fails — callers treat that as "feature off" (nil set).
//
// Detection shells out to `git status --porcelain -z` (the VS Code approach):
// one short-lived child process per call, no daemon, and git's own status
// semantics rather than a reimplementation. Callers are expected to run this
// off the UI goroutine.
func GitDirtySet(root string) (map[string]bool, bool) {
	if !IsGitRepo(root) {
		return nil, false
	}
	// --untracked-files=all lists every file inside an untracked directory
	// individually (instead of one collapsed "dir/" record), so consumers like
	// the Ctrl+U uncommitted-files finder see openable file paths.
	out, err := gitcmd.Out(root, "status", "--porcelain", "-z", "--untracked-files=all")
	if err != nil {
		return nil, false
	}
	dirty := map[string]bool{}
	for _, p := range parsePorcelain(out) {
		markDirty(dirty, p)
	}
	return dirty, true
}

// MergeRepoDirty unions GitDirtySet for each root-relative repo. Paths are
// rewritten to be relative to root (so the sidebar can look them up), and
// ancestors above each repo are marked too — a collapsed parent of a repo
// still renders dirty. ok is false only when every repo fails or repos is
// empty. Call it off the UI goroutine.
func MergeRepoDirty(root string, repos []string) (map[string]bool, bool) {
	if len(repos) == 0 {
		return nil, false
	}
	merged := map[string]bool{}
	anyOK := false
	for _, rel := range repos {
		rel = strings.Trim(filepath.ToSlash(rel), "/")
		if rel == "" || rel == "." {
			continue
		}
		dirty, ok := GitDirtySet(filepath.Join(root, filepath.FromSlash(rel)))
		if !ok {
			continue
		}
		anyOK = true
		for path := range dirty {
			full := rel
			if path != "" {
				full = rel + "/" + path
			}
			markDirty(merged, full)
		}
	}
	if !anyOK {
		return nil, false
	}
	return merged, true
}

// parsePorcelain extracts the repo-relative paths from `git status --porcelain
// -z` output: NUL-separated records of "XY path", where rename/copy records
// (R/C in the two-letter code) are followed by one extra NUL-separated origin
// path — both sides are reported (the origin's directory lost a file, which is
// itself an uncommitted change). An untracked directory arrives as "?? dir/";
// the trailing slash is trimmed. Pure function, unit-tested without git.
func parsePorcelain(out []byte) []string {
	var paths []string
	records := strings.Split(string(out), "\x00")
	for i := 0; i < len(records); i++ {
		rec := records[i]
		if len(rec) < 4 || rec[2] != ' ' {
			continue // trailing empty record or malformed
		}
		code, path := rec[:2], rec[3:]
		paths = append(paths, strings.TrimSuffix(path, "/"))
		if strings.ContainsAny(code, "RC") && i+1 < len(records) && records[i+1] != "" {
			i++
			paths = append(paths, strings.TrimSuffix(records[i], "/"))
		}
	}
	return paths
}

// markDirty inserts a slash-separated repo-relative path and all its ancestor
// directories into set — the pre-marking that lets a folded directory render
// dirty without any walk-time aggregation.
func markDirty(set map[string]bool, path string) {
	if path == "" {
		return
	}
	set[path] = true
	for i, r := range path {
		if r == '/' {
			set[path[:i]] = true
		}
	}
}
