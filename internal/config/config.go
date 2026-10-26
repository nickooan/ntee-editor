// Package config loads the editor's YAML configuration. Load order:
// built-in defaults ← ~/.config/ntee-editor/config.yaml ← <project>/.ntee-editor.yaml
// (later files override fields they set; most lists replace). Exception: a
// language's `extensions` are UNIONED with the built-in defaults, so a config
// extends (never shrinks) the set of file types routed to an LSP server; other
// language fields (command/args/init) overlay the default when set.
//
// Trust rule: only the USER config may name executables. The project-local
// .ntee-editor.yaml ships with whatever repo the editor is pointed at, so its
// per-language `lsp` blocks (command/args/init/bridge — all execution vectors)
// and `install` strategies are stripped before merging; it may still set
// behavior (enable, extensions, editor, tree, theme). Keybinds are also
// user-only, so a repo can never remap the user's keys.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Version   int                       `yaml:"version"`
	Editor    EditorConfig              `yaml:"editor"`
	Tree      TreeConfig                `yaml:"tree"`
	Theme     ThemeConfig               `yaml:"theme"`
	Languages map[string]LanguageConfig `yaml:"languages"`
	LSP       LSPConfig                 `yaml:"lsp"`
	// Keybinds maps action names to key chords (resolved by internal/keymap).
	// User config only: a project file's keybinds are ignored.
	Keybinds map[string]string `yaml:"keybinds"`
	// ProjectCodeStyle notes the tab sizes the project file set (not read
	// from YAML; filled by LoadWithWarnings).
	ProjectCodeStyle ProjectCodeStyle `yaml:"-"`
}

type EditorConfig struct {
	TabWidth int `yaml:"tab_width"`
	// LanguageTabWidths overrides TabWidth per CodeStyleLanguages key; a
	// language without an entry inherits TabWidth.
	LanguageTabWidths map[string]int `yaml:"language_tab_widths"`
	MaxSnapshots      int            `yaml:"max_snapshots"`
	MaxHighlightKB    int            `yaml:"max_highlight_kb"`
}

type TreeConfig struct {
	Ignore []string `yaml:"ignore"`
	// MaxIndexFiles caps the search corpus (BuildAllEntries). When the walk hits
	// it, indexing stops and the UI flags a truncated index. Bounds memory/CPU on
	// huge roots. <1 is clamped to the default in Load.
	MaxIndexFiles int `yaml:"max_index_files"`
}

type ThemeConfig struct {
	Syntax string `yaml:"syntax"`
}

type LanguageConfig struct {
	// Enabled toggles the language: nil (omitted) means enabled; false turns it
	// off (its extensions route to no server, and --prepare-lsp skips it).
	Enabled    *bool             `yaml:"enable,omitempty"`
	Extensions []string          `yaml:"extensions,omitempty"`
	LSP        *LSPServerConfig  `yaml:"lsp,omitempty"`
	Install    []InstallStrategy `yaml:"install,omitempty"` // consumed only by --prepare-lsp
}

// IsEnabled reports whether the language is active (default when unset).
func (l LanguageConfig) IsEnabled() bool { return l.Enabled == nil || *l.Enabled }

// InstallStrategy is one way --prepare-lsp can obtain a language's server. The
// first strategy whose platform + required tools are available is used.
type InstallStrategy struct {
	Kind      string   `yaml:"kind"`                // "brew" | "go" | "npm" | "gem"
	Platforms []string `yaml:"platforms,omitempty"` // "darwin","linux"; empty = all
	Requires  string   `yaml:"requires,omitempty"`  // runtime that must be present: go|node|java|ruby
	Formula   string   `yaml:"formula,omitempty"`   // brew formula
	Package   string   `yaml:"package,omitempty"`   // go install / gem install target
	Packages  []string `yaml:"packages,omitempty"`  // npm i -g targets
	Command   string   `yaml:"command,omitempty"`   // resulting server invocation
	Args      []string `yaml:"args,omitempty"`
}

