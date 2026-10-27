package app

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/filetree"
	"github.com/nickooan/ntee-editor/internal/syntax"
	"github.com/nickooan/ntee-editor/internal/view"
)

// reviewFileStatus is what a reviewed file's diff turned out to be.
type reviewFileStatus uint8

const (
	reviewLoading   reviewFileStatus = iota // not loaded yet
	reviewModified                          // changed against the base
	reviewAdded                             // not in the base: every row is an add
	reviewDeleted                           // gone from the working tree: every row is a del
	reviewUnchanged                         // identical to the base
	reviewMissing                           // in neither the working tree nor the base
	reviewBinary
	reviewTooLarge
	reviewFailed
)

// reviewFile is one file of a review: where it lives, and its diff once the
// loader has computed it.
type reviewFile struct {
	workspaceRel string
	repoRoot     string // absolute; "" when the file is in no git repository
	repoLabel    string // workspace-relative repo dir for display ("." = the workspace root itself)
	repoRel      string // path inside repoRoot
	status       reviewFileStatus
	document     diffDocument
	corpus       string // the rows' text joined with \n: the search corpus (row == line)
	problem      string // why there is no diff (binary, too large, failure)
	// loadGen counts the file's loads: a resent file reloads, and only the
	// newest load's result lands.
	loadGen int
	// notes are the sender's notes on the file, one per line, in line order;
	// the box shows the active one.
	notes      []reviewNote
	activeNote int
	// contentHash identifies the loaded content (0 = none): notes shown with
	// another one are earlier notes.
	contentHash uint64
}

// reviewRequest is one file asked for review: its workspace path, the line
// its note sits on (1-based; 0 = none), and the note ("" = none).
type reviewRequest struct {
	workspaceRel string
	line         int
	note         string
}

func (file reviewFile) loaded() bool {
	return file.status != reviewLoading
}

// reviewState is review mode: a read-only list of files (possibly spanning
// nested repos) with a git-diff view of the selected one. It never touches
// m.root, the open file, or the edit session, so leaving restores exactly
// where the user was. gen stays monotonic across reviews so in-flight loads
// of an older review are dropped.
type reviewState struct {
	files        []reviewFile  // in tree order (filetree.BuildPathTreeEntries)
	selection    listSelection // index into files
	position     rowCursor
	search       documentSearch
	base         string // "" = HEAD
	gen          int
	preparing    bool // repos are being resolved; files is the unsorted request
	previousMode mode

	// The left pane's folder tree. entries are rebuilt only when the file set
	// or the fold state changes (refreshReviewTree), never per frame.
	// collapsed is replaced, never mutated: Model copies share the map.
	collapsed   map[string]bool
	entries     []filetree.FileTreeEntry
	rowByPath   map[string]int // entry RelativePath → row
	fileByPath  map[string]int // workspace path → index into files
	hiddenCount map[string]int // collapsed dir → reviewed files it hides

	// A review session's live state (review_session.go): its id ("" = a
	// plain review), whether the view follows each arriving file, the latest
	// arrival, and the files that arrived while it didn't (replaced, never
	// mutated, like collapsed).
	session    string
	follow     bool
	newestPath string
	unseen     map[string]bool
}

const (
	maxReviewFiles     = 500
	maxReviewFileBytes = 2 << 20
	reviewLoadBatch    = 32
)

// reviewLoadSlots bounds review loads (git spawns, diff, highlight) across
// the whole process: batch workers and one-file session arrivals share it,
// so a burst of agent sends can't start hundreds of git processes at once.
var reviewLoadSlots = make(chan struct{}, runtime.NumCPU())

func withReviewLoadSlot(work func()) {
	reviewLoadSlots <- struct{}{}
	defer func() { <-reviewLoadSlots }()
	work()
}

// reviewPreparedMsg delivers the resolved and sorted file list.
type reviewPreparedMsg struct {
	gen   int
	files []reviewFile
}

