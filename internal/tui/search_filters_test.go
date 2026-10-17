package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestSearchCommandFilters(t *testing.T) {
	h := newHarness(t, 120, 40)
	if !h.a.conv.open {
		t.Fatal("no chat open")
	}
	var got []string
	h.a.RegisterCommand(Command{Name: "find", Run: func(_ *App, args []string) tea.Cmd {
		got = append([]string(nil), args...)
		return nil
	}})

	h.keys(":search from:bob has:image<enter>")
	if strings.Join(got, " ") != "from:bob has:image in:here" {
		t.Fatalf("filters in this chat: %q", got)
	}
	got = nil
	h.keys(":search in:book lunch<enter>")
	if strings.Join(got, " ") != "in:book lunch" {
		t.Fatalf("filters across chats: %q", got)
	}
	got = nil
	h.keys(":search before:2026-01 http://x<enter>")
	if strings.Join(got, " ") != "before:2026-01 http://x in:here" {
		t.Fatalf("date filter: %q", got)
	}
	got = nil
	h.keys(":search http://x lunch<enter>")
	if got != nil {
		t.Fatalf("plain text went to :find: %q", got)
	}
	if h.a.search.highlight != "http://x lunch" {
		t.Fatalf("plain search highlight %q", h.a.search.highlight)
	}
	if c := completeSearchFilters(h.a, []string{"has:"}); len(c) != 5 {
		t.Fatalf("has completions %v", c)
	}
}

func TestSearchCommandFiltersWithoutFinder(t *testing.T) {
	h := newHarness(t, 120, 40)
	delete(h.a.commands, "find")
	h.keys(":search from:bob<enter>")
	if h.a.search.highlight != "" {
		t.Fatal("filters searched as text")
	}
}
