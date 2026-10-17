// Package store is warte's persistence layer: chats, contacts, messages,
// receipts, drafts, stories, the call log, sticker packs, folders, settings,
// attachment metadata and a full-text search index, all in one encrypted
// SQLite database, plus an encrypted on-disk blob store for attachment bytes.
//
// # Driver and build tags
//
// The database driver is SQLCipher via github.com/mutecomm/go-sqlcipher/v4,
// which registers itself as database/sql driver "sqlite3" and bundles
// SQLCipher 4 (SQLite 3.33.0). It needs cgo. Only one SQLite driver may be
// linked into a binary: never import github.com/mattn/go-sqlite3 alongside
// this package, because both define the same C symbols and driver name.
//
// Full-text search prefers FTS5, which go-sqlcipher only compiles with the
// build tag sqlite_fts5 (or fts5):
//
//	go build -tags sqlite_fts5 ./cmd/warte
//
// Production builds must use that tag. Without it the store falls back to an
// FTS4 index (always compiled in), so plain "go test" works. The module in
// use is recorded in the database; a database indexed with FTS4 is upgraded
// to FTS5 automatically the first time a binary with FTS5 opens it, while
// opening an FTS5-indexed database with a binary lacking FTS5 fails with
// ErrFTS5Unavailable.
//
// # Sharing the database with signalmeow
//
// Open returns a *DB whose RawDB is the underlying *sql.DB and whose DBUtil
// method wraps it as a go.mau.fi/util/dbutil.Database for signalmeow. All of
// warte's tables are prefixed "warte_" and versioned in "warte_version", so
// they never collide with signalmeow's tables. Connections use WAL, foreign
// keys, a 5 s busy timeout, secure_delete and BEGIN IMMEDIATE transactions.
//
// # Encryption
//
// The database key is a 32-byte raw SQLCipher key (no KDF on open). The blob
// store (Blobs) encrypts each file with AES-256-GCM under a separate 32-byte
// key. Both keys come from LoadOrCreateKeys, which keeps a master secret in
// the OS keychain or derives it from a passphrase with argon2id.
//
// # Conventions
//
//   - Every not-found error wraps core.ErrNotFound.
//   - Timestamps are persisted at millisecond precision in indexed columns
//     and at full precision inside JSON blobs; locations are not preserved
//     (times come back in UTC).
//   - Fields of core.Chat that the store derives (Unread, MentionsUnread,
//     LastMessage, Draft) are ignored on write and computed on read.
//   - Message.SenderName, Reaction.FromName and Quote.AuthorName are
//     re-resolved from the contacts table on read when a contact is known.
//   - Text the store renders itself (MessagePreview.Text, PreviewText,
//     SearchHit.Snippet) is display-safe: control characters (ESC, C1, the
//     snippet markers) become spaces and bidi override/isolate controls are
//     dropped. Stored core values (Message.Body, titles, names) are returned
//     verbatim and must be sanitized by the UI before rendering.
//   - SearchHit.Matches are [start, end) byte offsets into Snippet.
//
// # Shared-database compatibility
//
// signalmeow's own schema upgrades are exercised against this driver in
// TestSignalmeowSchemaOnSharedDB. Note that the bundled SQLite (3.33) has no
// RETURNING clause and JSON1 only with the sqlite_json build tag; SQL in this
// package uses neither.
package store
