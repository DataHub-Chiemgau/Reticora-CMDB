-- Migration 000304 (provisional number): nameserver sets (IPM-05, WP-905).
-- A nameserver set is a reusable list of DNS servers of the organization,
-- as the nameserver sets of phpIPAM are; a subnet references one optionally
-- and its effective DNS servers are its own dns_servers, else the servers
-- of its set.
--
-- * nameserver_set(organization, name unique per organization, servers
--   inet[] with 1 to 8 entries, description). A set belongs to the whole
--   organization: every principal of it reads the sets, only principals
--   without client and site restriction create, change or delete them
--   (restrictive policies as vrf_scoped_* of 000148, E-09): for a scoped
--   principal an INSERT fails WITH CHECK (42501, 403 at the API), UPDATE and
--   DELETE find no row (404).
-- * subnet.nameserver_set_id NULL with the composite foreign key on
--   (id, organization_id) (Konvention 19); a set that subnets use cannot be
--   deleted (RESTRICT, 409 at the API).

CREATE TABLE nameserver_set (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    name            text NOT NULL CONSTRAINT nameserver_set_name_check CHECK (btrim(name) <> ''),
    servers         inet[] NOT NULL CONSTRAINT nameserver_set_servers_check CHECK (cardinality(servers) BETWEEN 1 AND 8),
    description     text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT nameserver_set_name_key UNIQUE (organization_id, name),
    CONSTRAINT nameserver_set_id_organization_key UNIQUE (id, organization_id)
);

CREATE TRIGGER trg_nameserver_set_updated_at BEFORE UPDATE ON nameserver_set
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON nameserver_set
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

ALTER TABLE nameserver_set ENABLE ROW LEVEL SECURITY;
ALTER TABLE nameserver_set FORCE ROW LEVEL SECURITY;

CREATE POLICY nameserver_set_isolation ON nameserver_set
    USING (organization_id = current_setting('app.org_id')::uuid)
    WITH CHECK (organization_id = current_setting('app.org_id')::uuid);
CREATE POLICY nameserver_set_scoped_insert ON nameserver_set AS RESTRICTIVE FOR INSERT
    WITH CHECK (organization_id = current_setting('app.org_id')::uuid
        AND NULLIF(current_setting('app.client_scope', true), '') IS NULL
        AND NULLIF(current_setting('app.site_scope', true), '') IS NULL);
CREATE POLICY nameserver_set_scoped_update ON nameserver_set AS RESTRICTIVE FOR UPDATE
    USING (organization_id = current_setting('app.org_id')::uuid
        AND NULLIF(current_setting('app.client_scope', true), '') IS NULL
        AND NULLIF(current_setting('app.site_scope', true), '') IS NULL)
    WITH CHECK (organization_id = current_setting('app.org_id')::uuid
        AND NULLIF(current_setting('app.client_scope', true), '') IS NULL
        AND NULLIF(current_setting('app.site_scope', true), '') IS NULL);
CREATE POLICY nameserver_set_scoped_delete ON nameserver_set AS RESTRICTIVE FOR DELETE
    USING (organization_id = current_setting('app.org_id')::uuid
        AND NULLIF(current_setting('app.client_scope', true), '') IS NULL
        AND NULLIF(current_setting('app.site_scope', true), '') IS NULL);

ALTER TABLE subnet ADD COLUMN nameserver_set_id uuid;
ALTER TABLE subnet ADD CONSTRAINT subnet_nameserver_set_fkey FOREIGN KEY (nameserver_set_id, organization_id)
    REFERENCES nameserver_set (id, organization_id) ON DELETE RESTRICT;
CREATE INDEX idx_subnet_nameserver_set ON subnet (nameserver_set_id) WHERE nameserver_set_id IS NOT NULL;
