-- name: GetCollector :one
SELECT * FROM collector WHERE id = $1;

-- name: ListCollectors :many
SELECT * FROM collector ORDER BY name LIMIT $1 OFFSET $2;

-- name: CreateCollector :one
INSERT INTO collector (
    organization_id, client_id, site_id, name,
    enrollment_token_hash, enrollment_token_expires_at,
    status, version, config
) VALUES (
    current_setting('app.org_id')::uuid, $1, $2, $3, $4, $5, $6, $7, $8
) RETURNING *;

-- name: UpdateCollectorHeartbeat :exec
UPDATE collector SET last_heartbeat_at = now(), status = 'active', version = $2
WHERE id = $1;

-- name: UpdateCollectorStatus :exec
UPDATE collector SET status = $2, updated_at = now() WHERE id = $1;
