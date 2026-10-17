package backend

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"go.mau.fi/util/ptr"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-signal/pkg/libsignalgo"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/events"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/types"

	"warte/internal/core"
	"warte/internal/store"
)

// typingTimeout is how long a "started typing" indicator lasts without a
// refresh, as in Signal Desktop.
const typingTimeout = 15 * time.Second

// handleSignalEvent is signalmeow's event callback. It runs on the receive
// loop, so it only does local work (store writes, queueing background jobs)
// and never blocks on the frontend. Returning false makes signalmeow keep
// the envelope for redelivery, which we only want for transient failures.
func (b *Backend) handleSignalEvent(evt events.SignalEvent) (handled bool) {
	b.handleMu.Lock()
	defer b.handleMu.Unlock()
	// A panic on malformed input must not take the client down, nor make
	// signalmeow redeliver the poisonous envelope forever: drop it.
	defer func() {
		if v := recover(); v != nil {
			_ = b.recovered("handle "+eventName(evt), v)
			handled = true
		}
	}()
	ctx := b.ctx
	if ctx.Err() != nil {
		return false
	}
	log := b.log.With().Str("handler", eventName(evt)).Logger()
	ctx = log.WithContext(ctx)
	var err error
	switch e := evt.(type) {
	case *events.ChatEvent:
		err = b.onChatEvent(ctx, e)
	case *events.DecryptionError:
		err = b.onDecryptionError(ctx, e)
	case *events.Receipt:
		err = b.onReceipt(ctx, e)
	case *events.ViewOnceOpen:
		err = b.onViewOnceOpen(ctx, e)
	case *events.ReadSelf:
		err = b.onReadSelf(ctx, e)
	case *events.Call:
		err = b.onCall(ctx, e)
	case *events.ContactList:
		err = b.onContactList(ctx, e)
	case *events.DeleteForMe:
		err = b.onDeleteForMe(ctx, e)
	case *events.MessageRequestResponse:
		err = b.onMessageRequestResponse(ctx, e)
	case *events.IdentityChange:
		err = b.onIdentityChange(ctx, e)
	case *events.QueueEmpty:
		// The backlog is drained; nothing to announce.
	case *events.LoggedOut:
		if cli, cerr := b.client(); cerr == nil {
			b.loggedOut(cli, e.Error)
		}
	case *events.ACIFound:
		// Chats are keyed by ACI already; nothing to merge.
	case *events.Story:
		err = b.onStory(ctx, e)
	case *events.StoryRecipientUpdate:
		err = b.onStoryRecipientUpdate(ctx, e)
	case *events.BlockedSync:
		err = b.onBlockedSync(ctx, e)
	case *events.ConfigurationSync:
		err = b.onConfigurationSync(ctx, e)
	case *events.StickerPackSync:
		err = b.onStickerPackSync(ctx, e)
	case *events.ViewedSync:
		err = b.onViewedSync(ctx, e)
	case *events.CallEventSync:
		err = b.onCallEventSync(ctx, e)
	case *events.CallLogEventSync:
		err = b.onCallLogEventSync(ctx, e)
	case *events.KeysSync:
		err = b.onKeysSync(ctx, e)
	case *events.FetchLatest:
		err = b.onFetchLatest(ctx, e)
	case *events.PniChangeNumber:
		err = b.onPniChangeNumber(ctx, e)
	}
	if err != nil {
		if isTransient(err) {
			log.Warn().Err(err).Msg("Transient failure handling Signal event, will retry")
			return false
		}
		log.Warn().Err(err).Msg("Dropping Signal event that could not be handled")
	}
	return true
}

func eventName(evt events.SignalEvent) string {
	switch evt.(type) {
	case *events.ChatEvent:
		return "chat"
	case *events.DecryptionError:
		return "decryption_error"
	case *events.Receipt:
		return "receipt"
	case *events.ViewOnceOpen:
		return "view_once_open"
	case *events.ReadSelf:
		return "read_self"
	case *events.Call:
		return "call"
	case *events.ContactList:
		return "contact_list"
	case *events.DeleteForMe:
		return "delete_for_me"
	case *events.MessageRequestResponse:
		return "message_request_response"
	case *events.IdentityChange:
		return "identity_change"
	case *events.QueueEmpty:
		return "queue_empty"
	case *events.LoggedOut:
		return "logged_out"
	case *events.Story:
		return "story"
	case *events.StoryRecipientUpdate:
		return "story_recipient_update"
	case *events.BlockedSync:
		return "blocked_sync"
	case *events.ConfigurationSync:
		return "configuration_sync"
	case *events.StickerPackSync:
		return "sticker_pack_sync"
	case *events.ViewedSync:
		return "viewed_sync"
	case *events.CallEventSync:
		return "call_event_sync"
	case *events.CallLogEventSync:
		return "call_log_event_sync"
	case *events.KeysSync:
		return "keys_sync"
	case *events.FetchLatest:
		return "fetch_latest"
	case *events.PniChangeNumber:
		return "pni_change_number"
	}
	return "other"
}

