BEGIN;
DROP TABLE IF EXISTS email_outbound_log;
DROP TABLE IF EXISTS email_webhook_events;
DROP TABLE IF EXISTS email_mailboxes;

ALTER TABLE csat_surveys DROP CONSTRAINT csat_surveys_channel_check;
ALTER TABLE csat_surveys ADD CONSTRAINT csat_surveys_channel_check
  CHECK (channel IN ('fb','x','wa','ig','widget','voice'));

ALTER TABLE mart_tickets_daily DROP CONSTRAINT mart_tickets_daily_channel_check;
ALTER TABLE mart_tickets_daily ADD CONSTRAINT mart_tickets_daily_channel_check
  CHECK (channel IN ('fb','x','wa','ig','widget','voice','all'));

ALTER TABLE conversations DROP CONSTRAINT conversations_channel_check;
ALTER TABLE conversations ADD CONSTRAINT conversations_channel_check
  CHECK (channel IN ('fb','x','wa','ig','widget','voice'));
COMMIT;
