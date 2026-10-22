package app

import (
	"errors"
	"slices"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/config"
	"github.com/nickooan/ntee-editor/internal/input"
	"github.com/nickooan/ntee-editor/internal/lsp"
	"github.com/nickooan/ntee-editor/internal/store"
	"github.com/nickooan/ntee-editor/internal/syntax"
	"github.com/nickooan/ntee-editor/internal/view"
)

// inspectMenuItems are the left-pane rows of the inspection dashboard, in
// display order. inspectMenu indexes into this list.
var inspectMenuItems = []string{"ntee-db", "lsp", "system", "op-commands", "keybinds", "external"}

const (
	inspectMenuDB = iota
	inspectMenuLSP
	inspectMenuSystem
	inspectMenuOpCommands
	inspectMenuKeybinds
	inspectMenuExternal
)

// syntaxStyles is the curated set of chroma styles offered by `syscolor`.
// syntax.SetStyle silently falls back to gruvbox for unknown names, so
// validation happens here, against this list.
var syntaxStyles = []string{"gruvbox", "monokai", "dracula", "nord", "solarized-dark", "github-dark"}

type inspectStatsMsg struct {
	info store.DBInfo
	err  error
}

type inspectMaintMsg struct {
	op  string // "compact" | "relieve"
	err error
}

// fetchDBInfoCmd gathers store statistics off the UI goroutine (BlobUsage
// does per-record preads).
func (m Model) fetchDBInfoCmd() tea.Cmd {
	db := m.db
	return func() tea.Msg {
		info, err := db.Maintenance()
		return inspectStatsMsg{info: info, err: err}
	}
}

// maintCmd runs a maintenance op in the background. If the user quits while it
// runs, db.Close makes the op error out and the msg dies with the program.
func (m Model) maintCmd(op string) tea.Cmd {
	db := m.db
	return func() tea.Msg {
		var err error
		if op == "compact" {
			err = db.Compact()
		} else {
			err = db.RelieveBlobs()
		}
		return inspectMaintMsg{op: op, err: err}
	}
}

// enterInspect opens the inspection dashboard (Ctrl+T), remembering the mode
// to return to, and kicks off a fresh stats fetch.
func (m Model) enterInspect() (tea.Model, tea.Cmd) {
	m.inspectPrevMode = m.mode
	m.mode = modeInspect
	m.inspectMenu = inspectMenuDB
	m.inspectInput, m.inspectCursor = "", 0
	m.inspectLoading = true
	m.opTable = opTableState{}
	m.stylePicker = stylePickerState{}
	m.keybindPicker = keybindPickerState{}
	m.externalPicker = externalPickerState{}
	m, loadOpCommands := m.loadOpCommandsCmd()
	m, loadHistory := m.loadExternalHistoryCmd()
	return m, tea.Batch(m.fetchDBInfoCmd(), loadOpCommands, m.loadExternalSettingsCmd(), loadHistory)
}

