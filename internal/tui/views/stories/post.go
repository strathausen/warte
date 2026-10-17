package stories

import (
	"context"
	"errors"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"warte/internal/core"
	"warte/internal/tui/keymap"
	"warte/internal/tui/ui"
)

// KeyContextPost is the keymap overlay context of the story composer
// ("overlay.story.post"). The composer captures keys raw (it hosts a text
// field), so the context only matters for help and future remaps.
const KeyContextPost = "story.post"

// MaxTextStoryLength is Signal's limit for text story bodies.
const MaxTextStoryLength = 700

type focusArea int

const (
	focusText focusArea = iota
	focusAudience
)

type audienceItem struct {
	chat  core.ChatID // "" = My Story
	title string
	on    bool
}

type groupsLoadedMsg struct {
	p     *Post
	chats []core.Chat
	err   error
}

type postDoneMsg struct {
	p   *Post
	err error
	// mineOK / groupsOK report which half of a split post went through, so a
	// retry after a partial failure does not post twice.
	mineOK, groupsOK bool
}

// Post is the story composer overlay: a text story with color presets and
// font styles, or a media story (from ":story post <path>"), sent to My Story
// and/or groups.
type Post struct {
	ctx      ui.Context
	input    textinput.Model
	preset   int
	font     int
	media    *core.OutgoingAttachment
	audience []audienceItem
	sel      int
	focus    focusArea
	posting  bool
}

// NewPost returns the text story composer.
func NewPost(ctx ui.Context) *Post {
	p := &Post{ctx: ctx, audience: []audienceItem{{title: "My Story", on: true}}}
	ti := textinput.New()
	ti.Prompt = "› "
	ti.Placeholder = "What's on your mind?"
	ti.CharLimit = MaxTextStoryLength
	styleInput(&ti, ctx.Styles)
	ti.Focus() // focused from the start; Init re-issues the cursor command
	p.input = ti
	return p
}

// NewMediaPost returns the composer for a photo or video story; the text
// field edits the caption.
func NewMediaPost(ctx ui.Context, att core.OutgoingAttachment) *Post {
	p := NewPost(ctx)
	a := att
	p.media = &a
	p.input.Placeholder = "Add a caption…"
	p.input.CharLimit = 0
	p.input.SetValue(att.Caption)
	p.input.CursorEnd()
	return p
}

// Init implements ui.Overlay.
func (p *Post) Init() tea.Cmd {
	svc := p.ctx.Svc
	cmds := []tea.Cmd{p.input.Focus()}
	if svc != nil {
		cmds = append(cmds, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
			defer cancel()
			chats, err := svc.Chats(ctx, core.ChatFilter{GroupsOnly: true, IncludeEmpty: true})
			return groupsLoadedMsg{p: p, chats: chats, err: err}
		})
	}
	return tea.Batch(cmds...)
}

// Title implements ui.Overlay.
func (p *Post) Title() string {
	if p.media != nil {
		return "New media story"
	}
	return "New text story"
}

// KeyContext implements ui.Overlay.
func (p *Post) KeyContext() string { return KeyContextPost }

// CaptureKeys implements ui.KeyCapturer: the composer handles all keys.
func (p *Post) CaptureKeys() bool { return true }

// Footer implements ui.Footer.
func (p *Post) Footer() string {
	st := p.ctx.Styles
	if p.posting {
		return st.RenderKeyHints("", "posting…")
	}
	if p.focus == focusAudience {
		return st.RenderKeyHints("space", "toggle", "i", "edit", "enter", "post", "esc", "close")
	}
	if p.media != nil {
		return st.RenderKeyHints("enter", "post", "↓", "audience", "esc", "audience")
	}
	return st.RenderKeyHints("tab", "color", "ctrl+t", "font", "enter", "post", "↓", "audience")
}

// Size implements ui.Overlay.
func (p *Post) Size(maxW, maxH int) (int, int) {
	return min(maxW, 64), min(maxH, 26)
}

// textStory is the story being composed (text mode).
func (p *Post) textStory() *core.TextStory {
	pr := Presets[p.preset%len(Presets)]
	return &core.TextStory{
		Text:       p.input.Value(),
		Style:      p.font,
		Foreground: pr.Foreground,
		Background: pr.Background,
		Gradient:   append([]uint32(nil), pr.Gradient...),
	}
}

