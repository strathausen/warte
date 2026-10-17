package tui

import (
	"image/color"
	"net/url"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"warte/internal/core"
	"warte/internal/format"
	"warte/internal/termimg"
	"warte/internal/tui/theme"
	"warte/internal/tui/ui"
)

// gutterW is the width of the selection gutter left of every message.
const gutterW = 2

// avatarColW is the indentation of incoming group bubbles (avatar + space).
const avatarColW = 5

// renderMessage renders one message block (with its dividers) at width w.
func (a *App) renderMessage(m *core.Message, w int, flags uint16, query string) *block {
	st := a.styles
	b := &block{}
	cw := max(w-gutterW, 8)

	// Dividers.
	if flags&flagDay != 0 {
		label := a.tf.DayHeader(m.SentAt, a.now())
		b.lines = append(b.lines, "", spaces(gutterW)+fit(st.RenderDayDivider(clean(label), cw), cw))
	}
	if flags&flagUnread != 0 {
		if flags&flagDay == 0 {
			b.lines = append(b.lines, "")
		}
		b.lines = append(b.lines, spaces(gutterW)+fit(st.RenderUnreadDivider(cw), cw))
	}
	if flags&flagFirst != 0 && flags&(flagDay|flagUnread|flagIRC) == 0 {
		b.lines = append(b.lines, "")
	}
	head := len(b.lines)

	var body []string
	var zs []zone
	switch {
	case m.Kind == core.KindSystem:
		body = a.renderSystem(m, cw)
	case m.Kind == core.KindCall:
		body = a.renderCall(m, cw)
	case flags&flagIRC != 0:
		body, zs = a.renderIRC(m, cw, flags, query)
	default:
		body, zs = a.renderBubble(m, cw, flags, query)
	}

	// Gutter: selection bar on every body line.
	sel := flags&(flagSelected|flagVisual) != 0
	gut := spaces(gutterW)
	if sel {
		gs := lipgloss.NewStyle().Foreground(st.Theme.Palette.Accent)
		if flags&flagFocused == 0 && flags&flagVisual == 0 {
			gs = lipgloss.NewStyle().Foreground(st.Theme.Palette.FgSubtle)
		}
		gut = gs.Render(a.icons.Cursor) + " "
	}
	band := a.selectionBand(flags)
	for _, l := range body {
		l = fit(l, cw)
		if band != nil {
			l = withLineBg(l, band)
		}
		b.lines = append(b.lines, gut+l)
	}
	for _, z := range zs {
		z.x += gutterW
		z.y += head
		b.zones = append(b.zones, z)
	}
	// Links (OSC 8) become click zones.
	for y := head; y < len(b.lines); y++ {
		for _, lr := range linkRanges(b.lines[y]) {
			b.zones = append(b.zones, zone{x: lr.x0, y: y, w: lr.x1 - lr.x0, h: 1, kind: zoneLink, url: lr.url})
		}
	}
	return b
}

func centerText(s string, w int) string {
	sw := width(s)
	if sw >= w {
		return fitEllipsis(s, w)
	}
	l := (w - sw) / 2
	return spaces(l) + s + spaces(w-sw-l)
}

func (a *App) renderSystem(m *core.Message, w int) []string {
	st := a.styles
	text := clean(m.SystemText)
	if text == "" {
		text = clean(m.Body)
	}
	var out []string
	for _, l := range format.Wrap(text, max(w-8, 10)) {
		out = append(out, centerText(st.SystemMessage.UnsetAlign().Render(l), w))
	}
	return out
}

func (a *App) renderCall(m *core.Message, w int) []string {
	st := a.styles
	ci := m.Call
	if ci == nil {
		return a.renderSystem(m, w)
	}
	icon := a.icons.Call
	if ci.Video {
		icon = a.icons.VideoCall
	}
	kind := "voice call"
	if ci.Video {
		kind = "video call"
	}
	if ci.Group {
		kind = "group " + kind
	}
	var text string
	style := st.Subtitle
	switch {
	case ci.Missed:
		icon = a.icons.MissedCall
		text = "Missed " + kind
		style = st.ErrorText
	case ci.Declined:
		text = "Declined " + kind
	case ci.Group && m.SystemText != "":
		// Group calls carry who started them ("Alice started a group call").
		text = clean(m.SystemText)
	case ci.Incoming:
		text = "Incoming " + kind
	default:
		text = "Outgoing " + kind
	}
	if ci.Duration > 0 {
		text += " · " + shortDuration(ci.Duration)
	}
	text += " · " + a.tf.Clock(m.SentAt)
	pill := style.Render(icon + " " + text)
	return []string{centerText(pill, w)}
}

func shortDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h := int(d.Hours())
	mi := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	switch {
	case h > 0:
		return itoa(h) + "h " + itoa(mi) + "m"
	case mi > 0:
		return itoa(mi) + "m " + itoa(s) + "s"
	}
	return itoa(s) + "s"
}

