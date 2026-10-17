package keymap

import (
	"reflect"
	"testing"
)

func TestBindingsSortedByGroup(t *testing.T) {
	e := New()
	bs := e.Bindings(Normal, Conversation)
	if len(bs) == 0 {
		t.Fatal("no bindings")
	}
	for i := 1; i < len(bs); i++ {
		if groupRank(bs[i-1].Group) > groupRank(bs[i].Group) {
			t.Fatalf("bindings not grouped: %s (%s) before %s (%s)", bs[i-1].Action, bs[i-1].Group, bs[i].Action, bs[i].Group)
		}
	}
	// Leader is resolved and context bindings are merged over global ones.
	var sawLeader, sawFilter, sawSearch bool
	for _, b := range bs {
		for _, k := range b.Keys {
			if k == LeaderToken {
				t.Fatalf("unresolved leader in %+v", b)
			}
		}
		if b.Action == ActFinderChats && b.Display() == "space space" && b.Notation() == "<Space><Space>" {
			sawLeader = true
		}
		if b.Action == ActChatFilter {
			sawFilter = true
		}
		if b.Action == ActSearchForward && b.Display() == "/" && b.Context == Conversation {
			sawSearch = true
		}
		if b.Help == "" || b.Group == "" || b.Mode != Normal {
			t.Errorf("incomplete binding %+v", b)
		}
	}
	if !sawLeader || sawFilter || !sawSearch {
		t.Fatalf("leader=%v filter=%v search=%v", sawLeader, sawFilter, sawSearch)
	}
	// Returned keys are copies.
	bs[0].Keys[0] = "mutated"
	if e.Bindings(Normal, Conversation)[0].Keys[0] == "mutated" {
		t.Fatal("Bindings leaked internal slice")
	}
}

func TestHelpGroups(t *testing.T) {
	e := New()
	groups := e.Help(Normal, ChatList)
	if len(groups) == 0 {
		t.Fatal("no groups")
	}
	seen := map[Group]bool{}
	var down *HelpEntry
	for gi := range groups {
		g := &groups[gi]
		if seen[g.Group] {
			t.Fatalf("group %q appears twice", g.Group)
		}
		seen[g.Group] = true
		for ei := range g.Entries {
			if g.Entries[ei].Action == ActCursorDown {
				down = &g.Entries[ei]
			}
		}
	}
	if down == nil {
		t.Fatal("no cursor.down entry")
	}
	if !reflect.DeepEqual(down.Keys, []string{"j", "down"}) || down.KeyLabel() != "j/down" || down.String() != "j/down Move down" {
		t.Fatalf("cursor.down entry = %+v", *down)
	}
	// Same action with different help text stays separate (story "r").
	story := e.Help(Normal, Overlay(OverlayStory))
	found := false
	for _, g := range story {
		for _, en := range g.Entries {
			if en.Action == ActMessageReply && en.Help == "Reply to story" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("story reply help override missing")
	}
}

func TestShortHelp(t *testing.T) {
	e := New()
	tests := []struct {
		mode  Mode
		ctx   Context
		first string
		want  []string
	}{
		{Normal, ChatList, "j/k move", []string{"j/k move", "enter open", "/ filter", "space space find chat", "? help"}},
		{Normal, Conversation, "j/k move", nil},
		{Normal, Global, "j/k move", nil},
		{Normal, Overlay(OverlayHelp), "j/k move", []string{"j/k move", "enter select", "q close"}},
		{Normal, Overlay(OverlayConfirm), "y yes", nil},
		{Insert, Conversation, "enter send", nil},
		{Visual, Conversation, "j/k extend", nil},
		{Command, Global, "enter run", nil},
		{Search, Conversation, "enter search", nil},
	}
	for _, tt := range tests {
		got := e.ShortHelp(tt.mode, tt.ctx)
		if len(got) == 0 || len(got) > ShortHelpLimit {
			t.Errorf("%s/%s: %d entries", tt.mode, tt.ctx, len(got))
			continue
		}
		if got[0].String() != tt.first {
			t.Errorf("%s/%s first = %q, want %q", tt.mode, tt.ctx, got[0].String(), tt.first)
		}
		if tt.want != nil {
			var s []string
			for _, h := range got {
				s = append(s, h.String())
			}
			if !reflect.DeepEqual(s, tt.want) {
				t.Errorf("%s/%s = %q, want %q", tt.mode, tt.ctx, s, tt.want)
			}
		}
	}

	// Hints follow remaps and skip unbound actions.
	e.ApplyOverrides(Overrides{"normal.chatlist": {"/": "none", "o": "chat.open"}})
	got := e.ShortHelp(Normal, ChatList)
	if got[1].String() != "o open" {
		t.Fatalf("remapped open hint = %q", got[1].String())
	}
	for _, h := range got {
		if h.Action == ActChatFilter {
			t.Fatalf("unbound filter still hinted: %v", got)
		}
	}

	// Custom hint lists.
	e.SetShortHelp(Normal, Sidebar, []ShortHelpItem{{[]Action{ActHelp}, "help"}, {[]Action{"nope"}, "x"}})
	if got := e.ShortHelp(Normal, Sidebar); len(got) != 1 || got[0].String() != "? help" {
		t.Fatalf("custom short help = %v", got)
	}
}

func TestKeysForAndHint(t *testing.T) {
	e := New()
	got := e.KeysFor(ActChatMute)
	if !reflect.DeepEqual(got, []string{"zm", "space m"}) {
		t.Fatalf("KeysFor(chat.mute) = %q", got)
	}
	if got := e.KeysFor(ActModeNormal); len(got) == 0 || got[0] != "esc" {
		t.Fatalf("KeysFor(mode.normal) = %q", got)
	}
	if got := e.KeysFor(ActMessageReply); !reflect.DeepEqual(got, []string{"r"}) {
		t.Fatalf("KeysFor(message.reply) = %q", got)
	}
	if got := e.KeysForIn(ActMessageReply, Normal, ChatList); len(got) != 0 {
		t.Fatalf("reply should not be bound in chat list: %q", got)
	}
	if h := e.Hint(ActMessageReply, Normal, Conversation); h != "r reply" {
		t.Fatalf("Hint = %q", h)
	}
	if h := e.Hint(ActMessageReply, Normal, ChatList); h != "" {
		t.Fatalf("Hint unbound = %q", h)
	}
	if h := e.Hint(ActFocusLeft, Normal, Conversation); h != "H focus left pane" {
		t.Fatalf("Hint = %q", h)
	}
}
