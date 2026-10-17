package backend

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"go.mau.fi/mautrix-signal/pkg/libsignalgo"
	"go.mau.fi/mautrix-signal/pkg/signalmeow"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/backuppb"
	mstore "go.mau.fi/mautrix-signal/pkg/signalmeow/store"

	"warte/internal/core"
	"warte/internal/store"
)

// historyPage is how many history items are imported per batch.
const historyPage = 500

// importHistory receives the message history the phone offers when linking
// (Signal's "transfer message history" option), imports it into warte's
// store and clears signalmeow's staging tables. The transfer archive is
// downloaded by signalmeow into a temporary file; it is encrypted and
// MAC-protected with the ephemeral backup key and deleted right after.
func (b *Backend) importHistory(ctx context.Context, cli *signalmeow.Client) {
	log := zerolog.Ctx(ctx).With().Str("action", "history transfer").Logger()
	ctx = log.WithContext(ctx)
	b.emit(core.SyncProgress{Stage: "history"})
	meta, err := cli.WaitForTransfer(ctx)
	if err != nil {
		if !errors.Is(err, signalmeow.ErrNoEphemeralBackupKey) {
			b.errorEvent("history", err)
		}
		b.emitSyncDone("history", 0)
		return
	}
	if meta.Error != "" {
		log.Info().Str("reason", meta.Error).Msg("Phone declined to transfer history")
		cli.Store.EphemeralBackupKey = nil
		if err := cli.Store.DeviceStore.PutDevice(ctx, &cli.Store.DeviceData); err != nil {
			log.Warn().Err(err).Msg("Failed to clear ephemeral backup key")
		}
		b.emitSyncDone("history", 0)
		return
	}
	if err := cli.FetchAndProcessTransfer(ctx, meta); err != nil {
		b.errorEvent("history", err)
		b.emitSyncDone("history", 0)
		return
	}
	if err := b.importBackupStore(ctx, cli); err != nil {
		b.errorEvent("history", err)
		b.emitSyncDone("history", 0)
		return
	}
	if err := cli.Store.BackupStore.ClearBackup(ctx); err != nil {
		log.Debug().Err(err).Msg("Failed to clear staged history")
	}
}

// importBackupStore copies staged history chats and messages into warte's
// store.
func (b *Backend) importBackupStore(ctx context.Context, cli *signalmeow.Client) error {
	chats, err := cli.Store.BackupStore.GetBackupChats(ctx)
	if err != nil {
		return err
	}
	total := 0
	for _, c := range chats {
		total += c.TotalMessages
	}
	self := b.selfACI()
	recipients := map[uint64]*backuppb.Recipient{}
	recipient := func(id uint64) *backuppb.Recipient {
		if r, ok := recipients[id]; ok {
			return r
		}
		r, err := cli.Store.BackupStore.GetBackupRecipient(ctx, id)
		if err != nil {
			r = nil
		}
		recipients[id] = r
		return r
	}
	aciOfRecipient := func(id uint64) core.ACI {
		switch d := recipient(id).GetDestination().(type) {
		case *backuppb.Recipient_Self:
			return self
		case *backuppb.Recipient_Contact:
			if len(d.Contact.GetAci()) == 16 {
				return aciOf(uuid.UUID(d.Contact.GetAci()))
			}
		}
		return ""
	}
	done := 0
	b.emit(core.SyncProgress{Stage: "history", Done: 0, Total: total})
	for _, bc := range chats {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rec := recipient(bc.RecipientId)
		chatID, peer, ok := backupChatID(rec, self)
		if !ok || bc.TotalMessages == 0 && !chatID.IsGroup() {
			continue
		}
		if err := b.importBackupChat(ctx, cli, chatID, bc, rec); err != nil {
			zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to import chat state")
			continue
		}
		bctx := backupCtx{Self: self, Chat: chatID, Peer: peer, ACIOf: aciOfRecipient, Name: b.names(ctx)}
		var anchor time.Time
		for {
			items, err := cli.Store.BackupStore.GetBackupChatItems(ctx, bc.Id, anchor, false, historyPage)
			if err != nil {
				return err
			}
			if len(items) == 0 {
				break
			}
			oldest := items[0].GetDateSent()
			for _, it := range items {
				conv, ok := convertBackupItem(bctx, it)
				if ok {
					if err := b.importBackupMessage(ctx, conv); err != nil {
						zerolog.Ctx(ctx).Debug().Err(err).Msg("Failed to import history item")
					}
				}
				oldest = min(oldest, it.GetDateSent())
				done++
			}
			// Page strictly before the oldest item, but re-read its
			// millisecond once so same-timestamp items from other senders
			// on the page boundary are not skipped (duplicates are no-ops).
			next := time.UnixMilli(int64(oldest) + 1)
			if !anchor.IsZero() && !next.Before(anchor) {
				next = time.UnixMilli(int64(oldest))
			}
			anchor = next
			b.emit(core.SyncProgress{Stage: "history", Done: min(done, total), Total: total})
		}
		b.emitChat(ctx, chatID)
	}
	b.emitSyncDone("history", total)
	return nil
}

