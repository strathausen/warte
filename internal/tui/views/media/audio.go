package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"runtime"
	"strings"
	"time"

	"warte/internal/config"
	"warte/internal/core"
)

// waveformSamples is the number of amplitude samples derived from a file.
const waveformSamples = 240

// audioDuration returns the playing time of an audio file: exact for
// Ogg/Opus (from the last page's granule position), estimated from the
// size for common compressed formats, 0 when unknown.
func audioDuration(data []byte, contentType string) time.Duration {
	if d := oggOpusDuration(data); d > 0 {
		return d
	}
	var kbps int
	switch strings.ToLower(contentType) {
	case "audio/mpeg", "audio/mp3":
		kbps = 128
	case "audio/aac", "audio/mp4", "audio/x-m4a", "audio/m4a":
		kbps = 96
	case "audio/ogg", "audio/opus", "audio/webm":
		kbps = 24
	default:
		return 0
	}
	return time.Duration(float64(len(data)) * 8 / float64(kbps*1000) * float64(time.Second))
}

// oggOpusDuration parses an Ogg/Opus stream's duration, or returns 0.
func oggOpusDuration(data []byte) time.Duration {
	if !bytes.HasPrefix(data, []byte("OggS")) {
		return 0
	}
	head := bytes.Index(data, []byte("OpusHead"))
	if head < 0 || head+12 > len(data) {
		return 0
	}
	preSkip := uint64(binary.LittleEndian.Uint16(data[head+10:]))
	last := bytes.LastIndex(data, []byte("OggS"))
	if last < 0 || last+14 > len(data) {
		return 0
	}
	granule := binary.LittleEndian.Uint64(data[last+6:])
	if granule == ^uint64(0) || granule <= preSkip {
		return 0
	}
	samples := granule - preSkip
	return time.Duration(float64(samples) / 48000 * float64(time.Second))
}

// audioWaveform derives display amplitudes from an encoded audio file. It
// cannot decode audio, so it uses what compressed formats leak: Ogg page
// sizes for VBR Opus, otherwise the byte energy of equal chunks. Values
// are stretched to 0.15..1 so the result reads like a waveform.
func audioWaveform(data []byte, n int) []float32 {
	if n <= 0 || len(data) == 0 {
		return nil
	}
	raw := oggPageSizes(data)
	if len(raw) < 8 || flat(raw) {
		raw = chunkEnergy(data, n)
	}
	raw = resample(raw, n)
	lo, hi := raw[0], raw[0]
	for _, v := range raw {
		lo, hi = min(lo, v), max(hi, v)
	}
	out := make([]float32, len(raw))
	for i, v := range raw {
		f := float32(0.6)
		if hi > lo {
			f = 0.15 + 0.85*(v-lo)/(hi-lo)
		}
		out[i] = f
	}
	return out
}

// oggPageSizes returns the payload size of every audio page.
func oggPageSizes(data []byte) []float32 {
	var out []float32
	for off := 0; off+27 <= len(data); {
		if !bytes.Equal(data[off:off+4], []byte("OggS")) {
			next := bytes.Index(data[off+1:], []byte("OggS"))
			if next < 0 {
				break
			}
			off += 1 + next
			continue
		}
		nseg := int(data[off+26])
		if off+27+nseg > len(data) {
			break
		}
		size := 0
		for _, s := range data[off+27 : off+27+nseg] {
			size += int(s)
		}
		payload := off + 27 + nseg
		if payload+8 <= len(data) {
			p := data[payload : payload+8]
			if !bytes.Equal(p, []byte("OpusHead")) && !bytes.Equal(p, []byte("OpusTags")) {
				out = append(out, float32(size))
			}
		}
		off = payload + size
	}
	return out
}

func flat(v []float32) bool {
	lo, hi := v[0], v[0]
	for _, x := range v {
		lo, hi = min(lo, x), max(hi, x)
	}
	return hi-lo <= hi*0.02
}

// chunkEnergy is the mean distance from 128 of n equal chunks of data.
func chunkEnergy(data []byte, n int) []float32 {
	n = min(n, len(data))
	out := make([]float32, n)
	for i := range n {
		a, b := i*len(data)/n, (i+1)*len(data)/n
		var sum int
		for _, c := range data[a:b] {
			d := int(c) - 128
			if d < 0 {
				d = -d
			}
			sum += d
		}
		out[i] = float32(sum) / float32(max(b-a, 1))
	}
	return out
}

// resample maps v onto n buckets (max per bucket when shrinking).
func resample(v []float32, n int) []float32 {
	if len(v) == 0 || n <= 0 {
		return nil
	}
	out := make([]float32, n)
	for i := range n {
		a := i * len(v) / n
		b := max((i+1)*len(v)/n, a+1)
		m := v[a]
		for _, x := range v[a:min(b, len(v))] {
			m = max(m, x)
		}
		out[i] = m
	}
	return out
}

// errNoPlayer is returned when no audio player is available.
var errNoPlayer = errors.New("no audio player found (set open_with.audio, or install mpv or ffplay)")

// playerArgs returns the command line (without the file) used to play an
// attachment: open_with.audio when configured, else afplay on macOS (or
// mpv/ffplay for Ogg/Opus, which afplay can't play), else paplay, ffplay
// or mpv, whichever is installed.
func playerArgs(cfg config.Config, a core.Attachment) ([]string, error) {
	if c := strings.Fields(cfg.OpenWith[config.OpenAudio]); len(c) > 0 {
		return c, nil
	}
	ct := strings.ToLower(a.ContentType)
	ogg := strings.Contains(ct, "ogg") || strings.Contains(ct, "opus") || strings.Contains(ct, "webm")
	mpv := []string{"mpv", "--no-video", "--really-quiet", "--no-terminal"}
	ffplay := []string{"ffplay", "-nodisp", "-autoexit", "-loglevel", "quiet"}
	var cands [][]string
	if runtime.GOOS == "darwin" {
		if ogg {
			cands = [][]string{mpv, ffplay, {"afplay"}}
		} else {
			cands = [][]string{{"afplay"}, mpv, ffplay}
		}
	} else {
		cands = [][]string{{"paplay"}, ffplay, mpv}
		if ogg {
			cands = [][]string{ffplay, mpv, {"paplay"}}
		}
	}
	for _, c := range cands {
		if _, err := lookPath(c[0]); err == nil {
			return c, nil
		}
	}
	return nil, errNoPlayer
}
