# Tenant Migrations

`canonical/` is the active source for provisioning tenant databases. The
organization service reads the directory configured by `TENANT_MIGRATION_DIR`;
in Docker this points to `/app/migrations/tenant/canonical`.

`legacy/` is historical reference only. Do not add new production migrations
there.

Conventions:

- Keep active migrations in `canonical/<microservice-or-domain>/`.
- The tenant migration runner reads `canonical/` recursively and orders files
  by the numeric prefix in the SQL filename, then by relative path.
- Keep each microservice/domain in its own folder, for example:
  `canonical/ecommerce/17_ecommerce.sql` and
  `canonical/ecommerce-builder/52_ecommerce_builder_templates.sql`.
- Name new files with the next global sequence number and the feature/domain,
  for example `53_ecommerce_builder_blocks.sql`.
- Prefer idempotent SQL: `CREATE TABLE IF NOT EXISTS`, `ALTER ... IF EXISTS`
  or `IF NOT EXISTS`, and `INSERT ... ON CONFLICT`.
- Seeds that are required for new tenants belong in `canonical/`, not in
  `legacy/`.
