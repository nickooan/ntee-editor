package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nickooan/ntee-editor/internal/lsp"
)

// jumpFixture builds a project with a path reference and cross-file Go
// definitions, opens main.go in edit mode, and returns the model.
func jumpFixture(t *testing.T) Model {
	t.Helper()
	m, root := newTestModel(t, nil)
	must(t, os.WriteFile(filepath.Join(root, "helper.go"), []byte(
		"package main\n\nfunc helperFunc() int {\n\treturn 1\n}\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(root, "main.go"), []byte(
		"package main\n\n// see lib/util.ts\nfunc main() {\n\thelperFunc()\n\tlocalThing()\n}\n\nfunc localThing() {}\n"), 0o644))
	m = m.openFileAt("main.go")
	if m.mode != modeEdit {
		t.Fatal("fixture should be in edit mode")
	}
	return m
}

func ctrl(m Model, k tea.KeyType) Model { return key(m, tea.KeyMsg{Type: k}) }

func TestJumpToPathUnderCursorAndBack(t *testing.T) {
	m := jumpFixture(t)
	// Cursor on "lib/util.ts" in the comment line (line 2).
	m.edit.cy, m.edit.cx = 2, 8
	m = ctrl(m, tea.KeyCtrlJ)
	if m.openRel != "lib/util.ts" || m.mode != modeEdit {
		t.Fatalf("path jump failed: open=%q err=%q", m.openRel, m.errText)
	}
	if len(m.jumpStack) != 1 {
		t.Fatalf("stack depth = %d", len(m.jumpStack))
	}
	m = ctrl(m, tea.KeyCtrlO)
	if m.openRel != "main.go" || m.edit.cy != 2 || m.edit.cx != 8 {
		t.Fatalf("jump back mismatch: open=%q cy=%d cx=%d", m.openRel, m.edit.cy, m.edit.cx)
	}
	if len(m.jumpStack) != 0 {
		t.Fatal("stack should be empty after back")
	}
	m = ctrl(m, tea.KeyCtrlO)
	if m.errText != "no jump to return to" {
		t.Fatalf("empty-stack error missing: %q", m.errText)
	}
}

func TestJumpGuards(t *testing.T) {
	m := jumpFixture(t)

	// Unresolvable token errors and leaves no stack residue.
	m.edit.cy, m.edit.cx = 0, 0 // "package" — keyword line, no definition
	m = ctrl(m, tea.KeyCtrlJ)
	if m.errText == "" || len(m.jumpStack) != 0 {
		t.Fatalf("unresolvable jump: err=%q stack=%d", m.errText, len(m.jumpStack))
	}
}

// Jumping with unsaved edits must not block: the dirty buffer is stashed as a
// draft (red tab), and jumping back restores it.
func TestJumpWithUnsavedChangesStashesDraft(t *testing.T) {
	m := jumpFixture(t)
	m = runes(m, "x") // dirty: line 0 becomes "xpackage main"
	if !m.edit.dirty {
		t.Fatal("fixture should be dirty")
	}

	m.edit.cy, m.edit.cx = 2, 8 // on "lib/util.ts" in the comment
	m = ctrl(m, tea.KeyCtrlJ)
	if m.openRel != "lib/util.ts" || m.errText != "" {
		t.Fatalf("dirty buffer must not block the jump: open=%q err=%q", m.openRel, m.errText)
	}
	if !m.draftSet["main.go"] {
		t.Fatal("the unsaved origin buffer should be stashed as a draft")
	}
	if m.edit.dirty {
		t.Fatal("the jump target opens clean")
	}

	// Jumping back restores the draft: content, dirty flag, red-tab marker.
	m = ctrl(m, tea.KeyCtrlO)
	if m.openRel != "main.go" || !m.edit.dirty {
		t.Fatalf("jump back should restore the drafted buffer: open=%q dirty=%v", m.openRel, m.edit.dirty)
	}
	if !strings.HasPrefix(m.edit.lines[0], "xpackage") {
		t.Fatalf("draft content lost: %q", m.edit.lines[0])
	}
	// Undo still walks back to the on-disk baseline.
	m = ctrl(m, tea.KeyCtrlZ)
	if m.edit.dirty || m.edit.lines[0] != "package main" {
		t.Fatalf("undo should reach the disk baseline: %q dirty=%v", m.edit.lines[0], m.edit.dirty)
	}
}

