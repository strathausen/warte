package format

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestSanitizeDisplayWidthTraps: characters whose cell width differs between
// terminals and the layout code are normalized.
func TestSanitizeDisplayWidthTraps(t *testing.T) {
	cases := map[string]string{
		"a\u2028b\u2029c":    "a\nb\nc",
		"soft\u00adhyphen":   "softhyphen",
		"key 1\ufe0f\u20e3!": "key 1\u20e3!",
		"heart \u2764\ufe0f": "heart \u2764\ufe0f",
	}
	for in, want := range cases {
		if got := SanitizeDisplay(in); got != want {
			t.Errorf("SanitizeDisplay(%+q) = %+q, want %+q", in, got, want)
		}
	}
	if got := sanitizeLine("a\u2028b"); got != "a b" {
		t.Errorf("sanitizeLine = %+q", got)
	}
	// Truncation and width agree on the result.
	s := SanitizeDisplay("ab 1\ufe0f\u20e3 cdef")
	for n := 1; n < ansi.StringWidth(s); n++ {
		if w := ansi.StringWidth(ansi.Truncate(s, n, "")); w > n {
			t.Fatalf("truncate %d -> %d cells", n, w)
		}
	}
}
