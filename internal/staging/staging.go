// Package staging keeps agent edits away from the files until they are
// applied. An agent sends whole-file writes and exact-text replacements for
// an edit session; they stack on the session's staged copy of each file, and
// the session's one record (store.AgentEditSession) is saved after every
// edit. Applying writes the staged contents to the files, skipping any file
// that changed on disk since it was staged.
package staging

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nickooan/ntee-editor/internal/diff"
	"github.com/nickooan/ntee-editor/internal/filetree"
	"github.com/nickooan/ntee-editor/internal/store"
)

// MaxFileBytes caps a staged file, before and after: the same limit the
// review page shows.
const MaxFileBytes = 2 << 20

// Edit operations, as recorded in store.AgentEdit.Op.
const (
	OpWrite   = "write"
	OpReplace = "replace"
)

// Replacement is one replace edit: Old must match the staged content exactly
// once, unless All replaces every match. HintLine (1-based, 0 = none) picks
// among several matches the one covering that line, or else the nearest.
type Replacement struct {
	Old, New string
	All      bool
	HintLine int
}

// EditResult is what an edit did, for the agent's reply.
type EditResult struct {
	Rel          string
	LineFrom     int // 1-based lines of the change in the staged content after it; 0 = no change
	LineTo       int
	Added        int
	Removed      int
	Replaced     int      // replace: how many matches were replaced
	NewFile      bool     // the file does not exist on disk
	EditCount    int      // edits staged for this file so far
	StagedFiles  int      // files staged in the session
	SessionStart bool     // this edit started the session
	BaseSession  string   // the pending session whose staged copy the file was built on; "" = the disk
	Joined       bool     // this edit added its agent to a session other agents started
	OtherAgents  []string // the session's other agents still editing
}

// FileResult is what applying did with one file.
type FileResult struct {
	Result store.AgentEditFileResult
	Note   string
	// Settles lists the pending sessions whose copy of the file a write
	// also wrote (see ApplyCheck); ApplyFiles settles them.
	Settles []string
}

// Registry holds the edit sessions of one workspace. Edits arrive on the
// socket's goroutines and the UI reads snapshots, so a session's record is
// copy-on-write: a published record is never mutated, only replaced.
type Registry struct {
	workspaceRoot string
	log           store.AgentEditLogStore
	now           func() time.Time

	// layerMu serializes a file's first staging in a session — reading the
	// top layer through publishing the record — so two sessions never both
	// build on the same layer. Taken after a set's saveMu, never before.
	layerMu sync.Mutex

	mu       sync.Mutex
	pending  map[string]*stagedSet
	finished map[string]store.AgentEditSession // finished this run, file contents dropped
}

// stagedSet is one pending session. saveMu serializes its edit-and-save
// sequences so saves land in edit order; mu guards only the published
// record, so readers never wait on a db write.
type stagedSet struct {
	saveMu sync.Mutex
	mu     sync.Mutex
	record store.AgentEditSession
}

func NewRegistry(workspaceRoot string, log store.AgentEditLogStore) *Registry {
	return &Registry{
		workspaceRoot: workspaceRoot,
		log:           log,
		now:           time.Now,
		pending:       map[string]*stagedSet{},
		finished:      map[string]store.AgentEditSession{},
	}
}

// WithClock replaces the registry's clock (tests).
func (r *Registry) WithClock(now func() time.Time) *Registry {
	r.now = now
	return r
}

// Log is the store the registry saves to (the UI lists finished sessions
// from it).
func (r *Registry) Log() store.AgentEditLogStore { return r.log }

// Load picks up the sessions still pending from an earlier run.
func (r *Registry) Load() error {
	sessions, err := r.log.LoadPendingAgentEdits()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, session := range sessions {
		r.pending[session.Session] = &stagedSet{record: session}
	}
	return err
}

func (set *stagedSet) snapshot() store.AgentEditSession {
	set.mu.Lock()
	defer set.mu.Unlock()
	return set.record
}

func (set *stagedSet) publish(record store.AgentEditSession) {
	set.mu.Lock()
	set.record = record
	set.mu.Unlock()
}

