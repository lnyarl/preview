CREATE TABLE IF NOT EXISTS agents (
    id           TEXT PRIMARY KEY NOT NULL,
    name         TEXT NOT NULL UNIQUE,
    token_hash   TEXT NOT NULL,
    labels       TEXT NOT NULL DEFAULT '{}',
    status       TEXT NOT NULL DEFAULT 'offline',
    last_seen_at TEXT,
    created_at   TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_agents_status ON agents(status);
