package graphql

import (
	"fmt"
	"path"
	"strings"

	"gopkg.in/yaml.v3"
)

// GatherIO is the injected filesystem surface — the package never touches
// disk. All paths are workspace-root relative ("" = the root itself).
type GatherIO struct {
	ReadFile func(rel string) ([]byte, error)
	ListDir  func(dirRel string) ([]string, error) // non-dir entry names in one dir
	ListAll  func() []string                       // recursive workspace file list (for globs)
}

// configNames are the GraphQL Config files probed per directory, in priority
// order (the graphql-config ecosystem convention).
var configNames = []string{".graphqlrc.yml", ".graphqlrc.yaml", "graphql.config.yml", "graphql.config.yaml"}

// Gather returns the schema file set for the file at openRel. A GraphQL
// Config found walking up from the file's directory to the workspace root
// wins with its schema: globs (relative to the config's dir); otherwise every
// SDL file in the open file's own directory is merged. openRel itself is
// always in the set. notes carries skipped config entries and similar
// degradations for the renderer's title block.
func Gather(openRel string, io GatherIO) (files []string, configFile string, notes []string) {
	if cfgRel, cfgDir, ok := findConfig(openRel, io); ok {
		files, notes = gatherByConfig(cfgRel, cfgDir, io)
		files = ensureIncluded(files, openRel)
		return files, cfgRel, notes
	}
	return gatherSameDir(openRel, io), "", nil
}

// findConfig walks up from openRel's directory probing for a config file via
// ListDir (existence, not content — ReadFile may fold read errors into
// content, so a directory listing is the reliable presence check).
func findConfig(openRel string, io GatherIO) (cfgRel, cfgDir string, ok bool) {
	dir := dirOf(openRel)
	for {
		if names, err := io.ListDir(dir); err == nil {
			present := map[string]bool{}
			for _, n := range names {
				present[n] = true
			}
			for _, name := range configNames {
				if present[name] {
					return joinRel(dir, name), dir, true
				}
			}
		}
		if dir == "" {
			return "", "", false
		}
		dir = dirOf(dir)
	}
}

// gatherByConfig matches the config's schema globs against the recursive
// workspace file list, preserving that list's order (which dedupes across
// overlapping patterns for free).
func gatherByConfig(cfgRel, cfgDir string, io GatherIO) (files []string, notes []string) {
	data, err := io.ReadFile(cfgRel)
	if err != nil {
		return nil, []string{"cannot read " + cfgRel}
	}
	patterns, notes := configPatterns(cfgRel, cfgDir, data)
	if len(patterns) == 0 {
		return nil, append(notes, cfgRel+" has no usable schema entries")
	}
	for _, rel := range io.ListAll() {
		for _, p := range patterns {
			if matchGlob(p, rel) {
				files = append(files, rel)
				break
			}
		}
	}
	if len(files) == 0 {
		notes = append(notes, cfgRel+" schema globs matched no files")
	}
	return files, notes
}

// configPatterns extracts the schema: globs from a GraphQL Config document.
// The field is a string or a list of strings; URL and mapping entries (the
// headers form) are skipped with a note, mirroring how the OpenAPI resolver
// rejects external refs inline instead of failing.
func configPatterns(cfgRel, cfgDir string, data []byte) (patterns, notes []string) {
	var cfg struct {
		Schema yaml.Node `yaml:"schema"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, []string{cfgRel + ": " + firstLine(err.Error())}
	}
	add := func(n *yaml.Node) {
		switch {
		case n.Kind == yaml.ScalarNode && n.Value != "":
			if strings.HasPrefix(n.Value, "http://") || strings.HasPrefix(n.Value, "https://") {
				notes = append(notes, "skipped schema entry: "+n.Value)
				return
			}
			patterns = append(patterns, joinRel(cfgDir, n.Value))
		case n.Kind == 0: // schema: absent
		default:
			notes = append(notes, fmt.Sprintf("skipped schema entry in %s (unsupported form)", cfgRel))
		}
	}
	if cfg.Schema.Kind == yaml.SequenceNode {
		for _, item := range cfg.Schema.Content {
			add(item)
		}
	} else {
		add(&cfg.Schema)
	}
	return patterns, notes
}

// gatherSameDir is the no-config default: every SDL file in the open file's
// own directory.
func gatherSameDir(openRel string, io GatherIO) []string {
	dir := dirOf(openRel)
	names, err := io.ListDir(dir)
	if err != nil {
		return []string{openRel}
	}
	var files []string
	for _, name := range names {
		if Detect(name) {
			files = append(files, joinRel(dir, name))
		}
	}
	return ensureIncluded(files, openRel)
}

func ensureIncluded(files []string, rel string) []string {
	for _, f := range files {
		if f == rel {
			return files
		}
	}
	return append(files, rel)
}

// matchGlob matches a root-relative path against a slash-separated glob where
// "**" spans zero or more whole segments and every other segment goes through
// path.Match. Brace patterns ({a,b}) are not supported — they simply match
// nothing, which surfaces as an empty gather instead of a silent subset.
func matchGlob(pattern, rel string) bool {
	return matchSegs(strings.Split(pattern, "/"), strings.Split(rel, "/"))
}

func matchSegs(ps, ss []string) bool {
	if len(ps) == 0 {
		return len(ss) == 0
	}
	if ps[0] == "**" {
		for i := 0; i <= len(ss); i++ {
			if matchSegs(ps[1:], ss[i:]) {
				return true
			}
		}
		return false
	}
	if len(ss) == 0 {
		return false
	}
	if ok, err := path.Match(ps[0], ss[0]); err != nil || !ok {
		return false
	}
	return matchSegs(ps[1:], ss[1:])
}

// dirOf is path.Dir with "" (not ".") for the workspace root, matching the
// root-relative path convention used across the app.
func dirOf(rel string) string {
	d := path.Dir(rel)
	if d == "." {
		return ""
	}
	return d
}

func joinRel(dir, name string) string {
	if dir == "" {
		return path.Clean(name)
	}
	return path.Join(dir, name)
}
