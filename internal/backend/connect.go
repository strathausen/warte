package backend

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"go.mau.fi/mautrix-signal/pkg/signalmeow"

	"warte/internal/core"
)

// connectLoop starts signalmeow's receive loops, retrying with backoff, and
// translates connection status changes into ConnChanged events.
func (b *Backend) connectLoop(cli *signalmeow.Client) {
	ctx := b.log.WithContext(b.ctx)
	defer b.stopLoops(cli)
	first := true
	for attempt := 0; ctx.Err() == nil; {
		b.loopMu.Lock()
		ch, err := cli.StartReceiveLoops(ctx)
		b.loopMu.Unlock()
		if err != nil {
			b.log.Warn().Err(err).Int("attempt", attempt).Msg("Failed to connect to Signal")
			b.setConn(core.ConnConnecting)
			delay := min(time.Duration(2<<min(attempt, 7))*time.Second, 150*time.Second)
			attempt++
			if !sleepCtx(ctx, delay) {
				return
			}
			continue
		}
		attempt = 0
		if first {
			first = false
			b.goBackground(func() { b.postConnect(cli) })
		}
		if !b.statusLoop(ctx, cli, ch) {
			return
		}
		// The loops stopped without us closing: restart them.
		b.stopLoops(cli)
		if !sleepCtx(ctx, 2*time.Second) {
			return
		}
	}
}

// statusLoop consumes connection status until the channel closes. It
// returns false if the backend should stop reconnecting.
func (b *Backend) statusLoop(ctx context.Context, cli *signalmeow.Client, ch <-chan signalmeow.SignalConnectionStatus) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		case st, ok := <-ch:
			if !ok {
				return ctx.Err() == nil && b.ConnState() != core.ConnLoggedOut
			}
			switch st.Event {
			case signalmeow.SignalConnectionEventConnected:
				b.setConn(core.ConnConnected)
				b.resumeStorageWrites()
			case signalmeow.SignalConnectionEventDisconnected, signalmeow.SignalConnectionEventError:
				b.setConn(core.ConnConnecting)
			case signalmeow.SignalConnectionEventFatalError:
				b.log.Error().Err(st.Err).Msg("Fatal Signal connection error")
				b.setConn(core.ConnDisconnected)
			case signalmeow.SignalConnectionEventLoggedOut:
				b.loggedOut(cli, st.Err)
				return false
			case signalmeow.SignalConnectionCleanShutdown:
				if !cli.IsLoggedIn() {
					b.loggedOut(cli, errors.New("device was unlinked"))
					return false
				}
				b.setConn(core.ConnDisconnected)
			}
		}
	}
}

// loggedOut handles the account unlinking this device remotely.
func (b *Backend) loggedOut(cli *signalmeow.Client, cause error) {
	if b.ConnState() == core.ConnLoggedOut {
		return
	}
	if cause == nil {
		cause = errors.New("logged out of Signal")
	}
	b.log.Warn().Err(cause).Msg("Logged out of Signal")
	b.setConn(core.ConnLoggedOut)
	b.emit(core.LoggedOut{Err: cause})
	b.dropPendingStorage() // queued changes belong to the old link
	// Forget the session keys and password (like signalmeow's
	// ClearKeysAndDisconnect); connectLoop stops the loops as it exits.
	ctx := context.Background()
	if err := cli.Store.ClearDeviceKeys(ctx); err != nil {
		b.log.Warn().Err(err).Msg("Failed to clear keys after logout")
	}
	if err := cli.Store.ClearPassword(ctx); err != nil {
		b.log.Warn().Err(err).Msg("Failed to clear password after logout")
	}
}

// stopLoops stops signalmeow's receive loops if they are running. It is
// safe to call repeatedly and concurrently.
func (b *Backend) stopLoops(cli *signalmeow.Client) {
	b.loopMu.Lock()
	defer b.loopMu.Unlock()
	if cli.AuthedWS == nil && cli.UnauthedWS == nil {
		return
	}
	if err := cli.StopReceiveLoops(); err != nil {
		b.log.Debug().Err(err).Msg("Error stopping receive loops")
	}
}