// handleInspectKey drives the inspection dashboard: Shift+↑/↓ move the left
// menu (mirroring the sidebar selection), the rest is the standard command-bar
// input (exec-bar pattern). On op-commands, → at the end of the bar input
// focuses the table, which then owns the keys. Esc returns to the previous
// mode; a busy maintenance op keeps running and lands as a notice.
func (m Model) handleInspectKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.opTable.focused {
		return m.handleOpTableKey(msg)
	}
	if m.stylePicker.focused {
		return m.handleStylePickerKey(msg)
	}
	if m.lspPicker.focused {
		return m.handleLSPPickerKey(msg)
	}
	if m.keybindPicker.focused {
		return m.handleKeybindPickerKey(msg)
	}
	if m.externalPicker.focused {
		return m.handleExternalPickerKey(msg)
	}
	switch msg.String() {
	case "esc":
		m.mode = m.inspectPrevMode
	case "shift+up":
		m.inspectMenu = input.Clamp(m.inspectMenu-1, 0, len(inspectMenuItems)-1)
	case "shift+down":
		m.inspectMenu = input.Clamp(m.inspectMenu+1, 0, len(inspectMenuItems)-1)
	case "enter":
		return m.runInspectCommand(strings.TrimSpace(m.inspectInput))
	case "left":
		m.inspectCursor = input.MoveCursor(m.inspectInput, m.inspectCursor, -1)
	case "right":
		atInputEnd := m.inspectCursor >= len([]rune(m.inspectInput))
		if m.inspectMenu == inspectMenuOpCommands && atInputEnd {
			m.opTable.focused = true
			m.opTable.index = input.Clamp(m.opTable.index, 0, len(m.opCommands))
			break
		}
		if m.inspectMenu == inspectMenuSystem && atInputEnd {
			m = m.focusStylePicker()
			break
		}
		if m.inspectMenu == inspectMenuLSP && atInputEnd {
			m = m.focusLSPPicker()
			break
		}
		if m.inspectMenu == inspectMenuKeybinds && atInputEnd {
			m.keybindPicker.focused = true
			break
		}
		if m.inspectMenu == inspectMenuExternal && atInputEnd {
			m.externalPicker.focused = true
			break
		}
		m.inspectCursor = input.MoveCursor(m.inspectInput, m.inspectCursor, 1)
	case "backspace":
		m.inspectInput, m.inspectCursor, _ = input.RemoveBeforeCursor(m.inspectInput, m.inspectCursor)
	case "space":
		m.inspectInput, m.inspectCursor = input.InsertAtCursor(m.inspectInput, m.inspectCursor, " ")
	default:
		if t := keyText(msg); t != "" {
			m.inspectInput, m.inspectCursor = input.InsertAtCursor(m.inspectInput, m.inspectCursor, t)
		}
	}
	return m, nil
}

// renderInspectInput draws the @inspection bar input; it drops its cursor while
// the op-commands table has focus, so only the table shows where typing goes.
func (m Model) renderInspectInput() string {
	if m.opTable.focused || m.stylePicker.focused || m.lspPicker.focused || m.keybindPicker.focused || m.externalPicker.focused {
		return execTextStyle.Render(m.inspectInput)
	}
	return renderInputLineStyled(m.inspectInput, m.inspectCursor, execTextStyle)
}

// inspectHint is the status-bar key hint for the inspect dashboard's focus.
func (m Model) inspectHint() string {
	switch {
	case m.opTable.confirmDelete != "":
		return "y delete · n cancel"
	case m.opTable.editing:
		if _, candidates := m.opSuggestions(); len(candidates) > 0 {
			return "↑/↓ choose · Tab/Enter insert · Esc dismiss"
		}
		return "Tab key/value · Ctrl+S save · Esc revert"
	case m.opTable.focused:
		return "↑/↓ row · Enter edit · d delete · ←/Esc back"
	case m.inspectMenu == inspectMenuOpCommands:
		return "→ edit op-commands · Shift+↑/↓ pane · Esc back"
	case m.stylePicker.focused:
		return "↑/↓ style · Enter apply · ←/Esc back"
	case m.inspectMenu == inspectMenuSystem:
		return "→ choose a color style · syscolor <style> · Shift+↑/↓ pane · Esc back"
	case m.lspPicker.menuOpen:
		return "↑/↓ action · Enter apply · Esc close"
	case m.lspPicker.focused:
		return "↑/↓ language · Enter actions · ←/Esc back"
	case m.inspectMenu == inspectMenuLSP:
		return "→ manage a server · lsp start|stop|restart|enable|disable <lang|all> · Shift+↑/↓ pane · Esc back"
	case m.keybindPicker.confirmReset:
		return "y reset all · n cancel"
	case m.keybindPicker.capturing:
		return "press the new key chord · Esc cancel · Ctrl+C quits"
	case m.keybindPicker.focused:
		return "↑/↓ action · Enter rebind · r reset · R reset all · ← back"
	case m.inspectMenu == inspectMenuKeybinds:
		return "→ rebind keys · keybind <action> <key|default> · keybind reset · Shift+↑/↓ pane · Esc back"
	case m.externalPicker.editingLimit:
		return "type a number · Enter save · Esc cancel"
	case m.externalPicker.focused:
		return "↑/↓ setting · Enter change · ← back"
	case m.inspectMenu == inspectMenuExternal:
		return "→ change settings · ext on|off · ext limit <n> · ext history · Shift+↑/↓ pane · Esc back"
	}
	return "db compact|relieve · lsp start|stop|enable|disable <lang|all> · syscolor <style> · keybind <action> <key> · Shift+↑/↓ pane · Esc back"
}