// msgStyles carries the per-bubble base style and helpers.
type msgStyles struct {
	st   *theme.Styles
	base lipgloss.Style
}

// on applies the base background (and reverse in mono) to s.
func (ms msgStyles) on(s lipgloss.Style) lipgloss.Style {
	if ms.base.GetReverse() {
		s = s.Reverse(true)
	}
	if bg := ms.base.GetBackground(); bg != nil {
		if _, none := bg.(lipgloss.NoColor); !none {
			s = s.Background(bg)
		}
	}
	return s
}

func (ms msgStyles) pad(s string, w int) string { return padStyled(s, w, ms.base) }

// bubbleBase returns the base style of a message bubble.
func (a *App) bubbleBase(m *core.Message, flags uint16) lipgloss.Style {
	st := a.styles
	base := st.BubbleIn.UnsetPadding()
	if m.Outgoing {
		base = st.BubbleOut.UnsetPadding()
	}
	if st.Theme.Mono && (flags&flagVisual != 0 || (flags&flagSelected != 0 && flags&flagFocused != 0)) {
		base = st.BubbleSelected.UnsetPadding()
	}
	return base
}

// selectionBand returns the background painted across a selected message
// (like vim's cursorline), or nil. The mono theme uses reverse video on the
// bubble instead.
func (a *App) selectionBand(flags uint16) color.Color {
	p := a.styles.Theme.Palette
	if a.styles.Theme.Mono {
		return nil
	}
	switch {
	case flags&flagVisual != 0:
		return p.Selection
	case flags&flagSelected != 0 && flags&flagFocused != 0:
		return p.BgSelected
	}
	return nil
}

// renderBubble renders a message in the "bubbles" layout. Returned lines are
// at most w cells; zones are relative to the first returned line.
func (a *App) renderBubble(m *core.Message, w int, flags uint16, query string) ([]string, []zone) {
	st := a.styles
	group := a.conv.chat.Kind == core.ChatGroup
	indent := 0
	if group && !m.Outgoing {
		indent = avatarColW
	}
	maxW := max(min(w*72/100, w-indent), 12)
	if w < 50 {
		maxW = max(w-indent-2, 12)
	}
	inner := maxW - 2 // padding

	var out []string
	var zs []zone

	// Sender header for incoming group runs.
	if group && !m.Outgoing && flags&flagFirst != 0 {
		name := clean(m.SenderName)
		if name == "" {
			name = "Unknown"
		}
		av := st.Avatar("", string(m.ID.Author)).Render(theme.Initials(name))
		out = append(out, av+" "+st.SenderStyle(string(m.ID.Author)).Render(fitEllipsisRaw(name, w-avatarColW)))
	}

	// Stickers render without a bubble.
	if m.Sticker != nil && !m.RemoteDeleted {
		rows, z := a.renderSticker(m, inner)
		lines := rows
		foot := a.footer(msgStyles{st: st, base: lipgloss.NewStyle()}, m, flags)
		lines = append(lines, foot)
		x := indent
		if m.Outgoing {
			x = w - max(maxLineWidth(lines), 1)
		}
		y0 := len(out)
		for _, l := range lines {
			out = append(out, spaces(x)+l)
		}
		for _, zz := range z {
			zz.x += x
			zz.y += y0
			zs = append(zs, zz)
		}
		out = append(out, a.reactionLine(m, x, w, m.Outgoing, &zs, len(out))...)
		return out, zs
	}

	ms := msgStyles{st: st, base: a.bubbleBase(m, flags)}
	// Media bubbles are as wide as their image (like Signal): the caption
	// wraps to the image width instead of stretching the bubble.
	textInner := inner
	if mw := a.mediaWidth(m, inner); mw > 0 && mw < inner {
		textInner = max(mw, min(24, inner))
	}
	content, czs := a.bubbleContent(ms, m, textInner, flags, query)

	// Footer: merge into the last text line when it fits.
	foot := a.footer(ms, m, flags)
	fw := width(foot)
	natural := maxLineWidth(content)
	if len(content) > 0 && !content[len(content)-1].fixed {
		last := width(content[len(content)-1].s)
		if last+2+fw <= textInner {
			content[len(content)-1].s = ms.pad(content[len(content)-1].s, max(natural, last+2+fw)-fw) + foot
			natural = max(natural, last+2+fw)
			foot = ""
		}
	}
	if foot != "" {
		natural = max(natural, fw)
		content = append(content, cline{s: foot, right: true})
	}
	bw := min(max(natural, 1), inner)

	x := indent
	if m.Outgoing {
		x = w - (bw + 2)
	}
	padL := ms.base.Render(" ")
	y0 := len(out)
	for _, c := range content {
		s := c.s
		if c.right {
			s = ms.pad("", bw-width(s)) + s
		}
		out = append(out, spaces(x)+padL+ms.pad(fitRaw(s, bw), bw)+padL)
	}
	for _, z := range czs {
		z.x += x + 1
		z.y += y0
		zs = append(zs, z)
	}
	out = append(out, a.reactionLine(m, x, w, m.Outgoing, &zs, len(out))...)
	return out, zs
}

