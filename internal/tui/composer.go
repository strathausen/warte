package tui

import (
	"bytes"
	"image"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/atotto/clipboard"
	"github.com/sahilm/fuzzy"

	"warte/internal/core"
	"warte/internal/format"
	"warte/internal/tui/keymap"
	"warte/internal/tui/ui"
)

// maxComposerLines is how far the composer grows before it scrolls.
const maxComposerLines = 8

// maxAttachmentSize bounds files staged with :attach.
const maxAttachmentSize = 100 << 20

type stagedFile struct {
	path string
	name string
	size int64
	data []byte // pasted images live in memory only; path is "" then
}

type mentionRef struct {
	text string // "@Name" as inserted
	aci  core.ACI
}

type compKind int

const (
	compNone compKind = iota
	compMention
	compEmoji
)

type compItem struct {
	label  string // shown
	insert string // inserted text
	aci    core.ACI
	hint   string
}

type completion struct {
	kind  compKind
	items []compItem
	sel   int
	start int // byte offset of the trigger character in the value
	end   int // byte offset of the cursor
}

type composer struct {
	ta          textarea.Model
	replyTo     *core.Message
	editing     *core.Message
	attachments []stagedFile
	viewOnce    bool // send the staged photo/video as view-once
	mentions    []mentionRef
	comp        completion
	draftChat   core.ChatID
	draftSaved  string

	typingChat core.ChatID
	typingOn   bool
	lastKey    time.Time
	typingSeq  int
}

func newComposer(a *App) *composer {
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.CharLimit = 0
	ta.DynamicHeight = true
	ta.MinHeight = 1
	ta.MaxHeight = maxComposerLines
	ta.MaxContentHeight = 100000
	ta.SetHeight(1)
	ta.KeyMap.InsertNewline.SetEnabled(false)
	ta.SetVirtualCursor(true)
	return &composer{ta: ta}
}

func (c *composer) restyle(a *App) {
	st := a.styles
	p := st.Theme.Palette
	s := textarea.DefaultStyles(st.Theme.Dark)
	for _, ss := range []*textarea.StyleState{&s.Focused, &s.Blurred} {
		ss.Base = lipgloss.NewStyle()
		ss.Text = lipgloss.NewStyle().Foreground(p.Fg)
		ss.CursorLine = lipgloss.NewStyle().Foreground(p.Fg)
		ss.Placeholder = st.Placeholder
		ss.EndOfBuffer = lipgloss.NewStyle()
		ss.Prompt = lipgloss.NewStyle()
	}
	s.Blurred.Text = s.Blurred.Text.Foreground(p.FgMuted)
	s.Blurred.CursorLine = s.Blurred.Text
	s.Cursor.Color = p.Accent
	s.Cursor.Blink = false
	c.ta.SetStyles(s)
}

func (c *composer) reset() {
	c.ta.Reset()
	c.replyTo = nil
	c.editing = nil
	c.attachments = nil
	c.viewOnce = false
	c.mentions = nil
	c.comp = completion{}
}

func (c *composer) empty() bool {
	return strings.TrimSpace(c.ta.Value()) == "" && len(c.attachments) == 0
}

// loadDraft fills the composer with a chat's draft.
func (c *composer) loadDraft(a *App, chat core.Chat) {
	c.reset()
	c.draftChat = chat.ID
	c.draftSaved = chat.Draft
	if chat.Draft != "" {
		// Drafts sync from other devices: untrusted text.
		c.ta.SetValue(sanitizeInput(chat.Draft))
	}
	c.updatePlaceholder(a)
}

func (c *composer) updatePlaceholder(a *App) {
	name := clean(a.conv.chat.Title)
	if a.conv.chat.Kind == core.ChatNoteToSelf {
		name = "Note to Self"
	}
	c.ta.Placeholder = "Message " + name + "…"
	if a.keys.Mode() != keymap.Insert {
		c.ta.Placeholder = "Message " + name + "…  (i to write)"
	}
}

// saveDraft persists the composer text for the current chat if it changed.
func (c *composer) saveDraft(a *App) {
	if c.draftChat == "" || c.editing != nil {
		return
	}
	v := c.ta.Value()
	if v == c.draftSaved {
		return
	}
	c.draftSaved = v
	chat := c.draftChat
	ctx, cancel := a.queryCtx()
	defer cancel()
	_ = a.svc.SaveDraft(ctx, chat, v)
}

