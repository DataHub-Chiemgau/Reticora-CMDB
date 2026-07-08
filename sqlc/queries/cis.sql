-- name: GetCI :one
SELECT * FROM ci WHERE id = $1;

-- name: ListCIs :many
SELECT * FROM ci
WHERE deleted_at IS NULL
ORDER BY name
LIMIT $1 OFFSET $2;

-- name: ListCIsByType :many
SELECT * FROM ci
WHERE ci_type_id = $1 AND deleted_at IS NULL
ORDER BY name
LIMIT $2 OFFSET $3;

-- name: ListCIsByStatus :many
SELECT * FROM ci
WHERE status = $1 AND deleted_at IS NULL
ORDER BY name
LIMIT $2 OFFSET $3;

-- name: CreateCI :one
INSERT INTO ci (
    organization_id, client_id, site_id, room_id, ci_type_id,
    name, status, manufacturer, model, serial_number,
    hardware_uuid, management_ip, primary_mac, hostname, fqdn,
    os_name, os_version, firmware_version, sys_object_id,
    attributes, discovery_source, first_seen_at, last_seen_at, is_manual
) VALUES (
    current_setting('app.org_id')::uuid, $1, $2, $3, $4,
    $5, $6, $7, $8, $9,
    $10, $11, $12, $13, $14,
    $15, $16, $17, $18,
    $19, $20, $21, $22, $23
) RETURNING *;

-- name: UpdateCI :one
UPDATE ci SET
    name = COALESCE(sqlc.narg('name'), name),
    status = COALESCE(sqlc.narg('status'), status),
    manufacturer = COALESCE(sqlc.narg('manufacturer'), manufacturer),
    model = COALESCE(sqlc.narg('model'), model),
    serial_number = COALESCE(sqlc.narg('serial_number'), serial_number),
    management_ip = COALESCE(sqlc.narg('management_ip'), management_ip),
    hostname = COALESCE(sqlc.narg('hostname'), hostname),
    firmware_version = COALESCE(sqlc.narg('firmware_version'), firmware_version),
    attributes = COALESCE(sqlc.narg('attributes'), attributes),
    last_seen_at = COALESCE(sqlc.narg('last_seen_at'), last_seen_at),
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: SoftDeleteCI :exec
UPDATE ci SET deleted_at = now(), updated_at = now() WHERE id = $1;

-- name: SearchCIs :many
SELECT * FROM ci
WHERE deleted_at IS NULL
  AND (
    to_tsvector('simple', coalesce(name,'') || ' ' || coalesce(hostname,'') || ' ' || coalesce(manufacturer,'') || ' ' || coalesce(model,'') || ' ' || coalesce(serial_number,''))
    @@ plainto_tsquery('simple', $1)
  )
ORDER BY name
LIMIT $2 OFFSET $3;

-- name: CountCIs :one
SELECT count(*) FROM ci WHERE deleted_at IS NULL;
