// Package extmsg is the external-message channel of a running editor: a unix
// socket per project over which other programs (coding agents, scripts) ask
// the editor to open a file, review a set of changed files, or stage edits.
package extmsg

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Message is one parsed request: the command line, then one path per line,
// then for an open (or a review session's review) an optional note after a
// LineNoteSeparator line. An edit session's write or replace carries a
// payload there instead.
type Message struct {
	Command     string        // one of the Command constants
	Args        []string      // tokens after the command, options aside: an optional base revision
	Paths       []string      // payload lines, trimmed, blanks dropped; may carry a #L<n> anchor
	LineNote    string        // free-form text shown next to the anchored line (or the file's first change), "" = none
	Session     string        // the session option's id, "" = none
	SessionKind SessionKind   // which session option named it; SessionNone without one
	WaitBudget  time.Duration // wait only: how long the sender waits for the reply, 0 = not given
	Agent       string        // edit session only: the sending agent's id, "" = unnamed
	Expect      string        // write and replace only: the file version the edit was made on, "" = unchecked
	// Payload is a write's new content or a replace's EncodeReplace JSON:
	// everything after the separator line, byte for byte. HasPayload tells
	// an empty payload (an empty file) from none.
	Payload    string
	HasPayload bool
}

const (
	CommandOpen   = "open"
	CommandReview = "review"
	CommandEnd    = "end"  // session: the agent closes it
	CommandWait   = "wait" // session: wait for the current step's answer

	// Edit session commands: stage edits without touching the files.
	CommandWrite   = "write"   // stage a file's whole new content
	CommandReplace = "replace" // stage an exact-text replacement
	CommandShow    = "show"    // a file's staged content (its disk content when not staged)
	CommandStatus  = "status"  // the session's staged files
	CommandDiscard = "discard" // drop the session's staged edits
	CommandSandbox = "sandbox" // a scratch copy of the session's repos with its staged files in place, to build and test
	CommandCheck   = "check"   // an agent's report on its finalising session: pass, fail or skip, with a note

	// Reads through the editor, for any agent; an edit session is optional.
	CommandRead   = "read"   // a file's latest content: staged by some session, else the disk
	CommandStaged = "staged" // which session, if any, has staged edits to a file
)

// SessionKind is what a session shows. The first-line option that names the
// session picks it.
type SessionKind uint8

const (
	SessionNone   SessionKind = iota
	SessionNote               // --session=: each step opens its file with the note
	SessionDiff               // --diff-session=: each step opens the file's diff with the note
	SessionReview             // --review-session=: each review adds one file to a live review
	SessionEdit               // --edit-session=: edits are staged, then applied or reviewed at end
)

const (
	SessionOption       = "--session="
	DiffSessionOption   = "--diff-session="
	ReviewSessionOption = "--review-session="
	EditSessionOption   = "--edit-session="
	// WaitOption on a wait tells the editor how long the sender will wait,
	// so the editor answers before then instead of holding the connection
	// past the sender's exit.
	WaitOption = "--wait="
	// AgentOption names the agent sending an edit session's write, replace,
	// end or status, so several agents can share one session; without it
	// the agent is unnamed.
	AgentOption = "--agent="
	// ExpectOption on a write or replace names the version of the file the
	// agent read (from read or an earlier edit's reply), so an edit made on
	// a stale read is caught.
	ExpectOption = "--expect="
)

// Option is the first-line option naming a session of this kind.
func (kind SessionKind) Option() string {
	switch kind {
	case SessionNote:
		return SessionOption
	case SessionDiff:
		return DiffSessionOption
	case SessionReview:
		return ReviewSessionOption
	case SessionEdit:
		return EditSessionOption
	}
	return ""
}

var sessionKinds = []SessionKind{SessionNote, SessionDiff, SessionReview, SessionEdit}

// sessionOptionsText lists the session options for error messages.
const sessionOptionsText = SessionOption + "<id>, " + DiffSessionOption + "<id>, " + ReviewSessionOption + "<id> or " + EditSessionOption + "<id>"

