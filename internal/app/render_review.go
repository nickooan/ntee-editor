package app

import (
	"fmt"
	"math"
	"path/filepath"
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/nickooan/ntee-editor/internal/filetree"
	"github.com/nickooan/ntee-editor/internal/keymap"
	"github.com/nickooan/ntee-editor/internal/staging"
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
	return sidebarList{rowCount: len(m.review.entries), rowAt: m.reviewSidebarRow, selectedIndex: m.reviewSelectedRow()}
}

// reviewSidebarRow builds one row of the review tree; the list builds rows
// lazily, so a large review formats only what is on screen.
func (m Model) reviewSidebarRow(row int) sidebarRow {
	entry := m.review.entries[row]
	indent := strings.Repeat("  ", entry.Depth)
	if entry.Type == "directory" {
		label := strings.TrimRight(filetree.FormatFileTreeEntryLabel(entry, len([]rune(indent))+len([]rune(entry.Name))+2), " ")
		if hidden := m.review.hiddenCount[entry.RelativePath]; hidden > 0 {
			label += fmt.Sprintf("  %d", hidden)
		}
		return m.injectingReviewRow(entry.RelativePath, sidebarRow{segments: []sidebarSegment{{text: label, style: dirStyle}}})
	}
	file := m.review.files[m.review.fileByPath[entry.RelativePath]]
	badge, color := reviewBadge(file.status)
	fileRow := sidebarBadgeRow(indent, badge, 2, color, entry.Name, fileStyle)
	if m.review.unseen[file.workspaceRel] {
		// Next to the name, so a narrow pane cuts the counts first.
		fileRow.segments = append(fileRow.segments, sidebarSegment{text: " •", style: reviewUnseenStyle})
	}
	if len(file.notes) > 0 {
		fileRow.segments = append(fileRow.segments, sidebarSegment{text: fmt.Sprintf(" ◆%d", len(file.notes)), style: reviewNoteCountStyle})
	}
	if file.status == reviewModified || file.status == reviewAdded || file.status == reviewDeleted {
		// Counts colored like the diff itself: additions green, deletions red.
		fileRow.segments = append(fileRow.segments,
			sidebarSegment{text: fmt.Sprintf("  +%d", file.document.adds), style: reviewHeaderAddStyle},
			sidebarSegment{text: fmt.Sprintf(" −%d", file.document.dels), style: reviewHeaderDelStyle})
	}
	return m.injectingReviewRow(entry.RelativePath, fileRow)
}

// hexInjectGlow is the background a row injected into the review list starts
// from, before it fades into the pane.
const hexInjectGlow = "#4b5a1c"

// injectingReviewRow draws a left-pane row that just joined the list: it
// slides in from a few cells right and its glow fades out. Any other row is
// returned as is.
func (m Model) injectingReviewRow(path string, row sidebarRow) sidebarRow {
	progress, injecting := m.reviewInjectProgress(path)
	if !injecting {
		return row
	}
	eased := easeOut(progress)
	background := lipgloss.Color(blendHex(hexInjectGlow, hexBg, eased))
	segments := make([]sidebarSegment, 0, len(row.segments)+1)
	if offset := int(math.Round((1 - eased) * 4)); offset > 0 {
		segments = append(segments, sidebarSegment{text: strings.Repeat(" ", offset), style: baseStyle.Background(background)})
	}
	for _, segment := range row.segments {
		segment.style = segment.style.Background(background)
		segments = append(segments, segment)
	}
	return sidebarRow{segments: segments}
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
	reviewNoteCountStyle  = lipgloss.NewStyle().Foreground(colAqua).Background(colBgChrome)
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
	case file.status == reviewUnchanged && len(file.notes) == 0:
		body = reviewHeaderDimStyle.Render("no changes against " + m.reviewBaseLabel())
	default:
		var search diffSearchOverlay
		if m.review.search.active && m.review.search.query != "" {
			search = diffSearchOverlay{
				byLine:  m.reviewMC.matchesByLine(file.corpus, m.review.search.query),
				focused: m.review.search.focused,
			}
		}
		body = renderDiffDocument(file.document, m.review.position, search, file.noteMarkerRows(), m.reviewCodeFrame(file), width, rowsHeight, m.tabWidthFor(file.workspaceRel))
		body = m.withReviewNote(file, body, width, rowsHeight)
	}
	return header + "\n" + m.withAgentEditDecisionPanel(file, body, width, rowsHeight)
}

// reviewCursorPaneRow is the cursor's row in the main pane's diff rows.
func (m Model) reviewCursorPaneRow(file reviewFile, height int) int {
	if !file.loaded() || len(file.document.rows) == 0 {
		return 0
	}
	return m.review.position.row - fileViewportTop(m.review.position.row, m.review.position.scrollY, height, len(file.document.rows))
}

