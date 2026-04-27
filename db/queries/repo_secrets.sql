-- name: ListRepoSecrets :many
SELECT repo_full_name, key, value, updated_at
FROM repo_secrets
WHERE repo_full_name = ?
ORDER BY key;

-- name: ListRepoSecretRepos :many
SELECT DISTINCT repo_full_name
FROM repo_secrets
ORDER BY repo_full_name;

-- name: UpsertRepoSecret :exec
INSERT INTO repo_secrets (repo_full_name, key, value, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(repo_full_name, key) DO UPDATE SET
    value      = excluded.value,
    updated_at = excluded.updated_at;

-- name: DeleteRepoSecret :exec
DELETE FROM repo_secrets WHERE repo_full_name = ? AND key = ?;

-- name: DeleteAllRepoSecretsFor :exec
DELETE FROM repo_secrets WHERE repo_full_name = ?;
