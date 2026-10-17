package backend

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-signal/pkg/libsignalgo"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/events"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"

	"warte/internal/core"
)

// testInviteLink builds a signal.group link with a fixed master key.
func testInviteLink(t *testing.T) string {
	t.Helper()
	mk := bytes.Repeat([]byte{7}, libsignalgo.GroupMasterKeyLength)
	raw, err := proto.Marshal(&signalpb.GroupInviteLink{Contents: &signalpb.GroupInviteLink_ContentsV1{
		ContentsV1: &signalpb.GroupInviteLink_GroupInviteLinkContentsV1{GroupMasterKey: mk, InviteLinkPassword: []byte{1, 2, 3, 250, 251}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return "https://signal.group/#" + base64.URLEncoding.EncodeToString(raw)
}

func TestParseGroupInviteLink(t *testing.T) {
	link := testInviteLink(t)
	inv, err := parseGroupInviteLink(link)
	if err != nil {
		t.Fatal(err)
	}
	if inv.MasterKey[0] != 7 || !bytes.Equal(inv.Password, []byte{1, 2, 3, 250, 251}) {
		t.Errorf("decoded %+v", inv)
	}
	frag := link[strings.Index(link, "#")+1:]
	for _, variant := range []string{
		"  " + link + "\n",
		"sgnl://joingroup#" + frag,
		"signal.group/#" + strings.TrimRight(frag, "="),
		"https://signal.group/#" + base64.StdEncoding.EncodeToString(mustB64URL(t, frag)),
	} {
		if got, err := parseGroupInviteLink(variant); err != nil || got.MasterKey != inv.MasterKey {
			t.Errorf("%q: %v", variant, err)
		}
	}
	short, _ := proto.Marshal(&signalpb.GroupInviteLink{Contents: &signalpb.GroupInviteLink_ContentsV1{
		ContentsV1: &signalpb.GroupInviteLink_GroupInviteLinkContentsV1{GroupMasterKey: []byte{1}, InviteLinkPassword: []byte{1}},
	}})
	for _, bad := range []string{
		"", "https://example.com/#abc", "https://signal.group/", "https://signal.group/#!!!",
		"https://signal.group/#" + base64.RawURLEncoding.EncodeToString(short),
		"https://signal.group/#" + base64.RawURLEncoding.EncodeToString([]byte{0xff, 0xff, 0xff}),
	} {
		if _, err := parseGroupInviteLink(bad); !errors.Is(err, errBadID) {
			t.Errorf("%q: %v, want errBadID", bad, err)
		}
	}
}

func mustB64URL(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBuildJoinActions(t *testing.T) {
	pres := []byte("presentation")
	direct := buildJoinActions(pres, 8, false)
	if direct.GetVersion() != 8 || len(direct.GetAddMembers()) != 1 || len(direct.GetAddMembersPendingAdminApproval()) != 0 {
		t.Fatalf("direct: %v", direct)
	}
	am := direct.GetAddMembers()[0]
	if !bytes.Equal(am.GetAdded().GetPresentation(), pres) || am.GetAdded().GetRole() != signalpb.Member_DEFAULT || len(am.GetAdded().GetUserId()) != 0 {
		t.Errorf("member: %v", am)
	}
	if len(direct.GetGroupId()) != 0 || len(direct.GetSourceUserId()) != 0 {
		t.Error("client must not set group id or source")
	}
	req := buildJoinActions(pres, 3, true)
	if len(req.GetAddMembers()) != 0 || len(req.GetAddMembersPendingAdminApproval()) != 1 ||
		!bytes.Equal(req.GetAddMembersPendingAdminApproval()[0].GetAdded().GetPresentation(), pres) {
		t.Errorf("request: %v", req)
	}
	for access, want := range map[signalpb.AccessControl_AccessRequired]string{
		signalpb.AccessControl_ANY: "direct", signalpb.AccessControl_ADMINISTRATOR: "approval",
		signalpb.AccessControl_UNSATISFIABLE: "error", signalpb.AccessControl_MEMBER: "error",
	} {
		approval, err := joinAccess(&signalpb.GroupJoinInfo{AddFromInviteLink: access})
		got := "direct"
		switch {
		case err != nil:
			got = "error"
		case approval:
			got = "approval"
		}
		if got != want {
			t.Errorf("%v: %s, want %s", access, got, want)
		}
	}
	if err := groupsHTTPError("x", 403); !errors.Is(err, core.ErrReadOnly) {
		t.Error("403")
	}
	if err := groupsHTTPError("x", 404); !errors.Is(err, core.ErrNotFound) {
		t.Error("404")
	}
}

func TestServerPublicParamsParse(t *testing.T) {
	if _, err := serverPublicParams(); err != nil {
		t.Fatal(err)
	}
}

func TestDecryptJoinTitle(t *testing.T) {
	mk := libsignalgo.GroupMasterKey(bytes.Repeat([]byte{9}, libsignalgo.GroupMasterKeyLength))
	gsp, err := libsignalgo.DeriveGroupSecretParamsFromMasterKey(mk)
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := proto.Marshal(&signalpb.GroupAttributeBlob{Content: &signalpb.GroupAttributeBlob_Title{Title: "Climbing"}})
	var randomness libsignalgo.Randomness
	enc, err := gsp.EncryptBlobWithPaddingDeterministic(randomness, plain, 0)
	if err != nil {
		t.Skipf("no deterministic blob encryption: %v", err)
	}
	if got := decryptJoinTitle(gsp, enc); got != "Climbing" {
		t.Errorf("title %q", got)
	}
	if got := decryptJoinTitle(gsp, []byte("garbage")); got != "" {
		t.Errorf("garbage title %q", got)
	}
}

func TestRecordJoinRequest(t *testing.T) {
	b, _ := newTestBackend(t)
	ctx := context.Background()
	if err := b.recordJoinRequest(ctx, groupChat, "Climbing", 4); err != nil {
		t.Fatal(err)
	}
	c, err := b.Chat(ctx, groupChat)
	if err != nil {
		t.Fatal(err)
	}
	if c.Title != "Climbing" || c.Kind != core.ChatGroup || !memberOf(c.RequestingJoin, selfACI) || c.GroupRevision != 4 {
		t.Errorf("chat %+v", c)
	}
	if err := canSend(c, selfACI); !errors.Is(err, core.ErrReadOnly) {
		t.Error("pending join must be read-only")
	}
	// Idempotent.
	if err := b.recordJoinRequest(ctx, groupChat, "", 2); err != nil {
		t.Fatal(err)
	}
	c, _ = b.Chat(ctx, groupChat)
	if len(c.RequestingJoin) != 1 || c.Title != "Climbing" || c.GroupRevision != 4 {
		t.Errorf("second record: %+v", c)
	}
}

func TestIsPublicIP(t *testing.T) {
	for addr, want := range map[string]bool{
		"93.184.216.34": true, "2606:2800:220:1:248:1893:25c8:1946": true, "1.1.1.1": true,
		"127.0.0.1": false, "10.1.2.3": false, "192.168.0.1": false, "172.16.5.4": false,
		"169.254.169.254": false, "100.64.0.1": false, "100.127.255.254": false, "0.1.2.3": false,
		"198.18.0.1": false, "240.0.0.1": false, "255.255.255.255": false, "192.0.0.8": false,
		"::1": false, "fc00::1": false, "fe80::1": false, "::ffff:10.0.0.1": false, "::ffff:8.8.8.8": true,
		"64:ff9b::a00:1": false, "64:ff9b::808:808": true, "ff02::1": false, "2001:db8::1": false,
	} {
		if got := isPublicIP(net.ParseIP(addr)); got != want {
			t.Errorf("%s: %v, want %v", addr, got, want)
		}
	}
	if isPublicIP(nil) {
		t.Error("nil")
	}
}

func TestGuardRecoversPanics(t *testing.T) {
	out, err := guard("op", func() ([]byte, error) {
		var s []byte
		return s[:1], nil // panics like signalmeow on a short ciphertext
	})
	if out != nil || !errors.Is(err, errPanic) {
		t.Fatalf("guard: %v %v", out, err)
	}
	out, err = guard("op", func() ([]byte, error) { return []byte("ok"), nil })
	if string(out) != "ok" || err != nil {
		t.Fatalf("guard passthrough: %q %v", out, err)
	}
}

func TestHandlerAndBackgroundRecoverPanics(t *testing.T) {
	b, _ := newTestBackend(t)
	drain(t, b)
	// A nil ChatEvent dereferences e.Info: the handler must survive and drop it.
	if !b.handleSignalEvent((*events.ChatEvent)(nil)) {
		t.Error("panicking event should be dropped (handled), not redelivered")
	}
	done := make(chan struct{})
	b.goBackground(func() {
		defer close(done)
		var m map[string]int
		m["x"] = 1
	})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("background job did not finish")
	}
	errs := eventsOf[core.ErrorEvent](drain(t, b))
	if len(errs) != 2 {
		t.Fatalf("got %d error events, want 2", len(errs))
	}
	for _, e := range errs {
		if !errors.Is(e.Err, errPanic) {
			t.Errorf("error event %v", e.Err)
		}
	}
	// The handler still works afterwards (lock released).
	if !b.handleSignalEvent(&events.QueueEmpty{}) {
		t.Error("handler stuck after panic")
	}
}

func TestProfileFieldCrypto(t *testing.T) {
	var key libsignalgo.ProfileKey
	copy(key[:], testKey(t))
	for _, tc := range []struct {
		in      string
		buckets []int
		size    int
	}{
		{"", profileNamePads, 53}, {"Ada\x00Lovelace", profileNamePads, 53},
		{strings.Repeat("x", 54), profileNamePads, 257}, {"hi", profileAboutPads, 128},
		{strings.Repeat("é", 100), profileAboutPads, 254}, {"🧗", profileEmojiPads, 32},
	} {
		enc, err := encryptProfileField(key, []byte(tc.in), tc.buckets)
		if err != nil {
			t.Fatalf("%q: %v", tc.in, err)
		}
		if len(enc) != 12+tc.size+16 {
			t.Errorf("%q: ciphertext %d bytes, want %d", tc.in, len(enc), 12+tc.size+16)
		}
		got, err := decryptProfileField(key, enc)
		if err != nil || got != tc.in {
			t.Errorf("%q: round trip %q %v", tc.in, got, err)
		}
	}
	if _, err := encryptProfileField(key, bytes.Repeat([]byte("x"), 258), profileNamePads); err == nil {
		t.Error("over-long name accepted")
	}
	enc, _ := encryptProfileField(key, []byte("x"), profileEmojiPads)
	enc[20] ^= 1
	if _, err := decryptProfileField(key, enc); err == nil {
		t.Error("tampered field decrypted")
	}
	if _, err := decryptProfileField(key, []byte{1, 2}); err == nil {
		t.Error("short field decrypted")
	}
	if g, f := splitProfileName("Ada\x00Lovelace"); g != "Ada" || f != "Lovelace" {
		t.Errorf("split %q %q", g, f)
	}
	if g, f := splitProfileName("Ada"); g != "Ada" || f != "" {
		t.Errorf("split %q %q", g, f)
	}
}

func TestPlanAndBuildProfile(t *testing.T) {
	str := func(s string) *string { return &s }
	cur := plannedProfile{Given: "Ada", Family: "Lovelace", About: "math", AboutEmoji: "🧮"}
	next, err := planProfile(cur, core.ProfileUpdate{About: str("  engines "), FamilyName: str("")})
	if err != nil {
		t.Fatal(err)
	}
	if next != (plannedProfile{Given: "Ada", About: "engines", AboutEmoji: "🧮"}) {
		t.Errorf("plan %+v", next)
	}
	for name, u := range map[string]core.ProfileUpdate{
		"empty given": {GivenName: str(" ")},
		"nul":         {GivenName: str("a\x00b")},
		"long about":  {About: str(strings.Repeat("a", 141))},
		"long name":   {GivenName: str(strings.Repeat("a", 53))},
	} {
		if _, err := planProfile(cur, u); err == nil {
			t.Errorf("%s accepted", name)
		}
	}

	var key libsignalgo.ProfileKey
	copy(key[:], testKey(t))
	raw := rawProfile{Avatar: "profiles/abc", PaymentAddress: []byte("pay"), PhoneNumberSharing: []byte("share")}
	for name, tc := range map[string]struct {
		avatar           []byte
		has, same        bool
		currentHasAvatar bool
	}{
		"keep":   {nil, true, true, true},
		"keep0":  {nil, false, false, false},
		"remove": {[]byte{}, false, false, true},
		"new":    {[]byte("png"), true, false, true},
	} {
		r := raw
		if !tc.currentHasAvatar {
			r.Avatar = ""
		}
		req, err := buildProfileRequest(key, "v1", []byte("commit"), next, r, tc.avatar)
		if err != nil {
			t.Fatal(err)
		}
		if req.Avatar != tc.has || req.SameAvatar != tc.same {
			t.Errorf("%s: avatar=%v same=%v", name, req.Avatar, req.SameAvatar)
		}
		if n, _ := decryptProfileField(key, req.Name); n != "Ada" {
			t.Errorf("%s: name %q", name, n)
		}
		if a, _ := decryptProfileField(key, req.About); a != "engines" {
			t.Errorf("%s: about %q", name, a)
		}
		if string(req.PaymentAddress) != "pay" || string(req.PhoneNumberSharing) != "share" {
			t.Errorf("%s: untouched fields not round-tripped", name)
		}
		js, _ := json.Marshal(req)
		var m map[string]any
		_ = json.Unmarshal(js, &m)
		for _, k := range []string{"version", "name", "about", "aboutEmoji", "avatar", "sameAvatar", "commitment"} {
			if _, ok := m[k]; !ok {
				t.Errorf("%s: JSON lacks %q", name, k)
			}
		}
		if m["commitment"] != base64.StdEncoding.EncodeToString([]byte("commit")) {
			t.Errorf("commitment encoding %v", m["commitment"])
		}
	}
	full, err := buildProfileRequest(key, "v", nil, plannedProfile{Given: "A", Family: "B"}, rawProfile{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := decryptProfileField(key, full.Name); n != "A\x00B" {
		t.Errorf("name with family %q", n)
	}
	if full.PaymentAddress != nil {
		t.Error("payment address invented")
	}
}

func TestPrivateLoggerCapsTrace(t *testing.T) {
	var buf bytes.Buffer
	l := privateLogger(zerolog.New(&buf).Level(zerolog.TraceLevel))
	l.Trace().Msg("secret")
	l.Debug().Msg("ok")
	if strings.Contains(buf.String(), "secret") || !strings.Contains(buf.String(), "ok") {
		t.Errorf("log: %s", buf.String())
	}
	if got := privateLogger(zerolog.Nop()).GetLevel(); got != zerolog.Disabled {
		t.Errorf("nop logger level changed to %v", got)
	}
	if got := privateLogger(zerolog.New(&buf).Level(zerolog.WarnLevel)).GetLevel(); got != zerolog.WarnLevel {
		t.Errorf("warn logger level changed to %v", got)
	}
}

func TestUnansweredCallTimesOut(t *testing.T) {
	b, _ := newTestBackend(t)
	b.ringTimeout = 50 * time.Millisecond
	drain(t, b)
	mustHandle(t, b, &events.Call{Info: events.MessageInfo{Sender: aliceUUID, ChatID: aliceUUID.String()}, Timestamp: 7000, IsRinging: true})
	deadline := time.Now().Add(3 * time.Second)
	var ended bool
	for !ended && time.Now().Before(deadline) {
		for _, ic := range eventsOf[core.IncomingCall](drain(t, b)) {
			if !ic.Ringing {
				ended = true
			}
		}
	}
	if !ended {
		t.Fatal("ringing call never ended")
	}
	if m := mustMessage(t, b, core.MessageID{Chat: aliceChat, Author: aliceACI, Timestamp: 7000}); m.Call == nil || !m.Call.Missed {
		t.Errorf("timed-out call not missed: %+v", m.Call)
	}
	// A hangup arriving later is a no-op.
	mustHandle(t, b, &events.Call{Info: events.MessageInfo{Sender: aliceUUID, ChatID: aliceUUID.String()}, Timestamp: 7100})
	if ic := eventsOf[core.IncomingCall](drain(t, b)); len(ic) != 0 {
		t.Errorf("late hangup emitted %+v", ic)
	}
}
