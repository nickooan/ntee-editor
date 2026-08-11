package view

import "testing"

func TestBuildTerminalViewportClamps(t *testing.T) {
	vp := BuildTerminalViewport("a\nb\nc\nd", 10, 2, 0, 99)
	if vp.SafeScrollY != 2 || len(vp.Lines) != 2 {
		t.Fatalf("clamp failed: %+v", vp)
	}
	if vp.Lines[0] != "c         " {
		t.Fatalf("window wrong: %q", vp.Lines[0])
	}
}

func TestNormalizeLinesCRLF(t *testing.T) {
	lines := NormalizeLines("a\r\nb\nc")
	if len(lines) != 3 || lines[0] != "a" {
		t.Fatalf("crlf normalize: %#v", lines)
	}
}

func TestNormalizeLinesLoneCR(t *testing.T) {
	// A bare \r is a line break too (classic-Mac remnants, stray control
	// bytes) — it must never survive into a rendered line.
	lines := NormalizeLines("a\rb\r\nc")
	if len(lines) != 3 || lines[0] != "a" || lines[1] != "b" || lines[2] != "c" {
		t.Fatalf("lone CR normalize: %#v", lines)
	}
}

func TestFindSearchMatchesByteOffsetsUTF8(t *testing.T) {
	// "é" is 2 bytes: the match offsets must be byte positions.
	matches := FindSearchMatches("héllo hello", "llo")
	if len(matches) != 2 {
		t.Fatalf("want 2 matches, got %d", len(matches))
	}
	if matches[0].Start != 3 { // h(1) + é(2) = byte 3
		t.Fatalf("first match should start at byte 3, got %d", matches[0].Start)
	}
	if matches[1].Start != 9 {
		t.Fatalf("second match should start at byte 9, got %d", matches[1].Start)
	}
}

// BuildMatchesByLine relies on FindSearchMatches emitting matches line by line
// with increasing starts — the buckets carry no sort of their own.
func TestBuildMatchesByLineBucketsStaySorted(t *testing.T) {
	matches := FindSearchMatches("ab ab ab\nxx ab", "ab")
	byLine := BuildMatchesByLine(matches)
	if len(byLine[0]) != 3 || len(byLine[1]) != 1 {
		t.Fatalf("buckets: %+v", byLine)
	}
	for line, bucket := range byLine {
		for i := 1; i < len(bucket); i++ {
			if bucket[i-1].Start >= bucket[i].Start {
				t.Fatalf("line %d bucket out of order: %+v", line, bucket)
			}
		}
	}
}

func TestCreateSearchRegexLiteralFallback(t *testing.T) {
	re := CreateSearchRegex("foo(") // invalid regex → literal
	if re == nil || !re.MatchString("call foo(") {
		t.Fatal("literal fallback failed")
	}
	if re := CreateSearchRegex("f.o"); re == nil || !re.MatchString("foo") {
		t.Fatal("regex mode failed")
	}
}

func TestSearchIsCaseSensitive(t *testing.T) {
	if re := CreateSearchRegex("foo"); re == nil || re.MatchString("FOO") {
		t.Fatal("in-file search must not case-fold")
	}
	if matches := FindSearchMatches("Foo foo FOO", "foo"); len(matches) != 1 || matches[0].Start != 4 {
		t.Fatalf("want exactly the lowercase match, got %+v", matches)
	}
}

func TestBuildMatchesByLineKeepsGlobalIndex(t *testing.T) {
	matches := FindSearchMatches("aa\naa", "a")
	byLine := BuildMatchesByLine(matches)
	if len(byLine[1]) != 2 || byLine[1][0].MatchIndex != 2 {
		t.Fatalf("global index lost: %+v", byLine)
	}
}
