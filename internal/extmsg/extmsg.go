// Package extmsg is the external-message channel of a running editor: a unix
// socket per project over which other programs (coding agents, scripts) ask
// the editor to open a file or review a set of changed files.
package extmsg

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Message is one parsed request: the command line, then one path per line,
// then for an open (or a review session's review) an optional note after a
// LineNoteSeparator line.
type Message struct {
	Command     string      // CommandOpen, CommandReview, CommandEnd or CommandWait
	Args        []string    // tokens after the command, options aside: an optional base revision
	Paths       []string    // payload lines, trimmed, blanks dropped; may carry a #L<n> anchor
	LineNote    string      // free-form text shown next to the anchored line (or the file's first change), "" = none
	Session     string      // the session option's id, "" = none
	SessionKind SessionKind // which session option named it; SessionNone without one
}

const (
	CommandOpen   = "open"
	CommandReview = "review"
	CommandEnd    = "end"  // session: the agent closes it
	CommandWait   = "wait" // session: wait for the current step's answer
)

// SessionKind is what a session shows. The first-line option that names the
// session picks it.
type SessionKind uint8

const (
	SessionNone   SessionKind = iota
	SessionNote               // --session=: each step opens its file with the note
	SessionDiff               // --diff-session=: each step opens the file's diff with the note
	SessionReview             // --review-session=: each review adds one file to a live review
)

const (
	SessionOption       = "--session="
	DiffSessionOption   = "--diff-session="
	ReviewSessionOption = "--review-session="
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
	}
	return ""
}

var sessionKinds = []SessionKind{SessionNote, SessionDiff, SessionReview}

// sessionOptionsText lists the session options for error messages.
const sessionOptionsText = SessionOption + "<id>, " + DiffSessionOption + "<id> or " + ReviewSessionOption + "<id>"

var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

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
)

// LineNoteSeparator is the line that ends the paths; everything after it is
// the note, verbatim.
const LineNoteSeparator = "---"

// Parse reads a request: the first non-blank line is the command and its
// arguments, every following non-blank line one path, until an optional
// LineNoteSeparator line after which the rest is the note. Errors are
// user-facing and go back to the sender verbatim.
func Parse(text string) (Message, error) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var message Message
	commandSeen, noteSeen := false, false
	for index, line := range lines {
		line = strings.TrimSpace(line)
		if commandSeen && line == LineNoteSeparator {
			// Blank lines and indentation inside the note are its formatting.
			message.LineNote = trimBlankLines(lines[index+1:])
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
	default:
		return Message{}, fmt.Errorf("unknown command %q — use open, review, end or wait", message.Command)
	}
	return message, nil
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
