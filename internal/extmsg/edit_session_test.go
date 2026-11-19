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
		{text: "end --edit-session=e1\n", want: Message{Command: "end", Args: []string{}, Session: "e1", SessionKind: SessionEdit}},
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
		if got.Command != test.want.Command || got.Session != test.want.Session || got.SessionKind != test.want.SessionKind ||
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
