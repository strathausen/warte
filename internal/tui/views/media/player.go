package media

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"warte/internal/core"
	"warte/internal/termimg"
	"warte/internal/tui/keymap"
	"warte/internal/tui/ui"
)

// KeyContextPlayer is the keymap context of the player overlay. The
// overlay captures keys (space would otherwise start a leader sequence).
const KeyContextPlayer = "media.player"

// PlayerState is the playback state of a Player.
type PlayerState int

const (
	PlayerIdle     PlayerState = iota // nothing loaded or played yet
	PlayerLoading                     // fetching the attachment
	PlayerStarting                    // launching the external player
	PlayerPlaying                     // playing
	PlayerPaused                      // paused (process stopped)
	PlayerStopped                     // stopped by the user
	PlayerFinished                    // played to the end
	PlayerFailed                      // see Err
)

func (s PlayerState) String() string {
	return [...]string{"idle", "loading", "starting", "playing", "paused", "stopped", "finished", "failed"}[s]
}

// Process is a running external audio player.
type Process interface {
	// Pause and Resume suspend and continue playback.
	Pause() error
	Resume() error
	// Kill stops playback.
	Kill()
	// Done is closed (after sending the exit error, if any) when the
	// process exits.
	Done() <-chan error
}

// StartAudio launches an external player for the file at path. It is a
// variable so tests (and alternative backends) can replace it.
var StartAudio = func(argv []string, path string) (Process, error) {
	cmd := exec.Command(argv[0], append(argv[1:], path)...) //nolint:gosec // user-configured player
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &execProcess{cmd: cmd, done: make(chan error, 1)}
	go func() {
		p.done <- cmd.Wait()
		close(p.done)
	}()
	return p, nil
}

type execProcess struct {
	cmd  *exec.Cmd
	done chan error
	once sync.Once
}

func (p *execProcess) Pause() error       { return signalPause(p.cmd.Process) }
func (p *execProcess) Resume() error      { return signalResume(p.cmd.Process) }
func (p *execProcess) Done() <-chan error { return p.done }
func (p *execProcess) Kill() {
	p.once.Do(func() {
		_ = signalResume(p.cmd.Process) // a stopped process must run to die cleanly
		_ = p.cmd.Process.Kill()
	})
}

var playerSeq atomic.Int64

// Live external players. A Player whose overlay is dropped without Close
// (for example when the App pops it on a click outside) would otherwise
// keep playing; the registry lets a new playback stop the old one and lets
// the App stop everything on exit (StopAll).
var (
	liveMu    sync.Mutex
	liveProcs = map[*trackedProcess]struct{}{}
)

// trackedProcess wraps a Process: it removes the temporary audio file as
// soon as the process exits (even when nobody reads Done any more) and is
// registered in liveProcs while it runs.
type trackedProcess struct {
	Process
	tmp    string
	done   chan error
	once   sync.Once
	killed atomic.Bool
}

func track(p Process, tmp string) *trackedProcess {
	t := &trackedProcess{Process: p, tmp: tmp, done: make(chan error, 1)}
	liveMu.Lock()
	liveProcs[t] = struct{}{}
	liveMu.Unlock()
	go func() {
		err := <-p.Done()
		t.cleanup()
		t.done <- err
		close(t.done)
	}()
	return t
}

func (t *trackedProcess) cleanup() {
	t.once.Do(func() {
		if t.tmp != "" {
			_ = os.Remove(t.tmp)
		}
		liveMu.Lock()
		delete(liveProcs, t)
		liveMu.Unlock()
	})
}

func (t *trackedProcess) Done() <-chan error { return t.done }

func (t *trackedProcess) Kill() {
	t.killed.Store(true)
	t.Process.Kill()
	t.cleanup()
}

// StopAll stops every external audio player started by this package and
// removes their temporary files, including decrypted copies handed to an
// external opener that are still waiting for their timer. Call it when the App exits (or when it
// drops a player overlay without closing it).
func StopAll() {
	liveMu.Lock()
	procs := make([]*trackedProcess, 0, len(liveProcs))
	for p := range liveProcs {
		procs = append(procs, p)
	}
	liveMu.Unlock()
	for _, p := range procs {
		p.Kill()
	}
	removeAllPending()
}

