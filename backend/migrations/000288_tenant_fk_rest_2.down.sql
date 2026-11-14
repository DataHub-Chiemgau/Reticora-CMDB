-- Reverts WP-877: the 55 references name the referenced row only again,
-- the unique keys the up migration may have added go where no foreign key
-- uses them any more, and the rows the up migration changed come back from
-- migration_quarantine.

ALTER TABLE ai_conversation
    DROP CONSTRAINT ai_conversation_user_id_fkey,
    ADD CONSTRAINT ai_conversation_user_id_fkey FOREIGN KEY (user_id) REFERENCES app_user (id) ON DELETE SET NULL;
ALTER TABLE ai_message
    DROP CONSTRAINT ai_message_conversation_id_fkey,
    ADD CONSTRAINT ai_message_conversation_id_fkey FOREIGN KEY (conversation_id) REFERENCES ai_conversation (id) ON DELETE CASCADE;
ALTER TABLE compliance_result
    DROP CONSTRAINT compliance_result_ci_id_fkey,
    ADD CONSTRAINT compliance_result_ci_id_fkey FOREIGN KEY (ci_id) REFERENCES ci (id) ON DELETE CASCADE,
    DROP CONSTRAINT compliance_result_rule_id_fkey,
    ADD CONSTRAINT compliance_result_rule_id_fkey FOREIGN KEY (rule_id) REFERENCES compliance_rule (id) ON DELETE CASCADE;
ALTER TABLE document
    DROP CONSTRAINT document_uploaded_by_fkey,
    ADD CONSTRAINT document_uploaded_by_fkey FOREIGN KEY (uploaded_by) REFERENCES app_user (id);
ALTER TABLE document_link
    DROP CONSTRAINT document_link_document_id_fkey,
    ADD CONSTRAINT document_link_document_id_fkey FOREIGN KEY (document_id) REFERENCES document (id) ON DELETE CASCADE;
ALTER TABLE form_submission
    DROP CONSTRAINT form_submission_ci_id_fkey,
    ADD CONSTRAINT form_submission_ci_id_fkey FOREIGN KEY (ci_id) REFERENCES ci (id) ON DELETE SET NULL,
    DROP CONSTRAINT form_submission_form_id_fkey,
    ADD CONSTRAINT form_submission_form_id_fkey FOREIGN KEY (form_id) REFERENCES form_def (id) ON DELETE CASCADE,
    DROP CONSTRAINT form_submission_submitted_by_fkey,
    ADD CONSTRAINT form_submission_submitted_by_fkey FOREIGN KEY (submitted_by) REFERENCES app_user (id) ON DELETE SET NULL,
    DROP CONSTRAINT form_submission_ticket_id_fkey,
    ADD CONSTRAINT form_submission_ticket_id_fkey FOREIGN KEY (ticket_id) REFERENCES ticket (id) ON DELETE SET NULL;
ALTER TABLE iga_access_request
    DROP CONSTRAINT iga_access_request_connector_id_fkey,
    ADD CONSTRAINT iga_access_request_connector_id_fkey FOREIGN KEY (connector_id) REFERENCES iga_connector (id) ON DELETE SET NULL,
    DROP CONSTRAINT iga_access_request_decision_by_fkey,
    ADD CONSTRAINT iga_access_request_decision_by_fkey FOREIGN KEY (decision_by) REFERENCES app_user (id) ON DELETE SET NULL,
    DROP CONSTRAINT iga_access_request_requester_id_fkey,
    ADD CONSTRAINT iga_access_request_requester_id_fkey FOREIGN KEY (requester_id) REFERENCES app_user (id) ON DELETE SET NULL,
    DROP CONSTRAINT iga_access_request_subject_user_id_fkey,
    ADD CONSTRAINT iga_access_request_subject_user_id_fkey FOREIGN KEY (subject_user_id) REFERENCES app_user (id) ON DELETE CASCADE,
    DROP CONSTRAINT iga_access_request_workflow_run_id_fkey,
    ADD CONSTRAINT iga_access_request_workflow_run_id_fkey FOREIGN KEY (workflow_run_id) REFERENCES workflow_run (id) ON DELETE SET NULL;
ALTER TABLE iga_access_review_item
    DROP CONSTRAINT iga_access_review_item_connector_id_fkey,
    ADD CONSTRAINT iga_access_review_item_connector_id_fkey FOREIGN KEY (connector_id) REFERENCES iga_connector (id) ON DELETE SET NULL,
    DROP CONSTRAINT iga_access_review_item_decision_by_fkey,
    ADD CONSTRAINT iga_access_review_item_decision_by_fkey FOREIGN KEY (decision_by) REFERENCES app_user (id) ON DELETE SET NULL,
    DROP CONSTRAINT iga_access_review_item_review_id_fkey,
    ADD CONSTRAINT iga_access_review_item_review_id_fkey FOREIGN KEY (review_id) REFERENCES iga_access_review (id) ON DELETE CASCADE,
    DROP CONSTRAINT iga_access_review_item_user_id_fkey,
    ADD CONSTRAINT iga_access_review_item_user_id_fkey FOREIGN KEY (user_id) REFERENCES app_user (id) ON DELETE CASCADE;
ALTER TABLE iga_connector
    DROP CONSTRAINT iga_connector_collector_id_fkey,
    ADD CONSTRAINT iga_connector_collector_id_fkey FOREIGN KEY (collector_id) REFERENCES collector (id) ON DELETE SET NULL,
    DROP CONSTRAINT iga_connector_credential_id_fkey,
    ADD CONSTRAINT iga_connector_credential_id_fkey FOREIGN KEY (credential_id) REFERENCES credential (id) ON DELETE SET NULL;
