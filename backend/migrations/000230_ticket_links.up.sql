-- Migration 000230: object links of tickets (m:n) and ticket tags from the
-- tag catalog (TKT-01, WP-764, E-39, E-127).
--
-- 1. UNIQUE (id, organization_id) on ticket and review_item, the targets of
--    the composite references below (K19; ci and asset have it since 000056,
--    location since 000194, contract since 000227).
-- 2. ticket_link holds further objects of a ticket next to its primary
--    object (related_ci_id, related_asset_id, which stay the scope anchor of
--    the ticket): one target column per entity type (ci, asset, location,
--    contract, review_item), each with the organization and ON DELETE
--    CASCADE, so a hard delete of the target removes the link. Exactly one
--    target column is set and it matches entity_type; a target is linked at
--    most once per ticket.
-- 3. ticket_tag assigns tags of the catalog (WP-281) to tickets (pattern
--    ci_tag): the ticket with the organization and ON DELETE CASCADE, the
--    tag with the organization without an action (an assigned tag cannot be
--    deleted).
-- 4. client_id, site_id and team_id of both tables follow the ticket
--    (derive_ticket_link_scope, derive_ticket_tag_scope, pattern
--    derive_ticket_comment_scope); propagate_ticket_scope (body of 000064)
--    re-derives them when the ticket changes client, site or team (the
--    trigger ticket_scope_propagation fires on team_id since 000217).
-- 5. Policies like ticket_tenant_isolation (000065: USING with the NULL
--    exception, WITH CHECK without) plus restrictive UPDATE and DELETE
--    policies (pattern 000129), ENABLE + FORCE RLS, TRUNCATE guard (000144).
-- 6. Data migration: the values of ticket.tags become catalog tags (key
--    normalized by tag_normalize_key, empty keys drop, display name de/en
--    the original value, existing keys stay) and ticket_tag rows; then the
--    column ticket.tags goes.

-- ─── 1. Reference targets ──────────────────────────────────────────────────────

ALTER TABLE ticket ADD CONSTRAINT ticket_id_organization_key UNIQUE (id, organization_id);
ALTER TABLE review_item ADD CONSTRAINT review_item_id_organization_key UNIQUE (id, organization_id);

-- ─── 2. Links ──────────────────────────────────────────────────────────────────

