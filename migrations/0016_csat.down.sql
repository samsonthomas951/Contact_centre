ALTER TABLE mart_tickets_daily
  DROP COLUMN IF EXISTS csat_avg,
  DROP COLUMN IF EXISTS csat_responses;
DROP TABLE IF EXISTS csat_surveys;
