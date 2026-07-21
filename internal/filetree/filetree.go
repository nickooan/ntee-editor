// Package filetree models the project sidebar: a cursor-driven directory tree
// plus a full-walk corpus for fuzzy file search. Directory listings are cached
// by mtime since the tree rebuilds on every keystroke/frame.
package filetree

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// FileTreeEntry is one visible row of the tree. CommandValue is the path as it
// appears in the query input bar: `rel+"/"` for directories, `rel` for files.
type FileTreeEntry struct {
	Name         string
	RelativePath string
	CommandValue string
	Depth        int
	Type         string // "directory" | "file"
	IsExpanded   bool
	Dimmed       bool // shown in the tree but excluded from search (gitignore match or node_modules) — rendered gray
	Uncommitted  bool // has uncommitted git changes (or, for a dir, contains one) — rendered yellow
}

type dirChild struct {
	name   string
	isDir  bool
	isFile bool
}

type cachedDir struct {
	mtime   time.Time
	entries []dirChild
}

type cachedGitignore struct {
	mtime time.Time
	gi    *Gitignore
}

var (
	dirCacheMu sync.Mutex
	dirCache   = map[string]cachedDir{}

	gitignoreCacheMu sync.Mutex
	gitignoreCache   = map[string]cachedGitignore{}
)

// ClearDirCache drops all cached directory listings and compiled nested
// .gitignore matchers so the next walk re-reads from disk. Used by the manual
// :refresh to pick up changes that mtime comparison would miss (e.g. a
// same-second external edit).
func ClearDirCache() {
	dirCacheMu.Lock()
	dirCache = map[string]cachedDir{}
	dirCacheMu.Unlock()
	gitignoreCacheMu.Lock()
	gitignoreCache = map[string]cachedGitignore{}
	gitignoreCacheMu.Unlock()
}

// scopedGitignore is one matcher in the chain applied during a walk. dir is the
// root-relative directory the matcher is rooted at ("" = root); paths are tested
// relative to it.
type scopedGitignore struct {
	dir string
	gi  *Gitignore
}

// chainIgnored reports whether a root-relative path is ignored by a chain of
// directory-scoped .gitignore matchers (shallow to deep). Each scope tests the
// path relative to its own directory; a deeper file's opinion overrides a
// shallower one (git's last-match-wins across levels, including `!` negation).
func chainIgnored(chain []scopedGitignore, rel string, isDir bool) bool {
	ignored := false
	for _, sc := range chain {
		sub := rel
		if sc.dir != "" {
			sub = strings.TrimPrefix(rel, sc.dir+"/")
		}
		if matched, ig := sc.gi.MatchState(sub, isDir); matched {
			ignored = ig
		}
	}
	return ignored
}

// loadNestedGitignore reads and compiles <absDir>/.gitignore, returning nil when
// absent. Compiled matchers are cached by path and the .gitignore file's own
// mtime (content edits do not bump the parent dir's mtime), so the per-keystroke
// tree walk does not re-read or recompile regexes while a directory sits open.
// Callers should invoke it only for directories whose listing actually contains
// a .gitignore, so a directory without one costs no syscall here.
func loadNestedGitignore(absDir string) *Gitignore {
	p := filepath.Join(absDir, ".gitignore")
	info, err := os.Stat(p)
	if err != nil {
		return nil
	}
	mtime := info.ModTime()

	gitignoreCacheMu.Lock()
	cached, ok := gitignoreCache[p]
	gitignoreCacheMu.Unlock()
	if ok && cached.mtime.Equal(mtime) {
		return cached.gi
	}

	data, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	gi := CompileGitignore(strings.Split(string(data), "\n"))

	gitignoreCacheMu.Lock()
	gitignoreCache[p] = cachedGitignore{mtime: mtime, gi: gi}
	gitignoreCacheMu.Unlock()
	return gi
}

// hasGitignore reports whether a directory listing contains a regular
// .gitignore file, so the walk can skip loadNestedGitignore entirely for the
// overwhelming majority of directories that have none.
func hasGitignore(children []dirChild) bool {
	for _, c := range children {
		if c.isFile && c.name == ".gitignore" {
			return true
		}
	}
	return false
}

