package backend

import (
	"sort"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/backuppb"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"

	"warte/internal/core"
)

// backupCtx is what converting a history-transfer chat item needs.
type backupCtx struct {
	Self core.ACI
	Chat core.ChatID
	// Peer is the other participant of a direct chat ("" for groups).
	Peer core.ACI
	// ACIOf resolves a backup recipient ID to an ACI ("" if unknown).
	ACIOf func(recipientID uint64) core.ACI
	// Name resolves display names for system text (nil = "Unknown").
	Name nameFunc
}

func (bc backupCtx) name(a core.ACI) string {
	if bc.Name == nil {
		return "Unknown"
	}
	return bc.Name(a)
}

// backupItem is a converted history item.
type backupItem struct {
	converted
	Read bool // incoming item already read on the phone
}

// convertBackupItem converts one chat item of a history transfer. ok is
// false for items warte does not import (unknown types, most chat updates).
func convertBackupItem(bc backupCtx, ci *backuppb.ChatItem) (backupItem, bool) {
	if ci == nil || ci.GetDateSent() == 0 {
		return backupItem{}, false
	}
	author := bc.ACIOf(ci.GetAuthorId())
	outgoing := ci.GetOutgoing() != nil
	if outgoing {
		author = bc.Self
	}
	if author == "" {
		return backupItem{}, false
	}
	hashes := map[*signalpb.AttachmentPointer][]byte{}
	ptr := func(fp *backuppb.FilePointer, flag backuppb.MessageAttachment_Flag, clientUUID []byte) *signalpb.AttachmentPointer {
		p, hash := backupPointer(fp, flag, clientUUID)
		if p != nil && hash != nil {
			hashes[p] = hash
		}
		return p
	}
	dm := &signalpb.DataMessage{Timestamp: proto.Uint64(ci.GetDateSent())}
	var reactions []*backuppb.Reaction
	var (
		system     string
		kind       = core.KindNormal
		call       *core.CallInfo
		deleted    bool
		pollEnded  bool
		pollVotes  map[int][]core.ACI
		storyReply bool
	)
	switch it := ci.GetItem().(type) {
	case *backuppb.ChatItem_StandardMessage:
		sm := it.StandardMessage
		reactions = sm.GetReactions()
		if t := sm.GetText(); t != nil {
			dm.Body = proto.String(t.GetBody())
			dm.BodyRanges = backupRanges(t.GetBodyRanges())
		}
		if lt := sm.GetLongText(); lt != nil {
			if p := ptr(lt, 0, nil); p != nil {
				p.ContentType = proto.String(longTextContentType)
				dm.Attachments = append(dm.Attachments, p)
			}
		}
		for _, a := range sm.GetAttachments() {
			if p := ptr(a.GetPointer(), a.GetFlag(), a.GetClientUuid()); p != nil {
				dm.Attachments = append(dm.Attachments, p)
			}
		}
		for _, lp := range sm.GetLinkPreview() {
			pv := &signalpb.Preview{Url: proto.String(lp.GetUrl()), Title: lp.Title, Description: lp.Description, Date: lp.Date}
			if lp.GetImage() != nil {
				pv.Image = ptr(lp.GetImage(), 0, nil)
			}
			dm.Preview = append(dm.Preview, pv)
		}
		if q := sm.GetQuote(); q != nil {
			dm.Quote = backupQuote(bc, q, ptr)
		}
	case *backuppb.ChatItem_ContactMessage:
		reactions = it.ContactMessage.GetReactions()
		dm.Contact = []*signalpb.DataMessage_Contact{backupContact(it.ContactMessage.GetContact())}
	case *backuppb.ChatItem_StickerMessage:
		reactions = it.StickerMessage.GetReactions()
		st := it.StickerMessage.GetSticker()
		dm.Sticker = &signalpb.DataMessage_Sticker{
			PackId: st.GetPackId(), PackKey: st.GetPackKey(), StickerId: proto.Uint32(st.GetStickerId()),
			Emoji: st.Emoji, Data: ptr(st.GetData(), backuppb.MessageAttachment_BORDERLESS, nil),
		}
	case *backuppb.ChatItem_RemoteDeletedMessage, *backuppb.ChatItem_AdminDeletedMessage:
		deleted = true
	case *backuppb.ChatItem_PaymentNotification:
		dm.Payment = &signalpb.DataMessage_Payment{Item: &signalpb.DataMessage_Payment_Notification_{
			Notification: &signalpb.DataMessage_Payment_Notification{Note: it.PaymentNotification.Note},
		}}
	case *backuppb.ChatItem_GiftBadge:
		dm.GiftBadge = &signalpb.DataMessage_GiftBadge{ReceiptCredentialPresentation: it.GiftBadge.GetReceiptCredentialPresentation()}
	case *backuppb.ChatItem_ViewOnceMessage:
		reactions = it.ViewOnceMessage.GetReactions()
		dm.IsViewOnce = proto.Bool(true)
		if a := it.ViewOnceMessage.GetAttachment(); a != nil {
			if p := ptr(a.GetPointer(), a.GetFlag(), a.GetClientUuid()); p != nil {
				dm.Attachments = []*signalpb.AttachmentPointer{p}
			}
		}
	case *backuppb.ChatItem_DirectStoryReplyMessage:
		r := it.DirectStoryReplyMessage
		reactions = r.GetReactions()
		storyReply = true
		switch rep := r.GetReply().(type) {
		case *backuppb.DirectStoryReplyMessage_TextReply_:
			dm.Body = proto.String(rep.TextReply.GetText().GetBody())
			dm.BodyRanges = backupRanges(rep.TextReply.GetText().GetBodyRanges())
		case *backuppb.DirectStoryReplyMessage_Emoji:
			dm.Body = proto.String(rep.Emoji)
		}
	case *backuppb.ChatItem_Poll:
		p := it.Poll
		pc := &signalpb.DataMessage_PollCreate{Question: proto.String(p.GetQuestion()), AllowMultiple: proto.Bool(p.GetAllowMultiple())}
		pollVotes = map[int][]core.ACI{}
		for i, o := range p.GetOptions() {
			pc.Options = append(pc.Options, o.GetOption())
			for _, v := range o.GetVotes() {
				if aci := bc.ACIOf(v.GetVoterId()); aci != "" {
					pollVotes[i] = append(pollVotes[i], aci)
				}
			}
		}
		dm.PollCreate = pc
		pollEnded = p.GetHasEnded()
	case *backuppb.ChatItem_UpdateMessage:
		switch up := it.UpdateMessage.GetUpdate().(type) {
		case *backuppb.ChatUpdateMessage_ExpirationTimerChange:
			kind = core.KindSystem
			system = timerChangeText(author, bc.Self, bc.name,
				time.Duration(up.ExpirationTimerChange.GetExpiresInMs())*time.Millisecond)
		case *backuppb.ChatUpdateMessage_IndividualCall:
			c := up.IndividualCall
			kind = core.KindCall
			call = &core.CallInfo{
				Video:     c.GetType() == backuppb.IndividualCall_VIDEO_CALL,
				Incoming:  c.GetDirection() != backuppb.IndividualCall_OUTGOING,
				Missed:    c.GetState() == backuppb.IndividualCall_MISSED || c.GetState() == backuppb.IndividualCall_MISSED_NOTIFICATION_PROFILE,
				Declined:  c.GetState() == backuppb.IndividualCall_NOT_ACCEPTED,
				StartedAt: msTime(c.GetStartedCallTimestamp()),
			}
		case *backuppb.ChatUpdateMessage_GroupCall:
			c := up.GroupCall
			kind = core.KindCall
			call = &core.CallInfo{
				Group:     true,
				Incoming:  c.GetState() != backuppb.GroupCall_OUTGOING_RING,
				Missed:    c.GetState() == backuppb.GroupCall_MISSED || c.GetState() == backuppb.GroupCall_MISSED_NOTIFICATION_PROFILE,
				Declined:  c.GetState() == backuppb.GroupCall_DECLINED,
				StartedAt: msTime(c.GetStartedCallTimestamp()),
			}
			if end := c.GetEndedCallTimestamp(); end > c.GetStartedCallTimestamp() && c.GetStartedCallTimestamp() > 0 {
				call.Duration = time.Duration(end-c.GetStartedCallTimestamp()) * time.Millisecond
			}
		default:
			return backupItem{}, false
		}
	default:
		return backupItem{}, false
	}

	cc := convCtx{Self: bc.Self, Chat: bc.Chat, Sender: author}
	conv := convertDataMessage(cc, dm)
	m := &conv.Msg
	if in := ci.GetIncoming(); in != nil {
		m.ReceivedAt = msTime(in.GetDateReceived())
		m.ServerTimestamp = in.GetDateServerSent()
	} else if out := ci.GetOutgoing(); out != nil {
		m.ReceivedAt = msTime(out.GetDateReceived())
		m.Status, m.PerRecipient = backupSendStatus(bc, out.GetSendStatus())
	}
	if ms := ci.GetExpiresInMs(); ms > 0 {
		m.ExpiresIn = time.Duration(ms) * time.Millisecond
		m.ExpireStartedAt = msTime(ci.GetExpireStartDate())
	}
	switch {
	case deleted:
		remoteDelete(m)
		conv.Atts, conv.LongText = nil, nil
	case kind == core.KindSystem:
		m.Kind, m.SystemText = kind, system
	case kind == core.KindCall:
		m.Kind, m.Call = kind, call
	}
	if storyReply {
		m.Kind = core.KindStoryReply
		storyAuthor := bc.Self
		if outgoing {
			storyAuthor = bc.Peer
		}
		m.StoryContext = &core.StoryRef{Author: storyAuthor}
		if r, ok := ci.GetDirectStoryReplyMessage().GetReply().(*backuppb.DirectStoryReplyMessage_Emoji); ok {
			m.StoryContext.Emoji, m.Body = r.Emoji, ""
		}
	}
	if m.Poll != nil {
		m.Poll.Ended = pollEnded
		for i, voters := range pollVotes {
			if i < len(m.Poll.Options) {
				m.Poll.Options[i].Voters = voters
			}
		}
	}
	for _, r := range reactions {
		if aci := bc.ACIOf(r.GetAuthorId()); aci != "" && r.GetEmoji() != "" {
			m.Reactions = append(m.Reactions, core.Reaction{Emoji: r.GetEmoji(), From: aci, At: msTime(r.GetSentTimestamp())})
		}
	}
	sort.SliceStable(m.Reactions, func(i, j int) bool { return m.Reactions[i].At.Before(m.Reactions[j].At) })
	for _, rev := range ci.GetRevisions() {
		if sm := rev.GetStandardMessage(); sm != nil {
			body := sm.GetText().GetBody()
			m.EditHistory = append(m.EditHistory, core.EditRevision{
				Body:   body,
				Ranges: rangesFromSignal(body, backupRanges(sm.GetText().GetBodyRanges())),
				At:     msTime(rev.GetDateSent()),
			})
		}
	}
	m.Edited = len(m.EditHistory) > 0
	if pd := ci.GetPinDetails(); pd != nil {
		m.Pinned = pd.GetPinNeverExpires() || pd.GetPinExpiresAtTimestamp() > uint64(time.Now().UnixMilli())
	}
	for i := range conv.Atts {
		conv.Atts[i].PlaintextHash = hashes[conv.Atts[i].Pointer]
	}
	if conv.LongText != nil {
		conv.LongText.PlaintextHash = hashes[conv.LongText.Pointer]
	}
	read := outgoing || ci.GetDirectionless() != nil || ci.GetIncoming().GetRead()
	return backupItem{converted: conv, Read: read}, true
}

