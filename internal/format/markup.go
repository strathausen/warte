package format

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"warte/internal/core"
)

// Composer markup
//
// The composer accepts a small, chat-style markup that Parse turns into a
// Signal body plus body ranges:
//
//	**bold**  *bold*        bold
//	__italic__  _italic_    italic
//	~~strike~~  ~strike~    strikethrough
//	`mono`  ```code```      monospace (no formatting or escapes inside)
//	||spoiler||             spoiler
//	@{<aci>}                mention of the account <aci>
//	\x                      literal x, for x in \ * _ ~ | ` @
//
// Single-character delimiters (*, _, ~) follow word boundaries so that
// snake_case_names and 2*3*4 stay literal: an opener must not follow a letter
// or digit and must be followed by a non-space; a closer must follow a
// non-space and must not be followed by a letter or digit. Doubled
// delimiters (**, __, ~~, ||) work anywhere, including inside words. A code
// block opened with ``` drops one newline directly after the opening fence and
// one directly before the closing fence. Unmatched delimiters are literal.

// MentionSpan marks a mention the composer inserted into its input: the
// bytes input[Start:End] (typically "@Name") stand for a mention of ACI.
type MentionSpan struct {
	Start, End int // byte offsets into the composer input, half-open
	ACI        core.ACI
}

// MentionToken returns the inline markup token for a mention of aci, which
// Parse understands: "@{" + aci + "}".
func MentionToken(aci core.ACI) string { return "@{" + string(aci) + "}" }

const escapable = "\\*_~|`@"

type tokKind int

const (
	tokText tokKind = iota
	tokDelim
	tokCode
	tokMention
)

type token struct {
	kind       tokKind
	text       string // literal text, delimiter, or code content
	style      core.RangeStyle
	aci        core.ACI
	start, end int // byte span in the input (delimiters only)
	pair       int // index of the matching delimiter, or -1
}

// Parse converts composer markup into a Signal message body and body ranges
// (UTF-16 offsets, normalized as by NormalizeRanges). Mentions may be written
// as @{aci} tokens.
func Parse(input string) (body string, ranges []core.BodyRange) {
	return ParseWithMentions(input, nil)
}

// ParseWithMentions is Parse for composers that track inserted mentions
// themselves: every valid, non-overlapping span in mentions is replaced by
// the U+FFFC placeholder and a mention range. Invalid spans (out of bounds,
// empty, overlapping an earlier span, or not on UTF-8 boundaries) are
// ignored and their text kept literally. Mention spans inside code are
// literal text.
func ParseWithMentions(input string, mentions []MentionSpan) (body string, ranges []core.BodyRange) {
	toks := tokenize(input, validMentions(input, mentions))
	pairDelims(input, toks)

	var b strings.Builder
	u := 0
	write := func(s string) {
		b.WriteString(s)
		u += UTF16Len(s)
	}
	opened := map[int]int{}
	for i, t := range toks {
		switch t.kind {
		case tokText:
			write(t.text)
		case tokDelim:
			switch {
			case t.pair < 0:
				write(t.text)
			case t.pair > i:
				opened[i] = u
			default:
				start := opened[t.pair]
				if u > start {
					ranges = append(ranges, core.BodyRange{Start: start, Length: u - start, Style: t.style})
				}
			}
		case tokCode:
			start := u
			write(t.text)
			if u > start {
				ranges = append(ranges, core.BodyRange{Start: start, Length: u - start, Style: core.StyleMonospace})
			}
		case tokMention:
			ranges = append(ranges, core.BodyRange{Start: u, Length: 1, Style: core.StyleMention, MentionACI: t.aci})
			write(string(MentionPlaceholder))
		}
	}
	body = b.String()
	return body, NormalizeRanges(body, ranges)
}

