package staging

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/nickooan/ntee-editor/internal/filetree"
	"github.com/nickooan/ntee-editor/internal/store"
)

// layer is one pending session's unsettled staged copy of a file.
type layer struct {
	session string
	file    store.AgentEditFile
}

// Latest is rel's newest content: the top staged layer of any pending
// session, or the disk. session names the layer ("" = the disk); exists is
// false for a file neither staged nor on disk. A disk file is returned
// whatever its size — the caller caps what it sends.
func (r *Registry) Latest(rel string) (content, session string, exists bool, err error) {
	disk, diskExists, err := r.readDisk(rel)
	if err != nil {
		return "", "", false, err
	}
	if top, ok := topLayer(r.Pending(), rel, disk, diskExists); ok {
		return top.file.Content, top.session, true, nil
	}
	return disk, "", diskExists, nil
}

// StagedIn is the pending session holding rel's top layer, "" when rel has
// no staged edits.
func (r *Registry) StagedIn(rel string) (string, error) {
	if !hasUnsettledLayer(r.Pending(), rel) {
		return "", nil // the common answer, without reading the file
	}
	_, session, _, err := r.Latest(rel)
	return session, err
}

// StagedTop is the session on top of a file's staged layers. Stale marks a
// stack that can no longer be written (Reason says why): the disk changed
// under its bottom layer, or that layer was built on a session the user
// discarded. Latest then serves the disk.
type StagedTop struct {
	Session string
	Stale   bool
	Reason  string
}

// UnsettledTops maps each file with unsettled staged layers to its newest
// layer no other session builds on. It reads no disk, so a stale layer still
// counts: StagedTops tells the two apart.
func UnsettledTops(sessions []store.AgentEditSession) map[string]string {
	builtOn := map[string]bool{}
	for _, session := range sessions {
		for _, file := range session.Files {
			if file.Result == "" && file.BaseSession != "" {
				builtOn[file.BaseSession+"\x00"+file.Rel] = true
			}
		}
	}
	tops := map[string]string{}
	for _, session := range sessions { // oldest first: a newer top wins
		for _, file := range session.Files {
			if file.Result == "" && !builtOn[session.Session+"\x00"+file.Rel] {
				tops[file.Rel] = session.Session
			}
		}
	}
	return tops
}

// StagedTops is UnsettledTops checked against the disk, the way Latest
// picks a file's top layer: a file whose stacks are all stale keeps its
// newest top, marked stale. A file it cannot read stays live. It reads every
// staged file: run it off the UI goroutine.
func (r *Registry) StagedTops() map[string]StagedTop {
	sessions := r.Pending()
	unsettled := UnsettledTops(sessions)
	tops := make(map[string]StagedTop, len(unsettled))
	for rel, newest := range unsettled {
		disk, diskExists, err := r.readDisk(rel)
		if err != nil {
			tops[rel] = StagedTop{Session: newest}
			continue
		}
		if top, ok := topLayer(sessions, rel, disk, diskExists); ok {
			tops[rel] = StagedTop{Session: top.session}
			continue
		}
		tops[rel] = StagedTop{Session: newest, Stale: true, Reason: r.staleReason(sessions, newest, rel)}
	}
	return tops
}

// staleReason says why the stack under session's copy of rel is stale, from
// its bottom layer: built on discarded edits, or the disk changed.
func (r *Registry) staleReason(sessions []store.AgentEditSession, session, rel string) string {
	bottom, _ := fileIn(sessions, session, rel)
	for range len(sessions) {
		lower, isPending := fileIn(sessions, bottom.BaseSession, rel)
		if bottom.BaseSession == "" || !isPending {
			break
		}
		bottom = lower
	}
	if bottom.BaseSession != "" {
		if finished, known := r.Finished(bottom.BaseSession); known && finished.Status == store.AgentEditDiscarded {
			return "built on edit session " + bottom.BaseSession + "'s edits, which were discarded"
		}
	}
	return "changed on disk since it was staged"
}

// hasUnsettledLayer reports whether any pending session stages rel and has
// not settled it.
func hasUnsettledLayer(sessions []store.AgentEditSession, rel string) bool {
	for _, session := range sessions {
		for _, file := range session.Files {
			if file.Rel == rel && file.Result == "" {
				return true
			}
		}
	}
	return false
}

