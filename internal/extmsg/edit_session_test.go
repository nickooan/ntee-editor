package extmsg

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseEditSession(t *testing.T) {
	replace := EncodeReplace(ReplacePayload{Old: "a\n---\nb", New: "c", All: true})
	cases := []struct {
		text    string
		want    Message
		wantErr string
	}{
		// A write's payload is the file content, byte for byte: a "---" line,
		// blank lines, CRLF and trailing spaces inside it are content.
		{text: "write --edit-session=e1\na.go\n---\nline 1  \r\n---\n\n\nlast", want: Message{
			Command: "write", Args: []string{}, Paths: []string{"a.go"}, Session: "e1", SessionKind: SessionEdit,
			Payload: "line 1  \r\n---\n\n\nlast", HasPayload: true,
		}},
		{text: "write --edit-session=e1\na.go\n---\n", want: Message{
			Command: "write", Args: []string{}, Paths: []string{"a.go"}, Session: "e1", SessionKind: SessionEdit, HasPayload: true,
		}},
		{text: "replace --edit-session=e1\na.go#L4\n---\n" + replace, want: Message{
			Command: "replace", Args: []string{}, Paths: []string{"a.go#L4"}, Session: "e1", SessionKind: SessionEdit,
			Payload: replace, HasPayload: true,
		}},
		{text: "show --edit-session=e1\na.go\n", want: Message{Command: "show", Args: []string{}, Paths: []string{"a.go"}, Session: "e1", SessionKind: SessionEdit}},
		{text: "status --edit-session=e1\n", want: Message{Command: "status", Args: []string{}, Session: "e1", SessionKind: SessionEdit}},
		// read and staged are for any agent: the edit session is optional.
		{text: "read\na.go\n", want: Message{Command: "read", Args: []string{}, Paths: []string{"a.go"}}},
		{text: "read --edit-session=e1\na.go\n", want: Message{Command: "read", Args: []string{}, Paths: []string{"a.go"}, Session: "e1", SessionKind: SessionEdit}},
		{text: "staged\na.go\n", want: Message{Command: "staged", Args: []string{}, Paths: []string{"a.go"}}},
		{text: "sandbox --edit-session=e1\n", want: Message{Command: "sandbox", Args: []string{}, Session: "e1", SessionKind: SessionEdit}},
		{text: "sandbox --edit-session=e1 fresh\n/p/libs/core\n", want: Message{Command: "sandbox", Args: []string{"fresh"}, Paths: []string{"/p/libs/core"}, Session: "e1", SessionKind: SessionEdit}},
		{text: "sandbox --edit-session=e1 drop\n", want: Message{Command: "sandbox", Args: []string{"drop"}, Session: "e1", SessionKind: SessionEdit}},
		{text: "sandbox\n", wantErr: "sandbox needs --edit-session=<id>"},
		{text: "replace --edit-session=e1 --expect=0123456789ab\na.go\n---\n" + replace, want: Message{Command: "replace", Args: []string{}, Paths: []string{"a.go"}, Session: "e1", SessionKind: SessionEdit, Payload: replace, HasPayload: true, Expect: "0123456789ab"}},
		{text: "replace --edit-session=e1 --expect=nope\na.go\n---\n" + replace, wantErr: "bad --expect=nope"},
		{text: "show --edit-session=e1 --expect=0123456789ab\na.go\n", wantErr: "--expect= goes with an edit session's write or replace"},
		{text: "check --edit-session=e1 --agent=a1 fail\n---\ngo test: 2 failing\n", want: Message{Command: "check", Args: []string{"fail"}, Session: "e1", SessionKind: SessionEdit, Agent: "a1"}},
		{text: "check --edit-session=e1\n", wantErr: "check takes one result: pass, fail or skip"},
		{text: "check --edit-session=e1 maybe\n", wantErr: "check takes one result"},
		{text: "check pass\n", wantErr: "check needs --edit-session=<id>"},
		{text: "check --edit-session=e1 pass\na.go\n", wantErr: "check takes no paths"},
		{text: "sandbox --edit-session=e1 again\n", wantErr: "sandbox takes fresh or drop at most"},
		{text: "sandbox --edit-session=e1 drop\n/p/x\n", wantErr: "sandbox drop takes no folders"},
		{text: "read\n", wantErr: "read needs exactly one path"},
		{text: "read --review-session=r1\na.go\n", wantErr: "read takes no --review-session="},
		{text: "staged HEAD\na.go\n", wantErr: "takes no base revision"},
		{text: "read\na.go\n---\nnote", wantErr: "read takes no note"},
		{text: "end --edit-session=e1\n", want: Message{Command: "end", Args: []string{}, Session: "e1", SessionKind: SessionEdit}},
		{text: "end --edit-session=e1 --agent=mcp-1\n", want: Message{Command: "end", Args: []string{}, Session: "e1", SessionKind: SessionEdit, Agent: "mcp-1"}},
		{text: "write --agent=a1 --edit-session=e1\na.go\n---\nx", want: Message{Command: "write", Args: []string{}, Paths: []string{"a.go"}, Session: "e1", SessionKind: SessionEdit, Agent: "a1", Payload: "x", HasPayload: true}},
		{text: "write --edit-session=e1 --agent=bad/id\na.go\n---\nx", wantErr: "bad agent id"},
		{text: "read --agent=a1\na.go\n", wantErr: "--agent= goes with an edit session's write, replace, end, status or check"},
		{text: "show --edit-session=e1 --agent=a1\na.go\n", wantErr: "--agent= goes with"},
		{text: "write\na.go\n---\nx", wantErr: "write needs --edit-session=<id>"},
		{text: "write --review-session=r1\na.go\n---\nx", wantErr: "write needs --edit-session=<id>"},
		{text: "write --edit-session=e1\na.go\n", wantErr: "needs its content after a --- line"},
		{text: "write --edit-session=e1\na.go\nb.go\n---\nx", wantErr: "exactly one path"},
		{text: "write --edit-session=e1 HEAD\na.go\n---\nx", wantErr: "takes no base revision"},
		{text: "replace --edit-session=e1\na.go\n---\nnot json", wantErr: "bad replace payload"},
		{text: "replace --edit-session=e1\na.go\n---\n{\"old\":\"\",\"new\":\"x\"}", wantErr: "needs the old text"},
		{text: "show --edit-session=e1\na.go\n---\nnote", wantErr: "takes no note"},
		{text: "status --edit-session=e1\na.go\n", wantErr: "takes only the session option"},
		{text: "discard --edit-session=e1\n---\nwhy", wantErr: "takes no note"},
		{text: "open --edit-session=e1\na.go#L2\n---\nnote", wantErr: "open takes no --edit-session="},
		{text: "review --edit-session=e1\na.go\n", wantErr: "review takes no --edit-session="},
	}
	for _, test := range cases {
		got, err := Parse(test.text)
		if test.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Parse(%q) error = %v, want %q", test.text, err, test.wantErr)
			}
			continue
		}
		if err != nil {
			t.Fatalf("Parse(%q): %v", test.text, err)
		}
		if got.Command != test.want.Command || got.Session != test.want.Session || got.SessionKind != test.want.SessionKind || got.Agent != test.want.Agent || got.Expect != test.want.Expect ||
			got.Payload != test.want.Payload || got.HasPayload != test.want.HasPayload ||
			strings.Join(got.Paths, "|") != strings.Join(test.want.Paths, "|") || len(got.Args) != len(test.want.Args) {
			t.Fatalf("Parse(%q) = %+v, want %+v", test.text, got, test.want)
		}
	}

	decoded, err := DecodeReplace(replace)
	if err != nil || decoded.Old != "a\n---\nb" || decoded.New != "c" || !decoded.All {
		t.Fatalf("replace payload round-trip: %+v %v", decoded, err)
	}
}

