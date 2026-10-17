package emoji

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"warte/internal/core"
	"warte/internal/tui/ui"
)

func init() {
	ui.RegisterAction(ui.ActReactionPicker, OpenReactionPicker)
	ui.RegisterCommand(ui.Command{
		Name: "emoji", Args: "[query]", Help: "Insert an emoji into the message",
		Run: func(ctx ui.Context, args []string) tea.Cmd {
			return ui.Push(NewPicker(ctx, Options{Purpose: PurposeInsert, Query: strings.Join(args, " ")}))
		},
	})
	ui.RegisterCommand(ui.Command{
		Name: "stickers", Aliases: []string{"sticker"}, Help: "Send a sticker",
		Run: func(ctx ui.Context, _ []string) tea.Cmd { return OpenStickers(ctx) },
	})
	ui.RegisterCommand(ui.Command{
		Name: "reactions", Help: "Show who reacted to the selected message",
		Run: func(ctx ui.Context, _ []string) tea.Cmd { return OpenReactions(ctx) },
	})
}

func selected(ctx ui.Context) *core.Message {
	if ctx.Selected == nil {
		return nil
	}
	return ctx.Selected()
}

// OpenReactionPicker pushes the emoji picker reacting to the selected
// message (the handler registered for ui.ActReactionPicker).
func OpenReactionPicker(ctx ui.Context) tea.Cmd {
	m := selected(ctx)
	if m == nil {
		return ui.Toast(ui.ToastWarning, "No message selected")
	}
	if m.Kind != core.KindNormal || m.RemoteDeleted {
		return ui.Toast(ui.ToastWarning, "Can't react to this message")
	}
	return ui.Push(NewReactionPicker(ctx, m.ID))
}

// OpenInsertPicker pushes the emoji picker inserting into the composer.
func OpenInsertPicker(ctx ui.Context) tea.Cmd { return ui.Push(NewInsertPicker(ctx)) }

// OpenReactions pushes the reaction detail overlay for the selected message.
func OpenReactions(ctx ui.Context) tea.Cmd {
	m := selected(ctx)
	if m == nil {
		return ui.Toast(ui.ToastWarning, "No message selected")
	}
	return ui.Push(NewReactionsFor(ctx, *m))
}

// OpenStickers pushes the sticker picker for the open chat.
func OpenStickers(ctx ui.Context) tea.Cmd {
	if ctx.Chat == nil || ctx.Chat() == "" {
		return ui.Toast(ui.ToastWarning, "Open a chat to send stickers")
	}
	return ui.Push(NewStickers(ctx, StickerOptions{}))
}
