package stories

import (
	"context"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"warte/internal/core"
	"warte/internal/format"
	"warte/internal/tui/keymap"
	"warte/internal/tui/theme"
	"warte/internal/tui/ui"
)

// KeyContextViewer is the keymap overlay context of the story viewer
// (keymap.OverlayStory, i.e. "overlay.story").
const KeyContextViewer = keymap.OverlayStory

// StoryDuration is how long each story is shown before the viewer advances.
var StoryDuration = 5 * time.Second

// frameInterval is the progress animation step (without ReduceMotion).
const frameInterval = 100 * time.Millisecond

type tickMsg struct {
	v    *Viewer
	gen  int
	step time.Duration
}

type replyDoneMsg struct {
	v     *Viewer
	emoji string
	err   error
}

// Viewer is the full-screen story viewer overlay.
type Viewer struct {
	ctx    ui.Context
	groups []authorGroup
	titles map[core.ChatID]string
	gi, si int

	duration time.Duration
	elapsed  time.Duration
	paused   bool
	gen      int

	replying bool
	input    textinput.Model
	loading  bool // opened without groups: waiting for loadStories
	viewW    int  // width of the last View (mouse hit testing only)
}

// NewViewer returns a viewer over all current stories, starting with the
// first author that has unviewed stories. It loads the stories itself.
func NewViewer(ctx ui.Context) *Viewer {
	v := newViewer(ctx, nil, nil, 0)
	v.loading = true
	return v
}

// newViewer starts at author gi of groups (at its first unviewed story).
func newViewer(ctx ui.Context, groups []authorGroup, titles map[core.ChatID]string, gi int) *Viewer {
	v := &Viewer{ctx: ctx, titles: titles, duration: StoryDuration}
	v.groups = make([]authorGroup, len(groups))
	for i, g := range groups {
		g.Stories = append([]core.Story(nil), g.Stories...)
		v.groups[i] = g
	}
	if v.titles == nil {
		v.titles = map[core.ChatID]string{}
	}
	v.gi = max(0, min(gi, len(v.groups)-1))
	if v.gi < len(v.groups) {
		v.si = v.groups[v.gi].firstUnviewed()
	}
	ti := textinput.New()
	ti.Prompt = "↩ "
	ti.Placeholder = "Reply…"
	ti.CharLimit = 2000
	styleInput(&ti, ctx.Styles)
	v.input = ti
	return v
}

// styleInput themes a text input from the current styles.
func styleInput(ti *textinput.Model, st *theme.Styles) {
	p := st.Theme.Palette
	s := textinput.DefaultStyles(st.Theme.Dark)
	s.Focused.Text = lipgloss.NewStyle().Foreground(p.Fg)
	s.Blurred.Text = lipgloss.NewStyle().Foreground(p.FgMuted)
	s.Focused.Placeholder = st.Placeholder
	s.Blurred.Placeholder = st.Placeholder
	s.Focused.Prompt = lipgloss.NewStyle().Foreground(p.Accent)
	s.Blurred.Prompt = lipgloss.NewStyle().Foreground(p.FgSubtle)
	s.Cursor.Color = p.Accent
	s.Cursor.Blink = false
	ti.SetStyles(s)
}

func (v *Viewer) reduceMotion() bool { return v.ctx.Config.ReduceMotion }

func (v *Viewer) self() core.ACI { return selfACI(v.ctx) }

func (v *Viewer) now() time.Time {
	if v.ctx.Now != nil {
		return v.ctx.Now()
	}
	return time.Now()
}

// current returns the story on screen, or nil.
func (v *Viewer) current() (*authorGroup, *core.Story) {
	if v.gi < 0 || v.gi >= len(v.groups) {
		return nil, nil
	}
	g := &v.groups[v.gi]
	if v.si < 0 || v.si >= len(g.Stories) {
		return g, nil
	}
	return g, &g.Stories[v.si]
}

