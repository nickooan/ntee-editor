package graphql

import (
	"fmt"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"

	"github.com/nickooan/ntee-editor/internal/view"
)

// SourceRef locates rendered content in its source file (workspace-root
// relative path, 1-based line).
type SourceRef struct {
	File string
	Line int
}

// Line is one rendered document row plus the source it came from. Rows with
// no source of their own (blanks, closing braces) inherit the previous row's.
type Line struct {
	Segs []view.HighlightSegment
	Src  SourceRef
}

// maxEnumValues caps an enum's rendered value rows (giant country/locale
// enums), collapsing the rest into one "…+N" row.
const maxEnumValues = 64

// Plain flattens rendered lines to their text, one string per row — the
// search corpus and the test comparison form.
func Plain(lines []Line) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		var b strings.Builder
		for _, s := range l.Segs {
			b.WriteString(s.Text)
		}
		out[i] = b.String()
	}
	return out
}

func seg(text, color string) view.HighlightSegment {
	return view.HighlightSegment{Text: text, Color: color}
}

func dim(text string) view.HighlightSegment {
	return view.HighlightSegment{Text: text, DimColor: true}
}

// sp is an n-space unstyled spacer segment.
func sp(n int) view.HighlightSegment {
	return view.HighlightSegment{Text: strings.Repeat(" ", n)}
}

func boldSeg(text, color string) view.HighlightSegment {
	return view.HighlightSegment{Text: text, Color: color, Bold: true}
}

func plainLen(segs []view.HighlightSegment) int {
	n := 0
	for _, s := range segs {
		n += len([]rune(s.Text))
	}
	return n
}

type renderer struct {
	s       *Schema
	width   int
	lines   []Line
	outline []OutlineEntry
	lastSrc SourceRef
}

// RenderSchema renders the merged schema as one continuous document: root
// operation groups first (fields rendered like OpenAPI operations), then the
// named types grouped by kind in declaration order. width only drives the
// group-header fill; long rows are truncated by the caller's viewport.
func RenderSchema(s *Schema, width int) ([]Line, []OutlineEntry) {
	if width < 40 {
		width = 40
	}
	rd := &renderer{s: s, width: width}
	rd.title()
	rd.parseErrors()

	rootNames := map[string]bool{}
	rd.rootGroup("Queries", "query", s.Roots.Query, rootNames)
	rd.rootGroup("Mutations", "mutation", s.Roots.Mutation, rootNames)
	rd.rootGroup("Subscriptions", "subscription", s.Roots.Subscription, rootNames)

	rd.typeGroup("Types", "type", func(d *Def) bool { return d.Kind == ast.Object && !rootNames[d.Name] })
	rd.typeGroup("Interfaces", "interface", func(d *Def) bool { return d.Kind == ast.Interface })
	rd.typeGroup("Inputs", "input", func(d *Def) bool { return d.Kind == ast.InputObject })
	rd.typeGroup("Enums", "enum", func(d *Def) bool { return d.Kind == ast.Enum })
	rd.typeGroup("Unions", "union", func(d *Def) bool { return d.Kind == ast.Union })
	rd.typeGroup("Scalars", "scalar", func(d *Def) bool { return d.Kind == ast.Scalar })
	rd.directiveGroup()
	return rd.lines, rd.outline
}

func (rd *renderer) emit(src SourceRef, segs ...view.HighlightSegment) {
	if src == (SourceRef{}) {
		src = rd.lastSrc
	} else {
		rd.lastSrc = src
	}
	rd.lines = append(rd.lines, Line{Segs: segs, Src: src})
}

func (rd *renderer) blank() { rd.emit(SourceRef{}) }

// posRef converts a gqlparser position into a SourceRef; parsed nodes always
// carry one, but stay safe against programmatic ASTs.
func posRef(p *ast.Position) SourceRef {
	if p == nil || p.Src == nil {
		return SourceRef{}
	}
	return SourceRef{File: p.Src.Name, Line: p.Line}
}

// defSrc anchors a merged definition: the base's position, else the first
// extension's.
func defSrc(d *Def) SourceRef {
	if d.Base != nil {
		return posRef(d.Base.Position)
	}
	if len(d.Exts) > 0 {
		return posRef(d.Exts[0].Position)
	}
	return SourceRef{}
}

