package fake

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"warte/internal/core"
)

// maxPinnedMessages mirrors Signal's limit of pinned messages per chat.
const maxPinnedMessages = 3

// begin checks ctx and lifecycle state and takes the write lock. On success
// the caller must call s.mu.Unlock.
func (s *Service) begin(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.isClosed() {
		return ErrClosed
	}
	s.mu.Lock()
	if s.loggedOut {
		s.mu.Unlock()
		return core.ErrNotLoggedIn
	}
	return nil
}

// chatLocked returns the chat state or ErrNotFound.
func (s *Service) chatLocked(id core.ChatID) (*chatState, error) {
	cs, ok := s.chats[id]
	if !ok {
		return nil, fmt.Errorf("chat %s: %w", id, core.ErrNotFound)
	}
	return cs, nil
}

// canSendLocked reports whether self may post in the chat.
func (s *Service) canSendLocked(cs *chatState) error {
	c := s.exportChatLocked(cs)
	switch {
	case c.Blocked:
		return fmt.Errorf("chat is blocked: %w", core.ErrReadOnly)
	case c.MessageRequest:
		return fmt.Errorf("accept the message request first: %w", core.ErrReadOnly)
	case cs.left:
		return fmt.Errorf("you left this group: %w", core.ErrReadOnly)
	case c.OnlyAdminsSend && !c.SelfIsAdmin:
		return fmt.Errorf("only admins can send messages: %w", core.ErrReadOnly)
	}
	return nil
}

// validateOutgoing enforces "exactly one of Body/Attachments, Sticker, Poll".
func validateOutgoing(out core.Outgoing) error {
	kinds := 0
	if strings.TrimSpace(out.Body) != "" || len(out.Attachments) > 0 {
		kinds++
	}
	if out.Sticker != nil {
		kinds++
	}
	if out.Poll != nil {
		kinds++
		if strings.TrimSpace(out.Poll.Question) == "" || len(out.Poll.Options) < 2 {
			return errors.New("fake: a poll needs a question and at least two options")
		}
	}
	switch {
	case kinds == 0:
		return errors.New("fake: empty message")
	case kinds > 1:
		return errors.New("fake: a message carries exactly one of text/attachments, a sticker or a poll")
	case out.ViewOnce && len(out.Attachments) == 0:
		return errors.New("fake: view-once requires an attachment")
	}
	return nil
}

// Send posts a message. It returns once the message is stored as Pending;
// Sent, Delivered and Read follow as MessageUpdated events, Latency apart.
func (s *Service) Send(ctx context.Context, chat core.ChatID, out core.Outgoing) (core.MessageID, error) {
	if err := validateOutgoing(out); err != nil {
		return core.MessageID{}, err
	}
	if err := s.begin(ctx); err != nil {
		return core.MessageID{}, err
	}
	cs, err := s.chatLocked(chat)
	if err == nil {
		err = s.canSendLocked(cs)
	}
	var m *core.Message
	if err == nil {
		m, err = s.newOutgoingLocked(cs, out)
	}
	if err != nil {
		s.mu.Unlock()
		return core.MessageID{}, err
	}
	s.emitAll(s.appendOutgoingLocked(cs, m))
	s.mu.Unlock()
	s.spawn(func() { s.progress(m.ID) })
	return m.ID, nil
}

// appendOutgoingLocked stores an outgoing message and marks the chat as read,
// unarchived and draft-free, as sending does in Signal.
func (s *Service) appendOutgoingLocked(cs *chatState, m *core.Message) []core.Event {
	cs.insert(m)
	cs.chat.Draft = ""
	cs.chat.MarkedUnread = false
	cs.chat.Archived = false
	cs.readUpTo = max(cs.readUpTo, m.ID.Timestamp)
	return []core.Event{
		core.MessageAdded{Message: s.exportMsgLocked(m)},
		core.ChatUpdated{Chat: s.exportChatLocked(cs)},
	}
}