// Write stages content as the whole new content of rel.
func (r *Registry) Write(session, agent, rel, content string) (EditResult, error) {
	if len(content) > MaxFileBytes {
		return EditResult{}, fmt.Errorf("content is larger than %d MB", MaxFileBytes>>20)
	}
	return r.edit(session, agent, rel, func(current string) (string, store.AgentEdit, EditResult, error) {
		edit := store.AgentEdit{Op: OpWrite}
		edit.LineFrom, edit.LineTo, edit.Added, edit.Removed = LineChanges(current, content)
		return content, edit, EditResult{}, nil
	})
}

// Replace stages an exact-text replacement in rel's staged content (its disk
// content when rel was not staged yet).
func (r *Registry) Replace(session, agent, rel string, replacement Replacement) (EditResult, error) {
	switch {
	case replacement.Old == "":
		return EditResult{}, errors.New("the old text is empty — use write to set the whole file")
	case replacement.Old == replacement.New:
		return EditResult{}, errors.New("the old and new text are the same")
	case replacement.All && replacement.HintLine > 0:
		return EditResult{}, errors.New("a line hint picks one match — leave it off with replace-all")
	}
	return r.edit(session, agent, rel, func(current string) (string, store.AgentEdit, EditResult, error) {
		next, edit, replaced, err := replaceText(current, rel, replacement)
		if err != nil {
			return "", edit, EditResult{}, err
		}
		if len(next) > MaxFileBytes {
			return "", edit, EditResult{}, fmt.Errorf("the result is larger than %d MB", MaxFileBytes>>20)
		}
		return next, edit, EditResult{Replaced: replaced}, nil
	})
}

// edit runs one edit by agent on rel's staged content, saves the session,
// and only then publishes it — a failed save leaves the session as it was.
// An agent's first edit adds it to the session's agents; an agent that
// already ended takes its part up again.
func (r *Registry) edit(session, agent, rel string, apply func(current string) (string, store.AgentEdit, EditResult, error)) (EditResult, error) {
	set, created, err := r.lockedSetFor(session)
	if err != nil {
		return EditResult{}, err
	}
	defer set.saveMu.Unlock()
	record := set.snapshot()
	if !record.AgentEndedAt.IsZero() {
		if stoppedAgents(record) {
			return EditResult{}, fmt.Errorf("the user stopped waiting for agents in edit session %s — start a new session for more edits", session)
		}
		return EditResult{}, fmt.Errorf("edit session %s already got end from every agent — start a new session for more edits", session)
	}
	agents := slices.Clone(record.Agents)
	at := slices.IndexFunc(agents, func(participant store.AgentEditParticipant) bool { return participant.Agent == agent })
	joined := false
	switch {
	case at < 0:
		joined = len(agents) > 0
		agents = append(agents, store.AgentEditParticipant{Agent: agent, JoinedAt: r.now()})
	case !agents[at].Editing():
		agents[at].EndedAt = time.Time{}
	}
	files := slices.Clone(record.Files)
	index := slices.IndexFunc(files, func(file store.AgentEditFile) bool { return file.Rel == rel })
	if index < 0 {
		r.layerMu.Lock()
		defer r.layerMu.Unlock()
		file, err := r.baseFor(rel)
		if err != nil {
			r.dropIfEmpty(session, set)
			return EditResult{}, err
		}
		files = append(files, file)
		index = len(files) - 1
	}
	file := files[index]
	if file.Result != "" {
		return EditResult{}, fmt.Errorf("%s was already written (%s) — start a new edit session to change it again", rel, file.ResultNote)
	}
	next, edit, result, err := apply(file.Content)
	if err != nil {
		r.dropIfEmpty(session, set)
		return EditResult{}, err
	}
	edit.At = r.now()
	edit.Agent = agent
	file.Content = next
	file.Edits = append(slices.Clip(file.Edits), edit)
	files[index] = file
	record.Files = files
	record.Agents = agents
	if err := r.log.SavePendingAgentEdit(record); err != nil {
		r.dropIfEmpty(session, set)
		return EditResult{}, fmt.Errorf("cannot save the staged edit: %w", err)
	}
	set.publish(record)
	result.Rel = rel
	result.LineFrom, result.LineTo = edit.LineFrom, edit.LineTo
	result.Added, result.Removed = edit.Added, edit.Removed
	result.NewFile = !file.BaseExists
	result.EditCount = len(file.Edits)
	result.StagedFiles = len(files)
	result.SessionStart = created
	result.BaseSession = file.BaseSession
	result.Joined = joined
	for _, other := range record.EditingAgents() {
		if other != agent {
			result.OtherAgents = append(result.OtherAgents, other)
		}
	}
	return result, nil
}

