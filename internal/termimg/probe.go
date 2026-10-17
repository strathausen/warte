package termimg

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// ErrProbeTimeout is returned by the query helpers when the terminal did not
// answer in time. Any partial result is still returned alongside it.
var ErrProbeTimeout = errors.New("termimg: terminal did not answer in time")

// kittyProbeID is the image id used by the kitty support query.
const kittyProbeID = 31

// Query sequences, exported for callers that run their own input loop (for
// example Bubble Tea, which can forward the replies as messages).
const (
	// KittyQuery asks a kitty-capable terminal to validate a 1x1 RGB image
	// without storing it (a=q). Supporting terminals answer with an APC
	// "_Gi=31;OK" reply.
	KittyQuery = "\x1b_Gi=31,s=1,v=1,a=q,t=d,f=24;AAAA\x1b\\"
	// DA1Query is the primary device attributes request. Every terminal
	// answers it, so it is sent last to bound the wait.
	DA1Query = "\x1b[c"
	// CellSizeQuery (XTWINOPS 16) asks for the cell size in pixels; the reply
	// is CSI 6 ; height ; width t.
	CellSizeQuery = "\x1b[16t"
)

// ProbeResult is what the terminal revealed in reply to probe queries.
type ProbeResult struct {
	// Kitty is true when the terminal acknowledged the kitty graphics query.
	Kitty bool
	// KittyError holds the error text from a negative kitty reply, if any.
	KittyError string
	// Sixel is true when DA1 listed attribute 4 (sixel graphics).
	Sixel bool
	// DA1 holds the parsed primary device attributes.
	DA1 []int
	// Cell is the cell size in pixels from a CSI 16 t reply (zero if absent).
	Cell CellSize
	// Answered is true when a DA1 reply was seen, meaning the terminal is
	// interactive and all earlier replies have arrived.
	Answered bool
}

// ParseProbeResponse extracts kitty, DA1 and cell size replies from raw bytes
// read from the terminal. Unrelated bytes are ignored.
func ParseProbeResponse(data []byte) ProbeResult {
	var r ProbeResult
	s := string(data)

	// Kitty APC replies: ESC _ G <keys> ; <message> ESC \
	for i := 0; ; {
		j := strings.Index(s[i:], "\x1b_G")
		if j < 0 {
			break
		}
		start := i + j + 3
		end := strings.Index(s[start:], "\x1b\\")
		if end < 0 {
			break
		}
		body := s[start : start+end]
		i = start + end + 2
		keys, msg, _ := strings.Cut(body, ";")
		if !hasKey(keys, "i", strconv.Itoa(kittyProbeID)) {
			continue
		}
		if msg == "OK" {
			r.Kitty = true
			r.KittyError = ""
		} else if !r.Kitty {
			r.KittyError = msg
		}
	}

	// CSI replies: DA1 (ESC [ ? ... c) and cell size (ESC [ 6 ; h ; w t).
	for i := 0; ; {
		j := strings.Index(s[i:], "\x1b[")
		if j < 0 {
			break
		}
		start := i + j + 2
		k := start
		for k < len(s) && (s[k] >= 0x30 && s[k] <= 0x3f) {
			k++
		}
		if k >= len(s) {
			break
		}
		params, final := s[start:k], s[k]
		i = k + 1
		switch {
		case final == 'c' && strings.HasPrefix(params, "?"):
			r.Answered = true
			r.DA1 = parseInts(params[1:])
			for _, a := range r.DA1[min(1, len(r.DA1)):] {
				if a == 4 {
					r.Sixel = true
				}
			}
		case final == 't':
			v := parseInts(params)
			if len(v) == 3 && v[0] == 6 && v[1] > 0 && v[2] > 0 {
				r.Cell = CellSize{Width: v[2], Height: v[1]}
			}
		}
	}
	return r
}

func hasKey(keys, k, want string) bool {
	for kv := range strings.SplitSeq(keys, ",") {
		key, val, ok := strings.Cut(kv, "=")
		if ok && key == k && val == want {
			return true
		}
	}
	return false
}

func parseInts(s string) []int {
	var out []int
	for f := range strings.SplitSeq(s, ";") {
		n, err := strconv.Atoi(f)
		if err != nil {
			continue
		}
		out = append(out, n)
	}
	return out
}

