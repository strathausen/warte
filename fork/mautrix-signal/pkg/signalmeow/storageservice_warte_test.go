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

// warte: tests for storage service writes (storageservice_warte.go).

import (
	"bytes"
	"context"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/store"
)

func init() {
	storageConflictBackoff = []time.Duration{0}
}

var testMasterKey = bytes.Repeat([]byte{0x42}, 32)

// fakeStorage is an in-memory storage service with the server's write
// rules: the new manifest must be exactly version+1, deleted keys must
// exist, inserted keys must not.
type fakeStorage struct {
	mu         sync.Mutex
	storageKey []byte
	manifest   *signalpb.StorageManifest
	items      map[string][]byte

	writes    []*signalpb.WriteOperation
	readKeys  int
	conflicts int
	// beforeWrite runs (unlocked) before a write is checked, simulating
	// another device writing first.
	beforeWrite func(f *fakeStorage)
}

func newFakeStorage(t *testing.T, ikm []byte, version uint64, recs ...*signalpb.StorageRecord) (*fakeStorage, [][]byte) {
	t.Helper()
	f := &fakeStorage{storageKey: deriveStorageServiceKey(testMasterKey), items: map[string][]byte{}}
	m := &signalpb.ManifestRecord{Version: version, SourceDevice: 1, RecordIkm: ikm}
	var ids [][]byte
	for _, r := range recs {
		id := randomID(t)
		ids = append(ids, id)
		f.putItem(t, ikm, id, mustMarshal(t, r))
		m.Identifiers = append(m.Identifiers, &signalpb.ManifestRecord_Identifier{Raw: id, Type: storageRecordType(r)})
	}
	f.setManifest(t, m)
	return f, ids
}

func randomID(t *testing.T) []byte {
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		t.Fatal(err)
	}
	return id
}

