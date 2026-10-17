package backend

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"go.mau.fi/mautrix-signal/pkg/signalmeow/events"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"

	"warte/internal/core"
	"warte/internal/store"
)

// maxRing bounds how long an unanswered call is considered ringing if the
// hangup never arrives. Offers and rings older than this when they arrive
// (a backlog after being offline) are recorded as missed without ringing,
// like RingRTC's "received offer expired".
const maxRing = 2 * time.Minute

// groupCallMergeWindow is how close a group ring and the group call update
// announcing the same call must be to share one call message.
const groupCallMergeWindow = 5 * time.Minute

// callSignal is the pure input of a call state transition.
type callSignal struct {
	Kind          events.CallKind
	Chat          core.ChatID
	Sender, Self  core.ACI
	TS            uint64
	CallID        uint64
	Video         bool
	Expired       bool // too old to ring
	HangupType    signalpb.CallMessage_Hangup_Type
	RingIntention events.RingIntentionType
	RingResponse  events.RingResponseType
}

// ringing is a call that currently rings.
type ringing struct {
	Msg    *core.Message
	CallID uint64 // 1:1 call ID or group ring ID; 0 = unknown
}

// callUpdate is the outcome of a call signal.
type callUpdate struct {
	Message *core.Message // call message to store (new or updated)
	New     bool          // Message is new
	Ringing bool          // the call now rings (emit IncomingCall{Ringing:true})
	Ended   bool          // the ringing call ended (emit IncomingCall{Ringing:false})
	CallID  uint64        // ID of the ringing call (with Ringing)
}

// callSignalFromEvent normalises an events.Call. Events built by code that
// predates the richer fields (CallKindLegacy) are read the old way: ringing
// means an offer, anything else a normal hangup.
func callSignalFromEvent(e *events.Call, chat core.ChatID, self core.ACI, ts uint64) callSignal {
	sig := callSignal{
		Kind: e.Kind, Chat: chat, Sender: aciOf(e.Info.Sender), Self: self, TS: ts,
		CallID: e.CallID, Video: e.IsVideo, HangupType: e.HangupType,
		RingIntention: e.RingIntention, RingResponse: e.RingResponse,
	}
	if sig.Kind == events.CallKindLegacy {
		switch {
		case chat.IsGroup():
			sig.Kind = events.CallKindGroupUpdate
		case e.IsRinging:
			sig.Kind = events.CallKindOffer
		default:
			sig.Kind = events.CallKindHangup
		}
	}
	return sig
}

// newCallMessage builds the call message a ringing or observed call gets.
func newCallMessage(chat core.ChatID, author, self core.ACI, ts uint64, video bool, now time.Time) *core.Message {
	return &core.Message{
		ID:   core.MessageID{Chat: chat, Author: author, Timestamp: ts},
		Kind: core.KindCall, Outgoing: author == self,
		SentAt: msTime(ts), ReceivedAt: now,
		Call: &core.CallInfo{Video: video, Group: chat.IsGroup(), Incoming: author != self, StartedAt: msTime(ts)},
	}
}

// endCall returns a copy of the ringing call's message with its outcome.
func endCall(cur *core.Message, missed, declined bool) *core.Message {
	m := *cur
	c := *m.Call
	c.Missed, c.Declined = missed, declined
	m.Call = &c
	return &m
}

