package backend

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-signal/pkg/signalmeow"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"

	"warte/internal/core"
	"warte/internal/store"
)

const (
	// sendTimeout bounds one background delivery (uploads included).
	sendTimeout = 10 * time.Minute
	// editWindow and maxEdits mirror Signal's limits on editing.
	editWindow = 24 * time.Hour
	maxEdits   = 10
	// deleteWindow is how long after sending delete-for-everyone works.
	deleteWindow = 24 * time.Hour
	// maxPinned is how many messages a chat keeps pinned.
	maxPinned = 3
)

// sendResult is the per-recipient outcome of a send.
type sendResult map[core.ACI]bool

// sendContent delivers content to a chat: one recipient for direct chats
// (our own devices only for Note to Self), the members for groups.
func (b *Backend) sendContent(ctx context.Context, cli *signalmeow.Client, chat core.ChatID, content *signalpb.Content) (sendResult, error) {
	switch {
	case chat.IsGroup():
		gid, err := groupIdentifier(chat)
		if err != nil {
			return nil, err
		}
		res, err := cli.SendGroupMessage(ctx, gid, content)
		out := sendResult{}
		if res != nil {
			for _, s := range res.SuccessfullySentTo {
				out[aciOf(s.Recipient.UUID)] = true
			}
			for _, f := range res.FailedToSendTo {
				out[aciOf(f.Recipient.UUID)] = false
			}
		}
		if err != nil {
			return out, fmt.Errorf("backend: send to group: %w", err)
		}
		return out, nil
	case chat.IsDirect():
		sid, err := aciServiceID(chat.ACI())
		if err != nil {
			return nil, err
		}
		res := cli.SendMessage(ctx, sid, content)
		out := sendResult{chat.ACI(): res.WasSuccessful}
		if !res.WasSuccessful {
			return out, fmt.Errorf("backend: send: %w", res.Error)
		}
		return out, nil
	}
	return nil, fmt.Errorf("%w: chat %q", errBadID, chat)
}

// sendDataMessage wraps and sends a DataMessage.
func (b *Backend) sendDataMessage(ctx context.Context, cli *signalmeow.Client, chat core.ChatID, dm *signalpb.DataMessage) (sendResult, error) {
	return b.sendContent(ctx, cli, chat, signalmeow.WrapDataMessage(dm))
}

// recordSendResult folds per-recipient results into receipts.
func (b *Backend) recordSendResult(ctx context.Context, id core.MessageID, res sendResult, sendErr error) {
	self := b.selfACI()
	if len(res) == 0 {
		status := core.StatusSent
		if sendErr != nil {
			status = core.StatusFailed
		}
		if _, err := b.db.SetReceipt(ctx, id, self, status); err != nil {
			zerolog.Ctx(ctx).Debug().Err(err).Msg("Failed to record send status")
		}
	}
	for aci, ok := range res {
		status := core.StatusSent
		if !ok {
			status = core.StatusFailed
		}
		if _, err := b.db.SetReceipt(ctx, id, aci, status); err != nil {
			zerolog.Ctx(ctx).Debug().Err(err).Msg("Failed to record send status")
		}
	}
	b.emitMessage(ctx, id)
	b.emitChat(ctx, id.Chat)
}

// sendableChat loads a chat for sending, creating direct chats on demand.
func (b *Backend) sendableChat(ctx context.Context, id core.ChatID) (core.Chat, error) {
	c, err := b.db.GetChat(ctx, id)
	if errors.Is(err, core.ErrNotFound) && id.IsDirect() {
		if _, err := b.ensureDirectChat(ctx, id.ACI(), core.Contact{}); err != nil {
			return core.Chat{}, err
		}
		c, err = b.db.GetChat(ctx, id)
	}
	if err != nil {
		return core.Chat{}, err
	}
	if err := canSend(c, b.selfACI()); err != nil {
		return core.Chat{}, err
	}
	return c, nil
}

// outgoingJob is a message stored as Pending and waiting for delivery.
type outgoingJob struct {
	id       core.MessageID
	chat     core.Chat
	out      core.Outgoing
	uploads  []preparedUpload
	attIDs   []string
	quote    *core.Message
	sticker  []byte
	previews bool
}

