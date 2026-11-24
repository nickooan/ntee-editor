package mcpserver

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nickooan/ntee-editor/internal/extmsg"
)

const (
	defaultWaitSeconds = 300
	maxWaitSeconds     = int(extmsg.MaxSessionWait / time.Second)
	// waitReplyMargin is how much longer than its budget a wait listens:
	// the editor answers "ok waiting" just before the budget runs out.
	waitReplyMargin = 5 * time.Second
	// defaultReadLines and maxReadLineRunes shape read_file's output like a
	// numbered file read.
	defaultReadLines = 2000
	maxReadLineRunes = 2000
)

const (
	toolReplace = "edit_replace"
	toolWrite   = "edit_write"
	toolRead    = "read_file"
	toolStatus  = "edit_status"
	toolDiscard = "edit_discard"
	toolEnd     = "edit_end"
	toolWait    = "edit_wait"
	toolSandbox = "edit_sandbox"
	toolCheck   = "edit_check"
)

type toolArguments struct {
	Session        string   `json:"session"`
	Path           string   `json:"path"`
	OldText        *string  `json:"old_text"`
	NewText        *string  `json:"new_text"`
	ReplaceAll     bool     `json:"replace_all"`
	Line           int      `json:"line"`
	Content        *string  `json:"content"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	Offset         int      `json:"offset"`
	Limit          int      `json:"limit"`
	Fresh          bool     `json:"fresh"`
	Drop           bool     `json:"drop"`
	Include        []string `json:"include"`
	Result         string   `json:"result"`
	Note           string   `json:"note"`
}

type textContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolResult struct {
	Content []textContent `json:"content"`
	IsError bool          `json:"isError"`
}

func textResult(text string, isError bool) toolResult {
	return toolResult{Content: []textContent{{Type: "text", Text: text}}, IsError: isError}
}

// editTools turns tool calls into edit session requests. Each server process
// is one agent, named by prefix on every edit, end and status. Without a
// session argument, calls share one generated session id per process: it
// starts on the first edit (or is the other agent's session an edit joined)
// and is let go once the editor answers end or discard, so the next edit
// opens a new session; wait then targets the session that ended last.
type editTools struct {
	config Config
	prefix string

	mu        sync.Mutex
	current   string
	lastEnded string
	// lastOver: the session that ended last is the user's now (or over), so
	// the next edit starts a new session rather than reopening it.
	lastOver bool
	count    int
	// versions is each file's version as this agent last saw it (read_file
	// or an edit's reply), sent with its next edit so a change made under
	// it by another agent is caught.
	versions map[string]string
}

func newEditTools(config Config) *editTools {
	stamp := config.Now().Format("20060102-150405")
	return &editTools{config: config, prefix: "mcp-" + stamp + "-" + strconv.Itoa(os.Getpid()), versions: map[string]string{}}
}

// sessionFor picks the call's session: the explicit one, else (for wait)
// the session that ended last, else the current one, started when needed.
func (tools *editTools) sessionFor(name, explicit string) (string, error) {
	tools.mu.Lock()
	defer tools.mu.Unlock()
	if explicit != "" {
		// An edit naming another agent's session joins it: later calls
		// without a session go there too.
		if (name == toolReplace || name == toolWrite) && tools.current == "" {
			tools.current = explicit
		}
		return explicit, nil
	}
	switch name {
	case toolRead:
		return tools.current, nil
	case toolSandbox, toolCheck:
		// After edit_end the session finalises: its sandbox and checks
		// belong to the session that ended.
		if session := cmp.Or(tools.current, tools.lastEnded); session != "" {
			return session, nil
		}
		return "", fmt.Errorf("no edit session yet — stage your edits and call %s first, or pass session", toolEnd)
	case toolWait:
		if tools.lastEnded != "" {
			return tools.lastEnded, nil
		}
		if tools.current != "" {
			return tools.current, nil
		}
		return "", fmt.Errorf("no edit session to wait on — call %s first, or pass session", toolEnd)
	case toolReplace, toolWrite:
		switch {
		case tools.current != "":
		case tools.lastEnded != "" && !tools.lastOver:
			// A fix after edit_end goes to the same session: it is still
			// being edited or finalising, and the editor reopens it.
			tools.current = tools.lastEnded
		default:
			tools.count++
			tools.current = tools.prefix + "-" + strconv.Itoa(tools.count)
		}
	}
	if tools.current == "" {
		return "", fmt.Errorf("no edit session yet — stage an edit with %s or %s first, or pass session", toolReplace, toolWrite)
	}
	return tools.current, nil
}

// sessionClosed lets the current session go once the editor has answered
// its end or discard.
func (tools *editTools) sessionClosed(name, session string) {
	tools.mu.Lock()
	defer tools.mu.Unlock()
	if session == tools.current {
		tools.current = ""
	}
	if name == toolDiscard {
		tools.versions = map[string]string{}
	}
	if name == toolEnd {
		tools.lastEnded, tools.lastOver = session, false
	}
}

// sessionOver notes that the session is the user's now, or finished: the
// next edit starts a new session.
func (tools *editTools) sessionOver(session string) {
	tools.mu.Lock()
	defer tools.mu.Unlock()
	// The versions this agent saw were of that session's copies, gone
	// now: its next edits are checked again once it reads.
	tools.versions = map[string]string{}
	if session == tools.lastEnded {
		tools.lastOver = true
	}
	if session == tools.current {
		tools.current = ""
	}
}

// sessionReopened notes that the user asked for changes to a session this
// agent ended: it is open for edits again, so the next edit without a
// session goes back to it instead of starting a new one.
func (tools *editTools) sessionReopened(session string) {
	tools.mu.Lock()
	defer tools.mu.Unlock()
	tools.versions = map[string]string{} // read again before editing
	if tools.current == "" {
		tools.lastEnded, tools.lastOver = session, false
	}
}

func (tools *editTools) versionOf(path string) string {
	tools.mu.Lock()
	defer tools.mu.Unlock()
	return tools.versions[path]
}

func (tools *editTools) saw(path, version string) {
	tools.mu.Lock()
	defer tools.mu.Unlock()
	tools.versions[path] = version
}

// overReply reports whether a reply says the session takes no more edits:
// the user is reviewing it, it finished, or an edit was refused for that.
func overReply(line string) bool {
	return strings.Contains(line, extmsg.PhraseUserReviewing) || strings.HasPrefix(line, "ok finished") || strings.HasPrefix(line, "ended ") ||
		strings.HasPrefix(line, "error edit session") && strings.Contains(line, " applied ") || strings.Contains(line, extmsg.PhraseStartNewSession)
}

func (tools *editTools) call(ctx context.Context, name string, raw json.RawMessage) toolResult {
	var arguments toolArguments
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &arguments); err != nil {
			return textResult("bad arguments: "+err.Error(), true)
		}
	}
	command, payload, err := commandFor(name, arguments)
	if err != nil {
		return textResult(err.Error(), true)
	}
	absolutePath := ""
	if needsPath(name) {
		absolutePath = arguments.Path
		if !filepath.IsAbs(absolutePath) {
			absolutePath = filepath.Join(tools.config.WorkingDir, absolutePath)
		}
	}
	var paths []string
	if absolutePath != "" {
		paths = []string{absolutePath}
	}
	socketPath, err := tools.config.FindSocket(paths)
	if err != nil {
		return textResult(noEditorText(err), true)
	}
	session, err := tools.sessionFor(name, arguments.Session)
	if err != nil {
		return textResult(err.Error(), true)
	}

	request, timeout := "", tools.config.RequestTimeout
	switch name {
	case toolWait:
		seconds := arguments.TimeoutSeconds
		if seconds <= 0 {
			seconds = defaultWaitSeconds
		}
		budget := time.Duration(min(seconds, maxWaitSeconds)) * time.Second
		request, timeout = extmsg.EditWaitRequest(session, budget), budget+waitReplyMargin
	case toolCheck:
		request = extmsg.CheckRequest(session, tools.prefix, arguments.Result, arguments.Note)
	case toolSandbox:
		mode := ""
		switch {
		case arguments.Drop:
			mode = extmsg.SandboxDrop
		case arguments.Fresh:
			mode = extmsg.SandboxFresh
		}
		include := make([]string, len(arguments.Include))
		for index, folder := range arguments.Include {
			if !filepath.IsAbs(folder) {
				folder = filepath.Join(tools.config.WorkingDir, folder)
			}
			include[index] = folder
		}
		request, timeout = extmsg.SandboxRequest(session, mode, include), tools.config.SandboxTimeout
	default:
		target := absolutePath
		if name == toolReplace && arguments.Line > 0 {
			target += "#L" + strconv.Itoa(arguments.Line)
		}
		if request, err = extmsg.EditRequest(command, session, tools.prefix, target, payload); err != nil {
			return textResult(err.Error(), true)
		}
		if name == toolReplace || name == toolWrite {
			request = extmsg.WithExpect(request, tools.versionOf(absolutePath))
		}
		if name == toolEnd {
			timeout = tools.config.EndTimeout
		}
	}

	line, body, hasBody, err := extmsg.SendEdit(ctx, socketPath, command, request, timeout)
	if err != nil {
		if name == toolWait && os.IsTimeout(err) {
			return textResult(stillWaitingText(session), false)
		}
		if errors.Is(err, extmsg.ErrNoReply) && name == toolRead {
			return textResult(err.Error()+"; call "+toolRead+" again", true)
		}
		if errors.Is(err, extmsg.ErrNoReply) {
			return textResult(err.Error()+"; edit session "+session+" stays staged if it was pending — check with "+toolStatus+", then call "+name+" again if needed", true)
		}
		return textResult("cannot reach the editor: "+err.Error(), true)
	}
	if name == toolEnd || name == toolDiscard {
		tools.sessionClosed(name, session)
	}
	if version := extmsg.VersionIn(line); version != "" && absolutePath != "" && (name == toolRead || name == toolReplace || name == toolWrite) {
		tools.saw(absolutePath, version)
	}
	if name == toolWait && extmsg.IsChangesRequestedReply(line) {
		tools.sessionReopened(session)
		return textResult(changesRequestedText(session), false)
	}
	if overReply(line) {
		tools.sessionOver(session)
		if (name == toolReplace || name == toolWrite) && arguments.Session == "" && strings.Contains(line, extmsg.PhraseStartNewSession) {
			// The session this edit went to is the user's now: the edit
			// opens a new one (which builds on it) instead.
			return tools.call(ctx, name, raw)
		}
	}
	switch {
	case hasBody && name == toolRead:
		return textResult(readText(arguments, session, extmsg.BodyDetail(line), body), false)
	case name == toolRead:
		return textResult(forTools(line), extmsg.IsErrorReply(line))
	case hasBody:
		return textResult(body+"\nedit session: "+session, false)
	case name == toolWait && strings.HasPrefix(line, "ok waiting") && (strings.Contains(line, extmsg.PhraseFinalising) || strings.Contains(line, extmsg.PhraseBackToEditing)):
		return textResult(forTools(line)+"\nedit session: "+session, false)
	case name == toolWait && strings.HasPrefix(line, "ok waiting") && strings.Contains(line, extmsg.PhraseSetAside):
		return textResult("the user set edit session "+session+" aside for later — still staged, nothing written; carry on with other work, and call "+toolWait+" when you need their decision", false)
	case name == toolWait && (strings.HasPrefix(line, "ok waiting") || strings.HasPrefix(line, "waiting")):
		return textResult(stillWaitingText(session), false)
	}
	return textResult(forTools(line)+"\nedit session: "+session, extmsg.IsErrorReply(line))
}

func noEditorText(err error) string {
	if errors.Is(err, extmsg.ErrNoEditor) {
		return "no ntee editor is open on this project — nothing was staged; make the edit with your usual file tools"
	}
	return "cannot find the ntee editor (" + err.Error() + ") — nothing was staged; make the edit with your usual file tools"
}

// changesRequestedText tells the agent to hand over to the user: they asked
// for changes from the review and are coming to this terminal.
func changesRequestedText(session string) string {
	return "changes requested — the user has questions or changes for edit session " + session + " and is switching to this terminal. " +
		"Stop here: tell them you are ready, and ask what they want to change or ask; do not call " + toolWait + " again yet. " +
		"Once they answer, re-read the files with " + toolRead + ", stage the follow-ups (they go into the same session, open for edits again), then call " + toolEnd + " and " + toolWait + ".\nedit session: " + session
}

func stillWaitingText(session string) string {
	return "still waiting — the user hasn't finished reviewing edit session " + session + "; call " + toolWait + " again"
}

// commandFor checks a call's arguments and returns its socket command and,
// for write and replace, the payload.
func commandFor(name string, arguments toolArguments) (command, payload string, err error) {
	if needsPath(name) && strings.TrimSpace(arguments.Path) == "" {
		return "", "", fmt.Errorf("%s needs path: the file, relative to the project or absolute", name)
	}
	switch name {
	case toolReplace:
		if arguments.OldText == nil || *arguments.OldText == "" {
			return "", "", fmt.Errorf("%s needs old_text: the exact text to find (use %s to set a whole file)", name, toolWrite)
		}
		if arguments.NewText == nil {
			return "", "", fmt.Errorf("%s needs new_text: the replacement (an empty string deletes old_text)", name)
		}
		if arguments.Line > 0 && arguments.ReplaceAll {
			return "", "", fmt.Errorf("line picks one match — leave it off with replace_all")
		}
		return extmsg.CommandReplace, extmsg.EncodeReplace(extmsg.ReplacePayload{Old: *arguments.OldText, New: *arguments.NewText, All: arguments.ReplaceAll}), nil
	case toolWrite:
		if arguments.Content == nil {
			return "", "", fmt.Errorf("%s needs content: the whole new file", name)
		}
		return extmsg.CommandWrite, *arguments.Content, nil
	case toolRead:
		if arguments.Offset < 0 || arguments.Limit < 0 {
			return "", "", fmt.Errorf("offset and limit start at 1")
		}
		return extmsg.CommandRead, "", nil
	case toolStatus:
		return extmsg.CommandStatus, "", nil
	case toolDiscard:
		return extmsg.CommandDiscard, "", nil
	case toolEnd:
		return extmsg.CommandEnd, "", nil
	case toolCheck:
		if arguments.Result != extmsg.CheckPass && arguments.Result != extmsg.CheckFail && arguments.Result != extmsg.CheckSkip {
			return "", "", fmt.Errorf("%s needs result: pass, fail or skip", name)
		}
		return extmsg.CommandCheck, "", nil
	case toolSandbox:
		if arguments.Drop && (arguments.Fresh || len(arguments.Include) > 0) {
			return "", "", fmt.Errorf("drop takes neither fresh nor include")
		}
		return extmsg.CommandSandbox, "", nil
	case toolWait:
		if arguments.TimeoutSeconds > maxWaitSeconds {
			return "", "", fmt.Errorf("timeout_seconds is at most %d", maxWaitSeconds)
		}
		return extmsg.CommandWait, "", nil
	}
	return "", "", fmt.Errorf("unknown tool: %s", name)
}

func needsPath(name string) bool {
	return name == toolReplace || name == toolWrite || name == toolRead
}

func knownTool(name string) bool {
	for _, definition := range toolDefinitions {
		if definition.Name == name {
			return true
		}
	}
	return false
}

var cliWaitHint = regexp.MustCompile(`run: ntee --msg --msg-edit-session \S+ wait`)

var cliSandboxHint = regexp.MustCompile(`run: ntee --msg --msg-edit-session \S+ sandbox`)

// hintsForTools rewrites the editor's ntee --msg hints into tool terms.
var hintsForTools = strings.NewReplacer(
	"ntee --msg show prints", toolRead+" shows",
	"read it again (ntee --msg read)", "read it again ("+toolRead+")",
	"its sandbox (sandbox)", "its sandbox ("+toolSandbox+")",
	"report with check pass, fail or skip", "report with "+toolCheck+" (pass, fail or skip)",
	"report with check,", "report with "+toolCheck+",",
	"add #L<n> to the path", "pass line",
	"with --msg-replace-all", "with replace_all",
)

func forTools(reply string) string {
	reply = cliSandboxHint.ReplaceAllString(reply, "call "+toolSandbox)
	return hintsForTools.Replace(cliWaitHint.ReplaceAllString(reply, "call "+toolWait))
}

// readText is read_file's output: a header saying where the content came
// from, the requested lines numbered like cat -n, and a footer when lines
// are left.
func readText(arguments toolArguments, session, detail, content string) string {
	header := arguments.Path + " — on disk, no staged edits"
	if from, staged := extmsg.StagedSessionIn(detail); staged {
		header = arguments.Path + " — latest staged content from edit session " + from + ", NOT on disk yet (your edits build on it; to add them to that change set instead, pass session=" + from + ")"
		if from == session {
			header = arguments.Path + " — your edit session " + from + "'s staged copy, NOT on disk yet"
		}
	}
	if stale, found := extmsg.StaleIn(detail); found {
		header += "\n⚠ your copy can no longer be applied as it is: " + stale + " — read the file without your session (or after leaving it) to see the latest, and stage your change on that in a new session, or join the session below it"
	}
	if repo, list, found := extmsg.AlsoEditingIn(detail); found {
		header += "\nopen edit sessions changing " + repo + ": " + list + " — if your change belongs with one of theirs, pass session=<its id> on your edits; else your own session builds on theirs"
	}
	if content == "" {
		return header + "\n(empty file)"
	}
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	first := max(arguments.Offset, 1)
	if first > len(lines) {
		return fmt.Sprintf("%s\n(offset %d is past the end: the file has %d lines)", header, first, len(lines))
	}
	count := arguments.Limit
	if count <= 0 {
		count = defaultReadLines
	}
	last := min(len(lines), first+count-1)
	var builder strings.Builder
	builder.WriteString(header + "\n")
	for index := first; index <= last; index++ {
		line := lines[index-1]
		if runes := []rune(line); len(runes) > maxReadLineRunes {
			line = string(runes[:maxReadLineRunes]) + fmt.Sprintf("… [line cut at %d characters]", maxReadLineRunes)
		}
		fmt.Fprintf(&builder, "%6d\t%s\n", index, line)
	}
	if last < len(lines) {
		fmt.Fprintf(&builder, "(lines %d–%d of %d — pass offset %d to read on)\n", first, last, len(lines), last+1)
	}
	return strings.TrimSuffix(builder.String(), "\n")
}
