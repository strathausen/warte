package keymap

import (
	"strings"
	"testing"
)

func TestDefaultsHaveNoConflicts(t *testing.T) {
	e := New()
	for _, c := range e.Conflicts() {
		t.Errorf("default keymap conflict: %s", c)
	}
}

func TestDefaultSpecsAreValid(t *testing.T) {
	type slot struct {
		mode Mode
		ctx  Context
		keys string
	}
	seen := map[slot]Action{}
	for _, s := range DefaultSpecs {
		keys, err := ParseKeys(s.Keys)
		if err != nil {
			t.Errorf("ParseKeys(%q): %v", s.Keys, err)
			continue
		}
		info, ok := LookupAction(s.Action)
		if !ok {
			t.Errorf("default %q uses unregistered action %q", s.Keys, s.Action)
			continue
		}
		if info.Help == "" {
			t.Errorf("action %q has no help text", s.Action)
		}
		if len(s.Modes) == 0 {
			t.Errorf("default %q has no modes", s.Keys)
		}
		// True conflicts: the same keys bound twice in one layer.
		for _, m := range s.Modes {
			k := slot{m, s.Context, seqKey(keys)}
			if prev, dup := seen[k]; dup {
				t.Errorf("%s/%s %q bound twice (%s and %s)", m, s.Context, s.Keys, prev, s.Action)
			}
			seen[k] = s.Action
		}
	}
}

func TestRegistry(t *testing.T) {
	bound := map[Action]bool{}
	for _, s := range DefaultSpecs {
		bound[s.Action] = true
	}
	names := map[Action]bool{}
	for _, info := range Actions() {
		if names[info.Name] {
			t.Errorf("duplicate action %q", info.Name)
		}
		names[info.Name] = true
		if info.Help == "" {
			t.Errorf("action %q has no help", info.Name)
		}
		if _, ok := groupIndex[info.Group]; !ok {
			t.Errorf("action %q has unknown group %q", info.Name, info.Group)
		}
		if !strings.Contains(string(info.Name), ".") {
			t.Errorf("action %q should be namespaced like \"cursor.down\"", info.Name)
		}
		if !bound[info.Name] {
			t.Errorf("action %q has no default binding", info.Name)
		}
		if !info.Name.Valid() || info.Name.Help() != info.Help {
			t.Errorf("Valid/Help mismatch for %q", info.Name)
		}
	}
	if len(names) != len(registry) {
		t.Fatalf("Actions() returned %d, registry has %d", len(names), len(registry))
	}
	if Action("nope").Valid() || Action("nope").Help() != "nope" {
		t.Error("unknown action handling")
	}
	if _, ok := LookupAction("nope"); ok {
		t.Error("LookupAction(nope) should fail")
	}
	// Actions() is sorted by group.
	all := Actions()
	for i := 1; i < len(all); i++ {
		if groupIndex[all[i-1].Group] > groupIndex[all[i].Group] {
			t.Fatalf("Actions not sorted by group at %d", i)
		}
	}
}

