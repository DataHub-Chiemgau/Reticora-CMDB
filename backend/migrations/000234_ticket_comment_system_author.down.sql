-- Down of 000234: system comments have no author and cannot stay; they are
-- deleted before author_id becomes NOT NULL again.

DELETE FROM ticket_comment WHERE author_type = 'system';

ALTER TABLE ticket_comment DROP CONSTRAINT ticket_comment_author_check;
ALTER TABLE ticket_comment DROP COLUMN author_type;
ALTER TABLE ticket_comment ALTER COLUMN author_id SET NOT NULL;
