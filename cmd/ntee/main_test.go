package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nickooan/ntee-editor/internal/app"
	"github.com/nickooan/ntee-editor/internal/extmsg"
	"github.com/nickooan/ntee-editor/internal/store"
)

func TestResolveStartTarget(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("A=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		argument, root, file string
		line                 int
		wantErr              string
	}{
		{argument: dir, root: dir},
		{argument: filepath.Join(dir, ".env"), root: dir, file: ".env"},
		{argument: filepath.Join(dir, "a.go") + "#L12", root: dir, file: "a.go", line: 12},
		{argument: filepath.Join(dir, "new.txt"), root: dir, file: "new.txt"},
		{argument: filepath.Join(dir, "missing", "new.txt"), wantErr: "no such file or directory"},
		{argument: dir + "#L3", wantErr: "needs a file"},
	}
	for _, c := range cases {
		root, file, line, err := resolveStartTarget(c.argument)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%s: err = %v, want %q", c.argument, err, c.wantErr)
			}
			continue
		}
		if err != nil || root != c.root || file != c.file || line != c.line {
			t.Errorf("%s = (%q, %q, %d, %v), want (%q, %q, %d)", c.argument, root, file, line, err, c.root, c.file, c.line)
		}
	}
}

func TestSingleFileModeNeverStartsTheSocket(t *testing.T) {
	// A short HOME keeps the socket path under the platform's sun_path limit.
	home, err := os.MkdirTemp("/tmp", "nteehome")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	t.Setenv("HOME", home)
	project := t.TempDir()

	server, socketPath, problem := listenForExternalMessages(project, nil)
	if server != nil || !strings.Contains(problem, "single-file mode") {
		t.Fatalf("single-file mode: server=%v problem=%q", server, problem)
	}
	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Fatalf("no socket file may exist in single-file mode: %v", err)
	}

	// A folder does listen, on the same path.
	receiver := app.NewExternalReceiver(project, store.NewMemoryExternalSettings(store.DefaultExternalSettings), store.NewMemoryExternalHistory())
	server, folderSocket, problem := listenForExternalMessages(project, receiver)
	if server == nil || problem != "" || folderSocket != socketPath {
		t.Fatalf("folder mode: server=%v problem=%q", server, problem)
	}
	defer server.Close()
	if _, err := os.Stat(socketPath); err != nil {
		t.Fatalf("a folder editor listens: %v", err)
	}
}

func TestBuildRequestCarriesTheBaseOnTheFirstLine(t *testing.T) {
	cases := []struct {
		command, base string
		paths         []string
		want          string
	}{
		{"open", "", []string{"/p/a.go#L3"}, "open\n/p/a.go#L3\n"},
		{"open", "v1.2.0", []string{"/p/a.go#L3"}, "open v1.2.0\n/p/a.go#L3\n"},
		{"review", "HEAD~2", []string{"/p/a.go", "/p/b.go"}, "review HEAD~2\n/p/a.go\n/p/b.go\n"},
	}
	for _, c := range cases {
		if got := buildRequest(c.command, c.base, "", c.paths, ""); got != c.want {
			t.Errorf("buildRequest(%q, %q) = %q, want %q", c.command, c.base, got, c.want)
		}
	}
}

func TestBuildRequestPutsTheNoteAfterTheSeparator(t *testing.T) {
	got := buildRequest("open", "v1", "", []string{"/p/a.go#L3"}, "# Why\n\n  - kept")
	if want := "open v1\n/p/a.go#L3\n---\n# Why\n\n  - kept\n"; got != want {
		t.Fatalf("buildRequest = %q, want %q", got, want)
	}
	message, err := extmsg.Parse(got)
	if err != nil || message.LineNote != "# Why\n\n  - kept" || message.Paths[0] != "/p/a.go#L3" {
		t.Fatalf("the editor reads it back as %+v, %v", message, err)
	}
}

