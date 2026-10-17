package theme

import (
	"fmt"
	"image/color"
	"math"
	"strings"

	"charm.land/lipgloss/v2"
)

// rgb is an 8-bit sRGB triple used for palette math.
type rgb struct{ r, g, b uint8 }

// parseHex parses "#rrggbb" or "rrggbb". It panics on malformed input; it is
// only ever called on the compile-time theme tables, which are covered by tests.
func parseHex(s string) rgb {
	s = strings.TrimPrefix(s, "#")
	var c rgb
	if len(s) != 6 {
		panic(fmt.Sprintf("theme: bad hex color %q", s))
	}
	if _, err := fmt.Sscanf(s, "%02x%02x%02x", &c.r, &c.g, &c.b); err != nil {
		panic(fmt.Sprintf("theme: bad hex color %q: %v", s, err))
	}
	return c
}

func (c rgb) hex() string { return fmt.Sprintf("#%02x%02x%02x", c.r, c.g, c.b) }

func (c rgb) color() color.Color { return lipgloss.Color(c.hex()) }

func toRGB(c color.Color) rgb {
	r, g, b, _ := c.RGBA()
	return rgb{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)}
}

// mix linearly blends a toward b by t (0 = a, 1 = b) in sRGB space.
func mix(a, b rgb, t float64) rgb {
	t = math.Max(0, math.Min(1, t))
	l := func(x, y uint8) uint8 {
		return uint8(math.Round(float64(x) + (float64(y)-float64(x))*t))
	}
	return rgb{l(a.r, b.r), l(a.g, b.g), l(a.b, b.b)}
}

func (c rgb) luminance() float64 {
	ch := func(v uint8) float64 {
		f := float64(v) / 255
		if f <= 0.03928 {
			return f / 12.92
		}
		return math.Pow((f+0.055)/1.055, 2.4)
	}
	return 0.2126*ch(c.r) + 0.7152*ch(c.g) + 0.0722*ch(c.b)
}

func contrast(a, b rgb) float64 {
	la, lb := a.luminance(), b.luminance()
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// Luminance returns the WCAG 2.x relative luminance of c in [0, 1].
func Luminance(c color.Color) float64 { return toRGB(c).luminance() }

// Contrast returns the WCAG 2.x contrast ratio between two colors, in [1, 21].
func Contrast(a, b color.Color) float64 { return contrast(toRGB(a), toRGB(b)) }

// Hex formats c as a lowercase "#rrggbb" string (alpha is ignored).
func Hex(c color.Color) string { return toRGB(c).hex() }

var (
	white = rgb{0xff, 0xff, 0xff}
	black = rgb{0x00, 0x00, 0x00}
)

// ensureContrast nudges c toward white (on dark backgrounds) or black (on light
// backgrounds) until it reaches min contrast against bg, keeping the hue as
// close to the original as possible.
func ensureContrast(c, bg rgb, min float64) rgb {
	if contrast(c, bg) >= min {
		return c
	}
	target := black
	if bg.luminance() < 0.18 {
		target = white
	}
	for t := 0.02; t <= 1.0; t += 0.02 {
		if m := mix(c, target, t); contrast(m, bg) >= min {
			return m
		}
	}
	return target
}

// tint blends hue into bg by amount, backing off until fg keeps at least min
// contrast on the result. It is used for soft, readable surface colors.
func tint(hue, bg, fg rgb, amount, min float64) rgb {
	for a := amount; a > 0; a -= 0.02 {
		if m := mix(bg, hue, a); contrast(fg, m) >= min {
			return m
		}
	}
	return bg
}

// readableOn returns the first theme-native candidate that reaches 4.5:1
// against bg, falling back to whichever of the candidates, white or black has
// the best contrast.
func readableOn(bg rgb, candidates ...rgb) rgb {
	for _, c := range candidates {
		if contrast(c, bg) >= 4.5 {
			return c
		}
	}
	best, bestC := white, -1.0
	for _, c := range append(candidates, white, black) {
		if cr := contrast(c, bg); cr > bestC {
			best, bestC = c, cr
		}
	}
	return best
}
