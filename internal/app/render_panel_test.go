package app

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/nickooan/ntee-editor/internal/lsp"
	"github.com/nickooan/ntee-editor/internal/store"
)

// frameHasRow reports whether one line of a stripped frame contains every
// part, in order — a table row's cells.
func frameHasRow(frame string, parts ...string) bool {
	for _, line := range strings.Split(frame, "\n") {
		rest, ok := line, true
		for _, part := range parts {
			index := strings.Index(rest, part)
			if index < 0 {
				ok = false
				break
			}
			rest = rest[index+len(part):]
		}
		if ok {
			return true
		}
	}
	return false
}

func inspectPanelModel(t *testing.T, menu int) Model {
	t.Helper()
	m, _ := newTestModel(t, nil)
	m.mode = modeInspect
	m.inspectMenu = menu
	m.inspectInfo = store.DBInfo{Records: 12, MainBytes: 1000, LiveBytes: 400, BlobTotalBytes: 2000, BlobLiveBytes: 1800, Generations: 1}
	m.lsp = &fakeRegistry{statuses: []lsp.LangStatus{{Lang: "go", State: lsp.LangRunning}}}
	return m
}

func TestInspectPanelsAreTables(t *testing.T) {
	cases := []struct {
		menu    int
		headers [2]string
		legend  []string
	}{
		{inspectMenuDB, [2]string{"METRIC", "VALUE"}, []string{"db compact", "db relieve"}},
		{inspectMenuLSP, [2]string{"LANGUAGE", "STATUS"}, []string{"lsp enable <lang|all>", "lsp disable <lang|all>"}},
		{inspectMenuSystem, [2]string{"SETTING", "VALUE"}, []string{"syscolor <name>"}},
	}
	for _, tc := range cases {
		m := inspectPanelModel(t, tc.menu)
		out := ansi.Strip(m.renderInspectMain(80, 20))
		for _, want := range append([]string{"╭", "├", "╰", "Commands"}, tc.legend...) {
			if !strings.Contains(out, want) {
				t.Fatalf("panel %d missing %q:\n%s", tc.menu, want, out)
			}
		}
		if !frameHasRow(out, "│ "+tc.headers[0], "│ "+tc.headers[1]) {
			t.Fatalf("panel %d missing header row:\n%s", tc.menu, out)
		}
		for _, line := range strings.Split(out, "\n") {
			if width := ansi.StringWidth(line); width > 80 {
				t.Fatalf("panel %d line exceeds the width (%d): %q", tc.menu, width, line)
			}
		}
	}
}

func TestInspectDBPanelUsage(t *testing.T) {
	m := inspectPanelModel(t, inspectMenuDB)
	raw := m.renderInspectMain(90, 20)
	out := ansi.Strip(raw)
	if !frameHasRow(out, "│ main log", "1000B", "████░░░░░░") || !strings.Contains(out, "400B live · 600B dead (60%)") {
		t.Fatalf("main log should show size, bar, and live/dead split:\n%s", out)
	}
	if !strings.Contains(raw, panelBadStyle.Render("600B dead (60%)")) {
		t.Fatal("60% dead space should be flagged red")
	}
	if !strings.Contains(raw, panelDimStyle.Render("200B orphaned (10%)")) {
		t.Fatal("a small orphaned share should stay dim")
	}
	if strings.Contains(out, "run db relieve") {
		t.Fatal("one generation needs no relieve warning")
	}

	m.inspectInfo.Generations = 3
	if out := ansi.Strip(m.renderInspectMain(90, 20)); !frameHasRow(out, "│ generations", "3", "stray file — run db relieve") {
		t.Fatalf("stray generations should warn:\n%s", out)
	}
	m.inspectBusy = "compact"
	if out := ansi.Strip(m.renderInspectMain(90, 20)); !strings.Contains(out, "db compact running…") {
		t.Fatalf("a running maintenance op should show:\n%s", out)
	}
	m.inspectLoading = true
	if out := ansi.Strip(m.renderInspectMain(90, 20)); !strings.Contains(out, "Gathering store statistics…") || strings.Contains(out, "│ records") {
		t.Fatalf("loading should replace the rows:\n%s", out)
	}
}

func TestInspectLSPPanelGloballyDisabled(t *testing.T) {
	m := inspectPanelModel(t, inspectMenuLSP)
	m.lsp = &fakeRegistry{}
	if out := ansi.Strip(m.renderInspectMain(70, 20)); !strings.Contains(out, "LSP is disabled globally") {
		t.Fatalf("an empty registry should explain why:\n%s", out)
	}
}

func TestInspectSystemPanelListsOneStylePerLine(t *testing.T) {
	m := inspectPanelModel(t, inspectMenuSystem)
	m.cfg.Theme.Syntax = "nord"
	for _, width := range []int{120, 50} {
		out := ansi.Strip(m.renderInspectMain(width, 30))
		if !frameHasRow(out, "│ available", "gruvbox") || !frameHasRow(out, "│ ", "● nord", "current") {
			t.Fatalf("width %d: available should start on its row and mark the current style:\n%s", width, out)
		}
		for _, name := range syntaxStyles {
			if !frameHasRow(out, "│ ", name+" ") {
				t.Fatalf("width %d: %q should have its own line:\n%s", width, name, out)
			}
		}
		if strings.Contains(out, "● gruvbox") {
			t.Fatal("only the current style is marked")
		}
	}
}

func TestInspectPanelsFitTheirHeight(t *testing.T) {
	m := inspectPanelModel(t, inspectMenuSystem)
	m = m.focusStylePicker()
	for _, height := range []int{30, 20, 12} {
		out := ansi.Strip(m.renderInspectMain(60, height))
		if lines := strings.Count(out, "\n") + 1; lines > height {
			t.Fatalf("height %d: panel drew %d lines", height, lines)
		}
	}
	if out := ansi.Strip(m.renderInspectMain(60, 30)); !strings.Contains(out, "Commands") {
		t.Fatal("the legend should show when there is room")
	}
	if out := ansi.Strip(m.renderInspectMain(60, 20)); strings.Contains(out, "Commands") || !strings.Contains(out, "╰") {
		t.Fatalf("a tight panel drops the legend before the table:\n%s", out)
	}
}

func TestPanelCellFitsExactly(t *testing.T) {
	long := panelGoodStyle.Render("abcdefghij") + panelBadStyle.Render("klmnop")
	cell := panelCell(long, 8)
	if width := ansi.StringWidth(cell); width != 8 {
		t.Fatalf("cut cell width = %d, want 8", width)
	}
	if plain := ansi.Strip(cell); plain != "abcdefg…" {
		t.Fatalf("cut cell = %q", plain)
	}
	short := panelCell(panelGoodStyle.Render("ab"), 6)
	if width := ansi.StringWidth(short); width != 6 || ansi.Strip(short) != "ab    " {
		t.Fatalf("padded cell = %q (width %d)", ansi.Strip(short), width)
	}
}

func TestWrapWords(t *testing.T) {
	cases := []struct {
		text  string
		width int
		want  []string
	}{
		{"one two three", 7, []string{"one two", "three"}},
		{"abcdefghij", 4, []string{"abcd", "efgh", "ij"}},
		{"", 5, []string{""}},
	}
	for _, tc := range cases {
		got := wrapWords(tc.text, tc.width)
		if strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Errorf("wrapWords(%q, %d) = %q, want %q", tc.text, tc.width, got, tc.want)
		}
	}
}
