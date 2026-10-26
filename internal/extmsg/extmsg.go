// Package extmsg is the external-message channel of a running editor: a unix
// socket per project over which other programs (coding agents, scripts) ask
// the editor to open a file or review a set of changed files.
package extmsg

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Message is one parsed request: the command line, then one path per line,
// then for an open an optional note after a LineNoteSeparator line.
type Message struct {
	Command  string   // CommandOpen or CommandReview
	Args     []string // tokens after the command: an optional base revision
	Paths    []string // payload lines, trimmed, blanks dropped; may carry a #L<n> anchor
	LineNote string   // open: free-form text shown next to the anchored line, "" = none
}

const (
	CommandOpen   = "open"
	CommandReview = "review"
)

const (
	// MaxMessageBytes caps one request; anything longer is refused unread.
	MaxMessageBytes = 1 << 20
	// MaxReviewPaths caps the files of one review request.
	MaxReviewPaths = 500
	// ReadTimeout bounds how long a sender may take to deliver its request.
	ReadTimeout = 2 * time.Second
	// MaxLineNoteBytes caps the note of one open request.
	MaxLineNoteBytes = 16 << 10
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
			message.Args = fields[1:]
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
		if noteSeen {
			return Message{}, errors.New("review takes no note — a note goes with open <file>#L<n>")
		}
	default:
		return Message{}, fmt.Errorf("unknown command %q — use open or review", message.Command)
	}
	return message, nil
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

// Replies are one line: "ok <detail>" or "error <reason>".
func OkReply(detail string) string {
	return strings.TrimSpace("ok " + oneLine(detail))
}

func ErrorReply(reason string) string {
	return "error " + oneLine(reason)
}

// IsErrorReply reports whether a reply line is an error.
func IsErrorReply(reply string) bool {
	return reply == "error" || strings.HasPrefix(reply, "error ")
}

func oneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}