// setFor finds the session's pending set, starting it on its first edit. A
// session finished this run is not reopened: its id must not mix two
// batches of edits.
func (r *Registry) setFor(session string) (*stagedSet, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if set, ok := r.pending[session]; ok {
		return set, false, nil
	}
	if finished, ok := r.finished[session]; ok {
		return nil, false, fmt.Errorf("edit session %s already finished (%s) — start a new session id", session, finished.Status)
	}
	set := &stagedSet{record: store.AgentEditSession{Session: session, Status: store.AgentEditPending, StartedAt: r.now(), Files: []store.AgentEditFile{}}}
	r.pending[session] = set
	return set, true, nil
}

// lockedSetFor is setFor with the set's saveMu held. A set can stop being
// the session's while an edit waits for that lock — its first edit failed
// and dropped it, or the session finished — so the edit re-checks after
// locking and goes again: to a fresh set, or to the "finished" refusal.
func (r *Registry) lockedSetFor(session string) (*stagedSet, bool, error) {
	for {
		set, created, err := r.setFor(session)
		if err != nil {
			return nil, false, err
		}
		set.saveMu.Lock()
		r.mu.Lock()
		current := r.pending[session] == set
		r.mu.Unlock()
		if current {
			return set, created, nil
		}
		set.saveMu.Unlock()
	}
}

// dropIfEmpty forgets a session whose first edit failed, so a refused edit
// leaves no empty session behind.
func (r *Registry) dropIfEmpty(session string, set *stagedSet) {
	if len(set.snapshot().Files) > 0 {
		return
	}
	r.mu.Lock()
	if r.pending[session] == set {
		delete(r.pending, session)
	}
	r.mu.Unlock()
}

// readBase reads rel's disk content as the base of a newly staged file; a
// missing file is a new file.
func (r *Registry) readBase(rel string) (store.AgentEditFile, error) {
	file := store.AgentEditFile{Rel: rel, Edits: []store.AgentEdit{}}
	data, binary, err := filetree.ReadContainedFile(r.workspaceRoot, rel)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if !filetree.IsContained(r.workspaceRoot, rel) {
			return file, fmt.Errorf("path outside the project: %s", rel)
		}
		return file, nil
	case err != nil:
		return file, fmt.Errorf("cannot read %s: %w", rel, err)
	case binary:
		return file, fmt.Errorf("%s is a binary file — edits take text files only", rel)
	case len(data) > MaxFileBytes:
		return file, fmt.Errorf("%s is larger than %d MB", rel, MaxFileBytes>>20)
	}
	file.BaseExists, file.BaseContent, file.Content = true, string(data), string(data)
	return file, nil
}

// Content is rel's staged content in the session, or — when the session
// has not staged it (or there is no such session) — its latest content
// (Latest). session names where it came from ("" = the disk); exists is
// false for a file neither staged nor on disk.
func (r *Registry) Content(session, rel string) (content, from string, exists bool, err error) {
	if record, ok := r.Snapshot(session); ok {
		for _, file := range record.Files {
			if file.Rel == rel {
				return file.Content, session, true, nil
			}
		}
	}
	return r.Latest(rel)
}

// Snapshot is the pending session's record.
func (r *Registry) Snapshot(session string) (store.AgentEditSession, bool) {
	r.mu.Lock()
	set, ok := r.pending[session]
	r.mu.Unlock()
	if !ok {
		return store.AgentEditSession{}, false
	}
	return set.snapshot(), true
}

// Pending lists the pending sessions, oldest first.
func (r *Registry) Pending() []store.AgentEditSession {
	r.mu.Lock()
	sets := make([]*stagedSet, 0, len(r.pending))
	for _, set := range r.pending {
		sets = append(sets, set)
	}
	r.mu.Unlock()
	sessions := make([]store.AgentEditSession, len(sets))
	for index, set := range sets {
		sessions[index] = set.snapshot()
	}
	sort.Slice(sessions, func(a, b int) bool {
		if !sessions[a].StartedAt.Equal(sessions[b].StartedAt) {
			return sessions[a].StartedAt.Before(sessions[b].StartedAt)
		}
		return sessions[a].Session < sessions[b].Session
	})
	return sessions
}