// Init implements ui.Overlay.
func (v *Viewer) Init() tea.Cmd {
	if v.loading {
		return loadStories(v.ctx.Svc, v.self())
	}
	return v.enter()
}

// Title implements ui.Overlay.
func (v *Viewer) Title() string {
	g, _ := v.current()
	if g == nil {
		return "Stories"
	}
	if g.Self {
		return "My stories"
	}
	return truncate(oneLine(g.Name), 40)
}

// KeyContext implements ui.Overlay.
func (v *Viewer) KeyContext() string { return KeyContextViewer }

// CaptureKeys implements ui.KeyCapturer. The viewer always takes keys raw:
// space (the leader) pauses and the digits 1-6 react, which the keymap
// engine would otherwise treat as a leader sequence and a count.
func (v *Viewer) CaptureKeys() bool { return true }

// Footer implements ui.Footer.
func (v *Viewer) Footer() string {
	st := v.ctx.Styles
	if v.replying {
		return st.RenderKeyHints("enter", "send", "esc", "cancel")
	}
	play := "pause"
	if v.paused {
		play = "play"
	}
	return st.RenderKeyHints("h/l", "story", "j/k", "person", "space", play)
}

// Size implements ui.Overlay: full height, phone-like width.
func (v *Viewer) Size(maxW, maxH int) (int, int) {
	w := max(48, maxH*5/4)
	return min(w, maxW), maxH
}

// storyDuration is how long story s stays on screen.
func (v *Viewer) storyDuration(s *core.Story) time.Duration {
	d := v.duration
	if d <= 0 {
		d = 5 * time.Second
	}
	if s != nil && s.Text != nil {
		// Long texts get a bit more time, up to twice the base duration.
		extra := time.Duration(len([]rune(s.Text.Text))/60) * time.Second
		d += min(extra, d)
	}
	return d
}

// enter resets the timer for the current story and marks it viewed.
func (v *Viewer) enter() tea.Cmd {
	v.gen++
	v.elapsed = 0
	g, s := v.current()
	var cmds []tea.Cmd
	if s != nil && g != nil && !g.Self && !s.Viewed {
		s.Viewed = true
		svc, id := v.ctx.Svc, s.ID
		if svc != nil {
			cmds = append(cmds, func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
				defer cancel()
				_ = svc.MarkStoryViewed(ctx, id) // best effort; the next load corrects it
				return nil
			})
		}
	}
	cmds = append(cmds, v.tick())
	return tea.Batch(cmds...)
}

// tick schedules the next progress step (nothing while paused).
func (v *Viewer) tick() tea.Cmd {
	if v.paused || v.replying {
		return nil
	}
	_, s := v.current()
	if s == nil {
		return nil
	}
	step := frameInterval
	if v.reduceMotion() {
		step = max(v.storyDuration(s)-v.elapsed, time.Millisecond)
	}
	gen := v.gen
	return tea.Tick(step, func(time.Time) tea.Msg { return tickMsg{v: v, gen: gen, step: step} })
}

// setPaused pauses or resumes auto-advance.
func (v *Viewer) setPaused(p bool) tea.Cmd {
	if v.paused == p {
		return nil
	}
	v.paused = p
	v.gen++ // drop the pending tick
	return v.tick()
}

// next advances to the next story (then the next author); ok=false when
// there is nothing left and the viewer should close.
func (v *Viewer) next() (ok bool, cmd tea.Cmd) {
	g, _ := v.current()
	if g == nil {
		return false, nil
	}
	if v.si+1 < len(g.Stories) {
		v.si++
		return true, v.enter()
	}
	return v.nextAuthor()
}

func (v *Viewer) prev() tea.Cmd {
	switch {
	case v.si > 0:
		v.si--
	case v.gi > 0:
		v.gi--
		v.si = max(len(v.groups[v.gi].Stories)-1, 0)
	}
	return v.enter()
}

