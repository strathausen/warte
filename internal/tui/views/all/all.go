// Package all links every feature view into the warte TUI. Importing it
// (cmd/warte does, blank) runs the views' init registrations and then wires
// the hooks the shell exposes in package ui:
//
//	sidebar pane            info.Sidebar (<leader>i, focus with ctrl+w l / L)
//	pinned strip            info.RenderPinnedStrip above the conversation
//	forward (f, visual f)   finder.NewForwardPicker (multi-chat)
//	K                       info.NewMessageDetails
//	gr, :reactions          emoji reaction details
//	gp, :pinned             info pinned messages
//	gi, :settings chat      forms chat / group settings
//	ctrl+g / ctrl+s (insert) emoji / sticker picker
//	"…" in the reaction bar emoji picker (answers with ui.ReactMsg so "." repeats it)
//	logged out              info connection screen (enter relinks)
//	chat opened             finder.NoteRecent (switcher recents)
//	shutdown                media.StopAll (stop voice notes)
//
// The integration tests in this package open every overlay through its key
// from the main view.
package all

import (
	tea "charm.land/bubbletea/v2"

	"warte/internal/core"
	"warte/internal/tui/keymap"
	"warte/internal/tui/ui"
	"warte/internal/tui/views/emoji"
	"warte/internal/tui/views/finder"
	"warte/internal/tui/views/forms"
	"warte/internal/tui/views/info"
	"warte/internal/tui/views/media"

	// Registered through their init functions only.
	_ "warte/internal/tui/views/stories"
)

func init() {
	ui.RegisterSidebar(func(ctx ui.Context) ui.Pane { return &sidebarPane{s: info.NewSidebar(ctx)} })
	ui.RegisterPinnedStrip(func(ctx ui.Context, chat core.Chat, pinned []core.Message, current, width int) string {
		return info.RenderPinnedStrip(ctx, &chat, pinned, current, width)
	})
	ui.RegisterForwarder(func(ctx ui.Context, ids []core.MessageID) tea.Cmd {
		if len(ids) == 0 {
			return nil
		}
		return ui.Push(finder.NewForwardPicker(ctx, ids[0], ids[1:]...))
	})
	ui.OnChatOpened(finder.NoteRecent)
	ui.OnShutdown(media.StopAll)

	ui.RegisterAction(keymap.ActMessageInfo, func(ctx ui.Context) tea.Cmd {
		o := info.NewMessageDetailsForSelected(ctx)
		if o == nil {
			return ui.Toast(ui.ToastInfo, "No message selected")
		}
		return ui.Push(o)
	})
	ui.RegisterAction(keymap.ActMessageReactions, func(ctx ui.Context) tea.Cmd {
		if m := selected(ctx); m != nil && len(m.Reactions) == 0 {
			return ui.Toast(ui.ToastInfo, "No reactions yet")
		}
		return emoji.OpenReactions(ctx)
	})
	ui.RegisterAction(keymap.ActChatPinned, func(ctx ui.Context) tea.Cmd {
		chat := currentChat(ctx)
		if chat == "" {
			return ui.Toast(ui.ToastWarning, "Open a chat first")
		}
		return ui.Push(info.NewPinnedMessages(ctx, chat, nil))
	})
	ui.RegisterAction(keymap.ActChatInfo, chatSettings)
	ui.RegisterAction(keymap.ActComposerEmoji, emoji.OpenInsertPicker)
	ui.RegisterAction(keymap.ActComposerSticker, emoji.OpenStickers)
	ui.RegisterAction(ui.ActReactionPicker, func(ctx ui.Context) tea.Cmd {
		m := selected(ctx)
		if m == nil {
			return ui.Toast(ui.ToastWarning, "No message selected")
		}
		if m.Kind != core.KindNormal || m.RemoteDeleted {
			return ui.Toast(ui.ToastWarning, "Can't react to this message")
		}
		return ui.Push(emoji.NewPicker(ctx, emoji.Options{
			Purpose: emoji.PurposeReact,
			Target:  m.ID,
			// Through the App, so "." repeats the reaction.
			OnPick: func(p emoji.PickedMsg) tea.Cmd {
				return func() tea.Msg { return ui.ReactMsg{ID: p.Target, Emoji: p.Emoji, Toggle: true} }
			},
		}))
	})
	ui.RegisterAction(ui.ActLoggedOut, func(ctx ui.Context) tea.Cmd {
		return ui.Push(info.NewConnection(ctx, core.ConnLoggedOut, nil))
	})

	// The sidebar's actions run these command lines.
	info.SafetyNumberCommand = "verify"
	info.GroupSettingsCommand = "group settings"

	// ":settings" also opens the open chat's settings (":settings chat").
	ui.RegisterCommand(ui.Command{
		Name: "settings", Aliases: []string{"prefs", "preferences"}, Args: "[chat]",
		Help: "Settings (ctrl+s or :w saves them to config.toml); :settings chat for the open chat",
		Complete: func(_ ui.Context, args []string) []string {
			if len(args) <= 1 {
				return []string{"chat"}
			}
			return nil
		},
		Run: func(ctx ui.Context, args []string) tea.Cmd {
			if len(args) > 0 && (args[0] == "chat" || args[0] == "group" || args[0] == "contact") {
				return chatSettings(ctx)
			}
			return ui.Push(forms.NewSettings(ctx))
		},
	})
}

func selected(ctx ui.Context) *core.Message {
	if ctx.Selected == nil {
		return nil
	}
	return ctx.Selected()
}

func currentChat(ctx ui.Context) core.ChatID {
	if ctx.Chat == nil {
		return ""
	}
	return ctx.Chat()
}

// chatSettings opens the group or contact settings of the open chat.
func chatSettings(ctx ui.Context) tea.Cmd {
	chat := currentChat(ctx)
	if chat == "" {
		return ui.Toast(ui.ToastWarning, "Open a chat first")
	}
	return ui.Push(forms.NewChatSettings(ctx, chat))
}

// sidebarPane adapts info.Sidebar to ui.Pane.
type sidebarPane struct{ s *info.Sidebar }

func (p *sidebarPane) SetChat(id core.ChatID) tea.Cmd { return p.s.SetChat(id) }
func (p *sidebarPane) Focus()                         { p.s.Focus() }
func (p *sidebarPane) Blur()                          { p.s.Blur() }
func (p *sidebarPane) View(w, h int) string           { return p.s.View(w, h) }
func (p *sidebarPane) Update(msg tea.Msg) tea.Cmd {
	s, cmd := p.s.Update(msg)
	if s != nil {
		p.s = s
	}
	return cmd
}
