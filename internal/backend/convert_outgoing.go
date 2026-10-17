package backend

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"

	"warte/internal/core"
)

// Signal limits that the official clients enforce on send.
const (
	inlineBodyLimit   = 2000 // bytes of body sent inline; the rest goes as a long-text attachment
	maxPollOptions    = 10
	maxPollTextRunes  = 100
	maxAttachmentSize = 100 << 20
)

// errEmptyMessage is returned for an Outgoing with nothing to send.
var errEmptyMessage = errors.New("backend: empty message")

// validateOutgoing enforces core.Outgoing's contract: exactly one of
// body/attachments, a sticker or a poll; view-once needs an attachment.
func validateOutgoing(out core.Outgoing) error {
	kinds := 0
	if strings.TrimSpace(out.Body) != "" || len(out.Attachments) > 0 {
		kinds++
	}
	if out.Sticker != nil {
		kinds++
	}
	if out.Poll != nil {
		kinds++
		if strings.TrimSpace(out.Poll.Question) == "" || len(out.Poll.Options) < 2 {
			return errors.New("backend: a poll needs a question and at least two options")
		}
		if len(out.Poll.Options) > maxPollOptions {
			return fmt.Errorf("backend: a poll can have at most %d options", maxPollOptions)
		}
		if len([]rune(out.Poll.Question)) > maxPollTextRunes {
			return fmt.Errorf("backend: poll question is longer than %d characters", maxPollTextRunes)
		}
		for _, o := range out.Poll.Options {
			if strings.TrimSpace(o.Text) == "" || len([]rune(o.Text)) > maxPollTextRunes {
				return fmt.Errorf("backend: poll options must be 1-%d characters", maxPollTextRunes)
			}
		}
	}
	switch {
	case kinds == 0:
		return errEmptyMessage
	case kinds > 1:
		return errors.New("backend: a message carries exactly one of text/attachments, a sticker or a poll")
	case out.ViewOnce && len(out.Attachments) != 1:
		return errors.New("backend: view-once needs exactly one attachment")
	case out.ViewOnce && strings.TrimSpace(out.Body) != "":
		return errors.New("backend: view-once media can't have a caption")
	}
	for _, a := range out.Attachments {
		if len(a.Data) == 0 {
			return fmt.Errorf("backend: attachment %q is empty", a.FileName)
		}
		if len(a.Data) > maxAttachmentSize {
			return fmt.Errorf("backend: attachment %q is larger than %d MB", a.FileName, maxAttachmentSize>>20)
		}
	}
	return nil
}

// splitLongBody splits a body that is too long to send inline. It returns
// the inline prefix (cut on a rune boundary) and whether the full text must
// be attached as text/x-signal-plain.
func splitLongBody(body string) (inline string, needsAttachment bool) {
	if len(body) <= inlineBodyLimit {
		return body, false
	}
	cut := inlineBodyLimit
	for cut > 0 && !utf8RuneStart(body[cut]) {
		cut--
	}
	return body[:cut], true
}

func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }

// trimRanges drops ranges that do not fit in body (after truncation).
func trimRanges(body string, ranges []core.BodyRange) []core.BodyRange {
	limit := utf16Len(body)
	var out []core.BodyRange
	for _, r := range ranges {
		if r.Start >= limit || r.Length <= 0 {
			continue
		}
		if r.Start+r.Length > limit {
			if r.Style == core.StyleMention {
				continue
			}
			r.Length = limit - r.Start
		}
		out = append(out, r)
	}
	return out
}

// sendSpec is everything needed to build an outgoing DataMessage after
// uploads completed.
type sendSpec struct {
	Timestamp    uint64
	Body         string
	Ranges       []core.BodyRange
	Attachments  []*signalpb.AttachmentPointer // uploaded pointers, in order
	Quote        *core.Message                 // quoted message, if any
	QuoteThumb   *signalpb.AttachmentPointer   // uploaded thumbnail of the quoted attachment
	Sticker      *core.Sticker
	StickerData  *signalpb.AttachmentPointer
	Preview      *core.LinkPreview
	PreviewImage *signalpb.AttachmentPointer
	Poll         *core.Poll
	ViewOnce     bool
	ExpireTimer  time.Duration
	TimerVersion uint32
	ProfileKey   []byte
	StoryContext *core.StoryRef
}

