package backend

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-signal/pkg/signalmeow/events"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/types"

	"warte/internal/core"
)

func TestCallTransition(t *testing.T) {
	now := time.Unix(1000, 0)
	offer := callSignal{Kind: events.CallKindOffer, Chat: aliceChat, Sender: aliceACI, Self: selfACI, TS: 50, CallID: 7}
	ringUp := callTransition(offer, ringing{}, now)
	if !ringUp.New || !ringUp.Ringing || ringUp.CallID != 7 || ringUp.Message.Kind != core.KindCall ||
		!ringUp.Message.Call.Incoming || ringUp.Message.Call.Video || ringUp.Message.ID != (core.MessageID{Chat: aliceChat, Author: aliceACI, Timestamp: 50}) {
		t.Fatalf("offer: %+v", ringUp)
	}
	cur := ringing{Msg: ringUp.Message, CallID: 7}
	video := offer
	video.Video, video.CallID, video.TS = true, 8, 51
	if up := callTransition(video, ringing{}, now); !up.Message.Call.Video {
		t.Error("video offer")
	}
	expired := offer
	expired.Expired = true
	if up := callTransition(expired, ringing{}, now); up.Ringing || !up.New || !up.Message.Call.Missed {
		t.Errorf("expired offer: %+v", up)
	}
	hangup := func(typ signalpb.CallMessage_Hangup_Type, from core.ACI, id uint64) callSignal {
		return callSignal{Kind: events.CallKindHangup, Chat: aliceChat, Sender: from, Self: selfACI, TS: 60, CallID: id, HangupType: typ}
	}
	tests := []struct {
		name                    string
		sig                     callSignal
		cur                     ringing
		ended, missed, declined bool
		ignored                 bool
	}{
		{name: "repeated offer", sig: offer, cur: cur, ignored: true},
		{name: "offer from own device", sig: callSignal{Kind: events.CallKindOffer, Chat: aliceChat, Sender: selfACI, Self: selfACI, TS: 70}, ignored: true},
		{name: "caller hangs up", sig: hangup(signalpb.CallMessage_Hangup_HANGUP_NORMAL, aliceACI, 7), cur: cur, ended: true, missed: true},
		{name: "accepted on another device", sig: hangup(signalpb.CallMessage_Hangup_HANGUP_ACCEPTED, aliceACI, 7), cur: cur, ended: true},
		{name: "declined on another device", sig: hangup(signalpb.CallMessage_Hangup_HANGUP_DECLINED, aliceACI, 7), cur: cur, ended: true, declined: true},
		{name: "busy on another device", sig: hangup(signalpb.CallMessage_Hangup_HANGUP_BUSY, aliceACI, 7), cur: cur, ended: true, missed: true},
		{name: "hangup of another call", sig: hangup(signalpb.CallMessage_Hangup_HANGUP_NORMAL, aliceACI, 99), cur: cur, ignored: true},
		{name: "hangup from a stranger", sig: hangup(signalpb.CallMessage_Hangup_HANGUP_NORMAL, bobACI, 7), cur: cur, ignored: true},
		{name: "hangup from own device", sig: hangup(signalpb.CallMessage_Hangup_HANGUP_NORMAL, selfACI, 0), cur: cur, ended: true},
		{name: "hangup without ringing call", sig: hangup(signalpb.CallMessage_Hangup_HANGUP_NORMAL, aliceACI, 7), ignored: true},
		{name: "answer from own device", sig: callSignal{Kind: events.CallKindAnswer, Chat: aliceChat, Sender: selfACI, Self: selfACI, CallID: 7}, cur: cur, ended: true},
		{name: "busy from own device", sig: callSignal{Kind: events.CallKindBusy, Chat: aliceChat, Sender: selfACI, Self: selfACI, CallID: 7}, cur: cur, ended: true, missed: true},
		{name: "answer from the peer", sig: callSignal{Kind: events.CallKindAnswer, Chat: aliceChat, Sender: aliceACI, Self: selfACI, CallID: 7}, cur: cur, ignored: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			up := callTransition(tt.sig, tt.cur, now)
			if tt.ignored {
				if up.Message != nil {
					t.Fatalf("not ignored: %+v", up)
				}
				return
			}
			if up.Ended != tt.ended || up.Message == nil || up.Message.Call.Missed != tt.missed || up.Message.Call.Declined != tt.declined {
				t.Fatalf("got %+v call %+v", up, up.Message.Call)
			}
			if up.Message.ID != cur.Msg.ID {
				t.Errorf("ended a different call: %v", up.Message.ID)
			}
		})
	}
	if cur.Msg.Call.Missed {
		t.Error("transition must not mutate the ringing call")
	}

	// Group rings.
	ring := callSignal{Kind: events.CallKindRingIntention, Chat: groupChat, Sender: aliceACI, Self: selfACI, TS: 80, CallID: 5, RingIntention: events.RingIntentionRing}
	gr := callTransition(ring, ringing{}, now)
	if !gr.Ringing || !gr.New || !gr.Message.Call.Group || gr.CallID != 5 {
		t.Fatalf("group ring: %+v", gr)
	}
	gcur := ringing{Msg: gr.Message, CallID: 5}
	cancel := ring
	cancel.RingIntention = events.RingIntentionCancelled
	if up := callTransition(cancel, gcur, now); !up.Ended || !up.Message.Call.Missed {
		t.Errorf("ring cancelled: %+v", up)
	}
	cancelByOther := cancel
	cancelByOther.Sender = bobACI
	if up := callTransition(cancelByOther, gcur, now); up.Message != nil {
		t.Error("only the ringer cancels")
	}
	for resp, want := range map[events.RingResponseType][2]bool{
		events.RingResponseAccepted: {false, false},
		events.RingResponseDeclined: {false, true},
		events.RingResponseBusy:     {true, false},
	} {
		sig := callSignal{Kind: events.CallKindRingResponse, Chat: groupChat, Sender: selfACI, Self: selfACI, CallID: 5, RingResponse: resp}
		up := callTransition(sig, gcur, now)
		if !up.Ended || up.Message.Call.Missed != want[0] || up.Message.Call.Declined != want[1] {
			t.Errorf("ring response %d: %+v", resp, up)
		}
		sig.Sender = bobACI
		if up := callTransition(sig, gcur, now); up.Message != nil {
			t.Errorf("ring response %d from another member must be ignored", resp)
		}
	}
	if up := callTransition(callSignal{Kind: events.CallKindOffer, Chat: groupChat, Sender: aliceACI, Self: selfACI, TS: 1}, ringing{}, now); up.Message != nil {
		t.Error("1:1 offer to a group")
	}
}