// extendChain appends a scope for dirPath's own .gitignore (when present in
// children) to chain, returning a chain safe to hand to child recursions. A
// full-slice-expression append keeps sibling recursions from aliasing a shared
// backing array, and allocates only at directories that actually have a
// .gitignore. dirPath "" (the root) is skipped: the root matcher is seeded by
// the caller from the passed-in gi.
func extendChain(chain []scopedGitignore, dirPath, absDir string, children []dirChild) []scopedGitignore {
	if dirPath == "" || !hasGitignore(children) {
		return chain
	}
	gi := loadNestedGitignore(absDir)
	if gi == nil {
		return chain
	}
	return append(chain[:len(chain):len(chain)], scopedGitignore{dir: dirPath, gi: gi})
}

func readDirectorySorted(path string) ([]dirChild, time.Time, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, time.Time{}, err
	}
	mtime := info.ModTime()

	dirCacheMu.Lock()
	cached, ok := dirCache[path]
	dirCacheMu.Unlock()
	if ok && cached.mtime.Equal(mtime) {
		return cached.entries, mtime, nil
	}

	raw, err := os.ReadDir(path)
	if err != nil {
		return nil, time.Time{}, err
	}

	entries := make([]dirChild, 0, len(raw))
	for _, e := range raw {
		entries = append(entries, dirChild{
			name:   e.Name(),
			isDir:  e.IsDir(),
			isFile: e.Type().IsRegular(),
		})
	}
	// Directories first, then by name.
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].isDir != entries[j].isDir {
			return entries[i].isDir
		}
		return entries[i].name < entries[j].name
	})

	dirCacheMu.Lock()
	dirCache[path] = cachedDir{mtime: mtime, entries: entries}
	dirCacheMu.Unlock()
	return entries, mtime, nil
}

func isInsideRoot(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel))
}

// hardIgnore names are dropped by every walk — never shown in the tree and
// never indexed. Config tree.ignore ADDS to this set (see hardIgnored); it can
// never remove from it.
var hardIgnore = map[string]bool{
	".git": true,
}

// softIgnore names are shown in the tree (rendered gray, flagged Dimmed) but
// kept out of the search corpus — the dominant source of file-count blowup in
// dependency-heavy trees (one ~/workspace had 1200+ node_modules), yet still
// worth seeing in the sidebar.
var softIgnore = map[string]bool{
	"node_modules": true,
}

// IsGitRepo reports whether dir is a git repository root — it contains a .git
// entry (a directory for a normal clone, or a file for a worktree/submodule).
func IsGitRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// FindRepoRoot returns the nearest ancestor of filePath — walking up to and
// including editorRoot — that IsGitRepo. If none is found, or filePath lies
// outside editorRoot, it returns the absolute editorRoot. Used to scope a
// language server to the file's own repo rather than the whole opened tree.
func FindRepoRoot(editorRoot, filePath string) string {
	er, err := filepath.Abs(editorRoot)
	if err != nil {
		return editorRoot
	}
	abs, err := filepath.Abs(filePath)
	if err != nil {
		return er
	}
	dir := abs
	if info, statErr := os.Stat(abs); statErr != nil || !info.IsDir() {
		dir = filepath.Dir(abs) // filePath is a file (or gone): search from its dir
	}
	if !isInsideRoot(er, dir) {
		return er // outside the opened tree → fall back to the editor root
	}
	for {
		if IsGitRepo(dir) {
			return dir
		}
		if dir == er {
			return er
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return er // filesystem root reached without a match
		}
		dir = parent
	}
}

// projectMarkers name a language project's root. FindProjectRoot returns the
// deepest ancestor containing one — for a monorepo that is the sub-project
// (e.g. a frontend's tsconfig/package.json), not the outer .git, so a language
// server scopes to hundreds of files instead of the whole repo.
var projectMarkers = []string{
	"tsconfig.json", "jsconfig.json", "package.json", // JS/TS
	"go.mod",                                  // Go
	"Cargo.toml",                              // Rust
	"pyproject.toml", "setup.py", "setup.cfg", // Python
	"pom.xml", "build.gradle", "build.gradle.kts", // JVM
	"Gemfile", // Ruby
	".git",    // repo boundary — the fallback marker
}

