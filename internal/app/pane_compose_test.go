package app

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// composeFrameWithLipgloss is the composition composeFrame replaces: each
// pane rendered by paneStyle, joined by lipgloss. composeFrame must match it
// byte for byte.
func composeFrameWithLipgloss(pieces framePieces) string {
	sidebar := paneStyle.Width(pieces.sidebarWidth).Height(pieces.bodyHeight).Render(fitPaneLinesBefore(pieces.sidebarBody, pieces.sidebarWidth-4))
	mainPane := paneStyle.Width(pieces.mainWidth).Height(pieces.bodyHeight).Render(fitPaneLinesBefore(pieces.mainBody, pieces.mainWidth-4))
	body := lipgloss.JoinHorizontal(lipgloss.Top, sidebar, mainPane)
	return lipgloss.JoinVertical(lipgloss.Left, pieces.header, body, pieces.status)
}

// fitPaneLinesBefore is fitPaneLines before composeFrame: it measured tabs
// as lipgloss does not draw them, so only bodies whose tab lines fit match.
func fitPaneLinesBefore(body string, width int) string {
	if width < 1 {
		return body
	}
	lines := strings.Split(body, "\n")
	for index, line := range lines {
		if ansi.StringWidth(line) > width {
			lines[index] = ansi.Truncate(line, width, "")
		}
	}
	return strings.Join(lines, "\n")
}

func requireSameFrame(t *testing.T, name string, pieces framePieces) {
	t.Helper()
	got, want := composeFrame(pieces), composeFrameWithLipgloss(pieces)
	if got == want {
		return
	}
	gotRows, wantRows := strings.Split(got, "\n"), strings.Split(want, "\n")
	for index := range max(len(gotRows), len(wantRows)) {
		var gotRow, wantRow string
		if index < len(gotRows) {
			gotRow = gotRows[index]
		}
		if index < len(wantRows) {
			wantRow = wantRows[index]
		}
		if gotRow != wantRow {
			t.Fatalf("%s: row %d of %d/%d differs\n got  %q\n want %q", name, index, len(gotRows), len(wantRows), gotRow, wantRow)
		}
	}
}

func TestComposeFrameMatchesLipgloss(t *testing.T) {
	styled := lipgloss.NewStyle().Foreground(lipgloss.Color("#ff0000")).Render("red") + "\x1b[0m plain " + lipgloss.NewStyle().Bold(true).Background(lipgloss.Color("#00ff00")).Render("bold")
	bodies := map[string]string{
		"empty":        "",
		"one line":     "hello",
		"blank lines":  "\n\nmiddle\n\n",
		"trailing ws":  "spaces   \n  lead",
		"styled":       styled + "\n" + styled,
		"wide runes":   "中文中文中文中文中文中文中文中文中文中文中文中文中文中文\n😀 emoji 😀",
		"too wide":     strings.Repeat("x", 300),
		"too tall":     strings.Repeat("row\n", 80),
		"exact width":  strings.Repeat("y", 26),
		"crlf":         "one\r\ntwo",
		"cut styled":   strings.Repeat(styled, 10),
		"reset inside": "\x1b[31mred\x1b[m after reset",
	}
	headers := map[string]string{
		"plain header":  "ntee · project",
		"styled header": lipgloss.NewStyle().Background(lipgloss.Color("#111111")).Render(strings.Repeat(" ", 60)),
		"two rows":      "first\nsecond row",
		"wider header":  strings.Repeat("h", 120),
	}
	for bodyName, body := range bodies {
		for headerName, header := range headers {
			for _, size := range [][3]int{{30, 70, 20}, {12, 88, 6}, {3, 3, 3}, {30, 2, 1}, {5, 40, 2}} {
				pieces := framePieces{
					header: header, status: "status · " + headerName,
					sidebarBody: body, mainBody: body + "\n" + body,
					sidebarWidth: size[0], mainWidth: size[1], bodyHeight: size[2],
				}
				requireSameFrame(t, fmt.Sprintf("%s / %s / %v", bodyName, headerName, size), pieces)
			}
		}
	}
}

func TestComposeFrameDrawsTabsAsThePaneDoes(t *testing.T) {
	pieces := framePieces{header: "h", status: "s", sidebarBody: "a\tb\n\tindented", mainBody: "x\ty", sidebarWidth: 30, mainWidth: 40, bodyHeight: 6}
	requireSameFrame(t, "tabs that fit", pieces)

	// A tab line that only fit while its tabs measured 0 cells used to wrap
	// inside the pane, spilling its tail into the next row; it is cut now.
	pieces.sidebarWidth = 12
	rows := strings.Split(ansi.Strip(composeFrame(pieces)), "\n")
	if len(rows) != 1+6+1 || !strings.HasPrefix(rows[3], "│    inde  │") {
		t.Fatalf("the tab line is cut at the pane edge, the frame keeps its height:\n%s", strings.Join(rows, "\n"))
	}
}

func TestComposeFrameMatchesLipglossForRealFrames(t *testing.T) {
	edit, _ := newTestModel(t, nil)
	edit = edit.openFileAt("main.go")
	query, _ := newTestModel(t, nil)
	fuzzy := key(query, ctrlKey('p'))
	review, _, _ := startedReviewSession(t)
	staged, _ := liveAutoDriveReview(t)
	for name, m := range map[string]Model{"edit": edit, "query": query, "fuzzy overlay": fuzzy, "review": review, "staged live review": staged} {
		for _, size := range [][2]int{{100, 30}, {200, 60}, {44, 12}} {
			m.width, m.height = size[0], size[1]
			requireSameFrame(t, fmt.Sprintf("%s %v", name, size), m.renderFramePieces())
		}
	}
	if !strings.Contains(ansi.Strip(edit.View().Content), "func main()") {
		t.Fatal("the composed frame draws the file")
	}
}
