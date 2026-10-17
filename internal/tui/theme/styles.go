package theme

import (
	"image/color"
	"strings"
	"unicode"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"warte/internal/core"
)

// Mode is an editor mode shown as a badge in the status line.
type Mode int

// Editor modes.
const (
	ModeNormal Mode = iota
	ModeInsert
	ModeVisual
	ModeCommand
	ModeSearch
)

// String returns the badge label for m, e.g. "NORMAL".
func (m Mode) String() string {
	switch m {
	case ModeInsert:
		return "INSERT"
	case ModeVisual:
		return "VISUAL"
	case ModeCommand:
		return "COMMAND"
	case ModeSearch:
		return "SEARCH"
	default:
		return "NORMAL"
	}
}

// Styles is every lipgloss.Style the TUI uses, derived from one Theme. The TUI
// must take all colors from here (or from Theme.Palette) and never hardcode
// them. Styles are values; callers may further customize copies, e.g.
// s.Pane.Width(40).
type Styles struct {
	// Theme is the theme these styles were built from.
	Theme Theme

	// App is the base text style for the whole program.
	App lipgloss.Style
	// Canvas paints the theme background; apply it to full-screen frames
	// when the theme should override the terminal's own background.
	Canvas lipgloss.Style

	// Pane is an unfocused bordered pane; PaneFocused is the focused one.
	Pane        lipgloss.Style
	PaneFocused lipgloss.Style
	// Title is a pane or dialog title; Subtitle is secondary heading text.
	Title    lipgloss.Style
	Subtitle lipgloss.Style

	// Chat list rows.
	ChatListItem         lipgloss.Style
	ChatListItemSelected lipgloss.Style
	ChatListItemUnread   lipgloss.Style
	ChatListPreview      lipgloss.Style
	UnreadBadge          lipgloss.Style
	MentionBadge         lipgloss.Style
	MutedIcon            lipgloss.Style
	PinnedIcon           lipgloss.Style

	// Message view.
	Timestamp      lipgloss.Style
	SenderName     lipgloss.Style // base; see SenderStyle for per-sender color
	BubbleIn       lipgloss.Style
	BubbleOut      lipgloss.Style
	BubbleSelected lipgloss.Style
	Quote          lipgloss.Style // quoted reply, with a left bar
	QuoteAuthor    lipgloss.Style
	Reaction       lipgloss.Style // reaction pill
	ReactionSelf   lipgloss.Style // reaction pill including the user's own
	SystemMessage  lipgloss.Style // centered once a Width is set
	DayDivider     lipgloss.Style
	UnreadDivider  lipgloss.Style
	Edited         lipgloss.Style
	Typing         lipgloss.Style

	// Delivery status icons.
	DeliveryPending   lipgloss.Style
	DeliverySent      lipgloss.Style
	DeliveryDelivered lipgloss.Style
	DeliveryRead      lipgloss.Style
	DeliveryFailed    lipgloss.Style

	// Inline content.
	Spoiler     lipgloss.Style // use RenderSpoiler, which masks the text itself
	Link        lipgloss.Style
	Mention     lipgloss.Style
	MentionSelf lipgloss.Style
	Code        lipgloss.Style
	CodeBlock   lipgloss.Style
	Selection   lipgloss.Style

	// Composer.
	Composer        lipgloss.Style
	ComposerFocused lipgloss.Style
	Placeholder     lipgloss.Style

	// Status line and key hints.
	StatusBar   lipgloss.Style
	KeyHintKey  lipgloss.Style
	KeyHintDesc lipgloss.Style
	KeyHintSep  lipgloss.Style

	// Overlays.
	Overlay     lipgloss.Style
	Dialog      lipgloss.Style
	DialogTitle lipgloss.Style
	ShadowStyle lipgloss.Style
	FuzzyMatch  lipgloss.Style

	// Toasts and status text.
	ToastError   lipgloss.Style
	ToastWarning lipgloss.Style
	ToastSuccess lipgloss.Style
	ToastInfo    lipgloss.Style
	ErrorText    lipgloss.Style
	WarningText  lipgloss.Style
	SuccessText  lipgloss.Style
	InfoText     lipgloss.Style

	// Scrollbar.
	ScrollbarThumb lipgloss.Style
	ScrollbarTrack lipgloss.Style

	modes       [5]lipgloss.Style
	avatars     [AvatarCount]lipgloss.Style
	senderNames [AvatarCount]lipgloss.Style
}