var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

var versionPattern = regexp.MustCompile(`^[0-9a-f]{12}$`)

const (
	// MaxMessageBytes caps one request; anything longer is refused unread.
	MaxMessageBytes = 1 << 20
	// MaxReviewPaths caps the files of one review request.
	MaxReviewPaths = 500
	// ReadTimeout bounds how long a sender may take to deliver its request.
	ReadTimeout = 2 * time.Second
	// MaxLineNoteBytes caps the note of one open request.
	MaxLineNoteBytes = 16 << 10
	// MaxSessionWait caps how long a session request waits for the user.
	MaxSessionWait = time.Hour
	// MaxBodyBytes caps a reply's body (a staged file shown to the agent).
	MaxBodyBytes = 4 << 20
)

// LineNoteSeparator is the line that ends the paths; everything after it is
// the note, verbatim.
const LineNoteSeparator = "---"

// Parse reads a request: the first non-blank line is the command and its
// arguments, every following non-blank line one path, until an optional
// LineNoteSeparator line after which the rest is the note — or, for a write
// or replace, the payload. Errors are user-facing and go back to the sender
// verbatim.
func Parse(text string) (Message, error) {
	var message Message
	commandSeen, noteSeen := false, false
	for offset := 0; offset <= len(text); {
		rawLine, next := text[offset:], len(text)+1
		if end := strings.IndexByte(rawLine, '\n'); end >= 0 {
			rawLine, next = rawLine[:end], offset+end+1
		}
		offset = next
		line := strings.TrimSpace(rawLine)
		if commandSeen && line == LineNoteSeparator {
			rest := text[min(next, len(text)):]
			if carriesPayload(message.Command) {
				// The payload is file content: kept byte for byte.
				message.Payload, message.HasPayload = rest, true
				break
			}
			// Blank lines and indentation inside the note are its formatting.
			message.LineNote = trimBlankLines(strings.Split(strings.ReplaceAll(rest, "\r\n", "\n"), "\n"))
			noteSeen = message.LineNote != ""
			break
		}
		if line == "" {
			continue
		}
		if !commandSeen {
			fields := strings.Fields(line)
			message.Command = strings.ToLower(fields[0])
			message.Args = []string{}
			for _, field := range fields[1:] {
				kind := sessionKindOf(field)
				switch {
				case kind != SessionNone:
					if message.Session != "" {
						return Message{}, errors.New("one session option only: " + sessionOptionsText)
					}
					message.SessionKind = kind
					message.Session = strings.TrimPrefix(field, kind.Option())
					if !sessionIDPattern.MatchString(message.Session) {
						return Message{}, errors.New("bad session id " + message.Session + " — use 1–64 letters, digits, '.', '_' or '-'")
					}
				case strings.HasPrefix(field, AgentOption):
					message.Agent = strings.TrimPrefix(field, AgentOption)
					if !sessionIDPattern.MatchString(message.Agent) {
						return Message{}, errors.New("bad agent id " + message.Agent + " — use 1–64 letters, digits, '.', '_' or '-'")
					}
				case strings.HasPrefix(field, ExpectOption):
					message.Expect = strings.TrimPrefix(field, ExpectOption)
					if !versionPattern.MatchString(message.Expect) {
						return Message{}, errors.New("bad " + field + " — give the version a read or an edit reply named")
					}
				case strings.HasPrefix(field, WaitOption):
					budget, err := time.ParseDuration(strings.TrimPrefix(field, WaitOption))
					if err != nil || budget <= 0 {
						return Message{}, errors.New("bad " + field + " — give a duration such as " + WaitOption + "10m")
					}
					message.WaitBudget = budget
				case strings.HasPrefix(field, "--"):
					return Message{}, errors.New("unknown option " + field + " — the options are " + sessionOptionsText)
				default:
					message.Args = append(message.Args, field)
				}
			}
			commandSeen = true
			continue
		}
		message.Paths = append(message.Paths, line)
	}
	if !commandSeen {
		return Message{}, errors.New("empty message — the first line must be a command (open, review)")
	}
	if message.Agent != "" && (message.SessionKind != SessionEdit || !slices.Contains([]string{CommandWrite, CommandReplace, CommandEnd, CommandStatus, CommandCheck}, message.Command)) {
		return Message{}, errors.New(AgentOption + " goes with an edit session's write, replace, end, status or check")
	}
	if message.Expect != "" && (message.SessionKind != SessionEdit || message.Command != CommandWrite && message.Command != CommandReplace) {
		return Message{}, errors.New(ExpectOption + " goes with an edit session's write or replace")
	}
	if message.WaitBudget > 0 && message.Command != CommandWait {
		return Message{}, errors.New(WaitOption + " goes with wait only")
	}
	switch message.Command {
	case CommandOpen:
		if err := checkBaseArgument(message.Command, message.Args); err != nil {
			return Message{}, err
		}
		if len(message.Paths) != 1 {
			return Message{}, fmt.Errorf("open needs exactly one path, got %d", len(message.Paths))
		}
		if len(message.LineNote) > MaxLineNoteBytes {
			return Message{}, fmt.Errorf("note is too long (max %d KiB)", MaxLineNoteBytes>>10)
		}
		if message.SessionKind == SessionReview {
			return Message{}, errors.New("open takes no " + ReviewSessionOption + " — a review session sends review requests")
		}
		if message.SessionKind == SessionEdit {
			return Message{}, errors.New("open takes no " + EditSessionOption + " — an edit session sends write, replace, show, status, discard, end and wait")
		}
		if message.Session != "" && message.LineNote == "" {
			return Message{}, errors.New("a session step needs a note: open <file>#L<n>, then a --- line and the note")
		}
		if message.SessionKind == SessionNote && message.Base() != "" {
			return Message{}, errors.New("a note session takes no base revision — walk diffs with " + DiffSessionOption + "<id>")
		}
	case CommandReview:
		if err := checkBaseArgument(message.Command, message.Args); err != nil {
			return Message{}, err
		}
		if len(message.Paths) == 0 {
			return Message{}, errors.New("review needs at least one path")
		}
		if len(message.Paths) > MaxReviewPaths {
			return Message{}, fmt.Errorf("review takes at most %d paths, got %d", MaxReviewPaths, len(message.Paths))
		}
		switch message.SessionKind {
		case SessionNone:
			if noteSeen {
				return Message{}, errors.New("review takes no note — a note goes with open <file>#L<n>, or with a review session's file")
			}
		case SessionReview:
			if len(message.Paths) != 1 {
				return Message{}, fmt.Errorf("a review session sends one file per review, got %d", len(message.Paths))
			}
			if len(message.LineNote) > MaxLineNoteBytes {
				return Message{}, fmt.Errorf("note is too long (max %d KiB)", MaxLineNoteBytes>>10)
			}
		default:
			return Message{}, errors.New("review takes no " + message.SessionKind.Option() + " — a review session uses " + ReviewSessionOption + "<id>")
		}
	case CommandEnd, CommandWait:
		if message.Session == "" {
			return Message{}, errors.New(message.Command + " needs " + sessionOptionsText)
		}
		if len(message.Args) > 0 || len(message.Paths) > 0 || noteSeen {
			return Message{}, errors.New(message.Command + " takes only the session option")
		}
	case CommandWrite, CommandReplace, CommandShow:
		if err := checkEditSessionCommand(message, noteSeen); err != nil {
			return Message{}, err
		}
		if len(message.Paths) != 1 {
			return Message{}, fmt.Errorf("%s needs exactly one path, got %d", message.Command, len(message.Paths))
		}
		switch {
		case message.Command == CommandShow && message.HasPayload:
			return Message{}, errors.New("show takes no content")
		case message.Command != CommandShow && !message.HasPayload:
			return Message{}, errors.New(message.Command + " needs its content after a " + LineNoteSeparator + " line")
		case message.Command == CommandReplace:
			if _, err := DecodeReplace(message.Payload); err != nil {
				return Message{}, err
			}
		}
	case CommandRead, CommandStaged:
		switch {
		case message.SessionKind != SessionNone && message.SessionKind != SessionEdit:
			return Message{}, errors.New(message.Command + " takes no " + message.SessionKind.Option() + " — only an optional " + EditSessionOption + "<id>")
		case len(message.Args) > 0:
			return Message{}, errors.New(message.Command + " takes no base revision")
		case noteSeen:
			return Message{}, errors.New(message.Command + " takes no note")
		case len(message.Paths) != 1:
			return Message{}, fmt.Errorf("%s needs exactly one path, got %d", message.Command, len(message.Paths))
		}
	case CommandCheck:
		switch {
		case message.SessionKind != SessionEdit:
			return Message{}, errors.New("check needs " + EditSessionOption + "<id>")
		case len(message.Args) != 1 || !slices.Contains([]string{CheckPass, CheckFail, CheckSkip}, message.Args[0]):
			return Message{}, errors.New("check takes one result: " + CheckPass + ", " + CheckFail + " or " + CheckSkip + " (then an optional note after a " + LineNoteSeparator + " line)")
		case len(message.Paths) > 0:
			return Message{}, errors.New("check takes no paths — say what you ran in its note")
		case len(message.LineNote) > MaxLineNoteBytes:
			return Message{}, fmt.Errorf("note is too long (max %d KiB)", MaxLineNoteBytes>>10)
		}
	case CommandSandbox:
		switch {
		case message.SessionKind != SessionEdit:
			return Message{}, errors.New("sandbox needs " + EditSessionOption + "<id>")
		case noteSeen || message.HasPayload:
			return Message{}, errors.New("sandbox takes no note")
		case len(message.Args) > 1 || len(message.Args) == 1 && message.Args[0] != SandboxFresh && message.Args[0] != SandboxDrop:
			return Message{}, errors.New("sandbox takes " + SandboxFresh + " or " + SandboxDrop + " at most, then the folders to include, one per line")
		case len(message.Args) == 1 && message.Args[0] == SandboxDrop && len(message.Paths) > 0:
			return Message{}, errors.New("sandbox drop takes no folders")
		}
	case CommandStatus, CommandDiscard:
		if err := checkEditSessionCommand(message, noteSeen); err != nil {
			return Message{}, err
		}
		if len(message.Paths) > 0 {
			return Message{}, errors.New(message.Command + " takes only the session option")
		}
	default:
		return Message{}, fmt.Errorf("unknown command %q — use open, review, read, staged, end or wait (an edit session also takes write, replace, show, status, discard, sandbox and check)", message.Command)
	}
	return message, nil
}