// runInspectCommand dispatches "db compact|relieve",
// "lsp enable|disable <lang|all>", "syscolor <style>", and "keybind …". On error it stays
// in the bar so the user can correct the input.
func (m Model) runInspectCommand(cmd string) (tea.Model, tea.Cmd) {
	ns, rest, _ := strings.Cut(cmd, " ")
	rest = strings.TrimSpace(rest)
	switch ns {
	case "":
		return m, nil
	case "db":
		return m.inspectDBCommand(rest)
	case "lsp":
		return m.inspectLSPCommand(rest)
	case "syscolor":
		return m.inspectSyscolorCommand(rest)
	case "keybind":
		return m.inspectKeybindCommand(rest)
	case "ext":
		return m.inspectExternalCommand(rest)
	default:
		m.errText = "unknown command: " + ns
		return m, nil
	}
}

func (m Model) inspectDBCommand(verb string) (tea.Model, tea.Cmd) {
	switch verb {
	case "compact", "relieve":
	case "":
		m.errText = "db needs compact or relieve"
		return m, nil
	default:
		m.errText = "unknown db command: " + verb
		return m, nil
	}
	if m.inspectBusy != "" {
		m.errText = "db " + m.inspectBusy + " already running"
		return m, nil
	}
	if errors.Is(m.inspectInfoErr, store.ErrNoStats) {
		m.errText = "in-memory store — maintenance unavailable"
		return m, nil
	}
	m.inspectBusy = verb
	m.inspectMenu = inspectMenuDB
	m.inspectInput, m.inspectCursor = "", 0
	m.notice = "db " + verb + " started"
	return m, m.maintCmd(verb)
}

// knownLanguages returns the configured language names, sorted.
func (m Model) knownLanguages() []string {
	langs := make([]string, 0, len(m.cfg.Languages))
	for lang := range m.cfg.Languages {
		langs = append(langs, lang)
	}
	sort.Strings(langs)
	return langs
}

func (m Model) inspectLSPCommand(rest string) (tea.Model, tea.Cmd) {
	verb, lang, _ := strings.Cut(rest, " ")
	lang = strings.ToLower(strings.TrimSpace(lang))
	if !slices.Contains(lspVerbs, verb) || lang == "" {
		m.errText = "usage: lsp " + strings.Join(lspVerbs, "|") + " <lang|all>"
		return m, nil
	}
	langs := m.knownLanguages()
	if lang != "all" {
		if _, ok := m.cfg.Languages[lang]; !ok {
			m.errText = "unknown language: " + lang + " (known: " + strings.Join(langs, ", ") + ")"
			return m, nil
		}
		langs = []string{lang}
	}
	applied := true
	switch verb {
	case lspVerbEnable, lspVerbDisable:
		m, applied = m.setLSPEnabled(langs, verb == lspVerbEnable, lang == "all")
	default:
		for _, each := range langs {
			if m, applied = m.applyLSPAction(each, verb); !applied {
				break
			}
		}
		if applied && len(langs) > 1 {
			m.notice = "lsp " + verb + "ed: " + strings.Join(langs, ", ") + " (this session)"
			if verb == lspVerbStop {
				m.notice = "lsp stopped: " + strings.Join(langs, ", ") + " (this session)"
			}
		}
	}
	if applied {
		m.inspectMenu = inspectMenuLSP
		m.inspectInput, m.inspectCursor = "", 0
	}
	return m, nil
}

