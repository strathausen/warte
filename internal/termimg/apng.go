package termimg

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/draw"
	"image/png"
	"time"
)

// APNG support. Signal's animated stickers are APNG files, which the
// standard image/png decoder reads as their static default image only. This
// file splits an APNG into one plain PNG per frame (IHDR resized to the
// frame, the shared ancillary chunks, the frame's IDAT/fdAT data as IDAT),
// decodes each with image/png and composites them with the frame's blend and
// dispose operations.

var pngSignature = []byte("\x89PNG\r\n\x1a\n")

type pngChunk struct {
	typ  string
	data []byte
}

// readPNGChunks splits a PNG stream into chunks without validating CRCs
// (image/png validates them when the frames are decoded).
func readPNGChunks(data []byte) ([]pngChunk, error) {
	if !bytes.HasPrefix(data, pngSignature) {
		return nil, errors.New("termimg: not a png")
	}
	var out []pngChunk
	for p := len(pngSignature); p+12 <= len(data); {
		n := binary.BigEndian.Uint32(data[p:])
		if uint64(n) > uint64(len(data)-p-12) {
			return nil, errors.New("termimg: truncated png chunk")
		}
		end := p + 8 + int(n)
		c := pngChunk{typ: string(data[p+4 : p+8]), data: data[p+8 : end]}
		out = append(out, c)
		p = end + 4
		if c.typ == "IEND" {
			break
		}
	}
	return out, nil
}

func writePNGChunk(b *bytes.Buffer, typ string, data []byte) {
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[:4], uint32(len(data)))
	copy(hdr[4:], typ)
	b.Write(hdr[:])
	b.Write(data)
	crc := crc32.NewIEEE()
	crc.Write(hdr[4:])
	crc.Write(data)
	var sum [4]byte
	binary.BigEndian.PutUint32(sum[:], crc.Sum32())
	b.Write(sum[:])
}

// APNG frame control constants.
const (
	apngDisposeNone       = 0
	apngDisposeBackground = 1
	apngDisposePrevious   = 2
	apngBlendSource       = 0
	apngBlendOver         = 1
)

type apngFrame struct {
	rect      image.Rectangle
	delay     time.Duration
	dispose   byte
	blend     byte
	data      [][]byte // IDAT payloads (fdAT with the sequence number stripped)
	isDefault bool
}

// isAPNG reports whether data is a PNG with an acTL chunk before its image
// data.
func isAPNG(data []byte) bool {
	chunks, err := readPNGChunks(data)
	if err != nil {
		return false
	}
	for _, c := range chunks {
		switch c.typ {
		case "acTL":
			return true
		case "IDAT":
			return false
		}
	}
	return false
}

