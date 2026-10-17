package finder

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/sahilm/fuzzy"

	"warte/internal/core"
	"warte/internal/format"
	"warte/internal/tui/theme"
	"warte/internal/tui/ui"
)

// queryTimeout bounds the local queries the finder runs.
const queryTimeout = 5 * time.Second

// resolveTimeout bounds svc.ResolveRecipient (may hit the network).
const resolveTimeout = 20 * time.Second

// ---- recents ----------------------------------------------------------------------------

// recents is a small most-recently-used list of chats opened through the
// finder, shared by all finder instances of the process.
var recents = struct {
	sync.Mutex
	ids []core.ChatID
}{}

const maxRecents = 20

// NoteRecent records that chat was opened, boosting it in the chat
// switcher. The finder calls it itself; the integrator may call it whenever
// a chat is opened by other means.
func NoteRecent(chat core.ChatID) {
	if chat == "" {
		return
	}
	recents.Lock()
	defer recents.Unlock()
	out := []core.ChatID{chat}
	for _, id := range recents.ids {
		if id != chat && len(out) < maxRecents {
			out = append(out, id)
		}
	}
	recents.ids = out
}

func recentRank() map[core.ChatID]int {
	recents.Lock()
	defer recents.Unlock()
	m := make(map[core.ChatID]int, len(recents.ids))
	for i, id := range recents.ids {
		m[id] = i
	}
	return m
}

// ---- entries ------------------------------------------------------------------------------

type entryKind int

const (
	entryChat    entryKind = iota
	entryContact           // contact without a chat: starts one
	entryStart             // "Start chat with …" pseudo entry
)

type chatEntry struct {
	kind    entryKind
	chat    core.Chat
	contact core.Contact
	// Sanitized fields matched by the fuzzy filter.
	title    string
	username string // "@name.42" or ""
	phone    string
	rank     int // recency rank (0 = most recent)
	query    string
}

func (e *chatEntry) id() string {
	switch e.kind {
	case entryContact:
		return "contact:" + string(e.contact.ACI)
	case entryStart:
		return "start:" + e.query
	}
	return string(e.chat.ID)
}

type scored struct {
	e     *chatEntry
	score int
	field int // 0 title, 1 username, 2 phone
	idx   []int
}

// ---- overlay ------------------------------------------------------------------------------

// Chats is the fuzzy chat switcher ("<leader><leader>"). Create it with
// NewChatSwitcher.
type Chats struct {
	ctx      ui.Context
	m        modal
	entries  []*chatEntry
	results  []scored
	sel, off int
	rows     int
	twoLine  bool
	loading  bool
	err      string
	busy     string // "Resolving …" while ResolveRecipient runs
	gen      int
	inflight bool // a load is running
	dirty    bool // data changed while loading: reload once it finishes
	listTop  int
	rowH     int
}

type chatsLoadedMsg struct {
	c        *Chats
	chats    []core.Chat
	archived []core.Chat
	contacts []core.Contact
	err      error
}

type resolvedMsg struct {
	c    *Chats
	chat core.ChatID
	err  error
	what string
}

// NewChatSwitcher returns the Telescope-style chat switcher overlay.
func NewChatSwitcher(ctx ui.Context) *Chats {
	return &Chats{ctx: ctx, loading: true}
}

// NewChatSwitcherQuery is NewChatSwitcher with a prefilled query.
func NewChatSwitcherQuery(ctx ui.Context, query string) *Chats {
	c := NewChatSwitcher(ctx)
	c.m.input.SetValue(query)
	return c
}

// Init implements ui.Overlay.
func (c *Chats) Init() tea.Cmd { return c.load() }

// reload starts a load, or (when one is running) schedules exactly one more
// after it, so bursts of chat events do not pile up queries.
func (c *Chats) reload() tea.Cmd {
	if c.inflight {
		c.dirty = true
		return nil
	}
	return c.load()
}