// Finished is a session finished during this run, without file contents.
func (r *Registry) Finished(session string) (store.AgentEditSession, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.finished[session]
	return record, ok
}

// MarkAgentEnded records one agent's end: its part is done. Once no agent
// in the session is still editing, the session is ready for the user
// (allEnded, and AgentEndedAt is set).
func (r *Registry) MarkAgentEnded(session, agent string) (record store.AgentEditSession, allEnded bool, err error) {
	set, ok := r.pendingSet(session)
	if !ok {
		return store.AgentEditSession{}, false, r.noSession(session)
	}
	set.saveMu.Lock()
	defer set.saveMu.Unlock()
	record = set.snapshot().WithAgents()
	if !record.AgentEndedAt.IsZero() {
		return record, true, nil
	}
	agents := slices.Clone(record.Agents)
	at := slices.IndexFunc(agents, func(participant store.AgentEditParticipant) bool { return participant.Agent == agent })
	if at < 0 {
		return record, false, fmt.Errorf("%s has no edits in edit session %s — nothing of yours to end (agents in it: %s)", AgentLabel(agent), session, agentList(record.Agents))
	}
	now := r.now()
	if agents[at].Editing() {
		agents[at].EndedAt = now
	}
	record.Agents = agents
	allEnded = len(record.EditingAgents()) == 0
	if allEnded {
		record.AgentEndedAt = now
	}
	if err := r.log.SavePendingAgentEdit(record); err != nil {
		return store.AgentEditSession{}, false, fmt.Errorf("cannot save the session: %w", err)
	}
	set.publish(record)
	return record, allEnded, nil
}

// ForceEnd is the user no longer waiting for a session's agents: those
// still editing are marked stopped (their further edits are refused) and
// the session is ready to review. stopped lists them.
func (r *Registry) ForceEnd(session string) (record store.AgentEditSession, stopped []string, err error) {
	set, ok := r.pendingSet(session)
	if !ok {
		return store.AgentEditSession{}, nil, r.noSession(session)
	}
	set.saveMu.Lock()
	defer set.saveMu.Unlock()
	record = set.snapshot().WithAgents()
	if !record.AgentEndedAt.IsZero() {
		return record, nil, nil
	}
	now := r.now()
	agents := slices.Clone(record.Agents)
	for index := range agents {
		if agents[index].Editing() {
			agents[index].EndedAt, agents[index].Stopped = now, true
			stopped = append(stopped, agents[index].Agent)
		}
	}
	record.Agents, record.AgentEndedAt = agents, now
	if err := r.log.SavePendingAgentEdit(record); err != nil {
		return store.AgentEditSession{}, nil, fmt.Errorf("cannot save the session: %w", err)
	}
	set.publish(record)
	return record, stopped, nil
}

func stoppedAgents(record store.AgentEditSession) bool {
	return slices.ContainsFunc(record.Agents, func(participant store.AgentEditParticipant) bool { return participant.Stopped })
}

// AgentLabel names an agent in a sentence: its id, or "an unnamed agent".
func AgentLabel(agent string) string {
	if agent == "" {
		return "an unnamed agent"
	}
	return "agent " + agent
}

// AgentNames lists agent ids for a sentence, "unnamed" for "".
func AgentNames(agents []string) string {
	names := make([]string, len(agents))
	for index, agent := range agents {
		names[index] = cmp.Or(agent, "unnamed")
	}
	return strings.Join(names, ", ")
}

func agentList(participants []store.AgentEditParticipant) string {
	agents := make([]string, len(participants))
	for index, participant := range participants {
		agents[index] = participant.Agent
	}
	return AgentNames(agents)
}

func (r *Registry) noSession(session string) error {
	if finished, ok := r.Finished(session); ok {
		return fmt.Errorf("edit session %s already finished (%s)", session, finished.Status)
	}
	return fmt.Errorf("no edit session %s — nothing was staged", session)
}

