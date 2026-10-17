// Package keymap is warte's modal, vim-style key engine.
//
// It is independent of Bubble Tea: the TUI converts each tea.KeyPressMsg to
// a canonical key string (see Canonical and FromTea) and feeds it to an
// Engine together with the focused Context. The Engine returns a Result
// naming an Action (a typed string such as ActCursorDown, "cursor.down")
// with its count and register, or reports that the sequence is pending,
// unmatched or canceled.
//
// Features:
//
//   - Modes (Normal, Insert, Visual, VisualLine, Command, Search). The UI
//     owns mode changes and tells the engine with SetMode.
//   - Context layers (ChatList, Conversation, Sidebar, Overlay(name)).
//     Lookup tries the context's bindings first, then (for overlays) the
//     bindings shared by all overlays, then the mode's global bindings.
//   - Counts ("5j", "10G"; "0" is a key unless a count is in progress) and
//     registers ('"a' before the command) in Normal and Visual modes.
//   - Multi-key sequences ("gg", "ctrl+w h", "<leader>s"), a configurable
//     leader key, and timeout-based resolution when a sequence is both a
//     complete binding and a prefix of another (see Flush and Timeout).
//   - "." repeats the last Repeatable action.
//   - User remaps from config (ApplyOverrides) in vim-like notation
//     (ParseKeys), help data for the "?" overlay (Help, Bindings), status
//     line hints (ShortHelp) and key hints (KeysFor, Hint).
//
// Typical use in a Bubble Tea Update:
//
//	case tea.KeyPressMsg:
//		res := m.keys.Feed(keymap.FromTea(msg), m.focusContext())
//		for _, r := range res.All() {
//			switch {
//			case r.Expires:
//				cmds = append(cmds, tea.Tick(m.keys.Timeout(), func(time.Time) tea.Msg { return flushMsg{} }))
//			case r.Unmatched && m.keys.Mode() == keymap.Insert:
//				// forward msg to the textarea
//			case r.Matched():
//				cmds = append(cmds, m.do(r))
//			}
//		}
//	case flushMsg:
//		res := m.keys.Flush()
//		// handle like above
package keymap
