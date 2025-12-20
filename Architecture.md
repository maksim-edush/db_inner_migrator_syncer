# migrate-hub — Architecture

## Overview
migrate-hub is a Go service providing:
- REST API for migrations, approvals, execution runs
- WebUI (server-rendered templates recommended for v1)
- Tool storage database (Postgres)
- Execution engine that connects to target DBs (Postgres/MySQL)
- Google SSO (OAuth2/OIDC) with cookie sessions

## High-level Components
1. **HTTP API**
   - Auth (Google OIDC + sessions; optional password)
   - RBAC middleware
   - Input validation
2. **WebUI**
   - Uses server-rendered HTML templates (minimal JS)
3. **Storage Layer**
   - Postgres tool DB
   - sqlc-generated queries
4. **Execution Engine**
   - Creates run + run_items
   - For each DB target:
     - Acquire lock
     - Ensure target migrations table exists
     - Check applied state
     - Execute migration SQL according to transaction_mode
     - Record result in target migrations table
     - Persist logs/errors in tool DB
5. **Audit Logger**
   - Central function to write audit_events for each action

## Auth Flow (Google OIDC)
1. User clicks “Sign in with Google”.
2. Server generates `state` + `nonce` and redirects to Google authorization endpoint.
3. Google redirects back to `/auth/google/callback` with `code`.
4. Server exchanges code for tokens, validates ID token (issuer/audience/exp/nonce via JWKS).
5. Server upserts user:
   - match by `google_sub` (preferred) or email (optional policy)
6. Server creates session cookie and redirects to UI.

## Data Flow: Approval + Run
1. User creates migration (draft).
2. User requests execution (env + db_set).
3. System creates run in `awaiting_approval`.
4. Manager approves => approval record created; run status becomes `approved`.
5. User executes => run status `queued` then `running`.
6. Engine processes run_items and finalizes run status.

## Execution Engine Model
- A run consists of N run_items, each bound to a db_target.
- Execution strategy:
  - sequential per run (v1 simplest)
  - optional parallel with concurrency limit (future)
- Locking:
  - Postgres: advisory lock derived from target-id
  - MySQL: `GET_LOCK('migrate-hub:<target-id>', timeout)`
- Ensure per-target “migrations” table exists before applying.

## Checksums and Re-approval
- Migration stores `checksum_up`, `checksum_down`.
- Approval stores the checksums at approval time.
- Execution only allowed when:
  - latest migration version checksums match approval checksums
  - approval exists for (migration, env)

## Security
- Sessions: signed and optionally encrypted cookies or server-side store (v1: signed cookie).
- Passwords/secret_ref: stored encrypted at rest (AES-GCM).
- RBAC:
  - user: create/request/execute (non-prod by policy)
  - manager: approve/deny
  - admin: manage users + db inventory
- Audit log must not include secrets.
- OIDC validation: issuer/audience/nonce/state, JWKS caching.

## Failure Handling
- Each run_item is independent:
  - if one target fails, mark run failed
  - v1 policy: stop processing remaining targets on first failure (configurable later)
- Idempotency:
  - if migration already applied with same checksum, mark `skipped`
  - if applied with different checksum, mark `failed` and stop

## Technology Choices (v1)
- Go 1.22+
- Postgres tool DB
- sqlc + pgx
- HTTP router: chi
- HTML templates for UI
- Target DB drivers:
  - Postgres: pgx
  - MySQL: go-sql-driver/mysql
- OIDC/OAuth2:
  - golang.org/x/oauth2
  - github.com/coreos/go-oidc/v3/oidc

## Project Layout (recommended)
- cmd/server/main.go
- internal/http/ (handlers, middleware)
- internal/auth/ (google oidc, sessions)
- internal/rbac/
- internal/store/ (sqlc)
- internal/executor/
- internal/audit/
- web/templates/
- migrations/ (tool db migrations)
- docs/
