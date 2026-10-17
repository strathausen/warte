package format

import (
	"reflect"
	"testing"
)

func TestFindLinks(t *testing.T) {
	type L struct {
		Text, URL string
		Kind      LinkKind
	}
	tests := []struct {
		in   string
		want []L
	}{
		{"", nil},
		{"no links here.", nil},
		{"https://signal.org", []L{{"https://signal.org", "https://signal.org", LinkURL}}},
		{"go to HTTP://Example.com/path?q=1#frag now", []L{{"HTTP://Example.com/path?q=1#frag", "HTTP://Example.com/path?q=1#frag", LinkURL}}},
		{"(see https://en.wikipedia.org/wiki/Go_(programming_language))", []L{{"https://en.wikipedia.org/wiki/Go_(programming_language)", "https://en.wikipedia.org/wiki/Go_(programming_language)", LinkURL}}},
		{"(https://example.com)", []L{{"https://example.com", "https://example.com", LinkURL}}},
		{"end https://example.com/a.", []L{{"https://example.com/a", "https://example.com/a", LinkURL}}},
		{"www.example.com, ok", []L{{"www.example.com", "https://www.example.com", LinkURL}}},
		{"example.com/foo", []L{{"example.com/foo", "https://example.com/foo", LinkURL}}},
		{"sub.example.de!", []L{{"sub.example.de", "https://sub.example.de", LinkURL}}},
		{"main.go file.txt e.g. 1.2.3 node.js", nil},
		{"mail me@example.com.", []L{{"me@example.com", "mailto:me@example.com", LinkEmail}}},
		{"call +49 30 1234567 now", []L{{"+49 30 1234567", "tel:+49301234567", LinkPhone}}},
		{"version 1.2 +1", nil},
		{"two: a.com b.org", []L{{"a.com", "https://a.com", LinkURL}, {"b.org", "https://b.org", LinkURL}}},
		{"👉https://x.io", []L{{"https://x.io", "https://x.io", LinkURL}}},
		{"https://ex.com/￼tail", []L{{"https://ex.com/", "https://ex.com/", LinkURL}}},
		{"user.name@mail.example.org", []L{{"user.name@mail.example.org", "mailto:user.name@mail.example.org", LinkEmail}}},
	}
	for _, tt := range tests {
		var got []L
		for _, l := range FindLinks(tt.in) {
			if tt.in[l.Start:l.End] != l.Text {
				t.Errorf("%q: offsets %d:%d do not match text %q", tt.in, l.Start, l.End, l.Text)
			}
			got = append(got, L{l.Text, l.URL, l.Kind})
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("FindLinks(%q)\n got %+v\nwant %+v", tt.in, got, tt.want)
		}
	}
}
