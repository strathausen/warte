package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestExpandLeaderNonASCII(t *testing.T) {
	c := Default()
	c.Leader = ","
	// U+0130 lowercases to a longer UTF-8 sequence; indices must still be
	// computed on the original string.
	got := c.ExpandLeader("İİ<Leader>x<leader>")
	if want := "İİ,x,"; got != want {
		t.Errorf("ExpandLeader = %q, want %q", got, want)
	}
	if got := c.ExpandLeader("ẞ<LEADER>"); got != "ẞ," {
		t.Errorf("ExpandLeader = %q", got)
	}
}

func TestWarningStringStripsControl(t *testing.T) {
	cfg, ws, err := Parse([]byte("\"evil\\u001b]0;pwn\\u0007\" = 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	_ = cfg
	if len(ws) != 1 {
		t.Fatalf("warnings = %v", ws)
	}
	if s := ws[0].String(); strings.ContainsAny(s, "\x1b\x07") {
		t.Errorf("warning contains control chars: %q", s)
	}
	if s := (Warning{Message: "a\x1b[2Jb"}).String(); s != "a[2Jb" {
		t.Errorf("String = %q", s)
	}
}

func TestDeviceNameControlChars(t *testing.T) {
	cfg, ws, err := Parse([]byte("device_name = \"my\\u001b[31mbox\\n\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DeviceName != "my[31mbox" {
		t.Errorf("DeviceName = %q", cfg.DeviceName)
	}
	if len(ws) != 1 || ws[0].Key != "device_name" {
		t.Errorf("warnings = %v", ws)
	}
}

func TestNerdFontsInteger(t *testing.T) {
	cfg, ws, err := Parse([]byte("nerd_fonts = 1\n"))
	if err != nil || len(ws) != 0 || cfg.NerdFonts != NerdFontsOn {
		t.Errorf("nerd_fonts=1: %v %v %v", cfg.NerdFonts, ws, err)
	}
	cfg, ws, _ = Parse([]byte("nerd_fonts = 7\n"))
	if len(ws) != 1 || cfg.NerdFonts != NerdFontsAuto {
		t.Errorf("nerd_fonts=7: %v %v", cfg.NerdFonts, ws)
	}
}

func TestConfigPathTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(EnvConfig, "~/dots/warte.toml")
	p, err := ConfigPath()
	if err != nil || p != filepath.Join(home, "dots", "warte.toml") {
		t.Errorf("ConfigPath = %q, %v", p, err)
	}
	t.Setenv(EnvConfig, "/abs/x.toml")
	if p, _ := ConfigPath(); p != "/abs/x.toml" {
		t.Errorf("ConfigPath = %q", p)
	}
}
