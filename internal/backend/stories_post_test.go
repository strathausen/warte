package backend

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"go.mau.fi/mautrix-signal/pkg/libsignalgo"
	"go.mau.fi/mautrix-signal/pkg/signalmeow"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/events"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/types"

	"warte/internal/core"
)

func TestValidateOutgoingStory(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 3))
	img.Set(1, 1, color.RGBA{R: 255, A: 255})
	png := encodePNG(t, img)
	cases := []struct {
		name string
		s    core.OutgoingStory
		want error // nil = valid
	}{
		{"empty", core.OutgoingStory{}, errEmptyMessage},
		{"blank text", core.OutgoingStory{Text: &core.TextStory{Text: "  "}}, errEmptyMessage},
		{"text", core.OutgoingStory{Text: &core.TextStory{Text: "hi"}}, nil},
		{"link only", core.OutgoingStory{Text: &core.TextStory{URL: "https://signal.org"}}, nil},
		{"too long", core.OutgoingStory{Text: &core.TextStory{Text: strings.Repeat("x", 701)}}, errInvalidStory},
		{"both", core.OutgoingStory{Text: &core.TextStory{Text: "hi"}, Media: &core.OutgoingAttachment{Data: png}}, errInvalidStory},
		{"no data", core.OutgoingStory{Media: &core.OutgoingAttachment{FileName: "a.png"}}, errEmptyMessage},
		{"not media", core.OutgoingStory{Media: &core.OutgoingAttachment{FileName: "a.txt", Data: []byte("hello")}}, errInvalidStory},
		{"image", core.OutgoingStory{Media: &core.OutgoingAttachment{FileName: "a.png", Data: png, Caption: "cap"}}, nil},
	}
	for _, c := range cases {
		ta, up, err := validateOutgoingStory(c.s)
		if c.want != nil {
			if !errors.Is(err, c.want) {
				t.Errorf("%s: %v, want %v", c.name, err, c.want)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if (ta != nil) == (up != nil) {
			t.Errorf("%s: text %v upload %v", c.name, ta, up)
		}
		if up != nil && (up.ContentType != "image/png" || up.Width != 4 || up.Height != 3 || up.Caption != "cap") {
			t.Errorf("%s: upload %+v", c.name, up.OutgoingAttachment)
		}
	}
}

func TestStoryTargets(t *testing.T) {
	g2 := core.GroupChatID("other")
	my, groups, err := storyTargets(selfACI, nil)
	if err != nil || !my || groups != nil {
		t.Errorf("empty: %v %v %v", my, groups, err)
	}
	my, groups, err = storyTargets(selfACI, []core.ChatID{groupChat, g2, groupChat})
	if err != nil || my || !slices.Equal(groups, []core.ChatID{groupChat, g2}) {
		t.Errorf("groups: %v %v %v", my, groups, err)
	}
	my, groups, err = storyTargets(selfACI, []core.ChatID{core.DirectChatID(selfACI), groupChat})
	if err != nil || !my || len(groups) != 1 {
		t.Errorf("my story + group: %v %v %v", my, groups, err)
	}
	if _, _, err := storyTargets(selfACI, []core.ChatID{core.DirectChatID(aliceACI)}); !errors.Is(err, errInvalidStory) {
		t.Errorf("direct chat target: %v", err)
	}
}

func TestStoryConnectionSet(t *testing.T) {
	alice, bob := libsignalgo.NewACIServiceID(aliceUUID), libsignalgo.NewACIServiceID(bobUUID)
	carol := libsignalgo.NewACIServiceID(uuid.MustParse("cccccccc-cccc-4ccc-8ccc-cccccccccccc"))
	dave := uuid.MustParse("dddddddd-dddd-4ddd-8ddd-dddddddddddd")
	last := &core.MessagePreview{Text: "hi"}
	chats := []core.Chat{
		{ID: core.DirectChatID(bobACI), Kind: core.ChatDirect, LastMessage: last},
		{ID: core.DirectChatID(aliceACI), Kind: core.ChatDirect, LastMessage: last}, // duplicate of base
		{ID: core.DirectChatID(aciOf(dave)), Kind: core.ChatDirect, LastMessage: last, MessageRequest: true},
		{ID: core.DirectChatID(aciOf(carol.UUID)), Kind: core.ChatDirect}, // never talked
		{ID: core.DirectChatID(selfACI), Kind: core.ChatNoteToSelf, LastMessage: last},
		{ID: groupChat, Kind: core.ChatGroup, LastMessage: last},
		{ID: core.DirectChatID("eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"), Kind: core.ChatDirect, LastMessage: last, Blocked: true},
	}
	base := []libsignalgo.ServiceID{alice, libsignalgo.NewACIServiceID(selfUUID), libsignalgo.NewPNIServiceID(dave)}
	got := storyConnectionSet(base, chats, selfACI)
	if want := []libsignalgo.ServiceID{alice, bob}; !slices.Equal(got, want) {
		t.Errorf("connections %v, want %v", got, want)
	}
}

func TestOwnTextStoryMatchesReceivedForm(t *testing.T) {
	in := core.TextStory{Text: "hello", Style: int(signalpb.TextAttachment_SERIF), Foreground: 0xff000000,
		Gradient: []uint32{0xff19a9fa, 0xff7097d7, 0xffffc369}, URL: "https://signal.org"}
	ta, _, err := validateOutgoingStory(core.OutgoingStory{Text: &in})
	if err != nil {
		t.Fatal(err)
	}
	conv, err := convertStory(aliceACI, "", 1000, &signalpb.StoryMessage{Attachment: &signalpb.StoryMessage_TextAttachment{TextAttachment: ta}})
	if err != nil {
		t.Fatal(err)
	}
	got := conv.Story.Text
	if got == nil || got.Text != in.Text || got.Style != in.Style || got.Foreground != in.Foreground || got.URL != in.URL ||
		!slices.Equal(got.Gradient, in.Gradient) || got.Background != in.Gradient[0] {
		t.Errorf("received form %+v, want %+v", got, in)
	}
	// Defaults: white on Signal blue.
	ta, _, _ = validateOutgoingStory(core.OutgoingStory{Text: &core.TextStory{Text: "x"}})
	if ta.GetTextForegroundColor() != signalmeow.DefaultStoryForeground || ta.GetColor() != signalmeow.DefaultStoryBackground {
		t.Errorf("defaults %v", ta)
	}
}

func TestStoreOwnStories(t *testing.T) {
	b, clock := newTestBackend(t)
	ctx := context.Background()
	groupWithMembers(t, b, selfACI, aliceACI)
	now := uint64(clock.Now().UnixMilli())

	ta, _, _ := validateOutgoingStory(core.OutgoingStory{Text: &core.TextStory{Text: "hi"}})
	text := ownStoryTarget{ID: core.MessageID{Chat: core.DirectChatID(selfACI), Author: selfACI, Timestamp: now}}
	if err := b.storeOwnStory(ctx, &text, ta, nil); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	data := encodePNG(t, img)
	_, up, err := validateOutgoingStory(core.OutgoingStory{Media: &core.OutgoingAttachment{FileName: "p.png", Data: data, Caption: "look"}})
	if err != nil {
		t.Fatal(err)
	}
	media := ownStoryTarget{ID: core.MessageID{Chat: groupChat, Author: selfACI, Timestamp: now + 1}, Group: groupChat}
	if err := b.storeOwnStory(ctx, &media, nil, up); err != nil {
		t.Fatal(err)
	}
	list, err := b.Stories(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("stories %+v %v", list, err)
	}
	if s := list[0]; s.Text == nil || s.Text.Text != "hi" || !s.Viewed || s.Group != "" || s.AuthorName == "" {
		t.Errorf("text story %+v", s)
	}
	s := list[1]
	if s.Media == nil || s.Media.ID != media.AttID || !s.Media.Downloaded || s.Caption != "look" || s.Group != groupChat {
		t.Errorf("media story %+v", s)
	}
	// Our own media is available locally without a download.
	got, err := b.Attachment(ctx, s.ID, media.AttID)
	if err != nil || !bytes.Equal(got, data) {
		t.Errorf("own story media: %v", err)
	}
	if !b.storyAllowsReplies(ctx, text.ID) {
		t.Error("own story should allow replies until the list says otherwise")
	}

	// Recipients recorded after sending, then used for the delete sync.
	alice := libsignalgo.NewACIServiceID(aliceUUID)
	res := &signalmeow.StorySendResult{SyncRecipients: []signalmeow.StorySyncRecipient{
		{ServiceID: alice, ListIDs: []uuid.UUID{signalmeow.MyStoryID}, AllowedToReply: false},
	}}
	meta := ownStoryMeta(res, false)
	if meta.Recipients != 1 || meta.AllowsReplies || meta.Sent[0].ACI != aliceACI || meta.Sent[0].Lists[0] != signalmeow.MyStoryID.String() {
		t.Errorf("meta %+v", meta)
	}
	if rs := storySyncRecipients(meta.Sent); len(rs) != 1 || rs[0].ServiceID != alice || rs[0].AllowedToReply || len(rs[0].ListIDs) != 0 {
		t.Errorf("delete sync recipients %+v", rs)
	}

	// Delete for everyone needs a connection; nothing is removed offline.
	if err := b.Delete(ctx, text.ID, true); !errors.Is(err, core.ErrNotLoggedIn) {
		t.Errorf("offline delete: %v", err)
	}
	if _, err := b.db.GetStory(ctx, text.ID); err != nil {
		t.Errorf("story removed by failed delete: %v", err)
	}
	// Delete for me works offline and removes the media.
	if err := b.Delete(ctx, media.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := b.db.GetStory(ctx, media.ID); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("deleted story still there: %v", err)
	}
	if _, err := b.db.GetAttachment(ctx, media.AttID); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("deleted story media still there: %v", err)
	}
	// Someone else's story can't be deleted for everyone.
	mustHandle(t, b, storyEvent(aliceUUID, now+2, textStory("theirs", true)))
	if err := b.Delete(ctx, core.MessageID{Chat: core.DirectChatID(aliceACI), Author: aliceACI, Timestamp: now + 2}, true); !errors.Is(err, core.ErrReadOnly) {
		t.Errorf("deleting someone else's story: %v", err)
	}
}