// Player plays an audio attachment (voice note or audio file) through an
// external program and renders an inline braille waveform with progress.
// It is a small model meant to be embedded (the conversation can keep one
// per voice note) or shown through PlayerOverlay.
//
// Feed it every message (it ignores messages of other players; Owns tells
// whether a message belongs to it). Call Close when discarding it: that
// stops playback and removes the temporary file.
type Player struct {
	ctx ui.Context
	it  Item
	seq int64

	state    PlayerState
	err      string
	data     []byte
	samples  []float32
	duration time.Duration

	elapsed   time.Duration // played before the current segment
	startedAt time.Time     // start of the current playing segment
	autoPlay  bool          // play once loaded

	proc Process
	tmp  string
	gen  int // invalidates ticks and exit messages
}

type (
	playerLoadedMsg struct {
		seq      int64
		data     []byte
		samples  []float32
		duration time.Duration
		err      error
	}
	playerStartedMsg struct {
		seq  int64
		gen  int
		proc Process
		tmp  string
		err  error
	}
	playerExitMsg struct {
		seq  int64
		proc Process
		err  error
	}
	playerTickMsg struct {
		seq int64
		gen int
	}
)

// NewPlayer creates a player for an audio attachment of m.
func NewPlayer(ctx ui.Context, m core.Message, att core.Attachment) *Player {
	return &Player{ctx: ctx, it: Item{Message: m, Attachment: att}, seq: playerSeq.Add(1)}
}

// Item returns the attachment the player plays.
func (p *Player) Item() Item { return p.it }

// State returns the playback state.
func (p *Player) State() PlayerState { return p.state }

// Err returns the last error text ("" if none).
func (p *Player) Err() string { return p.err }

// Duration returns the length of the audio (0 while unknown).
func (p *Player) Duration() time.Duration { return p.duration }

// Elapsed returns the playing position.
func (p *Player) Elapsed() time.Duration {
	e := p.elapsed
	if p.state == PlayerPlaying && !p.startedAt.IsZero() {
		e += p.now().Sub(p.startedAt)
	}
	if p.state == PlayerFinished && p.duration > 0 {
		e = p.duration
	}
	if p.duration > 0 {
		e = min(e, p.duration)
	}
	return max(e, 0)
}

// Progress returns the position as a fraction 0..1 (0 when unknown).
func (p *Player) Progress() float64 {
	if p.duration <= 0 {
		if p.state == PlayerFinished {
			return 1
		}
		return 0
	}
	return min(float64(p.Elapsed())/float64(p.duration), 1)
}

func (p *Player) now() time.Time {
	if p.ctx.Now != nil {
		return p.ctx.Now()
	}
	return time.Now()
}

// Owns reports whether msg is one of this player's internal messages.
func (p *Player) Owns(msg tea.Msg) bool {
	switch m := msg.(type) {
	case playerLoadedMsg:
		return m.seq == p.seq
	case playerStartedMsg:
		return m.seq == p.seq
	case playerExitMsg:
		return m.seq == p.seq
	case playerTickMsg:
		return m.seq == p.seq
	}
	return false
}

// Init loads the audio (for the waveform and duration) without playing.
func (p *Player) Init() tea.Cmd {
	if p.data != nil || p.state == PlayerLoading {
		return nil
	}
	p.state = PlayerLoading
	return p.load()
}

func (p *Player) load() tea.Cmd {
	svc := p.ctx.Svc
	it := p.it
	seq := p.seq
	return func() tea.Msg {
		if svc == nil {
			return playerLoadedMsg{seq: seq, err: errors.New("no service")}
		}
		c, cancel := opCtx()
		defer cancel()
		data, err := svc.Attachment(c, it.Message.ID, it.Attachment.ID)
		if err != nil {
			return playerLoadedMsg{seq: seq, err: err}
		}
		return playerLoadedMsg{
			seq:      seq,
			data:     data,
			samples:  audioWaveform(data, waveformSamples),
			duration: audioDuration(data, it.Attachment.ContentType),
		}
	}
}

// Toggle plays, pauses or resumes.
func (p *Player) Toggle() tea.Cmd {
	switch p.state {
	case PlayerPlaying:
		return p.Pause()
	case PlayerPaused:
		return p.Resume()
	case PlayerLoading, PlayerStarting:
		return nil
	}
	return p.Play()
}

