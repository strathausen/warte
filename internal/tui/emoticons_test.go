package tui

import (
	"testing"
	"time"
)

func TestEmoticonConvertsOnSpace(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.keys("<enter>") // opens the chat in Insert mode
	h.keys("hi :) http:// <3")
	if got := h.a.comp.ta.Value(); got != "hi 🙂 http:// <3" {
		t.Fatalf("value %q", got)
	}
	h.keys("<enter>") // the last word converts on send
	if !h.waitFor(2*time.Second, func() bool { return h.lastMsg().Body == "hi 🙂 http:// ❤️" }) {
		t.Fatalf("sent %q", h.lastMsg().Body)
	}
}

func TestCustomEmoticonsAndOff(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.a.cfg.Emoticons = map[string]string{"/shrug": "¯\\_(ツ)_/¯", ":)": ""}
	h.keys("<enter>")
	h.keys("/shrug :) ")
	if got := h.a.comp.ta.Value(); got != "¯\\_(ツ)_/¯ :) " {
		t.Fatalf("value %q", got)
	}
	h.a.comp.ta.SetValue("")
	h.a.cfg.ConvertEmoticons = false
	h.keys("/shrug ")
	if got := h.a.comp.ta.Value(); got != "/shrug " {
		t.Fatalf("off: value %q", got)
	}
}
