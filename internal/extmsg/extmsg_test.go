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
		{text: "review --exec=x\na\n", wantErr: "bad revision"},
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
