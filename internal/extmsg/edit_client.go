package extmsg

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
)

// EditRequest is the wire text of one edit session request: the command
// and the session option on the first line, the file (when the command
// names one) on the next, and for write and replace the payload after the
// separator line. read and staged go without a session when session is "";
// agent names the sender (write, replace, end, status), "" for none. A
// request over MaxMessageBytes is refused before sending.
func EditRequest(command, session, agent, absolutePath, payload string) (string, error) {
	request := command
	if session != "" {
		request += " " + EditSessionOption + session
	}
	if agent != "" && slices.Contains([]string{CommandWrite, CommandReplace, CommandEnd, CommandStatus, CommandCheck}, command) {
		request += " " + AgentOption + agent
	}
	request += "\n"
	if absolutePath != "" {
		request += absolutePath + "\n"
	}
	if command == CommandWrite || command == CommandReplace {
		request += LineNoteSeparator + "\n" + payload
	}
	if len(request) > MaxMessageBytes {
		return "", fmt.Errorf("%s is too large to send (max %d MiB per request) — stage the file in smaller replaces", command, MaxMessageBytes>>20)
	}
	return request, nil
}

// WithExpect adds the version an edit was made on to a write or replace
// request's first line; "" leaves the request as it is.
func WithExpect(request, version string) string {
	if version == "" {
		return request
	}
	first, rest, _ := strings.Cut(request, "\n")
	return first + " " + ExpectOption + version + "\n" + rest
}

// bodyHeaderBytes is room for a body reply's first line.
const bodyHeaderBytes = 1 << 10

// SandboxRequest is an edit session's sandbox request: mode is "",
// SandboxFresh or SandboxDrop; include lists more folders to copy in, one
// per line.
func SandboxRequest(session, mode string, include []string) string {
	request := CommandSandbox + " " + EditSessionOption + session
	if mode != "" {
		request += " " + mode
	}
	request += "\n"
	for _, folder := range include {
		request += folder + "\n"
	}
	return request
}

// ReportRequest is an agent's report on a finished session's git work:
// report.Result is ReportDone, ReportBlocked or ReportDeclined.
func ReportRequest(session string, report ReportArgs, note string) string {
	request := CommandReport + " " + EditSessionOption + session + " " + report.Result
	if report.Commit != "" {
		request += " " + ReportCommitArg + report.Commit
	}
	if report.PullRequest != "" {
		request += " " + ReportPullRequestArg + report.PullRequest
	}
	request += "\n"
	if note != "" {
		request += LineNoteSeparator + "\n" + note
	}
	return request
}

// CheckRequest is an agent's check of its finalising session: result is
// CheckPass, CheckFail or CheckSkip; the note (what it ran, what failed)
// follows the separator line.
func CheckRequest(session, agent, result, note string) string {
	request := CommandCheck + " " + EditSessionOption + session
	if agent != "" {
		request += " " + AgentOption + agent
	}
	request += " " + result + "\n"
	if note != "" {
		request += LineNoteSeparator + "\n" + note
	}
	return request
}

// EditWaitRequest is an edit session's wait carrying the sender's budget, so
// the editor answers "ok waiting …" before the sender gives up.
func EditWaitRequest(session string, budget time.Duration) string {
	return CommandWait + " " + EditSessionOption + session + " " + WaitOption + budget.String() + "\n"
}

// SendEdit delivers one edit session request. show, read and status answer
// with a body (BodyReply), returned byte for byte; every other reply is one
// line.
// Cancelling ctx closes the connection, so a pending wait stops at once.
func SendEdit(ctx context.Context, socketPath, command, request string, timeout time.Duration) (line, body string, hasBody bool, err error) {
	if command == CommandShow || command == CommandRead || command == CommandStatus {
		reply, err := exchange(ctx, socketPath, request, timeout, MaxBodyBytes+bodyHeaderBytes)
		if err != nil {
			return "", "", false, err
		}
		line, body, hasBody = SplitBodyReply(string(reply))
		return line, body, hasBody, nil
	}
	reply, err := exchange(ctx, socketPath, request, timeout, replyLimit)
	if err != nil {
		return "", "", false, err
	}
	return trimReply(reply), "", false, nil
}
