package format

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// TimeFormatter formats timestamps the way Signal does in lists and
// conversation views. The zero value uses a 24-hour clock.
type TimeFormatter struct {
	// Hour12 selects a 12-hour clock ("3:04 PM") instead of "15:04".
	Hour12 bool
}

// Clock formats the time of day of t.
func (f TimeFormatter) Clock(t time.Time) string {
	if f.Hour12 {
		return t.Format("3:04 PM")
	}
	return t.Format("15:04")
}

// RelativeTime formats t relative to now, Signal style:
//
//	< 1 minute ago (or slightly in the future)  "now"
//	< 1 hour ago                                "5m"
//	same calendar day                           "12:41"
//	within the last 6 days                      "Mon"
//	same year                                   "Mar 3"
//	otherwise                                   "Mar 3, 2025"
//
// t is converted to now's location; times more than a minute in the future
// are formatted by calendar day like past ones.
func (f TimeFormatter) RelativeTime(t, now time.Time) string {
	t = t.In(now.Location())
	d := now.Sub(t)
	if d < time.Minute && d > -time.Minute {
		return "now"
	}
	if d > 0 && d < time.Hour {
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	days := dayDiff(t, now)
	switch {
	case days == 0:
		return f.Clock(t)
	case days > 0 && days < 7:
		return t.Format("Mon")
	case t.Year() == now.Year():
		return t.Format("Jan 2")
	default:
		return t.Format("Jan 2, 2006")
	}
}

// DayHeader formats the day separator shown above a day's messages:
// "Today", "Yesterday", the weekday name within the last week ("Monday"),
// "March 3" within the same year, otherwise "March 3, 2025".
func (f TimeFormatter) DayHeader(t, now time.Time) string {
	t = t.In(now.Location())
	days := dayDiff(t, now)
	switch {
	case days == 0:
		return "Today"
	case days == 1:
		return "Yesterday"
	case days > 1 && days < 7:
		return t.Format("Monday")
	case t.Year() == now.Year():
		return t.Format("January 2")
	default:
		return t.Format("January 2, 2006")
	}
}

// MessageTime formats the timestamp shown next to a message: the clock time
// for today, otherwise the clock time prefixed with RelativeTime's day form.
func (f TimeFormatter) MessageTime(t, now time.Time) string {
	t = t.In(now.Location())
	days := dayDiff(t, now)
	switch {
	case days == 0:
		return f.Clock(t)
	case days > 0 && days < 7:
		return t.Format("Mon") + " " + f.Clock(t)
	case t.Year() == now.Year():
		return t.Format("Jan 2") + " " + f.Clock(t)
	default:
		return t.Format("Jan 2, 2006") + " " + f.Clock(t)
	}
}

// dayDiff returns the number of calendar days from t to now (positive when
// t is earlier), in now's location.
func dayDiff(t, now time.Time) int {
	ty, tm, td := t.Date()
	ny, nm, nd := now.Date()
	a := time.Date(ty, tm, td, 12, 0, 0, 0, time.UTC)
	b := time.Date(ny, nm, nd, 12, 0, 0, 0, time.UTC)
	return int(b.Sub(a).Round(time.Hour) / (24 * time.Hour))
}

// SameDay reports whether a and b fall on the same calendar day in loc.
func SameDay(a, b time.Time, loc *time.Location) bool {
	return dayDiff(a.In(loc), b.In(loc)) == 0
}

// RelativeTime formats t relative to now with a 24-hour clock; see
// TimeFormatter.RelativeTime.
func RelativeTime(t, now time.Time) string { return TimeFormatter{}.RelativeTime(t, now) }

// DayHeader formats a day separator; see TimeFormatter.DayHeader.
func DayHeader(t, now time.Time) string { return TimeFormatter{}.DayHeader(t, now) }

// Duration units used by FormatDuration and ParseDuration.
const (
	Day  = 24 * time.Hour
	Week = 7 * Day
)

var durationUnits = []struct {
	d     time.Duration
	short string
	long  string
}{
	{Week, "w", "week"},
	{Day, "d", "day"},
	{time.Hour, "h", "hour"},
	{time.Minute, "m", "minute"},
	{time.Second, "s", "second"},
}

// FormatDuration formats a disappearing-messages timer compactly: "off" for
// zero (or negative), otherwise the exact value in weeks, days, hours,
// minutes and seconds, largest first ("1w", "8h", "30s", "1h30m").
// Sub-second remainders are dropped.
func FormatDuration(d time.Duration) string {
	if d < time.Second {
		return "off"
	}
	var b strings.Builder
	for _, u := range durationUnits {
		if n := d / u.d; n > 0 {
			b.WriteString(strconv.FormatInt(int64(n), 10))
			b.WriteString(u.short)
			d -= n * u.d
		}
	}
	return b.String()
}

// FormatDurationLong formats a timer in words: "off", "1 week", "8 hours",
// "1 hour, 30 minutes".
func FormatDurationLong(d time.Duration) string {
	if d < time.Second {
		return "off"
	}
	var parts []string
	for _, u := range durationUnits {
		if n := d / u.d; n > 0 {
			p := strconv.FormatInt(int64(n), 10) + " " + u.long
			if n != 1 {
				p += "s"
			}
			parts = append(parts, p)
			d -= n * u.d
		}
	}
	return strings.Join(parts, ", ")
}

// FormatRemaining formats the time left on a disappearing message coarsely,
// rounding up to the largest unit: "2w", "3d", "5h", "12m", "40s"; "0s" when
// expired.
func FormatRemaining(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	for _, u := range durationUnits {
		if d >= u.d {
			n := (d + u.d - 1) / u.d
			return strconv.FormatInt(int64(n), 10) + u.short
		}
	}
	return "1s"
}

// ErrInvalidDuration is returned by ParseDuration for malformed input.
var ErrInvalidDuration = errors.New("invalid duration")

// ParseDuration parses a timer such as "1w", "8h", "30m", "45s", "2d",
// "1h30m", "1 week", "8 hours" or "off" (also "0", "none", "never" and "").
// Units: s/sec/second(s), m/min/minute(s), h/hr/hour(s), d/day(s),
// w/wk/week(s). Input is case-insensitive and may contain spaces and commas.
func ParseDuration(s string) (time.Duration, error) {
	in := strings.ToLower(strings.TrimSpace(s))
	switch in {
	case "", "off", "0", "none", "never", "disabled":
		return 0, nil
	}
	var total time.Duration
	rest := in
	parsed := false
	for {
		rest = strings.TrimLeftFunc(rest, func(r rune) bool { return unicode.IsSpace(r) || r == ',' })
		if rest == "" {
			break
		}
		if strings.HasPrefix(rest, "and ") {
			rest = rest[4:]
			continue
		}
		i := 0
		for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
			i++
		}
		if i == 0 {
			return 0, fmt.Errorf("%w: %q", ErrInvalidDuration, s)
		}
		n, err := strconv.ParseInt(rest[:i], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%w: %q", ErrInvalidDuration, s)
		}
		rest = strings.TrimLeft(rest[i:], " ")
		j := 0
		for j < len(rest) && rest[j] >= 'a' && rest[j] <= 'z' {
			j++
		}
		unit, ok := parseUnit(rest[:j])
		if !ok {
			return 0, fmt.Errorf("%w: unknown unit in %q", ErrInvalidDuration, s)
		}
		if n > int64((1<<63-1)/unit) || total > (1<<63-1)-time.Duration(n)*unit {
			return 0, fmt.Errorf("%w: %q overflows", ErrInvalidDuration, s)
		}
		total += time.Duration(n) * unit
		rest = rest[j:]
		parsed = true
	}
	if !parsed {
		return 0, fmt.Errorf("%w: %q", ErrInvalidDuration, s)
	}
	return total, nil
}

func parseUnit(u string) (time.Duration, bool) {
	switch u {
	case "s", "sec", "secs", "second", "seconds":
		return time.Second, true
	case "m", "min", "mins", "minute", "minutes":
		return time.Minute, true
	case "h", "hr", "hrs", "hour", "hours":
		return time.Hour, true
	case "d", "day", "days":
		return Day, true
	case "w", "wk", "wks", "week", "weeks":
		return Week, true
	}
	return 0, false
}

// TimerPresets are the disappearing-message timers Signal offers by default.
var TimerPresets = []time.Duration{
	0, 30 * time.Second, 5 * time.Minute, time.Hour, 8 * time.Hour, Day, Week, 4 * Week,
}
