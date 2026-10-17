package backend

import (
	"context"
	"encoding/hex"
	"errors"

	"github.com/rs/zerolog"

	"go.mau.fi/mautrix-signal/pkg/signalmeow/events"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"

	"warte/internal/core"
)

// Sync messages from our other devices that upstream signalmeow drops and
// the warte fork surfaces (see fork/CHANGES.md).

// blockedDiff computes which contacts and groups a blocked-list sync blocks
// and unblocks, given what is blocked locally. The sync carries the full
// list, so anything blocked locally but missing from it is unblocked.
func blockedDiff(syncACIs []core.ACI, syncGroups []core.ChatID, localACIs []core.ACI, localGroups []core.ChatID) (block, unblock []core.ChatID) {
	want := map[core.ChatID]bool{}
	for _, a := range syncACIs {
		want[core.DirectChatID(a)] = true
	}
	for _, g := range syncGroups {
		want[g] = true
	}
	have := map[core.ChatID]bool{}
	for _, a := range localACIs {
		have[core.DirectChatID(a)] = true
	}
	for _, g := range localGroups {
		have[g] = true
	}
	for _, list := range [][]core.ChatID{chatIDsOf(syncACIs), syncGroups} {
		for _, id := range list {
			if !have[id] {
				block = append(block, id)
				have[id] = true
			}
		}
	}
	for _, list := range [][]core.ChatID{chatIDsOf(localACIs), localGroups} {
		for _, id := range list {
			if !want[id] {
				unblock = append(unblock, id)
				want[id] = true // once
			}
		}
	}
	return block, unblock
}

func chatIDsOf(acis []core.ACI) []core.ChatID {
	out := make([]core.ChatID, 0, len(acis))
	for _, a := range acis {
		out = append(out, core.DirectChatID(a))
	}
	return out
}

// onBlockedSync applies the blocked list from our primary device to
// contacts and chats. (The fork already updated signalmeow's recipients.)
func (b *Backend) onBlockedSync(ctx context.Context, e *events.BlockedSync) error {
	var acis []core.ACI
	for _, u := range e.ACIs {
		acis = append(acis, aciOf(u))
	}
	if len(e.E164s) > 0 {
		cs, err := b.db.ListContacts(ctx)
		if err != nil {
			return err
		}
		byPhone := map[string]core.ACI{}
		for _, c := range cs {
			if c.E164 != "" {
				byPhone[c.E164] = c.ACI
			}
		}
		for _, n := range e.E164s {
			if a, ok := byPhone[n]; ok {
				acis = append(acis, a)
			}
		}
	}
	var groups []core.ChatID
	for _, g := range e.GroupIDs {
		groups = append(groups, groupChatID(g))
	}
	localACIs, localGroups, err := b.blockedList(ctx)
	if err != nil {
		return err
	}
	block, unblock := blockedDiff(acis, groups, localACIs, localGroups)
	apply := func(id core.ChatID, blocked bool) error {
		if id.IsDirect() {
			if err := b.setContactBlocked(ctx, id.ACI(), blocked); err != nil {
				return err
			}
		}
		_, err := b.db.UpdateChat(ctx, id, func(c *core.Chat) error {
			c.Blocked = blocked
			if !blocked {
				c.MessageRequest = false
			}
			return nil
		})
		if err != nil {
			return ignoreNotFound(err)
		}
		b.emitChat(ctx, id)
		return nil
	}
	for _, id := range block {
		if err := apply(id, true); err != nil {
			return err
		}
	}
	for _, id := range unblock {
		if err := apply(id, false); err != nil {
			return err
		}
	}
	if len(block)+len(unblock) > 0 {
		b.emit(core.StoriesChanged{}) // blocked authors' stories are hidden
	}
	return nil
}

