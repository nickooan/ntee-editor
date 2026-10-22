package app

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// TestPaneLinesNeverWrapIntoTheGutter pins the pane width guard: tabs and
// wide runes take more columns than the rune-based row windowing counts, and
// a row wider than the pane wrapped its tail onto the next row's line number.
func TestPaneLinesNeverWrapIntoTheGutter(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m.width = 140 // wide enough for the status hints, so only the panes are measured
	m = m.openFileAt("main.go")
	m.edit.lines = []string{
		"\t\t\t\t\tdeep\ttabs\tline " + strings.Repeat("中文", 40) + " end",
		"second",
	}
	m = m.refreshFileHighlights()
	view := m.View().Content
	lines := strings.Split(view, "\n")
	if len(lines) != m.height {
		t.Fatalf("frame has %d lines, want %d — a row wrapped", len(lines), m.height)
	}
	for i, line := range lines {
		if width := lipgloss.Width(line); width > m.width {
			t.Fatalf("line %d is %d columns wide, terminal is %d", i, width, m.width)
		}
	}
	// The row right under line 1 must be line 2 — a wrapped tail would sit
	// there instead, in the line-number column.
	plain := strings.Split(ansi.Strip(view), "\n")
	for i, line := range plain {
		if strings.Contains(line, " 1 │ ") {
			if i+1 >= len(plain) || !strings.Contains(plain[i+1], " 2 │ second") {
				t.Fatalf("the row under line 1 is not line 2:\n%s\n%s", line, plain[i+1])
			}
			return
		}
	}
	t.Fatalf("line 1 not drawn:\n%s", ansi.Strip(view))
}

func TestFitPaneLines(t *testing.T) {
	styled := lipgloss.NewStyle().Bold(true).Render("中文中文") + "tail"
	got := fitPaneLines("short\n"+styled, 6)
	parts := strings.Split(got, "\n")
	if parts[0] != "short" || lipgloss.Width(parts[1]) > 6 || !strings.HasPrefix(ansi.Strip(parts[1]), "中文中") {
		t.Fatalf("fitPaneLines = %q", got)
	}
}
