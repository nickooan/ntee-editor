package app

import (
	"strings"
	"testing"
)

func TestFindConflictBlocksSimple(t *testing.T) {
	lines := []string{
		"before",
		"<<<<<<< HEAD",
		"ours line",
		"=======",
		"theirs a",
		"theirs b",
		">>>>>>> feature/login",
		"after",
	}
	blocks := findConflictBlocks(lines)
	if len(blocks) != 1 {
		t.Fatalf("want 1 block, got %d", len(blocks))
	}
	b := blocks[0]
	if b.start != 1 || b.mid != 3 || b.end != 6 || b.base != -1 {
		t.Fatalf("indices = %+v", b)
	}
	if b.oursLabel != "HEAD" || b.theirsLabel != "feature/login" {
		t.Fatalf("labels = %q / %q", b.oursLabel, b.theirsLabel)
	}
	// ours content is lines[start+1:mid]; theirs is lines[mid+1:end].
	if got := strings.Join(lines[b.start+1:b.mid], "\n"); got != "ours line" {
		t.Fatalf("ours = %q", got)
	}
	if got := strings.Join(lines[b.mid+1:b.end], "\n"); got != "theirs a\ntheirs b" {
		t.Fatalf("theirs = %q", got)
	}
}

func TestFindConflictBlocksDiff3(t *testing.T) {
	lines := []string{
		"<<<<<<< HEAD",
		"ours",
		"||||||| merged common ancestors",
		"base",
		"=======",
		"theirs",
		">>>>>>> other",
	}
	blocks := findConflictBlocks(lines)
	if len(blocks) != 1 {
		t.Fatalf("want 1 block, got %d", len(blocks))
	}
	b := blocks[0]
	if b.base != 2 || b.mid != 4 || b.end != 6 {
		t.Fatalf("diff3 indices = %+v", b)
	}
	// Keeping ours must stop before the base section.
	out := resolveConflicts(lines, []conflictBlock{b}, []conflictSide{sideOurs})
	if strings.Join(out, "\n") != "ours" {
		t.Fatalf("diff3 ours resolve = %q", out)
	}
	// "both" drops the base section too: ours then theirs only.
	out = resolveConflicts(lines, []conflictBlock{b}, []conflictSide{sideBoth})
	if strings.Join(out, "\n") != "ours\ntheirs" {
		t.Fatalf("diff3 both resolve = %q", out)
	}
}

func TestFindConflictBlocksMalformed(t *testing.T) {
	cases := map[string][]string{
		"unclosed start":       {"<<<<<<< HEAD", "ours", "======="},
		"close before sep":     {"<<<<<<< HEAD", "ours", ">>>>>>> x"},
		"restart before close": {"<<<<<<< HEAD", "ours", "<<<<<<< AGAIN", "o2", "=======", "t2", ">>>>>>> y"},
		"lone separator":       {"code", "=======", "more"},
	}
	for name, lines := range cases {
		blocks := findConflictBlocks(lines)
		if name == "restart before close" {
			// The first start is abandoned; the well-formed inner block survives.
			if len(blocks) != 1 || blocks[0].oursLabel != "AGAIN" {
				t.Fatalf("%s: want the AGAIN block, got %+v", name, blocks)
			}
			continue
		}
		if len(blocks) != 0 {
			t.Fatalf("%s: want no blocks, got %+v", name, blocks)
		}
	}
}

func TestResolveConflictsMultiple(t *testing.T) {
	lines := []string{
		"top",
		"<<<<<<< HEAD",
		"ours1",
		"=======",
		"theirs1",
		">>>>>>> b1",
		"middle",
		"<<<<<<< HEAD",
		"ours2",
		"=======",
		"theirs2",
		">>>>>>> b2",
		"bottom",
	}
	blocks := findConflictBlocks(lines)
	if len(blocks) != 2 {
		t.Fatalf("want 2 blocks, got %d", len(blocks))
	}
	// Keep ours in the first, theirs in the second.
	out := resolveConflicts(lines, blocks, []conflictSide{sideOurs, sideTheirs})
	want := "top\nours1\nmiddle\ntheirs2\nbottom"
	if got := strings.Join(out, "\n"); got != want {
		t.Fatalf("resolve = %q, want %q", got, want)
	}
	// Both sides in the first, ours in the second.
	out = resolveConflicts(lines, blocks, []conflictSide{sideBoth, sideOurs})
	want = "top\nours1\ntheirs1\nmiddle\nours2\nbottom"
	if got := strings.Join(out, "\n"); got != want {
		t.Fatalf("both resolve = %q, want %q", got, want)
	}
	// Input slice must be untouched.
	if lines[1] != "<<<<<<< HEAD" {
		t.Fatal("resolveConflicts mutated its input")
	}
}
