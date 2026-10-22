package app

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestChoiceOverlayKeys(t *testing.T) {
	choice := choiceOverlay{kind: choiceRemovePath, title: "t", choices: []string{"Remove", "Cancel"}}
	if _, chosen, done := choice.handleKey("enter"); !done || chosen != 0 {
		t.Fatalf("Enter picks the highlighted (first) choice: %d %v", chosen, done)
	}
	next, _, done := choice.handleKey("right")
	if done || next.selected != 1 {
		t.Fatalf("→ moves the highlight: %+v", next)
	}
	if next, _, _ = next.handleKey("tab"); next.selected != 0 {
		t.Fatalf("Tab wraps around: %d", next.selected)
	}
	if _, chosen, done := choice.handleKey("esc"); !done || chosen != 1 {
		t.Fatal("Esc picks the last (safe) choice")
	}
	if _, chosen, done := choice.handleKey("y"); !done || chosen != 0 {
		t.Fatal("y picks the first choice")
	}
	if _, chosen, done := choice.handleKey("n"); !done || chosen != 1 {
		t.Fatal("n picks the last choice")
	}
	if next, _, done := choice.handleKey("z"); done || next.selected != 0 {
		t.Fatal("other keys are swallowed")
	}
}

func TestChoiceOverlayRendersBodyAndButtons(t *testing.T) {
	choice := choiceOverlay{
		kind:    choiceExternalRequest,
		title:   "External request · open",
		body:    []overlayLine{{text: "src/app.go#L10", style: overlayLineStrong}, {text: "switches repo: a/ → b/", style: overlayLineHighlight}},
		choices: []string{"Jump", "Ignore"},
	}
	plain := ansi.Strip(choice.render(80, 20))
	for _, want := range []string{"External request · open", "src/app.go#L10", "switches repo: a/ → b/", " Jump ", " Ignore ", "esc ignore"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("overlay missing %q:\n%s", want, plain)
		}
	}
}
