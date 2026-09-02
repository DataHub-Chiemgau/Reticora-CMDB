-- Migration 000055 down: remove the enterprise CMDB + asset/inventory extension.

-- Permission grants and catalogue entries
DELETE FROM role_permission WHERE permission_key IN (
    'ci_type:manage', 'ci_attribute:manage', 'ci_instance_attribute:manage',
    'relationship_type:manage', 'asset:assign', 'asset:move', 'asset:reserve',
    'inventory:manage', 'lifecycle:manage', 'reconciliation:resolve',
    'override:write', 'saved_view:read', 'saved_view:write'
);
DELETE FROM permission WHERE key IN (
    'ci_type:manage', 'ci_attribute:manage', 'ci_instance_attribute:manage',
    'relationship_type:manage', 'asset:assign', 'asset:move', 'asset:reserve',
    'inventory:manage', 'lifecycle:manage', 'reconciliation:resolve',
    'override:write', 'saved_view:read', 'saved_view:write'
);

DROP TABLE IF EXISTS saved_view;
DROP TABLE IF EXISTS entity_change;
DROP TABLE IF EXISTS source_priority_policy;
DROP TABLE IF EXISTS ci_field_value;
DROP TABLE IF EXISTS composition;
DROP TABLE IF EXISTS reservation;
DROP TABLE IF EXISTS asset_movement;
DROP FUNCTION IF EXISTS reject_asset_movement_mutation;
DROP TABLE IF EXISTS quantity_item;

ALTER TABLE asset DROP COLUMN IF EXISTS location_id;
ALTER TABLE ci DROP COLUMN IF EXISTS location_id;
DROP TABLE IF EXISTS location_node;

ALTER TABLE asset DROP CONSTRAINT IF EXISTS ci_type_lifecycle_definition_fk;
ALTER TABLE ci_type DROP CONSTRAINT IF EXISTS ci_type_lifecycle_definition_fk;
ALTER TABLE asset DROP COLUMN IF EXISTS lifecycle_state;
ALTER TABLE ci DROP COLUMN IF EXISTS lifecycle_state;
DROP TABLE IF EXISTS lifecycle_transition;
DROP TABLE IF EXISTS lifecycle_state;
DROP TABLE IF EXISTS lifecycle_definition;

ALTER TABLE ci_relationship DROP COLUMN IF EXISTS notes;
ALTER TABLE ci_relationship DROP COLUMN IF EXISTS source_system;
ALTER TABLE ci_relationship DROP COLUMN IF EXISTS verification_state;
ALTER TABLE ci_relationship DROP COLUMN IF EXISTS last_seen_at;
ALTER TABLE ci_relationship DROP COLUMN IF EXISTS first_seen_at;
ALTER TABLE ci_relationship DROP COLUMN IF EXISTS confidence;
ALTER TABLE ci_relationship ADD CONSTRAINT ci_relationship_rel_type_check
    CHECK (rel_type IN (
        'connected_to', 'hosted_on', 'depends_on', 'member_of',
        'powers', 'powered_by', 'stores', 'monitors', 'backs_up'
    ));
DROP TABLE IF EXISTS relationship_type;

DROP TABLE IF EXISTS ci_instance_field_definition;

ALTER TABLE ci_type_attribute DROP CONSTRAINT IF EXISTS ci_type_attribute_scope_check;
ALTER TABLE ci_type_attribute DROP COLUMN IF EXISTS reference_target;
ALTER TABLE ci_type_attribute DROP COLUMN IF EXISTS conditional;
ALTER TABLE ci_type_attribute DROP COLUMN IF EXISTS validation;
ALTER TABLE ci_type_attribute DROP COLUMN IF EXISTS ui_group;
ALTER TABLE ci_type_attribute DROP COLUMN IF EXISTS scope;
ALTER TABLE ci_type_attribute DROP COLUMN IF EXISTS description;
ALTER TABLE ci_type_attribute DROP COLUMN IF EXISTS label;
ALTER TABLE ci_type_attribute ADD CONSTRAINT ci_type_attribute_data_type_check
    CHECK (data_type IN ('string', 'number', 'boolean', 'date', 'enum'));

ALTER TABLE ci_type DROP COLUMN IF EXISTS lifecycle_definition_id;
ALTER TABLE ci_type DROP COLUMN IF EXISTS template_key;
ALTER TABLE ci_type DROP COLUMN IF EXISTS is_logical;
ALTER TABLE ci_type DROP COLUMN IF EXISTS discovery_mappings;
ALTER TABLE ci_type DROP COLUMN IF EXISTS compliance_rules;
ALTER TABLE ci_type DROP COLUMN IF EXISTS ui_schema;
ALTER TABLE ci_type DROP COLUMN IF EXISTS allowed_relationship_types;
ALTER TABLE ci_type DROP COLUMN IF EXISTS capabilities;
ALTER TABLE ci_type DROP COLUMN IF EXISTS cloned_from_id;
ALTER TABLE ci_type DROP COLUMN IF EXISTS version;
ALTER TABLE ci_type DROP COLUMN IF EXISTS is_active;
ALTER TABLE ci_type DROP COLUMN IF EXISTS category;
ALTER TABLE ci_type DROP COLUMN IF EXISTS description;

ALTER TABLE asset DROP COLUMN IF EXISTS parent_asset_id;
ALTER TABLE asset DROP COLUMN IF EXISTS asset_type_id;

ALTER TABLE asset ADD CONSTRAINT asset_status_check CHECK (status IN (
    'in_stock', 'assigned', 'maintenance', 'retired', 'disposed', 'lost'
));

ALTER TABLE ci_change DROP CONSTRAINT IF EXISTS chk_ci_change_type;
ALTER TABLE ci_change ADD CONSTRAINT chk_ci_change_type CHECK (change_type IN (
    'create', 'update', 'delete', 'status_change', 'relationship_change', 'attribute_change'
));
