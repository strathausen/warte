package tui

import (
	"context"
	"image"
	"os/exec"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"warte/internal/config"
	"warte/internal/termimg"
	"warte/internal/tui/ui"
)

// Image pipeline
//
// Protocol: detected once at startup (termimg.DetectFromOS refined by the
// config "images" setting). Kitty uses Unicode placeholders, so thumbnails
// are ordinary cells inside the View string and scroll and clip with it; the
// pixel data is transmitted out of band with tea.Raw, once per (image, size),
// with ids bounded by a termimg.Allocator (evicted images are deleted and
// re-transmitted on demand). iTerm2 and sixel are overlay protocols that
// would need cursor-positioned drawing after every frame; inside the
// scrolling message list warte falls back to half-blocks for them for now.
// Everything else uses half-blocks.
//
// Fetching (Service.Attachment/Avatar/StickerImage), decoding and resizing
// run in tea.Cmds bounded by a small worker semaphore. Until an image is
// ready its blurhash (or a styled box) is shown.

const imageWorkers = 4

type imgState int

const (
	imgIdle imgState = iota
	imgLoading
	imgReady
	imgFailed
)

type sizeKey struct{ cols, rows int }

type imgEntry struct {
	state   imgState
	img     image.Image
	decoded *termimg.Decoded
	err     error
	// rendered rows per size; pending marks sizes being rendered.
	rendered map[sizeKey][]string
	pending  map[sizeKey]bool
	lastUse  time.Time
}

type imageLoadedMsg struct {
	key     string
	size    sizeKey
	decoded *termimg.Decoded
	rows    []string
	raw     string   // kitty transmission (and evictions)
	gen     int      // imageManager.gen when requested
	evicted []string // allocator keys evicted
	err     error
}

type imageManager struct {
	a        *App
	protocol termimg.Protocol
	det      termimg.Detection
	renderer *termimg.Renderer
	alloc    *termimg.Allocator
	sem      chan struct{}

	entries map[string]*imgEntry
	queue   []tea.Cmd
	// placeholders caches blurhash/box renders.
	placeholders map[string][]string

	mu        sync.Mutex
	kittyInit string
	// gen counts hideAll calls; loads requested before the last one are
	// not shown (their kitty ids were reset).
	gen int
}

func newImageManager(a *App, cfg config.Config, opts Options, getenv func(string) string) *imageManager {
	det := termimg.Detect(termimg.Env(getenv))
	conf, err := termimg.Parse(cfg.Images)
	if err != nil {
		conf = termimg.Auto
	}
	p := termimg.Resolve(conf, det, nil)
	if conf == termimg.Auto && p == termimg.Kitty && det.Tmux && !tmuxPassthrough(getenv) {
		// Without "allow-passthrough" tmux drops the image data and the
		// placeholder cells would show as garbage.
		p = termimg.HalfBlocks
	}
	if opts.Images != nil {
		p = *opts.Images
	}
	// Overlay protocols fall back to half-blocks inside the cell layout.
	if p == termimg.ITerm2 || p == termimg.Sixel {
		p = termimg.HalfBlocks
	}
	cell := opts.Cell
	if cell.IsZero() {
		cell = termimg.DefaultCellSize
	}
	r := termimg.NewRenderer(p, det, cell)
	r.Quality = termimg.Fast
	m := &imageManager{
		a:            a,
		protocol:     p,
		det:          det,
		renderer:     r,
		sem:          make(chan struct{}, imageWorkers),
		entries:      map[string]*imgEntry{},
		placeholders: map[string][]string{},
	}
	if p == termimg.Kitty {
		m.alloc = termimg.NewAllocator(r.KittyEncoder(), 0)
	}
	return m
}

var _ ui.Images = (*imageManager)(nil)

func (m *imageManager) Enabled() bool { return m.protocol != termimg.None }

func (m *imageManager) Inline() bool {
	return m.protocol == termimg.Kitty || (m.protocol == termimg.HalfBlocks && !m.mono())
}

