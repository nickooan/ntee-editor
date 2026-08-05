package openapi

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func renderYAML(t *testing.T, content string, width int) ([]Line, []OutlineEntry) {
	t.Helper()
	doc, err := Parse([]byte(content))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	doc.File = "spec.yaml"
	r := NewResolver(func(rel string) ([]byte, error) {
		return os.ReadFile(filepath.Join("testdata", rel))
	})
	lines, outline := RenderDocument(doc, r, width)
	return lines, outline
}

func renderPetstore(t *testing.T) ([]Line, []OutlineEntry, []string) {
	t.Helper()
	doc := parsePetstore(t)
	doc.File = "petstore.yaml"
	load, _ := testLoader(t)
	lines, outline := RenderDocument(doc, NewResolver(load), 72)
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

func TestRenderPetstoreLayout(t *testing.T) {
	_, _, plain := renderPetstore(t)

	if plain[0] != "  Petstore API  v1.0.0" {
		t.Errorf("title = %q", plain[0])
	}
	if plain[1] != "  A sample API for managing pets." {
		t.Errorf("description = %q", plain[1])
	}
	findLine(t, plain, "── pets ──")

	// Operation headings: method, path, right-aligned operationId at width 72.
	get := plain[findLine(t, plain, "GET  /pets")]
	if !strings.HasSuffix(get, "listPets") || len([]rune(get)) != 72 {
		t.Errorf("GET heading = %q (len %d)", get, len([]rune(get)))
	}
	findLine(t, plain, "POST  /pets")
	findLine(t, plain, "PATCH  /pets/{petId}")

	// Aligned parameter rows, header params included.
	findLine(t, plain, "limit        query    integer($int32)   max records to return")
	findLine(t, plain, "X-Trace-Id   header   string($uuid)     correlation id")
	findLine(t, plain, "X-Idempotency-Key*   header   string   dedupe key")
	findLine(t, plain, "petId*   path   integer($int64)   pet to update")

	// Request body: content types, required flag, allOf merge + annotation.
	findLine(t, plain, "Request body  (application/json)  required")
	findLine(t, plain, "(application/xml)")
	findLine(t, plain, "NewPet {  (allOf: PetBase + 1 inline)")
	findLine(t, plain, "status: string  default: available")

	// PATCH body renders inline object properties directly, with oneOf.
	findLine(t, plain, "Request body  (application/merge-patch+json)")
	i := findLine(t, plain, "status: oneOf")
	if !strings.Contains(plain[i+1], "‣ string  enum: [available, pending, sold]") {
		t.Errorf("oneOf variant = %q", plain[i+1])
	}
	if !strings.Contains(plain[i+2], "‣ null") {
		t.Errorf("oneOf variant 2 = %q", plain[i+2])
	}

	// Response blocks: status + content type + description, schema trees.
	findLine(t, plain, "200  application/json    A paged list of pets")
	findLine(t, plain, "array of Pet {")
	findLine(t, plain, "id*: integer($int64)  readOnly")
	findLine(t, plain, "status: string  enum: [available, pending, sold]  default: available")
	findLine(t, plain, "vaccinated: boolean  nullable")
	findLine(t, plain, "tags: array of string")
	findLine(t, plain, "<additional>: string")

	// Local recursion stops with a marker.
	findLine(t, plain, "parent: ↩ Category (recursive)")

	// Response $ref resolved through components into the cross-file schema.
	findLine(t, plain, "404  application/json    Pet not found")
	findLine(t, plain, "code*: integer($int32)")
}

func TestRenderPetstoreSources(t *testing.T) {
	lines, _, plain := renderPetstore(t)

	cases := []struct {
		sub  string
		file string
		line int
	}{
		{"GET  /pets", "petstore.yaml", 11}, // the "get:" key line
		{"limit        query", "petstore.yaml", 16},
		{"owner: Owner {", "petstore.yaml", 123},
		{"name*: string", "petstore.yaml", 108},     // Pet.name property key
		{"email: string($email)", "common.yaml", 9}, // inside cross-file Owner
		{"code*: integer($int32)", "common.yaml", 16},
		{"PATCH  /pets/{petId}", "petstore.yaml", 67}, // the "patch:" key line
	}
	for _, c := range cases {
		i := findLine(t, plain, c.sub)
		if lines[i].Src.File != c.file || lines[i].Src.Line != c.line {
			t.Errorf("%q src = %s:%d, want %s:%d", c.sub, lines[i].Src.File, lines[i].Src.Line, c.file, c.line)
		}
	}

	// Blank rows inherit the previous row's source, so every row has one.
	for i, l := range lines {
		if l.Src.File == "" || l.Src.Line == 0 {
			t.Errorf("row %d (%q) has empty source", i, plain[i])
		}
	}
}

func TestRenderPetstoreOutline(t *testing.T) {
	lines, outline, plain := renderPetstore(t)
	want := []struct {
		label  string
		method string
		depth  int
	}{
		{"pets", "", 0},
		{"GET /pets", "GET", 1},
		{"POST /pets", "POST", 1},
		{"PATCH /pets/{petId}", "PATCH", 1},
	}
	if len(outline) != len(want) {
		t.Fatalf("outline = %+v", outline)
	}
	for i, w := range want {
		o := outline[i]
		if o.Label != w.label || o.Method != w.method || o.Depth != w.depth {
			t.Errorf("outline[%d] = %+v, want %+v", i, o, w)
		}
		if o.LineIdx < 0 || o.LineIdx >= len(lines) {
			t.Errorf("outline[%d] anchor %d out of range", i, o.LineIdx)
		}
	}
	// Anchors point at the heading rows themselves.
	if !strings.Contains(plain[outline[1].LineIdx], "GET  /pets") {
		t.Errorf("GET anchor row = %q", plain[outline[1].LineIdx])
	}
}

func TestRenderCrossFileCycle(t *testing.T) {
	spec := `openapi: 3.0.0
info: {title: C, version: "1"}
paths:
  /a:
    get:
      operationId: getA
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                $ref: "./cycle-a.yaml#/components/schemas/A"
`
	lines, _ := renderYAML(t, spec, 60)
	plain := Plain(lines)
	findLine(t, plain, "A {")
	findLine(t, plain, "b: B {")
	i := findLine(t, plain, "a: ↩ A (recursive)")
	if lines[i].Src.File != "cycle-b.yaml" {
		t.Errorf("cycle marker src = %+v", lines[i].Src)
	}
}

func TestRenderUnresolvedRefs(t *testing.T) {
	spec := `openapi: 3.0.0
info: {title: U, version: "1"}
paths:
  /u:
    get:
      operationId: getU
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                $ref: "./nope.yaml#/components/schemas/X"
        "500":
          description: err
          content:
            application/json:
              schema:
                $ref: "https://example.com/x.yaml#/components/schemas/X"
`
	lines, _ := renderYAML(t, spec, 60)
	plain := Plain(lines)
	findLine(t, plain, "unresolved: ./nope.yaml#/components/schemas/X (cannot read nope.yaml)")
	findLine(t, plain, "unresolved: https://example.com/x.yaml#/components/schemas/X (external ref")
}

func TestRenderDepthCap(t *testing.T) {
	var b strings.Builder
	b.WriteString("openapi: 3.0.0\ninfo: {title: D, version: \"1\"}\npaths:\n  /d:\n    get:\n      operationId: getD\n      responses:\n        \"200\":\n          description: ok\n          content:\n            application/json:\n              schema:\n")
	indent := "                "
	for i := 0; i < 12; i++ {
		b.WriteString(indent + "type: object\n")
		b.WriteString(indent + "properties:\n")
		b.WriteString(indent + "  nest:\n")
		indent += "    "
	}
	b.WriteString(indent + "type: string\n")
	lines, _ := renderYAML(t, b.String(), 60)
	findLine(t, Plain(lines), "… (max depth)")
}

func TestRenderLineBudget(t *testing.T) {
	var b strings.Builder
	b.WriteString("openapi: 3.0.0\ninfo: {title: B, version: \"1\"}\npaths:\n  /b:\n    get:\n      operationId: getB\n      responses:\n        \"200\":\n          description: ok\n          content:\n            application/json:\n              schema:\n                type: object\n                properties:\n")
	for i := 0; i < 250; i++ {
		fmt.Fprintf(&b, "                  p%03d:\n                    type: string\n", i)
	}
	lines, _ := renderYAML(t, b.String(), 60)
	plain := Plain(lines)
	count := 0
	for _, l := range plain {
		if strings.Contains(l, "… truncated") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("truncation markers = %d, want 1", count)
	}
	if len(plain) > 230 {
		t.Errorf("rendered %d rows despite budget", len(plain))
	}
}

func TestRenderEnumTruncation(t *testing.T) {
	spec := `openapi: 3.0.0
info: {title: E, version: "1"}
paths:
  /e:
    get:
      operationId: getE
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
                properties:
                  kind:
                    type: string
                    enum: [a, b, c, d, e, f, g, h, i]
`
	lines, _ := renderYAML(t, spec, 60)
	findLine(t, Plain(lines), "enum: [a, b, c, d, e, f, …+3]")
}

func TestRenderUntaggedNoHeaders(t *testing.T) {
	spec := `openapi: 3.0.0
info: {title: Plain, version: "1"}
paths:
  /x:
    get:
      operationId: getX
      responses:
        "200":
          description: ok
`
	lines, outline := renderYAML(t, spec, 60)
	plain := Plain(lines)
	for _, l := range plain {
		if strings.Contains(l, "──") {
			t.Errorf("unexpected tag header: %q", l)
		}
	}
	if len(outline) != 1 || outline[0].Label != "GET /x" {
		t.Errorf("outline = %+v", outline)
	}
	findLine(t, plain, "200  ok")
}
