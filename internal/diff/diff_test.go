package diff

import (
	"fmt"
	"testing"
)

// checkScript verifies ops is a valid edit script from a to b: the non-Insert
// ops walk a's indices 0..len(a)-1 in order, the non-Delete ops walk b's
// indices in order, and Equal ops join genuinely equal lines.
func checkScript(t *testing.T, a, b []string, ops []Op) {
	t.Helper()
	ai, bi := 0, 0
	for _, op := range ops {
		switch op.Kind {
		case Equal:
			if op.AIdx != ai || op.BIdx != bi {
				t.Fatalf("Equal at a=%d b=%d, want a=%d b=%d", op.AIdx, op.BIdx, ai, bi)
			}
			if a[ai] != b[bi] {
				t.Fatalf("Equal joins different lines: %q vs %q", a[ai], b[bi])
			}
			ai++
			bi++
		case Delete:
			if op.AIdx != ai {
				t.Fatalf("Delete at a=%d, want a=%d", op.AIdx, ai)
			}
			ai++
		case Insert:
			if op.BIdx != bi {
				t.Fatalf("Insert at b=%d, want b=%d", op.BIdx, bi)
			}
			bi++
		}
	}
	if ai != len(a) || bi != len(b) {
		t.Fatalf("script consumed a=%d/%d b=%d/%d", ai, len(a), bi, len(b))
	}
}

// kinds compresses ops to a compact string like "=-+=" for easy assertions.
func kinds(ops []Op) string {
	out := make([]byte, len(ops))
	for i, op := range ops {
		switch op.Kind {
		case Equal:
			out[i] = '='
		case Delete:
			out[i] = '-'
		case Insert:
			out[i] = '+'
		}
	}
	return string(out)
}

func TestLines(t *testing.T) {
	cases := []struct {
		name string
		a, b []string
		want string
	}{
		{"both empty", nil, nil, ""},
		{"identical", []string{"a", "b", "c"}, []string{"a", "b", "c"}, "==="},
		{"new file", nil, []string{"x", "y"}, "++"},
		{"emptied file", []string{"x", "y"}, nil, "--"},
		{"pure insert middle", []string{"a", "z"}, []string{"a", "m", "n", "z"}, "=++="},
		{"pure delete middle", []string{"a", "m", "n", "z"}, []string{"a", "z"}, "=--="},
		{"single replace is del then add", []string{"a", "old", "z"}, []string{"a", "new", "z"}, "=-+="},
		{"interleaved changes grouped per run",
			[]string{"a", "1", "b", "2", "c"},
			[]string{"a", "one", "b", "two", "c"},
			"=-+=-+="},
		{"multi-line replace groups dels before adds",
			[]string{"a", "1", "2", "z"},
			[]string{"a", "x", "y", "w", "z"},
			"=--+++="},
		{"prefix only changed", []string{"old", "b", "c"}, []string{"new", "b", "c"}, "-+=="},
		{"suffix only changed", []string{"a", "b", "old"}, []string{"a", "b", "new"}, "==-+"},
		{"trailing insert", []string{"a", "b"}, []string{"a", "b", "c"}, "==+"},
		{"repeated lines", []string{"x", "x", "x"}, []string{"x", "x"}, "==-"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ops := Lines(tc.a, tc.b)
			checkScript(t, tc.a, tc.b, ops)
			if got := kinds(ops); got != tc.want {
				t.Fatalf("kinds = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestLinesBoundFallback drives D past maxD with two fully disjoint inputs and
// expects the degraded "all deletes then all inserts" script — still valid.
func TestLinesBoundFallback(t *testing.T) {
	n := maxD // disjoint a and b of this size need D = 2*maxD > maxD
	a := make([]string, n)
	b := make([]string, n)
	for i := range a {
		a[i] = fmt.Sprintf("a%d", i)
		b[i] = fmt.Sprintf("b%d", i)
	}
	ops := Lines(a, b)
	checkScript(t, a, b, ops)
	for i, op := range ops {
		want := Delete
		if i >= n {
			want = Insert
		}
		if op.Kind != want {
			t.Fatalf("op %d kind = %d, want %d", i, op.Kind, want)
		}
	}
}

// TestLinesLargeUnchanged guards the trim fast path: a big identical input
// must not allocate a Myers trace at all (it would be visible as time here).
func TestLinesLargeUnchanged(t *testing.T) {
	a := make([]string, 200_000)
	for i := range a {
		a[i] = fmt.Sprintf("line %d", i)
	}
	b := append([]string(nil), a...)
	b[100_000] = "changed"
	ops := Lines(a, b)
	checkScript(t, a, b, ops)
	changed := 0
	for _, op := range ops {
		if op.Kind != Equal {
			changed++
		}
	}
	if changed != 2 {
		t.Fatalf("changed ops = %d, want 2", changed)
	}
}
