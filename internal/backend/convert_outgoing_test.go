package backend

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"

	"warte/internal/core"
)

func TestValidateOutgoing(t *testing.T) {
	att := core.OutgoingAttachment{FileName: "a.png", Data: []byte{1}}
	poll := &core.Poll{Question: "Q?", Options: []core.PollOption{{Text: "a"}, {Text: "b"}}}
	tests := []struct {
		name    string
		out     core.Outgoing
		wantErr bool
	}{
		{"text", core.Outgoing{Body: "hi"}, false},
		{"empty", core.Outgoing{}, true},
		{"blank", core.Outgoing{Body: "  \n"}, true},
		{"attachment", core.Outgoing{Attachments: []core.OutgoingAttachment{att}}, false},
		{"caption", core.Outgoing{Body: "look", Attachments: []core.OutgoingAttachment{att}}, false},
		{"empty attachment", core.Outgoing{Attachments: []core.OutgoingAttachment{{FileName: "x"}}}, true},
		{"sticker", core.Outgoing{Sticker: &core.Sticker{PackID: "x"}}, false},
		{"sticker and text", core.Outgoing{Body: "x", Sticker: &core.Sticker{}}, true},
		{"poll", core.Outgoing{Poll: poll}, false},
		{"poll and text", core.Outgoing{Body: "x", Poll: poll}, true},
		{"poll one option", core.Outgoing{Poll: &core.Poll{Question: "Q", Options: []core.PollOption{{Text: "a"}}}}, true},
		{"poll no question", core.Outgoing{Poll: &core.Poll{Options: []core.PollOption{{Text: "a"}, {Text: "b"}}}}, true},
		{"poll blank option", core.Outgoing{Poll: &core.Poll{Question: "Q", Options: []core.PollOption{{Text: "a"}, {Text: " "}}}}, true},
		{"poll long question", core.Outgoing{Poll: &core.Poll{Question: strings.Repeat("x", 101), Options: []core.PollOption{{Text: "a"}, {Text: "b"}}}}, true},
		{"poll too many", core.Outgoing{Poll: &core.Poll{Question: "Q", Options: make([]core.PollOption, 11)}}, true},
		{"view once", core.Outgoing{ViewOnce: true, Attachments: []core.OutgoingAttachment{att}}, false},
		{"view once text", core.Outgoing{ViewOnce: true, Body: "x"}, true},
		{"view once caption", core.Outgoing{ViewOnce: true, Body: "x", Attachments: []core.OutgoingAttachment{att}}, true},
		{"view once two", core.Outgoing{ViewOnce: true, Attachments: []core.OutgoingAttachment{att, att}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateOutgoing(tt.out); (err != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
	if !errors.Is(validateOutgoing(core.Outgoing{}), errEmptyMessage) {
		t.Error("empty message error")
	}
}

func TestSplitLongBody(t *testing.T) {
	short := strings.Repeat("a", inlineBodyLimit)
	if got, long := splitLongBody(short); long || got != short {
		t.Error("body at the limit must stay inline")
	}
	body := strings.Repeat("a", inlineBodyLimit-1) + "é" + strings.Repeat("b", 100)
	got, long := splitLongBody(body)
	if !long || len(got) != inlineBodyLimit-1 || !strings.HasPrefix(body, got) {
		t.Errorf("cut %d bytes, long=%v", len(got), long)
	}
}

func TestTrimRanges(t *testing.T) {
	in := []core.BodyRange{
		{Start: 0, Length: 2, Style: core.StyleBold},
		{Start: 2, Length: 10, Style: core.StyleItalic},
		{Start: 3, Length: 1, Style: core.StyleMention, MentionACI: aliceACI},
		{Start: 9, Length: 1, Style: core.StyleBold},
		{Start: 0, Length: 0, Style: core.StyleBold},
	}
	got := trimRanges("abc￼", in)
	want := []core.BodyRange{
		{Start: 0, Length: 2, Style: core.StyleBold},
		{Start: 2, Length: 2, Style: core.StyleItalic},
		{Start: 3, Length: 1, Style: core.StyleMention, MentionACI: aliceACI},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
	if got := trimRanges("ab", []core.BodyRange{{Start: 1, Length: 3, Style: core.StyleMention}}); got != nil {
		t.Errorf("cut mention must be dropped: %+v", got)
	}
}

// TestBuildDataMessageRoundTrip checks that what we send converts back to
// what we meant, through the same code that handles incoming messages.
func TestBuildDataMessageRoundTrip(t *testing.T) {
	quoted := core.Message{
		ID:          core.MessageID{Chat: groupChat, Author: aliceACI, Timestamp: 10},
		Body:        "quoted *text*",
		Ranges:      []core.BodyRange{{Start: 7, Length: 6, Style: core.StyleBold}},
		Attachments: []core.Attachment{{ContentType: "image/png", FileName: "q.png"}},
	}
	att := ptrAtt("up", "image/jpeg", 100)
	spec := sendSpec{
		Timestamp:    1000,
		Body:         "hi ￼ see https://example.com",
		Ranges:       []core.BodyRange{{Start: 3, Length: 1, Style: core.StyleMention, MentionACI: bobACI}, {Start: 0, Length: 2, Style: core.StyleItalic}},
		Attachments:  []*signalpb.AttachmentPointer{att},
		Quote:        &quoted,
		Preview:      &core.LinkPreview{URL: "https://example.com", Title: "Ex", Description: "D", Date: time.UnixMilli(5000)},
		PreviewImage: ptrAtt("pv", "image/png", 5),
		ExpireTimer:  time.Hour,
		TimerVersion: 3,
		ProfileKey:   make([]byte, 32),
	}
	dm := buildDataMessage(spec)
	if dm.GetExpireTimer() != 3600 || dm.GetExpireTimerVersion() != 3 || len(dm.GetProfileKey()) != 32 {
		t.Errorf("timer/profile key: %v", dm)
	}
	if dm.GetRequiredProtocolVersion() != uint32(signalpb.DataMessage_MENTIONS) {
		t.Errorf("mentions need protocol version, got %d", dm.GetRequiredProtocolVersion())
	}
	if q := dm.GetQuote(); q.GetAuthorAci() != string(aliceACI) || len(q.GetAuthorAciBinary()) != 16 || q.GetId() != 10 ||
		len(q.GetAttachments()) != 1 || q.GetAttachments()[0].GetFileName() != "q.png" {
		t.Errorf("quote %v", q)
	}
	m := convertDataMessage(convCtx{Self: selfACI, Chat: groupChat, Sender: selfACI}, dm).Msg
	if m.Body != spec.Body || m.ID.Timestamp != 1000 || !m.Outgoing || m.ExpiresIn != time.Hour {
		t.Errorf("round trip message %+v", m)
	}
	wantRanges := []core.BodyRange{spec.Ranges[1], spec.Ranges[0]}
	if !reflect.DeepEqual(m.Ranges, wantRanges) {
		t.Errorf("ranges %+v", m.Ranges)
	}
	if m.Quote == nil || m.Quote.ID != quoted.ID || m.Quote.Text != quoted.Body || !reflect.DeepEqual(m.Quote.Ranges, quoted.Ranges) {
		t.Errorf("quote %+v", m.Quote)
	}
	if m.Preview == nil || m.Preview.Title != "Ex" || m.Preview.Image == nil || !m.Preview.Date.Equal(time.UnixMilli(5000)) {
		t.Errorf("preview %+v", m.Preview)
	}
	if len(m.Attachments) != 1 || m.Attachments[0].ContentType != "image/jpeg" {
		t.Errorf("attachments %+v", m.Attachments)
	}
}

func TestBuildDataMessageVariants(t *testing.T) {
	t.Run("preview needs url in body", func(t *testing.T) {
		dm := buildDataMessage(sendSpec{Timestamp: 1, Body: "no link", Preview: &core.LinkPreview{URL: "https://x"}})
		if len(dm.GetPreview()) != 0 {
			t.Error("preview without its URL in the body must be dropped")
		}
	})
	t.Run("view once strips text", func(t *testing.T) {
		dm := buildDataMessage(sendSpec{Timestamp: 1, Body: "x", ViewOnce: true, Attachments: []*signalpb.AttachmentPointer{ptrAtt("a", "image/png", 1)}})
		if !dm.GetIsViewOnce() || dm.Body != nil || dm.GetRequiredProtocolVersion() < uint32(signalpb.DataMessage_VIEW_ONCE_VIDEO) {
			t.Errorf("view once %v", dm)
		}
	})
	t.Run("poll", func(t *testing.T) {
		dm := buildDataMessage(sendSpec{Timestamp: 1, Poll: &core.Poll{Question: "Q", AllowMultiple: true, Options: []core.PollOption{{Text: "a"}, {Text: "b"}}}})
		pc := dm.GetPollCreate()
		if pc.GetQuestion() != "Q" || !pc.GetAllowMultiple() || !reflect.DeepEqual(pc.GetOptions(), []string{"a", "b"}) ||
			dm.GetRequiredProtocolVersion() != uint32(signalpb.DataMessage_POLLS) {
			t.Errorf("poll %v", dm)
		}
		back := convertDataMessage(convCtx{Self: selfACI, Chat: aliceChat, Sender: selfACI}, dm).Msg
		if back.Poll == nil || len(back.Poll.Options) != 2 {
			t.Errorf("poll round trip %+v", back.Poll)
		}
	})
	t.Run("sticker", func(t *testing.T) {
		st := &core.Sticker{PackID: "30313233343536373839616263646566", PackKey: strings.Repeat("ab", 32), StickerID: 3, Emoji: "😀"}
		dm := buildDataMessage(sendSpec{Timestamp: 1, Sticker: st, StickerData: ptrAtt("s", "image/webp", 1)})
		back := convertDataMessage(convCtx{Self: selfACI, Chat: aliceChat, Sender: selfACI}, dm).Msg
		if back.Sticker == nil || back.Sticker.PackID != st.PackID || back.Sticker.PackKey != st.PackKey || back.Sticker.StickerID != 3 || back.Sticker.Emoji != "😀" {
			t.Errorf("sticker round trip %+v", back.Sticker)
		}
	})
	t.Run("story reply", func(t *testing.T) {
		dm := buildDataMessage(sendSpec{Timestamp: 1, Body: "wow", StoryContext: &core.StoryRef{Author: aliceACI, Timestamp: 77}})
		back := convertDataMessage(convCtx{Self: selfACI, Chat: aliceChat, Sender: selfACI}, dm).Msg
		if back.Kind != core.KindStoryReply || back.StoryContext.Timestamp != 77 || back.StoryContext.Author != aliceACI {
			t.Errorf("story reply %+v", back)
		}
	})
	t.Run("poll quote", func(t *testing.T) {
		q := quoteToSignal(&core.Message{ID: core.MessageID{Author: aliceACI, Timestamp: 4}, Poll: &core.Poll{Question: "Lunch?"}}, nil)
		if q.GetType() != signalpb.DataMessage_Quote_POLL || q.GetText() != "Lunch?" {
			t.Errorf("poll quote %v", q)
		}
		q = quoteToSignal(&core.Message{ID: core.MessageID{Author: aliceACI, Timestamp: 4}, GiftBadge: true}, nil)
		if q.GetType() != signalpb.DataMessage_Quote_GIFT_BADGE {
			t.Error("gift quote type")
		}
		q = quoteToSignal(&core.Message{ID: core.MessageID{Author: aliceACI, Timestamp: 4}, Sticker: &core.Sticker{Attachment: core.Attachment{ContentType: "image/webp"}}}, nil)
		if len(q.GetAttachments()) != 1 || q.GetAttachments()[0].GetContentType() != "image/webp" {
			t.Error("sticker quote attachment")
		}
	})
}

func TestActionBuilders(t *testing.T) {
	target := core.MessageID{Chat: aliceChat, Author: aliceACI, Timestamp: 55}
	r, err := buildReaction(9, target, "❤️", false)
	if err != nil {
		t.Fatal(err)
	}
	cc := convCtx{Self: selfACI, Chat: aliceChat, Sender: selfACI}
	if classifyDataMessage(r) != dmReaction {
		t.Error("reaction classification")
	}
	rt, err := reactionFromSignal(r.GetReaction())
	if err != nil || rt != (reactionTarget{Author: aliceACI, Timestamp: 55, Emoji: "❤️"}) {
		t.Errorf("reaction round trip %+v %v", rt, err)
	}
	if _, err := buildReaction(1, core.MessageID{Author: "bad"}, "x", false); err == nil {
		t.Error("bad author must fail")
	}
	if d := buildDelete(10, 55); classifyDataMessage(d) != dmDelete || d.GetDelete().GetTargetSentTimestamp() != 55 {
		t.Error("delete")
	}
	tu := buildTimerUpdate(11, 8*time.Hour, 2, nil)
	if classifyDataMessage(tu) != dmTimerUpdate || tu.GetExpireTimer() != 8*3600 || tu.GetExpireTimerVersion() != 2 {
		t.Errorf("timer update %v", tu)
	}
	if pk := buildProfileKeyUpdate(12, make([]byte, 32)); classifyDataMessage(pk) != dmEmpty || len(pk.GetProfileKey()) != 32 {
		t.Error("profile key update")
	}
	pv, err := buildPollVote(13, target, []int{0, 2}, 4)
	if err != nil || classifyDataMessage(pv) != dmPollVote || pv.GetPollVote().GetVoteCount() != 4 ||
		!reflect.DeepEqual(pv.GetPollVote().GetOptionIndexes(), []uint32{0, 2}) ||
		aciFromBinaryOrString(pv.GetPollVote().GetTargetAuthorAciBinary(), "") != aliceACI {
		t.Errorf("poll vote %v %v", pv, err)
	}
	if pt := buildPollTerminate(14, 55); classifyDataMessage(pt) != dmPollTerminate || pt.GetPollTerminate().GetTargetSentTimestamp() != 55 {
		t.Error("poll terminate")
	}
	pin, err := buildPin(15, target, true, time.Hour)
	if err != nil || classifyDataMessage(pin) != dmPin || pinDuration(pin.GetPinMessage()) != time.Hour {
		t.Errorf("pin %v", pin)
	}
	forever, _ := buildPin(15, target, true, 0)
	if !forever.GetPinMessage().GetPinDurationForever() {
		t.Error("pin forever")
	}
	unpin, _ := buildPin(16, target, false, 0)
	if classifyDataMessage(unpin) != dmUnpin {
		t.Error("unpin")
	}
	em := buildEditMessage(55, buildDataMessage(sendSpec{Timestamp: 17, Body: "fixed"}))
	if em.GetTargetSentTimestamp() != 55 || convertDataMessage(cc, em.GetDataMessage()).Msg.Body != "fixed" {
		t.Error("edit message")
	}
}

func TestConversationIdentifiers(t *testing.T) {
	for _, chat := range []core.ChatID{aliceChat, groupChat} {
		ci, err := conversationID(chat)
		if err != nil {
			t.Fatal(err)
		}
		back, ok := conversationChat(ci)
		if !ok || back != chat {
			t.Errorf("conversation round trip %q -> %q", chat, back)
		}
	}
	if _, err := conversationID("x:nope"); err == nil {
		t.Error("bad chat must fail")
	}
	if _, ok := conversationChat(&signalpb.ConversationIdentifier{Identifier: &signalpb.ConversationIdentifier_ThreadE164{ThreadE164: "+1"}}); ok {
		t.Error("e164 conversations are unsupported")
	}
	if c, ok := conversationChat(&signalpb.ConversationIdentifier{Identifier: &signalpb.ConversationIdentifier_ThreadServiceIdBinary{ThreadServiceIdBinary: aliceUUID[:]}}); !ok || c != aliceChat {
		t.Error("binary service id")
	}
	id := core.MessageID{Chat: groupChat, Author: bobACI, Timestamp: 99}
	am, err := addressable(id)
	if err != nil {
		t.Fatal(err)
	}
	if back, ok := addressableID(groupChat, am); !ok || back != id {
		t.Errorf("addressable round trip %+v", back)
	}
	if _, ok := addressableID(groupChat, &signalpb.AddressableMessage{Author: &signalpb.AddressableMessage_AuthorE164{AuthorE164: "+1"}, SentTimestamp: proto.Uint64(1)}); ok {
		t.Error("e164 authors are unsupported")
	}
}

func TestSyncBuilders(t *testing.T) {
	ids := []core.MessageID{{Chat: aliceChat, Author: aliceACI, Timestamp: 1}, {Chat: aliceChat, Author: selfACI, Timestamp: 2}}
	sm, err := syncDeleteMessages(aliceChat, ids)
	if err != nil {
		t.Fatal(err)
	}
	md := sm.GetDeleteForMe().GetMessageDeletes()
	if len(md) != 1 || len(md[0].GetMessages()) != 2 {
		t.Fatalf("delete for me %v", sm)
	}
	if c, _ := conversationChat(md[0].GetConversation()); c != aliceChat {
		t.Error("conversation")
	}
	full, err := syncDeleteConversation(groupChat, ids)
	if err != nil || len(full.GetDeleteForMe().GetConversationDeletes()) != 1 || !full.GetDeleteForMe().GetConversationDeletes()[0].GetIsFullDelete() {
		t.Errorf("conversation delete %v", full)
	}
	local, _ := syncDeleteConversation(groupChat, nil)
	if len(local.GetDeleteForMe().GetLocalOnlyConversationDeletes()) != 1 {
		t.Error("empty chats delete locally only")
	}
	read := syncRead(append(ids, core.MessageID{Author: "bad"}))
	if len(read.GetRead()) != 2 || read.GetRead()[0].GetTimestamp() != 1 || read.GetRead()[0].GetSenderAci() != string(aliceACI) {
		t.Errorf("read sync %v", read)
	}
	vo, err := syncViewOnceOpen(ids[0])
	if err != nil || vo.GetViewOnceOpen().GetTimestamp() != 1 {
		t.Error("view once open")
	}
	mrr, err := syncMessageRequestResponse(groupChat, signalpb.SyncMessage_MessageRequestResponse_ACCEPT)
	if err != nil || len(mrr.GetMessageRequestResponse().GetGroupId()) != 32 {
		t.Error("mrr group")
	}
	mrr, err = syncMessageRequestResponse(aliceChat, signalpb.SyncMessage_MessageRequestResponse_BLOCK)
	if err != nil || mrr.GetMessageRequestResponse().GetThreadAci() != string(aliceACI) {
		t.Error("mrr direct")
	}
	bl := syncBlocked([]core.ACI{aliceACI, "bad"}, []core.ChatID{groupChat, "g:bad"})
	if b := bl.GetBlocked(); len(b.GetAcis()) != 1 || len(b.GetBlockedAcis()) != 1 || len(b.GetGroupIds()) != 1 || len(b.GetBlockedGroups()) != 1 {
		t.Errorf("blocked %v", bl)
	}
	sp := syncStickerPack([]byte{1}, []byte{2}, false)
	if len(sp.GetStickerPackOperation()) != 1 || sp.GetStickerPackOperation()[0].GetType() != signalpb.SyncMessage_StickerPackOperation_REMOVE {
		t.Error("sticker op")
	}
	ver, err := syncVerified(aliceACI, []byte{5}, true)
	if err != nil || ver.GetVerified().GetState() != signalpb.Verified_VERIFIED || ver.GetVerified().GetDestinationAci() != string(aliceACI) {
		t.Error("verified")
	}
}

func TestPointerHelpers(t *testing.T) {
	if f := pointerFlags(core.OutgoingAttachment{VoiceNote: true}, true, true); f != uint32(signalpb.AttachmentPointer_VOICE_MESSAGE|signalpb.AttachmentPointer_BORDERLESS|signalpb.AttachmentPointer_GIF) {
		t.Errorf("flags %b", f)
	}
	p := &signalpb.AttachmentPointer{}
	decoratePointer(p, "image/png", "a.png", 10, 20, "cap", "hash", 2)
	if p.GetContentType() != "image/png" || p.GetFileName() != "a.png" || p.GetWidth() != 10 || p.GetHeight() != 20 ||
		p.GetCaption() != "cap" || p.GetBlurHash() != "hash" || p.GetFlags() != 2 || len(p.GetClientUuid()) != 16 || p.GetUploadTimestamp() == 0 {
		t.Errorf("decorated %v", p)
	}
}
