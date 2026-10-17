package theme

import (
	"fmt"
	"image/color"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"warte/internal/core"
)

var requiredNames = []string{
	"auto", "charm", "catppuccin-mocha", "catppuccin-latte", "catppuccin-frappe",
	"catppuccin-macchiato", "tokyonight", "tokyonight-day", "gruvbox-dark",
	"gruvbox-light", "rose-pine", "rose-pine-dawn", "nord", "dracula",
	"everforest", "mono",
}

func allThemes(t *testing.T) []Theme {
	t.Helper()
	var out []Theme
	for _, n := range Names() {
		th, ok := Get(n)
		if !ok {
			t.Fatalf("Get(%q) failed", n)
		}
		out = append(out, th)
	}
	return out
}

func TestNamesSortedAndComplete(t *testing.T) {
	names := Names()
	if !slices.IsSorted(names) {
		t.Fatalf("Names() not sorted: %v", names)
	}
	for _, want := range requiredNames {
		if !slices.Contains(names, want) {
			t.Errorf("Names() missing %q", want)
		}
	}
	if len(slices.Compact(slices.Clone(names))) != len(names) {
		t.Errorf("Names() has duplicates: %v", names)
	}
}

func TestGet(t *testing.T) {
	for _, n := range Names() {
		th, ok := Get(n)
		if !ok {
			t.Errorf("Get(%q) not found", n)
			continue
		}
		if n != AutoName && th.Name != n {
			t.Errorf("Get(%q).Name = %q", n, th.Name)
		}
	}
	tests := []struct {
		in     string
		wantOK bool
		want   string
	}{
		{"CHARM", true, "charm"},
		{"  nord ", true, "nord"},
		{"auto", true, "charm"},
		{"nope", false, ""},
		{"", false, ""},
	}
	for _, tt := range tests {
		th, ok := Get(tt.in)
		if ok != tt.wantOK || (ok && th.Name != tt.want) {
			t.Errorf("Get(%q) = (%q, %v), want (%q, %v)", tt.in, th.Name, ok, tt.want, tt.wantOK)
		}
	}
}

func TestDarkFlags(t *testing.T) {
	light := []string{"charm-light", "catppuccin-latte", "tokyonight-day", "gruvbox-light", "rose-pine-dawn"}
	for _, th := range allThemes(t) {
		if th.Mono {
			continue
		}
		wantDark := !slices.Contains(light, th.Name)
		if th.Dark != wantDark {
			t.Errorf("%s: Dark = %v, want %v", th.Name, th.Dark, wantDark)
		}
		if isDark := Luminance(th.Palette.Bg) < 0.18; isDark != th.Dark {
			t.Errorf("%s: background luminance disagrees with Dark flag", th.Name)
		}
	}
}

func TestAutoAndResolve(t *testing.T) {
	if got := Auto(true).Name; got != "charm" {
		t.Errorf("Auto(true) = %q", got)
	}
	if got := Auto(false).Name; got != "charm-light" {
		t.Errorf("Auto(false) = %q", got)
	}

	tests := []struct {
		name    string
		noColor string
		dark    bool
		want    string
		wantErr bool
	}{
		{"auto", "", true, "charm", false},
		{"auto", "", false, "charm-light", false},
		{"", "", false, "charm-light", false},
		{"dracula", "", false, "dracula", false},
		{"Gruvbox-Light", "", true, "gruvbox-light", false},
		{"bogus", "", true, "charm", true},
		{"dracula", "1", true, "mono", false},
		{"auto", "yes", false, "mono", false},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s/%s/%v", tt.name, tt.noColor, tt.dark), func(t *testing.T) {
			t.Setenv("NO_COLOR", tt.noColor)
			th, err := Resolve(tt.name, tt.dark)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if th.Name != tt.want {
				t.Errorf("Resolve(%q) = %q, want %q", tt.name, th.Name, tt.want)
			}
		})
	}
}

