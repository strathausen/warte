package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zalando/go-keyring"
)

// fastArgon2 keeps passphrase tests quick.
func fastArgon2(t *testing.T) {
	t.Helper()
	old := DefaultArgon2
	DefaultArgon2 = Argon2Params{Time: 1, MemoryKiB: 1024, Threads: 1}
	t.Cleanup(func() { DefaultArgon2 = old })
}

func pass(s string) func() (string, error) { return func() (string, error) { return s, nil } }

func readKF(t *testing.T, dir string) keyFile {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, KeyFileName))
	if err != nil {
		t.Fatal(err)
	}
	var kf keyFile
	if err := json.Unmarshal(b, &kf); err != nil {
		t.Fatal(err)
	}
	return kf
}

func TestKeysKeyring(t *testing.T) {
	keyring.MockInit()
	dir := t.TempDir()
	called := false
	cb := func() (string, error) { called = true; return "x", nil }
	db1, blob1, err := LoadOrCreateKeys("warte-test", dir, cb)
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Error("passphrase requested although keychain works")
	}
	if len(db1) != KeySize || len(blob1) != KeySize || bytes.Equal(db1, blob1) {
		t.Fatalf("bad keys %x %x", db1, blob1)
	}
	if kf := readKF(t, dir); kf.Source != sourceKeyring || kf.Salt != nil {
		t.Errorf("keys.json = %+v", kf)
	}
	db2, blob2, err := LoadOrCreateKeys("warte-test", dir, nil)
	if err != nil || !bytes.Equal(db1, db2) || !bytes.Equal(blob1, blob2) {
		t.Fatalf("reload mismatch: %v", err)
	}
	// keys.json lost: keychain entry is reused.
	os.Remove(filepath.Join(dir, KeyFileName))
	db3, _, err := LoadOrCreateKeys("warte-test", dir, nil)
	if err != nil || !bytes.Equal(db1, db3) {
		t.Fatalf("recover from keychain: %v", err)
	}
	// keys.json says keychain, but the entry is gone.
	if err := keyring.Delete("warte-test", keyringUser); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadOrCreateKeys("warte-test", dir, cb); !errors.Is(err, ErrKeyMissing) {
		t.Errorf("err = %v, want ErrKeyMissing", err)
	}
	if err := ForgetKeys("warte-test", dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, KeyFileName)); !os.IsNotExist(err) {
		t.Error("keys.json not removed")
	}
}

func TestKeysPassphraseFallback(t *testing.T) {
	fastArgon2(t)
	tests := []struct {
		name  string
		setup func(t *testing.T)
	}{
		{"keychain errors", func(t *testing.T) { keyring.MockInitWithError(errors.New("no dbus")) }},
		{"env override", func(t *testing.T) { keyring.MockInit(); t.Setenv(NoKeyringEnv, "1") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup(t)
			dir := t.TempDir()
			if _, _, err := LoadOrCreateKeys("warte-test", dir, nil); !errors.Is(err, ErrKeychainUnavailable) {
				t.Fatalf("no callback err = %v", err)
			}
			if _, _, err := LoadOrCreateKeys("warte-test", dir, pass("")); !errors.Is(err, ErrEmptyPassphrase) {
				t.Fatalf("empty passphrase err = %v", err)
			}
			db1, blob1, err := LoadOrCreateKeys("warte-test", dir, pass("hunter2"))
			if err != nil {
				t.Fatal(err)
			}
			kf := readKF(t, dir)
			if kf.Source != sourcePassphrase || len(kf.Salt) != 16 || kf.Argon2 == nil || len(kf.Check) == 0 {
				t.Fatalf("keys.json = %+v", kf)
			}
			st, _ := os.Stat(filepath.Join(dir, KeyFileName))
			if st.Mode().Perm() != 0o600 {
				t.Errorf("keys.json mode %o", st.Mode().Perm())
			}
			raw, _ := os.ReadFile(filepath.Join(dir, KeyFileName))
			if bytes.Contains(raw, []byte("hunter2")) {
				t.Error("passphrase written to disk")
			}
			db2, blob2, err := LoadOrCreateKeys("warte-test", dir, pass("hunter2"))
			if err != nil || !bytes.Equal(db1, db2) || !bytes.Equal(blob1, blob2) {
				t.Fatalf("reload: %v", err)
			}
			if _, _, err := LoadOrCreateKeys("warte-test", dir, pass("hunter3")); !errors.Is(err, ErrWrongPassphrase) {
				t.Errorf("wrong passphrase err = %v", err)
			}
			cbErr := errors.New("cancelled")
			if _, _, err := LoadOrCreateKeys("warte-test", dir, func() (string, error) { return "", cbErr }); !errors.Is(err, cbErr) {
				t.Errorf("callback err = %v", err)
			}
			// Once in passphrase mode, a working keychain is not consulted.
			keyring.MockInit()
			db3, _, err := LoadOrCreateKeys("warte-test", dir, pass("hunter2"))
			if err != nil || !bytes.Equal(db1, db3) {
				t.Errorf("passphrase mode with keychain back: %v", err)
			}
		})
	}
}