// TestSpecKeymap checks the keymap documented in SPEC.md §5.
func TestSpecKeymap(t *testing.T) {
	type c struct {
		mode Mode
		ctx  Context
		keys string
		want Action
	}
	tests := []c{
		{Normal, Conversation, "j", ActCursorDown},
		{Normal, Conversation, "k", ActCursorUp},
		{Normal, ChatList, "<Up>", ActCursorUp},
		{Normal, ChatList, "gg", ActCursorTop},
		{Normal, ChatList, "G", ActCursorBottom},
		{Normal, Conversation, "<C-d>", ActHalfPageDown},
		{Normal, Conversation, "<C-u>", ActHalfPageUp},
		{Normal, Conversation, "<C-f>", ActPageDown},
		{Normal, Conversation, "<C-b>", ActPageUp},
		{Normal, Conversation, "H", ActFocusLeft},
		{Normal, ChatList, "L", ActFocusRight},
		{Normal, Conversation, "<C-w>h", ActFocusLeft},
		{Normal, ChatList, "<C-w>l", ActFocusRight},
		{Normal, ChatList, "<Tab>", ActFocusNext},
		{Normal, Conversation, "<S-Tab>", ActFocusPrev},
		{Normal, ChatList, "<leader><leader>", ActFinderChats},
		{Normal, ChatList, "]u", ActChatNextUnread},
		{Normal, ChatList, "[u", ActChatPrevUnread},
		{Normal, ChatList, "gt", ActFolderNext},
		{Normal, ChatList, "gT", ActFolderPrev},
		{Normal, ChatList, "<CR>", ActChatOpen},
		{Normal, ChatList, "za", ActChatArchive},
		{Normal, ChatList, "zm", ActChatMute},
		{Normal, ChatList, "zp", ActChatPin},
		{Normal, ChatList, "zu", ActChatMarkUnread},
		{Normal, ChatList, "/", ActChatFilter},
		{Normal, ChatList, "dd", ActChatDelete},
		{Normal, ChatList, "A", ActChatShowArchived},
		{Normal, Conversation, "r", ActMessageReply},
		{Normal, Conversation, "R", ActMessageReact},
		{Normal, Conversation, "+", ActMessageReact},
		{Normal, Conversation, "e", ActMessageEdit},
		{Normal, Conversation, "dd", ActMessageDelete},
		{Normal, Conversation, "dD", ActMessageDeleteEveryone},
		{Normal, Conversation, "yy", ActMessageCopy},
		{Normal, Conversation, "yl", ActMessageCopyLink},
		{Normal, Conversation, "f", ActMessageForward},
		{Normal, Conversation, "o", ActMessageOpenAttachment},
		{Normal, Conversation, "gx", ActMessageOpenURL},
		{Normal, Conversation, "p", ActMessagePin},
		{Normal, Conversation, "i", ActModeInsert},
		{Normal, Conversation, "a", ActModeAppend},
		{Normal, Conversation, "I", ActModeInsertStart},
		{Normal, Conversation, "v", ActModeVisual},
		{Normal, Conversation, "V", ActModeVisualLine},
		{Normal, Conversation, "<CR>", ActMessageOpen},
		{Normal, Conversation, "gq", ActMessageGotoQuote},
		{Normal, Conversation, "K", ActMessageInfo},
		{Normal, Conversation, "s", ActMessageRevealSpoiler},
		{Normal, Conversation, "zz", ActCenter},
		{Normal, Conversation, "n", ActSearchNext},
		{Normal, Conversation, "N", ActSearchPrev},
		{Normal, Conversation, "<C-o>", ActJumpBack},
		{Normal, Conversation, "<C-i>", ActJumpForward},
		{Normal, Conversation, "gm", ActMediaGallery},
		{Normal, Conversation, "gi", ActChatInfo},
		{Normal, Conversation, "/", ActSearchForward},
		{Insert, Conversation, "<Esc>", ActModeNormal},
		{Insert, Conversation, "<CR>", ActComposerSend},
		{Insert, Conversation, "<A-CR>", ActComposerNewline},
		{Insert, Conversation, "<S-CR>", ActComposerNewline},
		{Insert, Conversation, "<C-e>", ActComposerEditor},
		{Insert, Conversation, "<C-a>", ActComposerAttach},
		{Insert, Conversation, "<C-v>", ActComposerPaste},
		{Insert, Conversation, "<C-r>", ActComposerCancelReply},
		{Insert, Conversation, "<Tab>", ActComposerComplete},
		{Insert, Conversation, "<C-c>", ActComposerCancel},
		{Visual, Conversation, "j", ActCursorDown},
		{Visual, Conversation, "k", ActCursorUp},
		{Visual, Conversation, "y", ActVisualYank},
		{Visual, Conversation, "d", ActVisualDelete},
		{Visual, Conversation, "f", ActVisualForward},
		{Visual, Conversation, "<Esc>", ActModeNormal},
		{VisualLine, Conversation, "<Esc>", ActModeNormal},
		{Command, Global, "<CR>", ActCmdExecute},
		{Command, Global, "<Esc>", ActCmdCancel},
		{Command, Global, "<Tab>", ActCmdComplete},
		{Command, Global, "<Up>", ActCmdHistoryPrev},
		{Command, Global, "<Down>", ActCmdHistoryNext},
		{Search, Global, "<CR>", ActSearchSubmit},
		{Search, Global, "<Esc>", ActSearchCancel},
		{Normal, ChatList, ":", ActModeCommand},
		{Normal, ChatList, "?", ActHelp},
		{Normal, ChatList, "<leader>/", ActSearchGlobal},
		{Normal, ChatList, "<leader>s", ActStories},
		{Normal, ChatList, "<leader>c", ActCalls},
		{Normal, ChatList, "<leader>n", ActChatNew},
		{Normal, ChatList, "<leader>g", ActGroupNew},
		{Normal, ChatList, "<leader>,", ActSettings},
		{Normal, ChatList, "<leader>t", ActThemePicker},
		{Normal, ChatList, "<leader>i", ActSidebarToggle},
		{Normal, ChatList, "<leader>m", ActChatMute},
		{Normal, ChatList, "<leader>l", ActLock},
		{Normal, ChatList, "<C-l>", ActRedraw},
		{Insert, Conversation, "<C-l>", ActRedraw},
		{Normal, ChatList, "q", ActBack},
		{Normal, Overlay("picker"), "q", ActBack},
		{Normal, ChatList, "ZZ", ActQuit},
		{Normal, ChatList, "<C-c>", ActQuitHint},
		{Normal, Conversation, ".", ActRepeat},
	}
	for _, tt := range tests {
		e := New()
		e.SetMode(tt.mode)
		keys, err := ParseKeys(tt.keys)
		if err != nil {
			t.Fatal(err)
		}
		for i, k := range keys {
			if k == LeaderToken {
				keys[i] = e.Leader()
			}
		}
		rs := feed(e, tt.ctx, keys...)
		r := rs[len(rs)-1]
		if r.Action != tt.want || len(rs) != len(keys) {
			t.Errorf("%s/%s %s: got %q (results %d), want %q", tt.mode, tt.ctx, tt.keys, r.Action, len(rs), tt.want)
		}
	}
}

