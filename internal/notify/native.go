package notify

import (
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gen2brain/beeep"
)

// DefaultNativeTimeout bounds how long a native notification may take.
const DefaultNativeTimeout = 5 * time.Second

// NativeFunc delivers one native notification. urgent requests a more
// prominent notification (with sound where supported).
type NativeFunc func(title, body string, urgent bool) error

var beeepMu sync.Mutex

// beeepSend delivers through beeep, setting its global application name.
func beeepSend(appName string) NativeFunc {
	return func(title, body string, urgent bool) error {
		// beeep.AppName is a package global read during delivery; hold the
		// lock for the whole call so concurrent senders never race on it.
		beeepMu.Lock()
		defer beeepMu.Unlock()
		beeep.AppName = appName
		if urgent && alertSafe(runtime.GOOS) {
			return beeep.Alert(title, body, "")
		}
		return beeep.Notify(title, body, "")
	}
}

// alertSafe reports whether beeep.Alert is safe to call from a TUI. On
// Linux and the BSDs it falls back to writing BEL straight to os.Stdout
// from the delivery goroutine, bypassing the renderer; a BEL landing inside
// an OSC sequence of the frame (hyperlink, title) would terminate it and
// corrupt the screen.
func alertSafe(goos string) bool { return goos == "darwin" || goos == "windows" }

// Native delivers notifications through the operating system's notification
// service (Notification Center, libnotify/D-Bus, Windows toast).
//
// Each Notify runs the delivery on its own goroutine and waits at most
// Timeout for it. At most one delivery is in flight at a time: while one is
// still running (even past its timeout) further notifications are dropped
// with ErrBusy, so a hung notification daemon can never pile up goroutines.
type Native struct {
	// Send performs the delivery. Defaults to beeep.
	Send NativeFunc
	// Timeout bounds a single delivery. Zero means DefaultNativeTimeout.
	Timeout time.Duration
	// GOOS selects platform-specific escaping; defaults to runtime.GOOS.
	GOOS string

	busy chan struct{}
	once sync.Once
}

// NewNative returns a Native notifier using beeep. An empty appName
// defaults to "warte"; a zero timeout to DefaultNativeTimeout.
func NewNative(appName string, timeout time.Duration) *Native {
	if appName == "" {
		appName = "warte"
	}
	return &Native{Send: beeepSend(appName), Timeout: timeout}
}

// Notify delivers n, blocking for at most the configured timeout.
func (nt *Native) Notify(n Notification) error {
	nt.once.Do(func() { nt.busy = make(chan struct{}, 1) })
	title, body, ok := nt.prepare(n)
	if !ok {
		return nil
	}
	select {
	case nt.busy <- struct{}{}:
	default:
		return ErrBusy
	}
	send := nt.Send
	if send == nil {
		send = beeepSend("warte")
	}
	timeout := nt.Timeout
	if timeout <= 0 {
		timeout = DefaultNativeTimeout
	}
	done := make(chan error, 1)
	urgent := n.Urgency == UrgencyCritical && !n.Silent
	go func() {
		defer func() { <-nt.busy }()
		done <- send(title, body, urgent)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		return ErrTimeout
	}
}

// prepare sanitizes and escapes n for the native service.
//
// beeep hands the text to platform tools that each have their own
// injection hazards, so on top of Sanitize the text is escaped per
// platform:
//
//   - darwin: beeep falls back to osascript with the text quoted by Go's
//     %q. Runes that %q escapes (\uXXXX) are not valid AppleScript string
//     escapes and make the script fail, so non-printable runes (e.g. ZWJ in
//     emoji sequences) are dropped. Values starting with '-' or '[' are
//     prefixed with a space so terminal-notifier never reads them as
//     options.
//   - linux/BSD: the freedesktop body may be interpreted as markup, so it is
//     entity-escaped; values starting with '-' get a zero-width space so the
//     notify-send fallback does not parse them as options.
//   - windows: go-toast embeds the text in a CDATA section of an XML
//     template that its PowerShell fallback places in a double-quoted
//     here-string. "]]>" would end the CDATA section (XML injection) and
//     '$' / '`' would be expanded by PowerShell ("$(...)" runs code), so
//     those are neutralized.
func (nt *Native) prepare(n Notification) (title, body string, ok bool) {
	title = Sanitize(n.Title, MaxTitleRunes)
	body = Sanitize(n.Body, MaxBodyRunes)
	if title == "" && body == "" {
		return "", "", false
	}
	if title == "" {
		title, body = body, ""
	}
	switch nt.goos() {
	case "darwin":
		title, body = darwinSafe(title), darwinSafe(body)
	case "linux", "freebsd", "openbsd", "netbsd", "dragonfly", "illumos":
		title, body = guardLeading(title, "\u200b"), guardLeading(escapeMarkup(body), "\u200b")
	case "windows":
		title, body = windowsSafe(title), windowsSafe(body)
	}
	return title, body, true
}

func (nt *Native) goos() string {
	if nt.GOOS != "" {
		return nt.GOOS
	}
	return runtime.GOOS
}

func guardLeading(s, guard string) string {
	if strings.HasPrefix(s, "-") || strings.HasPrefix(s, "[") {
		return guard + s
	}
	return s
}

// darwinSafe drops runes that strconv.Quote would escape and guards
// option-like prefixes.
func darwinSafe(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == ' ' || strconv.IsPrint(r) {
			return r
		}
		return -1
	}, s)
	return guardLeading(strings.TrimSpace(s), " ")
}

var windowsReplacer = strings.NewReplacer(
	"]]>", "]] >",
	"$", "\uff04", // FULLWIDTH DOLLAR SIGN
	"`", "\u02cb", // MODIFIER LETTER GRAVE ACCENT
)

func windowsSafe(s string) string { return windowsReplacer.Replace(s) }

var markupReplacer = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func escapeMarkup(s string) string { return markupReplacer.Replace(s) }
