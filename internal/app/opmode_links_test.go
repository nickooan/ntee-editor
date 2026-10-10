package app

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/nickooan/ntee-editor/internal/store"
)

func TestFindOutputLinks(t *testing.T) {
	cases := []struct {
		line string
		want []opLink
	}{
		{"no links here", nil},
		{"example.com is not a link", nil},
		{"https://go.dev", []opLink{{0, 14, "https://go.dev"}}},
		{"see http://localhost:8080/path?q=1#frag now", []opLink{{4, 39, "http://localhost:8080/path?q=1#frag"}}},
		{"done: https://ci.dev/run/42.", []opLink{{6, 27, "https://ci.dev/run/42"}}},
		{"a https://a.dev, b https://b.dev", []opLink{{2, 15, "https://a.dev"}, {19, 32, "https://b.dev"}}},
		{"(see https://x.dev/a)", []opLink{{5, 20, "https://x.dev/a"}}},
		{"https://en.wikipedia.org/wiki/Go_(language)", []opLink{{0, 43, "https://en.wikipedia.org/wiki/Go_(language)"}}},
		{`url="https://q.dev/x"`, []opLink{{5, 20, "https://q.dev/x"}}},
		{"héllo https://é.dev/ü", []opLink{{6, 21, "https://é.dev/ü"}}},
		{"bare https:// alone", nil},
	}
	for _, tc := range cases {
		if got := findOutputLinks(tc.line); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("findOutputLinks(%q) = %+v, want %+v", tc.line, got, tc.want)
		}
	}
}

// frameCell finds the screen cell where text last appears in the rendered
// frame — the output sits below the "$ command" header, which may echo the
// same URL. Columns are counted in runes (every frame rune is one cell here).
func frameCell(t *testing.T, m Model, text string) (int, int) {
	t.Helper()
	lines := strings.Split(ansi.Strip(m.render()), "\n")
	for y := len(lines) - 1; y >= 0; y-- {
		if index := strings.Index(lines[y], text); index >= 0 {
			return utf8.RuneCountInString(lines[y][:index]), y
		}
	}
	t.Fatalf("%q not on screen:\n%s", text, ansi.Strip(m.render()))
	return 0, 0
}

func leftClick(m Model, x, y int) (Model, tea.Cmd) {
	next, cmd := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	return next.(Model), cmd
}

// linkRunFixture runs command in the Ctrl+R overlay and records opened links.
func linkRunFixture(t *testing.T, command string) (Model, *[]string) {
	t.Helper()
	m, _ := opModeFixture(t, store.OpCommand{Name: "links", Command: command})
	opened := &[]string{}
	m.openBrowser = func(link string) error {
		*opened = append(*opened, link)
		return nil
	}
	m = press(t, m, ctrlKey('r'))
	m = press(t, m, keyPress(tea.KeyEnter))
	if m.opMode.run == nil || !m.opMode.run.finished {
		t.Fatal("command should have finished")
	}
	return m, opened
}

func TestOpRunClickOpensLink(t *testing.T) {
	m, opened := linkRunFixture(t, "printf 'first line\\nsee https://example.com/a for docs\\n'")
	if frame := ansi.Strip(m.render()); !strings.Contains(frame, "click a link to open it") {
		t.Fatalf("footer should say links are clickable:\n%s", frame)
	}
	if !strings.Contains(m.render(), opLinkStyle.Render("https://example.com/a")) {
		t.Fatal("the link should be drawn underlined in the link color")
	}
	x, y := frameCell(t, m, "https://example.com/a")
	if _, header := frameCell(t, m, "$ printf"); y <= header {
		t.Fatal("the link under test must be in the output, not the header")
	}
	m, cmd := leftClick(m, x+3, y)
	if cmd == nil || m.notice != "opening https://example.com/a" {
		t.Fatalf("clicking the link should open it: notice=%q", m.notice)
	}
	m, _ = deliver(m, cmd())
	if len(*opened) != 1 || (*opened)[0] != "https://example.com/a" {
		t.Fatalf("opened = %q", *opened)
	}
	if m.errText != "" {
		t.Fatalf("unexpected error: %q", m.errText)
	}

	x, y = frameCell(t, m, "for docs")
	if _, cmd := leftClick(m, x, y); cmd != nil {
		t.Fatal("clicking plain text must not open anything")
	}
	x, y = frameCell(t, m, "first line")
	if _, cmd := leftClick(m, x, y); cmd != nil {
		t.Fatal("clicking a line without links must not open anything")
	}
	if len(*opened) != 1 {
		t.Fatalf("opened = %q", *opened)
	}
}

func TestOpRunClickOpensFullURLWhenClipped(t *testing.T) {
	long := "https://example.com/" + strings.Repeat("segment/", 20) + "end"
	m, opened := linkRunFixture(t, "echo "+long)
	x, y := frameCell(t, m, "https://example.com/segment")
	_, cmd := leftClick(m, x+1, y)
	if cmd == nil {
		t.Fatal("clicking the visible part of a clipped link should open it")
	}
	cmd()
	if len(*opened) != 1 || (*opened)[0] != long {
		t.Fatalf("should open the full URL, opened = %q", *opened)
	}
}

func TestOpRunClickOpenErrorIsReported(t *testing.T) {
	m, _ := linkRunFixture(t, "echo https://example.com/x")
	m.openBrowser = func(string) error { return errors.New("no browser") }
	x, y := frameCell(t, m, "https://example.com/x")
	m, cmd := leftClick(m, x, y)
	m, _ = deliver(m, cmd())
	if m.errText != "open link: no browser" {
		t.Fatalf("errText = %q", m.errText)
	}
}

func TestOpRunClickWhileRunning(t *testing.T) {
	m, _ := opModeFixture(t)
	opened := ""
	m.openBrowser = func(link string) error { opened = link; return nil }
	m.opMode = opModeState{open: true, stage: opStageRun, run: newOpRun("long-running", m.workspaceRoot)}
	m.opMode.run.output.write([]byte("serving at http://localhost:3000/\n"))
	x, y := frameCell(t, m, "http://localhost:3000/")
	_, cmd := leftClick(m, x, y)
	if cmd == nil {
		t.Fatal("links should be clickable while the command is still running")
	}
	cmd()
	if opened != "http://localhost:3000/" {
		t.Fatalf("opened = %q", opened)
	}
}

func TestOpOverlayIgnoresClicksOutsideRunStage(t *testing.T) {
	m, _ := opModeFixture(t, store.OpCommand{Name: "x", Command: "echo https://example.com"})
	m = press(t, m, ctrlKey('r'))
	if _, cmd := leftClick(m, 50, 5); cmd != nil {
		t.Fatal("the picker has no clickable links")
	}
}
