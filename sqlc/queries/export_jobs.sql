-- name: CreateExportJob :one
INSERT INTO export_job (organization_id, requested_by, format, params, status)
VALUES (current_setting('app.org_id')::uuid, $1, $2, $3, 'queued')
RETURNING *;

-- name: GetExportJob :one
SELECT * FROM export_job WHERE id = $1;

-- name: UpdateExportJobStatus :exec
UPDATE export_job SET status = $2, object_key = $3, error = $4, updated_at = now()
WHERE id = $1;

-- name: ListExportJobs :many
SELECT * FROM export_job ORDER BY created_at DESC LIMIT $1 OFFSET $2;
