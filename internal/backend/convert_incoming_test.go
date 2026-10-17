package backend

import (
	"encoding/hex"
	"reflect"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"

	"warte/internal/core"
)

// ptrAtt builds an attachment pointer. Its CDN key is not real, and
// TestMain (nonetwork_test.go) points the CDN hosts at .invalid and refuses
// every HTTP request, so a test that triggers a download never reaches
// Signal's CDN.
func ptrAtt(key, ct string, size uint32) *signalpb.AttachmentPointer {
	return &signalpb.AttachmentPointer{
		AttachmentIdentifier: &signalpb.AttachmentPointer_CdnKey{CdnKey: key},
		ContentType:          proto.String(ct),
		Key:                  []byte("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"),
		Size:                 proto.Uint32(size),
		Digest:               []byte{1, 2, 3},
	}
}

func TestClassifyDataMessage(t *testing.T) {
	tests := []struct {
		name string
		dm   *signalpb.DataMessage
		want dmKind
	}{
		{"nil", nil, dmEmpty},
		{"empty", &signalpb.DataMessage{}, dmEmpty},
		{"text", &signalpb.DataMessage{Body: proto.String("hi")}, dmContent},
		{"empty body", &signalpb.DataMessage{Body: proto.String("")}, dmEmpty},
		{"attachment", &signalpb.DataMessage{Attachments: []*signalpb.AttachmentPointer{{}}}, dmContent},
		{"sticker", &signalpb.DataMessage{Sticker: &signalpb.DataMessage_Sticker{}}, dmContent},
		{"contact", &signalpb.DataMessage{Contact: []*signalpb.DataMessage_Contact{{}}}, dmContent},
		{"payment", &signalpb.DataMessage{Payment: &signalpb.DataMessage_Payment{}}, dmContent},
		{"gift", &signalpb.DataMessage{GiftBadge: &signalpb.DataMessage_GiftBadge{}}, dmContent},
		{"poll", &signalpb.DataMessage{PollCreate: &signalpb.DataMessage_PollCreate{}}, dmContent},
		{"view once", &signalpb.DataMessage{IsViewOnce: proto.Bool(true)}, dmContent},
		{"timer", &signalpb.DataMessage{Flags: proto.Uint32(uint32(signalpb.DataMessage_EXPIRATION_TIMER_UPDATE))}, dmTimerUpdate},
		{"timer wins over body", &signalpb.DataMessage{Body: proto.String("x"), Flags: proto.Uint32(uint32(signalpb.DataMessage_EXPIRATION_TIMER_UPDATE))}, dmTimerUpdate},
		{"reaction", &signalpb.DataMessage{Reaction: &signalpb.DataMessage_Reaction{}}, dmReaction},
		{"story reaction", &signalpb.DataMessage{Reaction: &signalpb.DataMessage_Reaction{}, StoryContext: &signalpb.DataMessage_StoryContext{}}, dmReaction},
		{"delete", &signalpb.DataMessage{Delete: &signalpb.DataMessage_Delete{}}, dmDelete},
		{"admin delete", &signalpb.DataMessage{AdminDelete: &signalpb.DataMessage_AdminDelete{}}, dmAdminDelete},
		{"poll vote", &signalpb.DataMessage{PollVote: &signalpb.DataMessage_PollVote{}}, dmPollVote},
		{"poll terminate", &signalpb.DataMessage{PollTerminate: &signalpb.DataMessage_PollTerminate{}}, dmPollTerminate},
		{"pin", &signalpb.DataMessage{PinMessage: &signalpb.DataMessage_PinMessage{}}, dmPin},
		{"unpin", &signalpb.DataMessage{UnpinMessage: &signalpb.DataMessage_UnpinMessage{}}, dmUnpin},
		{"group change", &signalpb.DataMessage{GroupV2: &signalpb.GroupContextV2{GroupChange: []byte{1}}}, dmGroupUpdate},
		{"group context", &signalpb.DataMessage{GroupV2: &signalpb.GroupContextV2{Revision: proto.Uint32(0)}}, dmGroupContext},
		{"group text", &signalpb.DataMessage{Body: proto.String("x"), GroupV2: &signalpb.GroupContextV2{}}, dmContent},
		{"group profile key", &signalpb.DataMessage{GroupV2: &signalpb.GroupContextV2{}, Flags: proto.Uint32(uint32(signalpb.DataMessage_PROFILE_KEY_UPDATE))}, dmEmpty},
		{"profile key", &signalpb.DataMessage{Flags: proto.Uint32(uint32(signalpb.DataMessage_PROFILE_KEY_UPDATE)), ProfileKey: make([]byte, 32)}, dmEmpty},
		{"future", &signalpb.DataMessage{RequiredProtocolVersion: proto.Uint32(uint32(signalpb.DataMessage_CURRENT) + 1)}, dmUnsupported},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyDataMessage(tt.dm); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
	if dmKind(99).String() == "" || dmContent.String() != "content" {
		t.Error("kind names")
	}
}

func TestConvertDataMessageText(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	cc := convCtx{Self: selfACI, Chat: groupChat, Sender: aliceACI, ServerTS: 777, ReceivedAt: now}
	dm := &signalpb.DataMessage{
		Timestamp:   proto.Uint64(1700000000000),
		Body:        proto.String("hey ￼, bold"),
		ExpireTimer: proto.Uint32(3600),
		BodyRanges: []*signalpb.BodyRange{
			{Start: proto.Uint32(4), Length: proto.Uint32(1), AssociatedValue: &signalpb.BodyRange_MentionAci{MentionAci: string(selfACI)}},
			styleRange(7, 4, signalpb.BodyRange_BOLD),
		},
	}
	conv := convertDataMessage(cc, dm)
	m := conv.Msg
	want := core.Message{
		ID:              core.MessageID{Chat: groupChat, Author: aliceACI, Timestamp: 1700000000000},
		Kind:            core.KindNormal,
		SentAt:          time.UnixMilli(1700000000000).UTC(),
		ReceivedAt:      now,
		ServerTimestamp: 777,
		Body:            "hey ￼, bold",
		Ranges: []core.BodyRange{
			{Start: 4, Length: 1, Style: core.StyleMention, MentionACI: selfACI},
			{Start: 7, Length: 4, Style: core.StyleBold},
		},
		MentionsSelf: true,
		ExpiresIn:    time.Hour,
	}
	if !reflect.DeepEqual(m, want) {
		t.Errorf("got  %+v\nwant %+v", m, want)
	}
	if len(conv.Atts) != 0 || conv.LongText != nil {
		t.Errorf("unexpected attachments %+v", conv)
	}
}

func TestConvertDataMessageOutgoingSync(t *testing.T) {
	cc := convCtx{Self: selfACI, Chat: aliceChat, Sender: selfACI}
	dm := &signalpb.DataMessage{Timestamp: proto.Uint64(5), Body: proto.String("from my phone ￼"),
		BodyRanges: []*signalpb.BodyRange{{Start: proto.Uint32(14), Length: proto.Uint32(1), AssociatedValue: &signalpb.BodyRange_MentionAci{MentionAci: string(selfACI)}}}}
	m := convertDataMessage(cc, dm).Msg
	if !m.Outgoing || m.Status != core.StatusSent {
		t.Errorf("sync transcript: outgoing=%v status=%v", m.Outgoing, m.Status)
	}
	if m.MentionsSelf {
		t.Error("own message must not count as mentioning self")
	}
}

func TestConvertDataMessageAttachments(t *testing.T) {
	cc := convCtx{Self: selfACI, Chat: aliceChat, Sender: aliceACI}
	voice := ptrAtt("voice", "audio/aac", 1000)
	voice.Flags = proto.Uint32(uint32(signalpb.AttachmentPointer_VOICE_MESSAGE))
	gif := ptrAtt("gif", "video/mp4", 2000)
	gif.Flags = proto.Uint32(uint32(signalpb.AttachmentPointer_GIF | signalpb.AttachmentPointer_BORDERLESS))
	img := ptrAtt("img", "image/jpeg", 300)
	img.Width, img.Height = proto.Uint32(640), proto.Uint32(480)
	img.FileName, img.Caption, img.BlurHash = proto.String("cat.jpg"), proto.String("my cat"), proto.String("LKO2?U%2Tw=w")
	long := ptrAtt("long", longTextContentType, 5000)
	broken := &signalpb.AttachmentPointer{ContentType: proto.String("image/png")}
	dm := &signalpb.DataMessage{
		Timestamp:   proto.Uint64(42),
		Body:        proto.String("truncated…"),
		Attachments: []*signalpb.AttachmentPointer{img, long, voice, gif, broken, nil},
	}
	conv := convertDataMessage(cc, dm)
	m := conv.Msg
	if len(m.Attachments) != 4 {
		t.Fatalf("got %d attachments, want 4 (long text and nil excluded)", len(m.Attachments))
	}
	a := m.Attachments[0]
	if a.ContentType != "image/jpeg" || a.FileName != "cat.jpg" || a.Width != 640 || a.Height != 480 ||
		a.Caption != "my cat" || a.BlurHash == "" || a.Size != 300 || a.Failed || a.Downloaded {
		t.Errorf("image attachment %+v", a)
	}
	if !m.Attachments[1].VoiceNote || m.Attachments[1].GIF {
		t.Errorf("voice flags %+v", m.Attachments[1])
	}
	if !m.Attachments[2].GIF || !m.Attachments[2].Borderless {
		t.Errorf("gif flags %+v", m.Attachments[2])
	}
	if !m.Attachments[3].Failed {
		t.Error("pointer without location should be marked failed")
	}
	if conv.LongText == nil || conv.LongText.Role != roleLongText || conv.LongText.Pointer != long {
		t.Fatalf("long text ref %+v", conv.LongText)
	}
	if len(conv.Atts) != 5 {
		t.Errorf("refs = %d, want 5 (4 attachments + long text)", len(conv.Atts))
	}
	ids := map[string]bool{}
	for _, r := range conv.Atts {
		if ids[r.ID] {
			t.Errorf("duplicate attachment id %s", r.ID)
		}
		ids[r.ID] = true
	}
	// Stable: converting again yields the same IDs.
	again := convertDataMessage(cc, dm)
	for i := range m.Attachments {
		if again.Msg.Attachments[i].ID != m.Attachments[i].ID {
			t.Error("attachment IDs are not stable")
		}
	}
	// Different message → different IDs even for the same pointer.
	cc2 := cc
	cc2.Chat = groupChat
	if other := convertDataMessage(cc2, dm); other.Msg.Attachments[0].ID == m.Attachments[0].ID {
		t.Error("attachment IDs must be per message")
	}
}

func TestConvertDataMessageRichContent(t *testing.T) {
	cc := convCtx{Self: selfACI, Chat: groupChat, Sender: bobACI}
	packID := []byte("0123456789abcdef")
	packKey := []byte("0123456789abcdef0123456789abcdef")
	thumb := ptrAtt("thumb", "image/jpeg", 10)
	thumb.Width, thumb.Height = proto.Uint32(32), proto.Uint32(24)
	dm := &signalpb.DataMessage{
		Timestamp: proto.Uint64(100),
		Body:      proto.String("see https://example.com/a"),
		Quote: &signalpb.DataMessage_Quote{
			Id: proto.Uint64(50), AuthorAciBinary: aliceUUID[:], Text: proto.String("orig **x**"),
			BodyRanges:  []*signalpb.BodyRange{styleRange(5, 5, signalpb.BodyRange_BOLD)},
			Attachments: []*signalpb.DataMessage_Quote_QuotedAttachment{{ContentType: proto.String("image/png"), FileName: proto.String("p.png"), Thumbnail: thumb}},
		},
		Preview: []*signalpb.Preview{
			{Url: proto.String("")},
			{Url: proto.String("https://example.com/a"), Title: proto.String("Example"), Description: proto.String("Desc"),
				Date: proto.Uint64(1600000000000), Image: ptrAtt("prev", "image/png", 99)},
		},
		Contact: []*signalpb.DataMessage_Contact{{
			Name:   &signalpb.DataMessage_Contact_Name{GivenName: proto.String("Ada"), FamilyName: proto.String("Lovelace")},
			Number: []*signalpb.DataMessage_Contact_Phone{{Value: proto.String(" +1555 ")}, {Value: proto.String("")}},
			Email:  []*signalpb.DataMessage_Contact_Email{{Value: proto.String("ada@example.com")}},
		}},
		Sticker:    &signalpb.DataMessage_Sticker{PackId: packID, PackKey: packKey, StickerId: proto.Uint32(7), Emoji: proto.String("🐱"), Data: ptrAtt("stk", "image/webp", 1234)},
		PollCreate: &signalpb.DataMessage_PollCreate{Question: proto.String("Lunch?"), AllowMultiple: proto.Bool(true), Options: []string{"Pizza", "Sushi"}},
		GiftBadge:  &signalpb.DataMessage_GiftBadge{},
		Payment: &signalpb.DataMessage_Payment{Item: &signalpb.DataMessage_Payment_Notification_{
			Notification: &signalpb.DataMessage_Payment_Notification{Note: proto.String("rent")}}},
	}
	conv := convertDataMessage(cc, dm)
	m := conv.Msg
	if q := m.Quote; q == nil || q.ID != (core.MessageID{Chat: groupChat, Author: aliceACI, Timestamp: 50}) ||
		q.Text != "orig **x**" || len(q.Ranges) != 1 || q.Attachment == nil ||
		q.Attachment.Width != 32 || q.Attachment.ContentType != "image/jpeg" || q.Attachment.FileName != "p.png" {
		t.Errorf("quote %+v", m.Quote)
	}
	if p := m.Preview; p == nil || p.URL != "https://example.com/a" || p.Title != "Example" || p.Description != "Desc" ||
		!p.Date.Equal(time.UnixMilli(1600000000000)) || p.Image == nil || p.Image.ID == "" {
		t.Errorf("preview %+v", m.Preview)
	}
	if c := m.Contact; c == nil || c.Name != "Ada Lovelace" || !reflect.DeepEqual(c.Phones, []string{"+1555"}) || c.Emails[0] != "ada@example.com" {
		t.Errorf("contact %+v", m.Contact)
	}
	if s := m.Sticker; s == nil || s.PackID != hex.EncodeToString(packID) || s.PackKey != hex.EncodeToString(packKey) ||
		s.StickerID != 7 || s.Emoji != "🐱" || !s.Attachment.Borderless || s.Attachment.ContentType != "image/webp" {
		t.Errorf("sticker %+v", m.Sticker)
	}
	if p := m.Poll; p == nil || p.Question != "Lunch?" || !p.AllowMultiple || len(p.Options) != 2 || p.Options[1].Text != "Sushi" {
		t.Errorf("poll %+v", m.Poll)
	}
	if !m.GiftBadge || m.PaymentNotice != "Payment received: rent (view it on your phone)" {
		t.Errorf("gift=%v payment=%q", m.GiftBadge, m.PaymentNotice)
	}
	roles := map[attRole]int{}
	for _, r := range conv.Atts {
		roles[r.Role]++
	}
	if roles[roleQuote] != 1 || roles[rolePreview] != 1 || roles[roleSticker] != 1 {
		t.Errorf("roles %v", roles)
	}
}

func TestConvertDataMessageStoryReplyAndUnsupported(t *testing.T) {
	cc := convCtx{Self: selfACI, Chat: aliceChat, Sender: aliceACI}
	dm := &signalpb.DataMessage{Timestamp: proto.Uint64(9), Body: proto.String("nice!"),
		StoryContext: &signalpb.DataMessage_StoryContext{AuthorAciBinary: selfUUID[:], SentTimestamp: proto.Uint64(3)}}
	m := convertDataMessage(cc, dm).Msg
	if m.Kind != core.KindStoryReply || m.StoryContext == nil || *m.StoryContext != (core.StoryRef{Author: selfACI, Timestamp: 3}) {
		t.Errorf("story reply %+v", m)
	}
	dm.StoryContext = &signalpb.DataMessage_StoryContext{AuthorAci: proto.String("garbage")}
	if m := convertDataMessage(cc, dm).Msg; m.Kind != core.KindNormal || m.StoryContext != nil {
		t.Error("invalid story author should not make a story reply")
	}
}

func TestPaymentNotice(t *testing.T) {
	tests := []struct {
		p    *signalpb.DataMessage_Payment
		out  bool
		want string
	}{
		{&signalpb.DataMessage_Payment{Item: &signalpb.DataMessage_Payment_Notification_{Notification: &signalpb.DataMessage_Payment_Notification{}}}, true, "Payment sent (view it on your phone)"},
		{&signalpb.DataMessage_Payment{Item: &signalpb.DataMessage_Payment_Activation_{Activation: &signalpb.DataMessage_Payment_Activation{Type: signalpb.DataMessage_Payment_Activation_REQUEST.Enum()}}}, false, "Requested to activate payments"},
		{&signalpb.DataMessage_Payment{Item: &signalpb.DataMessage_Payment_Activation_{Activation: &signalpb.DataMessage_Payment_Activation{Type: signalpb.DataMessage_Payment_Activation_ACTIVATED.Enum()}}}, false, "Activated payments"},
		{&signalpb.DataMessage_Payment{}, false, "Payment (view it on your phone)"},
	}
	for _, tt := range tests {
		if got := paymentNotice(tt.p, tt.out); got != tt.want {
			t.Errorf("got %q, want %q", got, tt.want)
		}
	}
}

func TestQuoteTypes(t *testing.T) {
	owner := core.MessageID{Chat: aliceChat, Author: aliceACI, Timestamp: 1}
	q, ref := quoteFromSignal(aliceChat, owner, &signalpb.DataMessage_Quote{Id: proto.Uint64(2), AuthorAci: proto.String(string(bobACI)), Type: signalpb.DataMessage_Quote_GIFT_BADGE.Enum()})
	if q.Text != "Gift badge" || ref != nil || q.ID.Author != bobACI {
		t.Errorf("gift quote %+v", q)
	}
	q, _ = quoteFromSignal(aliceChat, owner, &signalpb.DataMessage_Quote{Id: proto.Uint64(2), Text: proto.String("Lunch?"), Type: signalpb.DataMessage_Quote_POLL.Enum()})
	if q.Text != "📊 Lunch?" {
		t.Errorf("poll quote %q", q.Text)
	}
}

func TestReactionFromSignal(t *testing.T) {
	tests := []struct {
		name    string
		r       *signalpb.DataMessage_Reaction
		wantErr bool
		want    reactionTarget
	}{
		{"binary", &signalpb.DataMessage_Reaction{Emoji: proto.String("👍"), TargetAuthorAciBinary: aliceUUID[:], TargetSentTimestamp: proto.Uint64(5)},
			false, reactionTarget{Author: aliceACI, Timestamp: 5, Emoji: "👍"}},
		{"string remove", &signalpb.DataMessage_Reaction{Emoji: proto.String("👍"), Remove: proto.Bool(true), TargetAuthorAci: proto.String(string(bobACI)), TargetSentTimestamp: proto.Uint64(6)},
			false, reactionTarget{Author: bobACI, Timestamp: 6, Emoji: "👍", Remove: true}},
		{"no author", &signalpb.DataMessage_Reaction{Emoji: proto.String("👍"), TargetSentTimestamp: proto.Uint64(5)}, true, reactionTarget{}},
		{"no ts", &signalpb.DataMessage_Reaction{Emoji: proto.String("👍"), TargetAuthorAciBinary: aliceUUID[:]}, true, reactionTarget{}},
		{"empty emoji", &signalpb.DataMessage_Reaction{TargetAuthorAciBinary: aliceUUID[:], TargetSentTimestamp: proto.Uint64(5)}, true, reactionTarget{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := reactionFromSignal(tt.r)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestApplyEdit(t *testing.T) {
	sent := time.Unix(100, 0)
	m := core.Message{Body: "v1", Ranges: []core.BodyRange{{Start: 0, Length: 1, Style: core.StyleBold}}, SentAt: sent,
		Preview: &core.LinkPreview{URL: "https://a"}}
	applyEdit(&m, core.Message{Body: "v2", MentionsSelf: true}, sent)
	applyEdit(&m, core.Message{Body: "v3"}, time.Unix(200, 0))
	if m.Body != "v3" || !m.Edited || m.MentionsSelf || m.Preview != nil {
		t.Errorf("after edits %+v", m)
	}
	want := []core.EditRevision{
		{Body: "v1", Ranges: []core.BodyRange{{Start: 0, Length: 1, Style: core.StyleBold}}, At: sent},
		{Body: "v2", At: time.Unix(200, 0)},
	}
	if !reflect.DeepEqual(m.EditHistory, want) {
		t.Errorf("history %+v", m.EditHistory)
	}
}

func TestRemoteDelete(t *testing.T) {
	m := core.Message{
		ID: core.MessageID{Chat: aliceChat, Author: aliceACI, Timestamp: 1}, Body: "secret", Outgoing: true,
		Attachments: []core.Attachment{{ID: "a"}}, Reactions: []core.Reaction{{Emoji: "x"}}, Status: core.StatusRead,
		ExpiresIn: time.Hour, Quote: &core.Quote{}, Poll: &core.Poll{}, EditHistory: []core.EditRevision{{Body: "older secret"}},
	}
	remoteDelete(&m)
	if !m.RemoteDeleted || m.Body != "" || m.Attachments != nil || m.Reactions != nil || m.Quote != nil ||
		m.Poll != nil || m.EditHistory != nil {
		t.Errorf("content kept after delete: %+v", m)
	}
	if m.ID.Timestamp != 1 || !m.Outgoing || m.Status != core.StatusRead || m.ExpiresIn != time.Hour {
		t.Errorf("identity lost: %+v", m)
	}
}

func TestApplyPollVote(t *testing.T) {
	newPoll := func(multi bool) *core.Poll {
		return &core.Poll{AllowMultiple: multi, Options: []core.PollOption{{Text: "a"}, {Text: "b"}, {Text: "c"}}}
	}
	tests := []struct {
		name  string
		multi bool
		votes [][]uint32 // successive votes by alice
		want  []uint32
	}{
		{"single", false, [][]uint32{{1}}, []uint32{1}},
		{"change vote", false, [][]uint32{{1}, {2}}, []uint32{2}},
		{"single keeps first of many", false, [][]uint32{{2, 0}}, []uint32{2}},
		{"multi", true, [][]uint32{{0, 2}}, []uint32{0, 2}},
		{"dedupe", true, [][]uint32{{1, 1}}, []uint32{1}},
		{"out of range ignored", true, [][]uint32{{5, 0}}, []uint32{0}},
		{"retract", true, [][]uint32{{0}, {}}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newPoll(tt.multi)
			applyPollVote(p, bobACI, []uint32{1})
			for _, v := range tt.votes {
				applyPollVote(p, aliceACI, v)
			}
			if got := pollVotesOf(p, aliceACI); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("alice votes %v, want %v", got, tt.want)
			}
			if got := pollVotesOf(p, bobACI); !reflect.DeepEqual(got, []uint32{1}) {
				t.Errorf("bob's vote changed: %v", got)
			}
		})
	}
}

func TestPinDuration(t *testing.T) {
	if d := pinDuration(&signalpb.DataMessage_PinMessage{PinDuration: &signalpb.DataMessage_PinMessage_PinDurationSeconds{PinDurationSeconds: 60}}); d != time.Minute {
		t.Errorf("got %v", d)
	}
	if d := pinDuration(&signalpb.DataMessage_PinMessage{PinDuration: &signalpb.DataMessage_PinMessage_PinDurationForever{PinDurationForever: true}}); d != 0 {
		t.Errorf("forever got %v", d)
	}
}

func TestSharedContactNameFallbacks(t *testing.T) {
	c := sharedContactFromSignal(&signalpb.DataMessage_Contact{Name: &signalpb.DataMessage_Contact_Name{Nickname: proto.String("Nick"), GivenName: proto.String("G")}})
	if c.Name != "Nick" {
		t.Errorf("nickname first: %q", c.Name)
	}
	c = sharedContactFromSignal(&signalpb.DataMessage_Contact{Organization: proto.String("ACME")})
	if c.Name != "ACME" || c.Org != "ACME" {
		t.Errorf("org fallback: %+v", c)
	}
	c = sharedContactFromSignal(&signalpb.DataMessage_Contact{Name: &signalpb.DataMessage_Contact_Name{Prefix: proto.String("Dr."), GivenName: proto.String("A"), Suffix: proto.String("Jr")}})
	if !strings.HasPrefix(c.Name, "Dr. A") || !strings.HasSuffix(c.Name, "Jr") {
		t.Errorf("full name: %q", c.Name)
	}
}
