package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sahilm/fuzzy"

	"warte/internal/core"
)

// UpsertChat inserts or fully replaces a chat. The derived fields Unread,
// MentionsUnread, LastMessage and Draft are ignored: the store maintains them
// from messages and drafts. For read-modify-write use UpdateChat.
func (db *DB) UpsertChat(ctx context.Context, c core.Chat) error {
	if c.ID == "" {
		return errors.New("store: chat without ID")
	}
	return db.tx(ctx, func(tx *sql.Tx) error { return upsertChat(ctx, tx, c) })
}

func upsertChat(ctx context.Context, q querier, c core.Chat) error {
	c.Unread, c.MentionsUnread, c.LastMessage, c.Draft = 0, 0, nil, ""
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `INSERT INTO warte_chats
		(id, kind, title, archived, pinned, pin_order, marked_unread, muted_until, blocked, message_request, data)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET kind = excluded.kind, title = excluded.title,
			archived = excluded.archived, pinned = excluded.pinned, pin_order = excluded.pin_order,
			marked_unread = excluded.marked_unread, muted_until = excluded.muted_until,
			blocked = excluded.blocked, message_request = excluded.message_request, data = excluded.data`,
		string(c.ID), int(c.Kind), c.Title, b2i(c.Archived), b2i(c.Pinned), c.PinOrder,
		b2i(c.MarkedUnread), toMs(c.MutedUntil), b2i(c.Blocked), b2i(c.MessageRequest), data)
	if err != nil {
		return fmt.Errorf("store: upsert chat: %w", err)
	}
	return nil
}