// mediaWidth is the widest inline image/video thumbnail of m at inner
// columns, or 0.
func (a *App) mediaWidth(m *core.Message, inner int) int {
	if m.ViewOnce || m.RemoteDeleted || !a.images.Enabled() {
		return 0
	}
	w := 0
	for _, att := range m.Attachments {
		if !att.IsImage() && !att.IsVideo() {
			continue
		}
		ref := ui.ImageRef{Kind: ui.ImageAttachment, Message: m.ID, Attachment: att}
		cols, _ := a.images.Size(ref, inner, clamp(a.cfg.ImageMaxRows, 1, 100))
		w = max(w, cols)
	}
	return w
}

// fitRaw truncates s to w cells without padding.
func fitRaw(s string, w int) string {
	if width(s) <= w {
		return s
	}
	t := ansi.Truncate(s, w, "")
	if strings.Contains(t, "\x1b") {
		t += sgrReset
	}
	return t
}

// cline is one content line of a bubble. fixed lines (images) keep their
// width and never take the footer.
type cline struct {
	s     string
	fixed bool
	right bool
}

func maxLineWidth[T cline | string](lines []T) int {
	m := 0
	for _, l := range lines {
		switch v := any(l).(type) {
		case cline:
			m = max(m, width(v.s))
		case string:
			m = max(m, width(v))
		}
	}
	return m
}

// bubbleContent renders everything inside a bubble except the footer.
func (a *App) bubbleContent(ms msgStyles, m *core.Message, inner int, flags uint16, query string) ([]cline, []zone) {
	st := ms.st
	p := st.Theme.Palette
	var out []cline
	var zs []zone
	text := func(lines []string) {
		for _, l := range lines {
			out = append(out, cline{s: l})
		}
	}

	if m.RemoteDeleted {
		text([]string{ms.on(st.Edited).Render(a.icons.Delete + " This message was deleted")})
		return out, zs
	}

	if m.StoryContext != nil {
		label := "Replied to a story"
		if m.StoryContext.Emoji != "" {
			label = "Reacted " + clean(m.StoryContext.Emoji) + " to a story"
		}
		bar := ms.on(lipgloss.NewStyle().Foreground(p.AccentAlt)).Render("▎")
		text([]string{bar + ms.on(st.Subtitle).Render(" "+a.icons.Story+" "+label)})
	}

	if q := m.Quote; q != nil {
		text(a.renderQuote(ms, m.ID, q, inner))
	}

	if m.ViewOnce {
		label := a.icons.Image + " View once"
		if m.ViewOnceOpened || m.Outgoing {
			label += " · viewed"
			text([]string{ms.on(st.Subtitle).Render(label)})
		} else {
			text([]string{ms.on(lipgloss.NewStyle().Foreground(p.Accent).Bold(true)).Render(label) +
				ms.on(st.Subtitle).Render(" — press Enter")})
		}
		return out, zs
	}

	for i, att := range m.Attachments {
		lines, fixed := a.renderAttachment(ms, m, att, inner)
		y := len(out)
		lw := 0
		for _, l := range lines {
			out = append(out, cline{s: l, fixed: fixed})
			lw = max(lw, width(l))
		}
		zs = append(zs, zone{x: 0, y: y, w: lw, h: len(lines), kind: zoneAttachment, att: i})
		if c := strings.TrimSpace(att.Caption); c != "" && c != strings.TrimSpace(m.Body) {
			text(format.Wrap(ms.on(lipgloss.NewStyle()).Render(clean(c)), inner))
		}
	}

	if strings.TrimSpace(m.Body) != "" {
		spans := format.Spans(m.Body, m.Ranges, a.mentionName)
		rendered := a.renderSpans(ms, spans, flags&flagRevealed != 0, query)
		text(format.Wrap(rendered, inner))
	}

	if m.Preview != nil {
		lines, z := a.renderPreview(ms, m.Preview, m, inner)
		y := len(out)
		for _, l := range lines {
			out = append(out, cline{s: l})
		}
		for _, zz := range z {
			zz.y += y
			zs = append(zs, zz)
		}
	}
	if m.Poll != nil {
		text(a.renderPoll(ms, m.Poll, inner))
	}
	if m.Contact != nil {
		text(a.renderContactCard(ms, m.Contact, inner))
	}
	if m.GiftBadge {
		text([]string{ms.on(lipgloss.NewStyle().Foreground(p.Accent)).Render("🎁 Gift badge — open it on your phone")})
	}
	if m.PaymentNotice != "" {
		text([]string{ms.on(st.Subtitle).Render("💳 " + clean(m.PaymentNotice))})
	}
	if len(out) == 0 {
		text([]string{ms.on(st.Subtitle).Render("Unsupported message")})
	}
	return out, zs
}

