package emoji

import (
	"context"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"

	"warte/internal/core"
	"warte/internal/format"
	"warte/internal/tui/keymap"
	"warte/internal/tui/ui"
)

// Key contexts (keymap.Overlay(name)) of the views in this package.
const (
	KeyContextPicker    = "emoji"
	KeyContextReactions = "reactions"
	KeyContextStickers  = "stickers"
)

// Purposes of an emoji pick.
const (
	PurposeReact  = "react"  // react to Target via Service.React
	PurposeInsert = "insert" // insert into the composer (ui.ComposeInsertMsg)
)

// PickedMsg is the result of the emoji picker: the chosen emoji (with the
// skin tone applied), why it was picked and, for reactions, the target
// message. The picker hands it to Options.OnPick (default: Handle).
type PickedMsg struct {
	Emoji   string
	Purpose string
	Target  core.MessageID
}

// Options configure the emoji picker.
type Options struct {
	// Purpose is PurposeReact or PurposeInsert (any other value is passed
	// through to OnPick unchanged).
	Purpose string
	// Target is the message to react to (PurposeReact).
	Target core.MessageID
	// OnPick turns the pick into a command. Nil uses Handle. Return
	// func() tea.Msg { return m } to receive PickedMsg yourself.
	OnPick func(m PickedMsg) tea.Cmd
	// Recents stores recent emojis and the skin tone; nil uses Session.
	Recents *Recents
	// Query pre-fills the search (e.g. a partial ":shortcode").
	Query string
}

// Handle performs the default action for a pick: for PurposeReact it calls
// Service.React on the target (removing the reaction when the user already
// reacted with the same emoji, like Signal) and shows an error toast on
// failure; for PurposeInsert it returns ui.ComposeInsertMsg. Other purposes
// yield PickedMsg itself.
func Handle(ctx ui.Context, m PickedMsg) tea.Cmd {
	switch m.Purpose {
	case PurposeReact:
		if m.Target.IsZero() {
			return ui.Toast(ui.ToastWarning, "No message selected")
		}
		svc, self := ctx.Svc, ctx.Self.ACI
		if svc == nil {
			return nil
		}
		return func() tea.Msg {
			c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			remove := false
			if msg, err := svc.Message(c, m.Target); err == nil {
				for _, r := range msg.Reactions {
					if r.From == self && r.Emoji == m.Emoji {
						remove = true
					}
				}
			}
			if err := svc.React(c, m.Target, m.Emoji, remove); err != nil {
				return ui.ToastMsg{Level: ui.ToastError, Text: "Reaction failed: " + clean(err.Error())}
			}
			return nil
		}
	case PurposeInsert:
		return func() tea.Msg { return ui.ComposeInsertMsg{Text: m.Emoji} }
	}
	return func() tea.Msg { return m }
}

// item is one cell of the emoji grid.
type item struct {
	char string // as shown and picked (tone applied)
	base string // without the session tone, for lookups
}

// Picker is the full emoji picker overlay: category tabs, a grid adapting
// to the width, live search by name/shortcode, recents and skin tones.
type Picker struct {
	ctx  ui.Context
	opts Options
	rec  *Recents

	tab    int // 0 = recents, 1.. = format.Categories()
	items  []item
	sel    int
	off    int // first visible grid row
	query  []rune
	typing bool // search field focused (keys captured)
	search bool // results of query shown instead of a tab

	// layout of the last View, for paging and mouse hit tests
	cols, rows, gridTop int
	spans               []tabSpan
}

const cellW = 4 // " 😀 "

// NewPicker returns the emoji picker overlay.
func NewPicker(ctx ui.Context, opts Options) *Picker {
	p := &Picker{ctx: ctx, opts: opts, rec: opts.Recents, cols: 8, rows: 6}
	if p.rec == nil {
		p.rec = Session
	}
	if q := strings.TrimSpace(opts.Query); q != "" {
		p.query = []rune(strings.Trim(q, ":"))
		p.search = true
		p.typing = true
	}
	p.reload()
	return p
}

// NewReactionPicker returns a picker that reacts to target on enter.
func NewReactionPicker(ctx ui.Context, target core.MessageID) *Picker {
	return NewPicker(ctx, Options{Purpose: PurposeReact, Target: target})
}

// NewInsertPicker returns a picker that inserts the emoji into the composer.
func NewInsertPicker(ctx ui.Context) *Picker {
	return NewPicker(ctx, Options{Purpose: PurposeInsert})
}

