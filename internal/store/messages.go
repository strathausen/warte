package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"time"

	"warte/internal/core"
)

// DefaultPageSize is used when a limit <= 0 is passed to list methods.
const DefaultPageSize = 50

// PutOption tweaks PutMessage.
type PutOption func(*putOptions)

type putOptions struct{ read bool }

// AsRead stores the message as already read: it does not count towards the
// chat's unread counter (use for history sync or messages read elsewhere).
func AsRead() PutOption { return func(o *putOptions) { o.read = true } }

// PutMessage inserts m, or replaces the stored message with the same ID,
// keeping the search index and the chat's unread counters and preview in
// sync. A new incoming message counts as unread unless AsRead is given; a
// replaced message keeps its read state. Per-recipient receipts already
// recorded for the message are folded into Status/PerRecipient; everything
// else (Reactions, EditHistory, …) is replaced as given, so for partial
// changes of a stored message use UpdateMessage or SetReaction. The chat row
// is created if missing. created reports whether the message was new.
func (db *DB) PutMessage(ctx context.Context, m core.Message, opts ...PutOption) (created bool, err error) {
	var o putOptions
	for _, fn := range opts {
		fn(&o)
	}
	if err := validateID(m.ID); err != nil {
		return false, err
	}
	if m.SentAt.IsZero() {
		m.SentAt = time.UnixMilli(int64(m.ID.Timestamp)).UTC()
	}
	err = db.tx(ctx, func(tx *sql.Tx) error {
		if err := ensureChat(ctx, tx, m.ID.Chat); err != nil {
			return err
		}
		var oldUnread int
		err := tx.QueryRowContext(ctx, `SELECT unread FROM warte_messages WHERE chat_id = ? AND author = ? AND ts = ?`,
			idArgs(m.ID)...).Scan(&oldUnread)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			created = true
		case err != nil:
			return fmt.Errorf("store: put message: %w", err)
		}
		unread := oldUnread == 1
		if created {
			unread = countsAsUnread(m)
		} else if err := foldReceipts(ctx, tx, &m); err != nil {
			return err
		}
		if o.read {
			unread = false
		}
		if err := writeMessage(ctx, tx, m, unread); err != nil {
			return err
		}
		return refreshChat(ctx, tx, m.ID.Chat)
	})
	return created, err
}

func validateID(id core.MessageID) error {
	if id.Chat == "" || id.IsZero() || id.Timestamp > math.MaxInt64 {
		return fmt.Errorf("store: invalid message id %q", id)
	}
	if !id.Chat.IsDirect() && !id.Chat.IsGroup() {
		return fmt.Errorf("store: invalid chat id %q", id.Chat)
	}
	return nil
}

func idArgs(id core.MessageID) []any {
	return []any{string(id.Chat), string(id.Author), int64(id.Timestamp)}
}

// writeMessage upserts the message row. The rowid is stable across updates
// so the search index triggers see UPDATEs, not delete+insert.
func writeMessage(ctx context.Context, q querier, m core.Message, unread bool) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	var storyAuthor string
	var storyTS int64
	if m.StoryContext != nil {
		storyAuthor, storyTS = string(m.StoryContext.Author), int64(m.StoryContext.Timestamp)
	}
	body, flags := indexText(m.Body), mediaFlags(m)
	if m.RemoteDeleted {
		body, flags = "", 0
	}
	_, err = q.ExecContext(ctx, `INSERT INTO warte_messages
		(chat_id, author, ts, sent_at, received_at, kind, outgoing, unread, mentions_self, media_flags,
		 expires_at, story_author, story_ts, deleted, body, data)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (chat_id, author, ts) DO UPDATE SET sent_at = excluded.sent_at,
			received_at = excluded.received_at, kind = excluded.kind, outgoing = excluded.outgoing,
			unread = excluded.unread, mentions_self = excluded.mentions_self,
			media_flags = excluded.media_flags, expires_at = excluded.expires_at,
			story_author = excluded.story_author, story_ts = excluded.story_ts,
			deleted = excluded.deleted, body = excluded.body, data = excluded.data`,
		string(m.ID.Chat), string(m.ID.Author), int64(m.ID.Timestamp), toMs(m.SentAt), toMs(m.ReceivedAt),
		int(m.Kind), b2i(m.Outgoing), b2i(unread), b2i(m.MentionsSelf), flags,
		toMs(m.ExpiresAt()), storyAuthor, storyTS, b2i(m.RemoteDeleted), body, data)
	if err != nil {
		return fmt.Errorf("store: write message: %w", err)
	}
	return nil
}

