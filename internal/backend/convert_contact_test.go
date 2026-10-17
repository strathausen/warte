package backend

import (
	"encoding/base64"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"go.mau.fi/mautrix-signal/pkg/libsignalgo"
	"go.mau.fi/mautrix-signal/pkg/signalmeow"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/types"

	"warte/internal/core"
)

func TestRecipientToContact(t *testing.T) {
	existing := core.Contact{ACI: aliceACI, Username: "alice.01", Verified: true, ProfileName: "Old", Nickname: "Ally"}
	r := &types.Recipient{
		ACI: aliceUUID, E164: "+15550001", ContactName: "Alice Chen",
		Profile: types.Profile{Name: "Alice", About: "hi", AboutEmoji: "👋", AvatarPath: "profiles/abc", FetchedAt: time.Now()},
	}
	c := recipientToContact(r, existing, selfACI)
	if c.Username != "alice.01" || !c.Verified {
		t.Error("fields signalmeow doesn't know must be kept")
	}
	if c.E164 != "+15550001" || c.ContactName != "Alice Chen" || c.ProfileName != "Alice" || c.About != "hi" || c.AboutEmoji != "👋" {
		t.Errorf("merged %+v", c)
	}
	if c.Nickname != "" {
		t.Error("the recipient store is the source of truth for nicknames")
	}
	if c.AvatarID == "" || c.AvatarID != avatarID("profile", string(aliceACI), "profiles/abc") {
		t.Errorf("avatar id %q", c.AvatarID)
	}
	if c.IsSelf {
		t.Error("alice is not self")
	}
	// Contact-sync avatar when no profile avatar; blocked flag; self.
	r2 := &types.Recipient{ACI: selfUUID, ContactAvatar: types.ContactAvatar{Hash: "h"}, Blocked: true}
	c2 := recipientToContact(r2, core.Contact{}, selfACI)
	if c2.AvatarID != avatarID("contact", string(selfACI), "h") || !c2.Blocked || !c2.IsSelf {
		t.Errorf("contact avatar %+v", c2)
	}
	// A fetched profile without avatar clears the avatar.
	r3 := &types.Recipient{ACI: aliceUUID, Profile: types.Profile{FetchedAt: time.Now()}}
	if c3 := recipientToContact(r3, core.Contact{ACI: aliceACI, AvatarID: "old"}, selfACI); c3.AvatarID != "" {
		t.Error("removed profile avatar should clear AvatarID")
	}
	if avatarID("profile", "a", "") != "" {
		t.Error("no source, no avatar")
	}
}

func testGroup() *signalmeow.Group {
	pw := signalmeow.GenerateInviteLinkPassword()
	return &signalmeow.Group{
		GroupIdentifier:              types.GroupIdentifier(groupChat.GroupID()),
		Title:                        " Book club ",
		Description:                  "We read",
		AvatarPath:                   "groups/x",
		AnnouncementsOnly:            true,
		Revision:                     7,
		DisappearingMessagesDuration: 3600,
		Members: []*signalmeow.GroupMember{
			{ACI: selfUUID, Role: signalmeow.GroupMember_ADMINISTRATOR},
			{ACI: aliceUUID, Role: signalmeow.GroupMember_DEFAULT},
			nil,
		},
		PendingMembers: []*signalmeow.PendingMember{
			{ServiceID: libsignalgo.NewACIServiceID(bobUUID), Role: signalmeow.GroupMember_DEFAULT},
			{ServiceID: libsignalgo.NewPNIServiceID(uuid.New())},
		},
		RequestingMembers:  []*signalmeow.RequestingMember{{ACI: bobUUID}},
		AccessControl:      &signalmeow.GroupAccessControl{AddFromInviteLink: signalmeow.AccessControl_ADMINISTRATOR},
		InviteLinkPassword: &pw,
	}
}

