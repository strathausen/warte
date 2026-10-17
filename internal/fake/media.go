package fake

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	"image/color/palette"
	"image/draw"
	"image/gif"
	"image/png"
	"math"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// Procedural media. Everything here is a pure function of its parameters and
// memoised process-wide, so creating many Services (as tests do) only pays
// the encoding cost once.

var blobMemo sync.Map // string → *memoEntry

// memoEntry generates its value once; concurrent callers of the same key
// wait for the first instead of rendering the same image again (see
// prewarmDemoMedia).
type memoEntry struct {
	once sync.Once
	v    []byte
}

// memoTrace, when set (tests only), sees every memo key requested.
var memoTrace atomic.Pointer[func(key string)]

func memo(key string, gen func() []byte) []byte {
	if f := memoTrace.Load(); f != nil {
		(*f)(key)
	}
	e, ok := blobMemo.Load(key)
	if !ok {
		e, _ = blobMemo.LoadOrStore(key, new(memoEntry))
	}
	me := e.(*memoEntry)
	me.once.Do(func() { me.v = gen() })
	return me.v
}

func encodePNG(img image.Image) []byte {
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&buf, img); err != nil {
		panic(fmt.Sprintf("fake: png encode: %v", err)) // unreachable for in-memory images
	}
	return buf.Bytes()
}

// hash2 is a small deterministic integer hash used for value noise.
func hash2(x, y int, seed uint32) float64 {
	h := uint32(x)*374761393 + uint32(y)*668265263 + seed*2246822519
	h = (h ^ (h >> 13)) * 1274126177
	h ^= h >> 16
	return float64(h&0xffff) / 0xffff
}

// noise1 is smooth 1D value noise in [0,1].
func noise1(x float64, seed uint32) float64 {
	x0 := math.Floor(x)
	t := x - x0
	t = t * t * (3 - 2*t)
	a := hash2(int(x0), 0, seed)
	b := hash2(int(x0)+1, 0, seed)
	return a + (b-a)*t
}

func lerp(a, b, t float64) float64 { return a + (b-a)*t }

func mix(a, b color.RGBA, t float64) color.RGBA {
	t = math.Max(0, math.Min(1, t))
	return color.RGBA{
		R: uint8(lerp(float64(a.R), float64(b.R), t)),
		G: uint8(lerp(float64(a.G), float64(b.G), t)),
		B: uint8(lerp(float64(a.B), float64(b.B), t)),
		A: uint8(lerp(float64(a.A), float64(b.A), t)),
	}
}

func rgb(hex uint32) color.RGBA {
	return color.RGBA{R: uint8(hex >> 16), G: uint8(hex >> 8), B: uint8(hex), A: 0xff}
}

// Landscape variants.
const (
	sceneSunrise = iota
	sceneNoon
	sceneDusk
	sceneNight
	sceneLake
)

type scene struct {
	skyTop, skyBottom color.RGBA
	sun               color.RGBA
	sunX, sunY, sunR  float64
	ridges            []color.RGBA // far → near
	stars             bool
	lake              bool
}

var scenes = map[int]scene{
	sceneSunrise: {rgb(0x2b2d6e), rgb(0xffb38a), rgb(0xfff1c1), 0.7, 0.62, 0.09,
		[]color.RGBA{rgb(0x8d6e9e), rgb(0x5d4f7a), rgb(0x34304f), rgb(0x1c1a2b)}, false, false},
	sceneNoon: {rgb(0x3d8bfd), rgb(0xcfe8ff), rgb(0xffffe0), 0.25, 0.18, 0.06,
		[]color.RGBA{rgb(0x9fb8c9), rgb(0x5f8f6b), rgb(0x3f6e45), rgb(0x264d2c)}, false, false},
	sceneDusk: {rgb(0x1d1147), rgb(0xff6f91), rgb(0xffc75f), 0.35, 0.68, 0.12,
		[]color.RGBA{rgb(0x845ec2), rgb(0x5b3f8f), rgb(0x3a2763), rgb(0x1f1238)}, false, false},
	sceneNight: {rgb(0x050816), rgb(0x1b2a4a), rgb(0xf4f1de), 0.78, 0.2, 0.05,
		[]color.RGBA{rgb(0x24304d), rgb(0x18223a), rgb(0x0f1628), rgb(0x080c18)}, true, false},
	sceneLake: {rgb(0x6fb1fc), rgb(0xe0f2ff), rgb(0xfffbe6), 0.5, 0.22, 0.07,
		[]color.RGBA{rgb(0xa8c5da), rgb(0x6b9080), rgb(0x3d5a45)}, false, true},
}

