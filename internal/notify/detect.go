package notify

import (
	"runtime"
	"strings"
)

func (o Options) goos() string {
	if o.GOOS != "" {
		return o.GOOS
	}
	return runtime.GOOS
}

// Terminal identifies a terminal emulator family relevant for notification
// support.
type Terminal int

// Known terminals.
const (
	TerminalUnknown Terminal = iota
	TerminalKitty
	TerminalITerm2
	TerminalWezTerm
	TerminalWindowsTerminal
	TerminalGhostty
	TerminalFoot
	TerminalRxvt
)

// String returns a human-readable terminal name.
func (t Terminal) String() string {
	switch t {
	case TerminalKitty:
		return "kitty"
	case TerminalITerm2:
		return "iTerm2"
	case TerminalWezTerm:
		return "WezTerm"
	case TerminalWindowsTerminal:
		return "Windows Terminal"
	case TerminalGhostty:
		return "Ghostty"
	case TerminalFoot:
		return "foot"
	case TerminalRxvt:
		return "rxvt-unicode"
	default:
		return "unknown"
	}
}

// DetectTerminal identifies the terminal emulator from the environment.
//
// Inside tmux or screen TERM_PROGRAM and TERM describe the multiplexer, so
// detection also relies on variables that terminals export and that the
// multiplexer inherits (KITTY_WINDOW_ID, ITERM_SESSION_ID, LC_TERMINAL,
// WEZTERM_PANE, WT_SESSION, GHOSTTY_RESOURCES_DIR).
func DetectTerminal(getenv func(string) string) Terminal {
	termProgram := strings.ToLower(getenv("TERM_PROGRAM"))
	term := strings.ToLower(getenv("TERM"))
	switch {
	case getenv("KITTY_WINDOW_ID") != "" || term == "xterm-kitty" || termProgram == "kitty":
		return TerminalKitty
	case termProgram == "ghostty" || term == "xterm-ghostty" || getenv("GHOSTTY_RESOURCES_DIR") != "":
		return TerminalGhostty
	case termProgram == "wezterm" || getenv("WEZTERM_PANE") != "" || getenv("WEZTERM_EXECUTABLE") != "":
		return TerminalWezTerm
	case termProgram == "iterm.app" || strings.EqualFold(getenv("LC_TERMINAL"), "iTerm2") ||
		getenv("ITERM_SESSION_ID") != "":
		return TerminalITerm2
	case getenv("WT_SESSION") != "":
		return TerminalWindowsTerminal
	case term == "foot" || strings.HasPrefix(term, "foot-"):
		return TerminalFoot
	case strings.HasPrefix(term, "rxvt-unicode") || strings.HasPrefix(term, "rxvt"):
		return TerminalRxvt
	}
	return TerminalUnknown
}

// InTmux reports whether the process runs inside tmux.
func InTmux(getenv func(string) string) bool {
	return getenv("TMUX") != ""
}

// DetectOSC returns the best escape-sequence protocol for the terminal, or
// false if the terminal is not known to support one.
func DetectOSC(getenv func(string) string) (Method, bool) {
	switch DetectTerminal(getenv) {
	case TerminalKitty:
		return MethodOSC99, true
	case TerminalITerm2, TerminalWezTerm, TerminalWindowsTerminal:
		return MethodOSC9, true
	case TerminalGhostty, TerminalFoot, TerminalRxvt:
		return MethodOSC777, true
	}
	return MethodOff, false
}

// Detect picks the best notification method for the current OS and the
// environment described by getenv.
func Detect(getenv func(string) string) Method {
	return DetectFor(getenv, runtime.GOOS)
}

// DetectFor picks the best notification method: kitty → OSC 99; iTerm2,
// WezTerm, Windows Terminal → OSC 9; Ghostty, foot, rxvt → OSC 777;
// otherwise native notifications when goos has a supported service and the
// session is local; otherwise the bell.
//
// Over SSH native notifications would appear on the remote machine, so the
// bell is used instead (escape sequences still reach the local terminal).
func DetectFor(getenv func(string) string, goos string) Method {
	if m, ok := DetectOSC(getenv); ok {
		return m
	}
	if nativeSupported(goos) && !isRemote(getenv) && hasDisplay(getenv, goos) {
		return MethodNative
	}
	return MethodBell
}

func nativeSupported(goos string) bool {
	switch goos {
	case "darwin", "windows", "linux", "freebsd", "openbsd", "netbsd", "dragonfly":
		return true
	}
	return false
}

func isRemote(getenv func(string) string) bool {
	return getenv("SSH_CONNECTION") != "" || getenv("SSH_CLIENT") != "" || getenv("SSH_TTY") != ""
}

// hasDisplay reports whether a graphical session is available. On
// freedesktop systems notifications need a session bus or display.
func hasDisplay(getenv func(string) string, goos string) bool {
	switch goos {
	case "darwin", "windows":
		return true
	}
	return getenv("DBUS_SESSION_BUS_ADDRESS") != "" || getenv("WAYLAND_DISPLAY") != "" ||
		getenv("DISPLAY") != ""
}
