package app

import (
	"os/exec"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/extmsg"
	"github.com/nickooan/ntee-editor/internal/store"
)

// stagedReviewInRepo is stagedReview with the workspace a git repo, so the
// apply popup offers the git follow-ups.
func stagedReviewInRepo(t *testing.T) (Model, string, chan string) {
	t.Helper()
	return stagedReviewWith(t, func(m Model) Model {
		m.workspaceIsRepo = true
		return m
	})
}

func TestTheApplyPopupOffersGitFollowUpsForFilesInARepo(t *testing.T) {
	m, _, _ := stagedReviewInRepo(t)
	m = key(m, typeRune('a'))
	if m.choice.kind != choiceApplyAgentEdits || strings.Join(m.choice.choices, "|") != "Apply only|Apply & commit|Apply & new PR|Cancel" {
		t.Fatalf("the popup offers the follow-ups: %+v", m.choice.choices)
	}
	if last := m.choice.body[len(m.choice.body)-1].text; !strings.Contains(last, "commits in ") || !strings.Contains(last, "it asks you the name") {
		t.Fatalf("it says what each does: %q", last)
	}

	m, _, _ = stagedReview(t) // not a repo
	m = key(m, typeRune('a'))
	if strings.Join(m.choice.choices, "|") != "Apply|Cancel" {
		t.Fatalf("outside a repo there is nothing to commit: %+v", m.choice.choices)
	}

	m, _, _ = stagedReviewWith(t, func(m Model) Model {
		m.workspaceRepos = []string{"lib"} // main.go is in no repo
		return m
	})
	if m = key(m, typeRune('a')); strings.Join(m.choice.choices, "|") != "Apply|Cancel" {
		t.Fatalf("a file outside every repo leaves git out: %+v", m.choice.choices)
	}
}

// applyWith picks the apply popup's choice at index and settles the apply.
func applyWith(t *testing.T, m Model, index int) Model {
	t.Helper()
	m = key(m, typeRune('a'))
	for range index {
		m = key(m, keyPress(tea.KeyRight))
	}
	next, cmd := m.Update(keyPress(tea.KeyEnter))
	return settleAgentEdits(next.(Model), cmd)
}

func TestApplyAndCommitAsksTheAgentToCommitTheAppliedFiles(t *testing.T) {
	m, root, waitReply := stagedReviewInRepo(t)
	m = applyWith(t, m, 1)
	outcome := gotReply(t, waitReply)
	if !strings.HasPrefix(outcome, "finished — edit session e1 applied 1 file · the user asks you to commit the applied files on the current branch: commit only main.go") ||
		!strings.Contains(outcome, "no new branch, no push") {
		t.Fatalf("the agent is asked to commit: %q", outcome)
	}
	if !strings.Contains(m.notice, "asked the agent to commit") {
		t.Fatalf("the notice says so: %q", m.notice)
	}
	if diskContent(t, root, "main.go") != "package main\n\nfunc main() {\n\tprintln(1)\n}\n" {
		t.Fatal("the files are applied; the editor runs no git")
	}
	record, _ := m.agentEdits.registry.Finished("e1")
	if record.GitFollowUp != store.GitFollowUpCommit {
		t.Fatalf("the follow-up is kept on the record: %q", record.GitFollowUp)
	}
	late := make(chan string, 1)
	m, _ = deliver(m, externalMessageMsg{record: sessionRecord(extmsg.CommandWait, "e1", "", ""), sessionKind: extmsg.SessionEdit, reply: late})
	if !strings.Contains(gotReply(t, late), "asks you to commit") {
		t.Fatal("a later wait hears the follow-up too")
	}
}

