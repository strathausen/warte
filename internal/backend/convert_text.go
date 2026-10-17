package backend

import (
	"sort"
	"unicode/utf16"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"

	"warte/internal/core"
)

// maxBodyRanges bounds the number of body ranges accepted from a message so
// a malicious sender cannot make rendering quadratic.
const maxBodyRanges = 250

// utf16Len returns the length of s in UTF-16 code units, the unit Signal uses
// for body range offsets.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if l := utf16.RuneLen(r); l > 0 {
			n += l
		} else {
			n++ // invalid UTF-8 decodes to U+FFFD, one code unit
		}
	}
	return n
}

// styleFromSignal maps a Signal body range style to warte's.
func styleFromSignal(s signalpb.BodyRange_Style) core.RangeStyle {
	switch s {
	case signalpb.BodyRange_BOLD:
		return core.StyleBold
	case signalpb.BodyRange_ITALIC:
		return core.StyleItalic
	case signalpb.BodyRange_SPOILER:
		return core.StyleSpoiler
	case signalpb.BodyRange_STRIKETHROUGH:
		return core.StyleStrikethrough
	case signalpb.BodyRange_MONOSPACE:
		return core.StyleMonospace
	}
	return core.StyleNone
}

// styleToSignal maps a warte style to Signal's; ok is false for styles that
// have no Signal equivalent (none, mention).
func styleToSignal(s core.RangeStyle) (signalpb.BodyRange_Style, bool) {
	switch s {
	case core.StyleBold:
		return signalpb.BodyRange_BOLD, true
	case core.StyleItalic:
		return signalpb.BodyRange_ITALIC, true
	case core.StyleSpoiler:
		return signalpb.BodyRange_SPOILER, true
	case core.StyleStrikethrough:
		return signalpb.BodyRange_STRIKETHROUGH, true
	case core.StyleMonospace:
		return signalpb.BodyRange_MONOSPACE, true
	}
	return signalpb.BodyRange_NONE, false
}

// rangesFromSignal converts Signal body ranges to warte's, dropping ranges
// that are empty, out of bounds for body, unknown or malformed. The result
// is sorted by start offset (stable), mentions included.
func rangesFromSignal(body string, in []*signalpb.BodyRange) []core.BodyRange {
	if len(in) == 0 {
		return nil
	}
	limit := utf16Len(body)
	out := make([]core.BodyRange, 0, min(len(in), maxBodyRanges))
	for _, r := range in {
		if r == nil || len(out) >= maxBodyRanges {
			continue
		}
		start, length := int(r.GetStart()), int(r.GetLength())
		if length <= 0 || start < 0 || start >= limit || start+length > limit {
			continue
		}
		br := core.BodyRange{Start: start, Length: length}
		switch v := r.GetAssociatedValue().(type) {
		case *signalpb.BodyRange_Style_:
			br.Style = styleFromSignal(v.Style)
			if br.Style == core.StyleNone {
				continue
			}
		case *signalpb.BodyRange_MentionAci:
			aci := aciFromBinaryOrString(nil, v.MentionAci)
			if aci == "" {
				continue
			}
			br.Style, br.MentionACI = core.StyleMention, aci
		case *signalpb.BodyRange_MentionAciBinary:
			aci := aciFromBinaryOrString(v.MentionAciBinary, "")
			if aci == "" {
				continue
			}
			br.Style, br.MentionACI = core.StyleMention, aci
		default:
			continue
		}
		out = append(out, br)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	if len(out) == 0 {
		return nil
	}
	return out
}

// rangesToSignal converts warte body ranges to Signal's, dropping ranges that
// are empty, out of bounds for body or carry no style.
func rangesToSignal(body string, in []core.BodyRange) []*signalpb.BodyRange {
	if len(in) == 0 {
		return nil
	}
	limit := utf16Len(body)
	var out []*signalpb.BodyRange
	for _, r := range in {
		if r.Length <= 0 || r.Start < 0 || r.Start+r.Length > limit {
			continue
		}
		br := &signalpb.BodyRange{Start: proto.Uint32(uint32(r.Start)), Length: proto.Uint32(uint32(r.Length))}
		if r.Style == core.StyleMention {
			u, err := uuid.Parse(string(r.MentionACI))
			if err != nil {
				continue
			}
			br.AssociatedValue = &signalpb.BodyRange_MentionAci{MentionAci: u.String()}
		} else {
			st, ok := styleToSignal(r.Style)
			if !ok {
				continue
			}
			br.AssociatedValue = &signalpb.BodyRange_Style_{Style: st}
		}
		out = append(out, br)
	}
	return out
}

// mentionsACI reports whether ranges mention aci.
func mentionsACI(ranges []core.BodyRange, aci core.ACI) bool {
	if aci == "" {
		return false
	}
	for _, r := range ranges {
		if r.Style == core.StyleMention && r.MentionACI == aci {
			return true
		}
	}
	return false
}
