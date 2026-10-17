package core

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound    = errors.New("not found")
	ErrNotLoggedIn = errors.New("not linked to a Signal account")
	ErrReadOnly    = errors.New("not permitted in this chat")
)

// Service is everything a frontend may ask of the backend. Implementations:
// internal/backend (real Signal via signalmeow) and internal/fake (demo/tests).
//
// All methods are safe for concurrent use. Query methods never block on the
// network: they read local state. Action methods may block on the network and
// must honour ctx. State changes, including ones caused by the frontend's own
// actions, are announced on Events().
type Service interface {
	// Lifecycle
	Start(ctx context.Context) error // connect and begin receiving; returns once the store is loaded
	Close() error
	Events() <-chan Event // single consumer; never closed before Close
	Self() Contact
	ConnState() ConnState

	// Queries (local state)
	Chats(ctx context.Context, f ChatFilter) ([]Chat, error) // sorted: pinned by PinOrder, then by last activity desc
	Chat(ctx context.Context, id ChatID) (Chat, error)
	// Messages returns up to limit messages older than `before` (zero = newest),
	// sorted oldest → newest.
	Messages(ctx context.Context, chat ChatID, before MessageID, limit int) ([]Message, error)
	// MessagesAround returns a window centred on id, oldest → newest (for jump-to-quote/search).
	MessagesAround(ctx context.Context, id MessageID, limit int) ([]Message, error)
	Message(ctx context.Context, id MessageID) (Message, error)
	Search(ctx context.Context, q SearchQuery) ([]SearchHit, error)
	Contacts(ctx context.Context) ([]Contact, error)
	Contact(ctx context.Context, aci ACI) (Contact, error)
	Stories(ctx context.Context) ([]Story, error)
	CallLog(ctx context.Context) ([]CallLogEntry, error)
	StickerPacks(ctx context.Context) ([]StickerPack, error)
	Folders(ctx context.Context) ([]Folder, error)
	Settings(ctx context.Context) (Settings, error)

	// Media. Bytes are returned decrypted in memory; implementations must not
	// write plaintext to disk. Attachment triggers a download if needed.
	Attachment(ctx context.Context, msg MessageID, attachmentID string) ([]byte, error)
	Avatar(ctx context.Context, avatarID string) ([]byte, error)
	StickerImage(ctx context.Context, packID string, stickerID uint32) ([]byte, error)

	// Messaging
	Send(ctx context.Context, chat ChatID, out Outgoing) (MessageID, error)
	Edit(ctx context.Context, id MessageID, body string, ranges []BodyRange) error
	Delete(ctx context.Context, id MessageID, forEveryone bool) error
	React(ctx context.Context, id MessageID, emoji string, remove bool) error
	Forward(ctx context.Context, id MessageID, to []ChatID) error
	MarkRead(ctx context.Context, chat ChatID, upTo MessageID) error
	MarkViewOnceOpened(ctx context.Context, id MessageID) error
	SetTyping(ctx context.Context, chat ChatID, typing bool) error
	SaveDraft(ctx context.Context, chat ChatID, draft string) error
	VotePoll(ctx context.Context, id MessageID, options []int) error
	EndPoll(ctx context.Context, id MessageID) error
	PinMessage(ctx context.Context, id MessageID, pin bool, duration time.Duration) error

	// Chat management
	UpdateChat(ctx context.Context, id ChatID, u ChatUpdate) error
	DeleteChat(ctx context.Context, id ChatID) error
	AcceptMessageRequest(ctx context.Context, id ChatID) error
	Block(ctx context.Context, id ChatID, block bool) error
	ReportSpam(ctx context.Context, id ChatID) error
	// ResolveRecipient turns a phone number (+E164), username (name.42),
	// username link or contact name into a direct chat, creating it if needed.
	ResolveRecipient(ctx context.Context, query string) (ChatID, error)

	// Groups
	CreateGroup(ctx context.Context, title string, members []ACI, avatar []byte) (ChatID, error)
	UpdateGroup(ctx context.Context, id ChatID, u GroupUpdate) error
	LeaveGroup(ctx context.Context, id ChatID) error
	JoinGroupByLink(ctx context.Context, link string) (ChatID, error)

	// Contacts & safety
	SafetyNumber(ctx context.Context, aci ACI) (SafetyNumber, error)
	SetVerified(ctx context.Context, aci ACI, verified bool) error
	SetNickname(ctx context.Context, aci ACI, nickname string) error
	UpdateProfile(ctx context.Context, p ProfileUpdate) error

	// Stories
	MarkStoryViewed(ctx context.Context, id MessageID) error
	ReplyToStory(ctx context.Context, id MessageID, body string, emoji string) error
	PostStory(ctx context.Context, s OutgoingStory) error

	// Stickers
	InstallStickerPack(ctx context.Context, packID, packKey string, install bool) error

	// Account
	UpdateSettings(ctx context.Context, s Settings) error
	Unlink(ctx context.Context) error
	ExportBackup(ctx context.Context, path string) error
}

// Linker performs device linking (provisioning). It is separate from Service
// because it runs before an account exists.
type Linker interface {
	// Link starts provisioning. The channel yields a LinkQR (render it as a QR
	// code for the phone to scan), then exactly one of LinkDone or LinkError.
	Link(ctx context.Context, deviceName string) (<-chan LinkEvent, error)
}

type LinkEvent interface{ isLinkEvent() }

type LinkQR struct{ URL string }
type LinkProgress struct{ Stage string }
type LinkDone struct{ Self Contact }
type LinkError struct{ Err error }

func (LinkQR) isLinkEvent()       {}
func (LinkProgress) isLinkEvent() {}
func (LinkDone) isLinkEvent()     {}
func (LinkError) isLinkEvent()    {}

