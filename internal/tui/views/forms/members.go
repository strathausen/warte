package forms

import (
	"context"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/sahilm/fuzzy"

	"warte/internal/core"
	"warte/internal/tui/keymap"
	"warte/internal/tui/theme"
	"warte/internal/tui/ui"
)

// memberPicker is a fuzzy multi-select over contacts (Telescope style: type
// to filter, tab toggles, enter confirms). It is used inside the new group
// wizard and, wrapped in memberPickerOverlay, to add members to a group.
type memberPicker struct {
	ctx      ui.Context
	contacts []core.Contact
	keys     []string // search strings, parallel to contacts
	selected map[core.ACI]bool
	order    []core.ACI
	filter   []rune
	matches  []fuzzy.Match
	sel      int
	offset   int
}

func newMemberPicker(ctx ui.Context) *memberPicker {
	return &memberPicker{ctx: ctx, selected: map[core.ACI]bool{}}
}

// setContacts installs the candidates (blocked contacts, self and the
// excluded ACIs are skipped).
func (p *memberPicker) setContacts(list []core.Contact, exclude map[core.ACI]bool) {
	self := selfACI(p.ctx)
	p.contacts = p.contacts[:0]
	p.keys = p.keys[:0]
	for _, c := range list {
		if c.IsSelf || c.ACI == self || c.Blocked || exclude[c.ACI] {
			continue
		}
		p.contacts = append(p.contacts, c)
		p.keys = append(p.keys, searchKey(c))
	}
	p.refilter()
}

// searchKey is what the fuzzy filter matches against: the display name
// first (highlighted), then username and phone number.
func searchKey(c core.Contact) string {
	parts := []string{clean(c.DisplayName())}
	if c.Username != "" {
		parts = append(parts, clean(c.Username))
	}
	if c.E164 != "" {
		parts = append(parts, clean(c.E164))
	}
	return strings.Join(parts, "  ")
}

func (p *memberPicker) refilter() {
	q := strings.TrimSpace(string(p.filter))
	if q == "" {
		p.matches = p.matches[:0]
		for i, k := range p.keys {
			p.matches = append(p.matches, fuzzy.Match{Str: k, Index: i})
		}
	} else {
		p.matches = fuzzy.Find(q, p.keys)
	}
	p.sel = min(p.sel, max(len(p.matches)-1, 0))
}

// selection returns the chosen ACIs in the order they were picked.
func (p *memberPicker) selection() []core.ACI {
	out := make([]core.ACI, 0, len(p.order))
	for _, a := range p.order {
		if p.selected[a] {
			out = append(out, a)
		}
	}
	return out
}

// names returns the display names of the selection.
func (p *memberPicker) names() []string {
	byACI := map[core.ACI]string{}
	for _, c := range p.contacts {
		byACI[c.ACI] = clean(c.DisplayName())
	}
	var out []string
	for _, a := range p.selection() {
		out = append(out, byACI[a])
	}
	return out
}

func (p *memberPicker) toggle(i int) {
	if i < 0 || i >= len(p.matches) {
		return
	}
	a := p.contacts[p.matches[i].Index].ACI
	if p.selected[a] {
		delete(p.selected, a)
		return
	}
	p.selected[a] = true
	p.order = append(p.order, a)
}

// pickerResult is what a key did to the picker.
type pickerResult int

const (
	pickerNone pickerResult = iota
	pickerDone
	pickerCancel
)

// update handles a raw key press (the picker always captures keys).
func (p *memberPicker) update(msg tea.Msg) pickerResult {
	if d, ok := navDelta(msg, 8); ok {
		p.sel = min(max(p.sel+d, 0), max(len(p.matches)-1, 0))
		return pickerNone
	}
	km, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return pickerNone
	}
	switch k := keymap.FromTea(km); k {
	case "esc":
		if len(p.filter) > 0 {
			p.filter = p.filter[:0]
			p.refilter()
			return pickerNone
		}
		return pickerCancel
	case "ctrl+c":
		return pickerCancel
	case "enter":
		if len(p.selection()) == 0 && len(p.filter) > 0 && len(p.matches) > 0 {
			p.toggle(p.sel)
		}
		return pickerDone
	case "tab", "ctrl+space", "ctrl+@":
		p.toggle(p.sel)
		if p.sel < len(p.matches)-1 {
			p.sel++
		}
	case "shift+tab":
		p.toggle(p.sel)
		if p.sel > 0 {
			p.sel--
		}
	case "ctrl+j", "ctrl+n":
		p.sel = min(p.sel+1, max(len(p.matches)-1, 0))
	case "ctrl+k", "ctrl+p":
		p.sel = max(p.sel-1, 0)
	case "backspace", "ctrl+h":
		if len(p.filter) > 0 {
			p.filter = p.filter[:len(p.filter)-1]
			p.refilter()
		}
	case "ctrl+u":
		p.filter = p.filter[:0]
		p.refilter()
	case "ctrl+w":
		s := strings.TrimRightFunc(string(p.filter), unicode.IsSpace)
		if i := strings.LastIndexFunc(s, unicode.IsSpace); i >= 0 {
			s = s[:i+1]
		} else {
			s = ""
		}
		p.filter = []rune(s)
		p.refilter()
	default:
		if km.Text != "" {
			for _, r := range km.Text {
				if unicode.IsPrint(r) {
					p.filter = append(p.filter, r)
				}
			}
			p.sel = 0
			p.refilter()
		}
	}
	return pickerNone
}