// renderSpans styles message spans on the bubble base, with optional
// search highlighting. Text is sanitized and links become OSC 8 hyperlinks.
func (a *App) renderSpans(ms msgStyles, spans []format.Span, reveal bool, query string) string {
	st := ms.st
	p := st.Theme.Palette
	var b strings.Builder
	q := strings.ToLower(query)
	for _, sp := range spans {
		txt := format.SanitizeDisplay(sp.Text)
		if sp.Spoiler && !reveal {
			for i, line := range strings.Split(txt, "\n") {
				if i > 0 {
					b.WriteByte('\n')
				}
				if line != "" {
					b.WriteString(st.RenderSpoiler(line))
				}
			}
			continue
		}
		s := ms.base
		switch {
		case sp.Mention && sp.MentionACI == a.self.ACI && a.self.ACI != "":
			s = st.MentionSelf
		case sp.Mention:
			s = ms.on(lipgloss.NewStyle().Foreground(p.Mention).Bold(true))
		}
		if sp.Link != "" {
			s = s.Foreground(p.Link).Underline(true)
		}
		if sp.Mono {
			if fg := st.Code.GetForeground(); !isNoColor(fg) {
				s = s.Foreground(p.AccentAlt)
				if _, plain := ms.base.GetBackground().(lipgloss.NoColor); plain {
					s = s.Foreground(fg).Background(st.Code.GetBackground())
				}
			} else {
				s = s.Italic(true)
			}
		}
		if sp.Bold {
			s = s.Bold(true)
		}
		if sp.Italic {
			s = s.Italic(true)
		}
		if sp.Strike {
			s = s.Strikethrough(true)
		}
		hl := s.Reverse(!s.GetReverse())
		for i, line := range strings.Split(txt, "\n") {
			if i > 0 {
				b.WriteByte('\n')
			}
			if line == "" {
				continue
			}
			var seg strings.Builder
			for _, part := range splitMatches(line, q) {
				if part.match {
					seg.WriteString(hl.Render(part.text))
				} else {
					seg.WriteString(s.Render(part.text))
				}
			}
			out := seg.String()
			if sp.Link != "" {
				out = format.Hyperlink(sanitizeURL(sp.Link), out)
			}
			b.WriteString(out)
		}
	}
	return b.String()
}

type textPart struct {
	text  string
	match bool
}

// splitMatches splits s at case-insensitive occurrences of q (lowercase).
func splitMatches(s, q string) []textPart {
	if q == "" {
		return []textPart{{text: s}}
	}
	ls := strings.ToLower(s)
	if len(ls) != len(s) { // case mapping changed byte lengths; don't risk it
		return []textPart{{text: s}}
	}
	var out []textPart
	for {
		i := strings.Index(ls, q)
		if i < 0 {
			out = append(out, textPart{text: s})
			return out
		}
		if i > 0 {
			out = append(out, textPart{text: s[:i]})
		}
		out = append(out, textPart{text: s[i : i+len(q)], match: true})
		s, ls = s[i+len(q):], ls[i+len(q):]
		if s == "" {
			return out
		}
	}
}

// mentionName resolves a mentioned ACI to a display name.
func (a *App) mentionName(aci core.ACI) string {
	if aci == a.self.ACI {
		return a.self.DisplayName()
	}
	for _, mem := range a.conv.chat.Members {
		if mem.ACI == aci {
			return mem.Name
		}
	}
	if c := a.conv.chat.Contact; c != nil && c.ACI == aci {
		return c.DisplayName()
	}
	ctx, cancel := a.queryCtx()
	defer cancel()
	if c, err := a.svc.Contact(ctx, aci); err == nil {
		return c.DisplayName()
	}
	return ""
}

// renderQuote draws the quote block of message owner. The quote's thumbnail
// belongs to the quoting message (Signal sends it with the reply), so it is
// fetched with owner's ID, not the quoted message's.
func (a *App) renderQuote(ms msgStyles, owner core.MessageID, q *core.Quote, inner int) []string {
	st := ms.st
	p := st.Theme.Palette
	barColor := p.AccentAlt
	if !st.Theme.Mono && q.ID.Author != "" {
		barColor = st.SenderColor(string(q.ID.Author))
	}
	bar := ms.on(lipgloss.NewStyle().Foreground(barColor)).Render("▎")
	author := clean(q.AuthorName)
	if q.ID.Author == a.self.ACI && a.self.ACI != "" {
		author = "You"
	}
	if author == "" {
		author = "Unknown"
	}
	var thumb []string
	tw := 0
	if q.Attachment != nil && q.Attachment.IsImage() && a.images.Inline() {
		ref := ui.ImageRef{Kind: ui.ImageAttachment, Message: owner, Attachment: *q.Attachment}
		thumb, _ = a.images.Rows(ref, 4, 2)
		tw = 5
	}
	textW := max(inner-2-tw, 4)
	snippet := ""
	if q.Text != "" {
		snippet = format.Snippet(q.Text, q.Ranges, a.mentionName, textW)
	} else if q.Attachment != nil {
		snippet = attachmentLabel(a.icons, *q.Attachment)
	}
	if !q.OriginalFound {
		snippet = fitEllipsisRaw(snippet, textW)
	}
	l1 := bar + ms.on(lipgloss.NewStyle().Foreground(barColor).Bold(true)).Render(" "+fitEllipsisRaw(author, textW))
	l2 := bar + ms.on(st.Subtitle).Render(" "+fitEllipsisRaw(clean(snippet), textW))
	if len(thumb) == 2 {
		w := max(width(l1), width(l2))
		l1 = ms.pad(l1, w+1) + thumb[0]
		l2 = ms.pad(l2, w+1) + thumb[1]
	}
	return []string{l1, l2}
}

