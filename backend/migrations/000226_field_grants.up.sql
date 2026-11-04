-- Migration 000226: field permissions (RBA-07, MET-10, WP-264, E-43).
--
-- 1. field_grant assigns a role (role or custom_role, exactly one) a level
--    none|read|write for an object kind, optionally narrowed to a CI type
--    (ci and asset only; system types allowed) and to one target: a field
--    (field_key: core.<column>, attributes.<key>, _instance.<key>), a
--    relationship type (ci and relationship only) or an action (a key of
--    the permission catalogue, none|write only). At most one grant per
--    role, kind, type and target (UNIQUE NULLS NOT DISTINCT, no expression
--    index, K22).
-- 2. object_grant is the exception for a single object (E-43): a role, a
--    custom role or a team (grantee_team_id, so the team-scope rule of the
--    RLS catalogue does not apply: the team is the recipient, not the
--    scope of the row) gets a level for the object. object_id has no
--    foreign key (several object tables); the AFTER DELETE triggers
--    delete_object_grants(<kind>) on ci, asset, ticket, document and
--    ci_relationship remove the grants of a hard-deleted object. Soft-
--    deleted CIs (deleted_at) keep them for a restore. contract gets its
--    trigger with its table (WP-269).
-- 3. field_sensitivity marks a field of an object kind, optionally of a CI
--    type and its subtypes, as sensitive: only a field_grant with exactly
--    this field_key opens it. It applies to system attribute definitions
--    and typed columns as well; attribute_definition stays unchanged.
-- 4. The references to role, custom_role and team carry the organization
--    (K19, pattern 000136) with ON DELETE CASCADE; role and custom_role get
--    UNIQUE (id, organization_id) for them, team has it since 000150.
--    ci_type_id and relationship_type_id may name system rows (the
--    repository checks their visibility under RLS), action names the
--    global permission catalogue, created_by has no foreign key (E-124).
-- 5. Row level security with ENABLE and FORCE and one policy per table on
--    the organization (pattern 000082). The policies read app.org_id with
--    missing_ok like form_layout (000212): the delete trigger of a path
--    without app.org_id sees no rows instead of failing. Truncate guard of
--    WP-352.

ALTER TABLE role ADD CONSTRAINT role_id_organization_key UNIQUE (id, organization_id);
ALTER TABLE custom_role ADD CONSTRAINT custom_role_id_organization_key UNIQUE (id, organization_id);

