-- v2: remote-deleted flag as an indexed column, so Search can filter it in
-- SQL (before LIMIT) instead of after decoding.

ALTER TABLE warte_messages ADD COLUMN deleted INTEGER NOT NULL DEFAULT 0;
