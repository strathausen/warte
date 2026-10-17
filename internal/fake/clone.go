package fake

import (
	"maps"
	"slices"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"warte/internal/core"
)

// This file holds deep-copy helpers (so callers never alias internal state)
// and the derivations performed when state is exported: display names,
// unread counts, previews.

func cloneRanges(r []core.BodyRange) []core.BodyRange { return slices.Clone(r) }

func cloneAttachment(a *core.Attachment) *core.Attachment {
	if a == nil {
		return nil
	}
	c := *a
	return &c
}

func cloneSticker(st *core.Sticker) *core.Sticker {
	if st == nil {
		return nil
	}
	c := *st
	return &c
}

func clonePoll(p *core.Poll) *core.Poll {
	if p == nil {
		return nil
	}
	c := *p
	c.Options = make([]core.PollOption, len(p.Options))
	for i, o := range p.Options {
		c.Options[i] = core.PollOption{Text: o.Text, Voters: slices.Clone(o.Voters)}
	}
	return &c
}

func cloneMessage(m *core.Message) core.Message {
	c := *m
	c.Ranges = cloneRanges(m.Ranges)
	if m.Quote != nil {
		q := *m.Quote
		q.Ranges = cloneRanges(m.Quote.Ranges)
		q.Attachment = cloneAttachment(m.Quote.Attachment)
		c.Quote = &q
	}
	c.Attachments = slices.Clone(m.Attachments)
	c.Sticker = cloneSticker(m.Sticker)
	if m.Preview != nil {
		p := *m.Preview
		p.Image = cloneAttachment(m.Preview.Image)
		c.Preview = &p
	}
	if m.Contact != nil {
		sc := *m.Contact
		sc.Phones = slices.Clone(m.Contact.Phones)
		sc.Emails = slices.Clone(m.Contact.Emails)
		c.Contact = &sc
	}
	c.Poll = clonePoll(m.Poll)
	c.Reactions = slices.Clone(m.Reactions)
	if m.EditHistory != nil {
		c.EditHistory = make([]core.EditRevision, len(m.EditHistory))
		for i, r := range m.EditHistory {
			c.EditHistory[i] = core.EditRevision{Body: r.Body, Ranges: cloneRanges(r.Ranges), At: r.At}
		}
	}
	c.PerRecipient = maps.Clone(m.PerRecipient)
	if m.Call != nil {
		ci := *m.Call
		c.Call = &ci
	}
	if m.StoryContext != nil {
		sr := *m.StoryContext
		c.StoryContext = &sr
	}
	return c
}

func cloneStory(st *core.Story) core.Story {
	c := *st
	if st.Text != nil {
		t := *st.Text
		t.Gradient = slices.Clone(st.Text.Gradient)
		c.Text = &t
	}
	c.Media = cloneAttachment(st.Media)
	return c
}

func clonePack(p *core.StickerPack) core.StickerPack {
	c := *p
	c.Cover = cloneSticker(p.Cover)
	c.Stickers = slices.Clone(p.Stickers)
	return c
}

// nameLocked resolves an ACI to a display name.
func (s *Service) nameLocked(aci core.ACI) string {
	if aci == s.self.ACI {
		return s.self.DisplayName()
	}
	if c, ok := s.contacts[aci]; ok {
		return c.DisplayName()
	}
	return "Unknown"
}

// exportMsgLocked returns a caller-owned copy with names resolved.
func (s *Service) exportMsgLocked(m *core.Message) core.Message {
	c := cloneMessage(m)
	if c.ID.Author != "" {
		c.SenderName = s.nameLocked(c.ID.Author)
	}
	for i := range c.Reactions {
		c.Reactions[i].FromName = s.nameLocked(c.Reactions[i].From)
	}
	return c
}

// exportChatLocked returns a caller-owned copy of the chat with derived fields.
func (s *Service) exportChatLocked(cs *chatState) core.Chat {
	c := cs.chat
	c.Members = s.exportMembersLocked(cs.chat.Members)
	c.PendingMembers = s.exportMembersLocked(cs.chat.PendingMembers)
	c.RequestingJoin = s.exportMembersLocked(cs.chat.RequestingJoin)
	if aci := cs.chat.ID.ACI(); aci != "" {
		if aci == s.self.ACI {
			self := s.self
			c.Contact = &self
			c.Title = "Note to Self"
			c.AvatarID = s.self.AvatarID
		} else if ct, ok := s.contacts[aci]; ok {
			cc := *ct
			c.Contact = &cc
			c.Title = cc.DisplayName()
			c.Blocked = cc.Blocked
			if c.Description == "" {
				c.Description = strings.TrimSpace(cc.AboutEmoji + " " + cc.About)
			}
			c.AvatarID = cc.AvatarID
		}
	}
	now := s.now()
	c.Unread, c.MentionsUnread = 0, 0
	c.LastMessage = nil
	for i := len(cs.msgs) - 1; i >= 0; i-- {
		m := cs.msgs[i]
		if !visible(m, now) {
			continue
		}
		if c.LastMessage == nil {
			c.LastMessage = s.previewLocked(m)
		}
		if m.ID.Timestamp <= cs.readUpTo {
			break
		}
		if isUnreadCandidate(m) {
			c.Unread++
			if m.MentionsSelf {
				c.MentionsUnread++
			}
		}
	}
	return c
}