CREATE TABLE field_grant (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id      UUID NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    role_id              UUID,
    custom_role_id       UUID,
    object_kind          TEXT NOT NULL,
    ci_type_id           UUID REFERENCES ci_type (id) ON DELETE CASCADE,
    field_key            TEXT,
    relationship_type_id UUID REFERENCES relationship_type (id) ON DELETE CASCADE,
    action               TEXT REFERENCES permission (key),
    level                TEXT NOT NULL,
    created_by           UUID,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT field_grant_role_fkey FOREIGN KEY (role_id, organization_id)
        REFERENCES role (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT field_grant_custom_role_fkey FOREIGN KEY (custom_role_id, organization_id)
        REFERENCES custom_role (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT field_grant_one_role CHECK (num_nonnulls(role_id, custom_role_id) = 1),
    CONSTRAINT field_grant_object_kind_check
        CHECK (object_kind IN ('ci', 'asset', 'contract', 'ticket', 'document', 'relationship')),
    CONSTRAINT field_grant_one_target CHECK (num_nonnulls(field_key, relationship_type_id, action) <= 1),
    CONSTRAINT field_grant_type_kind CHECK (ci_type_id IS NULL OR object_kind IN ('ci', 'asset')),
    CONSTRAINT field_grant_relationship_kind
        CHECK (relationship_type_id IS NULL OR object_kind IN ('ci', 'relationship')),
    CONSTRAINT field_grant_level_check CHECK (level IN ('none', 'read', 'write')),
    CONSTRAINT field_grant_action_level CHECK (action IS NULL OR level IN ('none', 'write')),
    CONSTRAINT field_grant_unique UNIQUE NULLS NOT DISTINCT
        (organization_id, role_id, custom_role_id, object_kind, ci_type_id, field_key, relationship_type_id, action)
);
CREATE INDEX idx_field_grant_role ON field_grant (organization_id, role_id);
CREATE INDEX idx_field_grant_custom_role ON field_grant (organization_id, custom_role_id);
CREATE TRIGGER trg_field_grant_updated_at BEFORE UPDATE ON field_grant
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE field_grant ENABLE ROW LEVEL SECURITY;
ALTER TABLE field_grant FORCE ROW LEVEL SECURITY;
CREATE POLICY field_grant_isolation ON field_grant FOR ALL
    USING (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid)
    WITH CHECK (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid);
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON field_grant
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

CREATE TABLE object_grant (
    id                     UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id        UUID NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    grantee_role_id        UUID,
    grantee_custom_role_id UUID,
    grantee_team_id        UUID,
    object_kind            TEXT NOT NULL,
    object_id              UUID NOT NULL,
    level                  TEXT NOT NULL,
    created_by             UUID,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT object_grant_role_fkey FOREIGN KEY (grantee_role_id, organization_id)
        REFERENCES role (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT object_grant_custom_role_fkey FOREIGN KEY (grantee_custom_role_id, organization_id)
        REFERENCES custom_role (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT object_grant_team_fkey FOREIGN KEY (grantee_team_id, organization_id)
        REFERENCES team (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT object_grant_one_grantee
        CHECK (num_nonnulls(grantee_role_id, grantee_custom_role_id, grantee_team_id) = 1),
    CONSTRAINT object_grant_object_kind_check
        CHECK (object_kind IN ('ci', 'asset', 'contract', 'ticket', 'document', 'relationship')),
    CONSTRAINT object_grant_level_check CHECK (level IN ('none', 'read', 'write')),
    CONSTRAINT object_grant_unique UNIQUE NULLS NOT DISTINCT
        (organization_id, grantee_role_id, grantee_custom_role_id, grantee_team_id, object_kind, object_id)
);
CREATE INDEX idx_object_grant_role ON object_grant (organization_id, grantee_role_id);
CREATE INDEX idx_object_grant_custom_role ON object_grant (organization_id, grantee_custom_role_id);
CREATE INDEX idx_object_grant_team ON object_grant (organization_id, grantee_team_id);
CREATE INDEX idx_object_grant_object ON object_grant (organization_id, object_kind, object_id);

ALTER TABLE object_grant ENABLE ROW LEVEL SECURITY;
ALTER TABLE object_grant FORCE ROW LEVEL SECURITY;
CREATE POLICY object_grant_isolation ON object_grant FOR ALL
    USING (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid)
    WITH CHECK (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid);
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON object_grant
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

CREATE TABLE field_sensitivity (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    object_kind     TEXT NOT NULL,
    ci_type_id      UUID REFERENCES ci_type (id) ON DELETE CASCADE,
    field_key       TEXT NOT NULL,
    created_by      UUID,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT field_sensitivity_object_kind_check
        CHECK (object_kind IN ('ci', 'asset', 'contract', 'ticket', 'document', 'relationship')),
    CONSTRAINT field_sensitivity_type_kind CHECK (ci_type_id IS NULL OR object_kind IN ('ci', 'asset')),
    CONSTRAINT field_sensitivity_unique UNIQUE NULLS NOT DISTINCT (organization_id, object_kind, ci_type_id, field_key)
);

ALTER TABLE field_sensitivity ENABLE ROW LEVEL SECURITY;
ALTER TABLE field_sensitivity FORCE ROW LEVEL SECURITY;
CREATE POLICY field_sensitivity_isolation ON field_sensitivity FOR ALL
    USING (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid)
    WITH CHECK (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid);
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON field_sensitivity
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

-- Removes the object grants of a hard-deleted object; TG_ARGV[0] is its
-- object kind.
CREATE FUNCTION delete_object_grants() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM object_grant
     WHERE organization_id = OLD.organization_id AND object_kind = TG_ARGV[0] AND object_id = OLD.id;
    RETURN NULL;
END
$$;
CREATE TRIGGER trg_ci_object_grants AFTER DELETE ON ci
    FOR EACH ROW EXECUTE FUNCTION delete_object_grants('ci');
CREATE TRIGGER trg_asset_object_grants AFTER DELETE ON asset
    FOR EACH ROW EXECUTE FUNCTION delete_object_grants('asset');
CREATE TRIGGER trg_ticket_object_grants AFTER DELETE ON ticket
    FOR EACH ROW EXECUTE FUNCTION delete_object_grants('ticket');
CREATE TRIGGER trg_document_object_grants AFTER DELETE ON document
    FOR EACH ROW EXECUTE FUNCTION delete_object_grants('document');
CREATE TRIGGER trg_ci_relationship_object_grants AFTER DELETE ON ci_relationship
    FOR EACH ROW EXECUTE FUNCTION delete_object_grants('relationship');
