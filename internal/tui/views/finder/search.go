package finder

import (
	"context"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/sahilm/fuzzy"

	"warte/internal/core"
	"warte/internal/format"
	"warte/internal/tui/theme"
	"warte/internal/tui/ui"
)

// SearchDebounce is how long the global search waits after the last
// keystroke before querying the backend.
const SearchDebounce = 200 * time.Millisecond

// searchLimit caps the number of hits requested from the backend.
const searchLimit = 300

// previewContext is the number of messages loaded around a hit.
const previewContext = 15

// widePreview is the minimum content width at which the preview pane is
// shown next to the results.
const widePreview = 96

// Search is the global message search overlay ("<leader>/"). Create it with
// NewSearch or NewSearchQuery.
type Search struct {
	ctx ui.Context
	m   modal

	seq       int
	searching bool
	parsed    Query
	chips     []string // resolved filters for display
	problems  []string // filters that could not be resolved
	err       string
	ran       bool // a search finished for the current query

	hits  []core.SearchHit
	order []int     // hit indices in display (grouped) order
	lines []listRow // display rows
	sel   int       // index into order
	off   int       // first visible display row
	rows  int
	top   int // content line of the first list row at the last render

	contacts []core.Contact
	chats    []core.Chat
	dataOK   bool
	waitData bool // a search is waiting for contacts/chats to resolve from:/in:

	showPreview bool
	preview     map[core.MessageID][]core.Message
	previewErr  map[core.MessageID]string
	previewWant core.MessageID
}

// listRow is one display row of the grouped result list.
type listRow struct {
	header bool
	chat   core.ChatID
	title  string
	count  int
	hit    int // index into hits (rows only)
	pos    int // index into order (rows only)
}

type searchDataMsg struct {
	s        *Search
	contacts []core.Contact
	chats    []core.Chat
}

type searchDebounceMsg struct {
	s   *Search
	seq int
}

type searchResultMsg struct {
	s    *Search
	seq  int
	hits []core.SearchHit
	err  error
}

type searchPreviewMsg struct {
	s    *Search
	id   core.MessageID
	msgs []core.Message
	err  error
}

// NewSearch returns the global message search overlay.
func NewSearch(ctx ui.Context) *Search {
	return &Search{
		ctx:         ctx,
		showPreview: true,
		preview:     map[core.MessageID][]core.Message{},
		previewErr:  map[core.MessageID]string{},
	}
}

// NewSearchQuery is NewSearch with a prefilled query (e.g. from ":find
// from:bob lunch"); the search runs right away.
func NewSearchQuery(ctx ui.Context, query string) *Search {
	s := NewSearch(ctx)
	s.m.input.SetValue(query)
	s.parsed = ParseQuery(query, s.now())
	return s
}

func (s *Search) now() time.Time {
	if s.ctx.Now != nil {
		return s.ctx.Now()
	}
	return time.Now()
}

// Init implements ui.Overlay: it loads contacts and chats (to resolve from:
// and in:) and starts the search for a prefilled query.
func (s *Search) Init() tea.Cmd {
	svc := s.ctx.Svc
	load := func() tea.Msg {
		msg := searchDataMsg{s: s}
		if svc == nil {
			return msg
		}
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		msg.contacts, _ = svc.Contacts(ctx)
		active, _ := svc.Chats(ctx, core.ChatFilter{IncludeEmpty: true})
		archived, _ := svc.Chats(ctx, core.ChatFilter{Archived: true, IncludeEmpty: true})
		msg.chats = append(active, archived...)
		return msg
	}
	return load
}

// Query returns the raw query text.
func (s *Search) Query() string { return s.m.input.Value() }

// Hits returns the current results in display order.
func (s *Search) Hits() []core.SearchHit {
	out := make([]core.SearchHit, 0, len(s.order))
	for _, i := range s.order {
		out = append(out, s.hits[i])
	}
	return out
}

// Title implements ui.Overlay.
func (s *Search) Title() string { return "Search messages" }

// KeyContext implements ui.Overlay.
func (s *Search) KeyContext() string { return ContextSearch }

// CaptureKeys implements ui.KeyCapturer.
func (s *Search) CaptureKeys() bool { return true }

