package browser

import (
	"slices"
	"testing"
)

func TestOpenRejectsNonHTTPLinks(t *testing.T) {
	for _, link := range []string{"file:///etc/passwd", "javascript:alert(1)", "vscode://x", "example.com", "http://", "  https://x.dev"} {
		if err := Open(link); err == nil {
			t.Errorf("Open(%q) should be rejected", link)
		}
	}
}

func TestCommandPerPlatform(t *testing.T) {
	cases := []struct {
		goos, name string
		ok         bool
	}{
		{"darwin", "open", true},
		{"linux", "xdg-open", true},
		{"windows", "", false},
	}
	for _, tc := range cases {
		name, args, ok := command(tc.goos, "https://go.dev")
		if name != tc.name || ok != tc.ok {
			t.Errorf("command(%s) = %q %v, want %q %v", tc.goos, name, ok, tc.name, tc.ok)
		}
		if ok && !slices.Equal(args, []string{"https://go.dev"}) {
			t.Errorf("command(%s) args = %q", tc.goos, args)
		}
	}
}