// Update implements ui.Overlay.
func (p *Post) Update(msg tea.Msg) (ui.Overlay, tea.Cmd) {
	switch msg := msg.(type) {
	case groupsLoadedMsg:
		if msg.p != p {
			return p, nil
		}
		if msg.err != nil {
			return p, ui.Toast(ui.ToastWarning, "Couldn't load groups: "+msg.err.Error())
		}
		for _, c := range msg.chats {
			if c.Kind != core.ChatGroup || c.Blocked {
				continue
			}
			p.audience = append(p.audience, audienceItem{chat: c.ID, title: c.Title})
		}
	case postDoneMsg:
		if msg.p != p {
			return p, nil
		}
		p.posting = false
		if msg.err != nil {
			text := "Couldn't post story: " + msg.err.Error()
			if errors.Is(msg.err, errors.ErrUnsupported) {
				text = "Posting stories isn't supported by this backend yet"
			}
			if msg.mineOK || msg.groupsOK {
				// Deselect where it was posted so a retry only resends the rest.
				for i := range p.audience {
					if (p.audience[i].chat == "" && msg.mineOK) || (p.audience[i].chat != "" && msg.groupsOK) {
						p.audience[i].on = false
					}
				}
				text = "Story posted only partly. " + text
			}
			return p, ui.Toast(ui.ToastError, text)
		}
		// Overlays below (the stories list) missed events while covered.
		return nil, tea.Batch(ui.Toast(ui.ToastSuccess, "Story posted"), loadStories(p.ctx.Svc, selfACI(p.ctx)))
	case tea.PasteMsg:
		if p.focus == focusText && !p.posting {
			var cmd tea.Cmd
			p.input, cmd = p.input.Update(msg)
			return p, cmd
		}
	case tea.KeyPressMsg:
		return p.key(msg)
	case ui.ActionMsg:
		if msg.Action == keymap.ActBack {
			return nil, nil
		}
	}
	return p, nil
}

func (p *Post) key(msg tea.KeyPressMsg) (ui.Overlay, tea.Cmd) {
	k := msg.String()
	if k == "ctrl+c" {
		return nil, nil
	}
	if p.posting {
		return p, nil
	}
	switch k {
	case "tab":
		if p.media == nil {
			p.preset = (p.preset + 1) % len(Presets)
		}
		return p, nil
	case "shift+tab":
		if p.media == nil {
			p.preset = (p.preset + len(Presets) - 1) % len(Presets)
		}
		return p, nil
	case "ctrl+t":
		if p.media == nil {
			p.font = (p.font + 1) % len(FontStyles)
		}
		return p, nil
	case "ctrl+s":
		return p.submit()
	}
	if p.focus == focusText {
		switch k {
		case "enter":
			return p.submit()
		case "esc", "down":
			p.focus = focusAudience
			p.input.Blur()
			return p, nil
		}
		var cmd tea.Cmd
		p.input, cmd = p.input.Update(msg)
		return p, cmd
	}
	switch k {
	case "esc", "q":
		return nil, nil
	case "j", "down":
		p.sel = min(p.sel+1, len(p.audience)-1)
	case "k", "up":
		if p.sel == 0 && k == "up" {
			p.focus = focusText
			return p, p.input.Focus()
		}
		p.sel = max(p.sel-1, 0)
	case "g", "home":
		p.sel = 0
	case "G", "end":
		p.sel = len(p.audience) - 1
	case "space", "x":
		if p.sel >= 0 && p.sel < len(p.audience) {
			p.audience[p.sel].on = !p.audience[p.sel].on
		}
	case "enter":
		return p.submit()
	case "i", "a", "/":
		p.focus = focusText
		return p, p.input.Focus()
	}
	return p, nil
}

// submit validates the form and posts the story.
func (p *Post) submit() (ui.Overlay, tea.Cmd) {
	var out core.OutgoingStory
	if p.media != nil {
		m := *p.media
		m.Caption = strings.TrimSpace(p.input.Value())
		out.Media = &m
	} else {
		ts := p.textStory()
		ts.Text = strings.TrimSpace(ts.Text)
		if ts.Text == "" {
			return p, ui.Toast(ui.ToastWarning, "Write something first")
		}
		out.Text = ts
	}
	mine := false
	var groups []core.ChatID
	for _, a := range p.audience {
		switch {
		case !a.on:
		case a.chat == "":
			mine = true
		default:
			groups = append(groups, a.chat)
		}
	}
	if !mine && len(groups) == 0 {
		return p, ui.Toast(ui.ToastWarning, "Choose who can see this story")
	}
	svc := p.ctx.Svc
	if svc == nil {
		return p, ui.Toast(ui.ToastError, "Not connected")
	}
	p.posting = true
	return p, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*queryTimeout)
		defer cancel()
		var errs []error
		done := postDoneMsg{p: p}
		if mine {
			err := svc.PostStory(ctx, out)
			done.mineOK = err == nil
			errs = append(errs, err)
		}
		if len(groups) > 0 {
			g := out
			g.To = groups
			err := svc.PostStory(ctx, g)
			done.groupsOK = err == nil
			errs = append(errs, err)
		}
		done.err = errors.Join(errs...)
		return done
	}
}