// Send stores the message as Pending, announces it and delivers it in the
// background; status changes arrive as MessageUpdated events.
func (b *Backend) Send(ctx context.Context, chat core.ChatID, out core.Outgoing) (core.MessageID, error) {
	if err := validateOutgoing(out); err != nil {
		return core.MessageID{}, err
	}
	if _, err := b.client(); err != nil {
		return core.MessageID{}, err
	}
	c, err := b.sendableChat(ctx, chat)
	if err != nil {
		return core.MessageID{}, err
	}
	self := b.selfACI()
	now := b.now().UTC()
	id := core.MessageID{Chat: chat, Author: self, Timestamp: b.nextTimestamp()}
	m := core.Message{
		ID: id, Kind: core.KindNormal, Outgoing: true, SentAt: msTime(id.Timestamp), ReceivedAt: now,
		Status: core.StatusPending, Body: out.Body, Ranges: trimRanges(out.Body, out.Ranges),
		ViewOnce: out.ViewOnce, ExpiresIn: c.ExpireTimer,
	}
	if m.ExpiresIn > 0 {
		m.ExpireStartedAt = now
	}
	job := &outgoingJob{id: id, chat: c, out: out}
	if out.Quote != nil {
		q, err := b.db.GetMessage(ctx, *out.Quote)
		if err != nil {
			return core.MessageID{}, fmt.Errorf("backend: quoted message: %w", err)
		}
		job.quote = &q
		m.Quote = quoteOf(q)
	}
	for i, a := range out.Attachments {
		p := prepareUpload(a)
		att, err := b.storeOutgoingAttachment(ctx, id, i, p, roleBody)
		if err != nil {
			return core.MessageID{}, err
		}
		m.Attachments = append(m.Attachments, att)
		job.uploads = append(job.uploads, p)
		job.attIDs = append(job.attIDs, att.ID)
	}
	if s := out.Sticker; s != nil {
		data, err := b.StickerImage(ctx, s.PackID, s.StickerID)
		if err != nil {
			return core.MessageID{}, fmt.Errorf("backend: sticker: %w", err)
		}
		st := *s
		p := prepareUpload(core.OutgoingAttachment{FileName: "sticker", Data: data})
		att, err := b.storeOutgoingAttachment(ctx, id, 0, p, roleSticker)
		if err != nil {
			return core.MessageID{}, err
		}
		att.Borderless = true
		st.Attachment = att
		m.Sticker = &st
		job.sticker = data
	}
	if p := out.Poll; p != nil {
		poll := &core.Poll{Question: strings.TrimSpace(p.Question), AllowMultiple: p.AllowMultiple}
		for _, o := range p.Options {
			poll.Options = append(poll.Options, core.PollOption{Text: strings.TrimSpace(o.Text)})
		}
		m.Poll = poll
		job.out.Poll = poll
	}
	if out.Preview != nil {
		pv := *out.Preview
		m.Preview = &pv
	} else if settings, err := b.db.Settings(ctx); err == nil && settings.LinkPreviews && !out.ViewOnce && firstURL(out.Body) != "" {
		job.previews = true
	}
	if _, err := b.db.PutMessage(ctx, m, store.AsRead()); err != nil {
		return core.MessageID{}, err
	}
	if err := b.index.put(ctx, id, 0, "", nil); err != nil {
		return core.MessageID{}, err
	}
	if err := b.db.SetDraft(ctx, chat, ""); err != nil {
		zerolog.Ctx(ctx).Debug().Err(err).Msg("Failed to clear draft")
	}
	b.unarchiveOnActivity(ctx, c, true)
	if stored, err := b.db.GetMessage(ctx, id); err == nil {
		b.emit(core.MessageAdded{Message: stored})
	}
	b.emitChat(ctx, chat)
	if m.ExpiresIn > 0 {
		b.kickExpiry()
	}
	b.goBackground(func() { b.deliver(job) })
	return id, nil
}

// quoteOf builds the local quote of q.
func quoteOf(q core.Message) *core.Quote {
	out := &core.Quote{ID: q.ID, Text: q.Body, Ranges: q.Ranges, OriginalFound: true}
	if q.Poll != nil {
		out.Text = "📊 " + q.Poll.Question
	}
	switch {
	case len(q.Attachments) > 0:
		a := q.Attachments[0]
		out.Attachment = &a
	case q.Sticker != nil:
		a := q.Sticker.Attachment
		out.Attachment = &a
	}
	return out
}

// deliver uploads and sends a stored outgoing message.
func (b *Backend) deliver(job *outgoingJob) {
	ctx, cancel := context.WithTimeout(b.log.WithContext(b.ctx), sendTimeout)
	defer cancel()
	lock := b.chatLock(job.id.Chat)
	lock.Lock()
	defer lock.Unlock()
	res, err := b.deliverLocked(ctx, job)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Str("chat", string(job.id.Chat)).Msg("Send failed")
		b.emit(core.ErrorEvent{Op: "send", Err: err})
	}
	if ctx.Err() != nil && errors.Is(err, context.Canceled) && b.ctx.Err() != nil {
		return // shutting down; leave it Pending so it can be retried
	}
	b.recordSendResult(ctx, job.id, res, err)
	if err == nil && job.out.ViewOnce {
		// Like Signal, the sender can't reopen view-once media.
		if err := b.markViewOnceOpened(ctx, job.id); err != nil {
			zerolog.Ctx(ctx).Debug().Err(err).Msg("Failed to close sent view-once media")
		}
	}
}

