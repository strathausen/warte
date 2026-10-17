package forms

import (
	"reflect"
	"runtime"
	"strings"

	"charm.land/bubbles/v2/help"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"

	"warte/internal/tui/keymap"
	"warte/internal/tui/theme"
	"warte/internal/tui/ui"
)

// HuhTheme returns a huh theme built from the live warte styles, so forms
// follow the palette (including live :theme switches: the styles are read
// each time huh asks for them) and the colorless "mono" theme.
func HuhTheme(st *theme.Styles) huh.Theme {
	return huh.ThemeFunc(func(bool) *huh.Styles { return huhStyles(st) })
}

func huhStyles(st *theme.Styles) *huh.Styles {
	t := huh.ThemeBase(st.Theme.Dark)
	p := st.Theme.Palette
	ns := lipgloss.NewStyle
	if st.Theme.Mono {
		t.Focused.Base = t.Focused.Base.BorderForeground(lipgloss.NoColor{})
		t.Focused.Title = ns().Bold(true)
		t.Focused.NoteTitle = ns().Bold(true).MarginBottom(1)
		t.Focused.Description = ns().Faint(true)
		t.Focused.ErrorIndicator = ns().Bold(true).SetString(" *")
		t.Focused.ErrorMessage = ns().Bold(true).SetString(" *")
		t.Focused.SelectSelector = ns().Bold(true).SetString("▸ ")
		t.Focused.MultiSelectSelector = ns().Bold(true).SetString("▸ ")
		t.Focused.Option = ns()
		t.Focused.SelectedOption = ns().Bold(true)
		t.Focused.SelectedPrefix = ns().Bold(true).SetString("[x] ")
		t.Focused.UnselectedPrefix = ns().SetString("[ ] ")
		t.Focused.UnselectedOption = ns()
		t.Focused.FocusedButton = ns().Reverse(true).Bold(true).Padding(0, 2).MarginRight(1)
		t.Focused.BlurredButton = ns().Faint(true).Padding(0, 2).MarginRight(1)
		t.Focused.Next = t.Focused.FocusedButton
		t.Focused.TextInput.Cursor = ns().Reverse(true)
		t.Focused.TextInput.Placeholder = ns().Faint(true)
		t.Focused.TextInput.Prompt = ns().Bold(true)
		t.Focused.TextInput.Text = ns()
	} else {
		t.Focused.Base = t.Focused.Base.BorderForeground(p.BorderFocus)
		t.Focused.Card = t.Focused.Base
		t.Focused.Title = ns().Foreground(p.Accent).Bold(true)
		t.Focused.NoteTitle = ns().Foreground(p.Accent).Bold(true).MarginBottom(1)
		t.Focused.Directory = ns().Foreground(p.Accent)
		t.Focused.File = ns().Foreground(p.Fg)
		t.Focused.Description = ns().Foreground(p.FgMuted)
		t.Focused.ErrorIndicator = ns().Foreground(p.Error).SetString(" *")
		t.Focused.ErrorMessage = ns().Foreground(p.Error).SetString(" *")
		t.Focused.SelectSelector = ns().Foreground(p.AccentAlt).SetString("▸ ")
		t.Focused.NextIndicator = ns().Foreground(p.AccentAlt).MarginLeft(1).SetString("→")
		t.Focused.PrevIndicator = ns().Foreground(p.AccentAlt).MarginRight(1).SetString("←")
		t.Focused.Option = ns().Foreground(p.Fg)
		t.Focused.MultiSelectSelector = ns().Foreground(p.AccentAlt).SetString("▸ ")
		t.Focused.SelectedOption = ns().Foreground(p.Success)
		t.Focused.SelectedPrefix = ns().Foreground(p.Success).SetString("✓ ")
		t.Focused.UnselectedPrefix = ns().Foreground(p.FgSubtle).SetString("• ")
		t.Focused.UnselectedOption = ns().Foreground(p.Fg)
		t.Focused.FocusedButton = ns().Foreground(p.Bg).Background(p.Accent).Bold(true).Padding(0, 2).MarginRight(1)
		t.Focused.BlurredButton = ns().Foreground(p.FgMuted).Background(p.BgAlt).Padding(0, 2).MarginRight(1)
		t.Focused.Next = t.Focused.FocusedButton
		t.Focused.TextInput.Cursor = ns().Foreground(p.Accent)
		t.Focused.TextInput.CursorText = ns().Foreground(p.Bg).Background(p.Accent)
		t.Focused.TextInput.Placeholder = ns().Foreground(p.FgSubtle)
		t.Focused.TextInput.Prompt = ns().Foreground(p.AccentAlt)
		t.Focused.TextInput.Text = ns().Foreground(p.Fg)
	}
	t.Focused.Card = t.Focused.Base

	t.Blurred = t.Focused
	t.Blurred.Base = t.Focused.Base.BorderStyle(lipgloss.HiddenBorder())
	t.Blurred.Card = t.Blurred.Base
	t.Blurred.NextIndicator = ns()
	t.Blurred.PrevIndicator = ns()
	t.Blurred.SelectSelector = ns().SetString("  ")
	t.Blurred.MultiSelectSelector = ns().SetString("  ")
	if st.Theme.Mono {
		t.Blurred.Title = ns()
	} else {
		t.Blurred.Title = ns().Foreground(p.FgMuted).Bold(true)
		t.Blurred.TextInput.Text = ns().Foreground(p.FgMuted)
	}

	t.Group.Title = t.Focused.Title
	t.Group.Description = t.Focused.Description
	t.Help = help.Styles{
		Ellipsis:       st.KeyHintSep,
		ShortKey:       st.KeyHintKey,
		ShortDesc:      st.KeyHintDesc,
		ShortSeparator: st.KeyHintSep,
		FullKey:        st.KeyHintKey,
		FullDesc:       st.KeyHintDesc,
		FullSeparator:  st.KeyHintSep,
	}
	return t
}