// buildDataMessage assembles the DataMessage for an outgoing message.
func buildDataMessage(s sendSpec) *signalpb.DataMessage {
	dm := &signalpb.DataMessage{Timestamp: proto.Uint64(s.Timestamp)}
	if len(s.ProfileKey) > 0 {
		dm.ProfileKey = s.ProfileKey
	}
	if s.ExpireTimer > 0 {
		dm.ExpireTimer = proto.Uint32(uint32(s.ExpireTimer / time.Second))
		if s.TimerVersion > 0 {
			dm.ExpireTimerVersion = proto.Uint32(s.TimerVersion)
		}
	}
	if s.Body != "" {
		dm.Body = proto.String(s.Body)
		dm.BodyRanges = rangesToSignal(s.Body, s.Ranges)
	}
	dm.Attachments = s.Attachments
	if q := s.Quote; q != nil {
		dm.Quote = quoteToSignal(q, s.QuoteThumb)
	}
	if st := s.Sticker; st != nil {
		packID, _ := hex.DecodeString(st.PackID)
		packKey, _ := hex.DecodeString(st.PackKey)
		dm.Sticker = &signalpb.DataMessage_Sticker{
			PackId:    packID,
			PackKey:   packKey,
			StickerId: proto.Uint32(st.StickerID),
			Data:      s.StickerData,
		}
		if st.Emoji != "" {
			dm.Sticker.Emoji = proto.String(st.Emoji)
		}
	}
	if p := s.Preview; p != nil && p.URL != "" && s.Body != "" && strings.Contains(s.Body, p.URL) {
		pv := &signalpb.Preview{Url: proto.String(p.URL), Image: s.PreviewImage}
		if p.Title != "" {
			pv.Title = proto.String(p.Title)
		}
		if p.Description != "" {
			pv.Description = proto.String(p.Description)
		}
		if !p.Date.IsZero() {
			pv.Date = proto.Uint64(uint64(p.Date.UnixMilli()))
		}
		dm.Preview = []*signalpb.Preview{pv}
	}
	if p := s.Poll; p != nil {
		pc := &signalpb.DataMessage_PollCreate{Question: proto.String(p.Question), AllowMultiple: proto.Bool(p.AllowMultiple)}
		for _, o := range p.Options {
			pc.Options = append(pc.Options, o.Text)
		}
		dm.PollCreate = pc
		dm.RequiredProtocolVersion = proto.Uint32(uint32(signalpb.DataMessage_POLLS))
	}
	if s.ViewOnce {
		dm.IsViewOnce = proto.Bool(true)
		dm.RequiredProtocolVersion = proto.Uint32(max(dm.GetRequiredProtocolVersion(), uint32(signalpb.DataMessage_VIEW_ONCE_VIDEO)))
		dm.Body, dm.BodyRanges, dm.Preview = nil, nil, nil
	}
	if sc := s.StoryContext; sc != nil {
		if u, err := uuid.Parse(string(sc.Author)); err == nil {
			dm.StoryContext = &signalpb.DataMessage_StoryContext{
				AuthorAci:     proto.String(u.String()),
				SentTimestamp: proto.Uint64(sc.Timestamp),
			}
		}
	}
	if hasMention(s.Ranges) && dm.GetRequiredProtocolVersion() < uint32(signalpb.DataMessage_MENTIONS) {
		dm.RequiredProtocolVersion = proto.Uint32(uint32(signalpb.DataMessage_MENTIONS))
	}
	return dm
}

func hasMention(rs []core.BodyRange) bool {
	for _, r := range rs {
		if r.Style == core.StyleMention {
			return true
		}
	}
	return false
}

