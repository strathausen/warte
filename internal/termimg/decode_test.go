package termimg

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/color/palette"
	"image/gif"
	"image/jpeg"
	"os"
	"testing"
	"time"
)

func TestDecodeFormats(t *testing.T) {
	var jpg bytes.Buffer
	if err := jpeg.Encode(&jpg, gradient(30, 20), nil); err != nil {
		t.Fatal(err)
	}
	webp, err := os.ReadFile("testdata/gopher.webp")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		data   []byte
		format string
		w, h   int
	}{
		{"png", pngBytes(t, gradient(17, 9)), "png", 17, 9},
		{"jpeg", jpg.Bytes(), "jpeg", 30, 20},
		{"webp", webp, "webp", 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := Decode(tt.data)
			if err != nil {
				t.Fatal(err)
			}
			if d.Format != tt.format || d.Animated() || len(d.Frames) != 1 {
				t.Fatalf("got %s with %d frames", d.Format, len(d.Frames))
			}
			b := d.Bounds()
			if tt.w > 0 && (b.Dx() != tt.w || b.Dy() != tt.h) {
				t.Fatalf("bounds %v", b)
			}
			if b.Empty() {
				t.Fatal("empty image")
			}
			img, err := DecodeImage(tt.data)
			if err != nil || img.Bounds() != b {
				t.Fatalf("DecodeImage: %v", err)
			}
		})
	}
}

func TestDecodeErrors(t *testing.T) {
	if _, err := Decode([]byte("not an image")); err == nil {
		t.Fatal("expected error")
	}
	if _, err := DecodeImage(nil); err == nil {
		t.Fatal("expected error")
	}
	// A PNG header claiming a huge size must be rejected before decoding.
	huge := pngBytes(t, solid(1, 1, color.White))
	// IHDR width/height live at bytes 16..23.
	copy(huge[16:24], []byte{0, 0x01, 0, 0, 0, 0x01, 0, 0})
	binary.BigEndian.PutUint32(huge[29:33], crc32.ChecksumIEEE(huge[12:29]))
	if _, err := Decode(huge); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

func TestDecodeGIFFrames(t *testing.T) {
	pal := palette.Plan9
	red := uint8(color.Palette(pal).Index(color.RGBA{255, 0, 0, 255}))
	blue := uint8(color.Palette(pal).Index(color.RGBA{0, 0, 255, 255}))

	full := image.NewPaletted(image.Rect(0, 0, 4, 4), pal)
	for i := range full.Pix {
		full.Pix[i] = red
	}
	// A 2x2 sub-frame at (2,2), disposed to background afterwards.
	sub := image.NewPaletted(image.Rect(2, 2, 4, 4), pal)
	for i := range sub.Pix {
		sub.Pix[i] = blue
	}
	// A 1x1 sub-frame at (0,0).
	dot := image.NewPaletted(image.Rect(0, 0, 1, 1), pal)
	dot.Pix[0] = blue

	g := &gif.GIF{
		Image:     []*image.Paletted{full, sub, dot},
		Delay:     []int{10, 0, 25},
		Disposal:  []byte{gif.DisposalNone, gif.DisposalBackground, gif.DisposalNone},
		LoopCount: 0,
		Config:    image.Config{ColorModel: color.Palette(pal), Width: 4, Height: 4},
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, g); err != nil {
		t.Fatal(err)
	}
	d, err := Decode(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if d.Format != "gif" || !d.Animated() || len(d.Frames) != 3 {
		t.Fatalf("format %s frames %d", d.Format, len(d.Frames))
	}
	wantDelays := []time.Duration{100 * time.Millisecond, 100 * time.Millisecond, 250 * time.Millisecond}
	for i, f := range d.Frames {
		if f.Image.Bounds() != image.Rect(0, 0, 4, 4) {
			t.Errorf("frame %d bounds %v", i, f.Image.Bounds())
		}
		if f.Delay != wantDelays[i] {
			t.Errorf("frame %d delay %v, want %v", i, f.Delay, wantDelays[i])
		}
	}
	at := func(f, x, y int) color.RGBA {
		return color.RGBAModel.Convert(d.Frames[f].Image.At(x, y)).(color.RGBA)
	}
	if c := at(1, 3, 3); c.B != 255 || c.R != 0 {
		t.Errorf("frame 1 sub-image not composited: %v", c)
	}
	if c := at(1, 0, 0); c.R != 255 {
		t.Errorf("frame 1 should keep frame 0 pixels: %v", c)
	}
	// Frame 1 was disposed to background: (3,3) is transparent in frame 2.
	if c := at(2, 3, 3); c.A != 0 {
		t.Errorf("frame 2 should have cleared region: %v", c)
	}
	if c := at(2, 0, 0); c.B != 255 {
		t.Errorf("frame 2 dot missing: %v", c)
	}
	if c := at(2, 1, 1); c.R != 255 {
		t.Errorf("frame 2 should keep background: %v", c)
	}
}

func TestDecodeGIFDisposalPrevious(t *testing.T) {
	pal := palette.Plan9
	red := uint8(color.Palette(pal).Index(color.RGBA{255, 0, 0, 255}))
	blue := uint8(color.Palette(pal).Index(color.RGBA{0, 0, 255, 255}))
	base := image.NewPaletted(image.Rect(0, 0, 2, 2), pal)
	for i := range base.Pix {
		base.Pix[i] = red
	}
	over := image.NewPaletted(image.Rect(0, 0, 1, 1), pal)
	over.Pix[0] = blue
	empty := image.NewPaletted(image.Rect(1, 1, 2, 2), pal)
	empty.Pix[0] = red
	g := &gif.GIF{
		Image:    []*image.Paletted{base, over, empty},
		Delay:    []int{5, 5, 5},
		Disposal: []byte{0, gif.DisposalPrevious, 0},
		Config:   image.Config{ColorModel: color.Palette(pal), Width: 2, Height: 2},
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, g); err != nil {
		t.Fatal(err)
	}
	d, err := Decode(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	c := color.RGBAModel.Convert(d.Frames[2].Image.At(0, 0)).(color.RGBA)
	if c.R != 255 || c.B != 0 {
		t.Fatalf("disposal previous not restored: %v", c)
	}
}
