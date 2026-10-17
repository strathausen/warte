package backend

import (
	"context"
	"encoding/base64"
	"errors"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.mau.fi/mautrix-signal/pkg/libsignalgo"
	"go.mau.fi/mautrix-signal/pkg/signalmeow"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/types"

	"warte/internal/core"
)

// Storage service write-back.
//
// Chat state (archive, pin, mute, marked unread, blocked, message request
// accepted), nicknames, account settings and sticker pack installs are
// applied locally right away and queued as storage operations. The queue
// is flushed after storageWriteDelay of quiet (so a burst of changes
// becomes one manifest write) through signalmeow's StorageWrite, which
// re-applies the operations on top of the newest remote state when another
// device wrote in between. Our other devices are then told to refetch
// (FetchLatest STORAGE_MANIFEST), like Signal Desktop does after an upload.
//
// While operations are queued or being written, applyStorage does not
// mirror the remote state into the store, so a storage read cannot undo a
// local change that has not reached the server yet.
//
// The queue is durable: every operation has a serialisable storageSpec, and
// the specs of all queued and in-flight operations are saved in the store
// (kv key storage_pending) whenever the queue changes. They are cleared
// only after a successful write (or when the account has no storage
// service to write to). New loads them, so changes made offline or still
// pending at quit survive a restart; postConnect replays them before the
// first storage read, so the local intent wins over the remote state for
// those fields, as Signal Desktop's pending local changes do. A batch that
// keeps failing is parked (kept queued and saved, not retried) until the
// next reconnect or local change.

// storageWriteDelay is the debounce before queued operations are written.
var storageWriteDelay = 2 * time.Second

// storageRetryDelay is the wait after a failed write; it grows linearly.
var storageRetryDelay = 30 * time.Second

// maxStorageWriteFailures is how often a batch is retried before it is
// dropped (the phone's state then wins on the next storage read).
const maxStorageWriteFailures = 5

// storageOp is one queued change. It must be idempotent: after a conflict
// (or a restart) it runs again on the newer state.
type storageOp struct {
	desc  string
	spec  storageSpec // what is persisted; spec.op() rebuilds the op
	apply func(st *signalmeow.StorageState, env *storageEnv) error
}

// storageSpec kinds.
const (
	specChat     = "chat"
	specNickname = "nickname"
	specSetting  = "setting"
	specSticker  = "sticker"
)

// pendingStorageKey is the kv key of the persisted queue.
const pendingStorageKey = "storage_pending"

// storageSpec is the persisted form of a storageOp.
type storageSpec struct {
	Kind     string             `json:"kind"`
	Chat     core.ChatID        `json:"chat,omitempty"`
	Seed     recordSeed         `json:"seed"`
	Fields   *chatStorageFields `json:"fields,omitempty"`
	ACI      core.ACI           `json:"aci,omitempty"`
	Nickname string             `json:"nickname,omitempty"`
	Setting  string             `json:"setting,omitempty"`
	Bool     bool               `json:"bool,omitempty"`
	Uint     uint64             `json:"uint,omitempty"`
	PackID   []byte             `json:"pack_id,omitempty"`
	PackKey  []byte             `json:"pack_key,omitempty"`
	Install  bool               `json:"install,omitempty"`
}

// op rebuilds the operation; ok is false for an unknown or invalid spec.
func (s storageSpec) op() (storageOp, bool) {
	switch s.Kind {
	case specChat:
		if s.Fields == nil {
			return storageOp{}, false
		}
		return chatStorageOp(s.Chat, s.Seed, *s.Fields)
	case specNickname:
		return nicknameStorageOp(s.ACI, s.Seed, s.Nickname), s.ACI != ""
	case specSetting:
		return settingStorageOp(s.Setting, s)
	case specSticker:
		return stickerPackStorageOp(s.PackID, s.PackKey, s.Install), len(s.PackID) > 0
	}
	return storageOp{}, false
}

// storageEnv gives operations access to lookups that need the client.
type storageEnv struct {
	ctx  context.Context
	cli  *signalmeow.Client
	self uuid.UUID
}

// recordSeed is what a contact or group record that the storage service
// does not have yet starts with. Desktop (applyMessageRequestState) and
// Android copy a record's whitelisted flag into the conversation's profile
// sharing, so a new record must carry our real accepted state: a default
// false would turn the chat into a message request on the phone.
type recordSeed struct {
	E164        string
	Whitelisted bool
}

