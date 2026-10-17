package notify

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// envMap returns a getenv func backed by m.
func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestParseMethod(t *testing.T) {
	tests := []struct {
		in      string
		want    Method
		wantErr bool
	}{
		{"", MethodAuto, false},
		{"auto", MethodAuto, false},
		{" AUTO ", MethodAuto, false},
		{"osc", MethodOSC, false},
		{"osc9", MethodOSC9, false},
		{"osc-99", MethodOSC99, false},
		{"kitty", MethodOSC99, false},
		{"osc777", MethodOSC777, false},
		{"native", MethodNative, false},
		{"bell", MethodBell, false},
		{"off", MethodOff, false},
		{"none", MethodOff, false},
		{"growl", MethodAuto, true},
	}
	for _, tt := range tests {
		got, err := ParseMethod(tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("ParseMethod(%q) = %v, %v; want %v, err=%v", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
	for m := MethodAuto; m <= MethodOff; m++ {
		back, err := ParseMethod(m.String())
		if err != nil || back != m {
			t.Errorf("round trip %v: %v, %v", m, back, err)
		}
	}
	if s := Method(42).String(); s != "Method(42)" {
		t.Errorf("unknown String = %q", s)
	}
}

func TestDetectMatrix(t *testing.T) {
	gui := map[string]string{"DISPLAY": ":0"}
	with := func(base map[string]string, kv ...string) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	tests := []struct {
		name     string
		env      map[string]string
		goos     string
		want     Method
		wantTerm Terminal
	}{
		{"kitty window id", with(nil, "KITTY_WINDOW_ID", "1"), "linux", MethodOSC99, TerminalKitty},
		{"kitty TERM", with(nil, "TERM", "xterm-kitty"), "darwin", MethodOSC99, TerminalKitty},
		{"kitty in tmux", with(nil, "TERM", "tmux-256color", "TERM_PROGRAM", "tmux", "TMUX", "/tmp/x", "KITTY_WINDOW_ID", "3"), "linux", MethodOSC99, TerminalKitty},
		{"iterm2", with(nil, "TERM_PROGRAM", "iTerm.app"), "darwin", MethodOSC9, TerminalITerm2},
		{"iterm2 LC_TERMINAL in tmux", with(nil, "TERM_PROGRAM", "tmux", "LC_TERMINAL", "iTerm2"), "darwin", MethodOSC9, TerminalITerm2},
		{"wezterm", with(nil, "TERM_PROGRAM", "WezTerm"), "linux", MethodOSC9, TerminalWezTerm},
		{"wezterm pane", with(nil, "WEZTERM_PANE", "0"), "linux", MethodOSC9, TerminalWezTerm},
		{"windows terminal", with(nil, "WT_SESSION", "abc"), "windows", MethodOSC9, TerminalWindowsTerminal},
		{"ghostty", with(nil, "TERM_PROGRAM", "ghostty"), "darwin", MethodOSC777, TerminalGhostty},
		{"ghostty TERM", with(nil, "TERM", "xterm-ghostty"), "linux", MethodOSC777, TerminalGhostty},
		{"ghostty resources in tmux", with(nil, "TMUX", "x", "GHOSTTY_RESOURCES_DIR", "/a"), "linux", MethodOSC777, TerminalGhostty},
		{"foot", with(nil, "TERM", "foot"), "linux", MethodOSC777, TerminalFoot},
		{"foot-extra", with(nil, "TERM", "foot-extra"), "linux", MethodOSC777, TerminalFoot},
		{"urxvt", with(nil, "TERM", "rxvt-unicode-256color"), "linux", MethodOSC777, TerminalRxvt},
		{"apple terminal → native", with(nil, "TERM_PROGRAM", "Apple_Terminal"), "darwin", MethodNative, TerminalUnknown},
		{"xterm with display → native", with(gui, "TERM", "xterm-256color"), "linux", MethodNative, TerminalUnknown},
		{"wayland → native", with(nil, "WAYLAND_DISPLAY", "wayland-0"), "linux", MethodNative, TerminalUnknown},
		{"dbus → native", with(nil, "DBUS_SESSION_BUS_ADDRESS", "unix:x"), "freebsd", MethodNative, TerminalUnknown},
		{"linux console → bell", with(nil, "TERM", "linux"), "linux", MethodBell, TerminalUnknown},
		{"ssh → bell", with(gui, "SSH_CONNECTION", "1 2 3 4"), "linux", MethodBell, TerminalUnknown},
		{"ssh darwin → bell", with(nil, "SSH_TTY", "/dev/ttys1"), "darwin", MethodBell, TerminalUnknown},
		{"ssh kitty still osc", with(nil, "SSH_CONNECTION", "x", "TERM", "xterm-kitty"), "linux", MethodOSC99, TerminalKitty},
		{"plan9 → bell", nil, "plan9", MethodBell, TerminalUnknown},
		{"windows conhost → native", nil, "windows", MethodNative, TerminalUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := envMap(tt.env)
			if got := DetectFor(env, tt.goos); got != tt.want {
				t.Errorf("DetectFor = %v, want %v", got, tt.want)
			}
			if got := DetectTerminal(env); got != tt.wantTerm {
				t.Errorf("DetectTerminal = %v, want %v", got, tt.wantTerm)
			}
		})
	}
}

func TestDetectUsesProcessEnv(t *testing.T) {
	for _, k := range []string{"TERM_PROGRAM", "TERM", "KITTY_WINDOW_ID", "GHOSTTY_RESOURCES_DIR",
		"WEZTERM_PANE", "WEZTERM_EXECUTABLE", "ITERM_SESSION_ID", "LC_TERMINAL", "WT_SESSION"} {
		t.Setenv(k, "")
	}
	t.Setenv("KITTY_WINDOW_ID", "7")
	if got := Detect(os.Getenv); got != MethodOSC99 {
		t.Fatalf("Detect with KITTY_WINDOW_ID = %v", got)
	}
	if got := Resolve(MethodAuto, Options{}); got != MethodOSC99 {
		t.Fatalf("Resolve(auto) with KITTY_WINDOW_ID = %v", got)
	}
}

func TestResolve(t *testing.T) {
	tests := []struct {
		name string
		m    Method
		env  map[string]string
		goos string
		want Method
	}{
		{"auto kitty", MethodAuto, map[string]string{"TERM": "xterm-kitty"}, "linux", MethodOSC99},
		{"osc ghostty", MethodOSC, map[string]string{"TERM_PROGRAM": "ghostty"}, "linux", MethodOSC777},
		{"osc unknown → osc9", MethodOSC, nil, "linux", MethodOSC9},
		{"explicit bell kept", MethodBell, map[string]string{"TERM": "xterm-kitty"}, "linux", MethodBell},
		{"explicit native kept", MethodNative, nil, "plan9", MethodNative},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Resolve(tt.m, Options{Getenv: envMap(tt.env), GOOS: tt.goos})
			if got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNew(t *testing.T) {
	n := Notification{Title: "A", Body: "b"}

	t.Run("osc in tmux wraps", func(t *testing.T) {
		var buf bytes.Buffer
		nt := New(MethodAuto, Options{Writer: &buf, Getenv: envMap(map[string]string{
			"TERM_PROGRAM": "iTerm.app", "TMUX": "/tmp/tmux-1/default,1,0"})})
		o, ok := nt.(*OSC)
		if !ok || o.Protocol != MethodOSC9 || !o.Tmux {
			t.Fatalf("got %#v", nt)
		}
		if err := nt.Notify(n); err != nil {
			t.Fatal(err)
		}
		if want := "\x1bPtmux;\x1b\x1b]9;A: b\a\x1b\\"; buf.String() != want {
			t.Fatalf("wrote %q, want %q", buf.String(), want)
		}
	})
	t.Run("bell", func(t *testing.T) {
		if _, ok := New(MethodBell, Options{}).(*Bell); !ok {
			t.Fatal("want *Bell")
		}
	})
	t.Run("off", func(t *testing.T) {
		nt := New(MethodOff, Options{})
		if _, ok := nt.(Off); !ok {
			t.Fatal("want Off")
		}
		if err := nt.Notify(n); err != nil {
			t.Fatal(err)
		}
		if _, ok := nt.(Sequencer).Sequence(n); ok {
			t.Fatal("Off produced a sequence")
		}
	})
	t.Run("explicit native", func(t *testing.T) {
		var calls atomic.Int32
		nt := New(MethodNative, Options{NativeSend: func(string, string, bool) error {
			calls.Add(1)
			return nil
		}})
		if _, ok := nt.(*Native); !ok {
			t.Fatalf("want *Native, got %T", nt)
		}
		if err := nt.Notify(n); err != nil || calls.Load() != 1 {
			t.Fatalf("err=%v calls=%d", err, calls.Load())
		}
	})
	t.Run("auto native falls back to bell", func(t *testing.T) {
		var buf bytes.Buffer
		nt := New(MethodAuto, Options{
			Writer: &buf, GOOS: "darwin", Getenv: envMap(nil),
			NativeSend: func(string, string, bool) error { return errors.New("no daemon") },
		})
		if _, ok := nt.(*Fallback); !ok {
			t.Fatalf("want *Fallback, got %T", nt)
		}
		if err := nt.Notify(n); err != nil {
			t.Fatal(err)
		}
		if buf.String() != "\a" {
			t.Fatalf("wrote %q", buf.String())
		}
	})
}

type errNotifier struct{ err error }

func (e errNotifier) Notify(Notification) error { return e.err }

func TestFallback(t *testing.T) {
	e1, e2 := errors.New("one"), errors.New("two")
	tests := []struct {
		name    string
		f       Fallback
		wantErr []error
	}{
		{"primary ok", Fallback{Primary: errNotifier{}, Secondary: errNotifier{e2}}, nil},
		{"secondary ok", Fallback{Primary: errNotifier{e1}, Secondary: errNotifier{}}, nil},
		{"both fail", Fallback{Primary: errNotifier{e1}, Secondary: errNotifier{e2}}, []error{e1, e2}},
		{"no secondary", Fallback{Primary: errNotifier{e1}}, []error{e1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.f.Notify(Notification{Title: "x"})
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				return
			}
			for _, w := range tt.wantErr {
				if !errors.Is(err, w) {
					t.Fatalf("err %v does not wrap %v", err, w)
				}
			}
		})
	}
}

func TestCmd(t *testing.T) {
	n := Notification{Title: "A", Body: "b"}

	if Cmd(nil, n) != nil {
		t.Fatal("nil notifier should give nil cmd")
	}
	if Cmd(Off{}, n) != nil {
		t.Fatal("Off should give nil cmd")
	}

	msg := Cmd(&OSC{Protocol: MethodOSC9}, n)()
	raw, ok := msg.(tea.RawMsg)
	if !ok || raw.Msg != "\x1b]9;A: b\a" {
		t.Fatalf("osc cmd msg = %#v", msg)
	}

	if msg := Cmd(errNotifier{}, n)(); msg != nil {
		t.Fatalf("success msg = %#v", msg)
	}
	boom := errors.New("boom")
	em, ok := Cmd(errNotifier{boom}, n)().(ErrMsg)
	if !ok || !errors.Is(em, boom) || em.Error() != "boom" {
		t.Fatalf("err msg = %#v", em)
	}

	fbRaw := Cmd(&Fallback{Primary: errNotifier{boom}, Secondary: &Bell{}}, n)()
	if r, ok := fbRaw.(tea.RawMsg); !ok || r.Msg != "\a" {
		t.Fatalf("fallback raw = %#v", fbRaw)
	}
	if m := Cmd(&Fallback{Primary: errNotifier{}, Secondary: &Bell{}}, n)(); m != nil {
		t.Fatalf("fallback primary ok = %#v", m)
	}
	if m := Cmd(&Fallback{Primary: errNotifier{boom}, Secondary: &Bell{}}, Notification{Title: "x", Silent: true})(); m != nil {
		t.Fatalf("silent fallback = %#v", m)
	}
	if m := Cmd(&Fallback{Primary: errNotifier{boom}, Secondary: errNotifier{}}, n)(); m != nil {
		t.Fatalf("fallback secondary ok = %#v", m)
	}
	if m, ok := Cmd(&Fallback{Primary: errNotifier{boom}}, n)().(ErrMsg); !ok || !errors.Is(m, boom) {
		t.Fatalf("fallback no secondary = %#v", m)
	}
	two := errors.New("two")
	if m, ok := Cmd(&Fallback{Primary: errNotifier{boom}, Secondary: errNotifier{two}}, n)().(ErrMsg); !ok || !errors.Is(m, two) {
		t.Fatalf("fallback both = %#v", m)
	}
}

func TestNative(t *testing.T) {
	type call struct {
		title, body string
		urgent      bool
	}
	tests := []struct {
		name string
		goos string
		n    Notification
		want *call
	}{
		{"plain", "darwin", Notification{Title: "Alice", Body: "hi"}, &call{"Alice", "hi", false}},
		{"sanitized", "darwin", Notification{Title: "A\x1b", Body: "x\ny\a"}, &call{"A", "x y", false}},
		{"darwin dash guard", "darwin", Notification{Title: "-execute", Body: "[x]"}, &call{" -execute", " [x]", false}},
		{"darwin drops non-printable", "darwin", Notification{Title: "A\u200bB", Body: "\U0001F468\u200d\U0001F469 \"q\" \\"}, &call{"AB", "\U0001F468\U0001F469 \"q\" \\", false}},
		{"linux dash guard", "linux", Notification{Title: "-u critical", Body: "--wait"}, &call{"\u200b-u critical", "\u200b--wait", false}},
		{"windows cdata and powershell", "windows", Notification{Title: "$(calc)", Body: "]]><image src=\"http://x\"/><![CDATA[ `$env:USERNAME`"}, &call{"\uff04(calc)", "]] ><image src=\"http://x\"/><![CDATA[ \u02cb\uff04env:USERNAME\u02cb", false}},
		{"linux markup", "linux", Notification{Title: "<b>", Body: "<a href=x>&</a>"}, &call{"<b>", "&lt;a href=x&gt;&amp;&lt;/a&gt;", false}},
		{"critical urgent", "windows", Notification{Title: "Call", Urgency: UrgencyCritical}, &call{"Call", "", true}},
		{"critical silent", "windows", Notification{Title: "Call", Urgency: UrgencyCritical, Silent: true}, &call{"Call", "", false}},
		{"body promoted", "windows", Notification{Body: "hello"}, &call{"hello", "", false}},
		{"empty skipped", "darwin", Notification{Title: "\x1b"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got *call
			nt := &Native{GOOS: tt.goos, Send: func(title, body string, urgent bool) error {
				got = &call{title, body, urgent}
				return nil
			}}
			if err := nt.Notify(tt.n); err != nil {
				t.Fatal(err)
			}
			if (got == nil) != (tt.want == nil) || (got != nil && *got != *tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestNativeTimeoutAndBusy(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	nt := &Native{Timeout: 10 * time.Millisecond, Send: func(string, string, bool) error {
		started <- struct{}{}
		<-release
		return nil
	}}
	n := Notification{Title: "x"}
	if err := nt.Notify(n); !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	<-started
	if err := nt.Notify(n); !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v, want ErrBusy", err)
	}
	close(release)
	// Once the hung delivery finishes the notifier accepts work again.
	deadline := time.Now().Add(2 * time.Second)
	for {
		nt.Send = func(string, string, bool) error { return nil }
		err := nt.Notify(n)
		if err == nil {
			break
		}
		if !errors.Is(err, ErrBusy) || time.Now().After(deadline) {
			t.Fatalf("err = %v", err)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestNativeSendError(t *testing.T) {
	boom := errors.New("boom")
	nt := &Native{Send: func(string, string, bool) error { return boom }}
	if err := nt.Notify(Notification{Title: "x"}); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestNewNativeDefaults(t *testing.T) {
	nt := NewNative("", 0)
	if nt.Send == nil || nt.Timeout != 0 {
		t.Fatalf("unexpected %#v", nt)
	}
}

func TestUrgencyString(t *testing.T) {
	for u, want := range map[Urgency]string{UrgencyNormal: "normal", UrgencyLow: "low", UrgencyCritical: "critical"} {
		if u.String() != want {
			t.Errorf("%d: %q", u, u.String())
		}
	}
	for term := TerminalUnknown; term <= TerminalRxvt; term++ {
		if term.String() == "" {
			t.Errorf("empty name for %d", term)
		}
	}
}

func TestAlertSafe(t *testing.T) {
	for goos, want := range map[string]bool{"darwin": true, "windows": true, "linux": false, "freebsd": false} {
		if got := alertSafe(goos); got != want {
			t.Errorf("alertSafe(%q) = %v", goos, got)
		}
	}
}

// TestNativeWindowsNoInjection checks that no sanitized text can close the
// CDATA section or trigger PowerShell expansion.
func TestNativeWindowsNoInjection(t *testing.T) {
	nt := &Native{GOOS: "windows"}
	for _, in := range []string{"]]>", "]]]>>", "$(x)", "`n", "a]]\x1b>b", "]]\u2028>"} {
		title, body, ok := nt.prepare(Notification{Title: in, Body: in})
		if !ok {
			continue
		}
		for _, s := range []string{title, body} {
			if strings.Contains(s, "]]>") || strings.ContainsAny(s, "$`") {
				t.Errorf("prepare(%q) = %q", in, s)
			}
		}
	}
}
