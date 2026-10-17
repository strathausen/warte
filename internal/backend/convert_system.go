package backend

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.mau.fi/mautrix-signal/pkg/libsignalgo"
	"go.mau.fi/mautrix-signal/pkg/signalmeow"

	"warte/internal/core"
)

// nameFunc resolves an ACI to a display name. It must return something
// non-empty (e.g. "Unknown") for unknown ACIs.
type nameFunc func(core.ACI) string

// actor returns how a sentence should name who did something: "You" for
// self, the display name otherwise, "Someone" for unknown actors.
func actor(who, self core.ACI, name nameFunc) string {
	switch {
	case who == "":
		return "Someone"
	case who == self:
		return "You"
	}
	return name(who)
}

// object returns how a sentence should name who something happened to:
// "you" for self, the display name otherwise.
func object(who, self core.ACI, name nameFunc) string {
	if who != "" && who == self {
		return "you"
	}
	if who == "" {
		return "someone"
	}
	return name(who)
}

// formatTimer renders a disappearing-message duration the way Signal does:
// the largest unit that divides it evenly ("1 week", "8 hours", "30 seconds").
func formatTimer(d time.Duration) string {
	if d <= 0 {
		return "off"
	}
	d = max(d.Truncate(time.Second), time.Second) // Signal timers are whole seconds
	units := []struct {
		d    time.Duration
		name string
	}{
		{7 * 24 * time.Hour, "week"},
		{24 * time.Hour, "day"},
		{time.Hour, "hour"},
		{time.Minute, "minute"},
		{time.Second, "second"},
	}
	for _, u := range units {
		if d >= u.d && d%u.d == 0 {
			n := int64(d / u.d)
			if n == 1 {
				return "1 " + u.name
			}
			return fmt.Sprintf("%d %ss", n, u.name)
		}
	}
	return "" // unreachable: every whole number of seconds divides by a second
}

// timerChangeText is the system text for a disappearing-timer change.
func timerChangeText(who, self core.ACI, name nameFunc, d time.Duration) string {
	a := actor(who, self, name)
	if d <= 0 {
		return a + " disabled disappearing messages."
	}
	return fmt.Sprintf("%s set the disappearing message timer to %s.", a, formatTimer(d))
}

// safetyNumberChangedText is shown when a contact's identity key changes.
func safetyNumberChangedText(name string) string {
	return fmt.Sprintf("Your safety number with %s has changed.", name)
}

// decryptionErrorText is shown when a message from name could not be
// decrypted.
func decryptionErrorText(name string) string {
	return fmt.Sprintf("A message from %s couldn't be delivered. Check your phone.", name)
}

// unsupportedText replaces content warte cannot display.
const unsupportedText = "This message uses a feature warte doesn't support yet. Check your phone."

// pinText is the system text for (un)pinning a message.
func pinText(who, self core.ACI, name nameFunc, pin bool) string {
	if pin {
		return actor(who, self, name) + " pinned a message."
	}
	return actor(who, self, name) + " unpinned a message."
}

// accessLabel names a group access level in UI copy.
func accessLabel(a signalmeow.AccessControl) string {
	switch a {
	case signalmeow.AccessControl_ADMINISTRATOR:
		return "Only admins"
	case signalmeow.AccessControl_ANY, signalmeow.AccessControl_MEMBER:
		return "All members"
	}
	return "Nobody"
}