// seedContact fills a new contact record: phone number, profile sharing
// and (when signalmeow knows it) the profile key, which Desktop and
// Android only adopt from a record when they have none themselves.
func (e *storageEnv) seedContact(c *signalpb.ContactRecord, u uuid.UUID, seed recordSeed) {
	c.E164 = seed.E164
	c.Whitelisted = seed.Whitelisted
	if e.cli == nil || e.cli.Store == nil || e.cli.Store.RecipientStore == nil {
		return
	}
	if pk, err := e.cli.Store.RecipientStore.LoadProfileKey(e.ctx, u); err == nil && pk != nil && *pk != (libsignalgo.ProfileKey{}) {
		c.ProfileKey = slices.Clone(pk[:])
	}
}

// groupMasterKey finds a group's master key: first among the storage
// records, then in signalmeow's group store.
func (e *storageEnv) groupMasterKey(st *signalmeow.StorageState, id core.ChatID) ([]byte, error) {
	gid, err := groupIDBytes(id)
	if err != nil {
		return nil, err
	}
	for _, en := range st.Entries() {
		g := en.Record.GetGroupV2()
		if g == nil || len(g.GetMasterKey()) != libsignalgo.GroupMasterKeyLength {
			continue
		}
		if got, err := libsignalgo.GroupMasterKey(g.GetMasterKey()).GroupIdentifier(); err == nil && string(got[:]) == string(gid) {
			return g.GetMasterKey(), nil
		}
	}
	if e.cli == nil || e.cli.Store.GroupStore == nil {
		return nil, core.ErrNotFound
	}
	smk, err := e.cli.Store.GroupStore.MasterKeyFromGroupIdentifier(e.ctx, types.GroupIdentifier(id.GroupID()))
	if err != nil {
		return nil, err
	}
	if smk == "" {
		return nil, core.ErrNotFound
	}
	mk, err := base64.StdEncoding.DecodeString(string(smk))
	if err != nil || len(mk) != libsignalgo.GroupMasterKeyLength {
		return nil, errors.New("backend: bad group master key")
	}
	return mk, nil
}

// storageWriter is the queue of pending storage operations.
type storageWriter struct {
	mu       sync.Mutex
	ops      []storageOp
	timer    *time.Timer
	inflight bool
	// written is the batch being written (persisted until it succeeds).
	written  []storageOp
	failures int

	// applyMu serialises flushes with applyStorage.
	applyMu sync.Mutex

	// notify, when set, replaces the FetchLatest sync after a write (tests).
	notify func(ctx context.Context, cli *signalmeow.Client) error
}

// queueStorage queues operations and (re)starts the debounce timer.
func (b *Backend) queueStorage(ops ...storageOp) {
	if len(ops) == 0 {
		return
	}
	b.sw.mu.Lock()
	defer b.sw.mu.Unlock()
	b.sw.ops = append(b.sw.ops, ops...)
	b.persistStorageLocked()
	b.scheduleStorageFlushLocked(storageWriteDelay)
}

// persistStorageLocked saves the specs of the in-flight and queued
// operations (or removes the key when there are none). sw.mu must be held.
func (b *Backend) persistStorageLocked() {
	specs := make([]storageSpec, 0, len(b.sw.written)+len(b.sw.ops))
	for _, list := range [][]storageOp{b.sw.written, b.sw.ops} {
		for _, op := range list {
			if op.spec.Kind != "" {
				specs = append(specs, op.spec)
			}
		}
	}
	ctx := context.Background()
	var err error
	if len(specs) == 0 {
		err = b.kv.del(ctx, pendingStorageKey)
	} else {
		err = b.kv.set(ctx, pendingStorageKey, specs)
	}
	if err != nil && !errors.Is(err, core.ErrNotFound) {
		b.log.Warn().Err(err).Msg("Failed to save pending storage changes")
	}
}

// loadPendingStorage queues the operations saved by an earlier run. It
// runs in New, before anything else can queue (or persist) operations, and
// does not schedule a flush: postConnect replays them once connected.
func (b *Backend) loadPendingStorage(ctx context.Context) {
	var specs []storageSpec
	if ok, err := b.kv.get(ctx, pendingStorageKey, &specs); !ok || err != nil {
		if err != nil {
			b.log.Warn().Err(err).Msg("Failed to load pending storage changes")
		}
		return
	}
	b.sw.mu.Lock()
	defer b.sw.mu.Unlock()
	for _, sp := range specs {
		if op, ok := sp.op(); ok {
			b.sw.ops = append(b.sw.ops, op)
		}
	}
	if len(b.sw.ops) > 0 {
		b.log.Info().Int("ops", len(b.sw.ops)).Msg("Loaded pending storage changes")
	}
}

