package media

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"warte/internal/core"
	"warte/internal/termimg"
	"warte/internal/tui/keymap"
	"warte/internal/tui/ui"
)

// KeyContextViewer is the keymap context of the image viewer and the
// view-once viewer. It is keymap.OverlayImage, so the default "+", "=",
// "-" and "0" zoom bindings apply.
const KeyContextViewer = keymap.OverlayImage

// zoomLevels are the zoom factors relative to "fit".
var zoomLevels = []float64{1, 1.5, 2, 3, 4, 6}

// Scan limits when collecting a chat's media.
const (
	scanPage = 250
	scanMax  = 5000
)

// Animation limits for the half-block GIF player.
const (
	maxAnimFrames = 200
	maxAnimCells  = 1_500_000 // cols*rows*frames
	minFrameDelay = 40 * time.Millisecond
)

// Viewer is the full-screen media viewer overlay: one attachment at a time,
// fitted as large as possible, with zoom/pan, previous/next within the
// chat's media (or a given list), a sender/date/caption footer, open
// externally, copy, save and GIF playback. In view-once mode it shows a
// single item, blocks open/copy/save and marks the message viewed on close.
type Viewer struct {
	ctx      ui.Context
	items    []Item
	idx      int
	viewOnce bool
	// unavailable explains why a view-once message cannot be shown.
	unavailable string
	marked      bool

	scanChat core.ChatID
	scanning bool

	zoom       int
	panX, panY int

	// last content size seen by View (used from Update).
	lastW, lastH int

	anim         *animState
	animGen      int
	paused       bool
	kittyControl map[uint32]bool // kitty images whose animation we stopped
}

type animState struct {
	key        string
	cols, rows int
	frames     [][]string
	delays     []time.Duration
	loops      int // remaining extra loops; <0 = forever
	frame      int
	loading    bool
	failed     bool
	done       bool
	dec        *termimg.Decoded
}

type (
	chatMediaMsg struct {
		v     *Viewer
		items []Item
		err   error
	}
	animFramesMsg struct {
		v          *Viewer
		key        string
		cols, rows int
		dec        *termimg.Decoded
		frames     [][]string
		delays     []time.Duration
		loops      int
		err        error
	}
	animTickMsg struct {
		v   *Viewer
		gen int
	}
)

// NewViewer opens the viewer on one attachment of m. Previous/next walk
// through all photos and videos of m's chat (loaded in the background).
func NewViewer(ctx ui.Context, m core.Message, att core.Attachment) *Viewer {
	v := &Viewer{ctx: ctx, items: []Item{{Message: m, Attachment: att}}, scanChat: m.ID.Chat}
	return v
}

// NewViewerList opens the viewer on items[index]; previous/next stay within
// items (an album, or the gallery's list).
func NewViewerList(ctx ui.Context, items []Item, index int) *Viewer {
	v := &Viewer{ctx: ctx, items: append([]Item(nil), items...)}
	v.idx = clampInt(index, 0, max(len(items)-1, 0))
	return v
}

// NewViewOnce opens view-once media. It is shown once: closing the viewer
// calls Service.MarkViewOnceOpened, and open/copy/save are blocked.
func NewViewOnce(ctx ui.Context, m core.Message) *Viewer {
	v := &Viewer{ctx: ctx, viewOnce: true}
	switch {
	case m.Outgoing:
		v.unavailable = "You sent view-once media. It can't be viewed again."
	case m.ViewOnceOpened:
		v.unavailable = "Already viewed"
	case len(m.Attachments) == 0:
		v.unavailable = "Nothing to show"
	default:
		v.items = []Item{{Message: m, Attachment: m.Attachments[0]}}
	}
	return v
}

// ForSelected builds the right media overlay for a message: the view-once
// viewer, the image viewer (first visual attachment), the audio player, or
// nil when the message has no viewable media.
func ForSelected(ctx ui.Context, m *core.Message) ui.Overlay {
	if m == nil {
		return nil
	}
	if m.ViewOnce {
		return NewViewOnce(ctx, *m)
	}
	for _, a := range m.Attachments {
		if IsVisual(a) {
			return NewViewer(ctx, *m, a)
		}
	}
	for _, a := range m.Attachments {
		if a.IsAudio() {
			return NewPlayerOverlay(ctx, *m, a)
		}
	}
	return nil
}

