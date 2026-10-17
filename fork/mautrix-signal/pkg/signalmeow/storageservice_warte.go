// mautrix-signal - A Matrix-signal puppeting bridge.
// Copyright (C) 2026 warte contributors
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package signalmeow

// warte: storage service write support.
//
// Reference behaviour: Signal-Desktop ts/services/storage.preload.ts
// (generateManifest, encryptManifest, uploadManifest, upload) and
// ts/services/storageRecordOps.preload.ts (to*Record), Signal-Android
// StorageSyncJob / StorageServiceApi.
//
// Wire rules implemented here:
//   - Records are immutable. A changed record gets a fresh random 16-byte
//     storage ID; the old ID goes into WriteOperation.deleteKey and the new
//     item into insertItem.
//   - Items are AES-256-GCM encrypted (12-byte random nonce prepended, no
//     associated data) with a per-item key: HMAC-SHA256(storageKey,
//     "Item_"+base64(id)) without recordIkm, or HKDF-SHA256(ikm=recordIkm,
//     salt="", info="20240801_SIGNAL_STORAGE_SERVICE_ITEM_"||id, 32 bytes)
//     when the manifest carries a recordIkm (deriveStorageItemKey).
//   - The manifest is a ManifestRecord{version=remote+1, sourceDevice,
//     identifiers, recordIkm (passed through)} encrypted the same way with
//     HMAC-SHA256(storageKey, "Manifest_"+version).
//   - PUT /v1/storage with the WriteOperation; 409 means somebody else wrote
//     first: refetch, re-apply the mutation on the new state and retry.
//   - Unknown fields are preserved: records are decoded with the Go protobuf
//     runtime, which keeps unknown fields and re-emits them on Marshal, and
//     records that the mutation did not change are not re-encoded at all.
//     The manifest record and its identifiers are cloned, so their unknown
//     fields survive as well; records of unknown types and records that
//     could not be decrypted keep their identifiers untouched.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/web"
)

var (
	// ErrStorageConflict is returned by a StorageTransport when the server
	// rejects a write because the manifest version moved on (HTTP 409).
	ErrStorageConflict = errors.New("storage service: manifest version conflict")
	// ErrStorageNoManifest means the account has no storage manifest yet.
	// Only the primary device creates one, so a linked device cannot write.
	ErrStorageNoManifest = errors.New("storage service: no manifest")
	// ErrNoStorageKey means the master key is unknown (not synced yet).
	ErrNoStorageKey = errors.New("storage service: no master key")
)

// MaxStorageWriteAttempts is how often StorageWrite tries before giving up
// on conflicts (Desktop backs off three times as well).
const MaxStorageWriteAttempts = 3

// storageConflictBackoff is the wait before each retry after a conflict.
// Desktop's conflictBackOff is 1s, 5s, 30s.
var storageConflictBackoff = []time.Duration{time.Second, 5 * time.Second, 30 * time.Second}

const storageIDLength = 16

// StorageTransport is the HTTP layer of the storage service. The default
// talks to storage.signal.org; tests inject a fake through
// Client.StorageTransport.
type StorageTransport interface {
	// GetManifest returns the current encrypted manifest. With
	// ifDifferentFrom > 0 it returns (nil, nil) when the remote version
	// equals ifDifferentFrom (HTTP 204). A missing manifest is
	// ErrStorageNoManifest.
	GetManifest(ctx context.Context, ifDifferentFrom uint64) (*signalpb.StorageManifest, error)
	// ReadItems fetches encrypted items by raw storage ID. Unknown IDs are
	// left out of the result.
	ReadItems(ctx context.Context, keys [][]byte) ([]*signalpb.StorageItem, error)
	// Write performs a write operation; a 409 is ErrStorageConflict.
	Write(ctx context.Context, op *signalpb.WriteOperation) error
}

// StorageEntry is one decrypted storage record.
type StorageEntry struct {
	ID     []byte // raw storage ID; nil for records added by a mutation
	Type   signalpb.ManifestRecord_Identifier_Type
	Record *signalpb.StorageRecord // mutable working copy

	orig *signalpb.StorageRecord // as fetched; nil for new records
}

// IsNew reports whether the entry was added by the current mutation.
func (e *StorageEntry) IsNew() bool { return e.orig == nil }

