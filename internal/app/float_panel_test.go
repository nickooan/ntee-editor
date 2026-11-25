package app

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/nickooan/ntee-editor/internal/store"
)

func TestFloatPanelSitsBottomRightAndFlipsAtSixtyFivePercent(t *testing.T) {
	const paneWidth, paneHeight, panelWidth, panelHeight = 80, 20, 30, 6
	top, left, ok := floatPanelPlacement(paneWidth, paneHeight, 0, panelWidth, panelHeight)
	if !ok || top != paneHeight-panelHeight-1 || left != paneWidth-panelWidth-2 {
		t.Fatalf("bottom right with margins: top=%d left=%d ok=%v", top, left, ok)
	}
	// 65% of 20 rows is row 13.
	if top, _, _ = floatPanelPlacement(paneWidth, paneHeight, 12, panelWidth, panelHeight); top != paneHeight-panelHeight-1 {
		t.Fatalf("above 65%% the panel stays at the bottom: top=%d", top)
	}
	if top, _, _ = floatPanelPlacement(paneWidth, paneHeight, 13, panelWidth, panelHeight); top != 1 {
		t.Fatalf("at 65%% the panel moves to the top: top=%d", top)
	}
	if _, _, ok = floatPanelPlacement(40, paneHeight, 0, panelWidth, panelHeight); ok {
		t.Fatal("a pane too narrow to read beside the panel has none")
	}
	if _, _, ok = floatPanelPlacement(paneWidth, 8, 0, panelWidth, panelHeight); ok {
		t.Fatal("a pane too short has none")
	}
}

func TestFloatPanelRendersAlignedRowsOfOneWidth(t *testing.T) {
	panel := floatPanel{title: " decide ", titleStyle: floatPanelTitleStyle, rows: []floatPanelRow{{"a", "apply all"}, {"Esc", "leave…"}, {"", "plain text"}}}
	lines := panel.render()
	if len(lines) != panel.height() {
		t.Fatalf("rows + 2 borders: %d", len(lines))
	}
	for _, line := range lines {
		if width := ansi.StringWidth(line); width != panel.width() {
			t.Fatalf("every row is the panel's width (%d): %d %q", panel.width(), width, ansi.Strip(line))
		}
	}
	plain := ansi.Strip(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "╭─ decide ") || !strings.Contains(plain, "│  a    apply all") || !strings.Contains(plain, "│  Esc  leave…") || !strings.Contains(plain, "│  plain text") {
		t.Fatalf("title on the border, keys in a column:\n%s", plain)
	}
}

func TestAgentEditDecisionPanelFloatsOverTheCode(t *testing.T) {
	m, _, receiver, _, _ := agentEditsModel(t, store.AgentEditPolicyReview)
	m = endAgentEdits(t, m, receiver, true)
	file, _ := m.selectedReviewFile()
	width, height := max(3, m.width-m.sidebarWidth())-4, m.reviewRowsHeight()
	panel, top, _, ok := m.agentEditDecisionPlacement(file, width, height)
	if !ok || top != height-panel.height()-1 {
		t.Fatalf("the panel floats at the bottom while the cursor is near the top: top=%d ok=%v", top, ok)
	}
	if plain := ansi.Strip(m.View().Content); !strings.Contains(plain, "apply all") || !strings.Contains(plain, "request changes") {
		t.Fatalf("the panel names the decision keys:\n%s", plain)
	}
	if status := ansi.Strip(m.renderStatusLine()); strings.Contains(status, "a apply all to disk") {
		t.Fatalf("the status line leaves the decision to the panel: %q", status)
	}
}