func (p *Picker) tabCount() int { return 1 + len(format.Categories()) }

func (p *Picker) tone(char string) string {
	return format.WithSkinTone(char, p.rec.SkinTone())
}

// reload rebuilds the item list for the current tab or query.
func (p *Picker) reload() {
	p.items = p.items[:0]
	switch {
	case p.search:
		q := strings.TrimSpace(strings.Trim(string(p.query), ":"))
		if q == "" {
			break
		}
		for _, e := range format.SearchEmoji(q, 0) {
			p.items = append(p.items, item{char: p.tone(e.Char), base: e.Char})
		}
	case p.tab == 0:
		seen := map[string]bool{}
		for _, e := range p.rec.Emojis() {
			seen[e] = true
			p.items = append(p.items, item{char: e, base: e})
		}
		for _, e := range format.DefaultReactions() {
			t := p.tone(e)
			if !seen[t] && !seen[e] {
				seen[t] = true
				p.items = append(p.items, item{char: t, base: e})
			}
		}
	default:
		for _, e := range format.EmojisInCategory(format.Category(p.tab - 1)) {
			p.items = append(p.items, item{char: p.tone(e.Char), base: e.Char})
		}
	}
	p.sel = min(p.sel, max(len(p.items)-1, 0))
}

// Selected returns the emoji under the cursor ("" when the grid is empty).
func (p *Picker) Selected() string {
	if p.sel < 0 || p.sel >= len(p.items) {
		return ""
	}
	return p.items[p.sel].char
}

// Query returns the current search text.
func (p *Picker) Query() string { return string(p.query) }

// Init implements ui.Overlay.
func (p *Picker) Init() tea.Cmd { return nil }

// Title implements ui.Overlay.
func (p *Picker) Title() string {
	switch p.opts.Purpose {
	case PurposeReact:
		return "React"
	case PurposeInsert:
		return "Insert emoji"
	}
	return "Emoji"
}

// KeyContext implements ui.Overlay.
func (p *Picker) KeyContext() string { return KeyContextPicker }

// Size implements ui.Overlay.
func (p *Picker) Size(maxW, maxH int) (int, int) {
	return min(maxW, 60), min(maxH, 16)
}

// CaptureKeys implements ui.KeyCapturer: keys are raw while typing a search.
func (p *Picker) CaptureKeys() bool { return p.typing }

// HandleBack implements ui.BackHandler: q/esc first clears the search.
func (p *Picker) HandleBack() (bool, tea.Cmd) {
	if p.search {
		p.clearSearch()
		return true, nil
	}
	return false, nil
}

// Footer implements ui.Footer.
func (p *Picker) Footer() string {
	st := p.ctx.Styles
	if p.typing {
		return st.RenderKeyHints("enter", "pick", "tab", "browse", "esc", "done")
	}
	verb := "pick"
	if p.opts.Purpose == PurposeReact {
		verb = "react"
	}
	return st.RenderKeyHints("enter", verb, "/", "search", "tab", "category", "t", "tone")
}

func (p *Picker) clearSearch() {
	p.search, p.typing, p.query = false, false, nil
	p.sel, p.off = 0, 0
	p.reload()
}

func (p *Picker) setTab(t int) {
	n := p.tabCount()
	p.tab = ((t % n) + n) % n
	p.search, p.typing, p.query = false, false, nil
	p.sel, p.off = 0, 0
	p.reload()
}

func (p *Picker) cycleTone(dir int) {
	p.rec.SetSkinTone(NextSkinTone(p.rec.SkinTone(), dir))
	sel := p.sel
	p.reload()
	p.sel = min(sel, max(len(p.items)-1, 0))
}

func (p *Picker) move(d int) {
	if len(p.items) == 0 {
		p.sel = 0
		return
	}
	p.sel = max(0, min(p.sel+d, len(p.items)-1))
}

func (p *Picker) moveRows(n int) {
	c := max(p.cols, 1)
	if len(p.items) == 0 {
		return
	}
	if n < 0 && p.sel+n*c < 0 {
		p.sel %= c // stay in the column, top row
		return
	}
	p.move(n * c)
}