func TestCallSignalFromEvent(t *testing.T) {
	legacyRing := callSignalFromEvent(&events.Call{Info: events.MessageInfo{Sender: aliceUUID}, IsRinging: true}, aliceChat, selfACI, 5)
	if legacyRing.Kind != events.CallKindOffer || legacyRing.Sender != aliceACI {
		t.Errorf("legacy ring %+v", legacyRing)
	}
	if s := callSignalFromEvent(&events.Call{}, aliceChat, selfACI, 5); s.Kind != events.CallKindHangup {
		t.Errorf("legacy hangup %+v", s)
	}
	if s := callSignalFromEvent(&events.Call{IsRinging: true}, groupChat, selfACI, 5); s.Kind != events.CallKindGroupUpdate {
		t.Errorf("legacy group %+v", s)
	}
	v := callSignalFromEvent(&events.Call{Kind: events.CallKindOffer, IsVideo: true, CallID: 3}, aliceChat, selfACI, 5)
	if !v.Video || v.CallID != 3 {
		t.Errorf("video %+v", v)
	}
}

func TestGroupCallIsNew(t *testing.T) {
	now := time.Unix(10000, 0)
	tests := []struct {
		st   groupCallState
		era  string
		want bool
	}{
		{groupCallState{}, "a", true},
		{groupCallState{Era: "a", At: now.Add(-time.Hour)}, "a", false},
		{groupCallState{Era: "a", At: now}, "b", true},
		{groupCallState{Era: "", At: now.Add(-time.Minute)}, "a", true}, // a ring came first; the caller adopts it
		{groupCallState{At: now.Add(-time.Minute)}, "", false},
		{groupCallState{At: now.Add(-time.Hour)}, "", true},
	}
	for i, tt := range tests {
		if got := groupCallIsNew(tt.st, tt.era, now); got != tt.want {
			t.Errorf("%d: got %v", i, got)
		}
	}
}

