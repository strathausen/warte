package store

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"warte/internal/core"
)

func TestFTSQuery(t *testing.T) {
	tests := []struct {
		mod  ftsModule
		in   string
		want string
	}{
		{fts5, "hello", `"hello"*`},
		{fts5, "  hello   world ", `"hello"* "world"*`},
		{fts5, `say "hi" OR NOT x`, `"say"* "hi"* "OR"* "NOT"* "x"*`},
		{fts5, "!!! ???", ""},
		{fts5, "c++", `"c++"*`},
		{fts4, "hello world", `"hello*" "world*"`},
		{fts4, `"`, ""},
	}
	for _, tt := range tests {
		if got := ftsQuery(tt.mod, tt.in); got != tt.want {
			t.Errorf("ftsQuery(%s, %q) = %q, want %q", tt.mod, tt.in, got, tt.want)
		}
	}
}

func TestParseSnippet(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		matches [][2]int
	}{
		{"plain", "plain", nil},
		{"say \x02hello\x03 world", "say hello world", [][2]int{{4, 9}}},
		{"\x02a\x03 b \x02c\x03", "a b c", [][2]int{{0, 1}, {4, 5}}},
		{"\x02foo\x03\x02bar\x03", "foobar", [][2]int{{0, 6}}},
		{"line\none \x02x\x03", "line one x", [][2]int{{9, 10}}},
		{"…ünï \x02cödé\x03…", "…ünï cödé…", [][2]int{{9, 15}}},
	}
	for _, tt := range tests {
		got, m := parseSnippet(tt.in)
		if got != tt.want || !reflect.DeepEqual(m, tt.matches) {
			t.Errorf("parseSnippet(%q) = %q %v, want %q %v", tt.in, got, m, tt.want, tt.matches)
		}
		for _, r := range m {
			if r[0] < 0 || r[1] > len(got) || r[0] >= r[1] {
				t.Errorf("bad range %v in %q", r, got)
			}
		}
	}
}

func seedSearch(t *testing.T, env *testEnv) map[string]core.MessageID {
	t.Helper()
	ctx := ctxT(t)
	if err := env.db.UpsertChat(ctx, core.Chat{ID: "g:club", Kind: core.ChatGroup, Title: "Book club"}); err != nil {
		t.Fatal(err)
	}
	if err := env.db.UpsertContact(ctx, core.Contact{ACI: "alice", ProfileName: "Alice"}); err != nil {
		t.Fatal(err)
	}
	ids := map[string]core.MessageID{}
	add := func(key string, m core.Message) {
		if _, err := env.db.PutMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
		ids[key] = m.ID
	}
	add("pizza1", msg("u:alice", "alice", 1000, "Let's get pizza tonight"))
	img := msg("u:alice", "alice", 2000, "pizza photo")
	img.Attachments = []core.Attachment{{ID: "i", ContentType: "image/jpeg"}}
	add("pizzaimg", img)
	add("club", msg("g:club", "bob", 3000, "Pizza at the book club?"))
	link := msg("g:club", "bob", 4000, "recipe: https://example.org/pizza")
	add("link", link)
	add("cafe", msg("u:alice", "alice", 5000, "Meet at the Café Müller"))
	vid := msg("g:club", "carol", 6000, "")
	vid.Attachments = []core.Attachment{{ID: "v", ContentType: "video/mp4"}}
	add("video", vid)
	file := msg("g:club", "carol", 7000, "the agenda")
	file.Attachments = []core.Attachment{{ID: "f", ContentType: "application/pdf", FileName: "agenda.pdf"}}
	add("file", file)
	voice := msg("u:alice", "alice", 8000, "")
	voice.Attachments = []core.Attachment{{ID: "a", ContentType: "audio/aac", VoiceNote: true}}
	add("voice", voice)
	sys := msg("g:club", "bob", 9000, "")
	sys.Kind, sys.SystemText = core.KindSystem, "Bob changed pizza settings"
	add("system", sys)
	return ids
}