func decodeMessage(data []byte) (core.Message, error) {
	var m core.Message
	if err := json.Unmarshal(data, &m); err != nil {
		return core.Message{}, fmt.Errorf("store: decode message: %w", err)
	}
	return m, nil
}

func queryMessages(ctx context.Context, q querier, query string, args ...any) ([]core.Message, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: query messages: %w", err)
	}
	defer rows.Close()
	var out []core.Message
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		m, err := decodeMessage(data)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := resolveNames(ctx, q, out); err != nil {
		return nil, err
	}
	return out, nil
}

// getMessage loads one message (names resolved).
func getMessage(ctx context.Context, q querier, id core.MessageID) (core.Message, error) {
	ms, err := queryMessages(ctx, q, `SELECT data FROM warte_messages WHERE chat_id = ? AND author = ? AND ts = ?`, idArgs(id)...)
	if err != nil {
		return core.Message{}, err
	}
	if len(ms) == 0 {
		return core.Message{}, notFound("message", id)
	}
	return ms[0], nil
}

// GetMessage returns one message.
func (db *DB) GetMessage(ctx context.Context, id core.MessageID) (core.Message, error) {
	return getMessage(ctx, db.sql, id)
}

// sortKey returns the (sent_at, author, ts) ordering key of id. If the
// message is unknown, its timestamp stands in for sent_at.
func sortKey(ctx context.Context, q querier, id core.MessageID) (key []any, found bool, err error) {
	var sentAt int64
	err = q.QueryRowContext(ctx, `SELECT sent_at FROM warte_messages WHERE chat_id = ? AND author = ? AND ts = ?`,
		idArgs(id)...).Scan(&sentAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return []any{int64(id.Timestamp), string(id.Author), int64(id.Timestamp)}, false, nil
	case err != nil:
		return nil, false, err
	}
	return []any{sentAt, string(id.Author), int64(id.Timestamp)}, true, nil
}

// ListMessages returns up to limit messages of chat older than before (zero
// = newest), sorted oldest → newest by (SentAt, author, timestamp).
func (db *DB) ListMessages(ctx context.Context, chat core.ChatID, before core.MessageID, limit int) ([]core.Message, error) {
	if limit <= 0 {
		limit = DefaultPageSize
	}
	q := `SELECT data FROM warte_messages WHERE chat_id = ?`
	args := []any{string(chat)}
	if !before.IsZero() {
		before.Chat = chat
		key, _, err := sortKey(ctx, db.sql, before)
		if err != nil {
			return nil, err
		}
		q += ` AND (sent_at, author, ts) < (?, ?, ?)`
		args = append(args, key...)
	}
	q += ` ORDER BY sent_at DESC, author DESC, ts DESC LIMIT ?`
	args = append(args, limit)
	ms, err := queryMessages(ctx, db.sql, q, args...)
	if err != nil {
		return nil, err
	}
	slices.Reverse(ms)
	return ms, nil
}

// MessagesAround returns up to limit messages centred on id (included),
// oldest → newest. Near either end of the chat the window shifts so that up
// to limit messages are still returned.
func (db *DB) MessagesAround(ctx context.Context, id core.MessageID, limit int) ([]core.Message, error) {
	if limit <= 0 {
		limit = DefaultPageSize
	}
	key, found, err := sortKey(ctx, db.sql, id)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, notFound("message", id)
	}
	chat := string(id.Chat)
	older, err := queryMessages(ctx, db.sql, `SELECT data FROM warte_messages WHERE chat_id = ? AND (sent_at, author, ts) < (?, ?, ?)
		ORDER BY sent_at DESC, author DESC, ts DESC LIMIT ?`, chat, key[0], key[1], key[2], limit-1)
	if err != nil {
		return nil, err
	}
	newer, err := queryMessages(ctx, db.sql, `SELECT data FROM warte_messages WHERE chat_id = ? AND (sent_at, author, ts) > (?, ?, ?)
		ORDER BY sent_at, author, ts LIMIT ?`, chat, key[0], key[1], key[2], limit-1)
	if err != nil {
		return nil, err
	}
	self, err := getMessage(ctx, db.sql, id)
	if err != nil {
		return nil, err
	}
	nOld := (limit - 1) / 2
	nNew := limit - 1 - nOld
	if len(older) < nOld {
		nNew += nOld - len(older)
		nOld = len(older)
	}
	if len(newer) < nNew {
		nOld = min(len(older), nOld+nNew-len(newer))
		nNew = len(newer)
	}
	out := make([]core.Message, 0, nOld+1+nNew)
	for i := nOld - 1; i >= 0; i-- {
		out = append(out, older[i])
	}
	out = append(out, self)
	out = append(out, newer[:nNew]...)
	return out, nil
}