func TestStoryRecipientUpdateWithoutListsDeletes(t *testing.T) {
	b, clock := newTestBackend(t)
	ctx := context.Background()
	now := uint64(clock.Now().UnixMilli())
	list := signalmeow.MyStoryID.String()
	own := &events.Story{Info: events.MessageInfo{Sender: selfUUID}, Timestamp: now, Story: textStory("mine", true), IsSync: true,
		Recipients: []events.StoryRecipient{
			{ServiceID: aliceUUID.String(), DistributionListIDs: []string{list}, AllowedToReply: true},
			{ServiceID: bobUUID.String(), DistributionListIDs: []string{list}},
		}}
	mustHandle(t, b, own)
	id := core.MessageID{Author: selfACI, Timestamp: now}
	var meta storyMeta
	if ok, _ := b.kv.get(ctx, storyKey(id), &meta); !ok || meta.Recipients != 2 || len(meta.Sent) != 2 || !meta.Sent[0].Reply {
		t.Fatalf("meta %+v", meta)
	}
	// Bob loses access.
	mustHandle(t, b, &events.StoryRecipientUpdate{Timestamp: now, Recipients: []events.StoryRecipient{
		{ServiceID: aliceUUID.String(), DistributionListIDs: []string{list}},
		{ServiceID: bobUUID.String()},
	}})
	meta = storyMeta{}
	if _, _ = b.kv.get(ctx, storyKey(id), &meta); meta.Recipients != 1 || meta.Sent[0].ACI != aliceACI {
		t.Errorf("after update %+v", meta)
	}
	// Signal-Desktop's delete-for-everyone sync: every recipient listed,
	// none with a distribution list left.
	mustHandle(t, b, &events.StoryRecipientUpdate{Timestamp: now, Recipients: []events.StoryRecipient{
		{ServiceID: aliceUUID.String()}, {ServiceID: bobUUID.String()},
	}})
	if _, err := b.db.GetStory(ctx, id); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("story survived delete sync: %v", err)
	}
}