type LSPServerConfig struct {
	Command string   `yaml:"command"`
	Args    []string `yaml:"args"`
	// Init is passed through as LSP initializationOptions (e.g.
	// typescript-language-server's tsserver.path).
	Init map[string]any `yaml:"init"`
	// Bridge declares a hybrid-mode companion: this server's tsserver/request
	// notifications are relayed to the To language's server via the Command
	// executeCommand. Used by Volar/Vue (To: typescript, Command:
	// typescript.tsserverRequest). nil = standalone server.
	Bridge *BridgeConfig `yaml:"bridge,omitempty"`
}

// BridgeConfig wires a hybrid language server to a companion server.
type BridgeConfig struct {
	To      string `yaml:"to"`      // companion language whose server answers
	Command string `yaml:"command"` // executeCommand used to relay (tsserver passthrough)
}

type LSPConfig struct {
	Enabled bool `yaml:"enabled"` // hard-off in v1
}

// Default returns the built-in configuration.
func Default() Config {
	return Config{
		Version: 1,
		Editor: EditorConfig{
			TabWidth:          DefaultTabWidth,
			LanguageTabWidths: map[string]int{},
			MaxSnapshots:      50,
			MaxHighlightKB:    512,
		},
		Tree: TreeConfig{
			// .git is always fully hidden and node_modules is always shown-but-
			// dimmed and kept out of search (filetree.hardIgnore/softIgnore),
			// regardless of this list. These are additional, user-overridable
			// build/dependency dirs hidden from the tree and search corpus so a
			// repo without a covering .gitignore (or a multi-repo root) does not
			// index vendored/output trees. Overriding tree.ignore in config
			// replaces this list.
			Ignore: []string{
				"dist", "build", "target", "vendor",
				".next", ".nuxt", ".svelte-kit",
				".venv", "venv", "__pycache__",
				".gradle", "coverage", ".turbo", ".cache",
			},
			MaxIndexFiles: 50000,
		},
		Theme: ThemeConfig{Syntax: "gruvbox"},
		Languages: map[string]LanguageConfig{
			"go": {
				Extensions: []string{".go"},
				LSP:        &LSPServerConfig{Command: "gopls"},
			},
			"typescript": {
				// typescript-language-server (tsserver) also handles JavaScript.
				Extensions: []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs"},
				LSP:        &LSPServerConfig{Command: "typescript-language-server", Args: []string{"--stdio"}},
			},
		},
		// Safe default: a missing server binary degrades to a one-time notice
		// and the heuristic jump.
		LSP: LSPConfig{Enabled: true},
	}
}

// Load builds the effective config for a project root, discarding warnings.
func Load(projectRoot string) Config {
	cfg, _ := LoadWithWarnings(projectRoot)
	return cfg
}

// LoadWithWarnings builds the effective config for a project root. Missing
// files are fine; a malformed file is skipped so the editor still starts, but
// the skip is reported — a typo'd config silently behaving like defaults is
// worse than a notice.
func LoadWithWarnings(projectRoot string) (Config, []string) {
	cfg := Default()
	var warnings []string
	if path, err := ConfigPath(); err == nil {
		warnings = append(warnings, merge(&cfg, path, true)...) // user config: trusted, may name executables
	}
	projectPath := filepath.Join(projectRoot, ".ntee-editor.yaml")
	warnings = append(warnings, merge(&cfg, projectPath, false)...)
	cfg.ProjectCodeStyle = readProjectCodeStyle(projectPath)
	warnings = append(warnings, validateTabWidths(&cfg)...)
	if cfg.Editor.MaxSnapshots < 1 {
		cfg.Editor.MaxSnapshots = 50
	}
	if cfg.Tree.MaxIndexFiles < 1 {
		cfg.Tree.MaxIndexFiles = 50000
	}
	return cfg, warnings
}

// ConfigPath returns the user config file path: $XDG_CONFIG_HOME (else
// ~/.config) + ntee-editor/config.yaml.
func ConfigPath() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "ntee-editor", "config.yaml"), nil
}

