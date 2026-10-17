package forms

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"warte/internal/core"
	"warte/internal/tui/keymap"
	"warte/internal/tui/ui"
)

// maxAvatarBytes bounds avatar files read from disk.
const maxAvatarBytes = 5 << 20

// expandPath expands a leading "~/" and cleans the path.
func expandPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

// validateImagePath checks an optional avatar path ("" is fine).
func validateImagePath(p string) error {
	p = expandPath(p)
	if p == "" {
		return nil
	}
	fi, err := os.Stat(p)
	if err != nil {
		return errors.New("file not found")
	}
	if !fi.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	if fi.Size() > maxAvatarBytes {
		return errors.New("image is larger than 5 MB")
	}
	switch strings.ToLower(filepath.Ext(p)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return nil
	}
	return errors.New("use a PNG, JPEG, GIF or WebP image")
}

// readImage reads a validated avatar file (nil for "").
func readImage(p string) ([]byte, error) {
	p = expandPath(p)
	if p == "" {
		return nil, nil
	}
	if err := validateImagePath(p); err != nil {
		return nil, err
	}
	return os.ReadFile(p)
}

type wizardStep int

const (
	stepLoading wizardStep = iota
	stepDetails
	stepMembers
	stepReview
)

// NewGroupWizard creates a group in three steps: details (name, optional
// avatar path, disappearing timer; a huh form), members (fuzzy multi-select
// over the contacts) and review. Enter on the review creates the group with
// Service.CreateGroup (plus UpdateGroup for the timer) and opens it.
type NewGroupWizard struct {
	ctx    ui.Context
	step   wizardStep
	name   string
	avatar string
	timer  time.Duration
	form   *formOverlay
	picker *memberPicker
	busy   bool
	err    string

	contactsDone, settingsDone bool
}

// NewNewGroup returns the new group wizard overlay.
func NewNewGroup(ctx ui.Context) *NewGroupWizard {
	return &NewGroupWizard{ctx: ctx, picker: newMemberPicker(ctx)}
}

type wizardSettingsMsg struct {
	owner *NewGroupWizard
	s     core.Settings
	err   error
}

type groupCreatedMsg struct {
	owner *NewGroupWizard
	id    core.ChatID
	err   error
}

func (w *NewGroupWizard) Init() tea.Cmd {
	svc := w.ctx.Svc
	return tea.Batch(loadContacts(svc, w), func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
		defer cancel()
		s, err := svc.Settings(ctx)
		return wizardSettingsMsg{owner: w, s: s, err: err}
	})
}

func (w *NewGroupWizard) Title() string {
	switch w.step {
	case stepMembers:
		return "New group · members"
	case stepReview:
		return "New group · review"
	}
	return "New group"
}

func (w *NewGroupWizard) KeyContext() string { return ContextNewGroup }
func (w *NewGroupWizard) CaptureKeys() bool  { return true }

func (w *NewGroupWizard) Footer() string {
	st := w.ctx.Styles
	switch w.step {
	case stepMembers:
		return st.RenderKeyHints("tab", "toggle", "enter", "next", "esc", "back")
	case stepReview:
		return st.RenderKeyHints("enter", "create", "esc", "back")
	case stepDetails:
		return st.RenderKeyHints("tab", "next", "S-tab", "back", "enter", "ok", "esc", "cancel")
	}
	return st.RenderKeyHints("esc", "cancel")
}

func (w *NewGroupWizard) Size(maxW, maxH int) (int, int) {
	width := min(60, maxW)
	extra := 0
	if w.err != "" || w.busy {
		extra = 1
	}
	switch w.step {
	case stepDetails:
		return w.form.Size(maxW, maxH)
	case stepMembers:
		return width, min(max(len(w.picker.contacts)+2, 8)+extra, 20, maxH)
	case stepReview:
		return width, min(len(w.review(width))+extra, maxH)
	}
	return width, min(3, maxH)
}

func (w *NewGroupWizard) buildDetails() {
	ctx := w.ctx
	build := func() *huh.Form {
		var timers []huh.Option[time.Duration]
		for _, d := range timerOptions(w.timer) {
			timers = append(timers, huh.NewOption(timerLabel(d), d))
		}
		return newHuhForm(ctx.Styles, huh.NewGroup(
			huh.NewInput().Title("Group name").Placeholder("Book club").
				Value(&w.name).CharLimit(32).Validate(nonEmpty("A group name")),
			huh.NewInput().Title("Avatar").Description("Optional: path to a PNG or JPEG image").
				Placeholder("~/Pictures/group.png").Value(&w.avatar).Validate(validateImagePath),
			huh.NewSelect[time.Duration]().Title("Disappearing messages").
				Options(timers...).Value(&w.timer).Height(5),
		))
	}
	w.form = newFormOverlay(ctx, "New group", 60, build, nil)
}

func (w *NewGroupWizard) ready() bool { return w.contactsDone && w.settingsDone }

