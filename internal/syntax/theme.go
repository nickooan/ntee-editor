package syntax

import (
	"strings"
	"sync"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/styles"

	"github.com/nickooan/ntee-editor/internal/view"
)

// Grammar colors come from a chroma style (default: gruvbox dark, tuned to
// the classic Sublime/vim gruvbox pattern). Style.Get resolves
// category/subcategory inheritance, so every token gets a concrete hex color.

// styleMu guards activeStyle/entryCache: highlighting runs on tea.Cmd
// goroutines (grep and def-picker previews) while SetStyle can fire from the
// UI goroutine's inspect command.
var (
	styleMu     sync.Mutex
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

// resolveStyle maps a style name to its chroma style. Unknown names and the
// legacy "terminal16" fall back to gruvbox.
func resolveStyle(name string) *chroma.Style {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" || n == "terminal16" || n == "gruvbox" {
		return gruvboxTuned()
	}
	style := styles.Get(n)
	if style == nil || (style == styles.Fallback && n != "fallback") {
		return gruvboxTuned()
	}
	return style
}

// SetStyle selects the chroma style grammar colors are drawn from.
func SetStyle(name string) {
	style := resolveStyle(name)
	styleMu.Lock()
	activeStyle = style
	entryCache = map[chroma.TokenType]view.HighlightSegment{}
	styleMu.Unlock()
}

// segmentFor maps a token type to its styled segment (hex color + attrs).
// Text is filled in by the caller.
func segmentFor(t chroma.TokenType) view.HighlightSegment {
	styleMu.Lock()
	defer styleMu.Unlock()
	if seg, ok := entryCache[t]; ok {
		return seg
	}
	seg := segmentFromStyle(activeStyle, t)
	entryCache[t] = seg
	return seg
}

func segmentFromStyle(style *chroma.Style, t chroma.TokenType) view.HighlightSegment {
	entry := style.Get(t)
	seg := view.HighlightSegment{
		Bold:      entry.Bold == chroma.Yes,
		Italic:    entry.Italic == chroma.Yes,
		Underline: entry.Underline == chroma.Yes,
	}
	if entry.Colour.IsSet() {
		seg.Color = entry.Colour.String() // "#rrggbb"
	}
	return seg
}
