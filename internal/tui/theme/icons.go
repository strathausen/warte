package theme

import "warte/internal/core"

// IconSet is a set of glyphs used throughout the TUI. Use Icons to obtain the
// Nerd Font or the plain-Unicode variant. Every glyph is at most two cells
// wide.
type IconSet struct {
	Pinned      string
	Muted       string
	Archived    string
	Group       string
	NoteToSelf  string
	Attachment  string
	Image       string
	Video       string
	Audio       string
	Voice       string
	File        string
	Sticker     string
	Link        string
	Poll        string
	Lock        string
	Verified    string
	Check       string // sent
	DoubleCheck string // delivered / read
	Clock       string // pending
	Failed      string
	Reply       string
	Edit        string
	Delete      string
	Star        string
	Search      string
	Story       string
	Call        string
	VideoCall   string
	MissedCall  string
	Mention     string
	Unread      string // dot marking a chat as unread
	Cursor      string // marker in front of the selected row
	Ellipsis    string
	// Typing holds the animation frames of the typing indicator; all frames
	// share the same display width.
	Typing []string
}

var nerdIcons = IconSet{
	Pinned:      "",          // nf-fa-thumb_tack
	Muted:       "",          // nf-fa-bell_slash
	Archived:    "",          // nf-fa-archive
	Group:       "",          // nf-fa-users
	NoteToSelf:  "",          // nf-fa-sticky_note
	Attachment:  "",          // nf-fa-paperclip
	Image:       "",          // nf-fa-image
	Video:       "",          // nf-fa-film
	Audio:       "",          // nf-fa-music
	Voice:       "",          // nf-fa-microphone
	File:        "",          // nf-fa-file
	Sticker:     "",          // nf-fa-smile_o
	Link:        "",          // nf-fa-link
	Poll:        "",          // nf-fa-bar_chart
	Lock:        "",          // nf-fa-lock
	Verified:    "",          // nf-fa-check_circle
	Check:       "",          // nf-fa-check
	DoubleCheck: "\U000f012d", // nf-md-check_all
	Clock:       "",          // nf-fa-clock_o
	Failed:      "",          // nf-fa-exclamation_circle
	Reply:       "",          // nf-fa-reply
	Edit:        "",          // nf-fa-pencil
	Delete:      "",          // nf-fa-trash
	Star:        "",          // nf-fa-star
	Search:      "",          // nf-fa-search
	Story:       "",          // nf-fa-circle_o_notch
	Call:        "",          // nf-fa-phone
	VideoCall:   "",          // nf-fa-video_camera
	MissedCall:  "\U000f03fa", // nf-md-phone_missed
	Mention:     "",          // nf-fa-at
	Unread:      "",          // nf-fa-circle
	Cursor:      "▌",          // left half block
	Ellipsis:    "…",
	Typing:      []string{"●∙∙", "∙●∙", "∙∙●", "∙●∙"},
}

var unicodeIcons = IconSet{
	Pinned:      "⚲",
	Muted:       "⊘",
	Archived:    "▤",
	Group:       "⚇",
	NoteToSelf:  "✎",
	Attachment:  "⎘",
	Image:       "▣",
	Video:       "▶",
	Audio:       "♫",
	Voice:       "♪",
	File:        "▭",
	Sticker:     "☺",
	Link:        "↗",
	Poll:        "≡",
	Lock:        "⚿",
	Verified:    "✔",
	Check:       "✓",
	DoubleCheck: "✓✓",
	Clock:       "◷",
	Failed:      "✗",
	Reply:       "↩",
	Edit:        "✎",
	Delete:      "⌫",
	Star:        "★",
	Search:      "⌕",
	Story:       "◌",
	Call:        "☏",
	VideoCall:   "◙",
	MissedCall:  "↙",
	Mention:     "@",
	Unread:      "●",
	Cursor:      "▌",
	Ellipsis:    "…",
	Typing:      []string{"●∙∙", "∙●∙", "∙∙●", "∙●∙"},
}

// Icons returns the Nerd Font glyph set when nerd is true, otherwise a
// plain-Unicode fallback that renders in any modern terminal font. The
// returned value is a copy and may be modified freely.
func Icons(nerd bool) IconSet {
	src := unicodeIcons
	if nerd {
		src = nerdIcons
	}
	src.Typing = append([]string(nil), src.Typing...)
	return src
}

// TypingFrame returns the typing-indicator frame for animation tick n.
func (i IconSet) TypingFrame(n int) string {
	if len(i.Typing) == 0 {
		return ""
	}
	k := n % len(i.Typing)
	if k < 0 {
		k = -k // mirror negative ticks; safe even for math.MinInt
	}
	return i.Typing[k]
}

// Delivery returns the glyph for an outgoing message's delivery status, or ""
// for incoming messages (core.StatusNone) and unknown values.
func (i IconSet) Delivery(st core.DeliveryStatus) string {
	switch st {
	case core.StatusPending:
		return i.Clock
	case core.StatusSent:
		return i.Check
	case core.StatusDelivered, core.StatusRead, core.StatusViewed:
		return i.DoubleCheck
	case core.StatusFailed:
		return i.Failed
	default:
		return ""
	}
}
