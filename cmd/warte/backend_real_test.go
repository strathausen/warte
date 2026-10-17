package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	tea "charm.land/bubbletea/v2"

	"warte/internal/config"
	"warte/internal/core"
	"warte/internal/fake"
	"warte/internal/store"
)

func TestUnlinkConfirmation(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		input     string
		unlinkErr error
		code      int
		unlinked  int
		wiped     int
		out       []string
	}{
		{name: "declined", input: "n\n", code: 1, out: []string{"Unlink this device", "canceled"}},
		{name: "empty answer is no", input: "\n", code: 1, out: []string{"canceled"}},
		{name: "eof is no", input: "", code: 1, out: []string{"canceled"}},
		{name: "unlink, keep data", input: "y\nn\n", code: 0, unlinked: 1,
			out: []string{"This device is unlinked.", "delete warte's local data", "Local data kept"}},
		{name: "unlink and wipe", input: "yes\nY\n", code: 0, unlinked: 1, wiped: 1,
			out: []string{"This device is unlinked.", "Deleted the local data in /data/warte"}},
		{name: "--yes skips prompts", args: []string{"--yes"}, code: 0, unlinked: 1, wiped: 1,
			out: []string{"Deleted the local data"}},
		{name: "not linked still offers wipe", input: "y\ny\n", unlinkErr: core.ErrNotLoggedIn, code: 0, unlinked: 1, wiped: 1,
			out: []string{"not linked", "Deleted"}},
		{name: "server unreachable", input: "y\nn\n", unlinkErr: errUnlinkedLocally, code: 1, unlinked: 1,
			out: []string{"may not know", "Linked devices", "Local data kept"}},
		{name: "hard failure", input: "y\ny\n", unlinkErr: store.ErrWrongPassphrase, code: 1, unlinked: 1,
			out: []string{"wrong passphrase"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			isolate(t)
			s := stubBackend(t)
			s.unlinkErr = c.unlinkErr
			withStdin(t, c.input)
			var out, errb bytes.Buffer
			code := run(append([]string{"unlink"}, c.args...), &out, &errb)
			all := out.String() + errb.String()
			if code != c.code || s.unlinked != c.unlinked || s.wiped != c.wiped {
				t.Fatalf("exit %d unlinked %d wiped %d, want %d %d %d\n%s", code, s.unlinked, s.wiped, c.code, c.unlinked, c.wiped, all)
			}
			for _, want := range c.out {
				if !strings.Contains(all, want) {
					t.Errorf("output lacks %q:\n%s", want, all)
				}
			}
			if len(c.args) > 0 && strings.Contains(all, "[y/N]") {
				t.Errorf("--yes still prompted:\n%s", all)
			}
		})
	}
}

func TestLockFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), lockFileName)
	a, err := acquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireLock(path); !errors.Is(err, errLocked) {
		t.Fatalf("second lock: %v, want errLocked", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("lock mode %v", fi.Mode().Perm())
	}
	releaseLock(a)
	b, err := acquireLock(path)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	releaseLock(b)
}

