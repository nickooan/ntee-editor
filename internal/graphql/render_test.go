package graphql

import (
	"strings"
	"testing"
)

func renderBasic(t *testing.T) ([]Line, []OutlineEntry, []string) {
	t.Helper()
	s := parseBasic(t)
	lines, outline := RenderSchema(s, 72)
	return lines, outline, Plain(lines)
}

// findLine returns the index of the first row whose text contains sub.
func findLine(t *testing.T, plain []string, sub string) int {
	t.Helper()
	for i, l := range plain {
		if strings.Contains(l, sub) {
			return i
		}
	}
	t.Fatalf("no rendered line contains %q:\n%s", sub, strings.Join(plain, "\n"))
	return -1
}

func TestRenderLayout(t *testing.T) {
	_, _, plain := renderBasic(t)

	if plain[0] != "  GraphQL schema  10 types · 1 files" {
		t.Errorf("title = %q", plain[0])
	}
	findLine(t, plain, "── Queries ")
	findLine(t, plain, "── Mutations ")
	findLine(t, plain, "── Types ")
	findLine(t, plain, "── Interfaces ")
	findLine(t, plain, "── Inputs ")
	findLine(t, plain, "── Enums ")
	findLine(t, plain, "── Unions ")
	findLine(t, plain, "── Scalars ")
	findLine(t, plain, "── Directives ")

	// Root field with ≤2 args renders inline, operation-style.
	findLine(t, plain, "user(id: ID!): User")
	// >2 args break out one per row.
	head := findLine(t, plain, "users(…): [User!]!")
	block := strings.Join(plain[head:head+4], "\n")
	for _, arg := range []string{"first", "Int  = 10", "after", "filter"} {
		if !strings.Contains(block, arg) {
			t.Errorf("breakout args missing %q:\n%s", arg, block)
		}
	}
	findLine(t, plain, "version: String  deprecated: use meta")

	findLine(t, plain, "type User implements Node {")
	findLine(t, plain, "A user.")
	findLine(t, plain, "role: Role  deprecated")
	findLine(t, plain, "interface Node {")
	findLine(t, plain, "union Pet =")
	findLine(t, plain, "‣ Dog")
	findLine(t, plain, "MEMBER  deprecated: roles merged")
	findLine(t, plain, `name: String  = "x"`)
	findLine(t, plain, "scalar DateTime")
	findLine(t, plain, "directive @auth(role: Role) on FIELD_DEFINITION | OBJECT")
}

func TestRenderOutline(t *testing.T) {
	_, outline, _ := renderBasic(t)
	var headers, entries []string
	kinds := map[string]string{}
	for _, e := range outline {
		if e.Depth == 0 {
			headers = append(headers, e.Label)
		} else {
			entries = append(entries, e.Label)
			kinds[e.Label] = e.Kind
		}
	}
	wantHeaders := "Queries Mutations Types Interfaces Inputs Enums Unions Scalars Directives"
	if got := strings.Join(headers, " "); got != wantHeaders {
		t.Fatalf("headers = %q", got)
	}
	wantEntries := "user users version createUser User Dog Cat Node UserFilter Role Pet DateTime @auth"
	if got := strings.Join(entries, " "); got != wantEntries {
		t.Fatalf("entries = %q", got)
	}
	for label, kind := range map[string]string{
		"user": "query", "createUser": "mutation", "User": "type", "Node": "interface",
		"UserFilter": "input", "Role": "enum", "Pet": "union", "DateTime": "scalar", "@auth": "directive",
	} {
		if kinds[label] != kind {
			t.Errorf("kind[%s] = %q, want %q", label, kinds[label], kind)
		}
	}
}

func TestRenderSources(t *testing.T) {
	s := parseBasic(t)
	lines, _ := RenderSchema(s, 72)
	plain := Plain(lines)
	// Every row carries an anchor into the source file.
	for i, l := range lines {
		if l.Src.File != "schema.graphql" || l.Src.Line < 1 {
			t.Fatalf("row %d (%q) src = %+v", i, plain[i], l.Src)
		}
	}
	// The head anchors on the definition, fields on their own lines.
	if got := lines[findLine(t, plain, "type User implements Node {")].Src.Line; got != 13 {
		t.Errorf("User head line = %d, want 13", got)
	}
	if got := lines[findLine(t, plain, "name: String!")].Src.Line; got != 15 {
		t.Errorf("name field line = %d, want 15", got)
	}
}

func TestRenderCrossFileExtend(t *testing.T) {
	s := ParseFiles([]NamedSource{
		{Rel: "multi/schema.graphql", Content: multiBase},
		{Rel: "multi/extra.graphql", Content: multiExtra},
	})
	lines, _ := RenderSchema(s, 72)
	plain := Plain(lines)

	findLine(t, plain, "(+1 extend)")
	// A root field merged from the second file anchors there.
	if got := lines[findLine(t, plain, "extra: Int")].Src.File; got != "multi/extra.graphql" {
		t.Errorf("extra src = %q", got)
	}
	if got := lines[findLine(t, plain, "nickname")].Src.File; got != "multi/extra.graphql" {
		t.Errorf("nickname src = %q", got)
	}
	if got := lines[findLine(t, plain, "id: ID!")].Src.File; got != "multi/schema.graphql" {
		t.Errorf("id src = %q", got)
	}
}

func TestRenderUnresolvedType(t *testing.T) {
	s := ParseFiles([]NamedSource{{Rel: "u.graphql", Content: "type Query { thing(w: Wat): Missing! }\n"}})
	lines, _ := RenderSchema(s, 72)
	plain := Plain(lines)
	row := plain[findLine(t, plain, "thing")]
	if !strings.Contains(row, "unresolved type") {
		t.Fatalf("row = %q", row)
	}
}

func TestRenderParseErrorRow(t *testing.T) {
	s := ParseFiles([]NamedSource{
		{Rel: "broken.graphql", Content: "type {\n"},
		{Rel: "good.graphql", Content: "type Ok { f: Int }\n"},
	})
	lines, _ := RenderSchema(s, 72)
	plain := Plain(lines)
	row := findLine(t, plain, "parse error: broken.graphql:")
	if lines[row].Src.File != "broken.graphql" {
		t.Fatalf("error row src = %+v", lines[row].Src)
	}
	findLine(t, plain, "type Ok {")
}

func TestRenderOrphanExtend(t *testing.T) {
	s := ParseFiles([]NamedSource{{Rel: "x.graphql", Content: "extend type Ghost { f: Int }\n"}})
	lines, _ := RenderSchema(s, 72)
	plain := Plain(lines)
	findLine(t, plain, "extend type Ghost  (base not defined)")
	findLine(t, plain, "f: Int")
}

func TestRenderConfigNotes(t *testing.T) {
	s := parseBasic(t)
	s.Config = ".graphqlrc.yml"
	s.Notes = []string{"skipped schema entry: https://x"}
	lines, _ := RenderSchema(s, 72)
	plain := Plain(lines)
	findLine(t, plain, "via .graphqlrc.yml")
	findLine(t, plain, "skipped schema entry: https://x")
}
