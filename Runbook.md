# migrate-hub — Runbook

## Requirements
- Go 1.22+
- Tool DB: Postgres 14+
- Target DBs: Postgres and/or MySQL reachable from the service
- Google OAuth2/OIDC credentials for SSO

## Configuration (env vars)
### Core
- `MIGRATEHUB_DB_DSN` : Postgres DSN for tool storage (required)
- `MIGRATEHUB_HTTP_ADDR` : e.g. `:8080` (default `:8080`)
- `MIGRATEHUB_SECRET_KEY` : 32+ bytes base64 (required) used for:
  - session signing
  - encrypting stored db target passwords (AES-GCM)

### Google SSO (OIDC)
- `MIGRATEHUB_OIDC_GOOGLE_CLIENT_ID` : OAuth2 client id (required)
- `MIGRATEHUB_OIDC_GOOGLE_CLIENT_SECRET` : OAuth2 client secret (required)
- `MIGRATEHUB_OIDC_GOOGLE_REDIRECT_URL` : e.g. `http://localhost:8080/api/v1/auth/google/callback` (required)
- `MIGRATEHUB_OIDC_ALLOWED_DOMAINS` : comma-separated domains (optional), e.g. `ajaib.co.id`
- `MIGRATEHUB_OIDC_AUTO_PROVISION` : `true|false` (default `false`)
  - if `true`, first-time Google users are created with role `user`
  - if `false`, user must be pre-created by admin (matched by email or google_sub policy)

### Bootstrap admin (if using local passwords OR for initial setup)
- `MIGRATEHUB_ADMIN_EMAIL` : initial admin email (optional but recommended)
- `MIGRATEHUB_ADMIN_PASSWORD` : initial admin password (optional; only used if local login enabled)

Optional:
- `MIGRATEHUB_LOG_LEVEL` : `debug|info|warn|error`

## Bootstrapping
1. Create tool DB database.
2. Run tool DB migrations from `/migrations`.
3. Start server.
4. Configure Google OAuth consent + redirect URL to match `MIGRATEHUB_OIDC_GOOGLE_REDIRECT_URL`.
5. Login with Google SSO.
6. If `AUTO_PROVISION=false`, create user records as admin before allowing logins.

## Operational Procedures
### Add a DB Set/Target
- Create DB Set for env + project.
- Add one or more DB Targets, test connection.
- Ensure service network access to target DB.

### Create and run a migration (stg)
1. Create migration (draft).
2. Request approval for env `stg` + db_set.
3. Manager approves.
4. User executes approved run.
5. Verify target DB has `migrate_hub_migrations` (or configured name) record.

### Promote to production
1. Request approval for env `prd`.
2. Manager approves (or Admin if policy).
3. Execute prod run.
4. Monitor run items for failures.

### Rollback
1. Request rollback run for env + db_set.
2. Manager approves rollback.
3. Execute rollback run.
4. Confirm target migrations table reflects rollback policy (either:
   - insert a rollback record, or
   - mark applied entry as rolled_back_at; v1 pick one and document)

## Troubleshooting
### SSO login fails (common)
- Verify redirect URL exactly matches what you configured in Google console.
- Ensure cookies are allowed and `SameSite` works with your domain scheme.
- Check logs for issuer/audience mismatch:
  - `aud` must equal `MIGRATEHUB_OIDC_GOOGLE_CLIENT_ID`

### Run stuck in running
- Check server logs for the run_id.
- Check DB target lock:
  - Postgres: check blocking sessions / advisory locks
  - MySQL: check `GET_LOCK` holders
- If safe, cancel run (if implemented) or restart worker after marking run canceled.

### Migration fails with “already applied with different checksum”
- Someone applied a migration out-of-band or edited SQL after apply.
- Resolution:
  - inspect target DB migrations table
  - decide whether to create a new migration key to reconcile state

### Connection test failing
- Verify host/port connectivity from the service
- Verify credentials and permissions
- For Postgres: require permission to create table `migrate_hub_migrations` if absent

## Security Notes
- Rotate `MIGRATEHUB_SECRET_KEY` only with a planned procedure (may invalidate sessions and decrypt).
- Ensure logs do not contain SQL secrets or DB passwords.
- Restrict prod access by network and role.

## Backups
- Backup tool DB (Postgres) daily.
- For prod migrations, ensure you have DB backup/snapshot policy before execution.