func TestShortHelpDefaultsAreBound(t *testing.T) {
	e := New()
	for k, items := range defaultShortHelp {
		if len(items) > ShortHelpLimit {
			t.Errorf("%s/%s: %d short help items, limit %d", k.mode, k.ctx, len(items), ShortHelpLimit)
		}
		for _, it := range items {
			for _, a := range it.Actions {
				if len(e.KeysForIn(a, k.mode, k.ctx)) == 0 {
					t.Errorf("%s/%s short help %q: action %q unbound", k.mode, k.ctx, it.Label, a)
				}
			}
		}
		if got := e.ShortHelp(k.mode, k.ctx); len(got) != len(items) {
			t.Errorf("%s/%s: ShortHelp returned %d of %d items", k.mode, k.ctx, len(got), len(items))
		}
	}
}

func TestConflictsDetected(t *testing.T) {
	e := New(WithoutDefaults())
	mustBind(t, e, Normal, Global, "g", ActChatInfo)
	mustBind(t, e, Normal, Global, "gg", ActCursorTop)
	mustBind(t, e, Normal, Global, "<leader>,", ActSettings)
	mustBind(t, e, Normal, Global, "<leader><leader>", ActFinderChats)
	mustBind(t, e, Normal, ChatList, "x", ActHelp)
	mustBind(t, e, Visual, Global, "7", ActHelp)
	mustBind(t, e, Insert, Global, "7", ActHelp) // digits are fine in insert
	if err := e.SetLeader(","); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, c := range e.Conflicts() {
		got[c.String()] = true
	}
	checks := []struct{ mode, keys, sub string }{
		{"normal", `"g"`, "prefix of longer bindings"},
		{"normal", `",,"`, "bound to both app.settings and finder.chats"},
		{"visual", `"7"`, "count digit"},
	}
	for _, c := range checks {
		found := false
		for s := range got {
			if strings.HasPrefix(s, c.mode+"/") && strings.Contains(s, c.keys) && strings.Contains(s, c.sub) {
				found = true
			}
		}
		if !found {
			t.Errorf("missing conflict %v in %v", c, got)
		}
	}
	if len(got) != len(checks) {
		t.Errorf("got %d conflicts, want %d: %v", len(got), len(checks), got)
	}
	// Both are user bindings, so the later one wins the leader collision.
	if r := last(e, Global, ",", ","); r.Action != ActFinderChats {
		t.Fatalf(",, = %+v", r)
	}
}
