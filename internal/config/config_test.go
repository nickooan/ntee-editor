package config

import (
	"os"
	"path/filepath"
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

func TestLoadUnionsExtensionsAndOverlaysLSP(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // isolate from the real ~/.config
	root := t.TempDir()
	yaml := "" +
		"languages:\n" +
		"  typescript:\n" +
		"    extensions: [\".vue\"]\n" +
		"    lsp:\n" +
		"      command: \"/custom/tsls\"\n"
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
	// Command overridden by the file; args kept from the default (overlay).
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

func TestLoadAddsNewLanguage(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	yaml := "" +
		"languages:\n" +
		"  python:\n" +
		"    extensions: [\".py\"]\n" +
		"    lsp:\n" +
		"      command: \"pyright-langserver\"\n"
	if err := os.WriteFile(filepath.Join(root, ".ntee-editor.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Load(root)
	if py, ok := cfg.Languages["python"]; !ok || !contains(py.Extensions, ".py") {
		t.Errorf("python language should be added: %+v", cfg.Languages["python"])
	}
	if _, ok := cfg.Languages["typescript"]; !ok {
		t.Error("default languages should remain when a new one is added")
	}
}
