// mautrix-signal - A Matrix-signal puppeting bridge.
// Copyright (C) 2026 warte contributors
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package signalmeow

// warte: this file surfaces content that upstream signalmeow drops on the
// receive path: stories (and our own story sync transcripts), the sync
// messages it ignores (blocked list, configuration, sticker pack operations,
// viewed, call events, call log events, PNI change number) and richer call
// signalling (offer type, hangup type, group rings carried in RingRTC's
// opaque messages). The protocol behaviour follows Signal-Desktop's
// MessageReceiver.

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"google.golang.org/protobuf/encoding/protowire"

	"go.mau.fi/mautrix-signal/pkg/libsignalgo"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/events"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/types"
)

// warte: incomingStoryMessage handles Content.storyMessage from another
// user. Like Signal-Desktop it drops stories from blocked senders and never
// sends delivery receipts for stories. The story's timestamp is the
// envelope's client timestamp.
func (cli *Client) incomingStoryMessage(
	ctx context.Context,
	story *signalpb.StoryMessage,
	sender libsignalgo.ServiceID,
	envelope *signalpb.Envelope,
	isBlocked bool,
) (handlerSuccess bool) {
	log := zerolog.Ctx(ctx)
	if isBlocked {
		log.Debug().Msg("Dropping story from blocked user")
		return true
	}
	if !envelope.GetStory() {
		log.Debug().Msg("Story content without the envelope's story flag")
	}
	if len(story.GetProfileKey()) == libsignalgo.ProfileKeyLength {
		profileKey := libsignalgo.ProfileKey(story.GetProfileKey())
		if err := cli.Store.RecipientStore.StoreProfileKey(ctx, sender.UUID, profileKey); err != nil {
			log.Err(err).Msg("StoreProfileKey error (story)")
			return false
		}
	}
	groupID, groupRevision, ok, err := cli.storyGroup(ctx, story)
	if err != nil {
		return false
	} else if !ok {
		return true
	}
	info := events.MessageInfo{
		Sender:          sender.UUID,
		ChatID:          groupOrUserID(groupID, sender),
		GroupRevision:   groupRevision,
		ServerTimestamp: envelope.GetServerTimestamp(),
	}
	if guid, _ := ParseStringOrBinaryUUID(envelope.GetServerGuid(), envelope.GetServerGuidBinary()); guid != uuid.Nil {
		info.ServerGUID = guid.String()
	}
	info.ReportingToken = envelope.GetReportSpamToken()
	return cli.handleEvent(&events.Story{
		Info:          info,
		Timestamp:     envelope.GetClientTimestamp(),
		Story:         story,
		GroupID:       groupID,
		AllowsReplies: story.GetAllowsReplies(),
		EnvelopeStory: envelope.GetStory(),
	})
}

// warte: storyEnvelopeContentAllowed reports whether content may arrive in
// an envelope with the story flag (Signal-Desktop MessageReceiver): a
// story, a group story reply (storyContext with a group) or a delete.
// 1:1 story replies are never sent with story=true.
func storyEnvelopeContentAllowed(content *signalpb.Content) bool {
	if content.GetStoryMessage() != nil {
		return true
	}
	dm := content.GetDataMessage()
	if dm.GetDelete() != nil {
		return true
	}
	return dm.GetStoryContext() != nil && dm.GetGroupV2() != nil
}

// warte: storyGroup stores the master key of a group story and returns its
// identifier. ok is false if the story must be dropped (malformed key).
func (cli *Client) storyGroup(ctx context.Context, story *signalpb.StoryMessage) (gid types.GroupIdentifier, rev uint32, ok bool, err error) {
	if story.GetGroup() == nil {
		return "", 0, true, nil
	}
	mk := story.GetGroup().GetMasterKey()
	if len(mk) != libsignalgo.GroupMasterKeyLength {
		zerolog.Ctx(ctx).Warn().Int("group_master_key_length", len(mk)).Msg("Unexpected group master key length in story")
		return "", 0, false, nil
	}
	gid, err = cli.StoreMasterKey(ctx, masterKeyFromBytes(libsignalgo.GroupMasterKey(mk)))
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("StoreMasterKey error (story)")
		return "", 0, false, err
	}
	return gid, story.GetGroup().GetRevision(), true, nil
}

