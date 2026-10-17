package backend

import (
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"

	"warte/internal/core"
)

// longTextContentType marks the attachment that carries the full text of a
// message whose body was truncated by the sender.
const longTextContentType = "text/x-signal-plain"

// convCtx carries what a pure conversion needs to know about the envelope a
// message arrived in.
type convCtx struct {
	Self       core.ACI    // our own ACI
	Chat       core.ChatID // conversation the message belongs to
	Sender     core.ACI    // author (Self for sync transcripts)
	ServerTS   uint64      // server timestamp, ms
	ReceivedAt time.Time   // local receive time
}

// attRole says where an attachment pointer appears inside a message.
type attRole string

const (
	roleBody     attRole = "body"
	roleSticker  attRole = "sticker"
	rolePreview  attRole = "preview"
	roleQuote    attRole = "quote"
	roleLongText attRole = "longtext"
)

// attachmentRef ties a converted core.Attachment ID to the Signal pointer
// needed to download it.
type attachmentRef struct {
	ID            string
	Role          attRole
	Pointer       *signalpb.AttachmentPointer
	PlaintextHash []byte // from history transfer when the pointer has no digest
}

// converted is the result of converting a content-bearing DataMessage.
type converted struct {
	Msg      core.Message
	Atts     []attachmentRef // every downloadable pointer referenced by Msg
	LongText *attachmentRef  // full body to fetch and substitute, if any
}

// dmKind classifies what a DataMessage does.
type dmKind int

const (
	dmEmpty         dmKind = iota // nothing warte shows (e.g. bare profile key update)
	dmContent                     // a message to store and show
	dmTimerUpdate                 // disappearing timer change
	dmReaction                    // add/remove a reaction (or react to a story)
	dmDelete                      // delete for everyone by the author
	dmAdminDelete                 // delete for everyone by a group admin
	dmGroupUpdate                 // signed group change
	dmGroupContext                // group context without change: creation/refresh notice
	dmPollVote                    // vote on a poll
	dmPollTerminate               // end a poll
	dmPin                         // pin a message
	dmUnpin                       // unpin a message
	dmUnsupported                 // needs a newer protocol version than we speak
)

// String names the kind for logs.
func (k dmKind) String() string {
	names := [...]string{"empty", "content", "timer", "reaction", "delete", "admin_delete",
		"group_update", "group_context", "poll_vote", "poll_terminate", "pin", "unpin", "unsupported"}
	if int(k) < len(names) {
		return names[k]
	}
	return "kind(" + strconv.Itoa(int(k)) + ")"
}

// hasContent reports whether dm carries something to show as a message.
func hasContent(dm *signalpb.DataMessage) bool {
	return dm.GetBody() != "" || len(dm.GetAttachments()) > 0 || len(dm.GetContact()) > 0 ||
		dm.GetSticker() != nil || dm.GetPayment() != nil || dm.GetGiftBadge() != nil ||
		dm.GetPollCreate() != nil || dm.GetIsViewOnce()
}

// classifyDataMessage decides what dm does. The order mirrors Signal
// Desktop: timer updates and content first, then the action types.
func classifyDataMessage(dm *signalpb.DataMessage) dmKind {
	switch {
	case dm == nil:
		return dmEmpty
	case dm.GetFlags()&uint32(signalpb.DataMessage_EXPIRATION_TIMER_UPDATE) != 0:
		return dmTimerUpdate
	case dm.GetReaction() != nil:
		return dmReaction
	case hasContent(dm):
		return dmContent
	case dm.GetDelete() != nil:
		return dmDelete
	case dm.GetAdminDelete() != nil:
		return dmAdminDelete
	case dm.GetPollVote() != nil:
		return dmPollVote
	case dm.GetPollTerminate() != nil:
		return dmPollTerminate
	case dm.GetPinMessage() != nil:
		return dmPin
	case dm.GetUnpinMessage() != nil:
		return dmUnpin
	case len(dm.GetGroupV2().GetGroupChange()) > 0:
		return dmGroupUpdate
	case dm.GetRequiredProtocolVersion() > uint32(signalpb.DataMessage_CURRENT):
		return dmUnsupported
	case dm.GetGroupV2() != nil && dm.GetFlags()&uint32(signalpb.DataMessage_PROFILE_KEY_UPDATE) == 0:
		return dmGroupContext
	}
	return dmEmpty
}

// msTime converts a Signal millisecond timestamp to a UTC time (zero for 0).
func msTime(ms uint64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(int64(ms)).UTC()
}