// isTransient reports whether a handler error is worth a redelivery.
func isTransient(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if errors.Is(err, errBadID) || errors.Is(err, core.ErrNotFound) {
		return false
	}
	return containsAny(err.Error(), "database is locked", "SQLITE_BUSY", "disk I/O error")
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// resolveChat maps an event's ChatID to a chat, resolving PNI-only
// destinations through signalmeow's recipient store.
func (b *Backend) resolveChat(ctx context.Context, raw string) (core.ChatID, error) {
	chat, pni, err := eventChatID(raw)
	if err != nil {
		return "", err
	}
	if pni == uuid.Nil {
		return chat, nil
	}
	cli, err := b.client()
	if err != nil {
		return "", err
	}
	rec, err := cli.Store.RecipientStore.LoadAndUpdateRecipient(ctx, uuid.Nil, pni, nil)
	if err != nil {
		return "", err
	}
	if rec == nil || rec.ACI == uuid.Nil {
		return "", errorf(errBadID, "no ACI known for PNI destination")
	}
	return core.DirectChatID(aciOf(rec.ACI)), nil
}

func (b *Backend) onChatEvent(ctx context.Context, e *events.ChatEvent) error {
	chat, err := b.resolveChat(ctx, e.Info.ChatID)
	if err != nil {
		return err
	}
	cc := convCtx{
		Self:       b.selfACI(),
		Chat:       chat,
		Sender:     aciOf(e.Info.Sender),
		ServerTS:   e.Info.ServerTimestamp,
		ReceivedAt: b.now().UTC(),
	}
	switch ev := e.Event.(type) {
	case *signalpb.DataMessage:
		return b.onDataMessage(ctx, cc, e.Info, ev)
	case *signalpb.EditMessage:
		return b.onEditMessage(ctx, cc, e.Info, ev)
	case *signalpb.TypingMessage:
		b.onTyping(ctx, cc, ev)
	}
	return nil
}

// ensureChat makes sure the chat of an incoming event exists and is up to
// date enough. It returns the stored chat.
func (b *Backend) ensureChat(ctx context.Context, cc convCtx, groupRev uint32) (core.Chat, error) {
	c, err := b.db.GetChat(ctx, cc.Chat)
	switch {
	case err == nil:
		if cc.Chat.IsGroup() && groupRev > c.GroupRevision {
			b.queueGroupRefresh(cc.Chat)
		}
		return c, nil
	case !errors.Is(err, core.ErrNotFound):
		return core.Chat{}, err
	}
	c = core.Chat{ID: cc.Chat, Kind: core.ChatDirect}
	switch {
	case cc.Chat.IsGroup():
		c.Kind, c.Title = core.ChatGroup, "Unknown group"
		b.queueGroupRefresh(cc.Chat)
	case cc.Chat.ACI() == cc.Self:
		c.Kind, c.Title = core.ChatNoteToSelf, "Note to Self"
	default:
		peer := cc.Chat.ACI()
		if cc.Sender != cc.Self {
			c.MessageRequest = b.probablyMessageRequest(ctx, peer)
		}
		if ct, err := b.db.GetContact(ctx, peer); err == nil {
			c.Contact = &ct
			c.Blocked = ct.Blocked
		}
		b.queueProfile(peer)
	}
	if err := b.db.UpsertChat(ctx, c); err != nil {
		return core.Chat{}, err
	}
	return b.db.GetChat(ctx, cc.Chat)
}

// probablyMessageRequest decides whether a new direct chat from peer is an
// unaccepted message request: the peer is not in the phone's address book
// and the conversation was never accepted (whitelisted).
func (b *Backend) probablyMessageRequest(ctx context.Context, peer core.ACI) bool {
	cli, err := b.client()
	if err != nil {
		return false
	}
	u, err := parseACI(peer)
	if err != nil {
		return false
	}
	rec, err := cli.Store.RecipientStore.LoadAndUpdateRecipient(ctx, u, uuid.Nil, nil)
	if err != nil || rec == nil {
		return true
	}
	if rec.ProbablyMessageRequest() {
		return true
	}
	return rec.Whitelisted == nil && rec.ContactName == "" && rec.Nickname == ""
}

func (b *Backend) onDataMessage(ctx context.Context, cc convCtx, info events.MessageInfo, dm *signalpb.DataMessage) error {
	if len(dm.GetProfileKey()) == libsignalgo.ProfileKeyLength && cc.Sender != cc.Self {
		b.queueProfile(cc.Sender)
	}
	kind := classifyDataMessage(dm)
	zerolog.Ctx(ctx).Debug().Stringer("kind", kind).Uint64("ts", dm.GetTimestamp()).Str("chat", string(cc.Chat)).Msg("Data message")
	switch kind {
	case dmContent:
		return b.storeIncoming(ctx, cc, info, dm)
	case dmTimerUpdate:
		return b.onTimerUpdate(ctx, cc, info, dm)
	case dmReaction:
		return b.onReaction(ctx, cc, info, dm)
	case dmDelete:
		return b.onRemoteDelete(ctx, cc, cc.Sender, dm.GetDelete().GetTargetSentTimestamp(), false)
	case dmAdminDelete:
		ad := dm.GetAdminDelete()
		author := aciFromBinaryOrString(ad.GetTargetAuthorAciBinary(), "")
		return b.onRemoteDelete(ctx, cc, author, ad.GetTargetSentTimestamp(), true)
	case dmPollVote:
		return b.onPollVote(ctx, cc, dm.GetPollVote())
	case dmPollTerminate:
		return b.onPollTerminate(ctx, cc, dm.GetPollTerminate())
	case dmPin:
		pm := dm.GetPinMessage()
		return b.onPin(ctx, cc, info, dm.GetTimestamp(), aciFromBinaryOrString(pm.GetTargetAuthorAciBinary(), ""), pm.GetTargetSentTimestamp(), true, pinDuration(pm))
	case dmUnpin:
		um := dm.GetUnpinMessage()
		return b.onPin(ctx, cc, info, dm.GetTimestamp(), aciFromBinaryOrString(um.GetTargetAuthorAciBinary(), ""), um.GetTargetSentTimestamp(), false, 0)
	case dmGroupUpdate:
		return b.onGroupChange(ctx, cc, info, dm)
	case dmGroupContext:
		return b.onGroupContext(ctx, cc, info, dm)
	case dmUnsupported:
		return b.storeSystem(ctx, cc, info.GroupRevision, dm.GetTimestamp(), unsupportedText, true)
	}
	return nil
}

// storeIncoming stores a content message (incoming or a sync transcript of
// one we sent from another device).
func (b *Backend) storeIncoming(ctx context.Context, cc convCtx, info events.MessageInfo, dm *signalpb.DataMessage) error {
	chat, err := b.ensureChat(ctx, cc, info.GroupRevision)
	if err != nil {
		return err
	}
	conv := convertDataMessage(cc, dm)
	m := conv.Msg
	if _, err := b.db.GetMessage(ctx, m.ID); err == nil {
		return nil // duplicate delivery
	}
	if m.Outgoing && m.ExpiresIn > 0 {
		m.ExpireStartedAt = cc.ReceivedAt
	}
	if m.Quote != nil {
		if _, err := b.db.GetMessage(ctx, m.Quote.ID); err == nil {
			m.Quote.OriginalFound = true
		} else if id, ok, _ := b.index.lookup(ctx, m.Quote.ID.Author, m.Quote.ID.Timestamp); ok && id.Chat == cc.Chat {
			m.Quote.ID, m.Quote.OriginalFound = id, true
		}
	}
	if m.Kind == core.KindStoryReply && m.StoryContext != nil {
		if _, err := b.db.GetStory(ctx, core.MessageID{Author: m.StoryContext.Author, Timestamp: m.StoryContext.Timestamp}); err != nil {
			zerolog.Ctx(ctx).Debug().Msg("Story reply to a story warte has not seen")
		}
	}
	b.syncTimerFromMessage(ctx, &chat, dm)
	if err := b.persistAttachments(ctx, m.ID, conv.Atts); err != nil {
		return err
	}
	var opts []store.PutOption
	if m.Outgoing {
		opts = append(opts, store.AsRead())
	}
	if _, err := b.db.PutMessage(ctx, m, opts...); err != nil {
		return err
	}
	if err := b.index.put(ctx, m.ID, 0, info.ServerGUID, info.ReportingToken); err != nil {
		return err
	}
	b.unarchiveOnActivity(ctx, chat, m.Outgoing)
	stored, err := b.db.GetMessage(ctx, m.ID)
	if err != nil {
		return err
	}
	b.emit(core.MessageAdded{Message: stored, Notify: b.shouldNotify(chat, stored)})
	b.emitChat(ctx, chat.ID)
	if !m.ExpireStartedAt.IsZero() {
		b.kickExpiry()
	}
	b.scheduleDownloads(m, conv)
	b.rememberStickerPack(ctx, m.Sticker)
	return nil
}

// shouldNotify decides whether a new message should trigger a desktop
// notification: incoming, not muted (unless it mentions us), not a request.
func (b *Backend) shouldNotify(c core.Chat, m core.Message) bool {
	if m.Outgoing || m.Kind == core.KindSystem || c.Blocked || c.MessageRequest {
		return false
	}
	if c.Muted(b.now()) && !m.MentionsSelf {
		return false
	}
	return true
}

// unarchiveOnActivity unarchives a chat on new activity, as Signal does
// (muted chats stay archived).
func (b *Backend) unarchiveOnActivity(ctx context.Context, c core.Chat, outgoing bool) {
	if !c.Archived || (!outgoing && c.Muted(b.now())) {
		return
	}
	if _, err := b.db.UpdateChat(ctx, c.ID, func(ch *core.Chat) error { ch.Archived = false; return nil }); err != nil {
		zerolog.Ctx(ctx).Debug().Err(err).Msg("Failed to unarchive chat")
	}
}

// syncTimerFromMessage adopts the timer of a message in a direct chat when
// its timer version is at least the one we know (Signal's version-aware
// implicit timer update).
func (b *Backend) syncTimerFromMessage(ctx context.Context, c *core.Chat, dm *signalpb.DataMessage) {
	if !c.ID.IsDirect() || dm.ExpireTimerVersion == nil {
		return
	}
	d := time.Duration(dm.GetExpireTimer()) * time.Second
	if d == c.ExpireTimer {
		return
	}
	var known uint32
	_, _ = b.kv.get(ctx, "timer_version."+string(c.ID), &known)
	if dm.GetExpireTimerVersion() < known {
		return
	}
	_ = b.kv.set(ctx, "timer_version."+string(c.ID), dm.GetExpireTimerVersion())
	if nc, err := b.db.UpdateChat(ctx, c.ID, func(ch *core.Chat) error { ch.ExpireTimer = d; return nil }); err == nil {
		*c = nc
	}
}

// persistAttachments records attachment metadata and download pointers.
func (b *Backend) persistAttachments(ctx context.Context, owner core.MessageID, refs []attachmentRef) error {
	for _, r := range refs {
		existing, err := b.db.GetAttachment(ctx, r.ID)
		if err == nil && existing.Downloaded {
			continue
		}
		remote, err := encodeRemote(r)
		if err != nil {
			return err
		}
		rec := store.AttachmentRecord{
			ID:          r.ID,
			Owner:       store.MessageOwner(owner),
			ContentType: r.Pointer.GetContentType(),
			FileName:    r.Pointer.GetFileName(),
			Size:        int64(r.Pointer.GetSize()),
			Digest:      r.Pointer.GetDigest(),
			Remote:      remote,
		}
		if err := b.db.PutAttachment(ctx, rec); err != nil {
			return err
		}
	}
	return nil
}

// storeSystem stores a system message authored by cc.Sender at ts.
func (b *Backend) storeSystem(ctx context.Context, cc convCtx, groupRev uint32, ts uint64, text string, notify bool) error {
	chat, err := b.ensureChat(ctx, cc, groupRev)
	if err != nil {
		return err
	}
	id := core.MessageID{Chat: cc.Chat, Author: cc.Sender, Timestamp: ts}
	if _, err := b.db.GetMessage(ctx, id); err == nil {
		return nil
	}
	m := core.Message{
		ID: id, Kind: core.KindSystem, SystemText: text, Outgoing: cc.Sender == cc.Self,
		SentAt: msTime(ts), ReceivedAt: cc.ReceivedAt, ServerTimestamp: cc.ServerTS,
	}
	if _, err := b.db.PutMessage(ctx, m, store.AsRead()); err != nil {
		return err
	}
	_ = b.index.put(ctx, id, 0, "", nil)
	stored, err := b.db.GetMessage(ctx, id)
	if err != nil {
		return err
	}
	b.emit(core.MessageAdded{Message: stored, Notify: notify && b.shouldNotify(chat, core.Message{Kind: core.KindNormal})})
	b.emitChat(ctx, chat.ID)
	return nil
}

func (b *Backend) onTimerUpdate(ctx context.Context, cc convCtx, info events.MessageInfo, dm *signalpb.DataMessage) error {
	chat, err := b.ensureChat(ctx, cc, info.GroupRevision)
	if err != nil {
		return err
	}
	d := time.Duration(dm.GetExpireTimer()) * time.Second
	if v := dm.ExpireTimerVersion; v != nil && chat.ID.IsDirect() {
		var known uint32
		_, _ = b.kv.get(ctx, "timer_version."+string(chat.ID), &known)
		if *v < known {
			zerolog.Ctx(ctx).Debug().Msg("Ignoring outdated timer change")
			return nil
		}
		_ = b.kv.set(ctx, "timer_version."+string(chat.ID), *v)
	}
	if chat.ExpireTimer != d {
		if _, err := b.db.UpdateChat(ctx, chat.ID, func(c *core.Chat) error { c.ExpireTimer = d; return nil }); err != nil {
			return err
		}
	}
	return b.storeSystem(ctx, cc, info.GroupRevision, dm.GetTimestamp(), timerChangeText(cc.Sender, cc.Self, b.names(ctx), d), false)
}

// resolveTarget finds the stored message (author, ts) in chat, following
// edit aliases. If nothing is found it returns the literal ID and false.
func (b *Backend) resolveTarget(ctx context.Context, chat core.ChatID, author core.ACI, ts uint64) (core.MessageID, bool) {
	id := core.MessageID{Chat: chat, Author: author, Timestamp: ts}
	if _, err := b.db.GetMessage(ctx, id); err == nil {
		return id, true
	}
	if alias, ok, _ := b.index.lookup(ctx, author, ts); ok && alias.Chat == chat {
		return alias, true
	}
	return id, false
}

func (b *Backend) onReaction(ctx context.Context, cc convCtx, info events.MessageInfo, dm *signalpb.DataMessage) error {
	rt, err := reactionFromSignal(dm.GetReaction())
	if err != nil {
		return err
	}
	if sc := dm.GetStoryContext(); sc != nil {
		if rt.Remove {
			return nil
		}
		chat, err := b.ensureChat(ctx, cc, info.GroupRevision)
		if err != nil {
			return err
		}
		m := core.Message{
			ID:   core.MessageID{Chat: cc.Chat, Author: cc.Sender, Timestamp: dm.GetTimestamp()},
			Kind: core.KindStoryReply, Outgoing: cc.Sender == cc.Self,
			SentAt: msTime(dm.GetTimestamp()), ReceivedAt: cc.ReceivedAt, ServerTimestamp: cc.ServerTS,
			StoryContext: &core.StoryRef{Author: rt.Author, Timestamp: rt.Timestamp, Emoji: rt.Emoji},
		}
		if m.Outgoing {
			m.Status = core.StatusSent
		}
		if _, err := b.db.PutMessage(ctx, m); err != nil {
			return err
		}
		_ = b.index.put(ctx, m.ID, 0, info.ServerGUID, info.ReportingToken)
		stored, _ := b.db.GetMessage(ctx, m.ID)
		b.emit(core.MessageAdded{Message: stored, Notify: b.shouldNotify(chat, stored)})
		b.emitChat(ctx, chat.ID)
		return nil
	}
	target, ok := b.resolveTarget(ctx, cc.Chat, rt.Author, rt.Timestamp)
	if !ok {
		zerolog.Ctx(ctx).Debug().Msg("Reaction to unknown message")
		return nil
	}
	r := core.Reaction{Emoji: rt.Emoji, From: cc.Sender, At: msTime(dm.GetTimestamp())}
	if rt.Remove {
		cur, err := b.db.GetMessage(ctx, target)
		if err != nil {
			return err
		}
		for _, x := range cur.Reactions {
			if x.From == cc.Sender && x.Emoji != rt.Emoji && rt.Emoji != "" {
				return nil // removing a reaction that was already replaced
			}
		}
		r.Emoji = ""
	}
	m, err := b.db.SetReaction(ctx, target, r)
	if err != nil {
		return err
	}
	b.emit(core.MessageUpdated{Message: m})
	return nil
}

func (b *Backend) onRemoteDelete(ctx context.Context, cc convCtx, author core.ACI, ts uint64, admin bool) error {
	if author == "" || ts == 0 {
		return errorf(errBadID, "delete target")
	}
	if !admin && author != cc.Sender {
		return nil
	}
	if admin {
		c, err := b.db.GetChat(ctx, cc.Chat)
		if err != nil || !isAdmin(c, cc.Sender) {
			zerolog.Ctx(ctx).Debug().Msg("Ignoring admin delete from non-admin")
			return nil
		}
	}
	target, ok := b.resolveTarget(ctx, cc.Chat, author, ts)
	if !ok {
		if !admin {
			// Story deletes arrive as deletes in the 1:1 chat (or group).
			_, err := b.deleteStoryFrom(ctx, cc.Sender, author, ts)
			return err
		}
		return nil
	}
	return b.applyRemoteDelete(ctx, target)
}

func isAdmin(c core.Chat, aci core.ACI) bool {
	for _, m := range c.Members {
		if m.ACI == aci {
			return m.Admin
		}
	}
	return false
}

// applyRemoteDelete blanks a message and drops its attachment data.
func (b *Backend) applyRemoteDelete(ctx context.Context, id core.MessageID) error {
	m, err := b.db.UpdateMessage(ctx, id, func(m *core.Message) error {
		remoteDelete(m)
		return nil
	})
	if err != nil {
		return err
	}
	b.purgeAttachments(ctx, id)
	b.emit(core.MessageUpdated{Message: m})
	b.emitChat(ctx, id.Chat)
	return nil
}

// purgeAttachments removes attachment metadata (and, via garbage
// collection, the encrypted bytes) of a message.
func (b *Backend) purgeAttachments(ctx context.Context, id core.MessageID) {
	recs, err := b.db.ListAttachments(ctx, store.MessageOwner(id))
	if err != nil {
		return
	}
	for _, r := range recs {
		_ = b.db.DeleteAttachment(ctx, r.ID)
	}
	if len(recs) > 0 {
		if _, err := b.db.CollectGarbage(ctx, b.blobs, time.Minute); err != nil {
			zerolog.Ctx(ctx).Debug().Err(err).Msg("Blob garbage collection failed")
		}
	}
}

func (b *Backend) onPollVote(ctx context.Context, cc convCtx, v *signalpb.DataMessage_PollVote) error {
	author := aciFromBinaryOrString(v.GetTargetAuthorAciBinary(), "")
	target, ok := b.resolveTarget(ctx, cc.Chat, author, v.GetTargetSentTimestamp())
	if !ok {
		return nil
	}
	key := "poll_votes." + target.String()
	counts := map[core.ACI]uint32{}
	_, _ = b.kv.get(ctx, key, &counts)
	if prev, seen := counts[cc.Sender]; seen && v.GetVoteCount() <= prev {
		return nil // an older or duplicate vote
	}
	m, err := b.db.UpdateMessage(ctx, target, func(m *core.Message) error {
		if m.Poll == nil {
			return errorf(errBadID, "vote on a message that is not a poll")
		}
		if m.Poll.Ended {
			return errorf(errBadID, "vote on an ended poll")
		}
		applyPollVote(m.Poll, cc.Sender, v.GetOptionIndexes())
		return nil
	})
	if err != nil {
		if errors.Is(err, errBadID) {
			return nil
		}
		return err
	}
	counts[cc.Sender] = v.GetVoteCount()
	_ = b.kv.set(ctx, key, counts)
	b.emit(core.MessageUpdated{Message: m})
	return nil
}

func (b *Backend) onPollTerminate(ctx context.Context, cc convCtx, t *signalpb.DataMessage_PollTerminate) error {
	target, ok := b.resolveTarget(ctx, cc.Chat, cc.Sender, t.GetTargetSentTimestamp())
	if !ok {
		return nil
	}
	m, err := b.db.UpdateMessage(ctx, target, func(m *core.Message) error {
		if m.Poll != nil {
			m.Poll.Ended = true
		}
		return nil
	})
	if err != nil {
		return err
	}
	b.emit(core.MessageUpdated{Message: m})
	return nil
}

// onPin applies a (un)pin from cc.Sender and records a system message.
func (b *Backend) onPin(ctx context.Context, cc convCtx, info events.MessageInfo, ts uint64, author core.ACI, targetTS uint64, pin bool, d time.Duration) error {
	target, ok := b.resolveTarget(ctx, cc.Chat, author, targetTS)
	if !ok {
		return nil
	}
	if err := b.setPinned(ctx, target, pin, d); err != nil {
		return err
	}
	return b.storeSystem(ctx, cc, info.GroupRevision, ts, pinText(cc.Sender, cc.Self, b.names(ctx), pin), false)
}

// setPinned updates a message's pin state and its automatic unpin time.
func (b *Backend) setPinned(ctx context.Context, id core.MessageID, pin bool, d time.Duration) error {
	m, err := b.db.UpdateMessage(ctx, id, func(m *core.Message) error { m.Pinned = pin; return nil })
	if err != nil {
		return err
	}
	pins := map[string]time.Time{}
	_, _ = b.kv.get(ctx, "pin_expiry", &pins)
	delete(pins, id.String())
	if pin && d > 0 {
		pins[id.String()] = b.now().Add(d)
	}
	_ = b.kv.set(ctx, "pin_expiry", pins)
	if pin && d > 0 {
		b.kickExpiry()
	}
	b.emit(core.MessageUpdated{Message: m})
	b.emitChat(ctx, id.Chat)
	return nil
}

func (b *Backend) onGroupChange(ctx context.Context, cc convCtx, info events.MessageInfo, dm *signalpb.DataMessage) error {
	cli, err := b.client()
	if err != nil {
		return err
	}
	change, err := cli.DecryptGroupChange(ctx, dm.GetGroupV2())
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to decrypt group change")
		b.queueGroupRefresh(cc.Chat)
		return nil
	}
	if _, err := b.ensureChat(ctx, cc, 0); err != nil {
		return err
	}
	// Apply the cheap parts now; a refresh fetches the full state.
	if _, err := b.db.UpdateChat(ctx, cc.Chat, func(c *core.Chat) error {
		if change.ModifyTitle != nil {
			c.Title = *change.ModifyTitle
		}
		if change.ModifyDescription != nil {
			c.Description = *change.ModifyDescription
		}
		if change.ModifyDisappearingMessagesDuration != nil {
			c.ExpireTimer = time.Duration(*change.ModifyDisappearingMessagesDuration) * time.Second
		}
		if change.ModifyAnnouncementsOnly != nil {
			c.OnlyAdminsSend = *change.ModifyAnnouncementsOnly
		}
		return nil
	}); err != nil {
		return err
	}
	b.queueGroupRefresh(cc.Chat)
	if src := change.SourceServiceID; src.Type == libsignalgo.ServiceIDTypeACI && src.UUID != uuid.Nil {
		cc.Sender = aciOf(src.UUID)
	}
	return b.storeSystem(ctx, cc, 0, dm.GetTimestamp(), groupChangeText(change, cc.Self, b.names(ctx)), false)
}

