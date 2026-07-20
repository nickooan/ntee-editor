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
// file corpus so keywords find files inside collapsed directories. Results are
// deduped by path, ordered exact ++ prefix ++ fuzzy, capped at limit.
func BuildInputSuggestions(visible []FileTreeEntry, allFiles []string, command string, limit int) []InputSuggestion {
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

	fz := make([]InputSuggestion, 0, limit)
	for _, m := range fuzzy.Filter(normalized, fuzzy.Prepare(allFiles)) {
		rel := allFiles[m.Index]
		fz = append(fz, InputSuggestion{
			Label:      rel,
			InsertText: rel,
			Source:     "file",
			Entry: FileTreeEntry{
				Name:         path.Base(rel),
				RelativePath: rel,
				CommandValue: rel,
				Type:         "file",
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
