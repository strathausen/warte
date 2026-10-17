package media

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"warte/internal/config"
	"warte/internal/core"
	"warte/internal/format"
	"warte/internal/tui/keymap"
	"warte/internal/tui/ui"
)

// KeyContextGallery is the keymap context of the media gallery.
const KeyContextGallery = "media"

// galleryPage is how many messages one load step scans.
const galleryPage = 200

// Tab is a gallery tab.
type Tab int

const (
	TabMedia Tab = iota // photos, GIFs and videos (thumbnail grid)
	TabFiles            // documents and other files
	TabLinks            // URLs from link previews and message bodies
	TabVoice            // voice notes and audio files
	tabCount
)

func (t Tab) String() string { return [...]string{"Media", "Files", "Links", "Voice"}[t] }

// LinkItem is a link shared in a chat.
type LinkItem struct {
	Message core.Message
	URL     string
	Title   string
}

// Gallery is the per-chat media overlay: a thumbnail grid of photos and
// videos plus lists of files, links and voice/audio, loaded page by page by
// scanning Service.Messages backwards (newest first).
type Gallery struct {
	ctx       ui.Context
	chat      core.ChatID
	chatTitle string
	tab       Tab

	media, files, voice []Item
	links               []LinkItem
	seen                map[string]bool

	sel [tabCount]int
	top [tabCount]int // first visible grid row / list row

	before  core.MessageID
	done    bool
	loading bool
	scanned int
	err     string

	lastW, lastH int
	gridCols     int
}

type galleryPageMsg struct {
	g     *Gallery
	title string
	msgs  []core.Message
	err   error
}

// NewGallery builds the gallery for a chat.
func NewGallery(ctx ui.Context, chat core.ChatID) *Gallery {
	return &Gallery{ctx: ctx, chat: chat, seen: map[string]bool{}}
}

// NewGalleryTab builds the gallery opened on a given tab.
func NewGalleryTab(ctx ui.Context, chat core.ChatID, tab Tab) *Gallery {
	g := NewGallery(ctx, chat)
	if tab >= 0 && tab < tabCount {
		g.tab = tab
	}
	return g
}

// Tab returns the active tab.
func (g *Gallery) Tab() Tab { return g.tab }

// Count returns the number of loaded entries on a tab.
func (g *Gallery) Count(t Tab) int {
	switch t {
	case TabMedia:
		return len(g.media)
	case TabFiles:
		return len(g.files)
	case TabLinks:
		return len(g.links)
	case TabVoice:
		return len(g.voice)
	}
	return 0
}

// Selected returns the selected index on the active tab.
func (g *Gallery) Selected() int { return g.sel[g.tab] }

// Done reports whether the whole chat history has been scanned.
func (g *Gallery) Done() bool { return g.done }

// Init loads the first page.
func (g *Gallery) Init() tea.Cmd { return g.loadMore() }

func (g *Gallery) loadMore() tea.Cmd {
	if g.loading || g.done || g.ctx.Svc == nil {
		return nil
	}
	g.loading = true
	svc, chat, before := g.ctx.Svc, g.chat, g.before
	first := g.scanned == 0
	return func() tea.Msg {
		c, cancel := opCtx()
		defer cancel()
		msg := galleryPageMsg{g: g}
		if first {
			if ch, err := svc.Chat(c, chat); err == nil {
				msg.title = ch.Title
			}
		}
		msg.msgs, msg.err = svc.Messages(c, chat, before, galleryPage)
		return msg
	}
}

