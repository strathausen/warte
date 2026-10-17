package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	sqlite3 "github.com/mutecomm/go-sqlcipher/v4"
	"go.mau.fi/util/dbutil"

	"warte/internal/core"
)

// DriverName is the database/sql driver name registered by go-sqlcipher. Use
// it as the dialect when wrapping RawDB for go.mau.fi/util/dbutil.
const DriverName = "sqlite3"

// KeySize is the required length of database and blob keys in bytes.
const KeySize = 32

var (
	// ErrNotFound is returned (wrapped) when a record does not exist. It is
	// core.ErrNotFound so callers can pass it straight through the Service.
	ErrNotFound = core.ErrNotFound
	// ErrWrongKey is returned by Open when the database exists but cannot be
	// decrypted with the supplied key.
	ErrWrongKey = errors.New("store: wrong database key or not a database")
	// ErrNewerSchema is returned by Open when the database was written by a
	// newer warte with a schema this build does not know.
	ErrNewerSchema = errors.New("store: database schema is newer than this build")
	// ErrFTS5Unavailable is returned by Open when the search index was built
	// with FTS5 but this binary was compiled without the sqlite_fts5 tag.
	ErrFTS5Unavailable = errors.New("store: database uses FTS5 but this build lacks it (build with -tags sqlite_fts5)")
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// DB is warte's app database. It is safe for concurrent use.
type DB struct {
	sql  *sql.DB
	path string
	now  func() time.Time
	fts  ftsModule

	// writeMu serialises warte's own write transactions inside this process
	// so they queue in Go instead of spinning in SQLite's busy handler.
	writeMu sync.Mutex

	dbutilOnce sync.Once
	dbutilDB   *dbutil.Database
	dbutilErr  error
}

// Option configures Open.
type Option func(*DB)

// WithClock overrides the time source (used for unread/expiry bookkeeping
// and record timestamps). Intended for tests.
func WithClock(now func() time.Time) Option {
	return func(db *DB) { db.now = now }
}

// Open opens (creating if needed) the encrypted database at path with the
// 32-byte SQLCipher key, applies pending migrations and prepares the search
// index. The file and its WAL/SHM siblings are created with mode 0600 and the
// parent directory with 0700.
func Open(path string, key []byte, opts ...Option) (*DB, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("store: database key must be %d bytes, got %d", KeySize, len(key))
	}
	// The driver treats everything after the first '?' as DSN options (and
	// "file:" as a URI), so such paths would silently open the wrong file.
	if path == "" || strings.Contains(path, "?") || strings.HasPrefix(path, "file:") || path == ":memory:" {
		return nil, fmt.Errorf("store: unsupported database path %q", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("store: create data dir: %w", err)
	}
	// Pre-create the file so SQLite (which copies the main file's mode for
	// -wal/-shm) never creates anything world-readable.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("store: create database file: %w", err)
	}
	_ = f.Close()
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("store: chmod database: %w", err)
	}

	sqldb, err := sql.Open(DriverName, dsn(path, key))
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	sqldb.SetMaxIdleConns(4)
	sqldb.SetConnMaxIdleTime(5 * time.Minute)

	db := &DB{sql: sqldb, path: path, now: time.Now}
	for _, o := range opts {
		o(db)
	}

	ctx := context.Background()
	var n int
	if err := sqldb.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master").Scan(&n); err != nil {
		_ = sqldb.Close()
		if isNotADB(err) {
			return nil, fmt.Errorf("%w: %v", ErrWrongKey, err)
		}
		return nil, fmt.Errorf("store: open: %w", err)
	}
	if err := db.migrate(ctx); err != nil {
		_ = sqldb.Close()
		return nil, err
	}
	if err := db.ensureFTS(ctx); err != nil {
		_ = sqldb.Close()
		return nil, err
	}
	return db, nil
}

func dsn(path string, key []byte) string {
	v := url.Values{}
	// Raw 256-bit key: SQLCipher skips PBKDF2 for x'…' keys.
	v.Set("_pragma_key", "x'"+strings.ToUpper(hex.EncodeToString(key))+"'")
	v.Set("_busy_timeout", "5000")
	v.Set("_journal_mode", "WAL")
	v.Set("_foreign_keys", "1")
	v.Set("_synchronous", "NORMAL")
	v.Set("_secure_delete", "1")
	v.Set("_txlock", "immediate")
	return path + "?" + v.Encode()
}