// view renders the picker into w x h.
func (p *memberPicker) view(w, h int) []string {
	st := p.ctx.Styles
	if w <= 0 || h <= 0 {
		return nil
	}
	cursor := "▏"
	q := clean(string(p.filter))
	prompt := accent(st, "› ") + q + accent(st, cursor)
	if q == "" {
		prompt = accent(st, "› ") + subtle(st, "type to filter contacts")
	}
	count := muted(st, itoa(len(p.selection()))+" selected")
	lines := []string{twoCol(prompt, count, w)}
	if names := p.names(); len(names) > 0 {
		lines = append(lines, trunc(subtle(st, "with ")+st.SuccessText.Render(strings.Join(names, ", ")), w))
	} else {
		lines = append(lines, subtle(st, trunc("tab selects · enter continues", w)))
	}
	listH := h - len(lines)
	if listH <= 0 {
		return clampLinesSlice(lines, w, h)
	}
	if len(p.matches) == 0 {
		msg := "No contacts"
		if q != "" {
			msg = "No matches for “" + q + "”"
		}
		lines = append(lines, muted(st, trunc(msg, w)))
		return clampLinesSlice(lines, w, h)
	}
	if p.sel < p.offset {
		p.offset = p.sel
	}
	if p.sel >= p.offset+listH {
		p.offset = p.sel - listH + 1
	}
	p.offset = min(max(p.offset, 0), max(len(p.matches)-listH, 0))
	for i := p.offset; i < len(p.matches) && i < p.offset+listH; i++ {
		m := p.matches[i]
		c := p.contacts[m.Index]
		mark := "  "
		if p.selected[c.ACI] {
			mark = "✓ "
		}
		name := clean(c.DisplayName())
		sub := ""
		if c.Username != "" {
			sub = clean(c.Username)
		} else if c.E164 != "" {
			sub = clean(c.E164)
		}
		if i == p.sel {
			lines = append(lines, selectedRow(st, twoCol(" "+mark+name, sub+" ", w), w))
			continue
		}
		hl := highlight(st, name, m.MatchedIndexes)
		if p.selected[c.ACI] {
			mark = st.SuccessText.Render(mark)
		}
		lines = append(lines, twoCol(" "+mark+hl, muted(st, sub)+" ", w))
	}
	return clampLinesSlice(lines, w, h)
}

// highlight styles the runes of s starting at the byte offsets idx (from
// fuzzy) with FuzzyMatch.
func highlight(st *theme.Styles, s string, idx []int) string {
	if len(idx) == 0 {
		return s
	}
	set := make(map[int]bool, len(idx))
	for _, i := range idx {
		set[i] = true
	}
	var b strings.Builder
	for i, r := range s {
		if set[i] {
			b.WriteString(st.FuzzyMatch.Render(string(r)))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func clampLinesSlice(lines []string, w, h int) []string {
	return strings.Split(clampLines(lines, w, h), "\n")
}

// itoa formats a small non-negative int.
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

// ---- overlay wrapper -------------------------------------------------------

// memberPickerOverlay lets the user pick contacts and runs onDone with them.
type memberPickerOverlay struct {
	ctx     ui.Context
	title   string
	picker  *memberPicker
	exclude map[core.ACI]bool
	loaded  bool
	err     string
	onDone  func(acis []core.ACI) tea.Cmd
}

// NewMemberPicker returns an overlay with a fuzzy multi-select over the
// contacts (minus exclude); onDone receives the picked ACIs (not called when
// nothing was picked or the picker was cancelled).
func NewMemberPicker(ctx ui.Context, title string, exclude []core.ACI, onDone func(acis []core.ACI) tea.Cmd) ui.Overlay {
	ex := map[core.ACI]bool{}
	for _, a := range exclude {
		ex[a] = true
	}
	return &memberPickerOverlay{ctx: ctx, title: title, picker: newMemberPicker(ctx), exclude: ex, onDone: onDone}
}

type contactsLoadedMsg struct {
	owner any
	list  []core.Contact
	err   error
}

func loadContacts(svc core.Service, owner any) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
		defer cancel()
		list, err := svc.Contacts(ctx)
		return contactsLoadedMsg{owner: owner, list: list, err: err}
	}
}

func (o *memberPickerOverlay) Init() tea.Cmd      { return loadContacts(o.ctx.Svc, o) }
func (o *memberPickerOverlay) Title() string      { return clean(o.title) }
func (o *memberPickerOverlay) KeyContext() string { return ContextMembers }
func (o *memberPickerOverlay) CaptureKeys() bool  { return true }
func (o *memberPickerOverlay) Footer() string {
	return o.ctx.Styles.RenderKeyHints("tab", "toggle", "↑/↓", "move", "enter", "add", "esc", "cancel")
}
func (o *memberPickerOverlay) Size(maxW, maxH int) (int, int) {
	return min(56, maxW), min(max(len(o.picker.contacts)+2, 8), 20, maxH)
}

func (o *memberPickerOverlay) Update(msg tea.Msg) (ui.Overlay, tea.Cmd) {
	switch msg := msg.(type) {
	case contactsLoadedMsg:
		if msg.owner != o {
			return o, nil
		}
		o.loaded = true
		if msg.err != nil {
			o.err = msg.err.Error()
		}
		o.picker.setContacts(msg.list, o.exclude)
		return o, nil
	}
	switch o.picker.update(msg) {
	case pickerCancel:
		return nil, nil
	case pickerDone:
		sel := o.picker.selection()
		if len(sel) == 0 || o.onDone == nil {
			return nil, nil
		}
		return nil, o.onDone(sel)
	}
	return o, nil
}

func (o *memberPickerOverlay) View(w, h int) string {
	st := o.ctx.Styles
	if !o.loaded {
		return muted(st, "Loading contacts…")
	}
	lines := o.picker.view(w, h)
	if o.err != "" {
		lines = append(lines[:min(len(lines), max(h-1, 0))], statusLine(st, o.err, true, w))
	}
	return clampLines(lines, w, h)
}
