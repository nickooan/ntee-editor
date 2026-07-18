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

func TestCreateSearchRegexLiteralFallback(t *testing.T) {
	re := CreateSearchRegex("foo(") // invalid regex → literal
	if re == nil || !re.MatchString("call foo(") {
		t.Fatal("literal fallback failed")
	}
	if re := CreateSearchRegex("f.o"); re == nil || !re.MatchString("FAO") == false && !re.MatchString("foo") {
		t.Fatal("regex mode failed")
	}
}

func TestBuildMatchesByLineKeepsGlobalIndex(t *testing.T) {
	matches := FindSearchMatches("aa\naa", "a")
	byLine := BuildMatchesByLine(matches)
	if len(byLine[1]) != 2 || byLine[1][0].MatchIndex != 2 {
		t.Fatalf("global index lost: %+v", byLine)
	}
}
