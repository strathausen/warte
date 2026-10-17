package keymap

import (
	"fmt"
	"sort"
	"strings"
)

// Overrides are user key remaps as read from config.toml:
//
//	[keys.normal]
//	"<leader>m" = "chat.mute"
//	"dd" = "none"
//
//	[keys."normal.conversation"]
//	"x" = "message.delete"
//
// The outer key is a section: a mode name (see ParseMode), optionally
// followed by "." and a context (see ParseContext). The section "visual"
// applies to both Visual and VisualLine. The inner map goes from key
// notation (see ParseKeys) to an action name, or to "none" to unbind.
type Overrides map[string]map[string]string

// Unbind values accepted in Overrides.
var unbindValues = map[string]bool{"none": true, "<nop>": true, "nop": true, "": true}

// Warning reports an override that was skipped or may not behave as
// expected.
type Warning struct {
	// Section is the overrides section ("normal", "normal.chatlist").
	Section string
	// Keys is the key notation as written in the config.
	Keys string
	// Action is the value as written in the config.
	Action string
	// Message describes the problem.
	Message string
}

// Error implements error so warnings can be logged or joined.
func (w Warning) Error() string {
	switch {
	case w.Keys == "":
		return fmt.Sprintf("keys.%s: %s", w.Section, w.Message)
	default:
		return fmt.Sprintf("keys.%s %q = %q: %s", w.Section, w.Keys, w.Action, w.Message)
	}
}

// ParseSection parses an overrides section name into the modes and
// context it targets.
func ParseSection(section string) ([]Mode, Context, error) {
	modePart, ctxPart, _ := strings.Cut(strings.TrimSpace(section), ".")
	m, err := ParseMode(modePart)
	if err != nil {
		return nil, "", err
	}
	ctx, err := ParseContext(ctxPart)
	if err != nil {
		return nil, "", err
	}
	modes := []Mode{m}
	if m == Visual && strings.EqualFold(strings.TrimSpace(modePart), "visual") {
		modes = append(modes, VisualLine)
	}
	return modes, ctx, nil
}

// ApplyOverrides applies user remaps on top of the current bindings and
// returns warnings for entries that were skipped (unknown section, bad key
// notation, unknown action) or that may surprise (a binding that is a
// prefix of, or extends, another binding and so waits for the timeout; a
// binding that starts with a count digit). Valid entries are applied even
// when others fail. Entries are applied in sorted order so results are
// deterministic.
//
// Semantics match Bind and Unbind: a mode-wide entry replaces the keys in
// every context of that mode, a context entry only affects that context.
func (e *Engine) ApplyOverrides(o Overrides) []Warning {
	var warns []Warning
	sections := make([]string, 0, len(o))
	for s := range o {
		sections = append(sections, s)
	}
	sort.Strings(sections)
	for _, section := range sections {
		modes, ctx, err := ParseSection(section)
		if err != nil {
			warns = append(warns, Warning{Section: section, Message: err.Error()})
			continue
		}
		entries := o[section]
		notations := make([]string, 0, len(entries))
		for k := range entries {
			notations = append(notations, k)
		}
		sort.Strings(notations)
		for _, notation := range notations {
			value := entries[notation]
			w := Warning{Section: section, Keys: notation, Action: value}
			keys, err := ParseKeys(notation)
			if err != nil {
				w.Message = err.Error()
				warns = append(warns, w)
				continue
			}
			act := strings.TrimSpace(value)
			if unbindValues[strings.ToLower(act)] {
				for _, m := range modes {
					e.unbind(m, ctx, keys)
				}
				continue
			}
			a := Action(act)
			if !a.Valid() {
				w.Message = "unknown action" + suggest(act)
				warns = append(warns, w)
				continue
			}
			for _, m := range modes {
				e.bind(m, ctx, keys, a)
				if msg := e.reachability(m, ctx, keys); msg != "" {
					w.Message = msg
					warns = append(warns, w)
				}
			}
		}
	}
	return warns
}

// Bind binds keys (ParseKeys notation) to action in mode and ctx as a user
// binding. Binding in Global replaces the same keys in every context of the
// mode, so a remap like "r" = "chat.mute" applies everywhere.
func (e *Engine) Bind(mode Mode, ctx Context, keys string, action Action) error {
	seq, err := ParseKeys(keys)
	if err != nil {
		return err
	}
	if !action.Valid() {
		return fmt.Errorf("unknown action %q", action)
	}
	e.bind(mode, ctx, seq, action)
	return nil
}

// Unbind removes keys (ParseKeys notation) in mode and ctx. Unbinding in
// Global removes the keys from every context of the mode; unbinding in a
// context hides the mode's global binding there.
func (e *Engine) Unbind(mode Mode, ctx Context, keys string) error {
	seq, err := ParseKeys(keys)
	if err != nil {
		return err
	}
	e.unbind(mode, ctx, seq)
	return nil
}

func (e *Engine) bind(mode Mode, ctx Context, keys []string, a Action) {
	if ctx == Global {
		for _, c := range e.contextsFor(mode) {
			if c != Global {
				e.remove(mode, c, keys)
			}
		}
	}
	e.put(entry{b: makeBinding(mode, ctx, keys, a, ""), user: true})
	e.reset()
}

func (e *Engine) unbind(mode Mode, ctx Context, keys []string) {
	if ctx == Global {
		for _, c := range e.contextsFor(mode) {
			e.remove(mode, c, keys)
		}
	} else {
		e.remove(mode, ctx, keys)
		e.put(entry{b: Binding{Mode: mode, Context: ctx, Keys: keys}, user: true, unbind: true})
	}
	e.reset()
}

// reachability explains why a freshly bound sequence may not fire
// immediately, or "".
func (e *Engine) reachability(mode Mode, ctx Context, keys []string) string {
	resolved := resolve(keys, e.leader)
	if mode.Countable() && isCountDigit(resolved[0], false) {
		return "starts with a count digit and can never fire"
	}
	t := e.table(mode, ctx)
	n := t.root
	for i, k := range resolved {
		n = n.child(k)
		if n == nil {
			return ""
		}
		if i < len(resolved)-1 && n.binding != nil {
			return fmt.Sprintf("extends %q (%s), which then waits for the timeout",
				DisplayKeys(resolved[:i+1]), n.binding.Action)
		}
	}
	if len(n.children) > 0 {
		return "is a prefix of longer bindings and fires only after the timeout"
	}
	return ""
}

// suggest returns " (did you mean ...?)" for a near-miss action name.
func suggest(name string) string {
	best, bestDist := Action(""), 4
	for _, info := range registry {
		if d := editDistance(name, string(info.Name)); d < bestDist {
			best, bestDist = info.Name, d
		}
	}
	if best == "" {
		return ""
	}
	return fmt.Sprintf(" (did you mean %q?)", best)
}

// editDistance is the Levenshtein distance between a and b.
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}