ALTER TABLE iga_drift_finding
    DROP CONSTRAINT iga_drift_finding_connector_id_fkey,
    ADD CONSTRAINT iga_drift_finding_connector_id_fkey FOREIGN KEY (connector_id) REFERENCES iga_connector (id) ON DELETE CASCADE,
    DROP CONSTRAINT iga_drift_finding_remediation_task_id_fkey,
    ADD CONSTRAINT iga_drift_finding_remediation_task_id_fkey FOREIGN KEY (remediation_task_id) REFERENCES iga_provisioning_task (id) ON DELETE SET NULL,
    DROP CONSTRAINT iga_drift_finding_user_id_fkey,
    ADD CONSTRAINT iga_drift_finding_user_id_fkey FOREIGN KEY (user_id) REFERENCES app_user (id) ON DELETE SET NULL;
ALTER TABLE iga_provisioning_task
    DROP CONSTRAINT iga_provisioning_task_connector_id_fkey,
    ADD CONSTRAINT iga_provisioning_task_connector_id_fkey FOREIGN KEY (connector_id) REFERENCES iga_connector (id) ON DELETE CASCADE,
    DROP CONSTRAINT iga_provisioning_task_user_id_fkey,
    ADD CONSTRAINT iga_provisioning_task_user_id_fkey FOREIGN KEY (user_id) REFERENCES app_user (id) ON DELETE SET NULL;
ALTER TABLE internal_order_item
    DROP CONSTRAINT internal_order_item_order_id_fkey,
    ADD CONSTRAINT internal_order_item_order_id_fkey FOREIGN KEY (order_id) REFERENCES internal_order (id) ON DELETE CASCADE;
ALTER TABLE maintenance_notification
    DROP CONSTRAINT maintenance_notification_maintenance_window_id_fkey,
    ADD CONSTRAINT maintenance_notification_maintenance_window_id_fkey FOREIGN KEY (maintenance_window_id) REFERENCES maintenance_window (id) ON DELETE CASCADE;
ALTER TABLE maintenance_window_ci
    DROP CONSTRAINT maintenance_window_ci_ci_id_fkey,
    ADD CONSTRAINT maintenance_window_ci_ci_id_fkey FOREIGN KEY (ci_id) REFERENCES ci (id) ON DELETE CASCADE,
    DROP CONSTRAINT maintenance_window_ci_maintenance_window_id_fkey,
    ADD CONSTRAINT maintenance_window_ci_maintenance_window_id_fkey FOREIGN KEY (maintenance_window_id) REFERENCES maintenance_window (id) ON DELETE CASCADE;
ALTER TABLE notification_delivery
    DROP CONSTRAINT notification_delivery_channel_id_fkey,
    ADD CONSTRAINT notification_delivery_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES notification_channel (id) ON DELETE CASCADE,
    DROP CONSTRAINT notification_delivery_recipient_user_id_fkey,
    ADD CONSTRAINT notification_delivery_recipient_user_id_fkey FOREIGN KEY (recipient_user_id) REFERENCES app_user (id) ON DELETE CASCADE;
ALTER TABLE notification_digest_item
    DROP CONSTRAINT notification_digest_item_channel_id_fkey,
    ADD CONSTRAINT notification_digest_item_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES notification_channel (id) ON DELETE CASCADE,
    DROP CONSTRAINT notification_digest_item_delivery_id_fkey,
    ADD CONSTRAINT notification_digest_item_delivery_id_fkey FOREIGN KEY (delivery_id) REFERENCES notification_delivery (id) ON DELETE CASCADE,
    DROP CONSTRAINT notification_digest_item_recipient_user_id_fkey,
    ADD CONSTRAINT notification_digest_item_recipient_user_id_fkey FOREIGN KEY (recipient_user_id) REFERENCES app_user (id) ON DELETE CASCADE;
ALTER TABLE notification_inbox
    DROP CONSTRAINT notification_inbox_user_id_fkey,
    ADD CONSTRAINT notification_inbox_user_id_fkey FOREIGN KEY (user_id) REFERENCES app_user (id) ON DELETE CASCADE;
ALTER TABLE notification_preference
    DROP CONSTRAINT notification_preference_user_id_fkey,
    ADD CONSTRAINT notification_preference_user_id_fkey FOREIGN KEY (user_id) REFERENCES app_user (id) ON DELETE CASCADE;
ALTER TABLE security_finding
    DROP CONSTRAINT security_finding_ci_id_fkey,
    ADD CONSTRAINT security_finding_ci_id_fkey FOREIGN KEY (ci_id) REFERENCES ci (id) ON DELETE CASCADE;
ALTER TABLE stock_scan
    DROP CONSTRAINT stock_scan_asset_id_fkey,
    ADD CONSTRAINT stock_scan_asset_id_fkey FOREIGN KEY (asset_id) REFERENCES asset (id),
    DROP CONSTRAINT stock_scan_ci_id_fkey,
    ADD CONSTRAINT stock_scan_ci_id_fkey FOREIGN KEY (ci_id) REFERENCES ci (id),
    DROP CONSTRAINT stock_scan_scanned_by_fkey,
    ADD CONSTRAINT stock_scan_scanned_by_fkey FOREIGN KEY (scanned_by) REFERENCES app_user (id),
    DROP CONSTRAINT stock_scan_stocktake_id_fkey,
    ADD CONSTRAINT stock_scan_stocktake_id_fkey FOREIGN KEY (stocktake_id) REFERENCES stocktake (id) ON DELETE CASCADE;
ALTER TABLE stocktake
    DROP CONSTRAINT stocktake_started_by_fkey,
    ADD CONSTRAINT stocktake_started_by_fkey FOREIGN KEY (started_by) REFERENCES app_user (id);
