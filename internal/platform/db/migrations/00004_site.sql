-- +goose Up

CREATE TABLE crawls (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id      uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    trigger     text NOT NULL CHECK (trigger IN ('schedule', 'manual', 'onboarding')),
    status      text NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'done', 'failed')),
    started_at  timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    pages       integer NOT NULL DEFAULT 0,
    summary     jsonb NOT NULL DEFAULT '{}', -- AI access, robots, llms.txt, sitemaps, counts
    error       text
);
CREATE INDEX crawls_org_started ON crawls (org_id, started_at DESC);

-- The latest known state of each page: feeds the audit, page mapping for topics, and
-- fix detection.
CREATE TABLE pages (
    org_id           uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    url              text NOT NULL,
    final_url        text NOT NULL,
    status           integer NOT NULL,
    content_type     text,
    title            text,
    meta_description text,
    h1               text,
    canonical        text,
    noindex          boolean NOT NULL DEFAULT false,
    word_count       integer NOT NULL DEFAULT 0,
    json_ld_types    text[] NOT NULL DEFAULT '{}',
    content_hash     text,
    last_crawl_id    bigint REFERENCES crawls (id) ON DELETE SET NULL,
    last_crawled_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, url)
);

CREATE TABLE audit_findings (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id      uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    fingerprint text NOT NULL,
    rule        text NOT NULL,
    severity    text NOT NULL CHECK (severity IN ('critical', 'warning', 'info')),
    url         text,
    message     text NOT NULL,
    detail      jsonb NOT NULL DEFAULT '{}',
    status      text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'resolved', 'dismissed')),
    first_seen  timestamptz NOT NULL DEFAULT now(),
    last_seen   timestamptz NOT NULL DEFAULT now(),
    resolved_at timestamptz,
    crawl_id    bigint REFERENCES crawls (id) ON DELETE SET NULL,
    UNIQUE (org_id, fingerprint)
);
CREATE INDEX audit_findings_org_status ON audit_findings (org_id, status, severity);

-- Every recommended change, whatever produced it, with its delivery route and outcome:
-- proposed -> sent -> live (detected on the next crawl) -> measured.
CREATE TABLE fixes (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id       uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    source       text NOT NULL CHECK (source IN ('audit', 'blindspot')),
    fingerprint  text NOT NULL,
    title        text NOT NULL,
    instructions text NOT NULL,
    snippet      text,
    snippet_lang text,
    page_url     text,
    severity     text,
    status       text NOT NULL DEFAULT 'proposed' CHECK (status IN ('proposed', 'sent', 'live', 'measured', 'dismissed')),
    route        text NOT NULL DEFAULT 'instructions' CHECK (route IN ('instructions', 'asana', 'cms', 'github')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    sent_at      timestamptz,
    live_at      timestamptz,
    measured_at  timestamptz,
    outcome      jsonb,
    UNIQUE (org_id, source, fingerprint)
);
CREATE INDEX fixes_org_status ON fixes (org_id, status);

-- +goose StatementBegin
DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['crawls', 'pages', 'audit_findings', 'fixes']
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
DROP TABLE fixes, audit_findings, pages, crawls;
