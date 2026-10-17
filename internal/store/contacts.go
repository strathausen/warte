package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"warte/internal/core"
)

// UpsertContact inserts or replaces a contact keyed by ACI.
func (db *DB) UpsertContact(ctx context.Context, c core.Contact) error {
	if c.ACI == "" {
		return errors.New("store: contact without ACI")
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return db.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO warte_contacts (aci, e164, username, display_name, is_self, data)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (aci) DO UPDATE SET e164 = excluded.e164, username = excluded.username,
				display_name = excluded.display_name, is_self = excluded.is_self, data = excluded.data`,
			string(c.ACI), c.E164, c.Username, c.DisplayName(), b2i(c.IsSelf), data)
		if err != nil {
			return fmt.Errorf("store: upsert contact: %w", err)
		}
		return nil
	})
}

// GetContact returns the contact with the given ACI.
func (db *DB) GetContact(ctx context.Context, aci core.ACI) (core.Contact, error) {
	return db.contactWhere(ctx, `aci = ?`, string(aci))
}

// ContactByE164 finds a contact by phone number ("+4915…").
func (db *DB) ContactByE164(ctx context.Context, e164 string) (core.Contact, error) {
	if e164 == "" {
		return core.Contact{}, notFound("contact", e164)
	}
	return db.contactWhere(ctx, `e164 = ?`, e164)
}

// ContactByUsername finds a contact by Signal username (case-insensitive).
func (db *DB) ContactByUsername(ctx context.Context, username string) (core.Contact, error) {
	if username == "" {
		return core.Contact{}, notFound("contact", username)
	}
	return db.contactWhere(ctx, `username = ? COLLATE NOCASE`, username)
}

// SelfContact returns the contact flagged IsSelf.
func (db *DB) SelfContact(ctx context.Context) (core.Contact, error) {
	return db.contactWhere(ctx, `is_self = 1`)
}

func (db *DB) contactWhere(ctx context.Context, where string, args ...any) (core.Contact, error) {
	var data []byte
	err := db.sql.QueryRowContext(ctx, `SELECT data FROM warte_contacts WHERE `+where+` LIMIT 1`, args...).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return core.Contact{}, notFound("contact", args)
	}
	if err != nil {
		return core.Contact{}, fmt.Errorf("store: get contact: %w", err)
	}
	var c core.Contact
	if err := json.Unmarshal(data, &c); err != nil {
		return core.Contact{}, fmt.Errorf("store: decode contact: %w", err)
	}
	return c, nil
}

// ListContacts returns all contacts sorted by display name (case-insensitive).
func (db *DB) ListContacts(ctx context.Context) ([]core.Contact, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT data FROM warte_contacts ORDER BY display_name COLLATE NOCASE, aci`)
	if err != nil {
		return nil, fmt.Errorf("store: list contacts: %w", err)
	}
	defer rows.Close()
	var out []core.Contact
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var c core.Contact
		if err := json.Unmarshal(data, &c); err != nil {
			return nil, fmt.Errorf("store: decode contact: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteContact removes a contact.
func (db *DB) DeleteContact(ctx context.Context, aci core.ACI) error {
	return db.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM warte_contacts WHERE aci = ?`, string(aci))
		if err != nil {
			return fmt.Errorf("store: delete contact: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return notFound("contact", aci)
		}
		return nil
	})
}

// contactNames loads display names for the given ACIs (unknown or nameless
// ACIs are absent from the result).
func contactNames(ctx context.Context, q querier, acis []core.ACI) (map[core.ACI]string, error) {
	out := map[core.ACI]string{}
	seen := map[core.ACI]bool{}
	var uniq []any
	for _, a := range acis {
		if a == "" || seen[a] {
			continue
		}
		seen[a] = true
		uniq = append(uniq, string(a))
	}
	const chunk = 400
	for len(uniq) > 0 {
		n := min(chunk, len(uniq))
		part := uniq[:n]
		uniq = uniq[n:]
		rows, err := q.QueryContext(ctx, `SELECT aci, display_name FROM warte_contacts WHERE aci IN (`+placeholders(len(part))+`)`, part...)
		if err != nil {
			return nil, fmt.Errorf("store: contact names: %w", err)
		}
		for rows.Next() {
			var aci, name string
			if err := rows.Scan(&aci, &name); err != nil {
				rows.Close()
				return nil, err
			}
			if name != "" && name != "Unknown" {
				out[core.ACI(aci)] = name
			}
		}
		if err := closeRows(rows); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// resolveNames fills SenderName, Reaction.FromName and Quote.AuthorName from
// known contacts.
func resolveNames(ctx context.Context, q querier, msgs []core.Message) error {
	var acis []core.ACI
	for i := range msgs {
		m := &msgs[i]
		acis = append(acis, m.ID.Author)
		for _, r := range m.Reactions {
			acis = append(acis, r.From)
		}
		if m.Quote != nil {
			acis = append(acis, m.Quote.ID.Author)
		}
	}
	names, err := contactNames(ctx, q, acis)
	if err != nil {
		return err
	}
	for i := range msgs {
		m := &msgs[i]
		if n := names[m.ID.Author]; n != "" {
			m.SenderName = n
		}
		for j := range m.Reactions {
			if n := names[m.Reactions[j].From]; n != "" {
				m.Reactions[j].FromName = n
			}
		}
		if m.Quote != nil {
			if n := names[m.Quote.ID.Author]; n != "" {
				m.Quote.AuthorName = n
			}
		}
	}
	return nil
}

// nameFunc returns a NameFunc that looks up contacts lazily within q.
func nameFunc(ctx context.Context, q querier) NameFunc {
	cache := map[core.ACI]string{}
	return func(a core.ACI) string {
		if n, ok := cache[a]; ok {
			return n
		}
		names, err := contactNames(ctx, q, []core.ACI{a})
		if err != nil {
			return ""
		}
		cache[a] = names[a]
		return names[a]
	}
}

func normalizeQuery(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
