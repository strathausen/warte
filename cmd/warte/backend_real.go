package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/rs/zerolog"
	"github.com/zalando/go-keyring"

	"warte/internal/backend"
	"warte/internal/config"
	"warte/internal/core"
	"warte/internal/store"
	"warte/internal/tui"
)

// Environment variables read by the real backend wiring.
const (
	// envPassphrase supplies the data passphrase when no OS keychain is
	// available (scripting; prints a warning).
	envPassphrase = "WARTE_PASSPHRASE"
	// envKeyring selects the key storage (hidden, for testing):
	//   "" or "system": the OS keychain, falling back to a passphrase;
	//   "file": no keychain, always the passphrase (keys.json + argon2id);
	//   "mock": an in-memory keychain that is lost on exit (throwaway data).
	envKeyring = "WARTE_KEYRING"
)

// Names inside the data directory.
const (
	dbFileName   = "warte.db"
	blobsDirName = "blobs"
	lockFileName = "warte.lock"
)

// passphraseAttempts is how often a wrong passphrase may be retried on a
// terminal.
const passphraseAttempts = 3

// connectWait bounds how long unlink waits for the connection that tells
// Signal's servers.
const connectWait = 20 * time.Second

// errUnlinkedLocally means the device keys were deleted here but Signal's
// servers may still list the device.
var errUnlinkedLocally = errors.New("this device was unlinked locally, but Signal may not know")

// errLocked is returned when another warte process holds the data directory.
var errLocked = errors.New("another warte process is using this data directory")

// console is the terminal used for prompts before (or instead of) the TUI.
type console struct {
	in  *bufio.Reader
	out io.Writer
	// tty reports whether prompts can be answered interactively.
	tty bool
	// readSecret reads a line without echo.
	readSecret func() (string, error)
}

var (
	// stdin is where prompts read answers (tests replace it).
	stdin io.Reader = os.Stdin
	// stdinIsTerminal reports whether stdin is a terminal (tests replace it).
	stdinIsTerminal = func() bool { return term.IsTerminal(os.Stdin.Fd()) }
	// readPassword reads a secret from the terminal without echo.
	readPassword = func() (string, error) {
		b, err := term.ReadPassword(os.Stdin.Fd())
		return string(b), err
	}

	con = newConsole(os.Stderr)
)

func newConsole(out io.Writer) *console {
	c := &console{in: bufio.NewReader(stdin), out: out, tty: stdinIsTerminal()}
	c.readSecret = func() (string, error) {
		if !c.tty {
			return c.line()
		}
		s, err := readPassword()
		fmt.Fprintln(c.out)
		return s, err
	}
	return c
}

// line reads one line (without the newline). EOF with no input is an error.
func (c *console) line() (string, error) {
	s, err := c.in.ReadString('\n')
	if err != nil && (!errors.Is(err, io.EOF) || s == "") {
		return "", err
	}
	return strings.TrimRight(s, "\r\n"), nil
}

// confirm asks a yes/no question; anything but y/yes (including EOF) is no.
func (c *console) confirm(question string) bool {
	fmt.Fprint(c.out, question+" [y/N] ")
	s, err := c.line()
	if err != nil {
		fmt.Fprintln(c.out)
		return false
	}
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "y", "yes":
		return true
	}
	return false
}

// realEnv is the local account state of this process: the locked data
// directory, the open database and blob store, and the backend's log. It is
// opened once and shared by every backend and linker the process creates.
type realEnv struct {
	paths   config.Paths
	service string // keychain service name
	lock    *os.File
	db      *store.DB
	blobs   *store.Blobs
	blobKey []byte
	logFile *os.File
	log     zerolog.Logger
	device  string
}

var (
	envMu  sync.Mutex
	curEnv *realEnv
)