// newMessageLocked creates a message skeleton with a fresh timestamp.
func (s *Service) newMessageLocked(cs *chatState, author core.ACI) *core.Message {
	ts := s.nextTSLocked(s.now())
	at := time.UnixMilli(int64(ts))
	m := &core.Message{
		ID:              core.MessageID{Chat: cs.chat.ID, Author: author, Timestamp: ts},
		Kind:            core.KindNormal,
		Outgoing:        author == s.self.ACI,
		SentAt:          at,
		ReceivedAt:      at,
		ExpiresIn:       cs.chat.ExpireTimer,
		ServerTimestamp: ts,
	}
	if m.Outgoing {
		m.Status = core.StatusPending
		if m.ExpiresIn > 0 {
			m.ExpireStartedAt = at
		}
		if cs.chat.Kind == core.ChatGroup {
			m.PerRecipient = map[core.ACI]core.DeliveryStatus{}
			for _, mem := range cs.chat.Members {
				if mem.ACI != s.self.ACI {
					m.PerRecipient[mem.ACI] = core.StatusPending
				}
			}
		}
	}
	return m
}

func (s *Service) newOutgoingLocked(cs *chatState, out core.Outgoing) (*core.Message, error) {
	var quote *core.Quote
	if out.Quote != nil {
		q, err := s.quoteLocked(cs, *out.Quote)
		if err != nil {
			return nil, err
		}
		quote = q
	}
	m := s.newMessageLocked(cs, s.self.ACI)
	m.Body = out.Body
	m.Ranges = sanitizeRanges(out.Body, out.Ranges)
	for _, r := range m.Ranges {
		if r.Style == core.StyleMention {
			if _, known := s.contacts[r.MentionACI]; !known && r.MentionACI != s.self.ACI {
				return nil, fmt.Errorf("mentioned contact %s: %w", r.MentionACI, core.ErrNotFound)
			}
		}
	}
	m.Quote = quote
	m.ViewOnce = out.ViewOnce
	m.Sticker = cloneSticker(out.Sticker)
	m.Poll = clonePoll(out.Poll)
	if m.Poll != nil {
		for i := range m.Poll.Options {
			m.Poll.Options[i].Voters = nil
		}
		m.Poll.Ended = false
	}
	for i, oa := range out.Attachments {
		m.Attachments = append(m.Attachments, s.storeOutgoingLocked(m.ID.Timestamp, i, oa))
	}
	switch {
	case out.Preview != nil:
		p := *out.Preview
		p.Image = cloneAttachment(out.Preview.Image)
		m.Preview = &p
	case s.settings.LinkPreviews:
		m.Preview = autoPreview(out.Body)
	}
	return m, nil
}

// storeOutgoingLocked keeps the attachment bytes and derives metadata.
func (s *Service) storeOutgoingLocked(ts uint64, i int, oa core.OutgoingAttachment) core.Attachment {
	ct := oa.ContentType
	if ct == "" {
		ct = http.DetectContentType(oa.Data)
		if ct == "application/ogg" {
			ct = "audio/ogg"
		}
	}
	a := core.Attachment{
		ID:          fmt.Sprintf("out-%d-%d", ts, i),
		ContentType: ct,
		FileName:    oa.FileName,
		Size:        int64(len(oa.Data)),
		Width:       oa.Width,
		Height:      oa.Height,
		Caption:     oa.Caption,
		VoiceNote:   oa.VoiceNote,
		GIF:         ct == "image/gif",
		Downloaded:  true,
		Progress:    1,
	}
	if a.IsImage() {
		if cfg, _, err := image.DecodeConfig(bytes.NewReader(oa.Data)); err == nil {
			if a.Width == 0 || a.Height == 0 {
				a.Width, a.Height = cfg.Width, cfg.Height
			}
			if cfg.Width*cfg.Height <= 4_000_000 {
				a.BlurHash = blurHash(oa.Data)
			}
		}
	}
	s.blobs[a.ID] = slices.Clone(oa.Data)
	return a
}

// autoPreview builds a basic link preview for the first URL in body.
func autoPreview(body string) *core.LinkPreview {
	for _, f := range strings.Fields(body) {
		if !strings.HasPrefix(f, "https://") && !strings.HasPrefix(f, "http://") {
			continue
		}
		f = strings.TrimRight(f, ".,!?)")
		u, err := url.Parse(f)
		if err != nil || u.Host == "" {
			continue
		}
		title := strings.TrimPrefix(u.Host, "www.")
		desc := strings.Trim(u.Path, "/")
		return &core.LinkPreview{URL: f, Title: title, Description: desc}
	}
	return nil
}

