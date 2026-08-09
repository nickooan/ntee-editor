package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

const testGraphQLSchema = `type Query {
  user(id: ID!): User
}

type User {
  id: ID!
  name: String
}
`

const testGraphQLExtra = `extend type Query {
  extra: Int
}

extend type User {
  nickname: String
}
`

// graphQLFixture opens a schema file (with a sibling extend file) and enters
// the preview through the @exec bar, delivering the async render synchronously.
func graphQLFixture(t *testing.T) Model {
	t.Helper()
	m, root := newTestModel(t, nil)
	must(t, os.WriteFile(filepath.Join(root, "schema.graphql"), []byte(testGraphQLSchema), 0o644))
	must(t, os.WriteFile(filepath.Join(root, "extra.graphql"), []byte(testGraphQLExtra), 0o644))
	m = m.openFileAt("schema.graphql")

	m = key(m, ctrlKey('e'))
	m = runes(m, "graphql")
	next, cmd := m.Update(keyPress(tea.KeyEnter))
	m = next.(Model)
	if m.mode != modeGraphQL {
		t.Fatalf("graphql should enter modeGraphQL, got %v (err=%q)", m.mode, m.errText)
	}
	if !m.preview.loading || cmd == nil {
		t.Fatal("entry must kick off the async render")
	}
	next, _ = m.Update(cmd())
	m = next.(Model)
	if m.preview.loading || len(m.preview.lines) == 0 {
		t.Fatalf("render did not land: loading=%v lines=%d err=%q", m.preview.loading, len(m.preview.lines), m.errText)
	}
	return m
}

// graphQLRow finds the first rendered row containing sub.
func graphQLRow(t *testing.T, m Model, sub string) int {
	t.Helper()
	for i, l := range m.preview.plain {
		if strings.Contains(l, sub) {
			return i
		}
	}
	t.Fatalf("no rendered row contains %q:\n%s", sub, strings.Join(m.preview.plain, "\n"))
	return -1
}

func TestGraphQLEntryRejectsNonGraphQLFile(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = m.openFileAt("main.go")
	m = key(m, ctrlKey('e'))
	m = runes(m, "graphql")
	m = key(m, keyPress(tea.KeyEnter))
	if m.mode != modeExec {
		t.Fatalf("non-graphql file must stay in the exec bar, got %v", m.mode)
	}
	if !strings.Contains(m.errText, "not a graphql file") {
		t.Fatalf("errText = %q", m.errText)
	}
}

// The exec bar stays open on a failed entry, so the alert must be visible in
// the rendered status line itself — the field being set is not enough.
func TestGraphQLErrorVisibleInExecBar(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = m.openFileAt("main.go")
	m = key(m, ctrlKey('e'))
	m = runes(m, "graphql")
	m = key(m, keyPress(tea.KeyEnter))
	if m.mode != modeExec {
		t.Fatalf("mode = %v, want modeExec", m.mode)
	}
	status := ansi.Strip(m.renderStatusLine())
	if !strings.Contains(status, "not a graphql file") {
		t.Fatalf("exec status bar must show the alert, got %q", status)
	}
}

// A graphql file that fails to parse still enters the preview: the parse
// error renders as an inline row (only an all-broken schema falls back).
func TestGraphQLEntryBrokenFileFallsBack(t *testing.T) {
	m, root := newTestModel(t, nil)
	must(t, os.WriteFile(filepath.Join(root, "broken.graphql"), []byte("type {\n"), 0o644))
	m = m.openFileAt("broken.graphql")
	m = key(m, ctrlKey('e'))
	m = runes(m, "graphql")
	next, cmd := m.Update(keyPress(tea.KeyEnter))
	m = next.(Model)
	if m.mode != modeGraphQL || cmd == nil {
		t.Fatalf("detect passes on extension, so entry must start the async render (mode=%v)", m.mode)
	}
	next, _ = m.Update(cmd())
	m = next.(Model)
	if m.mode != modeEdit {
		t.Fatalf("an all-broken schema must fall back to edit mode, got %v", m.mode)
	}
	if !strings.Contains(m.errText, "invalid graphql schema") {
		t.Fatalf("errText = %q", m.errText)
	}
}

