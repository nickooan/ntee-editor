package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The frame benchmarks need the suite's *testing.T model builders, so they
// run as a gated test: NTEE_RENDER_BENCH=1 go test -run TestRenderCost -v
func TestRenderCost(t *testing.T) {
	if os.Getenv("NTEE_RENDER_BENCH") == "" {
		t.Skip("set NTEE_RENDER_BENCH=1 to measure frame cost")
	}
	report := func(name string, m Model) {
		result := testing.Benchmark(func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = m.render()
			}
		})
		t.Logf("%-14s %s %s", name, result.String(), result.MemString())
	}

	edit, root := newTestModel(t, nil)
	var source strings.Builder
	source.WriteString("package main\n\n")
	for index := range 1500 {
		fmt.Fprintf(&source, "func helper%d(value int) string { return \"héllo\" + string(rune(value)) } // %d\n", index, index)
	}
	must(t, os.WriteFile(filepath.Join(root, "big.go"), []byte(source.String()), 0o644))
	edit.width, edit.height = 200, 60
	edit = edit.openFileAt("big.go")
	report("edit 200x60", edit)

	review, _, _ := startedReviewSession(t)
	review.width, review.height = 200, 60
	review = settleReviewMotion(review)
	report("review 200x60", review)

	live, receiver := liveAutoDriveReview(t)
	live = settleReviewMotion(live)
	live = replaceLive(t, live, receiver, "line 5\n", "line 5a\nline 5b\n")
	live.width, live.height = 200, 60
	if !live.review.motion.running() {
		t.Fatal("the edit animates")
	}
	report("motion 200x60", live)
}