// reviewLoadedMsg delivers computed diffs. nextStart is the index the next
// batch begins at, -1 for a one-off load (the selected or an arriving file).
type reviewLoadedMsg struct {
	gen       int
	files     []reviewFile
	nextStart int
}

// leaveToEditOrQuery backs out of every overlay, bar, and special view to
// the plain edit (file open) or query mode underneath, keeping all edit work.
// It is the resting state an external request starts from.
func (m Model) leaveToEditOrQuery() Model {
	m = m.closeCompletion()
	if m.fuzzyOpen {
		m = m.closeFuzzy()
	}
	if m.grepOpen {
		m = m.closeGrep()
	}
	if m.opMode.open {
		m = m.closeOpMode()
	}
	m.defPickOpen = false
	m.messageOverlay = ""
	// Bars and views stack at most a few deep (inspect over exec over edit).
	for range 4 {
		switch m.mode {
		case modeInspect:
			m.mode = m.inspectPrevMode
		case modeExec:
			m.mode = m.execPrevMode
		case modeCommand:
			m.mode = m.cmdPrevMode
		case modeSearch, modeSearchExec:
			m.mode = m.searchPrevMode
		case modeDiff:
			m = m.exitDiff()
		case modeBlame:
			m = m.exitBlame()
		case modeConflict:
			m = m.exitConflict()
		case modeOpenAPI, modeGraphQL:
			m = m.clearPreviewState()
			m.mode = modeEdit
		case modeReview:
			m = m.exitReview()
		case modeExternalHistory:
			m = m.exitExternalHistory()
		default:
			return m
		}
	}
	return m
}

// enterReview opens review mode over workspace-relative files, each with its
// optional note. Repo resolution touches the disk, so the list is prepared
// off the UI goroutine and the diffs then load in batches.
func (m Model) enterReview(requests []reviewRequest, base string) (Model, tea.Cmd) {
	if len(requests) == 0 {
		m.errText = "review: no files"
		return m, nil
	}
	m = m.leaveToEditOrQuery()
	files := make([]reviewFile, 0, min(len(requests), maxReviewFiles))
	indexByPath := make(map[string]int, len(requests))
	for _, request := range requests {
		// A path repeated with another anchor is another note on the file
		// (a replayed review session saves one entry per note).
		if index, seen := indexByPath[request.workspaceRel]; seen {
			files[index] = m.addReviewNote(files[index], request)
			continue
		}
		if len(files) == maxReviewFiles {
			continue
		}
		indexByPath[request.workspaceRel] = len(files)
		files = append(files, m.newReviewFile(request))
	}
	m.review = reviewState{
		files:        files,
		base:         base,
		gen:          m.review.gen + 1,
		preparing:    true,
		previousMode: m.mode,
	}
	m.mode = modeReview
	return m, reviewPrepareCmd(m.review.gen, m.workspaceRoot, files)
}

// newReviewFile is a requested file, not resolved or loaded yet, with the
// requests' notes laid out for the file's tab size.
func (m Model) newReviewFile(requests ...reviewRequest) reviewFile {
	file := reviewFile{workspaceRel: requests[0].workspaceRel}
	for _, request := range requests {
		file = m.addReviewNote(file, request)
	}
	return file
}

// reviewPrepareCmd resolves each file's repo and puts the list in the left
// pane's tree order, so Shift+↑/↓ walks files exactly as they are shown.
func reviewPrepareCmd(gen int, workspaceRoot string, requested []reviewFile) tea.Cmd {
	files := append([]reviewFile(nil), requested...)
	return func() tea.Msg {
		workspaceIsRepo := filetree.IsGitRepo(workspaceRoot)
		forEachParallel(len(files), func(i int) {
			files[i] = resolveReviewFile(workspaceRoot, workspaceIsRepo, files[i])
		})
		return reviewPreparedMsg{gen: gen, files: inTreeOrder(files)}
	}
}

