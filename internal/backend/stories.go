package backend

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-signal/pkg/libsignalgo"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/events"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"

	"warte/internal/core"
	"warte/internal/store"
)

// roleStory marks the media of a story.
const roleStory attRole = "story"

// storyOwner is the attachment owner of a story's media. Story media is not
// reclaimed by store.PruneAttachments; deleteStory removes it.
func storyOwner(id core.MessageID) string {
	return "story:" + string(id.Author) + "|" + strconv.FormatUint(id.Timestamp, 10)
}

// storyKey is the kv key of a story's metadata.
func storyKey(id core.MessageID) string {
	return "story." + string(id.Author) + "." + strconv.FormatUint(id.Timestamp, 10)
}

// storyMeta is what warte needs about a story beyond core.Story.
type storyMeta struct {
	AllowsReplies bool `json:"r"`
	// Recipients is how many people can see a story we posted (here or on
	// another device); 0 for others' stories.
	Recipients int `json:"n,omitempty"`
	// Sent lists who can see a story we posted, for deleting it for
	// everyone (Sent.storyMessageRecipients of the sync transcript).
	Sent []storyRecipientMeta `json:"s,omitempty"`
}

// storyRecipientMeta is one recipient of a story we posted.
type storyRecipientMeta struct {
	ACI   core.ACI `json:"a"`
	Reply bool     `json:"r,omitempty"`
	// Lists are the distribution list IDs the recipient sees the story
	// through (empty for group stories).
	Lists []string `json:"l,omitempty"`
}

// storyRecipientsFromSync converts the recipients of a story sync
// transcript. For distribution-list stories only recipients that still
// see the story through some list count (Signal-Desktop
// onStoryRecipientUpdate removes the others); group story recipients
// carry no list IDs.
func storyRecipientsFromSync(rs []events.StoryRecipient, group bool) []storyRecipientMeta {
	var out []storyRecipientMeta
	for _, r := range rs {
		if !group && len(r.DistributionListIDs) == 0 {
			continue
		}
		u, err := uuid.Parse(r.ServiceID)
		if err != nil || u == uuid.Nil {
			continue // PNIs ("PNI:…") can't receive stories
		}
		out = append(out, storyRecipientMeta{ACI: aciOf(u), Reply: r.AllowedToReply, Lists: slices.Clone(r.DistributionListIDs)})
	}
	return out
}

// storyConv is a converted story.
type storyConv struct {
	Story         core.Story
	Media         *attachmentRef
	AllowsReplies bool
}

// errEmptyStory marks a story without displayable content.
var errEmptyStory = errors.New("story without media or text")

// convertStory converts a StoryMessage posted by author at ts (ms) to group
// (empty for stories sent to distribution lists). Like Signal-Desktop it
// keeps one attachment, requires text stories to have text or a link
// preview, and maps unknown text styles to the default.
func convertStory(author core.ACI, group core.ChatID, ts uint64, sm *signalpb.StoryMessage) (storyConv, error) {
	chat := group
	if chat == "" {
		chat = core.DirectChatID(author)
	}
	id := core.MessageID{Chat: chat, Author: author, Timestamp: ts}
	out := storyConv{
		Story:         core.Story{ID: id, At: msTime(ts), Group: group},
		AllowsReplies: sm.GetAllowsReplies(),
	}
	switch {
	case sm.GetFileAttachment() != nil:
		p := sm.GetFileAttachment()
		a := attachmentFromPointer(p)
		a.ID = attachmentID(id, roleStory, 0, p)
		out.Story.Media = &a
		out.Story.Caption = p.GetCaption()
		out.Media = &attachmentRef{ID: a.ID, Role: roleStory, Pointer: p}
	case sm.GetTextAttachment() != nil:
		ta := sm.GetTextAttachment()
		if ta.GetText() == "" && ta.GetPreview().GetUrl() == "" {
			return storyConv{}, errEmptyStory
		}
		text := &core.TextStory{
			Text:       ta.GetText(),
			Style:      int(ta.GetTextStyle()),
			Foreground: ta.GetTextForegroundColor(),
			URL:        ta.GetPreview().GetUrl(),
		}
		if _, known := signalpb.TextAttachment_Style_name[int32(text.Style)]; !known {
			text.Style = int(signalpb.TextAttachment_DEFAULT)
		}
		switch bg := ta.GetBackground().(type) {
		case *signalpb.TextAttachment_Color:
			text.Background = bg.Color
		case *signalpb.TextAttachment_Gradient_:
			colors := bg.Gradient.GetColors()
			if len(colors) == 0 && (bg.Gradient.StartColor != nil || bg.Gradient.EndColor != nil) {
				colors = []uint32{bg.Gradient.GetStartColor(), bg.Gradient.GetEndColor()}
			}
			if len(colors) > 0 {
				text.Gradient = append([]uint32(nil), colors...)
				text.Background = colors[0]
			}
		}
		out.Story.Text = text
	default:
		return storyConv{}, errEmptyStory
	}
	return out, nil
}