// quoteToSignal builds the quote of q. thumb is an optional uploaded
// thumbnail for the quoted attachment.
func quoteToSignal(q *core.Message, thumb *signalpb.AttachmentPointer) *signalpb.DataMessage_Quote {
	out := &signalpb.DataMessage_Quote{
		Id:   proto.Uint64(q.ID.Timestamp),
		Type: signalpb.DataMessage_Quote_NORMAL.Enum(),
	}
	if u, err := uuid.Parse(string(q.ID.Author)); err == nil {
		out.AuthorAci = proto.String(u.String())
		out.AuthorAciBinary = u[:]
	}
	text := q.Body
	if q.Poll != nil {
		text = q.Poll.Question
		out.Type = signalpb.DataMessage_Quote_POLL.Enum()
	}
	if q.GiftBadge {
		out.Type = signalpb.DataMessage_Quote_GIFT_BADGE.Enum()
	}
	if text != "" {
		out.Text = proto.String(text)
		if q.Poll == nil {
			out.BodyRanges = rangesToSignal(text, q.Ranges)
		}
	}
	var att *core.Attachment
	switch {
	case len(q.Attachments) > 0:
		att = &q.Attachments[0]
	case q.Sticker != nil:
		att = &q.Sticker.Attachment
	}
	if att != nil {
		qa := &signalpb.DataMessage_Quote_QuotedAttachment{Thumbnail: thumb}
		if att.ContentType != "" {
			qa.ContentType = proto.String(att.ContentType)
		}
		if att.FileName != "" {
			qa.FileName = proto.String(att.FileName)
		}
		out.Attachments = []*signalpb.DataMessage_Quote_QuotedAttachment{qa}
	}
	return out
}

// pointerFlags computes attachment pointer flags for an outgoing attachment.
func pointerFlags(a core.OutgoingAttachment, borderless, gif bool) uint32 {
	var f uint32
	if a.VoiceNote {
		f |= uint32(signalpb.AttachmentPointer_VOICE_MESSAGE)
	}
	if borderless {
		f |= uint32(signalpb.AttachmentPointer_BORDERLESS)
	}
	if gif {
		f |= uint32(signalpb.AttachmentPointer_GIF)
	}
	return f
}

// decoratePointer fills metadata on a freshly uploaded pointer.
func decoratePointer(p *signalpb.AttachmentPointer, contentType, fileName string, width, height int, caption, blurHash string, flags uint32) {
	if contentType != "" {
		p.ContentType = proto.String(contentType)
	}
	if fileName != "" {
		p.FileName = proto.String(fileName)
	}
	if width > 0 && height > 0 {
		p.Width, p.Height = proto.Uint32(uint32(width)), proto.Uint32(uint32(height))
	}
	if caption != "" {
		p.Caption = proto.String(caption)
	}
	if blurHash != "" {
		p.BlurHash = proto.String(blurHash)
	}
	if flags != 0 {
		p.Flags = proto.Uint32(flags)
	}
	p.UploadTimestamp = proto.Uint64(uint64(time.Now().UnixMilli()))
	if len(p.ClientUuid) == 0 {
		u := uuid.New()
		p.ClientUuid = u[:]
	}
}

// buildEditMessage wraps the new content of an edit.
func buildEditMessage(targetTS uint64, dm *signalpb.DataMessage) *signalpb.EditMessage {
	return &signalpb.EditMessage{TargetSentTimestamp: proto.Uint64(targetTS), DataMessage: dm}
}

// buildReaction builds a reaction DataMessage.
func buildReaction(ts uint64, target core.MessageID, emoji string, remove bool) (*signalpb.DataMessage, error) {
	u, err := parseACI(target.Author)
	if err != nil {
		return nil, err
	}
	return &signalpb.DataMessage{
		Timestamp:               proto.Uint64(ts),
		RequiredProtocolVersion: proto.Uint32(uint32(signalpb.DataMessage_REACTIONS)),
		Reaction: &signalpb.DataMessage_Reaction{
			Emoji:                 proto.String(emoji),
			Remove:                proto.Bool(remove),
			TargetAuthorAci:       proto.String(u.String()),
			TargetAuthorAciBinary: u[:],
			TargetSentTimestamp:   proto.Uint64(target.Timestamp),
		},
	}, nil
}

