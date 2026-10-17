package config

import (
	"fmt"
	"strings"
)

// NerdFonts is a tri-state setting: NerdFontsAuto, NerdFontsOn or
// NerdFontsOff. In TOML it may be written as a boolean or as one of the
// strings "auto", "on"/"off", "yes"/"no", "true"/"false", "1"/"0".
type NerdFonts string

// NerdFonts values.
const (
	NerdFontsAuto NerdFonts = "auto"
	NerdFontsOn   NerdFonts = "on"
	NerdFontsOff  NerdFonts = "off"
)

// UnmarshalTOML implements toml.Unmarshaler. It never fails: unrecognized
// values are stored verbatim so validation can warn and fall back.
func (n *NerdFonts) UnmarshalTOML(v any) error {
	switch x := v.(type) {
	case bool:
		if x {
			*n = NerdFontsOn
		} else {
			*n = NerdFontsOff
		}
	case string:
		*n = ParseNerdFonts(x)
	default:
		// e.g. nerd_fonts = 1; unrecognized numbers stay invalid.
		*n = ParseNerdFonts(fmt.Sprint(v))
	}
	return nil
}

// ParseNerdFonts normalizes a textual value. Unrecognized input is returned
// unchanged (and is therefore not Valid).
func ParseNerdFonts(s string) NerdFonts {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "auto", "":
		return NerdFontsAuto
	case "on", "yes", "true", "1", "enabled":
		return NerdFontsOn
	case "off", "no", "false", "0", "disabled":
		return NerdFontsOff
	}
	return NerdFonts(s)
}

// Valid reports whether n is one of the three known values.
func (n NerdFonts) Valid() bool {
	return n == NerdFontsAuto || n == NerdFontsOn || n == NerdFontsOff
}

// Enabled resolves the setting; detected is used for NerdFontsAuto.
func (n NerdFonts) Enabled(detected bool) bool {
	switch n {
	case NerdFontsOn:
		return true
	case NerdFontsOff:
		return false
	}
	return detected
}
