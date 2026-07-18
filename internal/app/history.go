package app

import (
	"time"
)

// The undo timeline is a list of snapshot seqs plus a cursor; snapshot content
// lives in the store (ntee-db), not in the model. Unlike the RPC-backed
// original, store calls are synchronous — the DB is in-process, so undo/redo
// load content directly with no message round-trip.

// beginEditSession resets the undo timeline for a freshly-opened editor and
// records the baseline snapshot (the on-disk content).
func (m Model) beginEditSession(content string) Model {
	m.edit = newEditor(content)
	m.undoSeqs = nil
	m.undoCursor = 0
	m.snapDirty = false
	m = m.refreshFileHighlights()
	return m.pushSnapshot("edit")
}

// pushSnapshot checkpoints the current editor content: it drops any redo branch
// (deleting those orphaned snapshots), appends a new snapshot seq, and persists
// it. kind is "edit" for coalesced bursts or "save" for saves.
func (m Model) pushSnapshot(kind string) Model {
	if m.openFile == nil {
		return m
	}

	// Dedupe: when the buffer already matches the current snapshot (e.g. a
	// save right after a flushed burst), don't add an undo step the user would
	// experience as a no-op — just upgrade the snapshot's kind to "save" so
	// :revert / LastSave see it.
	if m.undoCursor >= 0 && m.undoCursor < len(m.undoSeqs) {
		if cur, ok := m.db.SnapshotGet(m.undoSeqs[m.undoCursor]); ok && cur.Content == m.edit.content() {
			if kind == "save" && cur.Kind != "save" {
				_ = m.db.SnapshotPut(m.openRel, cur.Seq, "save", cur.Content)
			}
			m.snapDirty = false
			return m
		}
	}

	// A new edit after an undo discards the now-orphaned forward snapshots.
	if m.undoCursor < len(m.undoSeqs)-1 {
		dropped := append([]int64(nil), m.undoSeqs[m.undoCursor+1:]...)
		m.undoSeqs = m.undoSeqs[:m.undoCursor+1]
		m.db.SnapshotDelete(dropped)
	}

	seq := time.Now().UnixMilli()
	if seq <= m.nextSeq {
		seq = m.nextSeq + 1 // keep seqs strictly increasing (= save order)
	}
	m.nextSeq = seq

	content := m.edit.content()
	m.undoSeqs = append(m.undoSeqs, seq)
	if maxSnaps := m.cfg.Editor.MaxSnapshots; len(m.undoSeqs) > maxSnaps {
		m.undoSeqs = append([]int64(nil), m.undoSeqs[len(m.undoSeqs)-maxSnaps:]...)
	}
	m.undoCursor = len(m.undoSeqs) - 1
	m.snapDirty = false

	_ = m.db.SnapshotPut(m.openRel, seq, kind, content) // best-effort
	if client, ok := m.lsp.ClientFor(m.openFile.Path); ok {
		client.DidChange(m.openFile.Path, content, m.edit.rev)
	}
	return m
}

// flushBurst checkpoints the current content if edits are pending since the last
// snapshot; a no-op otherwise. Called at burst boundaries (cursor moves, space,
// newline) so one snapshot coalesces a typing burst.
func (m Model) flushBurst() Model {
	if m.snapDirty {
		return m.pushSnapshot("edit")
	}
	return m
}

// undo flushes any un-checkpointed edits (so redo can return to them), then
// steps back one snapshot and loads its content into the editor.
func (m Model) undo() Model {
	if m.snapDirty {
		m = m.pushSnapshot("edit")
	}
	if m.undoCursor > 0 {
		m.undoCursor--
		m = m.loadSnapshot(m.undoSeqs[m.undoCursor])
	}
	return m
}

// redo steps forward one snapshot, if the timeline has one.
func (m Model) redo() Model {
	if m.undoCursor < len(m.undoSeqs)-1 {
		m.undoCursor++
		m = m.loadSnapshot(m.undoSeqs[m.undoCursor])
	}
	return m
}

// loadSnapshot replaces the editor buffer with a snapshot's content, keeping
// the cursor position (clamped to the restored buffer).
func (m Model) loadSnapshot(seq int64) Model {
	snap, ok := m.db.SnapshotGet(seq)
	if !ok {
		m.errText = "snapshot unavailable"
		return m
	}
	cx, cy := m.edit.cx, m.edit.cy
	rev := m.edit.rev
	m.edit = newEditor(snap.Content)
	m.edit.cx, m.edit.cy = cx, cy
	m.edit.clampCursor()
	m.edit.rev = rev + 1 // force the highlight cache to rescan
	if m.openFile != nil {
		m.edit.dirty = snap.Content != m.openFile.Content
	}
	m.snapDirty = false
	return m.refreshFileHighlights()
}
