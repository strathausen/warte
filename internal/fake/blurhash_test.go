package fake

import (
	"bytes"
	"image"
	"math"
	"testing"
)

// refBlurFactors is the straightforward per-pixel blurhash DCT that
// blurHash's table-driven loop must reproduce exactly.
func refBlurFactors(t *testing.T, data []byte) [3][4][3]float64 {
	t.Helper()
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	bnd := img.Bounds()
	w, h := bnd.Dx(), bnd.Dy()
	step := max(1, min(w, h)/48)
	var f [3][4][3]float64
	for j := 0; j < 3; j++ {
		for i := 0; i < 4; i++ {
			norm := 2.0
			if i == 0 && j == 0 {
				norm = 1
			}
			var r, g, b float64
			n := 0
			for y := 0; y < h; y += step {
				for x := 0; x < w; x += step {
					basis := math.Cos(math.Pi*float64(i)*float64(x)/float64(w)) * math.Cos(math.Pi*float64(j)*float64(y)/float64(h))
					pr, pg, pb, _ := img.At(bnd.Min.X+x, bnd.Min.Y+y).RGBA()
					r += basis * srgbToLinear(uint8(pr>>8))
					g += basis * srgbToLinear(uint8(pg>>8))
					b += basis * srgbToLinear(uint8(pb>>8))
					n++
				}
			}
			s := norm / float64(n)
			f[j][i] = [3]float64{r * s, g * s, b * s}
		}
	}
	return f
}

// TestBlurHashMatchesReference checks the optimized encoder against the
// reference DCT on real demo images: the hashes must be identical.
func TestBlurHashMatchesReference(t *testing.T) {
	for _, data := range [][]byte{landscapePNG(96, 64, 0), landscapePNG(64, 96, 1), patternPNG(50, 50, 2, 0xff8800, 0x0088ff)} {
		want := encodeBlurHash(refBlurFactors(t, data))
		if got := blurHash(data); got != want || len(got) != 28 {
			t.Errorf("blurHash = %q, want %q", got, want)
		}
	}
	for i := range srgbLinear {
		if srgbLinear[i] != srgbToLinear(uint8(i)) {
			t.Fatalf("srgbLinear[%d] mismatch", i)
		}
	}
}