func (v *Viewer) nextAuthor() (bool, tea.Cmd) {
	if v.gi+1 >= len(v.groups) {
		return false, nil
	}
	v.gi++
	v.si = v.groups[v.gi].firstUnviewed()
	return true, v.enter()
}

func (v *Viewer) prevAuthor() tea.Cmd {
	if v.gi > 0 {
		v.gi--
	}
	v.si = 0
	return v.enter()
}

// close closes the viewer. Overlays below it (the stories list) get no
// events while covered, so it reloads the stories for them: the result is
// delivered to whatever is on top after the pop.
func (v *Viewer) close() (ui.Overlay, tea.Cmd) {
	v.gen++ // any tick still in flight is stale
	return nil, loadStories(v.ctx.Svc, v.self())
}

// Update implements ui.Overlay.
func (v *Viewer) Update(msg tea.Msg) (ui.Overlay, tea.Cmd) {
	switch msg := msg.(type) {
	case tickMsg:
		if msg.v != v || msg.gen != v.gen || v.paused || v.replying {
			return v, nil
		}
		v.elapsed += msg.step
		_, s := v.current()
		if v.elapsed >= v.storyDuration(s) {
			if ok, cmd := v.next(); ok {
				return v, cmd
			}
			return v.close()
		}
		return v, v.tick()
	case storiesLoadedMsg:
		return v.loaded(msg)
	case ui.ScreenLockMsg:
		return v, v.setPaused(true)
	case ui.EventMsg:
		if _, ok := msg.Event.(core.StoriesChanged); ok {
			return v, loadStories(v.ctx.Svc, v.self())
		}
	case replyDoneMsg:
		if msg.v != v {
			return v, nil
		}
		if msg.err != nil {
			return v, ui.Toast(ui.ToastError, "Couldn't send story reply: "+msg.err.Error())
		}
		if msg.emoji != "" {
			return v, ui.Toast(ui.ToastSuccess, "Reacted "+msg.emoji+" to the story")
		}
		return v, ui.Toast(ui.ToastSuccess, "Reply sent")
	case tea.PasteMsg:
		if v.replying {
			var cmd tea.Cmd
			v.input, cmd = v.input.Update(msg)
			return v, cmd
		}
	case tea.KeyPressMsg:
		return v.key(msg)
	case ui.ActionMsg:
		return v.action(msg.Action)
	case tea.MouseClickMsg:
		m := msg.Mouse()
		if m.Button != tea.MouseLeft || v.replying {
			return v, nil
		}
		if m.X < v.viewW/3 {
			return v, v.prev()
		}
		if ok, cmd := v.next(); ok {
			return v, cmd
		}
		return v.close()
	}
	return v, nil
}

// loaded merges freshly loaded stories: on first load it builds the author
// list, later it only refreshes viewed flags and reply counts so the
// position does not jump.
func (v *Viewer) loaded(msg storiesLoadedMsg) (ui.Overlay, tea.Cmd) {
	if msg.err != nil {
		if v.loading {
			v.loading = false
			return v, ui.Toast(ui.ToastError, "Couldn't load stories: "+msg.err.Error())
		}
		return v, nil
	}
	for k, t := range msg.titles {
		v.titles[k] = t
	}
	if v.loading {
		v.loading = false
		v.groups = msg.groups
		v.gi, v.si = 0, 0
		for i := range v.groups {
			if v.groups[i].unviewed() > 0 {
				v.gi = i
				v.si = v.groups[i].firstUnviewed()
				break
			}
		}
		return v, v.enter()
	}
	fresh := map[core.MessageID]core.Story{}
	for _, g := range msg.groups {
		for _, s := range g.Stories {
			fresh[s.ID] = s
		}
	}
	for gi := range v.groups {
		for si := range v.groups[gi].Stories {
			s := &v.groups[gi].Stories[si]
			if f, ok := fresh[s.ID]; ok {
				s.Replies = f.Replies
				s.Viewed = s.Viewed || f.Viewed
			}
		}
	}
	return v, nil
}

