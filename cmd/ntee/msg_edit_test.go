package main

import (
	"strings"
	"testing"

	"github.com/nickooan/ntee-editor/internal/extmsg"
)

func TestSplitReplaceInput(t *testing.T) {
	cases := []struct {
		input, separator, oldText, newText, wantErr string
	}{
		// A heredoc: the lines come out as written.
		{input: "func a() {\n\treturn 1\n}\n<<< :new\nfunc a() {\n\treturn 2\n}\n", separator: "<<< :new",
			oldText: "func a() {\n\treturn 1\n}", newText: "func a() {\n\treturn 2\n}"},
		// Spaces around the separator line are ignored.
		{input: "a\n   <<< :new  \nb\n", separator: "<<< :new", oldText: "a", newText: "b"},
		// An empty new text deletes; a blank line before the separator keeps
		// the old text's newline.
		{input: "gone\n\n<<< :new\n", separator: "<<< :new", oldText: "gone\n", newText: ""},
		{input: "a\r\n<<< :new\r\nb\r\n", separator: "<<< :new", oldText: "a", newText: "b"},
		{input: "a\n=====\nb\n", separator: "=====", oldText: "a", newText: "b"},
		{input: "a\nb\n", separator: "<<< :new", wantErr: "no \"<<< :new\" line"},
		{input: "a\n<<< :new\nb\n<<< :new\nc\n", separator: "<<< :new", wantErr: "2 \"<<< :new\" lines"},
		{input: "<<< :new\nb\n", separator: "<<< :new", wantErr: "the old text is empty"},
	}
	for _, test := range cases {
		oldText, newText, err := splitReplaceInput(test.input, test.separator)
		if test.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("splitReplaceInput(%q) error = %v, want %q", test.input, err, test.wantErr)
			}
			continue
		}
		if err != nil || oldText != test.oldText || newText != test.newText {
			t.Fatalf("splitReplaceInput(%q) = %q, %q, %v", test.input, oldText, newText, err)
		}
	}
}

func TestCheckEditSessionCommands(t *testing.T) {
	edit := msgSession{id: "e1", kind: extmsg.SessionEdit}
	for _, test := range []struct {
		command string
		paths   []string
		base    string
		wantErr string
	}{
		{command: "write", paths: []string{"a.go"}},
		{command: "replace", paths: []string{"a.go#L3"}},
		{command: "show", paths: []string{"a.go"}},
		{command: "status"},
		{command: "discard"},
		{command: "end"},
		{command: "wait"},
		{command: "write", wantErr: "exactly one file"},
		{command: "status", paths: []string{"a.go"}, wantErr: "takes no paths"},
		{command: "review", paths: []string{"a.go"}, wantErr: "goes with write, replace"},
		{command: "write", paths: []string{"a.go"}, base: "HEAD", wantErr: "--msg-base does not go"},
	} {
		err := checkSessionCommand(test.command, test.paths, edit, test.base)
		if test.wantErr == "" && err != nil {
			t.Fatalf("%s %v: %v", test.command, test.paths, err)
		}
		if test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)) {
			t.Fatalf("%s %v: error = %v, want %q", test.command, test.paths, err, test.wantErr)
		}
	}
	if err := checkSessionCommand("write", []string{"a.go"}, msgSession{}, ""); err == nil || !strings.Contains(err.Error(), "goes with an edit session") {
		t.Fatalf("write without an edit session: %v", err)
	}
	if _, err := sessionFromOptions(msgOptions{editSession: "e1", reviewSession: "r1"}); err == nil {
		t.Fatal("one session flag at a time")
	}
	if session, err := sessionFromOptions(msgOptions{editSession: "e1"}); err != nil || session.kind != extmsg.SessionEdit || session.option() != "--edit-session=e1" {
		t.Fatalf("edit session flag = %+v %v", session, err)
	}
}

func TestMsgHelpExplainsEditSessions(t *testing.T) {
	for _, want := range []string{
		"ntee --msg --msg-edit-session <id> replace <file>[#L<line>] <<'EOF'",
		"<<< :new",
		"--msg-replace-all",
		"Ctrl+T › agent edits",
		"Builds and tests\n      see staged edits only once they are applied",
	} {
		if !strings.Contains(msgHelp, want) {
			t.Fatalf("msg help is missing %q", want)
		}
	}
}
