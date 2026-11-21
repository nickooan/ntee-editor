package staging

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nickooan/ntee-editor/internal/filetree"
	"github.com/nickooan/ntee-editor/internal/store"
)

// maxSandboxCopyBytes caps a sandbox built by copying (no copy-on-write
// clone): past it the copy gives up rather than fill the disk.
var maxSandboxCopyBytes int64 = 2 << 30

// sandboxTrashPrefix names a sandbox being deleted: renamed out of the way
// at once, removed in the background, swept at the next start if that was
// cut short.
const sandboxTrashPrefix = ".trash-"

// SandboxResult is what Sandbox built: the box (which mirrors the
// workspace), the units copied into it, how many staged files were put in
// place, and when it was first built.
type SandboxResult struct {
	Path    string
	Units   []string // workspace-relative repos (or top-level folders) in the box; "." is the whole workspace
	Placed  int
	BuiltAt time.Time
}

// sandboxState is one session's box. mu serializes building it, apart from
// the session's saveMu, so edits never wait on a clone.
type sandboxState struct {
	mu      sync.Mutex
	units   map[string]bool
	builtAt time.Time
}

// WithSandboxDir sets where sessions' sandboxes live; without it Sandbox is
// refused.
func (r *Registry) WithSandboxDir(dir string) *Registry {
	r.sandboxDir = dir
	return r
}

// Sandbox builds or refreshes the session's sandbox: a copy of every repo
// the session's staged files are in (plus include, workspace-relative), laid
// out like the workspace, with the staged contents in place. fresh rebuilds
// it from the disk as it is now. It touches the disk: never call it on the
// UI goroutine.
func (r *Registry) Sandbox(session string, fresh bool, include []string) (SandboxResult, error) {
	if r.sandboxDir == "" {
		return SandboxResult{}, errors.New("this editor keeps no sandboxes")
	}
	state := r.sandboxStateFor(session)
	state.mu.Lock()
	defer state.mu.Unlock()
	record, ok := r.Snapshot(session)
	switch {
	case !ok:
		return SandboxResult{}, r.noSession(session)
	case len(record.Files) == 0:
		return SandboxResult{}, fmt.Errorf("edit session %s has no staged files yet", session)
	case record.Phase() == store.PhaseEditing:
		// Built from complete edits only: an agent still editing would leave
		// half its change in the copy another agent is testing.
		return SandboxResult{}, fmt.Errorf("edit session %s is still being edited (by %s) — its sandbox is built once every agent has sent end", session, AgentNames(record.WithAgents().EditingAgents()))
	}
	box := r.sandboxPath(session)
	if fresh {
		r.trashSandbox(box)
		state.units, state.builtAt = map[string]bool{}, time.Time{}
	}

	var units []string
	for _, file := range record.Files {
		units = append(units, r.repoOf(file.Rel))
	}
	for _, extra := range include {
		extra = path.Clean(filepath.ToSlash(extra))
		info, err := os.Stat(filepath.Join(r.workspaceRoot, filepath.FromSlash(extra)))
		if err != nil || !filetree.IsContained(r.workspaceRoot, extra) {
			return SandboxResult{}, fmt.Errorf("cannot include %s: not a folder or file in the project", extra)
		}
		if info.IsDir() || extra != "." {
			units = append(units, extra)
		}
	}
	units = outermostUnits(units)
	for _, unit := range units {
		if state.units[unit] {
			continue
		}
		for existing := range state.units {
			if unitContains(unit, existing) {
				return SandboxResult{}, fmt.Errorf("%s holds %s, already in the sandbox — rebuild it with fresh to take the whole of %s", unitName(unit), unitName(existing), unitName(unit))
			}
		}
		if err := r.copyUnit(box, unit); err != nil {
			return SandboxResult{}, err
		}
		state.units[unit] = true
		if state.builtAt.IsZero() {
			state.builtAt = r.now()
		}
	}

	placed := 0
	for _, file := range record.Files {
		if file.Result != "" {
			continue // already written with a stacked session: the copy has it
		}
		if err := placeFile(filepath.Join(box, filepath.FromSlash(file.Rel)), file.Content); err != nil {
			return SandboxResult{}, fmt.Errorf("cannot place %s in the sandbox: %w", file.Rel, err)
		}
		placed++
	}
	all := make([]string, 0, len(state.units))
	for unit := range state.units {
		all = append(all, unit)
	}
	slices.Sort(all)
	return SandboxResult{Path: box, Units: all, Placed: placed, BuiltAt: state.builtAt}, nil
}

