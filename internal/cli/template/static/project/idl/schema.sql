-- Sample SQLite Schema for Scorix Model Codegen
-- Run: scorix generate model -d . --schema etc/schema.sql

CREATE TABLE IF NOT EXISTS users (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    username   TEXT    NOT NULL UNIQUE,
    email      TEXT    NOT NULL,
    role       TEXT    NOT NULL DEFAULT 'user',
    created_at DATETIME,
    updated_at DATETIME,
    deleted_at DATETIME
);

CREATE TABLE IF NOT EXISTS notes (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    INTEGER NOT NULL REFERENCES users(id),
    title      TEXT    NOT NULL,
    body       TEXT,
    created_at DATETIME,
    updated_at DATETIME
);
