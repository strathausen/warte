// mautrix-signal - A Matrix-signal puppeting bridge.
// Copyright (C) 2026 warte contributors
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package signalmeow

// warte: tests for sending stories (sending_story_warte.go).

import (
	"bytes"
	"context"
	"errors"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-signal/pkg/libsignalgo"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/types"
)

func aciSID(n byte) libsignalgo.ServiceID {
	var u uuid.UUID
	u[0], u[15] = n, n
	u[6] = 0x40 // version 4
	u[8] = 0x80
	return libsignalgo.NewACIServiceID(u)
}

func TestStoryPrivacyFromRecord(t *testing.T) {
	a, b, c := aciSID(1), aciSID(2), aciSID(3)
	pni := libsignalgo.NewPNIServiceID(c.UUID)
	rec := &signalpb.StoryDistributionListRecord{
		Identifier:                MyStoryID[:],
		Name:                      "",
		IsBlockList:               true,
		AllowsReplies:             true,
		RecipientServiceIds:       []string{a.String(), "garbage", pni.String()},
		RecipientServiceIdsBinary: [][]byte{a.Bytes(), b.Bytes(), {1, 2}},
	}
	p, ok := StoryPrivacyFromRecord(rec)
	if !ok || p.ListID != MyStoryID || !p.IsBlockList || !p.AllowsReplies {
		t.Fatalf("privacy %+v ok=%v", p, ok)
	}
	if want := []libsignalgo.ServiceID{a, pni, b}; !slices.Equal(p.Members, want) {
		t.Errorf("members %v, want %v (deduplicated, string and binary merged)", p.Members, want)
	}
	if p.Mode() != StoryAllExcept {
		t.Errorf("mode %v, want all-except", p.Mode())
	}
	if _, ok := StoryPrivacyFromRecord(&signalpb.StoryDistributionListRecord{Identifier: []byte{1}}); ok {
		t.Error("bad identifier accepted")
	}
}

func TestStoryPrivacyModes(t *testing.T) {
	a := aciSID(1)
	other := uuid.MustParse("11111111-2222-4333-8444-555555555555")
	cases := []struct {
		p    StoryPrivacy
		want StoryPrivacyMode
	}{
		{DefaultMyStory(), StoryAllConnections},
		{StoryPrivacy{ListID: MyStoryID, IsBlockList: true, Members: []libsignalgo.ServiceID{a}}, StoryAllExcept},
		{StoryPrivacy{ListID: MyStoryID, IsBlockList: false, Members: []libsignalgo.ServiceID{a}}, StoryOnlyWith},
		{StoryPrivacy{ListID: MyStoryID}, StoryOnlyWith}, // only-with nobody
		// Desktop ignores isBlockList for custom lists.
		{StoryPrivacy{ListID: other, IsBlockList: true, Members: []libsignalgo.ServiceID{a}}, StoryOnlyWith},
	}
	for i, c := range cases {
		if got := c.p.Mode(); got != c.want {
			t.Errorf("%d: mode %v, want %v", i, got, c.want)
		}
	}
	if d := DefaultMyStory(); !d.AllowsReplies || d.ListID != MyStoryID {
		t.Errorf("default %+v", d)
	}
}

func TestMyStoryPrivacy(t *testing.T) {
	other := uuid.New()
	a := aciSID(1)
	lists := []*signalpb.StoryDistributionListRecord{
		{Identifier: other[:], Name: "Close friends", RecipientServiceIds: []string{a.String()}, AllowsReplies: true},
		{Identifier: MyStoryID[:], IsBlockList: false, RecipientServiceIds: []string{a.String()}, AllowsReplies: false},
	}
	p := MyStoryPrivacy(lists)
	if p.ListID != MyStoryID || p.Mode() != StoryOnlyWith || p.AllowsReplies || len(p.Members) != 1 {
		t.Errorf("my story %+v", p)
	}
	if p := MyStoryPrivacy(lists[:1]); p.Mode() != StoryAllConnections || !p.AllowsReplies {
		t.Errorf("missing my story should be the default, got %+v", p)
	}
}

