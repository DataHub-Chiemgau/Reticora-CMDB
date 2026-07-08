-- name: GetOrganization :one
SELECT * FROM organization WHERE id = $1;

-- name: GetOrganizationBySlug :one
SELECT * FROM organization WHERE slug = $1;

-- name: ListOrganizations :many
SELECT * FROM organization ORDER BY name LIMIT $1 OFFSET $2;

-- name: CreateOrganization :one
INSERT INTO organization (name, slug, plan, settings)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: UpdateOrganization :one
UPDATE organization SET name = $1, plan = $2, settings = $3, updated_at = now()
WHERE id = $4 RETURNING *;
