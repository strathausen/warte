package backend

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-signal/pkg/libsignalgo"
	"go.mau.fi/mautrix-signal/pkg/signalmeow"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"

	"warte/internal/core"
	"warte/internal/store"
)

// errInvalidStory marks a story that can't be posted as given.
var errInvalidStory = errors.New("backend: invalid story")

// textStoryFor maps a core text story to the fork's TextStory. Missing
// colors get Signal's defaults (white text on Desktop's default blue).
func textStoryFor(t core.TextStory) signalmeow.TextStory {
	return signalmeow.TextStory{
		Text:       t.Text,
		Style:      signalpb.TextAttachment_Style(t.Style),
		Foreground: t.Foreground,
		Background: t.Background,
		Gradient:   t.Gradient,
		PreviewURL: strings.TrimSpace(t.URL),
	}
}

// validateOutgoingStory checks a story before anything is stored. It
// returns the text attachment of a text story and the prepared upload of
// a media story.
func validateOutgoingStory(s core.OutgoingStory) (*signalpb.TextAttachment, *preparedUpload, error) {
	switch {
	case s.Text != nil && s.Media != nil:
		return nil, nil, fmt.Errorf("%w: a story has either text or media", errInvalidStory)
	case s.Text != nil:
		ta, err := signalmeow.BuildTextAttachment(textStoryFor(*s.Text))
		if errors.Is(err, signalmeow.ErrEmptyStory) {
			return nil, nil, errEmptyMessage
		} else if err != nil {
			return nil, nil, fmt.Errorf("%w: %w", errInvalidStory, err)
		}
		return ta, nil, nil
	case s.Media != nil:
		if len(s.Media.Data) == 0 {
			return nil, nil, errEmptyMessage
		}
		p := prepareUpload(*s.Media)
		if !strings.HasPrefix(p.ContentType, "image/") && !strings.HasPrefix(p.ContentType, "video/") {
			return nil, nil, fmt.Errorf("%w: stories can only be images or videos, not %s", errInvalidStory, p.ContentType)
		}
		return nil, &p, nil
	}
	return nil, nil, errEmptyMessage
}

// storyTargets splits OutgoingStory.To into "My Story" and groups. An
// empty list means My Story; Note to Self (our own direct chat) also
// stands for My Story.
func storyTargets(self core.ACI, to []core.ChatID) (myStory bool, groups []core.ChatID, err error) {
	if len(to) == 0 {
		return true, nil, nil
	}
	seen := map[core.ChatID]bool{}
	for _, c := range to {
		switch {
		case c == core.DirectChatID(self):
			myStory = true
		case c.IsGroup():
			if !seen[c] {
				seen[c] = true
				groups = append(groups, c)
			}
		default:
			return false, nil, fmt.Errorf("%w: stories go to My Story or to groups, not %q", errInvalidStory, c)
		}
	}
	return myStory, groups, nil
}

// storyConnectionSet approximates Signal-Desktop's "Signal connections"
// (the audience of My Story): base holds signalmeow's recipients with
// profile sharing or a system contact name; to them warte adds the direct
// chats we have talked in that are not message requests or blocked. The
// result is deduplicated; filtering blocked and unregistered accounts is
// the caller's job.
func storyConnectionSet(base []libsignalgo.ServiceID, chats []core.Chat, self core.ACI) []libsignalgo.ServiceID {
	seen := map[libsignalgo.ServiceID]bool{}
	var out []libsignalgo.ServiceID
	add := func(sid libsignalgo.ServiceID) {
		if sid.Type != libsignalgo.ServiceIDTypeACI || sid.UUID == uuid.Nil || aciOf(sid.UUID) == self || seen[sid] {
			return
		}
		seen[sid] = true
		out = append(out, sid)
	}
	for _, sid := range base {
		add(sid)
	}
	for _, c := range chats {
		if c.Kind != core.ChatDirect || c.MessageRequest || c.Blocked || c.LastMessage == nil {
			continue
		}
		if sid, err := aciServiceID(c.ID.ACI()); err == nil {
			add(sid)
		}
	}
	return out
}