func TestSearch(t *testing.T) {
	env := newTestDB(t)
	ctx := ctxT(t)
	ids := seedSearch(t, env)
	key := func(keys ...string) []core.MessageID {
		var out []core.MessageID
		for _, k := range keys {
			out = append(out, ids[k])
		}
		return out
	}
	tests := []struct {
		name string
		q    core.SearchQuery
		want []core.MessageID
	}{
		{"word newest first", core.SearchQuery{Text: "pizza"}, key("link", "club", "pizzaimg", "pizza1")},
		{"case insensitive", core.SearchQuery{Text: "PIZZA tonight"}, key("pizza1")},
		{"prefix", core.SearchQuery{Text: "piz"}, key("link", "club", "pizzaimg", "pizza1")},
		{"diacritics", core.SearchQuery{Text: "cafe muller"}, key("cafe")},
		{"chat filter", core.SearchQuery{Text: "pizza", Chat: "g:club"}, key("link", "club")},
		{"from filter", core.SearchQuery{Text: "pizza", From: "alice"}, key("pizzaimg", "pizza1")},
		{"has image", core.SearchQuery{Text: "pizza", HasType: "image"}, key("pizzaimg")},
		{"has link", core.SearchQuery{Text: "pizza", HasType: "link"}, key("link")},
		{"before", core.SearchQuery{Text: "pizza", Before: time.UnixMilli(3000)}, key("pizzaimg", "pizza1")},
		{"after", core.SearchQuery{Text: "pizza", After: time.UnixMilli(2000)}, key("link", "club")},
		{"limit", core.SearchQuery{Text: "pizza", Limit: 2}, key("link", "club")},
		{"no match", core.SearchQuery{Text: "sushi"}, nil},
		{"operators literal", core.SearchQuery{Text: `pizza" OR "x`}, nil},
		{"punctuation only", core.SearchQuery{Text: "***"}, nil},
		{"gallery video", core.SearchQuery{Chat: "g:club", HasType: "video"}, key("video")},
		{"gallery file", core.SearchQuery{HasType: "file"}, key("file")},
		{"gallery audio", core.SearchQuery{HasType: "audio"}, key("voice")},
		{"filter only from", core.SearchQuery{From: "carol"}, key("file", "video")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hits, err := env.db.Search(ctx, tt.q)
			if err != nil {
				t.Fatal(err)
			}
			var got []core.MessageID
			for _, h := range hits {
				got = append(got, h.Message.ID)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
	if _, err := env.db.Search(ctx, core.SearchQuery{HasType: "hologram"}); err == nil {
		t.Error("unknown HasType should fail")
	}
}

func TestSearchHitDetails(t *testing.T) {
	env := newTestDB(t)
	ctx := ctxT(t)
	ids := seedSearch(t, env)
	hits, err := env.db.Search(ctx, core.SearchQuery{Text: "book pizza", Chat: "g:club"})
	if err != nil || len(hits) != 1 {
		t.Fatalf("hits = %d, %v", len(hits), err)
	}
	h := hits[0]
	if h.Message.ID != ids["club"] || h.ChatTitle != "Book club" {
		t.Errorf("hit = %v %q", h.Message.ID, h.ChatTitle)
	}
	if h.Snippet != "Pizza at the book club?" {
		t.Errorf("snippet = %q", h.Snippet)
	}
	var words []string
	for _, r := range h.Matches {
		words = append(words, h.Snippet[r[0]:r[1]])
	}
	if !slices.Equal(words, []string{"Pizza", "book"}) {
		t.Errorf("matched words = %v (ranges %v)", words, h.Matches)
	}
	// Direct chat title follows the contact name; Sender name is resolved.
	hits, _ = env.db.Search(ctx, core.SearchQuery{Text: "tonight"})
	if len(hits) != 1 || hits[0].ChatTitle != "Alice" || hits[0].Message.SenderName != "Alice" {
		t.Errorf("direct hit = %+v", hits)
	}
	// Long bodies get an excerpt around the match.
	long := strings.Repeat("filler ", 60) + "needle " + strings.Repeat("filler ", 60)
	if _, err := env.db.PutMessage(ctx, msg("u:z", "z", 99000, long)); err != nil {
		t.Fatal(err)
	}
	hits, _ = env.db.Search(ctx, core.SearchQuery{Text: "needle"})
	if len(hits) != 1 {
		t.Fatalf("needle hits = %d", len(hits))
	}
	h = hits[0]
	if len(h.Snippet) >= len(long) || !strings.Contains(h.Snippet, "…") || len(h.Matches) != 1 ||
		h.Snippet[h.Matches[0][0]:h.Matches[0][1]] != "needle" {
		t.Errorf("excerpt = %q %v", h.Snippet, h.Matches)
	}
	// Filter-only search uses preview text as snippet.
	hits, _ = env.db.Search(ctx, core.SearchQuery{HasType: "audio"})
	if len(hits) != 1 || hits[0].Snippet != "🎤 Voice message" || hits[0].Matches != nil {
		t.Errorf("filter-only hit = %+v", hits)
	}
}

func TestSearchIndexMaintenance(t *testing.T) {
	env := newTestDB(t)
	ctx := ctxT(t)
	m := msg("u:a", "a", 1000, "alpha")
	if _, err := env.db.PutMessage(ctx, m); err != nil {
		t.Fatal(err)
	}
	count := func(q string) int {
		hits, err := env.db.Search(ctx, core.SearchQuery{Text: q})
		if err != nil {
			t.Fatal(err)
		}
		return len(hits)
	}
	m.Body = "beta"
	if _, err := env.db.PutMessage(ctx, m); err != nil {
		t.Fatal(err)
	}
	if count("alpha") != 0 || count("beta") != 1 {
		t.Errorf("replace: alpha=%d beta=%d", count("alpha"), count("beta"))
	}
	if _, err := env.db.SetReaction(ctx, m.ID, core.Reaction{Emoji: "👍", From: "b"}); err != nil {
		t.Fatal(err)
	}
	if count("beta") != 1 {
		t.Error("metadata update dropped index entry")
	}
	if err := env.db.DeleteMessage(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if count("beta") != 0 {
		t.Error("deleted message still indexed")
	}
	// The FTS index stays consistent (integrity-check passes).
	if env.db.FTSModule() == "fts5" {
		_, err := env.db.RawDB().Exec(`INSERT INTO warte_fts (warte_fts, rank) VALUES ('integrity-check', 0)`)
		if err != nil {
			t.Errorf("fts5 integrity-check: %v", err)
		}
	} else {
		_, err := env.db.RawDB().Exec(`INSERT INTO warte_fts (warte_fts) VALUES ('integrity-check')`)
		if err != nil {
			t.Errorf("fts4 integrity-check: %v", err)
		}
	}
}
