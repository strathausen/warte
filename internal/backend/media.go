package backend

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"  // register decoders for dimension sniffing
	_ "image/jpeg" // ditto
	_ "image/png"  // ditto
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	_ "golang.org/x/image/webp" // register the WebP decoder (stickers)
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-signal/pkg/libsignalgo"
	"go.mau.fi/mautrix-signal/pkg/signalmeow"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/types"

	"warte/internal/core"
	"warte/internal/store"
)

const (
	// eagerDownloadLimit is the size up to which images and stickers are
	// fetched as soon as they arrive.
	eagerDownloadLimit = 2 << 20
	// downloadWorkers is the number of concurrent background downloads.
	downloadWorkers = 3
	// maxLongText bounds a long-text attachment we substitute for the body.
	maxLongText = 1 << 20
)

// errUnavailable is returned for attachments that can no longer be fetched.
var errUnavailable = errors.New("attachment is no longer available")

// remoteRef is the JSON stored in AttachmentRecord.Remote for message
// attachments.
type remoteRef struct {
	Pointer []byte `json:"p"`           // marshaled signalpb.AttachmentPointer
	Hash    []byte `json:"h,omitempty"` // plaintext hash (history transfer)
	Role    string `json:"r"`
}

func encodeRemote(r attachmentRef) ([]byte, error) {
	p, err := proto.Marshal(r.Pointer)
	if err != nil {
		return nil, fmt.Errorf("backend: encode attachment pointer: %w", err)
	}
	return json.Marshal(remoteRef{Pointer: p, Hash: r.PlaintextHash, Role: string(r.Role)})
}

func decodeRemote(b []byte) (attachmentRef, error) {
	var rr remoteRef
	if err := json.Unmarshal(b, &rr); err != nil {
		return attachmentRef{}, fmt.Errorf("backend: decode attachment pointer: %w", err)
	}
	p := &signalpb.AttachmentPointer{}
	if err := proto.Unmarshal(rr.Pointer, p); err != nil {
		return attachmentRef{}, fmt.Errorf("backend: decode attachment pointer: %w", err)
	}
	return attachmentRef{Pointer: p, PlaintextHash: rr.Hash, Role: attRole(rr.Role)}, nil
}

// avatarSource is the JSON stored in AttachmentRecord.Remote for avatars.
type avatarSource struct {
	Kind  string   `json:"k"` // profile | contact | group
	ACI   core.ACI `json:"a,omitempty"`
	Path  string   `json:"p,omitempty"` // CDN path (profile, group)
	Group string   `json:"g,omitempty"` // group identifier
}

// downloadJob asks a worker to fetch one attachment of a message.
type downloadJob struct {
	msg core.MessageID
	ref attachmentRef
}

// inflight deduplicates concurrent downloads of the same attachment.
var inflight = struct {
	sync.Mutex
	m map[string]*flight
}{m: map[string]*flight{}}

type flight struct {
	done chan struct{}
	data []byte
	err  error
}

// queueDownload schedules a background download; it never blocks (a full
// queue leaves the attachment to be fetched lazily).
func (b *Backend) queueDownload(j downloadJob) {
	select {
	case b.downloads <- j:
	default:
	}
}

// scheduleDownloads queues the eager downloads of a new message: long text,
// stickers, small images, link preview images and quote thumbnails.
func (b *Backend) scheduleDownloads(m core.Message, conv converted) {
	for _, r := range conv.Atts {
		size := int64(r.Pointer.GetSize())
		ct := r.Pointer.GetContentType()
		// The declared size bounds what the download may allocate (see
		// signalmeow.MaxAttachmentCiphertextSize), so an unknown (zero) or
		// large size is never fetched without the user asking.
		eager := false
		switch r.Role {
		case roleLongText:
			eager = size <= maxLongText
		case rolePreview, roleQuote, roleSticker:
			eager = size <= eagerDownloadLimit
		case roleBody:
			eager = !m.ViewOnce && strings.HasPrefix(ct, "image/") && size <= eagerDownloadLimit
		}
		eager = eager && size > 0
		if eager && hasLocation(r.Pointer) {
			b.queueDownload(downloadJob{msg: m.ID, ref: r})
		}
	}
}

