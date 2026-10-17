package forms

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"warte/internal/core"
	"warte/internal/fake"
	"warte/internal/tui/ui"
)

func bg() context.Context { return context.Background() }

// selectRow moves a page's cursor to the row with id.
func selectRow(t *testing.T, p *page, id string) {
	t.Helper()
	for i, r := range p.rows {
		if r.id == id {
			p.sel = i
			return
		}
	}
	var ids []string
	for _, r := range p.rows {
		ids = append(ids, r.id)
	}
	t.Fatalf("no row %q in %v", id, ids)
}

func TestSafetyNumberView(t *testing.T) {
	h := newHarness(t)
	v := NewSafetyNumber(h.ctx, fake.AliceACI)
	h.push(v)
	out := h.plain()
	sn, err := h.svc.SafetyNumber(bg(), fake.AliceACI)
	if err != nil {
		t.Fatal(err)
	}
	rows := groupDigits(sn.Digits)
	if len(rows) != 3 {
		t.Fatalf("got %d digit rows, want 3: %q", len(rows), rows)
	}
	for _, r := range rows {
		groups := strings.Fields(r)
		if len(groups) != 4 {
			t.Fatalf("row %q: %d groups", r, len(groups))
		}
		for _, g := range groups {
			if len(g) != 5 {
				t.Fatalf("group %q is not 5 digits", g)
			}
		}
		if !strings.Contains(out, r) {
			t.Errorf("view lacks digit row %q:\n%s", r, out)
		}
	}
	if !strings.Contains(out, "▀") && !strings.Contains(out, "▄") {
		t.Errorf("no QR code at 120x40:\n%s", out)
	}
	if v.Title() != "Safety number · Alice Chen" {
		t.Errorf("title %q", v.Title())
	}
	status := func(v bool) string {
		if v {
			return "✔ Verified"
		}
		return "Not verified"
	}
	if !strings.Contains(out, status(sn.Verified)) {
		t.Fatalf("missing status %q:\n%s", status(sn.Verified), out)
	}
	h.key("v")
	c, _ := h.svc.Contact(bg(), fake.AliceACI)
	if c.Verified == sn.Verified {
		t.Fatal("v did not toggle verified")
	}
	if !strings.Contains(h.plain(), status(c.Verified)) {
		t.Errorf("view not refreshed:\n%s", h.plain())
	}
	h.key("enter")
	c, _ = h.svc.Contact(bg(), fake.AliceACI)
	if c.Verified != sn.Verified {
		t.Fatal("enter did not toggle back")
	}
	for _, sz := range sizes {
		h.view(sz[0], sz[1])
	}
	h.key("esc")
	if h.top() != nil {
		t.Fatal("esc did not close")
	}
}

func TestSafetyNumberSelfError(t *testing.T) {
	h := newHarness(t)
	h.push(NewSafetyNumber(h.ctx, fake.SelfACI))
	if out := h.plain(); !strings.Contains(out, "✗") {
		t.Fatalf("want error, got:\n%s", out)
	}
	h.key("v") // no panic, no action
}

func TestNewChatDialog(t *testing.T) {
	h := newHarness(t)
	d := NewNewChat(h.ctx, "")
	h.push(d)
	if !strings.Contains(h.plain(), "Who do you want to message?") {
		t.Fatalf("view:\n%s", h.plain())
	}
	h.typ("+4915799887766")
	h.key("enter")
	opened := outOf[ui.OpenChatMsg](h)
	if len(opened) != 1 {
		t.Fatalf("OpenChatMsg = %v (out %v)", opened, h.out)
	}
	if !opened[0].Chat.IsDirect() {
		t.Errorf("opened %q", opened[0].Chat)
	}
	if h.top() != nil {
		t.Error("dialog still open")
	}
}

func TestNewChatDialogByName(t *testing.T) {
	h := newHarness(t)
	h.push(NewNewChat(h.ctx, ""))
	h.typ("Alice")
	h.key("enter")
	opened := outOf[ui.OpenChatMsg](h)
	if len(opened) != 1 || opened[0].Chat != fake.ChatAlice {
		t.Fatalf("opened %v", opened)
	}
}

