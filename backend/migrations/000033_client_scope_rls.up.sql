-- Migration 000033: enforce app.client_scope in RLS policies
--
-- Until now every tenant policy only checked organization_id against
-- app.org_id. app.client_scope was set by the WithTenant helpers but never
-- evaluated by any policy — client (sub-tenant) scoping was enforced in
-- application code only. This migration rewrites the policies of all tables
-- that carry a client_id column so that, when app.client_scope is set to a
-- comma-separated list of client UUIDs, rows owned by other clients are
-- invisible at the database level. Rows with client_id IS NULL remain
-- visible to every client scope within the organization (shared records).
-- When app.client_scope is unset or empty the policies reduce to the
-- previous organization-only check, so org-wide users are unaffected.

-- ─── Helper: parsed client scope ────────────────────────────────────────────────
-- current_setting('app.client_scope', true) returns NULL when unset and ''
-- when set empty; both must disable client filtering. string_to_array would
-- turn '' into '{}'::uuid[] which accidentally matches nothing, hence the
-- NULLIF guard in every expression below:
--
--   client_id IS NULL
--   OR client_id = ANY (string_to_array(NULLIF(current_setting('app.client_scope', true), ''), ',')::uuid[])
--
-- When the scope is empty the ANY argument is NULL, so the explicit
-- NULLIF(... ) IS NULL arm is required: `x = ANY (NULL)` evaluates to NULL
-- (not true) even for scoped rows and would hide them from org-wide users.

-- ─── 1. Client table: scoped by its own id ──────────────────────────────────────

DROP POLICY IF EXISTS client_isolation ON client;
CREATE POLICY client_isolation ON client
    USING (
        organization_id = current_setting('app.org_id')::UUID
        AND (
            NULLIF(current_setting('app.client_scope', true), '') IS NULL
            OR id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[])
        )
    )
    WITH CHECK (
        organization_id = current_setting('app.org_id')::UUID
        AND (
            NULLIF(current_setting('app.client_scope', true), '') IS NULL
            OR id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[])
        )
    );

-- ─── 2. Client-scoped tables keyed on client_id ────────────────────────────────

DO $$
DECLARE
    entry RECORD;
BEGIN
    FOR entry IN
        SELECT * FROM (VALUES
            ('site', 'site_isolation'),
            ('ci', 'ci_isolation'),
            ('collector', 'collector_isolation'),
            ('subnet', 'org_isolation'),
            ('contact', 'org_isolation'),
            ('credential', 'credential_isolation')
        ) AS t(table_name, policy_name)
    LOOP
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', entry.policy_name, entry.table_name);
        EXECUTE format(
            'CREATE POLICY %I ON %I USING ('
            'organization_id = current_setting(''app.org_id'')::UUID '
            'AND (NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL '
            'OR client_id IS NULL '
            'OR client_id = ANY (string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[]))'
            ') WITH CHECK ('
            'organization_id = current_setting(''app.org_id'')::UUID '
            'AND (NULLIF(current_setting(''app.client_scope'', true), '''') IS NULL '
            'OR client_id IS NULL '
            'OR client_id = ANY (string_to_array(current_setting(''app.client_scope'', true), '','')::uuid[]))'
            ')',
            entry.policy_name, entry.table_name
        );
    END LOOP;
END
$$;
