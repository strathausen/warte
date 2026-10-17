package forms

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"warte/internal/core"
	"warte/internal/tui/keymap"
	"warte/internal/tui/ui"
)

func init() { Register() }

// currentChat returns the chat open in the conversation, or "".
func currentChat(ctx ui.Context) core.ChatID {
	if ctx.Chat == nil {
		return ""
	}
	return ctx.Chat()
}

// Register installs this package's key actions and ex commands in the ui
// registries. It runs from init (so a blank import is enough) and is
// idempotent.
//
// Actions: chat.new (<leader>n) → new chat dialog, group.new (<leader>g) →
// new group wizard, app.settings → settings.
//
// Commands: :group [create|settings|leave], :settings, :profile,
// :verify (alias :safety), :chatsettings (alias :chatprefs).
func Register() {
	ui.RegisterAction(keymap.ActChatNew, func(ctx ui.Context) tea.Cmd { return ui.Push(NewNewChat(ctx, "")) })
	ui.RegisterAction(keymap.ActGroupNew, func(ctx ui.Context) tea.Cmd { return ui.Push(NewNewGroup(ctx)) })
	ui.RegisterAction(keymap.ActSettings, func(ctx ui.Context) tea.Cmd { return ui.Push(NewSettings(ctx)) })

	ui.RegisterCommand(ui.Command{
		Name: "group", Args: "[create|settings|leave]", Help: "Create a group or edit the open group",
		Complete: func(_ ui.Context, args []string) []string {
			if len(args) <= 1 {
				return []string{"create", "settings", "leave"}
			}
			return nil
		},
		Run: func(ctx ui.Context, args []string) tea.Cmd {
			sub := ""
			if len(args) > 0 {
				sub = strings.ToLower(args[0])
			}
			chat := currentChat(ctx)
			switch sub {
			case "create", "new":
				return ui.Push(NewNewGroup(ctx))
			case "", "settings", "info", "edit":
				if !chat.IsGroup() {
					if sub == "" {
						return ui.Push(NewNewGroup(ctx))
					}
					return ui.Toast(ui.ToastWarning, "Open a group first")
				}
				return ui.Push(NewGroupSettings(ctx, chat))
			case "leave":
				if !chat.IsGroup() {
					return ui.Toast(ui.ToastWarning, "Open a group first")
				}
				g := NewGroupSettings(ctx, chat)
				return tea.Sequence(ui.Push(g), func() tea.Msg { return leaveRequestMsg{} })
			}
			return ui.Toast(ui.ToastError, "Usage: :group [create|settings|leave]")
		},
	})
	ui.RegisterCommand(ui.Command{
		Name: "settings", Aliases: []string{"prefs", "preferences"}, Help: "Open settings",
		Run: func(ctx ui.Context, _ []string) tea.Cmd { return ui.Push(NewSettings(ctx)) },
	})
	ui.RegisterCommand(ui.Command{
		Name: "profile", Help: "Edit your profile (name, about, avatar)",
		Run: func(ctx ui.Context, _ []string) tea.Cmd { return ui.Push(NewProfileEditor(ctx)) },
	})
	ui.RegisterCommand(ui.Command{
		Name: "verify", Aliases: []string{"safety"}, Help: "Show the safety number of the open chat",
		Run: func(ctx ui.Context, _ []string) tea.Cmd {
			chat := currentChat(ctx)
			if !chat.IsDirect() || chat.ACI() == selfACI(ctx) {
				return ui.Toast(ui.ToastWarning, "Safety numbers exist for 1:1 chats only")
			}
			return ui.Push(NewSafetyNumber(ctx, chat.ACI()))
		},
	})
	ui.RegisterCommand(ui.Command{
		Name: "chatsettings", Aliases: []string{"chatprefs"}, Help: "Settings of the open chat",
		Run: func(ctx ui.Context, _ []string) tea.Cmd {
			chat := currentChat(ctx)
			if chat == "" {
				return ui.Toast(ui.ToastWarning, "Open a chat first")
			}
			return ui.Push(NewChatSettings(ctx, chat))
		},
	})
}
