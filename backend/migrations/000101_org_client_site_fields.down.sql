ALTER TABLE site
    DROP CONSTRAINT site_org_client_name_key,
    DROP CONSTRAINT site_timezone_iana,
    DROP COLUMN timezone,
    ADD CONSTRAINT site_org_name_unique UNIQUE (organization_id, name);

ALTER TABLE client
    DROP CONSTRAINT client_org_name_key,
    DROP CONSTRAINT client_timezone_iana,
    DROP COLUMN timezone;

ALTER TABLE organization
    DROP CONSTRAINT organization_locale_bcp47,
    DROP CONSTRAINT organization_currency_iso4217,
    DROP CONSTRAINT organization_timezone_iana,
    DROP COLUMN locale,
    DROP COLUMN currency,
    DROP COLUMN timezone;

DROP FUNCTION reticora_is_iana_timezone(text);
