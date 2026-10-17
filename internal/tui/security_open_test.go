package tui

import (
	"testing"
	"time"

	"warte/internal/config"
	"warte/internal/core"
	"warte/internal/fake"
	"warte/internal/termimg"
	"warte/internal/tui/ui"
)

// TestOpenAttachmentRefusesRiskyExtensions: a received file whose name (or,
// without one, content type) maps to a launcher must not reach the opener.
func TestOpenAttachmentRefusesRiskyExtensions(t *testing.T) {
	svc := fake.New(fake.Options{Seed: 1, Latency: -1, Clock: func() time.Time { return testNow }})
	t.Cleanup(func() { _ = svc.Close() })
	cfg := config.Default()
	// An opener that would fail loudly if it were ever run.
	cfg.OpenWith = map[string]string{config.OpenDefault: "/nonexistent/warte-test-opener"}
	none := false
	blocks := termimg.HalfBlocks
	a := New(svc, cfg, Options{Now: func() time.Time { return testNow }, Getenv: func(string) string { return "" },
		NerdFonts: &none, Images: &blocks, SkipStart: true, Notifier: nopNotifier{}})
	a.noPump = true
	t.Cleanup(a.Close)
	m := core.Message{ID: core.MessageID{Chat: fake.ChatDev, Author: "x", Timestamp: 1}}
	for _, att := range []core.Attachment{
		{ID: "a1", FileName: "song.terminal", ContentType: "audio/mpeg"},
		{ID: "a2", FileName: "photo.JAR", ContentType: "image/png"},
		{ID: "a3", ContentType: "text/javascript"}, // no name: extension from the type (.js)
	} {
		before := len(a.toasts)
		_ = a.openAttachment(m, att)
		if len(a.toasts) != before+1 || a.toasts[len(a.toasts)-1].text != clean(ui.RiskyOpenText) {
			t.Errorf("%+v: not refused (toasts %+v)", att, a.toasts)
		}
		if len(a.tempFiles) != 0 {
			t.Errorf("%+v: temp file written", att)
		}
	}
}
