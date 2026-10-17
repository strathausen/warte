package info

import (
	tea "charm.land/bubbletea/v2"

	"warte/internal/core"
	"warte/internal/tui/keymap"
	"warte/internal/tui/ui"
)

func init() {
	ui.RegisterAction(keymap.ActCalls, func(ctx ui.Context) tea.Cmd {
		return ui.Push(NewCallLog(ctx))
	})
	ui.RegisterCommand(ui.Command{
		Name: "calls", Aliases: []string{"calllog"},
		Help: "Show the call history",
		Run:  func(ctx ui.Context, _ []string) tea.Cmd { return ui.Push(NewCallLog(ctx)) },
	})
	ui.RegisterCommand(ui.Command{
		Name: "pinned", Aliases: []string{"pins"},
		Help: "List the pinned messages of this chat",
		Run: func(ctx ui.Context, _ []string) tea.Cmd {
			var chat core.ChatID
			if ctx.Chat != nil {
				chat = ctx.Chat()
			}
			if chat == "" {
				return ui.Toast(ui.ToastWarning, "Open a chat first")
			}
			return ui.Push(NewPinnedMessages(ctx, chat, nil))
		},
	})
	ui.RegisterCommand(ui.Command{
		Name: "details", Aliases: []string{"msginfo"},
		Help: "Show details of the selected message",
		Run: func(ctx ui.Context, _ []string) tea.Cmd {
			o := NewMessageDetailsForSelected(ctx)
			if o == nil {
				return ui.Toast(ui.ToastInfo, "No message selected")
			}
			return ui.Push(o)
		},
	})
	ui.RegisterCommand(ui.Command{
		Name: "connection",
		Help: "Show the connection status screen",
		Run: func(ctx ui.Context, _ []string) tea.Cmd {
			if ctx.Svc == nil {
				return nil
			}
			st := ctx.Svc.ConnState()
			if st == core.ConnConnected {
				return ui.Toast(ui.ToastSuccess, "Connected to Signal")
			}
			return ui.Push(NewConnection(ctx, st, nil))
		},
	})
}