// callTransition computes what a call signal means given the call ringing
// in its chat (zero if none). warte never answers calls:
//
//   - a 1:1 offer or a group ring rings (or is recorded as missed if it
//     arrived too late);
//   - a normal hangup, busy hangup or ring cancellation by the caller makes
//     the ringing call missed;
//   - a hangup "accepted on device N" from the caller, a ring "accepted"
//     response from one of our own devices, or an answer/hangup from our own
//     devices means it was answered elsewhere; "declined" responses mark it
//     declined.
//
// Signals with a call ID that does not match the ringing call are ignored.
func callTransition(sig callSignal, cur ringing, now time.Time) callUpdate {
	matches := func() bool {
		return cur.Msg != nil && (sig.CallID == 0 || cur.CallID == 0 || sig.CallID == cur.CallID)
	}
	fromSelf := sig.Sender == sig.Self
	switch sig.Kind {
	case events.CallKindOffer, events.CallKindRingIntention:
		if sig.Kind == events.CallKindOffer && sig.Chat.IsGroup() {
			return callUpdate{}
		}
		if sig.Kind == events.CallKindRingIntention && !sig.Chat.IsGroup() {
			return callUpdate{}
		}
		if sig.Kind == events.CallKindRingIntention && sig.RingIntention == events.RingIntentionCancelled {
			if matches() && !fromSelf && cur.Msg.ID.Author == sig.Sender {
				return callUpdate{Message: endCall(cur.Msg, true, false), Ended: true}
			}
			return callUpdate{}
		}
		if fromSelf {
			return callUpdate{} // a call placed on another of our devices
		}
		if cur.Msg != nil && ((sig.CallID != 0 && cur.CallID == sig.CallID) || cur.Msg.ID.Timestamp == sig.TS) {
			return callUpdate{} // repeated offer
		}
		m := newCallMessage(sig.Chat, sig.Sender, sig.Self, sig.TS, sig.Video, now)
		if sig.Expired {
			m.Call.Missed = true
			return callUpdate{Message: m, New: true}
		}
		return callUpdate{Message: m, New: true, Ringing: true, CallID: sig.CallID}
	case events.CallKindHangup:
		if sig.Chat.IsGroup() || !matches() {
			return callUpdate{}
		}
		if fromSelf {
			return callUpdate{Message: endCall(cur.Msg, false, false), Ended: true}
		}
		if cur.Msg.ID.Author != sig.Sender {
			return callUpdate{}
		}
		switch sig.HangupType {
		case signalpb.CallMessage_Hangup_HANGUP_ACCEPTED:
			return callUpdate{Message: endCall(cur.Msg, false, false), Ended: true}
		case signalpb.CallMessage_Hangup_HANGUP_DECLINED:
			return callUpdate{Message: endCall(cur.Msg, false, true), Ended: true}
		}
		return callUpdate{Message: endCall(cur.Msg, true, false), Ended: true}
	case events.CallKindAnswer, events.CallKindBusy:
		if !fromSelf || sig.Chat.IsGroup() || !matches() {
			return callUpdate{}
		}
		return callUpdate{Message: endCall(cur.Msg, sig.Kind == events.CallKindBusy, false), Ended: true}
	case events.CallKindRingResponse:
		if !fromSelf || !sig.Chat.IsGroup() || !matches() {
			return callUpdate{}
		}
		switch sig.RingResponse {
		case events.RingResponseAccepted:
			return callUpdate{Message: endCall(cur.Msg, false, false), Ended: true}
		case events.RingResponseDeclined:
			return callUpdate{Message: endCall(cur.Msg, false, true), Ended: true}
		case events.RingResponseBusy:
			return callUpdate{Message: endCall(cur.Msg, true, false), Ended: true}
		}
	}
	return callUpdate{}
}

// groupCallStartedText is the system text of a group call start.
func groupCallStartedText(who, self core.ACI, name nameFunc) string {
	if who == self {
		return "You started a group call"
	}
	return actor(who, self, name) + " started a group call"
}

// groupCallState remembers the last group call seen in a group, so joins
// to the same call (same era) do not announce it again, also across
// restarts, and a ring can share the message of the update announcing it.
type groupCallState struct {
	Era string    `json:"e,omitempty"`
	Msg string    `json:"m,omitempty"` // core.MessageID of the call message
	At  time.Time `json:"t"`
}

// groupCallIsNew decides whether a group call update with era starts a new
// call given the remembered state. Clients without era IDs (old ones) are
// treated as the same call for groupCallMergeWindow.
func groupCallIsNew(st groupCallState, era string, now time.Time) bool {
	if st.At.IsZero() {
		return true
	}
	if era == "" {
		return now.Sub(st.At) > groupCallMergeWindow
	}
	return st.Era != era
}