// ---- typing indicator ---------------------------------------------------------------

type typingCheckMsg struct{ seq int }

const typingIdle = 5 * time.Second

func (c *composer) noteTyping(a *App) tea.Cmd {
	if !a.conv.open {
		return nil
	}
	c.lastKey = a.now()
	if strings.TrimSpace(c.ta.Value()) == "" {
		return c.stopTyping(a)
	}
	c.typingSeq++
	seq := c.typingSeq
	check := tea.Tick(typingIdle, func(time.Time) tea.Msg { return typingCheckMsg{seq: seq} })
	if c.typingOn && c.typingChat == a.conv.chat.ID {
		return check
	}
	c.typingOn = true
	c.typingChat = a.conv.chat.ID
	chat := c.typingChat
	return tea.Batch(check, a.do("", func(ctx contextT) error { return a.svc.SetTyping(ctx, chat, true) }, ""))
}

func (c *composer) typingCheck(a *App, msg typingCheckMsg) tea.Cmd {
	if msg.seq != c.typingSeq || !c.typingOn {
		return nil
	}
	return c.stopTyping(a)
}

func (c *composer) stopTyping(a *App) tea.Cmd {
	if !c.typingOn {
		return nil
	}
	c.typingOn = false
	chat := c.typingChat
	return a.do("", func(ctx contextT) error { return a.svc.SetTyping(ctx, chat, false) }, "")
}

// ---- input -----------------------------------------------------------------------------

// composerKey forwards an unmatched key press to the textarea.
func (a *App) composerKey(msg tea.KeyPressMsg) tea.Cmd {
	c := a.comp
	before := c.ta.Value()
	var cmd tea.Cmd
	c.ta, cmd = c.ta.Update(msg)
	cmds := []tea.Cmd{cmd}
	if c.ta.Value() != before {
		c.afterEdit(a)
		cmds = append(cmds, c.noteTyping(a))
	}
	return tea.Batch(cmds...)
}

func (c *composer) insert(a *App, s string) tea.Cmd {
	c.ta.InsertString(s)
	c.afterEdit(a)
	return c.noteTyping(a)
}

// afterEdit refreshes completions and drops mentions whose text was edited
// away.
func (c *composer) afterEdit(a *App) {
	v := c.ta.Value()
	kept := c.mentions[:0]
	for _, m := range c.mentions {
		if strings.Contains(v, m.text) {
			kept = append(kept, m)
		}
	}
	c.mentions = kept
	c.updateCompletion(a)
}

// cursorOffset returns the byte offset of the cursor in the value.
func (c *composer) cursorOffset() int {
	lines := strings.Split(c.ta.Value(), "\n")
	row := clamp(c.ta.Line(), 0, len(lines)-1)
	off := 0
	for i := 0; i < row; i++ {
		off += len(lines[i]) + 1
	}
	r := []rune(lines[row])
	col := clamp(c.ta.Column(), 0, len(r))
	return off + len(string(r[:col]))
}

// setValueCursor replaces the value and puts the cursor at byte offset off.
func (c *composer) setValueCursor(v string, off int) {
	c.ta.SetValue(v)
	off = clamp(off, 0, len(v))
	before := v[:off]
	row := strings.Count(before, "\n")
	col := len([]rune(before[strings.LastIndexByte(before, '\n')+1:]))
	c.ta.MoveToBegin()
	for i := 0; i < row; i++ {
		c.ta.CursorDown()
	}
	c.ta.SetCursorColumn(col)
}