func (b *Backend) downloadWorker() {
	ctx := b.log.WithContext(b.ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-b.downloads:
			if _, err := b.fetchAttachment(ctx, j.msg, j.ref.ID); err != nil && !errors.Is(err, context.Canceled) {
				zerolog.Ctx(ctx).Debug().Err(err).Str("attachment", j.ref.ID).Msg("Background download failed")
			}
		}
	}
}

// Attachment returns an attachment's decrypted bytes, downloading and
// caching them (encrypted) if needed. View-once media is refused once
// opened.
func (b *Backend) Attachment(ctx context.Context, msg core.MessageID, attachmentID string) ([]byte, error) {
	m, err := b.db.GetMessage(ctx, msg)
	if errors.Is(err, core.ErrNotFound) {
		// Story media is addressed by the story's ID.
		if s, serr := b.db.GetStory(ctx, msg); serr == nil {
			return b.storyMedia(ctx, s, attachmentID)
		}
	}
	if err != nil {
		return nil, err
	}
	if !messageHasAttachment(m, attachmentID) {
		return nil, fmt.Errorf("attachment %s: %w", attachmentID, core.ErrNotFound)
	}
	if m.ViewOnce && m.ViewOnceOpened {
		return nil, errorf(core.ErrReadOnly, "view-once media was already opened")
	}
	return b.fetchAttachment(ctx, msg, attachmentID)
}

// messageHasAttachment reports whether id is one of m's attachments.
func messageHasAttachment(m core.Message, id string) bool {
	found := false
	forEachAttachment(&m, func(a *core.Attachment) {
		if a.ID == id {
			found = true
		}
	})
	return found
}

// forEachAttachment visits every attachment slot of m.
func forEachAttachment(m *core.Message, fn func(*core.Attachment)) {
	for i := range m.Attachments {
		fn(&m.Attachments[i])
	}
	if m.Sticker != nil {
		fn(&m.Sticker.Attachment)
	}
	if m.Preview != nil && m.Preview.Image != nil {
		fn(m.Preview.Image)
	}
	if m.Quote != nil && m.Quote.Attachment != nil {
		fn(m.Quote.Attachment)
	}
}

