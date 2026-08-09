package app

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/filetree"
	"github.com/nickooan/ntee-editor/internal/openapi"
)

// openapiKind plugs the OpenAPI document preview into the shared preview
// engine (preview_mode.go). Detection is content-based: any file whose buffer
// declares openapi: 3.x qualifies, whatever its name.
var openapiKind = previewKind{
	mode:        modeOpenAPI,
	name:        "openapi",
	loadingText: "rendering openapi document…",
	badgeW:      7, // "OPTIONS" is the widest method
	detectErr:   "invalid openapi yml (missing openapi: 3.x)",
	detect:      func(_, content string) bool { return openapi.Detect(content) },
	produce:     renderOpenAPICmd,
}

// renderOpenAPICmd parses the snapshot and renders the document off the UI
// goroutine. Cross-file $refs load lazily during rendering through a reader
// jailed to the project root (filetree.ReadViewFile).
func renderOpenAPICmd(m Model, content string) tea.Cmd {
	gen, rel, root := m.preview.gen, m.openRel, m.root
	width := max(40, m.width-m.sidebarWidth()-4)
	return func() tea.Msg {
		msg := previewReadyMsg{mode: modeOpenAPI, gen: gen, rel: rel}
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
		lines, outline := openapi.RenderDocument(doc, r, width)
		msg.lines = make([]previewLine, len(lines))
		for i, l := range lines {
			msg.lines[i] = previewLine{Segs: l.Segs, Src: previewSrc(l.Src)}
		}
		msg.outline = make([]previewOutlineEntry, len(outline))
		for i, e := range outline {
			msg.outline[i] = previewOutlineEntry{
				Label:   e.Label,
				LineIdx: e.LineIdx,
				Depth:   e.Depth,
				Badge:   e.Method,
				Tail:    e.Path,
				Color:   openapi.MethodColor(e.Method),
			}
		}
		msg.plain = openapi.Plain(lines)
		msg.title = doc.Info.Title
		if doc.Info.Version != "" {
			msg.title = strings.TrimSpace(msg.title + "  v" + doc.Info.Version)
		}
		return msg
	}
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
