package media

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"warte/internal/core"
	"warte/internal/tui/keymap"
	"warte/internal/tui/ui"
)

// Actions this package handles (registered with ui.RegisterAction).
const (
	// ActViewImage is dispatched by the App on enter over an image.
	ActViewImage keymap.Action = "viewer.image"
	// ActViewOnce opens the selected view-once message (suggested hook).
	ActViewOnce keymap.Action = "viewer.viewonce"
	// ActPlayAudio plays the selected voice note / audio (suggested hook).
	ActPlayAudio keymap.Action = "player.audio"
)

func init() {
	ui.RegisterAction(ActViewImage, openSelectedViewer)
	ui.RegisterAction(ActViewOnce, openSelectedViewer)
	ui.RegisterAction(ActPlayAudio, func(ctx ui.Context) tea.Cmd { return playSelected(ctx) })
	ui.RegisterAction(keymap.ActMediaGallery, func(ctx ui.Context) tea.Cmd { return openGallery(ctx, TabMedia) })

	ui.RegisterCommand(ui.Command{
		Name:    "media",
		Aliases: []string{"gallery"},
		Args:    "[media|files|links|voice]",
		Help:    "Browse the media, files, links and voice notes of the open chat",
		Complete: func(_ ui.Context, args []string) []string {
			if len(args) > 1 {
				return nil
			}
			return []string{"media", "files", "links", "voice"}
		},
		Run: func(ctx ui.Context, args []string) tea.Cmd {
			tab := TabMedia
			if len(args) > 0 {
				t, ok := parseTab(args[0])
				if !ok {
					return toast(ui.ToastError, "Unknown tab: "+clean(args[0]))
				}
				tab = t
			}
			return openGallery(ctx, tab)
		},
	})
	ui.RegisterCommand(ui.Command{
		Name: "play",
		Help: "Play the selected voice note or audio file",
		Run:  func(ctx ui.Context, _ []string) tea.Cmd { return playSelected(ctx) },
	})
	ui.RegisterCommand(ui.Command{
		Name: "view",
		Help: "View the selected message's photo or video",
		Run:  func(ctx ui.Context, _ []string) tea.Cmd { return openSelectedViewer(ctx) },
	})
}

func parseTab(s string) (Tab, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	for t := range tabCount {
		name := strings.ToLower(t.String())
		if s != "" && strings.HasPrefix(name, s) {
			return t, true
		}
	}
	if s == "audio" {
		return TabVoice, true
	}
	return TabMedia, false
}

func selected(ctx ui.Context) *core.Message {
	if ctx.Selected == nil {
		return nil
	}
	return ctx.Selected()
}

// openSelectedViewer opens the viewer (or view-once viewer) for the
// selected message.
func openSelectedViewer(ctx ui.Context) tea.Cmd {
	m := selected(ctx)
	if m == nil {
		return toast(ui.ToastInfo, "No message selected")
	}
	if m.ViewOnce {
		return ui.Push(NewViewOnce(ctx, *m))
	}
	for _, a := range m.Attachments {
		if IsVisual(a) {
			return ui.Push(NewViewer(ctx, *m, a))
		}
	}
	return toast(ui.ToastInfo, "No photo or video in this message")
}

// playSelected opens the player for the selected message's audio.
func playSelected(ctx ui.Context) tea.Cmd {
	m := selected(ctx)
	if m == nil {
		return toast(ui.ToastInfo, "No message selected")
	}
	if m.ViewOnce {
		return toast(ui.ToastWarning, "View-once media can't be played here")
	}
	for _, a := range m.Attachments {
		if a.IsAudio() {
			return ui.Push(NewPlayerOverlay(ctx, *m, a))
		}
	}
	return toast(ui.ToastInfo, "No audio in this message")
}

// openGallery opens the gallery of the open chat.
func openGallery(ctx ui.Context, tab Tab) tea.Cmd {
	var chat core.ChatID
	if ctx.Chat != nil {
		chat = ctx.Chat()
	}
	if chat == "" {
		return toast(ui.ToastInfo, "Open a chat first")
	}
	return ui.Push(NewGalleryTab(ctx, chat, tab))
}