func (r *Registry) readDisk(rel string) (content string, exists bool, err error) {
	data, binary, err := filetree.ReadContainedFile(r.workspaceRoot, rel)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if !filetree.IsContained(r.workspaceRoot, rel) {
			return "", false, fmt.Errorf("path outside the project: %s", rel)
		}
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("cannot read %s: %w", rel, err)
	case binary:
		return "", true, fmt.Errorf("%s is a binary file", rel)
	}
	return string(data), true, nil
}

// topLayer walks rel's layers up from the disk. The root is a layer whose
// base is the disk as it is now — based on no session, or on one no longer
// pending (applied: its content is the disk; discarded: it is not, so the
// layer is an orphan and skipped). Each next layer is based on the one
// below; the last is the top. sessions come oldest first, so an older root
// wins a tie.
func topLayer(sessions []store.AgentEditSession, rel, disk string, diskExists bool) (layer, bool) {
	pending := make(map[string]bool, len(sessions))
	var layers []layer
	for _, session := range sessions {
		pending[session.Session] = true
		for _, file := range session.Files {
			if file.Rel == rel && file.Result == "" {
				layers = append(layers, layer{session: session.Session, file: file})
			}
		}
	}
	current, found := layer{}, false
	for _, candidate := range layers {
		basedOnDisk := candidate.file.BaseSession == "" || !pending[candidate.file.BaseSession]
		if basedOnDisk && candidate.file.BaseExists == diskExists && candidate.file.BaseContent == disk {
			current, found = candidate, true
			break
		}
	}
	if !found {
		return layer{}, false
	}
	for range layers {
		next := -1
		for index, candidate := range layers {
			if candidate.file.BaseSession == current.session {
				next = index
				break
			}
		}
		if next < 0 {
			break
		}
		current = layers[next]
	}
	return current, true
}

// baseFor is the base of a file a session stages for the first time: the
// top layer when another session staged it, else the disk.
func (r *Registry) baseFor(rel string) (store.AgentEditFile, error) {
	file, err := r.readBase(rel)
	if err != nil {
		return file, err
	}
	if top, ok := topLayer(r.Pending(), rel, file.BaseContent, file.BaseExists); ok {
		file.BaseExists, file.BaseContent, file.Content, file.BaseSession = true, top.file.Content, top.file.Content, top.session
	}
	return file, nil
}

// ApplyCheck says whether a staged file can be written: every layer below it
// must still hold the content it was built on, and the bottom one's base
// must still be the disk. below lists the pending sessions whose copy of the
// file is written along with it.
func (r *Registry) ApplyCheck(file store.AgentEditFile) (below []string, refusal FileResult, ok bool) {
	sessions := r.Pending()
	current := file
	for range len(sessions) + 1 {
		if current.BaseSession == "" {
			break
		}
		lower, isPending := fileIn(sessions, current.BaseSession, file.Rel)
		if !isPending {
			break
		}
		switch {
		case lower.Result != "":
			return nil, FileResult{Result: store.AgentEditFileConflicted, Note: "edit session " + current.BaseSession + " already wrote it (" + lower.ResultNote + ")"}, false
		case lower.Content != current.BaseContent:
			return nil, FileResult{Result: store.AgentEditFileConflicted, Note: "edit session " + current.BaseSession + " changed it after this session built on it"}, false
		}
		below = append(below, current.BaseSession)
		current = lower
	}
	matches, err := DiskMatchesBase(r.workspaceRoot, current)
	switch {
	case err != nil:
		return nil, FileResult{Result: store.AgentEditFileSkipped, Note: "cannot read it: " + err.Error()}, false
	case matches:
		return below, FileResult{}, true
	}
	if current.BaseSession != "" {
		if finished, known := r.Finished(current.BaseSession); known && finished.Status == store.AgentEditDiscarded {
			return nil, FileResult{Result: store.AgentEditFileConflicted, Note: "built on edit session " + current.BaseSession + "'s edits, which were discarded"}, false
		}
	}
	return nil, FileResult{Result: store.AgentEditFileConflicted, Note: "changed on disk since it was staged"}, false
}