func TestApplyAndNewPRAsksTheAgentToAgreeTheBranchName(t *testing.T) {
	m, _, waitReply := stagedReviewInRepo(t)
	m = applyWith(t, m, 2)
	outcome := gotReply(t, waitReply)
	if !strings.Contains(outcome, "the user asks for a pull request: the current branch is unknown — if the user already agreed on that branch for this change, use it; otherwise first ask them for the branch name") ||
		!strings.Contains(outcome, "commit only main.go, push it and open the pull request") {
		t.Fatalf("the agent is asked for a PR, the branch name first unless already agreed: %q", outcome)
	}
	if !strings.Contains(outcome, "if your harness asks the user to confirm a git step, say so in your reply and wait for them") ||
		!strings.Contains(outcome, "run: ntee --msg --msg-edit-session e1 --msg-note '<what happened>' report done|blocked|declined") {
		t.Fatalf("the agent is told to report back, blocked included: %q", outcome)
	}
	if !strings.Contains(m.notice, "asked the agent to open a PR") {
		t.Fatalf("the notice says so: %q", m.notice)
	}
}

func TestTheGitFollowUpNamesEachRepoCurrentBranch(t *testing.T) {
	repo, plain := t.TempDir(), t.TempDir()
	if output, err := exec.Command("git", "init", "-q", "-b", "fix-thing", repo).CombinedOutput(); err != nil {
		t.Skipf("git unavailable: %v %s", err, output)
	}
	branches := currentGitBranches(map[string]string{"repo": repo, "plain": plain})
	if len(branches) != 1 || branches["repo"] != "fix-thing" {
		t.Fatalf("the repo's branch is read, a folder outside git left out: %v", branches)
	}
	record := store.AgentEditSession{
		Session:     "e1",
		GitFollowUp: store.GitFollowUpCommit,
		GitBranches: map[string]string{"zeta": "main", "repo": "fix-thing"},
		Files:       []store.AgentEditFile{{Rel: "main.go", Result: store.AgentEditFileApplied}},
	}
	if text := agentEditGitFollowUpText(record); !strings.Contains(text, "on the current branch (repo: fix-thing, zeta: main): commit only main.go") {
		t.Fatalf("the commit text names the branches, sorted by repo: %q", text)
	}
	record.GitFollowUp = store.GitFollowUpPullRequest
	if text := agentEditGitFollowUpText(record); !strings.Contains(text, "the current branch is (repo: fix-thing, zeta: main) — if the user already agreed on that branch") {
		t.Fatalf("the PR text names the branches: %q", text)
	}
}

// appliedWithFollowUp stages main.go in e1 and applies it with followUp.
func appliedWithFollowUp(t *testing.T, followUp store.AgentEditGitFollowUp) (Model, *ExternalReceiver) {
	t.Helper()
	m, _, receiver, _, _ := agentEditsModel(t, store.AgentEditPolicyReview)
	m.workspaceIsRepo = true
	receiver.Handle("write --edit-session=e1\nmain.go\n---\nreplaced\n")
	if _, _, err := receiver.agentEdits.MarkAgentEnded("e1", ""); err != nil {
		t.Fatal(err)
	}
	m, cmd := m.startAgentEditApply("e1", store.AgentEditPolicyReview, false, followUp, 10, nil)
	return settleAgentEdits(m, cmd), receiver
}

func TestAnAgentsGitReportReachesTheRecordTheNoticeAndTheHistoryRow(t *testing.T) {
	m, receiver := appliedWithFollowUp(t, store.GitFollowUpPullRequest)
	reply := receiver.Handle("report --edit-session=e1 done pr=https://example.com/pull/117\n---\nopened the PR\nmore detail")
	if !strings.HasPrefix(reply, "ok report recorded") || !strings.Contains(reply, "PR opened https://example.com/pull/117") {
		t.Fatalf("report reply = %q", reply)
	}
	record, _ := receiver.agentEdits.Finished("e1")
	if record.GitReport == nil || record.GitReport.Result != store.GitReportDone || record.GitReport.Note != "opened the PR\nmore detail" || record.GitReport.At.IsZero() {
		t.Fatalf("the report is on the record: %+v", record.GitReport)
	}
	m, _ = deliver(m, agentEditsReportedMsg{record: record})
	if m.notice != "edit session e1 · PR opened https://example.com/pull/117 — opened the PR" {
		t.Fatalf("notice = %q", m.notice)
	}
	if row := agentEditSessionRow(record, false); !strings.Contains(strings.Join(row.pieces, ""), "PR opened https://example.com/pull/117") || strings.Contains(strings.Join(row.pieces, ""), "PR asked") {
		t.Fatalf("the history row shows the report instead of the ask: %+v", row.pieces)
	}
}

