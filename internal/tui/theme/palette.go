package theme

import (
	"hash/fnv"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
)

// AvatarCount is the number of Signal avatar colors (A100 through A210).
const AvatarCount = 12

// AvatarCodes lists Signal's avatar color names in palette order; index i of
// AvatarCodes corresponds to Palette.Avatars[i].
var AvatarCodes = [AvatarCount]string{
	"A100", "A110", "A120", "A130", "A140", "A150",
	"A160", "A170", "A180", "A190", "A200", "A210",
}

// Palette is the set of semantic colors a theme provides. Every field is
// non-nil for every registered theme; the "mono" theme uses lipgloss.NoColor
// throughout so the terminal's own colors show through.
type Palette struct {
	// Surfaces.
	Bg         color.Color // main background
	BgAlt      color.Color // panels, status bar, incoming bubbles, code
	BgSelected color.Color // selected list rows
	Shadow     color.Color // drop shadow under dialogs

	// Text.
	Fg       color.Color // primary text
	FgMuted  color.Color // secondary text: previews, timestamps
	FgSubtle color.Color // tertiary text: placeholders, hints, decorations

	// Borders.
	Border      color.Color // unfocused pane borders
	BorderFocus color.Color // focused pane borders

	// Accents.
	Accent    color.Color // primary brand accent
	AccentAlt color.Color // secondary accent

	// Status.
	Success color.Color
	Warning color.Color
	Error   color.Color
	Info    color.Color

	// Inline content.
	Link          color.Color
	Mention       color.Color // @mention of someone else
	MentionSelfBg color.Color // background behind an @mention of the user
	Spoiler       color.Color // fill color hiding spoiler text
	Selection     color.Color // background of a selected message / text

	// Message bubbles.
	BubbleIn    color.Color
	BubbleOut   color.Color
	BubbleInFg  color.Color
	BubbleOutFg color.Color

	// Unread badge.
	UnreadBg color.Color
	UnreadFg color.Color

	// Avatars holds theme-appropriate hues for Signal's avatar colors, in
	// AvatarCodes order (A100 … A210).
	Avatars [AvatarCount]color.Color

	// Status-line mode badge colors.
	ModeNormal  color.Color
	ModeInsert  color.Color
	ModeVisual  color.Color
	ModeCommand color.Color
	ModeSearch  color.Color
}

// AvatarIndex maps a Signal avatar color name ("A100" … "A210", case
// insensitive) to its index in Palette.Avatars.
func AvatarIndex(code string) (int, bool) {
	code = strings.ToUpper(strings.TrimSpace(code))
	for i, c := range AvatarCodes {
		if c == code {
			return i, true
		}
	}
	return 0, false
}

// senderIndex hashes an identifier (ACI, phone number or display name) into
// the avatar palette. FNV-1a is stable across runs and platforms.
func senderIndex(id string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	return int(h.Sum32() % AvatarCount)
}

// SenderColor deterministically maps an identifier (preferably the sender's
// ACI, otherwise a name) to one of the palette's avatar colors.
func (p Palette) SenderColor(id string) color.Color {
	return p.Avatars[senderIndex(id)]
}

// AvatarColor returns the color for a Signal avatar color name such as
// "A130". If code is empty or unknown it falls back to SenderColor(fallbackID).
func (p Palette) AvatarColor(code, fallbackID string) color.Color {
	if i, ok := AvatarIndex(code); ok {
		return p.Avatars[i]
	}
	return p.SenderColor(fallbackID)
}

// spec is the compact, human-curated description of a theme from which a
// full Palette is derived. All colors are "#rrggbb".
type spec struct {
	bg, bgAlt, bgSel, shadow  string
	fg, fgMuted, fgSubtle     string
	border                    string
	accent, accentAlt, bubble string // bubble: hue tinted into outgoing bubbles

	red, orange, yellow, green, teal, cyan     string
	sky, blue, lavender, purple, pink, magenta string

	link string // optional; defaults to blue
}

// Minimum WCAG contrast ratios enforced when deriving palettes.
const (
	minText   = 4.5 // body text
	minAccent = 3.0 // large/bold text, icons, accents
	minSubtle = 2.2 // purely decorative text (placeholders, hints)
)