// classify adds a message's media to the tab lists. prepend puts them in
// front (new messages); otherwise they are appended (older pages).
func (g *Gallery) classify(m core.Message, prepend bool) {
	if m.RemoteDeleted || m.ViewOnce || m.Kind != core.KindNormal {
		return
	}
	var media, files, voice []Item
	for _, a := range m.Attachments {
		it := Item{Message: m, Attachment: a}
		if g.seen[it.key()] {
			continue
		}
		g.seen[it.key()] = true
		switch {
		case IsVisual(a):
			media = append(media, it)
		case a.IsAudio():
			voice = append(voice, it)
		default:
			files = append(files, it)
		}
	}
	var links []LinkItem
	lk := "link:" + m.ID.String()
	if !g.seen[lk] {
		g.seen[lk] = true
		have := map[string]bool{}
		if p := m.Preview; p != nil && strings.TrimSpace(p.URL) != "" {
			links = append(links, LinkItem{Message: m, URL: p.URL, Title: p.Title})
			have[p.URL] = true
		}
		for _, l := range format.FindLinks(m.Body) {
			if l.Kind != format.LinkURL || have[l.URL] {
				continue
			}
			have[l.URL] = true
			links = append(links, LinkItem{Message: m, URL: l.URL})
		}
	}
	if prepend {
		g.media = append(media, g.media...)
		g.files = append(files, g.files...)
		g.voice = append(voice, g.voice...)
		g.links = append(links, g.links...)
		// Keep the selection on the same entry (an empty list keeps the
		// newest entry selected).
		shift := func(t Tab, added int) {
			if g.Count(t) > added {
				g.sel[t] += added
			}
		}
		shift(TabMedia, len(media))
		shift(TabFiles, len(files))
		shift(TabVoice, len(voice))
		shift(TabLinks, len(links))
		return
	}
	g.media = append(g.media, media...)
	g.files = append(g.files, files...)
	g.voice = append(g.voice, voice...)
	g.links = append(g.links, links...)
}

// remove drops every entry of a message.
func (g *Gallery) remove(id core.MessageID) {
	keep := func(items []Item) []Item {
		out := items[:0]
		for _, it := range items {
			if it.Message.ID != id {
				out = append(out, it)
			}
		}
		return out
	}
	g.media, g.files, g.voice = keep(g.media), keep(g.files), keep(g.voice)
	links := g.links[:0]
	for _, l := range g.links {
		if l.Message.ID != id {
			links = append(links, l)
		}
	}
	g.links = links
	for t := range tabCount {
		g.sel[t] = clampInt(g.sel[t], 0, max(g.Count(t)-1, 0))
	}
}

// update applies an edited message: remote-deleted (or view-once)
// messages drop out, others refresh their message and attachment data.
func (g *Gallery) update(m core.Message) {
	if m.ID.Chat != g.chat {
		return
	}
	if m.RemoteDeleted || m.ViewOnce || m.Kind != core.KindNormal {
		g.remove(m.ID)
		return
	}
	refresh := func(items []Item) {
		for i := range items {
			if items[i].Message.ID != m.ID {
				continue
			}
			items[i].Message = m
			for _, a := range m.Attachments {
				if a.ID == items[i].Attachment.ID {
					items[i].Attachment = a
				}
			}
		}
	}
	refresh(g.media)
	refresh(g.files)
	refresh(g.voice)
	for i := range g.links {
		if g.links[i].Message.ID == m.ID {
			g.links[i].Message = m
		}
	}
}

// wanted is how many entries the active tab should have loaded.
func (g *Gallery) wanted() int {
	per := max(g.gridCols, 1) * 4
	if g.tab != TabMedia {
		per = max(g.lastH, 10)
	}
	return g.sel[g.tab] + per + 1
}

// maybeLoad loads more when the active tab needs entries.
func (g *Gallery) maybeLoad() tea.Cmd {
	if g.done || g.loading || g.Count(g.tab) >= g.wanted() {
		return nil
	}
	return g.loadMore()
}

// Title implements ui.Overlay.
func (g *Gallery) Title() string {
	if t := clean(g.chatTitle); strings.TrimSpace(t) != "" {
		return "Media · " + t
	}
	return "Media"
}

// KeyContext implements ui.Overlay.
func (g *Gallery) KeyContext() string { return KeyContextGallery }

// Size implements ui.Overlay.
func (g *Gallery) Size(maxW, maxH int) (int, int) {
	w := min(maxW, 120)
	hMax := min(maxH, 40)
	head, foot := g.chrome(hMax)
	need := 0
	if g.tab == TabMedia {
		tw, th := thumbSize(w)
		cols := max((w+1)/(tw+3), 1)
		need = (g.Count(TabMedia) + cols - 1) / cols * (th + 2)
	} else {
		need = g.Count(g.tab)
	}
	return w, clampInt(head+foot+need, min(16, hMax), hMax)
}

