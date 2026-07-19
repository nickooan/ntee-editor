package clipboard

import "testing"

func TestOSC52Seq(t *testing.T) {
	got := osc52Seq("hi")
	want := "\x1b]52;c;aGk=\a" // base64("hi") == "aGk="
	if got != want {
		t.Fatalf("osc52Seq = %q, want %q", got, want)
	}
}
