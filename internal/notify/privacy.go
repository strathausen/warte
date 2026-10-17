package notify

import (
	"strconv"

	"warte/internal/core"
)

// Fallback texts used by Compose.
const (
	TextNewMessage    = "New message"
	TextAttachment    = "Sent a message"
	textNewMessages   = "new messages"
	unknownSenderName = "Unknown"
)

// Compose builds a notification for one incoming message according to the
// user's privacy level, mirroring Signal Desktop:
//
//   - NotifyNameAndMessage: title is the sender ("Alice", or
//     "Alice @ Family" in a group), body is the message text.
//   - NotifyNameOnly: title is "New message from Alice" (or
//     "New message from Alice in Family"), no body.
//   - NotifyNone: title is "New message", no body.
//
// The returned Notification has no ChatID; callers set it. Text is not
// sanitized here; notifiers sanitize on delivery.
func Compose(level core.NotificationLevel, sender, chatTitle, body string, isGroup bool) Notification {
	sender = displayName(sender)
	chatTitle = Sanitize(chatTitle, MaxTitleRunes)
	switch level {
	case core.NotifyNameAndMessage:
		if Sanitize(body, 1) == "" {
			body = TextAttachment
		}
		return Notification{Title: senderLabel(sender, chatTitle, isGroup), Body: body}
	case core.NotifyNameOnly:
		title := TextNewMessage + " from " + sender
		if isGroup && chatTitle != "" {
			title += " in " + chatTitle
		}
		return Notification{Title: title}
	default:
		return Notification{Title: TextNewMessage}
	}
}

// ComposeBatch builds the summary notification for count messages from a
// single chat, e.g. title "Alice", body "3 new messages". In a group the
// title is the group name. With NotifyNone the title is just
// "3 new messages". For count <= 1 it is equivalent to Compose with an
// empty body under the name-only rules.
func ComposeBatch(level core.NotificationLevel, sender, chatTitle string, isGroup bool, count int) Notification {
	if count <= 1 {
		if level == core.NotifyNameAndMessage {
			level = core.NotifyNameOnly
		}
		return Compose(level, sender, chatTitle, "", isGroup)
	}
	summary := strconv.Itoa(count) + " " + textNewMessages
	if level != core.NotifyNameAndMessage && level != core.NotifyNameOnly {
		return Notification{Title: summary}
	}
	title := displayName(sender)
	if isGroup {
		if ct := Sanitize(chatTitle, MaxTitleRunes); ct != "" {
			title = ct
		}
	}
	return Notification{Title: title, Body: summary}
}

func displayName(sender string) string {
	sender = Sanitize(sender, MaxTitleRunes)
	if sender == "" {
		return unknownSenderName
	}
	return sender
}

func senderLabel(sender, chatTitle string, isGroup bool) string {
	if isGroup && chatTitle != "" {
		return sender + " @ " + chatTitle
	}
	return sender
}