// replayPendingStorage writes the queued operations now (after connecting,
// before the first storage read). It is a no-op when nothing is queued.
func (b *Backend) replayPendingStorage(timeout time.Duration) {
	b.sw.mu.Lock()
	pending := len(b.sw.ops) > 0
	b.sw.failures = 0
	b.sw.mu.Unlock()
	if pending {
		b.flushStorageWithin(timeout)
	}
}

// dropPendingStorage forgets queued (and persisted) operations: after this
// device was unlinked they belong to an account we no longer write to.
func (b *Backend) dropPendingStorage() {
	b.sw.mu.Lock()
	defer b.sw.mu.Unlock()
	if b.sw.timer != nil {
		b.sw.timer.Stop()
	}
	b.sw.ops = nil
	b.sw.written = nil
	b.persistStorageLocked()
}

// resumeStorageWrites schedules a flush of parked or waiting operations,
// e.g. after the connection came back.
func (b *Backend) resumeStorageWrites() {
	b.sw.mu.Lock()
	defer b.sw.mu.Unlock()
	if len(b.sw.ops) == 0 || b.sw.inflight {
		return
	}
	b.sw.failures = 0
	b.scheduleStorageFlushLocked(storageWriteDelay)
}

func (b *Backend) scheduleStorageFlushLocked(d time.Duration) {
	if b.sw.timer != nil {
		b.sw.timer.Stop()
	}
	b.sw.timer = time.AfterFunc(d, func() { b.goBackground(b.flushStorage) })
}

// storageWritePending reports whether operations are queued or in flight.
func (b *Backend) storageWritePending() bool {
	b.sw.mu.Lock()
	defer b.sw.mu.Unlock()
	return len(b.sw.ops) > 0 || b.sw.inflight
}

// flushStorage writes all queued operations in one storage write.
func (b *Backend) flushStorage() { b.flushStorageWithin(3 * time.Minute) }

// flushStorageOnClose writes queued operations before shutdown, briefly.
func (b *Backend) flushStorageOnClose() {
	b.sw.mu.Lock()
	if b.sw.timer != nil {
		b.sw.timer.Stop()
	}
	pending := len(b.sw.ops) > 0
	b.sw.mu.Unlock()
	if pending && b.ConnState() == core.ConnConnected {
		b.flushStorageWithin(5 * time.Second)
	}
}

func (b *Backend) flushStorageWithin(timeout time.Duration) {
	b.sw.applyMu.Lock()
	defer b.sw.applyMu.Unlock()
	b.sw.mu.Lock()
	ops := b.sw.ops
	b.sw.ops = nil
	b.sw.written = ops
	b.sw.inflight = len(ops) > 0
	b.sw.mu.Unlock()
	if len(ops) == 0 {
		return
	}
	defer func() {
		b.sw.mu.Lock()
		b.sw.inflight = false
		b.sw.written = nil
		b.sw.mu.Unlock()
	}()
	// done drops the written batch from the persisted queue.
	done := func() {
		b.sw.mu.Lock()
		b.sw.written = nil
		b.persistStorageLocked()
		b.sw.mu.Unlock()
	}
	ctx, cancel := context.WithTimeout(b.log.WithContext(b.ctx), timeout)
	defer cancel()
	log := b.log.With().Str("action", "storage write").Int("ops", len(ops)).Logger()
	cli, err := b.client()
	if err != nil {
		if errors.Is(err, ErrClosed) {
			return // still persisted; replayed on the next start
		}
		b.retryStorage(ops, err)
		return
	}
	if len(cli.Store.MasterKey) == 0 {
		log.Warn().Msg("No storage key yet; changes stay on this device")
		done()
		return
	}
	var known uint64
	_, _ = b.kv.get(ctx, "storage_version", &known)
	env := &storageEnv{ctx: ctx, cli: cli, self: cli.Store.ACI}
	res, err := cli.StorageWrite(ctx, func(st *signalmeow.StorageState) error {
		st.Now = b.now
		for _, op := range ops {
			if err := op.apply(st, env); err != nil {
				// One unresolvable change must not block the others.
				log.Debug().Err(err).Str("op", op.desc).Msg("Skipping storage change")
			}
		}
		return nil
	})
	switch {
	case errors.Is(err, signalmeow.ErrStorageNoManifest), errors.Is(err, signalmeow.ErrNoStorageKey):
		log.Warn().Err(err).Msg("Storage service not writable; changes stay on this device")
		done()
		return
	case err != nil:
		b.retryStorage(ops, err)
		return
	}
	b.sw.mu.Lock()
	b.sw.failures = 0
	b.sw.mu.Unlock()
	done()
	if !res.Changed {
		b.kickStorage()
		return
	}
	if res.BaseVersion == known {
		// Our store already mirrors the base version plus these changes.
		_ = b.kv.set(ctx, "storage_version", res.Version)
	} else {
		// Another device wrote since our last read; pick up its changes.
		b.kickStorage()
	}
	notify := b.sw.notify
	if notify == nil {
		notify = b.sendStorageFetchLatest
	}
	if err := notify(ctx, cli); err != nil {
		log.Debug().Err(err).Msg("Failed to ask other devices to refetch storage")
	}
}