func (b *Backend) onGroupContext(ctx context.Context, cc convCtx, info events.MessageInfo, dm *signalpb.DataMessage) error {
	existing, err := b.db.GetChat(ctx, cc.Chat)
	isNew := errors.Is(err, core.ErrNotFound)
	if err != nil && !isNew {
		return err
	}
	if !isNew && dm.GetGroupV2().GetRevision() <= existing.GroupRevision {
		return nil
	}
	b.queueGroupRefresh(cc.Chat)
	if dm.GetGroupV2().GetRevision() == 0 {
		return b.storeSystem(ctx, cc, 0, dm.GetTimestamp(), groupCreatedText(cc.Sender, cc.Self, b.names(ctx)), cc.Sender != cc.Self)
	}
	_, err = b.ensureChat(ctx, cc, info.GroupRevision)
	return err
}

func (b *Backend) onEditMessage(ctx context.Context, cc convCtx, info events.MessageInfo, em *signalpb.EditMessage) error {
	dm := em.GetDataMessage()
	if dm == nil || em.GetTargetSentTimestamp() == 0 {
		return errorf(errBadID, "edit without content")
	}
	target, ok := b.resolveTarget(ctx, cc.Chat, cc.Sender, em.GetTargetSentTimestamp())
	editCC := cc
	conv := convertDataMessage(editCC, dm)
	if !ok {
		// The original never arrived: show the edited text as the message.
		dm2 := proto.Clone(dm).(*signalpb.DataMessage)
		dm2.Timestamp = ptr.Ptr(em.GetTargetSentTimestamp())
		if err := b.storeIncoming(ctx, cc, info, dm2); err != nil {
			return err
		}
		_, _ = b.db.UpdateMessage(ctx, target, func(m *core.Message) error { m.Edited = true; return nil })
		_ = b.index.put(ctx, target, dm.GetTimestamp(), "", nil)
		b.emitMessage(ctx, target)
		return nil
	}
	prevAt := msTime(target.Timestamp)
	if last, err := b.index.lastEdit(ctx, target); err == nil && last > 0 {
		prevAt = msTime(last)
	}
	var previewRefs []attachmentRef
	for _, r := range conv.Atts {
		if r.Role == rolePreview || r.Role == roleLongText {
			previewRefs = append(previewRefs, r)
		}
	}
	if err := b.persistAttachments(ctx, target, previewRefs); err != nil {
		return err
	}
	m, err := b.db.UpdateMessage(ctx, target, func(m *core.Message) error {
		if m.ViewOnce || m.RemoteDeleted {
			return errorf(errBadID, "edit of view-once or deleted message")
		}
		applyEdit(m, conv.Msg, prevAt)
		return nil
	})
	if err != nil {
		if errors.Is(err, errBadID) {
			return nil
		}
		return err
	}
	if err := b.index.put(ctx, target, dm.GetTimestamp(), info.ServerGUID, info.ReportingToken); err != nil {
		return err
	}
	b.emit(core.MessageUpdated{Message: m})
	b.emitChat(ctx, target.Chat)
	if conv.LongText != nil {
		b.queueDownload(downloadJob{msg: target, ref: *conv.LongText})
	}
	for _, r := range previewRefs {
		if r.Role == rolePreview {
			b.queueDownload(downloadJob{msg: target, ref: r})
		}
	}
	return nil
}

