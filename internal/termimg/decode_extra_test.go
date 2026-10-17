package termimg

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"image"
	"image/color"
	"image/color/palette"
	"image/gif"
	"image/jpeg"
	"testing"
	"time"
)

type apngTestFrame struct {
	img            image.Image
	x, y           int
	delayNum       uint16
	delayDen       uint16
	dispose, blend byte
}

// rawIDAT returns zlib-compressed, unfiltered 8-bit RGBA scanlines of img
// (PNG color type 6), so every frame shares one IHDR format.
func rawIDAT(t *testing.T, img image.Image) []byte {
	t.Helper()
	b := img.Bounds()
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		row := []byte{0}
		for x := b.Min.X; x < b.Max.X; x++ {
			c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			row = append(row, c.R, c.G, c.B, c.A)
		}
		zw.Write(row)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return z.Bytes()
}

// buildAPNG assembles an APNG whose default image is the first frame.
func buildAPNG(t *testing.T, w, h int, plays uint32, frames []apngTestFrame) []byte {
	t.Helper()
	var b bytes.Buffer
	b.Write(pngSignature)
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], uint32(w))
	binary.BigEndian.PutUint32(ihdr[4:], uint32(h))
	ihdr[8], ihdr[9] = 8, 6
	writePNGChunk(&b, "IHDR", ihdr)
	actl := make([]byte, 8)
	binary.BigEndian.PutUint32(actl, uint32(len(frames)))
	binary.BigEndian.PutUint32(actl[4:], plays)
	writePNGChunk(&b, "acTL", actl)
	seq := uint32(0)
	for i, f := range frames {
		fb := f.img.Bounds()
		fc := make([]byte, 26)
		binary.BigEndian.PutUint32(fc[0:], seq)
		seq++
		binary.BigEndian.PutUint32(fc[4:], uint32(fb.Dx()))
		binary.BigEndian.PutUint32(fc[8:], uint32(fb.Dy()))
		binary.BigEndian.PutUint32(fc[12:], uint32(f.x))
		binary.BigEndian.PutUint32(fc[16:], uint32(f.y))
		binary.BigEndian.PutUint16(fc[20:], f.delayNum)
		binary.BigEndian.PutUint16(fc[22:], f.delayDen)
		fc[24], fc[25] = f.dispose, f.blend
		writePNGChunk(&b, "fcTL", fc)
		data := rawIDAT(t, f.img)
		if i == 0 {
			writePNGChunk(&b, "IDAT", data)
			continue
		}
		fd := make([]byte, 4, 4+len(data))
		binary.BigEndian.PutUint32(fd, seq)
		seq++
		writePNGChunk(&b, "fdAT", append(fd, data...))
	}
	writePNGChunk(&b, "IEND", nil)
	return b.Bytes()
}

func rgbaAt(img image.Image, x, y int) color.RGBA {
	return color.RGBAModel.Convert(img.At(x, y)).(color.RGBA)
}

func TestDecodeAPNG(t *testing.T) {
	red := color.RGBA{255, 0, 0, 255}
	blue := color.RGBA{0, 0, 255, 255}
	data := buildAPNG(t, 4, 4, 0, []apngTestFrame{
		{img: solid(4, 4, red), delayNum: 1, delayDen: 10},
		// 2x2 blue at (2,2), cleared to transparent afterwards.
		{img: solid(2, 2, blue), x: 2, y: 2, delayNum: 50, delayDen: 1000, dispose: apngDisposeBackground, blend: apngBlendOver},
		// half-transparent pixel blended over red at (0,0), then restored.
		{img: solid(1, 1, color.NRGBA{0, 0, 255, 0}), delayNum: 0, delayDen: 0, dispose: apngDisposePrevious, blend: apngBlendOver},
		// source blend replaces with transparent.
		{img: solid(1, 1, color.NRGBA{0, 0, 0, 0}), x: 1, y: 1, delayNum: 3, delayDen: 0, blend: apngBlendSource},
	})
	if !isAPNG(data) {
		t.Fatal("isAPNG = false")
	}
	d, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	if d.Format != "png" || len(d.Frames) != 4 || d.LoopCount != 0 {
		t.Fatalf("format %s frames %d loops %d", d.Format, len(d.Frames), d.LoopCount)
	}
	wantDelays := []time.Duration{100 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond, 30 * time.Millisecond}
	for i, f := range d.Frames {
		if f.Image.Bounds() != image.Rect(0, 0, 4, 4) {
			t.Errorf("frame %d bounds %v", i, f.Image.Bounds())
		}
		if f.Delay != wantDelays[i] {
			t.Errorf("frame %d delay %v, want %v", i, f.Delay, wantDelays[i])
		}
	}
	if c := rgbaAt(d.Frames[1].Image, 3, 3); c != blue {
		t.Errorf("frame 1 (3,3) = %v", c)
	}
	if c := rgbaAt(d.Frames[1].Image, 0, 0); c != red {
		t.Errorf("frame 1 (0,0) = %v", c)
	}
	if c := rgbaAt(d.Frames[2].Image, 3, 3); c.A != 0 {
		t.Errorf("frame 2 should have cleared (3,3): %v", c)
	}
	if c := rgbaAt(d.Frames[2].Image, 0, 0); c != red {
		t.Errorf("frame 2 transparent over-blend changed (0,0): %v", c)
	}
	if c := rgbaAt(d.Frames[3].Image, 1, 1); c.A != 0 {
		t.Errorf("frame 3 source blend should clear (1,1): %v", c)
	}
	if c := rgbaAt(d.Frames[3].Image, 0, 0); c != red {
		t.Errorf("frame 3 (0,0) = %v", c)
	}
}