func (c *Chats) load() tea.Cmd {
	c.inflight = true
	svc := c.ctx.Svc
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		msg := chatsLoadedMsg{c: c}
		if svc == nil {
			return msg
		}
		msg.chats, msg.err = svc.Chats(ctx, core.ChatFilter{IncludeEmpty: true})
		if msg.err != nil {
			return msg
		}
		msg.archived, _ = svc.Chats(ctx, core.ChatFilter{Archived: true, IncludeEmpty: true})
		msg.contacts, _ = svc.Contacts(ctx)
		return msg
	}
}

func (c *Chats) setData(msg chatsLoadedMsg) {
	prev := ""
	if r, ok := c.current(); ok {
		prev = r.e.id()
	}
	// Deduplicate by ID: a chat can change archive state between the two
	// queries.
	var all []core.Chat
	seen := map[core.ChatID]bool{}
	for _, ch := range append(append([]core.Chat{}, msg.chats...), msg.archived...) {
		if !seen[ch.ID] {
			seen[ch.ID] = true
			all = append(all, ch)
		}
	}
	// Recency order: last activity, newest first.
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Archived != all[j].Archived {
			return !all[i].Archived
		}
		return lastAt(all[i]).After(lastAt(all[j]))
	})
	var entries []*chatEntry
	direct := map[core.ACI]bool{}
	for i, ch := range all {
		e := &chatEntry{kind: entryChat, chat: ch, title: theme.SanitizeLine(ch.Title), rank: i}
		if ch.Kind == core.ChatNoteToSelf && e.title == "" {
			e.title = "Note to Self"
		}
		if ch.Contact != nil {
			if ch.Contact.Username != "" {
				e.username = "@" + theme.SanitizeLine(ch.Contact.Username)
			}
			e.phone = theme.SanitizeLine(ch.Contact.E164)
		}
		if aci := ch.ID.ACI(); aci != "" {
			direct[aci] = true
		}
		entries = append(entries, e)
	}
	for _, ct := range msg.contacts {
		if direct[ct.ACI] || ct.Blocked || ct.IsSelf || ct.ACI == c.ctx.Self.ACI {
			continue
		}
		e := &chatEntry{kind: entryContact, contact: ct, title: theme.SanitizeLine(ct.DisplayName()), rank: len(entries)}
		if ct.Username != "" {
			e.username = "@" + theme.SanitizeLine(ct.Username)
		}
		e.phone = theme.SanitizeLine(ct.E164)
		entries = append(entries, e)
	}
	c.entries = entries
	c.filter()
	if prev != "" {
		for i, r := range c.results {
			if r.e.id() == prev {
				c.sel = i
			}
		}
	}
}

func lastAt(ch core.Chat) time.Time {
	if ch.LastMessage != nil {
		return ch.LastMessage.At
	}
	return time.Time{}
}

var (
	phoneLike    = regexp.MustCompile(`^\+?[0-9][0-9 ()/-]{4,}$`)
	usernameLike = regexp.MustCompile(`^@?[a-zA-Z_][a-zA-Z0-9_]{2,31}\.[0-9]{2,9}$`)
	signalLink   = regexp.MustCompile(`(?i)^(https://)?signal\.me/#`)
)

// startCandidate reports whether q looks like a phone number, username or
// username link that ResolveRecipient can turn into a chat.
func startCandidate(q string) bool {
	q = strings.TrimSpace(q)
	return phoneLike.MatchString(q) || usernameLike.MatchString(q) || signalLink.MatchString(q)
}