func TestGroupToChat(t *testing.T) {
	g := testGroup()
	existing := core.Chat{ID: groupChat, Archived: true, Pinned: true, PinOrder: 2}
	c := groupToChat(g, existing, selfACI, testNames, "https://signal.group/#x")
	want := core.Chat{
		ID: groupChat, Kind: core.ChatGroup, Title: "Book club", Description: "We read",
		AvatarID: avatarID("group", groupChat.GroupID(), "groups/x"),
		Archived: true, Pinned: true, PinOrder: 2,
		ExpireTimer: time.Hour, OnlyAdminsSend: true, GroupRevision: 7, SelfIsAdmin: true,
		Members:         []core.Member{{ACI: selfACI, Name: "Unknown", Admin: true}, {ACI: aliceACI, Name: "Alice"}},
		PendingMembers:  []core.Member{{ACI: bobACI, Name: "Bob"}},
		RequestingJoin:  []core.Member{{ACI: bobACI, Name: "Bob"}},
		AddFromLinkMode: core.AccessAdmin,
		InviteLink:      "https://signal.group/#x",
	}
	if !reflect.DeepEqual(c, want) {
		t.Errorf("got  %+v\nwant %+v", c, want)
	}
	if !isMember(g, aliceACI) || isMember(g, bobACI) || !isPendingMember(g, bobACI) || isPendingMember(g, aliceACI) {
		t.Error("membership helpers")
	}
	// Invited: shows as a message request; link off hides the link.
	g.AccessControl.AddFromInviteLink = signalmeow.AccessControl_UNSATISFIABLE
	c = groupToChat(g, core.Chat{}, bobACI, testNames, "https://signal.group/#x")
	if !c.MessageRequest || c.SelfIsAdmin || c.InviteLink != "" || c.AddFromLinkMode != core.AccessUnsatisfiable {
		t.Errorf("invited view %+v", c)
	}
}

func TestCanSend(t *testing.T) {
	member := []core.Member{{ACI: selfACI}}
	tests := []struct {
		name string
		c    core.Chat
		ok   bool
	}{
		{"direct", core.Chat{Kind: core.ChatDirect}, true},
		{"blocked", core.Chat{Blocked: true}, false},
		{"request", core.Chat{MessageRequest: true}, false},
		{"group member", core.Chat{Kind: core.ChatGroup, GroupRevision: 3, Members: member}, true},
		{"group unknown state", core.Chat{Kind: core.ChatGroup}, true},
		{"left group", core.Chat{Kind: core.ChatGroup, GroupRevision: 3}, false},
		{"announcement", core.Chat{Kind: core.ChatGroup, GroupRevision: 3, Members: member, OnlyAdminsSend: true}, false},
		{"announcement admin", core.Chat{Kind: core.ChatGroup, GroupRevision: 3, Members: member, OnlyAdminsSend: true, SelfIsAdmin: true}, true},
	}
	for _, tt := range tests {
		err := canSend(tt.c, selfACI)
		if (err == nil) != tt.ok {
			t.Errorf("%s: err = %v", tt.name, err)
		}
		if err != nil && !errors.Is(err, core.ErrReadOnly) {
			t.Errorf("%s: want ErrReadOnly, got %v", tt.name, err)
		}
	}
}

func TestAccessModeRoundTrip(t *testing.T) {
	for _, m := range []core.AccessMode{core.AccessAny, core.AccessMember, core.AccessAdmin, core.AccessUnsatisfiable, core.AccessUnknown} {
		if got := accessMode(accessControl(m)); got != m {
			t.Errorf("%v -> %v", m, got)
		}
	}
}

func TestMutedUntil(t *testing.T) {
	if !mutedUntil(0).IsZero() {
		t.Error("0 = not muted")
	}
	if !mutedUntil(math.MaxInt64).Equal(foreverMuted) {
		t.Error("INT64_MAX = forever")
	}
	if got := mutedUntil(1700000000000); !got.Equal(time.UnixMilli(1700000000000)) {
		t.Errorf("got %v", got)
	}
}

func TestReceiptStatus(t *testing.T) {
	if receiptStatus(signalpb.ReceiptMessage_DELIVERY) != core.StatusDelivered ||
		receiptStatus(signalpb.ReceiptMessage_READ) != core.StatusRead ||
		receiptStatus(signalpb.ReceiptMessage_VIEWED) != core.StatusViewed {
		t.Error("receipt mapping")
	}
}

