package termimg

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"strconv"
	"strings"
	"time"
)

// PlaceholderRune is the kitty Unicode placeholder character. Cells holding
// it (plus row/column diacritics) display part of a virtual image placement.
const PlaceholderRune = '\U0010EEEE'

// KittyChunkSize is the maximum base64 payload per graphics command.
const KittyChunkSize = 4096

// KittyFormat selects how pixel data is transmitted.
type KittyFormat int

const (
	// KittyPNG sends PNG data (f=100). Smallest on the wire for most
	// pictures and the default.
	KittyPNG KittyFormat = iota
	// KittyRGBA sends raw 32-bit RGBA pixels (f=32), optionally
	// zlib-compressed. Cheaper to encode, which helps animations.
	KittyRGBA
)

// AnimationState is the kitty animation playback state (the s key of a=a).
type AnimationState int

const (
	// AnimationStop stops the animation.
	AnimationStop AnimationState = 1
	// AnimationLoading runs the animation but waits for more frames at the
	// end instead of looping.
	AnimationLoading AnimationState = 2
	// AnimationRun runs the animation normally, looping as configured.
	AnimationRun AnimationState = 3
)

// KittyEncoder encodes images for the kitty graphics protocol using Unicode
// placeholders. The zero value sends PNG without tmux passthrough.
//
// The workflow is:
//
//  1. Write the result of [KittyEncoder.Transmit] (or [KittyEncoder.TransmitAnimation])
//     to the terminal once, outside the TUI's render string (with Bubble Tea
//     v2: tea.Raw). It uploads the image and creates a virtual placement of
//     cols x rows cells; nothing is drawn yet.
//  2. Embed the rows from [KittyEncoder.Placeholder] in the layout wherever the
//     image should appear. They are plain text cells, so the image moves,
//     scrolls and clips with them.
//  3. When the image is no longer needed, write [KittyEncoder.Delete].
//
// Width: each placeholder cell measures exactly one cell with
// github.com/charmbracelet/x/ansi.StringWidth and lipgloss.Width, because
// U+10EEEE is a narrow private-use character and the diacritics are zero
// width combining marks within the same grapheme cluster. Every cell carries
// explicit row and column diacritics, so the cells of a row are all distinct
// and a renderer cannot collapse them with REP; that also keeps the image
// correct when the row is clipped on the left.
type KittyEncoder struct {
	// Tmux wraps every command in tmux passthrough.
	Tmux bool
	// Format selects PNG (default) or raw RGBA transmission.
	Format KittyFormat
	// Compress zlib-compresses raw RGBA payloads (o=z). Ignored for PNG.
	Compress bool
}

// Transmit returns the escape sequence that uploads img under id and creates
// a virtual placement (U=1) spanning cols x rows cells. Kitty scales the
// image into that area preserving its aspect ratio, so callers should size
// img to roughly cols x rows cells of pixels to save bandwidth (see
// [Renderer]). Commands are sent with q=2 so the terminal stays silent.
// It returns "" for id 0, a nil image or a non-positive area.
func (k KittyEncoder) Transmit(id uint32, img image.Image, cols, rows int) string {
	if id == 0 || img == nil || cols <= 0 || rows <= 0 {
		return ""
	}
	cols, rows = min(cols, MaxPlaceholderCells), min(rows, MaxPlaceholderCells)
	keys, payload, err := k.encode(img)
	if err != nil {
		return ""
	}
	ctrl := fmt.Sprintf("a=T,U=1,i=%d,t=d,q=2,c=%d,r=%d,%s", id, cols, rows, keys)
	return k.chunked(ctrl, payload)
}

// TransmitAnimation uploads an animation: the first frame exactly like
// [KittyEncoder.Transmit], then every further frame with a=f, and finally starts
// playback with a=a. loopCount follows image/gif semantics (0 loops forever,
// -1 plays once, n plays n+1 times). All frames must have the same size.
func (k KittyEncoder) TransmitAnimation(id uint32, frames []Frame, loopCount, cols, rows int) string {
	if len(frames) == 0 {
		return ""
	}
	first := k.Transmit(id, frames[0].Image, cols, rows)
	if first == "" || len(frames) == 1 {
		return first
	}
	var b strings.Builder
	b.WriteString(first)
	b.WriteString(k.cmd(fmt.Sprintf("a=a,i=%d,r=1,z=%d,q=2", id, ms(frames[0].Delay)), ""))
	for _, f := range frames[1:] {
		keys, payload, err := k.encode(f.Image)
		if err != nil {
			continue
		}
		ctrl := fmt.Sprintf("a=f,i=%d,z=%d,q=2,%s", id, ms(f.Delay), keys)
		b.WriteString(k.chunked(ctrl, payload))
	}
	b.WriteString(k.AnimationControl(id, AnimationRun, loopCount))
	return b.String()
}

// AnimationControl changes the playback state of an animation. loopCount
// follows image/gif semantics and is translated to kitty's v key (1 means
// infinite, n means n-1 loops).
func (k KittyEncoder) AnimationControl(id uint32, state AnimationState, loopCount int) string {
	v := 1
	switch {
	case loopCount < 0:
		v = 2
	case loopCount > 0:
		v = loopCount + 2
	}
	return k.cmd(fmt.Sprintf("a=a,i=%d,s=%d,v=%d,q=2", id, int(state), v), "")
}

