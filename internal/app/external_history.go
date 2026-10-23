package app

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/extmsg"
	"github.com/nickooan/ntee-editor/internal/input"
	"github.com/nickooan/ntee-editor/internal/keymap"
	"github.com/nickooan/ntee-editor/internal/store"
	"github.com/nickooan/ntee-editor/internal/view"
)

// externalHistoryState is the message history view (Ctrl+T › external ›
// history): the project's saved requests on the left, the selected one's
// details on the right. It is the review mode's two-pane layout over records
// instead of files.
type externalHistoryState struct {
	records       []store.ExternalRecord
	selection     listSelection
	detail        rowCursor // scroll of the detail pane
	detailFocused bool      // → moved focus to the detail pane: ↑/↓ scroll it
	loading       bool
	err           error
}

// externalHistoryLoadedMsg delivers the history; gen guards against a load
// that a newer one superseded.
type externalHistoryLoadedMsg struct {
	gen     int
	records []store.ExternalRecord
	err     error
}

// loadExternalHistoryCmd reads the history off the UI goroutine. Both the
// Ctrl+T panel (its saved count) and the history view consume the result.
func (m Model) loadExternalHistoryCmd() (Model, tea.Cmd) {
	if m.external.historyStore == nil {
		return m, nil
	}
	m.externalHistoryGen++
	gen, historyStore := m.externalHistoryGen, m.external.historyStore
	return m, func() tea.Msg {
		records, err := historyStore.LoadExternal()
		return externalHistoryLoadedMsg{gen: gen, records: records, err: err}
	}
}

func (m Model) handleExternalHistoryLoaded(msg externalHistoryLoadedMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.externalHistoryGen {
		return m, nil
	}
	m.external.savedCount = len(msg.records)
	selectedKey := ""
	if index := m.externalHistory.selection.index; index < len(m.externalHistory.records) {
		selectedKey = m.externalHistory.records[index].Key
	}
	m.externalHistory.records = msg.records
	m.externalHistory.loading = false
	m.externalHistory.err = msg.err
	// Keep the selection on the same record when it survived the reload.
	m.externalHistory.selection = listSelection{}
	for i, record := range msg.records {
		if record.Key == selectedKey {
			m.externalHistory.selection.index = i
		}
	}
	return m, nil
}

// enterExternalHistory opens the history view over the inspection dashboard.
func (m Model) enterExternalHistory() (Model, tea.Cmd) {
	m.externalHistory = externalHistoryState{loading: true}
	m.mode = modeExternalHistory
	return m.loadExternalHistoryCmd()
}

// exitExternalHistory returns to the dashboard's external section.
func (m Model) exitExternalHistory() Model {
	m.externalHistory = externalHistoryState{}
	m.mode = modeInspect
	m.inspectMenu = inspectMenuExternal
	return m
}

// moveExternalSelection picks the message delta rows away and shows its
// details from the top; the focused pane stays focused.
func (m Model) moveExternalSelection(delta int) Model {
	history := &m.externalHistory
	moved := history.selection.moved(delta, len(history.records))
	if moved != history.selection {
		history.selection, history.detail = moved, rowCursor{}
	}
	return m
}

func (m Model) selectedExternalRecord() (store.ExternalRecord, bool) {
	records := m.externalHistory.records
	if len(records) == 0 {
		return store.ExternalRecord{}, false
	}
	return records[input.Clamp(m.externalHistory.selection.index, 0, len(records)-1)], true
}

