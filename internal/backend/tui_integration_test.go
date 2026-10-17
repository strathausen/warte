package backend

// End-to-end check of the real receive path into the TUI: Signal events go
// through the backend's handler (no network), the backend's store and event
// queue, and into a running tui.App, whose rendered output is inspected.
// This test sits in package backend for handleSignalEvent; the TUI itself
// still only sees core.Service.

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest/v2"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-signal/pkg/signalmeow/events"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/types"

	"warte/internal/config"
	"warte/internal/core"
	"warte/internal/notify"
	"warte/internal/termimg"
	"warte/internal/tui"

	_ "warte/internal/tui/views/all"
)

type tuiNopNotifier struct{}

func (tuiNopNotifier) Notify(notify.Notification) error { return nil }

// recordingService records the media requests the TUI makes.
type recordingService struct {
	core.Service
	mu   sync.Mutex
	gets []attGet
}

type attGet struct {
	msg core.MessageID
	att string
	err error
}

func (r *recordingService) Attachment(ctx context.Context, msg core.MessageID, id string) ([]byte, error) {
	data, err := r.Service.Attachment(ctx, msg, id)
	r.mu.Lock()
	r.gets = append(r.gets, attGet{msg, id, err})
	r.mu.Unlock()
	return data, err
}

func (r *recordingService) attachmentGets() []attGet {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]attGet(nil), r.gets...)
}

// offlinePtr is an attachment pointer without a CDN location: fetching it
// fails as unavailable without touching the network.
func offlinePtr(ct string, size uint32) *signalpb.AttachmentPointer {
	return &signalpb.AttachmentPointer{ContentType: proto.String(ct), Size: proto.Uint32(size),
		Width: proto.Uint32(8), Height: proto.Uint32(8),
		Key: bytes.Repeat([]byte{7}, 64), Digest: []byte{1, 2, 3}}
}