func TestNewChatDialogErrorStaysOpen(t *testing.T) {
	h := newHarness(t)
	h.push(NewNewChat(h.ctx, ""))
	h.key("enter") // empty: validation error, stays
	if h.top() == nil {
		t.Fatal("closed on empty input")
	}
	h.typ("zzzz-no-such-person")
	h.key("enter")
	if h.top() == nil {
		t.Fatal("closed on resolve error")
	}
	if len(outOf[ui.OpenChatMsg](h)) != 0 {
		t.Fatal("opened a chat")
	}
	if out := h.plain(); !strings.Contains(out, "✗") {
		t.Errorf("no error shown:\n%s", out)
	}
	for _, sz := range sizes {
		h.view(sz[0], sz[1])
	}
	h.key("esc")
	if h.top() != nil {
		t.Fatal("esc did not close")
	}
}

func TestNewGroupWizard(t *testing.T) {
	h := newHarness(t)
	w := NewNewGroup(h.ctx)
	h.push(w)
	if w.step != stepDetails {
		t.Fatalf("step %d", w.step)
	}
	h.typ("Climbing crew")
	h.key("tab", "tab", "j", "enter") // skip avatar, pick next timer
	if w.step != stepMembers {
		t.Fatalf("not on members step (step %d):\n%s", w.step, h.plain())
	}
	h.typ("ali")
	out := h.plain()
	if !strings.Contains(out, "Alice") {
		t.Fatalf("filter lacks Alice:\n%s", out)
	}
	h.key("tab")
	h.key("ctrl+u")
	h.typ("bob")
	h.key("tab")
	h.key("enter")
	if w.step != stepReview {
		t.Fatalf("not on review step:\n%s", h.plain())
	}
	out = h.plain()
	for _, want := range []string{"Climbing crew", "Alice Chen", "Bob", "3 including you"} {
		if !strings.Contains(out, want) {
			t.Errorf("review lacks %q:\n%s", want, out)
		}
	}
	for _, sz := range sizes {
		h.view(sz[0], sz[1])
	}
	h.key("enter")
	opened := outOf[ui.OpenChatMsg](h)
	if len(opened) != 1 || !opened[0].Chat.IsGroup() {
		t.Fatalf("opened %v; out %v", opened, h.out)
	}
	chat, err := h.svc.Chat(bg(), opened[0].Chat)
	if err != nil {
		t.Fatal(err)
	}
	if chat.Title != "Climbing crew" || len(chat.Members) != 3 || !chat.SelfIsAdmin {
		t.Errorf("chat = %q, %d members", chat.Title, len(chat.Members))
	}
	if chat.ExpireTimer != 30*time.Second {
		t.Errorf("timer %v", chat.ExpireTimer)
	}
}

func TestNewGroupWizardBackAndCancel(t *testing.T) {
	h := newHarness(t)
	w := NewNewGroup(h.ctx)
	h.push(w)
	h.key("enter") // name required
	if w.step != stepDetails {
		t.Fatal("advanced without a name")
	}
	h.typ("X")
	h.key("enter", "enter", "enter")
	if w.step != stepMembers {
		t.Fatalf("step %d", w.step)
	}
	h.key("esc") // back to details
	if w.step != stepDetails || w.name != "X" {
		t.Fatalf("step %d name %q", w.step, w.name)
	}
	h.key("esc")
	if h.top() != nil {
		t.Fatal("esc on details did not cancel")
	}
}