// handleExternalHistoryKey: with the list focused ↑/↓ pick a message and →
// moves into its details; with the details focused ↑/↓ scroll them and ← or
// Esc go back to the list. Shift+↑/↓ pick a message, PgUp/PgDn/Home/End
// scroll the details, and Enter replays, from either pane.
func (m Model) handleExternalHistoryKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	history := &m.externalHistory
	k := msg.String()
	if history.detailFocused {
		switch k {
		case "up":
			history.detail = history.detail.scrolledBy(-1, m.externalDetailSpan())
			return m, nil
		case "down":
			history.detail = history.detail.scrolledBy(1, m.externalDetailSpan())
			return m, nil
		case "left", "esc":
			history.detailFocused = false
			return m, nil
		}
	}
	switch k {
	case "esc":
		return m.exitExternalHistory(), nil
	case "right":
		history.detailFocused = len(history.records) > 0
	case "up", "shift+up":
		m = m.moveExternalSelection(-1)
	case "down", "shift+down":
		m = m.moveExternalSelection(1)
	case "pgup":
		m.externalHistory.detail = m.externalHistory.detail.scrolled(-1, m.externalDetailSpan())
	case "pgdown":
		m.externalHistory.detail = m.externalHistory.detail.scrolled(1, m.externalDetailSpan())
	case "home":
		m.externalHistory.detail = rowCursor{}
	case "end":
		span := m.externalDetailSpan()
		m.externalHistory.detail.scrollY = max(0, span.total-span.height)
	case "enter":
		return m.replayExternalRecord()
	}
	if m.keys.Matches(keymap.Inspect, k) {
		return m.exitExternalHistory(), nil
	}
	return m, nil
}

// replayExternalRecord runs the selected message again through the same
// path as a fresh one — from this view the user is busy, so it confirms
// first. A replay never rewrites the stored record.
func (m Model) replayExternalRecord() (tea.Model, tea.Cmd) {
	record, ok := m.selectedExternalRecord()
	if !ok {
		return m, nil
	}
	return m.handleExternalMessage(externalMessageMsg{record: record, replay: true})
}

// activateExternalHistoryRow applies a click on a history row.
func (m Model) activateExternalHistoryRow(rowIndex int) (Model, bool) {
	if rowIndex < 0 || rowIndex >= len(m.externalHistory.records) {
		return m, false
	}
	m.externalHistory.selection.index = rowIndex
	m.externalHistory.detail = rowCursor{}
	m.externalHistory.detailFocused = false
	return m, true
}

// externalStatusColor is a record's status color (badge and status text).
func externalStatusColor(status store.ExternalStatus) string {
	switch status {
	case store.ExternalOpened, store.ExternalReviewed:
		return "#b8bb26"
	case store.ExternalPending:
		return "#fabd2f"
	case store.ExternalFailed:
		return "#fb4934"
	default:
		return "#928374"
	}
}

func (m Model) externalHistorySidebarList() sidebarList {
	records := m.externalHistory.records
	if len(records) == 0 {
		label := "no messages yet"
		if m.externalHistory.loading {
			label = "loading…"
		}
		return sidebarList{rows: []sidebarRow{sidebarHeaderRow(label)}, selectedIndex: -1}
	}
	rows := make([]sidebarRow, len(records))
	today := time.Now().Local().Format("2006-01-02")
	for i, record := range records {
		// The sidebar is narrow: today's messages show only the time.
		received := record.ReceivedAt.Local()
		tail := received.Format("01-02 15:04")
		if received.Format("2006-01-02") == today {
			tail = received.Format("15:04:05")
		}
		if record.Command == extmsg.CommandReview {
			tail += fmt.Sprintf(" (%d)", len(record.Paths))
		}
		rows[i] = sidebarBadgeRow("  ", record.Command, len("review")+1, externalStatusColor(record.Status), tail, fileStyle)
	}
	return sidebarList{rows: rows, selectedIndex: m.externalHistory.selection.index}
}