// filter recomputes c.results for the current query.
func (c *Chats) filter() {
	q := strings.TrimSpace(c.m.input.Value())
	c.results = c.results[:0]
	mru := recentRank()
	open := core.ChatID("")
	if c.ctx.Chat != nil {
		open = c.ctx.Chat()
	}
	boost := func(e *chatEntry) int {
		b := 0
		if e.kind == entryChat {
			if r, ok := mru[e.chat.ID]; ok {
				b += 100 - 3*r
			}
			b += max(0, 20-e.rank)
			if e.chat.Unread > 0 || e.chat.MarkedUnread {
				b += 4
			}
			if e.chat.Pinned {
				b += 2
			}
			if e.chat.Archived {
				b -= 15
			}
		} else {
			b -= 10
		}
		return b
	}
	if q == "" {
		for _, e := range c.entries {
			if e.kind == entryChat && !e.chat.Archived {
				c.results = append(c.results, scored{e: e, score: boost(e)})
			}
		}
		sort.SliceStable(c.results, func(i, j int) bool { return c.results[i].score > c.results[j].score })
		// Like vim's alternate buffer: with the open chat on top, start on
		// the previous one so "<leader><leader><CR>" toggles between them.
		c.sel = 0
		if len(c.results) > 1 && c.results[0].e.chat.ID == open && open != "" {
			c.sel = 1
		}
	} else {
		best := map[*chatEntry]*scored{}
		fields := []func(*chatEntry) string{
			func(e *chatEntry) string { return e.title },
			func(e *chatEntry) string { return e.username },
			func(e *chatEntry) string { return e.phone },
		}
		pattern := q
		for fi, get := range fields {
			texts := make([]string, len(c.entries))
			for i, e := range c.entries {
				texts[i] = get(e)
			}
			p := pattern
			if fi == 2 {
				p = strings.TrimPrefix(strings.ReplaceAll(p, " ", ""), "+")
				if p == "" || strings.Trim(p, "0123456789") != "" {
					continue
				}
			}
			for _, m := range fuzzy.FindNoSort(p, texts) {
				e := c.entries[m.Index]
				score := m.Score
				if fi > 0 {
					score -= 5
				}
				if fi == 0 && strings.HasPrefix(strings.ToLower(e.title), strings.ToLower(q)) {
					score += 15
				}
				if cur, ok := best[e]; !ok || score > cur.score {
					best[e] = &scored{e: e, score: score, field: fi, idx: m.MatchedIndexes}
				}
			}
		}
		for _, e := range c.entries {
			if s, ok := best[e]; ok {
				s.score += boost(e)
				c.results = append(c.results, *s)
			}
		}
		sort.SliceStable(c.results, func(i, j int) bool { return c.results[i].score > c.results[j].score })
		if startCandidate(q) {
			st := scored{e: &chatEntry{kind: entryStart, query: q, title: theme.SanitizeLine(q)}}
			// Exact phone/username hits stay first; otherwise offer to start.
			if len(c.results) > 0 && c.results[0].field > 0 {
				c.results = append(c.results[:1], append([]scored{st}, c.results[1:]...)...)
			} else {
				c.results = append([]scored{st}, c.results...)
			}
		}
		c.sel = 0
	}
	c.off = 0
}

func (c *Chats) current() (scored, bool) {
	if c.sel < 0 || c.sel >= len(c.results) {
		return scored{}, false
	}
	return c.results[c.sel], true
}

// Query returns the current query text.
func (c *Chats) Query() string { return c.m.input.Value() }

// Title implements ui.Overlay.
func (c *Chats) Title() string { return "Chats" }

// KeyContext implements ui.Overlay.
func (c *Chats) KeyContext() string { return ContextChats }

// CaptureKeys implements ui.KeyCapturer.
func (c *Chats) CaptureKeys() bool { return true }

// Footer implements ui.Footer.
func (c *Chats) Footer() string {
	return c.ctx.Styles.RenderKeyHints(c.m.hints()...)
}

// Size implements ui.Overlay.
func (c *Chats) Size(maxW, maxH int) (int, int) {
	return min(72, maxW), min(max(maxH*2/3, 12), 26, maxH)
}