// warte: storyRecipients converts Sent.storyMessageRecipients.
func storyRecipients(sent *signalpb.SyncMessage_Sent) []events.StoryRecipient {
	var out []events.StoryRecipient
	for _, r := range sent.GetStoryMessageRecipients() {
		sid, err := ParseStringOrBinaryServiceID(r.GetDestinationServiceId(), r.GetDestinationServiceIdBinary())
		if err != nil || sid.IsEmpty() {
			continue
		}
		out = append(out, events.StoryRecipient{
			ServiceID:           sid.String(),
			DistributionListIDs: r.GetDistributionListIds(),
			AllowedToReply:      r.GetIsAllowedToReply(),
		})
	}
	return out
}

// warte: handleSyncSentStory handles the story parts of SyncMessage.Sent,
// in Signal-Desktop's order: a recipient update without content first, then
// a sent story. It reports whether sent was a story transcript at all.
func (cli *Client) handleSyncSentStory(ctx context.Context, sent *signalpb.SyncMessage_Sent, envelope *signalpb.Envelope) (isStory, handlerSuccess bool) {
	ts := sent.GetTimestamp()
	if ts == 0 {
		ts = envelope.GetClientTimestamp()
	}
	// warte: Signal-Desktop treats any isRecipientUpdate transcript with
	// storyMessageRecipients as a recipient update, story content or not.
	if sent.GetIsRecipientUpdate() && len(sent.GetStoryMessageRecipients()) > 0 {
		return true, cli.handleEvent(&events.StoryRecipientUpdate{
			Timestamp:  ts,
			Recipients: storyRecipients(sent),
		})
	}
	story := sent.GetStoryMessage()
	if story == nil {
		return false, true
	}
	groupID, groupRevision, ok, err := cli.storyGroup(ctx, story)
	if err != nil {
		return true, false
	} else if !ok {
		return true, true
	}
	return true, cli.handleEvent(&events.Story{
		Info: events.MessageInfo{
			Sender:          cli.Store.ACI,
			ChatID:          groupOrUserID(groupID, cli.Store.ACIServiceID()),
			GroupRevision:   groupRevision,
			ServerTimestamp: envelope.GetServerTimestamp(),
		},
		Timestamp:         ts,
		Story:             story,
		GroupID:           groupID,
		AllowsReplies:     story.GetAllowsReplies(),
		IsSync:            true,
		Recipients:        storyRecipients(sent),
		IsRecipientUpdate: sent.GetIsRecipientUpdate(),
		EnvelopeStory:     envelope.GetStory(),
	})
}

// warte: blockedFromSync converts SyncMessage.blocked, merging the
// deprecated and current fields and dropping duplicates and malformed IDs.
func blockedFromSync(b *signalpb.SyncMessage_Blocked) *events.BlockedSync {
	out := &events.BlockedSync{}
	seenACI := map[uuid.UUID]bool{}
	addACI := func(u uuid.UUID) {
		if u != uuid.Nil && !seenACI[u] {
			seenACI[u] = true
			out.ACIs = append(out.ACIs, u)
		}
	}
	for _, s := range b.GetAcis() {
		if u, err := uuid.Parse(s); err == nil {
			addACI(u)
		}
	}
	for _, raw := range b.GetAcisBinary() {
		if len(raw) == 16 {
			addACI(uuid.UUID(raw))
		}
	}
	for _, ba := range b.GetBlockedAcis() {
		if raw := ba.GetAciBinary(); len(raw) == 16 {
			addACI(uuid.UUID(raw))
		}
	}
	seenE164 := map[string]bool{}
	addE164 := func(s string) {
		if s != "" && !seenE164[s] {
			seenE164[s] = true
			out.E164s = append(out.E164s, s)
		}
	}
	for _, n := range b.GetNumbers() {
		addE164(n)
	}
	for _, be := range b.GetBlockedE164S() {
		addE164(be.GetE164())
	}
	seenGroup := map[types.GroupIdentifier]bool{}
	addGroup := func(raw []byte) {
		if len(raw) != libsignalgo.GroupIdentifierLength {
			return
		}
		gid := types.GroupIdentifier(base64.StdEncoding.EncodeToString(raw))
		if !seenGroup[gid] {
			seenGroup[gid] = true
			out.GroupIDs = append(out.GroupIDs, gid)
		}
	}
	for _, g := range b.GetGroupIds() {
		addGroup(g)
	}
	for _, bg := range b.GetBlockedGroups() {
		addGroup(bg.GetGroupId())
	}
	return out
}

