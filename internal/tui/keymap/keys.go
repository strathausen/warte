package keymap

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Canonical key strings
//
// A key string names a single key press. The format mirrors Bubble Tea v2's
// KeyPressMsg.String():
//
//   - A printable key is the character itself: "j", "G", "?", "/", "+", "]".
//     Shifted letters are written as the uppercase letter ("G", never
//     "shift+g").
//   - The space bar is "space".
//   - Named keys are lowercase words: "enter", "tab", "backspace", "esc",
//     "up", "down", "left", "right", "home", "end", "pgup", "pgdown",
//     "insert", "delete", "f1" ... "f20".
//   - Modifiers are prefixes joined with "+", always in the order ctrl, alt,
//     shift, meta, hyper, super: "ctrl+d", "alt+x", "shift+tab",
//     "alt+enter", "ctrl+shift+a". A letter combined with any modifier is
//     written lowercase, with "shift+" when it was uppercase ("alt+shift+x").
//
// A key sequence is a []string of key strings, for example
// []string{"ctrl+w", "h"} or []string{"g", "g"}.

// LeaderToken is the placeholder for the configurable leader key inside a
// binding's key sequence. It is resolved to the engine's leader key when the
// lookup tables are built.
const LeaderToken = "<leader>"

// modifier order used by Bubble Tea's Keystroke().
var modOrder = []string{"ctrl", "alt", "shift", "meta", "hyper", "super"}

// modAliases maps accepted modifier spellings to canonical modifier names.
var modAliases = map[string]string{
	"ctrl":    "ctrl",
	"control": "ctrl",
	"alt":     "alt",
	"opt":     "alt",
	"option":  "alt",
	"shift":   "shift",
	"meta":    "meta",
	"hyper":   "hyper",
	"super":   "super",
	"cmd":     "super",
}

// namedKeys maps accepted spellings of named keys to canonical names.
var namedKeys = map[string]string{
	"enter":     "enter",
	"return":    "enter",
	"cr":        "enter",
	"tab":       "tab",
	"backspace": "backspace",
	"bs":        "backspace",
	"esc":       "esc",
	"escape":    "esc",
	"space":     "space",
	"spc":       "space",
	"up":        "up",
	"down":      "down",
	"left":      "left",
	"right":     "right",
	"home":      "home",
	"end":       "end",
	"pgup":      "pgup",
	"pageup":    "pgup",
	"pgdown":    "pgdown",
	"pagedown":  "pgdown",
	"insert":    "insert",
	"ins":       "insert",
	"delete":    "delete",
	"del":       "delete",
}

func init() {
	for i := 1; i <= 20; i++ {
		f := fmt.Sprintf("f%d", i)
		namedKeys[f] = f
	}
}

// Canonical normalizes a single key string to the canonical format described
// in the package documentation. It accepts Bubble Tea style strings with
// loose spelling ("Ctrl+D", "shift+g", "escape", " ") and returns the
// canonical form ("ctrl+d", "G", "esc", "space"). Strings it does not
// recognize (for example multi-rune text from an input method) are returned
// unchanged.
func Canonical(key string) string {
	switch {
	case key == "":
		return ""
	case key == " ":
		return "space"
	case utf8.RuneCountInString(key) == 1:
		return key
	}

	mods := map[string]bool{}
	rest := key
	for {
		i := strings.IndexByte(rest, '+')
		if i <= 0 || i == len(rest)-1 {
			break
		}
		m, ok := modAliases[strings.ToLower(rest[:i])]
		if !ok {
			break
		}
		mods[m] = true
		rest = rest[i+1:]
	}
	if len(mods) == 0 {
		if n, ok := namedKeys[strings.ToLower(key)]; ok {
			return n
		}
		return key
	}
	return joinMods(mods, rest)
}

// joinMods builds a canonical key from a modifier set and a base key,
// applying the letter/shift normalization rules.
func joinMods(mods map[string]bool, base string) string {
	if base == " " {
		base = "space"
	}
	if utf8.RuneCountInString(base) == 1 {
		r, _ := utf8.DecodeRuneInString(base)
		if unicode.IsLetter(r) {
			if unicode.IsUpper(r) {
				mods["shift"] = true
				base = string(unicode.ToLower(r))
			}
			if mods["shift"] && len(mods) == 1 {
				return string(unicode.ToUpper(r))
			}
		}
	} else if n, ok := namedKeys[strings.ToLower(base)]; ok {
		base = n
	}
	var sb strings.Builder
	for _, m := range modOrder {
		if mods[m] {
			sb.WriteString(m)
			sb.WriteByte('+')
		}
	}
	sb.WriteString(base)
	return sb.String()
}

