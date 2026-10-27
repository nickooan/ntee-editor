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
  <note, a few short lines>
  EOF
      Open the file as above and show the note in a box next to the line,
      to explain it. The #L<line> anchor is required with a note. "-" reads
      the note from stdin. The user closes the box with Esc and keeps
      editing; it also closes on the first edit. On the wire the note
      follows the path after a line that is exactly "---".

      Keep notes short. A person reads them in a small box beside the code,
      so write a few short lines they take in at a glance: what changed and
      why, one point per note. Prefer a bullet or two and ` + "`inline code`" + ` to
      paragraphs; don't paste long explanations, logs or big code blocks.
      More to say? Split it across steps or files, not into one long note.
      Limits: the box shows at most 8 lines (fewer on a short terminal),
      then scrolls — Shift+Up/Down or the mouse wheel (only the wheel in a
      review); it is at most 100 columns wide, and longer lines wrap;
      notes are capped at 16 KiB.

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
  ntee --msg --msg-session <id> end
  ntee --msg --msg-session <id> wait
      Every step is an open with a note, sent with the same session id
      (1–64 letters, digits, '.', '_', '-'; pick a new one per walkthrough).
      Keep each note to a few short lines (see "Keep notes short" above).
      Send the steps one after another without waiting: each returns at once
      and the editor queues them in order. The first step asks the user to
      start the session; from then on they walk the queue at their own pace —
      Enter shows the next step instantly, Backspace goes back, Esc ends the
      session — and each step jumps straight to its file, other files and
      nested repos included, without asking. The file is read-only while a
      step shows. Past the last queued step the box waits for the agent's
      next one, which then shows as soon as it arrives.
      After the last step, send end: it marks the session complete, so
      Enter on the last step finishes it. A note session shows the files,
      never a diff: its steps take no --msg-base (use a diff session).
      What each command prints:
        open  ok step N queued   exit 0  keep sending steps
              ended …            exit 3  the user ended or declined the
                                         session, or it is over: stop
              error …            exit 1  bad path, another session running, …
        end   ok session … complete — it ends after step N
        wait  blocks until the session is over (up to --msg-wait, default 10m):
              ok finished …      exit 0  the user went through every step
              ended …            exit 3  the user closed or declined it
              ok waiting …       exit 4  not over yet: run wait again

  Waiting for the user (every session kind):
      The user reads at their own pace — a tour can take many minutes. To
      learn how it went, run wait, but don't block on it: start it as a
      background task (your tool's background mode, or "… wait > file &")
      and hold it while you carry on or end your turn; its exit tells you
      when the user is done. Give it a long --msg-wait (the editor answers
      a wait within 1h at most); on exit 4 start wait again. Then act on
      the result: exit 0 — the user saw every step; exit 3 — they closed
      it early, so ask where they stopped before moving on. wait can start
      any time after the first step, and several may wait at once.

  A walkthrough an agent might run:
    ntee --msg --msg-session tour1 --msg-note - open cmd/ntee/main.go#L40 <<'EOF'
    ## Flags
    Everything starts here.
    EOF
    ntee --msg --msg-session tour1 --msg-note "the request goes out here" open cmd/ntee/msg.go#L180
    ntee --msg --msg-session tour1 end
    ntee --msg --msg-session tour1 --msg-wait 1h wait   # optional, as a background task: until the user is done

  On the wire the session is a first-line option: "open --session=<id>",
  "end --session=<id>", "wait --session=<id>".

Diff sessions — walk the user through changes, one note at a time:

  ntee --msg --msg-diff-session <id> --msg-note "<text>" open <file>#L<line>
  ntee --msg --msg-diff-session <id> --msg-base <rev> --msg-note "<text>" open <file>#L<line>
  ntee --msg --msg-diff-session <id> end
  ntee --msg --msg-diff-session <id> wait
      Keep each note to a few short lines (see "Keep notes short" above).
      Like a guided session, but every step opens the file's diff view —
      against HEAD (the uncommitted changes) or the step's --msg-base — with
      the note in a box on its line, even when the file has no changes. The
      keys are the same: Enter next, Backspace back, Esc ends the session
      (the diff stays open; Esc again leaves it). A file outside any git
      repo, or a bad revision, shows the note on the plain file instead.
      Replies and exit codes are those of a guided session.
      On the wire: "open --diff-session=<id> [<rev>]".

Review sessions — a live review the user follows while you write code:

  ntee --msg --msg-review-session <id> review <file>
  ntee --msg --msg-review-session <id> --msg-note "<why>" review <file>[#L<line>]
  ntee --msg --msg-review-session <id> --msg-base <rev> review <file>   # first file only
  ntee --msg --msg-review-session <id> end
  ntee --msg --msg-review-session <id> wait
      Use it while you implement: each time you change a file, forward it
      here — with a note explaining the change, or without — instead of
      printing the diff in your own UI. The user reviews it in ntee as you
      go: the review page keeps growing, one file per send, each with its
      git diff (against HEAD, or the first send's --msg-base) and your note
      in a box on its #L<line>, or on the file's first change when you name
      no line. Send a file again after changing it again: its diff refreshes
      and the new note replaces the old one. Keep each note to a few short
      lines (see "Keep notes short" above).
      The first send asks the user to start the session; files sent before
      they answer queue. The view follows each new file until the user moves
      to another one; after that new files are marked • for them to visit.
      The user quits with Esc (they confirm first); you are then told to
      stop. Send end when you are done changing files — the review stays
      open for the user. Nothing goes to the editor's history until the
      session is over; then the whole review is saved as one entry, notes
      included, that the user can replay.
      What each command prints:
        review  ok file N queued      exit 0  before the user started it
                ok file N added …     exit 0  keep sending files
                ok file … refreshed   exit 0  a resent file
                ended …               exit 3  the user quit (or declined)
                                              the review: stop sending
                error …               exit 1  bad path, another session
                                              running, a different --msg-base, …
        end     ok session … complete — the user is reviewing N files
        wait    as above: ok finished … (exit 0) once the user quits after
                your end, ended … (exit 3) if they quit before it
      On the wire: "review --review-session=<id> [<rev>]", one path, then
      the optional note after a "---" line.

  The editor runs one session at a time, of any kind: while one is running
  or waiting to start, a request of another id — or of the same id as
  another kind of session — is refused with an error. While a review
  session is open, plain open/review requests are refused too.

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
  ntee --msg --msg-diff-session tour2 --msg-base main --msg-note "…" open <file>#L<line>
  ntee --msg --msg-review-session impl1 --msg-note "…" review <file>

External messages are off by default: the user turns them on in ntee under
Ctrl+T › external. The editor keeps the latest messages (10 by default) and
can replay them from there.

Reply (one line on stdout):
  ok <detail>       accepted — "opened …", "reviewing N files", or
                    "waiting for the user to confirm"; for a session
                    "step N queued", "file N added …", "finished …", or
                    from wait "waiting …" (exit 4)
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
  is fine; ` + "`--msg-note -`" + ` reads a longer note from stdin). Keep notes short — a
  few lines saying what changed and why, not an article.
  To walk the user through several places, send each as a step of one
  session, one after another: ` + "`ntee --msg --msg-session <id> --msg-note \"<why>\" open <file>#L<line>`" + `
  (each returns at once; exit 3 means the user ended it — stop sending), then
  ` + "`ntee --msg --msg-session <id> end`" + `. To walk through changes instead,
  use ` + "`--msg-diff-session <id>`" + ` (optionally with ` + "`--msg-base <rev>`" + `): each step
  shows the diff with the note on its line. To hear when the user is done,
  run ` + "`ntee --msg --msg-session <id> --msg-wait 1h wait`" + ` as a background task and
  hold it (exit 0 finished, 3 closed early, 4 still going — wait again).
  While implementing, forward each file you change to a live review instead
  of showing diffs yourself: ` + "`ntee --msg --msg-review-session <id> [--msg-note \"<why>\"] review <file>`" + `
  (one file per send; exit 3 means the user quit the review — stop sending),
  then ` + "`ntee --msg --msg-review-session <id> end`" + ` when you are done.
  Paths are relative to the current directory. The command prints ` + "`ok …`" + ` on
  success; ` + "`error … disabled`" + ` means the user has not enabled external messages
  (Ctrl+T → external) — mention it once and carry on. Run ` + "`ntee --msg help`" + `
  for details.
`

// msgOptions are the --msg-* flags.
type msgOptions struct {
	root          string        // --msg-root: the editor's folder
	base          string        // --msg-base: the revision to compare against
	note          string        // --msg-note: the note text, "-" = stdin
	session       string        // --msg-session: a guided note session's id
	diffSession   string        // --msg-diff-session: a guided diff session's id
	reviewSession string        // --msg-review-session: a live review session's id
	wait          time.Duration // --msg-wait: how long a session request waits for the user
}

// msgSession is the session a request belongs to: its id and kind.
type msgSession struct {
	id   string
	kind extmsg.SessionKind
}

// sessionFromOptions picks the session flag; more than one is refused.
func sessionFromOptions(options msgOptions) (msgSession, error) {
	var picked []msgSession
	for _, candidate := range []msgSession{
		{id: options.session, kind: extmsg.SessionNote},
		{id: options.diffSession, kind: extmsg.SessionDiff},
		{id: options.reviewSession, kind: extmsg.SessionReview},
	} {
		if candidate.id != "" {
			picked = append(picked, candidate)
		}
	}
	switch len(picked) {
	case 0:
		return msgSession{}, nil
	case 1:
		return picked[0], nil
	}
	return msgSession{}, errors.New("use one of --msg-session, --msg-diff-session or --msg-review-session")
}

// sessionFlag is the CLI flag naming a session of kind.
func sessionFlag(kind extmsg.SessionKind) string {
	switch kind {
	case extmsg.SessionDiff:
		return "--msg-diff-session"
	case extmsg.SessionReview:
		return "--msg-review-session"
	}
	return "--msg-session"
}

func (session msgSession) flag() string { return sessionFlag(session.kind) }

// option is the session's first-line wire option, "" for none.
func (session msgSession) option() string {
	if session.id == "" {
		return ""
	}
	return session.kind.Option() + session.id
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
	session, err := sessionFromOptions(options)
	if err == nil {
		err = checkSessionCommand(command, paths, session, options.base)
	}
	if err != nil {
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
	if err := checkLineNote(command, paths, note, session); err != nil {
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
	var output string
	var code int
	if command == extmsg.CommandWait {
		output, code = deliverWait(socketPath, session, options.wait)
	} else {
		output, code = deliverRequest(socketPath, buildRequest(command, options.base, session.option(), paths, note), session, 10*time.Second)
	}
	if code == 1 && !extmsg.IsErrorReply(output) {
		fmt.Fprintln(os.Stderr, output)
		return code
	}
	fmt.Println(output)
	return code
}

// deliverRequest sends a request and turns the reply into the printed line
// and the exit code. A session's wait blocks until the session is over, up
// to timeout; when that runs out first, the agent is told how to keep
// waiting.
func deliverRequest(socketPath, request string, session msgSession, timeout time.Duration) (string, int) {
	reply, err := extmsg.SendWithTimeout(socketPath, request, timeout)
	switch {
	case err != nil && session.id != "" && os.IsTimeout(err):
		return "ok waiting — the user hasn't answered yet; run: ntee --msg " + session.flag() + " " + session.id + " wait", exitSessionWaiting
	case err != nil:
		return "cannot reach the editor: " + err.Error(), 1
	}
	return reply, exitCodeForReply(reply)
}

// deliverWait sends a session's wait with this process's budget, so the
// editor answers before it gives up instead of holding the connection past
// its exit. An editor older than the budget option refuses it; the bare wait
// then goes out once.
func deliverWait(socketPath string, session msgSession, timeout time.Duration) (string, int) {
	budgeted := buildRequest(extmsg.CommandWait, "", session.option()+" "+extmsg.WaitOption+timeout.String(), nil, "")
	output, code := deliverRequest(socketPath, budgeted, session, timeout)
	if strings.HasPrefix(output, extmsg.ErrorReply("unknown option "+extmsg.WaitOption)) {
		return deliverRequest(socketPath, buildRequest(extmsg.CommandWait, "", session.option(), nil, ""), session, timeout)
	}
	return output, code
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
// wait act on a session and take no paths; a review session's review sends
// one file; note and diff sessions step through opens; a note session's
// open takes no base revision.
func checkSessionCommand(command string, paths []string, session msgSession, base string) error {
	switch command {
	case extmsg.CommandEnd, extmsg.CommandWait:
		if session.id == "" {
			return fmt.Errorf("%s needs --msg-session, --msg-diff-session or --msg-review-session <id>", command)
		}
		if len(paths) > 0 {
			return fmt.Errorf("%s takes no paths: ntee --msg %s %s %s", command, session.flag(), session.id, command)
		}
	case extmsg.CommandReview:
		switch {
		case session.id == "":
		case session.kind != extmsg.SessionReview:
			return errors.New(session.flag() + " goes with open (each step is an open with a note) — a live review uses --msg-review-session")
		case len(paths) != 1:
			return fmt.Errorf("a review session sends one file per review, got %d", len(paths))
		}
	case extmsg.CommandOpen:
		switch {
		case session.id == "":
		case session.kind == extmsg.SessionReview:
			return errors.New("--msg-review-session goes with review: ntee --msg --msg-review-session " + session.id + " review <file>")
		case session.kind == extmsg.SessionNote && base != "":
			return errors.New("--msg-session steps are notes only — to walk diffs use --msg-diff-session <id> [--msg-base <rev>]")
		}
	}
	return nil
}

// buildRequest is the wire text: the command, the optional session option
// (msgSession.option) and base revision on the first line, then one path per
// line, then the optional note after the separator line.
func buildRequest(command, base, sessionOption string, paths []string, note string) string {
	header := command
	if sessionOption != "" {
		header += " " + sessionOption
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
// is sent: an open takes one with a line to sit next to; a review session's
// file takes one with or without a line (none = its first change).
func checkLineNote(command string, paths []string, note string, session msgSession) error {
	if strings.TrimSpace(note) == "" {
		return nil
	}
	if command == extmsg.CommandReview && session.kind == extmsg.SessionReview {
		return nil
	}
	if command != extmsg.CommandOpen {
		return errors.New("--msg-note goes with open <file>#L<n>, or with a --msg-review-session review")
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
