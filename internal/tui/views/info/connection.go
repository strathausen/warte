package info

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"warte/internal/core"
	"warte/internal/format"
	"warte/internal/tui/keymap"
	"warte/internal/tui/ui"
)

// KeyContextConnection is the keymap overlay context of the connection
// screen.
const KeyContextConnection = "info.connection"

// RelinkMsg is emitted by the connection screen when the user asks to link
// this device again (enter / r while logged out). The integrator should
// start the linking flow (core.Linker, QR code view).
type RelinkMsg = ui.RelinkMsg

// RelinkHint is the shell command shown in the logged-out instructions.
var RelinkHint = "warte link"

// spinner frames for the reconnecting state.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

const spinnerInterval = 120 * time.Millisecond

type connTick struct{ id int }

// Connection is the full-screen view shown when the account is logged out
// (unlinked / expired) or the connection to Signal is down. It closes
// itself when the connection comes back (core.ConnChanged{ConnConnected}).
// While logged out, q / esc quit warte instead of closing the screen.
type Connection struct {
	ctx   ui.Context
	state core.ConnState
	err   error
	since time.Time
	frame int
	tick  int
	// ticking reports whether a spinner tick was scheduled at tickAt.
	// Ticks only reach the top overlay, so the chain breaks while another
	// overlay covers this screen; a later message restarts it once the
	// outstanding tick is overdue.
	ticking bool
	tickAt  time.Time
}

// NewConnection returns the screen for state (core.ConnLoggedOut,
// ConnDisconnected or ConnConnecting) with an optional cause.
func NewConnection(ctx ui.Context, state core.ConnState, err error) *Connection {
	return &Connection{ctx: ctx, state: state, err: err, since: now(ctx)}
}

// State returns the connection state shown.
func (o *Connection) State() core.ConnState { return o.state }

func (o *Connection) loggedOut() bool { return o.state == core.ConnLoggedOut }

func (o *Connection) animate() tea.Cmd {
	if o.loggedOut() || o.ctx.Config.ReduceMotion {
		o.ticking = false
		return nil
	}
	o.ticking, o.tickAt = true, time.Now()
	o.tick++
	id := o.tick
	return tea.Tick(spinnerInterval, func(time.Time) tea.Msg { return connTick{id} })
}

func (o *Connection) Init() tea.Cmd { return o.animate() }

func (o *Connection) Title() string {
	if o.loggedOut() {
		return "Logged out"
	}
	return "Connection problem"
}

func (o *Connection) KeyContext() string             { return KeyContextConnection }
func (o *Connection) Size(maxW, maxH int) (int, int) { return maxW, maxH }

func (o *Connection) Footer() string {
	st := o.ctx.Styles
	if o.loggedOut() {
		return st.RenderKeyHints("enter", "link again", "q", "quit")
	}
	return st.RenderKeyHints("esc", "dismiss")
}

// HandleBack quits warte while logged out (there is nothing behind this
// screen to go back to); otherwise the screen is dismissed.
func (o *Connection) HandleBack() (bool, tea.Cmd) {
	if o.loggedOut() {
		return true, tea.Quit
	}
	return false, nil
}

func (o *Connection) Update(msg tea.Msg) (ui.Overlay, tea.Cmd) {
	if t, ok := msg.(connTick); ok {
		if t.id != o.tick {
			return o, nil
		}
		o.frame++
		return o, o.animate()
	}
	ov, cmd := o.update(msg)
	if ov == nil || (o.ticking && time.Since(o.tickAt) < 4*spinnerInterval) {
		return ov, cmd
	}
	return ov, tea.Batch(cmd, o.animate())
}

func (o *Connection) update(msg tea.Msg) (ui.Overlay, tea.Cmd) {
	switch msg := msg.(type) {
	case ui.EventMsg:
		switch ev := msg.Event.(type) {
		case core.ConnChanged:
			if ev.State == core.ConnConnected {
				return nil, ui.Toast(ui.ToastSuccess, "Connected to Signal")
			}
			o.state = ev.State
		case core.LoggedOut:
			o.state, o.err = core.ConnLoggedOut, ev.Err
		}
	case ui.ActionMsg:
		if msg.Action == keymap.ActItemSelect && o.loggedOut() {
			return o, func() tea.Msg { return RelinkMsg{} }
		}
	case tea.KeyPressMsg:
		if (msg.String() == "r" || msg.String() == "enter") && o.loggedOut() {
			return o, func() tea.Msg { return RelinkMsg{} }
		}
	}
	return o, nil
}