// warte: applyBlockedSync mirrors a blocked-list sync into the recipient
// store (which signalmeow consults to drop messages from blocked users).
// The list is complete: recipients not on it are unblocked.
func (cli *Client) applyBlockedSync(ctx context.Context, b *events.BlockedSync) error {
	blocked := make(map[uuid.UUID]bool, len(b.ACIs))
	for _, u := range b.ACIs {
		blocked[u] = true
	}
	set := func(aci uuid.UUID, value bool) error {
		_, err := cli.Store.RecipientStore.LoadAndUpdateRecipient(ctx, aci, uuid.Nil, func(r *types.Recipient) (bool, error) {
			changed := r.Blocked != value
			r.Blocked = value
			return changed, nil
		})
		return err
	}
	for u := range blocked {
		if err := set(u, true); err != nil {
			return err
		}
	}
	for _, e164 := range b.E164s {
		r, err := cli.Store.RecipientStore.LoadRecipientByE164(ctx, e164)
		if err == nil && r != nil && r.ACI != uuid.Nil && !blocked[r.ACI] {
			blocked[r.ACI] = true
			if err := set(r.ACI, true); err != nil {
				return err
			}
		}
	}
	all, err := cli.Store.RecipientStore.LoadAllContacts(ctx)
	if err != nil {
		return err
	}
	for _, r := range all {
		if r != nil && r.Blocked && r.ACI != uuid.Nil && !blocked[r.ACI] {
			if err := set(r.ACI, false); err != nil {
				return err
			}
		}
	}
	return nil
}

// warte: configurationFromSync converts SyncMessage.configuration.
func configurationFromSync(c *signalpb.SyncMessage_Configuration) *events.ConfigurationSync {
	return &events.ConfigurationSync{
		ReadReceipts:                   c.ReadReceipts,
		UnidentifiedDeliveryIndicators: c.UnidentifiedDeliveryIndicators,
		TypingIndicators:               c.TypingIndicators,
		LinkPreviews:                   c.LinkPreviews,
	}
}

// warte: stickerOpsFromSync converts SyncMessage.stickerPackOperation,
// skipping operations without a valid 16-byte pack ID. A missing type
// means INSTALL (the proto default).
func stickerOpsFromSync(ops []*signalpb.SyncMessage_StickerPackOperation) []events.StickerPackOperation {
	var out []events.StickerPackOperation
	for _, op := range ops {
		if len(op.GetPackId()) != 16 {
			continue
		}
		out = append(out, events.StickerPackOperation{
			PackID:  op.GetPackId(),
			PackKey: op.GetPackKey(),
			Install: op.GetType() == signalpb.SyncMessage_StickerPackOperation_INSTALL,
		})
	}
	return out
}

// warte: viewedFromSync converts SyncMessage.viewed.
func viewedFromSync(vs []*signalpb.SyncMessage_Viewed) []events.ViewedMessage {
	var out []events.ViewedMessage
	for _, v := range vs {
		u, err := ParseStringOrBinaryUUID(v.GetSenderAci(), v.GetSenderAciBinary())
		if err != nil || u == uuid.Nil || v.GetTimestamp() == 0 {
			continue
		}
		out = append(out, events.ViewedMessage{Sender: u, Timestamp: v.GetTimestamp()})
	}
	return out
}

// warte: handleWarteSyncMessage handles the sync message contents upstream
// ignores. It reports whether msg's content was one of them.
func (cli *Client) handleWarteSyncMessage(ctx context.Context, msg *signalpb.SyncMessage, envelope *signalpb.Envelope) (handled, handlerSuccess bool) {
	log := zerolog.Ctx(ctx)
	ts := envelope.GetClientTimestamp()
	switch content := msg.Content.(type) {
	case *signalpb.SyncMessage_Blocked_:
		b := blockedFromSync(content.Blocked)
		if err := cli.applyBlockedSync(ctx, b); err != nil {
			log.Err(err).Msg("Failed to apply blocked list sync to recipient store")
		}
		return true, cli.handleEvent(b)
	case *signalpb.SyncMessage_Configuration_:
		return true, cli.handleEvent(configurationFromSync(content.Configuration))
	case *signalpb.SyncMessage_CallEvent_:
		return true, cli.handleEvent(&events.CallEventSync{Timestamp: ts, Event: content.CallEvent})
	case *signalpb.SyncMessage_CallLogEvent_:
		return true, cli.handleEvent(&events.CallLogEventSync{Timestamp: ts, Event: content.CallLogEvent})
	case *signalpb.SyncMessage_PniChangeNumber_:
		log.Info().Msg("Our phone number changed on the primary device (PNI change number sync is not applied)")
		return true, cli.handleEvent(&events.PniChangeNumber{NewE164: content.PniChangeNumber.GetNewE164()})
	}
	return false, true
}

