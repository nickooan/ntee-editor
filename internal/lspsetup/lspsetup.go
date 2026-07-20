// Package lspsetup drives `ntee-editor --prepare-lsp`: a small, curated
// package-manager-of-recipes that installs language servers via the platform's
// native tool (brew/go/npm/gem) and writes a platform-correct user config,
// preserving entries the user already tuned.
package lspsetup

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/nickooan/ntee-editor/internal/config"
	"github.com/nickooan/ntee-editor/internal/lsp"
)

// Recipes returns the built-in per-language install recipes. Each value's LSP
// block is the resulting server invocation; Install lists how to obtain it.
func Recipes() map[string]config.LanguageConfig {
	brew := func(formula, requires, command string) []config.InstallStrategy {
		return []config.InstallStrategy{{
			Kind: "brew", Formula: formula, Requires: requires,
			Platforms: []string{"darwin", "linux"}, Command: command,
		}}
	}
	npm := func(pkgs []string, command string, args ...string) []config.InstallStrategy {
		return []config.InstallStrategy{{
			Kind: "npm", Packages: pkgs, Requires: "node", Command: command, Args: args,
		}}
	}
	return map[string]config.LanguageConfig{
		"go": {
			Extensions: []string{".go"},
			LSP:        &config.LSPServerConfig{Command: "gopls"},
			Install: []config.InstallStrategy{{
				Kind: "go", Requires: "go",
				Package: "golang.org/x/tools/gopls@latest", Command: "gopls",
			}},
		},
		"typescript": {
			Extensions: []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs"},
			LSP:        &config.LSPServerConfig{Command: "typescript-language-server", Args: []string{"--stdio"}},
			// Only the server is installed globally. Classic TypeScript is NOT
			// installed globally — that would clobber a user's `typescript`
			// (e.g. the 7.x native preview). Instead a compatible classic TS is
			// installed into a private editor toolchain (ensureClassicTS) and the
			// server is pointed at it via tsserver.path.
			Install: npm([]string{"typescript-language-server"}, "typescript-language-server", "--stdio"),
		},
		"java": {
			Extensions: []string{".java"},
			LSP:        &config.LSPServerConfig{Command: "jdtls"},
			Install:    brew("jdtls", "java", "jdtls"),
		},
		"kotlin": {
			Extensions: []string{".kt", ".kts"},
			LSP:        &config.LSPServerConfig{Command: "kotlin-language-server"},
			Install:    brew("kotlin-language-server", "java", "kotlin-language-server"),
		},
		"ruby": {
			Extensions: []string{".rb"},
			LSP:        &config.LSPServerConfig{Command: "ruby-lsp"},
			Install: []config.InstallStrategy{{
				Kind: "gem", Requires: "ruby", Package: "ruby-lsp", Command: "ruby-lsp",
			}},
		},
		"python": {
			Extensions: []string{".py"},
			LSP:        &config.LSPServerConfig{Command: "pyright-langserver", Args: []string{"--stdio"}},
			Install:    npm([]string{"pyright"}, "pyright-langserver", "--stdio"),
		},
		"vue": {
			Extensions: []string{".vue"},
			// Volar hybrid mode: relay tsserver/request to the typescript server.
			// The --tsdk arg + typescript's @vue/typescript-plugin are injected at
			// prepare time (wireHybrid) from resolved paths.
			LSP: &config.LSPServerConfig{
				Command: "vue-language-server",
				Args:    []string{"--stdio"},
				Bridge:  &config.BridgeConfig{To: "typescript", Command: "typescript.tsserverRequest"},
			},
			Install: npm([]string{"@vue/language-server"}, "vue-language-server", "--stdio"),
		},
	}
}

// Preparer runs the install flow. Its function fields are injected so strategy
// selection and command construction are testable without shelling out.
type Preparer struct {
	GOOS     string
	Recipes  map[string]config.LanguageConfig
	LookPath func(string) (string, error)         // tool/runtime presence (default exec.LookPath)
	Verify   func(command string) (string, error) // resolved server path (default lsp.ResolveBinary)
	Run      func(name string, args ...string) error
	// TSDK resolves the classic TypeScript lib dir (for Vue's --tsdk); Plugin
	// resolves @vue/typescript-plugin given the resolved vue server path. Both
	// injected so the hybrid wiring is testable without a real filesystem.
	TSDK   func() (string, error)
	Plugin func(vueServer string) (string, error)
	out    io.Writer
}

