package backend

import (
	"context"
	"encoding/base64"
	"reflect"
	"testing"
	"time"

	"go.mau.fi/mautrix-signal/pkg/libsignalgo"
	"go.mau.fi/mautrix-signal/pkg/signalmeow"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"

	"warte/internal/core"
)

func folderRec(f *signalpb.ChatFolderRecord) *signalmeow.DecryptedStorageRecord {
	return &signalmeow.DecryptedStorageRecord{StorageRecord: &signalpb.StorageRecord{
		Record: &signalpb.StorageRecord_ChatFolder{ChatFolder: f}}}
}

func TestConvertChatFolders(t *testing.T) {
	mk := make([]byte, 32)
	mk[0] = 9
	gid, err := libsignalgo.GroupMasterKey(mk).GroupIdentifier()
	if err != nil {
		t.Fatal(err)
	}
	gchat := core.GroupChatID(base64.StdEncoding.EncodeToString(gid[:]))
	contact := func(u []byte) *signalpb.Recipient {
		return &signalpb.Recipient{Identifier: &signalpb.Recipient_Contact_{Contact: &signalpb.Recipient_Contact{ServiceIdBinary: u}}}
	}
	records := []*signalmeow.DecryptedStorageRecord{
		folderRec(&signalpb.ChatFolderRecord{Identifier: []byte{2}, Name: "Family", Position: 2,
			FolderType: signalpb.ChatFolderRecord_CUSTOM, ShowMutedChats: true,
			IncludedRecipients: []*signalpb.Recipient{
				{Identifier: &signalpb.Recipient_GroupMasterKey{GroupMasterKey: mk}},
				contact(aliceUUID[:]),
				{Identifier: &signalpb.Recipient_LegacyGroupId{LegacyGroupId: []byte{1}}},
			}}),
		folderRec(&signalpb.ChatFolderRecord{Identifier: []byte{1}, Name: "People", Position: 1,
			FolderType: signalpb.ChatFolderRecord_CUSTOM, IncludeAllIndividualChats: true, ShowOnlyUnread: true,
			ExcludedRecipients: []*signalpb.Recipient{contact(aliceUUID[:])}}),
		folderRec(&signalpb.ChatFolderRecord{Identifier: []byte{3}, Name: "All chats", FolderType: signalpb.ChatFolderRecord_ALL}),
		folderRec(&signalpb.ChatFolderRecord{Identifier: []byte{4}, Name: "Gone", FolderType: signalpb.ChatFolderRecord_CUSTOM, DeletedAtTimestampMs: 5}),
		folderRec(&signalpb.ChatFolderRecord{Name: "No ID", FolderType: signalpb.ChatFolderRecord_CUSTOM}),
	}
	s := convertStorage(records, selfACI)
	want := []storageFolder{
		{Position: 1, Folder: core.Folder{ID: "sf:01", Name: "People", Filter: core.ChatFilter{FolderID: "sf:01", UnreadOnly: true,
			Members: &core.FolderMembers{AllDirect: true, HideMuted: true, Exclude: []core.ChatID{aliceChat}}}}},
		{Position: 2, Folder: core.Folder{ID: "sf:02", Name: "Family", Filter: core.ChatFilter{FolderID: "sf:02",
			Members: &core.FolderMembers{Include: []core.ChatID{gchat, aliceChat}}}}},
	}
	if !reflect.DeepEqual(s.Folders, want) {
		t.Errorf("folders\n got  %+v\n want %+v", s.Folders, want)
	}
}

func TestSyncFolders(t *testing.T) {
	b, _ := newTestBackend(t)
	ctx := context.Background()
	bobChat := core.DirectChatID(bobACI)
	for _, c := range []core.Chat{
		{ID: aliceChat, Kind: core.ChatDirect, Title: "Alice"},
		{ID: bobChat, Kind: core.ChatDirect, Title: "Bob",
			MutedUntil: time.Now().Add(time.Hour)},
	} {
		if err := b.db.UpsertChat(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	// A folder that did not come from the storage service survives syncs.
	if err := b.db.PutFolder(ctx, core.Folder{ID: "local", Name: "Local"}); err != nil {
		t.Fatal(err)
	}
	people := storageFolder{Position: 1, Folder: core.Folder{ID: "sf:01", Name: "People",
		Filter: core.ChatFilter{FolderID: "sf:01", Members: &core.FolderMembers{AllDirect: true, HideMuted: true}}}}
	b.applyStorageSnapshot(ctx, storageSnapshot{HasAccount: true, Folders: []storageFolder{people}})

	fs, err := b.Folders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 2 || fs[0].ID != "sf:01" || fs[1].ID != "local" {
		t.Fatalf("folders %+v", fs)
	}
	f := fs[0].Filter
	f.IncludeEmpty = true
	chats, err := b.Chats(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(chats) != 1 || chats[0].ID != aliceChat {
		t.Fatalf("folder chats %+v (muted Bob must be hidden)", chats)
	}
	// A partial snapshot (no account record) leaves folders alone; a full
	// one without the folder deletes it.
	b.applyStorageSnapshot(ctx, storageSnapshot{})
	if fs, _ := b.Folders(ctx); len(fs) != 2 {
		t.Fatalf("partial snapshot changed folders: %+v", fs)
	}
	b.applyStorageSnapshot(ctx, storageSnapshot{HasAccount: true})
	if fs, _ := b.Folders(ctx); len(fs) != 1 || fs[0].ID != "local" {
		t.Fatalf("folders after removal %+v", fs)
	}
}