func (b *Backend) onTyping(ctx context.Context, cc convCtx, t *signalpb.TypingMessage) {
	if cc.Sender == cc.Self {
		return
	}
	if s, err := b.db.Settings(ctx); err == nil && !s.TypingIndicators {
		return
	}
	chat := cc.Chat
	if gid, ok := groupChatIDFromBytes(t.GetGroupId()); ok {
		chat = gid
	}
	typing := t.GetAction() == signalpb.TypingMessage_STARTED
	b.setTyping(ctx, chat, cc.Sender, typing)
}

// setTyping emits a typing change and arms the auto-stop timer.
func (b *Backend) setTyping(ctx context.Context, chat core.ChatID, who core.ACI, typing bool) {
	key := typingKey{chat: chat, who: who}
	b.typingMu.Lock()
	if t, ok := b.typing[key]; ok {
		t.Stop()
		delete(b.typing, key)
	}
	if typing {
		b.typing[key] = time.AfterFunc(typingTimeout, func() {
			b.typingMu.Lock()
			delete(b.typing, key)
			b.typingMu.Unlock()
			b.emit(core.TypingChanged{Chat: chat, Who: who, Name: b.nameOf(b.ctx, who), Typing: false})
		})
	}
	b.typingMu.Unlock()
	b.emit(core.TypingChanged{Chat: chat, Who: who, Name: b.nameOf(ctx, who), Typing: typing})
}

