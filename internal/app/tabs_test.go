package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nickooan/ntee-editor/internal/config"
	"github.com/nickooan/ntee-editor/internal/store"
)

func TestSwitchAwayStashesDraftAndRestores(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = m.openFileAt("main.go")
	m = runes(m, "// wip")
	if !m.edit.dirty {
		t.Fatal("fixture should be dirty")
	}
	edited := m.edit.content()

	m = m.openFileAt("lib/util.ts") // switch away → stash
	d, ok := m.db.LoadDraft("main.go")
	if !ok || d.Content != edited || len(d.Steps) == 0 {
		t.Fatalf("draft not stashed: ok=%v %+v", ok, d)
	}
	if !m.draftSet["main.go"] {
		t.Fatal("draftSet should mark main.go")
	}

	m = m.openFileAt("main.go") // back → restore
	if m.edit.content() != edited || !m.edit.dirty {
		t.Fatalf("draft not restored: %q dirty=%v", m.edit.content(), m.edit.dirty)
	}
	if m.notice != "restored unsaved draft" {
		t.Fatalf("notice = %q", m.notice)
	}
}

func TestDraftRestoreUndoTimeline(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = m.openFileAt("main.go")
	disk := m.openFile.Content
	m = runes(m, "A ") // space flushes → one draft step
	m = runes(m, "B")  // pending burst → flushed by stash
	edited := m.edit.content()

	m = m.openFileAt("lib/util.ts")
	m = m.openFileAt("main.go")
	if m.edit.content() != edited {
		t.Fatalf("restored head = %q", m.edit.content())
	}
	// Undo walks back through the draft steps down to the disk baseline.
	for i := 0; i < 20 && m.edit.content() != disk; i++ {
		m = ctrl(m, tea.KeyCtrlZ)
	}
	if m.edit.content() != disk {
		t.Fatalf("undo never reached disk baseline: %q", m.edit.content())
	}
	// Redo returns to the draft head.
	for i := 0; i < 20 && m.edit.content() != edited; i++ {
		m = ctrl(m, tea.KeyCtrlY)
	}
	if m.edit.content() != edited {
		t.Fatalf("redo never reached draft head: %q", m.edit.content())
	}
}

func TestSaveDeletesDraft(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = m.openFileAt("main.go")
	m = runes(m, "x")
	m = m.openFileAt("lib/util.ts")
	m = m.openFileAt("main.go")
	if _, ok := m.db.LoadDraft("main.go"); !ok {
		t.Fatal("setup: draft should exist")
	}
	m = ctrl(m, tea.KeyCtrlS)
	if _, ok := m.db.LoadDraft("main.go"); ok {
		t.Fatal("save must delete the draft")
	}
	if m.draftSet["main.go"] {
		t.Fatal("draftSet must clear on save")
	}
}

func TestEscDiscardsDraft(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = m.openFileAt("main.go")
	disk := m.openFile.Content
	m = runes(m, "junk")
	m = ctrl(m, tea.KeyEsc)
	if _, ok := m.db.LoadDraft("main.go"); ok {
		t.Fatal("esc must delete the draft")
	}
	if m.edit.dirty || m.edit.content() != disk {
		t.Fatalf("esc must reset the buffer to disk: dirty=%v", m.edit.dirty)
	}
	if m.mode != modeQuery {
		t.Fatal("esc should return to query mode")
	}
}

func TestTabsCreatedAndDeduped(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = m.openFileAt("main.go")
	m = m.openFileAt("lib/util.ts")
	m = m.openFileAt("main.go")
	if len(m.tabs) != 2 || m.tabs[0] != "main.go" || m.tabs[1] != "lib/util.ts" {
		t.Fatalf("tabs = %v", m.tabs)
	}
	if m.tabActive != 0 {
		t.Fatalf("active = %d", m.tabActive)
	}
	m = m.openFileAt("missing.go") // failed open must not create a tab
	if len(m.tabs) != 2 {
		t.Fatalf("failed open changed tabs: %v", m.tabs)
	}
	tabs, ok := m.db.LoadTabs()
	if !ok || len(tabs.Paths) != 2 || tabs.Active != 0 {
		t.Fatalf("tabs not persisted: %+v ok=%v", tabs, ok)
	}
}

func TestShiftTabCycles(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = m.openFileAt("main.go")
	m = m.openFileAt("lib/util.ts")
	if m.tabActive != 1 {
		t.Fatalf("setup active = %d", m.tabActive)
	}
	m = ctrl(m, tea.KeyShiftTab)
	if m.tabActive != 0 || m.openRel != "main.go" {
		t.Fatalf("cycle: active=%d open=%q", m.tabActive, m.openRel)
	}
	m = ctrl(m, tea.KeyShiftTab) // wraps
	if m.tabActive != 1 || m.openRel != "lib/util.ts" {
		t.Fatalf("wrap: active=%d open=%q", m.tabActive, m.openRel)
	}
}

func TestTabCommandJumpAndErrors(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = m.openFileAt("main.go")
	m = m.openFileAt("lib/util.ts")

	// : bar jump by base name.
	next, _ := m.executeCommand("tab main.go")
	m = next.(Model)
	if m.openRel != "main.go" || m.mode != modeEdit {
		t.Fatalf(":tab jump failed: %q mode=%v", m.openRel, m.mode)
	}

	// @exec jump.
	m = ctrl(m, tea.KeyCtrlE)
	m = runes(m, "tab util.ts")
	m = ctrl(m, tea.KeyEnter)
	if m.openRel != "lib/util.ts" || m.mode != modeEdit {
		t.Fatalf("@exec tab jump failed: %q mode=%v", m.openRel, m.mode)
	}

	// Unknown tab: exec stays with an error.
	m = ctrl(m, tea.KeyCtrlE)
	m = runes(m, "tab nope.zz")
	m = ctrl(m, tea.KeyEnter)
	if m.mode != modeExec || m.errText != "no tab: nope.zz" {
		t.Fatalf("unknown tab: mode=%v err=%q", m.mode, m.errText)
	}
}