// agentEditDecisionPanel is a staged review's floating panel: the decision
// keys once its agents finished, else who works on it and that the keys
// unlock after. ok=false for any other review.
func (m Model) agentEditDecisionPanel() (floatPanel, bool) {
	id := m.review.agentEdits
	if id == "" || m.review.agentEditsTrace {
		return floatPanel{}, false
	}
	decide := []floatPanelRow{{"a", "apply all"}, {"x", "discard all"}, {"r", "request changes"}, {"Esc", "leave…"}}
	panel := floatPanel{title: " decide ", titleStyle: floatPanelTitleStyle, rows: decide}
	record, ok := m.agentEditsPendingRecord(id)
	if !ok {
		return panel, true
	}
	working := func(activity string, agents []string) floatPanel {
		spinner := m.agentEditsSpinner(m.agentsWorkingRecently())
		who := ansi.Truncate(activity+" · "+staging.AgentNames(agents)+m.agentsIdleText(), 30, "…")
		return floatPanel{
			title:      " " + spinner + " agents working ",
			titleStyle: lineNoteSessionTitleStyle,
			rows:       []floatPanelRow{{"", who}, {"", "a x r unlock when done"}, {"Esc", "back to editor"}},
		}
	}
	switch {
	case m.agentEdits.stillEditing[id]:
		return working("editing", record.WithAgents().EditingAgents()), true
	case m.agentEdits.checking[id]:
		return working("checking", record.CheckingAgents()), true
	case slices.Contains(m.sessionsAwaitingReview(), id):
		panel.title, panel.titleStyle = " ⏳ agent waiting for you ", lineNoteSessionTitleStyle
	}
	return panel, true
}

// agentEditDecisionPlacement is where the decision panel floats over the
// main pane's diff rows; ok=false when there is none or it doesn't fit (the
// status line then carries the decision, as on a narrow window).
func (m Model) agentEditDecisionPlacement(file reviewFile, width, height int) (panel floatPanel, top, left int, ok bool) {
	panel, ok = m.agentEditDecisionPanel()
	if !ok {
		return panel, 0, 0, false
	}
	top, left, ok = floatPanelPlacement(width, height, m.reviewCursorPaneRow(file, height), panel.width(), panel.height())
	return panel, top, left, ok
}

// agentEditDecisionPanelShown reports whether this frame floats the
// decision panel, from the same sizes renderReview draws with.
func (m Model) agentEditDecisionPanelShown() bool {
	file, ok := m.selectedReviewFile()
	if !ok {
		return false
	}
	_, _, _, shown := m.agentEditDecisionPlacement(file, max(3, m.width-m.sidebarWidth())-4, m.reviewRowsHeight())
	return shown
}

// withAgentEditDecisionPanel floats the decision panel over the body, on
// top of the note box.
func (m Model) withAgentEditDecisionPanel(file reviewFile, body string, width, height int) string {
	panel, top, left, ok := m.agentEditDecisionPlacement(file, width, height)
	if !ok {
		return body
	}
	rows := strings.Split(body, "\n")
	for len(rows) < height {
		rows = append(rows, "")
	}
	return strings.Join(spliceFloatPanel(rows, panel, top, left), "\n")
}

// reviewNoteLabels are the borders of a review file's active note box: it
// can't be closed (Esc leaves the review), and Shift+↑/↓ walk files, so only
// the wheel scrolls it. The title counts the file's notes and dims a note
// written against an earlier content of the file.
func reviewNoteLabels(file reviewFile, note reviewNote) noteBoxLabels {
	labels := noteBoxLabels{scrollHints: [2]string{"wheel", "wheel"}}
	title := " note"
	if len(file.notes) > 1 {
		title += fmt.Sprintf(" %d/%d", min(file.activeNote, len(file.notes)-1)+1, len(file.notes))
	}
	if note.line > 0 {
		title += fmt.Sprintf(" · L%d", note.line)
	}
	if file.noteIsEarlier(note) {
		title += " · earlier"
		labels.dimmed = true
	}
	labels.title = title + " "
	return labels
}

