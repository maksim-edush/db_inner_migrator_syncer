# Development Log

## Iteration 1
- Bootstrapped Go module and project layout with chi HTTP server, config loader, logging, Postgres pool, and embedded migrations runner.
- Added initial tool DB schema migration aligned with documented schema.
- Added health endpoint (`/api/v1/health`) with DB check, basic request logging/recovery/timeout middleware, and RBAC/auth scaffolding (dev header-based authenticator).
- Added audit event helper and record server startup event after migrations run.

How to run/test:
- Set required env vars: `MIGRATEHUB_DB_DSN`, `MIGRATEHUB_SECRET_KEY`, `MIGRATEHUB_OIDC_GOOGLE_CLIENT_ID`, `MIGRATEHUB_OIDC_GOOGLE_CLIENT_SECRET`, `MIGRATEHUB_OIDC_GOOGLE_REDIRECT_URL`; optional: `MIGRATEHUB_HTTP_ADDR` (default `:8080`), `MIGRATEHUB_DEV_AUTH=true` for header-based dev auth, `MIGRATEHUB_LOG_LEVEL`.
- Run migrations + server: `GOCACHE="$(pwd)/.gocache" go run ./cmd/server`.
- Health check: `curl http://localhost:8080/api/v1/health`.
- Dev-only auth test (if `MIGRATEHUB_DEV_AUTH=true`): `curl -H 'X-MigrateHub-Email=dev@example.com' -H 'X-MigrateHub-Role=admin' http://localhost:8080/api/v1/me`.
- Unit build check: `GOCACHE="$(pwd)/.gocache" go test ./...`.

Known limitations:
- No real Google OIDC, sessions, or CSRF yet; only dev header-based authenticator for testing.
- No WebUI templates or API endpoints beyond health and `/me`.
- Audit logging only records server startup; domain actions not wired yet.
- Migrations runner only supports up migrations in order; no down/rollback handling.

Next step:
- Implement real Google OIDC login flow with session management and CSRF protection, replace dev auth, and start wiring RBAC + audit across endpoints.

## Iteration 2
- Implemented Google OAuth2/OIDC login flow with state/nonce, ID token verification, domain allowlist, and optional auto-provision of users (default role `user`).
- Added signed/encrypted session cookies with CSRF token cookie, session authenticator, and logout endpoint; `/me` now returns authenticated user info.
- Added audit events for login/logout and integrated RBAC/CSRF middleware on authenticated routes.

How to run/test:
- Set env: `MIGRATEHUB_DB_DSN`, `MIGRATEHUB_SECRET_KEY` (base64, >=32 bytes), `MIGRATEHUB_OIDC_GOOGLE_CLIENT_ID`, `MIGRATEHUB_OIDC_GOOGLE_CLIENT_SECRET`, `MIGRATEHUB_OIDC_GOOGLE_REDIRECT_URL`; optional `MIGRATEHUB_OIDC_ALLOWED_DOMAINS` (comma), `MIGRATEHUB_OIDC_AUTO_PROVISION=true` to auto-create users, `MIGRATEHUB_HTTP_ADDR`, `MIGRATEHUB_DEV_AUTH=true` for header-based dev bypass.
- Start server: `GOCACHE="$(pwd)/.gocache" go run ./cmd/server`.
- Begin login: open `http://localhost:8080/api/v1/auth/google/start` (browser), complete Google consent; on success session cookies are set and you’re redirected to `/`.
- Fetch current user: `curl -H "Cookie: <session>" http://localhost:8080/api/v1/me`.
- Logout (requires CSRF token from `migratehub_csrf` cookie): `curl -X POST -H "Cookie: <session>" -H "X-CSRF-Token: <csrf-token>" http://localhost:8080/api/v1/auth/logout`.
- Build check: `GOCACHE="$(pwd)/.gocache" go test ./...`.

Known limitations:
- No persistent session store beyond signed cookie; session invalidation requires cookie clearing or rotation of `MIGRATEHUB_SECRET_KEY`.
- No WebUI templates yet; login redirects to `/` placeholder.
- CSRF middleware applies to authenticated POST/PUT/PATCH/DELETE only; broader coverage may be needed as APIs expand.
- OIDC provider is initialized at startup and requires network access to Google.

