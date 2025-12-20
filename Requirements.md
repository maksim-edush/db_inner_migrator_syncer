# migrate-hub — Requirements

## Goal
Provide a WebUI + API service that tracks, approves, and executes SQL migrations across multiple database targets (Postgres/MySQL) in multiple environments (daily/staging/production), with strict approval gates and auditable history.

## Non-goals
- This tool is not a general SQL client.
- No “ad-hoc query execution” outside the migration flow.
- No automatic schema diff generation (optional future).

## Key Concepts
- **Project**: logical grouping (recommended).
- **Environment**: `daily`, `stg`, `prd`.
- **DB Set**: named collection per env, e.g. `auth_stg`, `auth_prd`.
- **DB Target**: a single database connection inside a DB Set.
- **Migration**: a versioned record containing `sql_up` and optional `sql_down`.
- **Run**: an execution attempt of a migration in an environment.
- **Run Item**: execution of a run against a specific DB Target.

## Roles & Permissions
- **User**
  - Create DB Sets / DB Targets (unless restricted by Admin policy)
  - Create migrations (draft)
  - Request execution (daily/stg/prd depending on policy)
  - Execute approved daily/stg runs (policy-controlled)
  - Request rollback
  - View statuses
- **Manager**
  - All User abilities
  - Approve / deny execution for daily/stg/prd (policy-configurable)
  - View approvals queue and all pending requests
- **Admin**
  - All Manager abilities
  - Manage users and roles
  - Manage DB Sets/Targets globally (including disabling targets)
  - Configure policies (optional v1 minimal)

## Authentication (Hard Requirements)
### SSO via Google (OIDC)
- Support login using Google OAuth2/OpenID Connect for browser SSO.
- After successful login:
  - if a user record exists for the Google identity, sign in
  - otherwise auto-provision a user with default role `user` (configurable) OR require admin pre-provision (choose v1 policy and document it)
- Persist:
  - Google subject (`sub`) as stable identity key
  - email, name
- Enforce optional restrictions:
  - allowed email domains (e.g. `ajaib.co.id`)
  - allowed email list / deny list (optional)
- Use server-side sessions (cookie) for WebUI.
- Keep local password login as optional fallback (admin-only toggle).

## Workflow Rules (Hard Requirements)
### 1) Strict approval gate
- No run may execute in an environment unless that migration is approved for that environment.
- Approval is environment-specific: `stg` approval does not imply `prd` approval.

### 2) Immutable audit trail
- Every significant action writes an `audit_event` with actor, action, entity reference, and payload.
- Approvals/denials must be stored as records (not only a status field).

### 3) Checksum-based re-approval
- The tool stores `checksum_up` and `checksum_down` (sha256).
- If migration SQL changes, existing approvals become invalid and execution must be blocked until re-approved.
- SQL changes require increasing migration `version` (integer) and recalculating checksums.

### 4) Execution tracking per DB target
- A run can target multiple DB targets.
- Each target has independent status with logs, timestamps, and errors.

### 5) Concurrency & locking
- Prevent concurrent migrations on the same DB target.
- Use advisory locks:
  - Postgres: `pg_advisory_lock(...)`
  - MySQL: `GET_LOCK(...)`

### 6) Target DB “migrations” table
- Each target DB must contain a table (created if missing) that records applied migrations:
  - migration_key (PK), checksum_up, checksum_down, applied_at, applied_by, tool_run_id
- Execution must:
  - Check if migration_key already applied with same checksum -> mark run_item `skipped`
  - If applied with different checksum -> fail and require manual resolution (do not proceed)

### 7) Transaction mode
- Migration has `transaction_mode`:
  - `auto`: best effort (default)
  - `single_transaction`: wrap in tx; fail if not possible
  - `no_transaction`: execute as-is
- Store mode per migration and honor it on execution.

### 8) Rollback rules
- Rollback requires:
  - rollback SQL exists OR admin overrides policy (optional)
  - approval for the same environment is required (separate approval event)
- Rollback execution is tracked as a run type: `rollback`.

## Status Model
### Run status (overall)
- `queued`, `awaiting_approval`, `approved`, `denied`, `running`, `executed`, `failed`, `canceled`

### Run item status (per DB target)
- `queued`, `running`, `executed`, `skipped`, `failed`, `canceled`

### Migration derived state per env (computed)
- `draft` (no approvals requested)
- `awaiting_approve_<env>`
- `approved_<env>`
- `executed_<env>` (all targets executed or skipped)
- `rollbacked_<env>` (rollback run executed successfully)
- `needs_reapproval_<env>` (SQL changed since last approval)

Note: store actual state as events + denormalized columns for UI.

## User Stories
### User
1. Create new DB sets in different environments (auth_stg, auth_prd) and specify connection info.
2. Create migration with: key, name, jira, description, sql_up, sql_down (optional), transaction_mode.
3. View migrations list and details.
4. Request to start migration on daily/stg.
5. Execute daily/stg migrations after manager approval.
6. Rollback executed migration (uses sql_down).
7. If stg run success, request prd run.
8. See status of migration across environments and targets.

### Manager
1. All User capabilities.
2. See all migrations awaiting approval / queued for execution.
3. Approve/deny with comment.
4. Deny allows user to edit and resubmit (SQL change triggers reapproval).

### Admin
0. All Manager capabilities.
1. Manage users/roles.
2. Manage DB sets/targets (enable/disable).
3. (Optional) Configure policies (SoD, prod restrictions).

## UX Requirements (WebUI)
- Dashboard: pending approvals, running jobs, recent activity.
- Auth:
  - “Sign in with Google” button
  - logout
- DB inventory: DB sets and targets, connection test.
- Migrations:
  - list with filters (project/env/status/jira)
  - details view with SQL + checksum + history
  - request execution
- Runs:
  - run details: per-target status, logs
  - cancel run (optional)
- Admin:
  - user management

## API Requirements
- REST JSON API (v1).
- WebUI auth: Google OIDC + cookie sessions.
- CSRF protection (cookie sessions).
- Optional local password login (if enabled).

## Observability
- Structured logs with request id.
- Basic metrics: runs started/succeeded/failed, duration, per-db-target failure count.
- Auth logs: successful/failed login attempts (without sensitive tokens).

## Security
- Store DB credentials securely:
  - v1: encrypt secrets at rest using app key (env var) + AES-GCM.
  - Prefer external secret manager later.
- No plaintext secrets in logs or audit events.
- Validate OIDC tokens using provider JWKS and verify:
  - issuer, audience, nonce, state
- Enforce `Secure`, `HttpOnly`, `SameSite` cookies.

## Acceptance Criteria (v1)
- Google SSO login works end-to-end; user is created or matched correctly; role enforced.
- Create a DB target and successfully test connection.
- Create migration; request staging approval; manager approves; user executes; target DB has migrations record; UI shows executed.
- Edit migration SQL -> approvals invalidated; execution blocked until re-approved.
- Attempt to execute without approval -> blocked.
- Rollback with approval -> executed and recorded.
- All actions appear in audit log.
