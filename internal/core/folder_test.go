package core

import (
	"testing"
	"time"
)

func TestFolderMembersMatch(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	direct := Chat{ID: "u:a", Kind: ChatDirect}
	nts := Chat{ID: "u:me", Kind: ChatNoteToSelf}
	group := Chat{ID: "g:x", Kind: ChatGroup}
	muted := Chat{ID: "u:m", Kind: ChatDirect, MutedUntil: now.Add(time.Hour)}
	for _, tc := range []struct {
		name string
		m    *FolderMembers
		c    Chat
		want bool
	}{
		{"nil matches all", nil, group, true},
		{"all direct", &FolderMembers{AllDirect: true}, direct, true},
		{"all direct has note to self", &FolderMembers{AllDirect: true}, nts, true},
		{"all direct no groups", &FolderMembers{AllDirect: true}, group, false},
		{"all groups", &FolderMembers{AllGroups: true}, group, true},
		{"included", &FolderMembers{Include: []ChatID{"g:x"}}, group, true},
		{"not included", &FolderMembers{Include: []ChatID{"g:x"}}, direct, false},
		{"excluded wins", &FolderMembers{AllGroups: true, Include: []ChatID{"g:x"}, Exclude: []ChatID{"g:x"}}, group, false},
		{"muted hidden", &FolderMembers{AllDirect: true, HideMuted: true}, muted, false},
		{"muted shown", &FolderMembers{AllDirect: true}, muted, true},
	} {
		if got := tc.m.Match(tc.c, now); got != tc.want {
			t.Errorf("%s: got %v", tc.name, got)
		}
	}
}
