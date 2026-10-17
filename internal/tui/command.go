package tui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"warte/internal/core"
	"warte/internal/format"
	"warte/internal/tui/keymap"
	"warte/internal/tui/theme"
	"warte/internal/tui/ui"
)

// Command is an ex command. Feature packages contribute ui.Command values
// (adapted with registerUICommand); built-ins use the richer *App signature.
type Command struct {
	Name    string
	Aliases []string
	Args    string
	Help    string
	// Complete returns candidates for the last (possibly partial) argument.
	Complete func(a *App, args []string) []string
	// Run executes the command.
	Run func(a *App, args []string) tea.Cmd
}

// RegisterCommand adds a command to this App (replacing one with the same
// name).
func (a *App) RegisterCommand(c Command) {
	if a.commands == nil {
		a.commands = map[string]*Command{}
	}
	cc := c
	a.commands[strings.ToLower(c.Name)] = &cc
	for _, al := range c.Aliases {
		a.commands[strings.ToLower(al)] = &cc
	}
	names := make([]string, 0, len(a.commands))
	seen := map[string]bool{}
	for _, cmd := range a.commands {
		if !seen[cmd.Name] {
			seen[cmd.Name] = true
			names = append(names, cmd.Name)
		}
	}
	sort.Strings(names)
	a.cmdNames = names
}

func (a *App) registerUICommand(c ui.Command) {
	uc := c
	cmd := Command{Name: uc.Name, Aliases: uc.Aliases, Args: uc.Args, Help: uc.Help}
	cmd.Run = func(a *App, args []string) tea.Cmd { return uc.Run(a.uictx, args) }
	if uc.Complete != nil {
		cmd.Complete = func(a *App, args []string) []string { return uc.Complete(a.uictx, args) }
	}
	a.RegisterCommand(cmd)
}

// ---- command line editor -------------------------------------------------------------

type cmdLine struct {
	input   textinput.Model
	history []string
	histIdx int
	stash   string
	// completion cycling
	cands   []string
	candIdx int
	prefix  string // the line up to the completed word
}

func newCmdLine(a *App) *cmdLine {
	ti := textinput.New()
	ti.Prompt = ""
	ti.CharLimit = 1024
	return &cmdLine{input: ti, histIdx: -1}
}

func (c *cmdLine) restyle(a *App) {
	styleInput(&c.input, a.styles)
}

func styleInput(ti *textinput.Model, st *theme.Styles) {
	p := st.Theme.Palette
	s := textinput.DefaultStyles(st.Theme.Dark)
	s.Focused.Text = lipgloss.NewStyle().Foreground(p.Fg)
	s.Blurred.Text = lipgloss.NewStyle().Foreground(p.FgMuted)
	s.Focused.Placeholder = st.Placeholder
	s.Blurred.Placeholder = st.Placeholder
	s.Focused.Prompt = lipgloss.NewStyle().Foreground(p.Accent)
	s.Cursor.Color = p.Accent
	s.Cursor.Blink = false
	ti.SetStyles(s)
}

// openCmdLine enters Command mode with an optional prefilled line.
func (a *App) openCmdLine(prefill string) tea.Cmd {
	a.cmdline.input.SetValue(prefill)
	a.cmdline.input.CursorEnd()
	a.cmdline.histIdx = -1
	a.cmdline.cands = nil
	a.keys.SetMode(keymap.Command)
	return a.cmdline.input.Focus()
}

func (a *App) closeCmdLine() {
	a.cmdline.input.Blur()
	a.cmdline.input.SetValue("")
	a.cmdline.cands = nil
	if a.focus == FocusComposer && a.conv.open {
		// Opened from Insert mode (ctrl+a → ":attach "): go back to the
		// composer, which is still focused.
		a.keys.SetMode(keymap.Insert)
		return
	}
	a.keys.SetMode(keymap.Normal)
	if a.focus == FocusComposer {
		a.focus = FocusConversation
	}
}