// build derives a full Palette from a spec, nudging any color that would be
// unreadable against its background while keeping the theme's hues.
func build(s spec) Palette {
	bg := parseHex(s.bg)
	bgAlt := parseHex(s.bgAlt)
	bgSel := parseHex(s.bgSel)
	fg := ensureContrast(parseHex(s.fg), bg, minText)
	fg = ensureContrast(fg, bgSel, minText)
	on := func(hex string, min float64) rgb { return ensureContrast(parseHex(hex), bg, min) }

	accent := on(s.accent, minAccent)
	accentAlt := on(s.accentAlt, minAccent)
	bubbleHue := accentAlt
	if s.bubble != "" {
		bubbleHue = parseHex(s.bubble)
	}
	link := s.link
	if link == "" {
		link = s.blue
	}

	bubbleIn := bgAlt
	bubbleOut := tint(bubbleHue, bg, fg, 0.22, minText)
	unreadBg := accent

	shadow := mix(bg, black, 0.45)
	if s.shadow != "" {
		shadow = parseHex(s.shadow)
	}

	hues := [AvatarCount]string{
		s.lavender, // A100 indigo
		s.blue,     // A110 blue
		s.sky,      // A120 sky
		s.green,    // A130 green
		s.purple,   // A140 violet
		s.magenta,  // A150 magenta
		s.pink,     // A160 pink
		s.red,      // A170 red
		s.yellow,   // A180 yellow
		s.orange,   // A190 orange
		s.teal,     // A200 teal
		s.cyan,     // A210 cyan
	}
	var avatars [AvatarCount]color.Color
	for i, h := range hues {
		avatars[i] = on(h, minAccent).color()
	}

	return Palette{
		Bg:         bg.color(),
		BgAlt:      bgAlt.color(),
		BgSelected: bgSel.color(),
		Shadow:     shadow.color(),

		Fg:       fg.color(),
		FgMuted:  on(s.fgMuted, minText).color(),
		FgSubtle: on(s.fgSubtle, minSubtle).color(),

		Border:      parseHex(s.border).color(),
		BorderFocus: accent.color(),

		Accent:    accent.color(),
		AccentAlt: accentAlt.color(),

		Success: on(s.green, minAccent).color(),
		Warning: on(s.yellow, minAccent).color(),
		Error:   on(s.red, minAccent).color(),
		Info:    on(s.cyan, minAccent).color(),

		Link:          on(link, minAccent).color(),
		Mention:       accent.color(),
		MentionSelfBg: tint(accent, bg, fg, 0.35, minText).color(),
		Spoiler:       mix(parseHex(s.fgSubtle), bg, 0.35).color(),
		Selection:     tint(accent, bg, fg, 0.28, minText).color(),

		BubbleIn:    bubbleIn.color(),
		BubbleOut:   bubbleOut.color(),
		BubbleInFg:  ensureContrast(fg, bubbleIn, minText).color(),
		BubbleOutFg: ensureContrast(fg, bubbleOut, minText).color(),

		UnreadBg: unreadBg.color(),
		UnreadFg: readableOn(unreadBg, bg, fg).color(),

		Avatars: avatars,

		ModeNormal:  accent.color(),
		ModeInsert:  on(s.green, minAccent).color(),
		ModeVisual:  on(s.purple, minAccent).color(),
		ModeCommand: on(s.orange, minAccent).color(),
		ModeSearch:  on(s.cyan, minAccent).color(),
	}
}

// monoPalette returns a palette with no colors at all: every entry is
// lipgloss.NoColor so text renders in the terminal's default colors.
func monoPalette() Palette {
	n := lipgloss.NoColor{}
	var av [AvatarCount]color.Color
	for i := range av {
		av[i] = n
	}
	return Palette{
		Bg: n, BgAlt: n, BgSelected: n, Shadow: n,
		Fg: n, FgMuted: n, FgSubtle: n,
		Border: n, BorderFocus: n,
		Accent: n, AccentAlt: n,
		Success: n, Warning: n, Error: n, Info: n,
		Link: n, Mention: n, MentionSelfBg: n, Spoiler: n, Selection: n,
		BubbleIn: n, BubbleOut: n, BubbleInFg: n, BubbleOutFg: n,
		UnreadBg: n, UnreadFg: n,
		Avatars:    av,
		ModeNormal: n, ModeInsert: n, ModeVisual: n, ModeCommand: n, ModeSearch: n,
	}
}