// splitMods splits a canonical key into its modifiers and base key.
func splitMods(key string) (mods []string, base string) {
	if utf8.RuneCountInString(key) <= 1 {
		return nil, key
	}
	rest := key
	for {
		i := strings.IndexByte(rest, '+')
		if i <= 0 || i == len(rest)-1 {
			break
		}
		if _, ok := modAliases[rest[:i]]; !ok {
			break
		}
		mods = append(mods, rest[:i])
		rest = rest[i+1:]
	}
	return mods, rest
}

// ParseKey parses a single key in any accepted notation: a vim-style token
// ("<C-d>", "<Space>", "<leader>"), a Bubble Tea style string ("ctrl+d",
// "space"), or a literal character (" " is the space bar). It is used for
// settings such as the leader key.
func ParseKey(s string) (string, error) {
	if s == "" {
		return "", fmt.Errorf("empty key")
	}
	if s == " " {
		return "space", nil
	}
	if utf8.RuneCountInString(s) == 1 {
		return s, nil
	}
	if strings.HasPrefix(s, "<") && strings.HasSuffix(s, ">") {
		keys, err := ParseKeys(s)
		if err != nil {
			return "", err
		}
		if len(keys) != 1 {
			return "", fmt.Errorf("%q is not a single key", s)
		}
		return keys[0], nil
	}
	c := Canonical(s)
	if mods, base := splitMods(c); len(mods) > 0 || isNamed(base) {
		return c, nil
	}
	return "", fmt.Errorf("unknown key %q", s)
}

// isNamed reports whether k is a canonical named key such as "enter".
func isNamed(k string) bool {
	n, ok := namedKeys[k]
	return ok && n == k
}

// ParseKeys parses a key sequence written in vim-like notation and returns
// canonical key strings.
//
// Supported notation:
//
//   - Plain characters are one key each: "gg" is g, g; "]u" is ], u.
//   - Angle-bracket tokens (case-insensitive): <leader>, <CR>, <Enter>,
//     <Return>, <Esc>, <Space>, <Tab>, <BS>, <Del>, <Insert>, <Up>, <Down>,
//     <Left>, <Right>, <Home>, <End>, <PageUp>, <PageDown>, <F1>..<F20>,
//     <lt> ("<"), <gt> (">"), <Bar> ("|"), <Bslash> ("\").
//   - Modifier prefixes inside angle brackets: C- (ctrl), A- or M- (alt),
//     S- (shift), D- (super), combinable: <C-d>, <A-x>, <S-Tab>, <C-S-a>,
//     <A-CR>.
//   - Whitespace separates keys and is otherwise ignored; write <Space> for
//     the space bar. A whitespace-separated field that starts with a Bubble
//     Tea modifier ("ctrl+w h") is read as one Bubble Tea style key.
//   - A "<" that does not start a valid token is a literal "<".
//
// The leader placeholder is returned as LeaderToken.
func ParseKeys(notation string) ([]string, error) {
	var out []string
	for _, field := range strings.Fields(notation) {
		if isTeaStyle(field) {
			out = append(out, Canonical(field))
			continue
		}
		keys, err := parseVimField(field)
		if err != nil {
			return nil, err
		}
		out = append(out, keys...)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("empty key sequence %q", notation)
	}
	return out, nil
}

// isTeaStyle reports whether a field looks like "ctrl+x"/"alt+enter".
func isTeaStyle(field string) bool {
	i := strings.IndexByte(field, '+')
	if i <= 0 || i == len(field)-1 {
		return false
	}
	_, ok := modAliases[strings.ToLower(field[:i])]
	return ok
}

// parseVimField parses one whitespace-free run of vim notation.
func parseVimField(s string) ([]string, error) {
	var out []string
	for len(s) > 0 {
		if s[0] == '<' {
			if end := strings.IndexByte(s[1:], '>'); end > 0 {
				inner := s[1 : end+1]
				if looksLikeToken(inner) {
					k, err := parseToken(inner)
					if err != nil {
						return nil, err
					}
					out = append(out, k)
					s = s[end+2:]
					continue
				}
			}
		}
		r, size := utf8.DecodeRuneInString(s)
		if r == utf8.RuneError && size == 1 {
			return nil, fmt.Errorf("invalid UTF-8 in key notation")
		}
		out = append(out, string(r))
		s = s[size:]
	}
	return out, nil
}

// looksLikeToken reports whether the text between angle brackets is meant
// as a named token (so an unknown name is an error, not literal text).
func looksLikeToken(inner string) bool {
	if inner == "" {
		return false
	}
	for i, r := range inner {
		if r == '-' && i > 0 {
			continue
		}
		if r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			continue
		}
		// Allow a final punctuation key after a modifier, e.g. <C-]> or <A-.>.
		if i == len(inner)-1 && i > 0 && inner[i-1] == '-' {
			continue
		}
		return false
	}
	return true
}

// vimNames maps vim token names (lowercase) to canonical key strings.
var vimNames = map[string]string{
	"lt":     "<",
	"gt":     ">",
	"bar":    "|",
	"bslash": "\\",
	"nul":    "ctrl+space",
}

