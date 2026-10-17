package theme

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// Theme is a named palette plus metadata.
type Theme struct {
	// Name is the registry name, e.g. "catppuccin-mocha".
	Name string
	// Palette holds the theme's semantic colors.
	Palette Palette
	// Dark reports whether the theme is designed for a dark background.
	Dark bool
	// Mono marks the colorless theme, whose styles rely only on bold,
	// reverse, faint, italic and underline attributes.
	Mono bool
}

// Well-known theme names.
const (
	// Default is the theme used when none is configured.
	Default = "charm"
	// AutoName selects charm or charm-light from the terminal background.
	AutoName = "auto"
	// MonoName is the colorless theme, also forced by NO_COLOR.
	MonoName = "mono"
	// CharmLight is the light variant of the default theme.
	CharmLight = "charm-light"
)

// specs holds the curated source colors for every colored theme.
var specs = map[string]struct {
	dark bool
	s    spec
}{
	"charm": {true, spec{
		bg: "#1b1a23", bgAlt: "#24222f", bgSel: "#36314c", shadow: "#0e0d13",
		fg: "#ece8f5", fgMuted: "#a8a1bf", fgSubtle: "#6f6889",
		border: "#3e3a54", accent: "#ff5f87", accentAlt: "#7d56f4", bubble: "#7d56f4",
		red: "#ff4d6d", orange: "#ff875f", yellow: "#ecfd65", green: "#04b575",
		teal: "#00d7af", cyan: "#5fd7ff", sky: "#87afff", blue: "#6b8cff",
		lavender: "#b9a8ff", purple: "#9b7bff", pink: "#ee6ff8", magenta: "#ff6ad5",
		link: "#5fd7ff",
	}},
	"charm-light": {false, spec{
		bg: "#faf8ff", bgAlt: "#f0ecfa", bgSel: "#e2dbf7",
		fg: "#2a2438", fgMuted: "#5e5873", fgSubtle: "#9a94ad",
		border: "#d6cfe8", accent: "#e8336f", accentAlt: "#6b40e8", bubble: "#7d56f4",
		red: "#d6264f", orange: "#d9541e", yellow: "#a68a00", green: "#00875a",
		teal: "#00897b", cyan: "#0087af", sky: "#3d74d9", blue: "#3b5bdb",
		lavender: "#7a5af8", purple: "#7d56f4", pink: "#c839d1", magenta: "#d6338a",
	}},
	"catppuccin-mocha": {true, spec{
		bg: "#1e1e2e", bgAlt: "#181825", bgSel: "#313244", shadow: "#11111b",
		fg: "#cdd6f4", fgMuted: "#a6adc8", fgSubtle: "#6c7086",
		border: "#45475a", accent: "#cba6f7", accentAlt: "#f5c2e7", bubble: "#89b4fa",
		red: "#f38ba8", orange: "#fab387", yellow: "#f9e2af", green: "#a6e3a1",
		teal: "#94e2d5", cyan: "#89dceb", sky: "#74c7ec", blue: "#89b4fa",
		lavender: "#b4befe", purple: "#cba6f7", pink: "#f5c2e7", magenta: "#f2cdcd",
	}},
	"catppuccin-macchiato": {true, spec{
		bg: "#24273a", bgAlt: "#1e2030", bgSel: "#363a4f", shadow: "#181926",
		fg: "#cad3f5", fgMuted: "#a5adcb", fgSubtle: "#6e738d",
		border: "#494d64", accent: "#c6a0f6", accentAlt: "#f5bde6", bubble: "#8aadf4",
		red: "#ed8796", orange: "#f5a97f", yellow: "#eed49f", green: "#a6da95",
		teal: "#8bd5ca", cyan: "#91d7e3", sky: "#7dc4e4", blue: "#8aadf4",
		lavender: "#b7bdf8", purple: "#c6a0f6", pink: "#f5bde6", magenta: "#f0c6c6",
	}},
	"catppuccin-frappe": {true, spec{
		bg: "#303446", bgAlt: "#292c3c", bgSel: "#414559", shadow: "#232634",
		fg: "#c6d0f5", fgMuted: "#a5adce", fgSubtle: "#737994",
		border: "#51576d", accent: "#ca9ee6", accentAlt: "#f4b8e4", bubble: "#8caaee",
		red: "#e78284", orange: "#ef9f76", yellow: "#e5c890", green: "#a6d189",
		teal: "#81c8be", cyan: "#99d1db", sky: "#85c1dc", blue: "#8caaee",
		lavender: "#babbf1", purple: "#ca9ee6", pink: "#f4b8e4", magenta: "#eebebe",
	}},
	"catppuccin-latte": {false, spec{
		bg: "#eff1f5", bgAlt: "#e6e9ef", bgSel: "#ccd0da", shadow: "#bcc0cc",
		fg: "#4c4f69", fgMuted: "#5c5f77", fgSubtle: "#8c8fa1",
		border: "#bcc0cc", accent: "#8839ef", accentAlt: "#ea76cb", bubble: "#1e66f5",
		red: "#d20f39", orange: "#fe640b", yellow: "#df8e1d", green: "#40a02b",
		teal: "#179299", cyan: "#04a5e5", sky: "#209fb5", blue: "#1e66f5",
		lavender: "#7287fd", purple: "#8839ef", pink: "#ea76cb", magenta: "#dd7878",
	}},
	"tokyonight": {true, spec{
		bg: "#1a1b26", bgAlt: "#16161e", bgSel: "#292e42", shadow: "#0c0c12",
		fg: "#c0caf5", fgMuted: "#a9b1d6", fgSubtle: "#565f89",
		border: "#3b4261", accent: "#7aa2f7", accentAlt: "#bb9af7", bubble: "#7aa2f7",
		red: "#f7768e", orange: "#ff9e64", yellow: "#e0af68", green: "#9ece6a",
		teal: "#73daca", cyan: "#7dcfff", sky: "#2ac3de", blue: "#7aa2f7",
		lavender: "#b4b9f7", purple: "#9d7cd8", pink: "#ff007c", magenta: "#bb9af7",
	}},
	"tokyonight-day": {false, spec{
		bg: "#e1e2e7", bgAlt: "#d0d5e3", bgSel: "#c4c8da",
		fg: "#3760bf", fgMuted: "#6172b0", fgSubtle: "#848cb5",
		border: "#a8aecb", accent: "#2e7de9", accentAlt: "#9854f1", bubble: "#2e7de9",
		red: "#f52a65", orange: "#b15c00", yellow: "#8c6c3e", green: "#587539",
		teal: "#118c74", cyan: "#007197", sky: "#188092", blue: "#2e7de9",
		lavender: "#6172b0", purple: "#7847bd", pink: "#d20065", magenta: "#9854f1",
	}},
	"gruvbox-dark": {true, spec{
		bg: "#282828", bgAlt: "#1d2021", bgSel: "#3c3836", shadow: "#141414",
		fg: "#ebdbb2", fgMuted: "#bdae93", fgSubtle: "#7c6f64",
		border: "#504945", accent: "#fe8019", accentAlt: "#fabd2f", bubble: "#83a598",
		red: "#fb4934", orange: "#fe8019", yellow: "#fabd2f", green: "#b8bb26",
		teal: "#8ec07c", cyan: "#689d6a", sky: "#83a598", blue: "#458588",
		lavender: "#a89984", purple: "#d3869b", pink: "#b16286", magenta: "#d65d0e",
	}},
	"gruvbox-light": {false, spec{
		bg: "#fbf1c7", bgAlt: "#f2e5bc", bgSel: "#ebdbb2", shadow: "#d5c4a1",
		fg: "#3c3836", fgMuted: "#665c54", fgSubtle: "#928374",
		border: "#d5c4a1", accent: "#af3a03", accentAlt: "#b57614", bubble: "#076678",
		red: "#9d0006", orange: "#af3a03", yellow: "#b57614", green: "#79740e",
		teal: "#427b58", cyan: "#689d6a", sky: "#458588", blue: "#076678",
		lavender: "#7c6f64", purple: "#8f3f71", pink: "#b16286", magenta: "#d65d0e",
	}},
	"rose-pine": {true, spec{
		bg: "#191724", bgAlt: "#1f1d2e", bgSel: "#26233a", shadow: "#0d0c13",
		fg: "#e0def4", fgMuted: "#908caa", fgSubtle: "#6e6a86",
		border: "#403d52", accent: "#c4a7e7", accentAlt: "#ebbcba", bubble: "#31748f",
		red: "#eb6f92", orange: "#f6c177", yellow: "#f6c177", green: "#9ccfd8",
		teal: "#31748f", cyan: "#9ccfd8", sky: "#56949f", blue: "#31748f",
		lavender: "#c4a7e7", purple: "#c4a7e7", pink: "#ebbcba", magenta: "#eb6f92",
		link: "#9ccfd8",
	}},
	"rose-pine-dawn": {false, spec{
		bg: "#faf4ed", bgAlt: "#fffaf3", bgSel: "#f2e9e1", shadow: "#dfdad9",
		fg: "#575279", fgMuted: "#797593", fgSubtle: "#9893a5",
		border: "#dfdad9", accent: "#907aa9", accentAlt: "#d7827e", bubble: "#56949f",
		red: "#b4637a", orange: "#ea9d34", yellow: "#ea9d34", green: "#56949f",
		teal: "#286983", cyan: "#56949f", sky: "#286983", blue: "#286983",
		lavender: "#907aa9", purple: "#907aa9", pink: "#d7827e", magenta: "#b4637a",
	}},
	"nord": {true, spec{
		bg: "#2e3440", bgAlt: "#3b4252", bgSel: "#434c5e", shadow: "#1f232b",
		fg: "#eceff4", fgMuted: "#d8dee9", fgSubtle: "#616e88",
		border: "#4c566a", accent: "#88c0d0", accentAlt: "#b48ead", bubble: "#5e81ac",
		red: "#bf616a", orange: "#d08770", yellow: "#ebcb8b", green: "#a3be8c",
		teal: "#8fbcbb", cyan: "#88c0d0", sky: "#81a1c1", blue: "#5e81ac",
		lavender: "#b48ead", purple: "#b48ead", pink: "#c895bf", magenta: "#bf616a",
		link: "#88c0d0",
	}},
	"dracula": {true, spec{
		bg: "#282a36", bgAlt: "#21222c", bgSel: "#44475a", shadow: "#191a21",
		fg: "#f8f8f2", fgMuted: "#bfbfd0", fgSubtle: "#6272a4",
		border: "#44475a", accent: "#bd93f9", accentAlt: "#ff79c6", bubble: "#bd93f9",
		red: "#ff5555", orange: "#ffb86c", yellow: "#f1fa8c", green: "#50fa7b",
		teal: "#8be9fd", cyan: "#8be9fd", sky: "#6272a4", blue: "#6272a4",
		lavender: "#bd93f9", purple: "#bd93f9", pink: "#ff79c6", magenta: "#ff92df",
		link: "#8be9fd",
	}},
	"everforest": {true, spec{
		bg: "#2d353b", bgAlt: "#232a2e", bgSel: "#3d484d", shadow: "#1a1f22",
		fg: "#d3c6aa", fgMuted: "#9da9a0", fgSubtle: "#7a8478",
		border: "#475258", accent: "#a7c080", accentAlt: "#83c092", bubble: "#7fbbb3",
		red: "#e67e80", orange: "#e69875", yellow: "#dbbc7f", green: "#a7c080",
		teal: "#83c092", cyan: "#83c092", sky: "#7fbbb3", blue: "#7fbbb3",
		lavender: "#d699b6", purple: "#d699b6", pink: "#e67e80", magenta: "#d699b6",
	}},
}

