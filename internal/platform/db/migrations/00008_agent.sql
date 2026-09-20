-- +goose Up

-- Every question the agent is asked, with what the router made of it and what came
-- back. It is the log the team can read, and the labelled data the trained router will
-- learn from.
CREATE TABLE agent_questions (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id      uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    asked_by    text,
    question    text NOT NULL,
    context     jsonb NOT NULL DEFAULT '{}',   -- the page, date range and selection it was asked from
    intent      text,                          -- the analysis the router chose, if it was sure
    confidence  numeric(3, 2),
    analysis    text,                          -- the analysis that actually ran
    slots       jsonb NOT NULL DEFAULT '{}',
    status      text NOT NULL DEFAULT 'answered' CHECK (status IN ('answered', 'thinking', 'failed', 'refused')),
    answer      text NOT NULL DEFAULT '',
    bundle      jsonb NOT NULL DEFAULT '{}',   -- the evidence every sentence came from
    used_model  boolean NOT NULL DEFAULT false,
    problems    jsonb NOT NULL DEFAULT '[]',   -- figures a model wrote that the evidence did not have
    took_ms     integer,
    created_at  timestamptz NOT NULL DEFAULT now(),
    answered_at timestamptz
);
CREATE INDEX agent_questions_org_time ON agent_questions (org_id, created_at DESC);

-- +goose StatementBegin
DO $$
BEGIN
    EXECUTE 'ALTER TABLE agent_questions ENABLE ROW LEVEL SECURITY';
    EXECUTE 'ALTER TABLE agent_questions FORCE ROW LEVEL SECURITY';
    EXECUTE 'CREATE POLICY system_access ON agent_questions TO vellatry_system USING (true) WITH CHECK (true)';
    EXECUTE 'CREATE POLICY tenant_isolation ON agent_questions TO vellatry_tenant USING (org_id = app_org_id()) WITH CHECK (org_id = app_org_id())';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON agent_questions TO vellatry_tenant, vellatry_system';
END
$$;
-- +goose StatementEnd

-- +goose Down
DROP TABLE agent_questions;
