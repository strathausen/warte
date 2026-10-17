// mautrix-signal - A Matrix-signal puppeting bridge.
// Copyright (C) 2024 Tulir Asokan
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

import (
	"github.com/google/uuid"

	"go.mau.fi/mautrix-signal/pkg/libsignalgo"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/types"
)

type SignalEvent interface {
	isSignalEvent()
}

func (*ChatEvent) isSignalEvent()              {}
func (*DecryptionError) isSignalEvent()        {}
func (*Receipt) isSignalEvent()                {}
func (*ViewOnceOpen) isSignalEvent()           {}
func (*ReadSelf) isSignalEvent()               {}
func (*Call) isSignalEvent()                   {}
func (*ContactList) isSignalEvent()            {}
func (*ACIFound) isSignalEvent()               {}
func (*DeleteForMe) isSignalEvent()            {}
func (*MessageRequestResponse) isSignalEvent() {}
func (*QueueEmpty) isSignalEvent()             {}
func (*LoggedOut) isSignalEvent()              {}
func (*IdentityChange) isSignalEvent()         {}

type MessageInfo struct {
	Sender uuid.UUID
	ChatID string

	GroupRevision   uint32
	ServerTimestamp uint64
	ServerGUID      string
	ReportingToken  []byte
}

type ChatEvent struct {
	Info  MessageInfo
	Event signalpb.ChatEventContent
}

type DecryptionError struct {
	Sender    uuid.UUID
	Err       error
	Timestamp uint64
}

type Receipt struct {
	Sender  uuid.UUID
	Content *signalpb.ReceiptMessage
}

type ViewOnceOpen struct {
	*signalpb.SyncMessage_ViewOnceOpen
}

type ReadSelf struct {
	Timestamp uint64
	Messages  []*signalpb.SyncMessage_Read
}

type Call struct {
	Info      MessageInfo
	Timestamp uint64
	IsRinging bool

	// warte: the fields below describe the call signal in more detail.
	// Kind is CallKindLegacy only for events built by code that does not
	// set it.
	Kind CallKind
	// CallID is the 1:1 call ID (Offer/Hangup/Answer/Busy .id) or, for group
	// rings, the RingRTC ring ID (as unsigned).
	CallID uint64
	// IsVideo is set for 1:1 offers of type OFFER_VIDEO_CALL.
	IsVideo bool
	// HangupType and HangupDeviceID come from CallMessage.hangup.
	HangupType     signalpb.CallMessage_Hangup_Type
	HangupDeviceID uint32
	// DestinationDeviceID is CallMessage.destinationDeviceId (0 = all devices).
	DestinationDeviceID uint32
	// GroupID is the group of a group call update or ring.
	GroupID types.GroupIdentifier
	// EraID is DataMessage.groupCallUpdate.eraId.
	EraID string
	// RingIntention / RingResponse are set for the respective kinds.
	RingIntention RingIntentionType
	RingResponse  RingResponseType
}

type ContactList struct {
	Contacts []*types.Recipient
	IsFromDB bool
}

type ACIFound struct {
	PNI libsignalgo.ServiceID
	ACI libsignalgo.ServiceID
}

type DeleteForMe struct {
	Timestamp uint64
	*signalpb.SyncMessage_DeleteForMe
}

type MessageRequestResponse struct {
	Timestamp uint64
	ThreadACI uuid.UUID
	GroupID   *libsignalgo.GroupIdentifier
	Type      signalpb.SyncMessage_MessageRequestResponse_Type
	Raw       *signalpb.SyncMessage_MessageRequestResponse
}

type QueueEmpty struct{}

type LoggedOut struct{ Error error }

type IdentityChange struct {
	ServiceID libsignalgo.ServiceID
}
