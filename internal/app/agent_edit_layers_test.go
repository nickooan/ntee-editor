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

func TestTheReceiverCatchesEditsOnAStaleRead(t *testing.T) {
	_, _, receiver, _, _ := agentEditsModel(t, store.AgentEditPolicyReview)
	line, _, _ := extmsg.SplitBodyReply(receiver.Handle("read\nmain.go\n"))
	version := strings.TrimPrefix(extmsg.BodyDetail(line), "disk · version ")
	receiver.Handle(agentReplace("S", "agent-a", "main.go", extmsg.ReplacePayload{Old: "func main() {\n}", New: "func main() {\n\tprintln(1)\n}"}))
	// A replace of the lines agent-a changed no longer matches: refused.
	request := extmsg.WithExpect(agentReplace("S", "agent-b", "main.go", extmsg.ReplacePayload{Old: "func main() {\n}", New: "func main() {}"}), version)
	if reply := receiver.Handle(request); !strings.HasPrefix(reply, "error the old text is not in main.go") {
		t.Fatalf("a replace of changed lines is refused: %q", reply)
	}
	// A whole-file write made on the stale read would undo agent-a's change.
	write := extmsg.WithExpect("write --edit-session=S --agent=agent-b\nmain.go\n---\npackage main\n", version)
	if reply := receiver.Handle(write); !strings.HasPrefix(reply, "error main.go changed since you read it: agent agent-a's replace") || !strings.Contains(reply, "a whole-file write would undo that") {
		t.Fatalf("a stale whole-file write is refused: %q", reply)
	}
	request = extmsg.WithExpect(agentReplace("S", "agent-b", "main.go", extmsg.ReplacePayload{Old: "package main", New: "package app"}), version)
	if reply := receiver.Handle(request); !strings.HasPrefix(reply, "ok replaced main.go L1") || !strings.Contains(reply, "· note: main.go changed since you read it") {
		t.Fatalf("one elsewhere goes through with a note: %q", reply)
	}
}

func TestAWaitingAgentHearsItsBaseChanged(t *testing.T) {
	m, _, receiver, _, _ := agentEditsModel(t, store.AgentEditPolicyReview)
	receiver.Handle(replaceRequest("A", "main.go", extmsg.ReplacePayload{Old: "func main() {\n}", New: "func main() {\n\tprintln(1)\n}"}))
	receiver.Handle(replaceRequest("B", "main.go", extmsg.ReplacePayload{Old: "\tprintln(1)\n", New: "\tprintln(1)\n\tprintln(2)\n"}))
	waitReply := make(chan string, 1)
	m, _ = deliver(m, externalMessageMsg{record: sessionRecord(extmsg.CommandWait, "B", "", ""), sessionKind: extmsg.SessionEdit, reply: waitReply})

	reply := receiver.Handle(replaceRequest("A", "main.go", extmsg.ReplacePayload{Old: "println(1)", New: "println(10)"}))
	if !strings.Contains(reply, "edit session B built on your older copy of main.go and can no longer be applied as it is") {
		t.Fatalf("A hears it broke B's layer: %q", reply)
	}
	next, _ := m.Update(agentEditsLayerChangedMsg{session: "B", below: "A", rel: "main.go", reason: "edit 1 no longer applies"})
	m = next.(Model)
	if outcome := gotReply(t, waitReply); !strings.HasPrefix(outcome, "waiting — edit session A changed main.go after edit session B built on it: your copy can no longer be applied") {
		t.Fatalf("B's waiting agent hears it at once: %q", outcome)
	}
	line, _, _ := extmsg.SplitBodyReply(receiver.Handle("read --edit-session=B\nmain.go\n"))
	if !strings.Contains(extmsg.BodyDetail(line), "· stale: edit session A changed it after this session built on it") {
		t.Fatalf("B's read of its own copy says it is stale: %q", line)
	}
}

