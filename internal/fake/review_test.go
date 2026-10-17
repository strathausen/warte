package fake

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	"warte/internal/core"
)

func TestOggPagesHaveValidCRC(t *testing.T) {
	if got := oggCRC([]byte("123456789")); got != 0x89a1897f {
		t.Fatalf("oggCRC check value = %08x, want 89a1897f", got)
	}
	data := voiceNoteOgg(3)
	pages := 0
	for off := 0; off < len(data); pages++ {
		if len(data)-off < 27 || string(data[off:off+4]) != "OggS" {
			t.Fatalf("page %d at %d: bad capture pattern", pages, off)
		}
		nseg := int(data[off+26])
		size := 27 + nseg
		for _, l := range data[off+27 : off+27+nseg] {
			size += int(l)
		}
		page := append([]byte(nil), data[off:off+size]...)
		want := binary.LittleEndian.Uint32(page[22:26])
		clear(page[22:26])
		if got := oggCRC(page); got != want {
			t.Fatalf("page %d: crc %08x, stored %08x", pages, got, want)
		}
		if pages == 0 && string(page[28:36]) != "OpusHead" {
			t.Fatalf("first page lacks OpusHead: %q", page[28:36])
		}
		off += size
	}
	if pages < 3 {
		t.Fatalf("only %d pages", pages)
	}
}

func TestSanitizeRanges(t *testing.T) {
	body := "hi ￼ 👋 there" // h i _ FFFC _ 👋(2) _ t h e r e = 13 units
	n := utf16Len(body)
	if n != 13 {
		t.Fatalf("utf16Len = %d", n)
	}
	in := []core.BodyRange{
		{Start: 5, Length: 2, Style: core.StyleBold},                         // the emoji: ok
		{Start: 3, Length: 1, Style: core.StyleMention, MentionACI: "x"},     // ok
		{Start: 0, Length: 1, Style: core.StyleMention, MentionACI: "x"},     // not on U+FFFC
		{Start: 3, Length: 1, Style: core.StyleMention},                      // no ACI
		{Start: -1, Length: 2, Style: core.StyleItalic},                      // negative
		{Start: 12, Length: 2, Style: core.StyleItalic},                      // past end
		{Start: 2, Length: 0, Style: core.StyleItalic},                       // empty
		{Start: 0, Length: 2, Style: core.RangeStyle(99)},                    // unknown style
		{Start: 0, Length: 13, Style: core.StyleSpoiler, MentionACI: "junk"}, // ok, ACI cleared
		{Start: 1 << 40, Length: 1 << 40, Style: core.StyleBold},             // overflow-ish
	}
	got := sanitizeRanges(body, in)
	want := []core.BodyRange{
		{Start: 0, Length: 13, Style: core.StyleSpoiler},
		{Start: 3, Length: 1, Style: core.StyleMention, MentionACI: "x"},
		{Start: 5, Length: 2, Style: core.StyleBold},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("range %d: got %+v want %+v", i, got[i], want[i])
		}
	}
	if sanitizeRanges("", []core.BodyRange{{Start: 0, Length: 1, Style: core.StyleBold}}) != nil {
		t.Error("range on empty body kept")
	}
}

func TestInlineMentions(t *testing.T) {
	s, _ := newTestService(t, Options{Seed: 1})
	s.mu.RLock()
	alice := s.nameLocked(AliceACI)
	// "**hey ￼ 🙂** ~~x~~" with the bold covering the mention.
	body := "hey ￼ 🙂 x"
	ranges := []core.BodyRange{
		{Start: 0, Length: 8, Style: core.StyleBold},
		{Start: 4, Length: 1, Style: core.StyleMention, MentionACI: AliceACI},
		{Start: 9, Length: 1, Style: core.StyleStrikethrough},
	}
	gotBody, gotRanges := s.inlineMentionsLocked(body, ranges)
	s.mu.RUnlock()
	if want := "hey @" + alice + " 🙂 x"; gotBody != want {
		t.Fatalf("body %q, want %q", gotBody, want)
	}
	units := utf16.Encode([]rune(gotBody))
	delta := utf16Len("@"+alice) - 1
	want := []core.BodyRange{
		{Start: 0, Length: 8 + delta, Style: core.StyleBold},
		{Start: 9 + delta, Length: 1, Style: core.StyleStrikethrough},
	}
	if len(gotRanges) != len(want) {
		t.Fatalf("ranges %+v", gotRanges)
	}
	for i, r := range gotRanges {
		if r != want[i] {
			t.Errorf("range %d = %+v, want %+v", i, r, want[i])
		}
		if r.Start+r.Length > len(units) {
			t.Errorf("range %d out of bounds", i)
		}
	}
	if string(utf16.Decode(units[want[1].Start:want[1].Start+1])) != "x" {
		t.Error("strikethrough no longer covers x")
	}
}

