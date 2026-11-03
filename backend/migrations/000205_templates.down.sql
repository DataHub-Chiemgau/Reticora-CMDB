-- Down of 000205: the item, version and template tables go away with their
-- triggers, policies and the seeded system templates, then the trigger
-- functions.

DROP TABLE template_version_item;
DROP TABLE template_version;
DROP TABLE template;
DROP FUNCTION template_version_item_binding();
DROP FUNCTION template_version_set_org();
DROP FUNCTION template_binding();
