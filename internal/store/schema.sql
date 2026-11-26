CREATE TABLE IF NOT EXISTS users (
    id            BIGSERIAL PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    is_admin      BOOLEAN NOT NULL DEFAULT FALSE,
    is_active     BOOLEAN NOT NULL DEFAULT TRUE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS groups (
    id   BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL UNIQUE
);
ALTER TABLE groups ADD COLUMN IF NOT EXISTS leader_id BIGINT REFERENCES users(id) ON DELETE SET NULL;

CREATE TABLE IF NOT EXISTS group_members (
    group_id BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    user_id  BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, user_id)
);

CREATE TABLE IF NOT EXISTS databases (
    id           BIGSERIAL PRIMARY KEY,
    name         TEXT NOT NULL UNIQUE,
    host         TEXT NOT NULL,
    port         INT NOT NULL,
    dbname       TEXT NOT NULL,
    username     TEXT NOT NULL,
    password_enc BYTEA NOT NULL,
    sslmode      TEXT NOT NULL DEFAULT 'disable'
);

CREATE TABLE IF NOT EXISTS group_database_access (
    group_id    BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    database_id BIGINT NOT NULL REFERENCES databases(id) ON DELETE CASCADE,
    level       TEXT NOT NULL CHECK (level IN ('view', 'edit')),
    PRIMARY KEY (group_id, database_id)
);

CREATE TABLE IF NOT EXISTS session_families (
    id                  TEXT PRIMARY KEY,
    user_id             BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    csrf_token          TEXT NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL,
    last_seen_at        TIMESTAMPTZ NOT NULL,
    absolute_expires_at TIMESTAMPTZ NOT NULL,
    revoked_at          TIMESTAMPTZ,
    rotate_required     BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE TABLE IF NOT EXISTS session_tokens (
    token_hash    BYTEA PRIMARY KEY,
    family_id     TEXT NOT NULL REFERENCES session_families(id) ON DELETE CASCADE,
    issued_at     TIMESTAMPTZ NOT NULL,
    superseded_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS migrations (
    id          BIGSERIAL PRIMARY KEY,
    database_id BIGINT NOT NULL REFERENCES databases(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    sql_text    TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'draft',
    version     INT,
    created_by  TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (database_id, version)
);

ALTER TABLE migrations ADD COLUMN IF NOT EXISTS down_sql TEXT NOT NULL DEFAULT '';
ALTER TABLE migrations ADD COLUMN IF NOT EXISTS irreversible_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE migrations ADD COLUMN IF NOT EXISTS mode TEXT NOT NULL DEFAULT 'normal';
ALTER TABLE migrations ADD COLUMN IF NOT EXISTS batch_table TEXT NOT NULL DEFAULT '';
ALTER TABLE migrations ADD COLUMN IF NOT EXISTS batch_column TEXT NOT NULL DEFAULT '';
ALTER TABLE migrations ADD COLUMN IF NOT EXISTS batch_size INT NOT NULL DEFAULT 0;
ALTER TABLE migrations DROP CONSTRAINT IF EXISTS migrations_status_check;
UPDATE migrations SET status = 'draft' WHERE status = 'failed';
ALTER TABLE migrations ADD CONSTRAINT migrations_status_check
    CHECK (status IN ('draft', 'applied', 'rolled_back', 'partial'));

CREATE TABLE IF NOT EXISTS change_requests (
    id                BIGSERIAL PRIMARY KEY,
    database_id       BIGINT NOT NULL REFERENCES databases(id) ON DELETE CASCADE,
    migration_id      BIGINT NOT NULL REFERENCES migrations(id) ON DELETE CASCADE,
    action            TEXT NOT NULL CHECK (action IN ('apply', 'rollback', 'forward', 'resume')),
    direction         TEXT NOT NULL CHECK (direction IN ('up', 'down')),
    target_request_id BIGINT REFERENCES change_requests(id),
    series_id         BIGINT NOT NULL,
    undo_series_id    BIGINT,
    status            TEXT NOT NULL,
    note              TEXT NOT NULL DEFAULT '',
    sql_hash          TEXT NOT NULL,
    scope_max         BIGINT,
    batches_done      INT NOT NULL DEFAULT 0,
    rows_done         BIGINT NOT NULL DEFAULT 0,
    error             TEXT NOT NULL DEFAULT '',
    requested_by      TEXT NOT NULL,
    requested_by_id   BIGINT NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS change_requests_db_idx ON change_requests (database_id, status);

CREATE TABLE IF NOT EXISTS request_approvals (
    request_id BIGINT NOT NULL REFERENCES change_requests(id) ON DELETE CASCADE,
    group_id   BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    group_name TEXT NOT NULL,
    decision   TEXT CHECK (decision IN ('approved', 'rejected')),
    decided_by TEXT,
    decided_at TIMESTAMPTZ,
    comment    TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (request_id, group_id)
);

CREATE TABLE IF NOT EXISTS jobs (
    id            BIGSERIAL PRIMARY KEY,
    request_id    BIGINT NOT NULL UNIQUE REFERENCES change_requests(id) ON DELETE CASCADE,
    database_id   BIGINT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'done', 'failed')),
    attempts      INT NOT NULL DEFAULT 0,
    max_attempts  INT NOT NULL DEFAULT 3,
    run_after     TIMESTAMPTZ NOT NULL DEFAULT now(),
    locked_by     TEXT,
    heartbeat_at  TIMESTAMPTZ,
    trace_context JSONB NOT NULL DEFAULT '{}',
    enqueued_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at    TIMESTAMPTZ,
    finished_at   TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS jobs_queue_idx ON jobs (status, run_after);

CREATE TABLE IF NOT EXISTS executions (
    id                   BIGSERIAL PRIMARY KEY,
    request_id           BIGINT NOT NULL,
    job_id               BIGINT NOT NULL,
    database_id          BIGINT NOT NULL,
    migration_id         BIGINT NOT NULL,
    migration_name       TEXT NOT NULL,
    migration_version    INT,
    action               TEXT NOT NULL,
    direction            TEXT NOT NULL,
    attempt              INT NOT NULL,
    reverts_execution_id BIGINT,
    sql_text             TEXT NOT NULL,
    status               TEXT NOT NULL,
    batches              INT NOT NULL DEFAULT 0,
    rows_affected        BIGINT NOT NULL DEFAULT 0,
    error                TEXT NOT NULL DEFAULT '',
    executed_by          TEXT NOT NULL,
    trace_id             TEXT NOT NULL DEFAULT '',
    started_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at          TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS executions_db_idx ON executions (database_id, id);

CREATE OR REPLACE FUNCTION executions_append_only() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'DELETE' OR OLD.status <> 'running' THEN
        RAISE EXCEPTION 'migration history is append-only (execution %)', OLD.id;
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS executions_append_only ON executions;
CREATE TRIGGER executions_append_only BEFORE UPDATE OR DELETE ON executions
    FOR EACH ROW EXECUTE FUNCTION executions_append_only();
