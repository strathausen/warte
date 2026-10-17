// Package notify delivers desktop notifications and terminal title updates
// for the warte TUI.
//
// Notifications can be delivered through several mechanisms:
//
//   - OSC 777 (rxvt-unicode, foot, Ghostty, WezTerm)
//   - OSC 9 (iTerm2, kitty, WezTerm, Windows Terminal)
//   - OSC 99 (kitty's richer desktop-notification protocol)
//   - the operating system's native notification service (via beeep)
//   - the terminal bell
//
// Escape-sequence based methods implement [Sequencer] so that the TUI can
// emit the bytes through Bubble Tea's raw output (see [Cmd]) instead of
// writing to the terminal behind the renderer's back.
//
// All text that reaches a terminal or a notification daemon is sanitized
// with [Sanitize]: message bodies are written by other people and must never
// be able to inject escape sequences into the user's terminal.
package notify

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// Urgency is the importance hint attached to a notification.
type Urgency int

const (
	// UrgencyNormal is the default urgency for ordinary messages.
	UrgencyNormal Urgency = iota
	// UrgencyLow marks informational notifications.
	UrgencyLow
	// UrgencyCritical marks notifications that should stand out (for
	// example an incoming call).
	UrgencyCritical
)

// String returns the lower-case name of the urgency.
func (u Urgency) String() string {
	switch u {
	case UrgencyLow:
		return "low"
	case UrgencyCritical:
		return "critical"
	default:
		return "normal"
	}
}

// Notification is a single desktop notification.
//
// Title and Body are untrusted user-visible text; notifiers sanitize them
// before delivery. ChatID identifies the conversation the notification is
// about: methods that support it use it to replace an earlier notification
// for the same chat instead of stacking a new one.
type Notification struct {
	Title   string
	Body    string
	ChatID  string
	Silent  bool
	Urgency Urgency
}

// Notifier delivers notifications.
type Notifier interface {
	Notify(n Notification) error
}

// Sequencer is implemented by notifiers that work by emitting terminal
// escape sequences. Sequence returns the exact bytes to write to the
// terminal, and false if nothing should be written for n.
type Sequencer interface {
	Sequence(n Notification) (string, bool)
}

// Errors returned by notifiers.
var (
	// ErrNoWriter is returned by escape-sequence notifiers that were asked
	// to Notify without a configured io.Writer.
	ErrNoWriter = errors.New("notify: no writer configured")
	// ErrTimeout is returned when a native notification did not complete
	// within its timeout.
	ErrTimeout = errors.New("notify: native notification timed out")
	// ErrBusy is returned when a native notification is dropped because a
	// previous one is still in flight.
	ErrBusy = errors.New("notify: native notifier busy")
)

// Method identifies a notification delivery mechanism.
type Method int

const (
	// MethodAuto picks the best method for the current terminal.
	MethodAuto Method = iota
	// MethodOSC picks the best escape-sequence protocol for the current
	// terminal, falling back to OSC 9.
	MethodOSC
	// MethodOSC777 uses "ESC ] 777 ; notify ; title ; body ST".
	MethodOSC777
	// MethodOSC9 uses "ESC ] 9 ; text BEL".
	MethodOSC9
	// MethodOSC99 uses kitty's OSC 99 protocol.
	MethodOSC99
	// MethodNative uses the operating system's notification service.
	MethodNative
	// MethodBell rings the terminal bell.
	MethodBell
	// MethodOff disables notifications.
	MethodOff
)

var methodNames = map[Method]string{
	MethodAuto:   "auto",
	MethodOSC:    "osc",
	MethodOSC777: "osc777",
	MethodOSC9:   "osc9",
	MethodOSC99:  "osc99",
	MethodNative: "native",
	MethodBell:   "bell",
	MethodOff:    "off",
}

// String returns the configuration name of the method.
func (m Method) String() string {
	if s, ok := methodNames[m]; ok {
		return s
	}
	return fmt.Sprintf("Method(%d)", int(m))
}

// ParseMethod parses a configuration value. Accepted values are "auto",
// "osc", "native", "bell" and "off", plus the explicit protocols "osc9",
// "osc99" and "osc777". Matching is case-insensitive and ignores
// surrounding whitespace; the empty string means "auto".
func ParseMethod(s string) (Method, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return MethodAuto, nil
	}
	switch s {
	case "none", "disabled", "false":
		return MethodOff, nil
	case "osc 9", "osc-9":
		return MethodOSC9, nil
	case "osc 99", "osc-99", "kitty":
		return MethodOSC99, nil
	case "osc 777", "osc-777":
		return MethodOSC777, nil
	}
	for m, name := range methodNames {
		if name == s {
			return m, nil
		}
	}
	return MethodAuto, fmt.Errorf("notify: unknown method %q (want auto, osc, native, bell or off)", s)
}

// IsOSC reports whether m is a concrete escape-sequence protocol.
func (m Method) IsOSC() bool {
	return m == MethodOSC9 || m == MethodOSC99 || m == MethodOSC777
}

