package opcmd

import (
	"slices"
	"testing"
)

func inserts(candidates []Candidate) []string {
	var out []string
	for _, candidate := range candidates {
		out = append(out, candidate.Insert)
	}
	return out
}

func TestCompleteFragments(t *testing.T) {
	cases := []struct {
		value     string
		wantStart int
		want      []string
	}{
		{"echo {", 5, []string{"{$1}", "{$fpath}", "{$dpath}"}},
		{"echo {$", 5, []string{"{$1}", "{$fpath}", "{$dpath}"}},
		{"echo $", 5, []string{"{$1}", "{$fpath}", "{$dpath}"}},
		{"echo {$f", 5, []string{"{$fpath}"}},
		{"echo $d", 5, []string{"{$dpath}"}},
		{"cp {$1} {$2} {$", 13, []string{"{$3}", "{$1}", "{$2}", "{$fpath}", "{$dpath}"}},
		{"cp {$1} {$", 8, []string{"{$2}", "{$1}", "{$fpath}", "{$dpath}"}},
		{"cp {$1} {$1", 8, []string{"{$1}"}},
		{"echo {$4} $", 10, []string{"{$5}", "{$1}", "{$2}", "{$3}", "{$4}", "{$fpath}", "{$dpath}"}},
	}
	for _, tc := range cases {
		start, candidates := Complete(tc.value, len([]rune(tc.value)))
		if start != tc.wantStart || !slices.Equal(inserts(candidates), tc.want) {
			t.Errorf("Complete(%q) = %d %q, want %d %q", tc.value, start, inserts(candidates), tc.wantStart, tc.want)
		}
	}
}

func TestCompleteNoFragment(t *testing.T) {
	for _, value := range []string{"", "make", "echo $HOME", "awk '{print", "echo {$1}", "echo $x"} {
		if _, candidates := Complete(value, len([]rune(value))); len(candidates) != 0 {
			t.Errorf("Complete(%q) = %q, want none", value, inserts(candidates))
		}
	}
}

func TestCompleteMidText(t *testing.T) {
	value := "cat {$f} | wc"
	start, candidates := Complete(value, 7) // cursor right after "{$f"
	if start != 4 || !slices.Equal(inserts(candidates), []string{"{$fpath}"}) {
		t.Fatalf("mid-text: %d %q", start, inserts(candidates))
	}
	if candidates[0].Detail == "" {
		t.Fatal("system candidates should carry a description")
	}
}
