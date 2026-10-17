package fake

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"warte/internal/core"
)

// Chats lists chats matching f, pinned chats first (by PinOrder), then by last
// activity, newest first.
func (s *Service) Chats(ctx context.Context, f core.ChatFilter) ([]core.Chat, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	var folder *folderState
	if f.FolderID != "" {
		for _, fs := range s.folders {
			if fs.folder.ID == f.FolderID {
				folder = fs
				break
			}
		}
		if folder == nil {
			return nil, fmt.Errorf("folder %q: %w", f.FolderID, core.ErrNotFound)
		}
	}

	type entry struct {
		chat core.Chat
		last time.Time
	}
	var entries []entry
	now := s.now()
	for id, cs := range s.chats {
		c := s.exportChatLocked(cs)
		if !matchChat(c, f, now) {
			continue
		}
		if folder != nil {
			if folder.members != nil && !folder.members[id] {
				continue
			}
			ff := folder.folder.Filter
			ff.Archived, ff.IncludeEmpty, ff.IncludeBlocked = f.Archived, f.IncludeEmpty, f.IncludeBlocked
			if !matchChat(c, ff, now) {
				continue
			}
		}
		entries = append(entries, entry{chat: c, last: s.lastActivityLocked(cs)})
	}
	slices.SortFunc(entries, func(a, b entry) int {
		if a.chat.Pinned != b.chat.Pinned {
			if a.chat.Pinned {
				return -1
			}
			return 1
		}
		if a.chat.Pinned {
			if d := cmp.Compare(a.chat.PinOrder, b.chat.PinOrder); d != 0 {
				return d
			}
		}
		if d := b.last.Compare(a.last); d != 0 {
			return d
		}
		return cmp.Compare(a.chat.ID, b.chat.ID)
	})
	out := make([]core.Chat, len(entries))
	for i, e := range entries {
		out[i] = e.chat
	}
	return out, nil
}

// matchChat applies a ChatFilter (except FolderID) to an exported chat.
func matchChat(c core.Chat, f core.ChatFilter, now time.Time) bool {
	switch {
	case c.Archived != f.Archived:
		return false
	case f.UnreadOnly && c.Unread == 0 && !c.MarkedUnread:
		return false
	case f.GroupsOnly && c.Kind != core.ChatGroup:
		return false
	case f.DirectOnly && c.Kind == core.ChatGroup:
		return false
	case !f.IncludeEmpty && c.LastMessage == nil && c.Draft == "":
		return false
	case !f.IncludeBlocked && c.Blocked:
		return false
	case f.Query != "" && !fuzzyMatch(c.Title, f.Query):
		return false
	case f.Members != nil && !f.Members.Match(c, now):
		return false
	}
	return true
}

// fuzzyMatch reports whether all runes of query appear in s in order,
// case-insensitively (a subsequence match, like most fuzzy finders).
func fuzzyMatch(s, query string) bool {
	var q []rune
	for _, r := range strings.ToLower(query) {
		if !unicode.IsSpace(r) {
			q = append(q, r)
		}
	}
	if len(q) == 0 {
		return true
	}
	i := 0
	for _, r := range strings.ToLower(s) {
		if r == q[i] {
			i++
			if i == len(q) {
				return true
			}
		}
	}
	return false
}