func TestGroupSettings(t *testing.T) {
	h := newHarness(t)
	g := NewGroupSettings(h.ctx, fake.ChatHike)
	h.push(g)
	if !g.loaded || g.err != "" {
		t.Fatalf("load: %q", g.err)
	}
	if !g.chat.SelfIsAdmin {
		t.Skip("demo data: self is not admin of the hike group")
	}
	out := h.plain()
	for _, want := range []string{"Warte Wanderers", "Group link", "REQUESTS TO JOIN", "Ines", "Leave group", "Admin"} {
		if !strings.Contains(out, want) {
			t.Errorf("view lacks %q:\n%s", want, out)
		}
	}
	for _, sz := range sizes {
		h.view(sz[0], sz[1])
	}

	// Approve the join request with "a".
	selectRow(t, &g.page, "r:"+string(fake.InesACI))
	h.key("a")
	chat, _ := h.svc.Chat(bg(), fake.ChatHike)
	if len(chat.RequestingJoin) != 0 || !hasMember(chat, fake.InesACI) {
		t.Fatalf("approve failed: %+v", chat.RequestingJoin)
	}

	// Promote Bob: p, then confirm with y.
	selectRow(t, &g.page, "m:"+string(fake.BobACI))
	h.key("p")
	if _, ok := h.top().(*confirmOverlay); !ok {
		t.Fatalf("no confirm, top %T", h.top())
	}
	h.key("y")
	chat, _ = h.svc.Chat(bg(), fake.ChatHike)
	if !isAdmin(chat, fake.BobACI) {
		t.Fatal("promote failed")
	}
	// Demote via the action menu: enter, p, y.
	selectRow(t, &g.page, "m:"+string(fake.BobACI))
	h.key("enter")
	if _, ok := h.top().(*actionMenu); !ok {
		t.Fatalf("no action menu, top %T", h.top())
	}
	h.key("p", "y")
	chat, _ = h.svc.Chat(bg(), fake.ChatHike)
	if isAdmin(chat, fake.BobACI) {
		t.Fatal("demote failed")
	}

	// Announcement-only toggle.
	selectRow(t, &g.page, "announce")
	h.key("enter")
	chat, _ = h.svc.Chat(bg(), fake.ChatHike)
	if !chat.OnlyAdminsSend {
		t.Fatal("announce toggle failed")
	}

	// Admin approval toggle and link reset.
	selectRow(t, &g.page, "approval")
	h.key("enter")
	chat, _ = h.svc.Chat(bg(), fake.ChatHike)
	if chat.AddFromLinkMode != core.AccessAdmin {
		t.Fatalf("approval: mode %v", chat.AddFromLinkMode)
	}
	old := chat.InviteLink
	selectRow(t, &g.page, "reset")
	h.key("enter", "y")
	chat, _ = h.svc.Chat(bg(), fake.ChatHike)
	if chat.InviteLink == old || chat.InviteLink == "" {
		t.Fatal("reset failed")
	}

	// Rename via the text dialog.
	selectRow(t, &g.page, "title")
	h.key("enter")
	if _, ok := h.top().(*formOverlay); !ok {
		t.Fatalf("no text dialog, top %T", h.top())
	}
	h.key("ctrl+u")
	h.typ("Hikers")
	h.key("enter")
	chat, _ = h.svc.Chat(bg(), fake.ChatHike)
	if chat.Title != "Hikers" {
		t.Fatalf("title %q", chat.Title)
	}
	if h.top() != g {
		t.Fatalf("top %T after rename", h.top())
	}

	// Disappearing timer via the select dialog.
	selectRow(t, &g.page, "timer")
	h.key("enter", "j", "enter")
	chat, _ = h.svc.Chat(bg(), fake.ChatHike)
	if chat.ExpireTimer == 0 {
		t.Fatal("timer not set")
	}

	// Add a member through the picker.
	selectRow(t, &g.page, "add")
	h.key("enter")
	h.typ("dev")
	h.key("tab", "enter")
	chat, _ = h.svc.Chat(bg(), fake.ChatHike)
	if !hasMember(chat, fake.DevACI) {
		t.Fatal("add member failed")
	}

	// Remove Dev again.
	selectRow(t, &g.page, "m:"+string(fake.DevACI))
	h.key("x", "y")
	chat, _ = h.svc.Chat(bg(), fake.ChatHike)
	if hasMember(chat, fake.DevACI) {
		t.Fatal("remove failed")
	}

	// Link off (confirm).
	selectRow(t, &g.page, "link")
	h.key("enter", "y")
	chat, _ = h.svc.Chat(bg(), fake.ChatHike)
	if linkOn(chat) {
		t.Fatal("link still on")
	}

	// Leave (confirm).
	selectRow(t, &g.page, "leave")
	h.key("enter", "y")
	chat, _ = h.svc.Chat(bg(), fake.ChatHike)
	if hasMember(chat, fake.SelfACI) {
		t.Fatal("still a member")
	}
	for _, sz := range sizes {
		h.view(sz[0], sz[1])
	}
}

