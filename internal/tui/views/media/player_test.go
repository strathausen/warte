package media

import (
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"warte/internal/core"
	"warte/internal/tui/ui"
)

// fakeProc is a controllable Process.
type fakeProc struct {
	mu      sync.Mutex
	path    string
	argv    []string
	paused  bool
	killed  bool
	done    chan error
	pauseOK bool
}

func (p *fakeProc) Pause() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.paused = true
	return nil
}
func (p *fakeProc) Resume() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.paused = false
	return nil
}
func (p *fakeProc) Kill() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.killed {
		p.killed = true
		close(p.done)
	}
}
func (p *fakeProc) Done() <-chan error { return p.done }
func (p *fakeProc) finish() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.killed {
		p.killed = true
		close(p.done)
	}
}

func newPlayerEnv(t *testing.T) (*Player, *[]*fakeProc, *time.Time) {
	t.Helper()
	svc := newSvc(t)
	ctx := newCtx(t, svc, &fakeImages{})
	now := testNow
	ctx.Now = func() time.Time { return now }
	ctx.Config.OpenWith["audio"] = "myplayer -q"
	m := findMsg(t, svc, hasAtt(func(a core.Attachment) bool { return a.VoiceNote }))
	procs := &[]*fakeProc{}
	t.Cleanup(restoreHooks())
	StartAudio = func(argv []string, path string) (Process, error) {
		p := &fakeProc{path: path, argv: argv, done: make(chan error, 1)}
		*procs = append(*procs, p)
		return p, nil
	}
	return NewPlayer(ctx, m, firstAtt(m, func(a core.Attachment) bool { return a.VoiceNote })), procs, &now
}

// step feeds the player's own messages produced by cmd (one level deep,
// not following ticks or process waits).
func step(p *Player, msgs []any) {
	for _, m := range msgs {
		if p.Owns(m) {
			p.Update(m)
		}
	}
}

func TestPlayerLifecycle(t *testing.T) {
	p, procs, now := newPlayerEnv(t)
	if p.State() != PlayerIdle {
		t.Fatal(p.State())
	}
	// Play loads first, then starts.
	cmd := p.Play()
	if p.State() != PlayerLoading {
		t.Fatalf("state %v", p.State())
	}
	loaded := run(cmd)
	if len(loaded) != 1 {
		t.Fatalf("load: %v", loaded)
	}
	_, cmd = p.Update(loaded[0])
	if p.Duration() < 13*time.Second || p.Duration() > 15*time.Second {
		t.Errorf("duration %v, want ~14s", p.Duration())
	}
	if len(p.samples) != waveformSamples {
		t.Errorf("samples: %d", len(p.samples))
	}
	if p.State() != PlayerStarting {
		t.Fatalf("auto-play: %v", p.State())
	}
	started := run(cmd)
	if len(*procs) != 1 {
		t.Fatalf("processes: %d", len(*procs))
	}
	proc := (*procs)[0]
	if proc.argv[0] != "myplayer" || proc.argv[1] != "-q" {
		t.Errorf("argv %v", proc.argv)
	}
	assertMode(t, proc.path, 0o600)
	_, cmd = p.Update(started[0])
	if p.State() != PlayerPlaying || cmd == nil {
		t.Fatalf("state %v", p.State())
	}
	*now = now.Add(4 * time.Second)
	if e := p.Elapsed(); e != 4*time.Second {
		t.Errorf("elapsed %v", e)
	}
	line := ansi.Strip(p.View(60))
	if !strings.Contains(line, "0:04 / 0:14") || !strings.HasPrefix(line, "▶") {
		t.Errorf("view: %q", line)
	}
	if w := ansi.StringWidth(p.View(60)); w != 60 {
		t.Errorf("view width %d", w)
	}
	// Pause freezes the clock.
	p.Toggle()
	if p.State() != PlayerPaused || !proc.paused {
		t.Fatalf("pause: %v %v", p.State(), proc.paused)
	}
	*now = now.Add(10 * time.Second)
	if e := p.Elapsed(); e != 4*time.Second {
		t.Errorf("elapsed while paused %v", e)
	}
	if cmd := p.Toggle(); cmd == nil || p.State() != PlayerPlaying || proc.paused {
		t.Fatalf("resume: %v", p.State())
	}
	// The process ends: finished, temp file removed.
	tracked := p.proc
	proc.finish()
	<-tracked.Done()
	exit := playerExitMsg{seq: p.seq, proc: tracked}
	p.Update(exit)
	if p.State() != PlayerFinished || p.Progress() != 1 {
		t.Errorf("after exit: %v %v", p.State(), p.Progress())
	}
	if _, err := os.Stat(proc.path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("temp file left behind: %v", err)
	}
	// Play again from the start; Stop kills and cleans up.
	step(p, toAny(run(p.Play())))
	if len(*procs) != 2 || p.State() != PlayerPlaying {
		t.Fatalf("replay: %d %v", len(*procs), p.State())
	}
	second := (*procs)[1]
	secondTracked := p.proc
	p.Stop()
	if !second.killed || p.State() != PlayerStopped || p.Elapsed() != 0 {
		t.Errorf("stop: %v %v", second.killed, p.State())
	}
	if _, err := os.Stat(second.path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("temp file left after stop")
	}
	// A late exit of a killed process is ignored.
	p.Update(playerExitMsg{seq: p.seq, proc: secondTracked})
	if p.State() != PlayerStopped {
		t.Errorf("late exit changed state: %v", p.State())
	}
	// Messages of other players are ignored.
	other := playerTickMsg{seq: p.seq + 100}
	if p.Owns(other) {
		t.Error("owns a foreign message")
	}
}