// buildDelete builds a delete-for-everyone DataMessage.
func buildDelete(ts, targetTS uint64) *signalpb.DataMessage {
	return &signalpb.DataMessage{
		Timestamp: proto.Uint64(ts),
		Delete:    &signalpb.DataMessage_Delete{TargetSentTimestamp: proto.Uint64(targetTS)},
	}
}

// buildTimerUpdate builds a disappearing-timer update for a direct chat.
func buildTimerUpdate(ts uint64, d time.Duration, version uint32, profileKey []byte) *signalpb.DataMessage {
	dm := &signalpb.DataMessage{
		Timestamp:   proto.Uint64(ts),
		Flags:       proto.Uint32(uint32(signalpb.DataMessage_EXPIRATION_TIMER_UPDATE)),
		ExpireTimer: proto.Uint32(uint32(d / time.Second)),
	}
	if version > 0 {
		dm.ExpireTimerVersion = proto.Uint32(version)
	}
	if len(profileKey) > 0 {
		dm.ProfileKey = profileKey
	}
	return dm
}

// buildProfileKeyUpdate shares our profile key (sent when accepting a
// message request so the other side can see our name and avatar).
func buildProfileKeyUpdate(ts uint64, profileKey []byte) *signalpb.DataMessage {
	return &signalpb.DataMessage{
		Timestamp:  proto.Uint64(ts),
		Flags:      proto.Uint32(uint32(signalpb.DataMessage_PROFILE_KEY_UPDATE)),
		ProfileKey: profileKey,
	}
}

// buildPollVote builds a poll vote.
func buildPollVote(ts uint64, poll core.MessageID, options []int, count uint32) (*signalpb.DataMessage, error) {
	u, err := parseACI(poll.Author)
	if err != nil {
		return nil, err
	}
	idx := make([]uint32, 0, len(options))
	for _, o := range options {
		idx = append(idx, uint32(o))
	}
	return &signalpb.DataMessage{
		Timestamp:               proto.Uint64(ts),
		RequiredProtocolVersion: proto.Uint32(uint32(signalpb.DataMessage_POLLS)),
		PollVote: &signalpb.DataMessage_PollVote{
			TargetAuthorAciBinary: u[:],
			TargetSentTimestamp:   proto.Uint64(poll.Timestamp),
			OptionIndexes:         idx,
			VoteCount:             proto.Uint32(count),
		},
	}, nil
}

// buildPollTerminate ends one of our polls.
func buildPollTerminate(ts, pollTS uint64) *signalpb.DataMessage {
	return &signalpb.DataMessage{
		Timestamp:               proto.Uint64(ts),
		RequiredProtocolVersion: proto.Uint32(uint32(signalpb.DataMessage_POLLS)),
		PollTerminate:           &signalpb.DataMessage_PollTerminate{TargetSentTimestamp: proto.Uint64(pollTS)},
	}
}

// buildPin builds a pin (pin=true) or unpin message. duration 0 pins forever.
func buildPin(ts uint64, target core.MessageID, pin bool, duration time.Duration) (*signalpb.DataMessage, error) {
	u, err := parseACI(target.Author)
	if err != nil {
		return nil, err
	}
	dm := &signalpb.DataMessage{Timestamp: proto.Uint64(ts)}
	if !pin {
		dm.UnpinMessage = &signalpb.DataMessage_UnpinMessage{TargetAuthorAciBinary: u[:], TargetSentTimestamp: proto.Uint64(target.Timestamp)}
		return dm, nil
	}
	pm := &signalpb.DataMessage_PinMessage{TargetAuthorAciBinary: u[:], TargetSentTimestamp: proto.Uint64(target.Timestamp)}
	if duration > 0 {
		pm.PinDuration = &signalpb.DataMessage_PinMessage_PinDurationSeconds{PinDurationSeconds: uint32(duration / time.Second)}
	} else {
		pm.PinDuration = &signalpb.DataMessage_PinMessage_PinDurationForever{PinDurationForever: true}
	}
	dm.PinMessage = pm
	return dm, nil
}

