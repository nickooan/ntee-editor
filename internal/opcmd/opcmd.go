// Package opcmd parses op-command templates — user-saved shell command lines
// with {$1}, {$2}, … positional placeholders and {$name} system variables —
// and renders them into an executable command line.
package opcmd

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// System variables, both relative to the workspace directory (the directory
// the editor was opened on, where commands run).
const (
	SystemFilePath = "fpath" // the open file
	SystemDirPath  = "dpath" // the open file's directory ("." at the top level)
)

// SystemVars lists the system variables a template may reference.
var SystemVars = []string{SystemFilePath, SystemDirPath}

// SystemVarDetails describes each system variable for completion menus.
var SystemVarDetails = map[string]string{
	SystemFilePath: "open file, workspace-relative",
	SystemDirPath:  "open file's directory",
}

type segment struct {
	literal      string
	argIndex     int    // 1-based; 0 when not a positional placeholder
	system       string // system variable name; "" when not a system placeholder
	defaultValue string // {$n or default}; an empty default renders as nothing
	hasDefault   bool
}

// Template is a parsed op-command template.
type Template struct {
	Source   string
	segments []segment
	// MaxArg is the highest positional placeholder referenced ({$3} → 3).
	MaxArg int
	// MinArg is how many arguments are required: indices referenced at least
	// once without a default. A run takes between MinArg and MaxArg args (see
	// Bind for how they are assigned).
	MinArg int
	// required[i] reports whether index i+1 is required.
	required []bool
	// System lists the distinct system variables referenced, in first-use order.
	System []string
}

// ArgSpec describes one positional argument for the UI.
type ArgSpec struct {
	Index    int
	Default  string // the first default given for it, when Optional
	Optional bool
}

// Binding is the value one argument index receives for a run.
type Binding struct {
	Index       int
	Value       string
	FromDefault bool
}

// Parse splits source into literal text and placeholders. "{$" always opens a
// placeholder, so a malformed or unknown one is an error rather than silently
// passed through to the shell.
func Parse(source string) (Template, error) {
	template := Template{Source: source}
	var quotes quoteTracker
	position := 0
	for {
		found := strings.Index(source[position:], "{$")
		if found < 0 {
			break
		}
		start := position + found
		if start > position {
			template.segments = append(template.segments, segment{literal: source[position:start]})
		}
		quotes.advance(source[position:start])
		placeholder, end, err := scanPlaceholder(source, start)
		if err != nil {
			return Template{}, err
		}
		if err := quotes.check(source[start:end]); err != nil {
			return Template{}, err
		}
		template.segments = append(template.segments, placeholder)
		template.MaxArg = max(template.MaxArg, placeholder.argIndex)
		if placeholder.system != "" && !slices.Contains(template.System, placeholder.system) {
			template.System = append(template.System, placeholder.system)
		}
		position = end
	}
	if position < len(source) {
		template.segments = append(template.segments, segment{literal: source[position:]})
	}
	template.required = make([]bool, template.MaxArg)
	for _, part := range template.segments {
		if part.argIndex > 0 && !part.hasDefault && !template.required[part.argIndex-1] {
			template.required[part.argIndex-1] = true
			template.MinArg++
		}
	}
	return template, nil
}

const (
	unquoted     = 0
	singleQuoted = '\''
	doubleQuoted = '"'
	ansiCQuoted  = '$' // $'…'
)

// quoteTracker follows sh quoting through a template's literal text. Render
// quotes each value as a standalone word, which is only safe outside quotes
// and not glued to a preceding \ or $: inside "…" the added '…' is literal
// (so $(…) in a value would run), \ would escape the opening ', and $ would
// turn it into a $'…' string that interprets backslashes in the value.
type quoteTracker struct {
	quote   byte
	pending byte // an unescaped \ or $ ending the latest literal
}

