-- WP-038 (AGT-06): endpoint agents enroll with a single-use token that binds
-- them to a client and optionally a site.
--
-- 1. agent_enrollment_token: one token per client (and site); only the
--    SHA-256 hash of the secret is stored. A token is consumed exactly once,
--    in the transaction that registers the agent. The site must belong to
--    the token's client.
-- 2. endpoint_agent: client_id/site_id from the token. Roaming devices
--    (token without site) keep their client; the site is suggested from the
--    network fingerprint (suggested_site_id) and set only on manual
--    confirmation (site_confirmed_at).
-- 3. Policies with client and site predicate (E-09 NULL semantics). Agents
--    enrolled before this migration have no client and stay org-wide.

CREATE TABLE agent_enrollment_token (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
    client_id       UUID NOT NULL REFERENCES client(id) ON DELETE CASCADE,
    site_id         UUID REFERENCES site(id) ON DELETE CASCADE,
    token_hash      TEXT NOT NULL UNIQUE,
    description     TEXT NOT NULL DEFAULT '',
    created_by      TEXT NOT NULL DEFAULT '',
    expires_at      TIMESTAMPTZ NOT NULL,
    used_at         TIMESTAMPTZ,
    used_by_agent   TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_agent_enrollment_token_org ON agent_enrollment_token (organization_id, client_id);

CREATE FUNCTION agent_enrollment_token_site_check() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.site_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM site s WHERE s.id = NEW.site_id AND s.client_id = NEW.client_id
    ) THEN
        RAISE EXCEPTION 'site % does not belong to client %', NEW.site_id, NEW.client_id
            USING ERRCODE = '23514', CONSTRAINT = 'agent_enrollment_token_site_client';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER agent_enrollment_token_site BEFORE INSERT OR UPDATE OF site_id, client_id ON agent_enrollment_token
    FOR EACH ROW EXECUTE FUNCTION agent_enrollment_token_site_check();

ALTER TABLE agent_enrollment_token ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent_enrollment_token FORCE ROW LEVEL SECURITY;

ALTER TABLE endpoint_agent
    ADD COLUMN client_id UUID,
    ADD COLUMN site_id UUID,
    ADD COLUMN suggested_site_id UUID,
    ADD COLUMN network_fingerprint TEXT NOT NULL DEFAULT '',
    ADD COLUMN site_confirmed_at TIMESTAMPTZ;

CREATE INDEX idx_endpoint_agent_client ON endpoint_agent (client_id) WHERE client_id IS NOT NULL;

DO $$
DECLARE
    entry RECORD;
BEGIN
    FOR entry IN
        SELECT * FROM (VALUES
            ('agent_enrollment_token', 'agent_enrollment_token_isolation'),
            ('endpoint_agent', 'endpoint_agent_isolation')
        ) AS t(table_name, policy_name)
    LOOP
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', entry.policy_name, entry.table_name);
        EXECUTE format($p$
            CREATE POLICY %1$I ON %2$I
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
            )$p$, entry.policy_name, entry.table_name);
    END LOOP;
END
$$;
