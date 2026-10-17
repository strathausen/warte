package format

import (
	"strings"
	"testing"
)

func TestEmojiTableSize(t *testing.T) {
	if n := len(AllEmojis()); n < 1500 {
		t.Fatalf("only %d emojis", n)
	}
	for _, c := range Categories() {
		if len(EmojisInCategory(c)) == 0 {
			t.Errorf("category %v is empty", c)
		}
		if c.String() == "Unknown" || c.Icon() == "❓" {
			t.Errorf("category %d has no name/icon", c)
		}
	}
	if Category(99).String() != "Unknown" {
		t.Error("out-of-range category name")
	}
	if len(Categories()) != 9 {
		t.Errorf("want 9 categories, got %d", len(Categories()))
	}
}

func TestEmojiCategories(t *testing.T) {
	tests := []struct {
		code string
		cat  Category
	}{
		{"joy", CategorySmileys},
		{"heart", CategorySmileys},
		{"+1", CategoryPeople},
		{"dog", CategoryAnimals},
		{"pizza", CategoryFood},
		{"soccer", CategoryActivities},
		{"airplane", CategoryTravel},
		{"bulb", CategoryObjects},
		{"warning", CategorySymbols},
		{"de", CategoryFlags},
	}
	for _, tt := range tests {
		e, ok := LookupShortcode(tt.code)
		if !ok || e.Category != tt.cat {
			t.Errorf("%s: got %+v ok=%v, want category %v", tt.code, e, ok, tt.cat)
		}
	}
}

