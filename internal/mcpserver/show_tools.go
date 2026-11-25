package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nickooan/ntee-editor/internal/extmsg"
)

// The show tools put code in front of the user without changing it: one
// place with a note (show_code), or a walkthrough — a live review session
// the agent adds stops to, each a file with a note on a line, which the
// editor's auto-drive follows.
const (
	toolShow            = "show_code"
	toolWalkthrough     = "walkthrough_step"
	toolWalkthroughEnd  = "walkthrough_end"
	toolWalkthroughWait = "walkthrough_wait"
)

func isShowTool(name string) bool {
	switch name {
	case toolShow, toolWalkthrough, toolWalkthroughEnd, toolWalkthroughWait:
		return true
	}
	return false
}

func showNeedsPath(name string) bool {
	return name == toolShow || name == toolWalkthrough
}

// walkthroughFor is the walkthrough a call goes to: a step continues the
// current one or starts the next; end continues the current one; wait
// takes the current one, else the one that ended last.
func (tools *editTools) walkthroughFor(name string) (string, error) {
	tools.mu.Lock()
	defer tools.mu.Unlock()
	switch name {
	case toolWalkthrough:
		if tools.walkthrough == "" {
			tools.walkthroughCount++
			tools.walkthrough = tools.prefix + "-walk-" + strconv.Itoa(tools.walkthroughCount)
		}
		return tools.walkthrough, nil
	case toolWalkthroughEnd:
		if tools.walkthrough == "" {
			return "", fmt.Errorf("no walkthrough to end — send its stops with %s first", toolWalkthrough)
		}
		return tools.walkthrough, nil
	}
	if walkthrough := tools.walkthrough; walkthrough != "" {
		return walkthrough, nil
	}
	if tools.lastWalkthrough != "" {
		return tools.lastWalkthrough, nil
	}
	return "", fmt.Errorf("no walkthrough to wait on — send stops with %s and call %s first", toolWalkthrough, toolWalkthroughEnd)
}

// walkthroughOver lets the walkthrough go: the agent ended it, or the user
// closed it, so the next step starts a new one.
func (tools *editTools) walkthroughOver(walkthrough string) {
	tools.mu.Lock()
	defer tools.mu.Unlock()
	if tools.walkthrough == walkthrough {
		tools.walkthrough = ""
	}
	tools.lastWalkthrough = walkthrough
}

// checkShowArguments refuses a call the editor would refuse, before
// anything is sent.
func checkShowArguments(name string, arguments toolArguments) error {
	if showNeedsPath(name) && strings.TrimSpace(arguments.Path) == "" {
		return fmt.Errorf("%s needs path: the file, relative to working_dir or absolute", name)
	}
	switch {
	case arguments.Line < 0:
		return errors.New("line starts at 1")
	case name == toolShow && arguments.Line == 0:
		return fmt.Errorf("%s needs line: the line to show (and to put the note beside)", name)
	case len(arguments.Note) > extmsg.MaxLineNoteBytes:
		return fmt.Errorf("note is too long (max %d KiB) — keep it to a few short lines, and split a long story across stops", extmsg.MaxLineNoteBytes>>10)
	case strings.ContainsAny(arguments.Base, " \t\n"):
		return errors.New("base is one revision (a commit, tag or branch), without spaces")
	case arguments.Base != "" && name != toolWalkthrough:
		return fmt.Errorf("%s takes no base — only the first %s does", name, toolWalkthrough)
	case arguments.TimeoutSeconds > maxWaitSeconds:
		return fmt.Errorf("timeout_seconds is at most %d", maxWaitSeconds)
	}
	return nil
}