func (tracker *quoteTracker) advance(literal string) {
	tracker.pending = 0
	for index := 0; index < len(literal); index++ {
		character := literal[index]
		switch tracker.quote {
		case unquoted:
			switch character {
			case '\\':
				if index+1 == len(literal) {
					tracker.pending = character
				}
				index++
			case '\'', '"':
				tracker.quote = character
			case '$':
				switch {
				case index+1 == len(literal):
					tracker.pending = character
				case literal[index+1] == '\'':
					tracker.quote = ansiCQuoted
					index++
				}
			}
		case singleQuoted:
			if character == '\'' {
				tracker.quote = unquoted
			}
		default:
			switch {
			case character == '\\':
				index++
			case character == '"' && tracker.quote == doubleQuoted,
				character == '\'' && tracker.quote == ansiCQuoted:
				tracker.quote = unquoted
			}
		}
	}
}

// check reports why a placeholder can't sit where the tracker stands.
func (tracker quoteTracker) check(placeholder string) error {
	switch {
	case tracker.quote == doubleQuoted:
		return fmt.Errorf(`%s is inside "…" quotes — remove them, values are quoted for you`, placeholder)
	case tracker.quote != unquoted:
		return fmt.Errorf(`%s is inside '…' quotes — remove them, values are quoted for you`, placeholder)
	case tracker.pending != 0:
		return fmt.Errorf(`%s can't directly follow %c — put a space between them`, placeholder, tracker.pending)
	}
	return nil
}

// scanPlaceholder reads the placeholder opening at start ("{$"):
//
//	{$NAME}  |  {$N or WORD}  |  {$N or "TEXT"}
//
// A quoted default may hold spaces and "}" (with \" and \\ escapes). It
// returns the segment and the offset just past the closing "}".
func scanPlaceholder(source string, start int) (segment, int, error) {
	unclosed := func() (segment, int, error) {
		return segment{}, 0, fmt.Errorf("unclosed placeholder %q", source[start:])
	}
	position := start + 2
	nameEnd := position
	for nameEnd < len(source) && isNameByte(source[nameEnd]) {
		nameEnd++
	}
	name := source[position:nameEnd]
	position = skipSpaces(source, nameEnd)

	var defaultValue string
	hasDefault := false
	if strings.HasPrefix(source[position:], "or") &&
		(position+2 == len(source) || source[position+2] == ' ' || source[position+2] == '\t' || source[position+2] == '}') {
		hasDefault = true
		position = skipSpaces(source, position+2)
		switch {
		case position >= len(source):
			return unclosed()
		case source[position] == '}':
			return segment{}, 0, fmt.Errorf("{$%s or …}: missing default value (use \"\" for empty)", name)
		case source[position] == '"':
			value, end, ok := scanQuoted(source, position)
			if !ok {
				return segment{}, 0, fmt.Errorf("{$%s or …}: unterminated \" in default", name)
			}
			defaultValue, position = value, end
		default:
			wordEnd := position
			for wordEnd < len(source) && !strings.ContainsRune(" \t}\"", rune(source[wordEnd])) {
				wordEnd++
			}
			defaultValue, position = source[position:wordEnd], wordEnd
		}
		position = skipSpaces(source, position)
	}
	if position >= len(source) {
		return unclosed()
	}
	if source[position] != '}' {
		closing := strings.IndexByte(source[position:], '}')
		if closing < 0 {
			return unclosed()
		}
		return segment{}, 0, fmt.Errorf("unexpected %q in %s (defaults are {$n or value})",
			strings.TrimSpace(source[position:position+closing]), source[start:position+closing+1])
	}

	placeholder, err := parsePlaceholder(name)
	if err != nil {
		return segment{}, 0, err
	}
	if hasDefault {
		if placeholder.system != "" {
			return segment{}, 0, fmt.Errorf("{$%s or …}: only arguments take defaults", name)
		}
		placeholder.defaultValue, placeholder.hasDefault = defaultValue, true
	}
	return placeholder, position + 1, nil
}

// scanQuoted reads a "…" default starting at the opening quote, unescaping
// \" and \\. It returns the value and the offset past the closing quote.
func scanQuoted(source string, start int) (string, int, bool) {
	var value strings.Builder
	for position := start + 1; position < len(source); position++ {
		switch source[position] {
		case '"':
			return value.String(), position + 1, true
		case '\\':
			if position+1 < len(source) && (source[position+1] == '"' || source[position+1] == '\\') {
				position++
			}
		}
		value.WriteByte(source[position])
	}
	return "", 0, false
}

func isNameByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

func skipSpaces(source string, position int) int {
	for position < len(source) && (source[position] == ' ' || source[position] == '\t') {
		position++
	}
	return position
}

func parsePlaceholder(name string) (segment, error) {
	if name == "" {
		return segment{}, fmt.Errorf("empty placeholder {$}")
	}
	if name[0] >= '0' && name[0] <= '9' {
		index, err := strconv.Atoi(name)
		if err != nil || index < 1 {
			return segment{}, fmt.Errorf("invalid argument placeholder {$%s} (args start at {$1})", name)
		}
		return segment{argIndex: index}, nil
	}
	if !slices.Contains(SystemVars, name) {
		return segment{}, fmt.Errorf("unknown variable {$%s} (available: %s)", name, strings.Join(SystemVars, ", "))
	}
	return segment{system: name}, nil
}

// ArgSpecs lists arguments 1..MaxArg: optional ones have a default (an index
// never referenced counts as optional with an empty default).
func (t Template) ArgSpecs() []ArgSpec {
	defaults := t.firstDefaults()
	specs := make([]ArgSpec, t.MaxArg)
	for index := range specs {
		specs[index] = ArgSpec{Index: index + 1, Optional: !t.required[index]}
		if specs[index].Optional {
			specs[index].Default = defaults[index]
		}
	}
	return specs
}

func (t Template) firstDefaults() []string {
	defaults := make([]string, t.MaxArg)
	seen := make([]bool, t.MaxArg)
	for _, part := range t.segments {
		if part.argIndex > 0 && part.hasDefault && !seen[part.argIndex-1] {
			seen[part.argIndex-1] = true
			defaults[part.argIndex-1] = part.defaultValue
		}
	}
	return defaults
}

// Bind assigns typed args to argument indices:
//   - exactly MinArg args fill only the required indices, in order, so a
//     default in the middle ({$1} {$2 or x} {$3}) never has to be typed;
//   - more fill indices 1..n in order, and every index past n must have a
//     default (an uncovered required index is an error);
//   - fewer than MinArg, or more than MaxArg, is an error.
func (t Template) Bind(args []string) ([]Binding, error) {
	count := len(args)
	switch {
	case count < t.MinArg || (count > t.MaxArg && t.MinArg == t.MaxArg):
		return nil, t.countError(count)
	case count > t.MaxArg:
		return nil, fmt.Errorf("takes at most %d args, got %d", t.MaxArg, count)
	}
	defaults := t.firstDefaults()
	bindings := make([]Binding, t.MaxArg)
	next := 0
	for index := range bindings {
		binding := Binding{Index: index + 1}
		switch {
		case count == t.MinArg && t.required[index], count > t.MinArg && index < count:
			binding.Value = args[next]
			next++
		case t.required[index]:
			return nil, fmt.Errorf("with %d args, $1–$%d are filled in order; $%d has no default", count, count, index+1)
		default:
			binding.Value, binding.FromDefault = defaults[index], true
		}
		bindings[index] = binding
	}
	return bindings, nil
}

func (t Template) countError(count int) error {
	if t.MinArg == t.MaxArg {
		return fmt.Errorf("needs %s, got %d", Plural(t.MaxArg, "arg"), count)
	}
	var required []string
	for index, isRequired := range t.required {
		if isRequired {
			required = append(required, "$"+strconv.Itoa(index+1))
		}
	}
	return fmt.Errorf("needs %s (%s), got %d", Plural(t.MinArg, "arg"), strings.Join(required, ", "), count)
}

// Plural formats a count with its noun: "1 arg", "2 args".
func Plural(count int, noun string) string {
	if count == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(count) + " " + noun + "s"
}

