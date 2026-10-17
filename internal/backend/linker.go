package backend

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"go.mau.fi/mautrix-signal/pkg/signalmeow"
	mstore "go.mau.fi/mautrix-signal/pkg/signalmeow/store"

	"warte/internal/core"
	"warte/internal/store"
)

var _ core.Linker = (*Linker)(nil)

const (
	// qrRefreshInterval matches Signal Desktop: the server drops an unused
	// provisioning socket after a minute, so a new QR is shown every 45 s.
	qrRefreshInterval = 45 * time.Second
	// maxQRRefreshes bounds how long linking waits for a scan (~4 minutes).
	maxQRRefreshes = 5
)

// Link progress stages reported as core.LinkProgress.
const (
	StageWaitingForScan = "Scan the QR code with Signal on your phone (Settings › Linked devices)"
	StageLinking        = "Linking…"
	StageSaving         = "Saving keys…"
	StageHistory        = "Your phone will now send recent message history; it is imported after warte starts"
)

// Linker links warte as a new secondary device of an existing Signal
// account. The device keys land in signalmeow's tables of warte's database.
type Linker struct {
	opts Options
	log  zerolog.Logger
}

// NewLinker returns a Linker for the given storage options (the same ones
// New uses).
func NewLinker(opts Options) *Linker {
	opts.Logger = privateLogger(opts.Logger)
	return &Linker{opts: opts, log: opts.Logger.With().Str("component", "linker").Logger()}
}

// Link starts provisioning. The channel yields LinkQR (again every 45
// seconds while unscanned), LinkProgress updates, then exactly one of
// LinkDone or LinkError, and is closed.
func (l *Linker) Link(ctx context.Context, deviceName string) (<-chan core.LinkEvent, error) {
	if deviceName == "" {
		deviceName = l.opts.DeviceName
	}
	if deviceName == "" {
		deviceName = DefaultDeviceName
	}
	db, ownDB, err := openDB(l.opts)
	if err != nil {
		return nil, err
	}
	container, err := openContainer(ctx, db, l.opts.Logger)
	if err != nil {
		if ownDB {
			_ = db.Close()
		}
		return nil, err
	}
	out := make(chan core.LinkEvent, 8)
	go func() {
		defer close(out)
		defer func() {
			if ownDB {
				_ = db.Close()
			}
		}()
		send := func(ev core.LinkEvent) bool {
			select {
			case out <- ev:
				return true
			case <-ctx.Done():
				return false
			}
		}
		self, err := l.run(ctx, container, db, deviceName, send)
		if err != nil {
			select {
			case out <- core.LinkError{Err: err}:
			case <-time.After(5 * time.Second):
			}
			return
		}
		send(core.LinkDone{Self: self})
	}()
	return out, nil
}

// run drives provisioning attempts until one completes.
func (l *Linker) run(ctx context.Context, container *mstore.Container, db *store.DB, name string, send func(core.LinkEvent) bool) (core.Contact, error) {
	log := l.log
	ctx = log.WithContext(ctx)
	for attempt := 0; attempt < maxQRRefreshes; attempt++ {
		provCtx, cancel := context.WithCancel(ctx)
		ch := signalmeow.PerformProvisioning(provCtx, container, name, true)
		resp, ok := recvProv(ctx, ch, 0)
		if !ok {
			cancel()
			return core.Contact{}, ctxErr(ctx)
		}
		if resp.Err != nil || resp.State != signalmeow.StateProvisioningURLReceived {
			cancel()
			return core.Contact{}, linkError(resp.Err, resp.State)
		}
		if !send(core.LinkQR{URL: resp.ProvisioningURL}) || !send(core.LinkProgress{Stage: StageWaitingForScan}) {
			cancel()
			return core.Contact{}, ctxErr(ctx)
		}
		resp, ok = recvProv(ctx, ch, qrRefreshInterval)
		if !ok {
			cancel()
			if ctx.Err() != nil {
				return core.Contact{}, ctx.Err()
			}
			log.Debug().Int("attempt", attempt).Msg("QR code expired, requesting a new one")
			continue
		}
		if resp.Err != nil || resp.State != signalmeow.StateProvisioningDataReceived {
			cancel()
			return core.Contact{}, linkError(resp.Err, resp.State)
		}
		send(core.LinkProgress{Stage: StageSaving})
		data := resp.ProvisioningData
		cancel()
		if data == nil || data.ACI == uuid.Nil {
			return core.Contact{}, errors.New("linking failed: no account ID received")
		}
		self := core.Contact{ACI: aciOf(data.ACI), E164: data.Number, IsSelf: true}
		if existing, err := db.GetContact(ctx, self.ACI); err == nil {
			existing.IsSelf = true
			if self.E164 != "" {
				existing.E164 = self.E164
			}
			self = existing
		}
		if err := db.UpsertContact(ctx, self); err != nil {
			return core.Contact{}, err
		}
		if data.EphemeralBackupKey != nil {
			send(core.LinkProgress{Stage: StageHistory})
		}
		log.Info().Msg("Device linked")
		return self, nil
	}
	return core.Contact{}, errors.New("linking timed out: the QR code was not scanned")
}

// recvProv receives one provisioning response; timeout 0 waits for ctx.
func recvProv(ctx context.Context, ch <-chan signalmeow.ProvisioningResponse, timeout time.Duration) (signalmeow.ProvisioningResponse, bool) {
	var tc <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		tc = t.C
	}
	select {
	case r, ok := <-ch:
		if !ok {
			return signalmeow.ProvisioningResponse{Err: errors.New("linking stopped unexpectedly")}, true
		}
		return r, true
	case <-tc:
		return signalmeow.ProvisioningResponse{}, false
	case <-ctx.Done():
		return signalmeow.ProvisioningResponse{}, false
	}
}

func ctxErr(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return context.Canceled
}

// linkError explains provisioning failures in user terms, following the
// status codes of Signal-Server's device link endpoint.
func linkError(err error, state signalmeow.ProvisioningState) error {
	if err == nil {
		return fmt.Errorf("linking failed (unexpected state %v)", state)
	}
	var dle signalmeow.DeviceLinkError
	if errors.As(err, &dle) {
		switch dle.StatusCode {
		case http.StatusConflict:
			return fmt.Errorf("linking failed: your Signal app is too old for this client, update it: %w", err)
		case http.StatusLengthRequired:
			return fmt.Errorf("linking failed: this account already has the maximum number of linked devices: %w", err)
		case http.StatusForbidden:
			return fmt.Errorf("linking failed: the QR code was invalid or already used, try again: %w", err)
		case http.StatusTooManyRequests:
			return fmt.Errorf("linking failed: Signal rate-limited linking, wait a few minutes: %w", err)
		}
	}
	return fmt.Errorf("linking failed: %w", err)
}
