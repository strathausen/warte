package backend

import (
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-signal/pkg/signalmeow"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"

	"warte/internal/core"
)

// decodeBlurHashAC decodes the AC components of a hash (test helper).
func decodeBlurHashAC(t *testing.T, h string) [][3]float64 {
	t.Helper()
	dec := func(s string) int {
		v := 0
		for _, c := range s {
			i := strings.IndexRune(blurHashChars, c)
			if i < 0 {
				t.Fatalf("invalid character %q in %q", c, h)
			}
			v = v*83 + i
		}
		return v
	}
	maxValue := float64(dec(h[1:2])+1) / 166
	var out [][3]float64
	for i := 6; i+2 <= len(h); i += 2 {
		v := dec(h[i : i+2])
		q := [3]int{v / (19 * 19), (v / 19) % 19, v % 19}
		var f [3]float64
		for k := range q {
			f[k] = signPow((float64(q[k])-9)/9, 2) * maxValue
		}
		out = append(out, f)
	}
	return out
}

func TestBlurHash(t *testing.T) {
	solid := image.NewRGBA(image.Rect(0, 0, 20, 10))
	for y := range 10 {
		for x := range 20 {
			solid.Set(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	h := blurHash(solid, 4, 3)
	if len(h) != 1+1+4+2*11 {
		t.Fatalf("length %d (%q)", len(h), h)
	}
	if h[0] != 'L' {
		t.Errorf("size flag %q", h[0])
	}
	if h[2:6] != base83(0xFF0000, 4) {
		t.Errorf("DC %q, want pure red", h[2:6])
	}
	if h != blurHash(solid, 4, 3) {
		t.Error("not deterministic")
	}
	if len(decodeBlurHashAC(t, h)) != 11 {
		t.Error("AC count")
	}
	// A horizontal gradient puts its energy into the first horizontal
	// component, not the vertical one.
	grad := image.NewGray(image.Rect(0, 0, 100, 100))
	for y := range 100 {
		for x := range 100 {
			grad.SetGray(x, y, color.Gray{Y: uint8(x * 255 / 99)})
		}
	}
	g := blurHash(grad, 4, 3)
	ac := decodeBlurHashAC(t, g)
	horiz, vert := math.Abs(ac[0][0]), math.Abs(ac[3][0])
	if horiz < 3*vert || horiz == 0 {
		t.Errorf("gradient energy: horizontal %.3f, vertical %.3f (%q)", horiz, vert, g)
	}
	if blurHash(image.NewRGBA(image.Rect(0, 0, 0, 0)), 4, 3) != "" || blurHash(solid, 0, 3) != "" || blurHash(solid, 10, 3) != "" {
		t.Error("invalid input must give empty hash")
	}
	if one := blurHash(solid, 1, 1); len(one) != 6 {
		t.Errorf("1x1 hash %q", one)
	}
}

func TestEventQueue(t *testing.T) {
	q := newEventQueue()
	for i := range 1000 {
		q.push(core.SyncProgress{Done: i})
	}
	if q.len() == 0 {
		t.Fatal("pushes must not block without a consumer")
	}
	for i := range 1000 {
		ev := <-q.out
		if ev.(core.SyncProgress).Done != i {
			t.Fatalf("event %d out of order: %+v", i, ev)
		}
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				q.push(core.StoriesChanged{})
			}
		}()
	}
	wg.Wait()
	for range 400 {
		<-q.out
	}
	q.push()
	q.close()
	if _, ok := <-q.out; ok {
		t.Error("channel must be closed after close")
	}
	q.push(core.StoriesChanged{}) // no panic after close
	q.close()                     // idempotent
}

func TestEventChatID(t *testing.T) {
	pni := uuid.New()
	tests := []struct {
		in      string
		chat    core.ChatID
		pni     uuid.UUID
		wantErr bool
	}{
		{string(aliceACI), aliceChat, uuid.Nil, false},
		{"PNI:" + pni.String(), "", pni, false},
		{groupChat.GroupID(), groupChat, uuid.Nil, false},
		{"", "", uuid.Nil, true},
		{"not-a-uuid-but-36-characters-long!!!", "", uuid.Nil, true},
		{"bm90IDMyIGJ5dGVz", "", uuid.Nil, true},
	}
	for _, tt := range tests {
		chat, p, err := eventChatID(tt.in)
		if (err != nil) != tt.wantErr || chat != tt.chat || p != tt.pni {
			t.Errorf("eventChatID(%q) = %q, %v, %v", tt.in, chat, p, err)
		}
	}
}

func TestIDHelpers(t *testing.T) {
	if aciFromBinaryOrString(aliceUUID[:], "") != aliceACI {
		t.Error("binary")
	}
	if aciFromBinaryOrString(append([]byte{0}, aliceUUID[:]...), "") != aliceACI {
		t.Error("17-byte ACI service id")
	}
	if aciFromBinaryOrString(append([]byte{1}, aliceUUID[:]...), "") != "" {
		t.Error("PNI service id is not an ACI")
	}
	if aciFromBinaryOrString(nil, "PNI:"+aliceUUID.String()) != "" || aciFromBinaryOrString(nil, "junk") != "" {
		t.Error("non-ACI strings")
	}
	if aciFromBinaryOrString(nil, string(bobACI)) != bobACI {
		t.Error("string")
	}
	if aciOf(uuid.Nil) != "" {
		t.Error("nil uuid")
	}
	if _, err := parseACI("x"); !errors.Is(err, errBadID) {
		t.Error("parseACI error")
	}
	raw, err := groupIDBytes(groupChat)
	if err != nil || len(raw) != 32 {
		t.Fatal(err)
	}
	if back, ok := groupChatIDFromBytes(raw); !ok || back != groupChat {
		t.Error("group id round trip")
	}
	if _, ok := groupChatIDFromBytes([]byte{1}); ok {
		t.Error("short group id")
	}
	if _, err := groupIdentifier(aliceChat); err == nil {
		t.Error("direct chat is not a group")
	}
	if _, err := groupIdentifier(core.GroupChatID(base64.StdEncoding.EncodeToString([]byte{1, 2}))); err == nil {
		t.Error("short group identifier")
	}
	if hashID("a", "x") == hashID("a", "y") || !strings.HasPrefix(hashID("v", "x"), "v") {
		t.Error("hashID")
	}
	if hashID("a", "ab", "c") == hashID("a", "a", "bc") {
		t.Error("hashID parts must be delimited")
	}
}

func TestParseE164(t *testing.T) {
	tests := []struct {
		in   string
		norm string
		num  uint64
		ok   bool
	}{
		{"+4915112345678", "+4915112345678", 4915112345678, true},
		{" +1 (555) 123-4567 ", "+15551234567", 15551234567, true},
		{"+49.151.1234567", "+491511234567", 491511234567, true},
		{"4915112345678", "", 0, false},
		{"+12", "", 0, false},
		{"+1555abc4567", "", 0, false},
		{"+12345678901234567", "", 0, false},
	}
	for _, tt := range tests {
		norm, num, ok := parseE164(tt.in)
		if norm != tt.norm || num != tt.num || ok != tt.ok {
			t.Errorf("parseE164(%q) = %q %d %v", tt.in, norm, num, ok)
		}
	}
}

func TestFirstURLAndOpenGraph(t *testing.T) {
	tests := []struct{ in, want string }{
		{"look https://example.com/a?b=1.", "https://example.com/a?b=1"},
		{"(see https://example.com/x)", "https://example.com/x"},
		{"http://insecure.example.com only", ""},
		{"no link", ""},
	}
	for _, tt := range tests {
		if got := firstURL(tt.in); got != tt.want {
			t.Errorf("firstURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	page := `<html><head><title>Fallback</title>
<meta property="og:title" content="Real &amp; Title">
<meta name='description' content='  A   description  '>
<meta property="og:image" content="/img.png">
<meta property="og:image" content="/second.png">
<meta property="article:published_time" content="2024-01-02T03:04:05Z">
</head></html>`
	og := parseOpenGraph(page)
	if og.Title != "Real & Title" || og.Description != "A description" || og.Image != "/img.png" || !og.Date.Equal(time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Errorf("og %+v", og)
	}
	og = parseOpenGraph(`<title>Only title</title>`)
	if og.Title != "Only title" {
		t.Errorf("title fallback %+v", og)
	}
	long := parseOpenGraph(`<meta property="og:title" content="` + strings.Repeat("é", 300) + `">`)
	if len(long.Title) > previewMaxTitle+len("…") || !strings.HasSuffix(long.Title, "…") {
		t.Errorf("title not clipped on a rune boundary: %d bytes", len(long.Title))
	}
}

func TestBuildGroupChange(t *testing.T) {
	title, desc := " New ", " d "
	only := true
	link := core.AccessAdmin
	timer := 2 * time.Hour
	ch, err := buildGroupChange(core.GroupUpdate{
		Title: &title, Description: &desc, Avatar: []byte{1},
		AddMembers: []core.ACI{aliceACI}, RemoveMembers: []core.ACI{bobACI},
		Promote: []core.ACI{aliceACI}, Demote: []core.ACI{bobACI},
		Approve: []core.ACI{aliceACI}, Deny: []core.ACI{bobACI},
		OnlyAdminsSend: &only, LinkMode: &link, ExpireTimer: &timer,
	}, "avatars/1", false)
	if err != nil {
		t.Fatal(err)
	}
	if *ch.ModifyTitle != "New" || *ch.ModifyDescription != "d" || *ch.ModifyAvatar != "avatars/1" {
		t.Error("attributes")
	}
	if len(ch.AddMembers) != 1 || ch.AddMembers[0].ACI != aliceUUID || len(ch.DeleteMembers) != 1 || *ch.DeleteMembers[0] != bobUUID {
		t.Error("members")
	}
	if len(ch.ModifyMemberRoles) != 2 || ch.ModifyMemberRoles[0].Role != signalmeow.GroupMember_ADMINISTRATOR || ch.ModifyMemberRoles[1].Role != signalmeow.GroupMember_DEFAULT {
		t.Error("roles")
	}
	if len(ch.PromoteRequestingMembers) != 1 || len(ch.DeleteRequestingMembers) != 1 {
		t.Error("requests")
	}
	if !*ch.ModifyAnnouncementsOnly || *ch.ModifyAddFromInviteLinkAccess != signalmeow.AccessControl_ADMINISTRATOR || ch.ModifyInviteLinkPassword == nil {
		t.Error("link/announcements")
	}
	if *ch.ModifyDisappearingMessagesDuration != 7200 {
		t.Error("timer")
	}
	if groupChangeEmpty(ch) {
		t.Error("not empty")
	}
	// Enabling a link that already exists keeps the password; removing the
	// avatar sends an empty path.
	ch, _ = buildGroupChange(core.GroupUpdate{LinkMode: &link, Avatar: []byte{}}, "", true)
	if ch.ModifyInviteLinkPassword != nil || *ch.ModifyAvatar != "" {
		t.Error("existing link / avatar removal")
	}
	ch, _ = buildGroupChange(core.GroupUpdate{ResetLink: true}, "", true)
	if ch.ModifyInviteLinkPassword == nil {
		t.Error("reset link")
	}
	empty, _ := buildGroupChange(core.GroupUpdate{}, "", false)
	if !groupChangeEmpty(empty) {
		t.Error("empty update")
	}
	blank := "  "
	if _, err := buildGroupChange(core.GroupUpdate{Title: &blank}, "", false); err == nil {
		t.Error("blank title")
	}
	member := core.AccessMember
	if _, err := buildGroupChange(core.GroupUpdate{LinkMode: &member}, "", false); err == nil {
		t.Error("invalid link mode")
	}
	if _, err := buildGroupChange(core.GroupUpdate{AddMembers: []core.ACI{"x"}}, "", false); err == nil {
		t.Error("bad member ACI")
	}
}

func TestPackFromManifest(t *testing.T) {
	m := &signalpb.Pack{
		Title: proto.String("Cats"), Author: proto.String("Me"),
		Stickers: []*signalpb.Pack_Sticker{{Id: proto.Uint32(1), Emoji: proto.String("🐱"), ContentType: proto.String("image/webp")}, nil, {Id: proto.Uint32(2)}},
	}
	p := packFromManifest("aa", "bb", m, true)
	if p.Title != "Cats" || p.Author != "Me" || !p.Installed || len(p.Stickers) != 2 || p.Cover == nil || p.Cover.StickerID != 1 {
		t.Errorf("%+v", p)
	}
	if p.Stickers[0].Attachment.ID != stickerRecordID("aa", 1) || p.Stickers[0].PackKey != "bb" || !p.Stickers[0].Attachment.Borderless {
		t.Errorf("sticker %+v", p.Stickers[0])
	}
	m.Cover = &signalpb.Pack_Sticker{Id: proto.Uint32(2)}
	if p := packFromManifest("aa", "bb", m, false); p.Cover.StickerID != 2 {
		t.Error("explicit cover")
	}
	if _, _, err := decodePackRef(strings.Repeat("ab", 16), strings.Repeat("cd", 32)); err != nil {
		t.Error(err)
	}
	for _, bad := range [][2]string{{"zz", strings.Repeat("cd", 32)}, {strings.Repeat("ab", 16), "cd"}, {"abcd", strings.Repeat("cd", 32)}} {
		if _, _, err := decodePackRef(bad[0], bad[1]); !errors.Is(err, errBadID) {
			t.Errorf("decodePackRef(%q, %q) should fail", bad[0], bad[1])
		}
	}
}

func TestMatchContact(t *testing.T) {
	cs := []core.Contact{
		{ACI: aliceACI, ProfileName: "Alice", ContactName: "Alice Chen"},
		{ACI: bobACI, ProfileName: "Bob", Username: "bobby.12"},
		{ACI: selfACI, ProfileName: "Alicia"},
	}
	tests := []struct {
		q       string
		want    core.ACI
		wantErr bool
	}{
		{"alice chen", aliceACI, false},
		{"ALICE", aliceACI, false},
		{"bob", bobACI, false},
		{"bobby", bobACI, false},
		{"ali", "", true},
		{"chen", aliceACI, false},
		{"zed", "", true},
	}
	for _, tt := range tests {
		c, err := matchContact(cs, tt.q)
		if (err != nil) != tt.wantErr || (!tt.wantErr && c.ACI != tt.want) {
			t.Errorf("matchContact(%q) = %v, %v", tt.q, c.ACI, err)
		}
	}
	if _, err := matchContact(cs, "zed"); !errors.Is(err, core.ErrNotFound) {
		t.Error("not found error")
	}
}

func TestPrepareUpload(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 30, 20))
	png := encodePNG(t, img)
	p := prepareUpload(core.OutgoingAttachment{FileName: "x.png", Data: png})
	if p.ContentType != "image/png" || p.Width != 30 || p.Height != 20 || p.BlurHash == "" {
		t.Errorf("png %+v", p.OutgoingAttachment)
	}
	p = prepareUpload(core.OutgoingAttachment{FileName: "noext", Data: png})
	if p.ContentType != "image/png" {
		t.Errorf("sniffed %q", p.ContentType)
	}
	p = prepareUpload(core.OutgoingAttachment{FileName: "notes.txt", Data: []byte("hello")})
	if !strings.HasPrefix(p.ContentType, "text/plain") || p.Width != 0 {
		t.Errorf("text %+v", p.OutgoingAttachment)
	}
	p = prepareUpload(core.OutgoingAttachment{FileName: "a.png", ContentType: "image/png", Data: png, Width: 3, Height: 2})
	if p.Width != 3 || p.Height != 2 {
		t.Error("given dimensions are kept")
	}
}

func TestMessageAttachmentHelpers(t *testing.T) {
	m := core.Message{
		Attachments: []core.Attachment{{ID: "a"}},
		Sticker:     &core.Sticker{Attachment: core.Attachment{ID: "s"}},
		Preview:     &core.LinkPreview{Image: &core.Attachment{ID: "p"}},
		Quote:       &core.Quote{Attachment: &core.Attachment{ID: "q"}},
	}
	for _, id := range []string{"a", "s", "p", "q"} {
		if !messageHasAttachment(m, id) {
			t.Errorf("missing %s", id)
		}
	}
	if messageHasAttachment(m, "x") {
		t.Error("unknown id")
	}
	n := 0
	forEachAttachment(&m, func(a *core.Attachment) { a.Downloaded = true; n++ })
	if n != 4 || !m.Sticker.Attachment.Downloaded || !m.Quote.Attachment.Downloaded {
		t.Error("forEachAttachment must visit and mutate every slot")
	}
	ref := attachmentRef{ID: "a", Role: rolePreview, Pointer: ptrAtt("k", "image/png", 3), PlaintextHash: []byte{1}}
	raw, err := encodeRemote(ref)
	if err != nil {
		t.Fatal(err)
	}
	back, err := decodeRemote(raw)
	if err != nil || back.Role != rolePreview || back.Pointer.GetCdnKey() != "k" || string(back.PlaintextHash) != "\x01" {
		t.Errorf("remote round trip %+v %v", back, err)
	}
	if _, err := decodeRemote([]byte("{")); err == nil {
		t.Error("bad remote")
	}
	if hasLocation(nil) || hasLocation(&signalpb.AttachmentPointer{AttachmentIdentifier: &signalpb.AttachmentPointer_CdnId{CdnId: 1}}) {
		t.Error("pointer without key is not downloadable")
	}
}