// resolveReviewFile finds the repo a file lives in, so it can be diffed
// there; a file in no repository is left without one. It touches the disk.
func resolveReviewFile(workspaceRoot string, workspaceIsRepo bool, file reviewFile) reviewFile {
	absolute := filepath.Join(workspaceRoot, filepath.FromSlash(file.workspaceRel))
	repoRoot := filetree.FindRepoRoot(workspaceRoot, absolute)
	if filepath.Clean(repoRoot) == filepath.Clean(workspaceRoot) && !workspaceIsRepo {
		return file // in no repository: no base to diff against
	}
	repoRel, err := filepath.Rel(repoRoot, absolute)
	if err != nil {
		return file
	}
	label, err := filepath.Rel(workspaceRoot, repoRoot)
	if err != nil {
		return file
	}
	file.repoRoot = repoRoot
	file.repoRel = filepath.ToSlash(repoRel)
	file.repoLabel = filepath.ToSlash(label)
	return file
}

// inTreeOrder orders files as the folder tree lists them: directories
// first, then files, by name at every level.
func inTreeOrder(files []reviewFile) []reviewFile {
	byPath := make(map[string]reviewFile, len(files))
	paths := make([]string, len(files))
	for i, file := range files {
		byPath[file.workspaceRel] = file
		paths[i] = file.workspaceRel
	}
	ordered := make([]reviewFile, 0, len(files))
	for _, entry := range filetree.BuildPathTreeEntries(paths, nil) {
		if file, ok := byPath[entry.RelativePath]; ok && entry.Type == "file" {
			ordered = append(ordered, file)
		}
	}
	return ordered
}

func (m Model) handleReviewPrepared(msg reviewPreparedMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.review.gen || m.mode != modeReview {
		return m, nil
	}
	m.review.files = msg.files
	m.review.preparing = false
	m.review.selection = listSelection{}
	m.review.position = rowCursor{}
	m.review.collapsed = nil
	m.review.fileByPath = make(map[string]int, len(msg.files))
	for i, file := range msg.files {
		m.review.fileByPath[file.workspaceRel] = i
	}
	m = m.refreshReviewTree()
	if len(m.review.files) == 0 {
		return m, nil
	}
	// The first file (selected) loads alone so it shows before a whole
	// batch is done; the batch chain continues after it.
	return m, m.reviewLoadCmd(0, 1, 1)
}

// refreshReviewTree rebuilds the folder tree's visible rows and the folded
// directories' hidden counts — on a new file set or a fold change only.
func (m Model) refreshReviewTree() Model {
	paths := make([]string, len(m.review.files))
	for i, file := range m.review.files {
		paths[i] = file.workspaceRel
	}
	m.review.entries = filetree.BuildPathTreeEntries(paths, m.review.collapsed)
	m.review.rowByPath = make(map[string]int, len(m.review.entries))
	m.review.hiddenCount = map[string]int{}
	for row, entry := range m.review.entries {
		m.review.rowByPath[entry.RelativePath] = row
		if entry.Type == "directory" && !entry.IsExpanded {
			prefix := entry.RelativePath + "/"
			for _, path := range paths {
				if strings.HasPrefix(path, prefix) {
					m.review.hiddenCount[entry.RelativePath]++
				}
			}
		}
	}
	return m
}

// toggleReviewFold folds or unfolds a directory of the left pane.
func (m Model) toggleReviewFold(dir string) Model {
	collapsed := make(map[string]bool, len(m.review.collapsed)+1)
	for folded := range m.review.collapsed {
		collapsed[folded] = true
	}
	if collapsed[dir] {
		delete(collapsed, dir)
	} else {
		collapsed[dir] = true
	}
	m.review.collapsed = collapsed
	return m.refreshReviewTree()
}

// unfoldReviewAncestors reopens every folded directory above rel, so a file
// the keyboard (or a search) selects is never hidden.
func (m Model) unfoldReviewAncestors(rel string) Model {
	var folded []string
	for dir := path.Dir(rel); dir != "." && dir != "/"; dir = path.Dir(dir) {
		if m.review.collapsed[dir] {
			folded = append(folded, dir)
		}
	}
	if len(folded) == 0 {
		return m
	}
	collapsed := make(map[string]bool, len(m.review.collapsed))
	for dir := range m.review.collapsed {
		collapsed[dir] = true
	}
	for _, dir := range folded {
		delete(collapsed, dir)
	}
	m.review.collapsed = collapsed
	return m.refreshReviewTree()
}