// landscapePNG paints layered mountain ridges under a gradient sky, with a
// small watchtower on the nearest ridge.
func landscapePNG(w, h, variant int) []byte {
	return memo(fmt.Sprintf("landscape/%d/%d/%d", w, h, variant), func() []byte {
		sc := scenes[variant]
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		fw, fh := float64(w), float64(h)
		horizon := 1.0
		if sc.lake {
			horizon = 0.68
		}
		ridgeY := make([][]float64, len(sc.ridges))
		for i := range sc.ridges {
			ridgeY[i] = make([]float64, w)
			base := 0.42 + 0.12*float64(i)
			amp := 0.22 - 0.035*float64(i)
			freq := 3.0 + 1.5*float64(i)
			for x := 0; x < w; x++ {
				fx := float64(x) / fw
				n := 0.65*noise1(fx*freq, uint32(i*7+variant)) + 0.35*noise1(fx*freq*3.1, uint32(i*13+variant+1))
				ridgeY[i][x] = (base + amp*(0.5-n)) * horizon
			}
		}
		towerX := int(fw * 0.62)
		for y := 0; y < h; y++ {
			fy := float64(y) / fh
			for x := 0; x < w; x++ {
				fx := float64(x) / fw
				c := mix(sc.skyTop, sc.skyBottom, fy/horizon)
				dx, dy := (fx-sc.sunX)*fw/fh, fy-sc.sunY
				if d := math.Hypot(dx, dy); d < sc.sunR {
					c = sc.sun
				} else if d < sc.sunR*2.2 {
					c = mix(sc.sun, c, (d-sc.sunR)/(sc.sunR*1.2))
				}
				if sc.stars && hash2(x, y, 99) > 0.996 {
					c = rgb(0xffffff)
				}
				for i := range sc.ridges {
					if fy >= ridgeY[i][x] {
						shade := 1 - 0.25*(fy-ridgeY[i][x])
						rc := sc.ridges[i]
						c = color.RGBA{uint8(float64(rc.R) * shade), uint8(float64(rc.G) * shade), uint8(float64(rc.B) * shade), 0xff}
					}
				}
				if sc.lake && fy > horizon {
					// mirrored, rippled reflection
					my := int(2*horizon*fh) - y
					ox := x + int(3*math.Sin(float64(y)*0.9))
					if ox < 0 {
						ox = 0
					}
					if ox >= w {
						ox = w - 1
					}
					if my >= 0 && my < h {
						r := img.RGBAAt(ox, my)
						c = mix(r, rgb(0x2a5470), 0.35+0.4*(fy-horizon))
					}
				}
				img.SetRGBA(x, y, c)
			}
		}
		// Watchtower silhouette on the nearest ridge.
		near := len(sc.ridges) - 1
		top := int(ridgeY[near][towerX] * fh)
		tw, th := max(4, w/40), max(10, h/7)
		dark := rgb(0x111111)
		if variant == sceneNight || variant == sceneDusk {
			dark = rgb(0x05030a)
		}
		fillRect(img, towerX-tw/2, top-th, towerX+tw/2, top+2, dark)
		fillRect(img, towerX-tw, top-th-tw/2, towerX+tw, top-th, dark)
		for i := 0; i <= tw; i++ { // conical roof
			fillRect(img, towerX-tw+i, top-th-tw/2-i, towerX+tw-i, top-th-tw/2-i+1, dark)
		}
		if variant == sceneNight || variant == sceneDusk {
			fillRect(img, towerX-1, top-th+2, towerX+2, top-th+5, rgb(0xffb000)) // lit window
		}
		return encodePNG(img)
	})
}

func fillRect(img *image.RGBA, x0, y0, x1, y1 int, c color.RGBA) {
	draw.Draw(img, image.Rect(x0, y0, x1, y1), &image.Uniform{c}, image.Point{}, draw.Src)
}

// Pattern variants.
const (
	patternStripes = iota
	patternCircles
	patternChecker
	patternWaves
)

