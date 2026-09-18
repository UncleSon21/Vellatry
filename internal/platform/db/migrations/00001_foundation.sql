-- +goose Up

-- Every query runs under one of two roles, chosen per transaction with SET LOCAL ROLE:
--   vellatry_tenant  sees only rows of the org in app.org_id
--   vellatry_system  cross-tenant work (scheduler, admin), chosen explicitly
-- Row-level security is FORCEd, so the connecting owner itself matches no policy and
-- sees nothing: forgetting to pick a role fails closed.
-- Untrusted SQL (the future agent query tool) must use its own low-privilege LOGIN
-- role and connection, never this one, because the owner can always SET ROLE.
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'vellatry_tenant') THEN
        CREATE ROLE vellatry_tenant NOLOGIN;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'vellatry_system') THEN
        CREATE ROLE vellatry_system NOLOGIN;
    END IF;
END
$$;
-- +goose StatementEnd
GRANT vellatry_tenant, vellatry_system TO CURRENT_USER;
GRANT USAGE ON SCHEMA public TO vellatry_tenant, vellatry_system;

-- The org the current transaction acts for; NULL outside tenant work, which matches no row.
CREATE FUNCTION app_org_id() RETURNS uuid
    LANGUAGE sql STABLE
    AS $$ SELECT nullif(current_setting('app.org_id', true), '')::uuid $$;

CREATE TABLE orgs (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Users are global identities (one person may belong to several orgs later).
CREATE TABLE users (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    external_id text UNIQUE,          -- auth provider id (Clerk)
    email       text NOT NULL UNIQUE,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE memberships (
    org_id     uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role       text NOT NULL CHECK (role IN ('owner', 'editor', 'viewer')),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, user_id)
);

-- One brand per org in v1; agencies will hold many.
CREATE TABLE brands (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id          uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    name            text NOT NULL,
    domain          text NOT NULL,
    aliases         text[] NOT NULL DEFAULT '{}',
    exclusions      text[] NOT NULL DEFAULT '{}',
    differentiators text[] NOT NULL DEFAULT '{}',
    provenance      jsonb NOT NULL DEFAULT '{}', -- field -> 'user' | 'suggested'
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, domain)
);

CREATE TABLE competitors (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id     uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    brand_id   uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    name       text NOT NULL,
    aliases    text[] NOT NULL DEFAULT '{}',
    exclusions text[] NOT NULL DEFAULT '{}',
    domains    text[] NOT NULL DEFAULT '{}',
    source     text NOT NULL DEFAULT 'user' CHECK (source IN ('user', 'suggested')),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE connections (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id     uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    kind       text NOT NULL CHECK (kind IN ('search_console', 'ga4', 'slack', 'asana', 'email')),
    status     text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'connected', 'broken', 'revoked')),
    config     jsonb NOT NULL DEFAULT '{}',
    secret     bytea,                 -- encrypted at rest by the application
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, kind)
);

-- Every paid external call, attributed to a tenant.
CREATE TABLE usage_ledger (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id      uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    occurred_at timestamptz NOT NULL DEFAULT now(),
    provider    text NOT NULL,            -- 'dataforseo', 'anthropic', ...
    purpose     text NOT NULL,            -- gateway purpose or engine
    units       numeric NOT NULL DEFAULT 1,
    cost_usd    numeric(12, 6) NOT NULL DEFAULT 0,
    cached      boolean NOT NULL DEFAULT false,
    ref         text                      -- task id or request hash
);
CREATE INDEX usage_ledger_org_time ON usage_ledger (org_id, occurred_at);

-- Domain events: the change timeline, the audit log and the learning ledger in one
-- stream. Subscribers are dispatched through the job queue in the same transaction.
CREATE TABLE events (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id      uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    occurred_at timestamptz NOT NULL DEFAULT now(),
    kind        text NOT NULL,
    subject_id  text,
    actor       text,                     -- user id, 'system' or a job kind
    payload     jsonb NOT NULL DEFAULT '{}'
);
CREATE INDEX events_org_time ON events (org_id, occurred_at);
CREATE INDEX events_org_kind ON events (org_id, kind);

-- Row-level security: forced on every table, one policy per role.
-- +goose StatementBegin
DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['orgs', 'users', 'memberships', 'brands', 'competitors', 'connections', 'usage_ledger', 'events']
    LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
        EXECUTE format('CREATE POLICY system_access ON %I TO vellatry_system USING (true) WITH CHECK (true)', t);
        EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO vellatry_tenant, vellatry_system', t);
    END LOOP;
END
$$;
-- +goose StatementEnd

CREATE POLICY tenant_isolation ON orgs TO vellatry_tenant
    USING (id = app_org_id()) WITH CHECK (id = app_org_id());
-- A user is visible to a tenant only through a membership in that tenant.
CREATE POLICY tenant_members ON users TO vellatry_tenant
    USING (EXISTS (SELECT 1 FROM memberships m WHERE m.user_id = users.id AND m.org_id = app_org_id()));
CREATE POLICY tenant_isolation ON memberships TO vellatry_tenant
    USING (org_id = app_org_id()) WITH CHECK (org_id = app_org_id());
CREATE POLICY tenant_isolation ON brands TO vellatry_tenant
    USING (org_id = app_org_id()) WITH CHECK (org_id = app_org_id());
CREATE POLICY tenant_isolation ON competitors TO vellatry_tenant
    USING (org_id = app_org_id()) WITH CHECK (org_id = app_org_id());
CREATE POLICY tenant_isolation ON connections TO vellatry_tenant
    USING (org_id = app_org_id()) WITH CHECK (org_id = app_org_id());
CREATE POLICY tenant_isolation ON usage_ledger TO vellatry_tenant
    USING (org_id = app_org_id()) WITH CHECK (org_id = app_org_id());
CREATE POLICY tenant_isolation ON events TO vellatry_tenant
    USING (org_id = app_org_id()) WITH CHECK (org_id = app_org_id());

-- +goose Down
DROP TABLE events, usage_ledger, connections, competitors, brands, memberships, users, orgs;
DROP FUNCTION app_org_id();
