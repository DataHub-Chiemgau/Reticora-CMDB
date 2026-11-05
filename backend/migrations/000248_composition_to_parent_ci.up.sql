-- WP-742 (AST-05, AST-04; E-26): the legacy composition rows become the
-- child contract on ci.parent_ci_id (000089, 000244). No schema change;
-- composition stays as it is until WP-743.
--
-- * The parent CI of a row is the primary CI of the parent asset
--   (asset.ci_id, API primary_ci_id) and, without one, the only live CI
--   with ci.asset_id = parent asset.
-- * A row with child_ci_id hangs that CI below the parent CI. Its mode is
--   configuration when configuration_only holds, no independently_* flag is
--   set, the child has no asset of its own and the same client as the
--   parent; otherwise standalone. A row with child_asset_id makes the live
--   CIs of the child asset standalone children of the parent CI.
-- * Not mapped, but logged in migration_quarantine (source_table
--   'composition', row_data.rule): a parent asset without a unique CI
--   (parent_asset_without_ci), a child asset without a CI
--   (child_asset_without_ci), a child CI with another parent_ci_id
--   (other_parent), a parent CI of another client that is not org-wide,
--   the rule of checkParents (WP-370; parent_of_other_client), a cycle
--   (ci_parent_not_self, ci_parent_cycle) and a mounted child
--   (rack_mount_composition_child, 000112).
-- * Before a CI changes, its columns parent_ci_id, child_mode and
--   location_id (a configuration child takes its parent's location) are
--   saved as a before image (source_table 'ci', row_data.rule
--   before_image); the down migration restores them.
--
-- Each child is written in its own subtransaction: a refused write (cycle,
-- rack lock) leaves the CI as it was and is logged. Rows are processed in
-- the order they were created, so the oldest row of a child CI wins and a
-- later one with another parent is other_parent.

DO $$
DECLARE
    comp record;
    child record;
    comp_data jsonb;
    parent_ci uuid;
    parent_client uuid;
    candidates uuid[];
    target_mode text;
    mapped integer;
    violated text;
BEGIN
    FOR comp IN SELECT * FROM composition ORDER BY created_at, id LOOP
        comp_data := to_jsonb(comp);

        SELECT c.id, c.client_id INTO parent_ci, parent_client
        FROM asset a
        JOIN ci c ON c.id = a.ci_id AND c.organization_id = a.organization_id AND c.deleted_at IS NULL
        WHERE a.id = comp.parent_asset_id AND a.organization_id = comp.organization_id;
        IF NOT FOUND THEN
            SELECT array_agg(c.id ORDER BY c.id) INTO candidates
            FROM ci c
            WHERE c.asset_id = comp.parent_asset_id AND c.organization_id = comp.organization_id
              AND c.deleted_at IS NULL;
            IF COALESCE(cardinality(candidates), 0) <> 1 THEN
                INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
                VALUES (comp.organization_id, 'composition',
                        comp_data || jsonb_build_object('rule', 'parent_asset_without_ci',
                                                        'candidates', to_jsonb(COALESCE(candidates, '{}'::uuid[]))),
                        'the parent asset has no primary CI and not exactly one CI with ci.asset_id (AST-05): '
                        || 'set its primary CI and hang the child below it through PATCH /api/v1/cis/{id}', '000248');
                CONTINUE;
            END IF;
            SELECT c.id, c.client_id INTO parent_ci, parent_client FROM ci c WHERE c.id = candidates[1];
        END IF;

        mapped := 0;
        FOR child IN
            SELECT c.id, c.client_id, c.asset_id, c.parent_ci_id, c.child_mode, c.location_id
            FROM ci c
            WHERE c.organization_id = comp.organization_id
              AND ((comp.child_ci_id IS NOT NULL AND c.id = comp.child_ci_id)
                OR (comp.child_ci_id IS NULL AND c.asset_id = comp.child_asset_id AND c.deleted_at IS NULL))
            ORDER BY c.id
        LOOP
            mapped := mapped + 1;
            IF child.parent_ci_id IS NOT NULL AND child.parent_ci_id <> parent_ci THEN
                INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
                VALUES (comp.organization_id, 'composition',
                        comp_data || jsonb_build_object('rule', 'other_parent', 'ci_id', child.id,
                                                        'parent_ci_id', parent_ci, 'current_parent_ci_id', child.parent_ci_id),
                        'the child CI already has another parent_ci_id (AST-05); it is kept', '000248');
                CONTINUE;
            END IF;
            IF parent_client IS NOT NULL AND parent_client IS DISTINCT FROM child.client_id THEN
                INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
                VALUES (comp.organization_id, 'composition',
                        comp_data || jsonb_build_object('rule', 'parent_of_other_client', 'ci_id', child.id,
                                                        'parent_ci_id', parent_ci),
                        'the parent CI belongs to another client and is not org-wide (checkParents, WP-370)', '000248');
                CONTINUE;
            END IF;

            IF comp.child_ci_id IS NOT NULL AND comp.configuration_only
               AND NOT (comp.independently_serialized OR comp.independently_assignable
                        OR comp.independently_locatable OR comp.independently_lifecycle_managed)
               AND child.asset_id IS NULL AND child.client_id IS NOT DISTINCT FROM parent_client THEN
                target_mode := 'configuration';
            ELSE
                target_mode := 'standalone';
            END IF;
            IF child.parent_ci_id IS NOT DISTINCT FROM parent_ci AND child.child_mode IS NOT DISTINCT FROM target_mode THEN
                CONTINUE;
            END IF;

            BEGIN
                IF NOT EXISTS (SELECT 1 FROM migration_quarantine q
                                WHERE q.migration = '000248' AND q.source_table = 'ci'
                                  AND q.organization_id = comp.organization_id AND q.row_data->>'id' = child.id::text) THEN
                    INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
                    VALUES (comp.organization_id, 'ci',
                            jsonb_build_object('rule', 'before_image', 'id', child.id, 'parent_ci_id', child.parent_ci_id,
                                               'child_mode', child.child_mode, 'location_id', child.location_id),
                            'before image of the CI columns the composition takeover changes (E-26); '
                            || 'the down migration restores them', '000248');
                END IF;
                UPDATE ci SET parent_ci_id = parent_ci, child_mode = target_mode
                WHERE id = child.id AND organization_id = comp.organization_id;
            EXCEPTION WHEN check_violation OR foreign_key_violation THEN
                GET STACKED DIAGNOSTICS violated = CONSTRAINT_NAME;
                INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
                VALUES (comp.organization_id, 'composition',
                        comp_data || jsonb_build_object('rule', COALESCE(NULLIF(violated, ''), 'refused'), 'ci_id', child.id,
                                                        'parent_ci_id', parent_ci),
                        'the parent_ci_id of the child CI was refused (' || SQLERRM || ')', '000248');
            END;
        END LOOP;

        IF mapped = 0 THEN
            INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
            VALUES (comp.organization_id, 'composition',
                    comp_data || jsonb_build_object('rule', 'child_asset_without_ci', 'parent_ci_id', parent_ci),
                    'the child asset has no live CI that could hang below the parent CI (AST-05)', '000248');
        END IF;
    END LOOP;
END
$$;