// newHuhForm creates a form with warte's theme and defaults (no built-in
// help: the dialog footer shows the key hints; no ctrl+c quit: the overlay
// handles esc / ctrl+c itself).
func newHuhForm(st *theme.Styles, groups ...*huh.Group) *huh.Form {
	km := huh.NewDefaultKeyMap()
	km.Quit.SetEnabled(false)
	f := huh.NewForm(groups...).
		WithTheme(HuhTheme(st)).
		WithKeyMap(km).
		WithShowHelp(false).
		WithShowErrors(true)
	f.SubmitCmd = nil
	f.CancelCmd = nil
	return f
}

// formDoneMsg reports the result of a form's submit action.
type formDoneMsg struct {
	owner *formOverlay
	err   error
	// follow is delivered after the dialog closed (e.g. ui.OpenChatMsg).
	follow tea.Msg
	toast  string
}

// formOverlay hosts a huh form as a ui.Overlay. It captures all keys while
// open (huh needs raw keys); esc and ctrl+c cancel. When the form completes
// submit runs; its command must return a formDoneMsg for this overlay (use
// done). On error the form is rebuilt (values live in the caller's
// variables, bound by pointer) and the error is shown.
type formOverlay struct {
	ctx      ui.Context
	title    string
	build    func() *huh.Form
	form     *huh.Form
	submit   func() tea.Cmd // nil = just close
	onChange func() tea.Cmd // called after every update (live forms)
	prefW    int
	prefH    int // 0 = derived from the form
	footer   []string
	busy     bool
	err      string
	w, h     int
	initCmd  tea.Cmd
}

// newFormOverlay builds the overlay; build is called now and again after a
// failed submit.
func newFormOverlay(ctx ui.Context, title string, prefW int, build func() *huh.Form, submit func() tea.Cmd) *formOverlay {
	o := &formOverlay{ctx: ctx, title: title, build: build, submit: submit, prefW: prefW}
	o.form = build()
	return o
}

