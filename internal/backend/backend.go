package backend

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"go.mau.fi/util/dbutil"

	"go.mau.fi/mautrix-signal/pkg/signalmeow"
	mstore "go.mau.fi/mautrix-signal/pkg/signalmeow/store"

	"warte/internal/core"
	"warte/internal/store"
)

var _ core.Service = (*Backend)(nil)

// DefaultDeviceName is the linked-device name shown on the phone.
const DefaultDeviceName = "warte"

// ErrClosed is returned by methods called after Close.
var ErrClosed = errors.New("backend: closed")

// Options configure New and NewLinker.
type Options struct {
	// DataDir holds warte.db and blobs/ when Store or Blobs are not given.
	DataDir string
	// Store is warte's database, shared with signalmeow. If nil it is opened
	// at DataDir/warte.db with DBKey and closed by Close.
	Store *store.DB
	// Blobs is the encrypted attachment store. If nil it is opened at
	// DataDir/blobs with BlobKey and closed by Close.
	Blobs *store.Blobs
	// DBKey is the 32-byte database key (only used to open Store).
	DBKey []byte
	// BlobKey is the 32-byte blob key. It opens Blobs and also derives the
	// key that encrypts ExportBackup files, so it is required for export.
	BlobKey []byte
	// DeviceName is the name shown in the phone's linked devices list.
	DeviceName string
	// Logger receives diagnostics. Message bodies and phone numbers are
	// never logged.
	Logger zerolog.Logger
	// Clock overrides time.Now (tests).
	Clock func() time.Time
}

// Backend is the real core.Service: Signal via signalmeow, persisted in
// warte's store. Create it with New, then call Start.
type Backend struct {
	opts  Options
	log   zerolog.Logger
	now   func() time.Time
	db    *store.DB
	blobs *store.Blobs
	kv    kv
	index *msgIndex

	container *mstore.Container
	ownDB     bool
	ownBlobs  bool

	events *eventQueue

	ctx    context.Context // lifetime of the backend
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.RWMutex
	cli     *signalmeow.Client
	self    core.Contact
	conn    core.ConnState
	started bool
	closed  bool

	// loopMu serialises starting and stopping signalmeow's receive loops.
	loopMu sync.Mutex

	// handleMu serialises processing of incoming Signal events so state
	// changes (edits, receipts, deletes) apply in arrival order.
	handleMu sync.Mutex

	lastTS atomic.Uint64

	downloads  chan downloadJob
	wakeExpiry chan struct{}
	profiles   chan core.ACI
	groupSync  chan core.ChatID
	// storageKick asks maintenanceLoop to re-read the storage service now
	// (keys or fetch-latest syncs).
	storageKick chan struct{}
	// sw queues chat state, settings and sticker changes for the storage
	// service (storage_write.go).
	sw storageWriter

	typingMu sync.Mutex
	typing   map[typingKey]*time.Timer

	callMu  sync.Mutex
	calls   map[core.ChatID]*core.Message // ringing calls by chat
	callIDs map[core.ChatID]uint64        // call/ring ID of each ringing call (0 = unknown)
	// ringTimeout ends a ringing call as missed if no hangup arrives.
	ringTimeout time.Duration

	sendMu   sync.Mutex
	sendLock map[core.ChatID]*sync.Mutex // keeps per-chat send order
}

type typingKey struct {
	chat core.ChatID
	who  core.ACI
}

// New opens (or adopts) warte's store, prepares signalmeow's tables in the
// same database and returns a backend that is not yet connected.
func New(ctx context.Context, opts Options) (*Backend, error) {
	if opts.DeviceName == "" {
		opts.DeviceName = DefaultDeviceName
	}
	opts.Logger = privateLogger(opts.Logger)
	b := &Backend{
		opts:        opts,
		log:         opts.Logger.With().Str("component", "backend").Logger(),
		now:         opts.Clock,
		events:      newEventQueue(),
		downloads:   make(chan downloadJob, 256),
		wakeExpiry:  make(chan struct{}, 1),
		profiles:    make(chan core.ACI, 512),
		groupSync:   make(chan core.ChatID, 128),
		typing:      map[typingKey]*time.Timer{},
		calls:       map[core.ChatID]*core.Message{},
		callIDs:     map[core.ChatID]uint64{},
		storageKick: make(chan struct{}, 1),
		ringTimeout: maxRing,
		sendLock:    map[core.ChatID]*sync.Mutex{},
	}
	if b.now == nil {
		b.now = time.Now
	}
	b.ctx, b.cancel = context.WithCancel(context.Background())
	if err := b.openStores(ctx); err != nil {
		b.events.close()
		b.cancel()
		return nil, err
	}
	b.loadPendingStorage(ctx) // warte: durable storage write queue
	return b, nil
}

