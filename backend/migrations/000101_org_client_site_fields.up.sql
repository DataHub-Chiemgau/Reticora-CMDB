-- WP-107 (LOC-01, LOC-02, LOC-03): validated time zone, currency and locale
-- of the organization; client names unique per organization with
-- external_ref and optional time zone; site names unique per
-- (organization, client) with optional time zone.

-- IANA time zone names as known to the database server. pg_timezone_names
-- lists the tz database; abbreviations and POSIX strings are not names.
CREATE FUNCTION reticora_is_iana_timezone(tz text) RETURNS boolean
    LANGUAGE sql STABLE
    SET search_path = pg_catalog
AS $$ SELECT tz IS NOT NULL AND tz <> '' AND EXISTS (SELECT 1 FROM pg_catalog.pg_timezone_names WHERE name = tz) $$;

ALTER TABLE organization
    ADD COLUMN timezone text NOT NULL DEFAULT 'UTC',
    ADD COLUMN currency text NOT NULL DEFAULT 'EUR',
    ADD COLUMN locale   text NOT NULL DEFAULT 'de-DE',
    ADD CONSTRAINT organization_timezone_iana CHECK (reticora_is_iana_timezone(timezone)),
    -- ISO 4217 alphabetic code; the server checks it against the code list.
    ADD CONSTRAINT organization_currency_iso4217 CHECK (currency ~ '^[A-Z]{3}$'),
    -- BCP 47 language tag (language[-script][-region]...); the server
    -- canonicalizes it.
    ADD CONSTRAINT organization_locale_bcp47 CHECK (locale ~ '^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$');

-- Existing clients with the same name in one organization keep their
-- first name; later ones get their slug appended so the new unique key
-- holds (the slug is unique per organization already).
UPDATE client c SET name = c.name || ' (' || c.slug || ')'
WHERE EXISTS (SELECT 1 FROM client o WHERE o.organization_id = c.organization_id AND o.name = c.name
              AND (o.created_at, o.id) < (c.created_at, c.id));

ALTER TABLE client
    ADD COLUMN timezone text,
    ADD CONSTRAINT client_timezone_iana CHECK (timezone IS NULL OR reticora_is_iana_timezone(timezone)),
    ADD CONSTRAINT client_org_name_key UNIQUE (organization_id, name);

ALTER TABLE site
    ADD COLUMN timezone text,
    ADD CONSTRAINT site_timezone_iana CHECK (timezone IS NULL OR reticora_is_iana_timezone(timezone)),
    DROP CONSTRAINT site_org_name_unique,
    -- MSPs keep equal site names for different clients (LOC-03).
    ADD CONSTRAINT site_org_client_name_key UNIQUE (organization_id, client_id, name);
