package app

import (
	"image/color"
	"reflect"
	"strings"
	"sync"

	"charm.land/lipgloss/v2"
)

// styleRunKey fingerprints a plain text style — colours and text attributes,
// no box (width, padding, border, …): for such a style, all lipgloss writes
// around a single line of text is a fixed opening and closing sequence.
type styleRunKey struct {
	foreground, background color.Color
	bold, italic, faint    bool
	reverse, blink         bool
}

// styleRunEnds caches runEnds per styleRunKey. Rows render on the UI
// goroutine, but previews and review documents render on tea.Cmd goroutines
// too, so the cache is a sync.Map.
var styleRunEnds sync.Map

// renderStyledRun is style.Render(text) for a single-line text, through the
// style's cached escape ends when it is a plain text style: the gutters and
// end-of-line fills of every visible row would otherwise each go through
// lipgloss's full measure / wrap / align / border pipeline every frame.
func renderStyledRun(style lipgloss.Style, text string) string {
	key, plain := styleRunKeyOf(style)
	if !plain || strings.ContainsAny(text, "\t\n\r") {
		return style.Render(text)
	}
	cached, known := styleRunEnds.Load(key)
	if !known {
		cached, _ = styleRunEnds.LoadOrStore(key, runEndsOfStyle(style))
	}
	ends := cached.(runEnds)
	if !ends.ok {
		return style.Render(text)
	}
	return ends.open + text + ends.close
}

func styleRunKeyOf(style lipgloss.Style) (styleRunKey, bool) {
	if style.GetUnderline() || style.GetStrikethrough() || style.GetUnderlineSpaces() || style.GetStrikethroughSpaces() ||
		style.GetWidth() > 0 || style.GetHeight() > 0 || style.GetMaxWidth() > 0 || style.GetMaxHeight() > 0 ||
		style.GetHorizontalFrameSize() > 0 || style.GetVerticalFrameSize() > 0 || style.GetTransform() != nil {
		return styleRunKey{}, false
	}
	if link, _ := style.GetHyperlink(); link != "" {
		return styleRunKey{}, false
	}
	foreground, background := style.GetForeground(), style.GetBackground()
	if !comparableColor(foreground) || !comparableColor(background) {
		return styleRunKey{}, false
	}
	return styleRunKey{
		foreground: foreground, background: background,
		bold: style.GetBold(), italic: style.GetItalic(), faint: style.GetFaint(),
		reverse: style.GetReverse(), blink: style.GetBlink(),
	}, true
}

// comparableColor guards the map key: a colour type that is not comparable
// would panic as part of one.
func comparableColor(value color.Color) bool {
	return value == nil || reflect.TypeOf(value).Comparable()
}

func runEndsOfStyle(style lipgloss.Style) runEnds {
	const sentinel = "X"
	rendered := style.Render(sentinel)
	if strings.Count(rendered, sentinel) != 1 {
		return runEnds{}
	}
	open, close, _ := strings.Cut(rendered, sentinel)
	return runEnds{open: open, close: close, ok: true}
}
