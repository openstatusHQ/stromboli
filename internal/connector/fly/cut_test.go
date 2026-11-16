package fly

import (
	"testing"
	"unicode/utf8"
)

func TestCutKeepsRunes(t *testing.T) {
	for _, tc := range []struct {
		in   string
		n    int
		want string
	}{
		{"abc", 10, "abc"},
		{"abcdef", 3, "abc"},
		// The 3-byte cut lands in the middle of the second "é".
		{"ééé", 3, "é"},
	} {
		got := cut(tc.in, tc.n)
		if got != tc.want {
			t.Errorf("cut(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
		if !utf8.ValidString(got) {
			t.Errorf("cut(%q, %d) = %q, not valid UTF-8", tc.in, tc.n, got)
		}
	}
}