ALTER TABLE ticket
    DROP CONSTRAINT ticket_assignee_id_fkey,
    ADD CONSTRAINT ticket_assignee_id_fkey FOREIGN KEY (assignee_id) REFERENCES app_user (id),
    DROP CONSTRAINT ticket_reporter_id_fkey,
    ADD CONSTRAINT ticket_reporter_id_fkey FOREIGN KEY (reporter_id) REFERENCES app_user (id),
    DROP CONSTRAINT ticket_team_id_fkey,
    ADD CONSTRAINT ticket_team_id_fkey FOREIGN KEY (team_id) REFERENCES team (id);
ALTER TABLE ticket_comment
    DROP CONSTRAINT ticket_comment_author_id_fkey,
    ADD CONSTRAINT ticket_comment_author_id_fkey FOREIGN KEY (author_id) REFERENCES app_user (id),
    DROP CONSTRAINT ticket_comment_ticket_id_fkey,
    ADD CONSTRAINT ticket_comment_ticket_id_fkey FOREIGN KEY (ticket_id) REFERENCES ticket (id) ON DELETE CASCADE;
ALTER TABLE ticket_sla
    DROP CONSTRAINT ticket_sla_sla_id_fkey,
    ADD CONSTRAINT ticket_sla_sla_id_fkey FOREIGN KEY (sla_id) REFERENCES sla (id) ON DELETE RESTRICT,
    DROP CONSTRAINT ticket_sla_ticket_id_fkey,
    ADD CONSTRAINT ticket_sla_ticket_id_fkey FOREIGN KEY (ticket_id) REFERENCES ticket (id) ON DELETE CASCADE;
ALTER TABLE webhook_dead_letter
    DROP CONSTRAINT webhook_dead_letter_delivery_id_fkey,
    ADD CONSTRAINT webhook_dead_letter_delivery_id_fkey FOREIGN KEY (delivery_id) REFERENCES webhook_delivery (id) ON DELETE CASCADE,
    DROP CONSTRAINT webhook_dead_letter_subscription_id_fkey,
    ADD CONSTRAINT webhook_dead_letter_subscription_id_fkey FOREIGN KEY (subscription_id) REFERENCES webhook_subscription (id) ON DELETE CASCADE;
ALTER TABLE webhook_delivery
    DROP CONSTRAINT webhook_delivery_subscription_id_fkey,
    ADD CONSTRAINT webhook_delivery_subscription_id_fkey FOREIGN KEY (subscription_id) REFERENCES webhook_subscription (id) ON DELETE CASCADE;
ALTER TABLE workflow_run
    DROP CONSTRAINT workflow_run_workflow_id_fkey,
    ADD CONSTRAINT workflow_run_workflow_id_fkey FOREIGN KEY (workflow_id) REFERENCES workflow_def (id) ON DELETE CASCADE;
ALTER TABLE workflow_step
    DROP CONSTRAINT workflow_step_run_id_fkey,
    ADD CONSTRAINT workflow_step_run_id_fkey FOREIGN KEY (run_id) REFERENCES workflow_run (id) ON DELETE CASCADE;