func TestJumpStackClearedOnEscAndOpen(t *testing.T) {
	m := jumpFixture(t)
	m.edit.cy, m.edit.cx = 2, 8
	m = ctrl(m, tea.KeyCtrlJ) // → lib/util.ts, stack 1
	if len(m.jumpStack) != 1 {
		t.Fatalf("setup: stack=%d err=%q", len(m.jumpStack), m.errText)
	}

	// Esc out of edit mode ends the trail.
	m = ctrl(m, tea.KeyEsc)
	if len(m.jumpStack) != 0 {
		t.Fatal("esc should clear the jump stack")
	}

	// A deliberate open also starts a fresh trail.
	m = jumpFixture(t)
	m.edit.cy, m.edit.cx = 2, 8
	m = ctrl(m, tea.KeyCtrlJ)
	m = m.openFileAt("main.go")
	if len(m.jumpStack) != 0 {
		t.Fatal("deliberate open should clear the jump stack")
	}
}

func TestEnterSearchPopulatesHighlights(t *testing.T) {
	m := jumpFixture(t)
	m = ctrl(m, tea.KeyCtrlF)
	if m.mode != modeSearch {
		t.Fatal("expected search mode")
	}
	if m.searchHl == nil {
		t.Fatal("go file search should be highlighted")
	}
	lineCount := len(strings.Split(m.searchContent, "\n"))
	if len(m.searchHl) != lineCount {
		t.Fatalf("searchHl rows %d != content lines %d", len(m.searchHl), lineCount)
	}

	// Unknown extension falls back to plain.
	root := m.root
	must(t, os.WriteFile(filepath.Join(root, "notes.xyzunknown"), []byte("plain text\n"), 0o644))
	m2 := m
	m2 = m2.openFileAt("notes.xyzunknown")
	m2 = ctrl(m2, tea.KeyCtrlF)
	if m2.searchHl != nil {
		t.Fatal("unknown extension should search plain")
	}
}

func TestPickerEscCancelsWithoutMoving(t *testing.T) {
	m, client := newLSPTestModel(t)
	m = m.openFileAt("main.go")
	root := m.root
	m.edit.cy, m.edit.cx = 3, 0 // "}" — not a candidate line

	// Two LSP hits open the picker.
	client.locs = []lsp.Location{
		{URI: lsp.PathToURI(filepath.Join(root, "lib", "util.ts")), Range: lsp.Range{Start: lsp.Position{Line: 0, Character: 0}}},
		{URI: lsp.PathToURI(filepath.Join(root, "main.go")), Range: lsp.Range{Start: lsp.Position{Line: 2, Character: 0}}},
	}
	next, _ := m.handleDefinition(definitionMsg{token: "x", locs: client.locs})
	m = next.(Model)
	if !m.defPickOpen {
		t.Fatalf("picker should open: err=%q", m.errText)
	}
	m = ctrl(m, tea.KeyEsc)
	if m.defPickOpen || m.openRel != "main.go" || m.edit.cy != 3 {
		t.Fatalf("esc should cancel in place: open=%q cy=%d", m.openRel, m.edit.cy)
	}
	if len(m.jumpStack) != 0 {
		t.Fatal("cancelled pick must leave no stack residue")
	}
}

