package app

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/filetree"
	"github.com/nickooan/ntee-editor/internal/graphql"
)

// graphqlReadyMsg delivers the gathered, parsed, and rendered schema from the
// worker goroutine. err is a user-facing message ("" on success); gen/rel
// guard against stale results (the diffReadyMsg pattern).
type graphqlReadyMsg struct {
	gen     int
	rel     string
	lines   []graphql.Line
	outline []graphql.OutlineEntry
	plain   []string
	title   string
	err     string
}

// enterGraphQL switches to the GraphQL schema preview ("graphql" in the @exec
// bar, the mode's only entry point) and kicks off the async gather+parse+
// render. The mode is only enterable while editing a GraphQL file — any other
// file type raises an alert and keeps the exec bar so the user sees it.
func (m Model) enterGraphQL() (tea.Model, tea.Cmd) {
	if m.openFile == nil {
		m.errText = "no file open"
		return m, nil
	}
	if !graphql.Detect(m.openRel) {
		m.errText = "not a graphql file (.graphql/.gql/.graphqls)"
		return m, nil
	}
	m = m.clearGraphQLState()
	m.edit.clearSelection()
	m.graphqlGen++
	m.graphqlLoading = true
	m.mode = modeGraphQL
	return m, m.renderGraphQLCmd(m.edit.content())
}

// clearGraphQLState drops the rendered schema and resets every graphql field
// except graphqlGen, which stays monotonic so in-flight results remain
// identifiable as stale.
func (m Model) clearGraphQLState() Model {
	m.graphqlLines, m.graphqlOutline, m.graphqlPlain = nil, nil, nil
	m.graphqlCorpus = ""
	m.graphqlScrollY, m.graphqlCursor, m.graphqlSel = 0, 0, 0
	m.graphqlSearching, m.graphqlSearch, m.graphqlFocused = false, "", 0
	m.graphqlLoading = false
	m.graphqlTitle = ""
	return m
}

// renderGraphQLCmd gathers the schema file set, parses, and renders off the
// UI goroutine. All file access goes through readers jailed to the project
// root; the open file itself renders from the captured buffer snapshot so
// unsaved edits show (sibling files come from disk — the same accepted
// limitation the OpenAPI preview has for $ref targets).
func (m Model) renderGraphQLCmd(content string) tea.Cmd {
	gen, rel, root, gi := m.graphqlGen, m.openRel, m.root, m.gitignore
	width := max(40, m.width-m.sidebarWidth()-4)
	return func() tea.Msg {
		msg := graphqlReadyMsg{gen: gen, rel: rel}
		io := graphql.GatherIO{
			ReadFile: func(refRel string) ([]byte, error) {
				f, ok := filetree.ReadViewFile(root, refRel)
				if !ok {
					return nil, fmt.Errorf("cannot read %s", refRel)
				}
				if f.Binary {
					return nil, fmt.Errorf("%s is binary", refRel)
				}
				return []byte(f.Content), nil
			},
			ListDir: func(dirRel string) ([]string, error) {
				names, ok := filetree.ListDirFiles(root, dirRel)
				if !ok {
					return nil, fmt.Errorf("cannot list %s", dirRel)
				}
				return names, nil
			},
			// Only invoked when a graphqlrc declares globs: the existing
			// gitignore-aware recursive walk, with the corpus's file cap.
			ListAll: func() []string {
				files, _, _ := filetree.BuildAllEntries(root, nil, gi, 20000)
				return files
			},
		}
		files, cfg, notes := graphql.Gather(rel, io)
		if len(files) == 0 {
			msg.err = "no graphql schema files found"
			return msg
		}
		sources := make([]graphql.NamedSource, 0, len(files))
		for _, f := range files {
			if f == rel {
				sources = append(sources, graphql.NamedSource{Rel: f, Content: content})
				continue
			}
			data, err := io.ReadFile(f)
			if err != nil {
				notes = append(notes, "cannot read "+f)
				continue
			}
			sources = append(sources, graphql.NamedSource{Rel: f, Content: string(data)})
		}
		s := graphql.ParseFiles(sources)
		if len(s.Defs) == 0 && len(s.Directives) == 0 && len(s.Errors) > 0 {
			msg.err = "invalid graphql schema: " + s.Errors[0].Msg
			return msg
		}
		s.Config, s.Notes = cfg, notes
		msg.lines, msg.outline = graphql.RenderSchema(s, width)
		msg.plain = graphql.Plain(msg.lines)
		msg.title = fmt.Sprintf("%d types · %d files", len(s.Defs), len(s.Files))
		return msg
	}
}

// handleGraphQLReady lands the async render. A generation, file, or mode
// mismatch means the user Esc'd, re-ran, or switched files — drop it.
func (m Model) handleGraphQLReady(msg graphqlReadyMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.graphqlGen || msg.rel != m.openRel || m.mode != modeGraphQL {
		return m, nil
	}
	if msg.err != "" {
		m.errText = msg.err
		m.mode = modeEdit
		return m.clearGraphQLState(), nil
	}
	m.graphqlLoading = false
	m.graphqlLines = msg.lines
	m.graphqlOutline = msg.outline
	m.graphqlPlain = msg.plain
	m.graphqlCorpus = strings.Join(msg.plain, "\n")
	m.graphqlTitle = msg.title
	// Land on the rendered row nearest the edit cursor's source line, so
	// entering and Esc'ing straight back roughly round-trips the position.
	m.graphqlCursor = graphqlRowForSource(msg.lines, msg.rel, m.edit.cy+1)
	h := m.contentHeight() + 1
	m.graphqlScrollY = anchorScroll(m.graphqlCursor, h, len(m.graphqlLines))
	return m.syncGraphQLSel(), nil
}

// graphqlRowForSource picks the row whose source (in file rel) sits closest
// to the 1-based line. Rendered order doesn't follow source order (types
// group by kind), so this is a plain nearest-scan.
func graphqlRowForSource(lines []graphql.Line, rel string, line int) int {
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

// graphqlSrcAt is the source anchor of a row, walking back to the nearest
// preceding row that has one (renderer rows always do, but stay safe).
func (m Model) graphqlSrcAt(idx int) graphql.SourceRef {
	for i := min(idx, len(m.graphqlLines)-1); i >= 0; i-- {
		if src := m.graphqlLines[i].Src; src.File != "" && src.Line > 0 {
			return src
		}
	}
	return graphql.SourceRef{}
}
