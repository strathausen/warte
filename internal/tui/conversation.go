package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"warte/internal/core"
	"warte/internal/format"
	"warte/internal/tui/keymap"
	"warte/internal/tui/ui"
)

// pageSize is how many messages are loaded per history page.
const pageSize = 100

// msgEntry is one loaded message. rev increases on every update so cached
// renders are invalidated.
type msgEntry struct {
	m   core.Message
	rev int
}

// blockKey identifies a cached message render.
type blockKey struct {
	id     core.MessageID
	rev    int
	width  int
	flags  uint16
	query  string
	media  int // image generation, only for messages with media
	minute int64
	theme  int
}

const (
	flagSelected uint16 = 1 << iota
	flagFocused
	flagDay
	flagUnread
	flagFirst // first of a sender run
	flagLast  // last of a sender run
	flagRevealed
	flagVisual
	flagIRC
	flagSearchHit
)

// block is a rendered message: its lines and mouse hit regions relative to
// the block's top-left corner.
type block struct {
	lines []string
	zones []zone
}

type conversation struct {
	open    bool
	chat    core.Chat
	msgs    []*msgEntry
	index   map[core.MessageID]int
	sel     int
	bottom  int // index of the message at the bottom of the viewport
	skip    int // lines of msgs[bottom] hidden below the viewport
	follow  bool
	hasMore bool
	loading bool
	// unreadFrom is the first unread message when the chat was opened.
	unreadFrom core.MessageID
	revealed   map[core.MessageID]bool
	visual     int // visual mode anchor
	cache      map[blockKey]*block
	mediaGen   int
	themeGen   int
	viewH      int // message area height at the last render
	viewW      int
}

func newConversation() *conversation {
	return &conversation{
		index:    map[core.MessageID]int{},
		revealed: map[core.MessageID]bool{},
		cache:    map[blockKey]*block{},
		visual:   -1,
	}
}

func (c *conversation) chatID() core.ChatID {
	if !c.open {
		return ""
	}
	return c.chat.ID
}

func (c *conversation) selected() *core.Message {
	if !c.open || c.sel < 0 || c.sel >= len(c.msgs) {
		return nil
	}
	m := c.msgs[c.sel].m
	return &m
}

func (c *conversation) last() int { return len(c.msgs) - 1 }

func (c *conversation) invalidate() {
	c.cache = map[blockKey]*block{}
	c.themeGen++
}

func (c *conversation) invalidateMedia() { c.mediaGen++ }

func (c *conversation) reindex() {
	c.index = make(map[core.MessageID]int, len(c.msgs))
	for i, e := range c.msgs {
		c.index[e.m.ID] = i
	}
}

// atBottom reports whether the newest message is fully visible at the bottom.
func (c *conversation) atBottom() bool {
	return len(c.msgs) == 0 || (c.bottom >= c.last() && c.skip == 0)
}

// ---- opening and loading ---------------------------------------------------------

