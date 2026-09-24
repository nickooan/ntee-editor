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
