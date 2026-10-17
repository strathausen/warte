package finder

import (
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/sahilm/fuzzy"

	"warte/internal/tui/theme"
	"warte/internal/tui/ui"
)

// Row describes one picker row to a PickerOptions.Render function.
type Row struct {
	// Width is the number of cells available to the rendered row (gutter
	// and check box already removed).
	Width int
	// Selected is true for the row under the cursor.
	Selected bool
	// Checked is true when the item is marked in a multi-select picker.
	Checked bool
	// Matches holds the byte offsets of fuzzy-matched runes in the item's
	// Filter text (nil when the query is empty).
	Matches []int
	// Styles is the live style set.
	Styles *theme.Styles
	// Icons is the active icon set.
	Icons theme.IconSet
}

// Base returns the row's text style (with the selection background on the
// selected row).
func (r Row) Base() lipgloss.Style { return rowStyles(r.Styles, r.Selected) }

// Muted returns secondary text in the row's background.
func (r Row) Muted() lipgloss.Style { return on(r.Styles.ChatListPreview, r.Base()) }

// On returns s with the row's background applied.
func (r Row) On(s lipgloss.Style) lipgloss.Style { return on(s, r.Base()) }

// Highlight renders text (sanitized here) with the row's fuzzy matches
// highlighted. Pass the same string the picker filtered on, so the match
// offsets line up (the picker matches the sanitized text, so the offsets
// refer to it).
func (r Row) Highlight(text string) string {
	base := r.Base()
	return highlight(theme.SanitizeLine(text), r.Matches, base, on(r.Styles.FuzzyMatch, base))
}

// Spread lays out a left and right part across the row width.
func (r Row) Spread(left, right string) string {
	return spread(left, right, r.Width, r.Base())
}

// PickerOptions configures a Picker.
type PickerOptions[T any] struct {
	// Title is shown in the dialog border (sanitized).
	Title string
	// KeyContext is the overlay's keymap context name (default "finder.picker").
	KeyContext string
	// Prompt precedes the query (default the search icon).
	Prompt string
	// Placeholder is shown while the query is empty.
	Placeholder string
	// Items are the candidates; Load (optional) replaces them asynchronously
	// when the overlay is pushed.
	Items []T
	Load  func() ([]T, error)
	// Filter returns the text the fuzzy matcher sees for an item. Required.
	Filter func(T) string
	// Render draws one row in r.Width cells. Nil renders Filter(item) with
	// the matches highlighted.
	Render func(item T, r Row) string
	// Key identifies items across reloads (keeps marks and the cursor);
	// defaults to Filter.
	Key func(T) string
	// Multi enables multi-select: space (normal mode) / tab (insert mode)
	// toggles, enter returns every marked item (or the current one when
	// nothing is marked).
	Multi bool
	// Initial is the index (into Items) highlighted first.
	Initial int
	// Checked pre-marks items in a multi-select picker.
	Checked func(T) bool
	// OnSelect runs when the user accepts; the picker closes and the
	// returned command runs. Nil just closes.
	OnSelect func(selected []T) tea.Cmd
	// OnHighlight runs whenever the item under the cursor changes.
	OnHighlight func(item T) tea.Cmd
	// OnCancel runs when the picker is closed without accepting.
	OnCancel func() tea.Cmd
	// Preview renders a preview of the current item in w x h cells; it is
	// shown on the right on wide dialogs and below the list otherwise
	// (ctrl+o / p toggles it).
	Preview func(item T, w, h int) string
	// PreviewWidth is the preferred preview width (default 40).
	PreviewWidth int
	// PreviewHeight is the preferred preview height when it is shown below
	// the list (default 8).
	PreviewHeight int
	// Width and Height are the preferred content size (0 = automatic).
	Width, Height int
	// Empty is shown when nothing matches (default "No matches").
	Empty string
	// KeepOrder keeps the item order while filtering instead of sorting by
	// match score.
	KeepOrder bool
	// Hints are extra footer key hints (key, description pairs).
	Hints []string
}

// Picker is a generic fuzzy picker overlay (Telescope style): a query field
// on top, the filtered list below, optional multi-select and preview. It
// implements ui.Overlay, ui.KeyCapturer, ui.BackHandler and ui.Footer.
type Picker[T any] struct {
	ctx  ui.Context
	opts PickerOptions[T]
	m    modal

	items    []T
	keys     []string
	matches  []pickMatch
	checked  map[string]bool
	sel, off int
	rows     int // visible rows at the last render
	loading  bool
	err      string
	preview  bool
	lastKey  string
	listTop  int // first list line in the content area at the last render
}

