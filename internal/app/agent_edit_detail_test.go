package app

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/nickooan/ntee-editor/internal/extmsg"
	"github.com/nickooan/ntee-editor/internal/store"
)

// agentEditsPanel opens Ctrl+T › agent edits with the panel focused.
func agentEditsPanel(t *testing.T, m Model) Model {
	t.Helper()
	m.width, m.height = 150, 60
	next, cmd := m.Update(ctrlKey('t'))
	m = settleAgentEdits(next.(Model), cmd)
	for m.inspectMenu != inspectMenuAgentEdits {
		m = key(m, shiftKey(tea.KeyDown))
	}
	return key(m, keyPress(tea.KeyRight))
}

// openFirstSessionDetail moves to the first session row and opens it.
func openFirstSessionDetail(t *testing.T, m Model) Model {
	t.Helper()
	m = key(m, keyPress(tea.KeyDown))
	m = key(m, keyPress(tea.KeyDown))
	next, cmd := m.Update(keyPress(tea.KeyEnter))
	m = settleAgentEdits(next.(Model), cmd)
	if m.agentEditsPicker.detail == nil {
		t.Fatal("Enter on a session opens its details")
	}
	return m
}

// appliedSharedSession is sharedSession's S taken all the way: both agents
// end and check, the user asks for changes, agent-a edits again, both end
// and check again, and S is applied with a commit asked.
func appliedSharedSession(t *testing.T) Model {
	t.Helper()
	m, _, receiver := sharedSession(t)
	registry := m.agentEdits.registry
	endAndCheck := func(checkB store.AgentEditCheckResult, noteB string) {
		for _, agent := range []string{"agent-a", "agent-b"} {
			_, _, err := registry.MarkAgentEnded("S", agent)
			must(t, err)
		}
		_, _, err := registry.RecordCheck("S", "agent-a", store.CheckPassed, "go test ./... ok")
		must(t, err)
		_, _, err = registry.RecordCheck("S", "agent-b", checkB, noteB)
		must(t, err)
	}
	endAndCheck(store.CheckFailed, "lint: 2 warnings\nthe rest")
	_, _, err := registry.RequestChanges("S")
	must(t, err)
	receiver.Handle(agentReplace("S", "agent-a", "main.go", extmsg.ReplacePayload{Old: "println(1)", New: "println(1); println(3)"}))
	endAndCheck(store.CheckSkipped, "")
	record, _ := registry.Snapshot("S")
	_, err = registry.Finish("S", store.AgentEditPolicyReview, store.GitFollowUpCommit, registry.ApplyFiles("S", record.Files, nil), 20)
	must(t, err)
	return m.refreshAgentEditsPending()
}

func TestTheAgentEditsPanelSplitsSettingsFromHistory(t *testing.T) {
	m, _, _ := sharedSession(t)
	plain := ansi.Strip(agentEditsPanel(t, m).View().Content)
	setting, history, session := strings.Index(plain, "SETTING"), strings.Index(plain, "history  ·  pending first, then finished"), strings.Index(plain, "SESSION")
	if setting < 0 || history < setting || session < history || !strings.Contains(plain, "● 2 of 2 agents editing") {
		t.Fatalf("the settings table, then the history table:\n%s", plain)
	}
}

