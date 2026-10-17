package backend

import (
	"context"
	"path/filepath"
	"testing"

	"go.mau.fi/mautrix-signal/pkg/libsignalgo"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/store"

	"warte/internal/core"
)

// TestIsLinkedAndOfflineExport: the linked check and the export read only
// local state; neither needs Start (a Signal connection). The test's
// network guard (nonetwork_test.go) fails the run if either tried.
func TestIsLinkedAndOfflineExport(t *testing.T) {
	b, _ := newTestBackend(t)
	ctx := context.Background()
	// newTestBackend fakes b.self; a fresh, unstarted backend has none.
	b.mu.Lock()
	b.self = core.Contact{}
	b.mu.Unlock()

	if linked, err := b.IsLinked(ctx); err != nil || linked {
		t.Fatalf("IsLinked without device = %v, %v", linked, err)
	}
	ikp, err := libsignalgo.GenerateIdentityKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	dev := &store.DeviceData{ACIIdentityKeyPair: ikp, ACI: selfUUID, DeviceID: 2, Password: "pw", Number: "+15550000"}
	if err := b.container.PutDevice(ctx, dev); err != nil {
		t.Fatal(err)
	}
	if linked, err := b.IsLinked(ctx); err != nil || !linked {
		t.Fatalf("IsLinked with device = %v, %v", linked, err)
	}
	if b.ConnState() == core.ConnConnecting || b.ConnState() == core.ConnConnected {
		t.Fatal("IsLinked started a connection")
	}

	path := filepath.Join(t.TempDir(), "backup.warte")
	if err := b.ExportBackup(ctx, path); err != nil {
		t.Fatal(err)
	}
	doc, err := ReadExport(path, b.opts.BlobKey)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Self.ACI != selfACI || doc.Self.ProfileName != "Me" || !doc.Self.IsSelf {
		t.Errorf("offline export self = %+v", doc.Self)
	}
}