func (b *Backend) onCall(ctx context.Context, e *events.Call) error {
	ts := e.Timestamp
	if ts == 0 {
		ts = uint64(b.now().UnixMilli())
	}
	chat, err := b.resolveChat(ctx, e.Info.ChatID)
	if err != nil {
		return err
	}
	if e.GroupID != "" {
		chat = groupChatID(e.GroupID)
	}
	if e.DestinationDeviceID != 0 {
		if cli, err := b.client(); err == nil && int(e.DestinationDeviceID) != cli.Store.DeviceID {
			return nil // signalling for another of our devices
		}
	}
	self := b.selfACI()
	sig := callSignalFromEvent(e, chat, self, ts)
	if st := e.Info.ServerTimestamp; st != 0 && b.now().Sub(msTime(st)) > b.ringTimeout {
		sig.Expired = true
	}
	if sig.Kind == events.CallKindGroupUpdate {
		return b.onGroupCallUpdate(ctx, sig, e.EraID)
	}
	if sig.Kind == events.CallKindRingIntention && sig.Sender != self {
		ok, err := b.acceptRing(ctx, sig)
		if err != nil || !ok {
			return err
		}
	}
	if sig.Sender == self && !chat.IsGroup() &&
		(sig.Kind == events.CallKindHangup || sig.Kind == events.CallKindAnswer || sig.Kind == events.CallKindBusy) {
		// Signalling from one of our own devices (addressed to us, so the
		// peer is unknown): the matching 1:1 call was handled there.
		return b.endAllDirect(ctx, sig)
	}

	b.callMu.Lock()
	cur := b.currentCall(chat)
	up := callTransition(sig, cur, b.now().UTC())
	if up.Message != nil && up.New && chat.IsGroup() && up.Ringing {
		// Share the call message of the group call update announcing it:
		// exactly by call ID (ring ID = callIDFromEra(era)), else by time.
		m, ok := b.callMessageByID(ctx, chat, sig.CallID)
		if !ok {
			m, ok = b.recentGroupCall(ctx, chat)
		}
		if ok {
			m.Call = &core.CallInfo{Group: true, Incoming: true, StartedAt: m.Call.StartedAt}
			up.Message, up.New = m, false
		}
	}
	prev := cur
	switch {
	case up.Ringing:
		b.calls[chat] = up.Message
		b.callIDs[chat] = up.CallID
		b.armRingTimeout(chat, up.Message.ID)
	case up.Ended:
		delete(b.calls, chat)
		delete(b.callIDs, chat)
	}
	b.callMu.Unlock()
	if up.Ringing && prev.Msg != nil && prev.Msg.ID != up.Message.ID {
		// A new call while another one rang: the old one is missed.
		if err := b.applyCall(ctx, chat, callUpdate{Message: endCall(prev.Msg, true, false), Ended: true}); err != nil {
			return err
		}
	}
	if sig.Kind == events.CallKindRingIntention && sig.RingIntention == events.RingIntentionCancelled && sig.CallID != 0 {
		b.rememberCancelledRing(ctx, sig.CallID)
	}
	if up.Message != nil && chat.IsGroup() && up.New {
		_ = b.kv.set(ctx, "group_call."+string(chat), groupCallState{Msg: up.Message.ID.String(), At: b.now()})
	}
	if up.Ringing || (up.New && sig.CallID != 0) {
		b.rememberCallID(ctx, sig.CallID, up.Message.ID)
	}
	return b.applyCall(ctx, chat, up)
}

// currentCall returns the ringing call of chat, dropping one that rang for
// longer than ringTimeout. callMu must be held.
func (b *Backend) currentCall(chat core.ChatID) ringing {
	cur := ringing{Msg: b.calls[chat], CallID: b.callIDs[chat]}
	if cur.Msg != nil && b.now().Sub(cur.Msg.ReceivedAt) > b.ringTimeout {
		delete(b.calls, chat)
		delete(b.callIDs, chat)
		return ringing{}
	}
	return cur
}

// endAllDirect applies signalling from one of our own devices to every
// ringing 1:1 call it matches (by call ID when both are known).
func (b *Backend) endAllDirect(ctx context.Context, sig callSignal) error {
	now := b.now().UTC()
	b.callMu.Lock()
	ups := map[core.ChatID]callUpdate{}
	for c, m := range b.calls {
		if !c.IsDirect() {
			continue
		}
		s := sig
		s.Chat = c
		if up := callTransition(s, ringing{Msg: m, CallID: b.callIDs[c]}, now); up.Ended {
			ups[c] = up
			delete(b.calls, c)
			delete(b.callIDs, c)
		}
	}
	b.callMu.Unlock()
	for c, up := range ups {
		if err := b.applyCall(ctx, c, up); err != nil {
			return err
		}
	}
	return nil
}

