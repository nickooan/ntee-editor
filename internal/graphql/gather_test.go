package graphql

import (
	"fmt"
	"path"
	"sort"
	"strings"
	"testing"
)

// fakeFS backs a GatherIO with an in-memory rel→content map.
type fakeFS map[string]string

func (f fakeFS) io() GatherIO {
	return GatherIO{
		ReadFile: func(rel string) ([]byte, error) {
			c, ok := f[rel]
			if !ok {
				return nil, fmt.Errorf("no such file: %s", rel)
			}
			return []byte(c), nil
		},
		ListDir: func(dir string) ([]string, error) {
			var names []string
			for rel := range f {
				if dirOf(rel) == dir {
					names = append(names, path.Base(rel))
				}
			}
			sort.Strings(names)
			return names, nil
		},
		ListAll: func() []string {
			out := make([]string, 0, len(f))
			for rel := range f {
				out = append(out, rel)
			}
			sort.Strings(out)
			return out
		},
	}
}

func TestGatherDirDefault(t *testing.T) {
	fs := fakeFS{
		"api/a.graphql":   "",
		"api/b.gql":       "",
		"api/readme.md":   "",
		"other/d.graphql": "",
	}
	files, cfg, notes := Gather("api/a.graphql", fs.io())
	if cfg != "" || len(notes) != 0 {
		t.Fatalf("cfg=%q notes=%v", cfg, notes)
	}
	if got := strings.Join(files, " "); got != "api/a.graphql api/b.gql" {
		t.Fatalf("files = %q", got)
	}
}

func TestGatherRootFile(t *testing.T) {
	fs := fakeFS{"schema.graphql": "", "extra.gql": ""}
	files, _, _ := Gather("schema.graphql", fs.io())
	if got := strings.Join(files, " "); got != "extra.gql schema.graphql" {
		t.Fatalf("files = %q", got)
	}
}

func TestGatherConfigString(t *testing.T) {
	fs := fakeFS{
		".graphqlrc.yml":       `schema: "sdl/**/*.graphql"`,
		"sdl/a.graphql":        "",
		"sdl/nested/b.graphql": "",
		"ignored.graphql":      "",
	}
	files, cfg, notes := Gather("sdl/a.graphql", fs.io())
	if cfg != ".graphqlrc.yml" || len(notes) != 0 {
		t.Fatalf("cfg=%q notes=%v", cfg, notes)
	}
	if got := strings.Join(files, " "); got != "sdl/a.graphql sdl/nested/b.graphql" {
		t.Fatalf("files = %q", got)
	}
}

// The config's globs are relative to the config file's own directory, found
// walking up from the open file.
func TestGatherConfigInParentDir(t *testing.T) {
	fs := fakeFS{
		"app/graphql.config.yml": `schema: "sdl/*.graphql"`,
		"app/sdl/a.graphql":      "",
		"app/sdl/b.graphql":      "",
		"sdl/outside.graphql":    "",
	}
	files, cfg, _ := Gather("app/sdl/a.graphql", fs.io())
	if cfg != "app/graphql.config.yml" {
		t.Fatalf("cfg = %q", cfg)
	}
	if got := strings.Join(files, " "); got != "app/sdl/a.graphql app/sdl/b.graphql" {
		t.Fatalf("files = %q", got)
	}
}

func TestGatherConfigListAndSkips(t *testing.T) {
	fs := fakeFS{
		".graphqlrc.yml": "schema:\n  - \"a/*.graphql\"\n  - \"https://example.com/sdl\"\n",
		"a/x.graphql":    "",
		"b/y.graphql":    "",
	}
	files, _, notes := Gather("a/x.graphql", fs.io())
	if got := strings.Join(files, " "); got != "a/x.graphql" {
		t.Fatalf("files = %q", got)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "skipped schema entry: https://example.com/sdl") {
		t.Fatalf("notes = %v", notes)
	}
}

// The open file always joins the set, even when the config's globs miss it.
func TestGatherConfigAlwaysIncludesOpenFile(t *testing.T) {
	fs := fakeFS{
		".graphqlrc.yml": `schema: "a/*.graphql"`,
		"a/x.graphql":    "",
		"b/y.graphql":    "",
	}
	files, _, _ := Gather("b/y.graphql", fs.io())
	if got := strings.Join(files, " "); got != "a/x.graphql b/y.graphql" {
		t.Fatalf("files = %q", got)
	}
}

func TestGatherConfigNoMatches(t *testing.T) {
	fs := fakeFS{
		".graphqlrc.yml": `schema: "missing/*.graphql"`,
		"a/x.graphql":    "",
	}
	files, _, notes := Gather("a/x.graphql", fs.io())
	if got := strings.Join(files, " "); got != "a/x.graphql" {
		t.Fatalf("files = %q", got)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "matched no files") {
		t.Fatalf("notes = %v", notes)
	}
}

func TestMatchGlobDoubleStar(t *testing.T) {
	cases := []struct {
		pattern, rel string
		want         bool
	}{
		{"src/**/*.graphql", "src/a/b/x.graphql", true},
		{"src/**/*.graphql", "src/x.graphql", true},
		{"src/**/*.graphql", "other/x.graphql", false},
		{"**/*.gql", "x.gql", true},
		{"**/*.gql", "a/b/x.gql", true},
		{"*.graphql", "a/b.graphql", false},
		{"a/*.graphql", "a/b.graphql", true},
		{"a/*.graphql", "a/b/c.graphql", false},
	}
	for _, c := range cases {
		if got := matchGlob(c.pattern, c.rel); got != c.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", c.pattern, c.rel, got, c.want)
		}
	}
}
