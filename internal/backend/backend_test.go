package backend

import (
	"bytes"
	"context"
	"errors"
	"image"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-signal/pkg/libsignalgo"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/events"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/types"

	"warte/internal/core"
	"warte/internal/store"
)

// Compile-time contract checks.
var (
	_ core.Service = (*Backend)(nil)
	_ core.Linker  = (*Linker)(nil)
)

// chatEvent builds an incoming ChatEvent.
func chatEvent(sender uuid.UUID, chat string, content signalpb.ChatEventContent) *events.ChatEvent {
	return &events.ChatEvent{
		Info:  events.MessageInfo{Sender: sender, ChatID: chat, ServerTimestamp: 1, ServerGUID: uuid.NewString(), ReportingToken: []byte("tok")},
		Event: content,
	}
}

func textMsg(ts uint64, body string) *signalpb.DataMessage {
	return &signalpb.DataMessage{Timestamp: proto.Uint64(ts), Body: proto.String(body)}
}

func mustHandle(t *testing.T, b *Backend, evt events.SignalEvent) {
	t.Helper()
	if !b.handleSignalEvent(evt) {
		t.Fatalf("handler rejected %T", evt)
	}
}

func mustMessage(t *testing.T, b *Backend, id core.MessageID) core.Message {
	t.Helper()
	m, err := b.Message(context.Background(), id)
	if err != nil {
		t.Fatalf("message %v: %v", id, err)
	}
	return m
}

func TestNewNeedsStorage(t *testing.T) {
	if _, err := New(context.Background(), Options{Logger: zerolog.Nop()}); err == nil {
		t.Error("New without storage must fail")
	}
	if _, err := New(context.Background(), Options{DataDir: t.TempDir(), DBKey: testKey(t), Logger: zerolog.Nop()}); err == nil {
		t.Error("New without blob key must fail")
	}
}

func TestStartWithoutDevice(t *testing.T) {
	b, _ := newTestBackend(t)
	if err := b.Start(context.Background()); !errors.Is(err, core.ErrNotLoggedIn) {
		t.Fatalf("Start = %v, want ErrNotLoggedIn", err)
	}
	if b.ConnState() != core.ConnDisconnected {
		t.Errorf("conn %v", b.ConnState())
	}
	if _, err := b.Send(context.Background(), aliceChat, core.Outgoing{Body: "x"}); !errors.Is(err, core.ErrNotLoggedIn) {
		t.Errorf("Send = %v", err)
	}
}

func TestCloseClosesEvents(t *testing.T) {
	b, _ := newTestBackend(t)
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal("Close must be idempotent")
	}
	select {
	case _, ok := <-b.Events():
		if ok {
			t.Error("events channel should be closed")
		}
	case <-time.After(time.Second):
		t.Error("events channel not closed")
	}
	if err := b.Start(context.Background()); !errors.Is(err, ErrClosed) {
		t.Errorf("Start after Close = %v", err)
	}
}

func TestSharedStoreIsNotClosed(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "warte.db"), testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	blobs, err := store.OpenBlobs(filepath.Join(dir, "blobs"), testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(context.Background(), Options{Store: db, Blobs: blobs, Logger: zerolog.Nop()})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ListContacts(context.Background()); err != nil {
		t.Errorf("caller's store was closed: %v", err)
	}
}

