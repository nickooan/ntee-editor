package app

import (
	"regexp"
	"strings"

	"github.com/nickooan/ntee-editor/internal/view"
)

// matchCache memoizes view.FindSearchMatches per (content, query). Search
// mode re-derives the match list in the status line, the body renderer, and
// every navigation key — 3-4 full-content regex scans per keystroke without
// this. The Model is copied by value, so the cache rides behind a pointer
// field and every copy shares it; Update and View both run on the program
// goroutine, so no locking is needed. The content key is a frozen snapshot
// (see freezeSearchSnapshot), which keeps the string compare on Go's
// data-pointer fast path.
type matchCache struct {
	content, query string
	ok             bool
	matches        []view.SearchMatch

	// Derived render structures under the same key: the per-line buckets (keyed
	// like matches) and the content split into lines (content-only key). Both
	// were rebuilt from scratch on every frame — an O(file) split plus a map of
	// bucket slices per render on a large buffer.
	byLineOK bool
	byLine   map[int][]view.LineMatch
	linesFor string
	linesOK  bool
	lines    []string
}

// get returns the matches for (content, query), recomputing only when either
// key changed since the last call. Callers must not mutate the result.
func (c *matchCache) get(content, query string) []view.SearchMatch {
	if c == nil {
		return view.FindSearchMatches(content, query)
	}
	if !c.ok || content != c.content || query != c.query {
		c.content, c.query, c.ok = content, query, true
		c.matches = view.FindSearchMatches(content, query)
		c.byLineOK = false
	}
	return c.matches
}

// matchesByLine returns the per-line buckets for (content, query), rebuilt at
// most once per match recompute. Callers must not mutate the result.
func (c *matchCache) matchesByLine(content, query string) map[int][]view.LineMatch {
	if c == nil {
		return view.BuildMatchesByLine(view.FindSearchMatches(content, query))
	}
	matches := c.get(content, query)
	if !c.byLineOK {
		c.byLine = view.BuildMatchesByLine(matches)
		c.byLineOK = true
	}
	return c.byLine
}

// splitLines returns content split on "\n", re-splitting only when the content
// changed. Callers must not mutate the result.
func (c *matchCache) splitLines(content string) []string {
	if c == nil {
		return strings.Split(content, "\n")
	}
	if !c.linesOK || content != c.linesFor {
		c.linesFor, c.linesOK = content, true
		c.lines = strings.Split(content, "\n")
	}
	return c.lines
}

// searchMatches is the memoized match list for in-file search mode.
func (m Model) searchMatches() []view.SearchMatch {
	return m.searchMC.get(m.searchContent, m.searchInput)
}

// regexCache memoizes one compiled search regex per query, for render paths
// that would otherwise recompile every frame (the grep overlay's preview).
// Same sharing/locking story as matchCache.
type regexCache struct {
	query string
	ok    bool
	re    *regexp.Regexp
}

// multiline returns view.CreateMultilineSearchRegex(query), recompiling only
// when the query changed since the last call.
func (c *regexCache) multiline(query string) *regexp.Regexp {
	if c == nil {
		return view.CreateMultilineSearchRegex(query)
	}
	if !c.ok || query != c.query {
		c.query, c.ok = query, true
		c.re = view.CreateMultilineSearchRegex(query)
	}
	return c.re
}
