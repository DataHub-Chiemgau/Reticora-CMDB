-- Complete updated_at coverage: attach the shared set_updated_at() trigger
-- (created in 000018) to every mutable table that carries an updated_at
-- column. Append-only tables (audit_log, ci_change, stock_scan, org_dek,
-- relationship_suppression, webhook_dead_letter) intentionally keep their
-- immutable rows and are excluded.

CREATE TRIGGER trg_entitlement_updated_at BEFORE UPDATE ON entitlement
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_role_updated_at BEFORE UPDATE ON role
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_ci_relationship_updated_at BEFORE UPDATE ON ci_relationship
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_webhook_subscription_updated_at BEFORE UPDATE ON webhook_subscription
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_webhook_delivery_updated_at BEFORE UPDATE ON webhook_delivery
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_asset_updated_at BEFORE UPDATE ON asset
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_assignment_updated_at BEFORE UPDATE ON assignment
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_document_updated_at BEFORE UPDATE ON document
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_stocktake_updated_at BEFORE UPDATE ON stocktake
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_ticket_updated_at BEFORE UPDATE ON ticket
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_ticket_comment_updated_at BEFORE UPDATE ON ticket_comment
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_team_updated_at BEFORE UPDATE ON team
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_custom_role_updated_at BEFORE UPDATE ON custom_role
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_network_interface_updated_at BEFORE UPDATE ON network_interface
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_subnet_updated_at BEFORE UPDATE ON subnet
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_ip_address_updated_at BEFORE UPDATE ON ip_address
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_cable_updated_at BEFORE UPDATE ON cable
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_contact_updated_at BEFORE UPDATE ON contact
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_export_job_updated_at BEFORE UPDATE ON export_job
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_api_key_updated_at BEFORE UPDATE ON api_key
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_rack_mount_updated_at BEFORE UPDATE ON rack_mount
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_review_item_updated_at BEFORE UPDATE ON review_item
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_credential_updated_at BEFORE UPDATE ON credential
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_permission_updated_at BEFORE UPDATE ON permission
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_sla_updated_at BEFORE UPDATE ON sla
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_ticket_sla_updated_at BEFORE UPDATE ON ticket_sla
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_form_def_updated_at BEFORE UPDATE ON form_def
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_form_submission_updated_at BEFORE UPDATE ON form_submission
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_workflow_def_updated_at BEFORE UPDATE ON workflow_def
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_workflow_run_updated_at BEFORE UPDATE ON workflow_run
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_workflow_step_updated_at BEFORE UPDATE ON workflow_step
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_compliance_rule_updated_at BEFORE UPDATE ON compliance_rule
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_compliance_result_updated_at BEFORE UPDATE ON compliance_result
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_iga_connector_updated_at BEFORE UPDATE ON iga_connector
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_iga_provisioning_task_updated_at BEFORE UPDATE ON iga_provisioning_task
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_iga_lifecycle_policy_updated_at BEFORE UPDATE ON iga_lifecycle_policy
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_iga_access_request_updated_at BEFORE UPDATE ON iga_access_request
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_iga_access_review_updated_at BEFORE UPDATE ON iga_access_review
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_iga_access_review_item_updated_at BEFORE UPDATE ON iga_access_review_item
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_iga_drift_finding_updated_at BEFORE UPDATE ON iga_drift_finding
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_search_document_updated_at BEFORE UPDATE ON search_document
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_ai_conversation_updated_at BEFORE UPDATE ON ai_conversation
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_ai_chunk_updated_at BEFORE UPDATE ON ai_chunk
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
