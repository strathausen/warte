package store

import (
	"sync"
	"testing"

	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"
)

// warte: the account record accessors are safe for concurrent use and
// copy on read and write. Run with -race.
func TestAccountRecordConcurrentAccess(t *testing.T) {
	var d DeviceData
	if d.HasAccountRecord() || d.AccountRecord() != nil {
		t.Fatal("zero device has an account record")
	}
	if d.UpdateAccountRecord(func(*signalpb.AccountRecord) { t.Error("update ran without a record") }) {
		t.Error("update without a record reported success")
	}
	in := &signalpb.AccountRecord{GivenName: "Me"}
	d.SetAccountRecord(in)
	in.GivenName = "changed after set"
	if d.AccountRecord().GetGivenName() != "Me" {
		t.Fatal("SetAccountRecord kept the caller's pointer")
	}

	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 500 {
				switch (g + i) % 4 {
				case 0:
					d.SetAccountRecord(&signalpb.AccountRecord{GivenName: "Me", ReadReceipts: i%2 == 0})
				case 1:
					d.UpdateAccountRecord(func(ar *signalpb.AccountRecord) { ar.TypingIndicators = !ar.TypingIndicators })
				case 2:
					ar := d.AccountRecord()
					_ = ar.GetReadReceipts()
					if ar != nil {
						ar.GivenName = "scribble" // a copy
					}
				case 3:
					if ar := d.AccountRecord(); ar != nil {
						if _, err := proto.Marshal(ar); err != nil {
							t.Error(err)
						}
					}
				}
			}
		}()
	}
	wg.Wait()
	if d.AccountRecord().GetGivenName() != "Me" {
		t.Error("a reader modified the stored account record")
	}
}