func TestCheckLineNote(t *testing.T) {
	cases := []struct {
		command string
		paths   []string
		note    string
		wantErr string
	}{
		{"open", []string{"/p/a.go"}, "", ""},
		{"open", []string{"/p/a.go#L3"}, "why", ""},
		{"open", []string{"/p/a.go#L3-L9"}, "why", ""},
		{"open", []string{"/p/a.go"}, "why", "a note needs a line"},
		{"open", []string{"/p/a.go#L"}, "why", "a note needs a line"},
		{"review", []string{"/p/a.go#L3"}, "why", "goes with open"},
	}
	for _, c := range cases {
		err := checkLineNote(c.command, c.paths, c.note)
		if (c.wantErr == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), c.wantErr)) {
			t.Errorf("checkLineNote(%q, %v, %q) = %v, want %q", c.command, c.paths, c.note, err, c.wantErr)
		}
	}
}

func TestReadNoteFromStdinKeepsTheText(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	note := "# Why\n\n  indented line\n"
	go func() {
		writer.WriteString(note)
		writer.Close()
	}()
	got, err := readNoteFromStdin(reader)
	if err != nil || got != note {
		t.Fatalf("readNoteFromStdin = %q, %v; want %q", got, err, note)
	}
}

func TestBuildRequestCarriesTheSession(t *testing.T) {
	if got, want := buildRequest("open", "v1", "walk1", []string{"/p/a.go#L3"}, "why"), "open --session=walk1 v1\n/p/a.go#L3\n---\nwhy\n"; got != want {
		t.Fatalf("open step = %q, want %q", got, want)
	}
	if got, want := buildRequest("wait", "", "walk1", nil, ""), "wait --session=walk1\n"; got != want {
		t.Fatalf("wait = %q, want %q", got, want)
	}
	for _, request := range []string{buildRequest("open", "", "walk1", []string{"/p/a.go#L3"}, "why"), buildRequest("end", "", "walk1", nil, "")} {
		if message, err := extmsg.Parse(request); err != nil || message.Session != "walk1" {
			t.Fatalf("the editor reads %q as %+v, %v", request, message, err)
		}
	}
}

func TestCheckSessionCommand(t *testing.T) {
	cases := []struct {
		command, session string
		paths            []string
		wantErr          string
	}{
		{"open", "walk1", []string{"a.go#L3"}, ""},
		{"wait", "walk1", nil, ""},
		{"end", "walk1", nil, ""},
		{"wait", "", nil, "needs --msg-session"},
		{"end", "walk1", []string{"a.go"}, "takes no paths"},
		{"review", "walk1", []string{"a.go"}, "goes with open"},
	}
	for _, c := range cases {
		err := checkSessionCommand(c.command, c.paths, c.session)
		if (c.wantErr == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), c.wantErr)) {
			t.Errorf("checkSessionCommand(%q, %v, %q) = %v, want %q", c.command, c.paths, c.session, err, c.wantErr)
		}
	}
}

func TestDeliverRequestExitCodes(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "nteecli")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socketPath := filepath.Join(dir, "s.sock")
	server, err := extmsg.Listen(socketPath, func(text string) string {
		switch {
		case strings.HasPrefix(text, "slow"):
			time.Sleep(time.Second)
			return "ok next"
		case strings.HasPrefix(text, "next"):
			return "ok next"
		case strings.HasPrefix(text, "ended"):
			return "ended session walk1 was closed by the user — stop sending steps"
		case strings.HasPrefix(text, "waiting"):
			return "ok waiting — the user hasn't answered yet"
		}
		return "error nope"
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	cases := []struct {
		request  string
		session  string
		wantCode int
		wantText string
	}{
		{"next\n", "walk1", 0, "ok next"},
		{"ended\n", "walk1", exitSessionEnded, "ended session walk1"},
		{"waiting\n", "walk1", exitSessionWaiting, "ok waiting"},
		{"bad\n", "", 1, "error nope"},
		{"slow\n", "walk1", exitSessionWaiting, "--msg-session walk1 wait"},
	}
	for _, c := range cases {
		output, code := deliverRequest(socketPath, c.request, c.session, 200*time.Millisecond)
		if code != c.wantCode || !strings.Contains(output, c.wantText) {
			t.Errorf("%q → %q exit %d, want %q exit %d", c.request, output, code, c.wantText, c.wantCode)
		}
	}
}