// acceptRing applies Signal-Desktop's checks before a group ring may ring:
// the group is known, not blocked, accepted, we and the ringer are members,
// announcement-only groups only ring for admins, and the ring was not
// already cancelled.
func (b *Backend) acceptRing(ctx context.Context, sig callSignal) (bool, error) {
	c, err := b.db.GetChat(ctx, sig.Chat)
	if errors.Is(err, core.ErrNotFound) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if c.Blocked || c.MessageRequest {
		return false, nil
	}
	if len(c.Members) > 0 && (!memberOf(c.Members, sig.Sender) || !memberOf(c.Members, sig.Self)) {
		return false, nil
	}
	if c.OnlyAdminsSend && len(c.Members) > 0 && !isAdmin(c, sig.Sender) {
		return false, nil
	}
	if sig.RingIntention == events.RingIntentionRing && sig.CallID != 0 {
		var cancelled map[string]time.Time
		if ok, _ := b.kv.get(ctx, "ring_cancelled", &cancelled); ok {
			if _, gone := cancelled[strconv.FormatUint(sig.CallID, 10)]; gone {
				return false, nil
			}
		}
	}
	return true, nil
}

// rememberCancelledRing records a cancelled ring ID so a ring arriving
// after its cancellation does not ring (kept for a day).
func (b *Backend) rememberCancelledRing(ctx context.Context, id uint64) {
	cancelled := map[string]time.Time{}
	_, _ = b.kv.get(ctx, "ring_cancelled", &cancelled)
	now := b.now()
	for k, at := range cancelled {
		if now.Sub(at) > 24*time.Hour {
			delete(cancelled, k)
		}
	}
	cancelled[strconv.FormatUint(id, 10)] = now
	_ = b.kv.set(ctx, "ring_cancelled", cancelled)
}

// callIDEntry maps a call ID to its call message (for call event syncs).
type callIDEntry struct {
	ID  string `json:"i"`
	Msg string `json:"m"`
}

// maxCallIDs bounds the remembered call IDs.
const maxCallIDs = 256

// rememberCallID records which message a call ID belongs to.
func (b *Backend) rememberCallID(ctx context.Context, callID uint64, id core.MessageID) {
	if callID == 0 {
		return
	}
	var list []callIDEntry
	_, _ = b.kv.get(ctx, "call_ids", &list)
	key := strconv.FormatUint(callID, 10)
	for _, e := range list {
		if e.ID == key {
			return
		}
	}
	list = append(list, callIDEntry{ID: key, Msg: id.String()})
	if len(list) > maxCallIDs {
		list = list[len(list)-maxCallIDs:]
	}
	_ = b.kv.set(ctx, "call_ids", list)
}

// lookupCallID returns the call message of a call ID.
func (b *Backend) lookupCallID(ctx context.Context, callID uint64) (core.MessageID, bool) {
	var list []callIDEntry
	if ok, _ := b.kv.get(ctx, "call_ids", &list); !ok {
		return core.MessageID{}, false
	}
	key := strconv.FormatUint(callID, 10)
	for _, e := range list {
		if e.ID == key {
			id, err := core.ParseMessageID(e.Msg)
			return id, err == nil
		}
	}
	return core.MessageID{}, false
}

// callIDFromEra is RingRTC's RingId::from_era_id as an unsigned call ID
// (Signal-Desktop callIdFromEra): a group call's era ID of 16 hex digits
// is the ID itself (0 maps to -1), anything else the little-endian first 8
// bytes of its SHA-256. A group ring's ring ID and the call ID of a group
// call's CallEvent sync are this value for the call's era.
func callIDFromEra(era string) uint64 {
	if len(era) == 16 {
		// Rust's u64::from_str_radix accepts one leading plus sign.
		if i, err := strconv.ParseUint(strings.TrimPrefix(era, "+"), 16, 64); err == nil {
			if i == 0 {
				return math.MaxUint64 // RingId(-1)
			}
			return i
		}
	}
	h := sha256.Sum256([]byte(era))
	return binary.LittleEndian.Uint64(h[:8])
}

// callMessageByID returns the call message remembered for callID in chat.
func (b *Backend) callMessageByID(ctx context.Context, chat core.ChatID, callID uint64) (*core.Message, bool) {
	if callID == 0 {
		return nil, false
	}
	id, ok := b.lookupCallID(ctx, callID)
	if !ok || id.Chat != chat {
		return nil, false
	}
	m, err := b.db.GetMessage(ctx, id)
	if err != nil || m.Call == nil {
		return nil, false
	}
	return &m, true
}

