package store

import (
	"strings"
	"unicode"
	"unicode/utf16"

	"warte/internal/core"
)

// previewMaxRunes caps MessagePreview.Text; the UI truncates further.
const previewMaxRunes = 160

// NameFunc resolves an ACI to a display name; it may return "".
type NameFunc func(core.ACI) string

// Preview builds the chat-list preview of m. Mentions are rendered as
// "@Name" using name (which may be nil), spoilers are masked, whitespace is
// collapsed to one line and attachments, stickers, polls, calls etc. are
// described with an emoji label (e.g. "📷 Photo").
func Preview(m core.Message, name NameFunc) core.MessagePreview {
	p := core.MessagePreview{
		ID:       m.ID,
		Outgoing: m.Outgoing,
		Text:     PreviewText(m, name),
		At:       m.SentAt,
		Status:   m.Status,
	}
	if previewHasSender(m) {
		p.Sender = m.SenderName
		if name != nil {
			if n := name(m.ID.Author); n != "" {
				p.Sender = n
			}
		}
	}
	return p
}

// previewHasSender reports whether the chat list prefixes m's preview with
// its sender ("Alice: …"). System and call messages carry their own wording
// ("Alice Chen started a group call"), so a prefix would repeat the name.
func previewHasSender(m core.Message) bool {
	return !m.Outgoing && m.Kind != core.KindSystem && m.Kind != core.KindCall
}

// groupCallText reports whether a call is shown by its system text.
func groupCallText(m core.Message) bool {
	c := m.Call
	return c != nil && c.Group && !c.Missed && !c.Declined && m.SystemText != ""
}

// PreviewText returns the single-line preview text for m (see Preview).
func PreviewText(m core.Message, name NameFunc) string {
	if m.RemoteDeleted {
		return "This message was deleted"
	}
	switch m.Kind {
	case core.KindSystem:
		return oneLine(m.SystemText)
	case core.KindCall:
		if groupCallText(m) {
			// "Alice started a group call", as in the conversation.
			icon := "📞 "
			if m.Call.Video {
				icon = "📹 "
			}
			return icon + oneLine(m.SystemText)
		}
		return callLabel(m.Call)
	}
	if m.ViewOnce {
		if m.ViewOnceOpened {
			return "View-once media (viewed)"
		}
		return "📷 View-once media"
	}
	body := oneLine(RenderBody(m.Body, m.Ranges, name))
	var label string
	switch {
	case m.Sticker != nil:
		label = strings.TrimSpace(m.Sticker.Emoji + " Sticker")
	case len(m.Attachments) > 0:
		label = attachmentLabel(m.Attachments)
	case m.Poll != nil:
		label = "📊 Poll: " + oneLine(m.Poll.Question)
	case m.Contact != nil:
		label = "👤 " + oneLine(m.Contact.Name)
	case m.GiftBadge:
		label = "🎁 Gift badge"
	case m.PaymentNotice != "":
		label = "💳 " + oneLine(m.PaymentNotice)
	}
	if m.Kind == core.KindStoryReply && m.StoryContext != nil && m.StoryContext.Emoji != "" && body == "" {
		label = "Reacted " + m.StoryContext.Emoji + " to a story"
	}
	text := body
	switch {
	case label != "" && body != "":
		text = label + " · " + body
	case label != "":
		text = label
	}
	return truncateRunes(text, previewMaxRunes)
}

func attachmentLabel(as []core.Attachment) string {
	a := as[0]
	var l string
	switch {
	case a.VoiceNote:
		l = "🎤 Voice message"
	case a.GIF:
		l = "🎞️ GIF"
	case a.IsImage():
		l = "📷 Photo"
	case a.IsVideo():
		l = "🎥 Video"
	case a.IsAudio():
		l = "🔊 Audio"
	default:
		l = "📎 File"
		if a.FileName != "" {
			l = "📎 " + oneLine(a.FileName)
		}
	}
	if len(as) > 1 && (a.IsImage() || a.IsVideo()) && !a.GIF {
		l = "📷 Album"
	}
	return l
}

func callLabel(c *core.CallInfo) string {
	if c == nil {
		return "📞 Call"
	}
	kind := "voice call"
	icon := "📞"
	if c.Video {
		kind = "video call"
		icon = "📹"
	}
	if c.Group {
		kind = "group " + kind
	}
	switch {
	case c.Missed:
		return icon + " Missed " + kind
	case c.Declined:
		return icon + " Declined " + kind
	case c.Incoming:
		return icon + " Incoming " + kind
	default:
		return icon + " Outgoing " + kind
	}
}

