-- Restores the ordinary AFTER … FOR EACH ROW regeneration triggers of
-- 000105, 000197 and 000200.

DROP TRIGGER trg_attribute_set_regenerate ON attribute_set;
CREATE TRIGGER trg_attribute_set_regenerate AFTER UPDATE OF is_active ON attribute_set
    FOR EACH ROW WHEN (OLD.is_active IS DISTINCT FROM NEW.is_active)
    EXECUTE FUNCTION attribute_set_regenerate();

DROP TRIGGER trg_ci_type_parent_regenerate ON ci_type;
CREATE TRIGGER trg_ci_type_parent_regenerate AFTER UPDATE OF parent_type_id ON ci_type
    FOR EACH ROW EXECUTE FUNCTION ci_type_parent_regenerate();

DROP TRIGGER trg_attribute_definition_regenerate ON attribute_definition;
CREATE TRIGGER trg_attribute_definition_regenerate
    AFTER INSERT OR UPDATE OR DELETE ON attribute_definition
    FOR EACH ROW EXECUTE FUNCTION attribute_definition_regenerate();
