-- +goose Up

-- When the team finished the setup wizard. Until then the dashboard sends them back to
-- it, and the paid work that depends on their answers (keyword research seeded from
-- their topics) waits.
ALTER TABLE org_settings ADD COLUMN onboarded_at timestamptz;

-- Organisations that already exist set up through the api directly.
UPDATE org_settings SET onboarded_at = now() WHERE onboarded_at IS NULL;

-- +goose Down
ALTER TABLE org_settings DROP COLUMN onboarded_at;
