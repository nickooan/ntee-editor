package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

const testSpec = `openapi: 3.0.3
info:
  title: T
  version: "1"
paths:
  /pets:
    get:
      operationId: listPets
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/Pet"
    post:
      operationId: createPet
      responses:
        "201":
          description: created
components:
  schemas:
    Pet:
      type: object
      properties:
        name:
          type: string
        owner:
          $ref: "./common.yaml#/components/schemas/Owner"
`

const testCommon = `components:
  schemas:
    Owner:
      type: object
      properties:
        email:
          type: string
`

// openAPIFixture opens a spec file and enters the preview through the @exec
// bar, delivering the async render synchronously.
func openAPIFixture(t *testing.T) Model {
	t.Helper()
	m, root := newTestModel(t, nil)
	must(t, os.WriteFile(filepath.Join(root, "spec.yaml"), []byte(testSpec), 0o644))
	must(t, os.WriteFile(filepath.Join(root, "common.yaml"), []byte(testCommon), 0o644))
	m = m.openFileAt("spec.yaml")

	m = key(m, ctrlKey('e'))
	m = runes(m, "openapi")
	next, cmd := m.Update(keyPress(tea.KeyEnter))
	m = next.(Model)
	if m.mode != modeOpenAPI {
		t.Fatalf("openapi should enter modeOpenAPI, got %v (err=%q)", m.mode, m.errText)
	}
	if !m.openapiLoading || cmd == nil {
		t.Fatal("entry must kick off the async render")
	}
	next, _ = m.Update(cmd())
	m = next.(Model)
	if m.openapiLoading || len(m.openapiLines) == 0 {
		t.Fatalf("render did not land: loading=%v lines=%d err=%q", m.openapiLoading, len(m.openapiLines), m.errText)
	}
	return m
}

// openAPIRow finds the first rendered row containing sub.
func openAPIRow(t *testing.T, m Model, sub string) int {
	t.Helper()
	for i, l := range m.openapiPlain {
		if strings.Contains(l, sub) {
			return i
		}
	}
	t.Fatalf("no rendered row contains %q:\n%s", sub, strings.Join(m.openapiPlain, "\n"))
	return -1
}

func TestOpenAPIEntryRejectsNonSpec(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = m.openFileAt("main.go")
	m = key(m, ctrlKey('e'))
	m = runes(m, "openapi")
	m = key(m, keyPress(tea.KeyEnter))
	if m.mode != modeExec {
		t.Fatalf("non-spec file must stay in the exec bar, got %v", m.mode)
	}
	if !strings.Contains(m.errText, "invalid openapi yml") {
		t.Fatalf("errText = %q", m.errText)
	}
}

// The exec bar stays open on a failed entry, so the error must be visible in
// the rendered status line itself — the field being set is not enough.
func TestOpenAPIErrorVisibleInExecBar(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = m.openFileAt("main.go")
	m = key(m, ctrlKey('e'))
	m = runes(m, "openapi")
	m = key(m, keyPress(tea.KeyEnter))
	if m.mode != modeExec {
		t.Fatalf("mode = %v, want modeExec", m.mode)
	}
	status := ansi.Strip(m.renderStatusLine())
	if !strings.Contains(status, "invalid openapi yml") {
		t.Fatalf("exec status bar must show the error, got %q", status)
	}
}

func TestOpenAPIEntryRejectsMalformedSpec(t *testing.T) {
	m, root := newTestModel(t, nil)
	// Detects as OpenAPI (openapi: 3 present) but is not valid YAML.
	broken := "openapi: 3.0.0\npaths: [\n  bad: {\n"
	must(t, os.WriteFile(filepath.Join(root, "broken.yaml"), []byte(broken), 0o644))
	m = m.openFileAt("broken.yaml")
	m = key(m, ctrlKey('e'))
	m = runes(m, "openapi")
	next, cmd := m.Update(keyPress(tea.KeyEnter))
	m = next.(Model)
	if m.mode != modeOpenAPI || cmd == nil {
		t.Fatalf("detect passes, so entry must start the async render (mode=%v)", m.mode)
	}
	next, _ = m.Update(cmd())
	m = next.(Model)
	if m.mode != modeEdit {
		t.Fatalf("parse failure must fall back to edit mode, got %v", m.mode)
	}
	if !strings.Contains(m.errText, "invalid openapi yml") {
		t.Fatalf("errText = %q", m.errText)
	}
}

