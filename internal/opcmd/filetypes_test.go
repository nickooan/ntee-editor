package opcmd

import (
	"strings"
	"testing"
)

func TestNormalizeFileTypes(t *testing.T) {
	cases := []struct{ spec, want string }{
		{" .Go , rb,go", "go,rb"},
		{"go,*", "*"},
		{"", "*"},
		{"  ", "*"},
		{"tf, tfvars", "tf,tfvars"},
		{"go,,rb", "go,rb"},
		{"Dockerfile", "dockerfile"},
		{"c++,objective-c", "c++,objective-c"},
	}
	for _, tc := range cases {
		if got, err := NormalizeFileTypes(tc.spec); err != nil || got != tc.want {
			t.Errorf("NormalizeFileTypes(%q) = %q, %v; want %q", tc.spec, got, err, tc.want)
		}
	}
	for _, spec := range []string{"a b", "go,r/b", "go;rb"} {
		if _, err := NormalizeFileTypes(spec); err == nil {
			t.Errorf("NormalizeFileTypes(%q) should fail", spec)
		}
	}
	if _, err := NormalizeFileTypes("go, a b"); err == nil || err.Error() != `file type "a b": use letters, digits, . _ + -` {
		t.Errorf("error text = %v", err)
	}
}

func TestMatchFileType(t *testing.T) {
	cases := []struct {
		types, file       string
		matched, specific bool
	}{
		{"go", "main.go", true, true},
		{"go,rb", "lib/app.rb", true, true},
		{"tfvars", "env/prod.tfvars", true, true},
		{"tf", "env/prod.tfvars", false, false},
		{"dockerfile", "deploy/Dockerfile", true, true},
		{"go", "README", false, false},
		{"*", "README", true, false},
		{"", "main.go", true, false},
		{"go", "MAIN.GO", true, true},
	}
	for _, tc := range cases {
		matched, specific := MatchFileType(tc.types, tc.file)
		if matched != tc.matched || specific != tc.specific {
			t.Errorf("MatchFileType(%q, %q) = %v, %v; want %v, %v", tc.types, tc.file, matched, specific, tc.matched, tc.specific)
		}
	}
	if got := FileTypeOf("deploy/Dockerfile"); got != "dockerfile" {
		t.Errorf("FileTypeOf(Dockerfile) = %q", got)
	}
	if got := FileTypeOf("a/b.TFVARS"); got != "tfvars" {
		t.Errorf("FileTypeOf(b.TFVARS) = %q", got)
	}
}

func TestFileTypesOverlap(t *testing.T) {
	cases := []struct {
		first, second string
		want          bool
	}{
		{"go,rb", "rb", true},
		{"go", "rb", false},
		{"*", "*", true},
		{"*", "go", false},
		{"", "*", true},
		{"sh,bash", "zsh,bash", true},
	}
	for _, tc := range cases {
		if got := FileTypesOverlap(tc.first, tc.second); got != tc.want {
			t.Errorf("FileTypesOverlap(%q, %q) = %v, want %v", tc.first, tc.second, got, tc.want)
		}
	}
}

func joinedInserts(candidates []Candidate) string {
	return strings.Join(inserts(candidates), " ")
}

func TestCompleteFileType(t *testing.T) {
	start, candidates := CompleteFileType("t", 1, "")
	if got := joinedInserts(candidates); start != 0 || got != "ts tsx toml tf tfvars" {
		t.Errorf(`"t" = %d %q`, start, got)
	}
	start, candidates = CompleteFileType("go, r", 5, "")
	if got := joinedInserts(candidates); start != 4 || got != "rb rs rake" {
		t.Errorf(`"go, r" = %d %q`, start, got)
	}
	if _, candidates = CompleteFileType("go, g", 5, ""); strings.Contains(joinedInserts(candidates), "go") {
		t.Errorf("a listed type should not be offered again: %q", joinedInserts(candidates))
	}
	_, candidates = CompleteFileType("", 0, "go")
	if len(candidates) < 3 || candidates[0].Insert != "go" || candidates[0].Detail != "open file" || candidates[1].Insert != "*" {
		t.Errorf("empty field should offer the open file's type, then *: %q", joinedInserts(candidates))
	}
	if _, candidates = CompleteFileType("go, ", 4, ""); strings.Contains(joinedInserts(candidates), "*") {
		t.Errorf("* only belongs in an otherwise empty spec: %q", joinedInserts(candidates))
	}
	if _, candidates = CompleteFileType("tf", 2, "go"); joinedInserts(candidates) != "tf tfvars" {
		t.Errorf("an exact match should rank first: %q", joinedInserts(candidates))
	}
	if _, candidates = CompleteFileType(".g", 2, ""); len(candidates) == 0 || candidates[0].Insert != "go" {
		t.Errorf("a leading dot should still complete: %q", joinedInserts(candidates))
	}
	if _, candidates = CompleteFileType("a b", 3, ""); len(candidates) != 0 {
		t.Errorf("an invalid item offers nothing: %q", joinedInserts(candidates))
	}
}