// Changed reports whether the record differs from what was fetched.
func (e *StorageEntry) Changed() bool {
	return e.orig == nil || !proto.Equal(e.orig, e.Record)
}

// StorageState is a snapshot of the storage service that a StorageWrite
// mutation edits. Records are working copies: change them in place, or add
// new ones with the Upsert helpers.
type StorageState struct {
	// Version is the remote manifest version the state was read from.
	Version uint64
	// RecordIkm is the manifest's record key material (nil for legacy
	// manifests).
	RecordIkm []byte
	// Now is the clock used for timestamps written by helpers.
	Now func() time.Time

	manifest *signalpb.ManifestRecord
	entries  []*StorageEntry
}

// Entries lists every decrypted record (fetched and added) in manifest
// order, new ones last.
func (s *StorageState) Entries() []*StorageEntry { return s.entries }

func (s *StorageState) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *StorageState) add(t signalpb.ManifestRecord_Identifier_Type, rec *signalpb.StorageRecord) *StorageEntry {
	e := &StorageEntry{Type: t, Record: rec}
	s.entries = append(s.entries, e)
	return e
}

// Account returns the account record, or nil if there is none.
func (s *StorageState) Account() *signalpb.AccountRecord {
	for _, e := range s.entries {
		if a := e.Record.GetAccount(); a != nil {
			return a
		}
	}
	return nil
}

// Contact returns the first contact record for aci, or nil.
func (s *StorageState) Contact(aci uuid.UUID) *signalpb.ContactRecord {
	if aci == uuid.Nil {
		return nil
	}
	for _, e := range s.entries {
		c := e.Record.GetContact()
		if c == nil {
			continue
		}
		if u, err := ParseStringOrBinaryUUID(c.GetAci(), c.GetAciBinary()); err == nil && u == aci {
			return c
		}
	}
	return nil
}

// UpsertContact returns the contact record for aci, adding a new record
// (with the ACI as a string, which every client reads) when there is none.
// init runs only for a new record, e.g. to fill in E164 and profile key.
func (s *StorageState) UpsertContact(aci uuid.UUID, init func(*signalpb.ContactRecord)) *signalpb.ContactRecord {
	if c := s.Contact(aci); c != nil {
		return c
	}
	c := &signalpb.ContactRecord{Aci: aci.String()}
	if init != nil {
		init(c)
	}
	s.add(signalpb.ManifestRecord_Identifier_CONTACT, &signalpb.StorageRecord{Record: &signalpb.StorageRecord_Contact{Contact: c}})
	return c
}

// GroupV2 returns the group record with masterKey, or nil.
func (s *StorageState) GroupV2(masterKey []byte) *signalpb.GroupV2Record {
	for _, e := range s.entries {
		if g := e.Record.GetGroupV2(); g != nil && bytes.Equal(g.GetMasterKey(), masterKey) {
			return g
		}
	}
	return nil
}

// UpsertGroupV2 returns the group record with masterKey, adding one if
// needed.
func (s *StorageState) UpsertGroupV2(masterKey []byte) *signalpb.GroupV2Record {
	if g := s.GroupV2(masterKey); g != nil {
		return g
	}
	g := &signalpb.GroupV2Record{MasterKey: slices.Clone(masterKey)}
	s.add(signalpb.ManifestRecord_Identifier_GROUPV2, &signalpb.StorageRecord{Record: &signalpb.StorageRecord_GroupV2{GroupV2: g}})
	return g
}

// StickerPack returns the sticker pack record for packID, or nil.
func (s *StorageState) StickerPack(packID []byte) *signalpb.StickerPackRecord {
	for _, e := range s.entries {
		if p := e.Record.GetStickerPack(); p != nil && bytes.Equal(p.GetPackId(), packID) {
			return p
		}
	}
	return nil
}