func (b *Backend) onDecryptionError(ctx context.Context, e *events.DecryptionError) error {
	sender := aciOf(e.Sender)
	if sender == "" || e.Timestamp == 0 {
		return nil
	}
	cc := convCtx{Self: b.selfACI(), Chat: core.DirectChatID(sender), Sender: sender, ReceivedAt: b.now().UTC()}
	return b.storeSystem(ctx, cc, 0, e.Timestamp, decryptionErrorText(b.nameOf(ctx, sender)), false)
}

func (b *Backend) onReceipt(ctx context.Context, e *events.Receipt) error {
	if e.Content == nil {
		return nil
	}
	sender := aciOf(e.Sender)
	self := b.selfACI()
	status := receiptStatus(e.Content.GetType())
	chats := map[core.ChatID]bool{}
	for _, ts := range e.Content.GetTimestamp() {
		id, ok, err := b.index.lookup(ctx, self, ts)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if sender == self {
			continue // our other devices only send read receipts as syncs
		}
		m, err := b.db.SetReceipt(ctx, id, sender, status)
		if err != nil {
			if errors.Is(err, core.ErrNotFound) {
				continue
			}
			return err
		}
		b.emit(core.MessageUpdated{Message: m})
		chats[id.Chat] = true
	}
	for c := range chats {
		b.emitChat(ctx, c)
	}
	return nil
}