func TestCallEventChat(t *testing.T) {
	if c, ok := callEventChat(signalpb.SyncMessage_CallEvent_VIDEO_CALL, aliceUUID[:]); !ok || c != aliceChat {
		t.Errorf("direct %v", c)
	}
	gid, _ := groupIDBytes(groupChat)
	if c, ok := callEventChat(signalpb.SyncMessage_CallEvent_GROUP_CALL, gid); !ok || c != groupChat {
		t.Errorf("group %v", c)
	}
	if _, ok := callEventChat(signalpb.SyncMessage_CallEvent_AD_HOC_CALL, gid); ok {
		t.Error("ad hoc calls are not supported")
	}
	if _, ok := callEventChat(signalpb.SyncMessage_CallEvent_AUDIO_CALL, gid); ok {
		t.Error("group id for a 1:1 call")
	}
	if c, ok := callLogEventChat(aliceUUID[:]); !ok || c != aliceChat {
		t.Errorf("log direct %v", c)
	}
	if c, ok := callLogEventChat(gid); !ok || c != groupChat {
		t.Errorf("log group %v", c)
	}
}

func TestVideoCallAcceptedElsewhere(t *testing.T) {
	b, _ := newTestBackend(t)
	call := func(c events.Call) {
		t.Helper()
		c.Info.Sender, c.Info.ChatID = aliceUUID, aliceUUID.String()
		mustHandle(t, b, &c)
	}
	call(events.Call{Kind: events.CallKindOffer, IsRinging: true, IsVideo: true, CallID: 77, Timestamp: 9000})
	ic := eventsOf[core.IncomingCall](drain(t, b))
	if len(ic) != 1 || !ic[0].Ringing || !ic[0].Video {
		t.Fatalf("ringing %+v", ic)
	}
	// A hangup for another call ID does not end it.
	call(events.Call{Kind: events.CallKindHangup, CallID: 78, Timestamp: 9050})
	if ic := eventsOf[core.IncomingCall](drain(t, b)); len(ic) != 0 {
		t.Errorf("foreign hangup ended the call: %+v", ic)
	}
	call(events.Call{Kind: events.CallKindHangup, CallID: 77, HangupType: signalpb.CallMessage_Hangup_HANGUP_ACCEPTED, HangupDeviceID: 1, Timestamp: 9100})
	if ic := eventsOf[core.IncomingCall](drain(t, b)); len(ic) != 1 || ic[0].Ringing || !ic[0].Video {
		t.Errorf("ended %+v", ic)
	}
	m := mustMessage(t, b, core.MessageID{Chat: aliceChat, Author: aliceACI, Timestamp: 9000})
	if m.Call == nil || m.Call.Missed || !m.Call.Video || !m.Call.Incoming {
		t.Errorf("answered call %+v", m.Call)
	}
}

func TestCallForAnotherDeviceIgnored(t *testing.T) {
	b, _ := newTestBackend(t)
	// Without a client the device ID is unknown; the event is processed.
	mustHandle(t, b, &events.Call{Info: events.MessageInfo{Sender: aliceUUID, ChatID: aliceUUID.String()}, Kind: events.CallKindOffer, IsRinging: true, CallID: 1, Timestamp: 100, DestinationDeviceID: 9})
	if ic := eventsOf[core.IncomingCall](drain(t, b)); len(ic) != 1 {
		t.Errorf("events %+v", ic)
	}
}

func TestExpiredOfferIsMissed(t *testing.T) {
	b, clock := newTestBackend(t)
	sent := uint64(clock.Now().Add(-10 * time.Minute).UnixMilli())
	mustHandle(t, b, &events.Call{Info: events.MessageInfo{Sender: aliceUUID, ChatID: aliceUUID.String(), ServerTimestamp: sent},
		Kind: events.CallKindOffer, IsRinging: true, CallID: 1, Timestamp: sent})
	evs := drain(t, b)
	if ic := eventsOf[core.IncomingCall](evs); len(ic) != 0 {
		t.Errorf("expired offer rang: %+v", ic)
	}
	if added := eventsOf[core.MessageAdded](evs); len(added) != 1 || !added[0].Message.Call.Missed || !added[0].Notify {
		t.Errorf("missed call %+v", added)
	}
}

