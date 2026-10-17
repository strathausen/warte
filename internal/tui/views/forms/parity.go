package forms

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"warte/internal/core"
	"warte/internal/tui/ui"
)

// Polls, joining groups by link, installing sticker packs from a link and
// exporting from inside the TUI (SPEC §6 parity: polls, invite links,
// sticker packs, backup).
//
// Commands:
//
//	:poll                 compose a poll in the open chat
//	:vote [n ...]         vote on the selected poll (no args: dialog)
//	:endpoll              end your own selected poll
//	:join <link>          join a group from a signal.group invite link
//	:stickerpack <link>   install a sticker pack from a signal.art link
//	:export <path>        write an encrypted export of the local data
//
// Enter on a poll message opens the vote dialog (ui.ActPollVote).

// Poll limits, the same as the backend enforces (convert_outgoing.go).
const (
	pollMaxQuestion = 100
	pollMaxOption   = 100
	pollMaxOptions  = 10
)

func init() {
	ui.RegisterLinkHandler(OpenSignalLink)
	ui.RegisterAction(ui.ActPollVote, func(ctx ui.Context) tea.Cmd {
		m := selectedMsg(ctx)
		if m == nil || m.Poll == nil {
			return ui.Toast(ui.ToastInfo, "No poll selected")
		}
		return pollVote(ctx, *m)
	})
	ui.RegisterCommand(ui.Command{
		Name: "poll", Help: "Create a poll in this chat",
		Run: func(ctx ui.Context, _ []string) tea.Cmd {
			chat := currentChat(ctx)
			if chat == "" {
				return ui.Toast(ui.ToastWarning, "Open a chat first")
			}
			return ui.Push(NewPollComposer(ctx, chat))
		},
	})
	ui.RegisterCommand(ui.Command{
		Name: "vote", Args: "[option …]", Help: "Vote on the selected poll (no option: retract with :vote 0)",
		Run: func(ctx ui.Context, args []string) tea.Cmd {
			m := selectedMsg(ctx)
			if m == nil || m.Poll == nil {
				return ui.Toast(ui.ToastWarning, "Select a poll first")
			}
			if len(args) == 0 {
				return pollVote(ctx, *m)
			}
			opts, err := parseVote(args, len(m.Poll.Options), m.Poll.AllowMultiple)
			if err != nil {
				return ui.Toast(ui.ToastError, "vote: "+err.Error())
			}
			id, svc := m.ID, ctx.Svc
			ok := "Vote sent"
			if len(opts) == 0 {
				ok = "Vote removed"
			}
			return runToast(svc, "vote", ok, func(c context.Context, s core.Service) error { return s.VotePoll(c, id, opts) })
		},
	})
	ui.RegisterCommand(ui.Command{
		Name: "endpoll", Help: "End your selected poll",
		Run: func(ctx ui.Context, _ []string) tea.Cmd {
			m := selectedMsg(ctx)
			switch {
			case m == nil || m.Poll == nil:
				return ui.Toast(ui.ToastWarning, "Select a poll first")
			case !m.Outgoing:
				return ui.Toast(ui.ToastWarning, "Only the poll's author can end it")
			case m.Poll.Ended:
				return ui.Toast(ui.ToastInfo, "This poll has already ended")
			}
			id, svc := m.ID, ctx.Svc
			return ui.Push(NewConfirm(ctx, "End poll?", "No one can vote after the poll ends.", "End poll", true,
				runToast(svc, "end poll", "Poll ended", func(c context.Context, s core.Service) error { return s.EndPoll(c, id) })))
		},
	})
	ui.RegisterCommand(ui.Command{
		Name: "join", Args: "<group link>", Help: "Join a group from an invite link",
		Run: func(ctx ui.Context, args []string) tea.Cmd { return JoinGroupLink(ctx, strings.Join(args, "")) },
	})
	ui.RegisterCommand(ui.Command{
		Name: "stickerpack", Aliases: []string{"addstickers"}, Args: "<signal.art link>",
		Help: "Install a sticker pack from a link",
		Run:  func(ctx ui.Context, args []string) tea.Cmd { return InstallStickerLink(ctx, strings.Join(args, "")) },
	})
	ui.RegisterCommand(ui.Command{
		Name: "export", Args: "<path>", Help: "Export an encrypted backup of the local data",
		Run: func(ctx ui.Context, args []string) tea.Cmd { return exportTo(ctx, strings.Join(args, " ")) },
	})
}

