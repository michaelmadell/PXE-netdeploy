-- Admin authentication (ADR 0029): local accounts, their web sessions, and
-- API tokens. Sessions and tokens go with the account that owns them.

CREATE TABLE users (
    id                  TEXT    PRIMARY KEY,
    username            TEXT    NOT NULL CHECK (length(username) BETWEEN 1 AND 64),
    password_hash       TEXT    NOT NULL CHECK (password_hash LIKE '$argon2id$%'),
    created_at          INTEGER NOT NULL,
    last_sign_in_at     INTEGER,
    password_changed_at INTEGER
) STRICT;
CREATE UNIQUE INDEX users_username ON users (username COLLATE NOCASE);

CREATE TABLE admin_sessions (
    hash         TEXT    PRIMARY KEY CHECK (length(hash) = 64),
    user_id      TEXT    NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at   INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL CHECK (expires_at > created_at)
) STRICT;
CREATE INDEX admin_sessions_user ON admin_sessions (user_id);

CREATE TABLE api_tokens (
    id           TEXT    PRIMARY KEY,
    hash         TEXT    NOT NULL UNIQUE CHECK (length(hash) = 64),
    name         TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    user_id      TEXT    NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER CHECK (expires_at IS NULL OR expires_at > created_at),
    last_used_at INTEGER
) STRICT;
CREATE INDEX api_tokens_user ON api_tokens (user_id);
