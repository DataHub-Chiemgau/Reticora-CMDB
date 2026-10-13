-- Migration 000133: saved views after VIE-01 (WP-188): sort and columns of
-- the view and the roles it is shared with. A view is visible to its
-- owner, to everybody when shared, and to the holders of a role in
-- shared_with_roles (standard or custom role).
ALTER TABLE saved_view
    ADD COLUMN sort JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(sort) = 'array'),
    ADD COLUMN columns TEXT[] NOT NULL DEFAULT '{}',
    ADD COLUMN shared_with_roles UUID[] NOT NULL DEFAULT '{}';
CREATE INDEX idx_saved_view_shared_roles ON saved_view USING gin (shared_with_roles);