func validMentions(input string, ms []MentionSpan) []MentionSpan {
	var out []MentionSpan
	for _, m := range ms {
		if m.Start < 0 || m.End > len(input) || m.Start >= m.End ||
			!utf8.RuneStart(byteAt(input, m.Start)) || (m.End < len(input) && !utf8.RuneStart(input[m.End])) {
			continue
		}
		out = append(out, m)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	res := out[:0]
	end := 0
	for _, m := range out {
		if m.Start < end {
			continue
		}
		res = append(res, m)
		end = m.End
	}
	return res
}

func byteAt(s string, i int) byte {
	if i < len(s) {
		return s[i]
	}
	return 0
}

func tokenize(in string, mentions []MentionSpan) []token {
	var toks []token
	var text strings.Builder
	flush := func() {
		if text.Len() > 0 {
			toks = append(toks, token{kind: tokText, text: text.String(), pair: -1})
			text.Reset()
		}
	}
	delim := func(d string, st core.RangeStyle, at int) {
		flush()
		toks = append(toks, token{kind: tokDelim, text: d, style: st, start: at, end: at + len(d), pair: -1})
	}
	mi := 0
	for i := 0; i < len(in); {
		for mi < len(mentions) && mentions[mi].Start < i {
			mi++
		}
		if mi < len(mentions) && mentions[mi].Start == i {
			flush()
			toks = append(toks, token{kind: tokMention, aci: mentions[mi].ACI, pair: -1})
			i = mentions[mi].End
			mi++
			continue
		}
		c := in[i]
		next := byteAt(in, i+1)
		switch {
		case c == '\\' && next != 0 && strings.IndexByte(escapable, next) >= 0:
			text.WriteByte(next)
			i += 2
		case c == '`':
			if content, n, ok := scanCode(in[i:]); ok {
				flush()
				toks = append(toks, token{kind: tokCode, text: content, pair: -1})
				i += n
				continue
			}
			if strings.HasPrefix(in[i:], "```") {
				text.WriteString("```")
				i += 3
			} else {
				text.WriteByte('`')
				i++
			}
		case c == '*' || c == '_' || c == '~':
			st := map[byte]core.RangeStyle{'*': core.StyleBold, '_': core.StyleItalic, '~': core.StyleStrikethrough}[c]
			if next == c {
				delim(in[i:i+2], st, i)
				i += 2
			} else {
				delim(in[i:i+1], st, i)
				i++
			}
		case c == '|' && next == '|':
			delim("||", core.StyleSpoiler, i)
			i += 2
		case c == '@' && next == '{':
			if j := strings.IndexByte(in[i+2:], '}'); j > 0 && isACIToken(in[i+2:i+2+j]) {
				flush()
				toks = append(toks, token{kind: tokMention, aci: core.ACI(strings.ToLower(in[i+2 : i+2+j])), pair: -1})
				i += j + 3
				continue
			}
			text.WriteByte(c)
			i++
		default:
			_, size := utf8.DecodeRuneInString(in[i:])
			text.WriteString(in[i : i+size])
			i += size
		}
	}
	flush()
	return toks
}

// scanCode recognizes a code span at the start of s and returns its content
// and the number of bytes consumed.
func scanCode(s string) (content string, n int, ok bool) {
	if strings.HasPrefix(s, "```") {
		j := strings.Index(s[3:], "```")
		if j <= 0 {
			return "", 0, false
		}
		content = s[3 : 3+j]
		content = strings.TrimPrefix(content, "\n")
		content = strings.TrimSuffix(content, "\n")
		return content, j + 6, true
	}
	j := strings.IndexByte(s[1:], '`')
	if j <= 0 {
		return "", 0, false
	}
	return s[1 : 1+j], j + 2, true
}

func isACIToken(s string) bool {
	if len(s) < 8 || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' || c == '-') {
			return false
		}
	}
	return true
}

// pairDelims matches opening and closing delimiters in place.
func pairDelims(in string, toks []token) {
	open := map[string]int{}
	for i := range toks {
		t := &toks[i]
		if t.kind != tokDelim {
			continue
		}
		prev, _ := utf8.DecodeLastRuneInString(in[:t.start])
		next, _ := utf8.DecodeRuneInString(in[t.end:])
		hasPrev, hasNext := t.start > 0, t.end < len(in)
		var canOpen, canClose bool
		if len(t.text) == 1 {
			canOpen = hasNext && !unicode.IsSpace(next) && !(hasPrev && isAlnum(prev))
			canClose = hasPrev && !unicode.IsSpace(prev) && !(hasNext && isAlnum(next))
		} else {
			canOpen, canClose = hasNext, hasPrev
		}
		if o, ok := open[t.text]; ok && canClose {
			delete(open, t.text)
			if o == i-1 { // empty content: both stay literal
				continue
			}
			toks[o].pair, t.pair = i, o
			continue
		}
		if _, ok := open[t.text]; !ok && canOpen {
			open[t.text] = i
		}
	}
}

