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

package events

// warte: this file adds events for content upstream signalmeow drops:
// stories, the sync messages it ignores, and richer call signalling.

import (
	"github.com/google/uuid"

	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/types"
)

func (*Story) isSignalEvent()                {} // warte:
func (*StoryRecipientUpdate) isSignalEvent() {} // warte:
func (*BlockedSync) isSignalEvent()          {} // warte:
func (*ConfigurationSync) isSignalEvent()    {} // warte:
func (*StickerPackSync) isSignalEvent()      {} // warte:
func (*ViewedSync) isSignalEvent()           {} // warte:
func (*CallEventSync) isSignalEvent()        {} // warte:
func (*CallLogEventSync) isSignalEvent()     {} // warte:
func (*KeysSync) isSignalEvent()             {} // warte:
func (*FetchLatest) isSignalEvent()          {} // warte:
func (*PniChangeNumber) isSignalEvent()      {} // warte:

// StoryRecipient is one recipient of a story we sent from another device
// (SyncMessage.Sent.storyMessageRecipients).
//
// warte: added.
type StoryRecipient struct {
	ServiceID           string // ACI (or "PNI:" prefixed) service ID string
	DistributionListIDs []string
	AllowedToReply      bool
}

// Story is an incoming story (Content.storyMessage), or a sync transcript of
// a story we posted on another device (SyncMessage.Sent.storyMessage).
//
// Stories carry no timestamp of their own: Timestamp is the envelope's
// client timestamp for received stories (as in Signal-Desktop) and the
// Sent.timestamp for sync transcripts. Info.ChatID is the group for group
// stories and the author's service ID otherwise; GroupID is empty for
// stories sent to distribution lists ("My Story" and custom lists).
//
// warte: added.
type Story struct {
	Info          MessageInfo
	Timestamp     uint64
	Story         *signalpb.StoryMessage
	GroupID       types.GroupIdentifier
	AllowsReplies bool

	// Sync transcripts only.
	IsSync            bool
	Recipients        []StoryRecipient
	IsRecipientUpdate bool
	// EnvelopeStory reports the envelope's story flag (Signal-Desktop logs a
	// story without it as malformed but still accepts it).
	EnvelopeStory bool
}

// StoryRecipientUpdate is a sync transcript that only changes who can see a
// story we sent (Sent.isRecipientUpdate with storyMessageRecipients and no
// story content). An empty Recipients list means nobody can see it anymore.
//
// warte: added.
type StoryRecipientUpdate struct {
	Timestamp  uint64
	Recipients []StoryRecipient
}

// BlockedSync is the full list of blocked contacts and groups sent by our
// primary device (SyncMessage.blocked). It replaces the previous list.
//
// warte: added.
type BlockedSync struct {
	ACIs     []uuid.UUID
	E164s    []string
	GroupIDs []types.GroupIdentifier
}

// ConfigurationSync carries account settings changed on another device
// (SyncMessage.configuration). Nil fields were not present.
//
// warte: added.
type ConfigurationSync struct {
	ReadReceipts                   *bool
	UnidentifiedDeliveryIndicators *bool
	TypingIndicators               *bool
	LinkPreviews                   *bool
}

// StickerPackOperation is one install/remove from SyncMessage.stickerPackOperation.
//
// warte: added.
type StickerPackOperation struct {
	PackID  []byte
	PackKey []byte
	Install bool
}

// StickerPackSync carries sticker pack installs/removals from another device.
//
// warte: added.
type StickerPackSync struct {
	Operations []StickerPackOperation
}

// ViewedMessage identifies a message (or story) viewed on another device.
//
// warte: added.
type ViewedMessage struct {
	Sender    uuid.UUID
	Timestamp uint64
}

// ViewedSync is SyncMessage.viewed: messages and stories viewed on another
// of our devices.
//
// warte: added.
type ViewedSync struct {
	Timestamp uint64
	Messages  []ViewedMessage
}

// CallEventSync is SyncMessage.callEvent: a call was accepted, declined,
// deleted or observed on another device.
//
// warte: added.
type CallEventSync struct {
	Timestamp uint64 // envelope timestamp
	Event     *signalpb.SyncMessage_CallEvent
}

// CallLogEventSync is SyncMessage.callLogEvent: the call log was cleared or
// marked read on another device.
//
// warte: added.
type CallLogEventSync struct {
	Timestamp uint64 // envelope timestamp
	Event     *signalpb.SyncMessage_CallLogEvent
}

// KeysSync reports that the primary device sent account keys
// (SyncMessage.keys). signalmeow has already stored the derived master key
// and queued a storage sync when MasterKeyChanged is set.
//
// warte: added.
type KeysSync struct {
	HasAccountEntropyPool bool
	MasterKeyChanged      bool
}

// FetchLatest asks this device to refetch some state (SyncMessage.fetchLatest).
//
// warte: added.
type FetchLatest struct {
	Type signalpb.SyncMessage_FetchLatest_Type
}

// PniChangeNumber reports that our phone number changed on the primary
// device. signalmeow does not apply the new PNI identity; this event is
// informational.
//
// warte: added.
type PniChangeNumber struct {
	NewE164 string
}

// CallKind classifies an events.Call.
//
// warte: added.
type CallKind int

const (
	CallKindLegacy        CallKind = iota // events produced by upstream code paths
	CallKindOffer                         // 1:1 offer: ringing
	CallKindAnswer                        // 1:1 answer (only from our own devices is meaningful)
	CallKindHangup                        // 1:1 hangup
	CallKindBusy                          // 1:1 busy
	CallKindGroupUpdate                   // DataMessage.groupCallUpdate (someone joined a group call)
	CallKindRingIntention                 // RingRTC group ring or ring cancellation (CallMessage.opaque)
	CallKindRingResponse                  // RingRTC ring response from one of our own devices
)

// RingIntentionType mirrors RingRTC's CallMessage.RingIntention.Type.
//
// warte: added.
type RingIntentionType int

const (
	RingIntentionRing      RingIntentionType = 0
	RingIntentionCancelled RingIntentionType = 1
)

// RingResponseType mirrors RingRTC's CallMessage.RingResponse.Type.
//
// warte: added.
type RingResponseType int

const (
	RingResponseRinging  RingResponseType = 0
	RingResponseAccepted RingResponseType = 1
	RingResponseDeclined RingResponseType = 2
	RingResponseBusy     RingResponseType = 3
)