// Delete returns the sequence that deletes all placements of image id and
// frees its data (d=I).
func (k KittyEncoder) Delete(id uint32) string {
	return k.cmd(fmt.Sprintf("a=d,d=I,i=%d,q=2", id), "")
}

// DeleteAll deletes every image and placement visible to the terminal and
// frees their data (d=A). Useful on shutdown.
func (k KittyEncoder) DeleteAll() string { return k.cmd("a=d,d=A,q=2", "") }

// Placeholder returns one string per row of Unicode placeholder cells for
// image id, each exactly cols cells wide, ready to embed in a layout. The
// image id is encoded in the foreground color: 256-color for ids 16..255,
// 24-bit otherwise, with the most significant byte (if any) as a third
// diacritic. Each row resets the foreground at its end. cols and rows are
// clamped to [MaxPlaceholderCells].
func (k KittyEncoder) Placeholder(id uint32, cols, rows int) []string {
	if id == 0 || cols <= 0 || rows <= 0 {
		return nil
	}
	cols, rows = min(cols, MaxPlaceholderCells), min(rows, MaxPlaceholderCells)
	fg := idColor(id)
	var msb string
	if hi := id >> 24; hi != 0 {
		msb = string(diacritics[hi])
	}
	out := make([]string, rows)
	var b strings.Builder
	for r := range rows {
		b.Reset()
		b.Grow(len(fg) + cols*12 + 5)
		b.WriteString(fg)
		rd := diacritics[r]
		for c := range cols {
			b.WriteRune(PlaceholderRune)
			b.WriteRune(rd)
			b.WriteRune(diacritics[c])
			b.WriteString(msb)
		}
		b.WriteString("\x1b[39m")
		out[r] = b.String()
	}
	return out
}

// idColor returns the SGR sequence that encodes id in the foreground color.
// Ids below 16 use 24-bit color too, because renderers may rewrite indexed
// colors 0-15 as basic ANSI colors (SGR 30-37/90-97).
func idColor(id uint32) string {
	if id >= 16 && id <= 255 {
		return "\x1b[38;5;" + strconv.Itoa(int(id)) + "m"
	}
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", (id>>16)&0xff, (id>>8)&0xff, id&0xff)
}

// encode returns the format keys and the raw (not yet base64) payload.
func (k KittyEncoder) encode(img image.Image) (string, []byte, error) {
	if k.Format == KittyRGBA {
		rgba := toRGBA(img)
		w, h := rgba.Bounds().Dx(), rgba.Bounds().Dy()
		pix := make([]byte, 0, w*h*4)
		for y := range h {
			off := y * rgba.Stride
			pix = append(pix, unpremultiply(rgba.Pix[off:off+w*4])...)
		}
		keys := fmt.Sprintf("f=32,s=%d,v=%d", w, h)
		if k.Compress {
			var buf bytes.Buffer
			zw := zlib.NewWriter(&buf)
			if _, err := zw.Write(pix); err != nil {
				return "", nil, err
			}
			if err := zw.Close(); err != nil {
				return "", nil, err
			}
			return keys + ",o=z", buf.Bytes(), nil
		}
		return keys, pix, nil
	}
	data, err := encodePNG(img)
	return "f=100", data, err
}

// unpremultiply converts premultiplied RGBA (image.RGBA) to straight alpha,
// which is what kitty expects for f=32.
func unpremultiply(p []byte) []byte {
	out := make([]byte, len(p))
	for i := 0; i+3 < len(p); i += 4 {
		a := p[i+3]
		switch a {
		case 0:
		case 255:
			copy(out[i:i+4], p[i:i+4])
		default:
			out[i] = byte(min(255, int(p[i])*255/int(a)))
			out[i+1] = byte(min(255, int(p[i+1])*255/int(a)))
			out[i+2] = byte(min(255, int(p[i+2])*255/int(a)))
			out[i+3] = a
		}
	}
	return out
}

var pngEncoder = png.Encoder{CompressionLevel: png.BestSpeed}

func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := pngEncoder.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// chunked base64-encodes payload and splits it into commands of at most
// KittyChunkSize base64 bytes. The first command carries ctrl; continuation
// commands carry only m (and q=2 to keep the terminal quiet).
func (k KittyEncoder) chunked(ctrl string, payload []byte) string {
	enc := base64.StdEncoding.EncodeToString(payload)
	var b strings.Builder
	b.Grow(len(enc) + (len(enc)/KittyChunkSize+1)*(len(ctrl)+24))
	if len(enc) <= KittyChunkSize {
		b.WriteString(k.cmd(ctrl, enc))
		return b.String()
	}
	for off := 0; off < len(enc); off += KittyChunkSize {
		end := min(off+KittyChunkSize, len(enc))
		more := 0
		if end < len(enc) {
			more = 1
		}
		var c string
		if off == 0 {
			c = ctrl + ",m=1"
		} else {
			c = "m=" + strconv.Itoa(more) + ",q=2"
		}
		b.WriteString(k.cmd(c, enc[off:end]))
	}
	return b.String()
}

// cmd builds one APC graphics command, wrapped for tmux if needed.
func (k KittyEncoder) cmd(ctrl, payload string) string {
	s := "\x1b_G" + ctrl
	if payload != "" {
		s += ";" + payload
	}
	s += "\x1b\\"
	return wrap(s, k.Tmux)
}

func ms(d time.Duration) int {
	return max(int(d/time.Millisecond), 1)
}
