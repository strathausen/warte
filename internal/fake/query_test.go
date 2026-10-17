package fake

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"warte/internal/core"
)

func chatIDs(cs []core.Chat) []core.ChatID {
	out := make([]core.ChatID, len(cs))
	for i, c := range cs {
		out[i] = c.ID
	}
	return out
}

func containsChat(cs []core.Chat, id core.ChatID) bool {
	for _, c := range cs {
		if c.ID == id {
			return true
		}
	}
	return false
}

func TestChatsSorting(t *testing.T) {
	s, _ := newTestService(t, Options{Seed: 3})
	chats, err := s.Chats(context.Background(), core.ChatFilter{})
	if err != nil {
		t.Fatal(err)
	}
	ids := chatIDs(chats)
	if want := []core.ChatID{ChatAlice, ChatHike, ChatNoteToSelf}; !reflect.DeepEqual(ids[:3], want) {
		t.Fatalf("pinned order = %v, want %v", ids[:3], want)
	}
	seenUnpinned := false
	var prev time.Time
	for _, c := range chats {
		if c.Pinned {
			if seenUnpinned {
				t.Fatalf("pinned chat %s after unpinned", c.Title)
			}
			continue
		}
		if c.LastMessage == nil {
			t.Fatalf("%s has no last message but IncludeEmpty is false", c.Title)
		}
		if seenUnpinned && c.LastMessage.At.After(prev) {
			t.Fatalf("%s out of order: %v after %v", c.Title, c.LastMessage.At, prev)
		}
		seenUnpinned, prev = true, c.LastMessage.At
	}
}

func TestChatsFilters(t *testing.T) {
	s, _ := newTestService(t, Options{Seed: 3})
	ctx := context.Background()
	tests := []struct {
		name    string
		f       core.ChatFilter
		want    []core.ChatID
		notWant []core.ChatID
		check   func(core.Chat) bool
	}{
		{name: "default hides archived and blocked", f: core.ChatFilter{},
			want: []core.ChatID{ChatAlice, ChatInes}, notWant: []core.ChatID{ChatMarco, ChatMax}},
		{name: "archived only", f: core.ChatFilter{Archived: true},
			want: []core.ChatID{ChatMarco}, notWant: []core.ChatID{ChatAlice},
			check: func(c core.Chat) bool { return c.Archived }},
		{name: "include blocked", f: core.ChatFilter{IncludeBlocked: true}, want: []core.ChatID{ChatMax}},
		{name: "unread", f: core.ChatFilter{UnreadOnly: true},
			want: []core.ChatID{ChatHike, ChatAlice}, notWant: []core.ChatID{ChatJonas, ChatNoteToSelf},
			check: func(c core.Chat) bool { return c.Unread > 0 || c.MarkedUnread }},
		{name: "groups", f: core.ChatFilter{GroupsOnly: true},
			want: []core.ChatID{ChatHike, ChatWork, ChatTrailAlerts}, notWant: []core.ChatID{ChatAlice},
			check: func(c core.Chat) bool { return c.Kind == core.ChatGroup }},
		{name: "direct", f: core.ChatFilter{DirectOnly: true},
			want: []core.ChatID{ChatAlice, ChatNoteToSelf}, notWant: []core.ChatID{ChatHike},
			check: func(c core.Chat) bool { return c.Kind != core.ChatGroup }},
		{name: "fuzzy query", f: core.ChatFilter{Query: "wwand"}, want: []core.ChatID{ChatHike}, notWant: []core.ChatID{ChatAlice}},
		{name: "query case-insensitive", f: core.ChatFilter{Query: "ALICE"}, want: []core.ChatID{ChatAlice}},
		{name: "folder by members", f: core.ChatFilter{FolderID: FolderWork},
			want: []core.ChatID{ChatWork, ChatDev}, notWant: []core.ChatID{ChatAlice, ChatHike}},
		{name: "folder by filter", f: core.ChatFilter{FolderID: FolderGroups},
			want: []core.ChatID{ChatHike, ChatBookClub}, notWant: []core.ChatID{ChatAlice}},
		{name: "folder unread", f: core.ChatFilter{FolderID: FolderUnread}, notWant: []core.ChatID{ChatJonas}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.Chats(ctx, tt.f)
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range tt.want {
				if !containsChat(got, id) {
					t.Errorf("missing %s in %v", id, chatIDs(got))
				}
			}
			for _, id := range tt.notWant {
				if containsChat(got, id) {
					t.Errorf("unexpected %s", id)
				}
			}
			if tt.check != nil {
				for _, c := range got {
					if !tt.check(c) {
						t.Errorf("%s fails filter", c.Title)
					}
				}
			}
		})
	}
	if _, err := s.Chats(ctx, core.ChatFilter{FolderID: "nope"}); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("unknown folder: %v", err)
	}
	// Folder filters straight from Folders() work.
	folders, _ := s.Folders(ctx)
	for _, f := range folders {
		if _, err := s.Chats(ctx, f.Filter); err != nil {
			t.Errorf("folder %s: %v", f.Name, err)
		}
	}
}

