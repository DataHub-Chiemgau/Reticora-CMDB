-- Migration 000210: the change class template_migration of ci_change
-- (WP-253, OVR-03, MET-16).
--
-- A template migration lifts the assignment of a CI (ci_template, WP-710)
-- from one published version of a template to another and writes the
-- defaults of the new version into the empty attribute fields. It is one
-- ci_change row with change_type template_migration and field_name
-- attributes (old and new attribute map, comment with template, versions,
-- defaults, findings and job). chk_ci_change_type is set again with the
-- list of 000167 plus template_migration. The trigger ci_change_kind
-- (000167) derives the kind updated for it without a change; the writer
-- sets it itself. ci_schema_version() (000183) stays unchanged: the schema
-- version of a CI remains the one of its type.

ALTER TABLE ci_change DROP CONSTRAINT chk_ci_change_type;
ALTER TABLE ci_change ADD CONSTRAINT chk_ci_change_type CHECK (change_type IN (
    'create', 'update', 'delete', 'status_change', 'relationship_change',
    'attribute_change', 'type_changed', 'lifecycle_transition',
    'location_change', 'movement', 'override', 'reconciliation_decision',
    'parent_child_change', 'assignment', 'template_migration'
));
