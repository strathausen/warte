// Package termimg renders images in the terminal for warte's Bubble Tea UI.
//
// It supports four output strategies, picked by [Detect] (environment
// sniffing), refined by optional active probes ([QueryKittySupport],
// [QuerySixelSupport]) and overridable from config via [Parse]:
//
//   - [Kitty]: the kitty graphics protocol with Unicode placeholders
//     (U+10EEEE). The image is transmitted once, out of band, and the visible
//     cells are ordinary text, so it scrolls, clips and redraws correctly with
//     a cell-based renderer such as Bubble Tea's.
//   - [ITerm2]: OSC 1337 inline images, drawn at the cursor position as an
//     overlay after the frame has been rendered.
//   - [Sixel]: DEC sixel graphics from a built-in encoder, also drawn as an
//     overlay.
//   - [HalfBlocks]: truecolor (or 256-color) '▀' cells that work everywhere.
//
// The [Renderer] ties these together behind a single call that returns rows
// to embed in a layout plus any raw escape payload that must be written to the
// terminal outside the render string (for example with tea.Raw).
//
// Helpers for decoding (PNG, JPEG, GIF with all frames, WebP), resizing,
// BlurHash placeholders and braille waveforms live here too.
package termimg

import (
	"fmt"
	"os"
	"strings"
)

// Protocol is a terminal image output strategy.
type Protocol int

// Supported protocols. [Auto] is only meaningful as a configuration value; it
// is resolved to a concrete protocol by [Resolve].
const (
	// None disables images entirely; callers show a textual stand-in.
	None Protocol = iota
	// HalfBlocks renders '▀' cells with foreground/background colors.
	HalfBlocks
	// Sixel uses DEC sixel graphics.
	Sixel
	// ITerm2 uses the iTerm2 OSC 1337 inline image protocol.
	ITerm2
	// Kitty uses the kitty graphics protocol with Unicode placeholders.
	Kitty
	// Auto asks for detection. It is never returned by [Detect].
	Auto Protocol = -1
)

// String returns the configuration spelling of p.
func (p Protocol) String() string {
	switch p {
	case Auto:
		return "auto"
	case None:
		return "off"
	case HalfBlocks:
		return "blocks"
	case Sixel:
		return "sixel"
	case ITerm2:
		return "iterm2"
	case Kitty:
		return "kitty"
	default:
		return fmt.Sprintf("Protocol(%d)", int(p))
	}
}

// Overlay reports whether images in this protocol are drawn at the cursor
// position (and therefore need an overlay pass after the frame renders)
// rather than being part of the cell text.
func (p Protocol) Overlay() bool { return p == ITerm2 || p == Sixel }

// Parse converts a config value ("auto", "kitty", "iterm2", "sixel",
// "blocks", "off") into a Protocol. Matching is case-insensitive and a few
// common aliases are accepted ("halfblocks", "none", "false", "iterm", "").
// An empty string means auto.
func Parse(s string) (Protocol, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "auto":
		return Auto, nil
	case "kitty":
		return Kitty, nil
	case "iterm2", "iterm":
		return ITerm2, nil
	case "sixel":
		return Sixel, nil
	case "blocks", "halfblocks", "half-blocks", "unicode":
		return HalfBlocks, nil
	case "off", "none", "false", "no":
		return None, nil
	default:
		return None, fmt.Errorf("termimg: unknown image protocol %q (want auto|kitty|iterm2|sixel|blocks|off)", s)
	}
}

// Env looks up an environment variable; os.Getenv satisfies it. A nil Env
// behaves like an empty environment.
type Env func(key string) string

// MapEnv adapts a map to [Env], which is convenient in tests.
func MapEnv(m map[string]string) Env {
	return func(k string) string { return m[k] }
}

// Detection is the result of environment sniffing.
type Detection struct {
	// Protocol is the best protocol the environment suggests.
	Protocol Protocol
	// Terminal is a short name of the detected terminal ("kitty",
	// "ghostty", "wezterm", "iterm2", "foot", ...), or "" when unknown.
	Terminal string
	// Tmux is true when running inside tmux. Graphics escape sequences must
	// then be wrapped with [TmuxPassthrough], and tmux needs
	// "set -g allow-passthrough on".
	Tmux bool
	// TrueColor reports whether 24-bit color is likely available.
	TrueColor bool
	// SixelCandidate is true for terminals that may support sixel but need a
	// DA1 probe to confirm (for example xterm, which only supports sixel
	// when started with a VT340 terminal id).
	SixelCandidate bool
}

// DetectFromOS runs [Detect] against the process environment.
func DetectFromOS() Detection { return Detect(os.Getenv) }

