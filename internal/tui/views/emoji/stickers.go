package emoji

import (
	"context"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"warte/internal/core"
	"warte/internal/tui/keymap"
	"warte/internal/tui/ui"
)

// Sticker cell geometry: images are stickerW x stickerH cells, each grid
// cell adds a one-cell margin on both sides and a label row.
const (
	stickerW     = 6
	stickerH     = 3
	stickerCellW = stickerW + 2
	stickerCellH = stickerH + 1
)

// StickerOptions configure the sticker picker.
type StickerOptions struct {
	// Chat receives the sticker; "" uses Context.Chat().
	Chat core.ChatID
	// Recents stores recently sent stickers; nil uses Session.
	Recents *Recents
	// OnSend sends the sticker. Nil calls Service.Send with
	// core.Outgoing{Sticker} and toasts on error.
	OnSend func(chat core.ChatID, s core.Sticker) tea.Cmd
}

// Stickers is the sticker picker overlay: installed packs first as tabs
// (plus "recent" when stickers were sent this session), a grid of sticker
// images, enter sends, "i" installs or removes the current pack.
type Stickers struct {
	ctx  ui.Context
	opts StickerOptions
	rec  *Recents

	loaded  bool
	err     error
	packs   []core.StickerPack
	recents []core.Sticker // snapshot shown in the recent tab
	tab     int
	sel     int
	off     int
	busy    bool

	cols, rows, gridTop int
	spans               []tabSpan
}

type packsLoadedMsg struct {
	packs []core.StickerPack
	err   error
	keep  string // pack id to keep selected
}

type installDoneMsg struct {
	packID, title string
	install       bool
	err           error
}

// NewStickers returns the sticker picker overlay.
func NewStickers(ctx ui.Context, opts StickerOptions) *Stickers {
	s := &Stickers{ctx: ctx, opts: opts, rec: opts.Recents, cols: 6, rows: 2}
	if s.rec == nil {
		s.rec = Session
	}
	s.recents = s.rec.Stickers()
	return s
}

// Init implements ui.Overlay.
func (s *Stickers) Init() tea.Cmd { return s.load("") }

func (s *Stickers) load(keep string) tea.Cmd {
	svc := s.ctx.Svc
	if svc == nil {
		return nil
	}
	return func() tea.Msg {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		packs, err := svc.StickerPacks(c)
		return packsLoadedMsg{packs: packs, err: err, keep: keep}
	}
}

func (s *Stickers) hasRecent() bool { return len(s.recents) > 0 }

func (s *Stickers) tabCount() int {
	n := len(s.packs)
	if s.hasRecent() {
		n++
	}
	return n
}

// pack returns the pack of the current tab, or nil for the recent tab.
func (s *Stickers) pack() *core.StickerPack {
	i := s.tab
	if s.hasRecent() {
		i--
	}
	if i < 0 || i >= len(s.packs) {
		return nil
	}
	return &s.packs[i]
}

// items returns the stickers of the current tab.
func (s *Stickers) items() []core.Sticker {
	if s.hasRecent() && s.tab == 0 {
		return s.recents
	}
	if p := s.pack(); p != nil {
		return p.Stickers
	}
	return nil
}

// SelectedSticker returns the sticker under the cursor.
func (s *Stickers) SelectedSticker() (core.Sticker, bool) {
	it := s.items()
	if s.sel < 0 || s.sel >= len(it) {
		return core.Sticker{}, false
	}
	return it[s.sel], true
}

// Title implements ui.Overlay.
func (s *Stickers) Title() string { return "Stickers" }

// KeyContext implements ui.Overlay.
func (s *Stickers) KeyContext() string { return KeyContextStickers }

// Size implements ui.Overlay.
func (s *Stickers) Size(maxW, maxH int) (int, int) {
	return min(maxW, stickerCellW*7), min(maxH, 3+stickerCellH*3)
}

// Footer implements ui.Footer.
func (s *Stickers) Footer() string {
	verb := "install"
	if p := s.pack(); p != nil && p.Installed {
		verb = "remove"
	}
	return s.ctx.Styles.RenderKeyHints("enter", "send", "tab", "pack", "i", verb, "esc", "close")
}

