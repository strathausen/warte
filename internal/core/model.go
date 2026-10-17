// Package core defines warte's UI-agnostic domain model and the Service
// contract between the Signal backend and any frontend (the TUI, tests, the
// demo/fake backend). Nothing in this package imports Signal or UI libraries.
package core

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ACI is a Signal account identifier (a UUID string, lowercase, no prefix).
type ACI string

// ChatID identifies a conversation.
//
//	"u:<aci>"            direct chat (Note to Self is a direct chat with your own ACI)
//	"g:<base64 groupid>" group v2 chat
type ChatID string

func DirectChatID(aci ACI) ChatID       { return ChatID("u:" + string(aci)) }
func GroupChatID(groupID string) ChatID { return ChatID("g:" + groupID) }

func (c ChatID) IsGroup() bool  { return strings.HasPrefix(string(c), "g:") }
func (c ChatID) IsDirect() bool { return strings.HasPrefix(string(c), "u:") }

// ACI returns the peer ACI of a direct chat, or "" for groups.
func (c ChatID) ACI() ACI {
	if !c.IsDirect() {
		return ""
	}
	return ACI(strings.TrimPrefix(string(c), "u:"))
}

// GroupID returns the base64 group identifier of a group chat, or "".
func (c ChatID) GroupID() string {
	if !c.IsGroup() {
		return ""
	}
	return strings.TrimPrefix(string(c), "g:")
}

// MessageID identifies a message the way Signal does: by author and the
// author's sent timestamp (ms since epoch). Together with the chat it is unique.
type MessageID struct {
	Chat      ChatID
	Author    ACI
	Timestamp uint64
}

func (m MessageID) IsZero() bool { return m.Timestamp == 0 }

// String encodes the ID as "<chat>|<author>|<ts>"; ParseMessageID reverses it.
func (m MessageID) String() string {
	return fmt.Sprintf("%s|%s|%d", m.Chat, m.Author, m.Timestamp)
}

func ParseMessageID(s string) (MessageID, error) {
	parts := strings.Split(s, "|")
	if len(parts) != 3 {
		return MessageID{}, fmt.Errorf("invalid message id %q", s)
	}
	ts, err := strconv.ParseUint(parts[2], 10, 64)
	if err != nil {
		return MessageID{}, fmt.Errorf("invalid message id %q: %w", s, err)
	}
	return MessageID{Chat: ChatID(parts[0]), Author: ACI(parts[1]), Timestamp: ts}, nil
}

type ChatKind int

const (
	ChatDirect ChatKind = iota
	ChatGroup
	ChatNoteToSelf
)

// Chat is a conversation as shown in the chat list.
type Chat struct {
	ID          ChatID
	Kind        ChatKind
	Title       string // contact display name or group title
	Description string // group description or contact "about"
	AvatarID    string // opaque key for Service.Avatar; "" = none
	Color       string // Signal avatar/chat color name, e.g. "A100"; may be ""

	Unread         int
	MentionsUnread int
	MarkedUnread   bool
	Archived       bool
	Pinned         bool
	PinOrder       int       // lower = higher in list; only meaningful if Pinned
	MutedUntil     time.Time // zero = not muted; far future = muted forever
	Blocked        bool
	MessageRequest bool // unaccepted incoming request
	ExpireTimer    time.Duration

	LastMessage *MessagePreview
	Draft       string

	// Group-only
	Members         []Member
	PendingMembers  []Member
	RequestingJoin  []Member
	OnlyAdminsSend  bool // announcement-only group
	InviteLink      string
	SelfIsAdmin     bool
	GroupRevision   uint32
	AddFromLinkMode AccessMode

	// Direct-only
	Contact *Contact
}

func (c Chat) Muted(now time.Time) bool { return now.Before(c.MutedUntil) }

type AccessMode int

const (
	AccessUnknown AccessMode = iota
	AccessAny
	AccessMember
	AccessAdmin
	AccessUnsatisfiable
)

type Member struct {
	ACI   ACI
	Name  string
	Admin bool
}

type MessagePreview struct {
	ID       MessageID
	Sender   string // display name; "" for own messages ("You")
	Outgoing bool
	Text     string // single-line snippet; attachments rendered as "📷 Photo" etc.
	At       time.Time
	Status   DeliveryStatus
}

type Contact struct {
	ACI         ACI
	E164        string // "+4915…" may be "" (phone number privacy)
	Username    string // "name.42"
	ProfileName string
	ContactName string // name from the phone's address book
	Nickname    string
	About       string
	AboutEmoji  string
	AvatarID    string
	Verified    bool
	Blocked     bool
	IsSelf      bool
}

// DisplayName follows Signal's precedence: nickname > contact > profile > username > phone.
func (c Contact) DisplayName() string {
	for _, s := range []string{c.Nickname, c.ContactName, c.ProfileName, c.Username, c.E164} {
		if s != "" {
			return s
		}
	}
	if c.ACI != "" {
		return "Unknown"
	}
	return ""
}

type DeliveryStatus int

const (
	StatusNone DeliveryStatus = iota // incoming messages
	StatusPending
	StatusSent
	StatusDelivered
	StatusRead
	StatusViewed
	StatusFailed
)

type MessageKind int

const (
	KindNormal     MessageKind = iota
	KindSystem                 // group update, timer change, safety number change, profile change … (SystemText)
	KindCall                   // call event (Call)
	KindStoryReply             // reply to a story (StoryContext)
)

