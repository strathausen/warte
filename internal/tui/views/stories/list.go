package stories

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"warte/internal/core"
	"warte/internal/tui/keymap"
	"warte/internal/tui/ui"
)

// KeyContextList is the keymap overlay context of the stories list
// (keymap.Overlay(KeyContextList) = "overlay.stories").
const KeyContextList = "stories"

// entryKind is the kind of a selectable row in the list.
type entryKind int

const (
	entryAuthor entryKind = iota
	entryNew
)

type listEntry struct {
	kind  entryKind
	group int // index into groups for entryAuthor
	line  int // first line in the rendered list
	lines int
}

// List is the stories list overlay: one row per author with an unviewed
// ring, a "My stories" section and an entry to post a new story.
type List struct {
	ctx     ui.Context
	groups  []authorGroup
	titles  map[core.ChatID]string
	loaded  bool
	err     error
	sel     int
	entries []listEntry
	total   int // rendered lines
	viewH   int // height of the last View (mouse hit testing only)
}

// NewList returns the stories list overlay. Push it with ui.Push.
func NewList(ctx ui.Context) *List {
	l := &List{ctx: ctx}
	l.layout()
	return l
}

func (l *List) self() core.ACI { return selfACI(l.ctx) }

func (l *List) now() time.Time {
	if l.ctx.Now != nil {
		return l.ctx.Now()
	}
	return time.Now()
}

// Init implements ui.Overlay.
func (l *List) Init() tea.Cmd { return loadStories(l.ctx.Svc, l.self()) }

// Title implements ui.Overlay.
func (l *List) Title() string {
	icon := l.ctx.Icons.Story
	if icon != "" {
		return icon + " Stories"
	}
	return "Stories"
}

// KeyContext implements ui.Overlay.
func (l *List) KeyContext() string { return KeyContextList }

// Footer implements ui.Footer.
func (l *List) Footer() string {
	return l.ctx.Styles.RenderKeyHints("enter", "view", "c", "new", "esc", "close")
}

// Size implements ui.Overlay.
func (l *List) Size(maxW, maxH int) (int, int) {
	return min(maxW, 60), min(maxH, max(l.total, 8))
}

// selectedAuthor returns the author of the selected row ("" if none).
func (l *List) selectedAuthor() (core.ACI, bool) {
	if l.sel < 0 || l.sel >= len(l.entries) {
		return "", false
	}
	e := l.entries[l.sel]
	if e.kind != entryAuthor {
		return "", false
	}
	return l.groups[e.group].Author, true
}

// layout computes the selectable entries and their line positions.
// Sections: "My stories" (own row + "New story"), "Recent", "Viewed".
func (l *List) layout() {
	l.entries = l.entries[:0]
	line := 0
	header := func() { line++ }
	gap := func() {
		if line > 0 {
			line++
		}
	}
	header() // My stories
	for i := range l.groups {
		if l.groups[i].Self {
			l.entries = append(l.entries, listEntry{kind: entryAuthor, group: i, line: line, lines: 3})
			line += 3
		}
	}
	l.entries = append(l.entries, listEntry{kind: entryNew, line: line, lines: 1})
	line++
	for _, want := range []bool{true, false} {
		first := true
		for i := range l.groups {
			g := &l.groups[i]
			if g.Self || (g.unviewed() > 0) != want {
				continue
			}
			if first {
				gap()
				header()
				first = false
			}
			l.entries = append(l.entries, listEntry{kind: entryAuthor, group: i, line: line, lines: 3})
			line += 3
		}
	}
	if l.loaded && len(l.groups) == 0 || !l.loaded || l.err != nil {
		line += 2 // empty / loading / error notice
	}
	l.total = line
	l.sel = max(0, min(l.sel, len(l.entries)-1))
}

// Update implements ui.Overlay.
func (l *List) Update(msg tea.Msg) (ui.Overlay, tea.Cmd) {
	switch msg := msg.(type) {
	case storiesLoadedMsg:
		prev, hadPrev := l.selectedAuthor()
		first := !l.loaded
		l.loaded, l.err = true, msg.err
		if msg.err == nil {
			l.groups, l.titles = msg.groups, msg.titles
		}
		l.layout()
		switch {
		case hadPrev:
			for i, e := range l.entries {
				if e.kind == entryAuthor && l.groups[e.group].Author == prev {
					l.sel = i
				}
			}
		case first:
			// Start on the first author with something new, else the first row.
			l.sel = 0
			for i, e := range l.entries {
				if e.kind == entryAuthor && l.groups[e.group].unviewed() > 0 {
					l.sel = i
					break
				}
			}
		}
	case ui.EventMsg:
		if _, ok := msg.Event.(core.StoriesChanged); ok {
			return l, loadStories(l.ctx.Svc, l.self())
		}
	case ui.ActionMsg:
		return l.action(msg)
	case tea.KeyPressMsg:
		switch msg.String() {
		case "c", "n", "+":
			return l, ui.Push(NewPost(l.ctx))
		case "R", "ctrl+r":
			return l, loadStories(l.ctx.Svc, l.self())
		}
	case tea.MouseClickMsg:
		m := msg.Mouse()
		line := l.offset(l.lastH()) + m.Y
		for i, e := range l.entries {
			if line >= e.line && line < e.line+e.lines {
				if i == l.sel {
					return l.open()
				}
				l.sel = i
			}
		}
	case tea.MouseWheelMsg:
		switch msg.Mouse().Button {
		case tea.MouseWheelDown:
			l.move(1)
		case tea.MouseWheelUp:
			l.move(-1)
		}
	}
	return l, nil
}

