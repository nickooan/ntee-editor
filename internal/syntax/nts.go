package syntax

import "github.com/alecthomas/chroma/v2"

// ntsLexer highlights ntee-r1quest request scripts and joint files. Statement
// keywords are reserved only at statement heads — inside body { } braces the
// same words are ordinary object keys — so the body state carries no keyword
// rules.
var ntsLexer = chroma.MustNewLexer(
	&chroma.Config{
		Name:      "NTS",
		Aliases:   []string{"nts"},
		Filenames: []string{"*.nts"},
	},
	ntsRules,
)

func ntsRules() chroma.Rules {
	return nteeSharedRules().Merge(chroma.Rules{
		"root": {
			chroma.Include("commentsAndSpace"),
			// `type` colors the following free HTTP token; `ref` the .ntd
			// path. Trailing groups are optional — ByGroups emits nothing for
			// an absent group.
			rule(`^([ \t]*)(type)\b([ \t]*)([A-Za-z][A-Za-z0-9!#$%&'*+.^_|~-]*)?`,
				chroma.ByGroups(chroma.TextWhitespace, chroma.Keyword,
					chroma.TextWhitespace, chroma.NameConstant), nil),
			rule(`^([ \t]*)(ref)\b([ \t]*)(\S+)?`,
				chroma.ByGroups(chroma.TextWhitespace, chroma.Keyword,
					chroma.TextWhitespace, chroma.LiteralStringOther), nil),
			rule(`^([ \t]*)(url|header|authorization|auth|body)\b`,
				chroma.ByGroups(chroma.TextWhitespace, chroma.Keyword), nil),
			rule(`->`, chroma.Operator, nil),
			chroma.Include("macroAndValue"),
			rule(`\{`, chroma.Punctuation, chroma.Push("body")),
			rule(`[\[\]}(),:]`, chroma.Punctuation, nil),
			rule(`.`, chroma.Text, nil),
		},
		"body": {
			chroma.Include("commentsAndSpace"),
			rule(`\{`, chroma.Punctuation, chroma.Push()),
			rule(`\}`, chroma.Punctuation, chroma.Pop(1)),
			rule(`([A-Za-z_][A-Za-z0-9_-]*|"(?:\\.|[^"\\\n])*")([ \t]*)(:)`,
				chroma.ByGroups(chroma.NameAttribute, chroma.TextWhitespace,
					chroma.Punctuation), nil),
			chroma.Include("macroAndValue"),
			rule(`[\[\],]`, chroma.Punctuation, nil),
			rule(`.`, chroma.Text, nil),
		},
	})
}
