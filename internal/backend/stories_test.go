package backend

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-signal/pkg/signalmeow/events"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/types"

	"warte/internal/core"
)

func TestConvertStory(t *testing.T) {
	ptr := &signalpb.AttachmentPointer{
		AttachmentIdentifier: &signalpb.AttachmentPointer_CdnKey{CdnKey: "abc"}, Key: []byte{1},
		ContentType: proto.String("image/jpeg"), Size: proto.Uint32(1234), Caption: proto.String("sunset"),
		Width: proto.Uint32(1080), Height: proto.Uint32(1920), BlurHash: proto.String("LKO2"),
	}
	media, err := convertStory(aliceACI, "", 1000, &signalpb.StoryMessage{
		Attachment: &signalpb.StoryMessage_FileAttachment{FileAttachment: ptr}, AllowsReplies: proto.Bool(true),
	})
	if err != nil {
		t.Fatal(err)
	}
	s := media.Story
	if s.ID != (core.MessageID{Chat: aliceChat, Author: aliceACI, Timestamp: 1000}) || s.Group != "" || !s.At.Equal(msTime(1000)) {
		t.Errorf("id %+v", s)
	}
	if s.Media == nil || s.Media.ContentType != "image/jpeg" || s.Media.Width != 1080 || s.Caption != "sunset" || s.Text != nil {
		t.Errorf("media %+v", s.Media)
	}
	if media.Media == nil || media.Media.ID != s.Media.ID || media.Media.Role != roleStory || !media.AllowsReplies {
		t.Errorf("ref %+v", media.Media)
	}
	again, _ := convertStory(aliceACI, "", 1000, &signalpb.StoryMessage{Attachment: &signalpb.StoryMessage_FileAttachment{FileAttachment: ptr}})
	if again.Story.Media.ID != s.Media.ID {
		t.Error("attachment ID must be stable")
	}

	text, err := convertStory(aliceACI, groupChat, 2000, &signalpb.StoryMessage{Attachment: &signalpb.StoryMessage_TextAttachment{TextAttachment: &signalpb.TextAttachment{
		Text: proto.String("hello"), TextStyle: signalpb.TextAttachment_SERIF.Enum(), TextForegroundColor: proto.Uint32(0xffffffff),
		Background: &signalpb.TextAttachment_Gradient_{Gradient: &signalpb.TextAttachment_Gradient{Colors: []uint32{0xff000000, 0xff0000ff}, Angle: proto.Uint32(90)}},
		Preview:    &signalpb.Preview{Url: proto.String("https://signal.org")},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	ts := text.Story.Text
	if ts == nil || ts.Text != "hello" || ts.Style != int(signalpb.TextAttachment_SERIF) || ts.Foreground != 0xffffffff ||
		ts.Background != 0xff000000 || len(ts.Gradient) != 2 || ts.URL != "https://signal.org" {
		t.Errorf("text story %+v", ts)
	}
	if text.Story.Group != groupChat || text.Story.ID.Chat != groupChat || text.AllowsReplies {
		t.Errorf("group story %+v", text.Story)
	}

	legacy, err := convertStory(aliceACI, "", 3000, &signalpb.StoryMessage{Attachment: &signalpb.StoryMessage_TextAttachment{TextAttachment: &signalpb.TextAttachment{
		Text: proto.String("x"), TextStyle: signalpb.TextAttachment_Style(42).Enum(),
		Background: &signalpb.TextAttachment_Gradient_{Gradient: &signalpb.TextAttachment_Gradient{StartColor: proto.Uint32(1), EndColor: proto.Uint32(2)}},
	}}})
	if err != nil || legacy.Story.Text.Style != 0 || len(legacy.Story.Text.Gradient) != 2 || legacy.Story.Text.Gradient[1] != 2 {
		t.Errorf("legacy gradient / unknown style %+v %v", legacy.Story.Text, err)
	}
	solid, _ := convertStory(aliceACI, "", 3000, &signalpb.StoryMessage{Attachment: &signalpb.StoryMessage_TextAttachment{TextAttachment: &signalpb.TextAttachment{
		Text: proto.String("x"), Background: &signalpb.TextAttachment_Color{Color: 0xff112233},
	}}})
	if solid.Story.Text.Background != 0xff112233 || solid.Story.Text.Gradient != nil {
		t.Errorf("solid %+v", solid.Story.Text)
	}
	if _, err := convertStory(aliceACI, "", 1, &signalpb.StoryMessage{Attachment: &signalpb.StoryMessage_TextAttachment{TextAttachment: &signalpb.TextAttachment{}}}); !errors.Is(err, errEmptyStory) {
		t.Errorf("text story without text or preview: %v", err)
	}
	if _, err := convertStory(aliceACI, "", 1, &signalpb.StoryMessage{}); !errors.Is(err, errEmptyStory) {
		t.Errorf("story without attachment: %v", err)
	}
}

func TestStoryViewReceiptsEnabled(t *testing.T) {
	on, off := core.Settings{ReadReceipts: true}, core.Settings{}
	tests := []struct {
		ar   *signalpb.AccountRecord
		s    core.Settings
		want bool
	}{
		{nil, on, true},
		{nil, off, false},
		{&signalpb.AccountRecord{StoryViewReceiptsEnabled: signalpb.OptionalBool_UNSET}, on, true},
		{&signalpb.AccountRecord{StoryViewReceiptsEnabled: signalpb.OptionalBool_DISABLED}, on, false},
		{&signalpb.AccountRecord{StoryViewReceiptsEnabled: signalpb.OptionalBool_ENABLED}, on, true},
		// Desktop's sendReceipts also requires the read receipt setting.
		{&signalpb.AccountRecord{StoryViewReceiptsEnabled: signalpb.OptionalBool_ENABLED}, off, false},
	}
	for i, tt := range tests {
		if got := storyViewReceiptsEnabled(tt.ar, tt.s); got != tt.want {
			t.Errorf("%d: got %v", i, got)
		}
	}
}

func TestSyncViewedAndReceipt(t *testing.T) {
	sm := syncViewed([]core.MessageID{{Author: aliceACI, Timestamp: 10}, {Author: "bad", Timestamp: 11}, {Author: bobACI}})
	if sm == nil || len(sm.Viewed) != 1 || sm.Content != nil {
		t.Fatalf("viewed sync %+v", sm)
	}
	v := sm.Viewed[0]
	if v.GetSenderAci() != aliceUUID.String() || string(v.GetSenderAciBinary()) != string(aliceUUID[:]) || v.GetTimestamp() != 10 {
		t.Errorf("viewed %+v", v)
	}
	// Round trip through the wire format.
	raw, err := proto.Marshal(&signalpb.Content{Content: &signalpb.Content_SyncMessage{SyncMessage: sm}})
	if err != nil {
		t.Fatal(err)
	}
	var back signalpb.Content
	if err := proto.Unmarshal(raw, &back); err != nil || len(back.GetSyncMessage().GetViewed()) != 1 {
		t.Errorf("round trip %v", err)
	}
	if syncViewed(nil) != nil {
		t.Error("empty viewed sync")
	}
	r := viewedReceipt(5, 6)
	if r.GetType() != signalpb.ReceiptMessage_VIEWED || len(r.GetTimestamp()) != 2 {
		t.Errorf("receipt %+v", r)
	}
}

func storyEvent(sender uuid.UUID, ts uint64, sm *signalpb.StoryMessage) *events.Story {
	return &events.Story{Info: events.MessageInfo{Sender: sender, ChatID: sender.String()}, Timestamp: ts, Story: sm, AllowsReplies: sm.GetAllowsReplies()}
}

func textStory(text string, replies bool) *signalpb.StoryMessage {
	return &signalpb.StoryMessage{AllowsReplies: proto.Bool(replies), Attachment: &signalpb.StoryMessage_TextAttachment{
		TextAttachment: &signalpb.TextAttachment{Text: proto.String(text)}}}
}

func TestIncomingStories(t *testing.T) {
	b, clock := newTestBackend(t)
	ctx := context.Background()
	now := uint64(clock.Now().UnixMilli())

	mustHandle(t, b, storyEvent(aliceUUID, now-1000, textStory("hi", true)))
	if sc := eventsOf[core.StoriesChanged](drain(t, b)); len(sc) != 1 {
		t.Errorf("stories changed %d", len(sc))
	}
	list, err := b.Stories(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("stories %+v %v", list, err)
	}
	s := list[0]
	if s.AuthorName != "Alice Chen" || s.Viewed || s.Text == nil || s.Text.Text != "hi" || s.ID.Author != aliceACI {
		t.Errorf("story %+v", s)
	}
	// Duplicate delivery.
	mustHandle(t, b, storyEvent(aliceUUID, now-1000, textStory("hi", true)))
	if sc := eventsOf[core.StoriesChanged](drain(t, b)); len(sc) != 0 {
		t.Error("duplicate story announced")
	}
	// Expired on arrival.
	mustHandle(t, b, storyEvent(aliceUUID, now-uint64(25*time.Hour/time.Millisecond), textStory("old", true)))
	// From a blocked sender.
	if err := b.setContactBlocked(ctx, bobACI, true); err != nil {
		t.Fatal(err)
	}
	mustHandle(t, b, storyEvent(bobUUID, now-500, textStory("blocked", true)))
	if list, _ := b.Stories(ctx); len(list) != 1 {
		t.Errorf("expired or blocked story stored: %+v", list)
	}

	// Viewing marks it viewed once (no client: no receipts).
	if err := b.MarkStoryViewed(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := b.db.GetStory(ctx, s.ID); !got.Viewed {
		t.Error("not viewed")
	}
	drain(t, b)
	if err := b.MarkStoryViewed(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	if sc := eventsOf[core.StoriesChanged](drain(t, b)); len(sc) != 0 {
		t.Error("second view announced")
	}
	if err := b.MarkStoryViewed(ctx, core.MessageID{Author: aliceACI, Timestamp: 1}); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("unknown story: %v", err)
	}

	// Viewed on another device.
	mustHandle(t, b, storyEvent(aliceUUID, now-900, textStory("second", false)))
	mustHandle(t, b, &events.ViewedSync{Messages: []events.ViewedMessage{{Sender: aliceUUID, Timestamp: now - 900}}})
	if got, _ := b.db.GetStory(ctx, core.MessageID{Author: aliceACI, Timestamp: now - 900}); !got.Viewed {
		t.Error("viewed sync not applied")
	}
	// Replies are refused when the story disallows them.
	if b.storyAllowsReplies(ctx, core.MessageID{Author: aliceACI, Timestamp: now - 900}) {
		t.Error("story without replies allows replies")
	}
	if !b.storyAllowsReplies(ctx, s.ID) {
		t.Error("story with replies refuses them")
	}

	// Deleted for everyone by its author (a delete in the 1:1 chat).
	mustHandle(t, b, chatEvent(bobUUID, bobUUID.String(), &signalpb.DataMessage{Timestamp: proto.Uint64(now), Delete: &signalpb.DataMessage_Delete{TargetSentTimestamp: proto.Uint64(now - 900)}}))
	if _, err := b.db.GetStory(ctx, core.MessageID{Author: aliceACI, Timestamp: now - 900}); err != nil {
		t.Error("someone else deleted Alice's story")
	}
	mustHandle(t, b, chatEvent(aliceUUID, aliceUUID.String(), &signalpb.DataMessage{Timestamp: proto.Uint64(now), Delete: &signalpb.DataMessage_Delete{TargetSentTimestamp: proto.Uint64(now - 900)}}))
	if _, err := b.db.GetStory(ctx, core.MessageID{Author: aliceACI, Timestamp: now - 900}); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("story survived delete: %v", err)
	}

	// Stories expire after 24 hours.
	drain(t, b)
	clock.Advance(storyLifetime)
	if next := b.expireStories(ctx); !next.IsZero() {
		t.Errorf("next expiry %v", next)
	}
	if list, _ := b.Stories(ctx); len(list) != 0 {
		t.Errorf("stories after a day %+v", list)
	}
	if sc := eventsOf[core.StoriesChanged](drain(t, b)); len(sc) != 1 {
		t.Errorf("expiry announced %d times", len(sc))
	}
}

func TestStoryMediaIsLazyAndCleanedUp(t *testing.T) {
	b, clock := newTestBackend(t)
	ctx := context.Background()
	ts := uint64(clock.Now().UnixMilli()) - 1
	// A pointer without a CDN location cannot be downloaded.
	sm := &signalpb.StoryMessage{Attachment: &signalpb.StoryMessage_FileAttachment{FileAttachment: &signalpb.AttachmentPointer{
		ContentType: proto.String("image/png"), Size: proto.Uint32(10),
	}}}
	mustHandle(t, b, storyEvent(aliceUUID, ts, sm))
	id := core.MessageID{Chat: aliceChat, Author: aliceACI, Timestamp: ts}
	s, err := b.db.GetStory(ctx, id)
	if err != nil || s.Media == nil {
		t.Fatalf("story %+v %v", s, err)
	}
	recs, _ := b.db.ListAttachments(ctx, storyOwner(id))
	if len(recs) != 1 || recs[0].Downloaded {
		t.Fatalf("story media record %+v", recs)
	}
	if _, err := b.Attachment(ctx, id, s.Media.ID); !errors.Is(err, errUnavailable) {
		t.Errorf("Attachment = %v, want unavailable", err)
	}
	if got, _ := b.db.GetStory(ctx, id); !got.Media.Failed {
		t.Errorf("media not marked failed: %+v", got.Media)
	}
	if _, err := b.Attachment(ctx, id, "nope"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("unknown attachment: %v", err)
	}
	if err := b.deleteStory(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	if recs, _ := b.db.ListAttachments(ctx, storyOwner(id)); len(recs) != 0 {
		t.Errorf("media metadata left behind: %+v", recs)
	}
}

func TestGroupAndOwnStories(t *testing.T) {
	b, clock := newTestBackend(t)
	ctx := context.Background()
	now := uint64(clock.Now().UnixMilli())
	groupWithMembers(t, b, selfACI, aliceACI)
	gid := types.GroupIdentifier(groupChat.GroupID())
	group := func(sender uuid.UUID, ts uint64) *events.Story {
		e := storyEvent(sender, ts, textStory("group", true))
		e.GroupID, e.Info.ChatID = gid, string(gid)
		return e
	}
	mustHandle(t, b, group(aliceUUID, now-10))
	mustHandle(t, b, group(bobUUID, now-20)) // not a member
	list, _ := b.Stories(ctx)
	if len(list) != 1 || list[0].Group != groupChat || list[0].ID.Author != aliceACI {
		t.Fatalf("group stories %+v", list)
	}
	// Our own story, posted on the phone to "My Story".
	own := &events.Story{Info: events.MessageInfo{Sender: selfUUID}, Timestamp: now - 5, Story: textStory("mine", true), IsSync: true,
		Recipients: []events.StoryRecipient{{ServiceID: aliceUUID.String(), DistributionListIDs: []string{"00000000-0000-0000-0000-000000000000"}}}}
	mustHandle(t, b, own)
	ownID := core.MessageID{Author: selfACI, Timestamp: now - 5}
	if s, err := b.db.GetStory(ctx, ownID); err != nil || !s.Viewed || s.Group != "" {
		t.Errorf("own story %+v %v", s, err)
	}
	var meta storyMeta
	if ok, _ := b.kv.get(ctx, storyKey(ownID), &meta); !ok || meta.Recipients != 1 {
		t.Errorf("own story meta %+v", meta)
	}
	// Recipient update leaving nobody: deleted.
	mustHandle(t, b, &events.StoryRecipientUpdate{Timestamp: now - 5})
	if _, err := b.db.GetStory(ctx, ownID); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("own story survived empty recipient update: %v", err)
	}
	// Blocking the group hides its stories.
	if err := b.setChatBlockedLocal(ctx, groupChat, true); err != nil {
		t.Fatal(err)
	}
	if list, _ := b.Stories(ctx); len(list) != 0 {
		t.Errorf("blocked group's stories listed: %+v", list)
	}
}
