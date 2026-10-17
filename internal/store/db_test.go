package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"warte/internal/core"
)

// testClock is a settable clock for deterministic tests.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

var t0 = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

func randKey(t testing.TB) []byte {
	t.Helper()
	k := make([]byte, KeySize)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

type testEnv struct {
	db    *DB
	clock *testClock
	path  string
	key   []byte
}

func newTestDB(t testing.TB) *testEnv {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "data", "warte.db")
	key := randKey(t)
	clock := &testClock{t: t0}
	db, err := Open(path, key, WithClock(clock.Now))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &testEnv{db: db, clock: clock, path: path, key: key}
}

func ctxT(t testing.TB) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestOpenRejectsBadKey(t *testing.T) {
	for _, n := range []int{0, 16, 31, 33, 64} {
		if _, err := Open(filepath.Join(t.TempDir(), "x.db"), make([]byte, n)); err == nil {
			t.Errorf("key len %d: expected error", n)
		}
	}
}

func TestOpenPermissions(t *testing.T) {
	env := newTestDB(t)
	ctx := ctxT(t)
	if err := env.db.UpsertChat(ctx, core.Chat{ID: "u:a", Title: "A"}); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(env.path)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Mode().Perm(); got != 0o600 {
		t.Errorf("db mode = %o, want 600", got)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if st, err := os.Stat(env.path + suffix); err == nil {
			if got := st.Mode().Perm(); got&0o077 != 0 {
				t.Errorf("%s mode = %o, want no group/other bits", suffix, got)
			}
		}
	}
	dst, err := os.Stat(filepath.Dir(env.path))
	if err != nil {
		t.Fatal(err)
	}
	if got := dst.Mode().Perm(); got != 0o700 {
		t.Errorf("dir mode = %o, want 700", got)
	}
}

func TestMigrationsIdempotent(t *testing.T) {
	env := newTestDB(t)
	ctx := ctxT(t)
	v, err := env.db.SchemaVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v != latestSchemaVersion() || v < 1 {
		t.Fatalf("version = %d, want %d", v, latestSchemaVersion())
	}
	if _, err := env.db.PutMessage(ctx, msg("u:a", "a", 1000, "hello world")); err != nil {
		t.Fatal(err)
	}
	mod := env.db.FTSModule()
	env.db.Close()

	for i := range 3 {
		db, err := Open(env.path, env.key)
		if err != nil {
			t.Fatalf("reopen %d: %v", i, err)
		}
		if v2, _ := db.SchemaVersion(ctx); v2 != v {
			t.Errorf("reopen %d: version %d, want %d", i, v2, v)
		}
		if db.FTSModule() != mod {
			t.Errorf("fts module changed: %s → %s", mod, db.FTSModule())
		}
		hits, err := db.Search(ctx, core.SearchQuery{Text: "hello"})
		if err != nil || len(hits) != 1 {
			t.Errorf("reopen %d: search = %v, %v", i, len(hits), err)
		}
		var n int
		if err := db.RawDB().QueryRow(`SELECT count(*) FROM warte_version`).Scan(&n); err != nil || n != 1 {
			t.Errorf("warte_version rows = %d, %v", n, err)
		}
		db.Close()
	}
}

func TestNewerSchemaRejected(t *testing.T) {
	env := newTestDB(t)
	if _, err := env.db.RawDB().Exec(`UPDATE warte_version SET version = 9999`); err != nil {
		t.Fatal(err)
	}
	env.db.Close()
	if _, err := Open(env.path, env.key); !errors.Is(err, ErrNewerSchema) {
		t.Fatalf("err = %v, want ErrNewerSchema", err)
	}
}

func TestWrongKey(t *testing.T) {
	env := newTestDB(t)
	ctx := ctxT(t)
	if err := env.db.UpsertChat(ctx, core.Chat{ID: "u:a", Title: "A"}); err != nil {
		t.Fatal(err)
	}
	env.db.Close()
	_, err := Open(env.path, randKey(t))
	if !errors.Is(err, ErrWrongKey) {
		t.Fatalf("err = %v, want ErrWrongKey", err)
	}
	db, err := Open(env.path, env.key)
	if err != nil {
		t.Fatalf("right key: %v", err)
	}
	defer db.Close()
	if _, err := db.GetChat(ctx, "u:a"); err != nil {
		t.Fatal(err)
	}
}

