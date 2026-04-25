-- name: CreateAgent :exec
INSERT INTO agents (id, name, token_hash, labels, status, last_seen_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: GetAgentByName :one
SELECT id, name, token_hash, labels, status, last_seen_at, created_at, build_commands, container_port FROM agents WHERE name = ?;

-- name: GetAgentByID :one
SELECT id, name, token_hash, labels, status, last_seen_at, created_at, build_commands, container_port FROM agents WHERE id = ?;

-- name: ListAgents :many
SELECT id, name, token_hash, labels, status, last_seen_at, created_at, build_commands, container_port FROM agents ORDER BY created_at DESC;

-- name: UpdateAgentStatus :exec
UPDATE agents SET status = ?, last_seen_at = ? WHERE id = ?;

-- name: DeleteAgent :exec
DELETE FROM agents WHERE id = ?;

-- name: GetAgentBuildConfig :one
SELECT build_commands, container_port FROM agents WHERE id = ?;

-- name: SaveAgentBuildConfig :exec
UPDATE agents
SET build_commands = NULLIF(sqlc.arg(raw_commands), ''),
    container_port = NULLIF(sqlc.arg(port), 0)
WHERE id = sqlc.arg(id);