// ensureChat creates a minimal chat row for id if none exists, so messages
// and drafts can reference it. Direct chats with the self contact become
// Note to Self.
func ensureChat(ctx context.Context, q querier, id core.ChatID) error {
	var one int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM warte_chats WHERE id = ?`, string(id)).Scan(&one)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	c := core.Chat{ID: id, Kind: core.ChatDirect}
	switch {
	case id.IsGroup():
		c.Kind = core.ChatGroup
	case id.IsDirect():
		var self int
		err := q.QueryRowContext(ctx, `SELECT is_self FROM warte_contacts WHERE aci = ?`, string(id.ACI())).Scan(&self)
		if err == nil && self == 1 {
			c.Kind = core.ChatNoteToSelf
			c.Title = "Note to Self"
		}
	default:
		return fmt.Errorf("store: invalid chat id %q", id)
	}
	return upsertChat(ctx, q, c)
}

const chatSelect = `SELECT c.data, c.unread, c.mentions_unread, c.last_preview, d.body, ct.data, la.display_name
	FROM warte_chats c
	LEFT JOIN warte_drafts d ON d.chat_id = c.id
	LEFT JOIN warte_contacts ct ON c.kind <> 1 AND substr(c.id, 1, 2) = 'u:' AND ct.aci = substr(c.id, 3)
	LEFT JOIN warte_contacts la ON la.aci = c.last_author `

func scanChat(sc interface{ Scan(...any) error }) (core.Chat, error) {
	var (
		data, preview, contact []byte
		unread, mentions       int
		draft, lastName        sql.NullString
	)
	if err := sc.Scan(&data, &unread, &mentions, &preview, &draft, &contact, &lastName); err != nil {
		return core.Chat{}, err
	}
	var c core.Chat
	if err := json.Unmarshal(data, &c); err != nil {
		return core.Chat{}, fmt.Errorf("store: decode chat: %w", err)
	}
	c.Unread, c.MentionsUnread, c.Draft = unread, mentions, draft.String
	if len(preview) > 0 {
		var p core.MessagePreview
		if err := json.Unmarshal(preview, &p); err != nil {
			return core.Chat{}, fmt.Errorf("store: decode preview: %w", err)
		}
		if !p.Outgoing && lastName.String != "" && lastName.String != "Unknown" {
			p.Sender = lastName.String
		}
		c.LastMessage = &p
	}
	if len(contact) > 0 {
		var ct core.Contact
		if err := json.Unmarshal(contact, &ct); err != nil {
			return core.Chat{}, fmt.Errorf("store: decode contact: %w", err)
		}
		c.Contact = &ct
		if c.Kind == core.ChatDirect {
			if n := ct.DisplayName(); n != "" && (n != "Unknown" || c.Title == "") {
				c.Title = n
			}
		}
		if c.Kind == core.ChatDirect || c.Kind == core.ChatNoteToSelf {
			// A direct chat shows its contact's avatar (the frontend reads
			// Chat.AvatarID for every chat).
			if ct.AvatarID != "" || c.AvatarID == "" {
				c.AvatarID = ct.AvatarID
			}
		}
	}
	return c, nil
}

// GetChat returns one chat with derived fields filled in.
func (db *DB) GetChat(ctx context.Context, id core.ChatID) (core.Chat, error) {
	return getChat(ctx, db.sql, id)
}

func getChat(ctx context.Context, q querier, id core.ChatID) (core.Chat, error) {
	c, err := scanChat(q.QueryRowContext(ctx, chatSelect+`WHERE c.id = ?`, string(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return core.Chat{}, notFound("chat", id)
	}
	if err != nil {
		return core.Chat{}, fmt.Errorf("store: get chat: %w", err)
	}
	return c, nil
}

// ListChats returns chats matching f, ordered as core.Service.Chats
// documents: pinned chats first by PinOrder, then by last activity (newest
// first), then title. f.Query fuzzy-matches the title, username and phone
// number but does not reorder results. f.FolderID applies the folder's
// filter on top of f (restrictive flags combine; Archived and the Include
// flags come from f); an unknown folder yields ErrNotFound. Chats with
// neither messages nor a draft are only included with f.IncludeEmpty.
func (db *DB) ListChats(ctx context.Context, f core.ChatFilter) ([]core.Chat, error) {
	queries := []string{f.Query}
	var members []*core.FolderMembers
	if f.Members != nil {
		members = append(members, f.Members)
	}
	if f.FolderID != "" {
		folder, err := db.GetFolder(ctx, f.FolderID)
		if err != nil {
			return nil, err
		}
		ff := folder.Filter
		if ff.Members != nil {
			members = append(members, ff.Members)
		}
		f.UnreadOnly = f.UnreadOnly || ff.UnreadOnly
		f.GroupsOnly = f.GroupsOnly || ff.GroupsOnly
		f.DirectOnly = f.DirectOnly || ff.DirectOnly
		queries = append(queries, ff.Query)
	}
	where := []string{"c.archived = ?"}
	args := []any{b2i(f.Archived)}
	if f.UnreadOnly {
		where = append(where, "(c.unread > 0 OR c.marked_unread = 1)")
	}
	if f.GroupsOnly {
		where = append(where, "c.kind = ?")
		args = append(args, int(core.ChatGroup))
	}
	if f.DirectOnly {
		where = append(where, "c.kind <> ?")
		args = append(args, int(core.ChatGroup))
	}
	if !f.IncludeBlocked {
		where = append(where, "c.blocked = 0")
	}
	if !f.IncludeEmpty {
		where = append(where, "(c.last_preview IS NOT NULL OR d.body IS NOT NULL)")
	}
	q := chatSelect + `WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY c.pinned DESC, CASE WHEN c.pinned = 1 THEN c.pin_order END, c.last_activity DESC, c.title COLLATE NOCASE, c.id`
	rows, err := db.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list chats: %w", err)
	}
	defer rows.Close()
	var out []core.Chat
	for rows.Next() {
		c, err := scanChat(rows)
		if err != nil {
			return nil, err
		}
		if !matchesAll(c, queries) || !inFolders(c, members, db.now()) {
			continue
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// inFolders reports whether c matches every folder membership rule.
func inFolders(c core.Chat, members []*core.FolderMembers, now time.Time) bool {
	for _, m := range members {
		if !m.Match(c, now) {
			return false
		}
	}
	return true
}

func matchesAll(c core.Chat, queries []string) bool {
	for _, q := range queries {
		q = normalizeQuery(q)
		if q == "" {
			continue
		}
		cands := []string{strings.ToLower(c.Title)}
		if c.Contact != nil {
			cands = append(cands, strings.ToLower(c.Contact.Username), c.Contact.E164,
				strings.ToLower(c.Contact.ProfileName), strings.ToLower(c.Contact.ContactName))
		}
		if len(fuzzy.Find(q, cands)) == 0 {
			return false
		}
	}
	return true
}

// DeleteChat removes a chat with its messages, receipts and draft. The
// search index is updated by triggers. Attachment rows owned by its messages
// become orphans; reclaim them with PruneAttachments.
func (db *DB) DeleteChat(ctx context.Context, id core.ChatID) error {
	return db.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM warte_chats WHERE id = ?`, string(id))
		if err != nil {
			return fmt.Errorf("store: delete chat: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return notFound("chat", id)
		}
		return nil
	})
}

