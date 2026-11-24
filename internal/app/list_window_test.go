package app

import "testing"

func TestListWindowKeepsTheSelectionVisible(t *testing.T) {
	cases := []struct {
		count, selectedIndex, maxRows int
		selected, start, end          int
	}{
		{count: 10, selectedIndex: 0, maxRows: 4, selected: 0, start: 0, end: 4},
		{count: 10, selectedIndex: 3, maxRows: 4, selected: 3, start: 0, end: 4},
		{count: 10, selectedIndex: 4, maxRows: 4, selected: 4, start: 1, end: 5},
		{count: 10, selectedIndex: 99, maxRows: 4, selected: 9, start: 6, end: 10},
		{count: 3, selectedIndex: -2, maxRows: 6, selected: 0, start: 0, end: 3},
		{count: 0, selectedIndex: 0, maxRows: 6, selected: 0, start: 0, end: 0},
		{count: 5, selectedIndex: 2, maxRows: 0, selected: 2, start: 0, end: 0},
		{count: 5, selectedIndex: 2, maxRows: -3, selected: 2, start: 0, end: 0},
	}
	for _, c := range cases {
		selected, start, end := listWindow(c.count, c.selectedIndex, c.maxRows)
		if selected != c.selected || start != c.start || end != c.end {
			t.Errorf("listWindow(%d, %d, %d) = %d, %d, %d; want %d, %d, %d", c.count, c.selectedIndex, c.maxRows, selected, start, end, c.selected, c.start, c.end)
		}
	}
}
