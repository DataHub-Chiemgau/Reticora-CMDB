-- Down of 000355.
DROP TABLE IF EXISTS compliance_rule_control;
DROP TABLE IF EXISTS compliance_control;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'compliance_rule_id_organization_key' AND conrelid = 'compliance_rule'::regclass)
       AND NOT EXISTS (SELECT 1 FROM pg_constraint WHERE contype = 'f' AND confrelid = 'compliance_rule'::regclass) THEN
        ALTER TABLE compliance_rule DROP CONSTRAINT compliance_rule_id_organization_key;
    END IF;
END
$$;
