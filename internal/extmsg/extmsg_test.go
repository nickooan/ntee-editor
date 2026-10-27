package extmsg

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	cases := []struct {
		text    string
		want    Message
		wantErr string
	}{
		{text: "open\nsrc/app.go#L10\n", want: Message{Command: "open", Args: []string{}, Paths: []string{"src/app.go#L10"}}},
		{text: "\r\n  REVIEW  \r\na.go\r\n\r\nb.go\r\n", want: Message{Command: "review", Args: []string{}, Paths: []string{"a.go", "b.go"}}},
		{text: "review HEAD~2\na.go", want: Message{Command: "review", Args: []string{"HEAD~2"}, Paths: []string{"a.go"}}},
		{text: "", wantErr: "empty message"},
		{text: "open\n", wantErr: "exactly one path"},
		{text: "open\na\nb\n", wantErr: "exactly one path"},
		{text: "open abc123\na.go\n", want: Message{Command: "open", Args: []string{"abc123"}, Paths: []string{"a.go"}}},
		{text: "open -x\na.go\n", wantErr: "bad revision"},
		{text: "open a b\nx\n", wantErr: "at most one argument"},
		{text: "review\n", wantErr: "at least one path"},
		{text: "review --exec=x\na\n", wantErr: "unknown option"},
		{text: "review a b\nx\n", wantErr: "at most one argument"},
		{text: "delete\na\n", wantErr: "unknown command"},
	}
	for _, c := range cases {
		got, err := Parse(c.text)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("Parse(%q) error = %v, want %q", c.text, err, c.wantErr)
			}
			continue
		}
		if err != nil || got.Command != c.want.Command || fmt.Sprint(got.Args) != fmt.Sprint(c.want.Args) || fmt.Sprint(got.Paths) != fmt.Sprint(c.want.Paths) {
			t.Errorf("Parse(%q) = %+v, %v; want %+v", c.text, got, err, c.want)
		}
	}
	tooMany := "review\n" + strings.Repeat("a.go\n", MaxReviewPaths+1)
	if _, err := Parse(tooMany); err == nil {
		t.Error("more than MaxReviewPaths paths must be refused")
	}
	if message, _ := Parse("review main\na.go"); message.Base() != "main" {
		t.Errorf("base = %q", message.Base())
	}
	if message, _ := Parse("open v1.2.0\na.go"); message.Base() != "v1.2.0" {
		t.Errorf("open base = %q", message.Base())
	}
	if message, _ := Parse("open\na.go"); message.Base() != "" {
		t.Errorf("no base = %q, want empty (HEAD)", message.Base())
	}
}