func TestKeysOpenStores(t *testing.T) {
	keyring.MockInit()
	dir := t.TempDir()
	dbKey, blobKey, err := LoadOrCreateKeys("warte-test", dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	db, err := Open(filepath.Join(dir, "warte.db"), dbKey)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := OpenBlobs(filepath.Join(dir, "blobs"), blobKey); err != nil {
		t.Fatal(err)
	}
}

func TestKeysMalformedFile(t *testing.T) {
	keyring.MockInit()
	tests := []struct {
		name    string
		content string
	}{
		{"not json", "{"},
		{"unknown source", `{"version":1,"source":"tpm"}`},
		{"passphrase without salt", `{"version":1,"source":"passphrase"}`},
		// argon2.IDKey panics on zero threads; must be an error instead.
		{"zero threads", `{"version":1,"source":"passphrase","salt":"AAAAAAAAAAAAAAAAAAAAAA==","argon2":{"time":1,"memory_kib":1024,"threads":0},"check":"AAAAAAAAAAAAAAAAAAAAAA=="}`},
		{"absurd memory", `{"version":1,"source":"passphrase","salt":"AAAAAAAAAAAAAAAAAAAAAA==","argon2":{"time":1,"memory_kib":4294967295,"threads":1},"check":"AAAAAAAAAAAAAAAAAAAAAA=="}`},
		{"zero time", `{"version":1,"source":"passphrase","salt":"AAAAAAAAAAAAAAAAAAAAAA==","argon2":{"time":0,"memory_kib":1024,"threads":1},"check":"AAAAAAAAAAAAAAAAAAAAAA=="}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, KeyFileName), []byte(tt.content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := LoadOrCreateKeys("warte-test", dir, pass("x")); err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestCheckPassphrase(t *testing.T) {
	fastArgon2(t)
	keyring.MockInit()
	kc := t.TempDir()
	if _, _, err := LoadOrCreateKeys("warte-test-check", kc, nil); err != nil {
		t.Fatal(err)
	}
	if PassphraseProtected(kc) || CheckPassphrase(kc, "anything") {
		t.Error("keychain store treated as passphrase-protected")
	}
	if PassphraseProtected(t.TempDir()) {
		t.Error("empty dir treated as passphrase-protected")
	}

	t.Setenv(NoKeyringEnv, "1")
	dir := t.TempDir()
	if _, _, err := LoadOrCreateKeys("warte-test-check", dir, pass("hunter2")); err != nil {
		t.Fatal(err)
	}
	if !PassphraseProtected(dir) {
		t.Fatal("passphrase store not detected")
	}
	for pw, want := range map[string]bool{"hunter2": true, "hunter3": false, "": false, "hunter2 ": false} {
		if got := CheckPassphrase(dir, pw); got != want {
			t.Errorf("CheckPassphrase(%q) = %v, want %v", pw, got, want)
		}
	}
}
