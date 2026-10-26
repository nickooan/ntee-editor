package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Tab sizes the editor accepts, globally and per language.
const (
	DefaultTabWidth = 4
	MinTabWidth     = 1
	MaxTabWidth     = 16
)

// CodeStyleLanguage is a language the code-style settings can target, with
// the files that belong to it.
type CodeStyleLanguage struct {
	Key        string   // config key under editor.language_tab_widths
	Label      string   // display name
	Extensions []string // lower-case, with the dot
	Basenames  []string // exact file names (Makefile)
}

// CodeStyleLanguages are the per-language rows of the code-style settings,
// in display order.
var CodeStyleLanguages = []CodeStyleLanguage{
	{Key: "go", Label: "Go", Extensions: []string{".go"}},
	{Key: "typescript", Label: "TypeScript", Extensions: []string{".ts", ".tsx", ".mts", ".cts"}},
	{Key: "javascript", Label: "JavaScript", Extensions: []string{".js", ".jsx", ".mjs", ".cjs"}},
	{Key: "python", Label: "Python", Extensions: []string{".py", ".pyi"}},
	{Key: "rust", Label: "Rust", Extensions: []string{".rs"}},
	{Key: "java", Label: "Java", Extensions: []string{".java"}},
	{Key: "kotlin", Label: "Kotlin", Extensions: []string{".kt", ".kts"}},
	{Key: "ruby", Label: "Ruby", Extensions: []string{".rb"}},
	{Key: "c", Label: "C", Extensions: []string{".c", ".h"}},
	{Key: "cpp", Label: "C++", Extensions: []string{".cc", ".cpp", ".cxx", ".hpp", ".hh"}},
	{Key: "csharp", Label: "C#", Extensions: []string{".cs"}},
	{Key: "php", Label: "PHP", Extensions: []string{".php"}},
	{Key: "swift", Label: "Swift", Extensions: []string{".swift"}},
	{Key: "shell", Label: "Shell", Extensions: []string{".sh", ".bash", ".zsh"}},
	{Key: "yaml", Label: "YAML", Extensions: []string{".yaml", ".yml"}},
	{Key: "json", Label: "JSON", Extensions: []string{".json"}},
	{Key: "toml", Label: "TOML", Extensions: []string{".toml"}},
	{Key: "markdown", Label: "Markdown", Extensions: []string{".md", ".markdown"}},
	{Key: "html", Label: "HTML", Extensions: []string{".html", ".htm"}},
	{Key: "css", Label: "CSS", Extensions: []string{".css", ".scss"}},
	{Key: "sql", Label: "SQL", Extensions: []string{".sql"}},
	{Key: "lua", Label: "Lua", Extensions: []string{".lua"}},
	{Key: "vue", Label: "Vue", Extensions: []string{".vue"}},
	{Key: "makefile", Label: "Makefile", Extensions: []string{".mk"}, Basenames: []string{"Makefile", "makefile", "GNUmakefile"}},
}

var (
	codeStyleByExtension = map[string]string{}
	codeStyleByBasename  = map[string]string{}
	codeStyleKeys        = map[string]bool{}
)

func init() {
	for _, language := range CodeStyleLanguages {
		codeStyleKeys[language.Key] = true
		for _, extension := range language.Extensions {
			codeStyleByExtension[extension] = language.Key
		}
		for _, basename := range language.Basenames {
			codeStyleByBasename[basename] = language.Key
		}
	}
}

// IsCodeStyleLanguage reports whether key names a CodeStyleLanguages row.
func IsCodeStyleLanguage(key string) bool { return codeStyleKeys[key] }

// LanguageForPath is the code-style language of a file, "" when none of
// CodeStyleLanguages claims it.
func LanguageForPath(path string) string {
	base := filepath.Base(path)
	if key, ok := codeStyleByBasename[base]; ok {
		return key
	}
	return codeStyleByExtension[strings.ToLower(filepath.Ext(base))]
}

// TabWidthFor is the tab size for a file: its language's own size when set,
// else the editor-wide one.
func (cfg Config) TabWidthFor(path string) int {
	if width, ok := cfg.Editor.LanguageTabWidths[LanguageForPath(path)]; ok {
		return width
	}
	return cfg.Editor.TabWidth
}

// ProjectCodeStyle records which tab sizes the project-local file set, so
// the settings panel can tell the user that file decides them.
type ProjectCodeStyle struct {
	TabWidth  bool
	Languages map[string]bool
}

// readProjectCodeStyle notes the tab sizes a project file sets. A missing or
// malformed file sets none (merge already reported a malformed one).
func readProjectCodeStyle(path string) ProjectCodeStyle {
	var file struct {
		Editor struct {
			TabWidth          *int           `yaml:"tab_width"`
			LanguageTabWidths map[string]int `yaml:"language_tab_widths"`
		} `yaml:"editor"`
	}
	style := ProjectCodeStyle{Languages: map[string]bool{}}
	data, err := os.ReadFile(path)
	if err != nil || yaml.Unmarshal(data, &file) != nil {
		return style
	}
	style.TabWidth = file.Editor.TabWidth != nil
	for key := range file.Editor.LanguageTabWidths {
		style.Languages[key] = true
	}
	return style
}

// validateTabWidths clamps the editor-wide size and drops per-language sizes
// that are out of range or name an unknown language, reporting each drop.
func validateTabWidths(cfg *Config) (warnings []string) {
	if cfg.Editor.TabWidth < MinTabWidth || cfg.Editor.TabWidth > MaxTabWidth {
		if cfg.Editor.TabWidth != 0 {
			warnings = append(warnings, fmt.Sprintf("config: editor.tab_width %d is outside %d–%d, using %d", cfg.Editor.TabWidth, MinTabWidth, MaxTabWidth, DefaultTabWidth))
		}
		cfg.Editor.TabWidth = DefaultTabWidth
	}
	valid := make(map[string]int, len(cfg.Editor.LanguageTabWidths))
	keys := make([]string, 0, len(cfg.Editor.LanguageTabWidths))
	for key := range cfg.Editor.LanguageTabWidths {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		width := cfg.Editor.LanguageTabWidths[key]
		switch {
		case !IsCodeStyleLanguage(key):
			warnings = append(warnings, "config: editor.language_tab_widths: unknown language "+key+" ignored")
		case width < MinTabWidth || width > MaxTabWidth:
			warnings = append(warnings, fmt.Sprintf("config: editor.language_tab_widths.%s %d is outside %d–%d, ignored", key, width, MinTabWidth, MaxTabWidth))
		default:
			valid[key] = width
		}
	}
	cfg.Editor.LanguageTabWidths = valid
	return warnings
}

// SetTabWidths writes the editor-wide tab size and the full per-language map
// into the user config file, creating it if needed (prior content backed up
// to config.yaml.bak). Returns the path written.
func SetTabWidths(tabWidth int, languageTabWidths map[string]int) (string, error) {
	file, existing, path, err := readUserConfig()
	if err != nil {
		return "", err
	}
	file.Editor.TabWidth = tabWidth
	file.Editor.LanguageTabWidths = languageTabWidths
	if file.Editor.LanguageTabWidths == nil {
		file.Editor.LanguageTabWidths = map[string]int{}
	}
	if err := writeUserConfig(path, &file, existing); err != nil {
		return "", err
	}
	return path, nil
}
