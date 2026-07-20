// Package config loads the editor's YAML configuration. Load order:
// built-in defaults ← ~/.config/ntee-editor/config.yaml ← <project>/.ntee-editor.yaml
// (later files override fields they set; most lists replace). Exception: a
// language's `extensions` are UNIONED with the built-in defaults, so a config
// extends (never shrinks) the set of file types routed to an LSP server; other
// language fields (command/args/init) overlay the default when set.
package config

import (
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Version   int                       `yaml:"version"`
	Editor    EditorConfig              `yaml:"editor"`
	Tree      TreeConfig                `yaml:"tree"`
	Theme     ThemeConfig               `yaml:"theme"`
	Languages map[string]LanguageConfig `yaml:"languages"`
	LSP       LSPConfig                 `yaml:"lsp"`
	Keybinds  map[string]string         `yaml:"keybinds"` // reserved, not applied in v1
}

type EditorConfig struct {
	TabWidth       int `yaml:"tab_width"`
	MaxSnapshots   int `yaml:"max_snapshots"`
	MaxHighlightKB int `yaml:"max_highlight_kb"`
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
	Extensions []string          `yaml:"extensions"`
	LSP        *LSPServerConfig  `yaml:"lsp"`
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
			TabWidth:       4,
			MaxSnapshots:   50,
			MaxHighlightKB: 512,
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

// Load builds the effective config for a project root. Missing files are fine;
// a malformed file is skipped (the editor should still start).
func Load(projectRoot string) Config {
	cfg := Default()
	if path, err := ConfigPath(); err == nil {
		merge(&cfg, path)
	}
	merge(&cfg, filepath.Join(projectRoot, ".ntee-editor.yaml"))
	if cfg.Editor.TabWidth < 1 {
		cfg.Editor.TabWidth = 4
	}
	if cfg.Editor.MaxSnapshots < 1 {
		cfg.Editor.MaxSnapshots = 50
	}
	if cfg.Tree.MaxIndexFiles < 1 {
		cfg.Tree.MaxIndexFiles = 50000
	}
	return cfg
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

// MergeUserLanguages adds languages to the user config file, keeping existing
// entries (and their tuning) untouched — a language already present is skipped.
// The prior file is backed up to config.yaml.bak. Returns the languages added.
// Note: comments in the existing file are lost on rewrite (hence the backup).
func MergeUserLanguages(langs map[string]LanguageConfig) (added []string, err error) {
	path, err := ConfigPath()
	if err != nil {
		return nil, err
	}

	var file Config
	existing, readErr := os.ReadFile(path)
	if readErr == nil {
		_ = yaml.Unmarshal(existing, &file) // best-effort; malformed → treated as empty
	} else {
		// No config yet: seed the non-language sections from defaults. Otherwise
		// we'd marshal bool/int zero values — notably lsp.enabled:false, which
		// DISABLES all LSP on load — into the fresh file. Languages stay empty so
		// only the prepared servers are written (not the default recipes).
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

	out, err := yaml.Marshal(&file)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if readErr == nil {
		_ = os.WriteFile(path+".bak", existing, 0o644) // best-effort backup
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return nil, err
	}
	return added, nil
}

func merge(cfg *Config, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	// Languages need union semantics for extensions, which plain unmarshal
	// (replace) can't do — so decode the file's languages separately, then merge.
	prior := cfg.Languages
	cfg.Languages = nil
	_ = yaml.Unmarshal(data, cfg) // scalar fields absent from the file keep prior values
	cfg.Languages = mergeLanguages(prior, cfg.Languages)
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
