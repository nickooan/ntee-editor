package app

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/staging"
	"github.com/nickooan/ntee-editor/internal/store"
)

// stagedReviewSource is an agent edit session's file as the review page
// shows it: its content before the agent's edits, the staged content, and
// the note it was shown with (a live refresh reloads the file only when one
// of them changed).
type stagedReviewSource struct {
	base       string
	baseExists bool
	content    string
	note       string
}

// agentEditReviewPreparedMsg delivers an agent edit session's review files,
// each with its note (its edit log, and a conflict warning when the file
// changed on disk since it was staged) in its staged source.
type agentEditReviewPreparedMsg struct {
	gen    int
	files  []reviewFile
	newest string // the file edited last: the live view follows from it
}

// agentEditReviewRefreshedMsg delivers the files of a live review again,
// after an agent staged more: newest is the file edited last.
type agentEditReviewRefreshedMsg struct {
	gen, refreshGen int
	files           []reviewFile
	newest          string
}

// maxAgentEditNoteEdits caps the edits a file's note lists (the newest).
const maxAgentEditNoteEdits = 12

// enterAgentEditReview opens the review page over an agent edit session:
// each file diffed from before the agent's edits to its staged content. A
// pending session's review is live — it follows the agents' edits — and,
// once they finished, applies (a), discards (x) or asks them for changes
// (r); trace is a finished session's read-only look back. Checking the disk
// for conflicts runs off the UI goroutine.
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
		agentEditsLive:  !trace,
		follow:          !trace,
	}
	m.mode = modeReview
	gen, registry := m.review.gen, m.agentEdits.registry
	return m, func() tea.Msg {
		files, newest := agentEditReviewFiles(registry, record, trace)
		return agentEditReviewPreparedMsg{gen: gen, files: inTreeOrder(files), newest: newest}
	}
}

// agentEditReviewFiles builds a session's review files with their notes,
// and names the file edited last. It reads the disk (ApplyCheck): run it
// off the UI goroutine.
func agentEditReviewFiles(registry *staging.Registry, record store.AgentEditSession, trace bool) (files []reviewFile, newest string) {
	files = make([]reviewFile, len(record.Files))
	shared := len(record.WithAgents().Agents) > 1
	var newestAt time.Time
	for index, file := range record.Files {
		refusal, below := "", []string(nil)
		if !trace && file.Result == "" {
			var result staging.FileResult
			var ok bool
			if below, result, ok = registry.ApplyCheck(file); !ok {
				refusal = result.Note
			}
		}
		files[index] = reviewFile{
			workspaceRel: file.Rel,
			staged: &stagedReviewSource{
				base: file.BaseContent, baseExists: file.BaseExists, content: file.Content,
				note: agentEditFileNote(file, trace, refusal, below, shared),
			},
		}
		if count := len(file.Edits); count > 0 && !file.Edits[count-1].At.Before(newestAt) {
			newestAt, newest = file.Edits[count-1].At, file.Rel
		}
	}
	return files, newest
}

// handleAgentEditReviewPrepared lays out the files' notes, then lands like
// any prepared review.
func (m Model) handleAgentEditReviewPrepared(msg agentEditReviewPreparedMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.review.gen || m.mode != modeReview {
		return m, nil
	}
	files := make([]reviewFile, len(msg.files))
	for index, file := range msg.files {
		files[index] = m.addReviewNote(file, reviewRequest{workspaceRel: file.workspaceRel, note: file.staged.note})
	}
	m.review.newestPath = msg.newest
	return m.handleReviewPrepared(reviewPreparedMsg{gen: msg.gen, files: files})
}

// refreshAgentEditReview re-reads the live review's session after its agents
// changed it. Only the newest refresh lands (refreshGen), so a burst of
// edits costs one reload per file.
func (m Model) refreshAgentEditReview(id string) (Model, tea.Cmd) {
	if m.mode != modeReview || m.review.agentEdits != id || !m.review.agentEditsLive || m.review.preparing {
		return m, nil
	}
	record, ok := m.agentEdits.registry.Snapshot(id)
	if !ok || len(record.Files) == 0 {
		return m, nil
	}
	m.review.agentEditsRefreshGen++
	gen, refreshGen, registry := m.review.gen, m.review.agentEditsRefreshGen, m.agentEdits.registry
	return m, func() tea.Msg {
		files, newest := agentEditReviewFiles(registry, record, false)
		return agentEditReviewRefreshedMsg{gen: gen, refreshGen: refreshGen, files: files, newest: newest}
	}
}