func (v *Viewer) key(msg tea.KeyPressMsg) (ui.Overlay, tea.Cmd) {
	k := msg.String()
	if v.replying {
		switch k {
		case "esc", "ctrl+c":
			v.replying = false
			v.input.Blur()
			v.input.Reset()
			v.gen++
			return v, v.tick()
		case "enter":
			body := strings.TrimSpace(v.input.Value())
			v.replying = false
			v.input.Blur()
			v.input.Reset()
			v.gen++
			if body == "" {
				return v, v.tick()
			}
			return v, tea.Batch(v.send(body, ""), v.tick())
		}
		var cmd tea.Cmd
		v.input, cmd = v.input.Update(msg)
		return v, cmd
	}
	switch k {
	case "esc", "q", "ctrl+c":
		return v.close()
	case "l", "right", "n":
		return v.action(keymap.ActStoryNext)
	case "h", "left", "p":
		return v.action(keymap.ActStoryPrev)
	case "j", "down", "tab":
		return v.action(keymap.ActCursorDown)
	case "k", "up", "shift+tab":
		return v.action(keymap.ActCursorUp)
	case "space":
		return v, v.setPaused(!v.paused)
	case "r":
		return v.action(keymap.ActMessageReply)
	case "1", "2", "3", "4", "5", "6":
		i := int(k[0] - '1')
		reactions := format.DefaultReactions()
		if i < len(reactions) {
			return v, v.react(reactions[i])
		}
	}
	return v, nil
}

func (v *Viewer) action(a keymap.Action) (ui.Overlay, tea.Cmd) {
	switch a {
	case keymap.ActStoryNext, keymap.ActCursorRight:
		if ok, cmd := v.next(); ok {
			return v, cmd
		}
		return v.close()
	case keymap.ActStoryPrev, keymap.ActCursorLeft:
		return v, v.prev()
	case keymap.ActCursorDown:
		if ok, cmd := v.nextAuthor(); ok {
			return v, cmd
		}
		return v.close()
	case keymap.ActCursorUp:
		return v, v.prevAuthor()
	case keymap.ActMessageReply:
		g, s := v.current()
		if s == nil {
			return v, nil
		}
		if g.Self {
			return v, ui.Toast(ui.ToastInfo, "You can't reply to your own story")
		}
		v.replying = true
		v.gen++ // pause while typing
		v.input.Placeholder = truncate("Reply to "+oneLine(g.Name)+"…", 40)
		return v, v.input.Focus()
	case keymap.ActBack:
		return v.close()
	}
	return v, nil
}

// react sends a quick reaction to the current story.
func (v *Viewer) react(emoji string) tea.Cmd {
	g, s := v.current()
	if s == nil {
		return nil
	}
	if g.Self {
		return ui.Toast(ui.ToastInfo, "You can't react to your own story")
	}
	return v.send("", emoji)
}

// send replies to the current story with text or an emoji reaction.
func (v *Viewer) send(body, emoji string) tea.Cmd {
	_, s := v.current()
	if s == nil || v.ctx.Svc == nil {
		return nil
	}
	svc, id := v.ctx.Svc, s.ID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		return replyDoneMsg{v: v, emoji: emoji, err: svc.ReplyToStory(ctx, id, body, emoji)}
	}
}

// ---- rendering ---------------------------------------------------------------------