// Update implements ui.Overlay.
func (c *Chats) Update(msg tea.Msg) (ui.Overlay, tea.Cmd) {
	switch msg := msg.(type) {
	case chatsLoadedMsg:
		if msg.c != c {
			return c, nil
		}
		c.loading = false
		c.inflight = false
		var next tea.Cmd
		if c.dirty {
			c.dirty = false
			next = c.load()
		}
		if msg.err != nil {
			c.err = msg.err.Error()
			if len(c.entries) > 0 {
				return c, next // keep the list we have
			}
		}
		c.setData(msg)
		return c, next
	case resolvedMsg:
		if msg.c != c {
			return c, nil
		}
		c.busy = ""
		if msg.err != nil {
			c.err = msg.what + ": " + msg.err.Error()
			return c, ui.Toast(ui.ToastError, "Can't start chat: "+theme.SanitizeLine(msg.err.Error()))
		}
		return c.open(msg.chat)
	case ui.EventMsg:
		switch msg.Event.(type) {
		case core.ChatUpdated, core.ChatRemoved, core.ContactUpdated:
			return c, c.reload()
		}
	case tea.KeyPressMsg:
		return c.apply(c.m.key(msg))
	case tea.PasteMsg:
		if c.m.paste(msg.Content) {
			return c.apply(navEdited)
		}
	case ui.ActionMsg:
		return c.apply(c.m.action(msg))
	case tea.MouseClickMsg:
		if c.rowH > 0 {
			i := (msg.Y - c.listTop) / c.rowH
			if msg.Y >= c.listTop && c.off+i < len(c.results) && i < c.rows {
				c.sel = c.off + i
				return c.apply(navSelect)
			}
		}
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelDown:
			return c.apply(navDown)
		case tea.MouseWheelUp:
			return c.apply(navUp)
		}
	}
	return c, nil
}

func (c *Chats) apply(a navAction) (ui.Overlay, tea.Cmd) {
	switch a {
	case navClose:
		return nil, nil
	case navEdited:
		c.err = ""
		c.filter()
		return c, nil
	case navSelect:
		return c.accept()
	}
	if isMove(a) {
		c.sel = moveCursor(a, c.sel, len(c.results), c.rows)
	}
	return c, nil
}

func (c *Chats) open(id core.ChatID) (ui.Overlay, tea.Cmd) {
	NoteRecent(id)
	return nil, func() tea.Msg { return ui.OpenChatMsg{Chat: id} }
}

func (c *Chats) accept() (ui.Overlay, tea.Cmd) {
	r, ok := c.current()
	if !ok || c.busy != "" {
		return c, nil
	}
	switch r.e.kind {
	case entryChat:
		return c.open(r.e.chat.ID)
	case entryContact:
		ct := r.e.contact
		q := ct.E164
		if q == "" && ct.Username != "" {
			q = ct.Username
		}
		if q == "" {
			q = ct.DisplayName()
		}
		return c, c.resolve(q, r.e.title)
	default:
		return c, c.resolve(r.e.query, r.e.title)
	}
}

func (c *Chats) resolve(q, label string) tea.Cmd {
	svc := c.ctx.Svc
	if svc == nil {
		return nil
	}
	c.busy = "Starting chat with " + label + "…"
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), resolveTimeout)
		defer cancel()
		id, err := svc.ResolveRecipient(ctx, q)
		return resolvedMsg{c: c, chat: id, err: err, what: label}
	}
}

// View implements ui.Overlay.
func (c *Chats) View(w, h int) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	st := c.ctx.Styles
	input := c.m.input.view(st, w, promptFor(c.ctx), "Search chats, contacts, +phone, @username", !c.m.normal, nil)
	count := ""
	if len(c.entries) > 0 {
		count = st.ChatListPreview.Render(itoa(len(c.results)))
	}
	lines := []string{spread(input, count, w, lipgloss.NewStyle()), hrule(st, w)}
	c.listTop = 2
	status := ""
	switch {
	case c.busy != "":
		status = st.InfoText.Render(clip("  "+c.busy, w))
	case c.err != "":
		status = st.ErrorText.Render(clip("  "+theme.SanitizeLine(c.err), w))
	case c.loading && len(c.entries) == 0:
		status = st.ChatListPreview.Render("  Loading…")
	case len(c.results) == 0 && strings.TrimSpace(c.Query()) != "":
		status = st.ChatListPreview.Render(clip("  No chats or contacts match", w))
	case len(c.results) == 0:
		status = st.ChatListPreview.Render(clip("  No chats yet", w))
	}
	if status != "" {
		lines = append(lines, status)
		c.listTop++
	}
	avail := h - len(lines)
	c.twoLine = h >= 12 && w >= 30
	c.rowH = 1
	if c.twoLine {
		c.rowH = 2
	}
	c.rows = max(avail/c.rowH, 0)
	c.off = scrollTo(c.off, c.sel, c.rows, len(c.results))
	for i := c.off; i < len(c.results) && i < c.off+c.rows; i++ {
		lines = append(lines, c.renderRow(c.results[i], i == c.sel, w)...)
	}
	return finish(lines, w, h)
}

