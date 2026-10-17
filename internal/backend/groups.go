package backend

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"go.mau.fi/util/ptr"

	"go.mau.fi/mautrix-signal/pkg/libsignalgo"
	"go.mau.fi/mautrix-signal/pkg/signalmeow"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/types"

	"warte/internal/core"
)

// queueGroupRefresh asks the group worker to fetch a group's state. It
// never blocks.
func (b *Backend) queueGroupRefresh(id core.ChatID) {
	if !id.IsGroup() {
		return
	}
	select {
	case b.groupSync <- id:
	default:
	}
}

// groupWorker refreshes groups one at a time once connected, coalescing
// repeated requests for the same group.
func (b *Backend) groupWorker() {
	ctx := b.log.WithContext(b.ctx)
	recent := map[core.ChatID]time.Time{}
	done := 0
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-b.groupSync:
			if !b.waitConnected(ctx, 10*time.Minute) {
				if ctx.Err() != nil {
					return
				}
				continue
			}
			if t, ok := recent[id]; !ok || b.now().Sub(t) >= 2*time.Second {
				recent[id] = b.now()
				if err := b.refreshGroup(ctx, id); err != nil {
					zerolog.Ctx(ctx).Debug().Err(err).Str("chat", string(id)).Msg("Group refresh failed")
				}
			}
			// A skipped duplicate still counts, so the stage always ends.
			done++
			if len(b.groupSync) == 0 {
				b.emitSyncDone("groups", done)
				done = 0
			}
		}
	}
}

// refreshGroup fetches a group's current state from the server and stores
// it as the chat.
func (b *Backend) refreshGroup(ctx context.Context, id core.ChatID) error {
	cli, err := b.client()
	if err != nil {
		return err
	}
	gid, err := groupIdentifier(id)
	if err != nil {
		return err
	}
	cli.GroupCache.Delete(gid)
	g, _, err := cli.RetrieveGroupByID(ctx, gid, 0)
	if err != nil {
		if errors.Is(err, signalmeow.AuthorizationFailedError) || errors.Is(err, signalmeow.NotFoundError) {
			return b.markGroupLeft(ctx, id)
		}
		return err
	}
	return b.storeGroup(ctx, cli, g)
}

// storeGroup merges a fetched group into its chat and announces it.
func (b *Backend) storeGroup(ctx context.Context, cli *signalmeow.Client, g *signalmeow.Group) error {
	id := groupChatID(g.GroupIdentifier)
	existing, err := b.db.GetChat(ctx, id)
	if err != nil && !errors.Is(err, core.ErrNotFound) {
		return err
	}
	link := ""
	if g.InviteLinkPassword != nil {
		link, _ = g.GetInviteLink()
	}
	self := b.selfACI()
	c := groupToChat(g, existing, self, b.names(ctx), link)
	if c.AvatarID != "" {
		b.registerAvatar(ctx, c.AvatarID, "group:"+string(g.GroupIdentifier), avatarSource{Kind: "group", Path: g.AvatarPath, Group: string(g.GroupIdentifier)})
	}
	if err := b.db.UpsertChat(ctx, c); err != nil {
		return err
	}
	b.emitChat(ctx, id)
	for _, m := range c.Members {
		if m.ACI != self && (m.Name == "" || m.Name == "Unknown") {
			b.queueProfile(m.ACI)
		}
	}
	return nil
}

// markGroupLeft records that we are no longer a member of a group.
func (b *Backend) markGroupLeft(ctx context.Context, id core.ChatID) error {
	self := b.selfACI()
	_, err := b.db.UpdateChat(ctx, id, func(c *core.Chat) error {
		var kept []core.Member
		for _, m := range c.Members {
			if m.ACI != self {
				kept = append(kept, m)
			}
		}
		c.Members, c.SelfIsAdmin = kept, false
		if c.GroupRevision == 0 {
			c.GroupRevision = 1 // known group we are not a member of
		}
		return nil
	})
	if err != nil {
		return ignoreNotFound(err)
	}
	b.emitChat(ctx, id)
	return nil
}

