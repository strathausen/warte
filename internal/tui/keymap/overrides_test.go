package keymap

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseSection(t *testing.T) {
	tests := []struct {
		in      string
		modes   []Mode
		ctx     Context
		wantErr bool
	}{
		{in: "normal", modes: []Mode{Normal}, ctx: Global},
		{in: "insert", modes: []Mode{Insert}, ctx: Global},
		{in: "visual", modes: []Mode{Visual, VisualLine}, ctx: Global},
		{in: "visual_line", modes: []Mode{VisualLine}, ctx: Global},
		{in: "command", modes: []Mode{Command}, ctx: Global},
		{in: "search", modes: []Mode{Search}, ctx: Global},
		{in: "normal.chatlist", modes: []Mode{Normal}, ctx: ChatList},
		{in: "normal.conversation", modes: []Mode{Normal}, ctx: Conversation},
		{in: "normal.sidebar", modes: []Mode{Normal}, ctx: Sidebar},
		{in: "normal.overlay", modes: []Mode{Normal}, ctx: AnyOverlay},
		{in: "normal.overlay.image", modes: []Mode{Normal}, ctx: Overlay("image")},
		{in: "Normal.Global", modes: []Mode{Normal}, ctx: Global},
		{in: "bogus", wantErr: true},
		{in: "normal.bogus", wantErr: true},
		{in: "normal.overlay.", wantErr: true},
	}
	for _, tt := range tests {
		modes, ctx, err := ParseSection(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseSection(%q) err = %v", tt.in, err)
			continue
		}
		if !tt.wantErr && (!reflect.DeepEqual(modes, tt.modes) || ctx != tt.ctx) {
			t.Errorf("ParseSection(%q) = %v %q, want %v %q", tt.in, modes, ctx, tt.modes, tt.ctx)
		}
	}
}

func TestApplyOverrides(t *testing.T) {
	e, _ := newTestEngine(t)
	warns := e.ApplyOverrides(Overrides{
		"normal": {
			"<leader>x": "chat.mute",
			"<C-x>":     "page.half_down",
			"dd":        "none",
			"r":         "chat.archive",
			"Q":         "app.quit",
		},
		"normal.chatlist": {
			"j": "none",
		},
		"normal.conversation": {
			"x": "message.delete",
		},
		"visual": {
			"x": "visual.delete",
		},
		"insert": {
			"<C-s>": "composer.send",
			"<CR>":  "composer.newline",
		},
	})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	tests := []struct {
		mode Mode
		ctx  Context
		keys []string
		want Action
	}{
		{Normal, ChatList, []string{"space", "x"}, ActChatMute},
		{Normal, Conversation, []string{"ctrl+x"}, ActHalfPageDown},
		{Normal, ChatList, []string{"Q"}, ActQuit},
		// "dd" = "none" in the mode section removes it in every context.
		{Normal, ChatList, []string{"d", "d"}, ""},
		{Normal, Conversation, []string{"d", "d"}, ""},
		// dD still works.
		{Normal, Conversation, []string{"d", "D"}, ActMessageDeleteEveryone},
		// A mode-wide remap replaces the context binding too.
		{Normal, Conversation, []string{"r"}, ActChatArchive},
		// Context unbind only affects that context.
		{Normal, ChatList, []string{"j"}, ""},
		{Normal, Conversation, []string{"j"}, ActCursorDown},
		{Normal, ChatList, []string{"down"}, ActCursorDown},
		{Normal, Conversation, []string{"x"}, ActMessageDelete},
		{Normal, ChatList, []string{"x"}, ""},
		{Visual, Conversation, []string{"x"}, ActVisualDelete},
		{VisualLine, Conversation, []string{"x"}, ActVisualDelete},
		{Insert, Conversation, []string{"ctrl+s"}, ActComposerSend},
		{Insert, Conversation, []string{"enter"}, ActComposerNewline},
	}
	for _, tt := range tests {
		e.SetMode(tt.mode)
		r := last(e, tt.ctx, tt.keys...)
		if r.Action != tt.want {
			t.Errorf("%s/%s %q: got %q, want %q", tt.mode, tt.ctx, tt.keys, r.Action, tt.want)
		}
	}
	if got := e.KeysForIn(ActChatMute, Normal, ChatList); len(got) == 0 || got[0] != "space x" {
		t.Errorf("user binding should be the primary hint, got %q", got)
	}
}

