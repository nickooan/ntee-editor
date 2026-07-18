package lsp

import "testing"

func TestURIRoundTrip(t *testing.T) {
	for _, path := range []string{
		"/Users/x/project/main.go",
		"/tmp/with space/a.ts",
		"/tmp/héllo/ü.go",
	} {
		uri := PathToURI(path)
		back, ok := URIToPath(uri)
		if !ok || back != path {
			t.Fatalf("round trip %q → %q → %q ok=%v", path, uri, back, ok)
		}
	}
	if _, ok := URIToPath("untitled:Untitled-1"); ok {
		t.Fatal("non-file scheme should not resolve")
	}
}

func TestUTF16Conversions(t *testing.T) {
	// "a😀b" — 😀 is one rune but two UTF-16 units.
	line := "a😀b"
	if got := UTF16Col(line, 2); got != 3 {
		t.Fatalf("rune 2 should be utf16 3, got %d", got)
	}
	if got := RuneCol(line, 3); got != 2 {
		t.Fatalf("utf16 3 should be rune 2, got %d", got)
	}
	// CJK stays 1:1.
	if got := UTF16Col("汉字x", 2); got != 2 {
		t.Fatalf("cjk: %d", got)
	}
	// Past-end clamps.
	if got := RuneCol("ab", 99); got != 2 {
		t.Fatalf("clamp: %d", got)
	}
}