// Footer implements ui.Footer.
func (g *Gallery) Footer() string {
	st := g.ctx.Styles
	switch g.tab {
	case TabMedia:
		return st.RenderKeyHints("hjkl", "move", "tab", "next tab", "enter", "view", "m", "go to message", "esc", "close")
	case TabVoice:
		return st.RenderKeyHints("j/k", "move", "tab", "next tab", "enter", "play", "m", "go to message", "esc", "close")
	case TabLinks:
		return st.RenderKeyHints("j/k", "move", "tab", "next tab", "enter", "go to message", "o", "open link", "esc", "close")
	}
	return st.RenderKeyHints("j/k", "move", "tab", "next tab", "enter", "go to message", "o", "open", "esc", "close")
}

// Update implements ui.Overlay.
func (g *Gallery) Update(msg tea.Msg) (ui.Overlay, tea.Cmd) {
	switch msg := msg.(type) {
	case galleryPageMsg:
		if msg.g != g {
			return g, nil
		}
		g.loading = false
		if msg.title != "" {
			g.chatTitle = msg.title
		}
		if msg.err != nil {
			g.err = clean(msg.err.Error())
			g.done = true
			return g, nil
		}
		g.scanned += len(msg.msgs)
		for i := len(msg.msgs) - 1; i >= 0; i-- {
			g.classify(msg.msgs[i], false)
		}
		if len(msg.msgs) < galleryPage || (len(msg.msgs) > 0 && msg.msgs[0].ID == g.before) {
			g.done = true
		}
		if len(msg.msgs) > 0 {
			g.before = msg.msgs[0].ID
		}
		return g, g.maybeLoad()
	case ui.EventMsg:
		switch e := msg.Event.(type) {
		case core.MessageAdded:
			if e.Message.ID.Chat == g.chat {
				g.classify(e.Message, true)
			}
		case core.MessageRemoved:
			g.remove(e.ID)
		case core.MessageUpdated:
			g.update(e.Message)
		}
	case ui.ActionMsg:
		return g.action(msg)
	case tea.KeyPressMsg:
		return g.key(msg.String())
	case tea.MouseClickMsg:
		return g.click(msg.X, msg.Y)
	case tea.MouseWheelMsg:
		step := 1
		if g.tab == TabMedia {
			step = max(g.gridCols, 1)
		}
		if msg.Button == tea.MouseWheelUp {
			step = -step
		}
		g.move(step)
		return g, g.maybeLoad()
	}
	return g, nil
}

func (g *Gallery) action(msg ui.ActionMsg) (ui.Overlay, tea.Cmd) {
	n := max(msg.Count, 1)
	cols := 1
	if g.tab == TabMedia {
		cols = max(g.gridCols, 1)
	}
	page := max(g.visibleRows()*cols, 1)
	switch msg.Action {
	case keymap.ActCursorDown:
		g.move(n * cols)
	case keymap.ActCursorUp:
		g.move(-n * cols)
	case keymap.ActCursorLeft:
		if g.tab == TabMedia {
			g.move(-n)
		} else {
			g.switchTab(-1)
		}
	case keymap.ActCursorRight:
		if g.tab == TabMedia {
			g.move(n)
		} else {
			g.switchTab(1)
		}
	case keymap.ActHalfPageDown:
		g.move(max(page/2, cols) / cols * cols)
	case keymap.ActHalfPageUp:
		g.move(-max(page/2, cols) / cols * cols)
	case keymap.ActPageDown:
		g.move(page)
	case keymap.ActPageUp:
		g.move(-page)
	case keymap.ActCursorTop:
		g.sel[g.tab] = 0
	case keymap.ActCursorBottom:
		g.sel[g.tab] = max(g.Count(g.tab)-1, 0)
	case keymap.ActFocusNext, keymap.ActFocusRight, keymap.ActFolderNext:
		g.switchTab(1)
	case keymap.ActFocusPrev, keymap.ActFocusLeft, keymap.ActFolderPrev:
		g.switchTab(-1)
	case keymap.ActItemSelect:
		return g, g.open()
	case keymap.ActMediaGallery:
		return nil, nil
	case keymap.ActMessageOpenAttachment:
		return g.key("o")
	}
	return g, g.maybeLoad()
}

