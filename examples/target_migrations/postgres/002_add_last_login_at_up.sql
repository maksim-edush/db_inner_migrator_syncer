-- Add last_login_at column (Postgres)
ALTER TABLE accounts ADD COLUMN last_login_at TIMESTAMPTZ;