func (b *Backend) deliverLocked(ctx context.Context, job *outgoingJob) (sendResult, error) {
	cli, err := b.client()
	if err != nil {
		return nil, err
	}
	spec := sendSpec{
		Timestamp:   job.id.Timestamp,
		Ranges:      job.out.Ranges,
		Quote:       job.quote,
		Poll:        job.out.Poll,
		ViewOnce:    job.out.ViewOnce,
		ExpireTimer: job.chat.ExpireTimer,
		ProfileKey:  profileKey(ctx, cli),
	}
	if job.chat.ExpireTimer > 0 && job.chat.ID.IsDirect() {
		var v uint32
		_, _ = b.kv.get(ctx, "timer_version."+string(job.chat.ID), &v)
		spec.TimerVersion = max(v, 1)
	}
	body := strings.TrimSpace(job.out.Body)
	if body != "" {
		inline, long := splitLongBody(job.out.Body)
		spec.Body = inline
		spec.Ranges = trimRanges(inline, job.out.Ranges)
		if long {
			p := preparedUpload{OutgoingAttachment: core.OutgoingAttachment{
				FileName: "long-message.txt", ContentType: longTextContentType, Data: []byte(job.out.Body),
			}}
			ptr, err := b.upload(ctx, cli, p, 0)
			if err != nil {
				return nil, err
			}
			spec.Attachments = append(spec.Attachments, ptr)
		}
	}
	for i, p := range job.uploads {
		ptr, err := b.upload(ctx, cli, p, pointerFlags(p.OutgoingAttachment, false, false))
		if err != nil {
			return nil, err
		}
		b.rememberPointer(ctx, job.attIDs[i], ptr, roleBody)
		spec.Attachments = append(spec.Attachments, ptr)
	}
	if job.out.Sticker != nil {
		p := prepareUpload(core.OutgoingAttachment{FileName: "sticker", Data: job.sticker})
		ptr, err := b.upload(ctx, cli, p, uint32(signalpb.AttachmentPointer_BORDERLESS))
		if err != nil {
			return nil, err
		}
		ptr.FileName = nil
		spec.Sticker, spec.StickerData = job.out.Sticker, ptr
	}
	if job.out.Preview != nil {
		spec.Preview = job.out.Preview
	} else if job.previews {
		if pv, img := b.generatePreview(ctx, job.out.Body); pv != nil {
			spec.Preview = pv
			if img != nil {
				p := prepareUpload(core.OutgoingAttachment{FileName: "preview", Data: img})
				if ptr, err := b.upload(ctx, cli, p, 0); err == nil {
					spec.PreviewImage = ptr
					if att, err := b.storeOutgoingAttachment(ctx, job.id, 0, p, rolePreview); err == nil {
						pv.Image = &att
						b.rememberPointer(ctx, att.ID, ptr, rolePreview)
					}
				}
			}
			if m, err := b.db.UpdateMessage(ctx, job.id, func(m *core.Message) error { m.Preview = pv; return nil }); err == nil {
				b.emit(core.MessageUpdated{Message: m})
			}
		}
	}
	if spec.Preview != nil && spec.PreviewImage == nil && spec.Preview.Image != nil {
		if data, err := b.fetchAttachment(ctx, job.id, spec.Preview.Image.ID); err == nil {
			p := prepareUpload(core.OutgoingAttachment{FileName: "preview", Data: data})
			if ptr, err := b.upload(ctx, cli, p, 0); err == nil {
				spec.PreviewImage = ptr
			}
		}
	}
	dm := buildDataMessage(spec)
	return b.sendDataMessage(ctx, cli, job.id.Chat, dm)
}

// ownMessage loads a message we sent, for actions only its author may do.
func (b *Backend) ownMessage(ctx context.Context, id core.MessageID) (core.Message, error) {
	m, err := b.db.GetMessage(ctx, id)
	if err != nil {
		return core.Message{}, err
	}
	if !m.Outgoing || m.ID.Author != b.selfACI() {
		return core.Message{}, errorf(core.ErrReadOnly, "only the author can do that")
	}
	return m, nil
}

// remotePointers returns the stored Signal pointers of m's attachments, in
// order, skipping any we cannot reference.
func (b *Backend) remotePointers(ctx context.Context, m core.Message) []*signalpb.AttachmentPointer {
	var out []*signalpb.AttachmentPointer
	for _, a := range m.Attachments {
		rec, err := b.db.GetAttachment(ctx, a.ID)
		if err != nil || len(rec.Remote) == 0 {
			continue
		}
		if ref, err := decodeRemote(rec.Remote); err == nil && hasLocation(ref.Pointer) {
			out = append(out, ref.Pointer)
		}
	}
	return out
}

