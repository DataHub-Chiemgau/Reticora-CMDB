-- Down of 000207: the copy trigger on template_version, the rule table with
-- its triggers and policies, then the trigger functions.

DROP TRIGGER metamodel_rule_copy_template_version ON template_version;
DROP TABLE metamodel_rule;
DROP FUNCTION metamodel_rule_copy_template_version();
DROP FUNCTION metamodel_rule_binding();
