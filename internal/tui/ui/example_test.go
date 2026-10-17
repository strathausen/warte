package ui_test

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"warte/internal/core"
	"warte/internal/tui/keymap"
	"warte/internal/tui/ui"
)

// contactsOverlay is the example from docs/TUI.md: a scrollable list of
// contacts; enter opens a chat with the selected one.
type contactsOverlay struct {
	ctx      ui.Context
	contacts []core.Contact
	sel      int
}

func newContactsOverlay(ctx ui.Context) ui.Overlay {
	return &contactsOverlay{ctx: ctx}
}

type contactsLoaded struct{ list []core.Contact }

func (o *contactsOverlay) Init() tea.Cmd {
	svc := o.ctx.Svc
	return func() tea.Msg {
		list, _ := svc.Contacts(context.Background())
		return contactsLoaded{list}
	}
}

func (o *contactsOverlay) Update(msg tea.Msg) (ui.Overlay, tea.Cmd) {
	switch msg := msg.(type) {
	case contactsLoaded:
		o.contacts = msg.list
	case ui.ActionMsg:
		switch msg.Action {
		case keymap.ActCursorDown:
			o.sel = min(o.sel+msg.Count, max(len(o.contacts)-1, 0))
		case keymap.ActCursorUp:
			o.sel = max(o.sel-msg.Count, 0)
		case keymap.ActItemSelect:
			if len(o.contacts) == 0 {
				return o, nil
			}
			chat := core.DirectChatID(o.contacts[o.sel].ACI)
			// Returning nil closes the overlay.
			return nil, func() tea.Msg { return ui.OpenChatMsg{Chat: chat} }
		}
	}
	return o, nil
}

func (o *contactsOverlay) View(width, height int) string {
	st := o.ctx.Styles
	var b strings.Builder
	for i, c := range o.contacts {
		if i >= height {
			break
		}
		line := " " + c.DisplayName()
		if i == o.sel {
			line = st.ChatListItemSelected.Render(line)
		}
		b.WriteString(line + "\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func (o *contactsOverlay) Title() string                  { return "Contacts" }
func (o *contactsOverlay) KeyContext() string             { return "contacts" }
func (o *contactsOverlay) Size(maxW, maxH int) (int, int) { return min(40, maxW), min(20, maxH) }

func Example() {
	// Register a command and a key action from the view package's init.
	ui.RegisterCommand(ui.Command{
		Name: "contacts",
		Help: "Browse contacts",
		Run:  func(ctx ui.Context, _ []string) tea.Cmd { return ui.Push(newContactsOverlay(ctx)) },
	})
	ui.RegisterAction(keymap.ActChatNew, func(ctx ui.Context) tea.Cmd {
		return ui.Push(newContactsOverlay(ctx))
	})
	for _, c := range ui.Commands() {
		if c.Name == "contacts" {
			fmt.Println(c.Name, "-", c.Help)
		}
	}
	// Output: contacts - Browse contacts
}