func TestTUIOverRealReceivePath(t *testing.T) {
	b, clock := newTestBackend(t)
	ctx := context.Background()
	groupWithMembers(t, b, selfACI, aliceACI, bobACI)
	base := uint64(clock.Now().UnixMilli())

	// History before the UI starts.
	mustHandle(t, b, chatEvent(aliceUUID, aliceUUID.String(), &signalpb.DataMessage{
		Timestamp: proto.Uint64(base - 60_000), Body: proto.String("photo incoming"),
		Attachments: []*signalpb.AttachmentPointer{offlinePtr("image/png", 100)}}))
	drain(t, b)

	cfg := config.Default()
	cfg.Theme = "charm"
	cfg.Notifications = config.NotifyOff
	cfg.NotifyMethod = config.MethodOff
	none := false
	blocks := termimg.HalfBlocks
	svc := &recordingService{Service: b}
	app := tui.New(svc, cfg, tui.Options{
		Now: clock.Now, Getenv: func(string) string { return "" }, NerdFonts: &none, Images: &blocks,
		Notifier: tuiNopNotifier{}, SkipStart: true,
	})
	t.Cleanup(app.Close)
	tm := teatest.NewTestModel(t, app, teatest.WithInitialTermSize(120, 40))
	t.Cleanup(func() { _ = tm.Quit() })

	var seen bytes.Buffer
	waitOut := func(want string) {
		t.Helper()
		teatest.WaitFor(t, tm.Output(), func(chunk []byte) bool {
			seen.Write(chunk)
			return strings.Contains(ansi.Strip(seen.String()), want)
		}, teatest.WithDuration(5*time.Second), teatest.WithCheckInterval(10*time.Millisecond))
	}
	waitOut("Alice Chen")
	waitOut("photo incoming")

	// A live message updates the chat list preview.
	mustHandle(t, b, chatEvent(aliceUUID, aliceUUID.String(), textMsg(base-50_000, "live hello")))
	waitOut("live hello")

	// A reply quoting the photo, with the quote's thumbnail.
	thumb := offlinePtr("image/png", 10)
	reply := &signalpb.DataMessage{Timestamp: proto.Uint64(base - 40_000), Body: proto.String("nice reply"),
		Quote: &signalpb.DataMessage_Quote{Id: proto.Uint64(base - 60_000), AuthorAciBinary: aliceUUID[:], Text: proto.String(""),
			Attachments: []*signalpb.DataMessage_Quote_QuotedAttachment{{ContentType: proto.String("image/png"), Thumbnail: thumb}}}}
	mustHandle(t, b, chatEvent(aliceUUID, aliceUUID.String(), reply))
	replyID := core.MessageID{Chat: aliceChat, Author: aliceACI, Timestamp: base - 40_000}
	rm := mustMessage(t, b, replyID)
	if rm.Quote == nil || rm.Quote.Attachment == nil || rm.Quote.Attachment.ID == "" {
		t.Fatalf("quote %+v", rm.Quote)
	}
	waitOut("nice reply")

	// Open the chat (it is on top).
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitOut("live hello")

	// Edits and reactions arrive while the chat is open.
	mustHandle(t, b, chatEvent(aliceUUID, aliceUUID.String(), &signalpb.EditMessage{
		TargetSentTimestamp: proto.Uint64(base - 50_000), DataMessage: textMsg(base-30_000, "edited hello")}))
	waitOut("edited hello")

	// A message we sent from the phone, then Alice's read receipt.
	mustHandle(t, b, chatEvent(selfUUID, aliceUUID.String(), textMsg(base-20_000, "from my phone")))
	waitOut("from my phone")
	mustHandle(t, b, &events.Receipt{Sender: aliceUUID, Content: &signalpb.ReceiptMessage{
		Type: signalpb.ReceiptMessage_READ.Enum(), Timestamp: []uint64{base - 20_000}}})

	// Group call started by Alice: the conversation and the chat list say who
	// started it.
	gid := types.GroupIdentifier(groupChat.GroupID())
	mustHandle(t, b, &events.Call{Info: events.MessageInfo{Sender: aliceUUID, ChatID: string(gid)}, Kind: events.CallKindGroupUpdate,
		GroupID: gid, EraID: "era-tui", Timestamp: base - 10_000})
	waitOut("📞 Alice Chen s")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEscape})
	tm.Type("H")
	tm.Type("gg")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitOut("Alice Chen started a group call ·")

	// Write-back: opening the chat marked Alice's messages read in the store.
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, err := b.Chat(ctx, aliceChat)
		if err == nil && c.Unread == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("chat still unread after opening: %+v %v", c.Unread, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if m := mustMessage(t, b, core.MessageID{Chat: aliceChat, Author: selfACI, Timestamp: base - 20_000}); m.Status != core.StatusRead {
		t.Errorf("own message status %v, want read", m.Status)
	}

	// The quote thumbnail was fetched with the quoting message's ID.
	deadline = time.Now().Add(5 * time.Second)
	for {
		ok := false
		for _, g := range svc.attachmentGets() {
			if g.att == rm.Quote.Attachment.ID {
				// The pointer has no CDN location, so the fetch fails as
				// unavailable; it must not fail as an unknown attachment.
				if g.msg != replyID || errors.Is(g.err, core.ErrNotFound) {
					t.Fatalf("quote thumbnail requested as %v: %v", g.msg, g.err)
				}
				ok = true
			}
		}
		if ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("quote thumbnail never requested; requests: %+v", svc.attachmentGets())
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The history transfer stores messages without per-message events; the
	// open conversation picks them up when the stage finishes.
	if _, err := b.db.PutMessage(ctx, core.Message{ID: core.MessageID{Chat: groupChat, Author: bobACI, Timestamp: base - 90_000},
		Kind: core.KindNormal, SenderName: "Bob", Body: "imported from the phone", SentAt: msTime(base - 90_000)}); err != nil {
		t.Fatal(err)
	}
	b.emitSyncDone("history", 1)
	waitOut("imported from the phone")

	for _, g := range svc.attachmentGets() {
		if errors.Is(g.err, core.ErrNotFound) {
			t.Errorf("TUI asked for attachment %s of %v, which the backend does not know", g.att, g.msg)
		}
	}

	tm.Type(":q")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))
}

// Every sync stage the backend starts must end in a state the frontend
// shows as finished (Total > 0 and Done >= Total): Total 0 means "still
// running, size unknown".
func TestEmptySyncStagesFinish(t *testing.T) {
	b, _ := newTestBackend(t)
	mustHandle(t, b, &events.ContactList{})
	sp := eventsOf[core.SyncProgress](drain(t, b))
	if len(sp) != 1 || sp[0].Stage != "contacts" || sp[0].Total == 0 || sp[0].Done < sp[0].Total {
		t.Errorf("empty contact sync %+v", sp)
	}
	b.emitSyncDone("groups", 0)
	b.emitSyncDone("history", 3)
	sp = eventsOf[core.SyncProgress](drain(t, b))
	if len(sp) != 2 || sp[0] != (core.SyncProgress{Stage: "groups", Done: 1, Total: 1}) ||
		sp[1] != (core.SyncProgress{Stage: "history", Done: 3, Total: 3}) {
		t.Errorf("sync done %+v", sp)
	}
}