func (a *App) cmdLineKey(msg tea.KeyPressMsg) tea.Cmd {
	if msg.String() == "backspace" && a.cmdline.input.Value() == "" {
		a.closeCmdLine()
		return nil
	}
	var cmd tea.Cmd
	a.cmdline.input, cmd = a.cmdline.input.Update(msg)
	a.cmdline.cands = nil
	return cmd
}

func (a *App) cmdHistory(dir int) {
	c := a.cmdline
	if len(c.history) == 0 {
		return
	}
	if c.histIdx == -1 {
		c.stash = c.input.Value()
		if dir > 0 {
			return
		}
		c.histIdx = len(c.history)
	}
	c.histIdx += dir
	if c.histIdx >= len(c.history) {
		c.histIdx = -1
		c.input.SetValue(c.stash)
	} else {
		c.histIdx = clamp(c.histIdx, 0, len(c.history)-1)
		c.input.SetValue(c.history[c.histIdx])
	}
	c.input.CursorEnd()
}

// cmdComplete completes the word before the cursor, cycling on repeat.
func (a *App) cmdComplete(dir int) {
	c := a.cmdline
	if c.cands == nil {
		line := c.input.Value()
		fields := strings.Fields(line)
		trailing := strings.HasSuffix(line, " ")
		var cands []string
		var prefix, word string
		if len(fields) == 0 || (len(fields) == 1 && !trailing) {
			if len(fields) == 1 {
				word = fields[0]
			}
			for _, n := range a.cmdNames {
				if strings.HasPrefix(n, strings.ToLower(word)) {
					cands = append(cands, n)
				}
			}
		} else {
			cmd := a.commands[strings.ToLower(fields[0])]
			args := fields[1:]
			if trailing {
				args = append(args, "")
			}
			word = args[len(args)-1]
			prefix = strings.TrimSuffix(line, word)
			if cmd != nil && cmd.Complete != nil {
				all := cmd.Complete(a, args)
				for _, cand := range all {
					if strings.HasPrefix(strings.ToLower(cand), strings.ToLower(word)) {
						cands = append(cands, cand)
					}
				}
				// Arguments with spaces (chat names, paths): complete the
				// whole argument text when it matches.
				if len(args) > 1 {
					rest := strings.TrimLeft(line[strings.Index(line, fields[0])+len(fields[0]):], " ")
					var multi []string
					for _, cand := range all {
						if strings.HasPrefix(strings.ToLower(cand), strings.ToLower(rest)) {
							multi = append(multi, cand)
						}
					}
					if len(multi) > 0 {
						cands, word = multi, rest
						prefix = strings.TrimSuffix(line, rest)
					}
				}
			}
		}
		if len(cands) == 0 {
			return
		}
		c.cands, c.prefix, c.candIdx = cands, prefix, -1
		if len(cands) == 1 {
			v := prefix + cands[0]
			if !strings.HasSuffix(v, "/") {
				v += " "
			}
			c.input.SetValue(v)
			c.input.CursorEnd()
			c.cands = nil
			return
		}
	}
	if c.candIdx < 0 && dir < 0 {
		// Shift-Tab on a fresh completion starts at the last candidate.
		c.candIdx = 0
	}
	c.candIdx = (c.candIdx + dir + len(c.cands)) % len(c.cands)
	c.input.SetValue(c.prefix + c.cands[c.candIdx])
	c.input.CursorEnd()
}

func (a *App) cmdExecute() tea.Cmd {
	line := strings.TrimSpace(a.cmdline.input.Value())
	a.closeCmdLine()
	if line == "" {
		return nil
	}
	h := a.cmdline.history
	if len(h) == 0 || h[len(h)-1] != line {
		a.cmdline.history = append(h, line)
		if len(a.cmdline.history) > 200 {
			a.cmdline.history = a.cmdline.history[1:]
		}
	}
	return a.runCommandLine(line)
}