func TestForwardInlinesMentions(t *testing.T) {
	s, _ := newTestService(t, Options{Seed: 1})
	ctx := context.Background()
	src := findMsg(t, s, ChatHike, func(m core.Message) bool {
		for _, r := range m.Ranges {
			if r.Style == core.StyleMention {
				return !m.ViewOnce && m.Poll == nil && !m.RemoteDeleted
			}
		}
		return false
	})
	if err := s.Forward(ctx, src.ID, []core.ChatID{ChatAlice}); err != nil {
		t.Fatal(err)
	}
	fwd := lastOf(t, s, ChatAlice)
	if strings.ContainsRune(fwd.Body, '￼') {
		t.Fatalf("forwarded body still has a placeholder: %q", fwd.Body)
	}
	if !strings.Contains(fwd.Body, "@") {
		t.Errorf("mention not inlined: %q", fwd.Body)
	}
	n := utf16Len(fwd.Body)
	for _, r := range fwd.Ranges {
		if r.Style == core.StyleMention || r.Style == core.StyleNone {
			t.Errorf("forwarded range %+v", r)
		}
		if r.Start < 0 || r.Start+r.Length > n {
			t.Errorf("forwarded range %+v out of bounds (%d)", r, n)
		}
	}
}

func TestSendAndEditSanitizeRanges(t *testing.T) {
	s, _ := newTestService(t, Options{Seed: 1})
	ctx := context.Background()
	id, err := s.Send(ctx, ChatAlice, core.Outgoing{Body: "ok", Ranges: []core.BodyRange{
		{Start: 0, Length: 2, Style: core.StyleBold},
		{Start: 1, Length: 50, Style: core.StyleItalic},
	}})
	if err != nil {
		t.Fatal(err)
	}
	m, _ := s.Message(ctx, id)
	if len(m.Ranges) != 1 || m.Ranges[0].Style != core.StyleBold {
		t.Fatalf("ranges not sanitized: %+v", m.Ranges)
	}
	if err := s.Edit(ctx, id, "ok!", []core.BodyRange{{Start: 2, Length: 9, Style: core.StyleBold}}); err != nil {
		t.Fatal(err)
	}
	if m, _ = s.Message(ctx, id); len(m.Ranges) != 0 || m.Body != "ok!" {
		t.Fatalf("edit ranges not sanitized: %+v", m)
	}
	_, err = s.Send(ctx, ChatAlice, core.Outgoing{Body: "hi ￼", Ranges: []core.BodyRange{
		{Start: 3, Length: 1, Style: core.StyleMention, MentionACI: "no-such-aci"},
	}})
	if !errors.Is(err, core.ErrNotFound) {
		t.Errorf("mention of unknown contact: %v", err)
	}
}

func TestFailedUpdatesChangeNothing(t *testing.T) {
	s, _ := newTestService(t, Options{Seed: 1})
	ctx := context.Background()
	id, err := s.CreateGroup(ctx, "Atomic", []core.ACI{AliceACI}, nil)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := s.Chat(ctx, id)
	err = s.UpdateGroup(ctx, id, core.GroupUpdate{AddMembers: []core.ACI{BobACI}, RemoveMembers: []core.ACI{SelfACI}})
	if err == nil {
		t.Fatal("removing self via UpdateGroup accepted")
	}
	neg := -time.Second
	title := "Changed"
	if err := s.UpdateGroup(ctx, id, core.GroupUpdate{Title: &title, ExpireTimer: &neg}); err == nil {
		t.Fatal("negative timer accepted")
	}
	if err := s.UpdateGroup(ctx, id, core.GroupUpdate{Title: &title, ResetLink: true}); !errors.Is(err, core.ErrReadOnly) {
		t.Fatalf("reset of disabled link: %v", err)
	}
	after, _ := s.Chat(ctx, id)
	if after.Title != before.Title || len(after.Members) != len(before.Members) || after.GroupRevision != before.GroupRevision {
		t.Fatalf("failed update mutated group: before %+v after %+v", before, after)
	}

	yes := true
	if err := s.UpdateChat(ctx, ChatAlice, core.ChatUpdate{Archived: &yes, ExpireTimer: &neg}); err == nil {
		t.Fatal("negative chat timer accepted")
	}
	if c, _ := s.Chat(ctx, ChatAlice); c.Archived {
		t.Fatal("failed UpdateChat archived the chat")
	}
}