func attachmentLabel(ic theme.IconSet, att core.Attachment) string {
	switch {
	case att.VoiceNote:
		return ic.Voice + " Voice message"
	case att.GIF:
		return ic.Image + " GIF"
	case att.IsImage():
		return ic.Image + " Photo"
	case att.IsVideo():
		return ic.Video + " Video"
	case att.IsAudio():
		return ic.Audio + " Audio"
	}
	name := clean(att.FileName)
	if name == "" {
		name = "File"
	}
	return ic.File + " " + name
}

// renderAttachment returns the lines for one attachment and whether they are
// fixed-width image rows.
func (a *App) renderAttachment(ms msgStyles, m *core.Message, att core.Attachment, inner int) ([]string, bool) {
	st := ms.st
	p := st.Theme.Palette
	ref := ui.ImageRef{Kind: ui.ImageAttachment, Message: m.ID, Attachment: att}
	maxRows := clamp(a.cfg.ImageMaxRows, 1, 100)
	status := func() string {
		switch {
		case att.Failed:
			return ms.on(st.ErrorText).Render(" " + a.icons.Failed + " download failed")
		case !att.Downloaded && att.Progress > 0:
			return ms.on(st.Subtitle).Render(" ↓ " + itoa(int(att.Progress*100)) + "%")
		}
		return ""
	}
	switch {
	case att.VoiceNote || (att.IsAudio() && att.VoiceNote):
		wave := termimg.Waveform(pseudoSamples(att.ID, 64), clamp(inner-14, 8, 40))
		play := ms.on(lipgloss.NewStyle().Foreground(p.Accent).Bold(true)).Render("▶ ")
		return []string{play + ms.on(lipgloss.NewStyle().Foreground(p.FgMuted)).Render(wave) +
			ms.on(st.Timestamp).Render(" "+humanSize(att.Size)) + status()}, false

	case att.IsImage() || att.IsVideo():
		if !a.images.Enabled() {
			return []string{ms.on(st.Subtitle).Render(attachmentLabel(a.icons, att)+" · "+humanSize(att.Size)) + status()}, false
		}
		cols, rows := a.images.Size(ref, inner, maxRows)
		var lines []string
		if att.IsVideo() {
			lines = a.images.placeholder(ref, cols, rows)
		} else {
			lines, _ = a.images.Rows(ref, cols, rows)
		}
		if att.IsVideo() || att.GIF {
			badge := "▶ " + "Video · " + humanSize(att.Size)
			if att.GIF {
				badge = "GIF"
			}
			lines = append(lines, ms.on(lipgloss.NewStyle().Foreground(p.Accent).Bold(true)).Render(badge)+status())
		} else if s := status(); s != "" {
			lines = append(lines, s)
		}
		return lines, true

	default:
		icon := a.icons.File
		if att.IsAudio() {
			icon = a.icons.Audio
		}
		name := clean(att.FileName)
		if name == "" {
			name = "Attachment"
		}
		ext := ""
		if i := strings.LastIndexByte(name, '.'); i > 0 && len(name)-i <= 6 {
			ext = strings.ToUpper(name[i+1:]) + " · "
		}
		iconBox := ms.on(lipgloss.NewStyle().Foreground(p.Accent).Bold(true)).Render(" " + icon + " ")
		l1 := iconBox + ms.on(lipgloss.NewStyle().Bold(true)).Render(fitEllipsisRaw(name, max(inner-5, 4)))
		l2 := ms.pad("", width(iconBox)) + ms.on(st.Timestamp).Render(ext+humanSize(att.Size)) + status()
		return []string{l1, l2}, false
	}
}

// pseudoSamples derives a stable fake waveform from an id (the real samples
// would need the audio decoded).
func pseudoSamples(id string, n int) []float32 {
	h := uint32(2166136261)
	for i := 0; i < len(id); i++ {
		h ^= uint32(id[i])
		h *= 16777619
	}
	out := make([]float32, n)
	for i := range out {
		h ^= h << 13
		h ^= h >> 17
		h ^= h << 5
		env := 0.35 + 0.65*float32(sinApprox(float64(i)/float64(n)*3.14159))
		out[i] = env * (0.25 + 0.75*float32(h%1000)/1000)
	}
	return out
}

