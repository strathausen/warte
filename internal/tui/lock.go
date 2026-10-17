package tui

import (
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"warte/internal/config"
	"warte/internal/notify"
	"warte/internal/tui/ui"
)

// Screen lock (SPEC §10)
//
// ":lock", <leader>l or screen_lock.idle_minutes of inactivity replace the
// whole UI with a calm lock screen (logo, clock, passphrase prompt). Nothing
// of the account is drawn while locked: no panes, overlays or toasts, kitty
// images are deleted from the terminal, the window title is just "warte",
// notifications degrade to "New message", and nothing is marked read.
// Overlays stay open underneath and come back on unlock.
//
// The passphrase is checked by Options.LockVerifier, or by the App's own lock
// password (lockPassword, set with a two-step prompt the first time you
// lock). Wrong attempts back off exponentially.

type lockStage int

const (
	lockOff        lockStage = iota
	lockLocked               // locked, asking for the passphrase
	lockSetNew               // first lock: choose a lock password (not locked yet)
	lockSetConfirm           // first lock: repeat it
)

// lockMaxBackoff caps the wait after wrong attempts.
const lockMaxBackoff = 5 * time.Minute

// lockIdleCheck is how often the idle timer is checked.
const lockIdleCheck = 15 * time.Second

type screenLock struct {
	stage    lockStage
	input    textinput.Model
	first    string // lock password typed in lockSetNew
	msg      string // error or hint below the prompt
	msgErr   bool
	busy     bool // verifying / storing
	seq      int  // invalidates results of older attempts
	failures int
	until    time.Time // no attempts before this (backoff)
	ticking  bool      // countdown tick scheduled
	clockOn  bool      // clock tick scheduled

	lastActivity time.Time
	idleTicking  bool
	warned       bool // "no lock password" warning shown
}

type (
	lockVerifiedMsg struct {
		seq int
		ok  bool
	}
	lockSetDoneMsg struct {
		seq int
		err error
	}
	lockTickMsg     struct{ clock bool }
	lockIdleTickMsg struct{}
)

func newScreenLock() *screenLock {
	ti := textinput.New()
	ti.Prompt = ""
	ti.CharLimit = 1024
	ti.EchoMode = textinput.EchoPassword
	ti.EchoCharacter = '•'
	return &screenLock{input: ti}
}

// locked reports whether the screen lock hides the UI (including the
// set-password prompt, which replaces the screen too).
func (a *App) locked() bool { return a.lock.stage != lockOff }

// isLocked reports whether the App is locked (not merely setting a password).
func (a *App) isLocked() bool { return a.lock.stage == lockLocked }

func (a *App) lockVerifier() Verifier {
	if a.opts.LockVerifier != nil {
		return a.opts.LockVerifier
	}
	return a.lockFile()
}

func (a *App) lockFile() lockPassword {
	p := a.opts.LockPath
	if p == "" {
		p = defaultLockPath(a.opts.ConfigPath)
	}
	return lockPassword{path: p}
}

// lockAvailable reports whether locking would be possible without first
// setting a lock password.
func (a *App) lockAvailable() bool {
	return a.opts.LockVerifier != nil || a.lockFile().IsSet()
}

// lockScreen locks now (":lock", <leader>l). Without a verifier or stored
// lock password it first asks for a new lock password.
func (a *App) lockScreen() tea.Cmd {
	if a.locked() {
		return nil
	}
	if !a.lockAvailable() {
		l := a.lock
		l.stage = lockSetNew
		l.first, l.msg, l.msgErr, l.busy = "", "", false, false
		l.input.SetValue("")
		return l.input.Focus()
	}
	return a.engageLock()
}

