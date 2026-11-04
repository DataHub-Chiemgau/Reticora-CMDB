-- Migration 000234: system comments of tickets (TKT-01, WP-766). A comment
-- is written by a user (author_type user, author_id set) or by the system
-- (author_type system, no author_id), e.g. when a review item is resolved
-- without an actor (WP-767). Existing comments are user comments through
-- the default. RLS (ticket_comment_tenant_isolation and the scoped
-- policies) and derive_ticket_comment_scope stay as they are.

ALTER TABLE ticket_comment ALTER COLUMN author_id DROP NOT NULL;

ALTER TABLE ticket_comment
    ADD COLUMN author_type TEXT NOT NULL DEFAULT 'user'
    CONSTRAINT ticket_comment_author_type_check CHECK (author_type IN ('user', 'system'));

ALTER TABLE ticket_comment
    ADD CONSTRAINT ticket_comment_author_check CHECK ((author_type = 'user') = (author_id IS NOT NULL));
