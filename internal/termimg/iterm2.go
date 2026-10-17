package termimg

import (
	"encoding/base64"
	"image"
	"strconv"
	"strings"
)

// ITerm2Encoder encodes images with the iTerm2 inline image protocol
// (OSC 1337 File=...). The image is drawn at the cursor position, so it is
// used through a [Placement] overlay.
type ITerm2Encoder struct {
	// Tmux wraps the sequence in tmux passthrough.
	Tmux bool
}

// Encode returns the OSC 1337 sequence that draws img scaled into a box of
// cols x rows cells, preserving its aspect ratio. The image is sent as PNG.
// It returns "" for a nil image or a non-positive box.
func (e ITerm2Encoder) Encode(img image.Image, cols, rows int) string {
	if img == nil || cols <= 0 || rows <= 0 {
		return ""
	}
	data, err := encodePNG(img)
	if err != nil {
		return ""
	}
	return e.EncodeData(data, cols, rows)
}

// EncodeData is like Encode but sends already encoded image data (any format
// iTerm2 understands, such as PNG, JPEG or GIF; GIFs animate natively).
func (e ITerm2Encoder) EncodeData(data []byte, cols, rows int) string {
	if len(data) == 0 || cols <= 0 || rows <= 0 {
		return ""
	}
	enc := base64.StdEncoding.EncodeToString(data)
	var b strings.Builder
	b.Grow(len(enc) + 96)
	b.WriteString("\x1b]1337;File=inline=1;size=")
	b.WriteString(strconv.Itoa(len(data)))
	b.WriteString(";width=")
	b.WriteString(strconv.Itoa(cols))
	b.WriteString(";height=")
	b.WriteString(strconv.Itoa(rows))
	b.WriteString(";preserveAspectRatio=1;doNotMoveCursor=1:")
	b.WriteString(enc)
	b.WriteString("\a")
	return wrap(b.String(), e.Tmux)
}

// Placement is an image drawn at the cursor position (iTerm2 or sixel). The
// layout reserves Cols x Rows blank cells (see [BlankRows]); after the frame
// has been rendered, the caller writes [Placement.At] for the cell where the
// reserved area starts.
type Placement struct {
	// Payload is the unpositioned image escape sequence.
	Payload string
	// Cols and Rows are the size of the reserved area in cells.
	Cols, Rows int
}

// At returns the overlay that draws the image with its top-left corner at
// zero-based cell (x, y): save cursor (DECSC), move (CUP), payload, restore
// cursor (DECRC). Only draw placements that are fully on screen; a sixel
// image reaching past the bottom edge would scroll the terminal.
func (p Placement) At(x, y int) string {
	if p.Payload == "" {
		return ""
	}
	return Overlay(p.Payload, x, y)
}

// Overlay positions payload at zero-based cell (x, y) between save and
// restore cursor sequences.
func Overlay(payload string, x, y int) string {
	return "\x1b7\x1b[" + strconv.Itoa(max(y, 0)+1) + ";" + strconv.Itoa(max(x, 0)+1) + "H" + payload + "\x1b8"
}

// BlankRows returns rows strings of cols spaces each: the area reserved in
// the layout for an overlay image.
func BlankRows(cols, rows int) []string {
	if cols <= 0 || rows <= 0 {
		return nil
	}
	line := strings.Repeat(" ", cols)
	out := make([]string, rows)
	for i := range out {
		out[i] = line
	}
	return out
}
