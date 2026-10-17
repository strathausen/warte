package config

import (
	"reflect"
	"testing"
)

func TestKeyMapParsing(t *testing.T) {
	cfg, ws, err := Parse([]byte(`
leader = ","

[keys.normal]
"<leader>m" = "chat.mute"
"q" = "none"
"Q" = "NONE"
"x" = ""
"<C-x>" = ":mute 8h"
"zz" = "Bad Action!"
"<oops" = "chat.mute"

[keys.Insert]
"<C-s>" = "composer.send"

[keys.visual]
"y" = "message.copy"

[keys.bogus]
"a" = "b"
`))
	if err != nil {
		t.Fatal(err)
	}
	wantWarn := []struct{ key, substr string }{
		{`keys.normal."zz"`, "invalid action"},
		{`keys.normal."<oops"`, "unterminated"},
		{"keys.bogus", "unknown mode"},
	}
	for _, w := range wantWarn {
		if !hasWarning(ws, w.key, w.substr) {
			t.Errorf("missing warning %s (%s); got %v", w.key, w.substr, ws)
		}
	}
	if len(ws) != len(wantWarn) {
		t.Errorf("got %d warnings, want %d: %v", len(ws), len(wantWarn), ws)
	}

	want := []KeyBinding{
		{Mode: ModeNormal, Keys: "<C-x>", Action: ":mute 8h"},
		{Mode: ModeNormal, Keys: "<leader>m", Action: "chat.mute"},
		{Mode: ModeNormal, Keys: "Q", Unbind: true},
		{Mode: ModeNormal, Keys: "q", Unbind: true},
		{Mode: ModeNormal, Keys: "x", Unbind: true},
		{Mode: ModeInsert, Keys: "<C-s>", Action: "composer.send"},
		{Mode: ModeVisual, Keys: "y", Action: "message.copy"},
	}
	if got := cfg.KeyBindings(); !reflect.DeepEqual(got, want) {
		t.Errorf("KeyBindings:\n got %+v\nwant %+v", got, want)
	}
	if _, ok := cfg.Keys["bogus"]; ok {
		t.Error("unknown mode kept")
	}
	if _, ok := cfg.Keys["Insert"]; ok {
		t.Error("mode name not normalized")
	}
	if cfg.Keys[ModeNormal]["q"] != "" {
		t.Error(`"none" not normalized to ""`)
	}
}

func TestKeyModeMerge(t *testing.T) {
	cfg, ws, err := Parse([]byte(`
[keys.normal]
"a" = "one"
[keys.NORMAL]
"b" = "two"
`))
	if err != nil || len(ws) != 0 {
		t.Fatalf("err=%v warnings=%v", err, ws)
	}
	if got := cfg.Keys[ModeNormal]; len(got) != 2 || got["a"] != "one" || got["b"] != "two" {
		t.Errorf("merged normal map = %v", got)
	}
}

func TestCheckKeySequence(t *testing.T) {
	tests := []struct {
		seq string
		ok  bool
	}{
		{"gg", true},
		{"<leader>m", true},
		{"<C-w>h", true},
		{"<space><space>", true},
		{">", true},
		{"]u", true},
		{"", false},
		{"<C-w", false},
		{"<<C-w>>", false},
		{"<>", false},
	}
	for _, tt := range tests {
		if err := checkKeySequence(tt.seq); (err == nil) != tt.ok {
			t.Errorf("checkKeySequence(%q) err = %v, want ok=%v", tt.seq, err, tt.ok)
		}
	}
}

func TestExpandLeader(t *testing.T) {
	tests := []struct {
		leader, seq, want string
	}{
		{" ", "<leader>m", "<space>m"},
		{" ", "<leader><leader>", "<space><space>"},
		{",", "<Leader>/", ",/"},
		{"\\", "gg", "gg"},
		{",", "a<leader>b<LEADER>", "a,b,"},
	}
	for _, tt := range tests {
		c := Config{Leader: tt.leader}
		if got := c.ExpandLeader(tt.seq); got != tt.want {
			t.Errorf("leader %q ExpandLeader(%q) = %q, want %q", tt.leader, tt.seq, got, tt.want)
		}
	}
}
