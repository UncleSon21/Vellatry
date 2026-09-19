-- +goose Up

-- A report series: what goes in, for which period, to whom.
CREATE TABLE report_series (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id         uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    name           text NOT NULL,
    period         text NOT NULL CHECK (period IN ('month', 'fy_quarter', 'fy', 'custom')),
    sections       text[] NOT NULL DEFAULT '{visibility,blindspots,search,ai_referrals,traffic,site,fixes}',
    recipients     text[] NOT NULL DEFAULT '{}',   -- told by email (a link, never an attachment) when a report is published
    auto_draft     boolean NOT NULL DEFAULT true,  -- draft each period automatically
    draft_lag_days integer NOT NULL DEFAULT 4 CHECK (draft_lag_days BETWEEN 0 AND 28), -- Search Console settles in 2-3 days
    accent         text NOT NULL DEFAULT '#1d4ed8' CHECK (accent ~ '^#[0-9a-fA-F]{6}$'),
    archived       boolean NOT NULL DEFAULT false,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX report_series_org ON report_series (org_id);

-- One report per series and period. The snapshot is frozen when drafted; the team
-- writes the summary and section notes; publishing copies all of it into a version.
CREATE TABLE reports (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id        uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    series_id     uuid NOT NULL REFERENCES report_series (id) ON DELETE CASCADE,
    period_start  date NOT NULL,
    period_end    date NOT NULL,
    status        text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'published', 'withdrawn')),
    title         text NOT NULL,
    snapshot      jsonb NOT NULL,
    summary       text NOT NULL DEFAULT '',
    summary_draft text,                         -- the model's suggestion, shown beside the editor; never published as is
    notes         jsonb NOT NULL DEFAULT '{}',  -- section key -> the team's note
    version       integer NOT NULL DEFAULT 0,   -- latest published version
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    published_at  timestamptz,
    UNIQUE (series_id, period_start, period_end),
    CHECK (period_end >= period_start)
);
CREATE INDEX reports_org_status ON reports (org_id, status, period_end DESC);

-- Published versions are immutable: a revision is a new version, and every one stays
-- readable in the hub's history.
CREATE TABLE report_versions (
    report_id    uuid NOT NULL REFERENCES reports (id) ON DELETE CASCADE,
    org_id       uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    version      integer NOT NULL CHECK (version > 0),
    title        text NOT NULL,
    snapshot     jsonb NOT NULL,
    summary      text NOT NULL,
    notes        jsonb NOT NULL,
    accent       text NOT NULL,
    published_at timestamptz NOT NULL DEFAULT now(),
    published_by text,
    pdf          bytea,
    pdf_status   text NOT NULL DEFAULT 'pending' CHECK (pdf_status IN ('pending', 'ready', 'failed', 'unavailable')),
    notified_at  timestamptz,                   -- recipients were emailed; never twice for one version
    PRIMARY KEY (report_id, version)
);

-- The CMO's reports hub: one per organisation, reached by an unguessable slug and
-- opened with an emailed sign-in link at an allowed domain.
CREATE TABLE hubs (
    org_id          uuid PRIMARY KEY REFERENCES orgs (id) ON DELETE CASCADE,
    slug            text NOT NULL UNIQUE,
    allowed_domains text[] NOT NULL DEFAULT '{}', -- besides the brand's own domain
    enabled         boolean NOT NULL DEFAULT true,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

-- Sign-in links. Only a hash is stored; the worker emails the token and never keeps it.
CREATE TABLE hub_logins (
    token_hash text PRIMARY KEY,
    org_id     uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    email      text NOT NULL,
    expires_at timestamptz NOT NULL,
    used_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX hub_logins_email ON hub_logins (org_id, email, created_at);

CREATE TABLE hub_sessions (
    token_hash   text PRIMARY KEY,
    org_id       uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    email        text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL DEFAULT now()
);

-- Every view is logged.
CREATE TABLE report_views (
    id        bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id    uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    report_id uuid NOT NULL REFERENCES reports (id) ON DELETE CASCADE,
    version   integer NOT NULL,
    viewer    text NOT NULL,
    format    text NOT NULL CHECK (format IN ('web', 'pdf')),
    viewed_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX report_views_report ON report_views (report_id, viewed_at DESC);

-- +goose StatementBegin
DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['report_series', 'reports', 'report_versions', 'hubs', 'hub_logins', 'hub_sessions', 'report_views']
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
DROP TABLE report_views, hub_sessions, hub_logins, hubs, report_versions, reports, report_series;
