# Database Migrations

This directory contains SQL migration files for the AgentOps database, managed by [golang-migrate](https://github.com/golang-migrate/migrate).

## Migration Files

### Phase 1 - Core Tables

1. **1_create_tools_table.up.sql** / **1_create_tools_table.down.sql**
   - Creates the `tools` table with columns for tool metadata
   - Includes indexes on `name` for performance

2. **2_create_agents_table.up.sql** / **2_create_agents_table.down.sql**
   - Creates the `agents` table with support for tool relationships
   - Includes indexes on `name` and `active_version`

## File Naming Convention

Migrations follow the standard golang-migrate naming scheme:

```
{version}_{name}.{direction}.sql
```

- `{version}`: Sequential number (1, 2, 3, etc.)
- `{name}`: Descriptive name for the migration
- `{direction}`: Either `up` (apply) or `down` (rollback)

## Running Migrations

### Automated (via Docker Compose)

Migrations run automatically when starting the container stack:

```bash
docker-compose up
```

The `db-migrate` service will:

1. Wait for PostgreSQL to be healthy
2. Apply all pending migrations
3. Complete successfully before the backend starts

### Manual Migration (Local Development)

Using the `migrate` CLI directly:

```bash
# Install migrate (macOS with Homebrew)
brew install golang-migrate

# Apply migrations
migrate -path ./migrations -database "postgres://agentops:agentops@localhost:5432/agentops?sslmode=disable" up

# Rollback one migration
migrate -path ./migrations -database "postgres://agentops:agentops@localhost:5432/agentops?sslmode=disable" down

# Check migration status
migrate -path ./migrations -database "postgres://agentops:agentops@localhost:5432/agentops?sslmode=disable" version
```

### Migration Status

Migrations are tracked in the `schema_migrations` table in PostgreSQL. You can query it to see applied migrations:

```sql
SELECT * FROM schema_migrations;
```

## Adding New Migrations

To add a new migration:

1. Create two new files with the next sequential version number:

   ```
   {next_version}_description.up.sql
   {next_version}_description.down.sql
   ```

2. Write your SQL changes in the `.up.sql` file
3. Write the corresponding rollback in the `.down.sql` file

Example:

```bash
# Create migration files
touch migrations/3_add_user_table.up.sql
touch migrations/3_add_user_table.down.sql
```

## Database Schema

### tools Table

```sql
id              UUID          Primary Key
name            TEXT          Not Null
description     TEXT
input_schema    JSONB
output_schema   JSONB
handler_type    TEXT          CHECK constraint: 'http', 'internal', or 'llm'
handler_config  JSONB
created_at      TIMESTAMPTZ   Default: now()
```

### agents Table

```sql
id                UUID          Primary Key
name              TEXT          Not Null
description       TEXT
system_prompt_id  TEXT
tool_ids          UUID[]        Array of tool IDs
model_config      JSONB
active_version    INT           Default: 1
created_at        TIMESTAMPTZ   Default: now()
```

## Troubleshooting

### Migration Already Applied

If you see errors about versions already applied, check the schema_migrations table and manually adjust if needed.

### Connection Issues

Ensure the database connection string is correct:

```
postgres://{user}:{password}@{host}:{port}/{database}?sslmode=disable
```

### Permission Errors

The database user needs permissions to:

- CREATE/DROP tables
- CREATE/DROP indexes
- Read/write to `schema_migrations` table
