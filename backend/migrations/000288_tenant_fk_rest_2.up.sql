-- Migration 000288: the remaining references between tenant tables stay in
-- their organization (WP-877, SR-03 rest II; TEN-09, E-137, E-167): the
-- tables of the modules and tickets, notifications and webhooks, the
-- existing phase-3 tables (maintenance, workflows, forms, compliance), AI
-- and IGA. After it every foreign key between two tenant tables carries the
-- organization and rls.ReferencesWithoutOrganization is empty.
--
-- The 55 references below, on 28 tables, become composite foreign keys
-- (column, organization_id) REFERENCES … (id, organization_id): a reference
-- into another organization fails like an unknown id (23503). Constraint
-- names and ON DELETE actions stay (SET NULL as SET NULL (column), pattern
-- 000136), so the error mapping of the repositories is unchanged.
--
-- Every target gets UNIQUE (id, organization_id) where it is missing
-- (Konvention 19): a DO block adds <table>_id_organization_key only if no
-- unique constraint over exactly these columns exists; later work packages
-- that need the same key add it the same way.
--
-- Existing rows whose reference crosses the organization are not deleted
-- (E-26, pattern 000194 and 000287): a nullable reference is recorded in
-- migration_quarantine with the whole row and set to NULL, a row with a
-- mandatory reference moves to migration_quarantine (its dependent rows
-- follow their own ON DELETE actions). The down migration gives the rows
-- back.

-- Every crossing reference is recorded before the first change.
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'ai_conversation', to_jsonb(t), 'SR-03 (WP-877): ai_conversation.user_id references another organization, set to NULL', '000288'
  FROM ai_conversation t JOIN app_user x ON x.id = t.user_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'form_submission', to_jsonb(t), 'SR-03 (WP-877): form_submission.ci_id references another organization, set to NULL', '000288'
  FROM form_submission t JOIN ci x ON x.id = t.ci_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'form_submission', to_jsonb(t), 'SR-03 (WP-877): form_submission.submitted_by references another organization, set to NULL', '000288'
  FROM form_submission t JOIN app_user x ON x.id = t.submitted_by WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'form_submission', to_jsonb(t), 'SR-03 (WP-877): form_submission.ticket_id references another organization, set to NULL', '000288'
  FROM form_submission t JOIN ticket x ON x.id = t.ticket_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'iga_access_request', to_jsonb(t), 'SR-03 (WP-877): iga_access_request.connector_id references another organization, set to NULL', '000288'
  FROM iga_access_request t JOIN iga_connector x ON x.id = t.connector_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'iga_access_request', to_jsonb(t), 'SR-03 (WP-877): iga_access_request.decision_by references another organization, set to NULL', '000288'
  FROM iga_access_request t JOIN app_user x ON x.id = t.decision_by WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'iga_access_request', to_jsonb(t), 'SR-03 (WP-877): iga_access_request.requester_id references another organization, set to NULL', '000288'
  FROM iga_access_request t JOIN app_user x ON x.id = t.requester_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'iga_access_request', to_jsonb(t), 'SR-03 (WP-877): iga_access_request.workflow_run_id references another organization, set to NULL', '000288'
  FROM iga_access_request t JOIN workflow_run x ON x.id = t.workflow_run_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'iga_access_review_item', to_jsonb(t), 'SR-03 (WP-877): iga_access_review_item.connector_id references another organization, set to NULL', '000288'
  FROM iga_access_review_item t JOIN iga_connector x ON x.id = t.connector_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'iga_access_review_item', to_jsonb(t), 'SR-03 (WP-877): iga_access_review_item.decision_by references another organization, set to NULL', '000288'
  FROM iga_access_review_item t JOIN app_user x ON x.id = t.decision_by WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'iga_connector', to_jsonb(t), 'SR-03 (WP-877): iga_connector.collector_id references another organization, set to NULL', '000288'
  FROM iga_connector t JOIN collector x ON x.id = t.collector_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'iga_connector', to_jsonb(t), 'SR-03 (WP-877): iga_connector.credential_id references another organization, set to NULL', '000288'
  FROM iga_connector t JOIN credential x ON x.id = t.credential_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'iga_drift_finding', to_jsonb(t), 'SR-03 (WP-877): iga_drift_finding.remediation_task_id references another organization, set to NULL', '000288'
  FROM iga_drift_finding t JOIN iga_provisioning_task x ON x.id = t.remediation_task_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'iga_drift_finding', to_jsonb(t), 'SR-03 (WP-877): iga_drift_finding.user_id references another organization, set to NULL', '000288'
  FROM iga_drift_finding t JOIN app_user x ON x.id = t.user_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'iga_provisioning_task', to_jsonb(t), 'SR-03 (WP-877): iga_provisioning_task.user_id references another organization, set to NULL', '000288'
  FROM iga_provisioning_task t JOIN app_user x ON x.id = t.user_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'notification_delivery', to_jsonb(t), 'SR-03 (WP-877): notification_delivery.recipient_user_id references another organization, set to NULL', '000288'
  FROM notification_delivery t JOIN app_user x ON x.id = t.recipient_user_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'notification_digest_item', to_jsonb(t), 'SR-03 (WP-877): notification_digest_item.delivery_id references another organization, set to NULL', '000288'
  FROM notification_digest_item t JOIN notification_delivery x ON x.id = t.delivery_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'security_finding', to_jsonb(t), 'SR-03 (WP-877): security_finding.ci_id references another organization, set to NULL', '000288'
  FROM security_finding t JOIN ci x ON x.id = t.ci_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'stock_scan', to_jsonb(t), 'SR-03 (WP-877): stock_scan.asset_id references another organization, set to NULL', '000288'
  FROM stock_scan t JOIN asset x ON x.id = t.asset_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'stock_scan', to_jsonb(t), 'SR-03 (WP-877): stock_scan.ci_id references another organization, set to NULL', '000288'
  FROM stock_scan t JOIN ci x ON x.id = t.ci_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'stocktake', to_jsonb(t), 'SR-03 (WP-877): stocktake.started_by references another organization, set to NULL', '000288'
  FROM stocktake t JOIN app_user x ON x.id = t.started_by WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'ticket', to_jsonb(t), 'SR-03 (WP-877): ticket.assignee_id references another organization, set to NULL', '000288'
  FROM ticket t JOIN app_user x ON x.id = t.assignee_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'ticket', to_jsonb(t), 'SR-03 (WP-877): ticket.team_id references another organization, set to NULL', '000288'
  FROM ticket t JOIN team x ON x.id = t.team_id WHERE x.organization_id <> t.organization_id;
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT t.organization_id, 'ticket_comment', to_jsonb(t), 'SR-03 (WP-877): ticket_comment.author_id references another organization, set to NULL', '000288'
  FROM ticket_comment t JOIN app_user x ON x.id = t.author_id WHERE x.organization_id <> t.organization_id;

