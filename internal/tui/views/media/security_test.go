package media

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"warte/internal/core"
	"warte/internal/tui/ui"
)

// A received attachment named like a launcher (.terminal, .jar, .webloc)
// must never reach the platform opener: "open x.terminal" runs commands.
func TestOpenExternalRefusesRiskyExtensions(t *testing.T) {
	t.Cleanup(restoreHooks())
	called := false
	startDetached = func(string, ...string) error { called = true; return nil }
	ctx := newCtx(t, nil, nil)
	for _, name := range []string{"evil.terminal", "x.JAR", "a.webloc", "run.command", "y.desktop"} {
		it := Item{
			Message:    core.Message{ID: core.MessageID{Chat: "u:x", Author: "x", Timestamp: 1}},
			Attachment: core.Attachment{ID: "a1", FileName: name, ContentType: "audio/mpeg"},
		}
		msgs := run(openExternal(ctx, it))
		if called {
			t.Fatalf("%s handed to the opener", name)
		}
		if len(msgs) != 1 {
			t.Fatalf("%s: msgs = %v", name, msgs)
		}
		tm, ok := msgs[0].(ui.ToastMsg)
		if !ok || tm.Level != ui.ToastWarning || tm.Text != ui.RiskyOpenText {
			t.Errorf("%s: got %#v", name, msgs[0])
		}
	}
}

func TestWriteTempDropsRiskyExtension(t *testing.T) {
	for ext, want := range map[string]string{".jar": "", ".terminal": "", ".mp3": ".mp3", ".png": ".png"} {
		p, err := writeTemp([]byte("x"), ext)
		if err != nil {
			t.Fatal(err)
		}
		if got := filepath.Ext(p); got != want && !(want == "" && !strings.HasPrefix(got, ".")) {
			t.Errorf("writeTemp(%q) = %s", ext, p)
		}
		st, err := os.Stat(p)
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Errorf("writeTemp(%q) mode = %v, %v", ext, st.Mode(), err)
		}
		_ = os.Remove(p)
	}
}
