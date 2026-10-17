package termimg

import (
	"encoding/base64"
	"image/color"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestRendererProtocols(t *testing.T) {
	img := gradient(400, 200) // 2:1
	tests := []struct {
		name     string
		r        Renderer
		overlay  bool
		pre      string // required prefix of Pre ("" = Pre must be empty)
		rowCheck func(string) bool
	}{
		{"kitty", Renderer{Protocol: Kitty}, false, "\x1b_G", func(s string) bool { return strings.ContainsRune(s, PlaceholderRune) }},
		{"kitty tmux", Renderer{Protocol: Kitty, Tmux: true}, false, "\x1bPtmux;", func(s string) bool { return strings.ContainsRune(s, PlaceholderRune) }},
		{"iterm2", Renderer{Protocol: ITerm2}, true, "\x1b]1337;File=inline=1;", func(s string) bool { return strings.TrimSpace(s) == "" }},
		{"sixel", Renderer{Protocol: Sixel}, true, "\x1bP0;1;0q", func(s string) bool { return strings.TrimSpace(s) == "" }},
		{"blocks", Renderer{Protocol: HalfBlocks}, false, "", func(s string) bool { return strings.Contains(s, "▀") }},
		{"auto as blocks", Renderer{Protocol: Auto, Color256: true}, false, "", func(s string) bool { return strings.Contains(s, "38;5;") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := tt.r.Render(42, img, 40, 40)
			// 400x200 px into 40x40 cells of 10x20 px: limited by width
			// (400 px) → 40 cols, 200 px tall → 10 rows.
			if out.Cols != 40 || out.Height != 10 || len(out.Rows) != 10 {
				t.Fatalf("size %dx%d rows=%d", out.Cols, out.Height, len(out.Rows))
			}
			for _, row := range out.Rows {
				if ansi.StringWidth(row) != out.Cols {
					t.Fatalf("row width %d", ansi.StringWidth(row))
				}
				if !tt.rowCheck(row) {
					t.Fatalf("unexpected row %q", row)
				}
			}
			if out.NeedsOverlay != tt.overlay {
				t.Fatalf("NeedsOverlay %v", out.NeedsOverlay)
			}
			if tt.pre == "" && out.Pre != "" || !strings.HasPrefix(out.Pre, tt.pre) {
				t.Fatalf("Pre %q", out.Pre[:min(len(out.Pre), 30)])
			}
			ov := out.OverlayAt(3, 7)
			if tt.overlay {
				if !strings.HasPrefix(ov, "\x1b7\x1b[8;4H") || !strings.HasSuffix(ov, "\x1b8") {
					t.Fatalf("overlay %q", ov[:min(len(ov), 20)])
				}
			} else if ov != "" {
				t.Fatal("unexpected overlay")
			}
		})
	}
}

func TestRendererITerm2Header(t *testing.T) {
	out := (&Renderer{Protocol: ITerm2}).Render(0, solid(20, 20, color.White), 4, 4)
	// 20x20 px in 10x20 cells: 2 cols x 1 row.
	if !strings.Contains(out.Pre, ";width=2;height=1;preserveAspectRatio=1;") || !strings.HasSuffix(out.Pre, "\a") {
		t.Fatalf("Pre %q", out.Pre)
	}
	b64 := out.Pre[strings.IndexByte(out.Pre, ':')+1 : len(out.Pre)-1]
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Pre, "size="+strconv.Itoa(len(data))+";") {
		t.Fatal("size key does not match payload")
	}
}

func TestRendererKittyDownscales(t *testing.T) {
	out := (&Renderer{Protocol: Kitty}).Render(99, gradient(4000, 2000), 20, 20)
	cmds := parseAPCs(t, out.Pre)
	if cmds[0].keys["c"] != "20" || cmds[0].keys["r"] != "5" {
		t.Fatalf("keys %v", cmds[0].keys)
	}
	var all strings.Builder
	for _, c := range cmds {
		all.WriteString(c.payload)
	}
	data, _ := base64.StdEncoding.DecodeString(all.String())
	img, err := DecodeImage(data)
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 200 || b.Dy() != 100 {
		t.Fatalf("transmitted %v, want 200x100", b)
	}
}