-- The nullable references are cleared through the regular triggers,
-- which derive the scope columns again from what is left.
UPDATE ai_conversation t SET user_id = NULL
  FROM app_user x WHERE x.id = t.user_id AND x.organization_id <> t.organization_id;
UPDATE form_submission t SET ci_id = NULL
  FROM ci x WHERE x.id = t.ci_id AND x.organization_id <> t.organization_id;
UPDATE form_submission t SET submitted_by = NULL
  FROM app_user x WHERE x.id = t.submitted_by AND x.organization_id <> t.organization_id;
UPDATE form_submission t SET ticket_id = NULL
  FROM ticket x WHERE x.id = t.ticket_id AND x.organization_id <> t.organization_id;
UPDATE iga_access_request t SET connector_id = NULL
  FROM iga_connector x WHERE x.id = t.connector_id AND x.organization_id <> t.organization_id;
UPDATE iga_access_request t SET decision_by = NULL
  FROM app_user x WHERE x.id = t.decision_by AND x.organization_id <> t.organization_id;
UPDATE iga_access_request t SET requester_id = NULL
  FROM app_user x WHERE x.id = t.requester_id AND x.organization_id <> t.organization_id;
UPDATE iga_access_request t SET workflow_run_id = NULL
  FROM workflow_run x WHERE x.id = t.workflow_run_id AND x.organization_id <> t.organization_id;
UPDATE iga_access_review_item t SET connector_id = NULL
  FROM iga_connector x WHERE x.id = t.connector_id AND x.organization_id <> t.organization_id;
UPDATE iga_access_review_item t SET decision_by = NULL
  FROM app_user x WHERE x.id = t.decision_by AND x.organization_id <> t.organization_id;
UPDATE iga_connector t SET collector_id = NULL
  FROM collector x WHERE x.id = t.collector_id AND x.organization_id <> t.organization_id;
UPDATE iga_connector t SET credential_id = NULL
  FROM credential x WHERE x.id = t.credential_id AND x.organization_id <> t.organization_id;
UPDATE iga_drift_finding t SET remediation_task_id = NULL
  FROM iga_provisioning_task x WHERE x.id = t.remediation_task_id AND x.organization_id <> t.organization_id;
UPDATE iga_drift_finding t SET user_id = NULL
  FROM app_user x WHERE x.id = t.user_id AND x.organization_id <> t.organization_id;
UPDATE iga_provisioning_task t SET user_id = NULL
  FROM app_user x WHERE x.id = t.user_id AND x.organization_id <> t.organization_id;
UPDATE notification_delivery t SET recipient_user_id = NULL
  FROM app_user x WHERE x.id = t.recipient_user_id AND x.organization_id <> t.organization_id;
UPDATE notification_digest_item t SET delivery_id = NULL
  FROM notification_delivery x WHERE x.id = t.delivery_id AND x.organization_id <> t.organization_id;
UPDATE security_finding t SET ci_id = NULL
  FROM ci x WHERE x.id = t.ci_id AND x.organization_id <> t.organization_id;
UPDATE stock_scan t SET asset_id = NULL
  FROM asset x WHERE x.id = t.asset_id AND x.organization_id <> t.organization_id;
UPDATE stock_scan t SET ci_id = NULL
  FROM ci x WHERE x.id = t.ci_id AND x.organization_id <> t.organization_id;
