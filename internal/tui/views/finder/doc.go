// Package finder provides warte's Telescope-style fuzzy finder overlays:
// the chat switcher, global message search and a generic picker used for
// forwarding and theme selection. Every view is a ui.Overlay, themed through
// ui.Context.Styles and safe at any size (no line wider than the width it is
// given, empty data renders an empty state).
//
// # Constructors
//
//	NewChatSwitcher(ctx) *Chats                       chat switcher ("<leader><leader>")
//	NewChatSwitcherQuery(ctx, query) *Chats           … with a prefilled query
//	NewSearch(ctx) *Search                            global message search ("<leader>/")
//	NewSearchQuery(ctx, query) *Search                … prefilled; searches immediately
//	NewPicker[T](ctx, PickerOptions[T]) *Picker[T]    generic fuzzy picker (single/multi, preview)
//	NewForwardPicker(ctx, id, more...) *Picker[core.Chat]  multi-select forward via svc.Forward + toast
//	NewChatPicker(ctx, title, multi, onSelect)        generic chat picker built on Picker
//	NewThemePicker(ctx) *Picker[string]               theme list with theme.Preview pane
//	ParseQuery(s, now) Query                          search filter syntax parser
//	NoteRecent(chat)                                  boost a chat in the switcher's recents
//
// # Keys
//
// All finder overlays implement ui.KeyCapturer (always true): the query
// field is live and the space key must not start a <leader> sequence, so
// the views route keys themselves with two Telescope-like modes.
//
// Insert mode (initial; the query has focus):
//
//	printable keys, space   edit the query (readline: ctrl+a/e/b/f, ctrl+w,
//	                        ctrl+u, ctrl+k, alt+b/f/d, home/end, left/right)
//	down, ctrl+n, ctrl+j    next item          up, ctrl+p   previous item
//	pgdown / pgup           half page
//	tab / shift+tab         toggle mark and move (multi-select), else move
//	enter                   accept
//	ctrl+o                  toggle the preview pane (search, pickers with Preview)
//	esc                     switch to normal mode
//	ctrl+c, alt+esc         close
//
// Normal mode:
//
//	j/k, down/up, ctrl+n/p  move              gg / G, home/end   first / last
//	ctrl+d / ctrl+u         half page          enter              accept
//	space, x, tab           toggle mark (multi-select)
//	p                       toggle preview     i, a, /, I, A      back to the query
//	S                       clear the query and edit
//	q, esc, ctrl+c          close
//
// Mouse: click selects (and accepts / toggles), wheel moves. If a host
// routes keys through the keymap instead, ui.ActionMsg values for the
// cursor actions, keymap.ActItemSelect and keymap.ActBack are understood.
//
// Keymap overlay context names (KeyContext): ContextChats "finder.chats",
// ContextSearch "finder.search", ContextPicker "finder.picker" (default for
// NewPicker), ContextForward "finder.forward", ContextTheme "finder.theme".
//
// # Messages emitted
//
//	ui.OpenChatMsg{Chat}            chat switcher: open a chat (contacts and the
//	                                "Start chat with +49…/@user" entry resolve via
//	                                svc.ResolveRecipient first)
//	ui.OpenChatMsg{Chat, Focus}     search: jump to the hit
//	ui.ToastMsg                     forward result; resolve failures
//	ui.RunCommandMsg{"theme <name>"} theme picker (applies via the shell's :theme)
//
// SetThemeMsg is defined for an integrator who prefers a dedicated message;
// nothing emits it today because the shell already handles ":theme".
//
// Search filter syntax (see Query): from:bob in:book has:image
// before:2026-01 after:2025-12 on:today, quoted values (from:"bob o"),
// relative dates (3d, 2w, 6m, 1y). Typing is debounced by SearchDebounce
// (200ms); stale results are dropped. Results are grouped by chat; on wide
// dialogs (>= 96 columns) a preview pane shows svc.MessagesAround the hit.
//
// # Registration (init)
//
// The package registers, so a blank import from cmd/warte is enough:
//
//	ui.RegisterAction(keymap.ActFinderChats, …)   <leader><leader> → NewChatSwitcher
//	ui.RegisterAction(keymap.ActSearchGlobal, …)  <leader>/        → NewSearch
//	ui.RegisterAction(keymap.ActThemePicker, …)   <leader>t        → NewThemePicker
//	:find <query>     global search with filters (tab-completes filter keys)
//	:chats [query]    chat switcher
//	:themes           theme picker
//	:forward          multi-chat forward of ctx.Selected()
//
// Suggested hooks for the integrator: route keymap.ActMessageForward ("f")
// and keymap.ActVisualForward to NewForwardPicker (the shell currently uses
// its built-in single-chat picker), and call NoteRecent whenever a chat is
// opened so recents also reflect chat-list navigation.
package finder
