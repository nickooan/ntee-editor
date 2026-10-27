package app

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/extmsg"
	"github.com/nickooan/ntee-editor/internal/filetree"
	"github.com/nickooan/ntee-editor/internal/input"
	"github.com/nickooan/ntee-editor/internal/store"
)

// reviewFileResolvedMsg delivers one review-session file with its repo
// resolved, ready to be inserted into the live review.
type reviewFileResolvedMsg struct {
	gen  int
	file reviewFile
}

// reviewSessionRunning reports whether the user is in a started review
// session — the live review is on screen until they quit it.
func (m Model) reviewSessionRunning() bool {
	return m.session.kind == extmsg.SessionReview && m.session.started
}

// receiveReviewSessionFile takes a review session's file: it is remembered
// in arrival order, one entry per note — a note on a line the file already
// has a note on replaces that one, a send without a note keeps the notes —
// and once the user started the session it goes into the review at once.
// The reply is what the agent hears.
func (m Model) receiveReviewSessionFile(request reviewRequest) (Model, tea.Cmd, string) {
	listed := slices.ContainsFunc(m.session.reviews, func(queued reviewRequest) bool {
		return queued.workspaceRel == request.workspaceRel
	})
	m.session.reviews = withQueuedReview(m.session.reviews, request)
	count := strconv.Itoa(reviewFileCount(m.session.reviews))
	if !m.session.started {
		if listed {
			return m, nil, "file " + request.workspaceRel + " updated — " + count + " files queued"
		}
		return m, nil, "file " + count + " queued"
	}
	m, cmd := m.addReviewSessionFile([]reviewRequest{request})
	if listed {
		return m, cmd, "file " + request.workspaceRel + " refreshed"
	}
	return m, cmd, "file " + count + " added to the review"
}

// withQueuedReview adds a request to a session's queue, keeping one entry
// per (file, note line): a file's note-less entry gives way to its first
// note, and a note-less resend of a queued file adds nothing.
func withQueuedReview(queue []reviewRequest, request reviewRequest) []reviewRequest {
	sameFile := func(queued reviewRequest) bool { return queued.workspaceRel == request.workspaceRel }
	if request.note == "" {
		if slices.ContainsFunc(queue, sameFile) {
			return queue
		}
		return append(slices.Clip(queue), request)
	}
	index := slices.IndexFunc(queue, func(queued reviewRequest) bool {
		return sameFile(queued) && (queued.note == "" || queued.line == request.line)
	})
	if index < 0 {
		return append(slices.Clip(queue), request)
	}
	queue = slices.Clone(queue)
	queue[index] = request
	return queue
}

// enterReviewSession opens the live review of a review session the user just
// started, with every file the agent queued before the answer. A session
// the agent already ended is saved now.
func (m Model) enterReviewSession() (Model, tea.Cmd) {
	m = m.leaveToEditOrQuery()
	m.review = reviewState{
		base:         m.session.base,
		gen:          m.review.gen + 1,
		previousMode: m.mode,
		session:      m.session.id,
		follow:       true,
		fileByPath:   map[string]int{},
	}
	m.mode = modeReview
	m = m.refreshReviewTree()
	// A file's queued notes go in together, so it resolves and loads once.
	var byFile [][]reviewRequest
	indexByPath := map[string]int{}
	for _, request := range m.session.reviews {
		if index, seen := indexByPath[request.workspaceRel]; seen {
			byFile[index] = append(byFile[index], request)
			continue
		}
		indexByPath[request.workspaceRel] = len(byFile)
		byFile = append(byFile, []reviewRequest{request})
	}
	cmds := make([]tea.Cmd, 0, len(byFile)+1)
	for _, requests := range byFile {
		var cmd tea.Cmd
		m, cmd = m.addReviewSessionFile(requests)
		cmds = append(cmds, cmd)
	}
	if m.session.complete {
		var save tea.Cmd
		m, save = m.saveReviewSession(store.ExternalReviewed, "the agent ended the session")
		cmds = append(cmds, save)
	}
	return m, tea.Batch(cmds...)
}

// addReviewSessionFile puts an arriving file (requests all name it, one per
// note) into the live review: a file already listed reloads with the notes
// added; a new one has its repo resolved off the UI goroutine first
// (handleReviewFileResolved inserts it).
func (m Model) addReviewSessionFile(requests []reviewRequest) (Model, tea.Cmd) {
	rel := requests[0].workspaceRel
	m.review.newestPath = rel
	if index, listed := m.review.fileByPath[rel]; listed {
		return m.refreshReviewFile(index, requests)
	}
	return m, reviewResolveCmd(m.review.gen, m.workspaceRoot, m.newReviewFile(requests...))
}