// engageLock switches to the locked state and hides everything.
func (a *App) engageLock() tea.Cmd {
	l := a.lock
	l.stage = lockLocked
	l.first, l.busy = "", false
	l.msg, l.msgErr = "", false
	l.seq++
	l.input.SetValue("")
	a.keys.Flush()
	a.toasts = nil
	a.comp.comp = completion{}
	a.coalescer.SetActiveChat("")
	a.coalescer.SetFocused(false)
	cmds := []tea.Cmd{l.input.Focus(), a.lockClockTick(), a.forwardOverlay(ui.ScreenLockMsg{})}
	if s := a.images.hideAll(); s != "" {
		cmds = append(cmds, tea.Raw(s))
	}
	return tea.Batch(cmds...)
}

// unlock leaves the lock screen.
func (a *App) unlock() tea.Cmd {
	l := a.lock
	l.stage = lockOff
	l.failures, l.until = 0, time.Time{}
	l.msg, l.msgErr, l.busy, l.first = "", false, false, ""
	l.input.SetValue("")
	l.input.Blur()
	l.lastActivity = a.now()
	a.coalescer.SetFocused(a.termFocused)
	a.syncActiveChat()
	// Image rows cached in message blocks point at deleted kitty images.
	a.conv.invalidate()
	a.conv.invalidateMedia()
	return a.markReadIfVisible()
}

// lockKey handles a key press while the lock screen is up.
func (a *App) lockKey(msg tea.KeyPressMsg) tea.Cmd {
	l := a.lock
	switch msg.String() {
	case "ctrl+c":
		// Quitting reveals nothing; it is the same as closing the terminal.
		now := a.now()
		if !a.ctrlCAt.IsZero() && now.Sub(a.ctrlCAt) < 1500*time.Millisecond {
			return a.quit()
		}
		a.ctrlCAt = now
		l.msg, l.msgErr = "Press ctrl+c again to quit", false
		return nil
	case "esc":
		if l.stage == lockSetNew || l.stage == lockSetConfirm {
			l.stage = lockOff
			l.input.SetValue("")
			l.input.Blur()
			l.first = ""
			return nil
		}
		l.input.SetValue("")
		return nil
	case "enter":
		return a.lockSubmit()
	}
	if l.busy {
		return nil
	}
	var cmd tea.Cmd
	l.input, cmd = l.input.Update(msg)
	return cmd
}

// lockPaste inserts pasted text into the passphrase field.
func (a *App) lockPaste(text string) tea.Cmd {
	l := a.lock
	if l.busy {
		return nil
	}
	text = strings.TrimRight(text, "\r\n")
	if strings.ContainsAny(text, "\r\n") {
		return nil
	}
	l.input.SetValue(l.input.Value() + text)
	l.input.CursorEnd()
	return nil
}

// lockSubmit handles enter on the lock screen.
func (a *App) lockSubmit() tea.Cmd {
	l := a.lock
	if l.busy {
		return nil
	}
	pw := l.input.Value()
	switch l.stage {
	case lockLocked:
		if pw == "" {
			return nil
		}
		if wait := l.until.Sub(a.now()); wait > 0 {
			l.input.SetValue("")
			l.msg, l.msgErr = "Try again in "+waitText(wait), true
			return a.lockCountdown()
		}
		l.busy = true
		l.msg, l.msgErr = "Checking…", false
		l.seq++
		seq, v := l.seq, a.lockVerifier()
		return func() tea.Msg { return lockVerifiedMsg{seq: seq, ok: v.Verify(pw)} }
	case lockSetNew:
		if utf8.RuneCountInString(pw) < 4 {
			l.msg, l.msgErr = "Use at least 4 characters", true
			return nil
		}
		l.first = pw
		l.stage = lockSetConfirm
		l.msg, l.msgErr = "", false
		l.input.SetValue("")
		return nil
	case lockSetConfirm:
		if pw != l.first {
			l.stage = lockSetNew
			l.first = ""
			l.input.SetValue("")
			l.msg, l.msgErr = "The passwords did not match. Try again.", true
			return nil
		}
		l.busy = true
		l.msg, l.msgErr = "Saving…", false
		l.seq++
		seq, f := l.seq, a.lockFile()
		return func() tea.Msg { return lockSetDoneMsg{seq: seq, err: f.Set(pw)} }
	}
	return nil
}

