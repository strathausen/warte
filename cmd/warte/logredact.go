package main

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
)

// redactedLogFields are zerolog fields that signalmeow fills with phone
// numbers, raw server responses (which carry numbers, profile data and
// contact lists), decrypted content or signed upload credentials. They are
// dropped from the log unless log_sensitive is set.
var redactedLogFields = []string{
	"sender_e164", "raw_e164", "e164", "number", "phone",
	"body", "text", "response_body", "response_data", "invalid_response_data",
	"item_data", "raw_data", "req", "resp", "request_headers", "response_headers",
	"location",
}

// phoneRe matches E.164 phone numbers anywhere in a log line (error
// strings, request paths).
var phoneRe = regexp.MustCompile(`\+[1-9][0-9]{6,14}`)

var redactedPhone = []byte("+[redacted]")

// redactWriter removes sensitive data from log lines before they reach
// the log file. zerolog writes one JSON object per Write; other lines
// (charm/log text) only get phone numbers masked.
type redactWriter struct{ w io.Writer }

func (r redactWriter) Write(p []byte) (int, error) {
	out := p
	trimmed := bytes.TrimSpace(p)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		var m map[string]json.RawMessage
		if json.Unmarshal(trimmed, &m) == nil {
			changed := false
			for _, k := range redactedLogFields {
				if _, ok := m[k]; ok {
					delete(m, k)
					changed = true
				}
			}
			if changed {
				if b, err := json.Marshal(m); err == nil {
					out = append(b, '\n')
				}
			}
		}
	}
	out = phoneRe.ReplaceAll(out, redactedPhone)
	if _, err := r.w.Write(out); err != nil {
		return 0, err
	}
	return len(p), nil
}

// logWriter wraps the log file with redaction unless sensitive logging was
// asked for.
func logWriter(w io.Writer, sensitive bool) io.Writer {
	if sensitive {
		return w
	}
	return redactWriter{w: w}
}
