package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/nickooan/ntee-editor/internal/extmsg"
)

// hookTimeout bounds the editor check: a hook runs before every matched
// tool call, so a slow or missing editor must never hold the agent up.
const hookTimeout = 2 * time.Second

// maxHookPayloadBytes bounds the PreToolUse payload read. It carries the
// tool's whole input — a Write's full content — so it is far above the
// socket's message cap: a cut payload does not parse and is allowed.
const maxHookPayloadBytes = 64 << 20

// hookInput is the part of Claude Code's PreToolUse payload the hook reads.
type hookInput struct {
	ToolName  string `json:"tool_name"`
	Cwd       string `json:"cwd"`
	ToolInput struct {
		FilePath string `json:"file_path"`
	} `json:"tool_input"`
}

type hookOutput struct {
	HookSpecificOutput struct {
		HookEventName            string `json:"hookEventName"`
		PermissionDecision       string `json:"permissionDecision"`
		PermissionDecisionReason string `json:"permissionDecisionReason"`
	} `json:"hookSpecificOutput"`
}

// runHook is --hook: a Claude Code PreToolUse hook that stops the built-in
// file tools on a file with staged edits, which they cannot see. Anything
// it cannot decide — no editor, a bad payload, a file outside the project —
// is allowed, silently. It always exits 0.
func runHook(editorRoot string, stdin io.Reader, stdout io.Writer) int {
	data, err := readHookPayload(stdin)
	if err != nil {
		return 0
	}
	output := hookDecision(data, func(path string) string { return stagedSession(editorRoot, path) })
	if output != "" {
		io.WriteString(stdout, output+"\n")
	}
	return 0
}

func readHookPayload(stdin io.Reader) ([]byte, error) {
	return io.ReadAll(io.LimitReader(stdin, maxHookPayloadBytes))
}

// hookDecision is the hook's answer to one payload: a deny when the tool
// touches a file whose staged edits stagedIn names, else "" (allow).
func hookDecision(payload []byte, stagedIn func(path string) string) string {
	var input hookInput
	if json.Unmarshal(payload, &input) != nil || input.ToolInput.FilePath == "" {
		return ""
	}
	var use string
	switch input.ToolName {
	case "Read":
		use = "read it with mcp__ntee__read_file, which shows them"
	case "Edit", "MultiEdit":
		use = "read it with mcp__ntee__read_file, then stage your change with mcp__ntee__edit_replace so it builds on them"
	case "Write":
		use = "stage the new content with mcp__ntee__edit_write so it builds on them"
	default:
		return ""
	}
	path := input.ToolInput.FilePath
	if !filepath.IsAbs(path) && input.Cwd != "" {
		path = filepath.Join(input.Cwd, path)
	}
	session := stagedIn(path)
	if session == "" {
		return ""
	}
	var output hookOutput
	output.HookSpecificOutput.HookEventName = "PreToolUse"
	output.HookSpecificOutput.PermissionDecision = "deny"
	output.HookSpecificOutput.PermissionDecisionReason = filepath.Base(path) + " has edits staged in the ntee editor (edit session " + session +
		") that are not on disk yet — " + input.ToolName + " would not see them; " + use + "."
	encoded, _ := json.Marshal(output) // strings only: always marshals
	return string(encoded)
}

// stagedSession asks the running editor which session has staged edits to
// path, "" for none or when there is no editor to ask.
func stagedSession(editorRoot, path string) string {
	socketPath, err := findEditorSocket(editorRoot, []string{path})
	if err != nil {
		return ""
	}
	request, err := extmsg.EditRequest(extmsg.CommandStaged, "", "", path, "")
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), hookTimeout)
	defer cancel()
	line, _, _, err := extmsg.SendEdit(ctx, socketPath, extmsg.CommandStaged, request, hookTimeout)
	if err != nil {
		return ""
	}
	session, _ := extmsg.StagedReplySession(line)
	return session
}

// runHookFromStdin is runHook on the process's stdin and stdout.
func runHookFromStdin(editorRoot string) int {
	return runHook(editorRoot, os.Stdin, os.Stdout)
}
