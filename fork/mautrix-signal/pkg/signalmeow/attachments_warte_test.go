package signalmeow

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"go.mau.fi/mautrix-signal/pkg/signalmeow/web"
)

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// warte: a CDN body far larger than the pointer's declared size is refused
// before it is allocated, and a peer-chosen CDN number cannot panic.
func TestDownloadAttachmentBounds(t *testing.T) {
	old := web.SignalHTTPClient
	t.Cleanup(func() { web.SignalHTTPClient = old })
	var hosts []string
	web.SignalHTTPClient = &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		hosts = append(hosts, r.URL.Host)
		return &http.Response{
			StatusCode:    200,
			ContentLength: 1 << 40, // would be allocated in full
			Body:          io.NopCloser(strings.NewReader("")),
			Request:       r,
		}, nil
	})}
	for _, cdn := range []uint32{0, 2, 3, uint32(len(web.CDNHosts)), 1000} {
		_, err := DownloadAttachment(context.Background(), 0, "key", cdn, make([]byte, 64), nil, false, 1000, nil)
		if !errors.Is(err, ErrAttachmentTooLarge) {
			t.Errorf("cdn %d: err = %v", cdn, err)
		}
	}
	for _, h := range hosts {
		if !strings.HasSuffix(h, ".signal.org") {
			t.Errorf("host %q", h)
		}
	}
	if MaxAttachmentCiphertextSize(100<<20) < 105<<20 {
		t.Error("bound below Signal's padding")
	}
}