func TestChatsIncludeEmpty(t *testing.T) {
	s, _ := newTestService(t, Options{Seed: 3})
	ctx := context.Background()
	id, err := s.ResolveRecipient(ctx, "+4917099998888")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.Chats(ctx, core.ChatFilter{})
	if containsChat(got, id) {
		t.Error("empty chat listed without IncludeEmpty")
	}
	got, _ = s.Chats(ctx, core.ChatFilter{IncludeEmpty: true})
	if !containsChat(got, id) {
		t.Error("empty chat missing with IncludeEmpty")
	}
	if err := s.SaveDraft(ctx, id, "hi there"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Chats(ctx, core.ChatFilter{})
	if !containsChat(got, id) {
		t.Error("chat with draft should be listed")
	}
}

func TestMessagesPagination(t *testing.T) {
	s, _ := newTestService(t, Options{Seed: 3})
	ctx := context.Background()
	all := allMessages(t, s, ChatHike)
	if len(all) < 40 {
		t.Fatalf("hike chat too small: %d", len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i].ID.Timestamp <= all[i-1].ID.Timestamp {
			t.Fatal("messages not strictly ordered oldest → newest")
		}
	}
	n := len(all)
	tests := []struct {
		name   string
		before core.MessageID
		limit  int
		want   []core.Message
	}{
		{"newest page", core.MessageID{}, 10, all[n-10:]},
		{"default limit", core.MessageID{}, 0, all[max(0, n-defaultMessagesLimit):]},
		{"before middle", all[20].ID, 5, all[15:20]},
		{"before near start", all[3].ID, 10, all[0:3]},
		{"before first", all[0].ID, 10, []core.Message{}},
		{"limit larger than chat", core.MessageID{}, 10_000, all},
		{"before unknown timestamp", core.MessageID{Chat: ChatHike, Timestamp: all[10].ID.Timestamp + 1}, 3, all[8:11]},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.Messages(ctx, ChatHike, tt.before, tt.limit)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) && !(len(got) == 0 && len(tt.want) == 0) {
				t.Fatalf("got %d messages, want %d", len(got), len(tt.want))
			}
		})
	}
	// Walking pages backwards visits every message exactly once.
	var walked []core.Message
	before := core.MessageID{}
	for {
		page, _ := s.Messages(ctx, ChatHike, before, 7)
		if len(page) == 0 {
			break
		}
		walked = append(page, walked...)
		before = page[0].ID
	}
	if !reflect.DeepEqual(walked, all) {
		t.Fatalf("paging visited %d of %d messages", len(walked), len(all))
	}
	if _, err := s.Messages(ctx, "u:nope", core.MessageID{}, 5); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("unknown chat: %v", err)
	}
}

func TestMessagesAround(t *testing.T) {
	s, _ := newTestService(t, Options{Seed: 3})
	ctx := context.Background()
	all := allMessages(t, s, ChatHike)
	n := len(all)
	tests := []struct {
		name       string
		idx, limit int
		from, to   int
	}{
		{"middle odd", 30, 5, 28, 33},
		{"middle even", 30, 4, 29, 33},
		{"start", 0, 5, 0, 5},
		{"near start", 1, 6, 0, 6},
		{"end", n - 1, 5, n - 5, n},
		{"limit one", 12, 1, 12, 13},
		{"larger than chat", 5, n + 10, 0, n},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.MessagesAround(ctx, all[tt.idx].ID, tt.limit)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, all[tt.from:tt.to]) {
				t.Fatalf("window [%d,%d) mismatch (got %d msgs)", tt.from, tt.to, len(got))
			}
			found := false
			for _, m := range got {
				found = found || m.ID == all[tt.idx].ID
			}
			if !found {
				t.Fatal("window does not contain the target")
			}
		})
	}
	if _, err := s.MessagesAround(ctx, core.MessageID{Chat: ChatHike, Timestamp: 1}, 5); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("unknown id: %v", err)
	}
	m, err := s.Message(ctx, all[7].ID)
	if err != nil || !reflect.DeepEqual(m, all[7]) {
		t.Errorf("Message: %v", err)
	}
}