// selectedMsg returns the selected message, or nil.
func selectedMsg(ctx ui.Context) *core.Message {
	if ctx.Selected == nil {
		return nil
	}
	return ctx.Selected()
}

// runToast runs fn in a command and reports the outcome as a toast.
func runToast(svc core.Service, op, okText string, fn func(context.Context, core.Service) error) tea.Cmd {
	return func() tea.Msg {
		c, cancel := context.WithTimeout(context.Background(), opTimeout)
		defer cancel()
		if err := fn(c, svc); err != nil {
			return ui.ToastMsg{Level: ui.ToastError, Text: op + ": " + clean(err.Error())}
		}
		return ui.ToastMsg{Level: ui.ToastSuccess, Text: okText}
	}
}

// ---- polls ------------------------------------------------------------------

// parsePollOptions splits the options text (one per line), dropping blank
// lines, and checks Signal's limits.
func parsePollOptions(s string) ([]string, error) {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	switch {
	case len(out) < 2:
		return out, errors.New("a poll needs at least two options")
	case len(out) > pollMaxOptions:
		return out, fmt.Errorf("a poll can have at most %d options", pollMaxOptions)
	}
	for _, o := range out {
		if utf8.RuneCountInString(o) > pollMaxOption {
			return out, fmt.Errorf("options can be at most %d characters", pollMaxOption)
		}
	}
	return out, nil
}

// NewPollComposer is the "create poll" dialog: a question, the options one
// per line and whether people may pick several. It sends the poll to chat.
func NewPollComposer(ctx ui.Context, chat core.ChatID) ui.Overlay {
	var question, options string
	var multi bool
	build := func() *huh.Form {
		return newHuhForm(ctx.Styles, huh.NewGroup(
			huh.NewInput().Title("Question").Value(&question).CharLimit(pollMaxQuestion).
				Validate(nonEmpty("A question")),
			huh.NewText().Title("Options").Description("One per line, 2 to 10").Value(&options).
				Lines(5).ExternalEditor(false).
				Validate(func(s string) error { _, err := parsePollOptions(s); return err }),
			huh.NewConfirm().Title("Allow multiple votes").Value(&multi),
		))
	}
	var o *formOverlay
	o = newFormOverlay(ctx, "New poll", 56, build, func() tea.Cmd {
		opts, err := parsePollOptions(options)
		poll := &core.Poll{Question: strings.TrimSpace(question), AllowMultiple: multi}
		for _, t := range opts {
			poll.Options = append(poll.Options, core.PollOption{Text: t})
		}
		svc := ctx.Svc
		return o.done(func() (tea.Msg, string, error) {
			if err != nil {
				return nil, "", err
			}
			c, cancel := context.WithTimeout(context.Background(), opTimeout)
			defer cancel()
			if _, err := svc.Send(c, chat, core.Outgoing{Poll: poll}); err != nil {
				return nil, "", err
			}
			return nil, "Poll sent", nil
		})
	})
	o.footer = []string{"tab", "next", "alt+enter", "newline", "esc", "cancel"}
	return o
}

// myVotes returns the option indexes the local user voted for.
func myVotes(p *core.Poll, self core.ACI) []int {
	var out []int
	for i, o := range p.Options {
		if slices.Contains(o.Voters, self) {
			out = append(out, i)
		}
	}
	return out
}

// pollVote opens the vote dialog for m, or explains why it can't.
func pollVote(ctx ui.Context, m core.Message) tea.Cmd {
	switch {
	case m.Poll == nil:
		return ui.Toast(ui.ToastInfo, "Not a poll")
	case m.RemoteDeleted:
		return ui.Toast(ui.ToastInfo, "This poll was deleted")
	case m.Poll.Ended:
		return ui.Toast(ui.ToastInfo, "This poll has ended")
	}
	return ui.Push(NewPollVote(ctx, m))
}