func toAny[T any](in []T) []any {
	out := make([]any, len(in))
	for i, v := range in {
		out[i] = v
	}
	return out
}

func TestPlayerStopDuringStart(t *testing.T) {
	p, procs, _ := newPlayerEnv(t)
	step(p, toAny(run(p.Init())))
	cmd := p.Play()
	p.Stop()
	started := run(cmd)
	step(p, toAny(started))
	if len(*procs) != 1 || !(*procs)[0].killed {
		t.Fatalf("late process not killed")
	}
	if _, err := os.Stat((*procs)[0].path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("late temp file left behind")
	}
	if p.State() != PlayerStopped {
		t.Errorf("state %v", p.State())
	}
}

func TestPlayerNoPlayer(t *testing.T) {
	p, _, _ := newPlayerEnv(t)
	p.ctx.Config.OpenWith = map[string]string{}
	lookPath = func(string) (string, error) { return "", errors.New("nope") }
	step(p, toAny(run(p.Init())))
	p.Play()
	if p.State() != PlayerFailed || !strings.Contains(p.Err(), "no audio player") {
		t.Errorf("state %v err %q", p.State(), p.Err())
	}
	if w := ansi.StringWidth(p.View(40)); w != 40 {
		t.Errorf("width %d", w)
	}
}

func TestPlayerArgs(t *testing.T) {
	t.Cleanup(restoreHooks())
	have := map[string]bool{"afplay": true, "ffplay": true, "paplay": true}
	lookPath = func(n string) (string, error) {
		if have[n] {
			return "/usr/bin/" + n, nil
		}
		return "", errors.New("missing")
	}
	cfg := newCtx(t, nil, nil).Config
	args, err := playerArgs(cfg, core.Attachment{ContentType: "audio/ogg"})
	if err != nil || args[0] != "ffplay" || !contains(args, "-nodisp") {
		t.Errorf("ogg: %v %v", args, err)
	}
	args, _ = playerArgs(cfg, core.Attachment{ContentType: "audio/mpeg"})
	if args[0] != "afplay" && args[0] != "paplay" {
		t.Errorf("mp3: %v", args)
	}
	cfg.OpenWith["audio"] = "vlc --intf dummy"
	args, _ = playerArgs(cfg, core.Attachment{ContentType: "audio/ogg"})
	if strings.Join(args, " ") != "vlc --intf dummy" {
		t.Errorf("configured: %v", args)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func TestPlayerOverlay(t *testing.T) {
	p, procs, _ := newPlayerEnv(t)
	o := &PlayerOverlay{p: p}
	checkView(t, o)
	// Init loads and plays.
	var ov ui.Overlay = o
	cmd := o.Init()
	for i := 0; i < 3 && cmd != nil; i++ {
		var next []any
		for _, m := range run(cmd) {
			next = append(next, m)
		}
		cmd = nil
		for _, m := range next {
			ov, cmd = ov.Update(m)
		}
	}
	if p.State() != PlayerPlaying {
		t.Fatalf("overlay did not start playback: %v", p.State())
	}
	checkView(t, o)
	out := ansi.Strip(o.View(60, 6))
	if !strings.Contains(out, senderName(p.Item().Message)) {
		t.Errorf("sender missing:\n%s", out)
	}
	o.Update(key("space"))
	if p.State() != PlayerPaused {
		t.Errorf("space: %v", p.State())
	}
	o.Update(key("s"))
	if p.State() != PlayerStopped {
		t.Errorf("s: %v", p.State())
	}
	if !o.CaptureKeys() {
		t.Error("player must capture keys")
	}
	if res, _ := o.Update(key("esc")); res != nil {
		t.Error("esc should close")
	}
	for _, pr := range *procs {
		if !pr.killed {
			t.Error("process alive after close")
		}
	}
}

func TestAudioDurationAndWaveform(t *testing.T) {
	if d := audioDuration(make([]byte, 16000), "audio/mpeg"); d != time.Second {
		t.Errorf("mp3 estimate %v", d)
	}
	if d := audioDuration([]byte("junk"), "application/octet-stream"); d != 0 {
		t.Errorf("unknown %v", d)
	}
	if audioWaveform(nil, 10) != nil {
		t.Error("empty waveform")
	}
	w := audioWaveform([]byte{0, 255, 128, 128, 0, 255, 1, 2, 3}, 4)
	if len(w) != 4 {
		t.Fatalf("len %d", len(w))
	}
	for _, v := range w {
		if v < 0.15 || v > 1 {
			t.Errorf("sample out of range: %v", v)
		}
	}
	if fmtClock(75*time.Second) != "1:15" {
		t.Error(fmtClock(75 * time.Second))
	}
}

// The screen lock pauses audio playing in the player dialog.
func TestPlayerOverlayPausesOnScreenLock(t *testing.T) {
	p, procs, _ := newPlayerEnv(t)
	o := &PlayerOverlay{p: p}
	t.Cleanup(p.Close)
	loaded := run(o.Init())
	_, cmd := p.Update(loaded[0])
	started := run(cmd)
	p.Update(started[0])
	if p.State() != PlayerPlaying || len(*procs) != 1 {
		t.Fatalf("not playing: %v", p.State())
	}
	if next, _ := o.Update(ui.ScreenLockMsg{}); next == nil {
		t.Fatal("screen lock closed the player")
	}
	if !pauseSupported {
		if p.State() != PlayerStopped {
			t.Fatalf("state %v, want stopped where pausing is unsupported", p.State())
		}
		return
	}
	if p.State() != PlayerPaused || !(*procs)[0].paused {
		t.Fatalf("state %v, paused %v", p.State(), (*procs)[0].paused)
	}
}
