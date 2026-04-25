-- name: UpsertPreview :one
INSERT INTO previews (
  id, repo_full_name, pr_number, commit_sha, branch,
  status, labels, created_at, updated_at
)
VALUES (?, ?, ?, ?, ?, 'queued', ?, ?, ?)
ON CONFLICT(repo_full_name, pr_number) DO UPDATE SET
  commit_sha = EXCLUDED.commit_sha,
  branch = EXCLUDED.branch,
  labels = EXCLUDED.labels,
  updated_at = EXCLUDED.updated_at
RETURNING *;

-- name: GetPreviewByID :one
SELECT * FROM previews WHERE id = ?;

-- name: GetPreviewByRepoAndPR :one
SELECT * FROM previews WHERE repo_full_name = ? AND pr_number = ?;

-- name: UpdatePreviewStatus :exec
UPDATE previews
SET status = ?, error_message = ?, updated_at = ?
WHERE id = ?;

-- name: InsertPreviewEvent :exec
INSERT INTO preview_events (id, preview_id, from_status, to_status, message, created_at)
VALUES (?, ?, ?, ?, ?, ?);

-- name: ListAllPreviews :many
SELECT * FROM previews ORDER BY created_at DESC;
