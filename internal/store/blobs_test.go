package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"warte/internal/core"
)

func newBlobs(t *testing.T) (*Blobs, []byte) {
	t.Helper()
	key := randKey(t)
	b, err := OpenBlobs(filepath.Join(t.TempDir(), "blobs"), key)
	if err != nil {
		t.Fatal(err)
	}
	return b, key
}

func TestBlobsRoundTrip(t *testing.T) {
	b, _ := newBlobs(t)
	tests := []struct {
		name string
		data []byte
	}{
		{"empty", []byte{}},
		{"small", []byte("hello")},
		{"binary", bytes.Repeat([]byte{0, 1, 2, 255}, 1000)},
		{"large", bytes.Repeat([]byte("x"), 3<<20)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, err := b.Put(tt.data)
			if err != nil {
				t.Fatal(err)
			}
			if !ValidBlobKey(key) || key != b.KeyFor(tt.data) {
				t.Fatalf("bad key %q", key)
			}
			got, err := b.Get(key)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, tt.data) {
				t.Fatal("data mismatch")
			}
		})
	}
}

func TestBlobsNoPlaintextOnDisk(t *testing.T) {
	b, _ := newBlobs(t)
	secret := []byte("TOP SECRET attachment contents 1234567890")
	key, err := b.Put(secret)
	if err != nil {
		t.Fatal(err)
	}
	k2, _ := b.Put(secret) // dedupe: second put writes nothing new
	if k2 != key {
		t.Fatalf("dedupe key %q != %q", k2, key)
	}
	files := 0
	err = filepath.WalkDir(b.Dir(), func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, _ := d.Info()
		if d.IsDir() {
			if info.Mode().Perm() != 0o700 {
				t.Errorf("dir %s mode %o", p, info.Mode().Perm())
			}
			return nil
		}
		files++
		if info.Mode().Perm() != 0o600 {
			t.Errorf("file %s mode %o", p, info.Mode().Perm())
		}
		raw, _ := os.ReadFile(p)
		if bytes.Contains(raw, secret) || bytes.Contains(raw, secret[:12]) {
			t.Errorf("%s contains plaintext", p)
		}
		if strings.HasPrefix(d.Name(), blobTmpPrefix) {
			t.Errorf("leftover temp file %s", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files != 1 {
		t.Errorf("files = %d, want 1", files)
	}
	// Same plaintext encrypts differently each time (random nonce).
	b2, _ := newBlobs(t)
	k3, _ := b2.Put(secret)
	raw1, _ := os.ReadFile(b.path(key))
	if err := b2.Delete(k3); err != nil {
		t.Fatal(err)
	}
	if k3 == key {
		t.Error("blob keys must depend on the store key")
	}
	if err := b.Delete(key); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Put(secret); err != nil {
		t.Fatal(err)
	}
	raw2, _ := os.ReadFile(b.path(key))
	if bytes.Equal(raw1, raw2) {
		t.Error("ciphertext identical across writes; nonce reuse?")
	}
}

func TestBlobsWrongKeyAndTampering(t *testing.T) {
	b, _ := newBlobs(t)
	key, _ := b.Put([]byte("payload"))

	other, err := OpenBlobs(b.Dir(), randKey(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Get(key); !errors.Is(err, ErrBlobCorrupt) {
		t.Errorf("wrong key err = %v", err)
	}

	p := b.path(key)
	raw, _ := os.ReadFile(p)
	tests := []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{"flip ciphertext bit", func(r []byte) []byte { c := bytes.Clone(r); c[len(c)-1] ^= 1; return c }},
		{"flip nonce bit", func(r []byte) []byte { c := bytes.Clone(r); c[5] ^= 1; return c }},
		{"truncate", func(r []byte) []byte { return r[:10] }},
		{"bad magic", func(r []byte) []byte { c := bytes.Clone(r); c[0] = 'X'; return c }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := os.WriteFile(p, tt.mutate(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := b.Get(key); !errors.Is(err, ErrBlobCorrupt) {
				t.Errorf("err = %v", err)
			}
		})
	}
	// Moving a valid file under another key name fails (key bound as AAD).
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	k2, _ := b.Put([]byte("other payload"))
	if err := os.WriteFile(b.path(k2), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Get(k2); !errors.Is(err, ErrBlobCorrupt) {
		t.Errorf("swapped file err = %v", err)
	}
}

func TestBlobsDeleteAndInvalidKeys(t *testing.T) {
	b, _ := newBlobs(t)
	key, _ := b.Put([]byte("x"))
	if !b.Has(key) {
		t.Fatal("Has = false")
	}
	if err := b.Delete(key); err != nil {
		t.Fatal(err)
	}
	if b.Has(key) {
		t.Error("Has after delete")
	}
	if _, err := b.Get(key); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Get deleted err = %v", err)
	}
	if err := b.Delete(key); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Delete twice err = %v", err)
	}
	for _, bad := range []string{"", "../x", strings.Repeat("g", 64), strings.Repeat("A", 64), strings.Repeat("a", 63)} {
		if _, err := b.Get(bad); err == nil || errors.Is(err, core.ErrNotFound) {
			t.Errorf("Get(%q) err = %v", bad, err)
		}
		if err := b.Delete(bad); err == nil {
			t.Errorf("Delete(%q) succeeded", bad)
		}
		if b.Has(bad) {
			t.Errorf("Has(%q)", bad)
		}
	}
	if _, err := OpenBlobs(t.TempDir(), []byte("short")); err == nil {
		t.Error("short key accepted")
	}
}

func TestBlobsListAndConcurrency(t *testing.T) {
	b, _ := newBlobs(t)
	var wg sync.WaitGroup
	keys := make([]string, 32)
	for i := range keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Half the goroutines write identical content concurrently.
			data := []byte{byte(i % 16)}
			k, err := b.Put(data)
			if err != nil {
				t.Error(err)
				return
			}
			keys[i] = k
		}()
	}
	wg.Wait()
	list, err := b.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 16 {
		t.Errorf("List = %d blobs, want 16", len(list))
	}
	for i, k := range keys {
		got, err := b.Get(k)
		if err != nil || !bytes.Equal(got, []byte{byte(i % 16)}) {
			t.Errorf("blob %d: %v %v", i, got, err)
		}
	}
}