// applyConfiguration merges a configuration sync into the settings. It
// reports whether anything changed.
func applyConfiguration(s *core.Settings, e *events.ConfigurationSync) bool {
	before := *s
	if e.ReadReceipts != nil {
		s.ReadReceipts = *e.ReadReceipts
	}
	if e.TypingIndicators != nil {
		s.TypingIndicators = *e.TypingIndicators
	}
	if e.LinkPreviews != nil {
		s.LinkPreviews = *e.LinkPreviews
	}
	if e.UnidentifiedDeliveryIndicators != nil {
		s.SealedSenderNotice = *e.UnidentifiedDeliveryIndicators
	}
	return *s != before
}

// onConfigurationSync applies settings changed on another device.
func (b *Backend) onConfigurationSync(ctx context.Context, e *events.ConfigurationSync) error {
	s, err := b.db.Settings(ctx)
	if err != nil {
		return err
	}
	if !applyConfiguration(&s, e) {
		return nil
	}
	if err := b.db.SaveSettings(ctx, s); err != nil {
		return err
	}
	if cli, err := b.client(); err == nil {
		applySettingsToAccount(cli, s)
	}
	return nil
}

// onStickerPackSync installs or removes sticker packs as our other device
// did. Installs fetch the manifest in the background.
func (b *Backend) onStickerPackSync(ctx context.Context, e *events.StickerPackSync) error {
	for _, op := range e.Operations {
		id := hex.EncodeToString(op.PackID)
		if !op.Install {
			if err := b.db.SetStickerPackInstalled(ctx, id, false); err != nil && !errors.Is(err, core.ErrNotFound) {
				return err
			}
			continue
		}
		if len(op.PackKey) != 32 {
			continue
		}
		key := hex.EncodeToString(op.PackKey)
		pack, err := b.db.GetStickerPack(ctx, id)
		switch {
		case errors.Is(err, core.ErrNotFound):
			pack = core.StickerPack{ID: id, Key: key}
		case err != nil:
			return err
		case pack.Installed:
			continue
		}
		pack.Installed = true
		if pack.Key == "" {
			pack.Key = key
		}
		if err := b.db.PutStickerPack(ctx, pack); err != nil {
			return err
		}
		if len(pack.Stickers) == 0 {
			b.goBackground(func() {
				if err := b.fetchStickerManifest(b.ctx, id, key, true); err != nil {
					b.log.Debug().Err(err).Msg("Failed to fetch synced sticker pack")
				}
			})
		}
	}
	return nil
}

// onViewedSync marks stories viewed on another of our devices as viewed
// here (no receipts: the other device sent them). Viewed syncs for
// messages (voice notes, media) need no local change.
func (b *Backend) onViewedSync(ctx context.Context, e *events.ViewedSync) error {
	changed := false
	for _, v := range e.Messages {
		id := core.MessageID{Author: aciOf(v.Sender), Timestamp: v.Timestamp}
		s, err := b.db.GetStory(ctx, id)
		if err != nil || s.Viewed {
			continue
		}
		if err := b.db.MarkStoryViewed(ctx, id); err == nil {
			changed = true
		}
	}
	if changed {
		b.emit(core.StoriesChanged{})
	}
	return nil
}

// kickStorage asks the maintenance loop to re-read the storage service.
func (b *Backend) kickStorage() {
	select {
	case b.storageKick <- struct{}{}:
	default:
	}
}

// onKeysSync re-reads the storage service after the primary device sent
// (new) account keys. A changed master key invalidates the known manifest
// version.
func (b *Backend) onKeysSync(ctx context.Context, e *events.KeysSync) error {
	if e.MasterKeyChanged {
		_ = b.kv.del(ctx, "storage_version")
	}
	b.kickStorage()
	return nil
}