// Footer implements ui.Footer.
func (s *Search) Footer() string {
	return s.ctx.Styles.RenderKeyHints(s.m.hints("ctrl+o", "preview")...)
}

// Size implements ui.Overlay.
func (s *Search) Size(maxW, maxH int) (int, int) {
	return min(150, maxW), min(max(maxH*4/5, 14), 44, maxH)
}

// Update implements ui.Overlay.
func (s *Search) Update(msg tea.Msg) (ui.Overlay, tea.Cmd) {
	switch msg := msg.(type) {
	case searchDataMsg:
		if msg.s != s {
			return s, nil
		}
		s.contacts, s.chats, s.dataOK = msg.contacts, msg.chats, true
		// Run a prefilled query, or one whose from:/in: waited for the data.
		if !s.parsed.IsEmpty() && (s.seq == 0 || s.waitData) {
			s.waitData = false
			return s, s.runNow()
		}
	case searchDebounceMsg:
		if msg.s != s || msg.seq != s.seq {
			return s, nil
		}
		return s, s.runNow()
	case searchResultMsg:
		if msg.s != s || msg.seq != s.seq {
			return s, nil // stale
		}
		s.searching = false
		s.ran = true
		s.err = ""
		if msg.err != nil {
			s.err = msg.err.Error()
		}
		s.setHits(msg.hits)
		return s, s.loadPreview()
	case searchPreviewMsg:
		if msg.s != s {
			return s, nil
		}
		if msg.err != nil {
			s.previewErr[msg.id] = msg.err.Error()
		} else {
			s.preview[msg.id] = msg.msgs
		}
	case ui.EventMsg:
		if ev, ok := msg.Event.(core.MessageRemoved); ok {
			s.removeHit(ev.ID)
		}
	case tea.KeyPressMsg:
		return s.apply(s.m.key(msg))
	case tea.PasteMsg:
		if s.m.paste(msg.Content) {
			return s.apply(navEdited)
		}
	case ui.ActionMsg:
		return s.apply(s.m.action(msg))
	case tea.MouseClickMsg:
		i := msg.Y - s.top
		if i >= 0 && i < s.rows && s.off+i < len(s.lines) {
			if r := s.lines[s.off+i]; !r.header {
				s.sel = r.pos
				return s.apply(navSelect)
			}
		}
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelDown:
			return s.apply(navDown)
		case tea.MouseWheelUp:
			return s.apply(navUp)
		}
	}
	return s, nil
}

func (s *Search) apply(a navAction) (ui.Overlay, tea.Cmd) {
	switch a {
	case navClose:
		return nil, nil
	case navEdited:
		s.seq++
		s.parsed = ParseQuery(s.Query(), s.now())
		if s.parsed.IsEmpty() {
			s.searching = false
			s.ran = false
			s.chips, s.problems, s.err = nil, nil, ""
			s.setHits(nil)
			return s, nil
		}
		s.searching = true
		seq := s.seq
		return s, tea.Tick(SearchDebounce, func(time.Time) tea.Msg { return searchDebounceMsg{s: s, seq: seq} })
	case navSelect:
		if s.sel < 0 || s.sel >= len(s.order) {
			return s, nil
		}
		hit := s.hits[s.order[s.sel]]
		id := hit.Message.ID
		NoteRecent(id.Chat)
		return nil, func() tea.Msg { return ui.OpenChatMsg{Chat: id.Chat, Focus: &id} }
	case navPreview:
		s.showPreview = !s.showPreview
		return s, s.loadPreview()
	}
	if isMove(a) {
		s.sel = moveCursor(a, s.sel, len(s.order), s.rows)
		return s, s.loadPreview()
	}
	return s, nil
}

// runNow resolves the filters and starts the backend search.
func (s *Search) runNow() tea.Cmd {
	if !s.dataOK && s.ctx.Svc != nil && (s.parsed.From != "" || s.parsed.In != "") {
		// Names cannot be resolved yet; the data load reruns the search.
		s.waitData = true
		s.searching = true
		return nil
	}
	q, chips, problems := s.resolve(s.parsed)
	s.chips, s.problems = chips, problems
	if len(problems) > 0 || s.ctx.Svc == nil {
		s.searching = false
		s.ran = true
		s.setHits(nil)
		return nil
	}
	s.searching = true
	svc, seq := s.ctx.Svc, s.seq
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		hits, err := svc.Search(ctx, q)
		return searchResultMsg{s: s, seq: seq, hits: hits, err: err}
	}
}

