package backend

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"go.mau.fi/mautrix-signal/pkg/libsignalgo"
	"go.mau.fi/mautrix-signal/pkg/signalmeow"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/web"

	"warte/internal/core"
)

// Profile writes.
//
// signalmeow only reads profiles, so UpdateProfile speaks Signal-Server's
// versioned profile API itself, as Signal Desktop does from a linked device:
//
//  1. GET /v1/profile/<aci>/<version> for the current encrypted fields, so
//     fields we do not change (and ones warte does not model, like the
//     payment address and phone number sharing) are sent back unchanged.
//  2. PUT /v1/profile with the profile key version and commitment, each
//     field AES-256-GCM encrypted under the profile key and zero-padded to
//     Signal's bucket sizes, and the avatar mode (keep, remove or new).
//  3. For a new avatar, POST the encrypted image to the CDN with the upload
//     form returned by step 2.
//  4. Tell our other devices to fetch the new profile (FetchLatest sync).
//
// The profile key itself is not rotated (that needs the primary device).

// Padded sizes (bytes) of encrypted profile fields, as in Signal's
// ProfileCipher.
var (
	profileNamePads  = []int{53, 257}
	profileAboutPads = []int{128, 254, 512}
	profileEmojiPads = []int{32}
)

// maxProfileAvatar bounds an avatar upload.
const maxProfileAvatar = 5 << 20

// profileFieldPad returns the bucket a plaintext of n bytes is padded to.
func profileFieldPad(n int, buckets []int) (int, bool) {
	for _, b := range buckets {
		if n <= b {
			return b, true
		}
	}
	return 0, false
}

// encryptProfileField zero-pads plain to its bucket and encrypts it with
// the profile key (nonce || ciphertext || tag).
func encryptProfileField(key libsignalgo.ProfileKey, plain []byte, buckets []int) ([]byte, error) {
	size := len(plain)
	if buckets != nil {
		var ok bool
		if size, ok = profileFieldPad(len(plain), buckets); !ok {
			return nil, fmt.Errorf("backend: profile field too long (%d bytes, max %d)", len(plain), buckets[len(buckets)-1])
		}
	}
	padded := make([]byte, size)
	copy(padded, plain)
	return profileSeal(key, padded)
}

func profileSeal(key libsignalgo.ProfileKey, plain []byte) ([]byte, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize(), gcm.NonceSize()+len(plain)+gcm.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plain, nil), nil
}

// decryptProfileField reverses encryptProfileField (trailing zeros cut).
func decryptProfileField(key libsignalgo.ProfileKey, enc []byte) (string, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(enc) < gcm.NonceSize()+gcm.Overhead() {
		return "", errors.New("backend: profile field too short")
	}
	plain, err := gcm.Open(nil, enc[:gcm.NonceSize()], enc[gcm.NonceSize():], nil)
	if err != nil {
		return "", errors.New("backend: profile field does not decrypt")
	}
	return string(bytes.TrimRight(plain, "\x00")), nil
}

// splitProfileName splits Signal's "given\x00family" name.
func splitProfileName(name string) (given, family string) {
	given, family, _ = strings.Cut(name, "\x00")
	return given, family
}

// rawProfile is the part of GET /v1/profile we round-trip. Byte fields are
// base64 in JSON, as Go encodes []byte.
type rawProfile struct {
	Name               []byte `json:"name"`
	About              []byte `json:"about"`
	AboutEmoji         []byte `json:"aboutEmoji"`
	Avatar             string `json:"avatar"`
	PaymentAddress     []byte `json:"paymentAddress"`
	PhoneNumberSharing []byte `json:"phoneNumberSharing"`
}

// setProfileRequest is PUT /v1/profile's body (Signal-Server's
// CreateProfileRequest).
type setProfileRequest struct {
	Version            string `json:"version"`
	Name               []byte `json:"name"`
	About              []byte `json:"about,omitempty"`
	AboutEmoji         []byte `json:"aboutEmoji,omitempty"`
	Avatar             bool   `json:"avatar"`
	SameAvatar         bool   `json:"sameAvatar"`
	Commitment         []byte `json:"commitment"`
	PaymentAddress     []byte `json:"paymentAddress,omitempty"`
	PhoneNumberSharing []byte `json:"phoneNumberSharing,omitempty"`
}

