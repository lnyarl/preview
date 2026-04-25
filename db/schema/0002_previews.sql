CREATE TABLE IF NOT EXISTS previews (
    id                 TEXT PRIMARY KEY NOT NULL,
    repo_full_name     TEXT NOT NULL,
    pr_number          INTEGER NOT NULL,
    commit_sha         TEXT NOT NULL,
    branch             TEXT NOT NULL DEFAULT '',
    status             TEXT NOT NULL DEFAULT 'queued',
    assigned_agent_id  TEXT,
    container_id       TEXT,
    agent_host         TEXT,
    agent_port         INTEGER,
    public_url         TEXT,
    labels             TEXT NOT NULL DEFAULT '{}',
    error_message      TEXT,
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL,
    UNIQUE (repo_full_name, pr_number),
    FOREIGN KEY (assigned_agent_id) REFERENCES agents(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_previews_status ON previews(status);
CREATE INDEX IF NOT EXISTS idx_previews_repo_pr ON previews(repo_full_name, pr_number);
CREATE INDEX IF NOT EXISTS idx_previews_assigned ON previews(assigned_agent_id, status);

CREATE TABLE IF NOT EXISTS preview_events (
    id           TEXT PRIMARY KEY NOT NULL,
    preview_id   TEXT NOT NULL,
    from_status  TEXT,
    to_status    TEXT NOT NULL,
    message      TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL,
    FOREIGN KEY (preview_id) REFERENCES previews(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_preview_events_preview ON preview_events(preview_id, created_at);
