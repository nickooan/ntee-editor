package app

import (
	"testing"

	"github.com/nickooan/ntee-editor/internal/filetree"
)

// The shared window replaced BuildFileTreeViewport in the app. The two must
// agree, or file-tree clicks land on a different row than the one drawn.
func TestSidebarWindowStartMatchesFileTreeViewport(t *testing.T) {
	entries := make([]filetree.FileTreeEntry, 20)
	for _, height := range []int{1, 2, 5, 8} {
		for _, selected := range []int{-1, 0, 1, 7, 19} {
			viewport := filetree.BuildFileTreeViewport(entries, height, 0, selected)
			got := sidebarWindowStart(len(entries), height, selected)
			if got != viewport.SafeScrollY {
				t.Fatalf("height %d selected %d: window %d, viewport %d", height, selected, got, viewport.SafeScrollY)
			}
		}
	}
}

func TestLazySidebarListMatchesEagerList(t *testing.T) {
	rows := make([]sidebarRow, 30)
	for index := range rows {
		rows[index] = sidebarHeaderRow(string(rune('a' + index%26)))
	}
	built := 0
	eager := sidebarList{rows: rows, selectedIndex: 20}
	lazy := sidebarList{rowCount: len(rows), rowAt: func(index int) sidebarRow {
		built++
		return rows[index]
	}, selectedIndex: 20}

	if got, want := renderSidebarList(lazy, 12, 5), renderSidebarList(eager, 12, 5); got != want {
		t.Fatalf("lazy render differs:\n%s\nwant:\n%s", got, want)
	}
	if built != 5 {
		t.Fatalf("a frame should build only the visible rows, built %d", built)
	}
	if lazy.count() != eager.count() || sidebarRowPlain(lazy.row(7)) != sidebarRowPlain(eager.row(7)) {
		t.Fatal("count/row accessors differ")
	}
}
