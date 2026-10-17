// warte: this file adds sending stories (Content.storyMessage) to
// signalmeow, which upstream only receives. Reference behaviour:
// Signal-Desktop ts/util/sendStoryMessage.preload.ts,
// ts/jobs/helpers/sendStory.preload.ts, ts/util/sendToGroup.preload.ts
// (story=true paths), ts/util/deleteStoryForEveryone.preload.ts and
// ts/jobs/helpers/sendDeleteStoryForEveryone.preload.ts; server semantics
// from Signal-Server MessageController (story=true: no access checks, 200
// for unknown recipients, group send tokens rejected).

package signalmeow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"go.mau.fi/util/exslices"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-signal/pkg/libsignalgo"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/store"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/types"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/web"
)

// warte: MyStoryID is the distribution list ID of "My Story" (Signal's
// MY_STORY_ID, all zeros).
var MyStoryID = uuid.Nil

// warte: default text story colors (Signal-Desktop TextStoryCreator BG1
// and white text).
const (
	DefaultStoryBackground uint32 = 0xFF688BD4 // 4285041620
	DefaultStoryForeground uint32 = 0xFFFFFFFF
	// DefaultStoryGradientAngle is the angle of Desktop's gradients.
	DefaultStoryGradientAngle uint32 = 180
	// MaxTextStoryLength bounds the text of a text story (Signal clients
	// limit it to 700 characters in their editors).
	MaxTextStoryLength = 700
)

// --- send options carried in the context ---------------------------------------

// warte: warteSendOpts changes how sendContent sends.
type warteSendOpts struct {
	// story sends with story semantics: PUT /v1/messages/{id}?story=true,
	// always sealed sender, no Unidentified-Access-Key.
	story bool
	// notUrgent forces urgent=false.
	notUrgent bool
}

type warteSendOptsKey struct{}

func warteSendOptsFrom(ctx context.Context) warteSendOpts {
	o, _ := ctx.Value(warteSendOptsKey{}).(warteSendOpts)
	return o
}

// withStorySend marks sends in ctx as story sends.
func withStorySend(ctx context.Context) context.Context {
	o := warteSendOptsFrom(ctx)
	o.story = true
	return context.WithValue(ctx, warteSendOptsKey{}, o)
}

// withoutStorySend clears the story flag (for sync copies to ourselves).
func withoutStorySend(ctx context.Context) context.Context {
	o := warteSendOptsFrom(ctx)
	o.story = false
	return context.WithValue(ctx, warteSendOptsKey{}, o)
}

// withNonUrgentSend marks sends in ctx as non-urgent.
func withNonUrgentSend(ctx context.Context) context.Context {
	o := warteSendOptsFrom(ctx)
	o.notUrgent = true
	return context.WithValue(ctx, warteSendOptsKey{}, o)
}

// --- distribution lists and audiences ---------------------------------------------

// warte: StoryPrivacy is a story distribution list as stored in the
// storage service (StoryDistributionListRecord).
//
// For "My Story" the record's isBlockList flag selects the mode:
//   - isBlockList, no members:  all Signal connections
//   - isBlockList, members:     all Signal connections except the members
//   - !isBlockList:             only the members
//
// Other lists always mean "only the members" (Signal-Desktop ignores
// isBlockList for them).
type StoryPrivacy struct {
	ListID        uuid.UUID
	Name          string
	IsBlockList   bool
	Members       []libsignalgo.ServiceID
	AllowsReplies bool
	Deleted       bool
}

// warte: StoryPrivacyMode is the "who can see My Story" mode.
type StoryPrivacyMode int

const (
	StoryAllConnections StoryPrivacyMode = iota
	StoryAllExcept
	StoryOnlyWith
)

// Mode reports the privacy mode.
func (p StoryPrivacy) Mode() StoryPrivacyMode {
	switch {
	case p.ListID != MyStoryID || !p.IsBlockList:
		return StoryOnlyWith
	case len(p.Members) > 0:
		return StoryAllExcept
	}
	return StoryAllConnections
}

// DefaultMyStory is "My Story" when the storage service has no record for
// it: all Signal connections, replies allowed (Signal's defaults).
func DefaultMyStory() StoryPrivacy {
	return StoryPrivacy{ListID: MyStoryID, Name: "My Story", IsBlockList: true, AllowsReplies: true}
}