// Finish records how applying went — each file's result, the session's
// status and outcome — and moves the session to the finished records,
// keeping the newest keep of them.
func (r *Registry) Finish(session, policy string, results map[string]FileResult, keep int) (store.AgentEditSession, error) {
	return r.finish(session, keep, func(record store.AgentEditSession) store.AgentEditSession {
		files := slices.Clone(record.Files)
		applied := 0
		var notApplied []string
		for index, file := range files {
			result, ok := results[file.Rel]
			if file.Result != "" {
				result, ok = FileResult{Result: file.Result, Note: file.ResultNote}, true
			}
			if !ok {
				result = FileResult{Result: store.AgentEditFileSkipped, Note: "not applied"}
			}
			files[index].Result, files[index].ResultNote = result.Result, result.Note
			if result.Result == store.AgentEditFileApplied {
				applied++
				continue
			}
			notApplied = append(notApplied, file.Rel+" ("+result.Note+")")
		}
		record.Files = files
		record.Policy = policy
		switch {
		case applied == len(files):
			record.Status = store.AgentEditApplied
			record.Outcome = "applied " + countFiles(applied)
		case applied == 0:
			record.Status = store.AgentEditNotApplied
			record.Outcome = "applied no file; not applied: " + strings.Join(notApplied, ", ")
		default:
			record.Status = store.AgentEditPartlyApplied
			record.Outcome = "applied " + strconv.Itoa(applied) + " of " + countFiles(len(files)) + "; not applied: " + strings.Join(notApplied, ", ")
		}
		return record
	})
}

// Discard drops the session's staged edits; the record stays, marked
// discarded with who did it.
func (r *Registry) Discard(session, by string, keep int) (store.AgentEditSession, error) {
	return r.finish(session, keep, func(record store.AgentEditSession) store.AgentEditSession {
		record.Status = store.AgentEditDiscarded
		record.Outcome = "discarded by " + by
		return record
	})
}

func (r *Registry) finish(session string, keep int, update func(store.AgentEditSession) store.AgentEditSession) (store.AgentEditSession, error) {
	r.mu.Lock()
	set, ok := r.pending[session]
	r.mu.Unlock()
	if !ok {
		return store.AgentEditSession{}, r.noSession(session)
	}
	set.saveMu.Lock()
	defer set.saveMu.Unlock()
	record := update(set.snapshot())
	record.EndedAt = r.now()
	if err := r.log.FinishAgentEdit(record, keep); err != nil {
		return store.AgentEditSession{}, fmt.Errorf("cannot save the finished session: %w", err)
	}
	summary := record
	summary.Files = make([]store.AgentEditFile, len(record.Files))
	for index, file := range record.Files {
		summary.Files[index] = store.AgentEditFile{Rel: file.Rel, BaseExists: file.BaseExists, Result: file.Result, ResultNote: file.ResultNote}
	}
	r.mu.Lock()
	if r.pending[session] == set {
		delete(r.pending, session)
	}
	r.finished[session] = summary
	r.mu.Unlock()
	return record, nil
}

// DiskMatchesBase reports whether file's disk content is still what it was
// when the file was first staged — applying over anything else would lose
// someone's change.
func DiskMatchesBase(workspaceRoot string, file store.AgentEditFile) (bool, error) {
	data, _, err := filetree.ReadContainedFile(workspaceRoot, file.Rel)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return !file.BaseExists, nil
	case err != nil:
		return false, err
	}
	return file.BaseExists && string(data) == file.BaseContent, nil
}

