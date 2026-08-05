package openapi

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func parsePetstore(t *testing.T) *Document {
	t.Helper()
	doc, err := Parse(loadFixture(t, "petstore.yaml"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return doc
}

func TestDetect(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"yaml", "openapi: 3.0.3\ninfo:\n", true},
		{"yaml quoted", `openapi: "3.1.0"`, true},
		{"json", `{"openapi": "3.0.0", "info": {}}`, false},
		{"json own line", "{\n  \"openapi\": \"3.0.0\",\n", true},
		{"swagger v2", "swagger: \"2.0\"\n", false},
		{"plain yaml", "foo: bar\n", false},
		{"beyond 50 lines", string(make([]byte, 0)) + repeatLines(60) + "openapi: 3.0.0\n", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Detect(c.content); got != c.want {
				t.Errorf("Detect(%q) = %v, want %v", c.name, got, c.want)
			}
		})
	}
}

func repeatLines(n int) string {
	s := ""
	for i := 0; i < n; i++ {
		s += "x: y\n"
	}
	return s
}

func TestParseBasics(t *testing.T) {
	doc := parsePetstore(t)
	if doc.OpenAPI != "3.0.3" {
		t.Errorf("OpenAPI = %q", doc.OpenAPI)
	}
	if doc.Info.Title != "Petstore API" || doc.Info.Version != "1.0.0" {
		t.Errorf("Info = %+v", doc.Info)
	}
	if len(doc.Tags) != 1 || doc.Tags[0].Name != "pets" {
		t.Errorf("Tags = %+v", doc.Tags)
	}
}

func TestParsePathOrderAndLines(t *testing.T) {
	doc := parsePetstore(t)
	wantPaths := []string{"/pets", "/pets/{petId}"}
	if !reflect.DeepEqual(doc.Paths.Keys, wantPaths) {
		t.Fatalf("Paths.Keys = %v, want %v", doc.Paths.Keys, wantPaths)
	}
	if got := doc.Paths.Line("/pets"); got != 10 {
		t.Errorf("line of /pets = %d, want 10", got)
	}

	pets, _ := doc.Paths.Get("/pets")
	ops := pets.Operations()
	if len(ops) != 2 || ops[0].Method != "GET" || ops[1].Method != "POST" {
		t.Fatalf("ops = %+v", ops)
	}
	get := ops[0].Op
	if get.OperationID != "listPets" {
		t.Errorf("operationId = %q", get.OperationID)
	}
	if get.Line != 12 {
		t.Errorf("get op line = %d, want 12", get.Line)
	}
	if len(get.Parameters) != 2 {
		t.Fatalf("get params = %d", len(get.Parameters))
	}
	if get.Parameters[0].In != "query" || get.Parameters[1].In != "header" {
		t.Errorf("param ins = %q, %q", get.Parameters[0].In, get.Parameters[1].In)
	}
	if get.Parameters[0].Line != 16 {
		t.Errorf("limit param line = %d, want 16", get.Parameters[0].Line)
	}
	if !reflect.DeepEqual(get.Responses.Keys, []string{"200", "404"}) {
		t.Errorf("response keys = %v", get.Responses.Keys)
	}
	notFound, _ := get.Responses.Get("404")
	if notFound.Ref != "#/components/responses/NotFound" {
		t.Errorf("404 ref = %q", notFound.Ref)
	}
}

func TestParseRequestBodyAndOneOf(t *testing.T) {
	doc := parsePetstore(t)
	item, _ := doc.Paths.Get("/pets/{petId}")
	patch := item.Patch
	if patch == nil {
		t.Fatal("no patch op")
	}
	if !reflect.DeepEqual(patch.RequestBody.Content.Keys, []string{"application/merge-patch+json"}) {
		t.Fatalf("content keys = %v", patch.RequestBody.Content.Keys)
	}
	mt, _ := patch.RequestBody.Content.Get("application/merge-patch+json")
	status, ok := mt.Schema.Properties.Get("status")
	if !ok {
		t.Fatal("no status property")
	}
	if len(status.OneOf) != 2 {
		t.Fatalf("oneOf len = %d", len(status.OneOf))
	}
	if status.OneOf[0].Type != "string" || len(status.OneOf[0].Enum) != 3 {
		t.Errorf("oneOf[0] = %+v", status.OneOf[0])
	}
	if status.OneOf[1].Type != "null" {
		t.Errorf("oneOf[1].Type = %q", status.OneOf[1].Type)
	}

	post, _ := doc.Paths.Get("/pets")
	if !reflect.DeepEqual(post.Post.RequestBody.Content.Keys, []string{"application/json", "application/xml"}) {
		t.Errorf("post content keys = %v", post.Post.RequestBody.Content.Keys)
	}
	if !post.Post.RequestBody.Required {
		t.Error("post body should be required")
	}
}