// PendingBase is the pending session a staged file was built on, "" when it
// was built on the disk or on a session that is over.
func (r *Registry) PendingBase(file store.AgentEditFile) string {
	if file.BaseSession == "" {
		return ""
	}
	if _, ok := r.Snapshot(file.BaseSession); ok {
		return file.BaseSession
	}
	return ""
}

func fileIn(sessions []store.AgentEditSession, session, rel string) (store.AgentEditFile, bool) {
	for _, candidate := range sessions {
		if candidate.Session != session {
			continue
		}
		for _, file := range candidate.Files {
			if file.Rel == rel {
				return file, true
			}
		}
	}
	return store.AgentEditFile{}, false
}

// SettleFile records a result on a still-pending session's file — its copy
// was written along with a session stacked on it — so the session's own
// apply leaves the file alone.
func (r *Registry) SettleFile(session, rel string, result FileResult) error {
	set, ok := r.pendingSet(session)
	if !ok {
		return r.noSession(session)
	}
	set.saveMu.Lock()
	defer set.saveMu.Unlock()
	record := set.snapshot()
	files := make([]store.AgentEditFile, len(record.Files))
	copy(files, record.Files)
	for index := range files {
		if files[index].Rel == rel {
			files[index].Result, files[index].ResultNote = result.Result, result.Note
		}
	}
	record.Files = files
	if err := r.log.SavePendingAgentEditFiles(record, []string{rel}); err != nil {
		return fmt.Errorf("cannot save edit session %s: %w", session, err)
	}
	set.publish(record)
	return nil
}

func (r *Registry) pendingSet(session string) (*stagedSet, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	set, ok := r.pending[session]
	return set, ok
}

// ApplyFiles writes each of the session's staged files, except those
// already settled, those in skip (whose result is given), and those that
// cannot be written (ApplyCheck). A file built on pending sessions settles
// their copies once written; so does a skip result that names them in
// Settles. It touches the disk: run it off the UI goroutine.
func (r *Registry) ApplyFiles(session string, files []store.AgentEditFile, skip map[string]FileResult) map[string]FileResult {
	results := make(map[string]FileResult, len(files))
	for _, file := range files {
		result, decided := skip[file.Rel]
		switch {
		case file.Result != "":
			result = FileResult{Result: file.Result, Note: file.ResultNote}
		case !decided:
			result = r.applyFile(file)
		}
		results[file.Rel] = result
		if result.Result != store.AgentEditFileApplied {
			continue
		}
		for _, lower := range result.Settles {
			if err := r.SettleFile(lower, file.Rel, FileResult{Result: store.AgentEditFileApplied, Note: "written with edit session " + session}); err != nil {
				result.Note = err.Error()
				results[file.Rel] = result
			}
		}
	}
	return results
}

func (r *Registry) applyFile(file store.AgentEditFile) FileResult {
	below, refusal, ok := r.ApplyCheck(file)
	if !ok {
		return refusal
	}
	result := WriteStaged(r.workspaceRoot, file)
	result.Settles = below
	return result
}

// WriteStaged writes a staged file's content, creating its folder when it
// is new on disk. The caller has checked it may (ApplyCheck).
func WriteStaged(workspaceRoot string, file store.AgentEditFile) FileResult {
	if !filetree.IsContained(workspaceRoot, file.Rel) {
		return FileResult{Result: store.AgentEditFileSkipped, Note: "path outside the project"}
	}
	if _, err := os.Stat(filepath.Join(workspaceRoot, filepath.FromSlash(file.Rel))); errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(workspaceRoot, filepath.FromSlash(file.Rel))), 0o755); err != nil {
			return FileResult{Result: store.AgentEditFileSkipped, Note: "cannot create its folder: " + err.Error()}
		}
	}
	if err := filetree.WriteViewFile(workspaceRoot, file.Rel, file.Content); err != nil {
		return FileResult{Result: store.AgentEditFileSkipped, Note: "write failed: " + err.Error()}
	}
	return FileResult{Result: store.AgentEditFileApplied}
}