// SetStickerPackInstalled installs or uninstalls a sticker pack the way
// Desktop's toInstalledStickerPackRecord does: an installed pack has its
// key and a position (max+1 of installed packs for a new install), an
// uninstalled one only its ID and deletedAtTimestamp.
func (s *StorageState) SetStickerPackInstalled(packID, packKey []byte, installed bool) {
	p := s.StickerPack(packID)
	// warte: an installed record without a pack key is rejected by Desktop
	// (mergeInstalledStickerPackRecord throws "No installedStickerPackRecord
	// key"), so never produce one.
	if installed && len(packKey) == 0 && (p == nil || len(p.GetPackKey()) == 0) {
		return
	}
	if p == nil {
		if !installed {
			// Nothing to remove; another device never had it.
			return
		}
		p = &signalpb.StickerPackRecord{PackId: slices.Clone(packID)}
		s.add(signalpb.ManifestRecord_Identifier_STICKER_PACK, &signalpb.StorageRecord{Record: &signalpb.StorageRecord_StickerPack{StickerPack: p}})
	}
	if !installed {
		if p.DeletedAtTimestamp == 0 {
			p.DeletedAtTimestamp = uint64(s.now().UnixMilli())
		}
		p.PackKey, p.Position = nil, 0
		return
	}
	wasInstalled := p.DeletedAtTimestamp == 0 && len(p.PackKey) > 0
	p.DeletedAtTimestamp = 0
	if len(packKey) > 0 {
		p.PackKey = slices.Clone(packKey)
	}
	if !wasInstalled {
		var maxPos uint32
		found := false
		for _, e := range s.entries {
			o := e.Record.GetStickerPack()
			if o == nil || o == p || o.GetDeletedAtTimestamp() != 0 {
				continue
			}
			if !found || o.GetPosition() > maxPos {
				maxPos, found = o.GetPosition(), true
			}
		}
		p.Position = 0
		if found {
			p.Position = maxPos + 1
		}
	}
}

// SetContactBlocked updates blocked, blockedAtTimestamp and whitelisted
// (profile sharing) of a contact or group record the way blocking and
// unblocking does on Android/Desktop: blocking stops profile sharing,
// unblocking (accepting) enables it.
func SetContactBlocked(c *signalpb.ContactRecord, blocked bool, now time.Time) {
	if blocked && !c.Blocked {
		c.BlockedAtTimestamp = uint64(now.UnixMilli())
	} else if !blocked {
		c.BlockedAtTimestamp = 0
	}
	c.Blocked = blocked
	c.Whitelisted = !blocked
}

// SetGroupBlocked is SetContactBlocked for groups.
func SetGroupBlocked(g *signalpb.GroupV2Record, blocked bool, now time.Time) {
	if blocked && !g.Blocked {
		g.BlockedAtTimestamp = uint64(now.UnixMilli())
	} else if !blocked {
		g.BlockedAtTimestamp = 0
	}
	g.Blocked = blocked
	g.Whitelisted = !blocked
}

// PinnedTarget identifies a conversation in AccountRecord.pinnedConversations.
type PinnedTarget struct {
	ACI            uuid.UUID // direct chat (also Note to Self)
	E164           string    // optional, written alongside ACI
	GroupMasterKey []byte    // group chat
}

func (t PinnedTarget) matches(p *signalpb.AccountRecord_PinnedConversation) bool {
	switch id := p.GetIdentifier().(type) {
	case *signalpb.AccountRecord_PinnedConversation_Contact_:
		if t.ACI == uuid.Nil {
			return false
		}
		bin := id.Contact.GetServiceIdBinary()
		if len(bin) == 16 && uuid.UUID(bin) == t.ACI {
			return true
		}
		if sid := id.Contact.GetServiceId(); sid != "" {
			if u, err := uuid.Parse(sid); err == nil && u == t.ACI {
				return true
			}
		}
		return false
	case *signalpb.AccountRecord_PinnedConversation_GroupMasterKey:
		return len(t.GroupMasterKey) > 0 && bytes.Equal(id.GroupMasterKey, t.GroupMasterKey)
	}
	return false
}

func (t PinnedTarget) toProto() *signalpb.AccountRecord_PinnedConversation {
	if len(t.GroupMasterKey) > 0 {
		return &signalpb.AccountRecord_PinnedConversation{Identifier: &signalpb.AccountRecord_PinnedConversation_GroupMasterKey{GroupMasterKey: slices.Clone(t.GroupMasterKey)}}
	}
	return &signalpb.AccountRecord_PinnedConversation{Identifier: &signalpb.AccountRecord_PinnedConversation_Contact_{Contact: &signalpb.AccountRecord_PinnedConversation_Contact{
		ServiceId: t.ACI.String(),
		E164:      t.E164,
	}}}
}