// backupChatID maps a backup recipient to a chat (and the peer of a direct
// chat).
func backupChatID(r *backuppb.Recipient, self core.ACI) (core.ChatID, core.ACI, bool) {
	switch d := r.GetDestination().(type) {
	case *backuppb.Recipient_Self:
		return core.DirectChatID(self), self, self != ""
	case *backuppb.Recipient_Contact:
		if len(d.Contact.GetAci()) != 16 {
			return "", "", false
		}
		aci := aciOf(uuid.UUID(d.Contact.GetAci()))
		return core.DirectChatID(aci), aci, aci != ""
	case *backuppb.Recipient_Group:
		mk := d.Group.GetMasterKey()
		if len(mk) != libsignalgo.GroupMasterKeyLength {
			return "", "", false
		}
		gid, err := libsignalgo.GroupMasterKey(mk).GroupIdentifier()
		if err != nil {
			return "", "", false
		}
		return core.GroupChatID(base64.StdEncoding.EncodeToString(gid[:])), "", true
	}
	return "", "", false
}

// backupChatState applies a backup chat's settings to c.
func backupChatState(c *core.Chat, bc *backuppb.Chat) {
	c.Archived = bc.GetArchived()
	c.MarkedUnread = bc.GetMarkedUnread()
	if bc.PinnedOrder != nil && bc.GetPinnedOrder() > 0 {
		c.Pinned, c.PinOrder = true, int(bc.GetPinnedOrder())
	}
	c.MutedUntil = mutedUntil(bc.GetMuteUntilMs())
	if ms := bc.GetExpirationTimerMs(); ms > 0 {
		c.ExpireTimer = time.Duration(ms) * time.Millisecond
	}
}

// importBackupChat creates or updates the chat and contact of a backup chat.
func (b *Backend) importBackupChat(ctx context.Context, cli *signalmeow.Client, id core.ChatID, bc *mstore.BackupChat, rec *backuppb.Recipient) error {
	c, err := b.db.GetChat(ctx, id)
	if errors.Is(err, core.ErrNotFound) {
		c = core.Chat{ID: id, Kind: core.ChatDirect}
	} else if err != nil {
		return err
	}
	backupChatState(&c, bc.Chat)
	switch d := rec.GetDestination().(type) {
	case *backuppb.Recipient_Self:
		c.Kind, c.Title = core.ChatNoteToSelf, "Note to Self"
	case *backuppb.Recipient_Group:
		c.Kind = core.ChatGroup
		if t := d.Group.GetSnapshot().GetTitle().GetTitle(); t != "" && c.Title == "" {
			c.Title = strings.TrimSpace(t)
		}
		c.Blocked = d.Group.GetBlocked()
		b.queueGroupRefresh(id)
	case *backuppb.Recipient_Contact:
		ct := d.Contact
		c.Blocked = ct.GetBlocked()
		aci := aciOf(uuid.UUID(ct.GetAci()))
		if r, err := cli.Store.RecipientStore.LoadAndUpdateRecipient(ctx, uuid.UUID(ct.GetAci()), uuid.Nil, nil); err == nil && r != nil {
			_ = b.mergeRecipient(ctx, r)
		}
		if contact, err := b.db.GetContact(ctx, aci); err == nil {
			upd := contact
			if ct.GetUsername() != "" {
				upd.Username = ct.GetUsername()
			}
			if n := strings.TrimSpace(ct.GetNickname().GetGiven() + " " + ct.GetNickname().GetFamily()); n != "" {
				upd.Nickname = n
			}
			if n := strings.TrimSpace(ct.GetSystemGivenName() + " " + ct.GetSystemFamilyName()); n != "" && upd.ContactName == "" {
				upd.ContactName = n
			}
			upd.Verified = ct.GetIdentityState() == backuppb.Contact_VERIFIED
			if upd != contact {
				_ = b.db.UpsertContact(ctx, upd)
			}
		}
		b.queueProfile(aci)
	}
	return b.db.UpsertChat(ctx, c)
}

// importBackupMessage stores one converted history item unless a live copy
// already exists.
func (b *Backend) importBackupMessage(ctx context.Context, it backupItem) error {
	m := it.Msg
	if _, err := b.db.GetMessage(ctx, m.ID); err == nil {
		return nil
	}
	if err := b.persistAttachments(ctx, m.ID, it.Atts); err != nil {
		return err
	}
	var opts []store.PutOption
	if it.Read {
		opts = append(opts, store.AsRead())
	}
	if _, err := b.db.PutMessage(ctx, m, opts...); err != nil {
		return err
	}
	if it.LongText != nil {
		b.queueDownload(downloadJob{msg: m.ID, ref: *it.LongText})
	}
	return b.index.put(ctx, m.ID, 0, "", nil)
}