func groupWithMembers(t *testing.T, b *Backend, members ...core.ACI) {
	t.Helper()
	c := core.Chat{ID: groupChat, Kind: core.ChatGroup, Title: "Team"}
	for _, m := range members {
		c.Members = append(c.Members, core.Member{ACI: m})
	}
	if err := b.db.UpsertChat(context.Background(), c); err != nil {
		t.Fatal(err)
	}
}

func TestGroupCalls(t *testing.T) {
	b, clock := newTestBackend(t)
	ctx := context.Background()
	groupWithMembers(t, b, selfACI, aliceACI, bobACI)
	gid := types.GroupIdentifier(groupChat.GroupID())
	update := func(sender uuid.UUID, ts uint64, era string) {
		t.Helper()
		mustHandle(t, b, &events.Call{Info: events.MessageInfo{Sender: sender, ChatID: string(gid)}, Kind: events.CallKindGroupUpdate,
			GroupID: gid, EraID: era, Timestamp: ts, IsRinging: true})
	}
	update(aliceUUID, 1000, "era1")
	evs := drain(t, b)
	added := eventsOf[core.MessageAdded](evs)
	if len(added) != 1 || added[0].Message.SystemText != "Alice Chen started a group call" || !added[0].Message.Call.Group || added[0].Notify {
		t.Fatalf("group call start %+v", added)
	}
	if ic := eventsOf[core.IncomingCall](evs); len(ic) != 0 {
		t.Errorf("group update must not ring: %+v", ic)
	}
	// Bob joins the same call; restart-safe because the era is persisted.
	update(bobUUID, 1100, "era1")
	if added := eventsOf[core.MessageAdded](drain(t, b)); len(added) != 0 {
		t.Errorf("join announced again: %+v", added)
	}
	// Alice rings the group: the call message is shared and it rings.
	ring := func(sender uuid.UUID, intent events.RingIntentionType, id uint64) {
		t.Helper()
		mustHandle(t, b, &events.Call{Info: events.MessageInfo{Sender: sender, ChatID: string(gid)}, Kind: events.CallKindRingIntention,
			GroupID: gid, CallID: id, RingIntention: intent, IsRinging: intent == events.RingIntentionRing, Timestamp: 1200})
	}
	ring(aliceUUID, events.RingIntentionRing, 42)
	evs = drain(t, b)
	if ic := eventsOf[core.IncomingCall](evs); len(ic) != 1 || !ic[0].Ringing || ic[0].Chat != groupChat {
		t.Fatalf("group ring %+v", ic)
	}
	if added := eventsOf[core.MessageAdded](evs); len(added) != 0 {
		t.Errorf("ring created a second call message: %+v", added)
	}
	// Accepted on the phone (ring response from our own device).
	mustHandle(t, b, &events.Call{Info: events.MessageInfo{Sender: selfUUID, ChatID: string(gid)}, Kind: events.CallKindRingResponse,
		GroupID: gid, CallID: 42, RingResponse: events.RingResponseAccepted, Timestamp: 1300})
	if ic := eventsOf[core.IncomingCall](drain(t, b)); len(ic) != 1 || ic[0].Ringing {
		t.Errorf("ring accepted elsewhere %+v", ic)
	}
	m := mustMessage(t, b, core.MessageID{Chat: groupChat, Author: aliceACI, Timestamp: 1000})
	if m.Call.Missed || !m.Call.Incoming {
		t.Errorf("group call %+v", m.Call)
	}

	// A cancelled ring arriving before its ring does not ring.
	clock.Advance(time.Hour)
	ring(bobUUID, events.RingIntentionCancelled, 43)
	ring(bobUUID, events.RingIntentionRing, 43)
	if ic := eventsOf[core.IncomingCall](drain(t, b)); len(ic) != 0 {
		t.Errorf("cancelled ring rang: %+v", ic)
	}
	// Rings from non-members, and in unknown groups, are ignored.
	carol := uuid.UUID{9}
	ring(carol, events.RingIntentionRing, 44)
	if ic := eventsOf[core.IncomingCall](drain(t, b)); len(ic) != 0 {
		t.Errorf("non-member ring: %+v", ic)
	}
	// A ring cancelled by the ringer is missed.
	ring(bobUUID, events.RingIntentionRing, 45)
	ring(bobUUID, events.RingIntentionCancelled, 45)
	evs = drain(t, b)
	if ic := eventsOf[core.IncomingCall](evs); len(ic) != 2 || !ic[0].Ringing || ic[1].Ringing {
		t.Errorf("ring then cancel %+v", ic)
	}
	calls, err := b.CallLog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	missed := 0
	for _, c := range calls {
		if c.Call.Missed {
			missed++
		}
	}
	if missed != 1 {
		t.Errorf("call log %+v", calls)
	}
}

