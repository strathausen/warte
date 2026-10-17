package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

// TestLogRedaction: signalmeow puts phone numbers and raw server responses
// in its log context; without log_sensitive none of it reaches the file.
func TestLogRedaction(t *testing.T) {
	var buf bytes.Buffer
	l := zerolog.New(logWriter(&buf, false)).With().Str("sender_e164", "+4915112345678").Logger()
	l.Debug().Str("response_body", `{"number":"+4915112345678"}`).Str("request_path", "/v1/x/+4915112345678").
		Str("chat", "c1").Msg("call +12025550123 failed")
	l.Warn().Any("request_headers", map[string]string{"Authorization": "Basic c2VjcmV0"}).Str("location", "https://cdn/upload?sig=x").Msg("upload")
	out := buf.String()
	for _, bad := range []string{"4915112345678", "2025550123", "response_body", "sender_e164", "Authorization", "sig=x"} {
		if strings.Contains(out, bad) {
			t.Errorf("log contains %q:\n%s", bad, out)
		}
	}
	for _, good := range []string{`"chat":"c1"`, "+[redacted]", `"level":"warn"`} {
		if !strings.Contains(out, good) {
			t.Errorf("log lacks %q:\n%s", good, out)
		}
	}
	if n := strings.Count(out, "\n"); n != 2 {
		t.Errorf("%d lines:\n%s", n, out)
	}

	buf.Reset()
	l = zerolog.New(logWriter(&buf, true))
	l.Info().Str("sender_e164", "+4915112345678").Msg("x")
	if !strings.Contains(buf.String(), "+4915112345678") {
		t.Errorf("log_sensitive redacted anyway: %s", buf.String())
	}
}
