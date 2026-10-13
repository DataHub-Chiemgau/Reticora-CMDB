CREATE OR REPLACE FUNCTION reject_audit_log_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'audit_log rows are append-only';
END;
$$ LANGUAGE plpgsql;