// ownStoryTarget is one destination of a story we post.
type ownStoryTarget struct {
	ID    core.MessageID
	Group core.ChatID // "" = My Story
	AttID string      // local media attachment, if any
}

// storyJob is a posted story waiting for upload and delivery.
type storyJob struct {
	text       *signalpb.TextAttachment
	upload     *preparedUpload
	profileKey []byte
	targets    []ownStoryTarget
}

// PostStory posts a story to "My Story" (To empty, or containing our own
// chat) and/or to groups. Like Signal-Desktop, every destination gets its
// own timestamp (groups: later than My Story's), the story is stored
// locally right away (core.StoriesChanged) and is uploaded and sent in the
// background; if nobody could receive a destination's story it is removed
// again and a core.ErrorEvent (Op "story") is emitted.
func (b *Backend) PostStory(ctx context.Context, s core.OutgoingStory) error {
	ta, upload, err := validateOutgoingStory(s)
	if err != nil {
		return err
	}
	self := b.selfACI()
	myStory, groups, err := storyTargets(self, s.To)
	if err != nil {
		return err
	}
	cli, err := b.client()
	if err != nil {
		return err
	}
	if b.storiesDisabled() {
		return errorf(core.ErrReadOnly, "stories are turned off")
	}
	pk := profileKey(ctx, cli)
	if len(pk) != libsignalgo.ProfileKeyLength {
		return errorf(core.ErrReadOnly, "your profile key is not known yet; try again after syncing")
	}
	for _, g := range groups {
		if _, err := b.sendableChat(ctx, g); err != nil {
			return err
		}
	}
	job := &storyJob{text: ta, upload: upload, profileKey: pk}
	if myStory {
		job.targets = append(job.targets, ownStoryTarget{ID: core.MessageID{Chat: core.DirectChatID(self), Author: self, Timestamp: b.nextTimestamp()}})
	}
	for _, g := range groups {
		job.targets = append(job.targets, ownStoryTarget{ID: core.MessageID{Chat: g, Author: self, Timestamp: b.nextTimestamp()}, Group: g})
	}
	for i := range job.targets {
		if err := b.storeOwnStory(ctx, &job.targets[i], ta, upload); err != nil {
			for _, t := range job.targets[:i] {
				_ = b.deleteStory(ctx, t.ID, false)
			}
			return err
		}
	}
	b.emit(core.StoriesChanged{})
	b.kickExpiry()
	b.goBackground(func() { b.deliverStory(job) })
	return nil
}

// storeOwnStory stores a story we are posting, as receivers will see it.
func (b *Backend) storeOwnStory(ctx context.Context, t *ownStoryTarget, ta *signalpb.TextAttachment, upload *preparedUpload) error {
	self := t.ID.Author
	st := core.Story{ID: t.ID, At: msTime(t.ID.Timestamp), Group: t.Group, Viewed: true}
	if ta != nil {
		conv, err := convertStory(self, t.Group, t.ID.Timestamp, &signalpb.StoryMessage{
			Attachment: &signalpb.StoryMessage_TextAttachment{TextAttachment: ta},
		})
		if err != nil {
			return err
		}
		st = conv.Story
		st.Viewed = true
	} else {
		key, err := b.blobs.Put(upload.Data)
		if err != nil {
			return err
		}
		t.AttID = hashID("a", t.ID.String(), string(roleStory), "0", "local", key)
		if err := b.db.PutAttachment(ctx, store.AttachmentRecord{
			ID: t.AttID, Owner: storyOwner(t.ID), BlobKey: key, ContentType: upload.ContentType,
			FileName: upload.FileName, Size: int64(len(upload.Data)), Downloaded: true,
		}); err != nil {
			return err
		}
		st.Media = &core.Attachment{
			ID: t.AttID, ContentType: upload.ContentType, FileName: upload.FileName, Size: int64(len(upload.Data)),
			Width: upload.Width, Height: upload.Height, Caption: upload.Caption, BlurHash: upload.BlurHash,
			Downloaded: true, Progress: 1,
		}
		st.Caption = upload.Caption
	}
	st.AuthorName = b.nameOf(ctx, self)
	if err := b.db.PutStory(ctx, st); err != nil {
		return err
	}
	// Group stories always allow replies; My Story's setting is known once
	// the distribution list has been read.
	return b.kv.set(ctx, storyKey(t.ID), storyMeta{AllowsReplies: true})
}

