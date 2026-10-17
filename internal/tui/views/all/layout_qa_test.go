package all_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"warte/internal/config"
	"warte/internal/core"
	"warte/internal/fake"
	"warte/internal/termimg"
	"warte/internal/tui"
	"warte/internal/tui/ui"
)

func newDriverWith(t *testing.T, w, h int, themeName, layout string, nerd bool) *driver {
	t.Helper()
	svc := fake.New(fake.Options{Seed: 1, Latency: -1, Clock: func() time.Time { return testNow }})
	t.Cleanup(func() { _ = svc.Close() })
	cfg := config.Default()
	cfg.Theme = themeName
	cfg.Layout = layout
	cfg.Images = config.ImagesBlocks
	cfg.Mouse = true
	cfg.Notifications = config.NotifyOff
	cfg.NotifyMethod = config.MethodOff
	blocks := termimg.HalfBlocks
	cfgPath := t.TempDir() + "/config.toml"
	a := tui.New(svc, cfg, tui.Options{
		Now: func() time.Time { return testNow }, Getenv: func(string) string { return "" },
		NerdFonts: &nerd, Images: &blocks, SkipStart: true, Notifier: nopNotifier{}, ConfigPath: cfgPath,
	})
	t.Cleanup(a.Close)
	d := &driver{t: t, a: a, svc: svc, w: w, h: h, cfg: cfgPath}
	d.send(windowSize(w, h))
	return d
}

var qaSizes = [][2]int{{40, 12}, {60, 20}, {120, 40}, {200, 60}}

var nasty = []string{
	"evil\x1b[2J\x1b[31mred\x1b]0;pwn\x07 title",
	"tab\there\rcr\bbs\x7fdel\x00nul",
	"rtl \u202egnp.exe\u202c and \u2066iso\u2069 \u05e2\u05d1\u05e8\u05d9\u05ea \u0645\u0631\u062d\u0628\u0627",
	"cjk 你好世界こんにちは안녕하세요 全角ＡＢＣ wide wide wide wide wide wide wide wide wide wide",
	"zwj \U0001f469\u200d\U0001f469\u200d\U0001f467 \U0001f3f3\ufe0f\u200d\U0001f308 \U0001f44d\U0001f3fd \U0001f1e9\U0001f1ea \u2764\ufe0f \u26f0\ufe0f 1\ufe0f\u20e3 #\ufe0f\u20e3 e\u0301\u0301 Z\u0337\u0322\u031ba\u0337lgo",
	strings.Repeat("x", 300),
	"line1\nline2\n\n\nline6\u2028ls\u2029ps\u0085nel",
	"\u200b\u200b\ufeff\u00ad soft",
}

// TestNastyTextLayout: hostile names, group titles, file names and bodies
// (escape sequences, controls, bidi overrides, CJK, ZWJ and keycap emoji,
// zalgo, separators) neither reach the terminal raw nor break the frame.
func TestNastyTextLayout(t *testing.T) {
	for _, sz := range qaSizes {
		for _, layout := range []string{"bubbles", "irc"} {
			t.Run(fmt.Sprintf("%dx%d/%s", sz[0], sz[1], layout), func(t *testing.T) {
				d := newDriverWith(t, sz[0], sz[1], "charm", layout, true)
				ctx := context.Background()
				chats, _ := d.svc.Chats(ctx, core.ChatFilter{IncludeEmpty: true})
				var grp core.ChatID
				for _, c := range chats {
					if c.Kind == core.ChatGroup {
						grp = c.ID
						break
					}
				}
				title := nasty[0] + nasty[2] + nasty[4]
				desc := strings.Join(nasty, " ")
				if err := d.svc.UpdateGroup(ctx, grp, core.GroupUpdate{Title: &title, Description: &desc}); err != nil {
					t.Fatal(err)
				}
				for _, n := range nasty {
					_, err := d.svc.Send(ctx, grp, core.Outgoing{Body: n, Attachments: []core.OutgoingAttachment{{FileName: n + ".pdf", ContentType: "application/pdf", Data: []byte("x"), Caption: n}}})
					if err != nil {
						t.Fatal(err)
					}
					d.svc.Send(ctx, grp, core.Outgoing{Body: n})
				}
				time.Sleep(50 * time.Millisecond)
				d.send(ui.OpenChatMsg{Chat: grp})
				reported := map[rune]bool{}
				check := func(label string) {
					d.checkFrame(label)
					raw := d.a.View().Content
					stripped := ansi.Strip(raw)
					for _, r := range stripped {
						if (r < 0x20 && r != '\n') || r == 0x7f || (r >= 0x80 && r < 0xa0) || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) || r == 0x2028 || r == 0x2029 {
							if !reported[r] {
								reported[r] = true
								t.Errorf("%s: control %U in frame", label, r)
							}
						}
					}
					if strings.Contains(raw, "\x1b[2J") || strings.Contains(raw, "\x1b]0;") {
						t.Fatalf("%s: injected escape survived", label)
					}
				}
				check("open")
				for i := 0; i < 25; i++ {
					d.keys("k")
					check(fmt.Sprintf("k%d", i))
				}
				for _, k := range []string{"K", "esc", "g i", "esc", "space i", "j", "j", "space i", "space space", "esc", "esc", "H"} {
					d.keys(k)
					check(k)
				}
				for _, k := range []string{"space /"} {
					d.keys(k)
				}
				d.keys(":find evil")
				check("find")
			})
		}
	}
}

// TestResizeWithOverlays: every overlay survives resizes down to tiny sizes
// and back with an exact frame.
func TestResizeWithOverlays(t *testing.T) {
	for _, f := range features() {
		t.Run(f.name, func(t *testing.T) {
			d := newDriverWith(t, 120, 40, "charm", "bubbles", true)
			if f.setup != nil {
				f.setup(d)
			}
			for _, k := range f.open {
				d.keys(k)
			}
			d.keys("space i")
			for _, sz := range [][2]int{{200, 60}, {60, 20}, {30, 10}, {80, 24}, {20, 6}, {1, 1}, {0, 0}, {120, 40}} {
				d.w, d.h = sz[0], sz[1]
				d.send(windowSize(sz[0], sz[1]))
				if sz[0] >= 20 {
					d.checkFrame(fmt.Sprintf("%v", sz))
				} else {
					_ = d.a.View()
				}
			}
		})
	}
}

func windowSize(w, h int) tea.WindowSizeMsg { return tea.WindowSizeMsg{Width: w, Height: h} }
