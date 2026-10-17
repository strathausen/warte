package backend

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"go.mau.fi/mautrix-signal/pkg/libsignalgo"
	"go.mau.fi/mautrix-signal/pkg/signalmeow"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/store"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/types"

	"warte/internal/core"
)

var storageTestMasterKey = bytes.Repeat([]byte{0x24}, 32)

// storageTestEnv is a backend wired to an in-memory storage service.
type storageTestEnv struct {
	b        *Backend
	clock    *testClock
	mem      *signalmeow.MemoryStorage
	cli      *signalmeow.Client
	mu       sync.Mutex
	notified int
}

func (e *storageTestEnv) notifications() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.notified
}

func newStorageTestEnv(t *testing.T, version uint64, recs ...*signalpb.StorageRecord) *storageTestEnv {
	t.Helper()
	old := storageWriteDelay
	storageWriteDelay = 20 * time.Millisecond
	t.Cleanup(func() { storageWriteDelay = old })
	b, clock := newTestBackend(t)
	mem, err := signalmeow.NewMemoryStorage(storageTestMasterKey, version, nil, recs...)
	if err != nil {
		t.Fatal(err)
	}
	cli := &signalmeow.Client{
		Store: &store.Device{
			DeviceData:     store.DeviceData{MasterKey: storageTestMasterKey, DeviceID: 2, ACI: selfUUID},
			RecipientStore: fakeRecipients{},
		},
		Log:              zerolog.Nop(),
		StorageTransport: mem,
	}
	e := &storageTestEnv{b: b, clock: clock, mem: mem, cli: cli}
	b.mu.Lock()
	b.cli = cli
	b.mu.Unlock()
	b.sw.notify = func(ctx context.Context, c *signalmeow.Client) error {
		e.mu.Lock()
		e.notified++
		e.mu.Unlock()
		return nil
	}
	_ = b.kv.set(context.Background(), "storage_version", version)
	return e
}

// fakeRecipients is the part of signalmeow's recipient store the tested
// code paths use.
type fakeRecipients struct{ store.RecipientStore }

func (fakeRecipients) LoadAndUpdateRecipient(ctx context.Context, aci, pni uuid.UUID, fn store.RecipientUpdaterFunc) (*types.Recipient, error) {
	r := &types.Recipient{ACI: aci, PNI: pni}
	_, err := fn(r)
	return r, err
}