func TestGraphQLNotReachableFromCommandBar(t *testing.T) {
	m, root := newTestModel(t, nil)
	must(t, os.WriteFile(filepath.Join(root, "schema.graphql"), []byte(testGraphQLSchema), 0o644))
	m = m.openFileAt("schema.graphql")
	m = m.enterCommand()
	m = runes(m, "graphql")
	m = key(m, keyPress(tea.KeyEnter))
	if m.mode == modeGraphQL {
		t.Fatal("graphql must not be reachable from the : bar")
	}
	if !strings.Contains(m.errText, "unknown command") {
		t.Fatalf("errText = %q", m.errText)
	}
}

func TestGraphQLAliasGql(t *testing.T) {
	m, root := newTestModel(t, nil)
	must(t, os.WriteFile(filepath.Join(root, "schema.graphql"), []byte(testGraphQLSchema), 0o644))
	m = m.openFileAt("schema.graphql")
	m = key(m, ctrlKey('e'))
	m = runes(m, "gql")
	next, _ := m.Update(keyPress(tea.KeyEnter))
	if got := next.(Model).mode; got != modeGraphQL {
		t.Fatalf("gql alias should enter modeGraphQL, got %v", got)
	}
}

func TestGraphQLRenderAndOutline(t *testing.T) {
	m := graphQLFixture(t)
	graphQLRow(t, m, "user(id: ID!): User")
	graphQLRow(t, m, "extra: Int") // merged from the sibling file
	graphQLRow(t, m, "type User {")
	graphQLRow(t, m, "nickname")
	graphQLRow(t, m, "(+1 extend)")

	var labels []string
	for _, e := range m.preview.outline {
		labels = append(labels, e.Label)
	}
	if got := strings.Join(labels, " "); got != "Queries user extra Types User" {
		t.Fatalf("outline = %q", got)
	}
}

func TestGraphQLReadOnly(t *testing.T) {
	m := graphQLFixture(t)
	before := m.edit.content()
	for _, msg := range []tea.KeyPressMsg{
		typeRune('x'), keyPress(tea.KeyBackspace), keyPress(tea.KeyTab),
		ctrlKey('z'), ctrlKey('a'), keyPress(tea.KeySpace),
	} {
		m = key(m, msg)
	}
	if m.edit.content() != before {
		t.Fatal("keys in graphql mode must not reach the buffer")
	}
	if m.mode != modeGraphQL {
		t.Fatalf("mode drifted to %v", m.mode)
	}
}

func TestGraphQLCursorAndOutlineJump(t *testing.T) {
	m := graphQLFixture(t)
	m.preview.cursor, m.preview.scrollY = 0, 0
	m = key(m, keyPress(tea.KeyDown))
	m = key(m, keyPress(tea.KeyDown))
	if m.preview.cursor != 2 {
		t.Fatalf("cursor = %d, want 2", m.preview.cursor)
	}

	// Select the "user" entry in the outline and jump to it.
	m.preview.sel = 0
	m = key(m, shiftKey(tea.KeyDown))
	if m.preview.sel != 1 {
		t.Fatalf("outline sel = %d, want 1", m.preview.sel)
	}
	m = key(m, keyPress(tea.KeyEnter))
	if m.preview.cursor != m.preview.outline[1].LineIdx {
		t.Fatalf("cursor = %d, want anchor %d", m.preview.cursor, m.preview.outline[1].LineIdx)
	}
}

func TestGraphQLSearchCycleAndLand(t *testing.T) {
	m := graphQLFixture(t)
	m = key(m, typeRune('/'))
	if !m.preview.searching {
		t.Fatal("/ must open the search bar")
	}
	m = runes(m, "User")
	matches := m.previewMatches()
	if len(matches) < 2 {
		t.Fatalf("matches = %d, want ≥2", len(matches))
	}
	if m.preview.cursor != matches[m.preview.focused].LineIndex {
		t.Fatal("typing must land the cursor on the focused match")
	}

	first := m.preview.focused
	m = key(m, keyPress(tea.KeyDown))
	if m.preview.focused == first {
		t.Fatal("down must advance the focused match")
	}

	landed := m.preview.cursor
	m = key(m, keyPress(tea.KeyEnter))
	if m.preview.searching || m.preview.search != "" {
		t.Fatal("enter must close the search bar")
	}
	if m.preview.cursor != landed {
		t.Fatal("enter must keep the cursor on the landed match")
	}

	// Esc closes the bar first; a second Esc exits the mode.
	m = key(m, typeRune('/'))
	m = runes(m, "x")
	m = key(m, keyPress(tea.KeyEsc))
	if m.preview.searching || m.mode != modeGraphQL {
		t.Fatal("first esc closes only the search bar")
	}
	m = key(m, keyPress(tea.KeyEsc))
	if m.mode != modeEdit {
		t.Fatalf("second esc must exit to edit mode, got %v", m.mode)
	}
}

