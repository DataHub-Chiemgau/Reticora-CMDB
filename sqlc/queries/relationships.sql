-- name: GetRelationship :one
SELECT * FROM ci_relationship WHERE id = $1;

-- name: ListRelationshipsBySource :many
SELECT * FROM ci_relationship WHERE source_ci_id = $1 ORDER BY created_at DESC;

-- name: ListRelationshipsByTarget :many
SELECT * FROM ci_relationship WHERE target_ci_id = $1 ORDER BY created_at DESC;

-- name: CreateRelationship :one
INSERT INTO ci_relationship (
    organization_id, source_ci_id, target_ci_id, relationship_type_id,
    source_interface_id, target_interface_id, discovered, confidence, metadata, last_confirmed_at
) VALUES (
    current_setting('app.org_id')::uuid, $1, $2, $3, $4, $5, $6, $7, $8, $9
) RETURNING *;

-- name: DeleteRelationship :exec
DELETE FROM ci_relationship WHERE id = $1;