// getEnv opens the environment on first use.
func getEnv(cfg config.Config) (*realEnv, error) {
	envMu.Lock()
	defer envMu.Unlock()
	if curEnv != nil {
		return curEnv, nil
	}
	e, err := openEnv(cfg, con)
	if err != nil {
		return nil, err
	}
	curEnv = e
	return e, nil
}

// closeRealEnv closes the environment if it was opened.
func closeRealEnv() {
	envMu.Lock()
	defer envMu.Unlock()
	if curEnv != nil {
		curEnv.close()
		curEnv = nil
	}
}

// dataPassphrase unlocks the screen lock with the data passphrase.
type dataPassphrase struct{ dataDir string }

func (d dataPassphrase) Verify(pass string) bool { return store.CheckPassphrase(d.dataDir, pass) }

// dataPassphraseVerifier returns a dataPassphrase for the open environment
// when its keys come from a passphrase, else nil.
func dataPassphraseVerifier() tui.Verifier {
	envMu.Lock()
	e := curEnv
	envMu.Unlock()
	if e == nil || !store.PassphraseProtected(e.paths.DataDir) {
		return nil
	}
	return dataPassphrase{dataDir: e.paths.DataDir}
}

// mockKeyringOnce keeps one in-memory keychain per process.
var mockKeyringOnce sync.Once

// applyKeyringEnv honours WARTE_KEYRING.
func applyKeyringEnv() error {
	switch v := os.Getenv(envKeyring); v {
	case "", "system":
	case "file":
		return os.Setenv(store.NoKeyringEnv, "1")
	case "mock":
		mockKeyringOnce.Do(keyring.MockInit)
	default:
		return fmt.Errorf("%s=%q: want system, file or mock", envKeyring, v)
	}
	return nil
}

// keyringService is the keychain service name for dataDir: "warte" for the
// default data directory, and a name tied to the directory otherwise (so a
// test setup with XDG_DATA_HOME never touches the real entry).
func keyringService(dataDir string) string {
	if os.Getenv("XDG_DATA_HOME") == "" {
		return config.AppName
	}
	sum := sha256.Sum256([]byte(filepath.Clean(dataDir)))
	return config.AppName + "-" + hex.EncodeToString(sum[:6])
}

// openEnv locks the data directory, loads the keys (prompting for a
// passphrase if needed) and opens the stores.
func openEnv(cfg config.Config, c *console) (_ *realEnv, err error) {
	if err := applyKeyringEnv(); err != nil {
		return nil, err
	}
	paths, err := config.EnsureDirs()
	if err != nil {
		return nil, err
	}
	e := &realEnv{paths: paths, service: keyringService(paths.DataDir), device: cfg.DeviceName}
	defer func() {
		if err != nil {
			e.close()
		}
	}()
	if e.lock, err = acquireLock(filepath.Join(paths.DataDir, lockFileName)); err != nil {
		if errors.Is(err, errLocked) {
			return nil, fmt.Errorf("%w (%s); quit it first", errLocked, paths.DataDir)
		}
		return nil, err
	}
	e.logFile, e.log = openBackendLog(paths.LogFile, cfg.LogLevel, cfg.LogSensitive)

	dbKey, blobKey, err := loadKeys(e.service, paths.DataDir, c)
	if err != nil {
		return nil, err
	}
	defer clear(dbKey)
	// Child processes (editor, opener, audio player) inherit the
	// environment; the passphrase must not travel with it.
	_ = os.Unsetenv(envPassphrase)
	e.blobKey = blobKey
	if e.db, err = store.Open(filepath.Join(paths.DataDir, dbFileName), dbKey); err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if e.blobs, err = store.OpenBlobs(filepath.Join(paths.DataDir, blobsDirName), blobKey); err != nil {
		return nil, fmt.Errorf("open attachment store: %w", err)
	}
	e.log.Info().Str("component", "cli").Msg("Local data opened")
	return e, nil
}

