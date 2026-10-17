package media

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"warte/internal/core"
	"warte/internal/tui/ui"
)

func TestSaveDialog(t *testing.T) {
	svc := newSvc(t)
	ctx := newCtx(t, svc, &fakeImages{})
	m := findMsg(t, svc, hasAtt(isPhoto))
	it := Item{Message: m, Attachment: firstAtt(m, isPhoto)}
	dir := t.TempDir()
	t.Cleanup(restoreHooks())
	downloadsDir = func() string { return dir }

	d := NewSaveDialog(ctx, it)
	checkView(t, d)
	if !strings.Contains(ansi.Strip(d.View(70, 5)), "not encrypted") {
		t.Errorf("no warning shown")
	}
	if !d.CaptureKeys() {
		t.Error("dialog must capture keys")
	}
	want := filepath.Join(dir, fileName(it))
	if got, _ := d.target(); got != want {
		t.Fatalf("target %q want %q", got, want)
	}
	save := func() ui.Overlay {
		o, cmd := d.Update(key("enter"))
		for _, msg := range run(cmd) {
			if sm, ok := msg.(savedMsg); ok {
				o, cmd = d.Update(sm)
				if ts := msgsOfType[ui.ToastMsg](run(cmd)); len(ts) == 1 && ts[0].Level != ui.ToastSuccess {
					t.Errorf("toast %v", ts[0])
				}
			}
		}
		return o
	}
	if o := save(); o != nil {
		t.Fatalf("dialog stays open after save: %q", d.status)
	}
	assertMode(t, want, 0o644)
	data, _ := os.ReadFile(want)
	if len(data) == 0 {
		t.Fatal("empty file")
	}
	// Saving again asks before overwriting.
	d = NewSaveDialog(ctx, it)
	if o := save(); o == nil || !d.overwrite {
		t.Fatal("existing file overwritten without asking")
	}
	if o := save(); o != nil {
		t.Fatalf("second enter should overwrite: %q", d.status)
	}
	// Editing resets the overwrite confirmation.
	d = NewSaveDialog(ctx, it)
	save()
	d.Update(key("x"))
	if d.overwrite {
		t.Error("edit kept overwrite armed")
	}
	// A directory gets the default name appended.
	d.input.SetValue(dir)
	if got, _ := d.target(); got != want {
		t.Errorf("dir target %q", got)
	}
	d.input.SetValue("  ")
	if _, err := d.target(); err == nil {
		t.Error("empty path accepted")
	}
	if o, _ := d.Update(key("esc")); o != nil {
		t.Error("esc should close")
	}
	// View-once can't be saved.
	vo := it
	vo.Message.ViewOnce = true
	if ts := msgsOfType[ui.ToastMsg](run(SaveCmd(ctx, vo))); len(ts) != 1 || ts[0].Level != ui.ToastWarning {
		t.Errorf("view-once save: %v", ts)
	}
}

func TestCopy(t *testing.T) {
	svc := newSvc(t)
	ctx := newCtx(t, svc, &fakeImages{})
	m := findMsg(t, svc, hasAtt(isPhoto))
	it := Item{Message: m, Attachment: firstAtt(m, isPhoto)}
	t.Cleanup(restoreHooks())
	var got []byte
	copyImage = func(b []byte) error { got = b; return nil }
	ts := msgsOfType[ui.ToastMsg](run(copyCmd(ctx, it)))
	if len(ts) != 1 || ts[0].Level != ui.ToastSuccess || len(got) < 8 || string(got[1:4]) != "PNG" {
		t.Fatalf("copy: %v (%d bytes)", ts, len(got))
	}
	for _, mod := range []func(*Item){
		func(i *Item) { i.Message.ViewOnce = true },
		func(i *Item) { i.Message.ExpiresIn = 3600e9 },
		func(i *Item) { i.Attachment.ContentType = "video/mp4" },
	} {
		c := it
		mod(&c)
		got = nil
		ts := msgsOfType[ui.ToastMsg](run(copyCmd(ctx, c)))
		if len(ts) != 1 || ts[0].Level == ui.ToastSuccess || got != nil {
			t.Errorf("blocked copy: %v", ts)
		}
	}
}

func TestFileName(t *testing.T) {
	m := core.Message{ID: core.MessageID{Timestamp: 1700000000000}}
	for in, want := range map[string]string{
		"../../etc/passwd": "passwd",
		"a\x1b[31mb.png":   "a[31mb.png",
		".hidden":          "hidden",
		"evil‮gpj.exe":     "evilgpj.exe",
		"dir\\win.txt":     "win.txt",
		"":                 "signal-",
		"photo":            "photo.jpg",
	} {
		got := fileName(Item{Message: m, Attachment: core.Attachment{FileName: in, ContentType: "image/jpeg"}})
		if !strings.HasPrefix(got, strings.TrimSuffix(want, "-")) || strings.ContainsAny(got, "/\\\x1b") {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}
