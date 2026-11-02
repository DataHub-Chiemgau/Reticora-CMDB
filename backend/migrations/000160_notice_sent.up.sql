-- Migration notice_sent: the notices of the scheduler's notice runs
-- (JOB-02, ENT-07, COL-04, WP-425).
--
-- entitlement.notices warns of an entitlement's valid_until 14, 7 and 1 day
-- ahead (entitlement.warning) and reports its end (entitlement.expired);
-- certificate.notices reports collector certificates whose renewal is
-- overdue and the active collector CA from its renewal point
-- (certificate.expiring). Each notice is sent once: the run inserts its row
-- here and appends the event to the outbox in the same transaction. The
-- outbox cannot carry the deduplication: event_outbox has no key for it,
-- and its cleanup removes published events after 30 days.
--
-- kind is the event type; dedup_key names the occasion (entitlement id,
-- valid_until and threshold; the fingerprint of the certificate). The rows
-- belong to the organization the notice went to.

CREATE TABLE notice_sent (
    organization_id UUID NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    kind            TEXT NOT NULL,
    dedup_key       TEXT NOT NULL,
    sent_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT notice_sent_pkey PRIMARY KEY (organization_id, kind, dedup_key)
);

ALTER TABLE notice_sent ENABLE ROW LEVEL SECURITY;
ALTER TABLE notice_sent FORCE ROW LEVEL SECURITY;
CREATE POLICY notice_sent_isolation ON notice_sent FOR ALL
    USING (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid)
    WITH CHECK (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid);

-- Every tenant table carries the TRUNCATE guard of the tenant tables.
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON notice_sent
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();
