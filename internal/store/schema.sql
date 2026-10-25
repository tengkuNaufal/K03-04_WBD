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
    status      TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'applied', 'failed')),
    version     INT,
    created_by  TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    executed_by TEXT,
    executed_at TIMESTAMPTZ,
    error       TEXT,
    UNIQUE (database_id, version)
);
