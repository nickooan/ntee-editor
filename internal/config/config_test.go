package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func TestDefaultTypescriptHandlesJS(t *testing.T) {
	ts := Default().Languages["typescript"]
	for _, want := range []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs"} {
		if !contains(ts.Extensions, want) {
			t.Errorf("default typescript extensions missing %q: %v", want, ts.Extensions)
		}
	}
}

func TestUnionStrings(t *testing.T) {
	got := unionStrings([]string{".ts", ".tsx"}, []string{".tsx", ".vue"})
	want := []string{".ts", ".tsx", ".vue"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func writeUserConfigFile(t *testing.T, xdgDir, content string) {
	t.Helper()
	cfgDir := filepath.Join(xdgDir, "ntee-editor")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadUnionsExtensionsAndOverlaysLSP(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg) // isolate from the real ~/.config
	// The lsp overlay must come from the USER config (only it may name
	// executables); the project file contributes the extensions union.
	writeUserConfigFile(t, xdg, ""+
		"languages:\n"+
		"  typescript:\n"+
		"    lsp:\n"+
		"      command: \"/custom/tsls\"\n")
	root := t.TempDir()
	yaml := "" +
		"languages:\n" +
		"  typescript:\n" +
		"    extensions: [\".vue\"]\n"
	if err := os.WriteFile(filepath.Join(root, ".ntee-editor.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := Load(root)
	ts := cfg.Languages["typescript"]

	// Extensions unioned with the built-in defaults (not replaced).
	for _, want := range []string{".ts", ".tsx", ".js", ".vue"} {
		if !contains(ts.Extensions, want) {
			t.Errorf("typescript extensions missing %q: %v", want, ts.Extensions)
		}
	}
	// Command overridden by the user file; args kept from the default (overlay).
	if ts.LSP.Command != "/custom/tsls" {
		t.Errorf("command = %q, want /custom/tsls", ts.LSP.Command)
	}
	if len(ts.LSP.Args) != 1 || ts.LSP.Args[0] != "--stdio" {
		t.Errorf("args should keep default --stdio, got %v", ts.LSP.Args)
	}
	// An untouched default language survives.
	if cfg.Languages["go"].LSP.Command != "gopls" {
		t.Errorf("go language should be untouched: %+v", cfg.Languages["go"])
	}
}

func TestLoadStripsExecutablesFromProjectConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	yaml := "" +
		"languages:\n" +
		"  typescript:\n" +
		"    enable: false\n" +
		"    extensions: [\".vue\"]\n" +
		"    lsp:\n" +
		"      command: \"./evil\"\n" +
		"      args: [\"--pwn\"]\n" +
		"      init: {tsserver: {path: \"/tmp/evil.js\"}}\n" +
		"    install:\n" +
		"      - { kind: npm, packages: [\"evil\"] }\n" +
		"  python:\n" +
		"    extensions: [\".py\"]\n" +
		"    lsp:\n" +
		"      command: \"evil-langserver\"\n"
	if err := os.WriteFile(filepath.Join(root, ".ntee-editor.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := Load(root)
	ts := cfg.Languages["typescript"]
	// Behavior keys survive: enable and the extensions union.
	if ts.IsEnabled() {
		t.Error("project config should still be able to disable a language")
	}
	if !contains(ts.Extensions, ".vue") || !contains(ts.Extensions, ".ts") {
		t.Errorf("extensions union lost: %v", ts.Extensions)
	}
	// Execution vectors do not: the default lsp block is untouched, install dropped.
	if ts.LSP.Command != "typescript-language-server" || len(ts.LSP.Init) != 0 {
		t.Errorf("project config overrode the lsp block: %+v", ts.LSP)
	}
	if len(ts.Install) != 0 {
		t.Errorf("project config injected install strategies: %+v", ts.Install)
	}
	// A project-added language exists for routing but gets no server.
	if py := cfg.Languages["python"]; py.LSP != nil || py.Install != nil {
		t.Errorf("project-added language must not carry lsp/install: %+v", py)
	}
}

func TestEnableToggle(t *testing.T) {
	if !(LanguageConfig{}).IsEnabled() {
		t.Fatal("omitted enable should default to enabled")
	}
	off := false
	if (LanguageConfig{Enabled: &off}).IsEnabled() {
		t.Fatal("enable:false should disable")
	}
	on := true
	if !(LanguageConfig{Enabled: &on}).IsEnabled() {
		t.Fatal("enable:true should enable")
	}
}

func TestLoadMergesEnableAndInstall(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	// install comes from the USER config (project files may not name executables);
	// enable comes from the project file (behavior, still allowed).
	writeUserConfigFile(t, xdg, ""+
		"languages:\n"+
		"  go:\n"+
		"    install:\n"+
		"      - { kind: brew, formula: gopls }\n")
	root := t.TempDir()
	yaml := "" +
		"languages:\n" +
		"  typescript:\n" +
		"    enable: false\n"
	if err := os.WriteFile(filepath.Join(root, ".ntee-editor.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Load(root)
	if cfg.Languages["typescript"].IsEnabled() {
		t.Fatal("config should be able to disable a default language")
	}
	if len(cfg.Languages["go"].Install) != 1 || cfg.Languages["go"].Install[0].Formula != "gopls" {
		t.Fatalf("install strategy not merged: %+v", cfg.Languages["go"].Install)
	}
	// The go LSP command from the default survives (only install was overlaid).
	if cfg.Languages["go"].LSP.Command != "gopls" {
		t.Fatalf("go lsp clobbered: %+v", cfg.Languages["go"].LSP)
	}
}

func TestMergeUserLanguages(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	cfgDir := filepath.Join(dir, "ntee-editor")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"),
		[]byte("languages:\n  typescript:\n    lsp:\n      command: \"/custom/tsls\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	added, err := MergeUserLanguages(map[string]LanguageConfig{
		"typescript": {LSP: &LSPServerConfig{Command: "should-be-ignored"}},
		"java":       {Extensions: []string{".java"}, LSP: &LSPServerConfig{Command: "jdtls"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 1 || added[0] != "java" {
		t.Fatalf("added = %v, want [java]", added)
	}

	out := Load(t.TempDir()) // re-load reads the merged file via XDG_CONFIG_HOME
	if out.Languages["typescript"].LSP.Command != "/custom/tsls" {
		t.Fatalf("existing typescript clobbered: %+v", out.Languages["typescript"].LSP)
	}
	if out.Languages["java"].LSP.Command != "jdtls" {
		t.Fatalf("java not added: %+v", out.Languages["java"])
	}
	if _, err := os.Stat(filepath.Join(cfgDir, "config.yaml.bak")); err != nil {
		t.Fatal("expected a .bak backup")
	}
}

func TestLoadAddsNewLanguage(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	writeUserConfigFile(t, xdg, ""+
		"languages:\n"+
		"  python:\n"+
		"    extensions: [\".py\"]\n"+
		"    lsp:\n"+
		"      command: \"pyright-langserver\"\n")
	cfg := Load(t.TempDir())
	if py, ok := cfg.Languages["python"]; !ok || !contains(py.Extensions, ".py") {
		t.Errorf("python language should be added: %+v", cfg.Languages["python"])
	}
	if cfg.Languages["python"].LSP.Command != "pyright-langserver" {
		t.Errorf("user config should be able to name a new language's server: %+v", cfg.Languages["python"].LSP)
	}
	if _, ok := cfg.Languages["typescript"]; !ok {
		t.Error("default languages should remain when a new one is added")
	}
}

func TestSetLanguagesEnabledCreatesFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	path, err := SetLanguagesEnabled([]string{"typescript"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dir, "ntee-editor", "config.yaml") {
		t.Fatalf("path = %q", path)
	}

	out := Load(t.TempDir())
	if out.Languages["typescript"].IsEnabled() {
		t.Fatal("typescript should be disabled")
	}
	// The seed guard must keep global LSP on and default extensions intact.
	if !out.LSP.Enabled {
		t.Fatal("global lsp.enabled must survive the fresh-file seed")
	}
	if !contains(out.Languages["typescript"].Extensions, ".ts") {
		t.Fatalf("default extensions lost: %v", out.Languages["typescript"].Extensions)
	}
}

func TestSetLanguagesEnabledPreservesTuning(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	cfgDir := filepath.Join(dir, "ntee-editor")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"),
		[]byte("languages:\n  typescript:\n    lsp:\n      command: \"/custom/tsls\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := SetLanguagesEnabled([]string{"typescript"}, false); err != nil {
		t.Fatal(err)
	}
	out := Load(t.TempDir())
	if out.Languages["typescript"].IsEnabled() {
		t.Fatal("typescript should be disabled")
	}
	if out.Languages["typescript"].LSP.Command != "/custom/tsls" {
		t.Fatalf("custom command clobbered: %+v", out.Languages["typescript"].LSP)
	}
	if _, err := os.Stat(filepath.Join(cfgDir, "config.yaml.bak")); err != nil {
		t.Fatal("expected a .bak backup")
	}

	// Round-trip: re-enable flips it back.
	if _, err := SetLanguagesEnabled([]string{"typescript"}, true); err != nil {
		t.Fatal(err)
	}
	if out := Load(t.TempDir()); !out.Languages["typescript"].IsEnabled() {
		t.Fatal("typescript should be enabled again")
	}
}

func TestSetLanguagesEnabledAll(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	if _, err := SetLanguagesEnabled([]string{"all"}, false); err != nil {
		t.Fatal(err)
	}
	if out := Load(t.TempDir()); out.LSP.Enabled {
		t.Fatal("'all' should disable global lsp.enabled")
	}
	if _, err := SetLanguagesEnabled([]string{"all"}, true); err != nil {
		t.Fatal(err)
	}
	if out := Load(t.TempDir()); !out.LSP.Enabled {
		t.Fatal("'all' should re-enable global lsp.enabled")
	}
}

func TestSetThemeSyntax(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	// Fresh file: seeded from defaults so lsp.enabled survives.
	path, err := SetThemeSyntax("dracula")
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dir, "ntee-editor", "config.yaml") {
		t.Fatalf("path = %q", path)
	}
	out := Load(t.TempDir())
	if out.Theme.Syntax != "dracula" {
		t.Fatalf("theme.syntax = %q", out.Theme.Syntax)
	}
	if !out.LSP.Enabled {
		t.Fatal("global lsp.enabled must survive the fresh-file seed")
	}

	// Existing file: other fields kept, prior content backed up.
	if _, err := SetThemeSyntax("nord"); err != nil {
		t.Fatal(err)
	}
	if out := Load(t.TempDir()); out.Theme.Syntax != "nord" {
		t.Fatalf("theme.syntax after rewrite = %q", out.Theme.Syntax)
	}
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Fatal("expected a .bak backup")
	}
}

func TestLoadWithWarningsReportsMalformedFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".ntee-editor.yaml"), []byte("editor:\n  tab_width: [not-an-int\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, warnings := LoadWithWarnings(root)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "malformed") {
		t.Fatalf("warnings = %v", warnings)
	}
	// The editor still starts with sane values.
	if cfg.Editor.TabWidth < 1 {
		t.Fatalf("defaults not applied: %+v", cfg.Editor)
	}
	// A clean load has no warnings.
	if _, w := LoadWithWarnings(t.TempDir()); len(w) != 0 {
		t.Fatalf("clean load warned: %v", w)
	}
}

func TestConfigEditsRefuseMalformedUserConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	cfgDir := filepath.Join(dir, "ntee-editor")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	malformed := []byte("languages: [broken\n")
	path := filepath.Join(cfgDir, "config.yaml")
	if err := os.WriteFile(path, malformed, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := SetThemeSyntax("nord"); err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("SetThemeSyntax on a malformed config: err = %v", err)
	}
	if _, err := SetLanguagesEnabled([]string{"all"}, false); err == nil {
		t.Fatal("SetLanguagesEnabled must refuse a malformed config")
	}
	if _, err := MergeUserLanguages(map[string]LanguageConfig{"x": {}}); err == nil {
		t.Fatal("MergeUserLanguages must refuse a malformed config")
	}
	// The recoverable file was not overwritten.
	if data, _ := os.ReadFile(path); string(data) != string(malformed) {
		t.Fatal("malformed config was overwritten")
	}
}
