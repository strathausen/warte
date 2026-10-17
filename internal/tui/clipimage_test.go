package tui

import (
	"bytes"
	"encoding/hex"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func testPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	img.Set(1, 1, color.NRGBA{R: 255, A: 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func stubClipboardImage(t *testing.T, data []byte) {
	t.Helper()
	old := readClipboardImageFn
	readClipboardImageFn = func() ([]byte, error) {
		if data == nil {
			return nil, errNoClipboardImage
		}
		return data, nil
	}
	t.Cleanup(func() { readClipboardImageFn = old })
}

func TestParseAppleScriptData(t *testing.T) {
	data := testPNG(t)
	out := []byte("«data PNGf" + strings.ToUpper(hex.EncodeToString(data)) + "»\n")
	got, err := parseAppleScriptData(out)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("parse: %v", err)
	}
	for _, bad := range []string{"", "hello", "«data TIFF0000»", "«data PNGfZZ»", "«data PNGf00»"} {
		if _, err := parseAppleScriptData([]byte(bad)); err == nil {
			t.Errorf("%q: want error", bad)
		}
	}
}

func TestCtrlVPastesClipboardImage(t *testing.T) {
	data := testPNG(t)
	stubClipboardImage(t, data)
	h := newHarness(t, 120, 40)
	h.keys("<enter>") // opens the chat in Insert mode
	h.keys("<c-v>")
	if len(h.a.comp.attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(h.a.comp.attachments))
	}
	f := h.a.comp.attachments[0]
	if f.path != "" || !bytes.Equal(f.data, data) || f.name != "pasted-image.png" {
		t.Fatalf("staged %+v", f)
	}
	if !strings.Contains(h.plain(), "pasted-image.png") {
		t.Fatalf("no attachment chip:\n%s", h.plain())
	}
	h.keys("look<enter>")
	ok := h.waitFor(2*time.Second, func() bool {
		m := h.lastMsg()
		return m.Body == "look" && len(m.Attachments) == 1 && m.Attachments[0].ContentType == "image/png" &&
			m.Attachments[0].Width == 3 && m.Attachments[0].Height == 2
	})
	if !ok {
		t.Fatalf("image not sent: %+v", h.lastMsg())
	}
	if len(h.a.comp.attachments) != 0 {
		t.Fatal("composer still has attachments after send")
	}
}

func TestCtrlVFallsBackToText(t *testing.T) {
	stubClipboardImage(t, nil)
	h := newHarness(t, 120, 40)
	h.keys("<enter>")
	h.send(clipboardPasteMsg{text: "hello"}) // what the text path yields
	if len(h.a.comp.attachments) != 0 || h.a.comp.ta.Value() != "hello" {
		t.Fatalf("value %q, attachments %d", h.a.comp.ta.Value(), len(h.a.comp.attachments))
	}
}

func TestEmptyTerminalPasteChecksForImage(t *testing.T) {
	data := testPNG(t)
	stubClipboardImage(t, data)
	h := newHarness(t, 120, 40)
	h.keys("<enter>")
	h.send(tea.PasteMsg{Content: ""})
	if len(h.a.comp.attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(h.a.comp.attachments))
	}
	// A normal text paste is still text.
	h.send(tea.PasteMsg{Content: "hi"})
	if h.a.comp.ta.Value() != "hi" || len(h.a.comp.attachments) != 1 {
		t.Fatalf("value %q, attachments %d", h.a.comp.ta.Value(), len(h.a.comp.attachments))
	}
}

func TestUnpinCommandActsOnSelectedMessage(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.keys("<enter><esc>") // open the top chat, go to the message list
	chat := h.a.conv.chat.ID
	wasPinned := h.a.conv.chat.Pinned
	h.keys("G")
	i := h.a.conv.sel
	h.keys(":pin<enter>")
	if !h.waitFor(time.Second, func() bool { return h.a.conv.msgs[i].m.Pinned }) {
		t.Fatal(":pin did not pin the selected message")
	}
	h.keys(":unpin<enter>")
	if !h.waitFor(time.Second, func() bool { return !h.a.conv.msgs[i].m.Pinned }) {
		t.Fatal(":unpin did not unpin the selected message")
	}
	if c := h.a.conv.chat; c.ID != chat || c.Pinned != wasPinned {
		t.Fatalf("chat pin changed: %v -> %v", wasPinned, c.Pinned)
	}
}

func TestUnpinCommandInChatListActsOnChat(t *testing.T) {
	h := newHarness(t, 120, 40)
	c := h.a.list.current()
	if c == nil || !c.Pinned {
		t.Skip("top demo chat is not pinned")
	}
	id := c.ID
	h.keys(":unpin<enter>")
	ok := h.waitFor(time.Second, func() bool {
		for _, x := range h.a.list.chats {
			if x.ID == id {
				return !x.Pinned
			}
		}
		return false
	})
	if !ok {
		t.Fatal(":unpin in the chat list did not unpin the chat")
	}
}
