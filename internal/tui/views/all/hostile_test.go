package all_test

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"warte/internal/config"
	"warte/internal/core"
	"warte/internal/fake"
	"warte/internal/termimg"
	"warte/internal/tui"
)

// evil is prepended to every free-text string the backend returns: OSC 52
// clipboard write, screen clear, title change, a C1 CSI, DCS, carriage
// return, backspace and a bidi override.
const evil = "\x1b]52;c;cHduZWQ=\x07\x1b[2J\x1b]0;pwned\x07\u009b31m\x1bP+q\x1b\\\r\b‮\x00"

// hostileSvc returns the fake's data with every plain string field
// poisoned (IDs are named string types and are left alone).
type hostileSvc struct {
	*fake.Service
	ch chan core.Event
}

func newHostile(f *fake.Service) *hostileSvc {
	h := &hostileSvc{Service: f, ch: make(chan core.Event, 64)}
	go func() {
		for ev := range f.Events() {
			h.ch <- poisonIface(ev).(core.Event)
		}
	}()
	return h
}

var skipFields = map[string]bool{"ContentType": true}

func poison(v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			poison(v.Elem())
		}
	case reflect.Interface:
		if !v.IsNil() && v.CanSet() {
			v.Set(reflect.ValueOf(poisonIface(v.Interface())))
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if skipFields[v.Type().Field(i).Name] {
				continue
			}
			poison(v.Field(i))
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			poison(v.Index(i))
		}
	case reflect.Map:
		if v.IsNil() || !v.CanSet() {
			return
		}
		m := reflect.MakeMapWithSize(v.Type(), v.Len())
		it := v.MapRange()
		for it.Next() {
			e := reflect.New(v.Type().Elem()).Elem()
			e.Set(it.Value())
			poison(e)
			m.SetMapIndex(it.Key(), e)
		}
		v.Set(m)
	case reflect.String:
		if v.CanSet() && v.Type() == reflect.TypeOf("") && !strings.HasPrefix(v.String(), evil) {
			v.SetString(evil + v.String())
		}
	}
}

func poisonIface(x any) any {
	if x == nil {
		return nil
	}
	p := reflect.New(reflect.TypeOf(x))
	p.Elem().Set(reflect.ValueOf(x))
	poison(p.Elem())
	return p.Elem().Interface()
}

func pz[T any](x T, err error) (T, error) {
	if err != nil {
		return x, err
	}
	return poisonIface(x).(T), nil
}

func (h *hostileSvc) Events() <-chan core.Event { return h.ch }
func (h *hostileSvc) Self() core.Contact        { return poisonIface(h.Service.Self()).(core.Contact) }
func (h *hostileSvc) Chat(ctx context.Context, id core.ChatID) (core.Chat, error) {
	return pz(h.Service.Chat(ctx, id))
}
func (h *hostileSvc) Chats(ctx context.Context, f core.ChatFilter) ([]core.Chat, error) {
	return pz(h.Service.Chats(ctx, f))
}
func (h *hostileSvc) Contact(ctx context.Context, a core.ACI) (core.Contact, error) {
	return pz(h.Service.Contact(ctx, a))
}
func (h *hostileSvc) Contacts(ctx context.Context) ([]core.Contact, error) {
	return pz(h.Service.Contacts(ctx))
}
func (h *hostileSvc) Message(ctx context.Context, id core.MessageID) (core.Message, error) {
	return pz(h.Service.Message(ctx, id))
}
func (h *hostileSvc) Messages(ctx context.Context, c core.ChatID, b core.MessageID, n int) ([]core.Message, error) {
	return pz(h.Service.Messages(ctx, c, b, n))
}
func (h *hostileSvc) MessagesAround(ctx context.Context, id core.MessageID, n int) ([]core.Message, error) {
	return pz(h.Service.MessagesAround(ctx, id, n))
}
func (h *hostileSvc) Search(ctx context.Context, q core.SearchQuery) ([]core.SearchHit, error) {
	return pz(h.Service.Search(ctx, q))
}
func (h *hostileSvc) Stories(ctx context.Context) ([]core.Story, error) {
	return pz(h.Service.Stories(ctx))
}
func (h *hostileSvc) CallLog(ctx context.Context) ([]core.CallLogEntry, error) {
	return pz(h.Service.CallLog(ctx))
}
func (h *hostileSvc) StickerPacks(ctx context.Context) ([]core.StickerPack, error) {
	return pz(h.Service.StickerPacks(ctx))
}
func (h *hostileSvc) Folders(ctx context.Context) ([]core.Folder, error) {
	return pz(h.Service.Folders(ctx))
}
func (h *hostileSvc) Settings(ctx context.Context) (core.Settings, error) {
	return pz(h.Service.Settings(ctx))
}

