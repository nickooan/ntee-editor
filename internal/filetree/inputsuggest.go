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

// PrepareCorpus precomputes the fuzzy-matching data for a files-then-dirs
// corpus, for callers that keep the corpus around across keystrokes: pass the
// result to BuildInputSuggestions instead of letting it re-prepare the whole
// corpus (up to Tree.MaxIndexFiles candidates) on every call.
func PrepareCorpus(allFiles, allDirs []string) []fuzzy.Prepared {
	all := make([]string, 0, len(allFiles)+len(allDirs))
	all = append(all, allFiles...)
	all = append(all, allDirs...)
	return fuzzy.Prepare(all)
}

// BuildInputSuggestions completes a typed query-bar path. Exact and prefix
// stages run over the VISIBLE entries (the current expanded tree), preserving
// directory-path navigation; the fuzzy stage runs score-ranked over the full
// file and directory corpus so keywords find entries inside collapsed
// directories. Directory candidates in allDirs carry a trailing "/" (the tree's
// CommandValue convention) and mix into the fuzzy ranking by score. Results are
// deduped by path, ordered exact ++ prefix ++ fuzzy, capped at limit.
//
// prepared, when non-nil, must be PrepareCorpus(allFiles, allDirs) for exactly
// these slices (match indexes resolve into them); nil prepares locally.
func BuildInputSuggestions(visible []FileTreeEntry, allFiles, allDirs []string, prepared []fuzzy.Prepared, command string, limit int) []InputSuggestion {
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

	if prepared == nil {
		prepared = PrepareCorpus(allFiles, allDirs)
	}
	fz := make([]InputSuggestion, 0, limit)
	for _, m := range fuzzy.Filter(normalized, prepared) {
		cv, typ := "", "file"
		if m.Index < len(allFiles) {
			cv = allFiles[m.Index]
		} else {
			cv, typ = allDirs[m.Index-len(allFiles)], "directory"
		}
		rel := cv
		if typ == "directory" {
			rel = strings.TrimSuffix(cv, "/")
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