// Message is one entry in a conversation.
type Message struct {
	ID         MessageID
	Kind       MessageKind
	SenderName string // resolved display name of ID.Author at load time
	Outgoing   bool
	SentAt     time.Time
	ReceivedAt time.Time

	Body   string
	Ranges []BodyRange // formatting + mentions; offsets in UTF-16 code units, like Signal

	Quote       *Quote
	Attachments []Attachment
	Sticker     *Sticker
	Preview     *LinkPreview
	Contact     *SharedContact
	Poll        *Poll
	Reactions   []Reaction

	Edited          bool
	EditHistory     []EditRevision // oldest first; excludes current
	RemoteDeleted   bool           // "This message was deleted"
	ViewOnce        bool
	ViewOnceOpened  bool
	ExpiresIn       time.Duration // 0 = not disappearing
	ExpireStartedAt time.Time     // zero until read (incoming) / sent (outgoing)
	Pinned          bool

	Status          DeliveryStatus
	PerRecipient    map[ACI]DeliveryStatus // groups only, may be nil
	SystemText      string
	Call            *CallInfo
	StoryContext    *StoryRef
	MentionsSelf    bool
	GiftBadge       bool
	PaymentNotice   string // read-only payment summary if any
	ServerTimestamp uint64
}

// ExpiresAt is the zero time if the message isn't counting down.
func (m Message) ExpiresAt() time.Time {
	if m.ExpiresIn == 0 || m.ExpireStartedAt.IsZero() {
		return time.Time{}
	}
	return m.ExpireStartedAt.Add(m.ExpiresIn)
}

type EditRevision struct {
	Body   string
	Ranges []BodyRange
	At     time.Time
}

type RangeStyle int

const (
	StyleNone RangeStyle = iota
	StyleBold
	StyleItalic
	StyleSpoiler
	StyleStrikethrough
	StyleMonospace
	StyleMention // MentionACI set; Body contains U+FFFC placeholder at Start (Length 1)
)

type BodyRange struct {
	Start      int // UTF-16 code units
	Length     int // UTF-16 code units
	Style      RangeStyle
	MentionACI ACI
}

type Quote struct {
	ID            MessageID
	AuthorName    string
	Text          string
	Ranges        []BodyRange
	Attachment    *Attachment // first quoted attachment thumbnail, if any
	OriginalFound bool
}

// Attachment metadata. Bytes are fetched through Service.Attachment.
type Attachment struct {
	ID          string // opaque, stable key for Service.Attachment
	ContentType string
	FileName    string
	Size        int64
	Width       int
	Height      int
	Caption     string
	BlurHash    string
	VoiceNote   bool
	Borderless  bool // stickers-like
	GIF         bool
	Downloaded  bool    // bytes available locally
	Progress    float64 // 0..1 while downloading
	Failed      bool
}

func (a Attachment) IsImage() bool { return strings.HasPrefix(a.ContentType, "image/") }
func (a Attachment) IsVideo() bool { return strings.HasPrefix(a.ContentType, "video/") }
func (a Attachment) IsAudio() bool { return strings.HasPrefix(a.ContentType, "audio/") }

type Sticker struct {
	PackID     string // hex
	PackKey    string // hex
	StickerID  uint32
	Emoji      string
	Attachment Attachment
}

type StickerPack struct {
	ID        string
	Key       string
	Title     string
	Author    string
	Installed bool
	Cover     *Sticker
	Stickers  []Sticker
}

type LinkPreview struct {
	URL         string
	Title       string
	Description string
	Date        time.Time
	Image       *Attachment
}

type SharedContact struct {
	Name   string
	Phones []string
	Emails []string
	Org    string
}

type Reaction struct {
	Emoji    string
	From     ACI
	FromName string
	At       time.Time
}

type Poll struct {
	Question      string
	Options       []PollOption
	AllowMultiple bool
	Ended         bool
}

type PollOption struct {
	Text   string
	Voters []ACI
}

type CallInfo struct {
	Video     bool
	Group     bool
	Incoming  bool
	Missed    bool
	Declined  bool
	Duration  time.Duration
	StartedAt time.Time
}

type StoryRef struct {
	Author    ACI
	Timestamp uint64
	Emoji     string // non-empty for story reactions
}

type Story struct {
	ID         MessageID
	AuthorName string
	At         time.Time
	Text       *TextStory
	Media      *Attachment
	Caption    string
	Viewed     bool
	Group      ChatID // non-empty if posted to a group
	Replies    int
}

type TextStory struct {
	Text       string
	Style      int    // Signal TextAttachment.Style
	Foreground uint32 // ARGB
	Background uint32 // ARGB; or gradient start
	Gradient   []uint32
	URL        string
}

type CallLogEntry struct {
	Chat   ChatID
	Title  string
	Call   CallInfo
	Unread bool
}

type SafetyNumber struct {
	Digits   string // 60 digits, grouped by caller
	QRData   []byte // scannable fingerprint for QR display
	Verified bool
}

// SearchHit is a full-text search result.
type SearchHit struct {
	Message   Message
	ChatTitle string
	Snippet   string // body with match context
	Matches   [][2]int
}

type SearchQuery struct {
	Text    string
	Chat    ChatID // "" = all chats
	From    ACI
	HasType string // "image", "video", "audio", "file", "link", ""
	Before  time.Time
	After   time.Time
	Limit   int
}

type ConnState int

const (
	ConnDisconnected ConnState = iota
	ConnConnecting
	ConnConnected
	ConnLoggedOut
)

func (s ConnState) String() string {
	switch s {
	case ConnConnecting:
		return "connecting"
	case ConnConnected:
		return "connected"
	case ConnLoggedOut:
		return "logged out"
	}
	return "offline"
}
