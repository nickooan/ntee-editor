package app

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/filetree"
	"github.com/nickooan/ntee-editor/internal/openapi"
)

// openapiReadyMsg delivers the parsed and rendered document from the worker
// goroutine. err is a user-facing message ("" on success); gen/rel guard
// against stale results (the diffReadyMsg pattern).
type openapiReadyMsg struct {
	gen     int
	rel     string
	lines   []openapi.Line
	outline []openapi.OutlineEntry
	plain   []string
	title   string
	err     string
}

// enterOpenAPI switches to the OpenAPI preview ("openapi" in the @exec bar,
// the mode's only entry point) and kicks off the async parse+render. The
// current file must detect as an OpenAPI v3 spec; errors keep the exec bar so
// the user sees what happened.
func (m Model) enterOpenAPI() (tea.Model, tea.Cmd) {
	if m.openFile == nil {
		m.errText = "no file open"
		return m, nil
	}
	content := m.edit.content()
	if !openapi.Detect(content) {
		m.errText = "invalid openapi yml (missing openapi: 3.x)"
		return m, nil
	}
	m = m.clearOpenAPIState()
	m.edit.clearSelection()
	m.openapiGen++
	m.openapiLoading = true
	m.mode = modeOpenAPI
	return m, m.renderOpenAPICmd(content)
}

// clearOpenAPIState drops the rendered document and resets every openapi
// field except openapiGen, which stays monotonic so in-flight results remain
// identifiable as stale.
func (m Model) clearOpenAPIState() Model {
	m.openapiLines, m.openapiOutline, m.openapiPlain = nil, nil, nil
	m.openapiCorpus = ""
	m.openapiScrollY, m.openapiCursor, m.openapiSel = 0, 0, 0
	m.openapiSearching, m.openapiSearch, m.openapiFocused = false, "", 0
	m.openapiLoading = false
	m.openapiTitle = ""
	return m
}

// renderOpenAPICmd parses the snapshot and renders the document off the UI
// goroutine. Cross-file $refs load lazily during rendering through a reader
// jailed to the project root (filetree.ReadViewFile).
func (m Model) renderOpenAPICmd(content string) tea.Cmd {
	gen, rel, root := m.openapiGen, m.openRel, m.root
	width := max(40, m.width-m.sidebarWidth()-4)
	return func() tea.Msg {
		msg := openapiReadyMsg{gen: gen, rel: rel}
		doc, err := openapi.Parse([]byte(content))
		if err != nil {
			msg.err = "invalid openapi yml: " + firstErrLine(err)
			return msg
		}
		doc.File = rel
		r := openapi.NewResolver(func(refRel string) ([]byte, error) {
			f, ok := filetree.ReadViewFile(root, refRel)
			if !ok {
				return nil, fmt.Errorf("cannot read %s", refRel)
			}
			if f.Binary {
				return nil, fmt.Errorf("%s is binary", refRel)
			}
			return []byte(f.Content), nil
		})
		msg.lines, msg.outline = openapi.RenderDocument(doc, r, width)
		msg.plain = openapi.Plain(msg.lines)
		msg.title = doc.Info.Title
		if doc.Info.Version != "" {
			msg.title = strings.TrimSpace(msg.title + "  v" + doc.Info.Version)
		}
		return msg
	}
}

// handleOpenAPIReady lands the async render. A generation, file, or mode
// mismatch means the user Esc'd, re-ran, or switched files — drop it.
func (m Model) handleOpenAPIReady(msg openapiReadyMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.openapiGen || msg.rel != m.openRel || m.mode != modeOpenAPI {
		return m, nil
	}
	if msg.err != "" {
		m.errText = msg.err
		m.mode = modeEdit
		return m.clearOpenAPIState(), nil
	}
	m.openapiLoading = false
	m.openapiLines = msg.lines
	m.openapiOutline = msg.outline
	m.openapiPlain = msg.plain
	m.openapiCorpus = strings.Join(msg.plain, "\n")
	m.openapiTitle = msg.title
	// Land on the rendered row nearest the edit cursor's source line, so
	// entering and Esc'ing straight back roughly round-trips the position.
	m.openapiCursor = openapiRowForSource(msg.lines, msg.rel, m.edit.cy+1)
	h := m.contentHeight() + 1
	m.openapiScrollY = anchorScroll(m.openapiCursor, h, len(m.openapiLines))
	return m.syncOpenAPISel(), nil
}

// openapiRowForSource picks the row whose source (in file rel) sits closest
// to the 1-based line. Rendered order doesn't follow source order (component
// schemas inline under every use), so this is a plain nearest-scan.
func openapiRowForSource(lines []openapi.Line, rel string, line int) int {
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

// openapiSrcAt is the source anchor of a row, walking back to the nearest
// preceding row that has one (renderer rows always do, but stay safe).
func (m Model) openapiSrcAt(idx int) openapi.SourceRef {
	for i := min(idx, len(m.openapiLines)-1); i >= 0; i-- {
		if src := m.openapiLines[i].Src; src.File != "" && src.Line > 0 {
			return src
		}
	}
	return openapi.SourceRef{}
}

// firstErrLine keeps only an error's first line — yaml parse errors can be
// multi-line and the status bar has one row.
func firstErrLine(err error) string {
	s := err.Error()
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}
