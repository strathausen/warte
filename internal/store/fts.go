package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// ftsModule names the SQLite full-text module backing warte_fts.
type ftsModule string

const (
	fts5 ftsModule = "fts5"
	fts4 ftsModule = "fts4"
)

// Snippet markers wrap matched terms in snippet() output; they are parsed
// out into SearchHit.Matches and never reach callers.
const (
	markStart = "\x02"
	markEnd   = "\x03"
)

const ftsMetaKey = "fts_module"

var ftsDDL = map[ftsModule][]string{
	fts5: {
		`CREATE VIRTUAL TABLE warte_fts USING fts5(body, content='warte_messages', content_rowid='rowid', tokenize='unicode61 remove_diacritics 2')`,
		`CREATE TRIGGER warte_fts_ai AFTER INSERT ON warte_messages BEGIN
			INSERT INTO warte_fts (rowid, body) VALUES (new.rowid, new.body);
		END`,
		`CREATE TRIGGER warte_fts_ad AFTER DELETE ON warte_messages BEGIN
			INSERT INTO warte_fts (warte_fts, rowid, body) VALUES ('delete', old.rowid, old.body);
		END`,
		`CREATE TRIGGER warte_fts_au AFTER UPDATE OF body ON warte_messages WHEN old.body IS NOT new.body BEGIN
			INSERT INTO warte_fts (warte_fts, rowid, body) VALUES ('delete', old.rowid, old.body);
			INSERT INTO warte_fts (rowid, body) VALUES (new.rowid, new.body);
		END`,
		`INSERT INTO warte_fts (warte_fts) VALUES ('rebuild')`,
	},
	fts4: {
		`CREATE VIRTUAL TABLE warte_fts USING fts4(content="warte_messages", body, tokenize=unicode61 "remove_diacritics=2")`,
		`CREATE TRIGGER warte_fts_bu BEFORE UPDATE OF body ON warte_messages WHEN old.body IS NOT new.body BEGIN
			DELETE FROM warte_fts WHERE docid = old.rowid;
		END`,
		`CREATE TRIGGER warte_fts_bd BEFORE DELETE ON warte_messages BEGIN
			DELETE FROM warte_fts WHERE docid = old.rowid;
		END`,
		`CREATE TRIGGER warte_fts_au AFTER UPDATE OF body ON warte_messages WHEN old.body IS NOT new.body BEGIN
			INSERT INTO warte_fts (docid, body) VALUES (new.rowid, new.body);
		END`,
		`CREATE TRIGGER warte_fts_ai AFTER INSERT ON warte_messages BEGIN
			INSERT INTO warte_fts (docid, body) VALUES (new.rowid, new.body);
		END`,
		`INSERT INTO warte_fts (warte_fts) VALUES ('rebuild')`,
	},
}

var ftsTriggers = []string{"warte_fts_ai", "warte_fts_ad", "warte_fts_au", "warte_fts_bu", "warte_fts_bd"}

// fts5Compiled reports whether the linked SQLite has FTS5.
func fts5Compiled(ctx context.Context, q querier) (bool, error) {
	var used int
	if err := q.QueryRowContext(ctx, `SELECT sqlite_compileoption_used('ENABLE_FTS5')`).Scan(&used); err != nil {
		return false, err
	}
	return used == 1, nil
}

// ensureFTS creates the search index with the best available module, or
// upgrades an FTS4 index to FTS5 when this build supports it.
func (db *DB) ensureFTS(ctx context.Context) error {
	return db.tx(ctx, func(tx *sql.Tx) error {
		has5, err := fts5Compiled(ctx, tx)
		if err != nil {
			return fmt.Errorf("store: detect fts5: %w", err)
		}
		want := fts4
		if has5 {
			want = fts5
		}
		var cur string
		err = tx.QueryRowContext(ctx, `SELECT value FROM warte_meta WHERE key = ?`, ftsMetaKey).Scan(&cur)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("store: read fts module: %w", err)
		}
		switch {
		case ftsModule(cur) == want:
			db.fts = want
			return nil
		case ftsModule(cur) == fts5 && !has5:
			return ErrFTS5Unavailable
		}
		// cur is "" (fresh) or fts4 while fts5 is available: (re)build.
		for _, t := range ftsTriggers {
			if _, err := tx.ExecContext(ctx, `DROP TRIGGER IF EXISTS `+t); err != nil {
				return fmt.Errorf("store: drop fts trigger: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS warte_fts`); err != nil {
			return fmt.Errorf("store: drop fts: %w", err)
		}
		for _, stmt := range ftsDDL[want] {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("store: create %s index: %w", want, err)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO warte_meta (key, value) VALUES (?, ?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value`, ftsMetaKey, string(want)); err != nil {
			return err
		}
		db.fts = want
		return nil
	})
}

// ftsQuery turns free user text into a safe MATCH expression: every word
// becomes a quoted prefix term and terms are ANDed. It returns "" when the
// text contains no searchable word.
func ftsQuery(mod ftsModule, text string) string {
	var terms []string
	for _, w := range strings.Fields(text) {
		w = strings.ReplaceAll(w, `"`, "")
		if !strings.ContainsFunc(w, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) {
			continue
		}
		if mod == fts5 {
			terms = append(terms, `"`+w+`"*`)
		} else {
			terms = append(terms, `"`+w+`*"`)
		}
	}
	return strings.Join(terms, " ")
}

// snippetExpr returns the SQL snippet() call for the module.
func snippetExpr(mod ftsModule) string {
	if mod == fts5 {
		return `snippet(warte_fts, 0, char(2), char(3), '…', 16)`
	}
	return `snippet(warte_fts, char(2), char(3), '…', 0, 16)`
}

// parseSnippet strips match markers from a snippet, flattening line breaks to
// spaces, and returns the clean text with [start, end) byte offsets of every
// highlighted match.
func parseSnippet(s string) (string, [][2]int) {
	s = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s)
	var b strings.Builder
	var matches [][2]int
	start := -1
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case markStart[0]:
			start = b.Len()
		case markEnd[0]:
			if start >= 0 && b.Len() > start {
				matches = append(matches, [2]int{start, b.Len()})
			}
			start = -1
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String(), mergeAdjacent(matches)
}

// mergeAdjacent joins matches separated by nothing (FTS4 highlights each
// token of a phrase separately).
func mergeAdjacent(m [][2]int) [][2]int {
	if len(m) < 2 {
		return m
	}
	out := m[:1]
	for _, r := range m[1:] {
		last := &out[len(out)-1]
		if r[0] == last[1] {
			last[1] = r[1]
			continue
		}
		out = append(out, r)
	}
	return out
}
