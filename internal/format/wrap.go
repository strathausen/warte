package format

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Wrap wraps s (plain text or ANSI-styled output of Render) to lines of at
// most width terminal cells and returns the lines. It breaks at spaces and
// hyphens where possible and hard-breaks words longer than a line (such as
// long URLs); grapheme clusters are never split and wide characters (CJK,
// emoji) count as two cells. Existing newlines are kept. Each returned line
// is self-contained: SGR styles and OSC 8 hyperlinks active at a break are
// closed at the end of the line and reopened at the start of the next, so
// lines can be laid out independently.
//
// width < 1 is treated as 1; a grapheme wider than width (e.g. an emoji at
// width 1) occupies a line of its own.
func Wrap(s string, width int) []string {
	if width < 1 {
		width = 1
	}
	wrapped := ansi.Wrap(s, width, "")
	// ansi.Wrap can leave a line one cell too wide when it keeps a leading
	// space in front of a word that fills the line (e.g. " 00000" at width
	// 5); hard-wrap any such line so the width guarantee holds.
	var lines []string
	for _, line := range strings.Split(wrapped, "\n") {
		if ansi.StringWidth(line) > width {
			lines = append(lines, strings.Split(ansi.Hardwrap(line, width, true), "\n")...)
			continue
		}
		lines = append(lines, line)
	}
	var st ansiState
	for i, line := range lines {
		prefix := st.open()
		st.scan(line)
		lines[i] = prefix + line + st.close()
	}
	return lines
}

// WrapSpans renders spans with st and wraps the result to width.
func WrapSpans(spans []Span, st Styles, width int) []string {
	return Wrap(Render(spans, st), width)
}

// ansiState tracks the SGR attributes and hyperlink active at a position.
type ansiState struct {
	sgr  []string // SGR sequences applied since the last reset
	link string   // the active OSC 8 opening sequence, or ""
}

// open returns the sequences that re-establish the state.
func (s *ansiState) open() string {
	return strings.Join(s.sgr, "") + s.link
}

// close returns the sequences that terminate the state at a line end.
func (s *ansiState) close() string {
	out := ""
	if s.link != "" {
		out += "\x1b]8;;\x1b\\"
	}
	if len(s.sgr) > 0 {
		out += "\x1b[m"
	}
	return out
}

// scan updates the state with the escape sequences in line.
func (s *ansiState) scan(line string) {
	for i := 0; i < len(line); i++ {
		if line[i] != 0x1b || i+1 >= len(line) {
			continue
		}
		switch line[i+1] {
		case '[':
			j := i + 2
			for j < len(line) && (line[j] < 0x40 || line[j] > 0x7e) {
				j++
			}
			if j >= len(line) {
				return
			}
			if line[j] == 'm' {
				s.applySGR(line[i:j+1], line[i+2:j])
			}
			i = j
		case ']':
			j := i + 2
			end := -1
			for ; j < len(line); j++ {
				if line[j] == 0x07 {
					end = j + 1
					break
				}
				if line[j] == 0x1b && j+1 < len(line) && line[j+1] == '\\' {
					end = j + 2
					break
				}
			}
			if end < 0 {
				return
			}
			body := line[i+2 : j]
			if strings.HasPrefix(body, "8;") {
				if k := strings.IndexByte(body[2:], ';'); k >= 0 {
					if body[2+k+1:] == "" {
						s.link = ""
					} else {
						s.link = line[i:end]
					}
				}
			}
			i = end - 1
		}
	}
}

func (s *ansiState) applySGR(seq, params string) {
	if params == "" || params == "0" {
		s.sgr = s.sgr[:0]
		return
	}
	if strings.HasPrefix(params, "0;") {
		s.sgr = s.sgr[:0]
		seq = "\x1b[" + params[2:] + "m"
	}
	s.sgr = append(s.sgr, seq)
}
