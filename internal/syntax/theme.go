package syntax

import (
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/styles"

	"github.com/nickooan/ntee-editor/internal/view"
)

// Grammar colors come from a chroma style (default: gruvbox dark, tuned to
// the classic Sublime/vim gruvbox pattern). Style.Get resolves
// category/subcategory inheritance, so every token gets a concrete hex color.

var (
	activeStyle *chroma.Style
	entryCache  map[chroma.TokenType]view.HighlightSegment
)

func init() { SetStyle("gruvbox") }

// gruvboxTuned derives chroma's gruvbox with red keywords/operators — chroma
// colors them orange, but the classic gruvbox pattern (vim, Sublime packages)
// uses red, keeping types/functions gold.
func gruvboxTuned() *chroma.Style {
	builder := styles.Get("gruvbox").Builder()
	builder.Add(chroma.Keyword, "noinherit #fb4934")
	builder.Add(chroma.KeywordType, "noinherit #fabd2f")
	builder.Add(chroma.Operator, "noinherit #fb4934")
	style, err := builder.Build()
	if err != nil {
		return styles.Get("gruvbox")
	}
	return style
}

// SetStyle selects the chroma style grammar colors are drawn from. Unknown
// names and the legacy "terminal16" fall back to gruvbox. Called once at
// startup; the TUI render loop is single-goroutine, so no locking.
func SetStyle(name string) {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" || n == "terminal16" {
		n = "gruvbox"
	}
	var style *chroma.Style
	if n == "gruvbox" {
		style = gruvboxTuned()
	} else {
		style = styles.Get(n)
		if style == nil || (style == styles.Fallback && n != "fallback") {
			style = gruvboxTuned()
		}
	}
	activeStyle = style
	entryCache = map[chroma.TokenType]view.HighlightSegment{}
}

// segmentFor maps a token type to its styled segment (hex color + attrs).
// Text is filled in by the caller.
func segmentFor(t chroma.TokenType) view.HighlightSegment {
	if seg, ok := entryCache[t]; ok {
		return seg
	}
	entry := activeStyle.Get(t)
	seg := view.HighlightSegment{
		Bold:      entry.Bold == chroma.Yes,
		Italic:    entry.Italic == chroma.Yes,
		Underline: entry.Underline == chroma.Yes,
	}
	if entry.Colour.IsSet() {
		seg.Color = entry.Colour.String() // "#rrggbb"
	}
	entryCache[t] = seg
	return seg
}