var (
	reSGR  = regexp.MustCompile(`\x1b\[[0-9;:]*m`)
	reOSC8 = regexp.MustCompile(`\x1b\]8;[^\x07\x1b]*(\x07|\x1b\\)`)
)

// unsafeOutput reports the first dangerous sequence in a rendered frame:
// anything but SGR and OSC 8 escapes, C0 controls other than newline, C1
// controls and bidi overrides.
func unsafeOutput(frame string) string {
	s := reOSC8.ReplaceAllString(reSGR.ReplaceAllString(frame, ""), "")
	for i, r := range s {
		bad := (r < 0x20 && r != '\n') || r == 0x7f || (r >= 0x80 && r <= 0x9f) ||
			(r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069)
		if bad {
			lo, hi := max(0, i-30), min(len(s), i+30)
			return fmt.Sprintf("%U at %d: %q", r, i, s[lo:hi])
		}
	}
	if strings.Contains(s, "pwned") && strings.Contains(frame, "\x1b]0;") {
		return "title escape"
	}
	return ""
}

func newHostileDriver(t *testing.T, w, h int) *driver {
	t.Helper()
	f := fake.New(fake.Options{Seed: 1, Latency: -1, Clock: func() time.Time { return testNow }})
	t.Cleanup(func() { _ = f.Close() })
	svc := newHostile(f)
	cfg := config.Default()
	cfg.Theme = "charm"
	cfg.Images = config.ImagesBlocks
	cfg.Mouse = true
	cfg.Notifications = config.NotifyOff
	cfg.NotifyMethod = config.MethodOff
	none := false
	blocks := termimg.HalfBlocks
	a := tui.New(svc, cfg, tui.Options{
		Now:        func() time.Time { return testNow },
		Getenv:     func(string) string { return "" },
		NerdFonts:  &none,
		Images:     &blocks,
		SkipStart:  true,
		Notifier:   nopNotifier{},
		ConfigPath: t.TempDir() + "/config.toml",
	})
	t.Cleanup(a.Close)
	d := &driver{t: t, a: a, svc: f, w: w, h: h}
	d.send(tea.WindowSizeMsg{Width: w, Height: h})
	return d
}

// TestHostileTextNeverReachesTerminal renders every feature over a backend
// whose names, bodies, filenames, link previews, reactions, stories, ...
// all carry terminal escape sequences, and checks the frames contain only
// SGR/OSC 8 escapes and keep their size.
func TestHostileTextNeverReachesTerminal(t *testing.T) {
	check := func(t *testing.T, d *driver, label string) {
		t.Helper()
		frame := d.a.View().Content
		if bad := unsafeOutput(frame); bad != "" {
			t.Fatalf("%s: unsafe output %s", label, bad)
		}
		d.checkFrame(label)
	}
	t.Run("main", func(t *testing.T) {
		d := newHostileDriver(t, 120, 40)
		check(t, d, "chat list")
		for i := 0; i < 6; i++ {
			d.keys("enter")
			check(t, d, fmt.Sprintf("chat %d", i))
			for j := 0; j < 4; j++ {
				d.keys("k")
				check(t, d, fmt.Sprintf("chat %d msg %d", i, j))
			}
			d.keys("space i")
			check(t, d, fmt.Sprintf("chat %d sidebar", i))
			d.keys("space i")
			d.keys("ctrl+w h j")
		}
	})
	for _, f := range features() {
		t.Run(f.name, func(t *testing.T) {
			d := newHostileDriver(t, 120, 40)
			if f.setup != nil {
				f.setup(d)
			}
			for _, k := range f.open {
				d.keys(k)
			}
			check(t, d, "open")
			d.keys("j")
			check(t, d, "j")
		})
	}
}

