-- +goose Up

-- A published report can be a notebook source. It is the one piece of Vellatry's own
-- data that can be, and the reason is the reason a notebook works at all: a citation has
-- to point at text that still says what it said.
--
-- A report version is frozen by construction (migration 0010's report_versions), so a
-- passage cited from one reads the same next year. Live rollups are not: a note quoting
-- "visibility is 42.5%" from a source read in August would be quoting a number that no
-- longer exists, with no version to point at. Current numbers are the agent's job, which
-- computes them fresh on every question; the notebook takes the agreed account of a
-- period, which is what a report is.
--
-- The version is pinned when the source is added. A revision is a different account of
-- the same period, so it is a different source, added deliberately.
ALTER TABLE notebook_sources
    DROP CONSTRAINT notebook_sources_kind_check,
    ADD CONSTRAINT notebook_sources_kind_check CHECK (kind IN ('url', 'text', 'file', 'report')),
    -- SET NULL rather than CASCADE: the text was copied into body when the source was
    -- read, so a withdrawn report leaves the notebook's passages and every note citing
    -- them exactly as they were. Only the link back is lost.
    ADD COLUMN report_id uuid REFERENCES reports (id) ON DELETE SET NULL,
    ADD COLUMN report_version integer;

-- +goose Down
ALTER TABLE notebook_sources
    DROP COLUMN report_version,
    DROP COLUMN report_id,
    DROP CONSTRAINT notebook_sources_kind_check,
    ADD CONSTRAINT notebook_sources_kind_check CHECK (kind IN ('url', 'text', 'file'));