// ---- rendering ---------------------------------------------------------------------

// View implements ui.Overlay.
func (p *Post) View(w, h int) string {
	if w < 1 || h < 1 {
		return ""
	}
	st := p.ctx.Styles
	pal := st.Theme.Palette
	muted := lipgloss.NewStyle().Foreground(pal.FgMuted)
	accent := lipgloss.NewStyle().Foreground(pal.Accent)
	label := lipgloss.NewStyle().Foreground(pal.FgMuted).Bold(true)

	p.input.SetWidth(max(w-width(p.input.Prompt)-1, 1))
	var top []string
	top = append(top, p.input.View())
	if p.media == nil {
		pr := Presets[p.preset%len(Presets)]
		opts := accent.Render("◀ ") + lipgloss.NewStyle().Foreground(pal.Fg).Bold(true).Render(pr.Name) +
			accent.Render(" ▶") + muted.Render("   font ") + lipgloss.NewStyle().Foreground(pal.Fg).Render(fontStyleName(p.font))
		count := muted.Render(itoa(len([]rune(p.input.Value()))) + "/" + itoa(MaxTextStoryLength))
		if width(opts)+2+width(count) <= w {
			opts = padRight(opts, w-width(count)) + count
		}
		top = append(top, opts)
	}

	// Audience rows get up to a third of the height; the preview the rest.
	head := 2 // blank + "Send to"
	aRows := min(len(p.audience), max(2, (h-len(top)-head)/3))
	previewH := h - len(top) - head - aRows - 1 // blank above preview
	lines := append([]string{}, top...)
	if previewH >= 3 {
		lines = append(lines, "")
		lines = append(lines, p.preview(w, previewH)...)
	} else {
		aRows = max(h-len(lines)-head, 1)
	}

	lines = append(lines, "")
	hs := label
	if p.focus == focusAudience {
		hs = accent.Bold(true)
	}
	n := 0
	for _, a := range p.audience {
		if a.on {
			n++
		}
	}
	lines = append(lines, hs.Render("SEND TO")+muted.Render("  "+itoa(n)+" selected"))

	off := 0
	if p.sel >= aRows {
		off = p.sel - aRows + 1
	}
	for i := off; i < len(p.audience) && i < off+aRows; i++ {
		lines = append(lines, p.audienceRow(i, w))
	}
	return clip(lines, w, h)
}

func (p *Post) audienceRow(i, w int) string {
	st := p.ctx.Styles
	pal := st.Theme.Palette
	a := p.audience[i]
	cursor := "  "
	if p.focus == focusAudience && i == p.sel {
		c := p.ctx.Icons.Cursor
		if c == "" || width(c) > 2 {
			c = "›"
		}
		cursor = lipgloss.NewStyle().Foreground(pal.Accent).Render(padRight(c, 2))
	}
	box := lipgloss.NewStyle().Foreground(pal.FgSubtle).Render("○")
	if a.on {
		box = lipgloss.NewStyle().Foreground(pal.Accent).Render("◉")
	}
	icon := p.ctx.Icons.Group
	if a.chat == "" {
		icon = p.ctx.Icons.Story
	}
	if icon != "" {
		icon += " "
	}
	title := oneLine(a.title)
	if title == "" {
		title = "Unnamed group"
	}
	ts := lipgloss.NewStyle().Foreground(pal.Fg)
	if p.focus == focusAudience && i == p.sel {
		ts = ts.Bold(true)
	}
	return cursor + box + " " + icon + ts.Render(truncate(title, max(w-6-width(icon), 1)))
}

// preview draws the story as it will look (text) or a file card (media).
func (p *Post) preview(w, h int) []string {
	st := p.ctx.Styles
	if p.media != nil {
		v := &Viewer{ctx: p.ctx}
		a := core.Attachment{ContentType: p.media.ContentType, FileName: p.media.FileName,
			Width: p.media.Width, Height: p.media.Height}
		return v.mediaCard(a, w, h)
	}
	ts := p.textStory()
	if strings.TrimSpace(ts.Text) == "" {
		ts.Text = "Your story"
	}
	pw := min(w, max(28, h*2))
	left := strings.Repeat(" ", (w-pw)/2)
	rows := renderTextStory(st, p.ctx.Icons, ts, pw, h)
	for i := range rows {
		rows[i] = left + rows[i]
	}
	return rows
}
