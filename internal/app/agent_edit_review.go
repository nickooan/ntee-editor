package app

import (
	"cmp"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/staging"
	"github.com/nickooan/ntee-editor/internal/store"
)

// stagedReviewSource is an agent edit session's file as the review page
// shows it: its content before the agent's edits and the staged content.
type stagedReviewSource struct {
	base       string
	baseExists bool
	content    string
}

// agentEditReviewPreparedMsg delivers an agent edit session's review files
// with each file's note (its edit log, and a conflict warning when the file
// changed on disk since it was staged).
type agentEditReviewPreparedMsg struct {
	gen   int
	files []reviewFile
	notes map[string]string
}

// maxAgentEditNoteEdits caps the edits a file's note lists (the newest).
const maxAgentEditNoteEdits = 12

// enterAgentEditReview opens the review page over an agent edit session:
// each file diffed from before the agent's edits to its staged content. A
// pending session's review applies (A) or discards (X) it; trace is a
// finished session's read-only look back. Checking the disk for conflicts
// runs off the UI goroutine.
func (m Model) enterAgentEditReview(record store.AgentEditSession, trace bool) (Model, tea.Cmd) {
	switch {
	case m.reviewSessionRunning():
		m.errText = "a review session (" + m.session.id + ") is running — quit it first"
		return m, nil
	case len(record.Files) == 0:
		m.errText = "edit session " + record.Session + " has no files"
		return m, nil
	}
	m = m.leaveToEditOrQuery()
	m.review = reviewState{
		gen:             m.review.gen + 1,
		preparing:       true,
		previousMode:    m.mode,
		agentEdits:      record.Session,
		agentEditsTrace: trace,
	}
	m.mode = modeReview
	gen, registry := m.review.gen, m.agentEdits.registry
	return m, func() tea.Msg {
		files := make([]reviewFile, len(record.Files))
		notes := make(map[string]string, len(record.Files))
		shared := len(record.WithAgents().Agents) > 1
		for index, file := range record.Files {
			files[index] = reviewFile{
				workspaceRel: file.Rel,
				staged:       &stagedReviewSource{base: file.BaseContent, baseExists: file.BaseExists, content: file.Content},
			}
			refusal, below := "", []string(nil)
			if !trace && file.Result == "" {
				var result staging.FileResult
				var ok bool
				if below, result, ok = registry.ApplyCheck(file); !ok {
					refusal = result.Note
				}
			}
			notes[file.Rel] = agentEditFileNote(file, trace, refusal, below, shared)
		}
		return agentEditReviewPreparedMsg{gen: gen, files: inTreeOrder(files), notes: notes}
	}
}

// handleAgentEditReviewPrepared lays out the files' notes, then lands like
// any prepared review.
func (m Model) handleAgentEditReviewPrepared(msg agentEditReviewPreparedMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.review.gen || m.mode != modeReview {
		return m, nil
	}
	files := make([]reviewFile, len(msg.files))
	for index, file := range msg.files {
		files[index] = m.addReviewNote(file, reviewRequest{workspaceRel: file.workspaceRel, note: msg.notes[file.workspaceRel]})
	}
	return m.handleReviewPrepared(reviewPreparedMsg{gen: msg.gen, files: files})
}

// loadStagedReviewFile diffs a staged file from its content before the
// agent's edits to its staged content. No disk, no git: safe anywhere.
func loadStagedReviewFile(file reviewFile, highlightLimit, tabWidth int) reviewFile {
	source := file.staged
	current := splitBufferLines(source.content)
	document := diffDocument{lines: current}
	if source.baseExists {
		document.rows, document.adds, document.dels = buildDiffRows(splitBufferLines(source.base), current)
	} else {
		document.newFile = true
		document.rows = make([]diffRow, len(current))
		for index := range current {
			document.rows[index] = diffRow{kind: diffAdd, bufLine: index}
		}
		document.adds = len(current)
	}
	file.contentHash = reviewContentHash([]byte(source.content))
	return finishReviewDocument(file, document, source.content, true, highlightLimit, tabWidth)
}

// agentEditFileNote is a staged file's note: what applying did (a trace,
// or a file already written with a stacked session), why it cannot be
// written, the pending sessions it builds on, and the edit log — naming
// each edit's agent when several share the session.
func agentEditFileNote(file store.AgentEditFile, trace bool, refusal string, below []string, shared bool) string {
	var lines []string
	if file.Result != "" {
		result := "**" + string(file.Result) + "**"
		if file.ResultNote != "" {
			result += " — " + file.ResultNote
		}
		lines = append(lines, result)
	}
	if refusal != "" {
		lines = append(lines, "**"+refusal+"** — applying skips this file")
	}
	if len(below) > 0 {
		lines = append(lines, "**builds on edit session "+strings.Join(below, ", ")+"'s staged edits**, not applied yet — the diff shows this session's changes; applying writes theirs to this file too")
	}
	edits := file.Edits
	if len(edits) > maxAgentEditNoteEdits {
		lines = append(lines, fmt.Sprintf("%d edits, the last %d:", len(edits), maxAgentEditNoteEdits))
		edits = edits[len(edits)-maxAgentEditNoteEdits:]
	} else {
		lines = append(lines, countText(len(edits), "edit")+":")
	}
	for _, edit := range edits {
		line := fmt.Sprintf("- %s %s · +%d −%d · %s", edit.Op, lineRangeText(edit.LineFrom, edit.LineTo), edit.Added, edit.Removed, edit.At.Local().Format("15:04:05"))
		if shared {
			line += " · " + cmp.Or(edit.Agent, "unnamed")
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// handleAgentEditReviewKey is the agent edit review's own keys: A applies
// and X discards a pending session. handled is false for every other key.
func (m Model) handleAgentEditReviewKey(key string) (Model, bool) {
	if m.review.agentEdits == "" || m.review.agentEditsTrace {
		return m, false
	}
	switch key {
	case "a", "A", "shift+a":
		return m.askApplyAgentEdits(m.review.agentEdits), true
	case "x", "X", "shift+x":
		return m.askDiscardAgentEdits(m.review.agentEdits), true
	case "esc":
		return m.askLeaveAgentEditReview(m.review.agentEdits), true
	}
	return m, false
}

// agentEditReviewBadge is the agent edit review's mark in the status bar.
func (m Model) agentEditReviewBadge() string {
	if m.review.agentEditsTrace {
		return "● edit session " + m.review.agentEdits + " · trace"
	}
	return "● edit session " + m.review.agentEdits + " · staged, not written"
}