// Current returns the item on screen (ok=false when there is none).
func (v *Viewer) Current() (Item, bool) {
	if v.idx < 0 || v.idx >= len(v.items) {
		return Item{}, false
	}
	return v.items[v.idx], true
}

// Index and Len report the position within the navigable list.
func (v *Viewer) Index() int { return v.idx }
func (v *Viewer) Len() int   { return len(v.items) }

// Zoom returns the current zoom factor (1 = fit).
func (v *Viewer) Zoom() float64 { return zoomLevels[v.zoom] }

// Init starts loading the chat's media for navigation.
func (v *Viewer) Init() tea.Cmd {
	if v.viewOnce || v.scanChat == "" || v.ctx.Svc == nil {
		return nil
	}
	v.scanning = true
	return scanChatMedia(v.ctx.Svc, v.scanChat, v)
}

// scanChatMedia collects every photo and video of a chat, oldest first.
func scanChatMedia(svc core.Service, chat core.ChatID, v *Viewer) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*opTimeout)
		defer cancel()
		var pages [][]Item
		before := core.MessageID{}
		seen := 0
		for seen < scanMax {
			msgs, err := svc.Messages(ctx, chat, before, scanPage)
			if err != nil {
				return chatMediaMsg{v: v, err: err}
			}
			if len(msgs) == 0 {
				break
			}
			seen += len(msgs)
			var page []Item
			for _, m := range msgs {
				page = append(page, MediaItems(m)...)
			}
			pages = append(pages, page)
			if len(msgs) < scanPage || msgs[0].ID == before {
				break
			}
			before = msgs[0].ID
		}
		var items []Item
		for i := len(pages) - 1; i >= 0; i-- {
			items = append(items, pages[i]...)
		}
		return chatMediaMsg{v: v, items: items}
	}
}

// Title implements ui.Overlay.
func (v *Viewer) Title() string {
	if v.viewOnce {
		return "View once"
	}
	it, ok := v.Current()
	if !ok {
		return "Media"
	}
	t := kindLabel(it.Attachment)
	if n := strings.TrimSpace(it.Attachment.FileName); n != "" {
		t += " · " + clean(n)
	}
	if len(v.items) > 1 {
		t += fmt.Sprintf(" · %d/%d", v.idx+1, len(v.items))
	}
	return t
}

// KeyContext implements ui.Overlay.
func (v *Viewer) KeyContext() string { return KeyContextViewer }

// Size implements ui.Overlay: the viewer takes all the room it gets.
func (v *Viewer) Size(maxW, maxH int) (int, int) { return maxW, maxH }

// Footer implements ui.Footer.
func (v *Viewer) Footer() string {
	st := v.ctx.Styles
	if v.viewOnce {
		return st.RenderKeyHints("esc", "close")
	}
	it, _ := v.Current()
	if v.zoom > 0 {
		return st.RenderKeyHints("+/-", "zoom", "hjkl", "pan", "0", "fit", "esc", "close")
	}
	pairs := []string{"+/-", "zoom"}
	if len(v.items) > 1 {
		pairs = append(pairs, "h/l", "prev/next")
	}
	pairs = append(pairs, "o", "open")
	if it.Attachment.IsImage() {
		pairs = append(pairs, "y", "copy")
	}
	pairs = append(pairs, "s", "save", "esc", "close")
	return st.RenderKeyHints(pairs...)
}

// HandleBack implements ui.BackHandler: a view-once viewer marks the
// message as viewed when it closes.
func (v *Viewer) HandleBack() (bool, tea.Cmd) {
	_, cmd := v.close()
	if cmd == nil {
		// Nothing to clean up: let the App pop us synchronously (a queued
		// CloseOverlay could close the overlay below on a fast double esc).
		return false, nil
	}
	return true, tea.Batch(cmd, ui.CloseOverlay)
}

// Dismiss implements ui.Dismisser: when the App closes the viewer without
// asking (click outside), a view-once message is still marked viewed and
// paused kitty animations are restarted.
func (v *Viewer) Dismiss() tea.Cmd {
	_, cmd := v.close()
	return cmd
}

// markViewed returns the MarkViewOnceOpened command (once).
func (v *Viewer) markViewed() tea.Cmd {
	it, ok := v.Current()
	if !v.viewOnce || v.marked || !ok || v.ctx.Svc == nil {
		return nil
	}
	v.marked = true
	svc := v.ctx.Svc
	id := it.Message.ID
	return func() tea.Msg {
		c, cancel := opCtx()
		defer cancel()
		if err := svc.MarkViewOnceOpened(c, id); err != nil {
			return ui.ToastMsg{Level: ui.ToastError, Text: errText("View once", err)}
		}
		return nil
	}
}

