package backend

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"go.mau.fi/mautrix-signal/pkg/libsignalgo"
	"go.mau.fi/mautrix-signal/pkg/signalmeow"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/types"

	"warte/internal/core"
)

// profileRefreshAfter is how stale a fetched profile may get before the
// worker fetches it again.
const profileRefreshAfter = 6 * time.Hour

// queueProfile asks the profile worker to (re)fetch aci's profile. It never
// blocks.
func (b *Backend) queueProfile(aci core.ACI) {
	if aci == "" {
		return
	}
	select {
	case b.profiles <- aci:
	default:
	}
}

// profileWorker fetches profiles one at a time once connected.
func (b *Backend) profileWorker() {
	ctx := b.log.WithContext(b.ctx)
	recent := map[core.ACI]time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case aci := <-b.profiles:
			if t, ok := recent[aci]; ok && b.now().Sub(t) < profileRefreshAfter {
				continue
			}
			if !b.waitConnected(ctx, 10*time.Minute) {
				if ctx.Err() != nil {
					return
				}
				continue
			}
			recent[aci] = b.now()
			if err := b.refreshProfile(ctx, aci); err != nil {
				zerolog.Ctx(ctx).Debug().Err(err).Msg("Profile refresh failed")
			}
			if !sleepCtx(ctx, 150*time.Millisecond) {
				return
			}
		}
	}
}

// refreshProfile fetches aci's profile and merges it into the contact.
func (b *Backend) refreshProfile(ctx context.Context, aci core.ACI) error {
	cli, err := b.client()
	if err != nil {
		return err
	}
	u, err := parseACI(aci)
	if err != nil {
		return err
	}
	r, err := cli.ContactByACIWithRefreshAfter(ctx, u, profileRefreshAfter)
	if err != nil || r == nil {
		return err
	}
	return b.mergeRecipient(ctx, r)
}

// mergeRecipient writes signalmeow's recipient data into warte's contact
// and announces changes.
func (b *Backend) mergeRecipient(ctx context.Context, r *types.Recipient) error {
	aci := aciOf(r.ACI)
	if aci == "" {
		return nil
	}
	existing, err := b.db.GetContact(ctx, aci)
	if err != nil && !errors.Is(err, core.ErrNotFound) {
		return err
	}
	c := recipientToContact(r, existing, b.selfACI())
	if r.Profile.AvatarPath != "" {
		b.registerAvatar(ctx, c.AvatarID, "avatar:"+string(aci), avatarSource{Kind: "profile", ACI: aci, Path: r.Profile.AvatarPath})
	} else if len(r.ContactAvatar.Image) > 0 {
		b.cacheAvatar(ctx, c.AvatarID, "avatar:"+string(aci), r.ContactAvatar.Image, r.ContactAvatar.ContentType, avatarSource{Kind: "contact", ACI: aci})
	}
	if c == existing && err == nil {
		return nil
	}
	if err := b.db.UpsertContact(ctx, c); err != nil {
		return err
	}
	if c.IsSelf {
		b.mu.Lock()
		b.self = c
		b.mu.Unlock()
	}
	b.emit(core.ContactUpdated{Contact: c})
	b.emitChat(ctx, core.DirectChatID(aci))
	return nil
}

// ResolveRecipient turns a phone number, username, username link, ACI or
// contact name into a direct chat, creating the chat if needed.
func (b *Backend) ResolveRecipient(ctx context.Context, query string) (core.ChatID, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return "", fmt.Errorf("backend: empty recipient: %w", core.ErrNotFound)
	}
	aci, seed, err := b.resolveACI(ctx, q)
	if err != nil {
		return "", err
	}
	return b.ensureDirectChat(ctx, aci, seed)
}

