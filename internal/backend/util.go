package backend

import (
	"encoding/hex"
	"errors"
	"fmt"
	"runtime/debug"

	"github.com/rs/zerolog"

	"warte/internal/core"
)

// ErrUnsupported is wrapped by errors for features signalmeow (the Signal
// protocol library warte uses) cannot do yet. Frontends can test for it with
// errors.Is to show a "not supported" hint instead of a failure.
var ErrUnsupported = errors.New("not supported by warte's Signal library yet")

// errorf wraps sentinel with a message: errors.Is(err, sentinel) holds.
func errorf(sentinel error, format string, args ...any) error {
	return fmt.Errorf("%s: %w", fmt.Sprintf(format, args...), sentinel)
}

// hexString hex-encodes b ("" for nil).
func hexString(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return hex.EncodeToString(b)
}

// errPanic wraps panics recovered from handlers and background jobs.
var errPanic = errors.New("internal error")

// recovered turns a recovered panic value into an error, logs it with a
// stack trace and announces it. It never logs message content: the panic
// value of a runtime error carries only types and indexes.
func (b *Backend) recovered(op string, v any) error {
	err := fmt.Errorf("backend: %s: %w: %v", op, errPanic, v)
	b.log.Error().Str("op", op).Bytes("stack", debug.Stack()).Msgf("Recovered from panic: %v", v)
	b.emit(core.ErrorEvent{Op: op, Err: err})
	return err
}

// guard runs fn, converting a panic into an error. signalmeow's decryption
// helpers index into untrusted ciphertext (an attacker-hosted sticker or
// attachment shorter than its MAC panics), so every call that decrypts
// remote data goes through guard.
func guard[T any](op string, fn func() (T, error)) (out T, err error) {
	defer func() {
		if v := recover(); v != nil {
			var zero T
			out, err = zero, fmt.Errorf("backend: %s: %w: %v", op, errPanic, v)
		}
	}()
	return fn()
}

// privateLogger caps verbosity at debug. At trace level signalmeow dumps
// raw message content, contact discovery results and the provisioning
// message (account keys) into the log; warte never logs those.
func privateLogger(l zerolog.Logger) zerolog.Logger {
	if l.GetLevel() < zerolog.DebugLevel {
		return l.Level(zerolog.DebugLevel)
	}
	return l
}
