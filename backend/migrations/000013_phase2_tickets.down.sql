ALTER TABLE IF EXISTS ticket_comment DISABLE ROW LEVEL SECURITY;
ALTER TABLE IF EXISTS ticket DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS ticket_comment_tenant_isolation ON ticket_comment;
DROP POLICY IF EXISTS ticket_tenant_isolation ON ticket;

DROP INDEX IF EXISTS idx_ticket_comment_ticket;
DROP INDEX IF EXISTS idx_ticket_number;
DROP INDEX IF EXISTS idx_ticket_team;
DROP INDEX IF EXISTS idx_ticket_reporter;
DROP INDEX IF EXISTS idx_ticket_assignee;
DROP INDEX IF EXISTS idx_ticket_status;
DROP INDEX IF EXISTS idx_ticket_org;

DROP TABLE IF EXISTS ticket_comment;
DROP TABLE IF EXISTS ticket;
