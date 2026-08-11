// Package syntax adapts chroma tokenization to the editor's per-line
// HighlightSegment rendering pipeline. Tokenization is always whole-buffer:
// chroma is stateful across lines (block comments, template literals), so
// per-line lexing would mis-color multi-line constructs.
package syntax

import (
	"path/filepath"
	"strings"
	"sync"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"

	"github.com/nickooan/ntee-editor/internal/view"
)

// explicitLexers pins the first-class languages; everything else falls back to
// chroma's filename matcher.
var explicitLexers = map[string]string{
	".go":       "go",
	".ts":       "typescript",
	".tsx":      "tsx",
	".json":     "json",
	".yaml":     "yaml",
	".yml":      "yaml",
	".sh":       "bash",
	".bash":     "bash",
	".zsh":      "bash",
	".graphql":  "graphql",
	".graphqls": "graphql",
	".gql":      "graphql", // no built-in chroma filename glob for .gql
}

// customLexers are app-defined chroma lexers (ntee-r1quest's request and data
// languages), resolved before the built-in registry.
var customLexers = map[string]chroma.Lexer{
	".nts": ntsLexer,
	".ntd": ntdLexer,
}

// lexerCache memoizes resolved (coalesced) lexers: lexers.Get/Match walk
// chroma's whole registry, and HighlightLines re-resolves on every full
// re-highlight. Keyed by extension for the pinned tables and by basename for
// the registry fallback (chroma globs can match specific basenames, e.g.
// CMakeLists.txt). Locked — highlighting runs on tea.Cmd goroutines too. A nil
// entry ("render plain") is cached like any other result.
var (
	lexerCacheMu sync.Mutex
	lexerCache   = map[string]chroma.Lexer{}
)

// LexerFor resolves the lexer for a filename, nil when the file should render
// plain.
func LexerFor(filename string) chroma.Lexer {
	ext := strings.ToLower(filepath.Ext(filename))
	key := ext
	_, isCustom := customLexers[ext]
	_, isExplicit := explicitLexers[ext]
	if !isCustom && !isExplicit {
		key = "base:" + strings.ToLower(filepath.Base(filename))
	}

	lexerCacheMu.Lock()
	lexer, cached := lexerCache[key]
	lexerCacheMu.Unlock()
	if cached {
		return lexer
	}

	switch {
	case isCustom:
		lexer = chroma.Coalesce(customLexers[ext])
	case isExplicit:
		lexer = lexers.Get(explicitLexers[ext])
	default:
		lexer = lexers.Match(filepath.Base(filename))
	}
	if lexer != nil && !isCustom {
		lexer = chroma.Coalesce(lexer)
	}

	lexerCacheMu.Lock()
	lexerCache[key] = lexer
	lexerCacheMu.Unlock()
	return lexer
}

// HighlightLines tokenizes the whole content and buckets styled segments per
// line. The result always has exactly len(NormalizeLines(content)) rows; nil
// when the file has no lexer (render plain).
func HighlightLines(filename, content string) [][]view.HighlightSegment {
	lexer := LexerFor(filename)
	if lexer == nil {
		return nil
	}
	// Count lines on the same normalization chroma tokenizes with (EnsureLF
	// turns lone \r into \n) — counting the raw content would shift every row
	// after a stray \r onto the previous row's colors.
	content = view.NormalizeLineBreaks(content)
	it, err := lexer.Tokenise(nil, content)
	if err != nil {
		return nil
	}

	lineCount := strings.Count(content, "\n") + 1
	lines := make([][]view.HighlightSegment, 1, lineCount)
	cur := 0
	for _, token := range it.Tokens() {
		seg := segmentFor(token.Type)
		// The overwhelming majority of tokens hold no newline — appending
		// directly skips a strings.Split allocation per token.
		if strings.IndexByte(token.Value, '\n') < 0 {
			if token.Value != "" {
				seg.Text = token.Value
				lines[cur] = append(lines[cur], seg)
			}
			continue
		}
		for pi, part := range strings.Split(token.Value, "\n") {
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