// quoteLocked builds a Quote of id, which must be in cs.
func (s *Service) quoteLocked(cs *chatState, id core.MessageID) (*core.Quote, error) {
	if id.Chat != cs.chat.ID {
		return nil, fmt.Errorf("quoted message is in another chat: %w", core.ErrNotFound)
	}
	t := cs.find(id)
	if t == nil || !visible(t, s.now()) {
		return nil, fmt.Errorf("quoted message %s: %w", id, core.ErrNotFound)
	}
	q := &core.Quote{
		ID:            t.ID,
		AuthorName:    s.nameLocked(t.ID.Author),
		Text:          t.Body,
		Ranges:        cloneRanges(t.Ranges),
		OriginalFound: true,
	}
	if len(t.Attachments) > 0 {
		q.Attachment = cloneAttachment(&t.Attachments[0])
	} else if t.Sticker != nil {
		q.Attachment = cloneAttachment(&t.Sticker.Attachment)
	}
	return q, nil
}

// progress walks an outgoing message through Sent → Delivered → Read.
func (s *Service) progress(id core.MessageID) {
	lat := s.opts.Latency
	steps := []struct {
		st    core.DeliveryStatus
		delay time.Duration
	}{
		{core.StatusSent, lat},
		{core.StatusDelivered, lat},
		{core.StatusRead, 2 * lat},
	}
	for _, step := range steps {
		if !s.sleep(step.delay) {
			return
		}
		if !s.setStatus(id, step.st) {
			return
		}
	}
	if s.opts.Live && id.Chat.IsDirect() && id.Chat.ACI() != s.Self().ACI {
		s.autoReply(id.Chat)
	}
}

// setStatus updates the delivery status of an outgoing message and emits the
// change. It reports whether progression should continue.
func (s *Service) setStatus(id core.MessageID, st core.DeliveryStatus) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loggedOut {
		return false
	}
	cs, ok := s.chats[id.Chat]
	if !ok {
		return false
	}
	m := cs.find(id)
	if m == nil || m.RemoteDeleted || m.Status == core.StatusFailed {
		return false
	}
	// Note to Self, blocked peers and left groups never get past Sent.
	if st > core.StatusSent && (cs.chat.Kind == core.ChatNoteToSelf || s.exportChatLocked(cs).Blocked || cs.left) {
		return false
	}
	if m.Status >= st {
		return true
	}
	m.Status = st
	for aci := range m.PerRecipient {
		m.PerRecipient[aci] = st
	}
	s.emitAll(s.updatedLocked(cs, m))
	return true
}

// messageLocked finds a message for mutation.
func (s *Service) messageLocked(id core.MessageID) (*chatState, *core.Message, error) {
	cs, err := s.chatLocked(id.Chat)
	if err != nil {
		return nil, nil, err
	}
	m := cs.find(id)
	if m == nil || !visible(m, s.now()) {
		return nil, nil, fmt.Errorf("message %s: %w", id, core.ErrNotFound)
	}
	return cs, m, nil
}

// updatedLocked returns the events announcing a message change.
func (s *Service) updatedLocked(cs *chatState, m *core.Message) []core.Event {
	return []core.Event{
		core.MessageUpdated{Message: s.exportMsgLocked(m)},
		core.ChatUpdated{Chat: s.exportChatLocked(cs)},
	}
}

// Edit replaces the body of one of your own messages, keeping history.
func (s *Service) Edit(ctx context.Context, id core.MessageID, body string, ranges []core.BodyRange) error {
	return s.mutate(ctx, func() ([]core.Event, error) {
		cs, m, err := s.messageLocked(id)
		if err != nil {
			return nil, err
		}
		switch {
		case !m.Outgoing || m.Kind != core.KindNormal:
			return nil, fmt.Errorf("can only edit your own messages: %w", core.ErrReadOnly)
		case m.RemoteDeleted:
			return nil, fmt.Errorf("message was deleted: %w", core.ErrReadOnly)
		case m.Poll != nil || m.Sticker != nil || m.ViewOnce:
			return nil, fmt.Errorf("this message can't be edited: %w", core.ErrReadOnly)
		case strings.TrimSpace(body) == "" && len(m.Attachments) == 0:
			return nil, errors.New("fake: edit would leave the message empty")
		}
		ranges = sanitizeRanges(body, ranges)
		if body == m.Body && slices.Equal(ranges, m.Ranges) {
			return nil, nil
		}
		at := s.lastEditLocked(m)
		m.EditHistory = append(m.EditHistory, core.EditRevision{Body: m.Body, Ranges: m.Ranges, At: at})
		m.Body, m.Ranges, m.Edited = body, ranges, true
		s.editedAt(m.ID, s.now())
		if m.Preview != nil || s.settings.LinkPreviews {
			m.Preview = autoPreviewKeep(m.Preview, body)
		}
		return s.updatedLocked(cs, m), nil
	})
}