Next step:
- Add real session-backed WebUI entry (templates), CSRF token surfacing in HTML, and protect additional state-changing APIs as they are added; begin user/admin management endpoints with audit logging.

## Iteration 3
- Enforced RBAC in HTTP middleware and added audit logging for denied access (unauthenticated or insufficient role) with request details.
- Auth middleware now writes `access_denied` audit events on 401/403 responses while continuing to block unauthorized users.

How to run/test:
- Same env + startup as previous iterations.
- Attempt an authenticated request with insufficient role (e.g., set `X-MigrateHub-Role=user` in dev auth to hit `/api/v1/me` behind manager/admin-only endpoint, if added) and confirm 403 plus audit record in `audit_events` for `access_denied`.
- Invalid/expired session should return 401 and also record `access_denied` audit event.
- Build check: `GOCACHE="$(pwd)/.gocache" go test ./...`.

Known limitations:
- No per-endpoint role matrix beyond current placeholder; only enforcement around middleware-protected routes.
- Audit logging currently covers denied access, login/logout, and server start; other domain actions still need audit hooks.

Next step:
- Wire role checks and audit logging into concrete domain endpoints (projects/DB targets/migrations/approvals) and expand WebUI/API routes with CSRF-surfaced tokens.

## Iteration 4
- Added projects API: list projects (auth required), admin-only create, and project selection persisted in session.
- Session now carries selected project id and `/api/v1/me` reflects it; audit events recorded for project create and selection.
- Project mutation routes protected with CSRF and role enforcement.

How to run/test:
- Start server with required env (same as prior iterations).
- List projects: `curl -H "Cookie: <session>" http://localhost:8080/api/v1/projects`.
- Create project (admin + CSRF): `curl -X POST -H "Cookie: <session>" -H "X-CSRF-Token: <csrf>" -d '{"name":"project-a"}' http://localhost:8080/api/v1/projects`.
- Select project (CSRF): `curl -X POST -H "Cookie: <session>" -H "X-CSRF-Token: <csrf>" http://localhost:8080/api/v1/projects/<project_id>/select`; follow with `/api/v1/me` to see `project_id`.
- Build check: `GOCACHE="$(pwd)/.gocache" go test ./...`.

Known limitations:
- Projects support create/list/select only; no rename/delete yet.
- Selected project is session-scoped and not yet enforced on other resources.
- No UI templates; API-only interactions.

Next step:
- Enforce project scoping across db sets/targets/migrations/approvals and add project rename/delete with audit logging and WebUI surfacing of selected project/CSRF token.

## Iteration 5
- Added DB inventory APIs: create/list/disable DB sets (per selected project), create/list/get/disable DB targets with AES-GCM encrypted passwords using the app secret key.
- Added target connection test endpoint for Postgres/MySQL; success/failure is audited (no secrets logged).
- All DB set/target mutations are CSRF-protected, scoped to the selected project in session, and audited (`db_set_created/disabled`, `db_target_created/disabled/test_success/test_failed`); `/api/v1/me` now returns `project_id`.

How to run/test:
- Ensure a project is selected (via `/api/v1/projects/{id}/select`) after login.
- List DB sets: `curl -H "Cookie: <session>" http://localhost:8080/api/v1/db-sets`.
- Create DB set (user/manager/admin + CSRF): `curl -X POST -H "Cookie: <session>" -H "X-CSRF-Token: <csrf>" -d '{"env":"stg","name":"auth_stg"}' http://localhost:8080/api/v1/db-sets`.
- Create DB target (user/manager/admin + CSRF): `curl -X POST -H "Cookie: <session>" -H "X-CSRF-Token: <csrf>" -d '{"engine":"postgres","host":"localhost","port":5432,"dbname":"app","username":"u","password":"p"}' http://localhost:8080/api/v1/db-sets/<db_set_id>/targets`.
- List targets: `curl -H "Cookie: <session>" http://localhost:8080/api/v1/db-sets/<db_set_id>/targets`.
- Get target: `curl -H "Cookie: <session>" http://localhost:8080/api/v1/targets/<target_id>`.
- Test connection (CSRF): `curl -X POST -H "Cookie: <session>" -H "X-CSRF-Token: <csrf>" http://localhost:8080/api/v1/targets/<target_id>/test-connection`.
- Disable DB set/target (admin + CSRF): `curl -X POST -H "Cookie: <session>" -H "X-CSRF-Token: <csrf>" http://localhost:8080/api/v1/db-sets/<id>/disable` or `/api/v1/targets/<id>/disable`.
- Build check: `GOCACHE="$(pwd)/.gocache" go test ./...`.