// openChat shows a chat in the conversation pane. focus optionally selects
// a message (loading history as needed). userAction moves keyboard focus to
// the conversation.
func (a *App) openChat(id core.ChatID, focus *core.MessageID, userAction bool) tea.Cmd {
	var cmds []tea.Cmd
	if a.conv.open && a.conv.chat.ID != id {
		if userAction {
			a.recordJump()
		}
		a.comp.saveDraft(a)
		cmds = append(cmds, a.comp.stopTyping(a))
		a.comp.reset()
	}
	ctx, cancel := a.queryCtx()
	defer cancel()
	chat, err := a.svc.Chat(ctx, id)
	if err != nil {
		return a.toast(ui.ToastError, "open chat: "+err.Error())
	}
	same := a.conv.open && a.conv.chat.ID == id
	c := a.conv
	c.open = true
	c.chat = chat
	if !same {
		msgs, err := a.svc.Messages(ctx, id, core.MessageID{}, pageSize)
		if err != nil {
			return a.toast(ui.ToastError, "load messages: "+err.Error())
		}
		c.msgs = c.msgs[:0]
		for _, m := range msgs {
			c.msgs = append(c.msgs, &msgEntry{m: m})
		}
		c.reindex()
		c.hasMore = len(msgs) >= pageSize
		c.loading = false
		c.revealed = map[core.MessageID]bool{}
		c.cache = map[blockKey]*block{}
		c.visual = -1
		if a.keys.Mode().IsVisual() {
			a.keys.SetMode(keymap.Normal)
		}
		c.unreadFrom = core.MessageID{}
		c.sel = c.last()
		c.bottom, c.skip = c.last(), 0
		c.follow = true
		if n := chat.Unread; n > 0 {
			// The unread divider goes above the n-th newest incoming message.
			k := 0
			for i := c.last(); i >= 0; i-- {
				if !c.msgs[i].m.Outgoing && c.msgs[i].m.Kind != core.KindSystem {
					k++
					if k == n {
						c.unreadFrom = c.msgs[i].m.ID
						c.sel = i
						break
					}
				}
			}
		}
		a.comp.loadDraft(a, chat)
		a.search.clearHits()
	}
	if i := a.list.indexOf(id); i >= 0 {
		a.list.sel = i
		a.list.ensureVisible(a.cfg.ReduceMotion)
	}
	if userAction {
		a.setFocus(FocusConversation)
	}
	a.coalescer.SetActiveChat(string(id))
	a.coalescer.Clear(string(id))
	if focus != nil {
		cmds = append(cmds, a.jumpTo(*focus))
	} else if !same {
		a.ensureSelVisible()
	}
	if userAction && focus == nil {
		// Going into a chat starts in the composer, ready to write.
		a.enterComposerIfWritable()
	}
	cmds = append(cmds, a.markReadIfVisible())
	if !same {
		a.pinnedIdx = 0
		ui.ChatOpened(id)
	}
	return tea.Batch(cmds...)
}

// enterComposerIfWritable focuses the composer in Insert mode unless the
// open chat can't take a message from us yet (message request, blocked,
// announcement-only without admin rights).
func (a *App) enterComposerIfWritable() {
	c := a.conv.chat
	if !a.conv.open || c.MessageRequest || c.Blocked || (c.OnlyAdminsSend && !c.SelfIsAdmin) {
		return
	}
	a.focusComposer()
}

// closeChat empties the conversation pane (chat deleted / left).
func (a *App) closeChat() {
	a.comp.reset()
	a.conv.open = false
	a.conv.msgs = nil
	a.conv.reindex()
	a.coalescer.SetActiveChat("")
	if a.focus == FocusConversation || a.focus == FocusComposer {
		a.setFocus(FocusChatList)
	}
}

type olderLoadedMsg struct {
	chat   core.ChatID
	before core.MessageID
	msgs   []core.Message
	err    error
	// then is run after the page was applied (e.g. continue loading to
	// reach a target).
	all    bool
	target *core.MessageID
}

// loadOlder fetches the page before the oldest loaded message.
func (a *App) loadOlder(all bool, target *core.MessageID) tea.Cmd {
	c := a.conv
	if !c.open || !c.hasMore || c.loading {
		return nil
	}
	c.loading = true
	var before core.MessageID
	if len(c.msgs) > 0 {
		before = c.msgs[0].m.ID
	}
	chat := c.chat.ID
	svc := a.svc
	n := pageSize
	if all || target != nil {
		n = 500
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(a.ctx, 10*time.Second)
		defer cancel()
		msgs, err := svc.Messages(ctx, chat, before, n)
		return olderLoadedMsg{chat: chat, before: before, msgs: msgs, err: err, all: all, target: target}
	}
}