// autoPreviewKeep keeps an existing preview if its URL is still in the body.
func autoPreviewKeep(p *core.LinkPreview, body string) *core.LinkPreview {
	if p != nil && strings.Contains(body, p.URL) {
		return p
	}
	return autoPreview(body)
}

// Delete removes a message for you, or (own messages only) for everyone.
func (s *Service) Delete(ctx context.Context, id core.MessageID, forEveryone bool) error {
	return s.mutate(ctx, func() ([]core.Event, error) {
		cs, m, err := s.messageLocked(id)
		if err != nil {
			return nil, err
		}
		if !forEveryone {
			i := cs.index(id)
			s.dropBlobsLocked(m)
			delete(cs.pinUntil, id)
			cs.msgs = slices.Delete(cs.msgs, i, i+1)
			return []core.Event{core.MessageRemoved{ID: id}, core.ChatUpdated{Chat: s.exportChatLocked(cs)}}, nil
		}
		if !m.Outgoing || m.Kind != core.KindNormal {
			return nil, fmt.Errorf("can only delete your own messages for everyone: %w", core.ErrReadOnly)
		}
		if m.RemoteDeleted {
			return nil, nil
		}
		s.dropBlobsLocked(m)
		delete(cs.pinUntil, id)
		*m = core.Message{
			ID: m.ID, Kind: m.Kind, Outgoing: m.Outgoing, SentAt: m.SentAt, ReceivedAt: m.ReceivedAt,
			Status: m.Status, PerRecipient: m.PerRecipient, ServerTimestamp: m.ServerTimestamp,
			ExpiresIn: m.ExpiresIn, ExpireStartedAt: m.ExpireStartedAt,
			RemoteDeleted: true,
		}
		return s.updatedLocked(cs, m), nil
	})
}

// React sets (or with remove, clears) your reaction. Signal allows one
// reaction per person, so a new emoji replaces the previous one.
func (s *Service) React(ctx context.Context, id core.MessageID, emoji string, remove bool) error {
	return s.mutate(ctx, func() ([]core.Event, error) {
		_, m, err := s.messageLocked(id)
		if err != nil {
			return nil, err
		}
		if m.Kind == core.KindSystem || m.Kind == core.KindCall || m.RemoteDeleted {
			return nil, fmt.Errorf("can't react to this message: %w", core.ErrReadOnly)
		}
		if !remove && emoji == "" {
			return nil, errors.New("fake: empty reaction")
		}
		if !s.setReactionLocked(m, s.self.ACI, emoji, remove) {
			return nil, nil
		}
		return []core.Event{core.MessageUpdated{Message: s.exportMsgLocked(m)}}, nil
	})
}

// setReactionLocked applies a reaction change, reporting whether anything
// changed.
func (s *Service) setReactionLocked(m *core.Message, from core.ACI, emoji string, remove bool) bool {
	i := slices.IndexFunc(m.Reactions, func(r core.Reaction) bool { return r.From == from })
	if remove {
		if i < 0 || (emoji != "" && m.Reactions[i].Emoji != emoji) {
			return false
		}
		m.Reactions = slices.Delete(m.Reactions, i, i+1)
		return true
	}
	r := core.Reaction{Emoji: emoji, From: from, At: s.now()}
	if i >= 0 {
		if m.Reactions[i].Emoji == emoji {
			return false
		}
		m.Reactions = slices.Delete(m.Reactions, i, i+1)
	}
	m.Reactions = append(m.Reactions, r)
	return true
}