// NewStyles builds the complete style set for t.
func NewStyles(t Theme) *Styles {
	p := t.Palette
	s := &Styles{Theme: t}
	ns := lipgloss.NewStyle

	s.App = ns().Foreground(p.Fg)
	s.Canvas = ns().Foreground(p.Fg).Background(p.Bg)

	s.Pane = ns().Border(lipgloss.RoundedBorder()).BorderForeground(p.Border)
	s.PaneFocused = ns().Border(lipgloss.RoundedBorder()).BorderForeground(p.BorderFocus)
	s.Title = ns().Bold(true).Foreground(p.Accent)
	s.Subtitle = ns().Foreground(p.FgMuted)

	s.ChatListItem = ns().Foreground(p.Fg).Padding(0, 1)
	s.ChatListItemSelected = ns().Foreground(p.Fg).Background(p.BgSelected).Bold(true).Padding(0, 1)
	s.ChatListItemUnread = ns().Foreground(p.Fg).Bold(true).Padding(0, 1)
	s.ChatListPreview = ns().Foreground(p.FgMuted)
	s.UnreadBadge = ns().Foreground(p.UnreadFg).Background(p.UnreadBg).Bold(true).Padding(0, 1)
	s.MentionBadge = ns().Foreground(p.UnreadFg).Background(p.Mention).Bold(true).Padding(0, 1)
	s.MutedIcon = ns().Foreground(p.FgSubtle)
	s.PinnedIcon = ns().Foreground(p.AccentAlt)

	s.Timestamp = ns().Foreground(p.FgMuted)
	s.SenderName = ns().Bold(true)
	s.BubbleIn = ns().Foreground(p.BubbleInFg).Background(p.BubbleIn).Padding(0, 1)
	s.BubbleOut = ns().Foreground(p.BubbleOutFg).Background(p.BubbleOut).Padding(0, 1)
	s.BubbleSelected = ns().Foreground(p.Fg).Background(p.Selection).Padding(0, 1)
	s.Quote = ns().Foreground(p.FgMuted).
		Border(lipgloss.ThickBorder(), false, false, false, true).
		BorderForeground(p.AccentAlt).PaddingLeft(1)
	s.QuoteAuthor = ns().Foreground(p.AccentAlt).Bold(true)
	s.Reaction = ns().Foreground(p.Fg).Background(p.BgSelected).Padding(0, 1)
	s.ReactionSelf = ns().Foreground(p.Fg).Background(p.MentionSelfBg).Padding(0, 1)
	s.SystemMessage = ns().Foreground(p.FgMuted).Italic(true).AlignHorizontal(lipgloss.Center)
	s.DayDivider = ns().Foreground(p.FgMuted)
	s.UnreadDivider = ns().Foreground(p.Accent).Bold(true)
	s.Edited = ns().Foreground(p.FgSubtle).Italic(true)
	s.Typing = ns().Foreground(p.Accent)

	s.DeliveryPending = ns().Foreground(p.FgSubtle)
	s.DeliverySent = ns().Foreground(p.FgMuted)
	s.DeliveryDelivered = ns().Foreground(p.FgMuted)
	s.DeliveryRead = ns().Foreground(p.Accent)
	s.DeliveryFailed = ns().Foreground(p.Error).Bold(true)

	s.Spoiler = ns().Foreground(p.Spoiler).Background(p.Spoiler)
	s.Link = ns().Foreground(p.Link).Underline(true)
	s.Mention = ns().Foreground(p.Mention).Bold(true)
	s.MentionSelf = ns().Foreground(p.Fg).Background(p.MentionSelfBg).Bold(true)
	s.Code = ns().Foreground(p.BubbleInFg).Background(p.BgAlt)
	s.CodeBlock = ns().Foreground(p.BubbleInFg).Background(p.BgAlt).Padding(0, 1)
	s.Selection = ns().Foreground(p.Fg).Background(p.Selection)

	s.Composer = ns().Border(lipgloss.RoundedBorder()).BorderForeground(p.Border).Padding(0, 1)
	s.ComposerFocused = ns().Border(lipgloss.RoundedBorder()).BorderForeground(p.BorderFocus).Padding(0, 1)
	s.Placeholder = ns().Foreground(p.FgSubtle).Italic(true)

	s.StatusBar = ns().Foreground(p.FgMuted).Background(p.BgAlt)
	s.KeyHintKey = ns().Foreground(p.Accent).Bold(true)
	s.KeyHintDesc = ns().Foreground(p.FgMuted)
	s.KeyHintSep = ns().Foreground(p.FgSubtle)

	s.Overlay = ns().Border(lipgloss.RoundedBorder()).BorderForeground(p.Border).Padding(0, 1)
	s.Dialog = ns().Border(lipgloss.RoundedBorder()).BorderForeground(p.BorderFocus).Padding(1, 2)
	s.DialogTitle = ns().Foreground(p.Accent).Bold(true).MarginBottom(1)
	s.ShadowStyle = ns().Foreground(p.Shadow)
	s.FuzzyMatch = ns().Foreground(p.Accent).Bold(true).Underline(true)

	toast := func(c color.Color) lipgloss.Style {
		return ns().Foreground(p.Fg).Border(lipgloss.RoundedBorder()).BorderForeground(c).Padding(0, 1)
	}
	s.ToastError = toast(p.Error)
	s.ToastWarning = toast(p.Warning)
	s.ToastSuccess = toast(p.Success)
	s.ToastInfo = toast(p.Info)
	s.ErrorText = ns().Foreground(p.Error)
	s.WarningText = ns().Foreground(p.Warning)
	s.SuccessText = ns().Foreground(p.Success)
	s.InfoText = ns().Foreground(p.Info)

	s.ScrollbarThumb = ns().Foreground(p.Accent)
	s.ScrollbarTrack = ns().Foreground(p.Border)

	modeColors := [5]color.Color{p.ModeNormal, p.ModeInsert, p.ModeVisual, p.ModeCommand, p.ModeSearch}
	for i, c := range modeColors {
		s.modes[i] = ns().Bold(true).Padding(0, 1).Background(c).Foreground(onColor(t, c))
	}
	for i, c := range p.Avatars {
		s.avatars[i] = ns().Bold(true).Background(c).Foreground(onColor(t, c)).Width(4).AlignHorizontal(lipgloss.Center)
		s.senderNames[i] = s.SenderName.Foreground(c)
	}

	if t.Mono {
		s.applyMono()
	}
	return s
}

