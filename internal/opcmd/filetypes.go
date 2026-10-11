package opcmd

import (
	"fmt"
	"path"
	"slices"
	"strings"
)

// AllFileTypes is the file-type spec matching every file.
const AllFileTypes = "*"

// BuiltinFileTypes are the file types offered while typing a command's TYPE.
var BuiltinFileTypes = []Candidate{
	{"go", "Go"}, {"rb", "Ruby"}, {"py", "Python"}, {"js", "JavaScript"}, {"ts", "TypeScript"},
	{"tsx", "TypeScript JSX"}, {"jsx", "JavaScript JSX"}, {"json", "JSON"}, {"yaml", "YAML"},
	{"yml", "YAML"}, {"toml", "TOML"}, {"md", "Markdown"}, {"sh", "shell script"}, {"bash", "Bash"},
	{"zsh", "Zsh"}, {"tf", "Terraform"}, {"tfvars", "Terraform variables"}, {"hcl", "HCL"},
	{"sql", "SQL"}, {"rs", "Rust"}, {"java", "Java"}, {"kt", "Kotlin"}, {"swift", "Swift"},
	{"c", "C"}, {"h", "C header"}, {"cpp", "C++"}, {"cs", "C#"}, {"php", "PHP"}, {"lua", "Lua"},
	{"html", "HTML"}, {"css", "CSS"}, {"scss", "SCSS"}, {"proto", "Protocol Buffers"},
	{"graphql", "GraphQL"}, {"erb", "Ruby template"}, {"rake", "Rake"}, {"xml", "XML"},
	{"vue", "Vue"}, {"dart", "Dart"}, {"ex", "Elixir"}, {"exs", "Elixir script"}, {"env", "dotenv"},
	{"dockerfile", "Dockerfile (whole name)"}, {"makefile", "Makefile (whole name)"},
}

// NormalizeFileTypes cleans a TYPE spec into its stored form: items split on
// ",", trimmed, without a leading ".", lowercased, de-duplicated, and joined
// with ",". An empty spec, or one containing "*", is AllFileTypes.
func NormalizeFileTypes(spec string) (string, error) {
	var items []string
	for _, raw := range strings.Split(spec, ",") {
		item := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(raw), "."))
		switch {
		case item == "":
			continue
		case item == AllFileTypes:
			return AllFileTypes, nil
		case strings.IndexFunc(item, invalidFileTypeRune) >= 0:
			return "", fmt.Errorf("file type %q: use letters, digits, . _ + -", strings.TrimSpace(raw))
		}
		if !slices.Contains(items, item) {
			items = append(items, item)
		}
	}
	if len(items) == 0 {
		return AllFileTypes, nil
	}
	return strings.Join(items, ","), nil
}

func invalidFileTypeRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		return false
	}
	return !strings.ContainsRune("._+-", r)
}

// FileTypeOf is the type a file is matched by: its extension without the dot,
// or the lowercased base name when it has none (Dockerfile → dockerfile).
func FileTypeOf(fileName string) string {
	base := strings.ToLower(path.Base(fileName))
	if extension := strings.TrimPrefix(path.Ext(base), "."); extension != "" {
		return extension
	}
	return base
}

// MatchFileType reports whether a normalized spec applies to fileName, and
// whether it did so by naming the type (specific) rather than through "*".
// An item matches the file's extension or its whole base name.
func MatchFileType(types, fileName string) (matched, specific bool) {
	if types == "" || types == AllFileTypes {
		return true, false
	}
	base := strings.ToLower(path.Base(fileName))
	extension := strings.TrimPrefix(path.Ext(base), ".")
	for _, item := range strings.Split(types, ",") {
		if item == base || (extension != "" && item == extension) {
			return true, true
		}
	}
	return false, false
}

// FileTypesOverlap reports whether two normalized specs claim a common file,
// which is what makes two commands with the same name conflict. "*" only
// overlaps "*": a specific type deliberately overrides it.
func FileTypesOverlap(first, second string) bool {
	firstAll := first == "" || first == AllFileTypes
	secondAll := second == "" || second == AllFileTypes
	if firstAll || secondAll {
		return firstAll && secondAll
	}
	secondItems := strings.Split(second, ",")
	for _, item := range strings.Split(first, ",") {
		if slices.Contains(secondItems, item) {
			return true
		}
	}
	return false
}

// CompleteFileType suggests file types for the comma item ending at cursor (a
// rune index), returning where that item starts. preferred (the open file's
// type) is offered first; an exact match of the typed text ranks above it;
// types already in the list are skipped; "*" is offered only while the rest
// of the spec is empty.
func CompleteFileType(value string, cursor int, preferred string) (start int, candidates []Candidate) {
	runes := []rune(value)
	if cursor < 0 || cursor > len(runes) {
		return 0, nil
	}
	start = cursor
	for start > 0 && runes[start-1] != ',' {
		start--
	}
	for start < cursor && runes[start] == ' ' {
		start++
	}
	typed := strings.ToLower(string(runes[start:cursor]))
	prefix := strings.TrimPrefix(typed, ".")
	if strings.IndexFunc(prefix, func(r rune) bool { return r != '*' && invalidFileTypeRune(r) }) >= 0 {
		return start, nil
	}

	itemEnd := cursor
	for itemEnd < len(runes) && runes[itemEnd] != ',' {
		itemEnd++
	}
	rest := string(runes[:start]) + string(runes[itemEnd:])
	var listed []string
	for _, item := range strings.Split(rest, ",") {
		if item = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(item), ".")); item != "" {
			listed = append(listed, item)
		}
	}

	seen := map[string]bool{}
	add := func(candidate Candidate) {
		if seen[candidate.Insert] || slices.Contains(listed, candidate.Insert) || !strings.HasPrefix(candidate.Insert, prefix) {
			return
		}
		seen[candidate.Insert] = true
		candidates = append(candidates, candidate)
	}
	if preferred != "" {
		add(Candidate{Insert: preferred, Detail: "open file"})
	}
	if len(listed) == 0 {
		add(Candidate{Insert: AllFileTypes, Detail: "all files"})
	}
	for _, candidate := range BuiltinFileTypes {
		add(candidate)
	}
	if exact := slices.IndexFunc(candidates, func(candidate Candidate) bool { return candidate.Insert == prefix }); exact > 0 {
		candidates = append(append([]Candidate{candidates[exact]}, candidates[:exact]...), candidates[exact+1:]...)
	}
	return start, candidates
}
