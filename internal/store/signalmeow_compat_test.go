package store

import (
	"testing"

	"go.mau.fi/mautrix-signal/pkg/signalmeow/store/upgrades"
	"go.mau.fi/util/dbutil"
)

// TestSignalmeowSchemaOnSharedDB runs signalmeow's real schema upgrades on
// the shared SQLCipher database (bundled SQLite 3.33) to prove the driver
// supports every statement signalmeow needs, and that the two schemas
// coexist. The upgrades package has no libsignal dependency, so this links
// without the Rust library.
func TestSignalmeowSchemaOnSharedDB(t *testing.T) {
	env := newTestDB(t)
	ctx := ctxT(t)
	dbu, err := env.db.DBUtil()
	if err != nil {
		t.Fatal(err)
	}
	child := dbu.Child("signalmeow_version", upgrades.Table, dbutil.NoopLogger)
	if err := child.Upgrade(ctx); err != nil {
		t.Fatalf("signalmeow upgrade on shared db: %v", err)
	}
	for _, tbl := range []string{"signalmeow_device", "signalmeow_sessions", "warte_messages"} {
		ok, err := dbu.TableExists(ctx, tbl)
		if err != nil || !ok {
			t.Errorf("table %s exists = %v, %v", tbl, ok, err)
		}
	}
	// warte's store keeps working after signalmeow's tables were added.
	if _, err := env.db.PutMessage(ctx, msg("u:a", "a", 1000, "hello")); err != nil {
		t.Fatal(err)
	}
	env.db.Close()
	db, err := Open(env.path, env.key)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()
	dbu2, _ := db.DBUtil()
	if err := dbu2.Child("signalmeow_version", upgrades.Table, dbutil.NoopLogger).Upgrade(ctx); err != nil {
		t.Fatalf("signalmeow re-upgrade: %v", err)
	}
}
