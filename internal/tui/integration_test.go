package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"warte/internal/config"
)

func TestWriteConfigKeysKeepsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	orig := "# my config\ntheme = \"nord\" # comment\nmouse = false\n\n[keys.normal]\n\"<leader>m\" = \"chat.mute\"\n"
	if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeConfigKeys(path, map[string]string{"theme": `"dracula"`, "layout": `"irc"`}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	s := string(data)
	for _, want := range []string{"# my config", `theme = "dracula"`, `layout = "irc"`, "mouse = false", "[keys.normal]", `"<leader>m" = "chat.mute"`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Index(s, "layout") > strings.Index(s, "[keys.normal]") {
		t.Errorf("new key written into a table:\n%s", s)
	}
	cfg, _, err := config.Load(path)
	if err != nil || cfg.Theme != "dracula" || cfg.Layout != "irc" || len(cfg.Keys) == 0 {
		t.Fatalf("reload: %+v %v", cfg.Theme, err)
	}
	// A fresh file is created with mode 0600.
	p2 := filepath.Join(t.TempDir(), "sub", "config.toml")
	if err := writeConfigKeys(p2, map[string]string{"mouse": "true"}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(p2); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("stat %v %v", fi, err)
	}
}

func TestSetMarksConfigDirty(t *testing.T) {
	h := newHarness(t, 120, 40, withConfig(func(c *config.Config) {}))
	path := filepath.Join(t.TempDir(), "c.toml")
	h.a.opts.ConfigPath = path
	h.keys(":set layout=irc<enter>")
	h.keys(":theme nord<enter>")
	h.keys(":w<enter>")
	cfg, _, err := config.Load(path)
	if err != nil || cfg.Layout != "irc" || cfg.Theme != "nord" {
		t.Fatalf("saved %q %q %v", cfg.Layout, cfg.Theme, err)
	}
}