func mustMarshal(t *testing.T, m proto.Message) []byte {
	t.Helper()
	b, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// putItem stores a plaintext record encrypted the way other clients do,
// using an independent implementation of the key derivation.
func (f *fakeStorage) putItem(t *testing.T, ikm, id, plain []byte) {
	t.Helper()
	val, err := encryptStorageBytes(refItemKey(t, f.storageKey, ikm, id), plain, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f.items[base64.StdEncoding.EncodeToString(id)] = val
}

func (f *fakeStorage) setManifest(t *testing.T, m *signalpb.ManifestRecord) {
	t.Helper()
	val, err := encryptStorageBytes(refManifestKey(f.storageKey, m.GetVersion()), mustMarshal(t, m), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f.manifest = &signalpb.StorageManifest{Version: m.GetVersion(), Value: val}
}

// decryptManifest decrypts the server's manifest with the reference keys.
func (f *fakeStorage) decryptManifest(t *testing.T) *signalpb.ManifestRecord {
	t.Helper()
	plain, err := decryptBytes(refManifestKey(f.storageKey, f.manifest.GetVersion()), f.manifest.GetValue())
	if err != nil {
		t.Fatal(err)
	}
	var m signalpb.ManifestRecord
	if err := proto.Unmarshal(plain, &m); err != nil {
		t.Fatal(err)
	}
	return &m
}

// records decrypts every record in the current manifest, keyed by type.
func (f *fakeStorage) records(t *testing.T) (map[string][]byte, *signalpb.ManifestRecord) {
	t.Helper()
	m := f.decryptManifest(t)
	out := map[string][]byte{}
	for _, id := range m.GetIdentifiers() {
		k := base64.StdEncoding.EncodeToString(id.GetRaw())
		val, ok := f.items[k]
		if !ok {
			t.Fatalf("manifest references missing item %s", k)
		}
		plain, err := decryptBytes(refItemKey(t, f.storageKey, m.GetRecordIkm(), id.GetRaw()), val)
		if err != nil {
			t.Fatalf("decrypt %s: %v", k, err)
		}
		out[k] = plain
	}
	if len(out) != len(f.items) {
		t.Fatalf("server has %d items, manifest lists %d", len(f.items), len(out))
	}
	return out, m
}

func (f *fakeStorage) GetManifest(ctx context.Context, ifDifferentFrom uint64) (*signalpb.StorageManifest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.manifest == nil {
		return nil, ErrStorageNoManifest
	}
	if ifDifferentFrom > 0 && f.manifest.GetVersion() == ifDifferentFrom {
		return nil, nil
	}
	return proto.Clone(f.manifest).(*signalpb.StorageManifest), nil
}

func (f *fakeStorage) ReadItems(ctx context.Context, keys [][]byte) ([]*signalpb.StorageItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.readKeys += len(keys)
	var out []*signalpb.StorageItem
	for _, k := range keys {
		if v, ok := f.items[base64.StdEncoding.EncodeToString(k)]; ok {
			out = append(out, &signalpb.StorageItem{Key: k, Value: v})
		}
	}
	return out, nil
}

func (f *fakeStorage) Write(ctx context.Context, op *signalpb.WriteOperation) error {
	if hook := f.beforeWrite; hook != nil {
		f.beforeWrite = nil
		hook(f)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, proto.Clone(op).(*signalpb.WriteOperation))
	if op.GetManifest().GetVersion() != f.manifest.GetVersion()+1 {
		f.conflicts++
		return ErrStorageConflict
	}
	for _, k := range op.GetDeleteKey() {
		if _, ok := f.items[base64.StdEncoding.EncodeToString(k)]; !ok {
			return fmt.Errorf("delete of unknown key")
		}
	}
	for _, it := range op.GetInsertItem() {
		if _, ok := f.items[base64.StdEncoding.EncodeToString(it.GetKey())]; ok {
			return fmt.Errorf("insert of existing key")
		}
		if len(it.GetKey()) != 16 {
			return fmt.Errorf("bad key length %d", len(it.GetKey()))
		}
	}
	for _, k := range op.GetDeleteKey() {
		delete(f.items, base64.StdEncoding.EncodeToString(k))
	}
	for _, it := range op.GetInsertItem() {
		f.items[base64.StdEncoding.EncodeToString(it.GetKey())] = it.GetValue()
	}
	f.manifest = op.GetManifest()
	return nil
}

// refItemKey is an independent implementation of the item key derivation
// (Desktop's deriveStorageItemKey) using the standard library.
func refItemKey(t *testing.T, storageKey, ikm, id []byte) []byte {
	t.Helper()
	if len(ikm) == 0 {
		h := hmac.New(sha256.New, storageKey)
		h.Write([]byte("Item_" + base64.StdEncoding.EncodeToString(id)))
		return h.Sum(nil)
	}
	k, err := hkdf.Key(sha256.New, ikm, nil, "20240801_SIGNAL_STORAGE_SERVICE_ITEM_"+string(id), 32)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func refManifestKey(storageKey []byte, version uint64) []byte {
	h := hmac.New(sha256.New, storageKey)
	fmt.Fprintf(h, "Manifest_%d", version)
	return h.Sum(nil)
}

func testStorageClient(f *fakeStorage) *Client {
	return &Client{
		Store:            &store.Device{DeviceData: store.DeviceData{MasterKey: testMasterKey, DeviceID: 3}},
		Log:              zerolog.Nop(),
		StorageTransport: f,
	}
}

func contactRec(aci uuid.UUID) *signalpb.StorageRecord {
	return &signalpb.StorageRecord{Record: &signalpb.StorageRecord_Contact{Contact: &signalpb.ContactRecord{Aci: aci.String(), GivenName: "Al"}}}
}

func accountRec() *signalpb.StorageRecord {
	return &signalpb.StorageRecord{Record: &signalpb.StorageRecord_Account{Account: &signalpb.AccountRecord{ReadReceipts: true, GivenName: "Me"}}}
}

func unmarshalRecord(t *testing.T, b []byte) *signalpb.StorageRecord {
	t.Helper()
	var r signalpb.StorageRecord
	if err := proto.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return &r
}

func findContact(t *testing.T, recs map[string][]byte, aci uuid.UUID) (string, *signalpb.ContactRecord) {
	t.Helper()
	for k, b := range recs {
		if c := unmarshalRecord(t, b).GetContact(); c != nil && c.GetAci() == aci.String() {
			return k, c
		}
	}
	return "", nil
}

func TestStorageKeyDerivationMatchesReference(t *testing.T) {
	sk := deriveStorageServiceKey(testMasterKey)
	h := hmac.New(sha256.New, testMasterKey)
	h.Write([]byte("Storage Service Encryption"))
	if !bytes.Equal(sk, h.Sum(nil)) {
		t.Fatal("storage key mismatch")
	}
	if !bytes.Equal(deriveStorageManifestKey(sk, 77), refManifestKey(sk, 77)) {
		t.Fatal("manifest key mismatch")
	}
	id := bytes.Repeat([]byte{1, 2}, 8)
	ikm := bytes.Repeat([]byte{9}, 32)
	for _, k := range [][]byte{nil, ikm} {
		got := deriveStorageItemKey(sk, k, id, base64.StdEncoding.EncodeToString(id))
		if !bytes.Equal(got, refItemKey(t, sk, k, id)) {
			t.Fatalf("item key mismatch (ikm=%v)", k != nil)
		}
	}
}

func TestStorageItemRoundTrip(t *testing.T) {
	sk := deriveStorageServiceKey(testMasterKey)
	rec := contactRec(testACI1)
	for _, ikm := range [][]byte{nil, bytes.Repeat([]byte{5}, 32)} {
		id := randomID(t)
		item, err := encryptStorageItem(sk, ikm, id, rec, rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(item.GetKey(), id) || len(item.GetValue()) < NONCE_LENGTH+16 {
			t.Fatalf("bad item %x", item.GetKey())
		}
		// The read path (fetchStorageRecords) decrypts with
		// deriveStorageItemKey + decryptBytes.
		plain, err := decryptBytes(deriveStorageItemKey(sk, ikm, id, base64.StdEncoding.EncodeToString(id)), item.GetValue())
		if err != nil {
			t.Fatal(err)
		}
		if !proto.Equal(unmarshalRecord(t, plain), rec) {
			t.Fatal("round trip changed the record")
		}
		// The wrong derivation must not decrypt.
		other := bytes.Repeat([]byte{6}, 32)
		if ikm != nil {
			other = nil
		}
		if _, err := decryptStorageItem(sk, other, item); err == nil {
			t.Fatal("decrypted with the wrong key derivation")
		}
	}
}

func TestStorageWriteBuildsManifest(t *testing.T) {
	for _, ikm := range [][]byte{nil, bytes.Repeat([]byte{8}, 32)} {
		t.Run(fmt.Sprintf("ikm=%v", ikm != nil), func(t *testing.T) {
			f, ids := newFakeStorage(t, ikm, 10, accountRec(), contactRec(testACI1), contactRec(testACI2))
			cli := testStorageClient(f)
			res, err := cli.StorageWrite(context.Background(), func(s *StorageState) error {
				s.Contact(testACI1).Archived = true
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if !res.Changed || res.BaseVersion != 10 || res.Version != 11 || res.Inserted != 1 || res.Deleted != 1 {
				t.Fatalf("result %+v", res)
			}
			op := f.writes[0]
			if len(op.GetDeleteKey()) != 1 || !bytes.Equal(op.GetDeleteKey()[0], ids[1]) {
				t.Fatal("old contact ID not deleted")
			}
			if op.GetClearAll() {
				t.Fatal("clearAll set")
			}
			recs, m := f.records(t)
			if m.GetVersion() != 11 || m.GetSourceDevice() != 3 || !bytes.Equal(m.GetRecordIkm(), ikm) {
				t.Fatalf("manifest %d/%d ikm=%x", m.GetVersion(), m.GetSourceDevice(), m.GetRecordIkm())
			}
			if len(m.GetIdentifiers()) != 3 {
				t.Fatalf("%d identifiers", len(m.GetIdentifiers()))
			}
			// Unchanged records keep their IDs and position.
			if !bytes.Equal(m.Identifiers[0].GetRaw(), ids[0]) || !bytes.Equal(m.Identifiers[2].GetRaw(), ids[2]) {
				t.Fatal("unchanged record IDs moved")
			}
			if bytes.Equal(m.Identifiers[1].GetRaw(), ids[1]) || m.Identifiers[1].GetType() != signalpb.ManifestRecord_Identifier_CONTACT {
				t.Fatal("changed record kept its ID or lost its type")
			}
			_, c := findContact(t, recs, testACI1)
			if c == nil || !c.GetArchived() || c.GetGivenName() != "Al" {
				t.Fatalf("contact %+v", c)
			}
			if _, c2 := findContact(t, recs, testACI2); c2.GetArchived() {
				t.Fatal("other contact changed")
			}
		})
	}
}

func TestStorageWriteNoChange(t *testing.T) {
	f, _ := newFakeStorage(t, nil, 4, accountRec(), contactRec(testACI1))
	cli := testStorageClient(f)
	res, err := cli.StorageWrite(context.Background(), func(s *StorageState) error {
		s.Contact(testACI1).GivenName = "Al" // same value
		s.Account().ReadReceipts = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed || res.Version != 4 || len(f.writes) != 0 {
		t.Fatalf("unexpected write: %+v", res)
	}
}

// appendUnknown adds an unknown varint field to a serialized message.
func appendUnknown(b []byte, num protowire.Number, v uint64) []byte {
	b = protowire.AppendTag(b, num, protowire.VarintType)
	return protowire.AppendVarint(b, v)
}

func hasUnknown(b []byte, num protowire.Number, v uint64) bool {
	for len(b) > 0 {
		n, typ, l := protowire.ConsumeTag(b)
		if l < 0 {
			return false
		}
		b = b[l:]
		if n == num && typ == protowire.VarintType {
			got, l := protowire.ConsumeVarint(b)
			if l >= 0 && got == v {
				return true
			}
		}
		l = protowire.ConsumeFieldValue(n, typ, b)
		if l < 0 {
			return false
		}
		b = b[l:]
	}
	return false
}

func TestStorageWritePreservesUnknownFields(t *testing.T) {
	f, _ := newFakeStorage(t, bytes.Repeat([]byte{3}, 32), 7, accountRec())
	ikm := bytes.Repeat([]byte{3}, 32)
	// A contact with an unknown field 99 inside ContactRecord and an
	// unknown field 98 on the StorageRecord.
	contact := appendUnknown(mustMarshal(t, &signalpb.ContactRecord{Aci: testACI1.String()}), 99, 12345)
	rec := protowire.AppendTag(nil, 1, protowire.BytesType)
	rec = protowire.AppendBytes(rec, contact)
	rec = appendUnknown(rec, 98, 7)
	// A record of a type this code does not know (StorageRecord field 12).
	futureType := protowire.AppendTag(nil, 12, protowire.BytesType)
	futureType = protowire.AppendBytes(futureType, []byte{0x08, 0x01})
	cid, fid := randomID(t), randomID(t)
	f.putItem(t, ikm, cid, rec)
	f.putItem(t, ikm, fid, futureType)
	m := f.decryptManifest(t)
	m.Identifiers = append(m.Identifiers,
		&signalpb.ManifestRecord_Identifier{Raw: cid, Type: signalpb.ManifestRecord_Identifier_CONTACT},
		&signalpb.ManifestRecord_Identifier{Raw: fid, Type: signalpb.ManifestRecord_Identifier_Type(12)},
	)
	// Unknown field on the manifest itself.
	mb := appendUnknown(mustMarshal(t, m), 9, 4242)
	val, err := encryptStorageBytes(refManifestKey(f.storageKey, 7), mb, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f.manifest = &signalpb.StorageManifest{Version: 7, Value: val}

	cli := testStorageClient(f)
	if _, err := cli.StorageWrite(context.Background(), func(s *StorageState) error {
		s.Contact(testACI1).MarkedUnread = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	recs, nm := f.records(t)
	k, c := findContact(t, recs, testACI1)
	if c == nil || !c.GetMarkedUnread() {
		t.Fatal("contact not updated")
	}
	if k == base64.StdEncoding.EncodeToString(cid) {
		t.Fatal("changed contact kept its ID")
	}
	if !hasUnknown(recs[k], 98, 7) {
		t.Error("StorageRecord unknown field lost")
	}
	sr := unmarshalRecord(t, recs[k])
	if !hasUnknown(mustMarshal(t, sr.GetContact()), 99, 12345) {
		t.Error("ContactRecord unknown field lost")
	}
	if got := recs[base64.StdEncoding.EncodeToString(fid)]; !bytes.Equal(got, futureType) {
		t.Error("record of unknown type was rewritten")
	}
	if !hasUnknown(mustMarshal(t, nm), 9, 4242) {
		t.Error("manifest unknown field lost")
	}
	if nm.Identifiers[2].GetType() != 12 {
		t.Error("unknown identifier type changed")
	}
}

func TestStorageWriteConflictMerges(t *testing.T) {
	f, ids := newFakeStorage(t, nil, 20, accountRec(), contactRec(testACI1), contactRec(testACI2))
	// Another device writes version 21 first: it marks contact 2 unread
	// and changes contact 1's name.
	f.beforeWrite = func(f *fakeStorage) {
		f.mu.Lock()
		defer f.mu.Unlock()
		m := f.decryptManifest(t)
		for i, old := range ids[1:] {
			r := contactRec([]uuid.UUID{testACI1, testACI2}[i])
			if i == 0 {
				r.GetContact().GivenName = "Alice"
			} else {
				r.GetContact().MarkedUnread = true
			}
			nid := randomID(t)
			delete(f.items, base64.StdEncoding.EncodeToString(old))
			f.putItem(t, nil, nid, mustMarshal(t, r))
			m.Identifiers[i+1].Raw = nid
		}
		m.Version = 21
		f.setManifest(t, m)
	}
	cli := testStorageClient(f)
	calls := 0
	res, err := cli.StorageWrite(context.Background(), func(s *StorageState) error {
		calls++
		s.Contact(testACI1).Archived = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || res.Attempts != 2 || f.conflicts != 1 || res.BaseVersion != 21 || res.Version != 22 {
		t.Fatalf("calls=%d res=%+v conflicts=%d", calls, res, f.conflicts)
	}
	recs, _ := f.records(t)
	_, c1 := findContact(t, recs, testACI1)
	_, c2 := findContact(t, recs, testACI2)
	if !c1.GetArchived() || c1.GetGivenName() != "Alice" {
		t.Errorf("contact 1 lost a change: %+v", c1)
	}
	if !c2.GetMarkedUnread() {
		t.Error("contact 2 lost the other device's change")
	}
}

type alwaysConflict struct{ *fakeStorage }

func (a alwaysConflict) Write(ctx context.Context, op *signalpb.WriteOperation) error {
	a.writes = append(a.writes, op)
	return ErrStorageConflict
}

func TestStorageWriteGivesUp(t *testing.T) {
	f, _ := newFakeStorage(t, nil, 1, accountRec())
	ac := alwaysConflict{f}
	cli := testStorageClient(f)
	cli.StorageTransport = ac
	_, err := cli.StorageWrite(context.Background(), func(s *StorageState) error {
		s.Account().TypingIndicators = true
		return nil
	})
	if !errors.Is(err, ErrStorageConflict) || len(f.writes) != MaxStorageWriteAttempts {
		t.Fatalf("err=%v writes=%d", err, len(f.writes))
	}
}

func TestStorageWriteCachesRecords(t *testing.T) {
	f, _ := newFakeStorage(t, nil, 1, accountRec(), contactRec(testACI1), contactRec(testACI2))
	cli := testStorageClient(f)
	ctx := context.Background()
	if _, err := cli.StorageWrite(ctx, func(s *StorageState) error { s.Contact(testACI1).Archived = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if f.readKeys != 3 {
		t.Fatalf("first write read %d keys", f.readKeys)
	}
	if _, err := cli.StorageWrite(ctx, func(s *StorageState) error { s.Contact(testACI2).Archived = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if f.readKeys != 3 {
		t.Fatalf("second write re-read records (%d keys)", f.readKeys)
	}
	recs, m := f.records(t)
	if m.GetVersion() != 3 {
		t.Fatalf("version %d", m.GetVersion())
	}
	for _, aci := range []uuid.UUID{testACI1, testACI2} {
		if _, c := findContact(t, recs, aci); !c.GetArchived() {
			t.Errorf("%s not archived", aci)
		}
	}
	if cli.Store.HasAccountRecord() {
		t.Error("account record touched without change")
	}
}

func TestStorageWriteNewRecordsAndAccount(t *testing.T) {
	f, _ := newFakeStorage(t, nil, 5, accountRec(), &signalpb.StorageRecord{Record: &signalpb.StorageRecord_StickerPack{StickerPack: &signalpb.StickerPackRecord{PackId: []byte{1}, PackKey: []byte{2}, Position: 4}}})
	cli := testStorageClient(f)
	mk := bytes.Repeat([]byte{7}, 32)
	now := time.UnixMilli(1_700_000_000_000)
	res, err := cli.StorageWrite(context.Background(), func(s *StorageState) error {
		s.Now = func() time.Time { return now }
		c := s.UpsertContact(testACI2, func(c *signalpb.ContactRecord) { c.E164 = "+15550001" })
		SetContactBlocked(c, true, s.Now())
		g := s.UpsertGroupV2(mk)
		g.MutedUntilTimestamp = 99
		s.SetStickerPackInstalled([]byte{9}, []byte{8}, true)
		a := s.Account()
		a.TypingIndicators = true
		SetPinned(a, PinnedTarget{GroupMasterKey: mk}, true)
		SetPinned(a, PinnedTarget{ACI: testACI2, E164: "+15550001"}, true)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Inserted != 4 || res.Deleted != 1 {
		t.Fatalf("res %+v", res)
	}
	recs, m := f.records(t)
	if len(m.GetIdentifiers()) != 5 {
		t.Fatalf("%d identifiers", len(m.GetIdentifiers()))
	}
	types := map[signalpb.ManifestRecord_Identifier_Type]int{}
	for _, id := range m.GetIdentifiers() {
		types[id.GetType()]++
		if storageRecordType(unmarshalRecord(t, recs[base64.StdEncoding.EncodeToString(id.GetRaw())])) != id.GetType() {
			t.Error("identifier type does not match record")
		}
	}
	if types[signalpb.ManifestRecord_Identifier_STICKER_PACK] != 2 || types[signalpb.ManifestRecord_Identifier_GROUPV2] != 1 {
		t.Errorf("types %v", types)
	}
	_, c := findContact(t, recs, testACI2)
	if !c.GetBlocked() || c.GetWhitelisted() || c.GetBlockedAtTimestamp() != uint64(now.UnixMilli()) || c.GetE164() != "+15550001" {
		t.Errorf("contact %+v", c)
	}
	for _, b := range recs {
		r := unmarshalRecord(t, b)
		if p := r.GetStickerPack(); p != nil && bytes.Equal(p.GetPackId(), []byte{9}) && p.GetPosition() != 5 {
			t.Errorf("new pack position %d, want 5", p.GetPosition())
		}
	}
	a := cli.Store.AccountRecord()
	if a == nil || !a.GetTypingIndicators() || len(a.GetPinnedConversations()) != 2 || a.GetGivenName() != "Me" {
		t.Fatalf("account %+v", a)
	}
	if a.PinnedConversations[1].GetContact().GetServiceId() != testACI2.String() {
		t.Error("pinned contact")
	}
}

func TestStorageWriteNoManifest(t *testing.T) {
	f := &fakeStorage{storageKey: deriveStorageServiceKey(testMasterKey), items: map[string][]byte{}}
	cli := testStorageClient(f)
	if _, err := cli.StorageWrite(context.Background(), func(*StorageState) error { return nil }); !errors.Is(err, ErrStorageNoManifest) {
		t.Fatalf("err = %v", err)
	}
	cli.Store.MasterKey = nil
	if _, err := cli.StorageWrite(context.Background(), func(*StorageState) error { return nil }); !errors.Is(err, ErrNoStorageKey) {
		t.Fatalf("err = %v", err)
	}
}

func TestSetPinned(t *testing.T) {
	a := &signalpb.AccountRecord{PinnedConversations: []*signalpb.AccountRecord_PinnedConversation{
		{Identifier: &signalpb.AccountRecord_PinnedConversation_ReleaseNotes_{ReleaseNotes: &signalpb.AccountRecord_PinnedConversation_ReleaseNotes{}}},
		{Identifier: &signalpb.AccountRecord_PinnedConversation_Contact_{Contact: &signalpb.AccountRecord_PinnedConversation_Contact{ServiceIdBinary: testACI1[:]}}},
	}}
	if SetPinned(a, PinnedTarget{ACI: testACI1}, true) {
		t.Error("binary service ID not matched")
	}
	if !SetPinned(a, PinnedTarget{ACI: testACI1}, false) || len(a.PinnedConversations) != 1 {
		t.Error("unpin failed")
	}
	if a.PinnedConversations[0].GetReleaseNotes() == nil {
		t.Error("release notes entry dropped")
	}
	if SetPinned(a, PinnedTarget{ACI: testACI2}, false) {
		t.Error("unpinning a non-pinned chat changed the list")
	}
}

func TestSetStickerPackInstalled(t *testing.T) {
	s := &StorageState{Now: func() time.Time { return time.UnixMilli(1000) }}
	s.SetStickerPackInstalled([]byte{1}, []byte{2}, false)
	if len(s.entries) != 0 {
		t.Fatal("uninstalling an unknown pack added a record")
	}
	s.SetStickerPackInstalled([]byte{1}, []byte{2}, true)
	s.SetStickerPackInstalled([]byte{3}, []byte{4}, true)
	if p := s.StickerPack([]byte{3}); p.GetPosition() != 1 {
		t.Errorf("position %d", p.GetPosition())
	}
	s.SetStickerPackInstalled([]byte{1}, nil, false)
	p := s.StickerPack([]byte{1})
	if p.GetDeletedAtTimestamp() != 1000 || p.GetPackKey() != nil || p.GetPosition() != 0 {
		t.Errorf("uninstalled pack %+v", p)
	}
	s.SetStickerPackInstalled([]byte{1}, []byte{2}, true)
	if p.GetDeletedAtTimestamp() != 0 || !slices.Equal(p.GetPackKey(), []byte{2}) || p.GetPosition() != 2 {
		t.Errorf("reinstalled pack %+v", p)
	}
}

// warte: fixed known-answer vectors computed outside Go (Python hmac /
// hashlib, RFC 5869 HKDF by hand) from Desktop's Crypto.node.ts formulas:
// deriveStorageServiceKey, deriveStorageManifestKey and both branches of
// deriveStorageItemKey (legacy "Item_"+base64(id), and libsignal hkdf with
// empty salt and info "20240801_SIGNAL_STORAGE_SERVICE_ITEM_"||id).
func TestStorageKeyDerivationKnownAnswers(t *testing.T) {
	mk := make([]byte, 32)
	for i := range mk {
		mk[i] = byte(i)
	}
	id := make([]byte, 16)
	for i := range id {
		id[i] = byte(i)
	}
	hexOf := func(b []byte) string { return fmt.Sprintf("%x", b) }
	sk := deriveStorageServiceKey(mk)
	if got := hexOf(sk); got != "d9fd23ee99c148ad879f62609297139e6a36f415b1ad779d23c687b1fa6d36d9" {
		t.Errorf("storage key %s", got)
	}
	if got := hexOf(deriveStorageManifestKey(sk, 5)); got != "13d0f3d16a0776b7320cfcb20a854c8fd8b729311088eecac687ee4fa22e8c23" {
		t.Errorf("manifest key %s", got)
	}
	if got := hexOf(deriveStorageItemKey(sk, nil, id, base64.StdEncoding.EncodeToString(id))); got != "93e3f1cee062cb39adbef00589a172248106ff336017c853154062453939294e" {
		t.Errorf("legacy item key %s", got)
	}
	ikm := bytes.Repeat([]byte{0x42}, 32)
	if got := hexOf(deriveStorageItemKey(sk, ikm, id, base64.StdEncoding.EncodeToString(id))); got != "936647f3a1d03207ba07242090b3c22ab05bba90b3509d8343949396688cde60" {
		t.Errorf("recordIkm item key %s", got)
	}
	// The write path's helper uses the same derivation.
	item, err := encryptStorageItem(sk, ikm, id, &signalpb.StorageRecord{Record: &signalpb.StorageRecord_Account{Account: &signalpb.AccountRecord{GivenName: "x"}}}, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := hex.DecodeString("936647f3a1d03207ba07242090b3c22ab05bba90b3509d8343949396688cde60")
	if _, err := decryptBytes(key, item.GetValue()); err != nil {
		t.Errorf("item not decryptable with the reference key: %v", err)
	}
}

// warte: an installed sticker pack record needs its key (Desktop rejects
// one without), so installing without any known key writes nothing.
func TestSetStickerPackInstalledNeedsKey(t *testing.T) {
	s := &StorageState{Now: func() time.Time { return time.UnixMilli(1000) }}
	s.SetStickerPackInstalled([]byte{1}, nil, true)
	if len(s.entries) != 0 {
		t.Fatal("installing without a key added a record")
	}
	s.SetStickerPackInstalled([]byte{1}, []byte{2}, true)
	s.SetStickerPackInstalled([]byte{1}, []byte{2}, false)
	s.SetStickerPackInstalled([]byte{1}, nil, true) // key was cleared on uninstall
	if p := s.StickerPack([]byte{1}); p.GetDeletedAtTimestamp() == 0 {
		t.Errorf("keyless reinstall changed the record: %+v", p)
	}
	bad := &signalpb.StorageRecord{Record: &signalpb.StorageRecord_StickerPack{StickerPack: &signalpb.StickerPackRecord{PackId: []byte{1}}}}
	if validateStorageRecord(bad) == nil {
		t.Error("installed pack without key passed validation")
	}
}
