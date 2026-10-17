package termimg

import (
	"image"
	"math"
	"strconv"
	"strings"
)

// HalfBlockRenderer draws images with the upper half block '▀': each cell
// shows two vertically stacked pixels, the top one as the foreground color
// and the bottom one as the background color.
type HalfBlockRenderer struct {
	// Color256 uses the xterm 256-color palette instead of 24-bit color.
	Color256 bool
	// Quality selects the resampling kernel.
	Quality Quality
}

// Render scales img to fit cols x rows cells (two pixels per cell
// vertically, preserving the aspect ratio assuming cells twice as tall as
// wide) and returns exactly rows strings, each exactly cols cells wide. The
// image is centered horizontally and top-aligned; unused and transparent
// areas are plain spaces with default colors. Every row ends with an SGR
// reset of the colors it set.
func (h HalfBlockRenderer) Render(img image.Image, cols, rows int) []string {
	if cols <= 0 || rows <= 0 {
		return nil
	}
	if img == nil || img.Bounds().Empty() {
		return BlankRows(cols, rows)
	}
	b := img.Bounds()
	w, ph := Fit(b.Dx(), b.Dy(), cols, rows*2, true)
	src := Resize(img, w, ph, h.Quality)
	offX := (cols - w) / 2

	out := make([]string, rows)
	var sb strings.Builder
	for r := range rows {
		sb.Reset()
		curFG, curBG := sgrDefaultFG, sgrDefaultBG
		setFG := func(s string) {
			if s != curFG {
				sb.WriteString(s)
				curFG = s
			}
		}
		setBG := func(s string) {
			if s != curBG {
				sb.WriteString(s)
				curBG = s
			}
		}
		for c := range cols {
			x := c - offX
			top, bot := r*2, r*2+1
			tOK := x >= 0 && x < w && top < ph
			bOK := x >= 0 && x < w && bot < ph
			var tr, tg, tb, ta, br, bg, bb, ba uint8
			if tOK {
				tr, tg, tb, ta = straight(src.Pix, top*src.Stride+x*4)
			}
			if bOK {
				br, bg, bb, ba = straight(src.Pix, bot*src.Stride+x*4)
			}
			tVis, bVis := tOK && ta >= alphaThreshold, bOK && ba >= alphaThreshold
			switch {
			case tVis && bVis:
				setFG(h.sgr(38, tr, tg, tb))
				setBG(h.sgr(48, br, bg, bb))
				sb.WriteString("▀")
			case tVis:
				setBG(sgrDefaultBG)
				setFG(h.sgr(38, tr, tg, tb))
				sb.WriteString("▀")
			case bVis:
				setBG(sgrDefaultBG)
				setFG(h.sgr(38, br, bg, bb))
				sb.WriteString("▄")
			default:
				setBG(sgrDefaultBG)
				sb.WriteByte(' ')
			}
		}
		if curFG != sgrDefaultFG || curBG != sgrDefaultBG {
			sb.WriteString("\x1b[39;49m")
		}
		out[r] = sb.String()
	}
	return out
}

const (
	sgrDefaultFG = "\x1b[39m"
	sgrDefaultBG = "\x1b[49m"
)

// sgr returns a foreground (38) or background (48) color sequence.
func (h HalfBlockRenderer) sgr(kind int, r, g, b uint8) string {
	if h.Color256 {
		return "\x1b[" + strconv.Itoa(kind) + ";5;" + strconv.Itoa(int(To256(r, g, b))) + "m"
	}
	return "\x1b[" + strconv.Itoa(kind) + ";2;" + strconv.Itoa(int(r)) + ";" + strconv.Itoa(int(g)) + ";" + strconv.Itoa(int(b)) + "m"
}

var cubeLevels = [6]int{0, 95, 135, 175, 215, 255}

// To256 maps a 24-bit color to the nearest xterm 256-color index, choosing
// between the 6x6x6 cube (16..231) and the gray ramp (232..255).
func To256(r, g, b uint8) uint8 {
	ci := func(v uint8) int {
		if v < 48 {
			return 0
		}
		if v < 115 {
			return 1
		}
		return (int(v) - 35) / 40
	}
	cr, cg, cb := ci(r), ci(g), ci(b)
	dist := func(r2, g2, b2 int) int {
		dr, dg, db := int(r)-r2, int(g)-g2, int(b)-b2
		return dr*dr + dg*dg + db*db
	}
	cubeD := dist(cubeLevels[cr], cubeLevels[cg], cubeLevels[cb])
	avg := (int(r) + int(g) + int(b)) / 3
	gi := 0
	if avg > 238 {
		gi = 23
	} else if avg >= 8 {
		gi = (avg - 3) / 10
	}
	gv := 8 + gi*10
	if dist(gv, gv, gv) < cubeD {
		return uint8(232 + gi)
	}
	return uint8(16 + 36*cr + 6*cg + cb)
}

