DROP TRIGGER IF EXISTS audit_log_compute_hash ON audit_log;
DROP FUNCTION IF EXISTS audit_log_hash_trigger();
DROP FUNCTION IF EXISTS audit_entry_hash(TIMESTAMPTZ, UUID, TEXT, TEXT, TEXT, UUID, JSONB, TEXT);
