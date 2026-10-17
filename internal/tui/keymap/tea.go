package keymap

import tea "charm.land/bubbletea/v2"

// FromTea converts a Bubble Tea key press to a canonical key string for
// Engine.Feed. It is msg.String() normalized with Canonical, so "shift+g"
// from terminals that report modifiers separately still becomes "G".
func FromTea(msg tea.KeyPressMsg) string {
	return Canonical(msg.String())
}