// failingSvc fails most calls with an error whose text is hostile.
type failingSvc struct{ *hostileSvc }

var errEvil = fmt.Errorf("backend: %s", evil)

func (failingSvc) Messages(context.Context, core.ChatID, core.MessageID, int) ([]core.Message, error) {
	return nil, errEvil
}
func (failingSvc) MessagesAround(context.Context, core.MessageID, int) ([]core.Message, error) {
	return nil, errEvil
}
func (failingSvc) Search(context.Context, core.SearchQuery) ([]core.SearchHit, error) {
	return nil, errEvil
}
func (failingSvc) Stories(context.Context) ([]core.Story, error)            { return nil, errEvil }
func (failingSvc) CallLog(context.Context) ([]core.CallLogEntry, error)     { return nil, errEvil }
func (failingSvc) StickerPacks(context.Context) ([]core.StickerPack, error) { return nil, errEvil }
func (failingSvc) Contacts(context.Context) ([]core.Contact, error)         { return nil, errEvil }
func (failingSvc) Contact(context.Context, core.ACI) (core.Contact, error) {
	return core.Contact{}, errEvil
}
func (failingSvc) SafetyNumber(context.Context, core.ACI) (core.SafetyNumber, error) {
	return core.SafetyNumber{}, errEvil
}
func (failingSvc) Send(context.Context, core.ChatID, core.Outgoing) (core.MessageID, error) {
	return core.MessageID{}, errEvil
}
func (failingSvc) Attachment(context.Context, core.MessageID, string) ([]byte, error) {
	return nil, errEvil
}
func (failingSvc) Avatar(context.Context, string) ([]byte, error) { return nil, errEvil }
func (failingSvc) StickerImage(context.Context, string, uint32) ([]byte, error) {
	return nil, errEvil
}

// TestBackendErrorsAreSanitized: error texts from the backend end up in
// toasts and empty states; they must not carry escapes either.
func TestBackendErrorsAreSanitized(t *testing.T) {
	f := fake.New(fake.Options{Seed: 1, Latency: -1, Clock: func() time.Time { return testNow }})
	t.Cleanup(func() { _ = f.Close() })
	svc := failingSvc{newHostile(f)}
	cfg := config.Default()
	cfg.Theme = "charm"
	cfg.Images = config.ImagesBlocks
	cfg.Notifications = config.NotifyOff
	cfg.NotifyMethod = config.MethodOff
	none := false
	blocks := termimg.HalfBlocks
	a := tui.New(svc, cfg, tui.Options{
		Now: func() time.Time { return testNow }, Getenv: func(string) string { return "" },
		NerdFonts: &none, Images: &blocks, SkipStart: true, Notifier: nopNotifier{},
		ConfigPath: t.TempDir() + "/config.toml",
	})
	t.Cleanup(a.Close)
	d := &driver{t: t, a: a, svc: f, w: 120, h: 40}
	d.send(tea.WindowSizeMsg{Width: 120, Height: 40})
	steps := []string{"enter", "i", "h", "i", "enter", "esc", "space c", "esc", "space s", "esc",
		"space /", "x", "esc", "esc", "space n", "esc", ":verify", "esc", "i", "ctrl+s", "esc", "esc",
		"g m", "esc", "space space", "esc", "esc", ":profile", "esc"}
	for _, k := range steps {
		if k == "h" {
			d.send(tea.KeyPressMsg{Code: 'h', Text: "h"})
			continue
		}
		d.keys(k)
		if bad := unsafeOutput(d.a.View().Content); bad != "" {
			t.Fatalf("after %q: unsafe output %s", k, bad)
		}
		d.checkFrame(k)
	}
}