// recentGroupCall returns the call message of the group's last call if it
// was announced within groupCallMergeWindow.
func (b *Backend) recentGroupCall(ctx context.Context, chat core.ChatID) (*core.Message, bool) {
	var st groupCallState
	if ok, _ := b.kv.get(ctx, "group_call."+string(chat), &st); !ok || st.Msg == "" || b.now().Sub(st.At) > groupCallMergeWindow {
		return nil, false
	}
	id, err := core.ParseMessageID(st.Msg)
	if err != nil {
		return nil, false
	}
	m, err := b.db.GetMessage(ctx, id)
	if err != nil || m.Call == nil {
		return nil, false
	}
	return &m, true
}

// onGroupCallUpdate announces a group call when someone joins a call with
// a new era ("Alice started a group call"). Joins to a call already
// announced are ignored; a ring that arrived first shares its message.
func (b *Backend) onGroupCallUpdate(ctx context.Context, sig callSignal, era string) error {
	if !sig.Chat.IsGroup() {
		return nil
	}
	key := "group_call." + string(sig.Chat)
	var st groupCallState
	_, _ = b.kv.get(ctx, key, &st)
	now := b.now()
	if !groupCallIsNew(st, era, now) {
		return nil
	}
	var callID uint64
	if era != "" {
		callID = callIDFromEra(era)
		// A ring or call event sync for this call arrived first: adopt its
		// message (exact match by call ID).
		if m, ok := b.callMessageByID(ctx, sig.Chat, callID); ok {
			_ = b.kv.set(ctx, key, groupCallState{Era: era, Msg: m.ID.String(), At: now})
			text := groupCallStartedText(sig.Sender, sig.Self, b.names(ctx))
			if m.SystemText != text {
				if um, err := b.db.UpdateMessage(ctx, m.ID, func(m *core.Message) error { m.SystemText = text; return nil }); err == nil {
					b.emit(core.MessageUpdated{Message: um})
				}
			}
			return nil
		}
	}
	if st.Era == "" && st.Msg != "" && now.Sub(st.At) <= groupCallMergeWindow {
		// The ring for this call arrived before the update: adopt it.
		st.Era = era
		_ = b.kv.set(ctx, key, st)
		if id, err := core.ParseMessageID(st.Msg); err == nil {
			b.rememberCallID(ctx, callID, id)
			text := groupCallStartedText(sig.Sender, sig.Self, b.names(ctx))
			if m, err := b.db.UpdateMessage(ctx, id, func(m *core.Message) error { m.SystemText = text; return nil }); err == nil {
				b.emit(core.MessageUpdated{Message: m})
			}
		}
		return nil
	}
	m := newCallMessage(sig.Chat, sig.Sender, sig.Self, sig.TS, false, now.UTC())
	m.SystemText = groupCallStartedText(sig.Sender, sig.Self, b.names(ctx))
	if err := b.applyCall(ctx, sig.Chat, callUpdate{Message: m, New: true}); err != nil {
		return err
	}
	b.rememberCallID(ctx, callID, m.ID)
	return b.kv.set(ctx, key, groupCallState{Era: era, Msg: m.ID.String(), At: now})
}

// armRingTimeout ends a ringing call as missed once ringTimeout passes
// without a hangup (lost hangups would otherwise ring forever).
func (b *Backend) armRingTimeout(chat core.ChatID, id core.MessageID) {
	time.AfterFunc(b.ringTimeout, func() {
		b.goBackground(func() {
			b.handleMu.Lock()
			defer b.handleMu.Unlock()
			if b.ctx.Err() != nil {
				return
			}
			b.callMu.Lock()
			cur := b.calls[chat]
			if cur == nil || cur.ID != id {
				b.callMu.Unlock()
				return
			}
			delete(b.calls, chat)
			delete(b.callIDs, chat)
			b.callMu.Unlock()
			ctx := b.log.WithContext(b.ctx)
			if err := b.applyCall(ctx, chat, callUpdate{Message: endCall(cur, true, false), Ended: true}); err != nil {
				zerolog.Ctx(ctx).Debug().Err(err).Msg("Failed to end unanswered call")
			}
		})
	})
}

