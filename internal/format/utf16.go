// Package format implements warte's text handling for Signal messages in a
// terminal: UTF-16 body ranges to styled spans, composer markup, link
// detection, emoji shortcodes, ANSI-aware wrapping, snippets and the time and
// duration formats used across the UI.
//
// The core logic is UI-agnostic; only render.go depends on Lip Gloss.
package format

import (
	"sort"
	"unicode/utf8"

	"github.com/rivo/uniseg"

	"warte/internal/core"
)

// MentionPlaceholder is the object replacement character Signal puts in a
// message body where a mention is rendered.
const MentionPlaceholder = '\uFFFC'

// UTF16Len returns the length of s in UTF-16 code units, the unit Signal uses
// for body range offsets. Invalid UTF-8 bytes count as one unit each (they
// decode to U+FFFD).
func UTF16Len(s string) int {
	n := 0
	for _, r := range s {
		n += runeUnits(r)
	}
	return n
}

func runeUnits(r rune) int {
	if r >= 0x10000 && r <= utf8.MaxRune {
		return 2
	}
	return 1
}

// UTF16ToByte converts a UTF-16 offset into a byte offset into s. Offsets are
// clamped to [0, len(s)]; an offset that points into the middle of a
// surrogate pair is rounded down to the start of that character.
func UTF16ToByte(s string, off int) int {
	if off <= 0 {
		return 0
	}
	u := 0
	for i, r := range s {
		if u >= off {
			return i
		}
		n := runeUnits(r)
		if u+n > off {
			return i
		}
		u += n
	}
	return len(s)
}

// ByteToUTF16 converts a byte offset into s into a UTF-16 offset. The byte
// offset is clamped to [0, len(s)]; one that points into the middle of a
// UTF-8 sequence is rounded down to the start of that character.
func ByteToUTF16(s string, b int) int {
	if b <= 0 {
		return 0
	}
	if b > len(s) {
		b = len(s)
	}
	u := 0
	for i := 0; i < b; {
		r, size := utf8.DecodeRuneInString(s[i:])
		if i+size > b {
			break
		}
		u += runeUnits(r)
		i += size
	}
	return u
}

// offsetIndex maps between UTF-16 offsets and byte offsets of one string in
// O(1) after an O(n) build.
type offsetIndex struct {
	s     string
	u2b   []int // u2b[u] = byte offset of UTF-16 unit u (len = units+1); mid-pair units map to the pair start
	units int
}

func newOffsetIndex(s string) *offsetIndex {
	ix := &offsetIndex{s: s, u2b: make([]int, 0, len(s)+1)}
	for i, r := range s {
		for k := 0; k < runeUnits(r); k++ {
			ix.u2b = append(ix.u2b, i)
		}
	}
	ix.u2b = append(ix.u2b, len(s))
	ix.units = len(ix.u2b) - 1
	return ix
}

// byteAt returns the byte offset for UTF-16 offset u, clamped.
func (ix *offsetIndex) byteAt(u int) int {
	if u <= 0 {
		return 0
	}
	if u >= ix.units {
		return len(ix.s)
	}
	return ix.u2b[u]
}

// unitAt returns the UTF-16 offset of byte offset b (b must be a rune start).
func (ix *offsetIndex) unitAt(b int) int {
	return sort.SearchInts(ix.u2b, b)
}

// graphemeBounds returns the sorted byte offsets of all grapheme cluster
// boundaries in s, including 0 and len(s).
func graphemeBounds(s string) []int {
	bounds := []int{0}
	rest := s
	state := -1
	pos := 0
	for len(rest) > 0 {
		var cluster string
		cluster, rest, _, state = uniseg.FirstGraphemeClusterInString(rest, state)
		pos += len(cluster)
		bounds = append(bounds, pos)
	}
	return bounds
}

// floorBound returns the largest boundary <= b.
func floorBound(bounds []int, b int) int {
	i := sort.SearchInts(bounds, b)
	if i < len(bounds) && bounds[i] == b {
		return b
	}
	if i == 0 {
		return bounds[0]
	}
	return bounds[i-1]
}

// ceilBound returns the smallest boundary >= b.
func ceilBound(bounds []int, b int) int {
	i := sort.SearchInts(bounds, b)
	if i >= len(bounds) {
		return bounds[len(bounds)-1]
	}
	return bounds[i]
}

// byteRange is a half-open byte interval of a body with a style.
type byteRange struct {
	start, end int
	style      core.RangeStyle
	aci        core.ACI
}

// knownStyle reports whether st is a style this package understands.
func knownStyle(st core.RangeStyle) bool {
	switch st {
	case core.StyleBold, core.StyleItalic, core.StyleSpoiler,
		core.StyleStrikethrough, core.StyleMonospace, core.StyleMention:
		return true
	}
	return false
}