// colorFields walks every color.Color in a Palette, including array entries.
func colorFields(p Palette) map[string]color.Color {
	out := map[string]color.Color{}
	v := reflect.ValueOf(p)
	ct := reflect.TypeOf((*color.Color)(nil)).Elem()
	for i := 0; i < v.NumField(); i++ {
		f, ft := v.Field(i), v.Type().Field(i)
		switch {
		case ft.Type == ct:
			c, _ := f.Interface().(color.Color)
			out[ft.Name] = c
		case ft.Type.Kind() == reflect.Array && ft.Type.Elem() == ct:
			for j := 0; j < f.Len(); j++ {
				c, _ := f.Index(j).Interface().(color.Color)
				out[fmt.Sprintf("%s[%d]", ft.Name, j)] = c
			}
		default:
			panic("unexpected Palette field type " + ft.Type.String())
		}
	}
	return out
}

func TestPaletteHasNoNilColors(t *testing.T) {
	for _, th := range allThemes(t) {
		fields := colorFields(th.Palette)
		if len(fields) < 30 {
			t.Fatalf("expected many palette colors, got %d", len(fields))
		}
		for name, c := range fields {
			if c == nil {
				t.Errorf("%s: Palette.%s is nil", th.Name, name)
			}
		}
	}
}

func TestMonoIsColorless(t *testing.T) {
	th, _ := Get("mono")
	if !th.Mono {
		t.Fatal("mono theme not flagged Mono")
	}
	for name, c := range colorFields(th.Palette) {
		if _, ok := c.(lipgloss.NoColor); !ok {
			t.Errorf("mono Palette.%s = %T, want lipgloss.NoColor", name, c)
		}
	}
	out := Preview(th)
	if strings.Contains(out, "38;") || strings.Contains(out, "48;") {
		t.Errorf("mono preview contains color escapes: %q", out)
	}
	s := NewStyles(th)
	if !s.ChatListItemSelected.GetReverse() || !s.ModeBadge(ModeInsert).GetReverse() {
		t.Error("mono selection and mode badge should use reverse video")
	}
}

func TestContrast(t *testing.T) {
	for _, th := range allThemes(t) {
		if th.Mono {
			continue
		}
		p := th.Palette
		check := func(what string, a, b color.Color, min float64) {
			t.Helper()
			if c := Contrast(a, b); c < min {
				t.Errorf("%s: %s contrast %.2f < %.1f (%s on %s)", th.Name, what, c, min, Hex(a), Hex(b))
			}
		}
		check("Fg/Bg", p.Fg, p.Bg, 4.5)
		check("FgMuted/Bg", p.FgMuted, p.Bg, 4.5)
		check("Fg/BgSelected", p.Fg, p.BgSelected, 4.5)
		check("BubbleInFg/BubbleIn", p.BubbleInFg, p.BubbleIn, 4.5)
		check("BubbleOutFg/BubbleOut", p.BubbleOutFg, p.BubbleOut, 4.5)
		check("Fg/MentionSelfBg", p.Fg, p.MentionSelfBg, 4.5)
		check("Fg/Selection", p.Fg, p.Selection, 4.5)
		check("UnreadFg/UnreadBg", p.UnreadFg, p.UnreadBg, 3)
		for name, c := range map[string]color.Color{
			"Accent": p.Accent, "AccentAlt": p.AccentAlt, "BorderFocus": p.BorderFocus,
			"Link": p.Link, "Mention": p.Mention, "Success": p.Success,
			"Warning": p.Warning, "Error": p.Error, "Info": p.Info,
			"ModeNormal": p.ModeNormal, "ModeInsert": p.ModeInsert, "ModeVisual": p.ModeVisual,
			"ModeCommand": p.ModeCommand, "ModeSearch": p.ModeSearch,
		} {
			check(name+"/Bg", c, p.Bg, 3)
		}
		for i, c := range p.Avatars {
			check(fmt.Sprintf("Avatars[%d]/Bg", i), c, p.Bg, 3)
		}
		s := NewStyles(th)
		for m := ModeNormal; m <= ModeSearch; m++ {
			b := s.ModeBadge(m)
			check(m.String()+" badge text", b.GetForeground(), b.GetBackground(), 3)
		}
		for _, code := range AvatarCodes {
			a := s.Avatar(code, "")
			check(code+" avatar initials", a.GetForeground(), a.GetBackground(), 3)
		}
	}
}