func TestPickerPreviewFollowsSelection(t *testing.T) {
	m, client := newLSPTestModel(t)
	m = m.openFileAt("main.go")
	root := m.root
	m.edit.cy, m.edit.cx = 3, 0 // "}" — not a candidate line

	client.locs = []lsp.Location{
		{URI: lsp.PathToURI(filepath.Join(root, "main.go")), Range: lsp.Range{Start: lsp.Position{Line: 2, Character: 0}}},
		{URI: lsp.PathToURI(filepath.Join(root, "lib", "util.ts")), Range: lsp.Range{Start: lsp.Position{Line: 0, Character: 0}}},
	}
	next, _ := m.handleDefinition(definitionMsg{token: "x", locs: client.locs})
	m = next.(Model)
	if !m.defPickOpen || len(m.defPickItems) != 2 {
		t.Fatalf("picker should open with 2 hits: open=%v n=%d err=%q", m.defPickOpen, len(m.defPickItems), m.errText)
	}
	if m.defPickPrevRel != m.defPickItems[0].rel {
		t.Fatalf("preview should load the first candidate: %q", m.defPickPrevRel)
	}
	if len(m.defPickPrevLines) == 0 || m.defPickPrevHl == nil {
		t.Fatal("preview lines/highlight missing")
	}

	first := m.defPickPrevRel
	m = ctrl(m, tea.KeyDown)
	if m.defPickPrevRel == first {
		t.Fatalf("preview should follow selection to the other file: %q", m.defPickPrevRel)
	}

	// The rendered overlay includes the second candidate's code and the divider.
	out := m.renderDefPickOverlay(100, 30)
	if !strings.Contains(out, "héllo") {
		t.Fatal("preview should show the second candidate's line")
	}
	if !strings.Contains(out, "─") {
		t.Fatal("divider missing")
	}
}

// lspJumpModel opens main.go with custom content against the stub server.
func lspJumpModel(t *testing.T, content string) (Model, *stubClient) {
	t.Helper()
	m, client := newLSPTestModel(t)
	must(t, os.WriteFile(filepath.Join(m.root, "main.go"), []byte(content), 0o644))
	m = m.openFileAt("main.go")
	if m.mode != modeEdit {
		t.Fatal("expected edit mode")
	}
	return m, client
}

// ctrlJ presses Ctrl+J and synchronously drains the chained async
// definition/references round-trips.
func ctrlJ(t *testing.T, m Model) Model {
	t.Helper()
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	m = next.(Model)
	for cmd != nil {
		next, cmd = m.Update(cmd())
		m = next.(Model)
	}
	return m
}

func selfLoc(m Model, line int) []lsp.Location {
	return []lsp.Location{{
		URI:   lsp.PathToURI(filepath.Join(m.root, "main.go")),
		Range: lsp.Range{Start: lsp.Position{Line: line, Character: 0}},
	}}
}

func TestIdentifierAt(t *testing.T) {
	line := []rune("\tx := helper(y)")
	if _, _, ok := identifierAt(line, 0); ok {
		t.Fatal("tab is not an identifier")
	}
	if s, e, ok := identifierAt(line, 8); !ok || string(line[s:e]) != "helper" {
		t.Fatalf("mid-word: ok=%v", ok)
	}
	if s, e, ok := identifierAt(line, 12); !ok || string(line[s:e]) != "helper" {
		t.Fatalf("just past word: ok=%v", ok)
	}
	if _, _, ok := identifierAt(line, 4); ok { // on '=' of ":="
		t.Fatal("punctuation between words is not an identifier")
	}
	if _, _, ok := identifierAt(nil, 0); ok {
		t.Fatal("empty line has no identifier")
	}
}

func TestIdentifierCols(t *testing.T) {
	line := []rune("\tfoo := bar(baz, 42)")
	// Cursor between foo and bar: both at distance 2, tie goes left; 42 is a
	// number literal, not a name.
	cols := identifierCols(line, 6, 4)
	want := []int{1, 8, 12}
	if len(cols) != len(want) {
		t.Fatalf("cols = %v, want %v", cols, want)
	}
	for i := range want {
		if cols[i] != want[i] {
			t.Fatalf("cols = %v, want %v", cols, want)
		}
	}
	if got := identifierCols(line, 0, 2); len(got) != 2 {
		t.Fatalf("cap: %v", got)
	}
	if got := identifierCols([]rune("   "), 1, 4); len(got) != 0 {
		t.Fatalf("blank line: %v", got)
	}
}