// defFields collects a merged definition's fields: base first, then each
// extension's, every field keeping its own file anchor.
func defFields(d *Def) []*ast.FieldDefinition {
	var out []*ast.FieldDefinition
	if d.Base != nil {
		out = append(out, d.Base.Fields...)
	}
	for _, e := range d.Exts {
		out = append(out, e.Fields...)
	}
	return out
}

func (rd *renderer) title() {
	s := rd.s
	src := SourceRef{Line: 1}
	if len(s.Files) > 0 {
		src.File = s.Files[0]
	}
	rd.emit(src, sp(2), boldSeg("GraphQL schema", ""),
		dim(fmt.Sprintf("  %d types · %d files", len(s.Defs), len(s.Files))))
	if s.Config != "" {
		rd.emit(src, sp(2), dim("via "+s.Config))
	}
	for _, n := range s.Notes {
		rd.emit(src, sp(2), dim(n))
	}
}

// parseErrors renders each unparseable file as an inline row anchored to the
// broken spot — Esc jumps straight to it, and the good files still render.
func (rd *renderer) parseErrors() {
	for _, e := range rd.s.Errors {
		rd.emit(SourceRef{File: e.File, Line: e.Line},
			sp(2), seg("parse error: ", "red"), dim(e.Msg))
	}
}

func (rd *renderer) groupHeader(name string, src SourceRef) {
	rd.blank()
	rd.outline = append(rd.outline, OutlineEntry{Label: name, LineIdx: len(rd.lines), Depth: 0})
	fill := rd.width - 6 - len([]rune(name))
	if fill < 3 {
		fill = 3
	}
	rd.emit(src,
		sp(2),
		dim("── "),
		boldSeg(name, ""),
		dim(" "+strings.Repeat("─", fill)),
	)
}

// rootGroup renders one operation root's fields as an operation list. The
// root's type name is excluded from the Types group only when the group
// actually rendered, so a degenerate field-less root still shows as a type.
func (rd *renderer) rootGroup(title, kind, typeName string, rootNames map[string]bool) {
	if typeName == "" {
		return
	}
	def := rd.s.Lookup(typeName)
	if def == nil {
		return
	}
	fields := defFields(def)
	if len(fields) == 0 {
		return
	}
	rootNames[typeName] = true
	rd.groupHeader(title, defSrc(def))
	for _, f := range fields {
		rd.rootField(kind, f)
	}
}

// rootField renders one operation root field, operation-style: a bold head
// with args and return type, args breaking out one per row when they would
// not fit inline.
func (rd *renderer) rootField(kind string, f *ast.FieldDefinition) {
	src := posRef(f.Position)
	rd.blank()
	rd.outline = append(rd.outline, OutlineEntry{Label: f.Name, Kind: kind, LineIdx: len(rd.lines), Depth: 1})

	head := rd.rootFieldHead(f, false)
	breakout := len(f.Arguments) > 0 && (len(f.Arguments) > 2 || plainLen(head) > rd.width)
	if breakout {
		head = rd.rootFieldHead(f, true)
	}
	rd.emit(src, head...)
	if breakout {
		rd.argRows(f.Arguments, 6)
	}
	if d := firstLine(f.Description); d != "" {
		rd.emit(src, sp(4), dim(d))
	}
}

// rootFieldHead builds the head row; elideArgs replaces the inline argument
// list with "(…)" when the args break out below.
func (rd *renderer) rootFieldHead(f *ast.FieldDefinition, elideArgs bool) []view.HighlightSegment {
	dep, reason := deprecation(f.Directives)
	head := []view.HighlightSegment{{Text: "  "}}
	if dep {
		head = append(head, view.HighlightSegment{Text: f.Name, Bold: true, Strike: true})
	} else {
		head = append(head, boldSeg(f.Name, ""))
	}
	switch {
	case elideArgs:
		head = append(head, dim("(…)"))
	case len(f.Arguments) > 0:
		head = append(head, dim("("+inlineArgs(f.Arguments)+")"))
	}
	head = append(head, view.HighlightSegment{Text: ": "}, seg(f.Type.String(), "cyan"))
	if !rd.s.IsDefined(f.Type.Name()) {
		head = append(head, dim("  unresolved type"))
	}
	if dep {
		head = append(head, dim(deprecatedTail(reason)))
	}
	return head
}

