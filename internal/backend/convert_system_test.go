package backend

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.mau.fi/util/ptr"

	"go.mau.fi/mautrix-signal/pkg/libsignalgo"
	"go.mau.fi/mautrix-signal/pkg/signalmeow"

	"warte/internal/core"
)

func testNames(a core.ACI) string {
	switch a {
	case aliceACI:
		return "Alice"
	case bobACI:
		return "Bob"
	}
	return "Unknown"
}

func TestFormatTimer(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "off"},
		{-time.Second, "off"},
		{time.Second, "1 second"},
		{30 * time.Second, "30 seconds"},
		{5 * time.Minute, "5 minutes"},
		{time.Hour, "1 hour"},
		{8 * time.Hour, "8 hours"},
		{24 * time.Hour, "1 day"},
		{7 * 24 * time.Hour, "1 week"},
		{4 * 7 * 24 * time.Hour, "4 weeks"},
		{90 * time.Minute, "90 minutes"},
		{36 * time.Hour, "36 hours"},
		{1500 * time.Millisecond, "1 second"},
		{time.Millisecond, "1 second"},
	}
	for _, tt := range tests {
		if got := formatTimer(tt.d); got != tt.want {
			t.Errorf("formatTimer(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestTimerChangeText(t *testing.T) {
	tests := []struct {
		who  core.ACI
		d    time.Duration
		want string
	}{
		{aliceACI, time.Hour, "Alice set the disappearing message timer to 1 hour."},
		{selfACI, 7 * 24 * time.Hour, "You set the disappearing message timer to 1 week."},
		{bobACI, 0, "Bob disabled disappearing messages."},
		{"", time.Minute, "Someone set the disappearing message timer to 1 minute."},
	}
	for _, tt := range tests {
		if got := timerChangeText(tt.who, selfACI, testNames, tt.d); got != tt.want {
			t.Errorf("got %q, want %q", got, tt.want)
		}
	}
}

func TestGroupChangeText(t *testing.T) {
	alice := libsignalgo.NewACIServiceID(aliceUUID)
	self := libsignalgo.NewACIServiceID(selfUUID)
	admin := signalmeow.AccessControl_ADMINISTRATOR
	anyone := signalmeow.AccessControl_ANY
	off := signalmeow.AccessControl_UNSATISFIABLE
	member := signalmeow.AccessControl_MEMBER
	tests := []struct {
		name string
		ch   *signalmeow.GroupChange
		want string
	}{
		{"nil", nil, ""},
		{"empty", &signalmeow.GroupChange{SourceServiceID: alice}, "Alice updated the group."},
		{"unknown source", &signalmeow.GroupChange{}, "The group updated the group."},
		{"title", &signalmeow.GroupChange{SourceServiceID: alice, ModifyTitle: ptr.Ptr("Book club")}, "Alice changed the group name to “Book club”."},
		{"title by self", &signalmeow.GroupChange{SourceServiceID: self, ModifyTitle: ptr.Ptr("X")}, "You changed the group name to “X”."},
		{"title removed", &signalmeow.GroupChange{SourceServiceID: alice, ModifyTitle: ptr.Ptr(" ")}, "Alice removed the group name."},
		{"avatar", &signalmeow.GroupChange{SourceServiceID: alice, ModifyAvatar: ptr.Ptr("path")}, "Alice changed the group avatar."},
		{"avatar removed", &signalmeow.GroupChange{SourceServiceID: alice, ModifyAvatar: ptr.Ptr("")}, "Alice removed the group avatar."},
		{"description", &signalmeow.GroupChange{SourceServiceID: alice, ModifyDescription: ptr.Ptr("about")}, "Alice changed the group description."},
		{"description removed", &signalmeow.GroupChange{SourceServiceID: alice, ModifyDescription: ptr.Ptr("")}, "Alice removed the group description."},
		{"timer", &signalmeow.GroupChange{SourceServiceID: alice, ModifyDisappearingMessagesDuration: ptr.Ptr(uint32(86400))}, "Alice set the disappearing message timer to 1 day."},
		{"timer off", &signalmeow.GroupChange{SourceServiceID: alice, ModifyDisappearingMessagesDuration: ptr.Ptr(uint32(0))}, "Alice disabled disappearing messages."},
		{"add member", &signalmeow.GroupChange{SourceServiceID: alice, AddMembers: []*signalmeow.AddMember{{GroupMember: signalmeow.GroupMember{ACI: bobUUID}}}}, "Alice added Bob."},
		{"add self", &signalmeow.GroupChange{SourceServiceID: alice, AddMembers: []*signalmeow.AddMember{{GroupMember: signalmeow.GroupMember{ACI: selfUUID}}}}, "Alice added you to the group."},
		{"join via link", &signalmeow.GroupChange{SourceServiceID: libsignalgo.NewACIServiceID(bobUUID), AddMembers: []*signalmeow.AddMember{{GroupMember: signalmeow.GroupMember{ACI: bobUUID}, JoinFromInviteLink: true}}}, "Bob joined the group via the group link."},
		{"joined", &signalmeow.GroupChange{SourceServiceID: libsignalgo.NewACIServiceID(bobUUID), AddMembers: []*signalmeow.AddMember{{GroupMember: signalmeow.GroupMember{ACI: bobUUID}}}}, "Bob joined the group."},
		{"left", &signalmeow.GroupChange{SourceServiceID: libsignalgo.NewACIServiceID(bobUUID), DeleteMembers: []*uuid.UUID{ptr.Ptr(bobUUID)}}, "Bob left the group."},
		{"self left", &signalmeow.GroupChange{SourceServiceID: self, DeleteMembers: []*uuid.UUID{ptr.Ptr(selfUUID)}}, "You left the group."},
		{"removed you", &signalmeow.GroupChange{SourceServiceID: alice, DeleteMembers: []*uuid.UUID{ptr.Ptr(selfUUID)}}, "Alice removed you."},
		{"promote", &signalmeow.GroupChange{SourceServiceID: alice, ModifyMemberRoles: []*signalmeow.RoleMember{{ACI: bobUUID, Role: signalmeow.GroupMember_ADMINISTRATOR}}}, "Alice made Bob an admin."},
		{"demote", &signalmeow.GroupChange{SourceServiceID: alice, ModifyMemberRoles: []*signalmeow.RoleMember{{ACI: selfUUID, Role: signalmeow.GroupMember_DEFAULT}}}, "Alice revoked admin privileges from you."},
		{"invite one", &signalmeow.GroupChange{SourceServiceID: alice, AddPendingMembers: []*signalmeow.PendingMember{{ServiceID: libsignalgo.NewACIServiceID(bobUUID)}}}, "Alice invited 1 person to the group."},
		{"invite many", &signalmeow.GroupChange{SourceServiceID: alice, AddPendingMembers: []*signalmeow.PendingMember{{ServiceID: libsignalgo.NewACIServiceID(bobUUID)}, {ServiceID: libsignalgo.NewPNIServiceID(uuid.New())}}}, "Alice invited 2 people to the group."},
		{"invite self", &signalmeow.GroupChange{SourceServiceID: alice, AddPendingMembers: []*signalmeow.PendingMember{{ServiceID: self}}}, "Alice invited you to the group."},
		{"declined", &signalmeow.GroupChange{SourceServiceID: libsignalgo.NewACIServiceID(bobUUID), DeletePendingMembers: []*libsignalgo.ServiceID{ptr.Ptr(libsignalgo.NewACIServiceID(bobUUID))}}, "Bob declined the invitation to the group."},
		{"revoked", &signalmeow.GroupChange{SourceServiceID: alice, DeletePendingMembers: []*libsignalgo.ServiceID{ptr.Ptr(libsignalgo.NewACIServiceID(bobUUID))}}, "Alice revoked an invitation to the group."},
		{"revoked many", &signalmeow.GroupChange{SourceServiceID: alice, DeletePendingMembers: []*libsignalgo.ServiceID{ptr.Ptr(libsignalgo.NewACIServiceID(bobUUID)), ptr.Ptr(self)}}, "Alice revoked 2 invitations to the group."},
		{"accepted", &signalmeow.GroupChange{SourceServiceID: libsignalgo.NewACIServiceID(bobUUID), PromotePendingMembers: []*signalmeow.PromotePendingMember{{ACI: bobUUID}}}, "Bob accepted the invitation to the group."},
		{"accepted pni", &signalmeow.GroupChange{PromotePendingPniAciMembers: []*signalmeow.PromotePendingPniAciMember{{ACI: selfUUID}}}, "You accepted the invitation to the group."},
		{"requested", &signalmeow.GroupChange{AddRequestingMembers: []*signalmeow.RequestingMember{{ACI: bobUUID}}}, "Bob requested to join via the group link."},
		{"canceled request", &signalmeow.GroupChange{SourceServiceID: libsignalgo.NewACIServiceID(bobUUID), DeleteRequestingMembers: []*uuid.UUID{ptr.Ptr(bobUUID)}}, "Bob canceled their request to join the group."},
		{"denied", &signalmeow.GroupChange{SourceServiceID: alice, DeleteRequestingMembers: []*uuid.UUID{ptr.Ptr(bobUUID)}}, "Alice denied a request to join the group from Bob."},
		{"approved", &signalmeow.GroupChange{SourceServiceID: self, PromoteRequestingMembers: []*signalmeow.RoleMember{{ACI: bobUUID}}}, "You approved a request to join the group from Bob."},
		{"attributes", &signalmeow.GroupChange{SourceServiceID: alice, ModifyAttributesAccess: &admin}, "Alice changed who can edit group info to “Only admins”."},
		{"members access", &signalmeow.GroupChange{SourceServiceID: alice, ModifyMemberAccess: &member}, "Alice changed who can add members to “All members”."},
		{"link on", &signalmeow.GroupChange{SourceServiceID: alice, ModifyAddFromInviteLinkAccess: &anyone, ModifyInviteLinkPassword: ptr.Ptr(signalmeow.GenerateInviteLinkPassword())}, "Alice turned on the group link with admin approval off."},
		{"link approval", &signalmeow.GroupChange{SourceServiceID: alice, ModifyAddFromInviteLinkAccess: &admin}, "Alice turned on the group link with admin approval on."},
		{"link off", &signalmeow.GroupChange{SourceServiceID: alice, ModifyAddFromInviteLinkAccess: &off}, "Alice turned off the group link."},
		{"link reset", &signalmeow.GroupChange{SourceServiceID: alice, ModifyInviteLinkPassword: ptr.Ptr(signalmeow.GenerateInviteLinkPassword())}, "Alice reset the group link."},
		{"announce", &signalmeow.GroupChange{SourceServiceID: alice, ModifyAnnouncementsOnly: ptr.Ptr(true)}, "Alice changed the group settings to only allow admins to send messages."},
		{"announce off", &signalmeow.GroupChange{SourceServiceID: alice, ModifyAnnouncementsOnly: ptr.Ptr(false)}, "Alice changed the group settings to allow all members to send messages."},
		{"multi", &signalmeow.GroupChange{SourceServiceID: alice, ModifyTitle: ptr.Ptr("T"), AddMembers: []*signalmeow.AddMember{{GroupMember: signalmeow.GroupMember{ACI: bobUUID}}}},
			"Alice changed the group name to “T”.\nAlice added Bob."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := groupChangeText(tt.ch, selfACI, testNames); got != tt.want {
				t.Errorf("got  %q\nwant %q", got, tt.want)
			}
		})
	}
}

func TestMiscSystemTexts(t *testing.T) {
	if got := groupCreatedText(selfACI, selfACI, testNames); got != "You created the group." {
		t.Error(got)
	}
	if got := groupCreatedText(aliceACI, selfACI, testNames); got != "Alice added you to the group." {
		t.Error(got)
	}
	if got := pinText(aliceACI, selfACI, testNames, true); got != "Alice pinned a message." {
		t.Error(got)
	}
	if got := pinText(selfACI, selfACI, testNames, false); got != "You unpinned a message." {
		t.Error(got)
	}
	if got := safetyNumberChangedText("Alice"); !strings.Contains(got, "Alice") {
		t.Error(got)
	}
	if got := decryptionErrorText("Bob"); !strings.Contains(got, "Bob") {
		t.Error(got)
	}
}

func TestCallText(t *testing.T) {
	tests := []struct {
		c    core.CallInfo
		want string
	}{
		{core.CallInfo{Incoming: true}, "Incoming voice call"},
		{core.CallInfo{Incoming: true, Video: true}, "Incoming video call"},
		{core.CallInfo{Incoming: true, Missed: true}, "Missed voice call"},
		{core.CallInfo{Incoming: true, Missed: true, Video: true}, "Missed video call"},
		{core.CallInfo{Declined: true}, "Declined voice call"},
		{core.CallInfo{}, "Outgoing voice call"},
		{core.CallInfo{Group: true, Missed: true}, "Group call"},
	}
	for _, tt := range tests {
		if got := callText(tt.c); got != tt.want {
			t.Errorf("callText(%+v) = %q, want %q", tt.c, got, tt.want)
		}
	}
}
