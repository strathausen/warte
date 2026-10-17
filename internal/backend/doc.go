// Package backend is warte's real core.Service: Signal through signalmeow
// (mautrix-signal's Go client) and libsignal, persisted in warte's encrypted
// store (internal/store). It is the only package that imports signalmeow.
//
// # Building
//
// The package needs cgo and lib/libsignal_ffi.a (make libsignal); cgo.go adds
// the link flags. It links SQLCipher through internal/store, which must stay
// the only SQLite driver in the binary. Production builds use
// -tags sqlite_fts5 (see internal/store).
//
// # Structure
//
//   - backend.go: Options, New, Start/Close, query methods (local state only).
//   - linker.go: Linker, QR provisioning into signalmeow's tables of the shared
//     database.
//   - connect.go: receive loops, connection state, storage-service mirroring,
//     maintenance.
//   - handle.go, calls.go, stories.go, syncs.go: signalmeow's event callback.
//     It runs on the receive loop, so it only writes to the store and queues
//     work; events reach the frontend through an unbounded queue (events.go)
//     and never block Signal. stories.go also holds Stories and
//     MarkStoryViewed; syncs.go the sync messages from our other devices that
//     the forked signalmeow surfaces (fork/CHANGES.md).
//   - send.go, groups.go, contacts.go, stickers.go: actions.
//   - media.go: attachment/avatar/sticker download, decryption and caching in
//     the encrypted blob store; upload preparation. Plaintext never touches
//     disk (signalmeow's history transfer stages its download in a temporary
//     file, but that file holds the encrypted, MAC-protected archive).
//   - history.go: import of the message history the phone transfers at link
//     time.
//   - expiry.go: disappearing messages and timed pins.
//   - join.go: joining groups by invite link, talking to the groups service
//     directly (signalmeow has no API for it) with signalmeow's group auth
//     and libsignal's zkgroup; zkgroup-server-public-params.dat is a copy of
//     the production parameters signalmeow embeds privately.
//   - profile_write.go: profile updates (PUT /v1/profile, avatar upload).
//   - export.go: ExportBackup's encrypted export format.
//   - convert_*.go: pure conversions between Signal protobufs and core types,
//     covered by table-driven tests.
//   - index.go: the backend's one table of its own (warte_be_msgindex), mapping
//     Signal's (author, timestamp) references, including edit timestamps, to
//     messages, plus backend state in store settings under "backend.".
//
// # Logging
//
// Logs go to Options.Logger. They carry chat IDs, ACIs and timestamps but
// never message bodies, phone numbers or keys. The logger is capped at
// debug level because signalmeow's trace logs dump message content and the
// provisioning secrets.
//
// # Robustness
//
// signalmeow does not recover panics in its event callback, and some of its
// decryption helpers index into untrusted ciphertext. The event handler and
// every background job recover panics (the envelope is dropped and an
// ErrorEvent emitted), and calls that decrypt remote data (attachments,
// stickers, avatars) go through guard.
//
// # Feature support
//
// Fully implemented:
//
//   - Linking (QR, refreshed every 45 s) and history transfer at link time.
//   - Receiving: text with formatting and mentions, quotes, attachments (lazy;
//     images and stickers up to 2 MB are fetched eagerly), long-text bodies,
//     stickers, link previews, contact cards, reactions, edits with history,
//     delete for everyone (also by group admins), view-once, disappearing
//     timers with Signal's timer versions, group updates as Signal-style
//     system text, polls (create/vote/end, stale votes ignored), pinned
//     messages (with expiry), story replies and story reactions, payment
//     notices (read-only), gift badges, delivery/read/viewed receipts,
//     typing indicators, read syncs, sent transcripts from our other
//     devices, delete-for-me syncs, message request responses, view-once
//     syncs, identity (safety number) changes, contact sync, decryption
//     failures.
//   - Stories: incoming stories (media and text, 1:1 and group) and stories
//     we post on the phone (sync transcripts, recipient updates), deleted for
//     everyone by their author, hidden for blocked contacts and groups,
//     expired after 24 hours with their media; media is downloaded on first
//     access (Attachment with the story's ID). Viewing a story syncs the
//     view to our other devices and, if story view receipts are enabled
//     (account record setting, falling back to read receipts; Desktop also
//     requires read receipts), sends a VIEWED receipt to the author. Stories
//     are dropped when the account disables stories.
//   - Calls: 1:1 offers ring with the right voice/video type and end on the
//     caller's hangup (missed), on "accepted/declined on another device"
//     hangups, on answers/hangups/busy from our own devices and on call
//     event syncs; offers older than two minutes on arrival are recorded as
//     missed without ringing. Group calls: the first join of a new call era
//     is shown as "Alice started a group call"; RingRTC group rings ring
//     (with Desktop's membership/blocked/announcement checks, muted chats are
//     recorded silently) and end on cancellation or a ring response from our
//     own devices. Call event syncs (accepted, declined, deleted; calls
//     placed on the phone) and call log syncs (clear, mark read) are applied.
//   - Sync messages: blocked list (complete list, applied to chats and
//     contacts), configuration (read receipts, typing indicators, link
//     previews, sealed sender indicators), sticker pack install/remove,
//     viewed (stories), keys and fetch-latest (storage re-read, own profile
//     refetch), phone number change (own number updated).
//   - Sending: text with ranges, quotes, attachments (dimensions and BlurHash
//     computed), long text (as text/x-signal-plain), stickers, link previews
//     (generated on request, https only, local addresses refused), polls,
//     view-once, edits, delete for everyone/for me, reactions, forwarding,
//     read receipts and read syncs, view-once opened, typing, poll votes and
//     ending, pins, disappearing timer changes, story replies.
//   - Groups: create, rename, describe, avatar, add/remove/promote/demote
//     members, approve/deny join requests, announcement-only, group link
//     modes and reset, timer, leave, accept invitations, join by invite link
//     (directly, or as a request when the link needs admin approval).
//   - Profile: name, about, about emoji and avatar (set/remove) through the
//     versioned profile API; payment address and phone number sharing are
//     preserved; other devices are asked to refetch (FetchLatest sync). The
//     profile key is not rotated.
//   - Contacts: profile fetching, recipient resolution (phone via contact
//     discovery, username, username link, signal.me phone link, ACI, contact
//     name), safety numbers (60 digits + scannable payload) and verification
//     (synced), blocking (blocked list synced), message request accept
//     (profile key shared) and spam reports.
//   - Storage service (read): archived/pinned/muted/marked-unread chats,
//     blocked state, usernames, verification, account settings, installed
//     sticker packs, custom chat folders (core.FolderMembers; read-only,
//     the "All chats" folder is skipped).
//   - Storage service (write, storage_write.go): archive, pin, mute and
//     marked-unread (contacts, groups, Note to Self), block/unblock and
//     message request acceptance, nicknames, synced account settings (read
//     receipts, typing, link previews, sealed sender indicators, default
//     timer, phone number sharing and discoverability) and sticker pack
//     installs. Changes apply locally at once and are written in batches
//     after 2s of quiet; conflicts with other devices are merged by
//     re-applying the changes to the newest records; other devices are
//     asked to refetch (FetchLatest sync). The queue is durable: pending
//     changes (made offline or unsent at quit) are kept in the store,
//     replayed after connecting before the first storage read, and cleared
//     once written; until then storage reads leave the local state alone.
//   - Sticker packs: install/remove (synced to own devices), browse, images.
//   - Media access, encrypted export, unlink.
//
// Partially implemented (signalmeow limitations):
//
//   - Storage writes need a storage manifest created by the primary device
//     and the master key; without them, changes stay on this device. A
//     batch that keeps failing is parked after five tries and retried
//     after the next reconnect, local change or start; meanwhile storage
//     reads are not mirrored. The notification content level is local
//     only (as in Signal Desktop) and never written; story hiding and
//     contact notes have no warte setting and are not written.
//   - Calls: warte never answers or declines. A call that rings for two
//     minutes without any end signal is recorded as missed. Ad-hoc (call
//     link) calls are ignored. Call log changes have no dedicated frontend
//     event (chats are re-announced).
//   - Viewed syncs for ordinary messages (voice notes) change nothing
//     locally; read syncs cover them.
//   - History transfer imports messages, reactions, edits, polls, calls and
//     timer changes; other chat updates (group changes, profile changes) are
//     skipped, and media only available from Signal's paid backup tier is
//     shown as unavailable.
//   - Posting stories (PostStory, stories_post.go; sending is in the fork's
//     sending_story_warte.go) goes to "My Story" and/or groups. My Story's
//     audience follows its storage service record (all connections, all
//     except, only with; Signal's defaults if there is none). "Signal
//     connections" are approximated as signalmeow recipients with profile
//     sharing or a system contact name plus accepted direct chats with
//     messages. Custom distribution lists, body ranges in captions and
//     text highlight colors are not offered (core has no fields for them).
//     Delete(storyID, true) deletes our story for everyone.
package backend