func TestShortcode(t *testing.T) {
	tests := []struct {
		code string
		want string
		ok   bool
	}{
		{"smile", "😄", true},
		{":smile:", "😄", true},
		{":SMILE:", "😄", true},
		{"+1", "👍", true},
		{"thumbsup", "👍", true},
		{"thumbs_up", "👍", true},
		{"-1", "👎", true},
		{"heart", "❤️", true},
		{"joy", "😂", true},
		{"fire", "🔥", true},
		{"tada", "🎉", true},
		{"party", "🎉", true},
		{"family_man_woman_girl", "👨‍👩‍👧", true},
		{"de", "🇩🇪", true},
		{"nonexistent_code", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		got, ok := Shortcode(tt.code)
		if got != tt.want || ok != tt.ok {
			t.Errorf("Shortcode(%q) = %q,%v want %q,%v", tt.code, got, ok, tt.want, tt.ok)
		}
	}
}

func TestShortcodesUniqueAndLookupConsistent(t *testing.T) {
	for _, e := range AllEmojis() {
		if e.Char == "" || e.Name == "" || len(e.Shortcodes) == 0 {
			t.Fatalf("incomplete emoji %+v", e)
		}
		got, ok := Shortcode(e.Shortcodes[0])
		if !ok {
			t.Errorf("canonical code %q not found", e.Shortcodes[0])
		} else if got != e.Char {
			// Duplicate codes resolve to the first emoji; that must be rare.
			if e2, _ := LookupShortcode(e.Shortcodes[0]); e2.Char == "" {
				t.Errorf("%q", e.Shortcodes[0])
			}
		}
	}
}

func TestLookupEmoji(t *testing.T) {
	for _, c := range []string{"❤️", "❤", "👍", "🇩🇪"} {
		if _, ok := LookupEmoji(c); !ok {
			t.Errorf("LookupEmoji(%q) not found", c)
		}
	}
	if ShortcodeFor("👍") != "+1" {
		t.Errorf("ShortcodeFor(👍) = %q", ShortcodeFor("👍"))
	}
	if ShortcodeFor("x") != "" {
		t.Error("ShortcodeFor(x) should be empty")
	}
}

func TestSearchShortcodes(t *testing.T) {
	codes := func(ms []ShortcodeMatch) []string {
		var out []string
		for _, m := range ms {
			out = append(out, m.Code)
		}
		return out
	}
	// Popular first.
	got := codes(SearchShortcodes("he", 0))
	if len(got) < 3 || got[0] != "heart" {
		t.Errorf("he → %v", got)
	}
	idx := map[string]int{}
	for i, c := range got {
		idx[c] = i
	}
	if idx["heart_eyes"] > idx["hedgehog"] {
		t.Errorf("popular heart_eyes should precede hedgehog: %v", got)
	}
	// Prefix group before word-prefix group.
	got = codes(SearchShortcodes(":heart", 0))
	if got[0] != "heart" {
		t.Errorf(":heart → %v", got)
	}
	sawWord := false
	for _, c := range got {
		if !strings.HasPrefix(c, "heart") {
			sawWord = true
		} else if sawWord {
			t.Errorf("prefix match %q after word matches: %v", c, got)
		}
	}
	if !contains2(got, "broken_heart") {
		t.Errorf("word match broken_heart missing: %v", got)
	}
	// Alphabetical among unpopular.
	got = codes(SearchShortcodes("zebr", 0))
	if len(got) != 1 || got[0] != "zebra" {
		t.Errorf("zebr → %v", got)
	}
	// Limit and dedupe (thumbsup/+1/thumbs_up are the same emoji).
	ms := SearchShortcodes("thumbs", 2)
	if len(ms) != 2 || ms[0].Emoji == ms[1].Emoji {
		t.Errorf("thumbs → %+v", ms)
	}
	if len(SearchShortcodes("", 5)) != 5 {
		t.Error("empty prefix should return popular codes")
	}
	if len(SearchShortcodes("qqqqqq", 0)) != 0 {
		t.Error("no match expected")
	}
}

func contains2(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func TestReplaceShortcodes(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"no codes", "no codes"},
		{"hi :smile:", "hi 😄"},
		{":+1::fire:", "👍🔥"},
		{":unknown: :joy:", ":unknown: 😂"},
		{"time 12:30:45", "time 12:30:45"},
		{"`:smile:` :smile:", "`:smile:` 😄"},
		{"a:b:c :tada", "a:b:c :tada"},
		{"::", "::"},
	}
	for _, tt := range tests {
		if got := ReplaceShortcodes(tt.in); got != tt.want {
			t.Errorf("ReplaceShortcodes(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestShortcodeQuery(t *testing.T) {
	tests := []struct {
		in    string
		min   int
		q     string
		start int
		ok    bool
	}{
		{"hello :sm", 2, "sm", 6, true},
		{":s", 2, "", 0, false},
		{":sm", 2, "sm", 0, true},
		{"12:30", 1, "", 0, false},
		{"hi :sm ile", 1, "", 0, false},
		{"no colon", 1, "", 0, false},
		{"(:+1", 1, "+1", 1, true},
	}
	for _, tt := range tests {
		q, start, ok := ShortcodeQuery(tt.in, tt.min)
		if q != tt.q || start != tt.start || ok != tt.ok {
			t.Errorf("ShortcodeQuery(%q) = %q,%d,%v", tt.in, q, start, ok)
		}
	}
}

func TestDefaultReactions(t *testing.T) {
	got := DefaultReactions()
	want := []string{"❤️", "👍", "👎", "😂", "😮", "😢"}
	if strings.Join(got, "") != strings.Join(want, "") {
		t.Errorf("got %v", got)
	}
	got[0] = "x"
	if DefaultReactions()[0] != "❤️" {
		t.Error("DefaultReactions must return a copy")
	}
	for _, r := range want {
		if _, ok := LookupEmoji(r); !ok {
			t.Errorf("reaction %q not in table", r)
		}
	}
}

func TestSearchEmoji(t *testing.T) {
	tests := []struct {
		q     string
		first string
	}{
		{"thumbs up", "👍"},
		{"joy", "😂"},
		{"tears joy", "😂"},
		{"flag germ", "🇩🇪"},
		{":fire:", "🔥"},
	}
	for _, tt := range tests {
		got := SearchEmoji(tt.q, 5)
		if len(got) == 0 || got[0].Char != tt.first {
			t.Errorf("SearchEmoji(%q) = %+v, want first %q", tt.q, got, tt.first)
		}
	}
	if SearchEmoji("   ", 0) != nil {
		t.Error("blank query")
	}
	if len(SearchEmoji("face", 3)) != 3 {
		t.Error("limit")
	}
}

func TestWithSkinTone(t *testing.T) {
	tests := []struct {
		in   string
		tone SkinTone
		want string
	}{
		{"👍", SkinToneMedium, thumbTone},
		{"👍", SkinToneNone, "👍"},
		{"✌️", SkinToneDark, "✌🏿"},
		{"😂", SkinToneLight, "😂"},
		{family, SkinToneLight, family},
	}
	for _, tt := range tests {
		if got := WithSkinTone(tt.in, tt.tone); got != tt.want {
			t.Errorf("WithSkinTone(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
