# migrate-hub — API (v1)

Base: `/api/v1`
Auth: cookie sessions for WebUI. Use CSRF token for state-changing browser requests.

## Common
### Error format
```json
{ "error": { "code": "validation_error", "message": "..." } }
```

### Pagination
- Query: `?limit=50&offset=0`

## Auth
### Google SSO (OIDC)
- `GET /auth/google/start`
  - Redirects to Google auth endpoint; sets state/nonce (cookie or server-side)
- `GET /auth/google/callback?code=...&state=...`
  - Exchanges code, validates ID token, creates session, redirects to UI
- `POST /auth/logout`
- `GET /auth/me`

### Optional password login (if enabled)
- `POST /auth/login`
  - body: `{ "email": "...", "password": "..." }`

## Users (admin)
- `GET /users`
- `POST /users`
- `PATCH /users/{id}`
- `POST /users/{id}/disable`

## DB Sets / Targets
### DB Sets
- `GET /db-sets?env=stg&project_id=...`
- `POST /db-sets`
  - `{ "project_id":"...", "env":"stg", "name":"auth_stg" }`
- `GET /db-sets/{id}`
- `PATCH /db-sets/{id}`
- `POST /db-sets/{id}/disable`

### DB Targets
- `GET /db-sets/{id}/targets`
- `POST /db-sets/{id}/targets`
  - `{ "engine":"postgres|mysql", "host":"...", "port":5432, "dbname":"...", "username":"...", "password":"...", "options":{...} }`
- `GET /targets/{id}`
- `PATCH /targets/{id}`
- `POST /targets/{id}/test-connection`
- `POST /targets/{id}/disable`

## Migrations
- `GET /migrations?project_id=...&q=...`
- `POST /migrations`
  - `{ "project_id":"...", "key":"20251220_001_add_col", "name":"...", "jira":"AUTH-123", "description":"...", "sql_up":"...", "sql_down":"...", "transaction_mode":"auto|single_transaction|no_transaction" }`
- `GET /migrations/{id}`
- `PATCH /migrations/{id}`
  - Editing sql_up/sql_down increments version and invalidates approvals
- `GET /migrations/{id}/history` (audit/event timeline)

## Approvals
- `GET /approvals?env=stg&status=pending`
- `POST /migrations/{id}/request-approval`
  - `{ "env":"stg", "db_set_id":"..." }`
  - creates a run in `awaiting_approval`
- `POST /runs/{run_id}/approve`
  - `{ "comment":"..." }`
- `POST /runs/{run_id}/deny`
  - `{ "comment":"..." }`

## Runs (execution)
- `GET /runs?env=stg&status=awaiting_approval|approved|running|failed|executed`
- `GET /runs/{id}`
- `POST /runs/{id}/execute`
  - transitions approved -> queued -> running
- `POST /runs/{id}/cancel` (optional v1)
- `GET /runs/{id}/items`
- `GET /runs/{id}/items/{item_id}/logs`

## Rollback
- `POST /migrations/{id}/request-rollback`
  - `{ "env":"stg", "db_set_id":"..." }`
  - creates rollback run awaiting approval
- `POST /runs/{run_id}/execute` executes rollback if approved
