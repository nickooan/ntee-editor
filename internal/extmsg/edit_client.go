package extmsg

import (
	"context"
	"fmt"
	"time"
)

// EditRequest is the wire text of one edit session request: the command
// and the session option on the first line, the file (when the command
// names one) on the next, and for write and replace the payload after the
// separator line. A request over MaxMessageBytes is refused before sending.
func EditRequest(command, session, absolutePath, payload string) (string, error) {
	request := command + " " + EditSessionOption + session + "\n"
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

// EditWaitRequest is an edit session's wait carrying the sender's budget, so
// the editor answers "ok waiting …" before the sender gives up.
func EditWaitRequest(session string, budget time.Duration) string {
	return CommandWait + " " + EditSessionOption + session + " " + WaitOption + budget.String() + "\n"
}

// SendEdit delivers one edit session request. show and status answer with a
// body (BodyReply), returned byte for byte; every other reply is one line.
// Cancelling ctx closes the connection, so a pending wait stops at once.
func SendEdit(ctx context.Context, socketPath, command, request string, timeout time.Duration) (line, body string, hasBody bool, err error) {
	if command == CommandShow || command == CommandStatus {
		reply, err := exchange(ctx, socketPath, request, timeout, MaxBodyBytes+64)
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
