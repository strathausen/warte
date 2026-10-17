package finder

import (
	tea "charm.land/bubbletea/v2"

	"warte/internal/tui/keymap"
	"warte/internal/tui/ui"
)

// navAction is what a key press means to a finder list.
type navAction int

const (
	navNone     navAction = iota
	navEdited             // the query changed
	navDown               // next item
	navUp                 // previous item
	navTop                // first item
	navBottom             // last item
	navPageDown           // half a page down
	navPageUp             // half a page up
	navSelect             // accept (enter)
	navToggle             // toggle multi-selection of the current item
	navToggleUp           // toggle and move up (shift+tab)
	navClose              // close the overlay
	navMode               // switched between insert and normal mode
	navPreview            // toggle the preview pane
)

// modal implements the Telescope-style two-mode key handling shared by all
// finder overlays. In insert mode (the default) the query field has focus:
// printable keys edit it, ctrl+n/ctrl+p/up/down move, enter accepts, tab
// toggles (multi-select), esc switches to normal mode. In normal mode
// j/k/gg/G/ctrl+d/ctrl+u move, space/tab toggle, i/a// return to the input,
// esc/q close.
//
// The overlays are KeyCapturers (the space key would otherwise start a
// <leader> sequence), so they see raw key presses; ui.ActionMsg values that
// still arrive (e.g. from a host that ignores KeyCapturer) are mapped too.
type modal struct {
	input    lineInput
	normal   bool
	pendingG bool
	multi    bool
	verb     string // footer label for enter ("open" by default)
}

func (m *modal) key(msg tea.KeyPressMsg) navAction {
	k := msg.String()
	if k == "ctrl+c" || k == "alt+esc" {
		return navClose // alt+esc: two quick escapes merged by the terminal
	}
	if !m.normal {
		switch k {
		case "esc":
			m.normal = true
			return navMode
		case "enter":
			return navSelect
		case "down", "ctrl+n", "ctrl+j":
			return navDown
		case "up", "ctrl+p":
			return navUp
		case "pgdown":
			return navPageDown
		case "pgup":
			return navPageUp
		case "tab":
			if m.multi {
				return navToggle
			}
			return navDown
		case "shift+tab":
			if m.multi {
				return navToggleUp
			}
			return navUp
		case "ctrl+o":
			return navPreview
		}
		if _, changed := m.input.handle(msg); changed {
			return navEdited
		}
		return navNone
	}
	// Normal mode.
	if m.pendingG {
		m.pendingG = false
		if k == "g" {
			return navTop
		}
	}
	switch k {
	case "esc", "q":
		return navClose
	case "enter":
		return navSelect
	case "j", "down", "ctrl+n":
		return navDown
	case "k", "up", "ctrl+p":
		return navUp
	case "g", "home":
		m.pendingG = k == "g"
		if k == "home" {
			return navTop
		}
		return navNone
	case "G", "end":
		return navBottom
	case "ctrl+d", "pgdown":
		return navPageDown
	case "ctrl+u", "pgup":
		return navPageUp
	case "space", "tab", "x":
		if m.multi {
			return navToggle
		}
		if k == "tab" {
			return navDown
		}
	case "shift+tab":
		if m.multi {
			return navToggleUp
		}
		return navUp
	case "p":
		return navPreview
	case "i", "a", "/":
		m.normal = false
		return navMode
	case "I":
		m.normal = false
		m.input.pos = 0
		return navMode
	case "A":
		m.normal = false
		m.input.pos = len(m.input.value)
		return navMode
	case "S":
		m.normal = false
		m.input.SetValue("")
		return navEdited
	}
	return navNone
}

// action maps a keymap action (delivered when the host routed a key through
// the keymap engine) to a navAction.
func (m *modal) action(msg ui.ActionMsg) navAction {
	switch msg.Action {
	case keymap.ActCursorDown:
		return navDown
	case keymap.ActCursorUp:
		return navUp
	case keymap.ActCursorTop:
		return navTop
	case keymap.ActCursorBottom:
		return navBottom
	case keymap.ActHalfPageDown, keymap.ActPageDown:
		return navPageDown
	case keymap.ActHalfPageUp, keymap.ActPageUp:
		return navPageUp
	case keymap.ActItemSelect:
		return navSelect
	case keymap.ActBack, keymap.ActCancel:
		return navClose
	}
	return navNone
}

// paste inserts pasted text into the query (insert mode only).
func (m *modal) paste(s string) bool {
	if m.normal {
		m.normal = false
	}
	before := m.input.Value()
	m.input.insert(s)
	return m.input.Value() != before
}

// cursor moves sel within [0, n) for a navigation action; page is the
// number of visible rows.
func moveCursor(a navAction, sel, n, page int) int {
	if n <= 0 {
		return 0
	}
	half := max(page/2, 1)
	switch a {
	case navDown:
		sel++
	case navUp:
		sel--
	case navTop:
		sel = 0
	case navBottom:
		sel = n - 1
	case navPageDown:
		sel = min(sel+half, n-1)
	case navPageUp:
		sel = max(sel-half, 0)
	}
	return min(max(sel, 0), n-1)
}

// isMove reports whether a is a cursor movement.
func isMove(a navAction) bool {
	switch a {
	case navDown, navUp, navTop, navBottom, navPageDown, navPageUp:
		return true
	}
	return false
}

// modeHints returns footer key hints for the current mode.
func (m *modal) hints(extra ...string) []string {
	verb := m.verb
	if verb == "" {
		verb = "open"
	}
	if m.normal {
		h := []string{"j/k", "move", "enter", verb, "i", "edit"}
		if m.multi {
			h = []string{"j/k", "move", "space", "mark", "enter", "done"}
		}
		return append(append(h, extra...), "q", "close")
	}
	h := []string{"↑/↓", "move", "enter", verb}
	if m.multi {
		h = []string{"↑/↓", "move", "tab", "mark", "enter", "done"}
	}
	return append(append(h, extra...), "esc", "normal")
}