// setLSPEnabled is what `lsp enable|disable` and the lsp picker both do: persist
// the languages' enable flag, then start or stop their servers live. all marks
// the "lsp enable all" form, which also turns the global flag back on.
// applied is false when the config write or a server start failed (errText
// says why).
func (m Model) setLSPEnabled(langs []string, enable, all bool) (Model, bool) {
	// Persist first, so nothing is half-applied when the write fails. "all"
	// enable also flips the global lsp.enabled flag on, recovering a
	// globally-off config; "all" disable stays per-language so the pane stays
	// informative after restart (the --disable-lsp CLI covers the global off).
	names := append([]string(nil), langs...)
	if enable && all {
		names = append(names, "all")
	}
	if _, err := config.SetLanguagesEnabled(names, enable); err != nil {
		m.errText = "config write failed: " + err.Error()
		return m, false
	}

	// Then apply live. m.cfg is never mutated (its Languages map is shared
	// with server goroutines); the registry owns the runtime state.
	if enable {
		for _, l := range langs {
			if started, reason := m.lsp.Enable(l); !started {
				m.errText = reason
				return m, false
			}
		}
		m.notice = "lsp enabled: " + strings.Join(langs, ", ") + " (config updated)"
	} else {
		for _, l := range langs {
			m.lsp.Disable(l)
		}
		m.notice = "lsp disabled: " + strings.Join(langs, ", ") + " (config updated)"
		if !m.cfg.LSP.Enabled {
			m.notice += " — restart to apply"
		}
	}
	return m, true
}

// inspectSyscolorCommand validates, persists, and live-applies a syntax color
// style (same persist-first order as the lsp command).
func (m Model) inspectSyscolorCommand(name string) (tea.Model, tea.Cmd) {
	name = strings.ToLower(strings.TrimSpace(name))
	if !slices.Contains(syntaxStyles, name) {
		m.errText = "usage: syscolor <" + strings.Join(syntaxStyles, "|") + ">"
		return m, nil
	}
	m = m.applySyntaxStyle(name)
	m.inspectMenu = inspectMenuSystem
	if m.errText == "" {
		m.inspectInput, m.inspectCursor = "", 0
	}
	return m, nil
}

// applySyntaxStyle persists the grammar color style first (nothing is
// half-applied when the config write fails), then switches it live.
func (m Model) applySyntaxStyle(name string) Model {
	if _, err := config.SetThemeSyntax(name); err != nil {
		m.errText = "config write failed: " + err.Error()
		return m
	}
	syntax.SetStyle(name) // also resets the syntax package's entry cache
	// Theme is an unshared value field — safe to mutate, unlike the Languages
	// map (see inspectLSPCommand).
	m.cfg.Theme.Syntax = name
	m = m.invalidateHighlightCaches()
	m.notice = "syntax style: " + name + " (config updated)"
	return m
}

// stylePickerState is the system panel's color-style list once → focuses it:
// index is the highlighted style and preview its sample, highlighted in that
// style without touching the active one.
type stylePickerState struct {
	focused bool
	index   int
	preview [][]view.HighlightSegment
}

// stylePreviewSample is the snippet the picker colors: comment, keywords, a
// function, a type, a string, and a number.
const stylePreviewSample = `// greet returns a friendly message.
func greet(name string) string {
    return fmt.Sprintf("hello, %s #%d", name, 42)
}`

func (m Model) focusStylePicker() Model {
	m.stylePicker.focused = true
	m.stylePicker.index = max(0, slices.Index(syntaxStyles, m.cfg.Theme.Syntax))
	return m.refreshStylePreview()
}

// refreshStylePreview re-highlights the sample in the highlighted style. It
// runs only when the selection moves, and the sample is a few lines.
func (m Model) refreshStylePreview() Model {
	m.stylePicker.preview = syntax.HighlightLinesWithStyle("sample.go", stylePreviewSample, syntaxStyles[m.stylePicker.index])
	return m
}

func (m Model) handleStylePickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up":
		m.stylePicker.index = input.Clamp(m.stylePicker.index-1, 0, len(syntaxStyles)-1)
		m = m.refreshStylePreview()
	case "down":
		m.stylePicker.index = input.Clamp(m.stylePicker.index+1, 0, len(syntaxStyles)-1)
		m = m.refreshStylePreview()
	case "enter":
		m = m.applySyntaxStyle(syntaxStyles[m.stylePicker.index])
	case "left", "esc":
		m.stylePicker.focused = false
	}
	return m, nil
}

// The lsp verbs. start, stop and restart act on this session only and leave
// the config alone; enable and disable also persist the language's enable
// flag (see setLSPEnabled).
const (
	lspVerbStart   = "start"
	lspVerbStop    = "stop"
	lspVerbRestart = "restart"
	lspVerbEnable  = "enable"
	lspVerbDisable = "disable"
)

