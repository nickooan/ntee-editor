// Package fuzzy implements a rune-based, case-insensitive subsequence matcher
// for file paths, in the spirit of Sublime's Goto Anything.
package fuzzy

import (
	"sort"
	"strings"
	"unicode"
)

// Match is one candidate that contains the query as a subsequence.
type Match struct {
	Index     int   // index into the candidates slice
	Score     int   // higher is better
	Positions []int // matched rune indices in the candidate (for bold rendering)
}

// Filter returns the candidates matching query as a case-insensitive rune
// subsequence, best score first. An empty query matches everything with score 0
// in the original order.
func Filter(query string, candidates []string) []Match {
	q := []rune(strings.ToLower(query))
	out := make([]Match, 0, len(candidates))
	for i, cand := range candidates {
		if len(q) == 0 {
			out = append(out, Match{Index: i})
			continue
		}
		if m, ok := matchOne(q, cand); ok {
			m.Index = i
			out = append(out, m)
		}
	}
	if len(q) > 0 {
		sort.SliceStable(out, func(a, b int) bool {
			if out[a].Score != out[b].Score {
				return out[a].Score > out[b].Score
			}
			return len(candidates[out[a].Index]) < len(candidates[out[b].Index])
		})
	}
	return out
}

// maxStarts caps how many alternative alignments matchOne tries per candidate.
const maxStarts = 16

// matchOne matches q against candidate, scoring boundary hits and consecutive
// runs and penalizing gaps. Pure greedy forward matching picks bad alignments
// ("tree" scattering across "in_t_e_rnal" instead of hitting "keys_tree"), so
// it greedily matches from each occurrence of the first query rune (capped)
// and keeps the best-scoring alignment.
func matchOne(q []rune, candidate string) (Match, bool) {
	cand := []rune(candidate)
	lower := []rune(strings.ToLower(candidate))
	baseStart := strings.LastIndexByte(candidate, '/') + 1 // rune-safe: '/' is ASCII

	best := Match{Score: -1 << 30}
	found := false
	starts := 0
	for si := 0; si < len(lower) && starts < maxStarts; si++ {
		if lower[si] != q[0] {
			continue
		}
		starts++
		m, ok := greedyFrom(q, cand, lower, si)
		if !ok {
			break // no full match from here means none from any later start
		}
		if byteIndexOfRune(candidate, m.Positions[0]) >= baseStart {
			m.Score += 4 // prefer matches concentrated in the basename
		}
		if m.Score > best.Score {
			best = m
		}
		found = true
	}
	if !found {
		return Match{}, false
	}
	// Shorter candidates rank higher on ties via the sort; also nudge directly.
	best.Score -= len(cand) / 8
	return best, true
}

func greedyFrom(q, cand, lower []rune, start int) (Match, bool) {
	positions := make([]int, 0, len(q))
	score := 0
	qi := 0
	lastHit := -2
	for ci := start; ci < len(lower) && qi < len(q); ci++ {
		if lower[ci] != q[qi] {
			continue
		}
		hit := 2
		if isBoundary(cand, ci) {
			hit += 3
		}
		if ci == lastHit+1 {
			hit += 2
		}
		// Gap penalty: distance from the previous hit, capped so one long gap
		// does not drown out boundary/run bonuses.
		if lastHit >= 0 {
			gap := ci - lastHit - 1
			if gap > 3 {
				gap = 3
			}
			hit -= gap
		}
		score += hit
		positions = append(positions, ci)
		lastHit = ci
		qi++
	}
	if qi < len(q) {
		return Match{}, false
	}
	return Match{Score: score, Positions: positions}, true
}

// isBoundary reports whether the rune at i starts a "word": the string start,
// after a separator, or an upper-case rune following a lower-case one.
func isBoundary(runes []rune, i int) bool {
	if i == 0 {
		return true
	}
	prev := runes[i-1]
	switch prev {
	case '/', '_', '-', '.', ' ':
		return true
	}
	return unicode.IsUpper(runes[i]) && unicode.IsLower(prev)
}

// byteIndexOfRune converts a rune index to its byte offset in s.
func byteIndexOfRune(s string, runeIdx int) int {
	count := 0
	for bi := range s {
		if count == runeIdx {
			return bi
		}
		count++
	}
	return len(s)
}
