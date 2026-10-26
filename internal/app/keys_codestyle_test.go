package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/nickooan/ntee-editor/internal/config"
)

// codeStylePanel opens the inspect dashboard on the code style section with
// an isolated user config dir, returned for reading what got written.
func codeStylePanel(t *testing.T) (Model, string) {
	t.Helper()
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	m, _ := newTestModel(t, nil)
	m.width, m.height = 140, 50
	m = key(m, ctrlKey('t'))
	for m.inspectMenu != inspectMenuCodeStyle {
		m = key(m, shiftKey(tea.KeyDown))
	}
	return m, filepath.Join(xdg, "ntee-editor", "config.yaml")
}

// selectCodeStyleRow focuses the table and moves to a language's row.
func selectCodeStyleRow(t *testing.T, m Model, language string) Model {
	t.Helper()
	m = key(m, keyPress(tea.KeyRight))
	if !m.codeStylePicker.focused {
		t.Fatal("→ focuses the table")
	}
	for codeStyleRowLanguage(m.codeStylePicker.index) != language {
		m = key(m, keyPress(tea.KeyDown))
		if m.codeStylePicker.index >= codeStyleResetRow {
			t.Fatalf("no row for %q", language)
		}
	}
	return m
}

func TestCodeStylePanelShowsTheTable(t *testing.T) {
	m, _ := codeStylePanel(t)
	plain := ansi.Strip(m.View().Content)
	for _, want := range []string{"code style", "LANGUAGE", "TAB SIZE", "DESCRIPTION", "all files", "go", "4 · all files", ".ts .tsx", "reset all to defaults", "tab <language> <size>"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("panel is missing %q:\n%s", want, plain)
		}
	}
}

func TestCodeStyleSetsALanguageSize(t *testing.T) {
	m, configPath := codeStylePanel(t)
	m = selectCodeStyleRow(t, m, "go")
	m = key(m, keyPress(tea.KeyEnter))
	if !m.codeStylePicker.editing || m.codeStylePicker.input != "4" {
		t.Fatalf("Enter edits the inherited size: %+v", m.codeStylePicker)
	}
	m = key(m, keyPress(tea.KeyBackspace))
	m = key(m, typeRune('8'))
	m = key(m, keyPress(tea.KeyEnter))
	if m.cfg.Editor.LanguageTabWidths["go"] != 8 || m.errText != "" {
		t.Fatalf("go = %v err=%q", m.cfg.Editor.LanguageTabWidths, m.errText)
	}
	raw, err := os.ReadFile(configPath)
	if err != nil || !strings.Contains(string(raw), "go: 8") {
		t.Fatalf("config written: %s %v", raw, err)
	}
	if plain := ansi.Strip(m.View().Content); !strings.Contains(plain, "instead of 4") {
		t.Fatalf("a custom size names what it replaces:\n%s", plain)
	}

	m = key(m, typeRune('+'))
	if m.cfg.Editor.LanguageTabWidths["go"] != 9 {
		t.Fatalf("+ steps up: %v", m.cfg.Editor.LanguageTabWidths)
	}
	m = key(m, typeRune('-'))
	m = key(m, typeRune('-'))
	if m.cfg.Editor.LanguageTabWidths["go"] != 7 {
		t.Fatalf("- steps down: %v", m.cfg.Editor.LanguageTabWidths)
	}
	m = key(m, typeRune('r'))
	if _, custom := m.cfg.Editor.LanguageTabWidths["go"]; custom {
		t.Fatal("r lets go inherit again")
	}
}

func TestCodeStyleRefusesOutOfRangeSizes(t *testing.T) {
	m, _ := codeStylePanel(t)
	m = selectCodeStyleRow(t, m, "")
	m = key(m, keyPress(tea.KeyEnter))
	m = key(m, keyPress(tea.KeyBackspace))
	m = key(m, typeRune('2'))
	m = key(m, typeRune('0'))
	m = key(m, keyPress(tea.KeyEnter))
	if m.cfg.Editor.TabWidth != 4 || !strings.Contains(m.errText, "from 1 to 16") {
		t.Fatalf("20 is refused: width=%d err=%q", m.cfg.Editor.TabWidth, m.errText)
	}
	m.errText = ""
	m.cfg.Editor.TabWidth = 1
	m = key(m, typeRune('-'))
	if m.cfg.Editor.TabWidth != 1 || m.errText == "" {
		t.Fatal("0 is refused")
	}
}

func TestCodeStyleResetAllConfirmsFirst(t *testing.T) {
	m, _ := codeStylePanel(t)
	m = selectCodeStyleRow(t, m, "yaml")
	m = key(m, typeRune('-'))
	m = key(m, typeRune('-'))
	m = key(m, typeRune('R'))
	if !m.codeStylePicker.confirmReset {
		t.Fatal("R asks first")
	}
	if plain := ansi.Strip(m.View().Content); !strings.Contains(plain, "Reset all 1 custom tab sizes?") {
		t.Fatalf("the reset row turns into the prompt:\n%s", plain)
	}
	m = key(m, typeRune('n'))
	if m.cfg.Editor.LanguageTabWidths["yaml"] != 2 {
		t.Fatal("n keeps the sizes")
	}
	m = key(m, typeRune('R'))
	m = key(m, typeRune('y'))
	if len(m.cfg.Editor.LanguageTabWidths) != 0 || m.cfg.Editor.TabWidth != 4 {
		t.Fatalf("y resets: %+v", m.cfg.Editor)
	}
}

