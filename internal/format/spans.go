package format

import (
	"sort"
	"strings"

	"warte/internal/core"
)

// Span is a run of message text with uniform styling, produced by Spans.
type Span struct {
	Text    string
	Bold    bool
	Italic  bool
	Strike  bool
	Mono    bool
	Spoiler bool
	// Mention is set for a resolved mention; Text is then "@Name".
	Mention    bool
	MentionACI core.ACI
	// Link is the target URL when the text is a detected link ("" otherwise).
	Link string
}

// sameStyle reports whether a and b can be merged into one span.
func (s Span) sameStyle(o Span) bool {
	return s.Bold == o.Bold && s.Italic == o.Italic && s.Strike == o.Strike &&
		s.Mono == o.Mono && s.Spoiler == o.Spoiler && s.Link == o.Link &&
		!s.Mention && !o.Mention
}

// UnknownMention is the display name used when a mention cannot be resolved.
const UnknownMention = "Unknown"

// MentionText returns the "@Name" text for a mention of aci, using resolve if
// non-nil and falling back to UnknownMention. The name is untrusted (it is
// another user's profile name), so it is reduced to one line of printable
// text with sanitizeLine.
func MentionText(aci core.ACI, resolve func(core.ACI) string) string {
	name := ""
	if resolve != nil {
		name = strings.TrimPrefix(strings.TrimSpace(sanitizeLine(resolve(aci))), "@")
	}
	if name == "" {
		name = UnknownMention
	}
	return "@" + name
}

// Spans splits body into styled segments according to Signal body ranges
// (UTF-16 offsets). Mention ranges replace the text they cover (normally the
// U+FFFC placeholder) with "@Name" resolved through resolve (which may be
// nil). Overlapping and nested ranges combine; out-of-range or inverted
// offsets are clamped or dropped; range edges are widened to whole grapheme
// clusters so surrogate pairs, skin-tone modifiers, ZWJ sequences and flags
// are never split. Detected links (see FindLinks) get Span.Link set.
// Adjacent segments with identical styling are merged. Spans never panics.
func Spans(body string, ranges []core.BodyRange, resolve func(core.ACI) string) []Span {
	if body == "" {
		return nil
	}
	brs := toByteRanges(body, ranges)
	var mentions, styles []byteRange
	for _, r := range brs {
		if r.style == core.StyleMention {
			mentions = append(mentions, r)
		} else {
			styles = append(styles, r)
		}
	}
	gb := graphemeBounds(body)
	var links []Link
	mc := 0
	for _, l := range FindLinks(body) {
		l.Start, l.End = floorBound(gb, l.Start), ceilBound(gb, l.End)
		if n := len(links); n > 0 && l.Start < links[n-1].End {
			continue // grapheme widening made it touch the previous link
		}
		for mc < len(mentions) && mentions[mc].end <= l.Start {
			mc++
		}
		if mc < len(mentions) && mentions[mc].start < l.End {
			continue // overlaps a mention
		}
		links = append(links, l)
	}

	// Sweep over the sorted cut points. Mentions and links are sorted and
	// non-overlapping, so they are walked with cursors; formatting styles
	// are tracked with per-style open counts. This keeps Spans
	// O((n + r) log r) even for hostile messages with thousands of ranges.
	type event struct {
		at    int
		style core.RangeStyle
		delta int
	}
	events := make([]event, 0, 2*len(styles))
	points := make([]int, 0, 2*len(brs)+2*len(links)+2)
	points = append(points, 0, len(body))
	for _, r := range styles {
		events = append(events, event{r.start, r.style, +1}, event{r.end, r.style, -1})
		points = append(points, r.start, r.end)
	}
	for _, m := range mentions {
		points = append(points, m.start, m.end)
	}
	for _, l := range links {
		points = append(points, l.Start, l.End)
	}
	sort.Ints(points)
	sort.Slice(events, func(i, j int) bool { return events[i].at < events[j].at })
	// Drop duplicates and points strictly inside a mention.
	uniq := points[:0]
	mi := 0
	for i, p := range points {
		if i > 0 && p == points[i-1] {
			continue
		}
		for mi < len(mentions) && mentions[mi].end <= p {
			mi++
		}
		if mi < len(mentions) && p > mentions[mi].start && p < mentions[mi].end {
			continue
		}
		uniq = append(uniq, p)
	}
	points = uniq

	var open [core.StyleMention + 1]int
	var out []Span
	ei, mi, li := 0, 0, 0
	for i := 0; i+1 < len(points); i++ {
		a, b := points[i], points[i+1]
		for ei < len(events) && events[ei].at <= a {
			open[events[ei].style] += events[ei].delta
			ei++
		}
		sp := Span{
			Text:    body[a:b],
			Bold:    open[core.StyleBold] > 0,
			Italic:  open[core.StyleItalic] > 0,
			Strike:  open[core.StyleStrikethrough] > 0,
			Mono:    open[core.StyleMonospace] > 0,
			Spoiler: open[core.StyleSpoiler] > 0,
		}
		for mi < len(mentions) && mentions[mi].end <= a {
			mi++
		}
		if mi < len(mentions) && mentions[mi].start == a && mentions[mi].end == b {
			m := mentions[mi]
			sp.Mention = true
			sp.MentionACI = m.aci
			sp.Text = MentionText(m.aci, resolve)
		}
		for li < len(links) && links[li].End <= a {
			li++
		}
		if !sp.Mention && li < len(links) && links[li].Start <= a && links[li].End >= b {
			sp.Link = links[li].URL
		}
		if n := len(out); n > 0 && out[n-1].sameStyle(sp) {
			out[n-1].Text += sp.Text
			continue
		}
		out = append(out, sp)
	}
	return out
}

// PlainText concatenates the text of spans, i.e. the message as displayed
// with mentions resolved and no styling.
func PlainText(spans []Span) string {
	var b strings.Builder
	for _, s := range spans {
		b.WriteString(s.Text)
	}
	return b.String()
}
