// Package ui is the small shared contract between the warte TUI shell
// (package tui) and feature views that live in their own packages under
// internal/tui/views/... It exists to avoid import cycles: views import ui,
// tui imports ui (and, from cmd/warte, the views), never the other way round.
//
// A view is an Overlay: a floating dialog the App draws centered over the
// dimmed main screen. Views talk back to the App only through tea.Msgs
// defined here (CloseOverlayMsg, ToastMsg, OpenChatMsg, ...), and reach the
// backend, theme, config and image pipeline through a Context.
//
// See docs/TUI.md for a walkthrough and an example overlay.
package ui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"warte/internal/config"
	"warte/internal/core"
	"warte/internal/tui/keymap"
	"warte/internal/tui/theme"
)

// Overlay is a floating view (dialog, picker, viewer) drawn above the main
// screen. The App owns a stack of overlays; only the top one receives input.
//
// Key handling: the App feeds every key press to the keymap engine in the
// context keymap.Overlay(KeyContext()) (which falls back to the bindings
// shared by all overlays, then to Normal mode's global ones). A fired binding
// reaches the overlay as an ActionMsg; keys that are not bound arrive as the
// original tea.KeyPressMsg. keymap.ActBack ("q", "esc") closes the overlay
// unless it implements BackHandler and handles it. Overlays that contain a
// text input implement KeyCapturer to receive raw key presses instead.
type Overlay interface {
	// Init is called once when the overlay is pushed.
	Init() tea.Cmd
	// Update handles messages: ActionMsg, tea.KeyPressMsg, tea.MouseMsg (with
	// coordinates relative to the overlay's content area), the results of
	// the overlay's own commands and broadcast messages such as EventMsg.
	// Return the (possibly new) overlay, or nil to close it (the returned
	// command still runs).
	Update(tea.Msg) (Overlay, tea.Cmd)
	// View renders the content area. The result must be at most width cells
	// wide and height lines tall; the App adds the border, title and shadow.
	View(width, height int) string
	// Title is shown in the dialog border. Untrusted parts must be sanitized
	// (theme.SanitizeLine).
	Title() string
	// KeyContext names the keymap overlay context, e.g. keymap.OverlayHelp.
	// The engine looks bindings up in keymap.Overlay(KeyContext()).
	KeyContext() string
	// Size returns the preferred content size given the maximum available
	// content area. The App clamps the result to [1, max].
	Size(maxW, maxH int) (w, h int)
}

// BackHandler is implemented by overlays that want to intercept "back"
// (q / esc), e.g. to clear a filter first. Returning handled=false lets the
// App close the overlay.
type BackHandler interface {
	HandleBack() (handled bool, cmd tea.Cmd)
}

// KeyCapturer is implemented by overlays that currently want every key press
// raw (for example while a text field has focus). While CaptureKeys returns
// true the keymap engine is bypassed, so the overlay must handle esc itself
// (typically by returning CloseOverlay or leaving its input).
type KeyCapturer interface {
	CaptureKeys() bool
}

// Footer is implemented by overlays that want key hints in the dialog's
// bottom border (e.g. "enter select · esc close").
type Footer interface {
	Footer() string
}

// ActionMsg delivers a fired keymap binding to the top overlay.
type ActionMsg struct {
	Action keymap.Action
	// Count is the numeric prefix (1 when none was typed).
	Count int
	// Keys is the typed sequence for display.
	Keys string
}

// CloseOverlayMsg pops the top overlay.
type CloseOverlayMsg struct{}

// CloseOverlay is a tea.Cmd that pops the top overlay.
func CloseOverlay() tea.Msg { return CloseOverlayMsg{} }

// PushOverlayMsg pushes an overlay on top of the stack.
type PushOverlayMsg struct{ Overlay Overlay }

// Push returns a command that pushes o.
func Push(o Overlay) tea.Cmd { return func() tea.Msg { return PushOverlayMsg{Overlay: o} } }

// ToastLevel is the severity of a toast.
type ToastLevel int

const (
	ToastInfo ToastLevel = iota
	ToastSuccess
	ToastWarning
	ToastError
)

// ToastMsg shows a transient notification in the corner of the screen.
type ToastMsg struct {
	Level ToastLevel
	Text  string
}

// Toast returns a command that shows a toast.
func Toast(level ToastLevel, text string) tea.Cmd {
	return func() tea.Msg { return ToastMsg{Level: level, Text: text} }
}