// postConnect runs once per Start after the first connection attempt:
// history transfer, storage sync and a full group refresh.
func (b *Backend) postConnect(cli *signalmeow.Client) {
	ctx := b.log.WithContext(b.ctx)
	if !b.waitConnected(ctx, 2*time.Minute) {
		return
	}
	// Storage changes saved by an earlier run (or made offline) go out
	// before the first storage read, so the remote state cannot undo them.
	if cli.Store.MasterKey != nil {
		b.replayPendingStorage(time.Minute)
	}
	if cli.SyncContactsOnConnect {
		_ = b.kv.set(ctx, "contacts_synced_at", b.now())
		b.emit(core.SyncProgress{Stage: "contacts"})
	}
	if cli.Store.EphemeralBackupKey != nil {
		if cli.Store.MasterKey != nil {
			cli.StorageSync(ctx)
		}
		b.importHistory(ctx, cli)
	}
	if cli.Store.MasterKey != nil {
		cli.StorageSync(ctx)
		b.applyStorage(ctx, cli)
	}
	b.refreshAllGroups(ctx, cli)
}

// waitConnected waits until the connection is up or timeout passes.
func (b *Backend) waitConnected(ctx context.Context, timeout time.Duration) bool {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for b.ConnState() != core.ConnConnected {
		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			return b.ConnState() == core.ConnConnected
		case <-tick.C:
		}
	}
	return true
}

// maintenanceLoop periodically re-reads the storage service, prunes expired
// stories and orphaned attachments.
func (b *Backend) maintenanceLoop() {
	ctx := b.log.WithContext(b.ctx)
	storageTick := time.NewTicker(5 * time.Minute)
	defer storageTick.Stop()
	cleanTick := time.NewTicker(time.Hour)
	defer cleanTick.Stop()
	b.cleanup(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-storageTick.C:
			if cli, err := b.client(); err == nil && cli.Store.MasterKey != nil && b.ConnState() == core.ConnConnected {
				b.applyStorage(ctx, cli)
			}
		case <-b.storageKick:
			if cli, err := b.client(); err == nil && cli.Store.MasterKey != nil {
				b.applyStorage(ctx, cli)
			}
		case <-cleanTick.C:
			b.cleanup(ctx)
		}
	}
}

// cleanup drops expired stories and unreferenced attachment data.
func (b *Backend) cleanup(ctx context.Context) {
	b.expireStories(ctx) // also removes story media
	if _, err := b.db.PruneAttachments(ctx, b.blobs); err != nil {
		b.log.Debug().Err(err).Msg("Failed to prune attachments")
	}
	if _, err := b.db.CollectGarbage(ctx, b.blobs, 24*time.Hour); err != nil {
		b.log.Debug().Err(err).Msg("Failed to collect blob garbage")
	}
}

// applyStorage reads the storage service and mirrors chat state (archived,
// pinned, muted, marked unread, blocked), usernames, verification, account
// settings and installed sticker packs into warte's store.
func (b *Backend) applyStorage(ctx context.Context, cli *signalmeow.Client) {
	if cli.Store.MasterKey == nil {
		return
	}
	// warte: never mirror remote state over local changes that are still
	// waiting to be written; the flush kicks a re-read when it is done.
	b.sw.applyMu.Lock()
	defer b.sw.applyMu.Unlock()
	if b.storageWritePending() {
		return
	}
	var known uint64
	_, _ = b.kv.get(ctx, "storage_version", &known)
	// FetchStorage returns nil when the manifest is not newer than known.
	upd, err := cli.FetchStorage(ctx, cli.Store.MasterKey, known, nil)
	if err != nil {
		b.log.Warn().Err(err).Msg("Failed to read storage service")
		return
	}
	if upd == nil || b.storageWritePending() {
		return
	}
	b.emit(core.SyncProgress{Stage: "storage"})
	snap := convertStorage(upd.NewRecords, b.selfACI())
	b.applyStorageSnapshot(ctx, snap)
	_ = b.kv.set(ctx, "storage_version", upd.Version)
	b.emit(core.SyncProgress{Stage: "storage", Done: 1, Total: 1})
}