// reviewSelectedRow is the left-pane row to highlight: the selected file's,
// or its nearest visible ancestor directory while that one is folded.
func (m Model) reviewSelectedRow() int {
	file, ok := m.selectedReviewFile()
	if !ok {
		return -1
	}
	for rel := file.workspaceRel; rel != "." && rel != "/"; rel = path.Dir(rel) {
		if row, visible := m.review.rowByPath[rel]; visible {
			return row
		}
	}
	return -1
}

// reviewLoadBatchCmd computes the diffs of files[start:start+reviewLoadBatch]
// off the UI goroutine; the handler schedules the next batch, so the chain
// ends by itself when a message is dropped as stale.
func (m Model) reviewLoadBatchCmd(start int) tea.Cmd {
	end := min(start+reviewLoadBatch, len(m.review.files))
	if start >= end {
		return nil
	}
	return m.reviewLoadCmd(start, end, end)
}

// reviewLoadSelectedCmd loads the selected file ahead of the batch chain when
// the user moves onto one that is still loading.
func (m Model) reviewLoadSelectedCmd() tea.Cmd {
	index := m.review.selection.index
	if index >= len(m.review.files) || m.review.files[index].loaded() {
		return nil
	}
	return m.reviewLoadCmd(index, index+1, -1)
}

// reviewLoadCmd loads files[start:end] in parallel, skipping any already
// loaded (the selected file may have been loaded ahead of its batch). The
// base is resolved once per repo of the batch, not per file — and only per
// batch, since an agent may commit while a review session runs.
func (m Model) reviewLoadCmd(start, end, nextStart int) tea.Cmd {
	gen, base, workspaceRoot := m.review.gen, m.review.base, m.workspaceRoot
	var pending []reviewFile
	for _, file := range m.review.files[start:end] {
		if !file.loaded() {
			pending = append(pending, file)
		}
	}
	highlightLimit := m.cfg.Editor.MaxHighlightKB * 1024
	tabWidths := make([]int, len(pending)) // resolved here: the cmd never reads m.cfg
	for i, file := range pending {
		tabWidths[i] = m.tabWidthFor(file.workspaceRel)
	}
	return func() tea.Msg {
		revisions := resolveReviewRevisions(base, pending)
		forEachParallel(len(pending), func(i int) {
			withReviewLoadSlot(func() {
				pending[i] = loadReviewFile(workspaceRoot, revisions[pending[i].repoRoot], pending[i], highlightLimit, tabWidths[i])
			})
		})
		return reviewLoadedMsg{gen: gen, files: pending, nextStart: nextStart}
	}
}

// resolveReviewRevisions resolves base in each distinct repo of files.
func resolveReviewRevisions(base string, files []reviewFile) map[string]diffRevision {
	var repoRoots []string
	seen := map[string]bool{}
	for _, file := range files {
		if file.repoRoot != "" && !seen[file.repoRoot] {
			seen[file.repoRoot] = true
			repoRoots = append(repoRoots, file.repoRoot)
		}
	}
	resolved := make([]diffRevision, len(repoRoots))
	forEachParallel(len(repoRoots), func(i int) {
		withReviewLoadSlot(func() { resolved[i] = resolveDiffRevision(repoRoots[i], base) })
	})
	revisions := make(map[string]diffRevision, len(repoRoots))
	for i, repoRoot := range repoRoots {
		revisions[repoRoot] = resolved[i]
	}
	return revisions
}