func TestGraphQLExitSameFileSource(t *testing.T) {
	m := graphQLFixture(t)
	row := graphQLRow(t, m, "user(id: ID!): User")
	m.preview.cursor = row
	src := m.preview.lines[row].Src
	if src.File != "schema.graphql" {
		t.Fatalf("user src = %+v", src)
	}
	m = key(m, keyPress(tea.KeyEsc))
	if m.mode != modeEdit || m.openRel != "schema.graphql" {
		t.Fatalf("esc → mode=%v rel=%q", m.mode, m.openRel)
	}
	if m.edit.cy != src.Line-1 {
		t.Fatalf("cursor line = %d, want %d", m.edit.cy, src.Line-1)
	}
	if !strings.Contains(m.edit.lines[m.edit.cy], "user(id: ID!)") {
		t.Fatalf("landed on %q", m.edit.lines[m.edit.cy])
	}
	if len(m.preview.lines) != 0 {
		t.Fatal("exit must clear the preview state")
	}
}

func TestGraphQLExitCrossFileSource(t *testing.T) {
	m := graphQLFixture(t)
	row := graphQLRow(t, m, "nickname")
	m.preview.cursor = row
	src := m.preview.lines[row].Src
	if src.File != "extra.graphql" {
		t.Fatalf("nickname src = %+v", src)
	}
	m = key(m, keyPress(tea.KeyEsc))
	if m.mode != modeEdit {
		t.Fatalf("esc → mode=%v", m.mode)
	}
	if m.openRel != "extra.graphql" {
		t.Fatalf("esc from cross-file content must open extra.graphql, got %q", m.openRel)
	}
	if m.edit.cy != src.Line-1 {
		t.Fatalf("cursor line = %d, want %d", m.edit.cy, src.Line-1)
	}
	if !strings.Contains(m.edit.lines[m.edit.cy], "nickname") {
		t.Fatalf("landed on %q", m.edit.lines[m.edit.cy])
	}
}

func TestGraphQLWheelScroll(t *testing.T) {
	m := graphQLFixture(t)
	m.preview.cursor, m.preview.scrollY = 0, 0
	m = m.wheelScroll(1)
	if m.preview.cursor != wheelScrollLines {
		t.Fatalf("wheel cursor = %d, want %d", m.preview.cursor, wheelScrollLines)
	}
}

// A .graphqlrc.yml drives the gathered file set with its schema globs.
func TestGraphQLGraphqlrcGather(t *testing.T) {
	m, root := newTestModel(t, nil)
	must(t, os.WriteFile(filepath.Join(root, ".graphqlrc.yml"), []byte(`schema: "sdl/**/*.graphql"`), 0o644))
	must(t, os.MkdirAll(filepath.Join(root, "sdl", "nested"), 0o755))
	must(t, os.WriteFile(filepath.Join(root, "sdl", "a.graphql"), []byte("type Query { a: Int }\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(root, "sdl", "nested", "b.graphql"), []byte("extend type Query { b: Int }\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(root, "decoy.graphql"), []byte("type Decoy { x: Int }\n"), 0o644))
	m = m.openFileAt("sdl/a.graphql")

	m = key(m, ctrlKey('e'))
	m = runes(m, "graphql")
	next, cmd := m.Update(keyPress(tea.KeyEnter))
	m = next.(Model)
	next, _ = m.Update(cmd())
	m = next.(Model)
	if len(m.preview.lines) == 0 {
		t.Fatalf("render did not land: err=%q", m.errText)
	}
	graphQLRow(t, m, "via .graphqlrc.yml")
	graphQLRow(t, m, "b: Int") // merged via the glob
	for _, l := range m.preview.plain {
		if strings.Contains(l, "Decoy") {
			t.Fatalf("decoy outside the glob must not merge: %q", l)
		}
	}
}