func TestGroupSettingsNonAdmin(t *testing.T) {
	h := newHarness(t)
	chats, _ := h.svc.Chats(bg(), core.ChatFilter{GroupsOnly: true})
	var id core.ChatID
	for _, c := range chats {
		if !c.SelfIsAdmin {
			id = c.ID
		}
	}
	if id == "" {
		t.Skip("no non-admin group in demo data")
	}
	g := NewGroupSettings(h.ctx, id)
	h.push(g)
	before, _ := h.svc.Chat(bg(), id)
	selectRow(t, &g.page, "announce")
	h.key("enter")
	if g.status != adminOnly {
		t.Errorf("status %q", g.status)
	}
	after, _ := h.svc.Chat(bg(), id)
	if after.OnlyAdminsSend != before.OnlyAdminsSend {
		t.Error("non-admin changed announce mode")
	}
	for _, sz := range sizes {
		h.view(sz[0], sz[1])
	}
}

func TestGroupLeaveCommand(t *testing.T) {
	h := newHarness(t)
	h.chat = fake.ChatBookClub
	var cmd ui.Command
	for _, c := range ui.Commands() {
		if c.Name == "group" {
			cmd = c
		}
	}
	if cmd.Run == nil {
		t.Fatal(":group not registered")
	}
	h.run(cmd.Run(h.ctx, []string{"leave"}))
	h.pump()
	if _, ok := h.top().(*confirmOverlay); !ok {
		t.Fatalf("top %T", h.top())
	}
	h.key("n")
	if _, ok := h.top().(*GroupSettings); !ok {
		t.Fatalf("top %T", h.top())
	}
}

func hasMember(c core.Chat, a core.ACI) bool {
	for _, m := range c.Members {
		if m.ACI == a {
			return true
		}
	}
	return false
}

func isAdmin(c core.Chat, a core.ACI) bool {
	for _, m := range c.Members {
		if m.ACI == a {
			return m.Admin
		}
	}
	return false
}

func TestContactSettings(t *testing.T) {
	h := newHarness(t)
	c := NewContactSettings(h.ctx, fake.ChatAlice)
	h.push(c)
	out := h.plain()
	for _, want := range []string{"Alice Chen", "Nickname", "Mute notifications", "Disappearing messages", "Safety number", "Block"} {
		if !strings.Contains(out, want) {
			t.Errorf("view lacks %q:\n%s", want, out)
		}
	}
	// Nickname.
	selectRow(t, &c.page, "nick")
	h.key("enter")
	h.typ("Ali")
	h.key("enter")
	ct, _ := h.svc.Contact(bg(), fake.AliceACI)
	if ct.Nickname != "Ali" {
		t.Fatalf("nickname %q", ct.Nickname)
	}
	// Mute cycling with l: Off → 1 hour.
	selectRow(t, &c.page, "mute")
	h.key("l")
	chat, _ := h.svc.Chat(bg(), fake.ChatAlice)
	if !chat.Muted(testNow) || chat.MutedUntil.Sub(testNow) != time.Hour {
		t.Fatalf("muted until %v", chat.MutedUntil)
	}
	h.key("h") // back to off
	chat, _ = h.svc.Chat(bg(), fake.ChatAlice)
	if chat.Muted(testNow) {
		t.Fatalf("still muted until %v", chat.MutedUntil)
	}
	// Mute "Always" via the dialog.
	h.key("enter", "G", "enter")
	chat, _ = h.svc.Chat(bg(), fake.ChatAlice)
	if chat.MutedUntil.Year() < 9000 {
		t.Fatalf("muted until %v", chat.MutedUntil)
	}
	// Safety number opens.
	selectRow(t, &c.page, "safety")
	h.key("enter")
	if _, ok := h.top().(*SafetyNumberView); !ok {
		t.Fatalf("top %T", h.top())
	}
	h.key("esc")
	// Block (confirm) and unblock.
	selectRow(t, &c.page, "block")
	h.key("enter", "y")
	chat, _ = h.svc.Chat(bg(), fake.ChatAlice)
	if !chat.Blocked {
		t.Fatal("not blocked")
	}
	selectRow(t, &c.page, "block")
	h.key("enter")
	chat, _ = h.svc.Chat(bg(), fake.ChatAlice)
	if chat.Blocked {
		t.Fatal("still blocked")
	}
	for _, sz := range sizes {
		h.view(sz[0], sz[1])
	}
}