// mono reports whether the monochrome theme is active (NO_COLOR): half-block
// images and blurhashes are colors only, so they would draw as a meaningless
// field of "▀"; labelled boxes are shown instead.
func (m *imageManager) mono() bool {
	return m.a != nil && m.a.styles != nil && m.a.styles.Theme.Mono
}

// tmuxPassthrough reports whether the tmux pane we run in lets graphics
// escape sequences through ("set -g allow-passthrough on"). It is a variable
// so tests can replace it.
var tmuxPassthrough = func(getenv func(string) string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	args := []string{"show", "-Apv"}
	if pane := getenv("TMUX_PANE"); pane != "" {
		args = append(args, "-t", pane)
	}
	args = append(args, "allow-passthrough")
	out, err := exec.CommandContext(ctx, "tmux", args...).Output()
	if err != nil {
		return false
	}
	v := strings.TrimSpace(string(out))
	return v == "on" || v == "all"
}

// avatarsAsImages reports whether avatars should be drawn as images (only
// with real pixels; 4x2 half-blocks are too coarse).
func (m *imageManager) avatarsAsImages() bool { return m.protocol == termimg.Kitty }

func (m *imageManager) Size(ref ui.ImageRef, maxCols, maxRows int) (int, int) {
	w, h := ref.Attachment.Width, ref.Attachment.Height
	if w <= 0 || h <= 0 {
		w, h = 4, 3
	}
	maxCols, maxRows = max(maxCols, 1), max(maxRows, 1)
	if m.protocol == termimg.HalfBlocks || m.protocol == termimg.None {
		// Two pixels per cell vertically.
		cols, rows := termimg.FitCells(w, h, maxCols, maxRows, termimg.CellSize{Width: 1, Height: 2}, true)
		return max(cols, 1), max(rows, 1)
	}
	cols, rows := m.renderer.Size(w, h, maxCols, maxRows)
	if cols <= 0 || rows <= 0 {
		cols, rows = termimg.FitCells(w, h, maxCols, maxRows, termimg.DefaultCellSize, true)
	}
	return max(cols, 1), max(rows, 1)
}

// Rows implements ui.Images.
func (m *imageManager) Rows(ref ui.ImageRef, cols, rows int) ([]string, bool) {
	cols, rows = max(cols, 1), max(rows, 1)
	if !m.Inline() {
		return m.placeholder(ref, cols, rows), false
	}
	key := ref.Key()
	e := m.entries[key]
	if e == nil {
		e = &imgEntry{rendered: map[sizeKey][]string{}, pending: map[sizeKey]bool{}}
		m.entries[key] = e
	}
	e.lastUse = time.Now()
	sk := sizeKey{cols, rows}
	if r, ok := e.rendered[sk]; ok {
		return r, true
	}
	if e.state == imgFailed {
		return m.placeholder(ref, cols, rows), false
	}
	if !e.pending[sk] {
		e.pending[sk] = true
		if e.state == imgIdle {
			e.state = imgLoading
		}
		m.queue = append(m.queue, m.loadCmd(ref, key, sk, e.decoded))
	}
	return m.placeholder(ref, cols, rows), false
}

// drain returns the queued fetch commands.
func (m *imageManager) drain() []tea.Cmd {
	q := m.queue
	m.queue = nil
	return q
}