CREATE TABLE ticket_link (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    ticket_id       UUID NOT NULL,
    entity_type     TEXT NOT NULL,
    ci_id           UUID,
    asset_id        UUID,
    location_id     UUID,
    contract_id     UUID,
    review_item_id  UUID,
    client_id       UUID,
    site_id         UUID,
    team_id         UUID,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT ticket_link_entity_type_check
        CHECK (entity_type IN ('ci', 'asset', 'location', 'contract', 'review_item')),
    CONSTRAINT ticket_link_target_check CHECK (
        num_nonnulls(ci_id, asset_id, location_id, contract_id, review_item_id) = 1
        AND CASE entity_type
            WHEN 'ci' THEN ci_id IS NOT NULL
            WHEN 'asset' THEN asset_id IS NOT NULL
            WHEN 'location' THEN location_id IS NOT NULL
            WHEN 'contract' THEN contract_id IS NOT NULL
            WHEN 'review_item' THEN review_item_id IS NOT NULL
            ELSE false
        END),
    CONSTRAINT ticket_link_ticket_fkey FOREIGN KEY (ticket_id, organization_id)
        REFERENCES ticket (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT ticket_link_ci_fkey FOREIGN KEY (ci_id, organization_id)
        REFERENCES ci (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT ticket_link_asset_fkey FOREIGN KEY (asset_id, organization_id)
        REFERENCES asset (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT ticket_link_location_fkey FOREIGN KEY (location_id, organization_id)
        REFERENCES location (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT ticket_link_contract_fkey FOREIGN KEY (contract_id, organization_id)
        REFERENCES contract (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT ticket_link_review_item_fkey FOREIGN KEY (review_item_id, organization_id)
        REFERENCES review_item (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT ticket_link_ticket_id_ci_id_key UNIQUE (ticket_id, ci_id),
    CONSTRAINT ticket_link_ticket_id_asset_id_key UNIQUE (ticket_id, asset_id),
    CONSTRAINT ticket_link_ticket_id_location_id_key UNIQUE (ticket_id, location_id),
    CONSTRAINT ticket_link_ticket_id_contract_id_key UNIQUE (ticket_id, contract_id),
    CONSTRAINT ticket_link_ticket_id_review_item_id_key UNIQUE (ticket_id, review_item_id)
);

CREATE INDEX idx_ticket_link_ticket ON ticket_link (ticket_id, created_at, id);
CREATE INDEX idx_ticket_link_ci ON ticket_link (ci_id) WHERE ci_id IS NOT NULL;
CREATE INDEX idx_ticket_link_asset ON ticket_link (asset_id) WHERE asset_id IS NOT NULL;
CREATE INDEX idx_ticket_link_location ON ticket_link (location_id) WHERE location_id IS NOT NULL;
CREATE INDEX idx_ticket_link_contract ON ticket_link (contract_id) WHERE contract_id IS NOT NULL;
CREATE INDEX idx_ticket_link_review_item ON ticket_link (review_item_id) WHERE review_item_id IS NOT NULL;

-- ─── 3. Tags ───────────────────────────────────────────────────────────────────

CREATE TABLE ticket_tag (
    ticket_id       UUID NOT NULL,
    tag_id          UUID NOT NULL,
    organization_id UUID NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    client_id       UUID,
    site_id         UUID,
    team_id         UUID,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT ticket_tag_pkey PRIMARY KEY (ticket_id, tag_id),
    CONSTRAINT ticket_tag_ticket_fkey FOREIGN KEY (ticket_id, organization_id)
        REFERENCES ticket (id, organization_id) ON DELETE CASCADE,
    CONSTRAINT ticket_tag_tag_fkey FOREIGN KEY (tag_id, organization_id)
        REFERENCES tag (id, organization_id)
);

CREATE INDEX idx_ticket_tag_org_tag ON ticket_tag (organization_id, tag_id);

-- ─── 4. Scope derivation and propagation ───────────────────────────────────────

CREATE FUNCTION derive_ticket_link_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.client_id := NULL;
    NEW.site_id := NULL;
    NEW.team_id := NULL;
    SELECT t.client_id, t.site_id, t.team_id INTO NEW.client_id, NEW.site_id, NEW.team_id
      FROM ticket t WHERE t.id = NEW.ticket_id AND t.organization_id = NEW.organization_id;
    RETURN NEW;
END
$$;

CREATE FUNCTION derive_ticket_tag_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.client_id := NULL;
    NEW.site_id := NULL;
    NEW.team_id := NULL;
    SELECT t.client_id, t.site_id, t.team_id INTO NEW.client_id, NEW.site_id, NEW.team_id
      FROM ticket t WHERE t.id = NEW.ticket_id AND t.organization_id = NEW.organization_id;
    RETURN NEW;
END
$$;

CREATE TRIGGER ticket_link_scope BEFORE INSERT OR UPDATE ON ticket_link
    FOR EACH ROW EXECUTE FUNCTION derive_ticket_link_scope();
CREATE TRIGGER ticket_tag_scope BEFORE INSERT OR UPDATE ON ticket_tag
    FOR EACH ROW EXECUTE FUNCTION derive_ticket_tag_scope();

-- A no-op UPDATE re-runs the derivation triggers of the dependent rows
-- (body of 000064 plus ticket_link and ticket_tag).
CREATE OR REPLACE FUNCTION propagate_ticket_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE ticket_comment SET ticket_id = ticket_id WHERE ticket_id = NEW.id;
    UPDATE document_link SET entity_id = entity_id WHERE entity_type = 'ticket' AND entity_id = NEW.id;
    UPDATE ticket_link SET ticket_id = ticket_id WHERE ticket_id = NEW.id;
    UPDATE ticket_tag SET ticket_id = ticket_id WHERE ticket_id = NEW.id;
    RETURN NULL;
END
$$;

-- ─── 5. Policies ───────────────────────────────────────────────────────────────

ALTER TABLE ticket_link ENABLE ROW LEVEL SECURITY;
ALTER TABLE ticket_link FORCE ROW LEVEL SECURITY;
ALTER TABLE ticket_tag ENABLE ROW LEVEL SECURITY;
ALTER TABLE ticket_tag FORCE ROW LEVEL SECURITY;

DO $$
DECLARE
    org_match CONSTANT text := 'organization_id = current_setting(''app.org_id'')::UUID';
    client_all CONSTANT text := 'NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL';
    client_ids CONSTANT text := 'string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[]';
    site_all CONSTANT text := 'NULLIF(current_setting(''app.site_scope'', true), '''') IS NULL';
    site_ids CONSTANT text := 'string_to_array(current_setting(''app.site_scope'', true), '','')::uuid[]';
    team_all CONSTANT text := 'NULLIF(current_setting(''app.team_scope'', true), '''') IS NULL';
    team_ids CONSTANT text := 'string_to_array(current_setting(''app.team_scope'', true), '','')::uuid[]';
    readable text := format('%1$s AND (%2$s OR client_id IS NULL OR client_id = ANY (%3$s))'
                            ' AND (%4$s OR site_id IS NULL OR site_id = ANY (%5$s))'
                            ' AND (%6$s OR team_id IS NULL OR team_id = ANY (%7$s))',
                            org_match, client_all, client_ids, site_all, site_ids, team_all, team_ids);
    writable text := format('%1$s AND (%2$s OR client_id = ANY (%3$s))'
                            ' AND (%4$s OR site_id = ANY (%5$s))'
                            ' AND (%6$s OR team_id = ANY (%7$s))',
                            org_match, client_all, client_ids, site_all, site_ids, team_all, team_ids);
    tbl text;
BEGIN
    FOREACH tbl IN ARRAY ARRAY['ticket_link', 'ticket_tag'] LOOP
        EXECUTE format('CREATE POLICY %I ON %I USING (%s) WITH CHECK (%s)',
                       tbl || '_tenant_isolation', tbl, readable, writable);
        EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE FOR UPDATE USING (%s) WITH CHECK (%s)',
                       tbl || '_scoped_update', tbl, writable, writable);
        EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE FOR DELETE USING (%s)',
                       tbl || '_scoped_delete', tbl, writable);
    END LOOP;
END
$$;

CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON ticket_link
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON ticket_tag
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();

-- ─── 6. Data migration of ticket.tags ──────────────────────────────────────────

CREATE TEMPORARY TABLE ticket_tag_backfill AS
SELECT t.organization_id, t.id AS ticket_id, el.value AS original,
       tag_normalize_key(el.value) AS key, t.created_at, el.ord
FROM ticket t
CROSS JOIN LATERAL unnest(COALESCE(t.tags, '{}'::text[])) WITH ORDINALITY AS el(value, ord)
WHERE el.value IS NOT NULL;

DELETE FROM ticket_tag_backfill WHERE key = '';

-- Missing catalog tags; the display name is the first original text.
INSERT INTO tag (organization_id, key, display_name)
SELECT DISTINCT ON (organization_id, key) organization_id, key, jsonb_build_object('de', original, 'en', original)
FROM ticket_tag_backfill
ORDER BY organization_id, key, created_at, ticket_id, ord
ON CONFLICT (organization_id, key) DO NOTHING;

INSERT INTO ticket_tag (organization_id, ticket_id, tag_id)
SELECT DISTINCT b.organization_id, b.ticket_id, g.id
FROM ticket_tag_backfill b JOIN tag g ON g.organization_id = b.organization_id AND g.key = b.key
ON CONFLICT (ticket_id, tag_id) DO NOTHING;

DROP TABLE ticket_tag_backfill;

ALTER TABLE ticket DROP COLUMN tags;
