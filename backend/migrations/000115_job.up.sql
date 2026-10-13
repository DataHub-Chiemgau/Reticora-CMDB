-- Migration 000115: the generic job model of JOB-01 (WP-129).
--
-- job: organization (NULL for system jobs), kind, status queued | running
-- | succeeded | failed | cancelled, progress 0–100, params, result, error,
-- idempotency_key, created_by (a surrogate of the user, no reference),
-- retry state (attempts, max_attempts, run_after), timeout per job and the
-- link to a specialised job table (ref_table, ref_id) for the adapters of
-- export and discovery jobs. Row level security after TEN-05: the
-- organization's rows; the system flag only widens SELECT (claiming), system
-- jobs without organization are not writable by the application role.

CREATE TABLE job (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid REFERENCES organization (id) ON DELETE CASCADE,
    kind text NOT NULL CHECK (kind ~ '^[a-z][a-z0-9_.-]*$'),
    status text NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
    progress smallint NOT NULL DEFAULT 0 CHECK (progress BETWEEN 0 AND 100),
    params jsonb NOT NULL DEFAULT '{}'::jsonb,
    result jsonb,
    error text,
    idempotency_key text,
    created_by uuid,
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    max_attempts integer NOT NULL DEFAULT 3 CHECK (max_attempts >= 1),
    run_after timestamptz NOT NULL DEFAULT now(),
    timeout_seconds integer CHECK (timeout_seconds > 0),
    ref_table text,
    ref_id uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    finished_at timestamptz,
    CONSTRAINT job_finished_check CHECK ((status IN ('succeeded', 'failed', 'cancelled')) = (finished_at IS NOT NULL)),
    CONSTRAINT job_ref_check CHECK ((ref_table IS NULL) = (ref_id IS NULL))
);

CREATE UNIQUE INDEX uq_job_idempotency ON job (
    COALESCE(organization_id, '00000000-0000-0000-0000-000000000000'::uuid), kind, idempotency_key)
    WHERE idempotency_key IS NOT NULL;
CREATE UNIQUE INDEX uq_job_ref ON job (ref_table, ref_id) WHERE ref_id IS NOT NULL;
CREATE INDEX idx_job_org_status ON job (organization_id, status, created_at DESC);
CREATE INDEX idx_job_queue ON job (kind, run_after) WHERE status = 'queued';
CREATE INDEX idx_job_running ON job (started_at) WHERE status = 'running';

CREATE TRIGGER trg_job_updated_at BEFORE UPDATE ON job
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE job ENABLE ROW LEVEL SECURITY;
ALTER TABLE job FORCE ROW LEVEL SECURITY;
CREATE POLICY job_isolation ON job FOR ALL
    USING (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid)
    WITH CHECK (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid);
CREATE POLICY job_system_select ON job FOR SELECT
    USING (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid
           OR current_setting('app.system', true) = 'on');