var registry = func() map[string]Theme {
	m := make(map[string]Theme, len(specs)+2)
	for name, e := range specs {
		m[name] = Theme{Name: name, Palette: build(e.s), Dark: e.dark}
	}
	m[MonoName] = Theme{Name: MonoName, Palette: monoPalette(), Dark: true, Mono: true}
	return m
}()

// Names returns every selectable theme name, including "auto", sorted.
func Names() []string {
	names := make([]string, 0, len(registry)+1)
	for n := range registry {
		names = append(names, n)
	}
	names = append(names, AutoName)
	sort.Strings(names)
	return names
}

// Get looks up a theme by name (case-insensitive). "auto" resolves to the
// dark charm theme, since no background information is available here; use
// Auto or Resolve when the terminal background is known.
func Get(name string) (Theme, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == AutoName {
		return Auto(true), true
	}
	t, ok := registry[name]
	return t, ok
}

// Auto returns the charm theme matching the terminal background, as reported
// by e.g. lipgloss.HasDarkBackground or a tea.BackgroundColorMsg.
func Auto(hasDarkBg bool) Theme {
	if hasDarkBg {
		return registry[Default]
	}
	return registry[CharmLight]
}

// NoColorRequested reports whether the user asked for colorless output via
// the NO_COLOR convention (https://no-color.org): set and non-empty.
func NoColorRequested() bool {
	return os.Getenv("NO_COLOR") != ""
}

// Resolve turns a configured theme name into a Theme. NO_COLOR forces "mono";
// "auto" (or an empty name) picks charm or charm-light from hasDarkBg. An
// unknown name returns the auto theme together with an error so callers can
// warn and keep going.
func Resolve(name string, hasDarkBg bool) (Theme, error) {
	if NoColorRequested() {
		return registry[MonoName], nil
	}
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" || n == AutoName {
		return Auto(hasDarkBg), nil
	}
	if t, ok := registry[n]; ok {
		return t, nil
	}
	return Auto(hasDarkBg), fmt.Errorf("theme: unknown theme %q (available: %s)", name, strings.Join(Names(), ", "))
}
