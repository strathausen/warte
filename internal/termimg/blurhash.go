package termimg

import (
	"errors"
	"fmt"
	"image"
	"math"
)

const base83Chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz#$%*+,-.:;=?@[]^_{|}~"

var base83Index = func() [256]int8 {
	var t [256]int8
	for i := range t {
		t[i] = -1
	}
	for i := 0; i < len(base83Chars); i++ {
		t[base83Chars[i]] = int8(i)
	}
	return t
}()

// ErrInvalidBlurHash is returned for malformed BlurHash strings.
var ErrInvalidBlurHash = errors.New("termimg: invalid blurhash")

func decode83(s string) (int, error) {
	v := 0
	for i := 0; i < len(s); i++ {
		d := base83Index[s[i]]
		if d < 0 {
			return 0, fmt.Errorf("%w: bad character %q", ErrInvalidBlurHash, s[i])
		}
		v = v*83 + int(d)
	}
	return v, nil
}

// BlurHashComponents returns the number of horizontal and vertical
// components encoded in hash and validates its length.
func BlurHashComponents(hash string) (x, y int, err error) {
	if len(hash) < 6 {
		return 0, 0, fmt.Errorf("%w: too short", ErrInvalidBlurHash)
	}
	size, err := decode83(hash[:1])
	if err != nil {
		return 0, 0, err
	}
	x, y = size%9+1, size/9+1
	if want := 4 + 2*x*y; len(hash) != want {
		return 0, 0, fmt.Errorf("%w: length %d, want %d", ErrInvalidBlurHash, len(hash), want)
	}
	return x, y, nil
}

func srgbToLinear(v int) float64 {
	f := float64(v) / 255
	if f <= 0.04045 {
		return f / 12.92
	}
	return math.Pow((f+0.055)/1.055, 2.4)
}

func linearToSRGB(v float64) uint8 {
	v = math.Max(0, math.Min(1, v))
	if v <= 0.0031308 {
		return uint8(math.Round(v * 12.92 * 255))
	}
	return uint8(math.Round((1.055*math.Pow(v, 1/2.4) - 0.055) * 255))
}

func signPow(v, e float64) float64 {
	return math.Copysign(math.Pow(math.Abs(v), e), v)
}

// DecodeBlurHash renders hash as a w x h image. punch scales the AC
// components (contrast); values <= 0 mean 1. Typical use is a small image
// (for example 32x32) that is then scaled up by the active renderer as a
// placeholder until the real attachment has downloaded.
func DecodeBlurHash(hash string, w, h int, punch float64) (*image.NRGBA, error) {
	if w <= 0 || h <= 0 || int64(w)*int64(h) > MaxPixels {
		return nil, fmt.Errorf("termimg: blurhash size %dx%d", w, h)
	}
	nx, ny, err := BlurHashComponents(hash)
	if err != nil {
		return nil, err
	}
	if punch <= 0 {
		punch = 1
	}
	qmax, err := decode83(hash[1:2])
	if err != nil {
		return nil, err
	}
	maxAC := float64(qmax+1) / 166

	colors := make([][3]float64, nx*ny)
	dc, err := decode83(hash[2:6])
	if err != nil {
		return nil, err
	}
	colors[0] = [3]float64{srgbToLinear(dc >> 16), srgbToLinear((dc >> 8) & 255), srgbToLinear(dc & 255)}
	for i := 1; i < len(colors); i++ {
		v, err := decode83(hash[4+i*2 : 6+i*2])
		if err != nil {
			return nil, err
		}
		qr, qg, qb := v/(19*19), (v/19)%19, v%19
		colors[i] = [3]float64{
			signPow((float64(qr)-9)/9, 2) * maxAC * punch,
			signPow((float64(qg)-9)/9, 2) * maxAC * punch,
			signPow((float64(qb)-9)/9, 2) * maxAC * punch,
		}
	}

	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	cosX := make([]float64, w*nx)
	for x := range w {
		for i := range nx {
			cosX[x*nx+i] = math.Cos(math.Pi * float64(x) * float64(i) / float64(w))
		}
	}
	cosY := make([]float64, h*ny)
	for y := range h {
		for j := range ny {
			cosY[y*ny+j] = math.Cos(math.Pi * float64(y) * float64(j) / float64(h))
		}
	}
	for y := range h {
		for x := range w {
			var r, g, b float64
			for j := range ny {
				cy := cosY[y*ny+j]
				for i := range nx {
					basis := cosX[x*nx+i] * cy
					c := colors[j*nx+i]
					r += c[0] * basis
					g += c[1] * basis
					b += c[2] * basis
				}
			}
			o := img.PixOffset(x, y)
			img.Pix[o] = linearToSRGB(r)
			img.Pix[o+1] = linearToSRGB(g)
			img.Pix[o+2] = linearToSRGB(b)
			img.Pix[o+3] = 255
		}
	}
	return img, nil
}
