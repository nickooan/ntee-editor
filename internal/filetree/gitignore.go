package filetree

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Gitignore matches root-relative slash paths against the rules of a project's
// .gitignore. It is a pragmatic subset of the gitignore spec: comments, blank
// lines, negation (!), directory-only (trailing /), anchoring (leading or
// internal /), and *, ?, ** globs. Rules apply in order, last match wins.
//
// Scope: only the root .gitignore is read (no nested per-directory gitignore or
// .git/info/exclude), and a `!` re-include under an already-ignored directory is
// not honored. It drives a visual cue (graying ignored files), not any hard
// exclusion.
type Gitignore struct {
	rules []gitRule
}

type gitRule struct {
	re      *regexp.Regexp
	negated bool
	dirOnly bool
}

// LoadGitignore reads <root>/.gitignore and compiles it. It returns nil when the
// file is absent or unreadable — a nil *Gitignore matches nothing.
func LoadGitignore(root string) *Gitignore {
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		return nil
	}
	return CompileGitignore(strings.Split(string(data), "\n"))
}

// CompileGitignore builds a matcher from raw .gitignore lines.
func CompileGitignore(lines []string) *Gitignore {
	g := &Gitignore{}
	for _, line := range lines {
		if r, ok := compileGitRule(line); ok {
			g.rules = append(g.rules, r)
		}
	}
	return g
}

// Match reports whether a root-relative, slash-separated path is ignored. isDir
// lets directory-only patterns (trailing /) apply only to directories.
func (g *Gitignore) Match(path string, isDir bool) bool {
	if g == nil {
		return false
	}
	path = strings.TrimPrefix(path, "/")
	ignored := false
	for _, r := range g.rules {
		if r.dirOnly && !isDir {
			continue
		}
		if r.re.MatchString(path) {
			ignored = !r.negated
		}
	}
	return ignored
}

// compileGitRule turns one .gitignore line into a rule. ok is false for blank
// lines and comments.
func compileGitRule(line string) (gitRule, bool) {
	line = strings.TrimRight(line, " \t\r")
	if line == "" || strings.HasPrefix(line, "#") {
		return gitRule{}, false
	}

	var r gitRule
	if strings.HasPrefix(line, "!") {
		r.negated = true
		line = line[1:]
	}
	if strings.HasSuffix(line, "/") {
		r.dirOnly = true
		line = strings.TrimSuffix(line, "/")
	}
	if line == "" {
		return gitRule{}, false
	}

	// A leading or internal slash anchors the pattern to the root; otherwise it
	// matches the basename at any depth.
	anchored := strings.HasPrefix(line, "/") || strings.Contains(line, "/")
	line = strings.TrimPrefix(line, "/")

	expr := "^" + globToRegexp(line) + "$"
	if !anchored {
		expr = "(^|.*/)" + globToRegexp(line) + "$"
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return gitRule{}, false
	}
	r.re = re
	return r, true
}

// globToRegexp translates gitignore glob syntax into a regexp body: `**` spans
// path segments, `*` stays within one segment, `?` is a single non-slash rune,
// and every other regexp metacharacter is escaped.
func globToRegexp(pat string) string {
	var b strings.Builder
	runes := []rune(pat)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch c {
		case '*':
			if i+1 < len(runes) && runes[i+1] == '*' {
				// `**` — consume the pair and an optional trailing slash so
				// `**/` and `a/**/b` collapse the separator too.
				i++
				if i+1 < len(runes) && runes[i+1] == '/' {
					i++
					b.WriteString("(.*/)?")
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '.', '+', '(', ')', '|', '^', '$', '{', '}', '[', ']', '\\':
			b.WriteByte('\\')
			b.WriteRune(c)
		default:
			b.WriteRune(c)
		}
	}
	return b.String()
}
