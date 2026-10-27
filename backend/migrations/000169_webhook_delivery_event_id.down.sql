-- WP-506 down: deliveries without the event they deliver.

DROP INDEX uq_webhook_delivery_event_subscription;

ALTER TABLE webhook_delivery
    DROP COLUMN event_entity_seq,
    DROP COLUMN event_sequence,
    DROP COLUMN event_id;
