package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/nickooan/ntee-editor/internal/extmsg"
)

// The waits come from extmsg, shared with the MCP tools.
const (
	editEndTimeout     = extmsg.EditEndTimeout
	editSandboxTimeout = extmsg.EditSandboxTimeout
	editRequestTimeout = extmsg.EditRequestTimeout
)

// checkEditCommand checks an edit session's command and paths: write,
// replace and show name one file, the rest none; no base revision.
func checkEditCommand(command string, paths []string, session msgSession, base string) error {
	if base != "" {
		return errors.New("--msg-base does not go with an edit session")
	}
	switch command {
	case extmsg.CommandWrite, extmsg.CommandReplace, extmsg.CommandShow, extmsg.CommandRead, extmsg.CommandStaged:
		if len(paths) != 1 {
			return fmt.Errorf("%s needs exactly one file, got %d", command, len(paths))
		}
	case extmsg.CommandSandbox:
		if len(paths) > 0 && paths[0] == extmsg.SandboxDrop && len(paths) > 1 {
			return errors.New("sandbox drop takes no folders")
		}
	case extmsg.CommandCheck:
		if len(paths) != 1 || paths[0] != extmsg.CheckPass && paths[0] != extmsg.CheckFail && paths[0] != extmsg.CheckSkip {
			return errors.New("check takes one result: pass, fail or skip (say what you ran with --msg-note)")
		}
	case extmsg.CommandStatus, extmsg.CommandDiscard, extmsg.CommandEnd, extmsg.CommandWait:
		if len(paths) > 0 {
			return fmt.Errorf("%s takes no paths: ntee --msg %s %s %s", command, session.flag(), session.id, command)
		}
	default:
		return errors.New("--msg-edit-session goes with write, replace, show, read, staged, status, discard, sandbox, check, end and wait")
	}
	return nil
}

// sendEditCommand sends one edit session request (end and wait go the
// session way), or a read or staged with or without a session, and prints
// the reply; show, read and status print the editor's content as is — a
// read says on stderr where it came from. It returns the exit code.
func sendEditCommand(command string, paths []string, session msgSession, options msgOptions) int {
	if options.replaceAll && command != extmsg.CommandReplace {
		fmt.Fprintln(os.Stderr, "--msg-replace-all goes with replace")
		return 1
	}
	mode := ""
	switch {
	case command == extmsg.CommandSandbox && len(paths) > 0 && (paths[0] == extmsg.SandboxFresh || paths[0] == extmsg.SandboxDrop):
		mode, paths = paths[0], paths[1:]
	case command == extmsg.CommandCheck:
		mode, paths = paths[0], nil // the result, not a path
	}
	for index, path := range paths {
		absolute, err := filepath.Abs(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		paths[index] = absolute
	}
	path, payload := "", ""
	if len(paths) > 0 {
		path = paths[0]
	}
	switch command {
	case extmsg.CommandWrite:
		content, err := readEditInput(os.Stdin, "usage: ntee --msg --msg-edit-session <id> write <file> < new-content (pipe the content on stdin)")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		payload = content
	case extmsg.CommandReplace:
		input, err := readEditInput(os.Stdin, "usage: ntee --msg --msg-edit-session <id> replace <file> <<'EOF' (old text, a \""+options.separator+"\" line, new text) EOF")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		oldText, newText, err := splitReplaceInput(input, options.separator)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		payload = extmsg.EncodeReplace(extmsg.ReplacePayload{Old: oldText, New: newText, All: options.replaceAll})
	}
	request, err := extmsg.EditRequest(command, session.id, options.agent, path, payload)
	if options.expect != "" && command != extmsg.CommandWrite && command != extmsg.CommandReplace {
		fmt.Fprintln(os.Stderr, "--msg-expect goes with write or replace")
		return 1
	}
	if command == extmsg.CommandWrite || command == extmsg.CommandReplace {
		request = extmsg.WithExpect(request, options.expect)
	}
	timeout := editRequestTimeout
	switch command {
	case extmsg.CommandSandbox:
		request, timeout = extmsg.SandboxRequest(session.id, mode, paths), editSandboxTimeout
	case extmsg.CommandCheck:
		request = extmsg.CheckRequest(session.id, options.agent, mode, options.note)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	socketPath, err := findEditorSocket(options.root, paths)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	line, body, hasBody, err := extmsg.SendEdit(context.Background(), socketPath, command, request, timeout)
	switch {
	case err != nil:
		fmt.Fprintln(os.Stderr, "cannot reach the editor: "+err.Error())
		return 1
	case hasBody:
		if detail := extmsg.BodyDetail(line); detail != "" {
			fmt.Fprintln(os.Stderr, "ntee: "+detail)
		}
		fmt.Print(body)
		return 0
	}
	fmt.Println(line)
	return exitCodeForReply(line)
}

// readEditInput reads an edit's stdin whole; an interactive terminal gets
// the usage instead of a silent wait.
func readEditInput(stdin *os.File, usage string) (string, error) {
	info, err := stdin.Stat()
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeCharDevice != 0 {
		return "", errors.New(usage)
	}
	text, err := io.ReadAll(io.LimitReader(stdin, extmsg.MaxMessageBytes+1))
	if err != nil {
		return "", err
	}
	if len(text) > extmsg.MaxMessageBytes {
		return "", fmt.Errorf("stdin is too large (max %d MiB per request) — stage the file in smaller replaces", extmsg.MaxMessageBytes>>20)
	}
	return string(text), nil
}

// splitReplaceInput splits a replace's stdin at its one separator line into
// the old and the new text. The newline before the separator line, its own
// newline, and one final newline belong to neither text — so a heredoc's
// lines come out as written.
func splitReplaceInput(input, separator string) (oldText, newText string, err error) {
	separator = strings.TrimSpace(separator)
	if separator == "" {
		return "", "", errors.New("--msg-separator is empty")
	}
	lineStart, separatorStart, separatorEnd, found := 0, 0, 0, 0
	for lineStart <= len(input) {
		lineEnd := strings.IndexByte(input[lineStart:], '\n')
		next := len(input) + 1
		if lineEnd < 0 {
			lineEnd = len(input)
		} else {
			lineEnd += lineStart
			next = lineEnd + 1
		}
		if strings.TrimSpace(input[lineStart:lineEnd]) == separator {
			found++
			separatorStart, separatorEnd = lineStart, min(next, len(input))
		}
		lineStart = next
	}
	switch found {
	case 0:
		return "", "", fmt.Errorf("replace: no %q line on stdin — send the old text, a %q line, then the new text", separator, separator)
	case 1:
	default:
		return "", "", fmt.Errorf("replace: %d %q lines on stdin — there must be exactly one; if the text holds that line, pick another with --msg-separator", found, separator)
	}
	oldText = trimOneNewline(input[:separatorStart])
	newText = trimOneNewline(input[separatorEnd:])
	if oldText == "" {
		return "", "", errors.New("replace: the old text is empty — use write to set the whole file")
	}
	return oldText, newText, nil
}

func trimOneNewline(text string) string {
	if trimmed, ok := strings.CutSuffix(text, "\r\n"); ok {
		return trimmed
	}
	return strings.TrimSuffix(text, "\n")
}