// FindProjectRoot returns the nearest ancestor of filePath (up to and including
// editorRoot) that holds a project marker, else editorRoot. Preferred over
// FindRepoRoot for scoping language servers: in a monorepo it lands on the
// sub-project rather than the outer repo.
func FindProjectRoot(editorRoot, filePath string) string {
	er, err := filepath.Abs(editorRoot)
	if err != nil {
		return editorRoot
	}
	abs, err := filepath.Abs(filePath)
	if err != nil {
		return er
	}
	dir := abs
	if info, statErr := os.Stat(abs); statErr != nil || !info.IsDir() {
		dir = filepath.Dir(abs)
	}
	if !isInsideRoot(er, dir) {
		return er
	}
	for {
		for _, m := range projectMarkers {
			if _, err := os.Stat(filepath.Join(dir, m)); err == nil {
				return dir
			}
		}
		if dir == er {
			return er
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return er
		}
		dir = parent
	}
}

// hardIgnored reports whether name is dropped from every walk: the built-in
// hardIgnore set plus any config tree.ignore entry.
func hardIgnored(name string, ignore []string) bool {
	if hardIgnore[name] {
		return true
	}
	for _, ig := range ignore {
		if name == ig {
			return true
		}
	}
	return false
}

// softIgnored reports whether name is shown-but-dimmed in the tree and kept out
// of the search corpus.
func softIgnored(name string) bool {
	return softIgnore[name]
}

// BuildFileTreeEntries walks the root, descending only into directories whose
// relative paths are in expanded. Hard-ignored names (.git, config tree.ignore)
// are skipped entirely. Entries matched by gi, nested under a dimmed directory,
// or soft-ignored (node_modules) are shown but flagged Dimmed (rendered gray and
// excluded from search); gi may be nil to disable gitignore matching. dirty is
// the GitDirtySet (paths with uncommitted changes plus their ancestor dirs, so
// a collapsed dir flags too); entries in it are flagged Uncommitted (rendered
// yellow). nil disables git-status flagging.
func BuildFileTreeEntries(root string, expanded map[string]bool, ignore []string, gi *Gitignore, dirty map[string]bool) []FileTreeEntry {
	if root == "" {
		return nil
	}
	resolvedRoot, err := filepath.Abs(root)
	if err != nil {
		return nil
	}

	// The chain of directory-scoped .gitignore matchers applying to the current
	// directory's children, seeded with the root .gitignore (gi may be nil).
	var rootChain []scopedGitignore
	if gi != nil {
		rootChain = []scopedGitignore{{dir: "", gi: gi}}
	}

	var entries []FileTreeEntry
	var appendDir func(dirPath string, depth int, parentIgnored bool, chain []scopedGitignore)
	appendDir = func(dirPath string, depth int, parentIgnored bool, chain []scopedGitignore) {
		resolvedDir := filepath.Join(resolvedRoot, dirPath)
		if !isInsideRoot(resolvedRoot, resolvedDir) {
			return
		}
		children, _, err := readDirectorySorted(resolvedDir)
		if err != nil {
			return
		}
		chain = extendChain(chain, dirPath, resolvedDir, children)

		for _, child := range children {
			if hardIgnored(child.name, ignore) {
				continue
			}
			rel := child.name
			if dirPath != "" {
				rel = dirPath + "/" + child.name
			}
			// Once a directory is dimmed (gitignored or soft-ignored), everything
			// under it inherits it.
			dim := parentIgnored || softIgnored(child.name) || chainIgnored(chain, rel, child.isDir)

			if child.isDir {
				isExpanded := expanded[rel]
				entries = append(entries, FileTreeEntry{
					Name:         child.name,
					RelativePath: rel,
					CommandValue: rel + "/",
					Depth:        depth,
					Type:         "directory",
					IsExpanded:   isExpanded,
					Dimmed:       dim,
					Uncommitted:  dirty[rel],
				})
				if isExpanded {
					appendDir(rel, depth+1, dim, chain)
				}
				continue
			}
			if !child.isFile {
				continue
			}
			entries = append(entries, FileTreeEntry{
				Name:         child.name,
				RelativePath: rel,
				CommandValue: rel,
				Depth:        depth,
				Type:         "file",
				Dimmed:       dim,
				Uncommitted:  dirty[rel],
			})
		}
	}

	appendDir("", 0, false, rootChain)
	return entries
}

