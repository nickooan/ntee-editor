// Package openapi parses the rendered subset of OpenAPI v3 documents and
// renders them as styled terminal lines. It is pure data-in/data-out: no UI
// framework, no filesystem access (file loading is injected into Resolver).
package openapi

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// OrderedMap is a YAML mapping that preserves key order and each key's source
// line — Go maps lose both, and property/path order matters for readability
// while source lines drive the exit-to-source jump.
type OrderedMap[V any] struct {
	Keys  []string
	Vals  map[string]V
	Lines map[string]int
}

func (m *OrderedMap[V]) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: expected mapping, got %s", node.Line, kindName(node.Kind))
	}
	m.Vals = make(map[string]V, len(node.Content)/2)
	m.Lines = make(map[string]int, len(node.Content)/2)
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i].Value
		var v V
		if err := node.Content[i+1].Decode(&v); err != nil {
			return err
		}
		if _, dup := m.Vals[key]; !dup {
			m.Keys = append(m.Keys, key)
		}
		m.Vals[key] = v
		m.Lines[key] = node.Content[i].Line
	}
	return nil
}

func (m *OrderedMap[V]) Get(key string) (V, bool) {
	v, ok := m.Vals[key]
	return v, ok
}

func (m *OrderedMap[V]) Len() int { return len(m.Keys) }

// Line returns the source line of a key (0 when absent).
func (m *OrderedMap[V]) Line(key string) int { return m.Lines[key] }

func kindName(k yaml.Kind) string {
	switch k {
	case yaml.DocumentNode:
		return "document"
	case yaml.SequenceNode:
		return "sequence"
	case yaml.MappingNode:
		return "mapping"
	case yaml.ScalarNode:
		return "scalar"
	case yaml.AliasNode:
		return "alias"
	}
	return "unknown"
}

// Document is the rendered subset of an OpenAPI v3 spec. File is the
// repo-relative path it was parsed from (set by the caller, not the YAML).
type Document struct {
	File       string                `yaml:"-"`
	OpenAPI    string                `yaml:"openapi"`
	Info       Info                  `yaml:"info"`
	Tags       []Tag                 `yaml:"tags"`
	Paths      OrderedMap[*PathItem] `yaml:"paths"`
	Components Components            `yaml:"components"`
}

type Info struct {
	Title       string `yaml:"title"`
	Version     string `yaml:"version"`
	Description string `yaml:"description"`
}

type Tag struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

type Components struct {
	Schemas       OrderedMap[*Schema]      `yaml:"schemas"`
	Parameters    OrderedMap[*Parameter]   `yaml:"parameters"`
	Responses     OrderedMap[*Response]    `yaml:"responses"`
	RequestBodies OrderedMap[*RequestBody] `yaml:"requestBodies"`
}

type PathItem struct {
	Line int `yaml:"-"`
	// MethodLines maps upper-case method names to their key's source line —
	// a mapping node's own Line is its first child's, so "post:" needs its
	// key line recorded separately to anchor the operation heading.
	MethodLines map[string]int `yaml:"-"`
	Get         *Operation     `yaml:"get"`
	Put         *Operation     `yaml:"put"`
	Post        *Operation     `yaml:"post"`
	Delete      *Operation     `yaml:"delete"`
	Options     *Operation     `yaml:"options"`
	Head        *Operation     `yaml:"head"`
	Patch       *Operation     `yaml:"patch"`
	Trace       *Operation     `yaml:"trace"`
	Parameters  []*Parameter   `yaml:"parameters"`
}

func (p *PathItem) UnmarshalYAML(node *yaml.Node) error {
	type alias PathItem
	var a alias
	if err := node.Decode(&a); err != nil {
		return err
	}
	*p = PathItem(a)
	p.Line = node.Line
	p.MethodLines = make(map[string]int, len(node.Content)/2)
	for i := 0; i+1 < len(node.Content); i += 2 {
		p.MethodLines[strings.ToUpper(node.Content[i].Value)] = node.Content[i].Line
	}
	return nil
}

// MethodOp pairs an HTTP method (upper-case) with its operation.
type MethodOp struct {
	Method string
	Op     *Operation
}

// Operations returns the item's operations in canonical method order.
func (p *PathItem) Operations() []MethodOp {
	all := []MethodOp{
		{"GET", p.Get}, {"POST", p.Post}, {"PUT", p.Put}, {"PATCH", p.Patch},
		{"DELETE", p.Delete}, {"OPTIONS", p.Options}, {"HEAD", p.Head}, {"TRACE", p.Trace},
	}
	var out []MethodOp
	for _, mo := range all {
		if mo.Op != nil {
			out = append(out, mo)
		}
	}
	return out
}

type Operation struct {
	Line        int                   `yaml:"-"`
	OperationID string                `yaml:"operationId"`
	Summary     string                `yaml:"summary"`
	Description string                `yaml:"description"`
	Tags        []string              `yaml:"tags"`
	Deprecated  bool                  `yaml:"deprecated"`
	Parameters  []*Parameter          `yaml:"parameters"`
	RequestBody *RequestBody          `yaml:"requestBody"`
	Responses   OrderedMap[*Response] `yaml:"responses"`
}