// buildGroupChange translates a core.GroupUpdate into a signalmeow group
// change. avatarPath is the uploaded avatar's CDN path when u.Avatar is
// non-empty. hasLink reports whether the group currently has an invite link
// password.
func buildGroupChange(u core.GroupUpdate, avatarPath string, hasLink bool) (*signalmeow.GroupChange, error) {
	ch := &signalmeow.GroupChange{}
	if u.Title != nil {
		t := strings.TrimSpace(*u.Title)
		if t == "" {
			return nil, errors.New("backend: a group needs a name")
		}
		ch.ModifyTitle = &t
	}
	if u.Description != nil {
		ch.ModifyDescription = ptr.Ptr(strings.TrimSpace(*u.Description))
	}
	if u.Avatar != nil {
		ch.ModifyAvatar = ptr.Ptr(avatarPath)
	}
	parse := func(list []core.ACI) ([]uuid.UUID, error) {
		var out []uuid.UUID
		for _, a := range list {
			id, err := parseACI(a)
			if err != nil {
				return nil, err
			}
			out = append(out, id)
		}
		return out, nil
	}
	add, err := parse(u.AddMembers)
	if err != nil {
		return nil, err
	}
	for _, id := range add {
		ch.AddMembers = append(ch.AddMembers, &signalmeow.AddMember{GroupMember: signalmeow.GroupMember{ACI: id, Role: signalmeow.GroupMember_DEFAULT}})
	}
	rm, err := parse(u.RemoveMembers)
	if err != nil {
		return nil, err
	}
	for _, id := range rm {
		ch.DeleteMembers = append(ch.DeleteMembers, ptr.Ptr(id))
	}
	promote, err := parse(u.Promote)
	if err != nil {
		return nil, err
	}
	for _, id := range promote {
		ch.ModifyMemberRoles = append(ch.ModifyMemberRoles, &signalmeow.RoleMember{ACI: id, Role: signalmeow.GroupMember_ADMINISTRATOR})
	}
	demote, err := parse(u.Demote)
	if err != nil {
		return nil, err
	}
	for _, id := range demote {
		ch.ModifyMemberRoles = append(ch.ModifyMemberRoles, &signalmeow.RoleMember{ACI: id, Role: signalmeow.GroupMember_DEFAULT})
	}
	approve, err := parse(u.Approve)
	if err != nil {
		return nil, err
	}
	for _, id := range approve {
		ch.PromoteRequestingMembers = append(ch.PromoteRequestingMembers, &signalmeow.RoleMember{ACI: id, Role: signalmeow.GroupMember_DEFAULT})
	}
	deny, err := parse(u.Deny)
	if err != nil {
		return nil, err
	}
	for _, id := range deny {
		ch.DeleteRequestingMembers = append(ch.DeleteRequestingMembers, ptr.Ptr(id))
	}
	if u.OnlyAdminsSend != nil {
		ch.ModifyAnnouncementsOnly = ptr.Ptr(*u.OnlyAdminsSend)
	}
	if u.LinkMode != nil {
		ac := accessControl(*u.LinkMode)
		if ac == signalmeow.AccessControl_UNKNOWN || ac == signalmeow.AccessControl_MEMBER {
			return nil, errors.New("backend: group link mode must be any (no approval), admin (approval) or off")
		}
		ch.ModifyAddFromInviteLinkAccess = &ac
		if ac != signalmeow.AccessControl_UNSATISFIABLE && !hasLink {
			ch.ModifyInviteLinkPassword = ptr.Ptr(signalmeow.GenerateInviteLinkPassword())
		}
	}
	if u.ResetLink {
		ch.ModifyInviteLinkPassword = ptr.Ptr(signalmeow.GenerateInviteLinkPassword())
	}
	if u.ExpireTimer != nil {
		ch.ModifyDisappearingMessagesDuration = ptr.Ptr(uint32(*u.ExpireTimer / time.Second))
	}
	return ch, nil
}