// maxScanDepth bounds the BuildAllEntries walk (symlink-loop guard).
const maxScanDepth = 16

// BuildAllEntries walks the whole root regardless of expansion state and
// returns every regular file's relative path. This is the fuzzy-search corpus:
// matching must find entries inside collapsed directories, hence the full walk.
// The ignore list is applied during the walk (load-bearing for big JS repos).
//
// maxFiles bounds the corpus (≤0 = unlimited); when hit, the walk stops and
// truncated is true. dirMtimes maps every visited directory's relative path
// (root = "") to its mtime (unix-nano) — a signature callers can persist and
// later stat-sweep to decide whether the corpus is still valid.
func BuildAllEntries(root string, ignore []string, gi *Gitignore, maxFiles int) (files []string, dirMtimes map[string]int64, truncated bool) {
	dirMtimes = map[string]int64{}
	if root == "" {
		return nil, dirMtimes, false
	}
	resolvedRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, dirMtimes, false
	}

	var rootChain []scopedGitignore
	if gi != nil {
		rootChain = []scopedGitignore{{dir: "", gi: gi}}
	}

	var appendDir func(dirPath string, depth int, chain []scopedGitignore)
	appendDir = func(dirPath string, depth int, chain []scopedGitignore) {
		if truncated || depth > maxScanDepth {
			return
		}
		resolvedDir := filepath.Join(resolvedRoot, dirPath)
		if !isInsideRoot(resolvedRoot, resolvedDir) {
			return
		}
		children, mtime, err := readDirectorySorted(resolvedDir)
		if err != nil {
			return
		}
		dirMtimes[dirPath] = mtime.UnixNano()
		chain = extendChain(chain, dirPath, resolvedDir, children)
		for _, child := range children {
			// Hard- and soft-ignored names (.git, config tree.ignore, node_modules)
			// are both kept out of the search corpus.
			if hardIgnored(child.name, ignore) || softIgnored(child.name) {
				continue
			}
			rel := child.name
			if dirPath != "" {
				rel = dirPath + "/" + child.name
			}
			// Gitignored entries are kept out of the search corpus entirely; a
			// gitignored directory is not descended, so its subtree is excluded.
			if chainIgnored(chain, rel, child.isDir) {
				continue
			}
			if child.isDir {
				appendDir(rel, depth+1, chain)
				if truncated {
					return
				}
				continue
			}
			if child.isFile {
				files = append(files, rel)
				if maxFiles > 0 && len(files) >= maxFiles {
					truncated = true
					return
				}
			}
		}
	}

	appendDir("", 0, rootChain)
	return files, dirMtimes, truncated
}

// FileTreeViewport is the visible window of the tree.
type FileTreeViewport struct {
	Entries     []FileTreeEntry
	MaxScrollY  int
	SafeScrollY int
}

// BuildFileTreeViewport centers the highlighted entry within height rows.
func BuildFileTreeViewport(entries []FileTreeEntry, height, scrollY, highlightedIndex int) FileTreeViewport {
	maxScrollY := max(0, len(entries)-height)
	next := scrollY
	if highlightedIndex != -1 {
		next = highlightedIndex - max(1, height)/2
	}
	safe := min(max(next, 0), maxScrollY)
	end := min(safe+height, len(entries))
	return FileTreeViewport{
		Entries:     entries[safe:end],
		MaxScrollY:  maxScrollY,
		SafeScrollY: safe,
	}
}

// ResolveNextFileTreeSelectionIndex moves the keyboard selection by direction,
// clamped. A -1 highlighted index starts at an end.
func ResolveNextFileTreeSelectionIndex(entries []FileTreeEntry, highlightedIndex, direction int) int {
	if len(entries) == 0 {
		return -1
	}
	if highlightedIndex == -1 {
		if direction == 1 {
			return 0
		}
		return len(entries) - 1
	}
	return min(max(highlightedIndex+direction, 0), len(entries)-1)
}