func (g *Gallery) key(k string) (ui.Overlay, tea.Cmd) {
	switch k {
	case "]", "tab":
		g.switchTab(1)
	case "[", "shift+tab":
		g.switchTab(-1)
	case "o":
		return g, g.openExternal()
	case "s":
		if it, ok := g.selectedItem(); ok {
			return g, SaveCmd(g.ctx, it)
		}
	case "y":
		if it, ok := g.selectedItem(); ok {
			return g, copyCmd(g.ctx, it)
		}
	case "m":
		if m, ok := g.selectedMessage(); ok {
			return nil, jumpTo(m.ID)
		}
	}
	return g, g.maybeLoad()
}

func (g *Gallery) switchTab(d int) {
	g.tab = Tab((int(g.tab) + d + int(tabCount)) % int(tabCount))
}

// SetTab switches tabs.
func (g *Gallery) SetTab(t Tab) tea.Cmd {
	if t >= 0 && t < tabCount {
		g.tab = t
	}
	return g.maybeLoad()
}

func (g *Gallery) move(d int) {
	n := g.Count(g.tab)
	if n == 0 {
		g.sel[g.tab] = 0
		return
	}
	g.sel[g.tab] = clampInt(g.sel[g.tab]+d, 0, n-1)
}

func jumpTo(id core.MessageID) tea.Cmd {
	return func() tea.Msg { return ui.OpenChatMsg{Chat: id.Chat, Focus: &id} }
}

// selectedItem returns the selected attachment (not on the Links tab).
func (g *Gallery) selectedItem() (Item, bool) {
	var list []Item
	switch g.tab {
	case TabMedia:
		list = g.media
	case TabFiles:
		list = g.files
	case TabVoice:
		list = g.voice
	default:
		return Item{}, false
	}
	i := g.sel[g.tab]
	if i < 0 || i >= len(list) {
		return Item{}, false
	}
	return list[i], true
}

func (g *Gallery) selectedLink() (LinkItem, bool) {
	i := g.sel[TabLinks]
	if g.tab != TabLinks || i < 0 || i >= len(g.links) {
		return LinkItem{}, false
	}
	return g.links[i], true
}

func (g *Gallery) selectedMessage() (core.Message, bool) {
	if l, ok := g.selectedLink(); ok {
		return l.Message, true
	}
	if it, ok := g.selectedItem(); ok {
		return it.Message, true
	}
	return core.Message{}, false
}

// open implements enter: the viewer for media, the player for voice and
// audio, jump to the message for files and links.
func (g *Gallery) open() tea.Cmd {
	switch g.tab {
	case TabMedia:
		if len(g.media) == 0 {
			return nil
		}
		// The viewer walks oldest → newest; the grid is newest first.
		items := make([]Item, len(g.media))
		for i, it := range g.media {
			items[len(items)-1-i] = it
		}
		return ui.Push(NewViewerList(g.ctx, items, len(items)-1-g.sel[TabMedia]))
	case TabVoice:
		if it, ok := g.selectedItem(); ok {
			return ui.Push(NewPlayerOverlay(g.ctx, it.Message, it.Attachment))
		}
	default:
		if m, ok := g.selectedMessage(); ok {
			return tea.Batch(ui.CloseOverlay, jumpTo(m.ID))
		}
	}
	return nil
}

// openExternal implements "o".
func (g *Gallery) openExternal() tea.Cmd {
	if l, ok := g.selectedLink(); ok {
		return openURL(g.ctx, l.URL)
	}
	if it, ok := g.selectedItem(); ok {
		return openExternal(g.ctx, it)
	}
	return nil
}

// openURL opens a link with the configured URL opener.
func openURL(ctx ui.Context, u string) tea.Cmd {
	opener := strings.Fields(ctx.Config.Opener(config.OpenURL))
	if len(opener) == 0 {
		return toast(ui.ToastError, "No opener configured")
	}
	u, ok := safeURL(u)
	if !ok {
		return toast(ui.ToastWarning, "Only http(s) links can be opened")
	}
	return func() tea.Msg {
		if err := startDetached(opener[0], append(opener[1:], u)...); err != nil {
			return ui.ToastMsg{Level: ui.ToastError, Text: errText("Open link", err)}
		}
		return ui.ToastMsg{Level: ui.ToastInfo, Text: "Opening " + clean(u)}
	}
}

