-- migrate-hub Tool DB schema (Postgres)
-- Note: keep actual schema managed by migrations in /migrations, this file is documentation.

CREATE TABLE projects (
  id           UUID PRIMARY KEY,
  name         TEXT NOT NULL UNIQUE,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TYPE user_role AS ENUM ('user', 'manager', 'admin');
CREATE TYPE auth_provider AS ENUM ('local', 'google');

CREATE TABLE users (
  id            UUID PRIMARY KEY,
  email         TEXT NOT NULL UNIQUE,
  name          TEXT NOT NULL,
  role          user_role NOT NULL,

  -- auth
  provider      auth_provider NOT NULL DEFAULT 'google',
  google_sub    TEXT UNIQUE,        -- OIDC 'sub' (stable user id from Google)
  password_hash TEXT,               -- nullable if google-only
  is_disabled   BOOLEAN NOT NULL DEFAULT false,
  last_login_at TIMESTAMPTZ,

  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TYPE env_type AS ENUM ('daily', 'stg', 'prd');

CREATE TABLE db_sets (
  id          UUID PRIMARY KEY,
  project_id  UUID REFERENCES projects(id) ON DELETE CASCADE,
  env         env_type NOT NULL,
  name        TEXT NOT NULL,
  is_active   BOOLEAN NOT NULL DEFAULT true,
  created_by  UUID REFERENCES users(id),
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (project_id, env, name)
);

CREATE TYPE db_engine AS ENUM ('postgres', 'mysql');

-- Store secrets encrypted. encryption handled in app.
CREATE TABLE db_targets (
  id            UUID PRIMARY KEY,
  db_set_id     UUID NOT NULL REFERENCES db_sets(id) ON DELETE CASCADE,
  engine        db_engine NOT NULL,
  host          TEXT NOT NULL,
  port          INT NOT NULL,
  dbname        TEXT NOT NULL,
  username      TEXT NOT NULL,
  password_enc  BYTEA NOT NULL, -- encrypted
  options_json  JSONB NOT NULL DEFAULT '{}'::jsonb,
  is_active     BOOLEAN NOT NULL DEFAULT true,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TYPE tx_mode AS ENUM ('auto', 'single_transaction', 'no_transaction');

CREATE TABLE migrations (
  id               UUID PRIMARY KEY,
  project_id       UUID REFERENCES projects(id) ON DELETE CASCADE,
  migration_key    TEXT NOT NULL, -- unique key e.g. 20251220_001_add_col
  name             TEXT NOT NULL,
  jira             TEXT NOT NULL,
  description      TEXT NOT NULL,
  sql_up           TEXT NOT NULL,
  sql_down         TEXT,
  checksum_up      TEXT NOT NULL,
  checksum_down    TEXT,
  version          INT NOT NULL DEFAULT 1,
  transaction_mode tx_mode NOT NULL DEFAULT 'auto',
  created_by       UUID REFERENCES users(id),
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (project_id, migration_key)
);

CREATE TYPE run_type AS ENUM ('apply', 'rollback');
CREATE TYPE run_status AS ENUM (
  'queued',
  'awaiting_approval',
  'approved',
  'denied',
  'running',
  'executed',
  'failed',
  'canceled'
);

CREATE TABLE runs (
  id             UUID PRIMARY KEY,
  run_type       run_type NOT NULL DEFAULT 'apply',
  migration_id   UUID NOT NULL REFERENCES migrations(id) ON DELETE CASCADE,
  project_id     UUID REFERENCES projects(id) ON DELETE CASCADE,
  env            env_type NOT NULL,
  db_set_id      UUID NOT NULL REFERENCES db_sets(id),
  status         run_status NOT NULL,
  requested_by   UUID REFERENCES users(id),
  requested_at   TIMESTAMPTZ NOT NULL DEFAULT now(),

  approved_by    UUID REFERENCES users(id),
  approved_at    TIMESTAMPTZ,
  approval_comment TEXT,

  executed_by    UUID REFERENCES users(id),
  started_at     TIMESTAMPTZ,
  finished_at    TIMESTAMPTZ,

  checksum_up_at_request   TEXT NOT NULL,
  checksum_down_at_request TEXT
);

CREATE TYPE run_item_status AS ENUM (
  'queued',
  'running',
  'executed',
  'skipped',
  'failed',
  'canceled'
);

CREATE TABLE run_items (
  id           UUID PRIMARY KEY,
  run_id       UUID NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  db_target_id UUID NOT NULL REFERENCES db_targets(id),
  status       run_item_status NOT NULL,
  started_at   TIMESTAMPTZ,
  finished_at  TIMESTAMPTZ,
  error        TEXT,
  log          TEXT
);

CREATE TABLE approvals (
  id            UUID PRIMARY KEY,
  migration_id  UUID NOT NULL REFERENCES migrations(id) ON DELETE CASCADE,
  env           env_type NOT NULL,
  decision      TEXT NOT NULL CHECK (decision IN ('approved', 'denied')),
  comment       TEXT,
  decided_by    UUID REFERENCES users(id),
  decided_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  checksum_up   TEXT NOT NULL,
  checksum_down TEXT
);

CREATE TABLE audit_events (
  id          UUID PRIMARY KEY,
  actor_id    UUID REFERENCES users(id),
  action      TEXT NOT NULL,
  entity_type TEXT NOT NULL,
  entity_id   UUID,
  payload     JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX runs_status_idx ON runs(status);
CREATE INDEX runs_env_idx ON runs(env);
CREATE INDEX migrations_project_idx ON migrations(project_id);
CREATE INDEX audit_events_created_at_idx ON audit_events(created_at);
