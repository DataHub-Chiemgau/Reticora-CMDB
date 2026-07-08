-- name: InsertAuditLog :one
INSERT INTO audit_log (
    organization_id, occurred_at, actor_type, actor_id,
    action, entity_type, entity_id, payload, prev_hash, hash
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: ListAuditLogs :many
SELECT * FROM audit_log
WHERE organization_id = $1
ORDER BY id DESC
LIMIT $2 OFFSET $3;

-- name: GetLatestAuditHash :one
SELECT hash FROM audit_log
WHERE organization_id = $1
ORDER BY id DESC
LIMIT 1;