// TestSecondInstanceRefused opens the real environment twice.
func TestSecondInstanceRefused(t *testing.T) {
	isolate(t)
	cfg := config.Default()
	e1, err := openEnv(cfg, newConsole(io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	defer e1.close()
	_, err = openEnv(cfg, newConsole(io.Discard))
	if !errors.Is(err, errLocked) || !strings.Contains(err.Error(), "quit it first") {
		t.Fatalf("second open: %v", err)
	}
	// A subcommand reports it clearly.
	var out, errb bytes.Buffer
	if code := run([]string{"export", filepath.Join(t.TempDir(), "x")}, &out, &errb); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(errb.String(), "another warte process") {
		t.Fatalf("stderr %q", errb.String())
	}
}

// TestPassphrasePrompt covers the no-keychain path on a terminal: a new
// passphrase is asked twice, a wrong one is retried.
func TestPassphrasePrompt(t *testing.T) {
	isolate(t)
	t.Setenv(store.NoKeyringEnv, "")
	t.Setenv(envKeyring, "file")
	answers := []string{"a", "b", "pw", "pw"} // mismatch, then match
	oldTTY, oldRead := stdinIsTerminal, readPassword
	stdinIsTerminal = func() bool { return true }
	readPassword = func() (string, error) {
		if len(answers) == 0 {
			return "", io.EOF
		}
		a := answers[0]
		answers = answers[1:]
		return a, nil
	}
	t.Cleanup(func() { stdinIsTerminal, readPassword = oldTTY, oldRead })

	var prompts bytes.Buffer
	cfg := config.Default()
	e, err := openEnv(cfg, newConsole(&prompts))
	if err != nil {
		t.Fatal(err)
	}
	dir := e.paths.DataDir
	e.close()
	if !strings.Contains(prompts.String(), "do not match") {
		t.Errorf("prompts %q", prompts.String())
	}

	answers = []string{"wrong", "pw"}
	prompts.Reset()
	e, err = openEnv(cfg, newConsole(&prompts))
	if err != nil {
		t.Fatal(err)
	}
	e.close()
	if !strings.Contains(prompts.String(), "Wrong passphrase") {
		t.Errorf("prompts %q", prompts.String())
	}

	// Scripted: $WARTE_PASSPHRASE, with a warning.
	t.Setenv(envPassphrase, "pw")
	prompts.Reset()
	e, err = openEnv(cfg, newConsole(&prompts))
	if err != nil {
		t.Fatal(err)
	}
	e.close()
	if !strings.Contains(prompts.String(), envPassphrase) {
		t.Errorf("no warning: %q", prompts.String())
	}
	// Child processes (editor, opener, player) must not inherit it.
	if v, ok := os.LookupEnv(envPassphrase); ok {
		t.Errorf("$%s still set after opening: %q", envPassphrase, v)
	}
	t.Setenv(envPassphrase, "nope")
	if _, err := openEnv(cfg, newConsole(io.Discard)); !errors.Is(err, store.ErrWrongPassphrase) {
		t.Fatalf("wrong env passphrase: %v", err)
	}

	// No terminal and no variable: a clear error, no hang.
	t.Setenv(envPassphrase, "")
	stdinIsTerminal = func() bool { return false }
	if _, err := openEnv(cfg, newConsole(io.Discard)); err == nil || !strings.Contains(err.Error(), envPassphrase) {
		t.Fatalf("headless: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, store.KeyFileName)); err != nil {
		t.Fatal(err)
	}
}

// countingLinker records Link calls and links "instantly" without network.
type countingLinker struct {
	fake.Linker
	calls atomic.Int32
}

func (l *countingLinker) Link(ctx context.Context, name string) (<-chan core.LinkEvent, error) {
	l.calls.Add(1)
	return l.Linker.Link(ctx, name)
}

// TestRealBackendChoosesLink opens a fresh data directory with the real
// backend (mock keyring, no network): Start reports ErrNotLoggedIn, so warte
// runs the link flow through the linker seam.
func TestRealBackendChoosesLink(t *testing.T) {
	isolate(t)
	cfg := config.Default()

	svc, err := newRealService(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Start(context.Background()); !errors.Is(err, core.ErrNotLoggedIn) {
		t.Fatalf("Start: %v, want ErrNotLoggedIn", err)
	}
	if err := svc.Close(); err != nil {
		t.Fatal(err)
	}
	e := curEnv
	for _, name := range []string{"warte.db", "blobs", lockFileName, store.KeyFileName} {
		if _, err := os.Stat(filepath.Join(e.paths.DataDir, name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if fi, err := os.Stat(e.paths.DataDir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("data dir: %v %v", fi.Mode(), err)
	}
	if fi, err := os.Stat(e.paths.LogFile); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("log file: %v", err)
	}
	closeRealEnv()

	// The full client path: not linked → link screen → (fake linker
	// "links" but writes no device) → clear error instead of a loop.
	l := &countingLinker{Linker: fake.Linker{Delay: -1}}
	oldLinker, oldOpts := newRealLinker, linkProgramOptions
	newRealLinker = func(context.Context, config.Config) (core.Linker, error) { return l, nil }
	linkProgramOptions = []tea.ProgramOption{tea.WithInput(strings.NewReader("")), tea.WithOutput(io.Discard), tea.WithWindowSize(80, 24)}
	t.Cleanup(func() { newRealLinker, linkProgramOptions = oldLinker, oldOpts })

	var out, errb bytes.Buffer
	code := run(nil, &out, &errb)
	if code != 1 || !strings.Contains(errb.String(), "no linked account") {
		t.Fatalf("exit %d, stderr %q", code, errb.String())
	}
	if n := l.calls.Load(); n != 1 {
		t.Fatalf("linker called %d times, want 1", n)
	}
	if out.Len() != 0 {
		t.Errorf("stdout must stay empty: %q", out.String())
	}
	// The lock was released on exit.
	if curEnv != nil {
		t.Error("environment left open")
	}

	// "warte link" on the unlinked store runs the linker as well.
	if code := run([]string{"link"}, &out, &errb); code != 0 {
		t.Fatalf("link: exit %d (%s)", code, errb.String())
	}
	if n := l.calls.Load(); n != 2 || !strings.Contains(out.String(), "Linked to") {
		t.Fatalf("calls %d, out %q", n, out.String())
	}

	// Unlink on the unlinked store: nothing to tell Signal; wipe removes
	// the database, blobs and key file.
	withStdin(t, "y\ny\n")
	out.Reset()
	errb.Reset()
	if code := run([]string{"unlink"}, &out, &errb); code != 0 {
		t.Fatalf("unlink: exit %d (%s)", code, errb.String())
	}
	if !strings.Contains(out.String(), "not linked") || !strings.Contains(out.String(), "Deleted the local data") {
		t.Fatalf("unlink out %q", out.String())
	}
	for _, name := range []string{"warte.db", "blobs", store.KeyFileName} {
		if _, err := os.Stat(filepath.Join(e.paths.DataDir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s still there: %v", name, err)
		}
	}
}

func TestKeyringService(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	if s := keyringService("/x"); s != "warte" {
		t.Fatalf("default: %q", s)
	}
	t.Setenv("XDG_DATA_HOME", "/tmp/a")
	a, b := keyringService("/tmp/a/warte"), keyringService("/tmp/b/warte")
	if a == "warte" || a == b || !strings.HasPrefix(a, "warte-") {
		t.Fatalf("custom: %q %q", a, b)
	}
}

func TestKeyringEnv(t *testing.T) {
	t.Setenv(envKeyring, "bogus")
	if err := applyKeyringEnv(); err == nil {
		t.Fatal("bogus accepted")
	}
}

// TestRealLinkedAndExportAreLocal: "warte link"'s linked check and "warte
// export" read the local store only (no Start, no connection), so they
// work offline. The linked-device case is covered by the backend's
// TestIsLinkedAndOfflineExport.
func TestRealLinkedAndExportAreLocal(t *testing.T) {
	isolate(t)
	cfg := config.Default()
	if err := openRealEnv(cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeRealEnv)
	ctx := context.Background()
	if linked, err := realLinked(ctx, cfg); err != nil || linked {
		t.Fatalf("realLinked = %v, %v", linked, err)
	}
	path := filepath.Join(t.TempDir(), "out.warte")
	if err := realExportBackup(ctx, cfg, path); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Size() == 0 {
		t.Fatalf("export file: %v", err)
	}
}

// TestLockVerifierUsesDataPassphrase: with passphrase-protected data the
// screen lock is unlocked with the data passphrase; with keychain keys the
// TUI's own lock password is used (nil verifier).
func TestLockVerifierUsesDataPassphrase(t *testing.T) {
	old := store.DefaultArgon2
	store.DefaultArgon2 = store.Argon2Params{Time: 1, MemoryKiB: 1024, Threads: 1}
	t.Cleanup(func() { store.DefaultArgon2 = old })
	setEnv := func(e *realEnv) {
		envMu.Lock()
		curEnv = e
		envMu.Unlock()
	}
	t.Cleanup(func() { setEnv(nil) })

	if v := realLockVerifier(); v != nil {
		t.Fatalf("no environment: verifier %T", v)
	}

	isolate(t) // mock keychain
	e, err := openEnv(config.Default(), newConsole(io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	setEnv(e)
	if v := realLockVerifier(); v != nil {
		t.Errorf("keychain keys: verifier %T, want nil", v)
	}
	e.close()
	setEnv(nil)

	isolate(t)
	t.Setenv(store.NoKeyringEnv, "")
	t.Setenv(envKeyring, "file")
	t.Setenv(envPassphrase, "correct horse")
	e, err = openEnv(config.Default(), newConsole(io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	defer e.close()
	setEnv(e)
	v := realLockVerifier()
	if v == nil {
		t.Fatal("passphrase keys: no verifier")
	}
	if !v.Verify("correct horse") || v.Verify("wrong") || v.Verify("") {
		t.Error("verifier does not check the data passphrase")
	}
}