func (c *conversation) applyOlder(a *App, msg olderLoadedMsg) tea.Cmd {
	if !c.open || c.chat.ID != msg.chat {
		return nil
	}
	c.loading = false
	if msg.err != nil {
		return a.toast(ui.ToastError, "load history: "+msg.err.Error())
	}
	if len(c.msgs) > 0 && c.msgs[0].m.ID != msg.before {
		return nil // stale
	}
	// Drop anything we already have (defensive against overlapping pages).
	fresh := make([]*msgEntry, 0, len(msg.msgs))
	for _, m := range msg.msgs {
		if _, dup := c.index[m.ID]; !dup {
			fresh = append(fresh, &msgEntry{m: m})
		}
	}
	limit := pageSize
	if msg.all || msg.target != nil {
		limit = 500
	}
	c.hasMore = len(msg.msgs) >= limit
	if len(fresh) > 0 {
		n := len(fresh)
		c.msgs = append(fresh, c.msgs...)
		c.sel += n
		c.bottom += n
		if c.visual >= 0 {
			c.visual += n
		}
		c.reindex()
	}
	switch {
	case msg.target != nil:
		if _, ok := c.index[*msg.target]; ok || !c.hasMore {
			return a.jumpTo(*msg.target)
		}
		return a.loadOlder(false, msg.target)
	case msg.all:
		if c.hasMore {
			return a.loadOlder(true, nil)
		}
		c.sel = 0
		a.ensureSelVisible()
	}
	return nil
}

type jumpLoadedMsg struct{ id core.MessageID }

// maxJumps bounds the jump list.
const maxJumps = 100

// recordJump remembers the selected message before a big jump (gg, G, gq,
// search, switching chats) so ctrl+o can return to it.
func (a *App) recordJump() {
	m := a.conv.selected()
	if m == nil || a.jumping {
		return
	}
	a.jumps = a.jumps[:min(a.jumpIdx, len(a.jumps))]
	if n := len(a.jumps); n == 0 || a.jumps[n-1] != m.ID {
		a.jumps = append(a.jumps, m.ID)
	}
	if len(a.jumps) > maxJumps {
		a.jumps = append([]core.MessageID(nil), a.jumps[len(a.jumps)-maxJumps:]...)
	}
	a.jumpIdx = len(a.jumps)
}

// jumpHistory moves through the jump list: dir < 0 is ctrl+o (back),
// dir > 0 ctrl+i (forward). Jumps across chats open the other chat.
func (a *App) jumpHistory(dir int) tea.Cmd {
	if dir < 0 {
		if a.jumpIdx >= len(a.jumps) {
			// Remember where we are so ctrl+i can come back.
			if m := a.conv.selected(); m != nil {
				if n := len(a.jumps); n == 0 || a.jumps[n-1] != m.ID {
					a.jumps = append(a.jumps, m.ID)
				}
			}
			a.jumpIdx = len(a.jumps) - 1
		}
		if a.jumpIdx <= 0 {
			return a.toast(ui.ToastInfo, "Start of the jump list")
		}
		a.jumpIdx--
	} else {
		if a.jumpIdx >= len(a.jumps)-1 {
			return nil
		}
		a.jumpIdx++
	}
	a.jumping = true
	defer func() { a.jumping = false }()
	return a.jumpTo(a.jumps[a.jumpIdx])
}

// jumpTo selects a message, loading older history until it is found.
func (a *App) jumpTo(id core.MessageID) tea.Cmd {
	c := a.conv
	if !c.open {
		return nil
	}
	if id.Chat != "" && id.Chat != c.chat.ID {
		return a.openChat(id.Chat, &id, true)
	}
	if i, ok := c.index[id]; ok {
		c.sel = i
		a.centerSel()
		return nil
	}
	if !c.hasMore {
		return a.toast(ui.ToastWarning, "Message not found")
	}
	return a.loadOlder(false, &id)
}

func (c *conversation) applyJump(a *App, msg jumpLoadedMsg) tea.Cmd { return a.jumpTo(msg.id) }

// ---- events ---------------------------------------------------------------------

func (a *App) convAdd(m core.Message) tea.Cmd {
	c := a.conv
	if !c.open || m.ID.Chat != c.chat.ID {
		return nil
	}
	if i, ok := c.index[m.ID]; ok {
		c.msgs[i].m = m
		c.msgs[i].rev++
		return nil
	}
	wasBottom := c.atBottom()
	selLast := c.sel == c.last()
	// Insert in timestamp order (normally at the end).
	pos := len(c.msgs)
	for pos > 0 && c.msgs[pos-1].m.ID.Timestamp > m.ID.Timestamp {
		pos--
	}
	e := &msgEntry{m: m}
	c.msgs = append(c.msgs, nil)
	copy(c.msgs[pos+1:], c.msgs[pos:])
	c.msgs[pos] = e
	if pos <= c.sel && !(selLast && pos == len(c.msgs)-1) {
		c.sel++
	}
	if c.visual >= pos {
		c.visual++
	}
	if pos <= c.bottom && !(wasBottom && pos == len(c.msgs)-1) {
		c.bottom++
	}
	c.reindex()
	if wasBottom {
		if selLast || m.Outgoing {
			c.sel = c.last()
		}
		c.bottom, c.skip = c.last(), 0
	}
	// The neighbour's grouping may change.
	if pos > 0 {
		c.msgs[pos-1].rev++
	}
	return a.markReadIfVisible()
}