// resolve turns a parsed query into a core.SearchQuery, resolving from:
// and in: against the loaded contacts and chats.
func (s *Search) resolve(p Query) (core.SearchQuery, []string, []string) {
	q := core.SearchQuery{Text: p.Text, HasType: p.Has, Before: p.Before, After: p.After, Limit: searchLimit}
	var chips, problems []string
	problems = append(problems, p.Errors...)
	if p.From != "" {
		aci, name, ok := s.resolveFrom(p.From)
		if ok {
			q.From = aci
			chips = append(chips, "from "+name)
		} else {
			problems = append(problems, "no contact “"+theme.SanitizeLine(p.From)+"”")
		}
	}
	if p.In != "" {
		id, name, ok := s.resolveIn(p.In)
		if ok {
			q.Chat = id
			chips = append(chips, "in "+name)
		} else {
			problems = append(problems, "no chat “"+theme.SanitizeLine(p.In)+"”")
		}
	}
	if p.Has != "" {
		chips = append(chips, "has "+p.Has)
	}
	const day = "Jan 2 2006"
	switch {
	case !p.After.IsZero() && !p.Before.IsZero():
		chips = append(chips, p.After.Add(time.Nanosecond).Format(day)+" – "+p.Before.Add(-time.Nanosecond).Format(day))
	case !p.After.IsZero():
		chips = append(chips, "since "+p.After.Add(time.Nanosecond).Format(day))
	case !p.Before.IsZero():
		chips = append(chips, "before "+p.Before.Format(day))
	}
	return q, chips, problems
}

func (s *Search) self() core.Contact {
	if s.ctx.Self.ACI != "" {
		return s.ctx.Self
	}
	if s.ctx.Svc != nil {
		return s.ctx.Svc.Self()
	}
	return core.Contact{}
}

func (s *Search) resolveFrom(v string) (core.ACI, string, bool) {
	lv := strings.ToLower(strings.TrimSpace(v))
	switch lv {
	case "me", "self", "you", "myself":
		me := s.self()
		return me.ACI, "you", me.ACI != ""
	}
	lv = strings.TrimPrefix(lv, "@")
	digits := strings.NewReplacer(" ", "", "-", "", "+", "").Replace(lv)
	var names []string
	for _, c := range s.contacts {
		dn := theme.SanitizeLine(c.DisplayName())
		for _, f := range []string{c.Nickname, c.ContactName, c.ProfileName, c.Username} {
			if f != "" && strings.ToLower(f) == lv {
				return c.ACI, dn, true
			}
		}
		if digits != "" && c.E164 != "" && strings.Trim(digits, "0123456789") == "" &&
			strings.HasSuffix(strings.TrimPrefix(c.E164, "+"), digits) && len(digits) >= 4 {
			return c.ACI, dn, true
		}
		names = append(names, dn+" "+c.Username)
	}
	if m := fuzzy.Find(lv, names); len(m) > 0 {
		c := s.contacts[m[0].Index]
		return c.ACI, theme.SanitizeLine(c.DisplayName()), true
	}
	return "", "", false
}

func (s *Search) resolveIn(v string) (core.ChatID, string, bool) {
	lv := strings.ToLower(strings.TrimSpace(v))
	if lv == "here" || lv == "this" || lv == "." {
		if s.ctx.Chat != nil {
			if id := s.ctx.Chat(); id != "" {
				for _, c := range s.chats {
					if c.ID == id {
						return id, theme.SanitizeLine(c.Title), true
					}
				}
				return id, "this chat", true
			}
		}
		return "", "", false
	}
	titles := make([]string, len(s.chats))
	for i, c := range s.chats {
		titles[i] = theme.SanitizeLine(c.Title)
		if strings.ToLower(titles[i]) == lv {
			return c.ID, titles[i], true
		}
	}
	if m := fuzzy.Find(lv, titles); len(m) > 0 {
		c := s.chats[m[0].Index]
		return c.ID, titles[m[0].Index], true
	}
	return "", "", false
}