// runCommandLine parses and runs "name args...".
func (a *App) runCommandLine(line string) tea.Cmd {
	line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), ":"))
	if line == "" {
		return nil
	}
	fields := strings.Fields(line)
	name := strings.ToLower(fields[0])
	bang := false
	if strings.HasSuffix(name, "!") && len(name) > 1 {
		if _, ok := a.commands[name]; !ok {
			name = strings.TrimSuffix(name, "!")
			bang = true
		}
	}
	cmd, ok := a.commands[name]
	if !ok {
		// Unique prefix match.
		var match *Command
		for _, n := range a.cmdNames {
			if strings.HasPrefix(n, name) {
				if match != nil {
					match = nil
					break
				}
				match = a.commands[n]
			}
		}
		if match == nil {
			return a.toast(ui.ToastError, "Unknown command: "+clean(fields[0]))
		}
		cmd = match
	}
	args := fields[1:]
	if bang {
		args = append([]string{"!"}, args...)
	}
	return cmd.Run(a, args)
}

// renderCmdLine renders the command/search input for the status line.
func (a *App) renderCmdLine(w int) string {
	st := a.styles
	in := &a.cmdline.input
	prefix := ":"
	if a.keys.Mode() == keymap.Search {
		in = &a.search.input
		prefix = "/"
	}
	cands := a.keys.Mode() == keymap.Command && len(a.cmdline.cands) > 1
	if cands {
		// The input pads itself to its width; keep it tight so the
		// candidates are visible after it.
		in.SetWidth(clamp(width(in.Value())+1, 1, max(w-2, 1)))
	} else {
		in.SetWidth(max(w-2, 1))
	}
	// Re-derive the horizontal scroll offset for the (possibly changed) width.
	in.SetCursor(in.Position())
	s := st.KeyHintKey.Render(prefix) + in.View()
	if cands {
		// Show a window of candidates around the current one (wildmenu).
		avail := w - width(s) - 2
		start := max(a.cmdline.candIdx, 0)
		for start > 0 {
			n := 0
			for i := start - 1; i <= max(a.cmdline.candIdx, 0); i++ {
				n += width(a.cmdline.cands[i]) + 1
			}
			if n > avail {
				break
			}
			start--
		}
		var parts []string
		used := 0
		if start > 0 {
			parts = append(parts, st.KeyHintSep.Render("<"))
			used += 2
		}
		for i := start; i < len(a.cmdline.cands); i++ {
			c := clean(a.cmdline.cands[i])
			if used+width(c)+1 > avail {
				parts = append(parts, st.KeyHintSep.Render(">"))
				break
			}
			used += width(c) + 1
			if i == a.cmdline.candIdx {
				parts = append(parts, st.KeyHintKey.Reverse(true).Render(c))
			} else {
				parts = append(parts, st.KeyHintDesc.Render(c))
			}
		}
		s += "  " + strings.Join(parts, " ")
	}
	return fit(s, w)
}

// ---- built-in commands --------------------------------------------------------------------

