package mcpserver

import (
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
)

type toolArguments struct {
	Session        string  `json:"session"`
	Path           string  `json:"path"`
	OldText        *string `json:"old_text"`
	NewText        *string `json:"new_text"`
	ReplaceAll     bool    `json:"replace_all"`
	Line           int     `json:"line"`
	Content        *string `json:"content"`
	TimeoutSeconds int     `json:"timeout_seconds"`
	Offset         int     `json:"offset"`
	Limit          int     `json:"limit"`
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
	count     int
}

func newEditTools(config Config) *editTools {
	stamp := config.Now().Format("20060102-150405")
	return &editTools{config: config, prefix: "mcp-" + stamp + "-" + strconv.Itoa(os.Getpid())}
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
	case toolWait:
		if tools.lastEnded != "" {
			return tools.lastEnded, nil
		}
		if tools.current != "" {
			return tools.current, nil
		}
		return "", fmt.Errorf("no edit session to wait on — call %s first, or pass session", toolEnd)
	case toolReplace, toolWrite:
		if tools.current == "" {
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
	if name == toolEnd {
		tools.lastEnded = session
	}
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
	default:
		target := absolutePath
		if name == toolReplace && arguments.Line > 0 {
			target += "#L" + strconv.Itoa(arguments.Line)
		}
		if request, err = extmsg.EditRequest(command, session, tools.prefix, target, payload); err != nil {
			return textResult(err.Error(), true)
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
	switch {
	case hasBody && name == toolRead:
		return textResult(readText(arguments, session, extmsg.BodyDetail(line), body), false)
	case name == toolRead:
		return textResult(forTools(line), extmsg.IsErrorReply(line))
	case hasBody:
		return textResult(body+"\nedit session: "+session, false)
	case name == toolWait && strings.HasPrefix(line, "ok waiting") && strings.Contains(line, "aside for later"):
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

// hintsForTools rewrites the editor's ntee --msg hints into tool terms.
var hintsForTools = strings.NewReplacer(
	"ntee --msg show prints", toolRead+" shows",
	"add #L<n> to the path", "pass line",
	"with --msg-replace-all", "with replace_all",
)

func forTools(reply string) string {
	return hintsForTools.Replace(cliWaitHint.ReplaceAllString(reply, "call "+toolWait))
}

// readText is read_file's output: a header saying where the content came
// from, the requested lines numbered like cat -n, and a footer when lines
// are left.
func readText(arguments toolArguments, session, detail, content string) string {
	header := arguments.Path + " — on disk, no staged edits"
	if from, staged := strings.CutPrefix(detail, "staged · edit session "); staged {
		from, _, _ = strings.Cut(from, " ")
		header = arguments.Path + " — latest staged content from edit session " + from + ", NOT on disk yet (your edits build on it; to add them to that change set instead, pass session=" + from + ")"
		if from == session {
			header = arguments.Path + " — your edit session " + from + "'s staged copy, NOT on disk yet"
		}
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