var lspVerbs = []string{lspVerbStart, lspVerbStop, lspVerbRestart, lspVerbEnable, lspVerbDisable}

// lspAction is one entry of the lsp picker's dropdown.
type lspAction struct{ verb, label, detail string }

var (
	lspStartAction   = lspAction{lspVerbStart, "▶ start", "start now · this session only"}
	lspStopAction    = lspAction{lspVerbStop, "■ stop", "stop now · starts again when needed"}
	lspRestartAction = lspAction{lspVerbRestart, "↻ restart", "stop and start now"}
	lspEnableAction  = lspAction{lspVerbEnable, "✓ enable", "start · save enable: true"}
	lspDisableAction = lspAction{lspVerbDisable, "⊘ disable", "stop · save enable: false"}
)

// lspActionsFor is the dropdown for a server state, most likely action first.
func lspActionsFor(state lsp.LangState) []lspAction {
	switch state {
	case lsp.LangRunning:
		return []lspAction{lspStopAction, lspRestartAction, lspDisableAction}
	case lsp.LangStopped:
		return []lspAction{lspStartAction, lspDisableAction}
	}
	return []lspAction{lspStartAction, lspEnableAction}
}

// applyLSPAction runs one verb for one language. The session verbs never
// touch the config; enable/disable go through setLSPEnabled. applied is false
// when a server failed to start (errText says why).
func (m Model) applyLSPAction(lang, verb string) (Model, bool) {
	switch verb {
	case lspVerbEnable, lspVerbDisable:
		return m.setLSPEnabled([]string{lang}, verb == lspVerbEnable, false)
	case lspVerbStop:
		m.lsp.Stop(lang)
		m.notice = "lsp stopped: " + lang + " (starts again when a " + lang + " file needs it)"
		return m, true
	case lspVerbRestart:
		m.lsp.Stop(lang)
	}
	if started, reason := m.lsp.Enable(lang); !started {
		m.errText = reason
		return m, false
	}
	if verb == lspVerbRestart {
		m.notice = "lsp restarted: " + lang
	} else {
		m.notice = "lsp started: " + lang + " (this session)"
	}
	return m, true
}

// lspPickerState is the lsp panel's language list once → focuses it: index is
// the highlighted language; Enter opens the dropdown of lspActionsFor its
// state (menuOpen) whose highlighted entry is action.
type lspPickerState struct {
	focused  bool
	index    int
	menuOpen bool
	action   int
}

// focusLSPPicker moves focus into the language list. With LSP globally off
// there is nothing to pick, and the panel already says why.
func (m Model) focusLSPPicker() Model {
	if len(m.lsp.Statuses()) == 0 {
		return m
	}
	m.lspPicker = lspPickerState{focused: true, index: m.lspPicker.index}
	return m
}

// handleLSPPickerKey drives the language list and its run/stop dropdown.
// Statuses are re-read on every key: a server can start or exit at any time.
func (m Model) handleLSPPickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	statuses := m.lsp.Statuses()
	if len(statuses) == 0 {
		m.lspPicker = lspPickerState{}
		return m, nil
	}
	picker := &m.lspPicker
	picker.index = input.Clamp(picker.index, 0, len(statuses)-1)
	status := statuses[picker.index]
	if picker.menuOpen {
		actions := lspActionsFor(status.State)
		picker.action = input.Clamp(picker.action, 0, len(actions)-1)
		switch msg.String() {
		case "up":
			picker.action = input.Clamp(picker.action-1, 0, len(actions)-1)
		case "down":
			picker.action = input.Clamp(picker.action+1, 0, len(actions)-1)
		case "enter":
			picker.menuOpen = false
			m, _ = m.applyLSPAction(status.Lang, actions[picker.action].verb)
		case "left", "esc":
			picker.menuOpen = false
		}
		return m, nil
	}
	switch msg.String() {
	case "up":
		picker.index = input.Clamp(picker.index-1, 0, len(statuses)-1)
	case "down":
		picker.index = input.Clamp(picker.index+1, 0, len(statuses)-1)
	case "enter":
		picker.menuOpen, picker.action = true, 0
	case "left", "esc":
		picker.focused = false
	}
	return m, nil
}