func splitNonEmpty(s, sep string) []string {
	parts := strings.Split(s, sep)
	out := parts[:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// BuildExpandedDirectoryPaths derives the directories to expand from a typed
// command path: every ancestor of the path, plus the last segment itself when
// the path ends in "/".
func BuildExpandedDirectoryPaths(command string) map[string]bool {
	out := map[string]bool{}
	norm := strings.ReplaceAll(strings.TrimSpace(command), "\\", "/")
	parts := splitNonEmpty(norm, "/")

	depth := len(parts) - 1
	if strings.HasSuffix(norm, "/") {
		depth = len(parts)
	}
	if depth < 0 {
		depth = 0
	}

	for i := 1; i <= depth; i++ {
		out[strings.Join(parts[:i], "/")] = true
	}
	return out
}

// FindFileTreeMatchIndex returns the best match (exact > prefix > substring)
// for input over CommandValue/Name, or -1.
func FindFileTreeMatchIndex(entries []FileTreeEntry, input string) int {
	normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(input), "\\", "/"))
	if normalized == "" {
		return -1
	}

	startsWith := -1
	includes := -1
	for i, entry := range entries {
		command := strings.ToLower(entry.CommandValue)
		name := strings.ToLower(entry.Name)

		if command == normalized || name == normalized {
			return i
		}
		if startsWith == -1 && (strings.HasPrefix(command, normalized) || strings.HasPrefix(name, normalized)) {
			startsWith = i
		}
		if includes == -1 && (strings.Contains(command, normalized) || strings.Contains(name, normalized)) {
			includes = i
		}
	}

	if startsWith != -1 {
		return startsWith
	}
	return includes
}

// ResolveHighlightedEntry returns the entry to highlight for input: the best
// match, else the nearest expanded ancestor directory, else -1.
func ResolveHighlightedEntry(entries []FileTreeEntry, input string) int {
	if matched := FindFileTreeMatchIndex(entries, input); matched != -1 {
		return matched
	}

	normalized := strings.ReplaceAll(strings.TrimSpace(input), "\\", "/")
	parts := splitNonEmpty(normalized, "/")
	for i := len(parts) - 1; i > 0; i-- {
		parentCommand := strings.Join(parts[:i], "/") + "/"
		for index, entry := range entries {
			if entry.Type == "directory" && entry.CommandValue == parentCommand {
				return index
			}
		}
	}
	return -1
}

// ResolveSidebarCommand picks the path that drives the sidebar: the typed
// input unless it is empty or a ":" editor command, in which case the
// confirmed selection.
func ResolveSidebarCommand(inputCommand, selectedCommand string) string {
	trimmed := strings.TrimSpace(inputCommand)
	if trimmed == "" || strings.HasPrefix(trimmed, ":") {
		return selectedCommand
	}
	return inputCommand
}

// ResolveParentDirectoryCommand returns the parent directory command of the
// given path (with a trailing slash), or ok=false when there is no parent.
func ResolveParentDirectoryCommand(commandValue string) (string, bool) {
	normalized := strings.ReplaceAll(strings.TrimSpace(commandValue), "\\", "/")
	if normalized == "" {
		return "", false
	}
	parts := splitNonEmpty(normalized, "/")
	if len(parts) == 0 {
		return "", false
	}
	parts = parts[:len(parts)-1]
	if len(parts) == 0 {
		return "", true
	}
	return strings.Join(parts, "/") + "/", true
}

// FormatFileTreeEntryLabel renders an entry line padded/truncated to width.
func FormatFileTreeEntryLabel(entry FileTreeEntry, width int) string {
	indent := strings.Repeat("  ", entry.Depth)
	marker := "  "
	if entry.Type == "directory" {
		if entry.IsExpanded {
			marker = "↓ "
		} else {
			marker = "→ "
		}
	}
	label := indent + marker + entry.Name
	runes := []rune(label)
	if len(runes) > width {
		cut := max(0, width-1)
		return padRight(string(runes[:cut]), width)
	}
	return padRight(label, width)
}

func padRight(s string, width int) string {
	if pad := width - len([]rune(s)); pad > 0 {
		return s + strings.Repeat(" ", pad)
	}
	return s
}
