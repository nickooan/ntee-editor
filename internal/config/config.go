// Package config loads the editor's YAML configuration. Load order:
// built-in defaults ← ~/.config/ntee-editor/config.yaml ← <project>/.ntee-editor.yaml
// (later files override fields they set; lists replace).
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
			Ignore: []string{".git", "node_modules", "dist"},
		},
		Theme: ThemeConfig{Syntax: "gruvbox"},
		Languages: map[string]LanguageConfig{
			"go": {
				Extensions: []string{".go"},
				LSP:        &LSPServerConfig{Command: "gopls"},
			},
			"typescript": {
				Extensions: []string{".ts", ".tsx"},
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
	_ = yaml.Unmarshal(data, cfg) // fields absent from the file keep prior values
}