func (a *App) convUpdate(m core.Message) {
	c := a.conv
	if !c.open || m.ID.Chat != c.chat.ID {
		return
	}
	if i, ok := c.index[m.ID]; ok {
		c.msgs[i].m = m
		c.msgs[i].rev++
	}
}

func (a *App) convRemove(id core.MessageID) {
	c := a.conv
	i, ok := c.index[id]
	if !c.open || !ok {
		return
	}
	c.msgs = append(c.msgs[:i], c.msgs[i+1:]...)
	if c.sel > i || c.sel >= len(c.msgs) {
		c.sel = max(c.sel-1, 0)
	}
	if c.bottom > i || c.bottom >= len(c.msgs) {
		c.bottom = max(c.bottom-1, 0)
	}
	if c.visual > i {
		c.visual--
	}
	if i < len(c.msgs) {
		c.msgs[i].rev++
	}
	c.reindex()
}

// markReadIfVisible marks the open chat read when the user can see it.
func (a *App) markReadIfVisible() tea.Cmd {
	c := a.conv
	if !c.open || !a.termFocused || len(c.msgs) == 0 || a.locked() {
		return nil
	}
	if a.layout.narrow && a.narrowPane == FocusChatList {
		return nil
	}
	if c.chat.Unread == 0 && !c.chat.MarkedUnread {
		return nil
	}
	upTo := c.msgs[c.last()].m.ID
	id := c.chat.ID
	c.chat.Unread = 0
	c.chat.MarkedUnread = false
	return a.do("mark read", func(ctx contextT) error { return a.svc.MarkRead(ctx, id, upTo) }, "")
}

// ---- navigation ------------------------------------------------------------------

// blockHeight returns the rendered height of message i at the current width.
func (a *App) blockHeight(i int) int {
	return len(a.messageBlock(i, a.conv.viewW).lines)
}

// ensureSelVisible scrolls minimally so the selected message is visible.
func (a *App) ensureSelVisible() {
	c := a.conv
	if len(c.msgs) == 0 || c.viewH <= 0 {
		c.bottom, c.skip = max(c.last(), 0), 0
		return
	}
	c.sel = clamp(c.sel, 0, c.last())
	H := c.viewH
	switch {
	case c.sel > c.bottom:
		c.bottom, c.skip = c.sel, 0
	case c.sel == c.bottom:
		if h := a.blockHeight(c.sel); h <= H {
			c.skip = 0
		} else {
			c.skip = h - H
		}
	default:
		// Distance from the top of sel to the viewport bottom.
		d := -c.skip
		for i := c.sel; i <= c.bottom && d <= H; i++ {
			d += a.blockHeight(i)
		}
		if d > H {
			a.anchorTop(c.sel)
		}
	}
}

// anchorTop scrolls so message i starts at the top of the viewport.
func (a *App) anchorTop(i int) {
	c := a.conv
	H := c.viewH
	acc := 0
	for j := i; j <= c.last(); j++ {
		acc += a.blockHeight(j)
		if acc >= H {
			c.bottom, c.skip = j, acc-H
			return
		}
	}
	c.bottom, c.skip = c.last(), 0
}

// centerSel scrolls so the selection is centered (zz).
func (a *App) centerSel() {
	c := a.conv
	if len(c.msgs) == 0 || c.viewH <= 0 {
		return
	}
	h := a.blockHeight(c.sel)
	below := (c.viewH - h) / 2
	if below <= 0 {
		a.anchorTop(c.sel)
		return
	}
	c.bottom, c.skip = c.sel, -below
	a.normalizeScroll()
}