func (c *Chats) renderRow(r scored, selected bool, w int) []string {
	st := c.ctx.Styles
	ic := c.ctx.Icons
	pal := st.Theme.Palette
	base := rowStyles(st, selected)
	muted := on(st.ChatListPreview, base)
	hl := on(st.FuzzyMatch, base)
	gutter := cursorCell(st, selected) + base.Render(" ")
	e := r.e

	// Avatar column (4 cells + 1 space).
	var av [2]string
	avW := 5
	{
		name, code, id := e.title, "", e.id()
		switch e.kind {
		case entryChat:
			code, id = e.chat.Color, string(e.chat.ID)
		case entryContact:
			id = string(e.contact.ACI)
		}
		switch {
		case e.kind == entryStart:
			icon := ic.Mention
			if strings.HasPrefix(strings.TrimSpace(e.query), "+") || phoneLike.MatchString(e.query) {
				icon = ic.Call
			}
			av[0] = on(lipgloss.NewStyle().Foreground(pal.Accent).Bold(true), base).Render(padCenter(icon, 4))
		case e.kind == entryChat && e.chat.Kind == core.ChatNoteToSelf:
			av[0] = st.Avatar(code, id).Render(padCenter(ic.NoteToSelf, 4))
		default:
			av[0] = st.RenderAvatar(code, id, name)
		}
		if av[1] == "" {
			av[1] = base.Render("    ")
		}
		av[0] = hardClip(av[0], 4) + base.Render(" ")
		av[1] = hardClip(av[1], 4) + base.Render(" ")
	}
	textW := max(w-width(gutter)-avW, 0)

	// Title with highlights.
	titleStyle := base
	if e.kind == entryChat && (e.chat.Unread > 0 || e.chat.MarkedUnread) && !selected {
		titleStyle = st.ChatListItemUnread.UnsetPadding().UnsetWidth()
	}
	if e.kind == entryContact {
		titleStyle = on(lipgloss.NewStyle().Foreground(pal.Fg), base)
	}
	var title string
	switch e.kind {
	case entryStart:
		title = base.Render("Start chat with ") + hl.Render(e.title)
	default:
		idx := []int(nil)
		if r.field == 0 {
			idx = r.idx
		}
		title = highlight(e.title, idx, titleStyle, hl)
		if e.kind == entryChat && e.chat.Kind == core.ChatGroup && ic.Group != "" {
			title = muted.Render(ic.Group+" ") + title
		}
		if r.field == 1 && e.username != "" {
			title += muted.Render("  ") + highlight(e.username, r.idx, muted, hl)
		}
		if r.field == 2 && e.phone != "" {
			title += muted.Render("  ") + c.highlightPhone(e.phone, muted, hl)
		}
	}

	// Right side: badges and time.
	var right []string
	if e.kind == entryChat {
		ch := e.chat
		if ch.Archived && ic.Archived != "" {
			right = append(right, on(st.MutedIcon, base).Render(ic.Archived))
		}
		if ch.MutedUntil.After(c.now()) && ic.Muted != "" {
			right = append(right, on(st.MutedIcon, base).Render(ic.Muted))
		}
		if ch.Pinned && ic.Pinned != "" {
			right = append(right, on(st.PinnedIcon, base).Render(ic.Pinned))
		}
		if ch.MentionsUnread > 0 {
			right = append(right, st.MentionBadge.Render("@"))
		}
		if ch.Unread > 0 {
			right = append(right, st.UnreadBadge.Render(itoa(min(ch.Unread, 999))))
		} else if ch.MarkedUnread {
			right = append(right, on(lipgloss.NewStyle().Foreground(pal.Accent), base).Render(ic.Unread))
		}
		if ch.LastMessage != nil && !ch.LastMessage.At.IsZero() {
			right = append(right, on(st.Timestamp, base).Render(c.timeLabel(ch.LastMessage.At)))
		}
	}
	rightS := strings.Join(right, base.Render(" "))
	if rightS != "" {
		rightS += base.Render(" ")
	}
	line1 := gutter + av[0] + spread(title, rightS, textW, base)
	if !c.twoLine {
		return []string{pad(line1, w, base)}
	}

	// Second line: snippet / hint.
	var sub string
	switch e.kind {
	case entryStart:
		sub = muted.Render("Look up and open a new direct chat")
	case entryContact:
		parts := []string{}
		if e.username != "" {
			parts = append(parts, e.username)
		}
		if e.phone != "" {
			parts = append(parts, e.phone)
		}
		parts = append(parts, "new chat")
		sub = muted.Render(strings.Join(parts, " · "))
	default:
		sub = c.snippet(e.chat, textW, muted, base)
	}
	line2 := gutter + av[1] + pad(clip(sub, textW), textW, base)
	return []string{pad(line1, w, base), pad(line2, w, base)}
}

