-- WP-261 (MET-53, DB-03): the publication of a metamodel draft applies all
-- its items in one transaction and materializes the schemas of the affected
-- types once at its end. The triggers that regenerate the generated columns
-- of ci_type (regenerate_attribute_schema) become constraint triggers,
-- DEFERRABLE INITIALLY IMMEDIATE, with unchanged events and functions: a
-- direct write still regenerates at the end of its statement, the
-- publication defers them with SET CONSTRAINTS … DEFERRED before its first
-- item and fires them with SET CONSTRAINTS … IMMEDIATE after its last one.
-- regenerate_attribute_schema raises schema_version only when the schema
-- changed, so every type gets at most one new schema_version per
-- publication, readable before its event and audit entry.
--
-- trg_ci_type_attribute_set_regenerate (000200) is a constraint trigger
-- already, DEFERRABLE INITIALLY DEFERRED: a replaced list of sets is
-- materialized once when the transaction ends, so it stays as it is.
-- Constraint triggers exist only per row (FOR EACH ROW).

DROP TRIGGER trg_attribute_definition_regenerate ON attribute_definition;
CREATE CONSTRAINT TRIGGER trg_attribute_definition_regenerate
    AFTER INSERT OR UPDATE OR DELETE ON attribute_definition
    DEFERRABLE INITIALLY IMMEDIATE
    FOR EACH ROW EXECUTE FUNCTION attribute_definition_regenerate();

DROP TRIGGER trg_ci_type_parent_regenerate ON ci_type;
CREATE CONSTRAINT TRIGGER trg_ci_type_parent_regenerate
    AFTER UPDATE OF parent_type_id ON ci_type
    DEFERRABLE INITIALLY IMMEDIATE
    FOR EACH ROW EXECUTE FUNCTION ci_type_parent_regenerate();

DROP TRIGGER trg_attribute_set_regenerate ON attribute_set;
CREATE CONSTRAINT TRIGGER trg_attribute_set_regenerate
    AFTER UPDATE OF is_active ON attribute_set
    DEFERRABLE INITIALLY IMMEDIATE
    FOR EACH ROW WHEN (OLD.is_active IS DISTINCT FROM NEW.is_active)
    EXECUTE FUNCTION attribute_set_regenerate();
