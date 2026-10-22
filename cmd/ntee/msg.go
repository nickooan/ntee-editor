package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nickooan/ntee-editor/internal/extmsg"
	"github.com/nickooan/ntee-editor/internal/store"
)

// msgHelp is the --msg guide, written for coding agents as much as people:
// what each request does in the editor, and what comes back.
const msgHelp = `ntee --msg — send a request to a running ntee editor

  ntee --msg open <file>[#L<line>]
      Open one file in edit mode, cursor on the line. If the user is busy
      (editing, in a special view) they are asked first; the file may switch
      the editor to the nested git repo that contains it.

  ntee --msg review <file>...
  git diff --name-only | ntee --msg review
      Open a read-only review of the files: a list on the left, a git-diff
      view on the right (green added, red removed; new files all green,
      deleted files all red). Paths may span nested repos of the workspace,
      and deleted files are fine. Diffs are against HEAD unless
      --msg-base <rev> comes before "review".

Paths are relative to the current directory or absolute, and must be inside
the folder the editor opened. The editor is found by walking up from the
current directory; --msg-root <dir> names it instead.

External messages are off by default: the user turns them on in ntee under
Ctrl+T › external. The editor keeps the latest messages (10 by default) and
can replay them from there.

Reply (one line on stdout):
  ok <detail>       accepted — "opened …", "reviewing N files", or
                    "waiting for the user to confirm"
  error <reason>    refused (disabled, path outside the project, …); the
                    exit code is 1

Typical agent use, after changing files:
  ntee --msg review $(git diff --name-only HEAD)
`

// sendMessage is the --msg client: it resolves the paths, finds the running
// editor, sends one request, and prints the reply. It returns the exit code.
func sendMessage(args []string, editorRoot, base string) int {
	if len(args) == 0 || args[0] == "help" {
		fmt.Print(msgHelp)
		return 0
	}
	command, paths := strings.ToLower(args[0]), args[1:]
	if command == extmsg.CommandReview && len(paths) == 0 {
		var err error
		if paths, err = readPathsFromStdin(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	if base != "" && command != extmsg.CommandReview {
		fmt.Fprintln(os.Stderr, "--msg-base only applies to review")
		return 1
	}
	for i, path := range paths {
		// A "#L<n>" anchor is plain filename text to Abs, so it rides along.
		absolute, err := filepath.Abs(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		paths[i] = absolute
	}

	socketPath, err := findEditorSocket(editorRoot, paths)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	header := command
	if base != "" {
		header += " " + base
	}
	reply, err := extmsg.Send(socketPath, header+"\n"+strings.Join(paths, "\n")+"\n")
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot reach the editor:", err)
		return 1
	}
	fmt.Println(reply)
	if extmsg.IsErrorReply(reply) {
		return 1
	}
	return 0
}

// findEditorSocket is the socket of editorRoot when given, else of the
// nearest running editor above the current directory, then above the first
// path (the sender may run from outside the project).
func findEditorSocket(editorRoot string, paths []string) (string, error) {
	if editorRoot != "" {
		return store.ExternalSocketPath(editorRoot)
	}
	starts := []string{"."}
	if len(paths) > 0 {
		starts = append(starts, filepath.Dir(paths[0]))
	}
	for _, start := range starts {
		if _, socketPath, err := extmsg.Discover(start, store.ExternalSocketPath); err == nil {
			return socketPath, nil
		}
	}
	return "", extmsg.ErrNoEditor
}

// readPathsFromStdin takes one path per line from a pipe; an interactive
// terminal gets the usage instead of a silent wait.
func readPathsFromStdin() ([]string, error) {
	info, err := os.Stdin.Stat()
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeCharDevice != 0 {
		return nil, errors.New("usage: ntee --msg review <file>... (or pipe paths on stdin)")
	}
	var paths []string
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); line != "" {
			paths = append(paths, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, errors.New("review: no paths on stdin")
	}
	return paths, nil
}
