package fake

import (
	"strings"
	"time"

	"warte/internal/core"
)

// Well-known demo identities, exported so TUI tests can address them.
const (
	SelfACI   core.ACI = "3f6a1c2e-8b4d-4e7a-9c1f-2d5b8a6e4f01"
	AliceACI  core.ACI = "a11ce5a1-7c3b-4d2e-8f6a-1b9c0d2e3f41"
	BobACI    core.ACI = "b0b0c4fe-2d1e-4a3b-9c8d-7e6f5a4b3c22"
	ClaraACI  core.ACI = "c1a7a9e2-4f3d-4b2c-8a1e-6d5c4b3a2f13"
	DevACI    core.ACI = "de7a11e0-9b8c-4d7e-a6f5-e4d3c2b1a094"
	ElifACI   core.ACI = "e11f0b3a-5c6d-4e7f-8091-a2b3c4d5e6f5"
	FelixACI  core.ACI = "fe11c5b2-1a2b-4c3d-9e4f-5a6b7c8d9e06"
	GraceACI  core.ACI = "67ace0d1-3e2f-4a1b-8c9d-0e1f2a3b4c57"
	HanaACI   core.ACI = "4a7ab00c-6d5e-4f4a-9b3c-2d1e0f9a8b68"
	MomACI    core.ACI = "3033a0e1-8f7e-4d6c-b5a4-938271605f19"
	DadACI    core.ACI = "dad0be47-2c3d-4e5f-a6b7-c8d9e0f1a20a"
	JonasACI  core.ACI = "70a5b1c8-4d3e-4f2a-8b1c-0d9e8f7a6b3b"
	NoorACI   core.ACI = "a00b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5c"
	MarcoACI  core.ACI = "3a2c0b1d-9e8f-4a7b-b6c5-d4e3f2a1b0cd"
	InesACI   core.ACI = "1e5d0a7e-6b5c-4d4e-9f3a-2b1c0d9e8fde"
	MaxACI    core.ACI = "9a3f5e2d-0c1b-4a9f-8e7d-6c5b4a3f2e1f"
	RangerACI core.ACI = "5a26e101-7f6e-4d5c-8b4a-39281706f5a0"
	OskarACI  core.ACI = "05ca7000-1b2c-4d3e-8f4a-5b6c7d8e9f60"
)

// Well-known demo chats.
var (
	ChatNoteToSelf  = core.DirectChatID(SelfACI)
	ChatAlice       = core.DirectChatID(AliceACI)
	ChatDev         = core.DirectChatID(DevACI)
	ChatMom         = core.DirectChatID(MomACI)
	ChatFelix       = core.DirectChatID(FelixACI) // muted forever
	ChatNoor        = core.DirectChatID(NoorACI)  // disappearing messages
	ChatJonas       = core.DirectChatID(JonasACI)
	ChatMarco       = core.DirectChatID(MarcoACI) // archived
	ChatInes        = core.DirectChatID(InesACI)  // message request
	ChatMax         = core.DirectChatID(MaxACI)   // blocked
	ChatHike        = core.GroupChatID(groupIDFor("warte-wanderers"))
	ChatBookClub    = core.GroupChatID(groupIDFor("book-club"))
	ChatFamily      = core.GroupChatID(groupIDFor("family"))
	ChatWork        = core.GroupChatID(groupIDFor("platform-team"))
	ChatTrailAlerts = core.GroupChatID(groupIDFor("trail-alerts")) // announcement-only
)

// Demo folder IDs.
const (
	FolderUnread  = "unread"
	FolderGroups  = "groups"
	FolderFriends = "friends"
	FolderWork    = "work"
)

type person struct {
	aci                   core.ACI
	given, family         string
	contactName, nickname string
	e164, username        string
	about, aboutEmoji     string
	avatar                string // "initials", "photo", "" = none
	verified, inAddrBook  bool
	blocked               bool
}

