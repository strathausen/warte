//go:build linkcheck

// Run: make libsignal && go test -tags linkcheck ./internal/deps/ (with CGO_LDFLAGS from the Makefile)

package deps

import (
	"testing"

	"go.mau.fi/mautrix-signal/pkg/libsignalgo"
)

func TestLink(t *testing.T) {
	k, err := libsignalgo.GenerateIdentityKeyPair()
	if err != nil || k == nil {
		t.Fatal(err)
	}
}