// replaceText applies a replacement to content. Matches don't overlap; with
// several and no All, the hint line picks one or the agent is told where
// they are.
func replaceText(content, rel string, replacement Replacement) (string, store.AgentEdit, int, error) {
	edit := store.AgentEdit{Op: OpReplace, Old: replacement.Old, New: replacement.New, All: replacement.All}
	offsets := matchOffsets(content, replacement.Old)
	switch {
	case len(offsets) == 0:
		return "", edit, 0, fmt.Errorf("the old text is not in %s — copy it exactly, whitespace included (ntee --msg show prints the staged content)", rel)
	case len(offsets) > 1 && !replacement.All && replacement.HintLine == 0:
		return "", edit, 0, fmt.Errorf("the old text matches %d times in %s (lines %s) — add surrounding lines to make it unique, add #L<n> to the path, or replace them all with --msg-replace-all",
			len(offsets), rel, matchLinesText(content, offsets))
	}
	if !replacement.All {
		offsets = []int{offsets[pickMatch(content, offsets, replacement.Old, replacement.HintLine)]}
	}
	var builder strings.Builder
	builder.Grow(len(content) + len(offsets)*(len(replacement.New)-len(replacement.Old)))
	previous := 0
	firstStart, lastEnd := -1, 0
	for _, offset := range offsets {
		builder.WriteString(content[previous:offset])
		if firstStart < 0 {
			firstStart = builder.Len()
		}
		builder.WriteString(replacement.New)
		lastEnd = builder.Len()
		previous = offset + len(replacement.Old)
	}
	builder.WriteString(content[previous:])
	next := builder.String()
	edit.LineFrom = lineAt(next, firstStart)
	edit.LineTo = max(edit.LineFrom, lineAt(next, max(firstStart, lastEnd-1)))
	edit.Added, edit.Removed = len(offsets)*lineCount(replacement.New), len(offsets)*lineCount(replacement.Old)
	return next, edit, len(offsets), nil
}

// matchOffsets are the byte offsets of old's non-overlapping matches.
func matchOffsets(content, old string) []int {
	var offsets []int
	for start := 0; start <= len(content); {
		index := strings.Index(content[start:], old)
		if index < 0 {
			break
		}
		offsets = append(offsets, start+index)
		start += index + len(old)
	}
	return offsets
}

// pickMatch is the index of the match covering hintLine, else the one
// starting nearest to it (the earlier one on a tie).
func pickMatch(content string, offsets []int, old string, hintLine int) int {
	if len(offsets) == 1 || hintLine <= 0 {
		return 0
	}
	best, bestDistance := 0, -1
	for index, offset := range offsets {
		first := lineAt(content, offset)
		last := lineAt(content, offset+max(0, len(old)-1))
		if hintLine >= first && hintLine <= last {
			return index
		}
		distance := min(abs(hintLine-first), abs(hintLine-last))
		if bestDistance < 0 || distance < bestDistance {
			best, bestDistance = index, distance
		}
	}
	return best
}

func matchLinesText(content string, offsets []int) string {
	const shown = 10
	lines := make([]string, 0, min(len(offsets), shown))
	for _, offset := range offsets[:min(len(offsets), shown)] {
		lines = append(lines, strconv.Itoa(lineAt(content, offset)))
	}
	text := strings.Join(lines, ", ")
	if len(offsets) > shown {
		text += ", …"
	}
	return text
}

// lineAt is the 1-based line of byte offset in content.
func lineAt(content string, offset int) int {
	return strings.Count(content[:min(max(0, offset), len(content))], "\n") + 1
}

// lineCount is how many lines text spans (a trailing newline ends a line,
// it starts none).
func lineCount(text string) int {
	if text == "" {
		return 0
	}
	return strings.Count(strings.TrimSuffix(text, "\n"), "\n") + 1
}

// LineChanges is a whole-content change's line stats: the first and last
// changed lines of next (1-based, 0 when nothing changed) and the lines
// added and removed.
func LineChanges(previous, next string) (lineFrom, lineTo, added, removed int) {
	nextLines := splitLines(next)
	position := 0 // lines of next passed so far
	for _, op := range diff.Lines(splitLines(previous), nextLines) {
		if op.Kind == diff.Equal {
			position++
			continue
		}
		// An added line is the next line of next; a removed one sat right
		// before it (the last line when it was removed at the end).
		line := min(position+1, max(1, len(nextLines)))
		if op.Kind == diff.Insert {
			added++
			position++
		} else {
			removed++
		}
		if lineFrom == 0 {
			lineFrom = line
		}
		lineTo = max(lineTo, line)
	}
	return lineFrom, lineTo, added, removed
}

func splitLines(content string) []string {
	if content == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(content, "\n"), "\n")
}

func countFiles(count int) string {
	if count == 1 {
		return "1 file"
	}
	return strconv.Itoa(count) + " files"
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
