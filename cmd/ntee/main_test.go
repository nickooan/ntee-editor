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
	review := msgSession{id: "r1", kind: extmsg.SessionReview}
	cases := []struct {
		command string
		paths   []string
		note    string
		session msgSession
		wantErr string
	}{
		{"open", []string{"/p/a.go"}, "", msgSession{}, ""},
		{"open", []string{"/p/a.go#L3"}, "why", msgSession{}, ""},
		{"open", []string{"/p/a.go#L3-L9"}, "why", msgSession{}, ""},
		{"open", []string{"/p/a.go"}, "why", msgSession{}, "a note needs a line"},
		{"open", []string{"/p/a.go#L"}, "why", msgSession{}, "a note needs a line"},
		{"review", []string{"/p/a.go#L3"}, "why", msgSession{}, "goes with open"},
		{"review", []string{"/p/a.go#L3"}, "why", review, ""},
		{"review", []string{"/p/a.go"}, "why", review, ""},
	}
	for _, c := range cases {
		err := checkLineNote(c.command, c.paths, c.note, c.session)
		if (c.wantErr == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), c.wantErr)) {
			t.Errorf("checkLineNote(%q, %v, %q, %+v) = %v, want %q", c.command, c.paths, c.note, c.session, err, c.wantErr)
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
	note, diff := msgSession{id: "walk1", kind: extmsg.SessionNote}, msgSession{id: "walk1", kind: extmsg.SessionDiff}
	if got, want := buildRequest("open", "", note.option(), []string{"/p/a.go#L3"}, "why"), "open --session=walk1\n/p/a.go#L3\n---\nwhy\n"; got != want {
		t.Fatalf("note step = %q, want %q", got, want)
	}
	if got, want := buildRequest("open", "v1", diff.option(), []string{"/p/a.go#L3"}, "why"), "open --diff-session=walk1 v1\n/p/a.go#L3\n---\nwhy\n"; got != want {
		t.Fatalf("diff step = %q, want %q", got, want)
	}
	if got, want := buildRequest("wait", "", note.option(), nil, ""), "wait --session=walk1\n"; got != want {
		t.Fatalf("wait = %q, want %q", got, want)
	}
	for _, c := range []struct {
		request  string
		wantDiff bool
	}{
		{buildRequest("open", "", note.option(), []string{"/p/a.go#L3"}, "why"), false},
		{buildRequest("end", "", note.option(), nil, ""), false},
		{buildRequest("open", "v1", diff.option(), []string{"/p/a.go#L3"}, "why"), true},
		{buildRequest("end", "", diff.option(), nil, ""), true},
	} {
		wantKind := extmsg.SessionNote
		if c.wantDiff {
			wantKind = extmsg.SessionDiff
		}
		if message, err := extmsg.Parse(c.request); err != nil || message.Session != "walk1" || message.SessionKind != wantKind {
			t.Fatalf("the editor reads %q as %+v, %v", c.request, message, err)
		}
	}
}

func TestBuildRequestForAReviewSession(t *testing.T) {
	review := msgSession{id: "r1", kind: extmsg.SessionReview}
	request := buildRequest("review", "main", review.option(), []string{"/p/a.go"}, "why")
	if want := "review --review-session=r1 main\n/p/a.go\n---\nwhy\n"; request != want {
		t.Fatalf("review session request = %q, want %q", request, want)
	}
	if message, err := extmsg.Parse(request); err != nil || message.SessionKind != extmsg.SessionReview || message.LineNote != "why" || message.Base() != "main" {
		t.Fatalf("the editor reads %q as %+v, %v", request, message, err)
	}
}

func TestSessionFromOptions(t *testing.T) {
	if session, err := sessionFromOptions(msgOptions{diffSession: "d1"}); err != nil || session != (msgSession{id: "d1", kind: extmsg.SessionDiff}) {
		t.Fatalf("diff session = %+v, %v", session, err)
	}
	if session, err := sessionFromOptions(msgOptions{session: "n1"}); err != nil || session != (msgSession{id: "n1", kind: extmsg.SessionNote}) {
		t.Fatalf("note session = %+v, %v", session, err)
	}
	if session, err := sessionFromOptions(msgOptions{reviewSession: "r1"}); err != nil || session != (msgSession{id: "r1", kind: extmsg.SessionReview}) {
		t.Fatalf("review session = %+v, %v", session, err)
	}
	if session, err := sessionFromOptions(msgOptions{}); err != nil || session != (msgSession{}) {
		t.Fatalf("no session = %+v, %v", session, err)
	}
	for _, options := range []msgOptions{{session: "n1", diffSession: "d1"}, {diffSession: "d1", reviewSession: "r1"}} {
		if _, err := sessionFromOptions(options); err == nil || !strings.Contains(err.Error(), "use one of") {
			t.Fatalf("two flags %+v = %v", options, err)
		}
	}
}

