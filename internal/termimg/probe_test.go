package termimg

import (
	"bytes"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseProbeResponse(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want ProbeResult
	}{
		{"kitty ok + da1", "\x1b_Gi=31;OK\x1b\\\x1b[?62;22c", ProbeResult{Kitty: true, DA1: []int{62, 22}, Answered: true}},
		{"kitty error", "\x1b_Gi=31;ENOTSUPPORTED:nope\x1b\\\x1b[?62c", ProbeResult{KittyError: "ENOTSUPPORTED:nope", DA1: []int{62}, Answered: true}},
		{"other id ignored", "\x1b_Gi=7;OK\x1b\\\x1b[?1;2c", ProbeResult{DA1: []int{1, 2}, Answered: true}},
		{"sixel attr", "\x1b[?63;1;2;4;6;9;15;22c", ProbeResult{Sixel: true, DA1: []int{63, 1, 2, 4, 6, 9, 15, 22}, Answered: true}},
		{"class 4 is not sixel", "\x1b[?4;6c", ProbeResult{DA1: []int{4, 6}, Answered: true}},
		{"cell size", "\x1b[6;20;10t\x1b[?62c", ProbeResult{Cell: CellSize{10, 20}, DA1: []int{62}, Answered: true}},
		{"no da1", "\x1b_Gi=31;OK\x1b\\", ProbeResult{Kitty: true}},
		{"noise", "garbage\x1b[1;2Rmore\x1b[?62;4c", ProbeResult{Sixel: true, DA1: []int{62, 4}, Answered: true}},
		{"truncated", "\x1b[?62;4", ProbeResult{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseProbeResponse([]byte(tt.in)); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

// fakeTerm answers queries written to it with a canned reply.
type fakeTerm struct {
	mu      sync.Mutex
	written bytes.Buffer
	pr      *io.PipeReader
	pw      *io.PipeWriter
	reply   string
}

func newFakeTerm(reply string) *fakeTerm {
	pr, pw := io.Pipe()
	return &fakeTerm{pr: pr, pw: pw, reply: reply}
}

func (f *fakeTerm) Write(p []byte) (int, error) {
	f.mu.Lock()
	f.written.Write(p)
	f.mu.Unlock()
	if f.reply != "" {
		go func() {
			// Split the reply to exercise accumulation.
			mid := len(f.reply) / 2
			f.pw.Write([]byte(f.reply[:mid])) //nolint:errcheck
			f.pw.Write([]byte(f.reply[mid:])) //nolint:errcheck
		}()
	}
	return len(p), nil
}

func (f *fakeTerm) Read(p []byte) (int, error) { return f.pr.Read(p) }

func (f *fakeTerm) sent() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.written.String()
}

func TestQueryKittySupport(t *testing.T) {
	ft := newFakeTerm("\x1b_Gi=31;OK\x1b\\\x1b[6;18;9t\x1b[?62;4;22c")
	t.Cleanup(func() { ft.pw.Close() })
	r, err := QueryKittySupport(ft, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Kitty || !r.Sixel || r.Cell != (CellSize{9, 18}) {
		t.Fatalf("result %+v", r)
	}
	sent := ft.sent()
	if !strings.HasPrefix(sent, KittyQuery) || !strings.HasSuffix(sent, DA1Query) {
		t.Fatalf("sent %q", sent)
	}
}

func TestProberTmuxWrapsKittyQuery(t *testing.T) {
	ft := newFakeTerm("\x1b[?62c")
	t.Cleanup(func() { ft.pw.Close() })
	if _, err := (Prober{RW: ft, Timeout: time.Second, Tmux: true}).Probe(); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ft.sent(), "\x1bPtmux;\x1b\x1b_G") || !strings.HasSuffix(ft.sent(), DA1Query) {
		t.Fatalf("sent %q", ft.sent())
	}
}

func TestQuerySixelSupport(t *testing.T) {
	for _, tt := range []struct {
		reply string
		want  bool
	}{{"\x1b[?62;4c", true}, {"\x1b[?62;22c", false}} {
		ft := newFakeTerm(tt.reply)
		got, err := QuerySixelSupport(ft, time.Second)
		ft.pw.Close()
		if err != nil || got != tt.want {
			t.Errorf("reply %q: got %v, %v", tt.reply, got, err)
		}
	}
}

func TestQueryCellSize(t *testing.T) {
	ft := newFakeTerm("\x1b[6;24;12t\x1b[?62c")
	t.Cleanup(func() { ft.pw.Close() })
	c, err := QueryCellSize(ft, time.Second)
	if err != nil || c != (CellSize{12, 24}) {
		t.Fatalf("got %v, %v", c, err)
	}
	ft2 := newFakeTerm("\x1b[?62c")
	t.Cleanup(func() { ft2.pw.Close() })
	if _, err := QueryCellSize(ft2, time.Second); err == nil {
		t.Fatal("expected error when cell size missing")
	}
}

func TestProbeTimeout(t *testing.T) {
	ft := newFakeTerm("") // never answers
	t.Cleanup(func() { ft.pw.Close() })
	start := time.Now()
	_, err := QueryKittySupport(ft, 50*time.Millisecond)
	if !errors.Is(err, ErrProbeTimeout) {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("timeout not honoured")
	}
}

// pipeTerm uses an *os.File pipe for reads, which supports deadlines.
type pipeTerm struct {
	r *os.File
	w io.Writer
}

func (p pipeTerm) Read(b []byte) (int, error)        { return p.r.Read(b) }
func (p pipeTerm) Write(b []byte) (int, error)       { return p.w.Write(b) }
func (p pipeTerm) SetReadDeadline(t time.Time) error { return p.r.SetReadDeadline(t) }

func TestProbeWithDeadline(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close(); w.Close() })
	term := pipeTerm{r: r, w: io.Discard}

	// No reply: deadline path must time out.
	if _, err := QuerySixelSupport(term, 50*time.Millisecond); !errors.Is(err, ErrProbeTimeout) {
		t.Fatalf("err = %v", err)
	}
	// Reply available.
	if _, err := w.Write([]byte("\x1b[?62;4c")); err != nil {
		t.Fatal(err)
	}
	ok, err := QuerySixelSupport(term, time.Second)
	if err != nil || !ok {
		t.Fatalf("got %v, %v", ok, err)
	}
}

func TestProberNilRW(t *testing.T) {
	if _, err := (Prober{}).Probe(); err == nil {
		t.Fatal("expected error")
	}
}
