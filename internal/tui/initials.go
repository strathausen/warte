package tui

import (
	"errors"
	"image"
	"image/color"
	"image/draw"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// initialsSize is the pixel size avatars with initials are drawn at; the
// renderer scales them down to the cell box.
const initialsSize = 128

var (
	initialsFontOnce sync.Once
	initialsFont     *opentype.Font
	initialsFontErr  error
)

// initialsImage draws a square avatar: initials in ink, centered on bg. It
// fails when the font has no glyph for a rune (the caller then keeps the
// text tile, which the terminal's own fonts can draw).
func initialsImage(text string, bg, ink color.RGBA) (image.Image, error) {
	initialsFontOnce.Do(func() { initialsFont, initialsFontErr = opentype.Parse(gobold.TTF) })
	if initialsFontErr != nil {
		return nil, initialsFontErr
	}
	face, err := opentype.NewFace(initialsFont, &opentype.FaceOptions{Size: initialsSize * 0.36, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return nil, err
	}
	defer face.Close()
	for _, r := range text {
		if _, ok := face.GlyphAdvance(r); !ok {
			return nil, errors.New("no glyph for initials")
		}
	}
	img := image.NewRGBA(image.Rect(0, 0, initialsSize, initialsSize))
	draw.Draw(img, img.Bounds(), image.NewUniform(bg), image.Point{}, draw.Src)
	d := font.Drawer{Dst: img, Src: image.NewUniform(ink), Face: face}
	bounds, _ := d.BoundString(text)
	w := bounds.Max.X - bounds.Min.X
	h := bounds.Max.Y - bounds.Min.Y
	c := fixed.I(initialsSize / 2)
	d.Dot = fixed.Point26_6{X: c - w/2 - bounds.Min.X, Y: c - h/2 - bounds.Min.Y}
	d.DrawString(text)
	return img, nil
}
