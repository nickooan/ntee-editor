package graphql

import (
	"strings"
	"testing"

	"github.com/vektah/gqlparser/v2/ast"
)

const basicSDL = `"""The root query."""
type Query {
  user(id: ID!): User
  users(first: Int = 10, after: String, filter: UserFilter): [User!]!
  version: String @deprecated(reason: "use meta")
}

type Mutation {
  createUser(input: UserFilter!): User
}

"A user."
type User implements Node {
  id: ID!
  name: String!
  role: Role @deprecated
  pet: Pet
}

interface Node {
  id: ID!
}

union Pet = Dog | Cat

type Dog { name: String }
type Cat { name: String }

enum Role {
  ADMIN
  "Regular user"
  MEMBER @deprecated(reason: "roles merged")
}

input UserFilter {
  name: String = "x"
}

scalar DateTime

directive @auth(role: Role) on FIELD_DEFINITION | OBJECT
`

const multiBase = `type Query {
  user: User
}

type User {
  id: ID!
}
`

const multiExtra = `extend type Query {
  extra: Int
}

extend type User {
  nickname: String
}
`

func parseBasic(t *testing.T) *Schema {
	t.Helper()
	s := ParseFiles([]NamedSource{{Rel: "schema.graphql", Content: basicSDL}})
	if len(s.Errors) != 0 {
		t.Fatalf("errors = %+v", s.Errors)
	}
	return s
}

func TestDetect(t *testing.T) {
	for _, name := range []string{"schema.graphql", "a/b.gql", "S.GraphQLS"} {
		if !Detect(name) {
			t.Errorf("Detect(%q) = false", name)
		}
	}
	for _, name := range []string{"main.go", "schema.yaml", "graphql", "x.graphql.bak"} {
		if Detect(name) {
			t.Errorf("Detect(%q) = true", name)
		}
	}
}

func TestParseFilesBasic(t *testing.T) {
	s := parseBasic(t)
	if s.Roots != (Roots{Query: "Query", Mutation: "Mutation"}) {
		t.Fatalf("roots = %+v", s.Roots)
	}
	if len(s.Defs) != 10 {
		t.Fatalf("defs = %d, want 10", len(s.Defs))
	}
	if !s.IsDefined("User") || !s.IsDefined("String") || s.IsDefined("Missing") {
		t.Fatal("IsDefined misbehaves")
	}
	if len(s.Directives) != 1 || s.Directives[0].Name != "auth" {
		t.Fatalf("directives = %+v", s.Directives)
	}
}

func TestParseFilesSchemaBlockRoots(t *testing.T) {
	s := ParseFiles([]NamedSource{{Rel: "s.graphql", Content: "schema { query: Root }\ntype Root { ok: Boolean }\n"}})
	if s.Roots.Query != "Root" {
		t.Fatalf("roots = %+v", s.Roots)
	}
}

func TestParseFilesMergeExtend(t *testing.T) {
	s := ParseFiles([]NamedSource{
		{Rel: "multi/schema.graphql", Content: multiBase},
		{Rel: "multi/extra.graphql", Content: multiExtra},
	})
	user := s.Lookup("User")
	if user == nil || user.Base == nil || len(user.Exts) != 1 {
		t.Fatalf("User = %+v", user)
	}
	fields := defFields(user)
	if len(fields) != 2 || fields[1].Name != "nickname" {
		t.Fatalf("fields = %+v", fields)
	}
	// Extension fields keep their own file anchor — the cross-file Esc story.
	if got := fields[1].Position.Src.Name; got != "multi/extra.graphql" {
		t.Fatalf("nickname src = %q", got)
	}
}

// The extend file parsing before the base file (alphabetical gather order)
// must not demote the real definition — the base backfills.
func TestParseFilesExtendBeforeBase(t *testing.T) {
	s := ParseFiles([]NamedSource{
		{Rel: "multi/extra.graphql", Content: multiExtra},
		{Rel: "multi/schema.graphql", Content: multiBase},
	})
	user := s.Lookup("User")
	if user == nil || user.Base == nil || len(user.Exts) != 1 || len(user.Notes) != 0 {
		t.Fatalf("User = %+v", user)
	}
	fields := defFields(user)
	if len(fields) != 2 || fields[0].Name != "id" || fields[1].Name != "nickname" {
		t.Fatalf("fields = %+v", fields)
	}
}

func TestParseFilesOrphanExtend(t *testing.T) {
	s := ParseFiles([]NamedSource{{Rel: "x.graphql", Content: "extend type Ghost { f: Int }\n"}})
	ghost := s.Lookup("Ghost")
	if ghost == nil || ghost.Base != nil || len(ghost.Exts) != 1 {
		t.Fatalf("Ghost = %+v", ghost)
	}
}

func TestParseFilesDuplicateBase(t *testing.T) {
	s := ParseFiles([]NamedSource{
		{Rel: "a.graphql", Content: "type T { a: Int }\n"},
		{Rel: "b.graphql", Content: "type T { b: Int }\n"},
	})
	d := s.Lookup("T")
	if d == nil || len(d.Exts) != 1 || len(d.Notes) != 1 {
		t.Fatalf("T = %+v", d)
	}
	if !strings.Contains(d.Notes[0], "b.graphql") {
		t.Fatalf("note = %q", d.Notes[0])
	}
}

func TestParseFilesPartialError(t *testing.T) {
	s := ParseFiles([]NamedSource{
		{Rel: "broken.graphql", Content: "type {\n"},
		{Rel: "good.graphql", Content: "type Ok { f: Int }\n"},
	})
	if len(s.Errors) != 1 {
		t.Fatalf("errors = %+v", s.Errors)
	}
	e := s.Errors[0]
	if e.File != "broken.graphql" || e.Line < 1 || !strings.Contains(e.Msg, "broken.graphql:") {
		t.Fatalf("error = %+v", e)
	}
	if s.Lookup("Ok") == nil {
		t.Fatal("the good file must still parse")
	}
	if s.Lookup("Ok").Kind != ast.Object {
		t.Fatalf("kind = %v", s.Lookup("Ok").Kind)
	}
}