// handleLockMsg handles the lock's own messages; ok is false for others.
func (a *App) handleLockMsg(msg tea.Msg) (tea.Cmd, bool) {
	l := a.lock
	switch msg := msg.(type) {
	case lockVerifiedMsg:
		if msg.seq != l.seq || l.stage != lockLocked {
			return nil, true
		}
		l.busy = false
		l.input.SetValue("")
		if msg.ok {
			return a.unlock(), true
		}
		l.failures++
		wait := lockBackoff(l.failures)
		l.until = a.now().Add(wait)
		l.msg, l.msgErr = "Wrong passphrase. Try again in "+waitText(wait), true
		return a.lockCountdown(), true
	case lockSetDoneMsg:
		if msg.seq != l.seq || l.stage != lockSetConfirm {
			return nil, true
		}
		l.busy = false
		l.first = ""
		if msg.err != nil {
			l.stage = lockOff
			l.input.Blur()
			return a.toast(ui.ToastError, "lock password: "+msg.err.Error()), true
		}
		return a.engageLock(), true
	case lockTickMsg:
		if msg.clock {
			l.clockOn = false
			return a.lockClockTick(), true
		}
		l.ticking = false
		if l.stage != lockLocked {
			return nil, true
		}
		if wait := l.until.Sub(a.now()); wait > 0 {
			if l.msgErr && strings.Contains(l.msg, "Try again in") {
				prefix := l.msg[:strings.Index(l.msg, "Try again in")]
				l.msg = prefix + "Try again in " + waitText(wait)
			}
			return a.lockCountdown(), true
		}
		if l.msgErr && strings.Contains(l.msg, "Try again in") {
			l.msg, l.msgErr = "Wrong passphrase", true
		}
		return nil, true
	case lockIdleTickMsg:
		l.idleTicking = false
		return a.checkIdle(), true
	}
	return nil, false
}

// lockCountdown re-renders every second while a backoff runs.
func (a *App) lockCountdown() tea.Cmd {
	if a.lock.ticking {
		return nil
	}
	a.lock.ticking = true
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return lockTickMsg{} })
}

// lockClockTick keeps the clock on the lock screen current.
func (a *App) lockClockTick() tea.Cmd {
	if a.lock.clockOn || a.lock.stage != lockLocked {
		return nil
	}
	a.lock.clockOn = true
	now := a.now()
	d := time.Minute - time.Duration(now.Second())*time.Second - time.Duration(now.Nanosecond())
	if d <= 0 || d > time.Minute {
		d = time.Minute
	}
	return tea.Tick(d, func(time.Time) tea.Msg { return lockTickMsg{clock: true} })
}

// lockBackoff is the wait after n wrong attempts: 1s, 2s, 4s, … up to 5
// minutes.
func lockBackoff(n int) time.Duration {
	if n <= 0 {
		return 0
	}
	d := time.Second
	for i := 1; i < n && d < lockMaxBackoff; i++ {
		d *= 2
	}
	return min(d, lockMaxBackoff)
}

func waitText(d time.Duration) string {
	s := int((d + time.Second - 1) / time.Second)
	if s >= 60 {
		m := (s + 59) / 60
		return strconv.Itoa(m) + " min"
	}
	return strconv.Itoa(max(s, 1)) + "s"
}

// ---- idle lock --------------------------------------------------------------

// noteActivity records user input for the idle timer.
func (a *App) noteActivity() { a.lock.lastActivity = a.now() }

// idleTimer starts the idle check when screen_lock is enabled.
func (a *App) idleTimer() tea.Cmd {
	if !a.cfg.ScreenLock.Enabled || a.lock.idleTicking {
		return nil
	}
	a.lock.idleTicking = true
	if a.lock.lastActivity.IsZero() {
		a.lock.lastActivity = a.now()
	}
	return tea.Tick(lockIdleCheck, func(time.Time) tea.Msg { return lockIdleTickMsg{} })
}