// patternPNG produces abstract art: book covers, screenshots, etc.
func patternPNG(w, h, variant int, a, b uint32) []byte {
	return memo(fmt.Sprintf("pattern/%d/%d/%d/%x/%x", w, h, variant, a, b), func() []byte {
		ca, cb := rgb(a), rgb(b)
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		cx, cy := float64(w)/2, float64(h)/2
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				var t float64
				switch variant {
				case patternStripes:
					t = float64(((x+y)/12)%2) * 0.85
				case patternCircles:
					d := math.Hypot(float64(x)-cx, float64(y)-cy)
					t = 0.5 + 0.5*math.Sin(d/6)
				case patternChecker:
					t = float64((x/16+y/16)%2) * 0.9
				default:
					t = 0.5 + 0.5*math.Sin(float64(x)/14+2*math.Sin(float64(y)/22))
				}
				t = t*0.85 + 0.15*float64(y)/float64(h)
				img.SetRGBA(x, y, mix(ca, cb, t))
			}
		}
		return encodePNG(img)
	})
}

// avatarColors are Signal-ish avatar background colours.
var avatarColors = []uint32{0xe3e3fe, 0xdde7fc, 0xd8e8f0, 0xcde4cd, 0xeae0f8, 0xf5e3fe, 0xf6d8ec, 0xf5d7d7, 0xfef5d0, 0xeae6d5, 0xd2d2dc, 0xd7d7d9}

var avatarInk = []uint32{0x3838f5, 0x1251d3, 0x086da0, 0x067906, 0x661aff, 0x9f00f0, 0xb8057c, 0xbe0404, 0x836b01, 0x7d6f40, 0x4f4f6d, 0x5c5c5c}

// avatarColorNames are the Signal colour names matching avatarColors.
var avatarColorNames = []string{"A100", "A110", "A120", "A130", "A140", "A150", "A160", "A170", "A180", "A190", "A200", "A210"}

func colorIndex(key string) int {
	h := fnv.New32a()
	h.Write([]byte(key))
	return int(h.Sum32() % uint32(len(avatarColors)))
}

// initials returns up to two uppercase initials of a name.
func initials(name string) string {
	var out []rune
	for _, f := range strings.Fields(name) {
		for _, r := range f {
			if r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' {
				out = append(out, r)
				break
			}
		}
		if len(out) == 2 {
			break
		}
	}
	if len(out) == 0 {
		return "?"
	}
	return strings.ToUpper(string(out))
}

// avatarInitialsPNG draws a round avatar with initials, Signal style.
func avatarInitialsPNG(name string, size int) []byte {
	return memo(fmt.Sprintf("avatar/%s/%d", name, size), func() []byte {
		idx := colorIndex(name)
		bg, ink := rgb(avatarColors[idx]), rgb(avatarInk[idx])
		img := image.NewRGBA(image.Rect(0, 0, size, size))
		r := float64(size) / 2
		for y := 0; y < size; y++ {
			for x := 0; x < size; x++ {
				d := math.Hypot(float64(x)+0.5-r, float64(y)+0.5-r)
				if d <= r {
					a := math.Min(1, r-d) // anti-aliased edge
					c := bg
					c.A = uint8(255 * a)
					c.R, c.G, c.B = uint8(float64(c.R)*a), uint8(float64(c.G)*a), uint8(float64(c.B)*a)
					img.SetRGBA(x, y, c)
				}
			}
		}
		// Render text small, then scale it up with nearest neighbour.
		text := initials(name)
		face := basicfont.Face7x13
		small := image.NewAlpha(image.Rect(0, 0, 7*len(text)+1, 13))
		d := font.Drawer{Dst: small, Src: image.Opaque, Face: face, Dot: fixed.P(0, 11)}
		d.DrawString(text)
		scale := max(1, size/(len(text)*7+6))
		if s2 := size * 2 / 3 / 13; s2 < scale {
			scale = max(1, s2)
		}
		tw, th := small.Bounds().Dx()*scale, 13*scale
		ox, oy := (size-tw)/2+scale/2, (size-th)/2
		for y := 0; y < th; y++ {
			for x := 0; x < tw; x++ {
				if small.AlphaAt(x/scale, y/scale).A > 0 {
					img.SetRGBA(ox+x, oy+y, ink)
				}
			}
		}
		return encodePNG(img)
	})
}