// fetchAttachment returns cached bytes or downloads them once, even under
// concurrent callers.
func (b *Backend) fetchAttachment(ctx context.Context, msg core.MessageID, attID string) ([]byte, error) {
	rec, err := b.db.GetAttachment(ctx, attID)
	if err != nil {
		return nil, err
	}
	if rec.Downloaded && rec.BlobKey != "" {
		data, err := b.blobs.Get(rec.BlobKey)
		if err == nil {
			return data, nil
		}
		zerolog.Ctx(ctx).Warn().Err(err).Str("attachment", attID).Msg("Cached attachment unreadable, downloading again")
	}
	inflight.Lock()
	if f, ok := inflight.m[attID]; ok {
		inflight.Unlock()
		select {
		case <-f.done:
			return f.data, f.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f := &flight{done: make(chan struct{})}
	inflight.m[attID] = f
	inflight.Unlock()
	f.data, f.err = b.download(ctx, msg, rec)
	inflight.Lock()
	delete(inflight.m, attID)
	inflight.Unlock()
	close(f.done)
	return f.data, f.err
}

// download fetches, decrypts, verifies and caches one attachment, then
// updates the owning message.
func (b *Backend) download(ctx context.Context, msg core.MessageID, rec store.AttachmentRecord) ([]byte, error) {
	ref, err := decodeRemote(rec.Remote)
	if err != nil {
		return nil, err
	}
	if !hasLocation(ref.Pointer) {
		b.markAttachment(ctx, msg, rec.ID, func(a *core.Attachment) { a.Failed = true })
		return nil, errUnavailable
	}
	data, err := guard("download attachment", func() ([]byte, error) {
		return signalmeow.DownloadAttachmentWithPointer(ctx, ref.Pointer, ref.PlaintextHash, nil)
	})
	if err != nil {
		if errors.Is(err, signalmeow.ErrAttachmentNotFound) {
			b.markAttachment(ctx, msg, rec.ID, func(a *core.Attachment) { a.Failed = true })
			return nil, fmt.Errorf("%w: %w", errUnavailable, err)
		}
		return nil, fmt.Errorf("backend: download attachment: %w", err)
	}
	key, err := b.blobs.Put(data)
	if err != nil {
		return nil, err
	}
	if err := b.db.SetAttachmentBlob(ctx, rec.ID, key); err != nil {
		return nil, err
	}
	if ref.Role == roleLongText {
		b.applyLongText(ctx, msg, data)
		return data, nil
	}
	ct := ""
	if rec.ContentType == "" {
		ct = http.DetectContentType(data)
	}
	b.markAttachment(ctx, msg, rec.ID, func(a *core.Attachment) {
		a.Downloaded, a.Failed, a.Progress = true, false, 1
		if ct != "" {
			a.ContentType = ct
		}
		if a.Size == 0 {
			a.Size = int64(len(data))
		}
	})
	return data, nil
}

// markAttachment updates attachment attID of message msg and emits the
// change.
func (b *Backend) markAttachment(ctx context.Context, msg core.MessageID, attID string, fn func(*core.Attachment)) {
	m, err := b.db.UpdateMessage(ctx, msg, func(m *core.Message) error {
		forEachAttachment(m, func(a *core.Attachment) {
			if a.ID == attID {
				fn(a)
			}
		})
		return nil
	})
	if err != nil {
		return
	}
	b.emit(core.MessageUpdated{Message: m})
}

// applyLongText substitutes the full text of a truncated message body.
func (b *Backend) applyLongText(ctx context.Context, msg core.MessageID, data []byte) {
	if len(data) > maxLongText || !utf8.Valid(data) {
		return
	}
	full := string(data)
	m, err := b.db.UpdateMessage(ctx, msg, func(m *core.Message) error {
		if m.RemoteDeleted || len(full) < len(m.Body) {
			return nil
		}
		m.Body = full
		m.Ranges = trimRanges(full, m.Ranges)
		return nil
	})
	if err != nil {
		return
	}
	b.emit(core.MessageUpdated{Message: m})
	b.emitChat(ctx, msg.Chat)
}

// --- avatars ----------------------------------------------------------------

// registerAvatar records where an avatar can be fetched from. A record that
// already has the bytes is kept.
func (b *Backend) registerAvatar(ctx context.Context, id, owner string, src avatarSource) {
	if id == "" {
		return
	}
	if rec, err := b.db.GetAttachment(ctx, id); err == nil && rec.Downloaded {
		return
	}
	raw, err := json.Marshal(src)
	if err != nil {
		return
	}
	if err := b.db.PutAttachment(ctx, store.AttachmentRecord{ID: id, Owner: owner, Remote: raw}); err != nil {
		zerolog.Ctx(ctx).Debug().Err(err).Msg("Failed to register avatar")
	}
}

// cacheAvatar stores avatar bytes we already have (contact sync).
func (b *Backend) cacheAvatar(ctx context.Context, id, owner string, data []byte, contentType string, src avatarSource) {
	if id == "" || len(data) == 0 {
		return
	}
	if rec, err := b.db.GetAttachment(ctx, id); err == nil && rec.Downloaded {
		return
	}
	key, err := b.blobs.Put(data)
	if err != nil {
		return
	}
	raw, _ := json.Marshal(src)
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}
	_ = b.db.PutAttachment(ctx, store.AttachmentRecord{
		ID: id, Owner: owner, BlobKey: key, ContentType: contentType,
		Size: int64(len(data)), Downloaded: true, Remote: raw,
	})
}

// Avatar returns the decrypted bytes of a contact or group avatar.
func (b *Backend) Avatar(ctx context.Context, id string) ([]byte, error) {
	rec, err := b.db.GetAttachment(ctx, id)
	if err != nil {
		return nil, err
	}
	if rec.Downloaded && rec.BlobKey != "" {
		if data, err := b.blobs.Get(rec.BlobKey); err == nil {
			return data, nil
		}
	}
	var src avatarSource
	if err := json.Unmarshal(rec.Remote, &src); err != nil {
		return nil, fmt.Errorf("backend: avatar %s: %w", id, err)
	}
	cli, err := b.client()
	if err != nil {
		return nil, err
	}
	var data []byte
	switch src.Kind {
	case "profile":
		u, err := parseACI(src.ACI)
		if err != nil {
			return nil, err
		}
		pk, err := cli.Store.RecipientStore.LoadProfileKey(ctx, u)
		if err != nil || pk == nil {
			return nil, fmt.Errorf("backend: no profile key for avatar: %w", core.ErrNotFound)
		}
		data, err = guard("download avatar", func() ([]byte, error) { return cli.DownloadUserAvatar(ctx, src.Path, *pk) })
		if err != nil {
			return nil, fmt.Errorf("backend: download avatar: %w", err)
		}
	case "group":
		mk, err := cli.Store.GroupStore.MasterKeyFromGroupIdentifier(ctx, types.GroupIdentifier(src.Group))
		if err != nil || mk == "" {
			return nil, fmt.Errorf("backend: no master key for group avatar: %w", core.ErrNotFound)
		}
		data, err = guard("download group avatar", func() ([]byte, error) { return cli.DownloadGroupAvatar(ctx, src.Path, mk) })
		if err != nil {
			return nil, fmt.Errorf("backend: download group avatar: %w", err)
		}
	case "contact":
		u, err := parseACI(src.ACI)
		if err != nil {
			return nil, err
		}
		r, err := cli.Store.RecipientStore.LoadAndUpdateRecipient(ctx, u, uuid.Nil, nil)
		if err != nil || r == nil || len(r.ContactAvatar.Image) == 0 {
			return nil, fmt.Errorf("backend: contact avatar: %w", core.ErrNotFound)
		}
		data = r.ContactAvatar.Image
	default:
		return nil, fmt.Errorf("backend: avatar %s: %w", id, core.ErrNotFound)
	}
	key, err := b.blobs.Put(data)
	if err != nil {
		return nil, err
	}
	rec.BlobKey, rec.Downloaded, rec.Size = key, true, int64(len(data))
	rec.ContentType = http.DetectContentType(data)
	if err := b.db.PutAttachment(ctx, rec); err != nil {
		return nil, err
	}
	return data, nil
}

// --- stickers -----------------------------------------------------------------

// stickerRecordID is the attachment record ID of a sticker image.
func stickerRecordID(packID string, stickerID uint32) string {
	return "s" + strings.ToLower(packID) + "." + strconv.FormatUint(uint64(stickerID), 10)
}

// StickerImage returns the decrypted image of a sticker from a known pack.
func (b *Backend) StickerImage(ctx context.Context, packID string, stickerID uint32) ([]byte, error) {
	id := stickerRecordID(packID, stickerID)
	if rec, err := b.db.GetAttachment(ctx, id); err == nil && rec.Downloaded {
		if data, err := b.blobs.Get(rec.BlobKey); err == nil {
			return data, nil
		}
	}
	pack, err := b.db.GetStickerPack(ctx, strings.ToLower(packID))
	if err != nil {
		return nil, err
	}
	rawID, err := hex.DecodeString(pack.ID)
	if err != nil {
		return nil, fmt.Errorf("%w: sticker pack id", errBadID)
	}
	rawKey, err := hex.DecodeString(pack.Key)
	if err != nil || len(rawKey) != 32 {
		return nil, fmt.Errorf("%w: sticker pack key", errBadID)
	}
	data, err := guard("download sticker", func() ([]byte, error) {
		return signalmeow.DownloadStickerPackItem(ctx, rawID, rawKey, stickerID)
	})
	if err != nil {
		return nil, fmt.Errorf("backend: download sticker: %w", err)
	}
	key, err := b.blobs.Put(data)
	if err != nil {
		return nil, err
	}
	if err := b.db.PutAttachment(ctx, store.AttachmentRecord{
		ID: id, Owner: "sticker:" + pack.ID, BlobKey: key, ContentType: http.DetectContentType(data),
		Size: int64(len(data)), Downloaded: true,
	}); err != nil {
		return nil, err
	}
	return data, nil
}

// --- uploads --------------------------------------------------------------------

// preparedUpload is an outgoing attachment with sniffed metadata.
type preparedUpload struct {
	core.OutgoingAttachment
	BlurHash string
}

// prepareUpload fills in content type and image dimensions.
func prepareUpload(a core.OutgoingAttachment) preparedUpload {
	p := preparedUpload{OutgoingAttachment: a}
	if p.ContentType == "" {
		if ext := filepath.Ext(p.FileName); ext != "" {
			p.ContentType = mime.TypeByExtension(strings.ToLower(ext))
		}
		if p.ContentType == "" || p.ContentType == "application/octet-stream" {
			p.ContentType = http.DetectContentType(p.Data)
		}
		if i := strings.IndexByte(p.ContentType, ';'); i > 0 && !strings.HasPrefix(p.ContentType, "text/") {
			p.ContentType = strings.TrimSpace(p.ContentType[:i])
		}
	}
	if strings.HasPrefix(p.ContentType, "image/") {
		if cfg, _, err := image.DecodeConfig(bytes.NewReader(p.Data)); err == nil {
			if p.Width == 0 || p.Height == 0 {
				p.Width, p.Height = cfg.Width, cfg.Height
			}
			if cfg.Width*cfg.Height <= 40_000_000 {
				if img, _, err := image.Decode(bytes.NewReader(p.Data)); err == nil {
					p.BlurHash = blurHash(img, 4, 3)
				}
			}
		}
	}
	return p
}

// upload encrypts and uploads data and decorates the pointer.
func (b *Backend) upload(ctx context.Context, cli *signalmeow.Client, p preparedUpload, flags uint32) (*signalpb.AttachmentPointer, error) {
	ptr, err := cli.UploadAttachment(ctx, p.Data)
	if err != nil {
		return nil, fmt.Errorf("backend: upload %q: %w", p.FileName, err)
	}
	decoratePointer(ptr, p.ContentType, p.FileName, p.Width, p.Height, p.Caption, p.BlurHash, flags)
	return ptr, nil
}

// storeOutgoingAttachment caches the plaintext of an attachment we send (so
// it never needs downloading) and returns its metadata.
func (b *Backend) storeOutgoingAttachment(ctx context.Context, owner core.MessageID, idx int, p preparedUpload, role attRole) (core.Attachment, error) {
	key, err := b.blobs.Put(p.Data)
	if err != nil {
		return core.Attachment{}, err
	}
	id := hashID("a", owner.String(), string(role), strconv.Itoa(idx), "local", key)
	rec := store.AttachmentRecord{
		ID: id, Owner: store.MessageOwner(owner), BlobKey: key, ContentType: p.ContentType,
		FileName: p.FileName, Size: int64(len(p.Data)), Downloaded: true,
	}
	if err := b.db.PutAttachment(ctx, rec); err != nil {
		return core.Attachment{}, err
	}
	return core.Attachment{
		ID: id, ContentType: p.ContentType, FileName: p.FileName, Size: int64(len(p.Data)),
		Width: p.Width, Height: p.Height, Caption: p.Caption, BlurHash: p.BlurHash,
		VoiceNote: p.VoiceNote, Downloaded: true, Progress: 1,
	}, nil
}

// rememberPointer stores the uploaded pointer of an attachment we sent, so
// forwarding can reuse it and other code paths can find its location.
func (b *Backend) rememberPointer(ctx context.Context, attID string, ptr *signalpb.AttachmentPointer, role attRole) {
	rec, err := b.db.GetAttachment(ctx, attID)
	if err != nil {
		return
	}
	raw, err := encodeRemote(attachmentRef{ID: attID, Role: role, Pointer: ptr})
	if err != nil {
		return
	}
	rec.Remote, rec.Digest = raw, ptr.GetDigest()
	_ = b.db.PutAttachment(ctx, rec)
}

// profileKey returns our own profile key (nil if unknown).
func profileKey(ctx context.Context, cli *signalmeow.Client) []byte {
	pk, err := cli.Store.RecipientStore.LoadProfileKey(ctx, cli.Store.ACI)
	if err != nil || pk == nil || *pk == (libsignalgo.ProfileKey{}) {
		return nil
	}
	return pk[:]
}