func TestStoryAudience(t *testing.T) {
	a, b, c, d := aciSID(1), aciSID(2), aciSID(3), aciSID(4)
	pni := libsignalgo.NewPNIServiceID(aciSID(5).UUID)
	connections := []libsignalgo.ServiceID{a, b, c, a, pni}
	blocked := func(s libsignalgo.ServiceID) bool { return s == c }

	all := StoryAudience(DefaultMyStory(), connections, blocked)
	if want := []libsignalgo.ServiceID{a, b}; !slices.Equal(all, want) {
		t.Errorf("all connections: %v, want %v", all, want)
	}
	except := StoryAudience(StoryPrivacy{ListID: MyStoryID, IsBlockList: true, Members: []libsignalgo.ServiceID{b}}, connections, nil)
	if want := []libsignalgo.ServiceID{a, c}; !slices.Equal(except, want) {
		t.Errorf("all except b: %v, want %v", except, want)
	}
	// Only-with ignores connections entirely: d is not a connection but is
	// a member; PNIs are dropped (sealed sender needs an ACI).
	only := StoryAudience(StoryPrivacy{ListID: MyStoryID, Members: []libsignalgo.ServiceID{d, pni, c, d}}, connections, blocked)
	if want := []libsignalgo.ServiceID{d}; !slices.Equal(only, want) {
		t.Errorf("only with: %v, want %v", only, want)
	}
	if got := StoryAudience(StoryPrivacy{ListID: MyStoryID}, connections, nil); len(got) != 0 {
		t.Errorf("only with nobody: %v", got)
	}
}

func TestIsStoryConnection(t *testing.T) {
	self := uuid.New()
	yes, no := true, false
	cases := []struct {
		r    *types.Recipient
		want bool
	}{
		{&types.Recipient{ACI: uuid.New(), Whitelisted: &yes}, true},
		{&types.Recipient{ACI: uuid.New(), ContactName: "Al"}, true},
		{&types.Recipient{ACI: uuid.New(), Whitelisted: &no}, false},
		{&types.Recipient{ACI: uuid.New()}, false},
		{&types.Recipient{ACI: uuid.New(), Whitelisted: &yes, Blocked: true}, false},
		{&types.Recipient{ACI: self, Whitelisted: &yes}, false},
		{&types.Recipient{PNI: uuid.New(), Whitelisted: &yes}, false},
		{nil, false},
	}
	for i, c := range cases {
		if got := isStoryConnection(c.r, self); got != c.want {
			t.Errorf("%d: %v, want %v", i, got, c.want)
		}
	}
}

// fields decodes the top-level fields of a protobuf message.
func fields(t *testing.T, b []byte) map[protowire.Number][]any {
	t.Helper()
	out := map[protowire.Number][]any{}
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			t.Fatal("bad tag")
		}
		b = b[n:]
		switch typ {
		case protowire.VarintType:
			v, n := protowire.ConsumeVarint(b)
			out[num] = append(out[num], v)
			b = b[n:]
		case protowire.BytesType:
			v, n := protowire.ConsumeBytes(b)
			out[num] = append(out[num], v)
			b = b[n:]
		case protowire.Fixed32Type:
			v, n := protowire.ConsumeFixed32(b)
			out[num] = append(out[num], v)
			b = b[n:]
		default:
			t.Fatalf("unexpected wire type %v", typ)
		}
		if n < 0 {
			t.Fatal("bad value")
		}
	}
	return out
}