// openStores opens or adopts the database and blob store and prepares
// signalmeow's container and the backend index.
func (b *Backend) openStores(ctx context.Context) error {
	var err error
	b.db, b.ownDB, b.blobs, b.ownBlobs, err = openStores(b.opts)
	if err != nil {
		return err
	}
	b.kv = kv{db: b.db}
	if b.index, err = newMsgIndex(ctx, b.db.RawDB()); err != nil {
		b.closeStores()
		return err
	}
	if b.container, err = openContainer(ctx, b.db, b.opts.Logger); err != nil {
		b.closeStores()
		return err
	}
	return nil
}

// openDB opens the database unless opts supplies one.
func openDB(opts Options) (*store.DB, bool, error) {
	if opts.Store != nil {
		return opts.Store, false, nil
	}
	if opts.DataDir == "" || len(opts.DBKey) == 0 {
		return nil, false, errors.New("backend: need Options.Store or DataDir and DBKey")
	}
	var sopts []store.Option
	if opts.Clock != nil {
		sopts = append(sopts, store.WithClock(opts.Clock))
	}
	db, err := store.Open(filepath.Join(opts.DataDir, "warte.db"), opts.DBKey, sopts...)
	if err != nil {
		return nil, false, err
	}
	return db, true, nil
}

// openStores opens whatever opts does not supply.
func openStores(opts Options) (db *store.DB, ownDB bool, blobs *store.Blobs, ownBlobs bool, err error) {
	if db, ownDB, err = openDB(opts); err != nil {
		return nil, false, nil, false, err
	}
	blobs = opts.Blobs
	if blobs == nil {
		if opts.DataDir == "" || len(opts.BlobKey) == 0 {
			err = errors.New("backend: need Options.Blobs or DataDir and BlobKey")
		} else {
			blobs, err = store.OpenBlobs(filepath.Join(opts.DataDir, "blobs"), opts.BlobKey)
			ownBlobs = true
		}
		if err != nil {
			if ownDB {
				_ = db.Close()
			}
			return nil, false, nil, false, err
		}
	}
	return db, ownDB, blobs, ownBlobs, nil
}

var ffiLoggerOnce sync.Once

// openContainer wraps the shared database for signalmeow and runs its
// schema upgrades.
func openContainer(ctx context.Context, db *store.DB, log zerolog.Logger) (*mstore.Container, error) {
	ffiLoggerOnce.Do(func() { signalmeow.SetLogger(log.With().Str("component", "libsignal").Logger()) })
	dbu, err := db.DBUtil()
	if err != nil {
		return nil, fmt.Errorf("backend: wrap database: %w", err)
	}
	c := mstore.NewStore(dbu, dbutil.ZeroLogger(log.With().Str("component", "signalmeow-db").Logger()))
	if err := c.Upgrade(ctx); err != nil {
		return nil, fmt.Errorf("backend: signalmeow schema: %w", err)
	}
	return c, nil
}

func (b *Backend) closeStores() {
	if b.ownBlobs {
		b.blobs = nil
	}
	if b.ownDB && b.db != nil {
		_ = b.db.Close()
	}
}

// loadDevice returns the linked device stored in the container.
func loadDevice(ctx context.Context, c *mstore.Container) (*mstore.Device, error) {
	devs, err := c.GetAllDevices(ctx)
	if err != nil {
		return nil, fmt.Errorf("backend: load device: %w", err)
	}
	for _, d := range devs {
		if d.IsDeviceLoggedIn() {
			return d, nil
		}
	}
	return nil, core.ErrNotLoggedIn
}

// Start loads the linked device, connects to Signal in the background and
// starts the background jobs. It returns core.ErrNotLoggedIn if no device is
// linked (run a Linker first).
func (b *Backend) Start(ctx context.Context) error {
	b.mu.Lock()
	switch {
	case b.closed:
		b.mu.Unlock()
		return ErrClosed
	case b.started:
		b.mu.Unlock()
		return nil
	}
	b.mu.Unlock()

	dev, err := loadDevice(ctx, b.container)
	if err != nil {
		return err
	}
	cli := signalmeow.NewClient(dev, b.opts.Logger.With().Str("component", "signalmeow").Logger(), b.handleSignalEvent)
	var lastContactSync time.Time
	_, _ = b.kv.get(ctx, "contacts_synced_at", &lastContactSync)
	cli.SyncContactsOnConnect = b.now().Sub(lastContactSync) > 3*24*time.Hour

	self, err := b.initSelf(ctx, dev)
	if err != nil {
		return err
	}
	if s, err := b.db.Settings(ctx); err == nil {
		applySettingsToAccount(cli, s)
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return ErrClosed
	}
	b.cli, b.self, b.started, b.conn = cli, self, true, core.ConnConnecting
	b.mu.Unlock()
	b.emit(core.ConnChanged{State: core.ConnConnecting})

	b.goBackground(func() { b.connectLoop(cli) })
	b.goBackground(b.expiryLoop)
	for range downloadWorkers {
		b.goBackground(b.downloadWorker)
	}
	b.goBackground(b.profileWorker)
	b.goBackground(b.groupWorker)
	b.goBackground(b.maintenanceLoop)
	return nil
}