// The arguments a sandbox request may carry: rebuild it from the disk, or
// delete it.
const (
	SandboxFresh = "fresh"
	SandboxDrop  = "drop"
)

// A check's result argument.
const (
	CheckPass = "pass"
	CheckFail = "fail"
	CheckSkip = "skip"
)

// carriesPayload reports whether a command's text after the separator line
// is a payload rather than a note.
func carriesPayload(command string) bool {
	return command == CommandWrite || command == CommandReplace
}

// checkEditSessionCommand checks what every edit session command shares:
// the edit session option, no base revision, and no note.
func checkEditSessionCommand(message Message, noteSeen bool) error {
	switch {
	case message.SessionKind != SessionEdit:
		return errors.New(message.Command + " needs " + EditSessionOption + "<id>")
	case len(message.Args) > 0:
		return errors.New(message.Command + " takes no base revision")
	case noteSeen:
		return errors.New(message.Command + " takes no note")
	}
	return nil
}

// ReplacePayload is a replace request's payload: the exact text to find and
// its replacement. All replaces every match instead of exactly one.
type ReplacePayload struct {
	Old string `json:"old"`
	New string `json:"new"`
	All bool   `json:"all"`
}

// EncodeReplace is a replace request's payload text.
func EncodeReplace(payload ReplacePayload) string {
	data, _ := json.Marshal(payload) // a struct of strings and a bool always marshals
	return string(data)
}

