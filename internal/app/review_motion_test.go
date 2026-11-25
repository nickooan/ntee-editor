package app

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/nickooan/ntee-editor/internal/store"
)

// settleReviewMotion runs the review's animation ticks to their end, as the
// tick chain would, so a render shows the settled page.
func settleReviewMotion(m Model) Model {
	for range 64 {
		if !m.review.motion.running() {
			break
		}
		next, _ := m.handleReviewMotionTick(reviewMotionTickMsg{gen: m.review.gen})
		m = next.(Model)
	}
	return m
}

// stagedDocument is the review document of a file edited from base to
// content.
func stagedDocument(base, content string) diffDocument {
	file := loadStagedReviewFile(reviewFile{workspaceRel: "f.go", staged: &stagedReviewSource{base: base, baseExists: true, content: content}}, 0, 4)
	return file.document
}

// deliverRound delivers the msgs of the given kinds from cmd, one round, and
// returns the commands they led to.
func deliverRound(m Model, cmd tea.Cmd, keep func(tea.Msg) bool) (Model, []tea.Cmd) {
	var next []tea.Cmd
	for _, msg := range execCmds(cmd) {
		if keep(msg) {
			var follow tea.Cmd
			m, follow = deliver(m, msg)
			next = append(next, follow)
		}
	}
	return m, next
}

func isReviewResolved(msg tea.Msg) bool { _, ok := msg.(reviewFileResolvedMsg); return ok }
func isReviewLoaded(msg tea.Msg) bool   { _, ok := msg.(reviewLoadedMsg); return ok }

func TestReviewResendKeepsTheDiffOnScreenWhileItReloads(t *testing.T) {
	m, _, _ := startedReviewSession(t)
	m = keySettled(m, shiftKey(tea.KeyUp)) // main.go, loaded
	m, cmd := m.addReviewSessionFile([]reviewRequest{{workspaceRel: "apps/web/main.go"}})
	file, _ := m.selectedReviewFile()
	if !file.loaded() || !file.reloading {
		t.Fatalf("a resent file keeps its loaded diff while it reloads: loaded=%v reloading=%v", file.loaded(), file.reloading)
	}
	if plain := ansi.Strip(m.View().Content); strings.Contains(plain, "computing diff…") || !strings.Contains(plain, "func main() { run() }") {
		t.Fatalf("the pane never blanks while the file reloads:\n%s", plain)
	}
	m = settleExternal(m, cmd)
	if file, _ = m.selectedReviewFile(); file.reloading || !file.loaded() {
		t.Fatalf("the reload lands: reloading=%v", file.reloading)
	}
}

func TestReviewFollowWaitsForAnArrivalToLoad(t *testing.T) {
	m, _, _ := startedReviewSession(t)
	m = settleReviewMotion(m) // the queued files' own arrival
	if selectedReviewPath(m) != "apps/web/new.go" || !m.review.follow {
		t.Fatalf("following the newest file: %q", selectedReviewPath(m))
	}
	m, cmd := m.addReviewSessionFile([]reviewRequest{{workspaceRel: "libs/core/lib.go"}})
	m, loads := deliverRound(m, cmd, isReviewResolved)
	if selectedReviewPath(m) != "apps/web/new.go" || m.review.followPending != "libs/core/lib.go" {
		t.Fatalf("the file on screen stays until the arrival loaded: selected=%q pending=%q", selectedReviewPath(m), m.review.followPending)
	}
	for _, path := range []string{"libs", "libs/core", "libs/core/lib.go"} {
		if _, injected := m.review.motion.injected[path]; !injected {
			t.Fatalf("%s slides into the list: %v", path, m.review.motion.injected)
		}
	}
	if _, injected := m.review.motion.injected["apps/web/new.go"]; injected {
		t.Fatal("rows already listed don't animate")
	}
	if plain := ansi.Strip(m.View().Content); strings.Contains(plain, "computing diff…") {
		t.Fatalf("no blank pane while the arrival loads:\n%s", plain)
	}
	m, _ = deliverRound(m, tea.Batch(loads...), isReviewLoaded)
	if selectedReviewPath(m) != "libs/core/lib.go" || !m.review.follow || m.review.followPending != "" {
		t.Fatalf("once loaded the view follows it: selected=%q follow=%v", selectedReviewPath(m), m.review.follow)
	}
	if m.review.motion.code == nil || m.review.motion.code.path != "libs/core/lib.go" {
		t.Fatal("the followed file reveals its rows")
	}
	if m = settleReviewMotion(m); m.review.motion.running() || len(m.review.motion.injected) != 0 || m.review.motion.code != nil {
		t.Fatalf("the animations end: %+v", m.review.motion)
	}
}

