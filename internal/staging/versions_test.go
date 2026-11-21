package staging

import (
	"strings"
	"testing"
)

func TestAnEditMadeOnAStaleReadIsCaught(t *testing.T) {
	registry, _, _ := newTestRegistry(t)
	// Both agents read a.go as it is on disk.
	read := registry.Remember(diskA)

	first, err := registry.Replace("S", "agent-a", "a.go", Replacement{Old: "one", New: "ONE", Expect: read})
	must(t, err)
	if first.Version != Version("ONE\ntwo\nthree\ntwo\n") || len(first.Notes) != 0 {
		t.Fatalf("a fresh read edits cleanly and names the new version: %+v", first)
	}

	// agent-b, still on the old read, changes the same line.
	_, err = registry.Replace("S", "agent-b", "a.go", Replacement{Old: "ONE", New: "uno", Expect: read})
	if err == nil || !strings.Contains(err.Error(), "a.go changed since you read it: agent agent-a's replace L1 (edit 1,") ||
		!strings.Contains(err.Error(), "now different at L1") || !strings.Contains(err.Error(), "your replace of L1 overlaps that") {
		t.Fatalf("an edit on lines changed since the read is refused, saying by whom and where: %v", err)
	}
	// …and a whole-file write would undo agent-a's change.
	if _, err := registry.WriteExpecting("S", "agent-b", "a.go", "rewritten\n", read); err == nil || !strings.Contains(err.Error(), "a whole-file write would undo that") {
		t.Fatalf("a stale whole-file write is refused: %v", err)
	}

	// A change elsewhere in the file goes through, with a note.
	other, err := registry.Replace("S", "agent-b", "a.go", Replacement{Old: "three", New: "THREE", Expect: read})
	must(t, err)
	if len(other.Notes) != 1 || !strings.Contains(other.Notes[0], "changed since you read it") || !strings.Contains(other.Notes[0], "does not touch those lines") {
		t.Fatalf("a stale read away from the change is applied with a note: %+v", other.Notes)
	}

	// Read again: up to date, so no stale check — but agent-a's lines get a note.
	current := registry.Remember(stagedContent(t, registry, "S", "a.go"))
	again, err := registry.Replace("S", "agent-b", "a.go", Replacement{Old: "ONE", New: "uno", Expect: current})
	must(t, err)
	if len(again.Notes) != 1 || !strings.Contains(again.Notes[0], "other agents changed these lines in this session too: agent agent-a (edit 1, L1)") {
		t.Fatalf("an edit on another agent's lines is told so: %+v", again.Notes)
	}
	if _, err := registry.Write("S", "agent-b", "a.go", "no check\n"); err != nil {
		t.Fatalf("without a version there is no check: %v", err)
	}
}

func TestAStaleReadOfAnUnknownVersionIsTreatedAsOverlapping(t *testing.T) {
	registry, _, _ := newTestRegistry(t)
	_, err := registry.Replace("S", "agent-a", "a.go", Replacement{Old: "one", New: "ONE"})
	must(t, err)
	_, err = registry.Replace("S", "agent-b", "a.go", Replacement{Old: "three", New: "THREE", Expect: "000000000000"})
	if err == nil || !strings.Contains(err.Error(), "changed since you read it") {
		t.Fatalf("a version no longer known cannot be shown safe: %v", err)
	}
}

func TestChangedRanges(t *testing.T) {
	for _, test := range []struct {
		old, current, want string
	}{
		{"a\nb\nc\n", "a\nB\nc\n", "L2"},
		{"a\nb\nc\n", "a\nb\nx\ny\nc\n", "L3–L4"},
		{"a\nb\nc\nd\n", "a\nd\n", "L2"},
		{"a\nb\nc\nd\ne\n", "A\nb\nc\nd\nE\n", "L1,L5"},
	} {
		var parts []string
		for _, span := range changedRanges(test.old, test.current) {
			parts = append(parts, span.String())
		}
		if got := strings.Join(parts, ","); got != test.want {
			t.Fatalf("changedRanges(%q, %q) = %s, want %s", test.old, test.current, got, test.want)
		}
	}
}
