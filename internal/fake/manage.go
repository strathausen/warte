package fake

import (
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"warte/internal/core"
)

// systemLocked appends a system notice to the chat and returns its event.
func (s *Service) systemLocked(cs *chatState, text string) core.Event {
	m := s.newMessageLocked(cs, "")
	m.Kind = core.KindSystem
	m.SystemText = text
	m.ExpiresIn = 0
	cs.insert(m)
	return core.MessageAdded{Message: s.exportMsgLocked(m)}
}

// mutate runs fn under the write lock and emits the events it returns. Events
// are queued before the lock is released (queueing never blocks), so the
// event stream reflects mutations in the order they happened.
func (s *Service) mutate(ctx context.Context, fn func() ([]core.Event, error)) error {
	if err := s.begin(ctx); err != nil {
		return err
	}
	defer s.mu.Unlock()
	evs, err := fn()
	s.emitAll(evs)
	return err
}

// formatTimer renders a disappearing-message timer the way Signal does.
func formatTimer(d time.Duration) string {
	switch {
	case d <= 0:
		return "off"
	case d%(7*24*time.Hour) == 0:
		return plural(int(d/(7*24*time.Hour)), "week")
	case d%(24*time.Hour) == 0:
		return plural(int(d/(24*time.Hour)), "day")
	case d%time.Hour == 0:
		return plural(int(d/time.Hour), "hour")
	case d%time.Minute == 0:
		return plural(int(d/time.Minute), "minute")
	}
	return plural(int(d/time.Second), "second")
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

func timerNotice(d time.Duration) string {
	if d <= 0 {
		return "You disabled disappearing messages."
	}
	return "You set the disappearing message timer to " + formatTimer(d) + "."
}

// UpdateChat changes archive/pin/unread/mute/timer state.
func (s *Service) UpdateChat(ctx context.Context, id core.ChatID, u core.ChatUpdate) error {
	if u.ExpireTimer != nil && *u.ExpireTimer < 0 {
		return errors.New("fake: negative disappearing timer")
	}
	return s.mutate(ctx, func() ([]core.Event, error) {
		cs, err := s.chatLocked(id)
		if err != nil {
			return nil, err
		}
		var evs []core.Event
		c := &cs.chat
		if u.Archived != nil {
			c.Archived = *u.Archived
			if c.Archived { // archiving unpins, as in Signal
				c.Pinned, c.PinOrder = false, 0
			}
		}
		if u.Pinned != nil && *u.Pinned != c.Pinned {
			c.Pinned = *u.Pinned
			c.PinOrder = 0
			if c.Pinned {
				c.Archived = false
				for _, o := range s.chats {
					if o != cs && o.chat.Pinned {
						c.PinOrder = max(c.PinOrder, o.chat.PinOrder+1)
					}
				}
			}
		}
		if u.MarkedUnread != nil {
			c.MarkedUnread = *u.MarkedUnread
		}
		if u.MutedUntil != nil {
			c.MutedUntil = *u.MutedUntil
		}
		if u.ExpireTimer != nil && *u.ExpireTimer != c.ExpireTimer {
			c.ExpireTimer = *u.ExpireTimer
			evs = append(evs, s.systemLocked(cs, timerNotice(c.ExpireTimer)))
		}
		return append(evs, core.ChatUpdated{Chat: s.exportChatLocked(cs)}), nil
	})
}

// DeleteChat removes a chat and its messages.
func (s *Service) DeleteChat(ctx context.Context, id core.ChatID) error {
	return s.mutate(ctx, func() ([]core.Event, error) {
		cs, err := s.chatLocked(id)
		if err != nil {
			return nil, err
		}
		delete(s.chats, id)
		for _, m := range cs.msgs {
			for _, a := range m.Attachments {
				if !s.attachmentSharedLocked(a.ID, m.ID) {
					delete(s.blobs, a.ID)
				}
			}
		}
		for _, f := range s.folders {
			delete(f.members, id)
		}
		return []core.Event{core.ChatRemoved{ID: id}}, nil
	})
}

// AcceptMessageRequest accepts an incoming message request (unblocking if needed).
func (s *Service) AcceptMessageRequest(ctx context.Context, id core.ChatID) error {
	return s.mutate(ctx, func() ([]core.Event, error) {
		cs, err := s.chatLocked(id)
		if err != nil {
			return nil, err
		}
		var evs []core.Event
		if ct, ok := s.contacts[id.ACI()]; ok && ct.Blocked {
			ct.Blocked = false
			evs = append(evs, core.ContactUpdated{Contact: *ct})
		}
		if !cs.chat.MessageRequest && !cs.chat.Blocked && len(evs) == 0 {
			return nil, nil
		}
		cs.chat.MessageRequest, cs.chat.Blocked = false, false
		return append(evs, core.ChatUpdated{Chat: s.exportChatLocked(cs)}), nil
	})
}

// Block blocks or unblocks a chat (and, for direct chats, the contact).
func (s *Service) Block(ctx context.Context, id core.ChatID, block bool) error {
	return s.mutate(ctx, func() ([]core.Event, error) {
		cs, err := s.chatLocked(id)
		if err != nil {
			return nil, err
		}
		if id.ACI() == s.self.ACI {
			return nil, fmt.Errorf("can't block yourself: %w", core.ErrReadOnly)
		}
		var evs []core.Event
		name := cs.chat.Title
		if ct, ok := s.contacts[id.ACI()]; ok {
			name = ct.DisplayName()
			if ct.Blocked != block {
				ct.Blocked = block
				evs = append(evs, core.ContactUpdated{Contact: *ct})
			}
		}
		if cs.chat.Blocked == block && len(evs) == 0 {
			return nil, nil
		}
		cs.chat.Blocked = block
		if block {
			cs.chat.MessageRequest = false
			evs = append(evs, s.systemLocked(cs, "You blocked "+name+"."))
		} else {
			evs = append(evs, s.systemLocked(cs, "You unblocked "+name+"."))
		}
		return append(evs, core.ChatUpdated{Chat: s.exportChatLocked(cs)}), nil
	})
}

// ReportSpam records a spam report for a chat.
func (s *Service) ReportSpam(ctx context.Context, id core.ChatID) error {
	return s.mutate(ctx, func() ([]core.Event, error) {
		cs, err := s.chatLocked(id)
		if err != nil {
			return nil, err
		}
		if cs.reported {
			return nil, nil
		}
		cs.reported = true
		return []core.Event{
			s.systemLocked(cs, "Reported as spam."),
			core.ChatUpdated{Chat: s.exportChatLocked(cs)},
		}, nil
	})
}

var (
	usernameRE = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{2,31}\.[0-9]{2,9}$`)
	e164RE     = regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`)
	// aciNamespace derives stable ACIs for contacts discovered at runtime.
	aciNamespace = uuid.MustParse("6ba7b811-9dad-11d1-80b4-00c04fd430c8")
)

