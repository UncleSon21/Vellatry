-- +goose Up

-- What the work leaves behind. A notebook is a place of work, and the two things worth
-- keeping out of a session are what you found and what you asked to find it.

-- A note is what you found. One saved from an answer keeps that answer's words and its
-- citations as they were: a source can be deleted or re-read later, and the note has to
-- stay a record of what was said and what it was said from, which is exactly why the
-- passage text is copied in rather than pointed at.
--
-- Notes are outputs, never inputs. Nothing retrieves them, because an answer grounded in
-- a note grounded in an answer is how a model's words launder themselves into a cited
-- fact. A team who wants a note to be a source pastes it in as one, deliberately, and it
-- appears in the sources panel where it belongs.
CREATE TABLE notebook_notes (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id       uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    notebook_id  uuid NOT NULL REFERENCES notebooks (id) ON DELETE CASCADE,
    -- answer: kept from what the notebook said, and its body never changes.
    -- written: the team's own words, theirs to edit.
    kind         text NOT NULL CHECK (kind IN ('answer', 'written')),
    title        text NOT NULL,
    body         text NOT NULL,
    citations    jsonb NOT NULL DEFAULT '[]'::jsonb,
    from_message bigint REFERENCES notebook_messages (id) ON DELETE SET NULL,
    saved_by     text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX notebook_notes_notebook ON notebook_notes (notebook_id, created_at DESC);

-- A recipe is a question worth asking again: of this notebook next month, or of the
-- notebook you make for the next competitor. It belongs to the organisation rather than
-- to one notebook, because a question that only ever suits one set of documents is a
-- question, not a recipe.
--
-- runs and last_run_at are the only measure of whether a recipe earns its place, and
-- they are counted, not judged.
CREATE TABLE notebook_recipes (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id      uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    name        text NOT NULL,
    question    text NOT NULL,
    saved_by    text NOT NULL DEFAULT '',
    runs        integer NOT NULL DEFAULT 0,
    last_run_at timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now(),
    -- Saving the same question twice is the same recipe, not a second one.
    UNIQUE (org_id, question)
);

-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['notebook_notes', 'notebook_recipes'] LOOP
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
DROP TABLE notebook_recipes;
DROP TABLE notebook_notes;