func (s *Stickers) setTab(t int) {
	n := s.tabCount()
	if n == 0 {
		s.tab = 0
		return
	}
	s.tab = ((t % n) + n) % n
	s.sel, s.off = 0, 0
}

func (s *Stickers) move(d int) {
	n := len(s.items())
	if n == 0 {
		s.sel = 0
		return
	}
	s.sel = max(0, min(s.sel+d, n-1))
}

func (s *Stickers) moveRows(n int) {
	c := max(s.cols, 1)
	if n < 0 && s.sel+n*c < 0 {
		s.sel %= c
		return
	}
	s.move(n * c)
}

func sortPacks(packs []core.StickerPack) {
	sort.SliceStable(packs, func(i, j int) bool { return packs[i].Installed && !packs[j].Installed })
}

// Update implements ui.Overlay.
func (s *Stickers) Update(msg tea.Msg) (ui.Overlay, tea.Cmd) {
	switch msg := msg.(type) {
	case packsLoadedMsg:
		s.loaded, s.busy = true, false
		if msg.err != nil {
			s.err = msg.err
			return s, nil
		}
		s.err = nil
		// Copy before sorting: the Service may hand out its own slice.
		s.packs = append([]core.StickerPack(nil), msg.packs...)
		sortPacks(s.packs)
		if msg.keep != "" {
			for i, p := range s.packs {
				if p.ID == msg.keep {
					s.tab = i
					if s.hasRecent() {
						s.tab++
					}
				}
			}
		} else if s.hasRecent() {
			s.tab = 0
		}
		s.tab = min(s.tab, max(s.tabCount()-1, 0))
		s.sel = min(s.sel, max(len(s.items())-1, 0))
	case installDoneMsg:
		s.busy = false
		if msg.err != nil {
			return s, nil
		}
		return s, s.load(msg.packID)
	case tea.KeyPressMsg:
		switch msg.String() {
		case "i":
			return s, s.toggleInstall()
		case "tab":
			s.setTab(s.tab + 1)
		case "shift+tab":
			s.setTab(s.tab - 1)
		}
	case ui.ActionMsg:
		n := max(msg.Count, 1)
		switch msg.Action {
		case keymap.ActCursorRight:
			s.move(n)
		case keymap.ActCursorLeft:
			s.move(-n)
		case keymap.ActCursorDown:
			s.moveRows(n)
		case keymap.ActCursorUp:
			s.moveRows(-n)
		case keymap.ActCursorTop:
			s.sel = 0
		case keymap.ActCursorBottom:
			s.sel = max(len(s.items())-1, 0)
		case keymap.ActHalfPageDown, keymap.ActPageDown:
			s.moveRows(max(s.rows, 1))
		case keymap.ActHalfPageUp, keymap.ActPageUp:
			s.moveRows(-max(s.rows, 1))
		case keymap.ActFocusNext, keymap.ActFocusRight, keymap.ActFolderNext:
			s.setTab(s.tab + n)
		case keymap.ActFocusPrev, keymap.ActFocusLeft, keymap.ActFolderPrev:
			s.setTab(s.tab - n)
		case keymap.ActItemSelect:
			return s.send()
		}
	case tea.MouseClickMsg:
		return s.click(msg.X, msg.Y)
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			s.moveRows(-1)
		case tea.MouseWheelDown:
			s.moveRows(1)
		}
	}
	return s, nil
}

func (s *Stickers) click(x, y int) (ui.Overlay, tea.Cmd) {
	if y == 0 {
		for _, sp := range s.spans {
			if x >= sp.x && x < sp.x+sp.w {
				s.setTab(sp.index)
			}
		}
		return s, nil
	}
	if y < s.gridTop || (y-s.gridTop)/stickerCellH >= s.rows {
		return s, nil // above the grid, or in padding below the visible rows
	}
	row := (y-s.gridTop)/stickerCellH + s.off
	col := x / stickerCellW
	if col >= s.cols {
		return s, nil
	}
	i := row*s.cols + col
	if i < 0 || i >= len(s.items()) {
		return s, nil
	}
	s.sel = i
	return s.send()
}

