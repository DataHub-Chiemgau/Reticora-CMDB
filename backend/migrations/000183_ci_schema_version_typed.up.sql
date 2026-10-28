-- Migration 000183: a CI records the schema version of every write of its
-- typed columns (WP-590, MET-16; F-309).
--
-- ci_schema_version (000107) set ci.schema_version only on INSERT, on a
-- change of ci_type_id or of attributes. A write of a typed column alone
-- (e.g. PATCH hostname after the type moved from v2 to v4) kept the old
-- version, although the write was validated against v4. Every UPDATE that
-- changes ci_type_id, attributes or one of the typed columns of CI-01 (the
-- columns the field metadata checks, ci.typedColumns in backend/internal/
-- ci/validation.go) now sets the current version of the type. A write of
-- fields the attribute schema does not govern alone — system fields such
-- as last_seen_at, first_seen_at, health, version, updated_at, deleted_at,
-- and the assignment and status (client, location, status) — keeps the
-- version; schema_version itself cannot be written directly.
--
-- Organization-global attribute definitions (organization_id set,
-- ci_type_id NULL) stay a validated runtime layer (E-93, transitional rule
-- option 2): the CI service enforces them on every write
-- (citype.ResolveFields), generate_attribute_schema does not materialize
-- them and schema_version references the system base and the type only.

CREATE OR REPLACE FUNCTION ci_schema_version() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'INSERT' OR NEW.ci_type_id IS DISTINCT FROM OLD.ci_type_id
       OR NEW.attributes IS DISTINCT FROM OLD.attributes
       OR NEW.name IS DISTINCT FROM OLD.name
       OR NEW.manufacturer IS DISTINCT FROM OLD.manufacturer
       OR NEW.model IS DISTINCT FROM OLD.model
       OR NEW.serial_number IS DISTINCT FROM OLD.serial_number
       OR NEW.hardware_uuid IS DISTINCT FROM OLD.hardware_uuid
       OR NEW.management_ip IS DISTINCT FROM OLD.management_ip
       OR NEW.primary_mac IS DISTINCT FROM OLD.primary_mac
       OR NEW.hostname IS DISTINCT FROM OLD.hostname
       OR NEW.fqdn IS DISTINCT FROM OLD.fqdn
       OR NEW.os_name IS DISTINCT FROM OLD.os_name
       OR NEW.os_version IS DISTINCT FROM OLD.os_version
       OR NEW.firmware_version IS DISTINCT FROM OLD.firmware_version
       OR NEW.sys_object_id IS DISTINCT FROM OLD.sys_object_id THEN
        NEW.schema_version := COALESCE((SELECT t.schema_version FROM ci_type t WHERE t.id = NEW.ci_type_id), 1);
    ELSE
        NEW.schema_version := OLD.schema_version;
    END IF;
    RETURN NEW;
END
$$;