func TestWindowAround(t *testing.T) {
	tests := []struct{ idx, limit, n, start, end int }{
		{5, 3, 10, 4, 7},
		{0, 3, 10, 0, 3},
		{9, 3, 10, 7, 10},
		{2, 20, 10, 0, 10},
		{0, 1, 1, 0, 1},
	}
	for _, tt := range tests {
		s, e := windowAround(tt.idx, tt.limit, tt.n)
		if s != tt.start || e != tt.end {
			t.Errorf("windowAround(%d,%d,%d) = %d,%d want %d,%d", tt.idx, tt.limit, tt.n, s, e, tt.start, tt.end)
		}
	}
}

func TestSearch(t *testing.T) {
	s, _ := newTestService(t, Options{Seed: 3})
	ctx := context.Background()
	tests := []struct {
		name  string
		q     core.SearchQuery
		min   int
		check func(core.SearchHit) bool
	}{
		{name: "text case-insensitive", q: core.SearchQuery{Text: "THERMOS"}, min: 3},
		{name: "unicode fold", q: core.SearchQuery{Text: "KAISERSCHMARRN"}, min: 2},
		{name: "chat scoped", q: core.SearchQuery{Text: "thermos", Chat: ChatHike}, min: 2,
			check: func(h core.SearchHit) bool {
				return h.Message.ID.Chat == ChatHike && h.ChatTitle == "Warte Wanderers 🏔️"
			}},
		{name: "from", q: core.SearchQuery{Text: "tower", From: ElifACI}, min: 2,
			check: func(h core.SearchHit) bool { return h.Message.ID.Author == ElifACI }},
		{name: "has image", q: core.SearchQuery{HasType: "image"}, min: 8,
			check: func(h core.SearchHit) bool { return hasType(&h.Message, "image") }},
		{name: "has file", q: core.SearchQuery{HasType: "file"}, min: 2,
			check: func(h core.SearchHit) bool { return strings.HasSuffix(h.Message.Attachments[0].FileName, ".pdf") }},
		{name: "has link", q: core.SearchQuery{HasType: "link"}, min: 3,
			check: func(h core.SearchHit) bool {
				return h.Message.Preview != nil || strings.Contains(h.Message.Body, "http")
			}},
		{name: "has audio", q: core.SearchQuery{HasType: "audio"}, min: 2},
		{name: "has video", q: core.SearchQuery{HasType: "video"}, min: 1},
		{name: "before", q: core.SearchQuery{Text: "the", Before: testNow.AddDate(0, 0, -10)}, min: 5,
			check: func(h core.SearchHit) bool { return h.Message.SentAt.Before(testNow.AddDate(0, 0, -10)) }},
		{name: "after", q: core.SearchQuery{Text: "the", After: testNow.AddDate(0, 0, -2)}, min: 2,
			check: func(h core.SearchHit) bool { return h.Message.SentAt.After(testNow.AddDate(0, 0, -2)) }},
		{name: "limit", q: core.SearchQuery{Text: "e", Limit: 4}, min: 4},
		{name: "mentions resolved", q: core.SearchQuery{Text: "@Robin Weber"}, min: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hits, err := s.Search(ctx, tt.q)
			if err != nil {
				t.Fatal(err)
			}
			if len(hits) < tt.min {
				t.Fatalf("got %d hits, want ≥ %d", len(hits), tt.min)
			}
			if tt.q.Limit > 0 && len(hits) > tt.q.Limit {
				t.Fatalf("limit exceeded: %d", len(hits))
			}
			for i, h := range hits {
				if i > 0 && h.Message.ID.Timestamp > hits[i-1].Message.ID.Timestamp {
					t.Fatal("hits not newest first")
				}
				if tt.check != nil && !tt.check(h) {
					t.Fatalf("hit fails filter: %+v", h.Message.ID)
				}
				if tt.q.Text != "" {
					if len(h.Matches) == 0 {
						t.Fatalf("hit without matches: %q", h.Snippet)
					}
					for _, m := range h.Matches {
						if !strings.EqualFold(h.Snippet[m[0]:m[1]], tt.q.Text) {
							t.Fatalf("match %v = %q in snippet %q", m, h.Snippet[m[0]:m[1]], h.Snippet)
						}
					}
				}
			}
		})
	}
	// Spoilers, deleted and view-once messages are not searchable.
	if hits, _ := s.Search(ctx, core.SearchQuery{Text: "lighthouse keeper was the narrator"}); len(hits) != 0 {
		t.Errorf("spoiler text is searchable: %+v", hits)
	}
	if _, err := s.Search(ctx, core.SearchQuery{Text: "x", Chat: "u:nope"}); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("unknown chat: %v", err)
	}
}