// Edit changes the text of one of our messages and sends the edit.
func (b *Backend) Edit(ctx context.Context, id core.MessageID, body string, ranges []core.BodyRange) error {
	cli, err := b.client()
	if err != nil {
		return err
	}
	m, err := b.ownMessage(ctx, id)
	if err != nil {
		return err
	}
	switch {
	case m.Kind != core.KindNormal || m.RemoteDeleted || m.ViewOnce || m.Poll != nil || m.Sticker != nil:
		return errorf(core.ErrReadOnly, "this message can't be edited")
	case strings.TrimSpace(body) == "" && len(m.Attachments) == 0:
		return errEmptyMessage
	case len(m.EditHistory) >= maxEdits:
		return errorf(core.ErrReadOnly, "a message can be edited at most %d times", maxEdits)
	case id.Chat.ACI() != b.selfACI() && b.now().Sub(m.SentAt) > editWindow:
		return errorf(core.ErrReadOnly, "messages can only be edited for 24 hours")
	}
	if len([]byte(body)) > inlineBodyLimit {
		return fmt.Errorf("backend: edited text is longer than %d bytes", inlineBodyLimit)
	}
	ranges = trimRanges(body, ranges)
	ts := b.nextTimestamp()
	spec := sendSpec{
		Timestamp:   ts,
		Body:        body,
		Ranges:      ranges,
		Attachments: b.remotePointers(ctx, m),
		ExpireTimer: m.ExpiresIn,
	}
	if m.Quote != nil {
		if q, err := b.db.GetMessage(ctx, m.Quote.ID); err == nil {
			spec.Quote = &q
		}
	}
	if m.Preview != nil && strings.Contains(body, m.Preview.URL) {
		spec.Preview = m.Preview
	}
	content := signalmeow.WrapEditMessage(buildEditMessage(id.Timestamp, buildDataMessage(spec)))
	if _, err := b.sendContent(ctx, cli, id.Chat, content); err != nil {
		return err
	}
	prevAt := m.SentAt
	if last, err := b.index.lastEdit(ctx, id); err == nil && last > 0 {
		prevAt = msTime(last)
	}
	edited := core.Message{Body: body, Ranges: ranges, Preview: spec.Preview}
	updated, err := b.db.UpdateMessage(ctx, id, func(mm *core.Message) error {
		applyEdit(mm, edited, prevAt)
		return nil
	})
	if err != nil {
		return err
	}
	_ = b.index.put(ctx, id, ts, "", nil)
	b.emit(core.MessageUpdated{Message: updated})
	b.emitChat(ctx, id.Chat)
	return nil
}

// Delete deletes a message for us (syncing to our other devices) or, for
// our own recent messages, for everyone. Story IDs delete the story
// (stories_post.go).
func (b *Backend) Delete(ctx context.Context, id core.MessageID, forEveryone bool) error {
	if handled, err := b.deleteStoryRequest(ctx, id, forEveryone); handled {
		return err
	}
	if !forEveryone {
		if _, err := b.db.GetMessage(ctx, id); err != nil {
			return err
		}
		if err := b.deleteLocal(ctx, id); err != nil {
			return err
		}
		if cli, err := b.client(); err == nil {
			if sm, err := syncDeleteMessages(id.Chat, []core.MessageID{id}); err == nil {
				if err := b.sendSync(ctx, cli, sm); err != nil {
					zerolog.Ctx(ctx).Debug().Err(err).Msg("Failed to sync delete")
				}
			}
		}
		return nil
	}
	cli, err := b.client()
	if err != nil {
		return err
	}
	m, err := b.ownMessage(ctx, id)
	if err != nil {
		return err
	}
	if m.RemoteDeleted {
		return nil
	}
	if m.Kind != core.KindNormal && m.Kind != core.KindStoryReply {
		return errorf(core.ErrReadOnly, "this message can't be deleted for everyone")
	}
	if id.Chat.ACI() != b.selfACI() && b.now().Sub(m.SentAt) > deleteWindow {
		return errorf(core.ErrReadOnly, "messages can only be deleted for everyone for 24 hours")
	}
	if _, err := b.sendDataMessage(ctx, cli, id.Chat, buildDelete(b.nextTimestamp(), id.Timestamp)); err != nil {
		return err
	}
	return b.applyRemoteDelete(ctx, id)
}

// React sets or removes our reaction on a message.
func (b *Backend) React(ctx context.Context, id core.MessageID, emoji string, remove bool) error {
	cli, err := b.client()
	if err != nil {
		return err
	}
	m, err := b.db.GetMessage(ctx, id)
	if err != nil {
		return err
	}
	if m.Kind == core.KindSystem || m.RemoteDeleted {
		return errorf(core.ErrReadOnly, "can't react to this message")
	}
	self := b.selfACI()
	if remove {
		emoji = ""
		for _, r := range m.Reactions {
			if r.From == self {
				emoji = r.Emoji
			}
		}
		if emoji == "" {
			return nil
		}
	} else if strings.TrimSpace(emoji) == "" {
		return errors.New("backend: empty reaction")
	}
	dm, err := buildReaction(b.nextTimestamp(), id, emoji, remove)
	if err != nil {
		return err
	}
	if _, err := b.sendDataMessage(ctx, cli, id.Chat, dm); err != nil {
		return err
	}
	r := core.Reaction{Emoji: emoji, From: self, At: b.now().UTC()}
	if remove {
		r.Emoji = ""
	}
	updated, err := b.db.SetReaction(ctx, id, r)
	if err != nil {
		return err
	}
	b.emit(core.MessageUpdated{Message: updated})
	return nil
}