// deliverStory uploads the media of a posted story and sends it to every
// destination.
func (b *Backend) deliverStory(job *storyJob) {
	ctx, cancel := context.WithTimeout(b.log.WithContext(b.ctx), sendTimeout)
	defer cancel()
	log := zerolog.Ctx(ctx).With().Str("action", "post story").Logger()
	ctx = log.WithContext(ctx)
	fail := func(t ownStoryTarget, err error) {
		if b.ctx.Err() != nil {
			return // shutting down
		}
		log.Warn().Err(err).Uint64("ts", t.ID.Timestamp).Msg("Posting story failed")
		_ = b.deleteStory(ctx, t.ID, true)
		b.emit(core.ErrorEvent{Op: "story", Err: err})
	}
	cli, err := b.client()
	if err != nil {
		for _, t := range job.targets {
			fail(t, err)
		}
		return
	}
	var file *signalpb.AttachmentPointer
	if job.upload != nil {
		file, err = b.upload(ctx, cli, *job.upload, 0)
		if err != nil {
			for _, t := range job.targets {
				fail(t, err)
			}
			return
		}
		for _, t := range job.targets {
			b.rememberPointer(ctx, t.AttID, file, roleStory)
		}
	}
	text := job.text
	if text != nil && text.GetPreview().GetUrl() != "" {
		text = b.storyLinkPreview(ctx, cli, text)
	}
	sm, err := signalmeow.BuildStoryMessage(job.profileKey, text, file)
	if err != nil {
		for _, t := range job.targets {
			fail(t, err)
		}
		return
	}
	for _, t := range job.targets {
		var res *signalmeow.StorySendResult
		allowsReplies := true
		if t.Group == "" {
			recipients, privacy := b.myStoryAudience(ctx, cli)
			allowsReplies = privacy.AllowsReplies
			res, err = cli.SendListStory(ctx, t.ID.Timestamp, signalmeow.MyStoryID, privacy.AllowsReplies, sm, recipients)
		} else {
			gid, gerr := groupIdentifier(t.Group)
			if gerr != nil {
				fail(t, gerr)
				continue
			}
			res, err = cli.SendGroupStory(ctx, t.ID.Timestamp, gid, sm)
		}
		if err != nil && (res == nil || len(res.Sent) == 0) {
			fail(t, fmt.Errorf("backend: post story: %w", err))
			continue
		}
		if len(res.Failed) > 0 {
			log.Info().Int("failed", len(res.Failed)).Int("sent", len(res.Sent)).Msg("Story not delivered to everyone")
		}
		meta := ownStoryMeta(res, allowsReplies)
		if err := b.kv.set(ctx, storyKey(t.ID), meta); err != nil {
			log.Debug().Err(err).Msg("Failed to store story recipients")
		}
		b.emit(core.StoriesChanged{})
	}
}

// ownStoryMeta records who received a story we posted.
func ownStoryMeta(res *signalmeow.StorySendResult, allowsReplies bool) storyMeta {
	meta := storyMeta{AllowsReplies: allowsReplies}
	for _, r := range res.SyncRecipients {
		m := storyRecipientMeta{ACI: aciOf(r.ServiceID.UUID), Reply: r.AllowedToReply}
		for _, l := range r.ListIDs {
			m.Lists = append(m.Lists, l.String())
		}
		meta.Sent = append(meta.Sent, m)
	}
	meta.Recipients = len(meta.Sent)
	return meta
}

// storyLinkPreview fills in the title and image of a text story's link
// preview, if link previews are enabled (best effort, like Desktop's
// story creator).
func (b *Backend) storyLinkPreview(ctx context.Context, cli *signalmeow.Client, ta *signalpb.TextAttachment) *signalpb.TextAttachment {
	if ta.GetPreview().GetTitle() != "" {
		return ta
	}
	if settings, err := b.db.Settings(ctx); err != nil || !settings.LinkPreviews {
		return ta
	}
	pv, img := b.generatePreview(ctx, ta.GetPreview().GetUrl())
	if pv == nil {
		return ta
	}
	out := proto.Clone(ta).(*signalpb.TextAttachment)
	if pv.Title != "" {
		out.Preview.Title = &pv.Title
	}
	if img != nil {
		p := prepareUpload(core.OutgoingAttachment{FileName: "preview", Data: img})
		if ptr, err := b.upload(ctx, cli, p, 0); err == nil {
			out.Preview.Image = ptr
		}
	}
	return out
}

