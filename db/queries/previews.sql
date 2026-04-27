-- name: UpsertPreview :one
INSERT INTO previews (
  id, repo_full_name, pr_number, commit_sha, branch,
  status, labels, repo_clone_url, is_adhoc, created_at, updated_at
)
VALUES (?, ?, ?, ?, ?, 'queued', ?, ?, ?, ?, ?)
ON CONFLICT(repo_full_name, commit_sha) DO UPDATE SET
  pr_number = EXCLUDED.pr_number,
  branch = EXCLUDED.branch,
  labels = EXCLUDED.labels,
  repo_clone_url = EXCLUDED.repo_clone_url,
  updated_at = EXCLUDED.updated_at
RETURNING *;

-- name: GetPreviewByID :one
SELECT * FROM previews WHERE id = ?;

-- name: GetPreviewByRepoAndPR :one
SELECT * FROM previews WHERE repo_full_name = ? AND pr_number = ?;

-- name: GetActivePreviewByRepoAndPR :one
SELECT * FROM previews
WHERE repo_full_name = ? AND pr_number = ?
  AND status IN ('queued','assigned','building','running')
ORDER BY created_at DESC
LIMIT 1;

-- name: GetPreviewByRepoAndSha :one
SELECT * FROM previews WHERE repo_full_name = ? AND commit_sha = ?;

-- name: UpdatePreviewStatus :exec
UPDATE previews
SET status = ?, error_message = ?, updated_at = ?
WHERE id = ?;

-- name: UpdatePreviewStatusFields :execrows
UPDATE previews
SET status = ?,
    updated_at = ?,
    container_id = COALESCE(?, container_id),
    agent_host = COALESCE(?, agent_host),
    agent_port = COALESCE(?, agent_port),
    preview_urls = COALESCE(?, preview_urls),
    error_message = COALESCE(?, error_message),
    assigned_agent_id = COALESCE(?, assigned_agent_id),
    commit_sha = COALESCE(commit_sha, ?)
WHERE id = ?;

-- name: InsertPreviewEvent :exec
INSERT INTO preview_events (id, preview_id, from_status, to_status, message, created_at)
VALUES (?, ?, ?, ?, ?, ?);

-- name: ListAllPreviews :many
SELECT * FROM previews ORDER BY created_at DESC;

-- name: ListQueuedPreviewsForLabels :many
SELECT * FROM previews WHERE status = 'queued' ORDER BY created_at ASC LIMIT 50;

-- name: ClaimPreview :one
UPDATE previews
SET status = 'assigned',
    assigned_agent_id = ?,
    updated_at = ?
WHERE id = (
  SELECT p.id FROM previews p
  WHERE p.status = 'queued'
    AND p.id IN (sqlc.slice('candidate_ids'))
  ORDER BY p.created_at ASC
  LIMIT 1
)
RETURNING *;

-- name: ResetAllAssignedPreviews :execrows
UPDATE previews SET status = 'queued', assigned_agent_id = NULL, updated_at = ? WHERE status = 'assigned';

-- name: FindPreviewByHost :one
SELECT * FROM previews WHERE pr_number = ? ORDER BY created_at DESC LIMIT 1;

-- name: ListStaleAssignedPreviews :many
SELECT * FROM previews WHERE status = 'assigned' AND updated_at < ? ORDER BY updated_at ASC;

-- name: ListRunningPreviewsByAgent :many
SELECT * FROM previews WHERE status = 'running' AND assigned_agent_id = ? ORDER BY updated_at ASC;

-- name: ListByAgent :many
SELECT * FROM previews
WHERE assigned_agent_id = ?
  AND status IN (sqlc.slice('statuses'))
ORDER BY updated_at ASC;

-- name: ListPreviewEvents :many
SELECT * FROM preview_events
WHERE preview_id = ?
ORDER BY created_at ASC, id ASC
LIMIT ? OFFSET ?;