func (a *App) registerBuiltinCommands() {
	durations := func(*App, []string) []string {
		return []string{"off", "30s", "5m", "1h", "8h", "1d", "1w", "4w"}
	}
	chatNames := func(a *App, args []string) []string {
		ctx, cancel := a.queryCtx()
		defer cancel()
		chats, _ := a.svc.Chats(ctx, core.ChatFilter{IncludeEmpty: true})
		out := make([]string, 0, len(chats))
		for _, c := range chats {
			out = append(out, clean(c.Title))
		}
		return out
	}
	quit := func(a *App, _ []string) tea.Cmd { return a.quit() }
	reg := a.RegisterCommand

	reg(Command{Name: "quit", Aliases: []string{"q", "qa"}, Help: "Quit warte", Run: quit})
	reg(Command{Name: "write", Aliases: []string{"w"}, Help: "Save changed settings (theme, layout, mouse, …) to config.toml",
		Run: func(a *App, _ []string) tea.Cmd { return a.saveConfig() }})
	reg(Command{Name: "wq", Aliases: []string{"x"}, Help: "Save changed settings and quit", Run: func(a *App, _ []string) tea.Cmd {
		var save tea.Cmd
		if len(a.cfgDirty) > 0 {
			save = a.saveConfig()
		}
		return tea.Sequence(save, a.quit())
	}})
	reg(Command{Name: "lock", Help: "Lock the screen (asks for a lock password the first time)", Run: func(a *App, _ []string) tea.Cmd {
		return a.lockScreen()
	}})
	reg(Command{Name: "help", Aliases: []string{"h"}, Help: "Show key bindings", Run: func(a *App, _ []string) tea.Cmd {
		return a.pushOverlay(newHelpOverlay(a))
	}})
	reg(Command{Name: "open", Args: "<chat>", Help: "Open a chat by name", Complete: chatNames, Run: func(a *App, args []string) tea.Cmd {
		q := strings.Join(args, " ")
		if q == "" {
			return a.pushOverlay(newChatPicker(a, "Open chat", func(id core.ChatID) tea.Cmd { return a.openChat(id, nil, true) }))
		}
		ctx, cancel := a.queryCtx()
		defer cancel()
		chats, _ := a.svc.Chats(ctx, core.ChatFilter{IncludeEmpty: true})
		if c := bestChat(chats, q); c != nil {
			return a.openChat(c.ID, nil, true)
		}
		archived, _ := a.svc.Chats(ctx, core.ChatFilter{Archived: true, IncludeEmpty: true})
		if c := bestChat(archived, q); c != nil {
			return a.openChat(c.ID, nil, true)
		}
		return a.toast(ui.ToastWarning, "No chat matches "+clean(q))
	}})
	reg(Command{Name: "new", Args: "<phone|username|name>", Help: "Start a chat", Run: func(a *App, args []string) tea.Cmd {
		q := strings.TrimSpace(strings.Join(args, " "))
		if q == "" {
			return a.toast(ui.ToastWarning, "usage: :new <phone|username|name>")
		}
		svc := a.svc
		return func() tea.Msg {
			ctx, cancel := a.opCtx()
			defer cancel()
			id, err := svc.ResolveRecipient(ctx, q)
			if err != nil {
				return errMsg{op: "new chat", err: err}
			}
			return ui.OpenChatMsg{Chat: id}
		}
	}})
	reg(Command{Name: "archive", Help: "Archive the chat", Run: func(a *App, _ []string) tea.Cmd {
		c := a.targetChat()
		if c == nil {
			return nil
		}
		v := true
		return a.chatUpdate(c, core.ChatUpdate{Archived: &v}, "Archived "+clean(c.Title))
	}})
	reg(Command{Name: "unarchive", Help: "Unarchive the chat", Run: func(a *App, _ []string) tea.Cmd {
		c := a.targetChat()
		if c == nil {
			return nil
		}
		v := false
		return a.chatUpdate(c, core.ChatUpdate{Archived: &v}, "Unarchived "+clean(c.Title))
	}})
	reg(Command{Name: "mute", Args: "[duration|off]", Help: "Mute the chat", Complete: durations, Run: func(a *App, args []string) tea.Cmd {
		if len(args) == 0 {
			c := a.targetChat()
			if c == nil {
				return nil
			}
			forever := time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
			return a.chatUpdate(c, core.ChatUpdate{MutedUntil: &forever}, "Muted "+clean(c.Title))
		}
		d, err := format.ParseDuration(strings.Join(args, " "))
		if err != nil {
			return a.toast(ui.ToastError, "mute: "+err.Error())
		}
		return a.toggleMute(d, true)
	}})
	reg(Command{Name: "unmute", Help: "Unmute the chat", Run: func(a *App, _ []string) tea.Cmd { return a.toggleMute(0, true) }})
	reg(Command{Name: "pin", Help: "Pin the selected message (in a chat) or the chat (in the list)", Run: func(a *App, _ []string) tea.Cmd {
		if cmd, ok := a.pinSelectedMessage(true); ok {
			return cmd
		}
		c := a.targetChat()
		if c == nil {
			return nil
		}
		v := true
		return a.chatUpdate(c, core.ChatUpdate{Pinned: &v}, "Pinned "+clean(c.Title))
	}})
	reg(Command{Name: "unpin", Help: "Unpin the selected message (in a chat) or the chat (in the list)", Run: func(a *App, _ []string) tea.Cmd {
		if cmd, ok := a.pinSelectedMessage(false); ok {
			return cmd
		}
		c := a.targetChat()
		if c == nil {
			return nil
		}
		v := false
		return a.chatUpdate(c, core.ChatUpdate{Pinned: &v}, "Unpinned "+clean(c.Title))
	}})
	reg(Command{Name: "disappear", Aliases: []string{"timer"}, Args: "<duration|off>", Help: "Set disappearing messages", Complete: durations,
		Run: func(a *App, args []string) tea.Cmd {
			if !a.conv.open {
				return a.toast(ui.ToastWarning, "Open a chat first")
			}
			d, err := format.ParseDuration(strings.Join(args, " "))
			if err != nil || len(args) == 0 {
				return a.toast(ui.ToastError, "usage: :disappear <1w|8h|off>")
			}
			c := a.conv.chat
			msg := "Disappearing messages off"
			if d > 0 {
				msg = "Messages disappear after " + format.FormatDurationLong(d)
			}
			return a.chatUpdate(&c, core.ChatUpdate{ExpireTimer: &d}, msg)
		}})
	reg(Command{Name: "block", Help: "Block the chat", Run: func(a *App, _ []string) tea.Cmd {
		c := a.targetChat()
		if c == nil {
			return nil
		}
		id, title := c.ID, clean(c.Title)
		return a.pushOverlay(newConfirm(a, "Block "+title+"?", "Blocked people can't message or call you.",
			a.do("block", func(ctx contextT) error { return a.svc.Block(ctx, id, true) }, "Blocked "+title)))
	}})
	reg(Command{Name: "unblock", Help: "Unblock the chat", Run: func(a *App, _ []string) tea.Cmd {
		c := a.targetChat()
		if c == nil {
			return nil
		}
		id, title := c.ID, clean(c.Title)
		return a.do("unblock", func(ctx contextT) error { return a.svc.Block(ctx, id, false) }, "Unblocked "+title)
	}})
	reg(Command{Name: "accept", Help: "Accept a message request", Run: func(a *App, _ []string) tea.Cmd {
		c := a.targetChat()
		if c == nil {
			return nil
		}
		id := c.ID
		return a.do("accept", func(ctx contextT) error { return a.svc.AcceptMessageRequest(ctx, id) }, "Request accepted")
	}})
	reg(Command{Name: "report", Help: "Report spam and block", Run: func(a *App, _ []string) tea.Cmd {
		c := a.targetChat()
		if c == nil {
			return nil
		}
		id := c.ID
		return a.pushOverlay(newConfirm(a, "Report spam?", "Signal is notified and the chat is blocked.",
			a.do("report", func(ctx contextT) error { return a.svc.ReportSpam(ctx, id) }, "Reported")))
	}})
	reg(Command{Name: "delete", Help: "Delete the chat", Run: func(a *App, args []string) tea.Cmd { return a.confirmDeleteChat() }})
	reg(Command{Name: "theme", Args: "<name>", Help: "Switch theme", Complete: func(*App, []string) []string { return theme.Names() },
		Run: func(a *App, args []string) tea.Cmd {
			if len(args) == 0 {
				return a.toast(ui.ToastInfo, "Theme: "+a.styles.Theme.Name+"  (themes: "+strings.Join(theme.Names(), ", ")+")")
			}
			name := strings.ToLower(args[0])
			var t theme.Theme
			if name == theme.AutoName {
				t = theme.Auto(a.styles.Theme.Dark)
			} else {
				var ok bool
				t, ok = theme.Get(name)
				if !ok {
					return a.toast(ui.ToastError, "Unknown theme: "+clean(args[0]))
				}
			}
			a.previewing = false // a previewed theme is now the chosen one
			a.setTheme(t)
			if a.cfg.Theme != name {
				a.cfg.Theme = name
				a.uictx.Config.Theme = name
				a.markDirty("theme")
			}
			return a.toast(ui.ToastSuccess, "Theme: "+t.Name+"  (:w saves)")
		}})
	reg(Command{Name: "set", Args: "<option>", Help: "Change a setting (mouse, nomouse, mouse!, layout=bubbles|irc, [no]reducemotion, [no]sidebar)",
		Complete: func(a *App, args []string) []string {
			if len(args) <= 1 {
				return []string{"mouse", "nomouse", "mouse!", "layout", "reducemotion", "noreducemotion", "sidebar", "nosidebar"}
			}
			if args[0] == "layout" {
				return []string{"bubbles", "irc"}
			}
			return nil
		},
		Run: func(a *App, args []string) tea.Cmd { return a.setOption(args) }})
	reg(Command{Name: "attach", Args: "<path>", Help: "Attach a file", Complete: completePath, Run: func(a *App, args []string) tea.Cmd {
		return a.stageAttachment(strings.Join(args, " "))
	}})
	reg(Command{Name: "viewonce", Help: "Toggle view-once for the attached photo or video", Run: func(a *App, _ []string) tea.Cmd {
		return a.toggleViewOnce()
	}})
	reg(Command{Name: "search", Args: "<query>", Help: "Search this chat (from: has: before: after: on:; in: searches other chats)",
		Complete: completeSearchFilters,
		Run: func(a *App, args []string) tea.Cmd {
			if len(args) == 0 {
				return a.startSearch(scopeConv)
			}
			if hasSearchFilter(args) {
				return a.filteredSearch(args)
			}
			return a.runSearch(strings.Join(args, " "))
		}})
	reg(Command{Name: "react", Args: "<emoji>", Help: "React to the selected message", Run: func(a *App, args []string) tea.Cmd {
		m, cmd := a.requireSelected()
		if m == nil {
			return cmd
		}
		if len(args) == 0 {
			return a.openReactionBar(m)
		}
		e := args[0]
		if em, ok := format.Shortcode(e); ok {
			e = em
		}
		return a.react(m.ID, e, true)
	}})
	reg(Command{Name: "reply", Help: "Reply to the selected message", Run: func(a *App, _ []string) tea.Cmd {
		m, cmd := a.requireSelected()
		if m == nil {
			return cmd
		}
		return a.startReply(m.ID)
	}})
	reg(Command{Name: "edit", Help: "Edit the selected message", Run: func(a *App, _ []string) tea.Cmd {
		m, cmd := a.requireSelected()
		if m == nil {
			return cmd
		}
		return a.startEdit(m)
	}})
	reg(Command{Name: "deletemsg", Aliases: []string{"rm"}, Args: "[!]", Help: "Delete the selected message (! = for everyone)", Run: func(a *App, args []string) tea.Cmd {
		m, cmd := a.requireSelected()
		if m == nil {
			return cmd
		}
		return a.confirmDelete([]core.Message{*m}, len(args) > 0 && args[0] == "!")
	}})
	reg(Command{Name: "read", Help: "Mark the chat as read", Run: func(a *App, _ []string) tea.Cmd {
		c := a.targetChat()
		if c == nil {
			return nil
		}
		id := c.ID
		return a.do("mark read", func(ctx contextT) error { return a.svc.MarkRead(ctx, id, core.MessageID{}) }, "Marked as read")
	}})
	reg(Command{Name: "unread", Help: "Mark the chat as unread", Run: func(a *App, _ []string) tea.Cmd {
		c := a.targetChat()
		if c == nil {
			return nil
		}
		v := true
		return a.chatUpdate(c, core.ChatUpdate{MarkedUnread: &v}, "Marked as unread")
	}})
	reg(Command{Name: "info", Help: "Show message details", Run: func(a *App, _ []string) tea.Cmd {
		m, cmd := a.requireSelected()
		if m == nil {
			return cmd
		}
		if cmd := a.dispatchRegistered(keymap.ActMessageInfo); cmd != nil {
			return cmd
		}
		return a.pushOverlay(newInfoOverlay(a, *m))
	}})
	reg(Command{Name: "sidebar", Help: "Toggle the chat info sidebar", Run: func(a *App, _ []string) tea.Cmd {
		return a.action(keymap.Result{Action: keymap.ActSidebarToggle, Count: 1})
	}})
	reg(Command{Name: "folder", Args: "<name>", Help: "Switch chat folder", Complete: func(a *App, _ []string) []string {
		var out []string
		for _, t := range a.list.tabs {
			out = append(out, t.name)
		}
		return out
	}, Run: func(a *App, args []string) tea.Cmd {
		q := strings.ToLower(strings.Join(args, " "))
		for i, t := range a.list.tabs {
			if strings.ToLower(t.name) == q || (q != "" && strings.HasPrefix(strings.ToLower(t.name), q)) {
				a.switchTab(i - a.list.tab)
				return nil
			}
		}
		return a.toast(ui.ToastWarning, "No folder "+clean(q))
	}})
}

