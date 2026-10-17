package termimg

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"io"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/kitty"
)

func TestDiacriticTable(t *testing.T) {
	if MaxPlaceholderCells != 297 || len(diacritics) != 297 {
		t.Fatalf("table length %d, want 297", len(diacritics))
	}
	first := []rune{0x0305, 0x030D, 0x030E, 0x0310, 0x0312, 0x033D, 0x033E, 0x033F, 0x0346, 0x034A, 0x034B, 0x034C, 0x0350, 0x0351, 0x0352, 0x0357, 0x035B, 0x0363}
	for i, r := range first {
		if Diacritic(i) != r {
			t.Errorf("Diacritic(%d) = %U, want %U", i, Diacritic(i), r)
		}
	}
	if got := Diacritic(296); got != 0x1D244 {
		t.Errorf("last = %U, want U+1D244", got)
	}
	for i := 1; i < len(diacritics); i++ {
		if diacritics[i] <= diacritics[i-1] {
			t.Fatalf("table not strictly increasing at %d", i)
		}
	}
	// Cross-check with the independent copy in charmbracelet/x/ansi.
	for i := range diacritics {
		if kitty.Diacritic(i) != diacritics[i] {
			t.Fatalf("entry %d: %U, x/ansi has %U", i, diacritics[i], kitty.Diacritic(i))
		}
		if DiacriticValue(diacritics[i]) != i {
			t.Fatalf("DiacriticValue(%U) != %d", diacritics[i], i)
		}
	}
	if DiacriticValue('a') != -1 {
		t.Error("DiacriticValue of non-diacritic")
	}
}

type cell struct{ row, col, msb int }

// parsePlaceholderRow splits a placeholder row into its color prefix and
// decoded cells.
func parsePlaceholderRow(t *testing.T, s string) (sgr string, cells []cell) {
	t.Helper()
	if !strings.HasPrefix(s, "\x1b[38;") {
		t.Fatalf("row does not start with fg color: %q", s)
	}
	m := strings.IndexByte(s, 'm')
	sgr, s = s[:m+1], s[m+1:]
	if !strings.HasSuffix(s, "\x1b[39m") {
		t.Fatalf("row does not reset fg: %q", s)
	}
	s = strings.TrimSuffix(s, "\x1b[39m")
	rs := []rune(s)
	for i := 0; i < len(rs); {
		if rs[i] != PlaceholderRune {
			t.Fatalf("expected placeholder at %d, got %U", i, rs[i])
		}
		c := cell{row: DiacriticValue(rs[i+1]), col: DiacriticValue(rs[i+2]), msb: -1}
		i += 3
		if i < len(rs) && rs[i] != PlaceholderRune {
			c.msb = DiacriticValue(rs[i])
			i++
		}
		cells = append(cells, c)
	}
	return sgr, cells
}

func TestPlaceholder(t *testing.T) {
	tests := []struct {
		name       string
		id         uint32
		cols, rows int
		sgr        string
		msb        int
	}{
		{"small id 256-color", 42, 3, 2, "\x1b[38;5;42m", -1},
		{"tiny id truecolor", 5, 2, 2, "\x1b[38;2;0;0;5m", -1},
		{"24-bit id", 0x123456, 4, 3, "\x1b[38;2;18;52;86m", -1},
		{"id with msb", 0x07000102, 2, 1, "\x1b[38;2;0;1;2m", 7},
		{"wide", 300, 40, 1, "\x1b[38;2;0;1;44m", -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows := KittyEncoder{}.Placeholder(tt.id, tt.cols, tt.rows)
			if len(rows) != tt.rows {
				t.Fatalf("got %d rows, want %d", len(rows), tt.rows)
			}
			for r, row := range rows {
				if w := ansi.StringWidth(row); w != tt.cols {
					t.Errorf("row %d ansi width %d, want %d", r, w, tt.cols)
				}
				if w := lipgloss.Width(row); w != tt.cols {
					t.Errorf("row %d lipgloss width %d, want %d", r, w, tt.cols)
				}
				sgr, cells := parsePlaceholderRow(t, row)
				if sgr != tt.sgr {
					t.Errorf("sgr %q, want %q", sgr, tt.sgr)
				}
				if len(cells) != tt.cols {
					t.Fatalf("cells %d, want %d", len(cells), tt.cols)
				}
				for c, got := range cells {
					if got != (cell{r, c, tt.msb}) {
						t.Errorf("cell (%d,%d) decoded as %+v", r, c, got)
					}
				}
			}
		})
	}
}

