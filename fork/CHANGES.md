# Changes to the forked mautrix-signal

Every change is marked with a `// warte:` comment in the code. Sections are
grouped by topic so they can be upstreamed separately.

## Receive side: stories, dropped sync messages, call signalling

Files: `pkg/signalmeow/receiving_warte.go` (new), `pkg/signalmeow/events/warte.go`
(new), `pkg/signalmeow/receiving_warte_test.go` (new), small hooks in
`pkg/signalmeow/receiving.go` and `pkg/signalmeow/events/message.go`.
Reference behaviour: Signal-Desktop `ts/textsecure/MessageReceiver.preload.ts`.

- **Stories.** `Content.storyMessage` was ignored. It now emits
  `events.Story` (timestamp = envelope client timestamp, group ID for group
  stories, `AllowsReplies`); stories from blocked senders are dropped, the
  profile key is stored, and no delivery receipt is sent (as in Desktop).
  Sync transcripts `SyncMessage.Sent.storyMessage` emit `events.Story` with
  `IsSync`, `Recipients` (from `storyMessageRecipients`) and
  `IsRecipientUpdate`; a recipient update without content emits
  `events.StoryRecipientUpdate` (Desktop's order: recipient update first).
- **Story-flagged envelopes** carrying a DataMessage that is neither a story
  reply nor a delete are dropped (Desktop does the same).
- **Sync messages** that were ignored now emit events:
  `Blocked` → `events.BlockedSync` (old and new fields merged; the recipient
  store's blocked flags are updated, unblocking recipients missing from the
  complete list), `Configuration` → `events.ConfigurationSync`,
  `stickerPackOperation` → `events.StickerPackSync`, `viewed` →
  `events.ViewedSync`, `CallEvent` → `events.CallEventSync`, `CallLogEvent` →
  `events.CallLogEventSync`, `PniChangeNumber` → `events.PniChangeNumber`
  (informational; the new PNI identity is not adopted). `Keys` and
  `FetchLatest` keep their behaviour and additionally emit `events.KeysSync`
  (with `MasterKeyChanged`) and `events.FetchLatest`. `Read` syncs now also
  run alongside the repeated fields instead of exclusively.
- **Calls.** `events.Call` gained `Kind`, `CallID`, `IsVideo` (Offer.type),
  `HangupType`/`HangupDeviceID`, `DestinationDeviceID`, `GroupID`, `EraID`,
  `RingIntention` and `RingResponse`. Busy and Answer messages are surfaced,
  and `CallMessage.opaque` is parsed for RingRTC's group ring intentions
  (ring/cancel) and ring responses (accepted/declined/busy from our own
  devices). The opaque payload is RingRTC's `signaling.proto` `CallMessage`
  (fields 2 `RingIntention`, 3 `RingResponse`: `group_id` bytes=1, `type`
  enum=2, `ring_id` sfixed64=3), decoded with `protowire`. Group call updates
  carry their era ID so clients can track calls across restarts.
  `IsRinging` keeps its old meaning for existing consumers.
- **`Client.SendReceiptWithoutSync`.** `SendMessage` turns a single VIEWED
  receipt into a ViewOnceOpen sync, which is wrong for story views. The new
  method sends a receipt without any sync copy and without consulting the
  read receipt setting (the caller decides; Desktop gates story view
  receipts on its own setting); it still skips probable message requests.

## Storage service writes

Files: `pkg/signalmeow/storageservice_warte.go` (new),
`pkg/signalmeow/storageservice_warte_memory.go` (new, in-memory server for
tests), `pkg/signalmeow/storageservice_warte_test.go` (new), three fields on
`Client` in `pkg/signalmeow/client.go`. The read path in `storageservice.go`
is unchanged. Reference behaviour: Signal-Desktop
`ts/services/storage.preload.ts` (generateManifest, encryptManifest,
uploadManifest, upload), `ts/services/storageRecordOps.preload.ts`
(to*Record), `ts/Crypto.node.ts` (key derivation, encryptProfile);
Signal-Android `StorageSyncJob.kt`, `StorageServiceApi.kt`.

- **`Client.StorageWrite(ctx, mutate)`** fetches the manifest and records,
  runs `mutate(*StorageState)`, and writes back only the records that
  changed (`proto.Equal` against the fetched copy). Each changed record gets
  a fresh random 16-byte ID: the old ID goes into `deleteKey`, the new item
  into `insertItem`. New records are appended to the manifest; unchanged
  ones keep their IDs and position.
- **Encryption** matches the read path: items are AES-256-GCM with a random
  12-byte nonce prepended, keyed by `HMAC-SHA256(storageKey, "Item_"+b64(id))`
  or, when the manifest has a `recordIkm`, by
  `HKDF-SHA256(ikm=recordIkm, salt="", info="20240801_SIGNAL_STORAGE_SERVICE_ITEM_"||id)`.
  The manifest is `version = remote+1`, `sourceDevice` = our device ID,
  `recordIkm` passed through, encrypted with
  `HMAC-SHA256(storageKey, "Manifest_"+version)`. An empty `recordIkm` is
  treated as absent, as Desktop's `Bytes.isNotEmpty` check does.
- **Unknown fields are preserved:** the decoded records and manifest keep
  Go protobuf unknown fields and re-emit them. Records of unknown types,
  records that failed to decrypt, and unchanged records are never re-encoded.
- **Writes:** `PUT /v1/storage?sendRemoteManifestOnConflict=false` (Android's
  query parameter). On 409 (`ErrStorageConflict`) it refetches, re-runs
  `mutate` on the newer state and retries, backing off 1s and then 5s, and
  gives up after 3 attempts (`MaxStorageWriteAttempts`). So `mutate` must
  express changes ("set archived"), not overwrite whole records.
- **Errors:** a 404 manifest is `ErrStorageNoManifest`; a missing master key
  is `ErrNoStorageKey`.
- **Caching:** the manifest and decrypted records are kept between writes.
  The manifest is re-checked with `GET /v1/storage/manifest/version/{v}`
  (204 = unchanged), and only IDs not seen before are read. After a
  successful write that changed the account record, the store's account record
  is updated and saved.
- **Helpers:**
  - Records: `StorageState.Account/Contact/UpsertContact/GroupV2/UpsertGroupV2/StickerPack`.
  - Sticker packs: `SetStickerPackInstalled`, which follows Desktop's
    toInstalledStickerPackRecord. A new install gets position max+1;
    uninstalling clears the key and position and sets `deletedAtTimestamp`.
  - Blocking: `SetContactBlocked` / `SetGroupBlocked` set `blocked`,
    `blockedAtTimestamp` and `whitelisted`. Blocking turns profile sharing
    off and unblocking turns it on, as Android's RecipientUtil does.
  - Pinning: `SetPinned` with a `PinnedTarget` appends or removes entries.
    Release-notes and legacy-group entries are kept. It matches
    `serviceId` / `serviceIdBinary` and writes the string form.
  - New contact records carry the ACI as a string, the encoding every
    client reads.
- **`Client.StorageTransport`** (interface `StorageTransport`) replaces the
  HTTP layer; nil uses storage.signal.org. `MemoryStorage` is an in-memory
  implementation with the server's write rules, for tests.
- Sending `SyncMessage.FetchLatest{STORAGE_MANIFEST}` afterwards is left to
  the caller.

## Send side: posting and deleting stories

Files: `pkg/signalmeow/sending_story_warte.go` (new),
`pkg/signalmeow/sending_story_warte_test.go` (new), small hooks in
`pkg/signalmeow/sending.go` and `pkg/signalmeow/retry.go`.
Reference behaviour: Signal-Desktop `ts/util/sendStoryMessage.preload.ts`,
`ts/jobs/helpers/sendStory.preload.ts`, `ts/util/sendToGroup.preload.ts`
(story paths), `ts/util/deleteStoryForEveryone.preload.ts`,
`ts/jobs/helpers/sendDeleteStoryForEveryone.preload.ts`; server semantics
from Signal-Server `MessageController` (story=true: no access checks, 200
for unknown recipients, group send tokens rejected).

- **Hooks in `sendContent`** (via context options, so nothing changes for
  existing callers): a story send uses `PUT /v1/messages/{id}?story=true`,
  is always sealed sender, sends no `Unidentified-Access-Key`, and never
  falls back to an unsealed send on 401 (the recipient could not tell who
  sent it). A separate option forces `urgent=false` (story sync
  transcripts). `isUrgent` now returns false and `getContentHint` returns
  Implicit for `StoryMessage`, as Desktop sends them; upstream never sent
  stories, so this changes nothing there.
- **Retry receipts:** a cached `StoryMessage` is resent with story semantics.
- **Distribution lists:** `StoryDistributionLists` reads the storage
  service's `StoryDistributionListRecord`s (through the cached, locked
  read path of `StorageWrite`, without writing). `StoryPrivacyFromRecord`,
  `MyStoryPrivacy` and `DefaultMyStory` parse them; `StoryPrivacy.Mode`
  gives all connections / all except / only with (only My Story uses
  `isBlockList`, like Desktop). `StoryAudience` computes the recipients
  (ACIs only, deduplicated, with a caller exclusion filter);
  `StoryConnections` returns signalmeow's "Signal connections" (profile
  sharing or system contact name, not blocked, not unregistered).