func TestRendererAnimated(t *testing.T) {
	d := &Decoded{Format: "gif", Frames: []Frame{
		{Image: solid(40, 40, color.White), Delay: 100 * time.Millisecond},
		{Image: solid(40, 40, color.Black), Delay: 100 * time.Millisecond},
	}}
	k := (&Renderer{Protocol: Kitty}).RenderDecoded(30, d, 10, 10)
	if !k.Animated || !strings.Contains(k.Pre, "a=f,") || !strings.Contains(k.Pre, "a=a,i=30,s=3") {
		t.Fatalf("kitty animation missing: %+v", k.Animated)
	}
	b := (&Renderer{Protocol: HalfBlocks}).RenderDecoded(30, d, 10, 10)
	if b.Animated || len(b.Rows) == 0 {
		t.Fatal("blocks should render first frame only")
	}
	if (&Renderer{Protocol: Kitty}).RenderDecoded(1, nil, 10, 10).Rows != nil {
		t.Fatal("nil decoded")
	}
}

func TestRendererEdgeCases(t *testing.T) {
	img := solid(10, 10, color.White)
	if out := (&Renderer{Protocol: None}).Render(1, img, 10, 10); out.Rows != nil || out.Pre != "" {
		t.Fatal("None should render nothing")
	}
	if out := (&Renderer{Protocol: Kitty}).Render(0, img, 10, 10); out.Rows != nil {
		t.Fatal("kitty needs an id")
	}
	if out := (&Renderer{Protocol: HalfBlocks}).Render(1, nil, 10, 10); out.Rows != nil {
		t.Fatal("nil image")
	}
	if out := (&Renderer{Protocol: HalfBlocks}).Render(1, img, 0, 10); out.Rows != nil {
		t.Fatal("zero box")
	}
	r := &Renderer{Protocol: Kitty}
	if r.Delete(5) != r.KittyEncoder().Delete(5) || len(r.Placeholder(5, 2, 2)) != 2 {
		t.Fatal("kitty helpers")
	}
	s := &Renderer{Protocol: Sixel}
	if s.Delete(5) != "" || s.Placeholder(5, 2, 2) != nil {
		t.Fatal("non-kitty helpers")
	}
	if c, rr := r.Size(400, 200, 40, 40); c != 40 || rr != 10 {
		t.Fatalf("Size %d %d", c, rr)
	}
}

func TestNewRenderer(t *testing.T) {
	d := Detection{Protocol: HalfBlocks, Tmux: true, TrueColor: false}
	r := NewRenderer(Auto, d, CellSize{8, 16})
	if r.Protocol != HalfBlocks || !r.Tmux || !r.Color256 || r.Cell != (CellSize{8, 16}) {
		t.Fatalf("%+v", r)
	}
	r = NewRenderer(Kitty, Detection{TrueColor: true}, CellSize{})
	if r.Protocol != Kitty || r.Color256 {
		t.Fatalf("%+v", r)
	}
}

func TestPlacementAndBlankRows(t *testing.T) {
	p := Placement{Payload: "IMG", Cols: 2, Rows: 1}
	if got := p.At(0, 0); got != "\x1b7\x1b[1;1HIMG\x1b8" {
		t.Fatalf("At = %q", got)
	}
	if (Placement{}).At(1, 1) != "" {
		t.Fatal("empty placement")
	}
	if got := Overlay("X", -3, -1); got != "\x1b7\x1b[1;1HX\x1b8" {
		t.Fatalf("negative coords %q", got)
	}
	rows := BlankRows(3, 2)
	if len(rows) != 2 || rows[0] != "   " {
		t.Fatalf("%q", rows)
	}
	if BlankRows(0, 1) != nil {
		t.Fatal("zero cols")
	}
	enc := ITerm2Encoder{Tmux: true}
	if got := enc.EncodeData([]byte{1, 2, 3}, 1, 1); !strings.HasPrefix(got, "\x1bPtmux;\x1b\x1b]1337;File=inline=1;size=3;") {
		t.Fatalf("tmux iterm %q", got)
	}
	if enc.Encode(nil, 1, 1) != "" || enc.EncodeData(nil, 1, 1) != "" {
		t.Fatal("empty input")
	}
}