// loadReviewFile reads the working-tree side through the workspace file jail
// and diffs it against the base, already resolved in the file's own repo.
// It is safe to run concurrently.
func loadReviewFile(workspaceRoot string, revision diffRevision, file reviewFile, highlightLimit, tabWidth int) reviewFile {
	data, binary, err := filetree.ReadContainedFile(workspaceRoot, file.workspaceRel)
	exists := err == nil
	switch {
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		file.status, file.problem = reviewFailed, err.Error()
		return file
	case binary:
		file.status, file.problem = reviewBinary, "binary file — not shown"
		return file
	case len(data) > maxReviewFileBytes:
		file.status, file.problem = reviewTooLarge, fmt.Sprintf("larger than %d MB — not shown", maxReviewFileBytes>>20)
		return file
	}
	var current []string
	if exists {
		current = splitBufferLines(string(data))
		file.contentHash = reviewContentHash(data)
	}

	var document diffDocument
	if file.repoRoot == "" {
		if !exists {
			file.status, file.problem = reviewMissing, "not found, and not in a git repository"
			return file
		}
		document = diffDocument{lines: current, newFile: true, adds: len(current), rows: make([]diffRow, len(current))}
		for i := range current {
			document.rows[i] = diffRow{kind: diffAdd, bufLine: i}
		}
		file.problem = "not in a git repository"
	} else {
		errText := revision.problem
		if errText == "" {
			document, errText = diffAgainstRevision(file.repoRoot, revision, file.repoRel, current, exists)
		}
		if errText != "" {
			file.status, file.problem = reviewFailed, errText
			if !exists {
				file.status = reviewMissing
			}
			return file
		}
	}
	if exists && (highlightLimit <= 0 || len(data) <= highlightLimit) {
		document.highlights = syntax.HighlightLines(path.Base(file.workspaceRel), string(data))
	}
	switch {
	case document.deletedFile:
		file.status = reviewDeleted
	case document.newFile:
		file.status = reviewAdded
	case document.adds == 0 && document.dels == 0:
		file.status = reviewUnchanged
	default:
		file.status = reviewModified
	}
	file.document = expandDocumentTabs(document, tabWidth)
	document = file.document
	texts := make([]string, len(document.rows))
	for i := range document.rows {
		texts[i] = document.rowText(i)
	}
	file.corpus = strings.Join(texts, "\n")
	return file
}

// expandDocumentTabs turns tabs into tabWidth spaces in a review document's
// text and highlights. Row windowing counts runes, so a raw tab would draw
// several columns for 1 counted and push tab-indented code past the pane.
// Review is read-only, so it can show expanded text; it runs after the diff,
// which compares the real content.
func expandDocumentTabs(document diffDocument, tabWidth int) diffDocument {
	tab := strings.Repeat(" ", tabWidth)
	lines := make([]string, len(document.lines))
	for i, line := range document.lines {
		lines[i] = strings.ReplaceAll(line, "\t", tab)
	}
	document.lines = lines
	rows := make([]diffRow, len(document.rows))
	for i, row := range document.rows {
		row.text = strings.ReplaceAll(row.text, "\t", tab)
		rows[i] = row
	}
	document.rows = rows
	if document.highlights != nil {
		highlights := make([][]view.HighlightSegment, len(document.highlights))
		for i, segments := range document.highlights {
			expanded := make([]view.HighlightSegment, len(segments))
			for j, segment := range segments {
				segment.Text = strings.ReplaceAll(segment.Text, "\t", tab)
				expanded[j] = segment
			}
			highlights[i] = expanded
		}
		document.highlights = highlights
	}
	return document
}

// handleReviewLoaded stores loaded diffs, found by path since a review
// session inserts files while loads are in flight. A file that already loaded
// (the priority load raced its batch) keeps its first result so the cursor on
// it stays put, and a stale load of a resent file is dropped (loadGen).
// Landing the selected file puts the cursor on its note or first change.
func (m Model) handleReviewLoaded(msg reviewLoadedMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.review.gen || m.mode != modeReview {
		return m, nil
	}
	files := append([]reviewFile(nil), m.review.files...)
	for _, file := range msg.files {
		index, ok := m.review.fileByPath[file.workspaceRel]
		if !ok || files[index].loaded() || files[index].loadGen != file.loadGen {
			continue
		}
		// The notes may have moved on while it loaded (a step to another
		// note); the load only brings the diff.
		file.notes = stampReviewNotes(files[index].notes, file.contentHash)
		file.activeNote = files[index].activeNote
		files[index] = file
		if index == m.review.selection.index {
			m.review.position = rowCursor{}.anchoredAt(file.landingRow(), m.reviewSpanFor(file))
		}
	}
	m.review.files = files
	if msg.nextStart < 0 {
		return m, nil
	}
	return m, m.reviewLoadBatchCmd(msg.nextStart)
}

