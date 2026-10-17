package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"warte/internal/config"
	"warte/internal/termimg"
)

// TestHugeMessagesRenderQuickly: a 256 KiB word without spaces, 20,000
// short lines and a long line of combining marks render within budget and
// keep the frame size.
func TestHugeMessagesRenderQuickly(t *testing.T) {
	big := newBigService(3)
	t.Cleanup(func() { _ = big.Close() })
	big.msgs[0].Body = strings.Repeat("A", 256<<10)
	big.msgs[1].Body = strings.Repeat("line\n", 20000)
	big.msgs[2].Body = strings.Repeat("é̂̃", 50000) + " **" + strings.Repeat("_x_ ", 5000)
	cfg := config.Default()
	none := false
	blocks := termimg.HalfBlocks
	a := New(big, cfg, Options{Now: func() time.Time { return testNow }, Getenv: func(string) string { return "" },
		NerdFonts: &none, Images: &blocks, SkipStart: true, Notifier: nopNotifier{}})
	a.noPump = true
	t.Cleanup(a.Close)
	h := &harness{t: t, a: a, svc: big.Service}
	h.send(tea.WindowSizeMsg{Width: 100, Height: 30})
	start := time.Now()
	h.send(OpenChatForTest(big.chat))
	for _, k := range []string{"k", "k", "k", "g", "g", "G", "K", "esc"} {
		h.keys(k)
		frame := a.View().Content
		lines := strings.Split(frame, "\n")
		if len(lines) != 30 {
			t.Fatalf("after %s: %d lines", k, len(lines))
		}
		for i, l := range lines {
			if w := ansi.StringWidth(l); w != 100 {
				t.Fatalf("after %s: line %d width %d", k, i, w)
			}
		}
	}
	budget := 3 * time.Second
	if raceEnabled {
		budget *= 10
	}
	if d := time.Since(start); d > budget {
		t.Fatalf("huge messages took %v", d)
	}
}

func TestSafeOpenURL(t *testing.T) {
	for u, want := range map[string]bool{
		"https://example.com/x":                      true,
		"http://example.com":                         true,
		"mailto:a@b.c":                               true,
		"tel:+49301234567":                           true,
		"file:///System/Applications/Calculator.app": false,
		"-a Calculator":                              false,
		"x-apple.systempreferences:":                 false,
		"javascript:alert(1)":                        false,
		"https:///nohost":                            false,
		"smb://evil/share":                           false,
	} {
		if _, ok := safeOpenURL(u); ok != want {
			t.Errorf("safeOpenURL(%q) = %v, want %v", u, ok, want)
		}
	}
	if got, _ := safeOpenURL("https://ex\x1b]52;c;x\x07ample.com"); strings.ContainsAny(got, "\x1b\x07") {
		t.Errorf("control characters kept: %q", got)
	}
}

// TestWriteConfigKeepsSymlink: :w on a symlinked config.toml updates the
// target and leaves the link (and no temp file) behind.
func TestWriteConfigKeepsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "dotfiles-config.toml")
	if err := os.WriteFile(target, []byte("# mine\ntheme = \"charm\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "config.toml")
	if err := os.Symlink(target, link); err != nil {
		t.Skip(err)
	}
	if err := writeConfigKeys(link, map[string]string{"theme": `"dracula"`}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("config symlink replaced: %v", err)
	}
	b, _ := os.ReadFile(target)
	if !strings.Contains(string(b), "dracula") || !strings.Contains(string(b), "# mine") {
		t.Fatalf("target not updated: %q", b)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 2 {
		t.Fatalf("leftover files: %v", ents)
	}
}