// highlightPhone highlights the digits of phone matched by the query
// (the query is matched against the phone with "+" and spaces removed).
func (c *Chats) highlightPhone(phone string, base, hl lipgloss.Style) string {
	q := strings.TrimPrefix(strings.ReplaceAll(strings.TrimSpace(c.Query()), " ", ""), "+")
	i := strings.Index(phone, q)
	if q == "" || i < 0 {
		return base.Render(phone)
	}
	return highlightRanges(phone, [][2]int{{i, i + len(q)}}, base, hl)
}

func (c *Chats) snippet(ch core.Chat, w int, muted, base lipgloss.Style) string {
	if ch.Draft != "" {
		return on(c.ctx.Styles.WarningText, base).Render("Draft: ") + muted.Render(format.Snippet(theme.SanitizeLine(ch.Draft), nil, nil, max(w-7, 1)))
	}
	lm := ch.LastMessage
	if lm == nil {
		if ch.Description != "" {
			return muted.Render(format.Snippet(theme.SanitizeLine(ch.Description), nil, nil, w))
		}
		return muted.Render("No messages yet")
	}
	prefix := ""
	switch {
	case lm.Outgoing:
		prefix = "You: "
	case ch.Kind == core.ChatGroup && lm.Sender != "":
		prefix = theme.SanitizeLine(lm.Sender) + ": "
	}
	// theme.SanitizeLine drops whole escape sequences (format.Snippet only
	// drops their control bytes, leaving "]0;…" residue visible).
	text := format.Snippet(theme.SanitizeLine(lm.Text), nil, nil, max(w-width(prefix), 1))
	out := ""
	if prefix != "" {
		out = on(c.ctx.Styles.ChatListPreview.Bold(true), base).Render(prefix)
	}
	if lm.Outgoing {
		if d := c.ctx.Icons.Delivery(lm.Status); d != "" {
			out = on(c.ctx.Styles.Delivery(lm.Status), base).Render(d) + base.Render(" ") + out
		}
	}
	return out + muted.Render(text)
}

func (c *Chats) now() time.Time {
	if c.ctx.Now != nil {
		return c.ctx.Now()
	}
	return time.Now()
}

func (c *Chats) timeLabel(t time.Time) string {
	f := format.TimeFormatter{Hour12: c.ctx.Config.TimeFormat == "12h"}
	return f.RelativeTime(t, c.now())
}

// padCenter centers s (one or two cells) in n cells.
func padCenter(s string, n int) string {
	sw := width(s)
	if sw >= n {
		return s
	}
	l := (n - sw) / 2
	return strings.Repeat(" ", l) + s + strings.Repeat(" ", n-sw-l)
}