// lastH is the height of the last rendered view, for mouse hit testing.
func (l *List) lastH() int {
	if l.viewH > 0 {
		return l.viewH
	}
	return l.total
}

func (l *List) move(d int) {
	if len(l.entries) == 0 {
		return
	}
	l.sel = max(0, min(len(l.entries)-1, l.sel+d))
}

func (l *List) action(msg ui.ActionMsg) (ui.Overlay, tea.Cmd) {
	n := max(msg.Count, 1)
	switch msg.Action {
	case keymap.ActCursorDown:
		l.move(n)
	case keymap.ActCursorUp:
		l.move(-n)
	case keymap.ActHalfPageDown, keymap.ActPageDown:
		l.move(4 * n)
	case keymap.ActHalfPageUp, keymap.ActPageUp:
		l.move(-4 * n)
	case keymap.ActCursorTop:
		l.sel = 0
	case keymap.ActCursorBottom:
		l.sel = max(len(l.entries)-1, 0)
	case keymap.ActItemSelect, keymap.ActCursorRight, keymap.ActStoryNext:
		return l.open()
	case keymap.ActMessageReply, keymap.ActChatNew:
		return l, ui.Push(NewPost(l.ctx))
	}
	return l, nil
}

// open opens the viewer on the selected author, or the composer.
func (l *List) open() (ui.Overlay, tea.Cmd) {
	if l.sel < 0 || l.sel >= len(l.entries) {
		return l, nil
	}
	e := l.entries[l.sel]
	if e.kind == entryNew {
		return l, ui.Push(NewPost(l.ctx))
	}
	return l, ui.Push(newViewer(l.ctx, l.groups, l.titles, e.group))
}

// offset is the first visible line for a viewport of h lines, chosen so the
// selected entry is visible.
func (l *List) offset(h int) int {
	if l.sel < 0 || l.sel >= len(l.entries) || h <= 0 {
		return 0
	}
	e := l.entries[l.sel]
	end := e.line + e.lines
	if end <= h {
		return 0
	}
	return min(end-h+1, max(l.total-h, 0))
}

// View implements ui.Overlay.
func (l *List) View(w, h int) string {
	if w < 1 || h < 1 {
		return ""
	}
	l.viewH = h
	st := l.ctx.Styles
	p := st.Theme.Palette
	head := lipgloss.NewStyle().Foreground(p.FgMuted).Bold(true)
	muted := lipgloss.NewStyle().Foreground(p.FgMuted)
	accent := lipgloss.NewStyle().Foreground(p.Accent)

	lines := make([]string, 0, l.total)
	section := func(title string, count int) {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		s := head.Render(strings.ToUpper(title))
		if count > 0 {
			s += " " + accent.Render(itoa(count))
		}
		lines = append(lines, s)
	}
	cursor := l.ctx.Icons.Cursor
	if cursor == "" || width(cursor) > 2 {
		cursor = "›"
	}
	selected := func(i int) bool { return i == l.sel }
	ei := 0 // entries are in display order
	section("My stories", 0)
	for i := range l.groups {
		if l.groups[i].Self {
			lines = append(lines, l.authorRow(&l.groups[i], w, selected(ei), cursor)...)
			ei++
		}
	}
	{
		mark := "  "
		label := lipgloss.NewStyle().Foreground(p.Fg).Render("New story")
		if selected(ei) {
			mark = accent.Render(padRight(cursor, 2))
			label = accent.Bold(true).Render("New story")
		}
		row := mark + accent.Render(" ＋  ") + "  " + label
		hint := muted.Render("c")
		if width(row)+2+width(hint) <= w {
			row = padRight(row, w-width(hint)) + hint
		}
		lines = append(lines, row)
		ei++
	}
	recent, viewed := 0, 0
	for i := range l.groups {
		switch g := &l.groups[i]; {
		case g.Self:
		case g.unviewed() > 0:
			recent++
		default:
			viewed++
		}
	}
	for _, want := range []bool{true, false} {
		first := true
		for i := range l.groups {
			g := &l.groups[i]
			if g.Self || (g.unviewed() > 0) != want {
				continue
			}
			if first {
				if want {
					section("Recent", recent)
				} else {
					section("Viewed", 0)
				}
				first = false
			}
			lines = append(lines, l.authorRow(g, w, selected(ei), cursor)...)
			ei++
		}
	}
	switch {
	case l.err != nil:
		lines = append(lines, "", st.ErrorText.Render(truncate("Couldn't load stories: "+oneLine(l.err.Error()), w)))
	case !l.loaded:
		lines = append(lines, "", muted.Render("Loading…"))
	case len(l.groups) == 0:
		lines = append(lines, "", muted.Render(truncate("No stories in the last 24 hours.", w)))
	}
	off := l.offset(h)
	if off > 0 && off < len(lines) {
		lines = lines[off:]
	}
	return clip(lines, w, h)
}