// SetPinned pins (appends to the end of the list) or unpins a
// conversation in the account record. Entries this code does not
// understand (release notes, legacy groups) are left alone. It reports
// whether the list changed.
func SetPinned(a *signalpb.AccountRecord, t PinnedTarget, pinned bool) bool {
	idx := slices.IndexFunc(a.PinnedConversations, t.matches)
	if pinned {
		if idx >= 0 {
			return false
		}
		a.PinnedConversations = append(a.PinnedConversations, t.toProto())
		return true
	}
	if idx < 0 {
		return false
	}
	a.PinnedConversations = slices.DeleteFunc(a.PinnedConversations, t.matches)
	return true
}

// storageCache keeps the last manifest and decrypted records so a write
// does not need to refetch everything. Records are immutable per ID, so a
// cached record stays valid as long as the ID is in the manifest and the
// record key material did not change.
type storageCache struct {
	key      []byte // storage key the cache was built with
	version  uint64
	manifest *signalpb.ManifestRecord
	records  map[string]*cachedStorageRecord
}

type cachedStorageRecord struct {
	typ signalpb.ManifestRecord_Identifier_Type
	rec *signalpb.StorageRecord
}

// StorageWriteResult describes a finished StorageWrite.
type StorageWriteResult struct {
	// BaseVersion is the remote version the mutation was applied to.
	BaseVersion uint64
	// Version is the new manifest version (BaseVersion when nothing changed).
	Version uint64
	// Changed is false when the mutation produced no difference.
	Changed bool
	// Inserted and Deleted count the written storage items.
	Inserted, Deleted int
	// Attempts is the number of tries (>1 after conflicts).
	Attempts int
}

func (cli *Client) storageTransport() StorageTransport {
	if cli.StorageTransport != nil {
		return cli.StorageTransport
	}
	return httpStorageTransport{cli: cli}
}

// StorageWrite reads the current storage state, applies mutate and writes
// the changed records back. On a version conflict it refetches and runs
// mutate again on the newer state (up to MaxStorageWriteAttempts times), so
// mutate must be idempotent and must only express the intended changes
// ("set archived on chat X"), not overwrite whole records. Afterwards the
// caller should send a FetchLatest(STORAGE_MANIFEST) sync to other devices.
func (cli *Client) StorageWrite(ctx context.Context, mutate func(*StorageState) error) (*StorageWriteResult, error) {
	if len(cli.Store.MasterKey) == 0 {
		return nil, ErrNoStorageKey
	}
	cli.storageWriteLock.Lock()
	defer cli.storageWriteLock.Unlock()
	log := zerolog.Ctx(ctx).With().Str("action", "write storage").Logger()
	storageKey := deriveStorageServiceKey(cli.Store.MasterKey)
	tr := cli.storageTransport()
	for attempt := 1; ; attempt++ {
		state, err := cli.loadStorageState(ctx, tr, storageKey)
		if err != nil {
			return nil, err
		}
		if err := mutate(state); err != nil {
			return nil, err
		}
		w, err := state.buildWrite(storageKey, uint32(cli.Store.DeviceID), rand.Reader)
		if err != nil {
			return nil, err
		}
		res := &StorageWriteResult{BaseVersion: state.Version, Version: state.Version, Attempts: attempt}
		if w == nil {
			log.Debug().Uint64("version", state.Version).Msg("Storage write has no changes")
			return res, nil
		}
		err = tr.Write(ctx, w.op)
		if errors.Is(err, ErrStorageConflict) {
			cli.storageCache = nil
			if attempt >= MaxStorageWriteAttempts {
				return nil, fmt.Errorf("storage service: giving up after %d conflicts: %w", attempt, err)
			}
			wait := storageConflictBackoff[min(attempt-1, len(storageConflictBackoff)-1)]
			log.Info().Uint64("version", w.manifest.GetVersion()).Int("attempt", attempt).Msg("Storage write conflict, retrying")
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
			continue
		} else if err != nil {
			return nil, fmt.Errorf("storage service: write: %w", err)
		}
		cli.storageCache = w.cache
		res.Version = w.manifest.GetVersion()
		res.Changed = true
		res.Inserted, res.Deleted = len(w.op.InsertItem), len(w.op.DeleteKey)
		log.Info().
			Uint64("version", res.Version).
			Int("inserted", res.Inserted).
			Int("deleted", res.Deleted).
			Msg("Wrote storage service records")
		if a := w.account; a != nil {
			// warte: a copy, so later in-place edits of the store's
			// account record cannot alter the cached "as fetched" record.
			cli.Store.SetAccountRecord(a) // stores a clone under the account record lock
			if cli.Store.DeviceStore != nil {
				if err := cli.Store.DeviceStore.PutDevice(ctx, &cli.Store.DeviceData); err != nil {
					log.Warn().Err(err).Msg("Failed to save device after writing account record")
				}
			}
		}
		return res, nil
	}
}