// conversationID builds the sync-message conversation identifier of a chat.
func conversationID(c core.ChatID) (*signalpb.ConversationIdentifier, error) {
	switch {
	case c.IsGroup():
		raw, err := groupIDBytes(c)
		if err != nil {
			return nil, err
		}
		return &signalpb.ConversationIdentifier{Identifier: &signalpb.ConversationIdentifier_ThreadGroupId{ThreadGroupId: raw}}, nil
	case c.IsDirect():
		u, err := parseACI(c.ACI())
		if err != nil {
			return nil, err
		}
		return &signalpb.ConversationIdentifier{Identifier: &signalpb.ConversationIdentifier_ThreadServiceId{ThreadServiceId: u.String()}}, nil
	}
	return nil, fmt.Errorf("%w: chat %q", errBadID, c)
}

// addressable builds an AddressableMessage for id.
func addressable(id core.MessageID) (*signalpb.AddressableMessage, error) {
	u, err := parseACI(id.Author)
	if err != nil {
		return nil, err
	}
	return &signalpb.AddressableMessage{
		Author:        &signalpb.AddressableMessage_AuthorServiceId{AuthorServiceId: u.String()},
		SentTimestamp: proto.Uint64(id.Timestamp),
	}, nil
}

// syncDeleteMessages builds a DeleteForMe sync for messages of one chat.
func syncDeleteMessages(chat core.ChatID, ids []core.MessageID) (*signalpb.SyncMessage, error) {
	conv, err := conversationID(chat)
	if err != nil {
		return nil, err
	}
	md := &signalpb.SyncMessage_DeleteForMe_MessageDeletes{Conversation: conv}
	for _, id := range ids {
		am, err := addressable(id)
		if err != nil {
			return nil, err
		}
		md.Messages = append(md.Messages, am)
	}
	return &signalpb.SyncMessage{Content: &signalpb.SyncMessage_DeleteForMe_{DeleteForMe: &signalpb.SyncMessage_DeleteForMe{
		MessageDeletes: []*signalpb.SyncMessage_DeleteForMe_MessageDeletes{md},
	}}}, nil
}

// syncDeleteConversation builds a DeleteForMe sync deleting a whole chat.
// recent are the newest messages, so other devices can tell what was seen.
func syncDeleteConversation(chat core.ChatID, recent []core.MessageID) (*signalpb.SyncMessage, error) {
	conv, err := conversationID(chat)
	if err != nil {
		return nil, err
	}
	cd := &signalpb.SyncMessage_DeleteForMe_ConversationDelete{Conversation: conv, IsFullDelete: proto.Bool(true)}
	for _, id := range recent {
		if am, err := addressable(id); err == nil {
			cd.MostRecentMessages = append(cd.MostRecentMessages, am)
		}
	}
	dfm := &signalpb.SyncMessage_DeleteForMe{}
	if len(cd.MostRecentMessages) == 0 {
		dfm.LocalOnlyConversationDeletes = []*signalpb.SyncMessage_DeleteForMe_LocalOnlyConversationDelete{{Conversation: conv}}
	} else {
		dfm.ConversationDeletes = []*signalpb.SyncMessage_DeleteForMe_ConversationDelete{cd}
	}
	return &signalpb.SyncMessage{Content: &signalpb.SyncMessage_DeleteForMe_{DeleteForMe: dfm}}, nil
}

