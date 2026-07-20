package app

import (
	"fmt"
	"path"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nickooan/ntee-editor/internal/filetree"
	"github.com/nickooan/ntee-editor/internal/input"
	"github.com/nickooan/ntee-editor/internal/syntax"
	"github.com/nickooan/ntee-editor/internal/view"
)

// Repo-wide content search (Ctrl+G). Everything expensive runs off the UI
// goroutine: the file snapshot streams in per-batch Cmds (grepBatchMsg — each
// batch appends and schedules the next, so results are usable while indexing
// and closing the overlay cancels the rest of the chain), and searches are
// debounced (grepTickMsg) then scanned in a background Cmd (grepResultsMsg).
// Generation counters tag every message so stale batches, ticks, and results
// are dropped instead of clobbering newer state.

type grepFile struct {
	rel        string
	content    string  // CRLF-normalized; line boundaries match view.NormalizeLines
	lineStarts []int32 // byte offset of each line start; [0] == 0
}

type grepHit struct {
	rel  string
	line int
}

// maxGrepResults caps the hit list per query.
const maxGrepResults = 200

// Grep loads matching files' contents into memory, so bound how much it will
// hold to avoid ballooning RAM on a huge corpus (the corpus itself is already
// capped by Tree.MaxIndexFiles, but each file's text is resident here).
const (
	maxGrepFiles = 10000
	maxGrepBytes = 200 << 20 // 200 MB
)

// grepDebounce delays the search after the last keystroke so intermediate
// queries never pay for a whole-snapshot scan.
const grepDebounce = 100 * time.Millisecond

// grepLoadBatch bounds how many files each parallel load round reads, so the
// maxGrepFiles/maxGrepBytes caps stop the walk with at most one batch of
// wasted reads.
const grepLoadBatch = 512

// grepBatchMsg delivers one background-loaded batch of the snapshot. nextStart
// is the corpus index the next batch begins at; the handler schedules it, so
// the chain ends naturally when a message is dropped as stale.
type grepBatchMsg struct {
	gen       int
	files     []grepFile
	nextStart int
}

// grepTickMsg fires when the debounce window after a query change elapses.
type grepTickMsg struct{ gen int }

// grepResultsMsg delivers a background scan's hits.
type grepResultsMsg struct {
	gen     int
	results []grepHit
}

// openGrep opens the overlay immediately and kicks off the snapshot load in
// the background. From edit mode with unsaved changes it asks the user to save
// first — Enter on a hit opens another file, which would silently discard the
// buffer.
func (m Model) openGrep() (Model, tea.Cmd) {
	m = m.closeCompletion()
	if m.mode == modeEdit && m.edit.dirty {
		m.errText = "unsaved changes — save (Ctrl+S) before repo search"
		return m, nil
	}
	m, corpusCmd := m.ensureCorpus()
	m.grepOpen = true
	m.grepQuery = ""
	m.grepIndex = 0
	m.grepResults = nil
	m.grepFiles = nil
	m.grepHlRel = ""
	m.grepHl = nil
	m.grepPrevLines = nil
	m.grepLoading = true
	m.grepLoadBytes = 0
	m.grepGen++
	m.grepSearchGen++ // invalidates any tick left over from a prior session
	m.grepResultsGen = m.grepSearchGen
	return m, tea.Batch(corpusCmd, m.grepLoadBatchCmd(m.grepGen, 0))
}

// closeGrep hides the overlay and releases the snapshot — grepFiles can hold
// hundreds of MB and nothing keeps it useful between opens.
func (m Model) closeGrep() Model {
	m.grepOpen = false
	m.grepLoading = false
	m.grepLoadBytes = 0
	m.grepFiles = nil
	m.grepHlRel = ""
	m.grepHl = nil
	m.grepPrevLines = nil
	return m
}

// grepLoadBatchCmd reads one batch of the corpus' file contents off the UI
// goroutine with a bounded worker pool and delivers it via grepBatchMsg. The
// handler appends the batch and schedules the next one, so the snapshot streams
// in and results over the loaded prefix are usable while indexing continues.
// The captured corpus slice and root are never mutated (the corpusMsg handler
// replaces m.corpus wholesale), so reading them from the workers is safe.
func (m Model) grepLoadBatchCmd(gen, start int) tea.Cmd {
	root := m.root
	corpus := m.corpus
	sizeCap := m.cfg.Editor.MaxHighlightKB * 1024
	return func() tea.Msg {
		end := min(start+grepLoadBatch, len(corpus))
		batch := corpus[start:end]
		slots := make([]*grepFile, len(batch))
		var next atomic.Int64
		var wg sync.WaitGroup
		for w := 0; w < min(runtime.NumCPU(), len(batch)); w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					i := int(next.Add(1)) - 1
					if i >= len(batch) {
						return
					}
					f, ok := filetree.ReadViewFile(root, batch[i])
					if !ok || f.Binary {
						continue
					}
					if sizeCap > 0 && len(f.Content) > sizeCap {
						continue
					}
					content := strings.ReplaceAll(f.Content, "\r\n", "\n")
					slots[i] = &grepFile{rel: batch[i], content: content, lineStarts: buildLineStarts(content)}
				}
			}()
		}
		wg.Wait()
		files := make([]grepFile, 0, len(batch))
		for _, gf := range slots {
			if gf != nil {
				files = append(files, *gf)
			}
		}
		return grepBatchMsg{gen: gen, files: files, nextStart: end}
	}
}