// close returns the result of closing the viewer.
func (v *Viewer) close() (ui.Overlay, tea.Cmd) {
	v.stopAnim()
	return nil, tea.Batch(v.markViewed(), v.releaseKitty())
}

// releaseKitty restarts kitty animations the viewer paused with "a" (the
// same image may be shown animated in the conversation). Under
// reduce_motion they stay stopped.
func (v *Viewer) releaseKitty() tea.Cmd {
	if v.ctx.Config.ReduceMotion {
		return nil
	}
	var cmds []tea.Cmd
	enc := termimg.KittyEncoder{Tmux: os.Getenv("TMUX") != ""}
	for id, stopped := range v.kittyControl {
		if stopped {
			cmds = append(cmds, tea.Raw(enc.AnimationControl(id, termimg.AnimationRun, 0)))
			v.kittyControl[id] = false
		}
	}
	return tea.Batch(cmds...)
}

// Update implements ui.Overlay.
func (v *Viewer) Update(msg tea.Msg) (ui.Overlay, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case chatMediaMsg:
		if msg.v != v {
			return v, nil
		}
		v.scanning = false
		if msg.err == nil {
			v.mergeChatItems(msg.items)
		}
	case animFramesMsg:
		if msg.v != v {
			return v, nil
		}
		return v, v.animLoaded(msg)
	case animTickMsg:
		if msg.v != v || msg.gen != v.animGen {
			return v, nil
		}
		return v, v.animStep()
	case ui.ImageReadyMsg:
		// handled below by refreshAnim
	case ui.EventMsg:
		if o, c, done := v.event(msg.Event); done {
			return o, c
		}
	case ui.ActionMsg:
		o, c, done := v.action(msg)
		if done {
			return o, c
		}
		cmd = c
	case tea.KeyPressMsg:
		o, c, done := v.key(msg.String())
		if done {
			return o, c
		}
		cmd = c
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			v.step(-1)
		case tea.MouseWheelDown:
			v.step(1)
		}
	}
	return v, tea.Batch(cmd, v.refreshAnim())
}

// mergeChatItems replaces the list with the chat's media, keeping the
// current item selected.
func (v *Viewer) mergeChatItems(items []Item) {
	cur, ok := v.Current()
	if !ok {
		v.items = items
		v.idx = 0
		return
	}
	k := cur.key()
	for i, it := range items {
		if it.key() == k {
			v.items = items
			v.idx = i
			return
		}
	}
	// Not in the scanned range (e.g. very old): keep it, ordered by time.
	pos := len(items)
	for i, it := range items {
		if it.Message.ID.Timestamp > cur.Message.ID.Timestamp {
			pos = i
			break
		}
	}
	out := append([]Item(nil), items[:pos]...)
	out = append(out, cur)
	v.items = append(out, items[pos:]...)
	v.idx = pos
}

func (v *Viewer) event(ev core.Event) (ui.Overlay, tea.Cmd, bool) {
	switch e := ev.(type) {
	case core.MessageRemoved:
		cur, ok := v.Current()
		var kept []Item
		for _, it := range v.items {
			if it.Message.ID != e.ID {
				kept = append(kept, it)
			}
		}
		if len(kept) == len(v.items) {
			return nil, nil, false
		}
		if ok && cur.Message.ID == e.ID && v.viewOnce {
			return nil, nil, true
		}
		v.items = kept
		if len(kept) == 0 {
			return nil, toast(ui.ToastInfo, "The message was deleted"), true
		}
		if ok {
			v.idx = 0
			for i, it := range kept {
				if it.Message.ID.Timestamp <= cur.Message.ID.Timestamp {
					v.idx = i
				}
			}
		}
		v.resetView()
	case core.MessageUpdated:
		if e.Message.RemoteDeleted {
			return v.event(core.MessageRemoved{ID: e.Message.ID})
		}
		for i := range v.items {
			if v.items[i].Message.ID != e.Message.ID {
				continue
			}
			v.items[i].Message = e.Message
			for _, a := range e.Message.Attachments {
				if a.ID == v.items[i].Attachment.ID {
					v.items[i].Attachment = a
				}
			}
		}
	}
	return nil, nil, false
}

