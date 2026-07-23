package filetree

import (
	"path"
	"strings"

	"github.com/nickooan/ntee-editor/internal/fuzzy"
)

// MaxInputSuggestions caps the query-bar suggestion list. It is deliberately
// large: the popup renders a small window around the selection, and ↓ must be
// able to walk through every match (e.g. all files of a typed directory),
// not just the first screenful.
const MaxInputSuggestions = 200

// InputSuggestion is one row of the query-bar completion popup.
type InputSuggestion struct {
	Label      string
	InsertText string
	Source     string // "file" | "directory"
	Entry      FileTreeEntry
}

func suggestionFor(e FileTreeEntry) InputSuggestion {
	source := "file"
	if e.Type == "directory" {
		source = "directory"
	}
	return InputSuggestion{Label: e.CommandValue, InsertText: e.CommandValue, Source: source, Entry: e}
}

// BuildInputSuggestions completes a typed query-bar path. Exact and prefix
// stages run over the VISIBLE entries (the current expanded tree), preserving
// directory-path navigation; the fuzzy stage runs score-ranked over the full
// file and directory corpus so keywords find entries inside collapsed
// directories. Directory candidates in allDirs carry a trailing "/" (the tree's
// CommandValue convention) and mix into the fuzzy ranking by score. Results are
// deduped by path, ordered exact ++ prefix ++ fuzzy, capped at limit.
func BuildInputSuggestions(visible []FileTreeEntry, allFiles, allDirs []string, command string, limit int) []InputSuggestion {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" || strings.HasPrefix(trimmed, ":") {
		return nil
	}
	normalized := strings.ToLower(strings.ReplaceAll(trimmed, "\\", "/"))

	var exact, prefix []InputSuggestion
	for _, e := range visible {
		cv := strings.ToLower(e.CommandValue)
		name := strings.ToLower(e.Name)
		switch {
		case cv == normalized || name == normalized:
			exact = append(exact, suggestionFor(e))
		case strings.HasPrefix(cv, normalized) || strings.HasPrefix(name, normalized):
			prefix = append(prefix, suggestionFor(e))
		}
	}

	candidates := make([]string, 0, len(allFiles)+len(allDirs))
	candidates = append(candidates, allFiles...)
	candidates = append(candidates, allDirs...)
	fz := make([]InputSuggestion, 0, limit)
	for _, m := range fuzzy.Filter(normalized, fuzzy.Prepare(candidates)) {
		cv := candidates[m.Index]
		rel, typ := cv, "file"
		if m.Index >= len(allFiles) {
			rel, typ = strings.TrimSuffix(cv, "/"), "directory"
		}
		fz = append(fz, InputSuggestion{
			Label:      cv,
			InsertText: cv,
			Source:     typ,
			Entry: FileTreeEntry{
				Name:         path.Base(rel),
				RelativePath: rel,
				CommandValue: cv,
				Type:         typ,
			},
		})
		if len(fz) >= limit {
			break
		}
	}

	seen := map[string]bool{}
	out := make([]InputSuggestion, 0, limit)
	for _, group := range [][]InputSuggestion{exact, prefix, fz} {
		for _, s := range group {
			key := strings.ToLower(s.InsertText)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, s)
			if len(out) >= limit {
				return out
			}
		}
	}
	return out
}