// retryStorage puts failed operations back in front of the queue. After
// maxStorageWriteFailures attempts the queue is parked: it stays queued
// (and persisted, so storage reads still leave those fields alone) and is
// retried on the next reconnect, local change or start.
func (b *Backend) retryStorage(ops []storageOp, err error) {
	b.sw.mu.Lock()
	defer b.sw.mu.Unlock()
	b.sw.failures++
	b.sw.ops = append(ops, b.sw.ops...)
	b.sw.written = nil
	b.persistStorageLocked()
	if b.sw.failures > maxStorageWriteFailures {
		b.log.Warn().Err(err).Int("ops", len(b.sw.ops)).Msg("Storage write keeps failing; will retry after reconnecting")
		b.sw.failures = 0
		if b.sw.timer != nil {
			b.sw.timer.Stop()
		}
		return
	}
	b.log.Info().Err(err).Int("attempt", b.sw.failures).Msg("Storage write failed, will retry")
	b.scheduleStorageFlushLocked(time.Duration(b.sw.failures) * storageRetryDelay)
}

// sendStorageFetchLatest asks our other devices to refetch the manifest.
func (b *Backend) sendStorageFetchLatest(ctx context.Context, cli *signalmeow.Client) error {
	return b.sendSync(ctx, cli, &signalpb.SyncMessage{Content: &signalpb.SyncMessage_FetchLatest_{FetchLatest: &signalpb.SyncMessage_FetchLatest{
		Type: signalpb.SyncMessage_FetchLatest_STORAGE_MANIFEST.Enum(),
	}}})
}

// --- operations ---------------------------------------------------------------

// chatStorageFields are storage-synced chat fields; nil = unchanged.
type chatStorageFields struct {
	Archived     *bool
	MarkedUnread *bool
	MutedUntil   *time.Time
	Pinned       *bool
	Blocked      *bool
	Accepted     *bool // message request accepted (whitelisted / profile sharing)
}

func (f chatStorageFields) recordFields() bool {
	return f.Archived != nil || f.MarkedUnread != nil || f.MutedUntil != nil || f.Blocked != nil || f.Accepted != nil
}

func (f chatStorageFields) empty() bool { return !f.recordFields() && f.Pinned == nil }

// muteToProto converts a mute end to the storage encoding (Desktop's
// MuteExpiration.toProto): 0 = not muted, MaxInt64 = forever.
func muteToProto(t time.Time) uint64 {
	switch {
	case t.IsZero() || t.UnixMilli() <= 0:
		return 0
	case !t.Before(foreverMuted):
		return math.MaxInt64
	}
	return uint64(t.UnixMilli())
}

