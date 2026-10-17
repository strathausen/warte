package stories

import (
	"image/color"
	"math"
)

// Text story colors are part of the story's content (chosen by its author),
// not of the UI theme, so they are the one place where this package draws
// colors that do not come from ctx.Styles. With the mono theme they are not
// used at all.

// argb converts a Signal ARGB value to an RGBA color. Alpha is applied by
// blending over base (usually the theme background); a fully transparent
// value yields base itself.
func argb(v uint32, base color.Color) color.Color {
	a := uint8(v >> 24)
	c := color.RGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 0xff}
	if a == 0xff || base == nil {
		return c
	}
	return blend(base, c, float64(a)/255)
}

// blend returns a + (b-a)*t in RGB space (t clamped to [0,1]).
func blend(a, b color.Color, t float64) color.Color {
	t = math.Max(0, math.Min(1, t))
	ar, ag, ab := rgb8(a)
	br, bg, bb := rgb8(b)
	mix := func(x, y uint8) uint8 {
		return uint8(math.Round(float64(x) + (float64(y)-float64(x))*t))
	}
	return color.RGBA{R: mix(ar, br), G: mix(ag, bg), B: mix(ab, bb), A: 0xff}
}

func rgb8(c color.Color) (r, g, b uint8) {
	if c == nil {
		return 0, 0, 0
	}
	R, G, B, _ := c.RGBA()
	return uint8(R >> 8), uint8(G >> 8), uint8(B >> 8)
}

// luminance is the relative luminance (0 = black, 1 = white).
func luminance(c color.Color) float64 {
	r, g, b := rgb8(c)
	lin := func(v uint8) float64 {
		x := float64(v) / 255
		if x <= 0.03928 {
			return x / 12.92
		}
		return math.Pow((x+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(r) + 0.7152*lin(g) + 0.0722*lin(b)
}

// contrast is the WCAG contrast ratio of two colors (1..21).
func contrast(a, b color.Color) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// gradientAt returns the color at position t (0..1) along evenly spaced stops.
func gradientAt(stops []color.Color, t float64) color.Color {
	switch len(stops) {
	case 0:
		return nil
	case 1:
		return stops[0]
	}
	t = math.Max(0, math.Min(1, t))
	seg := t * float64(len(stops)-1)
	i := int(seg)
	if i >= len(stops)-1 {
		return stops[len(stops)-1]
	}
	return blend(stops[i], stops[i+1], seg-float64(i))
}

var (
	white = color.RGBA{0xff, 0xff, 0xff, 0xff}
	black = color.RGBA{0x10, 0x10, 0x14, 0xff}
)

// readableOn picks fg if it is legible on bg, otherwise white or near-black,
// whichever contrasts more.
func readableOn(fg, bg color.Color) color.Color {
	if fg != nil && contrast(fg, bg) >= 2.2 {
		return fg
	}
	if contrast(white, bg) >= contrast(black, bg) {
		return white
	}
	return black
}

// Preset is a background choice offered by the story composer.
type Preset struct {
	Name       string
	Foreground uint32
	// Background is the solid color (or the first gradient stop).
	Background uint32
	// Gradient, when non-empty, lists the gradient stops top to bottom.
	Gradient []uint32
}

// Presets are the composer's background presets, cycled with tab. They
// mirror the kind of palette Signal's apps offer.
var Presets = []Preset{
	{Name: "Ink", Foreground: 0xffffffff, Background: 0xff2c2c3a},
	{Name: "Ocean", Foreground: 0xffffffff, Background: 0xff1d3557, Gradient: []uint32{0xff1d3557, 0xff457b9d, 0xffa8dadc}},
	{Name: "Sunset", Foreground: 0xffffffff, Background: 0xffff6f61, Gradient: []uint32{0xff6a0572, 0xffd7385e, 0xffff9a5a}},
	{Name: "Forest", Foreground: 0xffffffff, Background: 0xff2d6a4f, Gradient: []uint32{0xff1b4332, 0xff40916c}},
	{Name: "Berry", Foreground: 0xffffffff, Background: 0xff7b2cbf},
	{Name: "Sand", Foreground: 0xff2b2d42, Background: 0xffffd166},
	{Name: "Night", Foreground: 0xffffffff, Background: 0xff1d1147, Gradient: []uint32{0xff1d1147, 0xffff6f91}},
	{Name: "Mint", Foreground: 0xff1b4332, Background: 0xffb7e4c7},
	{Name: "Coral", Foreground: 0xffffffff, Background: 0xffe76f51},
}

// FontStyles names Signal's TextAttachment.Style values in order.
var FontStyles = []string{"Default", "Regular", "Bold", "Serif", "Script", "Condensed"}

func fontStyleName(s int) string {
	if s >= 0 && s < len(FontStyles) {
		return FontStyles[s]
	}
	return FontStyles[0]
}
