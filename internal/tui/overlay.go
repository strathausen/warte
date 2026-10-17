package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"warte/internal/tui/keymap"
	"warte/internal/tui/ui"
)

// pushOverlay opens o on top of the stack. Keys go to it in Normal mode;
// the previous mode is restored when the last overlay closes.
func (a *App) pushOverlay(o ui.Overlay) tea.Cmd {
	if o == nil {
		return nil
	}
	if len(a.overlays) == 0 {
		a.modeBeforeOver = a.keys.Mode()
		if a.modeBeforeOver.IsVisual() {
			a.modeBeforeOver = keymap.Normal
		}
	}
	a.keys.SetMode(keymap.Normal)
	a.overlays = append(a.overlays, o)
	a.overlayKeys = append(a.overlayKeys, a.nextOverlayKeys)
	a.nextOverlayKeys = ""
	return o.Init()
}

// popOverlay closes the top overlay.
func (a *App) popOverlay() tea.Cmd {
	if len(a.overlays) == 0 {
		return nil
	}
	a.overlays = a.overlays[:len(a.overlays)-1]
	if n := len(a.overlays); n < len(a.overlayKeys) {
		a.overlayKeys = a.overlayKeys[:n]
	}
	if len(a.overlays) == 0 {
		a.endPreview()
		if a.modeBeforeOver == keymap.Insert && a.conv.open {
			a.keys.SetMode(keymap.Insert)
			a.focus = FocusComposer
		} else {
			a.keys.SetMode(keymap.Normal)
		}
		return a.markReadIfVisible()
	}
	return nil
}

// overlayKey routes a key press to the top overlay.
func (a *App) overlayKey(msg tea.KeyPressMsg) tea.Cmd {
	top := a.overlays[len(a.overlays)-1]
	if kc, ok := top.(ui.KeyCapturer); ok && kc.CaptureKeys() {
		return a.forwardOverlay(msg)
	}
	ctx := keymap.Overlay(top.KeyContext())
	return a.handleResult(a.keys.Feed(keymap.FromTea(msg), ctx), &msg)
}

// overlayAction delivers a fired binding to the top overlay.
func (a *App) overlayAction(r keymap.Result, msg *tea.KeyPressMsg) tea.Cmd {
	top := a.overlays[len(a.overlays)-1]
	switch r.Action {
	case keymap.ActBack:
		if bh, ok := top.(ui.BackHandler); ok {
			if handled, cmd := bh.HandleBack(); handled {
				return cmd
			}
		}
		return a.popOverlay()
	case keymap.ActQuit:
		return a.quit()
	case keymap.ActRedraw:
		return tea.ClearScreen
	case keymap.ActLock:
		return a.lockScreen()
	case keymap.ActQuitHint:
		return a.popOverlay()
	case keymap.ActHelp:
		if _, isHelp := top.(*helpOverlay); !isHelp {
			// "?" over a view: the keys of that view.
			a.nextOverlayKeys = r.Keys
			return a.pushOverlay(newOverlayHelp(a, top))
		}
	}
	return a.forwardOverlay(ui.ActionMsg{Action: r.Action, Count: r.Count, Keys: r.Keys})
}

// isOverlayToggle reports whether r repeats the keys that opened the top
// overlay ("K" over message details, "?" over help), which closes it.
func (a *App) isOverlayToggle(r keymap.Result) bool {
	n := len(a.overlays)
	if n == 0 || n > len(a.overlayKeys) || r.Keys == "" || r.Action == keymap.ActBack {
		return false
	}
	return a.overlayKeys[n-1] == r.Keys
}

// toggleOpeners are the actions whose keys close the overlay they opened
// when typed again.
var toggleOpeners = map[keymap.Action]bool{
	keymap.ActHelp: true, keymap.ActMessageInfo: true, keymap.ActMessageReactions: true,
	keymap.ActChatPinned: true, keymap.ActChatInfo: true, keymap.ActMediaGallery: true,
	keymap.ActStories: true, keymap.ActCalls: true, keymap.ActSettings: true,
}

// overlayRect is the screen area of the top overlay (border included).
type overlayRect struct{ x, y, w, h int }

