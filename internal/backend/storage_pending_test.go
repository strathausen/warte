package backend

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/mautrix-signal/pkg/signalmeow"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/store"

	"warte/internal/core"
)

// newReopenable returns a function that opens backends over the same data
// directory, as a restart of warte would.
func newReopenable(t *testing.T) func(t *testing.T) *Backend {
	t.Helper()
	dir, dbKey, blobKey := t.TempDir(), testKey(t), testKey(t)
	clock := &testClock{now: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)}
	return func(t *testing.T) *Backend {
		t.Helper()
		b, err := New(context.Background(), Options{
			DataDir: dir, DBKey: dbKey, BlobKey: blobKey,
			Logger: zerolog.Nop(), Clock: clock.Now,
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		t.Cleanup(func() { _ = b.Close() })
		self := core.Contact{ACI: selfACI, ProfileName: "Me", IsSelf: true}
		_ = b.db.UpsertContact(context.Background(), self)
		b.mu.Lock()
		b.self = self
		b.mu.Unlock()
		return b
	}
}

// attachStorage wires b to an in-memory storage service, like
// newStorageTestEnv does for a fresh backend.
func attachStorage(b *Backend, mem *signalmeow.MemoryStorage) *signalmeow.Client {
	cli := &signalmeow.Client{
		Store: &store.Device{
			DeviceData:     store.DeviceData{MasterKey: storageTestMasterKey, DeviceID: 2, ACI: selfUUID},
			RecipientStore: fakeRecipients{},
		},
		Log:              zerolog.Nop(),
		StorageTransport: mem,
	}
	b.mu.Lock()
	b.cli = cli
	b.mu.Unlock()
	b.sw.notify = func(context.Context, *signalmeow.Client) error { return nil }
	return cli
}

func pendingSpecs(t *testing.T, b *Backend) []storageSpec {
	t.Helper()
	var specs []storageSpec
	if _, err := b.kv.get(context.Background(), pendingStorageKey, &specs); err != nil {
		t.Fatal(err)
	}
	return specs
}

// TestStorageQueueSurvivesRestart: changes made while offline (or pending
// at quit) are persisted, loaded by the next run, replayed before the first
// storage read, win over the remote state for those fields and are cleared
// once written.
func TestStorageQueueSurvivesRestart(t *testing.T) {
	old := storageWriteDelay
	storageWriteDelay = time.Hour // never flushed by the timer: offline
	t.Cleanup(func() { storageWriteDelay = old })
	open := newReopenable(t)
	ctx := context.Background()

	b1 := open(t)
	if err := b1.db.UpsertChat(ctx, core.Chat{ID: aliceChat, Kind: core.ChatDirect}); err != nil {
		t.Fatal(err)
	}
	if err := b1.db.UpsertContact(ctx, core.Contact{ACI: aliceACI, ProfileName: "Alice"}); err != nil {
		t.Fatal(err)
	}
	yes := true
	if err := b1.UpdateChat(ctx, aliceChat, core.ChatUpdate{Archived: &yes}); err != nil {
		t.Fatal(err)
	}
	s, err := b1.db.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s.ReadReceipts = false
	s.DefaultExpireTimer = time.Hour
	s.NotificationLevel = core.NotifyNameOnly // local only: never queued
	if err := b1.UpdateSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := b1.SetNickname(ctx, aliceACI, "Ally"); err != nil {
		t.Fatal(err)
	}
	b1.queueStorage(stickerPackStorageOp([]byte{1, 2}, bytes.Repeat([]byte{3}, 32), true))
	if got := len(pendingSpecs(t, b1)); got != 5 {
		t.Fatalf("%d persisted ops, want 5", got)
	}
	if err := b1.Close(); err != nil {
		t.Fatal(err)
	}

	// The next run: the phone still has the old state.
	b2 := open(t)
	if !b2.storageWritePending() {
		t.Fatal("pending changes not loaded")
	}
	mem, err := signalmeow.NewMemoryStorage(storageTestMasterKey, 4, nil, accountRecord(), contactRecord(aliceUUID))
	if err != nil {
		t.Fatal(err)
	}
	cli := attachStorage(b2, mem)
	// A storage read while the changes are still pending leaves them alone.
	b2.applyStorage(ctx, cli)
	if c, _ := b2.db.GetChat(ctx, aliceChat); !c.Archived {
		t.Fatal("storage read undid a pending archive")
	}

	b2.replayPendingStorage(time.Minute)
	if b2.storageWritePending() {
		t.Fatal("still pending after replay")
	}
	if specs := pendingSpecs(t, b2); len(specs) != 0 {
		t.Fatalf("persisted queue not cleared: %+v", specs)
	}
	_, recs, err := mem.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	c := storedContact(recs, aliceUUID)
	if !c.GetArchived() || c.GetNickname().GetGiven() != "Ally" {
		t.Errorf("contact %+v", c)
	}
	if a := storedAccount(recs); a.GetReadReceipts() || a.GetUniversalExpireTimer() != 3600 {
		t.Errorf("account %+v", a)
	}
	found := false
	for _, r := range recs {
		if p := r.GetStickerPack(); p != nil && bytes.Equal(p.GetPackId(), []byte{1, 2}) {
			found = true
		}
	}
	if !found {
		t.Error("sticker pack not written")
	}

	// Now a fresh storage read agrees with the local state.
	b2.applyStorage(ctx, cli)
	if c, _ := b2.db.GetChat(ctx, aliceChat); !c.Archived {
		t.Error("archive lost after replay and read")
	}
	if s, _ := b2.db.Settings(ctx); s.ReadReceipts || s.DefaultExpireTimer != time.Hour || s.NotificationLevel != core.NotifyNameOnly {
		t.Errorf("settings after read %+v", s)
	}
}

// TestStorageQueueParksAfterFailures: a batch that keeps failing is kept
// (in memory and on disk) instead of being dropped, and goes out after the
// connection comes back.
func TestStorageQueueParksAfterFailures(t *testing.T) {
	e := newStorageTestEnv(t, 1, accountRecord(), contactRecord(aliceUUID))
	oldRetry := storageRetryDelay
	storageRetryDelay = time.Millisecond
	t.Cleanup(func() { storageRetryDelay = oldRetry })
	fs := &failingStorage{MemoryStorage: e.mem, fails: maxStorageWriteFailures + 1}
	e.cli.StorageTransport = fs
	ctx := context.Background()
	if err := e.b.db.UpsertChat(ctx, core.Chat{ID: aliceChat, Kind: core.ChatDirect}); err != nil {
		t.Fatal(err)
	}
	yes := true
	if err := e.b.UpdateChat(ctx, aliceChat, core.ChatUpdate{MarkedUnread: &yes}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		fs.mu.Lock()
		left := fs.fails
		fs.mu.Unlock()
		if left == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("writes were not retried")
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if !e.b.storageWritePending() {
		t.Fatal("parked batch was dropped")
	}
	if specs := pendingSpecs(t, e.b); len(specs) != 1 || specs[0].Kind != specChat {
		t.Fatalf("persisted %+v", specs)
	}
	if e.mem.Writes != 0 {
		t.Fatalf("%d writes", e.mem.Writes)
	}
	// Reconnected.
	e.b.resumeStorageWrites()
	e.waitIdle(t)
	_, recs := e.snapshot(t)
	if !storedContact(recs, aliceUUID).GetMarkedUnread() {
		t.Error("parked change not written after reconnect")
	}
	if specs := pendingSpecs(t, e.b); len(specs) != 0 {
		t.Errorf("persisted queue not cleared: %+v", specs)
	}
}

func TestStorageSpecRoundTrip(t *testing.T) {
	yes := true
	mute := time.UnixMilli(1_900_000_000_000)
	ops := []storageOp{
		mustOp(chatStorageOp(aliceChat, recordSeed{E164: "+15550001", Whitelisted: true}, chatStorageFields{Archived: &yes, MutedUntil: &mute})),
		nicknameStorageOp(aliceACI, recordSeed{}, "Al"),
		stickerPackStorageOp([]byte{1}, []byte{2}, true),
	}
	ops = append(ops, settingsStorageOps(core.Settings{}, core.Settings{PhoneNumberSharing: true, DefaultExpireTimer: time.Minute})...)
	for _, op := range ops {
		back, ok := op.spec.op()
		if !ok {
			t.Fatalf("%s: spec does not rebuild", op.desc)
		}
		self := selfUUID
		a, b := &signalmeow.StorageState{}, &signalmeow.StorageState{}
		for _, st := range []*signalmeow.StorageState{a, b} {
			st.Now = func() time.Time { return mute }
			st.UpsertContact(aliceUUID, func(*signalpb.ContactRecord) {})
		}
		env := &storageEnv{ctx: context.Background(), self: self}
		_ = op.apply(a, env)
		_ = back.apply(b, env)
		if ca, cb := a.Contact(aliceUUID), b.Contact(aliceUUID); ca.String() != cb.String() {
			t.Errorf("%s: %v != %v", op.desc, ca, cb)
		}
	}
	if _, ok := (storageSpec{Kind: "bogus"}).op(); ok {
		t.Error("unknown spec kind accepted")
	}
}

func mustOp(op storageOp, ok bool) storageOp {
	if !ok {
		panic("no op")
	}
	return op
}

// TestAccountRecordConcurrentAccess hammers signalmeow's account record
// from the paths that touch it concurrently in warte: storage writes
// replacing it, UpdateSettings editing it, and senders/story code reading
// it. Run with -race.
func TestAccountRecordConcurrentAccess(t *testing.T) {
	e := newStorageTestEnv(t, 1, accountRecord(), contactRecord(aliceUUID))
	e.cli.Store.SetAccountRecord(&signalpb.AccountRecord{ReadReceipts: true})
	ctx := context.Background()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	run := func(fn func(i int)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				fn(i)
			}
		}()
	}
	run(func(i int) {
		_, _ = e.cli.StorageWrite(ctx, func(st *signalmeow.StorageState) error {
			st.Account().TypingIndicators = i%2 == 0
			return nil
		})
	})
	run(func(i int) {
		applySettingsToAccount(e.cli, core.Settings{ReadReceipts: i%2 == 0, LinkPreviews: true})
	})
	run(func(int) {
		_ = e.b.storiesDisabled()
		ar := e.cli.Store.AccountRecord()
		_ = storyViewReceiptsEnabled(ar, core.Settings{})
		if ar != nil {
			ar.GivenName = "scribble" // a copy: must not affect the store
		}
	})
	time.Sleep(300 * time.Millisecond)
	close(stop)
	wg.Wait()
	if e.cli.Store.AccountRecord().GetGivenName() == "scribble" {
		t.Error("AccountRecord returned the stored record, not a copy")
	}
}

// TestStorageQueueDroppedOnUnlink: pending changes belong to the account
// the device was linked to; after unlinking they must not be replayed into
// the next linked account.
func TestStorageQueueDroppedOnUnlink(t *testing.T) {
	old := storageWriteDelay
	storageWriteDelay = time.Hour
	t.Cleanup(func() { storageWriteDelay = old })
	open := newReopenable(t)
	b := open(t)
	b.queueStorage(stickerPackStorageOp([]byte{1}, bytes.Repeat([]byte{2}, 32), true))
	if len(pendingSpecs(t, b)) != 1 {
		t.Fatal("not persisted")
	}
	b.dropPendingStorage()
	if b.storageWritePending() || len(pendingSpecs(t, b)) != 0 {
		t.Fatal("queue not dropped")
	}
	_ = b.Close()
	if open(t).storageWritePending() {
		t.Error("dropped queue came back after restart")
	}
}