// storyExpired reports whether a story posted at ts is past its 24 hours.
func storyExpired(ts uint64, now time.Time) bool {
	return !msTime(ts).Add(storyLifetime).After(now)
}

// storiesDisabled reports whether the user turned stories off (account
// record), in which case Signal-Desktop drops incoming stories.
func (b *Backend) storiesDisabled() bool {
	cli, err := b.client()
	return err == nil && cli.Store.AccountRecord().GetStoriesDisabled()
}

// senderBlocked reports whether a contact (or its direct chat) is blocked.
func (b *Backend) senderBlocked(ctx context.Context, aci core.ACI) bool {
	if c, err := b.db.GetContact(ctx, aci); err == nil && c.Blocked {
		return true
	}
	if c, err := b.db.GetChat(ctx, core.DirectChatID(aci)); err == nil && c.Blocked {
		return true
	}
	return false
}

func (b *Backend) onStory(ctx context.Context, e *events.Story) error {
	if e.Story == nil {
		return nil
	}
	self := b.selfACI()
	author := aciOf(e.Info.Sender)
	if e.IsSync {
		author = self
	}
	if author == "" || e.Timestamp == 0 {
		return errorf(errBadID, "story without author or timestamp")
	}
	log := zerolog.Ctx(ctx).With().Uint64("ts", e.Timestamp).Bool("sync", e.IsSync).Logger()
	now := b.now()
	if storyExpired(e.Timestamp, now) {
		log.Debug().Msg("Dropping expired story")
		return nil
	}
	if author != self && (b.storiesDisabled() || b.senderBlocked(ctx, author)) {
		log.Debug().Msg("Dropping story (stories disabled or sender blocked)")
		return nil
	}
	var group core.ChatID
	if e.GroupID != "" {
		group = groupChatID(e.GroupID)
		cc := convCtx{Self: self, Chat: group, Sender: author, ServerTS: e.Info.ServerTimestamp, ReceivedAt: now.UTC()}
		c, err := b.ensureChat(ctx, cc, e.Info.GroupRevision)
		if err != nil {
			return err
		}
		if c.Blocked || (author != self && len(c.Members) > 0 && !memberOf(c.Members, author)) {
			log.Debug().Msg("Dropping group story (blocked group or sender not a member)")
			return nil
		}
	}
	conv, err := convertStory(author, group, e.Timestamp, e.Story)
	if err != nil {
		log.Debug().Err(err).Msg("Dropping story")
		return nil
	}
	meta := storyMeta{AllowsReplies: conv.AllowsReplies}
	if e.IsSync {
		meta.Sent = storyRecipientsFromSync(e.Recipients, group != "")
		meta.Recipients = len(meta.Sent)
	}
	id := conv.Story.ID
	if _, err := b.db.GetStory(ctx, id); err == nil {
		if e.IsSync && e.IsRecipientUpdate {
			_ = b.kv.set(ctx, storyKey(id), meta)
			b.emit(core.StoriesChanged{})
		}
		return nil // duplicate delivery
	} else if !errors.Is(err, core.ErrNotFound) {
		return err
	}
	if conv.Media != nil {
		remote, err := encodeRemote(*conv.Media)
		if err != nil {
			return err
		}
		p := conv.Media.Pointer
		if err := b.db.PutAttachment(ctx, store.AttachmentRecord{
			ID: conv.Media.ID, Owner: storyOwner(id), ContentType: p.GetContentType(), FileName: p.GetFileName(),
			Size: int64(p.GetSize()), Digest: p.GetDigest(), Remote: remote,
		}); err != nil {
			return err
		}
		if !hasLocation(p) {
			conv.Story.Media.Failed = true
		}
	}
	conv.Story.AuthorName = b.nameOf(ctx, author)
	conv.Story.Viewed = author == self
	if err := b.db.PutStory(ctx, conv.Story); err != nil {
		return err
	}
	if err := b.kv.set(ctx, storyKey(id), meta); err != nil {
		return err
	}
	if author != self && len(e.Story.GetProfileKey()) == libsignalgo.ProfileKeyLength {
		b.queueProfile(author)
	}
	b.emit(core.StoriesChanged{})
	b.kickExpiry()
	return nil
}

