package forms

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"warte/internal/tui/theme"
)

const testLinkURL = "sgnl://linkdevice?uuid=q3VnA4lXcVt1Zr0bQm2y8w&pub_key=BTXlYqv%2BJw8m6f0d6YQZr0sR4aV6qPb1o0bJXlqQm9Zl"

// TestQRRenderRoundTrip checks that the half-block rendering encodes exactly
// the module matrix plus a light quiet zone, for colored, mono-dark and
// mono-light themes.
func TestQRRenderRoundTrip(t *testing.T) {
	q, err := NewQR([]byte(testLinkURL))
	if err != nil {
		t.Fatal(err)
	}
	n := q.Size()
	if n < 21 || (n-21)%4 != 0 {
		t.Fatalf("odd QR size %d", n)
	}
	mono, _ := theme.Get(theme.MonoName)
	monoLight := mono
	monoLight.Dark = false
	charm, _ := theme.Get("charm")
	latte, _ := theme.Get("catppuccin-latte")
	for _, tc := range []struct {
		name   string
		th     theme.Theme
		invert bool
	}{
		{"charm", charm, false},
		{"latte", latte, false},
		{"mono-dark", mono, true},
		{"mono-light", monoLight, false},
	} {
		for _, quiet := range []int{1, 2, 4} {
			st := theme.NewStyles(tc.th)
			lines := q.Render(st, quiet)
			cw, ch := q.CellSize(quiet)
			if len(lines) != ch {
				t.Fatalf("%s: %d lines, want %d", tc.name, len(lines), ch)
			}
			for _, l := range lines {
				if w := ansi.StringWidth(l); w != cw {
					t.Fatalf("%s: line width %d, want %d", tc.name, w, cw)
				}
			}
			got := ParseQRRender(lines)
			full := n + 2*quiet
			for y := 0; y < full; y++ {
				for x := 0; x < full; x++ {
					dark := false
					if x >= quiet && y >= quiet && x < quiet+n && y < quiet+n {
						dark = q.Modules[y-quiet][x-quiet]
					}
					want := dark
					if tc.invert {
						want = !dark
					}
					if got[y][x] != want {
						t.Fatalf("%s quiet=%d: module (%d,%d) ink=%v, want %v", tc.name, quiet, x, y, got[y][x], want)
					}
				}
			}
			if !tc.th.Mono && !strings.Contains(lines[0], "\x1b[") {
				t.Errorf("%s: colored theme should paint explicit colors", tc.name)
			}
		}
	}
}

func TestQRColorsContrast(t *testing.T) {
	for _, name := range theme.Names() {
		th, _ := theme.Get(name)
		if th.Mono {
			continue
		}
		dark, light := qrColors(th.Palette)
		if theme.Luminance(dark) >= theme.Luminance(light) {
			t.Errorf("%s: ink is not darker than paper", name)
		}
		if c := theme.Contrast(dark, light); c < 7 {
			t.Errorf("%s: QR contrast %.1f < 7", name, c)
		}
	}
}

func TestQRFitQuiet(t *testing.T) {
	q, _ := NewQR([]byte(testLinkURL))
	n := q.Size()
	if got := q.FitQuiet(n+8, (n+9)/2); got != 4 {
		t.Errorf("FitQuiet roomy = %d, want 4", got)
	}
	if got := q.FitQuiet(n+4, 100); got != 2 {
		t.Errorf("FitQuiet narrow = %d, want 2", got)
	}
	if got := q.FitQuiet(n, 100); got != -1 {
		t.Errorf("FitQuiet too small = %d, want -1", got)
	}
}
