package app

import (
	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/keymap"
	"github.com/nickooan/ntee-editor/internal/view"
)

// handleReviewKey drives review mode. The switch is closed on purpose: any
// key not listed falls through inert, which is what makes the mode
// read-only — no editing, no jump-to-definition.
func (m Model) handleReviewKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.review.search.active {
		return m.handleReviewSearchKey(msg)
	}
	k := msg.String()
	switch {
	case m.keys.Matches(keymap.Find, k):
		if file, ok := m.selectedReviewFile(); ok && len(file.document.rows) > 0 {
			m.review.search = documentSearch{active: true}
		}
		return m, nil
	case m.keys.Matches(keymap.GotoFile, k):
		if m.review.preparing {
			return m, nil
		}
		return m.showFuzzy(fuzzyPromptReview, m.reviewFuzzyCandidates()), nil
	case m.keys.Matches(keymap.Grep, k):
		if m.review.preparing {
			return m, nil
		}
		return m.openGrepOver(m.reviewGrepFiles(), grepTargetReview), nil
	}

	span := m.reviewSpan()
	if position, handled := handleRowNavigationKey(k, m.review.position, span); handled {
		m.review.position = position
		return m, nil
	}
	file, _ := m.selectedReviewFile()
	switch k {
	case "esc":
		if m.reviewSessionRunning() {
			m.choice = m.quitReviewSessionChoice()
			return m, nil
		}
		return m.exitReview(), nil
	case "shift+up":
		return m.selectReviewFile(m.review.selection.index - 1)
	case "shift+down":
		return m.selectReviewFile(m.review.selection.index + 1)
	case "n":
		m.review.position = m.review.position.blockJump(1, span, file.document.isHunkStart)
	case "p":
		m.review.position = m.review.position.blockJump(-1, span, file.document.isHunkStart)
	}
	return m, nil
}

// handleReviewSearchKey drives the in-file search bar over the selected
// file's rows (documentSearch), removed lines included.
func (m Model) handleReviewSearchKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	return m.landReviewSearch(m.review.search.handleKey(msg, m.keys.Matches(keymap.Find, msg.String()), m.reviewMatchesFor, m.review.position.row)), nil
}

// reviewMatchesFor is the match set of a query over the selected file's rows.
func (m Model) reviewMatchesFor(query string) []view.SearchMatch {
	file, _ := m.selectedReviewFile()
	return m.reviewMC.get(file.corpus, query)
}

func (m Model) landReviewSearch(search documentSearch, landRow int, land bool) Model {
	m.review.search = search
	if land {
		m.review.position = m.review.position.anchoredAt(landRow, m.reviewSpan())
	}
	return m
}

// handleReviewClick moves the cursor to a clicked diff row; the review's
// file header sits above the rows.
func (m Model) handleReviewClick(x, y int) (Model, bool) {
	file, ok := m.selectedReviewFile()
	if !ok {
		return m, false
	}
	span := m.reviewSpanFor(file)
	row, column, hit := m.rowViewClickTarget(x, y, m.review.position, span, diffGutterWidth(file.document, span.height), 1)
	if !hit {
		return m, false
	}
	m.review.position = m.review.position.clickedAt(row, column, span)
	return m, true
}