// scrollLines scrolls the view by n lines (positive = towards older).
func (a *App) scrollLines(n int) tea.Cmd {
	c := a.conv
	if len(c.msgs) == 0 {
		return nil
	}
	c.skip += n
	a.normalizeScroll()
	c.follow = c.atBottom()
	return a.maybeLoadOlder()
}

// normalizeScroll keeps (bottom, skip) canonical and within the content.
func (a *App) normalizeScroll() {
	c := a.conv
	if len(c.msgs) == 0 {
		c.bottom, c.skip = 0, 0
		return
	}
	c.bottom = clamp(c.bottom, 0, c.last())
	for c.skip < 0 && c.bottom < c.last() {
		c.bottom++
		c.skip += a.blockHeight(c.bottom)
	}
	if c.skip < 0 {
		c.skip = 0
	}
	for c.bottom > 0 && c.skip >= a.blockHeight(c.bottom) {
		c.skip -= a.blockHeight(c.bottom)
		c.bottom--
	}
	// Don't scroll past the top: total lines from the oldest message to the
	// bottom anchor must be at least the viewport height.
	total := -c.skip
	for i := 0; i <= c.bottom && total < c.viewH; i++ {
		total += a.blockHeight(i)
	}
	if total < c.viewH {
		a.anchorTop(0)
	}
}

// topVisible returns the index of the topmost (partly) visible message.
func (a *App) topVisible() int {
	c := a.conv
	if len(c.msgs) == 0 {
		return 0
	}
	c.bottom = clamp(c.bottom, 0, c.last())
	acc := -c.skip
	i := c.bottom
	for ; i >= 0; i-- {
		acc += a.blockHeight(i)
		if acc >= c.viewH {
			return i
		}
	}
	return 0
}

// maybeLoadOlder loads history when the oldest message is on screen.
func (a *App) maybeLoadOlder() tea.Cmd {
	c := a.conv
	if c.hasMore && !c.loading && len(c.msgs) > 0 && a.topVisible() <= 2 {
		return a.loadOlder(false, nil)
	}
	return nil
}

func (a *App) convMove(delta int) tea.Cmd {
	c := a.conv
	if len(c.msgs) == 0 {
		return nil
	}
	c.sel = clamp(c.sel+delta, 0, c.last())
	a.ensureSelVisible()
	c.follow = c.sel == c.last()
	if c.sel <= 2 {
		return a.maybeLoadOlderForSel()
	}
	return nil
}

func (a *App) maybeLoadOlderForSel() tea.Cmd {
	if a.conv.hasMore && !a.conv.loading {
		return a.loadOlder(false, nil)
	}
	return nil
}

// convHalfPage moves the selection and view by half a page.
func (a *App) convPage(frac float64, dir int) tea.Cmd {
	c := a.conv
	if len(c.msgs) == 0 {
		return nil
	}
	lines := int(float64(max(c.viewH, 2)) * frac)
	cmd := a.scrollLines(dir * -lines)
	// Move the selection along to a message that is still visible.
	if dir > 0 { // down
		for c.sel < c.bottom && !a.isFullyVisible(c.sel) {
			c.sel++
		}
		if !a.isVisible(c.sel) {
			c.sel = c.bottom
		}
	} else {
		top := a.topVisible()
		if c.sel > c.bottom || !a.isVisible(c.sel) {
			c.sel = top
			if !a.isFullyVisible(c.sel) && c.sel < c.bottom {
				c.sel++
			}
		}
	}
	return cmd
}

// selIntoView moves the selection onto a visible message after the view
// was scrolled independently (mouse wheel), like vim keeps the cursor on
// screen.
func (a *App) selIntoView() {
	c := a.conv
	if len(c.msgs) == 0 || a.isVisible(c.sel) {
		return
	}
	if c.sel > c.bottom {
		c.sel = c.bottom
		if !a.isFullyVisible(c.sel) && c.sel > a.topVisible() {
			c.sel--
		}
		return
	}
	c.sel = a.topVisible()
	if !a.isFullyVisible(c.sel) && c.sel < c.bottom {
		c.sel++
	}
}

func (a *App) isVisible(i int) bool {
	c := a.conv
	return i <= c.bottom && i >= a.topVisible()
}

