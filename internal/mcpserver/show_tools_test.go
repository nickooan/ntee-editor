package mcpserver

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/nickooan/ntee-editor/internal/extmsg"
)

func TestShowCodeOpensTheLineWithItsNote(t *testing.T) {
	h := newHarness(t)
	h.setReply(func(string) string { return "ok opened a.go at line 12" })
	result := h.call(1, toolShow, map[string]any{"path": "a.go", "line": 12, "note": "**why** it matters"})
	want := extmsg.BuildRequest(extmsg.CommandOpen, "", "", []string{filepath.Join(h.workingDir, "a.go") + "#L12"}, "**why** it matters")
	if got := h.nextRequest(); got != want {
		t.Fatalf("show request = %q, want %q", got, want)
	}
	if result.IsError || result.Content[0].Text != "ok opened a.go at line 12" {
		t.Fatalf("show result = %+v", result)
	}

	h.setReply(func(string) string { return "waiting for the user to confirm" })
	if result := h.call(2, toolShow, map[string]any{"path": "a.go", "line": 3}); result.IsError || !strings.Contains(result.Content[0].Text, "asks them first") {
		t.Fatalf("a busy user is asked first, and the agent carries on: %+v", result)
	}
	h.nextRequest()
}

func TestShowArgumentsAreCheckedBeforeSending(t *testing.T) {
	h := newHarness(t)
	for index, arguments := range []map[string]any{
		{"path": "a.go"},             // show_code needs a line
		{"path": "a.go", "line": -1}, // lines start at 1
		{"line": 4},                  // no path
		{"path": "a.go", "line": 2, "base": "main"}, // base is a walkthrough's
	} {
		if result := h.call(index+1, toolShow, arguments); !result.IsError || !strings.Contains(result.Content[0].Text, "nothing was sent") {
			t.Fatalf("%v: refused without sending: %+v", arguments, result)
		}
	}
	h.noRequest()
	if result := h.call(9, toolWalkthroughEnd, map[string]any{}); !result.IsError || !strings.Contains(result.Content[0].Text, "no walkthrough to end") {
		t.Fatalf("end needs a walkthrough: %+v", result)
	}
	h.noRequest()
}

func TestAWalkthroughIsOneLiveReviewSession(t *testing.T) {
	h := newHarness(t)
	walkthrough := h.sessionTag + "walk-1"
	option := extmsg.SessionReview.Option() + walkthrough
	h.setReply(func(string) string { return "ok file 1 queued" })
	result := h.call(1, toolWalkthrough, map[string]any{"path": "a.go", "line": 7, "note": "starts here", "base": "main"})
	if got, want := h.nextRequest(), extmsg.BuildRequest(extmsg.CommandReview, "main", option, []string{filepath.Join(h.workingDir, "a.go") + "#L7"}, "starts here"); got != want {
		t.Fatalf("first stop = %q, want %q", got, want)
	}
	if result.IsError || result.Content[0].Text != "ok file 1 queued\nwalkthrough: "+walkthrough {
		t.Fatalf("first stop result = %+v", result)
	}
	h.setReply(func(string) string { return "ok file 2 added to the review" })
	h.call(2, toolWalkthrough, map[string]any{"path": "b.go"})
	if got, want := h.nextRequest(), extmsg.BuildRequest(extmsg.CommandReview, "", option, []string{filepath.Join(h.workingDir, "b.go")}, ""); got != want {
		t.Fatalf("a stop without a line = %q, want %q", got, want)
	}

	h.setReply(func(string) string {
		return "ok session " + walkthrough + " complete — the user is reviewing 2 files"
	})
	if result := h.call(3, toolWalkthroughEnd, map[string]any{}); result.IsError || !strings.Contains(result.Content[0].Text, toolWalkthroughWait) {
		t.Fatalf("end points at wait: %+v", result)
	}
	if got := h.nextRequest(); got != extmsg.BuildRequest(extmsg.CommandEnd, "", option, nil, "") {
		t.Fatalf("end request = %q", got)
	}

	h.setReply(func(string) string {
		return "waiting — the user hasn't answered yet; run: ntee --msg --msg-review-session " + walkthrough + " wait"
	})
	if result := h.call(4, toolWalkthroughWait, map[string]any{"timeout_seconds": 30}); result.IsError || !strings.Contains(result.Content[0].Text, "call "+toolWalkthroughWait+" again") {
		t.Fatalf("wait still waiting: %+v", result)
	}
	if got := h.nextRequest(); got != extmsg.BuildRequest(extmsg.CommandWait, "", option+" "+extmsg.WaitOption+"30s", nil, "") {
		t.Fatalf("wait waits on the ended walkthrough with its budget: %q", got)
	}

	h.setReply(func(string) string { return "ok file 1 queued" })
	h.call(5, toolWalkthrough, map[string]any{"path": "c.go", "line": 1})
	if got := h.nextRequest(); !strings.Contains(got, extmsg.SessionReview.Option()+h.sessionTag+"walk-2") {
		t.Fatalf("after end the next stop starts a new walkthrough: %q", got)
	}
}

func TestAClosedWalkthroughStopsTheAgent(t *testing.T) {
	h := newHarness(t)
	h.setReply(func(string) string {
		return extmsg.EndedReply("session x was closed by the user — stop sending steps")
	})
	result := h.call(1, toolWalkthrough, map[string]any{"path": "a.go", "line": 2, "note": "n"})
	if result.IsError || !strings.Contains(result.Content[0].Text, "stop sending stops") {
		t.Fatalf("the agent hears to stop: %+v", result)
	}
	h.nextRequest()
	h.setReply(func(string) string { return "ok file 1 queued" })
	h.call(2, toolWalkthrough, map[string]any{"path": "a.go"})
	if got := h.nextRequest(); !strings.Contains(got, h.sessionTag+"walk-2") {
		t.Fatalf("a closed walkthrough is not reused: %q", got)
	}
}

func TestShowToolsWithoutAnEditorSayToShowItInTheReply(t *testing.T) {
	h := newHarness(t)
	h.mu.Lock()
	h.noEditor = true
	h.mu.Unlock()
	if result := h.call(1, toolShow, map[string]any{"path": "a.go", "line": 1}); !result.IsError || !strings.Contains(result.Content[0].Text, "show the code in your reply") {
		t.Fatalf("no editor: %+v", result)
	}
}

func TestShowToolTermsReplaceCLIHints(t *testing.T) {
	got := forShowTools("error review session w compares against HEAD — leave --msg-base off after the first file")
	if !strings.Contains(got, "leave base off after the first "+toolWalkthrough) {
		t.Fatalf("hint in tool terms: %q", got)
	}
	if got := forShowTools("error another guided session (s1) is running — end it first: ntee --msg --msg-session s1 end"); strings.Contains(got, "ntee --msg") {
		t.Fatalf("no CLI commands for the agent: %q", got)
	}
}