// onColor picks a readable text color for content drawn on background c.
func onColor(t Theme, c color.Color) color.Color {
	if t.Mono {
		return lipgloss.NoColor{}
	}
	return readableOn(toRGB(c), toRGB(t.Palette.Bg), toRGB(t.Palette.Fg)).color()
}

// applyMono replaces color cues with text attributes so the colorless theme
// still communicates focus, selection and emphasis.
func (s *Styles) applyMono() {
	ns := lipgloss.NewStyle
	s.Canvas = ns()
	s.PaneFocused = s.PaneFocused.Border(lipgloss.ThickBorder())
	s.Pane = s.Pane.BorderForeground(lipgloss.NoColor{}).Faint(false)
	s.Subtitle = s.Subtitle.Faint(true)
	s.ChatListItemSelected = ns().Reverse(true).Bold(true).Padding(0, 1)
	s.ChatListPreview = s.ChatListPreview.Faint(true)
	s.UnreadBadge = ns().Reverse(true).Bold(true).Padding(0, 1)
	s.MentionBadge = ns().Reverse(true).Bold(true).Underline(true).Padding(0, 1)
	s.MutedIcon = s.MutedIcon.Faint(true)
	s.Timestamp = s.Timestamp.Faint(true)
	s.BubbleOut = s.BubbleOut.Bold(false)
	s.BubbleSelected = ns().Reverse(true).Padding(0, 1)
	s.Quote = s.Quote.Faint(true)
	s.Reaction = ns().Padding(0, 1).Faint(true)
	s.ReactionSelf = ns().Padding(0, 1).Bold(true)
	s.SystemMessage = s.SystemMessage.Faint(true)
	s.DayDivider = s.DayDivider.Faint(true)
	s.Edited = s.Edited.Faint(true)
	s.Typing = s.Typing.Faint(true)
	s.DeliveryPending = s.DeliveryPending.Faint(true)
	s.DeliverySent = s.DeliverySent.Faint(true)
	s.DeliveryDelivered = s.DeliveryDelivered.Faint(true)
	s.DeliveryRead = s.DeliveryRead.Bold(true)
	s.Spoiler = ns().Reverse(true).Faint(true)
	s.Mention = s.Mention.Underline(true)
	s.MentionSelf = ns().Reverse(true).Bold(true)
	s.Code = ns().Italic(true)
	s.CodeBlock = ns().Padding(0, 1).Border(lipgloss.NormalBorder(), false, false, false, true)
	s.Selection = ns().Reverse(true)
	s.Placeholder = s.Placeholder.Faint(true)
	s.StatusBar = ns().Reverse(true)
	s.KeyHintDesc = s.KeyHintDesc.Faint(true)
	s.KeyHintSep = s.KeyHintSep.Faint(true)
	s.Dialog = s.Dialog.Border(lipgloss.ThickBorder())
	s.ComposerFocused = s.ComposerFocused.Border(lipgloss.ThickBorder())
	s.ShadowStyle = ns().Faint(true)
	s.ToastError = s.ToastError.Border(lipgloss.ThickBorder()).Bold(true)
	s.ErrorText = s.ErrorText.Bold(true)
	s.WarningText = s.WarningText.Bold(true)
	s.ScrollbarTrack = s.ScrollbarTrack.Faint(true)
	s.ScrollbarThumb = s.ScrollbarThumb.Bold(true)
	for i := range s.modes {
		s.modes[i] = ns().Reverse(true).Bold(true).Padding(0, 1)
	}
	for i := range s.avatars {
		s.avatars[i] = ns().Reverse(true).Bold(true).Width(4).AlignHorizontal(lipgloss.Center)
		s.senderNames[i] = s.SenderName
	}
}

