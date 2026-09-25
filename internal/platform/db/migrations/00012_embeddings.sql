-- +goose Up

-- Vectors for short texts, so the product can tell when two of them mean the same
-- thing. Written by the worker from Vellatry's own embedding service (ml/embed).
--
-- Stored as real[] rather than a pgvector column: a tenant has tens to hundreds of
-- topics, so the comparison happens in Go over a few hundred rows and an approximate
-- index would buy nothing for the cost of an extension. Move to pgvector when a
-- tenant's corpus makes that scan slow.
--
-- model is part of every row because vectors from two models are not comparable;
-- text_hash is what tells a later run the text changed and the vector is stale.
CREATE TABLE embeddings (
    org_id     uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    kind       text NOT NULL CHECK (kind IN ('topic', 'query', 'prompt')),
    subject_id text NOT NULL,          -- a topic's id, or the text itself for a query
    model      text NOT NULL,
    text_hash  text NOT NULL,
    vector     real[] NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, kind, subject_id)
);

-- What the suggestion looks like once it is made: this topic appears to be one the team
-- already has. It is a suggestion for a person, never an automatic merge, so nothing
-- reads these columns to decide anything on its own.
ALTER TABLE topics
    ADD COLUMN similar_to uuid REFERENCES topics (id) ON DELETE SET NULL,
    ADD COLUMN similar_score real,
    ADD COLUMN similar_checked_at timestamptz;

-- +goose StatementBegin
DO $$
BEGIN
    EXECUTE 'ALTER TABLE embeddings ENABLE ROW LEVEL SECURITY';
    EXECUTE 'ALTER TABLE embeddings FORCE ROW LEVEL SECURITY';
    EXECUTE 'CREATE POLICY system_access ON embeddings TO vellatry_system USING (true) WITH CHECK (true)';
    EXECUTE 'CREATE POLICY tenant_isolation ON embeddings TO vellatry_tenant USING (org_id = app_org_id()) WITH CHECK (org_id = app_org_id())';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON embeddings TO vellatry_tenant, vellatry_system';
END
$$;
-- +goose StatementEnd

-- +goose Down
ALTER TABLE topics DROP COLUMN similar_to, DROP COLUMN similar_score, DROP COLUMN similar_checked_at;
DROP TABLE embeddings;
