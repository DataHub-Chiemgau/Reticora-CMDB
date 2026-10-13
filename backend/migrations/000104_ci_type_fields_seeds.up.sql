-- Migration 000104: CI type fields and seeds after MET-01 – MET-03 (WP-115).
--
-- * display_name becomes an i18n map {"de": …, "en": …} (MET-01); existing
--   strings are kept for both languages, the system seeds get German
--   labels.
-- * is_observable (MET-01): true for physical, false for logical types; it
--   controls the offline detector (LCY-05). A trigger derives it, and the
--   display name, when an insert leaves them out.
-- * schema_version (MET-01): version of the generated attribute schema,
--   raised by the schema generator (WP-116/WP-119).
-- * The six logical system types of MET-03. network, vlan and contract are
--   no CI types (DB-05); the physical seeds of migration 000021 stay as they
--   are.

ALTER TABLE ci_type ALTER COLUMN display_name TYPE JSONB USING
    jsonb_build_object('de', COALESCE(display_name, name), 'en', COALESCE(display_name, name));

UPDATE ci_type SET display_name = d.labels
FROM (VALUES
    ('switch',         '{"de": "Switch", "en": "Switch"}'::jsonb),
    ('router',         '{"de": "Router", "en": "Router"}'),
    ('firewall',       '{"de": "Firewall", "en": "Firewall"}'),
    ('access_point',   '{"de": "Access Point", "en": "Access Point"}'),
    ('server',         '{"de": "Server", "en": "Server"}'),
    ('hypervisor',     '{"de": "Hypervisor", "en": "Hypervisor"}'),
    ('vm',             '{"de": "Virtuelle Maschine", "en": "Virtual Machine"}'),
    ('client',         '{"de": "Client", "en": "Client"}'),
    ('pdu',            '{"de": "PDU", "en": "PDU"}'),
    ('ups',            '{"de": "USV", "en": "UPS"}'),
    ('nas',            '{"de": "NAS", "en": "NAS"}'),
    ('storage_array',  '{"de": "Speichersystem", "en": "Storage Array"}'),
    ('printer',        '{"de": "Drucker", "en": "Printer"}'),
    ('ip_phone',       '{"de": "IP-Telefon", "en": "IP Phone"}'),
    ('camera',         '{"de": "Kamera", "en": "Camera"}'),
    ('generic_device', '{"de": "Generisches Gerät", "en": "Generic Device"}'),
    ('patch_panel',    '{"de": "Patchfeld", "en": "Patch Panel"}')
) AS d(key, labels)
WHERE ci_type.organization_id IS NULL AND ci_type.key = d.key;

ALTER TABLE ci_type ADD COLUMN is_observable BOOLEAN;
UPDATE ci_type SET is_observable = NOT is_logical;
ALTER TABLE ci_type ADD COLUMN schema_version INTEGER NOT NULL DEFAULT 1
    CONSTRAINT ci_type_schema_version_check CHECK (schema_version >= 1);

-- Defaults that depend on other columns: a BEFORE trigger runs before the
-- NOT NULL checks.
CREATE FUNCTION ci_type_defaults() RETURNS trigger AS $$
BEGIN
    IF NEW.display_name IS NULL OR NEW.display_name = '{}'::jsonb THEN
        NEW.display_name := jsonb_build_object('de', NEW.name, 'en', NEW.name);
    END IF;
    IF NEW.is_observable IS NULL THEN
        NEW.is_observable := NOT NEW.is_logical;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER trg_ci_type_defaults BEFORE INSERT OR UPDATE ON ci_type
    FOR EACH ROW EXECUTE FUNCTION ci_type_defaults();

ALTER TABLE ci_type ALTER COLUMN display_name SET NOT NULL;
ALTER TABLE ci_type ALTER COLUMN is_observable SET NOT NULL;
-- Only de and en, each a non-empty string.
ALTER TABLE ci_type ADD CONSTRAINT ci_type_display_name_i18n CHECK (
    jsonb_typeof(display_name) = 'object'
    AND display_name ?| ARRAY['de', 'en']
    AND (display_name - 'de' - 'en') = '{}'::jsonb
    AND (NOT display_name ? 'de' OR (jsonb_typeof(display_name -> 'de') = 'string' AND display_name ->> 'de' <> ''))
    AND (NOT display_name ? 'en' OR (jsonb_typeof(display_name -> 'en') = 'string' AND display_name ->> 'en' <> ''))
);

INSERT INTO ci_type (organization_id, key, name, display_name, category, is_system, is_builtin, is_logical, is_observable)
VALUES
    (NULL, 'business_service', 'business_service', '{"de": "Geschäftsdienst", "en": "Business Service"}', 'service', true, true, true, false),
    (NULL, 'application',      'application',      '{"de": "Anwendung", "en": "Application"}',             'software', true, true, true, false),
    (NULL, 'database',         'database',         '{"de": "Datenbank", "en": "Database"}',                'software', true, true, true, false),
    (NULL, 'cluster',          'cluster',          '{"de": "Cluster", "en": "Cluster"}',                   'infrastructure', true, true, true, false),
    (NULL, 'cloud_resource',   'cloud_resource',   '{"de": "Cloud-Ressource", "en": "Cloud Resource"}',    'cloud', true, true, true, false),
    (NULL, 'software',         'software',         '{"de": "Software (Produkt)", "en": "Software (Product)"}', 'software', true, true, true, false)
ON CONFLICT DO NOTHING;
