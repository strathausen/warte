package store

import (
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"warte/internal/core"
)

func TestContactsCRUD(t *testing.T) {
	env := newTestDB(t)
	ctx := ctxT(t)
	contacts := []core.Contact{
		{ACI: "c", ProfileName: "carol", E164: "+3", Username: "Carol.03", About: "hi", AboutEmoji: "👋", AvatarID: "av", Verified: true},
		{ACI: "a", ContactName: "Alice", E164: "+1"},
		{ACI: "me", ProfileName: "Me", IsSelf: true},
		{ACI: "b", Nickname: "bob", Blocked: true},
	}
	for _, c := range contacts {
		if err := env.db.UpsertContact(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	got, err := env.db.GetContact(ctx, "c")
	if err != nil || !reflect.DeepEqual(got, contacts[0]) {
		t.Fatalf("GetContact = %+v, %v", got, err)
	}
	list, _ := env.db.ListContacts(ctx)
	var names []string
	for _, c := range list {
		names = append(names, c.DisplayName())
	}
	if !slices.Equal(names, []string{"Alice", "bob", "carol", "Me"}) {
		t.Errorf("order = %v", names)
	}
	if c, err := env.db.ContactByE164(ctx, "+1"); err != nil || c.ACI != "a" {
		t.Errorf("ContactByE164 = %v, %v", c.ACI, err)
	}
	if c, err := env.db.ContactByUsername(ctx, "carol.03"); err != nil || c.ACI != "c" {
		t.Errorf("ContactByUsername = %v, %v", c.ACI, err)
	}
	if c, err := env.db.SelfContact(ctx); err != nil || c.ACI != "me" {
		t.Errorf("SelfContact = %v, %v", c.ACI, err)
	}
	for _, fn := range []func() error{
		func() error { _, err := env.db.GetContact(ctx, "zz"); return err },
		func() error { _, err := env.db.ContactByE164(ctx, ""); return err },
		func() error { _, err := env.db.ContactByUsername(ctx, "nobody.1"); return err },
		func() error { return env.db.DeleteContact(ctx, "zz") },
	} {
		if err := fn(); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
	}
	if err := env.db.DeleteContact(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if err := env.db.UpsertContact(ctx, core.Contact{}); err == nil {
		t.Error("contact without ACI should fail")
	}
}

func TestStories(t *testing.T) {
	env := newTestDB(t)
	ctx := ctxT(t)
	s1 := core.Story{ID: core.MessageID{Chat: "u:a", Author: "a", Timestamp: 1000}, AuthorName: "A", At: t0,
		Text: &core.TextStory{Text: "hi", Style: 1, Foreground: 0xffffffff, Background: 0xff000000, Gradient: []uint32{1, 2}, URL: "u"}}
	s2 := core.Story{ID: core.MessageID{Chat: "g:x", Author: "b", Timestamp: 2000}, AuthorName: "B", At: t0.Add(time.Hour),
		Media: &core.Attachment{ID: "m", ContentType: "image/jpeg"}, Caption: "cap", Group: "g:x"}
	old := core.Story{ID: core.MessageID{Chat: "u:c", Author: "c", Timestamp: 3}, At: t0.Add(-48 * time.Hour)}
	for _, s := range []core.Story{s2, s1, old} {
		if err := env.db.PutStory(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	// Two replies to s1 (a reply and an emoji reaction).
	r1 := msg("u:a", "me", 5000, "nice")
	r1.Kind, r1.StoryContext = core.KindStoryReply, &core.StoryRef{Author: "a", Timestamp: 1000}
	r2 := msg("u:a", "me", 6000, "")
	r2.Kind, r2.StoryContext = core.KindStoryReply, &core.StoryRef{Author: "a", Timestamp: 1000, Emoji: "🔥"}
	for _, m := range []core.Message{r1, r2} {
		if _, err := env.db.PutMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	list, err := env.db.ListStories(ctx, t0.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	want1 := s1
	want1.Replies = 2
	if len(list) != 2 || !reflect.DeepEqual(list[0], want1) || !reflect.DeepEqual(list[1], s2) {
		t.Fatalf("ListStories = %+v", list)
	}
	if err := env.db.MarkStoryViewed(ctx, s2.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := env.db.GetStory(ctx, s2.ID); !got.Viewed {
		t.Error("story not viewed")
	}
	n, err := env.db.DeleteStoriesBefore(ctx, t0.Add(-24*time.Hour))
	if err != nil || n != 1 {
		t.Errorf("DeleteStoriesBefore = %d, %v", n, err)
	}
	if err := env.db.DeleteStory(ctx, s1.ID); err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{
		env.db.DeleteStory(ctx, s1.ID),
		env.db.MarkStoryViewed(ctx, s1.ID),
		func() error { _, err := env.db.GetStory(ctx, s1.ID); return err }(),
	} {
		if !errors.Is(err, core.ErrNotFound) {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
	}
	// Reply preview text for an emoji story reaction.
	if c, _ := env.db.GetChat(ctx, "u:a"); c.LastMessage == nil || c.LastMessage.Text != "Reacted 🔥 to a story" {
		t.Errorf("story reaction preview = %+v", c.LastMessage)
	}
}

func TestCallLog(t *testing.T) {
	env := newTestDB(t)
	ctx := ctxT(t)
	e1 := core.CallLogEntry{Chat: "u:a", Title: "A", Call: core.CallInfo{Video: true, Incoming: true, Missed: true, StartedAt: t0}, Unread: true}
	e2 := core.CallLogEntry{Chat: "g:x", Title: "X", Call: core.CallInfo{Group: true, Duration: time.Minute, StartedAt: t0.Add(time.Hour)}}
	for _, e := range []core.CallLogEntry{e1, e2} {
		if err := env.db.PutCall(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	// Upsert by (chat, started) key.
	e1.Title = "Alice"
	if err := env.db.PutCall(ctx, e1); err != nil {
		t.Fatal(err)
	}
	list, _ := env.db.ListCalls(ctx, 0)
	if !reflect.DeepEqual(list, []core.CallLogEntry{e2, e1}) {
		t.Fatalf("ListCalls = %+v", list)
	}
	if list, _ := env.db.ListCalls(ctx, 1); len(list) != 1 {
		t.Errorf("limit ignored: %d", len(list))
	}
	if n, _ := env.db.UnreadCalls(ctx); n != 1 {
		t.Errorf("UnreadCalls = %d", n)
	}
	if err := env.db.MarkCallsRead(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := env.db.UnreadCalls(ctx); n != 0 {
		t.Errorf("UnreadCalls after read = %d", n)
	}
	if err := env.db.DeleteCall(ctx, "u:a", t0); err != nil {
		t.Fatal(err)
	}
	if err := env.db.DeleteCall(ctx, "u:a", t0); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("err = %v", err)
	}
	if err := env.db.ClearCalls(ctx); err != nil {
		t.Fatal(err)
	}
	if list, _ := env.db.ListCalls(ctx, 0); len(list) != 0 {
		t.Errorf("after clear: %v", list)
	}
	if err := env.db.PutCall(ctx, core.CallLogEntry{Chat: "u:a"}); err == nil {
		t.Error("entry without StartedAt should fail")
	}
}

func TestStickerPacks(t *testing.T) {
	env := newTestDB(t)
	ctx := ctxT(t)
	st := core.Sticker{PackID: "p1", PackKey: "k1", StickerID: 3, Emoji: "😺", Attachment: core.Attachment{ID: "s3", ContentType: "image/webp"}}
	packs := []core.StickerPack{
		{ID: "p1", Key: "k1", Title: "Cats", Author: "x", Installed: true, Cover: &st, Stickers: []core.Sticker{st}},
		{ID: "p2", Key: "k2", Title: "Bees"},
		{ID: "p3", Key: "k3", Title: "Ants", Installed: true},
	}
	for _, p := range packs {
		if err := env.db.PutStickerPack(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	got, err := env.db.GetStickerPack(ctx, "p1")
	if err != nil || !reflect.DeepEqual(got, packs[0]) {
		t.Fatalf("GetStickerPack = %+v, %v", got, err)
	}
	ids := func(installedOnly bool) []string {
		ps, err := env.db.ListStickerPacks(ctx, installedOnly)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, p := range ps {
			out = append(out, p.ID)
		}
		return out
	}
	if got := ids(false); !slices.Equal(got, []string{"p1", "p3", "p2"}) {
		t.Errorf("all = %v", got)
	}
	if err := env.db.SetStickerPackInstalled(ctx, "p2", true); err != nil {
		t.Fatal(err)
	}
	// Re-putting an installed pack keeps its position.
	if err := env.db.PutStickerPack(ctx, packs[0]); err != nil {
		t.Fatal(err)
	}
	if got := ids(true); !slices.Equal(got, []string{"p1", "p3", "p2"}) {
		t.Errorf("installed = %v", got)
	}
	if err := env.db.SetStickerPackInstalled(ctx, "p1", false); err != nil {
		t.Fatal(err)
	}
	if got := ids(true); !slices.Equal(got, []string{"p3", "p2"}) {
		t.Errorf("after uninstall = %v", got)
	}
	if p, _ := env.db.GetStickerPack(ctx, "p1"); p.Installed {
		t.Error("p1 still installed")
	}
	if err := env.db.DeleteStickerPack(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	if err := env.db.SetStickerPackInstalled(ctx, "p1", true); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("err = %v", err)
	}
}

func TestFolders(t *testing.T) {
	env := newTestDB(t)
	ctx := ctxT(t)
	fs := []core.Folder{
		{ID: "unread", Name: "Unread", Filter: core.ChatFilter{UnreadOnly: true}},
		{ID: "groups", Name: "Groups", Filter: core.ChatFilter{GroupsOnly: true}},
		{ID: "work", Name: "Work", Filter: core.ChatFilter{Query: "acme", IncludeEmpty: true}},
	}
	for _, f := range fs {
		if err := env.db.PutFolder(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := env.db.ListFolders(ctx)
	if !reflect.DeepEqual(got, fs) {
		t.Fatalf("ListFolders = %+v", got)
	}
	fs[0].Name = "New"
	if err := env.db.PutFolder(ctx, fs[0]); err != nil {
		t.Fatal(err)
	}
	if err := env.db.ReorderFolders(ctx, []string{"work", "unread"}); err != nil {
		t.Fatal(err)
	}
	got, _ = env.db.ListFolders(ctx)
	if want := []core.Folder{fs[2], fs[0], fs[1]}; !reflect.DeepEqual(got, want) {
		t.Errorf("after reorder = %+v", got)
	}
	if err := env.db.ReorderFolders(ctx, []string{"nope"}); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("err = %v", err)
	}
	if err := env.db.DeleteFolder(ctx, "work"); err != nil {
		t.Fatal(err)
	}
	if _, err := env.db.GetFolder(ctx, "work"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("err = %v", err)
	}
	if err := env.db.PutFolder(ctx, core.Folder{ID: "z"}); err != nil {
		t.Fatal(err)
	}
	got, _ = env.db.ListFolders(ctx)
	if got[len(got)-1].ID != "z" {
		t.Errorf("new folder not last: %+v", got)
	}
}

func TestSettings(t *testing.T) {
	env := newTestDB(t)
	ctx := ctxT(t)
	s, err := env.db.Settings(ctx)
	if err != nil || !reflect.DeepEqual(s, DefaultSettings()) {
		t.Fatalf("defaults = %+v, %v", s, err)
	}
	s.ReadReceipts = false
	s.DefaultExpireTimer = time.Hour
	s.NotificationLevel = core.NotifyNone
	if err := env.db.SaveSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	if got, _ := env.db.Settings(ctx); !reflect.DeepEqual(got, s) {
		t.Errorf("Settings = %+v, want %+v", got, s)
	}

	type ui struct {
		Theme string
		Width int
	}
	if err := env.db.SetSetting(ctx, "ui", ui{"charm", 30}); err != nil {
		t.Fatal(err)
	}
	var u ui
	if err := env.db.GetSetting(ctx, "ui", &u); err != nil || u != (ui{"charm", 30}) {
		t.Errorf("GetSetting = %+v, %v", u, err)
	}
	if err := env.db.DeleteSetting(ctx, "ui"); err != nil {
		t.Fatal(err)
	}
	if err := env.db.GetSetting(ctx, "ui", &u); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("err = %v", err)
	}
	if err := env.db.SetSetting(ctx, "bad", func() {}); err == nil {
		t.Error("unencodable value should fail")
	}
}

func TestAttachmentsAndPrune(t *testing.T) {
	env := newTestDB(t)
	ctx := ctxT(t)
	blobs, err := OpenBlobs(t.TempDir(), randKey(t))
	if err != nil {
		t.Fatal(err)
	}
	m1 := msg("u:a", "a", 1000, "")
	m2 := msg("u:a", "a", 2000, "")
	for _, m := range []core.Message{m1, m2} {
		if _, err := env.db.PutMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	shared, _ := blobs.Put([]byte("same bytes"))
	only, _ := blobs.Put([]byte("only m1"))
	avatar, _ := blobs.Put([]byte("avatar"))
	recs := []AttachmentRecord{
		{ID: "a1", Owner: MessageOwner(m1.ID), BlobKey: shared, ContentType: "image/png", FileName: "x.png", Size: 10,
			Digest: []byte{1, 2}, Downloaded: true, Remote: []byte(`{"cdn":2}`), CreatedAt: t0},
		{ID: "a2", Owner: MessageOwner(m1.ID), BlobKey: only, Downloaded: true, CreatedAt: t0.Add(time.Second)},
		{ID: "a3", Owner: MessageOwner(m2.ID), BlobKey: shared, Downloaded: true, CreatedAt: t0},
		{ID: "av", Owner: "avatar:a", BlobKey: avatar, CreatedAt: t0},
		{ID: "pending", Owner: MessageOwner(m1.ID), CreatedAt: t0.Add(2 * time.Second)},
	}
	for _, r := range recs {
		if err := env.db.PutAttachment(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	got, err := env.db.GetAttachment(ctx, "a1")
	if err != nil || !reflect.DeepEqual(got, recs[0]) {
		t.Fatalf("GetAttachment = %+v, %v", got, err)
	}
	list, _ := env.db.ListAttachments(ctx, MessageOwner(m1.ID))
	if len(list) != 3 || list[0].ID != "a1" || list[2].ID != "pending" {
		t.Fatalf("ListAttachments = %+v", list)
	}
	later, _ := blobs.Put([]byte("downloaded later"))
	if err := env.db.SetAttachmentBlob(ctx, "pending", later); err != nil {
		t.Fatal(err)
	}
	if a, _ := env.db.GetAttachment(ctx, "pending"); !a.Downloaded || a.BlobKey != later {
		t.Errorf("SetAttachmentBlob: %+v", a)
	}
	if err := env.db.SetAttachmentBlob(ctx, "nope", later); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("err = %v", err)
	}
	if err := env.db.SetAttachmentBlob(ctx, "a1", "../../etc/passwd"); err == nil {
		t.Error("invalid blob key accepted")
	}

	// Nothing to prune while messages exist.
	if n, err := env.db.PruneAttachments(ctx, blobs); err != nil || n != 0 {
		t.Fatalf("prune = %d, %v", n, err)
	}
	if err := env.db.DeleteMessage(ctx, m1.ID); err != nil {
		t.Fatal(err)
	}
	n, err := env.db.PruneAttachments(ctx, blobs)
	if err != nil || n != 3 {
		t.Fatalf("prune = %d, %v", n, err)
	}
	if !blobs.Has(shared) {
		t.Error("shared blob deleted while still referenced by m2")
	}
	if blobs.Has(only) || blobs.Has(later) {
		t.Error("unreferenced blobs not deleted")
	}
	if !blobs.Has(avatar) {
		t.Error("non-message attachment pruned")
	}

	// Garbage collection removes unreferenced blobs past the grace period.
	orphan, _ := blobs.Put([]byte("orphan"))
	env.clock.Set(time.Now()) // file mtimes use the real clock
	if n, _ := env.db.CollectGarbage(ctx, blobs, time.Hour); n != 0 {
		t.Errorf("GC within grace period deleted %d", n)
	}
	env.clock.Set(time.Now().Add(2 * time.Hour))
	if n, err := env.db.CollectGarbage(ctx, blobs, time.Hour); err != nil || n != 1 {
		t.Errorf("GC = %d, %v", n, err)
	}
	if blobs.Has(orphan) || !blobs.Has(shared) || !blobs.Has(avatar) {
		t.Error("GC removed the wrong blobs")
	}
	if err := env.db.DeleteAttachment(ctx, "av"); err != nil {
		t.Fatal(err)
	}
	if err := env.db.DeleteAttachment(ctx, "av"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("err = %v", err)
	}
}