// Forward re-sends a message's content to other chats.
func (b *Backend) Forward(ctx context.Context, id core.MessageID, to []core.ChatID) error {
	m, err := b.db.GetMessage(ctx, id)
	if err != nil {
		return err
	}
	if m.Kind != core.KindNormal || m.RemoteDeleted || m.ViewOnce {
		return errorf(core.ErrReadOnly, "this message can't be forwarded")
	}
	out := core.Outgoing{}
	switch {
	case m.Sticker != nil:
		st := *m.Sticker
		out.Sticker = &st
	case m.Poll != nil:
		return errorf(core.ErrReadOnly, "polls can't be forwarded")
	default:
		out.Body, out.Ranges = m.Body, withoutMentions(m.Ranges)
		for _, a := range m.Attachments {
			data, err := b.fetchAttachment(ctx, id, a.ID)
			if err != nil {
				return fmt.Errorf("backend: forward attachment: %w", err)
			}
			out.Attachments = append(out.Attachments, core.OutgoingAttachment{
				FileName: a.FileName, ContentType: a.ContentType, Data: data,
				VoiceNote: a.VoiceNote, Width: a.Width, Height: a.Height, Caption: a.Caption,
			})
		}
		if m.Preview != nil {
			pv := *m.Preview
			out.Preview = &pv
		}
	}
	var errs []error
	for _, chat := range to {
		if _, err := b.Send(ctx, chat, out); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", chat, err))
		}
	}
	return errors.Join(errs...)
}

// withoutMentions drops mention ranges (they would point at people who may
// not be in the destination chat) but keeps formatting.
func withoutMentions(rs []core.BodyRange) []core.BodyRange {
	return slices.DeleteFunc(slices.Clone(rs), func(r core.BodyRange) bool { return r.Style == core.StyleMention })
}

// MarkRead marks a chat read up to upTo (zero = everything), sends read
// receipts if enabled and syncs the read state to our other devices.
func (b *Backend) MarkRead(ctx context.Context, chat core.ChatID, upTo core.MessageID) error {
	ids, err := b.db.MarkRead(ctx, chat, upTo)
	if err != nil {
		return err
	}
	b.emitReadChanges(ctx, chat, ids)
	if len(ids) == 0 {
		return nil
	}
	cli, err := b.client()
	if err != nil {
		return nil // read locally; nothing to tell others while offline
	}
	settings, _ := b.db.Settings(ctx)
	byAuthor := map[core.ACI][]uint64{}
	var syncIDs []core.MessageID
	self := b.selfACI()
	for _, id := range ids {
		if id.Author == self || id.Author == "" {
			continue
		}
		if m, err := b.db.GetMessage(ctx, id); err != nil || (m.Kind != core.KindNormal && m.Kind != core.KindStoryReply) {
			continue
		}
		byAuthor[id.Author] = append(byAuthor[id.Author], id.Timestamp)
		syncIDs = append(syncIDs, id)
	}
	if len(syncIDs) == 0 {
		return nil
	}
	b.goBackground(func() {
		ctx, cancel := context.WithTimeout(b.log.WithContext(b.ctx), time.Minute)
		defer cancel()
		if !settings.ReadReceipts {
			if err := b.sendSync(ctx, cli, syncRead(syncIDs)); err != nil {
				zerolog.Ctx(ctx).Debug().Err(err).Msg("Failed to sync read state")
			}
			return
		}
		// signalmeow sends the read sync to our devices with each receipt.
		for author, tss := range byAuthor {
			sid, err := aciServiceID(author)
			if err != nil {
				continue
			}
			res := cli.SendMessage(ctx, sid, signalmeow.ReadReceptMessageForTimestamps(tss))
			if !res.WasSuccessful {
				zerolog.Ctx(ctx).Debug().Err(res.Error).Msg("Failed to send read receipt")
			}
		}
	})
	return nil
}

// MarkViewOnceOpened marks view-once media opened, destroys it locally and
// tells the sender (viewed receipt) and our other devices.
func (b *Backend) MarkViewOnceOpened(ctx context.Context, id core.MessageID) error {
	if err := b.markViewOnceOpened(ctx, id); err != nil {
		return err
	}
	cli, err := b.client()
	if err != nil {
		return nil
	}
	settings, _ := b.db.Settings(ctx)
	if id.Author != b.selfACI() && settings.ReadReceipts {
		sid, err := aciServiceID(id.Author)
		if err != nil {
			return err
		}
		receipt := &signalpb.Content{Content: &signalpb.Content_ReceiptMessage{ReceiptMessage: &signalpb.ReceiptMessage{
			Type: signalpb.ReceiptMessage_VIEWED.Enum(), Timestamp: []uint64{id.Timestamp},
		}}}
		// signalmeow turns a single viewed receipt into a ViewOnceOpen sync.
		if res := cli.SendMessage(ctx, sid, receipt); !res.WasSuccessful {
			return fmt.Errorf("backend: viewed receipt: %w", res.Error)
		}
		return nil
	}
	sm, err := syncViewOnceOpen(id)
	if err != nil {
		return err
	}
	return b.sendSync(ctx, cli, sm)
}

