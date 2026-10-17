package format

import (
	"errors"
	"testing"
	"time"
)

var berlin = func() *time.Location {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		return time.FixedZone("CET", 3600)
	}
	return loc
}()

// now is Wednesday, 2026-10-14 15:30 in Berlin.
var now = time.Date(2026, 10, 14, 15, 30, 0, 0, berlin)

func TestRelativeTime(t *testing.T) {
	tests := []struct {
		t    time.Time
		want string
	}{
		{now, "now"},
		{now.Add(-30 * time.Second), "now"},
		{now.Add(30 * time.Second), "now"},
		{now.Add(-time.Minute), "1m"},
		{now.Add(-5 * time.Minute), "5m"},
		{now.Add(-59 * time.Minute), "59m"},
		{now.Add(-time.Hour), "14:30"},
		{time.Date(2026, 10, 14, 0, 5, 0, 0, berlin), "00:05"},
		{now.Add(3 * time.Hour), "18:30"},
		{time.Date(2026, 10, 13, 23, 59, 0, 0, berlin), "Tue"},
		{time.Date(2026, 10, 12, 9, 0, 0, 0, berlin), "Mon"},
		{time.Date(2026, 10, 8, 9, 0, 0, 0, berlin), "Thu"},
		{time.Date(2026, 10, 7, 9, 0, 0, 0, berlin), "Oct 7"},
		{time.Date(2026, 3, 3, 9, 0, 0, 0, berlin), "Mar 3"},
		{time.Date(2025, 3, 3, 9, 0, 0, 0, berlin), "Mar 3, 2025"},
		{time.Date(2027, 1, 1, 9, 0, 0, 0, berlin), "Jan 1, 2027"},
		// UTC input is converted to now's location: 22:30 UTC = 00:30 next day in Berlin (CEST).
		{time.Date(2026, 10, 13, 22, 30, 0, 0, time.UTC), "00:30"},
	}
	for _, tt := range tests {
		if got := RelativeTime(tt.t, now); got != tt.want {
			t.Errorf("RelativeTime(%v) = %q, want %q", tt.t, got, tt.want)
		}
	}
	if got := (TimeFormatter{Hour12: true}).RelativeTime(now.Add(-2*time.Hour), now); got != "1:30 PM" {
		t.Errorf("12h clock = %q", got)
	}
}

func TestDayHeader(t *testing.T) {
	tests := []struct {
		t    time.Time
		want string
	}{
		{now, "Today"},
		{time.Date(2026, 10, 14, 0, 0, 0, 0, berlin), "Today"},
		{time.Date(2026, 10, 13, 23, 59, 0, 0, berlin), "Yesterday"},
		{time.Date(2026, 10, 12, 8, 0, 0, 0, berlin), "Monday"},
		{time.Date(2026, 10, 8, 8, 0, 0, 0, berlin), "Thursday"},
		{time.Date(2026, 10, 7, 8, 0, 0, 0, berlin), "October 7"},
		{time.Date(2026, 3, 3, 8, 0, 0, 0, berlin), "March 3"},
		{time.Date(2025, 3, 3, 8, 0, 0, 0, berlin), "March 3, 2025"},
	}
	for _, tt := range tests {
		if got := DayHeader(tt.t, now); got != tt.want {
			t.Errorf("DayHeader(%v) = %q, want %q", tt.t, got, tt.want)
		}
	}
}

func TestDayHeaderAcrossDST(t *testing.T) {
	// 2026-03-29 is the spring-forward day in Berlin; the day before is still "Yesterday".
	n := time.Date(2026, 3, 30, 0, 30, 0, 0, berlin)
	if got := DayHeader(time.Date(2026, 3, 29, 1, 0, 0, 0, berlin), n); got != "Yesterday" {
		t.Errorf("got %q", got)
	}
	if !SameDay(n, n.Add(time.Hour), berlin) || SameDay(n, n.Add(-time.Hour), berlin) {
		t.Error("SameDay")
	}
}