// Play starts playback from the beginning (loading the audio first if
// needed).
func (p *Player) Play() tea.Cmd {
	if p.it.Message.ViewOnce {
		return toast(ui.ToastWarning, "View-once media can only be viewed inside warte")
	}
	p.kill()
	p.err = ""
	if p.data == nil {
		p.autoPlay = true
		if p.state == PlayerLoading {
			return nil
		}
		p.state = PlayerLoading
		return p.load()
	}
	argv, err := playerArgs(p.ctx.Config, p.it.Attachment)
	if err != nil {
		p.fail(err)
		return nil
	}
	p.state = PlayerStarting
	p.elapsed = 0
	p.gen++
	gen, seq, data := p.gen, p.seq, p.data
	ext := filepath.Ext(fileName(p.it))
	return func() tea.Msg {
		path, err := writeTemp(data, ext)
		if err != nil {
			return playerStartedMsg{seq: seq, gen: gen, err: err}
		}
		// Only one voice note plays at a time (this also stops players whose
		// overlay was dropped without Close).
		StopAll()
		proc, err := StartAudio(argv, path)
		if err != nil {
			_ = os.Remove(path)
			return playerStartedMsg{seq: seq, gen: gen, err: err}
		}
		return playerStartedMsg{seq: seq, gen: gen, proc: track(proc, path), tmp: path}
	}
}

// Pause suspends playback (or stops it where pausing is unsupported).
func (p *Player) Pause() tea.Cmd {
	if p.state != PlayerPlaying || p.proc == nil {
		return nil
	}
	if !pauseSupported {
		p.Stop()
		return nil
	}
	if err := p.proc.Pause(); err != nil {
		p.Stop()
		return nil
	}
	p.elapsed = p.Elapsed()
	p.startedAt = time.Time{}
	p.state = PlayerPaused
	return nil
}

// Resume continues after Pause.
func (p *Player) Resume() tea.Cmd {
	if p.state != PlayerPaused || p.proc == nil {
		return nil
	}
	if err := p.proc.Resume(); err != nil {
		p.fail(err)
		p.kill()
		return nil
	}
	p.state = PlayerPlaying
	p.startedAt = p.now()
	p.gen++
	return p.tick()
}

// Stop ends playback and rewinds.
func (p *Player) Stop() {
	if p.state == PlayerPlaying || p.state == PlayerPaused || p.state == PlayerStarting {
		p.state = PlayerStopped
	}
	p.autoPlay = false
	p.kill()
	p.elapsed = 0
}

// Close stops playback and deletes the temporary file. Call it when the
// player is discarded.
func (p *Player) Close() {
	p.Stop()
}

// kill terminates the process and removes the temp file.
func (p *Player) kill() {
	p.gen++
	if p.proc != nil {
		p.proc.Kill()
		p.proc = nil
	}
	if p.tmp != "" {
		_ = os.Remove(p.tmp)
		p.tmp = ""
	}
	p.startedAt = time.Time{}
}

func (p *Player) fail(err error) {
	p.state = PlayerFailed
	p.err = clean(err.Error())
}

func (p *Player) tick() tea.Cmd {
	d := 100 * time.Millisecond
	if p.ctx.Config.ReduceMotion {
		d = time.Second
	}
	seq, gen := p.seq, p.gen
	return tea.Tick(d, func(time.Time) tea.Msg { return playerTickMsg{seq: seq, gen: gen} })
}

func waitExit(seq int64, proc Process) tea.Cmd {
	return func() tea.Msg {
		err, ok := <-proc.Done()
		if !ok {
			err = nil
		}
		return playerExitMsg{seq: seq, proc: proc, err: err}
	}
}

// Update handles the player's own messages. Messages of other players and
// unrelated messages are ignored.
func (p *Player) Update(msg tea.Msg) (*Player, tea.Cmd) {
	if !p.Owns(msg) {
		return p, nil
	}
	switch m := msg.(type) {
	case playerLoadedMsg:
		if m.err != nil {
			p.fail(m.err)
			p.autoPlay = false
			return p, nil
		}
		p.data, p.samples, p.duration = m.data, m.samples, m.duration
		if p.state == PlayerLoading {
			p.state = PlayerIdle
		}
		if p.autoPlay {
			p.autoPlay = false
			return p, p.Play()
		}
	case playerStartedMsg:
		if m.gen != p.gen || p.state != PlayerStarting {
			// Stopped meanwhile: discard the late process.
			if m.proc != nil {
				m.proc.Kill()
			}
			if m.tmp != "" {
				_ = os.Remove(m.tmp)
			}
			return p, nil
		}
		if m.err != nil {
			p.fail(m.err)
			return p, nil
		}
		p.proc, p.tmp = m.proc, m.tmp
		p.state = PlayerPlaying
		p.startedAt = p.now()
		return p, tea.Batch(p.tick(), waitExit(p.seq, m.proc))
	case playerExitMsg:
		if p.proc == nil || p.proc != m.proc {
			return p, nil // a process we killed already
		}
		p.proc = nil
		if p.tmp != "" {
			_ = os.Remove(p.tmp)
			p.tmp = ""
		}
		if tp, ok := m.proc.(*trackedProcess); ok && tp.killed.Load() {
			// Stopped from elsewhere (another player started, StopAll).
			p.state = PlayerStopped
			p.elapsed, p.startedAt = 0, time.Time{}
			return p, nil
		}
		if m.err != nil && p.state == PlayerPlaying && p.Elapsed() < time.Second {
			p.fail(fmt.Errorf("player exited: %w", m.err))
			return p, nil
		}
		if p.state == PlayerPlaying || p.state == PlayerPaused {
			p.state = PlayerFinished
			p.elapsed = p.duration
			p.startedAt = time.Time{}
		}
	case playerTickMsg:
		if m.gen != p.gen || p.state != PlayerPlaying {
			return p, nil
		}
		return p, p.tick()
	}
	return p, nil
}