// syncRead builds a read sync for messages read on this device.
func syncRead(ids []core.MessageID) *signalpb.SyncMessage {
	sm := &signalpb.SyncMessage{}
	for _, id := range ids {
		u, err := parseACI(id.Author)
		if err != nil {
			continue
		}
		sm.Read = append(sm.Read, &signalpb.SyncMessage_Read{
			SenderAci:       proto.String(u.String()),
			SenderAciBinary: u[:],
			Timestamp:       proto.Uint64(id.Timestamp),
		})
	}
	return sm
}

// syncViewOnceOpen builds the sync for opening a view-once message.
func syncViewOnceOpen(id core.MessageID) (*signalpb.SyncMessage, error) {
	u, err := parseACI(id.Author)
	if err != nil {
		return nil, err
	}
	return &signalpb.SyncMessage{Content: &signalpb.SyncMessage_ViewOnceOpen_{ViewOnceOpen: &signalpb.SyncMessage_ViewOnceOpen{
		SenderAci: proto.String(u.String()), SenderAciBinary: u[:], Timestamp: proto.Uint64(id.Timestamp),
	}}}, nil
}

// syncMessageRequestResponse builds a message-request response sync.
func syncMessageRequestResponse(chat core.ChatID, typ signalpb.SyncMessage_MessageRequestResponse_Type) (*signalpb.SyncMessage, error) {
	r := &signalpb.SyncMessage_MessageRequestResponse{Type: typ.Enum()}
	switch {
	case chat.IsGroup():
		raw, err := groupIDBytes(chat)
		if err != nil {
			return nil, err
		}
		r.GroupId = raw
	default:
		u, err := parseACI(chat.ACI())
		if err != nil {
			return nil, err
		}
		r.ThreadAci, r.ThreadAciBinary = proto.String(u.String()), u[:]
	}
	return &signalpb.SyncMessage{Content: &signalpb.SyncMessage_MessageRequestResponse_{MessageRequestResponse: r}}, nil
}

// syncBlocked builds the full blocked-list sync.
func syncBlocked(acis []core.ACI, groups []core.ChatID) *signalpb.SyncMessage {
	b := &signalpb.SyncMessage_Blocked{}
	for _, a := range acis {
		if u, err := parseACI(a); err == nil {
			b.Acis = append(b.Acis, u.String())
			b.BlockedAcis = append(b.BlockedAcis, &signalpb.SyncMessage_Blocked_BlockedAci{AciBinary: u[:]})
		}
	}
	for _, g := range groups {
		if raw, err := groupIDBytes(g); err == nil {
			b.GroupIds = append(b.GroupIds, raw)
			b.BlockedGroups = append(b.BlockedGroups, &signalpb.SyncMessage_Blocked_BlockedGroup{GroupId: raw})
		}
	}
	return &signalpb.SyncMessage{Content: &signalpb.SyncMessage_Blocked_{Blocked: b}}
}

// syncStickerPack builds a sticker pack install/remove sync.
func syncStickerPack(packID, packKey []byte, install bool) *signalpb.SyncMessage {
	typ := signalpb.SyncMessage_StickerPackOperation_INSTALL
	if !install {
		typ = signalpb.SyncMessage_StickerPackOperation_REMOVE
	}
	return &signalpb.SyncMessage{StickerPackOperation: []*signalpb.SyncMessage_StickerPackOperation{{
		PackId: packID, PackKey: packKey, Type: typ.Enum(),
	}}}
}

// syncVerified builds a verification state sync for aci with identityKey.
func syncVerified(aci core.ACI, identityKey []byte, verified bool) (*signalpb.SyncMessage, error) {
	u, err := parseACI(aci)
	if err != nil {
		return nil, err
	}
	state := signalpb.Verified_DEFAULT
	if verified {
		state = signalpb.Verified_VERIFIED
	}
	return &signalpb.SyncMessage{Content: &signalpb.SyncMessage_Verified{Verified: &signalpb.Verified{
		DestinationAci: proto.String(u.String()), DestinationAciBinary: u[:],
		IdentityKey: identityKey, State: state.Enum(),
	}}}, nil
}
