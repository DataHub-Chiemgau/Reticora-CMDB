-- Migration 000188: audit entries stay append-only for the runtime role;
-- personal values are sealed in place without re-chaining (AUD-04, SEC-05,
-- WP-680, F-409).
--
-- Since 000125 reject_audit_log_mutation let an UPDATE of the changes pass
-- when the session declared app.audit_pii_migration, and audit_rechain
-- recomputed the hashes of an organization from any entry on. Both were open
-- to the runtime role: it set the setting itself, rewrote an entry and
-- re-chained the organization, and audit-verify reported the chain intact.
--
-- Now:
--   * The setting is gone and audit_rechain is dropped: no role of the
--     application recomputes a stored hash any more. reticora_app loses its
--     UPDATE privilege on audit_log (DELETE stays for the retention run,
--     000119).
--   * Sealing a personal value of an entry (the pii.backfill run, WP-241) goes
--     through audit_seal_pii(entry, changes), a SECURITY DEFINER function of
--     the role reticora_audit_sealer (NOLOGIN, NOSUPERUSER, NOBYPASSRLS, no
--     member: neither reticora_app nor reticora_owner can SET ROLE to it, the
--     startup check reports a login that reaches it). The role may only read
--     the id of an entry and update its changes; row level security binds it
--     to app.org_id of the caller like every other role.
--   * reject_audit_log_mutation accepts an UPDATE only from that role and only
--     when the new changes are the old ones with non-empty string values of
--     personal keys (audit_personal_keys, the keys of pii.PersonalKeys)
--     replaced by references {"$pii": "<id>"} to value subjects of the
--     entry's organization, or by the redaction {"$pii": ""} of a run without
--     vault. Every other column stays as it is, hash and previous_hash
--     included: the chain is never rewritten.
--   * The trigger records the seal: chained_digest is SHA-256 of the canonical
--     JSON the entry was chained with (kept from the first seal on),
--     sealed_refs the references the seals introduced, sealed_hash
--     SHA-256(hash || SHA-256(canonical JSON of the sealed entry)). audit-verify
--     checks a sealed entry's link with chained_digest, its present content
--     with sealed_hash and, with the vault, that the referenced values give
--     back chained_digest (docs/ops/workers.md).

-- The role that owns the seal function. Roles belong to the cluster; another
-- database may have created it already.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'reticora_audit_sealer') THEN
        CREATE ROLE reticora_audit_sealer NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION;
    END IF;
END
$$;
ALTER ROLE reticora_audit_sealer NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION;
-- A migrating login without SUPERUSER must be able to hand the function to
-- the role (as for reticora_owner, 000078).
DO $$
BEGIN
    EXECUTE format('GRANT reticora_audit_sealer TO %I WITH INHERIT FALSE, SET TRUE', current_user);
EXCEPTION
    WHEN OTHERS THEN NULL; -- already a member, or current_user is a superuser
END
$$;

ALTER TABLE audit_log ADD COLUMN chained_digest TEXT;
ALTER TABLE audit_log ADD COLUMN sealed_refs TEXT[];
ALTER TABLE audit_log ADD COLUMN sealed_hash TEXT;

