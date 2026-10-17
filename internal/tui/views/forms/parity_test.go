package forms

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"warte/internal/core"
	"warte/internal/tui/ui"
)

func TestParsePollOptions(t *testing.T) {
	got, err := parsePollOptions(" a \n\n b\n c ")
	if err != nil || !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := parsePollOptions("only one"); err == nil {
		t.Error("one option accepted")
	}
	if _, err := parsePollOptions(strings.Repeat("x\n", pollMaxOptions+1)); err == nil {
		t.Error("too many options accepted")
	}
	if _, err := parsePollOptions("ok\n" + strings.Repeat("é", pollMaxOption+1)); err == nil {
		t.Error("long option accepted")
	}
}

func TestParseVote(t *testing.T) {
	for _, tc := range []struct {
		args  []string
		multi bool
		want  []int
		ok    bool
	}{
		{[]string{"2"}, false, []int{1}, true},
		{[]string{"0"}, false, nil, true},
		{[]string{"none"}, true, nil, true},
		{[]string{"3,1"}, true, []int{0, 2}, true},
		{[]string{"1", "1"}, false, []int{0}, true},
		{[]string{"1", "2"}, false, nil, false},
		{[]string{"4"}, true, nil, false},
		{[]string{"x"}, true, nil, false},
	} {
		got, err := parseVote(tc.args, 3, tc.multi)
		if (err == nil) != tc.ok || (tc.ok && !slices.Equal(got, tc.want)) {
			t.Errorf("parseVote(%q, multi=%v) = %v, %v", tc.args, tc.multi, got, err)
		}
	}
}

func TestClassifyLink(t *testing.T) {
	for u, want := range map[string]LinkKind{
		"https://signal.group/#CjQKIP":                         LinkGroup,
		"sgnl://joingroup?x=1":                                 LinkGroup,
		"https://signal.art/addstickers/#pack_id=ab&pack_key=": LinkStickerPack,
		"sgnl://addstickers?pack_id=ab":                        LinkStickerPack,
		"https://signal.me/#p/+4915112345678":                  LinkContact,
		"https://signal.me/#eu/abc":                            LinkContact,
		"https://signal.group/":                                LinkNone,
		"https://signal.org/download":                          LinkNone,
		"https://example.com/#signal.group":                    LinkNone,
		"javascript:alert(1)":                                  LinkNone,
	} {
		if got := ClassifyLink(u); got != want {
			t.Errorf("ClassifyLink(%q) = %v, want %v", u, got, want)
		}
	}
}

func TestParseStickerLink(t *testing.T) {
	id, key := strings.Repeat("aB", 16), strings.Repeat("0f", 32)
	for _, l := range []string{
		"https://signal.art/addstickers/#pack_id=" + id + "&pack_key=" + key,
		"sgnl://addstickers?pack_id=" + id + "&pack_key=" + key,
	} {
		gid, gkey, err := parseStickerLink(l)
		if err != nil || gid != strings.ToLower(id) || gkey != key {
			t.Errorf("%s: %q %q %v", l, gid, gkey, err)
		}
	}
	for _, l := range []string{
		"https://signal.art/addstickers/#pack_id=" + id,
		"https://signal.art/addstickers/#pack_id=zz&pack_key=" + key,
		"https://example.com/#pack_id=" + id + "&pack_key=" + key,
	} {
		if _, _, err := parseStickerLink(l); err == nil {
			t.Errorf("%s accepted", l)
		}
	}
}

// findPoll returns a poll message of the demo data matching want.
func findPoll(t *testing.T, h *harness, want func(core.Message) bool) core.Message {
	t.Helper()
	chats, err := h.svc.Chats(bg(), core.ChatFilter{IncludeEmpty: true})
	if err != nil {
		t.Fatal(err)
	}
	arch, _ := h.svc.Chats(bg(), core.ChatFilter{IncludeEmpty: true, Archived: true})
	for _, c := range append(chats, arch...) {
		msgs, err := h.svc.Messages(bg(), c.ID, core.MessageID{}, 500)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range msgs {
			if m.Poll != nil && want(m) {
				return m
			}
		}
	}
	t.Skip("demo data has no matching poll")
	return core.Message{}
}

