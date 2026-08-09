package app

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/filetree"
	"github.com/nickooan/ntee-editor/internal/graphql"
)

// graphqlKind plugs the GraphQL schema preview into the shared preview
// engine (preview_mode.go). Detection is filename-based: only .graphql/.gql/
// .graphqls files qualify.
var graphqlKind = previewKind{
	mode:        modeGraphQL,
	name:        "graphql",
	loadingText: "rendering graphql schema…",
	badgeW:      6, // KindBadge column
	detectErr:   "not a graphql file (.graphql/.gql/.graphqls)",
	detect:      func(rel, _ string) bool { return graphql.Detect(rel) },
	produce:     renderGraphQLCmd,
}

// renderGraphQLCmd gathers the schema file set, parses, and renders off the
// UI goroutine. All file access goes through readers jailed to the project
// root; the open file itself renders from the captured buffer snapshot so
// unsaved edits show (sibling files come from disk — the same accepted
// limitation the OpenAPI preview has for $ref targets).
func renderGraphQLCmd(m Model, content string) tea.Cmd {
	gen, rel, root, gi := m.preview.gen, m.openRel, m.root, m.gitignore
	width := max(40, m.width-m.sidebarWidth()-4)
	return func() tea.Msg {
		msg := previewReadyMsg{mode: modeGraphQL, gen: gen, rel: rel}
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
		lines, outline := graphql.RenderSchema(s, width)
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
				Badge:   graphql.KindBadge(e.Kind),
				Tail:    e.Label,
				Color:   graphql.KindColor(e.Kind),
			}
		}
		msg.plain = graphql.Plain(lines)
		msg.title = fmt.Sprintf("%d types · %d files", len(s.Defs), len(s.Files))
		return msg
	}
}