func TestParseSession(t *testing.T) {
	cases := []struct {
		name, text, wantSession, wantBase, wantErr string
		wantKind                                   SessionKind
	}{
		{name: "open step", text: "open --session=walk-1.a\na.go#L3\n---\nwhy\n", wantSession: "walk-1.a", wantKind: SessionNote},
		{name: "note session refuses a base", text: "open HEAD~1 --session=w\na.go#L3\n---\nwhy\n", wantErr: "a note session takes no base revision"},
		{name: "diff step", text: "open --diff-session=w\na.go#L3\n---\nwhy\n", wantSession: "w", wantKind: SessionDiff},
		{name: "diff step with a base", text: "open HEAD~1 --diff-session=w\na.go#L3\n---\nwhy\n", wantSession: "w", wantBase: "HEAD~1", wantKind: SessionDiff},
		{name: "both options", text: "open --session=w --diff-session=w\na.go#L3\n---\nwhy\n", wantErr: "one session option only"},
		{name: "bad diff id", text: "open --diff-session=a/b\na.go#L3\n---\nwhy\n", wantErr: "bad session id"},
		{name: "diff step without a note", text: "open --diff-session=w\na.go#L3\n", wantErr: "needs a note"},
		{name: "review refuses a diff session", text: "review --diff-session=w\na.go\n", wantErr: "review takes no --diff-session="},
		{name: "end a diff session", text: "end --diff-session=w\n", wantSession: "w", wantKind: SessionDiff},
		{name: "wait a diff session", text: "wait --diff-session=w\n", wantSession: "w", wantKind: SessionDiff},
		{name: "bad id", text: "open --session=a/b\na.go#L3\n---\nwhy\n", wantErr: "bad session id"},
		{name: "empty id", text: "open --session=\na.go#L3\n---\nwhy\n", wantErr: "bad session id"},
		{name: "unknown option", text: "open --force\na.go\n", wantErr: "unknown option --force"},
		{name: "step without a note", text: "open --session=w\na.go#L3\n", wantErr: "needs a note"},
		{name: "review refuses", text: "review --session=w\na.go\n", wantErr: "review takes no --session="},
		{name: "end", text: "end --session=w\n", wantSession: "w", wantKind: SessionNote},
		{name: "wait", text: "WAIT --session=w", wantSession: "w", wantKind: SessionNote},
		{name: "review session", text: "review --review-session=r\na.go\n", wantSession: "r", wantKind: SessionReview},
		{name: "review session with a base", text: "review main --review-session=r\na.go\n", wantSession: "r", wantBase: "main", wantKind: SessionReview},
		{name: "review session note, anchored", text: "review --review-session=r\na.go#L3\n---\nwhy\n", wantSession: "r", wantKind: SessionReview},
		{name: "review session note, no anchor", text: "review --review-session=r\na.go\n---\nwhy\n", wantSession: "r", wantKind: SessionReview},
		{name: "review session, two files", text: "review --review-session=r\na.go\nb.go\n", wantErr: "one file per review, got 2"},
		{name: "open refuses a review session", text: "open --review-session=r\na.go#L3\n---\nwhy\n", wantErr: "open takes no --review-session="},
		{name: "end a review session", text: "end --review-session=r\n", wantSession: "r", wantKind: SessionReview},
		{name: "review and diff options", text: "review --review-session=r --diff-session=r\na.go\n", wantErr: "one session option only"},
		{name: "end without id", text: "end\n", wantErr: "end needs --session"},
		{name: "wait with a path", text: "wait --session=w\na.go\n", wantErr: "takes only"},
		{name: "end with a note", text: "end --session=w\n---\nhi\n", wantErr: "takes only"},
	}
	for _, c := range cases {
		got, err := Parse(c.text)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%s: error = %v, want %q", c.name, err, c.wantErr)
			}
			continue
		}
		if err != nil || got.Session != c.wantSession || got.Base() != c.wantBase || got.SessionKind != c.wantKind {
			t.Errorf("%s: Parse = %+v, %v", c.name, got, err)
		}
	}
	if !IsEndedReply(EndedReply("closed by\nthe user")) || EndedReply("x y") != "ended x y" || IsEndedReply("ok ended") || IsErrorReply(EndedReply("x")) {
		t.Fatal("ended replies")
	}
}

func TestParseLineNote(t *testing.T) {
	cases := []struct {
		name, text, wantNote, wantErr string
		wantPaths                     []string
	}{
		{name: "none", text: "open\na.go#L3\n", wantPaths: []string{"a.go#L3"}},
		{
			name:      "formatting kept",
			text:      "open\na.go#L3\n---\n\n# Why\n\n  - indented\n---\nlast  \n\n",
			wantNote:  "# Why\n\n  - indented\n---\nlast",
			wantPaths: []string{"a.go#L3"},
		},
		{name: "crlf", text: "open main\r\na.go#L3\r\n---\r\none\r\ntwo\r\n", wantNote: "one\ntwo", wantPaths: []string{"a.go#L3"}},
		{name: "empty note", text: "open\na.go#L3\n---\n  \n\n", wantPaths: []string{"a.go#L3"}},
		{name: "no path before note", text: "open\n---\nnote\n", wantErr: "exactly one path"},
		{name: "review refuses a note", text: "review\na.go\n---\nnote\n", wantErr: "review takes no note"},
		{name: "too long", text: "open\na.go#L3\n---\n" + strings.Repeat("x", MaxLineNoteBytes+1), wantErr: "note is too long"},
	}
	for _, c := range cases {
		got, err := Parse(c.text)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%s: error = %v, want %q", c.name, err, c.wantErr)
			}
			continue
		}
		if err != nil || got.LineNote != c.wantNote || fmt.Sprint(got.Paths) != fmt.Sprint(c.wantPaths) {
			t.Errorf("%s: Parse = %+v, %v; want note %q, paths %v", c.name, got, err, c.wantNote, c.wantPaths)
		}
	}
	if message, err := Parse("open v1\na.go#L3\n---\nn"); err != nil || message.Base() != "v1" {
		t.Errorf("a note keeps the base: %+v, %v", message, err)
	}
}

