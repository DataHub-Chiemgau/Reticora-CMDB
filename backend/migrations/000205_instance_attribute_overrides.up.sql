-- Migration 000205: instance attributes carry no manual override (WP-714,
-- MET-14, CI-04, OVR-02).
--
-- Before WP-714 a manual change of an instance attribute (PATCH, PUT
-- /cis/{id}/instance-attributes) recorded a protected override in
-- ci_field_value, projected into attributes._overrides. Instance attributes
-- are protected by the central write decision now (ReasonInstanceAttribute),
-- not by overrides. For every field that is an instance attribute of its CI:
--   * a row without an observed value (discovered_at IS NULL) is removed;
--   * otherwise the override columns are emptied and protected is reset,
--     so the provenance of the observed value stays (WP-056).
-- attributes._instance stays unchanged. The statement triggers
-- trg_ci_field_value_project_update / trg_ci_field_value_project_delete
-- (000135, ci_field_value_project_rows) update attributes._overrides.
-- Data migration only: no DDL, no new table, RLS and policies unchanged.

DELETE FROM ci_field_value v
USING ci_instance_field_definition d
WHERE d.ci_id = v.ci_id AND d.name = v.field_name
  AND v.discovered_at IS NULL;

UPDATE ci_field_value v SET
    override_value = NULL, override_author = NULL,
    override_reason = NULL, override_at = NULL, protected = false
FROM ci_instance_field_definition d
WHERE d.ci_id = v.ci_id AND d.name = v.field_name
  AND (v.override_at IS NOT NULL OR v.override_value IS NOT NULL
       OR v.override_author IS NOT NULL OR v.override_reason IS NOT NULL OR v.protected);