func TestBodyReplyRoundTrip(t *testing.T) {
	body := "line 1\n\n  indented\nno newline at the end"
	line, got, hasBody := SplitBodyReply(BodyReply(body) + "\n") // the server appends a newline
	if !hasBody || got != body || line != "ok "+strings.TrimPrefix(strings.SplitN(BodyReply(body), "\n", 2)[0], "ok ") {
		t.Fatalf("body round-trip: %q %q %v", line, got, hasBody)
	}
	if line, _, hasBody := SplitBodyReply("error no such file: a.go\n"); hasBody || line != "error no such file: a.go" {
		t.Fatalf("an error is a plain line: %q %v", line, hasBody)
	}
	if _, _, hasBody := SplitBodyReply("ok staged a.go\n"); hasBody {
		t.Fatal("an ok line without a size has no body")
	}
	if _, got, hasBody := SplitBodyReply(BodyReply("")); !hasBody || got != "" {
		t.Fatal("an empty file is an empty body")
	}
	line, got, hasBody = SplitBodyReply(BodyReplyWith(body, "staged · edit session e1 · not written"))
	if !hasBody || got != body || BodyDetail(line) != "staged · edit session e1 · not written" {
		t.Fatalf("a body with a detail: %q %q %v", line, got, hasBody)
	}
	if BodyDetail(strings.SplitN(BodyReply(body), "\n", 2)[0]) != "" {
		t.Fatal("a plain body reply has no detail")
	}

	socketPath := filepath.Join(shortSocketDir(t), "ntee.sock")
	server, err := Listen(socketPath, func(string) string { return BodyReply(body) })
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	_, got, hasBody, err = SendForBody(socketPath, "show --edit-session=e1\na.go\n", time.Second)
	if err != nil || !hasBody || got != body {
		t.Fatalf("over the socket: %q %v %v", got, hasBody, err)
	}
}