// externalDetailLines renders the selected record's detail panel in full;
// the view windows it by the detail cursor.
func (m Model) externalDetailLines(width int) []string {
	record, ok := m.selectedExternalRecord()
	panel := infoPanel{
		title:       "external message",
		subtitle:    "A request another program sent to this editor. Enter replays it.",
		keyHeader:   "FIELD",
		valueHeader: "VALUE",
		titleHint:   "press → to scroll",
	}
	if m.externalHistory.detailFocused {
		panel.titleHint = "▸ scrolling · ← back to the list"
	}
	switch {
	case m.externalHistory.err != nil:
		panel.message = []string{"Could not read the history: " + m.externalHistory.err.Error()}
	case m.externalHistory.loading:
		panel.message = []string{"Loading the message history…"}
	case !ok:
		panel.message = []string{"No messages yet. Senders use: ntee --msg open <file>#L<n> · ntee --msg review <files…>"}
	default:
		statusStyle := segStyleFor(view.HighlightSegment{Color: externalStatusColor(record.Status), Bold: true})
		status := statusStyle.Render(string(record.Status))
		if record.Note != "" {
			status += panelDimStyle.Render("  " + record.Note)
		}
		base := panelStrongStyle.Render(record.Base)
		if record.Base == store.ExternalDefaultBase {
			base = panelDimStyle.Render(record.Base)
		}
		panel.rows = []panelRow{
			{label: "command", pieces: []string{panelCommandStyle.Render(record.Command) +
				panelDimStyle.Render(fmt.Sprintf(" · %d %s", len(record.Paths), pluralize("file", len(record.Paths))))}},
			{label: "git hash", pieces: []string{base + panelDimStyle.Render("  compared with the current files")}},
			{label: "received", pieces: []string{panelStrongStyle.Render(record.ReceivedAt.Local().Format("2006-01-02 15:04:05"))}},
			{label: "status", pieces: []string{status}},
		}
		paths := make([]string, len(record.Paths))
		for i, path := range record.Paths {
			paths[i] = panelDimStyle.Render(fmt.Sprintf("%3d ", i+1)) + panelLabelStyle.Render(path)
		}
		panel.rows = append(panel.rows, panelRow{label: "paths", pieces: paths, stacked: true})
		panel.after = append(panel.after, panelHeaderStyle.Render("RAW MESSAGE"))
		for _, line := range strings.Split(strings.TrimRight(record.Raw, "\n"), "\n") {
			panel.after = append(panel.after, panelDimStyle.Render(truncateRunes("  "+line, width)))
		}
	}
	// Rendered at full length; the history view scrolls it.
	return strings.Split(panel.render(width, 1<<16), "\n")
}

// externalDetailSpan is the detail pane's scroll span: the panel's lines in
// the main pane's height (no tab strip in this view).
func (m Model) externalDetailSpan() rowSpan {
	width := max(3, m.width-m.sidebarWidth()) - 4
	return rowSpan{total: len(m.externalDetailLines(width)), height: max(1, m.bodyHeight()-2)}
}

// renderExternalHistory draws the detail pane, windowed by its scroll.
func (m Model) renderExternalHistory(width, height int) string {
	lines := m.externalDetailLines(width)
	start := input.Clamp(m.externalHistory.detail.scrollY, 0, max(0, len(lines)-height))
	end := min(start+height, len(lines))
	return strings.Join(lines[start:end], "\n")
}

func (m Model) renderExternalHistoryStatus() string {
	line := promptStyle.Render("@external history")
	if count := len(m.externalHistory.records); count > 0 {
		line += statusTextStyle.Render(fmt.Sprintf(" %d/%d", m.externalHistory.selection.index+1, count))
	}
	hint := "↑/↓ message · → details · Enter replay · Esc back"
	if m.externalHistory.detailFocused {
		span := m.externalDetailSpan()
		top := m.externalHistory.detail.scrollY
		line += statusTextStyle.Render(fmt.Sprintf("  lines %d–%d of %d", top+1, min(top+span.height, span.total), span.total))
		hint = "↑/↓ scroll · PgUp/PgDn page · Shift+↑/↓ message · ← list · Enter replay"
	}
	return withNotice(m, line) + statusTextStyle.Render("   ") + hintStyle.Render(hint)
}

func pluralize(noun string, count int) string {
	if count == 1 {
		return noun
	}
	return noun + "s"
}
