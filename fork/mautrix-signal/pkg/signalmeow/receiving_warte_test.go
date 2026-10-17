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

// warte: tests for the receive-side additions in receiving_warte.go.

import (
	"bytes"
	"encoding/base64"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-signal/pkg/signalmeow/events"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/types"
)

var (
	testACI1 = uuid.MustParse("22222222-2222-4222-8222-222222222222")
	testACI2 = uuid.MustParse("33333333-3333-4333-8333-333333333333")
	testGID  = bytes.Repeat([]byte{7}, 32)
)

// ringFieldsBytes encodes RingRTC's RingIntention/RingResponse by hand,
// following ringrtc/protobuf/signaling.proto.
func ringFieldsBytes(groupID []byte, typ int, ringID int64) []byte {
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.BytesType)
	b = protowire.AppendBytes(b, groupID)
	b = protowire.AppendTag(b, 2, protowire.VarintType)
	b = protowire.AppendVarint(b, uint64(typ))
	b = protowire.AppendTag(b, 3, protowire.Fixed64Type) // sfixed64
	b = protowire.AppendFixed64(b, uint64(ringID))
	return b
}

func ringRTC(field protowire.Number, inner []byte) []byte {
	var b []byte
	// An unrelated group_call_message first, which must be skipped.
	b = protowire.AppendTag(b, 1, protowire.BytesType)
	b = protowire.AppendBytes(b, []byte{0x0a, 0x01, 0x00})
	b = protowire.AppendTag(b, field, protowire.BytesType)
	return protowire.AppendBytes(b, inner)
}

func TestParseRingRTCMessage(t *testing.T) {
	m, err := parseRingRTCMessage(ringRTC(2, ringFieldsBytes(testGID, 1, -42)))
	if err != nil {
		t.Fatal(err)
	}
	if !m.HasIntention || m.HasResponse || m.Intention.Type != 1 || m.Intention.RingID != -42 || !bytes.Equal(m.Intention.GroupID, testGID) {
		t.Errorf("intention: %+v", m)
	}
	m, err = parseRingRTCMessage(ringRTC(3, ringFieldsBytes(testGID, 2, 7)))
	if err != nil || !m.HasResponse || m.Response.Type != 2 || m.Response.RingID != 7 {
		t.Errorf("response: %+v %v", m, err)
	}
	if _, err := parseRingRTCMessage([]byte{0x12, 0x05, 0x01}); err == nil {
		t.Error("truncated message must fail")
	}
	if m, err := parseRingRTCMessage(nil); err != nil || m.HasIntention || m.HasResponse {
		t.Errorf("empty: %+v %v", m, err)
	}
}