func TestContactSettingsMessageRequest(t *testing.T) {
	h := newHarness(t)
	c := NewContactSettings(h.ctx, fake.ChatInes)
	h.push(c)
	if !c.chat.MessageRequest {
		t.Skip("demo data: Ines is not a message request")
	}
	if out := h.plain(); !strings.Contains(out, "Report spam & block") || !strings.Contains(out, "Accept") {
		t.Fatalf("view:\n%s", out)
	}
	selectRow(t, &c.page, "accept")
	h.key("enter")
	chat, _ := h.svc.Chat(bg(), fake.ChatInes)
	if chat.MessageRequest {
		t.Fatal("not accepted")
	}

	h2 := newHarness(t)
	c2 := NewContactSettings(h2.ctx, fake.ChatInes)
	h2.push(c2)
	selectRow(t, &c2.page, "spam")
	h2.key("enter", "y")
	chat, _ = h2.svc.Chat(bg(), fake.ChatInes)
	if !chat.Blocked {
		t.Fatal("report spam did not block")
	}
}

func TestChatSettingsNoteToSelfAndGroup(t *testing.T) {
	h := newHarness(t)
	o := NewChatSettings(h.ctx, fake.ChatNoteToSelf)
	h.push(o)
	out := h.plain()
	if strings.Contains(out, "Safety number") || strings.Contains(out, "Nickname") {
		t.Errorf("note to self shows contact rows:\n%s", out)
	}
	if _, ok := NewChatSettings(h.ctx, fake.ChatHike).(*GroupSettings); !ok {
		t.Error("group id should give GroupSettings")
	}
}

func TestProfileEditor(t *testing.T) {
	h := newHarness(t)
	p := NewProfileEditor(h.ctx)
	h.push(p)
	out := h.plain()
	if !strings.Contains(out, "First name") || !strings.Contains(out, "About emoji") {
		t.Fatalf("view:\n%s", out)
	}
	h.key("tab", "tab")
	h.key("ctrl+u")
	h.typ("Out hiking")
	h.key("tab", "ctrl+u")
	h.typ(":tada:")
	h.key("tab", "enter")
	self := h.svc.Self()
	if self.About != "Out hiking" || self.AboutEmoji != "🎉" {
		t.Fatalf("self = %q %q (out %v)", self.About, self.AboutEmoji, h.out)
	}
	if h.top() != nil {
		t.Fatal("editor still open")
	}
	if len(outOf[ui.ToastMsg](h)) == 0 {
		t.Error("no toast")
	}
}

func TestProfileEditorValidation(t *testing.T) {
	h := newHarness(t)
	p := NewProfileEditor(h.ctx)
	h.push(p)
	h.key("tab", "tab", "tab")
	h.typ("notanemoji")
	h.key("tab")
	if !strings.Contains(h.plain(), "single emoji") {
		t.Errorf("no validation error:\n%s", h.plain())
	}
	h.key("esc")
	if h.top() != nil {
		t.Fatal("esc did not close")
	}
}

func TestSettingsLive(t *testing.T) {
	h := newHarness(t)
	h.ctx.Config.Theme = "charm"
	v := NewSettings(h.ctx)
	h.push(v)
	out := h.plain()
	if !strings.Contains(out, "Theme") || !strings.Contains(out, "Appearance") {
		t.Fatalf("view:\n%s", out)
	}
	h.key("j")
	changed := outOf[SettingsChangedMsg](h)
	if len(changed) == 0 {
		t.Fatal("no SettingsChangedMsg after moving the theme cursor")
	}
	if got := changed[len(changed)-1].Config.Theme; got == "charm" {
		t.Errorf("theme unchanged: %q", got)
	}
	for _, c := range outOf[ui.ConfigChangedMsg](h) {
		if c.Config.Theme != "charm" {
			t.Error("theme change not debounced")
		}
	}
	want := changed[len(changed)-1].Config.Theme
	h.send(themeApplyMsg{owner: v, seq: v.themeSeq, name: want})
	cfgs := outOf[ui.ConfigChangedMsg](h)
	if len(cfgs) == 0 || cfgs[len(cfgs)-1].Config.Theme != want || cfgs[len(cfgs)-1].Save {
		t.Errorf("config messages %+v", cfgs)
	}
	h.key("ctrl+s")
	cfgs = outOf[ui.ConfigChangedMsg](h)
	if len(cfgs) == 0 || !cfgs[len(cfgs)-1].Save {
		t.Error("ctrl+s did not ask to save")
	}
	// Go to the privacy page and turn read receipts off.
	before, _ := h.svc.Settings(bg())
	for i := 0; i < 8 && !strings.Contains(h.plain(), "Read receipts"); i++ {
		h.key("tab")
	}
	if !strings.Contains(h.plain(), "Read receipts") {
		t.Fatalf("privacy page not reached:\n%s", h.plain())
	}
	h.key("left")
	after, _ := h.svc.Settings(bg())
	if after.ReadReceipts == before.ReadReceipts {
		t.Errorf("read receipts not toggled live (%v)", after.ReadReceipts)
	}
	for _, sz := range sizes {
		h.view(sz[0], sz[1])
	}
	h.key("esc")
	if h.top() != nil {
		t.Fatal("esc did not close")
	}
}

