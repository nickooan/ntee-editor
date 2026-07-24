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

const ntsFixture = `// create a todo
ref ../data/user.ntd

url "@i(host)/todos"
type post
header Content-Type, application/json
auth bearer @i(token)

body {
  title: "buy
milk",
  url: false-positive,
  done: false,
  count: @i(count or 20),
}

@joint('trace')
-> @pick(id: data[0].userId)
-> @run(next)
`

const ntdFixture = `// env data
host: "https://api.example.com"
id: @env(ID or 1)
path: /todos/@env(ID)/items
todos: query getTodos($limit: Int) {
  # selection comment
  id
  title
}
`

// tokensFor tokenizes src with the lexer LexerFor resolves for filename.
func tokensFor(t *testing.T, filename, src string) []chroma.Token {
	t.Helper()
	lexer := LexerFor(filename)
	if lexer == nil {
		t.Fatalf("no lexer for %s", filename)
	}
	it, err := lexer.Tokenise(nil, src)
	if err != nil {
		t.Fatalf("tokenise %s: %v", filename, err)
	}
	return it.Tokens()
}

func hasToken(tokens []chroma.Token, typ chroma.TokenType, value string) bool {
	for _, tok := range tokens {
		if tok.Type == typ && tok.Value == value {
			return true
		}
	}
	return false
}

func TestLexerForPinnedAndCustom(t *testing.T) {
	cases := []struct{ file, name string }{
		{"req.nts", "NTS"},
		{"data.ntd", "NTD"},
		{"conf.json", "JSON"},
		{"conf.yaml", "YAML"},
		{"conf.yml", "YAML"},
		{"run.sh", "Bash"},
		{"run.bash", "Bash"},
		{"run.zsh", "Bash"},
	}
	for _, c := range cases {
		lexer := LexerFor(c.file)
		if lexer == nil {
			t.Fatalf("%s must resolve", c.file)
		}
		if got := lexer.Config().Name; got != c.name {
			t.Fatalf("%s resolved to %q, want %q", c.file, got, c.name)
		}
	}
}

func TestHighlightNteeAlignment(t *testing.T) {
	for _, c := range []struct{ file, content string }{
		{"req.nts", ntsFixture},
		{"data.ntd", ntdFixture},
	} {
		lines := HighlightLines(c.file, c.content)
		if lines == nil {
			t.Fatalf("%s should highlight (nil means lexer compile failed)", c.file)
		}
		src := strings.Split(c.content, "\n")
		if len(lines) != len(src) {
			t.Fatalf("%s row count %d != line count %d", c.file, len(lines), len(src))
		}
		for i, row := range lines {
			var b strings.Builder
			for _, seg := range row {
				b.WriteString(seg.Text)
			}
			if b.String() != src[i] {
				t.Fatalf("%s line %d mismatch: %q != %q", c.file, i, b.String(), src[i])
			}
		}
	}
}

func TestNtsTokenTypes(t *testing.T) {
	tokens := tokensFor(t, "req.nts", ntsFixture)

	for _, kw := range []string{"ref", "url", "type", "header", "auth", "body"} {
		if !hasToken(tokens, chroma.Keyword, kw) {
			t.Errorf("statement keyword %q not lexed as Keyword", kw)
		}
	}
	// Inside body { } the same word is an object key, never a keyword.
	if !hasToken(tokens, chroma.NameAttribute, "url") {
		t.Error("body key `url` should be NameAttribute")
	}
	if !hasToken(tokens, chroma.NameConstant, "post") {
		t.Error("HTTP method after `type` should be NameConstant")
	}
	for _, macro := range []string{"@i", "@joint", "@pick", "@run"} {
		if !hasToken(tokens, chroma.NameFunction, macro) {
			t.Errorf("macro %q not lexed as NameFunction", macro)
		}
	}
	if !hasToken(tokens, chroma.OperatorWord, "or") {
		t.Error("`or` default should be OperatorWord")
	}
	if !hasToken(tokens, chroma.LiteralNumber, "20") {
		t.Error("macro default 20 should be LiteralNumber")
	}
	if !hasToken(tokens, chroma.KeywordConstant, "false") {
		t.Error("terminated `false` should be KeywordConstant")
	}
	for _, tok := range tokens {
		if tok.Type == chroma.KeywordConstant && strings.Contains(tok.Value, "-") {
			t.Errorf("bare string %q wrongly lexed as constant", tok.Value)
		}
	}
	if !hasToken(tokens, chroma.Operator, "->") {
		t.Error("joint arrow should be Operator")
	}
	if !hasToken(tokens, chroma.LiteralStringSingle, "'trace'") {
		t.Error("@joint trace id should be LiteralStringSingle")
	}
}

func TestNtdTokenTypes(t *testing.T) {
	tokens := tokensFor(t, "data.ntd", ntdFixture)

	if !hasToken(tokens, chroma.NameAttribute, "host") {
		t.Error("top-level key should be NameAttribute")
	}
	if !hasToken(tokens, chroma.Keyword, "query") {
		t.Error("`query` should be Keyword")
	}
	if !hasToken(tokens, chroma.CommentSingle, "# selection comment") {
		t.Error("# comment inside selection set should be CommentSingle")
	}
	if !hasToken(tokens, chroma.NameFunction, "@env") {
		t.Error("@env embedded in bare value should be NameFunction")
	}
	if !hasToken(tokens, chroma.NameVariable, "$limit") {
		t.Error("GraphQL variable should be NameVariable")
	}
}

func TestNteeNoErrorTokens(t *testing.T) {
	for _, c := range []struct{ file, content string }{
		{"req.nts", ntsFixture},
		{"data.ntd", ntdFixture},
	} {
		for _, tok := range tokensFor(t, c.file, c.content) {
			if tok.Type == chroma.Error {
				t.Errorf("%s produced Error token %q", c.file, tok.Value)
			}
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
