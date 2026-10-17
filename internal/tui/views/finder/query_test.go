package finder

import (
	"reflect"
	"testing"
	"time"
)

func TestParseQuery(t *testing.T) {
	now := time.Date(2026, 3, 15, 14, 30, 0, 0, time.UTC)
	d := func(y int, m time.Month, day int) time.Time { return time.Date(y, m, day, 0, 0, 0, 0, time.UTC) }
	after := func(t time.Time) time.Time { return t.Add(-time.Nanosecond) }
	tests := []struct {
		in   string
		want Query
	}{
		{"", Query{}},
		{"hello world", Query{Text: "hello world"}},
		{"from:bob lunch", Query{Text: "lunch", From: "bob", Filters: []string{"from:bob"}}},
		{"lunch FROM:Bob", Query{Text: "lunch", From: "Bob", Filters: []string{"from:Bob"}}},
		{`from:"bob okafor" in:"book club" pizza`, Query{Text: "pizza", From: "bob okafor", In: "book club",
			Filters: []string{"from:bob okafor", "in:book club"}}},
		{"has:photo", Query{Has: "image", Filters: []string{"has:photo"}}},
		{"has:voice x", Query{Text: "x", Has: "audio", Filters: []string{"has:voice"}}},
		{"has:nope", Query{Errors: []string{"has:nope"}, Filters: []string{"has:nope"}}},
		{"before:2026-01", Query{Before: d(2026, 1, 1), Filters: []string{"before:2026-01"}}},
		{"after:2025-12", Query{After: after(d(2025, 12, 1)), Filters: []string{"after:2025-12"}}},
		{"after:2025", Query{After: after(d(2025, 1, 1)), Filters: []string{"after:2025"}}},
		{"before:2026-02-03", Query{Before: d(2026, 2, 3), Filters: []string{"before:2026-02-03"}}},
		{"on:2026-02-03", Query{After: after(d(2026, 2, 3)), Before: d(2026, 2, 4), Filters: []string{"on:2026-02-03"}}},
		{"on:today", Query{After: after(d(2026, 3, 15)), Before: d(2026, 3, 16), Filters: []string{"on:today"}}},
		{"after:yesterday", Query{After: after(d(2026, 3, 14)), Filters: []string{"after:yesterday"}}},
		{"after:3d", Query{After: after(d(2026, 3, 12)), Filters: []string{"after:3d"}}},
		{"after:2w", Query{After: after(d(2026, 3, 1)), Filters: []string{"after:2w"}}},
		{"after:1m", Query{After: after(d(2026, 2, 15)), Filters: []string{"after:1m"}}},
		{"before:soon", Query{Errors: []string{"before:soon"}, Filters: []string{"before:soon"}}},
		{"before:2026-13", Query{Errors: []string{"before:2026-13"}, Filters: []string{"before:2026-13"}}},
		// Unknown keys and URLs are text; empty filters are ignored.
		{"https://example.com foo:bar", Query{Text: "https://example.com foo:bar"}},
		{"from: lunch", Query{Text: "lunch"}},
		{"from:bob before:2026-01 after:2025-12 has:image in:book hi",
			Query{Text: "hi", From: "bob", In: "book", Has: "image", Before: d(2026, 1, 1), After: after(d(2025, 12, 1)),
				Filters: []string{"from:bob", "before:2026-01", "after:2025-12", "has:image", "in:book"}}},
		{`"exact phrase" more`, Query{Text: "exact phrase more"}},
	}
	for _, tt := range tests {
		got := ParseQuery(tt.in, now)
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("ParseQuery(%q)\n got %+v\nwant %+v", tt.in, got, tt.want)
		}
	}
}

func TestQueryHelpers(t *testing.T) {
	now := time.Now()
	if !ParseQuery("  ", now).IsEmpty() {
		t.Error("blank query should be empty")
	}
	if ParseQuery("has:image", now).IsEmpty() {
		t.Error("filter-only query is not empty")
	}
	if !ParseQuery("has:image", now).HasFilters() || ParseQuery("x", now).HasFilters() {
		t.Error("HasFilters wrong")
	}
	spans := filterSpans(`hi from:"a b" in:x y:z`)
	want := [][2]int{{3, 13}, {14, 18}}
	if !reflect.DeepEqual(spans, want) {
		t.Errorf("filterSpans = %v, want %v", spans, want)
	}
}

func FuzzParseQuery(f *testing.F) {
	for _, s := range []string{"from:bob x", `in:"a`, "before:2026-01-01", "after:99999y", "on:\x00"} {
		f.Add(s)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f.Fuzz(func(t *testing.T, s string) {
		_ = ParseQuery(s, now)
		_ = filterSpans(s)
	})
}