// SetTyping sends a typing indicator (if enabled in settings).
func (b *Backend) SetTyping(ctx context.Context, chat core.ChatID, typing bool) error {
	cli, err := b.client()
	if err != nil {
		return err
	}
	if settings, err := b.db.Settings(ctx); err == nil && !settings.TypingIndicators {
		return nil
	}
	if chat.IsDirect() && chat.ACI() == b.selfACI() {
		return nil
	}
	c, err := b.db.GetChat(ctx, chat)
	if err != nil {
		return err
	}
	if c.Blocked || c.MessageRequest {
		return nil
	}
	_, err = b.sendContent(ctx, cli, chat, signalmeow.TypingMessage(typing))
	return err
}

// SaveDraft stores the composer draft of a chat.
func (b *Backend) SaveDraft(ctx context.Context, chat core.ChatID, draft string) error {
	if err := b.db.SetDraft(ctx, chat, draft); err != nil {
		return err
	}
	b.emitChat(ctx, chat)
	return nil
}

// VotePoll replaces our vote on a poll.
func (b *Backend) VotePoll(ctx context.Context, id core.MessageID, options []int) error {
	cli, err := b.client()
	if err != nil {
		return err
	}
	m, err := b.db.GetMessage(ctx, id)
	if err != nil {
		return err
	}
	if m.Poll == nil || m.RemoteDeleted {
		return errorf(core.ErrReadOnly, "not a poll")
	}
	if m.Poll.Ended {
		return errorf(core.ErrReadOnly, "the poll has ended")
	}
	seen := map[int]bool{}
	var idx []uint32
	for _, o := range options {
		if o < 0 || o >= len(m.Poll.Options) {
			return fmt.Errorf("backend: poll option %d out of range", o)
		}
		if !seen[o] {
			seen[o] = true
			idx = append(idx, uint32(o))
		}
	}
	if len(seen) > 1 && !m.Poll.AllowMultiple {
		return errors.New("backend: this poll allows only one choice")
	}
	self := b.selfACI()
	key := "poll_votes." + id.String()
	counts := map[core.ACI]uint32{}
	_, _ = b.kv.get(ctx, key, &counts)
	count := counts[self] + 1
	sorted := make([]int, 0, len(idx))
	for _, i := range idx {
		sorted = append(sorted, int(i))
	}
	dm, err := buildPollVote(b.nextTimestamp(), id, sorted, count)
	if err != nil {
		return err
	}
	if _, err := b.sendDataMessage(ctx, cli, id.Chat, dm); err != nil {
		return err
	}
	counts[self] = count
	_ = b.kv.set(ctx, key, counts)
	updated, err := b.db.UpdateMessage(ctx, id, func(mm *core.Message) error {
		applyPollVote(mm.Poll, self, idx)
		return nil
	})
	if err != nil {
		return err
	}
	b.emit(core.MessageUpdated{Message: updated})
	return nil
}

// EndPoll closes one of our polls.
func (b *Backend) EndPoll(ctx context.Context, id core.MessageID) error {
	cli, err := b.client()
	if err != nil {
		return err
	}
	m, err := b.ownMessage(ctx, id)
	if err != nil {
		return err
	}
	if m.Poll == nil {
		return errorf(core.ErrReadOnly, "not a poll")
	}
	if m.Poll.Ended {
		return nil
	}
	if _, err := b.sendDataMessage(ctx, cli, id.Chat, buildPollTerminate(b.nextTimestamp(), id.Timestamp)); err != nil {
		return err
	}
	updated, err := b.db.UpdateMessage(ctx, id, func(mm *core.Message) error { mm.Poll.Ended = true; return nil })
	if err != nil {
		return err
	}
	b.emit(core.MessageUpdated{Message: updated})
	return nil
}