type pickMatch struct {
	index int
	idx   []int
}

type pickerLoadedMsg[T any] struct {
	p     *Picker[T]
	items []T
	err   error
}

// NewPicker builds a picker overlay. Push it with ui.Push.
func NewPicker[T any](ctx ui.Context, opts PickerOptions[T]) *Picker[T] {
	if opts.KeyContext == "" {
		opts.KeyContext = ContextPicker
	}
	if opts.Filter == nil {
		opts.Filter = func(t T) string { return "" }
	}
	if opts.Key == nil {
		opts.Key = opts.Filter
	}
	if opts.Prompt == "" {
		opts.Prompt = promptFor(ctx)
	}
	p := &Picker[T]{ctx: ctx, opts: opts, checked: map[string]bool{}, preview: opts.Preview != nil}
	p.m.multi = opts.Multi
	p.m.verb = "select"
	p.setItems(opts.Items)
	if opts.Initial > 0 {
		for i, m := range p.matches {
			if m.index == opts.Initial {
				p.sel = i
			}
		}
	}
	p.loading = opts.Load != nil
	return p
}

func promptFor(ctx ui.Context) string {
	ic := ctx.Icons.Search
	if ic == "" {
		ic = "❯"
	}
	return ic + " "
}

func (p *Picker[T]) setItems(items []T) {
	var curKey string
	if c, ok := p.current(); ok {
		curKey = p.opts.Key(c)
	}
	p.items = items
	p.keys = make([]string, len(items))
	for i, it := range items {
		p.keys[i] = p.opts.Key(it)
		if p.opts.Multi && p.opts.Checked != nil && p.opts.Checked(it) {
			p.checked[p.keys[i]] = true
		}
	}
	p.filter()
	if curKey != "" {
		for i, m := range p.matches {
			if p.keys[m.index] == curKey {
				p.sel = i
			}
		}
	}
}

// filter recomputes the matches for the current query.
func (p *Picker[T]) filter() {
	q := strings.TrimSpace(p.m.input.Value())
	p.matches = p.matches[:0]
	if q == "" {
		for i := range p.items {
			p.matches = append(p.matches, pickMatch{index: i})
		}
	} else {
		texts := make([]string, len(p.items))
		for i, it := range p.items {
			texts[i] = theme.SanitizeLine(p.opts.Filter(it))
		}
		found := fuzzy.FindNoSort(q, texts)
		if !p.opts.KeepOrder {
			sort.SliceStable(found, func(i, j int) bool { return found[i].Score > found[j].Score })
		}
		for _, f := range found {
			p.matches = append(p.matches, pickMatch{index: f.Index, idx: f.MatchedIndexes})
		}
	}
	p.sel = min(max(p.sel, 0), max(len(p.matches)-1, 0))
}

func (p *Picker[T]) current() (T, bool) {
	var zero T
	if p.sel < 0 || p.sel >= len(p.matches) {
		return zero, false
	}
	return p.items[p.matches[p.sel].index], true
}

// Query returns the current filter text.
func (p *Picker[T]) Query() string { return p.m.input.Value() }

// SetQuery replaces the filter text.
func (p *Picker[T]) SetQuery(q string) {
	p.m.input.SetValue(q)
	p.sel = 0
	p.filter()
}

// Current returns the item under the cursor.
func (p *Picker[T]) Current() (T, bool) { return p.current() }

// Selected returns the marked items in item order (multi-select), or the
// current item.
func (p *Picker[T]) Selected() []T {
	var out []T
	if p.opts.Multi {
		for i, it := range p.items {
			if p.checked[p.keys[i]] {
				out = append(out, it)
			}
		}
	}
	if len(out) == 0 {
		if c, ok := p.current(); ok {
			out = append(out, c)
		}
	}
	return out
}

// Len returns the number of items matching the current query.
func (p *Picker[T]) Len() int { return len(p.matches) }

// Init implements ui.Overlay.
func (p *Picker[T]) Init() tea.Cmd {
	var cmds []tea.Cmd
	if p.opts.Load != nil {
		load := p.opts.Load
		cmds = append(cmds, func() tea.Msg {
			items, err := load()
			return pickerLoadedMsg[T]{p: p, items: items, err: err}
		})
	}
	cmds = append(cmds, p.highlightCmd())
	return tea.Batch(cmds...)
}

