package app

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nickooan/ntee-editor/internal/view"
)

// grepBenchFiles fakes a loaded snapshot: n files of ~50 lines each.
func grepBenchFiles(n int) []grepFile {
	var b strings.Builder
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&b, "func handler%d(w http.ResponseWriter, r *http.Request) { // line %d\n", i, i)
	}
	content := b.String()
	starts := buildLineStarts(content)
	files := make([]grepFile, n)
	for i := range files {
		files[i] = grepFile{rel: fmt.Sprintf("pkg%d/file%d.go", i%40, i), content: content, lineStarts: starts}
	}
	return files
}

// BenchmarkGrepWholeContent measures the new scan: one FindStringIndex pass
// per file with offset→line mapping. A rare query (few hits) forces a full
// scan of every file — the worst case the debounced background search pays.
func BenchmarkGrepWholeContent(b *testing.B) {
	files := grepBenchFiles(2000)
	re := view.CreateMultilineSearchRegex("handler49.*line 49")
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var hits []grepHit
		for _, f := range files {
			hits = appendGrepHits(hits, f, re, maxGrepResults)
			if len(hits) >= maxGrepResults {
				break
			}
		}
	}
}

// BenchmarkGrepPerLine is the old approach (MatchString on every pre-split
// line), kept as a comparison baseline for the whole-content scan above.
func BenchmarkGrepPerLine(b *testing.B) {
	files := grepBenchFiles(2000)
	lines := make([][]string, len(files))
	for i, f := range files {
		lines[i] = strings.Split(f.content, "\n")
	}
	re := view.CreateSearchRegex("handler49.*line 49")
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var hits []grepHit
		for fi, f := range files {
			for li, line := range lines[fi] {
				if re.MatchString(line) {
					hits = append(hits, grepHit{rel: f.rel, line: li})
					if len(hits) >= maxGrepResults {
						break
					}
				}
			}
		}
	}
}
