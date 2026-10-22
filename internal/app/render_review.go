package app

import (
	"fmt"
	"path/filepath"
	"strings"

	"charm.land/lipgloss/v2"

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

// reviewRepoHeading is a repo group's header text: the workspace-relative
// repo dir, the workspace's own name for the root, or "(no repository)".
func (m Model) reviewRepoHeading(file reviewFile) string {
	switch {
	case file.repoRoot == "":
		return "(no repository)"
	case file.repoLabel == ".":
		return filepath.Base(m.workspaceRoot) + "/"
	default:
		return file.repoLabel + "/"
	}
}

// reviewListRows builds the left list — a header per repo group, then its
// files — and maps each file index to its row so selection and clicks line up.
func (m Model) reviewListRows() (rows []sidebarRow, fileRow []int, rowFile []int) {
	if m.review.preparing {
		return []sidebarRow{sidebarHeaderRow("preparing review…")}, nil, []int{-1}
	}
	fileRow = make([]int, len(m.review.files))
	heading := "\x00"
	for i, file := range m.review.files {
		if group := m.reviewRepoHeading(file); group != heading {
			heading = group
			rows = append(rows, sidebarHeaderRow(group))
			rowFile = append(rowFile, -1)
		}
		badge, color := reviewBadge(file.status)
		name := file.repoRel
		if file.repoRoot == "" {
			name = file.workspaceRel
		}
		if file.status == reviewModified || file.status == reviewAdded || file.status == reviewDeleted {
			name += fmt.Sprintf("  +%d −%d", file.document.adds, file.document.dels)
		}
		fileRow[i] = len(rows)
		rows = append(rows, sidebarBadgeRow(badge, 1, color, name, fileStyle))
		rowFile = append(rowFile, i)
	}
	return rows, fileRow, rowFile
}

func (m Model) reviewSidebarList() sidebarList {
	rows, fileRow, _ := m.reviewListRows()
	selected := -1
	if index := m.review.selection.index; index < len(fileRow) {
		selected = fileRow[index]
	}
	return sidebarList{rows: rows, selectedIndex: selected}
}

// activateReviewRow applies a click on a left-list row: a file row selects
// that file, a repo header does nothing.
func (m Model) activateReviewRow(rowIndex int) (Model, bool) {
	_, _, rowFile := m.reviewListRows()
	if rowIndex < 0 || rowIndex >= len(rowFile) || rowFile[rowIndex] < 0 {
		return m, false
	}
	m, _ = m.selectReviewFile(rowFile[rowIndex])
	return m, true
}

var (
	reviewHeaderPathStyle = lipgloss.NewStyle().Bold(true).Foreground(colFg).Background(colBg)
	reviewHeaderDimStyle  = lipgloss.NewStyle().Foreground(colComment).Background(colBg)
	reviewHeaderAddStyle  = lipgloss.NewStyle().Foreground(colGreen).Background(colBg)
	reviewHeaderDelStyle  = lipgloss.NewStyle().Foreground(colRed).Background(colBg)
)

// renderReview draws the main pane: a one-line header naming the selected
// file, its repo, and its change counts, above the shared diff renderer.
func (m Model) renderReview(width, height int) string {
	file, ok := m.selectedReviewFile()
	if !ok {
		return baseStyle.Render("preparing review…")
	}
	badge, color := reviewBadge(file.status)
	header := segStyleFor(view.HighlightSegment{Color: color, Bold: true}).Render(badge+" ") +
		reviewHeaderPathStyle.Render(file.workspaceRel)
	if file.repoRoot != "" {
		header += reviewHeaderDimStyle.Render("  ·  repo " + strings.TrimSuffix(m.reviewRepoHeading(file), "/"))
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
		body = baseStyle.Render("computing diff…")
	case file.note != "" && len(file.document.rows) == 0:
		body = reviewHeaderDimStyle.Render(file.note)
	case file.status == reviewUnchanged:
		body = reviewHeaderDimStyle.Render("no changes against " + m.reviewBaseLabel())
	default:
		var search diffSearchOverlay
		if m.review.search.active && m.review.search.query != "" {
			search = diffSearchOverlay{
				byLine:  m.reviewMC.matchesByLine(file.corpus, m.review.search.query),
				focused: m.review.search.focused,
			}
		}
		body = renderDiffDocument(file.document, m.review.position, search, width, rowsHeight)
	}
	return header + "\n" + body
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
	if !m.review.preparing {
		loaded := 0
		for _, file := range m.review.files {
			if file.loaded() {
				loaded++
			}
		}
		line += statusTextStyle.Render(fmt.Sprintf(" file %d/%d   base %s", m.review.selection.index+1, len(m.review.files), m.reviewBaseLabel()))
		if loaded < len(m.review.files) {
			line += statusTextStyle.Render("   ") + editingStyle.Render(fmt.Sprintf("loading %d/%d…", loaded, len(m.review.files)))
		}
		if file, ok := m.selectedReviewFile(); ok && len(file.document.rows) > 0 {
			line += statusTextStyle.Render(fmt.Sprintf("   Ln %d/%d", m.review.position.row+1, len(file.document.rows)))
		}
	}
	return withNotice(m, line) + statusTextStyle.Render("   ") +
		hintStyle.Render("↑/↓ move · Shift+↑/↓ file · n/p change · "+m.actionHints(
			hintFor("find", keymap.Find), hintFor("files", keymap.GotoFile), hintFor("grep", keymap.Grep))+" · Esc exit")
}
