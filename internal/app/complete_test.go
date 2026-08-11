package app

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/nickooan/ntee-editor/internal/lsp"
)

func TestCompletionOpenFilterAccept(t *testing.T) {
	m, client := newLSPTestModel(t)
	m = m.openFileAt("main.go")
	client.completions = []lsp.CompletionItem{
		{Label: "Println", InsertText: "Println"},
		{Label: "Printf", InsertText: "Printf"},
		{Label: "Scan", InsertText: "Scan"},
	}
	// A single-line buffer with the cursor after a typed "Printl".
	m.edit = newEditor("Printl")
	m.edit.cy, m.edit.cx = 0, 6

	next, cmd := m.requestCompletion()
	m = next.(Model)
	if cmd == nil {
		t.Fatal("expected a completion request cmd")
	}
	msg, ok := cmd().(completionMsg)
	if !ok {
		t.Fatalf("expected completionMsg, got %T", cmd())
	}

	next, _ = m.handleCompletion(msg)
	m = next.(Model)
	if !m.completionOpen {
		t.Fatal("popup should open")
	}
	// Prefix "Printl" matches only Println.
	if len(m.completionItems) != 1 || m.completionItems[0].Label != "Println" {
		t.Fatalf("filtered = %+v", m.completionItems)
	}

	// Accept replaces the partial word with the insert text.
	m = m.acceptCompletion()
	if m.edit.content() != "Println" {
		t.Fatalf("accept result = %q", m.edit.content())
	}
	if m.completionOpen {
		t.Fatal("popup should close after accept")
	}
}

func TestCompletionStaleAnswerDropped(t *testing.T) {
	m, _ := newLSPTestModel(t)
	m = m.openFileAt("main.go")
	m.edit = newEditor("foo")
	m.edit.cy, m.edit.cx = 0, 3

	// A result whose tagged word start no longer matches the cursor context.
	msg := completionMsg{rel: m.openRel, line: 0, start: 99, items: []lsp.CompletionItem{{Label: "foobar"}}}
	next, _ := m.handleCompletion(msg)
	m = next.(Model)
	if m.completionOpen {
		t.Fatal("a stale completion answer must be dropped")
	}
}

func TestCompletionAnswerForOtherFileDropped(t *testing.T) {
	m, _ := newLSPTestModel(t)
	m = m.openFileAt("main.go")
	m.edit = newEditor("foo")
	m.edit.cy, m.edit.cx = 0, 3

	// Same line and word start as the current cursor, but the request was
	// fired from a file the user has since left — it must not open the popup.
	msg := completionMsg{rel: "lib/util.ts", line: 0, start: 0, items: []lsp.CompletionItem{{Label: "foobar"}}}
	next, _ := m.handleCompletion(msg)
	m = next.(Model)
	if m.completionOpen {
		t.Fatal("a completion answer for another file must be dropped")
	}
}

func TestCompletionEscDismissesAndSuppresses(t *testing.T) {
	m, _ := newLSPTestModel(t)
	m = m.openFileAt("main.go")
	m.completionOpen = true
	m.completionItems = []lsp.CompletionItem{{Label: "Println"}}

	nm, _, done := m.completionKey(keyPress(tea.KeyEsc))
	if !done || nm.completionOpen || !nm.completionDismissed {
		t.Fatalf("Esc should dismiss and suppress: open=%v dismissed=%v done=%v", nm.completionOpen, nm.completionDismissed, done)
	}
	// While dismissed, typing an identifier char should not re-request.
	nm.edit = newEditor("a")
	nm.edit.cx = 1
	_, cmd := nm.afterEditType("a")
	if cmd != nil {
		t.Fatal("dismissed popup must not auto-reopen mid-word")
	}
}

// A word boundary (Space, Enter, arrows — any non-typing key) must lift an Esc
// dismissal so the popup auto-opens again at the next word. Regression: the
// flag used to survive Space/Enter, leaving completion dead on new lines until
// a punctuation rune happened to be typed.
func TestCompletionDismissalLiftsAtWordBoundary(t *testing.T) {
	for _, boundary := range []tea.KeyPressMsg{keyPress(tea.KeySpace), keyPress(tea.KeyEnter), keyPress(tea.KeyLeft)} {
		m, _ := newLSPTestModel(t)
		m = m.openFileAt("main.go")
		m.completionOpen = true
		m.completionItems = []lsp.CompletionItem{{Label: "Println"}}
		m, _, _ = m.completionKey(keyPress(tea.KeyEsc))
		if !m.completionDismissed {
			t.Fatal("Esc must set the dismissal flag")
		}

		next, _ := m.handleEditKey(boundary)
		m = next.(Model)
		if m.completionDismissed {
			t.Fatalf("%v must lift the Esc dismissal (word boundary)", boundary)
		}
	}
}