// reviewResolveCmd resolves one arriving file's repo.
func reviewResolveCmd(gen int, workspaceRoot string, file reviewFile) tea.Cmd {
	return func() tea.Msg {
		return reviewFileResolvedMsg{gen: gen, file: resolveReviewFile(workspaceRoot, filetree.IsGitRepo(workspaceRoot), file)}
	}
}

// handleReviewFileResolved inserts a resolved file in tree order, keeps the
// selection on the file it was on, lands the arrival, and loads its diff. A
// file listed meanwhile (resent while resolving) is refreshed instead.
func (m Model) handleReviewFileResolved(msg reviewFileResolvedMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.review.gen || m.mode != modeReview {
		return m, nil
	}
	if index, listed := m.review.fileByPath[msg.file.workspaceRel]; listed {
		return m.refreshReviewFile(index, msg.file.noteRequests())
	}
	selectedPath := ""
	if file, ok := m.selectedReviewFile(); ok {
		selectedPath = file.workspaceRel
	}
	files := inTreeOrder(append(slices.Clone(m.review.files), msg.file))
	m.review.files = files
	m.review.fileByPath = make(map[string]int, len(files))
	for index, file := range files {
		m.review.fileByPath[file.workspaceRel] = index
	}
	if selectedPath != "" {
		m.review.selection.index = m.review.fileByPath[selectedPath]
	}
	m = m.refreshReviewTree()
	return m.landReviewArrival(msg.file.workspaceRel, selectedPath == "")
}

// refreshReviewFile reloads a resent file with the requests' notes added:
// the newer load wins over one still in flight (loadGen).
func (m Model) refreshReviewFile(index int, requests []reviewRequest) (Model, tea.Cmd) {
	files := slices.Clone(m.review.files)
	file := files[index]
	for _, request := range requests {
		file = m.addReviewNote(file, request)
	}
	file.loadGen++
	file.status, file.document, file.corpus, file.problem = reviewLoading, diffDocument{}, "", ""
	files[index] = file
	m.review.files = files
	if index == m.review.selection.index {
		m.review.search = documentSearch{}
	}
	return m.landReviewArrival(file.workspaceRel, false)
}

// landReviewArrival follows an arriving (not yet loaded) file — selects it,
// which loads it — while the view follows or nothing is selected yet;
// otherwise it loads in the background, marked unseen and announced in the
// status bar.
func (m Model) landReviewArrival(rel string, nothingSelected bool) (Model, tea.Cmd) {
	if m.review.follow || nothingSelected {
		following := m.review.follow
		m, load := m.selectReviewPath(rel)
		// Arrivals can resolve out of order: following one that is not the
		// newest must not stop the view from following.
		m.review.follow = following
		return m, load
	}
	load := m.reviewLoadPathCmd(rel)
	if current, ok := m.selectedReviewFile(); ok && current.workspaceRel == rel {
		return m, load // the file on screen was resent: it reloads in place
	}
	unseen := make(map[string]bool, len(m.review.unseen)+1)
	for path := range m.review.unseen {
		unseen[path] = true
	}
	unseen[rel] = true
	m.review.unseen = unseen
	m.notice = fmt.Sprintf("+ file from the agent: %s · %d new", rel, len(unseen))
	return m, load
}

// reviewSessionSelected applies a selection to the live review: the view
// follows arrivals again once the user is on the newest file, and the file
// is no longer unseen.
func (m Model) reviewSessionSelected(rel string) Model {
	m.review.follow = rel == m.review.newestPath
	if m.review.unseen[rel] {
		unseen := make(map[string]bool, len(m.review.unseen))
		for path := range m.review.unseen {
			if path != rel {
				unseen[path] = true
			}
		}
		m.review.unseen = unseen
	}
	return m
}

// reviewLoadPathCmd loads one file of the review, wherever it now sits.
func (m Model) reviewLoadPathCmd(rel string) tea.Cmd {
	index, ok := m.review.fileByPath[rel]
	if !ok {
		return nil
	}
	return m.reviewLoadCmd(index, index+1, -1)
}

// reviewNoteHit reports whether terminal cell (x, y) lands on the selected
// review file's note box — the same placement renderReview draws, below the
// one-line file header.
func (m Model) reviewNoteHit(x, y int) (lineNoteBox, bool) {
	file, ok := m.selectedReviewFile()
	if m.mode != modeReview || !ok {
		return lineNoteBox{}, false
	}
	height := m.reviewRowsHeight()
	box, ok := m.reviewNoteBox(file, max(3, m.width-m.sidebarWidth())-4, height)
	if !ok {
		return box, false
	}
	paneRow := y - 2 - 1
	paneCol := x - (m.sidebarWidth() + 1)
	inRows := paneRow >= max(0, box.top) && paneRow < min(height, box.top+box.height())
	inColumns := paneCol >= box.left && paneCol < box.left+box.width
	return box, inRows && inColumns
}