func TestParseComponentSchemas(t *testing.T) {
	doc := parsePetstore(t)
	wantSchemas := []string{"Pet", "Category", "NewPet", "PetBase"}
	if !reflect.DeepEqual(doc.Components.Schemas.Keys, wantSchemas) {
		t.Fatalf("schema keys = %v", doc.Components.Schemas.Keys)
	}
	pet, _ := doc.Components.Schemas.Get("Pet")
	wantProps := []string{"id", "name", "status", "vaccinated", "tags", "category", "owner", "attributes"}
	if !reflect.DeepEqual(pet.Properties.Keys, wantProps) {
		t.Fatalf("Pet props = %v", pet.Properties.Keys)
	}
	if !reflect.DeepEqual(pet.Required, []string{"id", "name"}) {
		t.Errorf("Pet required = %v", pet.Required)
	}

	id, _ := pet.Properties.Get("id")
	if id.Type != "integer" || id.Format != "int64" || !id.ReadOnly {
		t.Errorf("id = %+v", id)
	}
	status, _ := pet.Properties.Get("status")
	if len(status.Enum) != 3 || status.Enum[0] != "available" || status.Default != "available" {
		t.Errorf("status = %+v", status)
	}
	vacc, _ := pet.Properties.Get("vaccinated")
	if !vacc.Nullable || vacc.Type != "boolean" {
		t.Errorf("vaccinated = %+v", vacc)
	}
	tags, _ := pet.Properties.Get("tags")
	if tags.Type != "array" || tags.Items == nil || tags.Items.Type != "string" {
		t.Errorf("tags = %+v", tags)
	}
	category, _ := pet.Properties.Get("category")
	if category.Ref != "#/components/schemas/Category" {
		t.Errorf("category ref = %q", category.Ref)
	}
	owner, _ := pet.Properties.Get("owner")
	if owner.Ref != "./common.yaml#/components/schemas/Owner" {
		t.Errorf("owner ref = %q", owner.Ref)
	}
	attrs, _ := pet.Properties.Get("attributes")
	if attrs.AdditionalProperties == nil || attrs.AdditionalProperties.Schema == nil ||
		attrs.AdditionalProperties.Schema.Type != "string" {
		t.Errorf("attributes = %+v", attrs.AdditionalProperties)
	}

	cat, _ := doc.Components.Schemas.Get("Category")
	parent, _ := cat.Properties.Get("parent")
	if parent.Ref != "#/components/schemas/Category" {
		t.Errorf("parent ref = %q", parent.Ref)
	}

	newPet, _ := doc.Components.Schemas.Get("NewPet")
	if len(newPet.AllOf) != 2 || newPet.AllOf[0].Ref != "#/components/schemas/PetBase" {
		t.Errorf("NewPet allOf = %+v", newPet.AllOf)
	}
	if pet.Line != 101 {
		t.Errorf("Pet schema line = %d, want 101", pet.Line)
	}
	if got := doc.Components.Schemas.Line("Pet"); got != 100 {
		t.Errorf("Pet key line = %d, want 100", got)
	}
}

func TestParseJSONInput(t *testing.T) {
	doc, err := Parse([]byte(`{"openapi":"3.1.0","info":{"title":"J","version":"2"},"paths":{"/a":{"get":{"operationId":"getA","responses":{"200":{"description":"ok"}}}}}}`))
	if err != nil {
		t.Fatalf("Parse json: %v", err)
	}
	if doc.Info.Title != "J" {
		t.Errorf("title = %q", doc.Info.Title)
	}
	item, ok := doc.Paths.Get("/a")
	if !ok || item.Get == nil || item.Get.OperationID != "getA" {
		t.Errorf("paths = %+v", doc.Paths)
	}
}

func TestParseAdditionalPropertiesBool(t *testing.T) {
	doc, err := Parse([]byte("components:\n  schemas:\n    M:\n      type: object\n      additionalProperties: false\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	m, _ := doc.Components.Schemas.Get("M")
	if m.AdditionalProperties == nil || m.AdditionalProperties.Allowed == nil || *m.AdditionalProperties.Allowed {
		t.Errorf("additionalProperties = %+v", m.AdditionalProperties)
	}
}
