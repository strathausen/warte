package store

import (
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/zalando/go-keyring"
	"golang.org/x/crypto/argon2"
)

// KeyFileName is the file in the data directory recording where the master
// secret comes from (and, for passphrases, the argon2id salt and parameters).
// It never contains key material.
const KeyFileName = "keys.json"

// keyringUser is the account name of warte's keychain entry.
const keyringUser = "master-key"

// NoKeyringEnv, when set to a non-empty value, makes LoadOrCreateKeys skip the
// OS keychain and use the passphrase fallback (for tests and headless boxes).
const NoKeyringEnv = "WARTE_NO_KEYRING"

var (
	// ErrKeychainUnavailable is returned when the keychain cannot be used and
	// no passphrase callback was supplied.
	ErrKeychainUnavailable = errors.New("store: OS keychain unavailable and no passphrase provided")
	// ErrKeyMissing is returned when keys.json says the key is in the
	// keychain but the entry is gone or unreadable.
	ErrKeyMissing = errors.New("store: data key missing from OS keychain")
	// ErrWrongPassphrase is returned when the passphrase does not match.
	ErrWrongPassphrase = errors.New("store: wrong passphrase")
	// ErrEmptyPassphrase is returned when the passphrase callback returns "".
	ErrEmptyPassphrase = errors.New("store: empty passphrase")
)

// Argon2Params are argon2id cost parameters for the passphrase fallback.
type Argon2Params struct {
	Time      uint32 `json:"time"`
	MemoryKiB uint32 `json:"memory_kib"`
	Threads   uint8  `json:"threads"`
}

// valid bounds-checks parameters read from disk: argon2.IDKey panics on
// zero threads, and absurd costs from a corrupted or tampered keys.json must
// not hang or OOM the process.
func (p Argon2Params) valid() bool {
	return p.Time >= 1 && p.Time <= 64 &&
		p.Threads >= 1 &&
		p.MemoryKiB >= 8*uint32(p.Threads) && p.MemoryKiB <= 4*1024*1024
}

// DefaultArgon2 are the parameters used for new passphrase-protected stores
// (RFC 9106 second recommended option: t=3, m=64 MiB).
var DefaultArgon2 = Argon2Params{Time: 3, MemoryKiB: 64 * 1024, Threads: 4}

const (
	sourceKeyring    = "keyring"
	sourcePassphrase = "passphrase"
	masterSize       = 32
)

type keyFile struct {
	Version int           `json:"version"`
	Source  string        `json:"source"`
	Salt    []byte        `json:"salt,omitempty"`
	Argon2  *Argon2Params `json:"argon2,omitempty"`
	Check   []byte        `json:"check,omitempty"` // HMAC verifier of the derived master
}

// LoadOrCreateKeys returns the database and blob keys (32 bytes each) for
// the data directory, creating a master secret on first use.
//
// The master secret is stored in the OS keychain (go-keyring, entry
// service/"master-key"). If the keychain is unavailable (or NoKeyringEnv is
// set) the master secret is derived from a passphrase with argon2id; the
// salt and parameters live in dataDir/keys.json (mode 0600). passphrase is
// only called in that case and may be nil if a keychain is required.
//
// keys.json records which source was used, so a keychain that is temporarily
// unreachable yields ErrKeyMissing instead of silently creating a new key.
// Both keys are derived from the master secret with HKDF-SHA256 under
// distinct labels. Tests should call keyring.MockInit first.
func LoadOrCreateKeys(service, dataDir string, passphrase func() (string, error)) (dbKey, blobKey []byte, err error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("store: create data dir: %w", err)
	}
	kf, err := readKeyFile(dataDir)
	if err != nil {
		return nil, nil, err
	}
	var master []byte
	switch {
	case kf != nil && kf.Source == sourceKeyring:
		master, err = keyringGet(service)
		if errors.Is(err, keyring.ErrNotFound) {
			return nil, nil, ErrKeyMissing
		}
		if err != nil {
			return nil, nil, fmt.Errorf("%w: %v", ErrKeyMissing, err)
		}
	case kf != nil && kf.Source == sourcePassphrase:
		master, err = unlockPassphrase(kf, passphrase)
	case kf != nil:
		return nil, nil, fmt.Errorf("store: unknown key source %q in %s", kf.Source, KeyFileName)
	default:
		master, err = createMaster(service, dataDir, passphrase)
	}
	if err != nil {
		return nil, nil, err
	}
	return deriveKeys(master)
}