func TestEditRequestNamesTheSessionOnlyWhenGiven(t *testing.T) {
	if got, _ := EditRequest(CommandRead, "", "agent-a", "/p/a.go", ""); got != "read\n/p/a.go\n" {
		t.Fatalf("a read without a session = %q", got)
	}
	if got, _ := EditRequest(CommandRead, "e1", "", "/p/a.go", ""); got != "read --edit-session=e1\n/p/a.go\n" {
		t.Fatalf("a read as a session = %q", got)
	}
	if got, _ := EditRequest(CommandWrite, "e1", "", "/p/a.go", "x"); got != "write --edit-session=e1\n/p/a.go\n---\nx" {
		t.Fatalf("a write = %q", got)
	}
	if got, _ := EditRequest(CommandWrite, "e1", "agent-a", "/p/a.go", "x"); got != "write --edit-session=e1 --agent=agent-a\n/p/a.go\n---\nx" {
		t.Fatalf("a write names its agent: %q", got)
	}
	if got, _ := EditRequest(CommandEnd, "e1", "agent-a", "", ""); got != "end --edit-session=e1 --agent=agent-a\n" {
		t.Fatalf("an end names its agent: %q", got)
	}
}

func TestSandboxRequest(t *testing.T) {
	if got := SandboxRequest("e1", "", nil); got != "sandbox --edit-session=e1\n" {
		t.Fatalf("plain = %q", got)
	}
	if got := SandboxRequest("e1", SandboxFresh, []string{"/p/libs/core"}); got != "sandbox --edit-session=e1 fresh\n/p/libs/core\n" {
		t.Fatalf("fresh with include = %q", got)
	}
	if message, err := Parse(SandboxRequest("e1", SandboxDrop, nil)); err != nil || message.Args[0] != "drop" {
		t.Fatalf("a request parses: %+v %v", message, err)
	}
}

func TestCheckRequest(t *testing.T) {
	request := CheckRequest("e1", "a1", CheckFail, "go test: 2 failing\n  TestX")
	message, err := Parse(request)
	if err != nil || message.Command != CommandCheck || message.Args[0] != CheckFail || message.Agent != "a1" || message.LineNote != "go test: 2 failing\n  TestX" {
		t.Fatalf("a check request round-trips: %q → %+v %v", request, message, err)
	}
	if got := CheckRequest("e1", "", CheckPass, ""); got != "check --edit-session=e1 pass\n" {
		t.Fatalf("plain = %q", got)
	}
}

func TestReportRequestRoundTripsThroughParse(t *testing.T) {
	request := ReportRequest("e1", ReportArgs{Result: ReportDone, Commit: "abc123", PullRequest: "https://example.com/pull/7"}, "pushed\nand opened")
	message, err := Parse(request)
	if err != nil || message.Command != CommandReport || message.LineNote != "pushed\nand opened" {
		t.Fatalf("a report request parses: %q → %+v %v", request, message, err)
	}
	if parsed, err := ParseReportArgs(message.Args); err != nil || parsed != (ReportArgs{Result: ReportDone, Commit: "abc123", PullRequest: "https://example.com/pull/7"}) {
		t.Fatalf("its arguments round-trip: %+v %v", parsed, err)
	}
	if got := ReportRequest("e1", ReportArgs{Result: ReportBlocked}, ""); got != "report --edit-session=e1 blocked\n" {
		t.Fatalf("plain = %q", got)
	}
}

func TestReportIsRefusedWhenItsArgumentsAreBad(t *testing.T) {
	for _, request := range []string{
		"report done\n",
		"report --edit-session=e1\n",
		"report --edit-session=e1 maybe\n",
		"report --edit-session=e1 done commit=a commit=b\n",
		"report --edit-session=e1 done branch=x\n",
		"report --edit-session=e1 done\n/some/path\n",
	} {
		if _, err := Parse(request); err == nil {
			t.Fatalf("%q should be refused", request)
		}
	}
}

func TestWithExpect(t *testing.T) {
	request, _ := EditRequest(CommandReplace, "e1", "a1", "/p/a.go", EncodeReplace(ReplacePayload{Old: "x", New: "y"}))
	message, err := Parse(WithExpect(request, "0123456789ab"))
	if err != nil || message.Expect != "0123456789ab" || message.Agent != "a1" || message.Paths[0] != "/p/a.go" {
		t.Fatalf("the version rides on the first line: %+v %v", message, err)
	}
	if WithExpect(request, "") != request {
		t.Fatal("no version, no change")
	}
}