// Options configures [New].
type Options struct {
	// Writer receives escape sequences when Notify is called on an
	// escape-sequence notifier. It may be nil when the TUI only uses
	// Sequence (via [Cmd]).
	Writer io.Writer
	// Getenv looks up environment variables for terminal detection.
	// Defaults to os.Getenv.
	Getenv func(string) string
	// GOOS overrides the operating system used for detection; defaults to
	// runtime.GOOS.
	GOOS string
	// NativeTimeout bounds a native notification. Defaults to
	// DefaultNativeTimeout.
	NativeTimeout time.Duration
	// AppName is the application name shown by native notification
	// services. Defaults to "warte".
	AppName string
	// NativeSend overrides the function used to deliver native
	// notifications (for tests). Defaults to beeep.
	NativeSend NativeFunc
}

func (o Options) getenv() func(string) string {
	if o.Getenv != nil {
		return o.Getenv
	}
	return os.Getenv
}

// Resolve turns MethodAuto and MethodOSC into a concrete method using the
// environment described by opts. Concrete methods are returned unchanged.
func Resolve(m Method, opts Options) Method {
	env := opts.getenv()
	switch m {
	case MethodAuto:
		return DetectFor(env, opts.goos())
	case MethodOSC:
		if p, ok := DetectOSC(env); ok {
			return p
		}
		return MethodOSC9
	default:
		return m
	}
}

// New builds a Notifier for method m. MethodAuto and MethodOSC are resolved
// with [Resolve]. When running inside tmux (TMUX is set) escape sequences
// are wrapped in tmux passthrough. An automatically chosen native notifier
// falls back to the bell if the native service fails.
func New(m Method, opts Options) Notifier {
	env := opts.getenv()
	tmux := InTmux(env)
	resolved := Resolve(m, opts)
	switch resolved {
	case MethodOSC9, MethodOSC99, MethodOSC777:
		return &OSC{Protocol: resolved, Tmux: tmux, W: opts.Writer}
	case MethodNative:
		if testing.Testing() && opts.NativeSend == nil {
			// Never pop real desktop notifications from go test.
			return Off{}
		}
		nat := NewNative(opts.AppName, opts.NativeTimeout)
		if opts.NativeSend != nil {
			nat.Send = opts.NativeSend
		}
		if m == MethodAuto {
			return &Fallback{Primary: nat, Secondary: &Bell{W: opts.Writer}}
		}
		return nat
	case MethodBell:
		return &Bell{W: opts.Writer}
	default:
		return Off{}
	}
}

// Off is a Notifier that discards every notification.
type Off struct{}

// Notify does nothing.
func (Off) Notify(Notification) error { return nil }

// Sequence always returns "", false.
func (Off) Sequence(Notification) (string, bool) { return "", false }

// Fallback delivers through Primary and, if that fails, through Secondary.
type Fallback struct {
	Primary   Notifier
	Secondary Notifier
}

// Notify tries Primary then Secondary. The returned error is nil if either
// succeeded, otherwise it joins both errors.
func (f *Fallback) Notify(n Notification) error {
	err := f.Primary.Notify(n)
	if err == nil || f.Secondary == nil {
		return err
	}
	if err2 := f.Secondary.Notify(n); err2 != nil {
		return errors.Join(err, err2)
	}
	return nil
}

// ErrMsg is the Bubble Tea message produced by [Cmd] when delivery fails.
type ErrMsg struct{ Err error }

// Error implements error.
func (e ErrMsg) Error() string { return e.Err.Error() }

// Unwrap returns the underlying error.
func (e ErrMsg) Unwrap() error { return e.Err }

// Cmd returns a Bubble Tea command that delivers n through nt.
//
// Escape-sequence notifiers are emitted with tea.Raw so the bytes go through
// the program's output without corrupting the rendered frame. Other
// notifiers run inside the command (off the UI goroutine). If nt is a
// [Fallback] whose primary fails and whose secondary is a [Sequencer], the
// secondary's sequence is emitted as a raw message. Cmd returns nil if there
// is nothing to do. Delivery errors are reported as [ErrMsg].
func Cmd(nt Notifier, n Notification) tea.Cmd {
	if nt == nil {
		return nil
	}
	if s, ok := nt.(Sequencer); ok {
		seq, ok := s.Sequence(n)
		if !ok {
			return nil
		}
		return tea.Raw(seq)
	}
	if fb, ok := nt.(*Fallback); ok {
		return func() tea.Msg {
			err := fb.Primary.Notify(n)
			if err == nil {
				return nil
			}
			if s, ok := fb.Secondary.(Sequencer); ok {
				if seq, ok := s.Sequence(n); ok {
					return tea.RawMsg{Msg: seq}
				}
				return nil
			}
			if fb.Secondary == nil {
				return ErrMsg{Err: err}
			}
			if err2 := fb.Secondary.Notify(n); err2 != nil {
				return ErrMsg{Err: errors.Join(err, err2)}
			}
			return nil
		}
	}
	return func() tea.Msg {
		if err := nt.Notify(n); err != nil {
			return ErrMsg{Err: err}
		}
		return nil
	}
}
