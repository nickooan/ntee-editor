package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nickooan/ntee-editor/internal/app"
	"github.com/nickooan/ntee-editor/internal/extmsg"
	"github.com/nickooan/ntee-editor/internal/store"
)

// msgHelp is the --msg guide, written for coding agents as much as people:
// what each request does in the editor, and what comes back.
const msgHelp = `ntee --msg — send a request to a running ntee editor

  ntee --msg open <file>[#L<line>]
  ntee --msg --msg-base <rev> open <file>[#L<line>]
      Open one file in edit mode, cursor on the line. If the user is busy
      (editing, in a special view) they are asked first; the file may switch
      the editor to the nested git repo that contains it. With --msg-base the
      file opens in the diff view against <rev> instead.

  ntee --msg --msg-note "<text>" open <file>#L<line>
  ntee --msg --msg-note - open <file>#L<line> <<'EOF'
  <note, any number of lines>
  EOF
      Open the file as above and show the note in a box next to the line,
      to explain it. The #L<line> anchor is required with a note. "-" reads
      the note from stdin. A tall note scrolls with Shift+Up/Down or the
      mouse wheel; the user closes the box with Esc and keeps editing; it
      also closes on the first edit. Notes are capped at 16 KiB. On the wire
      the note follows the path after a line that is exactly "---".

      Note formatting (light markdown):
        # Heading            any level (#–######), shown bold
        - item / * / +       bullet "•", wrapped lines hang under the text
        1. item / 1) item    numbered item, same hanging indent
        > text               quote, dimmed italic
        ` + "`code`" + `               inline code, highlighted
        **bold**             bold
        ` + "```" + ` … ` + "```" + `            fenced block on its own lines: shown as is on
                             a dark background, cut (not word-wrapped) at the
                             box edge
        | a | b |            table: a header row, a |---|---| row, then
        |---|---|            rows; columns line up, and when the box is too
        | 1 | 2 |            narrow the widest cells wrap (a cell keeps a
                             literal pipe as \|)
        blank line           paragraph break
      Line breaks and leading indentation are kept (a tab is the editor's
      all-files tab size, 4 by default), and long lines word-wrap to the
      box. Anything else — links, _italics_, HTML — shows as plain text,
      markers included.

  ntee --msg review <file>...
  git diff --name-only | ntee --msg review
      Open a read-only review of the files: a list on the left, a git-diff
      view on the right (green added, red removed; new files all green,
      deleted files all red). Paths may span nested repos of the workspace,
      and deleted files are fine. Diffs are against HEAD unless
      --msg-base <rev> comes before "review".

Guided sessions — walk the user through code, one note at a time:

  ntee --msg --msg-session <id> --msg-note "<text>" open <file>#L<line>
  ntee --msg --msg-session <id> wait
  ntee --msg --msg-session <id> end
      Every step is an open with a note, sent with the same session id
      (1–64 letters, digits, '.', '_', '-'; pick a new one per walkthrough).
      The first step asks the user to start the session; after that each
      step jumps straight there — other files, other nested repos — without
      asking. On each step the user presses Enter for the next step or Esc to
      end the session, and the file is read-only while the step shows.
      The command waits for that answer (up to --msg-wait, default 10m):
        ok next       exit 0  the user wants the next step: send it
        ended …       exit 3  the user ended or declined the session, or it
                              was already over: stop sending steps
        ok waiting …  exit 4  no answer yet: run the wait command, which
                              returns the answer once the user gives it
        error …       exit 1  the step could not be shown (bad path, another
                              session running, …)
      After the last step, send end to close the session.

  A walkthrough an agent might run:
    ntee --msg --msg-session tour1 --msg-note - open cmd/ntee/main.go#L40 <<'EOF'
    ## Flags
    Everything starts here.
    EOF
    # exit 0 → next step; exit 3 → stop
    ntee --msg --msg-session tour1 --msg-note "the request goes out here" open cmd/ntee/msg.go#L180
    ntee --msg --msg-session tour1 end

  On the wire the session is a first-line option: "open --session=<id>",
  "wait --session=<id>", "end --session=<id>".

A base revision (a commit hash, tag, branch, HEAD~2, …) is always compared
with the current files on disk, uncommitted changes included. It travels on
the request's first line ("open <rev>" / "review <rev>"), and the editor's
history saves it (HEAD when none was given).

Paths are relative to the current directory or absolute, and must be inside
the folder the editor opened. The editor is found by walking up from the
current directory, then from the first path's folder, so an absolute path
works from anywhere; --msg-root <dir> names it instead.

When the editor opened a folder holding several git repos and works in one
of them (Ctrl+W), an open of a file in another nested repo switches the
editor to that repo (a file in none of them switches back to the whole
folder); the user confirms first when busy:
  ntee --msg open /work/ws/libs/core/lib.go#L12   # editor in /work/ws/apps/web

The --msg-* flags go before the command:
  ntee --msg --msg-note "…" open <file>#L<line>
  ntee --msg --msg-session tour1 --msg-note "…" open <file>#L<line>

External messages are off by default: the user turns them on in ntee under
Ctrl+T › external. The editor keeps the latest messages (10 by default) and
can replay them from there.

Reply (one line on stdout):
  ok <detail>       accepted — "opened …", "reviewing N files", or
                    "waiting for the user to confirm"; for a session step
                    "next" (exit 0) or "waiting …" (exit 4)
  ended <reason>    a session is over — stop sending steps; the exit code
                    is 3
  error <reason>    refused (disabled, path outside the project, …); the
                    exit code is 1

Typical agent use, after changing files:
  ntee --msg review $(git diff --name-only HEAD)
  ntee --msg --msg-note "this guard is why the cache stays fresh" open internal/app/render.go#L571

Teach a coding agent once — paste this rule into the project's CLAUDE.md or
AGENTS.md:

  ## Showing changes in ntee
  The user edits in ntee-editor. After changing files, send them for review:
  ` + "`ntee --msg review <changed files…>`" + ` (or pipe ` + "`git diff --name-only`" + `).
  To point at a specific place: ` + "`ntee --msg open <file>#L<line>`" + `; to explain
  it, add a note: ` + "`ntee --msg --msg-note \"<why>\" open <file>#L<line>`" + ` (markdown
  is fine; ` + "`--msg-note -`" + ` reads a longer note from stdin).
  To walk the user through several places, send each as a step of one
  session: ` + "`ntee --msg --msg-session <id> --msg-note \"<why>\" open <file>#L<line>`" + `;
  exit 0 means show the next step, exit 3 means the user ended it — stop;
  finish with ` + "`ntee --msg --msg-session <id> end`" + `.
  Paths are relative to the current directory. The command prints ` + "`ok …`" + ` on
  success; ` + "`error … disabled`" + ` means the user has not enabled external messages
  (Ctrl+T → external) — mention it once and carry on. Run ` + "`ntee --msg help`" + `
  for details.
`

