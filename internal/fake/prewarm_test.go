package fake

import (
	"strings"
	"sync"
	"testing"
)

// TestDemoMediaPrewarmed checks that every message, preview and story image
// the demo seed renders is in demoMedia, so startup renders them in parallel.
func TestDemoMediaPrewarmed(t *testing.T) {
	var mu sync.Mutex
	var keys []string
	trace := func(k string) {
		mu.Lock()
		keys = append(keys, k)
		mu.Unlock()
	}
	collect := func(f func()) map[string]bool {
		mu.Lock()
		keys = nil
		mu.Unlock()
		memoTrace.Store(&trace)
		f()
		memoTrace.Store(nil)
		mu.Lock()
		defer mu.Unlock()
		m := map[string]bool{}
		for _, k := range keys {
			m[k] = true
		}
		return m
	}
	warm := collect(func() {
		for _, gen := range demoMedia {
			gen()
		}
	})
	seeded := collect(func() { New(Options{Seed: 1}).Close() })
	for k := range seeded {
		heavy := strings.HasPrefix(k, "pattern/") || strings.HasPrefix(k, "gif/") ||
			(strings.HasPrefix(k, "landscape/") && !strings.HasPrefix(k, "landscape/96/96/"))
		if heavy && !warm[k] {
			t.Errorf("seed renders %q, which demoMedia does not prewarm", k)
		}
	}
	if len(warm) != len(demoMedia) {
		t.Errorf("demoMedia has %d entries but %d distinct keys", len(demoMedia), len(warm))
	}
}
