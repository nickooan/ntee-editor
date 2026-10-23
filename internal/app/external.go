package app

import (
	"cmp"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/extmsg"
	"github.com/nickooan/ntee-editor/internal/store"
)

// externalState is the editor's side of external messages: the stores, the
// socket's status, the settings as last loaded (for the Ctrl+T panel), and
// the request waiting for the user's confirmation.
type externalState struct {
	settingsStore  store.ExternalSettingsStore
	historyStore   store.ExternalHistoryStore
	socketPath     string
	socketProblem  string // why the socket isn't listening; "" = listening
	settings       store.ExternalSettings
	settingsLoaded bool
	savedCount     int // records in the history, for the panel
	pending        *pendingExternal
}

// pendingExternal is a resolved request: what it targets, and for an open,
// which repo the editor must be rooted at to reach the file.
type pendingExternal struct {
	record      store.ExternalRecord
	replay      bool
	rootRel     string // open: the file relative to the target root
	line        int    // open: 1-based line, 0 = remembered position
	targetRepo  string // open: the activeRepo to switch to ("" = the workspace)
	reviewPaths []string
	base        string // the revision to compare with, "" = HEAD (diffBaseOf)
}

// WithExternal attaches the external-message stores and the socket's status
// (socketProblem "" means listening on socketPath).
func (m Model) WithExternal(settingsStore store.ExternalSettingsStore, historyStore store.ExternalHistoryStore, socketPath, socketProblem string) Model {
	m.external.settingsStore = settingsStore
	m.external.historyStore = historyStore
	m.external.socketPath = socketPath
	m.external.socketProblem = socketProblem
	return m
}

// externalStatusSavedMsg lands after a record's status was written; open
// views over the history reload.
type externalStatusSavedMsg struct{ err error }

// handleExternalMessage takes a request from the socket. In the plain query
// view it is carried out at once; anywhere else — editing, a bar, a special
// view, an overlay — the user is asked first. A newer request replaces an
// unanswered one.
func (m Model) handleExternalMessage(msg externalMessageMsg) (tea.Model, tea.Cmd) {
	pending, problem := m.resolveExternal(msg.record)
	pending.replay = msg.replay
	if problem != "" {
		sendExternalReply(msg.reply, extmsg.ErrorReply(problem))
		m.errText = "external " + msg.record.Command + ": " + problem
		return m, m.saveExternalStatusCmd(pending, store.ExternalFailed, problem)
	}

	var cmds []tea.Cmd
	if previous := m.external.pending; previous != nil {
		cmds = append(cmds, m.saveExternalStatusCmd(*previous, store.ExternalSuperseded, "a newer request arrived"))
		m.external.pending = nil
		if m.choice.kind == choiceExternalRequest {
			m.choice = choiceOverlay{}
		}
	}
	if m.mode == modeQuery && !m.overlayOpen() {
		next, cmd := m.carryOutExternal(pending)
		sendExternalReply(msg.reply, next.externalOutcome(pending))
		return next, tea.Batch(append(cmds, cmd)...)
	}
	if m.choice.kind == choiceRemovePath {
		// The request takes the one modal slot; an unanswered :rm is cancelled,
		// never confirmed.
		m.confirmRm, m.confirmRmDir = "", false
		m.notice = "rm cancelled"
	}
	m.external.pending = &pending
	m.choice = m.externalChoice(pending)
	sendExternalReply(msg.reply, "waiting for the user to confirm")
	return m, tea.Batch(cmds...)
}

func sendExternalReply(reply chan<- string, outcome string) {
	if reply != nil {
		reply <- outcome
	}
}