// resolveACI implements ResolveRecipient's lookup. seed carries what we
// learned about the contact (phone, username).
func (b *Backend) resolveACI(ctx context.Context, q string) (core.ACI, core.Contact, error) {
	if u, err := uuid.Parse(q); err == nil && u != uuid.Nil {
		return aciOf(u), core.Contact{ACI: aciOf(u)}, nil
	}
	lower := strings.ToLower(q)
	switch {
	case strings.Contains(lower, "signal.me/#eu/"):
		cli, err := b.client()
		if err != nil {
			return "", core.Contact{}, err
		}
		link := q[strings.Index(lower, "signal.me/#eu/")+len("signal.me/#eu/"):]
		username, err := cli.ResolveUsernameLink(ctx, signalmeow.SignalUserLinkPrefix+link)
		if err != nil {
			return "", core.Contact{}, fmt.Errorf("backend: resolve username link: %w", err)
		}
		if username == "" {
			return "", core.Contact{}, fmt.Errorf("backend: username link: %w", core.ErrNotFound)
		}
		return b.resolveUsername(ctx, username)
	case strings.Contains(lower, "signal.me/#p/"):
		num, err := url.PathUnescape(q[strings.Index(lower, "signal.me/#p/")+len("signal.me/#p/"):])
		if err != nil {
			return "", core.Contact{}, fmt.Errorf("%w: phone link", errBadID)
		}
		return b.resolvePhone(ctx, num)
	}
	if _, _, ok := parseE164(q); ok {
		return b.resolvePhone(ctx, q)
	}
	if u := strings.TrimPrefix(q, "@"); signalmeow.SignalUsernameRegex.MatchString(u) {
		return b.resolveUsername(ctx, u)
	}
	return b.resolveName(ctx, q)
}

func (b *Backend) resolvePhone(ctx context.Context, raw string) (core.ACI, core.Contact, error) {
	e164, num, ok := parseE164(raw)
	if !ok {
		return "", core.Contact{}, fmt.Errorf("%w: phone number must look like +4915…", errBadID)
	}
	if c, err := b.db.ContactByE164(ctx, e164); err == nil {
		return c.ACI, c, nil
	}
	cli, err := b.client()
	if err != nil {
		return "", core.Contact{}, err
	}
	if r, err := cli.Store.RecipientStore.LoadRecipientByE164(ctx, e164); err == nil && r != nil && r.ACI != uuid.Nil {
		return aciOf(r.ACI), core.Contact{ACI: aciOf(r.ACI), E164: e164}, nil
	}
	resp, err := cli.LookupPhone(ctx, num)
	if err != nil {
		return "", core.Contact{}, fmt.Errorf("backend: contact discovery: %w", err)
	}
	entry, ok := resp[num]
	if !ok || entry.ACI == uuid.Nil {
		return "", core.Contact{}, fmt.Errorf("backend: that number is not on Signal or hides its account: %w", core.ErrNotFound)
	}
	if _, err := cli.Store.RecipientStore.LoadAndUpdateRecipient(ctx, entry.ACI, entry.PNI, func(r *types.Recipient) (bool, error) {
		changed := r.E164 != e164
		r.E164 = e164
		return changed, nil
	}); err != nil {
		zerolog.Ctx(ctx).Debug().Err(err).Msg("Failed to record discovered recipient")
	}
	return aciOf(entry.ACI), core.Contact{ACI: aciOf(entry.ACI), E164: e164}, nil
}

func (b *Backend) resolveUsername(ctx context.Context, username string) (core.ACI, core.Contact, error) {
	if c, err := b.db.ContactByUsername(ctx, username); err == nil {
		return c.ACI, c, nil
	}
	cli, err := b.client()
	if err != nil {
		return "", core.Contact{}, err
	}
	sid, err := cli.ResolveUsername(ctx, username)
	if err != nil {
		return "", core.Contact{}, fmt.Errorf("backend: resolve username: %w", err)
	}
	if sid.IsEmpty() || sid.Type != libsignalgo.ServiceIDTypeACI {
		return "", core.Contact{}, fmt.Errorf("backend: username %q: %w", username, core.ErrNotFound)
	}
	return aciOf(sid.UUID), core.Contact{ACI: aciOf(sid.UUID), Username: username}, nil
}

// resolveName matches contacts by display name: an exact (case-insensitive)
// match wins, otherwise a unique prefix or substring match.
func (b *Backend) resolveName(ctx context.Context, name string) (core.ACI, core.Contact, error) {
	cs, err := b.db.ListContacts(ctx)
	if err != nil {
		return "", core.Contact{}, err
	}
	c, err := matchContact(cs, name)
	if err != nil {
		return "", core.Contact{}, err
	}
	return c.ACI, c, nil
}

// matchContact is resolveName's pure matcher.
func matchContact(cs []core.Contact, name string) (core.Contact, error) {
	needle := strings.ToLower(strings.TrimSpace(name))
	var prefix, contains []core.Contact
	for _, c := range cs {
		for _, n := range []string{c.DisplayName(), c.ProfileName, c.ContactName, c.Nickname, c.Username} {
			h := strings.ToLower(n)
			switch {
			case h == "":
			case h == needle:
				return c, nil
			case strings.HasPrefix(h, needle):
				prefix = appendUnique(prefix, c)
			case strings.Contains(h, needle):
				contains = appendUnique(contains, c)
			}
		}
	}
	switch {
	case len(prefix) == 1:
		return prefix[0], nil
	case len(prefix) > 1:
		return core.Contact{}, fmt.Errorf("backend: %q matches %d contacts, be more specific", name, len(prefix))
	case len(contains) == 1:
		return contains[0], nil
	case len(contains) > 1:
		return core.Contact{}, fmt.Errorf("backend: %q matches %d contacts, be more specific", name, len(contains))
	}
	return core.Contact{}, fmt.Errorf("backend: no contact named %q: %w", name, core.ErrNotFound)
}