// attachmentID derives a stable, opaque attachment ID from the owning
// message, the pointer's role and position, and its CDN location. The same
// message always yields the same IDs, so re-processing is idempotent.
func attachmentID(owner core.MessageID, role attRole, idx int, p *signalpb.AttachmentPointer) string {
	loc := p.GetCdnKey()
	if loc == "" && p.GetCdnId() != 0 {
		loc = strconv.FormatUint(p.GetCdnId(), 10)
	}
	if loc == "" && len(p.GetClientUuid()) > 0 {
		loc = hex.EncodeToString(p.GetClientUuid())
	}
	return hashID("a", owner.String(), string(role), strconv.Itoa(idx), loc, hex.EncodeToString(p.GetDigest()))
}

// attachmentFromPointer converts a pointer's metadata (not its bytes).
func attachmentFromPointer(p *signalpb.AttachmentPointer) core.Attachment {
	flags := p.GetFlags()
	return core.Attachment{
		ContentType: p.GetContentType(),
		FileName:    p.GetFileName(),
		Size:        int64(p.GetSize()),
		Width:       int(p.GetWidth()),
		Height:      int(p.GetHeight()),
		Caption:     p.GetCaption(),
		BlurHash:    p.GetBlurHash(),
		VoiceNote:   flags&uint32(signalpb.AttachmentPointer_VOICE_MESSAGE) != 0,
		Borderless:  flags&uint32(signalpb.AttachmentPointer_BORDERLESS) != 0,
		GIF:         flags&uint32(signalpb.AttachmentPointer_GIF) != 0,
	}
}

// hasLocation reports whether the pointer can be downloaded at all.
func hasLocation(p *signalpb.AttachmentPointer) bool {
	return p != nil && (p.GetCdnKey() != "" || p.GetCdnId() != 0) && len(p.GetKey()) > 0
}

// convertDataMessage converts a content-bearing DataMessage into a message
// plus the attachment pointers it references. It does not touch the network
// or the store.
func convertDataMessage(cc convCtx, dm *signalpb.DataMessage) converted {
	id := core.MessageID{Chat: cc.Chat, Author: cc.Sender, Timestamp: dm.GetTimestamp()}
	m := core.Message{
		ID:              id,
		Kind:            core.KindNormal,
		Outgoing:        cc.Sender != "" && cc.Sender == cc.Self,
		SentAt:          msTime(dm.GetTimestamp()),
		ReceivedAt:      cc.ReceivedAt,
		ServerTimestamp: cc.ServerTS,
		ViewOnce:        dm.GetIsViewOnce(),
	}
	if m.Outgoing {
		m.Status = core.StatusSent
	}
	if t := dm.GetExpireTimer(); t > 0 {
		m.ExpiresIn = time.Duration(t) * time.Second
	}
	var out converted
	m.Body = dm.GetBody()
	m.Ranges = rangesFromSignal(m.Body, dm.GetBodyRanges())
	m.MentionsSelf = !m.Outgoing && mentionsACI(m.Ranges, cc.Self)

	for i, p := range dm.GetAttachments() {
		if p == nil {
			continue
		}
		if p.GetContentType() == longTextContentType && out.LongText == nil {
			out.LongText = &attachmentRef{ID: attachmentID(id, roleLongText, i, p), Role: roleLongText, Pointer: p}
			continue
		}
		a := attachmentFromPointer(p)
		a.ID = attachmentID(id, roleBody, i, p)
		a.Failed = !hasLocation(p)
		m.Attachments = append(m.Attachments, a)
		out.Atts = append(out.Atts, attachmentRef{ID: a.ID, Role: roleBody, Pointer: p})
	}
	if out.LongText != nil {
		out.Atts = append(out.Atts, *out.LongText)
	}

	if q := dm.GetQuote(); q != nil {
		quote, ref := quoteFromSignal(cc.Chat, id, q)
		m.Quote = quote
		if ref != nil {
			out.Atts = append(out.Atts, *ref)
		}
	}

	if s := dm.GetSticker(); s != nil {
		st := &core.Sticker{
			PackID:    hex.EncodeToString(s.GetPackId()),
			PackKey:   hex.EncodeToString(s.GetPackKey()),
			StickerID: s.GetStickerId(),
			Emoji:     s.GetEmoji(),
		}
		if p := s.GetData(); p != nil {
			st.Attachment = attachmentFromPointer(p)
			st.Attachment.ID = attachmentID(id, roleSticker, 0, p)
			st.Attachment.Borderless = true
			st.Attachment.Failed = !hasLocation(p)
			out.Atts = append(out.Atts, attachmentRef{ID: st.Attachment.ID, Role: roleSticker, Pointer: p})
		}
		m.Sticker = st
	}

	for i, p := range dm.GetPreview() {
		if p.GetUrl() == "" {
			continue
		}
		lp := &core.LinkPreview{
			URL:         p.GetUrl(),
			Title:       p.GetTitle(),
			Description: p.GetDescription(),
			Date:        msTime(p.GetDate()),
		}
		if img := p.GetImage(); img != nil {
			a := attachmentFromPointer(img)
			a.ID = attachmentID(id, rolePreview, i, img)
			a.Failed = !hasLocation(img)
			lp.Image = &a
			out.Atts = append(out.Atts, attachmentRef{ID: a.ID, Role: rolePreview, Pointer: img})
		}
		m.Preview = lp
		break
	}

	if cs := dm.GetContact(); len(cs) > 0 && cs[0] != nil {
		m.Contact = sharedContactFromSignal(cs[0])
	}

	if pc := dm.GetPollCreate(); pc != nil {
		p := &core.Poll{Question: pc.GetQuestion(), AllowMultiple: pc.GetAllowMultiple()}
		for _, o := range pc.GetOptions() {
			p.Options = append(p.Options, core.PollOption{Text: o})
		}
		m.Poll = p
	}

	if pay := dm.GetPayment(); pay != nil {
		m.PaymentNotice = paymentNotice(pay, m.Outgoing)
	}
	m.GiftBadge = dm.GetGiftBadge() != nil

	if sc := dm.GetStoryContext(); sc != nil {
		if author := aciFromBinaryOrString(sc.GetAuthorAciBinary(), sc.GetAuthorAci()); author != "" {
			m.Kind = core.KindStoryReply
			m.StoryContext = &core.StoryRef{Author: author, Timestamp: sc.GetSentTimestamp()}
		}
	}
	out.Msg = m
	return out
}

