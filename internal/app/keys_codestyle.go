package app

import (
	"maps"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/config"
	"github.com/nickooan/ntee-editor/internal/input"
)

// codeStylePickerState is the code-style panel's table once → focuses it.
// index 0 is the editor-wide row, 1…n the config.CodeStyleLanguages rows,
// then one trailing "reset all" row. editing means the highlighted row is a
// number input holding input; confirmReset is the y/n prompt for resetting
// every size.
type codeStylePickerState struct {
	focused      bool
	index        int
	editing      bool
	input        string
	confirmReset bool
}

const codeStyleGlobalRow = 0

// codeStyleResetRow is the picker index of the "reset all to defaults" row.
var codeStyleResetRow = len(config.CodeStyleLanguages) + 1

// codeStyleRowLanguage is the language key of a table row, "" for the
// editor-wide row.
func codeStyleRowLanguage(index int) string {
	if index <= codeStyleGlobalRow || index > len(config.CodeStyleLanguages) {
		return ""
	}
	return config.CodeStyleLanguages[index-1].Key
}

func (m Model) handleCodeStylePickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	picker := &m.codeStylePicker
	if picker.confirmReset {
		switch msg.String() {
		case "y", "enter":
			picker.confirmReset = false
			m = m.applyTabWidths(config.DefaultTabWidth, map[string]int{}, "tab sizes reset to defaults")
		case "n", "esc":
			picker.confirmReset = false
			m.notice = "reset cancelled"
		}
		return m, nil
	}
	if picker.editing {
		switch msg.String() {
		case "esc":
			picker.editing = false
		case "enter":
			picker.editing = false
			size, err := strconv.Atoi(picker.input)
			if err != nil {
				m.errText = "tab size must be a number from " + strconv.Itoa(config.MinTabWidth) + " to " + strconv.Itoa(config.MaxTabWidth)
				return m, nil
			}
			m = m.setTabWidth(codeStyleRowLanguage(picker.index), size)
		case "backspace":
			if picker.input != "" {
				picker.input = picker.input[:len(picker.input)-1]
			}
		default:
			if text := keyText(msg); len(text) == 1 && text[0] >= '0' && text[0] <= '9' && len(picker.input) < 2 {
				picker.input += text
			}
		}
		return m, nil
	}
	switch msg.String() {
	case "up":
		picker.index = input.Clamp(picker.index-1, 0, codeStyleResetRow)
	case "down":
		picker.index = input.Clamp(picker.index+1, 0, codeStyleResetRow)
	case "enter":
		if picker.index == codeStyleResetRow {
			m = m.beginCodeStyleResetAll()
			break
		}
		if refused := m.codeStyleProjectRefusal(codeStyleRowLanguage(picker.index)); refused != "" {
			m.errText = refused
			break
		}
		picker.editing = true
		picker.input = strconv.Itoa(m.codeStyleRowWidth(picker.index))
	case "+", "=":
		if picker.index < codeStyleResetRow {
			m = m.setTabWidth(codeStyleRowLanguage(picker.index), m.codeStyleRowWidth(picker.index)+1)
		}
	case "-":
		if picker.index < codeStyleResetRow {
			m = m.setTabWidth(codeStyleRowLanguage(picker.index), m.codeStyleRowWidth(picker.index)-1)
		}
	case "r":
		if picker.index < codeStyleResetRow {
			m = m.resetTabWidth(codeStyleRowLanguage(picker.index))
		}
	case "R":
		m = m.beginCodeStyleResetAll()
	case "left", "esc":
		picker.focused = false
	}
	return m, nil
}

// codeStyleRowWidth is the size a row shows: the editor-wide one, or the
// language's own size, else the inherited editor-wide one.
func (m Model) codeStyleRowWidth(index int) int {
	if language := codeStyleRowLanguage(index); language != "" {
		if width, ok := m.cfg.Editor.LanguageTabWidths[language]; ok {
			return width
		}
	}
	return m.cfg.Editor.TabWidth
}

// codeStyleProjectRefusal is why a size can't be changed here: the project's
// .ntee-editor.yaml sets it, and that file wins over the user config.
func (m Model) codeStyleProjectRefusal(language string) string {
	project := m.cfg.ProjectCodeStyle
	if (language == "" && project.TabWidth) || (language != "" && project.Languages[language]) {
		return codeStyleRowName(language) + " is set in .ntee-editor.yaml — change it there"
	}
	return ""
}

func codeStyleRowName(language string) string {
	if language == "" {
		return "all files"
	}
	return language
}