// UpdateChat runs fn on the stored chat inside a transaction and saves the
// result. fn must not change ID. Derived fields set by fn are ignored.
func (db *DB) UpdateChat(ctx context.Context, id core.ChatID, fn func(*core.Chat) error) (core.Chat, error) {
	var out core.Chat
	err := db.tx(ctx, func(tx *sql.Tx) error {
		c, err := getChat(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := fn(&c); err != nil {
			return err
		}
		if c.ID != id {
			return fmt.Errorf("store: UpdateChat may not change the chat ID")
		}
		if err := upsertChat(ctx, tx, c); err != nil {
			return err
		}
		out, err = getChat(ctx, tx, id)
		return err
	})
	return out, err
}

// ApplyChatUpdate applies a core.ChatUpdate. Pinning appends the chat to the
// end of the pinned list; archiving a pinned chat unpins it.
func (db *DB) ApplyChatUpdate(ctx context.Context, id core.ChatID, u core.ChatUpdate) (core.Chat, error) {
	var out core.Chat
	err := db.tx(ctx, func(tx *sql.Tx) error {
		c, err := getChat(ctx, tx, id)
		if err != nil {
			return err
		}
		if u.Pinned != nil && *u.Pinned != c.Pinned {
			c.Pinned = *u.Pinned
			c.PinOrder = 0
			if c.Pinned {
				var maxOrder sql.NullInt64
				if err := tx.QueryRowContext(ctx, `SELECT max(pin_order) FROM warte_chats WHERE pinned = 1`).Scan(&maxOrder); err != nil {
					return err
				}
				c.PinOrder = int(maxOrder.Int64) + 1
				c.Archived = false
			}
		}
		if u.Archived != nil {
			c.Archived = *u.Archived
			if c.Archived && c.Pinned {
				c.Pinned, c.PinOrder = false, 0
			}
		}
		if u.MarkedUnread != nil {
			c.MarkedUnread = *u.MarkedUnread
		}
		if u.MutedUntil != nil {
			c.MutedUntil = *u.MutedUntil
		}
		if u.ExpireTimer != nil {
			c.ExpireTimer = *u.ExpireTimer
		}
		if err := upsertChat(ctx, tx, c); err != nil {
			return err
		}
		out, err = getChat(ctx, tx, id)
		return err
	})
	return out, err
}

// SetDraft stores the composer draft for chat; an empty draft deletes it.
// The chat row is created if missing.
func (db *DB) SetDraft(ctx context.Context, chat core.ChatID, draft string) error {
	return db.tx(ctx, func(tx *sql.Tx) error {
		if draft == "" {
			_, err := tx.ExecContext(ctx, `DELETE FROM warte_drafts WHERE chat_id = ?`, string(chat))
			return err
		}
		if err := ensureChat(ctx, tx, chat); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO warte_drafts (chat_id, body, updated_at) VALUES (?, ?, ?)
			ON CONFLICT (chat_id) DO UPDATE SET body = excluded.body, updated_at = excluded.updated_at`,
			string(chat), draft, db.now().UnixMilli())
		return err
	})
}

// GetDraft returns the draft for chat ("" if none).
func (db *DB) GetDraft(ctx context.Context, chat core.ChatID) (string, error) {
	var body string
	err := db.sql.QueryRowContext(ctx, `SELECT body FROM warte_drafts WHERE chat_id = ?`, string(chat)).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return body, err
}

// CountUnread returns the unread and unread-mention counts of a chat.
func (db *DB) CountUnread(ctx context.Context, chat core.ChatID) (unread, mentions int, err error) {
	err = db.sql.QueryRowContext(ctx, `SELECT unread, mentions_unread FROM warte_chats WHERE id = ?`, string(chat)).Scan(&unread, &mentions)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, notFound("chat", chat)
	}
	return unread, mentions, err
}

// TotalUnread sums unread messages over non-archived, non-blocked chats
// (for the terminal title / badge). Muted chats count only if includeMuted.
func (db *DB) TotalUnread(ctx context.Context, includeMuted bool) (int, error) {
	q := `SELECT coalesce(sum(unread), 0) FROM warte_chats WHERE archived = 0 AND blocked = 0`
	args := []any{}
	if !includeMuted {
		q += ` AND muted_until <= ?`
		args = append(args, db.now().UnixMilli())
	}
	var n int
	err := db.sql.QueryRowContext(ctx, q, args...).Scan(&n)
	return n, err
}

// refreshChat recomputes a chat's unread counters and last-message preview.
func refreshChat(ctx context.Context, q querier, chat core.ChatID) error {
	if _, err := q.ExecContext(ctx, `UPDATE warte_chats SET
		unread = (SELECT count(*) FROM warte_messages WHERE chat_id = ?1 AND unread = 1),
		mentions_unread = (SELECT count(*) FROM warte_messages WHERE chat_id = ?1 AND unread = 1 AND mentions_self = 1)
		WHERE id = ?1`, string(chat)); err != nil {
		return fmt.Errorf("store: refresh unread: %w", err)
	}
	var data []byte
	err := q.QueryRowContext(ctx, `SELECT data FROM warte_messages WHERE chat_id = ?
		ORDER BY sent_at DESC, author DESC, ts DESC LIMIT 1`, string(chat)).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = q.ExecContext(ctx, `UPDATE warte_chats SET last_preview = NULL, last_author = '', last_activity = 0 WHERE id = ?`, string(chat))
		return err
	}
	if err != nil {
		return fmt.Errorf("store: refresh preview: %w", err)
	}
	var m core.Message
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("store: decode message: %w", err)
	}
	p := Preview(m, nameFunc(ctx, q))
	pj, err := json.Marshal(p)
	if err != nil {
		return err
	}
	// last_author only feeds the preview's sender name (scanChat); leave it
	// empty when the preview has no sender (system and call messages).
	author := ""
	if previewHasSender(m) {
		author = string(m.ID.Author)
	}
	_, err = q.ExecContext(ctx, `UPDATE warte_chats SET last_preview = ?, last_author = ?, last_activity = ? WHERE id = ?`,
		pj, author, toMs(m.SentAt), string(chat))
	return err
}