// callShow sends a show tool's request: show_code opens the file at its
// line with the note; a walkthrough step reviews one file in the
// walkthrough's live review session, the note on its line.
func (tools *editTools) callShow(ctx context.Context, name string, arguments toolArguments) toolResult {
	if err := checkShowArguments(name, arguments); err != nil {
		return textResult(err.Error()+" — nothing was sent", true)
	}
	var paths []string
	if showNeedsPath(name) {
		target := tools.resolve(arguments.Path)
		if arguments.Line > 0 {
			target += "#L" + strconv.Itoa(arguments.Line)
		}
		paths = []string{target}
	}
	socketPath, err := tools.config.FindSocket(paths)
	if err != nil {
		return textResult(noShowEditorText(err), true)
	}
	if name == toolShow {
		request := extmsg.BuildRequest(extmsg.CommandOpen, "", "", paths, arguments.Note)
		line, _, _, err := extmsg.SendEdit(ctx, socketPath, extmsg.CommandOpen, request, tools.config.RequestTimeout)
		if err != nil {
			return textResult("cannot reach the editor: "+err.Error(), true)
		}
		return textResult(showCodeText(line), extmsg.IsErrorReply(line))
	}

	walkthrough, err := tools.walkthroughFor(name)
	if err != nil {
		return textResult(err.Error(), true)
	}
	option := extmsg.SessionReview.Option() + walkthrough
	command, request, timeout := "", "", tools.config.RequestTimeout
	switch name {
	case toolWalkthrough:
		command, request = extmsg.CommandReview, extmsg.BuildRequest(extmsg.CommandReview, arguments.Base, option, paths, arguments.Note)
	case toolWalkthroughEnd:
		command, request = extmsg.CommandEnd, extmsg.BuildRequest(extmsg.CommandEnd, "", option, nil, "")
	case toolWalkthroughWait:
		seconds := arguments.TimeoutSeconds
		if seconds <= 0 {
			seconds = defaultWaitSeconds
		}
		budget := time.Duration(min(seconds, maxWaitSeconds)) * time.Second
		command, timeout = extmsg.CommandWait, budget+waitReplyMargin
		request = extmsg.BuildRequest(extmsg.CommandWait, "", option+" "+extmsg.WaitOption+budget.String(), nil, "")
	}
	line, _, _, err := extmsg.SendEdit(ctx, socketPath, command, request, timeout)
	switch {
	case err != nil && name == toolWalkthroughWait && os.IsTimeout(err):
		return textResult(walkthroughWaitingText(walkthrough), false)
	case err != nil:
		return textResult("cannot reach the editor: "+err.Error()+walkthroughTag(walkthrough), true)
	}
	switch {
	case extmsg.IsEndedReply(line):
		tools.walkthroughOver(walkthrough)
		return textResult(forShowTools(line)+" — the user closed the walkthrough: stop sending stops (a new "+toolWalkthrough+" starts a new walkthrough, only if they ask)"+walkthroughTag(walkthrough), false)
	case name == toolWalkthroughEnd && !extmsg.IsErrorReply(line):
		tools.walkthroughOver(walkthrough)
		return textResult(forShowTools(line)+" — call "+toolWalkthroughWait+" to hear when the user is done, if you need to"+walkthroughTag(walkthrough), false)
	case name == toolWalkthroughWait && (strings.HasPrefix(line, "ok waiting") || strings.HasPrefix(line, "waiting")):
		return textResult(walkthroughWaitingText(walkthrough), false)
	case name == toolWalkthroughWait && strings.HasPrefix(line, "ok finished"):
		tools.walkthroughOver(walkthrough)
	}
	return textResult(forShowTools(line)+walkthroughTag(walkthrough), extmsg.IsErrorReply(line))
}

func walkthroughTag(walkthrough string) string {
	return "\nwalkthrough: " + walkthrough
}

func walkthroughWaitingText(walkthrough string) string {
	return "still waiting — the user is still going through walkthrough " + walkthrough + "; call " + toolWalkthroughWait + " again" + walkthroughTag(walkthrough)
}

// showCodeText is show_code's reply: shown, or waiting for the user (who was
// busy and is asked first), in plain terms.
func showCodeText(line string) string {
	if strings.HasPrefix(line, "waiting") {
		return "the user was busy, so the editor asks them first — it shows the code once they accept; carry on"
	}
	return forShowTools(line)
}

func noShowEditorText(err error) string {
	if errors.Is(err, extmsg.ErrNoEditor) {
		return "no ntee editor is open on this project — nothing was shown; show the code in your reply instead (file:line and a short excerpt)"
	}
	return "cannot find the ntee editor (" + err.Error() + ") — nothing was shown; show the code in your reply instead"
}

var (
	cliSessionWaitHint = regexp.MustCompile(`run: ntee --msg --msg-review-session \S+ wait`)
	cliSessionEndHint  = regexp.MustCompile(`end it first: ntee --msg --msg-\S+ \S+ end`)
)

// showHintsForTools rewrites the editor's ntee --msg hints for the show
// tools.
var showHintsForTools = strings.NewReplacer(
	"leave --msg-base off after the first file", "leave base off after the first "+toolWalkthrough,
	"send its reviews with --msg-review-session", "send its stops with "+toolWalkthrough,
)

func forShowTools(reply string) string {
	reply = cliSessionWaitHint.ReplaceAllString(reply, "call "+toolWalkthroughWait)
	reply = cliSessionEndHint.ReplaceAllString(reply, "end it first (the user or the agent running it must end it)")
	return showHintsForTools.Replace(reply)
}
