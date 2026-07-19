package lsp

import (
	"encoding/json"
	"testing"
)

func TestParseCompletion(t *testing.T) {
	list := parseCompletion(json.RawMessage(`{"isIncomplete":false,"items":[{"label":"Println"},{"label":"Printf"}]}`))
	if len(list) != 2 || list[0].Label != "Println" {
		t.Fatalf("object form: %+v", list)
	}
	arr := parseCompletion(json.RawMessage(`[{"label":"Foo","insertText":"Foo()"}]`))
	if len(arr) != 1 || arr[0].InsertText != "Foo()" {
		t.Fatalf("array form: %+v", arr)
	}
	if parseCompletion(json.RawMessage(`null`)) != nil {
		t.Fatal("null should parse to nil")
	}
	if parseCompletion(nil) != nil {
		t.Fatal("empty should parse to nil")
	}
}

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