func TestASessionsDetailsShowItsStagesChangesAndAgents(t *testing.T) {
	m := openFirstSessionDetail(t, agentEditsPanel(t, appliedSharedSession(t)))
	if detail := m.agentEditsPicker.detail; detail.pending || !detail.whole || detail.facts.edits != 3 {
		t.Fatalf("a finished session's whole record is loaded: %+v", detail)
	}
	plain := ansi.Strip(m.View().Content)
	for _, want := range []string{
		"edit session S", "▸ review", "▶ open the review (read-only trace)  2 files · 3 edits",
		"✔ applied", "✓ editing", "✓ checking", "✓ ready", "in all",
		"2 files  3 edits  +5 −4  2 applied",
		"you reviewed it, then decided",
		"✓ asked", "answered by agent-a",
		"commit asked",
		"agents  ·  2 agents · who did what",
		"2 edits in 1 file  +4 −3", "✓ check passed — go test ./... ok",
		"1 edit in 1 file  +1 −1", "– check skipped",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("the details miss %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "main.go") || strings.Contains(plain, "util.ts") {
		t.Fatalf("the details name no file — the review does:\n%s", plain)
	}
}

func TestEscFromASessionsReviewComesBackToItsDetails(t *testing.T) {
	m := openFirstSessionDetail(t, agentEditsPanel(t, appliedSharedSession(t)))
	next, cmd := m.Update(keyPress(tea.KeyEnter))
	m = settleAgentEdits(next.(Model), cmd)
	if m.mode != modeReview || m.review.agentEdits != "S" || !m.review.agentEditsTrace {
		t.Fatalf("the review row opens the trace: mode=%v %q", m.mode, m.review.agentEdits)
	}
	m = key(m, keyPress(tea.KeyEscape))
	if m.mode != modeInspect || m.agentEditsPicker.detail == nil || m.inspectMenu != inspectMenuAgentEdits {
		t.Fatalf("Esc comes back to the details: mode=%v", m.mode)
	}
	m = key(m, keyPress(tea.KeyEscape))
	if m.agentEditsPicker.detail != nil || !m.agentEditsPicker.focused || m.agentEditsPicker.index != agentEditsRowSessions {
		t.Fatalf("Esc goes back to the list, on the same row: %+v", m.agentEditsPicker)
	}
}

func TestAPendingSessionsDetailsFollowItsAgents(t *testing.T) {
	m, _, receiver := sharedSession(t)
	m = openFirstSessionDetail(t, agentEditsPanel(t, m))
	if detail := m.agentEditsPicker.detail; !detail.pending || detail.facts.edits != 2 {
		t.Fatalf("a pending session is whole from the mirror: %+v", detail)
	}
	if plain := ansi.Strip(m.View().Content); !strings.Contains(plain, "▶ open the live review") || !strings.Contains(plain, "● editing") || !strings.Contains(plain, "○ applied or discarded") {
		t.Fatalf("a pending session's stage runs:\n%s", plain)
	}

	receiver.Handle(agentReplace("S", "agent-a", "main.go", extmsg.ReplacePayload{Old: "println(1)", New: "println(2)"}))
	next, _ := m.Update(agentEditsChangedMsg{session: "S"})
	m = next.(Model)
	if m.agentEditsPicker.detail.facts.edits != 3 {
		t.Fatalf("a new edit shows at once: %+v", m.agentEditsPicker.detail.facts)
	}

	m = key(m, typeRune('x'))
	if m.choice.kind != choiceDiscardAgentEdits {
		t.Fatal("x on the details asks before discarding")
	}
	next, cmd := m.Update(typeRune('y'))
	m = settleAgentEdits(next.(Model), cmd)
	detail := m.agentEditsPicker.detail
	if detail == nil || detail.pending || !detail.whole || detail.record.Status != store.AgentEditDiscarded {
		t.Fatalf("the page follows the session into the log: %+v", detail)
	}
	if plain := ansi.Strip(m.View().Content); !strings.Contains(plain, "⊘ discarded") || !strings.Contains(plain, "– ready") {
		t.Fatalf("the discard shows, and the stages it skipped:\n%s", plain)
	}
}

func TestALiveReviewFromTheDetailsSaysEscGoesBackToIt(t *testing.T) {
	m, _, _ := sharedSession(t)
	m = openFirstSessionDetail(t, agentEditsPanel(t, m))
	next, cmd := m.Update(keyPress(tea.KeyEnter))
	m = settleAgentEdits(next.(Model), cmd)
	if m.mode != modeReview || m.review.agentEditsTrace || m.review.previousMode != modeInspect {
		t.Fatalf("the live review opens over the details: mode=%v", m.mode)
	}
	if plain := ansi.Strip(m.View().Content); !strings.Contains(plain, "back to session") {
		t.Fatalf("its panel says where Esc goes:\n%s", plain)
	}
}

func TestAnOutdatedDetailsLoadIsDropped(t *testing.T) {
	m := openFirstSessionDetail(t, agentEditsPanel(t, appliedSharedSession(t)))
	before := m.agentEditsPicker.detail
	next, _ := m.Update(agentEditDetailLoadedMsg{gen: m.agentEdits.detailGen - 1, session: "S"})
	if next.(Model).agentEditsPicker.detail != before {
		t.Fatal("an older load changes nothing")
	}
	next, _ = m.Update(agentEditDetailLoadedMsg{gen: m.agentEdits.detailGen, session: "S"})
	if plain := ansi.Strip(next.(Model).View().Content); !next.(Model).agentEditsPicker.detail.missing || !strings.Contains(plain, "no longer in the log") {
		t.Fatalf("a trimmed session says so:\n%s", plain)
	}
}

func TestAgentEditDurationReadsAtAGlance(t *testing.T) {
	for span, want := range map[time.Duration]string{
		-time.Second:                          "0s",
		42 * time.Second:                      "42s",
		8*time.Minute + 12*time.Second:        "8m 12s",
		2*time.Hour + 5*time.Minute:           "2h 5m",
		76*time.Hour + 20*time.Minute:         "3d 4h",
		59*time.Second + 600*time.Millisecond: "1m 0s",
	} {
		if got := agentEditDuration(span); got != want {
			t.Errorf("agentEditDuration(%v) = %q, want %q", span, got, want)
		}
	}
}

func TestTheSelectedSessionRowIsHighlightedAcrossEveryCell(t *testing.T) {
	m, _, _ := sharedSession(t)
	m = agentEditsPanel(t, m)
	m = key(m, keyPress(tea.KeyDown))
	m = key(m, keyPress(tea.KeyDown))
	panel := m.renderInspectAgentEdits(120, 60)
	var row string
	for _, line := range strings.Split(panel, "\n") {
		if strings.Contains(ansi.Strip(line), "▸ S ") {
			row = line
		}
	}
	// A style's opening escape, to find the text it draws in the row.
	opening := func(style lipgloss.Style) string { return strings.SplitN(style.Render("x"), "x", 2)[0] }
	for _, want := range []string{
		opening(panelPickedStyle) + "▸ S",
		opening(panelPicked(panelCurrentStyle, true)) + "● 2 of 2 agents editing",
		opening(panelPicked(panelDimStyle, true)) + "  2 files",
	} {
		if !strings.Contains(row, want) {
			t.Fatalf("the selected row draws %q on the selection bar:\n%q", ansi.Strip(want), row)
		}
	}
	if strings.Contains(row, opening(panelCurrentStyle)+"● 2 of 2 agents editing") {
		t.Fatal("its status text is not left on the panel background")
	}
}