// DecodeReplace reads a replace request's payload.
func DecodeReplace(text string) (ReplacePayload, error) {
	var payload ReplacePayload
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		return payload, errors.New("bad replace payload: " + err.Error())
	}
	if payload.Old == "" {
		return payload, errors.New("replace needs the old text")
	}
	return payload, nil
}

// sessionKindOf is the session kind a first-line token names, SessionNone
// when it is no session option.
func sessionKindOf(field string) SessionKind {
	for _, kind := range sessionKinds {
		if strings.HasPrefix(field, kind.Option()) {
			return kind
		}
	}
	return SessionNone
}

// trimBlankLines joins lines, dropping the blank (whitespace-only) ones at
// either end and the trailing spaces of each.
func trimBlankLines(lines []string) string {
	first, last := 0, len(lines)
	for first < last && strings.TrimSpace(lines[first]) == "" {
		first++
	}
	for last > first && strings.TrimSpace(lines[last-1]) == "" {
		last--
	}
	kept := make([]string, 0, last-first)
	for _, line := range lines[first:last] {
		kept = append(kept, strings.TrimRight(line, " \t"))
	}
	return strings.Join(kept, "\n")
}

// checkBaseArgument allows at most one argument, the base revision. One
// starting with "-" would reach git's argv as an option, not an operand.
func checkBaseArgument(command string, args []string) error {
	if len(args) > 1 {
		return errors.New(command + " takes at most one argument: the base revision — put paths on the next lines")
	}
	if len(args) == 1 && strings.HasPrefix(args[0], "-") {
		return errors.New("bad revision: " + args[0])
	}
	return nil
}

