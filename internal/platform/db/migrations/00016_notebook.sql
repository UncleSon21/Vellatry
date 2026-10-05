-- +goose Up

-- The notebook: a marketing team gives Vellatry the documents it already works from —
-- a brand guide, a competitor's pricing page, a transcript, a strategy deck's notes —
-- and asks questions of them. Everything the answer says comes from those documents and
-- cites the passage it came from.
--
-- It is deliberately not the agent. The agent answers from Vellatry's own measured data
-- with numbers computed by code; the notebook answers from text somebody pasted in, and
-- its guarantee is traceability rather than arithmetic.
CREATE TABLE notebooks (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id     uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    name       text NOT NULL,
    created_by text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX notebooks_org ON notebooks (org_id, created_at DESC);

-- One thing the team put in: a page we fetched, or text they pasted or uploaded.
--
-- body is the extracted text, kept beside the source so re-chunking never refetches a
-- page that may have changed since — the answer has to stay traceable to what was read.
-- It is a few kilobytes to a few hundred per source, so it lives in Postgres for the
-- same reason report PDFs do: adding object storage for this size buys nothing.
CREATE TABLE notebook_sources (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    notebook_id uuid NOT NULL REFERENCES notebooks (id) ON DELETE CASCADE,
    kind        text NOT NULL CHECK (kind IN ('url', 'text', 'file')),
    title       text NOT NULL,
    url         text,                           -- kind = url: where it was read from
    body        text NOT NULL DEFAULT '',
    bytes       integer NOT NULL DEFAULT 0,
    chunks      integer NOT NULL DEFAULT 0,
    -- pending -> ready | failed. A failed source says why, in the team's words.
    status      text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'ready', 'failed')),
    error       text,
    added_by    text,
    added_at    timestamptz NOT NULL DEFAULT now(),
    ready_at    timestamptz
);
CREATE INDEX notebook_sources_notebook ON notebook_sources (notebook_id, added_at);

-- A passage of one source, which is what an answer cites. seq is its position in the
-- source, so a citation can say where in the document it came from.
--
-- The vector lives here rather than in `embeddings` on purpose: a chunk's vector is part
-- of the chunk, so it is deleted with it and never needs a sweep, and retrieval is
-- scoped to one notebook, which `embeddings` (keyed by kind and subject) cannot express.
-- model travels with the vector for the same reason it does there: vectors from two
-- models are not comparable.
--
-- tsv is the other half of retrieval. Meaning-matching alone misses the exact words a
-- marketing team searches by — a product name, a competitor, a figure — and full text
-- alone misses the question asked in other words. The two are fused in Go.
CREATE TABLE notebook_chunks (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id      uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    notebook_id uuid NOT NULL REFERENCES notebooks (id) ON DELETE CASCADE,
    source_id   uuid NOT NULL REFERENCES notebook_sources (id) ON DELETE CASCADE,
    seq         integer NOT NULL,
    text        text NOT NULL,
    tsv         tsvector GENERATED ALWAYS AS (to_tsvector('english', text)) STORED,
    vector      real[],
    embed_model text,
    UNIQUE (org_id, source_id, seq)
);
CREATE INDEX notebook_chunks_tsv ON notebook_chunks USING gin (tsv);
CREATE INDEX notebook_chunks_notebook ON notebook_chunks (notebook_id);

-- A question and the answer it got, with the passages the answer actually used. Kept
-- because the notebook is a place of work, not a chat window: the team comes back to
-- what they asked last week and to what it was grounded in.
CREATE TABLE notebook_messages (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id      uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    notebook_id uuid NOT NULL REFERENCES notebooks (id) ON DELETE CASCADE,
    asked_by    text NOT NULL DEFAULT '',
    question    text NOT NULL,
    answer      text NOT NULL DEFAULT '',
    -- thinking -> answered | unanswerable (the sources do not hold it) | failed
    status      text NOT NULL DEFAULT 'thinking' CHECK (status IN ('thinking', 'answered', 'unanswerable', 'failed')),
    citations   jsonb NOT NULL DEFAULT '[]'::jsonb,
    dropped     integer NOT NULL DEFAULT 0,     -- sentences the grounding check removed
    asked_at    timestamptz NOT NULL DEFAULT now(),
    answered_at timestamptz
);
CREATE INDEX notebook_messages_notebook ON notebook_messages (notebook_id, asked_at);

-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['notebooks', 'notebook_sources', 'notebook_chunks', 'notebook_messages'] LOOP
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
DROP TABLE notebook_messages;
DROP TABLE notebook_chunks;
DROP TABLE notebook_sources;
DROP TABLE notebooks;
