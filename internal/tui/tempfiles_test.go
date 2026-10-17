package tui

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestEditorTempFileTrackedAndRemoved(t *testing.T) {
	h := newHarness(t, 100, 30)
	h.a.cfg.Editor = "true"
	h.keys("i")
	h.a.comp.ta.SetValue("secret draft")
	cmd := h.a.openEditor()
	if cmd == nil {
		t.Fatal("no editor command")
	}
	matches, _ := filepath.Glob(filepath.Join(h.a.opts.TempDir, tempEditPrefix+strconv.Itoa(os.Getpid())+"-*.md"))
	if len(matches) != 1 {
		t.Fatalf("draft files: %v", matches)
	}
	fi, err := os.Stat(matches[0])
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("draft file mode: %v %v", fi, err)
	}
	if !h.a.tempFiles[matches[0]] {
		t.Fatal("draft file not tracked for cleanup")
	}
	// A crash path: the editor never returns, the App is closed.
	h.a.Close()
	if _, err := os.Stat(matches[0]); !os.IsNotExist(err) {
		t.Fatal("draft file survived Close")
	}
}

func TestSweepStaleTemp(t *testing.T) {
	dir := t.TempDir()
	dead := 0
	for pid := 4_000_000; pid > 3_000_000; pid -= 7919 {
		if !processAlive(pid) {
			dead = pid
			break
		}
	}
	if dead == 0 {
		t.Skip("no dead pid found")
	}
	alive := os.Getppid()
	write := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	staleEdit := write(tempEditPrefix + strconv.Itoa(dead) + "-123.md")
	staleOpen := write(tempOpenPrefix + strconv.Itoa(dead) + "-456.jpg")
	liveEdit := write(tempEditPrefix + strconv.Itoa(alive) + "-789.md")
	mine := write(tempEditPrefix + strconv.Itoa(os.Getpid()) + "-1.md")
	other := write("warte-notes.md")
	if n := sweepStaleTemp(dir); n != 2 {
		t.Errorf("removed %d, want 2", n)
	}
	for _, p := range []string{staleEdit, staleOpen} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s not removed", filepath.Base(p))
		}
	}
	for _, p := range []string{liveEdit, mine, other} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s removed", filepath.Base(p))
		}
	}
	if !strings.HasPrefix(tempPattern(tempEditPrefix, ".md"), tempEditPrefix+strconv.Itoa(os.Getpid())+"-") {
		t.Error("pattern")
	}
}
