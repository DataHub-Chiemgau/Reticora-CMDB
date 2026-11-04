-- Down of 000233: the triggers and functions of the identifiers and the
-- asset documents, the column source of the field rows and
-- search_identifier. The asset documents and the attribute field rows of
-- the backfill stay; a reindex rebuilds them like before.

DROP TRIGGER search_identifier_ip_write ON ip_address;
DROP TRIGGER search_identifier_ip_update ON ip_address;
DROP FUNCTION search_identifier_ip();
DROP TRIGGER search_identifier_interface_write ON network_interface;
DROP TRIGGER search_identifier_interface_update ON network_interface;
DROP FUNCTION search_identifier_interface();
DROP TRIGGER trg_asset_sync_search_index ON asset;
DROP FUNCTION search_index_asset();
DROP FUNCTION search_identifier_rows(uuid);
DROP FUNCTION search_mac_norm(macaddr);
DROP FUNCTION search_asset_fields(asset);
DROP FUNCTION search_asset_summary(asset);
DROP FUNCTION search_ci_attribute_fields(ci);
DROP FUNCTION search_attribute_searchable(uuid, uuid, text);
DROP FUNCTION search_field_text(jsonb);
ALTER TABLE search_document_field DROP COLUMN source;
DROP TABLE search_identifier;
