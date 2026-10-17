package forms

import (
	"context"
	"errors"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"warte/internal/core"
	"warte/internal/tui/keymap"
	"warte/internal/tui/theme"
)

// LinkInstructions is shown under the device-link QR code.
const LinkInstructions = "Scan with Signal on your phone: Settings → Linked devices → +"

// ErrLinkCanceled is returned by Link.Result when the user quit before the
// device was linked.
var ErrLinkCanceled = errors.New("linking canceled")

type linkState int

const (
	linkStarting linkState = iota // waiting for the QR code
	linkWaiting                   // QR shown, waiting for the scan
	linkSyncing                   // scanned, progress stages
	linkDone
	linkFailed
)

// Link is the full-screen device-linking screen: a big scannable QR code
// (half blocks, quiet zone, high contrast), the provisioning URL as text
// fallback, progress stages and success / error states with retry. It is a
// standalone tea.Model so cmd/warte can run it before the main App:
//
//	m := forms.LinkModel(linker, cfg.DeviceName)
//	if _, err := tea.NewProgram(m).Run(); err != nil { … }
//	self, err := m.Result()
//
// Keys: q / esc / ctrl+c cancel and quit, r / enter retry after an error,
// c copies the link (OSC 52), enter continues after success (it also
// continues by itself after a short pause).
type Link struct {
	linker core.Linker
	device string
	styles *theme.Styles
	auto   bool // styles follow the terminal background
	icons  theme.IconSet

	state  linkState
	url    string
	qr     *QR
	qrErr  error
	stages []string
	self   core.Contact
	err    error

	w, h    int
	gen     int
	cancel  context.CancelFunc
	tick    int
	still   bool // reduce motion
	ticking bool
	// ContinueAfter is how long the success screen stays before quitting
	// (0 = wait for enter).
	ContinueAfter time.Duration
}

// LinkModel returns the device-link screen for linker; deviceName is sent to
// the phone (shown in its linked devices list).
func LinkModel(linker core.Linker, deviceName string) *Link {
	if strings.TrimSpace(deviceName) == "" {
		deviceName = "warte"
	}
	t, _ := theme.Get(theme.Default)
	return &Link{
		linker: linker, device: deviceName,
		styles: theme.NewStyles(t), auto: true,
		icons: theme.Icons(false),
		w:     80, h: 24,
		ContinueAfter: 1500 * time.Millisecond,
	}
}

// WithStyles fixes the theme (by default the screen follows the terminal
// background: charm or charm-light).
func (m *Link) WithStyles(st *theme.Styles) *Link {
	if st != nil {
		m.styles, m.auto = st, false
	}
	return m
}

// WithIcons sets the icon set (Nerd Font or Unicode).
func (m *Link) WithIcons(ic theme.IconSet) *Link { m.icons = ic; return m }

// WithReduceMotion disables the spinner animation.
func (m *Link) WithReduceMotion(on bool) *Link { m.still = on; return m }

// Result reports the linked account once the program has quit: the account
// on success, ErrLinkCanceled when the user quit, or the last error.
func (m *Link) Result() (core.Contact, error) {
	switch m.state {
	case linkDone:
		return m.self, nil
	case linkFailed:
		return core.Contact{}, m.err
	}
	return core.Contact{}, ErrLinkCanceled
}

// Done reports whether the device was linked.
func (m *Link) Done() bool { return m.state == linkDone }

type (
	linkStartedMsg struct {
		gen int
		ch  <-chan core.LinkEvent
		err error
	}
	linkEventMsg struct {
		gen int
		ch  <-chan core.LinkEvent
		ev  core.LinkEvent
	}
	linkClosedMsg struct{ gen int }
	linkTickMsg   struct{ gen int }
	linkQuitMsg   struct{}
)

func (m *Link) Init() tea.Cmd {
	return tea.Batch(tea.RequestBackgroundColor, m.start())
}

// start begins a linking attempt.
func (m *Link) start() tea.Cmd {
	if m.cancel != nil {
		m.cancel()
	}
	m.gen++
	m.ticking = false
	m.state = linkStarting
	m.url, m.qr, m.qrErr, m.err = "", nil, nil, nil
	m.stages = nil
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	gen, linker, device := m.gen, m.linker, m.device
	start := func() tea.Msg {
		if linker == nil {
			return linkStartedMsg{gen: gen, err: errors.New("no linker")}
		}
		ch, err := linker.Link(ctx, device)
		return linkStartedMsg{gen: gen, ch: ch, err: err}
	}
	return tea.Batch(start, m.tickCmd())
}

func (m *Link) tickCmd() tea.Cmd {
	if m.still || m.ticking {
		return nil
	}
	m.ticking = true
	gen := m.gen
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return linkTickMsg{gen: gen} })
}

// next waits for the next event of ch.
func next(gen int, ch <-chan core.LinkEvent) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return linkClosedMsg{gen: gen}
		}
		return linkEventMsg{gen: gen, ch: ch, ev: ev}
	}
}