// Typing more of the same word keeps the dismissal (that is the point of Esc).
func TestCompletionDismissalPersistsMidWord(t *testing.T) {
	m, _ := newLSPTestModel(t)
	m = m.openFileAt("main.go")
	m.completionOpen = true
	m.completionItems = []lsp.CompletionItem{{Label: "Println"}}
	m, _, _ = m.completionKey(keyPress(tea.KeyEsc))

	next, _ := m.handleEditKey(typeRune('x'))
	m = next.(Model)
	if !m.completionDismissed {
		t.Fatal("typing an identifier rune must keep the dismissal")
	}
}

func TestCompletionViewSmoke(t *testing.T) {
	m, _ := newLSPTestModel(t)
	m = m.openFileAt("main.go")
	m.completionOpen = true
	m.completionItems = []lsp.CompletionItem{{Label: "Println"}, {Label: "Printf"}}
	m.completionIndex = 1
	if out := m.render(); out == "" {
		t.Fatal("View with completion open should render")
	}
}

func TestCompletionLayout(t *testing.T) {
	sig := func(label, det, origin string) lsp.CompletionItem {
		return lsp.CompletionItem{Label: label, LabelDetails: &lsp.CompletionLabelDetails{Detail: det, Description: origin}}
	}
	cases := []struct {
		name  string
		items []lsp.CompletionItem
		cap   int
		check func(t *testing.T, labelW, detW, originW, total int)
	}{
		{"label only keeps the 12-cell floor", []lsp.CompletionItem{{Label: "Scan"}}, 80,
			func(t *testing.T, labelW, detW, originW, total int) {
				if total != 12 || detW != 0 || originW != 0 {
					t.Fatalf("labelW=%d detW=%d originW=%d total=%d", labelW, detW, originW, total)
				}
			}},
		{"all columns fit", []lsp.CompletionItem{sig("Append", "(s []T, e ...T) []T", "slices")}, 80,
			func(t *testing.T, labelW, detW, originW, total int) {
				if labelW != 6 || detW != 19 || originW != 6 || total != 2+6+1+19+2+6 {
					t.Fatalf("labelW=%d detW=%d originW=%d total=%d", labelW, detW, originW, total)
				}
			}},
		{"tight cap truncates detail first", []lsp.CompletionItem{sig("Append", "(s []T, e ...T) []T", "slices")}, 30,
			func(t *testing.T, labelW, detW, originW, total int) {
				if labelW != 6 || originW != 6 || total != 30 || detW != 13 {
					t.Fatalf("labelW=%d detW=%d originW=%d total=%d", labelW, detW, originW, total)
				}
			}},
		{"tighter cap drops origin whole", []lsp.CompletionItem{sig("Append", "(s []T, e ...T) []T", "slices")}, 14,
			func(t *testing.T, labelW, detW, originW, total int) {
				// The 12-cell floor pads the label back out after the columns drop.
				if originW != 0 || detW != 0 || total != 12 || labelW != 10 {
					t.Fatalf("labelW=%d detW=%d originW=%d total=%d", labelW, detW, originW, total)
				}
			}},
		{"tiny cap truncates the label last", []lsp.CompletionItem{sig("ridiculouslyLongName", "", "")}, 10,
			func(t *testing.T, labelW, detW, originW, total int) {
				if total != 10 || labelW != 8 {
					t.Fatalf("labelW=%d detW=%d originW=%d total=%d", labelW, detW, originW, total)
				}
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			labelW, detW, originW, total := completionLayout(tc.items, tc.cap)
			tc.check(t, labelW, detW, originW, total)
		})
	}
}

func TestCompletionPopupShowsDetailAndOrigin(t *testing.T) {
	m, _ := newLSPTestModel(t)
	m = m.openFileAt("main.go")
	m.completionOpen = true
	m.completionItems = []lsp.CompletionItem{
		{Label: "Append", Detail: "func(s []T, e ...T) []T",
			LabelDetails: &lsp.CompletionLabelDetails{Detail: "(s []T, e ...T) []T", Description: "slices"}},
		{Label: "Add", Detail: "func(a, b int) int"}, // legacy Detail fallback, no origin
	}
	out := ansi.Strip(m.render())
	for _, want := range []string{"Append", "(s []T, e ...T) []T", "slices", "Add", "func(a, b int) int"} {
		if !strings.Contains(out, want) {
			t.Fatalf("popup missing %q", want)
		}
	}
}