func (c *composer) updateCompletion(a *App) {
	v := c.ta.Value()
	off := c.cursorOffset()
	before := v[:off]
	c.comp = completion{}
	// @mention in groups.
	if a.conv.chat.Kind == core.ChatGroup {
		if i := strings.LastIndexByte(before, '@'); i >= 0 {
			q := before[i+1:]
			okStart := i == 0 || strings.ContainsAny(before[i-1:i], " \n\t([")
			if okStart && !strings.ContainsAny(q, " \n\t") && len(q) <= 32 {
				var names []string
				var mems []core.Member
				for _, m := range a.conv.chat.Members {
					if m.ACI == a.self.ACI {
						continue
					}
					names = append(names, clean(m.Name))
					mems = append(mems, m)
				}
				var items []compItem
				if q == "" {
					for i, m := range mems {
						items = append(items, compItem{label: names[i], insert: "@" + names[i], aci: m.ACI})
					}
				} else {
					for _, mt := range fuzzy.Find(q, names) {
						items = append(items, compItem{label: names[mt.Index], insert: "@" + names[mt.Index], aci: mems[mt.Index].ACI})
					}
				}
				if len(items) > 0 {
					c.comp = completion{kind: compMention, items: items, start: i, end: off}
					return
				}
			}
		}
	}
	// :shortcode:
	if q, start, ok := format.ShortcodeQuery(before, 2); ok {
		var items []compItem
		for _, m := range format.SearchShortcodes(q, 8) {
			items = append(items, compItem{label: m.Emoji + " :" + m.Code + ":", insert: m.Emoji, hint: m.Code})
		}
		if len(items) > 0 {
			c.comp = completion{kind: compEmoji, items: items, start: start, end: off}
		}
	}
}

// acceptCompletion inserts the selected completion item.
func (c *composer) acceptCompletion(a *App) tea.Cmd {
	if c.comp.kind == compNone || len(c.comp.items) == 0 {
		return nil
	}
	it := c.comp.items[clamp(c.comp.sel, 0, len(c.comp.items)-1)]
	v := c.ta.Value()
	start, end := clamp(c.comp.start, 0, len(v)), clamp(c.comp.end, 0, len(v))
	ins := it.insert
	if c.comp.kind == compMention {
		c.mentions = append(c.mentions, mentionRef{text: it.insert, aci: it.aci})
		ins += " "
	}
	nv := v[:start] + ins + v[end:]
	c.setValueCursor(nv, start+len(ins))
	c.comp = completion{}
	return c.noteTyping(a)
}

// ---- sending -----------------------------------------------------------------------------

// send sends (or saves an edit of) the composer contents.
func (a *App) send() tea.Cmd {
	c := a.comp
	if !a.conv.open {
		return a.toast(ui.ToastWarning, "No chat open")
	}
	raw := c.ta.Value()
	if strings.TrimSpace(raw) == "" && len(c.attachments) == 0 {
		return nil
	}
	if a.conv.chat.OnlyAdminsSend && !a.conv.chat.SelfIsAdmin {
		return a.toast(ui.ToastWarning, "Only admins can send messages here")
	}
	text := strings.TrimRight(format.ReplaceShortcodes(raw), " \n\t")
	text = strings.TrimLeft(text, "\n")
	// Mentions: find each inserted "@Name" in order.
	var spans []format.MentionSpan
	from := 0
	used := map[int]bool{}
	for _, m := range c.mentions {
		i := strings.Index(text[from:], m.text)
		if i < 0 {
			i = strings.Index(text, m.text)
			if i < 0 || used[i] {
				continue
			}
		} else {
			i += from
		}
		used[i] = true
		spans = append(spans, format.MentionSpan{Start: i, End: i + len(m.text), ACI: m.aci})
		from = i + len(m.text)
	}
	sortMentionSpans(spans)
	body, ranges := format.ParseWithMentions(text, spans)
	chat := a.conv.chat.ID

	cmds := []tea.Cmd{c.stopTyping(a)}
	if c.editing != nil {
		id := c.editing.ID
		cmds = append(cmds, a.do("edit", func(ctx contextT) error { return a.svc.Edit(ctx, id, body, ranges) }, ""))
		// Bring back the draft that was put aside when the edit started.
		draft := c.draftSaved
		c.reset()
		c.ta.SetValue(draft)
		a.keys.SetMode(keymap.Normal)
		a.setFocus(FocusConversation)
		return tea.Batch(cmds...)
	}
	out := core.Outgoing{Body: body, Ranges: ranges}
	if c.viewOnce {
		if len(c.attachments) != 1 || !viewOnceFile(c.attachments[0].name) {
			return a.toast(ui.ToastWarning, "View once needs exactly one photo or video")
		}
		if body != "" {
			return a.toast(ui.ToastWarning, "View-once media can't have a caption")
		}
		out.ViewOnce = true
	}
	if c.replyTo != nil {
		id := c.replyTo.ID
		out.Quote = &id
	}
	files := c.attachments
	c.reset()
	c.saveDraft(a) // clears a previously saved draft
	a.convBottom()
	cmds = append(cmds, func() tea.Msg {
		for _, f := range files {
			var oa core.OutgoingAttachment
			var err error
			if f.data != nil {
				oa = attachmentFromData(f.name, f.data)
			} else {
				oa, err = readAttachment(f.path)
			}
			if err != nil {
				return errMsg{op: "attach " + f.name, err: err}
			}
			out.Attachments = append(out.Attachments, oa)
		}
		ctx, cancel := a.opCtx()
		defer cancel()
		if _, err := a.svc.Send(ctx, chat, out); err != nil {
			return errMsg{op: "send", err: err}
		}
		return nil
	})
	return tea.Batch(cmds...)
}

