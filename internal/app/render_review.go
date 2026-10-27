package app

import (
	"fmt"
	"path/filepath"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/nickooan/ntee-editor/internal/filetree"
	"github.com/nickooan/ntee-editor/internal/keymap"
	"github.com/nickooan/ntee-editor/internal/view"
)

// reviewBadge is a file's one-letter status and its color in the left list.
func reviewBadge(status reviewFileStatus) (badge, color string) {
	switch status {
	case reviewModified:
		return "M", "#fabd2f"
	case reviewAdded:
		return "A", "#b8bb26"
	case reviewDeleted:
		return "D", "#fb4934"
	case reviewUnchanged:
		return "=", "#928374"
	case reviewBinary, reviewTooLarge:
		return "B", "#928374"
	case reviewMissing, reviewFailed:
		return "!", "#fb4934"
	default:
		return "…", "#928374"
	}
}

// reviewRepoName names a file's repo for the main-pane header: its
// workspace-relative dir, or the workspace's own name for the root itself.
func (m Model) reviewRepoName(file reviewFile) string {
	if file.repoLabel == "." {
		return filepath.Base(m.workspaceRoot)
	}
	return file.repoLabel
}

// reviewSidebarList draws the cached folder tree like the main file tree:
// directories with their ↓/→ markers (a folded one counts the files it
// hides), files with their status badge in the marker column, one space,
// then the name and change counts.
func (m Model) reviewSidebarList() sidebarList {
	if m.review.preparing {
		return sidebarList{rows: []sidebarRow{sidebarHeaderRow("preparing review…")}, selectedIndex: -1}
	}
	if len(m.review.files) == 0 && m.review.session != "" {
		return sidebarList{rows: []sidebarRow{sidebarHeaderRow("waiting for files…")}, selectedIndex: -1}
	}
	rows := make([]sidebarRow, len(m.review.entries))
	for row, entry := range m.review.entries {
		indent := strings.Repeat("  ", entry.Depth)
		if entry.Type == "directory" {
			label := strings.TrimRight(filetree.FormatFileTreeEntryLabel(entry, len([]rune(indent))+len([]rune(entry.Name))+2), " ")
			if hidden := m.review.hiddenCount[entry.RelativePath]; hidden > 0 {
				label += fmt.Sprintf("  %d", hidden)
			}
			rows[row] = sidebarRow{segments: []sidebarSegment{{text: label, style: dirStyle}}}
			continue
		}
		file := m.review.files[m.review.fileByPath[entry.RelativePath]]
		badge, color := reviewBadge(file.status)
		fileRow := sidebarBadgeRow(indent, badge, 2, color, entry.Name, fileStyle)
		if m.review.unseen[file.workspaceRel] {
			// Next to the name, so a narrow pane cuts the counts first.
			fileRow.segments = append(fileRow.segments, sidebarSegment{text: " •", style: reviewUnseenStyle})
		}
		if file.status == reviewModified || file.status == reviewAdded || file.status == reviewDeleted {
			// Counts colored like the diff itself: additions green, deletions red.
			fileRow.segments = append(fileRow.segments,
				sidebarSegment{text: fmt.Sprintf("  +%d", file.document.adds), style: reviewHeaderAddStyle},
				sidebarSegment{text: fmt.Sprintf(" −%d", file.document.dels), style: reviewHeaderDelStyle})
		}
		rows[row] = fileRow
	}
	return sidebarList{rows: rows, selectedIndex: m.reviewSelectedRow()}
}

// activateReviewRow applies a click on a left-pane row: a directory folds or
// unfolds, a file is selected.
func (m Model) activateReviewRow(rowIndex int) (Model, bool) {
	if m.review.preparing || rowIndex < 0 || rowIndex >= len(m.review.entries) {
		return m, false
	}
	entry := m.review.entries[rowIndex]
	if entry.Type == "directory" {
		return m.toggleReviewFold(entry.RelativePath), true
	}
	m, _ = m.selectReviewFile(m.review.fileByPath[entry.RelativePath])
	return m, true
}

var (
	reviewHeaderPathStyle = lipgloss.NewStyle().Bold(true).Foreground(colFg).Background(colBg)
	reviewHeaderDimStyle  = lipgloss.NewStyle().Foreground(colComment).Background(colBg)
	reviewHeaderAddStyle  = lipgloss.NewStyle().Foreground(colGreen).Background(colBg)
	reviewHeaderDelStyle  = lipgloss.NewStyle().Foreground(colRed).Background(colBg)
	reviewUnseenStyle     = lipgloss.NewStyle().Foreground(colOrange).Bold(true).Background(colBgChrome)
)

// renderReview draws the main pane: a one-line header naming the selected
// file, its repo, and its change counts, above the shared diff renderer.
func (m Model) renderReview(width, height int) string {
	file, ok := m.selectedReviewFile()
	if !ok {
		if m.review.session != "" && !m.review.preparing {
			return baseStyle.Render("waiting for the agent's files…")
		}
		return baseStyle.Render("preparing review…")
	}
	badge, color := reviewBadge(file.status)
	header := segStyleFor(view.HighlightSegment{Color: color, Bold: true}).Render(badge+" ") +
		reviewHeaderPathStyle.Render(file.workspaceRel)
	if file.repoRoot != "" {
		header += reviewHeaderDimStyle.Render("  ·  repo " + m.reviewRepoName(file))
	}
	if file.loaded() && len(file.document.rows) > 0 {
		header += reviewHeaderDimStyle.Render("  ·  ") +
			reviewHeaderAddStyle.Render(fmt.Sprintf("+%d", file.document.adds)) + reviewHeaderDimStyle.Render(" ") +
			reviewHeaderDelStyle.Render(fmt.Sprintf("−%d", file.document.dels))
	}
	header = padStyledTo(header, width, reviewHeaderDimStyle)

	rowsHeight := max(1, height-1)
	var body string
	switch {
	case !file.loaded():
		body = m.withReviewNote(file, baseStyle.Render("computing diff…"), width, rowsHeight)
	case file.problem != "" && len(file.document.rows) == 0:
		body = m.withReviewNote(file, reviewHeaderDimStyle.Render(file.problem), width, rowsHeight)
	case file.status == reviewUnchanged && file.agentNote.text == "":
		body = reviewHeaderDimStyle.Render("no changes against " + m.reviewBaseLabel())
	default:
		var search diffSearchOverlay
		if m.review.search.active && m.review.search.query != "" {
			search = diffSearchOverlay{
				byLine:  m.reviewMC.matchesByLine(file.corpus, m.review.search.query),
				focused: m.review.search.focused,
			}
		}
		body = renderDiffDocument(file.document, m.review.position, search, width, rowsHeight, m.tabWidthFor(file.workspaceRel))
		body = m.withReviewNote(file, body, width, rowsHeight)
	}
	return header + "\n" + body
}