// DeleteMessage removes a message ("delete for me" or expiry) with its
// receipts and search entry, and refreshes the chat. Attachment rows owned
// by it become orphans; reclaim them with PruneAttachments.
func (db *DB) DeleteMessage(ctx context.Context, id core.MessageID) error {
	return db.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM warte_messages WHERE chat_id = ? AND author = ? AND ts = ?`, idArgs(id)...)
		if err != nil {
			return fmt.Errorf("store: delete message: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return notFound("message", id)
		}
		return refreshChat(ctx, tx, id.Chat)
	})
}

// UpdateMessage loads the message, runs fn on it and saves the result in one
// transaction, returning the stored message. fn must not change the ID. The
// unread state is preserved. If fn returns an error nothing is saved.
func (db *DB) UpdateMessage(ctx context.Context, id core.MessageID, fn func(*core.Message) error) (core.Message, error) {
	var out core.Message
	err := db.tx(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = updateMessage(ctx, tx, id, fn)
		return err
	})
	return out, err
}

func updateMessage(ctx context.Context, tx *sql.Tx, id core.MessageID, fn func(*core.Message) error) (core.Message, error) {
	var unread int
	var data []byte
	err := tx.QueryRowContext(ctx, `SELECT unread, data FROM warte_messages WHERE chat_id = ? AND author = ? AND ts = ?`,
		idArgs(id)...).Scan(&unread, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return core.Message{}, notFound("message", id)
	}
	if err != nil {
		return core.Message{}, fmt.Errorf("store: load message: %w", err)
	}
	m, err := decodeMessage(data)
	if err != nil {
		return core.Message{}, err
	}
	if err := fn(&m); err != nil {
		return core.Message{}, err
	}
	if m.ID != id {
		return core.Message{}, errors.New("store: UpdateMessage may not change the message ID")
	}
	if err := writeMessage(ctx, tx, m, unread == 1); err != nil {
		return core.Message{}, err
	}
	if err := refreshChat(ctx, tx, id.Chat); err != nil {
		return core.Message{}, err
	}
	return getMessage(ctx, tx, id)
}

// SetReaction records r on message id with Signal's semantics: each sender
// has at most one reaction per message, so a new reaction from r.From
// replaces the previous one. An empty r.Emoji removes r.From's reaction.
// Reactions are kept sorted by time. Returns the updated message.
func (db *DB) SetReaction(ctx context.Context, id core.MessageID, r core.Reaction) (core.Message, error) {
	if r.From == "" {
		return core.Message{}, errors.New("store: reaction without sender")
	}
	if r.At.IsZero() {
		r.At = db.now().UTC()
	}
	return db.UpdateMessage(ctx, id, func(m *core.Message) error {
		m.Reactions = slices.DeleteFunc(m.Reactions, func(x core.Reaction) bool { return x.From == r.From })
		if r.Emoji != "" {
			m.Reactions = append(m.Reactions, r)
			sort.SliceStable(m.Reactions, func(i, j int) bool { return m.Reactions[i].At.Before(m.Reactions[j].At) })
		}
		if len(m.Reactions) == 0 {
			m.Reactions = nil
		}
		return nil
	})
}

// RemoveReaction removes from's reaction on id (no-op if none).
func (db *DB) RemoveReaction(ctx context.Context, id core.MessageID, from core.ACI) (core.Message, error) {
	return db.SetReaction(ctx, id, core.Reaction{From: from})
}

// MergeStatus combines a recorded delivery status with a newly reported one:
// statuses only move forward (Sent < Delivered < Read < Viewed), a failure
// never overrides a successful send, and a success clears a failure.
func MergeStatus(old, upd core.DeliveryStatus) core.DeliveryStatus {
	switch {
	case upd == core.StatusFailed:
		if old >= core.StatusSent && old != core.StatusFailed {
			return old
		}
		return core.StatusFailed
	case old == core.StatusFailed:
		return upd
	}
	return max(old, upd)
}

// AggregateStatus derives a message's status from per-recipient statuses
// like Signal Desktop: the highest successful status of any recipient, or
// Failed if every recipient failed. With no recipients it returns StatusNone.
func AggregateStatus(per map[core.ACI]core.DeliveryStatus) core.DeliveryStatus {
	best := core.StatusNone
	failed := 0
	for _, s := range per {
		if s == core.StatusFailed {
			failed++
			continue
		}
		best = max(best, s)
	}
	if len(per) > 0 && failed == len(per) {
		return core.StatusFailed
	}
	return best
}

// SetReceipt records that recipient reached status for message id (a
// delivery/read/viewed receipt, or a per-recipient send result) and updates
// the message's aggregate Status (and PerRecipient for group chats).
// Statuses never regress; see MergeStatus. Returns the updated message.
func (db *DB) SetReceipt(ctx context.Context, id core.MessageID, recipient core.ACI, status core.DeliveryStatus) (core.Message, error) {
	if recipient == "" {
		return core.Message{}, errors.New("store: receipt without recipient")
	}
	var out core.Message
	err := db.tx(ctx, func(tx *sql.Tx) error {
		var old core.DeliveryStatus
		err := tx.QueryRowContext(ctx, `SELECT status FROM warte_receipts WHERE chat_id = ? AND author = ? AND ts = ? AND recipient = ?`,
			append(idArgs(id), string(recipient))...).Scan(&old)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("store: load receipt: %w", err)
		}
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM warte_messages WHERE chat_id = ? AND author = ? AND ts = ?`,
			idArgs(id)...).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return notFound("message", id)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO warte_receipts (chat_id, author, ts, recipient, status, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (chat_id, author, ts, recipient) DO UPDATE SET status = excluded.status, updated_at = excluded.updated_at`,
			append(idArgs(id), string(recipient), int(MergeStatus(old, status)), db.now().UnixMilli())...); err != nil {
			return fmt.Errorf("store: save receipt: %w", err)
		}
		out, err = updateMessage(ctx, tx, id, func(m *core.Message) error { return foldReceipts(ctx, tx, m) })
		return err
	})
	return out, err
}

// Receipts returns the per-recipient statuses recorded for a message.
func (db *DB) Receipts(ctx context.Context, id core.MessageID) (map[core.ACI]core.DeliveryStatus, error) {
	return loadReceipts(ctx, db.sql, id)
}

func loadReceipts(ctx context.Context, q querier, id core.MessageID) (map[core.ACI]core.DeliveryStatus, error) {
	rows, err := q.QueryContext(ctx, `SELECT recipient, status FROM warte_receipts WHERE chat_id = ? AND author = ? AND ts = ?`, idArgs(id)...)
	if err != nil {
		return nil, fmt.Errorf("store: load receipts: %w", err)
	}
	defer rows.Close()
	out := map[core.ACI]core.DeliveryStatus{}
	for rows.Next() {
		var r string
		var s int
		if err := rows.Scan(&r, &s); err != nil {
			return nil, err
		}
		out[core.ACI(r)] = core.DeliveryStatus(s)
	}
	return out, rows.Err()
}

// foldReceipts applies recorded receipts to m.Status and (groups only)
// m.PerRecipient. It leaves m untouched when no receipts exist.
func foldReceipts(ctx context.Context, q querier, m *core.Message) error {
	per, err := loadReceipts(ctx, q, m.ID)
	if err != nil || len(per) == 0 {
		return err
	}
	m.Status = MergeStatus(m.Status, AggregateStatus(per))
	if m.ID.Chat.IsGroup() {
		m.PerRecipient = per
	}
	return nil
}

// MarkRead marks incoming unread messages of chat up to and including upTo
// (zero = all) as read, starts disappearing-message timers that were waiting
// for the read, clears the chat's MarkedUnread flag and refreshes its
// counters. It returns the IDs that changed, oldest → newest, so the caller
// can send read receipts.
func (db *DB) MarkRead(ctx context.Context, chat core.ChatID, upTo core.MessageID) ([]core.MessageID, error) {
	var ids []core.MessageID
	err := db.tx(ctx, func(tx *sql.Tx) error {
		q := `SELECT rowid, data FROM warte_messages WHERE chat_id = ? AND unread = 1`
		args := []any{string(chat)}
		if !upTo.IsZero() {
			upTo.Chat = chat
			key, _, err := sortKey(ctx, tx, upTo)
			if err != nil {
				return err
			}
			q += ` AND (sent_at, author, ts) <= (?, ?, ?)`
			args = append(args, key...)
		}
		q += ` ORDER BY sent_at, author, ts`
		type row struct {
			rowid int64
			m     core.Message
		}
		var rs []row
		rows, err := tx.QueryContext(ctx, q, args...)
		if err != nil {
			return fmt.Errorf("store: mark read: %w", err)
		}
		for rows.Next() {
			var r row
			var data []byte
			if err := rows.Scan(&r.rowid, &data); err != nil {
				rows.Close()
				return err
			}
			if r.m, err = decodeMessage(data); err != nil {
				rows.Close()
				return err
			}
			rs = append(rs, r)
		}
		if err := closeRows(rows); err != nil {
			return err
		}
		now := db.now().UTC()
		for _, r := range rs {
			ids = append(ids, r.m.ID)
			if r.m.ExpiresIn > 0 && r.m.ExpireStartedAt.IsZero() {
				r.m.ExpireStartedAt = now
				if err := writeMessage(ctx, tx, r.m, false); err != nil {
					return err
				}
				continue
			}
			if _, err := tx.ExecContext(ctx, `UPDATE warte_messages SET unread = 0 WHERE rowid = ?`, r.rowid); err != nil {
				return err
			}
		}
		if err := clearMarkedUnread(ctx, tx, chat); err != nil {
			return err
		}
		return refreshChat(ctx, tx, chat)
	})
	return ids, err
}

func clearMarkedUnread(ctx context.Context, q querier, chat core.ChatID) error {
	var data []byte
	err := q.QueryRowContext(ctx, `SELECT data FROM warte_chats WHERE id = ? AND marked_unread = 1`, string(chat)).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var c core.Chat
	if err := json.Unmarshal(data, &c); err != nil {
		return fmt.Errorf("store: decode chat: %w", err)
	}
	c.MarkedUnread = false
	return upsertChat(ctx, q, c)
}

// ExpiringMessages returns IDs of disappearing messages whose timer ran out
// at or before t, soonest first. The caller deletes them (DeleteMessage).
func (db *DB) ExpiringMessages(ctx context.Context, t time.Time) ([]core.MessageID, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT chat_id, author, ts FROM warte_messages
		WHERE expires_at > 0 AND expires_at <= ? ORDER BY expires_at`, t.UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("store: expiring messages: %w", err)
	}
	defer rows.Close()
	var out []core.MessageID
	for rows.Next() {
		var chat, author string
		var ts int64
		if err := rows.Scan(&chat, &author, &ts); err != nil {
			return nil, err
		}
		out = append(out, core.MessageID{Chat: core.ChatID(chat), Author: core.ACI(author), Timestamp: uint64(ts)})
	}
	return out, rows.Err()
}

// NextExpiration returns when the next disappearing message expires; ok is
// false if no timer is running. Use it to schedule the expiry sweep.
func (db *DB) NextExpiration(ctx context.Context) (t time.Time, ok bool, err error) {
	var ms sql.NullInt64
	if err := db.sql.QueryRowContext(ctx, `SELECT min(expires_at) FROM warte_messages WHERE expires_at > 0`).Scan(&ms); err != nil {
		return time.Time{}, false, err
	}
	if !ms.Valid {
		return time.Time{}, false, nil
	}
	return fromMs(ms.Int64), true, nil
}