// placeFile puts a staged file's content in the box. Agents may be building
// or testing there while another refreshes it, so a file already holding
// the content is left alone (its mtime too, so builds see nothing to redo),
// and a changed one is swapped in whole: written beside it, then renamed.
func placeFile(target, content string) error {
	if existing, err := os.ReadFile(target); err == nil && string(existing) == content {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".placing-*")
	if err != nil {
		return err
	}
	if _, err := temporary.WriteString(content); err != nil {
		temporary.Close()
		os.Remove(temporary.Name())
		return err
	}
	if err := temporary.Close(); err != nil {
		os.Remove(temporary.Name())
		return err
	}
	if err := os.Chmod(temporary.Name(), 0o644); err != nil {
		os.Remove(temporary.Name())
		return err
	}
	if err := os.Rename(temporary.Name(), target); err != nil {
		os.Remove(temporary.Name())
		return err
	}
	return nil
}

// DropSandbox deletes the session's sandbox, if it has one.
func (r *Registry) DropSandbox(session string) {
	if r.sandboxDir == "" {
		return
	}
	r.sandboxMu.Lock()
	state := r.sandboxes[session]
	delete(r.sandboxes, session)
	r.sandboxMu.Unlock()
	if state != nil {
		// Wait out a build in progress, so it cannot land after the drop.
		state.mu.Lock()
		defer state.mu.Unlock()
	}
	r.trashSandbox(r.sandboxPath(session))
}

// sweepSandboxes removes what an earlier run left behind: half-deleted
// sandboxes, and those of sessions no longer pending.
func (r *Registry) sweepSandboxes() {
	if r.sandboxDir == "" {
		return
	}
	entries, err := os.ReadDir(r.sandboxDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, sandboxTrashPrefix) {
			go os.RemoveAll(filepath.Join(r.sandboxDir, name))
			continue
		}
		if _, ok := r.Snapshot(name); !ok {
			r.trashSandbox(filepath.Join(r.sandboxDir, name))
		}
	}
}

func (r *Registry) sandboxStateFor(session string) *sandboxState {
	r.sandboxMu.Lock()
	defer r.sandboxMu.Unlock()
	state, ok := r.sandboxes[session]
	if !ok {
		state = &sandboxState{units: map[string]bool{}}
		r.sandboxes[session] = state
	}
	return state
}

func (r *Registry) sandboxPath(session string) string {
	return filepath.Join(r.sandboxDir, session)
}

// trashSandbox renames a sandbox out of the way — instant even for a large
// one — and removes it in the background.
func (r *Registry) trashSandbox(box string) {
	if _, err := os.Lstat(box); err != nil {
		return
	}
	trash := filepath.Join(r.sandboxDir, sandboxTrashPrefix+filepath.Base(box)+"-"+strconv.FormatInt(time.Now().UnixNano(), 10))
	if err := os.Rename(box, trash); err != nil {
		os.RemoveAll(box)
		return
	}
	go os.RemoveAll(trash)
}

// copyUnit copies one workspace unit into the box at the same relative
// path: a copy-on-write clone where the filesystem has one, else a plain
// copy that skips .git and stops at maxSandboxCopyBytes. It lands under a
// temporary name first, so a failed copy leaves nothing half-built.
func (r *Registry) copyUnit(box, unit string) error {
	source := filepath.Join(r.workspaceRoot, filepath.FromSlash(unit))
	target := filepath.Join(box, filepath.FromSlash(unit))
	if unit == "." {
		target = box
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("cannot make the sandbox: %w", err)
	}
	staging := target + ".building"
	os.RemoveAll(staging)
	if err := cloneTree(source, staging); err != nil {
		os.RemoveAll(staging)
		budget := maxSandboxCopyBytes
		if err := copyTree(source, staging, &budget); err != nil {
			os.RemoveAll(staging)
			return fmt.Errorf("cannot copy %s into the sandbox: %w", unitName(unit), err)
		}
	}
	if err := os.Rename(staging, target); err != nil {
		os.RemoveAll(staging)
		return fmt.Errorf("cannot make the sandbox: %w", err)
	}
	return nil
}