func (rd *renderer) argRows(args ast.ArgumentDefinitionList, indent int) {
	nameW := 0
	for _, a := range args {
		nameW = max(nameW, len([]rune(a.Name)))
	}
	for _, a := range args {
		segs := []view.HighlightSegment{sp(indent),
			seg(padTo(a.Name, nameW), "yellow"),
			{Text: "   "},
			seg(a.Type.String(), "cyan"),
		}
		if a.DefaultValue != nil {
			segs = append(segs, dim("  = "+a.DefaultValue.String()))
		}
		if !rd.s.IsDefined(a.Type.Name()) {
			segs = append(segs, dim("  unresolved type"))
		}
		if d := firstLine(a.Description); d != "" {
			segs = append(segs, dim("   "+d))
		}
		rd.emit(posRef(a.Position), segs...)
	}
}

func (rd *renderer) typeGroup(title, kind string, match func(*Def) bool) {
	var defs []*Def
	for _, d := range rd.s.Defs {
		if match(d) {
			defs = append(defs, d)
		}
	}
	if len(defs) == 0 {
		return
	}
	rd.groupHeader(title, defSrc(defs[0]))
	for _, d := range defs {
		rd.blank()
		rd.outline = append(rd.outline, OutlineEntry{Label: d.Name, Kind: kind, LineIdx: len(rd.lines), Depth: 1})
		switch d.Kind {
		case ast.Scalar:
			rd.scalarDef(d)
		case ast.Union:
			rd.unionDef(d)
		case ast.Enum:
			rd.enumDef(d)
		default: // Object, Interface, InputObject
			rd.objectDef(kind, d)
		}
	}
}

// defHead is the shared "keyword Name … {" head row for block definitions.
// A base-less def (only `extend` blocks anywhere) renders dim with a marker.
func (rd *renderer) defHead(keyword string, d *Def, tail ...view.HighlightSegment) {
	src := defSrc(d)
	var head []view.HighlightSegment
	if d.Base == nil {
		head = []view.HighlightSegment{sp(2),
			dim("extend " + keyword + " " + d.Name), dim("  (base not defined)")}
	} else {
		head = []view.HighlightSegment{sp(2), seg(keyword+" ", "magenta"), boldSeg(d.Name, "")}
		if len(d.Base.Interfaces) > 0 {
			head = append(head, dim(" implements "+strings.Join(d.Base.Interfaces, " & ")))
		}
	}
	head = append(head, tail...)
	if n := extCount(d); n > 0 {
		head = append(head, dim(fmt.Sprintf("  (+%d extend)", n)))
	}
	for _, note := range d.Notes {
		head = append(head, dim("  "+note))
	}
	rd.emit(src, head...)
	if d.Base != nil {
		if desc := firstLine(d.Base.Description); desc != "" {
			rd.emit(src, sp(4), dim(desc))
		}
	}
}

// extCount is how many extend blocks the head annotates: all of them for a
// based def; for a base-less def the first extension is the head itself.
func extCount(d *Def) int {
	if d.Base == nil {
		return len(d.Exts) - 1
	}
	return len(d.Exts)
}

func (rd *renderer) objectDef(kind string, d *Def) {
	keyword := kind // "type", "interface", or "input"
	rd.defHead(keyword, d, view.HighlightSegment{Text: " {"})
	for _, f := range defFields(d) {
		rd.fieldRow(f)
	}
	rd.emit(SourceRef{}, sp(2), dim("}"))
}

// fieldRow is the compact block form: name(args): Type with the dim
// annotation tail (default, unresolved, deprecated, description) inline.
func (rd *renderer) fieldRow(f *ast.FieldDefinition) {
	src := posRef(f.Position)
	dep, reason := deprecation(f.Directives)
	segs := []view.HighlightSegment{sp(4)}
	if dep {
		segs = append(segs, view.HighlightSegment{Text: f.Name, Color: "yellow", Strike: true})
	} else {
		segs = append(segs, seg(f.Name, "yellow"))
	}
	if a := inlineArgs(f.Arguments); a != "" {
		segs = append(segs, dim("("+truncRunes(a, 48)+")"))
	}
	segs = append(segs, view.HighlightSegment{Text: ": "}, seg(f.Type.String(), "cyan"))
	if f.DefaultValue != nil { // input object fields
		segs = append(segs, dim("  = "+f.DefaultValue.String()))
	}
	if !rd.s.IsDefined(f.Type.Name()) {
		segs = append(segs, dim("  unresolved type"))
	}
	if dep {
		segs = append(segs, dim(deprecatedTail(reason)))
	}
	if d := firstLine(f.Description); d != "" {
		segs = append(segs, dim("   "+d))
	}
	rd.emit(src, segs...)
}