// chatStorageOp writes chat state to the contact, group or (for Note to
// Self) account record. seed initialises a record the service lacks.
func chatStorageOp(id core.ChatID, seed recordSeed, f chatStorageFields) (storageOp, bool) {
	if f.empty() || (!id.IsDirect() && !id.IsGroup()) {
		return storageOp{}, false
	}
	spec := storageSpec{Kind: specChat, Chat: id, Seed: seed, Fields: &f}
	return storageOp{desc: "chat " + string(id), spec: spec, apply: func(st *signalmeow.StorageState, env *storageEnv) error {
		now := time.Now()
		if st.Now != nil {
			now = st.Now()
		}
		if id.IsDirect() {
			u, err := parseACI(id.ACI())
			if err != nil {
				return err
			}
			if u == env.self {
				a := st.Account()
				if a == nil {
					return errors.New("no account record")
				}
				if f.Archived != nil {
					a.NoteToSelfArchived = *f.Archived
				}
				if f.MarkedUnread != nil {
					a.NoteToSelfMarkedUnread = *f.MarkedUnread
				}
				if f.Pinned != nil {
					signalmeow.SetPinned(a, signalmeow.PinnedTarget{ACI: u, E164: seed.E164}, *f.Pinned)
				}
				return nil
			}
			if f.recordFields() {
				c := st.UpsertContact(u, func(c *signalpb.ContactRecord) { env.seedContact(c, u, seed) })
				if f.Archived != nil {
					c.Archived = *f.Archived
				}
				if f.MarkedUnread != nil {
					c.MarkedUnread = *f.MarkedUnread
				}
				if f.MutedUntil != nil {
					c.MutedUntilTimestamp = muteToProto(*f.MutedUntil)
				}
				if f.Blocked != nil {
					signalmeow.SetContactBlocked(c, *f.Blocked, now)
				}
				if f.Accepted != nil && *f.Accepted {
					signalmeow.SetContactBlocked(c, false, now)
				}
			}
			if f.Pinned != nil {
				a := st.Account()
				if a == nil {
					return errors.New("no account record")
				}
				signalmeow.SetPinned(a, signalmeow.PinnedTarget{ACI: u, E164: seed.E164}, *f.Pinned)
			}
			return nil
		}
		mk, err := env.groupMasterKey(st, id)
		if err != nil {
			return err
		}
		if f.recordFields() {
			isNew := st.GroupV2(mk) == nil
			g := st.UpsertGroupV2(mk)
			if isNew {
				g.Whitelisted = seed.Whitelisted // see recordSeed
			}
			if f.Archived != nil {
				g.Archived = *f.Archived
			}
			if f.MarkedUnread != nil {
				g.MarkedUnread = *f.MarkedUnread
			}
			if f.MutedUntil != nil {
				g.MutedUntilTimestamp = muteToProto(*f.MutedUntil)
			}
			if f.Blocked != nil {
				signalmeow.SetGroupBlocked(g, *f.Blocked, now)
			}
			if f.Accepted != nil && *f.Accepted {
				signalmeow.SetGroupBlocked(g, false, now)
			}
		}
		if f.Pinned != nil {
			a := st.Account()
			if a == nil {
				return errors.New("no account record")
			}
			signalmeow.SetPinned(a, signalmeow.PinnedTarget{GroupMasterKey: mk}, *f.Pinned)
		}
		return nil
	}}, true
}

// nicknameStorageOp sets (or clears) a contact's nickname. The record has
// given and family parts; warte has one string, written as the given name
// unless it already matches the record's "given family".
func nicknameStorageOp(aci core.ACI, seed recordSeed, nickname string) storageOp {
	spec := storageSpec{Kind: specNickname, ACI: aci, Seed: seed, Nickname: nickname}
	return storageOp{desc: "nickname", spec: spec, apply: func(st *signalmeow.StorageState, env *storageEnv) error {
		u, err := parseACI(aci)
		if err != nil {
			return err
		}
		if u == env.self {
			return nil // no nickname for ourselves
		}
		c := st.Contact(u)
		if c == nil {
			if nickname == "" {
				return nil
			}
			c = st.UpsertContact(u, func(c *signalpb.ContactRecord) { env.seedContact(c, u, seed) })
		}
		if nickname == "" {
			c.Nickname = nil
			return nil
		}
		if n := c.GetNickname(); n != nil && strings.TrimSpace(n.GetGiven()+" "+n.GetFamily()) == nickname {
			return nil
		}
		c.Nickname = &signalpb.ContactRecord_Name{Given: nickname}
		return nil
	}}
}

