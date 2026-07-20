package fuzzy

import (
	"fmt"
	"testing"
)

// buildCorpus fakes a large workspace: nested dirs with varied file names.
func buildCorpus(n int) []string {
	dirs := []string{"internal/app", "internal/store", "internal/fuzzy", "cmd/ntee", "pkg/util", "web/src/components", "web/src/pages", "vendor/lib/deep/nested"}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		d := dirs[i%len(dirs)]
		out = append(out, fmt.Sprintf("%s/module_%d/handler_%d.go", d, i%97, i))
	}
	return out
}

// BenchmarkFilter measures the per-keystroke cost over a corpus prepared once
// (the Ctrl+P hot path). allocs/op is the number to watch: it should be tiny and
// independent of corpus size, since Filter no longer allocates per candidate.
func BenchmarkFilter(b *testing.B) {
	prepared := Prepare(buildCorpus(50000))
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = Filter("handler", prepared)
	}
}

// BenchmarkPrepare measures the one-time cost paid when the finder opens.
func BenchmarkPrepare(b *testing.B) {
	corpus := buildCorpus(50000)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = Prepare(corpus)
	}
}

// BenchmarkFilterSelective is the common case: a query that matches a small
// slice of the corpus, so most candidates die at the pre-filter.
func BenchmarkFilterSelective(b *testing.B) {
	prepared := Prepare(buildCorpus(50000))
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = Filter("fuzzyhandler42", prepared)
	}
}
