-- Migration 000132 down: locations and contacts are indexed by the reindex
-- only again.
DROP TRIGGER IF EXISTS contact_search_index ON contact;
DROP FUNCTION IF EXISTS search_index_contact();
DROP TRIGGER IF EXISTS location_search_index ON location;
DROP FUNCTION IF EXISTS search_index_location();
