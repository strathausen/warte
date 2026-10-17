package finder

import (
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Query is a parsed global-search query. Free text goes to Text; filters
// are kept as typed (names are resolved against contacts and chats by the
// search overlay, see Resolve).
//
// Syntax (filters may appear anywhere, values may be "double quoted"):
//
//	from:bob        sender (contact name, @username, +phone, "me")
//	in:book         chat (fuzzy title match, "here" = the open chat)
//	has:image       image|photo, video, audio|voice, file|doc, link|url
//	                (exactly the core.SearchQuery.HasType values; anything
//	                else is reported in Errors and never sent to the backend)
//	before:2026-01  sent before the start of the period
//	after:2025-12   sent on or after the start of the period
//	on:2026-01-05   sent within the period
//
// Dates are YYYY, YYYY-MM, YYYY-MM-DD, today, yesterday, or a relative age
// such as 3d, 2w, 6m, 1y (that long ago). A key that is not a known filter
// (e.g. "http:") is kept as text.
type Query struct {
	Text   string
	From   string
	In     string
	Has    string
	Before time.Time
	After  time.Time
	// Errors lists filters that could not be understood ("before:soon").
	Errors []string
	// Filters are the recognized filter tokens in input order, e.g.
	// "from:bob", for display.
	Filters []string
}

// IsEmpty reports whether the query has neither text nor filters (a
// malformed filter, listed in Errors, makes the query non-empty so it can
// be reported).
func (q Query) IsEmpty() bool {
	return strings.TrimSpace(q.Text) == "" && q.From == "" && q.In == "" && q.Has == "" && len(q.Errors) == 0 &&
		q.Before.IsZero() && q.After.IsZero()
}

// HasFilters reports whether any filter is set.
func (q Query) HasFilters() bool {
	return q.From != "" || q.In != "" || q.Has != "" || !q.Before.IsZero() || !q.After.IsZero()
}

var hasAliases = map[string]string{
	"image": "image", "images": "image", "photo": "image", "photos": "image", "img": "image", "picture": "image", "pic": "image",
	"video": "video", "videos": "video", "vid": "video",
	"audio": "audio", "voice": "audio", "sound": "audio",
	"file": "file", "files": "file", "doc": "file", "document": "file", "attachment": "file",
	"link": "link", "links": "link", "url": "link",
}

// HasTypes lists the canonical has: values.
var HasTypes = []string{"image", "video", "audio", "file", "link"}

// filterKeys are the recognized filter names.
var filterKeys = []string{"from", "in", "has", "before", "after", "on"}

// token is one whitespace-separated word of the input with its rune span.
type token struct {
	text       string
	start, end int // rune offsets in the input [start, end)
}

// tokenize splits s at unquoted whitespace. Quotes are kept in the token text.
func tokenize(s string) []token {
	var out []token
	rs := []rune(s)
	i := 0
	for i < len(rs) {
		for i < len(rs) && unicode.IsSpace(rs[i]) {
			i++
		}
		if i >= len(rs) {
			break
		}
		start := i
		inQuote := false
		for i < len(rs) && (inQuote || !unicode.IsSpace(rs[i])) {
			if rs[i] == '"' {
				inQuote = !inQuote
			}
			i++
		}
		out = append(out, token{text: string(rs[start:i]), start: start, end: i})
	}
	return out
}

// splitFilter returns the lower-cased key and the unquoted value of a
// "key:value" token whose key is a known filter.
func splitFilter(tok string) (key, val string, ok bool) {
	k, v, found := strings.Cut(tok, ":")
	if !found {
		return "", "", false
	}
	k = strings.ToLower(k)
	known := false
	for _, f := range filterKeys {
		if f == k {
			known = true
		}
	}
	if !known {
		return "", "", false
	}
	v = strings.TrimSpace(strings.ReplaceAll(v, `"`, ""))
	return k, v, true
}

// filterSpans returns, for display, the rune spans of the recognized filter
// tokens in s.
func filterSpans(s string) [][2]int {
	var out [][2]int
	for _, t := range tokenize(s) {
		if _, _, ok := splitFilter(t.text); ok {
			out = append(out, [2]int{t.start, t.end})
		}
	}
	return out
}

// ParseQuery parses the global search syntax. now anchors relative dates
// (today, 3d, …) and supplies the time zone.
func ParseQuery(s string, now time.Time) Query {
	var q Query
	var text []string
	for _, t := range tokenize(s) {
		key, val, ok := splitFilter(t.text)
		if !ok {
			text = append(text, strings.ReplaceAll(t.text, `"`, ""))
			continue
		}
		if val == "" {
			// A filter still being typed ("from:") is ignored, not text.
			continue
		}
		q.Filters = append(q.Filters, key+":"+val)
		switch key {
		case "from":
			q.From = val
		case "in":
			q.In = val
		case "has":
			h, ok := hasAliases[strings.ToLower(val)]
			if !ok {
				q.Errors = append(q.Errors, "has:"+val)
				continue
			}
			q.Has = h
		case "before", "after", "on":
			start, end, ok := parsePeriod(val, now)
			if !ok {
				q.Errors = append(q.Errors, key+":"+val)
				continue
			}
			switch key {
			case "before":
				q.Before = start
			case "after":
				// Backends compare strictly (SentAt.After), so step back a
				// nanosecond to include the first instant of the period.
				q.After = start.Add(-time.Nanosecond)
			case "on":
				q.After = start.Add(-time.Nanosecond)
				q.Before = end
			}
		}
	}
	q.Text = strings.Join(text, " ")
	return q
}

// parsePeriod parses a date value into the half-open interval [start, end).
func parsePeriod(v string, now time.Time) (start, end time.Time, ok bool) {
	loc := now.Location()
	day := func(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc) }
	lv := strings.ToLower(v)
	switch lv {
	case "today":
		s := day(now)
		return s, s.AddDate(0, 0, 1), true
	case "yesterday":
		s := day(now).AddDate(0, 0, -1)
		return s, s.AddDate(0, 0, 1), true
	}
	// Relative ages: 3d, 2w, 6m, 1y, 12h.
	if n := len(lv); n >= 2 {
		if num, err := strconv.Atoi(lv[:n-1]); err == nil && num >= 0 && num < 10000 {
			var s time.Time
			switch lv[n-1] {
			case 'h':
				s = now.Add(-time.Duration(num) * time.Hour)
				return s, s.Add(time.Hour), true
			case 'd':
				s = day(now).AddDate(0, 0, -num)
				return s, s.AddDate(0, 0, 1), true
			case 'w':
				s = day(now).AddDate(0, 0, -7*num)
				return s, s.AddDate(0, 0, 7), true
			case 'm':
				s = day(now).AddDate(0, -num, 0)
				return s, s.AddDate(0, 1, 0), true
			case 'y':
				s = day(now).AddDate(-num, 0, 0)
				return s, s.AddDate(1, 0, 0), true
			}
		}
	}
	for _, f := range []struct {
		layout string
		step   func(time.Time) time.Time
	}{
		{"2006-01-02", func(t time.Time) time.Time { return t.AddDate(0, 0, 1) }},
		{"2006-1-2", func(t time.Time) time.Time { return t.AddDate(0, 0, 1) }},
		{"2006-01", func(t time.Time) time.Time { return t.AddDate(0, 1, 0) }},
		{"2006-1", func(t time.Time) time.Time { return t.AddDate(0, 1, 0) }},
		{"2006/01/02", func(t time.Time) time.Time { return t.AddDate(0, 0, 1) }},
		{"2006", func(t time.Time) time.Time { return t.AddDate(1, 0, 0) }},
	} {
		if t, err := time.ParseInLocation(f.layout, v, loc); err == nil {
			if t.Year() < 1970 || t.Year() > 9999 {
				return time.Time{}, time.Time{}, false
			}
			return t, f.step(t), true
		}
	}
	return time.Time{}, time.Time{}, false
}