func (s *Stickers) toggleInstall() tea.Cmd {
	p := s.pack()
	if p == nil || s.busy || s.ctx.Svc == nil {
		return nil
	}
	s.busy = true
	svc := s.ctx.Svc
	id, key, title, install := p.ID, p.Key, p.Title, !p.Installed
	return func() tea.Msg {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := svc.InstallStickerPack(c, id, key, install)
		done := installDoneMsg{packID: id, title: title, install: install, err: err}
		// The toast goes to the App, so it shows even when the picker was
		// closed meanwhile; done reaches the picker only while it is on top.
		toast := ui.Toast(ui.ToastSuccess, "Installed "+clean(title))
		switch {
		case err != nil:
			toast = ui.Toast(ui.ToastError, "Sticker pack: "+clean(err.Error()))
		case !install:
			toast = ui.Toast(ui.ToastSuccess, "Removed "+clean(title))
		}
		return tea.BatchMsg{toast, func() tea.Msg { return done }}
	}
}

func (s *Stickers) chat() core.ChatID {
	if s.opts.Chat != "" {
		return s.opts.Chat
	}
	if s.ctx.Chat != nil {
		return s.ctx.Chat()
	}
	return ""
}

func (s *Stickers) send() (ui.Overlay, tea.Cmd) {
	st, ok := s.SelectedSticker()
	if !ok {
		return s, nil
	}
	chat := s.chat()
	if chat == "" {
		return s, ui.Toast(ui.ToastWarning, "Open a chat to send stickers")
	}
	s.rec.AddSticker(st)
	if s.opts.OnSend != nil {
		return nil, s.opts.OnSend(chat, st)
	}
	svc := s.ctx.Svc
	if svc == nil {
		return nil, nil
	}
	return nil, func() tea.Msg {
		c, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		sticker := st
		if _, err := svc.Send(c, chat, core.Outgoing{Sticker: &sticker}); err != nil {
			return ui.ToastMsg{Level: ui.ToastError, Text: "Sticker not sent: " + clean(err.Error())}
		}
		return nil
	}
}

func (s *Stickers) tabLabels() []string {
	var labels []string
	if s.hasRecent() {
		labels = append(labels, "🕘 Recent")
	}
	for _, p := range s.packs {
		icon := ""
		if p.Cover != nil && p.Cover.Emoji != "" {
			icon = clean(p.Cover.Emoji) + " "
		} else if len(p.Stickers) > 0 && p.Stickers[0].Emoji != "" {
			icon = clean(p.Stickers[0].Emoji) + " "
		}
		title := fitEllipsis(clean(p.Title), min(width(clean(p.Title)), 18))
		if !p.Installed {
			title += " +"
		}
		labels = append(labels, icon+strings.TrimRight(title, " "))
	}
	return labels
}

// View implements ui.Overlay.
func (s *Stickers) View(w, h int) string {
	st := s.ctx.Styles
	if w <= 0 || h <= 0 {
		return ""
	}
	var lines []string
	switch {
	case s.err != nil:
		return padLines([]string{"", st.ErrorText.Render(fitEllipsis(" Couldn't load sticker packs: "+clean(s.err.Error()), w))}, w, h)
	case !s.loaded:
		return padLines([]string{"", muted(st).Render(center("Loading sticker packs…", w))}, w, h)
	case s.tabCount() == 0:
		return padLines([]string{"", muted(st).Render(center("No sticker packs", w))}, w, h)
	}
	bar, rule, spans := renderTabs(st, s.tabLabels(), s.tab, w)
	s.spans = spans
	lines = append(lines, bar)
	if h >= 7 {
		lines = append(lines, rule)
	}
	if h >= 3 {
		lines = append(lines, s.infoLine(w))
	}
	s.gridTop = len(lines)
	items := s.items()
	s.cols = max(w/stickerCellW, 1)
	s.rows = max((h-len(lines))/stickerCellH, 1)
	total := (len(items) + s.cols - 1) / s.cols
	s.off = scrollTo(s.off, s.sel/s.cols, s.rows, total)
	if len(items) == 0 {
		lines = append(lines, "", muted(st).Render(center("This pack is empty", w)))
		return padLines(lines, w, h)
	}
	for r := s.off; r < total && r < s.off+s.rows && len(lines) < h; r++ {
		cells := make([][]string, 0, s.cols)
		for c := 0; c < s.cols; c++ {
			i := r*s.cols + c
			if i >= len(items) {
				break
			}
			cells = append(cells, s.cell(items[i], i == s.sel))
		}
		for k := 0; k < stickerCellH; k++ {
			var b strings.Builder
			for _, cl := range cells {
				b.WriteString(cl[k])
			}
			lines = append(lines, b.String())
		}
	}
	return padLines(lines, w, h)
}