// onFetchLatest refetches what another device says changed.
func (b *Backend) onFetchLatest(ctx context.Context, e *events.FetchLatest) error {
	switch e.Type {
	case signalpb.SyncMessage_FetchLatest_LOCAL_PROFILE:
		if self := b.selfACI(); self != "" {
			b.queueProfile(self)
		}
	case signalpb.SyncMessage_FetchLatest_STORAGE_MANIFEST:
		b.kickStorage()
	default:
		zerolog.Ctx(ctx).Debug().Stringer("type", e.Type).Msg("Ignoring fetch-latest request")
	}
	return nil
}

// onPniChangeNumber records our new phone number. signalmeow does not
// adopt the new PNI identity; Signal keeps working through the ACI.
func (b *Backend) onPniChangeNumber(ctx context.Context, e *events.PniChangeNumber) error {
	zerolog.Ctx(ctx).Info().Msg("Phone number changed on the primary device")
	if e.NewE164 == "" {
		return nil
	}
	b.mu.Lock()
	b.self.E164 = e.NewE164
	self := b.self
	b.mu.Unlock()
	if self.ACI == "" {
		return nil
	}
	if err := b.db.UpsertContact(ctx, self); err != nil {
		return err
	}
	b.emit(core.ContactUpdated{Contact: self})
	return nil
}

// deleteConversationSync applies a conversation delete from another device
// the way Signal-Desktop does (deleteForMe.deleteConversation): messages up
// to the newest one the other device knew are deleted, so messages that
// arrived since survive; with isFullDelete the chat itself goes too, but
// only if nothing is left.
func (b *Backend) deleteConversationSync(ctx context.Context, chat core.ChatID, cd *signalpb.SyncMessage_DeleteForMe_ConversationDelete) error {
	newest, found := b.newestAddressed(ctx, chat, cd.GetMostRecentMessages())
	if !found {
		newest, found = b.newestAddressed(ctx, chat, cd.GetMostRecentNonExpiringMessages())
	}
	if found {
		if err := b.deleteMessagesUpTo(ctx, newest); err != nil {
			return err
		}
	}
	if !cd.GetIsFullDelete() {
		b.emitChat(ctx, chat)
		return nil
	}
	left, err := b.db.ListMessages(ctx, chat, core.MessageID{}, 1)
	if err != nil {
		return err
	}
	if len(left) > 0 {
		b.emitChat(ctx, chat)
		return nil
	}
	return b.deleteChatLocal(ctx, chat)
}

// newestAddressed returns the newest stored message among ams.
func (b *Backend) newestAddressed(ctx context.Context, chat core.ChatID, ams []*signalpb.AddressableMessage) (core.MessageID, bool) {
	var best core.Message
	found := false
	for _, am := range ams {
		id, ok := addressableID(chat, am)
		if !ok {
			continue
		}
		target, ok := b.resolveTarget(ctx, chat, id.Author, id.Timestamp)
		if !ok {
			continue
		}
		m, err := b.db.GetMessage(ctx, target)
		if err != nil {
			continue
		}
		if !found || m.SentAt.After(best.SentAt) {
			best, found = m, true
		}
	}
	return best.ID, found
}

// deleteMessagesUpTo deletes newest and every older message of its chat.
func (b *Backend) deleteMessagesUpTo(ctx context.Context, newest core.MessageID) error {
	ids := []core.MessageID{newest}
	for before := newest; ; {
		page, err := b.db.ListMessages(ctx, newest.Chat, before, 500)
		if err != nil {
			return err
		}
		if len(page) == 0 {
			break
		}
		for _, m := range page {
			ids = append(ids, m.ID)
		}
		before = page[0].ID
	}
	for _, id := range ids {
		if err := b.db.DeleteMessage(ctx, id); err != nil && !errors.Is(err, core.ErrNotFound) {
			return err
		}
		_ = b.index.forget(ctx, id)
		b.emit(core.MessageRemoved{ID: id})
	}
	if _, err := b.db.PruneAttachments(ctx, b.blobs); err != nil {
		zerolog.Ctx(ctx).Debug().Err(err).Msg("Failed to prune attachments")
	}
	return nil
}