func TestDatabaseFileHasNoPlaintext(t *testing.T) {
	env := newTestDB(t)
	ctx := ctxT(t)
	const secret = "correct-horse-battery-staple-plaintext"
	m := msg("u:a", "a", 1000, secret)
	if _, err := env.db.PutMessage(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err := env.db.UpsertContact(ctx, core.Contact{ACI: "a", ProfileName: secret}); err != nil {
		t.Fatal(err)
	}
	// Check while open (WAL present) and after close (checkpointed).
	check := func(stage string) {
		entries, _ := os.ReadDir(filepath.Dir(env.path))
		for _, e := range entries {
			b, err := os.ReadFile(filepath.Join(filepath.Dir(env.path), e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(b, []byte(secret)) || bytes.Contains(b, []byte("SQLite format 3")) {
				t.Errorf("%s: %s contains plaintext", stage, e.Name())
			}
		}
	}
	check("open")
	env.db.Close()
	check("closed")
}

func TestDBUtil(t *testing.T) {
	env := newTestDB(t)
	ctx := ctxT(t)
	d1, err := env.db.DBUtil()
	if err != nil {
		t.Fatal(err)
	}
	d2, _ := env.db.DBUtil()
	if d1 != d2 {
		t.Error("DBUtil should return the same instance")
	}
	if d1.RawDB != env.db.RawDB() {
		t.Error("DBUtil must wrap the shared *sql.DB")
	}
	exists, err := d1.TableExists(ctx, "warte_messages")
	if err != nil || !exists {
		t.Errorf("TableExists = %v, %v", exists, err)
	}
}

func TestFTSUpgradeFromFTS4(t *testing.T) {
	env := newTestDB(t)
	if env.db.FTSModule() != string(fts5) {
		t.Skip("build without sqlite_fts5; upgrade path not applicable")
	}
	ctx := ctxT(t)
	if _, err := env.db.PutMessage(ctx, msg("u:a", "a", 1000, "upgrade me please")); err != nil {
		t.Fatal(err)
	}
	// Rebuild the index as FTS4, as an older non-FTS5 build would have.
	raw := env.db.RawDB()
	for _, tr := range ftsTriggers {
		if _, err := raw.Exec(`DROP TRIGGER IF EXISTS ` + tr); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := raw.Exec(`DROP TABLE warte_fts`); err != nil {
		t.Fatal(err)
	}
	for _, s := range ftsDDL[fts4] {
		if _, err := raw.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := raw.Exec(`UPDATE warte_meta SET value = 'fts4' WHERE key = ?`, ftsMetaKey); err != nil {
		t.Fatal(err)
	}
	env.db.Close()
	db, err := Open(env.path, env.key)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if db.FTSModule() != string(fts5) {
		t.Fatalf("module = %s, want fts5", db.FTSModule())
	}
	hits, err := db.Search(ctx, core.SearchQuery{Text: "upgrade"})
	if err != nil || len(hits) != 1 {
		t.Fatalf("search after upgrade = %d hits, %v", len(hits), err)
	}
}

func TestConcurrentAccess(t *testing.T) {
	env := newTestDB(t)
	ctx := ctxT(t)
	const writers, perWriter = 8, 40
	var wg sync.WaitGroup
	errs := make(chan error, writers*perWriter*3)
	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			chat := core.ChatID("u:w" + string(rune('a'+w)))
			for i := range perWriter {
				m := msg(chat, core.ACI("w"), uint64(1000+i), "concurrent message")
				if _, err := env.db.PutMessage(ctx, m); err != nil {
					errs <- err
					return
				}
				if _, err := env.db.SetReaction(ctx, m.ID, core.Reaction{Emoji: "👍", From: "x"}); err != nil {
					errs <- err
					return
				}
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range perWriter {
				if _, err := env.db.ListChats(ctx, core.ChatFilter{}); err != nil {
					errs <- err
					return
				}
				if _, err := env.db.Search(ctx, core.SearchQuery{Text: "concurrent", Limit: 5}); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	chats, err := env.db.ListChats(ctx, core.ChatFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(chats) != writers {
		t.Fatalf("chats = %d, want %d", len(chats), writers)
	}
	for _, c := range chats {
		if c.Unread != perWriter {
			t.Errorf("%s unread = %d, want %d", c.ID, c.Unread, perWriter)
		}
	}
}

func TestDSNIncludesPragmas(t *testing.T) {
	ctx := ctxT(t)
	env := newTestDB(t)
	conn, err := env.db.RawDB().Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	tests := []struct {
		pragma string
		want   string
	}{
		{"journal_mode", "wal"},
		{"foreign_keys", "1"},
		{"busy_timeout", "5000"},
		{"secure_delete", "1"},
	}
	for _, tt := range tests {
		var got string
		if err := conn.QueryRowContext(ctx, "PRAGMA "+tt.pragma).Scan(&got); err != nil {
			t.Fatalf("%s: %v", tt.pragma, err)
		}
		if got != tt.want {
			t.Errorf("PRAGMA %s = %q, want %q", tt.pragma, got, tt.want)
		}
	}
}