// avatarGeometricPNG draws a symmetric identicon-like mosaic, used for groups.
func avatarGeometricPNG(key string, size int) []byte {
	return memo(fmt.Sprintf("geo/%s/%d", key, size), func() []byte {
		h := fnv.New64a()
		h.Write([]byte(key))
		bits := h.Sum64()
		idx := colorIndex(key)
		bg, ink := rgb(avatarColors[idx]), rgb(avatarInk[idx])
		accent := rgb(avatarInk[(idx+4)%len(avatarInk)])
		img := image.NewRGBA(image.Rect(0, 0, size, size))
		cell := size / 6
		pad := (size - cell*5) / 2
		fillRect(img, 0, 0, size, size, bg)
		for row := 0; row < 5; row++ {
			for col := 0; col < 3; col++ {
				bit := bits >> uint(row*3+col) & 1
				if bit == 0 {
					continue
				}
				c := ink
				if bits>>uint(32+row*3+col)&1 == 1 {
					c = accent
				}
				for _, cc := range []int{col, 4 - col} {
					fillRect(img, pad+cc*cell, pad+row*cell, pad+(cc+1)*cell, pad+(row+1)*cell, c)
				}
			}
		}
		// round the corners into a circle
		r := float64(size) / 2
		for y := 0; y < size; y++ {
			for x := 0; x < size; x++ {
				if math.Hypot(float64(x)+0.5-r, float64(y)+0.5-r) > r {
					img.SetRGBA(x, y, color.RGBA{})
				}
			}
		}
		return encodePNG(img)
	})
}

// signalFireGIF is an animated watchtower with a flickering beacon fire.
func signalFireGIF() []byte {
	return memo("gif/signalfire", func() []byte {
		const w, h, frames = 120, 90, 12
		pal := palette.Plan9
		anim := &gif.GIF{LoopCount: 0}
		for f := 0; f < frames; f++ {
			img := image.NewRGBA(image.Rect(0, 0, w, h))
			for y := 0; y < h; y++ {
				for x := 0; x < w; x++ {
					c := mix(rgb(0x0b1026), rgb(0x3b2a5a), float64(y)/h)
					if hash2(x, y, 7) > 0.993 && (f+x)%4 != 0 {
						c = rgb(0xffffff)
					}
					if y > 70+int(4*math.Sin(float64(x)/11)) {
						c = rgb(0x0a0a12)
					}
					img.SetRGBA(x, y, c)
				}
			}
			fillRect(img, 56, 40, 64, 74, rgb(0x1a1410)) // tower
			fillRect(img, 50, 36, 70, 40, rgb(0x1a1410)) // platform
			// flame: a wobbling teardrop
			phase := float64(f) / frames * 2 * math.Pi
			for y := 14; y < 36; y++ {
				t := float64(36-y) / 22
				half := (1 - t) * (5 + 2*math.Sin(phase+float64(y)/3))
				cx := 60 + 2*math.Sin(phase*2+t*4)
				for x := int(cx - half); x <= int(cx+half); x++ {
					img.SetRGBA(x, y, mix(rgb(0xffe066), rgb(0xff4500), t+0.15*math.Sin(phase+float64(x))))
				}
			}
			// glow
			glow := 14 + 3*math.Sin(phase)
			for y := 4; y < 50; y++ {
				for x := 36; x < 84; x++ {
					d := math.Hypot(float64(x-60), float64(y-28))
					if d < glow && d > glow-1.5 {
						img.SetRGBA(x, y, mix(img.RGBAAt(x, y), rgb(0xff8c00), 0.35))
					}
				}
			}
			pi := image.NewPaletted(img.Bounds(), pal)
			draw.FloydSteinberg.Draw(pi, img.Bounds(), img, image.Point{})
			anim.Image = append(anim.Image, pi)
			anim.Delay = append(anim.Delay, 8)
		}
		var buf bytes.Buffer
		if err := gif.EncodeAll(&buf, anim); err != nil {
			panic(fmt.Sprintf("fake: gif encode: %v", err))
		}
		return buf.Bytes()
	})
}

// stickerSpec describes one generated critter sticker.
type stickerSpec struct {
	emoji string
	body  uint32
	mood  int // 0 happy, 1 surprised, 2 sleepy, 3 love, 4 wink, 5 sad, 6 cool, 7 excited
}