func TestColorMath(t *testing.T) {
	near := func(a, b float64) bool { return math.Abs(a-b) < 0.01 }
	tests := []struct {
		a, b string
		want float64
	}{
		{"#000000", "#ffffff", 21},
		{"#ffffff", "#ffffff", 1},
		{"#777777", "#ffffff", 4.48},
		{"#ff0000", "#ffffff", 4.0},
	}
	for _, tt := range tests {
		if got := Contrast(lipgloss.Color(tt.a), lipgloss.Color(tt.b)); !near(got, tt.want) {
			t.Errorf("Contrast(%s,%s) = %.3f, want %.2f", tt.a, tt.b, got, tt.want)
		}
	}
	if got := Hex(lipgloss.Color("#AbCdEf")); got != "#abcdef" {
		t.Errorf("Hex = %q", got)
	}
	if got := mix(parseHex("#000000"), parseHex("#ffffff"), 0.5).hex(); got != "#808080" {
		t.Errorf("mix midpoint = %s", got)
	}
	if got := mix(parseHex("#102030"), white, 2).hex(); got != "#ffffff" {
		t.Errorf("mix clamps t: %s", got)
	}
	for _, bad := range []string{"#fff", "zzzzzz", ""} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("parseHex(%q) did not panic", bad)
				}
			}()
			parseHex(bad)
		}()
	}
}

func TestEnsureContrastTintReadable(t *testing.T) {
	cases := []struct {
		c, bg string
		min   float64
	}{
		{"#ffff00", "#ffffff", 3},
		{"#202020", "#101010", 4.5},
		{"#7d56f4", "#1b1a23", 7},
		{"#ff5f87", "#faf8ff", 4.5},
	}
	for _, tt := range cases {
		c, bg := parseHex(tt.c), parseHex(tt.bg)
		got := ensureContrast(c, bg, tt.min)
		if contrast(got, bg) < tt.min {
			t.Errorf("ensureContrast(%s,%s,%.1f) = %s (%.2f)", tt.c, tt.bg, tt.min, got.hex(), contrast(got, bg))
		}
		if contrast(c, bg) >= tt.min && got != c {
			t.Errorf("ensureContrast changed an already-good color")
		}
	}
	bg, fg := parseHex("#1e1e2e"), parseHex("#cdd6f4")
	ti := tint(parseHex("#cba6f7"), bg, fg, 0.9, 4.5)
	if contrast(fg, ti) < 4.5 {
		t.Errorf("tint result %s unreadable", ti.hex())
	}
	if ti == bg {
		t.Error("tint should keep some hue")
	}
	if r := readableOn(parseHex("#ff5f87"), parseHex("#1b1a23"), parseHex("#ece8f5")); r != parseHex("#1b1a23") {
		t.Errorf("readableOn preferred %s", r.hex())
	}
	if r := readableOn(parseHex("#808080")); contrast(r, parseHex("#808080")) < 3 {
		t.Errorf("readableOn fallback %s", r.hex())
	}
}

func TestSenderColorDeterministic(t *testing.T) {
	th, _ := Get("charm")
	s := NewStyles(th)
	ids := []string{"", "alice", "bob", "a1b2c3d4-0000-4000-8000-000000000000", "Ünïcödé 🎉"}
	for _, id := range ids {
		a, b := th.Palette.SenderColor(id), s.SenderColor(id)
		if Hex(a) != Hex(b) || Hex(a) != Hex(th.Palette.SenderColor(id)) {
			t.Errorf("SenderColor(%q) not deterministic", id)
		}
		if !slices.ContainsFunc(th.Palette.Avatars[:], func(c color.Color) bool { return Hex(c) == Hex(a) }) {
			t.Errorf("SenderColor(%q) not from avatar palette", id)
		}
		if got := s.SenderStyle(id).GetForeground(); Hex(got) != Hex(a) {
			t.Errorf("SenderStyle(%q) color mismatch", id)
		}
	}
	// Known value guards against accidental hash changes, which would
	// recolor every conversation for users.
	for id, want := range map[string]int{"alice": 11, "bob": 8} {
		if got := senderIndex(id); got != want {
			t.Errorf("senderIndex(%q) = %d, want %d", id, got, want)
		}
	}
	seen := map[int]bool{}
	for i := 0; i < 500; i++ {
		seen[senderIndex(fmt.Sprintf("user-%d", i))] = true
	}
	if len(seen) != AvatarCount {
		t.Errorf("hash only reaches %d of %d colors", len(seen), AvatarCount)
	}
}