// PinMessage pins (for duration, 0 = forever) or unpins a message for
// everyone in the chat.
func (b *Backend) PinMessage(ctx context.Context, id core.MessageID, pin bool, duration time.Duration) error {
	cli, err := b.client()
	if err != nil {
		return err
	}
	m, err := b.db.GetMessage(ctx, id)
	if err != nil {
		return err
	}
	if pin && (m.Kind != core.KindNormal || m.RemoteDeleted) {
		return errorf(core.ErrReadOnly, "can't pin this message")
	}
	if m.Pinned == pin && !pin {
		return nil
	}
	ts := b.nextTimestamp()
	dm, err := buildPin(ts, id, pin, duration)
	if err != nil {
		return err
	}
	if _, err := b.sendDataMessage(ctx, cli, id.Chat, dm); err != nil {
		return err
	}
	if err := b.setPinned(ctx, id, pin, duration); err != nil {
		return err
	}
	if pin {
		b.enforcePinLimit(ctx, id)
	}
	self := b.selfACI()
	cc := convCtx{Self: self, Chat: id.Chat, Sender: self, ReceivedAt: b.now().UTC()}
	return b.storeSystem(ctx, cc, 0, ts, pinText(self, self, b.names(ctx), pin), false)
}

// enforcePinLimit unpins the oldest pins beyond maxPinned locally (other
// clients apply the same rule).
func (b *Backend) enforcePinLimit(ctx context.Context, keep core.MessageID) {
	hits, err := b.db.ListMessages(ctx, keep.Chat, core.MessageID{}, 1000)
	if err != nil {
		return
	}
	var pinned []core.Message
	for _, m := range hits {
		if m.Pinned && m.ID != keep {
			pinned = append(pinned, m)
		}
	}
	for len(pinned) >= maxPinned {
		_ = b.setPinned(ctx, pinned[0].ID, false, 0)
		pinned = pinned[1:]
	}
}

// --- chat management --------------------------------------------------------------

// UpdateChat changes chat state. Archive, pin, mute and marked-unread apply
// locally at once and are written back to the storage service (batched,
// see storage_write.go); the disappearing timer is sent to the chat.
func (b *Backend) UpdateChat(ctx context.Context, id core.ChatID, u core.ChatUpdate) error {
	if u.ExpireTimer != nil {
		if err := b.setExpireTimer(ctx, id, *u.ExpireTimer); err != nil {
			return err
		}
		u.ExpireTimer = nil
	}
	if u.Archived == nil && u.Pinned == nil && u.MarkedUnread == nil && u.MutedUntil == nil {
		return nil
	}
	before, err := b.db.GetChat(ctx, id)
	if err != nil {
		return err
	}
	after, err := b.db.ApplyChatUpdate(ctx, id, u)
	if err != nil {
		return err
	}
	b.emitChat(ctx, id)
	// warte: write back what was asked for, plus the pin that archiving or
	// pinning implicitly changed (pinning unarchives, archiving unpins).
	f := chatStorageFields{Archived: u.Archived, MarkedUnread: u.MarkedUnread, MutedUntil: u.MutedUntil, Pinned: u.Pinned}
	if after.Pinned != before.Pinned {
		f.Pinned = &after.Pinned
	}
	if after.Archived != before.Archived {
		f.Archived = &after.Archived
	}
	b.queueChatStorage(ctx, id, f)
	return nil
}

// setExpireTimer changes a chat's disappearing-message timer for everyone.
func (b *Backend) setExpireTimer(ctx context.Context, id core.ChatID, d time.Duration) error {
	if d < 0 {
		return errors.New("backend: negative timer")
	}
	d = d.Truncate(time.Second)
	if id.IsGroup() {
		return b.UpdateGroup(ctx, id, core.GroupUpdate{ExpireTimer: &d})
	}
	cli, err := b.client()
	if err != nil {
		return err
	}
	c, err := b.sendableChat(ctx, id)
	if err != nil {
		return err
	}
	if c.ExpireTimer == d {
		return nil
	}
	var v uint32
	_, _ = b.kv.get(ctx, "timer_version."+string(id), &v)
	v++
	ts := b.nextTimestamp()
	if _, err := b.sendDataMessage(ctx, cli, id, buildTimerUpdate(ts, d, v, profileKey(ctx, cli))); err != nil {
		return err
	}
	_ = b.kv.set(ctx, "timer_version."+string(id), v)
	if _, err := b.db.UpdateChat(ctx, id, func(ch *core.Chat) error { ch.ExpireTimer = d; return nil }); err != nil {
		return err
	}
	self := b.selfACI()
	cc := convCtx{Self: self, Chat: id, Sender: self, ReceivedAt: b.now().UTC()}
	return b.storeSystem(ctx, cc, 0, ts, timerChangeText(self, self, b.names(ctx), d), false)
}

// DeleteChat deletes a chat and its messages here and on our other devices.
func (b *Backend) DeleteChat(ctx context.Context, id core.ChatID) error {
	recent, err := b.db.ListMessages(ctx, id, core.MessageID{}, 5)
	if err != nil {
		return err
	}
	if err := b.deleteChatLocal(ctx, id); err != nil {
		return err
	}
	cli, err := b.client()
	if err != nil {
		return nil
	}
	ids := make([]core.MessageID, 0, len(recent))
	for _, m := range recent {
		ids = append(ids, m.ID)
	}
	if sm, err := syncDeleteConversation(id, ids); err == nil {
		if err := b.sendSync(ctx, cli, sm); err != nil {
			zerolog.Ctx(ctx).Debug().Err(err).Msg("Failed to sync chat delete")
		}
	}
	return nil
}