// Signal-Desktop onStoryRecipientUpdate: a list story only we can still see
// is deleted; recipient updates never touch group stories.
func TestStoryRecipientUpdateSelfOnlyAndGroup(t *testing.T) {
	b, clock := newTestBackend(t)
	ctx := context.Background()
	now := uint64(clock.Now().UnixMilli())
	list := signalmeow.MyStoryID.String()
	mustHandle(t, b, &events.Story{Info: events.MessageInfo{Sender: selfUUID}, Timestamp: now, Story: textStory("mine", true), IsSync: true,
		Recipients: []events.StoryRecipient{{ServiceID: aliceUUID.String(), DistributionListIDs: []string{list}}}})
	id := core.MessageID{Author: selfACI, Timestamp: now}
	mustHandle(t, b, &events.StoryRecipientUpdate{Timestamp: now, Recipients: []events.StoryRecipient{
		{ServiceID: selfUUID.String(), DistributionListIDs: []string{list}},
		{ServiceID: aliceUUID.String()},
	}})
	if _, err := b.db.GetStory(ctx, id); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("story visible only to us survived: %v", err)
	}

	groupWithMembers(t, b, selfACI, aliceACI)
	gid := types.GroupIdentifier(groupChat.GroupID())
	gs := &events.Story{Info: events.MessageInfo{Sender: selfUUID, ChatID: string(gid)}, Timestamp: now - 1, Story: textStory("group", true),
		GroupID: gid, IsSync: true, Recipients: []events.StoryRecipient{{ServiceID: aliceUUID.String(), AllowedToReply: true}}}
	mustHandle(t, b, gs)
	gsID := core.MessageID{Author: selfACI, Timestamp: now - 1}
	mustHandle(t, b, &events.StoryRecipientUpdate{Timestamp: now - 1, Recipients: []events.StoryRecipient{{ServiceID: aliceUUID.String()}}})
	if _, err := b.db.GetStory(ctx, gsID); err != nil {
		t.Errorf("group story deleted by a recipient update: %v", err)
	}
}
