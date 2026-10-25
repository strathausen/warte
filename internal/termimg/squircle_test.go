package termimg

import (
	"image"
	"image/color"
	"image/draw"
	"testing"
)

func TestSquircle(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 60, 40))
	draw.Draw(src, src.Bounds(), image.NewUniform(color.RGBA{R: 255, A: 255}), image.Point{}, draw.Src)
	out := Squircle(src)
	if b := out.Bounds(); b.Dx() != 40 || b.Dy() != 40 {
		t.Fatalf("bounds %v, want 40x40", b)
	}
	alpha := func(x, y int) uint8 { return out.RGBAAt(x, y).A }
	if a := alpha(0, 0); a != 0 {
		t.Errorf("corner alpha %d, want 0", a)
	}
	if a := alpha(20, 20); a != 255 {
		t.Errorf("center alpha %d, want 255", a)
	}
	// 10% padding: the shape spans x 4..36.
	if a := alpha(2, 20); a != 0 {
		t.Errorf("padding alpha %d, want 0", a)
	}
	if a := alpha(5, 20); a == 0 {
		t.Errorf("edge midpoint is transparent; a squircle keeps its flat sides")
	}
	// Squarer than a circle: a circle of radius 16 excludes (7,7).
	if a := alpha(7, 7); a == 0 {
		t.Errorf("(7,7) transparent; shape is too round")
	}
}