// applyStorageSnapshot writes a converted storage read to the store.
func (b *Backend) applyStorageSnapshot(ctx context.Context, snap storageSnapshot) {
	self := b.selfACI()
	pinOrder := map[core.ChatID]int{}
	for i, id := range snap.Pinned {
		pinOrder[id] = i
	}
	seen := map[core.ChatID]bool{}
	for _, st := range snap.Chats {
		seen[st.Chat] = true
		c, err := b.db.GetChat(ctx, st.Chat)
		if errors.Is(err, core.ErrNotFound) {
			// Only materialise chats the user interacts with: groups and
			// pinned conversations. Plain contacts stay contacts.
			_, pinned := pinOrder[st.Chat]
			if !st.Chat.IsGroup() && !pinned {
				continue
			}
			c = core.Chat{ID: st.Chat, Kind: core.ChatDirect}
			if st.Chat.IsGroup() {
				c.Kind = core.ChatGroup
				b.queueGroupRefresh(st.Chat)
			}
		} else if err != nil {
			continue
		}
		before := c
		c.Archived, c.MarkedUnread, c.MutedUntil, c.Blocked = st.Archived, st.MarkedUnread, st.MutedUntil, st.Blocked
		if st.Whitelisted {
			c.MessageRequest = false
		}
		if i, ok := pinOrder[st.Chat]; ok {
			c.Pinned, c.PinOrder = true, i
		} else {
			c.Pinned, c.PinOrder = false, 0
		}
		if c.ID.IsDirect() && c.ID.ACI() == self {
			c.Kind = core.ChatNoteToSelf
		}
		if chatStateEqual(before, c) {
			continue
		}
		if err := b.db.UpsertChat(ctx, c); err == nil {
			b.emitChat(ctx, c.ID)
		}
	}
	// Pinned chats without a storage record of their own (rare).
	for id, i := range pinOrder {
		if seen[id] {
			continue
		}
		c, err := b.db.GetChat(ctx, id)
		if err != nil || (c.Pinned && c.PinOrder == i) {
			continue
		}
		c.Pinned, c.PinOrder = true, i
		if err := b.db.UpsertChat(ctx, c); err == nil {
			b.emitChat(ctx, id)
		}
	}
	for _, sc := range snap.Contacts {
		c, err := b.db.GetContact(ctx, sc.ACI)
		if errors.Is(err, core.ErrNotFound) {
			c = core.Contact{ACI: sc.ACI}
		} else if err != nil {
			continue
		}
		before := c
		if sc.Username != "" {
			c.Username = sc.Username
		}
		c.Verified = sc.Verified
		if c == before {
			continue
		}
		if err := b.db.UpsertContact(ctx, c); err == nil {
			b.emit(core.ContactUpdated{Contact: c})
		}
	}
	if snap.HasAccount {
		s, err := b.db.Settings(ctx)
		if err == nil {
			s.ReadReceipts = snap.Settings.ReadReceipts
			s.TypingIndicators = snap.Settings.TypingIndicators
			s.LinkPreviews = snap.Settings.LinkPreviews
			s.SealedSenderNotice = snap.Settings.SealedSenderIndicators
			s.DefaultExpireTimer = snap.Settings.UniversalExpireTimer
			s.PhoneNumberSharing = snap.Settings.PhoneNumberSharing
			s.DiscoverableByPhone = snap.Settings.DiscoverableByPhone
			_ = b.db.SaveSettings(ctx, s)
		}
		if u := snap.Settings.Username; u != "" {
			b.mu.Lock()
			b.self.Username = u
			self := b.self
			b.mu.Unlock()
			if err := b.db.UpsertContact(ctx, self); err == nil {
				b.emit(core.ContactUpdated{Contact: self})
			}
		}
	}
	if snap.HasAccount {
		// A full read (it always has the account record): mirror the
		// custom chat folders.
		b.syncFolders(ctx, snap.Folders)
	}
	for _, p := range snap.StickerPack {
		if p.Deleted {
			if err := b.db.SetStickerPackInstalled(ctx, p.ID, false); err != nil && !errors.Is(err, core.ErrNotFound) {
				b.log.Debug().Err(err).Msg("Failed to uninstall sticker pack from storage")
			}
			continue
		}
		existing, err := b.db.GetStickerPack(ctx, p.ID)
		if err == nil && existing.Installed {
			continue
		}
		pack := core.StickerPack{ID: p.ID, Key: p.Key, Installed: true}
		if err == nil {
			pack = existing
			pack.Installed = true
		}
		if err := b.db.PutStickerPack(ctx, pack); err == nil {
			b.goBackground(func() { _ = b.fetchStickerManifest(b.ctx, p.ID, p.Key, true) })
		}
	}
}

