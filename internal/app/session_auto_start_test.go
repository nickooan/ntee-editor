package app

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/nickooan/ntee-editor/internal/extmsg"
	"github.com/nickooan/ntee-editor/internal/store"
)

func TestAReviewSessionStartsWithoutAskingWhenChosen(t *testing.T) {
	m, _, _ := reviewSessionWorkspace(t)
	reply := make(chan string, 1)
	m, cmd := deliver(m, externalMessageMsg{record: sessionRecord("review", "r1", "apps/web/main.go#L3", "# Why\nmain now runs"), sessionKind: extmsg.SessionReview, reply: reply, sessionAutoStart: true})
	m = settleExternal(m, cmd)
	if outcome := gotReply(t, reply); !strings.Contains(outcome, "file 1 queued — the review session started at once") {
		t.Fatalf("the agent hears the session started: %q", outcome)
	}
	if m.choice.open() || m.mode != modeReview || !m.reviewSessionRunning() || len(m.review.files) != 1 {
		t.Fatalf("the live review opens without a prompt: choice=%v mode=%v files=%d", m.choice.open(), m.mode, len(m.review.files))
	}
	if !strings.HasPrefix(m.notice, "▶ REVIEW · agent review session r1 started") {
		t.Fatalf("a short notice names the kind: %q", m.notice)
	}
	if _, outcome := sendReviewFile(t, m, "r1", "apps/web/new.go", ""); outcome != "file 2 added to the review" {
		t.Fatalf("later files join the running review: %q", outcome)
	}
}

func TestANoteSessionStartsWithoutAskingWhenChosen(t *testing.T) {
	m, _ := noteWorkspace(t)
	m, _, _ = externalModel(t, m, true)
	reply := make(chan string, 1)
	m, cmd := deliver(m, externalMessageMsg{record: sessionRecord("open", "walk1", "long.go#L40", "# Step one\nwhy"), sessionKind: extmsg.SessionNote, reply: reply, sessionAutoStart: true})
	m = settleExternal(m, cmd)
	if outcome := gotReply(t, reply); !strings.Contains(outcome, "step 1 queued — the session started at once") {
		t.Fatalf("reply = %q", outcome)
	}
	if m.choice.open() || !m.session.started || !m.sessionStepVisible() || m.openRel != "long.go" || m.edit.cy != 39 {
		t.Fatalf("step 1 shows without a prompt: session=%+v open=%q cy=%d", m.session, m.openRel, m.edit.cy)
	}
	if !strings.HasPrefix(m.notice, "▶ TOUR · agent note session walk1 started") {
		t.Fatalf("notice = %q", m.notice)
	}
}

// endAgentEdits stages a replace in e1 and delivers its end with the given
// auto-start choice.
func endAgentEdits(t *testing.T, m Model, receiver *ExternalReceiver, autoStart bool) Model {
	t.Helper()
	receiver.Handle(replaceRequest("e1", "main.go", extmsg.ReplacePayload{Old: "func main() {\n}", New: "func main() {\n\tprintln(1)\n}"}))
	if _, _, err := receiver.agentEdits.MarkAgentEnded("e1", ""); err != nil {
		t.Fatal(err)
	}
	m, cmd := deliver(m, externalMessageMsg{
		record: sessionRecord(extmsg.CommandEnd, "e1", "", ""), sessionKind: extmsg.SessionEdit, reply: make(chan string, 1),
		agentEdits: agentEditSettings{policy: store.AgentEditPolicyReview, keep: 20}, sessionAutoStart: autoStart,
	})
	return settleAgentEdits(m, cmd)
}

func TestAgentEditsOpenTheirReviewWithoutAskingWhenChosen(t *testing.T) {
	m, _, receiver, _, _ := agentEditsModel(t, store.AgentEditPolicyReview)
	m = endAgentEdits(t, m, receiver, true)
	if m.choice.open() || m.mode != modeReview || m.review.agentEdits != "e1" || m.review.agentEditsTrace {
		t.Fatalf("the staged review opens without a prompt: choice=%v mode=%v %q", m.choice.open(), m.mode, m.review.agentEdits)
	}
	if header := headerLine(m); !strings.Contains(header, "● edit session e1 · staged, not written") {
		t.Fatalf("the header names the session: %q", header)
	}
	status := ansi.Strip(m.renderStatusLine())
	notice, decision := strings.Index(status, "✎ EDITS opened without asking"), strings.Index(status, "a apply all to disk")
	if notice < 0 || decision < notice || strings.Contains(status, "edit session e1") {
		t.Fatalf("the status line leads with the notice, then the decision, without the badge: %q", status)
	}
}

func TestAgentEditsAutoStartNeverPullsTheUserOutOfAnotherPrompt(t *testing.T) {
	m, _, receiver, _, _ := agentEditsModel(t, store.AgentEditPolicyReview)
	m.choice = removePathChoice("main.go", false)
	m.confirmRm = "main.go"
	m = endAgentEdits(t, m, receiver, true)
	if m.mode == modeReview || m.choice.kind != choiceRemovePath || !strings.Contains(m.notice, "agent edits ready") {
		t.Fatalf("with a prompt up only a notice appears: mode=%v choice=%v notice=%q", m.mode, m.choice.kind, m.notice)
	}
}

func TestExternalPanelTogglesSessionAutoStart(t *testing.T) {
	m, _, _, _, settings := agentEditsModel(t, store.AgentEditPolicyReview)
	next, cmd := m.Update(ctrlKey('t'))
	m = settleAgentEdits(next.(Model), cmd)
	for m.inspectMenu != inspectMenuExternal {
		m = key(m, shiftKey(tea.KeyDown))
	}
	if plain := ansi.Strip(m.View().Content); !strings.Contains(plain, "agent sessions") || !strings.Contains(plain, "● ask first") {
		t.Fatalf("the panel shows the row, asking by default:\n%s", plain)
	}
	m = key(m, keyPress(tea.KeyRight))
	m = key(m, keyPress(tea.KeyDown))
	next, cmd = m.Update(keyPress(tea.KeyEnter))
	m = settleAgentEdits(next.(Model), cmd)
	if saved, _ := settings.LoadExternalSettings(); !saved.SessionAutoStart || !m.external.settings.SessionAutoStart {
		t.Fatal("Enter on the agent sessions row switches to start directly")
	}
	if plain := ansi.Strip(m.View().Content); !strings.Contains(plain, "● start directly") {
		t.Fatalf("the row shows the new choice:\n%s", plain)
	}

	next, cmd = m.inspectExternalCommand("sessions ask")
	m = settleAgentEdits(next.(Model), cmd)
	if saved, _ := settings.LoadExternalSettings(); saved.SessionAutoStart {
		t.Fatal("ext sessions ask switches back")
	}
	next, _ = m.inspectExternalCommand("sessions maybe")
	if !strings.Contains(next.(Model).errText, "ext sessions ask|direct") {
		t.Fatalf("a bad value shows the usage: %q", next.(Model).errText)
	}
}
