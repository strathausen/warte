package backend

import (
	"testing"

	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"

	"warte/internal/core"
)

// TestEagerDownloadsNeedABoundedSize: background downloads allocate up to
// the declared size, so a pointer with no size or a large one (long text,
// link preview and quote thumbnails included) waits for the user.
func TestEagerDownloadsNeedABoundedSize(t *testing.T) {
	ptr := func(size uint32, ct string) *signalpb.AttachmentPointer {
		return &signalpb.AttachmentPointer{
			AttachmentIdentifier: &signalpb.AttachmentPointer_CdnKey{CdnKey: "k"},
			Key:                  []byte{1}, Size: proto.Uint32(size), ContentType: proto.String(ct),
		}
	}
	cases := []struct {
		role  attRole
		size  uint32
		ct    string
		eager bool
	}{
		{roleLongText, 1000, "text/x-signal-plain", true},
		{roleLongText, 0, "text/x-signal-plain", false},
		{roleLongText, maxLongText + 1, "text/x-signal-plain", false},
		{rolePreview, 5000, "image/jpeg", true},
		{rolePreview, 0, "image/jpeg", false},
		{rolePreview, 1 << 30, "image/jpeg", false},
		{roleQuote, 1 << 30, "image/jpeg", false},
		{roleSticker, 0, "image/webp", false},
		{roleSticker, 3000, "image/webp", true},
		{roleBody, 3000, "image/png", true},
		{roleBody, 0, "image/png", false},
		{roleBody, 3000, "application/pdf", false},
	}
	for _, c := range cases {
		b := &Backend{downloads: make(chan downloadJob, 1)}
		b.scheduleDownloads(core.Message{}, converted{Atts: []attachmentRef{{ID: "a", Role: c.role, Pointer: ptr(c.size, c.ct)}}})
		if got := len(b.downloads) == 1; got != c.eager {
			t.Errorf("role %v size %d %s: eager = %v, want %v", c.role, c.size, c.ct, got, c.eager)
		}
	}
}