func TestCallEventSync(t *testing.T) {
	b, clock := newTestBackend(t)
	ctx := context.Background()
	mustHandle(t, b, &events.Call{Info: events.MessageInfo{Sender: aliceUUID, ChatID: aliceUUID.String()}, Kind: events.CallKindOffer, IsRinging: true, CallID: 500, Timestamp: 2000})
	drain(t, b)
	ce := func(typ signalpb.SyncMessage_CallEvent_Type, dir signalpb.SyncMessage_CallEvent_Direction, ev signalpb.SyncMessage_CallEvent_Event, id, ts uint64, conv []byte) {
		t.Helper()
		mustHandle(t, b, &events.CallEventSync{Timestamp: ts, Event: &signalpb.SyncMessage_CallEvent{
			ConversationId: conv, CallId: proto.Uint64(id), Timestamp: proto.Uint64(ts), Type: typ.Enum(), Direction: dir.Enum(), Event: ev.Enum(),
		}})
	}
	ce(signalpb.SyncMessage_CallEvent_AUDIO_CALL, signalpb.SyncMessage_CallEvent_INCOMING, signalpb.SyncMessage_CallEvent_ACCEPTED, 500, 2000, aliceUUID[:])
	if ic := eventsOf[core.IncomingCall](drain(t, b)); len(ic) != 1 || ic[0].Ringing {
		t.Errorf("accepted on phone %+v", ic)
	}
	if m := mustMessage(t, b, core.MessageID{Chat: aliceChat, Author: aliceACI, Timestamp: 2000}); m.Call.Missed {
		t.Error("accepted call missed")
	}
	// A call placed on the phone shows up as outgoing.
	ts := uint64(clock.Now().UnixMilli())
	ce(signalpb.SyncMessage_CallEvent_VIDEO_CALL, signalpb.SyncMessage_CallEvent_OUTGOING, signalpb.SyncMessage_CallEvent_ACCEPTED, 501, ts, bobUUID[:])
	out := mustMessage(t, b, core.MessageID{Chat: core.DirectChatID(bobACI), Author: selfACI, Timestamp: ts})
	if !out.Outgoing || out.Call == nil || out.Call.Incoming || !out.Call.Video {
		t.Errorf("outgoing call %+v", out.Call)
	}
	// Repeated sync is idempotent.
	ce(signalpb.SyncMessage_CallEvent_VIDEO_CALL, signalpb.SyncMessage_CallEvent_OUTGOING, signalpb.SyncMessage_CallEvent_ACCEPTED, 501, ts, bobUUID[:])
	if added := eventsOf[core.MessageAdded](drain(t, b)); len(added) != 1 {
		t.Errorf("outgoing call added %d times", len(added))
	}
	// Deleted on the phone.
	ce(signalpb.SyncMessage_CallEvent_VIDEO_CALL, signalpb.SyncMessage_CallEvent_OUTGOING, signalpb.SyncMessage_CallEvent_DELETE, 501, ts, bobUUID[:])
	if _, err := b.Message(ctx, out.ID); err == nil {
		t.Error("deleted call still there")
	}
	calls, _ := b.CallLog(ctx)
	if len(calls) != 1 || calls[0].Chat != aliceChat {
		t.Errorf("call log after delete %+v", calls)
	}
}