func TestAStaleStagedFileKeepsItsMarkButNotTheLock(t *testing.T) {
	m, root, receiver, registry, _ := agentEditsModel(t, store.AgentEditPolicyReview)
	receiver.Handle(replaceRequest("A", "main.go", extmsg.ReplacePayload{Old: "func main() {\n}", New: "func main() {\n\tprintln(1)\n}"}))
	next, _ := m.Update(agentEditsChangedMsg{session: "A"})
	m = next.(Model).openFileAt("main.go")
	if _, locked := m.stagedReadOnly(); !locked || !m.agentEdits.stagedTopsDue && !m.agentEdits.stagedTopsRunning {
		t.Fatalf("a live staged file locks, and a check is on its way: %+v", m.agentEdits.stagedFiles)
	}

	must(t, os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n// someone else\n"), 0o644))
	next, _ = m.Update(stagedTopsCheckedMsg{rootGen: m.rootGen, tops: registry.StagedTops()})
	m = next.(Model)
	if _, locked := m.stagedReadOnly(); locked {
		t.Fatal("a stale copy can never be applied: the file is the user's again")
	}
	if notice := m.stagedFileNotice(); !strings.Contains(notice, "edit session A's staged copy is stale (changed on disk since it was staged)") {
		t.Fatalf("the notice says why: %q", notice)
	}
	if plain := ansi.Strip(m.View().Content); !strings.Contains(plain, "✎ main.go") {
		t.Fatalf("the tree still marks it, to find and discard it:\n%s", plain)
	}

	// A refresh keeps the verdict until the next check lands: no flicker.
	next, _ = m.Update(agentEditsChangedMsg{session: "A"})
	if file := next.(Model).agentEdits.stagedFiles["main.go"]; !file.stale {
		t.Fatalf("the verdict survives the refresh: %+v", file)
	}
}

func TestAStagedTopsCheckOnlyUpdatesFilesItStillDescribes(t *testing.T) {
	m, _, _, _, _ := agentEditsModel(t, store.AgentEditPolicyReview)
	m.agentEdits.stagedFiles = map[string]stagedFile{"main.go": {session: "B"}}
	m.agentEdits.stagedTopsRunning, m.agentEdits.stagedTopsDue = true, false
	next, _ := m.Update(stagedTopsCheckedMsg{rootGen: m.rootGen, tops: map[string]staging.StagedTop{
		"main.go":     {Session: "A", Stale: true, Reason: "changed on disk since it was staged"},
		"lib/util.ts": {Session: "A", Stale: true},
	}})
	m = next.(Model)
	if file := m.agentEdits.stagedFiles["main.go"]; file.stale || len(m.agentEdits.stagedFiles) != 1 {
		t.Fatalf("a check about another top, or a file no longer staged, changes nothing: %+v", m.agentEdits.stagedFiles)
	}
	if m.agentEdits.stagedTopsRunning {
		t.Fatal("the check is over")
	}
	next, cmd := m.Update(stagedTopsCheckedMsg{rootGen: m.rootGen - 1})
	if !next.(Model).agentEdits.stagedTopsRunning || cmd == nil {
		t.Fatal("a check for another root is dropped, and another starts")
	}
}

func TestTheStaleCheckRunsOneAtATime(t *testing.T) {
	m, _, receiver, _, _ := agentEditsModel(t, store.AgentEditPolicyReview)
	receiver.Handle(replaceRequest("A", "main.go", extmsg.ReplacePayload{Old: "func main() {\n}", New: "func main() {\n\tprintln(1)\n}"}))
	m = m.refreshAgentEditsPending()
	model, cmd := withStagedTopsCheck(m, nil)
	m = model.(Model)
	if !m.agentEdits.stagedTopsRunning || m.agentEdits.stagedTopsDue || cmd == nil {
		t.Fatal("a due check starts")
	}
	checked, ok := cmd().(stagedTopsCheckedMsg)
	if !ok || checked.tops["main.go"] != (staging.StagedTop{Session: "A"}) {
		t.Fatalf("it reports the tops: %+v", checked)
	}
	m = m.checkStagedTopsSoon()
	if _, again := withStagedTopsCheck(m, nil); again != nil {
		t.Fatal("one due while one runs waits for it")
	}
	next, _ := m.Update(checked)
	if m = next.(Model); m.agentEdits.stagedTopsDue || !m.agentEdits.stagedTopsRunning {
		t.Fatal("it starts as the running one lands")
	}
	m.agentEdits.stagedFiles, m.agentEdits.stagedTopsRunning, m.agentEdits.stagedTopsDue = nil, false, true
	if _, none := withStagedTopsCheck(m, nil); none != nil {
		t.Fatal("nothing staged: nothing to check")
	}
}