func (p *Picker) pick() (ui.Overlay, tea.Cmd) {
	e := p.Selected()
	if e == "" {
		return p, nil
	}
	p.rec.AddEmoji(e)
	m := PickedMsg{Emoji: e, Purpose: p.opts.Purpose, Target: p.opts.Target}
	if p.opts.OnPick != nil {
		return nil, p.opts.OnPick(m)
	}
	return nil, Handle(p.ctx, m)
}

// Update implements ui.Overlay.
func (p *Picker) Update(msg tea.Msg) (ui.Overlay, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if p.typing {
			return p.typeKey(msg)
		}
		switch msg.String() {
		case "t":
			p.cycleTone(1)
		case "T":
			p.cycleTone(-1)
		case "/":
			p.startSearch()
		case "tab":
			p.setTab(p.tab + 1)
		case "shift+tab":
			p.setTab(p.tab - 1)
		}
	case ui.ActionMsg:
		n := max(msg.Count, 1)
		switch msg.Action {
		case keymap.ActCursorRight:
			p.move(n)
		case keymap.ActCursorLeft:
			p.move(-n)
		case keymap.ActCursorDown:
			p.moveRows(n)
		case keymap.ActCursorUp:
			p.moveRows(-n)
		case keymap.ActCursorTop:
			p.sel = 0
		case keymap.ActCursorBottom:
			p.sel = max(len(p.items)-1, 0)
		case keymap.ActHalfPageDown:
			p.moveRows(max(p.rows/2, 1))
		case keymap.ActHalfPageUp:
			p.moveRows(-max(p.rows/2, 1))
		case keymap.ActPageDown:
			p.moveRows(max(p.rows, 1))
		case keymap.ActPageUp:
			p.moveRows(-max(p.rows, 1))
		case keymap.ActFocusNext, keymap.ActFocusRight, keymap.ActFolderNext:
			p.setTab(p.tab + n)
		case keymap.ActFocusPrev, keymap.ActFocusLeft, keymap.ActFolderPrev:
			p.setTab(p.tab - n)
		case keymap.ActSearchForward, keymap.ActChatFilter:
			p.startSearch()
		case keymap.ActItemSelect:
			return p.pick()
		}
	case tea.MouseClickMsg:
		return p.click(msg.X, msg.Y)
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			p.moveRows(-1)
		case tea.MouseWheelDown:
			p.moveRows(1)
		}
	}
	return p, nil
}

func (p *Picker) startSearch() {
	p.typing = true
	if !p.search {
		p.search = true
		p.query = nil
		p.sel, p.off = 0, 0
		p.reload()
	}
}

func (p *Picker) typeKey(msg tea.KeyPressMsg) (ui.Overlay, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return nil, nil
	case "esc":
		p.typing = false
		if len(p.query) == 0 {
			p.clearSearch()
		}
		return p, nil
	case "enter":
		return p.pick()
	case "tab":
		p.typing = false
		if len(p.query) == 0 {
			p.clearSearch()
		}
		return p, nil
	case "up", "ctrl+p", "ctrl+k":
		p.moveRows(-1)
		return p, nil
	case "down", "ctrl+n", "ctrl+j":
		p.moveRows(1)
		return p, nil
	case "left":
		p.move(-1)
		return p, nil
	case "right":
		p.move(1)
		return p, nil
	case "backspace", "ctrl+h":
		if len(p.query) > 0 {
			p.query = p.query[:len(p.query)-1]
		}
	case "ctrl+u":
		p.query = nil
	case "ctrl+w":
		q := strings.TrimRight(string(p.query), " ")
		if i := strings.LastIndexAny(q, " _"); i >= 0 {
			p.query = []rune(q[:i])
		} else {
			p.query = nil
		}
	default:
		if msg.Text == "" || msg.Mod&(tea.ModCtrl|tea.ModAlt) != 0 {
			return p, nil
		}
		for _, r := range msg.Text {
			if unicode.IsPrint(r) && len(p.query) < 64 {
				p.query = append(p.query, r)
			}
		}
	}
	p.sel, p.off = 0, 0
	p.reload()
	return p, nil
}

func (p *Picker) click(x, y int) (ui.Overlay, tea.Cmd) {
	if y == 0 {
		for _, s := range p.spans {
			if x >= s.x && x < s.x+s.w {
				p.setTab(s.index)
			}
		}
		return p, nil
	}
	if y < p.gridTop || y >= p.gridTop+p.rows {
		return p, nil
	}
	col := x / cellW
	if col >= p.cols {
		return p, nil
	}
	i := (p.off+y-p.gridTop)*p.cols + col
	if i < 0 || i >= len(p.items) {
		return p, nil
	}
	p.sel = i
	return p.pick()
}

