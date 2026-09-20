-- +goose Up

-- The fail-closed promise did not hold. 00001 granted the connecting user membership in
-- vellatry_tenant and vellatry_system so that transactions can SET ROLE to them, but a
-- plain GRANT also carries inheritance, and PostgreSQL applies a policy to any role the
-- current user inherits. The owner therefore picked up system_access (USING true) and a
-- query that chose no role saw every tenant's rows, which is exactly what
-- TestTenantIsolation exists to catch.
--
-- Membership without inheritance keeps SET ROLE working and stops the policies applying
-- until a transaction actually picks a role. Needs PostgreSQL 16 or newer.
-- +goose StatementBegin
DO $$
BEGIN
    EXECUTE format('REVOKE vellatry_tenant, vellatry_system FROM %I', CURRENT_USER);
    EXECUTE format('GRANT vellatry_tenant, vellatry_system TO %I WITH INHERIT FALSE', CURRENT_USER);
END
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    EXECUTE format('REVOKE vellatry_tenant, vellatry_system FROM %I', CURRENT_USER);
    EXECUTE format('GRANT vellatry_tenant, vellatry_system TO %I', CURRENT_USER);
END
$$;
-- +goose StatementEnd