// setHits stores results and builds the grouped display rows. Groups are
// ordered by their newest hit, hits within a group newest first.
func (s *Search) setHits(hits []core.SearchHit) {
	var keep core.MessageID
	if s.sel >= 0 && s.sel < len(s.order) {
		keep = s.hits[s.order[s.sel]].Message.ID
	}
	s.hits = hits
	type group struct {
		chat   core.ChatID
		title  string
		newest time.Time
		idx    []int
	}
	var groups []*group
	byChat := map[core.ChatID]*group{}
	for i, h := range hits {
		g, ok := byChat[h.Message.ID.Chat]
		if !ok {
			g = &group{chat: h.Message.ID.Chat, title: theme.SanitizeLine(h.ChatTitle)}
			byChat[g.chat] = g
			groups = append(groups, g)
		}
		g.idx = append(g.idx, i)
		if t := hitTime(h); t.After(g.newest) {
			g.newest = t
		}
	}
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].newest.After(groups[j].newest) })
	s.order = s.order[:0]
	s.lines = s.lines[:0]
	for _, g := range groups {
		sort.SliceStable(g.idx, func(a, b int) bool { return hitTime(hits[g.idx[a]]).After(hitTime(hits[g.idx[b]])) })
		s.lines = append(s.lines, listRow{header: true, chat: g.chat, title: g.title, count: len(g.idx)})
		for _, i := range g.idx {
			s.lines = append(s.lines, listRow{chat: g.chat, hit: i, pos: len(s.order)})
			s.order = append(s.order, i)
		}
	}
	s.sel = 0
	for p, i := range s.order {
		if hits[i].Message.ID == keep {
			s.sel = p
		}
	}
	s.off = 0
}

func hitTime(h core.SearchHit) time.Time {
	if !h.Message.SentAt.IsZero() {
		return h.Message.SentAt
	}
	return time.UnixMilli(int64(h.Message.ID.Timestamp))
}

func (s *Search) removeHit(id core.MessageID) {
	var hits []core.SearchHit
	for _, h := range s.hits {
		if h.Message.ID != id {
			hits = append(hits, h)
		}
	}
	if len(hits) != len(s.hits) {
		s.setHits(hits)
	}
}

// loadPreview fetches the messages around the selected hit (once per hit).
func (s *Search) loadPreview() tea.Cmd {
	if !s.showPreview || s.sel < 0 || s.sel >= len(s.order) || s.ctx.Svc == nil {
		return nil
	}
	id := s.hits[s.order[s.sel]].Message.ID
	s.previewWant = id
	if _, ok := s.preview[id]; ok {
		return nil
	}
	if _, ok := s.previewErr[id]; ok {
		return nil
	}
	svc := s.ctx.Svc
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		msgs, err := svc.MessagesAround(ctx, id, previewContext)
		return searchPreviewMsg{s: s, id: id, msgs: msgs, err: err}
	}
}

// ---- rendering -----------------------------------------------------------------------------

// View implements ui.Overlay.
func (s *Search) View(w, h int) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	st := s.ctx.Styles
	listW, pvW := w, 0
	if s.showPreview && w >= widePreview && !s.parsed.IsEmpty() {
		pvW = min(max(w*45/100, 36), w-50)
		listW = w - pvW - 3
	}
	accent := lipgloss.NewStyle().Foreground(st.Theme.Palette.Accent).Bold(true)
	spans := filterSpans(s.Query())
	styleAt := func(i int) lipgloss.Style {
		for _, sp := range spans {
			if i >= sp[0] && i < sp[1] {
				return accent
			}
		}
		return lipgloss.NewStyle().Foreground(st.Theme.Palette.Fg)
	}
	input := s.m.input.view(st, listW, promptFor(s.ctx), "Search messages…  from: in: has: before: after:", !s.m.normal, styleAt)
	lines := []string{input, s.statusLine(listW), hrule(st, listW)}
	s.top = len(lines)
	s.rows = max(h-len(lines), 0)
	body := s.listLines(listW)
	lines = append(lines, body...)
	if pvW > 0 {
		return finish(sideBySide(st, lines, s.previewLines(pvW, h), listW, pvW, h), w, h)
	}
	return finish(lines, w, h)
}