func (m *Link) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case tea.BackgroundColorMsg:
		if m.auto && !theme.NoColorRequested() {
			m.styles = theme.NewStyles(theme.Auto(msg.IsDark()))
		}
	case linkStartedMsg:
		if msg.gen != m.gen {
			return m, nil
		}
		if msg.err != nil {
			m.fail(msg.err)
			return m, nil
		}
		return m, next(msg.gen, msg.ch)
	case linkEventMsg:
		if msg.gen != m.gen {
			return m, nil
		}
		switch ev := msg.ev.(type) {
		case core.LinkQR:
			m.url = ev.URL
			m.qr, m.qrErr = NewQR([]byte(ev.URL))
			m.state = linkWaiting
		case core.LinkProgress:
			if isScanPrompt(ev.Stage) && (m.state == linkWaiting || m.state == linkStarting) {
				// Backends announce "waiting for the scan" right after the
				// QR code: keep showing the code (our own instructions
				// already say what to do).
				return m, next(msg.gen, msg.ch)
			}
			m.state = linkSyncing
			m.stages = append(m.stages, ev.Stage)
			return m, tea.Batch(next(msg.gen, msg.ch), m.tickCmd())
		case core.LinkDone:
			m.state = linkDone
			m.self = ev.Self
			if m.cancel != nil {
				m.cancel()
			}
			if m.ContinueAfter > 0 {
				return m, tea.Tick(m.ContinueAfter, func(time.Time) tea.Msg { return linkQuitMsg{} })
			}
			return m, nil
		case core.LinkError:
			m.fail(ev.Err)
			return m, nil
		}
		return m, next(msg.gen, msg.ch)
	case linkClosedMsg:
		if msg.gen == m.gen && m.state != linkDone && m.state != linkFailed {
			m.fail(errors.New("the linking session ended unexpectedly"))
		}
	case linkTickMsg:
		if msg.gen != m.gen {
			return m, nil
		}
		m.ticking = false
		if m.state == linkStarting || m.state == linkSyncing {
			m.tick++
			return m, m.tickCmd()
		}
	case linkQuitMsg:
		return m, m.quit()
	case tea.KeyPressMsg:
		switch k := keymap.FromTea(msg); k {
		case "ctrl+c", "q", "esc":
			return m, m.quit()
		case "r", "enter":
			switch m.state {
			case linkFailed:
				return m, m.start()
			case linkDone:
				return m, m.quit()
			}
		case "c":
			if m.url != "" && m.state == linkWaiting {
				return m, tea.SetClipboard(m.url)
			}
		}
	}
	return m, nil
}

// isScanPrompt reports whether a progress stage only asks the user to scan
// the code (the scan has not happened yet), e.g. the backend's "Scan the QR
// code with Signal on your phone …".
func isScanPrompt(stage string) bool {
	s := strings.ToLower(stage)
	return strings.Contains(s, "scan the") || strings.Contains(s, "waiting for scan") ||
		strings.Contains(s, "waiting for the scan")
}

func (m *Link) fail(err error) {
	if err == nil {
		err = errors.New("linking failed")
	}
	m.state = linkFailed
	m.err = err
	if m.cancel != nil {
		m.cancel()
	}
}

func (m *Link) quit() tea.Cmd {
	if m.cancel != nil {
		m.cancel()
	}
	return tea.Quit
}

var spinFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func (m *Link) spinner() string {
	if m.still {
		return "…"
	}
	return spinFrames[m.tick%len(spinFrames)]
}

// View renders the full screen.
func (m *Link) View() tea.View {
	v := tea.NewView(m.Render(m.w, m.h))
	v.AltScreen = true
	if !m.styles.Theme.Mono {
		v.BackgroundColor = m.styles.Theme.Palette.Bg
		v.ForegroundColor = m.styles.Theme.Palette.Fg
	}
	v.WindowTitle = "warte · link device"
	return v
}

// Render draws the screen into w x h cells (every line at most w wide).
func (m *Link) Render(w, h int) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	st := m.styles
	inner := max(w-4, 1)
	var body []string
	brand := st.Title.Render("warte") + muted(st, " · link this device to Signal")
	head := []string{center(brand, w), ""}
	hints := m.hints()

	switch m.state {
	case linkStarting:
		body = []string{center(accent(st, m.spinner())+" Requesting a link code from Signal…", w)}
	case linkWaiting:
		body = m.waitingContent(w, h-1, head)
		head = nil
	case linkSyncing:
		body = append(body, center(muted(st, "Linking this device…"), w), "")
		for i, s := range m.stages {
			mark := st.SuccessText.Render(m.icons.Check)
			if i == len(m.stages)-1 {
				mark = accent(st, m.spinner())
			}
			body = append(body, center(mark+" "+trunc(clean(s), inner-2), w))
		}
		body = append(body, "", center(muted(st, "Keep Signal open on your phone until linking finishes."), w))
	case linkDone:
		name := LinkedName(m.self)
		line := st.SuccessText.Bold(true).Render(m.icons.Check + " Linked")
		if name != "" {
			line += " as " + st.Title.Render(trunc(name, inner-12))
		}
		body = []string{center(line, w), "", center(muted(st, "Starting warte…"), w)}
	case linkFailed:
		body = []string{center(st.ErrorText.Bold(true).Render("✗ Linking failed"), w), ""}
		for _, l := range wrapText(clean(errText(m.err)), inner) {
			body = append(body, center(st.ErrorText.Render(l), w))
		}
		body = append(body, "", center(muted(st, "Press r to try again."), w))
	}

	// Vertically center head+body; hints on the last line.
	content := append(append([]string{}, head...), body...)
	avail := h - 1
	if len(content) > avail {
		// Drop the heading first, then cut.
		content = body
		if len(content) > avail {
			content = content[:max(avail, 0)]
		}
	}
	top := max((avail-len(content))/2, 0)
	lines := make([]string, 0, h)
	for i := 0; i < top; i++ {
		lines = append(lines, "")
	}
	lines = append(lines, content...)
	for len(lines) < h-1 {
		lines = append(lines, "")
	}
	if h > 1 {
		lines = append(lines, center(hints, w))
	}
	return clampLines(lines, w, h)
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	return err.Error()
}