// backupPointer converts a backup file pointer to an attachment pointer
// downloadable from the transit tier, plus its plaintext hash (used when the
// encrypted digest is absent). Media-tier-only files get a pointer without a
// location, which converts to a Failed attachment.
func backupPointer(fp *backuppb.FilePointer, flag backuppb.MessageAttachment_Flag, clientUUID []byte) (*signalpb.AttachmentPointer, []byte) {
	if fp == nil {
		return nil, nil
	}
	p := &signalpb.AttachmentPointer{
		ContentType:    fp.ContentType,
		FileName:       fp.FileName,
		Width:          fp.Width,
		Height:         fp.Height,
		BlurHash:       fp.BlurHash,
		IncrementalMac: fp.IncrementalMac,
	}
	switch flag {
	case backuppb.MessageAttachment_VOICE_MESSAGE:
		p.Flags = proto.Uint32(uint32(signalpb.AttachmentPointer_VOICE_MESSAGE))
	case backuppb.MessageAttachment_BORDERLESS:
		p.Flags = proto.Uint32(uint32(signalpb.AttachmentPointer_BORDERLESS))
	case backuppb.MessageAttachment_GIF:
		p.Flags = proto.Uint32(uint32(signalpb.AttachmentPointer_GIF))
	}
	if len(clientUUID) == 16 {
		p.ClientUuid = clientUUID
	} else {
		u := uuid.New()
		p.ClientUuid = u[:]
	}
	var hash []byte
	if li := fp.GetLocatorInfo(); li != nil {
		p.Key = li.GetKey()
		p.Size = proto.Uint32(li.GetSize())
		if li.TransitCdnKey != nil {
			p.AttachmentIdentifier = &signalpb.AttachmentPointer_CdnKey{CdnKey: li.GetTransitCdnKey()}
			p.CdnNumber = li.TransitCdnNumber
		}
		p.Digest = li.GetEncryptedDigest()
		hash = li.GetPlaintextHash()
	}
	return p, hash
}

