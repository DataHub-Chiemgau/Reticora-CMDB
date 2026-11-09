-- Down of 000276: idx_ci_list_sort without owner_team_id, as 000156 built it.
DROP INDEX IF EXISTS idx_ci_list_sort;
CREATE INDEX idx_ci_list_sort ON ci (organization_id, created_at DESC, id DESC)
    INCLUDE (client_id, site_id) WHERE deleted_at IS NULL;