// accountSettings maps a persisted setting name to how it is written to
// the account record.
var accountSettingWriters = map[string]func(a *signalpb.AccountRecord, v storageSpec){
	"read_receipts":            func(a *signalpb.AccountRecord, v storageSpec) { a.ReadReceipts = v.Bool },
	"typing_indicators":        func(a *signalpb.AccountRecord, v storageSpec) { a.TypingIndicators = v.Bool },
	"link_previews":            func(a *signalpb.AccountRecord, v storageSpec) { a.LinkPreviews = v.Bool },
	"sealed_sender_indicators": func(a *signalpb.AccountRecord, v storageSpec) { a.SealedSenderIndicators = v.Bool },
	"universal_expire_timer": func(a *signalpb.AccountRecord, v storageSpec) {
		a.UniversalExpireTimer = uint32(min(v.Uint, math.MaxUint32))
	},
	"phone_number_sharing": func(a *signalpb.AccountRecord, v storageSpec) {
		a.PhoneNumberSharingMode = signalpb.AccountRecord_NOBODY
		if v.Bool {
			a.PhoneNumberSharingMode = signalpb.AccountRecord_EVERYBODY
		}
	},
	"unlisted_phone_number": func(a *signalpb.AccountRecord, v storageSpec) { a.UnlistedPhoneNumber = v.Bool },
}

// settingStorageOp writes one account setting (v carries Bool or Uint).
func settingStorageOp(name string, v storageSpec) (storageOp, bool) {
	fn, ok := accountSettingWriters[name]
	if !ok {
		return storageOp{}, false
	}
	spec := storageSpec{Kind: specSetting, Setting: name, Bool: v.Bool, Uint: v.Uint}
	return storageOp{desc: name, spec: spec, apply: func(st *signalmeow.StorageState, _ *storageEnv) error {
		a := st.Account()
		if a == nil {
			return errors.New("no account record")
		}
		fn(a, spec)
		return nil
	}}, true
}

// settingsStorageOps writes the account settings that changed between old
// and new (unchanged settings are left as the phone has them). The
// notification level is local only, as in Signal Desktop.
func settingsStorageOps(old, s core.Settings) []storageOp {
	var ops []storageOp
	add := func(name string, v storageSpec) {
		if op, ok := settingStorageOp(name, v); ok {
			ops = append(ops, op)
		}
	}
	if old.ReadReceipts != s.ReadReceipts {
		add("read_receipts", storageSpec{Bool: s.ReadReceipts})
	}
	if old.TypingIndicators != s.TypingIndicators {
		add("typing_indicators", storageSpec{Bool: s.TypingIndicators})
	}
	if old.LinkPreviews != s.LinkPreviews {
		add("link_previews", storageSpec{Bool: s.LinkPreviews})
	}
	if old.SealedSenderNotice != s.SealedSenderNotice {
		add("sealed_sender_indicators", storageSpec{Bool: s.SealedSenderNotice})
	}
	if old.DefaultExpireTimer != s.DefaultExpireTimer {
		add("universal_expire_timer", storageSpec{Uint: uint64(max(s.DefaultExpireTimer, 0) / time.Second)})
	}
	if old.PhoneNumberSharing != s.PhoneNumberSharing {
		add("phone_number_sharing", storageSpec{Bool: s.PhoneNumberSharing})
	}
	if old.DiscoverableByPhone != s.DiscoverableByPhone {
		add("unlisted_phone_number", storageSpec{Bool: !s.DiscoverableByPhone})
	}
	return ops
}

// stickerPackStorageOp installs or removes a sticker pack record.
func stickerPackStorageOp(packID, packKey []byte, install bool) storageOp {
	spec := storageSpec{Kind: specSticker, PackID: packID, PackKey: packKey, Install: install}
	return storageOp{desc: "sticker pack", spec: spec, apply: func(st *signalmeow.StorageState, _ *storageEnv) error {
		st.SetStickerPackInstalled(packID, packKey, install)
		return nil
	}}
}

// queueChatStorage queues a chat state change.
func (b *Backend) queueChatStorage(ctx context.Context, id core.ChatID, f chatStorageFields) {
	if op, ok := chatStorageOp(id, b.recordSeed(ctx, id), f); ok {
		b.queueStorage(op)
	}
}

// recordSeed captures, at queue time, what a new storage record for id
// should start with: the phone number and whether the chat is accepted
// (profile sharing on), i.e. it exists, is not blocked and is not a
// pending message request.
func (b *Backend) recordSeed(ctx context.Context, id core.ChatID) recordSeed {
	var seed recordSeed
	if id.IsDirect() {
		if c, err := b.db.GetContact(ctx, id.ACI()); err == nil {
			seed.E164 = c.E164
		}
	}
	if c, err := b.db.GetChat(ctx, id); err == nil {
		seed.Whitelisted = !c.Blocked && !c.MessageRequest
	}
	return seed
}
