-- Down of 000212: the role triggers, the layout table with its triggers and
-- policy, then the trigger function.

DROP TRIGGER trg_custom_role_form_layout ON custom_role;
DROP TRIGGER trg_role_form_layout ON role;
DROP TABLE form_layout;
DROP FUNCTION form_layout_delete_role();
