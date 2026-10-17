package theme

import "testing"

func TestSanitizeWidthTraps(t *testing.T) {
	cases := map[string]string{
		"a\u2028b":           "a\nb",
		"soft\u00adhyphen":   "softhyphen",
		"key 1\ufe0f\u20e3!": "key 1\u20e3!",
	}
	for in, want := range cases {
		if got := Sanitize(in); got != want {
			t.Errorf("Sanitize(%+q) = %+q, want %+q", in, got, want)
		}
	}
	if got := SanitizeLine("a\u2029b"); got != "a b" {
		t.Errorf("SanitizeLine = %+q", got)
	}
}