-- audit_entry_digest is SHA-256 of the canonical JSON of an entry,
-- hex-encoded; audit_chain_link is SHA-256(previous_hash || digest) over the
-- binary values. audit_entry_hash keeps its signature and value (000158).
CREATE FUNCTION audit_entry_digest(
    p_organization_id UUID,
    p_seq BIGINT,
    p_timestamp TIMESTAMPTZ,
    p_actor_id UUID,
    p_actor_type TEXT,
    p_action TEXT,
    p_resource_type TEXT,
    p_resource_id UUID,
    p_changes JSONB,
    p_source TEXT,
    p_request_id TEXT,
    p_correlation_id TEXT
) RETURNS TEXT
LANGUAGE sql
IMMUTABLE
AS $$
    SELECT encode(digest(convert_to(
        '{"organization_id":' || to_jsonb(p_organization_id::text)::text
        || ',"seq":' || p_seq::text
        || ',"timestamp":' || to_jsonb(to_char(p_timestamp AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')::text)::text
        || ',"actor_id":' || to_jsonb(COALESCE(p_actor_id::text, '')::text)::text
        || ',"actor_type":' || to_jsonb(p_actor_type::text)::text
        || ',"action":' || to_jsonb(p_action::text)::text
        || ',"resource_type":' || to_jsonb(p_resource_type::text)::text
        || ',"resource_id":' || to_jsonb(COALESCE(p_resource_id::text, '')::text)::text
        || ',"changes":' || COALESCE(p_changes::text, '{}')
        || ',"source":' || to_jsonb(p_source::text)::text
        || ',"request_id":' || to_jsonb(COALESCE(p_request_id, '')::text)::text
        || ',"correlation_id":' || to_jsonb(COALESCE(p_correlation_id, '')::text)::text
        || '}', 'UTF8'), 'sha256'), 'hex');
$$;

CREATE FUNCTION audit_chain_link(p_previous_hash TEXT, p_digest TEXT) RETURNS TEXT
LANGUAGE sql
IMMUTABLE
AS $$
    SELECT encode(digest(decode(p_previous_hash, 'hex') || decode(p_digest, 'hex'), 'sha256'), 'hex');
$$;

CREATE OR REPLACE FUNCTION audit_entry_hash(
    p_organization_id UUID,
    p_seq BIGINT,
    p_timestamp TIMESTAMPTZ,
    p_actor_id UUID,
    p_actor_type TEXT,
    p_action TEXT,
    p_resource_type TEXT,
    p_resource_id UUID,
    p_changes JSONB,
    p_source TEXT,
    p_request_id TEXT,
    p_correlation_id TEXT,
    p_previous_hash TEXT
) RETURNS TEXT
LANGUAGE sql
IMMUTABLE
AS $$
    SELECT audit_chain_link(p_previous_hash, audit_entry_digest(p_organization_id, p_seq, p_timestamp, p_actor_id,
        p_actor_type, p_action, p_resource_type, p_resource_id, p_changes, p_source, p_request_id, p_correlation_id));
$$;

-- The personal field names of pii.PersonalKeys (backend/internal/pii,
-- checked by TestAuditSealOnlyReplacesPersonalValues).
CREATE FUNCTION audit_personal_keys() RETURNS TEXT[]
LANGUAGE sql
IMMUTABLE
AS $$
    SELECT ARRAY['checkout_signature', 'contact_email', 'contact_name', 'contact_phone', 'display_name', 'email',
                 'first_name', 'full_name', 'invitee_email', 'last_name', 'mobile', 'phone', 'return_signature',
                 'signature', 'user_email', 'user_name'];
$$;

-- audit_changes_seal returns the references p_new introduces when it is
-- p_old with string values of personal keys sealed the way pii.SealTx seals
-- them (a key's personal flag passes into arrays, not into objects), and
-- NULL for any other difference. p_personal is the flag of the enclosing
-- key. A reference must name a value subject of p_org the caller can see.
CREATE FUNCTION audit_changes_seal(p_old JSONB, p_new JSONB, p_org UUID, p_personal BOOLEAN) RETURNS TEXT[]
LANGUAGE plpgsql
STABLE
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    refs TEXT[] := '{}';
    part TEXT[];
    k TEXT;
    i INTEGER;
    ref TEXT;
BEGIN
    IF p_old IS NULL OR p_new IS NULL THEN
        RETURN CASE WHEN p_old IS NULL AND p_new IS NULL THEN refs END;
    END IF;
    -- The text, not jsonb equality: 2.50 and 2.5 are equal numbers but hash
    -- differently.
    IF p_old::text = p_new::text THEN
        RETURN refs;
    END IF;
    IF jsonb_typeof(p_old) = 'string' THEN
        IF NOT p_personal OR p_old #>> '{}' = '' OR jsonb_typeof(p_new) <> 'object'
           OR (SELECT count(*) FROM jsonb_object_keys(p_new)) <> 1
           OR jsonb_typeof(p_new -> '$pii') IS DISTINCT FROM 'string' THEN
            RETURN NULL;
        END IF;
        ref := p_new ->> '$pii';
        IF ref <> '' AND (ref !~ '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$'
                          OR NOT EXISTS (SELECT 1 FROM pii_subject s
                                          WHERE s.surrogate_id = ref::uuid AND s.organization_id = p_org
                                            AND s.kind = 'value')) THEN
            RETURN NULL;
        END IF;
        RETURN ARRAY[ref];
    END IF;
    IF jsonb_typeof(p_old) = 'object' AND jsonb_typeof(p_new) = 'object' THEN
        -- A reference stays as it is (equal text above).
        IF p_old ? '$pii' AND (SELECT count(*) FROM jsonb_object_keys(p_old)) = 1 THEN
            RETURN NULL;
        END IF;
        IF ARRAY(SELECT jsonb_object_keys(p_old) ORDER BY 1) IS DISTINCT FROM ARRAY(SELECT jsonb_object_keys(p_new) ORDER BY 1) THEN
            RETURN NULL;
        END IF;
        FOR k IN SELECT jsonb_object_keys(p_old) LOOP
            part := audit_changes_seal(p_old -> k, p_new -> k, p_org, lower(k) = ANY (audit_personal_keys()));
            IF part IS NULL THEN
                RETURN NULL;
            END IF;
            refs := refs || part;
        END LOOP;
        RETURN refs;
    END IF;
    IF jsonb_typeof(p_old) = 'array' AND jsonb_typeof(p_new) = 'array'
       AND jsonb_array_length(p_old) = jsonb_array_length(p_new) THEN
        FOR i IN 0 .. jsonb_array_length(p_old) - 1 LOOP
            part := audit_changes_seal(p_old -> i, p_new -> i, p_org, p_personal);
            IF part IS NULL THEN
                RETURN NULL;
            END IF;
            refs := refs || part;
        END LOOP;
        RETURN refs;
    END IF;
    RETURN NULL;
END
$$;

-- audit_unseal puts the values of p_values ({"<id>": "<value>"}) back in
-- place of their references: audit-verify rebuilds the changes an entry was
-- chained with from the vault.
CREATE FUNCTION audit_unseal(p_changes JSONB, p_values JSONB) RETURNS JSONB
LANGUAGE plpgsql
IMMUTABLE
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    out JSONB;
BEGIN
    CASE jsonb_typeof(p_changes)
    WHEN 'object' THEN
        IF p_changes ? '$pii' AND (SELECT count(*) FROM jsonb_object_keys(p_changes)) = 1
           AND jsonb_typeof(p_changes -> '$pii') = 'string' AND p_values ? (p_changes ->> '$pii') THEN
            RETURN p_values -> (p_changes ->> '$pii');
        END IF;
        SELECT COALESCE(jsonb_object_agg(key, audit_unseal(value, p_values)), '{}'::jsonb) INTO out
          FROM jsonb_each(p_changes);
        RETURN out;
    WHEN 'array' THEN
        SELECT COALESCE(jsonb_agg(audit_unseal(value, p_values) ORDER BY n), '[]'::jsonb) INTO out
          FROM jsonb_array_elements(p_changes) WITH ORDINALITY AS e(value, n);
        RETURN out;
    ELSE
        RETURN p_changes;
    END CASE;
END
$$;

CREATE OR REPLACE FUNCTION reject_audit_log_mutation() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    refs TEXT[];
BEGIN
    IF TG_OP = 'DELETE'
       AND current_setting('app.audit_retention', true) = 'on'
       AND OLD.timestamp < now() - interval '10 years' THEN
        RETURN OLD;
    END IF;
    IF TG_OP = 'UPDATE'
       AND current_user = 'reticora_audit_sealer'
       AND (NEW.id, NEW.organization_id, NEW.seq, NEW.timestamp, NEW.actor_id, NEW.actor_type, NEW.action,
            NEW.resource_type, NEW.resource_id, NEW.source, NEW.request_id, NEW.correlation_id, NEW.created_at,
            NEW.previous_hash, NEW.hash, NEW.chained_digest, NEW.sealed_refs, NEW.sealed_hash)
           IS NOT DISTINCT FROM
           (OLD.id, OLD.organization_id, OLD.seq, OLD.timestamp, OLD.actor_id, OLD.actor_type, OLD.action,
            OLD.resource_type, OLD.resource_id, OLD.source, OLD.request_id, OLD.correlation_id, OLD.created_at,
            OLD.previous_hash, OLD.hash, OLD.chained_digest, OLD.sealed_refs, OLD.sealed_hash) THEN
        refs := audit_changes_seal(OLD.changes, NEW.changes, OLD.organization_id, false);
        IF cardinality(refs) > 0 THEN
            NEW.chained_digest := COALESCE(OLD.chained_digest,
                audit_entry_digest(OLD.organization_id, OLD.seq, OLD.timestamp, OLD.actor_id, OLD.actor_type,
                    OLD.action, OLD.resource_type, OLD.resource_id, OLD.changes, OLD.source, OLD.request_id,
                    OLD.correlation_id));
            NEW.sealed_refs := COALESCE(OLD.sealed_refs, '{}') || refs;
            NEW.sealed_hash := audit_chain_link(OLD.hash,
                audit_entry_digest(NEW.organization_id, NEW.seq, NEW.timestamp, NEW.actor_id, NEW.actor_type,
                    NEW.action, NEW.resource_type, NEW.resource_id, NEW.changes, NEW.source, NEW.request_id,
                    NEW.correlation_id));
            RETURN NEW;
        END IF;
    END IF;
    RAISE EXCEPTION 'audit_log rows are append-only';
END;
$$;

-- A new entry is never sealed.
CREATE OR REPLACE FUNCTION audit_log_chain_trigger() RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    last_seq BIGINT;
    last_hash TEXT;
BEGIN
    PERFORM pg_advisory_xact_lock(1382380649, audit_org_lock_key(NEW.organization_id));
    SELECT seq, hash INTO last_seq, last_hash
    FROM audit_log
    WHERE organization_id = NEW.organization_id
    ORDER BY seq DESC
    LIMIT 1;
    NEW.seq := COALESCE(last_seq, 0) + 1;
    NEW.previous_hash := COALESCE(last_hash, repeat('0', 64));
    NEW.hash := audit_entry_hash(NEW.organization_id, NEW.seq, NEW.timestamp, NEW.actor_id, NEW.actor_type,
                                 NEW.action, NEW.resource_type, NEW.resource_id, NEW.changes,
                                 NEW.source, NEW.request_id, NEW.correlation_id, NEW.previous_hash);
    NEW.chained_digest := NULL;
    NEW.sealed_refs := NULL;
    NEW.sealed_hash := NULL;
    RETURN NEW;
END;
$$;

DROP FUNCTION audit_rechain(UUID, BIGINT);

-- audit_seal_pii replaces the changes of one entry of the caller's
-- organization with their sealed form; the trigger above refuses anything
-- else. It returns whether the entry was found.
CREATE FUNCTION audit_seal_pii(p_entry UUID, p_changes JSONB) RETURNS BOOLEAN
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
    UPDATE audit_log SET changes = p_changes WHERE id = p_entry;
    RETURN FOUND;
END
$$;

GRANT USAGE ON SCHEMA public TO reticora_audit_sealer;
GRANT SELECT (id, organization_id), UPDATE (changes) ON audit_log TO reticora_audit_sealer;
GRANT SELECT (surrogate_id, organization_id, kind) ON pii_subject TO reticora_audit_sealer;
REVOKE ALL ON FUNCTION audit_seal_pii(UUID, JSONB) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION audit_seal_pii(UUID, JSONB) TO reticora_app;
-- The owner of a function needs CREATE on its schema; the role keeps it only
-- for the hand-over. The grants above pass to the new owner.
GRANT CREATE ON SCHEMA public TO reticora_audit_sealer;
ALTER FUNCTION audit_seal_pii(UUID, JSONB) OWNER TO reticora_audit_sealer;
REVOKE CREATE ON SCHEMA public FROM reticora_audit_sealer;

REVOKE UPDATE ON audit_log FROM reticora_app;