func sinApprox(x float64) float64 {
	// Bhaskara I's approximation on [0, π].
	return 16 * x * (3.14159 - x) / (5*3.14159*3.14159 - 4*x*(3.14159-x))
}

func (a *App) renderSticker(m *core.Message, inner int) ([]string, []zone) {
	s := m.Sticker
	if a.images.Inline() {
		ref := ui.ImageRef{Kind: ui.ImageSticker, Message: m.ID, PackID: s.PackID, StickerID: s.StickerID, Attachment: s.Attachment}
		if s.Attachment.Width == 0 {
			ref.Attachment.Width, ref.Attachment.Height = 512, 512
		}
		cols, rows := a.images.Size(ref, min(inner, 20), min(8, a.cfg.ImageMaxRows))
		lines, _ := a.images.Rows(ref, cols, rows)
		return lines, []zone{{x: 0, y: 0, w: cols, h: rows, kind: zoneAttachment, att: -1}}
	}
	emoji := clean(s.Emoji)
	if emoji == "" {
		emoji = a.icons.Sticker
	}
	return []string{a.styles.Subtitle.Render("[ " + emoji + " sticker ]")}, nil
}

func (a *App) renderPreview(ms msgStyles, pv *core.LinkPreview, m *core.Message, inner int) ([]string, []zone) {
	st := ms.st
	p := st.Theme.Palette
	bar := ms.on(lipgloss.NewStyle().Foreground(p.Link)).Render("▎")
	tw := max(inner-2, 4)
	var out []string
	var zs []zone
	if pv.Image != nil && pv.Image.IsImage() && a.images.Enabled() {
		ref := ui.ImageRef{Kind: ui.ImageAttachment, Message: m.ID, Attachment: *pv.Image}
		cols, rows := a.images.Size(ref, tw, min(6, a.cfg.ImageMaxRows))
		lines, _ := a.images.Rows(ref, cols, rows)
		for _, l := range lines {
			out = append(out, bar+ms.base.Render(" ")+l)
		}
	}
	if t := strings.TrimSpace(pv.Title); t != "" {
		for i, l := range format.Wrap(clean(t), tw) {
			if i == 2 {
				break
			}
			out = append(out, bar+ms.on(lipgloss.NewStyle().Bold(true)).Render(" "+l))
		}
	}
	if d := strings.TrimSpace(pv.Description); d != "" {
		for i, l := range format.Wrap(clean(d), tw) {
			if i == 2 {
				break
			}
			out = append(out, bar+ms.on(st.Subtitle).Render(" "+l))
		}
	}
	host := pv.URL
	if u, err := url.Parse(pv.URL); err == nil && u.Host != "" {
		host = u.Host
	}
	link := ms.on(lipgloss.NewStyle().Foreground(p.Link)).Render(" " + a.icons.Link + " " + fitEllipsisRaw(clean(host), tw-3))
	out = append(out, bar+format.Hyperlink(sanitizeURL(pv.URL), link))
	return out, zs
}

func (a *App) renderPoll(ms msgStyles, poll *core.Poll, inner int) []string {
	st := ms.st
	p := st.Theme.Palette
	out := []string{ms.on(lipgloss.NewStyle().Bold(true)).Render(a.icons.Poll + " " + clean(poll.Question))}
	total, top := 0, 0
	for _, o := range poll.Options {
		total += len(o.Voters)
		top = max(top, len(o.Voters))
	}
	barW := clamp(inner-6, 6, 40)
	for _, o := range poll.Options {
		mine := false
		for _, v := range o.Voters {
			if v == a.self.ACI {
				mine = true
			}
		}
		mark := "○ "
		ms2 := lipgloss.NewStyle()
		if mine {
			mark = "✓ "
			ms2 = ms2.Foreground(p.Accent).Bold(true)
		}
		count := itoa(len(o.Voters))
		label := ms.on(ms2).Render(mark) + ms.on(lipgloss.NewStyle()).Render(fitEllipsisRaw(clean(o.Text), max(inner-4-len(count), 4)))
		out = append(out, ms.pad(label, inner-len(count))+ms.on(st.Timestamp).Render(count))
		filled := 0
		if top > 0 {
			filled = len(o.Voters) * barW / max(total, 1)
			if len(o.Voters) > 0 {
				filled = max(filled, 1)
			}
		}
		barStyle := lipgloss.NewStyle().Foreground(p.AccentAlt)
		if mine {
			barStyle = barStyle.Foreground(p.Accent)
		}
		trk := lipgloss.NewStyle().Foreground(p.FgSubtle)
		out = append(out, ms.base.Render("  ")+ms.on(barStyle).Render(strings.Repeat("━", filled))+ms.on(trk).Render(strings.Repeat("─", barW-filled)))
	}
	foot := itoa(total) + " votes"
	if total == 1 {
		foot = "1 vote"
	}
	if poll.Ended {
		foot += " · poll ended"
	} else if poll.AllowMultiple {
		foot += " · multiple choice"
	}
	out = append(out, ms.on(st.Timestamp).Render(foot))
	return out
}