func appendUnique(cs []core.Contact, c core.Contact) []core.Contact {
	for _, x := range cs {
		if x.ACI == c.ACI {
			return cs
		}
	}
	return append(cs, c)
}

// ensureDirectChat creates the contact (from seed) and chat for aci.
func (b *Backend) ensureDirectChat(ctx context.Context, aci core.ACI, seed core.Contact) (core.ChatID, error) {
	id := core.DirectChatID(aci)
	existing, err := b.db.GetContact(ctx, aci)
	switch {
	case errors.Is(err, core.ErrNotFound):
		seed.ACI, seed.IsSelf = aci, aci == b.selfACI()
		if err := b.db.UpsertContact(ctx, seed); err != nil {
			return "", err
		}
		b.emit(core.ContactUpdated{Contact: seed})
	case err != nil:
		return "", err
	default:
		upd := existing
		if upd.E164 == "" {
			upd.E164 = seed.E164
		}
		if upd.Username == "" {
			upd.Username = seed.Username
		}
		if upd != existing {
			if err := b.db.UpsertContact(ctx, upd); err == nil {
				b.emit(core.ContactUpdated{Contact: upd})
			}
		}
	}
	if _, err := b.db.GetChat(ctx, id); errors.Is(err, core.ErrNotFound) {
		c := core.Chat{ID: id, Kind: core.ChatDirect}
		if aci == b.selfACI() {
			c.Kind, c.Title = core.ChatNoteToSelf, "Note to Self"
		}
		if err := b.db.UpsertChat(ctx, c); err != nil {
			return "", err
		}
		b.emitChat(ctx, id)
	} else if err != nil {
		return "", err
	}
	b.queueProfile(aci)
	return id, nil
}

// safetyNumberIterations and version match Signal's ACI-based safety numbers.
const safetyNumberIterations = 5200

// SafetyNumber computes the safety number (60 digits plus the scannable QR
// payload) between us and aci.
func (b *Backend) SafetyNumber(ctx context.Context, aci core.ACI) (core.SafetyNumber, error) {
	cli, err := b.client()
	if err != nil {
		return core.SafetyNumber{}, err
	}
	u, err := parseACI(aci)
	if err != nil {
		return core.SafetyNumber{}, err
	}
	sid := libsignalgo.NewACIServiceID(u)
	theirs, err := cli.Store.IdentityKeyStore.GetIdentityKey(ctx, sid)
	if err != nil {
		return core.SafetyNumber{}, err
	}
	if theirs == nil {
		// No session yet: fetch their identity from a prekey bundle.
		if err := cli.FetchAndProcessPreKey(ctx, sid, -1); err != nil {
			return core.SafetyNumber{}, fmt.Errorf("backend: fetch identity key: %w", err)
		}
		if theirs, err = cli.Store.IdentityKeyStore.GetIdentityKey(ctx, sid); err != nil || theirs == nil {
			return core.SafetyNumber{}, fmt.Errorf("backend: no identity key for contact: %w", core.ErrNotFound)
		}
	}
	ours := cli.Store.ACIIdentityKeyPair.GetIdentityKey()
	digits, qr, err := computeSafetyNumber(cli.Store.ACI, ours, u, theirs)
	if err != nil {
		return core.SafetyNumber{}, err
	}
	sn := core.SafetyNumber{Digits: digits, QRData: qr}
	if c, err := b.db.GetContact(ctx, aci); err == nil {
		sn.Verified = c.Verified
	}
	return sn, nil
}

// computeSafetyNumber derives the displayable and scannable fingerprints.
func computeSafetyNumber(ourACI uuid.UUID, ours *libsignalgo.IdentityKey, theirACI uuid.UUID, theirs *libsignalgo.IdentityKey) (string, []byte, error) {
	ourPub, err := identityPublicKey(ours)
	if err != nil {
		return "", nil, err
	}
	theirPub, err := identityPublicKey(theirs)
	if err != nil {
		return "", nil, err
	}
	fp, err := libsignalgo.NewFingerprint(safetyNumberIterations, libsignalgo.FingerprintVersionV2, ourACI[:], ourPub, theirACI[:], theirPub)
	if err != nil {
		return "", nil, fmt.Errorf("backend: fingerprint: %w", err)
	}
	digits, err := fp.DisplayString()
	if err != nil {
		return "", nil, err
	}
	qr, err := fp.ScannableEncoding()
	if err != nil {
		return "", nil, err
	}
	return digits, qr, nil
}

