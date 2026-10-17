package backend

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"warte/internal/core"
)

// Export file format (version 1)
//
// ExportBackup writes warte's local state (not Signal's keys or sessions)
// as one encrypted file:
//
//	offset  size  content
//	0       8     magic "WARTEXP1"
//	8       16    random salt
//	24      12    random AES-GCM nonce
//	36      ...   AES-256-GCM ciphertext + 16-byte tag
//
// The key is HKDF-SHA256(blob key, salt, info "warte export v1"); the 36
// header bytes are authenticated as additional data. The plaintext is a
// gzip-compressed JSON Export document. Attachment bytes are not included
// (only their metadata); they stay in the encrypted blob store. ReadExport
// decrypts a file with the same blob key.
const (
	exportMagic   = "WARTEXP1"
	exportVersion = 1
	exportInfo    = "warte export v1"
	exportPage    = 500
)

// Export is the plaintext document of an export file.
type Export struct {
	Version      int                 `json:"version"`
	CreatedAt    time.Time           `json:"created_at"`
	Self         core.Contact        `json:"self"`
	Settings     core.Settings       `json:"settings"`
	Contacts     []core.Contact      `json:"contacts"`
	Chats        []ExportChat        `json:"chats"`
	Stories      []core.Story        `json:"stories,omitempty"`
	Calls        []core.CallLogEntry `json:"calls,omitempty"`
	StickerPacks []core.StickerPack  `json:"sticker_packs,omitempty"`
	Folders      []core.Folder       `json:"folders,omitempty"`
}

// ExportChat is one chat with its messages, oldest first.
type ExportChat struct {
	Chat     core.Chat      `json:"chat"`
	Messages []core.Message `json:"messages"`
}

// ErrNoExportKey is returned when no blob key was configured.
var ErrNoExportKey = errors.New("backend: export needs Options.BlobKey")

// ExportBackup writes an encrypted export of warte's local data to path
// (mode 0600, written atomically).
func (b *Backend) ExportBackup(ctx context.Context, path string) error {
	if len(b.opts.BlobKey) == 0 {
		return ErrNoExportKey
	}
	doc, err := b.collectExport(ctx)
	if err != nil {
		return err
	}
	return writeExport(path, b.opts.BlobKey, doc)
}

// collectExport gathers everything into an Export.
func (b *Backend) collectExport(ctx context.Context) (*Export, error) {
	// localSelf: export works offline, without Start.
	doc := &Export{Version: exportVersion, CreatedAt: b.now().UTC(), Self: b.localSelf(ctx)}
	var err error
	if doc.Settings, err = b.db.Settings(ctx); err != nil {
		return nil, err
	}
	if doc.Contacts, err = b.db.ListContacts(ctx); err != nil {
		return nil, err
	}
	chats, err := b.allChats(ctx)
	if err != nil {
		return nil, err
	}
	for _, c := range chats {
		ec := ExportChat{Chat: c}
		var before core.MessageID
		for {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			page, err := b.db.ListMessages(ctx, c.ID, before, exportPage)
			if err != nil {
				return nil, err
			}
			ec.Messages = append(page, ec.Messages...)
			if len(page) < exportPage {
				break
			}
			before = page[0].ID
		}
		doc.Chats = append(doc.Chats, ec)
	}
	if doc.Stories, err = b.db.ListStories(ctx, time.Time{}); err != nil {
		return nil, err
	}
	if doc.Calls, err = b.db.ListCalls(ctx, 0); err != nil {
		return nil, err
	}
	if doc.StickerPacks, err = b.db.ListStickerPacks(ctx, false); err != nil {
		return nil, err
	}
	if doc.Folders, err = b.db.ListFolders(ctx); err != nil {
		return nil, err
	}
	return doc, nil
}

func exportKey(blobKey, salt []byte) ([]byte, error) {
	return hkdf.Key(sha256.New, blobKey, salt, exportInfo, 32)
}

// writeExport encrypts doc with a key derived from blobKey and writes it.
func writeExport(path string, blobKey []byte, doc *Export) error {
	var plain bytes.Buffer
	zw := gzip.NewWriter(&plain)
	if err := json.NewEncoder(zw).Encode(doc); err != nil {
		return fmt.Errorf("backend: encode export: %w", err)
	}
	if err := zw.Close(); err != nil {
		return err
	}
	header := make([]byte, len(exportMagic)+16+12)
	copy(header, exportMagic)
	if _, err := rand.Read(header[len(exportMagic):]); err != nil {
		return err
	}
	salt := header[len(exportMagic) : len(exportMagic)+16]
	nonce := header[len(exportMagic)+16:]
	key, err := exportKey(blobKey, salt)
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	sealed := gcm.Seal(header, nonce, plain.Bytes(), header)
	clear(plain.Bytes())

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".warte-export-*")
	if err != nil {
		return fmt.Errorf("backend: create export: %w", err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(sealed); err != nil {
		tmp.Close()
		return fmt.Errorf("backend: write export: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("backend: write export: %w", err)
	}
	return nil
}

// ReadExport decrypts and decodes an export file written by ExportBackup.
func ReadExport(path string, blobKey []byte) (*Export, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	hl := len(exportMagic) + 16 + 12
	if len(raw) < hl+16 || string(raw[:len(exportMagic)]) != exportMagic {
		return nil, errors.New("backend: not a warte export file")
	}
	header := raw[:hl]
	key, err := exportKey(blobKey, header[len(exportMagic):len(exportMagic)+16])
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, header[len(exportMagic)+16:], raw[hl:], header)
	if err != nil {
		return nil, errors.New("backend: export is corrupt or was made with another key")
	}
	zr, err := gzip.NewReader(bytes.NewReader(plain))
	if err != nil {
		return nil, err
	}
	var doc Export
	if err := json.NewDecoder(io.LimitReader(zr, 8<<30)).Decode(&doc); err != nil {
		return nil, fmt.Errorf("backend: decode export: %w", err)
	}
	if doc.Version != exportVersion {
		return nil, fmt.Errorf("backend: unsupported export version %d", doc.Version)
	}
	return &doc, nil
}