// myStoryAudience reads "My Story" from the storage service (Signal's
// defaults if there is none) and computes who receives it.
func (b *Backend) myStoryAudience(ctx context.Context, cli *signalmeow.Client) ([]libsignalgo.ServiceID, signalmeow.StoryPrivacy) {
	log := zerolog.Ctx(ctx)
	privacy := signalmeow.DefaultMyStory()
	if lists, err := cli.StoryDistributionLists(ctx); err == nil {
		privacy = signalmeow.MyStoryPrivacy(lists)
	} else {
		log.Debug().Err(err).Msg("No story distribution lists, using My Story defaults")
	}
	base, err := cli.StoryConnections(ctx)
	if err != nil {
		log.Debug().Err(err).Msg("Failed to load story connections")
	}
	var chats []core.Chat
	for _, archived := range []bool{false, true} {
		cs, err := b.db.ListChats(ctx, core.ChatFilter{Archived: archived, DirectOnly: true})
		if err == nil {
			chats = append(chats, cs...)
		}
	}
	self := b.selfACI()
	exclude := func(sid libsignalgo.ServiceID) bool {
		aci := aciOf(sid.UUID)
		return aci == self || b.senderBlocked(ctx, aci) || cli.Store.RecipientStore.IsUnregistered(ctx, sid)
	}
	recipients := signalmeow.StoryAudience(privacy, storyConnectionSet(base, chats, self), exclude)
	log.Debug().Int("recipients", len(recipients)).Int("mode", int(privacy.Mode())).Msg("Computed My Story audience")
	return recipients, privacy
}

// storySyncRecipients converts stored story recipients back for the
// delete sync.
func storySyncRecipients(sent []storyRecipientMeta) []signalmeow.StorySyncRecipient {
	out := make([]signalmeow.StorySyncRecipient, 0, len(sent))
	for _, r := range sent {
		sid, err := aciServiceID(r.ACI)
		if err != nil {
			continue
		}
		out = append(out, signalmeow.StorySyncRecipient{ServiceID: sid, AllowedToReply: r.Reply})
	}
	return out
}

// deleteStoryRequest handles Delete for a story ID: handled is false if
// id is not a story. Deleting for everyone works for our own stories:
// group stories get a normal group delete (Desktop sends them as regular
// delete-for-everyone messages); distribution-list stories get a 1:1
// story=true delete to each recipient plus a recipient-update sync.
func (b *Backend) deleteStoryRequest(ctx context.Context, id core.MessageID, forEveryone bool) (handled bool, err error) {
	if _, err := b.db.GetMessage(ctx, id); !errors.Is(err, core.ErrNotFound) {
		return false, nil
	}
	s, err := b.db.GetStory(ctx, id)
	if err != nil {
		return false, nil
	}
	id = s.ID
	if !forEveryone {
		return true, b.deleteStory(ctx, id, true)
	}
	if id.Author != b.selfACI() {
		return true, errorf(core.ErrReadOnly, "only the author can delete a story for everyone")
	}
	cli, err := b.client()
	if err != nil {
		return true, err
	}
	if s.Group != "" {
		if _, err := b.sendDataMessage(ctx, cli, s.Group, buildDelete(b.nextTimestamp(), id.Timestamp)); err != nil {
			return true, err
		}
		return true, b.deleteStory(ctx, id, true)
	}
	var meta storyMeta
	_, _ = b.kv.get(ctx, storyKey(id), &meta)
	if _, _, err := cli.DeleteListStory(ctx, id.Timestamp, storySyncRecipients(meta.Sent)); err != nil {
		return true, err
	}
	return true, b.deleteStory(ctx, id, true)
}