// pinTestModel opens main.go in edit mode with a one-line buffer "append" and
// an open popup whose only candidate is append's signature.
func pinTestModel(t *testing.T) Model {
	t.Helper()
	m, _ := newLSPTestModel(t)
	m = m.openFileAt("main.go")
	m.edit = newEditor("append")
	m.edit.cy, m.edit.cx = 0, 6
	m.completionOpen = true
	m.completionAll = []lsp.CompletionItem{{
		Label: "append", Detail: "func(slice []T, elems ...T) []T",
		LabelDetails: &lsp.CompletionLabelDetails{Description: "builtin"},
	}}
	m.completionItems = m.completionAll
	return m
}

func TestSigPinOnParenFromPopup(t *testing.T) {
	m := pinTestModel(t)
	m = key(m, typeRune('('))
	if m.sigPinned == nil || m.sigPinned.Label != "append" {
		t.Fatalf("pin missing: %+v", m.sigPinned)
	}
	if m.sigDepth != 1 || m.sigLine != 0 || m.sigCol != 6 || m.sigAnchor != 0 {
		t.Fatalf("depth=%d line=%d col=%d anchor=%d", m.sigDepth, m.sigLine, m.sigCol, m.sigAnchor)
	}
	if m.completionOpen {
		t.Fatal("the popup itself closes on (")
	}

	// Arguments and commas keep the pin; the closing paren drops it.
	m = runes(m, "x, y")
	if m.sigPinned == nil {
		t.Fatal("typing arguments must keep the pin")
	}
	m = key(m, typeRune(')'))
	if m.sigPinned != nil {
		t.Fatal("the matching ) must unpin")
	}
}

func TestSigPinAfterAccept(t *testing.T) {
	m := pinTestModel(t)
	m.edit = newEditor("appe")
	m.edit.cy, m.edit.cx = 0, 4
	m = m.acceptCompletion() // closes the popup, remembers the item
	if m.completionOpen || m.sigLastAccepted == nil {
		t.Fatalf("accept: open=%v last=%v", m.completionOpen, m.sigLastAccepted)
	}
	m = key(m, typeRune('('))
	if m.sigPinned == nil || m.sigPinned.Label != "append" {
		t.Fatalf("pin after accept missing: %+v", m.sigPinned)
	}
}

func TestSigNoPinWithoutMatch(t *testing.T) {
	m := pinTestModel(t)
	m.edit = newEditor("if ")
	m.edit.cy, m.edit.cx = 0, 3
	m = key(m, typeRune('('))
	if m.sigPinned != nil {
		t.Fatalf("bare ( must not pin: %+v", m.sigPinned)
	}

	// A word that matches no candidate doesn't pin either.
	m = pinTestModel(t)
	m.edit = newEditor("frobnicate")
	m.edit.cy, m.edit.cx = 0, 10
	m = key(m, typeRune('('))
	if m.sigPinned != nil {
		t.Fatalf("unmatched word must not pin: %+v", m.sigPinned)
	}
}

func TestSigNestedParens(t *testing.T) {
	m := pinTestModel(t)
	m = key(m, typeRune('('))
	m = runes(m, "len")
	m = key(m, typeRune('(')) // nested call deepens the same pin
	if m.sigPinned == nil || m.sigDepth != 2 {
		t.Fatalf("nested (: pinned=%v depth=%d", m.sigPinned != nil, m.sigDepth)
	}
	m = key(m, typeRune(')'))
	if m.sigPinned == nil || m.sigDepth != 1 {
		t.Fatalf("inner ): pinned=%v depth=%d", m.sigPinned != nil, m.sigDepth)
	}
	m = key(m, typeRune(')'))
	if m.sigPinned != nil {
		t.Fatal("outer ) must unpin")
	}
}