// ModeBadge returns the status-line badge style for mode m.
func (s *Styles) ModeBadge(m Mode) lipgloss.Style {
	if m < 0 || int(m) >= len(s.modes) {
		m = ModeNormal
	}
	return s.modes[m]
}

// RenderMode renders the status-line badge for mode m, e.g. " NORMAL ".
func (s *Styles) RenderMode(m Mode) string { return s.ModeBadge(m).Render(m.String()) }

// SenderColor deterministically maps a sender identifier (ACI preferred,
// otherwise a name) to an avatar color of this theme.
func (s *Styles) SenderColor(id string) color.Color { return s.Theme.Palette.SenderColor(id) }

// SenderStyle returns SenderName colored for the given sender identifier.
func (s *Styles) SenderStyle(id string) lipgloss.Style { return s.senderNames[senderIndex(id)] }

// Avatar returns the initials-avatar style for a sender identifier. If code is
// a Signal avatar color name ("A100" … "A210") it takes precedence over the
// hash of id.
func (s *Styles) Avatar(code, id string) lipgloss.Style {
	if i, ok := AvatarIndex(code); ok {
		return s.avatars[i]
	}
	return s.avatars[senderIndex(id)]
}

// RenderAvatar renders a 4-cell colored initials avatar for name.
func (s *Styles) RenderAvatar(code, id, name string) string {
	return s.Avatar(code, id).Render(Initials(name))
}

// Initials derives up to two uppercase initials from a display name: the
// first letters of the first and last words, or the first two letters of a
// single word. Names without letters or digits yield "#".
func Initials(name string) string {
	var words [][]rune
	for _, f := range strings.Fields(name) {
		var w []rune
		for _, r := range f {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				w = append(w, r)
			}
		}
		if len(w) > 0 {
			words = append(words, w)
		}
	}
	switch len(words) {
	case 0:
		return "#"
	case 1:
		w := words[0]
		if len(w) == 1 {
			return strings.ToUpper(string(w[0]))
		}
		return strings.ToUpper(string(w[0])) + strings.ToLower(string(w[1]))
	default:
		return strings.ToUpper(string(words[0][0]) + string(words[len(words)-1][0]))
	}
}

// Delivery returns the style for a delivery-status icon.
func (s *Styles) Delivery(st core.DeliveryStatus) lipgloss.Style {
	switch st {
	case core.StatusPending:
		return s.DeliveryPending
	case core.StatusSent:
		return s.DeliverySent
	case core.StatusDelivered:
		return s.DeliveryDelivered
	case core.StatusRead, core.StatusViewed:
		return s.DeliveryRead
	case core.StatusFailed:
		return s.DeliveryFailed
	default:
		return lipgloss.NewStyle()
	}
}

