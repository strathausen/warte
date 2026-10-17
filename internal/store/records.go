package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"warte/internal/core"
)

// --- stories ------------------------------------------------------------------

// PutStory inserts or replaces a story keyed by (ID.Author, ID.Timestamp),
// which is how story replies reference it. Replies is derived on read.
func (db *DB) PutStory(ctx context.Context, s core.Story) error {
	if s.ID.Author == "" || s.ID.Timestamp == 0 {
		return fmt.Errorf("store: invalid story id %q", s.ID)
	}
	if s.At.IsZero() {
		s.At = time.UnixMilli(int64(s.ID.Timestamp)).UTC()
	}
	s.Replies = 0
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return db.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO warte_stories (author, ts, group_id, at, viewed, data)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (author, ts) DO UPDATE SET group_id = excluded.group_id, at = excluded.at,
				viewed = excluded.viewed, data = excluded.data`,
			string(s.ID.Author), int64(s.ID.Timestamp), string(s.Group), toMs(s.At), b2i(s.Viewed), data)
		if err != nil {
			return fmt.Errorf("store: put story: %w", err)
		}
		return nil
	})
}

const storySelect = `SELECT s.data, s.viewed,
	(SELECT count(*) FROM warte_messages m WHERE m.story_author = s.author AND m.story_ts = s.ts)
	FROM warte_stories s `

func scanStory(sc interface{ Scan(...any) error }) (core.Story, error) {
	var data []byte
	var viewed, replies int
	if err := sc.Scan(&data, &viewed, &replies); err != nil {
		return core.Story{}, err
	}
	var s core.Story
	if err := json.Unmarshal(data, &s); err != nil {
		return core.Story{}, fmt.Errorf("store: decode story: %w", err)
	}
	s.Viewed = viewed == 1
	s.Replies = replies
	return s, nil
}

// GetStory returns the story by author and timestamp (id.Chat is ignored).
func (db *DB) GetStory(ctx context.Context, id core.MessageID) (core.Story, error) {
	s, err := scanStory(db.sql.QueryRowContext(ctx, storySelect+`WHERE s.author = ? AND s.ts = ?`,
		string(id.Author), int64(id.Timestamp)))
	if errors.Is(err, sql.ErrNoRows) {
		return core.Story{}, notFound("story", id)
	}
	return s, err
}

// ListStories returns stories posted at or after since, oldest first.
func (db *DB) ListStories(ctx context.Context, since time.Time) ([]core.Story, error) {
	rows, err := db.sql.QueryContext(ctx, storySelect+`WHERE s.at >= ? ORDER BY s.at, s.author, s.ts`, toMs(since))
	if err != nil {
		return nil, fmt.Errorf("store: list stories: %w", err)
	}
	defer rows.Close()
	var out []core.Story
	for rows.Next() {
		s, err := scanStory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// MarkStoryViewed flags a story as viewed.
func (db *DB) MarkStoryViewed(ctx context.Context, id core.MessageID) error {
	return db.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE warte_stories SET viewed = 1 WHERE author = ? AND ts = ?`,
			string(id.Author), int64(id.Timestamp))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return notFound("story", id)
		}
		return nil
	})
}

// DeleteStory removes a story.
func (db *DB) DeleteStory(ctx context.Context, id core.MessageID) error {
	return db.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM warte_stories WHERE author = ? AND ts = ?`,
			string(id.Author), int64(id.Timestamp))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return notFound("story", id)
		}
		return nil
	})
}

// DeleteStoriesBefore removes stories posted before t (Signal stories last
// 24 hours) and returns how many were removed.
func (db *DB) DeleteStoriesBefore(ctx context.Context, t time.Time) (int, error) {
	var n int64
	err := db.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM warte_stories WHERE at < ?`, toMs(t))
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	return int(n), err
}

// --- call log -------------------------------------------------------------------

// PutCall inserts or replaces a call log entry keyed by (Chat,
// Call.StartedAt).
func (db *DB) PutCall(ctx context.Context, e core.CallLogEntry) error {
	if e.Chat == "" || e.Call.StartedAt.IsZero() {
		return errors.New("store: call log entry needs Chat and Call.StartedAt")
	}
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return db.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO warte_calls (chat_id, started_at, unread, data) VALUES (?, ?, ?, ?)
			ON CONFLICT (chat_id, started_at) DO UPDATE SET unread = excluded.unread, data = excluded.data`,
			string(e.Chat), toMs(e.Call.StartedAt), b2i(e.Unread), data)
		if err != nil {
			return fmt.Errorf("store: put call: %w", err)
		}
		return nil
	})
}

// ListCalls returns the call log newest first; limit <= 0 returns all.
func (db *DB) ListCalls(ctx context.Context, limit int) ([]core.CallLogEntry, error) {
	if limit <= 0 {
		limit = -1
	}
	rows, err := db.sql.QueryContext(ctx, `SELECT data, unread FROM warte_calls ORDER BY started_at DESC, chat_id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list calls: %w", err)
	}
	defer rows.Close()
	var out []core.CallLogEntry
	for rows.Next() {
		var data []byte
		var unread int
		if err := rows.Scan(&data, &unread); err != nil {
			return nil, err
		}
		var e core.CallLogEntry
		if err := json.Unmarshal(data, &e); err != nil {
			return nil, fmt.Errorf("store: decode call: %w", err)
		}
		e.Unread = unread == 1
		out = append(out, e)
	}
	return out, rows.Err()
}