func sortMentionSpans(s []format.MentionSpan) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j].Start < s[j-1].Start; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// readAttachment loads a staged file for sending.
func readAttachment(path string) (core.OutgoingAttachment, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return core.OutgoingAttachment{}, err
	}
	if fi.Size() > maxAttachmentSize {
		return core.OutgoingAttachment{}, errTooLarge
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return core.OutgoingAttachment{}, err
	}
	return attachmentFromData(filepath.Base(path), data), nil
}

// attachmentFromData builds an outgoing attachment from bytes in memory.
func attachmentFromData(name string, data []byte) core.OutgoingAttachment {
	oa := core.OutgoingAttachment{
		FileName:    name,
		ContentType: http.DetectContentType(data),
		Data:        data,
	}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		oa.Width, oa.Height = cfg.Width, cfg.Height
	}
	return oa
}

type tuiError string

func (e tuiError) Error() string { return string(e) }

const errTooLarge = tuiError("file is larger than 100 MB")

// stageAttachment adds a file to the composer.
func (a *App) stageAttachment(path string) tea.Cmd {
	path = expandHome(strings.TrimSpace(path))
	if path == "" {
		return a.toast(ui.ToastWarning, "usage: :attach <path>")
	}
	fi, err := os.Stat(path)
	if err != nil {
		return a.toast(ui.ToastError, "attach: "+err.Error())
	}
	if fi.IsDir() {
		return a.toast(ui.ToastError, "attach: "+clean(path)+" is a directory")
	}
	if fi.Size() > maxAttachmentSize {
		return a.toast(ui.ToastError, "attach: "+errTooLarge.Error())
	}
	if !a.conv.open {
		return a.toast(ui.ToastWarning, "Open a chat first")
	}
	a.comp.attachments = append(a.comp.attachments, stagedFile{path: path, name: filepath.Base(path), size: fi.Size()})
	a.focusComposer()
	return nil
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

// ---- reply / edit --------------------------------------------------------------------------

func (a *App) startReply(id core.MessageID) tea.Cmd {
	var cmd tea.Cmd
	if !a.conv.open || a.conv.chat.ID != id.Chat {
		cmd = a.openChat(id.Chat, &id, true)
	}
	i, ok := a.conv.index[id]
	if !ok {
		return cmd
	}
	m := a.conv.msgs[i].m
	if m.Kind != core.KindNormal || m.RemoteDeleted {
		return a.toast(ui.ToastWarning, "Can't reply to this message")
	}
	a.comp.editing = nil
	a.comp.replyTo = &m
	a.focusComposer()
	return cmd
}

// editWindow is how long Signal allows editing a sent message.
const editWindow = 24 * time.Hour

func (a *App) startEdit(m *core.Message) tea.Cmd {
	switch {
	case !m.Outgoing:
		return a.toast(ui.ToastWarning, "You can only edit your own messages")
	case m.RemoteDeleted || m.Kind != core.KindNormal:
		return a.toast(ui.ToastWarning, "This message can't be edited")
	case a.now().Sub(m.SentAt) > editWindow:
		return a.toast(ui.ToastWarning, "Messages can only be edited within 24 hours")
	case strings.TrimSpace(m.Body) == "":
		return a.toast(ui.ToastWarning, "Only text can be edited")
	}
	c := a.comp
	c.saveDraft(a)
	c.reset()
	mm := *m
	c.editing = &mm
	markup, spans := format.ToMarkupWithMentions(m.Body, m.Ranges, a.mentionName)
	c.ta.SetValue(markup)
	for _, s := range spans {
		c.mentions = append(c.mentions, mentionRef{text: markup[s.Start:s.End], aci: s.ACI})
	}
	a.focusComposer()
	return nil
}

func (a *App) composeInsert(text string) tea.Cmd {
	if !a.conv.open {
		return nil
	}
	a.focusComposer()
	return a.comp.insert(a, sanitizeInput(text))
}

// sanitizeInput strips control characters (except newline and tab) from
// pasted or inserted text.
func sanitizeInput(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
			return -1
		case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
			return -1
		}
		return r
	}, s)
}

