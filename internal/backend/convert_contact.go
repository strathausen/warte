package backend

import (
	"cmp"
	"encoding/base64"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.mau.fi/mautrix-signal/pkg/libsignalgo"
	"go.mau.fi/mautrix-signal/pkg/signalmeow"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/types"

	"warte/internal/core"
)

// avatarID derives the opaque avatar ID for a source (profile path, contact
// avatar hash, group avatar path). It changes whenever the source changes,
// so frontends can cache by ID.
func avatarID(kind, owner, source string) string {
	if source == "" {
		return ""
	}
	return hashID("v", kind, owner, source)
}

// recipientToContact merges signalmeow's view of a recipient into the stored
// warte contact. Fields signalmeow does not know (username, verification)
// are kept from existing.
func recipientToContact(r *types.Recipient, existing core.Contact, self core.ACI) core.Contact {
	c := existing
	aci := aciOf(r.ACI)
	if aci != "" {
		c.ACI = aci
	}
	if r.E164 != "" {
		c.E164 = r.E164
	}
	if r.ContactName != "" {
		c.ContactName = r.ContactName
	}
	// signalmeow's recipient store is the source of truth for nicknames:
	// storage sync writes them and SetNickname mirrors local edits into it.
	c.Nickname = r.Nickname
	if r.Profile.Name != "" {
		c.ProfileName = r.Profile.Name
	}
	if r.Profile.About != "" || r.Profile.AboutEmoji != "" || !r.Profile.FetchedAt.IsZero() {
		c.About, c.AboutEmoji = r.Profile.About, r.Profile.AboutEmoji
	}
	switch {
	case r.Profile.AvatarPath != "":
		c.AvatarID = avatarID("profile", string(c.ACI), r.Profile.AvatarPath)
	case r.ContactAvatar.Hash != "":
		c.AvatarID = avatarID("contact", string(c.ACI), r.ContactAvatar.Hash)
	case !r.Profile.FetchedAt.IsZero():
		c.AvatarID = ""
	}
	c.Blocked = r.Blocked
	c.IsSelf = c.ACI != "" && c.ACI == self
	return c
}

// accessMode maps signalmeow's access control to core's.
func accessMode(a signalmeow.AccessControl) core.AccessMode {
	switch a {
	case signalmeow.AccessControl_ANY:
		return core.AccessAny
	case signalmeow.AccessControl_MEMBER:
		return core.AccessMember
	case signalmeow.AccessControl_ADMINISTRATOR:
		return core.AccessAdmin
	case signalmeow.AccessControl_UNSATISFIABLE:
		return core.AccessUnsatisfiable
	}
	return core.AccessUnknown
}

// accessControl maps core's access mode to signalmeow's.
func accessControl(a core.AccessMode) signalmeow.AccessControl {
	switch a {
	case core.AccessAny:
		return signalmeow.AccessControl_ANY
	case core.AccessMember:
		return signalmeow.AccessControl_MEMBER
	case core.AccessAdmin:
		return signalmeow.AccessControl_ADMINISTRATOR
	case core.AccessUnsatisfiable:
		return signalmeow.AccessControl_UNSATISFIABLE
	}
	return signalmeow.AccessControl_UNKNOWN
}

// groupToChat merges a decrypted group into the stored chat. inviteLink is
// the group link if one is enabled (computed by the caller because it needs
// the master key).
func groupToChat(g *signalmeow.Group, existing core.Chat, self core.ACI, name nameFunc, inviteLink string) core.Chat {
	c := existing
	c.ID = groupChatID(g.GroupIdentifier)
	c.Kind = core.ChatGroup
	c.Title = strings.TrimSpace(g.Title)
	c.Description = g.Description
	c.GroupRevision = g.Revision
	c.OnlyAdminsSend = g.AnnouncementsOnly
	c.ExpireTimer = time.Duration(g.DisappearingMessagesDuration) * time.Second
	c.AvatarID = avatarID("group", string(g.GroupIdentifier), g.AvatarPath)
	c.Members, c.PendingMembers, c.RequestingJoin = nil, nil, nil
	c.SelfIsAdmin = false
	for _, m := range g.Members {
		if m == nil {
			continue
		}
		aci := aciOf(m.ACI)
		admin := m.Role == signalmeow.GroupMember_ADMINISTRATOR
		c.Members = append(c.Members, core.Member{ACI: aci, Name: name(aci), Admin: admin})
		if aci == self && admin {
			c.SelfIsAdmin = true
		}
	}
	for _, p := range g.PendingMembers {
		if p == nil || p.ServiceID.Type != libsignalgo.ServiceIDTypeACI {
			continue
		}
		aci := aciOf(p.ServiceID.UUID)
		c.PendingMembers = append(c.PendingMembers, core.Member{ACI: aci, Name: name(aci), Admin: p.Role == signalmeow.GroupMember_ADMINISTRATOR})
	}
	for _, r := range g.RequestingMembers {
		if r == nil {
			continue
		}
		aci := aciOf(r.ACI)
		c.RequestingJoin = append(c.RequestingJoin, core.Member{ACI: aci, Name: name(aci)})
	}
	c.AddFromLinkMode = core.AccessUnknown
	if g.AccessControl != nil {
		c.AddFromLinkMode = accessMode(g.AccessControl.AddFromInviteLink)
	}
	c.InviteLink = ""
	if c.AddFromLinkMode == core.AccessAny || c.AddFromLinkMode == core.AccessAdmin {
		c.InviteLink = inviteLink
	}
	if isPendingMember(g, self) {
		c.MessageRequest = true
	}
	return c
}