func TestMakeSnippet(t *testing.T) {
	long := strings.Repeat("lorem ipsum dolor ", 20) + "NEEDLE" + strings.Repeat(" sit amet", 20)
	tests := []struct {
		name string
		text string
	}{
		{"short", "find the needle here"},
		{"long", long},
		{"long at start", "needle " + strings.Repeat("x", 300)},
		{"multibyte", strings.Repeat("ü", 120) + " needle " + strings.Repeat("é", 120)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hits := foldIndexAll(tt.text, "needle")
			snip, ms := makeSnippet(tt.text, hits)
			if len(ms) == 0 {
				t.Fatalf("no matches in %q", snip)
			}
			for _, m := range ms {
				if !strings.EqualFold(snip[m[0]:m[1]], "needle") {
					t.Fatalf("bad offsets %v in %q", m, snip)
				}
			}
			if len(tt.text) > snippetMax && len(snip) > snippetMax+2*len(ellipsis)+len("needle") {
				t.Fatalf("snippet too long: %d", len(snip))
			}
		})
	}
}

func TestFoldIndexAll(t *testing.T) {
	tests := []struct {
		s, q string
		want [][2]int
	}{
		{"Hello hello HELLO", "hello", [][2]int{{0, 5}, {6, 11}, {12, 17}}},
		{"aaaa", "aa", [][2]int{{0, 2}, {2, 4}}},
		{"Straße STRASSE", "straße", [][2]int{{0, 7}}},
		{"ÄRGER ärger", "ärger", [][2]int{{0, 6}, {7, 13}}},
		{"nothing", "x", nil},
		{"x", "", nil},
	}
	for _, tt := range tests {
		if got := foldIndexAll(tt.s, tt.q); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("foldIndexAll(%q,%q) = %v want %v", tt.s, tt.q, got, tt.want)
		}
	}
}

func TestFuzzyMatch(t *testing.T) {
	tests := []struct {
		s, q string
		want bool
	}{
		{"Warte Wanderers", "wwand", true},
		{"Warte Wanderers", "warte wand", true},
		{"Alice Chen", "ac", true},
		{"Alice Chen", "ca", false},
		{"Book club", "", true},
		{"Book club", "xyz", false},
	}
	for _, tt := range tests {
		if got := fuzzyMatch(tt.s, tt.q); got != tt.want {
			t.Errorf("fuzzyMatch(%q,%q) = %v", tt.s, tt.q, got)
		}
	}
}

func TestContactsAndMisc(t *testing.T) {
	s, _ := newTestService(t, Options{Seed: 3})
	ctx := context.Background()
	cs, err := s.Contacts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i, c := range cs {
		if c.IsSelf {
			t.Fatal("Contacts includes self")
		}
		if i > 0 && strings.ToLower(cs[i-1].DisplayName()) > strings.ToLower(c.DisplayName()) {
			t.Fatalf("contacts not sorted: %q > %q", cs[i-1].DisplayName(), c.DisplayName())
		}
	}
	if c, err := s.Contact(ctx, SelfACI); err != nil || !c.IsSelf {
		t.Errorf("Contact(self) = %+v, %v", c, err)
	}
	if c, err := s.Contact(ctx, JonasACI); err != nil || c.DisplayName() != "Jo 🐢" {
		t.Errorf("nickname precedence: %+v, %v", c, err)
	}
	if _, err := s.Contact(ctx, "nope"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("unknown contact: %v", err)
	}
	if _, err := s.Chat(ctx, "g:nope"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("unknown chat: %v", err)
	}
	stories, _ := s.Stories(ctx)
	for i := 1; i < len(stories); i++ {
		if stories[i].At.After(stories[i-1].At) {
			t.Fatal("stories not newest first")
		}
	}
	for _, st := range stories {
		if st.AuthorName == "" || st.AuthorName == "Unknown" {
			t.Errorf("story without author name: %+v", st.ID)
		}
	}
	calls, _ := s.CallLog(ctx)
	for i := 1; i < len(calls); i++ {
		if calls[i].Call.StartedAt.After(calls[i-1].Call.StartedAt) {
			t.Fatal("call log not newest first")
		}
	}
	set, err := s.Settings(ctx)
	if err != nil || !set.ReadReceipts {
		t.Errorf("settings: %+v %v", set, err)
	}
}