// RenderDelivery renders the styled delivery icon for st, or "" for incoming
// messages.
func (s *Styles) RenderDelivery(icons IconSet, st core.DeliveryStatus) string {
	g := icons.Delivery(st)
	if g == "" {
		return ""
	}
	return s.Delivery(st).Render(g)
}

// RenderKeyHint renders a "key desc" pair for help lines.
func (s *Styles) RenderKeyHint(key, desc string) string {
	return s.KeyHintKey.Render(SanitizeLine(key)) + " " + s.KeyHintDesc.Render(SanitizeLine(desc))
}

// spoilerGlyph masks hidden text cell for cell.
const spoilerGlyph = "▒"

// RenderSpoiler renders text as an opaque block of the same display width, so
// the hidden content is neither visible (even in the colorless theme, where
// the Spoiler style can only use reverse video) nor copyable via terminal
// selection. Line breaks are preserved.
func (s *Styles) RenderSpoiler(text string) string {
	lines := strings.Split(Sanitize(text), "\n")
	for i, l := range lines {
		lines[i] = s.Spoiler.Render(strings.Repeat(spoilerGlyph, ansi.StringWidth(l)))
	}
	return strings.Join(lines, "\n")
}

// RenderKeyHints renders several key/description pairs separated by a dot.
// pairs is key, desc, key, desc, …; a trailing odd element is ignored.
func (s *Styles) RenderKeyHints(pairs ...string) string {
	parts := make([]string, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		parts = append(parts, s.RenderKeyHint(pairs[i], pairs[i+1]))
	}
	return strings.Join(parts, s.KeyHintSep.Render(" • "))
}

// divider renders "─── label ───" centered in width cells.
func divider(style lipgloss.Style, label string, width int) string {
	label = " " + SanitizeLine(label) + " "
	lw := ansi.StringWidth(label)
	if width <= lw {
		return style.Render(ansi.Truncate(strings.TrimSpace(label), max(width, 0), "…"))
	}
	left := (width - lw) / 2
	right := width - lw - left
	return style.Render(strings.Repeat("─", left) + label + strings.Repeat("─", right))
}

// RenderUnreadDivider renders "─── New messages ───" across width cells.
func (s *Styles) RenderUnreadDivider(width int) string {
	return divider(s.UnreadDivider, "New messages", width)
}

// RenderDayDivider renders a centered day label such as "Today" across width
// cells.
func (s *Styles) RenderDayDivider(label string, width int) string {
	return divider(s.DayDivider, label, width)
}

// RenderScrollbar renders a vertical scrollbar of height rows for a view
// showing visible of total lines starting at offset. When everything fits it
// returns a blank column so layouts stay stable.
func (s *Styles) RenderScrollbar(height, total, visible, offset int) string {
	if height <= 0 {
		return ""
	}
	rows := make([]string, height)
	if total <= visible || total <= 0 {
		for i := range rows {
			rows[i] = " "
		}
		return strings.Join(rows, "\n")
	}
	thumb := max(1, height*visible/total)
	maxOff := total - visible
	offset = min(max(offset, 0), maxOff)
	start := 0
	if maxOff > 0 {
		start = (height - thumb) * offset / maxOff
	}
	for i := range rows {
		if i >= start && i < start+thumb {
			rows[i] = s.ScrollbarThumb.Render("┃")
		} else {
			rows[i] = s.ScrollbarTrack.Render("│")
		}
	}
	return strings.Join(rows, "\n")
}

// DropShadow returns rendered (typically a Dialog) with a one-cell shadow on
// its right and bottom edges, offset by one cell, for a floating look.
func (s *Styles) DropShadow(rendered string) string {
	lines := strings.Split(rendered, "\n")
	w := 0
	for _, l := range lines {
		w = max(w, ansi.StringWidth(l))
	}
	glyph := "█"
	if s.Theme.Mono {
		glyph = "░"
	}
	sh := s.ShadowStyle.Render(glyph)
	var b strings.Builder
	for i, l := range lines {
		b.WriteString(l)
		if pad := w - ansi.StringWidth(l); pad > 0 {
			b.WriteString(strings.Repeat(" ", pad))
		}
		if i == 0 {
			b.WriteString(" ")
		} else {
			b.WriteString(sh)
		}
		b.WriteString("\n")
	}
	b.WriteString(" ")
	b.WriteString(s.ShadowStyle.Render(strings.Repeat(glyph, w)))
	return b.String()
}