-- A unique key the up migration added for the composite keys goes when no
-- foreign key references it any more (a later migration may use it).
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'ai_conversation'::regclass AND conname = 'ai_conversation_id_organization_key')
       AND NOT EXISTS (
        SELECT 1 FROM pg_constraint f
         WHERE f.contype = 'f' AND f.confrelid = 'ai_conversation'::regclass
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = f.confrelid AND a.attnum = ANY (f.confkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE ai_conversation DROP CONSTRAINT ai_conversation_id_organization_key;
    END IF;
END
$$;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'compliance_rule'::regclass AND conname = 'compliance_rule_id_organization_key')
       AND NOT EXISTS (
        SELECT 1 FROM pg_constraint f
         WHERE f.contype = 'f' AND f.confrelid = 'compliance_rule'::regclass
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = f.confrelid AND a.attnum = ANY (f.confkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE compliance_rule DROP CONSTRAINT compliance_rule_id_organization_key;
    END IF;
END
$$;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'form_def'::regclass AND conname = 'form_def_id_organization_key')
       AND NOT EXISTS (
        SELECT 1 FROM pg_constraint f
         WHERE f.contype = 'f' AND f.confrelid = 'form_def'::regclass
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = f.confrelid AND a.attnum = ANY (f.confkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE form_def DROP CONSTRAINT form_def_id_organization_key;
    END IF;
END
$$;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'iga_connector'::regclass AND conname = 'iga_connector_id_organization_key')
       AND NOT EXISTS (
        SELECT 1 FROM pg_constraint f
         WHERE f.contype = 'f' AND f.confrelid = 'iga_connector'::regclass
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = f.confrelid AND a.attnum = ANY (f.confkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE iga_connector DROP CONSTRAINT iga_connector_id_organization_key;
    END IF;
END
$$;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'workflow_run'::regclass AND conname = 'workflow_run_id_organization_key')
       AND NOT EXISTS (
        SELECT 1 FROM pg_constraint f
         WHERE f.contype = 'f' AND f.confrelid = 'workflow_run'::regclass
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = f.confrelid AND a.attnum = ANY (f.confkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE workflow_run DROP CONSTRAINT workflow_run_id_organization_key;
    END IF;
END
$$;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'iga_access_review'::regclass AND conname = 'iga_access_review_id_organization_key')
       AND NOT EXISTS (
        SELECT 1 FROM pg_constraint f
         WHERE f.contype = 'f' AND f.confrelid = 'iga_access_review'::regclass
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = f.confrelid AND a.attnum = ANY (f.confkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE iga_access_review DROP CONSTRAINT iga_access_review_id_organization_key;
    END IF;
END
$$;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'credential'::regclass AND conname = 'credential_id_organization_key')
       AND NOT EXISTS (
        SELECT 1 FROM pg_constraint f
         WHERE f.contype = 'f' AND f.confrelid = 'credential'::regclass
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = f.confrelid AND a.attnum = ANY (f.confkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE credential DROP CONSTRAINT credential_id_organization_key;
    END IF;
END
$$;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'iga_provisioning_task'::regclass AND conname = 'iga_provisioning_task_id_organization_key')
       AND NOT EXISTS (
        SELECT 1 FROM pg_constraint f
         WHERE f.contype = 'f' AND f.confrelid = 'iga_provisioning_task'::regclass
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = f.confrelid AND a.attnum = ANY (f.confkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE iga_provisioning_task DROP CONSTRAINT iga_provisioning_task_id_organization_key;
    END IF;
END
$$;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'maintenance_window'::regclass AND conname = 'maintenance_window_id_organization_key')
       AND NOT EXISTS (
        SELECT 1 FROM pg_constraint f
         WHERE f.contype = 'f' AND f.confrelid = 'maintenance_window'::regclass
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = f.confrelid AND a.attnum = ANY (f.confkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE maintenance_window DROP CONSTRAINT maintenance_window_id_organization_key;
    END IF;
END
$$;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'notification_channel'::regclass AND conname = 'notification_channel_id_organization_key')
       AND NOT EXISTS (
        SELECT 1 FROM pg_constraint f
         WHERE f.contype = 'f' AND f.confrelid = 'notification_channel'::regclass
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = f.confrelid AND a.attnum = ANY (f.confkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE notification_channel DROP CONSTRAINT notification_channel_id_organization_key;
    END IF;
END
$$;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'notification_delivery'::regclass AND conname = 'notification_delivery_id_organization_key')
       AND NOT EXISTS (
        SELECT 1 FROM pg_constraint f
         WHERE f.contype = 'f' AND f.confrelid = 'notification_delivery'::regclass
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = f.confrelid AND a.attnum = ANY (f.confkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE notification_delivery DROP CONSTRAINT notification_delivery_id_organization_key;
    END IF;
END
$$;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'sla'::regclass AND conname = 'sla_id_organization_key')
       AND NOT EXISTS (
        SELECT 1 FROM pg_constraint f
         WHERE f.contype = 'f' AND f.confrelid = 'sla'::regclass
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = f.confrelid AND a.attnum = ANY (f.confkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE sla DROP CONSTRAINT sla_id_organization_key;
    END IF;
END
$$;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'webhook_delivery'::regclass AND conname = 'webhook_delivery_id_organization_key')
       AND NOT EXISTS (
        SELECT 1 FROM pg_constraint f
         WHERE f.contype = 'f' AND f.confrelid = 'webhook_delivery'::regclass
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = f.confrelid AND a.attnum = ANY (f.confkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE webhook_delivery DROP CONSTRAINT webhook_delivery_id_organization_key;
    END IF;
END
$$;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'webhook_subscription'::regclass AND conname = 'webhook_subscription_id_organization_key')
       AND NOT EXISTS (
        SELECT 1 FROM pg_constraint f
         WHERE f.contype = 'f' AND f.confrelid = 'webhook_subscription'::regclass
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = f.confrelid AND a.attnum = ANY (f.confkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE webhook_subscription DROP CONSTRAINT webhook_subscription_id_organization_key;
    END IF;
END
$$;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'workflow_def'::regclass AND conname = 'workflow_def_id_organization_key')
       AND NOT EXISTS (
        SELECT 1 FROM pg_constraint f
         WHERE f.contype = 'f' AND f.confrelid = 'workflow_def'::regclass
           AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
                 WHERE a.attrelid = f.confrelid AND a.attnum = ANY (f.confkey)) = ARRAY['id', 'organization_id']
    ) THEN
        ALTER TABLE workflow_def DROP CONSTRAINT workflow_def_id_organization_key;
    END IF;
END
$$;

-- The recorded rows come back as they were. The triggers of the tables
-- would refuse or rewrite a reference into another organization, so they
-- are off while the rows are restored; the foreign keys stay checked. A
-- reference whose target has been deleted since stays NULL, a row whose
-- organization or target has been deleted since does not come back.
ALTER TABLE ai_conversation DISABLE TRIGGER USER;
ALTER TABLE ai_message DISABLE TRIGGER USER;
ALTER TABLE compliance_result DISABLE TRIGGER USER;
ALTER TABLE document DISABLE TRIGGER USER;
ALTER TABLE document_link DISABLE TRIGGER USER;
ALTER TABLE form_submission DISABLE TRIGGER USER;
ALTER TABLE iga_access_request DISABLE TRIGGER USER;
ALTER TABLE iga_access_review_item DISABLE TRIGGER USER;
ALTER TABLE iga_connector DISABLE TRIGGER USER;
ALTER TABLE iga_drift_finding DISABLE TRIGGER USER;
ALTER TABLE iga_provisioning_task DISABLE TRIGGER USER;
ALTER TABLE internal_order_item DISABLE TRIGGER USER;
ALTER TABLE maintenance_notification DISABLE TRIGGER USER;
ALTER TABLE maintenance_window_ci DISABLE TRIGGER USER;
ALTER TABLE notification_delivery DISABLE TRIGGER USER;
ALTER TABLE notification_digest_item DISABLE TRIGGER USER;
ALTER TABLE notification_inbox DISABLE TRIGGER USER;
ALTER TABLE notification_preference DISABLE TRIGGER USER;
ALTER TABLE security_finding DISABLE TRIGGER USER;
ALTER TABLE stock_scan DISABLE TRIGGER USER;
ALTER TABLE stocktake DISABLE TRIGGER USER;
ALTER TABLE ticket DISABLE TRIGGER USER;
ALTER TABLE ticket_comment DISABLE TRIGGER USER;
ALTER TABLE ticket_sla DISABLE TRIGGER USER;
ALTER TABLE webhook_dead_letter DISABLE TRIGGER USER;
ALTER TABLE webhook_delivery DISABLE TRIGGER USER;
ALTER TABLE workflow_run DISABLE TRIGGER USER;
ALTER TABLE workflow_step DISABLE TRIGGER USER;
UPDATE ai_conversation t SET user_id = (q.row_data ->> 'user_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000288' AND q.source_table = 'ai_conversation'
   AND q.reason = 'SR-03 (WP-877): ai_conversation.user_id references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM app_user x WHERE x.id = (q.row_data ->> 'user_id')::uuid);
UPDATE form_submission t SET ci_id = (q.row_data ->> 'ci_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000288' AND q.source_table = 'form_submission'
   AND q.reason = 'SR-03 (WP-877): form_submission.ci_id references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM ci x WHERE x.id = (q.row_data ->> 'ci_id')::uuid);
UPDATE form_submission t SET submitted_by = (q.row_data ->> 'submitted_by')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000288' AND q.source_table = 'form_submission'
   AND q.reason = 'SR-03 (WP-877): form_submission.submitted_by references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM app_user x WHERE x.id = (q.row_data ->> 'submitted_by')::uuid);
UPDATE form_submission t SET ticket_id = (q.row_data ->> 'ticket_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000288' AND q.source_table = 'form_submission'
   AND q.reason = 'SR-03 (WP-877): form_submission.ticket_id references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM ticket x WHERE x.id = (q.row_data ->> 'ticket_id')::uuid);
UPDATE iga_access_request t SET connector_id = (q.row_data ->> 'connector_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000288' AND q.source_table = 'iga_access_request'
   AND q.reason = 'SR-03 (WP-877): iga_access_request.connector_id references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM iga_connector x WHERE x.id = (q.row_data ->> 'connector_id')::uuid);
UPDATE iga_access_request t SET decision_by = (q.row_data ->> 'decision_by')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000288' AND q.source_table = 'iga_access_request'
   AND q.reason = 'SR-03 (WP-877): iga_access_request.decision_by references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM app_user x WHERE x.id = (q.row_data ->> 'decision_by')::uuid);
UPDATE iga_access_request t SET requester_id = (q.row_data ->> 'requester_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000288' AND q.source_table = 'iga_access_request'
   AND q.reason = 'SR-03 (WP-877): iga_access_request.requester_id references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM app_user x WHERE x.id = (q.row_data ->> 'requester_id')::uuid);
UPDATE iga_access_request t SET workflow_run_id = (q.row_data ->> 'workflow_run_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000288' AND q.source_table = 'iga_access_request'
   AND q.reason = 'SR-03 (WP-877): iga_access_request.workflow_run_id references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM workflow_run x WHERE x.id = (q.row_data ->> 'workflow_run_id')::uuid);
UPDATE iga_access_review_item t SET connector_id = (q.row_data ->> 'connector_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000288' AND q.source_table = 'iga_access_review_item'
   AND q.reason = 'SR-03 (WP-877): iga_access_review_item.connector_id references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM iga_connector x WHERE x.id = (q.row_data ->> 'connector_id')::uuid);
UPDATE iga_access_review_item t SET decision_by = (q.row_data ->> 'decision_by')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000288' AND q.source_table = 'iga_access_review_item'
   AND q.reason = 'SR-03 (WP-877): iga_access_review_item.decision_by references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM app_user x WHERE x.id = (q.row_data ->> 'decision_by')::uuid);
UPDATE iga_connector t SET collector_id = (q.row_data ->> 'collector_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000288' AND q.source_table = 'iga_connector'
   AND q.reason = 'SR-03 (WP-877): iga_connector.collector_id references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM collector x WHERE x.id = (q.row_data ->> 'collector_id')::uuid);
UPDATE iga_connector t SET credential_id = (q.row_data ->> 'credential_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000288' AND q.source_table = 'iga_connector'
   AND q.reason = 'SR-03 (WP-877): iga_connector.credential_id references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM credential x WHERE x.id = (q.row_data ->> 'credential_id')::uuid);
UPDATE iga_drift_finding t SET remediation_task_id = (q.row_data ->> 'remediation_task_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000288' AND q.source_table = 'iga_drift_finding'
   AND q.reason = 'SR-03 (WP-877): iga_drift_finding.remediation_task_id references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM iga_provisioning_task x WHERE x.id = (q.row_data ->> 'remediation_task_id')::uuid);
UPDATE iga_drift_finding t SET user_id = (q.row_data ->> 'user_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000288' AND q.source_table = 'iga_drift_finding'
   AND q.reason = 'SR-03 (WP-877): iga_drift_finding.user_id references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM app_user x WHERE x.id = (q.row_data ->> 'user_id')::uuid);
UPDATE iga_provisioning_task t SET user_id = (q.row_data ->> 'user_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000288' AND q.source_table = 'iga_provisioning_task'
   AND q.reason = 'SR-03 (WP-877): iga_provisioning_task.user_id references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM app_user x WHERE x.id = (q.row_data ->> 'user_id')::uuid);
UPDATE notification_delivery t SET recipient_user_id = (q.row_data ->> 'recipient_user_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000288' AND q.source_table = 'notification_delivery'
   AND q.reason = 'SR-03 (WP-877): notification_delivery.recipient_user_id references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM app_user x WHERE x.id = (q.row_data ->> 'recipient_user_id')::uuid);
UPDATE notification_digest_item t SET delivery_id = (q.row_data ->> 'delivery_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000288' AND q.source_table = 'notification_digest_item'
   AND q.reason = 'SR-03 (WP-877): notification_digest_item.delivery_id references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM notification_delivery x WHERE x.id = (q.row_data ->> 'delivery_id')::uuid);
UPDATE security_finding t SET ci_id = (q.row_data ->> 'ci_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000288' AND q.source_table = 'security_finding'
   AND q.reason = 'SR-03 (WP-877): security_finding.ci_id references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM ci x WHERE x.id = (q.row_data ->> 'ci_id')::uuid);
UPDATE stock_scan t SET asset_id = (q.row_data ->> 'asset_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000288' AND q.source_table = 'stock_scan'
   AND q.reason = 'SR-03 (WP-877): stock_scan.asset_id references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM asset x WHERE x.id = (q.row_data ->> 'asset_id')::uuid);
UPDATE stock_scan t SET ci_id = (q.row_data ->> 'ci_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000288' AND q.source_table = 'stock_scan'
   AND q.reason = 'SR-03 (WP-877): stock_scan.ci_id references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM ci x WHERE x.id = (q.row_data ->> 'ci_id')::uuid);
UPDATE stocktake t SET started_by = (q.row_data ->> 'started_by')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000288' AND q.source_table = 'stocktake'
   AND q.reason = 'SR-03 (WP-877): stocktake.started_by references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM app_user x WHERE x.id = (q.row_data ->> 'started_by')::uuid);
UPDATE ticket t SET assignee_id = (q.row_data ->> 'assignee_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000288' AND q.source_table = 'ticket'
   AND q.reason = 'SR-03 (WP-877): ticket.assignee_id references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM app_user x WHERE x.id = (q.row_data ->> 'assignee_id')::uuid);
UPDATE ticket t SET team_id = (q.row_data ->> 'team_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000288' AND q.source_table = 'ticket'
   AND q.reason = 'SR-03 (WP-877): ticket.team_id references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM team x WHERE x.id = (q.row_data ->> 'team_id')::uuid);
UPDATE ticket_comment t SET author_id = (q.row_data ->> 'author_id')::uuid
  FROM migration_quarantine q
 WHERE q.migration = '000288' AND q.source_table = 'ticket_comment'
   AND q.reason = 'SR-03 (WP-877): ticket_comment.author_id references another organization, set to NULL'
   AND t.id::text = q.row_data ->> 'id'
   AND EXISTS (SELECT 1 FROM app_user x WHERE x.id = (q.row_data ->> 'author_id')::uuid);
INSERT INTO ai_message
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::ai_message, q.row_data) AS r
 WHERE q.migration = '000288' AND q.source_table = 'ai_message'
   AND q.reason = 'SR-03 (WP-877): ai_message.conversation_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM ai_conversation x WHERE x.id = r.conversation_id)
   AND NOT EXISTS (SELECT 1 FROM ai_message e WHERE e.id = r.id);
INSERT INTO compliance_result
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::compliance_result, q.row_data) AS r
 WHERE q.migration = '000288' AND q.source_table = 'compliance_result'
   AND q.reason = 'SR-03 (WP-877): compliance_result.ci_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM ci x WHERE x.id = r.ci_id)
   AND NOT EXISTS (SELECT 1 FROM compliance_result e WHERE e.id = r.id);
INSERT INTO compliance_result
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::compliance_result, q.row_data) AS r
 WHERE q.migration = '000288' AND q.source_table = 'compliance_result'
   AND q.reason = 'SR-03 (WP-877): compliance_result.rule_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM compliance_rule x WHERE x.id = r.rule_id)
   AND NOT EXISTS (SELECT 1 FROM compliance_result e WHERE e.id = r.id);
INSERT INTO document
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::document, q.row_data) AS r
 WHERE q.migration = '000288' AND q.source_table = 'document'
   AND q.reason = 'SR-03 (WP-877): document.uploaded_by references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM app_user x WHERE x.id = r.uploaded_by)
   AND NOT EXISTS (SELECT 1 FROM document e WHERE e.id = r.id);
INSERT INTO document_link
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::document_link, q.row_data) AS r
 WHERE q.migration = '000288' AND q.source_table = 'document_link'
   AND q.reason = 'SR-03 (WP-877): document_link.document_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM document x WHERE x.id = r.document_id)
   AND NOT EXISTS (SELECT 1 FROM document_link e WHERE e.id = r.id);
INSERT INTO form_submission
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::form_submission, q.row_data) AS r
 WHERE q.migration = '000288' AND q.source_table = 'form_submission'
   AND q.reason = 'SR-03 (WP-877): form_submission.form_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM form_def x WHERE x.id = r.form_id)
   AND NOT EXISTS (SELECT 1 FROM form_submission e WHERE e.id = r.id);
INSERT INTO iga_access_request
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::iga_access_request, q.row_data) AS r
 WHERE q.migration = '000288' AND q.source_table = 'iga_access_request'
   AND q.reason = 'SR-03 (WP-877): iga_access_request.subject_user_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM app_user x WHERE x.id = r.subject_user_id)
   AND NOT EXISTS (SELECT 1 FROM iga_access_request e WHERE e.id = r.id);
INSERT INTO iga_access_review_item
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::iga_access_review_item, q.row_data) AS r
 WHERE q.migration = '000288' AND q.source_table = 'iga_access_review_item'
   AND q.reason = 'SR-03 (WP-877): iga_access_review_item.review_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM iga_access_review x WHERE x.id = r.review_id)
   AND NOT EXISTS (SELECT 1 FROM iga_access_review_item e WHERE e.id = r.id);
INSERT INTO iga_access_review_item
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::iga_access_review_item, q.row_data) AS r
 WHERE q.migration = '000288' AND q.source_table = 'iga_access_review_item'
   AND q.reason = 'SR-03 (WP-877): iga_access_review_item.user_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM app_user x WHERE x.id = r.user_id)
   AND NOT EXISTS (SELECT 1 FROM iga_access_review_item e WHERE e.id = r.id);
INSERT INTO iga_drift_finding
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::iga_drift_finding, q.row_data) AS r
 WHERE q.migration = '000288' AND q.source_table = 'iga_drift_finding'
   AND q.reason = 'SR-03 (WP-877): iga_drift_finding.connector_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM iga_connector x WHERE x.id = r.connector_id)
   AND NOT EXISTS (SELECT 1 FROM iga_drift_finding e WHERE e.id = r.id);
INSERT INTO iga_provisioning_task
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::iga_provisioning_task, q.row_data) AS r
 WHERE q.migration = '000288' AND q.source_table = 'iga_provisioning_task'
   AND q.reason = 'SR-03 (WP-877): iga_provisioning_task.connector_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM iga_connector x WHERE x.id = r.connector_id)
   AND NOT EXISTS (SELECT 1 FROM iga_provisioning_task e WHERE e.id = r.id);
INSERT INTO internal_order_item
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::internal_order_item, q.row_data) AS r
 WHERE q.migration = '000288' AND q.source_table = 'internal_order_item'
   AND q.reason = 'SR-03 (WP-877): internal_order_item.order_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM internal_order x WHERE x.id = r.order_id)
   AND NOT EXISTS (SELECT 1 FROM internal_order_item e WHERE e.id = r.id);
INSERT INTO maintenance_notification
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::maintenance_notification, q.row_data) AS r
 WHERE q.migration = '000288' AND q.source_table = 'maintenance_notification'
   AND q.reason = 'SR-03 (WP-877): maintenance_notification.maintenance_window_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM maintenance_window x WHERE x.id = r.maintenance_window_id)
   AND NOT EXISTS (SELECT 1 FROM maintenance_notification e WHERE e.id = r.id);
INSERT INTO maintenance_window_ci
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::maintenance_window_ci, q.row_data) AS r
 WHERE q.migration = '000288' AND q.source_table = 'maintenance_window_ci'
   AND q.reason = 'SR-03 (WP-877): maintenance_window_ci.ci_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM ci x WHERE x.id = r.ci_id)
   AND NOT EXISTS (SELECT 1 FROM maintenance_window_ci e WHERE e.id = r.id);
INSERT INTO maintenance_window_ci
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::maintenance_window_ci, q.row_data) AS r
 WHERE q.migration = '000288' AND q.source_table = 'maintenance_window_ci'
   AND q.reason = 'SR-03 (WP-877): maintenance_window_ci.maintenance_window_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM maintenance_window x WHERE x.id = r.maintenance_window_id)
   AND NOT EXISTS (SELECT 1 FROM maintenance_window_ci e WHERE e.id = r.id);
INSERT INTO notification_delivery
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::notification_delivery, q.row_data) AS r
 WHERE q.migration = '000288' AND q.source_table = 'notification_delivery'
   AND q.reason = 'SR-03 (WP-877): notification_delivery.channel_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM notification_channel x WHERE x.id = r.channel_id)
   AND NOT EXISTS (SELECT 1 FROM notification_delivery e WHERE e.id = r.id);
INSERT INTO notification_digest_item
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::notification_digest_item, q.row_data) AS r
 WHERE q.migration = '000288' AND q.source_table = 'notification_digest_item'
   AND q.reason = 'SR-03 (WP-877): notification_digest_item.channel_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM notification_channel x WHERE x.id = r.channel_id)
   AND NOT EXISTS (SELECT 1 FROM notification_digest_item e WHERE e.id = r.id);
INSERT INTO notification_digest_item
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::notification_digest_item, q.row_data) AS r
 WHERE q.migration = '000288' AND q.source_table = 'notification_digest_item'
   AND q.reason = 'SR-03 (WP-877): notification_digest_item.recipient_user_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM app_user x WHERE x.id = r.recipient_user_id)
   AND NOT EXISTS (SELECT 1 FROM notification_digest_item e WHERE e.id = r.id);
INSERT INTO notification_inbox
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::notification_inbox, q.row_data) AS r
 WHERE q.migration = '000288' AND q.source_table = 'notification_inbox'
   AND q.reason = 'SR-03 (WP-877): notification_inbox.user_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM app_user x WHERE x.id = r.user_id)
   AND NOT EXISTS (SELECT 1 FROM notification_inbox e WHERE e.id = r.id);
INSERT INTO notification_preference
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::notification_preference, q.row_data) AS r
 WHERE q.migration = '000288' AND q.source_table = 'notification_preference'
   AND q.reason = 'SR-03 (WP-877): notification_preference.user_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM app_user x WHERE x.id = r.user_id)
   AND NOT EXISTS (SELECT 1 FROM notification_preference e WHERE e.organization_id = r.organization_id AND e.user_id = r.user_id);
INSERT INTO stock_scan
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::stock_scan, q.row_data) AS r
 WHERE q.migration = '000288' AND q.source_table = 'stock_scan'
   AND q.reason = 'SR-03 (WP-877): stock_scan.scanned_by references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM app_user x WHERE x.id = r.scanned_by)
   AND NOT EXISTS (SELECT 1 FROM stock_scan e WHERE e.id = r.id);
INSERT INTO stock_scan
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::stock_scan, q.row_data) AS r
 WHERE q.migration = '000288' AND q.source_table = 'stock_scan'
   AND q.reason = 'SR-03 (WP-877): stock_scan.stocktake_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM stocktake x WHERE x.id = r.stocktake_id)
   AND NOT EXISTS (SELECT 1 FROM stock_scan e WHERE e.id = r.id);
INSERT INTO ticket
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::ticket, q.row_data) AS r
 WHERE q.migration = '000288' AND q.source_table = 'ticket'
   AND q.reason = 'SR-03 (WP-877): ticket.reporter_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM app_user x WHERE x.id = r.reporter_id)
   AND NOT EXISTS (SELECT 1 FROM ticket e WHERE e.id = r.id);
INSERT INTO ticket_comment
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::ticket_comment, q.row_data) AS r
 WHERE q.migration = '000288' AND q.source_table = 'ticket_comment'
   AND q.reason = 'SR-03 (WP-877): ticket_comment.ticket_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM ticket x WHERE x.id = r.ticket_id)
   AND NOT EXISTS (SELECT 1 FROM ticket_comment e WHERE e.id = r.id);
INSERT INTO ticket_sla
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::ticket_sla, q.row_data) AS r
 WHERE q.migration = '000288' AND q.source_table = 'ticket_sla'
   AND q.reason = 'SR-03 (WP-877): ticket_sla.sla_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM sla x WHERE x.id = r.sla_id)
   AND NOT EXISTS (SELECT 1 FROM ticket_sla e WHERE e.id = r.id);
INSERT INTO ticket_sla
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::ticket_sla, q.row_data) AS r
 WHERE q.migration = '000288' AND q.source_table = 'ticket_sla'
   AND q.reason = 'SR-03 (WP-877): ticket_sla.ticket_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM ticket x WHERE x.id = r.ticket_id)
   AND NOT EXISTS (SELECT 1 FROM ticket_sla e WHERE e.id = r.id);
INSERT INTO webhook_dead_letter
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::webhook_dead_letter, q.row_data) AS r
 WHERE q.migration = '000288' AND q.source_table = 'webhook_dead_letter'
   AND q.reason = 'SR-03 (WP-877): webhook_dead_letter.delivery_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM webhook_delivery x WHERE x.id = r.delivery_id)
   AND NOT EXISTS (SELECT 1 FROM webhook_dead_letter e WHERE e.id = r.id);
INSERT INTO webhook_dead_letter
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::webhook_dead_letter, q.row_data) AS r
 WHERE q.migration = '000288' AND q.source_table = 'webhook_dead_letter'
   AND q.reason = 'SR-03 (WP-877): webhook_dead_letter.subscription_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM webhook_subscription x WHERE x.id = r.subscription_id)
   AND NOT EXISTS (SELECT 1 FROM webhook_dead_letter e WHERE e.id = r.id);
INSERT INTO webhook_delivery
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::webhook_delivery, q.row_data) AS r
 WHERE q.migration = '000288' AND q.source_table = 'webhook_delivery'
   AND q.reason = 'SR-03 (WP-877): webhook_delivery.subscription_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM webhook_subscription x WHERE x.id = r.subscription_id)
   AND NOT EXISTS (SELECT 1 FROM webhook_delivery e WHERE e.id = r.id);
INSERT INTO workflow_run
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::workflow_run, q.row_data) AS r
 WHERE q.migration = '000288' AND q.source_table = 'workflow_run'
   AND q.reason = 'SR-03 (WP-877): workflow_run.workflow_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM workflow_def x WHERE x.id = r.workflow_id)
   AND NOT EXISTS (SELECT 1 FROM workflow_run e WHERE e.id = r.id);
INSERT INTO workflow_step
SELECT r.*
  FROM migration_quarantine q
 CROSS JOIN LATERAL jsonb_populate_record(NULL::workflow_step, q.row_data) AS r
 WHERE q.migration = '000288' AND q.source_table = 'workflow_step'
   AND q.reason = 'SR-03 (WP-877): workflow_step.run_id references another organization, moved to migration_quarantine'
   AND EXISTS (SELECT 1 FROM organization o WHERE o.id = q.organization_id)
   AND EXISTS (SELECT 1 FROM workflow_run x WHERE x.id = r.run_id)
   AND NOT EXISTS (SELECT 1 FROM workflow_step e WHERE e.id = r.id);
ALTER TABLE ai_conversation ENABLE TRIGGER USER;
ALTER TABLE ai_message ENABLE TRIGGER USER;
ALTER TABLE compliance_result ENABLE TRIGGER USER;
ALTER TABLE document ENABLE TRIGGER USER;
ALTER TABLE document_link ENABLE TRIGGER USER;
ALTER TABLE form_submission ENABLE TRIGGER USER;
ALTER TABLE iga_access_request ENABLE TRIGGER USER;
ALTER TABLE iga_access_review_item ENABLE TRIGGER USER;
ALTER TABLE iga_connector ENABLE TRIGGER USER;
ALTER TABLE iga_drift_finding ENABLE TRIGGER USER;
ALTER TABLE iga_provisioning_task ENABLE TRIGGER USER;
ALTER TABLE internal_order_item ENABLE TRIGGER USER;
ALTER TABLE maintenance_notification ENABLE TRIGGER USER;
ALTER TABLE maintenance_window_ci ENABLE TRIGGER USER;
ALTER TABLE notification_delivery ENABLE TRIGGER USER;
ALTER TABLE notification_digest_item ENABLE TRIGGER USER;
ALTER TABLE notification_inbox ENABLE TRIGGER USER;
ALTER TABLE notification_preference ENABLE TRIGGER USER;
ALTER TABLE security_finding ENABLE TRIGGER USER;
ALTER TABLE stock_scan ENABLE TRIGGER USER;
ALTER TABLE stocktake ENABLE TRIGGER USER;
ALTER TABLE ticket ENABLE TRIGGER USER;
ALTER TABLE ticket_comment ENABLE TRIGGER USER;
ALTER TABLE ticket_sla ENABLE TRIGGER USER;
ALTER TABLE webhook_dead_letter ENABLE TRIGGER USER;
ALTER TABLE webhook_delivery ENABLE TRIGGER USER;
ALTER TABLE workflow_run ENABLE TRIGGER USER;
ALTER TABLE workflow_step ENABLE TRIGGER USER;

DELETE FROM migration_quarantine WHERE migration = '000288';