func isNotADB(err error) bool {
	var se sqlite3.Error
	if errors.As(err, &se) && se.Code == sqlite3.ErrNotADB {
		return true
	}
	return strings.Contains(err.Error(), "file is not a database")
}

// RawDB returns the underlying *sql.DB. It is shared with signalmeow; do not
// close it directly, use DB.Close.
func (db *DB) RawDB() *sql.DB { return db.sql }

// Path returns the database file path.
func (db *DB) Path() string { return db.path }

// FTSModule reports which full-text module backs Search: "fts5" or "fts4".
func (db *DB) FTSModule() string { return string(db.fts) }

// DBUtil wraps RawDB as a go.mau.fi/util/dbutil.Database (dialect SQLite) for
// signalmeow. The same instance is returned on every call. Closing the
// returned Database closes the shared pool, so close only via DB.Close.
func (db *DB) DBUtil() (*dbutil.Database, error) {
	db.dbutilOnce.Do(func() {
		db.dbutilDB, db.dbutilErr = dbutil.NewWithDB(db.sql, DriverName)
	})
	return db.dbutilDB, db.dbutilErr
}

// Close closes the database pool.
func (db *DB) Close() error { return db.sql.Close() }

// SchemaVersion returns the applied warte schema version.
func (db *DB) SchemaVersion(ctx context.Context) (int, error) {
	return schemaVersion(ctx, db.sql)
}

// querier is the subset of *sql.DB / *sql.Tx used by helpers.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// tx runs fn in a write transaction (BEGIN IMMEDIATE via the DSN).
func (db *DB) tx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	db.writeMu.Lock()
	defer db.writeMu.Unlock()
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	return nil
}

type migration struct {
	version int
	name    string
	sql     string
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, e := range entries {
		name := e.Name()
		num, _, ok := strings.Cut(name, "_")
		if !ok || !strings.HasSuffix(name, ".sql") {
			continue
		}
		v, err := strconv.Atoi(num)
		if err != nil {
			return nil, fmt.Errorf("store: bad migration name %q", name)
		}
		b, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return nil, err
		}
		out = append(out, migration{version: v, name: name, sql: string(b)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	for i, m := range out {
		if m.version != i+1 {
			return nil, fmt.Errorf("store: migrations not contiguous at %q", m.name)
		}
	}
	return out, nil
}

// latestSchemaVersion is the schema version this build migrates to.
func latestSchemaVersion() int {
	ms, err := loadMigrations()
	if err != nil {
		return 0
	}
	return len(ms)
}

func schemaVersion(ctx context.Context, q querier) (int, error) {
	if _, err := q.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS warte_version (version INTEGER NOT NULL)`); err != nil {
		return 0, fmt.Errorf("store: version table: %w", err)
	}
	var v int
	err := q.QueryRowContext(ctx, `SELECT version FROM warte_version LIMIT 1`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("store: read version: %w", err)
	}
	return v, nil
}

func (db *DB) migrate(ctx context.Context) error {
	ms, err := loadMigrations()
	if err != nil {
		return err
	}
	return db.tx(ctx, func(tx *sql.Tx) error {
		cur, err := schemaVersion(ctx, tx)
		if err != nil {
			return err
		}
		if cur > len(ms) {
			return fmt.Errorf("%w (have %d, know %d)", ErrNewerSchema, cur, len(ms))
		}
		for _, m := range ms[cur:] {
			if _, err := tx.ExecContext(ctx, m.sql); err != nil {
				return fmt.Errorf("store: migration %s: %w", m.name, err)
			}
		}
		if cur == len(ms) {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM warte_version`); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO warte_version (version) VALUES (?)`, len(ms))
		return err
	})
}

// --- small encoding helpers -------------------------------------------------

func toMs(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMs(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func notFound(what string, key any) error {
	return fmt.Errorf("store: %s %v: %w", what, key, ErrNotFound)
}

// closeRows closes rows and reports any iteration error (rows.Err) or close
// error. Use it where rows are drained manually instead of deferring Close.
func closeRows(rows *sql.Rows) error {
	return errors.Join(rows.Err(), rows.Close())
}

// placeholders returns "?,?,…" with n marks.
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