var cast = []person{
	{aci: AliceACI, given: "Alice", family: "Chen", contactName: "Alice Chen", e164: "+4915112340001", username: "alice.07",
		about: "probably on a mountain", aboutEmoji: "⛰️", avatar: "photo", verified: true, inAddrBook: true},
	{aci: BobACI, given: "Bob", family: "Okafor", contactName: "Bob Okafor", e164: "+4915112340002", username: "bob.88",
		about: "Snacks are a love language", aboutEmoji: "🥨", avatar: "initials", inAddrBook: true},
	{aci: ClaraACI, given: "Clara", family: "Hoffmann", contactName: "Clara Hoffmann", e164: "+4917012340003",
		about: "Currently reading: too many books at once", aboutEmoji: "📚", avatar: "initials", inAddrBook: true},
	{aci: DevACI, given: "Dev", family: "Patel", contactName: "Dev Patel", e164: "+4917612340004", username: "devp.11",
		about: "on call (sorry)", aboutEmoji: "📟", avatar: "initials", inAddrBook: true},
	{aci: ElifACI, given: "Elif", family: "Yılmaz", contactName: "Elif Yılmaz", e164: "+4915212340005", username: "elif.42",
		about: "Trail runner, map nerd", aboutEmoji: "🗺️", avatar: "photo", verified: true, inAddrBook: true},
	{aci: FelixACI, given: "Felix", family: "Braun", contactName: "Felix Braun", e164: "+4916012340006",
		about: "Ask me about sourdough", aboutEmoji: "🍞", avatar: "initials", inAddrBook: true},
	{aci: GraceACI, given: "Grace", family: "Liu", contactName: "Grace Liu", e164: "+4915712340007", username: "grace.03",
		about: "", avatar: "initials", inAddrBook: true},
	{aci: HanaACI, given: "Hana", family: "Sato", contactName: "Hana Sato", e164: "+4915912340008",
		about: "マイペース", aboutEmoji: "🍵", avatar: "initials", inAddrBook: true},
	{aci: MomACI, given: "Gabi", family: "Weber", contactName: "Mama", e164: "+4917212340009",
		about: "Grandma in training 🌻", avatar: "photo", verified: true, inAddrBook: true},
	{aci: DadACI, given: "Thomas", family: "Weber", contactName: "Papa", e164: "+4917212340010",
		avatar: "initials", inAddrBook: true},
	{aci: JonasACI, given: "Jonas", family: "Weber", contactName: "Jonas", nickname: "Jo 🐢", e164: "+4915112340011", username: "jonas.99",
		about: "studying, allegedly", aboutEmoji: "🎓", avatar: "initials", inAddrBook: true},
	{aci: NoorACI, given: "Noor", family: "Haddad", contactName: "Noor Haddad", username: "noor.21",
		about: "ephemeral by default", aboutEmoji: "🫧", avatar: "initials", inAddrBook: true},
	{aci: MarcoACI, given: "Marco", family: "Rossi", contactName: "Marco Rossi", e164: "+393401234012",
		about: "Torino", aboutEmoji: "🇮🇹", avatar: "initials", inAddrBook: true},
	{aci: InesACI, given: "Ines", family: "Duarte", username: "ines.15",
		about: "Organiser @ Kletterhalle Nord", aboutEmoji: "🧗", avatar: "initials"},
	{aci: MaxACI, given: "Max", family: "Profit 💸", e164: "+4915512340014",
		about: "DM for 10x returns", avatar: "initials", blocked: true},
	{aci: RangerACI, given: "Bergwacht", family: "Trail Info", username: "bergwacht.01",
		about: "Official trail & weather notices", aboutEmoji: "🚩", avatar: "initials"},
	{aci: OskarACI, given: "Oskar", family: "Lindqvist", contactName: "Oskar Lindqvist", e164: "+46701234016",
		about: "Fika enjoyer", aboutEmoji: "☕", avatar: "initials", inAddrBook: true},
}

const (
	selfGiven  = "Robin"
	selfFamily = "Weber"
)

