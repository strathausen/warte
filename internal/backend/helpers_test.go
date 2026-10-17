package backend

import (
	"bytes"
	"context"
	"crypto/rand"
	"image"
	"image/png"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"warte/internal/core"
)

// Fixed identities used across tests.
var (
	selfUUID  = uuid.MustParse("11111111-1111-4111-8111-111111111111")
	aliceUUID = uuid.MustParse("22222222-2222-4222-8222-222222222222")
	bobUUID   = uuid.MustParse("33333333-3333-4333-8333-333333333333")
	selfACI   = aciOf(selfUUID)
	aliceACI  = aciOf(aliceUUID)
	bobACI    = aciOf(bobUUID)
	aliceChat = core.DirectChatID(aliceACI)
	// A valid base64 32-byte group identifier.
	groupChat = core.GroupChatID("AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=")
)

func testKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

// testClock is a settable clock.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// newTestBackend returns an unconnected backend over a fresh store, with
// the self contact set up as if Start had run (no Signal client).
func newTestBackend(t *testing.T) (*Backend, *testClock) {
	t.Helper()
	clock := &testClock{now: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)}
	dir := t.TempDir()
	b, err := New(context.Background(), Options{
		DataDir: dir, DBKey: testKey(t), BlobKey: testKey(t),
		Logger: zerolog.Nop(), Clock: clock.Now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	self := core.Contact{ACI: selfACI, ProfileName: "Me", IsSelf: true}
	ctx := context.Background()
	if err := b.db.UpsertContact(ctx, self); err != nil {
		t.Fatal(err)
	}
	for _, c := range []core.Contact{
		{ACI: aliceACI, ProfileName: "Alice", ContactName: "Alice Chen"},
		{ACI: bobACI, ProfileName: "Bob"},
	} {
		if err := b.db.UpsertContact(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	b.mu.Lock()
	b.self = self
	b.mu.Unlock()
	return b, clock
}

// drain collects events until none arrive for a short while.
func drain(t *testing.T, b *Backend) []core.Event {
	t.Helper()
	var out []core.Event
	for {
		select {
		case ev := <-b.Events():
			out = append(out, ev)
		case <-time.After(100 * time.Millisecond):
			return out
		}
	}
}

// eventsOf filters events by type.
func eventsOf[T core.Event](evs []core.Event) []T {
	var out []T
	for _, ev := range evs {
		if e, ok := ev.(T); ok {
			out = append(out, e)
		}
	}
	return out
}

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
