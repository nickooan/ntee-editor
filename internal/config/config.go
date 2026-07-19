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
}

type ThemeConfig struct {
	Syntax string `yaml:"syntax"`
}

type LanguageConfig struct {
	Extensions []string         `yaml:"extensions"`
	LSP        *LSPServerConfig `yaml:"lsp"`
}

type LSPServerConfig struct {
	Command string   `yaml:"command"`
	Args    []string `yaml:"args"`
	// Init is passed through as LSP initializationOptions (e.g.
	// typescript-language-server's tsserver.path).
	Init map[string]any `yaml:"init"`
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
			// Only .git is hard-hidden. Other noise (node_modules, dist, …) is
			// handled by .gitignore: shown grayed in the tree, browsable one
			// level at a time, and kept out of the search corpus.
			Ignore: []string{".git"},
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
	if home, err := os.UserHomeDir(); err == nil {
		dir := os.Getenv("XDG_CONFIG_HOME")
		if dir == "" {
			dir = filepath.Join(home, ".config")
		}
		merge(&cfg, filepath.Join(dir, "ntee-editor", "config.yaml"))
	}
	merge(&cfg, filepath.Join(projectRoot, ".ntee-editor.yaml"))
	if cfg.Editor.TabWidth < 1 {
		cfg.Editor.TabWidth = 4
	}
	if cfg.Editor.MaxSnapshots < 1 {
		cfg.Editor.MaxSnapshots = 50
	}
	return cfg
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