// NewPollVote is the vote dialog: a single-choice poll is a select (with
// "Remove my vote" once you voted), a multiple-choice poll a multi-select
// (an empty selection removes your vote).
func NewPollVote(ctx ui.Context, m core.Message) ui.Overlay {
	p := m.Poll
	mine := myVotes(p, ctx.Self.ACI)
	id := m.ID
	label := func(i int) string {
		n := len(p.Options[i].Voters)
		votes := strconv.Itoa(n) + " votes"
		if n == 1 {
			votes = "1 vote"
		}
		return clean(p.Options[i].Text) + "  (" + votes + ")"
	}
	var single int
	multi := slices.Clone(mine)
	if len(mine) > 0 {
		single = mine[0]
	}
	build := func() *huh.Form {
		title := clean(p.Question)
		if p.AllowMultiple {
			var opts []huh.Option[int]
			for i := range p.Options {
				opts = append(opts, huh.NewOption(label(i), i).Selected(slices.Contains(multi, i)))
			}
			return newHuhForm(ctx.Styles, huh.NewGroup(
				huh.NewMultiSelect[int]().Title(title).Description("Choose any").Options(opts...).Value(&multi),
			))
		}
		var opts []huh.Option[int]
		for i := range p.Options {
			opts = append(opts, huh.NewOption(label(i), i))
		}
		if len(mine) > 0 {
			opts = append(opts, huh.NewOption("Remove my vote", -1))
		}
		return newHuhForm(ctx.Styles, huh.NewGroup(
			huh.NewSelect[int]().Title(title).Options(opts...).Value(&single),
		))
	}
	var o *formOverlay
	o = newFormOverlay(ctx, "Vote", 56, build, func() tea.Cmd {
		var choice []int
		if p.AllowMultiple {
			choice = slices.Clone(multi)
			slices.Sort(choice)
		} else if single >= 0 {
			choice = []int{single}
		}
		svc := ctx.Svc
		return o.done(func() (tea.Msg, string, error) {
			c, cancel := context.WithTimeout(context.Background(), opTimeout)
			defer cancel()
			if err := svc.VotePoll(c, id, choice); err != nil {
				return nil, "", err
			}
			if len(choice) == 0 {
				return nil, "Vote removed", nil
			}
			return nil, "Vote sent", nil
		})
	})
	if p.AllowMultiple {
		o.footer = []string{"x", "toggle", "enter", "vote", "esc", "cancel"}
	} else {
		o.footer = []string{"j/k", "move", "enter", "vote", "esc", "cancel"}
	}
	return o
}

// parseVote turns ":vote" arguments (1-based option numbers; "0" or
// "none" retracts) into option indexes.
func parseVote(args []string, n int, multiple bool) ([]int, error) {
	var out []int
	for _, a := range args {
		for _, f := range strings.FieldsFunc(a, func(r rune) bool { return r == ',' }) {
			if f == "0" || strings.EqualFold(f, "none") {
				continue
			}
			v, err := strconv.Atoi(f)
			if err != nil || v < 1 || v > n {
				return nil, fmt.Errorf("option %q: pick 1 to %d", f, n)
			}
			if !slices.Contains(out, v-1) {
				out = append(out, v-1)
			}
		}
	}
	if len(out) > 1 && !multiple {
		return nil, errors.New("this poll allows only one choice")
	}
	slices.Sort(out)
	return out, nil
}

// ---- links ------------------------------------------------------------------

// LinkKind classifies Signal deep links found in messages.
type LinkKind int

const (
	LinkNone        LinkKind = iota
	LinkGroup                // https://signal.group/#… , sgnl://joingroup?…
	LinkStickerPack          // https://signal.art/addstickers/#pack_id=…&pack_key=…, sgnl://addstickers?…
	LinkContact              // https://signal.me/#p/… , #eu/… (phone, username link)
)

// ClassifyLink reports which kind of Signal link u is.
func ClassifyLink(u string) LinkKind {
	pu, err := url.Parse(strings.TrimSpace(u))
	if err != nil {
		return LinkNone
	}
	host := strings.ToLower(pu.Host)
	switch strings.ToLower(pu.Scheme) {
	case "https", "http":
		switch host {
		case "signal.group":
			if pu.Fragment != "" {
				return LinkGroup
			}
		case "signal.art":
			if strings.HasPrefix(pu.Path, "/addstickers") && pu.Fragment != "" {
				return LinkStickerPack
			}
		case "signal.me":
			if pu.Fragment != "" {
				return LinkContact
			}
		}
	case "sgnl":
		switch host {
		case "joingroup", "signal.group":
			return LinkGroup
		case "addstickers":
			return LinkStickerPack
		case "signal.me":
			return LinkContact
		}
	}
	return LinkNone
}

