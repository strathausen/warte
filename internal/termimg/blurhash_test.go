package termimg

import (
	"errors"
	"testing"
)

func TestBlurHashComponents(t *testing.T) {
	x, y, err := BlurHashComponents("LEHV6nWB2yk8pyo0adR*.7kCMdnj")
	if err != nil || x != 4 || y != 3 {
		t.Fatalf("got %d x %d, %v", x, y, err)
	}
	for _, bad := range []string{"", "LEHV6", "LEHV6nWB2yk8pyo0adR*.7kCMdn", "LEHV6nWB2yk8pyo0adR*.7kCMdn\"", "LEHV6nWB2yk8pyo0adR*.7kCMdnjj"} {
		if _, _, err := BlurHashComponents(bad); !errors.Is(err, ErrInvalidBlurHash) {
			if _, err2 := DecodeBlurHash(bad, 4, 4, 1); err2 == nil {
				t.Errorf("%q: expected error", bad)
			}
		}
	}
}

func TestDecodeBlurHash(t *testing.T) {
	img, err := DecodeBlurHash("LEHV6nWB2yk8pyo0adR*.7kCMdnj", 32, 32, 1)
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 32 || img.Bounds().Dy() != 32 {
		t.Fatalf("bounds %v", img.Bounds())
	}
	// The cosine basis functions for AC components average out, so the mean
	// color must be close to the DC component, and every pixel is opaque.
	var r, g, b int
	for y := range 32 {
		for x := range 32 {
			c := img.NRGBAAt(x, y)
			if c.A != 255 {
				t.Fatal("not opaque")
			}
			r += int(c.R)
			g += int(c.G)
			b += int(c.B)
		}
	}
	r, g, b = r/1024, g/1024, b/1024
	dc, _ := decode83("LEHV6nWB2yk8pyo0adR*.7kCMdnj"[2:6])
	wr, wg, wb := dc>>16, (dc>>8)&255, dc&255
	for _, d := range []int{r - wr, g - wg, b - wb} {
		if d < -30 || d > 30 {
			t.Fatalf("average %d,%d,%d far from DC %d,%d,%d", r, g, b, wr, wg, wb)
		}
	}
	// Not flat: the AC components produce variation.
	tl, br := img.NRGBAAt(0, 0), img.NRGBAAt(31, 31)
	if tl == br {
		t.Fatal("expected variation across the image")
	}
}

func TestDecodeBlurHashPunchAndSize(t *testing.T) {
	soft, _ := DecodeBlurHash("LEHV6nWB2yk8pyo0adR*.7kCMdnj", 8, 8, 0.5)
	hard, _ := DecodeBlurHash("LEHV6nWB2yk8pyo0adR*.7kCMdnj", 8, 8, 2)
	spread := func(px []uint8) int {
		lo, hi := 255, 0
		for i := 0; i < len(px); i += 4 {
			lo, hi = min(lo, int(px[i])), max(hi, int(px[i]))
		}
		return hi - lo
	}
	if spread(hard.Pix) <= spread(soft.Pix) {
		t.Fatal("higher punch should increase contrast")
	}
	if _, err := DecodeBlurHash("LEHV6nWB2yk8pyo0adR*.7kCMdnj", 0, 4, 1); err == nil {
		t.Fatal("expected size error")
	}
}

func TestDecode83(t *testing.T) {
	tests := map[string]int{"0": 0, "z": 61, "~": 82, "10": 83, "~~": 83*83 - 1}
	for in, want := range tests {
		if got, err := decode83(in); err != nil || got != want {
			t.Errorf("decode83(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	if _, err := decode83("\""); err == nil {
		t.Error("expected error")
	}
}

func encode83(v, length int) string {
	b := make([]byte, length)
	for i := length - 1; i >= 0; i-- {
		b[i] = base83Chars[v%83]
		v /= 83
	}
	return string(b)
}

func TestDecodeBlurHashSolid(t *testing.T) {
	// A 1x1-component hash is a flat image of its DC color.
	for _, c := range [][3]int{{255, 0, 0}, {18, 52, 86}, {0, 0, 0}, {255, 255, 255}} {
		hash := "00" + encode83(c[0]<<16|c[1]<<8|c[2], 4)
		img, err := DecodeBlurHash(hash, 3, 2, 1)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < len(img.Pix); i += 4 {
			got := [3]int{int(img.Pix[i]), int(img.Pix[i+1]), int(img.Pix[i+2])}
			if got != c {
				t.Fatalf("hash %q pixel %v, want %v", hash, got, c)
			}
		}
	}
}