func TestOpenAPINotReachableFromCommandBar(t *testing.T) {
	m, root := newTestModel(t, nil)
	must(t, os.WriteFile(filepath.Join(root, "spec.yaml"), []byte(testSpec), 0o644))
	must(t, os.WriteFile(filepath.Join(root, "common.yaml"), []byte(testCommon), 0o644))
	m = m.openFileAt("spec.yaml")
	m = m.enterCommand()
	m = runes(m, "openapi")
	m = key(m, keyPress(tea.KeyEnter))
	if m.mode == modeOpenAPI {
		t.Fatal("openapi must not be reachable from the : bar")
	}
	if !strings.Contains(m.errText, "unknown command") {
		t.Fatalf("errText = %q", m.errText)
	}
}

func TestOpenAPIAliasOpapi(t *testing.T) {
	m, root := newTestModel(t, nil)
	must(t, os.WriteFile(filepath.Join(root, "spec.yaml"), []byte(testSpec), 0o644))
	must(t, os.WriteFile(filepath.Join(root, "common.yaml"), []byte(testCommon), 0o644))
	m = m.openFileAt("spec.yaml")
	m = key(m, ctrlKey('e'))
	m = runes(m, "opapi")
	next, _ := m.Update(keyPress(tea.KeyEnter))
	if got := next.(Model).mode; got != modeOpenAPI {
		t.Fatalf("opapi alias should enter modeOpenAPI, got %v", got)
	}
}

func TestOpenAPIRenderAndOutline(t *testing.T) {
	m := openAPIFixture(t)
	openAPIRow(t, m, "GET  /pets")
	openAPIRow(t, m, "POST  /pets")
	openAPIRow(t, m, "Pet {")
	openAPIRow(t, m, "email: string") // cross-file Owner rendered inline
	if len(m.openapiOutline) != 2 {
		t.Fatalf("outline = %+v", m.openapiOutline)
	}
	if m.openapiOutline[0].Label != "GET /pets" || m.openapiOutline[1].Label != "POST /pets" {
		t.Fatalf("outline labels = %+v", m.openapiOutline)
	}
}

func TestOpenAPIReadOnly(t *testing.T) {
	m := openAPIFixture(t)
	before := m.edit.content()
	for _, msg := range []tea.KeyPressMsg{
		typeRune('x'), keyPress(tea.KeyBackspace), keyPress(tea.KeyTab),
		ctrlKey('z'), ctrlKey('a'), keyPress(tea.KeySpace),
	} {
		m = key(m, msg)
	}
	if m.edit.content() != before {
		t.Fatal("keys in openapi mode must not reach the buffer")
	}
	if m.mode != modeOpenAPI {
		t.Fatalf("mode drifted to %v", m.mode)
	}
}

func TestOpenAPICursorAndOutlineJump(t *testing.T) {
	m := openAPIFixture(t)
	m.openapiCursor, m.openapiScrollY = 0, 0
	m = key(m, keyPress(tea.KeyDown))
	m = key(m, keyPress(tea.KeyDown))
	if m.openapiCursor != 2 {
		t.Fatalf("cursor = %d, want 2", m.openapiCursor)
	}

	// Select the POST entry in the outline and jump to it.
	m = key(m, shiftKey(tea.KeyDown))
	if m.openapiSel != 1 {
		t.Fatalf("outline sel = %d, want 1", m.openapiSel)
	}
	m = key(m, keyPress(tea.KeyEnter))
	if m.openapiCursor != m.openapiOutline[1].LineIdx {
		t.Fatalf("cursor = %d, want anchor %d", m.openapiCursor, m.openapiOutline[1].LineIdx)
	}
	if !strings.Contains(m.openapiPlain[m.openapiCursor], "POST  /pets") {
		t.Fatalf("anchor row = %q", m.openapiPlain[m.openapiCursor])
	}
}