// loadStorageState fetches the current manifest and all records it
// references (using the cache for IDs already known).
func (cli *Client) loadStorageState(ctx context.Context, tr StorageTransport, storageKey []byte) (*StorageState, error) {
	cache := cli.storageCache
	if cache != nil && !bytes.Equal(cache.key, storageKey) {
		cache = nil // master key changed
	}
	var since uint64
	if cache != nil {
		since = cache.version
	}
	enc, err := tr.GetManifest(ctx, since)
	if err != nil {
		return nil, err
	}
	var manifest *signalpb.ManifestRecord
	if enc == nil {
		if cache == nil {
			return nil, errors.New("storage service: empty manifest response")
		}
		manifest = cache.manifest
	} else {
		manifest, err = decryptStorageManifest(storageKey, enc)
		if err != nil {
			return nil, err
		}
	}
	ikm := normalizeIkm(manifest.GetRecordIkm())
	records := map[string]*cachedStorageRecord{}
	if cache != nil && bytes.Equal(normalizeIkm(cache.manifest.GetRecordIkm()), ikm) {
		for _, id := range manifest.GetIdentifiers() {
			k := base64.StdEncoding.EncodeToString(id.GetRaw())
			if r, ok := cache.records[k]; ok {
				records[k] = r
			}
		}
	}
	var missing [][]byte
	types := map[string]signalpb.ManifestRecord_Identifier_Type{}
	for _, id := range manifest.GetIdentifiers() {
		k := base64.StdEncoding.EncodeToString(id.GetRaw())
		if _, dup := types[k]; dup {
			continue
		}
		types[k] = id.GetType()
		if _, ok := records[k]; !ok {
			missing = append(missing, id.GetRaw())
		}
	}
	log := zerolog.Ctx(ctx)
	for i := 0; i < len(missing); i += MaxReadStorageRecords {
		chunk := missing[i:min(i+MaxReadStorageRecords, len(missing))]
		items, err := tr.ReadItems(ctx, chunk)
		if err != nil {
			return nil, fmt.Errorf("storage service: read records: %w", err)
		}
		for _, item := range items {
			k := base64.StdEncoding.EncodeToString(item.GetKey())
			typ, ok := types[k]
			if !ok {
				continue
			}
			rec, err := decryptStorageItem(storageKey, ikm, item)
			if err != nil {
				// Left untouched: the identifier stays in the manifest.
				log.Warn().Err(err).Stringer("item_type", typ).Msg("Failed to decrypt storage item for write")
				continue
			}
			records[k] = &cachedStorageRecord{typ: typ, rec: rec}
		}
	}
	cli.storageCache = &storageCache{key: storageKey, version: manifest.GetVersion(), manifest: manifest, records: records}
	return newStorageState(manifest, records), nil
}

func newStorageState(manifest *signalpb.ManifestRecord, records map[string]*cachedStorageRecord) *StorageState {
	st := &StorageState{
		Version:   manifest.GetVersion(),
		RecordIkm: normalizeIkm(manifest.GetRecordIkm()),
		manifest:  manifest,
	}
	seen := map[string]bool{}
	for _, id := range manifest.GetIdentifiers() {
		k := base64.StdEncoding.EncodeToString(id.GetRaw())
		r, ok := records[k]
		if !ok || seen[k] {
			continue
		}
		seen[k] = true
		st.entries = append(st.entries, &StorageEntry{
			ID:     slices.Clone(id.GetRaw()),
			Type:   id.GetType(),
			Record: proto.Clone(r.rec).(*signalpb.StorageRecord),
			orig:   r.rec,
		})
	}
	return st
}

