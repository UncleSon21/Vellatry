-- +goose Up

-- What the CMO asked about a published report, and what came back.
--
-- A question is answered from that version's frozen snapshot and nothing else: not live
-- data, not another report, not another tenant. Keeping the question beside the version
-- is what makes that true, and it is also the record of what the report left unclear,
-- which is the most useful thing a report can tell its writer.
CREATE TABLE report_questions (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id      uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    report_id   uuid NOT NULL REFERENCES reports (id) ON DELETE CASCADE,
    version     integer NOT NULL,
    asked_by    text NOT NULL,                  -- the hub viewer's email
    question    text NOT NULL,
    answer      text,
    -- asked -> answered | unanswerable (the report does not hold it) | failed
    status      text NOT NULL DEFAULT 'asked' CHECK (status IN ('asked', 'answered', 'unanswerable', 'failed')),
    dropped     integer NOT NULL DEFAULT 0,     -- sentences the figure check removed
    follow_up   timestamptz,                    -- the reader asked the team to look at it
    asked_at    timestamptz NOT NULL DEFAULT now(),
    answered_at timestamptz
);
CREATE INDEX report_questions_version ON report_questions (org_id, report_id, version, asked_at);

-- +goose StatementBegin
DO $$
BEGIN
    EXECUTE 'ALTER TABLE report_questions ENABLE ROW LEVEL SECURITY';
    EXECUTE 'ALTER TABLE report_questions FORCE ROW LEVEL SECURITY';
    EXECUTE 'CREATE POLICY system_access ON report_questions TO vellatry_system USING (true) WITH CHECK (true)';
    EXECUTE 'CREATE POLICY tenant_isolation ON report_questions TO vellatry_tenant USING (org_id = app_org_id()) WITH CHECK (org_id = app_org_id())';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON report_questions TO vellatry_tenant, vellatry_system';
END
$$;
-- +goose StatementEnd

-- +goose Down
DROP TABLE report_questions;