func isAlnum(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// ToMarkup converts a Signal body and its ranges into composer markup that
// Parse turns back into the same body and NormalizeRanges(body, ranges), so a
// sent message can be edited. Mentions become @{aci} tokens.
//
// Limitations: monospace text cannot hold other formatting or mentions, so a
// mention inside a monospace range splits that range.
func ToMarkup(body string, ranges []core.BodyRange) string {
	s, _ := toMarkup(body, ranges, nil)
	return s
}

// ToMarkupWithMentions is ToMarkup for composers that track mentions: each
// mention is written as "@Name" (resolved through resolve, see MentionText)
// and reported as a MentionSpan over the returned markup, ready for
// ParseWithMentions.
func ToMarkupWithMentions(body string, ranges []core.BodyRange, resolve func(core.ACI) string) (string, []MentionSpan) {
	if resolve == nil {
		resolve = func(core.ACI) string { return "" }
	}
	return toMarkup(body, ranges, resolve)
}

// markupStyles lists the delimiter-based styles in the order they are opened.
var markupStyles = []struct {
	style core.RangeStyle
	delim string
}{
	{core.StyleSpoiler, "||"},
	{core.StyleBold, "**"},
	{core.StyleItalic, "__"},
	{core.StyleStrikethrough, "~~"},
}

func toMarkup(body string, ranges []core.BodyRange, resolve func(core.ACI) string) (string, []MentionSpan) {
	brs := toByteRanges(body, ranges)
	var mentions []byteRange
	cuts := map[int]bool{0: true, len(body): true}
	for _, r := range brs {
		cuts[r.start], cuts[r.end] = true, true
		if r.style == core.StyleMention {
			mentions = append(mentions, r)
		}
	}
	points := make([]int, 0, len(cuts))
	for p := range cuts {
		inside := false
		for _, m := range mentions {
			if p > m.start && p < m.end {
				inside = true
			}
		}
		if !inside {
			points = append(points, p)
		}
	}
	sort.Ints(points)

	covered := func(st core.RangeStyle, a, b int) bool {
		for _, r := range brs {
			if r.style == st && r.start <= a && r.end >= b {
				return true
			}
		}
		return false
	}

	var out strings.Builder
	var spans []MentionSpan
	var stack []int // indexes into markupStyles, in opening order
	for i := 0; i+1 < len(points); i++ {
		a, b := points[i], points[i+1]
		if a >= b {
			continue
		}
		want := make([]bool, len(markupStyles))
		for k, ms := range markupStyles {
			want[k] = covered(ms.style, a, b)
		}
		// Close exactly the styles that end here (innermost first). The
		// parser pairs delimiters per kind, so overlapping, non-nested
		// ranges need no reopening.
		for j := len(stack) - 1; j >= 0; j-- {
			if !want[stack[j]] {
				out.WriteString(markupStyles[stack[j]].delim)
				stack = append(stack[:j], stack[j+1:]...)
			}
		}
		for k := range markupStyles {
			if want[k] && !contains(stack, k) {
				out.WriteString(markupStyles[k].delim)
				stack = append(stack, k)
			}
		}
		seg := body[a:b]
		var mention *byteRange
		for mi := range mentions {
			if mentions[mi].start == a && mentions[mi].end == b {
				mention = &mentions[mi]
			}
		}
		switch {
		case mention != nil && resolve == nil:
			out.WriteString(MentionToken(mention.aci))
		case mention != nil:
			start := out.Len()
			out.WriteString(MentionText(mention.aci, resolve))
			spans = append(spans, MentionSpan{Start: start, End: out.Len(), ACI: mention.aci})
		case covered(core.StyleMonospace, a, b):
			out.WriteString(monoMarkup(seg))
		default:
			out.WriteString(escapeLiteral(seg))
		}
	}
	for j := len(stack) - 1; j >= 0; j-- {
		out.WriteString(markupStyles[stack[j]].delim)
	}
	return out.String(), spans
}

func contains(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// monoMarkup writes s as one or more adjacent code spans. Code spans cannot
// contain "```", so s is split between the second and third backtick of
// every longer backtick run; Parse merges the adjacent monospace ranges.
func monoMarkup(s string) string {
	var b strings.Builder
	start, run := 0, 0
	for i := 0; i < len(s); i++ {
		if s[i] != '`' {
			run = 0
			continue
		}
		run++
		if run == 3 {
			code, _ := codeMarkup(s[start:i])
			b.WriteString(code)
			start, run = i, 1
		}
	}
	code, _ := codeMarkup(s[start:])
	b.WriteString(code)
	return b.String()
}

// codeMarkup wraps s in backticks so that scanCode returns exactly s.
func codeMarkup(s string) (string, bool) {
	switch {
	case strings.Contains(s, "```"):
		return "", false
	case !strings.ContainsAny(s, "`\n"):
		return "`" + s + "`", true
	case strings.Contains(s, "\n") || strings.HasSuffix(s, "`"):
		return "```\n" + s + "\n```", true
	default:
		return "```" + s + "```", true
	}
}

// escapeLiteral escapes markup characters in s so Parse reads them as text.
// Characters that cannot be interpreted as markup where they stand (such as
// the underscore in snake_case) are left alone to keep edited text readable.
func escapeLiteral(s string) string {
	rs := []rune(s)
	var b strings.Builder
	for i, r := range rs {
		var prev, next rune = -1, -1
		if i > 0 {
			prev = rs[i-1]
		}
		if i+1 < len(rs) {
			next = rs[i+1]
		}
		esc := false
		switch r {
		case '\\':
			esc = next == -1 || strings.ContainsRune(escapable, next)
		case '*', '_', '~':
			esc = !(prev != -1 && next != -1 && isAlnum(prev) && isAlnum(next))
		case '|':
			esc = prev == -1 || next == -1 || prev == '|' || next == '|'
		case '`':
			esc = true
		case '@':
			esc = next == '{' || next == -1
		}
		if esc {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