// readUserConfig loads the user config file for editing. When the file is
// absent, the non-language sections are seeded from defaults — otherwise we'd
// marshal bool/int zero values (notably lsp.enabled:false, which DISABLES all
// LSP on load) into a fresh file. Languages stay empty so only explicit
// entries are written (not the default recipes). existing is the raw prior
// content (nil when absent), for the pre-write backup.
func readUserConfig() (file Config, existing []byte, path string, err error) {
	path, err = ConfigPath()
	if err != nil {
		return Config{}, nil, "", err
	}
	existing, readErr := os.ReadFile(path)
	if readErr == nil {
		if err := yaml.Unmarshal(existing, &file); err != nil {
			// Refuse to edit on top of a malformed config: proceeding would
			// overwrite the user's recoverable file with a defaults-seeded one.
			return Config{}, nil, "", fmt.Errorf(
				"user config %s is malformed (%s) — fix it or move it aside first; a prior backup may exist at %s.bak",
				path, firstLine(err.Error()), path)
		}
	} else {
		existing = nil
		d := Default()
		file.Version = d.Version
		file.Editor = d.Editor
		file.Tree = d.Tree
		file.Theme = d.Theme
		file.LSP = d.LSP
	}
	if file.Languages == nil {
		file.Languages = map[string]LanguageConfig{}
	}
	if readErr == nil {
		seedMissingEditorFields(&file, existing)
	}
	return file, existing, path, nil
}

// seedMissingEditorFields gives editor fields the file leaves out their
// defaults, so a rewrite never turns an absent field into a zero —
// max_highlight_kb: 0 means unlimited, not the default cap. A zero the user
// wrote stays.
func seedMissingEditorFields(file *Config, existing []byte) {
	var present struct {
		Editor struct {
			TabWidth       *int `yaml:"tab_width"`
			MaxSnapshots   *int `yaml:"max_snapshots"`
			MaxHighlightKB *int `yaml:"max_highlight_kb"`
		} `yaml:"editor"`
	}
	_ = yaml.Unmarshal(existing, &present) // the same bytes just decoded
	defaults := Default().Editor
	if present.Editor.TabWidth == nil {
		file.Editor.TabWidth = defaults.TabWidth
	}
	if present.Editor.MaxSnapshots == nil {
		file.Editor.MaxSnapshots = defaults.MaxSnapshots
	}
	if present.Editor.MaxHighlightKB == nil {
		file.Editor.MaxHighlightKB = defaults.MaxHighlightKB
	}
	if file.Editor.LanguageTabWidths == nil {
		file.Editor.LanguageTabWidths = map[string]int{}
	}
}

// writeUserConfig marshals file to path, backing the prior content up to
// path+".bak" first. Comments in the existing file are lost on rewrite (hence
// the backup).
func writeUserConfig(path string, file *Config, existing []byte) error {
	out, err := yaml.Marshal(file)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if existing != nil {
		if err := os.WriteFile(path+".bak", existing, 0o644); err != nil {
			// No backup → no rewrite: the rewrite loses the file's comments and
			// the backup is the only way back.
			return fmt.Errorf("config backup failed, not overwriting: %w", err)
		}
	}
	return os.WriteFile(path, out, 0o644)
}

// MergeUserLanguages adds languages to the user config file, keeping existing
// entries (and their tuning) untouched — a language already present is skipped.
// The prior file is backed up to config.yaml.bak. Returns the languages added.
func MergeUserLanguages(langs map[string]LanguageConfig) (added []string, err error) {
	file, existing, path, err := readUserConfig()
	if err != nil {
		return nil, err
	}

	for name, lang := range langs {
		if _, present := file.Languages[name]; present {
			continue // respect the user's existing entry
		}
		file.Languages[name] = lang
		added = append(added, name)
	}
	sort.Strings(added)
	if len(added) == 0 {
		return nil, nil
	}
	if err := writeUserConfig(path, &file, existing); err != nil {
		return nil, err
	}
	return added, nil
}

// SetLanguagesEnabled writes enable: <enabled> for the named languages into
// the user config file, creating it if needed. The special name "all" toggles
// the global lsp.enabled flag instead. Unlike MergeUserLanguages, existing
// entries ARE modified — but only their enable flag; other tuning is kept.
// Returns the config path written.
func SetLanguagesEnabled(names []string, enabled bool) (string, error) {
	file, existing, path, err := readUserConfig()
	if err != nil {
		return "", err
	}
	for _, name := range names {
		if name == "all" {
			file.LSP.Enabled = enabled
			continue
		}
		lc := file.Languages[name] // zero value when absent — extensions come from defaults at load time
		v := enabled
		lc.Enabled = &v
		file.Languages[name] = lc
	}
	if err := writeUserConfig(path, &file, existing); err != nil {
		return "", err
	}
	return path, nil
}

