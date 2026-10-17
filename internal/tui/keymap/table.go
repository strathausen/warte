package keymap

import (
	"fmt"
	"sort"
)

// Binding maps a key sequence to an action in a mode and context.
type Binding struct {
	// Mode the binding is active in.
	Mode Mode
	// Context scopes the binding; Global applies to the whole mode.
	Context Context
	// Keys is the canonical key sequence. Bindings returned by the engine
	// have the leader placeholder resolved to the actual leader key.
	Keys []string
	// Action performed when the sequence is typed.
	Action Action
	// Help is the human description shown in the help overlay.
	Help string
	// Group is the help overlay heading.
	Group Group
}

// Display renders the binding's keys for humans ("gg", "ctrl+w h").
func (b Binding) Display() string { return DisplayKeys(b.Keys) }

// Notation renders the binding's keys in config notation ("<C-w>h").
func (b Binding) Notation() string { return FormatNotation(b.Keys) }

// entry is a stored binding (raw keys, possibly containing LeaderToken).
type entry struct {
	b      Binding
	order  int
	user   bool
	unbind bool // tombstone: hides less specific bindings for these keys
}

type layerKey struct {
	mode Mode
	ctx  Context
}

// node is a key trie node.
type node struct {
	children map[string]*node
	binding  *Binding
	order    int
}

func (n *node) child(k string) *node {
	if n == nil || n.children == nil {
		return nil
	}
	return n.children[k]
}

// table is the effective lookup structure for one mode and context.
type table struct {
	root *node
	// list holds effective bindings (resolved keys) in priority order.
	list []effective
}

type effective struct {
	b     Binding
	order int
	user  bool
}

// resolve replaces the leader placeholder with the leader key.
func resolve(keys []string, leader string) []string {
	out := make([]string, len(keys))
	for i, k := range keys {
		if k == LeaderToken {
			k = leader
		}
		out[i] = k
	}
	return out
}

// buildTable merges the layers along ctx's chain for mode.
func (e *Engine) buildTable(mode Mode, ctx Context) *table {
	merged := map[string]effective{}
	for _, c := range ctx.chain() {
		// Within one layer two entries can only collide once the leader is
		// resolved (say "<leader>," with leader ","). The first definition
		// wins, unless a later one is a user binding.
		inLayer := map[string]bool{}
		for _, en := range e.layers[layerKey{mode, c}] {
			keys := resolve(en.b.Keys, e.leader)
			k := seqKey(keys)
			if inLayer[k] && !en.user {
				continue
			}
			inLayer[k] = true
			if en.unbind {
				delete(merged, k)
				continue
			}
			b := en.b
			b.Keys = keys
			merged[k] = effective{b: b, order: en.order, user: en.user}
		}
	}
	t := &table{root: &node{}}
	for _, ef := range merged {
		t.list = append(t.list, ef)
	}
	sort.Slice(t.list, func(i, j int) bool { return lessEffective(t.list[i], t.list[j]) })
	for i := range t.list {
		n := t.root
		for _, k := range t.list[i].b.Keys {
			if n.children == nil {
				n.children = map[string]*node{}
			}
			c := n.children[k]
			if c == nil {
				c = &node{}
				n.children[k] = c
			}
			n = c
		}
		n.binding = &t.list[i].b
		n.order = t.list[i].order
	}
	return t
}

// lessEffective orders bindings: user bindings first, then definition order.
func lessEffective(a, b effective) bool {
	if a.user != b.user {
		return a.user
	}
	return a.order < b.order
}

// table returns the cached effective table for mode and ctx.
func (e *Engine) table(mode Mode, ctx Context) *table {
	k := layerKey{mode, ctx}
	if t, ok := e.cache[k]; ok {
		return t
	}
	t := e.buildTable(mode, ctx)
	e.cache[k] = t
	return t
}

// invalidate drops cached tables after a binding or leader change.
func (e *Engine) invalidate() { e.cache = map[layerKey]*table{} }