func TestCheckSessionCommand(t *testing.T) {
	note, diff := msgSession{id: "walk1", kind: extmsg.SessionNote}, msgSession{id: "walk1", kind: extmsg.SessionDiff}
	review := msgSession{id: "walk1", kind: extmsg.SessionReview}
	cases := []struct {
		command string
		session msgSession
		base    string
		paths   []string
		wantErr string
	}{
		{"open", note, "", []string{"a.go#L3"}, ""},
		{"open", note, "HEAD~1", []string{"a.go#L3"}, "notes only"},
		{"open", diff, "HEAD~1", []string{"a.go#L3"}, ""},
		{"open", diff, "", []string{"a.go#L3"}, ""},
		{"wait", note, "", nil, ""},
		{"end", diff, "", nil, ""},
		{"wait", msgSession{}, "", nil, "needs --msg-session"},
		{"end", note, "", []string{"a.go"}, "takes no paths"},
		{"end", diff, "", []string{"a.go"}, "--msg-diff-session walk1 end"},
		{"review", note, "", []string{"a.go"}, "goes with open"},
		{"review", diff, "", []string{"a.go"}, "--msg-diff-session goes with open"},
		{"review", review, "main", []string{"a.go"}, ""},
		{"review", review, "", []string{"a.go", "b.go"}, "one file per review, got 2"},
		{"review", review, "", nil, "one file per review, got 0"},
		{"open", review, "", []string{"a.go#L3"}, "--msg-review-session goes with review"},
		{"end", review, "", nil, ""},
	}
	for _, c := range cases {
		err := checkSessionCommand(c.command, c.paths, c.session, c.base)
		if (c.wantErr == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), c.wantErr)) {
			t.Errorf("checkSessionCommand(%q, %v, %+v, %q) = %v, want %q", c.command, c.paths, c.session, c.base, err, c.wantErr)
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
			return "ok finished"
		case strings.HasPrefix(text, "next"):
			return "ok step 2 queued"
		case strings.HasPrefix(text, "finished"):
			return "ok finished — session walk1 was finished — the user went through all 3 steps"
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
		session  msgSession
		wantCode int
		wantText string
	}{
		{"next\n", msgSession{id: "walk1", kind: extmsg.SessionNote}, 0, "ok step 2 queued"},
		{"finished\n", msgSession{id: "walk1", kind: extmsg.SessionNote}, 0, "ok finished"},
		{"ended\n", msgSession{id: "walk1", kind: extmsg.SessionNote}, exitSessionEnded, "ended session walk1"},
		{"waiting\n", msgSession{id: "walk1", kind: extmsg.SessionNote}, exitSessionWaiting, "ok waiting"},
		{"bad\n", msgSession{}, 1, "error nope"},
		{"slow\n", msgSession{id: "walk1", kind: extmsg.SessionNote}, exitSessionWaiting, "--msg-session walk1 wait"},
		{"slow\n", msgSession{id: "diff1", kind: extmsg.SessionDiff}, exitSessionWaiting, "--msg-diff-session diff1 wait"},
		{"slow\n", msgSession{id: "rev1", kind: extmsg.SessionReview}, exitSessionWaiting, "--msg-review-session rev1 wait"},
	}
	for _, c := range cases {
		output, code := deliverRequest(socketPath, c.request, c.session, 200*time.Millisecond)
		if code != c.wantCode || !strings.Contains(output, c.wantText) {
			t.Errorf("%q → %q exit %d, want %q exit %d", c.request, output, code, c.wantText, c.wantCode)
		}
	}
}

func TestDeliverWaitSendsItsBudgetAndFallsBackForAnOlderEditor(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "nteecli")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	session := msgSession{id: "walk1", kind: extmsg.SessionNote}

	for _, olderEditor := range []bool{false, true} {
		socketPath := filepath.Join(dir, "s.sock")
		var received []string
		server, err := extmsg.Listen(socketPath, func(text string) string {
			received = append(received, strings.TrimSpace(text))
			if strings.Contains(text, extmsg.WaitOption) && olderEditor {
				// What an editor that predates the budget answers.
				return extmsg.ErrorReply("unknown option --wait=5s — the options are …")
			}
			return "ok finished"
		})
		if err != nil {
			t.Fatal(err)
		}
		output, code := deliverWait(socketPath, session, 5*time.Second)
		server.Close()
		want := []string{"wait --session=walk1 --wait=5s"}
		if olderEditor {
			want = append(want, "wait --session=walk1")
		}
		if code != 0 || output != "ok finished" || strings.Join(received, "|") != strings.Join(want, "|") {
			t.Fatalf("older editor %v: %q exit %d, requests %q", olderEditor, output, code, received)
		}
	}
}
