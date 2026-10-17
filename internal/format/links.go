package format

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// LinkKind classifies a detected link.
type LinkKind int

// Link kinds, in decreasing priority when matches overlap.
const (
	LinkURL   LinkKind = iota // http(s):// or www. or bare domain
	LinkEmail                 // user@example.com → mailto:
	LinkPhone                 // +49 30 1234567 → tel:
)

// Link is a link found in plain text. Start and End are byte offsets into the
// searched text (half-open); URL is the canonical target to open, e.g.
// "https://www.example.com" for the text "www.example.com".
type Link struct {
	Start, End int
	Text       string
	URL        string
	Kind       LinkKind
}

// commonTLDs are the top-level domains accepted for bare domains such as
// "example.com" (no scheme, no www.). The list is deliberately conservative
// to avoid linkifying things like "file.txt" or "main.go".
var commonTLDs = map[string]bool{
	"com": true, "org": true, "net": true, "edu": true, "gov": true, "io": true,
	"dev": true, "app": true, "info": true, "biz": true, "me": true, "co": true,
	"tv": true, "fm": true, "gg": true, "ai": true, "eu": true, "de": true,
	"uk": true, "fr": true, "nl": true, "at": true, "ch": true, "it": true,
	"es": true, "se": true, "no": true, "dk": true, "fi": true, "pl": true,
	"cz": true, "be": true, "pt": true, "ie": true, "us": true, "ca": true,
	"au": true, "nz": true, "jp": true, "br": true, "in": true, "ru": true,
	"cn": true, "xyz": true, "online": true, "social": true, "news": true,
	"blog": true, "page": true, "site": true, "tech": true, "cloud": true,
	"wiki": true, "store": true, "shop": true,
}

var (
	reScheme = regexp.MustCompile(`(?i)\bhttps?://[^\s<>"\x{FFFC}]+`)
	reWWW    = regexp.MustCompile(`(?i)\bwww\.[a-z0-9-]+(?:\.[a-z0-9-]+)+(?:[/?#][^\s<>"\x{FFFC}]*)?`)
	reEmail  = regexp.MustCompile(`(?i)\b[a-z0-9._%+-]+@[a-z0-9-]+(?:\.[a-z0-9-]+)*\.[a-z]{2,}\b`)
	reBare   = regexp.MustCompile(`(?i)\b[a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)*\.([a-z]{2,10})\b(?:[/?#][^\s<>"\x{FFFC}]*)?`)
	rePhone  = regexp.MustCompile(`\+[1-9][0-9 ()-]{6,18}[0-9]`)
)

// FindLinks returns the links in text, sorted by position and never
// overlapping. It detects http(s) URLs, "www." hosts, bare domains with a
// common TLD, email addresses and international phone numbers starting with
// "+". Trailing punctuation and unbalanced closing brackets are excluded.
func FindLinks(text string) []Link {
	if text == "" {
		return nil
	}
	type cand struct {
		Link
		prio int
	}
	var cands []cand
	add := func(start, end int, kind LinkKind, prio int, mk func(string) string) {
		end = trimLinkEnd(text, start, end)
		if end <= start {
			return
		}
		t := text[start:end]
		cands = append(cands, cand{Link{Start: start, End: end, Text: t, URL: mk(t), Kind: kind}, prio})
	}
	for _, m := range reScheme.FindAllStringIndex(text, -1) {
		add(m[0], m[1], LinkURL, 0, func(s string) string { return s })
	}
	for _, m := range reEmail.FindAllStringIndex(text, -1) {
		add(m[0], m[1], LinkEmail, 1, func(s string) string { return "mailto:" + s })
	}
	for _, m := range reWWW.FindAllStringIndex(text, -1) {
		if m[0] > 0 && !boundaryBefore(text, m[0]) {
			continue
		}
		add(m[0], m[1], LinkURL, 2, func(s string) string { return "https://" + s })
	}
	for _, m := range reBare.FindAllStringSubmatchIndex(text, -1) {
		tld := strings.ToLower(text[m[2]:m[3]])
		if !commonTLDs[tld] || (m[0] > 0 && !boundaryBefore(text, m[0])) {
			continue
		}
		add(m[0], m[1], LinkURL, 3, func(s string) string { return "https://" + s })
	}
	for _, m := range rePhone.FindAllStringIndex(text, -1) {
		if m[0] > 0 && !boundaryBefore(text, m[0]) {
			continue
		}
		if m[1] < len(text) {
			if r, _ := utf8.DecodeRuneInString(text[m[1]:]); isWordRune(r) {
				continue
			}
		}
		add(m[0], m[1], LinkPhone, 4, func(s string) string {
			var b strings.Builder
			b.WriteString("tel:+")
			for _, r := range s {
				if r >= '0' && r <= '9' {
					b.WriteRune(r)
				}
			}
			return b.String()
		})
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].Start != cands[j].Start {
			return cands[i].Start < cands[j].Start
		}
		if cands[i].prio != cands[j].prio {
			return cands[i].prio < cands[j].prio
		}
		return cands[i].End > cands[j].End
	})
	var out []Link
	end := 0
	for _, c := range cands {
		if c.Start < end {
			continue
		}
		// A lower-priority match starting at the same place as a later
		// higher-priority one (e.g. bare "user" in an email) is handled by
		// the priority sort; here we only need to drop overlaps.
		out = append(out, c.Link)
		end = c.End
	}
	return out
}

// boundaryBefore reports whether the character before byte offset i allows a
// link to start at i (not part of a word, domain, path or email).
func boundaryBefore(text string, i int) bool {
	r, _ := utf8.DecodeLastRuneInString(text[:i])
	if isWordRune(r) {
		return false
	}
	switch r {
	case '.', '@', '/', '-', '_', ':', '+', '%', '#', '=', '&', '?':
		return false
	}
	return true
}

func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// trimLinkEnd strips trailing punctuation and unbalanced closing brackets
// from the candidate link text[start:end] and returns the new end.
func trimLinkEnd(text string, start, end int) int {
	// Bracket balance is counted once and updated while trimming, keeping
	// this linear for hostile input such as "http://x" + 10k ")".
	pairs := map[rune]rune{')': '(', ']': '[', '}': '{', '>': '<'}
	var count map[rune]int
	for end > start {
		r, size := utf8.DecodeLastRuneInString(text[start:end])
		switch r {
		case '.', ',', ';', ':', '!', '?', '\'', '"', '*', '_', '~', '|', '`', '…':
			end -= size
			continue
		case ')', ']', '}', '>':
			if count == nil {
				count = map[rune]int{}
				for _, c := range text[start:end] {
					switch c {
					case '(', ')', '[', ']', '{', '}', '<', '>':
						count[c]++
					}
				}
			}
			if count[pairs[r]] < count[r] {
				count[r]--
				end -= size
				continue
			}
		}
		break
	}
	return end
}
