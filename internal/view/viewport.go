// Package view holds pure rendering helpers (viewport, search, highlight
// segments). No UI framework — these produce data the Bubble Tea View renders.
package view

import (
	"strings"

	"github.com/nickooan/ntee-editor/internal/input"
)

// Viewport is a width×height window over content at a clamped scroll offset.
type Viewport struct {
	Lines       []string
	MaxScrollX  int
	MaxScrollY  int
	SafeScrollX int
	SafeScrollY int
}

// NormalizeLines splits content into lines on "\n". CRLF counts as a line
// break too, so a stray \r from any source never reaches the terminal.
func NormalizeLines(content string) []string {
	return strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
}

func sliceLine(line string, scrollX, width int) string {
	runes := []rune(line)
	start := input.Clamp(scrollX, 0, len(runes))
	end := input.Clamp(start+width, 0, len(runes))
	visible := string(runes[start:end])
	if pad := width - (end - start); pad > 0 {
		visible += strings.Repeat(" ", pad)
	}
	return visible
}

// BuildTerminalViewport slices content to a width×height window at the given
// scroll offsets, padding short lines/columns.
func BuildTerminalViewport(content string, width, height, scrollX, scrollY int) Viewport {
	lines := NormalizeLines(content)

	maxLineWidth := 0
	for _, line := range lines {
		if n := len([]rune(line)); n > maxLineWidth {
			maxLineWidth = n
		}
	}

	maxScrollX := max(0, maxLineWidth-width)
	maxScrollY := max(0, len(lines)-height)
	safeScrollX := input.Clamp(scrollX, 0, maxScrollX)
	safeScrollY := input.Clamp(scrollY, 0, maxScrollY)

	end := min(safeScrollY+height, len(lines))
	visible := make([]string, 0, height)
	for _, line := range lines[safeScrollY:end] {
		visible = append(visible, sliceLine(line, safeScrollX, width))
	}
	for len(visible) < height {
		visible = append(visible, strings.Repeat(" ", width))
	}

	return Viewport{
		Lines:       visible,
		MaxScrollX:  maxScrollX,
		MaxScrollY:  maxScrollY,
		SafeScrollX: safeScrollX,
		SafeScrollY: safeScrollY,
	}
}