Known limitations:
- No rename/delete for projects/db sets/targets; no pagination on lists.
- Connection test requires network access to the target; errors are generic to avoid leaking details.
- Selected project is enforced for DB set/target routes but not yet across migrations/runs.
- No UI templates; API-only interactions.

Next step:
- Apply project scoping and RBAC to migrations/approvals/runs, add rename/delete for DB resources with audit logging, and begin surfacing these flows in server-rendered templates with CSRF token exposure.

## Iteration 6
- Added migrations API: list/get within selected project, create, and update with checksum/version management and approval invalidation.
- Creation computes SHA256 checksums for sql_up/sql_down, sets version=1; updates increment version and delete approvals when SQL changes.
- Audit events for migration create/update and approvals invalidation; RBAC (user/manager/admin), CSRF on mutations, and project selection required.

How to run/test:
- Select a project via `/api/v1/projects/{id}/select` after login.
- List migrations: `curl -H "Cookie: <session>" http://localhost:8080/api/v1/migrations`.
- Create migration (CSRF, user/manager/admin): `curl -X POST -H "Cookie: <session>" -H "X-CSRF-Token: <csrf>" -d '{"key":"20250101_001_add_users","name":"Add users","jira":"AUTH-1","description":"add table","sql_up":"CREATE TABLE t(id int);","transaction_mode":"auto"}' http://localhost:8080/api/v1/migrations`.
- Get migration: `curl -H "Cookie: <session>" http://localhost:8080/api/v1/migrations/<id>`.
- Update migration (CSRF): `curl -X PATCH -H "Cookie: <session>" -H "X-CSRF-Token: <csrf>" -d '{"sql_up":"CREATE TABLE t(id serial);"}' http://localhost:8080/api/v1/migrations/<id>`; version increments and approvals are cleared.
- Build check: `GOCACHE="$(pwd)/.gocache" go test ./...`.

Known limitations:
- No migration deletion; no history/events endpoint; approvals/runs execution still stubbed.
- No pagination or filtering beyond simple `?q=` search.
- UI not implemented; API-only interactions.

Next step:
- Wire approvals/runs pipeline (request approval, approve/deny, execute) with checksum validation and project scoping; add migration history/audit views and minimal templates for these flows.

## Iteration 7
- Added run request/approval/denial flow: create run awaiting approval for a migration + db set/env, generate run_items for active targets, and store checksums at request time.
- Approval/denial checks current migration checksums to prevent stale approvals; decisions persist to `approvals` and update run status. Audit events added for run request/approve/deny.
- Added run read endpoints (get by id, list by migration) scoped to selected project; RBAC enforced (approve/deny require manager/admin) and CSRF on mutations.

How to run/test:
- Select a project after login: `POST /api/v1/projects/<project_id>/select` with session + CSRF.
- Ensure a DB set with active targets exists for the target env and a migration in that project.
- Request approval (CSRF): `curl -X POST -H "Cookie: <session>" -H "X-CSRF-Token: <csrf>" -d '{"env":"stg","db_set_id":"<db_set_id>"}' http://localhost:8080/api/v1/migrations/<migration_id>/request-approval`.
- Approve run (manager/admin + CSRF): `curl -X POST -H "Cookie: <session>" -H "X-CSRF-Token: <csrf>" -d '{"comment":"looks good"}' http://localhost:8080/api/v1/runs/<run_id>/approve`.
- Deny run similarly at `/api/v1/runs/<run_id>/deny`.
- Get run: `GET /api/v1/runs/<run_id>`; list runs for migration: `GET /api/v1/migrations/<migration_id>/runs`.
- Build check: `GOCACHE="$(pwd)/.gocache" go test ./...`.