// Cursor on leading whitespace snaps to the nearest identifier and jumps.
func TestJumpSnapToNearestIdentifier(t *testing.T) {
	m, client := lspJumpModel(t, "package main\n\nfunc helper() {}\n\nfunc main() {\n\thelper()\n}\n")
	client.defQueue = [][]lsp.Location{selfLoc(m, 2)}
	m.edit.cy, m.edit.cx = 5, 0 // on the tab before helper()

	m = ctrlJ(t, m)
	if len(client.defCols) != 1 || client.defCols[0] != 1 {
		t.Fatalf("definition should be queried at the snapped column: %v", client.defCols)
	}
	if m.edit.cy != 2 || m.errText != "" {
		t.Fatalf("should jump to the definition: cy=%d err=%q", m.edit.cy, m.errText)
	}
}

// An empty answer at the nearest identifier retries the next-nearest.
func TestJumpRetriesNextIdentifier(t *testing.T) {
	m, client := lspJumpModel(t, "package main\n\nfunc helper() {}\n\nfunc main() {\n\tx := helper()\n\t_ = x\n}\n")
	client.defQueue = [][]lsp.Location{nil, selfLoc(m, 2)} // x: nothing; helper: hit
	m.edit.cy, m.edit.cx = 5, 0

	m = ctrlJ(t, m)
	if len(client.defCols) != 2 || client.defCols[0] != 1 || client.defCols[1] != 6 {
		t.Fatalf("expected a retry at the next identifier: %v", client.defCols)
	}
	if m.edit.cy != 2 || m.errText != "" {
		t.Fatalf("retry should land the jump: cy=%d err=%q", m.edit.cy, m.errText)
	}
}

// A snapped definition resolving to the cursor's own line pivots to
// references, and the references query reuses the snapped column — not the
// cursor column, which sits on punctuation.
func TestJumpDefLinePivotsToReferencesFromSnap(t *testing.T) {
	m, client := lspJumpModel(t, "package main\n\nfunc helper() {}\n\nfunc main() {\n\thelper()\n}\n")
	client.defQueue = [][]lsp.Location{selfLoc(m, 2)} // resolves to its own line
	client.refLocs = selfLoc(m, 5)
	m.edit.cy, m.edit.cx = 2, 15 // on '}' of "func helper() {}"

	m = ctrlJ(t, m)
	// Nearest identifier to col 15 is helper (cols 5-11).
	if len(client.defCols) != 1 || client.defCols[0] != 5 {
		t.Fatalf("definition col: %v", client.defCols)
	}
	if len(client.refCols) != 1 || client.refCols[0] != 5 {
		t.Fatalf("references should be queried at the snapped column: %v", client.refCols)
	}
	if m.edit.cy != 5 || m.errText != "" {
		t.Fatalf("should land on the single reference: cy=%d err=%q", m.edit.cy, m.errText)
	}
}

// The Kotlin case: the cursor sits directly on a declaration, but the server
// returns no definition (kotlin-language-server does not self-resolve a
// declaration). The jump pivots to references anyway, queried at the
// identifier column, and lands on the single usage.
func TestJumpEmptyDefinitionOnIdentifierPivotsToReferences(t *testing.T) {
	m, client := lspJumpModel(t,
		"package main\n\nvar bookingReference = 1\n\nfunc use() { _ = bookingReference }\n")
	// defQueue empty → Definition returns nothing, as kotlin-language-server
	// does on a declaration. References points at the usage on line 4.
	client.refLocs = selfLoc(m, 4)
	m.edit.cy, m.edit.cx = 2, 4 // on `bookingReference` in `var bookingReference = 1`

	m = ctrlJ(t, m)
	if len(client.defCols) != 1 || client.defCols[0] != 4 {
		t.Fatalf("definition should be queried once at the cursor: %v", client.defCols)
	}
	if len(client.refCols) != 1 || client.refCols[0] != 4 {
		t.Fatalf("references should be queried at the identifier column: %v", client.refCols)
	}
	if m.edit.cy != 4 || m.errText != "" {
		t.Fatalf("should land on the single reference: cy=%d err=%q", m.edit.cy, m.errText)
	}
}

