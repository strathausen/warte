package backend

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"go.mau.fi/mautrix-signal/pkg/libsignalgo"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/types"

	"warte/internal/core"
)

// errBadID is returned when a core identifier cannot be mapped to Signal.
var errBadID = errors.New("backend: invalid identifier")

// aciOf converts a Signal ACI UUID to warte's lowercase string form.
func aciOf(u uuid.UUID) core.ACI {
	if u == uuid.Nil {
		return ""
	}
	return core.ACI(u.String())
}

// parseACI parses a core.ACI into a UUID.
func parseACI(a core.ACI) (uuid.UUID, error) {
	u, err := uuid.Parse(string(a))
	if err != nil || u == uuid.Nil {
		return uuid.Nil, fmt.Errorf("%w: ACI %q", errBadID, a)
	}
	return u, nil
}

// aciFromBinaryOrString parses an ACI given as a 16-byte binary UUID or as a
// string, preferring the binary form. It returns "" if neither is valid.
func aciFromBinaryOrString(bin []byte, str string) core.ACI {
	if len(bin) == 16 {
		return aciOf(uuid.UUID(bin))
	}
	if len(bin) == 17 && bin[0] == byte(libsignalgo.ServiceIDTypeACI) {
		return aciOf(uuid.UUID(bin[1:]))
	}
	if str != "" {
		sid, err := libsignalgo.ServiceIDFromString(str)
		if err == nil && sid.Type == libsignalgo.ServiceIDTypeACI {
			return aciOf(sid.UUID)
		}
	}
	return ""
}

// aciServiceID converts an ACI to a libsignal service ID.
func aciServiceID(a core.ACI) (libsignalgo.ServiceID, error) {
	u, err := parseACI(a)
	if err != nil {
		return libsignalgo.EmptyServiceID, err
	}
	return libsignalgo.NewACIServiceID(u), nil
}

// groupChatID converts a signalmeow group identifier to a chat ID.
func groupChatID(gid types.GroupIdentifier) core.ChatID { return core.GroupChatID(string(gid)) }

// groupIdentifier returns the signalmeow group identifier of a group chat.
func groupIdentifier(c core.ChatID) (types.GroupIdentifier, error) {
	if !c.IsGroup() {
		return "", fmt.Errorf("%w: %q is not a group chat", errBadID, c)
	}
	gid := types.GroupIdentifier(c.GroupID())
	if _, err := gid.Bytes(); err != nil {
		return "", fmt.Errorf("%w: group %q: %v", errBadID, c, err)
	}
	return gid, nil
}

// groupIDBytes returns the raw 32-byte group identifier of a group chat.
func groupIDBytes(c core.ChatID) ([]byte, error) {
	gid, err := groupIdentifier(c)
	if err != nil {
		return nil, err
	}
	raw, err := gid.Bytes()
	if err != nil {
		return nil, err
	}
	return raw[:], nil
}

// groupChatIDFromBytes converts a raw 32-byte group identifier to a chat ID.
func groupChatIDFromBytes(b []byte) (core.ChatID, bool) {
	if len(b) != libsignalgo.GroupIdentifierLength {
		return "", false
	}
	return core.GroupChatID(base64.StdEncoding.EncodeToString(b)), true
}

// eventChatID maps signalmeow's events.MessageInfo.ChatID (a service ID
// string for direct chats, a base64 group identifier for groups) to a chat
// ID. PNI destinations are returned with pni set so the caller can resolve
// them to an ACI.
func eventChatID(s string) (chat core.ChatID, pni uuid.UUID, err error) {
	if s == "" {
		return "", uuid.Nil, fmt.Errorf("%w: empty chat", errBadID)
	}
	if len(s) == 36 || strings.HasPrefix(strings.ToUpper(s), "PNI:") {
		sid, perr := libsignalgo.ServiceIDFromString(s)
		if perr != nil {
			return "", uuid.Nil, fmt.Errorf("%w: chat %q", errBadID, s)
		}
		if sid.Type == libsignalgo.ServiceIDTypePNI {
			return "", sid.UUID, nil
		}
		return core.DirectChatID(aciOf(sid.UUID)), uuid.Nil, nil
	}
	if _, perr := types.GroupIdentifier(s).Bytes(); perr != nil {
		return "", uuid.Nil, fmt.Errorf("%w: chat %q", errBadID, s)
	}
	return core.GroupChatID(s), uuid.Nil, nil
}

// hashID returns a short stable hex identifier derived from parts.
func hashID(prefix string, parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return prefix + hex.EncodeToString(h.Sum(nil)[:16])
}

// parseE164 parses "+<digits>" into the numeric form used by contact
// discovery. Spaces, dashes, dots and parentheses are ignored.
func parseE164(s string) (string, uint64, bool) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "+") {
		return "", 0, false
	}
	var b strings.Builder
	b.WriteByte('+')
	for _, r := range s[1:] {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '.' || r == '(' || r == ')':
		default:
			return "", 0, false
		}
	}
	norm := b.String()
	if len(norm) < 8 || len(norm) > 16 {
		return "", 0, false
	}
	n, err := strconv.ParseUint(norm[1:], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return norm, n, true
}
