package forms

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"warte/internal/core"
	"warte/internal/fake"
	"warte/internal/tui/ui"
)

// failSvc wraps the fake service with injectable errors.
type failSvc struct {
	*fake.Service
	settingsErr, updateGroupErr error
	settingsSaves               int
}

func (f *failSvc) Settings(ctx context.Context) (core.Settings, error) {
	if f.settingsErr != nil {
		return core.Settings{}, f.settingsErr
	}
	return f.Service.Settings(ctx)
}

func (f *failSvc) UpdateSettings(ctx context.Context, s core.Settings) error {
	f.settingsSaves++
	return f.Service.UpdateSettings(ctx, s)
}

func (f *failSvc) UpdateGroup(ctx context.Context, id core.ChatID, u core.GroupUpdate) error {
	if f.updateGroupErr != nil {
		return f.updateGroupErr
	}
	return f.Service.UpdateGroup(ctx, id, u)
}

// promptLinker behaves like the real backend: LinkQR immediately followed
// by a "scan the QR code" progress stage, then nothing until canceled.
type promptLinker struct{}

func (promptLinker) Link(ctx context.Context, _ string) (<-chan core.LinkEvent, error) {
	ch := make(chan core.LinkEvent, 4)
	ch <- core.LinkQR{URL: "sgnl://linkdevice?uuid=DFQnvr337hHNbSuyPzuq1Q&pub_key=BbBiY9%2FSUv17Yl2SYkUVjCuuYFjOSunN8%2Buw%2BC1ePP6A"}
	ch <- core.LinkProgress{Stage: "Scan the QR code with Signal on your phone (Settings › Linked devices)"}
	go func() { <-ctx.Done(); close(ch) }()
	return ch, nil
}

func TestReviewLinkKeepsQRAfterScanPrompt(t *testing.T) {
	m := LinkModel(promptLinker{}, "x").WithReduceMotion(true)
	driveLink(t, m, m.Init(), func() bool { return false })
	if m.state != linkWaiting {
		t.Fatalf("state %d after the scan prompt, want waiting", m.state)
	}
	// The QR code must fit a standard 80x24 terminal.
	out := m.Render(80, 24)
	checkBounds(t, out, 80, 24)
	plain := ansi.Strip(out)
	if !strings.ContainsAny(plain, "▀▄") || !strings.Contains(plain, "Linked devices") {
		t.Fatalf("80x24 link screen lacks QR or instructions:\n%s", plain)
	}
	if m.cancel != nil {
		m.cancel()
	}
}

func TestReviewSettingsLoadErrorDoesNotSave(t *testing.T) {
	h := newHarness(t)
	fs := &failSvc{Service: h.svc, settingsErr: errors.New("offline")}
	h.ctx.Svc = fs
	v := NewSettings(h.ctx)
	h.push(v)
	if !strings.Contains(h.plain(), "offline") {
		t.Errorf("load error not shown:\n%s", h.plain())
	}
	for i := 0; i < 10; i++ {
		h.key("tab")
		h.key("left")
	}
	if fs.settingsSaves != 0 {
		t.Fatalf("UpdateSettings called %d times with unloaded settings", fs.settingsSaves)
	}
	if strings.Contains(h.plain(), "Read receipts") {
		t.Error("account settings offered although they could not be loaded")
	}
}

func TestReviewGroupConfirmCancelLeavesNoStatus(t *testing.T) {
	h := newHarness(t)
	g := NewGroupSettings(h.ctx, fake.ChatHike)
	h.push(g)
	if !g.chat.SelfIsAdmin {
		t.Skip("demo data: self is not admin")
	}
	selectRow(t, &g.page, "m:"+string(fake.BobACI))
	h.key("p")
	if _, ok := h.top().(*confirmOverlay); !ok {
		t.Fatalf("no confirm, top %T", h.top())
	}
	h.key("n")
	if g.status != "" {
		t.Errorf("status after cancel = %q", g.status)
	}
}