func (p *Picker[T]) highlightCmd() tea.Cmd {
	if p.opts.OnHighlight == nil {
		return nil
	}
	c, ok := p.current()
	if !ok {
		return nil
	}
	k := p.opts.Key(c)
	if k == p.lastKey {
		return nil
	}
	p.lastKey = k
	return p.opts.OnHighlight(c)
}

// Title implements ui.Overlay.
func (p *Picker[T]) Title() string { return theme.SanitizeLine(p.opts.Title) }

// KeyContext implements ui.Overlay.
func (p *Picker[T]) KeyContext() string { return p.opts.KeyContext }

// CaptureKeys implements ui.KeyCapturer: the picker routes keys itself.
func (p *Picker[T]) CaptureKeys() bool { return true }

// HandleBack implements ui.BackHandler: back leaves the input first.
func (p *Picker[T]) HandleBack() (bool, tea.Cmd) {
	if !p.m.normal {
		p.m.normal = true
		return true, nil
	}
	return false, p.cancelCmd()
}

// Dismiss implements ui.Dismisser: closing from outside (a click next to
// the dialog) counts as cancel.
func (p *Picker[T]) Dismiss() tea.Cmd { return p.cancelCmd() }

func (p *Picker[T]) cancelCmd() tea.Cmd {
	if p.opts.OnCancel != nil {
		return p.opts.OnCancel()
	}
	return nil
}

// Footer implements ui.Footer.
func (p *Picker[T]) Footer() string {
	return p.ctx.Styles.RenderKeyHints(p.m.hints(p.opts.Hints...)...)
}

// Size implements ui.Overlay.
func (p *Picker[T]) Size(maxW, maxH int) (int, int) {
	w := p.opts.Width
	if w <= 0 {
		w = 56
		if p.opts.Preview != nil {
			w += p.previewWidth() + 3
		}
	}
	h := p.opts.Height
	if h <= 0 {
		h = min(max(len(p.items)+3, 10), 22)
	}
	return min(w, maxW), min(h, maxH)
}

func (p *Picker[T]) previewWidth() int {
	if p.opts.PreviewWidth > 0 {
		return p.opts.PreviewWidth
	}
	return 40
}

// Update implements ui.Overlay.
func (p *Picker[T]) Update(msg tea.Msg) (ui.Overlay, tea.Cmd) {
	switch msg := msg.(type) {
	case pickerLoadedMsg[T]:
		if msg.p != p {
			return p, nil
		}
		p.loading = false
		if msg.err != nil {
			p.err = msg.err.Error()
			return p, nil
		}
		p.setItems(msg.items)
		if p.opts.Initial > 0 && p.opts.Initial < len(msg.items) && p.Query() == "" {
			p.sel = p.opts.Initial
		}
		return p, p.highlightCmd()
	case tea.KeyPressMsg:
		return p.apply(p.m.key(msg))
	case tea.PasteMsg:
		if p.m.paste(msg.Content) {
			return p.apply(navEdited)
		}
	case ui.ActionMsg:
		return p.apply(p.m.action(msg))
	case tea.MouseClickMsg:
		i := msg.Y - p.listTop
		if i >= 0 && i < p.rows && p.off+i < len(p.matches) {
			p.sel = p.off + i
			if p.opts.Multi {
				return p.apply(navToggle)
			}
			return p.apply(navSelect)
		}
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelDown:
			return p.apply(navDown)
		case tea.MouseWheelUp:
			return p.apply(navUp)
		}
	}
	return p, nil
}

func (p *Picker[T]) apply(a navAction) (ui.Overlay, tea.Cmd) {
	switch a {
	case navClose:
		return nil, p.cancelCmd()
	case navEdited:
		p.sel = 0
		p.off = 0
		p.filter()
		return p, p.highlightCmd()
	case navSelect:
		sel := p.Selected()
		if len(sel) == 0 {
			return p, nil
		}
		if p.opts.OnSelect == nil {
			return nil, nil
		}
		return nil, p.opts.OnSelect(sel)
	case navToggle, navToggleUp:
		if c, ok := p.current(); ok && p.opts.Multi {
			k := p.opts.Key(c)
			p.checked[k] = !p.checked[k]
			if !p.checked[k] {
				delete(p.checked, k)
			}
		}
		dir := navDown
		if a == navToggleUp {
			dir = navUp
		}
		p.sel = moveCursor(dir, p.sel, len(p.matches), p.rows)
		return p, p.highlightCmd()
	case navPreview:
		if p.opts.Preview != nil {
			p.preview = !p.preview
		}
	}
	if isMove(a) {
		p.sel = moveCursor(a, p.sel, len(p.matches), p.rows)
		return p, p.highlightCmd()
	}
	return p, nil
}