func TestCallLogEventSync(t *testing.T) {
	b, _ := newTestBackend(t)
	ctx := context.Background()
	for i, who := range []uuid.UUID{aliceUUID, bobUUID} {
		mustHandle(t, b, &events.Call{Info: events.MessageInfo{Sender: who, ChatID: who.String()}, Kind: events.CallKindOffer, IsRinging: true, CallID: uint64(i + 1), Timestamp: uint64(3000 + i)})
		mustHandle(t, b, &events.Call{Info: events.MessageInfo{Sender: who, ChatID: who.String()}, Kind: events.CallKindHangup, CallID: uint64(i + 1), Timestamp: uint64(3100 + i)})
	}
	log := func(typ signalpb.SyncMessage_CallLogEvent_Type, ts uint64, conv []byte) {
		t.Helper()
		mustHandle(t, b, &events.CallLogEventSync{Event: &signalpb.SyncMessage_CallLogEvent{Type: typ.Enum(), Timestamp: proto.Uint64(ts), ConversationId: conv}})
	}
	log(signalpb.SyncMessage_CallLogEvent_MARKED_AS_READ_IN_CONVERSATION, 5000, aliceUUID[:])
	calls, _ := b.CallLog(ctx)
	for _, c := range calls {
		if c.Unread != (c.Chat != aliceChat) {
			t.Errorf("mark read in conversation: %+v", c)
		}
	}
	log(signalpb.SyncMessage_CallLogEvent_CLEAR, 3000, nil) // only calls up to ts 3000
	if calls, _ := b.CallLog(ctx); len(calls) != 1 || calls[0].Chat != core.DirectChatID(bobACI) {
		t.Errorf("clear before 3000: %+v", calls)
	}
	log(signalpb.SyncMessage_CallLogEvent_MARKED_AS_READ, 5000, nil)
	if calls, _ := b.CallLog(ctx); len(calls) != 1 || calls[0].Unread {
		t.Errorf("mark all read: %+v", calls)
	}
	log(signalpb.SyncMessage_CallLogEvent_CLEAR, 5000, nil)
	if calls, _ := b.CallLog(ctx); len(calls) != 0 {
		t.Errorf("clear all: %+v", calls)
	}
}

func TestOwnGroupCallNotDuplicated(t *testing.T) {
	b, clock := newTestBackend(t)
	groupWithMembers(t, b, selfACI, aliceACI)
	gid := types.GroupIdentifier(groupChat.GroupID())
	ts := uint64(clock.Now().UnixMilli())
	// We started a group call on the phone: the sync transcript of the
	// group call update, then the call event sync.
	mustHandle(t, b, &events.Call{Info: events.MessageInfo{Sender: selfUUID, ChatID: string(gid)}, Kind: events.CallKindGroupUpdate, GroupID: gid, EraID: "e", Timestamp: ts})
	raw, _ := groupIDBytes(groupChat)
	mustHandle(t, b, &events.CallEventSync{Event: &signalpb.SyncMessage_CallEvent{ConversationId: raw, CallId: proto.Uint64(9), Timestamp: proto.Uint64(ts),
		Type: signalpb.SyncMessage_CallEvent_GROUP_CALL.Enum(), Direction: signalpb.SyncMessage_CallEvent_OUTGOING.Enum(), Event: signalpb.SyncMessage_CallEvent_ACCEPTED.Enum()}})
	added := eventsOf[core.MessageAdded](drain(t, b))
	if len(added) != 1 || added[0].Message.SystemText != "You started a group call" || !added[0].Message.Outgoing {
		t.Errorf("own group call %+v", added)
	}
}

// RingRTC RingId::from_era_id (group_call.rs): 16 hex digits are the ID
// (0 maps to -1), anything else the little-endian head of its SHA-256.
func TestCallIDFromEra(t *testing.T) {
	cases := map[string]uint64{
		"0123456789abcdef": 0x0123456789abcdef,
		"FFFFFFFFFFFFFFFE": 0xFFFFFFFFFFFFFFFE,
		"0000000000000000": math.MaxUint64,
		"+123456789abcdef": 0x0123456789abcdef,
		"abc":              16919744041952114874, // sha256 LE, computed independently
		"not-hex-era-id!!": 10406401516748163652,
		"":                 1449310910991872227,
	}
	for era, want := range cases {
		if got := callIDFromEra(era); got != want {
			t.Errorf("callIDFromEra(%q) = %d, want %d", era, got, want)
		}
	}
}

