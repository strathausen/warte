package termimg

import (
	"image"
	"image/color"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var rasterRe = regexp.MustCompile(`^\x1bP0;1;0q"1;1;(\d+);(\d+)`)

// decodeSixel is a minimal test decoder for the subset the encoder emits:
// raster attributes, RGB color registers, color selection, repeats, CR and
// LF. Undrawn pixels stay transparent.
func decodeSixel(t *testing.T, s string) *image.NRGBA {
	t.Helper()
	m := rasterRe.FindStringSubmatch(s)
	if m == nil {
		t.Fatal("missing raster attributes")
	}
	w, _ := strconv.Atoi(m[1])
	h, _ := strconv.Atoi(m[2])
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	body := strings.TrimSuffix(s[len(m[0]):], "\x1b\\")
	regs := map[int]color.NRGBA{}
	var cur color.NRGBA
	x, band := 0, 0
	num := func(i int) (int, int) {
		j := i
		for j < len(body) && body[j] >= '0' && body[j] <= '9' {
			j++
		}
		n, _ := strconv.Atoi(body[i:j])
		return n, j
	}
	put := func(ch byte, count int) {
		bits := ch - '?'
		for range count {
			for y := range 6 {
				if bits&(1<<y) != 0 {
					py := band*6 + y
					if x >= w || py >= h {
						t.Fatalf("pixel (%d,%d) outside %dx%d", x, py, w, h)
					}
					img.SetNRGBA(x, py, cur)
				}
			}
			x++
		}
	}
	for i := 0; i < len(body); {
		switch c := body[i]; {
		case c == '#':
			n, j := num(i + 1)
			if j < len(body) && body[j] == ';' {
				var p [4]int
				for k := range 4 {
					p[k], j = num(j + 1)
				}
				if p[0] != 2 {
					t.Fatalf("unexpected color space %d", p[0])
				}
				regs[n] = color.NRGBA{uint8(p[1] * 255 / 100), uint8(p[2] * 255 / 100), uint8(p[3] * 255 / 100), 255}
			} else {
				cur = regs[n]
			}
			i = j
		case c == '!':
			n, j := num(i + 1)
			put(body[j], n)
			i = j + 1
		case c == '$':
			x = 0
			i++
		case c == '-':
			x = 0
			band++
			i++
		case c >= '?' && c <= '~':
			put(c, 1)
			i++
		default:
			t.Fatalf("unexpected byte %q at %d", c, i)
		}
	}
	return img
}

func TestSixelEncode(t *testing.T) {
	tests := []struct {
		name string
		enc  SixelEncoder
		img  image.Image
	}{
		{"median cut gradient", SixelEncoder{}, gradient(37, 23)},
		{"dithered", SixelEncoder{Dither: true, Colors: 16}, gradient(20, 13)},
		{"fixed palette", SixelEncoder{Palette: PaletteFixed}, gradient(12, 6)},
		{"fixed dithered", SixelEncoder{Palette: PaletteFixed, Dither: true}, gradient(9, 1)},
		{"solid", SixelEncoder{}, solid(50, 12, color.RGBA{10, 200, 30, 255})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := tt.enc.Encode(tt.img)
			if !strings.HasPrefix(s, "\x1bP") || !strings.HasSuffix(s, "\x1b\\") {
				t.Fatalf("bad framing: %q...", s[:min(20, len(s))])
			}
			m := rasterRe.FindStringSubmatch(s)
			if m == nil {
				t.Fatalf("no raster attributes in %q", s[:min(40, len(s))])
			}
			b := tt.img.Bounds()
			if w, _ := strconv.Atoi(m[1]); w != b.Dx() {
				t.Errorf("raster width %d, want %d", w, b.Dx())
			}
			if h, _ := strconv.Atoi(m[2]); h != b.Dy() {
				t.Errorf("raster height %d, want %d", h, b.Dy())
			}
			// Only sixel data bytes may appear in the body.
			body := strings.TrimSuffix(s[len(m[0]):], "\x1b\\")
			for i := 0; i < len(body); i++ {
				c := body[i]
				if !(c >= '?' && c <= '~') && !(c >= '0' && c <= '9') && !strings.ContainsRune("#;!$-", rune(c)) {
					t.Fatalf("unexpected byte %q at %d", c, i)
				}
			}
			dec := decodeSixel(t, s)
			if dec.Bounds().Dx() != b.Dx() || dec.Bounds().Dy() < b.Dy() {
				t.Fatalf("decoded bounds %v, want %v", dec.Bounds(), b)
			}
			// Colors should be close to the source on average.
			var diff, n int
			for y := range b.Dy() {
				for x := range b.Dx() {
					r1, g1, b1, _ := tt.img.At(x, y).RGBA()
					r2, g2, b2, _ := dec.At(x, y).RGBA()
					diff += absInt(int(r1>>8)-int(r2>>8)) + absInt(int(g1>>8)-int(g2>>8)) + absInt(int(b1>>8)-int(b2>>8))
					n++
				}
			}
			if avg := diff / (3 * n); avg > 30 {
				t.Errorf("average channel error %d too high", avg)
			}
		})
	}
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func TestSixelSolidUsesRLE(t *testing.T) {
	s := SixelEncoder{}.Encode(solid(100, 6, color.RGBA{255, 0, 0, 255}))
	if !strings.Contains(s, "!100~") {
		t.Fatalf("expected run-length encoded full band, got %q", s)
	}
	if strings.Count(s, "#0;2;") != 1 {
		t.Fatalf("expected a single palette entry: %q", s)
	}
}

func TestSixelTransparency(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 4, 6))
	for y := range 6 {
		img.Set(0, y, color.NRGBA{0, 0, 255, 255})
	}
	s := SixelEncoder{}.Encode(img)
	// Only column 0 is drawn; trailing transparent columns are trimmed.
	if !strings.Contains(s, "#0~\x1b\\") {
		t.Fatalf("got %q", s)
	}
}

func TestSixelTmuxAndEmpty(t *testing.T) {
	s := SixelEncoder{Tmux: true}.Encode(solid(2, 2, color.White))
	if !strings.HasPrefix(s, "\x1bPtmux;\x1b\x1bP") {
		t.Fatalf("not wrapped: %q", s)
	}
	if (SixelEncoder{}).Encode(nil) != "" || (SixelEncoder{}).Encode(image.NewRGBA(image.Rectangle{})) != "" {
		t.Fatal("empty input should encode to empty string")
	}
}

func TestWriteRLE(t *testing.T) {
	tests := []struct {
		in   []byte
		want string
	}{
		{[]byte{0, 0, 0}, "???"},
		{[]byte{63, 63, 63, 63}, "!4~"},
		{[]byte{1, 2, 2, 2, 2, 2, 1}, "@!5A@"},
		{nil, ""},
	}
	for _, tt := range tests {
		var b strings.Builder
		writeRLE(&b, tt.in)
		if b.String() != tt.want {
			t.Errorf("writeRLE(%v) = %q, want %q", tt.in, b.String(), tt.want)
		}
	}
}

func TestMedianCutLimit(t *testing.T) {
	pal := medianCut(gradient(64, 64), 8)
	if len(pal) != 8 {
		t.Fatalf("palette size %d, want 8", len(pal))
	}
	if got := medianCut(solid(4, 4, color.Black), 256); len(got) != 1 {
		t.Fatalf("solid image palette size %d, want 1", len(got))
	}
	if len(fixedPalette()) != 240 {
		t.Fatal("fixed palette size")
	}
}
