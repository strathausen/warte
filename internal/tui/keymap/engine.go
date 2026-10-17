package keymap

import (
	"fmt"
	"time"
)

// DefaultTimeout is how long the engine waits for more keys when a typed
// sequence is both a complete binding and a prefix of a longer one.
const DefaultTimeout = 600 * time.Millisecond

// maxCount caps count prefixes so "99999999999j" cannot overflow.
const maxCount = 999999

// Result is the outcome of feeding one key to the Engine.
//
// Exactly one of these holds for a non-zero Result: Action is set (a binding
// fired), Pending is set (more keys are needed), Unmatched is set (the keys
// are not bound; in the text-entry modes the UI should insert them as text)
// or Canceled is set (Esc dropped a pending sequence).
type Result struct {
	// Action to perform, or "" when nothing fired.
	Action Action
	// Count is the numeric prefix, 1 when none was typed.
	Count int
	// HasCount reports whether a count was typed explicitly.
	HasCount bool
	// Register is the register named with a '"x' prefix.
	Register rune
	// HasRegister reports whether a register was named.
	HasRegister bool
	// Pending means the sequence is incomplete; show Keys like vim's
	// showcmd.
	Pending bool
	// Ambiguous means the pending sequence is also a complete binding. The
	// UI should call Flush after Timeout() to fire it if no key follows.
	Ambiguous bool
	// Expires means the pending sequence resolves by itself once Timeout()
	// passes without another key: either it is Ambiguous, or it is a
	// partial mapping in a text-entry mode (Insert, Command, Search) whose
	// keys Flush then returns as Unmatched text, like vim's timeoutlen. The
	// UI should schedule a Flush tick whenever Expires is set.
	Expires bool
	// Unmatched means the keys are not bound in this mode and context.
	Unmatched bool
	// Canceled means Esc dropped a pending sequence.
	Canceled bool
	// Repeat is set when the action is a "." repetition of the last change.
	Repeat bool
	// Keys is the typed sequence for display, including count and register
	// ("5j", "\"ayy", "ctrl+w h").
	Keys string
	// Seq is the typed sequence as canonical key strings. For an Unmatched
	// result in Insert, Command or Search mode these are the keys to insert.
	Seq []string
	// Help is the description of the fired binding.
	Help string
	// Next holds a further result produced by the same Feed call. This
	// happens when an ambiguous prefix is resolved by a key that does not
	// continue it: the prefix's action fires and the remaining keys are
	// processed again. Iterate with All.
	Next *Result
}

// Matched reports whether an action fired.
func (r Result) Matched() bool { return r.Action != "" }

// All flattens the result chain into a slice, first result first.
func (r Result) All() []Result {
	var out []Result
	for p := &r; p != nil; p = p.Next {
		c := *p
		c.Next = nil
		out = append(out, c)
	}
	return out
}

// chain appends b at the end of a's Next chain.
func chain(a, b Result) Result {
	head := a
	p := &head
	for p.Next != nil {
		next := *p.Next
		p.Next = &next
		p = p.Next
	}
	p.Next = &b
	return head
}

// state is the in-progress key sequence.
type state struct {
	ctx      Context
	raw      []string // all keys typed, for display
	seq      []string // keys matched against bindings
	countPre int      // count typed before a register
	hasPre   bool
	count    int // count being typed
	hasCount bool
	awaitReg bool
	register rune
	hasReg   bool
	ambig    *Binding // complete binding that is a prefix of seq
	ambigLen int
	deadline time.Time
}

func (s *state) active() bool { return len(s.raw) > 0 }

func (s *state) finalCount() (int, bool) {
	c, has := 1, false
	if s.hasPre {
		c *= s.countPre
		has = true
	}
	if s.hasCount {
		c *= s.count
		has = true
	}
	if c > maxCount {
		c = maxCount
	}
	return c, has
}

// Engine is a modal, vim-style key interpreter. It turns a stream of key
// strings into actions, handling modes, context layers, counts, registers,
// multi-key sequences, the leader key, ambiguity timeouts and "." repeat.
//
// An Engine is not safe for concurrent use; Bubble Tea calls Update from a
// single goroutine.
type Engine struct {
	layers    map[layerKey][]*entry
	cache     map[layerKey]*table
	seq       int
	leader    string
	timeout   time.Duration
	now       func() time.Time
	mode      Mode
	st        state
	last      *Result
	shortHelp map[shortKey][]ShortHelpItem
}

