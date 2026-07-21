package app

import (
	"path/filepath"
	"strings"
)

// execVerbs are the @exec bar's built-in commands, in display order.
var execVerbs = []string{"copy", "cp", "jump", "jp", "tab", "git"}

// execSuggestions returns completion candidates for the bar input's trailing
// token (the text after the last space; an empty trailing token offers every
// option for that position). Static verbs and args come from a fixed table;
// `tab` also offers the open tabs' base names, and `git scf` offers the
// conflict labels actually present in the buffer plus "both". Candidates are
// prefix-filtered against the token, case-insensitively.
func (m Model) execSuggestions(input string) []string {
	// Leading spaces carry no meaning; trailing ones do (they start a new token).
	input = strings.TrimLeft(input, " ")
	prev, token := splitTrailingToken(input)

	var cands []string
	switch {
	case prev == "":
		cands = execVerbs
	case prev == "copy" || prev == "cp":
		cands = []string{"all", "fpath"} // line ranges stay free-form
	case prev == "jump" || prev == "jp":
		cands = []string{"top", "end"} // line numbers stay free-form
	case prev == "tab":
		cands = append([]string{"cl", "cr"}, tabBaseNames(m.tabs)...)
	case prev == "git":
		cands = []string{"scf"}
	case prev == "git scf":
		cands = m.conflictSideCandidates()
	default:
		return nil // position past any known argument
	}

	var out []string
	for _, c := range cands {
		if strings.HasPrefix(strings.ToLower(c), strings.ToLower(token)) {
			out = append(out, c)
		}
	}
	return out
}

// splitTrailingToken cuts input into the completed words before the last space
// (space-normalized, e.g. "git scf") and the trailing token being typed.
func splitTrailingToken(input string) (prev, token string) {
	i := strings.LastIndex(input, " ")
	if i == -1 {
		return "", input
	}
	return strings.Join(strings.Fields(input[:i]), " "), input[i+1:]
}

// tabBaseNames lists the open tabs' base names (what findTab matches first),
// deduped in tab order.
func tabBaseNames(tabs []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(tabs))
	for _, rel := range tabs {
		base := filepath.Base(rel)
		if !seen[base] {
			seen[base] = true
			out = append(out, base)
		}
	}
	return out
}

// conflictSideCandidates lists the marker labels of every conflict block in
// the current buffer (deduped, encounter order) plus "both" — the exact
// targets `git scf` accepts. Empty when the buffer has no conflict blocks:
// there is nothing valid to suggest.
func (m Model) conflictSideCandidates() []string {
	blocks := findConflictBlocks(m.edit.lines)
	if len(blocks) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(label string) {
		if label != "" && !seen[label] {
			seen[label] = true
			out = append(out, label)
		}
	}
	for _, b := range blocks {
		add(b.oursLabel)
		add(b.theirsLabel)
	}
	return append(out, "both")
}

// acceptExecSuggestion replaces the input's trailing token with s plus a
// trailing space (harmless — runExecCommand trims — and it chains multi-token
// completions like git<Tab>scf<Tab>).
func acceptExecSuggestion(input, s string) string {
	if i := strings.LastIndex(input, " "); i != -1 {
		return input[:i+1] + s + " "
	}
	return s + " "
}