// applyCall persists a call transition and emits its events. Calls in
// muted chats and message requests are recorded but do not ring.
func (b *Backend) applyCall(ctx context.Context, chat core.ChatID, up callUpdate) error {
	if up.Message == nil {
		return nil
	}
	m := *up.Message
	cc := convCtx{Self: b.selfACI(), Chat: chat, Sender: m.ID.Author, ReceivedAt: m.ReceivedAt}
	c, err := b.ensureChat(ctx, cc, 0)
	if err != nil {
		return err
	}
	if up.New {
		if _, err := b.db.GetMessage(ctx, m.ID); err == nil {
			return nil
		}
	}
	var opts []store.PutOption
	if m.Outgoing || (m.Call != nil && m.Call.Group && !m.Call.Missed) {
		opts = append(opts, store.AsRead())
	}
	if _, err := b.db.PutMessage(ctx, m, opts...); err != nil {
		return err
	}
	_ = b.index.put(ctx, m.ID, 0, "", nil)
	entry := core.CallLogEntry{Chat: chat, Title: c.Title, Call: *m.Call, Unread: m.Call.Missed}
	if err := b.db.PutCall(ctx, entry); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	stored, err := b.db.GetMessage(ctx, m.ID)
	if err != nil {
		return err
	}
	silent := c.Blocked || c.MessageRequest || c.Muted(b.now())
	if up.New {
		b.emit(core.MessageAdded{Message: stored, Notify: (up.Ringing || m.Call.Missed) && !m.Outgoing && b.shouldNotify(c, stored)})
	} else {
		b.emit(core.MessageUpdated{Message: stored})
	}
	name := b.nameOf(ctx, m.ID.Author)
	switch {
	case up.Ringing && !silent:
		b.emit(core.IncomingCall{Chat: chat, From: m.ID.Author, Name: name, Video: m.Call.Video, Ringing: true})
	case up.Ended:
		b.emit(core.IncomingCall{Chat: chat, From: m.ID.Author, Name: name, Video: m.Call.Video, Ringing: false})
	}
	b.emitChat(ctx, chat)
	return nil
}

// --- call event syncs ------------------------------------------------------------

// callEventChat maps a call event's conversation ID: a 16-byte ACI for 1:1
// calls, a 32-byte group identifier for group calls. Ad-hoc (call link)
// calls are not supported.
func callEventChat(typ signalpb.SyncMessage_CallEvent_Type, conv []byte) (core.ChatID, bool) {
	switch typ {
	case signalpb.SyncMessage_CallEvent_AUDIO_CALL, signalpb.SyncMessage_CallEvent_VIDEO_CALL:
		if aci := aciFromBinaryOrString(conv, ""); aci != "" && len(conv) == 16 {
			return core.DirectChatID(aci), true
		}
	case signalpb.SyncMessage_CallEvent_GROUP_CALL:
		return groupChatIDFromBytes(conv)
	}
	return "", false
}

// callLogEventChat maps a call log event's conversation ID, which may be an
// ACI, a group ID or a call link room ID (ambiguous with group IDs; unknown
// groups are ignored by the caller).
func callLogEventChat(conv []byte) (core.ChatID, bool) {
	switch len(conv) {
	case 16:
		if aci := aciFromBinaryOrString(conv, ""); aci != "" {
			return core.DirectChatID(aci), true
		}
	case 32:
		return groupChatIDFromBytes(conv)
	}
	return "", false
}