// NewPreparer builds a Preparer wired to the real platform, streaming installer
// output to out.
func NewPreparer(out io.Writer) *Preparer {
	return &Preparer{
		GOOS:     runtime.GOOS,
		Recipes:  Recipes(),
		LookPath: exec.LookPath,
		Verify:   lsp.ResolveBinary,
		Run: func(name string, args ...string) error {
			cmd := exec.Command(name, args...)
			cmd.Stdout, cmd.Stderr = out, out
			return cmd.Run()
		},
		TSDK:   defaultResolveTSDK,
		Plugin: defaultResolvePlugin,
		out:    out,
	}
}

// toolchainDir is where the editor keeps a private classic TypeScript, so it
// never has to touch the user's global `typescript` (which may be the 7.x
// native preview they want to keep).
func toolchainDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".ntee-editor", "toolchain"), nil
}

// hasClassicTS reports whether dir/typescript.js exists (the classic JS API the
// 7.x native preview lacks).
func hasClassicTS(libDir string) bool {
	_, err := os.Stat(filepath.Join(libDir, "typescript.js"))
	return err == nil
}

// defaultResolveTSDK finds a classic-line TypeScript lib dir: the private editor
// toolchain first, then the npm global root (in case a classic TS is already
// installed globally). The 7.0.2 native preview has no typescript.js, so it is
// naturally rejected.
func defaultResolveTSDK() (string, error) {
	if tc, err := toolchainDir(); err == nil {
		if lib := filepath.Join(tc, "node_modules", "typescript", "lib"); hasClassicTS(lib) {
			return lib, nil
		}
	}
	if out, err := exec.Command("npm", "root", "-g").Output(); err == nil {
		if lib := filepath.Join(strings.TrimSpace(string(out)), "typescript", "lib"); hasClassicTS(lib) {
			return lib, nil
		}
	}
	return "", errors.New("classic typescript (typescript.js) not found")
}

// ensureClassicTS returns a resolvable classic-TS lib dir, installing
// `typescript@5` into the private toolchain if none is found. It never installs
// globally, so the user's global `typescript` (e.g. TS7 native) is untouched.
func (p *Preparer) ensureClassicTS() (string, error) {
	if dir, err := p.TSDK(); err == nil {
		return dir, nil // already have a compatible classic TS (private or global)
	}
	tc, err := toolchainDir()
	if err != nil {
		return "", err
	}
	fmt.Fprintf(p.out, "\n=== typescript: installing private classic TS (npm install --prefix %s typescript@5) ===\n", tc)
	if err := p.Run("npm", "install", "--prefix", tc, "typescript@5"); err != nil {
		return "", fmt.Errorf("private typescript install failed: %w", err)
	}
	return p.TSDK()
}

// defaultResolvePlugin locates @vue/typescript-plugin bundled next to the vue
// language server (…/@vue/language-server/node_modules/@vue/typescript-plugin).
func defaultResolvePlugin(vueServer string) (string, error) {
	real, err := filepath.EvalSymlinks(vueServer)
	if err != nil {
		real = vueServer
	}
	// …/@vue/language-server/bin/vue-language-server.js → …/@vue/language-server
	pkg := filepath.Dir(filepath.Dir(real))
	plugin := filepath.Join(pkg, "node_modules", "@vue", "typescript-plugin")
	if _, err := os.Stat(plugin); err != nil {
		return "", fmt.Errorf("@vue/typescript-plugin not found at %s", plugin)
	}
	return plugin, nil
}

// Prepare runs the full flow: plan → confirm → install → write config.
func Prepare(out io.Writer, confirm func() bool) error {
	return NewPreparer(out).Execute(confirm)
}

type status int

const (
	statusInstalled status = iota // already resolvable
	statusReady                   // a viable install strategy exists
	statusSkipped                 // disabled, or no viable strategy
)

type entry struct {
	lang    string
	lc      config.LanguageConfig
	status  status
	strat   *config.InstallStrategy
	command string // resolved path (installed) or recipe command (ready)
	reason  string
}

// Plan classifies each recipe language without making changes.
func (p *Preparer) Plan() []entry {
	names := make([]string, 0, len(p.Recipes))
	for n := range p.Recipes {
		names = append(names, n)
	}
	sort.Strings(names)

	out := make([]entry, 0, len(names))
	for _, name := range names {
		lc := p.Recipes[name]
		e := entry{lang: name, lc: lc}
		switch {
		case !lc.IsEnabled():
			e.status, e.reason = statusSkipped, "disabled"
		case p.isInstalled(name, lc):
			resolved, _ := p.Verify(lc.LSP.Command)
			e.status, e.command = statusInstalled, resolved
		default:
			if strat, reason := p.choose(lc); strat != nil {
				e.status, e.strat, e.command = statusReady, strat, strat.Command
			} else {
				e.status, e.reason = statusSkipped, reason
			}
		}
		out = append(out, e)
	}
	return out
}