func TestOpenAPISearchCycleAndLand(t *testing.T) {
	m := openAPIFixture(t)
	m = key(m, typeRune('/'))
	if !m.openapiSearching {
		t.Fatal("/ must open the search bar")
	}
	m = runes(m, "pets")
	matches := m.openapiMatches()
	if len(matches) < 2 {
		t.Fatalf("matches = %d, want ≥2", len(matches))
	}
	if m.openapiCursor != matches[m.openapiFocused].LineIndex {
		t.Fatal("typing must land the cursor on the focused match")
	}

	first := m.openapiFocused
	m = key(m, keyPress(tea.KeyDown))
	if m.openapiFocused == first {
		t.Fatal("down must advance the focused match")
	}
	// Wrap all the way back around.
	for i := 0; i < len(matches)-1; i++ {
		m = key(m, keyPress(tea.KeyDown))
	}
	if m.openapiFocused != first {
		t.Fatalf("focus should wrap to %d, got %d", first, m.openapiFocused)
	}

	landed := m.openapiCursor
	m = key(m, keyPress(tea.KeyEnter))
	if m.openapiSearching || m.openapiSearch != "" {
		t.Fatal("enter must close the search bar")
	}
	if m.openapiCursor != landed {
		t.Fatal("enter must keep the cursor on the landed match")
	}
	if m.mode != modeOpenAPI {
		t.Fatal("enter must stay in the preview")
	}

	// Esc closes the bar first; a second Esc exits the mode.
	m = key(m, typeRune('/'))
	m = runes(m, "x")
	m = key(m, keyPress(tea.KeyEsc))
	if m.openapiSearching || m.mode != modeOpenAPI {
		t.Fatal("first esc closes only the search bar")
	}
	m = key(m, keyPress(tea.KeyEsc))
	if m.mode != modeEdit {
		t.Fatalf("second esc must exit to edit mode, got %v", m.mode)
	}
}

func TestOpenAPIExitSameFileSource(t *testing.T) {
	m := openAPIFixture(t)
	row := openAPIRow(t, m, "POST  /pets")
	m.openapiCursor = row
	src := m.openapiLines[row].Src
	if src.File != "spec.yaml" {
		t.Fatalf("POST src = %+v", src)
	}
	m = key(m, keyPress(tea.KeyEsc))
	if m.mode != modeEdit || m.openRel != "spec.yaml" {
		t.Fatalf("esc → mode=%v rel=%q", m.mode, m.openRel)
	}
	if m.edit.cy != src.Line-1 {
		t.Fatalf("cursor line = %d, want %d", m.edit.cy, src.Line-1)
	}
	if !strings.Contains(m.edit.lines[m.edit.cy], "post:") {
		t.Fatalf("landed on %q", m.edit.lines[m.edit.cy])
	}
	if len(m.openapiLines) != 0 {
		t.Fatal("exit must clear the preview state")
	}
}

func TestOpenAPIExitCrossFileSource(t *testing.T) {
	m := openAPIFixture(t)
	row := openAPIRow(t, m, "email: string")
	m.openapiCursor = row
	src := m.openapiLines[row].Src
	if src.File != "common.yaml" {
		t.Fatalf("email src = %+v", src)
	}
	m = key(m, keyPress(tea.KeyEsc))
	if m.mode != modeEdit {
		t.Fatalf("esc → mode=%v", m.mode)
	}
	if m.openRel != "common.yaml" {
		t.Fatalf("esc from cross-file content must open common.yaml, got %q", m.openRel)
	}
	if m.edit.cy != src.Line-1 {
		t.Fatalf("cursor line = %d, want %d", m.edit.cy, src.Line-1)
	}
	if !strings.Contains(m.edit.lines[m.edit.cy], "email:") {
		t.Fatalf("landed on %q", m.edit.lines[m.edit.cy])
	}
}

func TestOpenAPIWheelScroll(t *testing.T) {
	m := openAPIFixture(t)
	m.openapiCursor, m.openapiScrollY = 0, 0
	m = m.wheelScroll(1)
	if m.openapiCursor != wheelScrollLines {
		t.Fatalf("wheel cursor = %d, want %d", m.openapiCursor, wheelScrollLines)
	}
}
