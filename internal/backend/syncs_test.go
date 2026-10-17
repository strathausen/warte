package backend

import (
	"context"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-signal/pkg/signalmeow/events"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/types"

	"warte/internal/core"
)

func TestBlockedDiff(t *testing.T) {
	g2 := core.GroupChatID("Z3JvdXAy")
	block, unblock := blockedDiff([]core.ACI{aliceACI, bobACI}, []core.ChatID{groupChat}, []core.ACI{bobACI, selfACI}, []core.ChatID{g2})
	if !reflect.DeepEqual(block, []core.ChatID{aliceChat, groupChat}) {
		t.Errorf("block %v", block)
	}
	if !reflect.DeepEqual(unblock, []core.ChatID{core.DirectChatID(selfACI), g2}) {
		t.Errorf("unblock %v", unblock)
	}
	if b, u := blockedDiff(nil, nil, nil, nil); len(b)+len(u) != 0 {
		t.Error("empty")
	}
}

func TestBlockedSync(t *testing.T) {
	b, _ := newTestBackend(t)
	ctx := context.Background()
	mustHandle(t, b, chatEvent(aliceUUID, aliceUUID.String(), textMsg(100, "hi")))
	groupWithMembers(t, b, selfACI, aliceACI)
	drain(t, b)
	mustHandle(t, b, &events.BlockedSync{ACIs: []uuid.UUID{aliceUUID}, GroupIDs: []types.GroupIdentifier{types.GroupIdentifier(groupChat.GroupID())}})
	if c, _ := b.Chat(ctx, aliceChat); !c.Blocked {
		t.Error("chat not blocked")
	}
	if c, _ := b.Contact(ctx, aliceACI); !c.Blocked {
		t.Error("contact not blocked")
	}
	if c, _ := b.Chat(ctx, groupChat); !c.Blocked {
		t.Error("group not blocked")
	}
	evs := drain(t, b)
	if len(eventsOf[core.ChatUpdated](evs)) < 2 || len(eventsOf[core.ContactUpdated](evs)) != 1 {
		t.Errorf("events %+v", evs)
	}
	// The list is complete: an empty one unblocks everything.
	mustHandle(t, b, &events.BlockedSync{})
	if c, _ := b.Chat(ctx, aliceChat); c.Blocked {
		t.Error("chat still blocked")
	}
	if c, _ := b.Contact(ctx, aliceACI); c.Blocked {
		t.Error("contact still blocked")
	}
	if c, _ := b.Chat(ctx, groupChat); c.Blocked {
		t.Error("group still blocked")
	}
	// Phone numbers are matched to known contacts.
	bob, _ := b.Contact(ctx, bobACI)
	bob.E164 = "+15550123"
	if err := b.db.UpsertContact(ctx, bob); err != nil {
		t.Fatal(err)
	}
	mustHandle(t, b, &events.BlockedSync{E164s: []string{"+15550123", "+19999"}})
	if c, _ := b.Contact(ctx, bobACI); !c.Blocked {
		t.Error("contact blocked by number not blocked")
	}
}

func TestConfigurationSync(t *testing.T) {
	s := core.Settings{ReadReceipts: true, TypingIndicators: true, LinkPreviews: true}
	if applyConfiguration(&s, &events.ConfigurationSync{}) {
		t.Error("empty configuration changed settings")
	}
	if !applyConfiguration(&s, &events.ConfigurationSync{ReadReceipts: proto.Bool(false), UnidentifiedDeliveryIndicators: proto.Bool(true)}) ||
		s.ReadReceipts || !s.SealedSenderNotice || !s.TypingIndicators || !s.LinkPreviews {
		t.Errorf("settings %+v", s)
	}

	b, _ := newTestBackend(t)
	ctx := context.Background()
	mustHandle(t, b, &events.ConfigurationSync{TypingIndicators: proto.Bool(false), LinkPreviews: proto.Bool(false)})
	got, _ := b.Settings(ctx)
	if got.TypingIndicators || got.LinkPreviews || !got.ReadReceipts {
		t.Errorf("stored settings %+v", got)
	}
}

