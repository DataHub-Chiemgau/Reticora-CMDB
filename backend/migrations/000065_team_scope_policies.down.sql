-- Reverts WP-029: policies of migration 000064 (ticket) and organization-only
-- policies (desk, desk_booking, training_assignment). Bookings cancelled by
-- the up migration because they overlapped stay cancelled.

ALTER TABLE desk_booking DROP CONSTRAINT IF EXISTS desk_booking_no_overlap;

DROP POLICY ticket_tenant_isolation ON ticket;
CREATE POLICY ticket_tenant_isolation ON ticket
    USING (
        organization_id = current_setting('app.org_id')::UUID
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL OR client_id IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL OR site_id IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[]))
    )
    WITH CHECK (
        organization_id = current_setting('app.org_id')::UUID
        AND (NULLIF(current_setting('app.client_scope', true), '') IS NULL
             OR client_id = ANY (string_to_array(current_setting('app.client_scope', true), ',')::uuid[]))
        AND (NULLIF(current_setting('app.site_scope', true), '') IS NULL
             OR site_id = ANY (string_to_array(current_setting('app.site_scope', true), ',')::uuid[]))
    );

DO $$
DECLARE
    entry RECORD;
BEGIN
    FOR entry IN
        SELECT * FROM (VALUES
            ('desk', 'desk_isolation'),
            ('desk_booking', 'desk_booking_isolation'),
            ('training_assignment', 'training_assignment_isolation')
        ) AS t(table_name, policy_name)
    LOOP
        EXECUTE format('DROP POLICY %I ON %I', entry.policy_name, entry.table_name);
        EXECUTE format(
            'CREATE POLICY %1$I ON %2$I USING (organization_id = current_setting(''app.org_id'')::UUID) '
            'WITH CHECK (organization_id = current_setting(''app.org_id'')::UUID)',
            entry.policy_name, entry.table_name);
    END LOOP;
END
$$;

DROP TRIGGER IF EXISTS desk_scope ON desk;
DROP TRIGGER IF EXISTS desk_booking_scope ON desk_booking;
DROP TRIGGER IF EXISTS location_room_desk_scope_propagation ON location;
DROP TRIGGER IF EXISTS desk_scope_propagation ON desk;
DROP FUNCTION IF EXISTS derive_desk_scope();
DROP FUNCTION IF EXISTS derive_desk_booking_scope();
DROP FUNCTION IF EXISTS propagate_room_desk_scope();
DROP FUNCTION IF EXISTS propagate_desk_scope();

ALTER TABLE desk DROP COLUMN IF EXISTS client_id, DROP COLUMN IF EXISTS site_id;
ALTER TABLE desk_booking DROP COLUMN IF EXISTS client_id, DROP COLUMN IF EXISTS site_id;
