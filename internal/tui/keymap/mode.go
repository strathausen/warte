package keymap

import (
	"fmt"
	"strings"
)

// Mode is a modal editing state, as in vim.
type Mode int

// Modes. Normal is the zero value.
const (
	// Normal navigates and acts on chats and messages.
	Normal Mode = iota
	// Insert edits the composer.
	Insert
	// Visual selects a range of messages.
	Visual
	// VisualLine selects whole messages linewise (same as Visual for most
	// actions; kept separate so the status line and toggles match vim).
	VisualLine
	// Command edits an ex-style command line after ":".
	Command
	// Search edits a search query after "/".
	Search
)

// AllModes lists every mode in display order.
var AllModes = []Mode{Normal, Insert, Visual, VisualLine, Command, Search}

var modeNames = map[Mode]string{
	Normal:     "normal",
	Insert:     "insert",
	Visual:     "visual",
	VisualLine: "visual_line",
	Command:    "command",
	Search:     "search",
}

var modeLabels = map[Mode]string{
	Normal:     "NORMAL",
	Insert:     "INSERT",
	Visual:     "VISUAL",
	VisualLine: "V-LINE",
	Command:    "COMMAND",
	Search:     "SEARCH",
}

// String returns the config name of the mode ("normal", "visual_line", ...).
func (m Mode) String() string {
	if s, ok := modeNames[m]; ok {
		return s
	}
	return fmt.Sprintf("mode(%d)", int(m))
}

// Label returns the status-line label of the mode ("NORMAL", "V-LINE", ...).
func (m Mode) Label() string {
	if s, ok := modeLabels[m]; ok {
		return s
	}
	return strings.ToUpper(m.String())
}

// Countable reports whether numeric count prefixes and registers are read in
// this mode. In the text-entry modes digits are ordinary input.
func (m Mode) Countable() bool {
	return m == Normal || m == Visual || m == VisualLine
}

// IsVisual reports whether m is Visual or VisualLine.
func (m Mode) IsVisual() bool { return m == Visual || m == VisualLine }

// ParseMode parses a mode name as used in config files. It accepts the
// String() names plus the aliases "visualline", "vline", "v-line", "cmd" and
// "cmdline".
func ParseMode(s string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "normal", "n":
		return Normal, nil
	case "insert", "i":
		return Insert, nil
	case "visual", "v", "x":
		return Visual, nil
	case "visual_line", "visualline", "vline", "v-line":
		return VisualLine, nil
	case "command", "cmd", "cmdline", "c":
		return Command, nil
	case "search", "s":
		return Search, nil
	}
	return 0, fmt.Errorf("unknown mode %q", s)
}

// Context is a binding layer scoped to a part of the UI. Bindings in a
// context take precedence over the mode's global bindings while that part of
// the UI has focus.
type Context string

// Contexts.
const (
	// Global holds bindings that apply everywhere in a mode.
	Global Context = ""
	// ChatList is the chat list pane.
	ChatList Context = "chatlist"
	// Conversation is the message pane.
	Conversation Context = "conversation"
	// Sidebar is the right sidebar (info, media gallery, results).
	Sidebar Context = "sidebar"
	// AnyOverlay holds bindings shared by every overlay. Overlay(name)
	// contexts fall back to it before Global.
	AnyOverlay Context = "overlay"
)

// Well-known overlay names used by the default bindings.
const (
	OverlayHelp    = "help"
	OverlayImage   = "image"
	OverlayStory   = "story"
	OverlayConfirm = "confirm"
)

// Overlay returns the context for the named overlay (help, picker, image
// viewer, ...).
func Overlay(name string) Context { return Context("overlay:" + name) }

// IsOverlay reports whether c is AnyOverlay or an Overlay(name) context.
func (c Context) IsOverlay() bool {
	return c == AnyOverlay || strings.HasPrefix(string(c), "overlay:")
}

// OverlayName returns the overlay name of an Overlay(name) context, or "".
func (c Context) OverlayName() string {
	return strings.TrimPrefix(string(c), "overlay:")
}

// String returns the config name of the context ("global" for Global).
func (c Context) String() string {
	if c == Global {
		return "global"
	}
	return string(c)
}

// chain returns the lookup layers for c from most general to most specific.
func (c Context) chain() []Context {
	switch {
	case c == Global:
		return []Context{Global}
	case c == AnyOverlay:
		return []Context{Global, AnyOverlay}
	case c.IsOverlay():
		return []Context{Global, AnyOverlay, c}
	default:
		return []Context{Global, c}
	}
}

// ParseContext parses a context name as used in config files: "global" (or
// ""), "chatlist", "conversation", "sidebar", "overlay" or "overlay.<name>"
// / "overlay:<name>".
func ParseContext(s string) (Context, error) {
	l := strings.ToLower(strings.TrimSpace(s))
	switch l {
	case "", "global":
		return Global, nil
	case "chatlist", "chat_list", "chats", "list":
		return ChatList, nil
	case "conversation", "chat", "messages":
		return Conversation, nil
	case "sidebar":
		return Sidebar, nil
	case "overlay":
		return AnyOverlay, nil
	}
	for _, p := range []string{"overlay.", "overlay:"} {
		if name, ok := strings.CutPrefix(l, p); ok && name != "" {
			return Overlay(name), nil
		}
	}
	return "", fmt.Errorf("unknown context %q", s)
}
