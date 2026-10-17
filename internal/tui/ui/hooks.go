package ui

import (
	tea "charm.land/bubbletea/v2"

	"warte/internal/config"
	"warte/internal/core"
	"warte/internal/tui/keymap"
)

// Actions the App dispatches to registered handlers when the user opens a
// message (enter / click). Without a handler the App falls back to its
// built-in overlays or the external opener.
const (
	// ActViewImage opens the image viewer for ctx.Selected().
	ActViewImage keymap.Action = "viewer.image"
	// ActViewOnce opens the view-once viewer for ctx.Selected().
	ActViewOnce keymap.Action = "viewer.viewonce"
	// ActPlayAudio plays the voice note / audio file of ctx.Selected().
	ActPlayAudio keymap.Action = "player.audio"
	// ActPollVote opens the vote dialog for the poll ctx.Selected().
	ActPollVote keymap.Action = "poll.vote"
	// ActLoggedOut is dispatched when the backend reports that this device
	// was unlinked; the handler pushes the logged-out screen.
	ActLoggedOut keymap.Action = "app.logged_out"
)

// Dismisser is implemented by overlays that must clean up when the App
// closes them without asking (a click outside the dialog, logout, quit):
// stop playback, mark a view-once message viewed, revert a preview.
type Dismisser interface {
	Dismiss() tea.Cmd
}

// ConfigChangedMsg asks the App to apply a changed local configuration
// live (theme, layout, mouse, reduce motion, notifications, time format,
// images). Save additionally writes the changed keys to config.toml.
type ConfigChangedMsg struct {
	Config config.Config
	Save   bool
}

// PreviewThemeMsg previews a theme without changing the configuration (the
// theme picker sends it as the cursor moves). An empty Name restores the
// configured theme. The App also restores it when the last overlay closes.
type PreviewThemeMsg struct{ Name string }

// RelinkMsg asks the App to quit and start linking this device again
// (from the logged-out screen).
type RelinkMsg struct{}

// Pane is a component drawn inside one of the App's panes (the right
// sidebar). The App draws the border and feeds it messages.
type Pane interface {
	// SetChat selects the chat to show (called when the open chat changes
	// while the pane is visible).
	SetChat(core.ChatID) tea.Cmd
	Focus()
	Blur()
	// Update receives ActionMsg (while focused), EventMsg, ImageReadyMsg
	// and the results of its own commands.
	Update(tea.Msg) tea.Cmd
	// View renders exactly h lines of w cells.
	View(w, h int) string
}

// PinnedStrip renders the one-line pinned-message strip shown below the
// conversation header. pinned holds the loaded pinned messages (newest
// first); current is the one to show. It returns "" to show nothing.
type PinnedStrip func(ctx Context, chat core.Chat, pinned []core.Message, current, width int) string

// Forwarder opens the forward dialog for messages of the open chat.
type Forwarder func(ctx Context, ids []core.MessageID) tea.Cmd

var (
	sidebarFactory func(Context) Pane
	pinnedStrip    PinnedStrip
	forwarder      Forwarder
	chatOpenHooks  []func(core.ChatID)
	shutdownHooks  []func()
)

// RegisterSidebar installs the factory for the right sidebar pane. The App
// uses its built-in info pane when none is registered.
func RegisterSidebar(f func(Context) Pane) {
	regMu.Lock()
	defer regMu.Unlock()
	sidebarFactory = f
}

// SidebarFactory returns the registered sidebar factory, if any.
func SidebarFactory() func(Context) Pane {
	regMu.RLock()
	defer regMu.RUnlock()
	return sidebarFactory
}

// RegisterPinnedStrip installs the renderer for the pinned-message strip.
func RegisterPinnedStrip(f PinnedStrip) {
	regMu.Lock()
	defer regMu.Unlock()
	pinnedStrip = f
}

// PinnedStripRenderer returns the registered pinned strip renderer, if any.
func PinnedStripRenderer() PinnedStrip {
	regMu.RLock()
	defer regMu.RUnlock()
	return pinnedStrip
}

// RegisterForwarder installs the forward dialog used by "f" and visual "f".
// The App uses its built-in single-chat picker when none is registered.
func RegisterForwarder(f Forwarder) {
	regMu.Lock()
	defer regMu.Unlock()
	forwarder = f
}

// ForwarderFor returns the registered forwarder, if any.
func ForwarderFor() Forwarder {
	regMu.RLock()
	defer regMu.RUnlock()
	return forwarder
}

// OnChatOpened registers f to be called (on the UI goroutine) whenever a
// chat is opened in the conversation pane.
func OnChatOpened(f func(core.ChatID)) {
	if f == nil {
		return
	}
	regMu.Lock()
	defer regMu.Unlock()
	chatOpenHooks = append(chatOpenHooks, f)
}

// ChatOpened runs the OnChatOpened hooks.
func ChatOpened(id core.ChatID) {
	regMu.RLock()
	hooks := append([]func(core.ChatID){}, chatOpenHooks...)
	regMu.RUnlock()
	for _, f := range hooks {
		f(id)
	}
}

// OnShutdown registers f to run when the App quits or is closed (stop
// audio playback, delete temp files, ...). Hooks must be idempotent.
func OnShutdown(f func()) {
	if f == nil {
		return
	}
	regMu.Lock()
	defer regMu.Unlock()
	shutdownHooks = append(shutdownHooks, f)
}

// Shutdown runs the OnShutdown hooks.
func Shutdown() {
	regMu.RLock()
	hooks := append([]func(){}, shutdownHooks...)
	regMu.RUnlock()
	for _, f := range hooks {
		f()
	}
}

// LinkHandler handles a link inside warte (Signal group invite, sticker
// pack and contact links). It returns ok=false for links it does not
// handle, which the App then hands to the external opener.
type LinkHandler func(ctx Context, link string) (cmd tea.Cmd, ok bool)

var linkHandler LinkHandler

// RegisterLinkHandler installs the in-app link handler.
func RegisterLinkHandler(f LinkHandler) {
	regMu.Lock()
	defer regMu.Unlock()
	linkHandler = f
}

// LinkHandlerFor returns the registered link handler, if any.
func LinkHandlerFor() LinkHandler {
	regMu.RLock()
	defer regMu.RUnlock()
	return linkHandler
}
