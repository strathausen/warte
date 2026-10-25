package format

import "testing"

func TestEmoticonBefore(t *testing.T) {
	for _, tc := range []struct {
		in    string
		start int
		emoji string
	}{
		{":)", 0, "🙂"},
		{"hi :-)", 3, "🙂"},
		{"line\n<3", 5, "❤️"},
		{"ok xD", 3, "😆"},
	} {
		start, emoji, ok := EmoticonBefore(tc.in, Emoticons)
		if !ok || start != tc.start || emoji != tc.emoji {
			t.Errorf("%q: got %d %q %v, want %d %q", tc.in, start, emoji, ok, tc.start, tc.emoji)
		}
	}
	for _, in := range []string{"", "hi ", "http://", "a:)", "see `x :)", "B)", "8)"} {
		if _, _, ok := EmoticonBefore(in, Emoticons); ok {
			t.Errorf("%q: converted", in)
		}
	}
	if _, _, ok := EmoticonBefore(":)", map[string]string{":)": ""}); ok {
		t.Error("an empty emoji disables an emoticon")
	}
}