func (b *Backend) onReadSelf(ctx context.Context, e *events.ReadSelf) error {
	upTo := map[core.ChatID]core.MessageID{}
	for _, r := range e.Messages {
		author := aciFromBinaryOrString(r.GetSenderAciBinary(), r.GetSenderAci())
		if author == "" {
			continue
		}
		id, ok, err := b.index.lookup(ctx, author, r.GetTimestamp())
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		m, err := b.db.GetMessage(ctx, id)
		if err != nil {
			continue
		}
		if cur, ok := upTo[id.Chat]; !ok || m.SentAt.After(msTime(cur.Timestamp)) {
			upTo[id.Chat] = id
		}
	}
	for chat, id := range upTo {
		ids, err := b.db.MarkRead(ctx, chat, id)
		if err != nil {
			return err
		}
		b.emitReadChanges(ctx, chat, ids)
	}
	return nil
}

// emitReadChanges announces messages whose disappearing timer started on
// read and the chat's new counters.
func (b *Backend) emitReadChanges(ctx context.Context, chat core.ChatID, ids []core.MessageID) {
	timers := false
	for _, id := range ids {
		m, err := b.db.GetMessage(ctx, id)
		if err != nil {
			continue
		}
		if m.ExpiresIn > 0 {
			timers = true
			b.emit(core.MessageUpdated{Message: m})
		}
	}
	if timers {
		b.kickExpiry()
	}
	b.emitChat(ctx, chat)
}

func (b *Backend) onViewOnceOpen(ctx context.Context, e *events.ViewOnceOpen) error {
	if e.SyncMessage_ViewOnceOpen == nil {
		return nil
	}
	author := aciFromBinaryOrString(e.GetSenderAciBinary(), e.GetSenderAci())
	id, ok, err := b.index.lookup(ctx, author, e.GetTimestamp())
	if err != nil || !ok {
		return err
	}
	return b.markViewOnceOpened(ctx, id)
}