// waitIdle waits until the write queue is empty and nothing is in flight.
func (e *storageTestEnv) waitIdle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	time.Sleep(storageWriteDelay)
	for e.b.storageWritePending() {
		if time.Now().After(deadline) {
			t.Fatal("storage write did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (e *storageTestEnv) snapshot(t *testing.T) (*signalpb.ManifestRecord, []*signalpb.StorageRecord) {
	t.Helper()
	m, recs, err := e.mem.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	return m, recs
}

func storedContact(recs []*signalpb.StorageRecord, u uuid.UUID) *signalpb.ContactRecord {
	for _, r := range recs {
		if c := r.GetContact(); c != nil && c.GetAci() == u.String() {
			return c
		}
	}
	return nil
}

func storedAccount(recs []*signalpb.StorageRecord) *signalpb.AccountRecord {
	for _, r := range recs {
		if a := r.GetAccount(); a != nil {
			return a
		}
	}
	return nil
}

func storedGroup(recs []*signalpb.StorageRecord, mk []byte) *signalpb.GroupV2Record {
	for _, r := range recs {
		if g := r.GetGroupV2(); g != nil && bytes.Equal(g.GetMasterKey(), mk) {
			return g
		}
	}
	return nil
}

func contactRecord(u uuid.UUID) *signalpb.StorageRecord {
	return &signalpb.StorageRecord{Record: &signalpb.StorageRecord_Contact{Contact: &signalpb.ContactRecord{Aci: u.String(), GivenName: "Remote", Whitelisted: true}}}
}

func accountRecord() *signalpb.StorageRecord {
	return &signalpb.StorageRecord{Record: &signalpb.StorageRecord_Account{Account: &signalpb.AccountRecord{
		GivenName: "Me", TypingIndicators: true, ReadReceipts: true,
		PinnedConversations: []*signalpb.AccountRecord_PinnedConversation{{Identifier: &signalpb.AccountRecord_PinnedConversation_ReleaseNotes_{ReleaseNotes: &signalpb.AccountRecord_PinnedConversation_ReleaseNotes{}}}},
	}}}
}

func groupRecord(mk []byte) *signalpb.StorageRecord {
	return &signalpb.StorageRecord{Record: &signalpb.StorageRecord_GroupV2{GroupV2: &signalpb.GroupV2Record{MasterKey: mk, Whitelisted: true}}}
}

// storageTestGroup returns a master key and the chat ID of its group.
func storageTestGroup(t *testing.T) ([]byte, core.ChatID) {
	t.Helper()
	mk := bytes.Repeat([]byte{0x31}, 32)
	gid, err := libsignalgo.GroupMasterKey(mk).GroupIdentifier()
	if err != nil {
		t.Fatal(err)
	}
	return mk, core.GroupChatID(base64.StdEncoding.EncodeToString(gid[:]))
}

func TestStorageWriteBatchesChanges(t *testing.T) {
	mk, gchat := storageTestGroup(t)
	e := newStorageTestEnv(t, 7, accountRecord(), contactRecord(aliceUUID), groupRecord(mk))
	b, ctx := e.b, context.Background()
	// The changes below must land in one batch: a debounce long enough
	// that a slow (-race, loaded) machine can't flush between them. Each
	// change also persists the pending queue to the database.
	short := storageWriteDelay
	storageWriteDelay = time.Second
	t.Cleanup(func() { storageWriteDelay = short })
	for _, c := range []core.Chat{{ID: aliceChat, Kind: core.ChatDirect}, {ID: gchat, Kind: core.ChatGroup}, {ID: core.DirectChatID(selfACI), Kind: core.ChatNoteToSelf}} {
		if err := b.db.UpsertChat(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	yes := true
	forever := foreverMuted
	if err := b.UpdateChat(ctx, aliceChat, core.ChatUpdate{Archived: &yes, MutedUntil: &forever, MarkedUnread: &yes}); err != nil {
		t.Fatal(err)
	}
	if err := b.UpdateChat(ctx, gchat, core.ChatUpdate{Pinned: &yes}); err != nil {
		t.Fatal(err)
	}
	if err := b.UpdateChat(ctx, core.DirectChatID(selfACI), core.ChatUpdate{Archived: &yes}); err != nil {
		t.Fatal(err)
	}
	if err := b.SetNickname(ctx, aliceACI, "Ally"); err != nil {
		t.Fatal(err)
	}
	// Local state changes immediately.
	if c, _ := b.db.GetChat(ctx, aliceChat); !c.Archived || !c.MarkedUnread {
		t.Fatalf("local chat not updated: %+v", c)
	}
	if !b.storageWritePending() {
		t.Fatal("no write queued")
	}
	// Only read receipts change; typing indicators stay as the phone has
	// them even though warte's local copy differs.
	s, err := b.db.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s.TypingIndicators = false
	if err := b.db.SaveSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	s.ReadReceipts = !s.ReadReceipts
	if err := b.UpdateSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	e.waitIdle(t)

	if e.mem.Writes != 1 {
		t.Fatalf("%d writes, want one batch", e.mem.Writes)
	}
	if e.notifications() != 1 {
		t.Errorf("%d fetch-latest syncs, want 1", e.notifications())
	}
	m, recs := e.snapshot(t)
	if m.GetVersion() != 8 || m.GetSourceDevice() != 2 {
		t.Errorf("manifest %d from device %d", m.GetVersion(), m.GetSourceDevice())
	}
	c := storedContact(recs, aliceUUID)
	if !c.GetArchived() || !c.GetMarkedUnread() || c.GetMutedUntilTimestamp() != math.MaxInt64 {
		t.Errorf("contact %+v", c)
	}
	if c.GetNickname().GetGiven() != "Ally" || c.GetGivenName() != "Remote" {
		t.Errorf("contact name fields %+v", c)
	}
	a := storedAccount(recs)
	if !a.GetNoteToSelfArchived() || a.GetReadReceipts() != s.ReadReceipts || !a.GetTypingIndicators() {
		t.Errorf("account %+v", a)
	}
	if len(a.GetPinnedConversations()) != 2 || a.PinnedConversations[0].GetReleaseNotes() == nil ||
		!bytes.Equal(a.PinnedConversations[1].GetGroupMasterKey(), mk) {
		t.Errorf("pinned %+v", a.GetPinnedConversations())
	}
	if g := storedGroup(recs, mk); g.GetArchived() {
		t.Error("group archived")
	}
	var known uint64
	_, _ = b.kv.get(ctx, "storage_version", &known)
	if known != 8 {
		t.Errorf("storage_version %d, want 8", known)
	}
	// signalmeow's view of the account record is updated too.
	if e.cli.Store.AccountRecord().GetReadReceipts() != s.ReadReceipts {
		t.Error("client account record not updated")
	}
}

func TestStorageWriteArchiveUnpinsAndMerges(t *testing.T) {
	mk, gchat := storageTestGroup(t)
	acc := accountRecord()
	acc.GetAccount().PinnedConversations = append(acc.GetAccount().PinnedConversations,
		&signalpb.AccountRecord_PinnedConversation{Identifier: &signalpb.AccountRecord_PinnedConversation_GroupMasterKey{GroupMasterKey: mk}})
	e := newStorageTestEnv(t, 3, acc, contactRecord(aliceUUID), contactRecord(bobUUID), groupRecord(mk))
	b, ctx := e.b, context.Background()
	if err := b.db.UpsertChat(ctx, core.Chat{ID: gchat, Kind: core.ChatGroup, Pinned: true, PinOrder: 1}); err != nil {
		t.Fatal(err)
	}
	// The phone writes first: it marks Bob unread.
	e.mem.BeforeWrite = func(m *signalmeow.MemoryStorage) {
		if err := m.Update(func(recs []*signalpb.StorageRecord) []*signalpb.StorageRecord {
			storedContact(recs, bobUUID).MarkedUnread = true
			return nil
		}); err != nil {
			t.Error(err)
		}
	}
	yes := true
	if err := b.UpdateChat(ctx, gchat, core.ChatUpdate{Archived: &yes}); err != nil {
		t.Fatal(err)
	}
	e.waitIdle(t)
	if e.mem.Conflicts != 1 || e.mem.Writes != 1 {
		t.Fatalf("conflicts=%d writes=%d", e.mem.Conflicts, e.mem.Writes)
	}
	m, recs := e.snapshot(t)
	if m.GetVersion() != 5 {
		t.Errorf("version %d", m.GetVersion())
	}
	if !storedGroup(recs, mk).GetArchived() {
		t.Error("group not archived")
	}
	if pins := storedAccount(recs).GetPinnedConversations(); len(pins) != 1 || pins[0].GetReleaseNotes() == nil {
		t.Errorf("archiving did not unpin: %+v", pins)
	}
	if !storedContact(recs, bobUUID).GetMarkedUnread() {
		t.Error("the phone's change was lost")
	}
	var known uint64
	_, _ = b.kv.get(ctx, "storage_version", &known)
	if known != 3 {
		t.Errorf("storage_version %d; must stay so the phone's change is read", known)
	}
	select {
	case <-b.storageKick:
	default:
		t.Error("no storage re-read after merging another device's change")
	}
}

func TestStorageWriteNewRecords(t *testing.T) {
	_, gchat := storageTestGroup(t)
	e := newStorageTestEnv(t, 1, accountRecord())
	ctx := context.Background()
	if err := e.b.db.UpsertContact(ctx, core.Contact{ACI: bobACI, ProfileName: "Bob", E164: "+15550002"}); err != nil {
		t.Fatal(err)
	}
	block := true
	e.b.queueChatStorage(ctx, core.DirectChatID(bobACI), chatStorageFields{Blocked: &block})
	e.b.queueStorage(stickerPackStorageOp([]byte{1, 2}, bytes.Repeat([]byte{3}, 32), true))
	// A group the storage service and signalmeow do not know is skipped
	// without failing the batch.
	e.b.queueChatStorage(ctx, gchat, chatStorageFields{Archived: &block})
	e.waitIdle(t)
	_, recs := e.snapshot(t)
	c := storedContact(recs, bobUUID)
	if c == nil || !c.GetBlocked() || c.GetWhitelisted() || c.GetE164() != "+15550002" ||
		c.GetBlockedAtTimestamp() != uint64(e.clock.Now().UnixMilli()) {
		t.Fatalf("contact %+v", c)
	}
	found := false
	for _, r := range recs {
		if p := r.GetStickerPack(); p != nil && bytes.Equal(p.GetPackId(), []byte{1, 2}) && p.GetDeletedAtTimestamp() == 0 {
			found = true
		}
	}
	if !found || len(recs) != 3 {
		t.Errorf("records %d, sticker pack found=%v", len(recs), found)
	}
	// Unblocking clears the timestamp and re-enables profile sharing.
	unblock := false
	e.b.queueChatStorage(ctx, core.DirectChatID(bobACI), chatStorageFields{Blocked: &unblock})
	e.waitIdle(t)
	_, recs = e.snapshot(t)
	if c := storedContact(recs, bobUUID); c.GetBlocked() || !c.GetWhitelisted() || c.GetBlockedAtTimestamp() != 0 {
		t.Errorf("unblocked contact %+v", c)
	}
}

type failingStorage struct {
	*signalmeow.MemoryStorage
	mu    sync.Mutex
	fails int
}

func (f *failingStorage) Write(ctx context.Context, op *signalpb.WriteOperation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fails > 0 {
		f.fails--
		return errors.New("network down")
	}
	return f.MemoryStorage.Write(ctx, op)
}

func TestStorageWriteRetriesAfterFailure(t *testing.T) {
	e := newStorageTestEnv(t, 1, accountRecord(), contactRecord(aliceUUID))
	oldRetry := storageRetryDelay
	storageRetryDelay = 20 * time.Millisecond
	t.Cleanup(func() { storageRetryDelay = oldRetry })
	fs := &failingStorage{MemoryStorage: e.mem, fails: 2}
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
		if m, _, err := e.mem.Snapshot(); err == nil && m.GetVersion() > 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("write was not retried")
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.waitIdle(t)
	_, recs := e.snapshot(t)
	if !storedContact(recs, aliceUUID).GetMarkedUnread() {
		t.Error("retried write lost the change")
	}
}

func TestSettingsStorageOps(t *testing.T) {
	old := core.Settings{ReadReceipts: true, TypingIndicators: true, PhoneNumberSharing: true, DiscoverableByPhone: true}
	if ops := settingsStorageOps(old, old); len(ops) != 0 {
		t.Fatalf("%d ops for unchanged settings", len(ops))
	}
	s := old
	s.DefaultExpireTimer = 90 * time.Minute
	s.PhoneNumberSharing = false
	s.DiscoverableByPhone = false
	s.NotificationLevel = core.NotifyNone // local only
	ops := settingsStorageOps(old, s)
	if len(ops) != 3 {
		t.Fatalf("%d ops", len(ops))
	}
}

func TestMuteToProto(t *testing.T) {
	at := time.UnixMilli(1_800_000_000_000)
	for _, c := range []struct {
		in   time.Time
		want uint64
	}{
		{time.Time{}, 0},
		{foreverMuted, math.MaxInt64},
		{at, 1_800_000_000_000},
	} {
		if got := muteToProto(c.in); got != c.want {
			t.Errorf("muteToProto(%v) = %d, want %d", c.in, got, c.want)
		}
		// And back through the read path.
		if got := mutedUntil(muteToProto(c.in)); !got.Equal(c.in) && !(c.in.IsZero() && got.IsZero()) {
			t.Errorf("round trip %v -> %v", c.in, got)
		}
	}
}

func TestApplyStorageWaitsForPendingWrites(t *testing.T) {
	e := newStorageTestEnv(t, 1, accountRecord())
	old := storageWriteDelay
	storageWriteDelay = time.Hour // keep the op queued
	defer func() { storageWriteDelay = old }()
	e.b.queueStorage(stickerPackStorageOp([]byte{9}, nil, false))
	if !e.b.storageWritePending() {
		t.Fatal("not pending")
	}
	// applyStorage must return before touching the (unreachable) server.
	done := make(chan struct{})
	go func() {
		e.b.applyStorage(context.Background(), e.cli)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("applyStorage did not skip")
	}
}

// LoadProfileKey returns a fixed key for Bob.
func (fakeRecipients) LoadProfileKey(ctx context.Context, aci uuid.UUID) (*libsignalgo.ProfileKey, error) {
	if aci != bobUUID {
		return nil, nil
	}
	pk := libsignalgo.ProfileKey(bytes.Repeat([]byte{0x77}, libsignalgo.ProfileKeyLength))
	return &pk, nil
}

// TestStorageWriteNewRecordSeed checks what a brand-new contact or group
// record carries. Desktop's applyMessageRequestState and Android copy
// whitelisted into profile sharing, so an accepted chat must not be written
// as whitelisted=false (that would make it a message request on the phone).
func TestStorageWriteNewRecordSeed(t *testing.T) {
	mk, gchat := storageTestGroup(t)
	e := newStorageTestEnv(t, 1, accountRecord())
	ctx := context.Background()
	if err := e.b.db.UpsertContact(ctx, core.Contact{ACI: bobACI, ProfileName: "Bob", E164: "+15550002"}); err != nil {
		t.Fatal(err)
	}
	if err := e.b.db.UpsertChat(ctx, core.Chat{ID: core.DirectChatID(bobACI), Kind: core.ChatDirect}); err != nil {
		t.Fatal(err)
	}
	if err := e.b.db.UpsertChat(ctx, core.Chat{ID: gchat, Kind: core.ChatGroup, MessageRequest: true}); err != nil {
		t.Fatal(err)
	}
	yes := true
	e.b.queueChatStorage(ctx, core.DirectChatID(bobACI), chatStorageFields{Archived: &yes})
	e.b.queueStorage(nicknameStorageOp(aliceACI, e.b.recordSeed(ctx, core.DirectChatID(aliceACI)), "Al"))
	e.waitIdle(t)
	_, recs := e.snapshot(t)
	c := storedContact(recs, bobUUID)
	if c == nil || !c.GetArchived() || !c.GetWhitelisted() || c.GetE164() != "+15550002" ||
		!bytes.Equal(c.GetProfileKey(), bytes.Repeat([]byte{0x77}, 32)) {
		t.Fatalf("new accepted contact %+v", c)
	}
	if a := storedContact(recs, aliceUUID); a == nil || a.GetWhitelisted() || a.GetNickname().GetGiven() != "Al" || len(a.GetProfileKey()) != 0 {
		t.Fatalf("new contact without chat %+v", a)
	}
	// Groups: the seed follows the chat's message-request state, and a
	// new group record (master key from signalmeow's group store) gets it.
	if seed := e.b.recordSeed(ctx, gchat); seed.Whitelisted {
		t.Error("message-request group seeded as whitelisted")
	}
	env := &storageEnv{ctx: ctx, cli: &signalmeow.Client{Store: &store.Device{GroupStore: fakeGroups{mk: mk}}}}
	for _, want := range []bool{true, false} {
		st := &signalmeow.StorageState{}
		op, _ := chatStorageOp(gchat, recordSeed{Whitelisted: want}, chatStorageFields{Archived: &yes})
		if err := op.apply(st, env); err != nil {
			t.Fatal(err)
		}
		if g := st.GroupV2(mk); g == nil || g.GetWhitelisted() != want || !g.GetArchived() {
			t.Errorf("new group record (seed %v) %+v", want, g)
		}
	}
	// An existing record keeps its own whitelisted value.
	st := &signalmeow.StorageState{}
	st.UpsertGroupV2(mk)
	op, _ := chatStorageOp(gchat, recordSeed{Whitelisted: true}, chatStorageFields{Archived: &yes})
	if err := op.apply(st, env); err != nil {
		t.Fatal(err)
	}
	if g := st.GroupV2(mk); g.GetWhitelisted() {
		t.Errorf("existing group record re-seeded %+v", g)
	}
}

// fakeGroups resolves one group's master key.
type fakeGroups struct{ mk []byte }

func (f fakeGroups) MasterKeyFromGroupIdentifier(ctx context.Context, gid types.GroupIdentifier) (types.SerializedGroupMasterKey, error) {
	return types.SerializedGroupMasterKey(base64.StdEncoding.EncodeToString(f.mk)), nil
}

func (fakeGroups) StoreMasterKey(context.Context, types.GroupIdentifier, types.SerializedGroupMasterKey) error {
	return nil
}
