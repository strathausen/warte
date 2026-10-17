package termimg

import (
	"strings"
	"sync"
	"testing"
)

func TestAllocatorLRU(t *testing.T) {
	k := KittyEncoder{}
	a := NewAllocator(k, 2)

	x := a.Acquire("a")
	if x.ID != 16 || !x.Fresh || x.Evict != "" {
		t.Fatalf("first %+v", x)
	}
	y := a.Acquire("b")
	if y.ID != 17 || !y.Fresh {
		t.Fatalf("second %+v", y)
	}
	if again := a.Acquire("a"); again.ID != 16 || again.Fresh {
		t.Fatalf("re-acquire %+v", again)
	}
	// "b" is now least recently used and gets evicted; its id is recycled.
	z := a.Acquire("c")
	if z.ID != 17 || !z.Fresh || z.Evict != k.Delete(17) || len(z.Evicted) != 1 || z.Evicted[0] != "b" {
		t.Fatalf("evicting acquire %+v", z)
	}
	if _, ok := a.Lookup("b"); ok {
		t.Fatal("b should be gone")
	}
	if id, ok := a.Lookup("a"); !ok || id != 16 {
		t.Fatal("a should stay")
	}
	if a.Len() != 2 {
		t.Fatalf("Len %d", a.Len())
	}
	if got := a.Release("a"); got != k.Delete(16) {
		t.Fatalf("Release %q", got)
	}
	if a.Release("a") != "" {
		t.Fatal("double release should be empty")
	}
	if n := a.Acquire("d"); n.ID != 16 || n.Evict != "" {
		t.Fatalf("after release %+v", n)
	}
	reset := a.Reset()
	if !strings.Contains(reset, "i=16,") || !strings.Contains(reset, "i=17,") || a.Len() != 0 {
		t.Fatalf("Reset %q", reset)
	}
	if n := a.Acquire("e"); n.ID != 16 {
		t.Fatalf("after reset %+v", n)
	}
}

func TestAllocatorDefaultsAndBase(t *testing.T) {
	a := NewAllocator(KittyEncoder{}, 0)
	a.SetBase(1000)
	for i := range DefaultAllocatorCapacity {
		if got := a.Acquire(string(rune('A' + i))); got.Evict != "" {
			t.Fatalf("unexpected eviction at %d", i)
		}
	}
	if got := a.Acquire("overflow"); got.Evict == "" || got.ID != 1000 {
		t.Fatalf("expected eviction of first id, got %+v", got)
	}
}

func TestAllocatorConcurrent(t *testing.T) {
	a := NewAllocator(KittyEncoder{}, 8)
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Go(func() {
			for i := range 100 {
				a.Acquire(string(rune('a' + (g+i)%20)))
			}
		})
	}
	wg.Wait()
	if a.Len() != 8 {
		t.Fatalf("Len %d", a.Len())
	}
	seen := map[uint32]bool{}
	for i := range 20 {
		if id, ok := a.Lookup(string(rune('a' + i))); ok {
			if seen[id] {
				t.Fatalf("duplicate id %d", id)
			}
			seen[id] = true
		}
	}
}
