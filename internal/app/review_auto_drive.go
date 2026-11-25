package app

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/nickooan/ntee-editor/internal/keymap"
)

// reviewIsLive reports whether the review follows an agent still sending or
// editing — the reviews auto-drive applies to.
func (m Model) reviewIsLive() bool {
	return m.mode == modeReview && (m.review.session != "" || m.review.agentEditsLive)
}

// pauseAutoDrive hands the review to the user: they moved, clicked or
// searched, so the agent's next change no longer moves their view.
func (m Model) pauseAutoDrive() Model {
	m.review.followPending = ""
	if !m.reviewIsLive() || !m.review.follow {
		return m
	}
	m.review.follow = false
	return m.flashAutoDrive()
}

// toggleAutoDrive is the auto-drive key: on, it jumps to the agent's newest
// file and change and follows from there; off, it pauses.
func (m Model) toggleAutoDrive() (Model, tea.Cmd) {
	if !m.reviewIsLive() {
		m.notice = "auto-drive follows a live review — this one is not live"
		return m, nil
	}
	if m.review.follow {
		return m.pauseAutoDrive(), nil
	}
	m.review.follow = true
	m = m.flashAutoDrive()
	var load tea.Cmd
	if index, ok := m.review.fileByPath[m.review.newestPath]; ok && index != m.review.selection.index {
		m, load = m.selectReviewFile(index)
		m.review.follow = true
	}
	if file, ok := m.selectedReviewFile(); ok && file.loaded() {
		m.review.position = rowCursor{}.anchoredAt(file.driveLandingRow(), m.reviewSpanFor(file))
	}
	return m, load
}

// flashAutoDrive starts the switch's flash in the floating panel.
func (m Model) flashAutoDrive() Model {
	m.review.motion.switchFrame, m.review.motion.switching = m.review.motion.frame, true
	return m
}

// driveLandingRow is where auto-drive puts the cursor on a file: the top of
// its newest change, else its note or first change.
func (file reviewFile) driveLandingRow() int {
	if file.hasChange {
		return file.changeRow
	}
	return file.landingRow()
}

// reviewLandingAfterLoad places the cursor on the selected file once a load
// lands. A first load lands on the note or first change. A reload follows
// the agent while auto-drive is on — onto a note it just anchored to a
// line, else onto the change — and otherwise leaves the user's cursor on
// the line they were reading, carried past rows the reload added or removed.
func (m Model) reviewLandingAfterLoad(previous, loaded reviewFile, motion *rowMotion) rowCursor {
	span := m.reviewSpanFor(loaded)
	if !previous.reloading {
		return rowCursor{}.anchoredAt(loaded.landingRow(), span)
	}
	if m.review.follow {
		if note, ok := previous.activeReviewNote(); ok && note.contentHash == 0 && note.line > 0 {
			return rowCursor{}.anchoredAt(loaded.noteRow(note), span)
		}
		if motion != nil && loaded.hasChange {
			return rowCursor{}.anchoredAt(loaded.changeRow, span)
		}
	}
	position := m.review.position
	if motion == nil || span.total == 0 {
		return position.anchoredAt(position.row, span).withScroll(position.scrollY)
	}
	row := motion.carried(position.row)
	return position.anchoredAt(row, span).withScroll(position.scrollY + row - position.row)
}

// withScroll keeps the viewport where it was (clamped), so a reload under a
// paused cursor doesn't scroll the page.
func (c rowCursor) withScroll(scrollY int) rowCursor {
	c.scrollY = max(0, scrollY)
	return c
}

var (
	autoDriveOnLabelStyle  = lipgloss.NewStyle().Foreground(colAqua).Bold(true).Background(colBgChrome)
	autoDriveOffLabelStyle = lipgloss.NewStyle().Foreground(colComment).Bold(true).Background(colBgChrome)
	autoDriveChipOnStyle   = lipgloss.NewStyle().Foreground(colBg).Bold(true)
	autoDriveChipOffStyle  = lipgloss.NewStyle().Foreground(colFg).Bold(true)
)

// The switch chip's resting backgrounds, and the brighter ones a flip
// flashes from.
const (
	hexAutoDriveOn       = "#b8bb26"
	hexAutoDriveOnFlash  = "#eef7a8"
	hexAutoDriveOff      = "#504945"
	hexAutoDriveOffFlash = "#a89984"
)

// autoDriveHints are the switch's second row, on and off.
func (m Model) autoDriveHints() (on, off string) {
	keyLabel := m.keys.Label(keymap.AutoDrive)
	return "following the agent · " + keyLabel + " pause", "you're driving · " + keyLabel + " resume"
}

// autoDriveWidth is the panel width that fits the switch in either state,
// so flipping it never resizes the panel.
func (m Model) autoDriveWidth() int {
	on, off := m.autoDriveHints()
	return max(ansi.StringWidth(on), ansi.StringWidth(off)) + 2*floatPanelPaddingX + 2
}

// autoDriveRows are the floating panel's switch: a lit ON chip while the
// view follows the agent (spinning with the agents' work), a dim OFF one
// once the user drives, and the key that flips it.
func (m Model) autoDriveRows() []floatPanelRow {
	flash := 1.0
	if motion := m.review.motion; motion.switching {
		flash = easeOut(motion.progress(motion.switchFrame, reviewInjectFrames))
	}
	onHint, offHint := m.autoDriveHints()
	if m.review.follow {
		mark := "◉"
		if m.review.agentEdits != "" && m.agentsWorkingRecently() {
			mark = m.agentEditsSpinner(true)
		}
		chip := autoDriveChipOnStyle.Background(lipgloss.Color(blendHex(hexAutoDriveOnFlash, hexAutoDriveOn, flash)))
		return []floatPanelRow{
			{segments: []floatPanelSegment{{mark + " AUTO-DRIVE ", autoDriveOnLabelStyle}, {" ON ● ", chip}}},
			{label: onHint},
		}
	}
	chip := autoDriveChipOffStyle.Background(lipgloss.Color(blendHex(hexAutoDriveOffFlash, hexAutoDriveOff, flash)))
	return []floatPanelRow{
		{segments: []floatPanelSegment{{"○ AUTO-DRIVE ", autoDriveOffLabelStyle}, {" ○ OFF ", chip}}},
		{label: offHint},
	}
}