// Prober sends capability queries to a terminal and parses the replies.
//
// The terminal must be in raw mode (no line buffering or echo), and nothing
// else may read from it concurrently, so probe before starting Bubble Tea.
// If RW implements SetReadDeadline (as *os.File does for pollable files),
// reads are bounded by deadlines; otherwise a helper goroutine performs the
// reads and, after a timeout, may consume one more chunk of input before it
// exits.
type Prober struct {
	// RW is the terminal.
	RW io.ReadWriter
	// Timeout bounds the wait for replies; zero means 500ms.
	Timeout time.Duration
	// Tmux wraps the kitty query in tmux passthrough. DA1 and cell size
	// queries are answered by tmux itself and are not wrapped.
	Tmux bool
}

// Probe sends the kitty graphics query, a cell size query and DA1, and
// collects the replies until DA1 arrives or the timeout expires.
func (p Prober) Probe() (ProbeResult, error) {
	kq := KittyQuery
	if p.Tmux {
		kq = TmuxPassthrough(kq)
	}
	return p.run(kq + CellSizeQuery + DA1Query)
}

// Sixel sends only DA1 and reports whether attribute 4 is present.
func (p Prober) Sixel() (bool, error) {
	r, err := p.run(DA1Query)
	return r.Sixel, err
}

// CellSize asks for the cell size in pixels (CSI 16 t).
func (p Prober) CellSize() (CellSize, error) {
	r, err := p.run(CellSizeQuery + DA1Query)
	if err == nil && r.Cell.IsZero() {
		err = errors.New("termimg: terminal did not report its cell size")
	}
	return r.Cell, err
}

func (p Prober) run(query string) (ProbeResult, error) {
	if p.RW == nil {
		return ProbeResult{}, errors.New("termimg: nil terminal")
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 500 * time.Millisecond
	}
	if _, err := io.WriteString(p.RW, query); err != nil {
		return ProbeResult{}, err
	}
	data, err := readUntil(p.RW, timeout, func(b []byte) bool {
		return ParseProbeResponse(b).Answered
	})
	return ParseProbeResponse(data), err
}

// QueryKittySupport sends the kitty graphics query (a=q) followed by DA1 and
// reports whether the terminal acknowledged it. DA1 attribute 4 also fills
// ProbeResult.Sixel. See [Prober] for requirements on rw.
func QueryKittySupport(rw io.ReadWriter, timeout time.Duration) (ProbeResult, error) {
	return Prober{RW: rw, Timeout: timeout}.Probe()
}

// QuerySixelSupport sends DA1 and reports whether attribute 4 (sixel) is
// listed. See [Prober] for requirements on rw.
func QuerySixelSupport(rw io.ReadWriter, timeout time.Duration) (bool, error) {
	return Prober{RW: rw, Timeout: timeout}.Sixel()
}

// QueryCellSize asks the terminal for its cell size in pixels with CSI 16 t.
// See [Prober] for requirements on rw.
func QueryCellSize(rw io.ReadWriter, timeout time.Duration) (CellSize, error) {
	return Prober{RW: rw, Timeout: timeout}.CellSize()
}

type deadliner interface {
	SetReadDeadline(t time.Time) error
}

// readUntil reads from r until done reports true, the reader fails, or the
// timeout passes.
func readUntil(r io.Reader, timeout time.Duration, done func([]byte) bool) ([]byte, error) {
	deadline := time.Now().Add(timeout)
	var buf bytes.Buffer
	chunk := make([]byte, 256)

	if d, ok := r.(deadliner); ok && d.SetReadDeadline(deadline) == nil {
		defer d.SetReadDeadline(time.Time{}) //nolint:errcheck // best effort reset
		for {
			n, err := r.Read(chunk)
			buf.Write(chunk[:n])
			if done(buf.Bytes()) {
				return buf.Bytes(), nil
			}
			if err != nil {
				if errors.Is(err, os.ErrDeadlineExceeded) || time.Now().After(deadline) {
					return buf.Bytes(), ErrProbeTimeout
				}
				return buf.Bytes(), err
			}
		}
	}

	type result struct {
		b   []byte
		err error
	}
	ch := make(chan result)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			c := make([]byte, 256)
			n, err := r.Read(c)
			select {
			case ch <- result{c[:n], err}:
			case <-stop:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case res := <-ch:
			buf.Write(res.b)
			if done(buf.Bytes()) {
				return buf.Bytes(), nil
			}
			if res.err != nil {
				if errors.Is(res.err, io.EOF) {
					return buf.Bytes(), ErrProbeTimeout
				}
				return buf.Bytes(), res.err
			}
		case <-timer.C:
			return buf.Bytes(), ErrProbeTimeout
		}
	}
}
