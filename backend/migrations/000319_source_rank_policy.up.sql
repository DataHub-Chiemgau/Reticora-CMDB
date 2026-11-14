-- Migration 000319: source priority per attribute with numeric ranks
-- (REC-04, WP-1105, E-233 option 1; successor of WP-220).
--
-- 1. source_rank_policy(organization, ci_type NULL, attribute_key, source,
--    rank 1-99, updated_by NULL, updated_at): the rank of an automation
--    source for one attribute, for one CI type or organization-wide
--    (ci_type_id NULL). Only the automation sources of REC-03 are
--    configurable; manual, override and manual_override stay 100 and are
--    not in the CHECK. UNIQUE NULLS NOT DISTINCT (organization_id,
--    ci_type_id, attribute_key, source).
--    ci_type_id references ci_type with a single-column key (system types
--    have no organization, pattern 000283); the trigger
--    source_rank_policy_type refuses a private type of another
--    organization. updated_by references app_user with the organization
--    (Konvention 19).
-- 2. ENABLE and FORCE RLS, one policy over app.org_id with USING and WITH
--    CHECK: an organization-wide setting without client reference, written
--    only by org-wide principals through reconciliation:manage. Truncate
--    guard, index (organization_id, attribute_key).
-- source_priority_policy stays unchanged; it is no longer written.

CREATE TABLE source_rank_policy (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    ci_type_id      uuid NULL REFERENCES ci_type (id) ON DELETE CASCADE,
    attribute_key   text NOT NULL CONSTRAINT source_rank_policy_attribute_key_check
                        CHECK (btrim(attribute_key) <> '' AND length(attribute_key) <= 200),
    source          text NOT NULL CONSTRAINT source_rank_policy_source_check
                        CHECK (source IN ('import', 'workflow', 'redfish', 'ipmi', 'agent', 'snmp', 'api', 'integration', 'wmi', 'ssh', 'nas', 'sweep')),
    rank            integer NOT NULL CONSTRAINT source_rank_policy_rank_check CHECK (rank BETWEEN 1 AND 99),
    updated_by      uuid NULL,
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT source_rank_policy_key UNIQUE NULLS NOT DISTINCT (organization_id, ci_type_id, attribute_key, source),
    CONSTRAINT source_rank_policy_updated_by_fkey FOREIGN KEY (updated_by, organization_id)
        REFERENCES app_user (id, organization_id) ON DELETE SET NULL (updated_by)
);
CREATE INDEX idx_source_rank_policy_attribute ON source_rank_policy (organization_id, attribute_key);
CREATE INDEX idx_source_rank_policy_ci_type ON source_rank_policy (ci_type_id) WHERE ci_type_id IS NOT NULL;
CREATE INDEX idx_source_rank_policy_updated_by ON source_rank_policy (organization_id, updated_by) WHERE updated_by IS NOT NULL;

-- The single-column foreign key bypasses row level security: a type must
-- be a system type or one of the same organization (as 000283).
CREATE FUNCTION source_rank_policy_type() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
    IF NEW.ci_type_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM ci_type t WHERE t.id = NEW.ci_type_id
                     AND (t.organization_id IS NULL OR t.organization_id = NEW.organization_id)) THEN
        RAISE EXCEPTION 'CI type % is not a type of the organization', NEW.ci_type_id
            USING ERRCODE = 'foreign_key_violation', CONSTRAINT = 'source_rank_policy_ci_type_id_fkey';
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER source_rank_policy_type BEFORE INSERT OR UPDATE OF ci_type_id, organization_id
    ON source_rank_policy FOR EACH ROW EXECUTE FUNCTION source_rank_policy_type();
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON source_rank_policy
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

ALTER TABLE source_rank_policy ENABLE ROW LEVEL SECURITY;
ALTER TABLE source_rank_policy FORCE ROW LEVEL SECURITY;
CREATE POLICY source_rank_policy_isolation ON source_rank_policy
    USING (organization_id = current_setting('app.org_id')::uuid)
    WITH CHECK (organization_id = current_setting('app.org_id')::uuid);