// action handles a fired keymap binding.
func (v *Viewer) action(msg ui.ActionMsg) (ui.Overlay, tea.Cmd, bool) {
	n := max(msg.Count, 1)
	stepX := max(v.lastW/8, 2) * n
	stepY := max(v.lastH/8, 1) * n
	switch msg.Action {
	case keymap.ActZoomIn:
		v.setZoom(v.zoom + n)
	case keymap.ActZoomOut:
		v.setZoom(v.zoom - n)
	case keymap.ActZoomReset:
		v.setZoom(0)
	case keymap.ActCursorLeft:
		if v.zoom > 0 {
			v.panX -= stepX
		} else {
			v.step(-n)
		}
	case keymap.ActCursorRight:
		if v.zoom > 0 {
			v.panX += stepX
		} else {
			v.step(n)
		}
	case keymap.ActCursorUp:
		v.panY -= stepY
	case keymap.ActCursorDown:
		v.panY += stepY
	case keymap.ActHalfPageUp, keymap.ActPageUp:
		v.panY -= max(v.lastH/2, 1)
	case keymap.ActHalfPageDown, keymap.ActPageDown:
		v.panY += max(v.lastH/2, 1)
	case keymap.ActCursorTop:
		if v.zoom > 0 {
			v.panX, v.panY = 0, 0
		} else {
			v.jump(0)
		}
	case keymap.ActCursorBottom:
		if v.zoom > 0 {
			v.panY = math.MaxInt32
		} else {
			v.jump(len(v.items) - 1)
		}
	case keymap.ActSearchNext, keymap.ActFocusRight, keymap.ActStoryNext:
		v.step(n)
	case keymap.ActSearchPrev, keymap.ActFocusLeft, keymap.ActStoryPrev:
		v.step(-n)
	case keymap.ActItemSelect:
		o, c := v.close()
		return o, c, true
	case keymap.ActMessageOpenAttachment:
		return v.key("o")
	}
	return v, nil, false
}

// key handles keys that are not bound in the keymap.
func (v *Viewer) key(k string) (ui.Overlay, tea.Cmd, bool) {
	it, ok := v.Current()
	switch k {
	case "n", "]":
		v.step(1)
	case "p", "[":
		v.step(-1)
	case "+", "=":
		v.setZoom(v.zoom + 1)
	case "-":
		v.setZoom(v.zoom - 1)
	case "0":
		v.setZoom(0)
	case "o":
		if !ok {
			return v, nil, false
		}
		if v.viewOnce {
			return v, toast(ui.ToastWarning, "View-once media can only be viewed inside warte"), true
		}
		return v, openExternal(v.ctx, it), true
	case "y":
		if !ok {
			return v, nil, false
		}
		return v, copyCmd(v.ctx, it), true
	case "s":
		if !ok {
			return v, nil, false
		}
		return v, SaveCmd(v.ctx, it), true
	case "a", "space":
		v.paused = !v.paused
		return v, v.togglePlayback(), false // refreshAnim handles kitty
	case "m":
		if !ok || v.viewOnce {
			return v, nil, false
		}
		id := it.Message.ID
		return nil, func() tea.Msg { return ui.OpenChatMsg{Chat: id.Chat, Focus: &id} }, true
	case "q", "esc":
		o, c := v.close()
		return o, c, true
	}
	return v, nil, false
}

// step moves by n items (wrapping is not done).
func (v *Viewer) step(n int) {
	if v.viewOnce || len(v.items) == 0 {
		return
	}
	v.jump(v.idx + n)
}

func (v *Viewer) jump(i int) {
	i = clampInt(i, 0, max(len(v.items)-1, 0))
	if i != v.idx {
		v.idx = i
		v.resetView()
	}
}

func (v *Viewer) resetView() {
	v.zoom, v.panX, v.panY = 0, 0, 0
	v.stopAnim()
}