// isMember reports whether aci is a full member of g.
func isMember(g *signalmeow.Group, aci core.ACI) bool {
	for _, m := range g.Members {
		if m != nil && aciOf(m.ACI) == aci {
			return true
		}
	}
	return false
}

// isPendingMember reports whether aci was invited to g but has not accepted.
func isPendingMember(g *signalmeow.Group, aci core.ACI) bool {
	for _, p := range g.PendingMembers {
		if p != nil && p.ServiceID.Type == libsignalgo.ServiceIDTypeACI && aciOf(p.ServiceID.UUID) == aci {
			return true
		}
	}
	return false
}

// canSend reports why self may not post in c, or nil.
func canSend(c core.Chat, self core.ACI) error {
	switch {
	case c.Blocked:
		return errorf(core.ErrReadOnly, "chat is blocked")
	case c.MessageRequest:
		return errorf(core.ErrReadOnly, "accept the message request first")
	case c.Kind == core.ChatGroup && c.GroupRevision > 0 && !memberOf(c.Members, self):
		return errorf(core.ErrReadOnly, "you are not a member of this group")
	case c.OnlyAdminsSend && !c.SelfIsAdmin:
		return errorf(core.ErrReadOnly, "only admins can send messages")
	}
	return nil
}

func memberOf(ms []core.Member, aci core.ACI) bool {
	for _, m := range ms {
		if m.ACI == aci {
			return true
		}
	}
	return false
}

// storageChatState is the per-chat state the storage service carries.
type storageChatState struct {
	Chat         core.ChatID
	Archived     bool
	MarkedUnread bool
	MutedUntil   time.Time
	Blocked      bool
	Whitelisted  bool
}

// storageContact is the per-contact state the storage service carries that
// signalmeow does not keep.
type storageContact struct {
	ACI      core.ACI
	Username string
	Verified bool
	Hidden   bool
}

// storageSnapshot is warte's view of one storage service read.
type storageSnapshot struct {
	Chats       []storageChatState
	Contacts    []storageContact
	Pinned      []core.ChatID // in pin order
	HasAccount  bool
	Settings    accountSettings
	StickerPack []storagePack
	Folders     []storageFolder // custom chat folders, in display order
}

// accountSettings is the subset of the account record warte mirrors.
type accountSettings struct {
	ReadReceipts           bool
	TypingIndicators       bool
	LinkPreviews           bool
	SealedSenderIndicators bool
	UniversalExpireTimer   time.Duration
	PhoneNumberSharing     bool
	DiscoverableByPhone    bool
	Username               string
	NoteToSelfArchived     bool
	NoteToSelfMarkedUnread bool
}

// storagePack is an installed sticker pack from storage.
type storagePack struct {
	ID, Key  string // hex
	Position uint32
	Deleted  bool
}

// mutedUntil converts a storage/backup mute timestamp (ms; INT64_MAX means
// forever) to a time.
func mutedUntil(ms uint64) time.Time {
	switch {
	case ms == 0:
		return time.Time{}
	case ms >= math.MaxInt64/2:
		return foreverMuted
	}
	return time.UnixMilli(int64(ms)).UTC()
}

// foreverMuted is how "muted forever" is stored.
var foreverMuted = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)