// Forward copies a message into each target chat as a new outgoing message.
func (s *Service) Forward(ctx context.Context, id core.MessageID, to []core.ChatID) error {
	if len(to) == 0 {
		return errors.New("fake: no forward targets")
	}
	var sent []core.MessageID
	err := s.mutate(ctx, func() ([]core.Event, error) {
		_, src, err := s.messageLocked(id)
		if err != nil {
			return nil, err
		}
		if src.Kind != core.KindNormal || src.RemoteDeleted || src.ViewOnce || src.Poll != nil {
			return nil, fmt.Errorf("this message can't be forwarded: %w", core.ErrReadOnly)
		}
		targets := make([]*chatState, 0, len(to))
		for _, t := range to {
			cs, err := s.chatLocked(t)
			if err != nil {
				return nil, err
			}
			if err := s.canSendLocked(cs); err != nil {
				return nil, err
			}
			targets = append(targets, cs)
		}
		var evs []core.Event
		for _, cs := range targets {
			m := s.newMessageLocked(cs, s.self.ACI)
			c := cloneMessage(src)
			// Mentions don't survive forwarding: they become plain "@Name" text.
			m.Body, m.Ranges = s.inlineMentionsLocked(c.Body, c.Ranges)
			m.Attachments, m.Sticker, m.Contact, m.Preview = c.Attachments, c.Sticker, c.Contact, c.Preview
			evs = append(evs, s.appendOutgoingLocked(cs, m)...)
			sent = append(sent, m.ID)
		}
		return evs, nil
	})
	for _, id := range sent {
		s.spawn(func() { s.progress(id) })
	}
	return err
}

// MarkRead marks incoming messages up to upTo (zero = all) as read and starts
// their disappearing timers.
func (s *Service) MarkRead(ctx context.Context, chat core.ChatID, upTo core.MessageID) error {
	return s.mutate(ctx, func() ([]core.Event, error) {
		cs, err := s.chatLocked(chat)
		if err != nil {
			return nil, err
		}
		return s.markReadLocked(cs, upTo), nil
	})
}

func (s *Service) markReadLocked(cs *chatState, upTo core.MessageID) []core.Event {
	target := upTo.Timestamp
	if upTo.IsZero() && len(cs.msgs) > 0 {
		target = cs.msgs[len(cs.msgs)-1].ID.Timestamp
	}
	var evs []core.Event
	now := s.now()
	for _, m := range cs.msgs {
		if m.ID.Timestamp <= cs.readUpTo || m.ID.Timestamp > target {
			continue
		}
		if m.ExpiresIn > 0 && m.ExpireStartedAt.IsZero() && !m.Outgoing {
			m.ExpireStartedAt = now
			evs = append(evs, core.MessageUpdated{Message: s.exportMsgLocked(m)})
		}
	}
	changed := target > cs.readUpTo || cs.chat.MarkedUnread
	cs.readUpTo = max(cs.readUpTo, target)
	cs.chat.MarkedUnread = false
	if changed {
		evs = append(evs, core.ChatUpdated{Chat: s.exportChatLocked(cs)})
	}
	return evs
}

// MarkViewOnceOpened records that view-once media was viewed and purges it.
func (s *Service) MarkViewOnceOpened(ctx context.Context, id core.MessageID) error {
	return s.mutate(ctx, func() ([]core.Event, error) {
		cs, m, err := s.messageLocked(id)
		if err != nil {
			return nil, err
		}
		if !m.ViewOnce {
			return nil, fmt.Errorf("not a view-once message: %w", core.ErrReadOnly)
		}
		if m.ViewOnceOpened {
			return nil, nil
		}
		s.dropBlobsLocked(m)
		m.ViewOnceOpened = true
		m.Attachments = nil
		return s.updatedLocked(cs, m), nil
	})
}

// SetTyping validates the chat. The fake has no peers to notify.
func (s *Service) SetTyping(ctx context.Context, chat core.ChatID, typing bool) error {
	if err := s.begin(ctx); err != nil {
		return err
	}
	defer s.mu.Unlock()
	_, err := s.chatLocked(chat)
	return err
}

// SaveDraft stores the composer draft for a chat.
func (s *Service) SaveDraft(ctx context.Context, chat core.ChatID, draft string) error {
	return s.mutate(ctx, func() ([]core.Event, error) {
		cs, err := s.chatLocked(chat)
		if err != nil {
			return nil, err
		}
		if cs.chat.Draft == draft {
			return nil, nil
		}
		cs.chat.Draft = draft
		return []core.Event{core.ChatUpdated{Chat: s.exportChatLocked(cs)}}, nil
	})
}

