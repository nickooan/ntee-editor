package syntax

import (
	"strings"
	"testing"

	"github.com/alecthomas/chroma/v2"
)

func TestHighlightLinesAlignment(t *testing.T) {
	content := "package main\n\n// a comment\nfunc main() {\n\tprintln(\"hi\")\n}\n"
	lines := HighlightLines("main.go", content)
	if lines == nil {
		t.Fatal("go file should have a lexer")
	}
	want := len(strings.Split(content, "\n"))
	if len(lines) != want {
		t.Fatalf("row count %d != line count %d", len(lines), want)
	}
	// Reassembling each row's segment text must reproduce the source line.
	src := strings.Split(content, "\n")
	for i, row := range lines {
		var b strings.Builder
		for _, seg := range row {
			b.WriteString(seg.Text)
		}
		if b.String() != src[i] {
			t.Fatalf("line %d mismatch: %q != %q", i, b.String(), src[i])
		}
	}
}

func TestHighlightLinesMultiLineToken(t *testing.T) {
	// A block comment spans lines: the token splitter must keep row indices
	// aligned with source lines.
	content := "/* one\ntwo\nthree */\nvar x = 1\n"
	lines := HighlightLines("a.ts", content)
	if lines == nil {
		t.Fatal("ts file should have a lexer")
	}
	src := strings.Split(content, "\n")
	if len(lines) != len(src) {
		t.Fatalf("row count %d != line count %d", len(lines), len(src))
	}
	for i, row := range lines {
		var b strings.Builder
		for _, seg := range row {
			b.WriteString(seg.Text)
		}
		if b.String() != src[i] {
			t.Fatalf("line %d mismatch: %q != %q", i, b.String(), src[i])
		}
	}
}

func TestLexerForUnknownExtension(t *testing.T) {
	if lex := LexerFor("picture.xyzunknown"); lex != nil {
		t.Fatalf("unknown extension should render plain, got %v", lex.Config().Name)
	}
	if lex := LexerFor("main.go"); lex == nil {
		t.Fatal("go must resolve")
	}
	if lex := LexerFor("app.TS"); lex == nil {
		t.Fatal("extension match must be case-insensitive")
	}
}

func TestMonokaiStyleMapping(t *testing.T) {
	SetStyle("monokai")
	if got := segmentFor(chroma.Keyword).Color; got != "#66d9ef" {
		t.Fatalf("keyword: %q", got)
	}
	if got := segmentFor(chroma.LiteralString).Color; got != "#e6db74" {
		t.Fatalf("string: %q", got)
	}
	if got := segmentFor(chroma.Comment).Color; got != "#75715e" {
		t.Fatalf("comment: %q", got)
	}
	if got := segmentFor(chroma.NameFunction).Color; got != "#a6e22e" {
		t.Fatalf("function: %q", got)
	}
}

func TestGruvboxDefaultMapping(t *testing.T) {
	SetStyle("gruvbox")
	if got := segmentFor(chroma.Keyword).Color; got != "#fb4934" {
		t.Fatalf("keyword should be gruvbox red: %q", got)
	}
	if got := segmentFor(chroma.KeywordType).Color; got != "#fabd2f" {
		t.Fatalf("type should stay gold: %q", got)
	}
	if got := segmentFor(chroma.NameFunction).Color; got != "#fabd2f" {
		t.Fatalf("function: %q", got)
	}
	if got := segmentFor(chroma.LiteralString).Color; got != "#b8bb26" {
		t.Fatalf("string: %q", got)
	}
	seg := segmentFor(chroma.Comment)
	if seg.Color != "#928374" || !seg.Italic {
		t.Fatalf("comment: %+v", seg)
	}
	if got := segmentFor(chroma.Operator).Color; got != "#fb4934" {
		t.Fatalf("operator should be red: %q", got)
	}
}

func TestUnknownStyleFallsBackToGruvbox(t *testing.T) {
	SetStyle("no-such-style-xyz")
	if got := segmentFor(chroma.Keyword).Color; got != "#fb4934" {
		t.Fatalf("fallback keyword: %q", got)
	}
	SetStyle("terminal16") // legacy config value
	if got := segmentFor(chroma.Keyword).Color; got != "#fb4934" {
		t.Fatalf("legacy fallback keyword: %q", got)
	}
	SetStyle("gruvbox")
}