func (a *App) isFullyVisible(i int) bool {
	c := a.conv
	if i > c.bottom || i < a.topVisible() {
		return false
	}
	if i == c.bottom {
		return c.skip == 0
	}
	d := -c.skip
	for j := i; j <= c.bottom && d <= c.viewH; j++ {
		d += a.blockHeight(j)
	}
	return d <= c.viewH
}

func (a *App) convTop() tea.Cmd {
	c := a.conv
	a.recordJump()
	if c.hasMore {
		return tea.Batch(a.toast(ui.ToastInfo, "Loading history…"), a.loadOlder(true, nil))
	}
	c.sel = 0
	a.ensureSelVisible()
	return nil
}

func (a *App) convBottom() {
	c := a.conv
	c.sel = max(c.last(), 0)
	c.bottom, c.skip = c.sel, 0
	c.follow = true
}

// visualRange returns the selected index range in visual mode.
func (c *conversation) visualRange() (int, int, bool) {
	if c.visual < 0 {
		return 0, 0, false
	}
	lo, hi := c.visual, c.sel
	if lo > hi {
		lo, hi = hi, lo
	}
	return lo, hi, true
}

// ---- rendering --------------------------------------------------------------------

// renderMessages renders the message viewport: exactly h lines of w cells.
// x0,y0 is the screen position for mouse zones.
func (a *App) renderMessages(w, h, x0, y0 int) []string {
	c := a.conv
	if c.viewW != w {
		c.viewW = w
	}
	c.viewH = h
	if len(c.msgs) == 0 {
		lines := make([]string, h)
		msg := a.styles.Subtitle.Render("No messages yet. Press i to say hi " + "👋")
		if c.chat.MessageRequest {
			msg = a.styles.Subtitle.Render("Message request")
		}
		mid := h / 2
		for i := range lines {
			lines[i] = spaces(w)
		}
		if mid < h {
			lines[mid] = fit(centerText(msg, w), w)
		}
		return lines
	}
	a.normalizeScroll()
	// Collect blocks from the bottom anchor upwards.
	type placed struct {
		idx int
		b   *block
	}
	var stack []placed
	acc := -c.skip
	for i := c.bottom; i >= 0 && acc < h; i-- {
		b := a.messageBlock(i, w)
		stack = append(stack, placed{i, b})
		acc += len(b.lines)
	}
	lines := make([]string, 0, acc)
	var zs []zone
	for k := len(stack) - 1; k >= 0; k-- {
		p := stack[k]
		base := len(lines)
		lines = append(lines, p.b.lines...)
		// The message zone goes first so the links, attachments and
		// reactions inside it win (later zones are on top).
		zs = append(zs, zone{x: 0, y: base, w: w, h: len(p.b.lines), kind: zoneMessage, index: p.idx})
		for _, z := range p.b.zones {
			z.y += base
			zs = append(zs, z)
		}
	}
	// Cut skip lines from the bottom, then excess from the top.
	if c.skip > 0 && c.skip <= len(lines) {
		lines = lines[:len(lines)-c.skip]
	}
	cut := 0
	if len(lines) > h {
		cut = len(lines) - h
		lines = lines[cut:]
	}
	top := 0
	if len(lines) < h {
		// Content shorter than the viewport: stick to the bottom.
		top = h - len(lines)
	}
	out := make([]string, 0, h)
	for i := 0; i < top; i++ {
		out = append(out, spaces(w))
	}
	for _, l := range lines {
		out = append(out, fit(l, w))
	}
	// Register zones (clipped to the viewport). Message zones go first so
	// finer zones (links, attachments, reactions) registered later win.
	for _, z := range zs {
		z.y = z.y - cut + top
		if z.y+z.h <= 0 || z.y >= h {
			continue
		}
		if z.y < 0 {
			z.h += z.y
			z.y = 0
		}
		if z.y+z.h > h {
			z.h = h - z.y
		}
		z.x += x0
		z.y += y0
		a.zones.add(z)
	}
	return out
}