// resolveExternal works out what a request targets. For an open, the file
// stays reachable from the current root when it lies under it; otherwise the
// editor re-roots at the file's own repo (or the whole workspace when no
// repo holds it).
func (m Model) resolveExternal(record store.ExternalRecord) (pendingExternal, string) {
	pending := pendingExternal{record: record, base: diffBaseOf(record)}
	switch record.Command {
	case extmsg.CommandOpen:
		if len(record.Paths) != 1 {
			return pending, "open needs exactly one path"
		}
		workspaceRel, line := splitLineAnchor(record.Paths[0])
		pending.line = line
		switch {
		case m.workspaceIsRepo || m.activeRepo == "":
			pending.targetRepo = ""
		case pathUnderRepo(workspaceRel, m.activeRepo):
			pending.targetRepo = m.activeRepo
		case !m.workspaceReposScanned:
			return pending, "the workspace is still being scanned — try again shortly"
		default:
			pending.targetRepo = m.deepestRepoFor(workspaceRel)
		}
		pending.rootRel = workspaceRel
		if pending.targetRepo != "" {
			pending.rootRel = strings.TrimPrefix(workspaceRel, pending.targetRepo+"/")
		}
	case extmsg.CommandReview:
		for _, path := range record.Paths {
			workspaceRel, _ := splitLineAnchor(path)
			pending.reviewPaths = append(pending.reviewPaths, workspaceRel)
		}
	default:
		return pending, "unknown command " + record.Command
	}
	return pending, ""
}

// diffBaseOf is the record's base in the diff and review views' terms, where
// "" means HEAD.
func diffBaseOf(record store.ExternalRecord) string {
	if record.Base == store.ExternalDefaultBase {
		return ""
	}
	return record.Base
}

// deepestRepoFor is the innermost nested repo holding a workspace-relative
// path, "" when none does.
func (m Model) deepestRepoFor(workspaceRel string) string {
	best := ""
	for _, repo := range m.workspaceRepos {
		if pathUnderRepo(workspaceRel, repo) && len(repo) > len(best) {
			best = repo
		}
	}
	return best
}

// carryOutExternal performs a resolved request from wherever the user is.
func (m Model) carryOutExternal(pending pendingExternal) (Model, tea.Cmd) {
	if pending.record.Command == extmsg.CommandReview {
		m, cmd := m.enterReview(pending.reviewPaths, pending.base)
		if m.mode != modeReview {
			return m, tea.Batch(cmd, m.saveExternalStatusCmd(pending, store.ExternalFailed, m.errText))
		}
		return m, tea.Batch(cmd, m.saveExternalStatusCmd(pending, store.ExternalReviewed, ""))
	}

	m = m.leaveToEditOrQuery()
	var cmds []tea.Cmd
	if pending.targetRepo != m.activeRepo {
		var cmd tea.Cmd
		m, cmd = m.switchRoot(pending.targetRepo)
		cmds = append(cmds, cmd)
	}
	m.errText = ""
	m = m.openFileAtLine(pending.rootRel, pending.line)
	if m.mode != modeEdit || m.openRel != pending.rootRel {
		problem := m.errText
		if problem == "" {
			problem = "cannot open " + pending.rootRel
		}
		return m, tea.Batch(append(cmds, m.saveExternalStatusCmd(pending, store.ExternalFailed, problem))...)
	}
	note := ""
	if pending.base != "" {
		// A named revision lands in the diff of the file against it. A refusal
		// (no repo, bad revision) leaves the file open with the reason shown.
		next, cmd := m.enterDiff(pending.base)
		m = next.(Model)
		cmds = append(cmds, cmd)
		if m.mode != modeDiff {
			note = "opened without the diff: " + m.errText
		}
	}
	return m, tea.Batch(append(cmds, m.saveExternalStatusCmd(pending, store.ExternalOpened, note))...)
}

// externalOutcome is the sender's reply after a request was carried out at
// once.
func (m Model) externalOutcome(pending pendingExternal) string {
	switch {
	case m.mode == modeReview && pending.record.Command == extmsg.CommandReview:
		return fmt.Sprintf("reviewing %d files", len(m.review.files))
	case m.mode == modeDiff && m.openRel == pending.rootRel:
		return fmt.Sprintf("opened %s at line %d, diff against %s", pending.record.Paths[0], m.edit.cy+1, pending.base)
	case m.mode == modeEdit && m.openRel == pending.rootRel:
		return fmt.Sprintf("opened %s at line %d", pending.record.Paths[0], m.edit.cy+1)
	case m.errText != "":
		return extmsg.ErrorReply(m.errText)
	default:
		return "delivered"
	}
}

