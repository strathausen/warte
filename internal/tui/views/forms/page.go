package forms

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"warte/internal/tui/theme"
)

// rowKind classifies a settings page row.
type rowKind int

const (
	rowHeader rowKind = iota // section title, not selectable
	rowInfo                  // read-only text, not selectable
	rowAction                // enter runs
	rowToggle                // enter / h / l flip
	rowChoice                // enter / h / l cycle
	rowEntity                // a member etc.: enter opens actions
	rowBlank                 // spacer
)

// row is one line of a settings page.
type row struct {
	id       string
	kind     rowKind
	label    string
	value    string // right column (choice value, member role)
	on       bool   // toggles
	disabled bool   // shown dimmed, actions refused with reason
	reason   string // why disabled (status line)
	danger   bool
	icon     string
	// enter runs activate; h/l run cycle(-1/+1) (toggles/choices).
	activate func() tea.Cmd
	cycle    func(delta int) tea.Cmd
	// keys are row-specific shortcuts ("a" approve, "x" remove, ...).
	keys map[string]func() tea.Cmd
	// hints are footer hints for the row: key, desc, key, desc...
	hints []string
}

func (r row) selectable() bool { return r.kind != rowHeader && r.kind != rowInfo && r.kind != rowBlank }

// page is a scrollable list of rows with a cursor, shared by the group and
// chat settings views.
type page struct {
	rows   []row
	sel    int
	offset int
	height int // last visible height
}

// setRows replaces the rows, keeping the cursor on the row with the same id.
func (p *page) setRows(rows []row) {
	var cur string
	if p.sel >= 0 && p.sel < len(p.rows) {
		cur = p.rows[p.sel].id
	}
	p.rows = rows
	p.sel = -1
	for i, r := range rows {
		if r.id == cur && r.selectable() {
			p.sel = i
			break
		}
	}
	if p.sel < 0 {
		p.sel = p.next(-1, 1)
	}
}

// next returns the next selectable index from i in direction dir, or i if
// there is none.
func (p *page) next(i, dir int) int {
	for j := i + dir; j >= 0 && j < len(p.rows); j += dir {
		if p.rows[j].selectable() {
			return j
		}
	}
	if i < 0 {
		return -1
	}
	return i
}

// move moves the cursor by delta selectable rows (large deltas clamp).
func (p *page) move(delta int) {
	if p.sel < 0 {
		p.sel = p.next(-1, 1)
		return
	}
	dir := 1
	if delta < 0 {
		dir, delta = -1, -delta
	}
	for ; delta > 0; delta-- {
		n := p.next(p.sel, dir)
		if n == p.sel {
			break
		}
		p.sel = n
	}
}

// current returns the selected row, or nil.
func (p *page) current() *row {
	if p.sel < 0 || p.sel >= len(p.rows) {
		return nil
	}
	return &p.rows[p.sel]
}

// render draws the rows into w x h, scrolling to keep the cursor visible.
func (p *page) render(st *theme.Styles, w, h int) []string {
	p.height = h
	if h <= 0 || w <= 0 {
		return nil
	}
	if p.sel >= 0 {
		if p.sel < p.offset {
			p.offset = p.sel
			// Show the section header above the first row of a section.
			if p.offset > 0 && p.rows[p.offset-1].kind == rowHeader {
				p.offset--
			}
		}
		if p.sel >= p.offset+h {
			p.offset = p.sel - h + 1
		}
	}
	p.offset = min(max(p.offset, 0), max(len(p.rows)-h, 0))
	scroll := len(p.rows) > h
	cw := w
	if scroll {
		cw = w - 1
	}
	var bar []string
	if scroll {
		bar = strings.Split(st.RenderScrollbar(h, len(p.rows), h, p.offset), "\n")
	}
	out := make([]string, 0, h)
	for i := p.offset; i < len(p.rows) && len(out) < h; i++ {
		line := p.renderRow(st, p.rows[i], i == p.sel, cw)
		if scroll {
			line = fit(line, cw)
			if len(out) < len(bar) {
				line += bar[len(out)]
			}
		}
		out = append(out, line)
	}
	return out
}

func (p *page) renderRow(st *theme.Styles, r row, selected bool, w int) string {
	switch r.kind {
	case rowBlank:
		return ""
	case rowHeader:
		return trunc(sectionTitle(st, strings.ToUpper(clean(r.label))), w)
	case rowInfo:
		return trunc(" "+r.label, w)
	}
	label := clean(r.label)
	if r.icon != "" {
		label = r.icon + " " + label
	}
	var right string
	switch r.kind {
	case rowToggle:
		right = toggleGlyph(st, r.on)
	case rowChoice:
		right = "‹ " + clean(r.value) + " ›"
		if !selected {
			right = muted(st, right)
		}
	default:
		right = r.value
	}
	if selected {
		plain := twoCol(" "+label, stripANSI(right)+" ", w)
		return selectedRow(st, plain, w)
	}
	switch {
	case r.disabled:
		label = subtle(st, label)
	case r.danger:
		label = st.ErrorText.Render(label)
	}
	return twoCol(" "+label, right+" ", w)
}
