package fake

import (
	"context"
	"encoding/base64"
	"math/rand/v2"
	"net/url"
	"time"

	"warte/internal/core"
)

// DefaultLinkDelay is the simulated time until the phone "scans" the QR code.
const DefaultLinkDelay = 3 * time.Second

// Linker is a fake core.Linker. The zero value is ready to use.
//
// Link emits a LinkQR with a realistic sgnl://linkdevice URL, then after
// Delay two LinkProgress stages, then LinkDone (or LinkError if Err is set or
// ctx is cancelled), and closes the channel.
type Linker struct {
	// Delay before the code counts as scanned; the sync stages that follow
	// take a third of it each. Zero uses DefaultLinkDelay; negative is instant.
	Delay time.Duration
	// Seed makes the generated provisioning URL deterministic.
	Seed int64
	// Err, if set, is delivered as LinkError instead of LinkDone.
	Err error
	// Self is the account returned in LinkDone. Zero uses the demo account.
	Self core.Contact
}

// Link starts simulated provisioning.
func (l *Linker) Link(ctx context.Context, deviceName string) (<-chan core.LinkEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	delay := orDefault(l.Delay, DefaultLinkDelay)
	self := l.Self
	if self.ACI == "" {
		self = core.Contact{
			ACI: SelfACI, E164: "+4915112340000", Username: "robin.07",
			ProfileName: selfGiven + " " + selfFamily, IsSelf: true,
		}
	}
	linkErr := l.Err
	qr := provisioningURL(l.Seed)

	// Buffered for every event we can send, so the goroutine never blocks on
	// an absent reader and always exits.
	ch := make(chan core.LinkEvent, 5)
	go func() {
		defer close(ch)
		ch <- core.LinkQR{URL: qr}
		pause := func(d time.Duration) bool {
			if d <= 0 {
				return ctx.Err() == nil
			}
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-t.C:
				return true
			case <-ctx.Done():
				return false
			}
		}
		stages := []struct {
			wait  time.Duration
			stage string
		}{
			{delay, "Scanned — confirming on your phone"},
			{delay / 3, "Syncing contacts and groups"},
		}
		for _, st := range stages {
			if !pause(st.wait) {
				ch <- core.LinkError{Err: ctx.Err()}
				return
			}
			ch <- core.LinkProgress{Stage: st.stage}
		}
		if !pause(delay / 3) {
			ch <- core.LinkError{Err: ctx.Err()}
			return
		}
		if linkErr != nil {
			ch <- core.LinkError{Err: linkErr}
			return
		}
		ch <- core.LinkDone{Self: self}
	}()
	return ch, nil
}

// provisioningURL builds a URL shaped like Signal's device-link QR payload:
// a base64 provisioning address and a 33-byte Curve25519 public key (0x05
// type prefix), URL-encoded.
func provisioningURL(seed int64) string {
	r := rand.New(rand.NewPCG(uint64(seed), 0x6c696e6b))
	addr := make([]byte, 16)
	for i := range addr {
		addr[i] = byte(r.UintN(256))
	}
	pub := make([]byte, 33)
	pub[0] = 0x05
	for i := 1; i < len(pub); i++ {
		pub[i] = byte(r.UintN(256))
	}
	return "sgnl://linkdevice?uuid=" + url.QueryEscape(base64.RawURLEncoding.EncodeToString(addr)) +
		"&pub_key=" + url.QueryEscape(base64.StdEncoding.EncodeToString(pub))
}
