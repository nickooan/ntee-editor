package view

import (
	"regexp"
	"sort"
	"strings"
)

// SearchMatch locates one match. Start/End are byte offsets into the line
// (consistent for slicing within a line).
type SearchMatch struct {
	LineIndex int
	Start     int
	End       int
}

// LineMatch is a SearchMatch tagged with its global match index (for the
// focused highlight).
type LineMatch struct {
	SearchMatch
	MatchIndex int
}

// CreateSearchRegex builds a case-insensitive regex from the query, falling
// back to a literal match when the query is not valid regex.
func CreateSearchRegex(query string) *regexp.Regexp {
	if query == "" {
		return nil
	}
	if re, err := regexp.Compile("(?i)" + query); err == nil {
		return re
	}
	re, _ := regexp.Compile("(?i)" + regexp.QuoteMeta(query))
	return re
}

// CreateMultilineSearchRegex is CreateSearchRegex in multi-line mode: ^ and $
// match at line boundaries. Required when matching whole file content in one
// pass, so anchored queries behave exactly as they would line-at-a-time.
func CreateMultilineSearchRegex(query string) *regexp.Regexp {
	if query == "" {
		return nil
	}
	if re, err := regexp.Compile("(?im)" + query); err == nil {
		return re
	}
	re, _ := regexp.Compile("(?im)" + regexp.QuoteMeta(query))
	return re
}

// FindSearchMatches returns every match of query across content.
func FindSearchMatches(content, query string) []SearchMatch {
	re := CreateSearchRegex(query)
	if re == nil {
		return nil
	}

	var matches []SearchMatch
	for lineIndex, line := range strings.Split(content, "\n") {
		for _, loc := range re.FindAllStringIndex(line, -1) {
			if loc[0] == loc[1] {
				continue
			}
			matches = append(matches, SearchMatch{LineIndex: lineIndex, Start: loc[0], End: loc[1]})
		}
	}
	return matches
}

// BuildMatchesByLine buckets matches by line, preserving each match's global
// index and sorting each bucket by start.
func BuildMatchesByLine(matches []SearchMatch) map[int][]LineMatch {
	byLine := map[int][]LineMatch{}
	for i, m := range matches {
		byLine[m.LineIndex] = append(byLine[m.LineIndex], LineMatch{SearchMatch: m, MatchIndex: i})
	}
	for _, bucket := range byLine {
		sort.Slice(bucket, func(a, b int) bool { return bucket[a].Start < bucket[b].Start })
	}
	return byLine
}
