-- name: GetSite :one
SELECT * FROM site WHERE id = $1;

-- name: ListSites :many
SELECT * FROM site ORDER BY name LIMIT $1 OFFSET $2;

-- name: CreateSite :one
INSERT INTO site (organization_id, client_id, name, address, geo_lat, geo_lon, notes)
VALUES (current_setting('app.org_id')::uuid, $1, $2, $3, $4, $5, $6)
RETURNING *;