// convertStorage interprets decrypted storage records. self is our ACI.
func convertStorage(records []*signalmeow.DecryptedStorageRecord, self core.ACI) storageSnapshot {
	var s storageSnapshot
	for _, rec := range records {
		if rec == nil || rec.StorageRecord == nil {
			continue
		}
		switch r := rec.StorageRecord.GetRecord().(type) {
		case *signalpb.StorageRecord_Contact:
			ct := r.Contact
			aci := aciFromBinaryOrString(ct.GetAciBinary(), ct.GetAci())
			if aci == "" {
				continue
			}
			s.Chats = append(s.Chats, storageChatState{
				Chat:         core.DirectChatID(aci),
				Archived:     ct.GetArchived(),
				MarkedUnread: ct.GetMarkedUnread(),
				MutedUntil:   mutedUntil(ct.GetMutedUntilTimestamp()),
				Blocked:      ct.GetBlocked(),
				Whitelisted:  ct.GetWhitelisted(),
			})
			s.Contacts = append(s.Contacts, storageContact{
				ACI:      aci,
				Username: ct.GetUsername(),
				Verified: ct.GetIdentityState() == signalpb.ContactRecord_VERIFIED,
				Hidden:   ct.GetHidden(),
			})
		case *signalpb.StorageRecord_GroupV2:
			mk := r.GroupV2.GetMasterKey()
			if len(mk) != libsignalgo.GroupMasterKeyLength {
				continue
			}
			gid, err := libsignalgo.GroupMasterKey(mk).GroupIdentifier()
			if err != nil {
				continue
			}
			s.Chats = append(s.Chats, storageChatState{
				Chat:         core.GroupChatID(base64.StdEncoding.EncodeToString(gid[:])),
				Archived:     r.GroupV2.GetArchived(),
				MarkedUnread: r.GroupV2.GetMarkedUnread(),
				MutedUntil:   mutedUntil(r.GroupV2.GetMutedUntilTimestamp()),
				Blocked:      r.GroupV2.GetBlocked(),
				Whitelisted:  r.GroupV2.GetWhitelisted(),
			})
		case *signalpb.StorageRecord_Account:
			a := r.Account
			s.HasAccount = true
			s.Settings = accountSettings{
				ReadReceipts:           a.GetReadReceipts(),
				TypingIndicators:       a.GetTypingIndicators(),
				LinkPreviews:           a.GetLinkPreviews(),
				SealedSenderIndicators: a.GetSealedSenderIndicators(),
				UniversalExpireTimer:   time.Duration(a.GetUniversalExpireTimer()) * time.Second,
				PhoneNumberSharing:     a.GetPhoneNumberSharingMode() == signalpb.AccountRecord_EVERYBODY,
				DiscoverableByPhone:    !a.GetUnlistedPhoneNumber(),
				Username:               a.GetUsername(),
				NoteToSelfArchived:     a.GetNoteToSelfArchived(),
				NoteToSelfMarkedUnread: a.GetNoteToSelfMarkedUnread(),
			}
			if self != "" {
				s.Chats = append(s.Chats, storageChatState{
					Chat:         core.DirectChatID(self),
					Archived:     a.GetNoteToSelfArchived(),
					MarkedUnread: a.GetNoteToSelfMarkedUnread(),
					Whitelisted:  true,
				})
			}
			for _, p := range a.GetPinnedConversations() {
				if id, ok := pinnedChatID(p); ok {
					s.Pinned = append(s.Pinned, id)
				}
			}
		case *signalpb.StorageRecord_StickerPack:
			sp := r.StickerPack
			if len(sp.GetPackId()) == 0 {
				continue
			}
			s.StickerPack = append(s.StickerPack, storagePack{
				ID:       hexString(sp.GetPackId()),
				Key:      hexString(sp.GetPackKey()),
				Position: sp.GetPosition(),
				Deleted:  sp.GetDeletedAtTimestamp() > 0,
			})
		case *signalpb.StorageRecord_ChatFolder:
			if f, ok := convertChatFolder(r.ChatFolder); ok {
				s.Folders = append(s.Folders, f)
			}
		}
	}
	sortStorageFolders(s.Folders)
	return s
}

// storageFolderPrefix marks folder IDs that come from the storage service.
const storageFolderPrefix = "sf:"

// storageFolder is a custom chat folder from the storage service.
type storageFolder struct {
	Folder   core.Folder
	Position uint32
}