// handleGrepBatch appends one loaded batch (in corpus order — batches arrive
// sequentially) and schedules the next. A search over the grown prefix fires
// when a query is typed and no search is pending — the pending case covers
// both an in-flight scan and an un-elapsed debounce, so batches never bypass
// the debounce or pile up scans. Completion always fires a final search: it is
// the guarantee that the displayed results cover the whole snapshot.
func (m Model) handleGrepBatch(msg grepBatchMsg) (tea.Model, tea.Cmd) {
	if !m.grepOpen || msg.gen != m.grepGen {
		return m, nil // stale: overlay closed or reopened — the chain ends here
	}
	truncated := false
	for _, gf := range msg.files {
		if len(m.grepFiles) >= maxGrepFiles || m.grepLoadBytes >= maxGrepBytes {
			truncated = true
			break
		}
		m.grepFiles = append(m.grepFiles, gf)
		m.grepLoadBytes += len(gf.content)
	}
	done := truncated || msg.nextStart >= len(m.corpus)
	var cmds []tea.Cmd
	if done {
		m.grepLoading = false
		if truncated {
			m.notice = fmt.Sprintf("repo search limited to %d files — narrow your root or add ignores", len(m.grepFiles))
		}
	} else {
		cmds = append(cmds, m.grepLoadBatchCmd(msg.gen, msg.nextStart))
	}
	if len([]rune(m.grepQuery)) >= 2 {
		searchIdle := m.grepResultsGen == m.grepSearchGen
		if done || (searchIdle && len(msg.files) > 0) {
			m.grepSearchGen++
			cmds = append(cmds, m.grepSearchCmd())
		}
	}
	return m, tea.Batch(cmds...)
}

// handleGrepTick runs the debounced search over whatever prefix of the
// snapshot has loaded so far, unless a newer keystroke superseded this tick.
// While indexing, later batches re-search the grown prefix and completion
// fires the covering search.
func (m Model) handleGrepTick(msg grepTickMsg) (tea.Model, tea.Cmd) {
	if !m.grepOpen || msg.gen != m.grepSearchGen {
		return m, nil
	}
	return m, m.grepSearchCmd()
}

// handleGrepResults lands a scan's hits, dropping results for outdated queries.
func (m Model) handleGrepResults(msg grepResultsMsg) (tea.Model, tea.Cmd) {
	if !m.grepOpen || msg.gen != m.grepSearchGen {
		return m, nil
	}
	m.grepResults = msg.results
	m.grepResultsGen = msg.gen
	m.grepIndex = 0
	return m.refreshGrepPreview(), nil
}

// queueGrepSearch registers a query change: short queries clear synchronously,
// otherwise a debounce tick is scheduled. The generation bump invalidates any
// earlier pending tick, so only the last keystroke's tick fires a search. Old
// results stay displayed until the new ones land (no flicker).
func (m Model) queueGrepSearch() (Model, tea.Cmd) {
	m.grepSearchGen++
	if len([]rune(m.grepQuery)) < 2 {
		m.grepResults = nil
		m.grepIndex = 0
		m.grepResultsGen = m.grepSearchGen
		m.grepHlRel, m.grepHl, m.grepPrevLines = "", nil, nil
		return m, nil
	}
	gen := m.grepSearchGen
	return m, tea.Tick(grepDebounce, func(time.Time) tea.Msg { return grepTickMsg{gen: gen} })
}

