package forms

import (
	"context"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"warte/internal/tui/ui"
)

// NewChatDialog asks for a phone number, username (or username link) or
// contact name, resolves it with Service.ResolveRecipient and opens the
// chat (ui.OpenChatMsg). Contact names and usernames are offered as inline
// suggestions (ctrl+e accepts). Errors keep the dialog open.
type NewChatDialog struct {
	*formOverlay
	query       string
	suggestions []string
}

// NewNewChat returns the new chat dialog, optionally pre-filled.
func NewNewChat(ctx ui.Context, initial string) *NewChatDialog {
	d := &NewChatDialog{query: initial}
	build := func() *huh.Form {
		in := huh.NewInput().
			Title("Who do you want to message?").
			Description("Phone number (+491701234567), username (name.42) or contact name").
			Placeholder("+49… · name.42 · Alice").
			Value(&d.query).
			Validate(nonEmpty("A recipient")).
			CharLimit(128)
		if len(d.suggestions) > 0 {
			in.Suggestions(d.suggestions)
		}
		return newHuhForm(ctx.Styles, huh.NewGroup(in))
	}
	d.formOverlay = newFormOverlay(ctx, "New chat", 56, build, nil)
	d.submit = func() tea.Cmd {
		q := strings.TrimSpace(d.query)
		svc := ctx.Svc
		return d.done(func() (tea.Msg, string, error) {
			c, cancel := context.WithTimeout(context.Background(), opTimeout)
			defer cancel()
			id, err := svc.ResolveRecipient(c, q)
			if err != nil {
				return nil, "", err
			}
			return ui.OpenChatMsg{Chat: id}, "", nil
		})
	}
	d.footer = []string{"enter", "open", "ctrl+e", "complete", "esc", "cancel"}
	d.initCmd = loadContacts(ctx.Svc, d)
	return d
}

// Update handles the contacts for the suggestions, then defers to the form.
func (d *NewChatDialog) Update(msg tea.Msg) (ui.Overlay, tea.Cmd) {
	if m, ok := msg.(contactsLoadedMsg); ok && m.owner == d {
		seen := map[string]bool{}
		for _, c := range m.list {
			for _, s := range []string{c.DisplayName(), c.Username} {
				s = clean(s)
				if s != "" && !seen[s] {
					seen[s] = true
					d.suggestions = append(d.suggestions, s)
				}
			}
		}
		sort.Strings(d.suggestions)
		if len(d.suggestions) > 0 && d.form.State == huh.StateNormal {
			d.rebuild()
			return d, d.form.Init()
		}
		return d, nil
	}
	o, cmd := d.formOverlay.Update(msg)
	if o == nil {
		return nil, cmd
	}
	return d, cmd
}