// safeURL strips control characters from an untrusted URL and accepts only
// absolute http, https and mailto URLs, so a link preview from a peer can't
// hand the opener a local file, an app scheme or a command-line flag.
func safeURL(u string) (string, bool) {
	u = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return -1
		}
		return r
	}, u))
	pu, err := url.Parse(u)
	if err != nil {
		return "", false
	}
	switch strings.ToLower(pu.Scheme) {
	case "http", "https":
		if pu.Host == "" {
			return "", false
		}
	case "mailto":
		if pu.Opaque == "" {
			return "", false
		}
	default:
		return "", false
	}
	return pu.String(), true
}

// ---- layout -------------------------------------------------------------------------

// thumbSize returns the thumbnail size (inside the cell border) for a width.
func thumbSize(w int) (tw, th int) {
	switch {
	case w >= 100:
		return 18, 8
	case w >= 48:
		return 14, 6
	}
	return 10, 4
}

// header lines (tab bar + blank) and footer lines (selection info).
func (g *Gallery) chrome(h int) (head, foot int) {
	head = 1
	if h >= 8 {
		head = 2
	}
	if h >= 6 {
		foot = 1
	}
	return head, foot
}

// visibleRows is the number of grid rows / list rows that fit.
func (g *Gallery) visibleRows() int {
	head, foot := g.chrome(g.lastH)
	body := max(g.lastH-head-foot, 1)
	if g.tab == TabMedia {
		_, th := thumbSize(g.lastW)
		return max(body/(th+2), 1)
	}
	return body
}

// click handles a mouse click at content coordinates.
func (g *Gallery) click(x, y int) (ui.Overlay, tea.Cmd) {
	if y == 0 {
		pos := 0
		for t := range tabCount {
			w := width(g.tabLabel(t)) + 3
			if x >= pos && x < pos+w {
				g.tab = t
				return g, g.maybeLoad()
			}
			pos += w
		}
		return g, nil
	}
	head, _ := g.chrome(g.lastH)
	y -= head
	if y < 0 {
		return g, nil
	}
	idx := -1
	if g.tab == TabMedia {
		tw, th := thumbSize(g.lastW)
		cols := max(g.gridCols, 1)
		used := min(cols, g.Count(TabMedia))
		gridW := used*(tw+2) + (used-1)*1
		left := max((g.lastW-gridW)/2, 0)
		cx := (x - left) / (tw + 3)
		if x < left || cx >= cols {
			return g, nil
		}
		row := g.top[TabMedia] + y/(th+2)
		idx = row*cols + cx
	} else {
		idx = g.top[g.tab] + y
	}
	if idx >= 0 && idx < g.Count(g.tab) {
		if idx == g.sel[g.tab] {
			return g, g.open()
		}
		g.sel[g.tab] = idx
	}
	return g, g.maybeLoad()
}

func (g *Gallery) tabLabel(t Tab) string {
	n := g.Count(t)
	more := ""
	if !g.done {
		more = "+"
	}
	if n == 0 && !g.done {
		return t.String()
	}
	return fmt.Sprintf("%s %d%s", t, n, more)
}

// View implements ui.Overlay.
func (g *Gallery) View(w, h int) string {
	g.lastW, g.lastH = w, h
	if w <= 0 || h <= 0 {
		return ""
	}
	tw, _ := thumbSize(w)
	g.gridCols = max((w+1)/(tw+3), 1)
	head, foot := g.chrome(h)
	lines := []string{g.tabBar(w)}
	if head > 1 {
		lines = append(lines, "")
	}
	body := max(h-head-foot, 0)
	var content []string
	switch {
	case g.Count(g.tab) == 0:
		content = g.emptyView(w, body)
	case g.tab == TabMedia:
		content = g.gridView(w, body)
	default:
		content = g.listView(w, body)
	}
	lines = append(lines, fitLines(content, w, body)...)
	if foot > 0 {
		lines = append(lines, g.infoLine(w))
	}
	return strings.Join(fitLines(lines, w, h), "\n")
}

