package termimg

import (
	"image"
	"slices"
	"strconv"
	"strings"
)

// PaletteMode selects how the sixel encoder chooses its colors.
type PaletteMode int

const (
	// PaletteMedianCut builds an adaptive palette with median cut.
	PaletteMedianCut PaletteMode = iota
	// PaletteFixed uses a fixed 6x6x6 color cube plus 24 grays (240
	// colors). It is fast and stable across frames.
	PaletteFixed
)

// SixelEncoder encodes images as DEC sixel graphics.
type SixelEncoder struct {
	// Colors is the maximum palette size for median cut, 2..256. Zero means
	// 256.
	Colors int
	// Palette selects median cut (default) or the fixed palette.
	Palette PaletteMode
	// Dither enables Floyd–Steinberg error diffusion.
	Dither bool
	// Tmux wraps the sequence in tmux passthrough.
	Tmux bool
}

// alphaThreshold is the alpha below which a pixel is left transparent.
const alphaThreshold = 128

// Encode returns the sixel sequence for img at its native pixel size:
// DCS 0;1;0 q, raster attributes "1;1;W;H, color registers, then six-pixel
// bands with run-length encoding, terminated by ST. Pixels with alpha below
// 50% are left transparent (P2=1). Returns "" for an empty image.
func (e SixelEncoder) Encode(img image.Image) string {
	if img == nil || img.Bounds().Empty() {
		return ""
	}
	src := toRGBA(img)
	w, h := src.Bounds().Dx(), src.Bounds().Dy()

	var pal []rgb
	if e.Palette == PaletteFixed {
		pal = fixedPalette()
	} else {
		n := e.Colors
		if n <= 0 || n > 256 {
			n = 256
		}
		pal = medianCut(src, max(n, 2))
	}
	idx := quantize(src, pal, e.Dither)

	var b strings.Builder
	b.Grow(w * h / 2)
	b.WriteString("\x1bP0;1;0q\"1;1;")
	b.WriteString(strconv.Itoa(w))
	b.WriteByte(';')
	b.WriteString(strconv.Itoa(h))
	for i, c := range pal {
		b.WriteByte('#')
		b.WriteString(strconv.Itoa(i))
		b.WriteString(";2;")
		b.WriteString(strconv.Itoa(pct(c.r)))
		b.WriteByte(';')
		b.WriteString(strconv.Itoa(pct(c.g)))
		b.WriteByte(';')
		b.WriteString(strconv.Itoa(pct(c.b)))
	}

	row := make([]byte, w)
	used := make([]bool, len(pal))
	for band := 0; band < h; band += 6 {
		if band > 0 {
			b.WriteByte('-')
		}
		clear(used)
		bh := min(6, h-band)
		for y := range bh {
			for x := range w {
				if ci := idx[(band+y)*w+x]; ci >= 0 {
					used[ci] = true
				}
			}
		}
		first := true
		for ci := range pal {
			if !used[ci] {
				continue
			}
			for x := range w {
				var bits byte
				for y := range bh {
					if idx[(band+y)*w+x] == int16(ci) {
						bits |= 1 << y
					}
				}
				row[x] = bits
			}
			// Trim trailing empty columns.
			end := w
			for end > 0 && row[end-1] == 0 {
				end--
			}
			if !first {
				b.WriteByte('$')
			}
			first = false
			b.WriteByte('#')
			b.WriteString(strconv.Itoa(ci))
			writeRLE(&b, row[:end])
		}
	}
	b.WriteString("\x1b\\")
	return wrap(b.String(), e.Tmux)
}

// writeRLE writes sixel data with "!<count><char>" runs for repeats of 4 or
// more (shorter runs are cheaper written out).
func writeRLE(b *strings.Builder, row []byte) {
	for i := 0; i < len(row); {
		j := i + 1
		for j < len(row) && row[j] == row[i] {
			j++
		}
		n := j - i
		ch := row[i] + '?'
		if n >= 4 {
			b.WriteByte('!')
			b.WriteString(strconv.Itoa(n))
			b.WriteByte(ch)
		} else {
			for range n {
				b.WriteByte(ch)
			}
		}
		i = j
	}
}

func pct(v uint8) int { return (int(v)*100 + 127) / 255 }

type rgb struct{ r, g, b uint8 }

// fixedPalette returns the 6x6x6 cube followed by 24 grays.
func fixedPalette() []rgb {
	levels := [6]uint8{0, 51, 102, 153, 204, 255}
	pal := make([]rgb, 0, 240)
	for _, r := range levels {
		for _, g := range levels {
			for _, b := range levels {
				pal = append(pal, rgb{r, g, b})
			}
		}
	}
	for i := range 24 {
		v := uint8(8 + i*10)
		pal = append(pal, rgb{v, v, v})
	}
	return pal
}

// straight returns the un-premultiplied color of pixel offset i in p.
func straight(p []uint8, i int) (r, g, b, a uint8) {
	a = p[i+3]
	switch a {
	case 0:
		return 0, 0, 0, 0
	case 255:
		return p[i], p[i+1], p[i+2], 255
	}
	return uint8(min(255, int(p[i])*255/int(a))), uint8(min(255, int(p[i+1])*255/int(a))), uint8(min(255, int(p[i+2])*255/int(a))), a
}

