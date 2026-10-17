package media

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"warte/internal/tui/theme"
	"warte/internal/tui/ui"
)

// KeyContextSave is the keymap context of the save dialog. It captures all
// keys while open (it holds a text field).
const KeyContextSave = "media.save"

// SaveDialog asks for the target path before writing an attachment outside
// the encrypted store (to ~/Downloads by default, mode 0644).
type SaveDialog struct {
	ctx       ui.Context
	it        Item
	input     textinput.Model
	status    string
	level     ui.ToastLevel
	overwrite bool // the next enter overwrites an existing file
	saving    bool
}

// savedMsg reports the result of a save.
type savedMsg struct {
	dlg  *SaveDialog
	path string
	err  error
}

// SaveCmd returns the command for "save" on an item: a toast for view-once
// media (which may not leave warte), otherwise it pushes a SaveDialog.
func SaveCmd(ctx ui.Context, it Item) tea.Cmd {
	if it.Message.ViewOnce {
		return toast(ui.ToastWarning, "View-once media can't be saved")
	}
	return ui.Push(NewSaveDialog(ctx, it))
}

// NewSaveDialog builds the save dialog for it, prefilled with
// ~/Downloads/<sanitized file name>.
func NewSaveDialog(ctx ui.Context, it Item) *SaveDialog {
	ti := textinput.New()
	ti.Prompt = "Save as "
	ti.CharLimit = 1024
	styleInput(&ti, ctx.Styles)
	ti.SetValue(tildePath(filepath.Join(downloadsDir(), fileName(it))))
	ti.CursorEnd()
	ti.Focus()
	return &SaveDialog{ctx: ctx, it: it, input: ti}
}

func styleInput(ti *textinput.Model, st *theme.Styles) {
	p := st.Theme.Palette
	s := textinput.DefaultStyles(st.Theme.Dark)
	s.Focused.Text = lipgloss.NewStyle().Foreground(p.Fg)
	s.Blurred.Text = lipgloss.NewStyle().Foreground(p.FgMuted)
	s.Focused.Placeholder = st.Placeholder
	s.Blurred.Placeholder = st.Placeholder
	s.Focused.Prompt = lipgloss.NewStyle().Foreground(p.Accent)
	s.Blurred.Prompt = lipgloss.NewStyle().Foreground(p.FgMuted)
	s.Cursor.Color = p.Accent
	s.Cursor.Blink = false
	ti.SetStyles(s)
}

// Init focuses the path field.
func (d *SaveDialog) Init() tea.Cmd { return d.input.Focus() }

// Title implements ui.Overlay.
func (d *SaveDialog) Title() string { return "Save " + strings.ToLower(kindLabel(d.it.Attachment)) }

// KeyContext implements ui.Overlay.
func (d *SaveDialog) KeyContext() string { return KeyContextSave }

// CaptureKeys implements ui.KeyCapturer: the dialog edits text.
func (d *SaveDialog) CaptureKeys() bool { return true }

// Size implements ui.Overlay.
func (d *SaveDialog) Size(maxW, maxH int) (int, int) { return min(maxW, 76), min(maxH, 5) }

// Footer implements ui.Footer.
func (d *SaveDialog) Footer() string {
	if d.overwrite {
		return d.ctx.Styles.RenderKeyHints("enter", "overwrite", "esc", "cancel")
	}
	return d.ctx.Styles.RenderKeyHints("enter", "save", "esc", "cancel")
}

// target resolves the typed path.
func (d *SaveDialog) target() (string, error) {
	p := strings.TrimSpace(d.input.Value())
	if p == "" {
		return "", errors.New("enter a file name")
	}
	p = expandHome(p)
	if !filepath.IsAbs(p) {
		p = filepath.Join(downloadsDir(), p)
	}
	if st, err := os.Stat(p); err == nil && st.IsDir() {
		p = filepath.Join(p, fileName(d.it))
	}
	return filepath.Clean(p), nil
}

// Update implements ui.Overlay.
func (d *SaveDialog) Update(msg tea.Msg) (ui.Overlay, tea.Cmd) {
	switch msg := msg.(type) {
	case savedMsg:
		if msg.dlg != d {
			return d, nil
		}
		d.saving = false
		switch {
		case errors.Is(msg.err, errExists):
			d.overwrite = true
			d.status, d.level = "File exists. Press enter again to overwrite it.", ui.ToastWarning
		case msg.err != nil:
			d.status, d.level = clean(msg.err.Error()), ui.ToastError
		default:
			return nil, toast(ui.ToastSuccess, "Saved to "+clean(tildePath(msg.path)))
		}
	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc", "ctrl+c":
			return nil, nil
		case "enter":
			if d.saving {
				return d, nil
			}
			path, err := d.target()
			if err != nil {
				d.status, d.level = err.Error(), ui.ToastError
				return d, nil
			}
			d.saving = true
			d.status, d.level = "Saving…", ui.ToastInfo
			return d, d.save(path, d.overwrite)
		}
		before := d.input.Value()
		var cmd tea.Cmd
		d.input, cmd = d.input.Update(msg)
		if d.input.Value() != before {
			d.overwrite = false
			d.status = ""
		}
		return d, cmd
	case tea.PasteMsg:
		var cmd tea.Cmd
		d.input, cmd = d.input.Update(msg)
		d.overwrite, d.status = false, ""
		return d, cmd
	}
	return d, nil
}

// save fetches the attachment and writes it.
func (d *SaveDialog) save(path string, overwrite bool) tea.Cmd {
	svc := d.ctx.Svc
	it := d.it
	return func() tea.Msg {
		if svc == nil {
			return savedMsg{dlg: d, path: path, err: errors.New("not connected")}
		}
		c, cancel := opCtx()
		defer cancel()
		data, err := svc.Attachment(c, it.Message.ID, it.Attachment.ID)
		if err != nil {
			return savedMsg{dlg: d, path: path, err: err}
		}
		return savedMsg{dlg: d, path: path, err: writeFile(path, data, overwrite)}
	}
}

// View implements ui.Overlay.
func (d *SaveDialog) View(w, h int) string {
	st := d.ctx.Styles
	warn := st.WarningText.Render(ellipsis("⚠ The saved copy is not encrypted: it leaves warte's encrypted store.", w))
	d.input.SetWidth(max(w-width(d.input.Prompt)-1, 1))
	status := ""
	if d.status != "" {
		s := st.InfoText
		switch d.level {
		case ui.ToastError:
			s = st.ErrorText
		case ui.ToastWarning:
			s = st.WarningText
		}
		status = s.Render(ellipsis(d.status, w))
	}
	lines := []string{warn, "", d.input.View(), "", status}
	return strings.Join(fitLines(lines, w, h), "\n")
}