func (v *Viewer) setZoom(z int) {
	z = clampInt(z, 0, len(zoomLevels)-1)
	if z == v.zoom {
		return
	}
	// Keep the viewport centered on the same spot of the image.
	_, _, oc, or_ := v.layout(v.lastW, v.imageHeight(v.lastH))
	cx := float64(v.panX) + float64(min(v.lastW, oc))/2
	cy := float64(v.panY) + float64(min(v.imageHeight(v.lastH), or_))/2
	old := zoomLevels[v.zoom]
	v.zoom = z
	f := zoomLevels[z] / old
	_, _, nc, nr := v.layout(v.lastW, v.imageHeight(v.lastH))
	v.panX = int(cx*f - float64(min(v.lastW, nc))/2)
	v.panY = int(cy*f - float64(min(v.imageHeight(v.lastH), nr))/2)
	if z == 0 {
		v.panX, v.panY = 0, 0
	}
	v.stopAnim()
}

// footerHeight is the number of footer lines for a content height.
func footerHeight(h int) int {
	switch {
	case h >= 10:
		return 2
	case h >= 4:
		return 1
	}
	return 0
}

func (v *Viewer) imageHeight(h int) int { return max(h-footerHeight(h), 1) }

// layout returns the fitted size (fc x fr) and the zoomed size (zc x zr) of
// the current image in an area of w x h cells.
func (v *Viewer) layout(w, h int) (fc, fr, zc, zr int) {
	it, ok := v.Current()
	if !ok || w <= 0 || h <= 0 {
		return 0, 0, 0, 0
	}
	ref := it.Ref()
	if v.ctx.Images == nil {
		fc, fr = fallbackSize(ref, w, h)
		return fc, fr, fc, fr
	}
	fc, fr = v.ctx.Images.Size(ref, w, h)
	fc, fr = clampInt(fc, 1, w), clampInt(fr, 1, h)
	if v.zoom == 0 {
		return fc, fr, fc, fr
	}
	z := zoomLevels[v.zoom]
	maxCells := termimg.MaxPlaceholderCells
	zc, zr = v.ctx.Images.Size(ref, min(int(math.Ceil(float64(fc)*z)), maxCells), min(int(math.Ceil(float64(fr)*z)), maxCells))
	zc, zr = clampInt(zc, 1, maxCells), clampInt(zr, 1, maxCells)
	return fc, fr, zc, zr
}

// imageRows returns the rendered rows of the current image at zc x zr.
func (v *Viewer) imageRows(it Item, zc, zr int) ([]string, bool) {
	a := it.Attachment
	if a.IsVideo() || !a.IsImage() {
		label := kindIcon(v.ctx.Icons, a) + " " + kindLabel(a)
		if !v.viewOnce {
			label = "▶ " + kindLabel(a) + " · o to play"
		}
		if rows := blurRows(a.BlurHash, zc, zr); rows != nil {
			mid := zr / 2
			rows[mid] = overlayLabel(rows[mid], v.ctx.Styles.Title.Render(" "+label+" "), zc)
			return rows, true
		}
		return styledBox(v.ctx.Styles, zc, zr, label), true
	}
	if v.ctx.Images == nil || !v.ctx.Images.Enabled() {
		return styledBox(v.ctx.Styles, zc, zr, kindIcon(v.ctx.Icons, a)+" "+kindLabel(a)+" · o to open"), true
	}
	if an := v.anim; an != nil && an.key == it.key() && an.cols == zc && an.rows == zr && len(an.frames) > 0 {
		return an.frames[an.frame%len(an.frames)], true
	}
	return v.ctx.Images.Rows(it.Ref(), zc, zr)
}

// overlayLabel centers label over a rendered row of w cells.
func overlayLabel(row, label string, w int) string {
	lw := width(label)
	if lw >= w {
		return ansi.Truncate(label, w, "")
	}
	x := (w - lw) / 2
	return ansi.Cut(row, 0, x) + label + ansi.Cut(row, x+lw, w)
}

