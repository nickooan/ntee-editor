package app

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/input"
	"github.com/nickooan/ntee-editor/internal/view"
)

// The document-preview engine shared by the OpenAPI and GraphQL modes. The
// two modes were byte-identical apart from three seams — how a file detects,
// how the document is produced, and what a sidebar outline row shows — so
// each mode is a previewKind descriptor (struct of function fields, like
// graphql.GatherIO) driving one state machine.

// previewSrc anchors a rendered row to its source file/line (1-based).
type previewSrc struct {
	File string
	Line int
}

// previewLine is one rendered document row plus the source it came from.
type previewLine struct {
	Segs []view.HighlightSegment
	Src  previewSrc
}

// previewOutlineEntry is one sidebar outline row. Depth 0 is a group header
// (Label only); deeper rows render Badge (colored, badgeW-padded) then Tail —
// method+path for OpenAPI, kind-badge+name for GraphQL. Producers precompute
// the strings so the renderer stays mode-agnostic.
type previewOutlineEntry struct {
	Label   string
	LineIdx int
	Depth   int
	Badge   string
	Tail    string
	Color   string
}

// previewReadyMsg delivers the parsed and rendered document from the worker
// goroutine. err is a user-facing message ("" on success); mode/gen/rel guard
// against stale results (the diffReadyMsg pattern, plus the mode tag since
// both preview kinds share one generation counter).
type previewReadyMsg struct {
	mode    mode
	gen     int
	rel     string
	lines   []previewLine
	outline []previewOutlineEntry
	plain   []string
	title   string
	err     string
}

// previewKind is one preview mode's identity: everything the shared engine
// needs that differs between OpenAPI and GraphQL.
type previewKind struct {
	mode        mode
	name        string // status-bar prompt: "@<name>"
	loadingText string
	badgeW      int // outline badge column width (method 7, kind badge 6)
	detectErr   string
	detect      func(rel, content string) bool
	produce     func(m Model, content string) tea.Cmd
}

// previewState is the live preview document — one instance on the Model (the
// two preview modes are mutually exclusive; every exit path clears it). gen
// stays monotonic across clears so in-flight results remain identifiable.
type previewState struct {
	lines     []previewLine
	outline   []previewOutlineEntry
	plain     []string // per-row plain text (search + match overlay)
	corpus    string   // plain rows joined with \n (the search corpus)
	scrollY   int
	cursor    int
	sel       int // outline selection index
	searching bool
	search    string
	focused   int // focused match index
	loading   bool
	gen       int
	title     string
}

// previewDesc is the active mode's descriptor. Only meaningful while
// m.mode is one of the preview modes.
func (m Model) previewDesc() *previewKind {
	if m.mode == modeGraphQL {
		return &graphqlKind
	}
	return &openapiKind
}

// enterPreview switches to the preview (its @exec verb is the only entry
// point) and kicks off the async parse+render. Errors keep the exec bar so
// the user sees what happened.
func (m Model) enterPreview(k *previewKind) (tea.Model, tea.Cmd) {
	if m.openFile == nil {
		m.errText = "no file open"
		return m, nil
	}
	content := m.edit.content()
	if !k.detect(m.openRel, content) {
		m.errText = k.detectErr
		return m, nil
	}
	m = m.clearPreviewState()
	m.edit.clearSelection()
	m.preview.gen++
	m.preview.loading = true
	m.mode = k.mode
	return m, k.produce(m, content)
}

// clearPreviewState drops the rendered document, preserving only gen (it
// stays monotonic so in-flight results remain identifiable as stale).
func (m Model) clearPreviewState() Model {
	m.preview = previewState{gen: m.preview.gen}
	return m
}

// handlePreviewReady lands the async render. A mode, generation, or file
// mismatch means the user Esc'd, re-ran, or switched files — drop it.
func (m Model) handlePreviewReady(msg previewReadyMsg) (tea.Model, tea.Cmd) {
	if msg.mode != m.mode || msg.gen != m.preview.gen || msg.rel != m.openRel {
		return m, nil
	}
	if msg.err != "" {
		m.errText = msg.err
		m.mode = modeEdit
		return m.clearPreviewState(), nil
	}
	m.preview.loading = false
	m.preview.lines = msg.lines
	m.preview.outline = msg.outline
	m.preview.plain = msg.plain
	m.preview.corpus = strings.Join(msg.plain, "\n")
	m.preview.title = msg.title
	// Land on the rendered row nearest the edit cursor's source line, so
	// entering and Esc'ing straight back roughly round-trips the position.
	m.preview.cursor = previewRowForSource(msg.lines, msg.rel, m.edit.cy+1)
	h := m.contentHeight() + 1
	m.preview.scrollY = anchorScroll(m.preview.cursor, h, len(m.preview.lines))
	return m.syncPreviewSel(), nil
}

// previewRowForSource picks the row whose source (in file rel) sits closest
// to the 1-based line. Rendered order doesn't follow source order (schemas
// inline/group), so this is a plain nearest-scan.
func previewRowForSource(lines []previewLine, rel string, line int) int {
	best, bestDist := 0, int(^uint(0)>>1)
	for i, l := range lines {
		if l.Src.File != rel {
			continue
		}
		d := l.Src.Line - line
		if d < 0 {
			d = -d
		}
		if d < bestDist {
			best, bestDist = i, d
		}
	}
	return best
}

// previewSrcAt is the source anchor of a row, walking back to the nearest
// preceding row that has one (renderer rows always do, but stay safe).
func (m Model) previewSrcAt(idx int) previewSrc {
	for i := min(idx, len(m.preview.lines)-1); i >= 0; i-- {
		if src := m.preview.lines[i].Src; src.File != "" && src.Line > 0 {
			return src
		}
	}
	return previewSrc{}
}

// previewMatches is the live match set of the current query over the rendered
// document's plain text.
func (m Model) previewMatches() []view.SearchMatch {
	return m.previewMC.get(m.preview.corpus, m.preview.search)
}

// movePreviewCursor moves the document cursor by dy rows; the viewport
// follows via fileViewportTop at render time.
func (m Model) movePreviewCursor(dy int) Model {
	if len(m.preview.lines) == 0 {
		return m // still loading
	}
	m.preview.cursor = input.Clamp(m.preview.cursor+dy, 0, len(m.preview.lines)-1)
	return m.syncPreviewSel()
}