func (g *Gallery) tabBar(w int) string {
	st := g.ctx.Styles
	p := st.Theme.Palette
	active := lipgloss.NewStyle().Foreground(p.Accent).Bold(true).Underline(true)
	if st.Theme.Mono {
		active = lipgloss.NewStyle().Reverse(true)
	}
	var parts []string
	for t := range tabCount {
		label := " " + g.tabLabel(t) + " "
		if t == g.tab {
			parts = append(parts, active.Render(label))
		} else {
			parts = append(parts, st.KeyHintDesc.Render(label))
		}
	}
	return fit(strings.Join(parts, st.KeyHintSep.Render("│")), w)
}

func (g *Gallery) emptyView(w, h int) []string {
	st := g.ctx.Styles
	text := "No " + strings.ToLower(g.tab.String()) + " in this chat"
	switch {
	case g.err != "":
		text = "Couldn't load: " + g.err
	case !g.done:
		text = fmt.Sprintf("Searching… (%d messages)", g.scanned)
	}
	out := make([]string, max(h, 0))
	if h > 0 {
		out[h/2] = st.Subtitle.Render(center(ellipsis(text, w), w))
	}
	return out
}

// gridView renders the thumbnail grid.
func (g *Gallery) gridView(w, h int) []string {
	st := g.ctx.Styles
	p := st.Theme.Palette
	tw, th := thumbSize(w)
	cols := g.gridCols
	cellH := th + 2
	rowsVis := max(h/cellH, 1)
	n := len(g.media)
	sel := clampInt(g.sel[TabMedia], 0, n-1)
	g.sel[TabMedia] = sel
	selRow := sel / cols
	top := g.top[TabMedia]
	if selRow < top {
		top = selRow
	}
	if selRow >= top+rowsVis {
		top = selRow - rowsVis + 1
	}
	g.top[TabMedia] = top
	used := min(cols, n)
	gridW := used*(tw+2) + (used-1)*1
	pad := strings.Repeat(" ", max((w-gridW)/2, 0))
	normal := lipgloss.NewStyle().Foreground(p.Border)
	focus := lipgloss.NewStyle().Foreground(p.BorderFocus).Bold(true)
	var out []string
	for r := top; r < top+rowsVis; r++ {
		if r*cols >= n {
			break
		}
		block := make([]string, cellH)
		for c := 0; c < used; c++ {
			i := r*cols + c
			var cell []string
			if i < n {
				bs := normal
				if i == sel {
					bs = focus
				}
				cell = g.thumbCell(g.media[i], tw, th, bs)
			} else {
				cell = fitLines(nil, tw+2, cellH)
			}
			for y := range block {
				if c > 0 {
					block[y] += " "
				}
				block[y] += cell[y]
			}
		}
		for _, l := range block {
			out = append(out, pad+l)
		}
	}
	return out
}

// thumbCell renders one bordered thumbnail (tw+2 x th+2).
func (g *Gallery) thumbCell(it Item, tw, th int, bs lipgloss.Style) []string {
	a := it.Attachment
	var img []string
	if a.IsImage() && g.ctx.Images != nil && g.ctx.Images.Enabled() {
		ref := it.Ref()
		c, r := g.ctx.Images.Size(ref, tw, th)
		c, r = clampInt(c, 1, tw), clampInt(r, 1, th)
		rows, _ := g.ctx.Images.Rows(ref, c, r)
		padL := (tw - c) / 2
		padT := (th - r) / 2
		img = make([]string, 0, th)
		for i := 0; i < padT; i++ {
			img = append(img, "")
		}
		for _, row := range rows {
			img = append(img, strings.Repeat(" ", padL)+row)
		}
	} else if rows := blurRows(a.BlurHash, tw, th); rows != nil {
		img = rows
	} else {
		label := kindIcon(g.ctx.Icons, a)
		if a.IsVideo() {
			label = "▶"
		}
		img = styledBox(g.ctx.Styles, tw, th, label)
	}
	img = fitLines(img, tw, th)
	label := ""
	switch {
	case a.IsVideo():
		label = "▶ Video"
	case a.GIF || a.ContentType == "image/gif":
		label = "GIF"
	}
	top := "╭" + strings.Repeat("─", tw) + "╮"
	bottom := "╰" + strings.Repeat("─", tw) + "╯"
	if label != "" && tw >= width(label)+4 {
		bottom = "╰─ " + label + " " + strings.Repeat("─", tw-width(label)-3) + "╯"
	}
	out := []string{bs.Render(top)}
	for _, l := range img {
		out = append(out, bs.Render("│")+l+bs.Render("│"))
	}
	return append(out, bs.Render(bottom))
}

