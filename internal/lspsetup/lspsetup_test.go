package lspsetup

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
	// Deterministic hybrid resolvers so tests never touch npm/the real FS.
	// Individual tests override these to exercise the degraded path.
	p.TSDK = func() (string, error) { return "/ts5/lib", nil }
	p.Plugin = func(string) (string, error) { return "/vue/plugin", nil }
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

// Even when typescript-language-server already resolves, a missing classic
// TypeScript (only the 7.x native preview present) must mark typescript as
// READY-to-install so the pinned typescript@5 gets pulled in — otherwise Vue
// hybrid mode silently degrades.
func TestTypescriptReinstallsWhenClassicTSMissing(t *testing.T) {
	var out bytes.Buffer
	var ran []string
	p := stubPreparer("darwin",
		map[string]bool{"npm": true, "node": true},
		map[string]bool{"typescript-language-server": true, "vue-language-server": true},
		nil, &out, &ran)
	p.TSDK = func() (string, error) { return "", errors.New("only 7.x native present") }

	byLang := map[string]entry{}
	for _, e := range p.Plan() {
		byLang[e.lang] = e
	}
	if byLang["typescript"].status != statusReady {
		t.Fatalf("typescript should be READY to install when classic TS is missing, got %v", byLang["typescript"].status)
	}
	// vue's server resolves and nothing bridges *to* vue → it stays installed.
	if byLang["vue"].status != statusInstalled {
		t.Fatalf("vue should be installed, got %v", byLang["vue"].status)
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
	// A fresh config must not disable LSP: loading it back must keep LSP on and
	// carry sane editor defaults (regression — a zero-value Config wrote
	// lsp.enabled:false, turning all LSP off).
	loaded := config.Load(t.TempDir()) // no project file; user config is the one we wrote
	if !loaded.LSP.Enabled {
		t.Fatal("freshly generated config disabled LSP (lsp.enabled:false)")
	}
	if loaded.Editor.TabWidth == 0 || loaded.Editor.MaxSnapshots == 0 {
		t.Fatalf("editor defaults zeroed by generated config: %+v", loaded.Editor)
	}
}

func TestTypescriptRecipeDoesNotInstallGlobalTS(t *testing.T) {
	ts := Recipes()["typescript"]
	for _, s := range ts.Install {
		for _, pkg := range s.Packages {
			if pkg == "typescript" || strings.HasPrefix(pkg, "typescript@") {
				t.Fatalf("recipe must not install a global typescript (would clobber the user's TS7); "+
					"classic TS goes into the private toolchain. got %q", pkg)
			}
		}
	}
}

func TestGenConfigPreservesInitAndBridge(t *testing.T) {
	lc := config.LanguageConfig{
		Extensions: []string{".vue"},
		LSP: &config.LSPServerConfig{
			Command: "vue-language-server",
			Args:    []string{"--stdio"},
			Init:    map[string]any{"k": "v"},
			Bridge:  &config.BridgeConfig{To: "typescript", Command: "typescript.tsserverRequest"},
		},
	}
	got := genConfig(lc, "/resolved/vue-ls")
	if got.LSP.Command != "/resolved/vue-ls" {
		t.Fatalf("command: %q", got.LSP.Command)
	}
	if got.LSP.Init["k"] != "v" {
		t.Fatal("Init was dropped")
	}
	if got.LSP.Bridge == nil || got.LSP.Bridge.To != "typescript" {
		t.Fatalf("Bridge was dropped: %+v", got.LSP.Bridge)
	}
}

func TestWireHybridInjectsTsdkAndPlugin(t *testing.T) {
	var out bytes.Buffer
	p := NewPreparer(&out)
	p.TSDK = func() (string, error) { return "/ts5/lib", nil }
	p.Plugin = func(string) (string, error) { return "/vue/plugin", nil }

	prepared := map[string]config.LanguageConfig{
		"typescript": genConfig(Recipes()["typescript"], "/bin/tsls"),
		"vue":        genConfig(Recipes()["vue"], "/bin/vue-ls"),
	}
	p.wireHybrid(prepared)

	vue := prepared["vue"]
	if !contains(vue.LSP.Args, "--tsdk=/ts5/lib") {
		t.Fatalf("vue args missing tsdk: %v", vue.LSP.Args)
	}
	if vue.LSP.Bridge == nil || vue.LSP.Bridge.To != "typescript" {
		t.Fatalf("vue bridge should be kept: %+v", vue.LSP.Bridge)
	}
	tsInit := prepared["typescript"].LSP.Init
	if srv, _ := tsInit["tsserver"].(map[string]any); srv["path"] != "/ts5/lib" {
		t.Fatalf("typescript tsserver.path should point at the classic TS: %+v", tsInit)
	}
	plugins, _ := tsInit["plugins"].([]any)
	if len(plugins) != 1 {
		t.Fatalf("typescript should carry one plugin: %+v", tsInit)
	}
	p0 := plugins[0].(map[string]any)
	if p0["name"] != "@vue/typescript-plugin" || p0["location"] != "/vue/plugin" {
		t.Fatalf("plugin entry wrong: %+v", p0)
	}
}

// When no classic TS resolves, wireHybrid installs typescript@5 into the
// PRIVATE toolchain (never global) and points the servers at it.
func TestWireHybridInstallsPrivateClassicTS(t *testing.T) {
	var out bytes.Buffer
	var ran [][]string
	p := NewPreparer(&out)
	installed := false
	p.TSDK = func() (string, error) {
		if installed {
			return "/home/.ntee-editor/toolchain/node_modules/typescript/lib", nil
		}
		return "", errors.New("missing")
	}
	p.Plugin = func(string) (string, error) { return "/vue/plugin", nil }
	p.Run = func(name string, args ...string) error {
		ran = append(ran, append([]string{name}, args...))
		installed = true
		return nil
	}
	prepared := map[string]config.LanguageConfig{
		"typescript": genConfig(Recipes()["typescript"], "/bin/tsls"),
		"vue":        genConfig(Recipes()["vue"], "/bin/vue-ls"),
	}
	p.wireHybrid(prepared)

	privateInstall := false
	for _, cmd := range ran {
		joined := strings.Join(cmd, " ")
		if strings.Contains(joined, "npm install --prefix") && strings.Contains(joined, "typescript@5") {
			privateInstall = true
			if strings.Contains(joined, "-g") {
				t.Fatalf("classic TS must NOT be installed globally: %s", joined)
			}
		}
	}
	if !privateInstall {
		t.Fatalf("expected a private typescript@5 install, ran %v", ran)
	}
	if prepared["vue"].LSP.Bridge == nil {
		t.Fatal("bridge should be wired after the private install")
	}
}

func TestWireHybridDegradesWhenInstallFails(t *testing.T) {
	var out bytes.Buffer
	p := NewPreparer(&out)
	p.TSDK = func() (string, error) { return "", errors.New("no classic ts") }
	p.Plugin = func(string) (string, error) { return "/vue/plugin", nil }
	p.Run = func(string, ...string) error { return errors.New("npm unavailable") }

	prepared := map[string]config.LanguageConfig{
		"typescript": genConfig(Recipes()["typescript"], "/bin/tsls"),
		"vue":        genConfig(Recipes()["vue"], "/bin/vue-ls"),
	}
	p.wireHybrid(prepared)

	if prepared["vue"].LSP.Bridge != nil {
		t.Fatal("bridge must be dropped when classic TS can't be installed")
	}
	if !strings.Contains(out.String(), "template features only") {
		t.Fatalf("expected a degrade note, got: %q", out.String())
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