// syncFolders replaces the folders from the storage service with fs (in
// display order). Folders created otherwise are left alone.
func (b *Backend) syncFolders(ctx context.Context, fs []storageFolder) {
	existing, err := b.db.ListFolders(ctx)
	if err != nil {
		return
	}
	keep := map[string]bool{}
	ids := make([]string, 0, len(fs))
	for _, f := range fs {
		keep[f.Folder.ID] = true
		ids = append(ids, f.Folder.ID)
		if err := b.db.PutFolder(ctx, f.Folder); err != nil {
			b.log.Debug().Err(err).Msg("Failed to store chat folder")
		}
	}
	for _, f := range existing {
		if strings.HasPrefix(f.ID, storageFolderPrefix) && !keep[f.ID] {
			if err := b.db.DeleteFolder(ctx, f.ID); err != nil && !errors.Is(err, core.ErrNotFound) {
				b.log.Debug().Err(err).Msg("Failed to delete chat folder")
			}
		}
	}
	if len(ids) > 0 {
		if err := b.db.ReorderFolders(ctx, ids); err != nil {
			b.log.Debug().Err(err).Msg("Failed to order chat folders")
		}
	}
}

// chatStateEqual compares the storage-synced fields of two chats.
func chatStateEqual(a, b core.Chat) bool {
	return a.Archived == b.Archived && a.MarkedUnread == b.MarkedUnread && a.MutedUntil.Equal(b.MutedUntil) &&
		a.Blocked == b.Blocked && a.Pinned == b.Pinned && a.PinOrder == b.PinOrder &&
		a.MessageRequest == b.MessageRequest && a.Kind == b.Kind
}

// knownGroups lists the group IDs whose master keys signalmeow knows.
func (b *Backend) knownGroups(ctx context.Context, cli *signalmeow.Client) ([]core.ChatID, error) {
	rows, err := b.db.RawDB().QueryContext(ctx, `SELECT group_identifier FROM signalmeow_groups WHERE account_id = ?`, cli.Store.ACI.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.ChatID
	for rows.Next() {
		var gid string
		if err := rows.Scan(&gid); err != nil {
			return nil, err
		}
		out = append(out, core.GroupChatID(gid))
	}
	return out, rows.Err()
}

// refreshAllGroups queues every known group for a refresh.
func (b *Backend) refreshAllGroups(ctx context.Context, cli *signalmeow.Client) {
	ids, err := b.knownGroups(ctx, cli)
	if err != nil {
		b.log.Debug().Err(err).Msg("Failed to list groups")
		return
	}
	chats, _ := b.allChats(ctx)
	for _, c := range chats {
		if c.ID.IsGroup() && !slices.Contains(ids, c.ID) {
			ids = append(ids, c.ID)
		}
	}
	if len(ids) == 0 {
		// Total 0 means "unknown" to the frontend, which would show the
		// stage as running forever.
		b.emitSyncDone("groups", 0)
		return
	}
	b.emit(core.SyncProgress{Stage: "groups", Total: len(ids)})
	for _, id := range ids {
		b.queueGroupRefresh(id)
	}
}

// emitSyncDone announces that a sync stage finished after n items. Total 0
// means "unknown" in core.SyncProgress, so an empty stage reports 1/1.
func (b *Backend) emitSyncDone(stage string, n int) {
	n = max(n, 1)
	b.emit(core.SyncProgress{Stage: stage, Done: n, Total: n})
}

// allChats lists every chat, archived and blocked included.
func (b *Backend) allChats(ctx context.Context) ([]core.Chat, error) {
	var out []core.Chat
	for _, archived := range []bool{false, true} {
		cs, err := b.db.ListChats(ctx, core.ChatFilter{Archived: archived, IncludeEmpty: true, IncludeBlocked: true})
		if err != nil {
			return nil, err
		}
		out = append(out, cs...)
	}
	return out, nil
}

// sleepCtx sleeps for d or until ctx is done; it reports whether it slept.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