// listView renders the Files, Links and Voice lists.
func (g *Gallery) listView(w, h int) []string {
	st := g.ctx.Styles
	n := g.Count(g.tab)
	sel := clampInt(g.sel[g.tab], 0, n-1)
	g.sel[g.tab] = sel
	top := g.top[g.tab]
	if sel < top {
		top = sel
	}
	if sel >= top+h {
		top = sel - h + 1
	}
	g.top[g.tab] = clampInt(top, 0, max(n-1, 0))
	var out []string
	for i := g.top[g.tab]; i < n && len(out) < h; i++ {
		icon, name, meta := g.row(i)
		metaW := min(width(meta), w/2)
		meta = ellipsis(meta, metaW)
		nameW := max(w-metaW-width(icon)-4, 1)
		left := " " + icon + " " + ellipsis(name, nameW)
		gap := max(w-width(left)-width(meta)-1, 1)
		line := left + strings.Repeat(" ", gap) + meta + " "
		if i == sel {
			out = append(out, st.ChatListItemSelected.Render(fit(line, w)))
		} else {
			out = append(out, fit(st.ChatListItem.Render(left)+strings.Repeat(" ", gap)+st.Timestamp.Render(meta), w))
		}
	}
	return out
}

// row returns icon, name and meta text for list entry i of the active tab.
func (g *Gallery) row(i int) (icon, name, meta string) {
	var m core.Message
	ic := g.ctx.Icons
	switch g.tab {
	case TabLinks:
		l := g.links[i]
		m = l.Message
		icon = ic.Link
		name = clean(l.URL)
		if t := strings.TrimSpace(clean(l.Title)); t != "" {
			name = t + " — " + name
		}
	default:
		var it Item
		if g.tab == TabFiles {
			it = g.files[i]
		} else {
			it = g.voice[i]
		}
		m = it.Message
		a := it.Attachment
		icon = kindIcon(ic, a)
		name = clean(strings.TrimSpace(a.FileName))
		if name == "" || a.VoiceNote {
			name = kindLabel(a)
		}
		if s := humanSize(a.Size); s != "" {
			name += "  " + s
		}
	}
	meta = senderName(m)
	now := g.now()
	if !m.SentAt.IsZero() {
		meta += " · " + timeFormatter(g.ctx.Config).RelativeTime(m.SentAt, now)
	}
	return icon, name, meta
}

func (g *Gallery) now() time.Time {
	if g.ctx.Now != nil {
		return g.ctx.Now()
	}
	return time.Now()
}

// infoLine describes the selected entry.
func (g *Gallery) infoLine(w int) string {
	st := g.ctx.Styles
	if g.loading && g.Count(g.tab) > 0 {
		return st.Subtitle.Render(ellipsis("loading more…", w))
	}
	if g.err != "" && g.Count(g.tab) > 0 {
		return st.ErrorText.Render(ellipsis("Couldn't load older messages: "+g.err, w))
	}
	if g.tab != TabMedia {
		if g.Count(g.tab) > 0 {
			return st.Subtitle.Render(ellipsis(fmt.Sprintf("%d of %d", g.sel[g.tab]+1, g.Count(g.tab)), w))
		}
		return ""
	}
	it, ok := g.selectedItem()
	if !ok {
		return ""
	}
	parts := []string{st.SenderStyle(string(it.Message.ID.Author)).Render(senderName(it.Message))}
	if d := dateLine(g.ctx.Config, it.Message.SentAt); d != "" {
		parts = append(parts, st.Timestamp.Render(d))
	}
	if c := strings.TrimSpace(clean(it.Attachment.Caption)); c != "" {
		parts = append(parts, st.App.Render(c))
	}
	return ellipsis(strings.Join(parts, st.KeyHintSep.Render(" · ")), w)
}
