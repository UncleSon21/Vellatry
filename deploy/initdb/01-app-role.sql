-- Runs once when the container's data volume is first created.
-- vellatry_app owns the databases but is not a superuser, so row-level security applies.
-- CREATEROLE lets migrations create the vellatry_tenant and vellatry_system roles.
CREATE ROLE vellatry_app LOGIN PASSWORD 'vellatry' CREATEROLE;
CREATE DATABASE vellatry OWNER vellatry_app;
CREATE DATABASE vellatry_test OWNER vellatry_app;