type ChatFilter struct {
	Archived       bool // true = only archived; false = only non-archived
	UnreadOnly     bool
	GroupsOnly     bool
	DirectOnly     bool
	FolderID       string // "" = all
	Query          string // fuzzy title filter, optional
	IncludeEmpty   bool   // include chats with no messages (contacts)
	IncludeBlocked bool
	// Members restricts the result to a Signal chat folder's chats; nil =
	// no restriction. Backends set it on the folders they sync.
	Members *FolderMembers
}

// FolderMembers is the membership rule of a Signal chat folder (storage
// service ChatFolderRecord): all 1:1 chats and/or all groups, plus chats
// included by name, minus excluded ones, optionally without muted chats.
type FolderMembers struct {
	AllDirect bool // every 1:1 chat, Note to Self included
	AllGroups bool
	Include   []ChatID
	Exclude   []ChatID
	HideMuted bool
}

// Match reports whether c belongs to the folder at time now.
func (m *FolderMembers) Match(c Chat, now time.Time) bool {
	if m == nil {
		return true
	}
	if m.HideMuted && c.Muted(now) {
		return false
	}
	for _, id := range m.Exclude {
		if id == c.ID {
			return false
		}
	}
	for _, id := range m.Include {
		if id == c.ID {
			return true
		}
	}
	if c.Kind == ChatGroup {
		return m.AllGroups
	}
	return m.AllDirect
}

// Outgoing is a message to send. Exactly one of Body/Attachments/Sticker/Poll
// must be non-empty (Body may accompany Attachments as caption).
type Outgoing struct {
	Body        string
	Ranges      []BodyRange
	Quote       *MessageID
	Attachments []OutgoingAttachment
	Sticker     *Sticker
	Preview     *LinkPreview // nil = backend may generate one if link previews are enabled
	Poll        *Poll
	ViewOnce    bool
}

type OutgoingAttachment struct {
	FileName    string
	ContentType string // "" = sniff
	Data        []byte
	VoiceNote   bool
	Width       int
	Height      int
	Caption     string
}

type OutgoingStory struct {
	Text  *TextStory
	Media *OutgoingAttachment
	To    []ChatID // groups; empty = "My Story"
}

// ChatUpdate changes local/synced chat state. Nil fields are left unchanged.
type ChatUpdate struct {
	Archived     *bool
	Pinned       *bool
	MarkedUnread *bool
	MutedUntil   *time.Time
	ExpireTimer  *time.Duration
}

type GroupUpdate struct {
	Title          *string
	Description    *string
	Avatar         []byte
	AddMembers     []ACI
	RemoveMembers  []ACI
	Promote        []ACI
	Demote         []ACI
	Approve        []ACI
	Deny           []ACI
	OnlyAdminsSend *bool
	LinkMode       *AccessMode // AccessUnsatisfiable disables the invite link
	ResetLink      bool
	ExpireTimer    *time.Duration
}

type ProfileUpdate struct {
	GivenName  *string
	FamilyName *string
	About      *string
	AboutEmoji *string
	Avatar     []byte // nil = unchanged; empty non-nil = remove
}

type Folder struct {
	ID     string
	Name   string
	Filter ChatFilter
}

// Settings mirrors the subset of Signal Desktop settings that apply to warte.
type Settings struct {
	ReadReceipts        bool
	TypingIndicators    bool
	LinkPreviews        bool
	SealedSenderNotice  bool
	DefaultExpireTimer  time.Duration
	NotificationLevel   NotificationLevel
	PhoneNumberSharing  bool // who can see my number: everybody vs nobody
	DiscoverableByPhone bool
}

type NotificationLevel int

const (
	NotifyNameAndMessage NotificationLevel = iota
	NotifyNameOnly
	NotifyNone
)

// Event is a backend → frontend notification. Frontends should treat events as
// hints and re-query state where convenient.
type Event interface{ isEvent() }

type (
	MessageAdded struct {
		Message Message
		Notify  bool // new incoming message that should trigger a notification
	}
	MessageUpdated struct{ Message Message } // edits, reactions, receipts, downloads …
	MessageRemoved struct{ ID MessageID }    // delete-for-me / expiry
	ChatUpdated    struct{ Chat Chat }
	ChatRemoved    struct{ ID ChatID }
	TypingChanged  struct {
		Chat   ChatID
		Who    ACI
		Name   string
		Typing bool
	}
	ConnChanged  struct{ State ConnState }
	SyncProgress struct {
		Stage string // "contacts", "groups", "history", "storage"
		Done  int
		Total int // 0 = unknown
	}
	IncomingCall struct {
		Chat    ChatID
		From    ACI
		Name    string
		Video   bool
		Ringing bool // false = ended/answered elsewhere
	}
	ContactUpdated  struct{ Contact Contact }
	StoriesChanged  struct{}
	IdentityChanged struct {
		ACI  ACI
		Name string
	}
	LoggedOut  struct{ Err error }
	ErrorEvent struct {
		Op  string
		Err error
	}
)

func (MessageAdded) isEvent()    {}
func (MessageUpdated) isEvent()  {}
func (MessageRemoved) isEvent()  {}
func (ChatUpdated) isEvent()     {}
func (ChatRemoved) isEvent()     {}
func (TypingChanged) isEvent()   {}
func (ConnChanged) isEvent()     {}
func (SyncProgress) isEvent()    {}
func (IncomingCall) isEvent()    {}
func (ContactUpdated) isEvent()  {}
func (StoriesChanged) isEvent()  {}
func (IdentityChanged) isEvent() {}
func (LoggedOut) isEvent()       {}
func (ErrorEvent) isEvent()      {}