func TestStickerPackSync(t *testing.T) {
	b, _ := newTestBackend(t)
	ctx := context.Background()
	id := make([]byte, 16)
	key := make([]byte, 32)
	id[0], key[0] = 1, 2
	idHex := "01000000000000000000000000000000"
	// A known pack (with stickers) is installed without a download.
	if err := b.db.PutStickerPack(ctx, core.StickerPack{ID: idHex, Stickers: []core.Sticker{{PackID: idHex, StickerID: 1}}}); err != nil {
		t.Fatal(err)
	}
	mustHandle(t, b, &events.StickerPackSync{Operations: []events.StickerPackOperation{{PackID: id, PackKey: key, Install: true}}})
	if p, err := b.db.GetStickerPack(ctx, idHex); err != nil || !p.Installed {
		t.Errorf("install %+v %v", p, err)
	}
	mustHandle(t, b, &events.StickerPackSync{Operations: []events.StickerPackOperation{{PackID: id, Install: false}}})
	if p, _ := b.db.GetStickerPack(ctx, idHex); p.Installed {
		t.Error("remove")
	}
	// Removing an unknown pack is fine.
	mustHandle(t, b, &events.StickerPackSync{Operations: []events.StickerPackOperation{{PackID: make([]byte, 16)}}})
}

func TestKeysFetchLatestAndNumberChange(t *testing.T) {
	b, _ := newTestBackend(t)
	ctx := context.Background()
	_ = b.kv.set(ctx, "storage_version", uint64(7))
	mustHandle(t, b, &events.KeysSync{HasAccountEntropyPool: true, MasterKeyChanged: true})
	if len(b.storageKick) != 1 {
		t.Error("keys sync did not kick a storage read")
	}
	var v uint64
	if ok, _ := b.kv.get(ctx, "storage_version", &v); ok {
		t.Error("storage version survived a master key change")
	}
	<-b.storageKick
	mustHandle(t, b, &events.FetchLatest{Type: signalpb.SyncMessage_FetchLatest_STORAGE_MANIFEST})
	if len(b.storageKick) != 1 {
		t.Error("fetch latest storage did not kick a storage read")
	}
	mustHandle(t, b, &events.FetchLatest{Type: signalpb.SyncMessage_FetchLatest_LOCAL_PROFILE})
	select {
	case aci := <-b.profiles:
		if aci != selfACI {
			t.Errorf("profile refresh for %v", aci)
		}
	default:
		t.Error("fetch latest profile did not queue our profile")
	}
	mustHandle(t, b, &events.PniChangeNumber{NewE164: "+4915112345"})
	if b.Self().E164 != "+4915112345" {
		t.Errorf("self %+v", b.Self())
	}
	if c, _ := b.Contact(ctx, selfACI); c.E164 != "+4915112345" {
		t.Errorf("self contact %+v", c)
	}
}

func TestConversationDeleteSync(t *testing.T) {
	b, _ := newTestBackend(t)
	ctx := context.Background()
	for ts := uint64(100); ts <= 103; ts++ {
		mustHandle(t, b, chatEvent(aliceUUID, aliceUUID.String(), textMsg(ts, "m")))
	}
	conv := &signalpb.ConversationIdentifier{Identifier: &signalpb.ConversationIdentifier_ThreadServiceIdBinary{ThreadServiceIdBinary: aliceUUID[:]}}
	addr := func(ts uint64) *signalpb.AddressableMessage {
		return &signalpb.AddressableMessage{Author: &signalpb.AddressableMessage_AuthorServiceIdBinary{AuthorServiceIdBinary: aliceUUID[:]}, SentTimestamp: proto.Uint64(ts)}
	}
	// The phone knew messages up to 102; 103 arrived since and survives,
	// so even a full delete keeps the chat.
	mustHandle(t, b, &events.DeleteForMe{SyncMessage_DeleteForMe: &signalpb.SyncMessage_DeleteForMe{ConversationDeletes: []*signalpb.SyncMessage_DeleteForMe_ConversationDelete{{
		Conversation: conv, MostRecentMessages: []*signalpb.AddressableMessage{addr(101), addr(102), addr(999)}, IsFullDelete: proto.Bool(true),
	}}}})
	msgs, _ := b.Messages(ctx, aliceChat, core.MessageID{}, 10)
	if len(msgs) != 1 || msgs[0].ID.Timestamp != 103 {
		t.Fatalf("left %+v", msgs)
	}
	if _, err := b.Chat(ctx, aliceChat); err != nil {
		t.Error("chat with newer messages deleted")
	}
	mustHandle(t, b, &events.DeleteForMe{SyncMessage_DeleteForMe: &signalpb.SyncMessage_DeleteForMe{ConversationDeletes: []*signalpb.SyncMessage_DeleteForMe_ConversationDelete{{
		Conversation: conv, MostRecentMessages: []*signalpb.AddressableMessage{addr(103)}, IsFullDelete: proto.Bool(true),
	}}}})
	if _, err := b.Chat(ctx, aliceChat); err == nil {
		t.Error("fully deleted chat survives")
	}
}