func (s *Stickers) infoLine(w int) string {
	st := s.ctx.Styles
	p := s.pack()
	if p == nil {
		return joinRight(accent(st).Render("Recently sent"), muted(st).Render(plural(len(s.recents), "sticker", "stickers")), w)
	}
	left := accent(st).Render(clean(p.Title))
	if p.Author != "" {
		left += muted(st).Render(" by " + clean(p.Author))
	}
	var right string
	switch {
	case s.busy:
		right = muted(st).Render("working…")
	case p.Installed:
		right = st.SuccessText.Render(s.ctx.Icons.Check + " installed")
	default:
		right = st.WarningText.Render("not installed")
	}
	if width(left)+width(right)+1 > w {
		left = fitEllipsis(left, max(w-width(right)-1, 1))
	}
	return joinRight(left, right, w)
}

// cell renders one sticker as stickerCellH lines of stickerCellW cells.
func (s *Stickers) cell(sk core.Sticker, selected bool) []string {
	st := s.ctx.Styles
	img := s.image(sk)
	l, r := " ", " "
	if selected {
		l, r = accent(st).Render("▐"), accent(st).Render("▌")
	}
	out := make([]string, 0, stickerCellH)
	for _, line := range img {
		out = append(out, l+line+r)
	}
	label := center(clean(sk.Emoji), stickerW)
	if selected {
		label = " " + selStyle(st).Render(label) + " "
	} else {
		label = " " + subtle(st).Render(label) + " "
	}
	return append(out, label)
}

// image returns stickerH lines of exactly stickerW cells.
func (s *Stickers) image(sk core.Sticker) []string {
	imgs := s.ctx.Images
	if imgs != nil && imgs.Enabled() && imgs.Inline() {
		ref := ui.ImageRef{
			Kind:       ui.ImageSticker,
			PackID:     sk.PackID,
			StickerID:  sk.StickerID,
			Attachment: sk.Attachment,
			Label:      sk.Emoji,
		}
		c, r := imgs.Size(ref, stickerW, stickerH)
		c, r = max(min(c, stickerW), 1), max(min(r, stickerH), 1)
		rows, _ := imgs.Rows(ref, c, r)
		out := make([]string, 0, stickerH)
		top := (stickerH - len(rows)) / 2
		for range top {
			out = append(out, strings.Repeat(" ", stickerW))
		}
		left := (stickerW - c) / 2
		for _, row := range rows {
			if len(out) >= stickerH {
				break
			}
			out = append(out, strings.Repeat(" ", left)+row+strings.Repeat(" ", max(stickerW-c-left, 0)))
		}
		for len(out) < stickerH {
			out = append(out, strings.Repeat(" ", stickerW))
		}
		return out
	}
	// No inline images: a soft tile with the sticker's emoji.
	box := lipgloss.NewStyle()
	if !s.ctx.Styles.Theme.Mono {
		box = box.Background(s.ctx.Styles.Theme.Palette.BgAlt)
	}
	blank := box.Render(strings.Repeat(" ", stickerW))
	out := []string{blank, box.Render(center(clean(sk.Emoji), stickerW)), blank}
	return out[:stickerH]
}