func TestPlaceholderInLayout(t *testing.T) {
	rows := KittyEncoder{}.Placeholder(100, 6, 3)
	block := lipgloss.JoinVertical(lipgloss.Left, rows...)
	boxed := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Render(block)
	if w := lipgloss.Width(boxed); w != 8 {
		t.Fatalf("bordered width %d, want 8", w)
	}
	if h := lipgloss.Height(boxed); h != 5 {
		t.Fatalf("bordered height %d, want 5", h)
	}
	joined := lipgloss.JoinHorizontal(lipgloss.Top, block, " caption")
	if w := lipgloss.Width(joined); w != 14 {
		t.Fatalf("joined width %d, want 14", w)
	}
}

func TestPlaceholderEdgeCases(t *testing.T) {
	k := KittyEncoder{}
	if k.Placeholder(0, 2, 2) != nil || k.Placeholder(1, 0, 2) != nil || k.Placeholder(1, 2, -1) != nil {
		t.Fatal("expected nil for invalid input")
	}
	rows := k.Placeholder(20, 400, 400)
	if len(rows) != MaxPlaceholderCells || ansi.StringWidth(rows[0]) != MaxPlaceholderCells {
		t.Fatal("expected clamping to 297")
	}
}

// apc is one parsed kitty graphics command.
type apc struct {
	keys    map[string]string
	order   []string
	payload string
}

func parseAPCs(t *testing.T, s string) []apc {
	t.Helper()
	var out []apc
	for s != "" {
		if !strings.HasPrefix(s, "\x1b_G") {
			t.Fatalf("expected APC, got %q", s[:min(len(s), 20)])
		}
		end := strings.Index(s, "\x1b\\")
		if end < 0 {
			t.Fatal("unterminated APC")
		}
		body := s[3:end]
		s = s[end+2:]
		ctrl, payload, _ := strings.Cut(body, ";")
		a := apc{keys: map[string]string{}, payload: payload}
		for kv := range strings.SplitSeq(ctrl, ",") {
			k, v, _ := strings.Cut(kv, "=")
			a.keys[k] = v
			a.order = append(a.order, k)
		}
		out = append(out, a)
	}
	return out
}

func TestTransmitChunking(t *testing.T) {
	// A noisy image so the PNG spans several chunks.
	img := image.NewRGBA(image.Rect(0, 0, 160, 120))
	seed := uint32(1)
	for i := range img.Pix {
		seed = seed*1664525 + 1013904223
		img.Pix[i] = byte(seed >> 24)
	}
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 255
	}
	out := KittyEncoder{}.Transmit(77, img, 16, 6)
	cmds := parseAPCs(t, out)
	if len(cmds) < 3 {
		t.Fatalf("expected several chunks, got %d", len(cmds))
	}
	first := cmds[0]
	for k, v := range map[string]string{"a": "T", "U": "1", "i": "77", "f": "100", "t": "d", "q": "2", "c": "16", "r": "6", "m": "1"} {
		if first.keys[k] != v {
			t.Errorf("first chunk %s=%q, want %q", k, first.keys[k], v)
		}
	}
	var all strings.Builder
	for i, c := range cmds {
		if len(c.payload) > KittyChunkSize || len(c.payload)%4 != 0 {
			t.Errorf("chunk %d payload length %d", i, len(c.payload))
		}
		want := "1"
		if i == len(cmds)-1 {
			want = "0"
		}
		if c.keys["m"] != want {
			t.Errorf("chunk %d m=%q, want %q", i, c.keys["m"], want)
		}
		if i > 0 {
			if _, ok := c.keys["a"]; ok {
				t.Errorf("continuation chunk %d repeats control keys", i)
			}
			if i < len(cmds)-1 && len(c.payload) != KittyChunkSize {
				t.Errorf("middle chunk %d not full", i)
			}
		}
		all.WriteString(c.payload)
	}
	data, err := base64.StdEncoding.DecodeString(all.String())
	if err != nil {
		t.Fatal(err)
	}
	dec, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if dec.Bounds() != img.Bounds() {
		t.Fatalf("decoded bounds %v", dec.Bounds())
	}
	if r, g, b, _ := dec.At(5, 7).RGBA(); [3]uint32{r >> 8, g >> 8, b >> 8} != [3]uint32{uint32(img.Pix[img.PixOffset(5, 7)]), uint32(img.Pix[img.PixOffset(5, 7)+1]), uint32(img.Pix[img.PixOffset(5, 7)+2])} {
		t.Fatal("pixel mismatch after round trip")
	}
}

func TestTransmitSingleChunk(t *testing.T) {
	out := KittyEncoder{}.Transmit(9, solid(4, 4, color.White), 1, 1)
	cmds := parseAPCs(t, out)
	if len(cmds) != 1 {
		t.Fatalf("got %d commands", len(cmds))
	}
	if _, ok := cmds[0].keys["m"]; ok {
		t.Error("single chunk should not carry m")
	}
}