func TestMemberPickerFuzzy(t *testing.T) {
	h := newHarness(t)
	var got []core.ACI
	h.push(NewMemberPicker(h.ctx, "Add", []core.ACI{fake.BobACI}, func(a []core.ACI) tea.Cmd {
		got = a
		return nil
	}))
	out := h.plain()
	if strings.Contains(out, "Bob") {
		t.Errorf("excluded contact listed:\n%s", out)
	}
	h.typ("grc") // fuzzy: Grace
	out = h.plain()
	if !strings.Contains(out, "Grace") {
		t.Fatalf("fuzzy filter failed:\n%s", out)
	}
	h.key("enter") // nothing toggled: takes the highlighted match
	if len(got) != 1 || got[0] != fake.GraceACI {
		t.Fatalf("got %v", got)
	}
	if h.top() != nil {
		t.Fatal("picker still open")
	}
	// esc clears the filter first, then closes.
	h.push(NewMemberPicker(h.ctx, "Add", nil, nil))
	h.typ("zz")
	h.key("esc")
	if h.top() == nil {
		t.Fatal("esc with filter closed the picker")
	}
	h.key("esc")
	if h.top() != nil {
		t.Fatal("second esc did not close")
	}
}

func TestAllViewsBounds(t *testing.T) {
	for _, th := range []string{"charm", "mono", "catppuccin-latte"} {
		h := newHarnessTheme(t, th)
		views := []ui.Overlay{
			NewSafetyNumber(h.ctx, fake.AliceACI),
			NewNewChat(h.ctx, "bob"),
			NewNewGroup(h.ctx),
			NewGroupSettings(h.ctx, fake.ChatHike),
			NewGroupSettings(h.ctx, core.GroupChatID("nope")),
			NewContactSettings(h.ctx, fake.ChatInes),
			NewContactSettings(h.ctx, core.DirectChatID("nope")),
			NewProfileEditor(h.ctx),
			NewSettings(h.ctx),
			NewMemberPicker(h.ctx, "Add", nil, nil),
			NewQRView(h.ctx, "Link", "https://signal.group/#abc", "caption"),
			NewConfirm(h.ctx, "Sure?", strings.Repeat("Very long question ", 10), "Yes", true, nil),
		}
		for _, v := range views {
			// Unloaded first (no Init), then loaded.
			for _, sz := range sizes {
				renderOverlay(t, v, sz[0], sz[1])
			}
			h.push(v)
			for _, sz := range sizes {
				renderOverlay(t, v, sz[0], sz[1])
			}
			for _, sz := range [][2]int{{20, 8}, {12, 5}} {
				w, hh := v.Size(sz[0], sz[1])
				checkBounds(t, v.View(min(w, sz[0]), min(hh, sz[1])), min(w, sz[0]), min(hh, sz[1]))
			}
			h.stack = nil
		}
	}
}

func TestSanitizesUntrustedText(t *testing.T) {
	h := newHarness(t)
	evil := "Evil\x1b]0;pwned\x07\x1b[31mName\u202e"
	if err := h.svc.SetNickname(bg(), fake.AliceACI, evil); err != nil {
		t.Fatal(err)
	}
	c := NewContactSettings(h.ctx, fake.ChatAlice)
	h.push(c)
	out := h.view(120, 40) + c.Title()
	if strings.Contains(out, "pwned") || strings.Contains(out, "\u202e") || strings.Contains(out, "\x07") {
		t.Fatalf("unsanitized output: %q", ansi.Strip(out))
	}
}