// Render substitutes args (assigned by Bind) and system values into the
// template, shell-quoting each value. An omitted argument uses the default
// written at that placeholder; an empty default renders as nothing.
func (t Template) Render(args []string, system map[string]string) (string, error) {
	bindings, err := t.Bind(args)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	for _, part := range t.segments {
		switch {
		case part.argIndex > 0:
			binding := bindings[part.argIndex-1]
			if !binding.FromDefault {
				out.WriteString(ShellQuote(binding.Value))
			} else if part.defaultValue != "" {
				// A defaulted index is never required, so every occurrence
				// carries its own default.
				out.WriteString(ShellQuote(part.defaultValue))
			}
		case part.system != "":
			value, ok := system[part.system]
			if !ok {
				return "", fmt.Errorf("{$%s} is not available", part.system)
			}
			out.WriteString(ShellQuote(value))
		default:
			out.WriteString(part.literal)
		}
	}
	return out.String(), nil
}

// SpanKind classifies a placeholder in template text.
type SpanKind uint8

const (
	SpanArg SpanKind = iota + 1
	SpanSystem
	SpanInvalid
)

// Span is one placeholder in a template, as byte offsets [Start, End).
// Index is the argument number for SpanArg.
type Span struct {
	Start, End int
	Kind       SpanKind
	Index      int
}

// Spans finds every placeholder in source with the parser's own scanner, so
// the editor's coloring always agrees with what Parse accepts. A broken
// placeholder spans to its next "}" (or the end).
func Spans(source string) []Span {
	var spans []Span
	var quotes quoteTracker
	for position := 0; ; {
		found := strings.Index(source[position:], "{$")
		if found < 0 {
			return spans
		}
		start := position + found
		quotes.advance(source[position:start])
		placeholder, end, err := scanPlaceholder(source, start)
		span := Span{Start: start, End: end, Kind: SpanSystem}
		switch {
		case err != nil:
			span.Kind, span.End = SpanInvalid, len(source)
			if closing := strings.IndexByte(source[start:], '}'); closing >= 0 {
				span.End = start + closing + 1
			}
		case quotes.check("") != nil:
			span.Kind = SpanInvalid
		case placeholder.argIndex > 0:
			span.Kind, span.Index = SpanArg, placeholder.argIndex
		}
		spans = append(spans, span)
		position = span.End
	}
}

// ShellQuote returns value unchanged when sh would read it as one plain word,
// and single-quoted otherwise. Either way the program receives the same argv.
func ShellQuote(value string) string {
	if value == "" {
		return "''"
	}
	if strings.IndexFunc(value, needsQuoting) < 0 {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func needsQuoting(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return false
	}
	return !strings.ContainsRune("_@%+=:,./-", r)
}

// SplitArgs splits the user's argument line the way sh splits words:
// whitespace separates, '…' is literal, "…" groups (with \" and \\ escapes),
// and a bare backslash escapes the next character.
func SplitArgs(line string) ([]string, error) {
	var (
		args    []string
		current strings.Builder
		inWord  bool
	)
	runes := []rune(line)
	for index := 0; index < len(runes); index++ {
		r := runes[index]
		switch {
		case r == ' ' || r == '\t':
			if inWord {
				args = append(args, current.String())
				current.Reset()
				inWord = false
			}
		case r == '\'':
			inWord = true
			closing := slices.Index(runes[index+1:], '\'')
			if closing < 0 {
				return nil, fmt.Errorf("unterminated ' quote")
			}
			current.WriteString(string(runes[index+1 : index+1+closing]))
			index += closing + 1
		case r == '"':
			inWord = true
			closed := false
			for index++; index < len(runes); index++ {
				if runes[index] == '"' {
					closed = true
					break
				}
				if runes[index] == '\\' && index+1 < len(runes) && (runes[index+1] == '"' || runes[index+1] == '\\') {
					index++
				}
				current.WriteRune(runes[index])
			}
			if !closed {
				return nil, fmt.Errorf("unterminated \" quote")
			}
		case r == '\\' && index+1 < len(runes):
			inWord = true
			index++
			current.WriteRune(runes[index])
		default:
			inWord = true
			current.WriteRune(r)
		}
	}
	if inWord {
		args = append(args, current.String())
	}
	return args, nil
}
