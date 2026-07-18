// Package syntax adapts chroma tokenization to the editor's per-line
// HighlightSegment rendering pipeline. Tokenization is always whole-buffer:
// chroma is stateful across lines (block comments, template literals), so
// per-line lexing would mis-color multi-line constructs.
package syntax

import (
	"path/filepath"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"

	"github.com/nickooan/ntee-editor/internal/view"
)

// explicitLexers pins the first-class languages; everything else falls back to
// chroma's filename matcher.
var explicitLexers = map[string]string{
	".go":  "go",
	".ts":  "typescript",
	".tsx": "tsx",
}

// LexerFor resolves the lexer for a filename, nil when the file should render
// plain.
func LexerFor(filename string) chroma.Lexer {
	var lexer chroma.Lexer
	if name, ok := explicitLexers[strings.ToLower(filepath.Ext(filename))]; ok {
		lexer = lexers.Get(name)
	} else {
		lexer = lexers.Match(filepath.Base(filename))
	}
	if lexer == nil {
		return nil
	}
	return chroma.Coalesce(lexer)
}

// HighlightLines tokenizes the whole content and buckets styled segments per
// line. The result always has exactly len(NormalizeLines(content)) rows; nil
// when the file has no lexer (render plain).
func HighlightLines(filename, content string) [][]view.HighlightSegment {
	lexer := LexerFor(filename)
	if lexer == nil {
		return nil
	}
	it, err := lexer.Tokenise(nil, content)
	if err != nil {
		return nil
	}

	lineCount := strings.Count(content, "\n") + 1
	lines := make([][]view.HighlightSegment, 1, lineCount)
	cur := 0
	for _, token := range it.Tokens() {
		seg := segmentFor(token.Type)
		parts := strings.Split(token.Value, "\n")
		for pi, part := range parts {
			if pi > 0 {
				lines = append(lines, nil)
				cur++
			}
			if part == "" {
				continue
			}
			seg.Text = part
			lines[cur] = append(lines[cur], seg)
		}
	}
	for len(lines) < lineCount {
		lines = append(lines, nil)
	}
	return lines[:lineCount]
}