// Chat returns one chat.
func (s *Service) Chat(ctx context.Context, id core.ChatID) (core.Chat, error) {
	if err := ctx.Err(); err != nil {
		return core.Chat{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	cs, ok := s.chats[id]
	if !ok {
		return core.Chat{}, fmt.Errorf("chat %s: %w", id, core.ErrNotFound)
	}
	return s.exportChatLocked(cs), nil
}

// visibleMsgsLocked returns the chat's messages that have not expired.
func (s *Service) visibleMsgsLocked(cs *chatState) []*core.Message {
	now := s.now()
	out := make([]*core.Message, 0, len(cs.msgs))
	for _, m := range cs.msgs {
		if visible(m, now) {
			out = append(out, m)
		}
	}
	return out
}

func (s *Service) exportListLocked(ms []*core.Message) []core.Message {
	out := make([]core.Message, len(ms))
	for i, m := range ms {
		out[i] = s.exportMsgLocked(m)
	}
	return out
}

// Messages returns up to limit messages older than before (zero = newest),
// oldest first. A non-positive limit defaults to 50.
func (s *Service) Messages(ctx context.Context, chat core.ChatID, before core.MessageID, limit int) ([]core.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = defaultMessagesLimit
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	cs, ok := s.chats[chat]
	if !ok {
		return nil, fmt.Errorf("chat %s: %w", chat, core.ErrNotFound)
	}
	vis := s.visibleMsgsLocked(cs)
	end := len(vis)
	if !before.IsZero() {
		end = sort.Search(len(vis), func(i int) bool { return vis[i].ID.Timestamp >= before.Timestamp })
	}
	start := max(0, end-limit)
	return s.exportListLocked(vis[start:end]), nil
}

// MessagesAround returns up to limit messages centred on id, oldest first.
func (s *Service) MessagesAround(ctx context.Context, id core.MessageID, limit int) ([]core.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = defaultMessagesLimit
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	cs, ok := s.chats[id.Chat]
	if !ok {
		return nil, fmt.Errorf("chat %s: %w", id.Chat, core.ErrNotFound)
	}
	vis := s.visibleMsgsLocked(cs)
	idx := slices.IndexFunc(vis, func(m *core.Message) bool { return m.ID == id })
	if idx < 0 {
		return nil, fmt.Errorf("message %s: %w", id, core.ErrNotFound)
	}
	start, end := windowAround(idx, limit, len(vis))
	return s.exportListLocked(vis[start:end]), nil
}

// windowAround returns [start,end) of at most limit items around idx within n.
func windowAround(idx, limit, n int) (int, int) {
	start := idx - (limit-1)/2
	end := start + limit
	if end > n {
		end = n
		start = end - limit
	}
	if start < 0 {
		start = 0
		end = min(n, limit)
	}
	return start, end
}

// Message returns one message.
func (s *Service) Message(ctx context.Context, id core.MessageID) (core.Message, error) {
	if err := ctx.Err(); err != nil {
		return core.Message{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, err := s.lookupLocked(id)
	if err != nil {
		return core.Message{}, err
	}
	return s.exportMsgLocked(m), nil
}

// lookupLocked finds a visible message.
func (s *Service) lookupLocked(id core.MessageID) (*core.Message, error) {
	cs, ok := s.chats[id.Chat]
	if !ok {
		return nil, fmt.Errorf("chat %s: %w", id.Chat, core.ErrNotFound)
	}
	m := cs.find(id)
	if m == nil || !visible(m, s.now()) {
		return nil, fmt.Errorf("message %s: %w", id, core.ErrNotFound)
	}
	return m, nil
}

// Search runs a case-insensitive substring search over message text (with
// mentions resolved to "@Name"), honouring all SearchQuery filters. Results
// are newest first. An empty Text matches every message passing the filters.
func (s *Service) Search(ctx context.Context, q core.SearchQuery) ([]core.SearchHit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	text := flattenWhitespace(strings.TrimSpace(q.Text))
	s.mu.RLock()
	defer s.mu.RUnlock()
	if q.Chat != "" {
		if _, ok := s.chats[q.Chat]; !ok {
			return nil, fmt.Errorf("chat %s: %w", q.Chat, core.ErrNotFound)
		}
	}
	type cand struct {
		m     *core.Message
		title string
		plain string
		hits  [][2]int
	}
	var cands []cand
	now := s.now()
	for id, cs := range s.chats {
		if q.Chat != "" && id != q.Chat {
			continue
		}
		title := s.exportChatLocked(cs).Title
		for _, m := range cs.msgs {
			if !searchable(m, now) || !matchSearchFilters(m, q) {
				continue
			}
			plain := flattenWhitespace(s.searchTextLocked(m))
			var hits [][2]int
			if text != "" {
				hits = foldIndexAll(plain, text)
				if len(hits) == 0 {
					continue
				}
			}
			cands = append(cands, cand{m: m, title: title, plain: plain, hits: hits})
		}
	}
	slices.SortFunc(cands, func(a, b cand) int {
		if d := cmp.Compare(b.m.ID.Timestamp, a.m.ID.Timestamp); d != 0 {
			return d
		}
		return cmp.Compare(a.m.ID.Chat, b.m.ID.Chat)
	})
	if len(cands) > limit {
		cands = cands[:limit]
	}
	out := make([]core.SearchHit, len(cands))
	for i, c := range cands {
		snippet, matches := makeSnippet(c.plain, c.hits)
		out[i] = core.SearchHit{Message: s.exportMsgLocked(c.m), ChatTitle: c.title, Snippet: snippet, Matches: matches}
	}
	return out, nil
}

func searchable(m *core.Message, now time.Time) bool {
	return (m.Kind == core.KindNormal || m.Kind == core.KindStoryReply) &&
		!m.RemoteDeleted && !m.ViewOnce && visible(m, now)
}

// searchTextLocked is the text a message is searched by: its body with
// mentions resolved and spoilers hidden, falling back to attachment captions,
// poll questions and shared contact names when there is no body.
func (s *Service) searchTextLocked(m *core.Message) string {
	if m.Body != "" {
		return s.plainTextLocked(m.Body, m.Ranges, true)
	}
	for _, a := range m.Attachments {
		if a.Caption != "" {
			return a.Caption
		}
	}
	switch {
	case m.Poll != nil:
		return m.Poll.Question
	case m.Contact != nil:
		return m.Contact.Name
	case len(m.Attachments) > 0 && m.Attachments[0].FileName != "":
		return m.Attachments[0].FileName
	}
	return ""
}

func matchSearchFilters(m *core.Message, q core.SearchQuery) bool {
	if q.From != "" && m.ID.Author != q.From {
		return false
	}
	if !q.Before.IsZero() && !m.SentAt.Before(q.Before) {
		return false
	}
	if !q.After.IsZero() && !m.SentAt.After(q.After) {
		return false
	}
	if q.HasType != "" && !hasType(m, q.HasType) {
		return false
	}
	return true
}

// hasType implements SearchQuery.HasType.
func hasType(m *core.Message, typ string) bool {
	switch strings.ToLower(typ) {
	case "link":
		return m.Preview != nil || strings.Contains(m.Body, "http://") || strings.Contains(m.Body, "https://")
	case "image":
		return slices.ContainsFunc(m.Attachments, func(a core.Attachment) bool { return a.IsImage() })
	case "video":
		return slices.ContainsFunc(m.Attachments, func(a core.Attachment) bool { return a.IsVideo() })
	case "audio":
		return slices.ContainsFunc(m.Attachments, func(a core.Attachment) bool { return a.IsAudio() })
	case "file":
		return slices.ContainsFunc(m.Attachments, func(a core.Attachment) bool {
			return !a.IsImage() && !a.IsVideo() && !a.IsAudio()
		})
	case "sticker":
		return m.Sticker != nil
	case "poll":
		return m.Poll != nil
	}
	return false
}

// flattenWhitespace maps newlines and tabs to spaces, preserving byte offsets.
func flattenWhitespace(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		return r
	}, s)
}

// foldIndexAll returns the byte ranges of all non-overlapping,
// case-insensitive occurrences of query in s.
func foldIndexAll(s, query string) [][2]int {
	q := []rune(query)
	if len(q) == 0 {
		return nil
	}
	var out [][2]int
	for i := 0; i < len(s); {
		if end, ok := foldPrefix(s[i:], q); ok {
			out = append(out, [2]int{i, i + end})
			i += end
			continue
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return out
}

func foldPrefix(s string, q []rune) (int, bool) {
	j := 0
	for _, qr := range q {
		if j >= len(s) {
			return 0, false
		}
		r, size := utf8.DecodeRuneInString(s[j:])
		if !runeFoldEqual(r, qr) {
			return 0, false
		}
		j += size
	}
	return j, true
}

func runeFoldEqual(a, b rune) bool {
	if a == b {
		return true
	}
	for r := unicode.SimpleFold(a); r != a; r = unicode.SimpleFold(r) {
		if r == b {
			return true
		}
	}
	return false
}

const (
	snippetMax    = 160
	snippetBefore = 48
	ellipsis      = "…"
)

// makeSnippet cuts a window of text around the first match and returns it with
// the match ranges (byte offsets into the snippet) that fall inside it.
func makeSnippet(text string, hits [][2]int) (string, [][2]int) {
	if len(text) <= snippetMax {
		return text, slices.Clone(hits)
	}
	start := 0
	if len(hits) > 0 && hits[0][0] > snippetBefore {
		start = hits[0][0] - snippetBefore
		for start > 0 && !utf8.RuneStart(text[start]) {
			start--
		}
		// Prefer starting at a word boundary.
		if sp := strings.IndexByte(text[start:hits[0][0]], ' '); sp >= 0 {
			start += sp + 1
		}
	}
	end := min(len(text), start+snippetMax)
	if len(hits) > 0 && hits[0][1] > end {
		end = hits[0][1]
	}
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end++
	}
	var b strings.Builder
	offset := -start
	if start > 0 {
		b.WriteString(ellipsis)
		offset += len(ellipsis)
	}
	b.WriteString(text[start:end])
	if end < len(text) {
		b.WriteString(ellipsis)
	}
	var matches [][2]int
	for _, h := range hits {
		if h[0] >= start && h[1] <= end {
			matches = append(matches, [2]int{h[0] + offset, h[1] + offset})
		}
	}
	return b.String(), matches
}

// Contacts returns all known contacts except self, sorted by display name.
func (s *Service) Contacts(ctx context.Context) ([]core.Contact, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]core.Contact, 0, len(s.contacts))
	for _, c := range s.contacts {
		out = append(out, *c)
	}
	slices.SortFunc(out, func(a, b core.Contact) int {
		if d := cmp.Compare(strings.ToLower(a.DisplayName()), strings.ToLower(b.DisplayName())); d != 0 {
			return d
		}
		return cmp.Compare(a.ACI, b.ACI)
	})
	return out, nil
}

// Contact returns one contact (including self).
func (s *Service) Contact(ctx context.Context, aci core.ACI) (core.Contact, error) {
	if err := ctx.Err(); err != nil {
		return core.Contact{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if aci == s.self.ACI {
		return s.self, nil
	}
	c, ok := s.contacts[aci]
	if !ok {
		return core.Contact{}, fmt.Errorf("contact %s: %w", aci, core.ErrNotFound)
	}
	return *c, nil
}

// storyLifetime is how long stories stay visible.
const storyLifetime = 24 * time.Hour

// Stories returns stories from the last 24 hours, newest first.
func (s *Service) Stories(ctx context.Context) ([]core.Story, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	cutoff := s.now().Add(-storyLifetime)
	var out []core.Story
	for _, st := range s.stories {
		if st.At.Before(cutoff) {
			continue
		}
		c := cloneStory(st)
		c.AuthorName = s.nameLocked(st.ID.Author)
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b core.Story) int { return b.At.Compare(a.At) })
	return out, nil
}

// CallLog lists call events across all chats, newest first.
func (s *Service) CallLog(ctx context.Context) ([]core.CallLogEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []core.CallLogEntry
	for _, cs := range s.chats {
		title := s.exportChatLocked(cs).Title
		for _, m := range cs.msgs {
			if m.Kind != core.KindCall || m.Call == nil {
				continue
			}
			out = append(out, core.CallLogEntry{
				Chat:   cs.chat.ID,
				Title:  title,
				Call:   *m.Call,
				Unread: m.Call.Missed && m.ID.Timestamp > cs.readUpTo,
			})
		}
	}
	slices.SortFunc(out, func(a, b core.CallLogEntry) int {
		if d := b.Call.StartedAt.Compare(a.Call.StartedAt); d != 0 {
			return d
		}
		return cmp.Compare(a.Chat, b.Chat)
	})
	return out, nil
}

// StickerPacks returns all known packs (installed and not).
func (s *Service) StickerPacks(ctx context.Context) ([]core.StickerPack, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]core.StickerPack, len(s.packs))
	for i, p := range s.packs {
		out[i] = clonePack(p)
	}
	return out, nil
}

