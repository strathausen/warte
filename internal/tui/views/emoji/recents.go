package emoji

import (
	"sync"

	"warte/internal/core"
	"warte/internal/format"
)

// DefaultRecentLimit is how many emojis and stickers a Recents keeps.
const DefaultRecentLimit = 32

// Recents remembers the emojis and stickers picked during a session (most
// recently used first, with use counts for "frequently used") and the chosen
// skin tone. It is safe for concurrent use.
//
// The pickers use Session unless Options.Recents is set. Recents are kept in
// memory only; an integrator that wants them to survive restarts can load
// them with SetEmojis/SetStickers/SetSkinTone at startup and save them from
// the OnChange hook.
type Recents struct {
	mu       sync.Mutex
	limit    int
	emojis   []string
	counts   map[string]int
	stickers []core.Sticker
	tone     format.SkinTone
	onChange func()
}

// Session is the process-wide Recents used by default.
var Session = NewRecents(DefaultRecentLimit)

// NewRecents returns an empty store keeping at most limit entries per kind
// (limit <= 0 uses DefaultRecentLimit).
func NewRecents(limit int) *Recents {
	if limit <= 0 {
		limit = DefaultRecentLimit
	}
	return &Recents{limit: limit, counts: map[string]int{}}
}

// OnChange installs a hook called (outside the lock, from the goroutine that
// made the change) after every change. Pass nil to remove it.
func (r *Recents) OnChange(f func()) {
	r.mu.Lock()
	r.onChange = f
	r.mu.Unlock()
}

func (r *Recents) changed() {
	r.mu.Lock()
	f := r.onChange
	r.mu.Unlock()
	if f != nil {
		f()
	}
}

// Emojis returns the recently used emojis, most recent first.
func (r *Recents) Emojis() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.emojis...)
}

// Frequent returns up to n emojis ordered by use count (ties: most recent
// first). n <= 0 means all.
func (r *Recents) Frequent(n int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := append([]string(nil), r.emojis...)
	// Stable insertion sort by count: the list is short.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && r.counts[out[j]] > r.counts[out[j-1]]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

// Count returns how often e was picked this session.
func (r *Recents) Count(e string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.counts[e]
}

// AddEmoji records a use of e (moving it to the front).
func (r *Recents) AddEmoji(e string) {
	if e == "" {
		return
	}
	r.mu.Lock()
	r.emojis = moveFront(r.emojis, e, r.limit)
	r.counts[e]++
	r.mu.Unlock()
	r.changed()
}

// SetEmojis replaces the recent emojis (most recent first), e.g. when
// restoring them from disk. Use counts are reset to 1.
func (r *Recents) SetEmojis(list []string) {
	r.mu.Lock()
	r.emojis = nil
	r.counts = map[string]int{}
	for i := len(list) - 1; i >= 0; i-- {
		if list[i] != "" {
			r.emojis = moveFront(r.emojis, list[i], r.limit)
			r.counts[list[i]] = 1
		}
	}
	r.mu.Unlock()
	r.changed()
}

// Stickers returns the recently sent stickers, most recent first.
func (r *Recents) Stickers() []core.Sticker {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]core.Sticker(nil), r.stickers...)
}

// AddSticker records that s was sent.
func (r *Recents) AddSticker(s core.Sticker) {
	r.mu.Lock()
	out := []core.Sticker{s}
	for _, o := range r.stickers {
		if o.PackID == s.PackID && o.StickerID == s.StickerID {
			continue
		}
		if len(out) >= r.limit {
			break
		}
		out = append(out, o)
	}
	r.stickers = out
	r.mu.Unlock()
	r.changed()
}

// SetStickers replaces the recent stickers (most recent first).
func (r *Recents) SetStickers(list []core.Sticker) {
	r.mu.Lock()
	r.stickers = nil
	for _, s := range list {
		if len(r.stickers) >= r.limit {
			break
		}
		r.stickers = append(r.stickers, s)
	}
	r.mu.Unlock()
	r.changed()
}

// SkinTone returns the skin tone applied to supporting emojis.
func (r *Recents) SkinTone() format.SkinTone {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.tone
}

// SetSkinTone sets the skin tone applied to supporting emojis.
func (r *Recents) SetSkinTone(t format.SkinTone) {
	r.mu.Lock()
	r.tone = t
	r.mu.Unlock()
	r.changed()
}

// SkinTones lists the selectable tones in cycle order.
var SkinTones = []format.SkinTone{
	format.SkinToneNone,
	format.SkinToneLight,
	format.SkinToneMediumLight,
	format.SkinToneMedium,
	format.SkinToneMediumDark,
	format.SkinToneDark,
}

// NextSkinTone returns the tone after t in SkinTones (dir < 0: before),
// wrapping around.
func NextSkinTone(t format.SkinTone, dir int) format.SkinTone {
	i := 0
	for k, s := range SkinTones {
		if s == t {
			i = k
		}
	}
	n := len(SkinTones)
	if dir < 0 {
		return SkinTones[(i-1+n)%n]
	}
	return SkinTones[(i+1)%n]
}

func moveFront(list []string, e string, limit int) []string {
	out := make([]string, 0, min(len(list)+1, limit))
	out = append(out, e)
	for _, o := range list {
		if o == e {
			continue
		}
		if len(out) >= limit {
			break
		}
		out = append(out, o)
	}
	return out
}