// avatarUploadForm is PUT /v1/profile's response for a new avatar.
type avatarUploadForm struct {
	Key        string `json:"key"`
	Credential string `json:"credential"`
	ACL        string `json:"acl"`
	Algorithm  string `json:"algorithm"`
	Date       string `json:"date"`
	Policy     string `json:"policy"`
	Signature  string `json:"signature"`
}

// plannedProfile is the decrypted result of applying an update to the
// current profile.
type plannedProfile struct {
	Given, Family, About, AboutEmoji string
}

// planProfile applies u to the current decrypted fields and validates the
// result.
func planProfile(cur plannedProfile, u core.ProfileUpdate) (plannedProfile, error) {
	next := cur
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = strings.TrimSpace(*v)
		}
	}
	set(&next.Given, u.GivenName)
	set(&next.Family, u.FamilyName)
	set(&next.About, u.About)
	set(&next.AboutEmoji, u.AboutEmoji)
	for _, f := range []string{next.Given, next.Family, next.About, next.AboutEmoji} {
		if !utf8.ValidString(f) || strings.ContainsRune(f, 0) {
			return cur, errors.New("backend: profile text must be valid UTF-8 without NUL")
		}
	}
	if next.Given == "" {
		return cur, errors.New("backend: a profile needs a first name")
	}
	if utf8.RuneCountInString(next.About) > 140 {
		return cur, errors.New("backend: about text is limited to 140 characters")
	}
	if utf8.RuneCountInString(next.Given)+utf8.RuneCountInString(next.Family) > 52 {
		return cur, errors.New("backend: name is too long")
	}
	return next, nil
}

// buildProfileRequest encrypts a planned profile into the PUT body.
func buildProfileRequest(key libsignalgo.ProfileKey, version string, commitment []byte, p plannedProfile, cur rawProfile, avatar []byte) (*setProfileRequest, error) {
	name := p.Given
	if p.Family != "" {
		name += "\x00" + p.Family
	}
	req := &setProfileRequest{
		Version: version, Commitment: commitment,
		PaymentAddress: cur.PaymentAddress, PhoneNumberSharing: cur.PhoneNumberSharing,
	}
	var err error
	if req.Name, err = encryptProfileField(key, []byte(name), profileNamePads); err != nil {
		return nil, err
	}
	if req.About, err = encryptProfileField(key, []byte(p.About), profileAboutPads); err != nil {
		return nil, err
	}
	if req.AboutEmoji, err = encryptProfileField(key, []byte(p.AboutEmoji), profileEmojiPads); err != nil {
		return nil, err
	}
	switch {
	case avatar == nil: // unchanged
		req.Avatar, req.SameAvatar = cur.Avatar != "", cur.Avatar != ""
	case len(avatar) == 0: // remove
	default:
		req.Avatar = true
	}
	return req, nil
}