func (s *Search) statusLine(w int) string {
	st := s.ctx.Styles
	chip := lipgloss.NewStyle().Foreground(st.Theme.Palette.AccentAlt)
	var parts []string
	for _, c := range s.chips {
		parts = append(parts, chip.Render(theme.SanitizeLine(c)))
	}
	for _, p := range s.problems {
		parts = append(parts, st.ErrorText.Render(theme.SanitizeLine(p)))
	}
	left := strings.Join(parts, st.ChatListPreview.Render(" · "))
	right := ""
	switch {
	case s.searching:
		right = st.ChatListPreview.Render("searching…")
	case s.err != "":
		right = st.ErrorText.Render("error")
	case s.ran && len(s.problems) == 0:
		chats := 0
		for _, l := range s.lines {
			if l.header {
				chats++
			}
		}
		right = st.ChatListPreview.Render(plural(len(s.order), "result") + " · " + plural(chats, "chat"))
	}
	if left == "" && right == "" {
		return ""
	}
	return " " + spread(left, right, max(w-1, 0), lipgloss.NewStyle())
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return itoa(n) + " " + word + "s"
}

func (s *Search) listLines(w int) []string {
	st := s.ctx.Styles
	if s.err != "" {
		return []string{st.ErrorText.Render(clip("  "+theme.SanitizeLine(s.err), w))}
	}
	if s.parsed.IsEmpty() {
		return s.helpLines(w)
	}
	if len(s.lines) == 0 {
		if len(s.problems) > 0 && !s.searching {
			return []string{st.ChatListPreview.Render(clip("  Fix the filter above to search", w))}
		}
		if s.searching || !s.ran {
			return []string{st.ChatListPreview.Render("  Searching…")}
		}
		return []string{st.ChatListPreview.Render(clip("  No messages found", w))}
	}
	// Keep the selected row (and its group header when close) visible.
	selLine := 0
	for i, l := range s.lines {
		if !l.header && l.pos == s.sel {
			selLine = i
		}
	}
	if selLine > 0 && s.lines[selLine-1].header && selLine-1 < s.off {
		s.off = selLine - 1
	}
	s.off = scrollTo(s.off, selLine, s.rows, len(s.lines))
	var out []string
	senderW := min(max(w/6, 6), 16)
	for i := s.off; i < len(s.lines) && len(out) < s.rows; i++ {
		l := s.lines[i]
		if l.header {
			out = append(out, s.headerLine(l, w))
			continue
		}
		out = append(out, s.hitLine(s.hits[l.hit], l.pos == s.sel, w, senderW))
	}
	return out
}

func (s *Search) helpLines(w int) []string {
	st := s.ctx.Styles
	key := lipgloss.NewStyle().Foreground(st.Theme.Palette.Accent).Bold(true)
	muted := st.ChatListPreview
	rows := [][2]string{
		{"from:bob", "messages sent by a contact (from:me for yours)"},
		{"in:book", "only in a chat (in:here = the open chat)"},
		{"has:image", "image, video, audio, file, link"},
		{"before:2026-01", "sent before a date (YYYY, YYYY-MM, YYYY-MM-DD)"},
		{"after:2025-12", "sent on or after a date (also 3d, 2w, today)"},
		{"on:yesterday", "sent on that day"},
	}
	out := []string{"", muted.Render(clip("  Type to search all chats. Filters can go anywhere:", w)), ""}
	for _, r := range rows {
		out = append(out, clip("  "+key.Render(padRight(r[0], 16))+muted.Render(r[1]), w))
	}
	return out
}

