-- WP-1152 (TLC-06 part): the deletion state of an organization. An
-- org_admin with a second factor requests the deletion; it falls due after
-- 30 days of grace, in which the request can be cancelled. The purge
-- (purging, purged) follows in WP-1154 and WP-1155.
--
-- deletion_requested_by is the user id of the request without a foreign
-- key, like audit_log.actor_id: the user goes with the organization.
--
-- organization keeps its row level security (ENABLE + FORCE, org_isolation,
-- organization_system_select); no new table, no new policy.

ALTER TABLE organization
    ADD COLUMN deletion_state text NOT NULL DEFAULT 'none',
    ADD COLUMN deletion_requested_at timestamptz,
    ADD COLUMN deletion_due_at timestamptz,
    ADD COLUMN deletion_requested_by uuid,
    ADD CONSTRAINT organization_deletion_state_check
        CHECK (deletion_state IN ('none', 'requested', 'purging', 'purged')),
    -- Without a request the columns are empty, with one both dates are set
    -- and the due date does not precede the request (a request without a
    -- due date would never be purged).
    ADD CONSTRAINT organization_deletion_dates_check
        CHECK ((deletion_state = 'none' AND deletion_requested_at IS NULL AND deletion_due_at IS NULL
                AND deletion_requested_by IS NULL)
            OR (deletion_state <> 'none' AND deletion_requested_at IS NOT NULL AND deletion_due_at IS NOT NULL
                AND deletion_due_at >= deletion_requested_at));

-- The purge job finds the due requests and the purges in progress.
CREATE INDEX idx_organization_deletion_due ON organization (deletion_due_at)
    WHERE deletion_state IN ('requested', 'purging');

-- organization_deletion_transition allows only the transitions of the
-- deletion: none -> requested with a due date at least 30 days after the
-- request (TLC-06), requested -> none (cancel), requested -> purging once
-- deletion_due_at has passed, purging -> purged. Without a change of the
-- state the deletion columns stay as they are.
CREATE FUNCTION organization_deletion_transition() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.deletion_state = OLD.deletion_state THEN
        IF NEW.deletion_requested_at IS DISTINCT FROM OLD.deletion_requested_at
           OR NEW.deletion_due_at IS DISTINCT FROM OLD.deletion_due_at
           OR NEW.deletion_requested_by IS DISTINCT FROM OLD.deletion_requested_by THEN
            RAISE EXCEPTION 'organization deletion: the request of state % cannot be changed', OLD.deletion_state
                USING ERRCODE = 'check_violation';
        END IF;
        RETURN NEW;
    END IF;
    IF OLD.deletion_state = 'none' AND NEW.deletion_state = 'requested' THEN
        IF NEW.deletion_due_at >= NEW.deletion_requested_at + interval '30 days' THEN
            RETURN NEW;
        END IF;
        RAISE EXCEPTION 'organization deletion: the due date must be at least 30 days after the request'
            USING ERRCODE = 'check_violation';
    END IF;
    IF (OLD.deletion_state, NEW.deletion_state) IN (('requested', 'none'), ('purging', 'purged')) THEN
        RETURN NEW;
    END IF;
    IF OLD.deletion_state = 'requested' AND NEW.deletion_state = 'purging' THEN
        IF OLD.deletion_due_at <= now() AND NEW.deletion_due_at IS NOT DISTINCT FROM OLD.deletion_due_at THEN
            RETURN NEW;
        END IF;
        RAISE EXCEPTION 'organization deletion: the grace period ends at %', OLD.deletion_due_at
            USING ERRCODE = 'check_violation';
    END IF;
    RAISE EXCEPTION 'organization deletion: transition % -> % is not allowed', OLD.deletion_state, NEW.deletion_state
        USING ERRCODE = 'check_violation';
END;
$$;

CREATE TRIGGER organization_deletion_transition
    BEFORE UPDATE ON organization
    FOR EACH ROW EXECUTE FUNCTION organization_deletion_transition();