func identityPublicKey(k *libsignalgo.IdentityKey) (*libsignalgo.PublicKey, error) {
	raw, err := k.Serialize()
	if err != nil {
		return nil, err
	}
	return libsignalgo.DeserializePublicKey(raw)
}

// SetVerified marks a contact's safety number as verified (or not) and
// tells our other devices.
func (b *Backend) SetVerified(ctx context.Context, aci core.ACI, verified bool) error {
	cli, err := b.client()
	if err != nil {
		return err
	}
	u, err := parseACI(aci)
	if err != nil {
		return err
	}
	c, err := b.db.GetContact(ctx, aci)
	if err != nil {
		return err
	}
	c.Verified = verified
	if err := b.db.UpsertContact(ctx, c); err != nil {
		return err
	}
	b.emit(core.ContactUpdated{Contact: c})
	if key, err := cli.Store.IdentityKeyStore.GetIdentityKey(ctx, libsignalgo.NewACIServiceID(u)); err == nil && key != nil {
		if raw, err := key.Serialize(); err == nil {
			if sm, err := syncVerified(aci, raw, verified); err == nil {
				return b.sendSync(ctx, cli, sm)
			}
		}
	}
	return nil
}

// SetNickname sets the nickname of a contact here and writes it to the
// contact's storage record so other devices see it.
func (b *Backend) SetNickname(ctx context.Context, aci core.ACI, nickname string) error {
	nickname = strings.TrimSpace(nickname)
	c, err := b.db.GetContact(ctx, aci)
	if err != nil {
		return err
	}
	c.Nickname = nickname
	if err := b.db.UpsertContact(ctx, c); err != nil {
		return err
	}
	if cli, err := b.client(); err == nil {
		if u, err := parseACI(aci); err == nil {
			_, _ = cli.Store.RecipientStore.LoadAndUpdateRecipient(ctx, u, uuid.Nil, func(r *types.Recipient) (bool, error) {
				changed := r.Nickname != nickname
				r.Nickname = nickname
				return changed, nil
			})
		}
	}
	b.emit(core.ContactUpdated{Contact: c})
	b.emitChat(ctx, core.DirectChatID(aci))
	b.queueStorage(nicknameStorageOp(aci, b.recordSeed(ctx, core.DirectChatID(aci)), nickname)) // warte: storage write-back
	return nil
}

// Block blocks or unblocks a chat and syncs the full blocked list to our
// other devices.
func (b *Backend) Block(ctx context.Context, id core.ChatID, block bool) error {
	cli, err := b.client()
	if err != nil {
		return err
	}
	if _, err := b.db.UpdateChat(ctx, id, func(c *core.Chat) error {
		c.Blocked = block
		if !block {
			c.MessageRequest = false
		}
		return nil
	}); err != nil {
		return err
	}
	if id.IsDirect() {
		if u, err := parseACI(id.ACI()); err == nil {
			_, _ = cli.Store.RecipientStore.LoadAndUpdateRecipient(ctx, u, uuid.Nil, func(r *types.Recipient) (bool, error) {
				changed := r.Blocked != block
				r.Blocked = block
				return changed, nil
			})
		}
		_ = b.setContactBlocked(ctx, id.ACI(), block)
	}
	b.emitChat(ctx, id)
	b.queueChatStorage(ctx, id, chatStorageFields{Blocked: &block}) // warte: storage write-back
	acis, groups, err := b.blockedList(ctx)
	if err != nil {
		return err
	}
	if err := b.sendSync(ctx, cli, syncBlocked(acis, groups)); err != nil {
		return err
	}
	typ := signalpb.SyncMessage_MessageRequestResponse_BLOCK
	if !block {
		typ = signalpb.SyncMessage_MessageRequestResponse_ACCEPT
	}
	if sm, err := syncMessageRequestResponse(id, typ); err == nil {
		return b.sendSync(ctx, cli, sm)
	}
	return nil
}