// reviewNoteLabels are the borders of a review file's note box: it can't be
// closed (Esc leaves the review), and Shift+↑/↓ walk files, so only the
// wheel scrolls it.
func reviewNoteLabels(file reviewFile) noteBoxLabels {
	labels := noteBoxLabels{title: " note ", scrollHints: [2]string{"wheel", "wheel"}}
	if file.noteLine > 0 {
		labels.title = fmt.Sprintf(" note · L%d ", file.noteLine)
	}
	return labels
}

// reviewNoteBox places a review file's note box in the main pane's diff rows
// (below the file header): on its line's diff row, or the first change; over
// a file with no rows, under the placeholder line.
func (m Model) reviewNoteBox(file reviewFile, width, height int) (lineNoteBox, bool) {
	if file.agentNote.text == "" {
		return lineNoteBox{}, false
	}
	rows := file.document.rows
	if !file.loaded() || len(rows) == 0 {
		return file.agentNote.geometry(0, -1, 0, max(1, width-3), height)
	}
	start := fileViewportTop(m.review.position.row, m.review.position.scrollY, height, len(rows))
	gutterWidth := diffGutterWidth(file.document, height)
	return file.agentNote.geometry(file.landingRow()-start, m.review.position.row-start, gutterWidth, max(1, width-gutterWidth-3), height)
}

// withReviewNote splices the file's note box onto the rendered body.
func (m Model) withReviewNote(file reviewFile, body string, width, height int) string {
	box, ok := m.reviewNoteBox(file, width, height)
	if !ok {
		return body
	}
	rows := strings.Split(body, "\n")
	for len(rows) < height {
		rows = append(rows, "")
	}
	return strings.Join(spliceNoteBox(rows, box, renderLineNoteBox(box, reviewNoteLabels(file))), "\n")
}

func (m Model) reviewBaseLabel() string {
	if m.review.base == "" {
		return "HEAD"
	}
	return m.review.base
}

// padStyledTo pads an already-styled line to width with the given style, so
// the row's background runs to the pane edge.
func padStyledTo(line string, width int, style lipgloss.Style) string {
	if extra := width - lipgloss.Width(line); extra > 0 {
		return line + style.Render(strings.Repeat(" ", extra))
	}
	return line
}

// renderReviewStatus is the status row: the search bar while searching,
// otherwise position in the file set plus key hints.
func (m Model) renderReviewStatus() string {
	if m.review.search.active {
		return m.renderDocumentSearchStatus("review", m.review.search, len(m.reviewMatchesFor(m.review.search.query)))
	}
	line := promptStyle.Render("@review")
	if m.review.session != "" {
		line += statusTextStyle.Render("   ") + sessionBadgeStyle.Render(m.reviewSessionBadge())
	}
	if !m.review.preparing && len(m.review.files) > 0 {
		loaded := 0
		for _, file := range m.review.files {
			if file.loaded() {
				loaded++
			}
		}
		if m.review.session != "" {
			line += statusTextStyle.Render("  ")
		}
		line += statusTextStyle.Render(fmt.Sprintf(" file %d/%d   base %s", m.review.selection.index+1, len(m.review.files), m.reviewBaseLabel()))
		if loaded < len(m.review.files) {
			line += statusTextStyle.Render("   ") + editingStyle.Render(fmt.Sprintf("loading %d/%d…", loaded, len(m.review.files)))
		}
		if file, ok := m.selectedReviewFile(); ok && len(file.document.rows) > 0 {
			line += statusTextStyle.Render(fmt.Sprintf("   Ln %d/%d", m.review.position.row+1, len(file.document.rows)))
		}
	}
	exitHint := "Esc exit"
	if m.review.session != "" {
		exitHint = "Esc quit session"
	}
	return withNotice(m, line) + statusTextStyle.Render("   ") +
		hintStyle.Render("↑/↓ move · Shift+↑/↓ file · n/p change · "+m.actionHints(
			hintFor("find", keymap.Find), hintFor("files", keymap.GotoFile), hintFor("grep", keymap.Grep))+" · "+exitHint)
}

// reviewSessionBadge is the live review's mark in the status bar: how many
// files arrived, whether the view follows new ones (or how many are
// unseen), and whether the agent has finished.
func (m Model) reviewSessionBadge() string {
	badge := fmt.Sprintf("● review session %s · %d %s", m.review.session, len(m.review.files), pluralize("file", len(m.review.files)))
	switch {
	case len(m.review.unseen) > 0:
		badge += fmt.Sprintf(" · %d new", len(m.review.unseen))
	case m.review.follow:
		badge += " · following"
	}
	if m.session.complete {
		badge += " · complete"
	}
	return badge
}