func TestReviewFollowPendingDropsWhenTheUserMoves(t *testing.T) {
	m, _, _ := startedReviewSession(t)
	m, cmd := m.addReviewSessionFile([]reviewRequest{{workspaceRel: "libs/core/lib.go"}})
	m, loads := deliverRound(m, cmd, isReviewResolved)
	m = keySettled(m, shiftKey(tea.KeyUp)) // the user moves to main.go
	m, _ = deliverRound(m, tea.Batch(loads...), isReviewLoaded)
	if selectedReviewPath(m) != "apps/web/main.go" || m.review.follow {
		t.Fatalf("the user's choice wins over a pending follow: selected=%q follow=%v", selectedReviewPath(m), m.review.follow)
	}
}

func TestCompareReviewDocumentsFindsWhatAReloadChanged(t *testing.T) {
	base := "a\nb\nc\n"
	motion := compareReviewDocuments(stagedDocument(base, base), stagedDocument(base, "a\nB\nc\nd\n"))
	// new rows: a · -b · +B · c · +d
	if motion == nil || !motion.recolored[1] || !motion.inserted[2] || !motion.inserted[4] || motion.inserted[0] || motion.inserted[3] || len(motion.ghosts) != 0 {
		t.Fatalf("b turned removed, B and d are new: %+v", motion)
	}

	motion = compareReviewDocuments(stagedDocument(base, "a\nb\nX\nc\n"), stagedDocument(base, base))
	if motion == nil || len(motion.ghosts) != 1 || strings.Join(motion.ghosts[2], ",") != "X" {
		t.Fatalf("a removed row collapses where it sat: %+v", motion)
	}
	motion = compareReviewDocuments(stagedDocument(base, "a\nb\nc\nd\n"), stagedDocument(base, base))
	if motion == nil || strings.Join(motion.ghosts[3], ",") != "d" {
		t.Fatalf("a removed last row sits past the end: %+v", motion)
	}

	if motion := compareReviewDocuments(stagedDocument(base, "a\nb\n"), stagedDocument(base, "a\nb\n")); motion != nil {
		t.Fatalf("nothing changed, nothing animates: %+v", motion)
	}
	huge := diffDocument{rows: make([]diffRow, maxMotionRows+1), lines: make([]string, maxMotionRows+1)}
	if motion := compareReviewDocuments(huge, stagedDocument(base, base)); motion != nil {
		t.Fatal("huge documents are not compared")
	}
}

func TestDiffMotionExpandsInsertedRowsAndCollapsesRemovedOnes(t *testing.T) {
	base := "l1\nl2\nl3\nl4\nl5\nl6\n"
	document := stagedDocument(base, "l1\nn1\nn2\nn3\nn4\nl2\nl3\nl4\nl5\nl6\n")
	motion := &rowMotion{inserted: make([]bool, len(document.rows)), ghosts: map[int][]string{7: {"gone one", "gone two"}}}
	for row := range document.rows {
		motion.inserted[row] = strings.HasPrefix(document.rowText(row), "n")
	}
	position := rowCursor{row: 0}
	render := func(progress float64) string {
		return ansi.Strip(renderDiffDocument(document, position, diffSearchOverlay{}, nil, diffMotionFrame{rows: motion, progress: progress}, 60, 14, 4))
	}
	start := render(0)
	if strings.Count(start, "│ n") != 1 || !strings.Contains(start, "n1") || strings.Count(start, "gone") != 2 || strings.Count(start, "×") != 2 {
		t.Fatalf("the first frame shows one new row and both removed rows:\n%s", start)
	}
	middle := render(0.5)
	if count := strings.Count(middle, "│ n"); count < 2 || count > 3 || strings.Count(middle, "gone") != 1 {
		t.Fatalf("mid-way the block has grown and the removed rows shrunk:\n%s", middle)
	}
	static := renderDiffDocument(document, position, diffSearchOverlay{}, nil, diffMotionFrame{}, 60, 14, 4)
	if done := renderDiffDocument(document, position, diffSearchOverlay{}, nil, diffMotionFrame{rows: motion, progress: 1}, 60, 14, 4); done != static {
		t.Fatalf("the last frame is the settled page:\n%s\n---\n%s", ansi.Strip(done), ansi.Strip(static))
	}
	if lines := strings.Split(start, "\n"); len(lines) != 14 {
		t.Fatalf("every frame fills the pane: %d rows", len(lines))
	}
}

