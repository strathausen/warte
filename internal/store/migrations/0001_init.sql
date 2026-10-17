-- warte app schema v1. The full-text index (warte_fts) is created at runtime
-- because its module (FTS5 or FTS4) depends on the build; see fts.go.

CREATE TABLE warte_meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE warte_chats (
	id              TEXT PRIMARY KEY,
	kind            INTEGER NOT NULL,
	title           TEXT NOT NULL DEFAULT '',
	archived        INTEGER NOT NULL DEFAULT 0,
	pinned          INTEGER NOT NULL DEFAULT 0,
	pin_order       INTEGER NOT NULL DEFAULT 0,
	marked_unread   INTEGER NOT NULL DEFAULT 0,
	muted_until     INTEGER NOT NULL DEFAULT 0,
	blocked         INTEGER NOT NULL DEFAULT 0,
	message_request INTEGER NOT NULL DEFAULT 0,
	unread          INTEGER NOT NULL DEFAULT 0,
	mentions_unread INTEGER NOT NULL DEFAULT 0,
	last_activity   INTEGER NOT NULL DEFAULT 0,
	last_author     TEXT NOT NULL DEFAULT '',
	last_preview    BLOB,
	data            BLOB NOT NULL
);
CREATE INDEX warte_chats_order ON warte_chats (archived, pinned DESC, pin_order, last_activity DESC);

CREATE TABLE warte_drafts (
	chat_id    TEXT PRIMARY KEY REFERENCES warte_chats (id) ON DELETE CASCADE,
	body       TEXT NOT NULL,
	updated_at INTEGER NOT NULL
);

CREATE TABLE warte_contacts (
	aci          TEXT PRIMARY KEY,
	e164         TEXT NOT NULL DEFAULT '',
	username     TEXT NOT NULL DEFAULT '',
	display_name TEXT NOT NULL DEFAULT '',
	is_self      INTEGER NOT NULL DEFAULT 0,
	data         BLOB NOT NULL
);
CREATE INDEX warte_contacts_e164 ON warte_contacts (e164) WHERE e164 <> '';
CREATE INDEX warte_contacts_username ON warte_contacts (username COLLATE NOCASE) WHERE username <> '';

CREATE TABLE warte_messages (
	chat_id       TEXT NOT NULL REFERENCES warte_chats (id) ON DELETE CASCADE,
	author        TEXT NOT NULL,
	ts            INTEGER NOT NULL,
	sent_at       INTEGER NOT NULL,
	received_at   INTEGER NOT NULL DEFAULT 0,
	kind          INTEGER NOT NULL,
	outgoing      INTEGER NOT NULL,
	unread        INTEGER NOT NULL DEFAULT 0,
	mentions_self INTEGER NOT NULL DEFAULT 0,
	media_flags   INTEGER NOT NULL DEFAULT 0,
	expires_at    INTEGER NOT NULL DEFAULT 0,
	story_author  TEXT NOT NULL DEFAULT '',
	story_ts      INTEGER NOT NULL DEFAULT 0,
	body          TEXT NOT NULL DEFAULT '',
	data          BLOB NOT NULL,
	UNIQUE (chat_id, author, ts)
);
CREATE INDEX warte_messages_order ON warte_messages (chat_id, sent_at, author, ts);
CREATE INDEX warte_messages_unread ON warte_messages (chat_id, sent_at) WHERE unread = 1;
CREATE INDEX warte_messages_expiry ON warte_messages (expires_at) WHERE expires_at > 0;
CREATE INDEX warte_messages_sent ON warte_messages (sent_at);
CREATE INDEX warte_messages_author ON warte_messages (author, sent_at);
CREATE INDEX warte_messages_story ON warte_messages (story_author, story_ts) WHERE story_ts > 0;

CREATE TABLE warte_receipts (
	chat_id    TEXT NOT NULL,
	author     TEXT NOT NULL,
	ts         INTEGER NOT NULL,
	recipient  TEXT NOT NULL,
	status     INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	PRIMARY KEY (chat_id, author, ts, recipient),
	FOREIGN KEY (chat_id, author, ts) REFERENCES warte_messages (chat_id, author, ts) ON DELETE CASCADE
);

CREATE TABLE warte_stories (
	author   TEXT NOT NULL,
	ts       INTEGER NOT NULL,
	group_id TEXT NOT NULL DEFAULT '',
	at       INTEGER NOT NULL,
	viewed   INTEGER NOT NULL DEFAULT 0,
	data     BLOB NOT NULL,
	PRIMARY KEY (author, ts)
);
CREATE INDEX warte_stories_at ON warte_stories (at);

CREATE TABLE warte_calls (
	chat_id    TEXT NOT NULL,
	started_at INTEGER NOT NULL,
	unread     INTEGER NOT NULL DEFAULT 0,
	data       BLOB NOT NULL,
	PRIMARY KEY (chat_id, started_at)
);
CREATE INDEX warte_calls_started ON warte_calls (started_at);

CREATE TABLE warte_sticker_packs (
	id        TEXT PRIMARY KEY,
	installed INTEGER NOT NULL DEFAULT 0,
	position  INTEGER NOT NULL DEFAULT 0,
	title     TEXT NOT NULL DEFAULT '',
	data      BLOB NOT NULL
);

CREATE TABLE warte_folders (
	id       TEXT PRIMARY KEY,
	position INTEGER NOT NULL,
	data     BLOB NOT NULL
);

CREATE TABLE warte_settings (
	key   TEXT PRIMARY KEY,
	value BLOB NOT NULL
);

CREATE TABLE warte_attachments (
	id           TEXT PRIMARY KEY,
	owner        TEXT NOT NULL DEFAULT '',
	blob_key     TEXT NOT NULL DEFAULT '',
	content_type TEXT NOT NULL DEFAULT '',
	file_name    TEXT NOT NULL DEFAULT '',
	size         INTEGER NOT NULL DEFAULT 0,
	digest       BLOB,
	downloaded   INTEGER NOT NULL DEFAULT 0,
	remote       BLOB,
	created_at   INTEGER NOT NULL
);
CREATE INDEX warte_attachments_owner ON warte_attachments (owner);
CREATE INDEX warte_attachments_blob ON warte_attachments (blob_key) WHERE blob_key <> '';