// Option configures an Engine.
type Option func(*Engine)

// WithTimeout sets the ambiguity timeout (default DefaultTimeout).
func WithTimeout(d time.Duration) Option {
	return func(e *Engine) {
		if d > 0 {
			e.timeout = d
		}
	}
}

// WithLeader sets the leader key in any notation ParseKey accepts. An
// invalid key leaves the default; use SetLeader to see the error.
func WithLeader(key string) Option {
	return func(e *Engine) { _ = e.SetLeader(key) }
}

// WithClock replaces time.Now, for tests.
func WithClock(now func() time.Time) Option {
	return func(e *Engine) { e.now = now }
}

// WithoutDefaults starts the engine with no bindings.
func WithoutDefaults() Option {
	return func(e *Engine) {
		e.layers = map[layerKey][]*entry{}
		e.invalidate()
	}
}

// New returns an Engine in Normal mode loaded with DefaultSpecs.
func New(opts ...Option) *Engine {
	e := &Engine{
		layers:    map[layerKey][]*entry{},
		cache:     map[layerKey]*table{},
		leader:    DefaultLeader,
		timeout:   DefaultTimeout,
		now:       time.Now,
		shortHelp: map[shortKey][]ShortHelpItem{},
	}
	for k, v := range defaultShortHelp {
		e.shortHelp[k] = v
	}
	for _, s := range DefaultSpecs {
		keys, err := ParseKeys(s.Keys)
		if err != nil {
			panic(fmt.Sprintf("keymap: bad default keys %q: %v", s.Keys, err))
		}
		for _, m := range s.Modes {
			e.put(entry{b: makeBinding(m, s.Context, keys, s.Action, s.Help)})
		}
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

func makeBinding(mode Mode, ctx Context, keys []string, a Action, help string) Binding {
	info, _ := LookupAction(a)
	if help == "" {
		help = a.Help()
	}
	return Binding{Mode: mode, Context: ctx, Keys: keys, Action: a, Help: help, Group: info.Group}
}

// Mode returns the current mode.
func (e *Engine) Mode() Mode { return e.mode }

// SetMode switches mode and drops any pending sequence. The engine never
// changes mode by itself: the UI decides when an action such as
// ActModeInsert actually enters Insert mode.
func (e *Engine) SetMode(m Mode) {
	e.mode = m
	e.reset()
}

// Leader returns the canonical leader key (default "space").
func (e *Engine) Leader() string { return e.leader }

// SetLeader changes the leader key. It accepts any notation ParseKey does
// (" ", "<Space>", ",", "\\").
func (e *Engine) SetLeader(key string) error {
	k, err := ParseKey(key)
	if err != nil {
		return fmt.Errorf("leader: %w", err)
	}
	if k == LeaderToken {
		return fmt.Errorf("leader cannot be <leader>")
	}
	e.leader = k
	e.invalidate()
	e.reset()
	return nil
}

// Timeout returns how long the UI should wait before calling Flush after a
// Result with Ambiguous set.
func (e *Engine) Timeout() time.Duration { return e.timeout }

// SetTimeout changes the ambiguity timeout; non-positive values are ignored.
func (e *Engine) SetTimeout(d time.Duration) {
	if d > 0 {
		e.timeout = d
	}
}

// Pending reports whether a key sequence is in progress.
func (e *Engine) Pending() bool { return e.st.active() }

// PendingKeys returns the in-progress sequence for display (showcmd), or "".
func (e *Engine) PendingKeys() string { return DisplayKeys(e.st.raw) }

// Deadline returns when a pending expiring sequence times out (see
// Result.Expires).
func (e *Engine) Deadline() (time.Time, bool) {
	if !e.expiring() {
		return time.Time{}, false
	}
	return e.st.deadline, true
}

// Reset drops any pending sequence, count and register.
func (e *Engine) Reset() { e.reset() }

func (e *Engine) reset() { e.st = state{} }

// LastChange returns the action "." would repeat.
func (e *Engine) LastChange() (Result, bool) {
	if e.last == nil {
		return Result{}, false
	}
	return *e.last, true
}

// SetLastChange records r as the change "." repeats. The engine records
// Repeatable actions itself; the UI can use this for changes it performs
// outside the keymap (for example after choosing a reaction in a picker).
func (e *Engine) SetLastChange(r Result) {
	r.Next = nil
	r.Pending, r.Ambiguous, r.Expires, r.Unmatched, r.Canceled, r.Repeat = false, false, false, false, false, false
	if r.Count < 1 {
		r.Count = 1
	}
	e.last = &r
}

// Feed processes one key (a canonical key string, see Canonical) typed
// while ctx has focus, in the current mode.
//
// Lookup consults the context's bindings first, then the mode's global
// bindings. In Normal and Visual modes a count ("5j", "10G") and a register
// ('"a') may precede the sequence; "0" is a count digit only after another
// digit. Esc cancels a pending sequence. If a previous ambiguous sequence
// timed out and Flush was not called, it fires first and both results are
// returned in a chain (see Result.Next).
func (e *Engine) Feed(key string, ctx Context) Result {
	key = Canonical(key)
	if key == "" {
		return Result{Count: 1, Unmatched: true}
	}
	if e.expiring() && !e.now().Before(e.st.deadline) {
		pre := e.resolveExpired()
		return chain(pre, e.feed(key, ctx))
	}
	return e.feed(key, ctx)
}

// expiring reports whether the pending sequence resolves on timeout.
func (e *Engine) expiring() bool {
	return e.st.ambig != nil || (e.st.active() && !e.mode.Countable())
}

// resolveExpired resolves a timed-out sequence: an ambiguous binding fires,
// a partial text-mode mapping becomes Unmatched text.
func (e *Engine) resolveExpired() Result {
	if e.st.ambig != nil {
		return e.resolveAmbiguous()
	}
	return e.unmatched()
}

func (e *Engine) feed(key string, ctx Context) Result {
	if e.st.active() && e.st.ctx != ctx {
		e.reset()
	}
	e.st.ctx = ctx

	if key == "esc" && e.st.active() {
		res := Result{Count: 1, Canceled: true, Keys: DisplayKeys(e.st.raw), Seq: clone(e.st.raw)}
		e.reset()
		return res
	}

	t := e.table(e.mode, ctx)

	if e.st.awaitReg {
		e.st.raw = append(e.st.raw, key)
		r, ok := registerRune(key)
		if !ok {
			return e.unmatched()
		}
		e.st.awaitReg = false
		e.st.register, e.st.hasReg = r, true
		return e.pending()
	}

	if len(e.st.seq) == 0 && e.mode.Countable() {
		if isCountDigit(key, e.st.hasCount) {
			e.st.count = min(e.st.count*10+int(key[0]-'0'), maxCount)
			e.st.hasCount = true
			e.st.raw = append(e.st.raw, key)
			return e.pending()
		}
		if key == `"` && !e.st.hasReg && t.root.child(`"`) == nil {
			if e.st.hasCount {
				e.st.countPre, e.st.hasPre = e.st.count, true
				e.st.count, e.st.hasCount = 0, false
			}
			e.st.awaitReg = true
			e.st.raw = append(e.st.raw, key)
			return e.pending()
		}
	}

	e.st.raw = append(e.st.raw, key)
	seq := append(clone(e.st.seq), key)
	n := t.root
	for _, k := range seq {
		n = n.child(k)
	}

	switch {
	case n == nil && e.st.ambig != nil:
		// The longer sequence failed: fire the complete prefix, then
		// reprocess the keys after it.
		leftover := seq[e.st.ambigLen:]
		raw := e.st.raw[:len(e.st.raw)-len(leftover)]
		res := e.emit(*e.st.ambig, raw)
		for _, k := range leftover {
			res = chain(res, e.feed(k, ctx))
		}
		return res
	case n == nil && len(seq) > 1 && !e.mode.Countable():
		// In the text-entry modes a failed mapping prefix is text: return
		// its first key as Unmatched and reprocess the rest, so a partial
		// "jk" mapping followed by enter inserts "j" and then sends.
		first := seq[:1]
		rest := clone(seq[1:])
		res := Result{Count: 1, Unmatched: true, Keys: DisplayKeys(first), Seq: clone(first)}
		e.reset()
		for _, k := range rest {
			res = chain(res, e.feed(k, ctx))
		}
		return res
	case n == nil:
		return e.unmatched()
	case n.binding != nil && len(n.children) == 0:
		return e.emit(*n.binding, e.st.raw)
	}

	e.st.seq = seq
	if n.binding != nil {
		e.st.ambig = n.binding
		e.st.ambigLen = len(seq)
	}
	if e.expiring() {
		e.st.deadline = e.now().Add(e.timeout)
	}
	return e.pending()
}

// Flush resolves a pending sequence whose timeout has passed: an ambiguous
// binding fires, and a partial mapping in a text-entry mode is returned as
// Unmatched keys to insert. If the timeout has not passed yet, or the
// pending sequence does not expire (a plain prefix in Normal mode), it
// returns the pending state. With nothing pending it returns a Result with
// no Action and Pending unset. The UI calls it from a tick scheduled
// Timeout() after an Expires result; stale ticks are harmless.
func (e *Engine) Flush() Result {
	if !e.expiring() {
		if e.st.active() {
			return e.pending()
		}
		return Result{Count: 1}
	}
	if e.now().Before(e.st.deadline) {
		return e.pending()
	}
	return e.resolveExpired()
}

// Resolve resolves an expiring pending sequence immediately, ignoring the
// timeout. Otherwise it behaves like Flush.
func (e *Engine) Resolve() Result {
	if !e.expiring() {
		return e.Flush()
	}
	return e.resolveExpired()
}

func (e *Engine) resolveAmbiguous() Result {
	ctx := e.st.ctx
	leftover := clone(e.st.seq[e.st.ambigLen:])
	raw := e.st.raw[:len(e.st.raw)-len(leftover)]
	res := e.emit(*e.st.ambig, raw)
	for _, k := range leftover {
		res = chain(res, e.feed(k, ctx))
	}
	return res
}

func (e *Engine) pending() Result {
	c, has := e.st.finalCount()
	return Result{
		Count: c, HasCount: has,
		Register: e.st.register, HasRegister: e.st.hasReg,
		Pending: true, Ambiguous: e.st.ambig != nil, Expires: e.expiring(),
		Keys: DisplayKeys(e.st.raw), Seq: clone(e.st.raw),
	}
}

func (e *Engine) unmatched() Result {
	res := Result{Count: 1, Unmatched: true, Keys: DisplayKeys(e.st.raw), Seq: clone(e.st.raw)}
	e.reset()
	return res
}

// emit fires b and resets the sequence state.
func (e *Engine) emit(b Binding, raw []string) Result {
	c, has := e.st.finalCount()
	res := Result{
		Action: b.Action, Count: c, HasCount: has,
		Register: e.st.register, HasRegister: e.st.hasReg,
		Keys: DisplayKeys(raw), Seq: clone(raw), Help: b.Help,
	}
	e.reset()

	if b.Action == ActRepeat {
		if e.last == nil {
			return res
		}
		r := *e.last
		r.Repeat = true
		r.Keys, r.Seq = res.Keys, res.Seq
		if has {
			r.Count, r.HasCount = c, true
			e.last.Count, e.last.HasCount = c, true
		}
		if res.HasRegister {
			r.Register, r.HasRegister = res.Register, true
		}
		return r
	}
	if info, ok := LookupAction(b.Action); ok && info.Repeatable {
		l := res
		e.last = &l
	}
	return res
}

// isCountDigit reports whether key continues or starts a count. "0" only
// continues one.
func isCountDigit(key string, counting bool) bool {
	if len(key) != 1 {
		return false
	}
	c := key[0]
	return (c >= '1' && c <= '9') || (c == '0' && counting)
}

func clone(s []string) []string {
	if s == nil {
		return nil
	}
	return append([]string(nil), s...)
}