// quoteFromSignal converts a quote. The returned ref is the quoted
// attachment's thumbnail pointer, registered under the quoting message.
func quoteFromSignal(chat core.ChatID, owner core.MessageID, q *signalpb.DataMessage_Quote) (*core.Quote, *attachmentRef) {
	author := aciFromBinaryOrString(q.GetAuthorAciBinary(), q.GetAuthorAci())
	out := &core.Quote{
		ID:     core.MessageID{Chat: chat, Author: author, Timestamp: q.GetId()},
		Text:   q.GetText(),
		Ranges: rangesFromSignal(q.GetText(), q.GetBodyRanges()),
	}
	var ref *attachmentRef
	if qa := q.GetAttachments(); len(qa) > 0 && qa[0] != nil {
		a := core.Attachment{ContentType: qa[0].GetContentType(), FileName: qa[0].GetFileName()}
		if th := qa[0].GetThumbnail(); th != nil {
			a.ID = attachmentID(owner, roleQuote, 0, th)
			a.Width, a.Height = int(th.GetWidth()), int(th.GetHeight())
			a.Size = int64(th.GetSize())
			if th.GetContentType() != "" {
				a.ContentType = th.GetContentType()
			}
			a.Failed = !hasLocation(th)
			ref = &attachmentRef{ID: a.ID, Role: roleQuote, Pointer: th}
		}
		out.Attachment = &a
	}
	switch q.GetType() {
	case signalpb.DataMessage_Quote_GIFT_BADGE:
		if out.Text == "" {
			out.Text = "Gift badge"
		}
	case signalpb.DataMessage_Quote_POLL:
		if out.Text != "" {
			out.Text = "📊 " + out.Text
		}
	}
	return out, ref
}

// sharedContactFromSignal converts a shared contact card.
func sharedContactFromSignal(c *signalpb.DataMessage_Contact) *core.SharedContact {
	n := c.GetName()
	name := n.GetNickname()
	if name == "" {
		parts := slices.DeleteFunc([]string{n.GetPrefix(), n.GetGivenName(), n.GetMiddleName(), n.GetFamilyName(), n.GetSuffix()},
			func(s string) bool { return s == "" })
		name = strings.Join(parts, " ")
	}
	if name == "" {
		name = c.GetOrganization()
	}
	sc := &core.SharedContact{Name: name, Org: c.GetOrganization()}
	for _, p := range c.GetNumber() {
		if v := strings.TrimSpace(p.GetValue()); v != "" {
			sc.Phones = append(sc.Phones, v)
		}
	}
	for _, e := range c.GetEmail() {
		if v := strings.TrimSpace(e.GetValue()); v != "" {
			sc.Emails = append(sc.Emails, v)
		}
	}
	return sc
}

