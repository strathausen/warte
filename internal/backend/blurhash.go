package backend

import (
	"image"
	"math"
	"strings"
)

// blurHashChars is the base83 alphabet of the BlurHash format.
const blurHashChars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz#$%*+,-.:;=?@[]^_{|}~"

// blurHashSample is the edge length images are downsampled to before
// encoding; BlurHash only keeps a handful of low frequencies anyway.
const blurHashSample = 64

// blurHash encodes img as a BlurHash (https://blurha.sh) with xc×yc
// components (1-9 each). Signal sends it with media so receivers can show a
// placeholder before downloading. It returns "" for empty images.
func blurHash(img image.Image, xc, yc int) string {
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w <= 0 || h <= 0 || xc < 1 || xc > 9 || yc < 1 || yc > 9 {
		return ""
	}
	sw, sh := min(w, blurHashSample), min(h, blurHashSample)
	// Linear RGB samples of a nearest-neighbour downscale.
	px := make([][3]float64, sw*sh)
	for y := range sh {
		for x := range sw {
			r, g, b, _ := img.At(bounds.Min.X+x*w/sw, bounds.Min.Y+y*h/sh).RGBA()
			px[y*sw+x] = [3]float64{srgbToLinear(int(r >> 8)), srgbToLinear(int(g >> 8)), srgbToLinear(int(b >> 8))}
		}
	}
	factors := make([][3]float64, 0, xc*yc)
	for j := range yc {
		for i := range xc {
			norm := 2.0
			if i == 0 && j == 0 {
				norm = 1
			}
			var f [3]float64
			for y := range sh {
				cy := math.Cos(math.Pi * float64(j) * float64(y) / float64(sh))
				for x := range sw {
					basis := norm * math.Cos(math.Pi*float64(i)*float64(x)/float64(sw)) * cy
					p := px[y*sw+x]
					f[0] += basis * p[0]
					f[1] += basis * p[1]
					f[2] += basis * p[2]
				}
			}
			scale := 1 / float64(sw*sh)
			factors = append(factors, [3]float64{f[0] * scale, f[1] * scale, f[2] * scale})
		}
	}
	var sb strings.Builder
	sb.WriteString(base83((xc-1)+(yc-1)*9, 1))
	maxValue := 1.0
	if len(factors) > 1 {
		actualMax := 0.0
		for _, f := range factors[1:] {
			actualMax = math.Max(actualMax, math.Max(math.Abs(f[0]), math.Max(math.Abs(f[1]), math.Abs(f[2]))))
		}
		q := int(math.Max(0, math.Min(82, math.Floor(actualMax*166-0.5))))
		maxValue = float64(q+1) / 166
		sb.WriteString(base83(q, 1))
	} else {
		sb.WriteString(base83(0, 1))
	}
	dc := factors[0]
	sb.WriteString(base83(linearToSrgb(dc[0])<<16+linearToSrgb(dc[1])<<8+linearToSrgb(dc[2]), 4))
	for _, f := range factors[1:] {
		q := func(v float64) int {
			return int(math.Max(0, math.Min(18, math.Floor(signPow(v/maxValue, 0.5)*9+9.5))))
		}
		sb.WriteString(base83(q(f[0])*19*19+q(f[1])*19+q(f[2]), 2))
	}
	return sb.String()
}

func base83(v, length int) string {
	out := make([]byte, length)
	for i := 1; i <= length; i++ {
		digit := (v / int(math.Pow(83, float64(length-i)))) % 83
		out[i-1] = blurHashChars[digit]
	}
	return string(out)
}

func srgbToLinear(v int) float64 {
	f := float64(v) / 255
	if f <= 0.04045 {
		return f / 12.92
	}
	return math.Pow((f+0.055)/1.055, 2.4)
}

func linearToSrgb(v float64) int {
	v = math.Max(0, math.Min(1, v))
	if v <= 0.0031308 {
		return int(v*12.92*255 + 0.5)
	}
	return int((1.055*math.Pow(v, 1/2.4)-0.055)*255 + 0.5)
}

func signPow(v, exp float64) float64 {
	return math.Copysign(math.Pow(math.Abs(v), exp), v)
}