// VotePoll replaces your vote with the given option indexes (empty retracts).
func (s *Service) VotePoll(ctx context.Context, id core.MessageID, options []int) error {
	return s.mutate(ctx, func() ([]core.Event, error) {
		cs, m, err := s.messageLocked(id)
		if err != nil {
			return nil, err
		}
		p := m.Poll
		switch {
		case p == nil || m.RemoteDeleted:
			return nil, fmt.Errorf("not a poll: %w", core.ErrReadOnly)
		case p.Ended:
			return nil, fmt.Errorf("poll has ended: %w", core.ErrReadOnly)
		}
		chosen := map[int]bool{}
		for _, o := range options {
			if o < 0 || o >= len(p.Options) {
				return nil, fmt.Errorf("fake: poll option %d out of range", o)
			}
			chosen[o] = true
		}
		if len(chosen) > 1 && !p.AllowMultiple {
			return nil, errors.New("fake: this poll allows only one choice")
		}
		self := s.self.ACI
		for i := range p.Options {
			p.Options[i].Voters = slices.DeleteFunc(p.Options[i].Voters, func(a core.ACI) bool { return a == self })
			if chosen[i] {
				p.Options[i].Voters = append(p.Options[i].Voters, self)
			}
		}
		return s.updatedLocked(cs, m), nil
	})
}

// EndPoll closes one of your own polls.
func (s *Service) EndPoll(ctx context.Context, id core.MessageID) error {
	return s.mutate(ctx, func() ([]core.Event, error) {
		cs, m, err := s.messageLocked(id)
		if err != nil {
			return nil, err
		}
		switch {
		case m.Poll == nil:
			return nil, fmt.Errorf("not a poll: %w", core.ErrReadOnly)
		case !m.Outgoing:
			return nil, fmt.Errorf("only the poll's author can end it: %w", core.ErrReadOnly)
		case m.Poll.Ended:
			return nil, nil
		}
		m.Poll.Ended = true
		return s.updatedLocked(cs, m), nil
	})
}

// PinMessage pins or unpins a message; duration > 0 unpins automatically.
// At most three messages stay pinned per chat; the oldest pin is dropped.
func (s *Service) PinMessage(ctx context.Context, id core.MessageID, pin bool, duration time.Duration) error {
	return s.mutate(ctx, func() ([]core.Event, error) {
		cs, m, err := s.messageLocked(id)
		if err != nil {
			return nil, err
		}
		if pin && (m.Kind != core.KindNormal || m.RemoteDeleted) {
			return nil, fmt.Errorf("can't pin this message: %w", core.ErrReadOnly)
		}
		if cs.pinUntil == nil {
			cs.pinUntil = map[core.MessageID]time.Time{}
		}
		delete(cs.pinUntil, id)
		if pin && duration > 0 {
			cs.pinUntil[id] = s.now().Add(duration)
		}
		if m.Pinned == pin {
			return nil, nil
		}
		m.Pinned = pin
		evs := []core.Event{core.MessageUpdated{Message: s.exportMsgLocked(m)}}
		if pin {
			var pinned []*core.Message
			for _, o := range cs.msgs {
				if o.Pinned && o != m {
					pinned = append(pinned, o)
				}
			}
			for len(pinned) >= maxPinnedMessages {
				pinned[0].Pinned = false
				delete(cs.pinUntil, pinned[0].ID)
				evs = append(evs, core.MessageUpdated{Message: s.exportMsgLocked(pinned[0])})
				pinned = pinned[1:]
			}
		}
		return append(evs, core.ChatUpdated{Chat: s.exportChatLocked(cs)}), nil
	})
}

// editedAt / lastEditLocked remember when a message was last edited, so the
// next revision gets the right timestamp.
func (s *Service) editedAt(id core.MessageID, t time.Time) {
	if s.edits == nil {
		s.edits = map[core.MessageID]time.Time{}
	}
	s.edits[id] = t
}

func (s *Service) lastEditLocked(m *core.Message) time.Time {
	if t, ok := s.edits[m.ID]; ok {
		return t
	}
	if n := len(m.EditHistory); n > 0 {
		return m.EditHistory[n-1].At.Add(time.Minute)
	}
	return m.SentAt
}
