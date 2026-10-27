package app

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/nickooan/ntee-editor/internal/input"
)

// choiceOverlayKind says which decision an open choice overlay is asking
// for; the zero value means no overlay is open.
type choiceOverlayKind uint8

const (
	choiceNone              choiceOverlayKind = iota
	choiceRemovePath                          // :rm confirmation — payload in confirmRm/confirmRmDir
	choiceExternalRequest                     // an external open/review request — payload in external.pending
	choiceQuitReviewSession                   // leaving a live review session — the agent is told to stop
)

// overlayLineStyle is how one body line of a choice overlay is drawn.
type overlayLineStyle uint8

const (
	overlayLinePlain overlayLineStyle = iota
	overlayLineStrong
	overlayLineDim
	overlayLineHighlight // the line the user must not miss (a repo switch)
)

type overlayLine struct {
	text  string
	style overlayLineStyle
}

// choiceOverlay is the editor's one modal decision box: a title, body lines,
// and a row of choice buttons. ←/→ or Tab move between choices, Enter picks
// the highlighted one, y/n pick the first/last, and Esc picks the last (the
// safe "cancel"/"ignore" choice by convention). Every other key is swallowed.
type choiceOverlay struct {
	kind     choiceOverlayKind
	title    string
	body     []overlayLine
	choices  []string
	selected int
}

var (
	choiceButtonStyle         = lipgloss.NewStyle().Foreground(colFg).Background(colBgChrome)
	choiceButtonSelectedStyle = lipgloss.NewStyle().Bold(true).Foreground(colBg).Background(colAqua)
	overlayHighlightStyle     = lipgloss.NewStyle().Bold(true).Foreground(colOrange).Background(colBg)
	overlayPlainStyle         = lipgloss.NewStyle().Foreground(colFg).Background(colBg)
)

func (c choiceOverlay) open() bool {
	return c.kind != choiceNone
}

// handleKey applies one key. done is true once a choice is made, with chosen
// its index; the caller closes the overlay and acts on the choice.
func (c choiceOverlay) handleKey(key string) (next choiceOverlay, chosen int, done bool) {
	last := len(c.choices) - 1
	switch key {
	case "left", "shift+tab":
		c.selected = (c.selected + last) % (last + 1)
	case "right", "tab":
		c.selected = (c.selected + 1) % (last + 1)
	case "enter":
		return c, c.selected, true
	case "y":
		return c, 0, true
	case "n", "esc":
		return c, last, true
	}
	return c, 0, false
}

// render centers the box in the main pane.
func (c choiceOverlay) render(width, height int) string {
	lines := make([]string, 0, len(c.body)+4)
	contentWidth := len([]rune(c.title))
	for _, line := range c.body {
		contentWidth = max(contentWidth, len([]rune(line.text)))
	}
	buttons := make([]string, len(c.choices))
	buttonsWidth := 0
	for i, choice := range c.choices {
		style := choiceButtonStyle
		if i == c.selected {
			style = choiceButtonSelectedStyle
		}
		buttons[i] = style.Render(" " + choice + " ")
		buttonsWidth += len([]rune(choice)) + 2
	}
	buttonsWidth += 2 * max(0, len(c.choices)-1)
	hint := "←/→ choose · enter confirm · esc " + strings.ToLower(c.choices[len(c.choices)-1])
	contentWidth = max(contentWidth, buttonsWidth, len([]rune(hint)))
	boxWidth := input.Clamp(contentWidth+4, 20, max(20, width-2))
	textWidth := boxWidth - 2

	lines = append(lines, modalTitleStyle.Render(truncateRunes(c.title, textWidth)), "")
	for _, line := range c.body {
		text := truncateRunes(line.text, textWidth)
		switch line.style {
		case overlayLineStrong:
			lines = append(lines, modalTitleStyle.Render(text))
		case overlayLineDim:
			lines = append(lines, overlayHintStyle.Render(text))
		case overlayLineHighlight:
			lines = append(lines, overlayHighlightStyle.Render(text))
		default:
			lines = append(lines, overlayPlainStyle.Render(text))
		}
	}
	if len(c.body) > 0 {
		lines = append(lines, "")
	}
	lines = append(lines, strings.Join(buttons, whitespaceStyle.Render("  ")), "", overlayHintStyle.Render(truncateRunes(hint, textWidth)))
	box := modalStyle.Width(boxWidth + 2).Render(strings.Join(lines, "\n"))
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box,
		lipgloss.WithWhitespaceStyle(whitespaceStyle))
}

// removePathChoice is the :rm confirmation — deletion is irreversible, so it
// names exactly what goes.
func removePathChoice(rel string, isDirectory bool) choiceOverlay {
	title := "remove file " + rel + "?"
	if isDirectory {
		title = "remove directory " + rel + " and its contents?"
	}
	return choiceOverlay{kind: choiceRemovePath, title: title, choices: []string{"Remove", "Cancel"}}
}

// handleChoiceKey routes a key to the open choice overlay and, once a choice
// is made, closes it and acts on what it was asking.
func (m Model) handleChoiceKey(key string) (tea.Model, tea.Cmd) {
	kind := m.choice.kind
	next, chosen, done := m.choice.handleKey(key)
	if !done {
		m.choice = next
		return m, nil
	}
	m.choice = choiceOverlay{}
	switch kind {
	case choiceRemovePath:
		rel := m.confirmRm
		m.confirmRm, m.confirmRmDir = "", false
		if chosen == 0 {
			return m.queryRemove(rel)
		}
		m.notice = "rm cancelled"
	case choiceExternalRequest:
		return m.resolveExternalChoice(chosen == 0)
	case choiceQuitReviewSession:
		if chosen == 0 {
			return m.quitReviewSession()
		}
	}
	return m, nil
}