// blockedList collects every blocked contact and group.
func (b *Backend) blockedList(ctx context.Context) ([]core.ACI, []core.ChatID, error) {
	chats, err := b.allChats(ctx)
	if err != nil {
		return nil, nil, err
	}
	var acis []core.ACI
	var groups []core.ChatID
	seen := map[core.ACI]bool{}
	for _, c := range chats {
		if !c.Blocked {
			continue
		}
		if c.ID.IsGroup() {
			groups = append(groups, c.ID)
		} else if aci := c.ID.ACI(); !seen[aci] {
			seen[aci] = true
			acis = append(acis, aci)
		}
	}
	cs, err := b.db.ListContacts(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, c := range cs {
		if c.Blocked && !seen[c.ACI] {
			seen[c.ACI] = true
			acis = append(acis, c.ACI)
		}
	}
	return acis, groups, nil
}

// AcceptMessageRequest accepts a message request: shares our profile with
// the sender (or joins the group we were invited to) and syncs the choice.
func (b *Backend) AcceptMessageRequest(ctx context.Context, id core.ChatID) error {
	cli, err := b.client()
	if err != nil {
		return err
	}
	c, err := b.db.GetChat(ctx, id)
	if err != nil {
		return err
	}
	if id.IsGroup() {
		if err := b.acceptGroupInvite(ctx, cli, id); err != nil {
			return err
		}
	} else if u, err := parseACI(id.ACI()); err == nil {
		_, _ = cli.Store.RecipientStore.LoadAndUpdateRecipient(ctx, u, uuid.Nil, func(r *types.Recipient) (bool, error) {
			r.Whitelisted = new(bool)
			*r.Whitelisted = true
			r.Blocked = false
			return true, nil
		})
		if pk := profileKey(ctx, cli); pk != nil && id.ACI() != b.selfACI() {
			res := cli.SendMessage(ctx, libsignalgo.NewACIServiceID(u), signalmeow.WrapDataMessage(buildProfileKeyUpdate(b.nextTimestamp(), pk)))
			if !res.WasSuccessful {
				zerolog.Ctx(ctx).Debug().Err(res.Error).Msg("Failed to share profile key")
			}
		}
	}
	if _, err := b.db.UpdateChat(ctx, c.ID, func(ch *core.Chat) error { ch.MessageRequest, ch.Blocked = false, false; return nil }); err != nil {
		return err
	}
	b.emitChat(ctx, id)
	accepted := true
	b.queueChatStorage(ctx, id, chatStorageFields{Accepted: &accepted}) // warte: storage write-back
	sm, err := syncMessageRequestResponse(id, signalpb.SyncMessage_MessageRequestResponse_ACCEPT)
	if err != nil {
		return err
	}
	return b.sendSync(ctx, cli, sm)
}

// ReportSpam reports the newest message of a chat's sender as spam and
// syncs the report. The chat is left as is; Block separately if wanted.
func (b *Backend) ReportSpam(ctx context.Context, id core.ChatID) error {
	cli, err := b.client()
	if err != nil {
		return err
	}
	var author core.ACI
	if id.IsDirect() {
		author = id.ACI()
	} else {
		msgs, err := b.db.ListMessages(ctx, id, core.MessageID{}, 50)
		if err != nil {
			return err
		}
		for i := len(msgs) - 1; i >= 0; i-- {
			if !msgs[i].Outgoing && msgs[i].Kind == core.KindNormal {
				author = msgs[i].ID.Author
				break
			}
		}
	}
	if author == "" {
		return fmt.Errorf("backend: no message to report: %w", core.ErrNotFound)
	}
	guid, token, err := b.index.spamReport(ctx, id, author)
	if err != nil {
		return err
	}
	if guid == "" {
		return fmt.Errorf("backend: no reportable message from this sender: %w", core.ErrNotFound)
	}
	u, err := parseACI(author)
	if err != nil {
		return err
	}
	if err := cli.ReportSpam(ctx, u, guid, token); err != nil {
		return fmt.Errorf("backend: report spam: %w", err)
	}
	if sm, err := syncMessageRequestResponse(id, signalpb.SyncMessage_MessageRequestResponse_SPAM); err == nil {
		return b.sendSync(ctx, cli, sm)
	}
	return nil
}

// sendSync sends a sync message to our own other devices.
func (b *Backend) sendSync(ctx context.Context, cli *signalmeow.Client, sm *signalpb.SyncMessage) error {
	res := cli.SendMessage(ctx, cli.Store.ACIServiceID(), signalmeow.WrapSyncMessage(sm))
	if !res.WasSuccessful {
		return fmt.Errorf("backend: sync to own devices: %w", res.Error)
	}
	return nil
}