func TestTabCloseSidesSkipUnsaved(t *testing.T) {
	m, root := newTestModel(t, nil)
	must(t, os.WriteFile(filepath.Join(root, "c.go"), []byte("package main\n"), 0o644))
	m = m.openFileAt("main.go")
	m = runes(m, "dirty") // main.go will hold a draft
	m = m.openFileAt("lib/util.ts")
	m = m.openFileAt("c.go") // tabs: main.go(draft), util.ts, c.go(active)

	m = m.closeTabsSide(true) // close-left: util.ts closes, main.go kept (unsaved)
	if len(m.tabs) != 2 || m.tabs[0] != "main.go" || m.tabs[1] != "c.go" {
		t.Fatalf("close-left: %v", m.tabs)
	}
	if m.tabActive != 1 || !strings.Contains(m.notice, "kept 1 unsaved") {
		t.Fatalf("close-left active=%d notice=%q", m.tabActive, m.notice)
	}
	tabs, _ := m.db.LoadTabs()
	if len(tabs.Paths) != 2 {
		t.Fatalf("close not persisted: %+v", tabs)
	}
}

func TestTabDirtyAndStrip(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = m.openFileAt("main.go")
	m = runes(m, "x")
	m = m.openFileAt("lib/util.ts") // main.go → draft, util.ts active clean

	if !m.tabDirty(0) {
		t.Fatal("drafted inactive tab should be dirty")
	}
	if m.tabDirty(1) {
		t.Fatal("clean active tab should not be dirty")
	}
	strip := m.renderTabStrip(60)
	if !strings.Contains(strip, "main.go") || !strings.Contains(strip, "util.ts") {
		t.Fatalf("strip missing names: %q", strip)
	}

	m = runes(m, "y") // active tab dirty now
	if !m.tabDirty(1) {
		t.Fatal("dirty active tab should be dirty")
	}
}

func TestStartupRestoresTabsAndDraft(t *testing.T) {
	m0, root := newTestModel(t, nil)
	db := m0.db
	_ = db.SaveTabs(store.Tabs{Paths: []string{"main.go", "lib/util.ts"}, Active: 0})
	_ = db.SaveDraft(store.Draft{
		Path: "main.go", Content: "draft content", Cy: 0, Cx: 0,
		Steps: []store.DraftStep{{Kind: "edit", Content: "draft content"}},
	})

	m := New(config.Default(), db, root, "", nil)
	m.width, m.height, m.ready = 100, 30, true
	if len(m.tabs) != 2 || m.tabActive != 0 || m.openRel != "main.go" {
		t.Fatalf("startup tabs: %v active=%d open=%q", m.tabs, m.tabActive, m.openRel)
	}
	if m.edit.content() != "draft content" || !m.edit.dirty {
		t.Fatalf("startup draft: %q dirty=%v", m.edit.content(), m.edit.dirty)
	}
	if m.mode != modeQuery {
		t.Fatal("startup should land on the query bar")
	}
}

func TestTabCursorRestoreAnchors(t *testing.T) {
	m := execLineFixture(t, 100) // opens big.go in edit mode, height 30
	m.edit.cy, m.edit.cx = 40, 2 // move the cursor mid-file

	m = m.openFileAt("main.go") // leave → records big.go's cursor
	if c := m.cursorMem["big.go"]; c.Cy != 40 || c.Cx != 2 {
		t.Fatalf("cursor not recorded: %+v", m.cursorMem["big.go"])
	}

	m = m.openFileAt("big.go") // back → restore + 30% anchor
	if m.edit.cy != 40 || m.edit.cx != 2 {
		t.Fatalf("cursor not restored: cy=%d cx=%d", m.edit.cy, m.edit.cx)
	}
	want := anchorScroll(40, m.contentHeight(), len(m.edit.lines))
	if m.fileScrollY != want {
		t.Fatalf("scroll not 30%%-anchored: %d, want %d", m.fileScrollY, want)
	}
}

func TestFreshFileOpensAtTop(t *testing.T) {
	m := execLineFixture(t, 100)
	if m.edit.cy != 0 || m.fileScrollY != 0 {
		t.Fatalf("fresh open should be at top: cy=%d scrollY=%d", m.edit.cy, m.fileScrollY)
	}
}

func TestQuitPersistsCursor(t *testing.T) {
	m := execLineFixture(t, 100)
	m.edit.cy, m.edit.cx = 12, 5
	next, _ := m.quit()
	m = next.(Model)
	tabs, ok := m.db.LoadTabs()
	if !ok {
		t.Fatal("tabs not persisted on quit")
	}
	if c := tabs.Cursors["big.go"]; c.Cy != 12 || c.Cx != 5 {
		t.Fatalf("quit did not persist cursor: %+v", tabs.Cursors)
	}
}

func TestQuitStashesDraft(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = m.openFileAt("main.go")
	m = runes(m, "unsaved!")
	next, _ := m.quit()
	m = next.(Model)
	if d, ok := m.db.LoadDraft("main.go"); !ok || d.Content != m.edit.content() {
		t.Fatalf("quit did not stash: ok=%v", ok)
	}
}
