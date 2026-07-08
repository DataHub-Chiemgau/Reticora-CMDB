-- name: GetWebhookSubscription :one
SELECT * FROM webhook_subscription WHERE id = $1;

-- name: ListWebhookSubscriptions :many
SELECT * FROM webhook_subscription WHERE is_active = true ORDER BY name;

-- name: CreateWebhookSubscription :one
INSERT INTO webhook_subscription (organization_id, name, target_url, secret, event_types, is_active)
VALUES (current_setting('app.org_id')::uuid, $1, $2, $3, $4, $5)
RETURNING *;

-- name: UpdateWebhookSubscription :one
UPDATE webhook_subscription SET
    name = COALESCE(sqlc.narg('name'), name),
    target_url = COALESCE(sqlc.narg('target_url'), target_url),
    event_types = COALESCE(sqlc.narg('event_types'), event_types),
    is_active = COALESCE(sqlc.narg('is_active'), is_active),
    updated_at = now()
WHERE id = $1 RETURNING *;

-- name: DeleteWebhookSubscription :exec
DELETE FROM webhook_subscription WHERE id = $1;

-- name: CreateWebhookDelivery :one
INSERT INTO webhook_delivery (
    organization_id, subscription_id, event_type, event_id, payload,
    attempt, status, next_attempt_at
) VALUES (
    current_setting('app.org_id')::uuid, $1, $2, $3, $4, $5, $6, $7
) RETURNING *;

-- name: ListPendingDeliveries :many
SELECT * FROM webhook_delivery
WHERE status IN ('pending', 'failed') AND next_attempt_at <= now()
ORDER BY next_attempt_at
LIMIT $1;

-- name: UpdateDeliveryStatus :exec
UPDATE webhook_delivery SET
    status = $2, attempt = $3, last_status_code = $4, last_error = $5,
    next_attempt_at = $6
WHERE id = $1;
