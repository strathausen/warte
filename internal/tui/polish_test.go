package tui

import (
	"strings"
	"testing"

	"warte/internal/config"
	"warte/internal/core"
	"warte/internal/fake"
)

// TestMonoImagesAreLabelledBoxes: without colors, half-block images would be
// a field of "▀"; the monochrome theme shows shaded, labelled boxes instead.
func TestMonoImagesAreLabelledBoxes(t *testing.T) {
	h := newHarness(t, 100, 40, func(_ *fake.Options, c *config.Config, _ *Options) { c.NoColor = true })
	h.keys("enter")
	out := h.plain()
	if strings.Contains(out, "▀▀▀▀") {
		t.Fatalf("half-block image drawn in mono:\n%s", out)
	}
	if !strings.Contains(out, "░░░░░░░░") {
		t.Fatalf("no labelled image placeholder:\n%s", out)
	}
}

// TestIRCLayoutRightMargin: IRC lines keep a blank cell before the pane
// border and a space between the nick and the separator.
func TestIRCLayoutRightMargin(t *testing.T) {
	h := newHarness(t, 100, 30, func(_ *fake.Options, c *config.Config, _ *Options) { c.Layout = "irc" })
	m := core.Message{ID: core.MessageID{Author: "x"}, SenderName: "Alice Chen", Body: strings.Repeat("word ", 60)}
	lines, _ := h.a.renderIRC(&m, 60, flagFirst|flagLast, "")
	if len(lines) < 2 {
		t.Fatalf("no wrap: %q", lines)
	}
	for _, l := range lines {
		p := stripANSI(l)
		// The caller pads to the pane width; the last cell stays blank.
		if width(p) > 59 {
			t.Fatalf("line width %d (no right margin): %q", width(p), p)
		}
	}
	if !strings.Contains(stripANSI(lines[0]), "Alice Chen │") {
		t.Fatalf("nick touches the separator: %q", stripANSI(lines[0]))
	}
}

// TestStatusHidesHintsForCapturingOverlay: while an overlay reads keys raw
// (the chat switcher's search field), keymap hints like "q close" would be
// wrong, so the status line shows none.
func TestStatusHidesHintsForCapturingOverlay(t *testing.T) {
	h := newHarness(t, 100, 30)
	h.keys("<space><space>")
	if len(h.a.overlays) == 0 {
		t.Skip("switcher not registered in this package")
	}
	lines := strings.Split(h.plain(), "\n")
	if st := lines[len(lines)-1]; strings.Contains(st, "q close") {
		t.Fatalf("status shows keymap hints: %q", st)
	}
}
