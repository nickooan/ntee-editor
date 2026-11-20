package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/nickooan/ntee-editor/internal/extmsg"
	"github.com/nickooan/ntee-editor/internal/staging"
	"github.com/nickooan/ntee-editor/internal/store"
)

// stackedSessions stages A's edit to main.go and B's on top of it, then
// ends B under policy.
func stackedSessions(t *testing.T, policy string) (Model, string, *staging.Registry, chan string) {
	t.Helper()
	m, root, receiver, registry, settings := agentEditsModel(t, policy)
	if reply := receiver.Handle(replaceRequest("A", "main.go", extmsg.ReplacePayload{Old: "func main() {\n}", New: "func main() {\n\tprintln(1)\n}"})); !strings.HasPrefix(reply, "ok ") {
		t.Fatal(reply)
	}
	reply := receiver.Handle(replaceRequest("B", "main.go", extmsg.ReplacePayload{Old: "\tprintln(1)\n", New: "\tprintln(1)\n\tprintln(2)\n"}))
	if !strings.Contains(reply, "builds on edit session A's staged edits to it (not applied yet; to add to that change set instead, edit in session A)") {
		t.Fatalf("B hears it builds on A: %q", reply)
	}
	current, _ := settings.LoadExternalSettings()
	m, endReply := sendAgentEditControl(t, m, receiver, extmsg.CommandEnd, "B", current)
	return m, root, registry, endReply
}

func TestApplyingAStackedSessionAsksAndWritesBoth(t *testing.T) {
	m, root, registry, _ := stackedSessions(t, store.AgentEditPolicyReview)
	next, cmd := m.Update(keyPress(tea.KeyEnter)) // Review edits
	m = settleAgentEdits(next.(Model), cmd)
	if m.mode != modeReview || m.review.agentEdits != "B" {
		t.Fatal("B's staged review is open")
	}
	if notes := m.review.files[0].notes; len(notes) != 1 || !strings.Contains(notes[0].overlay.text, "builds on edit session A's staged edits") {
		t.Fatalf("the file's note says it builds on A: %+v", notes)
	}

	m = key(m, typeRune('a'))
	if m.choice.kind != choiceApplyAgentEdits || strings.Join(m.choice.choices, "|") != "Apply both|Cancel" {
		t.Fatalf("applying asks first: %+v", m.choice)
	}
	if plain := ansi.Strip(m.View().Content); !strings.Contains(plain, "main.go builds on edit session A's edits, which aren't applied yet") {
		t.Fatalf("the prompt says why:\n%s", plain)
	}
	next, cmd = m.Update(keyPress(tea.KeyEnter))
	m = settleAgentEdits(next.(Model), cmd)

	if data, _ := os.ReadFile(filepath.Join(root, "main.go")); string(data) != "package main\n\nfunc main() {\n\tprintln(1)\n\tprintln(2)\n}\n" {
		t.Fatalf("both sessions' edits are on disk: %q", data)
	}
	recordA, ok := registry.Snapshot("A")
	if !ok || recordA.Files[0].Result != store.AgentEditFileApplied || !strings.Contains(recordA.Files[0].ResultNote, "written with edit session B") {
		t.Fatalf("A stays pending with its main.go settled: %+v", recordA.Files)
	}
	if _, staged := m.agentEdits.stagedFiles["main.go"]; staged {
		t.Fatal("main.go has no unwritten staged edits left")
	}
}

func TestTheApplyPolicyLeavesAStackedSessionToTheUser(t *testing.T) {
	m, root, registry, endReply := stackedSessions(t, store.AgentEditPolicyApply)
	if reply := gotReply(t, endReply); !strings.Contains(reply, "builds on edit session A, still pending, so the user decides") {
		t.Fatalf("the agent hears why it was not applied: %q", reply)
	}
	if _, ok := registry.Snapshot("B"); !ok {
		t.Fatal("B is still pending")
	}
	if data, _ := os.ReadFile(filepath.Join(root, "main.go")); string(data) != "package main\n\nfunc main() {\n}\n" {
		t.Fatalf("nothing is written: %q", data)
	}
	m = settleAgentEdits(m, nil)
	if m.mode != modeReview || m.review.agentEdits != "B" || !strings.Contains(m.notice, "builds on A's pending edits — review it") {
		t.Fatalf("its review opens with a notice: mode=%v %q notice=%q", m.mode, m.review.agentEdits, m.notice)
	}
}

func TestStagedFilesAreMarkedInTheTreeAndOnOpen(t *testing.T) {
	m, _, receiver, _, _ := agentEditsModel(t, store.AgentEditPolicyReview)
	receiver.Handle(replaceRequest("A", "main.go", extmsg.ReplacePayload{Old: "func main() {\n}", New: "func main() {\n\tprintln(1)\n}"}))
	next, _ := m.Update(agentEditsChangedMsg{session: "A"})
	m = next.(Model)
	if plain := ansi.Strip(m.View().Content); !strings.Contains(plain, "✎ main.go") || strings.Contains(plain, "✎ lib") {
		t.Fatalf("the tree marks the staged file only:\n%s", plain)
	}
	m = m.openFileAt("main.go")
	if m.notice != "✎ main.go has staged edits (edit session A) — not on disk yet · Ctrl+T › agent edits" {
		t.Fatalf("opening it says so: %q", m.notice)
	}
	if m = m.openFileAt("lib/util.ts"); strings.Contains(m.notice, "staged edits") {
		t.Fatalf("an unstaged file gets no notice: %q", m.notice)
	}
}