func normalizeIkm(ikm []byte) []byte {
	if len(ikm) == 0 {
		return nil
	}
	return ikm
}

// storageWrite is a prepared write operation.
type storageWrite struct {
	op       *signalpb.WriteOperation
	manifest *signalpb.ManifestRecord // plaintext of op.Manifest
	cache    *storageCache            // the state after a successful write
	account  *signalpb.AccountRecord  // new account record, if it changed
}

// buildWrite turns the state's changes into a write operation. It returns
// nil when nothing changed.
func (s *StorageState) buildWrite(storageKey []byte, deviceID uint32, rnd io.Reader) (*storageWrite, error) {
	type change struct {
		entry *StorageEntry
		newID []byte
	}
	var changes []change
	replaced := map[string][]byte{} // old base64 ID -> new raw ID
	for _, e := range s.entries {
		if !e.Changed() {
			continue
		}
		if err := validateStorageRecord(e.Record); err != nil {
			return nil, err
		}
		id := make([]byte, storageIDLength)
		if _, err := io.ReadFull(rnd, id); err != nil {
			return nil, fmt.Errorf("storage service: generate ID: %w", err)
		}
		changes = append(changes, change{entry: e, newID: id})
		if !e.IsNew() {
			replaced[base64.StdEncoding.EncodeToString(e.ID)] = id
		}
	}
	if len(changes) == 0 {
		return nil, nil
	}
	ikm := normalizeIkm(s.RecordIkm)
	newVersion := s.Version + 1
	// Clone keeps the manifest's unknown fields and recordIkm.
	manifest := proto.Clone(s.manifest).(*signalpb.ManifestRecord)
	manifest.Version = newVersion
	manifest.SourceDevice = deviceID
	manifest.Identifiers = make([]*signalpb.ManifestRecord_Identifier, 0, len(s.manifest.GetIdentifiers())+len(changes))
	op := &signalpb.WriteOperation{}
	for _, id := range s.manifest.GetIdentifiers() {
		k := base64.StdEncoding.EncodeToString(id.GetRaw())
		if newID, ok := replaced[k]; ok {
			if newID == nil {
				continue // duplicate identifier of a replaced record
			}
			nid := proto.Clone(id).(*signalpb.ManifestRecord_Identifier)
			nid.Raw = newID
			manifest.Identifiers = append(manifest.Identifiers, nid)
			op.DeleteKey = append(op.DeleteKey, slices.Clone(id.GetRaw()))
			replaced[k] = nil
			continue
		}
		manifest.Identifiers = append(manifest.Identifiers, proto.Clone(id).(*signalpb.ManifestRecord_Identifier))
	}
	newCache := &storageCache{key: storageKey, version: newVersion, manifest: manifest, records: map[string]*cachedStorageRecord{}}
	for _, e := range s.entries {
		if !e.Changed() {
			k := base64.StdEncoding.EncodeToString(e.ID)
			newCache.records[k] = &cachedStorageRecord{typ: e.Type, rec: e.orig}
		}
	}
	var account *signalpb.AccountRecord
	for _, c := range changes {
		typ := c.entry.Type
		if c.entry.IsNew() || typ == signalpb.ManifestRecord_Identifier_UNKNOWN {
			typ = storageRecordType(c.entry.Record)
		}
		if c.entry.IsNew() {
			manifest.Identifiers = append(manifest.Identifiers, &signalpb.ManifestRecord_Identifier{Raw: c.newID, Type: typ})
		}
		item, err := encryptStorageItem(storageKey, ikm, c.newID, c.entry.Record, rnd)
		if err != nil {
			return nil, err
		}
		op.InsertItem = append(op.InsertItem, item)
		rec := proto.Clone(c.entry.Record).(*signalpb.StorageRecord)
		newCache.records[base64.StdEncoding.EncodeToString(c.newID)] = &cachedStorageRecord{typ: typ, rec: rec}
		if a := rec.GetAccount(); a != nil {
			account = a
		}
	}
	plain, err := proto.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("storage service: marshal manifest: %w", err)
	}
	value, err := encryptStorageBytes(deriveStorageManifestKey(storageKey, newVersion), plain, rnd)
	if err != nil {
		return nil, err
	}
	op.Manifest = &signalpb.StorageManifest{Version: newVersion, Value: value}
	return &storageWrite{op: op, manifest: manifest, cache: newCache, account: account}, nil
}

