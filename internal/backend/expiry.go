package backend

import (
	"context"
	"time"

	"github.com/rs/zerolog"

	"warte/internal/core"
)

// maxExpiryWait caps how long the expiry loop sleeps, so clock jumps and
// missed wake-ups are corrected within the hour.
const maxExpiryWait = time.Hour

// kickExpiry wakes the expiry loop (after a timer started).
func (b *Backend) kickExpiry() {
	select {
	case b.wakeExpiry <- struct{}{}:
	default:
	}
}

// expiryLoop deletes disappearing messages when their timers run out,
// unpins messages whose pin duration ended and removes stories after 24
// hours.
func (b *Backend) expiryLoop() {
	ctx := b.log.WithContext(b.ctx)
	for {
		next := b.sweepExpired(ctx)
		wait := maxExpiryWait
		if !next.IsZero() {
			wait = min(max(next.Sub(b.now()), 0), maxExpiryWait)
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-b.wakeExpiry:
		case <-t.C:
		}
		t.Stop()
	}
}

// sweepExpired removes everything due now and returns when the next item
// is due (zero if nothing is scheduled).
func (b *Backend) sweepExpired(ctx context.Context) time.Time {
	now := b.now()
	ids, err := b.db.ExpiringMessages(ctx, now)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to list expiring messages")
	}
	chats := map[core.ChatID]bool{}
	for _, id := range ids {
		if err := b.db.DeleteMessage(ctx, id); err != nil {
			continue
		}
		_ = b.index.forget(ctx, id)
		b.emit(core.MessageRemoved{ID: id})
		chats[id.Chat] = true
	}
	if len(ids) > 0 {
		if _, err := b.db.PruneAttachments(ctx, b.blobs); err != nil {
			zerolog.Ctx(ctx).Debug().Err(err).Msg("Failed to prune attachments")
		}
	}
	for c := range chats {
		b.emitChat(ctx, c)
	}

	var next time.Time
	if t, ok, err := b.db.NextExpiration(ctx); err == nil && ok {
		next = t
	}
	if t := b.expireStories(ctx); !t.IsZero() && (next.IsZero() || t.Before(next)) {
		next = t
	}
	pins := map[string]time.Time{}
	if ok, _ := b.kv.get(ctx, "pin_expiry", &pins); ok && len(pins) > 0 {
		changed := false
		for key, at := range pins {
			if at.After(now) {
				if next.IsZero() || at.Before(next) {
					next = at
				}
				continue
			}
			delete(pins, key)
			changed = true
			id, err := core.ParseMessageID(key)
			if err != nil {
				continue
			}
			if m, err := b.db.UpdateMessage(ctx, id, func(m *core.Message) error { m.Pinned = false; return nil }); err == nil {
				b.emit(core.MessageUpdated{Message: m})
				b.emitChat(ctx, id.Chat)
			}
		}
		if changed {
			_ = b.kv.set(ctx, "pin_expiry", pins)
		}
	}
	return next
}