// When the references pivot also comes back empty, the message is the
// references miss — proving the pivot ran rather than the old definition
// dead-end.
func TestJumpEmptyDefinitionEmptyReferencesReportsReferencesMiss(t *testing.T) {
	m, client := lspJumpModel(t, "package main\n\nvar bookingReference = 1\n")
	client.refLocs = nil // no references either
	m.edit.cy, m.edit.cx = 2, 4 // on `bookingReference`

	m = ctrlJ(t, m)
	if len(client.refCols) != 1 {
		t.Fatalf("the pivot should still query references: %v", client.refCols)
	}
	if m.errText != "no references found: bookingReference" {
		t.Fatalf("should report a references miss, not a definition miss: %q", m.errText)
	}
}

// A cursor on no symbol jumps to a file referenced (quoted) on the line.
func TestJumpLinePathRedirect(t *testing.T) {
	m, client := lspJumpModel(t, "package main\n\n// see \"lib/util.ts\" for details\n")
	m.edit.cy, m.edit.cx = 2, 0 // on '/' of the comment marker

	m = ctrlJ(t, m)
	if m.openRel != "lib/util.ts" || m.errText != "" {
		t.Fatalf("should open the referenced file: open=%q err=%q", m.openRel, m.errText)
	}
	if len(client.defCols) != 0 {
		t.Fatal("a resolvable line path must not query the language server")
	}
}

// Cursor inside a quoted path still opens the file directly (regression: the
// identifier branch must not preempt the path-under-cursor check).
func TestJumpCursorInsideQuotedPath(t *testing.T) {
	m, client := lspJumpModel(t, "package main\n\nvar p = \"lib/util.ts\"\n")
	m.edit.cy, m.edit.cx = 2, 10 // inside "lib/util.ts"

	m = ctrlJ(t, m)
	if m.openRel != "lib/util.ts" || len(client.defCols) != 0 {
		t.Fatalf("cursor in path should open it without LSP: open=%q calls=%v", m.openRel, client.defCols)
	}
}

// tsImportFixture builds lib/app.ts holding an import line plus config.ts at
// the root, and opens lib/app.ts. reg nil → no language server (noop).
func tsImportFixture(t *testing.T, m Model, importLine string) Model {
	t.Helper()
	root := m.root
	must(t, os.WriteFile(filepath.Join(root, "config.ts"), []byte("export const x = 1\n"), 0o644))
	must(t, os.MkdirAll(filepath.Join(root, "lib"), 0o755))
	must(t, os.WriteFile(filepath.Join(root, "lib", "app.ts"), []byte(importLine+"\n"), 0o644))
	m = m.openFileAt("lib/app.ts")
	if m.mode != modeEdit {
		t.Fatal("expected edit mode")
	}
	return m
}

// The reported bug: with no language server, Ctrl+J on an import line must
// redirect to the imported file instead of erroring.
func TestJumpImportLineNoServer(t *testing.T) {
	m, _ := newTestModel(t, nil) // noop registry
	m = tsImportFixture(t, m, `import { clearRuntimeConfigCache, type RuntimeConfig } from "../config.ts"`)
	m.edit.cy, m.edit.cx = 0, 0 // on `import`
	m = ctrlJ(t, m)
	if m.openRel != "config.ts" || m.errText != "" {
		t.Fatalf("import line should redirect: open=%q err=%q", m.openRel, m.errText)
	}
}

// With no server and no path on the line, the error names the extension and
// points at the installer.
func TestJumpNoServerHint(t *testing.T) {
	m, root := newTestModel(t, nil)
	must(t, os.WriteFile(filepath.Join(root, "app.ts"), []byte("const answer = 1\n"), 0o644))
	m = m.openFileAt("app.ts")
	m.edit.cy, m.edit.cx = 0, 6 // on `answer`
	m = ctrlJ(t, m)
	if !strings.Contains(m.errText, "prepare-lsp") || !strings.Contains(m.errText, ".ts") {
		t.Fatalf("error should hint at the installer: %q", m.errText)
	}
}

