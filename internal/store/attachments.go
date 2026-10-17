package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"warte/internal/core"
)

// AttachmentRecord is the local metadata of an attachment, avatar or sticker
// image: where its encrypted bytes live (BlobKey in Blobs) and whatever the
// backend needs to fetch it (Remote, opaque).
type AttachmentRecord struct {
	ID          string // matches core.Attachment.ID / avatar ID
	Owner       string // MessageOwner(id) for message attachments, or e.g. "avatar:<aci>"
	BlobKey     string // "" until downloaded
	ContentType string
	FileName    string
	Size        int64
	Digest      []byte // Signal's attachment digest, for integrity checks
	Downloaded  bool
	Remote      []byte // backend-specific pointer (CDN key, keys, …); opaque here
	CreatedAt   time.Time
}

const msgOwnerPrefix = "msg:"

// MessageOwner is the Owner value for attachments of message id. Such rows
// are reclaimed by PruneAttachments once the message is gone.
func MessageOwner(id core.MessageID) string { return msgOwnerPrefix + id.String() }

// PutAttachment inserts or replaces attachment metadata. CreatedAt defaults
// to now.
func (db *DB) PutAttachment(ctx context.Context, a AttachmentRecord) error {
	if a.ID == "" {
		return errors.New("store: attachment without ID")
	}
	if a.BlobKey != "" && !ValidBlobKey(a.BlobKey) {
		return fmt.Errorf("store: invalid blob key %q", a.BlobKey)
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = db.now()
	}
	return db.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO warte_attachments
			(id, owner, blob_key, content_type, file_name, size, digest, downloaded, remote, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (id) DO UPDATE SET owner = excluded.owner, blob_key = excluded.blob_key,
				content_type = excluded.content_type, file_name = excluded.file_name, size = excluded.size,
				digest = excluded.digest, downloaded = excluded.downloaded, remote = excluded.remote`,
			a.ID, a.Owner, a.BlobKey, a.ContentType, a.FileName, a.Size, a.Digest, b2i(a.Downloaded), a.Remote, toMs(a.CreatedAt))
		if err != nil {
			return fmt.Errorf("store: put attachment: %w", err)
		}
		return nil
	})
}

const attachmentCols = `id, owner, blob_key, content_type, file_name, size, digest, downloaded, remote, created_at`

func scanAttachment(sc interface{ Scan(...any) error }) (AttachmentRecord, error) {
	var a AttachmentRecord
	var downloaded int
	var created int64
	if err := sc.Scan(&a.ID, &a.Owner, &a.BlobKey, &a.ContentType, &a.FileName, &a.Size, &a.Digest, &downloaded, &a.Remote, &created); err != nil {
		return AttachmentRecord{}, err
	}
	a.Downloaded = downloaded == 1
	a.CreatedAt = fromMs(created)
	return a, nil
}

// GetAttachment returns attachment metadata by ID.
func (db *DB) GetAttachment(ctx context.Context, id string) (AttachmentRecord, error) {
	a, err := scanAttachment(db.sql.QueryRowContext(ctx, `SELECT `+attachmentCols+` FROM warte_attachments WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return AttachmentRecord{}, notFound("attachment", id)
	}
	return a, err
}

// ListAttachments returns the attachments of an owner in insertion order.
func (db *DB) ListAttachments(ctx context.Context, owner string) ([]AttachmentRecord, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT `+attachmentCols+` FROM warte_attachments WHERE owner = ? ORDER BY created_at, rowid`, owner)
	if err != nil {
		return nil, fmt.Errorf("store: list attachments: %w", err)
	}
	defer rows.Close()
	var out []AttachmentRecord
	for rows.Next() {
		a, err := scanAttachment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SetAttachmentBlob records that an attachment's bytes are stored in Blobs
// under blobKey and marks it downloaded.
func (db *DB) SetAttachmentBlob(ctx context.Context, id, blobKey string) error {
	if !ValidBlobKey(blobKey) {
		return fmt.Errorf("store: invalid blob key %q", blobKey)
	}
	return db.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE warte_attachments SET blob_key = ?, downloaded = 1 WHERE id = ?`, blobKey, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return notFound("attachment", id)
		}
		return nil
	})
}

// DeleteAttachment removes attachment metadata (not the blob; see
// PruneAttachments / CollectGarbage).
func (db *DB) DeleteAttachment(ctx context.Context, id string) error {
	return db.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM warte_attachments WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return notFound("attachment", id)
		}
		return nil
	})
}

// PruneAttachments deletes metadata of message attachments whose message no
// longer exists, then deletes their blobs from blobs (if non-nil) unless
// another attachment still references the same content. It returns the
// number of metadata rows removed.
func (db *DB) PruneAttachments(ctx context.Context, blobs *Blobs) (int, error) {
	var removed int
	candidates := map[string]bool{}
	err := db.tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id, owner, blob_key FROM warte_attachments WHERE owner LIKE 'msg:%'`)
		if err != nil {
			return err
		}
		type row struct{ id, owner, blob string }
		var rs []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.id, &r.owner, &r.blob); err != nil {
				rows.Close()
				return err
			}
			rs = append(rs, r)
		}
		if err := closeRows(rows); err != nil {
			return err
		}
		live := map[string]bool{}
		for _, r := range rs {
			alive, ok := live[r.owner]
			if !ok {
				mid, err := core.ParseMessageID(strings.TrimPrefix(r.owner, msgOwnerPrefix))
				if err == nil {
					var n int
					if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM warte_messages WHERE chat_id = ? AND author = ? AND ts = ?`,
						idArgs(mid)...).Scan(&n); err != nil {
						return err
					}
					alive = n > 0
				}
				live[r.owner] = alive
			}
			if alive {
				continue
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM warte_attachments WHERE id = ?`, r.id); err != nil {
				return err
			}
			removed++
			if r.blob != "" {
				candidates[r.blob] = true
			}
		}
		return nil
	})
	if err != nil || blobs == nil {
		return removed, err
	}
	for key := range candidates {
		var refs int
		if err := db.sql.QueryRowContext(ctx, `SELECT count(*) FROM warte_attachments WHERE blob_key = ?`, key).Scan(&refs); err != nil {
			return removed, err
		}
		if refs == 0 {
			if err := blobs.Delete(key); err != nil && !errors.Is(err, ErrNotFound) {
				return removed, err
			}
		}
	}
	return removed, nil
}

// CollectGarbage deletes blob files that no attachment row references and
// that are older than minAge (the grace period protects blobs written just
// before their metadata). It returns the number of blobs deleted.
func (db *DB) CollectGarbage(ctx context.Context, blobs *Blobs, minAge time.Duration) (int, error) {
	list, err := blobs.List()
	if err != nil {
		return 0, err
	}
	refs := map[string]bool{}
	rows, err := db.sql.QueryContext(ctx, `SELECT DISTINCT blob_key FROM warte_attachments WHERE blob_key <> ''`)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return 0, err
		}
		refs[k] = true
	}
	if err := closeRows(rows); err != nil {
		return 0, err
	}
	cutoff := db.now().Add(-minAge)
	n := 0
	for _, bi := range list {
		if refs[bi.Key] || bi.ModTime.After(cutoff) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return n, err
		}
		if err := blobs.Delete(bi.Key); err != nil && !errors.Is(err, ErrNotFound) {
			return n, err
		}
		n++
	}
	return n, nil
}
