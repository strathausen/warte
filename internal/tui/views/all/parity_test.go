package all_test

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"warte/internal/core"
	"warte/internal/tui/ui"
)

// Parity features added on top of the feature table: polls, in-app Signal
// links, view-once sending, :export.

func TestPollReachable(t *testing.T) {
	d := newDriver(t, 100, 30)
	d.focus(func(m core.Message) bool { return m.Poll != nil && !m.Poll.Ended })
	d.keys("enter")
	if got := d.top(); !strings.Contains(got, "Vote") {
		t.Fatalf("enter on a poll: top overlay = %q", got)
	}
	d.checkFrame("vote")
	d.keys("esc")
	if d.a.Overlays() != 0 {
		t.Fatal("vote dialog did not close")
	}
	d.keys(":poll")
	if got := d.top(); !strings.Contains(got, "New poll") {
		t.Fatalf(":poll: top overlay = %q", got)
	}
	d.checkFrame("new poll")
	d.keys("esc")
	if d.a.Overlays() != 0 {
		t.Fatal("poll composer did not close")
	}
}

func TestSignalLinkOpensInApp(t *testing.T) {
	d := newDriver(t, 100, 30)
	ctx := context.Background()
	chats, err := d.svc.Chats(ctx, core.ChatFilter{})
	if err != nil || len(chats) == 0 {
		t.Fatal(chats, err)
	}
	// Not the chat the App opened at start (its messages are cached).
	if _, err := d.svc.Send(ctx, chats[len(chats)-1].ID, core.Outgoing{Body: "join us https://signal.group/#bm90LWEtbGluaw"}); err != nil {
		t.Fatal(err)
	}
	d.focus(func(m core.Message) bool { return strings.Contains(m.Body, "signal.group") })
	d.keys("g x")
	if got := d.top(); !strings.Contains(got, "Join") {
		t.Fatalf("gx on a group link: top overlay = %q", got)
	}
	d.keys("n")
	d.keys("enter")
	if got := d.top(); !strings.Contains(got, "Join") {
		t.Fatalf("enter on a group link: top overlay = %q", got)
	}
	d.keys("esc")
}

func TestViewOnceSend(t *testing.T) {
	d := newDriver(t, 100, 30)
	dir := t.TempDir()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	pic := filepath.Join(dir, "pic.png")
	if err := os.WriteFile(pic, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	txt := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(txt, []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	chat := d.a.UIContext().Chat()
	if chat == "" {
		t.Fatal("no chat open")
	}
	// A text file can't be view-once.
	d.send(ui.RunCommandMsg{Line: "attach " + txt})
	d.keys("esc") // :attach focuses the composer
	d.keys(":viewonce")
	if strings.Contains(d.checkFrame("txt"), "view once") {
		t.Fatal("view once enabled for a text file")
	}

	d2 := newDriver(t, 100, 30)
	chat = d2.a.UIContext().Chat()
	d2.send(ui.RunCommandMsg{Line: "attach " + pic})
	d2.keys("esc")
	d2.keys(":viewonce")
	if f := d2.checkFrame("pic"); !strings.Contains(f, "view once") {
		t.Fatalf("no view-once chip:\n%s", f)
	}
	d2.keys("i enter")
	msgs, err := d2.svc.Messages(context.Background(), chat, core.MessageID{}, 3)
	if err != nil {
		t.Fatal(err)
	}
	last := msgs[len(msgs)-1]
	if !last.Outgoing || !last.ViewOnce || len(last.Attachments) != 1 {
		t.Fatalf("last message: outgoing=%v viewOnce=%v atts=%d", last.Outgoing, last.ViewOnce, len(last.Attachments))
	}
}

func TestExportFromTUI(t *testing.T) {
	d := newDriver(t, 100, 30)
	path := filepath.Join(t.TempDir(), "backup.wexp")
	d.keys(":export " + path)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
}