// CheckedCount returns the number of marked items.
func (p *Picker[T]) CheckedCount() int { return len(p.checked) }

// View implements ui.Overlay.
func (p *Picker[T]) View(w, h int) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	st := p.ctx.Styles
	listW, pvW, pvH := w, 0, 0
	if p.preview && p.opts.Preview != nil {
		switch {
		case w >= 30+p.previewWidth()+3:
			pvW = p.previewWidth()
			listW = w - pvW - 3
		case h >= 10:
			pvH = min(max(p.opts.PreviewHeight, 0), h/2)
			if pvH == 0 {
				pvH = min(8, h/2)
			}
		}
	}
	input := p.m.input.view(st, listW, p.opts.Prompt, p.opts.Placeholder, !p.m.normal, nil)
	count := ""
	if n := len(p.items); n > 0 {
		count = st.ChatListPreview.Render(itoa(len(p.matches)) + "/" + itoa(n))
		if c := len(p.checked); c > 0 {
			count = on(st.FuzzyMatch, lipgloss.NewStyle()).UnsetUnderline().Render(itoa(c)+" marked") + " " + count
		}
	}
	lines := []string{spread(input, count, listW, lipgloss.NewStyle()), hrule(st, listW)}
	p.listTop = 2
	listH := h - 2
	if pvH > 0 {
		listH -= pvH + 1
	}
	p.rows = max(listH, 0)
	p.off = scrollTo(p.off, p.sel, p.rows, len(p.matches))
	switch {
	case p.loading && len(p.items) == 0:
		lines = append(lines, st.ChatListPreview.Render("  Loading…"))
	case p.err != "":
		lines = append(lines, st.ErrorText.Render(clip("  "+theme.SanitizeLine(p.err), listW)))
	case len(p.matches) == 0:
		empty := p.opts.Empty
		if empty == "" {
			empty = "No matches"
		}
		lines = append(lines, st.ChatListPreview.Render(clip("  "+empty, listW)))
	}
	for i := p.off; i < len(p.matches) && i < p.off+p.rows; i++ {
		lines = append(lines, p.renderRow(i, listW))
	}
	for len(lines) < 2+p.rows {
		lines = append(lines, "")
	}
	if pvH > 0 {
		lines = append(lines, hrule(st, w))
		if c, ok := p.current(); ok {
			for _, l := range strings.Split(p.opts.Preview(c, w, pvH), "\n") {
				lines = append(lines, l)
			}
		}
		return finish(lines, w, h)
	}
	if pvW > 0 {
		var pv []string
		if c, ok := p.current(); ok {
			pv = strings.Split(p.opts.Preview(c, pvW, h), "\n")
		}
		return finish(sideBySide(st, lines, pv, listW, pvW, h), w, h)
	}
	return finish(lines, w, h)
}

func (p *Picker[T]) renderRow(i, w int) string {
	st := p.ctx.Styles
	m := p.matches[i]
	item := p.items[m.index]
	selected := i == p.sel
	base := rowStyles(st, selected)
	prefix := cursorCell(st, selected)
	if p.opts.Multi {
		box := "○ "
		bs := on(st.ChatListPreview, base)
		if p.checked[p.keys[m.index]] {
			box = "● "
			bs = on(lipgloss.NewStyle().Foreground(st.Theme.Palette.Accent).Bold(true), base)
		}
		prefix += base.Render(" ") + bs.Render(box)
	} else {
		prefix += base.Render(" ")
	}
	r := Row{
		Width:    max(w-width(prefix), 0),
		Selected: selected,
		Checked:  p.checked[p.keys[m.index]],
		Matches:  m.idx,
		Styles:   st,
		Icons:    p.ctx.Icons,
	}
	var body string
	if p.opts.Render != nil {
		body = p.opts.Render(item, r)
	} else {
		body = r.Highlight(p.opts.Filter(item))
	}
	return pad(prefix+hardClip(body, r.Width), w, base)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
