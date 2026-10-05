-- +goose Up

-- Where a topic went when the team folded it into one they already track. The row stays:
-- it is the record that this wording was seen and decided on, and it stops the same
-- topic being proposed again as though it were new.
ALTER TABLE topics ADD COLUMN merged_into uuid REFERENCES topics (id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE topics DROP COLUMN merged_into;