// BrailleRenderer draws images as braille patterns: each cell holds a 2x4
// grid of dots, lit where the pixel is brighter (or, with Invert, darker)
// than Threshold. Good for tiny thumbnails.
type BrailleRenderer struct {
	// Threshold is the luminance cut-off in [0,1]. Zero means 0.5.
	Threshold float64
	// Invert lights dark pixels instead of bright ones.
	Invert bool
	// Color colors each cell with the average of its lit pixels (24-bit).
	Color bool
}

// Render returns exactly rows strings of exactly cols cells.
func (br BrailleRenderer) Render(img image.Image, cols, rows int) []string {
	if cols <= 0 || rows <= 0 {
		return nil
	}
	if img == nil || img.Bounds().Empty() {
		return BlankRows(cols, rows)
	}
	th := br.Threshold
	if th <= 0 {
		th = 0.5
	}
	b := img.Bounds()
	// Braille dots are about square on 1:2 cells, so fit pixels directly.
	w, h := Fit(b.Dx(), b.Dy(), cols*2, rows*4, true)
	src := Resize(img, w, h, Fast)
	offX := (cols*2 - w) / 2 / 2 * 2

	out := make([]string, rows)
	var sb strings.Builder
	for r := range rows {
		sb.Reset()
		var cur string
		for c := range cols {
			var bits rune
			var sr, sg, sbl, n int
			for dy := range 4 {
				for dx := range 2 {
					x, y := c*2+dx-offX, r*4+dy
					if x < 0 || x >= w || y >= h {
						continue
					}
					pr, pg, pb, pa := straight(src.Pix, y*src.Stride+x*4)
					if pa < alphaThreshold {
						continue
					}
					l := (0.2126*float64(pr) + 0.7152*float64(pg) + 0.0722*float64(pb)) / 255
					if (l >= th) != br.Invert {
						bits |= brailleBit(dx, dy)
						sr += int(pr)
						sg += int(pg)
						sbl += int(pb)
						n++
					}
				}
			}
			if br.Color && n > 0 {
				s := "\x1b[38;2;" + strconv.Itoa(sr/n) + ";" + strconv.Itoa(sg/n) + ";" + strconv.Itoa(sbl/n) + "m"
				if s != cur {
					sb.WriteString(s)
					cur = s
				}
			}
			sb.WriteRune(0x2800 + bits)
		}
		if cur != "" {
			sb.WriteString("\x1b[39m")
		}
		out[r] = sb.String()
	}
	return out
}

// brailleBit returns the dot bit for column dx (0..1) and row dy (0..3) of a
// braille cell.
func brailleBit(dx, dy int) rune {
	if dy == 3 {
		return rune(0x40 << dx)
	}
	return rune(1 << (dy + 3*dx))
}

// Waveform renders audio samples as a single row of width braille cells,
// bars growing from the bottom (up to four dots). Each cell holds two bars;
// each bar is the peak absolute amplitude of its share of samples,
// normalized to the loudest bar. Silence renders as the lowest dot row so
// the track stays visible. The result is exactly width cells wide.
func Waveform(samples []float32, width int) string {
	rows := WaveformRows(samples, width, 1)
	if len(rows) == 0 {
		return ""
	}
	return rows[0]
}

// WaveformRows renders a waveform height rows tall (4*height dots), bars
// growing from the bottom, and returns height strings of width cells each.
func WaveformRows(samples []float32, width, height int) []string {
	if width <= 0 || height <= 0 {
		return nil
	}
	bars := width * 2
	dots := height * 4
	levels := make([]int, bars)
	peak := make([]float64, bars)
	var top float64
	if len(samples) > 0 {
		for i := range bars {
			lo := i * len(samples) / bars
			hi := max((i+1)*len(samples)/bars, lo+1)
			hi = min(hi, len(samples))
			var p float64
			for _, s := range samples[lo:hi] {
				v := math.Abs(float64(s))
				if math.IsNaN(v) || math.IsInf(v, 0) {
					continue
				}
				p = max(p, v)
			}
			peak[i] = p
			top = max(top, p)
		}
	}
	for i, p := range peak {
		l := 1
		if top > 0 {
			l = max(1, int(math.Round(p/top*float64(dots))))
		}
		levels[i] = l
	}
	out := make([]string, height)
	var sb strings.Builder
	for r := range height {
		sb.Reset()
		// dot rows covered by this text row, counted from the bottom
		base := (height - 1 - r) * 4
		for c := range width {
			var bits rune
			for dx := range 2 {
				lv := levels[c*2+dx]
				for dy := range 4 {
					fromBottom := base + (3 - dy)
					if fromBottom < lv {
						bits |= brailleBit(dx, dy)
					}
				}
			}
			sb.WriteRune(0x2800 + bits)
		}
		out[r] = sb.String()
	}
	return out
}
