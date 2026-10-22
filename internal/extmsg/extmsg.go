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

// Message is one parsed request: the command line, then one path per line.
type Message struct {
	Command string   // CommandOpen or CommandReview
	Args    []string // tokens after the command (review: an optional base revision)
	Paths   []string // payload lines, trimmed, blanks dropped; may carry a #L<n> anchor
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
)

// Parse reads a request: the first non-blank line is the command and its
// arguments, every following non-blank line one path. Errors are user-facing
// and go back to the sender verbatim.
func Parse(text string) (Message, error) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var message Message
	commandSeen := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
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
		if len(message.Args) > 0 {
			return Message{}, errors.New("open takes no arguments — put the path on the next line")
		}
		if len(message.Paths) != 1 {
			return Message{}, fmt.Errorf("open needs exactly one path, got %d", len(message.Paths))
		}
	case CommandReview:
		if len(message.Args) > 1 {
			return Message{}, errors.New("review takes at most one argument: the base revision")
		}
		if len(message.Args) == 1 && strings.HasPrefix(message.Args[0], "-") {
			return Message{}, errors.New("bad revision: " + message.Args[0])
		}
		if len(message.Paths) == 0 {
			return Message{}, errors.New("review needs at least one path")
		}
		if len(message.Paths) > MaxReviewPaths {
			return Message{}, fmt.Errorf("review takes at most %d paths, got %d", MaxReviewPaths, len(message.Paths))
		}
	default:
		return Message{}, fmt.Errorf("unknown command %q — use open or review", message.Command)
	}
	return message, nil
}

// Base is a review request's base revision, "" for HEAD.
func (message Message) Base() string {
	if message.Command == CommandReview && len(message.Args) == 1 {
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
