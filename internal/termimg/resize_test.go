package termimg

import (
	"image"
	"image/color"
	"testing"
)

func TestFit(t *testing.T) {
	tests := []struct {
		name           string
		sw, sh, mw, mh int
		upscale        bool
		wantW, wantH   int
	}{
		{"shrink wide", 400, 200, 100, 100, false, 100, 50},
		{"shrink tall", 200, 400, 100, 100, false, 50, 100},
		{"already fits", 50, 20, 100, 100, false, 50, 20},
		{"upscale", 50, 20, 100, 100, true, 100, 40},
		{"exact", 100, 100, 100, 100, false, 100, 100},
		{"extreme strip", 10000, 1, 100, 100, false, 100, 1},
		{"rounding", 3, 2, 100, 100, true, 100, 67},
		{"degenerate src", 0, 10, 10, 10, false, 0, 0},
		{"degenerate box", 10, 10, 0, 10, false, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, h := Fit(tt.sw, tt.sh, tt.mw, tt.mh, tt.upscale)
			if w != tt.wantW || h != tt.wantH {
				t.Fatalf("Fit = %dx%d, want %dx%d", w, h, tt.wantW, tt.wantH)
			}
		})
	}
}

func TestFitCells(t *testing.T) {
	tests := []struct {
		name               string
		w, h, mc, mr       int
		cell               CellSize
		upscale            bool
		wantCols, wantRows int
	}{
		{"landscape photo", 4000, 3000, 40, 12, CellSize{}, false, 32, 12},
		{"portrait photo", 3000, 4000, 40, 12, CellSize{}, false, 18, 12},
		{"small sticker", 64, 64, 40, 12, CellSize{}, false, 7, 4},
		{"small upscaled", 64, 64, 40, 12, CellSize{}, true, 24, 12},
		{"custom cell", 160, 160, 40, 40, CellSize{8, 16}, false, 20, 10},
		{"degenerate", 0, 0, 10, 10, CellSize{}, false, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, r := FitCells(tt.w, tt.h, tt.mc, tt.mr, tt.cell, tt.upscale)
			if c != tt.wantCols || r != tt.wantRows {
				t.Fatalf("FitCells = %dx%d, want %dx%d", c, r, tt.wantCols, tt.wantRows)
			}
		})
	}
}

func TestResizePreservesAspect(t *testing.T) {
	src := gradient(300, 100)
	for _, q := range []Quality{Fast, High, Nearest} {
		out := ResizeToFit(src, 90, 90, q, false)
		if b := out.Bounds(); b.Dx() != 90 || b.Dy() != 30 {
			t.Fatalf("quality %d: bounds %v", q, b)
		}
	}
	// No-op when it already fits.
	if ResizeToFit(src, 1000, 1000, Fast, false) != image.Image(src) {
		t.Fatal("expected the same image back")
	}
	// Resize to the same size copies.
	same := Resize(src, 300, 100, Fast)
	if same.RGBAAt(150, 50) != src.RGBAAt(150, 50) {
		t.Fatal("same-size resize changed pixels")
	}
	// Solid colors survive resampling.
	s := Resize(solid(10, 10, color.RGBA{10, 20, 30, 255}), 3, 3, High)
	if c := s.RGBAAt(1, 1); c != (color.RGBA{10, 20, 30, 255}) {
		t.Fatalf("got %v", c)
	}
	if b := ResizeToFit(src, 0, 0, Fast, false).Bounds(); b.Dx() != 1 {
		t.Fatalf("degenerate box %v", b)
	}
}

func TestToRGBAOffsetBounds(t *testing.T) {
	src := image.NewRGBA(image.Rect(5, 5, 8, 7))
	src.Set(5, 5, color.RGBA{1, 2, 3, 255})
	out := toRGBA(src)
	if out.Bounds() != image.Rect(0, 0, 3, 2) || out.RGBAAt(0, 0) != (color.RGBA{1, 2, 3, 255}) {
		t.Fatalf("toRGBA %v %v", out.Bounds(), out.RGBAAt(0, 0))
	}
}

func TestCellSize(t *testing.T) {
	if (CellSize{}).OrDefault() != DefaultCellSize || !(CellSize{0, 5}).IsZero() || (CellSize{7, 14}).OrDefault() != (CellSize{7, 14}) {
		t.Fatal("CellSize helpers")
	}
}
