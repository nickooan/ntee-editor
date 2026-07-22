package app

import (
	"errors"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nickooan/ntee-editor/internal/config"
	"github.com/nickooan/ntee-editor/internal/input"
	"github.com/nickooan/ntee-editor/internal/store"
)

// inspectMenuItems are the left-pane rows of the inspection dashboard, in
// display order. inspectMenu indexes into this list.
var inspectMenuItems = []string{"ntee-db", "lsp"}

const (
	inspectMenuDB = iota
	inspectMenuLSP
)

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
	return m, m.fetchDBInfoCmd()
}

// handleInspectKey drives the inspection dashboard: Shift+↑/↓ move the left
// menu (mirroring the sidebar selection), the rest is the standard command-bar
// input (exec-bar pattern). Esc returns to the previous mode; a busy
// maintenance op keeps running and lands as a notice.
func (m Model) handleInspectKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.mode = m.inspectPrevMode
	case tea.KeyShiftUp:
		m.inspectMenu = input.Clamp(m.inspectMenu-1, 0, len(inspectMenuItems)-1)
	case tea.KeyShiftDown:
		m.inspectMenu = input.Clamp(m.inspectMenu+1, 0, len(inspectMenuItems)-1)
	case tea.KeyEnter:
		return m.runInspectCommand(strings.TrimSpace(m.inspectInput))
	case tea.KeyLeft:
		m.inspectCursor = input.MoveCursor(m.inspectInput, m.inspectCursor, -1)
	case tea.KeyRight:
		m.inspectCursor = input.MoveCursor(m.inspectInput, m.inspectCursor, 1)
	case tea.KeyBackspace:
		m.inspectInput, m.inspectCursor, _ = input.RemoveBeforeCursor(m.inspectInput, m.inspectCursor)
	case tea.KeySpace:
		m.inspectInput, m.inspectCursor = input.InsertAtCursor(m.inspectInput, m.inspectCursor, " ")
	case tea.KeyRunes:
		m.inspectInput, m.inspectCursor = input.InsertAtCursor(m.inspectInput, m.inspectCursor, string(msg.Runes))
	}
	return m, nil
}

// runInspectCommand dispatches "db compact|relieve" and
// "lsp enable|disable <lang|all>". On error it stays in the bar so the user
// can correct the input.
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
	if (verb != "enable" && verb != "disable") || lang == "" {
		m.errText = "usage: lsp enable|disable <lang|all>"
		return m, nil
	}
	enable := verb == "enable"
	langs := m.knownLanguages()
	if lang != "all" {
		if _, ok := m.cfg.Languages[lang]; !ok {
			m.errText = "unknown language: " + lang + " (known: " + strings.Join(langs, ", ") + ")"
			return m, nil
		}
		langs = []string{lang}
	}

	// Persist first, so nothing is half-applied when the write fails. "all"
	// enable also flips the global lsp.enabled flag on, recovering a
	// globally-off config; "all" disable stays per-language so the pane stays
	// informative after restart (the --disable-lsp CLI covers the global off).
	names := append([]string(nil), langs...)
	if enable && lang == "all" {
		names = append(names, "all")
	}
	if _, err := config.SetLanguagesEnabled(names, enable); err != nil {
		m.errText = "config write failed: " + err.Error()
		return m, nil
	}

	// Then apply live. m.cfg is never mutated (its Languages map is shared
	// with server goroutines); the registry owns the runtime state.
	if enable {
		for _, l := range langs {
			if started, reason := m.lsp.Enable(l); !started {
				m.errText = reason
				return m, nil
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
	m.inspectMenu = inspectMenuLSP
	m.inspectInput, m.inspectCursor = "", 0
	return m, nil
}
