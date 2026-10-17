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

// warte: an in-memory storage service for tests of code built on
// StorageWrite. It stores encrypted items exactly like the server and
// enforces the server's write rules (version must be remote+1, deleted keys
// must exist, inserted keys must not).

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"

	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"
)

// MemoryStorage implements StorageTransport in memory.
type MemoryStorage struct {
	mu         sync.Mutex
	storageKey []byte
	manifest   *signalpb.StorageManifest
	items      map[string][]byte

	// Writes counts accepted writes, Conflicts rejected ones.
	Writes, Conflicts int
	// BeforeWrite, if set, runs once before the next write is checked
	// (without the lock held), e.g. to simulate another device writing.
	BeforeWrite func(m *MemoryStorage)
}

var _ StorageTransport = (*MemoryStorage)(nil)

// NewMemoryStorage creates a storage service for masterKey holding records
// at version (recordIkm may be nil for the legacy key derivation).
func NewMemoryStorage(masterKey []byte, version uint64, recordIkm []byte, records ...*signalpb.StorageRecord) (*MemoryStorage, error) {
	m := &MemoryStorage{storageKey: deriveStorageServiceKey(masterKey), items: map[string][]byte{}}
	man := &signalpb.ManifestRecord{Version: version, SourceDevice: 1, RecordIkm: recordIkm}
	for _, r := range records {
		id, err := m.putLocked(normalizeIkm(recordIkm), r)
		if err != nil {
			return nil, err
		}
		man.Identifiers = append(man.Identifiers, &signalpb.ManifestRecord_Identifier{Raw: id, Type: storageRecordType(r)})
	}
	if err := m.setManifestLocked(man); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *MemoryStorage) putLocked(ikm []byte, r *signalpb.StorageRecord) ([]byte, error) {
	id := make([]byte, storageIDLength)
	if _, err := rand.Read(id); err != nil {
		return nil, err
	}
	item, err := encryptStorageItem(m.storageKey, ikm, id, r, rand.Reader)
	if err != nil {
		return nil, err
	}
	m.items[base64.StdEncoding.EncodeToString(id)] = item.GetValue()
	return id, nil
}

func (m *MemoryStorage) setManifestLocked(man *signalpb.ManifestRecord) error {
	plain, err := proto.Marshal(man)
	if err != nil {
		return err
	}
	val, err := encryptStorageBytes(deriveStorageManifestKey(m.storageKey, man.GetVersion()), plain, rand.Reader)
	if err != nil {
		return err
	}
	m.manifest = &signalpb.StorageManifest{Version: man.GetVersion(), Value: val}
	return nil
}

// Snapshot decrypts the current manifest and its records (in manifest
// order).
func (m *MemoryStorage) Snapshot() (*signalpb.ManifestRecord, []*signalpb.StorageRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshotLocked()
}

func (m *MemoryStorage) snapshotLocked() (*signalpb.ManifestRecord, []*signalpb.StorageRecord, error) {
	man, err := decryptStorageManifest(m.storageKey, m.manifest)
	if err != nil {
		return nil, nil, err
	}
	var out []*signalpb.StorageRecord
	for _, id := range man.GetIdentifiers() {
		val, ok := m.items[base64.StdEncoding.EncodeToString(id.GetRaw())]
		if !ok {
			return nil, nil, fmt.Errorf("manifest references missing item")
		}
		rec, err := decryptStorageItem(m.storageKey, man.GetRecordIkm(), &signalpb.StorageItem{Key: id.GetRaw(), Value: val})
		if err != nil {
			return nil, nil, err
		}
		out = append(out, rec)
	}
	if len(out) != len(m.items) {
		return nil, nil, fmt.Errorf("server holds %d items, manifest lists %d", len(m.items), len(out))
	}
	return man, out, nil
}

// Update simulates another device: edit changes the decrypted records in
// place (or returns extra records to add); changed records get new IDs and
// the version is bumped.
func (m *MemoryStorage) Update(edit func(recs []*signalpb.StorageRecord) []*signalpb.StorageRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	man, recs, err := m.snapshotLocked()
	if err != nil {
		return err
	}
	orig := make([]*signalpb.StorageRecord, len(recs))
	for i, r := range recs {
		orig[i] = proto.Clone(r).(*signalpb.StorageRecord)
	}
	added := edit(recs)
	ikm := normalizeIkm(man.GetRecordIkm())
	for i, r := range recs {
		if proto.Equal(r, orig[i]) {
			continue
		}
		delete(m.items, base64.StdEncoding.EncodeToString(man.Identifiers[i].GetRaw()))
		id, err := m.putLocked(ikm, r)
		if err != nil {
			return err
		}
		man.Identifiers[i].Raw = id
	}
	for _, r := range added {
		id, err := m.putLocked(ikm, r)
		if err != nil {
			return err
		}
		man.Identifiers = append(man.Identifiers, &signalpb.ManifestRecord_Identifier{Raw: id, Type: storageRecordType(r)})
	}
	man.Version++
	return m.setManifestLocked(man)
}

func (m *MemoryStorage) GetManifest(ctx context.Context, ifDifferentFrom uint64) (*signalpb.StorageManifest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.manifest == nil {
		return nil, ErrStorageNoManifest
	}
	if ifDifferentFrom > 0 && m.manifest.GetVersion() == ifDifferentFrom {
		return nil, nil
	}
	return proto.Clone(m.manifest).(*signalpb.StorageManifest), nil
}

func (m *MemoryStorage) ReadItems(ctx context.Context, keys [][]byte) ([]*signalpb.StorageItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*signalpb.StorageItem
	for _, k := range keys {
		if v, ok := m.items[base64.StdEncoding.EncodeToString(k)]; ok {
			out = append(out, &signalpb.StorageItem{Key: k, Value: v})
		}
	}
	return out, nil
}

func (m *MemoryStorage) Write(ctx context.Context, op *signalpb.WriteOperation) error {
	m.mu.Lock()
	hook := m.BeforeWrite
	m.BeforeWrite = nil
	m.mu.Unlock()
	if hook != nil {
		hook(m)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.manifest == nil {
		return ErrStorageNoManifest
	}
	if op.GetManifest().GetVersion() != m.manifest.GetVersion()+1 {
		m.Conflicts++
		return ErrStorageConflict
	}
	for _, k := range op.GetDeleteKey() {
		if _, ok := m.items[base64.StdEncoding.EncodeToString(k)]; !ok {
			return errors.New("memory storage: delete of unknown key")
		}
	}
	for _, it := range op.GetInsertItem() {
		if _, ok := m.items[base64.StdEncoding.EncodeToString(it.GetKey())]; ok || len(it.GetKey()) != storageIDLength {
			return errors.New("memory storage: bad insert key")
		}
	}
	for _, k := range op.GetDeleteKey() {
		delete(m.items, base64.StdEncoding.EncodeToString(k))
	}
	for _, it := range op.GetInsertItem() {
		m.items[base64.StdEncoding.EncodeToString(it.GetKey())] = it.GetValue()
	}
	m.manifest = proto.Clone(op.GetManifest()).(*signalpb.StorageManifest)
	m.Writes++
	return nil
}
