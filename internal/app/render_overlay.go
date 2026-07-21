package app

import (
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/nickooan/ntee-editor/internal/fuzzy"
	"github.com/nickooan/ntee-editor/internal/input"
	"github.com/nickooan/ntee-editor/internal/view"
)

var (
	modalStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1).
			Background(colBg).BorderBackground(colBg).BorderForeground(colComment)
	modalTitleStyle  = lipgloss.NewStyle().Bold(true).Foreground(colFg).Background(colBg)
	overlayHintStyle = lipgloss.NewStyle().Foreground(colComment).Background(colBg)
	fuzzyBoldStyle   = lipgloss.NewStyle().Bold(true).Foreground(colOrange).Background(colBg)
)

// renderMessageOverlay centers a dismissible message box in the main pane.
func (m Model) renderMessageOverlay(width, height int) string {
	title := m.messageOverlay
	hint := "[enter] dismiss"
	boxWidth := input.Clamp(max(len([]rune(title)), len([]rune(hint)))+4, 20, max(20, width-2))
	box := modalStyle.Width(boxWidth).Render(
		modalTitleStyle.Render(title) + "\n\n" + overlayHintStyle.Render(hint),
	)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box,
		lipgloss.WithWhitespaceBackground(colBg))
}

// renderFuzzyOverlay draws the fuzzy file finder (Ctrl+P goto / Ctrl+U
// uncommitted — fuzzyPrompt labels the source): query input on top, matches
// beneath with the matched runes bold.
func (m Model) renderFuzzyOverlay(width, height int) string {
	const maxRows = 12
	boxWidth := input.Clamp(width*8/10, 24, max(24, width-2))
	rowWidth := max(1, boxWidth-2)

	var b strings.Builder
	b.WriteString(promptStyle.Render(m.fuzzyPrompt) + renderInputLine(m.fuzzyQuery, len([]rune(m.fuzzyQuery))) + "\n")

	if len(m.fuzzyMatches) == 0 {
		b.WriteString(overlayHintStyle.Render("(no matches)"))
	}

	visible := min(len(m.fuzzyMatches), maxRows)
	selected := input.Clamp(m.fuzzyIndex, 0, max(0, len(m.fuzzyMatches)-1))
	start := 0
	if selected >= visible {
		start = selected - visible + 1
	}
	for i := start; i < start+visible && i < len(m.fuzzyMatches); i++ {
		match := m.fuzzyMatches[i]
		cand := m.fuzzyCorpus[match.Index]
		// Matched positions are computed here, only for the visible rows, rather
		// than for every match during Filter.
		positions := fuzzy.Positions(m.fuzzyQuery, cand)
		row := renderFuzzyRow(cand.Text, positions, rowWidth, i == selected)
		b.WriteString("\n" + row)
	}

	box := modalStyle.Width(boxWidth).Render(b.String())
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box,
		lipgloss.WithWhitespaceBackground(colBg))
}