// stickerPNG draws a round critter on a transparent background.
func stickerPNG(spec stickerSpec, size int) []byte {
	return memo(fmt.Sprintf("sticker/%x/%d/%d", spec.body, spec.mood, size), func() []byte {
		img := image.NewRGBA(image.Rect(0, 0, size, size))
		s := float64(size)
		body := rgb(spec.body)
		dark := rgb(0x222222)
		circle := func(cx, cy, r float64, c color.RGBA) {
			for y := int(cy - r - 1); y <= int(cy+r+1); y++ {
				for x := int(cx - r - 1); x <= int(cx+r+1); x++ {
					if x < 0 || y < 0 || x >= size || y >= size {
						continue
					}
					if math.Hypot(float64(x)+0.5-cx, float64(y)+0.5-cy) <= r {
						img.SetRGBA(x, y, c)
					}
				}
			}
		}
		circle(s*0.5, s*0.55, s*0.42, rgb(0xffffff)) // sticker outline
		circle(s*0.28, s*0.22, s*0.12, body)         // ears
		circle(s*0.72, s*0.22, s*0.12, body)
		circle(s*0.5, s*0.55, s*0.38, body)
		circle(s*0.5, s*0.66, s*0.16, mix(body, rgb(0xffffff), 0.5)) // muzzle
		eyeY, eyeR := s*0.46, s*0.05
		switch spec.mood {
		case 1: // surprised
			circle(s*0.37, eyeY, eyeR*1.5, dark)
			circle(s*0.63, eyeY, eyeR*1.5, dark)
			circle(s*0.5, s*0.7, s*0.05, dark)
		case 2: // sleepy
			fillRect(img, int(s*0.31), int(eyeY), int(s*0.43), int(eyeY)+max(2, size/40), dark)
			fillRect(img, int(s*0.57), int(eyeY), int(s*0.69), int(eyeY)+max(2, size/40), dark)
		case 3: // love
			circle(s*0.35, eyeY, eyeR, rgb(0xe63946))
			circle(s*0.39, eyeY, eyeR, rgb(0xe63946))
			circle(s*0.61, eyeY, eyeR, rgb(0xe63946))
			circle(s*0.65, eyeY, eyeR, rgb(0xe63946))
		case 4: // wink
			circle(s*0.37, eyeY, eyeR, dark)
			fillRect(img, int(s*0.57), int(eyeY), int(s*0.69), int(eyeY)+max(2, size/40), dark)
		case 6: // cool: sunglasses
			fillRect(img, int(s*0.27), int(eyeY-eyeR), int(s*0.73), int(eyeY+eyeR*1.2), dark)
		default:
			circle(s*0.37, eyeY, eyeR, dark)
			circle(s*0.63, eyeY, eyeR, dark)
		}
		switch spec.mood {
		case 5: // sad: tear
			circle(s*0.33, eyeY+s*0.09, eyeR*0.7, rgb(0x4ea8de))
		case 7: // excited: blush
			circle(s*0.28, s*0.6, s*0.05, rgb(0xff8fa3))
			circle(s*0.72, s*0.6, s*0.05, rgb(0xff8fa3))
		}
		if spec.mood != 1 {
			circle(s*0.5, s*0.63, s*0.035, dark) // nose
		}
		return encodePNG(img)
	})
}

// oggCRCTable is the CRC-32 table Ogg uses (polynomial 0x04c11db7,
// MSB-first, no reflection, zero initial value and no final XOR).
var oggCRCTable = func() (t [256]uint32) {
	for i := range t {
		r := uint32(i) << 24
		for range 8 {
			if r&0x80000000 != 0 {
				r = r<<1 ^ 0x04c11db7
			} else {
				r <<= 1
			}
		}
		t[i] = r
	}
	return t
}()

func oggCRC(data []byte) uint32 {
	var crc uint32
	for _, b := range data {
		crc = crc<<8 ^ oggCRCTable[byte(crc>>24)^b]
	}
	return crc
}

