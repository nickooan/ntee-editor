package lspsetup

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/nickooan/ntee-editor/internal/config"
)

func TestRecipesComplete(t *testing.T) {
	want := []string{"go", "typescript", "java", "kotlin", "ruby", "python", "vue"}
	r := Recipes()
	if len(r) != len(want) {
		t.Fatalf("recipe count = %d, want %d", len(r), len(want))
	}
	for _, name := range want {
		lc, ok := r[name]
		if !ok {
			t.Fatalf("missing recipe: %s", name)
		}
		if len(lc.Extensions) == 0 || lc.LSP == nil || lc.LSP.Command == "" || len(lc.Install) == 0 {
			t.Fatalf("recipe %s incomplete: %+v", name, lc)
		}
	}
}

// stubPreparer models the platform: `present` = tools/runtimes on PATH,
// `preinstalled` = servers that already resolve, `afterInstall` = servers that
// resolve once an install has run (Run flips the switch).
func stubPreparer(goos string, present, preinstalled, afterInstall map[string]bool, out *bytes.Buffer, ran *[]string) *Preparer {
	installed := false
	p := NewPreparer(out)
	p.GOOS = goos
	p.LookPath = func(name string) (string, error) {
		if present[name] {
			return "/usr/bin/" + name, nil
		}
		return "", errors.New("not found")
	}
	p.Verify = func(command string) (string, error) {
		if preinstalled[command] || (installed && afterInstall[command]) {
			return "/resolved/" + command, nil
		}
		return "", errors.New("not found")
	}
	p.Run = func(name string, args ...string) error {
		*ran = append(*ran, name)
		installed = true
		return nil
	}
	return p
}

func TestChooseStrategySelection(t *testing.T) {
	var out bytes.Buffer
	var ran []string
	// macOS with brew + java present → java picks the brew strategy.
	p := stubPreparer("darwin", map[string]bool{"brew": true, "java": true}, nil, nil, &out, &ran)
	if s, _ := p.choose(p.Recipes["java"]); s == nil || s.Kind != "brew" {
		t.Fatalf("java should choose brew: %+v", s)
	}
	// brew present but no JDK → skipped, reason names the missing runtime.
	p2 := stubPreparer("darwin", map[string]bool{"brew": true}, nil, nil, &out, &ran)
	if s, reason := p2.choose(p2.Recipes["kotlin"]); s != nil || reason == "" {
		t.Fatalf("kotlin without JDK should skip: %+v %q", s, reason)
	}
}

func TestPlanClassification(t *testing.T) {
	var out bytes.Buffer
	var ran []string
	// gopls already installed; node+npm present so ts is ready; java skipped.
	p := stubPreparer("darwin",
		map[string]bool{"npm": true, "node": true},
		map[string]bool{"gopls": true}, nil,
		&out, &ran)
	byLang := map[string]entry{}
	for _, e := range p.Plan() {
		byLang[e.lang] = e
	}
	if byLang["go"].status != statusInstalled {
		t.Fatalf("go should be installed, got %v", byLang["go"].status)
	}
	if byLang["typescript"].status != statusReady {
		t.Fatalf("typescript should be ready, got %v", byLang["typescript"].status)
	}
	if byLang["java"].status != statusSkipped {
		t.Fatalf("java should be skipped (no brew/jdk), got %v", byLang["java"].status)
	}
}

func TestExecuteWritesConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out bytes.Buffer
	var ran []string
	// npm+node present → ts/python/vue install and then resolve.
	p := stubPreparer("darwin",
		map[string]bool{"npm": true, "node": true},
		nil,
		map[string]bool{
			"typescript-language-server": true,
			"pyright-langserver":         true,
			"vue-language-server":        true,
		},
		&out, &ran)

	if err := p.Execute(func() bool { return true }); err != nil {
		t.Fatal(err)
	}
	if len(ran) != 3 { // ts, python, vue
		t.Fatalf("expected 3 installs, ran %v", ran)
	}

	path, _ := config.ConfigPath()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	var written config.Config
	if err := yaml.Unmarshal(data, &written); err != nil {
		t.Fatal(err)
	}
	ts, ok := written.Languages["typescript"]
	if !ok || ts.LSP == nil || ts.LSP.Command != "/resolved/typescript-language-server" {
		t.Fatalf("typescript config: %+v", ts)
	}
	if ts.Enabled == nil || !*ts.Enabled {
		t.Fatal("generated language should carry enable:true")
	}
}

func TestExecuteSkipsExistingConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	cfgDir := filepath.Join(dir, "ntee-editor")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Pre-write a user config with a custom typescript entry.
	custom := "languages:\n  typescript:\n    lsp:\n      command: \"/custom/tsls\"\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	var ran []string
	// ts + python resolve after install; python is new, ts is already configured.
	p := stubPreparer("darwin",
		map[string]bool{"npm": true, "node": true},
		nil,
		map[string]bool{"typescript-language-server": true, "pyright-langserver": true},
		&out, &ran)
	if err := p.Execute(func() bool { return true }); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(filepath.Join(cfgDir, "config.yaml"))
	var written config.Config
	_ = yaml.Unmarshal(data, &written)
	if written.Languages["typescript"].LSP.Command != "/custom/tsls" {
		t.Fatalf("existing typescript entry must be preserved: %+v", written.Languages["typescript"].LSP)
	}
	if _, ok := written.Languages["python"]; !ok {
		t.Fatal("newly-prepared python should be added")
	}
	if _, err := os.Stat(filepath.Join(cfgDir, "config.yaml.bak")); err != nil {
		t.Fatal("expected a .bak backup")
	}
}