func TestApplyOverridesWarnings(t *testing.T) {
	e, _ := newTestEngine(t)
	warns := e.ApplyOverrides(Overrides{
		"bogus":  {"x": "app.help"},
		"normal": {"<Foo>": "app.help", "x": "chat.mutee", "y": "totally.unknown", "g": "app.help", "zzz": "app.help", "5x": "app.help"},
	})
	want := map[string]string{
		"bogus|":       "unknown mode",
		"normal|<Foo>": "unknown key",
		"normal|x":     `unknown action (did you mean "chat.mute"?)`,
		"normal|y":     "unknown action",
		"normal|g":     "prefix of longer bindings",
		"normal|zzz":   `extends "zz"`,
		"normal|5x":    "count digit",
	}
	got := map[string]string{}
	for _, w := range warns {
		got[w.Section+"|"+w.Keys] = w.Error()
	}
	for k, sub := range want {
		msg, ok := got[k]
		if !ok {
			t.Errorf("missing warning for %s; got %v", k, warns)
			continue
		}
		if !strings.Contains(msg, sub) {
			t.Errorf("warning %s = %q, want it to contain %q", k, msg, sub)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d warnings, want %d: %v", len(got), len(want), warns)
	}
	if strings.Contains(got["normal|y"], "did you mean") {
		t.Errorf("no suggestion expected for a far-off name: %q", got["normal|y"])
	}
	// Bad entries are skipped; "g" was still bound (with a warning).
	if r := e.Feed("g", Conversation); !r.Ambiguous {
		t.Fatalf("g should now be ambiguous: %+v", r)
	}
}

func TestBindUnbindErrors(t *testing.T) {
	e := New()
	if err := e.Bind(Normal, Global, "<Foo>", ActHelp); err == nil {
		t.Error("bad keys should fail")
	}
	if err := e.Bind(Normal, Global, "x", "nope"); err == nil {
		t.Error("unknown action should fail")
	}
	if err := e.Unbind(Normal, Global, ""); err == nil {
		t.Error("empty keys should fail")
	}
	if err := e.Unbind(Normal, Conversation, "r"); err != nil {
		t.Fatal(err)
	}
	if r := e.Feed("r", Conversation); !r.Unmatched {
		t.Fatalf("r should be unbound in conversation: %+v", r)
	}
	// Re-binding in the context after an unbind works.
	if err := e.Bind(Normal, Conversation, "r", ActMessageReply); err != nil {
		t.Fatal(err)
	}
	if r := e.Feed("r", Conversation); r.Action != ActMessageReply {
		t.Fatalf("got %+v", r)
	}
	// A context unbind hides a global binding.
	if err := e.Unbind(Normal, Sidebar, "q"); err != nil {
		t.Fatal(err)
	}
	if r := e.Feed("q", Sidebar); !r.Unmatched {
		t.Fatalf("q should be hidden in sidebar: %+v", r)
	}
	if r := e.Feed("q", ChatList); r.Action != ActBack {
		t.Fatalf("q elsewhere: %+v", r)
	}
}

func TestLeaderOverrideAfterRemap(t *testing.T) {
	e := New()
	if w := e.ApplyOverrides(Overrides{"normal": {"<leader>m": "app.help"}}); len(w) != 0 {
		t.Fatal(w)
	}
	if err := e.SetLeader(","); err != nil {
		t.Fatal(err)
	}
	if r := last(e, ChatList, ",", "m"); r.Action != ActHelp {
		t.Fatalf(",m: %+v", r)
	}
}

func TestEditDistance(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"", "", 0}, {"a", "", 1}, {"", "abc", 3}, {"kitten", "sitting", 3}, {"chat.mute", "chat.mutee", 1},
	}
	for _, tt := range tests {
		if got := editDistance(tt.a, tt.b); got != tt.want {
			t.Errorf("editDistance(%q,%q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}