// renderDefPickOverlay lists multiple definition hits: `name.ext:LINE` with
// the containing directory, pick with ↑/↓ + Enter.
func (m Model) renderDefPickOverlay(width, height int) string {
	const maxRows = 10
	boxWidth := input.Clamp(width*7/10, 30, max(30, width-2))
	rowWidth := max(1, boxWidth-2)

	var b strings.Builder
	b.WriteString(modalTitleStyle.Render(padTo(truncateRunes(m.defPickTitle, rowWidth), rowWidth)) + "\n")
	b.WriteString(overlayHintStyle.Render(padTo("↑/↓ choose · enter jump · esc cancel", rowWidth)) + "\n")

	// A small code preview of the selected candidate, like the grep overlay's
	// top pane but compact.
	const previewH = 5
	if len(m.defPickPrevLines) > 0 {
		var re *regexp.Regexp
		if m.defPickToken != "" {
			re, _ = regexp.Compile(`\b` + regexp.QuoteMeta(m.defPickToken) + `\b`)
		}
		sel := m.defPickItems[input.Clamp(m.defPickIndex, 0, max(0, len(m.defPickItems)-1))]
		b.WriteString("\n")
		for _, row := range renderPreviewRows(m.defPickPrevLines, m.defPickPrevHl, re, sel.line, previewH, rowWidth) {
			b.WriteString(row + "\n")
		}
		b.WriteString(overlayHintStyle.Render(strings.Repeat("─", rowWidth)))
	}

	visible := min(len(m.defPickItems), maxRows)
	selected := input.Clamp(m.defPickIndex, 0, max(0, len(m.defPickItems)-1))
	start := 0
	if selected >= visible {
		start = selected - visible + 1
	}
	for i := start; i < start+visible && i < len(m.defPickItems); i++ {
		c := m.defPickItems[i]
		dir := path.Dir(c.rel)
		if dir == "." {
			dir = ""
		}
		name := fmt.Sprintf("%s:%d", path.Base(c.rel), c.line+1)
		b.WriteString("\n")
		if i == selected {
			row := padTo(truncateRunes(" "+name+"  "+dir, rowWidth), rowWidth)
			b.WriteString(selectedEntryStyle.Render(row))
			continue
		}
		row := suggestionFileStyle.Render(" " + name)
		rest := rowWidth - lipgloss.Width(row)
		if rest > 0 {
			b.WriteString(row + overlayHintStyle.Render(padTo(truncateRunes("  "+dir, rest), rest)))
		} else {
			b.WriteString(row)
		}
	}

	box := modalStyle.Width(boxWidth).Render(b.String())
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box,
		lipgloss.WithWhitespaceBackground(colBg))
}

// renderPreviewRows renders a syntax-colored window of lines with the target
// line ~40% down; matches of re render via the search pipeline, the first
// match on the target line in the focused (orange) style. Shared by the grep
// overlay and the definition/references picker.
func renderPreviewRows(lines []string, hl [][]view.HighlightSegment, re *regexp.Regexp,
	targetLine, height, innerW int) []string {
	blank := baseStyle.Render(strings.Repeat(" ", innerW))
	rows := make([]string, 0, height)
	start := input.Clamp(targetLine-height*4/10, 0, max(0, len(lines)-height))
	gutterW := len(strconv.Itoa(len(lines)))
	contentW := max(1, innerW-gutterW-3)
	for i := start; i < start+height; i++ {
		if i >= len(lines) {
			rows = append(rows, blank)
			continue
		}
		var segs []view.HighlightSegment
		if hl != nil && i < len(hl) {
			segs = hl[i]
		}
		var matches []view.LineMatch
		if re != nil {
			for _, loc := range re.FindAllStringIndex(lines[i], -1) {
				if loc[0] == loc[1] {
					continue
				}
				idx := -1 // gold
				if i == targetLine && len(matches) == 0 {
					idx = 0 // the target renders focused (orange)
				}
				matches = append(matches, view.LineMatch{
					SearchMatch: view.SearchMatch{LineIndex: i, Start: loc[0], End: loc[1]},
					MatchIndex:  idx,
				})
			}
		}
		num := gutterStyle.Render(pad(strconv.Itoa(i+1), gutterW) + " │ ")
		rows = append(rows, num+renderSearchLine(lines[i], segs, matches, 0, 0, contentW))
	}
	return rows
}