UPDATE stocktake t SET started_by = NULL
  FROM app_user x WHERE x.id = t.started_by AND x.organization_id <> t.organization_id;
UPDATE ticket t SET assignee_id = NULL
  FROM app_user x WHERE x.id = t.assignee_id AND x.organization_id <> t.organization_id;
UPDATE ticket t SET team_id = NULL
  FROM team x WHERE x.id = t.team_id AND x.organization_id <> t.organization_id;
UPDATE ticket_comment t SET author_id = NULL
  FROM app_user x WHERE x.id = t.author_id AND x.organization_id <> t.organization_id;

-- A row whose mandatory reference crosses the organization moves to
-- migration_quarantine.
WITH moved AS (
    DELETE FROM ai_message t
     USING ai_conversation x
     WHERE x.id = t.conversation_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'ai_message', to_jsonb(moved), 'SR-03 (WP-877): ai_message.conversation_id references another organization, moved to migration_quarantine', '000288'
  FROM moved;
WITH moved AS (
    DELETE FROM compliance_result t
     USING ci x
     WHERE x.id = t.ci_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'compliance_result', to_jsonb(moved), 'SR-03 (WP-877): compliance_result.ci_id references another organization, moved to migration_quarantine', '000288'
  FROM moved;
WITH moved AS (
    DELETE FROM compliance_result t
     USING compliance_rule x
     WHERE x.id = t.rule_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'compliance_result', to_jsonb(moved), 'SR-03 (WP-877): compliance_result.rule_id references another organization, moved to migration_quarantine', '000288'
  FROM moved;
WITH moved AS (
    DELETE FROM document t
     USING app_user x
     WHERE x.id = t.uploaded_by AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'document', to_jsonb(moved), 'SR-03 (WP-877): document.uploaded_by references another organization, moved to migration_quarantine', '000288'
  FROM moved;
WITH moved AS (
    DELETE FROM document_link t
     USING document x
     WHERE x.id = t.document_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'document_link', to_jsonb(moved), 'SR-03 (WP-877): document_link.document_id references another organization, moved to migration_quarantine', '000288'
  FROM moved;
WITH moved AS (
    DELETE FROM form_submission t
     USING form_def x
     WHERE x.id = t.form_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'form_submission', to_jsonb(moved), 'SR-03 (WP-877): form_submission.form_id references another organization, moved to migration_quarantine', '000288'
  FROM moved;
WITH moved AS (
    DELETE FROM iga_access_request t
     USING app_user x
     WHERE x.id = t.subject_user_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'iga_access_request', to_jsonb(moved), 'SR-03 (WP-877): iga_access_request.subject_user_id references another organization, moved to migration_quarantine', '000288'
  FROM moved;
WITH moved AS (
    DELETE FROM iga_access_review_item t
     USING iga_access_review x
     WHERE x.id = t.review_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'iga_access_review_item', to_jsonb(moved), 'SR-03 (WP-877): iga_access_review_item.review_id references another organization, moved to migration_quarantine', '000288'
  FROM moved;
WITH moved AS (
    DELETE FROM iga_access_review_item t
     USING app_user x
     WHERE x.id = t.user_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'iga_access_review_item', to_jsonb(moved), 'SR-03 (WP-877): iga_access_review_item.user_id references another organization, moved to migration_quarantine', '000288'
  FROM moved;
WITH moved AS (
    DELETE FROM iga_drift_finding t
     USING iga_connector x
     WHERE x.id = t.connector_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'iga_drift_finding', to_jsonb(moved), 'SR-03 (WP-877): iga_drift_finding.connector_id references another organization, moved to migration_quarantine', '000288'
  FROM moved;
WITH moved AS (
    DELETE FROM iga_provisioning_task t
     USING iga_connector x
     WHERE x.id = t.connector_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'iga_provisioning_task', to_jsonb(moved), 'SR-03 (WP-877): iga_provisioning_task.connector_id references another organization, moved to migration_quarantine', '000288'
  FROM moved;
WITH moved AS (
    DELETE FROM internal_order_item t
     USING internal_order x
     WHERE x.id = t.order_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'internal_order_item', to_jsonb(moved), 'SR-03 (WP-877): internal_order_item.order_id references another organization, moved to migration_quarantine', '000288'
  FROM moved;
WITH moved AS (
    DELETE FROM maintenance_notification t
     USING maintenance_window x
     WHERE x.id = t.maintenance_window_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'maintenance_notification', to_jsonb(moved), 'SR-03 (WP-877): maintenance_notification.maintenance_window_id references another organization, moved to migration_quarantine', '000288'
  FROM moved;
WITH moved AS (
    DELETE FROM maintenance_window_ci t
     USING ci x
     WHERE x.id = t.ci_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'maintenance_window_ci', to_jsonb(moved), 'SR-03 (WP-877): maintenance_window_ci.ci_id references another organization, moved to migration_quarantine', '000288'
  FROM moved;
WITH moved AS (
    DELETE FROM maintenance_window_ci t
     USING maintenance_window x
     WHERE x.id = t.maintenance_window_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'maintenance_window_ci', to_jsonb(moved), 'SR-03 (WP-877): maintenance_window_ci.maintenance_window_id references another organization, moved to migration_quarantine', '000288'
  FROM moved;
WITH moved AS (
    DELETE FROM notification_delivery t
     USING notification_channel x
     WHERE x.id = t.channel_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'notification_delivery', to_jsonb(moved), 'SR-03 (WP-877): notification_delivery.channel_id references another organization, moved to migration_quarantine', '000288'
  FROM moved;
WITH moved AS (
    DELETE FROM notification_digest_item t
     USING notification_channel x
     WHERE x.id = t.channel_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'notification_digest_item', to_jsonb(moved), 'SR-03 (WP-877): notification_digest_item.channel_id references another organization, moved to migration_quarantine', '000288'
  FROM moved;
WITH moved AS (
    DELETE FROM notification_digest_item t
     USING app_user x
     WHERE x.id = t.recipient_user_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'notification_digest_item', to_jsonb(moved), 'SR-03 (WP-877): notification_digest_item.recipient_user_id references another organization, moved to migration_quarantine', '000288'
  FROM moved;
WITH moved AS (
    DELETE FROM notification_inbox t
     USING app_user x
     WHERE x.id = t.user_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'notification_inbox', to_jsonb(moved), 'SR-03 (WP-877): notification_inbox.user_id references another organization, moved to migration_quarantine', '000288'
  FROM moved;
WITH moved AS (
    DELETE FROM notification_preference t
     USING app_user x
     WHERE x.id = t.user_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'notification_preference', to_jsonb(moved), 'SR-03 (WP-877): notification_preference.user_id references another organization, moved to migration_quarantine', '000288'
  FROM moved;
WITH moved AS (
    DELETE FROM stock_scan t
     USING app_user x
     WHERE x.id = t.scanned_by AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'stock_scan', to_jsonb(moved), 'SR-03 (WP-877): stock_scan.scanned_by references another organization, moved to migration_quarantine', '000288'
  FROM moved;
WITH moved AS (
    DELETE FROM stock_scan t
     USING stocktake x
     WHERE x.id = t.stocktake_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'stock_scan', to_jsonb(moved), 'SR-03 (WP-877): stock_scan.stocktake_id references another organization, moved to migration_quarantine', '000288'
  FROM moved;
WITH moved AS (
    DELETE FROM ticket t
     USING app_user x
     WHERE x.id = t.reporter_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'ticket', to_jsonb(moved), 'SR-03 (WP-877): ticket.reporter_id references another organization, moved to migration_quarantine', '000288'
  FROM moved;
WITH moved AS (
    DELETE FROM ticket_comment t
     USING ticket x
     WHERE x.id = t.ticket_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'ticket_comment', to_jsonb(moved), 'SR-03 (WP-877): ticket_comment.ticket_id references another organization, moved to migration_quarantine', '000288'
  FROM moved;
WITH moved AS (
    DELETE FROM ticket_sla t
     USING sla x
     WHERE x.id = t.sla_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'ticket_sla', to_jsonb(moved), 'SR-03 (WP-877): ticket_sla.sla_id references another organization, moved to migration_quarantine', '000288'
  FROM moved;
WITH moved AS (
    DELETE FROM ticket_sla t
     USING ticket x
     WHERE x.id = t.ticket_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'ticket_sla', to_jsonb(moved), 'SR-03 (WP-877): ticket_sla.ticket_id references another organization, moved to migration_quarantine', '000288'
  FROM moved;
WITH moved AS (
    DELETE FROM webhook_dead_letter t
     USING webhook_delivery x
     WHERE x.id = t.delivery_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'webhook_dead_letter', to_jsonb(moved), 'SR-03 (WP-877): webhook_dead_letter.delivery_id references another organization, moved to migration_quarantine', '000288'
  FROM moved;
WITH moved AS (
    DELETE FROM webhook_dead_letter t
     USING webhook_subscription x
     WHERE x.id = t.subscription_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'webhook_dead_letter', to_jsonb(moved), 'SR-03 (WP-877): webhook_dead_letter.subscription_id references another organization, moved to migration_quarantine', '000288'
  FROM moved;
WITH moved AS (
    DELETE FROM webhook_delivery t
     USING webhook_subscription x
     WHERE x.id = t.subscription_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'webhook_delivery', to_jsonb(moved), 'SR-03 (WP-877): webhook_delivery.subscription_id references another organization, moved to migration_quarantine', '000288'
  FROM moved;
WITH moved AS (
    DELETE FROM workflow_run t
     USING workflow_def x
     WHERE x.id = t.workflow_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'workflow_run', to_jsonb(moved), 'SR-03 (WP-877): workflow_run.workflow_id references another organization, moved to migration_quarantine', '000288'
  FROM moved;
WITH moved AS (
    DELETE FROM workflow_step t
     USING workflow_run x
     WHERE x.id = t.run_id AND x.organization_id <> t.organization_id
    RETURNING t.*
)
INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
SELECT moved.organization_id, 'workflow_step', to_jsonb(moved), 'SR-03 (WP-877): workflow_step.run_id references another organization, moved to migration_quarantine', '000288'
  FROM moved;

-- UNIQUE (<column>, organization_id) on the targets that lack it.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint u
         WHERE u.conrelid = 'ai_conversation'::regclass AND u.contype IN ('u', 'p')
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = u.conrelid AND a.attnum = ANY (u.conkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE ai_conversation ADD CONSTRAINT ai_conversation_id_organization_key UNIQUE (id, organization_id);
    END IF;
END
$$;
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint u
         WHERE u.conrelid = 'compliance_rule'::regclass AND u.contype IN ('u', 'p')
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = u.conrelid AND a.attnum = ANY (u.conkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE compliance_rule ADD CONSTRAINT compliance_rule_id_organization_key UNIQUE (id, organization_id);
    END IF;
END
$$;
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint u
         WHERE u.conrelid = 'form_def'::regclass AND u.contype IN ('u', 'p')
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = u.conrelid AND a.attnum = ANY (u.conkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE form_def ADD CONSTRAINT form_def_id_organization_key UNIQUE (id, organization_id);
    END IF;
END
$$;
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint u
         WHERE u.conrelid = 'iga_connector'::regclass AND u.contype IN ('u', 'p')
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = u.conrelid AND a.attnum = ANY (u.conkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE iga_connector ADD CONSTRAINT iga_connector_id_organization_key UNIQUE (id, organization_id);
    END IF;
END
$$;
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint u
         WHERE u.conrelid = 'workflow_run'::regclass AND u.contype IN ('u', 'p')
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = u.conrelid AND a.attnum = ANY (u.conkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE workflow_run ADD CONSTRAINT workflow_run_id_organization_key UNIQUE (id, organization_id);
    END IF;
END
$$;
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint u
         WHERE u.conrelid = 'iga_access_review'::regclass AND u.contype IN ('u', 'p')
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = u.conrelid AND a.attnum = ANY (u.conkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE iga_access_review ADD CONSTRAINT iga_access_review_id_organization_key UNIQUE (id, organization_id);
    END IF;
END
$$;
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint u
         WHERE u.conrelid = 'credential'::regclass AND u.contype IN ('u', 'p')
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = u.conrelid AND a.attnum = ANY (u.conkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE credential ADD CONSTRAINT credential_id_organization_key UNIQUE (id, organization_id);
    END IF;
END
$$;
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint u
         WHERE u.conrelid = 'iga_provisioning_task'::regclass AND u.contype IN ('u', 'p')
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = u.conrelid AND a.attnum = ANY (u.conkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE iga_provisioning_task ADD CONSTRAINT iga_provisioning_task_id_organization_key UNIQUE (id, organization_id);
    END IF;
END
$$;
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint u
         WHERE u.conrelid = 'maintenance_window'::regclass AND u.contype IN ('u', 'p')
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = u.conrelid AND a.attnum = ANY (u.conkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE maintenance_window ADD CONSTRAINT maintenance_window_id_organization_key UNIQUE (id, organization_id);
    END IF;
END
$$;
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint u
         WHERE u.conrelid = 'notification_channel'::regclass AND u.contype IN ('u', 'p')
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = u.conrelid AND a.attnum = ANY (u.conkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE notification_channel ADD CONSTRAINT notification_channel_id_organization_key UNIQUE (id, organization_id);
    END IF;
END
$$;
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint u
         WHERE u.conrelid = 'notification_delivery'::regclass AND u.contype IN ('u', 'p')
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = u.conrelid AND a.attnum = ANY (u.conkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE notification_delivery ADD CONSTRAINT notification_delivery_id_organization_key UNIQUE (id, organization_id);
    END IF;
END
$$;
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint u
         WHERE u.conrelid = 'sla'::regclass AND u.contype IN ('u', 'p')
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = u.conrelid AND a.attnum = ANY (u.conkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE sla ADD CONSTRAINT sla_id_organization_key UNIQUE (id, organization_id);
    END IF;
END
$$;
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint u
         WHERE u.conrelid = 'webhook_delivery'::regclass AND u.contype IN ('u', 'p')
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = u.conrelid AND a.attnum = ANY (u.conkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE webhook_delivery ADD CONSTRAINT webhook_delivery_id_organization_key UNIQUE (id, organization_id);
    END IF;
END
$$;
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint u
         WHERE u.conrelid = 'webhook_subscription'::regclass AND u.contype IN ('u', 'p')
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = u.conrelid AND a.attnum = ANY (u.conkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE webhook_subscription ADD CONSTRAINT webhook_subscription_id_organization_key UNIQUE (id, organization_id);
    END IF;
END
$$;
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint u
         WHERE u.conrelid = 'workflow_def'::regclass AND u.contype IN ('u', 'p')
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = u.conrelid AND a.attnum = ANY (u.conkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE workflow_def ADD CONSTRAINT workflow_def_id_organization_key UNIQUE (id, organization_id);
    END IF;
END
$$;

ALTER TABLE ai_conversation
    DROP CONSTRAINT ai_conversation_user_id_fkey,
    ADD CONSTRAINT ai_conversation_user_id_fkey FOREIGN KEY (user_id, organization_id) REFERENCES app_user (id, organization_id) ON DELETE SET NULL (user_id);
ALTER TABLE ai_message
    DROP CONSTRAINT ai_message_conversation_id_fkey,
    ADD CONSTRAINT ai_message_conversation_id_fkey FOREIGN KEY (conversation_id, organization_id) REFERENCES ai_conversation (id, organization_id) ON DELETE CASCADE;
ALTER TABLE compliance_result
    DROP CONSTRAINT compliance_result_ci_id_fkey,
    ADD CONSTRAINT compliance_result_ci_id_fkey FOREIGN KEY (ci_id, organization_id) REFERENCES ci (id, organization_id) ON DELETE CASCADE,
    DROP CONSTRAINT compliance_result_rule_id_fkey,
    ADD CONSTRAINT compliance_result_rule_id_fkey FOREIGN KEY (rule_id, organization_id) REFERENCES compliance_rule (id, organization_id) ON DELETE CASCADE;
ALTER TABLE document
    DROP CONSTRAINT document_uploaded_by_fkey,
    ADD CONSTRAINT document_uploaded_by_fkey FOREIGN KEY (uploaded_by, organization_id) REFERENCES app_user (id, organization_id);
ALTER TABLE document_link
    DROP CONSTRAINT document_link_document_id_fkey,
    ADD CONSTRAINT document_link_document_id_fkey FOREIGN KEY (document_id, organization_id) REFERENCES document (id, organization_id) ON DELETE CASCADE;
ALTER TABLE form_submission
    DROP CONSTRAINT form_submission_ci_id_fkey,
    ADD CONSTRAINT form_submission_ci_id_fkey FOREIGN KEY (ci_id, organization_id) REFERENCES ci (id, organization_id) ON DELETE SET NULL (ci_id),
    DROP CONSTRAINT form_submission_form_id_fkey,
    ADD CONSTRAINT form_submission_form_id_fkey FOREIGN KEY (form_id, organization_id) REFERENCES form_def (id, organization_id) ON DELETE CASCADE,
    DROP CONSTRAINT form_submission_submitted_by_fkey,
    ADD CONSTRAINT form_submission_submitted_by_fkey FOREIGN KEY (submitted_by, organization_id) REFERENCES app_user (id, organization_id) ON DELETE SET NULL (submitted_by),
    DROP CONSTRAINT form_submission_ticket_id_fkey,
    ADD CONSTRAINT form_submission_ticket_id_fkey FOREIGN KEY (ticket_id, organization_id) REFERENCES ticket (id, organization_id) ON DELETE SET NULL (ticket_id);
ALTER TABLE iga_access_request
    DROP CONSTRAINT iga_access_request_connector_id_fkey,
    ADD CONSTRAINT iga_access_request_connector_id_fkey FOREIGN KEY (connector_id, organization_id) REFERENCES iga_connector (id, organization_id) ON DELETE SET NULL (connector_id),
    DROP CONSTRAINT iga_access_request_decision_by_fkey,
    ADD CONSTRAINT iga_access_request_decision_by_fkey FOREIGN KEY (decision_by, organization_id) REFERENCES app_user (id, organization_id) ON DELETE SET NULL (decision_by),
    DROP CONSTRAINT iga_access_request_requester_id_fkey,
    ADD CONSTRAINT iga_access_request_requester_id_fkey FOREIGN KEY (requester_id, organization_id) REFERENCES app_user (id, organization_id) ON DELETE SET NULL (requester_id),
    DROP CONSTRAINT iga_access_request_subject_user_id_fkey,
    ADD CONSTRAINT iga_access_request_subject_user_id_fkey FOREIGN KEY (subject_user_id, organization_id) REFERENCES app_user (id, organization_id) ON DELETE CASCADE,
    DROP CONSTRAINT iga_access_request_workflow_run_id_fkey,
    ADD CONSTRAINT iga_access_request_workflow_run_id_fkey FOREIGN KEY (workflow_run_id, organization_id) REFERENCES workflow_run (id, organization_id) ON DELETE SET NULL (workflow_run_id);
ALTER TABLE iga_access_review_item
    DROP CONSTRAINT iga_access_review_item_connector_id_fkey,
    ADD CONSTRAINT iga_access_review_item_connector_id_fkey FOREIGN KEY (connector_id, organization_id) REFERENCES iga_connector (id, organization_id) ON DELETE SET NULL (connector_id),
    DROP CONSTRAINT iga_access_review_item_decision_by_fkey,
    ADD CONSTRAINT iga_access_review_item_decision_by_fkey FOREIGN KEY (decision_by, organization_id) REFERENCES app_user (id, organization_id) ON DELETE SET NULL (decision_by),
    DROP CONSTRAINT iga_access_review_item_review_id_fkey,
    ADD CONSTRAINT iga_access_review_item_review_id_fkey FOREIGN KEY (review_id, organization_id) REFERENCES iga_access_review (id, organization_id) ON DELETE CASCADE,
    DROP CONSTRAINT iga_access_review_item_user_id_fkey,
    ADD CONSTRAINT iga_access_review_item_user_id_fkey FOREIGN KEY (user_id, organization_id) REFERENCES app_user (id, organization_id) ON DELETE CASCADE;
ALTER TABLE iga_connector
    DROP CONSTRAINT iga_connector_collector_id_fkey,
    ADD CONSTRAINT iga_connector_collector_id_fkey FOREIGN KEY (collector_id, organization_id) REFERENCES collector (id, organization_id) ON DELETE SET NULL (collector_id),
    DROP CONSTRAINT iga_connector_credential_id_fkey,
    ADD CONSTRAINT iga_connector_credential_id_fkey FOREIGN KEY (credential_id, organization_id) REFERENCES credential (id, organization_id) ON DELETE SET NULL (credential_id);
ALTER TABLE iga_drift_finding
    DROP CONSTRAINT iga_drift_finding_connector_id_fkey,
    ADD CONSTRAINT iga_drift_finding_connector_id_fkey FOREIGN KEY (connector_id, organization_id) REFERENCES iga_connector (id, organization_id) ON DELETE CASCADE,
    DROP CONSTRAINT iga_drift_finding_remediation_task_id_fkey,
    ADD CONSTRAINT iga_drift_finding_remediation_task_id_fkey FOREIGN KEY (remediation_task_id, organization_id) REFERENCES iga_provisioning_task (id, organization_id) ON DELETE SET NULL (remediation_task_id),
    DROP CONSTRAINT iga_drift_finding_user_id_fkey,
    ADD CONSTRAINT iga_drift_finding_user_id_fkey FOREIGN KEY (user_id, organization_id) REFERENCES app_user (id, organization_id) ON DELETE SET NULL (user_id);
ALTER TABLE iga_provisioning_task
    DROP CONSTRAINT iga_provisioning_task_connector_id_fkey,
    ADD CONSTRAINT iga_provisioning_task_connector_id_fkey FOREIGN KEY (connector_id, organization_id) REFERENCES iga_connector (id, organization_id) ON DELETE CASCADE,
    DROP CONSTRAINT iga_provisioning_task_user_id_fkey,
    ADD CONSTRAINT iga_provisioning_task_user_id_fkey FOREIGN KEY (user_id, organization_id) REFERENCES app_user (id, organization_id) ON DELETE SET NULL (user_id);
ALTER TABLE internal_order_item
    DROP CONSTRAINT internal_order_item_order_id_fkey,
    ADD CONSTRAINT internal_order_item_order_id_fkey FOREIGN KEY (order_id, organization_id) REFERENCES internal_order (id, organization_id) ON DELETE CASCADE;
ALTER TABLE maintenance_notification
    DROP CONSTRAINT maintenance_notification_maintenance_window_id_fkey,
    ADD CONSTRAINT maintenance_notification_maintenance_window_id_fkey FOREIGN KEY (maintenance_window_id, organization_id) REFERENCES maintenance_window (id, organization_id) ON DELETE CASCADE;
ALTER TABLE maintenance_window_ci
    DROP CONSTRAINT maintenance_window_ci_ci_id_fkey,
    ADD CONSTRAINT maintenance_window_ci_ci_id_fkey FOREIGN KEY (ci_id, organization_id) REFERENCES ci (id, organization_id) ON DELETE CASCADE,
    DROP CONSTRAINT maintenance_window_ci_maintenance_window_id_fkey,
    ADD CONSTRAINT maintenance_window_ci_maintenance_window_id_fkey FOREIGN KEY (maintenance_window_id, organization_id) REFERENCES maintenance_window (id, organization_id) ON DELETE CASCADE;
ALTER TABLE notification_delivery
    DROP CONSTRAINT notification_delivery_channel_id_fkey,
    ADD CONSTRAINT notification_delivery_channel_id_fkey FOREIGN KEY (channel_id, organization_id) REFERENCES notification_channel (id, organization_id) ON DELETE CASCADE,
    DROP CONSTRAINT notification_delivery_recipient_user_id_fkey,
    ADD CONSTRAINT notification_delivery_recipient_user_id_fkey FOREIGN KEY (recipient_user_id, organization_id) REFERENCES app_user (id, organization_id) ON DELETE CASCADE;
ALTER TABLE notification_digest_item
    DROP CONSTRAINT notification_digest_item_channel_id_fkey,
    ADD CONSTRAINT notification_digest_item_channel_id_fkey FOREIGN KEY (channel_id, organization_id) REFERENCES notification_channel (id, organization_id) ON DELETE CASCADE,
    DROP CONSTRAINT notification_digest_item_delivery_id_fkey,
    ADD CONSTRAINT notification_digest_item_delivery_id_fkey FOREIGN KEY (delivery_id, organization_id) REFERENCES notification_delivery (id, organization_id) ON DELETE CASCADE,
    DROP CONSTRAINT notification_digest_item_recipient_user_id_fkey,
    ADD CONSTRAINT notification_digest_item_recipient_user_id_fkey FOREIGN KEY (recipient_user_id, organization_id) REFERENCES app_user (id, organization_id) ON DELETE CASCADE;
ALTER TABLE notification_inbox
    DROP CONSTRAINT notification_inbox_user_id_fkey,
    ADD CONSTRAINT notification_inbox_user_id_fkey FOREIGN KEY (user_id, organization_id) REFERENCES app_user (id, organization_id) ON DELETE CASCADE;
ALTER TABLE notification_preference
    DROP CONSTRAINT notification_preference_user_id_fkey,
    ADD CONSTRAINT notification_preference_user_id_fkey FOREIGN KEY (user_id, organization_id) REFERENCES app_user (id, organization_id) ON DELETE CASCADE;
ALTER TABLE security_finding
    DROP CONSTRAINT security_finding_ci_id_fkey,
    ADD CONSTRAINT security_finding_ci_id_fkey FOREIGN KEY (ci_id, organization_id) REFERENCES ci (id, organization_id) ON DELETE CASCADE;
ALTER TABLE stock_scan
    DROP CONSTRAINT stock_scan_asset_id_fkey,
    ADD CONSTRAINT stock_scan_asset_id_fkey FOREIGN KEY (asset_id, organization_id) REFERENCES asset (id, organization_id),
    DROP CONSTRAINT stock_scan_ci_id_fkey,
    ADD CONSTRAINT stock_scan_ci_id_fkey FOREIGN KEY (ci_id, organization_id) REFERENCES ci (id, organization_id),
    DROP CONSTRAINT stock_scan_scanned_by_fkey,
    ADD CONSTRAINT stock_scan_scanned_by_fkey FOREIGN KEY (scanned_by, organization_id) REFERENCES app_user (id, organization_id),
    DROP CONSTRAINT stock_scan_stocktake_id_fkey,
    ADD CONSTRAINT stock_scan_stocktake_id_fkey FOREIGN KEY (stocktake_id, organization_id) REFERENCES stocktake (id, organization_id) ON DELETE CASCADE;
ALTER TABLE stocktake
    DROP CONSTRAINT stocktake_started_by_fkey,
    ADD CONSTRAINT stocktake_started_by_fkey FOREIGN KEY (started_by, organization_id) REFERENCES app_user (id, organization_id);
ALTER TABLE ticket
    DROP CONSTRAINT ticket_assignee_id_fkey,
    ADD CONSTRAINT ticket_assignee_id_fkey FOREIGN KEY (assignee_id, organization_id) REFERENCES app_user (id, organization_id),
    DROP CONSTRAINT ticket_reporter_id_fkey,
    ADD CONSTRAINT ticket_reporter_id_fkey FOREIGN KEY (reporter_id, organization_id) REFERENCES app_user (id, organization_id),
    DROP CONSTRAINT ticket_team_id_fkey,
    ADD CONSTRAINT ticket_team_id_fkey FOREIGN KEY (team_id, organization_id) REFERENCES team (id, organization_id);
ALTER TABLE ticket_comment
    DROP CONSTRAINT ticket_comment_author_id_fkey,
    ADD CONSTRAINT ticket_comment_author_id_fkey FOREIGN KEY (author_id, organization_id) REFERENCES app_user (id, organization_id),
    DROP CONSTRAINT ticket_comment_ticket_id_fkey,
    ADD CONSTRAINT ticket_comment_ticket_id_fkey FOREIGN KEY (ticket_id, organization_id) REFERENCES ticket (id, organization_id) ON DELETE CASCADE;
ALTER TABLE ticket_sla
    DROP CONSTRAINT ticket_sla_sla_id_fkey,
    ADD CONSTRAINT ticket_sla_sla_id_fkey FOREIGN KEY (sla_id, organization_id) REFERENCES sla (id, organization_id) ON DELETE RESTRICT,
    DROP CONSTRAINT ticket_sla_ticket_id_fkey,
    ADD CONSTRAINT ticket_sla_ticket_id_fkey FOREIGN KEY (ticket_id, organization_id) REFERENCES ticket (id, organization_id) ON DELETE CASCADE;
ALTER TABLE webhook_dead_letter
    DROP CONSTRAINT webhook_dead_letter_delivery_id_fkey,
    ADD CONSTRAINT webhook_dead_letter_delivery_id_fkey FOREIGN KEY (delivery_id, organization_id) REFERENCES webhook_delivery (id, organization_id) ON DELETE CASCADE,
    DROP CONSTRAINT webhook_dead_letter_subscription_id_fkey,
    ADD CONSTRAINT webhook_dead_letter_subscription_id_fkey FOREIGN KEY (subscription_id, organization_id) REFERENCES webhook_subscription (id, organization_id) ON DELETE CASCADE;
ALTER TABLE webhook_delivery
    DROP CONSTRAINT webhook_delivery_subscription_id_fkey,
    ADD CONSTRAINT webhook_delivery_subscription_id_fkey FOREIGN KEY (subscription_id, organization_id) REFERENCES webhook_subscription (id, organization_id) ON DELETE CASCADE;
ALTER TABLE workflow_run
    DROP CONSTRAINT workflow_run_workflow_id_fkey,
    ADD CONSTRAINT workflow_run_workflow_id_fkey FOREIGN KEY (workflow_id, organization_id) REFERENCES workflow_def (id, organization_id) ON DELETE CASCADE;
ALTER TABLE workflow_step
    DROP CONSTRAINT workflow_step_run_id_fkey,
    ADD CONSTRAINT workflow_step_run_id_fkey FOREIGN KEY (run_id, organization_id) REFERENCES workflow_run (id, organization_id) ON DELETE CASCADE;
