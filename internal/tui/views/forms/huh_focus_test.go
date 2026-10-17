package forms

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
)

// TestHuhFastTypingAfterTab feeds "Lunch?" tab "Pizza" tab "Pasta" as one
// burst: every key reaches the overlay before any command it returned has
// run, as when the terminal delivers them in one read. huh's focus change is
// a command (NextField), so before the fix "P" landed in the first field.
func TestHuhFastTypingAfterTab(t *testing.T) {
	h := newHarness(t)
	var q, a, b string
	build := func() *huh.Form {
		return newHuhForm(h.ctx.Styles, huh.NewGroup(
			huh.NewInput().Title("Question").Value(&q),
			huh.NewInput().Title("Option 1").Value(&a),
			huh.NewInput().Title("Option 2").Value(&b),
		))
	}
	o := newFormOverlay(h.ctx, "Burst", 56, build, nil)
	h.push(o)
	_ = h.view(120, 40)

	var pending []tea.Cmd
	feed := func(msg tea.KeyPressMsg) {
		ov, cmd := h.top().Update(msg)
		if ov == nil {
			t.Fatal("form closed")
		}
		pending = append(pending, cmd)
	}
	burst := func(s string) {
		for _, r := range s {
			feed(tea.KeyPressMsg{Code: r, Text: string(r)})
		}
	}
	burst("Lunch?")
	feed(keyMsg("tab"))
	burst("Pizza")
	feed(keyMsg("tab"))
	burst("Pasta")
	feed(keyMsg("shift+tab"))
	burst("!")
	// Only now do the queued commands deliver their messages.
	for _, c := range pending {
		h.run(c)
	}
	_ = h.view(120, 40)

	if q != "Lunch?" || a != "Pizza!" || b != "Pasta" {
		t.Fatalf("values = %q, %q, %q; want %q, %q, %q", q, a, b, "Lunch?", "Pizza!", "Pasta")
	}
}

// TestHuhFastTypingAcrossGroups checks the second hop: tab on the last
// field of a group moves to the next group through another command.
func TestHuhFastTypingAcrossGroups(t *testing.T) {
	h := newHarness(t)
	var a, b string
	build := func() *huh.Form {
		return newHuhForm(h.ctx.Styles,
			huh.NewGroup(huh.NewInput().Title("First").Value(&a)),
			huh.NewGroup(huh.NewInput().Title("Second").Value(&b)),
		)
	}
	o := newFormOverlay(h.ctx, "Groups", 56, build, func() tea.Cmd { return nil })
	h.push(o)
	_ = h.view(120, 40)
	var pending []tea.Cmd
	for _, msg := range []tea.KeyPressMsg{keyMsg("x"), keyMsg("tab"), keyMsg("y"), keyMsg("z")} {
		_, cmd := h.top().Update(msg)
		pending = append(pending, cmd)
	}
	for _, c := range pending {
		h.run(c)
	}
	if a != "x" || b != "yz" {
		t.Fatalf("values = %q, %q; want x, yz", a, b)
	}
}

// TestSettingsNotificationsSingleSource: the notifications group has one
// field, the local config value; the account-level level is not edited.
func TestSettingsNotificationsSingleSource(t *testing.T) {
	h := newHarness(t)
	v := NewSettings(h.ctx)
	h.push(v)
	before := v.s.NotificationLevel
	// Walk to the Notifications group (5 appearance fields) and pick
	// "Name only".
	for i := 0; i < 5; i++ {
		h.key("tab")
	}
	out := h.plain()
	if strings.Contains(out, "synced") || !strings.Contains(out, "Notification content") {
		t.Fatalf("notifications group:\n%s", out)
	}
	h.key("down")
	c, s := v.Values()
	if c.Notifications == h.ctx.Config.Notifications {
		t.Fatalf("local notifications unchanged: %q", c.Notifications)
	}
	if s.NotificationLevel != before {
		t.Fatal("account notification level edited")
	}
}
