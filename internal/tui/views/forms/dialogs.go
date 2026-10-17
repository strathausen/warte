package forms

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"warte/internal/core"
	"warte/internal/tui/theme"
	"warte/internal/tui/ui"
)

// applyFunc performs a backend change for a dialog.
type applyFunc[T any] func(ctx context.Context, svc core.Service, v T) error

// textDialog edits one string (an Input, or a multi-line Text when lines >
// 1). On success the dialog closes and an opDoneMsg (op, ok) reaches the
// overlay below so it can refresh.
func textDialog(ctx ui.Context, title, label, desc, initial string, lines, charLimit int,
	validate func(string) error, apply applyFunc[string], op, okToast string) ui.Overlay {
	// The initial value may come from other people (a group title): strip
	// escape sequences and control characters before huh renders it.
	value := clean(initial)
	if lines > 1 {
		value = strings.ReplaceAll(theme.Sanitize(initial), "\t", " ")
	}
	if validate == nil {
		validate = func(string) error { return nil }
	}
	if charLimit > 0 {
		inner := validate
		validate = func(s string) error {
			if utf8.RuneCountInString(s) > charLimit {
				return errors.New("too long")
			}
			return inner(s)
		}
	}
	build := func() *huh.Form {
		var field huh.Field
		if lines > 1 {
			t := huh.NewText().Title(label).Value(&value).Lines(lines).
				ExternalEditor(false).Validate(validate)
			if charLimit > 0 {
				t.CharLimit(charLimit)
			}
			if desc != "" {
				t.Description(desc)
			}
			field = t
		} else {
			in := huh.NewInput().Title(label).Value(&value).Validate(validate)
			if charLimit > 0 {
				in.CharLimit(charLimit)
			}
			if desc != "" {
				in.Description(desc)
			}
			field = in
		}
		return newHuhForm(ctx.Styles, huh.NewGroup(field))
	}
	var o *formOverlay
	o = newFormOverlay(ctx, title, 56, build, func() tea.Cmd {
		v := value
		svc := ctx.Svc
		return o.done(func() (tea.Msg, string, error) {
			c, cancel := context.WithTimeout(context.Background(), opTimeout)
			defer cancel()
			if err := apply(c, svc, v); err != nil {
				return nil, "", err
			}
			return opDoneMsg{op: op, ok: okToast}, "", nil
		})
	})
	if lines > 1 {
		o.footer = []string{"tab", "save", "alt+enter", "newline", "esc", "cancel"}
	} else {
		o.footer = []string{"enter", "save", "esc", "cancel"}
	}
	return o
}

// choiceDialog picks one of opts (huh Select) and applies it.
func choiceDialog[T comparable](ctx ui.Context, title, label string, opts []huh.Option[T], cur T,
	apply applyFunc[T], op, okToast string) ui.Overlay {
	value := cur
	build := func() *huh.Form {
		sel := huh.NewSelect[T]().Title(label).Options(opts...).Value(&value)
		if len(opts) > 8 {
			sel.Height(10)
		}
		return newHuhForm(ctx.Styles, huh.NewGroup(sel))
	}
	var o *formOverlay
	o = newFormOverlay(ctx, title, 40, build, func() tea.Cmd {
		v := value
		svc := ctx.Svc
		return o.done(func() (tea.Msg, string, error) {
			c, cancel := context.WithTimeout(context.Background(), opTimeout)
			defer cancel()
			if err := apply(c, svc, v); err != nil {
				return nil, "", err
			}
			return opDoneMsg{op: op, ok: okToast}, "", nil
		})
	})
	o.footer = []string{"j/k", "move", "enter", "choose", "esc", "cancel"}
	return o
}

// timerDialog chooses a disappearing-message timer.
func timerDialog(ctx ui.Context, cur time.Duration, apply applyFunc[time.Duration]) ui.Overlay {
	var opts []huh.Option[time.Duration]
	for _, d := range timerOptions(cur) {
		opts = append(opts, huh.NewOption(timerLabel(d), d))
	}
	return choiceDialog(ctx, "Disappearing messages", "Delete new messages after", opts, cur, apply,
		"Disappearing messages", "Disappearing messages updated")
}

// muteDialog chooses a mute duration for a chat.
func muteDialog(ctx ui.Context, chat core.ChatID) ui.Overlay {
	var opts []huh.Option[int]
	for i, m := range MuteOptions {
		opts = append(opts, huh.NewOption(m.Label, i))
	}
	now := nowFn(ctx)
	return choiceDialog(ctx, "Mute notifications", "Mute for", opts, 0,
		func(c context.Context, svc core.Service, i int) error {
			until := MuteUntil(MuteOptions[i], now())
			return svc.UpdateChat(c, chat, core.ChatUpdate{MutedUntil: &until})
		}, "Mute", "Notifications updated")
}

// nonEmpty validates a required field.
func nonEmpty(what string) func(string) error {
	return func(s string) error {
		if strings.TrimSpace(s) == "" {
			return errors.New(what + " is required")
		}
		return nil
	}
}
