package backend

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"testing"

	"go.mau.fi/mautrix-signal/pkg/signalmeow/web"
)

// Tests must never reach the network. TestMain replaces every HTTP layer
// the backend can use (signalmeow's Signal client, which also carries the
// websockets, attachment CDN downloads, the link preview client and Go's
// default transport) with one that refuses the request and records it, and
// points the CDN hosts at the reserved .invalid TLD as a second line of
// defence. Any attempted request fails the run, listed at the end.

var errNetworkInTest = errors.New("backend test: network access is not allowed")

type noNetwork struct {
	mu       sync.Mutex
	attempts []string
}

func (n *noNetwork) record(what string) error {
	n.mu.Lock()
	n.attempts = append(n.attempts, what)
	n.mu.Unlock()
	return fmt.Errorf("%w (%s)", errNetworkInTest, what)
}

func (n *noNetwork) RoundTrip(req *http.Request) (*http.Response, error) {
	return nil, n.record(req.Method + " " + req.URL.Redacted())
}

func (n *noNetwork) violations() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.attempts...)
}

var testNetGuard = &noNetwork{}

func TestMain(m *testing.M) {
	http.DefaultTransport = testNetGuard
	http.DefaultClient.Transport = testNetGuard
	web.SignalHTTPClient.Transport = testNetGuard
	previewClient.Transport = testNetGuard
	for i := range web.CDNHosts {
		web.CDNHosts[i] = fmt.Sprintf("cdn%d.warte-test.invalid", i)
	}
	code := m.Run()
	if v := testNetGuard.violations(); len(v) > 0 {
		fmt.Fprintf(os.Stderr, "FAIL: %d network request(s) attempted by backend tests:\n", len(v))
		for _, a := range v {
			fmt.Fprintln(os.Stderr, "  ", a)
		}
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

// TestNetworkGuard checks that the guard catches signalmeow's HTTP layer,
// CDN downloads and the link preview client.
func TestNetworkGuard(t *testing.T) {
	before := len(testNetGuard.violations())
	ctx := context.Background()
	if _, err := web.SendHTTPRequest(ctx, web.StorageHostname, http.MethodGet, "/v1/storage/manifest", nil); !errors.Is(err, errNetworkInTest) {
		t.Errorf("signalmeow request: %v", err)
	}
	if _, err := web.GetAttachment(ctx, "/attachments/x", 2); !errors.Is(err, errNetworkInTest) {
		t.Errorf("CDN download: %v", err)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.com/", nil)
	if _, err := previewClient.Do(req); !errors.Is(err, errNetworkInTest) {
		t.Errorf("preview client: %v", err)
	}
	got := testNetGuard.violations()[before:]
	if len(got) != 3 {
		t.Fatalf("recorded %v", got)
	}
	// These were deliberate; do not fail the run for them.
	testNetGuard.mu.Lock()
	testNetGuard.attempts = testNetGuard.attempts[:before]
	testNetGuard.mu.Unlock()
}
