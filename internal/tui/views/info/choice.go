package info

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"warte/internal/format"
	"warte/internal/tui/keymap"
	"warte/internal/tui/ui"
)

// KeyContextChoice is the keymap overlay context of the small pickers
// (mute duration, disappearing timer).
const KeyContextChoice = "info.choice"

// Choice is one entry of a choice overlay: Label is shown, Line is the ex
// command emitted (as ui.RunCommandMsg) when it is picked.
type Choice struct {
	Label string
	Hint  string
	Line  string
}

type choiceOverlay struct {
	ctx     ui.Context
	title   string
	choices []Choice
	sel     int
	height  int
}

// NewChoice returns a small list overlay; enter emits ui.RunCommandMsg with
// the chosen entry's Line and closes it. Digits 1–9 pick directly.
func NewChoice(ctx ui.Context, title string, choices []Choice, selected int) ui.Overlay {
	return &choiceOverlay{ctx: ctx, title: title, choices: choices, sel: clamp(selected, 0, len(choices)-1)}
}

// NewMutePicker offers the mute durations Signal offers.
func NewMutePicker(ctx ui.Context) ui.Overlay {
	return NewChoice(ctx, "Mute notifications", []Choice{
		{Label: "For 1 hour", Line: "mute 1h"},
		{Label: "For 8 hours", Line: "mute 8h"},
		{Label: "For 1 day", Line: "mute 1d"},
		{Label: "For 1 week", Line: "mute 1w"},
		{Label: "Always", Line: "mute"},
	}, 0)
}

// NewTimerPicker offers the disappearing-message presets; current is
// preselected and marked.
func NewTimerPicker(ctx ui.Context, current time.Duration) ui.Overlay {
	var cs []Choice
	sel := 0
	for i, d := range format.TimerPresets {
		c := Choice{Label: strings.ToUpper(format.FormatDurationLong(d)[:1]) + format.FormatDurationLong(d)[1:], Line: "disappear " + format.FormatDuration(d)}
		if d == current {
			c.Hint = "current"
			sel = i
		}
		cs = append(cs, c)
	}
	return NewChoice(ctx, "Disappearing messages", cs, sel)
}

func (o *choiceOverlay) Init() tea.Cmd      { return nil }
func (o *choiceOverlay) Title() string      { return o.title }
func (o *choiceOverlay) KeyContext() string { return KeyContextChoice }
func (o *choiceOverlay) Size(maxW, maxH int) (int, int) {
	w := 28
	for _, c := range o.choices {
		w = max(w, width(c.Label)+width(c.Hint)+8)
	}
	return min(w, maxW), min(len(o.choices), maxH)
}

// CaptureKeys makes digits reach the picker as shortcuts (they would
// otherwise be count prefixes); esc / q are handled in Update.
func (o *choiceOverlay) CaptureKeys() bool { return true }

func (o *choiceOverlay) Footer() string {
	return o.ctx.Styles.RenderKeyHints("j/k", "move", "1-9/enter", "choose", "esc", "cancel")
}

func (o *choiceOverlay) Update(msg tea.Msg) (ui.Overlay, tea.Cmd) {
	var act keymap.Action
	count := 1
	switch msg := msg.(type) {
	case ui.ActionMsg:
		act, count = msg.Action, msg.Count
	case tea.KeyPressMsg:
		k := msg.String()
		if k == "esc" || k == "q" || k == "ctrl+c" {
			return nil, nil
		}
		if len(k) == 1 && k[0] >= '1' && k[0] <= '9' {
			if i := int(k[0] - '1'); i < len(o.choices) {
				o.sel = i
				return o.pick()
			}
			return o, nil
		}
		a, ok := keyAction(k)
		if !ok {
			return o, nil
		}
		act = a
	default:
		return o, nil
	}
	if act == keymap.ActItemSelect {
		return o.pick()
	}
	o.sel, _ = moveAction(act, count, o.sel, len(o.choices), o.height)
	return o, nil
}

func (o *choiceOverlay) pick() (ui.Overlay, tea.Cmd) {
	if len(o.choices) == 0 {
		return nil, nil
	}
	line := o.choices[o.sel].Line
	return nil, func() tea.Msg { return ui.RunCommandMsg{Line: line} }
}

func (o *choiceOverlay) View(w, h int) string {
	o.height = h
	st := o.ctx.Styles
	ic := o.ctx.Icons
	off := clamp(o.sel-h+1, 0, max(len(o.choices)-h, 0))
	var lines []string
	for i := off; i < len(o.choices) && len(lines) < h; i++ {
		c := o.choices[i]
		label := clean(c.Label)
		if c.Hint != "" {
			label += "  " + st.Subtitle.Render(clean(c.Hint))
		}
		if i == o.sel {
			lines = append(lines, selectedRow(st, ic, label, w, true))
		} else {
			lines = append(lines, listRow(ic, label, w))
		}
	}
	return fitLines(lines, w, h)
}
