-- Migration 000374 down: removes the address store of the deletion
-- protocol (WP-1156).
DROP TABLE IF EXISTS org_deletion_notice;
