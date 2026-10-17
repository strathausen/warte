package forms

import (
	"errors"
	"image/color"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	qrcode "github.com/skip2/go-qrcode"

	"warte/internal/tui/theme"
	"warte/internal/tui/ui"
)

// QR is an encoded QR code: Modules[y][x] is true for dark modules. It has
// no quiet zone; Render adds one.
type QR struct {
	Modules [][]bool
}

// ErrQRTooLarge is returned by QR.Render when the code does not fit.
var ErrQRTooLarge = errors.New("qr code does not fit")

// NewQR encodes data (bytes, e.g. a URL or a binary fingerprint) at the
// lowest error-correction level, which gives the smallest symbol; terminal
// rendering is crisp, so extra redundancy only costs space.
func NewQR(data []byte) (*QR, error) {
	return NewQRLevel(data, qrcode.Low)
}

// NewQRLevel encodes data at the given recovery level.
func NewQRLevel(data []byte, level qrcode.RecoveryLevel) (*QR, error) {
	q, err := qrcode.New(string(data), level)
	if err != nil {
		return nil, err
	}
	q.DisableBorder = true
	return &QR{Modules: q.Bitmap()}, nil
}

// Size is the number of modules per side (without quiet zone).
func (q *QR) Size() int { return len(q.Modules) }

// CellSize returns the rendered size in cells for a quiet zone of quiet
// modules: one module per column, two modules (upper/lower half block) per
// row, so modules are roughly square on typical 1:2 terminal cells.
func (q *QR) CellSize(quiet int) (cols, rows int) {
	n := q.Size() + 2*quiet
	return n, (n + 1) / 2
}

// FitQuiet returns the largest quiet zone in {4, 3, 2, 1} for which the code
// fits into maxW x maxH cells, or -1 when it does not fit at all. Scanners
// want 4 modules; 2 works with virtually every phone when the surrounding
// terminal background is uniform.
func (q *QR) FitQuiet(maxW, maxH int) int {
	for quiet := 4; quiet >= 1; quiet-- {
		if w, h := q.CellSize(quiet); w <= maxW && h <= maxH {
			return quiet
		}
	}
	return -1
}

// Render draws the code with a quiet zone of quiet modules into lines of
// exactly CellSize(quiet) cells, using ' ', '▀', '▄' and '█'.
//
// With a colored theme the code is painted explicitly: dark modules in the
// darker and light modules (and the quiet zone) in the lighter of the
// theme's foreground and background, falling back to black on white when the
// theme's contrast is too low. That is correct whatever the terminal's own
// colors are. With the colorless mono theme the glyphs use the terminal's
// default colors, so on dark themes (light text on dark background) the
// bitmap is inverted: the block glyphs then draw the light modules.
func (q *QR) Render(st *theme.Styles, quiet int) []string {
	n := q.Size() + 2*quiet
	at := func(x, y int) bool { // dark module?
		x -= quiet
		y -= quiet
		if x < 0 || y < 0 || x >= q.Size() || y >= q.Size() {
			return false
		}
		return q.Modules[y][x]
	}
	mono := st == nil || st.Theme.Mono
	invert := mono && (st == nil || st.Theme.Dark)
	var style lipgloss.Style
	if !mono {
		dark, light := qrColors(st.Theme.Palette)
		style = lipgloss.NewStyle().Foreground(dark).Background(light)
	}
	lines := make([]string, 0, (n+1)/2)
	var b strings.Builder
	for y := 0; y < n; y += 2 {
		b.Reset()
		for x := 0; x < n; x++ {
			top := at(x, y)
			bot := y+1 < n && at(x, y+1)
			if y+1 >= n {
				// Odd height: the missing lower half is quiet zone (light).
				bot = false
			}
			if invert {
				top = !top
				bot = !bot
				if y+1 >= n {
					bot = false // outside the symbol: terminal background
				}
			}
			b.WriteRune(halfBlock(top, bot))
		}
		line := b.String()
		if !mono {
			line = style.Render(line)
		}
		lines = append(lines, line)
	}
	return lines
}

// halfBlock returns the glyph whose ink covers the upper and/or lower half.
func halfBlock(top, bot bool) rune {
	switch {
	case top && bot:
		return '█'
	case top:
		return '▀'
	case bot:
		return '▄'
	}
	return ' '
}

// qrColors picks the dark (ink) and light (paper) colors for a palette.
func qrColors(p theme.Palette) (dark, light color.Color) {
	a, b := p.Fg, p.Bg
	if theme.Luminance(a) > theme.Luminance(b) {
		a, b = b, a
	}
	if theme.Contrast(a, b) < 7 {
		return lipgloss.Color("#000000"), lipgloss.Color("#ffffff")
	}
	return a, b
}

// ParseQRRender reverses Render for tests and self checks: it turns rendered
// lines (ANSI allowed) back into a module bitmap including the quiet zone,
// where true means "ink". With inverted mono rendering ink marks light
// modules.
func ParseQRRender(lines []string) [][]bool {
	var out [][]bool
	for _, l := range lines {
		rs := []rune(stripANSI(l))
		top := make([]bool, len(rs))
		bot := make([]bool, len(rs))
		for i, r := range rs {
			switch r {
			case '█':
				top[i], bot[i] = true, true
			case '▀':
				top[i] = true
			case '▄':
				bot[i] = true
			}
		}
		out = append(out, top, bot)
	}
	return out
}

// QRView is an overlay showing data as a QR code with a caption, e.g. a
// group invite link.
type QRView struct {
	ctx     ui.Context
	title   string
	data    string
	caption string
	qr      *QR
	err     error
}

// NewQRView returns an overlay showing data as a QR code.
func NewQRView(ctx ui.Context, title, data, caption string) *QRView {
	q, err := NewQR([]byte(data))
	return &QRView{ctx: ctx, title: title, data: data, caption: caption, qr: q, err: err}
}

func (v *QRView) Init() tea.Cmd                        { return nil }
func (v *QRView) Title() string                        { return clean(v.title) }
func (v *QRView) KeyContext() string                   { return ContextSafety }
func (v *QRView) Update(tea.Msg) (ui.Overlay, tea.Cmd) { return v, nil }

func (v *QRView) Size(maxW, maxH int) (int, int) {
	w, h := 40, 6
	if v.qr != nil {
		qw, qh := v.qr.CellSize(2)
		w, h = max(w, qw), qh+3
	}
	return min(w, maxW), min(h, maxH)
}

func (v *QRView) View(w, h int) string {
	st := v.ctx.Styles
	if v.err != nil {
		return clampLines([]string{statusLine(st, v.err.Error(), true, w)}, w, h)
	}
	text := wrapText(clean(v.data), w)
	if v.caption != "" {
		text = append([]string{muted(st, trunc(clean(v.caption), w))}, text...)
	}
	var out []string
	if quiet := v.qr.FitQuiet(w, h-len(text)-1); quiet >= 1 {
		for _, l := range v.qr.Render(st, quiet) {
			out = append(out, center(l, w))
		}
		out = append(out, "")
	} else {
		out = append(out, subtle(st, trunc("Enlarge the window to show the QR code.", w)))
	}
	for _, l := range text {
		out = append(out, center(l, w))
	}
	return clampLines(out, w, h)
}
