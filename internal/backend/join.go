package backend

import (
	"context"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/rs/zerolog"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-signal/pkg/libsignalgo"
	"go.mau.fi/mautrix-signal/pkg/signalmeow"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/types"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/web"

	"warte/internal/core"
)

// Joining groups by invite link.
//
// signalmeow has no API for it, so this file talks to the groups service
// directly, using only signalmeow's exported building blocks (group auth,
// the expiring profile key credential, web.SendHTTPRequest) and libsignal's
// zkgroup. The flow follows Signal-Android's GroupManagerV2.GroupJoiner:
//
//  1. Decode the link: https://signal.group/#<base64url GroupInviteLink>
//     carries the group master key and the invite link password.
//  2. GET /v1/groups/join/<password> (group auth) → GroupJoinInfo: title,
//     revision, and whether joining needs admin approval.
//  3. PATCH /v2/groups/?inviteLinkPassword=<password> with an AddMembers
//     action (open link) or AddMembersPendingAdminApproval (approval link),
//     each carrying a presentation of our expiring profile key credential,
//     at revision+1.
//  4. On a direct join, tell the members (GroupContextV2 with the signed
//     change) and fetch the group; on a request, record the pending group.

//go:embed zkgroup-server-public-params.dat
var serverPublicParamsBytes []byte

// serverPublicParams are Signal's production zkgroup parameters (a copy of
// the file signalmeow embeds; signalmeow keeps its parsed copy private).
var serverPublicParams = sync.OnceValues(func() (*libsignalgo.ServerPublicParams, error) {
	return libsignalgo.DeserializeServerPublicParams(serverPublicParamsBytes)
})

// errAlreadyRequested reports a join request that is still waiting for an
// admin.
var errAlreadyRequested = errors.New("backend: you already asked to join this group; an admin has to approve it")

// groupInvite is the content of a signal.group link.
type groupInvite struct {
	MasterKey libsignalgo.GroupMasterKey
	Password  []byte
}

// parseGroupInviteLink decodes https://signal.group/#… (also sgnl://joingroup
// links and bare fragments with the scheme left off).
func parseGroupInviteLink(link string) (groupInvite, error) {
	link = strings.TrimSpace(link)
	var frag string
	if u, err := url.Parse(link); err == nil && u.Fragment != "" &&
		(strings.EqualFold(u.Host, "signal.group") || (strings.EqualFold(u.Scheme, "sgnl") && strings.EqualFold(u.Host, "joingroup"))) {
		frag = u.Fragment
	} else if i := strings.Index(link, "signal.group/#"); i >= 0 {
		frag = link[i+len("signal.group/#"):]
	}
	if frag == "" {
		return groupInvite{}, fmt.Errorf("%w: not a signal.group link", errBadID)
	}
	frag = strings.TrimRight(frag, "=")
	raw, err := base64.RawURLEncoding.DecodeString(frag)
	if err != nil {
		// Some clients produce standard base64.
		if raw, err = base64.RawStdEncoding.DecodeString(frag); err != nil {
			return groupInvite{}, fmt.Errorf("%w: malformed group link", errBadID)
		}
	}
	var gl signalpb.GroupInviteLink
	if err := proto.Unmarshal(raw, &gl); err != nil {
		return groupInvite{}, fmt.Errorf("%w: malformed group link", errBadID)
	}
	v1 := gl.GetContentsV1()
	if v1 == nil || len(v1.GetGroupMasterKey()) != libsignalgo.GroupMasterKeyLength || len(v1.GetInviteLinkPassword()) == 0 {
		return groupInvite{}, fmt.Errorf("%w: unsupported group link", errBadID)
	}
	return groupInvite{MasterKey: libsignalgo.GroupMasterKey(v1.GetGroupMasterKey()), Password: v1.GetInviteLinkPassword()}, nil
}

// buildJoinActions builds the group change that joins (or asks to join) a
// group at revision with our credential presentation.
func buildJoinActions(presentation []byte, revision uint32, needsApproval bool) *signalpb.GroupChange_Actions {
	a := &signalpb.GroupChange_Actions{Version: revision}
	if needsApproval {
		a.AddMembersPendingAdminApproval = []*signalpb.GroupChange_Actions_AddMemberPendingAdminApprovalAction{{
			Added: &signalpb.MemberPendingAdminApproval{Presentation: presentation},
		}}
	} else {
		a.AddMembers = []*signalpb.GroupChange_Actions_AddMemberAction{{
			Added:              &signalpb.Member{Role: signalpb.Member_DEFAULT, Presentation: presentation},
			JoinFromInviteLink: true,
		}}
	}
	return a
}