// Detect guesses the best image protocol from environment variables alone.
//
// Heuristics, in order:
//
//   - KITTY_WINDOW_ID or TERM=xterm-kitty → Kitty.
//   - TERM_PROGRAM=ghostty, GHOSTTY_RESOURCES_DIR or TERM=xterm-ghostty →
//     Kitty (Ghostty implements Unicode placeholders).
//   - TERM_PROGRAM=WezTerm → ITerm2. WezTerm's kitty graphics support does
//     not reliably implement Unicode placeholders, while its iTerm2 support is
//     solid; users can force "kitty" in config.
//   - TERM_PROGRAM=iTerm.app or LC_TERMINAL=iTerm2 → ITerm2; mintty → ITerm2.
//   - TERM=foot*, mlterm*, contour*, TERM_PROGRAM=contour, WT_SESSION
//     (Windows Terminal), KONSOLE_VERSION → Sixel.
//   - XTERM_VERSION → HalfBlocks with SixelCandidate set; confirm with
//     [QuerySixelSupport].
//   - TERM=dumb or no TERM → None; anything else → HalfBlocks.
//
// Inside tmux (TMUX set), the outer terminal is inferred from inherited
// variables (KITTY_WINDOW_ID, GHOSTTY_RESOURCES_DIR, LC_TERMINAL, ...).
// Kitty placeholders work through tmux because the cells are plain text and
// only the transmission needs passthrough. Cursor-positioned protocols
// (iTerm2, sixel) do not survive tmux's own redraws, so they degrade to
// HalfBlocks in tmux unless forced in config.
func Detect(env Env) Detection {
	if env == nil {
		env = func(string) string { return "" }
	}
	term := strings.ToLower(env("TERM"))
	prog := strings.ToLower(env("TERM_PROGRAM"))
	colorterm := strings.ToLower(env("COLORTERM"))
	d := Detection{
		Tmux:      env("TMUX") != "" || prog == "tmux" || strings.HasPrefix(term, "tmux"),
		TrueColor: colorterm == "truecolor" || colorterm == "24bit",
	}
	if d.Tmux && prog == "tmux" {
		prog = "" // the outer program is unknown
	}

	set := func(name string, p Protocol, truecolor bool) Detection {
		d.Terminal = name
		d.Protocol = p
		d.TrueColor = d.TrueColor || truecolor
		return d
	}

	switch {
	case env("KITTY_WINDOW_ID") != "" || term == "xterm-kitty":
		d = set("kitty", Kitty, true)
	case prog == "ghostty" || env("GHOSTTY_RESOURCES_DIR") != "" || term == "xterm-ghostty":
		d = set("ghostty", Kitty, true)
	case prog == "wezterm" || env("WEZTERM_EXECUTABLE") != "" || term == "wezterm":
		d = set("wezterm", ITerm2, true)
	case prog == "iterm.app" || env("LC_TERMINAL") == "iTerm2" || env("ITERM_SESSION_ID") != "":
		d = set("iterm2", ITerm2, true)
	case prog == "mintty" || term == "mintty":
		d = set("mintty", ITerm2, true)
	case strings.HasPrefix(term, "foot"):
		d = set("foot", Sixel, true)
	case strings.HasPrefix(term, "mlterm") || env("MLTERM") != "":
		d = set("mlterm", Sixel, true)
	case strings.HasPrefix(term, "contour") || prog == "contour":
		d = set("contour", Sixel, true)
	case env("WT_SESSION") != "":
		d = set("windows-terminal", Sixel, true)
	case env("KONSOLE_VERSION") != "":
		d = set("konsole", Sixel, true)
	case prog == "apple_terminal":
		d = set("apple-terminal", HalfBlocks, false)
	case env("XTERM_VERSION") != "":
		d = set("xterm", HalfBlocks, false)
		d.SixelCandidate = true
	case term == "" || term == "dumb":
		d = set("", None, false)
	default:
		d = set("", HalfBlocks, false)
		if strings.Contains(term, "direct") || strings.Contains(term, "truecolor") {
			d.TrueColor = true
		}
	}

	if d.Tmux && d.Protocol.Overlay() {
		d.Protocol = HalfBlocks
	}
	return d
}

// Resolve turns a configured protocol into a concrete one. A non-Auto
// configured value always wins. For Auto, the environment detection is used
// and refined by probe results when available: for an otherwise unidentified
// terminal (block fallback), a positive kitty probe selects Kitty, else a
// positive sixel DA1 probe selects Sixel (outside tmux). Terminals identified
// from the environment are not overridden by the probe, because some (WezTerm,
// Konsole) answer the kitty query without implementing Unicode placeholders.
func Resolve(configured Protocol, d Detection, probe *ProbeResult) Protocol {
	if configured != Auto {
		return configured
	}
	p := d.Protocol
	if probe == nil {
		return p
	}
	if probe.Kitty && p == HalfBlocks {
		return Kitty
	}
	if probe.Sixel && p == HalfBlocks && !d.Tmux {
		return Sixel
	}
	return p
}
