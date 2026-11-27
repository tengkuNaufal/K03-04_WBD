CREATE DATABASE lab;
\connect lab
CREATE TABLE events (
    id         BIGSERIAL PRIMARY KEY,
    kind       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO events (kind) SELECT 'seed-' || g FROM generate_series(1, 1000) g;