func padRight(s string, n int) string {
	if d := n - width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

func (s *Search) headerLine(l listRow, w int) string {
	st := s.ctx.Styles
	ic := s.ctx.Icons
	icon := ""
	if l.chat.IsGroup() && ic.Group != "" {
		icon = ic.Group + " "
	}
	title := l.title
	if title == "" {
		title = "Unknown chat"
	}
	head := lipgloss.NewStyle().Foreground(st.Theme.Palette.Accent).Bold(true).Render(" " + icon + title)
	return clip(head+st.ChatListPreview.Render(" · "+itoa(l.count)), w)
}

func (s *Search) hitLine(h core.SearchHit, selected bool, w, senderW int) string {
	st := s.ctx.Styles
	base := rowStyles(st, selected)
	muted := on(st.ChatListPreview, base)
	hl := on(st.FuzzyMatch, base)
	m := h.Message
	sender := "You"
	senderStyle := on(st.SenderName, base).Bold(true)
	if !m.Outgoing {
		sender = theme.SanitizeLine(m.SenderName)
		if sender == "" {
			sender = "Unknown"
		}
		senderStyle = on(st.SenderStyle(string(m.ID.Author)), base).Bold(true)
	}
	f := format.TimeFormatter{Hour12: s.ctx.Config.TimeFormat == "12h"}
	date := on(st.Timestamp, base).Render(f.RelativeTime(hitTime(h), s.now())) + base.Render(" ")
	var snip string
	if strings.TrimSpace(h.Snippet) != "" {
		snip = highlightRanges(h.Snippet, h.Matches, base, hl)
	} else {
		snip = muted.Render(describe(m, s.ctx.Icons))
	}
	if icon := mediaIcon(m, s.ctx.Icons); icon != "" && strings.TrimSpace(h.Snippet) != "" {
		snip = muted.Render(icon+" ") + snip
	}
	gutter := cursorCell(st, selected) + base.Render("  ")
	name := senderStyle.Render(clip(sender, senderW))
	name += base.Render(strings.Repeat(" ", max(senderW-width(name), 0)+1))
	inner := max(w-width(gutter)-width(name), 0)
	return pad(gutter+name+spread(snip, date, inner, base), w, base)
}

// mediaIcon returns an icon for the first attachment kind of m, or "".
func mediaIcon(m core.Message, ic theme.IconSet) string {
	switch {
	case m.Sticker != nil:
		return ic.Sticker
	case m.Poll != nil:
		return ic.Poll
	case len(m.Attachments) > 0:
		a := m.Attachments[0]
		switch {
		case a.VoiceNote:
			return ic.Voice
		case a.IsImage():
			return ic.Image
		case a.IsVideo():
			return ic.Video
		case a.IsAudio():
			return ic.Audio
		default:
			return ic.File
		}
	case m.Preview != nil:
		return ic.Link
	}
	return ""
}

// describe returns a plain one-line description of a message without text.
func describe(m core.Message, ic theme.IconSet) string {
	switch {
	case m.RemoteDeleted:
		return "This message was deleted"
	case m.Kind == core.KindSystem:
		return theme.SanitizeLine(m.SystemText)
	case m.Kind == core.KindCall:
		return ic.Call + " Call"
	case m.Sticker != nil:
		return ic.Sticker + " Sticker " + theme.SanitizeLine(m.Sticker.Emoji)
	case m.Poll != nil:
		return ic.Poll + " " + theme.SanitizeLine(m.Poll.Question)
	case m.Contact != nil:
		return "Contact: " + theme.SanitizeLine(m.Contact.Name)
	case len(m.Attachments) > 0:
		a := m.Attachments[0]
		label := "File"
		switch {
		case a.VoiceNote:
			label = "Voice message"
		case a.IsImage():
			label = "Photo"
		case a.IsVideo():
			label = "Video"
		case a.IsAudio():
			label = "Audio"
		case a.FileName != "":
			label = theme.SanitizeLine(a.FileName)
		}
		if a.Caption != "" {
			label += " · " + theme.SanitizeLine(a.Caption)
		}
		return mediaIcon(m, ic) + " " + label
	}
	return ""
}

// previewLines renders the messages around the selected hit, centered on it.
func (s *Search) previewLines(w, h int) []string {
	st := s.ctx.Styles
	if s.sel < 0 || s.sel >= len(s.order) {
		if s.parsed.IsEmpty() {
			return nil
		}
		return []string{"", st.ChatListPreview.Render(clip("Nothing selected", w))}
	}
	hit := s.hits[s.order[s.sel]]
	id := hit.Message.ID
	title := theme.SanitizeLine(hit.ChatTitle)
	f := format.TimeFormatter{Hour12: s.ctx.Config.TimeFormat == "12h"}
	head := []string{
		clip(st.Title.UnsetMargins().UnsetPadding().Render(title), w),
		clip(st.ChatListPreview.Render(f.DayHeader(hitTime(hit), s.now())), w),
		hrule(st, w),
	}
	msgs, ok := s.preview[id]
	if e, bad := s.previewErr[id]; bad {
		return append(head, st.ErrorText.Render(clip(theme.SanitizeLine(e), w)))
	}
	if !ok {
		return append(head, st.ChatListPreview.Render("Loading…"))
	}
	terms := highlightTerms(s.parsed.Text)
	var body []string
	hitLine := 0
	for _, m := range msgs {
		isHit := m.ID == id
		if isHit {
			hitLine = len(body)
		}
		body = append(body, s.previewMessage(m, isHit, terms, w, f)...)
	}
	avail := h - len(head)
	start := 0
	if len(body) > avail {
		start = min(max(hitLine-avail/3, 0), len(body)-avail)
	}
	end := min(len(body), start+max(avail, 0))
	return append(head, body[start:end]...)
}

// highlightTerms splits the free text of a query into lower-cased terms.
func highlightTerms(text string) []string {
	var out []string
	for _, t := range strings.Fields(strings.ToLower(text)) {
		if t != "" {
			out = append(out, t)
		}
	}
	if t := strings.ToLower(strings.TrimSpace(text)); t != "" && len(out) > 1 {
		out = append([]string{t}, out...)
	}
	return out
}

// termRanges finds case-insensitive occurrences of terms in s (ASCII-safe
// folding; non-matching case variants are simply not highlighted).
func termRanges(s string, terms []string) [][2]int {
	if len(terms) == 0 {
		return nil
	}
	ls := strings.ToLower(s)
	if len(ls) != len(s) {
		return nil
	}
	var out [][2]int
	for _, t := range terms {
		for i := 0; ; {
			j := strings.Index(ls[i:], t)
			if j < 0 {
				break
			}
			out = append(out, [2]int{i + j, i + j + len(t)})
			i += j + len(t)
		}
	}
	return out
}

func (s *Search) previewMessage(m core.Message, isHit bool, terms []string, w int, f format.TimeFormatter) []string {
	st := s.ctx.Styles
	bar := "  "
	if isHit {
		bar = lipgloss.NewStyle().Foreground(st.Theme.Palette.Accent).Render("▌") + " "
	}
	if m.Kind == core.KindSystem || m.Kind == core.KindCall {
		return []string{bar + st.SystemMessage.UnsetWidth().UnsetAlign().Render(clip(describe(m, s.ctx.Icons), max(w-2, 1)))}
	}
	sender := "You"
	ss := st.SenderName.Bold(true)
	if !m.Outgoing {
		sender = theme.SanitizeLine(m.SenderName)
		ss = st.SenderStyle(string(m.ID.Author)).Bold(true)
	}
	out := []string{bar + clip(ss.Render(sender)+" "+st.Timestamp.Render(f.Clock(m.SentAt)), max(w-2, 1))}
	text := ""
	if m.RemoteDeleted {
		text = "This message was deleted"
	} else if m.Body != "" {
		text = format.Snippet(m.Body, m.Ranges, nil, 0)
	}
	if d := describe(m, s.ctx.Icons); d != "" && (text == "" || len(m.Attachments) > 0 || m.Sticker != nil) && !m.RemoteDeleted {
		out = append(out, bar+st.ChatListPreview.Render(clip(d, max(w-2, 1))))
	}
	if text != "" {
		base := lipgloss.NewStyle().Foreground(st.Theme.Palette.Fg)
		if !isHit {
			base = lipgloss.NewStyle().Foreground(st.Theme.Palette.FgMuted)
		}
		for i, line := range format.Wrap(text, max(w-2, 1)) {
			if i >= 6 {
				out = append(out, bar+st.ChatListPreview.Render("…"))
				break
			}
			out = append(out, bar+hardClip(highlightRanges(line, termRanges(line, terms), base, st.FuzzyMatch), max(w-2, 1)))
		}
	}
	return out
}
