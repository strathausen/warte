package keymap

import "unicode"

// Register names with special meaning.
const (
	// RegUnnamed is the default register ('"'), written by every yank.
	RegUnnamed = '"'
	// RegYank ('0') holds the most recent yank.
	RegYank = '0'
	// RegBlackhole ('_') discards what is written to it.
	RegBlackhole = '_'
	// RegClipboard ('+') is the system clipboard.
	RegClipboard = '+'
	// RegSelection ('*') is the primary selection; warte treats it as the
	// clipboard.
	RegSelection = '*'
)

// IsValidRegister reports whether r can follow '"' as a register name:
// a-z, A-Z (append to a-z), 0-9, '"', '_', '+', '*' and '-'.
func IsValidRegister(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	}
	switch r {
	case RegUnnamed, RegBlackhole, RegClipboard, RegSelection, '-':
		return true
	}
	return false
}

// IsClipboard reports whether the register is backed by the system
// clipboard ('+' or '*'). The UI writes those to the clipboard itself.
func IsClipboard(r rune) bool { return r == RegClipboard || r == RegSelection }

// registerRune converts a key string to a register name.
func registerRune(key string) (rune, bool) {
	rs := []rune(key)
	if len(rs) != 1 || !IsValidRegister(rs[0]) {
		return 0, false
	}
	return rs[0], true
}

// Registers is a small vim-style register store for yanked message text.
// The zero value is ready to use. Clipboard registers are stored too, so
// the UI can mirror them, but reading the real clipboard is the UI's job.
type Registers struct {
	m map[rune]string
}

// Yank writes text to reg (RegUnnamed when reg is 0). Uppercase registers
// append to their lowercase counterpart, the blackhole register discards,
// and every non-blackhole yank also updates the unnamed register and, when
// no register was named, RegYank.
func (r *Registers) Yank(reg rune, text string) {
	if reg == 0 {
		reg = RegUnnamed
	}
	if reg == RegBlackhole || !IsValidRegister(reg) {
		return
	}
	if r.m == nil {
		r.m = map[rune]string{}
	}
	if unicode.IsUpper(reg) {
		lower := unicode.ToLower(reg)
		if prev, ok := r.m[lower]; ok && prev != "" {
			text = prev + "\n" + text
		}
		reg = lower
	}
	if reg == RegUnnamed {
		r.m[RegYank] = text
	}
	r.m[reg] = text
	r.m[RegUnnamed] = text
}

// Get returns the contents of reg (RegUnnamed when reg is 0). Uppercase
// names read their lowercase register.
func (r *Registers) Get(reg rune) (string, bool) {
	if reg == 0 {
		reg = RegUnnamed
	}
	reg = unicode.ToLower(reg)
	s, ok := r.m[reg]
	return s, ok
}

// Clear empties every register.
func (r *Registers) Clear() { r.m = nil }