// StoryPrivacyFromRecord parses a StoryDistributionListRecord. ok is false
// for records without a valid 16-byte identifier.
func StoryPrivacyFromRecord(r *signalpb.StoryDistributionListRecord) (p StoryPrivacy, ok bool) {
	id, err := uuid.FromBytes(r.GetIdentifier())
	if err != nil {
		return StoryPrivacy{}, false
	}
	p = StoryPrivacy{
		ListID:        id,
		Name:          r.GetName(),
		IsBlockList:   r.GetIsBlockList(),
		AllowsReplies: r.GetAllowsReplies(),
		Deleted:       r.GetDeletedAtTimestamp() != 0,
	}
	seen := map[libsignalgo.ServiceID]bool{}
	add := func(sid libsignalgo.ServiceID, err error) {
		if err != nil || sid.IsEmpty() || sid.UUID == uuid.Nil || seen[sid] {
			return
		}
		seen[sid] = true
		p.Members = append(p.Members, sid)
	}
	for _, s := range r.GetRecipientServiceIds() {
		add(libsignalgo.ServiceIDFromString(s))
	}
	for _, b := range r.GetRecipientServiceIdsBinary() {
		add(libsignalgo.ServiceIDFromBytes(b))
	}
	return p, true
}

// MyStoryPrivacy picks "My Story" out of the storage service's
// distribution lists, or the default if there is none.
func MyStoryPrivacy(lists []*signalpb.StoryDistributionListRecord) StoryPrivacy {
	for _, r := range lists {
		if p, ok := StoryPrivacyFromRecord(r); ok && p.ListID == MyStoryID {
			p.Deleted = false // My Story can't be deleted
			return p
		}
	}
	return DefaultMyStory()
}

// StoryDistributionLists returns the story distribution lists from the
// storage service. It returns ErrNoStorageKey or ErrStorageNoManifest
// when there is no storage to read.
func (cli *Client) StoryDistributionLists(ctx context.Context) ([]*signalpb.StoryDistributionListRecord, error) {
	if len(cli.Store.MasterKey) == 0 {
		return nil, ErrNoStorageKey
	}
	cli.storageWriteLock.Lock()
	defer cli.storageWriteLock.Unlock()
	state, err := cli.loadStorageState(ctx, cli.storageTransport(), deriveStorageServiceKey(cli.Store.MasterKey))
	if err != nil {
		return nil, err
	}
	var out []*signalpb.StoryDistributionListRecord
	for _, e := range state.Entries() {
		if l := e.Record.GetStoryDistributionList(); l != nil {
			out = append(out, l)
		}
	}
	return out, nil
}