// handleAgentEditReviewRefreshed lands a live refresh: new files join in
// tree order, changed ones reload, and the view follows the file edited
// last while it follows (see landReviewArrival).
func (m Model) handleAgentEditReviewRefreshed(msg agentEditReviewRefreshedMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.review.gen || msg.refreshGen != m.review.agentEditsRefreshGen || m.mode != modeReview || m.review.preparing {
		return m, nil
	}
	selectedPath := ""
	if file, ok := m.selectedReviewFile(); ok {
		selectedPath = file.workspaceRel
	}
	files := slices.Clone(m.review.files)
	var changed []string
	added := false
	for _, file := range msg.files {
		file = m.addReviewNote(file, reviewRequest{workspaceRel: file.workspaceRel, note: file.staged.note})
		index, listed := m.review.fileByPath[file.workspaceRel]
		if !listed {
			files, added = append(files, file), true
			changed = append(changed, file.workspaceRel)
			continue
		}
		if previous := files[index].staged; previous != nil && *previous == *file.staged {
			continue
		}
		file.loadGen = files[index].loadGen + 1
		files[index] = file
		changed = append(changed, file.workspaceRel)
		if file.workspaceRel == selectedPath {
			m.review.search = documentSearch{}
		}
	}
	if len(changed) == 0 {
		return m, nil
	}
	if added {
		files = inTreeOrder(files)
	}
	m.review.files = files
	m.review.fileByPath = make(map[string]int, len(files))
	for index, file := range files {
		m.review.fileByPath[file.workspaceRel] = index
	}
	if selectedPath != "" {
		m.review.selection.index = m.review.fileByPath[selectedPath]
	}
	if added {
		m = m.refreshReviewTree()
	}
	var loads []tea.Cmd
	for _, rel := range changed {
		if rel == msg.newest {
			continue
		}
		loads = append(loads, m.reviewLoadPathCmd(rel))
	}
	if slices.Contains(changed, msg.newest) {
		m.review.newestPath = msg.newest
		var land tea.Cmd
		m, land = m.landReviewArrival(msg.newest, selectedPath == "")
		loads = append(loads, land)
	}
	return m, tea.Batch(loads...)
}

// agentEditReviewLocked reports whether the reviewed session's agents are
// still at work — editing, or checking it — so the user cannot decide yet.
func (m Model) agentEditReviewLocked() bool {
	id := m.review.agentEdits
	return m.agentEdits.stillEditing[id] || m.agentEdits.checking[id]
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

// handleAgentEditReviewKey is the agent edit review's own keys: once the
// agents finished, A applies, X discards and R asks them for changes; while
// they work those are locked and Esc simply leaves. handled is false for
// every other key.
func (m Model) handleAgentEditReviewKey(key string) (Model, tea.Cmd, bool) {
	if m.review.agentEdits == "" || m.review.agentEditsTrace {
		return m, nil, false
	}
	id := m.review.agentEdits
	switch key {
	case "a", "A", "shift+a", "x", "X", "shift+x", "r", "R", "shift+r":
		if m.agentEditReviewLocked() {
			m.errText = "agents are still working on edit session " + id + " — decide once they finish (Ctrl+T › agent edits: x discards, f stops waiting)"
			return m, nil, true
		}
	}
	switch key {
	case "a", "A", "shift+a":
		return m.askApplyAgentEdits(id), nil, true
	case "x", "X", "shift+x":
		return m.askDiscardAgentEdits(id), nil, true
	case "r", "R", "shift+r":
		next, cmd := m.startAgentEditRequestChanges(id)
		return next, cmd, true
	case "esc":
		if m.agentEditReviewLocked() {
			m = m.exitReview()
			m.notice = "agents still working on edit session " + id + " — click ✎ in the header to watch again"
			return m, nil, true
		}
		return m.askLeaveAgentEditReview(id), nil, true
	}
	return m, nil, false
}

// agentEditReviewBadge is the agent edit review's mark in the status bar.
func (m Model) agentEditReviewBadge() string {
	if m.review.agentEditsTrace {
		return "● edit session " + m.review.agentEdits + " · trace"
	}
	return "● edit session " + m.review.agentEdits + " · staged, not written"
}