// openBackendLog opens the backend's zerolog log (appending to the log
// file, 0600). Without a log file the log is discarded. Unless sensitive
// is set, phone numbers and raw server responses are redacted.
func openBackendLog(path, level string, sensitive bool) (*os.File, zerolog.Logger) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, zerolog.Nop()
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, zerolog.Nop()
	}
	_ = f.Chmod(0o600)
	lvl, err := zerolog.ParseLevel(level)
	if err != nil || level == "" {
		lvl = zerolog.InfoLevel
	}
	return f, zerolog.New(logWriter(f, sensitive)).Level(lvl).With().Timestamp().Logger()
}

// loadKeys returns the database and blob keys, retrying a wrong passphrase
// on a terminal.
func loadKeys(service, dataDir string, c *console) (dbKey, blobKey []byte, err error) {
	pass := passphraseFunc(dataDir, c)
	for attempt := 1; ; attempt++ {
		dbKey, blobKey, err = store.LoadOrCreateKeys(service, dataDir, pass)
		if errors.Is(err, store.ErrWrongPassphrase) && c.tty && os.Getenv(envPassphrase) == "" && attempt < passphraseAttempts {
			fmt.Fprintln(c.out, "Wrong passphrase, try again.")
			continue
		}
		break
	}
	switch {
	case err == nil:
		return dbKey, blobKey, nil
	case errors.Is(err, store.ErrKeyMissing):
		return nil, nil, fmt.Errorf("%w: the key for %s was in the OS keychain but cannot be read (unlock the keychain, or delete the directory to start over)", err, dataDir)
	case errors.Is(err, store.ErrWrongPassphrase), errors.Is(err, store.ErrEmptyPassphrase):
		return nil, nil, err
	}
	return nil, nil, fmt.Errorf("load keys: %w", err)
}

// passphraseFunc returns the passphrase callback for LoadOrCreateKeys. It is
// only called when the keychain cannot be used: it reads $WARTE_PASSPHRASE
// or asks on the terminal (twice when creating a new key).
func passphraseFunc(dataDir string, c *console) func() (string, error) {
	return func() (string, error) {
		if p := os.Getenv(envPassphrase); p != "" {
			fmt.Fprintf(c.out, "warte: using the passphrase from $%s (meant for scripts; prefer the OS keychain or typing it)\n", envPassphrase)
			return p, nil
		}
		if !c.tty {
			return "", fmt.Errorf("the OS keychain is unavailable and no terminal to ask for a passphrase; set $%s", envPassphrase)
		}
		_, statErr := os.Stat(filepath.Join(dataDir, store.KeyFileName))
		if statErr == nil {
			fmt.Fprint(c.out, "Passphrase for warte's local data: ")
			return c.readSecret()
		}
		fmt.Fprintln(c.out, "The OS keychain is not available. warte encrypts its local data with a passphrase instead.")
		fmt.Fprintln(c.out, "There is no way to recover it if you forget it.")
		for {
			fmt.Fprint(c.out, "New passphrase: ")
			p1, err := c.readSecret()
			if err != nil {
				return "", err
			}
			if p1 == "" {
				return "", store.ErrEmptyPassphrase
			}
			fmt.Fprint(c.out, "Repeat passphrase: ")
			p2, err := c.readSecret()
			if err != nil {
				return "", err
			}
			if p1 == p2 {
				return p1, nil
			}
			fmt.Fprintln(c.out, "The passphrases do not match, try again.")
		}
	}
}

// close releases everything in reverse order of opening.
func (e *realEnv) close() {
	if e.db != nil {
		_ = e.db.Close()
		e.db = nil
	}
	e.blobs = nil
	clear(e.blobKey)
	if e.logFile != nil {
		e.log.Info().Str("component", "cli").Msg("Local data closed")
		_ = e.logFile.Sync()
		_ = e.logFile.Close()
		e.logFile = nil
		e.log = zerolog.Nop()
	}
	if e.lock != nil {
		releaseLock(e.lock)
		e.lock = nil
	}
}