// authorRow renders the three lines of one author: a ringed avatar (accent
// ring while something is unviewed, muted otherwise), name, time, summary.
func (l *List) authorRow(g *authorGroup, w int, sel bool, cursor string) []string {
	st := l.ctx.Styles
	p := st.Theme.Palette
	unv := g.unviewed()
	ringStyle := lipgloss.NewStyle().Foreground(p.FgSubtle)
	if unv > 0 {
		ringStyle = lipgloss.NewStyle().Foreground(p.Accent).Bold(true)
	}
	if st.Theme.Mono {
		ringStyle = lipgloss.NewStyle().Faint(true)
		if unv > 0 {
			ringStyle = lipgloss.NewStyle().Bold(true)
		}
	}
	name := oneLine(g.Name)
	if g.Self {
		name = "My stories"
	}
	if name == "" {
		name = "Unknown"
	}
	avatar := st.RenderAvatar(g.Color, string(g.Author), oneLine(g.Name))
	if width(avatar) != 4 {
		avatar = center(truncate(avatar, 4), 4)
	}
	mark := "  "
	nameStyle := lipgloss.NewStyle().Foreground(p.FgMuted)
	if unv > 0 || g.Self {
		nameStyle = lipgloss.NewStyle().Foreground(p.Fg).Bold(true)
	}
	if sel {
		mark = lipgloss.NewStyle().Foreground(p.Accent).Render(padRight(cursor, 2))
		nameStyle = lipgloss.NewStyle().Foreground(p.Accent).Bold(true)
	}
	when := relTime(g.latest(), l.now())
	textW := max(w-9, 1)
	nameW := max(textW-width(when)-1, 1)
	top := "  " + ringStyle.Render("╭────╮")
	mid := mark + ringStyle.Render("│") + avatar + ringStyle.Render("│") + " " +
		padRight(nameStyle.Render(truncate(name, nameW)), nameW) + " " + st.Timestamp.Render(when)
	bot := "  " + ringStyle.Render("╰────╯") + " " + l.summary(g, textW)
	return []string{top, mid, bot}
}

// summary is the muted second line of an author row.
func (l *List) summary(g *authorGroup, w int) string {
	st := l.ctx.Styles
	p := st.Theme.Palette
	muted := lipgloss.NewStyle().Foreground(p.FgMuted)
	n := len(g.Stories)
	count := itoa(n) + " stor" + map[bool]string{true: "y", false: "ies"}[n == 1]
	var kinds []string
	seen := map[string]bool{}
	for _, s := range g.Stories {
		k := storyKind(&s)
		if !seen[k] {
			seen[k] = true
			kinds = append(kinds, k)
		}
	}
	parts := []string{}
	lead := ""
	switch {
	case g.Self:
		parts = append(parts, count)
		if r := g.replies(); r > 0 {
			parts = append(parts, itoa(r)+" repl"+map[bool]string{true: "y", false: "ies"}[r == 1])
		}
	case g.unviewed() > 0:
		lead = lipgloss.NewStyle().Foreground(p.Accent).Render(itoa(g.unviewed()) + " new")
		if g.unviewed() != n {
			parts = append(parts, "of "+itoa(n))
		}
	default:
		parts = append(parts, count)
	}
	parts = append(parts, strings.Join(kinds, ", "))
	rest := strings.Join(parts, " · ")
	if lead != "" {
		rest = " · " + rest
		if len(parts) > 0 && strings.HasPrefix(parts[0], "of ") {
			rest = " " + strings.Join(parts, " · ")
		}
	}
	return lead + muted.Render(truncate(rest, max(w-width(lead), 1)))
}

// storyKind is a one-word description of a story's content.
func storyKind(s *core.Story) string {
	switch {
	case s.Text != nil:
		return "text"
	case s.Media != nil && strings.HasPrefix(s.Media.ContentType, "video/"):
		return "video"
	case s.Media != nil:
		return "photo"
	}
	return "story"
}
