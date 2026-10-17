package tui

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/argon2"

	"warte/internal/config"
)

// Verifier checks the passphrase that unlocks the screen lock. cmd/warte
// may provide one (Options.LockVerifier); without one the App uses its own
// lock password (lockPassword).
type Verifier interface {
	Verify(passphrase string) bool
}

// lockArgon are the argon2id parameters for new lock password files.
// Verification uses the parameters stored in the file. Tests lower them.
var lockArgon = struct {
	Time, Memory uint32 // memory in KiB
	Threads      uint8
}{Time: 3, Memory: 64 * 1024, Threads: 4}

const lockFileName = "lock"

// lockPassword is the default Verifier: an argon2id hash of a lock password
// the user sets the first time they lock, stored in the file "lock" (0600)
// in the config directory as
//
//	argon2id$v=19$t=3,m=65536,p=4$<salt b64>$<hash b64>
type lockPassword struct{ path string }

// defaultLockPath is the lock file next to config.toml.
func defaultLockPath(configPath string) string {
	if configPath == "" {
		p, err := config.ConfigPath()
		if err != nil {
			return ""
		}
		configPath = p
	}
	return filepath.Join(filepath.Dir(configPath), lockFileName)
}

// IsSet reports whether a lock password has been stored.
func (l lockPassword) IsSet() bool {
	if l.path == "" {
		return false
	}
	_, _, _, err := l.read()
	return err == nil
}

type argonParams struct {
	t, m uint32
	p    uint8
}

func (l lockPassword) read() (argonParams, []byte, []byte, error) {
	var ap argonParams
	b, err := os.ReadFile(l.path)
	if err != nil {
		return ap, nil, nil, err
	}
	parts := strings.Split(strings.TrimSpace(string(b)), "$")
	if len(parts) != 5 || parts[0] != "argon2id" || parts[1] != "v=19" {
		return ap, nil, nil, errors.New("lock file: unknown format")
	}
	var t, m, p uint32
	if _, err := fmt.Sscanf(parts[2], "t=%d,m=%d,p=%d", &t, &m, &p); err != nil || t == 0 || m == 0 || p == 0 || p > 255 || t > 100 || m > 4<<20 {
		return ap, nil, nil, errors.New("lock file: bad parameters")
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[3])
	hash, err2 := base64.RawStdEncoding.DecodeString(parts[4])
	if err1 != nil || err2 != nil || len(salt) < 16 || len(hash) < 16 {
		return ap, nil, nil, errors.New("lock file: bad encoding")
	}
	return argonParams{t: t, m: m, p: uint8(p)}, salt, hash, nil
}

// Verify implements Verifier.
func (l lockPassword) Verify(pw string) bool {
	ap, salt, hash, err := l.read()
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, ap.t, ap.m, ap.p, uint32(len(hash)))
	return subtle.ConstantTimeCompare(got, hash) == 1
}

// Set stores a new lock password (atomically, mode 0600).
func (l lockPassword) Set(pw string) error {
	if l.path == "" {
		return errors.New("no config directory for the lock file")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	p := lockArgon
	hash := argon2.IDKey([]byte(pw), salt, p.Time, p.Memory, p.Threads, 32)
	line := fmt.Sprintf("argon2id$v=19$t=%d,m=%d,p=%d$%s$%s\n", p.Time, p.Memory, p.Threads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash))
	dir := filepath.Dir(l.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".lock-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.WriteString(line); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, l.path)
}
