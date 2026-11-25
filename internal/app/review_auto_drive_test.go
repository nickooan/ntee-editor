package app

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/nickooan/ntee-editor/internal/extmsg"
	"github.com/nickooan/ntee-editor/internal/store"
)

func numberedLines(count int) string {
	var lines strings.Builder
	for line := 1; line <= count; line++ {
		fmt.Fprintf(&lines, "line %d\n", line)
	}
	return lines.String()
}

// rowOf is the row of the selected review file showing text.
func rowOf(t *testing.T, m Model, text string) int {
	t.Helper()
	file, _ := m.selectedReviewFile()
	for row := range file.document.rows {
		if file.document.rowText(row) == text {
			return row
		}
	}
	t.Fatalf("no row %q", text)
	return -1
}

func cursorText(m Model) string {
	file, _ := m.selectedReviewFile()
	return file.document.rowText(m.review.position.row)
}

// liveAutoDriveReview is edit session e1's live review over a 40-line
// main.go, auto-drive on.
func liveAutoDriveReview(t *testing.T) (Model, *ExternalReceiver) {
	t.Helper()
	m, _, receiver, _, _ := agentEditsModel(t, store.AgentEditPolicyReview)
	m = stageLive(t, m, receiver, "write --edit-session=e1\nmain.go\n---\n"+numberedLines(40), true, store.AgentEditPolicyReview)
	if m.mode != modeReview || !m.reviewIsLive() || !m.review.follow {
		t.Fatalf("the live review opens with auto-drive on: mode=%v live=%v follow=%v", m.mode, m.reviewIsLive(), m.review.follow)
	}
	return m, receiver
}

func replaceLive(t *testing.T, m Model, receiver *ExternalReceiver, old, new string) Model {
	t.Helper()
	return stageLive(t, m, receiver, replaceRequest("e1", "main.go", extmsg.ReplacePayload{Old: old, New: new}), false, store.AgentEditPolicyReview)
}

func TestAutoDriveMovesTheCursorToEachEdit(t *testing.T) {
	m, receiver := liveAutoDriveReview(t)
	m = replaceLive(t, m, receiver, "line 30\n", "line 30 changed\n")
	if want := rowOf(t, m, "line 30 changed"); m.review.position.row != want {
		t.Fatalf("the cursor follows the agent onto its edit: row %d (%q), want %d", m.review.position.row, cursorText(m), want)
	}
	m = replaceLive(t, m, receiver, "line 8\n", "line 8 changed\n")
	if cursorText(m) != "line 8 changed" {
		t.Fatalf("and onto the next one: %q", cursorText(m))
	}
	if plain := ansi.Strip(m.View().Content); !strings.Contains(plain, "AUTO-DRIVE") || !strings.Contains(plain, "ON ●") || !strings.Contains(plain, "Ctrl+` pause") {
		t.Fatalf("the panel shows auto-drive on:\n%s", plain)
	}
}

func TestMovingPausesAutoDriveAndKeepsTheCursorOnItsLine(t *testing.T) {
	m, receiver := liveAutoDriveReview(t)
	m = replaceLive(t, m, receiver, "line 30\n", "line 30 changed\n")
	m = key(m, keyPress(tea.KeyUp))
	if m.review.follow {
		t.Fatal("a move hands the review to the user")
	}
	reading := cursorText(m)
	m = replaceLive(t, m, receiver, "line 5\n", "line 5a\nline 5b\n")
	if cursorText(m) != reading {
		t.Fatalf("an edit above leaves the cursor on the line being read: %q, want %q", cursorText(m), reading)
	}
	if plain := ansi.Strip(m.View().Content); !strings.Contains(plain, "○ OFF") || !strings.Contains(plain, "Ctrl+` resume") {
		t.Fatalf("the panel shows auto-drive off:\n%s", plain)
	}

	m = key(m, ctrlKey('`'))
	if !m.review.follow || cursorText(m) != "line 5a" {
		t.Fatalf("the key resumes, on the agent's newest change: follow=%v %q", m.review.follow, cursorText(m))
	}
	if !m.review.motion.switching {
		t.Fatal("flipping the switch flashes it")
	}
	m = key(m, tea.KeyPressMsg{Code: tea.KeySpace, Mod: tea.ModCtrl})
	if m.review.follow {
		t.Fatal("ctrl+space — Ctrl+` in a legacy terminal — pauses it again")
	}
}

func TestAutoDriveKeyIsInertOutsideALiveReview(t *testing.T) {
	m, _ := gitReviewWorkspace(t)
	m = reviewOf(t, m, "apps/web/main.go")
	m = key(m, ctrlKey('`'))
	if m.review.follow || !strings.Contains(m.notice, "not live") {
		t.Fatalf("a plain review has nothing to follow: follow=%v %q", m.review.follow, m.notice)
	}
	if _, shown := m.reviewFloatPanel(); shown {
		t.Fatal("and no panel")
	}
}

func TestAReviewSessionFloatsItsAutoDriveSwitch(t *testing.T) {
	m, _, _ := startedReviewSession(t)
	panel, ok := m.reviewFloatPanel()
	if !ok || !strings.Contains(panel.title, "review session r1") {
		t.Fatalf("a live review session floats the switch: %+v", panel)
	}
	if plain := ansi.Strip(m.View().Content); !strings.Contains(plain, "AUTO-DRIVE") {
		t.Fatalf("drawn over the code:\n%s", plain)
	}
}

func TestRowMotionCarriesACursorPastAddedAndRemovedRows(t *testing.T) {
	base := "a\nb\nc\nd\n"
	motion := compareReviewDocuments(stagedDocument(base, base), stagedDocument(base, "x\na\nc\nd\n"))
	// new rows: +x · a · -b · c · d — b is still on screen, now removed
	if motion.carried(0) != 1 || motion.carried(1) != 2 || motion.carried(2) != 3 {
		t.Fatalf("each row keeps its line: %v", motion.previousToNext)
	}
	if row, ok := motion.firstChangedRow(); !ok || row != 0 {
		t.Fatalf("the change starts at the inserted x: %d %v", row, ok)
	}
	// rows a · b · +X · c · d → a · b · c · d: X is gone
	motion = compareReviewDocuments(stagedDocument(base, "a\nb\nX\nc\nd\n"), stagedDocument(base, base))
	if motion.carried(2) != 2 || stagedDocument(base, base).rowText(2) != "c" {
		t.Fatalf("a cursor on a vanished row moves to the next kept row: %v", motion.previousToNext)
	}
}
