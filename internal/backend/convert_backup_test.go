package backend

import (
	"reflect"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/backuppb"

	"warte/internal/core"
)

func testBackupCtx(chat core.ChatID) backupCtx {
	return backupCtx{
		Self: selfACI, Chat: chat, Peer: aliceACI,
		ACIOf: func(id uint64) core.ACI {
			switch id {
			case 1:
				return selfACI
			case 2:
				return aliceACI
			case 3:
				return bobACI
			}
			return ""
		},
		Name: testNames,
	}
}

func filePointer(cdnKey string, digest, hash []byte) *backuppb.FilePointer {
	li := &backuppb.FilePointer_LocatorInfo{Key: []byte("k"), Size: 42}
	if cdnKey != "" {
		li.TransitCdnKey = proto.String(cdnKey)
		li.TransitCdnNumber = proto.Uint32(3)
	}
	if digest != nil {
		li.IntegrityCheck = &backuppb.FilePointer_LocatorInfo_EncryptedDigest{EncryptedDigest: digest}
	} else if hash != nil {
		li.IntegrityCheck = &backuppb.FilePointer_LocatorInfo_PlaintextHash{PlaintextHash: hash}
	}
	return &backuppb.FilePointer{ContentType: proto.String("image/jpeg"), LocatorInfo: li, Width: proto.Uint32(10), Height: proto.Uint32(20)}
}

func TestConvertBackupStandardMessage(t *testing.T) {
	bc := testBackupCtx(aliceChat)
	ci := &backuppb.ChatItem{
		AuthorId: 2, DateSent: 1000, ExpiresInMs: proto.Uint64(60000), ExpireStartDate: proto.Uint64(1500),
		DirectionalDetails: &backuppb.ChatItem_Incoming{Incoming: &backuppb.ChatItem_IncomingMessageDetails{
			DateReceived: 1100, DateServerSent: proto.Uint64(1050), Read: true}},
		Item: &backuppb.ChatItem_StandardMessage{StandardMessage: &backuppb.StandardMessage{
			Text: &backuppb.Text{Body: "hello ￼", BodyRanges: []*backuppb.BodyRange{
				{Start: 0, Length: 5, AssociatedValue: &backuppb.BodyRange_Style_{Style: backuppb.BodyRange_BOLD}},
				{Start: 6, Length: 1, AssociatedValue: &backuppb.BodyRange_MentionAci{MentionAci: selfUUID[:]}},
			}},
			Attachments: []*backuppb.MessageAttachment{
				{Pointer: filePointer("cdn1", []byte{9}, nil), Flag: backuppb.MessageAttachment_VOICE_MESSAGE},
				{Pointer: filePointer("", nil, []byte{7})},
				{Pointer: filePointer("cdn3", nil, []byte{8})},
			},
			Quote: &backuppb.Quote{TargetSentTimestamp: proto.Uint64(900), AuthorId: 1, Text: &backuppb.Text{Body: "earlier"}},
			Reactions: []*backuppb.Reaction{
				{Emoji: "👍", AuthorId: 1, SentTimestamp: 1200},
				{Emoji: "😂", AuthorId: 3, SentTimestamp: 1150},
				{Emoji: "x", AuthorId: 99},
			},
		}},
		Revisions: []*backuppb.ChatItem{{DateSent: 950, Item: &backuppb.ChatItem_StandardMessage{StandardMessage: &backuppb.StandardMessage{Text: &backuppb.Text{Body: "helo"}}}}},
	}
	it, ok := convertBackupItem(bc, ci)
	if !ok {
		t.Fatal("not converted")
	}
	m := it.Msg
	if m.ID != (core.MessageID{Chat: aliceChat, Author: aliceACI, Timestamp: 1000}) || m.Outgoing || !it.Read {
		t.Errorf("identity %+v read=%v", m.ID, it.Read)
	}
	if m.Body != "hello ￼" || len(m.Ranges) != 2 || !m.MentionsSelf {
		t.Errorf("text %q %+v", m.Body, m.Ranges)
	}
	if !m.ReceivedAt.Equal(time.UnixMilli(1100)) || m.ServerTimestamp != 1050 {
		t.Errorf("times %v %d", m.ReceivedAt, m.ServerTimestamp)
	}
	if m.ExpiresIn != time.Minute || !m.ExpireStartedAt.Equal(time.UnixMilli(1500)) {
		t.Errorf("expiry %v %v", m.ExpiresIn, m.ExpireStartedAt)
	}
	if len(m.Attachments) != 3 || !m.Attachments[0].VoiceNote || m.Attachments[0].Failed || !m.Attachments[1].Failed || m.Attachments[2].Failed {
		t.Errorf("attachments %+v", m.Attachments)
	}
	if len(it.Atts) != 3 || it.Atts[2].PlaintextHash == nil || it.Atts[0].PlaintextHash != nil {
		t.Errorf("plaintext hashes %+v", it.Atts)
	}
	if m.Quote == nil || m.Quote.ID != (core.MessageID{Chat: aliceChat, Author: selfACI, Timestamp: 900}) || m.Quote.Text != "earlier" {
		t.Errorf("quote %+v", m.Quote)
	}
	wantReactions := []core.Reaction{
		{Emoji: "😂", From: bobACI, At: time.UnixMilli(1150).UTC()},
		{Emoji: "👍", From: selfACI, At: time.UnixMilli(1200).UTC()},
	}
	if !reflect.DeepEqual(m.Reactions, wantReactions) {
		t.Errorf("reactions %+v", m.Reactions)
	}
	if !m.Edited || len(m.EditHistory) != 1 || m.EditHistory[0].Body != "helo" {
		t.Errorf("revisions %+v", m.EditHistory)
	}
}