func (a *App) renderContactCard(ms msgStyles, c *core.SharedContact, inner int) []string {
	st := ms.st
	name := clean(c.Name)
	if name == "" {
		name = "Contact"
	}
	av := st.Avatar("", name).Render(theme.Initials(name))
	out := []string{av + ms.on(lipgloss.NewStyle().Bold(true)).Render(" "+fitEllipsisRaw(name, inner-5))}
	var details []string
	if c.Org != "" {
		details = append(details, clean(c.Org))
	}
	details = append(details, cleanAll(c.Phones)...)
	details = append(details, cleanAll(c.Emails)...)
	for _, d := range details {
		out = append(out, ms.pad("", 5)+ms.on(st.Subtitle).Render(fitEllipsisRaw(d, inner-5)))
	}
	return out
}

func cleanAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, clean(s))
	}
	return out
}

// footer renders "pinned · timer · edited 12:41 ✓✓" on the bubble base.
func (a *App) footer(ms msgStyles, m *core.Message, flags uint16) string {
	st := ms.st
	ts := ms.on(st.Timestamp)
	var parts []string
	if m.Pinned {
		parts = append(parts, ms.on(st.PinnedIcon).Render(a.icons.Pinned))
	}
	if exp := m.ExpiresAt(); !exp.IsZero() {
		parts = append(parts, ts.Render(a.icons.Clock+" "+format.FormatRemaining(exp.Sub(a.now()))))
	} else if m.ExpiresIn > 0 {
		parts = append(parts, ts.Render(a.icons.Clock+" "+format.FormatDuration(m.ExpiresIn)))
	}
	if m.Edited && !m.RemoteDeleted {
		parts = append(parts, ms.on(st.Edited).Render("edited"))
	}
	parts = append(parts, ts.Render(a.tf.Clock(m.SentAt)))
	if m.Outgoing {
		if d := st.RenderDelivery(a.icons, m.Status); d != "" {
			parts = append(parts, ms.on(st.Delivery(m.Status)).Render(a.icons.Delivery(m.Status)))
		}
	}
	sp := ms.base.Render(" ")
	return strings.Join(parts, sp)
}

// reactionLine renders reaction pills under a bubble.
func (a *App) reactionLine(m *core.Message, x, w int, right bool, zs *[]zone, y int) []string {
	if len(m.Reactions) == 0 {
		return nil
	}
	st := a.styles
	type agg struct {
		emoji string
		n     int
		mine  bool
	}
	var order []*agg
	idx := map[string]*agg{}
	for _, r := range m.Reactions {
		e := clean(r.Emoji)
		g := idx[e]
		if g == nil {
			g = &agg{emoji: e}
			idx[e] = g
			order = append(order, g)
		}
		g.n++
		if r.From == a.self.ACI {
			g.mine = true
		}
	}
	var pills []string
	var widths []int
	for _, g := range order {
		label := g.emoji
		if g.n > 1 {
			label += " " + itoa(g.n)
		}
		s := st.Reaction
		if g.mine {
			s = st.ReactionSelf
		}
		pill := s.Render(label)
		pills = append(pills, pill)
		widths = append(widths, width(pill))
	}
	line := strings.Join(pills, " ")
	lw := width(line)
	px := x + 1
	if right {
		px = max(w-lw-1, 0)
	}
	cx := px
	for i, g := range order {
		*zs = append(*zs, zone{x: cx, y: y, w: widths[i], h: 1, kind: zoneReaction, emoji: g.emoji})
		cx += widths[i] + 1
	}
	return []string{spaces(px) + line}
}

// ---- IRC layout -------------------------------------------------------------------

