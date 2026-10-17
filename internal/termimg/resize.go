package termimg

import (
	"image"
	"image/draw"

	xdraw "golang.org/x/image/draw"
)

// CellSize is the size of one terminal cell in pixels.
type CellSize struct {
	Width, Height int
}

// DefaultCellSize is assumed when the terminal does not report its cell size.
var DefaultCellSize = CellSize{Width: 10, Height: 20}

// IsZero reports whether the size is unset or invalid.
func (c CellSize) IsZero() bool { return c.Width <= 0 || c.Height <= 0 }

// OrDefault returns c, or [DefaultCellSize] when c is unset.
func (c CellSize) OrDefault() CellSize {
	if c.IsZero() {
		return DefaultCellSize
	}
	return c
}

// Quality selects the resampling kernel.
type Quality int

const (
	// Fast uses approximate bilinear filtering; good for thumbnails and
	// per-frame animation work.
	Fast Quality = iota
	// High uses Catmull-Rom; best for still images and the full viewer.
	High
	// Nearest uses nearest-neighbour; for pixel art and tests.
	Nearest
)

func (q Quality) scaler() xdraw.Scaler {
	switch q {
	case High:
		return xdraw.CatmullRom
	case Nearest:
		return xdraw.NearestNeighbor
	default:
		return xdraw.ApproxBiLinear
	}
}

// Fit scales (srcW, srcH) to the largest size that fits within (maxW, maxH)
// while preserving the aspect ratio. With upscale false the result never
// exceeds the source size. Each dimension is at least 1 when the inputs are
// positive; zero is returned for degenerate input.
func Fit(srcW, srcH, maxW, maxH int, upscale bool) (w, h int) {
	if srcW <= 0 || srcH <= 0 || maxW <= 0 || maxH <= 0 {
		return 0, 0
	}
	if !upscale && srcW <= maxW && srcH <= maxH {
		return srcW, srcH
	}
	// Compare srcW/srcH with maxW/maxH using integers.
	if int64(srcW)*int64(maxH) >= int64(srcH)*int64(maxW) {
		w = maxW
		h = int((int64(srcH)*int64(maxW) + int64(srcW)/2) / int64(srcW))
	} else {
		h = maxH
		w = int((int64(srcW)*int64(maxH) + int64(srcH)/2) / int64(srcH))
	}
	return max(w, 1), max(h, 1)
}

// FitCells returns the number of cells an image of srcW x srcH pixels
// occupies when scaled to fit within maxCols x maxRows cells of the given
// size, preserving the aspect ratio. It is the layout counterpart of
// [Renderer.Render].
func FitCells(srcW, srcH, maxCols, maxRows int, cell CellSize, upscale bool) (cols, rows int) {
	cell = cell.OrDefault()
	w, h := Fit(srcW, srcH, maxCols*cell.Width, maxRows*cell.Height, upscale)
	if w == 0 || h == 0 {
		return 0, 0
	}
	cols = min(max((w+cell.Width-1)/cell.Width, 1), maxCols)
	rows = min(max((h+cell.Height-1)/cell.Height, 1), maxRows)
	return cols, rows
}

// Resize scales img to exactly w x h pixels.
func Resize(img image.Image, w, h int, q Quality) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, max(w, 1), max(h, 1)))
	b := img.Bounds()
	if b.Dx() == w && b.Dy() == h {
		draw.Draw(dst, dst.Bounds(), img, b.Min, draw.Src)
		return dst
	}
	q.scaler().Scale(dst, dst.Bounds(), img, b, xdraw.Src, nil)
	return dst
}

// ResizeToFit scales img to fit within maxW x maxH pixels, preserving its
// aspect ratio. Without upscale, images that already fit are returned as is.
func ResizeToFit(img image.Image, maxW, maxH int, q Quality, upscale bool) image.Image {
	b := img.Bounds()
	w, h := Fit(b.Dx(), b.Dy(), maxW, maxH, upscale)
	if w == 0 || h == 0 {
		return image.NewRGBA(image.Rect(0, 0, 1, 1))
	}
	if w == b.Dx() && h == b.Dy() {
		return img
	}
	return Resize(img, w, h, q)
}

// toRGBA returns img as an *image.RGBA anchored at the origin, converting if
// needed.
func toRGBA(img image.Image) *image.RGBA {
	if r, ok := img.(*image.RGBA); ok && r.Bounds().Min == (image.Point{}) {
		return r
	}
	b := img.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), img, b.Min, draw.Src)
	return dst
}
