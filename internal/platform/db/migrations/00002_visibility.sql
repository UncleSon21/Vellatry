-- +goose Up

-- Per-tenant settings the Visibility engine plans with.
CREATE TABLE org_settings (
    org_id              uuid PRIMARY KEY REFERENCES orgs (id) ON DELETE CASCADE,
    plan                text NOT NULL DEFAULT 'trial',
    daily_answer_budget integer NOT NULL DEFAULT 60 CHECK (daily_answer_budget >= 0),
    discover_share      numeric(3, 2) NOT NULL DEFAULT 0.60 CHECK (discover_share BETWEEN 0 AND 1),
    engines             text[] NOT NULL DEFAULT '{chatgpt,gemini,ai_overview}',
    location_code       integer NOT NULL DEFAULT 2036,
    language_code       text NOT NULL DEFAULT 'en',
    judge_enabled       boolean NOT NULL DEFAULT true,
    updated_at          timestamptz NOT NULL DEFAULT now()
);

-- Topics are the backbone: prompts, keywords, pages and blindspots hang off them.
CREATE TABLE topics (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id         uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    brand_id       uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    name           text NOT NULL,
    source         text NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'site', 'search_console', 'keywords', 'ai_only')),
    demand_monthly integer,               -- search volume when known
    status         text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'out_of_scope')),
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX topics_org_name ON topics (org_id, lower(name));

CREATE TABLE prompts (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id            uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    brand_id          uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    topic_id          uuid REFERENCES topics (id) ON DELETE SET NULL,
    text              text NOT NULL,
    source            text NOT NULL CHECK (source IN ('template', 'keyword', 'people_also_ask', 'search_console', 'fan_out', 'manual')),
    status            text NOT NULL DEFAULT 'candidate' CHECK (status IN ('candidate', 'active', 'tracked', 'rejected')),
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX prompts_org_text ON prompts (org_id, lower(text));
CREATE INDEX prompts_org_status ON prompts (org_id, status);

-- One paid DataForSEO task: one attempt of one prompt on one engine.
CREATE TABLE answer_tasks (
    id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id           uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    prompt_id        uuid NOT NULL REFERENCES prompts (id) ON DELETE CASCADE,
    engine           text NOT NULL,
    purpose          text NOT NULL CHECK (purpose IN ('screen', 'confirm', 'track', 'check')),
    round            integer NOT NULL,
    provider_task_id text UNIQUE,
    status           text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'queued', 'done', 'failed')),
    cost_usd         numeric(12, 6) NOT NULL DEFAULT 0,
    error            text,
    created_at       timestamptz NOT NULL DEFAULT now(),
    completed_at     timestamptz
);
CREATE INDEX answer_tasks_org_created ON answer_tasks (org_id, created_at);
CREATE INDEX answer_tasks_open ON answer_tasks (prompt_id, engine) WHERE status IN ('pending', 'queued');

CREATE TABLE answers (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id         uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    prompt_id      uuid NOT NULL REFERENCES prompts (id) ON DELETE CASCADE,
    task_id        bigint NOT NULL UNIQUE REFERENCES answer_tasks (id) ON DELETE CASCADE,
    engine         text NOT NULL,
    round          integer NOT NULL,
    collected_at   timestamptz NOT NULL DEFAULT now(),
    present        boolean NOT NULL,      -- false when the engine produced no AI answer
    text           text NOT NULL DEFAULT '',
    sources        jsonb NOT NULL DEFAULT '[]',
    fan_out        text[] NOT NULL DEFAULT '{}',
    brand_entities text[] NOT NULL DEFAULT '{}',
    model          text,
    method_version text NOT NULL
);
CREATE INDEX answers_cell ON answers (prompt_id, engine, round);

CREATE TABLE answer_signals (
    answer_id             bigint PRIMARY KEY REFERENCES answers (id) ON DELETE CASCADE,
    org_id                uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    brand_mentioned       boolean NOT NULL,
    brand_count           integer NOT NULL,
    brand_position        integer NOT NULL,
    brand_cited           boolean NOT NULL,
    competitors_mentioned text[] NOT NULL DEFAULT '{}',
    cited_competitors     text[] NOT NULL DEFAULT '{}',
    sources_count         integer NOT NULL,
    visibility_gap        boolean NOT NULL,
    displacement_gap      boolean NOT NULL,
    detector_version      text NOT NULL
);

-- Judged rubric labels, one row per dimension. Also the training data for the trained judge.
CREATE TABLE judgments (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id     uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    answer_id  bigint NOT NULL REFERENCES answers (id) ON DELETE CASCADE,
    dimension  text NOT NULL,
    label      integer,                  -- 1-5 for scaled dimensions
    score      numeric(4, 2),            -- 1-10 display score
    value      jsonb,                    -- non-scaled values (role, booleans, claims)
    evidence   text,
    judge      text NOT NULL,            -- model id or trained model version
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (answer_id, dimension, judge)
);

