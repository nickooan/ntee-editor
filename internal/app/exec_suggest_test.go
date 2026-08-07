package app

import (
	"reflect"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestExecSuggestionsTable(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m.tabs = []string{"lib/util.ts", "main.go"}

	cases := []struct {
		input string
		want  []string
	}{
		{"", []string{"copy", "cp", "cpfp", "cpafp", "jump", "jp", "tab", "git", "openapi", "opapi"}},
		{"g", []string{"git"}},
		{"op", []string{"openapi", "opapi"}},
		{"opa", []string{"opapi"}},
		{"ju", []string{"jump"}}, // jp does not start with "ju"
		{"copy ", []string{"all", "fpath"}},
		{"cp f", []string{"fpath"}},
		{"jump ", []string{"top", "end"}},
		{"tab ", []string{"cl", "cr", "util.ts", "main.go"}},
		{"tab m", []string{"main.go"}},
		{"git ", []string{"scf", "diff", "blame"}},
		{"git d", []string{"diff"}},
		{"git b", []string{"blame"}},
		{"git diff ", nil},      // the revision arg stays free-form
		{"git scf ", nil},       // scf takes no argument
		{"git blame ", nil},     // blame takes no argument
		{"copy all extra", nil}, // past any known argument
		{"zz", nil},             // unknown prefix
	}
	for _, c := range cases {
		if got := m.execSuggestions(c.input); !reflect.DeepEqual(got, c.want) {
			t.Errorf("execSuggestions(%q) = %v, want %v", c.input, got, c.want)
		}
	}
}

// Tab chains token completions: g<Tab> → "git ", s<Tab> → "git scf ", then
// Enter opens the conflict-solving mode.
func TestExecTabCompletionChain(t *testing.T) {
	m := conflictFixture(t, 2)
	m = key(m, ctrlKey('e'))

	m = runes(m, "g")
	m = key(m, keyPress(tea.KeyTab))
	if m.execInput != "git " {
		t.Fatalf("after g<Tab>: %q", m.execInput)
	}
	m = runes(m, "s")
	m = key(m, keyPress(tea.KeyTab))
	if m.execInput != "git scf " {
		t.Fatalf("after s<Tab>: %q", m.execInput)
	}
	m = key(m, keyPress(tea.KeyEnter))
	if m.mode != modeConflict {
		t.Fatalf("git scf should enter conflict mode, got %v (err=%q)", m.mode, m.errText)
	}
}

func TestExecSuggestionCycle(t *testing.T) {
	m := execLineFixture(t, 3) // Ctrl+E is bound in edit mode
	m = key(m, ctrlKey('e'))
	if len(m.execSugs) != 10 {
		t.Fatalf("empty bar must offer all verbs, got %v", m.execSugs)
	}

	m = key(m, keyPress(tea.KeyDown)) // copy → cp
	m = key(m, keyPress(tea.KeyDown)) // cp → cpfp
	m = key(m, keyPress(tea.KeyDown)) // cpfp → cpafp
	m = key(m, keyPress(tea.KeyDown)) // cpafp → jump
	m = key(m, keyPress(tea.KeyTab))
	if m.execInput != "jump " {
		t.Fatalf("Tab after two Downs should accept jump, got %q", m.execInput)
	}

	m = key(m, keyPress(tea.KeyUp)) // wrap: top → end (last candidate)
	if m.execSugIndex != len(m.execSugs)-1 {
		t.Fatalf("Up from 0 must wrap to last, got %d of %v", m.execSugIndex, m.execSugs)
	}
}

func TestExecTabNoopWithoutSuggestions(t *testing.T) {
	m := execLineFixture(t, 3)
	m = key(m, ctrlKey('e'))
	m = runes(m, "zz")
	if len(m.execSugs) != 0 {
		t.Fatalf("unknown prefix must have no suggestions: %v", m.execSugs)
	}
	m = key(m, keyPress(tea.KeyTab))
	if m.execInput != "zz" {
		t.Fatalf("Tab with no suggestions must not change input: %q", m.execInput)
	}
}
