package termimg

import (
	"image"
	"image/color"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestHalfBlocksDimensions(t *testing.T) {
	tests := []struct {
		name       string
		img        image.Image
		cols, rows int
		r          HalfBlockRenderer
	}{
		{"square", gradient(40, 40), 10, 5, HalfBlockRenderer{}},
		{"wide", gradient(200, 20), 30, 10, HalfBlockRenderer{}},
		{"tall", gradient(10, 300), 8, 4, HalfBlockRenderer{Color256: true}},
		{"tiny", solid(1, 1, color.White), 3, 3, HalfBlockRenderer{}},
		{"nil", nil, 4, 2, HalfBlockRenderer{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows := tt.r.Render(tt.img, tt.cols, tt.rows)
			if len(rows) != tt.rows {
				t.Fatalf("rows %d, want %d", len(rows), tt.rows)
			}
			for i, row := range rows {
				if w := ansi.StringWidth(row); w != tt.cols {
					t.Errorf("row %d width %d, want %d: %q", i, w, tt.cols, row)
				}
			}
		})
	}
	if (HalfBlockRenderer{}).Render(solid(1, 1, color.White), 0, 3) != nil {
		t.Error("zero cols should be nil")
	}
}

func TestHalfBlocksColors(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 1, 2))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	img.Set(0, 1, color.RGBA{0, 0, 255, 255})
	rows := HalfBlockRenderer{Quality: Nearest}.Render(img, 1, 1)
	want := "\x1b[38;2;255;0;0m\x1b[48;2;0;0;255m▀\x1b[39;49m"
	if rows[0] != want {
		t.Fatalf("got %q, want %q", rows[0], want)
	}
	rows = HalfBlockRenderer{Quality: Nearest, Color256: true}.Render(img, 1, 1)
	want = "\x1b[38;5;196m\x1b[48;5;21m▀\x1b[39;49m"
	if rows[0] != want {
		t.Fatalf("256: got %q, want %q", rows[0], want)
	}
}

func TestHalfBlocksTransparency(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 1, color.NRGBA{0, 255, 0, 255}) // bottom-left only
	rows := HalfBlockRenderer{Quality: Nearest}.Render(img, 2, 1)
	if !strings.Contains(rows[0], "▄") || !strings.HasSuffix(rows[0], " \x1b[39;49m") {
		t.Fatalf("got %q", rows[0])
	}
	blank := HalfBlockRenderer{}.Render(image.NewNRGBA(image.Rect(0, 0, 2, 2)), 2, 1)
	if blank[0] != "  " {
		t.Fatalf("fully transparent row should be plain spaces, got %q", blank[0])
	}
}

func TestHalfBlocksAspect(t *testing.T) {
	// A 2:1 wide image in a 10x10 box fills 10 cols and 5 rows (10 px tall).
	rows := HalfBlockRenderer{}.Render(solid(20, 10, color.White), 10, 10)
	drawn := 0
	for _, r := range rows {
		if strings.Contains(r, "▀") {
			drawn++
		}
	}
	if drawn != 3 { // 5 pixel rows → 3 cells (last half)
		t.Fatalf("drawn rows %d, want 3", drawn)
	}
}

func TestTo256(t *testing.T) {
	tests := []struct {
		r, g, b uint8
		want    uint8
	}{
		{0, 0, 0, 16},
		{255, 255, 255, 231},
		{255, 0, 0, 196},
		{0, 255, 0, 46},
		{0, 0, 255, 21},
		{128, 128, 128, 244},
		{8, 8, 8, 232},
		{95, 135, 175, 67},
	}
	for _, tt := range tests {
		if got := To256(tt.r, tt.g, tt.b); got != tt.want {
			t.Errorf("To256(%d,%d,%d) = %d, want %d", tt.r, tt.g, tt.b, got, tt.want)
		}
	}
}

func TestBraille(t *testing.T) {
	// Left half white, right half black.
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := range 4 {
		for x := range 4 {
			if x < 2 {
				img.Set(x, y, color.White)
			} else {
				img.Set(x, y, color.Black)
			}
		}
	}
	rows := BrailleRenderer{}.Render(img, 2, 1)
	if len(rows) != 1 || rows[0] != "⣿⠀" {
		t.Fatalf("got %q", rows)
	}
	inv := BrailleRenderer{Invert: true}.Render(img, 2, 1)
	if inv[0] != "⠀⣿" {
		t.Fatalf("inverted %q", inv)
	}
	col := BrailleRenderer{Color: true}.Render(img, 2, 1)
	if col[0] != "\x1b[38;2;255;255;255m⣿⠀\x1b[39m" {
		t.Fatalf("colored %q", col[0])
	}
	for _, tt := range []struct{ cols, rows int }{{5, 3}, {1, 1}, {12, 2}} {
		out := BrailleRenderer{}.Render(gradient(30, 17), tt.cols, tt.rows)
		if len(out) != tt.rows {
			t.Fatalf("rows %d", len(out))
		}
		for _, r := range out {
			if ansi.StringWidth(r) != tt.cols {
				t.Fatalf("width %d, want %d", ansi.StringWidth(r), tt.cols)
			}
		}
	}
}

func TestBrailleBit(t *testing.T) {
	want := map[[2]int]rune{
		{0, 0}: 0x01, {0, 1}: 0x02, {0, 2}: 0x04, {0, 3}: 0x40,
		{1, 0}: 0x08, {1, 1}: 0x10, {1, 2}: 0x20, {1, 3}: 0x80,
	}
	for k, v := range want {
		if got := brailleBit(k[0], k[1]); got != v {
			t.Errorf("brailleBit(%d,%d) = %#x, want %#x", k[0], k[1], got, v)
		}
	}
}

func TestWaveform(t *testing.T) {
	tests := []struct {
		name    string
		samples []float32
		width   int
		want    string
	}{
		{"silence", make([]float32, 100), 3, "⣀⣀⣀"},
		{"empty", nil, 2, "⣀⣀"},
		{"full", []float32{1, -1, 1, -1}, 2, "⣿⣿"},
		{"ramp", []float32{0.25, 0.5, 0.75, 1}, 2, "⣠⣾"},
		{"zero width", []float32{1}, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Waveform(tt.samples, tt.width)
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
			if ansi.StringWidth(got) != tt.width {
				t.Fatalf("width %d", ansi.StringWidth(got))
			}
		})
	}
	rows := WaveformRows([]float32{1, 0.5, 0.1, 0}, 2, 2)
	if len(rows) != 2 || rows[0] != "⡇⠀" || rows[1] != "⣿⣀" {
		t.Fatalf("multi-row %q", rows)
	}
}
