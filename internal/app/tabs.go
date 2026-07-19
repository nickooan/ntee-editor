package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nickooan/ntee-editor/internal/store"
)

// Tabs: every opened file becomes a tab (rels in m.tabs, display order). The
// list and active index persist in the store on every mutation, so a crash or
// relaunch restores the working set.

// addTab makes rel the active tab, appending it if new. Idempotent.
func (m Model) addTab(rel string) Model {
	for i, t := range m.tabs {
		if t == rel {
			m.tabActive = i
			m.persistTabs()
			return m
		}
	}
	m.tabs = append(m.tabs, rel)
	m.tabActive = len(m.tabs) - 1
	m.persistTabs()
	return m
}

func (m Model) persistTabs() {
	_ = m.db.SaveTabs(store.Tabs{Paths: m.tabs, Active: m.tabActive, Cursors: m.cursorMem})
}

// recordCursor remembers the active file's cursor (persisted in the tabs
// record) so returning to its tab restores the position.
func (m Model) recordCursor() Model {
	if m.openFile == nil {
		return m
	}
	m.cursorMem[m.openRel] = store.TabCursor{Cy: m.edit.cy, Cx: m.edit.cx}
	m.persistTabs()
	return m
}

// activateTab opens the i-th tab. A tab whose file has vanished is dropped
// (lazily) rather than erroring forever.
func (m Model) activateTab(i int) Model {
	if len(m.tabs) == 0 {
		return m
	}
	i = min(max(i, 0), len(m.tabs)-1)
	rel := m.tabs[i]
	if _, err := os.Stat(filepath.Join(m.root, rel)); err != nil {
		m.tabs = append(m.tabs[:i], m.tabs[i+1:]...)
		if m.tabActive >= len(m.tabs) {
			m.tabActive = max(0, len(m.tabs)-1)
		}
		m.persistTabs()
		m.errText = "file gone: " + filepath.Base(rel)
		return m
	}
	m = m.closeCompletion()
	return m.openFileAt(rel) // stash/restore/addTab happen inside
}

// cycleTab (Shift+Tab) focuses the next tab left→right, wrapping.
func (m Model) cycleTab() Model {
	if len(m.tabs) < 2 {
		return m
	}
	return m.activateTab((m.tabActive + 1) % len(m.tabs))
}

// findTab resolves a user-typed name: exact rel, then exact base name, then
// base-name prefix; first match wins.
func (m Model) findTab(arg string) (int, bool) {
	for i, rel := range m.tabs {
		if rel == arg {
			return i, true
		}
	}
	for i, rel := range m.tabs {
		if filepath.Base(rel) == arg {
			return i, true
		}
	}
	for i, rel := range m.tabs {
		if strings.HasPrefix(filepath.Base(rel), arg) {
			return i, true
		}
	}
	return 0, false
}

// tabDirty reports whether tab i should render red: the active tab from the
// live buffer, inactive tabs from having a stashed draft.
func (m Model) tabDirty(i int) bool {
	if i < 0 || i >= len(m.tabs) {
		return false
	}
	if i == m.tabActive && m.tabs[i] == m.openRel {
		return m.edit.dirty
	}
	return m.draftSet[m.tabs[i]]
}

// closeTabsSide closes the clean tabs strictly left (or right) of the active
// one. Unsaved tabs refuse to close and stay red; the active tab is never a
// candidate by construction.
func (m Model) closeTabsSide(left bool) Model {
	lo, hi := 0, m.tabActive
	if !left {
		lo, hi = m.tabActive+1, len(m.tabs)
	}
	kept := 0
	out := make([]string, 0, len(m.tabs))
	for i, rel := range m.tabs {
		if i >= lo && i < hi {
			if m.draftSet[rel] || (rel == m.openRel && m.edit.dirty) {
				kept++ // unsaved — keep, stays red
			} else {
				delete(m.cursorMem, rel) // closed — drop its remembered cursor
				continue
			}
		}
		out = append(out, rel)
	}
	m.tabs = out
	for i, rel := range m.tabs {
		if rel == m.openRel {
			m.tabActive = i
			break
		}
	}
	m.persistTabs()
	if kept > 0 {
		m.notice = fmt.Sprintf("kept %d unsaved tab(s)", kept)
	}
	return m
}

// tabCommand handles `tab <name|cl|cr>` from the : and @exec bars. ok is false
// when the input was invalid (the caller may stay in its bar).
func (m Model) tabCommand(arg string) (Model, bool) {
	switch arg {
	case "":
		m.errText = "tab needs a name, cl, or cr"
		return m, false
	case "cl", "close-left":
		return m.closeTabsSide(true), true
	case "cr", "close-right":
		return m.closeTabsSide(false), true
	default:
		i, ok := m.findTab(arg)
		if !ok {
			m.errText = "no tab: " + arg
			return m, false
		}
		return m.activateTab(i), true
	}
}
