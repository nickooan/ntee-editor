package extmsg

import (
	"regexp"
	"strings"
	"time"
)

// How long an edit session's sender waits for each kind of reply — the same
// for ntee --msg and the MCP tools. EditorEndWait is how long the editor
// itself holds an end for the UI; it stays under EditEndTimeout, so the
// sender hears the editor's answer, never its own timeout.
const (
	EditRequestTimeout = 10 * time.Second // write, replace, show, status, discard, read
	EditEndTimeout     = 45 * time.Second // the editor may apply the files before answering
	EditSandboxTimeout = 60 * time.Second // the editor may be copying whole repos
	EditorEndWait      = 40 * time.Second
)

// Edit session replies are written for people — an agent on the command
// line reads them as they are — so their wording is free to change, except
// for the parts below: the MCP tools and the Claude Code hook act on them.
// The editor writes them with these builders and phrases, and the clients
// read them with these parsers, so rewording one side rewords both.
const (
	// PhraseUserReviewing: an end handed the session to the user.
	PhraseUserReviewing = "the user is reviewing"
	// PhraseStartNewSession: the session takes no more edits.
	PhraseStartNewSession = "start a new session"
	// PhraseFinalising, PhraseBackToEditing, PhraseSetAside: what a wait
	// answered "waiting" for.
	PhraseFinalising    = "is finalising"
	PhraseBackToEditing = "back to editing"
	PhraseSetAside      = "aside for later"
)

const (
	versionMarker     = " · version "
	stagedMarker      = "staged · edit session "
	staleMarker       = " · stale: "
	alsoEditingMarker = " · also editing "
	notWrittenSuffix  = " · not written"
)

var replyVersionPattern = regexp.MustCompile(`· version ([0-9a-f]{12})\b`)

var stalePattern = regexp.MustCompile(` · stale: (.*?)(?: · also editing |$)`)

// WithVersion adds the file version an edit or a read was made on.
func WithVersion(text, version string) string { return text + versionMarker + version }

// VersionIn is the file version a reply names, "" for none.
func VersionIn(line string) string {
	if match := replyVersionPattern.FindStringSubmatch(line); match != nil {
		return match[1]
	}
	return ""
}

// ReadSourceDetail says where a read's content came from: a session's
// staged copy, or the disk (session "").
func ReadSourceDetail(session string) string {
	if session == "" {
		return "disk"
	}
	return stagedMarker + session + notWrittenSuffix
}

// StagedSessionIn is the session a read's detail says its content came
// from; false for the disk.
func StagedSessionIn(detail string) (string, bool) {
	rest, staged := strings.CutPrefix(detail, stagedMarker)
	if !staged {
		return "", false
	}
	session, _, _ := strings.Cut(rest, " ")
	return session, true
}

// StagedReply answers a staged request: the session on top of the file.
func StagedReply(session string) string { return OkReply(stagedMarker + session) }

// StagedReplySession is the session a staged reply names; false for "not
// staged", an error, or anything else.
func StagedReplySession(line string) (string, bool) {
	session, staged := strings.CutPrefix(line, "ok "+stagedMarker)
	if !staged || session == "" {
		return "", false
	}
	return session, true
}

// WithStale adds why a session's own copy can no longer be applied.
func WithStale(detail, note string) string { return detail + staleMarker + note }

// StaleIn is the stale note in a read's detail; false for none.
func StaleIn(detail string) (string, bool) {
	if match := stalePattern.FindStringSubmatch(detail); match != nil {
		return match[1], true
	}
	return "", false
}

// WithAlsoEditing adds the other open sessions changing the file's repo,
// sessions already formatted ("S (a, b); T (c)").
func WithAlsoEditing(detail, repo, sessions string) string {
	return detail + alsoEditingMarker + repo + ": " + sessions
}

// AlsoEditingIn is the repo and sessions WithAlsoEditing added; false for
// none.
func AlsoEditingIn(detail string) (repo, sessions string, found bool) {
	_, others, found := strings.Cut(detail, alsoEditingMarker)
	if !found {
		return "", "", false
	}
	repo, sessions, _ = strings.Cut(others, ": ")
	return repo, sessions, true
}
