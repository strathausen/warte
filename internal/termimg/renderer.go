package termimg

import (
	"image"
)

// Rendered is an image prepared for a cell-based layout.
type Rendered struct {
	// Rows are the strings to embed in the layout, one per terminal row,
	// each exactly Cols cells wide. Nil when the protocol is None.
	Rows []string
	// Cols and Height are the size of the image area in cells.
	Cols, Height int
	// Pre is raw output that must reach the terminal outside the render
	// string (for example via tea.Raw):
	//   - Kitty: the transmission. Write it once, before the frame that
	//     first shows the placeholders; afterwards only Rows are needed.
	//   - ITerm2/Sixel: the unpositioned image payload; use OverlayAt.
	//   - HalfBlocks/None: empty.
	Pre string
	// NeedsOverlay is true for cursor-positioned protocols (ITerm2, Sixel):
	// Rows are blanks, and OverlayAt(x, y) must be written after each frame
	// that changes the reserved area, at its top-left screen cell.
	NeedsOverlay bool
	// Animated is true when the terminal plays the animation natively
	// (kitty). Otherwise only the first frame is shown and callers may add
	// a ▶ badge.
	Animated bool
}

// OverlayAt positions the overlay payload at zero-based cell (x, y). It
// returns "" when no overlay is needed.
func (r Rendered) OverlayAt(x, y int) string {
	if !r.NeedsOverlay || r.Pre == "" {
		return ""
	}
	return Overlay(r.Pre, x, y)
}

// Renderer renders images with one protocol.
type Renderer struct {
	// Protocol is the output strategy. Auto is treated as HalfBlocks.
	Protocol Protocol
	// Cell is the cell size in pixels; zero means DefaultCellSize.
	Cell CellSize
	// Tmux wraps graphics sequences in tmux passthrough.
	Tmux bool
	// Color256 makes HalfBlocks use the 256-color palette.
	Color256 bool
	// Quality is the resampling kernel used to fit images to their box.
	Quality Quality
	// Upscale lets small images grow to fill the box (for the full-pane
	// viewer); by default images are only ever shrunk.
	Upscale bool
	// KittyFormat selects PNG or raw RGBA transmission for kitty.
	KittyFormat KittyFormat
	// Sixel configures the sixel encoder (its Tmux field is overridden).
	Sixel SixelEncoder
}

// NewRenderer returns a renderer for protocol p configured from a
// detection result.
func NewRenderer(p Protocol, d Detection, cell CellSize) *Renderer {
	if p == Auto {
		p = d.Protocol
	}
	return &Renderer{
		Protocol: p,
		Cell:     cell,
		Tmux:     d.Tmux,
		Color256: !d.TrueColor && p == HalfBlocks,
		Quality:  High,
		Sixel:    SixelEncoder{Dither: true},
	}
}

func (r *Renderer) kitty() KittyEncoder {
	return KittyEncoder{Tmux: r.Tmux, Format: r.KittyFormat, Compress: r.KittyFormat == KittyRGBA}
}

// KittyEncoder returns the kitty encoder used by the renderer (for deleting images
// or building an [Allocator]).
func (r *Renderer) KittyEncoder() KittyEncoder { return r.kitty() }

// Size returns the cell area an image of w x h pixels occupies when fitted
// into maxCols x maxRows cells. Use it to lay out before the image data is
// available (for example from attachment metadata).
func (r *Renderer) Size(w, h, maxCols, maxRows int) (cols, rows int) {
	return FitCells(w, h, maxCols, maxRows, r.Cell, r.Upscale)
}

// Render prepares img for display within maxCols x maxRows cells. The
// returned area preserves the image's aspect ratio and may be smaller than
// the box. id is the kitty image id (ignored by other protocols); it must be
// nonzero for kitty, see [Allocator].
func (r *Renderer) Render(id uint32, img image.Image, maxCols, maxRows int) Rendered {
	if img == nil || img.Bounds().Empty() || maxCols <= 0 || maxRows <= 0 {
		return Rendered{}
	}
	return r.render(id, []Frame{{Image: img}}, 0, maxCols, maxRows)
}

// RenderDecoded is like Render but animates multi-frame images where the
// protocol supports it (kitty). Other protocols show the first frame.
func (r *Renderer) RenderDecoded(id uint32, d *Decoded, maxCols, maxRows int) Rendered {
	if d == nil || len(d.Frames) == 0 || maxCols <= 0 || maxRows <= 0 {
		return Rendered{}
	}
	return r.render(id, d.Frames, d.LoopCount, maxCols, maxRows)
}

func (r *Renderer) render(id uint32, frames []Frame, loops, maxCols, maxRows int) Rendered {
	cell := r.Cell.OrDefault()
	img := frames[0].Image
	b := img.Bounds()
	cols, rows := FitCells(b.Dx(), b.Dy(), maxCols, maxRows, cell, r.Upscale)
	if cols == 0 || rows == 0 {
		return Rendered{}
	}
	pixelBox := func(im image.Image) image.Image {
		return ResizeToFit(im, cols*cell.Width, rows*cell.Height, r.Quality, r.Upscale)
	}
	out := Rendered{Cols: cols, Height: rows}

	switch r.Protocol {
	case Kitty:
		if id == 0 {
			return Rendered{}
		}
		k := r.kitty()
		out.Rows = k.Placeholder(id, cols, rows)
		if len(frames) > 1 {
			scaled := make([]Frame, len(frames))
			for i, f := range frames {
				scaled[i] = Frame{Image: ResizeToFit(f.Image, cols*cell.Width, rows*cell.Height, Fast, r.Upscale), Delay: f.Delay}
			}
			out.Pre = k.TransmitAnimation(id, scaled, loops, cols, rows)
			out.Animated = true
		} else {
			out.Pre = k.Transmit(id, pixelBox(img), cols, rows)
		}
	case ITerm2:
		out.Rows = BlankRows(cols, rows)
		out.Pre = ITerm2Encoder{Tmux: r.Tmux}.Encode(pixelBox(img), cols, rows)
		out.NeedsOverlay = true
	case Sixel:
		enc := r.Sixel
		enc.Tmux = r.Tmux
		out.Rows = BlankRows(cols, rows)
		out.Pre = enc.Encode(pixelBox(img))
		out.NeedsOverlay = true
	case None:
		return Rendered{}
	default: // HalfBlocks, Auto
		out.Rows = HalfBlockRenderer{Color256: r.Color256, Quality: r.Quality}.Render(img, cols, rows)
	}
	return out
}

// Placeholder returns the kitty placeholder rows for an image that has
// already been transmitted with the same size, avoiding a re-render. It
// returns nil for other protocols.
func (r *Renderer) Placeholder(id uint32, cols, rows int) []string {
	if r.Protocol != Kitty {
		return nil
	}
	return r.kitty().Placeholder(id, cols, rows)
}

// Delete returns the sequence that frees kitty image id ("" for other
// protocols, whose images live only on screen).
func (r *Renderer) Delete(id uint32) string {
	if r.Protocol != Kitty || id == 0 {
		return ""
	}
	return r.kitty().Delete(id)
}
