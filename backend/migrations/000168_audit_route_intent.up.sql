-- Migration audit_route_intent: the audit intents of the route audit
-- (AUD-05, AUD-07, WP-504, F-149).
--
-- A write route without a domain recorder stores its intent here in a
-- committed transaction of its own before the handler runs; without the
-- intent the route answers 503 and changes nothing. After a successful
-- response the route audit deletes the intent and appends the audit_log
-- entry in one transaction; after a refused request it deletes the intent
-- without an entry. An intent that is still here after two minutes (crash,
-- failed completion) becomes an audit_log entry with outcome unknown through
-- the scheduler kind audit.route_intents. Completion and the scheduler take
-- the intent with DELETE ... RETURNING, so only one of them writes the entry.
--
-- The row names who (actor), what (route, action, resource, the names of the
-- written fields) and the request; it holds no values (CH16).
--
-- Policies: audit_route_intent_isolation (ALL) rows of app.org_id only;
-- audit_route_intent_system_select (SELECT) lets the system flag find the
-- organizations with due intents and count the pending ones (as migration
-- 000060); the scheduler deletes them per organization in a tenant
-- transaction.

CREATE TABLE audit_route_intent (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization (id) ON DELETE CASCADE,
    request_id      TEXT,
    correlation_id  TEXT,
    route           TEXT NOT NULL,
    action          TEXT NOT NULL,
    resource_type   TEXT NOT NULL,
    resource_key    TEXT,
    actor_id        UUID,
    actor_type      TEXT NOT NULL,
    source          TEXT NOT NULL,
    fields          TEXT[] NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX audit_route_intent_created_idx ON audit_route_intent (organization_id, created_at);

ALTER TABLE audit_route_intent ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_route_intent FORCE ROW LEVEL SECURITY;
CREATE POLICY audit_route_intent_isolation ON audit_route_intent FOR ALL
    USING (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid)
    WITH CHECK (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid);
CREATE POLICY audit_route_intent_system_select ON audit_route_intent FOR SELECT
    USING (organization_id = NULLIF(current_setting('app.org_id', true), '')::uuid
           OR current_setting('app.system', true) = 'on');

-- Every tenant table carries the TRUNCATE guard of the tenant tables.
CREATE TRIGGER reticora_truncate_guard BEFORE TRUNCATE ON audit_route_intent
    FOR EACH STATEMENT EXECUTE FUNCTION reticora_truncate_guard();