// isInstalled reports whether a language needs no install. Normally that is
// just "the server binary resolves", but a hybrid companion (e.g. typescript,
// which Vue bridges to) ALSO needs a compatible classic TypeScript — the server
// alone isn't enough. If the tsdk is missing (e.g. only the 7.0.2 native
// preview is present), the language is treated as needing an install so the
// pinned `typescript@5` gets pulled in.
func (p *Preparer) isInstalled(name string, lc config.LanguageConfig) bool {
	if lc.LSP == nil || !p.resolves(lc.LSP.Command) {
		return false
	}
	if p.isBridgeTarget(name) {
		if _, err := p.TSDK(); err != nil {
			return false
		}
	}
	return true
}

// isBridgeTarget reports whether some enabled recipe declares a hybrid bridge to
// this language (so it must supply a classic TypeScript, not just its server).
func (p *Preparer) isBridgeTarget(lang string) bool {
	for _, lc := range p.Recipes {
		if lc.IsEnabled() && lc.LSP != nil && lc.LSP.Bridge != nil && lc.LSP.Bridge.To == lang {
			return true
		}
	}
	return false
}

func (p *Preparer) resolves(command string) bool {
	_, err := p.Verify(command)
	return err == nil
}

// choose picks the first install strategy whose platform, tool, and required
// runtime are all available. reason names what's missing otherwise.
func (p *Preparer) choose(lc config.LanguageConfig) (*config.InstallStrategy, string) {
	var missing []string
	for i := range lc.Install {
		s := lc.Install[i]
		if len(s.Platforms) > 0 && !contains(s.Platforms, p.GOOS) {
			continue
		}
		tool := toolFor(s.Kind)
		if tool == "" {
			continue
		}
		if _, err := p.LookPath(tool); err != nil {
			missing = appendUniq(missing, tool)
			continue
		}
		if s.Requires != "" {
			if _, err := p.LookPath(s.Requires); err != nil {
				missing = appendUniq(missing, s.Requires)
				continue
			}
		}
		return &lc.Install[i], ""
	}
	if len(missing) > 0 {
		return nil, "needs " + strings.Join(missing, " or ")
	}
	return nil, "no strategy for " + p.GOOS
}

// Execute prints the plan, confirms, runs installs, and merges the resulting
// server configs into the user config file.
func (p *Preparer) Execute(confirm func() bool) error {
	plan := p.Plan()
	fmt.Fprintln(p.out, "Language server setup:")
	ready := 0
	for _, e := range plan {
		switch e.status {
		case statusInstalled:
			fmt.Fprintf(p.out, "  ✓ %-11s already installed (%s)\n", e.lang, e.command)
		case statusReady:
			name, args := installCmd(*e.strat)
			fmt.Fprintf(p.out, "  + %-11s %s %s\n", e.lang, name, strings.Join(args, " "))
			ready++
		case statusSkipped:
			fmt.Fprintf(p.out, "  · %-11s skipped (%s)\n", e.lang, e.reason)
		}
	}

	if ready > 0 {
		fmt.Fprintf(p.out, "\nInstall %d server(s)? [y/N] ", ready)
		if !confirm() {
			fmt.Fprintln(p.out, "aborted; no changes made.")
			return nil
		}
	}

	prepared := map[string]config.LanguageConfig{}
	for _, e := range plan {
		switch e.status {
		case statusInstalled:
			prepared[e.lang] = genConfig(e.lc, e.command)
		case statusReady:
			name, args := installCmd(*e.strat)
			fmt.Fprintf(p.out, "\n=== %s: %s %s ===\n", e.lang, name, strings.Join(args, " "))
			if err := p.Run(name, args...); err != nil {
				fmt.Fprintf(p.out, "  ✗ %s install failed: %v\n", e.lang, err)
				continue
			}
			resolved, err := p.Verify(e.strat.Command)
			if err != nil {
				fmt.Fprintf(p.out, "  ✗ %s installed but not found — check your PATH\n", e.lang)
				continue
			}
			fmt.Fprintf(p.out, "  ✓ %s ready (%s)\n", e.lang, resolved)
			prepared[e.lang] = genConfig(e.lc, resolved)
		}
	}

	p.wireHybrid(prepared)

	if len(prepared) == 0 {
		fmt.Fprintln(p.out, "\nNo servers to configure.")
		return nil
	}
	added, err := config.MergeUserLanguages(prepared)
	if err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	path, _ := config.ConfigPath()
	if len(added) > 0 {
		fmt.Fprintf(p.out, "\nAdded to %s: %s\n", path, strings.Join(added, ", "))
	} else {
		fmt.Fprintf(p.out, "\n%s already covers these languages; left unchanged.\n", path)
	}
	return nil
}