// ResolveRecipient turns "+E164", "name.42", "@name.42", a signal.me username
// link or a (partial) contact name into a direct chat, creating the contact
// and chat when a well-formed number or username is unknown.
func (s *Service) ResolveRecipient(ctx context.Context, query string) (core.ChatID, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return "", errors.New("fake: empty recipient")
	}
	var id core.ChatID
	err := s.mutate(ctx, func() ([]core.Event, error) {
		aci, evs, err := s.resolveLocked(q)
		if err != nil {
			return nil, err
		}
		id = core.DirectChatID(aci)
		if _, ok := s.chats[id]; !ok {
			cs := s.direct(aci)
			cs.created = s.now()
			evs = append(evs, core.ChatUpdated{Chat: s.exportChatLocked(cs)})
		}
		return evs, nil
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

func (s *Service) resolveLocked(q string) (core.ACI, []core.Event, error) {
	if i := strings.Index(q, "signal.me/#"); i >= 0 {
		frag := q[i+len("signal.me/#"):]
		if j := strings.LastIndexByte(frag, '/'); j >= 0 {
			frag = frag[j+1:]
		}
		for aci, c := range s.contacts {
			if c.Username != "" && strings.EqualFold(c.Username, frag) {
				return aci, nil, nil
			}
		}
		return "", nil, fmt.Errorf("username link: %w", core.ErrNotFound)
	}
	if strings.HasPrefix(q, "+") {
		num := strings.Map(func(r rune) rune {
			if r == ' ' || r == '-' || r == '(' || r == ')' || r == '/' {
				return -1
			}
			return r
		}, q)
		if !e164RE.MatchString(num) {
			return "", nil, fmt.Errorf("fake: %q is not a valid phone number", q)
		}
		if num == s.self.E164 {
			return s.self.ACI, nil, nil
		}
		for aci, c := range s.contacts {
			if c.E164 == num {
				return aci, nil, nil
			}
		}
		return s.newContactLocked(core.Contact{E164: num})
	}
	if u := strings.TrimPrefix(q, "@"); usernameRE.MatchString(u) {
		if strings.EqualFold(u, s.self.Username) {
			return s.self.ACI, nil, nil
		}
		for aci, c := range s.contacts {
			if strings.EqualFold(c.Username, u) {
				return aci, nil, nil
			}
		}
		return s.newContactLocked(core.Contact{Username: strings.ToLower(u)})
	}
	lq := strings.ToLower(q)
	if lq == "note to self" || lq == "self" || lq == "me" || strings.EqualFold(q, s.self.DisplayName()) {
		return s.self.ACI, nil, nil
	}
	// Exact name match, then unique prefix, then unique substring.
	for _, match := range []func(string) bool{
		func(n string) bool { return n == lq },
		func(n string) bool { return strings.HasPrefix(n, lq) },
		func(n string) bool { return strings.Contains(n, lq) },
	} {
		var found []core.ACI
		for aci, c := range s.contacts {
			for _, n := range []string{c.Nickname, c.ContactName, c.ProfileName} {
				if n != "" && match(strings.ToLower(n)) {
					found = append(found, aci)
					break
				}
			}
		}
		switch len(found) {
		case 0:
			continue
		case 1:
			return found[0], nil, nil
		default:
			names := make([]string, len(found))
			for i, a := range found {
				names[i] = s.contacts[a].DisplayName()
			}
			slices.Sort(names)
			return "", nil, fmt.Errorf("fake: %q is ambiguous: %s", q, strings.Join(names, ", "))
		}
	}
	return "", nil, fmt.Errorf("recipient %q: %w", q, core.ErrNotFound)
}

func (s *Service) newContactLocked(c core.Contact) (core.ACI, []core.Event, error) {
	key := c.E164 + "|" + c.Username
	c.ACI = core.ACI(uuid.NewSHA1(aciNamespace, []byte(key)).String())
	c.AvatarID = "avatar-" + string(c.ACI)
	s.avatars[c.AvatarID] = avatarInitialsPNG(c.DisplayName(), 96)
	s.contacts[c.ACI] = &c
	return c.ACI, []core.Event{core.ContactUpdated{Contact: c}}, nil
}

// ---------------------------------------------------------------------------
// Groups

// CreateGroup creates a group with self as admin.
func (s *Service) CreateGroup(ctx context.Context, title string, members []core.ACI, avatar []byte) (core.ChatID, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return "", errors.New("fake: group title is required")
	}
	var id core.ChatID
	err := s.mutate(ctx, func() ([]core.Event, error) {
		var mem []core.ACI
		for _, m := range members {
			if m == s.self.ACI || slices.Contains(mem, m) {
				continue
			}
			if _, ok := s.contacts[m]; !ok {
				return nil, fmt.Errorf("member %s: %w", m, core.ErrNotFound)
			}
			mem = append(mem, m)
		}
		raw := make([]byte, 32)
		for i := range raw {
			raw[i] = byte(s.rng.UintN(256))
		}
		id = core.GroupChatID(base64.StdEncoding.EncodeToString(raw))
		cs := s.group(id, title, "", mem, s.self.ACI)
		cs.chat.GroupRevision = 0
		cs.created = s.now()
		if len(avatar) > 0 {
			s.avatars[cs.chat.AvatarID] = slices.Clone(avatar)
		}
		evs := []core.Event{s.systemLocked(cs, "You created the group.")}
		if len(mem) > 0 {
			names := make([]string, len(mem))
			for i, m := range mem {
				names[i] = s.nameLocked(m)
			}
			evs = append(evs, s.systemLocked(cs, "You added "+joinNames(names)+"."))
		}
		return append(evs, core.ChatUpdated{Chat: s.exportChatLocked(cs)}), nil
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// groupLocked returns a group chat self is still part of.
func (s *Service) groupLocked(id core.ChatID) (*chatState, error) {
	cs, err := s.chatLocked(id)
	if err != nil {
		return nil, err
	}
	if cs.chat.Kind != core.ChatGroup {
		return nil, fmt.Errorf("not a group: %w", core.ErrReadOnly)
	}
	if cs.left {
		return nil, fmt.Errorf("you left this group: %w", core.ErrReadOnly)
	}
	return cs, nil
}

func memberIndex(ms []core.Member, aci core.ACI) int {
	return slices.IndexFunc(ms, func(m core.Member) bool { return m.ACI == aci })
}

// UpdateGroup applies group changes. Title, description, avatar, timer and
// adding members are open to all members; everything else needs admin.
func (s *Service) UpdateGroup(ctx context.Context, id core.ChatID, u core.GroupUpdate) error {
	switch {
	case u.Title != nil && strings.TrimSpace(*u.Title) == "":
		return errors.New("fake: group title is required")
	case u.ExpireTimer != nil && *u.ExpireTimer < 0:
		return errors.New("fake: negative disappearing timer")
	}
	return s.mutate(ctx, func() ([]core.Event, error) {
		cs, err := s.groupLocked(id)
		if err != nil {
			return nil, err
		}
		c := &cs.chat
		adminOnly := len(u.RemoveMembers) > 0 || len(u.Promote) > 0 || len(u.Demote) > 0 ||
			len(u.Approve) > 0 || len(u.Deny) > 0 || u.OnlyAdminsSend != nil || u.LinkMode != nil || u.ResetLink
		if adminOnly && !c.SelfIsAdmin {
			return nil, fmt.Errorf("only admins can do that: %w", core.ErrReadOnly)
		}
		// Validate everything before mutating, so a failed update changes nothing.
		for _, list := range [][]core.ACI{u.AddMembers, u.RemoveMembers, u.Promote, u.Demote, u.Approve, u.Deny} {
			for _, a := range list {
				if _, ok := s.contacts[a]; !ok && a != s.self.ACI {
					return nil, fmt.Errorf("member %s: %w", a, core.ErrNotFound)
				}
			}
		}
		if slices.Contains(u.AddMembers, s.self.ACI) {
			return nil, errors.New("fake: you are already a member")
		}
		if slices.Contains(u.RemoveMembers, s.self.ACI) {
			return nil, errors.New("fake: use LeaveGroup to leave")
		}
		if u.ResetLink && c.InviteLink == "" && (u.LinkMode == nil || *u.LinkMode == core.AccessUnsatisfiable || *u.LinkMode == core.AccessUnknown) {
			return nil, fmt.Errorf("the group link is off: %w", core.ErrReadOnly)
		}
		var notes []string
		if u.Title != nil {
			if t := strings.TrimSpace(*u.Title); t != c.Title {
				c.Title = t
				notes = append(notes, "You changed the group name to “"+t+"”.")
			}
		}
		if u.Description != nil && *u.Description != c.Description {
			c.Description = *u.Description
			notes = append(notes, "You changed the group description.")
		}
		if u.Avatar != nil {
			if len(u.Avatar) == 0 {
				delete(s.avatars, c.AvatarID)
				c.AvatarID = ""
				notes = append(notes, "You removed the group avatar.")
			} else {
				if c.AvatarID == "" {
					c.AvatarID = "avatar-" + id.GroupID()
				}
				s.avatars[c.AvatarID] = slices.Clone(u.Avatar)
				notes = append(notes, "You changed the group avatar.")
			}
		}
		for _, a := range u.AddMembers {
			if memberIndex(c.Members, a) >= 0 {
				continue
			}
			c.Members = append(c.Members, core.Member{ACI: a})
			c.PendingMembers = slices.DeleteFunc(c.PendingMembers, func(m core.Member) bool { return m.ACI == a })
			notes = append(notes, "You added "+s.nameLocked(a)+".")
		}
		for _, a := range u.RemoveMembers {
			if i := memberIndex(c.Members, a); i >= 0 {
				c.Members = slices.Delete(c.Members, i, i+1)
				notes = append(notes, "You removed "+s.nameLocked(a)+".")
			}
		}
		for _, a := range u.Promote {
			if i := memberIndex(c.Members, a); i >= 0 && !c.Members[i].Admin {
				c.Members[i].Admin = true
				notes = append(notes, "You made "+s.nameLocked(a)+" an admin.")
			}
		}
		for _, a := range u.Demote {
			if i := memberIndex(c.Members, a); i >= 0 && c.Members[i].Admin {
				c.Members[i].Admin = false
				if a == s.self.ACI {
					c.SelfIsAdmin = false
				}
				notes = append(notes, "You revoked admin privileges from "+s.nameLocked(a)+".")
			}
		}
		for _, a := range u.Approve {
			if i := memberIndex(c.RequestingJoin, a); i >= 0 {
				c.RequestingJoin = slices.Delete(c.RequestingJoin, i, i+1)
				c.Members = append(c.Members, core.Member{ACI: a})
				notes = append(notes, "You approved a request to join the group from "+s.nameLocked(a)+".")
			}
		}
		for _, a := range u.Deny {
			if i := memberIndex(c.RequestingJoin, a); i >= 0 {
				c.RequestingJoin = slices.Delete(c.RequestingJoin, i, i+1)
				notes = append(notes, "You denied a request to join the group from "+s.nameLocked(a)+".")
			}
		}
		if u.OnlyAdminsSend != nil && *u.OnlyAdminsSend != c.OnlyAdminsSend {
			c.OnlyAdminsSend = *u.OnlyAdminsSend
			if c.OnlyAdminsSend {
				notes = append(notes, "You changed the group settings to only allow admins to send messages.")
			} else {
				notes = append(notes, "You changed the group settings to allow all members to send messages.")
			}
		}
		if u.LinkMode != nil && *u.LinkMode != c.AddFromLinkMode {
			c.AddFromLinkMode = *u.LinkMode
			switch c.AddFromLinkMode {
			case core.AccessUnsatisfiable, core.AccessUnknown:
				c.InviteLink = ""
				notes = append(notes, "You turned off the group link.")
			case core.AccessAdmin:
				s.ensureLinkLocked(cs, false)
				notes = append(notes, "You turned on the group link with admin approval on.")
			default:
				s.ensureLinkLocked(cs, false)
				notes = append(notes, "You turned on the group link with admin approval off.")
			}
		}
		if u.ResetLink && c.InviteLink != "" {
			s.ensureLinkLocked(cs, true)
			notes = append(notes, "You reset the group link.")
		}
		if u.ExpireTimer != nil && *u.ExpireTimer != c.ExpireTimer {
			c.ExpireTimer = *u.ExpireTimer
			notes = append(notes, timerNotice(c.ExpireTimer))
		}
		if len(notes) == 0 {
			return nil, nil
		}
		c.GroupRevision++
		var evs []core.Event
		for _, n := range notes {
			evs = append(evs, s.systemLocked(cs, n))
		}
		return append(evs, core.ChatUpdated{Chat: s.exportChatLocked(cs)}), nil
	})
}

// ensureLinkLocked creates (or with reset, regenerates) the invite link.
func (s *Service) ensureLinkLocked(cs *chatState, reset bool) {
	if cs.chat.InviteLink != "" && !reset {
		return
	}
	raw := make([]byte, 24)
	for i := range raw {
		raw[i] = byte(s.rng.UintN(256))
	}
	cs.chat.InviteLink = "https://signal.group/#" + base64.RawURLEncoding.EncodeToString(raw)
}

// LeaveGroup leaves a group. If self was the only admin, the first remaining
// member is promoted, as Signal requires a remaining admin.
func (s *Service) LeaveGroup(ctx context.Context, id core.ChatID) error {
	return s.mutate(ctx, func() ([]core.Event, error) {
		cs, err := s.groupLocked(id)
		if err != nil {
			return nil, err
		}
		c := &cs.chat
		if i := memberIndex(c.Members, s.self.ACI); i >= 0 {
			c.Members = slices.Delete(c.Members, i, i+1)
		}
		if c.SelfIsAdmin && len(c.Members) > 0 && !slices.ContainsFunc(c.Members, func(m core.Member) bool { return m.Admin }) {
			c.Members[0].Admin = true
		}
		c.SelfIsAdmin = false
		c.GroupRevision++
		cs.left = true
		return []core.Event{
			s.systemLocked(cs, "You left the group."),
			core.ChatUpdated{Chat: s.exportChatLocked(cs)},
		}, nil
	})
}

// JoinGroupByLink joins (or rejoins) a known group by its invite link.
func (s *Service) JoinGroupByLink(ctx context.Context, link string) (core.ChatID, error) {
	link = strings.TrimSpace(link)
	if !strings.HasPrefix(link, "https://signal.group/#") && !strings.HasPrefix(link, "sgnl://signal.group/#") {
		return "", fmt.Errorf("fake: %q is not a group link", link)
	}
	link = "https://signal.group/#" + link[strings.Index(link, "#")+1:]
	var id core.ChatID
	err := s.mutate(ctx, func() ([]core.Event, error) {
		for cid, cs := range s.chats {
			if cs.chat.InviteLink != link || cs.chat.AddFromLinkMode == core.AccessUnsatisfiable {
				continue
			}
			id = cid
			if !cs.left && memberIndex(cs.chat.Members, s.self.ACI) >= 0 {
				return nil, nil
			}
			if cs.chat.AddFromLinkMode == core.AccessAdmin {
				// Admin approval required: file a join request instead.
				if memberIndex(cs.chat.RequestingJoin, s.self.ACI) >= 0 {
					return nil, nil
				}
				cs.chat.RequestingJoin = append(cs.chat.RequestingJoin, core.Member{ACI: s.self.ACI})
				return []core.Event{
					s.systemLocked(cs, "You sent a request to join the group."),
					core.ChatUpdated{Chat: s.exportChatLocked(cs)},
				}, nil
			}
			cs.left = false
			cs.chat.Members = append(cs.chat.Members, core.Member{ACI: s.self.ACI})
			cs.chat.GroupRevision++
			return []core.Event{
				s.systemLocked(cs, "You joined the group via the group link."),
				core.ChatUpdated{Chat: s.exportChatLocked(cs)},
			}, nil
		}
		return nil, fmt.Errorf("group link: %w", core.ErrNotFound)
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// ---------------------------------------------------------------------------
// Contacts & safety

// fingerprint derives a deterministic 30-digit displayable fingerprint and
// its 32 raw bytes for an identity, mimicking Signal's iterated SHA-512.
func fingerprint(aci core.ACI) (string, []byte) {
	h := sha512.Sum512(append([]byte{0, 0}, []byte("warte-identity/"+string(aci))...))
	for range 64 {
		h = sha512.Sum512(append(h[:], []byte(aci)...))
	}
	var b strings.Builder
	for i := 0; i < 6; i++ {
		chunk := h[i*5 : i*5+5]
		v := uint64(chunk[0])<<32 | uint64(binary.BigEndian.Uint32(chunk[1:]))
		fmt.Fprintf(&b, "%05d", v%100000)
	}
	return b.String(), h[:32]
}

// SafetyNumber returns the 60-digit safety number shared with a contact.
func (s *Service) SafetyNumber(ctx context.Context, aci core.ACI) (core.SafetyNumber, error) {
	if err := ctx.Err(); err != nil {
		return core.SafetyNumber{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if aci == s.self.ACI {
		return core.SafetyNumber{}, fmt.Errorf("no safety number with yourself: %w", core.ErrReadOnly)
	}
	c, ok := s.contacts[aci]
	if !ok {
		return core.SafetyNumber{}, fmt.Errorf("contact %s: %w", aci, core.ErrNotFound)
	}
	local, lraw := fingerprint(s.self.ACI)
	remote, rraw := fingerprint(aci)
	digits := local + remote
	if remote < local {
		digits = remote + local
	}
	qr := append([]byte{0x02}, lraw...)
	qr = append(qr, rraw...)
	return core.SafetyNumber{Digits: digits, QRData: qr, Verified: c.Verified}, nil
}

// SetVerified marks a contact's safety number as (un)verified.
func (s *Service) SetVerified(ctx context.Context, aci core.ACI, verified bool) error {
	return s.mutate(ctx, func() ([]core.Event, error) {
		c, ok := s.contacts[aci]
		if !ok {
			return nil, fmt.Errorf("contact %s: %w", aci, core.ErrNotFound)
		}
		if c.Verified == verified {
			return nil, nil
		}
		c.Verified = verified
		evs := []core.Event{core.ContactUpdated{Contact: *c}}
		if cs, ok := s.chats[core.DirectChatID(aci)]; ok {
			verb := "verified"
			if !verified {
				verb = "not verified"
			}
			evs = append(evs, s.systemLocked(cs, "You marked your safety number with "+c.DisplayName()+" "+verb+"."),
				core.ChatUpdated{Chat: s.exportChatLocked(cs)})
		}
		return evs, nil
	})
}

// SetNickname sets (or with "", clears) a local nickname for a contact.
func (s *Service) SetNickname(ctx context.Context, aci core.ACI, nickname string) error {
	return s.mutate(ctx, func() ([]core.Event, error) {
		c, ok := s.contacts[aci]
		if !ok {
			return nil, fmt.Errorf("contact %s: %w", aci, core.ErrNotFound)
		}
		nickname = strings.TrimSpace(nickname)
		if c.Nickname == nickname {
			return nil, nil
		}
		c.Nickname = nickname
		evs := []core.Event{core.ContactUpdated{Contact: *c}}
		for _, cs := range s.chats {
			if cs.chat.ID == core.DirectChatID(aci) || memberIndex(cs.chat.Members, aci) >= 0 {
				evs = append(evs, core.ChatUpdated{Chat: s.exportChatLocked(cs)})
			}
		}
		return evs, nil
	})
}

// UpdateProfile edits your own profile.
func (s *Service) UpdateProfile(ctx context.Context, p core.ProfileUpdate) error {
	return s.mutate(ctx, func() ([]core.Event, error) {
		if p.GivenName != nil {
			g := strings.TrimSpace(*p.GivenName)
			if g == "" {
				return nil, errors.New("fake: a first name is required")
			}
			s.selfGiven = g
		}
		if p.FamilyName != nil {
			s.selfFamily = strings.TrimSpace(*p.FamilyName)
		}
		s.self.ProfileName = strings.TrimSpace(s.selfGiven + " " + s.selfFamily)
		if p.About != nil {
			s.self.About = *p.About
		}
		if p.AboutEmoji != nil {
			s.self.AboutEmoji = *p.AboutEmoji
		}
		if p.Avatar != nil {
			delete(s.avatars, s.self.AvatarID)
			if len(p.Avatar) == 0 {
				s.self.AvatarID = ""
			} else {
				s.self.AvatarID = fmt.Sprintf("avatar-self-%d", s.nextTSLocked(s.now()))
				s.avatars[s.self.AvatarID] = slices.Clone(p.Avatar)
			}
		}
		evs := []core.Event{core.ContactUpdated{Contact: s.self}}
		if cs, ok := s.chats[core.DirectChatID(s.self.ACI)]; ok {
			evs = append(evs, core.ChatUpdated{Chat: s.exportChatLocked(cs)})
		}
		return evs, nil
	})
}

// ---------------------------------------------------------------------------
// Stories

func (s *Service) storyLocked(id core.MessageID) (*core.Story, error) {
	for _, st := range s.stories {
		if st.ID == id {
			return st, nil
		}
	}
	return nil, fmt.Errorf("story %s: %w", id, core.ErrNotFound)
}

// MarkStoryViewed marks a story as seen.
func (s *Service) MarkStoryViewed(ctx context.Context, id core.MessageID) error {
	return s.mutate(ctx, func() ([]core.Event, error) {
		st, err := s.storyLocked(id)
		if err != nil {
			return nil, err
		}
		if st.Viewed {
			return nil, nil
		}
		st.Viewed = true
		return []core.Event{core.StoriesChanged{}}, nil
	})
}

// ReplyToStory sends a text reply or emoji reaction to someone's story. The
// reply lands in the group (group stories) or the 1:1 chat with the author.
func (s *Service) ReplyToStory(ctx context.Context, id core.MessageID, body string, emoji string) error {
	if strings.TrimSpace(body) == "" && emoji == "" {
		return errors.New("fake: empty story reply")
	}
	var sent core.MessageID
	err := s.mutate(ctx, func() ([]core.Event, error) {
		st, err := s.storyLocked(id)
		if err != nil {
			return nil, err
		}
		if st.ID.Author == s.self.ACI {
			return nil, fmt.Errorf("can't reply to your own story: %w", core.ErrReadOnly)
		}
		var evs []core.Event
		target := core.DirectChatID(st.ID.Author)
		if st.Group != "" {
			target = st.Group
		}
		cs, ok := s.chats[target]
		if !ok {
			cs = s.direct(st.ID.Author)
			cs.created = s.now()
		}
		if err := s.canSendLocked(cs); err != nil {
			return nil, err
		}
		m := s.newMessageLocked(cs, s.self.ACI)
		m.Kind = core.KindStoryReply
		m.Body = body
		m.StoryContext = &core.StoryRef{Author: st.ID.Author, Timestamp: st.ID.Timestamp, Emoji: emoji}
		st.Replies++
		sent = m.ID
		evs = append(evs, s.appendOutgoingLocked(cs, m)...)
		return append(evs, core.StoriesChanged{}), nil
	})
	if err == nil {
		s.spawn(func() { s.progress(sent) })
	}
	return err
}

// PostStory posts a text or media story to "My Story" or the given groups.
func (s *Service) PostStory(ctx context.Context, story core.OutgoingStory) error {
	if (story.Text == nil) == (story.Media == nil) {
		return errors.New("fake: a story has exactly one of text or media")
	}
	if story.Text != nil && strings.TrimSpace(story.Text.Text) == "" && story.Text.URL == "" {
		return errors.New("fake: empty text story")
	}
	return s.mutate(ctx, func() ([]core.Event, error) {
		targets := []core.ChatID{""}
		if len(story.To) > 0 {
			targets = story.To
			for _, t := range targets {
				if _, err := s.groupLocked(t); err != nil {
					return nil, err
				}
			}
		}
		for _, t := range targets {
			ts := s.nextTSLocked(s.now())
			chat := t
			if chat == "" {
				chat = core.DirectChatID(s.self.ACI)
			}
			st := &core.Story{
				ID:     core.MessageID{Chat: chat, Author: s.self.ACI, Timestamp: ts},
				At:     time.UnixMilli(int64(ts)),
				Viewed: true,
				Group:  t,
			}
			if story.Text != nil {
				txt := *story.Text
				txt.Gradient = slices.Clone(story.Text.Gradient)
				st.Text = &txt
			} else {
				a := s.storeOutgoingLocked(ts, 0, *story.Media)
				st.Media = &a
				st.Caption = story.Media.Caption
			}
			s.stories = append(s.stories, st)
		}
		return []core.Event{core.StoriesChanged{}}, nil
	})
}

// ---------------------------------------------------------------------------
// Stickers & account

// InstallStickerPack installs or uninstalls a known sticker pack.
func (s *Service) InstallStickerPack(ctx context.Context, packID, packKey string, install bool) error {
	return s.mutate(ctx, func() ([]core.Event, error) {
		for _, p := range s.packs {
			if p.ID != packID {
				continue
			}
			if packKey != "" && !strings.EqualFold(packKey, p.Key) {
				return nil, errors.New("fake: sticker pack key mismatch")
			}
			p.Installed = install
			return nil, nil
		}
		return nil, fmt.Errorf("sticker pack %s: %w", packID, core.ErrNotFound)
	})
}

// UpdateSettings replaces the account settings.
func (s *Service) UpdateSettings(ctx context.Context, st core.Settings) error {
	return s.mutate(ctx, func() ([]core.Event, error) {
		if st.DefaultExpireTimer < 0 {
			return nil, errors.New("fake: negative default timer")
		}
		s.settings = st
		return nil, nil
	})
}

// Unlink logs this device out; subsequent actions return core.ErrNotLoggedIn.
func (s *Service) Unlink(ctx context.Context) error {
	return s.mutate(ctx, func() ([]core.Event, error) {
		s.loggedOut = true
		s.conn = core.ConnLoggedOut
		return []core.Event{core.ConnChanged{State: core.ConnLoggedOut}, core.LoggedOut{}}, nil
	})
}

// backupFile is the JSON layout written by ExportBackup.
type backupFile struct {
	Format       string             `json:"format"`
	ExportedAt   time.Time          `json:"exported_at"`
	Self         core.Contact       `json:"self"`
	Settings     core.Settings      `json:"settings"`
	Contacts     []core.Contact     `json:"contacts"`
	Chats        []backupChat       `json:"chats"`
	Stories      []core.Story       `json:"stories"`
	StickerPacks []core.StickerPack `json:"sticker_packs"`
	Folders      []core.Folder      `json:"folders"`
}

type backupChat struct {
	Chat     core.Chat      `json:"chat"`
	Messages []core.Message `json:"messages"`
}

// ExportBackup writes a JSON dump of all state (attachment metadata only, no
// media bytes) to path with mode 0600.
func (s *Service) ExportBackup(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.RLock()
	b := backupFile{
		Format:     "warte-fake-backup/1",
		ExportedAt: s.now(),
		Self:       s.self,
		Settings:   s.settings,
	}
	for _, c := range s.contacts {
		b.Contacts = append(b.Contacts, *c)
	}
	slices.SortFunc(b.Contacts, func(x, y core.Contact) int { return strings.Compare(string(x.ACI), string(y.ACI)) })
	ids := make([]core.ChatID, 0, len(s.chats))
	for id := range s.chats {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		cs := s.chats[id]
		b.Chats = append(b.Chats, backupChat{Chat: s.exportChatLocked(cs), Messages: s.exportListLocked(s.visibleMsgsLocked(cs))})
	}
	for _, st := range s.stories {
		b.Stories = append(b.Stories, cloneStory(st))
	}
	for _, p := range s.packs {
		b.StickerPacks = append(b.StickerPacks, clonePack(p))
	}
	for _, f := range s.folders {
		b.Folders = append(b.Folders, f.folder)
	}
	s.mu.RUnlock()

	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return fmt.Errorf("fake: encode backup: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return writePrivateFile(path, data)
}

// writePrivateFile atomically replaces path with data. The bytes go to a 0600
// temporary file in the same directory first, so the content is never
// readable by others, even if path already existed with looser permissions.
func writePrivateFile(path string, data []byte) (err error) {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("fake: write backup: %w", err)
	}
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(f.Name())
		}
	}()
	if err = f.Chmod(0o600); err != nil {
		return fmt.Errorf("fake: write backup: %w", err)
	}
	if _, err = f.Write(data); err != nil {
		return fmt.Errorf("fake: write backup: %w", err)
	}
	if err = f.Sync(); err != nil {
		return fmt.Errorf("fake: write backup: %w", err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("fake: write backup: %w", err)
	}
	if err = os.Rename(f.Name(), path); err != nil {
		os.Remove(f.Name())
		return fmt.Errorf("fake: write backup: %w", err)
	}
	return nil
}
