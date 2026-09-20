-- +goose Up

-- A provider's task id was unique across the whole table, which is a constraint that
-- spans tenants. Under row-level security the colliding row belongs to someone else and
-- is invisible, so an insert with ON CONFLICT DO NOTHING quietly did nothing and the
-- task was lost, while a plain insert would have failed with an error about a row the
-- tenant cannot see. Uniqueness belongs inside the tenant.
ALTER TABLE answer_tasks DROP CONSTRAINT answer_tasks_provider_task_id_key;
ALTER TABLE answer_tasks ADD CONSTRAINT answer_tasks_org_provider_task UNIQUE (org_id, provider_task_id);

ALTER TABLE serp_tasks DROP CONSTRAINT serp_tasks_provider_task_id_key;
ALTER TABLE serp_tasks ADD CONSTRAINT serp_tasks_org_provider_task UNIQUE (org_id, provider_task_id);

-- +goose Down
ALTER TABLE serp_tasks DROP CONSTRAINT serp_tasks_org_provider_task;
ALTER TABLE serp_tasks ADD CONSTRAINT serp_tasks_provider_task_id_key UNIQUE (provider_task_id);
ALTER TABLE answer_tasks DROP CONSTRAINT answer_tasks_org_provider_task;
ALTER TABLE answer_tasks ADD CONSTRAINT answer_tasks_provider_task_id_key UNIQUE (provider_task_id);
