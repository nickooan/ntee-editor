package app

import (
	"time"

	"github.com/nickooan/ntee-editor/internal/input"
	"github.com/nickooan/ntee-editor/internal/store"
)

// Drafts persist a file's unsaved edits (content + recent undo steps) so
// switching tabs/files — or quitting — never silently loses work. A draft is
// deleted by Ctrl+S (saved) or Esc (deliberate discard).

// draftMaxSteps caps how many undo steps a draft carries.
const draftMaxSteps = 15

// stashDraftIfDirty persists the current buffer as a draft when it has unsaved
// edits. Called before switching away (openFileAt) and on quit.
func (m Model) stashDraftIfDirty() Model {
	if m.openFile == nil || !m.edit.dirty {
		return m
	}
	m = m.flushBurst() // the timeline head must equal the live buffer

	// Collect the undo steps up to the cursor, skipping index 0: that baseline
	// is the on-disk content, re-seeded by beginEditSession on restore.
	var steps []store.DraftStep
	if m.undoCursor >= 1 && m.undoCursor < len(m.undoSeqs) {
		for _, seq := range m.undoSeqs[1 : m.undoCursor+1] {
			if snap, ok := m.db.SnapshotGet(seq); ok {
				steps = append(steps, store.DraftStep{Kind: snap.Kind, Content: snap.Content})
			}
		}
	}
	if len(steps) > draftMaxSteps {
		steps = steps[len(steps)-draftMaxSteps:]
	}
	content := m.edit.content()
	if len(steps) == 0 || steps[len(steps)-1].Content != content {
		steps = append(steps, store.DraftStep{Kind: "edit", Content: content})
	}

	_ = m.db.SaveDraft(store.Draft{
		Path:    m.openRel,
		Content: content,
		Cx:      m.edit.cx,
		Cy:      m.edit.cy,
		ScrollY: m.fileScrollY,
		Steps:   steps,
		At:      time.Now().UnixMilli(),
	})
	m.draftSet[m.openRel] = true
	return m
}

// restoreDraft rebuilds the unsaved state from a stashed draft. It runs right
// after beginEditSession, so the timeline is [disk baseline]; the draft's steps
// stack on top — undo walks back down to the on-disk content.
func (m Model) restoreDraft(d store.Draft) Model {
	head := ""
	if cur, ok := m.db.SnapshotGet(m.undoSeqs[len(m.undoSeqs)-1]); ok {
		head = cur.Content
	}
	for _, step := range d.Steps {
		if step.Content == head {
			continue // draft step equals the baseline — a no-op undo stop
		}
		seq := nextSeqAfter(m.nextSeq)
		m.nextSeq = seq
		_ = m.db.SnapshotPut(m.openRel, seq, step.Kind, step.Content)
		m.undoSeqs = append(m.undoSeqs, seq)
		head = step.Content
	}
	m.undoCursor = len(m.undoSeqs) - 1

	prevRev := m.edit.rev
	m.edit = newEditor(d.Content)
	m.edit.cx, m.edit.cy = d.Cx, d.Cy
	m.edit.clampCursor()
	m.edit.rev = prevRev + 1 // force the highlight cache to rescan
	m.edit.dirty = d.Content != m.openFile.Content
	m.fileScrollY = input.Clamp(d.ScrollY, 0, max(0, len(m.edit.lines)-1))
	m.snapDirty = false

	if !m.edit.dirty {
		// Disk caught up with the draft (saved elsewhere) — self-heal.
		_ = m.db.DeleteDraft(m.openRel)
		delete(m.draftSet, m.openRel)
	} else {
		m.draftSet[m.openRel] = true
		m.notice = "restored unsaved draft"
	}
	if client, ok := m.lsp.ClientFor(m.openFile.Path); ok {
		client.DidChange(m.openFile.Path, d.Content, m.edit.rev)
	}
	return m.refreshFileHighlights()
}