func (a *App) handlePaste(s string) tea.Cmd {
	switch a.keys.Mode() {
	case keymap.Insert:
		return a.comp.insert(a, sanitizeInput(s))
	case keymap.Command:
		a.cmdline.input.SetValue(a.cmdline.input.Value() + clean(s))
		a.cmdline.input.CursorEnd()
	case keymap.Search:
		a.search.input.SetValue(a.search.input.Value() + clean(s))
		a.search.input.CursorEnd()
		return a.search.live(a)
	}
	return nil
}

type clipboardPasteMsg struct{ text string }

func clipboardTextPaste() tea.Msg {
	s, err := clipboard.ReadAll()
	if err != nil {
		return errMsg{op: "paste", err: err}
	}
	return clipboardPasteMsg{text: s}
}

// ---- $EDITOR --------------------------------------------------------------------------------

type editorDoneMsg struct {
	text string
	err  error
}

func (a *App) openEditor() tea.Cmd {
	// The draft is plaintext in a 0600 temp file while the editor runs
	// (editors need a normal path). It is removed when the editor exits,
	// on quit/Close (tempFiles) and, after a crash, by the next start
	// (sweepStaleTemp).
	f, err := os.CreateTemp(a.tempDir(), tempPattern(tempEditPrefix, ".md"))
	if err != nil {
		return a.toast(ui.ToastError, "editor: "+err.Error())
	}
	path := f.Name()
	a.trackTemp(path)
	_ = f.Chmod(0o600)
	_, werr := f.WriteString(a.comp.ta.Value())
	cerr := f.Close()
	if werr != nil || cerr != nil {
		a.removeTemp(path)
		return a.toast(ui.ToastError, "editor: cannot write temp file")
	}
	editor := strings.Fields(a.cfg.Editor)
	if len(editor) == 0 {
		editor = []string{"vi"}
	}
	args := append(editor[1:], path)
	cmd := exec.Command(editor[0], args...) //nolint:gosec // the user's configured editor
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		defer a.removeTemp(path)
		if err != nil {
			return editorDoneMsg{err: err}
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return editorDoneMsg{err: rerr}
		}
		return editorDoneMsg{text: string(b)}
	})
}

func (c *composer) editorDone(a *App, msg editorDoneMsg) tea.Cmd {
	if msg.err != nil {
		return a.toast(ui.ToastError, "editor: "+msg.err.Error())
	}
	text := strings.TrimRight(sanitizeInput(msg.text), "\n")
	c.ta.SetValue(text)
	c.afterEdit(a)
	a.focusComposer()
	return c.noteTyping(a)
}

// ---- rendering -------------------------------------------------------------------------------

