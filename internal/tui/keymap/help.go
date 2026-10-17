package keymap

import (
	"sort"
	"strings"
)

// HelpEntry is one line of help: an action and the keys bound to it.
type HelpEntry struct {
	// Keys are display strings ("j", "down", "ctrl+w h").
	Keys []string
	// Action is the (first) action of the entry.
	Action Action
	// Help is the description.
	Help string
	// Group is the help heading.
	Group Group
}

// KeyLabel joins the keys with "/" ("j/down").
func (h HelpEntry) KeyLabel() string { return strings.Join(h.Keys, "/") }

// String renders "keys help" ("r reply").
func (h HelpEntry) String() string { return h.KeyLabel() + " " + h.Help }

// HelpGroup is a heading with its entries for the help overlay.
type HelpGroup struct {
	Group   Group
	Entries []HelpEntry
}

// Bindings returns the effective bindings for mode and ctx (context
// bindings merged over the mode's global ones), with the leader resolved,
// sorted by help group and then by definition order (user bindings first).
func (e *Engine) Bindings(mode Mode, ctx Context) []Binding {
	t := e.table(mode, ctx)
	list := append([]effective(nil), t.list...)
	sort.SliceStable(list, func(i, j int) bool {
		gi, gj := groupRank(list[i].b.Group), groupRank(list[j].b.Group)
		if gi != gj {
			return gi < gj
		}
		return lessEffective(list[i], list[j])
	})
	out := make([]Binding, len(list))
	for i, ef := range list {
		out[i] = ef.b
		out[i].Keys = clone(ef.b.Keys)
	}
	return out
}

func groupRank(g Group) int {
	if i, ok := groupIndex[g]; ok {
		return i
	}
	return len(Groups)
}

// Help returns the effective bindings for mode and ctx grouped for the help
// overlay. Bindings with the same action and description are merged into
// one entry listing all their keys.
func (e *Engine) Help(mode Mode, ctx Context) []HelpGroup {
	var groups []HelpGroup
	type pos struct{ g, e int }
	index := map[string]pos{}
	for _, b := range e.Bindings(mode, ctx) {
		if len(groups) == 0 || groups[len(groups)-1].Group != b.Group {
			groups = append(groups, HelpGroup{Group: b.Group})
		}
		gi := len(groups) - 1
		k := string(b.Group) + "\x00" + string(b.Action) + "\x00" + b.Help
		if p, ok := index[k]; ok {
			he := &groups[p.g].Entries[p.e]
			he.Keys = append(he.Keys, b.Display())
			continue
		}
		groups[gi].Entries = append(groups[gi].Entries, HelpEntry{
			Keys: []string{b.Display()}, Action: b.Action, Help: b.Help, Group: b.Group,
		})
		index[k] = pos{gi, len(groups[gi].Entries) - 1}
	}
	return groups
}

// ShortHelpLimit is the maximum number of status-line hints.
const ShortHelpLimit = 5

// ShortHelp returns up to ShortHelpLimit status-line hints for mode and ctx,
// using the current key bindings ("j/k move", "enter open"). Items whose
// actions are all unbound are skipped.
func (e *Engine) ShortHelp(mode Mode, ctx Context) []HelpEntry {
	items := e.shortHelpItems(mode, ctx)
	var out []HelpEntry
	for _, it := range items {
		var keys []string
		for _, a := range it.Actions {
			if ks := e.KeysForIn(a, mode, ctx); len(ks) > 0 {
				keys = append(keys, ks[0])
			}
		}
		if len(keys) == 0 {
			continue
		}
		info, _ := LookupAction(it.Actions[0])
		out = append(out, HelpEntry{Keys: keys, Action: it.Actions[0], Help: it.Label, Group: info.Group})
		if len(out) == ShortHelpLimit {
			break
		}
	}
	return out
}

// shortHelpItems finds the most specific configured hint list.
func (e *Engine) shortHelpItems(mode Mode, ctx Context) []ShortHelpItem {
	chain := ctx.chain()
	for i := len(chain) - 1; i >= 0; i-- {
		if items, ok := e.shortHelp[shortKey{mode, chain[i]}]; ok {
			return items
		}
	}
	return nil
}

// SetShortHelp replaces the status-line hint list for mode and ctx.
func (e *Engine) SetShortHelp(mode Mode, ctx Context, items []ShortHelpItem) {
	e.shortHelp[shortKey{mode, ctx}] = items
}

// KeysForIn returns the display keys bound to action in mode and ctx, the
// primary (first listed) key first.
func (e *Engine) KeysForIn(a Action, mode Mode, ctx Context) []string {
	var out []string
	for _, ef := range e.table(mode, ctx).list {
		if ef.b.Action == a {
			out = append(out, ef.b.Display())
		}
	}
	return out
}

// KeysFor returns the display keys bound to action anywhere, searching Normal
// mode first and global bindings before context ones, without duplicates.
func (e *Engine) KeysFor(a Action) []string {
	var out []string
	seen := map[string]bool{}
	for _, mode := range AllModes {
		for _, ctx := range e.contextsFor(mode) {
			for _, k := range e.KeysForIn(a, mode, ctx) {
				if !seen[k] {
					seen[k] = true
					out = append(out, k)
				}
			}
		}
	}
	return out
}

// Hint returns a short hint such as "r reply" for action in mode and ctx,
// or "" when the action is unbound there.
func (e *Engine) Hint(a Action, mode Mode, ctx Context) string {
	ks := e.KeysForIn(a, mode, ctx)
	if len(ks) == 0 {
		return ""
	}
	return ks[0] + " " + strings.ToLower(a.Help())
}
