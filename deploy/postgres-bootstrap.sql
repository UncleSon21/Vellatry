-- Run once on a new managed Postgres (Neon, or any Postgres 16+), connected as the admin
-- role the provider gives you (on Neon: neondb_owner). Local and CI databases use
-- deploy/initdb/01-app-role.sql instead.
--
--   psql "<admin connection string>" -v ON_ERROR_STOP=1 -v app_password="<password>" -f deploy/postgres-bootstrap.sql
--
-- vellatry_app owns the database and runs the migrations, which is why it needs
-- CREATEROLE (they create vellatry_tenant and vellatry_system). It is not a superuser
-- and does not have BYPASSRLS, so row-level security applies to it. The app checks
-- this on every start and refuses a user that could see across tenants.
--
-- In a web SQL editor, which cannot take -v variables, replace :'app_password' with the
-- password in single quotes and run the two statements one at a time.

CREATE ROLE vellatry_app LOGIN CREATEROLE NOSUPERUSER NOBYPASSRLS PASSWORD :'app_password';
CREATE DATABASE vellatry OWNER vellatry_app;