// initSelf ensures the self contact and Note to Self chat exist.
func (b *Backend) initSelf(ctx context.Context, dev *mstore.Device) (core.Contact, error) {
	aci := aciOf(dev.ACI)
	existing, err := b.db.GetContact(ctx, aci)
	if err != nil && !errors.Is(err, core.ErrNotFound) {
		return core.Contact{}, err
	}
	rec, _ := dev.RecipientStore.LoadAndUpdateRecipient(ctx, dev.ACI, uuid.Nil, nil)
	self := selfContact(dev.ACI, dev.Number, rec, existing)
	if err := b.db.UpsertContact(ctx, self); err != nil {
		return core.Contact{}, err
	}
	nts := core.DirectChatID(aci)
	if _, err := b.db.GetChat(ctx, nts); errors.Is(err, core.ErrNotFound) {
		if err := b.db.UpsertChat(ctx, core.Chat{ID: nts, Kind: core.ChatNoteToSelf, Title: "Note to Self"}); err != nil {
			return core.Contact{}, err
		}
	}
	return self, nil
}

// goBackground runs fn on a tracked goroutine (not after Close).
func (b *Backend) goBackground(fn func()) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed {
		return
	}
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		defer func() {
			if v := recover(); v != nil {
				_ = b.recovered("background", v)
			}
		}()
		fn()
	}()
}

// Close disconnects, stops background work, closes the event channel and
// releases stores the backend opened itself.
func (b *Backend) Close() error {
	b.mu.RLock()
	closed := b.closed
	b.mu.RUnlock()
	if !closed {
		b.flushStorageOnClose()
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	b.mu.Unlock()

	// Cancelling makes connectLoop stop signalmeow's loops on its way out.
	b.cancel()
	b.wg.Wait()
	// Wait for a signalmeow callback that is still inside the handler; later
	// ones see the cancelled context and return before touching the store.
	b.handleMu.Lock()
	b.handleMu.Unlock() //nolint:staticcheck // barrier, not a critical section
	b.typingMu.Lock()
	for k, t := range b.typing {
		t.Stop()
		delete(b.typing, k)
	}
	b.typingMu.Unlock()
	b.events.close()
	b.closeStores()
	return nil
}

// Events returns the event stream. It is closed by Close.
func (b *Backend) Events() <-chan core.Event { return b.events.out }

// Self returns the linked account's contact (zero before Start).
func (b *Backend) Self() core.Contact {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.self
}

// IsLinked reports whether a linked device is stored locally. It reads
// signalmeow's device table only and never connects to Signal, so it works
// offline and before Start.
func (b *Backend) IsLinked(ctx context.Context) (bool, error) {
	_, err := loadDevice(ctx, b.container)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, core.ErrNotLoggedIn):
		return false, nil
	}
	return false, err
}

// localSelf returns our own contact: the started backend's, or else the
// one stored for the locally linked device (without connecting). It is
// empty when no device is linked.
func (b *Backend) localSelf(ctx context.Context) core.Contact {
	if s := b.Self(); s.ACI != "" {
		return s
	}
	dev, err := loadDevice(ctx, b.container)
	if err != nil {
		return core.Contact{}
	}
	aci := aciOf(dev.ACI)
	if c, err := b.db.GetContact(ctx, aci); err == nil {
		c.IsSelf = true
		return c
	}
	return core.Contact{ACI: aci, IsSelf: true}
}

// ConnState reports the connection state.
func (b *Backend) ConnState() core.ConnState {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.conn
}

func (b *Backend) setConn(s core.ConnState) {
	b.mu.Lock()
	changed := b.conn != s
	b.conn = s
	b.mu.Unlock()
	if changed {
		b.emit(core.ConnChanged{State: s})
	}
}

// client returns the signalmeow client, or an error if not started/closed.
func (b *Backend) client() (*signalmeow.Client, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	switch {
	case b.closed:
		return nil, ErrClosed
	case b.cli == nil:
		return nil, core.ErrNotLoggedIn
	case b.conn == core.ConnLoggedOut:
		return nil, core.ErrNotLoggedIn
	}
	return b.cli, nil
}