func TestReviewListInjectSlidesAndSettles(t *testing.T) {
	m, _, _ := startedReviewSession(t)
	m = settleReviewMotion(m)
	path := "apps/web/main.go"
	row := m.review.rowByPath[path]
	settled := sidebarRowPlain(m.reviewSidebarRow(row))
	m.review.motion.injected = map[string]int{path: m.review.motion.frame}
	if sliding := sidebarRowPlain(m.reviewSidebarRow(row)); sliding != "    "+settled {
		t.Fatalf("a new row starts a few cells right: %q vs %q", sliding, settled)
	}
	ticked, cmd := withReviewMotionTick(m, nil)
	if !ticked.(Model).review.motion.ticking || cmd == nil {
		t.Fatal("an inject starts the tick chain")
	}
	settledModel := settleReviewMotion(ticked.(Model))
	if got := sidebarRowPlain(settledModel.reviewSidebarRow(row)); got != settled || settledModel.review.motion.ticking {
		t.Fatalf("the row settles in place and the chain stops: %q ticking=%v", got, settledModel.review.motion.ticking)
	}
	if _, next := settledModel.handleReviewMotionTick(reviewMotionTickMsg{gen: settledModel.review.gen - 1}); next != nil {
		t.Fatal("a tick from an older review is dropped")
	}
}

func TestReviewFoldDoesNotInject(t *testing.T) {
	m, _, _ := startedReviewSession(t)
	m = settleReviewMotion(m)
	m = m.toggleReviewFold("apps").toggleReviewFold("apps")
	if len(m.review.motion.injected) != 0 {
		t.Fatalf("unfolding shows rows, it doesn't inject them: %v", m.review.motion.injected)
	}
}

func TestBlendHexMixesAndQuantizes(t *testing.T) {
	if got := blendHex("#000000", "#ffffff", 0); got != "#000000" {
		t.Fatalf("t=0 is from: %s", got)
	}
	if got := blendHex("#000000", "#ffffff", 1); got != "#ffffff" {
		t.Fatalf("t=1 is to: %s", got)
	}
	if got := blendHex("#000000", "#ffffff", 0.51); got != "#808080" {
		t.Fatalf("t is quantized to eighths: %s", got)
	}
	if got := blendHex("bad", "#282828", 0.3); got != "#282828" {
		t.Fatalf("an unparsable color falls back to the target: %s", got)
	}
}

func TestAgentEditDecisionFallsBackToTheStatusLineWhenThePaneIsSmall(t *testing.T) {
	m, _, receiver, _, _ := agentEditsModel(t, store.AgentEditPolicyReview)
	m = endAgentEdits(t, m, receiver, true)
	if !m.reviewPanelShown() {
		t.Fatal("the panel floats in a regular window")
	}
	m.width = 44 // a 24-cell code pane
	if m.reviewPanelShown() {
		t.Fatal("a narrow pane has no room for the panel")
	}
	if status := ansi.Strip(m.renderStatusLine()); !strings.Contains(status, "a apply all to disk") {
		t.Fatalf("the status line carries the decision instead: %q", status)
	}
}