// done wraps a submit action: fn runs in a command and its error decides
// whether the dialog closes (follow is then delivered) or stays open.
func (o *formOverlay) done(fn func() (follow tea.Msg, toast string, err error)) tea.Cmd {
	return func() tea.Msg {
		follow, toast, err := fn()
		return formDoneMsg{owner: o, err: err, follow: follow, toast: toast}
	}
}

func (o *formOverlay) Init() tea.Cmd      { return tea.Batch(o.form.Init(), o.initCmd) }
func (o *formOverlay) Title() string      { return o.title }
func (o *formOverlay) KeyContext() string { return ContextForm }
func (o *formOverlay) CaptureKeys() bool  { return true }
func (o *formOverlay) Footer() string {
	if len(o.footer) > 0 {
		return o.ctx.Styles.RenderKeyHints(o.footer...)
	}
	return o.ctx.Styles.RenderKeyHints("tab", "next", "S-tab", "back", "enter", "ok", "esc", "cancel")
}

func (o *formOverlay) Size(maxW, maxH int) (int, int) {
	w := min(o.prefW, maxW)
	if o.prefH > 0 {
		return w, min(o.prefH, maxH)
	}
	o.layout(w, maxH)
	h := len(o.formLines()) + 1
	if o.err != "" || o.busy {
		h++
	}
	return w, min(max(h, 3), maxH)
}

// layout sizes the form for the content area. It uses huh's own
// WindowSizeMsg handling, which sizes every group to the tallest group's
// natural height (capped at h), so short forms stay short.
func (o *formOverlay) layout(w, h int) {
	if w == o.w && h == o.h {
		return
	}
	o.w, o.h = w, h
	if o.form.State != huh.StateNormal {
		return
	}
	m, _ := o.form.Update(tea.WindowSizeMsg{Width: w, Height: max(h-1, 1)})
	if f, ok := m.(*huh.Form); ok {
		o.form = f
	}
}

func (o *formOverlay) Update(msg tea.Msg) (ui.Overlay, tea.Cmd) {
	switch msg := msg.(type) {
	case formDoneMsg:
		if msg.owner != o {
			return o, nil
		}
		o.busy = false
		if msg.err != nil {
			o.err = msg.err.Error()
			o.rebuild()
			return o, o.form.Init()
		}
		var cmds []tea.Cmd
		if msg.follow != nil {
			f := msg.follow
			cmds = append(cmds, func() tea.Msg { return f })
		}
		if msg.toast != "" {
			cmds = append(cmds, ui.Toast(ui.ToastSuccess, msg.toast))
		}
		return nil, tea.Batch(cmds...)
	case tea.KeyPressMsg:
		if o.busy {
			return o, nil
		}
		switch keymap.FromTea(msg) {
		case "esc":
			if !o.fieldFiltering() {
				return nil, nil
			}
		case "ctrl+c":
			return nil, nil
		}
	case tea.MouseMsg:
		return o, nil
	}
	if o.busy || o.form.State != huh.StateNormal {
		return o, nil
	}
	m, cmd := o.form.Update(msg)
	if f, ok := m.(*huh.Form); ok {
		o.form = f
	}
	cmd = o.settle(cmd, 0)
	cmds := []tea.Cmd{cmd}
	if _, isKey := msg.(tea.KeyPressMsg); isKey {
		o.err = ""
	}
	if o.onChange != nil {
		cmds = append(cmds, o.onChange())
	}
	switch o.form.State {
	case huh.StateCompleted:
		if o.submit == nil {
			return nil, tea.Batch(cmds...)
		}
		o.busy = true
		cmds = append(cmds, o.submit())
	case huh.StateAborted:
		return nil, tea.Batch(cmds...)
	}
	return o, tea.Batch(cmds...)
}

// rebuild recreates the form after a failed submit.
func (o *formOverlay) rebuild() {
	o.form = o.build()
	w, h := o.w, o.h
	o.w, o.h = 0, 0
	if w > 0 {
		o.layout(w, h)
	}
}