func (a *App) renderIRC(m *core.Message, w int, flags uint16, query string) ([]string, []zone) {
	st := a.styles
	p := st.Theme.Palette
	base := lipgloss.NewStyle()
	if st.Theme.Mono && (flags&flagVisual != 0 || (flags&flagSelected != 0 && flags&flagFocused != 0)) {
		base = st.Selection
	}
	ms := msgStyles{st: st, base: base}
	nickW := 12
	if w < 50 {
		nickW = 8
	}
	clock := a.tf.Clock(m.SentAt)
	timeW := width(clock)
	// "12:41 <nick> │ text ": the separator is followed by one space and
	// the text keeps a one-cell right margin, like the bubble layout.
	textX := timeW + 1 + nickW + 1
	textW := max(w-textX-2, 8)
	var out []string
	var zs []zone

	name := clean(m.SenderName)
	if m.Outgoing {
		name = "you"
	}
	nick := ""
	if flags&flagFirst != 0 {
		ns := st.SenderStyle(string(m.ID.Author))
		if m.Outgoing {
			ns = lipgloss.NewStyle().Foreground(p.Accent).Bold(true)
		}
		nick = ms.on(ns).Render(fitEllipsisRaw(name, nickW-1)) + ms.base.Render(" ")
	}
	nickCol := ms.pad("", nickW-width(nick)) + nick
	prefix := ms.on(st.Timestamp).Render(clock) + ms.base.Render(" ") + nickCol + ms.on(lipgloss.NewStyle().Foreground(p.FgSubtle)).Render("│")
	cont := ms.pad("", timeW+1+nickW) + ms.on(lipgloss.NewStyle().Foreground(p.FgSubtle)).Render("│")

	var content []cline
	var czs []zone
	if m.Sticker != nil && !m.RemoteDeleted {
		rows, z := a.renderSticker(m, min(textW, 16))
		for _, r := range rows {
			content = append(content, cline{s: r, fixed: true})
		}
		czs = z
	} else {
		content, czs = a.bubbleContent(ms, m, textW, flags, query)
	}
	foot := ""
	var fp []string
	if m.Edited && !m.RemoteDeleted {
		fp = append(fp, ms.on(st.Edited).Render("(edited)"))
	}
	if m.Outgoing {
		if g := a.icons.Delivery(m.Status); g != "" {
			fp = append(fp, ms.on(st.Delivery(m.Status)).Render(g))
		}
	}
	if exp := m.ExpiresAt(); !exp.IsZero() {
		fp = append(fp, ms.on(st.Timestamp).Render(a.icons.Clock+format.FormatRemaining(exp.Sub(a.now()))))
	}
	if m.Pinned {
		fp = append(fp, ms.on(st.PinnedIcon).Render(a.icons.Pinned))
	}
	foot = strings.Join(fp, ms.base.Render(" "))
	if foot != "" && len(content) > 0 {
		last := &content[len(content)-1]
		if !last.fixed && width(last.s)+1+width(foot) <= textW {
			last.s += ms.base.Render(" ") + foot
		} else {
			content = append(content, cline{s: foot})
		}
	}
	for i, c := range content {
		pre := cont
		if i == 0 {
			pre = prefix
		}
		out = append(out, pre+ms.base.Render(" ")+ms.pad(c.s, textW))
	}
	for _, z := range czs {
		z.x += textX + 1
		zs = append(zs, z)
	}
	if len(m.Reactions) > 0 {
		out = append(out, a.reactionLine(m, textX, w, false, &zs, len(out))...)
	}
	return out, zs
}

// ---- links ---------------------------------------------------------------------------

type linkRange struct {
	x0, x1 int
	url    string
}

// linkRanges finds OSC 8 hyperlinks in a rendered line and returns their
// cell ranges.
func linkRanges(line string) []linkRange {
	if !strings.Contains(line, "\x1b]8;") {
		return nil
	}
	var out []linkRange
	x := 0
	cur := ""
	start := 0
	i := 0
	for i < len(line) {
		if line[i] == 0x1b && i+1 < len(line) {
			switch line[i+1] {
			case ']':
				end := strings.Index(line[i:], "\x1b\\")
				bel := strings.IndexByte(line[i:], 0x07)
				term := 2
				if end < 0 || (bel >= 0 && bel < end) {
					end, term = bel, 1
				}
				if end < 0 {
					return out
				}
				seq := line[i+2 : i+end]
				if strings.HasPrefix(seq, "8;") {
					parts := strings.SplitN(seq, ";", 3)
					u := ""
					if len(parts) == 3 {
						u = parts[2]
					}
					if u == "" && cur != "" {
						out = append(out, linkRange{x0: start, x1: x, url: cur})
						cur = ""
					} else if u != "" {
						cur, start = u, x
					}
				}
				i += end + term
				continue
			case '[':
				j := i + 2
				for j < len(line) && (line[j] < 0x40 || line[j] > 0x7e) {
					j++
				}
				i = j + 1
				continue
			}
		}
		// Printable run up to the next escape.
		j := strings.IndexByte(line[i:], 0x1b)
		if j < 0 {
			j = len(line) - i
		}
		if j == 0 {
			j = 1
		}
		x += ansi.StringWidth(line[i : i+j])
		i += j
	}
	if cur != "" {
		out = append(out, linkRange{x0: start, x1: x, url: cur})
	}
	return out
}

// isNoColor reports whether c is lipgloss.NoColor.
func isNoColor(c color.Color) bool {
	_, ok := c.(lipgloss.NoColor)
	return c == nil || ok
}

// sanitizeURL strips everything from an untrusted URL that could confuse a
// terminal or spoof what a hover shows: controls, whitespace and bidi
// formatting characters.
func sanitizeURL(u string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r <= 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
			return -1
		case r >= 0x200b && r <= 0x200f, r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069, r == 0xfeff:
			return -1
		}
		return r
	}, u)
}