// decodeAPNG decodes every frame of an animated PNG. It returns an error for
// malformed animation data; callers fall back to the static image.
func decodeAPNG(data []byte) (*Decoded, error) {
	chunks, err := readPNGChunks(data)
	if err != nil {
		return nil, err
	}
	var (
		ihdr     []byte
		shared   []pngChunk
		frames   []*apngFrame
		cur      *apngFrame
		plays    uint32
		seenIDAT bool
		hasACTL  bool
	)
	for _, c := range chunks {
		switch c.typ {
		case "IHDR":
			if len(c.data) != 13 {
				return nil, errors.New("termimg: bad IHDR")
			}
			ihdr = c.data
		case "acTL":
			if len(c.data) != 8 {
				return nil, errors.New("termimg: bad acTL")
			}
			hasACTL = true
			plays = binary.BigEndian.Uint32(c.data[4:])
		case "fcTL":
			if len(c.data) != 26 {
				return nil, errors.New("termimg: bad fcTL")
			}
			d := c.data
			w, h := binary.BigEndian.Uint32(d[4:]), binary.BigEndian.Uint32(d[8:])
			x, y := binary.BigEndian.Uint32(d[12:]), binary.BigEndian.Uint32(d[16:])
			num, den := binary.BigEndian.Uint16(d[20:]), binary.BigEndian.Uint16(d[22:])
			if w == 0 || h == 0 || w > 1<<20 || h > 1<<20 || x > 1<<20 || y > 1<<20 {
				return nil, errors.New("termimg: bad fcTL geometry")
			}
			if den == 0 {
				den = 100
			}
			cur = &apngFrame{
				rect:      image.Rect(int(x), int(y), int(x+w), int(y+h)),
				delay:     time.Duration(num) * time.Second / time.Duration(den),
				dispose:   d[24],
				blend:     d[25],
				isDefault: !seenIDAT,
			}
			frames = append(frames, cur)
		case "IDAT":
			seenIDAT = true
			// IDAT belongs to the animation only if an fcTL preceded it.
			if cur != nil && cur.isDefault {
				cur.data = append(cur.data, c.data)
			}
		case "fdAT":
			if cur == nil || len(c.data) < 4 {
				return nil, errors.New("termimg: fdAT without fcTL")
			}
			cur.data = append(cur.data, c.data[4:])
		case "IEND":
		default:
			if !seenIDAT && cur == nil {
				shared = append(shared, c)
			}
		}
	}
	if ihdr == nil || !hasACTL || len(frames) == 0 {
		return nil, errors.New("termimg: not an animated png")
	}
	cw, ch := int(binary.BigEndian.Uint32(ihdr[0:])), int(binary.BigEndian.Uint32(ihdr[4:]))
	if cw <= 0 || ch <= 0 || int64(cw)*int64(ch) > MaxPixels {
		return nil, ErrTooLarge
	}
	bounds := image.Rect(0, 0, cw, ch)
	n := min(len(frames), MaxFrames, max(1, MaxAnimationBytes/(4*cw*ch)))

	loops := 0 // 0 plays forever in both conventions
	switch {
	case plays == 1:
		loops = -1
	case plays > 1:
		loops = int(min(plays, 1<<20)) - 1
	}
	out := &Decoded{Format: "png", LoopCount: loops, Frames: make([]Frame, 0, n)}
	canvas := image.NewRGBA(bounds)
	clearRect := func(r image.Rectangle) {
		draw.Draw(canvas, r, image.Transparent, image.Point{}, draw.Src)
	}

	for i, f := range frames[:n] {
		if !f.rect.In(bounds) || len(f.data) == 0 {
			return nil, errors.New("termimg: bad apng frame")
		}
		img, err := decodeAPNGFrame(ihdr, shared, f)
		if err != nil {
			return nil, err
		}
		dispose := f.dispose
		if i == 0 && dispose == apngDisposePrevious {
			dispose = apngDisposeBackground
		}
		var prev *image.RGBA
		if dispose == apngDisposePrevious {
			prev = image.NewRGBA(bounds)
			copy(prev.Pix, canvas.Pix)
		}
		op := draw.Over
		if f.blend == apngBlendSource {
			op = draw.Src
		}
		draw.Draw(canvas, f.rect, img, img.Bounds().Min, op)

		snap := image.NewRGBA(bounds)
		copy(snap.Pix, canvas.Pix)
		delay := f.delay
		if delay < minFrameDelay {
			delay = 100 * time.Millisecond
		}
		out.Frames = append(out.Frames, Frame{Image: snap, Delay: delay})

		switch dispose {
		case apngDisposeBackground:
			clearRect(f.rect)
		case apngDisposePrevious:
			copy(canvas.Pix, prev.Pix)
		}
	}
	return out, nil
}

// decodeAPNGFrame assembles and decodes a standalone PNG for one frame.
func decodeAPNGFrame(ihdr []byte, shared []pngChunk, f *apngFrame) (image.Image, error) {
	var b bytes.Buffer
	b.Write(pngSignature)
	hdr := append([]byte(nil), ihdr...)
	binary.BigEndian.PutUint32(hdr[0:], uint32(f.rect.Dx()))
	binary.BigEndian.PutUint32(hdr[4:], uint32(f.rect.Dy()))
	writePNGChunk(&b, "IHDR", hdr)
	for _, c := range shared {
		writePNGChunk(&b, c.typ, c.data)
	}
	for _, d := range f.data {
		writePNGChunk(&b, "IDAT", d)
	}
	writePNGChunk(&b, "IEND", nil)
	return png.Decode(&b)
}