// setOption implements :set.
func (a *App) setOption(args []string) tea.Cmd {
	if len(args) == 0 {
		return a.toast(ui.ToastInfo, "mouse="+boolStr(a.mouse)+" layout="+a.cfg.Layout+" reducemotion="+boolStr(a.cfg.ReduceMotion))
	}
	opt := strings.ToLower(strings.Join(args, " "))
	opt = strings.ReplaceAll(opt, " = ", "=")
	opt = strings.ReplaceAll(opt, " ", "=")
	switch opt {
	case "mouse":
		a.mouse = true
	case "nomouse":
		a.mouse = false
	case "mouse!", "invmouse":
		a.mouse = !a.mouse
	case "reducemotion":
		a.cfg.ReduceMotion = true
	case "noreducemotion":
		a.cfg.ReduceMotion = false
	case "sidebar":
		a.sidebarOpen = true
	case "nosidebar":
		a.sidebarOpen = false
		if a.focus == FocusSidebar {
			a.setFocus(FocusConversation)
		}
	case "layout=bubbles", "layout=irc":
		a.cfg.Layout = strings.TrimPrefix(opt, "layout=")
		a.conv.invalidate()
		a.markDirty("layout")
		a.uictx.Config = a.cfg
		return a.toast(ui.ToastSuccess, "Layout: "+a.cfg.Layout)
	default:
		return a.toast(ui.ToastError, "Unknown option: "+clean(opt))
	}
	if strings.Contains(opt, "mouse") && a.cfg.Mouse != a.mouse {
		a.cfg.Mouse = a.mouse
		a.markDirty("mouse")
	}
	if strings.Contains(opt, "reducemotion") {
		a.markDirty("reduce_motion")
	}
	a.uictx.Config = a.cfg
	if strings.Contains(opt, "mouse") {
		return a.toast(ui.ToastInfo, "Mouse "+map[bool]string{true: "on", false: "off"}[a.mouse])
	}
	return nil
}

func boolStr(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// completePath completes filesystem paths for :attach.
func completePath(_ *App, args []string) []string {
	word := strings.Join(args, " ")
	exp := expandHome(word)
	dir, base := filepath.Split(exp)
	if dir == "" {
		dir = "."
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	shownDir, _ := filepath.Split(word)
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, ".") && !strings.HasPrefix(base, ".") {
			continue
		}
		if !strings.HasPrefix(n, base) {
			continue
		}
		if e.IsDir() {
			n += "/"
		}
		out = append(out, shownDir+n)
		if len(out) >= 50 {
			break
		}
	}
	return out
}

// bestChat finds the chat whose title best matches q.
func bestChat(chats []core.Chat, q string) *core.Chat {
	lq := strings.ToLower(q)
	for i := range chats {
		if strings.ToLower(chats[i].Title) == lq {
			return &chats[i]
		}
	}
	for i := range chats {
		if strings.HasPrefix(strings.ToLower(chats[i].Title), lq) {
			return &chats[i]
		}
	}
	for i := range chats {
		if strings.Contains(strings.ToLower(chats[i].Title), lq) {
			return &chats[i]
		}
	}
	return nil
}