// UnreadCalls counts unread (missed, unseen) call log entries.
func (db *DB) UnreadCalls(ctx context.Context) (int, error) {
	var n int
	err := db.sql.QueryRowContext(ctx, `SELECT count(*) FROM warte_calls WHERE unread = 1`).Scan(&n)
	return n, err
}

// MarkCallsRead clears the unread flag on all call log entries.
func (db *DB) MarkCallsRead(ctx context.Context) error {
	return db.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE warte_calls SET unread = 0 WHERE unread = 1`)
		return err
	})
}

// DeleteCall removes one call log entry.
func (db *DB) DeleteCall(ctx context.Context, chat core.ChatID, startedAt time.Time) error {
	return db.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM warte_calls WHERE chat_id = ? AND started_at = ?`, string(chat), toMs(startedAt))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return notFound("call", chat)
		}
		return nil
	})
}

// ClearCalls empties the call log.
func (db *DB) ClearCalls(ctx context.Context) error {
	return db.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM warte_calls`)
		return err
	})
}

// --- sticker packs ----------------------------------------------------------------

// PutStickerPack inserts or replaces a sticker pack. Newly installed packs
// are appended to the end of the installed order.
func (db *DB) PutStickerPack(ctx context.Context, p core.StickerPack) error {
	if p.ID == "" {
		return errors.New("store: sticker pack without ID")
	}
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return db.tx(ctx, func(tx *sql.Tx) error {
		pos, err := stickerPosition(ctx, tx, p.ID, p.Installed)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO warte_sticker_packs (id, installed, position, title, data) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (id) DO UPDATE SET installed = excluded.installed, position = excluded.position,
				title = excluded.title, data = excluded.data`,
			p.ID, b2i(p.Installed), pos, p.Title, data)
		if err != nil {
			return fmt.Errorf("store: put sticker pack: %w", err)
		}
		return nil
	})
}

