package view

// HighlightSegment is one styled run of text within a line. Color is an
// ANSI-16 color name the renderer maps to a lipgloss style ("" = default).
type HighlightSegment struct {
	Text      string
	Color     string
	Bold      bool
	DimColor  bool
	Underline bool
	Italic    bool
	Strike    bool
}
