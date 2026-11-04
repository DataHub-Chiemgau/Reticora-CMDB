-- Down of 000230: ticket.tags comes back with the sorted keys of the
-- ticket's catalog tags; the catalog tags the up migration created stay (the
-- catalog is shared with the CIs, a tag may be in use there by now).

ALTER TABLE ticket ADD COLUMN tags TEXT[] DEFAULT '{}';

ALTER TABLE ticket DISABLE TRIGGER trg_ticket_updated_at;
UPDATE ticket t SET tags = (SELECT array_agg(g.key ORDER BY g.key)
                              FROM ticket_tag tt JOIN tag g ON g.id = tt.tag_id
                             WHERE tt.ticket_id = t.id)
 WHERE EXISTS (SELECT 1 FROM ticket_tag tt WHERE tt.ticket_id = t.id);
ALTER TABLE ticket ENABLE TRIGGER trg_ticket_updated_at;

CREATE OR REPLACE FUNCTION propagate_ticket_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE ticket_comment SET ticket_id = ticket_id WHERE ticket_id = NEW.id;
    UPDATE document_link SET entity_id = entity_id WHERE entity_type = 'ticket' AND entity_id = NEW.id;
    RETURN NULL;
END
$$;

DROP TABLE ticket_tag;
DROP TABLE ticket_link;
DROP FUNCTION derive_ticket_tag_scope();
DROP FUNCTION derive_ticket_link_scope();

ALTER TABLE review_item DROP CONSTRAINT review_item_id_organization_key;
ALTER TABLE ticket DROP CONSTRAINT ticket_id_organization_key;
