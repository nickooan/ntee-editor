package syntax

import "github.com/alecthomas/chroma/v2"

// ntdLexer highlights ntee-r1quest data/definition files: key: value entries
// plus the query/mutation GraphQL sugar, whose selection sets are the only
// place `#` starts a comment.
var ntdLexer = chroma.MustNewLexer(
	&chroma.Config{
		Name:      "NTD",
		Aliases:   []string{"ntd"},
		Filenames: []string{"*.ntd"},
	},
	ntdRules,
)

func ntdRules() chroma.Rules {
	return nteeSharedRules().Merge(chroma.Rules{
		"root": {
			chroma.Include("commentsAndSpace"),
			chroma.Include("gqlStart"),
			chroma.Include("keyColon"),
			chroma.Include("macroAndValue"),
			rule(`\{`, chroma.Punctuation, chroma.Push("object")),
			rule(`[\[\],}]`, chroma.Punctuation, nil),
			rule(`.`, chroma.Text, nil),
		},
		"object": {
			chroma.Include("commentsAndSpace"),
			rule(`\{`, chroma.Punctuation, chroma.Push()),
			rule(`\}`, chroma.Punctuation, chroma.Pop(1)),
			chroma.Include("gqlStart"),
			chroma.Include("keyColon"),
			chroma.Include("macroAndValue"),
			rule(`[\[\],]`, chroma.Punctuation, nil),
			rule(`.`, chroma.Text, nil),
		},
		"keyColon": {
			rule(`([A-Za-z][A-Za-z0-9_-]*|"(?:\\.|[^"\\\n])*")([ \t]*)(:)`,
				chroma.ByGroups(chroma.NameAttribute, chroma.TextWhitespace,
					chroma.Punctuation), nil),
		},
		// query/mutation are the language's only reserved words. keyColon is
		// included first in root/object, so a key literally named `query:`
		// still lexes as a key.
		"gqlStart": {
			rule(`(?<![A-Za-z0-9_-])(query|mutation)\b`, chroma.Keyword,
				chroma.Push("gqlHead")),
		},
		// Operation name and variable defs, up to the selection-set brace.
		"gqlHead": {
			rule(`[ \t]+`, chroma.TextWhitespace, nil),
			rule(`\{`, chroma.Punctuation,
				chroma.Mutators(chroma.Pop(1), chroma.Push("gqlBody"))),
			rule(`\$[A-Za-z_][A-Za-z0-9_]*`, chroma.NameVariable, nil),
			rule(`[A-Za-z_][A-Za-z0-9_]*!?`, chroma.NameFunction, nil),
			rule(`[(),:!\[\]=]`, chroma.Punctuation, nil),
			rule(`\n`, chroma.TextWhitespace, nil),
			rule(`.`, chroma.Text, nil),
		},
		// Brace-balanced selection set.
		"gqlBody": {
			rule(`#[^\n]*`, chroma.CommentSingle, nil),
			rule(`//[^\n]*`, chroma.CommentSingle, nil),
			rule(`\{`, chroma.Punctuation, chroma.Push()),
			rule(`\}`, chroma.Punctuation, chroma.Pop(1)),
			rule(`"`, chroma.LiteralStringDouble, chroma.Push("string")),
			rule(`\$[A-Za-z_][A-Za-z0-9_]*`, chroma.NameVariable, nil),
			rule(`-?\d+(?:\.\d+)?`, chroma.LiteralNumber, nil),
			rule(`(?:true|false|null)\b`, chroma.KeywordConstant, nil),
			rule(`[(),:!=\[\]]`, chroma.Punctuation, nil),
			rule(`\s+`, chroma.TextWhitespace, nil),
			rule(`.`, chroma.Text, nil),
		},
	})
}