// groupChangeEmpty reports whether a change does nothing.
func groupChangeEmpty(ch *signalmeow.GroupChange) bool {
	return ch.ModifyTitle == nil && ch.ModifyDescription == nil && ch.ModifyAvatar == nil &&
		len(ch.AddMembers) == 0 && len(ch.DeleteMembers) == 0 && len(ch.ModifyMemberRoles) == 0 &&
		len(ch.PromoteRequestingMembers) == 0 && len(ch.DeleteRequestingMembers) == 0 &&
		len(ch.PromotePendingMembers) == 0 && len(ch.DeletePendingMembers) == 0 &&
		ch.ModifyAnnouncementsOnly == nil && ch.ModifyAddFromInviteLinkAccess == nil &&
		ch.ModifyInviteLinkPassword == nil && ch.ModifyDisappearingMessagesDuration == nil
}

// UpdateGroup applies a group change on the server, notifies the members
// and records a system message.
func (b *Backend) UpdateGroup(ctx context.Context, id core.ChatID, u core.GroupUpdate) error {
	cli, err := b.client()
	if err != nil {
		return err
	}
	gid, err := groupIdentifier(id)
	if err != nil {
		return err
	}
	c, err := b.db.GetChat(ctx, id)
	if err != nil {
		return err
	}
	avatarPath := ""
	if len(u.Avatar) > 0 {
		if avatarPath, err = cli.UploadGroupAvatar(ctx, u.Avatar, gid, ""); err != nil {
			return fmt.Errorf("backend: upload group avatar: %w", err)
		}
	}
	ch, err := buildGroupChange(u, avatarPath, c.InviteLink != "")
	if err != nil {
		return err
	}
	if groupChangeEmpty(ch) {
		return nil
	}
	return b.applyGroupChange(ctx, cli, id, gid, ch)
}

// applyGroupChange submits ch and records the result locally.
func (b *Backend) applyGroupChange(ctx context.Context, cli *signalmeow.Client, id core.ChatID, gid types.GroupIdentifier, ch *signalmeow.GroupChange) error {
	rev, err := cli.UpdateGroup(ctx, ch, gid)
	if err != nil {
		return fmt.Errorf("backend: update group: %w", err)
	}
	ch.Revision = rev
	ch.SourceServiceID = cli.Store.ACIServiceID()
	self := b.selfACI()
	cc := convCtx{Self: self, Chat: id, Sender: self, ReceivedAt: b.now().UTC()}
	if err := b.storeSystem(ctx, cc, 0, b.nextTimestamp(), groupChangeText(ch, self, b.names(ctx)), false); err != nil {
		return err
	}
	if err := b.refreshGroup(ctx, id); err != nil {
		zerolog.Ctx(ctx).Debug().Err(err).Msg("Group refresh after change failed")
	}
	return nil
}

// LeaveGroup leaves a group (or declines an invitation to it).
func (b *Backend) LeaveGroup(ctx context.Context, id core.ChatID) error {
	cli, err := b.client()
	if err != nil {
		return err
	}
	gid, err := groupIdentifier(id)
	if err != nil {
		return err
	}
	c, err := b.db.GetChat(ctx, id)
	if err != nil {
		return err
	}
	self := b.selfACI()
	selfID := cli.Store.ACI
	ch := &signalmeow.GroupChange{}
	switch {
	case memberOf(c.Members, self):
		if c.SelfIsAdmin && len(c.Members) > 1 && adminCount(c.Members) == 1 {
			return errorf(core.ErrReadOnly, "make someone else an admin before leaving")
		}
		ch.DeleteMembers = []*uuid.UUID{ptr.Ptr(selfID)}
	case memberOf(c.PendingMembers, self):
		sid := libsignalgo.NewACIServiceID(selfID)
		ch.DeletePendingMembers = []*libsignalgo.ServiceID{&sid}
	case memberOf(c.RequestingJoin, self):
		ch.DeleteRequestingMembers = []*uuid.UUID{ptr.Ptr(selfID)}
	default:
		return errorf(core.ErrReadOnly, "you are not in this group")
	}
	if _, err := cli.UpdateGroup(ctx, ch, gid); err != nil {
		return fmt.Errorf("backend: leave group: %w", err)
	}
	cc := convCtx{Self: self, Chat: id, Sender: self, ReceivedAt: b.now().UTC()}
	if err := b.storeSystem(ctx, cc, 0, b.nextTimestamp(), "You left the group.", false); err != nil {
		return err
	}
	return b.markGroupLeft(ctx, id)
}

