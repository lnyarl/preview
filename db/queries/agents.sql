-- name: CreateAgent :exec
INSERT INTO agents (id, name, token_hash, labels, status, last_seen_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: GetAgentByName :one
SELECT id, name, token_hash, labels, status, last_seen_at, created_at FROM agents WHERE name = ?;

-- name: GetAgentByID :one
SELECT id, name, token_hash, labels, status, last_seen_at, created_at FROM agents WHERE id = ?;

-- name: ListAgents :many
SELECT id, name, token_hash, labels, status, last_seen_at, created_at FROM agents ORDER BY created_at DESC;

-- name: UpdateAgentStatus :exec
UPDATE agents SET status = ?, last_seen_at = ? WHERE id = ?;

-- name: DeleteAgent :exec
DELETE FROM agents WHERE id = ?;

-- name: ResetAllOnlineAgents :execrows
UPDATE agents SET status = 'offline' WHERE status = 'online';
