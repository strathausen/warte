package notify

import (
	"encoding/base64"
	"encoding/hex"
	"hash/fnv"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Escape-sequence building blocks.
const (
	esc = "\x1b"
	bel = "\a"
	st  = "\x1b\\"
)

// OSC delivers notifications as terminal escape sequences.
type OSC struct {
	// Protocol is one of MethodOSC9, MethodOSC99 or MethodOSC777.
	Protocol Method
	// Tmux wraps every sequence in tmux passthrough.
	Tmux bool
	// W receives the sequence on Notify. It may be nil if only Sequence is
	// used.
	W io.Writer

	mu sync.Mutex
}

// Sequence returns the escape sequence for n. It returns false if n has no
// visible text or the protocol is not an OSC protocol.
func (o *OSC) Sequence(n Notification) (string, bool) {
	var seq string
	switch o.Protocol {
	case MethodOSC9:
		seq = OSC9Sequence(n)
	case MethodOSC99:
		seq = OSC99Sequence(n)
	case MethodOSC777:
		seq = OSC777Sequence(n)
	}
	if seq == "" {
		return "", false
	}
	if o.Tmux {
		seq = TmuxWrap(seq)
	}
	return seq, true
}

// Notify writes the sequence for n to W.
func (o *OSC) Notify(n Notification) error {
	seq, ok := o.Sequence(n)
	if !ok {
		return nil
	}
	if o.W == nil {
		return ErrNoWriter
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	_, err := io.WriteString(o.W, seq)
	return err
}

// OSC777Sequence returns "ESC ] 777 ; notify ; title ; body ST" for n, or
// "" if n has neither title nor body. Semicolons in the title are replaced
// because they would end the title field.
func OSC777Sequence(n Notification) string {
	title := sanitizeField(n.Title, MaxTitleRunes, ";")
	body := Sanitize(n.Body, MaxBodyRunes)
	if title == "" && body == "" {
		return ""
	}
	return esc + "]777;notify;" + title + ";" + body + st
}

// OSC9Sequence returns "ESC ] 9 ; text BEL" for n, or "" if n has neither
// title nor body. OSC 9 carries a single string, so the title and body are
// joined as "title: body".
//
// Payloads that look like ConEmu/Windows Terminal OSC 9 sub-commands
// ("4;…" progress and friends) are prefixed with a space so they are always
// shown as a notification.
func OSC9Sequence(n Notification) string {
	text := oneLine(n, MaxTitleRunes+MaxBodyRunes)
	if text == "" {
		return ""
	}
	if looksLikeOSC9Subcommand(text) {
		text = " " + text
	}
	return esc + "]9;" + text + bel
}

// oneLine joins a sanitized title and body into one string.
func oneLine(n Notification, maxRunes int) string {
	title := Sanitize(n.Title, MaxTitleRunes)
	body := Sanitize(n.Body, MaxBodyRunes)
	var text string
	switch {
	case title == "":
		text = body
	case body == "":
		text = title
	default:
		text = title + ": " + body
	}
	return truncate(text, maxRunes)
}

func looksLikeOSC9Subcommand(s string) bool {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return i > 0 && (i == len(s) || s[i] == ';')
}

var osc99Counter atomic.Uint64

// OSC99ID returns the kitty notification identifier used for chatID.
// Identifiers only contain [A-Za-z0-9-_+.], as kitty requires; chat IDs
// containing other characters are hashed. An empty chatID yields a fresh
// unique identifier so unrelated notifications never replace each other.
func OSC99ID(chatID string) string {
	if chatID == "" {
		return "warte-n" + strconv.FormatUint(osc99Counter.Add(1), 10)
	}
	safe := len(chatID) <= 64
	for i := 0; safe && i < len(chatID); i++ {
		c := chatID[i]
		safe = c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '-' || c == '_' || c == '+' || c == '.'
	}
	if safe {
		return "warte-" + chatID
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(chatID))
	return "warte-" + hex.EncodeToString(h.Sum(nil))
}

// OSC99Sequence returns kitty's OSC 99 notification for n: a title chunk
// ("d=0", "p=title") followed by a body chunk ("d=1", "p=body"), both
// base64 encoded ("e=1"). The first chunk carries the identifier derived
// from ChatID (so a newer notification for the same chat replaces the old
// one), the focus action, urgency and, for silent notifications,
// "s=silent". It returns "" if n has neither title nor body.
func OSC99Sequence(n Notification) string {
	title := Sanitize(n.Title, MaxTitleRunes)
	body := Sanitize(n.Body, MaxBodyRunes)
	if title == "" {
		title, body = body, ""
	}
	if title == "" {
		return ""
	}
	id := OSC99ID(n.ChatID)

	meta := []string{"i=" + id}
	if body != "" {
		meta = append(meta, "d=0")
	}
	meta = append(meta, "a=focus", "u="+osc99Urgency(n.Urgency))
	if n.Silent {
		meta = append(meta, "s=silent")
	}
	meta = append(meta, "e=1", "p=title")

	var b strings.Builder
	b.WriteString(esc + "]99;" + strings.Join(meta, ":") + ";")
	b.WriteString(base64.StdEncoding.EncodeToString([]byte(title)))
	b.WriteString(st)
	if body != "" {
		b.WriteString(esc + "]99;i=" + id + ":d=1:e=1:p=body;")
		b.WriteString(base64.StdEncoding.EncodeToString([]byte(body)))
		b.WriteString(st)
	}
	return b.String()
}

func osc99Urgency(u Urgency) string {
	switch u {
	case UrgencyLow:
		return "0"
	case UrgencyCritical:
		return "2"
	default:
		return "1"
	}
}

// TmuxWrap wraps seq in tmux's DCS passthrough
// ("ESC P tmux ; <seq with every ESC doubled> ESC \") so that tmux forwards
// it to the outer terminal. tmux must have "allow-passthrough on".
func TmuxWrap(seq string) string {
	return esc + "Ptmux;" + strings.ReplaceAll(seq, esc, esc+esc) + st
}

// BellSequence is the terminal bell.
const BellSequence = bel

// Bell rings the terminal bell for each non-silent notification.
type Bell struct {
	// W receives the bell on Notify.
	W  io.Writer
	mu sync.Mutex
}

// Sequence returns BEL, or false for silent notifications.
func (b *Bell) Sequence(n Notification) (string, bool) {
	if n.Silent {
		return "", false
	}
	return BellSequence, true
}

// Notify writes BEL to W unless n is silent.
func (b *Bell) Notify(n Notification) error {
	seq, ok := b.Sequence(n)
	if !ok {
		return nil
	}
	if b.W == nil {
		return ErrNoWriter
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	_, err := io.WriteString(b.W, seq)
	return err
}
