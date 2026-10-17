package notify

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func TestSanitize(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"plain", "hello", 0, "hello"},
		{"esc stripped", "a\x1b]2;pwned\x07b", 0, "a]2;pwnedb"},
		{"bel stripped", "ding\a", 0, "ding"},
		{"st stripped", "x\x1b\\y", 0, "x\\y"},
		{"nul and del", "a\x00b\x7fc", 0, "abc"},
		{"c1 runes", "a\u009b31mb\u009dc\u009cd\u0090e", 0, "a31mbcde"},
		{"raw 8-bit csi byte", "a\x9b31mb", 0, "a31mb"},
		{"invalid utf8", "a\xff\xfeb", 0, "ab"},
		{"newlines to space", "line1\nline2\r\nline3", 0, "line1 line2 line3"},
		{"tabs and runs", "a\t\t b   c", 0, "a b c"},
		{"trim", "  \n hi \n ", 0, "hi"},
		{"nel u0085 is space", "a\u0085b", 0, "a b"},
		{"line separator", "a b", 0, "a b"},
		{"bidi override", "evil‮gnp.exe", 0, "evilgnp.exe"},
		{"bidi isolates", "⁦x⁩", 0, "x"},
		{"unicode kept", "Grüße 👋 日本", 0, "Grüße 👋 日本"},
		{"truncate", "abcdefgh", 5, "abcd…"},
		{"truncate exact", "abcde", 5, "abcde"},
		{"truncate one", "abc", 1, "…"},
		{"truncate trims space", "abc defg", 5, "abc…"},
		{"truncate runes", "äöüßéè", 3, "äö…"},
		{"empty", "", 10, ""},
		{"only controls", "\x1b\x07\n", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Sanitize(tt.in, tt.max)
			if got != tt.want {
				t.Fatalf("Sanitize(%q, %d) = %q, want %q", tt.in, tt.max, got, tt.want)
			}
			for _, r := range got {
				if isControl(r) {
					t.Fatalf("control rune %U survived in %q", r, got)
				}
			}
		})
	}
}

func FuzzSanitize(f *testing.F) {
	for _, s := range []string{"\x1b]0;x\a", "\x9b", "a\u009cb", "\n\r\t"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got := Sanitize(s, 50)
		for i := 0; i < len(got); i++ {
			if got[i] < 0x20 || got[i] == 0x7f {
				t.Fatalf("control byte %#x in %q", got[i], got)
			}
		}
		for _, r := range got {
			if r >= 0x80 && r <= 0x9f {
				t.Fatalf("C1 rune in %q", got)
			}
		}
	})
}