func TestMsgIndexAndKV(t *testing.T) {
	b, _ := newTestBackend(t)
	ctx := context.Background()
	id := core.MessageID{Chat: aliceChat, Author: aliceACI, Timestamp: 100}
	if err := b.index.put(ctx, id, 0, "guid-1", []byte("t1")); err != nil {
		t.Fatal(err)
	}
	if err := b.index.put(ctx, id, 150, "", nil); err != nil {
		t.Fatal(err)
	}
	if err := b.index.put(ctx, id, 120, "", nil); err != nil {
		t.Fatal(err)
	}
	for _, ts := range []uint64{100, 120, 150} {
		got, ok, err := b.index.lookup(ctx, aliceACI, ts)
		if err != nil || !ok || got != id {
			t.Errorf("lookup %d = %v %v %v", ts, got, ok, err)
		}
	}
	if _, ok, _ := b.index.lookup(ctx, bobACI, 100); ok {
		t.Error("author is part of the key")
	}
	if last, _ := b.index.lastEdit(ctx, id); last != 150 {
		t.Errorf("last edit %d", last)
	}
	guid, token, err := b.index.spamReport(ctx, aliceChat, aliceACI)
	if err != nil || guid != "guid-1" || string(token) != "t1" {
		t.Errorf("spam report %q %q %v", guid, token, err)
	}
	// Re-indexing without a GUID keeps the stored one.
	_ = b.index.put(ctx, id, 0, "", nil)
	if guid, _, _ := b.index.spamReport(ctx, aliceChat, aliceACI); guid != "guid-1" {
		t.Error("guid lost on re-index")
	}
	if err := b.index.forget(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := b.index.lookup(ctx, aliceACI, 150); ok {
		t.Error("forget must drop aliases")
	}
	_ = b.index.put(ctx, id, 0, "", nil)
	_ = b.index.forgetChat(ctx, aliceChat)
	if _, ok, _ := b.index.lookup(ctx, aliceACI, 100); ok {
		t.Error("forgetChat")
	}

	var v map[string]int
	if ok, err := b.kv.get(ctx, "missing", &v); ok || err != nil {
		t.Errorf("missing key: %v %v", ok, err)
	}
	if err := b.kv.set(ctx, "k", map[string]int{"a": 1}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := b.kv.get(ctx, "k", &v); !ok || v["a"] != 1 {
		t.Errorf("kv %v", v)
	}
	_ = b.kv.del(ctx, "k")
	if ok, _ := b.kv.get(ctx, "k", &v); ok {
		t.Error("kv delete")
	}
}

func TestIncomingTextFlow(t *testing.T) {
	b, _ := newTestBackend(t)
	ctx := context.Background()
	mustHandle(t, b, chatEvent(aliceUUID, aliceUUID.String(), textMsg(1000, "hello")))
	id := core.MessageID{Chat: aliceChat, Author: aliceACI, Timestamp: 1000}
	m := mustMessage(t, b, id)
	if m.Body != "hello" || m.Outgoing || m.SenderName != "Alice Chen" {
		t.Errorf("stored %+v", m)
	}
	evs := drain(t, b)
	added := eventsOf[core.MessageAdded](evs)
	if len(added) != 1 || !added[0].Notify || added[0].Message.ID != id {
		t.Errorf("MessageAdded %+v", added)
	}
	if len(eventsOf[core.ChatUpdated](evs)) == 0 {
		t.Error("no ChatUpdated")
	}
	c, err := b.Chat(ctx, aliceChat)
	if err != nil || c.Unread != 1 || c.Kind != core.ChatDirect || c.Title != "Alice Chen" {
		t.Errorf("chat %+v %v", c, err)
	}
	// Duplicate delivery is ignored.
	mustHandle(t, b, chatEvent(aliceUUID, aliceUUID.String(), textMsg(1000, "hello again")))
	if mustMessage(t, b, id).Body != "hello" {
		t.Error("duplicate overwrote message")
	}
	if got, ok, _ := b.index.lookup(ctx, aliceACI, 1000); !ok || got != id {
		t.Error("message not indexed")
	}
	// A muted chat doesn't notify unless we are mentioned.
	until := time.Now().Add(time.Hour)
	if err := b.UpdateChat(ctx, aliceChat, core.ChatUpdate{MutedUntil: &until}); err != nil {
		t.Fatal(err)
	}
	drain(t, b)
	mustHandle(t, b, chatEvent(aliceUUID, aliceUUID.String(), textMsg(1001, "quiet")))
	mention := &signalpb.DataMessage{Timestamp: proto.Uint64(1002), Body: proto.String("￼ look"),
		BodyRanges: []*signalpb.BodyRange{{Start: proto.Uint32(0), Length: proto.Uint32(1), AssociatedValue: &signalpb.BodyRange_MentionAci{MentionAci: string(selfACI)}}}}
	mustHandle(t, b, chatEvent(aliceUUID, aliceUUID.String(), mention))
	added = eventsOf[core.MessageAdded](drain(t, b))
	if len(added) != 2 || added[0].Notify || !added[1].Notify {
		t.Errorf("mute/mention notify: %+v", added)
	}
}

func TestSyncTranscriptAndReceipts(t *testing.T) {
	b, _ := newTestBackend(t)
	ctx := context.Background()
	// A message we sent from the phone arrives as a sync transcript.
	mustHandle(t, b, chatEvent(selfUUID, aliceUUID.String(), textMsg(2000, "sent from phone")))
	id := core.MessageID{Chat: aliceChat, Author: selfACI, Timestamp: 2000}
	m := mustMessage(t, b, id)
	if !m.Outgoing || m.Status != core.StatusSent {
		t.Errorf("transcript %+v", m)
	}
	if c, _ := b.Chat(ctx, aliceChat); c.Unread != 0 {
		t.Error("own messages are not unread")
	}
	if added := eventsOf[core.MessageAdded](drain(t, b)); len(added) != 1 || added[0].Notify {
		t.Errorf("own messages don't notify: %+v", added)
	}
	receipt := func(typ signalpb.ReceiptMessage_Type, from uuid.UUID) {
		mustHandle(t, b, &events.Receipt{Sender: from, Content: &signalpb.ReceiptMessage{Type: typ.Enum(), Timestamp: []uint64{2000, 99999}}})
	}
	receipt(signalpb.ReceiptMessage_DELIVERY, aliceUUID)
	if m := mustMessage(t, b, id); m.Status != core.StatusDelivered {
		t.Errorf("after delivery %v", m.Status)
	}
	receipt(signalpb.ReceiptMessage_READ, aliceUUID)
	if m := mustMessage(t, b, id); m.Status != core.StatusRead {
		t.Errorf("after read %v", m.Status)
	}
	receipt(signalpb.ReceiptMessage_DELIVERY, aliceUUID)
	if m := mustMessage(t, b, id); m.Status != core.StatusRead {
		t.Error("status regressed")
	}
	if upd := eventsOf[core.MessageUpdated](drain(t, b)); len(upd) < 2 {
		t.Errorf("updates %d", len(upd))
	}
}

func TestEditsReactionsDeletes(t *testing.T) {
	b, clock := newTestBackend(t)
	ctx := context.Background()
	mustHandle(t, b, chatEvent(aliceUUID, aliceUUID.String(), textMsg(3000, "v1")))
	id := core.MessageID{Chat: aliceChat, Author: aliceACI, Timestamp: 3000}
	edit := func(ts uint64, target uint64, body string) {
		mustHandle(t, b, chatEvent(aliceUUID, aliceUUID.String(), &signalpb.EditMessage{
			TargetSentTimestamp: proto.Uint64(target), DataMessage: textMsg(ts, body)}))
	}
	edit(3100, 3000, "v2")
	edit(3200, 3100, "v3") // targeting the previous edit resolves via the alias
	m := mustMessage(t, b, id)
	if m.Body != "v3" || !m.Edited || len(m.EditHistory) != 2 ||
		m.EditHistory[0].Body != "v1" || !m.EditHistory[0].At.Equal(msTime(3000)) ||
		m.EditHistory[1].Body != "v2" || !m.EditHistory[1].At.Equal(msTime(3100)) {
		t.Errorf("edit history %+v", m)
	}
	// Someone else can't edit Alice's message.
	mustHandle(t, b, chatEvent(bobUUID, aliceUUID.String(), &signalpb.EditMessage{TargetSentTimestamp: proto.Uint64(3000), DataMessage: textMsg(3300, "hacked")}))
	if mustMessage(t, b, id).Body != "v3" {
		t.Error("foreign edit applied")
	}

	react := func(from uuid.UUID, ts uint64, emoji string, remove bool) {
		mustHandle(t, b, chatEvent(from, aliceUUID.String(), &signalpb.DataMessage{Timestamp: proto.Uint64(ts),
			Reaction: &signalpb.DataMessage_Reaction{Emoji: proto.String(emoji), Remove: proto.Bool(remove),
				TargetAuthorAciBinary: aliceUUID[:], TargetSentTimestamp: proto.Uint64(3000)}}))
	}
	react(bobUUID, 3400, "👍", false)
	react(selfUUID, 3401, "❤️", false)
	react(bobUUID, 3402, "😂", false)
	m = mustMessage(t, b, id)
	if len(m.Reactions) != 2 || m.Reactions[1].Emoji != "😂" || m.Reactions[1].From != bobACI || m.Reactions[1].FromName != "Bob" {
		t.Errorf("reactions %+v", m.Reactions)
	}
	react(bobUUID, 3403, "👍", true) // stale removal of a replaced reaction
	if len(mustMessage(t, b, id).Reactions) != 2 {
		t.Error("stale removal applied")
	}
	react(bobUUID, 3404, "😂", true)
	if r := mustMessage(t, b, id).Reactions; len(r) != 1 || r[0].From != selfACI {
		t.Errorf("after removal %+v", r)
	}

	// Only the author may delete for everyone.
	del := func(from uuid.UUID, target uint64) {
		mustHandle(t, b, chatEvent(from, aliceUUID.String(), &signalpb.DataMessage{Timestamp: proto.Uint64(3500),
			Delete: &signalpb.DataMessage_Delete{TargetSentTimestamp: proto.Uint64(target)}}))
	}
	del(bobUUID, 3000)
	if mustMessage(t, b, id).RemoteDeleted {
		t.Error("foreign delete applied")
	}
	del(aliceUUID, 3000)
	if m := mustMessage(t, b, id); !m.RemoteDeleted || m.Body != "" || m.Reactions != nil || m.EditHistory != nil {
		t.Errorf("remote delete %+v", m)
	}

	// Delete for me works offline and removes the message.
	mustHandle(t, b, chatEvent(aliceUUID, aliceUUID.String(), textMsg(3600, "bye")))
	other := core.MessageID{Chat: aliceChat, Author: aliceACI, Timestamp: 3600}
	drain(t, b)
	if err := b.Delete(ctx, other, false); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Message(ctx, other); !errors.Is(err, core.ErrNotFound) {
		t.Error("message still there")
	}
	if rm := eventsOf[core.MessageRemoved](drain(t, b)); len(rm) != 1 || rm[0].ID != other {
		t.Errorf("removed events %+v", rm)
	}
	_ = clock
}

func TestReadSelfAndDisappearing(t *testing.T) {
	b, clock := newTestBackend(t)
	ctx := context.Background()
	dm := textMsg(4000, "secret")
	dm.ExpireTimer = proto.Uint32(60)
	mustHandle(t, b, chatEvent(aliceUUID, aliceUUID.String(), dm))
	mustHandle(t, b, chatEvent(aliceUUID, aliceUUID.String(), textMsg(4001, "plain")))
	id := core.MessageID{Chat: aliceChat, Author: aliceACI, Timestamp: 4000}
	if m := mustMessage(t, b, id); m.ExpiresIn != time.Minute || !m.ExpireStartedAt.IsZero() {
		t.Errorf("timer must wait for read: %+v", m)
	}
	if c, _ := b.Chat(ctx, aliceChat); c.Unread != 2 {
		t.Fatalf("unread %d", c.Unread)
	}
	// Read on the phone up to the second message.
	mustHandle(t, b, &events.ReadSelf{Messages: []*signalpb.SyncMessage_Read{{SenderAciBinary: aliceUUID[:], Timestamp: proto.Uint64(4001)}}})
	if c, _ := b.Chat(ctx, aliceChat); c.Unread != 0 {
		t.Errorf("unread after read sync %d", c.Unread)
	}
	m := mustMessage(t, b, id)
	if m.ExpireStartedAt.IsZero() {
		t.Fatal("reading must start the timer")
	}
	drain(t, b)
	if next := b.sweepExpired(ctx); next.IsZero() {
		t.Error("sweep should report the pending expiry")
	}
	clock.Advance(2 * time.Minute)
	b.sweepExpired(ctx)
	if _, err := b.Message(ctx, id); !errors.Is(err, core.ErrNotFound) {
		t.Error("expired message still there")
	}
	if rm := eventsOf[core.MessageRemoved](drain(t, b)); len(rm) != 1 || rm[0].ID != id {
		t.Errorf("removal events %+v", rm)
	}
}

func TestTimerUpdateAndVersions(t *testing.T) {
	b, _ := newTestBackend(t)
	ctx := context.Background()
	timer := func(ts uint64, secs, version uint32) {
		dm := &signalpb.DataMessage{Timestamp: proto.Uint64(ts), Flags: proto.Uint32(uint32(signalpb.DataMessage_EXPIRATION_TIMER_UPDATE)),
			ExpireTimer: proto.Uint32(secs), ExpireTimerVersion: proto.Uint32(version)}
		mustHandle(t, b, chatEvent(aliceUUID, aliceUUID.String(), dm))
	}
	timer(5000, 3600, 2)
	c, _ := b.Chat(ctx, aliceChat)
	if c.ExpireTimer != time.Hour {
		t.Errorf("timer %v", c.ExpireTimer)
	}
	m := mustMessage(t, b, core.MessageID{Chat: aliceChat, Author: aliceACI, Timestamp: 5000})
	if m.Kind != core.KindSystem || m.SystemText != "Alice Chen set the disappearing message timer to 1 hour." {
		t.Errorf("system text %+v", m)
	}
	timer(5100, 60, 1) // outdated version
	if c, _ := b.Chat(ctx, aliceChat); c.ExpireTimer != time.Hour {
		t.Error("outdated timer applied")
	}
	// A newer-version message with a different timer updates silently.
	dm := textMsg(5200, "x")
	dm.ExpireTimer, dm.ExpireTimerVersion = proto.Uint32(0), proto.Uint32(3)
	mustHandle(t, b, chatEvent(aliceUUID, aliceUUID.String(), dm))
	if c, _ := b.Chat(ctx, aliceChat); c.ExpireTimer != 0 {
		t.Errorf("implicit timer update: %v", c.ExpireTimer)
	}
}

func TestTyping(t *testing.T) {
	b, _ := newTestBackend(t)
	ctx := context.Background()
	typing := func(action signalpb.TypingMessage_Action, gid []byte) {
		mustHandle(t, b, chatEvent(aliceUUID, aliceUUID.String(), &signalpb.TypingMessage{Timestamp: proto.Uint64(1), Action: action.Enum(), GroupId: gid}))
	}
	raw, _ := groupIDBytes(groupChat)
	typing(signalpb.TypingMessage_STARTED, nil)
	typing(signalpb.TypingMessage_STARTED, raw)
	typing(signalpb.TypingMessage_STOPPED, nil)
	tc := eventsOf[core.TypingChanged](drain(t, b))
	want := []core.TypingChanged{
		{Chat: aliceChat, Who: aliceACI, Name: "Alice Chen", Typing: true},
		{Chat: groupChat, Who: aliceACI, Name: "Alice Chen", Typing: true},
		{Chat: aliceChat, Who: aliceACI, Name: "Alice Chen", Typing: false},
	}
	if !reflect.DeepEqual(tc, want) {
		t.Errorf("typing events %+v", tc)
	}
	s, _ := b.Settings(ctx)
	s.TypingIndicators = false
	if err := b.UpdateSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	typing(signalpb.TypingMessage_STARTED, nil)
	if tc := eventsOf[core.TypingChanged](drain(t, b)); len(tc) != 0 {
		t.Error("typing shown although disabled")
	}
}

func TestPollsAndPins(t *testing.T) {
	b, clock := newTestBackend(t)
	ctx := context.Background()
	gid := groupChat.GroupID()
	mustHandle(t, b, chatEvent(aliceUUID, gid, &signalpb.DataMessage{Timestamp: proto.Uint64(6000),
		PollCreate: &signalpb.DataMessage_PollCreate{Question: proto.String("Lunch?"), Options: []string{"Pizza", "Sushi"}}}))
	poll := core.MessageID{Chat: groupChat, Author: aliceACI, Timestamp: 6000}
	if c, err := b.Chat(ctx, groupChat); err != nil || c.Kind != core.ChatGroup {
		t.Fatalf("group chat %+v %v", c, err)
	}
	vote := func(from uuid.UUID, ts uint64, opt uint32, count uint32) {
		mustHandle(t, b, chatEvent(from, gid, &signalpb.DataMessage{Timestamp: proto.Uint64(ts), PollVote: &signalpb.DataMessage_PollVote{
			TargetAuthorAciBinary: aliceUUID[:], TargetSentTimestamp: proto.Uint64(6000), OptionIndexes: []uint32{opt}, VoteCount: proto.Uint32(count)}}))
	}
	vote(bobUUID, 6100, 1, 2)
	vote(bobUUID, 6101, 0, 1) // older vote arriving late
	if p := mustMessage(t, b, poll).Poll; !reflect.DeepEqual(pollVotesOf(p, bobACI), []uint32{1}) {
		t.Errorf("votes %+v", p)
	}
	mustHandle(t, b, chatEvent(aliceUUID, gid, &signalpb.DataMessage{Timestamp: proto.Uint64(6200), PollTerminate: &signalpb.DataMessage_PollTerminate{TargetSentTimestamp: proto.Uint64(6000)}}))
	if !mustMessage(t, b, poll).Poll.Ended {
		t.Error("poll not ended")
	}
	vote(bobUUID, 6300, 0, 3)
	if p := mustMessage(t, b, poll).Poll; !reflect.DeepEqual(pollVotesOf(p, bobACI), []uint32{1}) {
		t.Error("vote on ended poll applied")
	}

	mustHandle(t, b, chatEvent(bobUUID, gid, &signalpb.DataMessage{Timestamp: proto.Uint64(6400), PinMessage: &signalpb.DataMessage_PinMessage{
		TargetAuthorAciBinary: aliceUUID[:], TargetSentTimestamp: proto.Uint64(6000),
		PinDuration: &signalpb.DataMessage_PinMessage_PinDurationSeconds{PinDurationSeconds: 60}}}))
	if !mustMessage(t, b, poll).Pinned {
		t.Fatal("not pinned")
	}
	if sys := mustMessage(t, b, core.MessageID{Chat: groupChat, Author: bobACI, Timestamp: 6400}); sys.SystemText != "Bob pinned a message." {
		t.Errorf("pin text %q", sys.SystemText)
	}
	clock.Advance(2 * time.Minute)
	b.sweepExpired(ctx)
	if mustMessage(t, b, poll).Pinned {
		t.Error("pin did not expire")
	}
	mustHandle(t, b, chatEvent(bobUUID, gid, &signalpb.DataMessage{Timestamp: proto.Uint64(6500), PinMessage: &signalpb.DataMessage_PinMessage{
		TargetAuthorAciBinary: aliceUUID[:], TargetSentTimestamp: proto.Uint64(6000),
		PinDuration: &signalpb.DataMessage_PinMessage_PinDurationForever{PinDurationForever: true}}}))
	mustHandle(t, b, chatEvent(bobUUID, gid, &signalpb.DataMessage{Timestamp: proto.Uint64(6600), UnpinMessage: &signalpb.DataMessage_UnpinMessage{
		TargetAuthorAciBinary: aliceUUID[:], TargetSentTimestamp: proto.Uint64(6000)}}))
	if mustMessage(t, b, poll).Pinned {
		t.Error("unpin")
	}
}

func TestAdminDeleteNeedsAdmin(t *testing.T) {
	b, _ := newTestBackend(t)
	ctx := context.Background()
	gid := groupChat.GroupID()
	mustHandle(t, b, chatEvent(aliceUUID, gid, textMsg(7000, "spam")))
	id := core.MessageID{Chat: groupChat, Author: aliceACI, Timestamp: 7000}
	adminDelete := func() {
		mustHandle(t, b, chatEvent(bobUUID, gid, &signalpb.DataMessage{Timestamp: proto.Uint64(7100),
			AdminDelete: &signalpb.DataMessage_AdminDelete{TargetAuthorAciBinary: aliceUUID[:], TargetSentTimestamp: proto.Uint64(7000)}}))
	}
	adminDelete()
	if mustMessage(t, b, id).RemoteDeleted {
		t.Fatal("non-admin delete applied")
	}
	if _, err := b.db.UpdateChat(ctx, groupChat, func(c *core.Chat) error {
		c.Members = []core.Member{{ACI: bobACI, Admin: true}, {ACI: aliceACI}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	adminDelete()
	if !mustMessage(t, b, id).RemoteDeleted {
		t.Error("admin delete not applied")
	}
}

func TestSystemEvents(t *testing.T) {
	b, _ := newTestBackend(t)
	ctx := context.Background()
	gid := groupChat.GroupID()

	// Group creation notice.
	mustHandle(t, b, chatEvent(aliceUUID, gid, &signalpb.DataMessage{Timestamp: proto.Uint64(8000), GroupV2: &signalpb.GroupContextV2{Revision: proto.Uint32(0)}}))
	if m := mustMessage(t, b, core.MessageID{Chat: groupChat, Author: aliceACI, Timestamp: 8000}); m.SystemText != "Alice Chen added you to the group." {
		t.Errorf("created %q", m.SystemText)
	}
	// Unsupported content.
	mustHandle(t, b, chatEvent(aliceUUID, aliceUUID.String(), &signalpb.DataMessage{Timestamp: proto.Uint64(8100), RequiredProtocolVersion: proto.Uint32(99)}))
	if m := mustMessage(t, b, core.MessageID{Chat: aliceChat, Author: aliceACI, Timestamp: 8100}); m.Kind != core.KindSystem || m.SystemText != unsupportedText {
		t.Errorf("unsupported %+v", m)
	}
	// Decryption error.
	mustHandle(t, b, &events.DecryptionError{Sender: bobUUID, Timestamp: 8200, Err: errors.New("x")})
	if m := mustMessage(t, b, core.MessageID{Chat: core.DirectChatID(bobACI), Author: bobACI, Timestamp: 8200}); m.Kind != core.KindSystem {
		t.Errorf("decryption error %+v", m)
	}
	// Identity change: verified flag cleared, system message, event.
	c, _ := b.Contact(ctx, aliceACI)
	c.Verified = true
	_ = b.db.UpsertContact(ctx, c)
	drain(t, b)
	mustHandle(t, b, &events.IdentityChange{ServiceID: libsignalgo.NewACIServiceID(aliceUUID)})
	if c, _ := b.Contact(ctx, aliceACI); c.Verified {
		t.Error("verification survived identity change")
	}
	evs := drain(t, b)
	if ic := eventsOf[core.IdentityChanged](evs); len(ic) != 1 || ic[0].ACI != aliceACI {
		t.Errorf("identity events %+v", ic)
	}
	msgs, _ := b.Messages(ctx, aliceChat, core.MessageID{}, 10)
	if last := msgs[len(msgs)-1]; last.SystemText != safetyNumberChangedText("Alice Chen") {
		t.Errorf("safety number notice %q", last.SystemText)
	}
	// Story reaction becomes a story reply.
	mustHandle(t, b, chatEvent(aliceUUID, aliceUUID.String(), &signalpb.DataMessage{Timestamp: proto.Uint64(8300),
		Reaction:     &signalpb.DataMessage_Reaction{Emoji: proto.String("🔥"), TargetAuthorAciBinary: selfUUID[:], TargetSentTimestamp: proto.Uint64(10)},
		StoryContext: &signalpb.DataMessage_StoryContext{AuthorAciBinary: selfUUID[:], SentTimestamp: proto.Uint64(10)}}))
	if m := mustMessage(t, b, core.MessageID{Chat: aliceChat, Author: aliceACI, Timestamp: 8300}); m.Kind != core.KindStoryReply || m.StoryContext.Emoji != "🔥" {
		t.Errorf("story reaction %+v", m)
	}
}

func TestCalls(t *testing.T) {
	b, _ := newTestBackend(t)
	ctx := context.Background()
	mustHandle(t, b, &events.Call{Info: events.MessageInfo{Sender: aliceUUID, ChatID: aliceUUID.String()}, Timestamp: 9000, IsRinging: true})
	evs := drain(t, b)
	if ic := eventsOf[core.IncomingCall](evs); len(ic) != 1 || !ic[0].Ringing || ic[0].From != aliceACI || ic[0].Name != "Alice Chen" {
		t.Errorf("ringing %+v", ic)
	}
	if added := eventsOf[core.MessageAdded](evs); len(added) != 1 || !added[0].Notify || added[0].Message.Kind != core.KindCall {
		t.Errorf("call message %+v", added)
	}
	mustHandle(t, b, &events.Call{Info: events.MessageInfo{Sender: aliceUUID, ChatID: aliceUUID.String()}, Timestamp: 9100})
	if ic := eventsOf[core.IncomingCall](drain(t, b)); len(ic) != 1 || ic[0].Ringing {
		t.Errorf("ended %+v", ic)
	}
	m := mustMessage(t, b, core.MessageID{Chat: aliceChat, Author: aliceACI, Timestamp: 9000})
	if m.Call == nil || !m.Call.Missed {
		t.Errorf("missed call %+v", m.Call)
	}
	log, err := b.CallLog(ctx)
	if err != nil || len(log) != 1 || !log[0].Call.Missed || !log[0].Unread {
		t.Errorf("call log %+v %v", log, err)
	}
	// Answered on the phone: our own device hangs up.
	mustHandle(t, b, &events.Call{Info: events.MessageInfo{Sender: bobUUID, ChatID: bobUUID.String()}, Timestamp: 9200, IsRinging: true})
	mustHandle(t, b, &events.Call{Info: events.MessageInfo{Sender: selfUUID, ChatID: selfUUID.String()}, Timestamp: 9300})
	if m := mustMessage(t, b, core.MessageID{Chat: core.DirectChatID(bobACI), Author: bobACI, Timestamp: 9200}); m.Call.Missed {
		t.Error("call answered elsewhere marked missed")
	}
}

func TestSyncDeletesAndRequests(t *testing.T) {
	b, _ := newTestBackend(t)
	ctx := context.Background()
	mustHandle(t, b, chatEvent(aliceUUID, aliceUUID.String(), textMsg(10000, "one")))
	mustHandle(t, b, chatEvent(aliceUUID, aliceUUID.String(), textMsg(10001, "two")))
	sm, _ := syncDeleteMessages(aliceChat, []core.MessageID{{Chat: aliceChat, Author: aliceACI, Timestamp: 10000}})
	mustHandle(t, b, &events.DeleteForMe{SyncMessage_DeleteForMe: sm.GetDeleteForMe()})
	if _, err := b.Message(ctx, core.MessageID{Chat: aliceChat, Author: aliceACI, Timestamp: 10000}); !errors.Is(err, core.ErrNotFound) {
		t.Error("synced delete not applied")
	}
	if _, err := b.db.UpdateChat(ctx, aliceChat, func(c *core.Chat) error { c.MessageRequest = true; return nil }); err != nil {
		t.Fatal(err)
	}
	mustHandle(t, b, &events.MessageRequestResponse{ThreadACI: aliceUUID, Type: signalpb.SyncMessage_MessageRequestResponse_ACCEPT})
	if c, _ := b.Chat(ctx, aliceChat); c.MessageRequest {
		t.Error("accept not applied")
	}
	mustHandle(t, b, &events.MessageRequestResponse{ThreadACI: aliceUUID, Type: signalpb.SyncMessage_MessageRequestResponse_BLOCK})
	if c, _ := b.Chat(ctx, aliceChat); !c.Blocked {
		t.Error("block not applied")
	}
	if c, _ := b.Contact(ctx, aliceACI); !c.Blocked {
		t.Error("contact not blocked")
	}
	full, _ := syncDeleteConversation(aliceChat, []core.MessageID{{Chat: aliceChat, Author: aliceACI, Timestamp: 10001}})
	drain(t, b)
	mustHandle(t, b, &events.DeleteForMe{SyncMessage_DeleteForMe: full.GetDeleteForMe()})
	if _, err := b.Chat(ctx, aliceChat); !errors.Is(err, core.ErrNotFound) {
		t.Error("conversation delete not applied")
	}
	if rm := eventsOf[core.ChatRemoved](drain(t, b)); len(rm) != 1 {
		t.Errorf("chat removed events %+v", rm)
	}
}

func TestContactListAndAvatars(t *testing.T) {
	b, _ := newTestBackend(t)
	ctx := context.Background()
	newACI := uuid.New()
	avatar := encodePNG(t, image.NewRGBA(image.Rect(0, 0, 4, 4)))
	mustHandle(t, b, &events.ContactList{Contacts: []*types.Recipient{
		{ACI: newACI, E164: "+15550100", ContactName: "Carol", ContactAvatar: types.ContactAvatar{Image: avatar, Hash: "hash1", ContentType: "image/png"}},
		{ACI: uuid.Nil, ContactName: "skipped"},
	}})
	c, err := b.Contact(ctx, aciOf(newACI))
	if err != nil || c.ContactName != "Carol" || c.E164 != "+15550100" || c.AvatarID == "" {
		t.Fatalf("contact %+v %v", c, err)
	}
	evs := drain(t, b)
	if cu := eventsOf[core.ContactUpdated](evs); len(cu) != 1 {
		t.Errorf("contact events %+v", cu)
	}
	if sp := eventsOf[core.SyncProgress](evs); len(sp) != 1 || sp[0].Stage != "contacts" {
		t.Errorf("progress %+v", sp)
	}
	got, err := b.Avatar(ctx, c.AvatarID)
	if err != nil || !bytes.Equal(got, avatar) {
		t.Errorf("avatar %d bytes, %v", len(got), err)
	}
	if _, err := b.Avatar(ctx, "nope"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("unknown avatar: %v", err)
	}
}

func TestLocalActionsOffline(t *testing.T) {
	b, _ := newTestBackend(t)
	ctx := context.Background()
	mustHandle(t, b, chatEvent(aliceUUID, aliceUUID.String(), textMsg(11000, "hi")))
	if err := b.SaveDraft(ctx, aliceChat, "draft text"); err != nil {
		t.Fatal(err)
	}
	if c, _ := b.Chat(ctx, aliceChat); c.Draft != "draft text" {
		t.Error("draft")
	}
	yes := true
	if err := b.UpdateChat(ctx, aliceChat, core.ChatUpdate{Archived: &yes}); err != nil {
		t.Fatal(err)
	}
	if c, _ := b.Chat(ctx, aliceChat); !c.Archived {
		t.Error("archive")
	}
	// New activity unarchives an unmuted chat.
	mustHandle(t, b, chatEvent(aliceUUID, aliceUUID.String(), textMsg(11001, "ping")))
	if c, _ := b.Chat(ctx, aliceChat); c.Archived {
		t.Error("incoming message should unarchive")
	}
	if err := b.MarkRead(ctx, aliceChat, core.MessageID{}); err != nil {
		t.Fatal(err)
	}
	if c, _ := b.Chat(ctx, aliceChat); c.Unread != 0 {
		t.Error("mark read")
	}
	if err := b.DeleteChat(ctx, aliceChat); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Chat(ctx, aliceChat); !errors.Is(err, core.ErrNotFound) {
		t.Error("delete chat")
	}
	if err := b.PostStory(ctx, core.OutgoingStory{}); !errors.Is(err, errEmptyMessage) {
		t.Errorf("empty story: %v, want errEmptyMessage", err)
	}
	if err := b.PostStory(ctx, core.OutgoingStory{Text: &core.TextStory{Text: "hi"}}); !errors.Is(err, core.ErrNotLoggedIn) {
		t.Errorf("story offline: %v, want ErrNotLoggedIn", err)
	}
	if err := b.UpdateProfile(ctx, core.ProfileUpdate{}); !errors.Is(err, core.ErrNotLoggedIn) {
		t.Errorf("profile offline: %v, want ErrNotLoggedIn", err)
	}
	for _, link := range []string{"https://example.com", "https://signal.group/#abc"} {
		if _, err := b.JoinGroupByLink(ctx, link); !errors.Is(err, errBadID) {
			t.Errorf("join %q: %v, want errBadID", link, err)
		}
	}
	if _, err := b.JoinGroupByLink(ctx, testInviteLink(t)); !errors.Is(err, core.ErrNotLoggedIn) {
		t.Errorf("join offline: %v, want ErrNotLoggedIn", err)
	}
	for name, err := range map[string]error{
		"react":   b.React(ctx, core.MessageID{Chat: aliceChat, Author: aliceACI, Timestamp: 1}, "x", false),
		"typing":  b.SetTyping(ctx, aliceChat, true),
		"block":   b.Block(ctx, aliceChat, true),
		"unlink":  b.Unlink(ctx),
		"install": b.InstallStickerPack(ctx, "a", "b", true),
	} {
		if !errors.Is(err, core.ErrNotLoggedIn) {
			t.Errorf("%s offline: %v, want ErrNotLoggedIn", name, err)
		}
	}
}

func TestAttachmentAccessAndViewOnce(t *testing.T) {
	b, _ := newTestBackend(t)
	ctx := context.Background()
	id := core.MessageID{Chat: aliceChat, Author: aliceACI, Timestamp: 12000}
	p := prepareUpload(core.OutgoingAttachment{FileName: "a.txt", Data: []byte("payload")})
	att, err := b.storeOutgoingAttachment(ctx, id, 0, p, roleBody)
	if err != nil {
		t.Fatal(err)
	}
	m := core.Message{ID: id, Kind: core.KindNormal, ViewOnce: true, Attachments: []core.Attachment{att}}
	if _, err := b.db.PutMessage(ctx, m); err != nil {
		t.Fatal(err)
	}
	data, err := b.Attachment(ctx, id, att.ID)
	if err != nil || string(data) != "payload" {
		t.Fatalf("attachment %q %v", data, err)
	}
	if _, err := b.Attachment(ctx, id, "other"); !errors.Is(err, core.ErrNotFound) {
		t.Error("foreign attachment id")
	}
	// No plaintext on disk.
	_ = filepath.WalkDir(b.blobs.Dir(), func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			raw, _ := os.ReadFile(path)
			if bytes.Contains(raw, []byte("payload")) {
				t.Errorf("plaintext in %s", path)
			}
		}
		return nil
	})
	if err := b.markViewOnceOpened(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Attachment(ctx, id, att.ID); !errors.Is(err, core.ErrReadOnly) {
		t.Errorf("opened view-once: %v", err)
	}
	if recs, _ := b.db.ListAttachments(ctx, store.MessageOwner(id)); len(recs) != 0 {
		t.Error("view-once attachment metadata kept")
	}
	if err := b.markViewOnceOpened(ctx, core.MessageID{Chat: aliceChat, Author: aliceACI, Timestamp: 1}); err == nil {
		t.Error("unknown message")
	}
}

func TestExportRoundTrip(t *testing.T) {
	b, _ := newTestBackend(t)
	ctx := context.Background()
	mustHandle(t, b, chatEvent(aliceUUID, aliceUUID.String(), textMsg(13000, "exported secret")))
	mustHandle(t, b, chatEvent(selfUUID, aliceUUID.String(), textMsg(13001, "reply")))
	path := filepath.Join(t.TempDir(), "backup.warte")
	if err := b.ExportBackup(ctx, path); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("export file mode %v %v", fi.Mode(), err)
	}
	raw, _ := os.ReadFile(path)
	if bytes.Contains(raw, []byte("exported secret")) {
		t.Error("plaintext in export")
	}
	doc, err := ReadExport(path, b.opts.BlobKey)
	if err != nil {
		t.Fatal(err)
	}
	var chat *ExportChat
	for i := range doc.Chats {
		if doc.Chats[i].Chat.ID == aliceChat {
			chat = &doc.Chats[i]
		}
	}
	if chat == nil || len(chat.Messages) != 2 || chat.Messages[0].Body != "exported secret" || chat.Messages[1].Body != "reply" {
		t.Fatalf("exported chat %+v", chat)
	}
	if len(doc.Contacts) < 3 || doc.Version != exportVersion {
		t.Errorf("contacts %d version %d", len(doc.Contacts), doc.Version)
	}
	if _, err := ReadExport(path, testKey(t)); err == nil {
		t.Error("wrong key must fail")
	}
	raw[len(raw)-1] ^= 1
	_ = os.WriteFile(path, raw, 0o600)
	if _, err := ReadExport(path, b.opts.BlobKey); err == nil {
		t.Error("tampered export must fail")
	}
	_ = os.WriteFile(path, []byte("nope"), 0o600)
	if _, err := ReadExport(path, b.opts.BlobKey); err == nil {
		t.Error("garbage must fail")
	}
	key := b.opts.BlobKey
	b.opts.BlobKey = nil
	if err := b.ExportBackup(ctx, path); !errors.Is(err, ErrNoExportKey) {
		t.Error("export without key")
	}
	b.opts.BlobKey = key
}

func TestNextTimestampMonotonic(t *testing.T) {
	b, _ := newTestBackend(t)
	prev := uint64(0)
	for range 100 {
		ts := b.nextTimestamp()
		if ts <= prev {
			t.Fatalf("timestamp %d after %d", ts, prev)
		}
		prev = ts
	}
}

func TestStickerImageCached(t *testing.T) {
	b, _ := newTestBackend(t)
	ctx := context.Background()
	key, err := b.blobs.Put([]byte("webp"))
	if err != nil {
		t.Fatal(err)
	}
	if err := b.db.PutAttachment(ctx, store.AttachmentRecord{ID: stickerRecordID("ab", 3), Owner: "sticker:ab", BlobKey: key, Downloaded: true}); err != nil {
		t.Fatal(err)
	}
	data, err := b.StickerImage(ctx, "AB", 3)
	if err != nil || string(data) != "webp" {
		t.Errorf("sticker %q %v", data, err)
	}
	if _, err := b.StickerImage(ctx, "cd", 1); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("unknown pack: %v", err)
	}
}