// backupRanges converts backup body ranges to Signal's wire form.
func backupRanges(in []*backuppb.BodyRange) []*signalpb.BodyRange {
	var out []*signalpb.BodyRange
	for _, r := range in {
		if r == nil {
			continue
		}
		br := &signalpb.BodyRange{Start: proto.Uint32(r.GetStart()), Length: proto.Uint32(r.GetLength())}
		switch v := r.GetAssociatedValue().(type) {
		case *backuppb.BodyRange_MentionAci:
			br.AssociatedValue = &signalpb.BodyRange_MentionAciBinary{MentionAciBinary: v.MentionAci}
		case *backuppb.BodyRange_Style_:
			br.AssociatedValue = &signalpb.BodyRange_Style_{Style: signalpb.BodyRange_Style(v.Style)}
		default:
			continue
		}
		out = append(out, br)
	}
	return out
}

// backupQuote converts a backup quote.
func backupQuote(bc backupCtx, q *backuppb.Quote, ptr func(*backuppb.FilePointer, backuppb.MessageAttachment_Flag, []byte) *signalpb.AttachmentPointer) *signalpb.DataMessage_Quote {
	out := &signalpb.DataMessage_Quote{Id: proto.Uint64(q.GetTargetSentTimestamp())}
	if aci := bc.ACIOf(q.GetAuthorId()); aci != "" {
		out.AuthorAci = proto.String(string(aci))
	}
	if t := q.GetText(); t != nil {
		out.Text = proto.String(t.GetBody())
		out.BodyRanges = backupRanges(t.GetBodyRanges())
	}
	switch q.GetType() {
	case backuppb.Quote_GIFT_BADGE:
		out.Type = signalpb.DataMessage_Quote_GIFT_BADGE.Enum()
	case backuppb.Quote_POLL:
		out.Type = signalpb.DataMessage_Quote_POLL.Enum()
	}
	for _, a := range q.GetAttachments() {
		qa := &signalpb.DataMessage_Quote_QuotedAttachment{ContentType: a.ContentType, FileName: a.FileName}
		if th := a.GetThumbnail(); th != nil {
			qa.Thumbnail = ptr(th.GetPointer(), th.GetFlag(), th.GetClientUuid())
		}
		out.Attachments = append(out.Attachments, qa)
	}
	return out
}

