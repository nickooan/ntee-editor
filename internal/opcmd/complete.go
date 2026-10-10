package opcmd

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Candidate is one placeholder completion.
type Candidate struct {
	Insert string // the full placeholder, e.g. "{$2}"
	Detail string
}

// maxCompleteCandidates caps the completion menu; there are only a handful of
// system variables, so this bounds the argument entries.
const maxCompleteCandidates = 8

var argPlaceholder = regexp.MustCompile(`\{\$([0-9]+)\}`)

// Complete finds the partial placeholder ending at cursor (a rune index) —
// "{", "{$", "$", "{$f", "$2" — and returns where it starts plus the matching
// candidates: the next unused argument first (auto-incremented past the
// highest {$n} already in the template), then the existing arguments, then
// the system variables. No fragment, or nothing matching, returns no
// candidates.
func Complete(value string, cursor int) (start int, candidates []Candidate) {
	runes := []rune(value)
	if cursor < 0 || cursor > len(runes) {
		return 0, nil
	}
	nameStart := cursor
	for nameStart > 0 && (unicode.IsLetter(runes[nameStart-1]) || unicode.IsDigit(runes[nameStart-1])) {
		nameStart--
	}
	name := string(runes[nameStart:cursor])
	start = nameStart
	switch {
	case start > 0 && runes[start-1] == '$':
		start--
		if start > 0 && runes[start-1] == '{' {
			start--
		}
	case start > 0 && runes[start-1] == '{' && name == "":
		start--
	default:
		return 0, nil
	}

	// The fragment itself is never a complete placeholder, so scanning the
	// whole value counts only finished {$n} references.
	highest := 0
	for _, match := range argPlaceholder.FindAllStringSubmatch(value, -1) {
		if index, err := strconv.Atoi(match[1]); err == nil {
			highest = max(highest, index)
		}
	}
	add := func(placeholderName, detail string) {
		if strings.HasPrefix(placeholderName, name) && len(candidates) < maxCompleteCandidates {
			candidates = append(candidates, Candidate{Insert: "{$" + placeholderName + "}", Detail: detail})
		}
	}
	add(strconv.Itoa(highest+1), "new argument")
	for index := 1; index <= highest; index++ {
		add(strconv.Itoa(index), "argument "+strconv.Itoa(index))
	}
	for _, system := range SystemVars {
		add(system, SystemVarDetails[system])
	}
	return start, candidates
}