func TestABlockedGitReportIsShownAsNeedingTheUser(t *testing.T) {
	m, receiver := appliedWithFollowUp(t, store.GitFollowUpCommit)
	if reply := receiver.Handle("report --edit-session=e1 blocked\n---\nthe permission check refused git commit"); !strings.HasPrefix(reply, "ok ") {
		t.Fatalf("report reply = %q", reply)
	}
	record, _ := receiver.agentEdits.Finished("e1")
	m, _ = deliver(m, agentEditsReportedMsg{record: record})
	if m.errText != "edit session e1 · commit blocked — the agent needs your OK in its terminal — the permission check refused git commit" {
		t.Fatalf("a blocked report stands out: %q", m.errText)
	}
}

func TestAGitReportIsRefusedWhenThereIsNothingToReportOn(t *testing.T) {
	_, receiver := appliedWithFollowUp(t, store.GitFollowUpNone)
	if reply := receiver.Handle("report --edit-session=e1 done\n"); !strings.Contains(reply, "the user asked for no git work on edit session e1") {
		t.Fatalf("apply only: %q", reply)
	}
	receiver.Handle("write --edit-session=e2\nmain.go\n---\nagain\n")
	if reply := receiver.Handle("report --edit-session=e2 done\n"); !strings.Contains(reply, "edit session e2 is not applied yet") {
		t.Fatalf("still pending: %q", reply)
	}
	if reply := receiver.Handle("report --edit-session=nope done\n"); !strings.Contains(reply, "no finished edit session nope") {
		t.Fatalf("unknown session: %q", reply)
	}
	if reply := receiver.Handle("report --edit-session=e1 maybe\n"); !strings.Contains(reply, "report takes a result: done, blocked or declined") {
		t.Fatalf("bad result: %q", reply)
	}
}

func TestApplyOnlyLeavesGitToNoOne(t *testing.T) {
	m, _, waitReply := stagedReviewInRepo(t)
	m = applyWith(t, m, 0)
	if outcome := gotReply(t, waitReply); outcome != "finished — edit session e1 applied 1 file" {
		t.Fatalf("apply only asks nothing more: %q", outcome)
	}
	if record, _ := m.agentEdits.registry.Finished("e1"); record.GitFollowUp != store.GitFollowUpNone {
		t.Fatalf("stored as none: %q", record.GitFollowUp)
	}
	if strings.Contains(m.notice, "asked the agent") {
		t.Fatalf("no follow-up in the notice: %q", m.notice)
	}
}

func TestCancelOnTheApplyPopupWritesNothing(t *testing.T) {
	m, root, waitReply := stagedReviewInRepo(t)
	m = applyWith(t, m, 3)
	noReply(t, waitReply)
	if m.notice != "apply cancelled" || diskContent(t, root, "main.go") != "package main\n\nfunc main() {\n}\n" {
		t.Fatalf("cancel writes nothing: %q", m.notice)
	}
}

func TestAgentAppliesAsksNoGitFollowUp(t *testing.T) {
	m, _, receiver, registry, settings := agentEditsModel(t, store.AgentEditPolicyApply)
	m.workspaceIsRepo = true
	receiver.Handle("write --edit-session=e1\nmain.go\n---\nreplaced\n")
	current, _ := settings.LoadExternalSettings()
	_, reply := sendAgentEditControl(t, m, receiver, extmsg.CommandEnd, "e1", current)
	if outcome := gotReply(t, reply); outcome != "finished — edit session e1 applied 1 file" {
		t.Fatalf("agent applies: no follow-up: %q", outcome)
	}
	if record, _ := registry.Finished("e1"); record.GitFollowUp != store.GitFollowUpNone {
		t.Fatalf("stored as none: %q", record.GitFollowUp)
	}
}