func TestMessageTime(t *testing.T) {
	f := TimeFormatter{}
	tests := []struct {
		t    time.Time
		want string
	}{
		{now.Add(-time.Hour), "14:30"},
		{time.Date(2026, 10, 12, 9, 5, 0, 0, berlin), "Mon 09:05"},
		{time.Date(2026, 3, 3, 9, 5, 0, 0, berlin), "Mar 3 09:05"},
		{time.Date(2024, 3, 3, 9, 5, 0, 0, berlin), "Mar 3, 2024 09:05"},
	}
	for _, tt := range tests {
		if got := f.MessageTime(tt.t, now); got != tt.want {
			t.Errorf("MessageTime(%v) = %q, want %q", tt.t, got, tt.want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d          time.Duration
		short, lng string
	}{
		{0, "off", "off"},
		{-time.Hour, "off", "off"},
		{500 * time.Millisecond, "off", "off"},
		{30 * time.Second, "30s", "30 seconds"},
		{time.Minute, "1m", "1 minute"},
		{5 * time.Minute, "5m", "5 minutes"},
		{time.Hour, "1h", "1 hour"},
		{8 * time.Hour, "8h", "8 hours"},
		{Day, "1d", "1 day"},
		{Week, "1w", "1 week"},
		{4 * Week, "4w", "4 weeks"},
		{90 * time.Minute, "1h30m", "1 hour, 30 minutes"},
		{Week + Day + time.Second, "1w1d1s", "1 week, 1 day, 1 second"},
	}
	for _, tt := range tests {
		if got := FormatDuration(tt.d); got != tt.short {
			t.Errorf("FormatDuration(%v) = %q, want %q", tt.d, got, tt.short)
		}
		if got := FormatDurationLong(tt.d); got != tt.lng {
			t.Errorf("FormatDurationLong(%v) = %q, want %q", tt.d, got, tt.lng)
		}
	}
}

func TestFormatRemaining(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{-time.Second, "0s"},
		{0, "0s"},
		{time.Millisecond, "1s"},
		{40 * time.Second, "40s"},
		{61 * time.Second, "2m"},
		{5 * time.Hour, "5h"},
		{2*Day + time.Hour, "3d"},
		{Week, "1w"},
		{Week + time.Second, "2w"},
	}
	for _, tt := range tests {
		if got := FormatRemaining(tt.d); got != tt.want {
			t.Errorf("FormatRemaining(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestParseDuration(t *testing.T) {
	tests := []struct {
		in   string
		want time.Duration
		err  bool
	}{
		{"off", 0, false},
		{"OFF", 0, false},
		{"", 0, false},
		{"0", 0, false},
		{"never", 0, false},
		{"30s", 30 * time.Second, false},
		{"30m", 30 * time.Minute, false},
		{"8h", 8 * time.Hour, false},
		{"1d", Day, false},
		{"1w", Week, false},
		{"4W", 4 * Week, false},
		{"1h30m", 90 * time.Minute, false},
		{"1h 30m", 90 * time.Minute, false},
		{"1 week", Week, false},
		{"8 hours", 8 * time.Hour, false},
		{"1 hour, 30 minutes", 90 * time.Minute, false},
		{"2 days and 3 hours", 2*Day + 3*time.Hour, false},
		{"5 min", 5 * time.Minute, false},
		{"5", 0, true},
		{"h", 0, true},
		{"1y", 0, true},
		{"-1h", 0, true},
		{"1.5h", 0, true},
		{"99999999999999999999w", 0, true},
		{"9999999w", 0, true},
	}
	for _, tt := range tests {
		got, err := ParseDuration(tt.in)
		if (err != nil) != tt.err || got != tt.want {
			t.Errorf("ParseDuration(%q) = %v, %v; want %v, err=%v", tt.in, got, err, tt.want, tt.err)
		}
		if err != nil && !errors.Is(err, ErrInvalidDuration) {
			t.Errorf("error %v does not wrap ErrInvalidDuration", err)
		}
	}
	// Format/parse round trip.
	for _, d := range append(TimerPresets, 90*time.Minute, Week+Day+time.Second) {
		got, err := ParseDuration(FormatDuration(d))
		if err != nil || got != d {
			t.Errorf("round trip %v: %v %v", d, got, err)
		}
		got, err = ParseDuration(FormatDurationLong(d))
		if err != nil || got != d {
			t.Errorf("long round trip %v: %v %v", d, got, err)
		}
	}
}
