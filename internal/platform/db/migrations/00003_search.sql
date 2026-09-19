-- +goose Up

-- One Google grant per org ('google', holding the sealed refresh token) serves both the
-- Search Console and GA4 connections, which hold only the chosen property.
ALTER TABLE connections DROP CONSTRAINT connections_kind_check;
ALTER TABLE connections ADD CONSTRAINT connections_kind_check
    CHECK (kind IN ('google', 'search_console', 'ga4', 'slack', 'asana', 'email'));
ALTER TABLE connections ADD COLUMN status_detail text;

-- Search Console totals per day, from a date-only query (exact, includes anonymised
-- queries). Query and page detail stays in BigQuery; only monthly top-N rollups land here.
CREATE TABLE search_daily (
    org_id       uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    day          date NOT NULL,
    clicks       bigint NOT NULL,
    impressions  bigint NOT NULL,
    position_sum double precision NOT NULL, -- sum(position x impressions), for weighted averages
    synced_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, day)
);

CREATE TABLE search_query_monthly (
    org_id       uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    month        date NOT NULL,             -- first day of the month
    query        text NOT NULL,
    clicks       bigint NOT NULL,
    impressions  bigint NOT NULL,
    position_sum double precision NOT NULL,
    PRIMARY KEY (org_id, month, query)
);

CREATE TABLE search_page_monthly (
    org_id       uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    month        date NOT NULL,
    page         text NOT NULL,
    clicks       bigint NOT NULL,
    impressions  bigint NOT NULL,
    position_sum double precision NOT NULL,
    PRIMARY KEY (org_id, month, page)
);

CREATE TABLE analytics_daily (
    org_id     uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    day        date NOT NULL,
    channel    text NOT NULL,
    sessions   bigint NOT NULL,
    users      bigint NOT NULL,
    key_events double precision NOT NULL,
    PRIMARY KEY (org_id, day, channel)
);

-- Sessions referred by AI assistants (chatgpt.com, perplexity.ai, gemini.google.com...).
CREATE TABLE ai_referral_daily (
    org_id     uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    day        date NOT NULL,
    assistant  text NOT NULL,
    sessions   bigint NOT NULL,
    users      bigint NOT NULL,
    key_events double precision NOT NULL,
    PRIMARY KEY (org_id, day, assistant)
);

CREATE TABLE analytics_landing_monthly (
    org_id     uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    month      date NOT NULL,
    page       text NOT NULL,
    sessions   bigint NOT NULL,
    key_events double precision NOT NULL,
    PRIMARY KEY (org_id, month, page)
);

CREATE TABLE sync_state (
    org_id             uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    kind               text NOT NULL CHECK (kind IN ('search_console', 'ga4')),
    backfill_requested timestamptz,
    last_synced_at     timestamptz,
    last_day           date,
    last_error         text,
    PRIMARY KEY (org_id, kind)
);

-- +goose StatementBegin
DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['search_daily', 'search_query_monthly', 'search_page_monthly', 'analytics_daily',
                             'ai_referral_daily', 'analytics_landing_monthly', 'sync_state']
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
DROP TABLE sync_state, analytics_landing_monthly, ai_referral_daily, analytics_daily,
    search_page_monthly, search_query_monthly, search_daily;
ALTER TABLE connections DROP COLUMN status_detail;
ALTER TABLE connections DROP CONSTRAINT connections_kind_check;
ALTER TABLE connections ADD CONSTRAINT connections_kind_check
    CHECK (kind IN ('search_console', 'ga4', 'slack', 'asana', 'email'));