func TestPollComposerSends(t *testing.T) {
	h := newHarness(t)
	chats, _ := h.svc.Chats(bg(), core.ChatFilter{})
	h.chat = chats[0].ID
	h.push(NewPollComposer(h.ctx, h.chat))
	h.typ("Lunch?")
	h.key("tab")
	h.typ("Pizza")
	h.press(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt})
	h.typ("Ramen")
	h.key("tab", "tab") // past the multiple-votes toggle (No)
	h.key("enter")
	if h.top() != nil {
		t.Fatalf("dialog still open:\n%s", h.plain())
	}
	msgs, _ := h.svc.Messages(bg(), h.chat, core.MessageID{}, 5)
	last := msgs[len(msgs)-1]
	if last.Poll == nil || last.Poll.Question != "Lunch?" || len(last.Poll.Options) != 2 ||
		last.Poll.Options[1].Text != "Ramen" || last.Poll.AllowMultiple {
		t.Fatalf("last message is not the poll: %+v", last.Poll)
	}
}

func TestPollComposerValidates(t *testing.T) {
	h := newHarness(t)
	chats, _ := h.svc.Chats(bg(), core.ChatFilter{})
	h.chat = chats[0].ID
	before, _ := h.svc.Messages(bg(), h.chat, core.MessageID{}, 50)
	h.push(NewPollComposer(h.ctx, h.chat))
	h.typ("Q")
	h.key("tab")
	h.typ("only")
	h.key("tab", "tab", "enter")
	after, _ := h.svc.Messages(bg(), h.chat, core.MessageID{}, 50)
	if len(after) != len(before) {
		t.Fatal("a one-option poll was sent")
	}
	if h.top() == nil || !strings.Contains(h.plain(), "two options") {
		t.Fatalf("no validation error:\n%s", h.plain())
	}
}

func TestPollVoteSingle(t *testing.T) {
	h := newHarness(t)
	m := findPoll(t, h, func(m core.Message) bool { return !m.Poll.Ended && !m.Poll.AllowMultiple })
	h.ctx.Selected = func() *core.Message { return &m }
	h.run(pollVote(h.ctx, m))
	if h.top() == nil {
		t.Fatal("vote dialog not opened")
	}
	if !strings.Contains(h.plain(), clean(m.Poll.Options[0].Text)) {
		t.Fatalf("options not shown:\n%s", h.plain())
	}
	h.key("down", "enter")
	got, _ := h.svc.Message(bg(), m.ID)
	mine := myVotes(got.Poll, h.ctx.Self.ACI)
	if len(mine) != 1 {
		t.Fatalf("votes after voting: %v", mine)
	}
}

func TestPollVoteMultiAndRetract(t *testing.T) {
	h := newHarness(t)
	m := findPoll(t, h, func(m core.Message) bool { return !m.Poll.Ended && m.Poll.AllowMultiple })
	h.ctx.Selected = func() *core.Message { return &m }
	// :vote 1,2 then :vote 0
	cmd := findCommand(t, "vote")
	h.run(cmd.Run(h.ctx, []string{"1,2"}))
	got, _ := h.svc.Message(bg(), m.ID)
	if v := myVotes(got.Poll, h.ctx.Self.ACI); !slices.Equal(v, []int{0, 1}) {
		t.Fatalf("votes = %v", v)
	}
	h.run(cmd.Run(h.ctx, []string{"0"}))
	got, _ = h.svc.Message(bg(), m.ID)
	if v := myVotes(got.Poll, h.ctx.Self.ACI); len(v) != 0 {
		t.Fatalf("votes after retract = %v", v)
	}
	// The dialog: toggle the first option and submit.
	h.run(pollVote(h.ctx, got))
	h.key("x", "enter")
	got, _ = h.svc.Message(bg(), m.ID)
	if v := myVotes(got.Poll, h.ctx.Self.ACI); !slices.Equal(v, []int{0}) {
		t.Fatalf("votes via dialog = %v", v)
	}
}