func TestTransmitRGBA(t *testing.T) {
	img := solid(3, 2, color.NRGBA{200, 100, 50, 128})
	for _, compress := range []bool{false, true} {
		out := KittyEncoder{Format: KittyRGBA, Compress: compress}.Transmit(300, img, 1, 1)
		cmds := parseAPCs(t, out)
		c := cmds[0]
		if c.keys["f"] != "32" || c.keys["s"] != "3" || c.keys["v"] != "2" {
			t.Fatalf("keys %v", c.keys)
		}
		raw, err := base64.StdEncoding.DecodeString(c.payload)
		if err != nil {
			t.Fatal(err)
		}
		if compress {
			if c.keys["o"] != "z" {
				t.Fatal("missing o=z")
			}
			zr, err := zlib.NewReader(bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			if raw, err = io.ReadAll(zr); err != nil {
				t.Fatal(err)
			}
		}
		if len(raw) != 3*2*4 {
			t.Fatalf("raw length %d", len(raw))
		}
		// Straight (non-premultiplied) alpha, within rounding.
		if d := int(raw[0]) - 200; d < -2 || d > 2 || raw[3] != 128 {
			t.Fatalf("pixel %v", raw[:4])
		}
	}
}

func TestTransmitInvalid(t *testing.T) {
	k := KittyEncoder{}
	img := solid(2, 2, color.Black)
	if k.Transmit(0, img, 1, 1) != "" || k.Transmit(1, nil, 1, 1) != "" || k.Transmit(1, img, 0, 1) != "" {
		t.Fatal("expected empty output")
	}
}

func TestTransmitTmux(t *testing.T) {
	out := KittyEncoder{Tmux: true}.Transmit(20, solid(2, 2, color.Black), 1, 1)
	if !strings.HasPrefix(out, "\x1bPtmux;\x1b\x1b_G") || !strings.HasSuffix(out, "\x1b\x1b\\\x1b\\") {
		t.Fatalf("not wrapped: %q", out[:min(len(out), 40)])
	}
	if got := (KittyEncoder{Tmux: true}).Delete(20); got != TmuxPassthrough("\x1b_Ga=d,d=I,i=20,q=2\x1b\\") {
		t.Fatalf("delete %q", got)
	}
}

func TestDelete(t *testing.T) {
	k := KittyEncoder{}
	if got := k.Delete(42); got != "\x1b_Ga=d,d=I,i=42,q=2\x1b\\" {
		t.Fatalf("Delete = %q", got)
	}
	if got := k.DeleteAll(); got != "\x1b_Ga=d,d=A,q=2\x1b\\" {
		t.Fatalf("DeleteAll = %q", got)
	}
}

func TestTransmitAnimation(t *testing.T) {
	frames := []Frame{
		{Image: solid(4, 4, color.RGBA{255, 0, 0, 255}), Delay: 100 * time.Millisecond},
		{Image: solid(4, 4, color.RGBA{0, 255, 0, 255}), Delay: 50 * time.Millisecond},
		{Image: solid(4, 4, color.RGBA{0, 0, 255, 255}), Delay: 0},
	}
	cmds := parseAPCs(t, KittyEncoder{}.TransmitAnimation(55, frames, 0, 2, 2))
	if len(cmds) != 5 {
		t.Fatalf("got %d commands, want 5", len(cmds))
	}
	if cmds[0].keys["a"] != "T" || cmds[0].keys["U"] != "1" {
		t.Error("first command must transmit with a virtual placement")
	}
	if c := cmds[1]; c.keys["a"] != "a" || c.keys["r"] != "1" || c.keys["z"] != "100" {
		t.Errorf("root frame gap %v", c.keys)
	}
	for i, wantZ := range []string{"50", "1"} {
		c := cmds[2+i]
		if c.keys["a"] != "f" || c.keys["i"] != "55" || c.keys["z"] != wantZ || c.payload == "" {
			t.Errorf("frame %d keys %v", i+2, c.keys)
		}
	}
	if c := cmds[4]; c.keys["a"] != "a" || c.keys["s"] != "3" || c.keys["v"] != "1" {
		t.Errorf("start %v", c.keys)
	}
	// Single frame degenerates to Transmit.
	if got := (KittyEncoder{}).TransmitAnimation(55, frames[:1], 0, 2, 2); got != (KittyEncoder{}).Transmit(55, frames[0].Image, 2, 2) {
		t.Error("single-frame animation should equal Transmit")
	}
	if (KittyEncoder{}).TransmitAnimation(55, nil, 0, 2, 2) != "" {
		t.Error("no frames should be empty")
	}
}

func TestAnimationControlLoops(t *testing.T) {
	tests := []struct {
		loops int
		v     string
	}{{0, "1"}, {-1, "2"}, {1, "3"}, {4, "6"}}
	for _, tt := range tests {
		c := parseAPCs(t, KittyEncoder{}.AnimationControl(3, AnimationStop, tt.loops))[0]
		if c.keys["v"] != tt.v || c.keys["s"] != "1" {
			t.Errorf("loops %d: %v", tt.loops, c.keys)
		}
	}
}