func TestCodeStyleProjectSizesAreRefused(t *testing.T) {
	m, _ := codeStylePanel(t)
	m.cfg.ProjectCodeStyle = config.ProjectCodeStyle{Languages: map[string]bool{"yaml": true}}
	if plain := ansi.Strip(m.View().Content); !strings.Contains(plain, "project · YAML") {
		t.Fatalf("a project-set row is tagged:\n%s", plain)
	}
	m = selectCodeStyleRow(t, m, "yaml")
	m = key(m, keyPress(tea.KeyEnter))
	if m.codeStylePicker.editing || !strings.Contains(m.errText, ".ntee-editor.yaml") {
		t.Fatalf("editing it is refused: %q", m.errText)
	}
}

func TestCodeStyleBarCommands(t *testing.T) {
	m, _ := codeStylePanel(t)
	run := func(command string) Model {
		t.Helper()
		next, _ := m.runInspectCommand(command)
		return next.(Model)
	}
	m = run("tab 2")
	if m.cfg.Editor.TabWidth != 2 {
		t.Fatalf("tab 2: %d (%s)", m.cfg.Editor.TabWidth, m.errText)
	}
	m = run("tab Go 8")
	if m.cfg.Editor.LanguageTabWidths["go"] != 8 {
		t.Fatalf("tab go 8: %v (%s)", m.cfg.Editor.LanguageTabWidths, m.errText)
	}
	m = run("tab go default")
	if _, custom := m.cfg.Editor.LanguageTabWidths["go"]; custom {
		t.Fatal("tab go default clears it")
	}
	if m = run("tab cobol 3"); !strings.Contains(m.errText, "unknown language") {
		t.Fatalf("unknown language: %q", m.errText)
	}
	m.errText = ""
	m = run("tab reset")
	if m.cfg.Editor.TabWidth != 4 {
		t.Fatalf("tab reset: %d", m.cfg.Editor.TabWidth)
	}
}

func TestTabKeyInsertsTheLanguageSize(t *testing.T) {
	m, root := newTestModel(t, nil)
	must(t, os.WriteFile(filepath.Join(root, "notes.md"), []byte("x\n"), 0o644))
	m = rebuildCorpusNow(m)
	m.cfg.Editor.TabWidth = 2
	m.cfg.Editor.LanguageTabWidths = map[string]int{"go": 8}

	m = m.openFileAt("main.go")
	m.mode = modeEdit
	m.edit.cy, m.edit.cx = 0, 0
	m = key(m, keyPress(tea.KeyTab))
	if got := m.edit.lines[0]; got != strings.Repeat(" ", 8)+"package main" {
		t.Fatalf("a Go file indents 8: %q", got)
	}

	m = m.openFileAt("notes.md")
	m.mode = modeEdit
	m.edit.cy, m.edit.cx = 0, 0
	m = key(m, keyPress(tea.KeyTab))
	if got := m.edit.lines[0]; got != "  x" {
		t.Fatalf("markdown uses all files' size: %q", got)
	}
}

func TestTabCharactersDrawAtTheLanguageSize(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = m.openFileAt("main.go")
	m.edit.lines = []string{"\tx := 1", "y"}
	m.edit.rev++ // the buffer changed: rescan the highlights
	m.edit.cy = 1
	m.mode = modeEdit
	m = m.refreshFileHighlights()

	firstRow := func(m Model) string {
		return strings.Split(ansi.Strip(m.renderFile(60, 5)), "\n")[0]
	}
	if got := firstRow(m); !strings.Contains(got, "│ "+strings.Repeat(" ", 4)+"x := 1") {
		t.Fatalf("the default draws a tab 4 wide: %q", got)
	}
	m.cfg.Editor.LanguageTabWidths = map[string]int{"go": 8}
	if got := firstRow(m); !strings.Contains(got, "│ "+strings.Repeat(" ", 8)+"x := 1") {
		t.Fatalf("go: 8 draws a tab 8 wide: %q", got)
	}
	m.cfg.Editor.LanguageTabWidths = map[string]int{"go": 2}
	m.edit.cy = 0 // the cursor row has its own renderer
	m.edit.cx = 1
	if got := firstRow(m); !strings.Contains(got, "│ "+strings.Repeat(" ", 2)+"x := 1") {
		t.Fatalf("the cursor row draws a tab 2 wide too: %q", got)
	}
}

func TestReviewTabsExpandToTheFileSize(t *testing.T) {
	document := diffDocument{lines: []string{"\tx"}, rows: []diffRow{{text: "\tx"}}}
	got := expandDocumentTabs(document, 8)
	if got.lines[0] != strings.Repeat(" ", 8)+"x" || got.rows[0].text != strings.Repeat(" ", 8)+"x" {
		t.Fatalf("expanded: %q / %q", got.lines[0], got.rows[0].text)
	}
}
