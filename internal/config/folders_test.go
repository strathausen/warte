package config

import (
	"testing"
	"time"

	"warte/internal/core"
)

func TestFolderValidation(t *testing.T) {
	cfg, ws, err := Parse([]byte(`
[[folders]]
name = " Work "
include_groups = true

[[folders]]
name = ""
include_direct = true

[[folders]]
name = "work"
include_direct = true

[[folders]]
name = "Empty"
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Folders) != 2 || cfg.Folders[0].Name != "Work" || cfg.Folders[1].Name != "Empty" {
		t.Fatalf("folders = %+v", cfg.Folders)
	}
	for _, w := range []struct{ key, substr string }{
		{"folders[1]", "without a name"},
		{"folders[2]", "duplicate"},
		{"folders[3]", "includes no chats"},
	} {
		if !hasWarning(ws, w.key, w.substr) {
			t.Errorf("missing warning %s; got %v", w.key, ws)
		}
	}

	cfg, _, _ = Parse([]byte("[[folders]]\nname = \"\"\n"))
	if cfg.Folders != nil {
		t.Errorf("all-invalid folders should be nil, got %+v", cfg.Folders)
	}
}

func TestFolderMatch(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	direct := core.Chat{ID: "d1", Kind: core.ChatDirect}
	self := core.Chat{ID: "me", Kind: core.ChatNoteToSelf}
	group := core.Chat{ID: "g1", Kind: core.ChatGroup}
	unread := core.Chat{ID: "g2", Kind: core.ChatGroup, Unread: 3}
	marked := core.Chat{ID: "d2", Kind: core.ChatDirect, MarkedUnread: true}
	muted := core.Chat{ID: "g3", Kind: core.ChatGroup, MutedUntil: now.Add(time.Hour)}
	expired := core.Chat{ID: "g4", Kind: core.ChatGroup, MutedUntil: now.Add(-time.Hour)}
	archived := core.Chat{ID: "g5", Kind: core.ChatGroup, Archived: true}

	tests := []struct {
		name   string
		folder FolderConfig
		chat   core.Chat
		want   bool
	}{
		{"direct included", FolderConfig{IncludeDirect: true}, direct, true},
		{"note to self is direct", FolderConfig{IncludeDirect: true}, self, true},
		{"group not in direct folder", FolderConfig{IncludeDirect: true}, group, false},
		{"group included", FolderConfig{IncludeGroups: true}, group, true},
		{"explicit chat", FolderConfig{Chats: []string{"g1"}}, group, true},
		{"explicit other", FolderConfig{Chats: []string{"g1"}}, direct, false},
		{"exclude wins", FolderConfig{IncludeGroups: true, Chats: []string{"g1"}, ExcludeChats: []string{"g1"}}, group, false},
		{"unread only, read", FolderConfig{IncludeGroups: true, UnreadOnly: true}, group, false},
		{"unread only, unread", FolderConfig{IncludeGroups: true, UnreadOnly: true}, unread, true},
		{"unread only, marked", FolderConfig{IncludeDirect: true, UnreadOnly: true}, marked, true},
		{"hide muted", FolderConfig{IncludeGroups: true, HideMuted: true}, muted, false},
		{"muted shown", FolderConfig{IncludeGroups: true}, muted, true},
		{"mute expired", FolderConfig{IncludeGroups: true, HideMuted: true}, expired, true},
		{"archived hidden", FolderConfig{IncludeGroups: true}, archived, false},
		{"archived shown", FolderConfig{IncludeGroups: true, ShowArchived: true}, archived, true},
	}
	for _, tt := range tests {
		if got := tt.folder.Match(tt.chat, now); got != tt.want {
			t.Errorf("%s: Match = %v, want %v", tt.name, got, tt.want)
		}
	}
}