// toByteRanges converts UTF-16 ranges to byte ranges over body. Offsets are
// clamped to the body, widened to whole grapheme clusters, unknown styles and
// empty ranges dropped, overlapping mentions resolved (earliest wins) and
// same-style formatting ranges merged. The result is sorted by start.
func toByteRanges(body string, ranges []core.BodyRange) []byteRange {
	if len(ranges) == 0 || body == "" {
		return nil
	}
	ix := newOffsetIndex(body)
	gb := graphemeBounds(body)
	var mentions []byteRange
	perStyle := map[core.RangeStyle][]byteRange{}
	for _, r := range ranges {
		if !knownStyle(r.Style) || r.Length <= 0 {
			continue
		}
		start := r.Start
		if start < 0 {
			start = 0
		}
		end := r.Start + r.Length
		if end < r.Start { // integer overflow
			end = ix.units
		}
		if end > ix.units {
			end = ix.units
		}
		if start >= end {
			continue
		}
		// Rune-align first: an end in the middle of a surrogate pair rounds
		// up to the end of that character.
		bs, be := ix.byteAt(start), ix.byteAt(end)
		if end < ix.units && end > 0 && be == ix.byteAt(end-1) {
			_, size := utf8.DecodeRuneInString(body[be:])
			be += size
		}
		// Formatting ranges are widened to whole grapheme clusters. Mentions
		// are not: they replace exactly the placeholder they cover, and
		// widening would swallow e.g. a combining mark after U+FFFC.
		if r.Style != core.StyleMention {
			bs, be = floorBound(gb, bs), ceilBound(gb, be)
		}
		if bs >= be {
			continue
		}
		br := byteRange{start: bs, end: be, style: r.Style, aci: r.MentionACI}
		if r.Style == core.StyleMention {
			mentions = append(mentions, br)
		} else {
			perStyle[r.Style] = append(perStyle[r.Style], br)
		}
	}
	var out []byteRange
	sort.SliceStable(mentions, func(i, j int) bool { return mentions[i].start < mentions[j].start })
	lastEnd := -1
	for _, m := range mentions {
		if m.start < lastEnd {
			continue
		}
		out = append(out, m)
		lastEnd = m.end
	}
	for st, list := range perStyle {
		sort.Slice(list, func(i, j int) bool { return list[i].start < list[j].start })
		cur := list[0]
		for _, r := range list[1:] {
			if r.start <= cur.end {
				if r.end > cur.end {
					cur.end = r.end
				}
				continue
			}
			out = append(out, cur)
			cur = r
		}
		cur.style = st
		out = append(out, cur)
	}
	sortByteRanges(out)
	return out
}

func sortByteRanges(rs []byteRange) {
	sort.Slice(rs, func(i, j int) bool {
		if rs[i].start != rs[j].start {
			return rs[i].start < rs[j].start
		}
		if rs[i].style != rs[j].style {
			return rs[i].style < rs[j].style
		}
		return rs[i].end < rs[j].end
	})
}

// fromByteRanges converts byte ranges back into sorted UTF-16 body ranges.
func fromByteRanges(body string, rs []byteRange) []core.BodyRange {
	if len(rs) == 0 {
		return nil
	}
	ix := newOffsetIndex(body)
	out := make([]core.BodyRange, 0, len(rs))
	for _, r := range rs {
		s, e := ix.unitAt(r.start), ix.unitAt(r.end)
		if e <= s {
			continue
		}
		out = append(out, core.BodyRange{Start: s, Length: e - s, Style: r.style, MentionACI: r.aci})
	}
	SortRanges(out)
	return out
}

// SortRanges sorts body ranges in place by start, then style, then length,
// then mention ACI, giving ranges a canonical order for comparison.
func SortRanges(rs []core.BodyRange) {
	sort.SliceStable(rs, func(i, j int) bool {
		a, b := rs[i], rs[j]
		if a.Start != b.Start {
			return a.Start < b.Start
		}
		if a.Style != b.Style {
			return a.Style < b.Style
		}
		if a.Length != b.Length {
			return a.Length < b.Length
		}
		return a.MentionACI < b.MentionACI
	})
}

// NormalizeRanges returns the canonical form of ranges for body: offsets
// clamped and widened to whole grapheme clusters, unknown styles and empty
// ranges dropped, overlapping mentions resolved, overlapping or adjacent
// ranges of the same style merged, and the result sorted with SortRanges.
// Parse always returns normalized ranges, and ToMarkup followed by Parse
// reproduces the normalized form exactly.
func NormalizeRanges(body string, ranges []core.BodyRange) []core.BodyRange {
	return fromByteRanges(body, toByteRanges(body, ranges))
}