func TestDecodeAPNGLoopsAndFallback(t *testing.T) {
	red := color.RGBA{255, 0, 0, 255}
	for _, tt := range []struct {
		plays uint32
		want  int
	}{{0, 0}, {1, -1}, {3, 2}} {
		d, err := Decode(buildAPNG(t, 2, 2, tt.plays, []apngTestFrame{
			{img: solid(2, 2, red)}, {img: solid(2, 2, red)},
		}))
		if err != nil {
			t.Fatal(err)
		}
		if d.LoopCount != tt.want {
			t.Errorf("plays %d: LoopCount %d, want %d", tt.plays, d.LoopCount, tt.want)
		}
	}

	// A frame outside the canvas makes the animation invalid; the static
	// default image is still returned.
	bad := buildAPNG(t, 2, 2, 0, []apngTestFrame{
		{img: solid(2, 2, red)}, {img: solid(2, 2, red), x: 1},
	})
	d, err := Decode(bad)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Frames) != 1 || rgbaAt(d.First(), 0, 0) != red {
		t.Fatalf("fallback: %d frames", len(d.Frames))
	}

	// A plain PNG is not an APNG.
	if isAPNG(pngBytes(t, solid(1, 1, red))) || isAPNG([]byte("junk")) {
		t.Fatal("isAPNG on plain data")
	}
}