func TestSigBackspaceAdjustsDepth(t *testing.T) {
	// Backspacing a ")" re-opens the call: depth goes back up.
	m := pinTestModel(t)
	m = key(m, typeRune('('))
	m = runes(m, "x")
	m = key(m, typeRune('(')) // depth 2
	m = key(m, typeRune(')')) // depth 1
	m = key(m, keyPress(tea.KeyBackspace))
	if m.sigPinned == nil || m.sigDepth != 2 {
		t.Fatalf("deleting ) must re-increment: pinned=%v depth=%d", m.sigPinned != nil, m.sigDepth)
	}
	m = key(m, keyPress(tea.KeyBackspace)) // deletes the inner ( → depth 1
	if m.sigPinned == nil || m.sigDepth != 1 {
		t.Fatalf("deleting ( must decrement: pinned=%v depth=%d", m.sigPinned != nil, m.sigDepth)
	}
	m = key(m, keyPress(tea.KeyBackspace)) // deletes "x"
	m = key(m, keyPress(tea.KeyBackspace)) // deletes the pinned ( → unpin
	if m.sigPinned != nil {
		t.Fatal("deleting the opening ( must unpin")
	}

	// A selection delete gives up on tracking.
	m = pinTestModel(t)
	m = key(m, typeRune('('))
	m.edit.sel = &selRange{start: 0, end: 7}
	m = key(m, keyPress(tea.KeyBackspace))
	if m.sigPinned != nil {
		t.Fatal("a selection delete must unpin")
	}
}

func TestSigEscAndEnterUnpin(t *testing.T) {
	m := pinTestModel(t)
	m = key(m, typeRune('('))
	m = key(m, keyPress(tea.KeyEsc))
	if m.sigPinned != nil {
		t.Fatal("Esc must unpin")
	}
	if m.mode != modeEdit {
		t.Fatal("the unpinning Esc must be consumed, not leave edit mode")
	}

	m = pinTestModel(t)
	m = key(m, typeRune('('))
	m = key(m, keyPress(tea.KeyEnter))
	if m.sigPinned != nil {
		t.Fatal("Enter (line change) must unpin")
	}
}

func TestSigCursorLeavesCall(t *testing.T) {
	m := pinTestModel(t)
	m = key(m, typeRune('('))
	m = key(m, keyPress(tea.KeyLeft)) // back onto the ( → outside the args
	if m.sigPinned != nil {
		t.Fatal("walking left past the ( must unpin")
	}

	m = pinTestModel(t)
	m.edit = newEditor("append\nx")
	m.edit.cy, m.edit.cx = 0, 6
	m = key(m, typeRune('('))
	m = key(m, keyPress(tea.KeyDown))
	if m.sigPinned != nil {
		t.Fatal("leaving the line must unpin")
	}
}

// The dismissPopup split: a non-text key while the popup is open closes only
// the popup — an active pin (inner-argument completion) survives.
func TestSigSurvivesPopupDismiss(t *testing.T) {
	m := pinTestModel(t)
	m = key(m, typeRune('(')) // pinned, popup closed
	// Re-open the popup as if completing an inner argument.
	m.completionOpen = true
	m.completionItems = m.completionAll
	m = key(m, keyPress(tea.KeyRight)) // non-text key: dismisses the popup
	if m.completionOpen {
		t.Fatal("non-text key must close the popup")
	}
	if m.sigPinned == nil {
		t.Fatal("the pin must survive a popup dismissal")
	}
}

func TestSigOverlayRenders(t *testing.T) {
	m := pinTestModel(t)
	m.edit = newEditor("append\nzzz")
	m.edit.cy, m.edit.cx = 0, 6
	m = key(m, typeRune('('))
	if m.sigPinned == nil {
		t.Fatal("setup: pin expected")
	}
	frame := m.render()
	out := ansi.Strip(frame)
	for _, want := range []string{"func(slice []T, elems ...T) []T", "builtin"} {
		if !strings.Contains(out, want) {
			t.Fatalf("pinned overlay missing %q", want)
		}
	}
	// The signature sits on the row BELOW the call line, like the popup.
	lines := strings.Split(out, "\n")
	callRow, sigRow := -1, -1
	for i, l := range lines {
		if strings.Contains(l, "append(") {
			callRow = i
		}
		if strings.Contains(l, "func(slice") {
			sigRow = i
		}
	}
	if callRow < 0 || sigRow != callRow+1 {
		t.Fatalf("signature row %d should sit right below call row %d", sigRow, callRow)
	}
	// And it keeps the popup's selected-row styling (aqua bar).
	it := *m.sigPinned
	labelW, detW, originW, _ := completionLayout([]lsp.CompletionItem{it}, 1000)
	if want := completionRow(it, labelW, detW, originW, true); !strings.Contains(frame, want) {
		t.Fatal("pinned row should render in the selected-row style")
	}
}