// With a server, an import keyword whose definition is empty prefers the file
// named on the line over snapping to neighboring identifiers.
func TestJumpImportKeywordPrefersLinePath(t *testing.T) {
	m, client := newLSPTestModel(t)
	m = tsImportFixture(t, m, `import { clearRuntimeConfigCache } from "../config.ts"`)
	m.edit.cy, m.edit.cx = 0, 0 // on `import` — a keyword the server has no answer for
	m = ctrlJ(t, m)
	if m.openRel != "config.ts" || m.errText != "" {
		t.Fatalf("should redirect to the imported file: open=%q err=%q", m.openRel, m.errText)
	}
	if len(client.defCols) != 1 {
		t.Fatalf("path must be preferred over identifier retries: %v", client.defCols)
	}
}

// A real imported symbol still resolves via the server, not the path.
func TestJumpImportedSymbolStillGoesToDefinition(t *testing.T) {
	m, client := newLSPTestModel(t)
	m = tsImportFixture(t, m, `import { clearRuntimeConfigCache } from "../config.ts"`)
	client.defQueue = [][]lsp.Location{{{
		URI:   lsp.PathToURI(filepath.Join(m.root, "config.ts")),
		Range: lsp.Range{Start: lsp.Position{Line: 0, Character: 13}},
	}}}
	m.edit.cy, m.edit.cx = 0, 12 // on clearRuntimeConfigCache

	m = ctrlJ(t, m)
	if m.openRel != "config.ts" || m.edit.cx != 13 {
		t.Fatalf("symbol should jump to its definition position: open=%q cx=%d err=%q", m.openRel, m.edit.cx, m.errText)
	}
	if len(client.defCols) != 1 || client.defCols[0] != 12 {
		t.Fatalf("definition should be queried at the cursor: %v", client.defCols)
	}
}

// Extensionless imports resolve by probing the configured language
// extensions — in TS and in Go alike (nothing language-specific).
func TestJumpExtensionlessImportProbing(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = tsImportFixture(t, m, `import { x } from "../config"`)
	m.edit.cy, m.edit.cx = 0, 0
	m = ctrlJ(t, m)
	if m.openRel != "config.ts" || m.errText != "" {
		t.Fatalf("extensionless TS import: open=%q err=%q", m.openRel, m.errText)
	}

	m2, root2 := newTestModel(t, nil)
	must(t, os.WriteFile(filepath.Join(root2, "lib", "helper.go"), []byte("package lib\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(root2, "note.go"), []byte("package main\n\n// see \"lib/helper\"\n"), 0o644))
	m2 = m2.openFileAt("note.go")
	m2.edit.cy, m2.edit.cx = 2, 0
	m2 = ctrlJ(t, m2)
	if m2.openRel != "lib/helper.go" || m2.errText != "" {
		t.Fatalf("extensionless Go reference: open=%q err=%q", m2.openRel, m2.errText)
	}
}

// A directory reference opens its index file.
func TestJumpDirectoryIndexProbing(t *testing.T) {
	m, root := newTestModel(t, nil)
	must(t, os.MkdirAll(filepath.Join(root, "widgets"), 0o755))
	must(t, os.WriteFile(filepath.Join(root, "widgets", "index.ts"), []byte("export {}\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(root, "app.ts"), []byte("import { w } from \"./widgets\"\n"), 0o644))
	m = m.openFileAt("app.ts")
	m.edit.cy, m.edit.cx = 0, 0
	m = ctrlJ(t, m)
	if m.openRel != "widgets/index.ts" || m.errText != "" {
		t.Fatalf("directory import should open its index: open=%q err=%q", m.openRel, m.errText)
	}
}

// A blank line still reports nothing to jump to; exhausted retries report a
// near-cursor miss.
func TestJumpDeadEnds(t *testing.T) {
	m, client := lspJumpModel(t, "package main\n\n\nfunc main() {\n\tprintln()\n}\n")
	m.edit.cy, m.edit.cx = 2, 0 // blank line
	m = ctrlJ(t, m)
	if m.errText != "nothing to jump to" {
		t.Fatalf("blank line: %q", m.errText)
	}
	if len(client.defCols) != 0 {
		t.Fatal("blank line must not query the server")
	}

	m.edit.cy, m.edit.cx = 4, 0 // "\tprintln()" — the server has no answers
	m = ctrlJ(t, m)
	if m.errText != "no definition found near cursor" {
		t.Fatalf("exhausted retries: %q", m.errText)
	}
}