// markViewOnceOpened marks a view-once message opened and destroys its
// media.
func (b *Backend) markViewOnceOpened(ctx context.Context, id core.MessageID) error {
	m, err := b.db.UpdateMessage(ctx, id, func(m *core.Message) error {
		if !m.ViewOnce {
			return errorf(core.ErrReadOnly, "not a view-once message")
		}
		m.ViewOnceOpened = true
		for i := range m.Attachments {
			m.Attachments[i].Downloaded = false
		}
		return nil
	})
	if err != nil {
		return err
	}
	b.purgeAttachments(ctx, id)
	b.emit(core.MessageUpdated{Message: m})
	return nil
}

// conversationChat maps a sync conversation identifier to a chat.
func conversationChat(ci *signalpb.ConversationIdentifier) (core.ChatID, bool) {
	switch id := ci.GetIdentifier().(type) {
	case *signalpb.ConversationIdentifier_ThreadServiceId:
		if aci := aciFromBinaryOrString(nil, id.ThreadServiceId); aci != "" {
			return core.DirectChatID(aci), true
		}
	case *signalpb.ConversationIdentifier_ThreadServiceIdBinary:
		if aci := aciFromBinaryOrString(id.ThreadServiceIdBinary, ""); aci != "" {
			return core.DirectChatID(aci), true
		}
	case *signalpb.ConversationIdentifier_ThreadGroupId:
		return groupChatIDFromBytes(id.ThreadGroupId)
	}
	return "", false
}

// addressableID maps an addressable message to a message ID in chat.
func addressableID(chat core.ChatID, am *signalpb.AddressableMessage) (core.MessageID, bool) {
	var author core.ACI
	switch a := am.GetAuthor().(type) {
	case *signalpb.AddressableMessage_AuthorServiceId:
		author = aciFromBinaryOrString(nil, a.AuthorServiceId)
	case *signalpb.AddressableMessage_AuthorServiceIdBinary:
		author = aciFromBinaryOrString(a.AuthorServiceIdBinary, "")
	}
	if author == "" || am.GetSentTimestamp() == 0 {
		return core.MessageID{}, false
	}
	return core.MessageID{Chat: chat, Author: author, Timestamp: am.GetSentTimestamp()}, true
}

func (b *Backend) onDeleteForMe(ctx context.Context, e *events.DeleteForMe) error {
	if e.SyncMessage_DeleteForMe == nil {
		return nil
	}
	for _, md := range e.GetMessageDeletes() {
		chat, ok := conversationChat(md.GetConversation())
		if !ok {
			continue
		}
		for _, am := range md.GetMessages() {
			id, ok := addressableID(chat, am)
			if !ok {
				continue
			}
			target, found := b.resolveTarget(ctx, chat, id.Author, id.Timestamp)
			if !found {
				continue
			}
			if err := b.deleteLocal(ctx, target); err != nil && !errors.Is(err, core.ErrNotFound) {
				return err
			}
		}
	}
	for _, cd := range e.GetConversationDeletes() {
		if chat, ok := conversationChat(cd.GetConversation()); ok {
			if err := b.deleteConversationSync(ctx, chat, cd); err != nil && !errors.Is(err, core.ErrNotFound) {
				return err
			}
		}
	}
	for _, ld := range e.GetLocalOnlyConversationDeletes() {
		if chat, ok := conversationChat(ld.GetConversation()); ok {
			if err := b.deleteChatLocal(ctx, chat); err != nil && !errors.Is(err, core.ErrNotFound) {
				return err
			}
		}
	}
	for _, ad := range e.GetAttachmentDeletes() {
		chat, ok := conversationChat(ad.GetConversation())
		if !ok {
			continue
		}
		id, ok := addressableID(chat, ad.GetTargetMessage())
		if !ok {
			continue
		}
		target, found := b.resolveTarget(ctx, chat, id.Author, id.Timestamp)
		if !found {
			continue
		}
		if err := b.deleteAttachmentMatching(ctx, target, ad.GetClientUuid(), ad.GetFallbackDigest()); err != nil && !errors.Is(err, core.ErrNotFound) {
			return err
		}
	}
	return nil
}

// deleteLocal deletes one message on this device ("delete for me").
func (b *Backend) deleteLocal(ctx context.Context, id core.MessageID) error {
	if err := b.db.DeleteMessage(ctx, id); err != nil {
		return err
	}
	_ = b.index.forget(ctx, id)
	if _, err := b.db.PruneAttachments(ctx, b.blobs); err != nil {
		zerolog.Ctx(ctx).Debug().Err(err).Msg("Failed to prune attachments")
	}
	b.emit(core.MessageRemoved{ID: id})
	b.emitChat(ctx, id.Chat)
	return nil
}

// deleteChatLocal deletes a chat and its messages on this device.
func (b *Backend) deleteChatLocal(ctx context.Context, chat core.ChatID) error {
	if err := b.db.DeleteChat(ctx, chat); err != nil {
		return err
	}
	_ = b.index.forgetChat(ctx, chat)
	if _, err := b.db.PruneAttachments(ctx, b.blobs); err != nil {
		zerolog.Ctx(ctx).Debug().Err(err).Msg("Failed to prune attachments")
	}
	b.emit(core.ChatRemoved{ID: chat})
	return nil
}