// View implements ui.Overlay.
func (v *Viewer) View(w, h int) string {
	if w < 1 || h < 1 {
		return ""
	}
	v.viewW = w
	st := v.ctx.Styles
	muted := lipgloss.NewStyle().Foreground(st.Theme.Palette.FgMuted)
	g, s := v.current()
	if s == nil {
		msg := "No stories to show."
		if v.loading {
			msg = "Loading…"
		}
		lines := make([]string, h)
		lines[h/2] = center(muted.Render(truncate(msg, w)), w)
		return clip(lines, w, h)
	}
	var lines []string
	chrome := h >= 6
	if chrome {
		frac := float64(v.elapsed) / float64(v.storyDuration(s))
		if v.reduceMotion() {
			frac = 0
		}
		lines = append(lines, renderProgress(st, len(g.Stories), v.si, frac, w))
		lines = append(lines, v.header(g, s, w))
	}
	canvasH := h - len(lines) - 1
	if canvasH < 1 {
		canvasH = max(h-1, 1)
		lines = nil
	}
	lines = append(lines, v.canvas(s, w, canvasH)...)
	if len(lines) < h {
		lines = append(lines, v.bar(g, s, w))
	}
	return clip(lines, w, h)
}

// header is the author line: avatar, name, age, group and pause state.
func (v *Viewer) header(g *authorGroup, s *core.Story, w int) string {
	st := v.ctx.Styles
	p := st.Theme.Palette
	muted := lipgloss.NewStyle().Foreground(p.FgMuted)
	name := oneLine(g.Name)
	if g.Self {
		name = "You"
	}
	if name == "" {
		name = "Unknown"
	}
	right := ""
	if v.paused {
		right = lipgloss.NewStyle().Foreground(p.Warning).Render("⏸ paused")
	}
	left := st.RenderAvatar(g.Color, string(g.Author), oneLine(g.Name)) + " "
	meta := " · " + relTime(s.At, v.now())
	if s.Group != "" {
		title := oneLine(v.titles[s.Group])
		if title == "" {
			title = "group"
		}
		icon := v.ctx.Icons.Group
		if icon != "" {
			icon += " "
		}
		meta += " · " + icon + title
	}
	avail := max(w-width(left)-width(right)-1, 1)
	nameR := truncate(name, avail)
	rest := truncate(meta, max(avail-width(nameR), 0))
	line := left + lipgloss.NewStyle().Foreground(p.Fg).Bold(true).Render(nameR) + muted.Render(rest)
	if right != "" {
		line = padRight(line, w-width(right)) + right
	}
	return line
}

// canvas draws the story itself into exactly h lines of w cells.
func (v *Viewer) canvas(s *core.Story, w, h int) []string {
	if s.Text != nil || s.Media == nil {
		return renderTextStory(v.ctx.Styles, v.ctx.Icons, s.Text, w, h)
	}
	return v.media(s, w, h)
}

// media draws a photo/video story: the image fitted and centered, the
// caption below it.
func (v *Viewer) media(s *core.Story, w, h int) []string {
	st := v.ctx.Styles
	p := st.Theme.Palette
	var caption []string
	if c := strings.TrimSpace(theme.Sanitize(s.Caption)); c != "" && h >= 6 {
		caption = format.Wrap(c, max(w-4, 1))
		if len(caption) > 3 {
			caption = caption[:3]
			caption[2] = truncate(strings.TrimRight(caption[2], " "), max(w-5, 1)) + "…"
		}
	}
	imgH := h
	if len(caption) > 0 {
		imgH = h - len(caption) - 1
	}
	out := make([]string, 0, h)
	a := *s.Media
	images := v.ctx.Images
	if images != nil && images.Enabled() && images.Inline() && a.IsImage() && imgH >= 2 {
		ref := ui.ImageRef{Kind: ui.ImageAttachment, Message: s.ID, Attachment: a, Label: "Story"}
		cols, rows := images.Size(ref, w, imgH)
		cols, rows = max(1, min(cols, w)), max(1, min(rows, imgH))
		rl, _ := images.Rows(ref, cols, rows)
		top := (imgH - rows) / 2
		left := strings.Repeat(" ", (w-cols)/2)
		for i := 0; i < imgH; i++ {
			j := i - top
			if j >= 0 && j < len(rl) {
				out = append(out, left+rl[j])
			} else {
				out = append(out, "")
			}
		}
	} else {
		out = append(out, v.mediaCard(a, w, imgH)...)
	}
	if len(caption) > 0 {
		out = append(out, "")
		cs := lipgloss.NewStyle().Foreground(p.Fg)
		for _, c := range caption {
			out = append(out, center(cs.Render(c), w))
		}
	}
	return out
}