// setTabWidth sets the editor-wide size (language "") or a language's own.
func (m Model) setTabWidth(language string, size int) Model {
	if refused := m.codeStyleProjectRefusal(language); refused != "" {
		m.errText = refused
		return m
	}
	if size < config.MinTabWidth || size > config.MaxTabWidth {
		m.errText = "tab size must be from " + strconv.Itoa(config.MinTabWidth) + " to " + strconv.Itoa(config.MaxTabWidth)
		return m
	}
	global, languages := m.cfg.Editor.TabWidth, maps.Clone(m.cfg.Editor.LanguageTabWidths)
	if languages == nil {
		languages = map[string]int{}
	}
	if language == "" {
		global = size
	} else {
		languages[language] = size
	}
	return m.applyTabWidths(global, languages, codeStyleRowName(language)+" → "+strconv.Itoa(size))
}

// resetTabWidth puts the editor-wide size back to the default, or lets a
// language inherit it again.
func (m Model) resetTabWidth(language string) Model {
	if refused := m.codeStyleProjectRefusal(language); refused != "" {
		m.errText = refused
		return m
	}
	if language == "" {
		if m.cfg.Editor.TabWidth == config.DefaultTabWidth {
			m.notice = "all files is already the default " + strconv.Itoa(config.DefaultTabWidth)
			return m
		}
		return m.applyTabWidths(config.DefaultTabWidth, m.cfg.Editor.LanguageTabWidths, "all files reset to "+strconv.Itoa(config.DefaultTabWidth))
	}
	if _, custom := m.cfg.Editor.LanguageTabWidths[language]; !custom {
		m.notice = language + " already uses all files' size"
		return m
	}
	languages := maps.Clone(m.cfg.Editor.LanguageTabWidths)
	delete(languages, language)
	return m.applyTabWidths(m.cfg.Editor.TabWidth, languages, language+" now uses all files' size")
}

// codeStyleCustomCount is how many sizes differ from the defaults.
func (m Model) codeStyleCustomCount() int {
	count := len(m.cfg.Editor.LanguageTabWidths)
	if m.cfg.Editor.TabWidth != config.DefaultTabWidth {
		count++
	}
	return count
}

// beginCodeStyleResetAll arms the reset-all confirmation, or says there is
// nothing to reset.
func (m Model) beginCodeStyleResetAll() Model {
	if m.codeStyleCustomCount() == 0 {
		m.notice = "tab sizes already at defaults"
		return m
	}
	m.codeStylePicker.index = codeStyleResetRow
	m.codeStylePicker.confirmReset = true
	return m
}

// applyTabWidths persists the sizes first (nothing is half-applied when the
// config write fails), then switches the live config; the next frame draws
// tabs at the new sizes.
func (m Model) applyTabWidths(global int, languages map[string]int, notice string) Model {
	if _, err := config.SetTabWidths(global, languages); err != nil {
		m.errText = "config write failed: " + err.Error()
		return m
	}
	// Replaced wholesale, never mutated: cfg is copied by value and the map
	// would otherwise be shared between copies.
	m.cfg.Editor.TabWidth = global
	m.cfg.Editor.LanguageTabWidths = maps.Clone(languages)
	m.notice = notice + " (config updated)"
	return m
}

// inspectTabCommand handles the bar forms "tab <n>", "tab <language>
// <n|default>", and "tab reset" (typing the command out is the confirmation,
// so reset-all runs immediately).
func (m Model) inspectTabCommand(args string) (tea.Model, tea.Cmd) {
	fields := strings.Fields(args)
	usage := "usage: tab <size> · tab <language> <size|default> · tab reset"
	switch {
	case len(fields) == 1 && fields[0] == "reset":
		if m.codeStyleCustomCount() == 0 {
			m.notice = "tab sizes already at defaults"
		} else {
			m = m.applyTabWidths(config.DefaultTabWidth, map[string]int{}, "tab sizes reset to defaults")
		}
	case len(fields) == 1:
		size, err := strconv.Atoi(fields[0])
		if err != nil {
			m.errText = usage
			return m, nil
		}
		m = m.setTabWidth("", size)
	case len(fields) == 2:
		language := strings.ToLower(fields[0])
		if !config.IsCodeStyleLanguage(language) {
			m.errText = "unknown language " + fields[0] + " — see the code style pane"
			return m, nil
		}
		if fields[1] == "default" {
			m = m.resetTabWidth(language)
			break
		}
		size, err := strconv.Atoi(fields[1])
		if err != nil {
			m.errText = usage
			return m, nil
		}
		m = m.setTabWidth(language, size)
	default:
		m.errText = usage
		return m, nil
	}
	m.inspectMenu = inspectMenuCodeStyle
	if m.errText == "" {
		m.inspectInput, m.inspectCursor = "", 0
	}
	return m, nil
}