func TestAvatarIndexAndColor(t *testing.T) {
	tests := []struct {
		code   string
		want   int
		wantOK bool
	}{
		{"A100", 0, true},
		{"a150", 5, true},
		{" A210 ", 11, true},
		{"A999", 0, false},
		{"", 0, false},
	}
	for _, tt := range tests {
		got, ok := AvatarIndex(tt.code)
		if got != tt.want || ok != tt.wantOK {
			t.Errorf("AvatarIndex(%q) = %d,%v", tt.code, got, ok)
		}
	}
	th, _ := Get("nord")
	p := th.Palette
	if Hex(p.AvatarColor("A130", "x")) != Hex(p.Avatars[3]) {
		t.Error("AvatarColor ignored code")
	}
	if Hex(p.AvatarColor("", "x")) != Hex(p.SenderColor("x")) {
		t.Error("AvatarColor fallback mismatch")
	}
	s := NewStyles(th)
	if out := s.RenderAvatar("A100", "", "Ada Lovelace"); !strings.Contains(out, "AL") || ansi.StringWidth(out) != 4 {
		t.Errorf("RenderAvatar = %q (width %d)", out, ansi.StringWidth(out))
	}
}

func TestInitials(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Ada Lovelace", "AL"},
		{"ada  byron lovelace", "AL"},
		{"Bob", "Bo"},
		{"b", "B"},
		{"", "#"},
		{"   ", "#"},
		{"🎉 🎉", "#"},
		{"+49 151", "41"},
		{"Ünal Çelik", "ÜÇ"},
		{"(Work) Team", "WT"},
	}
	for _, tt := range tests {
		if got := Initials(tt.in); got != tt.want {
			t.Errorf("Initials(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestModes(t *testing.T) {
	tests := []struct {
		m    Mode
		want string
	}{
		{ModeNormal, "NORMAL"}, {ModeInsert, "INSERT"}, {ModeVisual, "VISUAL"},
		{ModeCommand, "COMMAND"}, {ModeSearch, "SEARCH"}, {Mode(42), "NORMAL"},
	}
	th, _ := Get("charm")
	s := NewStyles(th)
	for _, tt := range tests {
		if got := tt.m.String(); got != tt.want {
			t.Errorf("Mode(%d).String() = %q", tt.m, got)
		}
		if out := s.RenderMode(tt.m); !strings.Contains(out, tt.want) {
			t.Errorf("RenderMode(%d) = %q", tt.m, out)
		}
	}
	if Hex(s.ModeBadge(Mode(-1)).GetBackground()) != Hex(th.Palette.ModeNormal) {
		t.Error("out-of-range mode should fall back to normal")
	}
	if Hex(s.ModeBadge(ModeInsert).GetBackground()) != Hex(th.Palette.ModeInsert) {
		t.Error("insert badge color mismatch")
	}
}

func TestIcons(t *testing.T) {
	nerd, uni := Icons(true), Icons(false)
	for _, set := range []struct {
		name string
		ic   IconSet
	}{{"nerd", nerd}, {"unicode", uni}} {
		v := reflect.ValueOf(set.ic)
		for i := 0; i < v.NumField(); i++ {
			f := v.Field(i)
			name := v.Type().Field(i).Name
			if f.Kind() == reflect.String {
				if f.String() == "" {
					t.Errorf("%s icon %s empty", set.name, name)
				}
				if w := ansi.StringWidth(f.String()); w > 2 {
					t.Errorf("%s icon %s too wide: %d", set.name, name, w)
				}
			}
		}
		if len(set.ic.Typing) < 2 {
			t.Errorf("%s: need typing frames", set.name)
		}
		w0 := ansi.StringWidth(set.ic.Typing[0])
		for _, f := range set.ic.Typing {
			if ansi.StringWidth(f) != w0 {
				t.Errorf("%s: typing frames differ in width", set.name)
			}
		}
	}
	if nerd.Pinned == uni.Pinned {
		t.Error("nerd and unicode sets should differ")
	}
	a := Icons(false)
	a.Typing[0] = "x"
	if Icons(false).Typing[0] == "x" {
		t.Error("Icons must return an independent copy")
	}
	if got := uni.TypingFrame(len(uni.Typing) + 1); got != uni.Typing[1] {
		t.Errorf("TypingFrame wrap = %q", got)
	}
	if got := uni.TypingFrame(-1); got != uni.Typing[1] {
		t.Errorf("TypingFrame(-1) = %q", got)
	}
	if (IconSet{}).TypingFrame(3) != "" {
		t.Error("empty TypingFrame should be empty")
	}
}

func TestDelivery(t *testing.T) {
	ic := Icons(false)
	th, _ := Get("charm")
	s := NewStyles(th)
	tests := []struct {
		st   core.DeliveryStatus
		icon string
	}{
		{core.StatusNone, ""},
		{core.StatusPending, ic.Clock},
		{core.StatusSent, ic.Check},
		{core.StatusDelivered, ic.DoubleCheck},
		{core.StatusRead, ic.DoubleCheck},
		{core.StatusViewed, ic.DoubleCheck},
		{core.StatusFailed, ic.Failed},
		{core.DeliveryStatus(99), ""},
	}
	for _, tt := range tests {
		if got := ic.Delivery(tt.st); got != tt.icon {
			t.Errorf("Delivery(%d) = %q, want %q", tt.st, got, tt.icon)
		}
		out := s.RenderDelivery(ic, tt.st)
		if (out == "") != (tt.icon == "") || (tt.icon != "" && !strings.Contains(out, tt.icon)) {
			t.Errorf("RenderDelivery(%d) = %q", tt.st, out)
		}
	}
	if Hex(s.Delivery(core.StatusRead).GetForeground()) == Hex(s.Delivery(core.StatusDelivered).GetForeground()) {
		t.Error("read and delivered should be visually distinct")
	}
	if Hex(s.Delivery(core.StatusFailed).GetForeground()) != Hex(th.Palette.Error) {
		t.Error("failed should use Error color")
	}
}

func TestDividers(t *testing.T) {
	th, _ := Get("tokyonight")
	s := NewStyles(th)
	for _, w := range []int{0, 3, 12, 14, 15, 40, 81} {
		u := s.RenderUnreadDivider(w)
		if got := ansi.StringWidth(u); got != w && w > ansi.StringWidth(" New messages ") {
			t.Errorf("unread divider width %d, want %d", got, w)
		}
		if got := ansi.StringWidth(u); got > max(w, 0) {
			t.Errorf("unread divider overflows: %d > %d", got, w)
		}
		d := s.RenderDayDivider("Today", w)
		if got := ansi.StringWidth(d); got > w {
			t.Errorf("day divider overflows: %d > %d", got, w)
		}
	}
	if plain := ansi.Strip(s.RenderUnreadDivider(30)); !strings.Contains(plain, "─ New messages ─") {
		t.Errorf("unread divider = %q", plain)
	}
}

func TestScrollbar(t *testing.T) {
	th, _ := Get("dracula")
	s := NewStyles(th)
	tests := []struct {
		height, total, visible, offset int
		wantThumb                      int
		wantFirstThumb                 bool
		wantLastThumb                  bool
	}{
		{10, 5, 10, 0, 0, false, false},
		{10, 100, 10, 0, 1, true, false},
		{10, 100, 10, 90, 1, false, true},
		{10, 100, 10, 500, 1, false, true},
		{10, 20, 10, 0, 5, true, false},
		{4, 8, 4, -3, 2, true, false},
	}
	for _, tt := range tests {
		out := s.RenderScrollbar(tt.height, tt.total, tt.visible, tt.offset)
		lines := strings.Split(ansi.Strip(out), "\n")
		if len(lines) != tt.height {
			t.Fatalf("got %d lines, want %d", len(lines), tt.height)
		}
		thumbs := 0
		for _, l := range lines {
			if l == "┃" {
				thumbs++
			}
		}
		if thumbs != tt.wantThumb {
			t.Errorf("%+v: thumbs = %d", tt, thumbs)
		}
		if (lines[0] == "┃") != tt.wantFirstThumb || (lines[len(lines)-1] == "┃") != tt.wantLastThumb {
			t.Errorf("%+v: thumb position wrong: %q", tt, lines)
		}
	}
	if s.RenderScrollbar(0, 10, 5, 0) != "" {
		t.Error("zero height should render nothing")
	}
}

func TestDropShadowAndHints(t *testing.T) {
	for _, name := range []string{"charm", "mono"} {
		th, _ := Get(name)
		s := NewStyles(th)
		box := s.Dialog.Render("hello")
		bw, bh := lipgloss.Width(box), lipgloss.Height(box)
		out := s.DropShadow(box)
		if lipgloss.Width(out) != bw+1 || lipgloss.Height(out) != bh+1 {
			t.Errorf("%s: shadow dims %dx%d, want %dx%d", name, lipgloss.Width(out), lipgloss.Height(out), bw+1, bh+1)
		}
		hints := ansi.Strip(s.RenderKeyHints("j", "down", "k", "up", "dangling"))
		if hints != "j down • k up" {
			t.Errorf("%s: hints = %q", name, hints)
		}
	}
}

func TestStylesUseThemeColors(t *testing.T) {
	for _, th := range allThemes(t) {
		s := NewStyles(th)
		if s.Theme.Name != th.Name {
			t.Errorf("Styles.Theme mismatch")
		}
		if th.Mono {
			continue
		}
		p := th.Palette
		eq := []struct {
			name string
			a, b color.Color
		}{
			{"PaneFocused border", s.PaneFocused.GetBorderTopForeground(), p.BorderFocus},
			{"Pane border", s.Pane.GetBorderTopForeground(), p.Border},
			{"BubbleIn bg", s.BubbleIn.GetBackground(), p.BubbleIn},
			{"BubbleOut bg", s.BubbleOut.GetBackground(), p.BubbleOut},
			{"UnreadBadge bg", s.UnreadBadge.GetBackground(), p.UnreadBg},
			{"Link fg", s.Link.GetForeground(), p.Link},
			{"MentionSelf bg", s.MentionSelf.GetBackground(), p.MentionSelfBg},
			{"Code bg", s.Code.GetBackground(), p.BgAlt},
			{"ToastError border", s.ToastError.GetBorderTopForeground(), p.Error},
			{"Selected bg", s.ChatListItemSelected.GetBackground(), p.BgSelected},
		}
		for _, e := range eq {
			if Hex(e.a) != Hex(e.b) {
				t.Errorf("%s: %s = %s, want %s", th.Name, e.name, Hex(e.a), Hex(e.b))
			}
		}
	}
}

func TestPreviewEveryTheme(t *testing.T) {
	for _, th := range allThemes(t) {
		t.Run(th.Name, func(t *testing.T) {
			out := Preview(th)
			if out == "" {
				t.Fatal("empty preview")
			}
			plain := ansi.Strip(out)
			for _, want := range []string{th.Name, "Ada Lovelace", "New messages", "NORMAL"} {
				if !strings.Contains(plain, want) {
					t.Errorf("preview missing %q:\n%s", want, plain)
				}
			}
			lines := strings.Split(out, "\n")
			w := ansi.StringWidth(lines[0])
			for i, l := range lines {
				if ansi.StringWidth(l) != w {
					t.Errorf("line %d width %d != %d: %q", i, ansi.StringWidth(l), w, ansi.Strip(l))
				}
			}
		})
	}
}