// grepSearchCmd scans the snapshot off the UI goroutine. Contiguous chunks go
// to a bounded worker pool — per-worker hit slices need no locks, and merging
// in worker order keeps results in corpus order, identical to a sequential
// scan. The workers share one compiled regex: since Go 1.12 regexp pools its
// match state internally, so concurrent use scales without Copy.
func (m Model) grepSearchCmd() tea.Cmd {
	gen := m.grepSearchGen
	query := m.grepQuery
	files := m.grepFiles
	return func() tea.Msg {
		re := view.CreateMultilineSearchRegex(query)
		if re == nil || len(files) == 0 {
			return grepResultsMsg{gen: gen}
		}
		workers := min(runtime.NumCPU(), len(files))
		chunk := (len(files) + workers - 1) / workers
		hitsByWorker := make([][]grepHit, workers)
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			lo, hi := w*chunk, min((w+1)*chunk, len(files))
			if lo >= hi {
				break
			}
			wg.Add(1)
			go func(w, lo, hi int) {
				defer wg.Done()
				var hits []grepHit
				for _, f := range files[lo:hi] {
					hits = appendGrepHits(hits, f, re, maxGrepResults)
					if len(hits) >= maxGrepResults {
						break
					}
				}
				hitsByWorker[w] = hits
			}(w, lo, hi)
		}
		wg.Wait()
		var results []grepHit
		for _, hits := range hitsByWorker {
			take := min(len(hits), maxGrepResults-len(results))
			results = append(results, hits[:take]...)
			if len(results) >= maxGrepResults {
				break
			}
		}
		return grepResultsMsg{gen: gen, results: results}
	}
}

// buildLineStarts returns the byte offset of every line start in content.
func buildLineStarts(content string) []int32 {
	starts := []int32{0}
	for i := 0; i < len(content); i++ {
		if content[i] == '\n' {
			starts = append(starts, int32(i+1))
		}
	}
	return starts
}

// lineForOffset returns the index of the line containing byte offset off.
func lineForOffset(starts []int32, off int) int {
	return sort.Search(len(starts), func(i int) bool { return int(starts[i]) > off }) - 1
}

// appendGrepHits appends one hit per matching line of f to dst, scanning the
// whole content instead of line-at-a-time — regexp's literal-prefix fast path
// makes this far cheaper than a MatchString call per line. After each hit the
// scan resumes at the next line start: same-line matches dedupe to one hit and
// the offset strictly advances, so even empty-width matches terminate. A
// multi-line-spanning match (e.g. via (?s)) reports its start line.
func appendGrepHits(dst []grepHit, f grepFile, re *regexp.Regexp, limit int) []grepHit {
	off := 0
	for off < len(f.content) {
		loc := re.FindStringIndex(f.content[off:])
		if loc == nil {
			break
		}
		line := lineForOffset(f.lineStarts, off+loc[0])
		dst = append(dst, grepHit{rel: f.rel, line: line})
		if len(dst) >= limit || line+1 >= len(f.lineStarts) {
			break
		}
		off = int(f.lineStarts[line+1])
	}
	return dst
}

// grepSelectedFile returns the corpus entry for the current selection.
func (m Model) grepSelectedFile() (grepFile, grepHit, bool) {
	if len(m.grepResults) == 0 {
		return grepFile{}, grepHit{}, false
	}
	hit := m.grepResults[input.Clamp(m.grepIndex, 0, len(m.grepResults)-1)]
	for _, f := range m.grepFiles {
		if f.rel == hit.rel {
			return f, hit, true
		}
	}
	return grepFile{}, grepHit{}, false
}

func (m Model) handleGrepKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch msg.Type {
	case tea.KeyEsc:
		m = m.closeGrep()
	case tea.KeyUp:
		m.grepIndex = max(0, m.grepIndex-1)
		m = m.refreshGrepPreview()
	case tea.KeyDown:
		m.grepIndex = min(max(0, len(m.grepResults)-1), m.grepIndex+1)
		m = m.refreshGrepPreview()
	case tea.KeyEnter:
		_, hit, ok := m.grepSelectedFile()
		m = m.closeGrep()
		if !ok {
			break
		}
		m = m.openFileAt(hit.rel)
		if m.mode == modeEdit {
			m.edit.cy = input.Clamp(hit.line, 0, len(m.edit.lines)-1)
			m.edit.cx = 0
			m = m.anchorCursorLine()
		}
	case tea.KeyBackspace:
		if runes := []rune(m.grepQuery); len(runes) > 0 {
			m.grepQuery = string(runes[:len(runes)-1])
			m, cmd = m.queueGrepSearch()
		}
	case tea.KeySpace:
		m.grepQuery += " "
		m, cmd = m.queueGrepSearch()
	case tea.KeyRunes:
		m.grepQuery += string(msg.Runes)
		m, cmd = m.queueGrepSearch()
	}
	return m, cmd
}

// refreshGrepPreview keeps the selected hit's preview lines and syntax
// highlighting cached (whole-buffer tokenize + line split, re-run only when
// the selection changes file).
func (m Model) refreshGrepPreview() Model {
	f, _, ok := m.grepSelectedFile()
	if !ok {
		m.grepHlRel, m.grepHl, m.grepPrevLines = "", nil, nil
		return m
	}
	if f.rel == m.grepHlRel {
		return m
	}
	m.grepHlRel = f.rel
	m.grepPrevLines = strings.Split(f.content, "\n") // pre-normalized ⇒ same as NormalizeLines
	m.grepHl = syntax.HighlightLines(path.Base(f.rel), f.content)
	return m
}