func TestConvertBackupOutgoingStatus(t *testing.T) {
	bc := testBackupCtx(groupChat)
	ci := &backuppb.ChatItem{
		AuthorId: 1, DateSent: 2000,
		DirectionalDetails: &backuppb.ChatItem_Outgoing{Outgoing: &backuppb.ChatItem_OutgoingMessageDetails{
			DateReceived: 2000,
			SendStatus: []*backuppb.SendStatus{
				{RecipientId: 2, DeliveryStatus: &backuppb.SendStatus_Read_{Read: &backuppb.SendStatus_Read{}}},
				{RecipientId: 3, DeliveryStatus: &backuppb.SendStatus_Failed_{Failed: &backuppb.SendStatus_Failed{}}},
			}}},
		Item: &backuppb.ChatItem_StandardMessage{StandardMessage: &backuppb.StandardMessage{Text: &backuppb.Text{Body: "hi all"}}},
	}
	it, ok := convertBackupItem(bc, ci)
	if !ok || !it.Msg.Outgoing || it.Msg.ID.Author != selfACI || !it.Read {
		t.Fatalf("outgoing %+v", it.Msg)
	}
	if it.Msg.Status != core.StatusRead || it.Msg.PerRecipient[bobACI] != core.StatusFailed || it.Msg.PerRecipient[aliceACI] != core.StatusRead {
		t.Errorf("status %v %v", it.Msg.Status, it.Msg.PerRecipient)
	}
}

func TestBackupSendStatus(t *testing.T) {
	bc := testBackupCtx(aliceChat)
	st, per := backupSendStatus(bc, nil)
	if st != core.StatusSent || per != nil {
		t.Error("no statuses = sent")
	}
	st, per = backupSendStatus(bc, []*backuppb.SendStatus{{RecipientId: 2, DeliveryStatus: &backuppb.SendStatus_Failed_{}}})
	if st != core.StatusFailed || per != nil {
		t.Errorf("all failed: %v %v (direct chats keep no per-recipient map)", st, per)
	}
	st, _ = backupSendStatus(bc, []*backuppb.SendStatus{{RecipientId: 2}})
	if st != core.StatusPending {
		t.Errorf("unset = pending, got %v", st)
	}
	st, _ = backupSendStatus(bc, []*backuppb.SendStatus{{RecipientId: 2, DeliveryStatus: &backuppb.SendStatus_Viewed_{}}, {RecipientId: 3, DeliveryStatus: &backuppb.SendStatus_Delivered_{}}})
	if st != core.StatusViewed {
		t.Errorf("best status, got %v", st)
	}
}