// warte: handleWarteSyncRepeated handles the repeated (non-oneof) sync
// fields upstream ignores: sticker pack operations and viewed messages.
func (cli *Client) handleWarteSyncRepeated(msg *signalpb.SyncMessage, envelope *signalpb.Envelope) bool {
	ok := true
	if ops := stickerOpsFromSync(msg.GetStickerPackOperation()); len(ops) > 0 {
		ok = cli.handleEvent(&events.StickerPackSync{Operations: ops}) && ok
	}
	if viewed := viewedFromSync(msg.GetViewed()); len(viewed) > 0 {
		ok = cli.handleEvent(&events.ViewedSync{Timestamp: envelope.GetClientTimestamp(), Messages: viewed}) && ok
	}
	return ok
}

// warte: ringRTCMessage is the subset of RingRTC's signaling.proto
// CallMessage carried in Signal's CallMessage.opaque.data that matters for
// a device that never joins calls:
//
//	message CallMessage {
//	  optional group_call.DeviceToDevice group_call_message = 1;
//	  optional RingIntention ring_intention = 2; // group_id=1 bytes, type=2 enum, ring_id=3 sfixed64
//	  optional RingResponse ring_response = 3;   // group_id=1 bytes, type=2 enum, ring_id=3 sfixed64
//	}
type ringRTCMessage struct {
	HasIntention bool
	Intention    ringFields
	HasResponse  bool
	Response     ringFields
}

type ringFields struct {
	GroupID []byte
	Type    int
	RingID  int64
}

var errBadRingRTCMessage = errors.New("malformed RingRTC call message")

// warte: parseRingRTCMessage decodes RingRTC's opaque CallMessage. Unknown
// fields (including group_call_message) are skipped.
func parseRingRTCMessage(data []byte) (ringRTCMessage, error) {
	var out ringRTCMessage
	err := walkProto(data, func(num protowire.Number, typ protowire.Type, v []byte, _ uint64) error {
		if typ != protowire.BytesType || (num != 2 && num != 3) {
			return nil
		}
		f, err := parseRingFields(v)
		if err != nil {
			return err
		}
		if num == 2 {
			out.HasIntention, out.Intention = true, f
		} else {
			out.HasResponse, out.Response = true, f
		}
		return nil
	})
	return out, err
}

func parseRingFields(data []byte) (ringFields, error) {
	var f ringFields
	err := walkProto(data, func(num protowire.Number, typ protowire.Type, v []byte, n uint64) error {
		switch {
		case num == 1 && typ == protowire.BytesType:
			f.GroupID = append([]byte(nil), v...)
		case num == 2 && typ == protowire.VarintType:
			f.Type = int(int32(n))
		case num == 3 && typ == protowire.Fixed64Type:
			f.RingID = int64(n)
		}
		return nil
	})
	return f, err
}

// warte: walkProto calls fn for each field of a protobuf message. For
// bytes fields v holds the payload; for varint and fixed fields n holds the
// value.
func walkProto(data []byte, fn func(num protowire.Number, typ protowire.Type, v []byte, n uint64) error) error {
	for len(data) > 0 {
		num, typ, l := protowire.ConsumeTag(data)
		if l < 0 {
			return errBadRingRTCMessage
		}
		data = data[l:]
		var v []byte
		var n uint64
		switch typ {
		case protowire.VarintType:
			n, l = protowire.ConsumeVarint(data)
		case protowire.Fixed64Type:
			n, l = protowire.ConsumeFixed64(data)
		case protowire.Fixed32Type:
			var n32 uint32
			n32, l = protowire.ConsumeFixed32(data)
			n = uint64(n32)
		case protowire.BytesType:
			v, l = protowire.ConsumeBytes(data)
		default:
			l = protowire.ConsumeFieldValue(num, typ, data)
		}
		if l < 0 {
			return errBadRingRTCMessage
		}
		data = data[l:]
		if err := fn(num, typ, v, n); err != nil {
			return err
		}
	}
	return nil
}