// put stores an entry, replacing an entry with identical raw keys in the
// same layer.
func (e *Engine) put(en entry) {
	e.seq++
	en.order = e.seq
	lk := layerKey{en.b.Mode, en.b.Context}
	k := seqKey(en.b.Keys)
	layer := e.layers[lk]
	for i, old := range layer {
		if seqKey(old.b.Keys) == k {
			layer = append(layer[:i], layer[i+1:]...)
			break
		}
	}
	e.layers[lk] = append(layer, &en)
	e.invalidate()
}

// remove deletes entries with the given resolved keys from a layer.
func (e *Engine) remove(mode Mode, ctx Context, keys []string) {
	lk := layerKey{mode, ctx}
	k := seqKey(resolve(keys, e.leader))
	layer := e.layers[lk][:0]
	for _, en := range e.layers[lk] {
		if seqKey(resolve(en.b.Keys, e.leader)) != k {
			layer = append(layer, en)
		}
	}
	e.layers[lk] = layer
	e.invalidate()
}

// contextsFor lists the contexts that have a layer in mode, plus Global,
// sorted.
func (e *Engine) contextsFor(mode Mode) []Context {
	seen := map[Context]bool{Global: true}
	out := []Context{Global}
	for lk := range e.layers {
		if lk.mode == mode && !seen[lk.ctx] {
			seen[lk.ctx] = true
			out = append(out, lk.ctx)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Conflict describes a problem in the binding tables.
type Conflict struct {
	// Mode and Context of the effective table with the problem.
	Mode    Mode
	Context Context
	// Keys is the display form of the offending sequence.
	Keys string
	// Message explains the conflict.
	Message string
}

// String implements fmt.Stringer.
func (c Conflict) String() string {
	return fmt.Sprintf("%s/%s %q: %s", c.Mode, c.Context, c.Keys, c.Message)
}

// Conflicts reports true conflicts in the current bindings:
//
//   - two bindings in the same layer whose keys are identical once the
//     leader is resolved but whose actions differ (one silently wins);
//   - a complete binding that is also a prefix of a longer one in the same
//     effective table (it only fires after the ambiguity timeout);
//   - a binding starting with a count digit 1-9 in a counting mode (it can
//     never fire).
//
// Overriding a less specific layer from a more specific one is intended
// and not reported.
func (e *Engine) Conflicts() []Conflict {
	var out []Conflict
	reported := map[string]bool{}
	add := func(c Conflict) {
		k := c.Mode.String() + "\x00" + c.Keys + "\x00" + c.Message
		if !reported[k] {
			reported[k] = true
			out = append(out, c)
		}
	}
	for _, mode := range AllModes {
		for _, ctx := range e.contextsFor(mode) {
			// Same-layer duplicates.
			seen := map[string]Action{}
			for _, en := range e.layers[layerKey{mode, ctx}] {
				if en.unbind {
					continue
				}
				keys := resolve(en.b.Keys, e.leader)
				k := seqKey(keys)
				if a, ok := seen[k]; ok && a != en.b.Action {
					add(Conflict{mode, ctx, DisplayKeys(keys),
						fmt.Sprintf("bound to both %s and %s", a, en.b.Action)})
				}
				seen[k] = en.b.Action
			}
			// Prefix ambiguity and unreachable digits in the merged table.
			t := e.table(mode, ctx)
			for _, ef := range t.list {
				n := t.root
				for _, k := range ef.b.Keys {
					n = n.child(k)
				}
				if len(n.children) > 0 {
					add(Conflict{mode, ctx, ef.b.Display(),
						fmt.Sprintf("%s is also a prefix of longer bindings (resolved by timeout)", ef.b.Action)})
				}
				if mode.Countable() && isCountDigit(ef.b.Keys[0], false) {
					add(Conflict{mode, ctx, ef.b.Display(),
						"starts with a count digit and can never fire"})
				}
			}
		}
	}
	return out
}
