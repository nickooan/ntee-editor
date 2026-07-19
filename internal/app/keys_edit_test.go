package app

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestEditPageDownUpOverlap(t *testing.T) {
	m := execLineFixture(t, 100)
	m.edit.cy, m.fileScrollY = 0, 0

	h := m.contentHeight() + 1 // rendered file rows (height=30 → 26)
	step := h - 1              // one-line overlap

	m = ctrl(m, tea.KeyPgDown)
	if m.edit.cy != step {
		t.Fatalf("PgDown cy = %d, want %d", m.edit.cy, step)
	}
	// New top equals the old bottom line (0 + h-1 == step): one-line overlap.
	if m.fileScrollY != step {
		t.Fatalf("PgDown scrollY = %d, want %d (one-line overlap)", m.fileScrollY, step)
	}

	m = ctrl(m, tea.KeyPgUp)
	if m.edit.cy != 0 || m.fileScrollY != 0 {
		t.Fatalf("PgUp back cy=%d scrollY=%d, want 0/0", m.edit.cy, m.fileScrollY)
	}
}

func TestEditPageDownClampsAtEnd(t *testing.T) {
	m := execLineFixture(t, 100)
	last := len(m.edit.lines) - 1
	m.edit.cy, m.fileScrollY = last, last

	m = ctrl(m, tea.KeyPgDown)
	if m.edit.cy != last {
		t.Fatalf("PgDown at end cy = %d, want %d", m.edit.cy, last)
	}
	if m.fileScrollY < 0 || m.fileScrollY > max(0, len(m.edit.lines)-1) {
		t.Fatalf("scrollY out of range: %d", m.fileScrollY)
	}
}

func TestEditPageUpAtTopStays(t *testing.T) {
	m := execLineFixture(t, 100)
	m.edit.cy, m.fileScrollY = 0, 0
	m = ctrl(m, tea.KeyPgUp)
	if m.edit.cy != 0 || m.fileScrollY != 0 {
		t.Fatalf("PgUp at top should stay: cy=%d scrollY=%d", m.edit.cy, m.fileScrollY)
	}
}
