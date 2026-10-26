package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLanguageForPath(t *testing.T) {
	cases := map[string]string{
		"main.go":              "go",
		"web/App.TSX":          "typescript",
		"lib/a.mjs":            "javascript",
		"Makefile":             "makefile",
		"build/rules.mk":       "makefile",
		"config/ci.yml":        "yaml",
		"docs/README.md":       "markdown",
		"src/x.hpp":            "cpp",
		"notes.txt":            "",
		"LICENSE":              "",
		"dir.go/no-extension":  "",
		"styles/theme.scss":    "css",
		"scripts/deploy.bash":  "shell",
		"deep/path/Program.cs": "csharp",
	}
	for path, want := range cases {
		if got := LanguageForPath(path); got != want {
			t.Errorf("LanguageForPath(%q) = %q, want %q", path, got, want)
		}
	}
	for _, language := range CodeStyleLanguages {
		if !IsCodeStyleLanguage(language.Key) {
			t.Errorf("%s is not registered", language.Key)
		}
	}
}

func TestTabWidthForInheritsOrOverrides(t *testing.T) {
	cfg := Default()
	cfg.Editor.TabWidth = 2
	cfg.Editor.LanguageTabWidths = map[string]int{"go": 8}
	if got := cfg.TabWidthFor("a/main.go"); got != 8 {
		t.Fatalf("go override = %d", got)
	}
	if got := cfg.TabWidthFor("a/x.yaml"); got != 2 {
		t.Fatalf("yaml inherits = %d", got)
	}
	if got := cfg.TabWidthFor("notes.txt"); got != 2 {
		t.Fatalf("no language inherits = %d", got)
	}
}

func TestLoadValidatesTabWidths(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(xdg, "ntee-editor"), 0o755))
	must(os.WriteFile(filepath.Join(xdg, "ntee-editor", "config.yaml"), []byte(
		"editor:\n  tab_width: 40\n  language_tab_widths: {go: 8, yaml: 0, cobol: 4}\n"), 0o644))
	cfg, warnings := LoadWithWarnings(t.TempDir())
	if cfg.Editor.TabWidth != DefaultTabWidth {
		t.Fatalf("an out-of-range global falls back: %d", cfg.Editor.TabWidth)
	}
	if len(cfg.Editor.LanguageTabWidths) != 1 || cfg.Editor.LanguageTabWidths["go"] != 8 {
		t.Fatalf("only valid overrides stay: %v", cfg.Editor.LanguageTabWidths)
	}
	joined := strings.Join(warnings, "\n")
	for _, want := range []string{"tab_width 40", "yaml 0", "unknown language cobol"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings miss %q:\n%s", want, joined)
		}
	}
}

func TestProjectCodeStyleMarksWhatTheProjectSets(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, ".ntee-editor.yaml"), []byte(
		"editor:\n  language_tab_widths: {yaml: 2}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Load(project)
	if cfg.ProjectCodeStyle.TabWidth || !cfg.ProjectCodeStyle.Languages["yaml"] || cfg.ProjectCodeStyle.Languages["go"] {
		t.Fatalf("project code style = %+v", cfg.ProjectCodeStyle)
	}
	if cfg.TabWidthFor("ci.yml") != 2 {
		t.Fatal("the project's size applies")
	}
}

func TestSetTabWidthsRoundTrips(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path, err := SetTabWidths(2, map[string]int{"go": 8})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "language_tab_widths:") || !strings.Contains(string(raw), "go: 8") {
		t.Fatalf("written file:\n%s", raw)
	}
	cfg := Load(t.TempDir())
	if cfg.Editor.TabWidth != 2 || cfg.TabWidthFor("x.go") != 8 || cfg.Editor.MaxHighlightKB != 512 {
		t.Fatalf("loaded back: %+v", cfg.Editor)
	}
	if _, err := SetTabWidths(4, nil); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path)
	if !strings.Contains(string(raw), "language_tab_widths: {}") {
		t.Fatalf("no overrides are written explicitly:\n%s", raw)
	}
}

func TestConfigRewriteKeepsAbsentEditorFieldsAtTheirDefaults(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := filepath.Join(dir, "ntee-editor", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("theme: {syntax: nord}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := SetThemeSyntax("dracula"); err != nil {
		t.Fatal(err)
	}
	if cfg := Load(t.TempDir()); cfg.Editor.MaxHighlightKB != 512 || cfg.Editor.MaxSnapshots != 50 {
		t.Fatalf("an absent editor section is not rewritten as zeros: %+v", cfg.Editor)
	}

	if err := os.WriteFile(path, []byte("editor: {max_highlight_kb: 0}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := SetThemeSyntax("nord"); err != nil {
		t.Fatal(err)
	}
	if cfg := Load(t.TempDir()); cfg.Editor.MaxHighlightKB != 0 {
		t.Fatalf("a zero the user wrote stays: %d", cfg.Editor.MaxHighlightKB)
	}
}