// --- stories --------------------------------------------------------------------

// MarkStoryViewed is in stories.go.

// ReplyToStory replies to a story with text, or reacts with emoji.
func (b *Backend) ReplyToStory(ctx context.Context, id core.MessageID, body string, emoji string) error {
	cli, err := b.client()
	if err != nil {
		return err
	}
	if strings.TrimSpace(body) == "" && emoji == "" {
		return errEmptyMessage
	}
	if !b.storyAllowsReplies(ctx, id) {
		return errorf(core.ErrReadOnly, "this story does not allow replies")
	}
	chat := core.DirectChatID(id.Author)
	if s, err := b.db.GetStory(ctx, id); err == nil && s.Group != "" {
		chat = s.Group
	}
	if _, err := b.sendableChat(ctx, chat); err != nil {
		return err
	}
	ts := b.nextTimestamp()
	ref := &core.StoryRef{Author: id.Author, Timestamp: id.Timestamp}
	var dm *signalpb.DataMessage
	if emoji != "" {
		dm, err = buildReaction(ts, core.MessageID{Chat: chat, Author: id.Author, Timestamp: id.Timestamp}, emoji, false)
		if err != nil {
			return err
		}
		dm.StoryContext = &signalpb.DataMessage_StoryContext{AuthorAci: proto.String(string(id.Author)), SentTimestamp: proto.Uint64(id.Timestamp)}
		ref.Emoji = emoji
	} else {
		dm = buildDataMessage(sendSpec{Timestamp: ts, Body: body, StoryContext: ref, ProfileKey: profileKey(ctx, cli)})
	}
	res, sendErr := b.sendDataMessage(ctx, cli, chat, dm)
	self := b.selfACI()
	m := core.Message{
		ID: core.MessageID{Chat: chat, Author: self, Timestamp: ts}, Kind: core.KindStoryReply, Outgoing: true,
		SentAt: msTime(ts), ReceivedAt: b.now().UTC(), Status: core.StatusPending, StoryContext: ref,
	}
	if emoji == "" {
		m.Body = body
	}
	if _, err := b.db.PutMessage(ctx, m, store.AsRead()); err != nil {
		return err
	}
	_ = b.index.put(ctx, m.ID, 0, "", nil)
	if stored, err := b.db.GetMessage(ctx, m.ID); err == nil {
		b.emit(core.MessageAdded{Message: stored})
	}
	b.recordSendResult(ctx, m.ID, res, sendErr)
	b.emit(core.StoriesChanged{})
	return sendErr
}

// PostStory is in stories_post.go.

// --- account --------------------------------------------------------------------

// UpdateSettings stores the account settings. They apply to this device
// (receipts, typing, link previews) and the synced ones are written back to
// the account record in the storage service. The notification level is
// local only.
func (b *Backend) UpdateSettings(ctx context.Context, s core.Settings) error {
	old, oldErr := b.db.Settings(ctx)
	if err := b.db.SaveSettings(ctx, s); err != nil {
		return err
	}
	if oldErr == nil {
		b.queueStorage(settingsStorageOps(old, s)...)
	}
	if cli, err := b.client(); err == nil {
		applySettingsToAccount(cli, s)
	}
	return nil
}

// applySettingsToAccount mirrors local receipt/typing choices into the
// in-memory account record, which signalmeow consults before sending read
// receipts and typing indicators.
func applySettingsToAccount(cli *signalmeow.Client, s core.Settings) {
	// The fork guards the account record: storage reads and writes replace
	// it while senders read it, so update it atomically through the store.
	cli.Store.UpdateAccountRecord(func(ar *signalpb.AccountRecord) {
		ar.ReadReceipts, ar.TypingIndicators, ar.LinkPreviews = s.ReadReceipts, s.TypingIndicators, s.LinkPreviews
	})
}

// Unlink removes this device from the account and forgets its keys. Local
// chats and messages stay in warte's store.
func (b *Backend) Unlink(ctx context.Context) error {
	cli, err := b.client()
	if err != nil {
		return err
	}
	var errs []error
	// Mark logged out first so connectLoop does not reconnect.
	b.setConn(core.ConnLoggedOut)
	if cli.AuthedWS != nil {
		if err := cli.Unlink(ctx); err != nil {
			errs = append(errs, fmt.Errorf("backend: unlink: %w", err))
		}
	}
	b.stopLoops(cli)
	b.dropPendingStorage()
	if err := b.container.DeleteDevice(ctx, &cli.Store.DeviceData); err != nil {
		errs = append(errs, fmt.Errorf("backend: forget device: %w", err))
	}
	b.emit(core.LoggedOut{Err: errors.New("device unlinked")})
	return errors.Join(errs...)
}