// loadCmd fetches (if needed), decodes and renders an image off the UI
// goroutine.
func (m *imageManager) loadCmd(ref ui.ImageRef, key string, sk sizeKey, have *termimg.Decoded) tea.Cmd {
	svc := m.a.svc
	appCtx := m.a.ctx
	renderer := m.renderer
	alloc := m.alloc
	sem := m.sem
	gen := m.gen
	return func() tea.Msg {
		sem <- struct{}{}
		defer func() { <-sem }()
		msg := imageLoadedMsg{key: key, size: sk, gen: gen}
		dec := have
		if dec == nil {
			ctx, cancel := context.WithTimeout(appCtx, 60*time.Second)
			defer cancel()
			var data []byte
			var err error
			switch ref.Kind {
			case ui.ImageAvatar:
				data, err = svc.Avatar(ctx, ref.AvatarID)
			case ui.ImageSticker:
				data, err = svc.StickerImage(ctx, ref.PackID, ref.StickerID)
			default:
				data, err = svc.Attachment(ctx, ref.Message, ref.Attachment.ID)
			}
			if err != nil {
				msg.err = err
				return msg
			}
			dec, err = termimg.Decode(data)
			if err != nil {
				msg.err = err
				return msg
			}
		}
		msg.decoded = dec
		img := dec.First()
		if alloc != nil {
			al := alloc.Acquire(key + "@" + itoa(sk.cols) + "x" + itoa(sk.rows))
			msg.evicted = al.Evicted
			var r termimg.Rendered
			if dec.Animated() {
				r = renderer.RenderDecoded(al.ID, dec, sk.cols, sk.rows)
			} else {
				r = renderer.Render(al.ID, img, sk.cols, sk.rows)
			}
			msg.raw = al.Evict + r.Pre
			msg.rows = padRows(r.Rows, sk.cols, sk.rows)
			return msg
		}
		hb := termimg.HalfBlockRenderer{Quality: termimg.Fast, Color256: renderer.Color256}
		msg.rows = hb.Render(img, sk.cols, sk.rows)
		return msg
	}
}

// padRows makes rendered rows exactly cols x rows (kitty may return a
// smaller area than the box).
func padRows(rows []string, cols, n int) []string {
	out := make([]string, n)
	for i := 0; i < n; i++ {
		if i < len(rows) {
			out[i] = fit(rows[i], cols)
		} else {
			out[i] = spaces(cols)
		}
	}
	return out
}

func (m *imageManager) loaded(msg imageLoadedMsg) {
	e := m.entries[msg.key]
	if e == nil {
		e = &imgEntry{rendered: map[sizeKey][]string{}, pending: map[sizeKey]bool{}}
		m.entries[msg.key] = e
	}
	delete(e.pending, msg.size)
	if msg.err != nil {
		e.state = imgFailed
		e.err = msg.err
		return
	}
	e.state = imgReady
	e.decoded = msg.decoded
	e.rendered[msg.size] = msg.rows
	m.forgetEvicted(msg.evicted)
	m.evictMemory()
}

// forgetEvicted drops the cached rows of images the allocator evicted, so
// they are transmitted again when next drawn.
func (m *imageManager) forgetEvicted(evicted []string) {
	for _, k := range evicted {
		// k is "key@WxH"
		i := strings.LastIndexByte(k, '@')
		if i < 0 {
			continue
		}
		if ev := m.entries[k[:i]]; ev != nil {
			var c, r int
			if parseSize(k[i+1:], &c, &r) {
				delete(ev.rendered, sizeKey{c, r})
			}
		}
	}
}

func parseSize(s string, c, r *int) bool {
	x := strings.IndexByte(s, 'x')
	if x <= 0 {
		return false
	}
	*c, *r = atoi(s[:x]), atoi(s[x+1:])
	return *c > 0 && *r > 0
}

func atoi(s string) int {
	n := 0
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return 0
		}
		n = n*10 + int(ch-'0')
	}
	return n
}

// evictMemory bounds decoded images kept in memory.
func (m *imageManager) evictMemory() {
	const maxEntries = 300
	if len(m.entries) <= maxEntries {
		return
	}
	var oldestKey string
	var oldest time.Time
	for len(m.entries) > maxEntries {
		oldestKey, oldest = "", time.Time{}
		for k, e := range m.entries {
			if e.state == imgLoading {
				continue
			}
			if oldestKey == "" || e.lastUse.Before(oldest) {
				oldestKey, oldest = k, e.lastUse
			}
		}
		if oldestKey == "" {
			return
		}
		delete(m.entries, oldestKey)
	}
}