func (m *Link) hints() string {
	st := m.styles
	switch m.state {
	case linkWaiting:
		return st.RenderKeyHints("c", "copy link", "q", "quit")
	case linkFailed:
		return st.RenderKeyHints("r", "retry", "q", "quit")
	case linkDone:
		return st.RenderKeyHints("enter", "continue")
	}
	return st.RenderKeyHints("q", "quit")
}

// waitingContent lays out the QR screen into at most h lines. The QR code
// has priority (largest quiet zone that fits), then the instructions, then
// spacing, the heading and the URL fallback, so a scannable code fits even
// in an 80x24 terminal.
func (m *Link) waitingContent(w, h int, head []string) []string {
	st := m.styles
	inner := max(w-4, 1)
	var instr []string
	for _, l := range wrapText(LinkInstructions, inner) {
		instr = append(instr, center(st.Title.Render(l), w))
	}
	urlLines := chunk(clean(m.url), min(inner, 100))
	var urlBlock []string
	for _, l := range urlLines {
		urlBlock = append(urlBlock, center(subtle(st, l), w))
	}
	if m.qr != nil {
		for quiet := 4; quiet >= 1; quiet-- {
			qw, qh := m.qr.CellSize(quiet)
			if qw > w || qh+len(instr) > h {
				continue
			}
			var qr []string
			for _, l := range m.qr.Render(st, quiet) {
				qr = append(qr, center(l, w))
			}
			room := h - qh - len(instr)
			gap := room >= 1
			if gap {
				room--
			}
			withHead := room >= len(head)
			if withHead {
				room -= len(head)
			}
			withURL := room >= 2+len(urlBlock)
			var out []string
			if withHead {
				out = append(out, head...)
			}
			out = append(out, qr...)
			if gap {
				out = append(out, "")
			}
			out = append(out, instr...)
			if withURL {
				out = append(out, "", center(muted(st, "Or open this link on the phone:"), w))
				out = append(out, urlBlock...)
			}
			return out
		}
	}
	out := append([]string{}, head...)
	switch {
	case m.qr != nil:
		qw, qh := m.qr.CellSize(1)
		out = append(out, center(st.WarningText.Render(trunc(
			"Window too small for the QR code (needs "+itoa(qw)+"×"+itoa(qh+len(instr)+1)+").", inner)), w), "")
	case m.qrErr != nil:
		out = append(out, center(st.ErrorText.Render(trunc("QR: "+clean(m.qrErr.Error()), inner)), w), "")
	}
	out = append(out, instr...)
	out = append(out, "", center(muted(st, "Link:"), w))
	for _, l := range urlLines {
		out = append(out, center(l, w))
	}
	return out
}

// chunk splits s into pieces of at most n runes (URLs have no good break
// points, and equal pieces read better than word-wrapped ones).
func chunk(s string, n int) []string {
	if n <= 0 {
		return nil
	}
	rs := []rune(s)
	var out []string
	for len(rs) > n {
		out = append(out, string(rs[:n]))
		rs = rs[n:]
	}
	if len(rs) > 0 {
		out = append(out, string(rs))
	}
	return out
}

// LinkedName names the account a device was linked to for display: the
// name or username when one is known, otherwise the phone number masked
// ("+49 ••• ••• 42"), never the raw number. "" when nothing is known. The
// result is sanitized.
func LinkedName(c core.Contact) string {
	for _, s := range []string{c.Nickname, c.ContactName, c.ProfileName, c.Username} {
		if s = clean(s); s != "" {
			return s
		}
	}
	return MaskPhone(c.E164)
}

// MaskPhone hides all but the country-code start and the last two digits of
// a phone number: "+4915112345642" → "+49 ••• ••• 42". Numbers too short to
// mask meaningfully are hidden completely.
func MaskPhone(e164 string) string {
	var digits []rune
	for _, r := range e164 {
		if r >= '0' && r <= '9' {
			digits = append(digits, r)
		}
	}
	switch {
	case len(digits) == 0:
		return ""
	case len(digits) < 6:
		return "+••• •••"
	}
	return "+" + string(digits[:2]) + " ••• ••• " + string(digits[len(digits)-2:])
}
