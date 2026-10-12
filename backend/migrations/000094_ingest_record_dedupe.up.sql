-- WP-085 (REC-01): the ingest consumer processes every record_id once within
-- 48 hours. A row is written when the record was processed; rows older than
-- 48 hours are removed by the consumer of the organization.
CREATE TABLE ingest_record (
    organization_id uuid NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    record_id uuid NOT NULL,
    collector_id text,
    observed_at timestamptz NOT NULL,
    processed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, record_id)
);
CREATE INDEX ingest_record_processed ON ingest_record (organization_id, processed_at);

ALTER TABLE ingest_record ENABLE ROW LEVEL SECURITY;
ALTER TABLE ingest_record FORCE ROW LEVEL SECURITY;
CREATE POLICY ingest_record_isolation ON ingest_record
    USING (organization_id = current_setting('app.org_id')::uuid)
    WITH CHECK (organization_id = current_setting('app.org_id')::uuid);
