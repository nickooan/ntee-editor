// Package fuzzy implements a rune-based, case-insensitive subsequence matcher
// for file paths, in the spirit of Sublime's Goto Anything.
package fuzzy

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Match is one candidate that contains the query as a subsequence. Matched rune
// positions (for bold rendering) are computed separately via Positions, so
// Filter can score the whole corpus without allocating a slice per match.
type Match struct {
	Index int // index into the candidates slice
	Score int // higher is better
}

// Prepared is a candidate ready for repeated matching. It intentionally holds no
// decoded rune data: matching ranges over Text and case-folds each rune on the
// fly, so a prepared corpus adds only a string header plus an int per candidate
// (Text shares its bytes with the caller's slice). That keeps a finder open over
// a large workspace at a few MB rather than tens of MB of []rune copies.
type Prepared struct {
	Text      string // the path — matched, rendered, and selected from directly
	baseStart int    // rune index where the basename begins (after the last '/')
}

// Prepare wraps candidates for matching, preserving their order so a Match.Index
// still indexes into the original slice. Call it once when the finder opens.
func Prepare(candidates []string) []Prepared {
	out := make([]Prepared, len(candidates))
	for i, c := range candidates {
		// Directory candidates carry a trailing "/"; anchor their basename on
		// the last real segment so the basename bonuses can still apply.
		base, ci := 0, 0
		for _, r := range strings.TrimSuffix(c, "/") {
			if r == '/' {
				base = ci + 1
			}
			ci++
		}
		out[i] = Prepared{Text: c, baseStart: base}
	}
	return out
}

// Filter returns the candidates matching query as a case-insensitive rune
// subsequence, best score first. An empty query matches everything with score 0
// in the original order.
func Filter(query string, candidates []Prepared) []Match {
	q := []rune(strings.ToLower(query))
	out := make([]Match, 0, len(candidates))
	if len(q) == 0 {
		for i := range candidates {
			out = append(out, Match{Index: i})
		}
		return out
	}
	for i := range candidates {
		c := &candidates[i]
		// Cheap, allocation-free reject: most candidates don't contain the query
		// at all, and skipping the multi-start scorer for them is the win on a
		// large corpus.
		if !isSubsequence(q, c.Text) {
			continue
		}
		if score, _, ok := align(q, c.Text, c.baseStart, false); ok {
			out = append(out, Match{Index: i, Score: score})
		}
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Score != out[b].Score {
			return out[a].Score > out[b].Score
		}
		return len(candidates[out[a].Index].Text) < len(candidates[out[b].Index].Text)
	})
	return out
}

// Positions returns the matched rune indices of the best alignment of query in
// cand, for bold rendering. It recomputes the alignment Filter scored, so it is
// meant to be called only for the handful of visible rows — not the whole
// result set. Returns nil when there is no match or the query is empty.
func Positions(query string, cand Prepared) []int {
	q := []rune(strings.ToLower(query))
	if len(q) == 0 {
		return nil
	}
	_, pos, ok := align(q, cand.Text, cand.baseStart, true)
	if !ok {
		return nil
	}
	return pos
}

// foldRune lower-cases r, with a fast path for ASCII (the overwhelming majority
// of path bytes) so per-rune folding stays cheap in the hot loop.
func foldRune(r rune) rune {
	if r < utf8.RuneSelf {
		if 'A' <= r && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}
	return unicode.ToLower(r)
}

// isSubsequence reports whether q appears in text in order (case-folded). This
// is the fast pre-filter: one linear pass, no allocation, no scoring.
func isSubsequence(q []rune, text string) bool {
	qi := 0
	for _, r := range text {
		if foldRune(r) == q[qi] {
			qi++
			if qi == len(q) {
				return true
			}
		}
	}
	return false
}

// maxStarts caps how many alternative alignments align tries per candidate.
const maxStarts = 16

// minScore seeds the best-score search below any achievable alignment score.
const minScore = -1 << 30

// align finds the highest-scoring alignment of q in text. Pure greedy forward
// matching picks bad alignments ("tree" scattering across "in_t_e_rnal" instead
// of hitting "keys_tree"), so it greedily matches from each occurrence of the
// first query rune (capped) and keeps the best-scoring one. When wantPos is true
// it also returns that alignment's matched rune indices; when false it allocates
// nothing (the per-keystroke Filter path).
func align(q []rune, text string, baseStart int, wantPos bool) (score int, positions []int, ok bool) {
	best := minScore
	var scratch []int
	if wantPos {
		scratch = make([]int, 0, len(q))
	}
	starts := 0
	ci := 0
	prev := rune(-1) // rune before the current position; -1 at the string start
	for bi, r := range text {
		if starts >= maxStarts {
			break
		}
		if foldRune(r) == q[0] {
			starts++
			var out *[]int
			if wantPos {
				scratch = scratch[:0]
				out = &scratch
			}
			s, matched := scoreFrom(q, text, bi, ci, prev, out)
			if !matched {
				break // no full match from here means none from any later start
			}
			if ci >= baseStart {
				s += 4 // prefer matches concentrated in the basename
			}
			if s > best {
				best = s
				if wantPos {
					positions = append(positions[:0], scratch...)
				}
			}
			ok = true
		}
		prev = r
		ci++
	}
	if !ok {
		return 0, nil, false
	}
	// Shorter candidates rank higher on ties via the sort; also nudge directly.
	return best - len(text)/8, positions, true
}

// scoreFrom greedily matches q against text starting at the rune whose byte
// offset is startByte and rune index is startRune (prevRune is the rune just
// before it, for a boundary test on the first hit). It scores boundary hits and
// consecutive runs and penalizes gaps, appending matched rune indices to *out
// when non-nil. Returns ok=false if q is not fully consumed.
func scoreFrom(q []rune, text string, startByte, startRune int, prevRune rune, out *[]int) (score int, ok bool) {
	qi := 0
	lastHit := -2
	ci := startRune
	prev := prevRune
	for _, r := range text[startByte:] {
		if qi >= len(q) {
			break
		}
		if foldRune(r) == q[qi] {
			hit := 2
			if isBoundary(ci, prev, r) {
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
			if out != nil {
				*out = append(*out, ci)
			}
			lastHit = ci
			qi++
		}
		prev = r
		ci++
	}
	if qi < len(q) {
		return 0, false
	}
	return score, true
}

// isBoundary reports whether the rune cur at index i starts a "word": the string
// start, after a separator, or an upper-case rune following a lower-case one
// (prev is the preceding rune, in original case).
func isBoundary(i int, prev, cur rune) bool {
	if i == 0 {
		return true
	}
	switch prev {
	case '/', '_', '-', '.', ' ':
		return true
	}
	return unicode.IsUpper(cur) && unicode.IsLower(prev)
}
