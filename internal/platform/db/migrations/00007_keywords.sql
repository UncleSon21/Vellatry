-- +goose Up

-- One pass of keyword research: seeds, expansion, competitor gap, search results,
-- clustering. Every step is code; nothing here calls an LLM.
CREATE TABLE keyword_runs (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id      uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    trigger     text NOT NULL CHECK (trigger IN ('manual', 'schedule', 'onboarding')),
    status      text NOT NULL DEFAULT 'collecting' CHECK (status IN ('collecting', 'clustering', 'done', 'failed')),
    started_at  timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    stats       jsonb NOT NULL DEFAULT '{}', -- seeds, ideas, competitor keywords, SERPs, clusters, cost
    error       text
);
CREATE INDEX keyword_runs_org ON keyword_runs (org_id, started_at DESC);

-- The living keyword set. One row per keyword per tenant, refreshed by each run.
CREATE TABLE keywords (
    org_id         uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    keyword        text NOT NULL,
    topic_id       uuid REFERENCES topics (id) ON DELETE SET NULL,
    source         text NOT NULL CHECK (source IN ('search_console', 'seed', 'idea', 'competitor', 'topic', 'upload')),
    status         text NOT NULL DEFAULT 'candidate' CHECK (status IN ('candidate', 'approved', 'rejected')),
    reject_reason  text,                       -- why code or a person rejected it: training data later
    search_volume  integer NOT NULL DEFAULT 0,
    cpc            numeric(10, 2),
    competition    numeric(4, 3),
    difficulty     integer,
    intent         text,
    monthly        jsonb NOT NULL DEFAULT '[]',
    serp_urls      text[] NOT NULL DEFAULT '{}', -- top ten organic results, in order
    serp_features  text[] NOT NULL DEFAULT '{}',
    serp_fetched_at timestamptz,
    competitors    jsonb NOT NULL DEFAULT '{}', -- domain -> position
    sc_clicks      bigint NOT NULL DEFAULT 0,   -- Search Console, the window the run used
    sc_impressions bigint NOT NULL DEFAULT 0,
    sc_position    double precision,
    first_seen     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    run_id         bigint REFERENCES keyword_runs (id) ON DELETE SET NULL,
    PRIMARY KEY (org_id, keyword)
);
CREATE INDEX keywords_org_topic ON keywords (org_id, topic_id);
CREATE INDEX keywords_org_status ON keywords (org_id, status, search_volume DESC);

-- One queued search-results task per keyword.
CREATE TABLE serp_tasks (
    id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id           uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    run_id           bigint NOT NULL REFERENCES keyword_runs (id) ON DELETE CASCADE,
    keyword          text NOT NULL,
    provider_task_id text UNIQUE,
    status           text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'done', 'failed')),
    cost_usd         numeric(12, 6) NOT NULL DEFAULT 0,
    error            text,
    created_at       timestamptz NOT NULL DEFAULT now(),
    completed_at     timestamptz
);
CREATE INDEX serp_tasks_open ON serp_tasks (run_id) WHERE status = 'queued';

-- Topics gain what keyword research works out about them. A clustered topic starts
-- 'proposed': the team approves it before Vellatry spends anything measuring it.
ALTER TABLE topics DROP CONSTRAINT topics_status_check;
ALTER TABLE topics ADD CONSTRAINT topics_status_check CHECK (status IN ('proposed', 'active', 'out_of_scope'));
ALTER TABLE topics ADD COLUMN intent text;
ALTER TABLE topics ADD COLUMN keyword_count integer NOT NULL DEFAULT 0;
ALTER TABLE topics ADD COLUMN page_url text;                 -- the page that should own this topic
ALTER TABLE topics ADD COLUMN page_source text CHECK (page_source IN ('search_console', 'ranking', 'match', 'manual'));
ALTER TABLE topics ADD COLUMN page_kind text;                -- home | product | category | article | other
ALTER TABLE topics ADD COLUMN opportunity jsonb NOT NULL DEFAULT '{}';  -- score with every component
ALTER TABLE topics ADD COLUMN issues jsonb NOT NULL DEFAULT '[]';       -- intent mismatch, cannibalisation, no page
ALTER TABLE topics ADD COLUMN run_id bigint REFERENCES keyword_runs (id) ON DELETE SET NULL;
ALTER TABLE topics ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now();
CREATE INDEX topics_org_status ON topics (org_id, status);

-- +goose StatementBegin
DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['keyword_runs', 'keywords', 'serp_tasks']
    LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
        EXECUTE format('CREATE POLICY system_access ON %I TO vellatry_system USING (true) WITH CHECK (true)', t);
        EXECUTE format('CREATE POLICY tenant_isolation ON %I TO vellatry_tenant USING (org_id = app_org_id()) WITH CHECK (org_id = app_org_id())', t);
        EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO vellatry_tenant, vellatry_system', t);
    END LOOP;
END
$$;
-- +goose StatementEnd

-- +goose Down
DROP TABLE serp_tasks, keywords;
DROP INDEX topics_org_status;
ALTER TABLE topics DROP COLUMN updated_at, DROP COLUMN run_id, DROP COLUMN issues, DROP COLUMN opportunity,
    DROP COLUMN page_kind, DROP COLUMN page_source, DROP COLUMN page_url, DROP COLUMN keyword_count, DROP COLUMN intent;
ALTER TABLE topics DROP CONSTRAINT topics_status_check;
ALTER TABLE topics ADD CONSTRAINT topics_status_check CHECK (status IN ('active', 'out_of_scope'));
DROP TABLE keyword_runs;