func adminCount(ms []core.Member) int {
	n := 0
	for _, m := range ms {
		if m.Admin {
			n++
		}
	}
	return n
}

// acceptGroupInvite accepts a pending invitation to a group.
func (b *Backend) acceptGroupInvite(ctx context.Context, cli *signalmeow.Client, id core.ChatID) error {
	c, err := b.db.GetChat(ctx, id)
	if err != nil {
		return err
	}
	if !memberOf(c.PendingMembers, b.selfACI()) {
		return nil
	}
	gid, err := groupIdentifier(id)
	if err != nil {
		return err
	}
	pk := profileKey(ctx, cli)
	if pk == nil {
		return errors.New("backend: own profile key unknown, can't accept invitation yet")
	}
	ch := &signalmeow.GroupChange{PromotePendingMembers: []*signalmeow.PromotePendingMember{{
		ACI: cli.Store.ACI, ProfileKey: libsignalgo.ProfileKey(pk),
	}}}
	return b.applyGroupChange(ctx, cli, id, gid, ch)
}

// CreateGroup creates a group with us as its admin.
func (b *Backend) CreateGroup(ctx context.Context, title string, members []core.ACI, avatar []byte) (core.ChatID, error) {
	cli, err := b.client()
	if err != nil {
		return "", err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return "", errors.New("backend: a group needs a name")
	}
	settings, _ := b.db.Settings(ctx)
	g := &signalmeow.Group{
		Title: title,
		AccessControl: &signalmeow.GroupAccessControl{
			Members:           signalmeow.AccessControl_MEMBER,
			Attributes:        signalmeow.AccessControl_MEMBER,
			AddFromInviteLink: signalmeow.AccessControl_UNSATISFIABLE,
		},
		DisappearingMessagesDuration: uint32(settings.DefaultExpireTimer / time.Second),
		Members:                      []*signalmeow.GroupMember{{ACI: cli.Store.ACI, Role: signalmeow.GroupMember_ADMINISTRATOR}},
	}
	seen := map[uuid.UUID]bool{cli.Store.ACI: true}
	for _, m := range members {
		u, err := parseACI(m)
		if err != nil {
			return "", err
		}
		if seen[u] {
			continue
		}
		seen[u] = true
		g.Members = append(g.Members, &signalmeow.GroupMember{ACI: u, Role: signalmeow.GroupMember_DEFAULT})
	}
	if len(avatar) > 0 {
		if _, err := signalmeow.PrepareGroupCreation(g); err != nil {
			return "", err
		}
		path, err := cli.UploadGroupAvatar(ctx, avatar, g.GroupIdentifier, g.GroupMasterKey)
		if err != nil {
			return "", fmt.Errorf("backend: upload group avatar: %w", err)
		}
		g.AvatarPath = path
	}
	created, err := cli.CreateGroup(ctx, g)
	if err != nil {
		return "", fmt.Errorf("backend: create group: %w", err)
	}
	if err := b.storeGroup(ctx, cli, created); err != nil {
		return "", err
	}
	id := groupChatID(created.GroupIdentifier)
	if len(avatar) > 0 {
		if c, err := b.db.GetChat(ctx, id); err == nil && c.AvatarID != "" {
			b.cacheAvatar(ctx, c.AvatarID, "group:"+string(created.GroupIdentifier), avatar, "", avatarSource{Kind: "group", Path: created.AvatarPath, Group: string(created.GroupIdentifier)})
		}
	}
	self := b.selfACI()
	cc := convCtx{Self: self, Chat: id, Sender: self, ReceivedAt: b.now().UTC()}
	if err := b.storeSystem(ctx, cc, 0, b.nextTimestamp(), groupCreatedText(self, self, b.names(ctx)), false); err != nil {
		return "", err
	}
	return id, nil
}