// UpdateProfile changes our Signal profile (name, about, emoji, avatar).
func (b *Backend) UpdateProfile(ctx context.Context, u core.ProfileUpdate) error {
	cli, err := b.client()
	if err != nil {
		return err
	}
	if len(u.Avatar) > maxProfileAvatar {
		return fmt.Errorf("backend: avatar is larger than %d MB", maxProfileAvatar>>20)
	}
	if cli.AuthedWS == nil {
		return errors.New("backend: not connected")
	}
	pkBytes := profileKey(ctx, cli)
	if pkBytes == nil {
		return errors.New("backend: own profile key unknown yet, try again after the first sync")
	}
	key := libsignalgo.ProfileKey(pkBytes)
	self := cli.Store.ACI
	ver, err := key.GetProfileKeyVersion(self)
	if err != nil {
		return fmt.Errorf("backend: profile key version: %w", err)
	}
	commit, err := key.GetCommitment(self)
	if err != nil {
		return fmt.Errorf("backend: profile key commitment: %w", err)
	}

	cur, err := fetchRawProfile(ctx, cli, self, ver.String())
	if err != nil {
		return err
	}
	var decoded plannedProfile
	if len(cur.Name) > 0 {
		name, err := decryptProfileField(key, cur.Name)
		if err != nil {
			return err
		}
		decoded.Given, decoded.Family = splitProfileName(name)
	}
	if len(cur.About) > 0 {
		decoded.About, _ = decryptProfileField(key, cur.About)
	}
	if len(cur.AboutEmoji) > 0 {
		decoded.AboutEmoji, _ = decryptProfileField(key, cur.AboutEmoji)
	}
	next, err := planProfile(decoded, u)
	if err != nil {
		return err
	}
	req, err := buildProfileRequest(key, ver.String(), commit[:], next, cur, u.Avatar)
	if err != nil {
		return err
	}
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	resp, err := cli.AuthedWS.SendRequest(ctx, http.MethodPut, "/v1/profile", body, nil)
	if err != nil {
		return fmt.Errorf("backend: update profile: %w", err)
	}
	if st := resp.GetStatus(); st < 200 || st >= 300 {
		return fmt.Errorf("backend: update profile: HTTP %d", st)
	}
	if len(u.Avatar) > 0 {
		var form avatarUploadForm
		if err := json.Unmarshal(resp.GetBody(), &form); err != nil || form.Key == "" {
			return fmt.Errorf("backend: profile avatar upload form missing")
		}
		enc, err := profileSeal(key, u.Avatar)
		if err != nil {
			return err
		}
		if err := uploadProfileAvatar(ctx, form, enc); err != nil {
			return err
		}
	}
	zerolog.Ctx(ctx).Info().Msg("Profile updated")

	// Mirror the change locally and on our other devices.
	if r, err := cli.ContactByACIWithRefreshAfter(ctx, self, 0); err == nil && r != nil {
		_ = b.mergeRecipient(ctx, r)
	}
	b.mu.Lock()
	b.self.ProfileName = strings.TrimSpace(next.Given + " " + next.Family)
	b.self.About, b.self.AboutEmoji = next.About, next.AboutEmoji
	me := b.self
	b.mu.Unlock()
	if err := b.db.UpsertContact(ctx, me); err == nil {
		b.emit(core.ContactUpdated{Contact: me})
	}
	if len(u.Avatar) > 0 && me.AvatarID != "" {
		b.cacheAvatar(ctx, me.AvatarID, "avatar:"+string(me.ACI), u.Avatar, "", avatarSource{Kind: "profile", ACI: me.ACI})
	}
	sm := &signalpb.SyncMessage{Content: &signalpb.SyncMessage_FetchLatest_{FetchLatest: &signalpb.SyncMessage_FetchLatest{
		Type: signalpb.SyncMessage_FetchLatest_LOCAL_PROFILE.Enum(),
	}}}
	if err := b.sendSync(ctx, cli, sm); err != nil {
		zerolog.Ctx(ctx).Debug().Err(err).Msg("Failed to ask own devices to refetch profile")
	}
	return nil
}

// fetchRawProfile reads our own versioned profile with the encrypted
// fields as stored on the server.
func fetchRawProfile(ctx context.Context, cli *signalmeow.Client, self uuid.UUID, version string) (rawProfile, error) {
	resp, err := cli.AuthedWS.SendRequest(ctx, http.MethodGet, "/v1/profile/"+self.String()+"/"+version, nil, nil)
	if err != nil {
		return rawProfile{}, fmt.Errorf("backend: read own profile: %w", err)
	}
	switch st := resp.GetStatus(); {
	case st == http.StatusNotFound:
		return rawProfile{}, nil // no profile for this key version yet
	case st < 200 || st >= 300:
		return rawProfile{}, fmt.Errorf("backend: read own profile: HTTP %d", st)
	}
	var p rawProfile
	if err := json.Unmarshal(resp.GetBody(), &p); err != nil {
		return rawProfile{}, fmt.Errorf("backend: read own profile: %w", err)
	}
	return p, nil
}

// uploadProfileAvatar posts an encrypted avatar with the server's upload
// form (an S3-style POST policy) to the CDN.
func uploadProfileAvatar(ctx context.Context, form avatarUploadForm, enc []byte) error {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, f := range [][2]string{
		{"key", form.Key}, {"x-amz-credential", form.Credential}, {"acl", form.ACL},
		{"x-amz-algorithm", form.Algorithm}, {"x-amz-date", form.Date}, {"policy", form.Policy},
		{"x-amz-signature", form.Signature}, {"Content-Type", "application/octet-stream"},
	} {
		if err := w.WriteField(f[0], f[1]); err != nil {
			return err
		}
	}
	fw, err := w.CreateFormFile("file", "file")
	if err != nil {
		return err
	}
	if _, err := fw.Write(enc); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	resp, err := web.SendHTTPRequest(ctx, web.CDN1Hostname, http.MethodPost, "", &web.HTTPReqOpt{
		Body: buf.Bytes(), ContentType: web.ContentType(w.FormDataContentType()),
	})
	defer web.CloseBody(resp)
	if err != nil {
		return fmt.Errorf("backend: upload avatar: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("backend: upload avatar: HTTP %d", resp.StatusCode)
	}
	return nil
}
