package openapi

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testLoader reads from testdata/, tracking how many times each path loads.
func testLoader(t *testing.T) (func(string) ([]byte, error), map[string]int) {
	t.Helper()
	counts := map[string]int{}
	return func(rel string) ([]byte, error) {
		counts[rel]++
		return os.ReadFile(filepath.Join("testdata", rel))
	}, counts
}

func newPetstoreResolver(t *testing.T) (*Resolver, *Document, map[string]int) {
	t.Helper()
	doc := parsePetstore(t)
	doc.File = "petstore.yaml"
	load, counts := testLoader(t)
	return NewResolver(load), doc, counts
}

func TestResolveLocalSchema(t *testing.T) {
	r, doc, _ := newPetstoreResolver(t)
	s, owner, reason := r.ResolveSchema(doc, "#/components/schemas/Pet")
	if reason != "" {
		t.Fatalf("reason = %q", reason)
	}
	if owner.File != "petstore.yaml" {
		t.Errorf("owner file = %q", owner.File)
	}
	if s.Type != "object" || s.Properties.Len() == 0 {
		t.Errorf("schema = %+v", s)
	}
}

func TestResolveCrossFileSchema(t *testing.T) {
	r, doc, counts := newPetstoreResolver(t)
	s, owner, reason := r.ResolveSchema(doc, "./common.yaml#/components/schemas/Owner")
	if reason != "" {
		t.Fatalf("reason = %q", reason)
	}
	if owner.File != "common.yaml" {
		t.Errorf("owner file = %q", owner.File)
	}
	email, ok := s.Properties.Get("email")
	if !ok || email.Format != "email" {
		t.Errorf("email = %+v", email)
	}
	// Second resolve hits the cache — no re-read.
	if _, _, reason := r.ResolveSchema(doc, "./common.yaml#/components/schemas/Error"); reason != "" {
		t.Fatalf("second resolve: %q", reason)
	}
	if counts["common.yaml"] != 1 {
		t.Errorf("common.yaml loaded %d times, want 1", counts["common.yaml"])
	}
}

func TestResolveCrossFileResponse(t *testing.T) {
	r, doc, _ := newPetstoreResolver(t)
	resp, owner, reason := r.ResolveResponse(doc, "#/components/responses/NotFound")
	if reason != "" {
		t.Fatalf("reason = %q", reason)
	}
	if owner.File != "petstore.yaml" || resp.Description != "Pet not found" {
		t.Errorf("resp = %+v owner = %q", resp, owner.File)
	}
	// The response's schema ref resolves relative to petstore.yaml.
	mt, _ := resp.Content.Get("application/json")
	s, sOwner, reason := r.ResolveSchema(owner, mt.Schema.Ref)
	if reason != "" || sOwner.File != "common.yaml" || s.Type != "object" {
		t.Errorf("error schema: %+v owner=%v reason=%q", s, sOwner, reason)
	}
}

func TestResolveRelativeToOwningFile(t *testing.T) {
	// A ref found inside cycle-a.yaml must resolve relative to cycle-a.yaml,
	// not the root doc, including when the root lives in a subdirectory.
	load, _ := testLoader(t)
	r := NewResolver(load)
	root := &Document{File: "cycle-a.yaml"}
	a, ownerA, reason := r.ResolveSchema(root, "./cycle-a.yaml#/components/schemas/A")
	if reason != "" {
		t.Fatalf("resolve A: %q", reason)
	}
	b, _ := a.Properties.Get("b")
	sB, ownerB, reason := r.ResolveSchema(ownerA, b.Ref)
	if reason != "" || ownerB.File != "cycle-b.yaml" {
		t.Fatalf("resolve B: %q owner=%v", reason, ownerB)
	}
	backA, _ := sB.Properties.Get("a")
	if got := r.RefKey(ownerB, backA.Ref); got != "cycle-a.yaml#/components/schemas/A" {
		t.Errorf("cycle key = %q", got)
	}
	if got := r.RefKey(ownerA, b.Ref); got != "cycle-b.yaml#/components/schemas/B" {
		t.Errorf("forward key = %q", got)
	}
}

func TestResolveFailures(t *testing.T) {
	r, doc, counts := newPetstoreResolver(t)
	cases := []struct {
		name string
		ref  string
		want string
	}{
		{"http", "https://example.com/spec.yaml#/components/schemas/X", "external ref"},
		{"missing file", "./nope.yaml#/components/schemas/X", "cannot read"},
		{"escapes root", "../secret.yaml#/components/schemas/X", "outside workspace root"},
		{"unknown name", "#/components/schemas/Nope", "not found"},
		{"bad pointer", "#/definitions/Old", "unsupported pointer"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, _, reason := r.ResolveSchema(doc, c.ref)
			if s != nil || reason == "" || !contains(reason, c.want) {
				t.Errorf("ResolveSchema(%q) = %v, %q; want reason containing %q", c.ref, s, reason, c.want)
			}
		})
	}
	// Missing-file failures are cached: resolve again, still one read attempt.
	r.ResolveSchema(doc, "./nope.yaml#/components/schemas/X")
	if counts["nope.yaml"] != 1 {
		t.Errorf("nope.yaml load attempts = %d, want 1", counts["nope.yaml"])
	}
}

func TestResolveBareFileSchema(t *testing.T) {
	load := func(rel string) ([]byte, error) {
		if rel != "pet.yaml" {
			return nil, fmt.Errorf("unexpected path %s", rel)
		}
		return []byte("type: object\nproperties:\n  name:\n    type: string\n"), nil
	}
	r := NewResolver(load)
	doc := &Document{File: "spec.yaml"}
	s, owner, reason := r.ResolveSchema(doc, "./pet.yaml")
	if reason != "" || owner.File != "pet.yaml" || s.Type != "object" {
		t.Errorf("bare ref: %+v owner=%v reason=%q", s, owner, reason)
	}
	if RefName("./pet.yaml") != "pet" {
		t.Errorf("RefName = %q", RefName("./pet.yaml"))
	}
}

func TestRefName(t *testing.T) {
	if got := RefName("#/components/schemas/Pet"); got != "Pet" {
		t.Errorf("RefName = %q", got)
	}
	if got := RefName("./common.yaml#/components/schemas/Owner"); got != "Owner" {
		t.Errorf("RefName = %q", got)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