// errTooLargeToCopy stops a plain copy past its budget.
var errTooLargeToCopy = errors.New("too large to copy without copy-on-write — pass include for only what the build needs, or build after the edits are applied")

// copyTree copies source to target without .git folders, keeping symlinks
// as links, and charges every file's size to budget.
func copyTree(source, target string, budget *int64) error {
	return filepath.WalkDir(source, func(current string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(source, current)
		destination := filepath.Join(target, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case entry.IsDir() && entry.Name() == ".git" && current != source:
			return filepath.SkipDir
		case entry.IsDir():
			return os.MkdirAll(destination, info.Mode().Perm()|0o700)
		case info.Mode()&fs.ModeSymlink != 0:
			link, err := os.Readlink(current)
			if err != nil {
				return err
			}
			return os.Symlink(link, destination)
		case !info.Mode().IsRegular():
			return nil
		}
		if *budget -= info.Size(); *budget < 0 {
			return errTooLargeToCopy
		}
		return copyFile(current, destination, info.Mode().Perm())
	})
}

func copyFile(source, target string, mode fs.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// repoOf is the unit a file belongs to in a sandbox: its git repo inside
// the workspace (the nearest folder above it holding .git), else its
// top-level folder, else the file itself; "." when the workspace is the
// repo. Answers are cached: a file's repo does not move while it is staged.
func (r *Registry) repoOf(rel string) string {
	r.repoMu.Lock()
	if repo, ok := r.repoCache[rel]; ok {
		r.repoMu.Unlock()
		return repo
	}
	r.repoMu.Unlock()
	repo := r.findRepo(rel)
	r.repoMu.Lock()
	r.repoCache[rel] = repo
	r.repoMu.Unlock()
	return repo
}

func (r *Registry) findRepo(rel string) string {
	for dir := path.Dir(rel); dir != "." && dir != "/"; dir = path.Dir(dir) {
		if _, err := os.Lstat(filepath.Join(r.workspaceRoot, filepath.FromSlash(dir), ".git")); err == nil {
			return dir
		}
	}
	if _, err := os.Lstat(filepath.Join(r.workspaceRoot, ".git")); err == nil {
		return "."
	}
	if top, _, found := strings.Cut(rel, "/"); found {
		return top
	}
	return rel
}

// RepoName names a unit in a sentence: its path, or "the project" for ".".
func RepoName(unit string) string {
	return unitName(unit)
}

func unitName(unit string) string {
	if unit == "." {
		return "the project"
	}
	return unit
}

func unitContains(outer, inner string) bool {
	return outer == "." || strings.HasPrefix(inner+"/", outer+"/")
}

// outermostUnits drops duplicates and units inside another one.
func outermostUnits(units []string) []string {
	slices.Sort(units)
	units = slices.Compact(units)
	var kept []string
	for _, unit := range units {
		inside := false
		for _, other := range units {
			if other != unit && unitContains(other, unit) {
				inside = true
				break
			}
		}
		if !inside {
			kept = append(kept, unit)
		}
	}
	return kept
}

// RepoSession is a pending session still open for edits that has staged
// files in a repo, with its agents still editing.
type RepoSession struct {
	Session string
	Editing []string
}

// SessionsInRepo lists the sessions still open for edits with staged files
// in the same repo (sandbox unit) as rel, but for except — the reader's own
// session or the one already named. An agent about to change that repo can
// join one of them instead of starting its own.
func (r *Registry) SessionsInRepo(rel string, except ...string) (repo string, sessions []RepoSession) {
	repo = r.repoOf(rel)
	for _, record := range r.Pending() {
		if !record.AgentEndedAt.IsZero() || slices.Contains(except, record.Session) {
			continue
		}
		if slices.ContainsFunc(record.Files, func(file store.AgentEditFile) bool { return r.repoOf(file.Rel) == repo }) {
			sessions = append(sessions, RepoSession{Session: record.Session, Editing: record.WithAgents().EditingAgents()})
		}
	}
	return repo, sessions
}