// RenderBody replaces mention placeholders (U+FFFC) with "@Name" and masks
// spoiler ranges with "■■■■". Range offsets are UTF-16 code units.
func RenderBody(body string, ranges []core.BodyRange, name NameFunc) string {
	if len(ranges) == 0 {
		return body
	}
	units := utf16.Encode([]rune(body))
	mentions := map[int]core.ACI{}
	type span struct{ start, end int }
	var spoilers []span
	for _, r := range ranges {
		switch r.Style {
		case core.StyleMention:
			mentions[r.Start] = r.MentionACI
		case core.StyleSpoiler:
			spoilers = append(spoilers, span{r.Start, r.Start + r.Length})
		}
	}
	inSpoiler := func(off int) (span, bool) {
		for _, s := range spoilers {
			if off >= s.start && off < s.end {
				return s, true
			}
		}
		return span{}, false
	}
	var b strings.Builder
	maskedUpTo := -1
	for i := 0; i < len(units); {
		if s, ok := inSpoiler(i); ok {
			if maskedUpTo < s.end {
				b.WriteString("■■■■")
				maskedUpTo = s.end
			}
			i = min(s.end, len(units))
			continue
		}
		r := rune(units[i])
		n := 1
		if utf16.IsSurrogate(r) {
			// Only a valid high+low pair forms one rune; a lone surrogate
			// (e.g. a range boundary splitting a pair) is one U+FFFD.
			r = unicode.ReplacementChar
			if i+1 < len(units) {
				if d := utf16.DecodeRune(rune(units[i]), rune(units[i+1])); d != unicode.ReplacementChar {
					r, n = d, 2
				}
			}
		}
		if r == '￼' {
			if aci, ok := mentions[i]; ok {
				nm := ""
				if name != nil {
					nm = name(aci)
				}
				if nm == "" {
					nm = "Unknown"
				}
				b.WriteString("@" + nm)
				i += n
				continue
			}
		}
		b.WriteRune(r)
		i += n
	}
	return b.String()
}

// oneLine collapses s to a single line of display-safe text: runs of
// whitespace and control characters (C0/C1, including ESC, so untrusted text
// cannot inject terminal escape sequences) become one space, and Unicode
// bidi embedding/override/isolate controls are dropped.
func oneLine(s string) string {
	return strings.Join(strings.FieldsFunc(stripBidi(s), func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}), " ")
}

// isBidiControl reports whether r is an explicit bidi formatting character
// (LRE…RLO, LRI…PDI, LRM/RLM/ALM), which can visually reorder text.
func isBidiControl(r rune) bool {
	switch {
	case r >= '\u202a' && r <= '\u202e', r >= '\u2066' && r <= '\u2069':
		return true
	case r == '\u200e', r == '\u200f', r == '\u061c':
		return true
	}
	return false
}

func stripBidi(s string) string {
	if !strings.ContainsFunc(s, isBidiControl) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if isBidiControl(r) {
			return -1
		}
		return r
	}, s)
}

// indexText is the message body as stored in the full-text index column:
// control characters (including the snippet markers \x02/\x03, so message
// text cannot forge match highlights) become spaces and bidi controls are
// dropped. Snippets are cut from this text, so they are display-safe too.
func indexText(s string) string {
	s = stripBidi(s)
	if !strings.ContainsFunc(s, unicode.IsControl) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimRightFunc(string(r[:n-1]), unicode.IsSpace) + "…"
}

// Media flag bits stored in warte_messages.media_flags for Search HasType.
const (
	flagImage = 1 << iota
	flagVideo
	flagAudio
	flagFile
	flagLink
)

var hasTypeFlags = map[string]int{
	"image": flagImage,
	"video": flagVideo,
	"audio": flagAudio,
	"file":  flagFile,
	"link":  flagLink,
}

func mediaFlags(m core.Message) int {
	f := 0
	for _, a := range m.Attachments {
		switch {
		case a.IsImage():
			f |= flagImage
		case a.IsVideo():
			f |= flagVideo
		case a.IsAudio():
			f |= flagAudio
		default:
			f |= flagFile
		}
	}
	if m.Preview != nil || strings.Contains(m.Body, "http://") || strings.Contains(m.Body, "https://") {
		f |= flagLink
	}
	return f
}

// countsAsUnread reports whether a newly stored message should bump the
// chat's unread counter.
func countsAsUnread(m core.Message) bool {
	if m.Outgoing || m.RemoteDeleted {
		return false
	}
	switch m.Kind {
	case core.KindNormal, core.KindStoryReply:
		return true
	case core.KindCall:
		return m.Call != nil && m.Call.Incoming && m.Call.Missed
	}
	return false
}