// voiceNoteOgg returns Ogg/Opus-shaped placeholder bytes: valid, checksummed
// pages with OpusHead/OpusTags packets followed by deterministic filler
// pages. The filler is not decodable audio, but the container parses and is
// sniffed as audio/ogg.
func voiceNoteOgg(seconds int) []byte {
	return memo(fmt.Sprintf("ogg/%d", seconds), func() []byte {
		var buf bytes.Buffer
		serial := uint32(0x5741_5254) // "WART"
		page := func(seq uint32, flags byte, granule uint64, payload []byte) {
			start := buf.Len()
			buf.WriteString("OggS")
			buf.WriteByte(0)
			buf.WriteByte(flags)
			var hdr [20]byte
			binary.LittleEndian.PutUint64(hdr[0:], granule)
			binary.LittleEndian.PutUint32(hdr[8:], serial)
			binary.LittleEndian.PutUint32(hdr[12:], seq)
			// hdr[16:20] is the CRC, computed below over the page with it zeroed.
			buf.Write(hdr[:])
			defer func() {
				pg := buf.Bytes()[start:]
				binary.LittleEndian.PutUint32(pg[22:26], oggCRC(pg))
			}()
			var segs []byte
			for n := len(payload); ; n -= 255 {
				if n >= 255 {
					segs = append(segs, 255)
					continue
				}
				segs = append(segs, byte(n))
				break
			}
			buf.WriteByte(byte(len(segs)))
			buf.Write(segs)
			buf.Write(payload)
		}
		head := []byte("OpusHead")
		head = append(head, 1, 1) // version, channels
		head = binary.LittleEndian.AppendUint16(head, 312)
		head = binary.LittleEndian.AppendUint32(head, 48000)
		head = append(head, 0, 0, 0)
		page(0, 0x02, 0, head)
		tags := []byte("OpusTags")
		vendor := "warte fake encoder"
		tags = binary.LittleEndian.AppendUint32(tags, uint32(len(vendor)))
		tags = append(tags, vendor...)
		tags = binary.LittleEndian.AppendUint32(tags, 0)
		page(1, 0, 0, tags)
		// ~3 KB per second, as a 24 kbit/s voice note would be.
		pages := seconds * 3
		for i := 0; i < pages; i++ {
			payload := make([]byte, 1000)
			for j := range payload {
				payload[j] = byte(hash2(i, j, 31) * 255)
			}
			flags := byte(0)
			if i == pages-1 {
				flags = 0x04
			}
			page(uint32(i+2), flags, uint64(i+1)*48000/3, payload)
		}
		return buf.Bytes()
	})
}

// pdfDocument returns a minimal, valid single-page PDF showing lines of text.
func pdfDocument(title string, lines []string) []byte {
	return memo("pdf/"+title, func() []byte {
		esc := strings.NewReplacer(`\`, `\\`, "(", `\(`, ")", `\)`)
		var content strings.Builder
		content.WriteString("BT /F1 22 Tf 56 760 Td (" + esc.Replace(title) + ") Tj ET\n")
		y := 720
		for _, l := range lines {
			fmt.Fprintf(&content, "BT /F1 12 Tf 56 %d Td (%s) Tj ET\n", y, esc.Replace(l))
			y -= 18
		}
		objs := []string{
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
			fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", content.Len(), content.String()),
			"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
			fmt.Sprintf("<< /Title (%s) /Producer (warte demo) >>", esc.Replace(title)),
		}
		var buf bytes.Buffer
		buf.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
		offsets := make([]int, len(objs))
		for i, o := range objs {
			offsets[i] = buf.Len()
			fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, o)
		}
		xref := buf.Len()
		fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
		for _, off := range offsets {
			fmt.Fprintf(&buf, "%010d 00000 n \n", off)
		}
		fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R /Info 6 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
		return buf.Bytes()
	})
}

// mp4Placeholder returns an ISO-BMFF shaped placeholder (ftyp + free + mdat)
// that sniffs as video/mp4.
func mp4Placeholder(size int) []byte {
	return memo(fmt.Sprintf("mp4/%d", size), func() []byte {
		var buf bytes.Buffer
		box := func(typ string, payload []byte) {
			binary.Write(&buf, binary.BigEndian, uint32(8+len(payload)))
			buf.WriteString(typ)
			buf.Write(payload)
		}
		box("ftyp", []byte("isom\x00\x00\x02\x00isomiso2avc1mp41"))
		box("free", nil)
		payload := make([]byte, size)
		for i := range payload {
			payload[i] = byte(hash2(i, 3, 17) * 255)
		}
		box("mdat", payload)
		return buf.Bytes()
	})
}
