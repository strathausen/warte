package finder

import (
	"context"
	"image/color"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"warte/internal/core"
	"warte/internal/tui/theme"
	"warte/internal/tui/ui"
)

// Keymap overlay context names used by the finder views (see
// keymap.Overlay). The views capture keys while open, so these names mainly
// label the contexts in help and allow the host to bind extra keys.
const (
	ContextChats   = "finder.chats"
	ContextSearch  = "finder.search"
	ContextPicker  = "finder.picker"
	ContextForward = "finder.forward"
	ContextTheme   = "finder.theme"
)

// forwardTimeout bounds svc.Forward.
const forwardTimeout = 30 * time.Second

// ---- forward picker ---------------------------------------------------------------------

// NewForwardPicker returns a multi-select chat picker that forwards the
// message(s) to every marked chat with svc.Forward and reports the outcome
// with a toast.
func NewForwardPicker(ctx ui.Context, id core.MessageID, more ...core.MessageID) *Picker[core.Chat] {
	ids := append([]core.MessageID{id}, more...)
	svc := ctx.Svc
	label := "Forward to…"
	if len(ids) > 1 {
		label = "Forward " + itoa(len(ids)) + " messages to…"
	}
	return NewPicker(ctx, PickerOptions[core.Chat]{
		Title:       label,
		KeyContext:  ContextForward,
		Placeholder: "Find chats",
		Multi:       true,
		Filter:      func(c core.Chat) string { return chatTitle(c) },
		Key:         func(c core.Chat) string { return string(c.ID) },
		Load: func() ([]core.Chat, error) {
			if svc == nil {
				return nil, nil
			}
			cctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
			defer cancel()
			chats, err := svc.Chats(cctx, core.ChatFilter{IncludeEmpty: true})
			if err != nil {
				return nil, err
			}
			out := chats[:0:0]
			for _, c := range chats {
				if c.Blocked || c.MessageRequest || (c.Kind == core.ChatGroup && c.OnlyAdminsSend && !c.SelfIsAdmin) {
					continue
				}
				out = append(out, c)
			}
			return out, nil
		},
		Render: func(c core.Chat, r Row) string { return renderChatRow(c, r) },
		OnSelect: func(chats []core.Chat) tea.Cmd {
			if svc == nil || len(chats) == 0 {
				return nil
			}
			to := make([]core.ChatID, len(chats))
			names := make([]string, len(chats))
			for i, c := range chats {
				to[i] = c.ID
				names[i] = chatTitle(c)
			}
			return func() tea.Msg {
				cctx, cancel := context.WithTimeout(context.Background(), forwardTimeout)
				defer cancel()
				for _, id := range ids {
					if err := svc.Forward(cctx, id, to); err != nil {
						return ui.ToastMsg{Level: ui.ToastError, Text: "Forward failed: " + theme.SanitizeLine(err.Error())}
					}
				}
				return ui.ToastMsg{Level: ui.ToastSuccess, Text: "Forwarded to " + joinNames(names, 3)}
			}
		},
		Hints: nil,
		Empty: "No chats match",
	})
}

func chatTitle(c core.Chat) string {
	t := theme.SanitizeLine(c.Title)
	if t == "" && c.Kind == core.ChatNoteToSelf {
		t = "Note to Self"
	}
	if t == "" {
		t = "Unknown"
	}
	return t
}

func joinNames(names []string, n int) string {
	if len(names) <= n {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:n], ", ") + " +" + itoa(len(names)-n)
}

// renderChatRow is the Picker row for a chat: initials avatar, highlighted
// title and a group/self marker.
func renderChatRow(c core.Chat, r Row) string {
	st := r.Styles
	av := st.RenderAvatar(c.Color, string(c.ID), chatTitle(c))
	if c.Kind == core.ChatNoteToSelf && r.Icons.NoteToSelf != "" {
		av = st.Avatar(c.Color, string(c.ID)).Render(padCenter(r.Icons.NoteToSelf, 4))
	}
	right := ""
	if c.Kind == core.ChatGroup {
		right = r.Muted().Render(itoa(len(c.Members))+" members") + r.Base().Render(" ")
	}
	return hardClip(av, 4) + r.Base().Render(" ") + spread(r.Highlight(chatTitle(c)), right, max(r.Width-5, 0), r.Base())
}