func TestBuildTextAttachmentGradientWire(t *testing.T) {
	// Desktop's BG6 gradient.
	colors := []uint32{0xff19a9fa, 0xff7097d7, 0xffd1998d, 0xffffc369}
	ta, err := BuildTextAttachment(TextStory{
		Text: "hello", Style: signalpb.TextAttachment_BOLD, Foreground: 0xff000000,
		Gradient: colors, Positions: []float32{0, 0.33, 0.66, 1}, Angle: 180,
	})
	if err != nil {
		t.Fatal(err)
	}
	f := fields(t, mustMarshal(t, ta))
	// SignalService.proto TextAttachment: text=1, textStyle=2,
	// textForegroundColor=3, textBackgroundColor=4, preview=5,
	// gradient=6, color=7.
	if string(f[1][0].([]byte)) != "hello" || f[2][0].(uint64) != 2 || f[3][0].(uint64) != 0xff000000 {
		t.Errorf("fields %v", f)
	}
	if _, ok := f[4]; ok {
		t.Error("text background set without being asked")
	}
	if _, ok := f[7]; ok {
		t.Error("color set next to a gradient (oneof)")
	}
	g := fields(t, f[6][0].([]byte))
	// Gradient: startColor=1, endColor=2, angle=3, colors=4 (packed),
	// positions=5 (packed float).
	if g[1][0].(uint64) != uint64(colors[0]) || g[2][0].(uint64) != uint64(colors[3]) || g[3][0].(uint64) != 180 {
		t.Errorf("gradient %v", g)
	}
	// SignalService.proto is proto2: repeated scalars are not packed, so
	// each color and position is its own field (as Desktop encodes them).
	var got []uint32
	for _, v := range g[4] {
		got = append(got, uint32(v.(uint64)))
	}
	if !slices.Equal(got, colors) {
		t.Errorf("colors %x", got)
	}
	if len(g[5]) != 4 {
		t.Fatalf("positions %v, want 4 fixed32 values", g[5])
	}
	if v := math.Float32frombits(g[5][1].(uint32)); v != 0.33 {
		t.Errorf("position[1] = %v", v)
	}
}

func TestBuildTextAttachmentDefaults(t *testing.T) {
	ta, err := BuildTextAttachment(TextStory{Text: "hi", Style: 99})
	if err != nil {
		t.Fatal(err)
	}
	if ta.GetTextStyle() != signalpb.TextAttachment_DEFAULT {
		t.Errorf("unknown style kept: %v", ta.GetTextStyle())
	}
	if ta.GetTextForegroundColor() != DefaultStoryForeground || ta.GetColor() != DefaultStoryBackground || ta.GetGradient() != nil {
		t.Errorf("defaults %v", ta)
	}
	// Single gradient color = solid background; even positions and the
	// default angle when not given.
	ta, _ = BuildTextAttachment(TextStory{Text: "hi", Gradient: []uint32{0xff112233}})
	if ta.GetColor() != 0xff112233 {
		t.Errorf("single-color gradient: %v", ta)
	}
	ta, _ = BuildTextAttachment(TextStory{Text: "hi", Gradient: []uint32{1, 2, 3}})
	if g := ta.GetGradient(); g.GetAngle() != 180 || !slices.Equal(g.GetPositions(), []float32{0, 0.5, 1}) {
		t.Errorf("gradient %v", g)
	}
	bg := uint32(0xffffffff)
	ta, _ = BuildTextAttachment(TextStory{Text: "hi", TextBackground: &bg, Background: 0xff000000})
	if ta.TextBackgroundColor == nil || ta.GetTextBackgroundColor() != bg || ta.GetColor() != 0xff000000 {
		t.Errorf("text background %v", ta)
	}
}

func TestBuildTextAttachmentPreviewAndValidation(t *testing.T) {
	if _, err := BuildTextAttachment(TextStory{Text: "  "}); !errors.Is(err, ErrEmptyStory) {
		t.Errorf("blank story: %v", err)
	}
	if _, err := BuildTextAttachment(TextStory{Text: strings.Repeat("x", MaxTextStoryLength+1)}); err == nil {
		t.Error("over-long story accepted")
	}
	img := &signalpb.AttachmentPointer{ContentType: proto.String("image/jpeg")}
	ta, err := BuildTextAttachment(TextStory{PreviewURL: "https://signal.org", PreviewTitle: "Signal", PreviewImage: img})
	if err != nil {
		t.Fatal(err)
	}
	if ta.Text != nil || ta.GetPreview().GetUrl() != "https://signal.org" || ta.GetPreview().GetTitle() != "Signal" ||
		ta.GetPreview().GetImage() != img || ta.GetPreview().Description != nil {
		t.Errorf("preview %v", ta)
	}
}