// The ring ID of a group ring is callIDFromEra of the call's era, so a ring
// and a call event sync find the call announced by a group call update
// exactly, even long after the merge window.
func TestGroupCallLinkedByEra(t *testing.T) {
	b, clock := newTestBackend(t)
	groupWithMembers(t, b, selfACI, aliceACI)
	gid := types.GroupIdentifier(groupChat.GroupID())
	const era = "00000000000004d2"
	ts := uint64(clock.Now().UnixMilli())
	mustHandle(t, b, &events.Call{Info: events.MessageInfo{Sender: aliceUUID, ChatID: string(gid)}, Kind: events.CallKindGroupUpdate,
		GroupID: gid, EraID: era, Timestamp: ts})
	drain(t, b)
	clock.Advance(groupCallMergeWindow + time.Minute)
	mustHandle(t, b, &events.Call{Info: events.MessageInfo{Sender: aliceUUID, ChatID: string(gid)}, Kind: events.CallKindRingIntention,
		GroupID: gid, CallID: 1234, RingIntention: events.RingIntentionRing, IsRinging: true, Timestamp: ts + 1})
	evs := drain(t, b)
	if added := eventsOf[core.MessageAdded](evs); len(added) != 0 {
		t.Errorf("ring for an announced call created a new message: %+v", added)
	}
	if ic := eventsOf[core.IncomingCall](evs); len(ic) != 1 || !ic[0].Ringing {
		t.Errorf("ring %+v", ic)
	}
	raw, _ := groupIDBytes(groupChat)
	mustHandle(t, b, &events.CallEventSync{Event: &signalpb.SyncMessage_CallEvent{ConversationId: raw, CallId: proto.Uint64(1234), Timestamp: proto.Uint64(ts + 5),
		Type: signalpb.SyncMessage_CallEvent_GROUP_CALL.Enum(), Direction: signalpb.SyncMessage_CallEvent_INCOMING.Enum(), Event: signalpb.SyncMessage_CallEvent_ACCEPTED.Enum()}})
	evs = drain(t, b)
	if added := eventsOf[core.MessageAdded](evs); len(added) != 0 {
		t.Errorf("call event sync created a new message: %+v", added)
	}
	m := mustMessage(t, b, core.MessageID{Chat: groupChat, Author: aliceACI, Timestamp: ts})
	if m.Call.Missed || m.SystemText != "Alice Chen started a group call" {
		t.Errorf("call %+v %q", m.Call, m.SystemText)
	}

	// Ring first, update long after: the update adopts the ring's message.
	const era2 = "0000000000001000"
	clock.Advance(time.Hour)
	ts2 := uint64(clock.Now().UnixMilli())
	mustHandle(t, b, &events.Call{Info: events.MessageInfo{Sender: aliceUUID, ChatID: string(gid)}, Kind: events.CallKindRingIntention,
		GroupID: gid, CallID: 0x1000, RingIntention: events.RingIntentionRing, IsRinging: true, Timestamp: ts2})
	drain(t, b)
	clock.Advance(groupCallMergeWindow + time.Minute)
	mustHandle(t, b, &events.Call{Info: events.MessageInfo{Sender: aliceUUID, ChatID: string(gid)}, Kind: events.CallKindGroupUpdate,
		GroupID: gid, EraID: era2, Timestamp: ts2 + 10})
	evs = drain(t, b)
	if added := eventsOf[core.MessageAdded](evs); len(added) != 0 {
		t.Errorf("update for a rung call created a new message: %+v", added)
	}
	if m := mustMessage(t, b, core.MessageID{Chat: groupChat, Author: aliceACI, Timestamp: ts2}); m.SystemText != "Alice Chen started a group call" {
		t.Errorf("adopted ring message text %q", m.SystemText)
	}
}