// renderComposer renders the composer area at width w.
func (a *App) renderComposer(w int) []string {
	st := a.styles
	p := st.Theme.Palette
	c := a.comp
	insert := a.keys.Mode() == keymap.Insert
	var out []string
	out = append(out, hrule(st, w))

	if a.conv.chat.MessageRequest {
		out = append(out, fit(st.WarningText.Render(" Message request")+
			st.Subtitle.Render(" — ")+st.RenderKeyHints(":accept", "accept", ":block", "block", ":delete", "delete"), w))
	}
	if c.replyTo != nil || c.editing != nil {
		var s string
		if c.editing != nil {
			s = lipgloss.NewStyle().Foreground(p.Warning).Bold(true).Render(" "+a.icons.Edit+" Editing message") +
				st.Subtitle.Render(" · enter save · esc cancel")
		} else {
			m := c.replyTo
			name := clean(m.SenderName)
			if m.Outgoing {
				name = "yourself"
			}
			snip := format.Snippet(m.Body, m.Ranges, a.mentionName, max(w-20-width(name), 8))
			if snip == "" && len(m.Attachments) > 0 {
				snip = attachmentLabel(a.icons, m.Attachments[0])
			}
			s = lipgloss.NewStyle().Foreground(p.Accent).Render(" "+a.icons.Reply+" Replying to ") +
				st.SenderStyle(string(m.ID.Author)).Render(name) + st.Subtitle.Render(": "+clean(snip))
		}
		hint := st.KeyHintSep.Render(" ✕ ctrl+r ")
		if c.editing != nil {
			hint = ""
		}
		out = append(out, rightAlign(s, hint, w))
	}
	if len(c.attachments) > 0 {
		var chips []string
		chip := lipgloss.NewStyle().Foreground(p.Fg).Background(p.BgSelected).Padding(0, 1)
		if st.Theme.Mono {
			chip = lipgloss.NewStyle().Reverse(true).Padding(0, 1)
		}
		for _, f := range c.attachments {
			chips = append(chips, chip.Render(a.icons.Attachment+" "+fitEllipsisRaw(clean(f.name), 24)+" · "+humanSize(f.size)))
		}
		if c.viewOnce {
			chips = append(chips, chip.Render("① view once"))
		}
		out = append(out, fit(" "+strings.Join(chips, " "), w))
	}

	promptStyle := lipgloss.NewStyle().Foreground(p.FgSubtle)
	if insert {
		promptStyle = lipgloss.NewStyle().Foreground(p.Accent)
	}
	prompt := promptStyle.Render(" " + a.icons.Cursor + " ")
	pw := width(prompt)
	c.ta.SetWidth(max(w-pw-1, 4))
	if insert {
		c.ta.Focus()
	} else {
		c.ta.Blur()
	}
	c.updatePlaceholder(a)
	view := splitLines(c.ta.View())
	for i, l := range view {
		pre := spaces(pw)
		if i == 0 {
			pre = prompt
		}
		out = append(out, fit(pre+l, w))
	}
	return out
}

// renderCompletion renders the @mention / :emoji: popup (or nil).
func (a *App) renderCompletion(maxW int) []string {
	c := a.comp.comp
	if c.kind == compNone || len(c.items) == 0 || a.keys.Mode() != keymap.Insert {
		return nil
	}
	st := a.styles
	p := st.Theme.Palette
	n := min(len(c.items), 6)
	start := clamp(c.sel-n+1, 0, max(len(c.items)-n, 0))
	w := 0
	for _, it := range c.items {
		w = max(w, width(it.label))
	}
	w = min(w+4, maxW-2)
	var lines []string
	for i := start; i < start+n; i++ {
		it := c.items[i]
		s := " " + it.label
		if i == c.sel {
			sel := lipgloss.NewStyle().Foreground(p.Fg).Background(p.BgSelected).Bold(true)
			if st.Theme.Mono {
				sel = lipgloss.NewStyle().Reverse(true)
			}
			lines = append(lines, sel.Render(fit(s, w)))
		} else {
			lines = append(lines, fit(s, w))
		}
	}
	title := "Mention"
	if c.kind == compEmoji {
		title = "Emoji"
	}
	return box(st, lines, w+2, n+2, true, st.Subtitle.Render(title), st.KeyHintSep.Render("tab"))
}

// viewOnceFile reports whether a file can be sent as view-once media
// (Signal allows photos and videos only).
func viewOnceFile(name string) bool {
	t := mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
	return strings.HasPrefix(t, "image/") || strings.HasPrefix(t, "video/")
}

// toggleViewOnce turns view-once on or off for the staged attachment.
func (a *App) toggleViewOnce() tea.Cmd {
	c := a.comp
	if !c.viewOnce && (len(c.attachments) != 1 || !viewOnceFile(c.attachments[0].name)) {
		return a.toast(ui.ToastWarning, "Attach exactly one photo or video first (:attach <path>)")
	}
	c.viewOnce = !c.viewOnce
	if c.viewOnce {
		return a.toast(ui.ToastInfo, "Will be sent as view-once media")
	}
	return a.toast(ui.ToastInfo, "View once off")
}