func TestBuildStoryMessage(t *testing.T) {
	pk := bytes.Repeat([]byte{7}, libsignalgo.ProfileKeyLength)
	ta, _ := BuildTextAttachment(TextStory{Text: "hi"})
	sm, err := BuildStoryMessage(pk, ta, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sm.GetProfileKey(), pk) || sm.GetTextAttachment() != ta || sm.GetFileAttachment() != nil || sm.Group != nil {
		t.Errorf("story %v", sm)
	}
	f := fields(t, mustMarshal(t, sm))
	// StoryMessage: profileKey=1, group=2, fileAttachment=3,
	// textAttachment=4, allowsReplies=5.
	if _, ok := f[4]; !ok || f[5][0].(uint64) != 1 {
		t.Errorf("wire fields %v", f)
	}
	file := &signalpb.AttachmentPointer{ContentType: proto.String("image/png"), Caption: proto.String("cap")}
	sm, err = BuildStoryMessage(pk, nil, file)
	if err != nil || sm.GetFileAttachment().GetCaption() != "cap" {
		t.Errorf("file story %v %v", sm, err)
	}
	if _, err := BuildStoryMessage(pk, ta, file); err == nil {
		t.Error("story with both attachments accepted")
	}
	if _, err := BuildStoryMessage(pk, nil, nil); !errors.Is(err, ErrEmptyStory) {
		t.Errorf("empty story: %v", err)
	}
	if _, err := BuildStoryMessage(nil, ta, nil); err == nil {
		t.Error("story without profile key accepted")
	}
}

func TestStorySyncContentRoundTrip(t *testing.T) {
	self, a, b := aciSID(9), aciSID(1), aciSID(2)
	pk := bytes.Repeat([]byte{7}, libsignalgo.ProfileKeyLength)
	ta, _ := BuildTextAttachment(TextStory{Text: "hi"})
	sm, _ := BuildStoryMessage(pk, ta, nil)
	recips := []StorySyncRecipient{
		{ServiceID: a, ListIDs: []uuid.UUID{MyStoryID}, AllowedToReply: true},
		{ServiceID: b, ListIDs: []uuid.UUID{MyStoryID}, AllowedToReply: false},
	}
	content := storySyncContent(1234, sm, &self, recips, false)
	// Decode from the wire, like the receiving device does.
	var decoded signalpb.Content
	if err := proto.Unmarshal(mustMarshal(t, content), &decoded); err != nil {
		t.Fatal(err)
	}
	sent := decoded.GetSyncMessage().GetSent()
	if sent.GetTimestamp() != 1234 || sent.GetIsRecipientUpdate() || sent.GetStoryMessage().GetTextAttachment().GetText() != "hi" || sent.GetMessage() != nil {
		t.Fatalf("sent %v", sent)
	}
	if sent.GetDestinationServiceId() != self.String() || !bytes.Equal(sent.GetDestinationServiceIdBinary(), self.Bytes()) {
		t.Errorf("destination %q %x", sent.GetDestinationServiceId(), sent.GetDestinationServiceIdBinary())
	}
	if len(decoded.GetSyncMessage().GetPadding()) == 0 {
		t.Error("sync message without padding")
	}
	got := storyRecipients(sent) // the receive side's parser
	if len(got) != 2 || got[0].ServiceID != a.String() || !got[0].AllowedToReply || got[1].AllowedToReply ||
		!slices.Equal(got[0].DistributionListIDs, []string{"00000000-0000-0000-0000-000000000000"}) {
		t.Errorf("recipients %+v", got)
	}

	// Group stories: no destination, recipients without list IDs.
	content = storySyncContent(1235, sm, nil, []StorySyncRecipient{{ServiceID: a, AllowedToReply: true}}, false)
	sent = content.GetSyncMessage().GetSent()
	if sent.DestinationServiceId != nil || sent.DestinationServiceIdBinary != nil || len(sent.GetStoryMessageRecipients()[0].GetDistributionListIds()) != 0 {
		t.Errorf("group sync %v", sent)
	}

	// Delete: a recipient update without a story, everyone left with no
	// distribution list.
	content = storySyncContent(1234, nil, &self, []StorySyncRecipient{{ServiceID: a, AllowedToReply: true}}, true)
	sent = content.GetSyncMessage().GetSent()
	if !sent.GetIsRecipientUpdate() || sent.StoryMessage != nil || len(sent.GetStoryMessageRecipients()) != 1 ||
		len(sent.GetStoryMessageRecipients()[0].DistributionListIds) != 0 {
		t.Errorf("delete sync %v", sent)
	}
}

