package app

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// The outline sidebar's badge+tail row was the one structural divergence
// between the two preview modes; pin its layout for both descriptors.
func TestRenderPreviewSidebarRows(t *testing.T) {
	m, _ := newTestModel(t, nil)
	outline := []previewOutlineEntry{
		{Label: "Pets", Depth: 0},
		{Label: "GET /pets", Depth: 1, Badge: "GET", Tail: "/pets", Color: "#8ec07c"},
	}

	m.mode = modeOpenAPI
	m.preview.outline = outline
	m.preview.sel = 0
	got := ansi.Strip(m.renderPreviewSidebar(30, 5))
	rows := strings.Split(got, "\n")
	if len(rows) != 2 {
		t.Fatalf("rows = %d: %q", len(rows), got)
	}
	if !strings.HasPrefix(rows[0], " Pets") {
		t.Fatalf("header row = %q", rows[0])
	}
	// openapi badge column is 7 wide: "  " + "GET    " + "/pets"
	if !strings.HasPrefix(rows[1], "  GET    /pets") {
		t.Fatalf("openapi entry row = %q", rows[1])
	}

	m.mode = modeGraphQL
	m.preview.outline = []previewOutlineEntry{
		{Label: "Types", Depth: 0},
		{Label: "User", Depth: 1, Badge: "type", Tail: "User", Color: "#83a598"},
	}
	got = ansi.Strip(m.renderPreviewSidebar(30, 5))
	rows = strings.Split(got, "\n")
	// graphql badge column is 6 wide: "  " + "type  " + "User"
	if !strings.HasPrefix(rows[1], "  type  User") {
		t.Fatalf("graphql entry row = %q", rows[1])
	}

	// Selected entry renders badge+tail as one highlighted label.
	m.preview.sel = 1
	got = ansi.Strip(m.renderPreviewSidebar(30, 5))
	rows = strings.Split(got, "\n")
	if !strings.HasPrefix(rows[1], "  type  User") {
		t.Fatalf("selected entry row = %q", rows[1])
	}
}

// The batched renderers must paint exactly the same visible characters as the
// old per-column loops: the padded window with the cursor/selection styling
// carried in ANSI only.
func TestBatchedLineRenderersVisibleOutput(t *testing.T) {
	line := "hello world"
	width := 8

	// renderEditLine: cursor inside, no selection.
	if got := ansi.Strip(renderEditLine(line, 3, width, nil)); got != "hello wo" {
		t.Fatalf("renderEditLine = %q", got)
	}
	// Cursor beyond width scrolls the window.
	if got := ansi.Strip(renderEditLine(line, 10, width, nil)); got != "lo world" {
		t.Fatalf("renderEditLine scrolled = %q", got)
	}
	// Selection spanning the cursor still renders the same runes.
	sel := &selRange{start: 1, end: 6}
	if got := ansi.Strip(renderEditLine(line, 3, width, sel)); got != "hello wo" {
		t.Fatalf("renderEditLine selected = %q", got)
	}
	// Short line pads to width.
	if got := ansi.Strip(renderEditLine("hi", 1, 6, nil)); got != "hi    " {
		t.Fatalf("renderEditLine padded = %q", got)
	}

	if got := ansi.Strip(renderSelectedLine("hi", 6)); got != "hi    " {
		t.Fatalf("renderSelectedLine = %q", got)
	}

	if got := ansi.Strip(renderDiffCursorLine(line, 3, 8, cursorLineStyle)); got != "hello wo" {
		t.Fatalf("renderDiffCursorLine = %q", got)
	}
	if got := ansi.Strip(renderDiffCursorLine("hi", 0, 6, cursorLineStyle)); got != "hi    " {
		t.Fatalf("renderDiffCursorLine padded = %q", got)
	}
}