func TestConvertStorage(t *testing.T) {
	mk := make([]byte, 32)
	mk[0] = 7
	gid, err := libsignalgo.GroupMasterKey(mk).GroupIdentifier()
	if err != nil {
		t.Fatal(err)
	}
	gchat := core.GroupChatID(base64.StdEncoding.EncodeToString(gid[:]))
	rec := func(r *signalpb.StorageRecord) *signalmeow.DecryptedStorageRecord {
		return &signalmeow.DecryptedStorageRecord{StorageRecord: r}
	}
	records := []*signalmeow.DecryptedStorageRecord{
		nil,
		rec(&signalpb.StorageRecord{Record: &signalpb.StorageRecord_Contact{Contact: &signalpb.ContactRecord{
			AciBinary: aliceUUID[:], Archived: true, MarkedUnread: true, MutedUntilTimestamp: math.MaxInt64,
			Username: "alice.01", IdentityState: signalpb.ContactRecord_VERIFIED, Whitelisted: true,
		}}}),
		rec(&signalpb.StorageRecord{Record: &signalpb.StorageRecord_Contact{Contact: &signalpb.ContactRecord{Pni: uuid.NewString()}}}),
		rec(&signalpb.StorageRecord{Record: &signalpb.StorageRecord_GroupV2{GroupV2: &signalpb.GroupV2Record{
			MasterKey: mk, Blocked: true, MutedUntilTimestamp: 1700000000000,
		}}}),
		rec(&signalpb.StorageRecord{Record: &signalpb.StorageRecord_GroupV2{GroupV2: &signalpb.GroupV2Record{MasterKey: []byte{1}}}}),
		rec(&signalpb.StorageRecord{Record: &signalpb.StorageRecord_Account{Account: &signalpb.AccountRecord{
			ReadReceipts: true, TypingIndicators: false, LinkPreviews: true, SealedSenderIndicators: true,
			UniversalExpireTimer: 86400, PhoneNumberSharingMode: signalpb.AccountRecord_NOBODY, UnlistedPhoneNumber: true,
			Username: "me.42", NoteToSelfArchived: true,
			PinnedConversations: []*signalpb.AccountRecord_PinnedConversation{
				{Identifier: &signalpb.AccountRecord_PinnedConversation_GroupMasterKey{GroupMasterKey: mk}},
				{Identifier: &signalpb.AccountRecord_PinnedConversation_Contact_{Contact: &signalpb.AccountRecord_PinnedConversation_Contact{ServiceId: string(aliceACI)}}},
				{Identifier: &signalpb.AccountRecord_PinnedConversation_LegacyGroupId{LegacyGroupId: []byte{1}}},
			},
		}}}),
		rec(&signalpb.StorageRecord{Record: &signalpb.StorageRecord_StickerPack{StickerPack: &signalpb.StickerPackRecord{
			PackId: []byte{0xab, 0xcd}, PackKey: []byte{0x01}, Position: 3}}}),
		rec(&signalpb.StorageRecord{Record: &signalpb.StorageRecord_StickerPack{StickerPack: &signalpb.StickerPackRecord{
			PackId: []byte{0xee}, DeletedAtTimestamp: 5}}}),
	}
	s := convertStorage(records, selfACI)
	wantChats := []storageChatState{
		{Chat: aliceChat, Archived: true, MarkedUnread: true, MutedUntil: foreverMuted, Whitelisted: true},
		{Chat: gchat, Blocked: true, MutedUntil: time.UnixMilli(1700000000000).UTC()},
		{Chat: core.DirectChatID(selfACI), Archived: true, Whitelisted: true},
	}
	if !reflect.DeepEqual(s.Chats, wantChats) {
		t.Errorf("chats\n got  %+v\n want %+v", s.Chats, wantChats)
	}
	if !reflect.DeepEqual(s.Contacts, []storageContact{{ACI: aliceACI, Username: "alice.01", Verified: true}}) {
		t.Errorf("contacts %+v", s.Contacts)
	}
	if !reflect.DeepEqual(s.Pinned, []core.ChatID{gchat, aliceChat}) {
		t.Errorf("pinned %+v", s.Pinned)
	}
	want := accountSettings{ReadReceipts: true, LinkPreviews: true, SealedSenderIndicators: true,
		UniversalExpireTimer: 24 * time.Hour, Username: "me.42", NoteToSelfArchived: true}
	if !s.HasAccount || s.Settings != want {
		t.Errorf("settings %+v", s.Settings)
	}
	if !reflect.DeepEqual(s.StickerPack, []storagePack{{ID: "abcd", Key: "01", Position: 3}, {ID: "ee", Deleted: true}}) {
		t.Errorf("packs %+v", s.StickerPack)
	}
}

func TestChatStateEqual(t *testing.T) {
	a := core.Chat{Archived: true, MutedUntil: time.Unix(5, 0)}
	b := a
	b.MutedUntil = time.Unix(5, 0).UTC()
	if !chatStateEqual(a, b) {
		t.Error("equal instants in different zones are equal")
	}
	b.Pinned = true
	if chatStateEqual(a, b) {
		t.Error("pin differs")
	}
}

func TestSelfContact(t *testing.T) {
	r := &types.Recipient{ACI: selfUUID, Profile: types.Profile{Name: "Me Myself"}}
	c := selfContact(selfUUID, "+1555", r, core.Contact{Username: "me.1"})
	if c.ACI != selfACI || c.E164 != "+1555" || !c.IsSelf || c.ProfileName != "Me Myself" || c.Username != "me.1" {
		t.Errorf("self %+v", c)
	}
	c = selfContact(selfUUID, "", nil, core.Contact{E164: "+1"})
	if c.E164 != "+1" || !c.IsSelf {
		t.Errorf("self without recipient %+v", c)
	}
}