// medianCut builds a palette of at most n colors from the opaque pixels of
// img by repeatedly splitting the box with the widest channel range at its
// median. Large images are sampled.
func medianCut(img *image.RGBA, n int) []rgb {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	step := 1
	for (w*h)/(step*step) > 1<<16 {
		step++
	}
	var px []rgb
	for y := 0; y < h; y += step {
		for x := 0; x < w; x += step {
			r, g, b, a := straight(img.Pix, y*img.Stride+x*4)
			if a >= alphaThreshold {
				px = append(px, rgb{r, g, b})
			}
		}
	}
	if len(px) == 0 {
		return []rgb{{0, 0, 0}}
	}

	type box struct{ px []rgb }
	rangeOf := func(p []rgb) (ch int, span int) {
		lo, hi := [3]uint8{255, 255, 255}, [3]uint8{}
		for _, c := range p {
			v := [3]uint8{c.r, c.g, c.b}
			for k := range 3 {
				lo[k] = min(lo[k], v[k])
				hi[k] = max(hi[k], v[k])
			}
		}
		for k := range 3 {
			if s := int(hi[k]) - int(lo[k]); s > span {
				ch, span = k, s
			}
		}
		return ch, span
	}
	boxes := []box{{px}}
	for len(boxes) < n {
		best, bestSpan, bestCh := -1, 0, 0
		for i, bx := range boxes {
			if len(bx.px) < 2 {
				continue
			}
			ch, span := rangeOf(bx.px)
			if span > bestSpan {
				best, bestSpan, bestCh = i, span, ch
			}
		}
		if best < 0 {
			break
		}
		p := boxes[best].px
		key := func(c rgb) uint8 { return [3]uint8{c.r, c.g, c.b}[bestCh] }
		slices.SortFunc(p, func(a, b rgb) int { return int(key(a)) - int(key(b)) })
		mid := len(p) / 2
		boxes[best] = box{p[:mid]}
		boxes = append(boxes, box{p[mid:]})
	}
	pal := make([]rgb, 0, len(boxes))
	for _, bx := range boxes {
		var r, g, b int
		for _, c := range bx.px {
			r += int(c.r)
			g += int(c.g)
			b += int(c.b)
		}
		n := len(bx.px)
		pal = append(pal, rgb{uint8(r / n), uint8(g / n), uint8(b / n)})
	}
	return pal
}

// nearest finds the closest palette entry by squared RGB distance.
type nearest struct {
	pal   []rgb
	cache []int16 // keyed by 5-bit-per-channel color; -1 = unknown
}

func newNearest(pal []rgb) *nearest {
	c := make([]int16, 1<<15)
	for i := range c {
		c[i] = -1
	}
	return &nearest{pal: pal, cache: c}
}

func (n *nearest) find(r, g, b int) int16 {
	r, g, b = clamp8(r), clamp8(g), clamp8(b)
	k := (r>>3)<<10 | (g>>3)<<5 | b>>3
	if v := n.cache[k]; v >= 0 {
		return v
	}
	best, bestD := 0, 1<<30
	for i, c := range n.pal {
		dr, dg, db := r-int(c.r), g-int(c.g), b-int(c.b)
		if d := dr*dr*3 + dg*dg*4 + db*db*2; d < bestD {
			best, bestD = i, d
		}
	}
	n.cache[k] = int16(best)
	return int16(best)
}

func clamp8(v int) int { return min(max(v, 0), 255) }

// quantize maps every pixel to a palette index, -1 for transparent pixels.
func quantize(img *image.RGBA, pal []rgb, dither bool) []int16 {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	out := make([]int16, w*h)
	nn := newNearest(pal)
	var cur, next [][3]int
	if dither {
		cur, next = make([][3]int, w+2), make([][3]int, w+2)
	}
	for y := range h {
		for x := range w {
			r8, g8, b8, a := straight(img.Pix, y*img.Stride+x*4)
			if a < alphaThreshold {
				out[y*w+x] = -1
				continue
			}
			r, g, b := int(r8), int(g8), int(b8)
			if dither {
				e := cur[x+1]
				r, g, b = clamp8(r+e[0]/16), clamp8(g+e[1]/16), clamp8(b+e[2]/16)
			}
			ci := nn.find(r, g, b)
			out[y*w+x] = ci
			if dither {
				c := pal[ci]
				er, eg, eb := r-int(c.r), g-int(c.g), b-int(c.b)
				add := func(row [][3]int, i, f int) {
					row[i][0] += er * f
					row[i][1] += eg * f
					row[i][2] += eb * f
				}
				add(cur, x+2, 7)
				add(next, x, 3)
				add(next, x+1, 5)
				add(next, x+2, 1)
			}
		}
		if dither {
			cur, next = next, cur
			clear(next)
		}
	}
	return out
}