// OpenChatMsg opens a chat in the conversation pane, optionally jumping to
// (and selecting) a message.
type OpenChatMsg struct {
	Chat  core.ChatID
	Focus *core.MessageID
}

// ReplyToMsg starts a reply to a message in the composer (and enters Insert
// mode). The message must belong to the open chat, or the chat is opened.
type ReplyToMsg struct{ ID core.MessageID }

// ComposeInsertMsg inserts text at the composer's cursor and enters Insert
// mode (e.g. an emoji chosen in a picker).
type ComposeInsertMsg struct{ Text string }

// RunCommandMsg runs an ex command line as if typed after ":" (without the
// colon), e.g. "mute 8h".
type RunCommandMsg struct{ Line string }

// ReactMsg toggles a reaction on a message (sent by reaction pickers).
// Remove is resolved by the App when Toggle is set: if you already reacted
// with Emoji the reaction is removed.
type ReactMsg struct {
	ID     core.MessageID
	Emoji  string
	Toggle bool
}

// EventMsg wraps a backend event. The App handles every event itself and also
// forwards it to the top overlay so views can refresh.
type EventMsg struct{ Event core.Event }

// ImageReadyMsg is broadcast to the top overlay when an image requested
// through Images finished loading, so the overlay can re-render if it cares.
// The App handles the image itself; overlays usually ignore this.
type ImageReadyMsg struct{ Key string }

// ScreenLockMsg is sent to the top overlay when the screen lock engages.
// Overlays that play on their own (audio, story auto-advance) pause, so
// nothing plays, advances or is marked viewed while nobody can see it.
// Nothing is sent on unlock: the user resumes.
type ScreenLockMsg struct{}

// Context is handed to view constructors. All pointer fields stay valid for
// the App's lifetime; Styles is updated in place on a live theme switch, so
// always read colors through it (Styles.Theme) rather than copying them.
type Context struct {
	Svc    core.Service
	Styles *theme.Styles
	Icons  theme.IconSet
	// Theme is the theme at the time the Context was made. Prefer
	// Styles.Theme, which follows live theme switches.
	Theme  theme.Theme
	Config config.Config
	Images Images
	Keys   *keymap.Engine
	Now    func() time.Time
	// Chat returns the chat open in the conversation pane ("" if none).
	Chat func() core.ChatID
	// Selected returns the selected message in the conversation, or nil.
	Selected func() *core.Message
	// Self is the local account.
	Self core.Contact
}

// Images gives views access to the App's image pipeline: terminal protocol
// detection, async fetching through the Service, decoding off the UI
// goroutine and caching of rendered rows.
type Images interface {
	// Enabled reports whether images can be shown at all (protocol != off).
	Enabled() bool
	// Inline reports whether real images can be drawn inside the cell
	// layout (kitty placeholders or half-blocks).
	Inline() bool
	// Size returns the cell area the image occupies when fitted into
	// maxCols x maxRows (using pixel dimensions from the ref when known).
	Size(ref ImageRef, maxCols, maxRows int) (cols, rows int)
	// Rows returns exactly rows strings of exactly cols cells. When the
	// image is not ready yet it returns a placeholder (blurhash or a styled
	// box) and ready=false, and schedules the fetch automatically; the App
	// re-renders when it arrives (and sends ImageReadyMsg to the top overlay).
	Rows(ref ImageRef, cols, rows int) (lines []string, ready bool)
}

// ImageKind selects where image bytes come from.
type ImageKind int

const (
	ImageAttachment ImageKind = iota // Service.Attachment(Message, Attachment.ID)
	ImageAvatar                      // Service.Avatar(AvatarID)
	ImageSticker                     // Service.StickerImage(PackID, StickerID)
)

// ImageRef identifies an image for Images.
type ImageRef struct {
	Kind       ImageKind
	Message    core.MessageID
	Attachment core.Attachment // for attachments (and sticker metadata)
	AvatarID   string
	PackID     string
	StickerID  uint32
	// Label is shown in the placeholder box when there is no blurhash
	// (e.g. "Photo"). Optional.
	Label string
}

// Key returns a stable cache key for the image source (without size).
func (r ImageRef) Key() string {
	switch r.Kind {
	case ImageAvatar:
		return "av:" + r.AvatarID
	case ImageSticker:
		return "st:" + r.PackID + ":" + itoa(int(r.StickerID))
	default:
		return "at:" + r.Message.String() + ":" + r.Attachment.ID
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