// NewChatPicker returns a generic chat picker (single or multi-select)
// that calls onSelect with the chosen chats. It is the building block for
// "add to group", "share contact" and similar flows.
func NewChatPicker(ctx ui.Context, title string, multi bool, onSelect func([]core.Chat) tea.Cmd) *Picker[core.Chat] {
	svc := ctx.Svc
	return NewPicker(ctx, PickerOptions[core.Chat]{
		Title:  title,
		Multi:  multi,
		Filter: func(c core.Chat) string { return chatTitle(c) },
		Key:    func(c core.Chat) string { return string(c.ID) },
		Load: func() ([]core.Chat, error) {
			if svc == nil {
				return nil, nil
			}
			cctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
			defer cancel()
			return svc.Chats(cctx, core.ChatFilter{IncludeEmpty: true})
		},
		Render:   func(c core.Chat, r Row) string { return renderChatRow(c, r) },
		OnSelect: onSelect,
	})
}

// ---- theme picker ------------------------------------------------------------------------

// SetThemeMsg asks the App to switch to the named theme. The current shell
// does not handle it (ui has no such message), so the theme picker applies
// themes with ui.RunCommandMsg{Line: "theme <name>"} instead; the type is
// exported for an integrator that prefers a dedicated message.
type SetThemeMsg struct{ Name string }

// NewThemePicker returns a picker over theme.Names() with a preview pane
// (theme.Preview) of the highlighted theme. Moving the cursor also previews
// the theme on the whole screen (ui.PreviewThemeMsg; the App restores the
// configured theme on cancel); enter applies it through the ":theme"
// command.
func NewThemePicker(ctx ui.Context) *Picker[string] {
	names := theme.Names()
	cur := ctx.Config.Theme
	if cur == "" {
		cur = ctx.Styles.Theme.Name
	}
	initial := 0
	for i, n := range names {
		if strings.EqualFold(n, cur) {
			initial = i
		}
	}
	if initial == 0 {
		for i, n := range names {
			if strings.EqualFold(n, ctx.Styles.Theme.Name) {
				initial = i
			}
		}
	}
	resolve := func(name string) theme.Theme {
		if name == theme.AutoName {
			return theme.Auto(ctx.Styles.Theme.Dark)
		}
		t, ok := theme.Get(name)
		if !ok {
			return ctx.Styles.Theme
		}
		return t
	}
	return NewPicker(ctx, PickerOptions[string]{
		Title:        "Theme",
		KeyContext:   ContextTheme,
		Placeholder:  "Filter themes",
		Items:        names,
		Initial:      initial,
		Filter:       func(s string) string { return s },
		PreviewWidth: 38,
		Width:        0,
		Height:       min(len(names)+3, 20),
		Render: func(name string, r Row) string {
			t := resolve(name)
			sw := ""
			if !t.Mono {
				p := t.Palette
				for _, c := range []color.Color{p.Accent, p.AccentAlt, p.BubbleOut, p.Success, p.Warning} {
					sw += r.On(lipgloss.NewStyle().Foreground(c)).Render("●")
				}
			} else {
				sw = r.Muted().Render("mono")
			}
			mark := ""
			if strings.EqualFold(name, cur) {
				mark = r.On(lipgloss.NewStyle().Foreground(r.Styles.Theme.Palette.Success)).Render(r.Icons.Check) + r.Base().Render(" ")
			}
			return r.Spread(r.Highlight(name), mark+sw+r.Base().Render(" "))
		},
		Preview: func(name string, w, h int) string {
			return theme.Preview(resolve(name))
		},
		OnSelect: func(sel []string) tea.Cmd {
			name := sel[0]
			return func() tea.Msg { return ui.RunCommandMsg{Line: "theme " + name} }
		},
		OnHighlight: func(name string) tea.Cmd {
			return func() tea.Msg { return ui.PreviewThemeMsg{Name: name} }
		},
		OnCancel: func() tea.Cmd {
			return func() tea.Msg { return ui.PreviewThemeMsg{} }
		},
		Hints: []string{"ctrl+o", "preview"},
	})
}