// placeholder renders the blurhash (as half-blocks) or a styled box.
func (m *imageManager) placeholder(ref ui.ImageRef, cols, rows int) []string {
	att := ref.Attachment
	key := ref.Key() + "|" + itoa(cols) + "x" + itoa(rows)
	if r, ok := m.placeholders[key]; ok {
		return r
	}
	var out []string
	if att.BlurHash != "" && m.protocol != termimg.None && !m.mono() {
		if img, err := termimg.DecodeBlurHash(att.BlurHash, 24, 24, 1); err == nil {
			hb := termimg.HalfBlockRenderer{Quality: termimg.Fast}
			// Stretch to fill the box: resize to the exact pixel area.
			out = hb.Render(termimg.Resize(img, cols, rows*2, termimg.Fast), cols, rows)
		}
	}
	if out == nil {
		out = m.box(ref, cols, rows)
	}
	if len(m.placeholders) > 2000 {
		m.placeholders = map[string][]string{}
	}
	m.placeholders[key] = out
	return out
}

func (m *imageManager) box(ref ui.ImageRef, cols, rows int) []string {
	st := m.a.styles
	p := st.Theme.Palette
	fill := lipgloss.NewStyle().Background(p.BgAlt).Foreground(p.FgSubtle)
	if st.Theme.Mono {
		fill = lipgloss.NewStyle().Faint(true)
	}
	label := clean(ref.Label)
	if label == "" {
		label = attachmentLabel(m.a.icons, ref.Attachment)
		if ref.Kind == ui.ImageSticker {
			label = m.a.icons.Sticker
		}
		if ref.Kind == ui.ImageAvatar {
			label = ""
		}
	}
	out := make([]string, rows)
	mid := rows / 2
	if st.Theme.Mono {
		// Without colors a filled box is invisible: shade it instead.
		for i := range out {
			if i == mid && cols >= 5 {
				l := fitEllipsisRaw(label, cols-4)
				pad := cols - width(l) - 2
				out[i] = fill.Render(strings.Repeat("░", pad/2) + " " + l + " " + strings.Repeat("░", pad-pad/2))
			} else {
				out[i] = fill.Render(strings.Repeat("░", cols))
			}
		}
		return out
	}
	for i := range out {
		if i == mid && cols >= 3 {
			out[i] = fill.Render(centerText(fitEllipsisRaw(label, cols-2), cols))
		} else {
			out[i] = fill.Render(spaces(cols))
		}
	}
	return out
}

func (m *imageManager) resetPlaceholders() { m.placeholders = map[string][]string{} }

// shutdown returns the sequence that frees all kitty images.
func (m *imageManager) shutdown() string {
	if m.alloc == nil {
		return ""
	}
	return m.alloc.Reset()
}

// hideAll deletes every kitty image from the terminal (screen lock) and
// forgets what was rendered, so images are transmitted again when they are
// next drawn. Decoded images stay in memory.
func (m *imageManager) hideAll() string {
	if m.alloc == nil {
		return ""
	}
	s := m.alloc.Reset()
	m.gen++
	for _, e := range m.entries {
		e.rendered = map[sizeKey][]string{}
	}
	return s
}

// loadedStale takes a kitty image that finished loading after hideAll (it
// was requested before the screen was locked): its decoded data is kept,
// but nothing is transmitted and no rows are cached, so the image is
// rendered and transmitted again when it is next drawn. The allocator was
// reset since the request, so its id and evictions are meaningless now.
func (m *imageManager) loadedStale(msg imageLoadedMsg) {
	e := m.entries[msg.key]
	if e == nil {
		e = &imgEntry{rendered: map[sizeKey][]string{}, pending: map[sizeKey]bool{}}
		m.entries[msg.key] = e
	}
	delete(e.pending, msg.size)
	if msg.err != nil {
		e.state = imgFailed
		e.err = msg.err
		return
	}
	e.state = imgReady
	e.decoded = msg.decoded
	// The load may have acquired its id after the reset: free that id (it
	// was never transmitted), and forget images it evicted, whose ids may
	// be reused now.
	if m.alloc != nil {
		m.alloc.Release(msg.key + "@" + itoa(msg.size.cols) + "x" + itoa(msg.size.rows))
	}
	m.forgetEvicted(msg.evicted)
}

// isStale reports whether msg must not be shown: kitty images requested
// before the last hideAll, or arriving while the screen is locked.
func (m *imageManager) isStale(msg imageLoadedMsg, locked bool) bool {
	return m.alloc != nil && (locked || msg.gen != m.gen)
}