// Folders returns the chat folders. Each folder's Filter has FolderID set, so
// it can be passed to Chats directly.
func (s *Service) Folders(ctx context.Context) ([]core.Folder, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]core.Folder, len(s.folders))
	for i, f := range s.folders {
		out[i] = f.folder
	}
	return out, nil
}

// Settings returns the account settings.
func (s *Service) Settings(ctx context.Context) (core.Settings, error) {
	if err := ctx.Err(); err != nil {
		return core.Settings{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings, nil
}

// Attachment returns an attachment's bytes. Attachments not yet downloaded
// are "downloaded" after the simulated latency, with a MessageUpdated event.
func (s *Service) Attachment(ctx context.Context, msgID core.MessageID, attachmentID string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	data, downloaded, err := s.attachmentLocked(msgID, attachmentID)
	s.mu.RUnlock()
	if err != nil {
		return nil, err
	}
	if !downloaded {
		if err := s.wait(ctx, s.opts.Latency); err != nil {
			return nil, err
		}
		s.mu.Lock()
		var ev core.Event
		if m, err := s.lookupLocked(msgID); err == nil {
			for i := range m.Attachments {
				if m.Attachments[i].ID == attachmentID && !m.Attachments[i].Downloaded {
					m.Attachments[i].Downloaded, m.Attachments[i].Progress = true, 1
					ev = core.MessageUpdated{Message: s.exportMsgLocked(m)}
				}
			}
		}
		if ev != nil {
			s.emit(ev)
		}
		s.mu.Unlock()
	}
	return slices.Clone(data), nil
}

// attachmentLocked locates attachment bytes on a message or story.
func (s *Service) attachmentLocked(msgID core.MessageID, id string) ([]byte, bool, error) {
	notFound := fmt.Errorf("attachment %s: %w", id, core.ErrNotFound)
	for _, st := range s.stories {
		if st.ID == msgID && st.Media != nil && st.Media.ID == id {
			if data, ok := s.blobs[id]; ok {
				return data, true, nil
			}
			return nil, false, notFound
		}
	}
	m, err := s.lookupLocked(msgID)
	if err != nil {
		return nil, false, err
	}
	if m.Sticker != nil && m.Sticker.Attachment.ID == id {
		if data, ok := s.stickers[stickerKey{m.Sticker.PackID, m.Sticker.StickerID}]; ok {
			return data, true, nil
		}
		return nil, false, notFound
	}
	var att *core.Attachment
	for i := range m.Attachments {
		if m.Attachments[i].ID == id {
			att = &m.Attachments[i]
		}
	}
	if att == nil && m.Preview != nil && m.Preview.Image != nil && m.Preview.Image.ID == id {
		att = m.Preview.Image
	}
	if att == nil && m.Quote != nil && m.Quote.Attachment != nil && m.Quote.Attachment.ID == id {
		att = m.Quote.Attachment
	}
	if att == nil {
		return nil, false, notFound
	}
	data, ok := s.blobs[id]
	if !ok {
		return nil, false, notFound
	}
	return data, att.Downloaded, nil
}

// Avatar returns avatar image bytes (PNG).
func (s *Service) Avatar(ctx context.Context, avatarID string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, ok := s.avatars[avatarID]
	if !ok {
		return nil, fmt.Errorf("avatar %s: %w", avatarID, core.ErrNotFound)
	}
	return slices.Clone(data), nil
}

// StickerImage returns a sticker's image bytes (PNG).
func (s *Service) StickerImage(ctx context.Context, packID string, stickerID uint32) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, ok := s.stickers[stickerKey{packID, stickerID}]
	if !ok {
		return nil, fmt.Errorf("sticker %s/%d: %w", packID, stickerID, core.ErrNotFound)
	}
	return slices.Clone(data), nil
}

// wait sleeps for d, honouring ctx and Close.
func (s *Service) wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return ErrClosed
	}
}