func TestReviewGroupUpdateErrorToast(t *testing.T) {
	h := newHarness(t)
	fs := &failSvc{Service: h.svc, updateGroupErr: errors.New("server says no\x1b[31m")}
	h.ctx.Svc = fs
	g := NewGroupSettings(h.ctx, fake.ChatHike)
	h.push(g)
	if !g.chat.SelfIsAdmin {
		t.Skip("demo data: self is not admin")
	}
	selectRow(t, &g.page, "announce")
	h.key("enter")
	toasts := outOf[ui.ToastMsg](h)
	if len(toasts) == 0 || toasts[len(toasts)-1].Level != ui.ToastError ||
		!strings.Contains(toasts[len(toasts)-1].Text, "server says no") || strings.Contains(toasts[len(toasts)-1].Text, "\x1b") {
		t.Fatalf("toasts %+v", toasts)
	}
	if g.status != "" {
		t.Errorf("status = %q", g.status)
	}
}

func TestReviewSoleAdminCannotLeave(t *testing.T) {
	h := newHarness(t)
	id, err := h.svc.CreateGroup(bg(), "Solo", []core.ACI{fake.AliceACI}, nil)
	if err != nil {
		t.Fatal(err)
	}
	h.pump()
	g := NewGroupSettings(h.ctx, id)
	h.push(g)
	selectRow(t, &g.page, "leave")
	h.key("enter")
	if _, ok := h.top().(*confirmOverlay); ok {
		t.Fatal("offered to leave although self is the only admin")
	}
	if !strings.Contains(g.status, "only admin") {
		t.Errorf("status = %q", g.status)
	}
}

func TestReviewWizardTimerFailureOpensGroup(t *testing.T) {
	h := newHarness(t)
	fs := &failSvc{Service: h.svc, updateGroupErr: errors.New("timer rejected")}
	h.ctx.Svc = fs
	w := NewNewGroup(h.ctx)
	w.name, w.timer = "Timed", time.Hour
	msg := w.create()()
	o, cmd := w.Update(msg)
	if o != nil {
		t.Fatal("wizard stayed open after the group was created; enter would create a second one")
	}
	h.run(cmd)
	if len(outOf[ui.OpenChatMsg](h)) != 1 {
		t.Errorf("group not opened: %v", h.out)
	}
}

func TestReviewWideAndHostileText(t *testing.T) {
	h := newHarness(t)
	if err := h.svc.SetNickname(bg(), fake.AliceACI, "王小明 👩‍👩‍👧 ﷽ 🇩🇪"); err != nil {
		t.Fatal(err)
	}
	title := "日本語のグループ\x1b]0;pwned\x07 🧗‍♀️"
	desc := "説明\tです\x1b[2J — " + strings.Repeat("長い", 40)
	if err := h.svc.UpdateGroup(bg(), fake.ChatHike, core.GroupUpdate{Title: &title, Description: &desc}); err != nil {
		t.Fatal(err)
	}
	h.pump()
	for _, o := range []ui.Overlay{NewContactSettings(h.ctx, fake.ChatAlice), NewGroupSettings(h.ctx, fake.ChatHike), NewSafetyNumber(h.ctx, fake.AliceACI)} {
		h.stack = nil
		h.push(o)
		for _, sz := range append(sizes, [2]int{40, 14}) {
			out := h.view(sz[0], sz[1])
			if strings.Contains(out, "pwned") || strings.Contains(out, "\t") || strings.Contains(out, "\x1b[2J") {
				t.Errorf("%T: unsanitized output %q", o, ansi.Strip(out))
			}
		}
	}
	// Editing the title prefills a sanitized value.
	h.stack = nil
	g := NewGroupSettings(h.ctx, fake.ChatHike)
	h.push(g)
	selectRow(t, &g.page, "title")
	h.key("enter")
	if _, ok := h.top().(*formOverlay); !ok {
		t.Fatalf("no title dialog, top %T", h.top())
	}
	if out := h.view(120, 40); strings.Contains(out, "pwned") || strings.Contains(out, "\x07") {
		t.Errorf("title dialog shows raw escape: %q", ansi.Strip(out))
	}
}
