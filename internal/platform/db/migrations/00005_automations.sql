-- +goose Up

-- Places Vellatry sends things to. Outbound only: nothing here is a bot surface.
CREATE TABLE destinations (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id        uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    kind          text NOT NULL CHECK (kind IN ('slack', 'email')),
    name          text NOT NULL,              -- "#marketing", "Marketing team"
    config        jsonb NOT NULL DEFAULT '{}', -- email: {"to": [...]}; never a secret
    secret        bytea,                      -- slack: the sealed incoming-webhook URL
    digest        boolean NOT NULL DEFAULT true, -- receives the weekly digest
    status        text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused', 'broken')),
    status_detail text,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX destinations_org ON destinations (org_id);

-- Rules evaluated by code when a job finishes. The agent may propose one; a person
-- confirms it.
CREATE TABLE watchers (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id          uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    kind            text NOT NULL CHECK (kind IN ('visibility_drop', 'competitor_overtakes', 'critical_finding',
                                                  'connection_broken', 'blindspot_confirmed')),
    params          jsonb NOT NULL DEFAULT '{}',
    delivery        text NOT NULL DEFAULT 'immediate' CHECK (delivery IN ('immediate', 'digest')),
    destination_ids uuid[] NOT NULL DEFAULT '{}', -- empty: every active destination
    enabled         boolean NOT NULL DEFAULT true,
    source          text NOT NULL DEFAULT 'user' CHECK (source IN ('default', 'user', 'agent')),
    created_by      text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX watchers_org_kind ON watchers (org_id, kind) WHERE enabled;

-- What a watcher (or the digest, or a report) had to say. A repeat of the same thing
-- merges into its row (occurrences + 1) instead of alerting again.
CREATE TABLE notifications (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id       uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    watcher_id   uuid REFERENCES watchers (id) ON DELETE SET NULL,
    kind         text NOT NULL,
    dedupe_key   text NOT NULL,
    severity     text NOT NULL DEFAULT 'info' CHECK (severity IN ('critical', 'warning', 'info')),
    title        text NOT NULL,
    body         text NOT NULL DEFAULT '',
    link         text,                          -- dashboard path, e.g. /site?finding=12
    data         jsonb NOT NULL DEFAULT '{}',   -- structured content for the renderers
    delivery     text NOT NULL CHECK (delivery IN ('immediate', 'digest')),
    destination_ids uuid[] NOT NULL DEFAULT '{}',
    occurrences  integer NOT NULL DEFAULT 1,
    status       text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sent', 'partial', 'failed', 'digested', 'in_app')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    sent_at      timestamptz,
    UNIQUE (org_id, dedupe_key)
);
CREATE INDEX notifications_org_created ON notifications (org_id, created_at DESC);
CREATE INDEX notifications_digest ON notifications (org_id) WHERE delivery = 'digest' AND status = 'pending';

-- One row per notification per destination, so a retried job never sends twice to a
-- destination that already accepted it.
CREATE TABLE deliveries (
    notification_id bigint NOT NULL REFERENCES notifications (id) ON DELETE CASCADE,
    destination_id  uuid NOT NULL REFERENCES destinations (id) ON DELETE CASCADE,
    org_id          uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    status          text NOT NULL CHECK (status IN ('sent', 'failed')),
    error           text,
    attempted_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (notification_id, destination_id)
);

-- A Vellatry item (fix or blindspot) sent to a task tool. Closed automatically when the
-- item is resolved.
CREATE TABLE task_links (
    org_id       uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    source       text NOT NULL CHECK (source IN ('fix', 'blindspot')),
    subject_id   text NOT NULL,
    provider     text NOT NULL DEFAULT 'asana' CHECK (provider IN ('asana')),
    external_id  text,                      -- NULL while the task is being created
    url          text,
    status       text NOT NULL DEFAULT 'creating' CHECK (status IN ('creating', 'open', 'completed', 'failed')),
    error        text,
    requested_by text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    PRIMARY KEY (org_id, source, subject_id)
);

-- +goose StatementBegin
DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['destinations', 'watchers', 'notifications', 'deliveries', 'task_links']
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
DROP TABLE task_links, deliveries, notifications, watchers, destinations;