var hex16, hex32 = regexp.MustCompile(`^[0-9a-fA-F]{32}$`), regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// parseStickerLink extracts the pack ID and key from a sticker pack link.
func parseStickerLink(link string) (id, key string, err error) {
	pu, err := url.Parse(strings.TrimSpace(link))
	if err != nil || ClassifyLink(link) != LinkStickerPack {
		return "", "", errors.New("not a sticker pack link")
	}
	raw := pu.Fragment
	if strings.EqualFold(pu.Scheme, "sgnl") {
		raw = pu.RawQuery
	}
	q, err := url.ParseQuery(raw)
	if err != nil {
		return "", "", errors.New("not a sticker pack link")
	}
	id, key = q.Get("pack_id"), q.Get("pack_key")
	if !hex16.MatchString(id) || !hex32.MatchString(key) {
		return "", "", errors.New("the sticker pack link is incomplete")
	}
	return strings.ToLower(id), strings.ToLower(key), nil
}

// JoinGroupLink asks for confirmation and joins the group of an invite
// link, opening it afterwards.
func JoinGroupLink(ctx ui.Context, link string) tea.Cmd {
	link = strings.TrimSpace(link)
	if ClassifyLink(link) != LinkGroup {
		return ui.Toast(ui.ToastWarning, "usage: :join <https://signal.group/#… link>")
	}
	svc := ctx.Svc
	run := func() tea.Msg {
		c, cancel := context.WithTimeout(context.Background(), opTimeout)
		defer cancel()
		id, err := svc.JoinGroupByLink(c, link)
		if err != nil {
			return ui.ToastMsg{Level: ui.ToastError, Text: "join group: " + clean(err.Error())}
		}
		return ui.OpenChatMsg{Chat: id}
	}
	return ui.Push(NewConfirm(ctx, "Join this group?",
		"You'll join the group from this invite link. If an admin must approve new members, a request is sent instead.",
		"Join", false, run))
}

// InstallStickerLink asks for confirmation and installs the sticker pack
// of a signal.art link.
func InstallStickerLink(ctx ui.Context, link string) tea.Cmd {
	id, key, err := parseStickerLink(link)
	if err != nil {
		return ui.Toast(ui.ToastWarning, "stickerpack: "+err.Error())
	}
	svc := ctx.Svc
	return ui.Push(NewConfirm(ctx, "Install sticker pack?",
		"The pack is downloaded and added to your stickers on all your devices.", "Install", false,
		runToast(svc, "install sticker pack", "Sticker pack installed", func(c context.Context, s core.Service) error {
			return s.InstallStickerPack(c, id, key, true)
		})))
}

// OpenSignalLink handles a Signal deep link inside warte. ok is false for
// other links (the caller opens those in the browser).
func OpenSignalLink(ctx ui.Context, link string) (tea.Cmd, bool) {
	switch ClassifyLink(link) {
	case LinkGroup:
		return JoinGroupLink(ctx, link), true
	case LinkStickerPack:
		return InstallStickerLink(ctx, link), true
	case LinkContact:
		svc := ctx.Svc
		return func() tea.Msg {
			c, cancel := context.WithTimeout(context.Background(), opTimeout)
			defer cancel()
			id, err := svc.ResolveRecipient(c, strings.TrimSpace(link))
			if err != nil {
				return ui.ToastMsg{Level: ui.ToastError, Text: "open contact link: " + clean(err.Error())}
			}
			return ui.OpenChatMsg{Chat: id}
		}, true
	}
	return nil, false
}

// ---- export -----------------------------------------------------------------

// exportTo writes an encrypted export to path (made absolute, ~ expanded).
func exportTo(ctx ui.Context, path string) tea.Cmd {
	path = strings.TrimSpace(path)
	if path == "" {
		return ui.Toast(ui.ToastWarning, "usage: :export <path>")
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[1:])
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return ui.Toast(ui.ToastError, "export: "+err.Error())
	}
	if fi, err := os.Stat(abs); err == nil && fi.IsDir() {
		return ui.Toast(ui.ToastError, "export: "+clean(abs)+" is a directory")
	}
	svc := ctx.Svc
	run := func() tea.Msg {
		// Exports of big histories take a while; allow more than opTimeout.
		c, cancel := context.WithTimeout(context.Background(), 10*opTimeout)
		defer cancel()
		if err := svc.ExportBackup(c, abs); err != nil {
			return ui.ToastMsg{Level: ui.ToastError, Text: "export: " + clean(err.Error())}
		}
		return ui.ToastMsg{Level: ui.ToastSuccess, Text: "Exported to " + clean(abs)}
	}
	if _, err := os.Stat(abs); err == nil {
		return ui.Push(NewConfirm(ctx, "Replace "+filepath.Base(abs)+"?", "The file exists and will be overwritten.", "Replace", true, run))
	}
	return tea.Batch(ui.Toast(ui.ToastInfo, "Exporting…"), run)
}
