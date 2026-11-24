package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nickooan/ntee-editor/internal/extmsg"
	"github.com/nickooan/ntee-editor/internal/store"
)

func hookPayload(tool, path, cwd string) []byte {
	payload, _ := json.Marshal(map[string]any{
		"hook_event_name": "PreToolUse", "tool_name": tool, "cwd": cwd,
		"tool_input": map[string]any{"file_path": path},
	})
	return payload
}

func TestHookDeniesFileToolsOnStagedFiles(t *testing.T) {
	asked := ""
	stagedIn := func(path string) string {
		asked = path
		if strings.HasSuffix(path, "main.go") {
			return "A"
		}
		return ""
	}
	output := hookDecision(hookPayload("Read", "main.go", "/work/p"), stagedIn)
	var decoded hookOutput
	if err := json.Unmarshal([]byte(output), &decoded); err != nil {
		t.Fatalf("output is JSON: %q", output)
	}
	if asked != "/work/p/main.go" {
		t.Fatalf("a relative path is taken from cwd: %q", asked)
	}
	decision := decoded.HookSpecificOutput
	if decision.HookEventName != "PreToolUse" || decision.PermissionDecision != "deny" ||
		!strings.Contains(decision.PermissionDecisionReason, "main.go has edits staged in the ntee editor (edit session A)") ||
		!strings.Contains(decision.PermissionDecisionReason, "mcp__ntee__read_file") {
		t.Fatalf("decision = %+v", decision)
	}
	if output := hookDecision(hookPayload("Edit", "/work/p/main.go", ""), stagedIn); !strings.Contains(output, "mcp__ntee__edit_replace") {
		t.Fatalf("an edit is pointed at edit_replace: %q", output)
	}
	if output := hookDecision(hookPayload("Write", "/work/p/main.go", ""), stagedIn); !strings.Contains(output, "mcp__ntee__edit_write") {
		t.Fatalf("a write is pointed at edit_write: %q", output)
	}
	for _, allowed := range [][]byte{
		hookPayload("Read", "/work/p/other.go", ""),
		hookPayload("Bash", "/work/p/main.go", ""),
		hookPayload("Read", "", ""),
		[]byte("not json"),
	} {
		if output := hookDecision(allowed, stagedIn); output != "" {
			t.Fatalf("%s is allowed silently: %q", allowed, output)
		}
	}
}

func TestHookAsksTheRunningEditorAndAllowsWithoutOne(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "nteehook")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	t.Setenv("HOME", home)
	project := t.TempDir()

	var stdout bytes.Buffer
	if code := runHook(project, bytes.NewReader(hookPayload("Read", filepath.Join(project, "main.go"), "")), &stdout); code != 0 || stdout.Len() != 0 {
		t.Fatalf("no editor: allow (exit %d, %q)", code, stdout.String())
	}

	socketPath, err := store.ExternalSocketPath(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		t.Fatal(err)
	}
	requests := make(chan string, 1)
	server, err := extmsg.Listen(socketPath, func(text string) string {
		requests <- text
		return "ok staged · edit session A"
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	stdout.Reset()
	if code := runHook(project, bytes.NewReader(hookPayload("Read", filepath.Join(project, "main.go"), "")), &stdout); code != 0 || !strings.Contains(stdout.String(), `"permissionDecision":"deny"`) {
		t.Fatalf("a staged file is denied (exit %d, %q)", code, stdout.String())
	}
	if request := <-requests; request != "staged\n"+filepath.Join(project, "main.go")+"\n" {
		t.Fatalf("the hook asks staged: %q", request)
	}
}

func TestHookWithAnEditorThatHasNotStagedThePath(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "nteehook")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	t.Setenv("HOME", home)
	project := t.TempDir()
	socketPath, err := store.ExternalSocketPath(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		t.Fatal(err)
	}
	var reply atomic.Value
	server, err := extmsg.Listen(socketPath, func(string) string { return reply.Load().(string) })
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	for _, testCase := range []struct{ name, reply string }{
		{"should allow the tool when the editor refuses a path outside the project", extmsg.ErrorReply("path outside the project: ../notes.md")},
		{"should allow the tool when the editor has no staged edits for the file", extmsg.OkReply("not staged")},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			reply.Store(testCase.reply)
			var stdout bytes.Buffer
			if code := runHook(project, bytes.NewReader(hookPayload("Read", filepath.Join(home, "notes.md"), "")), &stdout); code != 0 || stdout.Len() != 0 {
				t.Fatalf("exit %d, %q", code, stdout.String())
			}
		})
	}
}

func TestHookDeniesAWriteLargerThanASocketMessage(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{
		"hook_event_name": "PreToolUse", "tool_name": "Write", "cwd": "/work/p",
		"tool_input": map[string]any{"file_path": "main.go", "content": strings.Repeat("x", 2*extmsg.MaxMessageBytes)},
	})
	read, err := readHookPayload(bytes.NewReader(payload))
	if err != nil || len(read) != len(payload) {
		t.Fatalf("the whole payload is read: %d of %d bytes, %v", len(read), len(payload), err)
	}
	if output := hookDecision(read, func(string) string { return "A" }); !strings.Contains(output, `"deny"`) {
		t.Fatalf("a big Write to a staged file is still denied: %q", output)
	}
}