// onCallEventSync applies a call accepted, declined or deleted on another
// of our devices. Calls we never saw (e.g. placed on the phone) are added
// to the call log.
func (b *Backend) onCallEventSync(ctx context.Context, e *events.CallEventSync) error {
	ce := e.Event
	if ce == nil {
		return nil
	}
	chat, ok := callEventChat(ce.GetType(), ce.GetConversationId())
	if !ok {
		return nil
	}
	self := b.selfACI()
	accepted := ce.GetEvent() == signalpb.SyncMessage_CallEvent_ACCEPTED
	declined := ce.GetEvent() == signalpb.SyncMessage_CallEvent_NOT_ACCEPTED

	// A call that rings here right now.
	b.callMu.Lock()
	cur := b.currentCall(chat)
	if cur.Msg != nil && (cur.CallID == 0 || cur.CallID == ce.GetCallId()) && (accepted || declined || ce.GetEvent() == signalpb.SyncMessage_CallEvent_DELETE) {
		delete(b.calls, chat)
		delete(b.callIDs, chat)
	} else {
		cur = ringing{}
	}
	b.callMu.Unlock()
	if cur.Msg != nil {
		if err := b.applyCall(ctx, chat, callUpdate{Message: endCall(cur.Msg, false, declined), Ended: true}); err != nil {
			return err
		}
	}

	id, known := b.lookupCallID(ctx, ce.GetCallId())
	if !known && cur.Msg != nil {
		id, known = cur.Msg.ID, true
	}
	switch ce.GetEvent() {
	case signalpb.SyncMessage_CallEvent_DELETE:
		if !known {
			return nil
		}
		m, err := b.db.GetMessage(ctx, id)
		if err == nil && m.Call != nil {
			_ = ignoreNotFound(b.db.DeleteCall(ctx, chat, m.Call.StartedAt))
		}
		return ignoreNotFound(b.deleteLocal(ctx, id))
	case signalpb.SyncMessage_CallEvent_ACCEPTED, signalpb.SyncMessage_CallEvent_NOT_ACCEPTED:
		if known {
			if cur.Msg != nil {
				return nil // handled above
			}
			m, err := b.db.GetMessage(ctx, id)
			if err != nil || m.Call == nil {
				return ignoreNotFound(err)
			}
			if !m.Call.Missed && m.Call.Declined == declined {
				return nil
			}
			return b.applyCall(ctx, chat, callUpdate{Message: endCall(&m, false, declined)})
		}
		ts := ce.GetTimestamp()
		if ts == 0 {
			ts = e.Timestamp
		}
		author := self
		if chat.IsGroup() {
			if m, ok := b.recentGroupCall(ctx, chat); ok {
				// The group call update already announced this call.
				b.rememberCallID(ctx, ce.GetCallId(), m.ID)
				return nil
			}
		}
		if ce.GetDirection() == signalpb.SyncMessage_CallEvent_INCOMING {
			if !chat.IsDirect() {
				return nil // a group call joined on the phone; the update announced it
			}
			author = chat.ACI()
		}
		m := newCallMessage(chat, author, self, ts, ce.GetType() == signalpb.SyncMessage_CallEvent_VIDEO_CALL, b.now().UTC())
		m.Call.Declined = declined
		b.rememberCallID(ctx, ce.GetCallId(), m.ID)
		return b.applyCall(ctx, chat, callUpdate{Message: m, New: true})
	}
	return nil
}

// onCallLogEventSync applies a call log clear or mark-read from another of
// our devices. Entries at or before the event's timestamp are affected.
func (b *Backend) onCallLogEventSync(ctx context.Context, e *events.CallLogEventSync) error {
	le := e.Event
	if le == nil {
		return nil
	}
	cutoff := msTime(le.GetTimestamp())
	if cutoff.IsZero() {
		cutoff = b.now()
	}
	var chat core.ChatID
	switch le.GetType() {
	case signalpb.SyncMessage_CallLogEvent_CLEAR_IN_CONVERSATION, signalpb.SyncMessage_CallLogEvent_MARKED_AS_READ_IN_CONVERSATION:
		c, ok := callLogEventChat(le.GetConversationId())
		if !ok {
			return nil
		}
		chat = c
	}
	entries, err := b.db.ListCalls(ctx, 0)
	if err != nil {
		return err
	}
	changed := map[core.ChatID]bool{}
	for _, en := range entries {
		if (chat != "" && en.Chat != chat) || en.Call.StartedAt.After(cutoff) {
			continue
		}
		switch le.GetType() {
		case signalpb.SyncMessage_CallLogEvent_CLEAR, signalpb.SyncMessage_CallLogEvent_CLEAR_IN_CONVERSATION:
			if err := ignoreNotFound(b.db.DeleteCall(ctx, en.Chat, en.Call.StartedAt)); err != nil {
				return err
			}
			changed[en.Chat] = true
		case signalpb.SyncMessage_CallLogEvent_MARKED_AS_READ, signalpb.SyncMessage_CallLogEvent_MARKED_AS_READ_IN_CONVERSATION:
			if !en.Unread {
				continue
			}
			en.Unread = false
			if err := b.db.PutCall(ctx, en); err != nil {
				return err
			}
			changed[en.Chat] = true
		}
	}
	for c := range changed {
		b.emitChat(ctx, c)
	}
	return nil
}