func TestEndPollOwnOnly(t *testing.T) {
	h := newHarness(t)
	cmd := findCommand(t, "endpoll")
	other := findPoll(t, h, func(m core.Message) bool { return !m.Outgoing && !m.Poll.Ended })
	h.ctx.Selected = func() *core.Message { return &other }
	h.run(cmd.Run(h.ctx, nil))
	if h.top() != nil {
		t.Fatal("confirm opened for someone else's poll")
	}
	mine := findPoll(t, h, func(m core.Message) bool { return m.Outgoing && !m.Poll.Ended })
	h.ctx.Selected = func() *core.Message { return &mine }
	h.run(cmd.Run(h.ctx, nil))
	h.key("y")
	got, _ := h.svc.Message(bg(), mine.ID)
	if !got.Poll.Ended {
		t.Fatal("poll not ended")
	}
	// Voting on an ended poll only explains.
	h.run(pollVote(h.ctx, got))
	if h.top() != nil {
		t.Fatal("vote dialog opened for an ended poll")
	}
}

func TestJoinAndStickerLinks(t *testing.T) {
	h := newHarness(t)
	// A link that is not a group link is refused without a dialog.
	h.run(JoinGroupLink(h.ctx, "https://example.com"))
	if h.top() != nil {
		t.Fatal("dialog for a non-group link")
	}
	cmd, ok := OpenSignalLink(h.ctx, "https://signal.group/#bm90LWEtbGluaw")
	if !ok {
		t.Fatal("group link not handled")
	}
	h.run(cmd)
	if h.top() == nil || !strings.Contains(h.plain(), "Join") {
		t.Fatalf("no join confirmation:\n%s", h.plain())
	}
	h.key("n")
	if _, ok := OpenSignalLink(h.ctx, "https://example.com/x"); ok {
		t.Fatal("web link handled in-app")
	}

	packs, err := h.svc.StickerPacks(bg())
	if err != nil {
		t.Fatal(err)
	}
	var p *core.StickerPack
	for i := range packs {
		if !packs[i].Installed && packs[i].Key != "" {
			p = &packs[i]
		}
	}
	if p == nil {
		t.Skip("no uninstalled demo pack")
	}
	cmd, ok = OpenSignalLink(h.ctx, "https://signal.art/addstickers/#pack_id="+p.ID+"&pack_key="+p.Key)
	if !ok {
		t.Fatal("sticker link not handled")
	}
	h.run(cmd)
	h.key("y")
	packs, _ = h.svc.StickerPacks(bg())
	for _, q := range packs {
		if q.ID == p.ID && !q.Installed {
			t.Fatalf("pack not installed; toasts: %v", outOf[ui.ToastMsg](h))
		}
	}
}

func TestExportCommand(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "out.wexp")
	cmd := findCommand(t, "export")
	h.run(cmd.Run(h.ctx, []string{path}))
	// The harness drops commands slower than 40ms (race detector), so wait
	// for the file rather than the toast.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if fi, err := os.Stat(path); err == nil && fi.Size() > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no export file; toasts: %v", outOf[ui.ToastMsg](h))
		}
		time.Sleep(10 * time.Millisecond)
	}
	// An existing file asks before replacing.
	h.run(cmd.Run(h.ctx, []string{path}))
	if h.top() == nil {
		t.Fatal("no overwrite confirmation")
	}
	h.key("n")
	h.run(cmd.Run(h.ctx, []string{dir}))
	if toasts := outOf[ui.ToastMsg](h); toasts[len(toasts)-1].Level != ui.ToastError {
		t.Fatal("exporting to a directory did not fail")
	}
}

func findCommand(t *testing.T, name string) ui.Command {
	t.Helper()
	for _, c := range ui.Commands() {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no :%s command", name)
	return ui.Command{}
}