// jpegWithOrientation returns a JPEG of img carrying an EXIF APP1 segment
// with the given orientation, in the given byte order.
func jpegWithOrientation(t *testing.T, img image.Image, o uint16, bigEndian bool) []byte {
	t.Helper()
	var j bytes.Buffer
	if err := jpeg.Encode(&j, img, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	var bo binary.AppendByteOrder = binary.LittleEndian
	tiff := []byte("II")
	if bigEndian {
		bo, tiff = binary.BigEndian, []byte("MM")
	}
	tiff = bo.AppendUint16(tiff, 42)
	tiff = bo.AppendUint32(tiff, 8)
	tiff = bo.AppendUint16(tiff, 2) // two entries
	// An unrelated tag first (ImageWidth, LONG).
	tiff = bo.AppendUint16(tiff, 0x0100)
	tiff = bo.AppendUint16(tiff, 4)
	tiff = bo.AppendUint32(tiff, 1)
	tiff = bo.AppendUint32(tiff, 99)
	tiff = bo.AppendUint16(tiff, 0x0112)
	tiff = bo.AppendUint16(tiff, 3)
	tiff = bo.AppendUint32(tiff, 1)
	tiff = bo.AppendUint16(tiff, o)
	tiff = bo.AppendUint16(tiff, 0)
	tiff = bo.AppendUint32(tiff, 0) // next IFD
	seg := append([]byte("Exif\x00\x00"), tiff...)
	out := []byte{0xFF, 0xD8, 0xFF, 0xE1}
	out = binary.BigEndian.AppendUint16(out, uint16(len(seg)+2))
	out = append(out, seg...)
	return append(out, j.Bytes()[2:]...)
}

func TestJPEGOrientation(t *testing.T) {
	// 16x8 image: left half red, right half blue, so rotations are visible.
	src := image.NewRGBA(image.Rect(0, 0, 16, 8))
	for y := range 8 {
		for x := range 16 {
			c := color.RGBA{255, 0, 0, 255}
			if x >= 8 {
				c = color.RGBA{0, 0, 255, 255}
			}
			src.Set(x, y, c)
		}
	}
	isRed := func(c color.RGBA) bool { return c.R > 200 && c.B < 60 }
	isBlue := func(c color.RGBA) bool { return c.B > 200 && c.R < 60 }
	tests := []struct {
		o      uint16
		w, h   int
		redAt  image.Point // a point that must be red
		blueAt image.Point // a point that must be blue
	}{
		{1, 16, 8, image.Pt(1, 1), image.Pt(14, 1)},
		{2, 16, 8, image.Pt(14, 1), image.Pt(1, 1)},
		{3, 16, 8, image.Pt(14, 6), image.Pt(1, 6)},
		{4, 16, 8, image.Pt(1, 6), image.Pt(14, 6)},
		{5, 8, 16, image.Pt(1, 1), image.Pt(1, 14)},
		{6, 8, 16, image.Pt(1, 1), image.Pt(1, 14)},
		{7, 8, 16, image.Pt(1, 14), image.Pt(1, 1)},
		{8, 8, 16, image.Pt(1, 14), image.Pt(1, 1)},
	}
	for _, tt := range tests {
		for _, be := range []bool{false, true} {
			data := jpegWithOrientation(t, src, tt.o, be)
			if got := jpegOrientation(data); got != int(tt.o) {
				t.Fatalf("orientation %d (be=%v): parsed %d", tt.o, be, got)
			}
			img, err := DecodeImage(data)
			if err != nil {
				t.Fatal(err)
			}
			if b := img.Bounds(); b.Dx() != tt.w || b.Dy() != tt.h {
				t.Fatalf("orientation %d: bounds %v", tt.o, b)
			}
			if c := rgbaAt(img, tt.redAt.X, tt.redAt.Y); !isRed(c) {
				t.Errorf("orientation %d: %v = %v, want red", tt.o, tt.redAt, c)
			}
			if c := rgbaAt(img, tt.blueAt.X, tt.blueAt.Y); !isBlue(c) {
				t.Errorf("orientation %d: %v = %v, want blue", tt.o, tt.blueAt, c)
			}
		}
	}
	// Orientation 6 vs 8 differ in where the top-left source pixel lands.
	o6 := applyOrientation(gradient(3, 2), 6)
	if rgbaAt(o6, 1, 0) != rgbaAt(gradient(3, 2), 0, 0) {
		t.Error("orientation 6: source (0,0) should land at top-right")
	}
	o8 := applyOrientation(gradient(3, 2), 8)
	if rgbaAt(o8, 0, 2) != rgbaAt(gradient(3, 2), 0, 0) {
		t.Error("orientation 8: source (0,0) should land at bottom-left")
	}
	// Garbage never panics and means "upright".
	for _, junk := range [][]byte{nil, {0xFF, 0xD8}, {0xFF, 0xD8, 0xFF, 0xE1, 0, 1}, {0xFF, 0xD8, 0xFF, 0xE1, 0xFF, 0xFF}} {
		if o := jpegOrientation(junk); o != 1 {
			t.Errorf("junk %x: orientation %d", junk, o)
		}
	}
	for _, junk := range [][]byte{nil, []byte("II*\x00\xff\xff\xff\xff"), []byte("XX*\x00\x08\x00\x00\x00"), []byte("MM\x00*\x00\x00\x00\x08\x00\x05")} {
		if o := tiffOrientation(junk); o != 1 {
			t.Errorf("tiff junk %x: orientation %d", junk, o)
		}
	}
}

func TestLimitGIF(t *testing.T) {
	pal := palette.Plan9
	frames := make([]*image.Paletted, 5)
	for i := range frames {
		frames[i] = image.NewPaletted(image.Rect(0, 0, 10, 10), pal)
		for j := range frames[i].Pix {
			frames[i].Pix[j] = uint8(i * 20)
		}
	}
	g := &gif.GIF{
		Image:     frames,
		Delay:     []int{10, 10, 10, 10, 10},
		LoopCount: 3,
		Config:    image.Config{ColorModel: color.Palette(pal), Width: 10, Height: 10},
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, g); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()

	if got := limitGIF(data, 10, 1<<20); !bytes.Equal(got, data) {
		t.Fatal("within budget: data changed")
	}
	for _, tt := range []struct {
		maxFrames int
		maxBytes  int64
		want      int
	}{{2, 1 << 20, 2}, {10, 350, 3}, {1, 1, 1}} {
		cut := limitGIF(data, tt.maxFrames, tt.maxBytes)
		dg, err := gif.DecodeAll(bytes.NewReader(cut))
		if err != nil {
			t.Fatalf("truncated gif does not decode: %v", err)
		}
		if len(dg.Image) != tt.want || dg.LoopCount != 3 {
			t.Errorf("budget %d/%d: %d frames (loop %d), want %d", tt.maxFrames, tt.maxBytes, len(dg.Image), dg.LoopCount, tt.want)
		}
	}
	// Malformed input is passed through untouched.
	for _, junk := range [][]byte{nil, []byte("GIF89a"), append(append([]byte(nil), data[:20]...), 0x2C)} {
		if got := limitGIF(junk, 1, 1); !bytes.Equal(got, junk) {
			t.Errorf("junk changed")
		}
	}
}

func TestDecodeBlurHashSizeLimit(t *testing.T) {
	if _, err := DecodeBlurHash("LEHV6nWB2yk8pyo0adR*.7kCMdnj", 1<<16, 1<<16, 1); err == nil {
		t.Fatal("expected error for oversized blurhash output")
	}
}