- **Content:** `BuildTextAttachment` (Desktop's getTextAttachmentProto:
  gradients carry startColor/endColor plus colors/positions/angle, default
  angle 180; defaults white on Desktop's BG1 blue; preview url/title/image)
  and `BuildStoryMessage` (exactly one attachment, profile key required).
- **`SendListStory`** (My Story = `MyStoryID`, the all-zero UUID) and
  **`SendGroupStory`** (StoryMessage.group = master key + revision,
  allowsReplies=true): sender key when at least two recipients have
  sessions (Desktop's minimum), with `PUT
  /v1/messages/multi_recipient?ts=…&online=false&urgent=false&story=true`
  and no auth header; everything else 1:1 story sends. The outbound sender
  key of a list is stored under `story:<list uuid>`; groups share the
  group's key and their SKDMs are normal sealed-sender sends (Desktop: SKDMs
  are story sends only for distribution lists). Afterwards a
  `SyncMessage.Sent` transcript with `storyMessage` (allowsReplies=true, as
  Desktop) and `storyMessageRecipients` (list IDs; none for groups) goes to
  our devices, urgent=false, destination = our ACI for lists and unset for
  groups; no sync if nobody received the story.
- **`DeleteListStory`:** `DataMessage.delete` sent 1:1 with story=true to
  each former recipient, then a `Sent` recipient update
  (`isRecipientUpdate`, no story) listing them with no distribution lists,
  which makes other devices delete it. Group stories are deleted with a
  normal group delete by the caller.
- **Not done:** custom distribution list management, body ranges in
  captions, `isAlreadyIncludedInAnotherDistributionList` bookkeeping
  (only one list per send), Desktop's "untrusted identity" stop, and
  resending SKDMs on retry receipts for list stories (the sealed sender
  content of a list story has no group ID to look the key up by).

## Storage write review fixes (wire/crypto review)

Cross-checked `storageservice_warte.go` against Signal-Desktop
`ts/Crypto.node.ts` (deriveStorageServiceKey / deriveStorageManifestKey /
deriveStorageItemKey), `ts/services/storage.preload.ts` (generateManifest,
encryptManifest, uploadManifest, conflictBackOff),
`storageRecordOps.preload.ts` (to*/merge*Record, applyMessageRequestState)
and Android `StorageServiceApi.kt` (`PUT /v1/storage?sendRemoteManifestOnConflict=false`,
409 = version != remote+1). Key derivation, AES-GCM layout (12-byte nonce ||
ct||tag, no AD), manifest version/sourceDevice/recordIkm passthrough,
immutable record IDs and the fork's proto field numbers all match.

- **`SetStickerPackInstalled`:** never produces an installed record without
  a pack key (Desktop's mergeInstalledStickerPackRecord throws on it);
  `validateStorageRecord` rejects one as well.
- **`StorageWrite`:** the store's account record gets a clone of the written
  account record, so in-place edits elsewhere cannot alias the cached
  "as fetched" copy used for change detection.
- **`loadStorageState`:** duplicate-ID check uses a map (was O(n²)).
- **Tests:** fixed known-answer vectors for the storage key, manifest key
  and both item-key derivations (computed independently in Python), and the
  sticker-pack key rule.
- **Backend (`internal/backend/storage_write.go`, not fork):** new contact
  and group records are seeded with `whitelisted` from the local chat state
  and the contact's profile key. Desktop's applyMessageRequestState and
  Android copy `whitelisted` into profile sharing, so the old default
  `false` would have put an accepted chat back into message-request state
  on the other devices.

## Receive/send protocol review fixes (stories, syncs, calls)

Cross-checked against Signal-Desktop `MessageReceiver.preload.ts`
(story-flag gate, handleStoryMessage, sent-transcript dispatch),
`onStoryRecipientUpdate.preload.ts`, `sendStory.preload.ts`,
`sendToGroup.preload.ts`, `stories.preload.ts`/`sendReceipts.preload.ts`
(story view receipts) and RingRTC `signaling.proto` / `group_call.rs`
(`RingId::from_era_id`).

- **Story-flag gate (`receiving.go`, `storyEnvelopeContentAllowed`):** the
  check now covers every content type, after SKDM processing, as in
  Desktop: a story-flagged envelope must hold a story, a *group* story
  reply (storyContext + groupV2) or a delete. Before, only DataMessages
  were checked, so edit, call, receipt, typing and sync content and 1:1
  story replies passed in story=true envelopes (which skip the server's
  access-key checks).
- **Sent transcripts (`handleSyncSentStory`):** `isRecipientUpdate` with
  `storyMessageRecipients` is a recipient update even if the story is
  attached (Desktop checks only those two fields).
- **Tests:** the gate table and the recipient-update transcript.
- **Backend (not fork):** recipient updates ignore group stories and treat
  a story only we can still see as deleted (Desktop); group rings, group
  call updates and group call-event syncs are linked by
  `callIDFromEra(eraId)` (= RingRTC ring ID = CallEvent.callId) before the
  time-window fallback.

## Attachment download size bound

Files: `pkg/signalmeow/attachments.go`, `pkg/signalmeow/web/web.go`,
`pkg/signalmeow/attachments_warte_test.go` (new).

- **`DownloadAttachment`** allocated the whole CDN body from
  `Content-Length`, whatever the pointer's declared `size` said, so a
  sender could make a small-looking attachment (eagerly fetched thumbnails,
  long text, stickers) exhaust memory. A body larger than
  `MaxAttachmentCiphertextSize(size)` (size + 5% padding + 4 KiB) is now
  refused with `ErrAttachmentTooLarge` before anything is allocated.
- **`web.GetAttachment`** indexed `CDNHosts[cdnNumber]` when `cdnNumber ==
  len(CDNHosts)` (off by one), so a peer's pointer with `cdnNumber` 4
  panicked; it now falls back to CDN 0.
- **`httpReqCounter`** was a plain int incremented by concurrent requests
  (a data race); it is now an `atomic.Int64`.

## Account record guarded against concurrent access

Files: `pkg/signalmeow/store/device.go`, `pkg/signalmeow/store/container.go`,
`pkg/signalmeow/store/device_warte_test.go` (new), `pkg/signalmeow/sending.go`,
`pkg/signalmeow/storageservice.go`, `pkg/signalmeow/storageservice_warte.go`.

- **Data race:** `Store.AccountRecord` was a plain exported pointer. The
  storage read (`StorageSync`) and `StorageWrite` replaced it while sends
  read it (phone number sharing, typing indicator and read receipt
  checks) and warte's settings code edited it, all on different
  goroutines.
- **Fix:** the field is now unexported (`accountRecord`) behind a
  `sync.RWMutex` in `DeviceData`, reached only through accessors:
  `AccountRecord()` (a `proto.Clone` copy, nil if unknown),
  `HasAccountRecord()`, `SetAccountRecord(ar)` (stores a copy) and
  `UpdateAccountRecord(fn)` (atomic clone-modify-store; false when no
  record is known). `PutDevice` marshals a copy taken under the lock and
  the device loader stores the decoded record with `SetAccountRecord`.
  All fork users were switched over (`sending.go`, `storageservice.go`,
  `storageservice_warte.go`); renaming the field makes any missed caller a
  compile error.
- **Tests:** a `-race` test hammers concurrent set/update/read/marshal and
  checks copy-on-read; the backend has a matching test across
  `StorageWrite`, settings updates and story code.

## Storage read path uses an injected StorageTransport

Files: `pkg/signalmeow/storageservice.go`.

- `fetchStorageManifest` and `fetchStorageItemsChunk` use
  `Client.StorageTransport` when it is set (same decryption as before), so
  tests with `MemoryStorage` can run `FetchStorage` / `StorageSync`
  without reaching storage.signal.org. With no transport set nothing
  changes.