func TestCallFromCallMessage(t *testing.T) {
	gidStr := types.GroupIdentifier(base64.StdEncoding.EncodeToString(testGID))
	tests := []struct {
		name string
		cm   *signalpb.CallMessage
		ok   bool
		want events.Call
	}{
		{"audio offer", &signalpb.CallMessage{Offer: &signalpb.CallMessage_Offer{Id: proto.Uint64(9), Type: signalpb.CallMessage_Offer_OFFER_AUDIO_CALL.Enum()}},
			true, events.Call{Kind: events.CallKindOffer, CallID: 9, IsRinging: true}},
		{"video offer", &signalpb.CallMessage{Offer: &signalpb.CallMessage_Offer{Id: proto.Uint64(9), Type: signalpb.CallMessage_Offer_OFFER_VIDEO_CALL.Enum()}},
			true, events.Call{Kind: events.CallKindOffer, CallID: 9, IsRinging: true, IsVideo: true}},
		{"offer without type is audio", &signalpb.CallMessage{Offer: &signalpb.CallMessage_Offer{Id: proto.Uint64(1)}},
			true, events.Call{Kind: events.CallKindOffer, CallID: 1, IsRinging: true}},
		{"hangup accepted elsewhere", &signalpb.CallMessage{Hangup: &signalpb.CallMessage_Hangup{Id: proto.Uint64(9), Type: signalpb.CallMessage_Hangup_HANGUP_ACCEPTED.Enum(), DeviceId: proto.Uint32(2)}, DestinationDeviceId: proto.Uint32(3)},
			true, events.Call{Kind: events.CallKindHangup, CallID: 9, HangupType: signalpb.CallMessage_Hangup_HANGUP_ACCEPTED, HangupDeviceID: 2, DestinationDeviceID: 3}},
		{"busy", &signalpb.CallMessage{Busy: &signalpb.CallMessage_Busy{Id: proto.Uint64(4)}},
			true, events.Call{Kind: events.CallKindBusy, CallID: 4}},
		{"answer", &signalpb.CallMessage{Answer: &signalpb.CallMessage_Answer{Id: proto.Uint64(5)}},
			true, events.Call{Kind: events.CallKindAnswer, CallID: 5}},
		{"ice only", &signalpb.CallMessage{IceUpdate: []*signalpb.CallMessage_IceUpdate{{Id: proto.Uint64(5)}}}, false, events.Call{}},
		{"group ring", &signalpb.CallMessage{Opaque: &signalpb.CallMessage_Opaque{Data: ringRTC(2, ringFieldsBytes(testGID, 0, -5))}},
			true, events.Call{Kind: events.CallKindRingIntention, IsRinging: true, GroupID: gidStr, CallID: uint64(0xfffffffffffffffb), RingIntention: events.RingIntentionRing}},
		{"ring cancelled", &signalpb.CallMessage{Opaque: &signalpb.CallMessage_Opaque{Data: ringRTC(2, ringFieldsBytes(testGID, 1, 5))}},
			true, events.Call{Kind: events.CallKindRingIntention, GroupID: gidStr, CallID: 5, RingIntention: events.RingIntentionCancelled}},
		{"ring accepted on own device", &signalpb.CallMessage{Opaque: &signalpb.CallMessage_Opaque{Data: ringRTC(3, ringFieldsBytes(testGID, 1, 5))}},
			true, events.Call{Kind: events.CallKindRingResponse, GroupID: gidStr, CallID: 5, RingResponse: events.RingResponseAccepted}},
		{"ringing ack ignored", &signalpb.CallMessage{Opaque: &signalpb.CallMessage_Opaque{Data: ringRTC(3, ringFieldsBytes(testGID, 0, 5))}}, false, events.Call{}},
		{"ring with bad group id", &signalpb.CallMessage{Opaque: &signalpb.CallMessage_Opaque{Data: ringRTC(2, ringFieldsBytes(testGID[:16], 0, 5))}}, false, events.Call{}},
		{"group call media keys only", &signalpb.CallMessage{Opaque: &signalpb.CallMessage_Opaque{Data: []byte{0x0a, 0x00}}}, false, events.Call{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := callFromCallMessage(tt.cm)
			if ok != tt.ok {
				t.Fatalf("ok = %v", ok)
			}
			if ok && !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestBlockedFromSync(t *testing.T) {
	g2 := bytes.Repeat([]byte{8}, 32)
	b := blockedFromSync(&signalpb.SyncMessage_Blocked{
		Numbers:       []string{"+15550001"},
		Acis:          []string{testACI1.String(), "not-a-uuid"},
		AcisBinary:    [][]byte{testACI1[:], {1, 2}},
		GroupIds:      [][]byte{testGID, {1}},
		BlockedE164S:  []*signalpb.SyncMessage_Blocked_BlockedE164{{E164: proto.String("+15550001")}, {E164: proto.String("+15550002")}},
		BlockedAcis:   []*signalpb.SyncMessage_Blocked_BlockedAci{{AciBinary: testACI2[:]}},
		BlockedGroups: []*signalpb.SyncMessage_Blocked_BlockedGroup{{GroupId: testGID}, {GroupId: g2}},
	})
	if len(b.ACIs) != 2 || b.ACIs[0] != testACI1 || b.ACIs[1] != testACI2 {
		t.Errorf("acis %v", b.ACIs)
	}
	if len(b.E164s) != 2 {
		t.Errorf("e164s %v", b.E164s)
	}
	if len(b.GroupIDs) != 2 || string(b.GroupIDs[0]) != base64.StdEncoding.EncodeToString(testGID) {
		t.Errorf("groups %v", b.GroupIDs)
	}
	if empty := blockedFromSync(&signalpb.SyncMessage_Blocked{}); len(empty.ACIs)+len(empty.GroupIDs)+len(empty.E164s) != 0 {
		t.Error("empty list")
	}
}

func TestSyncConversions(t *testing.T) {
	cfg := configurationFromSync(&signalpb.SyncMessage_Configuration{ReadReceipts: proto.Bool(false), LinkPreviews: proto.Bool(true)})
	if cfg.ReadReceipts == nil || *cfg.ReadReceipts || cfg.TypingIndicators != nil || cfg.LinkPreviews == nil || !*cfg.LinkPreviews {
		t.Errorf("configuration %+v", cfg)
	}
	key := bytes.Repeat([]byte{1}, 32)
	ops := stickerOpsFromSync([]*signalpb.SyncMessage_StickerPackOperation{
		{PackId: bytes.Repeat([]byte{2}, 16), PackKey: key}, // type absent: INSTALL
		{PackId: bytes.Repeat([]byte{3}, 16), Type: signalpb.SyncMessage_StickerPackOperation_REMOVE.Enum()},
		{PackId: []byte{1}},
	})
	if len(ops) != 2 || !ops[0].Install || ops[1].Install || !bytes.Equal(ops[0].PackKey, key) {
		t.Errorf("sticker ops %+v", ops)
	}
	viewed := viewedFromSync([]*signalpb.SyncMessage_Viewed{
		{SenderAci: proto.String(testACI1.String()), Timestamp: proto.Uint64(10)},
		{SenderAciBinary: testACI2[:], Timestamp: proto.Uint64(11)},
		{SenderAciBinary: testACI2[:]},
		{Timestamp: proto.Uint64(12)},
	})
	if len(viewed) != 2 || viewed[0] != (events.ViewedMessage{Sender: testACI1, Timestamp: 10}) || viewed[1].Sender != testACI2 {
		t.Errorf("viewed %+v", viewed)
	}
}

func TestStoryRecipients(t *testing.T) {
	list := "00000000-0000-0000-0000-000000000000"
	rs := storyRecipients(&signalpb.SyncMessage_Sent{StoryMessageRecipients: []*signalpb.SyncMessage_Sent_StoryMessageRecipient{
		{DestinationServiceId: proto.String(testACI1.String()), DistributionListIds: []string{list}, IsAllowedToReply: proto.Bool(true)},
		{DestinationServiceIdBinary: testACI2[:], DistributionListIds: []string{list}},
		{DistributionListIds: []string{list}}, // no destination
	}})
	if len(rs) != 2 || rs[0].ServiceID != testACI1.String() || !rs[0].AllowedToReply || rs[1].ServiceID != testACI2.String() || rs[1].AllowedToReply {
		t.Errorf("recipients %+v", rs)
	}
}

// warte: Signal-Desktop MessageReceiver only accepts stories, group story
// replies and deletes in story-flagged envelopes.
func TestStoryEnvelopeContentAllowed(t *testing.T) {
	group := &signalpb.GroupContextV2{MasterKey: bytes.Repeat([]byte{1}, 32)}
	sc := &signalpb.DataMessage_StoryContext{SentTimestamp: proto.Uint64(1)}
	cases := []struct {
		name string
		c    *signalpb.Content
		want bool
	}{
		{"story", &signalpb.Content{Content: &signalpb.Content_StoryMessage{StoryMessage: &signalpb.StoryMessage{}}}, true},
		{"group story reply", WrapDataMessage(&signalpb.DataMessage{StoryContext: sc, GroupV2: group}), true},
		{"delete", WrapDataMessage(&signalpb.DataMessage{Delete: &signalpb.DataMessage_Delete{TargetSentTimestamp: proto.Uint64(1)}}), true},
		{"1:1 story reply", WrapDataMessage(&signalpb.DataMessage{StoryContext: sc}), false},
		{"plain message", WrapDataMessage(&signalpb.DataMessage{Body: proto.String("hi")}), false},
		{"call", &signalpb.Content{Content: &signalpb.Content_CallMessage{CallMessage: &signalpb.CallMessage{}}}, false},
		{"edit", &signalpb.Content{Content: &signalpb.Content_EditMessage{EditMessage: &signalpb.EditMessage{}}}, false},
		{"receipt", &signalpb.Content{Content: &signalpb.Content_ReceiptMessage{ReceiptMessage: &signalpb.ReceiptMessage{}}}, false},
		{"sync", WrapSyncMessage(&signalpb.SyncMessage{}), false},
		{"skdm only", &signalpb.Content{SenderKeyDistributionMessage: []byte{1}}, false},
	}
	for _, c := range cases {
		if got := storyEnvelopeContentAllowed(c.c); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// warte: like Signal-Desktop, a transcript with isRecipientUpdate and
// storyMessageRecipients is a recipient update even if it carries the story.
func TestSyncSentStoryRecipientUpdateWithContent(t *testing.T) {
	var got []events.SignalEvent
	cli := &Client{EventHandler: func(e events.SignalEvent) bool { got = append(got, e); return true }}
	sent := &signalpb.SyncMessage_Sent{
		Timestamp:         proto.Uint64(42),
		IsRecipientUpdate: proto.Bool(true),
		StoryMessage:      &signalpb.StoryMessage{},
		StoryMessageRecipients: []*signalpb.SyncMessage_Sent_StoryMessageRecipient{
			{DestinationServiceId: proto.String(testACI1.String())},
		},
	}
	isStory, ok := cli.handleSyncSentStory(t.Context(), sent, &signalpb.Envelope{})
	if !isStory || !ok || len(got) != 1 {
		t.Fatalf("isStory=%v ok=%v events=%v", isStory, ok, got)
	}
	upd, isUpdate := got[0].(*events.StoryRecipientUpdate)
	if !isUpdate || upd.Timestamp != 42 || len(upd.Recipients) != 1 || upd.Recipients[0].ServiceID != testACI1.String() {
		t.Errorf("event %#v", got[0])
	}
}
