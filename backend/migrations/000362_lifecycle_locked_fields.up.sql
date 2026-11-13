-- Migration 000362: fields a lifecycle state locks (LCY-04, WP-1103, E-232
-- option 1).
--
-- lifecycle_state.locked_fields lists the attribute keys and typed CI
-- columns a CI in this state may not change: the CI service derives a
-- readonly catalog rule per field with the condition lifecycle_state =
-- <state> when it resolves the rules of the CI (citype.ResolveFieldsAndRules),
-- and the write decision of the automation sources (override.
-- DecideAutomatedWrite) turns a differing value of a source below rank 92
-- into a conflict with reason lifecycle_locked. No new table, row level
-- security unchanged.

ALTER TABLE lifecycle_state
    ADD COLUMN locked_fields jsonb NOT NULL DEFAULT '[]'::jsonb
        CONSTRAINT lifecycle_state_locked_fields_check CHECK (jsonb_typeof(locked_fields) = 'array');