// reviewNoteBox places a review file's active note box in the main pane's
// diff rows (below the file header): on its line's diff row, or the first
// change; over a file with no rows, under the placeholder line.
func (m Model) reviewNoteBox(file reviewFile, width, height int) (lineNoteBox, bool) {
	note, ok := file.activeReviewNote()
	if !ok {
		return lineNoteBox{}, false
	}
	rows := file.document.rows
	if !file.loaded() || len(rows) == 0 {
		return note.overlay.geometry(0, -1, 0, max(1, width-3), height)
	}
	start := fileViewportTop(m.review.position.row, m.review.position.scrollY, height, len(rows))
	gutterWidth := diffGutterWidth(file.document, height)
	return note.overlay.geometry(file.noteRow(note)-start, m.review.position.row-start, gutterWidth, max(1, width-gutterWidth-3), height)
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
	note, _ := file.activeReviewNote()
	return strings.Join(spliceNoteBox(rows, box, renderLineNoteBox(box, reviewNoteLabels(file, note))), "\n")
}

func (m Model) reviewBaseLabel() string {
	if m.review.agentEdits != "" {
		return "the file before the agent's edits"
	}
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
	// Notices and errors come right after the prompt: a review's status line
	// is long, and its end is what a narrow window cuts.
	line := withNotice(m, promptStyle.Render("@review"))
	// The session itself is named in the header (headerSessionBadge). A
	// staged review's decision leads here: it is what the user is here for,
	// and the end of a long status line is the first thing a narrow window
	// cuts.
	// The floating panel carries the decision when it fits the pane.
	if m.review.agentEdits != "" && !m.review.agentEditsTrace && !m.agentEditDecisionPanelShown() {
		line += statusTextStyle.Render("   ") + agentEditDecisionStyle.Render(m.agentEditReviewHint())
	}
	if !m.review.preparing && len(m.review.files) > 0 {
		loaded := 0
		for _, file := range m.review.files {
			if file.loaded() {
				loaded++
			}
		}
		if m.review.session != "" || m.review.agentEdits != "" {
			line += statusTextStyle.Render("  ")
		}
		line += statusTextStyle.Render(fmt.Sprintf(" file %d/%d", m.review.selection.index+1, len(m.review.files)))
		if m.review.agentEdits == "" {
			line += statusTextStyle.Render("   base " + m.reviewBaseLabel())
		}
		if loaded < len(m.review.files) {
			line += statusTextStyle.Render("   ") + editingStyle.Render(fmt.Sprintf("loading %d/%d…", loaded, len(m.review.files)))
		}
		if file, ok := m.selectedReviewFile(); ok && len(file.document.rows) > 0 {
			line += statusTextStyle.Render(fmt.Sprintf("   Ln %d/%d", m.review.position.row+1, len(file.document.rows)))
		}
	}
	noteHint := ""
	if file, ok := m.selectedReviewFile(); ok && len(file.notes) > 1 {
		noteHint = "[/] note · "
	}
	repoHint := ""
	if len(m.reviewRepos()) > 1 {
		repoHint = "{/} repo · "
	}
	navigationHints := "↑/↓ move · Shift+↑/↓ file · " + repoHint + "n/p change · " + noteHint + m.actionHints(
		hintFor("find", keymap.Find), hintFor("files", keymap.GotoFile), hintFor("grep", keymap.Grep))
	if m.review.agentEdits != "" && !m.review.agentEditsTrace {
		return line + statusTextStyle.Render("   ") + hintStyle.Render(navigationHints)
	}
	exitHint := "Esc exit"
	if m.review.session != "" {
		exitHint = "Esc quit session"
	}
	return line + statusTextStyle.Render("   ") + hintStyle.Render(navigationHints+" · "+exitHint)
}

// agentEditDecisionHint is a staged review's choice, shown first once its
// agents finished.
const agentEditDecisionHint = " a apply all to disk · x discard all · r request changes · Esc decide "

// agentEditReviewHint is the staged review's lead hint: the decision, or —
// while the agents still edit or check the session, and the decision keys
// are locked — that they are working, animated with the header's label.
func (m Model) agentEditReviewHint() string {
	id := m.review.agentEdits
	spinner, idle := m.agentEditsSpinner(m.agentsWorkingRecently()), m.agentsIdleText()
	record, ok := m.agentEditsPendingRecord(id)
	switch {
	case !ok:
		return agentEditDecisionHint
	case m.agentEdits.stillEditing[id]:
		return " " + spinner + " agents are working… (editing · " + staging.AgentNames(record.WithAgents().EditingAgents()) + idle + ") · Esc back to the editor "
	case m.agentEdits.checking[id]:
		return " " + spinner + " agents are working… (checking · " + staging.AgentNames(record.CheckingAgents()) + idle + ") · Esc back to the editor "
	case slices.Contains(m.sessionsAwaitingReview(), id):
		return " " + m.agentEditsSpinner(true) + " ⏳ agent waiting for you ·" + agentEditDecisionHint
	}
	return agentEditDecisionHint
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
