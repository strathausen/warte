package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"warte/internal/tui/keymap"
)

// Shift-Tab on a fresh completion selects the last candidate, not the one
// before it.
func TestCmdCompleteShiftTabStartsAtLast(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.keys(":s")
	h.send(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	c := h.a.cmdline
	if len(c.cands) < 2 {
		t.Fatalf("want several candidates, got %v", c.cands)
	}
	if got, want := c.input.Value(), c.cands[len(c.cands)-1]; got != want {
		t.Fatalf("shift+tab chose %q, want last candidate %q", got, want)
	}
	h.keys("<esc>")
}

// ":set nosidebar" while the sidebar has focus moves focus back.
func TestSetNoSidebarMovesFocus(t *testing.T) {
	h := newHarness(t, 160, 48)
	h.keys("<enter>")
	h.a.sidebarOpen = true
	h.a.setFocus(FocusSidebar)
	h.keys(":set nosidebar<enter>")
	if h.a.sidebarOpen || h.a.focus == FocusSidebar {
		t.Fatalf("sidebar open=%v focus=%v", h.a.sidebarOpen, h.a.focus)
	}
}

// "h" in Visual mode must not move focus to the chat list while the
// selection stays active.
func TestVisualHKeepsFocus(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.keys("<enter><esc>vk")
	if !h.a.keys.Mode().IsVisual() {
		t.Fatalf("mode = %v", h.a.keys.Mode())
	}
	h.keys("h")
	if h.a.focus != FocusConversation || !h.a.keys.Mode().IsVisual() {
		t.Fatalf("after h: focus=%v mode=%v", h.a.focus, h.a.keys.Mode())
	}
	h.keys("<esc>")
	if h.a.keys.Mode() != keymap.Normal {
		t.Fatalf("esc: mode = %v", h.a.keys.Mode())
	}
}

// "h" in the narrow layout goes back to the chat list, like "l" opens.
func TestNarrowHGoesBack(t *testing.T) {
	h := newHarness(t, 80, 24)
	h.keys("<enter><esc>")
	if h.a.focus != FocusConversation {
		t.Fatalf("focus = %v", h.a.focus)
	}
	h.keys("h")
	if h.a.focus != FocusChatList {
		t.Fatalf("h: focus = %v", h.a.focus)
	}
}

// The key that opened an overlay closes it again ("?" over help, "K" over
// message details); other keys don't.
func TestOpenerKeyTogglesOverlay(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.keys("<enter><esc>?")
	if len(h.a.overlays) != 1 {
		t.Fatalf("? did not open help")
	}
	h.keys("j")
	if len(h.a.overlays) != 1 {
		t.Fatalf("j closed help")
	}
	h.keys("?")
	if len(h.a.overlays) != 0 {
		t.Fatalf("? did not close help (%d overlays)", len(h.a.overlays))
	}
	h.keys("K")
	if len(h.a.overlays) != 1 {
		t.Fatalf("K did not open details")
	}
	h.keys("K")
	if len(h.a.overlays) != 0 || h.a.keys.Mode() != keymap.Normal {
		t.Fatalf("K did not close details: %d overlays, mode %v", len(h.a.overlays), h.a.keys.Mode())
	}
}

// ctrl+a opens ":attach " from Insert mode; leaving the command line
// returns to the composer.
func TestAttachCmdLineReturnsToInsert(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.keys("<enter>")
	h.keys("<C-a>")
	if h.a.keys.Mode() != keymap.Command {
		t.Fatalf("ctrl+a: mode = %v", h.a.keys.Mode())
	}
	h.keys("<esc>")
	if h.a.keys.Mode() != keymap.Insert || h.a.focus != FocusComposer {
		t.Fatalf("esc: mode = %v focus = %v", h.a.keys.Mode(), h.a.focus)
	}
	h.keys("x")
	if h.a.comp.ta.Value() != "x" {
		t.Fatalf("typing after esc: %q", h.a.comp.ta.Value())
	}
}

// Clicking while the command line is open closes it.
func TestClickClosesCmdLine(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.a.mouse = true
	h.keys(":")
	_ = h.view()
	h.send(tea.MouseClickMsg{X: 60, Y: 10, Button: tea.MouseLeft})
	if h.a.keys.Mode() != keymap.Normal {
		t.Fatalf("click: mode = %v", h.a.keys.Mode())
	}
}

// Shift-Tab in the composer without a completion inserts nothing.
func TestComposerShiftTabInsertsNothing(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.keys("<enter>hi")
	h.send(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if v := h.a.comp.ta.Value(); v != "hi" {
		t.Fatalf("composer = %q", v)
	}
}