// selfACI returns our ACI ("" before Start).
func (b *Backend) selfACI() core.ACI {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.self.ACI
}

func (b *Backend) emit(evs ...core.Event) { b.events.push(evs...) }

// nextTimestamp returns a unique, monotonically increasing ms timestamp for
// outgoing messages.
func (b *Backend) nextTimestamp() uint64 {
	for {
		now := uint64(b.now().UnixMilli())
		last := b.lastTS.Load()
		next := max(now, last+1)
		if b.lastTS.CompareAndSwap(last, next) {
			return next
		}
	}
}

// --- queries --------------------------------------------------------------

// Chats lists chats (local state).
func (b *Backend) Chats(ctx context.Context, f core.ChatFilter) ([]core.Chat, error) {
	return b.db.ListChats(ctx, f)
}

// Chat returns one chat.
func (b *Backend) Chat(ctx context.Context, id core.ChatID) (core.Chat, error) {
	return b.db.GetChat(ctx, id)
}

// Messages pages through a chat, oldest → newest.
func (b *Backend) Messages(ctx context.Context, chat core.ChatID, before core.MessageID, limit int) ([]core.Message, error) {
	return b.db.ListMessages(ctx, chat, before, limit)
}

// MessagesAround returns a window centred on id.
func (b *Backend) MessagesAround(ctx context.Context, id core.MessageID, limit int) ([]core.Message, error) {
	return b.db.MessagesAround(ctx, id, limit)
}

// Message returns one message.
func (b *Backend) Message(ctx context.Context, id core.MessageID) (core.Message, error) {
	return b.db.GetMessage(ctx, id)
}

// Search runs a full-text search.
func (b *Backend) Search(ctx context.Context, q core.SearchQuery) ([]core.SearchHit, error) {
	return b.db.Search(ctx, q)
}

// Contacts lists known contacts.
func (b *Backend) Contacts(ctx context.Context) ([]core.Contact, error) {
	return b.db.ListContacts(ctx)
}

// Contact returns one contact.
func (b *Backend) Contact(ctx context.Context, aci core.ACI) (core.Contact, error) {
	return b.db.GetContact(ctx, aci)
}

// Stories is in stories.go.

// CallLog lists the call history, newest first.
func (b *Backend) CallLog(ctx context.Context) ([]core.CallLogEntry, error) {
	return b.db.ListCalls(ctx, 0)
}

// StickerPacks lists known sticker packs, installed ones first.
func (b *Backend) StickerPacks(ctx context.Context) ([]core.StickerPack, error) {
	return b.db.ListStickerPacks(ctx, false)
}

// Folders lists chat folders.
func (b *Backend) Folders(ctx context.Context) ([]core.Folder, error) { return b.db.ListFolders(ctx) }

// Settings returns the account settings.
func (b *Backend) Settings(ctx context.Context) (core.Settings, error) { return b.db.Settings(ctx) }

// storyLifetime is how long stories stay visible.
const storyLifetime = 24 * time.Hour

// --- shared helpers -----------------------------------------------------------

// nameOf resolves a display name for system text and events.
func (b *Backend) nameOf(ctx context.Context, aci core.ACI) string {
	if aci == "" {
		return "Unknown"
	}
	c, err := b.db.GetContact(ctx, aci)
	if err != nil {
		return "Unknown"
	}
	return c.DisplayName()
}

// names returns a nameFunc bound to ctx.
func (b *Backend) names(ctx context.Context) nameFunc {
	return func(a core.ACI) string { return b.nameOf(ctx, a) }
}

// emitChat emits ChatUpdated with the stored chat.
func (b *Backend) emitChat(ctx context.Context, id core.ChatID) {
	c, err := b.db.GetChat(ctx, id)
	if err != nil {
		return
	}
	b.emit(core.ChatUpdated{Chat: c})
}

// emitMessage emits MessageUpdated with the stored message.
func (b *Backend) emitMessage(ctx context.Context, id core.MessageID) {
	m, err := b.db.GetMessage(ctx, id)
	if err != nil {
		return
	}
	b.emit(core.MessageUpdated{Message: m})
}

// chatLock returns the per-chat send mutex.
func (b *Backend) chatLock(chat core.ChatID) *sync.Mutex {
	b.sendMu.Lock()
	defer b.sendMu.Unlock()
	l, ok := b.sendLock[chat]
	if !ok {
		l = &sync.Mutex{}
		b.sendLock[chat] = l
	}
	return l
}

// errorEvent reports a background failure to the frontend.
func (b *Backend) errorEvent(op string, err error) {
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	b.log.Warn().Err(err).Str("op", op).Msg("Background operation failed")
	b.emit(core.ErrorEvent{Op: op, Err: err})
}