// convertChatFolder maps a ChatFolderRecord to a folder. The "All chats"
// folder (warte has its own) and deleted folders are skipped.
func convertChatFolder(r *signalpb.ChatFolderRecord) (storageFolder, bool) {
	if r == nil || len(r.GetIdentifier()) == 0 || r.GetDeletedAtTimestampMs() != 0 ||
		r.GetFolderType() == signalpb.ChatFolderRecord_ALL || strings.TrimSpace(r.GetName()) == "" {
		return storageFolder{}, false
	}
	id := storageFolderPrefix + hexString(r.GetIdentifier())
	m := &core.FolderMembers{
		AllDirect: r.GetIncludeAllIndividualChats(),
		AllGroups: r.GetIncludeAllGroupChats(),
		HideMuted: !r.GetShowMutedChats(),
	}
	for _, rc := range r.GetIncludedRecipients() {
		if c, ok := recipientChatID(rc); ok {
			m.Include = append(m.Include, c)
		}
	}
	for _, rc := range r.GetExcludedRecipients() {
		if c, ok := recipientChatID(rc); ok {
			m.Exclude = append(m.Exclude, c)
		}
	}
	return storageFolder{
		Folder: core.Folder{ID: id, Name: r.GetName(), Filter: core.ChatFilter{
			FolderID: id, UnreadOnly: r.GetShowOnlyUnread(), Members: m,
		}},
		Position: r.GetPosition(),
	}, true
}

// sortStorageFolders orders folders by position, then ID.
func sortStorageFolders(fs []storageFolder) {
	slices.SortStableFunc(fs, func(a, b storageFolder) int {
		if a.Position != b.Position {
			return cmp.Compare(a.Position, b.Position)
		}
		return strings.Compare(a.Folder.ID, b.Folder.ID)
	})
}

// recipientChatID maps a storage service Recipient to a chat ID (ACIs and
// v2 groups; PNI-only contacts and legacy groups are skipped).
func recipientChatID(r *signalpb.Recipient) (core.ChatID, bool) {
	switch id := r.GetIdentifier().(type) {
	case *signalpb.Recipient_Contact_:
		aci := aciFromBinaryOrString(id.Contact.GetServiceIdBinary(), id.Contact.GetServiceId())
		if aci == "" {
			return "", false
		}
		return core.DirectChatID(aci), true
	case *signalpb.Recipient_GroupMasterKey:
		if len(id.GroupMasterKey) != libsignalgo.GroupMasterKeyLength {
			return "", false
		}
		gid, err := libsignalgo.GroupMasterKey(id.GroupMasterKey).GroupIdentifier()
		if err != nil {
			return "", false
		}
		return core.GroupChatID(base64.StdEncoding.EncodeToString(gid[:])), true
	}
	return "", false
}

// pinnedChatID maps an account record pinned conversation to a chat ID.
func pinnedChatID(p *signalpb.AccountRecord_PinnedConversation) (core.ChatID, bool) {
	switch id := p.GetIdentifier().(type) {
	case *signalpb.AccountRecord_PinnedConversation_Contact_:
		sid := id.Contact.GetServiceId()
		var bin []byte = id.Contact.GetServiceIdBinary()
		aci := aciFromBinaryOrString(bin, sid)
		if aci == "" {
			return "", false
		}
		return core.DirectChatID(aci), true
	case *signalpb.AccountRecord_PinnedConversation_GroupMasterKey:
		if len(id.GroupMasterKey) != libsignalgo.GroupMasterKeyLength {
			return "", false
		}
		gid, err := libsignalgo.GroupMasterKey(id.GroupMasterKey).GroupIdentifier()
		if err != nil {
			return "", false
		}
		return core.GroupChatID(base64.StdEncoding.EncodeToString(gid[:])), true
	}
	return "", false
}

// receiptStatus maps a receipt type to a delivery status.
func receiptStatus(t signalpb.ReceiptMessage_Type) core.DeliveryStatus {
	switch t {
	case signalpb.ReceiptMessage_READ:
		return core.StatusRead
	case signalpb.ReceiptMessage_VIEWED:
		return core.StatusViewed
	}
	return core.StatusDelivered
}

// selfContact builds the self contact from device data and the profile.
func selfContact(aci uuid.UUID, number string, r *types.Recipient, existing core.Contact) core.Contact {
	c := existing
	if r != nil {
		c = recipientToContact(r, existing, aciOf(aci))
	}
	c.ACI = aciOf(aci)
	if number != "" {
		c.E164 = number
	}
	c.IsSelf = true
	return c
}