// StoryAudience computes who receives a story sent to the list p.
// connections are the account's "Signal connections" (Signal-Desktop
// isSignalConnection: profile sharing or system contact, not blocked, not
// unregistered); they are only used by My Story's block-list modes.
// exclude drops recipients that must never receive stories (self,
// blocked, unregistered); it may be nil. Only ACIs are returned: stories
// are sealed-sender only and PNIs can't receive sealed sender messages.
// The result is deduplicated and keeps input order.
func StoryAudience(p StoryPrivacy, connections []libsignalgo.ServiceID, exclude func(libsignalgo.ServiceID) bool) []libsignalgo.ServiceID {
	var candidates []libsignalgo.ServiceID
	switch p.Mode() {
	case StoryOnlyWith:
		candidates = p.Members
	default:
		excluded := map[libsignalgo.ServiceID]bool{}
		for _, m := range p.Members {
			excluded[m] = true
		}
		for _, c := range connections {
			if !excluded[c] {
				candidates = append(candidates, c)
			}
		}
	}
	seen := map[libsignalgo.ServiceID]bool{}
	var out []libsignalgo.ServiceID
	for _, c := range candidates {
		if c.Type != libsignalgo.ServiceIDTypeACI || c.UUID == uuid.Nil || seen[c] {
			continue
		}
		seen[c] = true
		if exclude != nil && exclude(c) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// StoryConnections returns the recipients signalmeow knows as Signal
// connections: profile sharing (whitelisted) or a system contact name,
// not blocked, not unregistered, and not ourselves.
func (cli *Client) StoryConnections(ctx context.Context) ([]libsignalgo.ServiceID, error) {
	all, err := cli.Store.RecipientStore.LoadAllContacts(ctx)
	if err != nil {
		return nil, err
	}
	var out []libsignalgo.ServiceID
	for _, r := range all {
		if !isStoryConnection(r, cli.Store.ACI) {
			continue
		}
		sid := libsignalgo.NewACIServiceID(r.ACI)
		if cli.Store.RecipientStore.IsUnregistered(ctx, sid) {
			continue
		}
		out = append(out, sid)
	}
	return out, nil
}

// isStoryConnection is Signal-Desktop's isSignalConnection on a
// signalmeow recipient (registration is checked separately).
func isStoryConnection(r *types.Recipient, self uuid.UUID) bool {
	if r == nil || r.ACI == uuid.Nil || r.ACI == self || r.Blocked {
		return false
	}
	return (r.Whitelisted != nil && *r.Whitelisted) || r.ContactName != ""
}

// --- content ------------------------------------------------------------------------

// warte: TextStory describes a text story (TextAttachment).
type TextStory struct {
	Text  string
	Style signalpb.TextAttachment_Style
	// Foreground is the ARGB text color (0 = white).
	Foreground uint32
	// TextBackground is the ARGB color behind the text (nil = none).
	TextBackground *uint32
	// Background is a solid ARGB background (used when Gradient has fewer
	// than two colors; 0 = Signal's default blue).
	Background uint32
	// Gradient colors (ARGB), with optional Positions (0..1, same length)
	// and Angle in degrees (0 = 180, Desktop's angle).
	Gradient  []uint32
	Positions []float32
	Angle     uint32
	// Link preview; a text story needs Text or PreviewURL.
	PreviewURL         string
	PreviewTitle       string
	PreviewDescription string
	PreviewImage       *signalpb.AttachmentPointer
}

// ErrEmptyStory is returned for a story without content.
var ErrEmptyStory = errors.New("story has no content")

// BuildTextAttachment builds the TextAttachment of a text story the way
// Signal-Desktop's getTextAttachmentProto does: gradients carry the
// deprecated startColor/endColor next to colors/positions/angle.
func BuildTextAttachment(t TextStory) (*signalpb.TextAttachment, error) {
	text := strings.TrimSpace(t.Text)
	if text == "" && t.PreviewURL == "" {
		return nil, ErrEmptyStory
	}
	if len([]rune(t.Text)) > MaxTextStoryLength {
		return nil, fmt.Errorf("text story is longer than %d characters", MaxTextStoryLength)
	}
	style := t.Style
	if _, known := signalpb.TextAttachment_Style_name[int32(style)]; !known {
		style = signalpb.TextAttachment_DEFAULT
	}
	fg := t.Foreground
	if fg == 0 {
		fg = DefaultStoryForeground
	}
	ta := &signalpb.TextAttachment{
		TextStyle:           style.Enum(),
		TextForegroundColor: proto.Uint32(fg),
	}
	if t.Text != "" {
		ta.Text = proto.String(t.Text)
	}
	if t.TextBackground != nil {
		ta.TextBackgroundColor = proto.Uint32(*t.TextBackground)
	}
	if len(t.Gradient) >= 2 {
		positions := t.Positions
		if len(positions) != len(t.Gradient) {
			positions = evenPositions(len(t.Gradient))
		}
		angle := t.Angle
		if angle == 0 {
			angle = DefaultStoryGradientAngle
		}
		ta.Background = &signalpb.TextAttachment_Gradient_{Gradient: &signalpb.TextAttachment_Gradient{
			StartColor: proto.Uint32(t.Gradient[0]),
			EndColor:   proto.Uint32(t.Gradient[len(t.Gradient)-1]),
			Angle:      proto.Uint32(angle),
			Colors:     slices.Clone(t.Gradient),
			Positions:  slices.Clone(positions),
		}}
	} else {
		bg := t.Background
		if len(t.Gradient) == 1 {
			bg = t.Gradient[0]
		}
		if bg == 0 {
			bg = DefaultStoryBackground
		}
		ta.Background = &signalpb.TextAttachment_Color{Color: bg}
	}
	if t.PreviewURL != "" {
		pv := &signalpb.Preview{Url: proto.String(t.PreviewURL), Image: t.PreviewImage}
		if t.PreviewTitle != "" {
			pv.Title = proto.String(t.PreviewTitle)
		}
		if t.PreviewDescription != "" {
			pv.Description = proto.String(t.PreviewDescription)
		}
		ta.Preview = pv
	}
	return ta, nil
}

// evenPositions spreads n gradient stops evenly over 0..1.
func evenPositions(n int) []float32 {
	out := make([]float32, n)
	for i := range out {
		out[i] = float32(i) / float32(n-1)
	}
	return out
}

// BuildStoryMessage builds a StoryMessage with exactly one of text or file.
// The caller sets AllowsReplies and Group per destination.
func BuildStoryMessage(profileKey []byte, text *signalpb.TextAttachment, file *signalpb.AttachmentPointer) (*signalpb.StoryMessage, error) {
	if (text == nil) == (file == nil) {
		return nil, fmt.Errorf("story needs exactly one of a text or a file attachment: %w", ErrEmptyStory)
	}
	if len(profileKey) != libsignalgo.ProfileKeyLength {
		// Signal-Desktop refuses to send stories without our profile key.
		return nil, errors.New("story: no profile key")
	}
	sm := &signalpb.StoryMessage{ProfileKey: slices.Clone(profileKey), AllowsReplies: proto.Bool(true)}
	if text != nil {
		sm.Attachment = &signalpb.StoryMessage_TextAttachment{TextAttachment: text}
	} else {
		sm.Attachment = &signalpb.StoryMessage_FileAttachment{FileAttachment: file}
	}
	return sm, nil
}

// StorySyncRecipient is one entry of Sent.storyMessageRecipients.
type StorySyncRecipient struct {
	ServiceID      libsignalgo.ServiceID
	ListIDs        []uuid.UUID // empty for group stories (and removed recipients)
	AllowedToReply bool
}

func (r StorySyncRecipient) toProto() *signalpb.SyncMessage_Sent_StoryMessageRecipient {
	ids := make([]string, 0, len(r.ListIDs))
	for _, id := range r.ListIDs {
		ids = append(ids, id.String())
	}
	return &signalpb.SyncMessage_Sent_StoryMessageRecipient{
		DestinationServiceId:       proto.String(r.ServiceID.String()),
		DestinationServiceIdBinary: r.ServiceID.Bytes(),
		DistributionListIds:        ids,
		IsAllowedToReply:           proto.Bool(r.AllowedToReply),
	}
}

// storySyncContent builds the SyncMessage.Sent transcript of a story send
// (story != nil) or a recipient update (isUpdate, e.g. after a delete).
// destination is our own ACI for distribution list stories and nil for
// group stories, like Signal-Desktop.
func storySyncContent(ts uint64, story *signalpb.StoryMessage, destination *libsignalgo.ServiceID, recipients []StorySyncRecipient, isUpdate bool) *signalpb.Content {
	sent := &signalpb.SyncMessage_Sent{
		Timestamp:         proto.Uint64(ts),
		StoryMessage:      story,
		IsRecipientUpdate: proto.Bool(isUpdate),
	}
	if destination != nil {
		sent.DestinationServiceId = proto.String(destination.String())
		sent.DestinationServiceIdBinary = destination.Bytes()
	}
	for _, r := range recipients {
		sent.StoryMessageRecipients = append(sent.StoryMessageRecipients, r.toProto())
	}
	return WrapSyncMessage(&signalpb.SyncMessage{Content: &signalpb.SyncMessage_Sent_{Sent: sent}})
}

// storyMultiRecipientPath is the multi-recipient endpoint for stories:
// never online-only, never urgent, story=true (Signal-Desktop sendStory).
func storyMultiRecipientPath(ts uint64) string {
	return fmt.Sprintf("/v1/messages/multi_recipient?ts=%d&online=false&urgent=false&story=true", ts)
}

// storySenderKeyID is the key under which the outbound sender key of a
// distribution list is stored (groups use their group identifier).
func storySenderKeyID(listID uuid.UUID) types.GroupIdentifier {
	return types.GroupIdentifier("story:" + listID.String())
}

// --- sending ----------------------------------------------------------------------

// warte: StorySendResult is the outcome of a story send.
type StorySendResult struct {
	Timestamp uint64
	Sent      []libsignalgo.ServiceID
	Failed    []FailedSendResult
	// SyncRecipients is what was sent to our other devices.
	SyncRecipients []StorySyncRecipient
	SyncSent       bool
}

// SendListStory sends a story posted at ts to a distribution list
// ("My Story": MyStoryID). sm is cloned; its AllowsReplies is set from
// allowsReplies. recipients come from StoryAudience. Like Signal-Desktop
// it uses sender key (story=true, no access key) when at least two
// recipients can take it, sends 1:1 story=true messages otherwise, and
// then syncs the story with storyMessageRecipients to our other devices.
func (cli *Client) SendListStory(ctx context.Context, ts uint64, listID uuid.UUID, allowsReplies bool, sm *signalpb.StoryMessage, recipients []libsignalgo.ServiceID) (*StorySendResult, error) {
	log := zerolog.Ctx(ctx).With().Str("action", "send story").Stringer("list_id", listID).Uint64("timestamp", ts).Logger()
	ctx = log.WithContext(ctx)
	story := proto.Clone(sm).(*signalpb.StoryMessage)
	story.Group = nil
	story.AllowsReplies = proto.Bool(allowsReplies)
	recipients = cli.storyRecipientsWithoutSelf(recipients)
	content := &signalpb.Content{Content: &signalpb.Content_StoryMessage{StoryMessage: story}}
	res := &StorySendResult{Timestamp: ts}
	if len(recipients) > 0 {
		gres, err := cli.sendStoryWithSenderKey(ctx, storySenderKeyID(listID), nil, recipients, content, ts, 0)
		if err != nil {
			return nil, err
		}
		res.fill(gres)
	}
	for _, sid := range res.Sent {
		res.SyncRecipients = append(res.SyncRecipients, StorySyncRecipient{ServiceID: sid, ListIDs: []uuid.UUID{listID}, AllowedToReply: allowsReplies})
	}
	if len(res.Sent) == 0 && len(recipients) > 0 {
		return res, fmt.Errorf("failed to send story to any recipient: %w", lastStoryError(res.Failed))
	}
	// Desktop's sync carries the story with allowsReplies=true; who may
	// reply is in storyMessageRecipients.
	syncStory := proto.Clone(story).(*signalpb.StoryMessage)
	syncStory.AllowsReplies = proto.Bool(true)
	self := cli.Store.ACIServiceID()
	if len(res.SyncRecipients) > 0 { // Desktop: no sync without successful sends
		res.SyncSent = cli.sendStorySync(ctx, ts, storySyncContent(ts, syncStory, &self, res.SyncRecipients, false))
	}
	return res, nil
}

// SendGroupStory sends a story posted at ts to a group: a StoryMessage
// with the group context to all members (allowsReplies=true, sender key
// of the group, story=true), then a sync transcript.
func (cli *Client) SendGroupStory(ctx context.Context, ts uint64, gid types.GroupIdentifier, sm *signalpb.StoryMessage) (*StorySendResult, error) {
	log := zerolog.Ctx(ctx).With().Str("action", "send group story").Stringer("group_id", gid).Uint64("timestamp", ts).Logger()
	ctx = log.WithContext(ctx)
	group, _, err := cli.RetrieveGroupByID(ctx, gid, 0)
	if err != nil {
		return nil, err
	}
	gidBytes, err := gid.Bytes()
	if err != nil {
		return nil, err
	}
	story := proto.Clone(sm).(*signalpb.StoryMessage)
	story.Group = groupMetadataForDataMessage(*group)
	story.AllowsReplies = proto.Bool(true)
	var recipients []libsignalgo.ServiceID
	for _, m := range group.Members {
		recipients = append(recipients, m.UserServiceID())
	}
	recipients = cli.storyRecipientsWithoutSelf(recipients)
	content := &signalpb.Content{Content: &signalpb.Content_StoryMessage{StoryMessage: story}}
	res := &StorySendResult{Timestamp: ts}
	if len(recipients) > 0 {
		gres, err := cli.sendStoryWithSenderKey(ctx, gid, &gidBytes, recipients, content, ts, 0)
		if err != nil {
			return nil, err
		}
		res.fill(gres)
	}
	for _, sid := range res.Sent {
		res.SyncRecipients = append(res.SyncRecipients, StorySyncRecipient{ServiceID: sid, AllowedToReply: true})
	}
	if len(res.Sent) == 0 && len(recipients) > 0 {
		return res, fmt.Errorf("failed to send group story to any member: %w", lastStoryError(res.Failed))
	}
	if len(res.SyncRecipients) > 0 { // Desktop: no sync without successful sends
		res.SyncSent = cli.sendStorySync(ctx, ts, storySyncContent(ts, story, nil, res.SyncRecipients, false))
	}
	return res, nil
}

// DeleteListStory deletes a distribution-list story we posted at storyTS
// for everyone, like Signal-Desktop's deleteStoryForEveryone: a
// DataMessage.delete sent 1:1 with story=true to each recipient, then a
// recipient-update sync (isRecipientUpdate, no story) listing the former
// recipients with the distribution lists they still see it in (none here),
// which makes our other devices delete it too. Group stories are deleted
// with a normal group DataMessage.delete instead.
func (cli *Client) DeleteListStory(ctx context.Context, storyTS uint64, recipients []StorySyncRecipient) (sent []libsignalgo.ServiceID, failed []FailedSendResult, err error) {
	log := zerolog.Ctx(ctx).With().Str("action", "delete story").Uint64("target_ts", storyTS).Logger()
	ctx = log.WithContext(ctx)
	ts := currentMessageTimestamp()
	sctx := withStorySend(ctx)
	for _, r := range recipients {
		if r.ServiceID == cli.Store.ACIServiceID() || r.ServiceID.Type != libsignalgo.ServiceIDTypeACI {
			continue
		}
		content := WrapDataMessage(StoryDeleteMessage(ts, storyTS))
		cli.addSendCache(r.ServiceID, "", ts, content)
		if _, err := cli.sendContent(sctx, r.ServiceID, ts, content, 0, true, nil, nil); err != nil {
			log.Warn().Err(err).Stringer("recipient", r.ServiceID).Msg("Failed to send story delete")
			failed = append(failed, FailedSendResult{Recipient: r.ServiceID, Error: err})
			continue
		}
		sent = append(sent, r.ServiceID)
	}
	if len(sent) == 0 && len(failed) > 0 {
		return sent, failed, fmt.Errorf("failed to send story delete to anyone: %w", lastStoryError(failed))
	}
	update := make([]StorySyncRecipient, 0, len(recipients))
	for _, r := range recipients {
		update = append(update, StorySyncRecipient{ServiceID: r.ServiceID, ListIDs: nil, AllowedToReply: r.AllowedToReply})
	}
	self := cli.Store.ACIServiceID()
	cli.sendStorySync(ctx, storyTS, storySyncContent(storyTS, nil, &self, update, true))
	return sent, failed, nil
}

// StoryDeleteMessage is the DataMessage that deletes our story storyTS.
func StoryDeleteMessage(ts, storyTS uint64) *signalpb.DataMessage {
	return &signalpb.DataMessage{
		Timestamp: proto.Uint64(ts),
		Delete:    &signalpb.DataMessage_Delete{TargetSentTimestamp: proto.Uint64(storyTS)},
	}
}

func lastStoryError(failed []FailedSendResult) error {
	if len(failed) == 0 {
		return errors.New("no recipients")
	}
	return failed[len(failed)-1].Error
}

func (r *StorySendResult) fill(g *GroupMessageSendResult) {
	if g == nil {
		return
	}
	for _, s := range g.SuccessfullySentTo {
		r.Sent = append(r.Sent, s.Recipient)
	}
	r.Failed = append(r.Failed, g.FailedToSendTo...)
}

func (cli *Client) storyRecipientsWithoutSelf(in []libsignalgo.ServiceID) []libsignalgo.ServiceID {
	self := cli.Store.ACIServiceID()
	seen := map[libsignalgo.ServiceID]bool{}
	out := make([]libsignalgo.ServiceID, 0, len(in))
	for _, r := range in {
		if r == self || r.Type != libsignalgo.ServiceIDTypeACI || r.UUID == uuid.Nil || seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	return out
}

// sendStorySync sends a story sync transcript to our other devices
// (urgent=false, normal authenticated send).
func (cli *Client) sendStorySync(ctx context.Context, ts uint64, content *signalpb.Content) bool {
	ctx = withNonUrgentSend(withoutStorySend(ctx))
	_, err := cli.sendContent(ctx, cli.Store.ACIServiceID(), ts, content, 0, false, nil, nil)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to send story sync message to myself")
		return false
	}
	return true
}

// sendStoryIndividually sends a story 1:1 (story=true, sealed sender
// without access key) to each recipient.
func (cli *Client) sendStoryIndividually(
	ctx context.Context,
	recipients []libsignalgo.ServiceID,
	content *signalpb.Content,
	ts uint64,
	groupID *libsignalgo.GroupIdentifier,
	result *GroupMessageSendResult,
) *GroupMessageSendResult {
	if result == nil {
		result = &GroupMessageSendResult{}
	}
	ctx = withStorySend(ctx)
	for _, r := range recipients {
		log := zerolog.Ctx(ctx).With().Stringer("recipient", r).Logger()
		unidentified, err := cli.sendContent(log.WithContext(ctx), r, ts, content, 0, true, groupID, nil)
		if err != nil {
			log.Warn().Err(err).Msg("Failed to send story")
			result.FailedToSendTo = append(result.FailedToSendTo, FailedSendResult{Recipient: r, Error: err})
			continue
		}
		result.SuccessfullySentTo = append(result.SuccessfullySentTo, SuccessfulSendResult{Recipient: r, Unidentified: unidentified})
	}
	return result
}

// sendStoryWithSenderKey is sendToGroupWithSenderKey for stories. The
// differences (Signal-Desktop sendToGroupViaSenderKey with story=true):
// every ACI with sessions can take sender key (no access key or group send
// endorsement is needed), the multi-recipient request has story=true,
// urgent=false and no auth header, SKDMs to distribution-list recipients
// are themselves story sends, and fallback sends are 1:1 story sends.
// keyID names the stored sender key (the group, or the list); groupID is
// the group for group stories (it goes into the sealed sender content) and
// nil for distribution lists. No sync copy is sent.
func (cli *Client) sendStoryWithSenderKey(
	ctx context.Context,
	keyID types.GroupIdentifier,
	groupID *libsignalgo.GroupIdentifier,
	recipients []libsignalgo.ServiceID,
	content *signalpb.Content,
	ts uint64,
	retries int,
) (*GroupMessageSendResult, error) {
	cacheGroup := types.GroupIdentifier("")
	if groupID != nil {
		cacheGroup = keyID
	}
	if retries == 0 {
		for _, r := range recipients {
			cli.addSendCache(r, cacheGroup, ts, content)
		}
	}
	if retries >= 3 {
		return cli.sendStoryIndividually(ctx, recipients, content, ts, groupID, nil), nil
	}
	myAddress, err := cli.Store.ACIServiceID().Address(uint(cli.Store.DeviceID))
	if err != nil {
		return nil, fmt.Errorf("failed to get own address: %w", err)
	}
	log := zerolog.Ctx(ctx)

	cli.encryptionLock.Lock()
	unlocked := false
	doUnlock := func() {
		if !unlocked {
			unlocked = true
			cli.encryptionLock.Unlock()
		}
	}
	defer doUnlock()
	// unlockedCtx is for everything done after doUnlock: it must not claim
	// to hold the encryption lock.
	unlockedCtx := ctx
	ctx = context.WithValue(ctx, contextKeyEncryptionLock, true)
	result := &GroupMessageSendResult{}

	devices, skRecipients, fallback := cli.storySenderKeyDevices(ctx, recipients, result)
	if len(devices) < 2 {
		// Desktop: "Not enough recipients for Sender Key message. Failing over."
		doUnlock()
		log.Debug().Int("sender_key_recipients", len(devices)).Msg("Too few sender key recipients for story, sending individually")
		return cli.sendStoryIndividually(unlockedCtx, append(slices.Collect(maps.Keys(devices)), fallback...), content, ts, groupID, result), nil
	}
	ski, err := cli.Store.SenderKeyStore.GetSenderKeyInfo(ctx, keyID)
	if err != nil {
		return nil, fmt.Errorf("failed to get sender key info: %w", err)
	}
	if ski == nil || time.Since(ski.CreatedAt) > SenderKeyMaxAge {
		if ski != nil {
			if err = cli.Store.SenderKeyStore.DeleteSenderKey(ctx, myAddress, ski.DistributionID); err != nil {
				return nil, fmt.Errorf("failed to delete old sender key: %w", err)
			}
		}
		ski = &store.SenderKeyInfo{
			DistributionID: uuid.New(),
			CreatedAt:      time.Now(),
			SharedWith:     make(map[libsignalgo.ServiceID][]int),
		}
	}
	_, addedTo, removed := diffRecipients(ski.SharedWith, devices)
	if len(removed) > 0 {
		log.Debug().Any("removed_devices", removed).Msg("Resetting story sender key due to recipient changes")
		ski.SharedWith = make(map[libsignalgo.ServiceID][]int)
		addedTo = slices.Collect(maps.Keys(devices))
		if err = cli.Store.SenderKeyStore.DeleteSenderKey(ctx, myAddress, ski.DistributionID); err != nil {
			return nil, fmt.Errorf("failed to delete old sender key: %w", err)
		}
	}
	if len(addedTo) > 0 {
		skdm, err := libsignalgo.NewSenderKeyDistributionMessage(ctx, myAddress, ski.DistributionID, cli.Store.SenderKeyStore)
		if err != nil {
			return nil, fmt.Errorf("failed to create sender key distribution message: %w", err)
		}
		skdmBytes, err := skdm.Serialize()
		if err != nil {
			return nil, fmt.Errorf("failed to serialize sender key distribution message: %w", err)
		}
		// Desktop: SKDMs only have story=true when sending to a
		// distribution list; for groups they are normal sealed sender.
		skdmCtx := ctx
		if groupID == nil {
			skdmCtx = withStorySend(ctx)
		}
		needsRetry := false
		for _, r := range addedTo {
			_, err = cli.sendContent(skdmCtx, r, ts, &signalpb.Content{SenderKeyDistributionMessage: skdmBytes}, 0, true, groupID, nil)
			if errors.Is(err, ErrDevicesChanged) || errors.Is(err, ErrUnregisteredUser) {
				needsRetry = true
			} else if err != nil {
				log.Warn().Err(err).Stringer("recipient", r).Msg("Failed to send story sender key, falling back to 1:1")
				fallback = append(fallback, r)
				delete(devices, r)
				skRecipients = slices.DeleteFunc(skRecipients, func(t store.SessionAddressTuple) bool { return t.ServiceID == r })
			} else {
				ski.SharedWith[r] = devices[r].DeviceIDs
			}
		}
		if err = cli.Store.SenderKeyStore.PutSenderKeyInfo(ctx, keyID, ski); err != nil {
			return nil, fmt.Errorf("failed to store sender key info: %w", err)
		}
		if needsRetry {
			doUnlock()
			return cli.sendStoryWithSenderKey(unlockedCtx, keyID, groupID, recipients, content, ts, retries+1)
		}
		if len(devices) < 2 {
			doUnlock()
			return cli.sendStoryIndividually(unlockedCtx, append(slices.Collect(maps.Keys(devices)), fallback...), content, ts, groupID, result), nil
		}
	} else if len(removed) > 0 {
		if err = cli.Store.SenderKeyStore.PutSenderKeyInfo(ctx, keyID, ski); err != nil {
			return nil, fmt.Errorf("failed to store sender key info: %w", err)
		}
	}
	ciphertext, err := cli.encryptWithSenderKey(ctx, groupID, ski.DistributionID, myAddress, skRecipients, content)
	if err != nil {
		if errors.Is(err, libsignalgo.ErrorCodeSessionNotFound) {
			if err = cli.Store.SenderKeyStore.DeleteSenderKeyInfo(ctx, keyID); err != nil {
				return nil, fmt.Errorf("failed to delete sender key info: %w", err)
			}
			doUnlock()
			return cli.sendStoryWithSenderKey(unlockedCtx, keyID, groupID, recipients, content, ts, retries+1)
		}
		return nil, err
	}
	header := http.Header{}
	header.Set("Content-Type", string(web.ContentTypeMultiRecipientMessage))
	// No Unidentified-Access-Key / Group-Send-Token: stories need no
	// authorization, and the server rejects group send tokens for them.
	resp, err := cli.UnauthedWS.SendRequest(ctx, http.MethodPut, storyMultiRecipientPath(ts), ciphertext, header)
	if err != nil {
		doUnlock()
		log.Warn().Err(err).Msg("Multi-recipient story send failed, sending individually")
		return cli.sendStoryIndividually(unlockedCtx, recipients, content, ts, groupID, nil), nil
	}
	switch resp.GetStatus() {
	case 200:
		var body MultiRecipient200Response
		_ = json.Unmarshal(resp.GetBody(), &body) // the server answers stories with an empty list
		for sid := range devices {
			if slices.Contains(body.UUIDs404, sid) {
				result.FailedToSendTo = append(result.FailedToSendTo, FailedSendResult{Recipient: sid, Error: ErrUnregisteredUser})
				continue
			}
			result.SuccessfullySentTo = append(result.SuccessfullySentTo, SuccessfulSendResult{Recipient: sid, Unidentified: true})
		}
		doUnlock()
		return cli.sendStoryIndividually(unlockedCtx, fallback, content, ts, groupID, result), nil
	case 409, 410:
		if err = cli.handleMultiRecipient409410Response(ctx, resp); err != nil {
			return nil, err
		}
		doUnlock()
		return cli.sendStoryWithSenderKey(unlockedCtx, keyID, groupID, recipients, content, ts, retries+1)
	case 400, 401, 404:
		log.Warn().Uint32("status_code", resp.GetStatus()).Msg("Multi-recipient story send rejected, sending individually")
		doUnlock()
		return cli.sendStoryIndividually(unlockedCtx, recipients, content, ts, groupID, nil), nil
	default:
		return nil, fmt.Errorf("unexpected status code %d in multi-recipient story send", resp.GetStatus())
	}
}

// storySenderKeyDevices finds the devices of story recipients that can use
// sender key: every ACI we have (or can establish) sessions with. The
// others go to the 1:1 fallback; unregistered ones fail.
func (cli *Client) storySenderKeyDevices(
	ctx context.Context,
	recipients []libsignalgo.ServiceID,
	result *GroupMessageSendResult,
) (map[libsignalgo.ServiceID]senderKeySendMeta, []store.SessionAddressTuple, []libsignalgo.ServiceID) {
	log := zerolog.Ctx(ctx)
	devices := make(map[libsignalgo.ServiceID]senderKeySendMeta)
	var tuples []store.SessionAddressTuple
	var fallback []libsignalgo.ServiceID
	for _, r := range recipients {
		if r == cli.Store.ACIServiceID() {
			continue
		}
		if r.Type != libsignalgo.ServiceIDTypeACI {
			result.FailedToSendTo = append(result.FailedToSendTo, FailedSendResult{Recipient: r, Error: errors.New("stories can only be sent to ACIs")})
			continue
		}
		sessions, err := cli.Store.ACISessionStore.AllSessionsForServiceID(ctx, r)
		if err == nil && len(sessions) == 0 {
			err = cli.FetchAndProcessPreKey(ctx, r, -1)
			if errors.Is(err, ErrUnregisteredUser) {
				result.FailedToSendTo = append(result.FailedToSendTo, FailedSendResult{Recipient: r, Error: err})
				continue
			} else if err != nil {
				log.Warn().Err(err).Stringer("recipient", r).Msg("Failed to fetch keys for story recipient")
				fallback = append(fallback, r)
				continue
			}
			sessions, err = cli.Store.ACISessionStore.AllSessionsForServiceID(ctx, r)
		}
		if err != nil || len(sessions) == 0 {
			fallback = append(fallback, r)
			continue
		}
		devices[r] = senderKeySendMeta{
			DeviceIDs: exslices.CastFunc(sessions, func(t store.SessionAddressTuple) int { return t.DeviceID }),
		}
		tuples = append(tuples, sessions...)
	}
	return devices, tuples, fallback
}
