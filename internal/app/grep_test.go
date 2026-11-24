package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/nickooan/ntee-editor/internal/view"
)

func grepFixture(t *testing.T) Model {
	t.Helper()
	m, root := newTestModel(t, nil)
	must(t, os.WriteFile(filepath.Join(root, "alpha.go"), []byte(
		"package main\n\n// grepNeedle here\nfunc a() {}\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(root, "beta.go"), []byte(
		"package main\n\nfunc b() {\n\t_ = \"grepNeedle too\"\n}\n"), 0o644))
	return rebuildCorpusNow(m) // the fixture's files postdate newTestModel's warm-up
}

// deliver feeds an async message to Update and returns the model + follow-up Cmd.
func deliver(m Model, msg tea.Msg) (Model, tea.Cmd) {
	next, cmd := m.Update(msg)
	return next.(Model), cmd
}

// execCmds expands a Cmd (possibly a tea.Batch) into the messages it produces.
func execCmds(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, execCmds(c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

// grepLoad drives the whole batch chain to completion synchronously,
// delivering every message it produces (batches, searches, results) in order.
func grepLoad(t *testing.T, m Model) Model {
	t.Helper()
	queue := []tea.Msg{m.grepLoadBatchCmd(m.grep.gen, 0)()}
	for len(queue) > 0 {
		msg := queue[0]
		queue = queue[1:]
		var cmd tea.Cmd
		m, cmd = deliver(m, msg)
		queue = append(queue, execCmds(cmd)...)
	}
	return m
}

// grepSettle fires the pending debounce tick and delivers the search results
// and the follow-up preview highlight, as if grepDebounce elapsed and the
// background scan and tokenize completed.
func grepSettle(t *testing.T, m Model) Model {
	t.Helper()
	queue := []tea.Msg{grepTickMsg{gen: m.grep.searchGen}}
	for len(queue) > 0 {
		msg := queue[0]
		queue = queue[1:]
		var cmd tea.Cmd
		m, cmd = deliver(m, msg)
		queue = append(queue, execCmds(cmd)...)
	}
	return m
}

func TestGrepSearchAcrossFiles(t *testing.T) {
	m := grepFixture(t)
	m = key(m, ctrlKey('g'))
	if !m.grep.open || !m.grep.loading || m.grep.files != nil {
		t.Fatal("ctrl+g should open instantly and index in the background")
	}
	m = grepLoad(t, m)
	if m.grep.loading || len(m.grep.files) == 0 {
		t.Fatal("snapshot should be loaded")
	}

	// One rune → no search yet (cleared synchronously, no debounce).
	m = runes(m, "g")
	if len(m.grep.results) != 0 {
		t.Fatal("sub-2-rune query must not search")
	}
	m = runes(m, "repNeedle")
	if len(m.grep.results) != 0 {
		t.Fatal("results must not appear before the debounce settles")
	}
	m = grepSettle(t, m)
	if len(m.grep.results) != 2 {
		t.Fatalf("want 2 hits, got %+v", m.grep.results)
	}
	if m.grep.results[0].rel != "alpha.go" || m.grep.results[0].line != 2 {
		t.Fatalf("first hit: %+v", m.grep.results[0])
	}
	if m.grep.hlRel != "alpha.go" || m.grep.hl == nil || m.grep.prevLines == nil {
		t.Fatalf("preview should follow selection: %q", m.grep.hlRel)
	}

	// ↓ moves the selection; preview cache follows the new file.
	m = key(m, keyPress(tea.KeyDown))
	if m.grep.index != 1 || m.grep.hlRel != "beta.go" {
		t.Fatalf("selection/preview: idx=%d rel=%q", m.grep.index, m.grep.hlRel)
	}

	// Enter opens the selected hit in edit mode, anchored.
	m = key(m, keyPress(tea.KeyEnter))
	if m.grep.open || m.openRel != "beta.go" || m.mode != modeEdit {
		t.Fatalf("enter open failed: open=%q mode=%d", m.openRel, m.mode)
	}
	if m.edit.cy != 3 {
		t.Fatalf("cursor line: %d", m.edit.cy)
	}
}

func TestGrepLiteralFallbackAndCase(t *testing.T) {
	m := grepFixture(t)
	root := m.root
	must(t, os.WriteFile(filepath.Join(root, "paren.go"), []byte("package main\n\n// call foo(bar\n"), 0o644))
	m = rebuildCorpusNow(m)
	m = key(m, ctrlKey('g'))
	m = grepLoad(t, m)

	m = runes(m, "foo(bar") // invalid regex → literal fallback
	m = grepSettle(t, m)
	if len(m.grep.results) != 1 || m.grep.results[0].rel != "paren.go" {
		t.Fatalf("literal fallback: %+v", m.grep.results)
	}
	for range "foo(bar" {
		m = key(m, keyPress(tea.KeyBackspace))
	}
	m = runes(m, "GREPNEEDLE")
	m = grepSettle(t, m)
	if len(m.grep.results) != 2 {
		t.Fatalf("case-insensitive: %+v", m.grep.results)
	}
}

func TestGrepDirtyEditGuard(t *testing.T) {
	m := grepFixture(t)
	m = m.openFileAt("alpha.go")
	m = runes(m, "x") // dirty
	m = key(m, ctrlKey('g'))
	if m.grep.open {
		t.Fatal("grep must not open over unsaved changes")
	}
	if !strings.Contains(m.errText, "save (Ctrl+S)") {
		t.Fatalf("guard message: %q", m.errText)
	}
	m = key(m, ctrlKey('s'))
	m = key(m, ctrlKey('g'))
	if !m.grep.open {
		t.Fatal("grep should open after saving")
	}
}

func TestGrepEscClosesWithoutOpening(t *testing.T) {
	m := grepFixture(t)
	m = m.openFileAt("alpha.go")
	before := m.openRel
	m = key(m, ctrlKey('g'))
	m = grepLoad(t, m)
	m = runes(m, "grepNeedle")
	m = key(m, keyPress(tea.KeyEsc))
	if m.grep.open || m.openRel != before {
		t.Fatalf("esc should close in place: open=%q", m.openRel)
	}
	if m.grep.files != nil {
		t.Fatal("corpus should be released on close")
	}
	if m.grep.prevLines != nil || m.grep.hl != nil {
		t.Fatal("preview state should be released on close")
	}
}

func TestGrepOverlayLayoutSplit(t *testing.T) {
	m := grepFixture(t)
	m = key(m, ctrlKey('g'))
	if out := m.renderGrepOverlay(100, 30); !strings.Contains(out, "indexing") {
		t.Fatal("pre-load render should show the indexing state")
	}
	m = grepLoad(t, m)
	m = runes(m, "grepNeedle")
	m = grepSettle(t, m)
	// lipgloss v2 always emits ANSI (no TTY detection), so strip the escapes
	// before asserting on substrings that span style boundaries.
	out := ansi.Strip(m.renderGrepOverlay(100, 30))
	lines := strings.Split(out, "\n")
	if len(lines) < 25 {
		t.Fatalf("overlay should be tall: %d rows", len(lines))
	}
	if !strings.Contains(out, "grepNeedle here") {
		t.Fatal("preview should show the hit line content")
	}
	if !strings.Contains(out, "alpha.go:3") {
		t.Fatal("result rows should show name:line")
	}
	if !strings.Contains(out, "2 results") {
		t.Fatal("result count missing")
	}
}

// A load from a closed-then-reopened session must not clobber the new one.
func TestGrepStaleLoadDropped(t *testing.T) {
	m := grepFixture(t)
	m = key(m, ctrlKey('g'))
	staleLoad := m.grepLoadBatchCmd(m.grep.gen, 0) // captures the first open's generation
	m = key(m, keyPress(tea.KeyEsc))
	m = key(m, ctrlKey('g')) // grep.gen bumped
	m, _ = deliver(m, staleLoad())
	if m.grep.files != nil || !m.grep.loading {
		t.Fatal("stale load must be dropped")
	}
	m = grepLoad(t, m)
	if m.grep.loading || len(m.grep.files) == 0 {
		t.Fatal("current-generation load should land")
	}
}

// Ticks and results from a superseded query must be ignored.
func TestGrepStaleTickAndResultsDropped(t *testing.T) {
	m := grepFixture(t)
	m = key(m, ctrlKey('g'))
	m = grepLoad(t, m)
	m = runes(m, "gr")
	oldGen := m.grep.searchGen
	m = runes(m, "e") // newer keystroke supersedes the pending tick
	var cmd tea.Cmd
	m, cmd = deliver(m, grepTickMsg{gen: oldGen})
	if cmd != nil {
		t.Fatal("stale tick must not fire a search")
	}
	m, _ = deliver(m, grepResultsMsg{gen: oldGen, results: []grepHit{{rel: "alpha.go", line: 0}}})
	if len(m.grep.results) != 0 {
		t.Fatal("stale results must be dropped")
	}
}

// Typing while the snapshot streams in searches the loaded prefix, and load
// completion fires a covering search over the full snapshot.
func TestGrepSearchWhileIndexing(t *testing.T) {
	m := grepFixture(t)
	m = key(m, ctrlKey('g'))
	m = runes(m, "grepNeedle") // typed before any batch landed
	m = grepSettle(t, m)       // debounced search runs over the (empty) prefix
	if len(m.grep.results) != 0 {
		t.Fatalf("prefix search over an empty snapshot: %+v", m.grep.results)
	}
	m = grepLoad(t, m) // batches stream in; completion fires the covering search
	if m.grep.loading || len(m.grep.results) != 2 {
		t.Fatalf("full-snapshot search should land after indexing: %+v", m.grep.results)
	}
}

// A partial batch appends, keeps loading, and serves progressive results.
func TestGrepIncrementalDelivery(t *testing.T) {
	m := grepFixture(t)
	m = key(m, ctrlKey('g'))
	m = runes(m, "grepNeedle")
	m = grepSettle(t, m) // empty-prefix search lands → search idle

	content := "has grepNeedle\n"
	part := grepFile{rel: "part.txt", content: content, lineStarts: buildLineStarts(content)}
	var cmd tea.Cmd
	m, cmd = deliver(m, grepBatchMsg{gen: m.grep.gen, files: []grepFile{part}, nextStart: 1})
	if !m.grep.loading || len(m.grep.files) != 1 {
		t.Fatalf("partial batch should append and keep loading: %d files", len(m.grep.files))
	}
	if cmd == nil {
		t.Fatal("expected next-batch and progressive-search commands")
	}
	// Deliver only the progressive search's results (the real next-batch
	// message is discarded) to observe results landing mid-index.
	for _, msg := range execCmds(cmd) {
		if res, ok := msg.(grepResultsMsg); ok {
			m, _ = deliver(m, res)
		}
	}
	if len(m.grep.results) != 1 || m.grep.results[0].rel != "part.txt" {
		t.Fatalf("progressive results should land while indexing: %+v", m.grep.results)
	}
	if !m.grep.loading {
		t.Fatal("indexing should still be in progress")
	}

	// The final batch completes the load and fires the covering search.
	m, cmd = deliver(m, grepBatchMsg{gen: m.grep.gen, files: nil, nextStart: len(m.corpus)})
	if m.grep.loading {
		t.Fatal("final batch should finish indexing")
	}
	if cmd == nil {
		t.Fatal("completion must fire the covering search")
	}
}

// A closed overlay must ignore results that were in flight.
func TestGrepEscDropsInFlightResults(t *testing.T) {
	m := grepFixture(t)
	m = key(m, ctrlKey('g'))
	m = grepLoad(t, m)
	m = runes(m, "grepNeedle")
	gen := m.grep.searchGen
	m = key(m, keyPress(tea.KeyEsc))
	m, _ = deliver(m, grepResultsMsg{gen: gen, results: []grepHit{{rel: "alpha.go", line: 0}}})
	if m.grep.open || m.grep.files != nil || len(m.grep.results) != 0 {
		t.Fatal("closed overlay must ignore in-flight results")
	}
}

// Whole-content matching must agree with the old line-at-a-time semantics.
func TestGrepWholeContentMatching(t *testing.T) {
	content := "foo bar foo\nbaz\nFOO end"
	f := grepFile{rel: "x.txt", content: content, lineStarts: buildLineStarts(content)}

	// Same-line matches dedupe to one hit; case-insensitive; last line without
	// a trailing newline still matches.
	re := view.CreateMultilineSearchRegex("foo")
	hits := appendGrepHits(nil, f, re, maxGrepResults)
	want := []grepHit{{rel: "x.txt", line: 0}, {rel: "x.txt", line: 2}}
	if len(hits) != len(want) || hits[0] != want[0] || hits[1] != want[1] {
		t.Fatalf("hits = %+v, want %+v", hits, want)
	}

	// $ anchors at line ends, not only end-of-file — the (?m) fix.
	re = view.CreateMultilineSearchRegex("bar foo$")
	hits = appendGrepHits(nil, f, re, maxGrepResults)
	if len(hits) != 1 || hits[0].line != 0 {
		t.Fatalf("$ should anchor per line: %+v", hits)
	}
	re = view.CreateMultilineSearchRegex("^baz")
	hits = appendGrepHits(nil, f, re, maxGrepResults)
	if len(hits) != 1 || hits[0].line != 1 {
		t.Fatalf("^ should anchor per line: %+v", hits)
	}

	// Empty-width matches terminate and yield one hit per line; the limit
	// truncates within a file.
	re = view.CreateMultilineSearchRegex("^")
	hits = appendGrepHits(nil, f, re, 2)
	if len(hits) != 2 || hits[0].line != 0 || hits[1].line != 1 {
		t.Fatalf("limit truncation: %+v", hits)
	}
}

// Line starts must agree with view.NormalizeLines on CRLF input, so hit line
// numbers match what the preview displays.
func TestGrepLineStartsMatchNormalizeLines(t *testing.T) {
	raw := "a\r\nbb\r\nccc\nno-trailing"
	content := strings.ReplaceAll(raw, "\r\n", "\n")
	starts := buildLineStarts(content)
	lines := view.NormalizeLines(raw)
	if len(starts) != len(lines) {
		t.Fatalf("%d line starts vs %d lines", len(starts), len(lines))
	}
	for i, s := range starts {
		end := len(content)
		if i+1 < len(starts) {
			end = int(starts[i+1]) - 1
		}
		if content[s:end] != lines[i] {
			t.Fatalf("line %d: %q != %q", i, content[s:end], lines[i])
		}
	}
	for i, s := range starts {
		if got := lineForOffset(starts, int(s)); got != i {
			t.Fatalf("lineForOffset(start of %d) = %d", i, got)
		}
	}
}

// The parallel scan must return identical, corpus-ordered results every run.
func TestGrepParallelDeterministic(t *testing.T) {
	m, root := newTestModel(t, nil)
	for i := 0; i < 100; i++ {
		must(t, os.WriteFile(filepath.Join(root, fmt.Sprintf("f%03d.go", i)),
			[]byte(fmt.Sprintf("package main\n// parNeedle %d\n", i)), 0o644))
	}
	m = rebuildCorpusNow(m)
	m = key(m, ctrlKey('g'))
	m = grepLoad(t, m)
	m = runes(m, "parNeedle")

	var prev []grepHit
	for round := 0; round < 5; round++ {
		got := grepSettle(t, m).grep.results
		if len(got) != 100 {
			t.Fatalf("round %d: want 100 hits, got %d", round, len(got))
		}
		for i := 1; i < len(got); i++ {
			if got[i-1].rel >= got[i].rel {
				t.Fatalf("round %d: results out of corpus order at %d: %+v", round, i, got[i-1:i+1])
			}
		}
		if round > 0 {
			for i := range got {
				if got[i] != prev[i] {
					t.Fatalf("round %d: nondeterministic result at %d: %+v vs %+v", round, i, got[i], prev[i])
				}
			}
		}
		prev = got
	}
}

// Ctrl+J inserts a newline; pasted CR/CRLF line endings normalize to \n so a
// multi-line literal matches the snapshot's normalized content end-to-end.
func TestGrepMultilineQuery(t *testing.T) {
	m := grepFixture(t)
	m = key(m, ctrlKey('g'))
	m = grepLoad(t, m)

	m = runes(m, "here")
	m = key(m, ctrlKey('j'))
	m = runes(m, "func")
	if m.grep.query != "here\nfunc" {
		t.Fatalf("ctrl+j should insert a newline: %q", m.grep.query)
	}
	m = grepSettle(t, m)
	if len(m.grep.results) != 1 || m.grep.results[0].rel != "alpha.go" || m.grep.results[0].line != 2 {
		t.Fatalf("multi-line literal should hit alpha.go at its start line: %+v", m.grep.results)
	}

	// Bracketed paste arrives as one PasteMsg with raw CR/CRLF endings.
	m = key(m, keyPress(tea.KeyEsc))
	m = key(m, ctrlKey('g'))
	m = grepLoad(t, m)
	next, _ := m.Update(tea.PasteMsg{Content: "here\r\nfunc"})
	m = next.(Model)
	if m.grep.query != "here\nfunc" {
		t.Fatalf("pasted CRLF should normalize: %q", m.grep.query)
	}
	m = key(m, keyPress(tea.KeyBackspace))
	if m.grep.query != "here\nfun" {
		t.Fatalf("backspace should cross into the last line: %q", m.grep.query)
	}
}

// The overlay box height must not change as the query grows lines, including
// past the grepMaxInputRows display cap.
func TestGrepOverlayMultilineInputHeightStable(t *testing.T) {
	m := grepFixture(t)
	m = key(m, ctrlKey('g'))
	m = grepLoad(t, m)
	m = runes(m, "grepNeedle")
	m = grepSettle(t, m)
	base := strings.Count(m.renderGrepOverlay(100, 30), "\n")
	for lines := 2; lines <= 5; lines++ {
		m = key(m, ctrlKey('j'))
		m = runes(m, "x")
		if got := strings.Count(m.renderGrepOverlay(100, 30), "\n"); got != base {
			t.Fatalf("%d-line query changed overlay height: %d != %d", lines, got, base)
		}
	}
}

// Arrows move the query cursor with editor semantics (vertical moves clamp the
// column; up/down fall through to result navigation at the boundary lines),
// and insert/backspace/delete work at the cursor position.
func TestGrepQueryCursorEditing(t *testing.T) {
	m := grepFixture(t)
	m = key(m, ctrlKey('g'))
	m = grepLoad(t, m)
	m = runes(m, "abc")
	m = key(m, ctrlKey('j'))
	m = runes(m, "xyz")

	m = key(m, keyPress(tea.KeyLeft))
	m = key(m, keyPress(tea.KeyUp))
	if m.grep.cursor != 2 {
		t.Fatalf("left+up should land on line 0 col 2: %d", m.grep.cursor)
	}
	m = runes(m, "Q")
	if m.grep.query != "abQc\nxyz" || m.grep.cursor != 3 {
		t.Fatalf("insert at cursor: %q cur=%d", m.grep.query, m.grep.cursor)
	}
	m = key(m, keyPress(tea.KeyDelete))
	if m.grep.query != "abQ\nxyz" || m.grep.cursor != 3 {
		t.Fatalf("forward delete at cursor: %q cur=%d", m.grep.query, m.grep.cursor)
	}
	m = key(m, keyPress(tea.KeyDown))
	if m.grep.cursor != len([]rune(m.grep.query)) {
		t.Fatalf("down should clamp the column to the target line end: %d", m.grep.cursor)
	}
	m = key(m, keyPress(tea.KeyHome))
	if m.grep.cursor != 4 {
		t.Fatalf("home should go to the line start: %d", m.grep.cursor)
	}
	m = key(m, keyPress(tea.KeyBackspace))
	if m.grep.query != "abQxyz" || m.grep.cursor != 3 {
		t.Fatalf("backspace at line start should join lines: %q cur=%d", m.grep.query, m.grep.cursor)
	}

	// Single-line again: up/down fall through to the result list.
	before := m.grep.cursor
	m = key(m, keyPress(tea.KeyUp))
	m = key(m, keyPress(tea.KeyDown))
	if m.grep.cursor != before {
		t.Fatalf("boundary up/down must not move the cursor: %d", m.grep.cursor)
	}
}