func TestStorySendParameters(t *testing.T) {
	if got := storyMultiRecipientPath(1700000000123); got != "/v1/messages/multi_recipient?ts=1700000000123&online=false&urgent=false&story=true" {
		t.Errorf("path %q", got)
	}
	story := &signalpb.Content{Content: &signalpb.Content_StoryMessage{StoryMessage: &signalpb.StoryMessage{}}}
	if isUrgent(story) {
		t.Error("stories must not be urgent")
	}
	if getContentHint(story) != libsignalgo.UnidentifiedSenderMessageContentHintImplicit {
		t.Error("stories use ContentHint.Implicit")
	}
	if !isUrgent(WrapDataMessage(StoryDeleteMessage(2, 1))) {
		t.Error("story deletes are urgent")
	}
	if d := StoryDeleteMessage(2, 1); d.GetTimestamp() != 2 || d.GetDelete().GetTargetSentTimestamp() != 1 {
		t.Errorf("delete %v", d)
	}
	if got := storySenderKeyID(MyStoryID); got != "story:00000000-0000-0000-0000-000000000000" {
		t.Errorf("sender key id %q", got)
	}

	ctx := context.Background()
	if o := warteSendOptsFrom(ctx); o.story || o.notUrgent {
		t.Error("default options set")
	}
	sctx := withStorySend(ctx)
	if !warteSendOptsFrom(sctx).story {
		t.Error("story flag not set")
	}
	nctx := withNonUrgentSend(withoutStorySend(sctx))
	if o := warteSendOptsFrom(nctx); o.story || !o.notUrgent {
		t.Errorf("options %+v", o)
	}
}

func TestStoryRecipientsWithoutSelf(t *testing.T) {
	self, a := aciSID(9), aciSID(1)
	cli := testStorageClient(nil)
	cli.Store.ACI = self.UUID
	pni := libsignalgo.NewPNIServiceID(a.UUID)
	got := cli.storyRecipientsWithoutSelf([]libsignalgo.ServiceID{self, a, pni, a, libsignalgo.NewACIServiceID(uuid.Nil)})
	if !slices.Equal(got, []libsignalgo.ServiceID{a}) {
		t.Errorf("recipients %v", got)
	}
}

func TestStoryDistributionListsFromStorage(t *testing.T) {
	a := aciSID(1)
	other := uuid.New()
	my := &signalpb.StorageRecord{Record: &signalpb.StorageRecord_StoryDistributionList{StoryDistributionList: &signalpb.StoryDistributionListRecord{
		Identifier: MyStoryID[:], IsBlockList: true, AllowsReplies: true, RecipientServiceIdsBinary: [][]byte{a.Bytes()},
	}}}
	custom := &signalpb.StorageRecord{Record: &signalpb.StorageRecord_StoryDistributionList{StoryDistributionList: &signalpb.StoryDistributionListRecord{
		Identifier: other[:], Name: "Friends",
	}}}
	f, _ := newFakeStorage(t, nil, 5, accountRec(), my, custom)
	cli := testStorageClient(f)
	lists, err := cli.StoryDistributionLists(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(lists) != 2 {
		t.Fatalf("%d lists", len(lists))
	}
	p := MyStoryPrivacy(lists)
	if p.Mode() != StoryAllExcept || !slices.Equal(p.Members, []libsignalgo.ServiceID{a}) {
		t.Errorf("my story %+v", p)
	}
	if len(f.writes) != 0 {
		t.Error("reading lists wrote to storage")
	}
	cli.Store.MasterKey = nil
	if _, err := cli.StoryDistributionLists(context.Background()); !errors.Is(err, ErrNoStorageKey) {
		t.Errorf("no key: %v", err)
	}
}