// options are the backend options over the shared stores.
func (e *realEnv) options() backend.Options {
	return backend.Options{
		DataDir:    e.paths.DataDir,
		Store:      e.db,
		Blobs:      e.blobs,
		BlobKey:    e.blobKey,
		DeviceName: e.device,
		Logger:     e.log,
	}
}

// newBackend returns a backend over the shared stores (closing it leaves
// them open).
func (e *realEnv) newBackend(ctx context.Context) (*backend.Backend, error) {
	if e.db == nil {
		return nil, errors.New("local data was deleted")
	}
	return backend.New(ctx, e.options())
}

func realService(ctx context.Context, cfg config.Config) (core.Service, error) {
	e, err := getEnv(cfg)
	if err != nil {
		return nil, err
	}
	return e.newBackend(ctx)
}

func realLinker(_ context.Context, cfg config.Config) (core.Linker, error) {
	e, err := getEnv(cfg)
	if err != nil {
		return nil, err
	}
	if e.db == nil {
		return nil, errors.New("local data was deleted")
	}
	return backend.NewLinker(e.options()), nil
}

// startBackend creates and starts a backend; b is returned (for Close) even
// when Start fails.
func startBackend(ctx context.Context, cfg config.Config) (*backend.Backend, error) {
	e, err := getEnv(cfg)
	if err != nil {
		return nil, err
	}
	b, err := e.newBackend(ctx)
	if err != nil {
		return nil, err
	}
	return b, b.Start(ctx)
}

// realLinked checks the local device table; it does not connect.
func realLinked(ctx context.Context, cfg config.Config) (bool, error) {
	e, err := getEnv(cfg)
	if err != nil {
		return false, err
	}
	b, err := e.newBackend(ctx)
	if err != nil {
		return false, err
	}
	defer b.Close()
	return b.IsLinked(ctx)
}

func realUnlinkDevice(ctx context.Context, cfg config.Config) error {
	b, err := startBackend(ctx, cfg)
	if b != nil {
		defer b.Close()
	}
	if err != nil {
		return err
	}
	// Unlink tells the server only over a live connection.
	wait, cancel := context.WithTimeout(ctx, connectWait)
	defer cancel()
	for b.ConnState() != core.ConnConnected && wait.Err() == nil {
		select {
		case <-wait.Done():
		case <-time.After(100 * time.Millisecond):
		}
	}
	connected := b.ConnState() == core.ConnConnected
	// Unlink forgets the device locally even when the server call fails.
	err = b.Unlink(ctx)
	switch {
	case err != nil:
		return fmt.Errorf("%w: %w", errUnlinkedLocally, err)
	case !connected:
		return fmt.Errorf("%w: could not reach Signal's servers", errUnlinkedLocally)
	}
	return nil
}

// realExportBackup exports the local store without connecting to Signal,
// so it works offline. An unlinked store can still be exported (without
// the account's own contact).
func realExportBackup(ctx context.Context, cfg config.Config, path string) error {
	e, err := getEnv(cfg)
	if err != nil {
		return err
	}
	b, err := e.newBackend(ctx)
	if err != nil {
		return err
	}
	defer b.Close()
	return b.ExportBackup(ctx, path)
}

// realWipeData deletes the database, the attachment store and the key.
func realWipeData(cfg config.Config) (string, error) {
	e, err := getEnv(cfg)
	if err != nil {
		return "", err
	}
	dir := e.paths.DataDir
	if e.db != nil {
		_ = e.db.Close()
		e.db = nil
	}
	e.blobs = nil
	var errs []error
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if err := os.Remove(filepath.Join(dir, dbFileName+suffix)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	if err := os.RemoveAll(filepath.Join(dir, blobsDirName)); err != nil {
		errs = append(errs, err)
	}
	if err := store.ForgetKeys(e.service, dir); err != nil {
		errs = append(errs, err)
	}
	e.log.Info().Str("component", "cli").Msg("Local data deleted")
	return dir, errors.Join(errs...)
}