// onStoryRecipientUpdate applies a change of who can see a story we posted
// on another device. When nobody can see it anymore it was deleted for
// everyone (Signal-Desktop's onStoryRecipientUpdate).
func (b *Backend) onStoryRecipientUpdate(ctx context.Context, e *events.StoryRecipientUpdate) error {
	self := b.selfACI()
	id := core.MessageID{Author: self, Timestamp: e.Timestamp}
	s, err := b.db.GetStory(ctx, id)
	if err != nil {
		return ignoreNotFound(err)
	}
	if s.Group != "" {
		// Desktop applies recipient updates to distribution-list stories
		// only; group stories are deleted with a group delete message.
		return nil
	}
	sent := storyRecipientsFromSync(e.Recipients, false)
	// Desktop: a story only we can still see was deleted for everyone.
	sent = slices.DeleteFunc(sent, func(r storyRecipientMeta) bool { return r.ACI == self })
	if len(sent) == 0 {
		return b.deleteStory(ctx, id, true)
	}
	var meta storyMeta
	_, _ = b.kv.get(ctx, storyKey(id), &meta)
	meta.Sent = sent
	meta.Recipients = len(sent)
	if err := b.kv.set(ctx, storyKey(id), meta); err != nil {
		return err
	}
	b.emit(core.StoriesChanged{})
	return nil
}

// deleteStoryFrom handles a delete-for-everyone that targets a story: only
// its author may delete it. It reports whether a story was deleted.
func (b *Backend) deleteStoryFrom(ctx context.Context, sender, author core.ACI, ts uint64) (bool, error) {
	if sender != author {
		return false, nil
	}
	id := core.MessageID{Author: author, Timestamp: ts}
	if _, err := b.db.GetStory(ctx, id); err != nil {
		return false, ignoreNotFound(err)
	}
	return true, b.deleteStory(ctx, id, true)
}

// deleteStory removes a story, its media and metadata.
func (b *Backend) deleteStory(ctx context.Context, id core.MessageID, emit bool) error {
	if err := b.db.DeleteStory(ctx, id); err != nil && !errors.Is(err, core.ErrNotFound) {
		return err
	}
	recs, err := b.db.ListAttachments(ctx, storyOwner(id))
	if err == nil {
		for _, r := range recs {
			_ = b.db.DeleteAttachment(ctx, r.ID)
		}
		if len(recs) > 0 {
			if _, err := b.db.CollectGarbage(ctx, b.blobs, time.Minute); err != nil {
				zerolog.Ctx(ctx).Debug().Err(err).Msg("Blob garbage collection failed")
			}
		}
	}
	_ = b.kv.del(ctx, storyKey(id))
	if emit {
		b.emit(core.StoriesChanged{})
	}
	return nil
}

// expireStories deletes stories older than 24 hours and returns when the
// next one expires (zero if none).
func (b *Backend) expireStories(ctx context.Context) time.Time {
	all, err := b.db.ListStories(ctx, time.Time{})
	if err != nil {
		zerolog.Ctx(ctx).Debug().Err(err).Msg("Failed to list stories")
		return time.Time{}
	}
	now := b.now()
	removed := false
	var next time.Time
	for _, s := range all {
		at := s.At.Add(storyLifetime)
		if at.After(now) {
			if next.IsZero() || at.Before(next) {
				next = at
			}
			continue
		}
		if err := b.deleteStory(ctx, s.ID, false); err == nil {
			removed = true
		}
	}
	if removed {
		b.emit(core.StoriesChanged{})
	}
	return next
}

// Stories lists the stories of the last 24 hours, oldest first, without
// those of blocked contacts and groups.
func (b *Backend) Stories(ctx context.Context) ([]core.Story, error) {
	list, err := b.db.ListStories(ctx, b.now().Add(-storyLifetime))
	if err != nil {
		return nil, err
	}
	self := b.selfACI()
	blockedGroup := map[core.ChatID]bool{}
	blockedAuthor := map[core.ACI]bool{}
	out := list[:0]
	for _, s := range list {
		if s.ID.Author != self {
			blocked, seen := blockedAuthor[s.ID.Author]
			if !seen {
				blocked = b.senderBlocked(ctx, s.ID.Author)
				blockedAuthor[s.ID.Author] = blocked
			}
			if blocked {
				continue
			}
		}
		if s.Group != "" {
			blocked, seen := blockedGroup[s.Group]
			if !seen {
				c, err := b.db.GetChat(ctx, s.Group)
				blocked = err == nil && c.Blocked
				blockedGroup[s.Group] = blocked
			}
			if blocked {
				continue
			}
		}
		if name := b.nameOf(ctx, s.ID.Author); name != "Unknown" {
			s.AuthorName = name
		}
		out = append(out, s)
	}
	return out, nil
}

// storyAllowsReplies reports whether replies to a story are allowed
// (unknown stories are assumed to allow them).
func (b *Backend) storyAllowsReplies(ctx context.Context, id core.MessageID) bool {
	if s, err := b.db.GetStory(ctx, id); err == nil && s.Group != "" {
		return true // group story replies go to the group
	}
	var meta storyMeta
	if ok, _ := b.kv.get(ctx, storyKey(id), &meta); ok {
		return meta.AllowsReplies
	}
	return true
}

