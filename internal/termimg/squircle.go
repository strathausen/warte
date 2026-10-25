package termimg

import (
	"image"
	"image/draw"
	"math"
)

// squircleExp is the superellipse exponent: 2 is a circle, higher is
// squarer. 5 is close to the iOS app icon shape.
const squircleExp = 5

// squirclePad is the transparent margin on each side, as a fraction of the
// size, so avatars in neighbouring rows don't touch.
const squirclePad = 0.1

// Squircle crops img to a centered square and makes everything outside a
// superellipse (|x|^n + |y|^n <= 1) transparent, with anti-aliased edges.
// The shape is inset by squirclePad on every side.
func Squircle(img image.Image) *image.RGBA {
	b := img.Bounds()
	s := min(b.Dx(), b.Dy())
	src := image.Pt(b.Min.X+(b.Dx()-s)/2, b.Min.Y+(b.Dy()-s)/2)
	out := image.NewRGBA(image.Rect(0, 0, s, s))
	draw.Draw(out, out.Bounds(), img, src, draw.Src)
	c := float64(s) / 2
	r := c * (1 - 2*squirclePad)
	shape := func(x, y float64) float64 {
		return math.Pow(math.Abs(x-c)/r, squircleExp) + math.Pow(math.Abs(y-c)/r, squircleExp)
	}
	inside := func(x, y float64) bool { return shape(x, y) <= 1 }
	const ss = 4 // supersamples per axis, only near the edge
	for y := 0; y < s; y++ {
		for x := 0; x < s; x++ {
			f := shape(float64(x)+0.5, float64(y)+0.5)
			if f < 0.9 {
				continue
			}
			n := 0
			if f < 1.1 {
				for sy := 0; sy < ss; sy++ {
					for sx := 0; sx < ss; sx++ {
						if inside(float64(x)+(float64(sx)+0.5)/ss, float64(y)+(float64(sy)+0.5)/ss) {
							n++
						}
					}
				}
			}
			if n == ss*ss {
				continue
			}
			// RGBA is premultiplied: scale every channel.
			i := out.PixOffset(x, y)
			for c := 0; c < 4; c++ {
				out.Pix[i+c] = uint8(int(out.Pix[i+c]) * n / (ss * ss))
			}
		}
	}
	return out
}
