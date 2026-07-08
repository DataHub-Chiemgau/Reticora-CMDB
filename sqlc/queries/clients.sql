-- name: GetClient :one
SELECT * FROM client WHERE id = $1 AND organization_id = current_setting('app.org_id')::uuid;

-- name: ListClients :many
SELECT * FROM client WHERE organization_id = current_setting('app.org_id')::uuid
ORDER BY name LIMIT $1 OFFSET $2;

-- name: CreateClient :one
INSERT INTO client (organization_id, name, external_ref, settings)
VALUES (current_setting('app.org_id')::uuid, $1, $2, $3)
RETURNING *;
