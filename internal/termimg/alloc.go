package termimg

import (
	"container/list"
	"strings"
	"sync"
)

// DefaultAllocatorCapacity is used by [NewAllocator] for capacity <= 0. It
// keeps every id in the 256-color range (16..255), which survives color
// profile conversions better than 24-bit ids.
const DefaultAllocatorCapacity = 240

// Allocation is the result of [Allocator.Acquire].
type Allocation struct {
	// ID is the kitty image id for the key.
	ID uint32
	// Fresh is true when the image must be (re)transmitted: the key was not
	// resident, or its id was recycled.
	Fresh bool
	// Evict holds delete sequences for images evicted to make room. Write
	// it to the terminal before transmitting the new image.
	Evict string
	// Evicted lists the keys that were evicted.
	Evicted []string
}

// Allocator hands out kitty image ids for cache keys and evicts the least
// recently used image when full, emitting the matching delete sequences.
// It is safe for concurrent use.
//
// Ids start at Base (default 16) and are reused after eviction. Use a
// distinct key per (attachment, size) pair, since a virtual placement has a
// fixed cell size.
type Allocator struct {
	mu       sync.Mutex
	kitty    KittyEncoder
	capacity int
	base     uint32
	next     uint32
	free     []uint32
	lru      *list.List // front = most recent; values are *allocEntry
	byKey    map[string]*list.Element
}

type allocEntry struct {
	key string
	id  uint32
}

// NewAllocator returns an allocator holding at most capacity images, using
// k to produce delete sequences.
func NewAllocator(k KittyEncoder, capacity int) *Allocator {
	if capacity <= 0 {
		capacity = DefaultAllocatorCapacity
	}
	return &Allocator{
		kitty:    k,
		capacity: capacity,
		base:     16,
		next:     16,
		lru:      list.New(),
		byKey:    make(map[string]*list.Element),
	}
}

// SetBase sets the first id handed out, for example to avoid clashing with
// another program sharing the terminal. It only affects ids not yet issued
// and must be called before the first Acquire.
func (a *Allocator) SetBase(base uint32) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if base == 0 {
		base = 1
	}
	a.base, a.next = base, base
}

// Acquire returns the id for key, marking it most recently used. When the key
// is new and the allocator is full, the least recently used images are
// evicted.
func (a *Allocator) Acquire(key string) Allocation {
	a.mu.Lock()
	defer a.mu.Unlock()
	if el, ok := a.byKey[key]; ok {
		a.lru.MoveToFront(el)
		return Allocation{ID: el.Value.(*allocEntry).id}
	}
	var res Allocation
	var evict strings.Builder
	for a.lru.Len() >= a.capacity {
		el := a.lru.Back()
		e := el.Value.(*allocEntry)
		a.lru.Remove(el)
		delete(a.byKey, e.key)
		a.free = append(a.free, e.id)
		evict.WriteString(a.kitty.Delete(e.id))
		res.Evicted = append(res.Evicted, e.key)
	}
	var id uint32
	if n := len(a.free); n > 0 {
		id = a.free[0]
		a.free = a.free[1:]
	} else {
		id = a.next
		a.next++
		if a.next == 0 {
			a.next = 1
		}
	}
	a.byKey[key] = a.lru.PushFront(&allocEntry{key: key, id: id})
	res.ID = id
	res.Fresh = true
	res.Evict = evict.String()
	return res
}

// Lookup returns the id for key without changing recency.
func (a *Allocator) Lookup(key string) (uint32, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if el, ok := a.byKey[key]; ok {
		return el.Value.(*allocEntry).id, true
	}
	return 0, false
}

// Release frees key's id and returns the delete sequence for it ("" if the
// key was unknown).
func (a *Allocator) Release(key string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	el, ok := a.byKey[key]
	if !ok {
		return ""
	}
	e := el.Value.(*allocEntry)
	a.lru.Remove(el)
	delete(a.byKey, key)
	a.free = append(a.free, e.id)
	return a.kitty.Delete(e.id)
}

// Reset forgets every allocation and returns delete sequences for all of
// them.
func (a *Allocator) Reset() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var b strings.Builder
	for el := a.lru.Front(); el != nil; el = el.Next() {
		b.WriteString(a.kitty.Delete(el.Value.(*allocEntry).id))
	}
	a.lru.Init()
	clear(a.byKey)
	a.free = nil
	a.next = a.base
	return b.String()
}

// Len returns the number of resident images.
func (a *Allocator) Len() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lru.Len()
}
