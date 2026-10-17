package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"warte/internal/core"
)

// Search finds messages matching q, newest first.
//
// q.Text is split into words; every word must match (as a prefix, case- and
// diacritic-insensitive) in the message body. Quotes and FTS operators in the
// text are treated literally. With empty Text, Search lists messages matching
// only the filters (useful for a media gallery: Chat + HasType). Remote-
// deleted messages are never returned. HasType is one of "image", "video",
// "audio", "file" (other attachments) or "link" (link preview or URL in the
// body). Limit <= 0 means DefaultPageSize.
//
// SearchHit.Snippet is a single-line excerpt of the body; Matches holds
// [start, end) byte offsets into Snippet of each highlighted match. With
// empty Text the snippet is the message's preview text and Matches is nil.
func (db *DB) Search(ctx context.Context, q core.SearchQuery) ([]core.SearchHit, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultPageSize
	}
	var where []string
	var args []any
	var from, snippetCol string
	text := strings.TrimSpace(q.Text)
	if text != "" {
		match := ftsQuery(db.fts, text)
		if match == "" {
			return nil, nil
		}
		from = `warte_fts JOIN warte_messages m ON m.rowid = warte_fts.rowid`
		snippetCol = snippetExpr(db.fts)
		where = append(where, `warte_fts MATCH ?`)
		args = append(args, match)
	} else {
		from = `warte_messages m`
		snippetCol = `''`
	}
	where = append(where, `m.kind <> ?`, `m.deleted = 0`)
	args = append(args, int(core.KindSystem))
	if q.Chat != "" {
		where = append(where, `m.chat_id = ?`)
		args = append(args, string(q.Chat))
	}
	if q.From != "" {
		where = append(where, `m.author = ?`)
		args = append(args, string(q.From))
	}
	if q.HasType != "" {
		flag, ok := hasTypeFlags[q.HasType]
		if !ok {
			return nil, fmt.Errorf("store: unknown search type %q", q.HasType)
		}
		where = append(where, `(m.media_flags & ?) <> 0`)
		args = append(args, flag)
	}
	if !q.Before.IsZero() {
		where = append(where, `m.sent_at < ?`)
		args = append(args, q.Before.UnixMilli())
	}
	if !q.After.IsZero() {
		where = append(where, `m.sent_at > ?`)
		args = append(args, q.After.UnixMilli())
	}
	query := `SELECT m.data, ` + snippetCol + `, coalesce(c.title, '') FROM ` + from + `
		LEFT JOIN warte_chats c ON c.id = m.chat_id
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY m.sent_at DESC, m.author DESC, m.ts DESC LIMIT ?`
	args = append(args, limit)

	rows, err := db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: search: %w", err)
	}
	var hits []core.SearchHit
	var msgs []core.Message
	for rows.Next() {
		var data []byte
		var snip sql.NullString
		var title string
		if err := rows.Scan(&data, &snip, &title); err != nil {
			rows.Close()
			return nil, err
		}
		m, err := decodeMessage(data)
		if err != nil {
			rows.Close()
			return nil, err
		}
		h := core.SearchHit{ChatTitle: title}
		if text != "" {
			h.Snippet, h.Matches = parseSnippet(snip.String)
		}
		hits = append(hits, h)
		msgs = append(msgs, m)
	}
	if err := closeRows(rows); err != nil {
		return nil, fmt.Errorf("store: search: %w", err)
	}
	if err := resolveNames(ctx, db.sql, msgs); err != nil {
		return nil, err
	}
	names := nameFunc(ctx, db.sql)
	titles := map[core.ChatID]string{}
	for i := range hits {
		hits[i].Message = msgs[i]
		if text == "" {
			hits[i].Snippet = PreviewText(msgs[i], names)
		}
		// Direct chat titles follow the contact's current display name.
		id := msgs[i].ID.Chat
		if id.IsDirect() {
			t, ok := titles[id]
			if !ok {
				if c, err := getChat(ctx, db.sql, id); err == nil {
					t = c.Title
				}
				titles[id] = t
			}
			if t != "" {
				hits[i].ChatTitle = t
			}
		}
	}
	return hits, nil
}