func (o *Operation) UnmarshalYAML(node *yaml.Node) error {
	type alias Operation
	var a alias
	if err := node.Decode(&a); err != nil {
		return err
	}
	*o = Operation(a)
	o.Line = node.Line
	return nil
}

type Parameter struct {
	Line        int     `yaml:"-"`
	Ref         string  `yaml:"$ref"`
	Name        string  `yaml:"name"`
	In          string  `yaml:"in"`
	Description string  `yaml:"description"`
	Required    bool    `yaml:"required"`
	Deprecated  bool    `yaml:"deprecated"`
	Schema      *Schema `yaml:"schema"`
}

func (p *Parameter) UnmarshalYAML(node *yaml.Node) error {
	type alias Parameter
	var a alias
	if err := node.Decode(&a); err != nil {
		return err
	}
	*p = Parameter(a)
	p.Line = node.Line
	return nil
}

type RequestBody struct {
	Line        int                    `yaml:"-"`
	Ref         string                 `yaml:"$ref"`
	Description string                 `yaml:"description"`
	Required    bool                   `yaml:"required"`
	Content     OrderedMap[*MediaType] `yaml:"content"`
}

func (r *RequestBody) UnmarshalYAML(node *yaml.Node) error {
	type alias RequestBody
	var a alias
	if err := node.Decode(&a); err != nil {
		return err
	}
	*r = RequestBody(a)
	r.Line = node.Line
	return nil
}

type MediaType struct {
	Line   int     `yaml:"-"`
	Schema *Schema `yaml:"schema"`
}

func (m *MediaType) UnmarshalYAML(node *yaml.Node) error {
	type alias MediaType
	var a alias
	if err := node.Decode(&a); err != nil {
		return err
	}
	*m = MediaType(a)
	m.Line = node.Line
	return nil
}

type Response struct {
	Line        int                    `yaml:"-"`
	Ref         string                 `yaml:"$ref"`
	Description string                 `yaml:"description"`
	Content     OrderedMap[*MediaType] `yaml:"content"`
}

func (r *Response) UnmarshalYAML(node *yaml.Node) error {
	type alias Response
	var a alias
	if err := node.Decode(&a); err != nil {
		return err
	}
	*r = Response(a)
	r.Line = node.Line
	return nil
}

// AdditionalProps is additionalProperties, which is either a bool or a schema.
type AdditionalProps struct {
	Allowed *bool
	Schema  *Schema
}

func (a *AdditionalProps) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		var b bool
		if err := node.Decode(&b); err == nil {
			a.Allowed = &b
			return nil
		}
	}
	var s Schema
	if err := node.Decode(&s); err != nil {
		return err
	}
	a.Schema = &s
	return nil
}

type Schema struct {
	Line                 int                 `yaml:"-"`
	Ref                  string              `yaml:"$ref"`
	Type                 string              `yaml:"type"`
	Format               string              `yaml:"format"`
	Description          string              `yaml:"description"`
	Required             []string            `yaml:"required"`
	Enum                 []any               `yaml:"enum"`
	Default              any                 `yaml:"default"`
	Nullable             bool                `yaml:"nullable"`
	ReadOnly             bool                `yaml:"readOnly"`
	WriteOnly            bool                `yaml:"writeOnly"`
	Deprecated           bool                `yaml:"deprecated"`
	Properties           OrderedMap[*Schema] `yaml:"properties"`
	Items                *Schema             `yaml:"items"`
	AdditionalProperties *AdditionalProps    `yaml:"additionalProperties"`
	AllOf                []*Schema           `yaml:"allOf"`
	OneOf                []*Schema           `yaml:"oneOf"`
	AnyOf                []*Schema           `yaml:"anyOf"`
}

func (s *Schema) UnmarshalYAML(node *yaml.Node) error {
	type alias Schema
	var a alias
	if err := node.Decode(&a); err != nil {
		return err
	}
	*s = Schema(a)
	s.Line = node.Line
	return nil
}

// detectRe matches the `openapi: 3…` key in YAML or JSON form.
var detectRe = regexp.MustCompile(`^\s*"?openapi"?\s*:\s*"?3`)

// Detect reports whether content looks like an OpenAPI v3 document, checking
// only the first 50 lines so it is cheap enough to run synchronously.
func Detect(content string) bool {
	lines := strings.SplitN(content, "\n", 51)
	if len(lines) > 50 {
		lines = lines[:50]
	}
	for _, l := range lines {
		if detectRe.MatchString(l) {
			return true
		}
	}
	return false
}

// Parse decodes a document (YAML or JSON — JSON is valid YAML). It does not
// enforce the version field: referenced files may be bare components files.
func Parse(data []byte) (*Document, error) {
	var doc Document
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	return &doc, nil
}

// ParseSchema decodes a file whose entire content is a single schema (the
// bare-file `$ref: ./pet.yaml` form).
func ParseSchema(data []byte) (*Schema, error) {
	var s Schema
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}