// seed populates the service with demo data. It runs once from New.
func (s *Service) seed() {
	now := s.now()
	b := &builder{s: s, now: now}

	s.selfGiven, s.selfFamily = selfGiven, selfFamily
	s.self = core.Contact{
		ACI:         SelfACI,
		E164:        "+4915112340000",
		Username:    "robin.07",
		ProfileName: selfGiven + " " + selfFamily,
		About:       "Watching the ridges",
		AboutEmoji:  "🔭",
		AvatarID:    "avatar-self",
		IsSelf:      true,
		Verified:    true,
	}
	s.avatars["avatar-self"] = avatarInitialsPNG(s.self.ProfileName, 96)

	for _, p := range cast {
		c := &core.Contact{
			ACI:         p.aci,
			E164:        p.e164,
			Username:    p.username,
			ProfileName: strings.TrimSpace(p.given + " " + p.family),
			ContactName: p.contactName,
			Nickname:    p.nickname,
			About:       p.about,
			AboutEmoji:  p.aboutEmoji,
			Verified:    p.verified,
			Blocked:     p.blocked,
		}
		switch p.avatar {
		case "initials":
			c.AvatarID = "avatar-" + string(p.aci)
			s.avatars[c.AvatarID] = avatarInitialsPNG(c.DisplayName(), 96)
		case "photo":
			c.AvatarID = "avatar-" + string(p.aci)
			s.avatars[c.AvatarID] = landscapePNG(96, 96, int(colorIndex(c.DisplayName())%5))
		}
		s.contacts[p.aci] = c
	}

	s.seedStickers(b)
	s.seedChats(b)
	s.seedStories(b)
	b.finalize()
	s.seedFolders()
}

// direct creates a 1:1 chat state.
func (s *Service) direct(aci core.ACI) *chatState {
	kind := core.ChatDirect
	if aci == s.self.ACI {
		kind = core.ChatNoteToSelf
	}
	cs := &chatState{chat: core.Chat{
		ID:    core.DirectChatID(aci),
		Kind:  kind,
		Color: avatarColorNames[colorIndex(string(aci))],
	}}
	s.chats[cs.chat.ID] = cs
	return cs
}

// group creates a group chat state. The first admin list entries are admins.
func (s *Service) group(id core.ChatID, title, desc string, members []core.ACI, admins ...core.ACI) *chatState {
	isAdmin := map[core.ACI]bool{}
	for _, a := range admins {
		isAdmin[a] = true
	}
	cs := &chatState{chat: core.Chat{
		ID:              id,
		Kind:            core.ChatGroup,
		Title:           title,
		Description:     desc,
		Color:           avatarColorNames[colorIndex(title)],
		AvatarID:        "avatar-" + id.GroupID(),
		SelfIsAdmin:     isAdmin[s.self.ACI],
		GroupRevision:   uint32(3 + colorIndex(title)),
		AddFromLinkMode: core.AccessUnsatisfiable,
	}}
	s.avatars[cs.chat.AvatarID] = avatarGeometricPNG(title, 96)
	for _, m := range append([]core.ACI{s.self.ACI}, members...) {
		cs.chat.Members = append(cs.chat.Members, core.Member{ACI: m, Admin: isAdmin[m]})
	}
	s.chats[id] = cs
	return cs
}

func (s *Service) seedFolders() {
	set := func(ids ...core.ChatID) map[core.ChatID]bool {
		m := map[core.ChatID]bool{}
		for _, id := range ids {
			m[id] = true
		}
		return m
	}
	s.folders = []*folderState{
		{folder: core.Folder{ID: FolderUnread, Name: "Unread", Filter: core.ChatFilter{UnreadOnly: true}}},
		{folder: core.Folder{ID: FolderGroups, Name: "Groups", Filter: core.ChatFilter{GroupsOnly: true}}},
		{folder: core.Folder{ID: FolderFriends, Name: "Friends"},
			members: set(ChatAlice, ChatHike, ChatBookClub, ChatFelix, ChatNoor, ChatJonas, ChatMarco)},
		{folder: core.Folder{ID: FolderWork, Name: "Work"},
			members: set(ChatWork, ChatDev)},
	}
	for _, f := range s.folders {
		f.folder.Filter.FolderID = f.folder.ID
	}
}

// muteForever is the far-future MutedUntil used for "muted always".
var muteForever = time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)