// parseToken parses the inside of a <...> token.
func parseToken(inner string) (string, error) {
	lower := strings.ToLower(inner)
	if lower == "leader" {
		return LeaderToken, nil
	}
	mods := map[string]bool{}
	rest := inner
	for len(rest) > 2 && rest[1] == '-' {
		switch unicode.ToLower(rune(rest[0])) {
		case 'c':
			mods["ctrl"] = true
		case 'a', 'm':
			mods["alt"] = true
		case 's':
			mods["shift"] = true
		case 'd':
			mods["super"] = true
		default:
			return "", fmt.Errorf("unknown modifier in <%s>", inner)
		}
		rest = rest[2:]
	}
	var base string
	switch {
	case utf8.RuneCountInString(rest) == 1:
		base = rest
		// vim treats <C-D> and <C-d> the same.
		if mods["ctrl"] && !mods["shift"] {
			base = strings.ToLower(base)
		}
	default:
		l := strings.ToLower(rest)
		if n, ok := namedKeys[l]; ok {
			base = n
		} else if n, ok := vimNames[l]; ok {
			base = n
		} else {
			return "", fmt.Errorf("unknown key <%s>", inner)
		}
	}
	if len(mods) == 0 {
		return Canonical(base), nil
	}
	if strings.Contains(base, "+") && utf8.RuneCountInString(base) > 1 {
		// A vim name that already carries modifiers, e.g. <Nul>.
		m, b := splitMods(base)
		for _, x := range m {
			mods[x] = true
		}
		base = b
	}
	return joinMods(mods, base), nil
}

// vimOut maps canonical named keys to their vim notation.
var vimOut = map[string]string{
	"enter":     "CR",
	"tab":       "Tab",
	"backspace": "BS",
	"esc":       "Esc",
	"space":     "Space",
	"up":        "Up",
	"down":      "Down",
	"left":      "Left",
	"right":     "Right",
	"home":      "Home",
	"end":       "End",
	"pgup":      "PageUp",
	"pgdown":    "PageDown",
	"insert":    "Insert",
	"delete":    "Del",
}

// FormatNotation renders a key sequence in the vim-like notation accepted by
// ParseKeys, so that ParseKeys(FormatNotation(k)) returns k.
func FormatNotation(keys []string) string {
	var sb strings.Builder
	for _, k := range keys {
		sb.WriteString(notationKey(k))
	}
	return sb.String()
}

func notationKey(k string) string {
	if k == LeaderToken {
		return "<leader>"
	}
	mods, base := splitMods(k)
	var prefix strings.Builder
	for _, m := range mods {
		switch m {
		case "ctrl":
			prefix.WriteString("C-")
		case "alt":
			prefix.WriteString("A-")
		case "shift":
			prefix.WriteString("S-")
		case "super":
			prefix.WriteString("D-")
		default:
			// meta and hyper have no vim notation; fall back to Bubble Tea
			// style, which ParseKeys accepts as its own field.
			return " " + k + " "
		}
	}
	var name string
	switch {
	case vimOut[base] != "":
		name = vimOut[base]
	case strings.HasPrefix(base, "f") && namedKeys[base] == base:
		name = strings.ToUpper(base)
	case base == "<":
		name = "lt"
	case base == ">" && prefix.Len() == 0:
		return ">"
	case base == "|":
		name = "Bar"
	case base == "\\":
		name = "Bslash"
	default:
		if prefix.Len() == 0 {
			return base
		}
		name = base
	}
	return "<" + prefix.String() + name + ">"
}

// DisplayKeys renders a key sequence for humans (help overlay, status line):
// single-character keys are concatenated and named or modified keys are
// separated by spaces, so {"g","g"} is "gg", {"ctrl+w","h"} is "ctrl+w h"
// and {"space","space"} is "space space". The leader placeholder is shown
// as "<leader>"; resolve it first to show the real key. Control characters
// are escaped so untrusted input cannot inject terminal escape sequences.
func DisplayKeys(keys []string) string {
	var sb strings.Builder
	prevWide := false
	for i, k := range keys {
		wide := utf8.RuneCountInString(k) > 1
		if i > 0 && (wide || prevWide) {
			sb.WriteByte(' ')
		}
		sb.WriteString(printable(k))
		prevWide = wide
	}
	return sb.String()
}

// printable makes a key string safe to draw on a terminal: control
// characters (an ESC byte could start an escape sequence) and invalid UTF-8
// are rendered as Go escapes instead of being written raw.
func printable(k string) string {
	safe := utf8.ValidString(k)
	for _, r := range k {
		if unicode.IsControl(r) || r == utf8.RuneError {
			safe = false
			break
		}
	}
	if safe {
		return k
	}
	q := strconv.QuoteToGraphic(k)
	return q[1 : len(q)-1]
}

// seqKey joins a key sequence into a map key. The unit separator cannot
// appear in a canonical key string.
func seqKey(keys []string) string { return strings.Join(keys, "\x1f") }
