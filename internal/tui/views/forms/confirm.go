package forms

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"warte/internal/tui/keymap"
	"warte/internal/tui/ui"
)

// confirmOverlay is a y/n dialog (keymap context keymap.OverlayConfirm:
// y / enter confirm, n / esc / q cancel).
type confirmOverlay struct {
	ctx    ui.Context
	title  string
	text   string
	yes    string
	onYes  tea.Cmd
	danger bool
}

// NewConfirm returns a confirmation dialog that runs onYes when confirmed.
// danger paints the confirm button in the error color.
func NewConfirm(ctx ui.Context, title, text, yesLabel string, danger bool, onYes tea.Cmd) ui.Overlay {
	if yesLabel == "" {
		yesLabel = "Yes"
	}
	return &confirmOverlay{ctx: ctx, title: title, text: text, yes: yesLabel, onYes: onYes, danger: danger}
}

func (c *confirmOverlay) Init() tea.Cmd      { return nil }
func (c *confirmOverlay) Title() string      { return clean(c.title) }
func (c *confirmOverlay) KeyContext() string { return keymap.OverlayConfirm }
func (c *confirmOverlay) Footer() string {
	return c.ctx.Styles.RenderKeyHints("y", strings.ToLower(c.yes), "n", "cancel")
}

func (c *confirmOverlay) Size(maxW, maxH int) (int, int) {
	w := min(max(width(clean(c.text)), 36), 56, maxW)
	return w, min(len(wrapText(clean(c.text), w))+2, maxH)
}

func (c *confirmOverlay) Update(msg tea.Msg) (ui.Overlay, tea.Cmd) {
	switch msg := msg.(type) {
	case ui.ActionMsg:
		switch msg.Action {
		case keymap.ActConfirmYes:
			return nil, c.onYes
		case keymap.ActConfirmNo:
			return nil, nil
		}
	case tea.KeyPressMsg:
		switch keymap.FromTea(msg) {
		case "y", "Y", "enter":
			return nil, c.onYes
		case "n", "N", "esc", "q":
			return nil, nil
		}
	}
	return c, nil
}

func (c *confirmOverlay) View(w, h int) string {
	st := c.ctx.Styles
	lines := wrapText(clean(c.text), w)
	yes := " " + c.yes + " (y) "
	var yb string
	switch {
	case st.Theme.Mono:
		yb = lipgloss.NewStyle().Reverse(true).Bold(true).Render(yes)
	case c.danger:
		yb = lipgloss.NewStyle().Background(pal(st).Error).Foreground(pal(st).Bg).Bold(true).Render(yes)
	default:
		yb = lipgloss.NewStyle().Background(pal(st).Accent).Foreground(pal(st).Bg).Bold(true).Render(yes)
	}
	no := muted(st, " Cancel (n) ")
	lines = append(lines, "", yb+"  "+no)
	if len(lines) > h {
		// Keep the buttons visible.
		lines = append(lines[:max(h-1, 0)], yb+"  "+no)
	}
	return clampLines(lines, w, h)
}

// ---- action menu ---------------------------------------------------------

// menuAction is one entry of an actionMenu.
type menuAction struct {
	key    string // shortcut shown and accepted, e.g. "p"
	label  string
	danger bool
	run    func() tea.Cmd
}

// actionMenu is a small list of actions (e.g. for a group member). j/k move,
// enter runs, the shortcut letters run directly, esc closes.
type actionMenu struct {
	ctx     ui.Context
	title   string
	actions []menuAction
	sel     int
}

func newActionMenu(ctx ui.Context, title string, actions []menuAction) *actionMenu {
	return &actionMenu{ctx: ctx, title: title, actions: actions}
}

func (m *actionMenu) Init() tea.Cmd      { return nil }
func (m *actionMenu) Title() string      { return clean(m.title) }
func (m *actionMenu) KeyContext() string { return ContextActions }
func (m *actionMenu) Footer() string {
	return m.ctx.Styles.RenderKeyHints("j/k", "move", "enter", "run", "esc", "close")
}

func (m *actionMenu) Size(maxW, maxH int) (int, int) {
	w := 24
	for _, a := range m.actions {
		w = max(w, width(a.label)+8)
	}
	return min(w, maxW), min(len(m.actions), maxH)
}

func (m *actionMenu) Update(msg tea.Msg) (ui.Overlay, tea.Cmd) {
	if d, ok := navDelta(msg, len(m.actions)); ok {
		m.sel = min(max(m.sel+d, 0), max(len(m.actions)-1, 0))
		return m, nil
	}
	if isSelect(msg) {
		if m.sel < len(m.actions) {
			return nil, m.actions[m.sel].run()
		}
		return nil, nil
	}
	if k := keyOf(msg); k != "" {
		for _, a := range m.actions {
			if a.key == k {
				return nil, a.run()
			}
		}
	}
	return m, nil
}

func (m *actionMenu) View(w, h int) string {
	st := m.ctx.Styles
	lines := make([]string, 0, len(m.actions))
	for i, a := range m.actions {
		label := a.label
		if a.danger && !st.Theme.Mono {
			label = st.ErrorText.Render(label)
		}
		row := " " + accent(st, a.key) + "  " + label
		if i == m.sel {
			row = selectedRow(st, " "+a.key+"  "+a.label, w)
		}
		lines = append(lines, row)
	}
	return clampLines(lines, w, h)
}
