-- Reverts WP-296: no history of the stored relationships.

DROP TRIGGER IF EXISTS relationship_change_scope_propagation ON ci;
DROP FUNCTION IF EXISTS propagate_relationship_change_scope();
DROP TABLE IF EXISTS relationship_change;
