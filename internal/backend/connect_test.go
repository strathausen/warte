package backend

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"go.mau.fi/mautrix-signal/pkg/signalmeow"

	"warte/internal/core"
)

func TestApplyStorageSnapshot(t *testing.T) {
	b, _ := newTestBackend(t)
	ctx := context.Background()
	bobChat := core.DirectChatID(bobACI)
	for _, c := range []core.Chat{
		{ID: aliceChat, Kind: core.ChatDirect, Pinned: true, PinOrder: 0},
		{ID: bobChat, Kind: core.ChatDirect, MessageRequest: true},
		{ID: core.DirectChatID(selfACI), Kind: core.ChatNoteToSelf, Title: "Note to Self"}, // as initSelf does
	} {
		if err := b.db.UpsertChat(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.db.PutStickerPack(ctx, core.StickerPack{ID: "ee", Key: "ff", Installed: true}); err != nil {
		t.Fatal(err)
	}
	until := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	snap := storageSnapshot{
		Chats: []storageChatState{
			{Chat: aliceChat, Archived: true, MutedUntil: until},
			{Chat: bobChat, Whitelisted: true, MarkedUnread: true},
			{Chat: groupChat, Blocked: true},
			{Chat: core.DirectChatID(aciOf(selfUUID)), Archived: true, Whitelisted: true},
			{Chat: core.DirectChatID("44444444-4444-4444-8444-444444444444")}, // plain contact: not materialised
		},
		Contacts:   []storageContact{{ACI: aliceACI, Username: "alice.07", Verified: true}},
		Pinned:     []core.ChatID{bobChat},
		HasAccount: true,
		Settings: accountSettings{ReadReceipts: false, TypingIndicators: true, LinkPreviews: false,
			UniversalExpireTimer: time.Hour, PhoneNumberSharing: true, DiscoverableByPhone: false, Username: "me.99"},
		StickerPack: []storagePack{{ID: "ee", Deleted: true}},
	}
	b.applyStorageSnapshot(ctx, snap)

	alice, _ := b.Chat(ctx, aliceChat)
	if !alice.Archived || alice.Pinned || !alice.MutedUntil.Equal(until) {
		t.Errorf("alice chat %+v", alice)
	}
	bob, _ := b.Chat(ctx, bobChat)
	if bob.MessageRequest || !bob.MarkedUnread || !bob.Pinned || bob.PinOrder != 0 {
		t.Errorf("bob chat %+v", bob)
	}
	group, err := b.Chat(ctx, groupChat)
	if err != nil || group.Kind != core.ChatGroup || !group.Blocked {
		t.Errorf("group chat %+v %v", group, err)
	}
	nts, _ := b.Chat(ctx, core.DirectChatID(selfACI))
	if !nts.Archived || nts.Kind != core.ChatNoteToSelf {
		t.Errorf("note to self %+v", nts)
	}
	if _, err := b.Chat(ctx, core.DirectChatID("44444444-4444-4444-8444-444444444444")); !errors.Is(err, core.ErrNotFound) {
		t.Error("contact-only records must not create chats")
	}
	if c, _ := b.Contact(ctx, aliceACI); c.Username != "alice.07" || !c.Verified {
		t.Errorf("contact %+v", c)
	}
	s, _ := b.Settings(ctx)
	if s.ReadReceipts || !s.TypingIndicators || s.LinkPreviews || s.DefaultExpireTimer != time.Hour || !s.PhoneNumberSharing || s.DiscoverableByPhone {
		t.Errorf("settings %+v", s)
	}
	if b.Self().Username != "me.99" {
		t.Errorf("self username %q", b.Self().Username)
	}
	if p, _ := b.db.GetStickerPack(ctx, "ee"); p.Installed {
		t.Error("deleted pack still installed")
	}
	evs := drain(t, b)
	if len(eventsOf[core.ChatUpdated](evs)) < 4 || len(eventsOf[core.ContactUpdated](evs)) < 1 {
		t.Errorf("events %d chat, %d contact", len(eventsOf[core.ChatUpdated](evs)), len(eventsOf[core.ContactUpdated](evs)))
	}
	// Applying the same snapshot again changes nothing.
	b.applyStorageSnapshot(ctx, snap)
	if n := len(eventsOf[core.ChatUpdated](drain(t, b))); n != 0 {
		t.Errorf("idempotent apply emitted %d chat updates", n)
	}
}

func TestLinkErrors(t *testing.T) {
	tests := []struct {
		code int
		want string
	}{
		{http.StatusConflict, "too old"},
		{http.StatusLengthRequired, "maximum number"},
		{http.StatusForbidden, "invalid or already used"},
		{http.StatusTooManyRequests, "rate-limited"},
		{http.StatusInternalServerError, "linking failed"},
	}
	for _, tt := range tests {
		err := linkError(signalmeow.DeviceLinkError{StatusCode: tt.code}, signalmeow.StateProvisioningError)
		if !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%d: %v", tt.code, err)
		}
		var dle signalmeow.DeviceLinkError
		if !errors.As(err, &dle) {
			t.Error("cause must stay inspectable")
		}
	}
	if err := linkError(nil, signalmeow.StateProvisioningDataReceived); err == nil {
		t.Error("nil error with unexpected state")
	}
}

func TestRecvProv(t *testing.T) {
	ctx := context.Background()
	ch := make(chan signalmeow.ProvisioningResponse, 1)
	if _, ok := recvProv(ctx, ch, 10*time.Millisecond); ok {
		t.Error("timeout should report !ok")
	}
	ch <- signalmeow.ProvisioningResponse{State: signalmeow.StateProvisioningURLReceived, ProvisioningURL: "sgnl://x"}
	if r, ok := recvProv(ctx, ch, 0); !ok || r.ProvisioningURL != "sgnl://x" {
		t.Error("response")
	}
	close(ch)
	if r, ok := recvProv(ctx, ch, 0); !ok || r.Err == nil {
		t.Error("closed channel should be an error")
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, ok := recvProv(cctx, make(chan signalmeow.ProvisioningResponse), 0); ok {
		t.Error("cancelled context")
	}
	if !errors.Is(ctxErr(cctx), context.Canceled) || !errors.Is(ctxErr(ctx), context.Canceled) {
		t.Error("ctxErr")
	}
}

func TestLinkerNeedsStorage(t *testing.T) {
	l := NewLinker(Options{Logger: zerolog.Nop()})
	if _, err := l.Link(context.Background(), ""); err == nil {
		t.Error("Link without storage must fail")
	}
}

func TestQueriesOnEmptyStore(t *testing.T) {
	b, _ := newTestBackend(t)
	ctx := context.Background()
	if s, err := b.Stories(ctx); err != nil || len(s) != 0 {
		t.Errorf("stories %v %v", s, err)
	}
	if p, err := b.StickerPacks(ctx); err != nil || len(p) != 0 {
		t.Errorf("packs %v %v", p, err)
	}
	if f, err := b.Folders(ctx); err != nil || len(f) != 0 {
		t.Errorf("folders %v %v", f, err)
	}
	if cs, err := b.Contacts(ctx); err != nil || len(cs) != 3 {
		t.Errorf("contacts %d %v", len(cs), err)
	}
	if hits, err := b.Search(ctx, core.SearchQuery{Text: "nothing"}); err != nil || len(hits) != 0 {
		t.Errorf("search %v %v", hits, err)
	}
	if _, err := b.Chat(ctx, aliceChat); !errors.Is(err, core.ErrNotFound) {
		t.Error("missing chat")
	}
	if b.Self().ACI != selfACI {
		t.Error("self")
	}
}
