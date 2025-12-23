-- migrate-hub Tool DB schema (Postgres)

CREATE TABLE IF NOT EXISTS projects (
  id           UUID PRIMARY KEY,
  name         TEXT NOT NULL UNIQUE,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

DO $$ BEGIN
  CREATE TYPE user_role AS ENUM ('user', 'manager', 'admin');
EXCEPTION
  WHEN duplicate_object THEN NULL;
END $$;

DO $$ BEGIN
  CREATE TYPE auth_provider AS ENUM ('local', 'google');
EXCEPTION
  WHEN duplicate_object THEN NULL;
END $$;

CREATE TABLE IF NOT EXISTS users (
  id            UUID PRIMARY KEY,
  email         TEXT NOT NULL UNIQUE,
  name          TEXT NOT NULL,
  role          user_role NOT NULL,
  provider      auth_provider NOT NULL DEFAULT 'google',
  google_sub    TEXT UNIQUE,
  password_hash TEXT,
  is_disabled   BOOLEAN NOT NULL DEFAULT false,
  last_login_at TIMESTAMPTZ,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

DO $$ BEGIN
  CREATE TYPE env_type AS ENUM ('daily', 'stg', 'prd');
EXCEPTION
  WHEN duplicate_object THEN NULL;
END $$;

CREATE TABLE IF NOT EXISTS db_sets (
  id          UUID PRIMARY KEY,
  project_id  UUID REFERENCES projects(id) ON DELETE CASCADE,
  env         env_type NOT NULL,
  name        TEXT NOT NULL,
  is_active   BOOLEAN NOT NULL DEFAULT true,
  created_by  UUID REFERENCES users(id),
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (project_id, env, name)
);

DO $$ BEGIN
  CREATE TYPE db_engine AS ENUM ('postgres', 'mysql');
EXCEPTION
  WHEN duplicate_object THEN NULL;
END $$;

CREATE TABLE IF NOT EXISTS db_targets (
  id            UUID PRIMARY KEY,
  db_set_id     UUID NOT NULL REFERENCES db_sets(id) ON DELETE CASCADE,
  engine        db_engine NOT NULL,
  host          TEXT NOT NULL,
  port          INT NOT NULL,
  dbname        TEXT NOT NULL,
  username      TEXT NOT NULL,
  password_enc  BYTEA NOT NULL,
  options_json  JSONB NOT NULL DEFAULT '{}'::jsonb,
  is_active     BOOLEAN NOT NULL DEFAULT true,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

DO $$ BEGIN
  CREATE TYPE tx_mode AS ENUM ('auto', 'single_transaction', 'no_transaction');
EXCEPTION
  WHEN duplicate_object THEN NULL;
END $$;

CREATE TABLE IF NOT EXISTS migrations (
  id               UUID PRIMARY KEY,
  project_id       UUID REFERENCES projects(id) ON DELETE CASCADE,
  migration_key    TEXT NOT NULL,
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

DO $$ BEGIN
  CREATE TYPE run_type AS ENUM ('apply', 'rollback');
EXCEPTION
  WHEN duplicate_object THEN NULL;
END $$;

DO $$ BEGIN
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
EXCEPTION
  WHEN duplicate_object THEN NULL;
END $$;

CREATE TABLE IF NOT EXISTS runs (
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

DO $$ BEGIN
  CREATE TYPE run_item_status AS ENUM (
    'queued',
    'running',
    'executed',
    'skipped',
    'failed',
    'canceled'
  );
EXCEPTION
  WHEN duplicate_object THEN NULL;
END $$;

CREATE TABLE IF NOT EXISTS run_items (
  id           UUID PRIMARY KEY,
  run_id       UUID NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  db_target_id UUID NOT NULL REFERENCES db_targets(id),
  status       run_item_status NOT NULL,
  started_at   TIMESTAMPTZ,
  finished_at  TIMESTAMPTZ,
  error        TEXT,
  log          TEXT
);

CREATE TABLE IF NOT EXISTS approvals (
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

CREATE TABLE IF NOT EXISTS audit_events (
  id          UUID PRIMARY KEY,
  actor_id    UUID REFERENCES users(id),
  action      TEXT NOT NULL,
  entity_type TEXT NOT NULL,
  entity_id   UUID,
  payload     JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS runs_status_idx ON runs(status);
CREATE INDEX IF NOT EXISTS runs_env_idx ON runs(env);
CREATE INDEX IF NOT EXISTS migrations_project_idx ON migrations(project_id);
CREATE INDEX IF NOT EXISTS audit_events_created_at_idx ON audit_events(created_at);