// storyViewReceiptsEnabled decides whether viewing a story sends a viewed
// receipt. Signal-Desktop requires the read receipt setting (sendReceipts)
// and the story view receipt setting, which defaults to the read receipt
// setting when the account record leaves it unset.
func storyViewReceiptsEnabled(ar *signalpb.AccountRecord, s core.Settings) bool {
	if !s.ReadReceipts {
		return false
	}
	switch ar.GetStoryViewReceiptsEnabled() {
	case signalpb.OptionalBool_ENABLED:
		return true
	case signalpb.OptionalBool_DISABLED:
		return false
	}
	return s.ReadReceipts
}

// syncViewed builds a viewed sync (SyncMessage.viewed) for messages or
// stories viewed on this device.
func syncViewed(ids []core.MessageID) *signalpb.SyncMessage {
	sm := &signalpb.SyncMessage{}
	for _, id := range ids {
		u, err := parseACI(id.Author)
		if err != nil || id.Timestamp == 0 {
			continue
		}
		sm.Viewed = append(sm.Viewed, &signalpb.SyncMessage_Viewed{
			SenderAci:       proto.String(u.String()),
			SenderAciBinary: u[:],
			Timestamp:       proto.Uint64(id.Timestamp),
		})
	}
	if len(sm.Viewed) == 0 {
		return nil
	}
	return sm
}

// viewedReceipt builds a VIEWED receipt for timestamps.
func viewedReceipt(timestamps ...uint64) *signalpb.ReceiptMessage {
	return &signalpb.ReceiptMessage{Type: signalpb.ReceiptMessage_VIEWED.Enum(), Timestamp: timestamps}
}

// MarkStoryViewed marks a story viewed. As in Signal-Desktop, the first
// view of someone else's story is synced to our other devices
// (SyncMessage.viewed) and, if story view receipts are enabled, a VIEWED
// receipt goes to the author.
func (b *Backend) MarkStoryViewed(ctx context.Context, id core.MessageID) error {
	s, err := b.db.GetStory(ctx, id)
	if err != nil {
		return err
	}
	if s.Viewed {
		return nil
	}
	if err := b.db.MarkStoryViewed(ctx, id); err != nil {
		return err
	}
	b.emit(core.StoriesChanged{})
	if id.Author == b.selfACI() {
		return nil
	}
	cli, err := b.client()
	if err != nil {
		return nil // viewed locally; nothing to tell others while offline
	}
	settings, _ := b.db.Settings(ctx)
	sendReceipt := storyViewReceiptsEnabled(cli.Store.AccountRecord(), settings)
	sid, err := aciServiceID(id.Author)
	if err != nil {
		return err
	}
	b.goBackground(func() {
		ctx, cancel := context.WithTimeout(b.log.WithContext(b.ctx), time.Minute)
		defer cancel()
		if sm := syncViewed([]core.MessageID{id}); sm != nil {
			if err := b.sendSync(ctx, cli, sm); err != nil {
				zerolog.Ctx(ctx).Debug().Err(err).Msg("Failed to sync story view")
			}
		}
		if !sendReceipt {
			return
		}
		if res := cli.SendReceiptWithoutSync(ctx, sid, viewedReceipt(id.Timestamp)); !res.WasSuccessful {
			zerolog.Ctx(ctx).Debug().Err(res.Error).Msg("Failed to send story viewed receipt")
		}
	})
	return nil
}

// storyMedia returns the decrypted media of a story, downloading it on
// first use (story media is never fetched eagerly).
func (b *Backend) storyMedia(ctx context.Context, s core.Story, attID string) ([]byte, error) {
	if s.Media == nil || s.Media.ID != attID {
		return nil, fmt.Errorf("attachment %s: %w", attID, core.ErrNotFound)
	}
	data, err := b.fetchAttachment(ctx, s.ID, attID)
	failed := errors.Is(err, errUnavailable)
	if err != nil && !failed {
		return nil, err
	}
	if cur, gerr := b.db.GetStory(ctx, s.ID); gerr == nil && cur.Media != nil &&
		(cur.Media.Downloaded == failed || cur.Media.Failed != failed) {
		cur.Media.Downloaded, cur.Media.Failed = !failed, failed
		if !failed {
			cur.Media.Progress = 1
			if cur.Media.Size == 0 {
				cur.Media.Size = int64(len(data))
			}
		}
		if perr := b.db.PutStory(ctx, cur); perr == nil {
			b.emit(core.StoriesChanged{})
		}
	}
	return data, err
}
