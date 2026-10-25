package tui

import (
	"image/color"
	"testing"

	"warte/internal/termimg"
	"warte/internal/tui/ui"
)

func TestInitialsImage(t *testing.T) {
	bg, ink := color.RGBA{R: 200, A: 255}, color.RGBA{R: 255, G: 255, B: 255, A: 255}
	img, err := initialsImage("AB", bg, ink)
	if err != nil {
		t.Fatal(err)
	}
	sq := termimg.Squircle(img)
	if a := sq.RGBAAt(0, 0).A; a != 0 {
		t.Errorf("corner alpha %d, want 0", a)
	}
	inked := 0
	b := sq.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if sq.RGBAAt(x, y) == ink {
				inked++
			}
		}
	}
	if inked == 0 {
		t.Error("no initials drawn")
	}
	if _, err := initialsImage("你", bg, ink); err == nil {
		t.Error("want an error for a rune the font lacks")
	}
}

func TestInitialsRefKey(t *testing.T) {
	a := ui.InitialsRef("AB", color.RGBA{R: 1, A: 255}, color.White)
	b := ui.InitialsRef("AB", color.RGBA{R: 2, A: 255}, color.White)
	if a.Key() == b.Key() || a.Key() != ui.InitialsRef("AB", color.RGBA{R: 1, A: 255}, color.White).Key() {
		t.Errorf("keys %q %q", a.Key(), b.Key())
	}
}