// joinAccess interprets GroupJoinInfo.AddFromInviteLink.
func joinAccess(info *signalpb.GroupJoinInfo) (needsApproval bool, err error) {
	switch info.GetAddFromInviteLink() {
	case signalpb.AccessControl_ANY:
		return false, nil
	case signalpb.AccessControl_ADMINISTRATOR:
		return true, nil
	}
	return false, errorf(core.ErrReadOnly, "this group link is disabled")
}

// decryptJoinTitle decrypts the title of a GroupJoinInfo.
func decryptJoinTitle(gsp libsignalgo.GroupSecretParams, enc []byte) string {
	if len(enc) == 0 {
		return ""
	}
	plain, err := gsp.DecryptBlobWithPadding(enc)
	if err != nil {
		return ""
	}
	var blob signalpb.GroupAttributeBlob
	if proto.Unmarshal(plain, &blob) != nil {
		return ""
	}
	return blob.GetTitle()
}

// groupsHTTPError maps a groups service status to an error.
func groupsHTTPError(op string, status int) error {
	switch status {
	case http.StatusForbidden:
		return errorf(core.ErrReadOnly, "%s: the group link was reset or you were removed from the group", op)
	case http.StatusNotFound:
		return fmt.Errorf("backend: %s: group link: %w", op, core.ErrNotFound)
	case http.StatusConflict:
		return fmt.Errorf("backend: %s: the group changed meanwhile, try again", op)
	case http.StatusTooManyRequests:
		return fmt.Errorf("backend: %s: rate-limited by Signal, try again later", op)
	case http.StatusBadRequest:
		return fmt.Errorf("backend: %s: request rejected by Signal", op)
	}
	return fmt.Errorf("backend: %s: HTTP %d", op, status)
}