Known limitations:
- No execution engine yet; approved runs are not queued for execution.
- No pagination on runs; no migration history endpoint.
- No UI templates; API-only interactions.

Next step:
- Implement run execution queue/worker with per-target locking and migrations table checks, plus history endpoints/templates and pagination; ensure checksum validation before execution.

## Iteration 8
- Added run execution: executes approved runs per target with advisory locks (Postgres) / GET_LOCK (MySQL), creates per-target migrations table if missing, records applied/ skipped/failed statuses, and updates run status accordingly.
- Enforces checksum match against requested approvals before execution; skips targets already applied with same checksum; fails on checksum mismatch in target migrations table.
- Added `/api/v1/runs/{id}/execute` endpoint (auth+CSRF; user/manager/admin) and run read/list endpoints enhanced to show statuses; audit events for execute success/failure.

How to run/test:
- Ensure a project is selected and migration run is approved with active targets.
- Execute run (CSRF): `curl -X POST -H "Cookie: <session>" -H "X-CSRF-Token: <csrf>" http://localhost:8080/api/v1/runs/<run_id>/execute`.
- Inspect run: `GET /api/v1/runs/<run_id>` to see per-target statuses.
- Approve/deny/request as in previous iteration; build check: `GOCACHE="$(pwd)/.gocache" go test ./...`.

Known limitations:
- Execution is synchronous and stops on first target failure; no background queue or retry/backoff.
- No pagination/history views; minimal logging captured in run_items only.
- Target migrations table schema is minimal and not configurable.

Next step:
- Introduce async worker/queue for runs with retry policies, richer logging, pagination/history endpoints/templates, and execution cancellation; enforce checksum validation at execution start and add rollback handling.

## Iteration 9
- Added rollback support: request rollback (if sql_down exists), approve/deny as before, and execute rollback using `sql_down` across targets with the same locking and checksum safeguards as apply.
- Execution now distinguishes apply vs rollback; skips targets already applied with matching checksum, fails on mismatched target checksums, and records run item statuses accordingly. Audit events for rollback request and execution.

How to run/test:
- Select project; ensure migration has `sql_down` and a db set/targets for the env.
- Request rollback (CSRF): `POST /api/v1/migrations/<migration_id>/request-rollback` with `{"env":"stg","db_set_id":"<db_set_id>"}`.
- Approve/deny run as before: `POST /api/v1/runs/<run_id>/approve|deny` (manager/admin + CSRF).
- Execute rollback (CSRF): `POST /api/v1/runs/<run_id>/execute`.
- Inspect run items: `GET /api/v1/runs/<run_id>`.
- Build check: `GOCACHE="$(pwd)/.gocache" go test ./...`.

Known limitations:
- Target migrations table does not mark rollbacks explicitly (no rolled_back_at); history is via runs/run_items.
- Execution is synchronous and halts on first failure; no retries/queue.
- No UI templates or pagination yet.

Next step:
- Add explicit rollback marking in target migrations table or history log, move execution to async worker with retries, and surface run/migration history in UI with pagination and clearer status timelines.

## Iteration 10
- Added server-rendered WebUI under `/ui` with a shared layout, navigation, flash messages, project selector, and logout; all UI pages require login and enforce RBAC both in UI and handlers.
- Implemented UI pages for dashboard, projects, DB sets/targets, migrations, approvals, runs, and run logs; wired CSRF-protected POST forms for all state-changing actions.
- Added UI data queries (counts, recent runs, pending approvals, timeline) and static styling (`/static/app.css`); audit events are triggered for all UI actions using the same events as API.