-- The state of one prompt on one engine for the current round.
CREATE TABLE cells (
    org_id           uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    prompt_id        uuid NOT NULL REFERENCES prompts (id) ON DELETE CASCADE,
    engine           text NOT NULL,
    round            integer NOT NULL DEFAULT 1,
    phase            text NOT NULL DEFAULT 'screening' CHECK (phase IN ('screening', 'confirming', 'settled')),
    answers          integer NOT NULL DEFAULT 0,
    present          integer NOT NULL DEFAULT 0,
    mentions         integer NOT NULL DEFAULT 0,
    displacements    integer NOT NULL DEFAULT 0,
    band             text,
    round_started_at timestamptz NOT NULL DEFAULT now(),
    last_answer_at   timestamptz,
    updated_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (prompt_id, engine)
);
CREATE INDEX cells_org_phase ON cells (org_id, phase);

CREATE TABLE blindspots (
    id                 bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id             uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    prompt_id          uuid NOT NULL REFERENCES prompts (id) ON DELETE CASCADE,
    engine             text NOT NULL,
    kind               text NOT NULL CHECK (kind IN ('visibility', 'displacement', 'sentiment', 'differentiator')),
    status             text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'resolved', 'dismissed')),
    confirmed          boolean NOT NULL DEFAULT false,
    winning_competitor text,
    evidence_answer_id bigint REFERENCES answers (id) ON DELETE SET NULL,
    first_seen         timestamptz NOT NULL DEFAULT now(),
    last_seen          timestamptz NOT NULL DEFAULT now(),
    UNIQUE (prompt_id, engine, kind)
);
CREATE INDEX blindspots_org_status ON blindspots (org_id, status);

-- Daily rollups for Performance, updated incrementally as each answer arrives.
CREATE TABLE visibility_daily (
    org_id         uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    day            date NOT NULL,
    engine         text NOT NULL,
    answers        integer NOT NULL DEFAULT 0,
    present        integer NOT NULL DEFAULT 0,
    mentioned      integer NOT NULL DEFAULT 0,
    position_sum   integer NOT NULL DEFAULT 0,
    position_n     integer NOT NULL DEFAULT 0,
    sentiment_sum  numeric NOT NULL DEFAULT 0,
    sentiment_n    integer NOT NULL DEFAULT 0,
    method_version text NOT NULL,
    PRIMARY KEY (org_id, day, engine)
);

-- Mentions per tracked entity (brand and each competitor), for share of voice.
CREATE TABLE visibility_daily_entities (
    org_id   uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    day      date NOT NULL,
    engine   text NOT NULL,
    entity   text NOT NULL,
    is_brand boolean NOT NULL,
    answers  integer NOT NULL DEFAULT 0, -- answers mentioning the entity
    mentions integer NOT NULL DEFAULT 0,
    PRIMARY KEY (org_id, day, engine, entity)
);

CREATE TABLE source_citations_daily (
    org_id    uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    day       date NOT NULL,
    engine    text NOT NULL,
    domain    text NOT NULL,
    citations integer NOT NULL DEFAULT 0,
    PRIMARY KEY (org_id, day, engine, domain)
);

-- LLM gateway response cache, per tenant.
CREATE TABLE llm_cache (
    org_id     uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    key        text NOT NULL,
    purpose    text NOT NULL,
    model      text NOT NULL,
    response   jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, key)
);

-- Row-level security, same pattern as 00001.
-- +goose StatementBegin
DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['org_settings', 'topics', 'prompts', 'answer_tasks', 'answers', 'answer_signals',
                             'judgments', 'cells', 'blindspots', 'visibility_daily', 'visibility_daily_entities',
                             'source_citations_daily', 'llm_cache']
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

-- Real-time: every event is announced on a channel the api role listens to, so the
-- dashboard updates over SSE without polling. The payload carries ids only.
-- +goose StatementBegin
CREATE FUNCTION notify_event() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    PERFORM pg_notify('vellatry_events', json_build_object('org_id', NEW.org_id, 'id', NEW.id, 'kind', NEW.kind)::text);
    RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER events_notify AFTER INSERT ON events FOR EACH ROW EXECUTE FUNCTION notify_event();

-- +goose Down
DROP TRIGGER events_notify ON events;
DROP FUNCTION notify_event();
DROP TABLE llm_cache, source_citations_daily, visibility_daily_entities, visibility_daily, blindspots, cells,
    judgments, answer_signals, answers, answer_tasks, prompts, topics, org_settings;
