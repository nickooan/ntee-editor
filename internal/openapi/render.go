package openapi

import (
	"fmt"
	"strings"

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

const (
	maxSchemaDepth = 8
	maxSchemaLines = 200
)

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

// MethodColor is the ANSI color name for an HTTP method badge (the Swagger
// UI convention), for callers styling outline rows outside this package.
func MethodColor(method string) string { return methodColor(method) }

func methodColor(method string) string {
	switch method {
	case "GET":
		return "green"
	case "POST":
		return "yellow"
	case "PUT":
		return "blue"
	case "PATCH":
		return "magenta"
	case "DELETE":
		return "red"
	}
	return "cyan"
}

func statusColor(code string) string {
	switch {
	case strings.HasPrefix(code, "2"):
		return "green"
	case strings.HasPrefix(code, "3"):
		return "cyan"
	case strings.HasPrefix(code, "4"):
		return "yellow"
	case strings.HasPrefix(code, "5"):
		return "red"
	}
	return ""
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
	r       *Resolver
	width   int
	lines   []Line
	outline []OutlineEntry
	lastSrc SourceRef

	budget    int  // remaining schema-tree rows for the current tree
	truncated bool // truncation marker already emitted for the current tree
}

// RenderDocument renders the whole spec as one continuous document. width
// only drives right-alignment and separators; long rows are truncated by the
// caller's viewport.
func RenderDocument(doc *Document, r *Resolver, width int) ([]Line, []OutlineEntry) {
	if width < 40 {
		width = 40
	}
	rd := &renderer{r: r, width: width}
	rd.title(doc)
	groups := groupByTag(doc)
	multi := len(groups) > 1 || (len(groups) == 1 && groups[0].name != "")
	for _, g := range groups {
		if multi {
			rd.tagHeader(doc, g)
		}
		for _, po := range g.ops {
			rd.operation(doc, po)
		}
	}
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

// emitS is emit for schema-tree rows: it spends the tree's line budget and
// collapses everything past it into one truncation marker.
func (rd *renderer) emitS(src SourceRef, depth int, segs ...view.HighlightSegment) {
	if rd.budget <= 0 {
		if !rd.truncated {
			rd.truncated = true
			rd.emit(src, dim(strings.Repeat("  ", depth)+"… truncated"))
		}
		return
	}
	rd.budget--
	all := append([]view.HighlightSegment{{Text: strings.Repeat("  ", depth)}}, segs...)
	rd.emit(src, all...)
}

func (rd *renderer) title(doc *Document) {
	src := SourceRef{File: doc.File, Line: 1}
	segs := []view.HighlightSegment{{Text: "  "}}
	title := doc.Info.Title
	if title == "" {
		title = doc.File
	}
	segs = append(segs, boldSeg(title, ""))
	if doc.Info.Version != "" {
		segs = append(segs, dim("  v"+doc.Info.Version))
	}
	rd.emit(src, segs...)
	if d := firstLine(doc.Info.Description); d != "" {
		rd.emit(src, sp(2), dim(d))
	}
}

type pathOp struct {
	path   string
	item   *PathItem
	method string
	op     *Operation
}

type tagGroup struct {
	name        string
	description string
	ops         []pathOp
}

// groupByTag buckets operations by their first tag: declared tag order first,
// undeclared tags in encounter order, untagged operations last.
func groupByTag(doc *Document) []tagGroup {
	order := []string{}
	desc := map[string]string{}
	byName := map[string]*tagGroup{}
	add := func(name string) *tagGroup {
		if g, ok := byName[name]; ok {
			return g
		}
		g := &tagGroup{name: name, description: desc[name]}
		byName[name] = g
		order = append(order, name)
		return g
	}
	for _, t := range doc.Tags {
		desc[t.Name] = t.Description
	}

	var untagged []pathOp
	for _, p := range doc.Paths.Keys {
		item := doc.Paths.Vals[p]
		for _, mo := range item.Operations() {
			po := pathOp{path: p, item: item, method: mo.Method, op: mo.Op}
			if len(mo.Op.Tags) == 0 {
				untagged = append(untagged, po)
				continue
			}
			add(mo.Op.Tags[0]).ops = append(add(mo.Op.Tags[0]).ops, po)
		}
	}

	var out []tagGroup
	for _, t := range doc.Tags { // declared order first, only if used
		if g, ok := byName[t.Name]; ok && len(g.ops) > 0 {
			out = append(out, *g)
			delete(byName, t.Name)
		}
	}
	for _, name := range order { // then encounter order
		if g, ok := byName[name]; ok && len(g.ops) > 0 {
			out = append(out, *g)
			delete(byName, name)
		}
	}
	if len(untagged) > 0 {
		out = append(out, tagGroup{ops: untagged})
	}
	return out
}

func (rd *renderer) tagHeader(doc *Document, g tagGroup) {
	rd.blank()
	name := g.name
	if name == "" {
		name = "other"
	}
	src := SourceRef{}
	if len(g.ops) > 0 {
		src = SourceRef{File: doc.File, Line: doc.Paths.Line(g.ops[0].path)}
	}
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
	if g.description != "" {
		rd.emit(src, sp(2), dim(firstLine(g.description)))
	}
}

func (rd *renderer) operation(doc *Document, po pathOp) {
	op := po.op
	src := SourceRef{File: doc.File, Line: op.Line}
	if l := po.item.MethodLines[po.method]; l > 0 {
		src.Line = l // anchor the heading on the "get:"/"post:" key itself
	}
	rd.blank()
	rd.outline = append(rd.outline, OutlineEntry{
		Label: po.method + " " + po.path, Method: po.method, Path: po.path,
		LineIdx: len(rd.lines), Depth: 1,
	})

	head := []view.HighlightSegment{
		{Text: "  "},
		boldSeg(po.method, methodColor(po.method)),
		{Text: "  "},
	}
	if op.Deprecated {
		head = append(head, view.HighlightSegment{Text: po.path, Bold: true, Strike: true})
		head = append(head, dim("  deprecated"))
	} else {
		head = append(head, view.HighlightSegment{Text: po.path, Bold: true})
	}
	if op.OperationID != "" {
		gap := rd.width - plainLen(head) - len([]rune(op.OperationID))
		if gap < 2 {
			gap = 2
		}
		head = append(head, dim(strings.Repeat(" ", gap)+op.OperationID))
	}
	rd.emit(src, head...)

	if op.Summary != "" {
		rd.emit(src, sp(2), view.HighlightSegment{Text: firstLine(op.Summary)})
	}
	if d := firstLine(op.Description); d != "" && d != firstLine(op.Summary) {
		rd.emit(src, sp(2), dim(d))
	}

	params := append(append([]*Parameter{}, po.item.Parameters...), op.Parameters...)
	if len(params) > 0 {
		rd.parameters(doc, params)
	}
	if op.RequestBody != nil {
		rd.requestBody(doc, op.RequestBody)
	}
	if op.Responses.Len() > 0 {
		rd.responses(doc, op)
	}
}

type paramRow struct {
	name     string
	required bool
	in       string
	typ      string
	desc     string
	src      SourceRef
}

func (rd *renderer) parameters(doc *Document, params []*Parameter) {
	rd.blank()
	rd.emit(SourceRef{}, sp(4), boldSeg("Parameters", ""))

	var rows []paramRow
	for _, p := range params {
		owner := doc
		if p.Ref != "" {
			resolved, rdoc, reason := rd.r.ResolveParameter(doc, p.Ref)
			if reason != "" {
				rd.emit(SourceRef{File: doc.File, Line: p.Line},
					sp(6), dim("unresolved: "+p.Ref+" ("+reason+")"))
				continue
			}
			p, owner = resolved, rdoc
		}
		rows = append(rows, paramRow{
			name:     p.Name,
			required: p.Required,
			in:       p.In,
			typ:      labelFor(p.Schema),
			desc:     firstLine(p.Description),
			src:      SourceRef{File: owner.File, Line: p.Line},
		})
	}

	nameW, inW, typW := 0, 0, 0
	for _, r := range rows {
		nameW = max(nameW, len([]rune(r.name))+starLen(r.required))
		inW = max(inW, len([]rune(r.in)))
		typW = max(typW, min(24, len([]rune(r.typ))))
	}
	for _, r := range rows {
		segs := []view.HighlightSegment{{Text: "      "}, seg(r.name, "yellow")}
		pad := nameW - len([]rune(r.name))
		if r.required {
			segs = append(segs, boldSeg("*", "red"))
			pad--
		}
		segs = append(segs, view.HighlightSegment{Text: strings.Repeat(" ", pad+3)})
		segs = append(segs, dim(padTo(r.in, inW)))
		segs = append(segs, view.HighlightSegment{Text: "   "})
		segs = append(segs, seg(padTo(truncRunes(r.typ, 24), typW), "cyan"))
		if r.desc != "" {
			segs = append(segs, view.HighlightSegment{Text: "   "}, dim(r.desc))
		}
		rd.emit(r.src, segs...)
	}
}

func (rd *renderer) requestBody(doc *Document, body *RequestBody) {
	owner := doc
	if body.Ref != "" {
		resolved, rdoc, reason := rd.r.ResolveRequestBody(doc, body.Ref)
		if reason != "" {
			rd.blank()
			rd.emit(SourceRef{File: doc.File, Line: body.Line},
				sp(4), boldSeg("Request body", ""), sp(2),
				dim("unresolved: "+body.Ref+" ("+reason+")"))
			return
		}
		body, owner = resolved, rdoc
	}
	rd.blank()
	src := SourceRef{File: owner.File, Line: body.Line}
	for i, ct := range body.Content.Keys {
		mt := body.Content.Vals[ct]
		mtSrc := SourceRef{File: owner.File, Line: body.Content.Line(ct)}
		if i == 0 {
			segs := []view.HighlightSegment{{Text: "    "}, boldSeg("Request body", ""), dim("  (" + ct + ")")}
			if body.Required {
				segs = append(segs, dim("  required"))
			}
			rd.emit(src, segs...)
		} else {
			rd.emit(mtSrc, sp(4), dim("("+ct+")"))
		}
		if mt.Schema != nil {
			rd.schemaRoot(owner, mt.Schema, 3, mtSrc)
		}
	}
	if body.Content.Len() == 0 {
		segs := []view.HighlightSegment{{Text: "    "}, boldSeg("Request body", "")}
		if body.Required {
			segs = append(segs, dim("  required"))
		}
		rd.emit(src, segs...)
	}
}

func (rd *renderer) responses(doc *Document, op *Operation) {
	rd.blank()
	rd.emit(SourceRef{}, sp(4), boldSeg("Responses", ""))
	for _, code := range op.Responses.Keys {
		resp := op.Responses.Vals[code]
		src := SourceRef{File: doc.File, Line: op.Responses.Line(code)}
		owner := doc
		if resp.Ref != "" {
			resolved, rdoc, reason := rd.r.ResolveResponse(doc, resp.Ref)
			if reason != "" {
				rd.emit(src, sp(6), boldSeg(code, statusColor(code)),
					sp(2), dim("unresolved: "+resp.Ref+" ("+reason+")"))
				continue
			}
			resp, owner = resolved, rdoc
		}
		for i, ct := range resp.Content.Keys {
			mt := resp.Content.Vals[ct]
			mtSrc := SourceRef{File: owner.File, Line: resp.Content.Line(ct)}
			if i == 0 {
				segs := []view.HighlightSegment{{Text: "      "}, boldSeg(code, statusColor(code)),
					{Text: "  "}, dim(ct)}
				if d := firstLine(resp.Description); d != "" {
					segs = append(segs, view.HighlightSegment{Text: "    "}, dim(d))
				}
				rd.emit(src, segs...)
			} else {
				rd.emit(mtSrc, sp(6), dim("("+ct+")"))
			}
			if mt.Schema != nil {
				rd.schemaRoot(owner, mt.Schema, 4, mtSrc)
			}
		}
		if resp.Content.Len() == 0 {
			segs := []view.HighlightSegment{{Text: "      "}, boldSeg(code, statusColor(code))}
			if d := firstLine(resp.Description); d != "" {
				segs = append(segs, sp(2), dim(d))
			}
			rd.emit(src, segs...)
		}
	}
}

// schemaRoot starts a fresh schema tree (own budget, own cycle set) at the
// given indent depth. Inline objects render their properties directly; refs
// and other shapes get a head line.
func (rd *renderer) schemaRoot(doc *Document, s *Schema, depth int, src SourceRef) {
	rd.budget = maxSchemaLines
	rd.truncated = false
	seen := map[string]bool{}
	if s.Ref == "" && isPlainObject(s) {
		rd.objectChildren(doc, s, depth, seen)
		return
	}
	rd.schemaTree(doc, s, nil, depth, seen, src)
}

// isPlainObject reports an inline object schema with no combinators.
func isPlainObject(s *Schema) bool {
	return s != nil && s.Ref == "" && len(s.AllOf) == 0 && len(s.OneOf) == 0 && len(s.AnyOf) == 0 &&
		(s.Type == "object" || s.Type == "") && (s.Properties.Len() > 0 || s.AdditionalProperties != nil)
}

// schemaTree renders one schema node. prefix is the already-styled name part
// ("name*: ", "‣ ", or nil for a bare head); src overrides the head line's
// source (zero → the schema's own position).
func (rd *renderer) schemaTree(doc *Document, s *Schema, prefix []view.HighlightSegment, depth int, seen map[string]bool, src SourceRef) {
	if depth > maxSchemaDepth {
		rd.emitS(src, depth, dim("… (max depth)"))
		return
	}
	if s == nil {
		rd.emitS(src, depth, append(prefix, dim("(any)"))...)
		return
	}
	if src == (SourceRef{}) {
		src = SourceRef{File: doc.File, Line: s.Line}
	}
	if s.Ref != "" {
		key := rd.r.RefKey(doc, s.Ref)
		name := RefName(s.Ref)
		if seen[key] {
			rd.emitS(src, depth, append(prefix, seg("↩ "+name+" (recursive)", "cyan"))...)
			return
		}
		resolved, owner, reason := rd.r.ResolveSchema(doc, s.Ref)
		if reason != "" {
			rd.emitS(src, depth, append(prefix, dim("unresolved: "+s.Ref+" ("+reason+")"))...)
			return
		}
		seen[key] = true
		rd.schemaNode(owner, resolved, prefix, name, depth, seen, src)
		delete(seen, key)
		return
	}
	rd.schemaNode(doc, s, prefix, "", depth, seen, src)
}

// schemaNode renders a resolved (non-ref) schema. refName is the display name
// when the node was reached through a $ref.
func (rd *renderer) schemaNode(doc *Document, s *Schema, prefix []view.HighlightSegment, refName string, depth int, seen map[string]bool, src SourceRef) {
	switch {
	case len(s.AllOf) > 0:
		rd.allOfNode(doc, s, prefix, refName, depth, seen, src)
	case len(s.OneOf) > 0:
		rd.variantsNode(doc, s, "oneOf", s.OneOf, prefix, depth, seen, src)
	case len(s.AnyOf) > 0:
		rd.variantsNode(doc, s, "anyOf", s.AnyOf, prefix, depth, seen, src)
	case s.Type == "array":
		rd.arrayNode(doc, s, prefix, refName, depth, seen, src)
	case s.Type == "object" || s.Properties.Len() > 0 || s.AdditionalProperties != nil:
		label := refName
		if label == "" {
			label = "object"
		}
		head := append(prefix, seg(label+" {", "cyan"))
		head = appendAnnotations(head, s)
		rd.emitS(src, depth, head...)
		rd.descLine(doc, s, depth+1)
		rd.objectChildren(doc, s, depth+1, seen)
		rd.emitS(SourceRef{}, depth, dim("}"))
	default:
		label := refName
		if label == "" {
			label = scalarLabel(s)
		}
		head := append(prefix, seg(label, "cyan"))
		head = appendAnnotations(head, s)
		rd.emitS(src, depth, head...)
		rd.descLine(doc, s, depth+1)
	}
}

func (rd *renderer) descLine(doc *Document, s *Schema, depth int) {
	if d := firstLine(s.Description); d != "" {
		rd.emitS(SourceRef{}, depth, dim(truncRunes(d, max(20, rd.width-2*depth-4))))
	}
}

// objectChildren renders properties and additionalProperties at depth.
func (rd *renderer) objectChildren(doc *Document, s *Schema, depth int, seen map[string]bool) {
	required := map[string]bool{}
	for _, r := range s.Required {
		required[r] = true
	}
	for _, key := range s.Properties.Keys {
		prop := s.Properties.Vals[key]
		src := SourceRef{File: doc.File, Line: s.Properties.Line(key)}
		rd.schemaTree(doc, prop, propPrefix(key, required[key]), depth, seen, src)
	}
	if ap := s.AdditionalProperties; ap != nil && ap.Schema != nil {
		rd.schemaTree(doc, ap.Schema, propPrefix("<additional>", false), depth, seen, SourceRef{})
	}
}

func propPrefix(name string, required bool) []view.HighlightSegment {
	segs := []view.HighlightSegment{seg(name, "yellow")}
	if required {
		segs = append(segs, boldSeg("*", "red"))
	}
	segs = append(segs, view.HighlightSegment{Text: ": "})
	return segs
}

func (rd *renderer) allOfNode(doc *Document, s *Schema, prefix []view.HighlightSegment, refName string, depth int, seen map[string]bool, src SourceRef) {
	props, required, ann := rd.mergeAllOf(doc, s, seen)
	label := refName
	if label == "" {
		label = "object"
	}
	head := append(prefix, seg(label+" {", "cyan"), dim("  ("+ann+")"))
	rd.emitS(src, depth, head...)
	rd.descLine(doc, s, depth+1)
	for _, p := range props {
		rd.schemaTree(p.doc, p.s, propPrefix(p.name, required[p.name]), depth+1, seen, p.src)
	}
	rd.emitS(SourceRef{}, depth, dim("}"))
}

type mergedProp struct {
	name string
	s    *Schema
	doc  *Document
	src  SourceRef
}

// mergeAllOf shallow-merges allOf branches: properties in first-seen order
// (later branches override), union of required. The annotation names ref
// branches and counts inline ones.
func (rd *renderer) mergeAllOf(doc *Document, s *Schema, seen map[string]bool) ([]mergedProp, map[string]bool, string) {
	var props []mergedProp
	index := map[string]int{}
	required := map[string]bool{}
	var names []string
	inline := 0

	addBranch := func(bdoc *Document, b *Schema) {
		for _, r := range b.Required {
			required[r] = true
		}
		for _, key := range b.Properties.Keys {
			p := mergedProp{
				name: key,
				s:    b.Properties.Vals[key],
				doc:  bdoc,
				src:  SourceRef{File: bdoc.File, Line: b.Properties.Line(key)},
			}
			if i, ok := index[key]; ok {
				props[i] = p
			} else {
				index[key] = len(props)
				props = append(props, p)
			}
		}
	}

	for _, b := range s.AllOf {
		if b == nil {
			continue
		}
		if b.Ref != "" {
			key := rd.r.RefKey(doc, b.Ref)
			if seen[key] {
				names = append(names, "↩ "+RefName(b.Ref))
				continue
			}
			resolved, owner, reason := rd.r.ResolveSchema(doc, b.Ref)
			if reason != "" {
				names = append(names, RefName(b.Ref)+"?")
				continue
			}
			names = append(names, RefName(b.Ref))
			addBranch(owner, resolved)
			continue
		}
		inline++
		addBranch(doc, b)
	}
	// The schema's own sibling properties merge last (highest precedence).
	if s.Properties.Len() > 0 {
		addBranch(doc, s)
	}
	for _, r := range s.Required {
		required[r] = true
	}

	ann := "allOf: " + strings.Join(names, " + ")
	if inline > 0 {
		if len(names) > 0 {
			ann += fmt.Sprintf(" + %d inline", inline)
		} else {
			ann = fmt.Sprintf("allOf: %d inline", inline)
		}
	}
	return props, required, ann
}

func (rd *renderer) variantsNode(doc *Document, s *Schema, kind string, variants []*Schema, prefix []view.HighlightSegment, depth int, seen map[string]bool, src SourceRef) {
	head := append(prefix, seg(kind, "cyan"))
	head = appendAnnotations(head, s)
	rd.emitS(src, depth, head...)
	rd.descLine(doc, s, depth+1)
	for _, v := range variants {
		rd.schemaTree(doc, v, []view.HighlightSegment{dim("‣ ")}, depth+1, seen, SourceRef{})
	}
}

func (rd *renderer) arrayNode(doc *Document, s *Schema, prefix []view.HighlightSegment, refName string, depth int, seen map[string]bool, src SourceRef) {
	it := s.Items
	label := refName
	if label == "" {
		label = "array"
	}
	if it == nil {
		head := append(prefix, seg(label, "cyan"))
		rd.emitS(src, depth, appendAnnotations(head, s)...)
		return
	}
	if it.Ref != "" {
		key := rd.r.RefKey(doc, it.Ref)
		name := RefName(it.Ref)
		if seen[key] {
			rd.emitS(src, depth, append(prefix, seg("array of ", "cyan"), seg("↩ "+name+" (recursive)", "cyan"))...)
			return
		}
		resolved, owner, reason := rd.r.ResolveSchema(doc, it.Ref)
		if reason != "" {
			rd.emitS(src, depth, append(prefix, seg("array", "cyan"), dim("  unresolved: "+it.Ref+" ("+reason+")"))...)
			return
		}
		seen[key] = true
		rd.schemaNode(owner, resolved, append(prefix, seg("array of ", "cyan")), name, depth, seen, src)
		delete(seen, key)
		return
	}
	if isPlainObject(it) || len(it.AllOf) > 0 || len(it.OneOf) > 0 || len(it.AnyOf) > 0 || it.Type == "array" {
		rd.schemaNode(doc, it, append(prefix, seg("array of ", "cyan")), "", depth, seen, src)
		return
	}
	head := append(prefix, seg("array of "+scalarLabel(it), "cyan"))
	head = appendAnnotations(head, it)
	rd.emitS(src, depth, head...)
}

// scalarLabel folds format into the type: integer($int64), string($uuid).
func scalarLabel(s *Schema) string {
	if s.Type == "" {
		return "any"
	}
	if s.Format != "" {
		return s.Type + "($" + s.Format + ")"
	}
	return s.Type
}

// labelFor is the one-line type label used where trees don't expand
// (parameter rows): refs by name, arrays by item label.
func labelFor(s *Schema) string {
	switch {
	case s == nil:
		return ""
	case s.Ref != "":
		return RefName(s.Ref)
	case len(s.AllOf) > 0:
		return "allOf"
	case len(s.OneOf) > 0:
		return "oneOf"
	case len(s.AnyOf) > 0:
		return "anyOf"
	case s.Type == "array":
		if s.Items == nil {
			return "array"
		}
		return "array of " + labelFor(s.Items)
	case s.Type == "object" || s.Properties.Len() > 0:
		return "object"
	default:
		return scalarLabel(s)
	}
}

// appendAnnotations adds the dim annotation tail: enum, default, nullable,
// readOnly, writeOnly, deprecated.
func appendAnnotations(segs []view.HighlightSegment, s *Schema) []view.HighlightSegment {
	var parts []string
	if len(s.Enum) > 0 {
		vals := make([]string, 0, 6)
		for i, v := range s.Enum {
			if i == 6 {
				vals = append(vals, fmt.Sprintf("…+%d", len(s.Enum)-6))
				break
			}
			vals = append(vals, fmt.Sprint(v))
		}
		parts = append(parts, "enum: ["+strings.Join(vals, ", ")+"]")
	}
	if s.Default != nil {
		parts = append(parts, "default: "+fmt.Sprint(s.Default))
	}
	if s.Nullable {
		parts = append(parts, "nullable")
	}
	if s.ReadOnly {
		parts = append(parts, "readOnly")
	}
	if s.WriteOnly {
		parts = append(parts, "writeOnly")
	}
	if s.Deprecated {
		parts = append(parts, "deprecated")
	}
	if len(parts) == 0 {
		return segs
	}
	return append(segs, dim("  "+strings.Join(parts, "  ")))
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

func starLen(required bool) int {
	if required {
		return 1
	}
	return 0
}
