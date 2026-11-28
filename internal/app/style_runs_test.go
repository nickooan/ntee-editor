package app

import (
	"testing"

	"charm.land/lipgloss/v2"
)

func TestRenderStyledRunMatchesRender(t *testing.T) {
	styles := map[string]lipgloss.Style{
		"gutter":        gutterStyle,
		"diff gutter":   diffGutterAddStyle,
		"diff mark":     diffMarkDelStyle,
		"base":          baseStyle,
		"cursor line":   cursorLineStyle,
		"glow":          diffAddTextStyle.Background(lipgloss.Color(blendHex(hexDiffAddGlow, hexDiffAddBg, 0.5))),
		"bold italic":   lipgloss.NewStyle().Bold(true).Italic(true).Faint(true).Foreground(lipgloss.Color("#123456")),
		"reverse blink": lipgloss.NewStyle().Reverse(true).Blink(true),
		"basic color":   lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Background(lipgloss.Color("200")),
		"empty":         lipgloss.NewStyle(),
		// Not plain text styles: they keep going through Render.
		"underline": lipgloss.NewStyle().Underline(true).Foreground(colRed),
		"strike":    lipgloss.NewStyle().Strikethrough(true),
		"width":     lipgloss.NewStyle().Width(12).Background(colBg),
		"padding":   lipgloss.NewStyle().Padding(0, 1),
		"border":    paneStyle,
	}
	texts := []string{"", "x", "  12 │ ", "中文 wide", "a\tb", "two\nlines", "\x1b[31mred\x1b[m"}
	for name, style := range styles {
		for _, text := range texts {
			for range 2 { // the second call is served from the cache
				if got, want := renderStyledRun(style, text), style.Render(text); got != want {
					t.Fatalf("%s %q:\n got  %q\n want %q", name, text, got, want)
				}
			}
		}
	}
	// Same colours, different attributes: different cache entries.
	plain, bold := lipgloss.NewStyle().Foreground(colRed), lipgloss.NewStyle().Foreground(colRed).Bold(true)
	if renderStyledRun(plain, "x") == renderStyledRun(bold, "x") {
		t.Fatal("bold is part of the fingerprint")
	}
}