// genConfig builds the LanguageConfig written to disk: the resolved (usually
// absolute) command path, so the editor finds it regardless of the launching
// shell's PATH, with `enable: true` so the user can toggle it later.
func genConfig(lc config.LanguageConfig, resolvedCommand string) config.LanguageConfig {
	on := true
	var args []string
	var init map[string]any
	var bridge *config.BridgeConfig
	if lc.LSP != nil {
		args = lc.LSP.Args
		init = lc.LSP.Init     // carry plugins/tsserver options through
		bridge = lc.LSP.Bridge // carry the hybrid companion declaration through
	}
	return config.LanguageConfig{
		Enabled:    &on,
		Extensions: lc.Extensions,
		LSP:        &config.LSPServerConfig{Command: resolvedCommand, Args: args, Init: init, Bridge: bridge},
	}
}

// wireHybrid completes Vue hybrid mode on the generated config: it injects the
// resolved --tsdk into the vue server args and the @vue/typescript-plugin into
// the typescript server's init. If the toolchain can't be resolved (no classic
// TS, plugin missing, or typescript not being configured), it drops the bridge
// so Vue degrades to template-only rather than crashing, and says so.
func (p *Preparer) wireHybrid(prepared map[string]config.LanguageConfig) {
	ts, hasTS := prepared["typescript"]
	vue, hasVue := prepared["vue"]
	vueHybrid := hasVue && vue.LSP != nil && vue.LSP.Bridge != nil

	dropVueBridge := func(reason string) {
		if vueHybrid {
			vue.LSP.Bridge = nil // template-only
			prepared["vue"] = vue
			fmt.Fprintf(p.out, "  · vue: hybrid TypeScript disabled (%s); template features only\n", reason)
		}
	}

	if !hasTS || ts.LSP == nil {
		dropVueBridge("no typescript server configured")
		return
	}

	tsdk, err := p.ensureClassicTS()
	if err != nil {
		fmt.Fprintf(p.out, "  · typescript: no classic TS (%v); tsserver may fail if the global typescript is the 7.x native preview\n", err)
		dropVueBridge(err.Error())
		return
	}

	// Point typescript-language-server at the private classic TS so it does not
	// resolve the (possibly TS7-native) global typescript. Global stays untouched.
	if ts.LSP.Init == nil {
		ts.LSP.Init = map[string]any{}
	}
	ts.LSP.Init["tsserver"] = map[string]any{"path": tsdk}
	fmt.Fprintf(p.out, "  ✓ typescript: using private classic TS at %s (global typescript untouched)\n", tsdk)

	if vueHybrid {
		plugin, perr := p.Plugin(vue.LSP.Command)
		if perr != nil {
			dropVueBridge(perr.Error())
		} else {
			vue.LSP.Args = append(append([]string{}, vue.LSP.Args...), "--tsdk="+tsdk)
			prepared["vue"] = vue
			ts.LSP.Init["plugins"] = []any{map[string]any{
				"name":      "@vue/typescript-plugin",
				"location":  plugin,
				"languages": []any{"vue"},
			}}
			fmt.Fprintf(p.out, "  ✓ vue: hybrid TypeScript wired (--tsdk %s)\n", tsdk)
		}
	}
	prepared["typescript"] = ts
}

func installCmd(s config.InstallStrategy) (name string, args []string) {
	switch s.Kind {
	case "brew":
		return "brew", []string{"install", s.Formula}
	case "go":
		return "go", []string{"install", s.Package}
	case "npm":
		return "npm", append([]string{"install", "-g"}, s.Packages...)
	case "gem":
		return "gem", []string{"install", s.Package}
	}
	return "", nil
}

func toolFor(kind string) string {
	switch kind {
	case "brew", "go", "npm", "gem":
		return kind
	}
	return ""
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func appendUniq(xs []string, v string) []string {
	if contains(xs, v) {
		return xs
	}
	return append(xs, v)
}