func TestConvertBackupItemKinds(t *testing.T) {
	bc := testBackupCtx(aliceChat)
	in := &backuppb.ChatItem_Incoming{Incoming: &backuppb.ChatItem_IncomingMessageDetails{}}
	tests := []struct {
		name  string
		item  *backuppb.ChatItem
		ok    bool
		check func(t *testing.T, it backupItem)
	}{
		{"remote deleted", &backuppb.ChatItem{AuthorId: 2, DateSent: 1, DirectionalDetails: in, Item: &backuppb.ChatItem_RemoteDeletedMessage{RemoteDeletedMessage: &backuppb.RemoteDeletedMessage{}}}, true,
			func(t *testing.T, it backupItem) {
				if !it.Msg.RemoteDeleted || it.Read {
					t.Errorf("%+v read=%v", it.Msg, it.Read)
				}
			}},
		{"timer", &backuppb.ChatItem{AuthorId: 2, DateSent: 1, DirectionalDetails: &backuppb.ChatItem_Directionless{Directionless: &backuppb.ChatItem_DirectionlessMessageDetails{}},
			Item: &backuppb.ChatItem_UpdateMessage{UpdateMessage: &backuppb.ChatUpdateMessage{Update: &backuppb.ChatUpdateMessage_ExpirationTimerChange{
				ExpirationTimerChange: &backuppb.ExpirationTimerChatUpdate{ExpiresInMs: 3600000}}}}}, true,
			func(t *testing.T, it backupItem) {
				if it.Msg.Kind != core.KindSystem || it.Msg.SystemText != "Alice set the disappearing message timer to 1 hour." || !it.Read {
					t.Errorf("%+v", it.Msg)
				}
			}},
		{"missed call", &backuppb.ChatItem{AuthorId: 2, DateSent: 5, DirectionalDetails: in,
			Item: &backuppb.ChatItem_UpdateMessage{UpdateMessage: &backuppb.ChatUpdateMessage{Update: &backuppb.ChatUpdateMessage_IndividualCall{
				IndividualCall: &backuppb.IndividualCall{Type: backuppb.IndividualCall_VIDEO_CALL, Direction: backuppb.IndividualCall_INCOMING, State: backuppb.IndividualCall_MISSED, StartedCallTimestamp: 5}}}}}, true,
			func(t *testing.T, it backupItem) {
				c := it.Msg.Call
				if it.Msg.Kind != core.KindCall || c == nil || !c.Video || !c.Missed || !c.Incoming || !c.StartedAt.Equal(time.UnixMilli(5)) {
					t.Errorf("%+v", it.Msg)
				}
			}},
		{"group call", &backuppb.ChatItem{AuthorId: 2, DateSent: 5, DirectionalDetails: in,
			Item: &backuppb.ChatItem_UpdateMessage{UpdateMessage: &backuppb.ChatUpdateMessage{Update: &backuppb.ChatUpdateMessage_GroupCall{
				GroupCall: &backuppb.GroupCall{State: backuppb.GroupCall_JOINED, StartedCallTimestamp: 5, EndedCallTimestamp: proto.Uint64(60005)}}}}}, true,
			func(t *testing.T, it backupItem) {
				if c := it.Msg.Call; c == nil || !c.Group || c.Duration != time.Minute {
					t.Errorf("%+v", it.Msg.Call)
				}
			}},
		{"other update skipped", &backuppb.ChatItem{AuthorId: 2, DateSent: 5, Item: &backuppb.ChatItem_UpdateMessage{UpdateMessage: &backuppb.ChatUpdateMessage{
			Update: &backuppb.ChatUpdateMessage_SimpleUpdate{SimpleUpdate: &backuppb.SimpleChatUpdate{}}}}}, false, nil},
		{"poll", &backuppb.ChatItem{AuthorId: 2, DateSent: 7, DirectionalDetails: in, Item: &backuppb.ChatItem_Poll{Poll: &backuppb.Poll{
			Question: "Q", AllowMultiple: true, HasEnded: true,
			Options: []*backuppb.Poll_PollOption{{Option: "a", Votes: []*backuppb.Poll_PollOption_PollVote{{VoterId: 1}, {VoterId: 3}}}, {Option: "b"}}}}}, true,
			func(t *testing.T, it backupItem) {
				p := it.Msg.Poll
				if p == nil || !p.Ended || !p.AllowMultiple || len(p.Options) != 2 || !reflect.DeepEqual(p.Options[0].Voters, []core.ACI{selfACI, bobACI}) {
					t.Errorf("%+v", p)
				}
			}},
		{"view once", &backuppb.ChatItem{AuthorId: 2, DateSent: 8, DirectionalDetails: in, Item: &backuppb.ChatItem_ViewOnceMessage{ViewOnceMessage: &backuppb.ViewOnceMessage{
			Attachment: &backuppb.MessageAttachment{Pointer: filePointer("v", []byte{1}, nil)}}}}, true,
			func(t *testing.T, it backupItem) {
				if !it.Msg.ViewOnce || len(it.Msg.Attachments) != 1 {
					t.Errorf("%+v", it.Msg)
				}
			}},
		{"sticker", &backuppb.ChatItem{AuthorId: 2, DateSent: 9, DirectionalDetails: in, Item: &backuppb.ChatItem_StickerMessage{StickerMessage: &backuppb.StickerMessage{
			Sticker: &backuppb.Sticker{PackId: []byte{1}, PackKey: []byte{2}, StickerId: 4, Emoji: proto.String("🙂"), Data: filePointer("s", []byte{1}, nil)}}}}, true,
			func(t *testing.T, it backupItem) {
				s := it.Msg.Sticker
				if s == nil || s.StickerID != 4 || s.Emoji != "🙂" || !s.Attachment.Borderless {
					t.Errorf("%+v", s)
				}
			}},
		{"contact card", &backuppb.ChatItem{AuthorId: 2, DateSent: 10, DirectionalDetails: in, Item: &backuppb.ChatItem_ContactMessage{ContactMessage: &backuppb.ContactMessage{
			Contact: &backuppb.ContactAttachment{Name: &backuppb.ContactAttachment_Name{GivenName: "Ada"}, Organization: "X"}}}}, true,
			func(t *testing.T, it backupItem) {
				if c := it.Msg.Contact; c == nil || c.Name != "Ada" || c.Org != "X" {
					t.Errorf("%+v", c)
				}
			}},
		{"story emoji reply", &backuppb.ChatItem{AuthorId: 1, DateSent: 11, DirectionalDetails: &backuppb.ChatItem_Outgoing{Outgoing: &backuppb.ChatItem_OutgoingMessageDetails{}},
			Item: &backuppb.ChatItem_DirectStoryReplyMessage{DirectStoryReplyMessage: &backuppb.DirectStoryReplyMessage{
				Reply: &backuppb.DirectStoryReplyMessage_Emoji{Emoji: "🔥"}}}}, true,
			func(t *testing.T, it backupItem) {
				m := it.Msg
				if m.Kind != core.KindStoryReply || m.StoryContext == nil || m.StoryContext.Emoji != "🔥" || m.StoryContext.Author != aliceACI || m.Body != "" {
					t.Errorf("%+v", m)
				}
			}},
		{"payment", &backuppb.ChatItem{AuthorId: 2, DateSent: 12, DirectionalDetails: in, Item: &backuppb.ChatItem_PaymentNotification{PaymentNotification: &backuppb.PaymentNotification{Note: proto.String("pizza")}}}, true,
			func(t *testing.T, it backupItem) {
				if it.Msg.PaymentNotice == "" {
					t.Error("payment notice")
				}
			}},
		{"unknown author", &backuppb.ChatItem{AuthorId: 42, DateSent: 13, DirectionalDetails: in, Item: &backuppb.ChatItem_StandardMessage{StandardMessage: &backuppb.StandardMessage{}}}, false, nil},
		{"no date", &backuppb.ChatItem{AuthorId: 2, Item: &backuppb.ChatItem_StandardMessage{StandardMessage: &backuppb.StandardMessage{}}}, false, nil},
		{"no item", &backuppb.ChatItem{AuthorId: 2, DateSent: 14}, false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			it, ok := convertBackupItem(bc, tt.item)
			if ok != tt.ok {
				t.Fatalf("ok = %v", ok)
			}
			if ok && tt.check != nil {
				tt.check(t, it)
			}
		})
	}
}