func TestReplies(t *testing.T) {
	if OkReply("opened\n  a.go") != "ok opened a.go" || ErrorReply("no\nway") != "error no way" {
		t.Fatal("replies are single lines")
	}
	if !IsErrorReply("error x") || IsErrorReply("ok error") || IsErrorReply("errors") {
		t.Fatal("IsErrorReply")
	}
}

// shortSocketDir keeps the socket path under the platform's sun_path limit
// (t.TempDir paths on macOS are too long).
func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "nteemsg")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestServerRoundTripAndPermissions(t *testing.T) {
	socketPath := filepath.Join(shortSocketDir(t), "sub", "ntee.sock")
	server, err := Listen(socketPath, func(text string) string { return OkReply("got " + strings.TrimSpace(text)) })
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	reply, err := Send(socketPath, "open\na.go\n")
	if err != nil || reply != "ok got open a.go" {
		t.Fatalf("reply = %q, %v", reply, err)
	}
	if info, _ := os.Stat(filepath.Dir(socketPath)); info.Mode().Perm() != 0o700 {
		t.Fatalf("socket dir mode = %v", info.Mode().Perm())
	}
	if info, _ := os.Stat(socketPath); info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %v", info.Mode().Perm())
	}

	if _, err := Listen(socketPath, nil); err != ErrInUse {
		t.Fatalf("a second listener on a live socket: %v", err)
	}

	big := strings.Repeat("x", MaxMessageBytes+10)
	if reply, _ := Send(socketPath, big); reply != "error message too large" {
		t.Fatalf("oversize reply = %q", reply)
	}

	server.Close()
	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Fatal("Close removes the socket file")
	}
}

func TestListenReplacesLeftoverSocket(t *testing.T) {
	socketPath := filepath.Join(shortSocketDir(t), "ntee.sock")
	// A crashed instance's socket: the file exists, nobody listens.
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	listener.(*net.UnixListener).SetUnlinkOnClose(false)
	listener.Close()
	if _, err := os.Stat(socketPath); err != nil {
		t.Fatal("fixture: the leftover file should remain")
	}
	server, err := Listen(socketPath, func(string) string { return OkReply("fresh") })
	if err != nil {
		t.Fatalf("a leftover socket file is replaced: %v", err)
	}
	defer server.Close()
	if reply, _ := Send(socketPath, "x"); reply != "ok fresh" {
		t.Fatalf("reply = %q", reply)
	}
}

func TestServerTimesOutAnUnfinishedRequest(t *testing.T) {
	socketPath := filepath.Join(shortSocketDir(t), "ntee.sock")
	server, err := Listen(socketPath, func(string) string { return OkReply("") })
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = conn.Write([]byte("open\n")) // never half-closes
	_ = conn.SetReadDeadline(time.Now().Add(ReadTimeout + 2*time.Second))
	buffer := make([]byte, 256)
	n, _ := conn.Read(buffer)
	if !strings.HasPrefix(string(buffer[:n]), "error message not completed") {
		t.Fatalf("reply = %q", buffer[:n])
	}
}

func TestDiscoverWalksUpToARunningEditor(t *testing.T) {
	base := shortSocketDir(t)
	project := filepath.Join(base, "project")
	nested := filepath.Join(project, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	socketFor := func(root string) (string, error) {
		return filepath.Join(base, "sockets", filepath.Base(root)+".sock"), nil
	}
	if _, _, err := Discover(nested, socketFor); err != ErrNoEditor {
		t.Fatalf("no editor running: %v", err)
	}
	socketPath, _ := socketFor(project)
	server, err := Listen(socketPath, func(string) string { return "ok" })
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	root, found, err := Discover(nested, socketFor)
	realProject, _ := filepath.EvalSymlinks(project)
	if err != nil || found != socketPath || (root != project && root != realProject) {
		t.Fatalf("Discover = %q %q %v", root, found, err)
	}
}

// TestSendReadsAnEarlyReply pins the client side of an early answer: a server
// that replies and closes without reading the whole request (the oversize
// refusal) fails the client's write, and the reply must still come through.
func TestSendReadsAnEarlyReply(t *testing.T) {
	socketPath := filepath.Join(shortSocketDir(t), "early.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		buffer := make([]byte, 16)
		_, _ = conn.Read(buffer) // read a little, then answer and hang up
		_, _ = conn.Write([]byte("error message too large\n"))
		conn.Close()
	}()
	reply, err := Send(socketPath, strings.Repeat("x", 8<<20))
	if err != nil || reply != "error message too large" {
		t.Fatalf("reply = %q, %v", reply, err)
	}
}