// selectedReviewFile is the file under the list selection.
func (m Model) selectedReviewFile() (reviewFile, bool) {
	if m.review.preparing || len(m.review.files) == 0 {
		return reviewFile{}, false
	}
	return m.review.files[min(m.review.selection.index, len(m.review.files)-1)], true
}

// reviewRowsHeight is how many diff rows fit: the main pane below the
// review's one-line file header (the tab strip is hidden in review).
func (m Model) reviewRowsHeight() int {
	return max(1, m.bodyHeight()-3)
}

func (m Model) reviewSpanFor(file reviewFile) rowSpan {
	return file.document.span(m.reviewRowsHeight())
}

func (m Model) reviewSpan() rowSpan {
	file, _ := m.selectedReviewFile()
	return m.reviewSpanFor(file)
}

// selectReviewFile moves the list selection to index and lands on the file's
// first change; an unloaded file is fetched ahead of the batch chain.
func (m Model) selectReviewFile(index int) (Model, tea.Cmd) {
	if m.review.preparing || len(m.review.files) == 0 {
		return m, nil
	}
	m.review.selection = listSelection{}.moved(index, len(m.review.files))
	m.review.search = documentSearch{}
	file := m.review.files[m.review.selection.index]
	m = m.unfoldReviewAncestors(file.workspaceRel)
	m.review.position = rowCursor{}.anchoredAt(file.landingRow(), m.reviewSpanFor(file))
	if m.review.session != "" {
		m = m.reviewSessionSelected(file.workspaceRel)
	}
	return m, m.reviewLoadSelectedCmd()
}

// landingRow is where selecting the file puts the cursor: on its active
// note's line, else on its first change.
func (file reviewFile) landingRow() int {
	if note, ok := file.activeReviewNote(); ok {
		return file.noteRow(note)
	}
	return file.document.firstChangeRow()
}

// selectReviewPath selects the file with workspace-relative path rel.
func (m Model) selectReviewPath(rel string) (Model, tea.Cmd) {
	if index, ok := m.review.fileByPath[rel]; ok {
		return m.selectReviewFile(index)
	}
	return m, nil
}

// reviewGoToGrepHit lands a Ctrl+G hit from the review-scoped grep: the
// searched text is each file's row corpus, so the hit line is a row.
func (m Model) reviewGoToGrepHit(hit grepHit) Model {
	m, _ = m.selectReviewPath(hit.rel)
	m.review.position = m.review.position.anchoredAt(hit.line, m.reviewSpan())
	return m
}

// reviewGrepFiles is the review's Ctrl+G search set: every loaded file's
// row text, removed lines included.
func (m Model) reviewGrepFiles() []grepFile {
	files := make([]grepFile, 0, len(m.review.files))
	for _, file := range m.review.files {
		if file.loaded() && len(file.document.rows) > 0 {
			files = append(files, newGrepFile(file.workspaceRel, file.corpus))
		}
	}
	return files
}

// reviewFuzzyCandidates is the review's Ctrl+P list, in list order.
func (m Model) reviewFuzzyCandidates() []string {
	paths := make([]string, len(m.review.files))
	for i, file := range m.review.files {
		paths[i] = file.workspaceRel
	}
	return paths
}

// exitReview leaves review mode for the mode it was entered from. Nothing
// else changed underneath, so this is a pure state drop.
func (m Model) exitReview() Model {
	m.mode = m.review.previousMode
	m.review = reviewState{gen: m.review.gen}
	return m
}
