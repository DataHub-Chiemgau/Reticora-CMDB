-- org-purge-check.sql: rows left of purged organizations (TLC-06, WP-1156).
--
-- For every table of schema public with organization_id (tables and
-- hypertables; a continuous aggregate through its materialization,
-- org_purge_target of migration 000356) the script counts the rows of the
-- organizations in deletion_state 'purged' and prints only the tables with
-- more than 0 rows. After a complete purge the output is empty. The
-- organization row itself stays as tombstone and is not checked.
--
-- Usage (a role that sees every organization, e.g. the migration owner):
--   psql "$DATABASE_URL" -X -q -v ON_ERROR_STOP=1 -f tests/org-purge-check.sql
\set QUIET on
\pset tuples_only on
\pset format unaligned
\pset fieldsep '\t'
SELECT format(
    'SELECT %L AS table_name, count(*) AS rows FROM %s t
      WHERE t.organization_id IN (SELECT id FROM organization WHERE deletion_state = ''purged'')
     HAVING count(*) > 0',
    c.relname, org_purge_target(c.oid)::regclass)
  FROM pg_class c
 WHERE c.relnamespace = 'public'::regnamespace AND NOT c.relispartition
   AND (c.relkind IN ('r', 'p') OR (c.relkind = 'v' AND org_purge_target(c.oid) <> c.oid))
   AND c.relname <> 'organization'
   AND EXISTS (SELECT 1 FROM pg_attribute a
                WHERE a.attrelid = c.oid AND a.attname = 'organization_id' AND NOT a.attisdropped)
 ORDER BY c.relname
\gexec