// fmtClock formats a duration as m:ss.
func fmtClock(d time.Duration) string {
	s := int(d.Round(time.Second) / time.Second)
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// stateIcon is the glyph in front of the waveform.
func (p *Player) stateIcon() string {
	switch p.state {
	case PlayerPlaying:
		return "▶"
	case PlayerPaused:
		return "⏸"
	case PlayerLoading, PlayerStarting:
		return "…"
	case PlayerFailed:
		return "!"
	}
	return "▷"
}

// timeText is "0:04 / 0:14" (or just the elapsed/total time).
func (p *Player) timeText() string {
	switch {
	case p.duration > 0 && (p.state == PlayerPlaying || p.state == PlayerPaused):
		return fmtClock(p.Elapsed()) + " / " + fmtClock(p.duration)
	case p.duration > 0:
		return fmtClock(p.duration)
	case p.state == PlayerPlaying || p.state == PlayerPaused:
		return fmtClock(p.Elapsed())
	}
	return "-:--"
}

// waveRows renders the waveform w cells wide and h rows tall, the played
// part in the accent color.
func (p *Player) waveRows(w, h int) []string {
	st := p.ctx.Styles
	pal := st.Theme.Palette
	samples := p.samples
	if len(samples) == 0 {
		samples = []float32{0}
	}
	var rows []string
	if h <= 1 {
		rows = []string{termimg.Waveform(samples, w)}
	} else {
		rows = termimg.WaveformRows(samples, w, h)
	}
	played := int(p.Progress()*float64(w) + 0.5)
	if p.state == PlayerIdle || p.state == PlayerStopped || p.state == PlayerLoading || p.state == PlayerFailed {
		played = 0
	}
	on := lipgloss.NewStyle().Foreground(pal.Accent)
	off := lipgloss.NewStyle().Foreground(pal.FgSubtle)
	if st.Theme.Mono {
		on = lipgloss.NewStyle().Bold(true)
		off = lipgloss.NewStyle().Faint(true)
	}
	for i, r := range rows {
		rs := []rune(r)
		k := clampInt(played, 0, len(rs))
		rows[i] = on.Render(string(rs[:k])) + off.Render(string(rs[k:]))
	}
	return rows
}

// View renders the one-line inline player ("▶ ⣀⣤⣶⣿… 0:04 / 0:14"),
// exactly w cells wide.
func (p *Player) View(w int) string {
	st := p.ctx.Styles
	if w <= 0 {
		return ""
	}
	icon := st.Title.Render(p.stateIcon())
	t := st.Timestamp.Render(p.timeText())
	if p.state == PlayerFailed && p.err != "" {
		return fit(icon+" "+st.ErrorText.Render(ellipsis(p.err, max(w-2, 1))), w)
	}
	ww := w - width(icon) - width(t) - 2
	if ww < 4 {
		return fit(icon+" "+t, w)
	}
	return fit(icon+" "+p.waveRows(ww, 1)[0]+" "+t, w)
}

// ViewRows renders a taller waveform (h rows) above a status line, each
// line exactly w cells wide.
func (p *Player) ViewRows(w, h int) []string {
	if h <= 1 {
		return []string{p.View(w)}
	}
	st := p.ctx.Styles
	out := make([]string, 0, h)
	for _, r := range p.waveRows(max(w, 1), h-1) {
		out = append(out, fit(r, w))
	}
	status := p.stateIcon() + " " + p.timeText()
	switch p.state {
	case PlayerFinished:
		status = "✓ played · " + p.timeText()
	case PlayerStopped:
		status = "■ stopped · " + p.timeText()
	case PlayerStarting:
		status = "… starting"
	case PlayerFailed:
		status = st.ErrorText.Render("! " + p.err)
	case PlayerLoading:
		status = "loading…"
	}
	return append(out, fit(st.Subtitle.Render(ellipsis(status, w)), w))
}

// ---- overlay -----------------------------------------------------------------------

// PlayerOverlay shows a Player in a small dialog.
type PlayerOverlay struct {
	p *Player
}

// NewPlayerOverlay builds the player dialog for an audio attachment. It
// starts playing as soon as it opens.
func NewPlayerOverlay(ctx ui.Context, m core.Message, att core.Attachment) *PlayerOverlay {
	return &PlayerOverlay{p: NewPlayer(ctx, m, att)}
}

// Player returns the embedded player.
func (o *PlayerOverlay) Player() *Player { return o.p }

// Init loads and starts playback.
func (o *PlayerOverlay) Init() tea.Cmd { return o.p.Play() }

// Title implements ui.Overlay.
func (o *PlayerOverlay) Title() string {
	t := kindLabel(o.p.it.Attachment)
	if n := strings.TrimSpace(o.p.it.Attachment.FileName); n != "" && !o.p.it.Attachment.VoiceNote {
		t += " · " + clean(n)
	}
	return t
}

// KeyContext implements ui.Overlay.
func (o *PlayerOverlay) KeyContext() string { return KeyContextPlayer }

// CaptureKeys implements ui.KeyCapturer.
func (o *PlayerOverlay) CaptureKeys() bool { return true }

// Size implements ui.Overlay.
func (o *PlayerOverlay) Size(maxW, maxH int) (int, int) { return min(maxW, 64), min(maxH, 6) }

// Footer implements ui.Footer.
func (o *PlayerOverlay) Footer() string {
	verb := "play"
	if o.p.state == PlayerPlaying {
		verb = "pause"
	}
	return o.p.ctx.Styles.RenderKeyHints("space", verb, "s", "stop", "o", "open", "esc", "close")
}

// Dismiss implements ui.Dismisser: playback stops when the App closes the
// dialog from outside.
func (o *PlayerOverlay) Dismiss() tea.Cmd {
	o.p.Close()
	return nil
}

// Update implements ui.Overlay.
func (o *PlayerOverlay) Update(msg tea.Msg) (ui.Overlay, tea.Cmd) {
	if o.p.Owns(msg) {
		_, cmd := o.p.Update(msg)
		return o, cmd
	}
	switch m := msg.(type) {
	case ui.ScreenLockMsg:
		return o, o.p.Pause()
	case tea.KeyPressMsg:
		switch m.String() {
		case "space", "enter", "p", "k":
			return o, o.p.Toggle()
		case "s", "x":
			o.p.Stop()
		case "r", "0":
			return o, o.p.Play()
		case "o":
			o.p.Stop()
			return o, openExternal(o.p.ctx, o.p.it)
		case "q", "esc", "ctrl+c":
			o.p.Close()
			return nil, nil
		}
	case ui.ActionMsg: // not reached while capturing, but keep it usable
		switch m.Action {
		case keymap.ActItemSelect:
			return o, o.p.Toggle()
		case keymap.ActBack:
			o.p.Close()
			return nil, nil
		}
	case tea.MouseClickMsg:
		return o, o.p.Toggle()
	case ui.EventMsg:
		if rm, ok := m.Event.(core.MessageRemoved); ok && rm.ID == o.p.it.Message.ID {
			o.p.Close()
			return nil, nil
		}
	}
	return o, nil
}

// View implements ui.Overlay.
func (o *PlayerOverlay) View(w, h int) string {
	st := o.p.ctx.Styles
	m := o.p.it.Message
	head := st.SenderStyle(string(m.ID.Author)).Render(senderName(m))
	if d := dateLine(o.p.ctx.Config, m.SentAt); d != "" {
		head += st.KeyHintSep.Render(" · ") + st.Timestamp.Render(d)
	}
	lines := []string{ellipsis(head, w)}
	if h >= 4 {
		lines = append(lines, "")
	}
	rest := h - len(lines)
	if rest > 0 {
		lines = append(lines, o.p.ViewRows(w, rest)...)
	}
	return strings.Join(fitLines(lines, w, h), "\n")
}
