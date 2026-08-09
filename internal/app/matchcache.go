package app

import (
	"regexp"

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
	}
	return c.matches
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
