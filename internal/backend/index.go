package backend

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"warte/internal/core"
	"warte/internal/store"
)

// The backend keeps one table of its own in the shared database, next to
// store's warte_* tables: an index from (author, sent timestamp) to the
// message it identifies. Signal refers to messages by author and timestamp
// only (receipts, read syncs, view-once syncs), and edits get a new
// timestamp that must resolve to the original message. It also remembers
// the server GUID and spam-report token the server attached to incoming
// messages, which ReportSpam needs.
const indexSchema = `
CREATE TABLE IF NOT EXISTS warte_be_msgindex (
	author       TEXT    NOT NULL,
	ts           INTEGER NOT NULL,
	chat_id      TEXT    NOT NULL,
	orig_ts      INTEGER NOT NULL,
	server_guid  TEXT    NOT NULL DEFAULT '',
	report_token BLOB,
	PRIMARY KEY (author, ts)
);
CREATE INDEX IF NOT EXISTS warte_be_msgindex_chat ON warte_be_msgindex (chat_id, orig_ts);
`

// msgIndex wraps the side table.
type msgIndex struct{ db *sql.DB }

func newMsgIndex(ctx context.Context, db *sql.DB) (*msgIndex, error) {
	if _, err := db.ExecContext(ctx, indexSchema); err != nil {
		return nil, fmt.Errorf("backend: create message index: %w", err)
	}
	return &msgIndex{db: db}, nil
}

// put records that (id.Author, ts) refers to message id. ts is id.Timestamp
// for the message itself or an edit's timestamp for an alias. guid and token
// are only stored when non-empty.
func (x *msgIndex) put(ctx context.Context, id core.MessageID, ts uint64, guid string, token []byte) error {
	if ts == 0 {
		ts = id.Timestamp
	}
	_, err := x.db.ExecContext(ctx, `INSERT INTO warte_be_msgindex (author, ts, chat_id, orig_ts, server_guid, report_token)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (author, ts) DO UPDATE SET chat_id = excluded.chat_id, orig_ts = excluded.orig_ts,
			server_guid = CASE WHEN excluded.server_guid <> '' THEN excluded.server_guid ELSE warte_be_msgindex.server_guid END,
			report_token = COALESCE(excluded.report_token, warte_be_msgindex.report_token)`,
		string(id.Author), int64(ts), string(id.Chat), int64(id.Timestamp), guid, token)
	if err != nil {
		return fmt.Errorf("backend: index message: %w", err)
	}
	return nil
}

// lookup resolves (author, ts) to the message it identifies.
func (x *msgIndex) lookup(ctx context.Context, author core.ACI, ts uint64) (core.MessageID, bool, error) {
	var chat string
	var orig int64
	err := x.db.QueryRowContext(ctx, `SELECT chat_id, orig_ts FROM warte_be_msgindex WHERE author = ? AND ts = ?`,
		string(author), int64(ts)).Scan(&chat, &orig)
	if errors.Is(err, sql.ErrNoRows) {
		return core.MessageID{}, false, nil
	}
	if err != nil {
		return core.MessageID{}, false, fmt.Errorf("backend: lookup message: %w", err)
	}
	return core.MessageID{Chat: core.ChatID(chat), Author: author, Timestamp: uint64(orig)}, true, nil
}

// lastEdit returns the timestamp of the newest edit of id (0 if never
// edited).
func (x *msgIndex) lastEdit(ctx context.Context, id core.MessageID) (uint64, error) {
	var ts sql.NullInt64
	err := x.db.QueryRowContext(ctx, `SELECT max(ts) FROM warte_be_msgindex WHERE chat_id = ? AND author = ? AND orig_ts = ? AND ts <> orig_ts`,
		string(id.Chat), string(id.Author), int64(id.Timestamp)).Scan(&ts)
	if err != nil {
		return 0, fmt.Errorf("backend: last edit: %w", err)
	}
	return uint64(ts.Int64), nil
}

// spamReport returns the server GUID and report token of the newest message
// from author in chat that carried them.
func (x *msgIndex) spamReport(ctx context.Context, chat core.ChatID, author core.ACI) (string, []byte, error) {
	var guid string
	var token []byte
	err := x.db.QueryRowContext(ctx, `SELECT server_guid, report_token FROM warte_be_msgindex
		WHERE chat_id = ? AND author = ? AND server_guid <> '' ORDER BY ts DESC LIMIT 1`,
		string(chat), string(author)).Scan(&guid, &token)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, nil
	}
	return guid, token, err
}

// forget removes the entries of one message.
func (x *msgIndex) forget(ctx context.Context, id core.MessageID) error {
	_, err := x.db.ExecContext(ctx, `DELETE FROM warte_be_msgindex WHERE chat_id = ? AND author = ? AND orig_ts = ?`,
		string(id.Chat), string(id.Author), int64(id.Timestamp))
	return err
}

// forgetChat removes all entries of a chat.
func (x *msgIndex) forgetChat(ctx context.Context, chat core.ChatID) error {
	_, err := x.db.ExecContext(ctx, `DELETE FROM warte_be_msgindex WHERE chat_id = ?`, string(chat))
	return err
}

// kv stores small backend state in store's settings table under a
// "backend." prefix.
type kv struct{ db *store.DB }

func (k kv) get(ctx context.Context, key string, out any) (bool, error) {
	err := k.db.GetSetting(ctx, "backend."+key, out)
	if errors.Is(err, core.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (k kv) set(ctx context.Context, key string, v any) error {
	return k.db.SetSetting(ctx, "backend."+key, v)
}

func (k kv) del(ctx context.Context, key string) error {
	return k.db.DeleteSetting(ctx, "backend."+key)
}