// resolveExternalChoice applies the user's answer to the confirm overlay.
func (m Model) resolveExternalChoice(accept bool) (Model, tea.Cmd) {
	pending := m.external.pending
	m.external.pending = nil
	if pending == nil {
		return m, nil
	}
	if !accept {
		m.notice = "external " + pending.record.Command + " ignored"
		return m, m.saveExternalStatusCmd(*pending, store.ExternalIgnored, "")
	}
	return m.carryOutExternal(*pending)
}

// externalChoice is the confirm overlay for a request that arrived while the
// user was busy: what it opens, where, and what happens to their work.
func (m Model) externalChoice(pending pendingExternal) choiceOverlay {
	choice := choiceOverlay{kind: choiceExternalRequest, title: "External request · " + pending.record.Command}
	if pending.record.Command == extmsg.CommandReview {
		repos := map[string]bool{}
		for _, path := range pending.reviewPaths {
			repos[m.deepestRepoFor(path)] = true
		}
		summary := fmt.Sprintf("%d files", len(pending.reviewPaths))
		if len(repos) > 1 {
			summary += fmt.Sprintf(" across %d repos", len(repos))
		}
		choice.body = append(choice.body, overlayLine{text: summary + " · against " + cmp.Or(pending.base, store.ExternalDefaultBase), style: overlayLineStrong})
		const shown = 5
		for _, path := range pending.reviewPaths[:min(shown, len(pending.reviewPaths))] {
			choice.body = append(choice.body, overlayLine{text: "  " + path})
		}
		if extra := len(pending.reviewPaths) - shown; extra > 0 {
			choice.body = append(choice.body, overlayLine{text: fmt.Sprintf("  +%d more", extra), style: overlayLineDim})
		}
		choice.choices = []string{"Review", "Ignore"}
	} else {
		target := pending.record.Paths[0]
		choice.body = append(choice.body, overlayLine{text: target, style: overlayLineStrong})
		if pending.base != "" {
			choice.body = append(choice.body, overlayLine{text: "diff against " + pending.base, style: overlayLineHighlight})
		}
		if pending.targetRepo != m.activeRepo {
			choice.body = append(choice.body, overlayLine{
				text:  "switches repo: " + m.repoDisplayName(m.activeRepo) + " → " + m.repoDisplayName(pending.targetRepo),
				style: overlayLineHighlight,
			})
		} else if !m.workspaceIsRepo {
			choice.body = append(choice.body, overlayLine{text: "stays in: " + m.repoDisplayName(m.activeRepo), style: overlayLineDim})
		}
		if m.openFile != nil && m.edit.dirty {
			choice.body = append(choice.body, overlayLine{text: "unsaved edits in " + m.openRel + " are kept as a draft", style: overlayLineDim})
		}
		choice.choices = []string{"Jump", "Ignore"}
	}
	if m.opMode.run != nil {
		choice.body = append(choice.body, overlayLine{text: "a running op-command will be stopped", style: overlayLineDim})
	}
	return choice
}

// repoDisplayName names a repo for the user: its workspace-relative dir, or
// the workspace's own name for the workspace itself.
func (m Model) repoDisplayName(repo string) string {
	if repo == "" {
		return m.workspaceLabel()
	}
	return repo + "/"
}

// saveExternalStatusCmd records what became of a request. Replays from the
// history view leave the stored record as it was.
func (m Model) saveExternalStatusCmd(pending pendingExternal, status store.ExternalStatus, note string) tea.Cmd {
	historyStore := m.external.historyStore
	if pending.replay || historyStore == nil {
		return nil
	}
	key := pending.record.Key
	return func() tea.Msg {
		return externalStatusSavedMsg{err: historyStore.UpdateExternalStatus(key, status, note)}
	}
}
