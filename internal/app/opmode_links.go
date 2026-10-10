package app

import (
	"regexp"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
)

// opLink is one http(s) URL in an output line; start/end are rune offsets.
type opLink struct {
	start, end int
	url        string
}

var opLinkPattern = regexp.MustCompile("https?://[^\\s<>\"'`]+")

// findOutputLinks finds the http(s) URLs in a display line. Sentence
// punctuation after a URL is not part of it, and a closing bracket is kept
// only when the URL opened it — "(see https://x.dev/a)" vs
// "https://en.wikipedia.org/wiki/Go_(language)".
func findOutputLinks(line string) []opLink {
	locations := opLinkPattern.FindAllStringIndex(line, -1)
	if len(locations) == 0 {
		return nil
	}
	links := make([]opLink, 0, len(locations))
	for _, location := range locations {
		link := trimLinkTail(line[location[0]:location[1]])
		if len(link) <= len("https://") {
			continue
		}
		start := utf8.RuneCountInString(line[:location[0]])
		links = append(links, opLink{start: start, end: start + utf8.RuneCountInString(link), url: link})
	}
	return links
}

func trimLinkTail(link string) string {
	for len(link) > 0 {
		last := link[len(link)-1]
		switch {
		case strings.IndexByte(".,;:!?", last) >= 0:
			link = link[:len(link)-1]
		case last == ')' && strings.Count(link, "(") < strings.Count(link, ")"),
			last == ']' && strings.Count(link, "[") < strings.Count(link, "]"),
			last == '}' && strings.Count(link, "{") < strings.Count(link, "}"):
			link = link[:len(link)-1]
		default:
			return link
		}
	}
	return link
}

type opLinkOpenedMsg struct{ err error }

// handleOpMouse gives the Ctrl+R overlay the mouse: a left click on a link in
// the run output opens it in the browser; everything else is ignored.
func (m Model) handleOpMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	click, ok := msg.(tea.MouseClickMsg)
	if !ok || click.Button != tea.MouseLeft || m.opMode.stage != opStageRun || m.opMode.run == nil {
		return m, nil
	}
	link, ok := m.opRunLinkAt(click.X, click.Y)
	if !ok {
		return m, nil
	}
	m.notice = "opening " + link
	openBrowser := m.openBrowser
	return m, func() tea.Msg { return opLinkOpenedMsg{err: openBrowser(link)} }
}

// opRunLinkAt maps a terminal cell to the link drawn there, mirroring
// render(): the main pane's content starts at (sidebarWidth+1, 2), the overlay
// is centered in it the way lipgloss.Place centers (floor of half the gap),
// and the box adds a border plus one column of padding.
func (m Model) opRunLinkAt(x, y int) (string, bool) {
	run := m.opMode.run
	placeWidth := max(3, m.width-m.sidebarWidth()) - 4
	placeHeight := m.bodyHeight() - 2
	layout := newOpRunLayout(run, placeWidth, placeHeight)
	boxHeight := len(layout.header) + layout.outputHeight + 3 + 2 // dividers, footer, border
	boxLeft := m.sidebarWidth() + 1 + max(0, placeWidth-(layout.boxWidth+2))/2
	boxTop := 2 + max(0, placeHeight-boxHeight)/2

	row := y - (boxTop + 1 + len(layout.header) + 1)
	column := x - (boxLeft + 2)
	if row < 0 || row >= layout.outputHeight || column < 0 || column >= layout.innerWidth {
		return "", false
	}
	lines := run.output.displayLines()
	start, end := layout.visibleRange(len(lines), run.scroll)
	if start+row >= end {
		return "", false
	}
	for _, link := range findOutputLinks(opDisplayLine(lines[start+row])) {
		if column >= link.start && column < link.end {
			return link.url, true
		}
	}
	return "", false
}
