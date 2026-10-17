package store

import (
	"math"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"warte/internal/core"
)

func TestOpenRejectsDSNPaths(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"", ":memory:", "file:x.db", filepath.Join(dir, "a.db?_pragma_key=x")} {
		if _, err := Open(p, randKey(t)); err == nil {
			t.Errorf("Open(%q) succeeded", p)
		}
	}
}

func TestPutMessageRejectsBadIDs(t *testing.T) {
	env := newTestDB(t)
	ctx := ctxT(t)
	bad := []core.MessageID{
		{Chat: "u:a", Author: "a", Timestamp: math.MaxInt64 + 1},
		{Chat: "x:a", Author: "a", Timestamp: 1},
		{Chat: "", Author: "a", Timestamp: 1},
		{Chat: "u:a", Author: "a", Timestamp: 0},
	}
	for _, id := range bad {
		m := msg(id.Chat, id.Author, 1, "x")
		m.ID = id
		if _, err := env.db.PutMessage(ctx, m); err == nil {
			t.Errorf("PutMessage(%v) succeeded", id)
		}
	}
}

func TestSearchExcludesRemoteDeletedBeforeLimit(t *testing.T) {
	env := newTestDB(t)
	ctx := ctxT(t)
	for i, body := range []string{"one", "two", "three", "four"} {
		m := msg("u:a", "a", uint64(1000+i), body)
		m.RemoteDeleted = i >= 2 // the two newest are deleted
		if _, err := env.db.PutMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	hits, err := env.db.Search(ctx, core.SearchQuery{Chat: "u:a", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 || hits[0].Message.Body != "two" || hits[1].Message.Body != "one" {
		var got []string
		for _, h := range hits {
			got = append(got, h.Message.Body)
		}
		t.Fatalf("hits = %q, want [two one]", got)
	}
	// Un-deleting via UpdateMessage brings it back.
	if _, err := env.db.UpdateMessage(ctx, core.MessageID{Chat: "u:a", Author: "a", Timestamp: 1003},
		func(m *core.Message) error { m.RemoteDeleted = false; return nil }); err != nil {
		t.Fatal(err)
	}
	hits, _ = env.db.Search(ctx, core.SearchQuery{Text: "four"})
	if len(hits) != 1 {
		t.Fatalf("search after undelete = %d hits", len(hits))
	}
}

func TestSearchSnippetCannotBeForged(t *testing.T) {
	env := newTestDB(t)
	ctx := ctxT(t)
	body := "evil \x02fake\x03 \x1b[31mred\x1b[0m\nhello ‮world"
	if _, err := env.db.PutMessage(ctx, msg("u:a", "a", 1000, body)); err != nil {
		t.Fatal(err)
	}
	hits, err := env.db.Search(ctx, core.SearchQuery{Text: "hello"})
	if err != nil || len(hits) != 1 {
		t.Fatalf("search = %d, %v", len(hits), err)
	}
	h := hits[0]
	if h.Message.Body != body {
		t.Errorf("stored body altered: %q", h.Message.Body)
	}
	if strings.ContainsFunc(h.Snippet, func(r rune) bool { return unicode.IsControl(r) || isBidiControl(r) }) {
		t.Errorf("snippet contains control characters: %q", h.Snippet)
	}
	if len(h.Matches) != 1 || h.Snippet[h.Matches[0][0]:h.Matches[0][1]] != "hello" {
		t.Errorf("matches = %v in %q, want only \"hello\"", h.Matches, h.Snippet)
	}
	// Control characters do not glue words together for search.
	if hits, _ := env.db.Search(ctx, core.SearchQuery{Text: "fake"}); len(hits) != 1 {
		t.Errorf("search fake = %d hits", len(hits))
	}
}

func TestPreviewStripsControlCharacters(t *testing.T) {
	m := msg("u:a", "a", 1000, "\x1b]0;pwned\x07hi\u009b31m ‮dlrow")
	got := PreviewText(m, nil)
	if strings.ContainsFunc(got, func(r rune) bool { return unicode.IsControl(r) || isBidiControl(r) }) {
		t.Errorf("preview contains control characters: %q", got)
	}
	if !strings.Contains(got, "hi") || !strings.Contains(got, "dlrow") {
		t.Errorf("preview lost text: %q", got)
	}
	m.Attachments = []core.Attachment{{ContentType: "application/pdf", FileName: "a\x1b[2Jb.pdf"}}
	if got := PreviewText(m, nil); strings.ContainsRune(got, '\x1b') {
		t.Errorf("file name escape survived: %q", got)
	}
}

func TestRenderBodySplitSurrogate(t *testing.T) {
	// "a😀b": units a, hi, lo, b. A spoiler ending inside the pair must not
	// swallow the following "b".
	got := RenderBody("a😀b", []core.BodyRange{{Start: 0, Length: 2, Style: core.StyleSpoiler}}, nil)
	if got != "■■■■�b" {
		t.Errorf("RenderBody = %q", got)
	}
	// Garbage ranges never panic.
	for _, r := range []core.BodyRange{
		{Start: -5, Length: 3, Style: core.StyleSpoiler},
		{Start: 2, Length: -10, Style: core.StyleSpoiler},
		{Start: 100, Length: 1, Style: core.StyleMention, MentionACI: "x"},
		{Start: 1, Length: math.MaxInt, Style: core.StyleSpoiler},
	} {
		_ = RenderBody("a😀b￼", []core.BodyRange{r}, nil)
	}
}