How to run/test:
- Start server and login: visit `http://localhost:8080/api/v1/auth/google/start`, then open `http://localhost:8080/ui`.
- Verify header shows user email/role, project selector, and logout button on all pages.
- Create/select project: `/ui/projects` (admin-only create); select a project via header dropdown.
- Create DB set/target: `/ui/db-sets?env=stg` and `/ui/db-sets/{id}` (add target, test connection).
- Create migration: `/ui/migrations/new`; view/update at `/ui/migrations/{id}`; request approval/rollback from detail page.
- Approve/deny: `/ui/approvals` (manager/admin); execute approved run at `/ui/runs/{id}`; view logs at `/ui/runs/{id}/items/{item_id}/logs`.
- Build check: `GOCACHE="$(pwd)/.gocache" go test ./...`.

Known limitations:
- UI is minimal and synchronous; no pagination or async execution controls.
- Migration status badges are derived from latest runs, not a denormalized state table.
- No admin user management UI yet.

Next step:
- Add user management UI, pagination on runs/migrations, and richer history views; consider async execution worker with live status updates.

## Iteration 11
- Added UI login landing page at `/ui/login` with Google sign-in; unauthenticated UI routes redirect to login and post-login redirects to `/ui`.
- Added admin user management UI under `/ui/users` with create, role/name updates, and disable actions; server-side validation and audit events added.
- Updated UI header to show admin Users link and a sign-in button when logged out; added minimal styles for login and compact table inputs.

How to run/test:
- Visit `http://localhost:8080/ui/login` and sign in with Google.
- Confirm authenticated UI routes redirect to `/ui` and show user header with project selector/logout.
- As admin, open `/ui/users` to create a user, change role/name, and disable a user; verify disabled users cannot log in.
- Build check: `GOCACHE="$(pwd)/.gocache" go test ./...`.

Known limitations:
- No re-enable user flow; email changes are not supported.
- User management has no pagination or search filters.

Next step:
- Add user enable/reset flows and pagination/search for users, plus optional domain allowlist controls.

## Iteration 12
- Made migration `jira` and `description` optional in API/UI; validations updated accordingly.
- Expanded `/ui/projects` with per-project DB inventory summary (envs, db sets/targets) and masked DSNs.
- Rendered migration history payloads as pretty JSON for readability.

How to run/test:
- Create a migration without `jira`/`description` via UI or API; confirm it saves and displays as "-" in UI.
- Visit `/ui/projects` and verify DB inventory tables show env counts and masked DSNs (`***` for user/pass).
- Open a migration detail page and confirm audit payloads are pretty JSON.
- Build check: `GOCACHE="$(pwd)/.gocache" go test ./...`.

Known limitations:
- Project inventory list is not paginated; large target lists may be long.

Next step:
- Add pagination/search for projects inventory and migrations.

## Iteration 13
- Added Run/Rollback buttons in migration detail and migration list views when the latest run for an env is approved.
- Buttons post to `/ui/runs/{id}/execute` for quicker execution (works for daily/stg/prd and rollback approvals).

How to run/test:
- Request approval for a migration in stg/prd, approve it, then verify the Run button appears on `/ui/migrations` and `/ui/migrations/{id}`.
- Click Run and confirm the run executes and status updates.
- Request rollback, approve it, and verify the Rollback button appears; execute it from the same pages.
- Build check: `GOCACHE="$(pwd)/.gocache" go test ./...`.

Known limitations:
- Only the latest run per env is shown; if you need both apply + rollback context, open the run detail page.

Next step:
- Add richer per-env history on migration detail (show latest apply + rollback side-by-side).

## Iteration 14
- Added `/ui/targets` page to compare migrations per DB target, showing applied/missing/approved/failed/rollbacked statuses and links to latest runs.
- Added a light/dark theme toggle and refreshed UI styling with modernized colors, gradients, and subtle animations.

How to run/test:
- Visit `/ui/targets` and verify per-target migration tables with status badges and run links; use env filter and “Only missing”.
- Toggle Theme in the header and verify light/dark styling persists across reloads.
- Build check: `GOCACHE="$(pwd)/.gocache" go test ./...`.

Known limitations:
- Target coverage uses latest run items from tool DB (no direct inspection of target DB migrations table).
- Large projects may render long tables without pagination.

Next step:
- Add pagination or a per-target drilldown view for large migration lists; optionally add direct target DB introspection for authoritative applied state.