// groupsRequest performs one authenticated groups service request and
// returns the body of a 200 response.
func groupsRequest(ctx context.Context, cli *signalmeow.Client, mk libsignalgo.GroupMasterKey, method, path string, body []byte, op string) ([]byte, error) {
	auth, err := cli.GetAuthorizationForToday(ctx, mk)
	if err != nil {
		return nil, fmt.Errorf("backend: %s: group auth: %w", op, err)
	}
	opts := &web.HTTPReqOpt{Username: &auth.Username, Password: &auth.Password, ContentType: web.ContentTypeProtobuf, Body: body}
	resp, err := web.SendHTTPRequest(ctx, web.StorageHostname, method, path, opts)
	defer web.CloseBody(resp)
	if err != nil {
		return nil, fmt.Errorf("backend: %s: %w", op, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, groupsHTTPError(op, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}

// JoinGroupByLink joins a group through its invite link, or asks its admins
// to let us in when the link requires approval. The returned chat is
// read-only until the request is approved.
func (b *Backend) JoinGroupByLink(ctx context.Context, link string) (core.ChatID, error) {
	inv, err := parseGroupInviteLink(link)
	if err != nil {
		return "", err
	}
	cli, err := b.client()
	if err != nil {
		return "", err
	}
	gidRaw, err := inv.MasterKey.GroupIdentifier()
	if err != nil {
		return "", fmt.Errorf("backend: group link: %w", err)
	}
	gid := types.BytesToGroupIdentifier(gidRaw)
	chat := groupChatID(gid)
	self := b.selfACI()
	if c, err := b.db.GetChat(ctx, chat); err == nil && memberOf(c.Members, self) {
		return chat, nil // already a member
	}
	ctx = zerolog.Ctx(ctx).With().Str("chat", string(chat)).Logger().WithContext(ctx)

	gsp, err := libsignalgo.DeriveGroupSecretParamsFromMasterKey(inv.MasterKey)
	if err != nil {
		return "", fmt.Errorf("backend: group link: %w", err)
	}
	pw := base64.RawURLEncoding.EncodeToString(inv.Password)
	raw, err := groupsRequest(ctx, cli, inv.MasterKey, http.MethodGet, "/v1/groups/join/"+pw, nil, "group join info")
	if err != nil {
		return "", err
	}
	var info signalpb.GroupJoinInfo
	if err := proto.Unmarshal(raw, &info); err != nil {
		return "", fmt.Errorf("backend: group join info: %w", err)
	}
	if info.GetPendingAdminApproval() {
		return chat, errAlreadyRequested
	}
	needsApproval, err := joinAccess(&info)
	if err != nil {
		return "", err
	}

	spp, err := serverPublicParams()
	if err != nil {
		return "", fmt.Errorf("backend: zkgroup parameters: %w", err)
	}
	cred, err := cli.FetchExpiringProfileKeyCredentialById(ctx, cli.Store.ACI)
	if err != nil {
		return "", fmt.Errorf("backend: own profile key credential: %w", err)
	}
	pres, err := gsp.CreateExpiringProfileKeyCredentialPresentation(spp, *cred)
	if err != nil {
		return "", fmt.Errorf("backend: credential presentation: %w", err)
	}
	actions := buildJoinActions(*pres, info.GetVersion()+1, needsApproval)
	body, err := proto.Marshal(actions)
	if err != nil {
		return "", err
	}
	raw, err = groupsRequest(ctx, cli, inv.MasterKey, http.MethodPatch, "/v2/groups/?inviteLinkPassword="+pw, body, "join group")
	if err != nil {
		return "", err
	}
	var resp signalpb.GroupChangeResponse
	if err := proto.Unmarshal(raw, &resp); err != nil {
		return "", fmt.Errorf("backend: join group response: %w", err)
	}
	if _, err := cli.StoreMasterKey(ctx, types.SerializedGroupMasterKey(base64.StdEncoding.EncodeToString(inv.MasterKey[:]))); err != nil {
		return "", fmt.Errorf("backend: store group key: %w", err)
	}
	revision := info.GetVersion() + 1
	title := decryptJoinTitle(gsp, info.GetTitle())
	ts := b.nextTimestamp()
	cc := convCtx{Self: self, Chat: chat, Sender: self, ReceivedAt: b.now().UTC()}

	if needsApproval {
		if err := b.recordJoinRequest(ctx, chat, title, revision); err != nil {
			return "", err
		}
		return chat, b.storeSystem(ctx, cc, 0, ts, "You sent a request to join the group.", false)
	}

	cli.GroupCache.Delete(gid)
	g, _, err := cli.RetrieveGroupByID(ctx, gid, 0)
	if err != nil {
		// Joined, but the fetch failed: record what we know and refresh later.
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Joined group but could not fetch it")
		if err := b.recordJoinRequest(ctx, chat, title, revision); err != nil {
			return "", err
		}
		b.queueGroupRefresh(chat)
	} else {
		if err := b.storeGroup(ctx, cli, g); err != nil {
			return "", err
		}
		if changeBytes, err := proto.Marshal(resp.GetGroupChange()); err == nil && resp.GetGroupChange() != nil {
			gctx := &signalpb.GroupContextV2{MasterKey: inv.MasterKey[:], Revision: proto.Uint32(revision), GroupChange: changeBytes}
			if _, err := cli.SendGroupUpdate(ctx, g, gctx, nil); err != nil {
				zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to announce group join to members")
			}
		}
	}
	return chat, b.storeSystem(ctx, cc, 0, ts, "You joined the group via the group link.", false)
}

// recordJoinRequest stores a group we asked to join (or joined without
// being able to fetch it yet) with what the join info revealed.
func (b *Backend) recordJoinRequest(ctx context.Context, chat core.ChatID, title string, revision uint32) error {
	self := b.selfACI()
	c, err := b.db.GetChat(ctx, chat)
	if errors.Is(err, core.ErrNotFound) {
		c = core.Chat{ID: chat, Kind: core.ChatGroup}
	} else if err != nil {
		return err
	}
	if title != "" {
		c.Title = title
	}
	if c.Title == "" {
		c.Title = "Unknown group"
	}
	if !memberOf(c.RequestingJoin, self) {
		c.RequestingJoin = append(c.RequestingJoin, core.Member{ACI: self, Name: b.nameOf(ctx, self)})
	}
	c.GroupRevision = max(c.GroupRevision, revision)
	if err := b.db.UpsertChat(ctx, c); err != nil {
		return err
	}
	b.emitChat(ctx, chat)
	return nil
}
