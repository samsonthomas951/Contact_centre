-- 0021_canned_reply_actions: turn canned_replies into macros.
--
-- A macro is a canned_reply with one or more `actions` to run after
-- the reply is sent. Each action is a small JSON object:
--
--   {"type":"set_state","to":"resolved"}
--   {"type":"add_tag","tag_slug":"refund"}
--   {"type":"assign","to":"me"|"unassign"|"<agent-uuid>"}
--
-- Stored as a JSONB array so the schema doesn't change when new
-- action kinds land. NULL is normalised to '[]' so the application
-- never has to special-case it.

BEGIN;

ALTER TABLE canned_replies
  ADD COLUMN actions JSONB NOT NULL DEFAULT '[]'::jsonb;

COMMIT;
