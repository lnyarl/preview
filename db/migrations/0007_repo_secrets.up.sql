-- WARNING: stored as plaintext (Phase 10 limitation, see docs/specs/phase-10-repo-build-secrets.md §3 결정 7).
CREATE TABLE repo_secrets (
    repo_full_name TEXT NOT NULL,
    key            TEXT NOT NULL,
    value          TEXT NOT NULL DEFAULT '',
    updated_at     TEXT NOT NULL,
    PRIMARY KEY (repo_full_name, key)
);
CREATE INDEX idx_repo_secrets_repo ON repo_secrets(repo_full_name);