// backupContact converts a backup contact card.
func backupContact(c *backuppb.ContactAttachment) *signalpb.DataMessage_Contact {
	out := &signalpb.DataMessage_Contact{}
	if c == nil {
		return out
	}
	if n := c.GetName(); n != nil {
		out.Name = &signalpb.DataMessage_Contact_Name{
			GivenName: proto.String(n.GetGivenName()), FamilyName: proto.String(n.GetFamilyName()),
			Prefix: proto.String(n.GetPrefix()), Suffix: proto.String(n.GetSuffix()),
			MiddleName: proto.String(n.GetMiddleName()), Nickname: proto.String(n.GetNickname()),
		}
	}
	for _, p := range c.GetNumber() {
		out.Number = append(out.Number, &signalpb.DataMessage_Contact_Phone{Value: proto.String(p.GetValue())})
	}
	for _, e := range c.GetEmail() {
		out.Email = append(out.Email, &signalpb.DataMessage_Contact_Email{Value: proto.String(e.GetValue())})
	}
	if c.GetOrganization() != "" {
		out.Organization = proto.String(c.GetOrganization())
	}
	return out
}

// backupSendStatus folds per-recipient send statuses.
func backupSendStatus(bc backupCtx, ss []*backuppb.SendStatus) (core.DeliveryStatus, map[core.ACI]core.DeliveryStatus) {
	per := map[core.ACI]core.DeliveryStatus{}
	for _, s := range ss {
		aci := bc.ACIOf(s.GetRecipientId())
		if aci == "" {
			continue
		}
		var st core.DeliveryStatus
		switch s.GetDeliveryStatus().(type) {
		case *backuppb.SendStatus_Sent_, *backuppb.SendStatus_Skipped_:
			st = core.StatusSent
		case *backuppb.SendStatus_Delivered_:
			st = core.StatusDelivered
		case *backuppb.SendStatus_Read_:
			st = core.StatusRead
		case *backuppb.SendStatus_Viewed_:
			st = core.StatusViewed
		case *backuppb.SendStatus_Failed_:
			st = core.StatusFailed
		default:
			st = core.StatusPending
		}
		per[aci] = st
	}
	if len(per) == 0 {
		return core.StatusSent, nil
	}
	best, failed := core.StatusNone, 0
	for _, s := range per {
		if s == core.StatusFailed {
			failed++
			continue
		}
		best = max(best, s)
	}
	if failed == len(per) {
		best = core.StatusFailed
	}
	if !bc.Chat.IsGroup() {
		per = nil
	}
	return best, per
}