func TestOSC777Sequence(t *testing.T) {
	tests := []struct {
		name string
		n    Notification
		want string
	}{
		{"basic", Notification{Title: "Alice", Body: "hi there"}, "\x1b]777;notify;Alice;hi there\x1b\\"},
		{"title semicolon", Notification{Title: "A;B", Body: "x;y"}, "\x1b]777;notify;A,B;x;y\x1b\\"},
		{"injection", Notification{Title: "Al\x1bice", Body: "evil\x1b\\\x1b]2;owned\a"}, "\x1b]777;notify;Alice;evil\\]2;owned\x1b\\"},
		{"title only", Notification{Title: "New message"}, "\x1b]777;notify;New message;\x1b\\"},
		{"empty", Notification{}, ""},
		{"multiline", Notification{Title: "A", Body: "one\ntwo"}, "\x1b]777;notify;A;one two\x1b\\"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := OSC777Sequence(tt.n); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOSC9Sequence(t *testing.T) {
	tests := []struct {
		name string
		n    Notification
		want string
	}{
		{"title and body", Notification{Title: "Alice", Body: "hi"}, "\x1b]9;Alice: hi\a"},
		{"title only", Notification{Title: "New message"}, "\x1b]9;New message\a"},
		{"body only", Notification{Body: "hi"}, "\x1b]9;hi\a"},
		{"empty", Notification{}, ""},
		{"bel injection", Notification{Title: "A", Body: "x\ay\x1b]9;z"}, "\x1b]9;A: xy]9;z\a"},
		{"progress lookalike", Notification{Body: "4;1;50"}, "\x1b]9; 4;1;50\a"},
		{"digits only", Notification{Title: "42"}, "\x1b]9; 42\a"},
		{"digits then text", Notification{Title: "42 apples"}, "\x1b]9;42 apples\a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := OSC9Sequence(tt.n); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOSC99Sequence(t *testing.T) {
	tests := []struct {
		name string
		n    Notification
		want string
	}{
		{
			"title and body",
			Notification{Title: "Alice", Body: "hi", ChatID: "chat1"},
			"\x1b]99;i=warte-chat1:d=0:a=focus:u=1:e=1:p=title;" + b64("Alice") + "\x1b\\" +
				"\x1b]99;i=warte-chat1:d=1:e=1:p=body;" + b64("hi") + "\x1b\\",
		},
		{
			"title only, silent, critical",
			Notification{Title: "Call", ChatID: "c", Silent: true, Urgency: UrgencyCritical},
			"\x1b]99;i=warte-c:a=focus:u=2:s=silent:e=1:p=title;" + b64("Call") + "\x1b\\",
		},
		{
			"low urgency",
			Notification{Title: "x", ChatID: "c", Urgency: UrgencyLow},
			"\x1b]99;i=warte-c:a=focus:u=0:e=1:p=title;" + b64("x") + "\x1b\\",
		},
		{
			"body promoted to title",
			Notification{Body: "only body", ChatID: "c"},
			"\x1b]99;i=warte-c:a=focus:u=1:e=1:p=title;" + b64("only body") + "\x1b\\",
		},
		{
			"payload sanitized before encoding",
			Notification{Title: "A\x1b]2;x\a", Body: "line\nnext", ChatID: "c"},
			"\x1b]99;i=warte-c:d=0:a=focus:u=1:e=1:p=title;" + b64("A]2;x") + "\x1b\\" +
				"\x1b]99;i=warte-c:d=1:e=1:p=body;" + b64("line next") + "\x1b\\",
		},
		{"empty", Notification{ChatID: "c"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := OSC99Sequence(tt.n); got != tt.want {
				t.Fatalf("got  %q\nwant %q", got, tt.want)
			}
		})
	}
}

func TestOSC99ID(t *testing.T) {
	if got := OSC99ID("abc-DEF_1.2+3"); got != "warte-abc-DEF_1.2+3" {
		t.Fatalf("safe id: %q", got)
	}
	unsafe := OSC99ID("chat:1;evil\x1b")
	if strings.ContainsAny(unsafe, ":;\x1b") || !strings.HasPrefix(unsafe, "warte-") {
		t.Fatalf("unsafe id not hashed: %q", unsafe)
	}
	if unsafe != OSC99ID("chat:1;evil\x1b") {
		t.Fatal("hashed id not stable")
	}
	if long := OSC99ID(strings.Repeat("a", 65)); len(long) != len("warte-")+16 {
		t.Fatalf("long id not hashed: %q", long)
	}
	a, b := OSC99ID(""), OSC99ID("")
	if a == b {
		t.Fatalf("empty chat ids should be unique, got %q twice", a)
	}
}

func TestTmuxWrap(t *testing.T) {
	tests := []struct{ in, want string }{
		{"\x1b]9;hi\a", "\x1bPtmux;\x1b\x1b]9;hi\a\x1b\\"},
		{"\x1b]777;notify;a;b\x1b\\", "\x1bPtmux;\x1b\x1b]777;notify;a;b\x1b\x1b\\\x1b\\"},
		{"plain", "\x1bPtmux;plain\x1b\\"},
	}
	for _, tt := range tests {
		if got := TmuxWrap(tt.in); got != tt.want {
			t.Errorf("TmuxWrap(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestOSCNotifier(t *testing.T) {
	n := Notification{Title: "Alice", Body: "hi", ChatID: "c"}
	tests := []struct {
		name  string
		proto Method
		tmux  bool
		want  string
	}{
		{"osc9", MethodOSC9, false, OSC9Sequence(n)},
		{"osc777", MethodOSC777, false, OSC777Sequence(n)},
		{"osc99", MethodOSC99, false, OSC99Sequence(n)},
		{"osc9 tmux", MethodOSC9, true, "\x1bPtmux;\x1b\x1b]9;Alice: hi\a\x1b\\"},
		{"osc777 tmux", MethodOSC777, true, TmuxWrap(OSC777Sequence(n))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			o := &OSC{Protocol: tt.proto, Tmux: tt.tmux, W: &buf}
			seq, ok := o.Sequence(n)
			if !ok || seq != tt.want {
				t.Fatalf("Sequence = %q, %v; want %q", seq, ok, tt.want)
			}
			if err := o.Notify(n); err != nil {
				t.Fatal(err)
			}
			if buf.String() != tt.want {
				t.Fatalf("wrote %q, want %q", buf.String(), tt.want)
			}
		})
	}

	t.Run("empty notification writes nothing", func(t *testing.T) {
		var buf bytes.Buffer
		o := &OSC{Protocol: MethodOSC9, W: &buf}
		if _, ok := o.Sequence(Notification{}); ok {
			t.Fatal("expected no sequence")
		}
		if err := o.Notify(Notification{}); err != nil || buf.Len() != 0 {
			t.Fatalf("err=%v wrote %q", err, buf.String())
		}
	})
	t.Run("no writer", func(t *testing.T) {
		o := &OSC{Protocol: MethodOSC9}
		if err := o.Notify(n); !errors.Is(err, ErrNoWriter) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("non-osc protocol", func(t *testing.T) {
		o := &OSC{Protocol: MethodBell}
		if _, ok := o.Sequence(n); ok {
			t.Fatal("expected no sequence")
		}
	})
}

func TestBell(t *testing.T) {
	var buf bytes.Buffer
	b := &Bell{W: &buf}
	if err := b.Notify(Notification{Title: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Notify(Notification{Title: "x", Silent: true}); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "\a" {
		t.Fatalf("wrote %q", buf.String())
	}
	if seq, ok := b.Sequence(Notification{}); !ok || seq != "\a" {
		t.Fatalf("Sequence = %q, %v", seq, ok)
	}
	if err := (&Bell{}).Notify(Notification{}); !errors.Is(err, ErrNoWriter) {
		t.Fatalf("err = %v", err)
	}
}

func TestTitle(t *testing.T) {
	tests := []struct {
		unread   int
		app      string
		want     string
		wantOSC2 string
		wantOSC0 string
	}{
		{3, "warte", "(3) warte", "\x1b]2;(3) warte\a", "\x1b]0;(3) warte\a"},
		{0, "warte", "warte", "\x1b]2;warte\a", "\x1b]0;warte\a"},
		{-1, "warte", "warte", "\x1b]2;warte\a", "\x1b]0;warte\a"},
		{1000, "warte", "(999+) warte", "\x1b]2;(999+) warte\a", "\x1b]0;(999+) warte\a"},
		{2, "", "(2)", "\x1b]2;(2)\a", "\x1b]0;(2)\a"},
		{1, "wa\x1brte\a", "(1) warte", "\x1b]2;(1) warte\a", "\x1b]0;(1) warte\a"},
	}
	for _, tt := range tests {
		if got := Title(tt.unread, tt.app); got != tt.want {
			t.Errorf("Title(%d,%q) = %q, want %q", tt.unread, tt.app, got, tt.want)
		}
		if got := TitleSequence(tt.unread, tt.app); got != tt.wantOSC2 {
			t.Errorf("TitleSequence = %q, want %q", got, tt.wantOSC2)
		}
		if got := IconTitleSequence(tt.unread, tt.app); got != tt.wantOSC0 {
			t.Errorf("IconTitleSequence = %q, want %q", got, tt.wantOSC0)
		}
	}
}