// checkIdle locks after screen_lock.idle_minutes without input.
func (a *App) checkIdle() tea.Cmd {
	if !a.cfg.ScreenLock.Enabled {
		return nil
	}
	idle := time.Duration(max(a.cfg.ScreenLock.IdleMinutes, config.MinIdleMinutes)) * time.Minute
	var cmd tea.Cmd
	if !a.locked() && a.now().Sub(a.lock.lastActivity) >= idle {
		if a.lockAvailable() {
			cmd = a.engageLock()
		} else if !a.lock.warned {
			a.lock.warned = true
			cmd = a.toast(ui.ToastWarning, "Screen lock: set a lock password with :lock first")
		}
	}
	return tea.Batch(cmd, a.idleTimer())
}

// lockWarning is the startup warning when idle lock can't work yet.
func (a *App) lockWarning() tea.Cmd {
	if !a.cfg.ScreenLock.Enabled || a.lockAvailable() {
		return nil
	}
	a.lock.warned = true
	return ui.Toast(ui.ToastWarning, "Screen lock is on but no lock password is set: run :lock once to set one")
}

// ---- notifications while locked ---------------------------------------------

// lockedNotification strips names and content while the screen is locked.
func (a *App) lockedNotification(n notify.Notification) notify.Notification {
	if !a.isLocked() {
		return n
	}
	n.Title = config.AppName
	if n.Urgency == notify.UrgencyCritical {
		n.Body = "Incoming call"
	} else {
		n.Body = "New message"
	}
	return n
}

// ---- rendering ----------------------------------------------------------------

// lockLogo is a small watchtower (a "Warte").
var lockLogo = []string{
	"▄ ▄ ▄ ▄",
	"▀█████▀",
	" ██▀██ ",
	" █████ ",
	"▄█████▄",
}

// renderLock draws the lock (or set-password) screen.
func (a *App) renderLock(W, H int) []string {
	st := a.styles
	l := a.lock
	var title, hint string
	switch l.stage {
	case lockSetNew:
		title = "Set a lock password"
		hint = st.RenderKeyHints("enter", "next", "esc", "cancel")
	case lockSetConfirm:
		title = "Repeat the lock password"
		hint = st.RenderKeyHints("enter", "lock", "esc", "cancel")
	default:
		title = "Locked — enter passphrase"
		hint = st.RenderKeyHints("enter", "unlock", "ctrl+c ctrl+c", "quit")
	}
	switch l.stage {
	case lockSetNew:
		l.input.Placeholder = "new lock password"
	case lockSetConfirm:
		l.input.Placeholder = "the same again"
	default:
		l.input.Placeholder = "passphrase"
	}
	inW := clamp(W-8, 8, 32)
	l.input.SetWidth(inW)
	field := l.input.View()
	if fw := width(field); fw < inW+1 {
		field += spaces(inW + 1 - fw)
	}
	field = st.Subtitle.Render("› ") + field

	var body []string
	if H >= 16 {
		for _, s := range lockLogo {
			body = append(body, st.Title.Render(s))
		}
		body = append(body, "")
	}
	body = append(body, st.Title.Render(config.AppName))
	if l.stage == lockLocked {
		body = append(body, st.Subtitle.Render(a.tf.Clock(a.now())))
	}
	body = append(body, "", st.App.Render(title), "", field)
	switch {
	case l.msg != "" && l.msgErr:
		body = append(body, "", st.ErrorText.Render(clean(l.msg)))
	case l.msg != "":
		body = append(body, "", st.InfoText.Render(clean(l.msg)))
	case l.stage == lockSetNew:
		body = append(body, "", st.Subtitle.Render("Asked for when warte is locked"))
	default:
		body = append(body, "")
	}
	body = append(body, "", hint)

	out := make([]string, H)
	top := max((H-len(body))/2, 0)
	for i := range out {
		out[i] = spaces(W)
		if j := i - top; j >= 0 && j < len(body) {
			out[i] = centerText(body[j], W)
		}
	}
	return out
}