// renderOverlay composites the top overlay over the (dimmed) frame lines.
func (a *App) renderOverlay(lines []string) []string {
	if len(a.overlays) == 0 {
		return lines
	}
	st := a.styles
	W, H := a.width, a.height
	dim := lipgloss.NewStyle().Foreground(st.Theme.Palette.FgSubtle)
	if st.Theme.Mono {
		dim = lipgloss.NewStyle().Faint(true)
	}
	for i := range lines {
		lines[i] = fit(dimLine(lines[i], dim), W)
	}
	top := a.overlays[len(a.overlays)-1]
	maxW := max(W-6, 10)
	maxH := max(H-4, 3)
	cw, ch := top.Size(maxW-4, maxH-2)
	cw = clamp(cw, 1, maxW-4)
	ch = clamp(ch, 1, maxH-2)
	content := splitLines(top.View(cw, ch))
	// Inner padding of one cell left/right.
	inner := make([]string, 0, ch)
	for _, l := range fitLines(content, cw, ch) {
		inner = append(inner, " "+l+" ")
	}
	title := st.DialogTitle.UnsetMarginBottom().Render(clean(top.Title()))
	footer := ""
	if f, ok := top.(ui.Footer); ok {
		footer = f.Footer()
	} else {
		footer = st.RenderKeyHints("esc", "close")
	}
	boxLines := box(st, inner, cw+4, ch+2, true, title, footer)
	bw, bh := cw+4, ch+2
	x := (W - bw) / 2
	y := max((H-bh)/2-1, 0)
	if an, ok := top.(selectionAnchored); ok && an.anchorToSelection() {
		x, y = a.anchorNearSelection(x, y, bw, bh)
	}
	a.zones.add(zone{x: x + 2, y: y + 1, w: cw, h: ch, kind: zoneOverlay})
	a.zones.overlay = &overlayRect{x: x, y: y, w: bw, h: bh}
	shadow := st.ShadowStyle.Render("▒")
	if st.Theme.Mono {
		shadow = " "
	}
	for i, bl := range boxLines {
		if y+i >= len(lines) {
			break
		}
		row := bl
		if i > 0 && x+bw < W {
			row += shadow
		}
		lines[y+i] = splice(lines[y+i], row, x)
	}
	if y+bh < len(lines) && x+bw < W {
		lines[y+bh] = splice(lines[y+bh], strings.Repeat(shadow, bw), x+1)
	}
	return lines
}

// selectionAnchored is implemented by built-in overlays that pop up next to
// the selected message (the quick reaction bar) instead of centered.
type selectionAnchored interface{ anchorToSelection() bool }

// anchorNearSelection places a bw x bh box just below the selected message
// (above it when there is no room), aligned with its side of the
// conversation. It returns the centered position cx, cy when the message is
// not on screen.
func (a *App) anchorNearSelection(cx, cy, bw, bh int) (int, int) {
	m := a.conv.selected()
	if m == nil {
		return cx, cy
	}
	var mz zone
	found := false
	for _, z := range a.zones.zones {
		if z.kind == zoneMessage && z.index == a.conv.sel {
			mz, found = z, true
		}
	}
	if !found {
		return cx, cy
	}
	H := a.height - 1 // status line
	y := mz.y + mz.h
	if y+bh+1 > H {
		y = mz.y - bh - 1
	}
	if y < 0 {
		return cx, cy
	}
	r := a.layout.conv
	x := r.x + 1 + gutterW
	if m.Outgoing {
		x = r.x + r.w - 2 - bw
	}
	return clamp(x, 0, max(a.width-bw-1, 0)), y
}

// ---- confirm dialog --------------------------------------------------------------------

type confirmOverlay struct {
	a      *App
	title  string
	text   string
	onYes  tea.Cmd
	danger bool
}

// newConfirm builds a y/n dialog that runs onYes when confirmed.
func newConfirm(a *App, title, text string, onYes tea.Cmd) ui.Overlay {
	return &confirmOverlay{a: a, title: title, text: text, onYes: onYes, danger: true}
}

func (c *confirmOverlay) Init() tea.Cmd      { return nil }
func (c *confirmOverlay) Title() string      { return c.title }
func (c *confirmOverlay) KeyContext() string { return keymap.OverlayConfirm }
func (c *confirmOverlay) Size(w, h int) (int, int) {
	return min(max(width(c.text), 34), w), 3
}
func (c *confirmOverlay) Footer() string {
	return c.a.styles.RenderKeyHints("y", "yes", "n", "no")
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
	case tea.MouseClickMsg:
		return c, nil
	}
	return c, nil
}

func (c *confirmOverlay) View(w, h int) string {
	st := c.a.styles
	lines := []string{st.App.Render(fitEllipsisRaw(c.text, w)), ""}
	yes := st.ModeBadge(3).Render(" Yes (y) ")
	if c.danger && !st.Theme.Mono {
		yes = lipgloss.NewStyle().Background(st.Theme.Palette.Error).Foreground(st.Theme.Palette.Bg).Bold(true).Padding(0, 1).Render("Yes (y)")
	}
	no := lipgloss.NewStyle().Foreground(st.Theme.Palette.FgMuted).Padding(0, 1).Render("No (n)")
	lines = append(lines, yes+"  "+no)
	return strings.Join(lines, "\n")
}