func (p *Picker) tabLabels() []string {
	labels := []string{"🕘"}
	for _, c := range format.Categories() {
		labels = append(labels, c.Icon())
	}
	return labels
}

func (p *Picker) tabName() string {
	if p.tab == 0 {
		if len(p.rec.Emojis()) == 0 {
			return "Frequently used"
		}
		return "Recently used"
	}
	return format.Category(p.tab - 1).String()
}

// View implements ui.Overlay.
func (p *Picker) View(w, h int) string {
	st := p.ctx.Styles
	if w <= 0 || h <= 0 {
		return ""
	}
	var lines []string
	sel := p.tab
	if p.search {
		sel = -1
	}
	bar, rule, spans := renderTabs(st, p.tabLabels(), sel, w)
	p.spans = spans
	lines = append(lines, bar)
	showRule := h >= 6
	showDetail := h >= 5
	if showRule {
		lines = append(lines, rule)
	}
	if h >= 3 {
		lines = append(lines, p.searchLine(w))
	}
	p.gridTop = len(lines)
	gh := h - len(lines)
	if showDetail {
		gh--
	}
	gh = max(gh, 0)
	p.cols = max(w/cellW, 1)
	p.rows = gh
	total := (len(p.items) + p.cols - 1) / p.cols
	p.off = scrollTo(p.off, p.sel/p.cols, gh, total)

	grid := make([]string, 0, gh)
	if len(p.items) == 0 && gh > 0 {
		msg := "No emoji found"
		if p.search && strings.TrimSpace(string(p.query)) == "" {
			msg = "Type to search by name or :shortcode:"
		}
		grid = append(grid, "", muted(st).Render(center(msg, w)))
	}
	hl := selStyle(st)
	for r := p.off; r < total && len(grid) < gh; r++ {
		var b strings.Builder
		for c := 0; c < p.cols; c++ {
			i := r*p.cols + c
			if i >= len(p.items) {
				break
			}
			cell := center(clean(p.items[i].char), cellW)
			if i == p.sel {
				cell = hl.Render(cell)
			}
			b.WriteString(cell)
		}
		grid = append(grid, b.String())
	}
	for len(grid) < gh {
		grid = append(grid, "")
	}
	if len(grid) > gh {
		grid = grid[:gh]
	}
	// Scrollbar in the last column when the grid overflows and there's room.
	if total > gh && gh > 0 && w-p.cols*cellW >= 1 {
		sb := strings.Split(st.RenderScrollbar(gh, total, gh, p.off), "\n")
		for i := range grid {
			if i < len(sb) {
				grid[i] = fit(grid[i], w-1) + sb[i]
			}
		}
	}
	lines = append(lines, grid...)
	if showDetail {
		lines = append(lines, p.detailLine(w))
	}
	return padLines(lines, w, h)
}

func (p *Picker) searchLine(w int) string {
	st := p.ctx.Styles
	toneHint := muted(st).Render("tone ") + format.WithSkinTone("✋", p.rec.SkinTone())
	if p.search {
		q := clean(string(p.query))
		left := accent(st).Render(p.ctx.Icons.Search+" ") + q
		if p.typing {
			left += accent(st).Render("▏")
		} else if q == "" {
			left += subtle(st).Render("search")
		}
		right := muted(st).Render(plural(len(p.items), "match", "matches"))
		if len(p.query) == 0 {
			right = toneHint
		}
		return joinRight(left, right, w)
	}
	left := accent(st).Render(p.tabName())
	return joinRight(left, toneHint, w)
}

func (p *Picker) detailLine(w int) string {
	st := p.ctx.Styles
	e := p.Selected()
	if e == "" {
		return ""
	}
	info, ok := format.LookupEmoji(p.items[p.sel].base)
	if !ok {
		info, ok = format.LookupEmoji(e)
	}
	left := clean(e) + " "
	if ok {
		left += info.Name
		if len(info.Shortcodes) > 0 {
			left += " " + muted(st).Render(":"+info.Shortcodes[0]+":")
		}
	}
	right := ""
	if n := p.rec.Count(e); n > 0 {
		right = subtle(st).Render("used " + itoa(n) + "×")
	}
	return joinRight(fitEllipsis(left, w), right, w)
}
