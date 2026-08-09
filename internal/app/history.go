package app

import (
	"time"

	"github.com/nickooan/ntee-editor/internal/store"
)

// The undo timeline is a list of snapshot seqs plus a cursor; snapshot content
// lives in the store (ntee-db), not in the model. Unlike the RPC-backed
// original, store calls are synchronous — the DB is in-process, so undo/redo
// load content directly with no message round-trip.

// snapMetaEntry is what pushSnapshot remembers about a written snapshot so
// the burst-boundary dedupe can compare hashes without re-reading the store.
type snapMetaEntry struct {
	hash string
	kind string
}

// beginEditSession resets the undo timeline for a freshly-opened editor and
// records the baseline snapshot (the on-disk content).
func (m Model) beginEditSession(content string) Model {
	m.edit = newEditor(content)
	m.undoSeqs = nil
	m.undoCursor = 0
	m.snapDirty = false
	m.snapMeta = map[int64]snapMetaEntry{}
	m = m.refreshFileHighlights()
	return m.pushSnapshot("edit")
}

// pushSnapshot checkpoints the current editor content: it drops any redo branch
// (deleting those orphaned snapshots), appends a new snapshot seq, and persists
// it. kind is "edit" for coalesced bursts or "save" for saves. This runs on
// every burst boundary (word, newline, cursor line change), so the hot path is
// hash-only: one rev-memoized join+hash, no store read.
func (m Model) pushSnapshot(kind string) Model {
	if m.openFile == nil {
		return m
	}
	if m.snapMeta == nil {
		m.snapMeta = map[int64]snapMetaEntry{}
	}
	content, hash := m.edit.contentHashed()

	// Dedupe: when the buffer already matches the current snapshot (e.g. a
	// save right after a flushed burst), don't add an undo step the user would
	// experience as a no-op — just upgrade the snapshot's kind to "save" so
	// :revert / LastSave see it.
	if m.undoCursor >= 0 && m.undoCursor < len(m.undoSeqs) {
		seq := m.undoSeqs[m.undoCursor]
		if meta, ok := m.snapMeta[seq]; ok {
			if meta.hash == hash {
				if kind == "save" && meta.kind != "save" {
					_ = m.db.SnapshotPut(m.openRel, seq, "save", content)
					m.snapMeta[seq] = snapMetaEntry{hash: hash, kind: "save"}
				}
				m.snapDirty = false
				return m
			}
		} else if cur, ok := m.db.SnapshotGet(seq); ok && cur.Content == content {
			// No meta remembered for this seq (restored timeline) — the old
			// read-and-compare fallback, now also seeding the meta.
			metaKind := cur.Kind
			if kind == "save" && cur.Kind != "save" {
				_ = m.db.SnapshotPut(m.openRel, cur.Seq, "save", cur.Content)
				metaKind = "save"
			}
			m.snapMeta[seq] = snapMetaEntry{hash: hash, kind: metaKind}
			m.snapDirty = false
			return m
		}
	}

	// A new edit after an undo discards the now-orphaned forward snapshots.
	if m.undoCursor < len(m.undoSeqs)-1 {
		dropped := append([]int64(nil), m.undoSeqs[m.undoCursor+1:]...)
		m.undoSeqs = m.undoSeqs[:m.undoCursor+1]
		m.db.SnapshotDelete(dropped)
		for _, s := range dropped {
			delete(m.snapMeta, s)
		}
	}

	seq := nextSeqAfter(m.nextSeq)
	m.nextSeq = seq

	m.undoSeqs = append(m.undoSeqs, seq)
	if maxSnaps := m.cfg.Editor.MaxSnapshots; len(m.undoSeqs) > maxSnaps {
		// Delete the trimmed records too — previously they leaked until the
		// DB's per-file eviction caught up.
		dropped := append([]int64(nil), m.undoSeqs[:len(m.undoSeqs)-maxSnaps]...)
		m.undoSeqs = append([]int64(nil), m.undoSeqs[len(m.undoSeqs)-maxSnaps:]...)
		m.db.SnapshotDelete(dropped)
		for _, s := range dropped {
			delete(m.snapMeta, s)
		}
	}
	m.undoCursor = len(m.undoSeqs) - 1
	m.snapDirty = false

	m.snapMeta[seq] = snapMetaEntry{hash: hash, kind: kind}
	_ = m.db.SnapshotPut(m.openRel, seq, kind, content) // best-effort
	if client, ok := m.lsp.ClientFor(m.openFile.Path); ok {
		client.DidChange(m.openFile.Path, content, m.edit.rev)
	}
	return m
}

// nextSeqAfter returns a fresh snapshot seq: the current time, bumped past the
// last issued seq so seqs stay strictly increasing (= save order).
func nextSeqAfter(last int64) int64 {
	seq := time.Now().UnixMilli()
	if seq <= last {
		seq = last + 1
	}
	return seq
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
	if m.snapMeta != nil {
		hash := snap.Hash
		if hash == "" {
			hash = store.ContentHash(snap.Content) // pre-Hash record
		}
		m.snapMeta[seq] = snapMetaEntry{hash: hash, kind: snap.Kind}
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