func (rd *renderer) enumDef(d *Def) {
	rd.defHead("enum", d, view.HighlightSegment{Text: " {"})
	var values []*ast.EnumValueDefinition
	if d.Base != nil {
		values = append(values, d.Base.EnumValues...)
	}
	for _, e := range d.Exts {
		values = append(values, e.EnumValues...)
	}
	for i, v := range values {
		if i == maxEnumValues {
			rd.emit(SourceRef{}, sp(4), dim(fmt.Sprintf("…+%d", len(values)-maxEnumValues)))
			break
		}
		dep, reason := deprecation(v.Directives)
		segs := []view.HighlightSegment{sp(4)}
		if dep {
			segs = append(segs, view.HighlightSegment{Text: v.Name, Strike: true})
		} else {
			segs = append(segs, view.HighlightSegment{Text: v.Name})
		}
		if dep {
			segs = append(segs, dim(deprecatedTail(reason)))
		}
		if desc := firstLine(v.Description); desc != "" {
			segs = append(segs, dim("   "+desc))
		}
		rd.emit(posRef(v.Position), segs...)
	}
	rd.emit(SourceRef{}, sp(2), dim("}"))
}

func (rd *renderer) unionDef(d *Def) {
	rd.defHead("union", d, view.HighlightSegment{Text: " ="})
	emitMember := func(def *ast.Definition) {
		for i, name := range def.Types {
			src := posRef(def.Position)
			if i < len(def.TypePositions) {
				src = posRef(def.TypePositions[i])
			}
			segs := []view.HighlightSegment{sp(4), dim("‣ "), seg(name, "cyan")}
			if !rd.s.IsDefined(name) {
				segs = append(segs, dim("  unresolved type"))
			}
			rd.emit(src, segs...)
		}
	}
	if d.Base != nil {
		emitMember(d.Base)
	}
	for _, e := range d.Exts {
		emitMember(e)
	}
}

func (rd *renderer) scalarDef(d *Def) {
	rd.defHead("scalar", d)
}

func (rd *renderer) directiveGroup() {
	dds := rd.s.Directives
	if len(dds) == 0 {
		return
	}
	rd.groupHeader("Directives", posRef(dds[0].Position))
	for _, dd := range dds {
		rd.outline = append(rd.outline, OutlineEntry{Label: "@" + dd.Name, Kind: "directive", LineIdx: len(rd.lines), Depth: 1})
		segs := []view.HighlightSegment{sp(2), seg("directive ", "magenta"), boldSeg("@"+dd.Name, "")}
		if a := inlineArgs(dd.Arguments); a != "" {
			segs = append(segs, dim("("+truncRunes(a, 48)+")"))
		}
		locs := make([]string, len(dd.Locations))
		for i, l := range dd.Locations {
			locs[i] = string(l)
		}
		segs = append(segs, dim(" on "+strings.Join(locs, " | ")))
		rd.emit(posRef(dd.Position), segs...)
		if desc := firstLine(dd.Description); desc != "" {
			rd.emit(SourceRef{}, sp(4), dim(desc))
		}
	}
}

// inlineArgs is the one-line "name: Type = default, …" argument list.
func inlineArgs(args ast.ArgumentDefinitionList) string {
	if len(args) == 0 {
		return ""
	}
	parts := make([]string, 0, len(args))
	for _, a := range args {
		p := a.Name + ": " + a.Type.String()
		if a.DefaultValue != nil {
			p += " = " + a.DefaultValue.String()
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, ", ")
}

// deprecation reads the @deprecated directive and its reason argument.
func deprecation(dl ast.DirectiveList) (bool, string) {
	d := dl.ForName("deprecated")
	if d == nil {
		return false, ""
	}
	if a := d.Arguments.ForName("reason"); a != nil && a.Value != nil {
		return true, a.Value.Raw
	}
	return true, ""
}

func deprecatedTail(reason string) string {
	if reason == "" {
		return "  deprecated"
	}
	return "  deprecated: " + reason
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return s
}

func padTo(s string, w int) string {
	if n := len([]rune(s)); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

func truncRunes(s string, w int) string {
	runes := []rune(s)
	if len(runes) <= w {
		return s
	}
	if w <= 1 {
		return "…"
	}
	return string(runes[:w-1]) + "…"
}