// scrollReviewNote moves the selected file's note text by delta rows within
// its capped box.
func (m Model) scrollReviewNote(box lineNoteBox, delta int) Model {
	index := m.review.selection.index
	files := slices.Clone(m.review.files)
	file := files[index]
	if len(file.notes) == 0 {
		return m
	}
	notes := slices.Clone(file.notes)
	active := min(file.activeNote, len(notes)-1)
	notes[active].overlay.scroll = input.Clamp(box.scroll+delta, 0, len(box.lines)-box.bodyRows)
	files[index].notes = notes
	m.review.files = files
	return m
}

// quitReviewSessionChoice asks before the user leaves a live review: quitting
// tells the agent to stop sending files.
func (m Model) quitReviewSessionChoice() choiceOverlay {
	choice := choiceOverlay{kind: choiceQuitReviewSession, title: "Quit the review session?", choices: []string{"Quit", "Stay"}}
	files := fmt.Sprintf("%d files reviewed in session %s", reviewFileCount(m.session.reviews), m.session.id)
	if m.session.complete {
		choice.body = []overlayLine{{text: files, style: overlayLineStrong}, {text: "the agent has finished sending files", style: overlayLineDim}}
	} else {
		choice.body = []overlayLine{{text: files, style: overlayLineStrong}, {text: "the agent will be told to stop sending files", style: overlayLineHighlight}}
	}
	return choice
}

// quitReviewSession ends the live review the user chose to leave: the agent
// hears it is over (finished when it had sent end, else told to stop), the
// session is saved to history unless the agent's end already did, and the
// user is back where they were.
func (m Model) quitReviewSession() (Model, tea.Cmd) {
	if !m.reviewSessionRunning() {
		return m.exitReview(), nil
	}
	id, files := m.session.id, reviewFileCount(m.session.reviews)
	m, save := m.saveReviewSession(store.ExternalReviewed, "the user quit the review")
	if m.session.complete {
		m = m.endSession("was finished — the user reviewed all "+strconv.Itoa(files)+" files", true)
	} else {
		m = m.endSession("was closed by the user — the user quit the review", false)
	}
	m = m.exitReview()
	m.notice = "review session " + id + " ended"
	return m, save
}

// saveReviewSession writes the review session's one history record — every
// file in arrival order with its note — once: at the agent's end or when the
// session closes, whichever comes first. The write runs off the UI
// goroutine; the returned cmd is nil when there is nothing to save.
func (m Model) saveReviewSession(status store.ExternalStatus, note string) (Model, tea.Cmd) {
	historyStore, settingsStore := m.external.historyStore, m.external.settingsStore
	if m.session.kind != extmsg.SessionReview || m.session.saved || len(m.session.reviews) == 0 || historyStore == nil {
		return m, nil
	}
	m.session.saved = true
	record := m.reviewSessionRecord(status, note, time.Now())
	return m, func() tea.Msg {
		limit := store.DefaultExternalHistoryLimit
		if settingsStore != nil {
			if settings, err := settingsStore.LoadExternalSettings(); err == nil {
				limit = settings.HistoryLimit
			}
		}
		return externalStatusSavedMsg{err: historyStore.AppendExternal(record, limit)}
	}
}

// reviewSessionRecord is the history record of the running review session:
// a review of its files against the session's base, one entry per note
// (anchors kept, so a replay puts each note back on its line; enterReview
// merges a file's entries again).
func (m Model) reviewSessionRecord(status store.ExternalStatus, note string, at time.Time) store.ExternalRecord {
	paths := make([]string, len(m.session.reviews))
	notes := make([]string, len(m.session.reviews))
	for index, request := range m.session.reviews {
		paths[index] = request.workspaceRel
		if request.line > 0 {
			paths[index] += "#L" + strconv.Itoa(request.line)
		}
		notes[index] = request.note
	}
	args := []string{}
	if m.session.base != "" {
		args = []string{m.session.base}
	}
	return store.ExternalRecord{
		Key:        store.NewExternalRecordKey(at),
		Command:    extmsg.CommandReview,
		Args:       args,
		Base:       cmp.Or(m.session.base, store.ExternalDefaultBase),
		Paths:      paths,
		PathNotes:  notes,
		Raw:        fmt.Sprintf("review session %s · %d files", m.session.id, reviewFileCount(m.session.reviews)),
		ReceivedAt: at,
		Status:     status,
		Note:       note,
		Session:    m.session.id,
	}
}
