package finder

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"warte/internal/tui/keymap"
	"warte/internal/tui/ui"
)

func init() {
	ui.RegisterAction(keymap.ActFinderChats, func(ctx ui.Context) tea.Cmd {
		return ui.Push(NewChatSwitcher(ctx))
	})
	ui.RegisterAction(keymap.ActSearchGlobal, func(ctx ui.Context) tea.Cmd {
		return ui.Push(NewSearch(ctx))
	})
	ui.RegisterAction(keymap.ActThemePicker, func(ctx ui.Context) tea.Cmd {
		return ui.Push(NewThemePicker(ctx))
	})
	ui.RegisterCommand(ui.Command{
		Name: "find", Args: "<query>", Help: "Search messages in all chats (from: in: has: before: after:)",
		Complete: func(ctx ui.Context, args []string) []string {
			if len(args) == 0 {
				return nil
			}
			last := args[len(args)-1]
			if strings.HasPrefix(last, "has:") {
				out := make([]string, len(HasTypes))
				for i, h := range HasTypes {
					out[i] = "has:" + h
				}
				return out
			}
			if !strings.Contains(last, ":") {
				return []string{"from:", "in:", "has:", "before:", "after:", "on:"}
			}
			return nil
		},
		Run: func(ctx ui.Context, args []string) tea.Cmd {
			return ui.Push(NewSearchQuery(ctx, strings.Join(args, " ")))
		},
	})
	ui.RegisterCommand(ui.Command{
		Name: "chats", Args: "[query]", Help: "Fuzzy chat switcher",
		Run: func(ctx ui.Context, args []string) tea.Cmd {
			return ui.Push(NewChatSwitcherQuery(ctx, strings.Join(args, " ")))
		},
	})
	ui.RegisterCommand(ui.Command{
		Name: "themes", Help: "Pick a theme with preview",
		Run: func(ctx ui.Context, _ []string) tea.Cmd { return ui.Push(NewThemePicker(ctx)) },
	})
	ui.RegisterCommand(ui.Command{
		Name: "forward", Help: "Forward the selected message to several chats",
		Run: func(ctx ui.Context, _ []string) tea.Cmd {
			if ctx.Selected == nil {
				return ui.Toast(ui.ToastWarning, "No message selected")
			}
			m := ctx.Selected()
			if m == nil {
				return ui.Toast(ui.ToastWarning, "No message selected")
			}
			return ui.Push(NewForwardPicker(ctx, m.ID))
		},
	})
}