// warte: callFromCallMessage classifies a CallMessage. ok is false for
// signalling a device that never takes part in calls can ignore (ICE
// updates, group call media keys, ring "ringing" acknowledgements).
func callFromCallMessage(cm *signalpb.CallMessage) (evt events.Call, ok bool) {
	evt.DestinationDeviceID = cm.GetDestinationDeviceId()
	switch {
	case cm.GetOffer() != nil:
		evt.Kind, evt.IsRinging = events.CallKindOffer, true
		evt.CallID = cm.GetOffer().GetId()
		evt.IsVideo = cm.GetOffer().GetType() == signalpb.CallMessage_Offer_OFFER_VIDEO_CALL
		return evt, true
	case cm.GetHangup() != nil:
		evt.Kind = events.CallKindHangup
		evt.CallID = cm.GetHangup().GetId()
		evt.HangupType = cm.GetHangup().GetType()
		evt.HangupDeviceID = cm.GetHangup().GetDeviceId()
		return evt, true
	case cm.GetBusy() != nil:
		evt.Kind, evt.CallID = events.CallKindBusy, cm.GetBusy().GetId()
		return evt, true
	case cm.GetAnswer() != nil:
		evt.Kind, evt.CallID = events.CallKindAnswer, cm.GetAnswer().GetId()
		return evt, true
	case cm.GetOpaque() != nil:
		rm, err := parseRingRTCMessage(cm.GetOpaque().GetData())
		if err != nil {
			return evt, false
		}
		var f ringFields
		switch {
		case rm.HasIntention:
			f = rm.Intention
			evt.Kind = events.CallKindRingIntention
			evt.RingIntention = events.RingIntentionType(f.Type)
			evt.IsRinging = evt.RingIntention == events.RingIntentionRing
		case rm.HasResponse:
			f = rm.Response
			evt.Kind = events.CallKindRingResponse
			evt.RingResponse = events.RingResponseType(f.Type)
			if evt.RingResponse == events.RingResponseRinging {
				return evt, false
			}
		default:
			return evt, false
		}
		if len(f.GroupID) != libsignalgo.GroupIdentifierLength {
			return evt, false
		}
		evt.GroupID = types.GroupIdentifier(base64.StdEncoding.EncodeToString(f.GroupID))
		evt.CallID = uint64(f.RingID)
		return evt, true
	}
	return evt, false
}

// warte: incomingCallMessage emits an events.Call for the call signalling
// that matters to warte. Calls from blocked users are dropped, as in
// Signal-Desktop.
func (cli *Client) incomingCallMessage(ctx context.Context, cm *signalpb.CallMessage, sender libsignalgo.ServiceID, envelope *signalpb.Envelope, isBlocked bool) bool {
	if isBlocked {
		return true
	}
	evt, ok := callFromCallMessage(cm)
	if !ok {
		return true
	}
	evt.Info = events.MessageInfo{
		Sender:          sender.UUID,
		ChatID:          groupOrUserID(evt.GroupID, sender),
		ServerTimestamp: envelope.GetServerTimestamp(),
	}
	// CallMessage doesn't have its own timestamp, use one from the envelope
	evt.Timestamp = envelope.GetClientTimestamp()
	zerolog.Ctx(ctx).Debug().
		Int("call_kind", int(evt.Kind)).
		Uint64("call_id", evt.CallID).
		Msg("Call message")
	return cli.handleEvent(&evt)
}

// warte: SendReceiptWithoutSync sends a receipt to recipient. Unlike
// SendMessage it does not send a sync copy to our other devices (SendMessage
// turns a single VIEWED receipt into a ViewOnceOpen sync, which is wrong for
// stories and voice notes) and does not consult the read receipt setting;
// the caller decides. Like SendMessage it does not send receipts to
// recipients that are probably message requests.
func (cli *Client) SendReceiptWithoutSync(ctx context.Context, recipientID libsignalgo.ServiceID, receipt *signalpb.ReceiptMessage) SendMessageResult {
	content := &signalpb.Content{Content: &signalpb.Content_ReceiptMessage{ReceiptMessage: receipt}}
	var aci, pni uuid.UUID
	switch recipientID.Type {
	case libsignalgo.ServiceIDTypeACI:
		aci = recipientID.UUID
	case libsignalgo.ServiceIDTypePNI:
		pni = recipientID.UUID
	}
	res := SuccessfulSendResult{Recipient: recipientID}
	recipientData, err := cli.Store.RecipientStore.LoadAndUpdateRecipient(ctx, aci, pni, nil)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to get receipt recipient data")
	} else if recipientData.ProbablyMessageRequest() {
		zerolog.Ctx(ctx).Debug().Msg("Not sending receipt to probable message request")
		return SendMessageResult{WasSuccessful: true, SuccessfulSendResult: res}
	}
	if recipientID == cli.Store.ACIServiceID() {
		return SendMessageResult{WasSuccessful: true, SuccessfulSendResult: res}
	}
	ts := currentMessageTimestamp()
	cli.addSendCache(recipientID, "", ts, content)
	res.Unidentified, err = cli.sendContent(ctx, recipientID, ts, content, 0, true, nil, nil)
	if err != nil {
		return SendMessageResult{FailedSendResult: FailedSendResult{Recipient: recipientID, Error: fmt.Errorf("send receipt: %w", err)}}
	}
	return SendMessageResult{WasSuccessful: true, SuccessfulSendResult: res}
}