// validateStorageRecord rejects records the server or other clients would
// refuse (Android's StorageSyncValidations).
func validateStorageRecord(r *signalpb.StorageRecord) error {
	switch rec := r.GetRecord().(type) {
	case *signalpb.StorageRecord_Contact:
		if rec.Contact.GetAci() == "" && len(rec.Contact.GetAciBinary()) == 0 &&
			rec.Contact.GetPni() == "" && len(rec.Contact.GetPniBinary()) == 0 && rec.Contact.GetE164() == "" {
			return errors.New("storage service: contact record without identifier")
		}
	case *signalpb.StorageRecord_GroupV2:
		if len(rec.GroupV2.GetMasterKey()) != 32 {
			return errors.New("storage service: group record with invalid master key")
		}
	case *signalpb.StorageRecord_StickerPack:
		if len(rec.StickerPack.GetPackId()) == 0 {
			return errors.New("storage service: sticker pack record without ID")
		}
		// warte: Desktop drops installed packs without a key.
		if rec.StickerPack.GetDeletedAtTimestamp() == 0 && len(rec.StickerPack.GetPackKey()) == 0 {
			return errors.New("storage service: installed sticker pack record without key")
		}
	case nil:
		return errors.New("storage service: empty record")
	}
	return nil
}

// storageRecordType maps a record to its manifest identifier type.
func storageRecordType(r *signalpb.StorageRecord) signalpb.ManifestRecord_Identifier_Type {
	switch r.GetRecord().(type) {
	case *signalpb.StorageRecord_Contact:
		return signalpb.ManifestRecord_Identifier_CONTACT
	case *signalpb.StorageRecord_GroupV1:
		return signalpb.ManifestRecord_Identifier_GROUPV1
	case *signalpb.StorageRecord_GroupV2:
		return signalpb.ManifestRecord_Identifier_GROUPV2
	case *signalpb.StorageRecord_Account:
		return signalpb.ManifestRecord_Identifier_ACCOUNT
	case *signalpb.StorageRecord_StoryDistributionList:
		return signalpb.ManifestRecord_Identifier_STORY_DISTRIBUTION_LIST
	case *signalpb.StorageRecord_StickerPack:
		return signalpb.ManifestRecord_Identifier_STICKER_PACK
	case *signalpb.StorageRecord_CallLink:
		return signalpb.ManifestRecord_Identifier_CALL_LINK
	case *signalpb.StorageRecord_ChatFolder:
		return signalpb.ManifestRecord_Identifier_CHAT_FOLDER
	case *signalpb.StorageRecord_NotificationProfile:
		return signalpb.ManifestRecord_Identifier_NOTIFICATION_PROFILE
	case *signalpb.StorageRecord_FavoriteSticker:
		return signalpb.ManifestRecord_Identifier_FAVORITE_STICKER
	}
	return signalpb.ManifestRecord_Identifier_UNKNOWN
}

// encryptStorageBytes is Desktop's encryptProfile: a random 12-byte nonce
// followed by the AES-256-GCM ciphertext and tag. decryptBytes reverses it.
func encryptStorageBytes(key, plaintext []byte, rnd io.Reader) ([]byte, error) {
	nonce := make([]byte, NONCE_LENGTH)
	if _, err := io.ReadFull(rnd, nonce); err != nil {
		return nil, fmt.Errorf("storage service: nonce: %w", err)
	}
	ct, err := AesgcmEncrypt(key, nonce, plaintext)
	if err != nil {
		return nil, fmt.Errorf("storage service: encrypt: %w", err)
	}
	return append(nonce, ct...), nil
}

