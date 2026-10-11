package app

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/nickooan/ntee-editor/internal/view"
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
	got := ansi.Strip(renderSidebarList(m.previewSidebarList(), 30, 5))
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
	got = ansi.Strip(renderSidebarList(m.previewSidebarList(), 30, 5))
	rows = strings.Split(got, "\n")
	// graphql badge column is 6 wide: "  " + "type  " + "User"
	if !strings.HasPrefix(rows[1], "  type  User") {
		t.Fatalf("graphql entry row = %q", rows[1])
	}

	// Selected entry renders badge+tail as one highlighted label.
	m.preview.sel = 1
	got = ansi.Strip(renderSidebarList(m.previewSidebarList(), 30, 5))
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
	if got := ansi.Strip(renderEditLine(line, nil, 3, width, nil)); got != "hello wo" {
		t.Fatalf("renderEditLine = %q", got)
	}
	// Cursor beyond width scrolls the window.
	if got := ansi.Strip(renderEditLine(line, nil, 10, width, nil)); got != "lo world" {
		t.Fatalf("renderEditLine scrolled = %q", got)
	}
	// Selection spanning the cursor still renders the same runes.
	sel := &selRange{start: 1, end: 6}
	if got := ansi.Strip(renderEditLine(line, nil, 3, width, sel)); got != "hello wo" {
		t.Fatalf("renderEditLine selected = %q", got)
	}
	// Short line pads to width.
	if got := ansi.Strip(renderEditLine("hi", nil, 1, 6, nil)); got != "hi    " {
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

func TestRenderEditLineKeepsSyntaxColours(t *testing.T) {
	keyword := view.HighlightSegment{Text: "func", Color: "#fb4934", Bold: true}
	rest := view.HighlightSegment{Text: " main()"}
	segments := []view.HighlightSegment{keyword, rest}
	line := "func main()"

	got := renderEditLine(line, segments, 9, 14, nil)
	if ansi.Strip(got) != "func main()   " {
		t.Fatalf("visible text changed: %q", ansi.Strip(got))
	}
	if !strings.Contains(got, segStyleWithBg(keyword, hexLineHl).Render("func")) {
		t.Fatal("the keyword should keep its colour on the cursor-line tint")
	}
	if !strings.Contains(got, cursorStyle.Render("(")) {
		t.Fatal("the cursor cell should stay a block cursor")
	}
	if strings.Contains(got, cursorLineStyle.Render("func")) {
		t.Fatal("the keyword must not be repainted in the plain foreground")
	}

	selected := renderEditLine(line, segments, 9, 14, &selRange{start: 0, end: 2})
	if !strings.Contains(selected, segStyleWithBg(keyword, hexSelection).Render("fu")) ||
		!strings.Contains(selected, segStyleWithBg(keyword, hexLineHl).Render("nc")) {
		t.Fatal("a selection should keep the token colour on the selection background")
	}
}

func TestRenderEditLineScrolledWindowColoursRightRunes(t *testing.T) {
	keyword := view.HighlightSegment{Text: "return", Color: "#fb4934"}
	segments := []view.HighlightSegment{{Text: "x := 1; "}, keyword}
	got := renderEditLine("x := 1; return", segments, 14, 8, nil)
	if ansi.Strip(got) != " return " {
		t.Fatalf("scrolled window = %q", ansi.Strip(got))
	}
	if !strings.Contains(got, segStyleWithBg(keyword, hexLineHl).Render("return")) {
		t.Fatal("the keyword inside the scrolled window should keep its colour")
	}
}

func TestRenderEditLineIgnoresStaleSegments(t *testing.T) {
	stale := []view.HighlightSegment{{Text: "fun", Color: "#fb4934"}}
	got := renderEditLine("func main()", stale, 0, 12, nil)
	if want := cursorStyle.Render("f") + cursorLineStyle.Render("unc main() "); got != want {
		t.Fatalf("mismatched segments should render plain:\n got %q\nwant %q", got, want)
	}
}

func TestEditModeCursorLineKeepsSyntaxColours(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = m.openFileAt("main.go") // opens straight into edit mode
	if m.mode != modeEdit || m.edit.cy != 0 {
		t.Fatalf("expected edit mode on line 1: mode=%v cy=%d", m.mode, m.edit.cy)
	}
	segments := m.lineSegments(0)
	if len(segments) == 0 || segments[0].Color == "" {
		t.Fatalf("line 1 should be highlighted: %+v", segments)
	}
	keyword := segments[0]
	if want := segStyleWithBg(keyword, hexLineHl).Render(keyword.Text[1:]); !strings.Contains(m.render(), want) {
		t.Fatalf("the cursor line should keep %q's syntax colour on the tint", keyword.Text)
	}
}