func (s *Service) exportMembersLocked(ms []core.Member) []core.Member {
	if ms == nil {
		return nil
	}
	out := make([]core.Member, len(ms))
	for i, m := range ms {
		out[i] = m
		out[i].Name = s.nameLocked(m.ACI)
	}
	return out
}

func isUnreadCandidate(m *core.Message) bool {
	return !m.Outgoing && (m.Kind == core.KindNormal || m.Kind == core.KindStoryReply ||
		(m.Kind == core.KindCall && m.Call != nil && m.Call.Missed))
}

// visible reports whether a message has not yet disappeared.
func visible(m *core.Message, now time.Time) bool {
	exp := m.ExpiresAt()
	return exp.IsZero() || now.Before(exp)
}

// lastActivity is the sort key for the chat list.
func (s *Service) lastActivityLocked(cs *chatState) time.Time {
	now := s.now()
	for i := len(cs.msgs) - 1; i >= 0; i-- {
		if visible(cs.msgs[i], now) {
			return cs.msgs[i].SentAt
		}
	}
	return cs.created
}

func (s *Service) previewLocked(m *core.Message) *core.MessagePreview {
	p := &core.MessagePreview{
		ID:       m.ID,
		Outgoing: m.Outgoing,
		At:       m.SentAt,
		Status:   m.Status,
		Text:     s.previewTextLocked(m),
	}
	if !m.Outgoing && m.Kind != core.KindSystem && m.Kind != core.KindCall {
		// System and call lines name who did it themselves (as in the store).
		p.Sender = s.nameLocked(m.ID.Author)
	}
	return p
}

// previewTextLocked renders a single-line summary of a message.
func (s *Service) previewTextLocked(m *core.Message) string {
	switch {
	case m.RemoteDeleted:
		return "This message was deleted"
	case m.Kind == core.KindSystem:
		return oneLine(m.SystemText)
	case m.Kind == core.KindCall && m.Call != nil:
		return "📞 " + callLabel(*m.Call)
	case m.ViewOnce:
		if m.ViewOnceOpened {
			return "View-once media · viewed"
		}
		return "📷 View-once media"
	case m.Sticker != nil:
		return strings.TrimSpace(m.Sticker.Emoji + " Sticker")
	case m.Poll != nil:
		return "📊 Poll: " + oneLine(m.Poll.Question)
	case m.Contact != nil:
		return "👤 " + m.Contact.Name
	}
	text := oneLine(s.plainTextLocked(m.Body, m.Ranges, true))
	if m.Kind == core.KindStoryReply && m.StoryContext != nil && m.StoryContext.Emoji != "" && text == "" {
		text = "Reacted " + m.StoryContext.Emoji + " to a story"
	}
	if len(m.Attachments) > 0 {
		label := attachmentLabel(m.Attachments[0])
		if text == "" {
			text = m.Attachments[0].Caption
		}
		if text != "" {
			return truncate(label+" "+text, 120)
		}
		return label
	}
	return truncate(text, 120)
}

func attachmentLabel(a core.Attachment) string {
	switch {
	case a.VoiceNote:
		return "🎤 Voice message"
	case a.GIF:
		return "🎞️ GIF"
	case a.IsImage():
		return "📷 Photo"
	case a.IsVideo():
		return "🎥 Video"
	case a.IsAudio():
		return "🎵 Audio"
	}
	if a.FileName != "" {
		return "📎 " + a.FileName
	}
	return "📎 File"
}

func callLabel(c core.CallInfo) string {
	kind := "voice call"
	if c.Video {
		kind = "video call"
	}
	if c.Group {
		kind = "group " + kind
	}
	switch {
	case c.Missed:
		return "Missed " + kind
	case c.Declined:
		return "Declined " + kind
	case c.Incoming:
		return "Incoming " + kind
	}
	return "Outgoing " + kind
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n-1])) + "…"
}