// View implements ui.Overlay.
func (v *Viewer) View(w, h int) string {
	v.lastW, v.lastH = w, h
	st := v.ctx.Styles
	if w <= 0 || h <= 0 {
		return ""
	}
	fh := footerHeight(h)
	ih := h - fh
	it, ok := v.Current()
	if !ok || v.unavailable != "" {
		text := v.unavailable
		if text == "" {
			text = "No media"
			if v.scanning {
				text = "Loading…"
			}
		}
		lines := make([]string, h)
		lines[h/2] = st.Subtitle.Render(center(ellipsis(text, w), w))
		return strings.Join(fitLines(lines, w, h), "\n")
	}
	var img []string
	ready := true
	if ih > 0 {
		_, _, zc, zr := v.layout(w, ih)
		var rows []string
		rows, ready = v.imageRows(it, zc, zr)
		vw, vh := min(w, zc), min(ih, zr)
		v.panX = clampInt(v.panX, 0, max(zc-vw, 0))
		v.panY = clampInt(v.panY, 0, max(zr-vh, 0))
		padL := (w - vw) / 2
		padT := (ih - vh) / 2
		img = make([]string, 0, ih)
		for i := 0; i < padT; i++ {
			img = append(img, "")
		}
		for y := v.panY; y < v.panY+vh && y < len(rows); y++ {
			row := rows[y]
			if v.panX > 0 || vw < zc {
				row = ansi.Cut(row, v.panX, v.panX+vw)
			}
			img = append(img, strings.Repeat(" ", padL)+row)
		}
	}
	lines := fitLines(img, w, ih)
	lines = append(lines, v.footerLines(it, ready, w, fh)...)
	return strings.Join(lines, "\n")
}

// footerLines renders sender, date, position and caption.
func (v *Viewer) footerLines(it Item, ready bool, w, n int) []string {
	if n == 0 {
		return nil
	}
	st := v.ctx.Styles
	parts := []string{st.SenderStyle(string(it.Message.ID.Author)).Render(senderName(it.Message))}
	if d := dateLine(v.ctx.Config, it.Message.SentAt); d != "" {
		parts = append(parts, st.Timestamp.Render(d))
	}
	if len(v.items) > 1 {
		parts = append(parts, st.Subtitle.Render(fmt.Sprintf("%d of %d", v.idx+1, len(v.items))))
	}
	if v.zoom > 0 {
		parts = append(parts, st.InfoText.Render(fmt.Sprintf("%.0f%%", zoomLevels[v.zoom]*100)))
	}
	if !ready {
		parts = append(parts, st.Subtitle.Render("loading…"))
	} else if v.anim != nil && v.paused {
		parts = append(parts, st.Subtitle.Render("paused"))
	}
	if v.viewOnce {
		parts = append(parts, st.WarningText.Render(v.ctx.Icons.Lock+" view once"))
	}
	info := ellipsis(strings.Join(parts, st.KeyHintSep.Render(" · ")), w)
	caption := strings.TrimSpace(it.Attachment.Caption)
	if caption == "" {
		caption = strings.TrimSpace(it.Message.Body)
	}
	caption = clean(caption)
	if n == 1 {
		return []string{info}
	}
	return []string{info, st.App.Render(ellipsis(caption, w))}
}

// ---- animation -------------------------------------------------------------------

// refreshAnim starts or stops GIF playback for the current item. With kitty
// the terminal animates the image itself; we only stop it under
// reduce_motion or when paused. With half-blocks the frames are decoded and
// rendered here and advanced with ticks.
func (v *Viewer) refreshAnim() tea.Cmd {
	it, ok := v.Current()
	if !ok || v.lastW <= 0 || v.ctx.Images == nil || !isAnimatedType(it.Attachment) || !v.ctx.Images.Inline() {
		return nil
	}
	ih := v.imageHeight(v.lastH)
	_, _, zc, zr := v.layout(v.lastW, ih)
	if zc <= 0 || zr <= 0 {
		return nil
	}
	if an := v.anim; an != nil && an.key == it.key() && an.cols == zc && an.rows == zr {
		return nil // already playing (or loading) at this size
	}
	rows, ready := v.ctx.Images.Rows(it.Ref(), zc, zr)
	if !ready {
		return nil
	}
	if isKittyRows(rows) {
		return v.kittyAnim(rows)
	}
	if v.ctx.Config.ReduceMotion || zc*zr > maxAnimCells/4 {
		return nil
	}
	v.stopAnim()
	v.anim = &animState{key: it.key(), cols: zc, rows: zr, loading: true}
	return v.loadFrames(it, zc, zr, nil)
}

// kittyAnim stops kitty's own animation when motion is reduced or paused.
func (v *Viewer) kittyAnim(rows []string) tea.Cmd {
	if len(rows) == 0 {
		return nil
	}
	id, ok := kittyID(rows[0])
	if !ok {
		return nil
	}
	stop := v.ctx.Config.ReduceMotion || v.paused
	if v.kittyControl == nil {
		v.kittyControl = map[uint32]bool{}
	}
	// kittyControl[id] is true while we hold the animation stopped; an
	// unseen id is running (kitty plays animations on its own).
	if v.kittyControl[id] == stop {
		return nil
	}
	v.kittyControl[id] = stop
	state := termimg.AnimationRun
	if stop {
		state = termimg.AnimationStop
	}
	enc := termimg.KittyEncoder{Tmux: os.Getenv("TMUX") != ""}
	return tea.Raw(enc.AnimationControl(id, state, 0))
}

