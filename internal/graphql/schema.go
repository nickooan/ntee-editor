package graphql

import (
	"errors"
	"fmt"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"github.com/vektah/gqlparser/v2/parser"
)

// NamedSource is one SDL file's content, keyed by its workspace-relative
// path. The path becomes ast.Source.Name, so every parsed node's Position
// carries the file it came from — the SourceRef story for cross-file merges.
type NamedSource struct {
	Rel     string
	Content string
}

// FileError is one unparseable file. Rendering continues without it — the
// error surfaces as an inline document row anchored to the broken spot.
type FileError struct {
	File string
	Line int
	Msg  string
}

// Def is one named type merged across files: the base definition (nil when
// only `extend` blocks were seen) plus every extension, from any file. Each
// ast node keeps its own Position, so no per-file bookkeeping is needed.
type Def struct {
	Name  string
	Kind  ast.DefinitionKind
	Base  *ast.Definition
	Exts  []*ast.Definition
	Notes []string // e.g. a duplicate base definition demoted to an extension
}

// Roots are the resolved operation root type names ("" = absent).
type Roots struct {
	Query        string
	Mutation     string
	Subscription string
}

// Schema is the merged multi-file schema. Config and Notes are gathering
// metadata filled in by the caller (Gather) for the renderer's title block.
type Schema struct {
	Files      []string
	Defs       []*Def // first-declaration order across files
	Directives []*ast.DirectiveDefinition
	Roots      Roots
	Errors     []FileError
	Config     string   // config file that drove gathering ("" = same-dir default)
	Notes      []string // gathering notes, rendered dim under the title

	names map[string]*Def
}

// builtinScalars are the spec-defined scalar names, always considered defined.
var builtinScalars = map[string]bool{
	"String": true, "Int": true, "Float": true, "Boolean": true, "ID": true,
}

// ParseFiles parses each source independently and merges the results: one
// broken file becomes a FileError instead of killing the whole schema, and
// declaration order is preserved (parsing per file keeps the AST slices the
// validator's map form would lose).
func ParseFiles(sources []NamedSource) *Schema {
	s := &Schema{names: map[string]*Def{}}
	for _, src := range sources {
		s.Files = append(s.Files, src.Rel)
		doc, err := parser.ParseSchema(&ast.Source{Name: src.Rel, Input: src.Content})
		if err != nil {
			s.Errors = append(s.Errors, fileError(src.Rel, err))
			continue
		}
		for _, sd := range append(append(ast.SchemaDefinitionList{}, doc.Schema...), doc.SchemaExtension...) {
			for _, ot := range sd.OperationTypes {
				switch ot.Operation {
				case ast.Query:
					s.Roots.Query = ot.Type
				case ast.Mutation:
					s.Roots.Mutation = ot.Type
				case ast.Subscription:
					s.Roots.Subscription = ot.Type
				}
			}
		}
		s.Directives = append(s.Directives, doc.Directives...)
		for _, d := range doc.Definitions {
			if existing, ok := s.names[d.Name]; ok {
				// A name first seen as `extend` gets its base backfilled —
				// file order (alphabetical in the same-dir gather) must not
				// decide which block is "real".
				if existing.Base == nil {
					existing.Base = d
					existing.Kind = d.Kind
					continue
				}
				// Keep the first base; later duplicates still render (as
				// extensions) with a note instead of silently vanishing.
				existing.Exts = append(existing.Exts, d)
				existing.Notes = append(existing.Notes, "duplicate definition in "+src.Rel)
				continue
			}
			def := &Def{Name: d.Name, Kind: d.Kind, Base: d}
			s.names[d.Name] = def
			s.Defs = append(s.Defs, def)
		}
		for _, d := range doc.Extensions {
			if existing, ok := s.names[d.Name]; ok {
				existing.Exts = append(existing.Exts, d)
				continue
			}
			// An extend with no base anywhere still renders, marked as such.
			def := &Def{Name: d.Name, Kind: d.Kind, Exts: []*ast.Definition{d}}
			s.names[d.Name] = def
			s.Defs = append(s.Defs, def)
		}
	}
	// Without a schema{} block the roots default to the conventional names,
	// but only when such an object type actually exists.
	if s.Roots.Query == "" && s.names["Query"] != nil {
		s.Roots.Query = "Query"
	}
	if s.Roots.Mutation == "" && s.names["Mutation"] != nil {
		s.Roots.Mutation = "Mutation"
	}
	if s.Roots.Subscription == "" && s.names["Subscription"] != nil {
		s.Roots.Subscription = "Subscription"
	}
	return s
}

// IsDefined reports whether a type name resolves in the merged schema
// (including the spec builtins) — the "one hop" replacement for OpenAPI's
// $ref resolution.
func (s *Schema) IsDefined(name string) bool {
	return builtinScalars[name] || s.names[name] != nil
}

// Lookup returns the merged definition for a type name, nil when undefined.
func (s *Schema) Lookup(name string) *Def {
	return s.names[name]
}

// fileError shapes a parser error into a FileError. gqlparser's error return
// changed concrete types across v2.5.x, so unwrap via errors.As both ways.
func fileError(rel string, err error) FileError {
	fe := FileError{File: rel, Line: 1, Msg: err.Error()}
	var one *gqlerror.Error
	var list gqlerror.List
	if errors.As(err, &list) && len(list) > 0 {
		one = list[0]
	} else if !errors.As(err, &one) {
		return fe
	}
	fe.Msg = one.Message
	if len(one.Locations) > 0 && one.Locations[0].Line > 0 {
		fe.Line = one.Locations[0].Line
	}
	fe.Msg = fmt.Sprintf("%s:%d: %s", rel, fe.Line, one.Message)
	return fe
}