func (w *NewGroupWizard) Update(msg tea.Msg) (ui.Overlay, tea.Cmd) {
	switch msg := msg.(type) {
	case contactsLoadedMsg:
		if msg.owner != w {
			return w, nil
		}
		w.contactsDone = true
		if msg.err != nil {
			w.err = msg.err.Error()
		}
		w.picker.setContacts(msg.list, nil)
		return w, w.maybeStart()
	case wizardSettingsMsg:
		if msg.owner != w {
			return w, nil
		}
		w.settingsDone = true
		if msg.err == nil {
			w.timer = msg.s.DefaultExpireTimer
		}
		return w, w.maybeStart()
	case groupCreatedMsg:
		if msg.owner != w {
			return w, nil
		}
		w.busy = false
		id := msg.id
		if msg.err != nil && id == "" {
			w.err = msg.err.Error()
			return w, nil
		}
		toast := ui.Toast(ui.ToastSuccess, "Created "+clean(strings.TrimSpace(w.name)))
		if msg.err != nil {
			// The group exists: close and open it rather than offering a
			// retry that would create a second group.
			toast = ui.Toast(ui.ToastWarning, clean(msg.err.Error()))
		}
		return nil, tea.Batch(
			func() tea.Msg { return ui.OpenChatMsg{Chat: id} },
			toast)
	}
	if w.busy {
		return w, nil
	}
	switch w.step {
	case stepLoading:
		if km, ok := msg.(tea.KeyPressMsg); ok {
			if k := keymap.FromTea(km); k == "esc" || k == "ctrl+c" {
				return nil, nil
			}
		}
	case stepDetails:
		o, cmd := w.form.Update(msg)
		if o == nil {
			if w.form.form.State == huh.StateCompleted {
				w.step = stepMembers
				w.err = ""
				return w, cmd
			}
			return nil, cmd // cancelled
		}
		return w, cmd
	case stepMembers:
		switch w.picker.update(msg) {
		case pickerCancel:
			w.step = stepDetails
			w.form.rebuild()
			return w, w.form.form.Init()
		case pickerDone:
			w.step = stepReview
		}
	case stepReview:
		km, ok := msg.(tea.KeyPressMsg)
		if !ok {
			return w, nil
		}
		switch keymap.FromTea(km) {
		case "esc", "shift+tab", "backspace":
			w.step = stepMembers
			w.err = ""
		case "ctrl+c":
			return nil, nil
		case "enter":
			w.busy = true
			w.err = ""
			return w, w.create()
		}
	}
	return w, nil
}

func (w *NewGroupWizard) maybeStart() tea.Cmd {
	if !w.ready() || w.step != stepLoading {
		return nil
	}
	w.buildDetails()
	w.step = stepDetails
	return w.form.Init()
}

func (w *NewGroupWizard) create() tea.Cmd {
	svc := w.ctx.Svc
	name := strings.TrimSpace(w.name)
	members := w.picker.selection()
	avatar := w.avatar
	timer := w.timer
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
		defer cancel()
		img, err := readImage(avatar)
		if err != nil {
			return groupCreatedMsg{owner: w, err: fmt.Errorf("avatar: %w", err)}
		}
		id, err := svc.CreateGroup(ctx, name, members, img)
		if err != nil {
			return groupCreatedMsg{owner: w, err: err}
		}
		if timer > 0 {
			if err := svc.UpdateGroup(ctx, id, core.GroupUpdate{ExpireTimer: &timer}); err != nil {
				return groupCreatedMsg{owner: w, id: id, err: fmt.Errorf("group created, but setting the timer failed: %w", err)}
			}
		}
		return groupCreatedMsg{owner: w, id: id}
	}
}

func (w *NewGroupWizard) View(width, h int) string {
	st := w.ctx.Styles
	var lines []string
	switch w.step {
	case stepLoading:
		lines = []string{muted(st, "Loading contacts…")}
	case stepDetails:
		return w.form.View(width, h)
	case stepMembers:
		lines = w.picker.view(width, h-1)
	case stepReview:
		lines = w.review(width)
	}
	switch {
	case w.busy:
		lines = append(lines, st.InfoText.Render("… creating group"))
	case w.err != "":
		lines = append(lines, statusLine(st, w.err, true, width))
	}
	if len(lines) > h {
		lines = append(lines[:max(h-1, 0)], lines[len(lines)-1])
	}
	return clampLines(lines, width, h)
}

func (w *NewGroupWizard) review(width int) []string {
	st := w.ctx.Styles
	name := clean(strings.TrimSpace(w.name))
	av := st.RenderAvatar("", name, name)
	lines := []string{av + " " + st.Title.Render(trunc(name, max(width-6, 1))), ""}
	field := func(label, value string) {
		lines = append(lines, twoCol(muted(st, label), value, width))
	}
	names := w.picker.names()
	field("Members", itoa(len(names)+1)+" including you")
	field("Disappearing messages", timerLabel(w.timer))
	avatar := "None"
	if p := strings.TrimSpace(w.avatar); p != "" {
		avatar = filepath.Base(clean(p))
	}
	field("Avatar", trunc(avatar, max(width/2, 1)))
	lines = append(lines, "")
	if len(names) == 0 {
		lines = append(lines, subtle(st, "Only you — add people later in the group settings."))
	} else {
		for _, l := range wrapText(strings.Join(names, ", "), width) {
			lines = append(lines, l)
		}
	}
	lines = append(lines, "", accent(st, "Press enter to create the group."))
	return lines
}
