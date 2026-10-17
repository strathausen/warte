package theme

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"warte/internal/core"
)

// previewWidth is the inner width of the Preview swatch.
const previewWidth = 34

// Preview renders a small sample of t for the ":theme" picker: two chat list
// rows (one selected, one unread with a badge), an incoming and an outgoing
// bubble, a mode badge and the avatar palette. It uses the Unicode icon set so
// it renders without a Nerd Font.
func Preview(t Theme) string {
	s := NewStyles(t)
	ic := Icons(false)
	w := previewWidth

	// row lays out an avatar followed by left/right aligned text. Each
	// segment is rendered separately so the row background is not reset by
	// the avatar's own styling.
	// suffix is pre-rendered content placed flush right after the text.
	row := func(st lipgloss.Style, avatar, left, right, suffix string) string {
		text := st.UnsetPadding()
		inner := w - ansi.StringWidth(avatar) - ansi.StringWidth(suffix) - 2
		gap := max(1, inner-ansi.StringWidth(left)-ansi.StringWidth(right))
		return text.Render(" ") + avatar + text.Render(" "+left+strings.Repeat(" ", gap)+right) + suffix
	}

	selected := row(s.ChatListItemSelected, s.RenderAvatar("A130", "", "Ada Lovelace"),
		"Ada Lovelace", "12:41 ", "")
	unread := row(s.ChatListItemUnread, s.RenderAvatar("", "grp", "Charm Crew"),
		"Charm Crew", "", s.MutedIcon.Render(ic.Pinned)+" "+s.UnreadBadge.Render("3"))

	in := s.SenderStyle("bob").Render("bob") + "\n" +
		s.BubbleIn.UnsetPadding().Render(" got the ") + s.Link.Render("link") +
		s.BubbleIn.UnsetPadding().Render("? ")
	out := s.BubbleOut.UnsetPadding().Render(" yes, ") + s.MentionSelf.Render("@you") +
		s.BubbleOut.UnsetPadding().Render(" ") + " " + s.RenderDelivery(ic, core.StatusRead)
	out = lipgloss.PlaceHorizontal(w, lipgloss.Right, out)

	var sw strings.Builder
	if t.Mono {
		sw.WriteString(s.Subtitle.Render("no colors, attributes only"))
	} else {
		for _, c := range t.Palette.Avatars {
			sw.WriteString(lipgloss.NewStyle().Background(c).Render("  "))
		}
	}
	status := s.RenderMode(ModeNormal) + " " + s.RenderKeyHint("?", "help")

	body := lipgloss.JoinVertical(lipgloss.Left,
		s.Title.Render(t.Name),
		selected,
		unread,
		s.RenderUnreadDivider(w),
		in,
		out,
		sw.String(),
		status,
	)
	return s.PaneFocused.Render(body)
}
