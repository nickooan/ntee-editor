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
	literal  string
	argIndex int    // 1-based; 0 when not a positional placeholder
	system   string // system variable name; "" when not a system placeholder
}

// Template is a parsed op-command template.
type Template struct {
	Source   string
	segments []segment
	// MaxArg is the highest positional placeholder referenced ({$3} → 3); the
	// rendered command needs exactly this many user args.
	MaxArg int
	// System lists the distinct system variables referenced, in first-use order.
	System []string
}

// Parse splits source into literal text and placeholders. "{$" always opens a
// placeholder, so a malformed or unknown one is an error rather than silently
// passed through to the shell.
func Parse(source string) (Template, error) {
	template := Template{Source: source}
	var literal strings.Builder
	rest := source
	for {
		start := strings.Index(rest, "{$")
		if start < 0 {
			literal.WriteString(rest)
			break
		}
		literal.WriteString(rest[:start])
		end := strings.IndexByte(rest[start:], '}')
		if end < 0 {
			return Template{}, fmt.Errorf("unclosed placeholder %q", rest[start:])
		}
		name := rest[start+2 : start+end]
		rest = rest[start+end+1:]

		if literal.Len() > 0 {
			template.segments = append(template.segments, segment{literal: literal.String()})
			literal.Reset()
		}
		placeholder, err := parsePlaceholder(name)
		if err != nil {
			return Template{}, err
		}
		template.segments = append(template.segments, placeholder)
		template.MaxArg = max(template.MaxArg, placeholder.argIndex)
		if placeholder.system != "" && !slices.Contains(template.System, placeholder.system) {
			template.System = append(template.System, placeholder.system)
		}
	}
	if literal.Len() > 0 {
		template.segments = append(template.segments, segment{literal: literal.String()})
	}
	return template, nil
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

// Render substitutes args and system values into the template, shell-quoting
// each value. The arg count must match MaxArg exactly: a missing arg would run
// a half-built command, an extra one usually means a forgotten quote.
func (t Template) Render(args []string, system map[string]string) (string, error) {
	if len(args) != t.MaxArg {
		return "", fmt.Errorf("needs %d args, got %d", t.MaxArg, len(args))
	}
	var out strings.Builder
	for _, part := range t.segments {
		switch {
		case part.argIndex > 0:
			out.WriteString(ShellQuote(args[part.argIndex-1]))
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