// Base is the request's base revision, "" for HEAD.
func (message Message) Base() string {
	if len(message.Args) == 1 {
		return message.Args[0]
	}
	return ""
}

// Replies are one line: "ok <detail>", "error <reason>", or for a session
// the user or agent closed, "ended <reason>".
func OkReply(detail string) string {
	return strings.TrimSpace("ok " + oneLine(detail))
}

func ErrorReply(reason string) string {
	return "error " + oneLine(reason)
}

// EndedReply says a guided session is over; the sender must stop.
func EndedReply(reason string) string {
	return "ended " + oneLine(reason)
}

// BodyReply is an ok reply carrying content: "ok <bytes>", a newline, then
// the content byte for byte. Only a reply read with SendForBody has a body.
func BodyReply(body string) string {
	return "ok " + strconv.Itoa(len(body)) + "\n" + body
}

// BodyReplyWith is a BodyReply whose first line also says where the content
// came from: "ok <bytes> <detail>".
func BodyReplyWith(body, detail string) string {
	return strings.TrimSpace("ok "+strconv.Itoa(len(body))+" "+oneLine(detail)) + "\n" + body
}

// SplitBodyReply splits a raw reply into its first line and, for a
// BodyReply, its body. A reply without a well-formed body comes back as its
// trimmed text with hasBody false.
func SplitBodyReply(raw string) (line, body string, hasBody bool) {
	header, rest, found := strings.Cut(raw, "\n")
	sizeText, _, _ := strings.Cut(strings.TrimPrefix(header, "ok "), " ")
	size, err := strconv.Atoi(sizeText)
	if !found || !strings.HasPrefix(header, "ok ") || err != nil || size < 0 || size > len(rest) {
		return strings.TrimSpace(raw), "", false
	}
	return header, rest[:size], true
}

// BodyDetail is the detail of a BodyReplyWith first line, "" for none.
func BodyDetail(line string) string {
	_, detail, _ := strings.Cut(strings.TrimPrefix(line, "ok "), " ")
	return detail
}

// IsEndedReply reports whether a reply line ends a session.
func IsEndedReply(reply string) bool {
	return reply == "ended" || strings.HasPrefix(reply, "ended ")
}

// IsErrorReply reports whether a reply line is an error.
func IsErrorReply(reply string) bool {
	return reply == "error" || strings.HasPrefix(reply, "error ")
}

func oneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}