// encryptStorageItem encrypts a record under its storage ID.
func encryptStorageItem(storageKey, recordIkm, rawID []byte, rec *signalpb.StorageRecord, rnd io.Reader) (*signalpb.StorageItem, error) {
	plain, err := proto.Marshal(rec)
	if err != nil {
		return nil, fmt.Errorf("storage service: marshal record: %w", err)
	}
	key := deriveStorageItemKey(storageKey, normalizeIkm(recordIkm), rawID, base64.StdEncoding.EncodeToString(rawID))
	value, err := encryptStorageBytes(key, plain, rnd)
	if err != nil {
		return nil, err
	}
	return &signalpb.StorageItem{Key: slices.Clone(rawID), Value: value}, nil
}

// decryptStorageItem decrypts one item (same derivation as the read path).
func decryptStorageItem(storageKey, recordIkm []byte, item *signalpb.StorageItem) (*signalpb.StorageRecord, error) {
	key := deriveStorageItemKey(storageKey, normalizeIkm(recordIkm), item.GetKey(), base64.StdEncoding.EncodeToString(item.GetKey()))
	plain, err := decryptBytes(key, item.GetValue())
	if err != nil {
		return nil, err
	}
	var rec signalpb.StorageRecord
	if err := proto.Unmarshal(plain, &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

// decryptStorageManifest decrypts a manifest with the key of its version.
func decryptStorageManifest(storageKey []byte, enc *signalpb.StorageManifest) (*signalpb.ManifestRecord, error) {
	plain, err := decryptBytes(deriveStorageManifestKey(storageKey, enc.GetVersion()), enc.GetValue())
	if err != nil {
		return nil, fmt.Errorf("storage service: decrypt manifest: %w", err)
	}
	var m signalpb.ManifestRecord
	if err := proto.Unmarshal(plain, &m); err != nil {
		return nil, fmt.Errorf("storage service: unmarshal manifest: %w", err)
	}
	if m.GetVersion() != enc.GetVersion() {
		return nil, fmt.Errorf("storage service: manifest version %d does not match envelope %d", m.GetVersion(), enc.GetVersion())
	}
	return &m, nil
}

// httpStorageTransport talks to the real storage service.
type httpStorageTransport struct{ cli *Client }

func (t httpStorageTransport) opts(ctx context.Context, body []byte) (*web.HTTPReqOpt, error) {
	creds, err := t.cli.getStorageCredentials(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch storage credentials: %w", err)
	}
	return &web.HTTPReqOpt{Username: &creds.Username, Password: &creds.Password, Body: body, ContentType: web.ContentTypeProtobuf}, nil
}

func (t httpStorageTransport) GetManifest(ctx context.Context, ifDifferentFrom uint64) (*signalpb.StorageManifest, error) {
	opt, err := t.opts(ctx, nil)
	if err != nil {
		return nil, err
	}
	path := "/v1/storage/manifest"
	if ifDifferentFrom > 0 {
		path += fmt.Sprintf("/version/%d", ifDifferentFrom)
	}
	resp, err := web.SendHTTPRequest(ctx, web.StorageHostname, http.MethodGet, path, opt)
	defer web.CloseBody(resp)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch storage manifest: %w", err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNoContent:
		return nil, nil
	case http.StatusNotFound:
		return nil, ErrStorageNoManifest
	default:
		return nil, fmt.Errorf("unexpected status code %d fetching storage manifest", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var m signalpb.StorageManifest
	if err := proto.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("failed to unmarshal storage manifest: %w", err)
	}
	return &m, nil
}

func (t httpStorageTransport) ReadItems(ctx context.Context, keys [][]byte) ([]*signalpb.StorageItem, error) {
	return t.cli.fetchStorageItemsChunk(ctx, keys)
}

func (t httpStorageTransport) Write(ctx context.Context, op *signalpb.WriteOperation) error {
	body, err := proto.Marshal(op)
	if err != nil {
		return err
	}
	opt, err := t.opts(ctx, body)
	if err != nil {
		return err
	}
	// Android asks the server not to send the remote manifest back on
	// conflict since it refetches anyway; so do we.
	resp, err := web.SendHTTPRequest(ctx, web.StorageHostname, http.MethodPut, "/v1/storage?sendRemoteManifestOnConflict=false", opt)
	defer web.CloseBody(resp)
	if err != nil {
		return fmt.Errorf("failed to write storage records: %w", err)
	}
	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent:
		return nil
	case http.StatusConflict:
		return ErrStorageConflict
	}
	return fmt.Errorf("unexpected status code %d writing storage records", resp.StatusCode)
}