// msgOptions are the --msg-* flags.
type msgOptions struct {
	root    string        // --msg-root: the editor's folder
	base    string        // --msg-base: the revision to compare against
	note    string        // --msg-note: the note text, "-" = stdin
	session string        // --msg-session: the guided session's id
	wait    time.Duration // --msg-wait: how long a session request waits for the user
}

// Exit codes beyond 0 (ok) and 1 (error) for guided sessions.
const (
	exitSessionEnded   = 3 // the session is over: stop sending steps
	exitSessionWaiting = 4 // the user hasn't answered yet: run wait
)

// sendMessage is the --msg client: it resolves the paths, finds the running
// editor, sends one request, and prints the reply. It returns the exit code.
func sendMessage(args []string, options msgOptions) int {
	if len(args) == 0 || args[0] == "help" {
		fmt.Print(msgHelp)
		return 0
	}
	command, paths := strings.ToLower(args[0]), args[1:]
	note := options.note
	if err := checkSessionCommand(command, paths, options.session); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if note == "-" {
		var err error
		if note, err = readNoteFromStdin(os.Stdin); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	if err := checkLineNote(command, paths, note); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if command == extmsg.CommandReview && len(paths) == 0 {
		var err error
		if paths, err = readPathsFromStdin(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
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

	socketPath, err := findEditorSocket(options.root, paths)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	output, code := deliverRequest(socketPath, buildRequest(command, options.base, options.session, paths, note), options.session, options.wait)
	if code == 1 && !extmsg.IsErrorReply(output) {
		fmt.Fprintln(os.Stderr, output)
		return code
	}
	fmt.Println(output)
	return code
}

// deliverRequest sends a request and turns the reply into the printed line
// and the exit code. A session request waits for the user up to wait; when
// that runs out first, the agent is told how to keep waiting.
func deliverRequest(socketPath, request, session string, wait time.Duration) (string, int) {
	timeout := 10 * time.Second
	if session != "" {
		timeout = wait
	}
	reply, err := extmsg.SendWithTimeout(socketPath, request, timeout)
	switch {
	case err != nil && session != "" && os.IsTimeout(err):
		return "ok waiting — the user hasn't answered yet; run: ntee --msg --msg-session " + session + " wait", exitSessionWaiting
	case err != nil:
		return "cannot reach the editor: " + err.Error(), 1
	}
	return reply, exitCodeForReply(reply)
}

// exitCodeForReply maps a reply line to the exit code an agent branches on.
func exitCodeForReply(reply string) int {
	switch {
	case extmsg.IsErrorReply(reply):
		return 1
	case extmsg.IsEndedReply(reply):
		return exitSessionEnded
	case strings.HasPrefix(reply, "ok waiting"):
		return exitSessionWaiting
	}
	return 0
}

// checkSessionCommand checks the session flag against the command: end and
// wait act on a session and take no paths; review takes none.
func checkSessionCommand(command string, paths []string, session string) error {
	switch command {
	case extmsg.CommandEnd, extmsg.CommandWait:
		if session == "" {
			return fmt.Errorf("%s needs --msg-session <id>", command)
		}
		if len(paths) > 0 {
			return fmt.Errorf("%s takes no paths: ntee --msg --msg-session %s %s", command, session, command)
		}
	case extmsg.CommandReview:
		if session != "" {
			return errors.New("--msg-session goes with open (each step is an open with a note)")
		}
	}
	return nil
}

// buildRequest is the wire text: the command and optional base revision on
// the first line, then one path per line, then the optional note after the
// separator line.
func buildRequest(command, base, session string, paths []string, note string) string {
	header := command
	if session != "" {
		header += " " + extmsg.SessionOption + session
	}
	if base != "" {
		header += " " + base
	}
	request := header + "\n"
	if len(paths) > 0 {
		request += strings.Join(paths, "\n") + "\n"
	}
	if note != "" {
		request += extmsg.LineNoteSeparator + "\n" + note + "\n"
	}
	return request
}

// checkLineNote refuses a note the editor would refuse, before the request
// is sent: only an open takes one, and only with a line to sit next to.
func checkLineNote(command string, paths []string, note string) error {
	if strings.TrimSpace(note) == "" {
		return nil
	}
	if command != extmsg.CommandOpen {
		return errors.New("--msg-note goes with open <file>#L<n>")
	}
	for _, path := range paths {
		if _, line := app.SplitLineAnchor(path); line == 0 {
			return fmt.Errorf("a note needs a line: %s#L<n>", path)
		}
	}
	return nil
}

// readNoteFromStdin is the --msg-note - text, read whole and kept verbatim;
// an interactive terminal gets the usage instead of a silent wait.
func readNoteFromStdin(stdin *os.File) (string, error) {
	info, err := stdin.Stat()
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeCharDevice != 0 {
		return "", errors.New("usage: ntee --msg --msg-note - open <file>#L<n> <<'EOF' … EOF (pipe the note on stdin)")
	}
	text, err := io.ReadAll(io.LimitReader(stdin, extmsg.MaxLineNoteBytes+1))
	if err != nil {
		return "", err
	}
	if len(text) > extmsg.MaxLineNoteBytes {
		return "", fmt.Errorf("note is too long (max %d KiB)", extmsg.MaxLineNoteBytes>>10)
	}
	if strings.TrimSpace(string(text)) == "" {
		return "", errors.New("--msg-note -: no note on stdin")
	}
	return string(text), nil
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