// plainTextLocked resolves mention placeholders to "@Name" and, if
// hideSpoilers is set, replaces spoiler runs with "░░░".
func (s *Service) plainTextLocked(body string, ranges []core.BodyRange, hideSpoilers bool) string {
	if len(ranges) == 0 {
		return body
	}
	mentions := map[int]core.ACI{}
	var spoilers [][2]int
	for _, r := range ranges {
		switch r.Style {
		case core.StyleMention:
			mentions[r.Start] = r.MentionACI
		case core.StyleSpoiler:
			if hideSpoilers {
				spoilers = append(spoilers, [2]int{r.Start, r.Start + r.Length})
			}
		}
	}
	var b strings.Builder
	pos := 0
	inSpoiler := -1
	for _, r := range body {
		width := utf16.RuneLen(r)
		if width < 0 {
			width = 1
		}
		sp := -1
		for i, rg := range spoilers {
			if pos >= rg[0] && pos < rg[1] {
				sp = i
				break
			}
		}
		switch {
		case sp >= 0:
			if sp != inSpoiler {
				b.WriteString("░░░")
			}
		case r == '￼' && mentions[pos] != "":
			b.WriteString("@" + s.nameLocked(mentions[pos]))
		default:
			b.WriteRune(r)
		}
		inSpoiler = sp
		pos += width
	}
	return b.String()
}

// utf16Len returns the length of s in UTF-16 code units.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if w := utf16.RuneLen(r); w > 0 {
			n += w
		} else {
			n++
		}
	}
	return n
}

func (cs *chatState) find(id core.MessageID) *core.Message {
	if i := cs.index(id); i >= 0 {
		return cs.msgs[i]
	}
	return nil
}

// index returns the position of id in cs.msgs, or -1.
func (cs *chatState) index(id core.MessageID) int {
	i, ok := slices.BinarySearchFunc(cs.msgs, id.Timestamp, func(m *core.Message, ts uint64) int {
		switch {
		case m.ID.Timestamp < ts:
			return -1
		case m.ID.Timestamp > ts:
			return 1
		}
		return 0
	})
	if !ok {
		return -1
	}
	for j := i; j < len(cs.msgs) && cs.msgs[j].ID.Timestamp == id.Timestamp; j++ {
		if cs.msgs[j].ID.Author == id.Author {
			return j
		}
	}
	return -1
}

// insert adds m keeping cs.msgs sorted by timestamp.
func (cs *chatState) insert(m *core.Message) {
	i := len(cs.msgs)
	for i > 0 && cs.msgs[i-1].ID.Timestamp > m.ID.Timestamp {
		i--
	}
	cs.msgs = slices.Insert(cs.msgs, i, m)
}

// sanitizeRanges drops body ranges a frontend could not render safely:
// negative or empty spans, spans past the end of body (in UTF-16 code units),
// unknown styles, and mentions that do not cover exactly one U+FFFC or lack
// an ACI. The result is sorted by Start.
func sanitizeRanges(body string, ranges []core.BodyRange) []core.BodyRange {
	if len(ranges) == 0 {
		return nil
	}
	units := utf16.Encode([]rune(body))
	out := make([]core.BodyRange, 0, len(ranges))
	for _, r := range ranges {
		switch {
		case r.Start < 0 || r.Length <= 0 || r.Start > len(units) || r.Length > len(units)-r.Start:
			continue
		case r.Style <= core.StyleNone || r.Style > core.StyleMention:
			continue
		case r.Style == core.StyleMention && (r.Length != 1 || units[r.Start] != '￼' || r.MentionACI == ""):
			continue
		}
		if r.Style != core.StyleMention {
			r.MentionACI = ""
		}
		out = append(out, r)
	}
	slices.SortStableFunc(out, func(a, b core.BodyRange) int { return a.Start - b.Start })
	if len(out) == 0 {
		return nil
	}
	return out
}

// inlineMentionsLocked replaces each mention placeholder with "@Name" plain
// text, shifting the other ranges so their UTF-16 offsets stay correct. It is
// used where mentions must not survive (forwarding).
func (s *Service) inlineMentionsLocked(body string, ranges []core.BodyRange) (string, []core.BodyRange) {
	ranges = sanitizeRanges(body, ranges)
	var mentions []core.BodyRange
	var rest []core.BodyRange
	for _, r := range ranges {
		if r.Style == core.StyleMention {
			mentions = append(mentions, r)
		} else {
			rest = append(rest, r)
		}
	}
	if len(mentions) == 0 {
		return body, ranges
	}
	units := utf16.Encode([]rune(body))
	// Walk mentions from the end so earlier offsets stay valid.
	for i := len(mentions) - 1; i >= 0; i-- {
		m := mentions[i]
		name := utf16.Encode([]rune("@" + s.nameLocked(m.MentionACI)))
		delta := len(name) - 1
		units = slices.Concat(units[:m.Start], name, units[m.Start+1:])
		for j := range rest {
			switch r := &rest[j]; {
			case r.Start > m.Start:
				r.Start += delta
			case r.Start+r.Length > m.Start:
				r.Length += delta
			}
		}
	}
	if len(rest) == 0 {
		rest = nil
	}
	return string(utf16.Decode(units)), rest
}