func (o *Connection) View(w, h int) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	st := o.ctx.Styles
	p := st.Theme.Palette
	textW := min(w-4, 60)
	var body []string
	var centered []bool
	add := func(l ...string) {
		body = append(body, l...)
		for range l {
			centered = append(centered, false)
		}
	}
	addc := func(l ...string) {
		body = append(body, l...)
		for range l {
			centered = append(centered, true)
		}
	}
	para := func(s string, style lipgloss.Style) {
		for _, l := range format.Wrap(s, max(textW, 1)) {
			add(style.Render(l))
		}
	}

	// Wordmark.
	mark := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(p.Accent).
		Foreground(p.Accent).Bold(true).Padding(0, 3).Render("w a r t e")
	if st.Theme.Mono || w < 20 {
		mark = lipgloss.NewStyle().Bold(true).Render("warte")
	}
	addc(strings.Split(mark, "\n")...)
	add("")

	if o.loggedOut() {
		addc(st.ErrorText.Bold(true).Render("You've been logged out"))
		add("")
		para("This computer is no longer linked to your Signal account. It was removed from Linked devices on your phone, or the link expired after a long time offline. Your local history stays encrypted on this computer.", st.Subtitle)
		add("")
		add(st.Title.Render("To link again"))
		add("")
		steps := []string{
			"Press enter here (or run " + RelinkHint + ")",
			"On your phone open Signal → Settings → Linked devices",
			"Tap Link new device and scan the QR code",
		}
		for i, s := range steps {
			num := st.KeyHintKey.Render(" " + itoa(i+1) + " ")
			for j, l := range format.Wrap(s, max(textW-5, 1)) {
				if j == 0 {
					add(num + "  " + st.App.Render(l))
				} else {
					add(spaces(width(num)+2) + st.App.Render(l))
				}
			}
		}
	} else {
		spin := spinnerFrames[o.frame%len(spinnerFrames)]
		if o.ctx.Config.ReduceMotion {
			spin = "…"
		}
		head := "Can't reach Signal"
		if o.state == core.ConnConnecting {
			head = "Connecting to Signal"
		}
		addc(st.WarningText.Bold(true).Render(spin + " " + head))
		add("")
		para("warte keeps retrying in the background and this screen closes once the connection is back. Messages you already have stay readable.", st.Subtitle)
		add("")
		add(st.Title.Render("If it doesn't come back"))
		add("")
		for _, s := range []string{
			"Check your internet connection, VPN or proxy",
			"Make sure your phone still lists this device under Linked devices",
			"Esc dismisses this screen; the status line keeps showing the state",
		} {
			for j, l := range format.Wrap(s, max(textW-4, 1)) {
				lead := "  "
				if j == 0 {
					lead = st.Subtitle.Render("•") + " "
				}
				add(lead + st.App.Render(l))
			}
		}
		if d := now(o.ctx).Sub(o.since); d >= time.Minute {
			add("", st.Subtitle.Render("Offline for "+callDuration(d.Truncate(time.Minute))))
		}
	}
	if o.err != nil {
		add("", st.Subtitle.Render(trunc("Reason: "+clean(o.err.Error()), textW)))
	}

	// Center the block: left-align lines within a centered column.
	colW := 0
	for _, l := range body {
		colW = max(colW, width(l))
	}
	colW = min(colW, w)
	left := max((w-colW)/2, 0)
	top := max((h-len(body))/2, 0)
	out := make([]string, 0, h)
	for range top {
		out = append(out, "")
	}
	for i, l := range body {
		if centered[i] {
			l = spaces((colW-width(l))/2) + l
		}
		out = append(out, spaces(left)+trunc(l, w-left))
	}
	if len(out) > h {
		out = out[:h]
	}
	return fitLines(out, w, h)
}
