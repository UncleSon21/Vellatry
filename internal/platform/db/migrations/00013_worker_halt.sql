-- +goose Up

-- One row, or none: the worker is halted or it is not. A halt means one of Vellatry's
-- own accounts with a paid service cannot be used (no credit, a rejected key), so every
-- job would fail the same way. Jobs stay queued while it is set.
--
-- The id column exists only to allow one row: a second halt would hide the first
-- reason, which is the one that explains the outage.
CREATE TABLE worker_halt (
    id        boolean PRIMARY KEY DEFAULT TRUE CHECK (id),
    service   text NOT NULL,
    reason    text NOT NULL,
    halted_at timestamptz NOT NULL DEFAULT now()
);

-- Not a tenant table: it belongs to Vellatry's own accounts, and only the worker and
-- the resume command touch it. No tenant role is granted anything.
GRANT SELECT, INSERT, UPDATE, DELETE ON worker_halt TO vellatry_system;

-- +goose Down
DROP TABLE worker_halt;