// renderGrepOverlay draws the repo-wide content search: top ~60% is a
// syntax-colored preview of the selected hit (match highlighted), bottom ~40%
// is the query input plus the result list.
func (m Model) renderGrepOverlay(width, height int) string {
	boxWidth := input.Clamp(width*9/10, 40, max(40, width-2))
	boxHeight := max(12, height-2)
	innerW := max(1, boxWidth-2)
	innerH := max(8, boxHeight-2)
	previewH := innerH * 6 / 10
	listH := innerH - previewH - 1 // one divider row

	blank := baseStyle.Render(strings.Repeat(" ", innerW))
	rows := make([]string, 0, innerH)

	// --- top: preview of the selected hit ---
	re := view.CreateMultilineSearchRegex(m.grepQuery)
	_, hit, ok := m.grepSelectedFile()
	if ok && m.grepPrevLines != nil {
		rows = append(rows, renderPreviewRows(m.grepPrevLines, m.grepHl, re, hit.line, previewH, innerW)...)
	} else {
		msg := "Type at least 2 characters to search the repo."
		switch {
		case m.grepLoading:
			msg = "Indexing repository…"
		case m.grepResultsGen != m.grepSearchGen:
			msg = "Searching…"
		case len([]rune(m.grepQuery)) >= 2:
			msg = "No matches."
		}
		rows = append(rows, overlayHintStyle.Render(padTo(" "+msg, innerW)))
		for len(rows) < previewH {
			rows = append(rows, blank)
		}
	}

	rows = append(rows, overlayHintStyle.Render(strings.Repeat("─", innerW)))

	// --- bottom: query input + result rows ---
	count := fmt.Sprintf("%d results", len(m.grepResults))
	switch {
	case m.grepLoading:
		count = fmt.Sprintf("indexing %d/%d files…", len(m.grepFiles), len(m.corpus))
	case m.grepResultsGen != m.grepSearchGen:
		count = "searching…"
	}
	inputRow := promptStyle.Render("grep ") + renderInputLine(m.grepQuery, len([]rune(m.grepQuery)))
	if pad := innerW - lipgloss.Width(inputRow) - lipgloss.Width(count); pad > 0 {
		inputRow += statusTextStyle.Render(strings.Repeat(" ", pad)) + overlayHintStyle.Render(count)
	}
	rows = append(rows, inputRow)

	visible := min(len(m.grepResults), listH-1)
	selected := input.Clamp(m.grepIndex, 0, max(0, len(m.grepResults)-1))
	start := 0
	if selected >= visible && visible > 0 {
		start = selected - visible + 1
	}
	for i := start; i < start+visible && i < len(m.grepResults); i++ {
		h := m.grepResults[i]
		dir := path.Dir(h.rel)
		if dir == "." {
			dir = ""
		}
		name := fmt.Sprintf("%s:%d", path.Base(h.rel), h.line+1)
		if i == selected {
			rows = append(rows, selectedEntryStyle.Render(padTo(truncateRunes(" "+name+"  "+dir, innerW), innerW)))
			continue
		}
		row := suggestionFileStyle.Render(" " + name)
		if rest := innerW - lipgloss.Width(row); rest > 0 {
			row += overlayHintStyle.Render(padTo(truncateRunes("  "+dir, rest), rest))
		}
		rows = append(rows, row)
	}
	for len(rows) < innerH {
		rows = append(rows, blank)
	}

	box := modalStyle.Width(boxWidth).Render(strings.Join(rows[:innerH], "\n"))
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box,
		lipgloss.WithWhitespaceBackground(colBg))
}

// renderFuzzyRow renders one candidate path, bolding the matched rune
// positions; the selected row is reversed whole.
func renderFuzzyRow(path string, positions []int, width int, selected bool) string {
	if selected {
		return selectedEntryStyle.Render(padTo(truncateRunes(" "+path, width), width))
	}
	matched := make(map[int]bool, len(positions))
	for _, p := range positions {
		matched[p] = true
	}
	runes := []rune(path)
	var b strings.Builder
	b.WriteString(baseStyle.Render(" "))
	rendered := 1
	for i, r := range runes {
		if rendered >= width {
			break
		}
		if matched[i] {
			b.WriteString(fuzzyBoldStyle.Render(string(r)))
		} else {
			b.WriteString(baseStyle.Render(string(r)))
		}
		rendered++
	}
	if pad := width - rendered; pad > 0 {
		b.WriteString(baseStyle.Render(strings.Repeat(" ", pad)))
	}
	return b.String()
}