// mediaCard is the stand-in for media that can't be drawn inline (videos,
// images with images turned off).
func (v *Viewer) mediaCard(a core.Attachment, w, h int) []string {
	st := v.ctx.Styles
	p := st.Theme.Palette
	icon, label := v.ctx.Icons.Image, "Photo"
	if a.IsVideo() {
		icon, label = v.ctx.Icons.Video, "Video"
	}
	if icon == "" {
		icon = "▣"
	}
	info := []string{lipgloss.NewStyle().Foreground(p.Accent).Bold(true).Render(icon + "  " + label)}
	if name := oneLine(a.FileName); name != "" {
		info = append(info, lipgloss.NewStyle().Foreground(p.FgMuted).Render(truncate(name, max(w-6, 1))))
	}
	if a.Width > 0 && a.Height > 0 {
		info = append(info, st.Timestamp.Render(itoa(a.Width)+"×"+itoa(a.Height)))
	}
	out := make([]string, h)
	if h <= len(info)+1 {
		for i := 0; i < h && i < len(info); i++ {
			out[i] = center(info[i], w)
		}
		return out
	}
	boxW := min(w, max(28, maxWidth(info)+6))
	border := lipgloss.NewStyle().Foreground(p.Border)
	inner := boxW - 2
	boxH := min(h, len(info)+4)
	top := (h - boxH) / 2
	left := strings.Repeat(" ", (w-boxW)/2)
	infoTop := (boxH - 2 - len(info)) / 2
	for i := 0; i < boxH; i++ {
		var row string
		switch {
		case i == 0:
			row = border.Render("╭" + strings.Repeat("─", inner) + "╮")
		case i == boxH-1:
			row = border.Render("╰" + strings.Repeat("─", inner) + "╯")
		default:
			c := ""
			if j := i - 1 - infoTop; j >= 0 && j < len(info) {
				c = info[j]
			}
			row = border.Render("│") + center(truncate(c, inner), inner) + border.Render("│")
		}
		out[top+i] = left + row
	}
	return out
}

func maxWidth(lines []string) int {
	m := 0
	for _, l := range lines {
		m = max(m, width(l))
	}
	return m
}

// bar is the bottom line: reply input, quick reactions or own-story info.
func (v *Viewer) bar(g *authorGroup, s *core.Story, w int) string {
	st := v.ctx.Styles
	p := st.Theme.Palette
	muted := lipgloss.NewStyle().Foreground(p.FgMuted)
	if v.replying {
		v.input.SetWidth(max(w-width(v.input.Prompt)-1, 1))
		return v.input.View()
	}
	if g.Self {
		to := "My Story"
		if s.Group != "" {
			to = oneLine(v.titles[s.Group])
			if to == "" {
				to = "group"
			}
		}
		parts := []string{"Sent to " + to}
		if s.Replies > 0 {
			parts = append(parts, itoa(s.Replies)+" repl"+map[bool]string{true: "y", false: "ies"}[s.Replies == 1])
		}
		return center(muted.Render(truncate(strings.Join(parts, " · "), w)), w)
	}
	key := st.KeyHintKey
	var b strings.Builder
	for i, e := range format.DefaultReactions() {
		if i > 0 {
			b.WriteString("  ")
		}
		b.WriteString(key.Render(itoa(i+1)) + " " + e)
	}
	full := b.String() + "   " + st.RenderKeyHint("r", "reply")
	if width(full) <= w {
		return center(full, w)
	}
	short := st.RenderKeyHints("1-6", "react", "r", "reply")
	if width(short) <= w {
		return center(short, w)
	}
	return truncate(short, w)
}