// deleteAttachmentMatching removes one attachment (matched by client UUID
// or digest) from a message.
func (b *Backend) deleteAttachmentMatching(ctx context.Context, id core.MessageID, clientUUID, digest []byte) error {
	recs, err := b.db.ListAttachments(ctx, store.MessageOwner(id))
	if err != nil {
		return err
	}
	for _, r := range recs {
		ref, err := decodeRemote(r.Remote)
		if err != nil {
			continue
		}
		match := (len(clientUUID) > 0 && string(ref.Pointer.GetClientUuid()) == string(clientUUID)) ||
			(len(digest) > 0 && string(ref.Pointer.GetDigest()) == string(digest))
		if !match {
			continue
		}
		_ = b.db.DeleteAttachment(ctx, r.ID)
		m, err := b.db.UpdateMessage(ctx, id, func(m *core.Message) error {
			for i, a := range m.Attachments {
				if a.ID == r.ID {
					m.Attachments = append(m.Attachments[:i], m.Attachments[i+1:]...)
					break
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		_, _ = b.db.CollectGarbage(ctx, b.blobs, time.Minute)
		b.emit(core.MessageUpdated{Message: m})
	}
	return nil
}

func (b *Backend) onMessageRequestResponse(ctx context.Context, e *events.MessageRequestResponse) error {
	var chat core.ChatID
	switch {
	case e.GroupID != nil:
		chat = core.GroupChatID(types.BytesToGroupIdentifier(e.GroupID).String())
	case e.ThreadACI != uuid.Nil:
		chat = core.DirectChatID(aciOf(e.ThreadACI))
	default:
		return nil
	}
	switch e.Type {
	case signalpb.SyncMessage_MessageRequestResponse_DELETE:
		return ignoreNotFound(b.deleteChatLocal(ctx, chat))
	case signalpb.SyncMessage_MessageRequestResponse_BLOCK_AND_DELETE:
		_ = b.setChatBlockedLocal(ctx, chat, true)
		return ignoreNotFound(b.deleteChatLocal(ctx, chat))
	}
	_, err := b.db.UpdateChat(ctx, chat, func(c *core.Chat) error {
		switch e.Type {
		case signalpb.SyncMessage_MessageRequestResponse_ACCEPT:
			c.MessageRequest, c.Blocked = false, false
		case signalpb.SyncMessage_MessageRequestResponse_BLOCK, signalpb.SyncMessage_MessageRequestResponse_BLOCK_AND_SPAM:
			c.Blocked = true
		}
		return nil
	})
	if err != nil {
		return ignoreNotFound(err)
	}
	if chat.IsDirect() {
		_ = b.setContactBlocked(ctx, chat.ACI(), e.Type != signalpb.SyncMessage_MessageRequestResponse_ACCEPT &&
			e.Type != signalpb.SyncMessage_MessageRequestResponse_SPAM)
	}
	b.emitChat(ctx, chat)
	return nil
}

func ignoreNotFound(err error) error {
	if errors.Is(err, core.ErrNotFound) || errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}

// setChatBlockedLocal updates the blocked flag of a chat without syncing.
func (b *Backend) setChatBlockedLocal(ctx context.Context, chat core.ChatID, blocked bool) error {
	_, err := b.db.UpdateChat(ctx, chat, func(c *core.Chat) error { c.Blocked = blocked; return nil })
	if err == nil {
		b.emitChat(ctx, chat)
	}
	return err
}

// setContactBlocked updates the blocked flag of a contact.
func (b *Backend) setContactBlocked(ctx context.Context, aci core.ACI, blocked bool) error {
	c, err := b.db.GetContact(ctx, aci)
	if errors.Is(err, core.ErrNotFound) {
		c = core.Contact{ACI: aci}
	} else if err != nil {
		return err
	}
	if c.Blocked == blocked && err == nil {
		return nil
	}
	c.Blocked = blocked
	if err := b.db.UpsertContact(ctx, c); err != nil {
		return err
	}
	b.emit(core.ContactUpdated{Contact: c})
	return nil
}

func (b *Backend) onContactList(ctx context.Context, e *events.ContactList) error {
	self := b.selfACI()
	for _, r := range e.Contacts {
		if r == nil || r.ACI == uuid.Nil {
			continue
		}
		aci := aciOf(r.ACI)
		existing, err := b.db.GetContact(ctx, aci)
		if err != nil && !errors.Is(err, core.ErrNotFound) {
			return err
		}
		c := recipientToContact(r, existing, self)
		if r.Profile.AvatarPath != "" {
			b.registerAvatar(ctx, c.AvatarID, "avatar:"+string(aci), avatarSource{Kind: "profile", ACI: aci, Path: r.Profile.AvatarPath})
		} else if len(r.ContactAvatar.Image) > 0 && c.AvatarID != "" && c.AvatarID == avatarID("contact", string(aci), r.ContactAvatar.Hash) {
			b.cacheAvatar(ctx, c.AvatarID, "avatar:"+string(aci), r.ContactAvatar.Image, r.ContactAvatar.ContentType, avatarSource{Kind: "contact", ACI: aci})
		}
		if c == existing && err == nil {
			continue
		}
		if err := b.db.UpsertContact(ctx, c); err != nil {
			return err
		}
		if c.IsSelf {
			b.mu.Lock()
			b.self = c
			b.mu.Unlock()
		}
		b.emit(core.ContactUpdated{Contact: c})
		if ch, err := b.db.GetChat(ctx, core.DirectChatID(aci)); err == nil {
			if ch.Blocked != c.Blocked {
				_ = b.setChatBlockedLocal(ctx, ch.ID, c.Blocked)
			} else {
				b.emit(core.ChatUpdated{Chat: ch})
			}
		}
		if r.Profile.Key != (libsignalgo.ProfileKey{}) && r.Profile.FetchedAt.IsZero() {
			b.queueProfile(aci)
		}
	}
	if !e.IsFromDB {
		b.emitSyncDone("contacts", len(e.Contacts))
	}
	return nil
}

func (b *Backend) onIdentityChange(ctx context.Context, e *events.IdentityChange) error {
	if e.ServiceID.Type != libsignalgo.ServiceIDTypeACI {
		return nil
	}
	aci := aciOf(e.ServiceID.UUID)
	if aci == b.selfACI() {
		return nil
	}
	if c, err := b.db.GetContact(ctx, aci); err == nil && c.Verified {
		c.Verified = false
		if err := b.db.UpsertContact(ctx, c); err == nil {
			b.emit(core.ContactUpdated{Contact: c})
		}
	}
	name := b.nameOf(ctx, aci)
	b.emit(core.IdentityChanged{ACI: aci, Name: name})
	chat := core.DirectChatID(aci)
	if _, err := b.db.GetChat(ctx, chat); err != nil {
		return ignoreNotFound(err)
	}
	cc := convCtx{Self: b.selfACI(), Chat: chat, Sender: aci, ReceivedAt: b.now().UTC()}
	return b.storeSystem(ctx, cc, 0, b.nextTimestamp(), safetyNumberChangedText(name), false)
}