// togglePlayback pauses or resumes the animation.
func (v *Viewer) togglePlayback() tea.Cmd {
	if an := v.anim; an != nil && len(an.frames) > 1 {
		if v.paused {
			v.animGen++
			return nil
		}
		an.done = false
		return v.scheduleTick()
	}
	return nil
}

// stopAnim drops the half-block animation (sizes or items changed).
func (v *Viewer) stopAnim() {
	if v.anim != nil {
		v.anim = nil
		v.animGen++
	}
}

// loadFrames fetches, decodes and renders all frames off the UI goroutine.
func (v *Viewer) loadFrames(it Item, cols, rows int, have *termimg.Decoded) tea.Cmd {
	svc := v.ctx.Svc
	key := it.key()
	return func() tea.Msg {
		msg := animFramesMsg{v: v, key: key, cols: cols, rows: rows}
		dec := have
		if dec == nil {
			if svc == nil {
				msg.err = fmt.Errorf("no service")
				return msg
			}
			c, cancel := opCtx()
			defer cancel()
			data, err := svc.Attachment(c, it.Message.ID, it.Attachment.ID)
			if err != nil {
				msg.err = err
				return msg
			}
			if dec, err = termimg.Decode(data); err != nil {
				msg.err = err
				return msg
			}
		}
		msg.dec = dec
		if !dec.Animated() {
			return msg
		}
		frames := dec.Frames
		if len(frames) > maxAnimFrames {
			frames = frames[:maxAnimFrames]
		}
		if cols*rows*len(frames) > maxAnimCells {
			return msg
		}
		hb := termimg.HalfBlockRenderer{Quality: termimg.Fast}
		for _, f := range frames {
			msg.frames = append(msg.frames, hb.Render(f.Image, cols, rows))
			msg.delays = append(msg.delays, max(f.Delay, minFrameDelay))
		}
		msg.loops = dec.LoopCount
		return msg
	}
}

func (v *Viewer) animLoaded(msg animFramesMsg) tea.Cmd {
	an := v.anim
	if an == nil || an.key != msg.key || an.cols != msg.cols || an.rows != msg.rows {
		return nil
	}
	an.loading = false
	an.dec = msg.dec
	if msg.err != nil || len(msg.frames) < 2 {
		an.failed = true
		return nil
	}
	an.frames, an.delays = msg.frames, msg.delays
	switch {
	case msg.loops == 0:
		an.loops = -1
	case msg.loops < 0:
		an.loops = 0
	default:
		an.loops = msg.loops
	}
	if v.paused {
		return nil
	}
	return v.scheduleTick()
}

func (v *Viewer) scheduleTick() tea.Cmd {
	an := v.anim
	if an == nil || len(an.frames) < 2 {
		return nil
	}
	v.animGen++
	gen := v.animGen
	d := an.delays[an.frame%len(an.delays)]
	return tea.Tick(d, func(time.Time) tea.Msg { return animTickMsg{v: v, gen: gen} })
}

func (v *Viewer) animStep() tea.Cmd {
	an := v.anim
	if an == nil || v.paused || an.done || len(an.frames) < 2 {
		return nil
	}
	next := an.frame + 1
	if next >= len(an.frames) {
		if an.loops == 0 {
			an.done = true
			return nil
		}
		if an.loops > 0 {
			an.loops--
		}
		next = 0
	}
	an.frame = next
	return v.scheduleTick()
}

func clampInt(v, lo, hi int) int {
	if hi < lo {
		return lo
	}
	return min(max(v, lo), hi)
}

// fallbackSize fits an image into w x h cells without an image pipeline.
func fallbackSize(ref ui.ImageRef, w, h int) (int, int) {
	iw, ih := ref.Attachment.Width, ref.Attachment.Height
	if iw <= 0 || ih <= 0 {
		iw, ih = 4, 3
	}
	c, r := termimg.FitCells(iw, ih, w, h, termimg.CellSize{Width: 1, Height: 2}, true)
	return clampInt(c, 1, w), clampInt(r, 1, h)
}
