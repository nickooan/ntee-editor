package input

import "testing"

func TestRemoveAtCursor(t *testing.T) {
	cases := []struct {
		text       string
		cursor     int
		wantText   string
		wantCursor int
		wantOK     bool
	}{
		{"abc", 1, "ac", 1, true},
		{"abc", 0, "bc", 0, true},
		{"abc", 3, "abc", 3, false},
		{"", 0, "", 0, false},
		{"héllo", 1, "hllo", 1, true},
		{"ab", 9, "ab", 2, false},
	}
	for _, tc := range cases {
		text, cursor, ok := RemoveAtCursor(tc.text, tc.cursor)
		if text != tc.wantText || cursor != tc.wantCursor || ok != tc.wantOK {
			t.Errorf("RemoveAtCursor(%q, %d) = %q, %d, %v; want %q, %d, %v",
				tc.text, tc.cursor, text, cursor, ok, tc.wantText, tc.wantCursor, tc.wantOK)
		}
	}
}
