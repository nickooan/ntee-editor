package openapi

import (
	"path"
	"strings"
)

// Resolver resolves $refs against the root document and, for file refs, other
// files loaded through an injected reader. The reader receives workspace-root
// relative paths — jailing to the root is the caller's responsibility, but
// paths that escape the root are rejected here as well.
type Resolver struct {
	load    func(rel string) ([]byte, error)
	docs    map[string]*Document // cleaned rel path → parsed document
	failed  map[string]string    // cleaned rel path → load/parse failure reason
	schemas map[string]*Schema   // cleaned rel path → bare-file schema
}

// NewResolver creates a resolver. load reads a workspace-root-relative file.
func NewResolver(load func(rel string) ([]byte, error)) *Resolver {
	return &Resolver{
		load:    load,
		docs:    map[string]*Document{},
		failed:  map[string]string{},
		schemas: map[string]*Schema{},
	}
}

// splitRef splits "file#/pointer" into its halves; either may be empty.
func splitRef(ref string) (file, pointer string) {
	if i := strings.IndexByte(ref, '#'); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	return ref, ""
}

// RefName returns the display name of a ref: the last pointer segment, or the
// file's base name (without extension) for bare-file refs.
func RefName(ref string) string {
	file, pointer := splitRef(ref)
	if pointer != "" {
		parts := strings.Split(pointer, "/")
		return unescapePointer(parts[len(parts)-1])
	}
	base := path.Base(file)
	return strings.TrimSuffix(base, path.Ext(base))
}

// unescapePointer undoes JSON-pointer escaping (~1 → /, ~0 → ~).
func unescapePointer(s string) string {
	s = strings.ReplaceAll(s, "~1", "/")
	return strings.ReplaceAll(s, "~0", "~")
}

func isExternalURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// RefKey normalizes a ref appearing in doc `from` to "cleanedPath#pointer" —
// the identity used for cycle guards, stable across files.
func (r *Resolver) RefKey(from *Document, ref string) string {
	file, pointer := splitRef(ref)
	return r.normPath(from, file) + "#" + pointer
}

// normPath resolves a (possibly empty) file ref relative to the directory of
// the document it appears in, returning a workspace-root-relative clean path.
func (r *Resolver) normPath(from *Document, file string) string {
	if file == "" {
		return from.File
	}
	if isExternalURL(file) {
		return file
	}
	return path.Clean(path.Join(path.Dir(from.File), file))
}

// document loads and caches the doc a ref points into. Returns the owning
// document (from itself for local refs) or a failure reason.
func (r *Resolver) document(from *Document, file string) (*Document, string) {
	if file == "" {
		return from, ""
	}
	if isExternalURL(file) {
		return nil, "external ref: " + file
	}
	rel := r.normPath(from, file)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return nil, "outside workspace root: " + rel
	}
	if doc, ok := r.docs[rel]; ok {
		return doc, ""
	}
	if reason, ok := r.failed[rel]; ok {
		return nil, reason
	}
	data, err := r.load(rel)
	if err != nil {
		reason := "cannot read " + rel
		r.failed[rel] = reason
		return nil, reason
	}
	doc, err := Parse(data)
	if err != nil {
		reason := "cannot parse " + rel
		r.failed[rel] = reason
		return nil, reason
	}
	doc.File = rel
	r.docs[rel] = doc
	return doc, ""
}

// componentRef splits a "/components/<kind>/<name>" pointer.
func componentRef(pointer string) (kind, name string, ok bool) {
	parts := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	if len(parts) != 3 || parts[0] != "components" {
		return "", "", false
	}
	return parts[1], unescapePointer(parts[2]), true
}

// ResolveSchema resolves a schema ref one hop. On success the returned
// document owns the schema (its File locates the source). reason is non-empty
// on failure.
func (r *Resolver) ResolveSchema(from *Document, ref string) (*Schema, *Document, string) {
	file, pointer := splitRef(ref)
	if pointer == "" && file != "" {
		// Bare-file ref: the whole file is one schema.
		if isExternalURL(file) {
			return nil, nil, "external ref: " + file
		}
		rel := r.normPath(from, file)
		if rel == ".." || strings.HasPrefix(rel, "../") {
			return nil, nil, "outside workspace root: " + rel
		}
		if s, ok := r.schemas[rel]; ok {
			return s, &Document{File: rel}, ""
		}
		if reason, ok := r.failed[rel]; ok {
			return nil, nil, reason
		}
		data, err := r.load(rel)
		if err != nil {
			reason := "cannot read " + rel
			r.failed[rel] = reason
			return nil, nil, reason
		}
		s, err := ParseSchema(data)
		if err != nil {
			reason := "cannot parse " + rel
			r.failed[rel] = reason
			return nil, nil, reason
		}
		r.schemas[rel] = s
		return s, &Document{File: rel}, ""
	}
	doc, reason := r.document(from, file)
	if reason != "" {
		return nil, nil, reason
	}
	kind, name, ok := componentRef(pointer)
	if !ok || kind != "schemas" {
		return nil, nil, "unsupported pointer: " + pointer
	}
	s, ok := doc.Components.Schemas.Get(name)
	if !ok {
		return nil, nil, "not found: " + ref
	}
	return s, doc, ""
}

// ResolveParameter resolves a parameter component ref one hop.
func (r *Resolver) ResolveParameter(from *Document, ref string) (*Parameter, *Document, string) {
	doc, kind, name, reason := r.component(from, ref)
	if reason != "" {
		return nil, nil, reason
	}
	if kind != "parameters" {
		return nil, nil, "unsupported pointer in " + ref
	}
	p, ok := doc.Components.Parameters.Get(name)
	if !ok {
		return nil, nil, "not found: " + ref
	}
	return p, doc, ""
}

// ResolveResponse resolves a response component ref one hop.
func (r *Resolver) ResolveResponse(from *Document, ref string) (*Response, *Document, string) {
	doc, kind, name, reason := r.component(from, ref)
	if reason != "" {
		return nil, nil, reason
	}
	if kind != "responses" {
		return nil, nil, "unsupported pointer in " + ref
	}
	resp, ok := doc.Components.Responses.Get(name)
	if !ok {
		return nil, nil, "not found: " + ref
	}
	return resp, doc, ""
}

// ResolveRequestBody resolves a requestBody component ref one hop.
func (r *Resolver) ResolveRequestBody(from *Document, ref string) (*RequestBody, *Document, string) {
	doc, kind, name, reason := r.component(from, ref)
	if reason != "" {
		return nil, nil, reason
	}
	if kind != "requestBodies" {
		return nil, nil, "unsupported pointer in " + ref
	}
	b, ok := doc.Components.RequestBodies.Get(name)
	if !ok {
		return nil, nil, "not found: " + ref
	}
	return b, doc, ""
}

func (r *Resolver) component(from *Document, ref string) (doc *Document, kind, name, reason string) {
	file, pointer := splitRef(ref)
	doc, reason = r.document(from, file)
	if reason != "" {
		return nil, "", "", reason
	}
	kind, name, ok := componentRef(pointer)
	if !ok {
		return nil, "", "", "unsupported pointer: " + pointer
	}
	return doc, kind, name, ""
}
