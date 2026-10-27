-- WP-506 (EVT-01, F-151, F-320): a webhook delivery of an outbox event
-- names the event. The outbox publisher hands an event to the dispatcher
-- again until the hand-off succeeds (at least once); the unique index makes
-- the hand-off idempotent per event and subscription, so a repeated hand-off
-- creates no second delivery. event_sequence and event_entity_seq keep the
-- numbering of the event for the headers X-Reticora-Sequence and
-- X-Reticora-Entity-Seq of every attempt, also after the outbox row is gone.
-- Deliveries without an event (test sends, direct dispatches) keep NULL and
-- are not restricted by the index.

ALTER TABLE webhook_delivery
    ADD COLUMN event_id UUID,
    ADD COLUMN event_sequence BIGINT,
    ADD COLUMN event_entity_seq BIGINT;

CREATE UNIQUE INDEX uq_webhook_delivery_event_subscription
    ON webhook_delivery (event_id, subscription_id);
