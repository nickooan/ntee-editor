package app

import (
	"testing"

	"github.com/nickooan/ntee-editor/internal/view"
)

func TestASegmentRunWritesWhatLipglossWould(t *testing.T) {
	texts := []string{"func", " ", "  two  spaces ", "\ttabbed\t", "ünïcødé ✓", ""}
	for _, bg := range []string{hexBg, "#203040"} {
		for _, flags := range []int{0, 1, 2, 3, 4, 5, 8, 16, 31} {
			segment := view.HighlightSegment{
				Color:     []string{"", "#ff8800"}[flags&1],
				Bold:      flags&2 != 0,
				Italic:    flags&4 != 0,
				Underline: flags&8 != 0,
				Strike:    flags&16 != 0,
			}
			for _, text := range texts {
				for _, tabWidth := range []int{2, 4, 8} {
					want := renderRun(segStyleWithBg(segment, bg), text, tabWidth)
					if got := renderSegmentRun(segment, bg, text, tabWidth); got != want {
						t.Fatalf("segment %+v bg %s text %q tab %d:\n got %q\nwant %q", segment, bg, text, tabWidth, got, want)
					}
				}
			}
		}
	}
	if ends := segRunEnds[segmentStyleKey(view.HighlightSegment{Color: "#ff8800", Bold: true}, hexBg)]; !ends.ok || ends.open == "" {
		t.Fatalf("a plain coloured style takes the cached ends: %+v", ends)
	}
}