// fieldFiltering reports whether the focused field is a Select whose filter
// is active (esc then clears the filter instead of closing the dialog).
func (o *formOverlay) fieldFiltering() bool {
	type filterer interface{ GetFiltering() bool }
	if f, ok := o.form.GetFocusedField().(filterer); ok {
		return f.GetFiltering()
	}
	return false
}

func (o *formOverlay) View(w, h int) string {
	o.layout(w, h)
	st := o.ctx.Styles
	lines := o.formLines()
	var tail string
	switch {
	case o.busy:
		tail = st.InfoText.Render(trunc("… working", w))
	case o.err != "":
		tail = statusLine(st, o.err, true, w)
	}
	if tail != "" {
		lines = append(lines[:min(len(lines), max(h-1, 0))], tail)
	}
	return clampLines(lines, w, h)
}

// formLines is the form's view without the blank padding huh adds below the
// fields.
func (o *formOverlay) formLines() []string {
	lines := strings.Split(o.form.View(), "\n")
	for len(lines) > 1 && strings.TrimSpace(stripANSI(lines[len(lines)-1])) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// ---- synchronous focus changes ----------------------------------------------

// huh moves focus asynchronously: Tab in a field returns the command
// huh.NextField, whose message only reaches the form after a round trip
// through the Bubble Tea event loop (and the last field of a group then
// returns nextGroup, another round trip). Keys the terminal delivered in the
// same read as the Tab ("Lunch?<tab>Pizza" typed or pasted quickly) are
// already queued ahead of that message, so they land in the field that is
// being left ("Lunch?P", then "izza" in the next field).
//
// settle closes the gap: it looks for huh's focus commands in cmd (inside
// tea.Batch trees), runs them right away (they only return a marker
// message) and feeds their messages to the form before the overlay returns,
// so the focus has moved before the next key is processed. Everything else
// in cmd (cursor blinks, validation, submit) stays asynchronous.

var (
	pcNextField = funcPC(huh.NextField)
	pcPrevField = funcPC(huh.PrevField)
	// tea.Batch and tea.Sequence of two or more commands return a closure
	// that just returns the slice; both share compactCmds' code.
	pcBatch    = funcPC(tea.Batch(func() tea.Msg { return nil }, func() tea.Msg { return nil }))
	pcSequence = funcPC(tea.Sequence(func() tea.Msg { return nil }, func() tea.Msg { return nil }))
)

// huhGroupFuncs are huh's unexported group navigation commands.
var huhGroupFuncs = map[string]bool{
	"charm.land/huh/v2.nextGroup": true,
	"charm.land/huh/v2.prevGroup": true,
}

func funcPC(cmd tea.Cmd) uintptr {
	if cmd == nil {
		return 0
	}
	return reflect.ValueOf(cmd).Pointer()
}

// isFocusCmd reports whether cmd is one of huh's instant focus commands.
func isFocusCmd(cmd tea.Cmd) bool {
	pc := funcPC(cmd)
	if pc == pcNextField || pc == pcPrevField {
		return true
	}
	if fn := runtime.FuncForPC(pc); fn != nil {
		return huhGroupFuncs[fn.Name()]
	}
	return false
}

// settle applies huh's focus commands in cmd synchronously and returns the
// rest. depth bounds the recursion (each focus change can produce another).
func (o *formOverlay) settle(cmd tea.Cmd, depth int) tea.Cmd {
	if cmd == nil || depth > 16 {
		return cmd
	}
	if isFocusCmd(cmd) {
		if o.form.State != huh.StateNormal {
			return nil
		}
		m, next := o.form.Update(cmd())
		if f, ok := m.(*huh.Form); ok {
			o.form = f
		}
		return o.settle(next, depth+1)
	}
	pc := funcPC(cmd)
	if pc != pcBatch && pc != pcSequence {
		return cmd
	}
	// A batch/sequence closure: calling it only builds the slice.
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		// A sequence (unexported type): keep its order, deliver as is.
		return func() tea.Msg { return msg }
	}
	out := make([]tea.Cmd, 0, len(batch))
	for _, c := range batch {
		out = append(out, o.settle(c, depth+1))
	}
	return tea.Batch(out...)
}