// ForgetKeys removes the keychain entry and keys.json (used by unlink, after
// the database and blobs have been deleted).
func ForgetKeys(service, dataDir string) error {
	var errs []error
	if os.Getenv(NoKeyringEnv) == "" {
		if err := keyring.Delete(service, keyringUser); err != nil && !errors.Is(err, keyring.ErrNotFound) {
			errs = append(errs, err)
		}
	}
	if err := os.Remove(filepath.Join(dataDir, KeyFileName)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func deriveKeys(master []byte) (dbKey, blobKey []byte, err error) {
	dbKey, err = hkdf.Key(sha256.New, master, nil, "warte db key v1", KeySize)
	if err != nil {
		return nil, nil, err
	}
	blobKey, err = hkdf.Key(sha256.New, master, nil, "warte blob key v1", KeySize)
	if err != nil {
		return nil, nil, err
	}
	return dbKey, blobKey, nil
}

func keyringGet(service string) ([]byte, error) {
	s, err := keyring.Get(service, keyringUser)
	if err != nil {
		return nil, err
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != masterSize {
		return nil, errors.New("store: malformed keychain entry")
	}
	return b, nil
}

func createMaster(service, dataDir string, passphrase func() (string, error)) ([]byte, error) {
	if os.Getenv(NoKeyringEnv) == "" {
		master, err := keyringGet(service)
		if err == nil {
			// keys.json was lost but the keychain still has the secret.
			return master, writeKeyFile(dataDir, keyFile{Version: 1, Source: sourceKeyring})
		}
		if errors.Is(err, keyring.ErrNotFound) {
			master = make([]byte, masterSize)
			if _, err := rand.Read(master); err != nil {
				return nil, err
			}
			if err := keyring.Set(service, keyringUser, base64.StdEncoding.EncodeToString(master)); err == nil {
				return master, writeKeyFile(dataDir, keyFile{Version: 1, Source: sourceKeyring})
			}
		}
		// Keychain unusable: fall through to a passphrase.
	}
	if passphrase == nil {
		return nil, ErrKeychainUnavailable
	}
	pass, err := passphrase()
	if err != nil {
		return nil, err
	}
	if pass == "" {
		return nil, ErrEmptyPassphrase
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	params := DefaultArgon2
	master := argon2.IDKey([]byte(pass), salt, params.Time, params.MemoryKiB, params.Threads, masterSize)
	kf := keyFile{Version: 1, Source: sourcePassphrase, Salt: salt, Argon2: &params, Check: keyCheck(master)}
	return master, writeKeyFile(dataDir, kf)
}

func unlockPassphrase(kf *keyFile, passphrase func() (string, error)) ([]byte, error) {
	if passphrase == nil {
		return nil, ErrKeychainUnavailable
	}
	if len(kf.Salt) < 8 || kf.Argon2 == nil || len(kf.Check) == 0 || !kf.Argon2.valid() {
		return nil, fmt.Errorf("store: malformed %s", KeyFileName)
	}
	pass, err := passphrase()
	if err != nil {
		return nil, err
	}
	if pass == "" {
		return nil, ErrEmptyPassphrase
	}
	p := kf.Argon2
	master := argon2.IDKey([]byte(pass), kf.Salt, p.Time, p.MemoryKiB, p.Threads, masterSize)
	if !hmac.Equal(keyCheck(master), kf.Check) {
		return nil, ErrWrongPassphrase
	}
	return master, nil
}

func keyCheck(master []byte) []byte {
	m := hmac.New(sha256.New, master)
	m.Write([]byte("warte key check v1"))
	return m.Sum(nil)[:16]
}

func readKeyFile(dataDir string) (*keyFile, error) {
	b, err := os.ReadFile(filepath.Join(dataDir, KeyFileName))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: read %s: %w", KeyFileName, err)
	}
	var kf keyFile
	if err := json.Unmarshal(b, &kf); err != nil {
		return nil, fmt.Errorf("store: parse %s: %w", KeyFileName, err)
	}
	return &kf, nil
}

func writeKeyFile(dataDir string, kf keyFile) error {
	b, err := json.MarshalIndent(kf, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dataDir, ".keys-*")
	if err != nil {
		return fmt.Errorf("store: write %s: %w", KeyFileName, err)
	}
	name := tmp.Name()
	_, werr := tmp.Write(b)
	serr := tmp.Sync()
	cerr := tmp.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("store: write %s: %w", KeyFileName, err)
	}
	if err := os.Rename(name, filepath.Join(dataDir, KeyFileName)); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("store: write %s: %w", KeyFileName, err)
	}
	return nil
}

// PassphraseProtected reports whether dataDir's master secret is derived
// from a passphrase (no OS keychain).
func PassphraseProtected(dataDir string) bool {
	kf, err := readKeyFile(dataDir)
	return err == nil && kf != nil && kf.Source == sourcePassphrase
}

// CheckPassphrase reports whether pass is the passphrase of dataDir's
// passphrase-protected master secret. It is false for keychain stores.
func CheckPassphrase(dataDir, pass string) bool {
	kf, err := readKeyFile(dataDir)
	if err != nil || kf == nil || kf.Source != sourcePassphrase || pass == "" {
		return false
	}
	master, err := unlockPassphrase(kf, func() (string, error) { return pass, nil })
	clear(master)
	return err == nil
}