func TestBackupChatState(t *testing.T) {
	c := core.Chat{}
	backupChatState(&c, &backuppb.Chat{Archived: true, MarkedUnread: true, PinnedOrder: proto.Uint32(2), MuteUntilMs: proto.Uint64(1<<63 - 1), ExpirationTimerMs: proto.Uint64(60000)})
	if !c.Archived || !c.MarkedUnread || !c.Pinned || c.PinOrder != 2 || !c.MutedUntil.Equal(foreverMuted) || c.ExpireTimer != time.Minute {
		t.Errorf("%+v", c)
	}
}

func TestBackupChatID(t *testing.T) {
	if id, peer, ok := backupChatID(&backuppb.Recipient{Destination: &backuppb.Recipient_Self{Self: &backuppb.Self{}}}, selfACI); !ok || id != core.DirectChatID(selfACI) || peer != selfACI {
		t.Error("self")
	}
	if id, peer, ok := backupChatID(&backuppb.Recipient{Destination: &backuppb.Recipient_Contact{Contact: &backuppb.Contact{Aci: aliceUUID[:]}}}, selfACI); !ok || id != aliceChat || peer != aliceACI {
		t.Error("contact")
	}
	if _, _, ok := backupChatID(&backuppb.Recipient{Destination: &backuppb.Recipient_Contact{Contact: &backuppb.Contact{}}}, selfACI); ok {
		t.Error("contact without ACI")
	}
	if id, _, ok := backupChatID(&backuppb.Recipient{Destination: &backuppb.Recipient_Group{Group: &backuppb.Group{MasterKey: make([]byte, 32)}}}, selfACI); !ok || !id.IsGroup() {
		t.Error("group")
	}
	if _, _, ok := backupChatID(nil, selfACI); ok {
		t.Error("nil recipient")
	}
}