// messageBlock returns the (cached) render of message i at width w.
func (a *App) messageBlock(i, w int) *block {
	c := a.conv
	e := c.msgs[i]
	m := &e.m
	var prev *core.Message
	if i > 0 {
		prev = &c.msgs[i-1].m
	}
	var next *core.Message
	if i < c.last() {
		next = &c.msgs[i+1].m
	}
	var flags uint16
	loc := a.now().Location()
	if prev == nil || !format.SameDay(prev.SentAt, m.SentAt, loc) {
		flags |= flagDay
	}
	if !c.unreadFrom.IsZero() && c.unreadFrom == m.ID {
		flags |= flagUnread
	}
	if prev == nil || !sameRun(prev, m) || flags&(flagDay|flagUnread) != 0 {
		flags |= flagFirst
	}
	if next == nil || !sameRun(m, next) || (!c.unreadFrom.IsZero() && c.unreadFrom == next.ID) ||
		!format.SameDay(m.SentAt, next.SentAt, loc) {
		flags |= flagLast
	}
	focused := a.focus == FocusConversation || a.keys.Mode().IsVisual()
	if i == c.sel {
		flags |= flagSelected
		if focused {
			flags |= flagFocused
		}
	}
	if lo, hi, ok := c.visualRange(); ok && a.keys.Mode().IsVisual() && i >= lo && i <= hi {
		flags |= flagVisual
	}
	if c.revealed[m.ID] {
		flags |= flagRevealed
	}
	if a.cfg.Layout == "irc" {
		flags |= flagIRC
	}
	key := blockKey{id: m.ID, rev: e.rev, width: w, flags: flags, theme: c.themeGen}
	if a.search.highlight != "" {
		key.query = a.search.highlight
		if a.search.isHit(m.ID) {
			key.flags |= flagSearchHit
		}
	}
	if hasMedia(m) {
		key.media = c.mediaGen
	}
	if m.ExpiresIn > 0 || flags&flagDay != 0 {
		key.minute = a.now().Unix() / 60
	}
	if b, ok := c.cache[key]; ok {
		return b
	}
	if len(c.cache) > 4000 {
		c.cache = map[blockKey]*block{}
	}
	b := a.renderMessage(m, w, key.flags, key.query)
	c.cache[key] = b
	return b
}

// sameRun reports whether b continues a's run: same sender, same kind,
// within a few minutes.
func sameRun(a, b *core.Message) bool {
	if a.Kind != core.KindNormal || b.Kind != core.KindNormal {
		return false
	}
	if a.ID.Author != b.ID.Author || a.Outgoing != b.Outgoing {
		return false
	}
	d := b.SentAt.Sub(a.SentAt)
	return d >= 0 && d < 3*time.Minute
}

func hasMedia(m *core.Message) bool {
	if len(m.Attachments) > 0 || m.Sticker != nil {
		return true
	}
	if m.Preview != nil && m.Preview.Image != nil {
		return true
	}
	if m.Quote != nil && m.Quote.Attachment != nil {
		return true
	}
	return false
}

// selectedMessageAction returns the selected message for an action, or a toast.
func (a *App) requireSelected() (*core.Message, tea.Cmd) {
	m := a.conv.selected()
	if m == nil {
		return nil, a.toast(ui.ToastInfo, "No message selected")
	}
	return m, nil
}

// visualMessages returns the messages in the visual selection, oldest first.
func (a *App) visualMessages() []core.Message {
	c := a.conv
	lo, hi, ok := c.visualRange()
	if !ok {
		if m := c.selected(); m != nil {
			return []core.Message{*m}
		}
		return nil
	}
	out := make([]core.Message, 0, hi-lo+1)
	for i := lo; i <= hi && i < len(c.msgs); i++ {
		out = append(out, c.msgs[i].m)
	}
	return out
}

func (a *App) enterVisual(line bool) {
	c := a.conv
	if len(c.msgs) == 0 {
		return
	}
	if c.visual < 0 {
		c.visual = c.sel
	}
	if line {
		a.keys.SetMode(keymap.VisualLine)
	} else {
		a.keys.SetMode(keymap.Visual)
	}
	a.setFocus(FocusConversation)
}

func (a *App) exitVisual() {
	a.conv.visual = -1
	a.keys.SetMode(keymap.Normal)
}
