package app

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/filetree"
	"github.com/nickooan/ntee-editor/internal/syntax"
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
	note         string // why there is no diff (binary, too large, failure)
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
	files        []reviewFile
	selection    listSelection // index into files
	position     rowCursor
	search       documentSearch
	base         string // "" = HEAD
	gen          int
	preparing    bool // repos are being resolved; files is the unsorted request
	previousMode mode
}

const (
	maxReviewFiles     = 500
	maxReviewFileBytes = 2 << 20
	reviewLoadBatch    = 8
)

// reviewPreparedMsg delivers the resolved and sorted file list.
type reviewPreparedMsg struct {
	gen   int
	files []reviewFile
}

// reviewLoadedMsg delivers computed diffs. nextStart is the index the next
// batch begins at, -1 for a one-off priority load (the selected file).
type reviewLoadedMsg struct {
	gen       int
	start     int
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

// enterReview opens review mode over workspace-relative paths. Repo
// resolution touches the disk, so the list is prepared off the UI goroutine
// and the diffs then load in batches.
func (m Model) enterReview(paths []string, base string) (Model, tea.Cmd) {
	if len(paths) == 0 {
		m.errText = "review: no files"
		return m, nil
	}
	m = m.leaveToEditOrQuery()
	files := make([]reviewFile, 0, min(len(paths), maxReviewFiles))
	seen := make(map[string]bool, len(paths))
	for _, rel := range paths {
		if seen[rel] || len(files) == maxReviewFiles {
			continue
		}
		seen[rel] = true
		files = append(files, reviewFile{workspaceRel: rel})
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

// reviewPrepareCmd resolves each file's repo and sorts the list by repo, then
// path, so the left pane groups predictably.
func reviewPrepareCmd(gen int, workspaceRoot string, requested []reviewFile) tea.Cmd {
	files := append([]reviewFile(nil), requested...)
	return func() tea.Msg {
		workspaceIsRepo := filetree.IsGitRepo(workspaceRoot)
		for i := range files {
			absolute := filepath.Join(workspaceRoot, filepath.FromSlash(files[i].workspaceRel))
			repoRoot := filetree.FindRepoRoot(workspaceRoot, absolute)
			if filepath.Clean(repoRoot) == filepath.Clean(workspaceRoot) && !workspaceIsRepo {
				continue // in no repository: no base to diff against
			}
			repoRel, err := filepath.Rel(repoRoot, absolute)
			if err != nil {
				continue
			}
			label, err := filepath.Rel(workspaceRoot, repoRoot)
			if err != nil {
				continue
			}
			files[i].repoRoot = repoRoot
			files[i].repoRel = filepath.ToSlash(repoRel)
			files[i].repoLabel = filepath.ToSlash(label)
		}
		sort.SliceStable(files, func(a, b int) bool {
			if files[a].repoLabel != files[b].repoLabel {
				return files[a].repoLabel < files[b].repoLabel
			}
			return files[a].workspaceRel < files[b].workspaceRel
		})
		return reviewPreparedMsg{gen: gen, files: files}
	}
}

func (m Model) handleReviewPrepared(msg reviewPreparedMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.review.gen || m.mode != modeReview {
		return m, nil
	}
	m.review.files = msg.files
	m.review.preparing = false
	m.review.selection = listSelection{}
	m.review.position = rowCursor{}
	return m, m.reviewLoadBatchCmd(0)
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

func (m Model) reviewLoadCmd(start, end, nextStart int) tea.Cmd {
	gen, base, workspaceRoot := m.review.gen, m.review.base, m.workspaceRoot
	pending := append([]reviewFile(nil), m.review.files[start:end]...)
	highlightLimit := m.cfg.Editor.MaxHighlightKB * 1024
	return func() tea.Msg {
		for i := range pending {
			pending[i] = loadReviewFile(workspaceRoot, base, pending[i], highlightLimit)
		}
		return reviewLoadedMsg{gen: gen, start: start, files: pending, nextStart: nextStart}
	}
}

// loadReviewFile reads the working-tree side through the workspace file jail
// and diffs it against the base in the file's own repo.
func loadReviewFile(workspaceRoot, base string, file reviewFile, highlightLimit int) reviewFile {
	data, binary, err := filetree.ReadContainedFile(workspaceRoot, file.workspaceRel)
	exists := err == nil
	switch {
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		file.status, file.note = reviewFailed, err.Error()
		return file
	case binary:
		file.status, file.note = reviewBinary, "binary file — not shown"
		return file
	case len(data) > maxReviewFileBytes:
		file.status, file.note = reviewTooLarge, fmt.Sprintf("larger than %d MB — not shown", maxReviewFileBytes>>20)
		return file
	}
	var current []string
	if exists {
		current = splitBufferLines(string(data))
	}

	var document diffDocument
	if file.repoRoot == "" {
		if !exists {
			file.status, file.note = reviewMissing, "not found, and not in a git repository"
			return file
		}
		document = diffDocument{lines: current, newFile: true, adds: len(current), rows: make([]diffRow, len(current))}
		for i := range current {
			document.rows[i] = diffRow{kind: diffAdd, bufLine: i}
		}
		file.note = "not in a git repository"
	} else {
		var errText string
		document, errText = computeDiffDocument(file.repoRoot, base, file.repoRel, current, exists)
		if errText != "" {
			file.status, file.note = reviewFailed, errText
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
	file.document = document
	texts := make([]string, len(document.rows))
	for i := range document.rows {
		texts[i] = document.rowText(i)
	}
	file.corpus = strings.Join(texts, "\n")
	return file
}

// handleReviewLoaded stores loaded diffs. A file that already loaded (the
// priority load raced its batch) keeps its first result so the cursor on it
// stays put. Landing the selected file puts the cursor on its first change.
func (m Model) handleReviewLoaded(msg reviewLoadedMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.review.gen || m.mode != modeReview {
		return m, nil
	}
	files := append([]reviewFile(nil), m.review.files...)
	for offset, file := range msg.files {
		index := msg.start + offset
		if index >= len(files) || files[index].loaded() || files[index].workspaceRel != file.workspaceRel {
			continue
		}
		files[index] = file
		if index == m.review.selection.index {
			m.review.position = rowCursor{}.anchoredAt(file.document.firstChangeRow(), m.reviewSpanFor(file))
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
	m.review.position = rowCursor{}.anchoredAt(file.document.firstChangeRow(), m.reviewSpanFor(file))
	return m, m.reviewLoadSelectedCmd()
}

// selectReviewPath selects the file with workspace-relative path rel.
func (m Model) selectReviewPath(rel string) (Model, tea.Cmd) {
	for i, file := range m.review.files {
		if file.workspaceRel == rel {
			return m.selectReviewFile(i)
		}
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