// stickerPosition keeps an installed pack's position, or assigns the next one.
func stickerPosition(ctx context.Context, q querier, id string, installed bool) (int, error) {
	if !installed {
		return 0, nil
	}
	var wasInstalled, pos int
	err := q.QueryRowContext(ctx, `SELECT installed, position FROM warte_sticker_packs WHERE id = ?`, id).Scan(&wasInstalled, &pos)
	if err == nil && wasInstalled == 1 {
		return pos, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	var maxPos sql.NullInt64
	if err := q.QueryRowContext(ctx, `SELECT max(position) FROM warte_sticker_packs WHERE installed = 1`).Scan(&maxPos); err != nil {
		return 0, err
	}
	return int(maxPos.Int64) + 1, nil
}

func decodePack(data []byte, installed int) (core.StickerPack, error) {
	var p core.StickerPack
	if err := json.Unmarshal(data, &p); err != nil {
		return core.StickerPack{}, fmt.Errorf("store: decode sticker pack: %w", err)
	}
	p.Installed = installed == 1
	return p, nil
}

// GetStickerPack returns a sticker pack by ID.
func (db *DB) GetStickerPack(ctx context.Context, id string) (core.StickerPack, error) {
	var data []byte
	var installed int
	err := db.sql.QueryRowContext(ctx, `SELECT data, installed FROM warte_sticker_packs WHERE id = ?`, id).Scan(&data, &installed)
	if errors.Is(err, sql.ErrNoRows) {
		return core.StickerPack{}, notFound("sticker pack", id)
	}
	if err != nil {
		return core.StickerPack{}, err
	}
	return decodePack(data, installed)
}

// ListStickerPacks returns installed packs in install order followed (unless
// installedOnly) by known but uninstalled packs sorted by title.
func (db *DB) ListStickerPacks(ctx context.Context, installedOnly bool) ([]core.StickerPack, error) {
	q := `SELECT data, installed FROM warte_sticker_packs`
	if installedOnly {
		q += ` WHERE installed = 1`
	}
	q += ` ORDER BY installed DESC, CASE WHEN installed = 1 THEN position END, title COLLATE NOCASE, id`
	rows, err := db.sql.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("store: list sticker packs: %w", err)
	}
	defer rows.Close()
	var out []core.StickerPack
	for rows.Next() {
		var data []byte
		var installed int
		if err := rows.Scan(&data, &installed); err != nil {
			return nil, err
		}
		p, err := decodePack(data, installed)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetStickerPackInstalled installs or uninstalls a known pack.
func (db *DB) SetStickerPackInstalled(ctx context.Context, id string, installed bool) error {
	return db.tx(ctx, func(tx *sql.Tx) error {
		pos, err := stickerPosition(ctx, tx, id, installed)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE warte_sticker_packs SET installed = ?, position = ? WHERE id = ?`, b2i(installed), pos, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return notFound("sticker pack", id)
		}
		return nil
	})
}

// DeleteStickerPack forgets a sticker pack.
func (db *DB) DeleteStickerPack(ctx context.Context, id string) error {
	return db.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM warte_sticker_packs WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return notFound("sticker pack", id)
		}
		return nil
	})
}

// --- folders ----------------------------------------------------------------------

// PutFolder inserts or replaces a chat folder. New folders go last.
func (db *DB) PutFolder(ctx context.Context, f core.Folder) error {
	if f.ID == "" {
		return errors.New("store: folder without ID")
	}
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return db.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO warte_folders (id, position, data)
			VALUES (?, (SELECT coalesce(max(position), 0) + 1 FROM warte_folders), ?)
			ON CONFLICT (id) DO UPDATE SET data = excluded.data`, f.ID, data)
		if err != nil {
			return fmt.Errorf("store: put folder: %w", err)
		}
		return nil
	})
}

// GetFolder returns a folder by ID.
func (db *DB) GetFolder(ctx context.Context, id string) (core.Folder, error) {
	var data []byte
	err := db.sql.QueryRowContext(ctx, `SELECT data FROM warte_folders WHERE id = ?`, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return core.Folder{}, notFound("folder", id)
	}
	if err != nil {
		return core.Folder{}, err
	}
	var f core.Folder
	if err := json.Unmarshal(data, &f); err != nil {
		return core.Folder{}, fmt.Errorf("store: decode folder: %w", err)
	}
	return f, nil
}

// ListFolders returns folders in display order.
func (db *DB) ListFolders(ctx context.Context) ([]core.Folder, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT data FROM warte_folders ORDER BY position, id`)
	if err != nil {
		return nil, fmt.Errorf("store: list folders: %w", err)
	}
	defer rows.Close()
	var out []core.Folder
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var f core.Folder
		if err := json.Unmarshal(data, &f); err != nil {
			return nil, fmt.Errorf("store: decode folder: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ReorderFolders sets the display order to ids; folders not listed keep
// their relative order after the listed ones. Unknown IDs yield ErrNotFound.
func (db *DB) ReorderFolders(ctx context.Context, ids []string) error {
	return db.tx(ctx, func(tx *sql.Tx) error {
		for i, id := range ids {
			res, err := tx.ExecContext(ctx, `UPDATE warte_folders SET position = ? WHERE id = ?`, i-len(ids)-1_000_000, id)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return notFound("folder", id)
			}
		}
		// Renumber 1..n preserving the new order.
		rows, err := tx.QueryContext(ctx, `SELECT id FROM warte_folders ORDER BY position, id`)
		if err != nil {
			return err
		}
		var all []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			all = append(all, id)
		}
		if err := closeRows(rows); err != nil {
			return err
		}
		for i, id := range all {
			if _, err := tx.ExecContext(ctx, `UPDATE warte_folders SET position = ? WHERE id = ?`, i+1, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// DeleteFolder removes a folder.
func (db *DB) DeleteFolder(ctx context.Context, id string) error {
	return db.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM warte_folders WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return notFound("folder", id)
		}
		return nil
	})
}

// --- settings -----------------------------------------------------------------------

// settingsKey holds core.Settings in the key/value table.
const settingsKey = "core.settings"

// SetSetting stores v (JSON-encoded) under key.
func (db *DB) SetSetting(ctx context.Context, key string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("store: encode setting %q: %w", key, err)
	}
	return db.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO warte_settings (key, value) VALUES (?, ?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, data)
		return err
	})
}

// GetSetting decodes the value stored under key into out. It returns an
// error wrapping ErrNotFound if the key is unset.
func (db *DB) GetSetting(ctx context.Context, key string, out any) error {
	var data []byte
	err := db.sql.QueryRowContext(ctx, `SELECT value FROM warte_settings WHERE key = ?`, key).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return notFound("setting", key)
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("store: decode setting %q: %w", key, err)
	}
	return nil
}

// DeleteSetting removes key (no error if unset).
func (db *DB) DeleteSetting(ctx context.Context, key string) error {
	return db.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM warte_settings WHERE key = ?`, key)
		return err
	})
}

// DefaultSettings are Signal's defaults for a fresh account.
func DefaultSettings() core.Settings {
	return core.Settings{
		ReadReceipts:        true,
		TypingIndicators:    true,
		LinkPreviews:        true,
		NotificationLevel:   core.NotifyNameAndMessage,
		DiscoverableByPhone: true,
	}
}

// Settings returns the stored account settings, or DefaultSettings if none
// were saved yet.
func (db *DB) Settings(ctx context.Context) (core.Settings, error) {
	s := DefaultSettings()
	err := db.GetSetting(ctx, settingsKey, &s)
	if errors.Is(err, ErrNotFound) {
		return DefaultSettings(), nil
	}
	return s, err
}

// SaveSettings stores the account settings.
func (db *DB) SaveSettings(ctx context.Context, s core.Settings) error {
	return db.SetSetting(ctx, settingsKey, s)
}