func TestJoinByLinkWithAdminApproval(t *testing.T) {
	s, _ := newTestService(t, Options{Seed: 1})
	ctx := context.Background()
	id, err := s.CreateGroup(ctx, "Approval", []core.ACI{AliceACI}, nil)
	if err != nil {
		t.Fatal(err)
	}
	mode := core.AccessAdmin
	if err := s.UpdateGroup(ctx, id, core.GroupUpdate{LinkMode: &mode}); err != nil {
		t.Fatal(err)
	}
	c, _ := s.Chat(ctx, id)
	if err := s.LeaveGroup(ctx, id); err != nil {
		t.Fatal(err)
	}
	got, err := s.JoinGroupByLink(ctx, c.InviteLink)
	if err != nil || got != id {
		t.Fatalf("join: %s %v", got, err)
	}
	c, _ = s.Chat(ctx, id)
	if memberIndex(c.RequestingJoin, SelfACI) < 0 || memberIndex(c.Members, SelfACI) >= 0 {
		t.Fatalf("expected a pending join request: members %+v requesting %+v", c.Members, c.RequestingJoin)
	}
	if _, err := s.Send(ctx, id, core.Outgoing{Body: "let me in"}); !errors.Is(err, core.ErrReadOnly) {
		t.Errorf("send while awaiting approval: %v", err)
	}
	// Asking again is idempotent.
	if _, err := s.JoinGroupByLink(ctx, c.InviteLink); err != nil {
		t.Fatal(err)
	}
	if c2, _ := s.Chat(ctx, id); len(c2.RequestingJoin) != len(c.RequestingJoin) {
		t.Errorf("duplicate join request: %+v", c2.RequestingJoin)
	}
}

func TestUnlinkStopsLiveActivity(t *testing.T) {
	s, _ := newTestService(t, Options{
		Seed: 3, Live: true, Clock: time.Now,
		LiveMinInterval: time.Millisecond, LiveMaxInterval: 2 * time.Millisecond,
		TypingDelay: -1, CallRing: -1,
	})
	waitEvent(t, s, 5*time.Second, func(ev core.Event) bool {
		_, ok := ev.(core.MessageAdded)
		return ok
	})
	if err := s.Unlink(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitEvent(t, s, 5*time.Second, func(ev core.Event) bool {
		_, ok := ev.(core.LoggedOut)
		return ok
	})
	deadline := time.After(150 * time.Millisecond)
	for {
		select {
		case ev := <-s.Events():
			switch ev.(type) {
			case core.MessageAdded, core.MessageUpdated, core.ChatUpdated:
				t.Fatalf("activity after unlink: %T", ev)
			}
		case <-deadline:
			return
		}
	}
}

// TestEventOrderMatchesMutations sends concurrently and checks that the chat
// snapshots in ChatUpdated events never go back in time.
func TestEventOrderMatchesMutations(t *testing.T) {
	s, _ := newTestService(t, Options{Seed: 1})
	ctx := context.Background()
	drain(s)
	const n = 40
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Send(ctx, ChatAlice, core.Outgoing{Body: strings.Repeat("x", i+1)}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var last uint64
	added := 0
	deadline := time.After(5 * time.Second)
	for added < n {
		select {
		case ev := <-s.Events():
			switch ev := ev.(type) {
			case core.MessageAdded:
				if ev.Message.ID.Chat == ChatAlice {
					added++
				}
			case core.ChatUpdated:
				if ev.Chat.ID != ChatAlice || ev.Chat.LastMessage == nil {
					continue
				}
				if ts := ev.Chat.LastMessage.ID.Timestamp; ts < last {
					t.Fatalf("ChatUpdated went back in time: %d after %d", ts, last)
				} else {
					last = ts
				}
			}
		case <-deadline:
			t.Fatalf("saw only %d of %d MessageAdded events", added, n)
		}
	}
}

func TestSearchNormalizesQueryWhitespace(t *testing.T) {
	s, _ := newTestService(t, Options{Seed: 1})
	ctx := context.Background()
	if _, err := s.Send(ctx, ChatAlice, core.Outgoing{Body: "two\nlines here"}); err != nil {
		t.Fatal(err)
	}
	hits, err := s.Search(ctx, core.SearchQuery{Text: "two\nlines", Chat: ChatAlice})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || len(hits[0].Matches) != 1 {
		t.Fatalf("hits: %+v", hits)
	}
	h := hits[0]
	if got := h.Snippet[h.Matches[0][0]:h.Matches[0][1]]; got != "two lines" {
		t.Errorf("match %q", got)
	}
}

func TestExportBackupTightensExistingFile(t *testing.T) {
	s, _ := newTestService(t, Options{Seed: 1})
	dir := t.TempDir()
	path := filepath.Join(dir, "backup.json")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.ExportBackup(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 || fi.Size() < 1000 {
		t.Errorf("backup mode %v size %d", fi.Mode().Perm(), fi.Size())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("temporary files left behind: %v", entries)
	}
	if err := s.ExportBackup(context.Background(), filepath.Join(dir, "missing", "x.json")); err == nil {
		t.Error("export into a missing directory succeeded")
	}
}
