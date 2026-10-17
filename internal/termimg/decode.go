package termimg

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	_ "image/jpeg" // register JPEG
	_ "image/png"  // register PNG
	"time"

	_ "golang.org/x/image/webp" // register WebP
)

// MaxPixels bounds the decoded size of an image (width*height, per frame) to
// protect against decompression bombs.
const MaxPixels = 64 << 20

// MaxFrames bounds the number of GIF frames kept in memory.
const MaxFrames = 1000

// MaxAnimationBytes bounds the memory used by composited GIF frames; frames
// beyond the budget are dropped.
const MaxAnimationBytes = 256 << 20

// ErrTooLarge is returned when an image exceeds [MaxPixels].
var ErrTooLarge = errors.New("termimg: image dimensions too large")

// minFrameDelay is applied to GIF frames that declare a zero or tiny delay,
// matching what browsers do.
const minFrameDelay = 20 * time.Millisecond

// Frame is one fully composited frame of an animation.
type Frame struct {
	Image image.Image
	// Delay is how long the frame stays on screen.
	Delay time.Duration
}

// Decoded is a decoded image with all of its frames.
type Decoded struct {
	// Format is the registered format name ("png", "jpeg", "gif", "webp").
	Format string
	// Frames holds at least one frame. Every frame has the same bounds
	// (GIF sub-frames are composited onto the full canvas).
	Frames []Frame
	// LoopCount follows image/gif: 0 loops forever, -1 plays once, n plays
	// n+1 times.
	LoopCount int
}

// First returns the first frame.
func (d *Decoded) First() image.Image { return d.Frames[0].Image }

// Animated reports whether there is more than one frame.
func (d *Decoded) Animated() bool { return len(d.Frames) > 1 }

// Bounds returns the bounds shared by all frames.
func (d *Decoded) Bounds() image.Rectangle { return d.Frames[0].Image.Bounds() }

// Decode decodes PNG, JPEG, GIF (all frames, composited with their disposal
// methods), APNG (all frames, composited with their blend and dispose
// operations) and WebP data. JPEGs are rotated upright according to their
// EXIF orientation. Animated WebP is not supported by golang.org/x/image/webp
// and fails to decode.
//
// Decoding is bounded: frames larger than [MaxPixels] are rejected, and
// animations keep at most [MaxFrames] frames within [MaxAnimationBytes].
func Decode(data []byte) (*Decoded, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("termimg: decode: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > MaxPixels {
		return nil, ErrTooLarge
	}
	if format == "gif" {
		budget := min(MaxFrames, max(1, MaxAnimationBytes/(4*cfg.Width*cfg.Height)))
		g, err := gif.DecodeAll(bytes.NewReader(limitGIF(data, budget, MaxAnimationBytes)))
		if err != nil {
			return nil, fmt.Errorf("termimg: decode gif: %w", err)
		}
		return compositeGIF(g)
	}
	if format == "png" && isAPNG(data) {
		if d, err := decodeAPNG(data); err == nil {
			return d, nil
		}
		// Malformed animation data: fall back to the static default image.
	}
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("termimg: decode %s: %w", format, err)
	}
	if format == "jpeg" {
		img = applyOrientation(img, jpegOrientation(data))
	}
	return &Decoded{Format: format, Frames: []Frame{{Image: img}}}, nil
}

// DecodeImage decodes data and returns only its first frame.
func DecodeImage(data []byte) (image.Image, error) {
	d, err := Decode(data)
	if err != nil {
		return nil, err
	}
	return d.First(), nil
}

// compositeGIF renders each GIF sub-frame onto a canvas the size of the
// logical screen, honouring the disposal methods, and returns full frames.
func compositeGIF(g *gif.GIF) (*Decoded, error) {
	if len(g.Image) == 0 {
		return nil, errors.New("termimg: gif has no frames")
	}
	w, h := g.Config.Width, g.Config.Height
	if w == 0 || h == 0 {
		b := g.Image[0].Bounds()
		w, h = b.Max.X, b.Max.Y
	}
	bounds := image.Rect(0, 0, w, h)
	canvas := image.NewRGBA(bounds)
	n := min(len(g.Image), MaxFrames, max(1, MaxAnimationBytes/(4*w*h)))
	out := &Decoded{Format: "gif", LoopCount: g.LoopCount, Frames: make([]Frame, 0, n)}

	var previous *image.RGBA
	for i := range n {
		fr := g.Image[i]
		disposal := byte(0)
		if i < len(g.Disposal) {
			disposal = g.Disposal[i]
		}
		if disposal == gif.DisposalPrevious {
			previous = image.NewRGBA(bounds)
			copy(previous.Pix, canvas.Pix)
		}
		draw.Draw(canvas, fr.Bounds().Intersect(bounds), fr, fr.Bounds().Min, draw.Over)

		snap := image.NewRGBA(bounds)
		copy(snap.Pix, canvas.Pix)
		delay := time.Duration(0)
		if i < len(g.Delay) {
			delay = time.Duration(g.Delay[i]) * 10 * time.Millisecond
		}
		if delay < minFrameDelay {
			delay = 100 * time.Millisecond
		}
		out.Frames = append(out.Frames, Frame{Image: snap, Delay: delay})

		switch disposal {
		case gif.DisposalBackground:
			draw.Draw(canvas, fr.Bounds().Intersect(bounds), image.NewUniform(color.Transparent), image.Point{}, draw.Src)
		case gif.DisposalPrevious:
			if previous != nil {
				copy(canvas.Pix, previous.Pix)
			}
		}
	}
	return out, nil
}