// groupChangeText renders a decrypted group change as Signal-style system
// text, one sentence per line. self is our ACI; name resolves other ACIs.
func groupChangeText(ch *signalmeow.GroupChange, self core.ACI, name nameFunc) string {
	if ch == nil {
		return ""
	}
	var source core.ACI
	if ch.SourceServiceID.Type == libsignalgo.ServiceIDTypeACI {
		source = aciOf(ch.SourceServiceID.UUID)
	}
	src := actor(source, self, name)
	if source == "" {
		src = "The group"
	}
	obj := func(u uuid.UUID) string { return object(aciOf(u), self, name) }
	subj := func(u uuid.UUID) string { return actor(aciOf(u), self, name) }
	var lines []string
	add := func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }

	if ch.ModifyTitle != nil {
		if t := strings.TrimSpace(*ch.ModifyTitle); t != "" {
			add("%s changed the group name to “%s”.", src, t)
		} else {
			add("%s removed the group name.", src)
		}
	}
	if ch.ModifyAvatar != nil {
		if *ch.ModifyAvatar != "" {
			add("%s changed the group avatar.", src)
		} else {
			add("%s removed the group avatar.", src)
		}
	}
	if ch.ModifyDescription != nil {
		if strings.TrimSpace(*ch.ModifyDescription) != "" {
			add("%s changed the group description.", src)
		} else {
			add("%s removed the group description.", src)
		}
	}
	if ch.ModifyDisappearingMessagesDuration != nil {
		lines = append(lines, timerChangeText(source, self, name, time.Duration(*ch.ModifyDisappearingMessagesDuration)*time.Second))
	}
	for _, m := range ch.AddMembers {
		if m == nil {
			continue
		}
		who := aciOf(m.ACI)
		switch {
		case m.JoinFromInviteLink:
			add("%s joined the group via the group link.", subj(m.ACI))
		case who == source && source != "":
			add("%s joined the group.", subj(m.ACI))
		case who == self:
			add("%s added you to the group.", src)
		default:
			add("%s added %s.", src, obj(m.ACI))
		}
	}
	for _, u := range ch.DeleteMembers {
		if u == nil {
			continue
		}
		if aciOf(*u) == source && source != "" {
			add("%s left the group.", subj(*u))
		} else {
			add("%s removed %s.", src, obj(*u))
		}
	}
	for _, r := range ch.ModifyMemberRoles {
		if r == nil {
			continue
		}
		if r.Role == signalmeow.GroupMember_ADMINISTRATOR {
			add("%s made %s an admin.", src, obj(r.ACI))
		} else {
			add("%s revoked admin privileges from %s.", src, obj(r.ACI))
		}
	}
	if n := len(ch.AddPendingMembers); n > 0 {
		invitedSelf := false
		for _, p := range ch.AddPendingMembers {
			if p != nil && p.ServiceID.Type == libsignalgo.ServiceIDTypeACI && aciOf(p.ServiceID.UUID) == self {
				invitedSelf = true
			}
		}
		switch {
		case invitedSelf:
			add("%s invited you to the group.", src)
		case n == 1:
			add("%s invited 1 person to the group.", src)
		default:
			add("%s invited %d people to the group.", src, n)
		}
	}
	if n := len(ch.DeletePendingMembers); n > 0 {
		declined := false
		for _, p := range ch.DeletePendingMembers {
			if p != nil && p.Type == libsignalgo.ServiceIDTypeACI && aciOf(p.UUID) == source && source != "" {
				declined = true
			}
		}
		switch {
		case declined:
			add("%s declined the invitation to the group.", src)
		case n == 1:
			add("%s revoked an invitation to the group.", src)
		default:
			add("%s revoked %d invitations to the group.", src, n)
		}
	}
	for _, p := range ch.PromotePendingMembers {
		if p != nil {
			add("%s accepted the invitation to the group.", subj(p.ACI))
		}
	}
	for _, p := range ch.PromotePendingPniAciMembers {
		if p != nil {
			add("%s accepted the invitation to the group.", subj(p.ACI))
		}
	}
	for _, r := range ch.AddRequestingMembers {
		if r != nil {
			add("%s requested to join via the group link.", subj(r.ACI))
		}
	}
	for _, u := range ch.DeleteRequestingMembers {
		if u == nil {
			continue
		}
		if aciOf(*u) == source && source != "" {
			add("%s canceled their request to join the group.", subj(*u))
		} else {
			add("%s denied a request to join the group from %s.", src, obj(*u))
		}
	}
	for _, r := range ch.PromoteRequestingMembers {
		if r != nil {
			add("%s approved a request to join the group from %s.", src, obj(r.ACI))
		}
	}
	if ch.ModifyAttributesAccess != nil {
		add("%s changed who can edit group info to “%s”.", src, accessLabel(*ch.ModifyAttributesAccess))
	}
	if ch.ModifyMemberAccess != nil {
		add("%s changed who can add members to “%s”.", src, accessLabel(*ch.ModifyMemberAccess))
	}
	if ch.ModifyAddFromInviteLinkAccess != nil {
		switch *ch.ModifyAddFromInviteLinkAccess {
		case signalmeow.AccessControl_ANY:
			add("%s turned on the group link with admin approval off.", src)
		case signalmeow.AccessControl_ADMINISTRATOR:
			add("%s turned on the group link with admin approval on.", src)
		default:
			add("%s turned off the group link.", src)
		}
	}
	if ch.ModifyInviteLinkPassword != nil && ch.ModifyAddFromInviteLinkAccess == nil {
		add("%s reset the group link.", src)
	}
	if ch.ModifyAnnouncementsOnly != nil {
		if *ch.ModifyAnnouncementsOnly {
			add("%s changed the group settings to only allow admins to send messages.", src)
		} else {
			add("%s changed the group settings to allow all members to send messages.", src)
		}
	}
	if len(lines) == 0 {
		return src + " updated the group."
	}
	return strings.Join(lines, "\n")
}

// groupCreatedText is shown for the initial group context message (revision
// 0, no change): the group was created with us in it.
func groupCreatedText(who, self core.ACI, name nameFunc) string {
	if who == self {
		return "You created the group."
	}
	return actor(who, self, name) + " added you to the group."
}

// callText renders a call event for the call message body.
func callText(c core.CallInfo) string {
	kind := "Voice call"
	if c.Video {
		kind = "Video call"
	}
	switch {
	case c.Group:
		return "Group call"
	case c.Missed:
		if c.Video {
			return "Missed video call"
		}
		return "Missed voice call"
	case c.Declined:
		return "Declined " + strings.ToLower(kind)
	case c.Incoming:
		return "Incoming " + strings.ToLower(kind)
	}
	return "Outgoing " + strings.ToLower(kind)
}
