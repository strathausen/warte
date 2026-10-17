package store

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// blobMagic prefixes every blob file (format version 1).
var blobMagic = []byte("WBL1")

const (
	blobNonceSize = 12
	blobKeyLen    = 64 // hex-encoded HMAC-SHA256
	blobTmpPrefix = ".tmp-"
)

// ErrBlobCorrupt is returned by Blobs.Get when a file fails authentication
// (tampered, truncated, or encrypted under another key).
var ErrBlobCorrupt = errors.New("store: blob corrupt or encrypted with another key")

// Blobs is an encrypted, content-addressed file store for attachments,
// avatars and stickers. Each file is AES-256-GCM encrypted with a fresh
// random nonce; plaintext never touches disk.
//
// Keys are content addressed: a key is the hex HMAC-SHA256 of the plaintext
// under a secret derived from the store key, so identical content dedupes to
// one file without revealing a plain hash of it. Because of deduplication,
// Delete removes the content for every holder of that key; callers that
// share blobs (see DB.PruneAttachments) must check references first.
//
// Files live in dir/<first two hex chars>/<key>, written atomically (temp
// file + rename) with mode 0600 in directories with mode 0700.
//
// File layout: "WBL1" | nonce (12 bytes) | ciphertext+tag. The key name is
// bound as additional authenticated data, so files cannot be swapped.
//
// Blobs is safe for concurrent use.
type Blobs struct {
	dir    string
	aead   cipher.AEAD
	macKey []byte
}

// OpenBlobs opens (creating if needed) a blob store in dir with a 32-byte key.
func OpenBlobs(dir string, key []byte) (*Blobs, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("store: blob key must be %d bytes, got %d", KeySize, len(key))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("store: create blob dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("store: chmod blob dir: %w", err)
	}
	encKey, err := hkdf.Key(sha256.New, key, nil, "warte blob enc v1", 32)
	if err != nil {
		return nil, err
	}
	macKey, err := hkdf.Key(sha256.New, key, nil, "warte blob mac v1", 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(encKey)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Blobs{dir: dir, aead: aead, macKey: macKey}, nil
}

// Dir returns the store directory.
func (b *Blobs) Dir() string { return b.dir }

// KeyFor returns the key Put would assign to data, without storing it.
func (b *Blobs) KeyFor(data []byte) string {
	m := hmac.New(sha256.New, b.macKey)
	m.Write(data)
	return hex.EncodeToString(m.Sum(nil))
}

// ValidBlobKey reports whether key is syntactically a blob key.
func ValidBlobKey(key string) bool {
	if len(key) != blobKeyLen {
		return false
	}
	for _, c := range key {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func (b *Blobs) path(key string) string { return filepath.Join(b.dir, key[:2], key) }

// Put encrypts and stores data, returning its key. Storing content that is
// already present is a cheap no-op returning the same key.
func (b *Blobs) Put(data []byte) (string, error) {
	key := b.KeyFor(data)
	p := b.path(key)
	if _, err := os.Stat(p); err == nil {
		return key, nil
	}
	shard := filepath.Dir(p)
	if err := os.MkdirAll(shard, 0o700); err != nil {
		return "", fmt.Errorf("store: create blob shard: %w", err)
	}
	nonce := make([]byte, blobNonceSize, len(blobMagic)+blobNonceSize+len(data)+b.aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := append(append([]byte{}, blobMagic...), nonce...)
	out = b.aead.Seal(out, nonce, data, blobAAD(key))

	tmp, err := os.CreateTemp(shard, blobTmpPrefix+"*")
	if err != nil {
		return "", fmt.Errorf("store: create temp blob: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", err
	}
	if _, err := tmp.Write(out); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", fmt.Errorf("store: write blob: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", fmt.Errorf("store: sync blob: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", err
	}
	if err := os.Rename(tmpName, p); err != nil {
		cleanup()
		return "", fmt.Errorf("store: commit blob: %w", err)
	}
	return key, nil
}

func blobAAD(key string) []byte { return []byte("warte-blob-v1:" + key) }

// Get decrypts and returns the blob stored under key.
func (b *Blobs) Get(key string) ([]byte, error) {
	if !ValidBlobKey(key) {
		return nil, fmt.Errorf("store: invalid blob key %q", key)
	}
	raw, err := os.ReadFile(b.path(key))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, notFound("blob", key)
	}
	if err != nil {
		return nil, fmt.Errorf("store: read blob: %w", err)
	}
	hdr := len(blobMagic) + blobNonceSize
	if len(raw) < hdr+b.aead.Overhead() || !bytes.Equal(raw[:len(blobMagic)], blobMagic) {
		return nil, ErrBlobCorrupt
	}
	pt, err := b.aead.Open(nil, raw[len(blobMagic):hdr], raw[hdr:], blobAAD(key))
	if err != nil {
		return nil, ErrBlobCorrupt
	}
	return pt, nil
}

// Has reports whether a blob exists under key.
func (b *Blobs) Has(key string) bool {
	if !ValidBlobKey(key) {
		return false
	}
	_, err := os.Stat(b.path(key))
	return err == nil
}

// Delete removes the blob stored under key. Deleting a missing blob returns
// an error wrapping ErrNotFound.
func (b *Blobs) Delete(key string) error {
	if !ValidBlobKey(key) {
		return fmt.Errorf("store: invalid blob key %q", key)
	}
	err := os.Remove(b.path(key))
	if errors.Is(err, fs.ErrNotExist) {
		return notFound("blob", key)
	}
	return err
}

// BlobInfo describes a stored blob file.
type BlobInfo struct {
	Key     string
	Size    int64 // encrypted file size
	ModTime time.Time
}

// List returns all stored blobs. Stale temp files from interrupted writes
// older than an hour are removed along the way.
func (b *Blobs) List() ([]BlobInfo, error) {
	var out []BlobInfo
	err := filepath.WalkDir(b.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		name := d.Name()
		if strings.HasPrefix(name, blobTmpPrefix) {
			if time.Since(info.ModTime()) > time.Hour {
				_ = os.Remove(p)
			}
			return nil
		}
		if ValidBlobKey(name) {
			out = append(out, BlobInfo{Key: name, Size: info.Size(), ModTime: info.ModTime()})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("store: list blobs: %w", err)
	}
	return out, nil
}