// SetThemeSyntax writes theme.syntax into the user config file, creating it if
// needed (prior content backed up to config.yaml.bak). Returns the path written.
func SetThemeSyntax(name string) (string, error) {
	file, existing, path, err := readUserConfig()
	if err != nil {
		return "", err
	}
	file.Theme.Syntax = name
	if err := writeUserConfig(path, &file, existing); err != nil {
		return "", err
	}
	return path, nil
}

// SetKeybinds replaces the keybinds section of the user config file, creating
// it if needed (prior content backed up to config.yaml.bak). Callers pass the
// full action→key map so the file lists every binding explicitly. Returns the
// path written.
func SetKeybinds(bindings map[string]string) (string, error) {
	file, existing, path, err := readUserConfig()
	if err != nil {
		return "", err
	}
	file.Keybinds = bindings
	if err := writeUserConfig(path, &file, existing); err != nil {
		return "", err
	}
	return path, nil
}

func merge(cfg *Config, path string, trustExec bool) (warnings []string) {
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			warnings = append(warnings, "config "+path+" unreadable: "+firstLine(err.Error()))
		}
		return warnings
	}
	// Languages need union semantics for extensions, which plain unmarshal
	// (replace) can't do — so decode the file's languages separately, then merge.
	prior := cfg.Languages
	cfg.Languages = nil
	// yaml.v3 decodes into an existing map in place, so clear it first: the
	// user file's keybinds replace (not merge), and a project file's are
	// detected and dropped below.
	priorKeybinds := cfg.Keybinds
	cfg.Keybinds = nil
	if err := yaml.Unmarshal(data, cfg); err != nil {
		// Fields parsed before the error may already be applied; report rather
		// than silently running a half-configured editor.
		warnings = append(warnings, "config "+path+" is malformed (partially applied): "+firstLine(err.Error()))
	}
	if !trustExec {
		for name, l := range cfg.Languages {
			// Untrusted (project-local) files may not name executables: lsp
			// covers command/args/init/bridge, install covers --prepare-lsp.
			l.LSP, l.Install = nil, nil
			cfg.Languages[name] = l
		}
		if len(cfg.Keybinds) > 0 {
			warnings = append(warnings, "config "+path+": keybinds ignored — set them in the user config")
		}
		cfg.Keybinds = priorKeybinds
	}
	cfg.Languages = mergeLanguages(prior, cfg.Languages)
	return warnings
}

// firstLine clips a (possibly multi-line yaml) error to its first line.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// mergeLanguages overlays the file's languages onto the accumulated ones: a
// language's Extensions are unioned (so config extends the defaults), and its
// LSP command/args/init overlay the default when set. New languages are added.
func mergeLanguages(base, overlay map[string]LanguageConfig) map[string]LanguageConfig {
	out := map[string]LanguageConfig{}
	for name, lang := range base {
		out[name] = lang
	}
	for name, o := range overlay {
		b, ok := out[name]
		if !ok {
			out[name] = o
			continue
		}
		b.Extensions = unionStrings(b.Extensions, o.Extensions)
		if o.LSP != nil {
			b.LSP = mergeLSP(b.LSP, o.LSP)
		}
		if o.Enabled != nil {
			b.Enabled = o.Enabled
		}
		if o.Install != nil {
			b.Install = o.Install
		}
		out[name] = b
	}
	return out
}

// mergeLSP overlays the set fields of o onto base (a nil base is replaced whole).
func mergeLSP(base, o *LSPServerConfig) *LSPServerConfig {
	if base == nil {
		return o
	}
	res := *base
	if o.Command != "" {
		res.Command = o.Command
	}
	if o.Args != nil {
		res.Args = o.Args
	}
	if o.Init != nil {
		res.Init = o.Init
	}
	if o.Bridge != nil {
		res.Bridge = o.Bridge
	}
	return &res
}

// unionStrings appends add's new members to base, preserving order, de-duped.
func unionStrings(base, add []string) []string {
	seen := make(map[string]bool, len(base)+len(add))
	out := make([]string, 0, len(base)+len(add))
	for _, s := range base {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, s := range add {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