// paymentNotice renders a read-only payment summary. warte never handles
// MobileCoin; the user is pointed to their phone.
func paymentNotice(p *signalpb.DataMessage_Payment, outgoing bool) string {
	switch {
	case p.GetNotification() != nil:
		s := "Payment received"
		if outgoing {
			s = "Payment sent"
		}
		if note := strings.TrimSpace(p.GetNotification().GetNote()); note != "" {
			s += ": " + note
		}
		return s + " (view it on your phone)"
	case p.GetActivation() != nil:
		if p.GetActivation().GetType() == signalpb.DataMessage_Payment_Activation_ACTIVATED {
			return "Activated payments"
		}
		return "Requested to activate payments"
	}
	return "Payment (view it on your phone)"
}

// reactionTarget extracts the target of a reaction.
type reactionTarget struct {
	Author    core.ACI
	Timestamp uint64
	Emoji     string
	Remove    bool
}

func reactionFromSignal(r *signalpb.DataMessage_Reaction) (reactionTarget, error) {
	t := reactionTarget{
		Author:    aciFromBinaryOrString(r.GetTargetAuthorAciBinary(), r.GetTargetAuthorAci()),
		Timestamp: r.GetTargetSentTimestamp(),
		Emoji:     r.GetEmoji(),
		Remove:    r.GetRemove(),
	}
	if t.Author == "" || t.Timestamp == 0 {
		return t, fmt.Errorf("%w: reaction target", errBadID)
	}
	if !t.Remove && t.Emoji == "" {
		return t, fmt.Errorf("%w: empty reaction", errBadID)
	}
	return t, nil
}

// applyEdit records the current text of m as a revision and replaces it with
// the edited content. prevAt is when the current revision was written (the
// original send time for the first edit).
func applyEdit(m *core.Message, edited core.Message, prevAt time.Time) {
	m.EditHistory = append(m.EditHistory, core.EditRevision{Body: m.Body, Ranges: m.Ranges, At: prevAt})
	m.Body = edited.Body
	m.Ranges = edited.Ranges
	m.MentionsSelf = edited.MentionsSelf
	m.Edited = true
	if edited.Preview != nil || m.Preview != nil {
		m.Preview = edited.Preview
	}
	if edited.Quote != nil {
		m.Quote = edited.Quote
	}
}

// remoteDelete blanks a message's content as "delete for everyone" does,
// keeping identity, timing and delivery state.
func remoteDelete(m *core.Message) {
	*m = core.Message{
		ID: m.ID, Kind: m.Kind, SenderName: m.SenderName, Outgoing: m.Outgoing,
		SentAt: m.SentAt, ReceivedAt: m.ReceivedAt,
		Status: m.Status, PerRecipient: m.PerRecipient,
		ExpiresIn: m.ExpiresIn, ExpireStartedAt: m.ExpireStartedAt,
		StoryContext: m.StoryContext, ServerTimestamp: m.ServerTimestamp,
		RemoteDeleted: true,
	}
}

// applyPollVote replaces voter's choices on p with the given option indexes.
// Out-of-range indexes are ignored; a single-choice poll keeps only the
// first valid one.
func applyPollVote(p *core.Poll, voter core.ACI, indexes []uint32) {
	for i := range p.Options {
		p.Options[i].Voters = slices.DeleteFunc(p.Options[i].Voters, func(a core.ACI) bool { return a == voter })
		if len(p.Options[i].Voters) == 0 {
			p.Options[i].Voters = nil
		}
	}
	seen := map[uint32]bool{}
	for _, idx := range indexes {
		if int(idx) >= len(p.Options) || seen[idx] {
			continue
		}
		if !p.AllowMultiple && len(seen) > 0 {
			break
		}
		seen[idx] = true
		p.Options[idx].Voters = append(p.Options[idx].Voters, voter)
	}
}

// pollVotesOf returns the option indexes voter currently has on p.
func pollVotesOf(p *core.Poll, voter core.ACI) []uint32 {
	var out []uint32
	for i, o := range p.Options {
		if slices.Contains(o.Voters, voter) {
			out = append(out, uint32(i))
		}
	}
	return out
}

// pinDuration converts a PinMessage duration; zero means forever.
func pinDuration(p *signalpb.DataMessage_PinMessage) time.Duration {
	if s := p.GetPinDurationSeconds(); s > 0 {
		return time.Duration(s) * time.Second
	}
	return 0
}
