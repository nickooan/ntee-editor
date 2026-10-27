package app

import (
	"cmp"
	"hash/fnv"
	"slices"
)

// reviewNote is one of the agent's notes on a review file, keyed by the line
// it sits on.
type reviewNote struct {
	line    int // 1-based; 0 = the file's first change
	overlay lineNoteOverlay
	// contentHash is the file content the note was first shown with (0 = not
	// shown yet); a later content makes it an earlier note.
	contentHash uint64
}

// addReviewNote adds a request's note to the file: a note on a line that
// already has one replaces it, any other is inserted in line order. The note
// becomes the active one. A request without a note leaves the notes alone.
func (m Model) addReviewNote(file reviewFile, request reviewRequest) reviewFile {
	if request.note == "" {
		return file
	}
	note := reviewNote{
		line:    request.line,
		overlay: newLineNote(request.workspaceRel, max(0, request.line-1), request.note, 0, m.cfg.Editor.TabWidth),
	}
	index, found := slices.BinarySearchFunc(file.notes, request.line, func(existing reviewNote, line int) int {
		return cmp.Compare(existing.line, line)
	})
	notes := slices.Clone(file.notes)
	if found {
		notes[index] = note
	} else {
		notes = slices.Insert(notes, index, note)
	}
	file.notes, file.activeNote = notes, index
	return file
}

func (file reviewFile) activeReviewNote() (reviewNote, bool) {
	if len(file.notes) == 0 {
		return reviewNote{}, false
	}
	return file.notes[min(file.activeNote, len(file.notes)-1)], true
}

// noteRow is the diff row a note sits on: its line's row, or the file's
// first change for a note without a line.
func (file reviewFile) noteRow(note reviewNote) int {
	if note.line > 0 && len(file.document.rows) > 0 {
		return diffRowForBufLine(file.document.rows, note.line-1)
	}
	return file.document.firstChangeRow()
}

// noteIsEarlier reports whether the note was written against an earlier
// content of the file: its line may no longer point at what it explains.
func (file reviewFile) noteIsEarlier(note reviewNote) bool {
	return note.contentHash != 0 && file.contentHash != 0 && note.contentHash != file.contentHash
}

// noteMarkerRows are the diff rows of the notes not on screen, marked in the
// gutter so the user sees there is more to step to.
func (file reviewFile) noteMarkerRows() map[int]bool {
	if len(file.notes) < 2 || len(file.document.rows) == 0 {
		return nil
	}
	rows := make(map[int]bool, len(file.notes)-1)
	active := min(file.activeNote, len(file.notes)-1)
	for index, note := range file.notes {
		if index != active {
			rows[file.noteRow(note)] = true
		}
	}
	return rows
}

// noteRequests are the file's notes as requests, to carry them onto the
// listed copy of a file that was resent while its repo was resolving.
func (file reviewFile) noteRequests() []reviewRequest {
	requests := make([]reviewRequest, len(file.notes))
	for index, note := range file.notes {
		requests[index] = reviewRequest{workspaceRel: file.workspaceRel, line: note.line, note: note.overlay.text}
	}
	return requests
}

// stampReviewNotes gives the notes not shown yet the content they are first
// shown with. It returns a new slice when it changes one: Model copies share
// the old one.
func stampReviewNotes(notes []reviewNote, contentHash uint64) []reviewNote {
	if contentHash == 0 {
		return notes
	}
	var stamped []reviewNote
	for index, note := range notes {
		if note.contentHash != 0 {
			continue
		}
		if stamped == nil {
			stamped = slices.Clone(notes)
		}
		stamped[index].contentHash = contentHash
	}
	if stamped == nil {
		return notes
	}
	return stamped
}

func reviewContentHash(data []byte) uint64 {
	hash := fnv.New64a()
	_, _ = hash.Write(data)
	return max(hash.Sum64(), 1) // 0 means "no content"
}

// stepReviewNote makes the next (delta 1) or previous (-1) note of the
// selected file active, wrapping around, and puts the cursor on its line.
func (m Model) stepReviewNote(delta int) Model {
	file, ok := m.selectedReviewFile()
	if !ok || len(file.notes) == 0 {
		return m
	}
	count := len(file.notes)
	file.activeNote = (min(file.activeNote, count-1) + delta%count + count) % count
	files := slices.Clone(m.review.files)
	files[min(m.review.selection.index, len(files)-1)] = file
	m.review.files = files
	if file.loaded() && len(file.document.rows) > 0 {
		note, _ := file.activeReviewNote()
		m.review.position = rowCursor{}.anchoredAt(file.noteRow(note), m.reviewSpanFor(file))
	}
	return m
}

// reviewFileCount is how many distinct files the requests name: a file with
// several notes is one request per note.
func reviewFileCount(requests []reviewRequest) int {
	seen := make(map[string]bool, len(requests))
	for _, request := range requests {
		seen[request.workspaceRel] = true
	}
	return len(seen)
}
