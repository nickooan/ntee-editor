package syntax

import "github.com/alecthomas/chroma/v2"

// rule builds a keyed chroma.Rule; chroma's own lexers use unkeyed literals,
// which go vet rejects outside the defining package.
func rule(pattern string, emitter chroma.Emitter, mutator chroma.Mutator) chroma.Rule {
	return chroma.Rule{Pattern: pattern, Type: emitter, Mutator: mutator}
}

// nteeSharedRules are the lexical fragments common to the ntee-r1quest .nts
// and .ntd languages (see ntee-r1quest/src/compiler/*.ohm): // comments,
// JSON-ish literals, @name(...) macros with `or` defaults, and double-quoted
// strings that may span lines and interpolate macros.
func nteeSharedRules() chroma.Rules {
	return chroma.Rules{
		// Chroma anchors every pattern at the current position with (?m) on,
		// so ^-anchored rules only fire when the position sits at a line
		// start. \n must therefore be its own token — a greedy \s+ would
		// swallow the next line's start and break the keyword rules.
		"commentsAndSpace": {
			rule(`//[^\n]*`, chroma.CommentSingle, nil),
			rule(`\n`, chroma.TextWhitespace, nil),
			rule(`[ \t]+`, chroma.TextWhitespace, nil),
		},
		// Values that may appear anywhere: macros, strings, primitives. The
		// grammar only treats numbers/booleans/null as primitives when
		// followed by whitespace, `,]}`, a comment, or EOL — the lookaheads
		// keep bare strings like `false-positive` or UUIDs plain.
		"macroAndValue": {
			rule(`(@[A-Za-z][A-Za-z0-9_-]*)(\()`,
				chroma.ByGroups(chroma.NameFunction, chroma.Punctuation),
				chroma.Push("macroArgs")),
			rule(`"`, chroma.LiteralStringDouble, chroma.Push("string")),
			rule(`-?\d+(?:\.\d+)?(?=[ \t\n,\]})]|//|$)`, chroma.LiteralNumber, nil),
			rule(`(?:true|false|null)(?=[ \t\n,\]})]|//|$)`, chroma.KeywordConstant, nil),
		},
		// Double-quoted string: JSON escapes, raw newlines allowed (GraphQL
		// bodies), macros interpolate inside.
		"string": {
			rule(`\\(?:["\\/bfnrt]|u[0-9a-fA-F]{4})`, chroma.LiteralStringEscape, nil),
			rule(`(@[A-Za-z][A-Za-z0-9_-]*)(\()`,
				chroma.ByGroups(chroma.NameFunction, chroma.Punctuation),
				chroma.Push("macroArgs")),
			rule(`"`, chroma.LiteralStringDouble, chroma.Pop(1)),
			rule(`[^"\\@]+`, chroma.LiteralStringDouble, nil),
			rule(`[@\\]`, chroma.LiteralStringDouble, nil),
		},
		// Inside @name( ... ): keys and jsonPath segments, `or` defaults,
		// @joint's single-quoted trace id, nested macros/literal defaults.
		"macroArgs": {
			rule(`\)`, chroma.Punctuation, chroma.Pop(1)),
			rule(`[ \t]+`, chroma.TextWhitespace, nil),
			rule(`\bor\b`, chroma.OperatorWord, nil),
			rule(`'(?:\\.|[^'\\\n])*'`, chroma.LiteralStringSingle, nil),
			chroma.Include("macroAndValue"),
			rule(`[A-Za-z_][A-Za-z0-9_-]*`, chroma.NameVariable, nil),
			rule(`[.,:\[\]]`, chroma.Punctuation, nil),
			rule(`.`, chroma.Text, nil),
		},
	}
}
