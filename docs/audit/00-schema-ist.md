# Audit Teil 0 – migrationsbasiertes Ist-Schema

## Grundlage und Zählweise

Ausschließliche Quelle: die **114 SQL-Dateien** unter `backend/migrations/`: **57 Up-/Down-Paare**, lückenlos `000001`–`000057`. Beschrieben wird der kumulative Zustand nach erfolgreicher Ausführung aller Up-Migrationen auf einer frischen Datenbank, nicht ein verifizierter Laufzeit-/Produktionszustand. SQL-Kommentare über Repository-Code, Deployment oder angeblich erzwungene Eigenschaften sind kein Nachweis dafür.

Das Inventar umfasst **105 explizit angelegte Tabellen**, darunter die Hypertable `metric_sample`, sowie separat **eine materialisierte Continuous-Aggregate-View** (`metric_sample_1h`). TimescaleDB-interne Tabellen/Chunks und eine etwaige Migrationstool-Verwaltungstabelle werden nicht als migrationsdefinierte Anwendungstabellen hinzugerechnet.

| Merkmal | Kumulativer Bestand |
|---|---:|
| Tabellen mit `organization_id NOT NULL` | 95 |
| Tabellen mit nullable `organization_id` | 5 |
| Tabellen ohne `organization_id` | 5 |
| Tabellen mit `client_id` | 15: 1 NOT NULL, 14 nullable |
| Tabellen mit `site_id` | 4: 1 NOT NULL, 3 nullable |
| Tabellen mit `owner_team_id` | 0 |
| Tabellen mit RLS ENABLE und FORCE | 103 |
| Tabellen ohne RLS | 2: `permission`, `metric_sample` |
| Tabellen mit direkter Client-Scope-Policy | 7, einschließlich `client` über dessen `id` |
| Tabellen mit eigenständiger Site-/Team-Scope-Policy | 0 / 0 |
| Explizit angelegte PostgreSQL-Rollen / Extensions | 1 / 3 |
| CI-Typ-Keys / Relationship-Typ-Keys / Permission-Keys als Seeds | 17 / 24 / 95 |
| Standard-Anwendungsrollen als Seed-Vorlagen | 4 je Organisation |

### Leseschlüssel für die Tabelle

- Spalten: **NN** = vorhanden und `NOT NULL`; **NULL** = vorhanden, nullable; **–** = Spalte fehlt. `organization.id`, `scope_client_id`, `scope_site_id`, `team_id` und `owner_id` werden nicht als gleichnamige Scope-Spalten gezählt.
- **O** = `organization_id = current_setting('app.org_id')::uuid`; **O(id)** prüft stattdessen `organization.id`.
- **OC** = O **UND** Client-Filter: fehlendes/leeres `app.client_scope` lässt alle Clients zu; andernfalls `client_id IS NULL` oder enthalten in der kommaseparierten UUID-Liste. **OC(id)** verwendet `client.id` ohne NULL-Zweig. Bei `site.client_id` ist der NULL-Zweig wegen NOT NULL unerreichbar.
- **G** = `organization_id IS NULL OR O`. **S** = Organisation passt über `NULLIF(current_setting('app.org_id', true), '')::uuid` **ODER** `app.system = 'on'`.
- **P(…)** = indirekte Prüfung durch `EXISTS` auf der genannten Elterntabelle; kein eigener Client-/Site-/Team-Scope, sofern nicht ausdrücklich angegeben.
- Die Scope-Angabe ist vollständig: **O bedeutet nur Organisation**, nicht zusätzlich Client/Site/Team. Ein Fremdschlüssel allein übernimmt keine RLS-Policy der referenzierten Tabelle.
- **U** = Unique-Constraint/-Index; **I** = ausgewählte wichtige, nicht eindeutige Indizes; **EX** = Exclusion-Constraint. Ohne Sonderhinweis haben Tabellen `PRIMARY KEY(id)`; Ausnahmen stehen in der Tabelle. Normale Indizes werden nach indizierten Spalten, nicht durchgehend nach SQL-Namen angegeben.
- Die Spalte „Migration“ nennt die **Erstanlage** mit repository-relativem Pfad/Zeile. Angegebene spätere Änderungen werden in den folgenden Abschnitten mit vollständigen Quellen aufgelöst; RLS-Spalten zeigen immer den **Endzustand**, nicht nur die Erstanlage.

## Vollständiges Tabelleninventar

| Tabelle | Migration | organization_id NOT NULL | client_id | site_id | owner_team_id | RLS ENABLE | FORCE | USING deckt ab (org/client/site/team) | WITH CHECK deckt ab | wichtige Unique-/Exclusion-Constraints und Indizes |
|---|---|---|---|---|---|---|---|---|---|---|
| `organization` | `backend/migrations/000001_tenant_model.up.sql:6` | – | – | – | – | ja | ja | O(id) | O(id) | U(slug) |
| `client` | `backend/migrations/000001_tenant_model.up.sql:16` | NN | – | – | – | ja | ja | OC(id) | OC(id) | U(organization_id, slug); I(organization_id) |
| `site` | `backend/migrations/000001_tenant_model.up.sql:27` | NN | NN | – | – | ja | ja | OC | OC | U(organization_id, name), ergänzt 000021; I(organization_id), I(client_id) |
| `building` | `backend/migrations/000001_tenant_model.up.sql:38` | NN | – | NN | – | ja | ja | O | O | I(site_id) |
| `room` | `backend/migrations/000001_tenant_model.up.sql:48` | NN | – | – | – | ja | ja | O | O | I(building_id) |
| `rack` | `backend/migrations/000001_tenant_model.up.sql:59` | NN | – | – | – | ja | ja | O | O | I(room_id) |
| `ci_type` | `backend/migrations/000002_ci_type_system.up.sql:3` | NULL seit 000021 | – | – | – | ja | ja | G | O | U(organization_id, name); seit 000056 U(organization_id, key) bei org NOT NULL, U(key) bei org NULL; I(org, is_active), partiell I(template_key) |
| `ci_type_attribute` | `backend/migrations/000002_ci_type_system.up.sql:14` | – | – | – | – | ja | ja | P(ci_type: G) | P(ci_type: O) | U(ci_type_id, name); zusätzlich identischer partieller U bei scope='global' |
| `ci` | `backend/migrations/000002_ci_type_system.up.sql:27` | NN | NULL | NULL seit 000021 | – | ja | ja | OC | OC | U(id, organization_id) seit 000056; partielle Identitäts-U, GIN(attributes), GIN(FTS), I(org,type/status), I(client_id), I(last_seen_at), partiell I(location_id); Details unten |
| `audit_log` | `backend/migrations/000003_audit_log.up.sql:3` | NN | – | – | – | ja | ja | O | O | I(org,timestamp DESC), I(resource_type,resource_id), I(actor_id); Hash- und Append-only-Trigger |
| `entitlement` | `backend/migrations/000004_entitlements.up.sql:3` | NN | – | – | – | ja | ja | O | O | U(org,feature_key); I(org), I(feature_key) |
| `app_user` | `backend/migrations/000005_users_roles.up.sql:3` | NN | – | – | – | ja | ja | O | O | U(oidc_subject), nullable; I(org), I(email), E-Mail nicht UNIQUE |
| `role` | `backend/migrations/000005_users_roles.up.sql:17` | NN | – | – | – | ja | ja | O | O | U(org,name); I(org) |
| `role_assignment` | `backend/migrations/000005_users_roles.up.sql:30` | NN | – | – | – | ja | ja | O | O | U(user_id,role_id,scope_client_id,scope_site_id); beide Scope-Spalten nullable; I(user_id) |
| `ci_relationship` | `backend/migrations/000006_ci_relationships.up.sql:3` | NN | – | – | – | ja | ja | O | O | U(org,source_ci_id,target_ci_id,rel_type); I(source/target/type), I(org,source); zwei zusammengesetzte Tenant-FKs seit 000056 |
| `webhook_subscription` | `backend/migrations/000007_webhooks.up.sql:3` | NN | – | – | – | ja | ja | O | O | I(org) |
| `webhook_delivery` | `backend/migrations/000007_webhooks.up.sql:16` | NN | – | – | – | ja | ja | S | S | I(subscription_id), partiell I(status) bei status!='success'; I(next_retry_at) bei pending/retrying; I(org,subscription_id,created_at DESC) |
| `collector` | `backend/migrations/000008_discovery.up.sql:3` | NN | NULL | – | – | ja | ja | OC | OC | I(org) |
| `discovery_job` | `backend/migrations/000008_discovery.up.sql:16` | NN | – | – | – | ja | ja | O | O | I(collector_id), I(status) |
| `discovery_result` | `backend/migrations/000008_discovery.up.sql:29` | NN | – | – | – | ja | ja | O | O | I(job_id), partiell I(matched_ci_id) |
| `asset` | `backend/migrations/000009_phase2_assets.up.sql:4` | NN | NULL | – | – | ja | ja | O | O | U(org,asset_tag); seit 000051 partiell U(org,rfid_tag); seit 000055 partiell U(ci_id); seit 000056 U(id,org); I(org,status), partiell I(location_id) |
| `assignment` | `backend/migrations/000010_phase2_assignments.up.sql:4` | NN | – | – | – | ja | ja | O | O | I(org), I(assigned_to), I(asset_id), I(org,status); asset_id ODER ci_id muss gesetzt sein |
| `document` | `backend/migrations/000011_phase2_documents.up.sql:4` | NN | – | – | – | ja | ja | O | O | I(org), I(org,category) |
| `document_link` | `backend/migrations/000011_phase2_documents.up.sql:28` | NN | – | – | – | ja | ja | O | O | I(document_id), I(entity_type,entity_id); entity_id ohne FK |
| `stocktake` | `backend/migrations/000012_phase2_stocktake.up.sql:4` | NN | – | – | – | ja | ja | O | O | I(org), I(org,status) |
| `stock_scan` | `backend/migrations/000012_phase2_stocktake.up.sql:29` | NN | – | – | – | ja | ja | O | O | I(stocktake_id), I(asset_id) |
| `ticket` | `backend/migrations/000013_phase2_tickets.up.sql:4` | NN | – | – | – | ja | ja | O | O | I(org,status), I(assignee_id/reporter_id/team_id), I(org,ticket_number) nicht UNIQUE; team_id-FK seit 000014 |
| `ticket_comment` | `backend/migrations/000013_phase2_tickets.up.sql:35` | NN | – | – | – | ja | ja | O | O | I(ticket_id) |
| `team` | `backend/migrations/000014_phase2_users_teams_roles.up.sql:6` | NN | – | – | – | ja | ja | O | O | U(org,name); I(org) |
| `team_member` | `backend/migrations/000014_phase2_users_teams_roles.up.sql:19` | – | – | – | – | ja | ja | P(team: O) | P(team: O) | U(team_id,user_id); I(team_id), I(user_id); keine Prüfung der Organisation des user_id durch die Policy |
| `custom_role` | `backend/migrations/000014_phase2_users_teams_roles.up.sql:30` | NN | – | – | – | ja | ja | O | O | U(org,name); I(org) |
| `user_custom_role` | `backend/migrations/000014_phase2_users_teams_roles.up.sql:44` | – | – | – | – | ja | ja | P(custom_role: O) | P(custom_role: O) | U(user_id,custom_role_id,scope_type,scope_id), scope_id nullable; I(user_id); Scope-Werte selbst nicht durch RLS ausgewertet |
| `network_interface` | `backend/migrations/000015_network_ipam.up.sql:4` | NN | – | – | – | ja | ja | O | O | I(ci_id), I(org) |
| `subnet` | `backend/migrations/000015_network_ipam.up.sql:22` | NN | NULL | NULL | – | ja | ja | OC | OC | U(org,cidr); I(org) |
| `ip_address` | `backend/migrations/000015_network_ipam.up.sql:39` | NN | – | – | – | ja | ja | O | O | Seit 000031 U(org,interface_id,address) bei interface_id NOT NULL; U(org,address) bei interface_id NULL; I(org), I(subnet_id) |
| `cable` | `backend/migrations/000015_network_ipam.up.sql:55` | NN | – | – | – | ja | ja | O | O | I(org), I(source_interface_id), I(target_interface_id) |
| `contact` | `backend/migrations/000016_contacts.up.sql:4` | NN | NULL | – | – | ja | ja | OC | OC | I(org) |
| `ci_contact` | `backend/migrations/000016_contacts.up.sql:18` | NN | – | – | – | ja | ja | O | O | U(ci_id,contact_id,relationship_type); I(ci_id), I(contact_id) |
| `export_job` | `backend/migrations/000017_export_jobs.up.sql:4` | NN | – | – | – | ja | ja | S | S | I(org,status); partiell I(created_at,id) bei status='pending' |
| `api_key` | `backend/migrations/000019_api_keys_invitations.up.sql:4` | NN | – | – | – | ja | ja | O | O | I(org), I(key_prefix); I(org,key_prefix) bei revoked_at NULL; kein U(key_hash) |
| `user_invitation` | `backend/migrations/000019_api_keys_invitations.up.sql:19` | NN | – | – | – | ja | ja | O | O | I(org,email), zusätzlich partiell bei accepted_at NULL; kein U(token_hash) |
| `metric_sample` | `backend/migrations/000020_metrics.up.sql:6` | NN | – | – | – | nein | nein | keine | keine | Kein PK/Unique und keine FKs auf org/ci; Hypertable(time); I(ci_id,time DESC), I(metric_name,time DESC) |
| `rack_mount` | `backend/migrations/000021_spec_alignment.up.sql:112` | NN | – | – | – | ja | ja | O | O | U(ci_id); EX GiST(rack_id =, face =, int4range(position_u,position_u+height_u) &&) nur face!='both'; I(rack_id) |
| `relationship_suppression` | `backend/migrations/000021_spec_alignment.up.sql:143` | NN | – | – | – | ja | ja | O | O | U(org,source_ci_id,target_ci_id,relationship_type_id); relationship_type_id ohne FK |
| `credential` | `backend/migrations/000021_spec_alignment.up.sql:160` | NN | NULL | – | – | ja | ja | OC | OC | Außer PK keine weiteren expliziten Indizes/Unique |
| `org_dek` | `backend/migrations/000021_spec_alignment.up.sql:180` | NN | – | – | – | ja | ja | O | O | U(organization_id); RLS erst 000041 |
| `review_item` | `backend/migrations/000021_spec_alignment.up.sql:189` | NN | – | – | – | ja | ja | O | O | Außer PK keine weiteren expliziten Indizes/Unique |
| `ci_change` | `backend/migrations/000022_ci_change.up.sql:4` | NN | – | – | – | ja | ja | O | O | I(ci_id,created_at DESC), I(org), I(actor_id), I(change_type) |
| `permission` | `backend/migrations/000026_permissions_sla.up.sql:3` | – | – | – | – | nein | nein | keine | keine | PK(key); globaler Katalog; kein id-PK |
| `role_permission` | `backend/migrations/000026_permissions_sla.up.sql:55` | NN | – | – | – | ja | ja | O | O | PK(org,role_id,permission_key); I(org,role_id), I(permission_key); kein id-PK |
| `sla` | `backend/migrations/000026_permissions_sla.up.sql:67` | NN | NULL | – | – | ja | ja | O | O | U(org,client_id,priority,name), client_id nullable; I(org,priority), I(org,client_id) |
| `ticket_sla` | `backend/migrations/000026_permissions_sla.up.sql:85` | NN | – | – | – | ja | ja | O | O | U(org,ticket_id); I(org,sla_id), I(org,resolution_due_at), I(org,response_breached,resolution_breached) |
| `form_def` | `backend/migrations/000027_workflows_forms.up.sql:10` | NN | NULL | – | – | ja | ja | O | O | U(org,name); I(org,active), I(org,client_id) |
| `form_submission` | `backend/migrations/000027_workflows_forms.up.sql:26` | NN | – | – | – | ja | ja | O | O | I(org,form_id/status/ci_id/ticket_id), jeweils eigener Index |
| `workflow_def` | `backend/migrations/000027_workflows_forms.up.sql:44` | NN | – | – | – | ja | ja | O | O | U(org,name); I(org,active) |
| `workflow_run` | `backend/migrations/000027_workflows_forms.up.sql:59` | NN | – | – | – | ja | ja | O | O | I(org,workflow_id), I(org,status) |
| `workflow_step` | `backend/migrations/000027_workflows_forms.up.sql:75` | NN | – | – | – | ja | ja | O | O | U(org,run_id,step_index); I(org,run_id,step_index) |
| `compliance_rule` | `backend/migrations/000028_compliance.up.sql:8` | NN | – | – | – | ja | ja | O | O | U(org,name); I(org,active), I(org,ci_type_id), I(org,category) |
| `compliance_result` | `backend/migrations/000028_compliance.up.sql:28` | NN | – | – | – | ja | ja | O | O | I(org,ci_type_id), I(org,status), I(org,ci_id) |
| `iga_connector` | `backend/migrations/000029_iga.up.sql:8` | NN | – | – | – | ja | ja | O | O | U(org,name); I(org,type) |
| `iga_provisioning_task` | `backend/migrations/000029_iga.up.sql:27` | NN | – | – | – | ja | ja | O | O | I(org,status,next_run_at) |
| `iga_lifecycle_policy` | `backend/migrations/000029_iga.up.sql:46` | NN | – | – | – | ja | ja | O | O | U(org,name); I(org,event,active) |
| `iga_access_request` | `backend/migrations/000029_iga.up.sql:60` | NN | – | – | – | ja | ja | O | O | I(org,status) |
| `iga_access_review` | `backend/migrations/000029_iga.up.sql:77` | NN | – | – | – | ja | ja | O | O | I(org,status) |
| `iga_access_review_item` | `backend/migrations/000029_iga.up.sql:88` | NN | – | – | – | ja | ja | O | O | I(org,review_id) |
| `iga_drift_finding` | `backend/migrations/000029_iga.up.sql:102` | NN | – | – | – | ja | ja | O | O | I(org,status) |
| `search_document` | `backend/migrations/000030_search_ai.up.sql:8` | NN | – | – | – | ja | ja | O | O | U(org,entity_type,entity_id); I(org,entity_type); GIN(search_vector) |
| `ai_conversation` | `backend/migrations/000030_search_ai.up.sql:26` | NN | – | – | – | ja | ja | O | O | I(org,user_id,updated_at DESC); kein Benutzer-Scope in Policy |
| `ai_message` | `backend/migrations/000030_search_ai.up.sql:36` | NN | – | – | – | ja | ja | O | O | I(org,conversation_id,created_at) |
| `ai_chunk` | `backend/migrations/000030_search_ai.up.sql:49` | NN | – | – | – | ja | ja | O | O | U(org,entity_type,entity_id); I(org,entity_type,entity_id); embedding ist JSONB, kein Vektorindex |
| `webhook_dead_letter` | `backend/migrations/000034_webhook_dead_letter.up.sql:14` | NN | – | – | – | ja | ja seit 000056 | S | S | U(delivery_id); I(org,dead_at DESC) |
| `alert_rule` | `backend/migrations/000037_alert_rule.up.sql:8` | NN | – | – | – | ja | ja seit 000056 | S | S | I(org,name); I(org,metric_name) bei enabled |
| `privacy_retention_policy` | `backend/migrations/000038_privacy_retention.up.sql:4` | NN | – | – | – | ja | ja | O | O | U(org) |
| `collector_enrollment_code` | `backend/migrations/000042_collector_enrollment.up.sql:7` | NN | – | – | – | ja | ja | S | S | U(code_hash); I(org), I(code_hash) |
| `consumable` | `backend/migrations/000045_consumables.up.sql:5` | NN | NULL | – | – | ja | ja | O | O | U(org,sku), sku nullable; I(org) |
| `stock_movement` | `backend/migrations/000045_consumables.up.sql:22` | NN | – | – | – | ja | ja | O | O | I(consumable_id) |
| `internal_order` | `backend/migrations/000046_internal_orders.up.sql:2` | NN | NULL | – | – | ja | ja | O | O | U(org,order_number); I(org) |
| `internal_order_item` | `backend/migrations/000046_internal_orders.up.sql:20` | NN | – | – | – | ja | ja | O | O | I(order_id) |
| `maintenance_window` | `backend/migrations/000048_maintenance.up.sql:2` | NN | – | – | – | ja | ja | O | O | I(org); ends_at > starts_at |
| `maintenance_window_ci` | `backend/migrations/000048_maintenance.up.sql:17` | NN | – | – | – | ja | ja | O | O | U(maintenance_window_id,ci_id); I(maintenance_window_id) |
| `maintenance_notification` | `backend/migrations/000048_maintenance.up.sql:25` | NN | NULL | – | – | ja | ja | O | O | I(maintenance_window_id) |
| `disposal_record` | `backend/migrations/000049_disposal.up.sql:2` | NN | – | – | – | ja | ja | O | O | I(org); Append-only-Trigger |
| `key_item` | `backend/migrations/000050_keys_training_desks.up.sql:4` | NN | NULL | – | – | ja | ja | O | O | I(org) |
| `key_assignment` | `backend/migrations/000050_keys_training_desks.up.sql:18` | NN | – | – | – | ja | ja | O | O | I(key_item_id); keine Unique-Regel für offene Ausgabe |
| `training` | `backend/migrations/000050_keys_training_desks.up.sql:31` | NN | – | – | – | ja | ja | O | O | Außer PK keine weiteren expliziten Indizes/Unique |
| `training_assignment` | `backend/migrations/000050_keys_training_desks.up.sql:42` | NN | – | – | – | ja | ja | O | O | U(training_id,user_id); I(user_id) |
| `desk` | `backend/migrations/000050_keys_training_desks.up.sql:57` | NN | – | – | – | ja | ja | O | O | I(org) |
| `desk_booking` | `backend/migrations/000050_keys_training_desks.up.sql:68` | NN | – | – | – | ja | ja | O | O | I(desk_id), I(desk_id,starts_at,ends_at); ends_at > starts_at, keine Überschneidungs-Exclusion |
| `asset_location` | `backend/migrations/000051_rfid_gps.up.sql:9` | NN | – | – | – | ja | ja | O | O | I(asset_id,recorded_at DESC) |
| `endpoint_agent` | `backend/migrations/000052_agents.up.sql:2` | NN | – | – | – | ja | ja | O | O | U(org,agent_id); I(org) |
| `security_finding` | `backend/migrations/000053_security_findings.up.sql:6` | NN | – | – | – | ja | ja | O | O | I(org), I(ci_id), I(org,status) |
| `ci_instance_field_definition` | `backend/migrations/000055_cmdb_extensions.up.sql:64` | NN | – | – | – | ja | ja | O | O | U(ci_id,name); I(ci_id) |
| `relationship_type` | `backend/migrations/000055_cmdb_extensions.up.sql:95` | NULL | – | – | – | ja | ja | G | O | U(COALESCE(org,Null-UUID),key); org NULL erfordert is_system |
| `lifecycle_definition` | `backend/migrations/000055_cmdb_extensions.up.sql:165` | NULL | – | – | – | ja | ja | G | O | U(COALESCE(org,Null-UUID),key); org NULL erfordert is_system |
| `lifecycle_state` | `backend/migrations/000055_cmdb_extensions.up.sql:183` | NULL seit 000056 | – | – | – | ja | ja | G | O | U(definition_id,key) |
| `lifecycle_transition` | `backend/migrations/000055_cmdb_extensions.up.sql:196` | NULL seit 000056 | – | – | – | ja | ja | G | O | U(definition_id,from_state_id,to_state_id); from_state_id nullable |
| `location_node` | `backend/migrations/000055_cmdb_extensions.up.sql:286` | NN | NULL | NULL | – | ja | ja | O | O | I(org), I(parent_id), I(org,node_type), partiell I(client_id) |
| `asset_movement` | `backend/migrations/000055_cmdb_extensions.up.sql:329` | NN | – | – | – | ja | ja | O | O | I(org,created_at DESC), partiell I(asset_id,created_at DESC), I(org,movement_type); Append-only-Trigger |
| `quantity_item` | `backend/migrations/000055_cmdb_extensions.up.sql:376` | NN | NULL | – | – | ja | ja | O | O | U(org,sku), sku nullable |
| `reservation` | `backend/migrations/000055_cmdb_extensions.up.sql:409` | NN | – | – | – | ja | ja | O | O | U(asset_id) bei asset_id NOT NULL und state='active'; I(org,state), partiell I(asset_id), partiell I(expires_at) |
| `composition` | `backend/migrations/000055_cmdb_extensions.up.sql:453` | NN | – | – | – | ja | ja | O | O | Partielle U(parent_asset_id,child_ci_id) bzw. U(parent_asset_id,child_asset_id); I(parent_asset_id); XOR-Kind, Tenant-FKs 000056, Zyklus-Trigger 000057 |
| `ci_field_value` | `backend/migrations/000055_cmdb_extensions.up.sql:486` | NN | – | – | – | ja | ja | O | O | U(ci_id,field_name); I(ci_id) |
| `source_priority_policy` | `backend/migrations/000055_cmdb_extensions.up.sql:513` | NN | – | – | – | ja | ja | O | O | U(org,name); keine Unique-Regel für is_default |
| `entity_change` | `backend/migrations/000055_cmdb_extensions.up.sql:538` | NN | – | – | – | ja | ja | O | O | I(entity_type,entity_id,created_at DESC), I(org,created_at DESC); entity_id ohne FK |
| `saved_view` | `backend/migrations/000055_cmdb_extensions.up.sql:572` | NN | – | – | – | ja | ja | O | O | U(org,owner_id,name), owner_id nullable; owner_id/shared nicht in RLS |

`org` in der Indexspalte steht für `organization_id`; „Null-UUID“ bezeichnet `00000000-0000-0000-0000-000000000000`, **nicht** SQL-NULL.

### Policy-Entwicklung und tatsächliche Reichweite

1. `backend/migrations/000018_force_rls_triggers.up.sql:13–62` ergänzt FORCE und ersetzt zunächst sechs Policies. Erst `backend/migrations/000023_rls_variable_unification.up.sql:13–67` vereinheitlicht die dort aufgeführten älteren Tabellen auf `app.org_id`, FORCE und explizites WITH CHECK. Die früheren Variablen `app.organization_id`/`app.current_org` sind daher nicht als finaler Zustand zu übernehmen.
2. `backend/migrations/000033_client_scope_rls.up.sql:29–79` ersetzt ausschließlich die Policies von `client`, `site`, `ci`, `collector`, `subnet`, `contact`, `credential`. Trotz Kommentar „all tables“ werden insbesondere `asset`, `sla`, `form_def` und die später angelegten Client-Tabellen **nicht** clientgefiltert. Die neun Tabellen mit `client_id`, aber nur O, sind `asset`, `sla`, `form_def`, `consumable`, `internal_order`, `maintenance_notification`, `key_item`, `location_node`, `quantity_item`. Auch abhängige Tabellen wie `building`, `discovery_job`, `network_interface` oder `ci_relationship` erben keinen Client-Filter.
3. Indirekte Policies entstehen in `backend/migrations/000041_rls_gap_closure.up.sql:25–72`: `ci_type_attribute` über `ci_type`, `team_member` über `team`, `user_custom_role` über `custom_role`. Das ist **indirekte Organisationsprüfung**, kein Mitgliedschafts-/Team-Scope und keine Prüfung beider Seiten einer Zuordnung. `org_dek` erhält hier erstmals direkte RLS (`:18–23`).
4. System-Ausnahmen gelten auf fünf Tabellen: `webhook_delivery` (`backend/migrations/000024_durable_webhook_delivery.up.sql:21–30`), `webhook_dead_letter` (`backend/migrations/000034_webhook_dead_letter.up.sql:28–40`), `export_job` (`backend/migrations/000035_export_job_formats.up.sql:13–22`), `alert_rule` (`backend/migrations/000037_alert_rule.up.sql:27–37`) und `collector_enrollment_code` (`backend/migrations/000043_enrollment_system_read.up.sql:8–17`). **Beide** Ausdrücke erlauben `app.system='on'`; insbesondere Enrollment ist im SQL nicht auf SELECT beschränkt, ungeachtet des Dateinamens/Kommentars.
5. `backend/migrations/000056_rls_enforcement.up.sql:62–78` setzt FORCE dynamisch für alle bereits RLS-aktivierten gewöhnlichen `public`-Tabellen. Im hier definierten Inventar schließt das die FORCE-Lücken bei `webhook_dead_letter` und `alert_rule`. Es aktiviert **keine** neue RLS auf `permission` oder `metric_sample`.
6. Alle migrationsdefinierten Policies verwenden den Standard **PERMISSIVE, FOR ALL, TO PUBLIC**; kein `AS RESTRICTIVE`, keine befehlsspezifische Policy. Die Ersetzungen mit DROP/CREATE sind zeitlich aufeinanderfolgende Definitionen, **keine kumulativ UND-verknüpften Policies**. PERMISSIVE-Policies würden bei mehreren gleichzeitig passenden Policies mit ODER wirken; die finalen Anwendungstabellen haben hier jeweils eine Policy.
7. G-Policies sind **nicht gleichbedeutend mit „globale Zeilen nur lesbar“**: USING erlaubt globale Bestandszeilen auch für DELETE; WITH CHECK kontrolliert neue INSERT-/UPDATE-Zeilen, nicht DELETE. Ein UPDATE kann mit passendem neuen org-Wert ebenfalls die WITH-CHECK-Bedingung erfüllen. Das betrifft `ci_type` (`backend/migrations/000040_ci_type_global_read.up.sql:8–11`), `relationship_type`, `lifecycle_definition` (`backend/migrations/000055_cmdb_extensions.up.sql:119–123,209–213`), `lifecycle_state`/`lifecycle_transition` (`backend/migrations/000056_rls_enforcement.up.sql:97–105`) und entsprechend den globalen Elternfall bei `ci_type_attribute`. Eine zusätzliche DB-Regel „Systemmetadaten unveränderlich“ ist dort nicht definiert.

### Weitere kumulative Integritätsdetails

- **CI-Identität:** `backend/migrations/000023_rls_variable_unification.up.sql:96–107` definiert U(org,serial_number) nur für nichtleere, nicht-NULL Seriennummern ohne `deleted_at`; U(org,hardware_uuid) und U(org,primary_mac) nur bei nicht-NULL Wert und `deleted_at IS NULL`. Die älteren nicht eindeutigen Indizes bleiben daneben bestehen. `last_seen`/`source` werden nach Backfill entfernt und durch `last_seen_at`/`discovery_source` ersetzt (`:69–92`).
- **Scope-konsistente Referenzen:** Nur die zwei CI-Kanten-Enden und drei Composition-Referenzen erhalten hier zusammengesetzte FKs mit `organization_id`; zuvor werden unpassende Zeilen gelöscht (`backend/migrations/000056_rls_enforcement.up.sql:127–161`). Das sichert gleiche Organisation, **nicht** gleichen Client/Site/Team. Andere einfache UUID-FKs sind kein Nachweis gleicher Organisation; z. B. bleibt `asset.ci_id` trotz Unique nur ein einfacher CI-FK.
- **NULL und Unique:** Kein `NULLS NOT DISTINCT` ist definiert. NULL-haltige Schlüsselkomponenten erlauben daher mehrere ansonsten gleiche Kombinationen, z. B. bei `role_assignment`, `user_custom_role`, `sla`, `saved_view`, `lifecycle_transition`. `ci_type.key` bleibt nullable, auch nach den partiellen U-Indizes in 000056. Die zusätzlichen „global“-Attributindizes sind weiterhin nur pro `(ci_type_id,name)`, nicht organisationsweit (`backend/migrations/000055_cmdb_extensions.up.sql:56–60`).
- **Rack:** Die einzige explizite Exclusion im Inventar ignoriert `face='both'` vollständig und trennt front/rear durch Gleichheit. Keine hier definierte Regel prüft `position_u + height_u` gegen `rack.height_u` (`backend/migrations/000021_spec_alignment.up.sql:112–131`).
- **Composition:** Genau eines von child_ci_id/child_asset_id ist Pflicht, aber ein Kind kann durch die paarweisen Unique-Indizes mehrere Eltern haben (`backend/migrations/000055_cmdb_extensions.up.sql:453–475`). Der Trigger in `backend/migrations/000057_composition_acyclic.up.sql:13–55` prüft Selbstbezug und verfolgt per einzelnem `SELECT ... INTO` eine Elternkette bis maximal 100 Schritte. Das SQL definiert weder eindeutige Elternschaft noch eine rekursive Prüfung sämtlicher Verzweigungen oder explizite Parallelitäts-Sperren; daraus wird keine stärkere globale Azyklizitätsgarantie abgeleitet. `asset.parent_asset_id` erhält diesen Trigger nicht.
- **Append-only:** UPDATE/DELETE-Trigger existieren für `disposal_record` (`backend/migrations/000049_disposal.up.sql:25–35`), `asset_movement` (`backend/migrations/000055_cmdb_extensions.up.sql:360–370`) und final `audit_log` (`backend/migrations/000056_rls_enforcement.up.sql:107–125`). `ci_change`, `entity_change` und `stock_movement` haben keine entsprechende Regel. Die Audit-Hashberechnung und Neuverkettung kommen separat aus `backend/migrations/000039_audit_canonical_hash.up.sql:19–96`.

## Extensions und zusätzliche DB-Objekte

| Extension | Nachweis | Im SQL verwendeter Zweck / Grenze |
|---|---|---|
| `pgcrypto` | `backend/migrations/000001_tenant_model.up.sql:4`; erneut `backend/migrations/000039_audit_canonical_hash.up.sql:14` | UUID-Defaults und `digest(...,'sha256')` für Audit-Hashes; zwei CREATE-Anweisungen, nur eine Extension |
| `timescaledb` | `backend/migrations/000020_metrics.up.sql:4` | `CREATE EXTENSION ... CASCADE`; Hypertable, Kompression, Retention, Continuous Aggregate; keine Versionsbindung |
| `btree_gist` | `backend/migrations/000021_spec_alignment.up.sql:125` | GiST-Exclusion für Rack-Belegung |

`metric_sample_hourly` wird in `backend/migrations/000020_metrics.up.sql:41–59` mit `WITH NO DATA` angelegt und durch `backend/migrations/000031_schema_corrections.up.sql:41` nach **`metric_sample_1h`** umbenannt: Gruppierung nach Stunden-Bucket, org, ci und metric_name; avg/min/max/count. Die Migration setzt Kompression nach sieben Tagen, Retention nach 400 Tagen und Refresh stündlich für das Fenster „vor drei Stunden bis vor einer Stunde“ (`000020:27–59`). Es gibt keinen migrationsdefinierten RLS-Schutz für die Metriktabelle oder einen tenantfilternden Ausdruck in dieser View. Die Kommentare über Backend-Filter sind keine hier geprüfte Garantie.

## PostgreSQL-Rollen, GRANTs und DDL-Rechte

Einzige explizit angelegte DB-Rolle: **`reticora_app`** (`backend/migrations/000056_rls_enforcement.up.sql:23–56`).

| Aspekt | Durch SQL belegt |
|---|---|
| Neuanlage | `NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE`, nur falls die Rolle noch nicht existiert (`:25–27`) |
| Auch bei vorhandener Rolle erzwungen | Nur `NOSUPERUSER NOBYPASSRLS` durch ALTER ROLE (`:33`); andere eventuell vorhandene Attribute/Mitgliedschaften werden nicht bereinigt |
| Schema | `USAGE ON SCHEMA public` (`:35`) |
| Tabellen/Views | `SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public` (`:36`); keine Scope-Ausnahme für den globalen Permission-Katalog oder Metriken |
| Sequenzen | `USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public` (`:37`), u. a. relevant für `ticket.ticket_number SERIAL` |
| Funktionen | `EXECUTE ON ALL FUNCTIONS IN SCHEMA public` (`:38`) |
| Zukünftige Objekte | Gleiche DEFAULT PRIVILEGES für Tabellen, Sequenzen und Funktionen des ausführenden Objekt-Erzeugers in `public`, nicht automatisch aller DB-Rollen (`:41–46`) |
| Mitgliedschaft | Versuch `GRANT reticora_app TO current_user` per dynamischem SQL; alle Fehler werden abgefangen, daher Erfolg nicht garantiert (`:50–56`) |
| Owner-/DDL-Rechte | Kein `ALTER ... OWNER TO reticora_app`, kein ausdrückliches Schema-`CREATE`, kein `TRUNCATE`-Grant, keine Eigentumszuweisung an diese Rolle |
| Superuser/Migrationslogin | Kein namentlicher Login/Migrationsbenutzer wird angelegt oder zum Superuser gemacht; Identität, vorhandene Rechte und tatsächlicher Table Owner sind aus diesen Dateien nicht bestimmbar |

GRANTs ersetzen keine RLS; FORCE unterwirft auch normale Table Owner der Policy. **Superuser und BYPASSRLS bleiben davon ausgenommen.** Ob Laufzeitverbindungen tatsächlich `SET ROLE reticora_app` ausführen, ist kein durch die Migration belegter Zustand. Ebenso beweist das Fehlen eines direkten DDL-GRANTs nicht das Fehlen geerbter/PUBLIC-/bereits bestehender Rechte.

Der Kommentar in `backend/migrations/000054_default_organization.up.sql:13–16`, der allein aus Table Ownership eine RLS-Ausnahme ableitet, darf nicht als Rechte-Nachweis übernommen werden: `organization` hat bereits FORCE. Der Seed benötigt einen tatsächlich zulässigen Ausführungskontext. Keine Funktion in den Up-Dateien deklariert `SECURITY DEFINER`.

DB-Rolle `reticora_app`, Datensätze in `role` und Permission-Keys sind drei verschiedene Ebenen; die letzteren verleihen für sich keine PostgreSQL-Privilegien.

## CHECK-/Statusvokabulare

Die hier angefragten Vokabulare sind **TEXT-Spalten mit CHECK**, keine PostgreSQL-ENUM-Typen; die Migrationen enthalten kein `CREATE TYPE`.

| Feld / angefragter Begriff | Final erlaubte Werte / Einordnung | Quelle und Entwicklung |
|---|---|---|
| `ci.status` | `active`, `inactive`, `maintenance`, `decommissioned`, `unknown`; NN, Default `active` | Anlage `backend/migrations/000002_ci_type_system.up.sql:33`; `unknown` ergänzt durch `backend/migrations/000021_spec_alignment.up.sql:27–30`; bleibt trotz Lifecycle-Erweiterung bestehen |
| `ci.discovery_source` | `snmp`, `ssh`, `redfish`, `ipmi`, `wmi`, `api`, `agent`, `sweep`, `manual`; nullable, kein Default | `backend/migrations/000021_spec_alignment.up.sql:20–21`; Backfill/Entfernung von `ci.source` in `backend/migrations/000023_rls_variable_unification.up.sql:79–90`; **kein `import`** in diesem CHECK |
| Review-Typ `review_item.kind` | `ambiguous_identity`, `conflicting_values`, `unclassified_device` | `backend/migrations/000021_spec_alignment.up.sql:192` |
| `review_item.status` | `open`, `resolved`, `dismissed`; Default `open` | `backend/migrations/000021_spec_alignment.up.sql:193` |
| Weitere Review-Felder | `iga_access_review.status`: `draft`, `active`, `completed`, `cancelled`; Default `active`; `iga_access_review_item.decision`: nullable `approve`/`revoke` | `backend/migrations/000029_iga.up.sql:82,95` |
| **`change_kind`** | Kein solcher Spalten-/Typname in den Migrationen. Entsprechendes Feld heißt **`ci_change.change_type`**: `create`, `update`, `delete`, `status_change`, `relationship_change`, `attribute_change`, `type_change`, `lifecycle_transition`, `location_change`, `movement`, `override`, `reconciliation_decision`, `parent_child_change`, `assignment` | Ursprünglich sechs Werte in `backend/migrations/000022_ci_change.up.sql:15`; erweitert auf 14 in `backend/migrations/000055_cmdb_extensions.up.sql:561–568` |
| `entity_change.change_type` | NN, aber **kein CHECK** | `backend/migrations/000055_cmdb_extensions.up.sql:538–550` |
| `discovery_job.status` | `pending`, `running`, `completed`, `failed`; Default `pending` | `backend/migrations/000008_discovery.up.sql:21` |
| `discovery_job.job_type` | `sweep`, `poll`, `full` | `backend/migrations/000008_discovery.up.sql:20` |
| `discovery_result.reconciliation_status` | `pending`, `matched`, `created`, `conflict`, `ignored`; Default `pending` | `backend/migrations/000008_discovery.up.sql:36–37` |
| `export_job.status` | `pending`, `running`, `completed`, `failed`, `expired`; Default `pending` | `backend/migrations/000017_export_jobs.up.sql:9,21`; Format dagegen final `csv`, `json`, `datev` durch `backend/migrations/000035_export_job_formats.up.sql:6–8` |
| Weitere asynchrone Zustände | `workflow_run.status` und `workflow_step.status`: `pending`, `running`, `waiting_approval`, `succeeded`, `failed`, `cancelled`; `iga_provisioning_task.status`: `pending`, `running`, `succeeded`, `failed`, `cancelled`; jeweils Default `pending` | `backend/migrations/000027_workflows_forms.up.sql:63,70,81,88`; `backend/migrations/000029_iga.up.sql:34` |
| `collector.status` | `online`, `offline`, `degraded`; Default `online` | `backend/migrations/000008_discovery.up.sql:9` |
| `endpoint_agent.status` | Separat: `online`, `offline`, `disabled`; Default `online`, kein Collector-Status | `backend/migrations/000052_agents.up.sql:11` |
| `webhook_delivery.status` | `pending`, `success`, `failed`, `retrying`, `dead`; Default `pending` | Ursprünglich ohne `dead`: `backend/migrations/000007_webhooks.up.sql:28`; ersetzt durch `backend/migrations/000034_webhook_dead_letter.up.sql:9–12` |

Nicht mit statischen Vokabularen verwechseln: 000055 entfernt die CHECKs für `ci_type_attribute.data_type`, `ci_relationship.rel_type` und `asset.status` (`backend/migrations/000055_cmdb_extensions.up.sql:19,155,274`). `relationship_type` ist ein Katalog, aber `ci_relationship.rel_type` hat **keinen FK auf diesen Katalog**. `ci.lifecycle_state`/`asset.lifecycle_state` sind ebenfalls freie TEXT-Spalten, nicht automatisch FK-gesicherte `lifecycle_state`-Keys (`:269–274`).

## Seeds

### CI-Typen, Beziehungen und Lifecycle

**17 globale CI-Typ-Keys**, jeweils `organization_id=NULL`, `is_system=true`, `is_builtin=true`:

`switch`, `router`, `firewall`, `access_point`, `server`, `hypervisor`, `vm`, `client`, `pdu`, `ups`, `nas`, `storage_array`, `printer`, `ip_phone`, `camera`, `generic_device`, `patch_panel`.

Quelle: `backend/migrations/000021_spec_alignment.up.sql:89–109`. Keine weiteren CI-Typ-INSERTs in den Up-Migrationen. Die spätere Deduplizierung von Keys und die getrennten globalen/tenantbezogenen Unique-Indizes stehen in `backend/migrations/000056_rls_enforcement.up.sql:163–180`.

**24 globale Relationship-Typ-Keys** werden in `backend/migrations/000055_cmdb_extensions.up.sql:125–151` angelegt. **`impact_direction` existiert weder als Spalte noch als Seed-Wert**; eine Impact-Richtung darf nicht aus Key, forward/reverse_label oder Kommentar erfunden werden. Die tatsächlich vorhandenen Felder sind `direction` (CHECK `directed`/`undirected`, Default `directed`) und `impact_participation` (BOOLEAN, Default `true`), im INSERT beide ausgelassen (`:95–114`).

| Relationship-Seed-Keys | `impact_direction` | Tatsächliches `direction` / `impact_participation` |
|---|---|---|
| `connected_to`, `hosted_on`, `runs_on`, `depends_on`, `member_of`, `member_of_cluster` | nicht definiert | jeweils `directed` / `true` |
| `powers`, `powered_by`, `mounted_in`, `uplink_to`, `stores`, `monitors` | nicht definiert | jeweils `directed` / `true` |
| `backs_up`, `backed_up_by`, `managed_by`, `manages`, `assigned_to`, `contains` | nicht definiert | jeweils `directed` / `true` |
| `contained_by`, `parent_of`, `child_of`, `located_in`, `uses`, `used_by` | nicht definiert | jeweils `directed` / `true` |

Insbesondere wird auch `connected_to` hier nicht explizit als `undirected` gesät. Der frühere rel_type-CHECK wächst in 000006 → 000025 → 000031 von 8 → 9 → 13 Werten und wird durch 000055 ganz entfernt; das ist vom neu angelegten 24er-Katalog zu trennen.

Zusätzlich: eine globale Lifecycle-Definition `physical_asset` (ID `…0101`), neun States `ordered`, `received`, `in_stock`, `reserved`, `preparing`, `deployed`, `repair`, `retired`, `disposed` und 15 Transition-Paare (`backend/migrations/000055_cmdb_extensions.up.sql:227–267`). States/Transitions werden zunächst der Demo-Organisation zugeordnet, in 000056 auf `organization_id=NULL` umgestellt (`backend/migrations/000056_rls_enforcement.up.sql:86–105`). Default-Organisation: ID `00000000-0000-0000-0000-000000000001`, Name `Reticora Demo`, Slug `reticora-demo`; `ON CONFLICT (id) DO NOTHING`, **kein** Konflikthandler für eine bereits anderweitig belegte Slug (`backend/migrations/000054_default_organization.up.sql:21–23`).

### Vollständiger Permission-Katalog aus Seeds

Schreibweise `resource:{read,write}` expandiert exakt zu `resource:read` und `resource:write`; sie steht nicht für Wildcards. Insgesamt **95 unterschiedliche Keys**, keine späteren Up-Löschungen.

| Migration / Quelle | Hinzugefügte Keys | Anzahl |
|---|---|---:|
| `backend/migrations/000026_permissions_sla.up.sql:13–48` | `ci:{read,write,delete}`; `topology:read`; `discovery:{read,write}`; `asset:{read,write}`; `assignment:{read,write}`; `document:{read,write}`; `stocktake:{read,write}`; `ticket:{read,write}`; `user:{read,write}`; `role:{read,write}`; `permission:{read,write}`; `sla:{read,write}`; `webhook:{read,write}`; `credential:{read,write}`; `audit:read`; `ipam:{read,write}`; `rack:{read,write}`; `contact:{read,write}` | 35 |
| `backend/migrations/000027_workflows_forms.up.sql:3–8` | `form:{read,write}`, `workflow:{read,write}` | 4 |
| `backend/migrations/000028_compliance.up.sql:3–6` | `compliance:{read,write}` | 2 |
| `backend/migrations/000029_iga.up.sql:3–6` | `iga:{read,write}` | 2 |
| `backend/migrations/000030_search_ai.up.sql:3–6` | `search:write`, `ai:read` | 2 |
| `backend/migrations/000032_standard_role_seeds.up.sql:20–39` | `user:manage`, `role:manage`, `webhook:manage`, `credential:manage`, `apikey:manage`, `citype:manage`, `search:read`, `relationship:{read,write}`, `site:{read,write}`, `entitlement:{read,manage}`, `monitoring:{read,write}`, `export:run`, `discovery:ingest` | 17 |
| `backend/migrations/000045_consumables.up.sql:53–56` | `consumable:{read,write}` | 2 |
| `backend/migrations/000046_internal_orders.up.sql:49–53` | `order:{read,write,approve}` | 3 |
| `backend/migrations/000048_maintenance.up.sql:61–64` | `maintenance:{read,write}` | 2 |
| `backend/migrations/000049_disposal.up.sql:37–40` | `disposal:{read,write}` | 2 |
| `backend/migrations/000050_keys_training_desks.up.sql:100–107` | `key:{read,write}`, `training:{read,write}`, `desk:{read,write}` | 6 |
| `backend/migrations/000052_agents.up.sql:28–32` | `agent:{read,manage,ingest}` | 3 |
| `backend/migrations/000053_security_findings.up.sql:36–39` | `security:{read,write}` | 2 |
| `backend/migrations/000055_cmdb_extensions.up.sql:616–634` | `ci_type:manage`, `ci_attribute:manage`, `ci_instance_attribute:manage`, `relationship_type:manage`, `asset:{assign,move,reserve}`, `inventory:manage`, `lifecycle:manage`, `reconciliation:resolve`, `override:write`, `saved_view:{read,write}` | 13 |

`citype:manage` und `ci_type:manage` sind zwei verschiedene gesäte Keys. Ebenso sind `:write` und `:manage` nicht automatisch dasselbe DB-Recht. Der Katalog hat keine Scope-Spalten/RLS und `reticora_app` erhält darauf dieselben Tabellen-GRANTs wie auf andere public-Tabellen.

### Anwendungsrollen und Grants

`backend/migrations/000032_standard_role_seeds.up.sql:48–180` definiert `seed_standard_roles(UUID)`, ruft sie für bestehende Organisationen auf und installiert einen AFTER-INSERT-Trigger auf `organization`. Die vier Vorlagen werden in `role` (`is_builtin=true`) und `role_permission` geschrieben; `role.permissions` erhält zugleich JSONB.

| Rolle | Scope | Exakte anfängliche Grant-Menge aus 000032 |
|---|---|---|
| `org_admin` | `org` | Alle **zum Aufrufzeitpunkt** vorhandenen `permission.key` per SELECT; bei 000032 sind es 62 |
| `engineer` | `org` | `ci:{read,write,delete}`, `topology:read`, `discovery:{read,write,ingest}`, `asset:{read,write}`, `assignment:{read,write}`, `document:{read,write}`, `stocktake:{read,write}`, `ticket:{read,write}`, `user:read`, `role:read`, `permission:read`, `sla:read`, `webhook:read`, `credential:{read,write}`, `audit:read`, `ipam:{read,write}`, `rack:{read,write}`, `contact:{read,write}`, `search:{read,write}`, `ai:read`, `relationship:{read,write}`, `site:{read,write}`, `entitlement:read`, `form:{read,write}`, `workflow:{read,write}`, `compliance:{read,write}`, `monitoring:{read,write}`, `iga:read`, `export:run` |
| `viewer` | `org` | Jeweils `:read` für `ci`, `topology`, `discovery`, `asset`, `assignment`, `document`, `stocktake`, `ticket`, `user`, `role`, `permission`, `sla`, `webhook`, `credential`, `audit`, `ipam`, `rack`, `contact`, `search`, `relationship`, `site`, `entitlement`, `form`, `workflow`, `compliance`, `monitoring`, `iga` |
| `client_technician` | `client` | Jeweils `:read` für `ci`, `topology`, `asset`, `assignment`, `document`, `stocktake`, `ticket`, `ipam`, `rack`, `contact`, `search`, `relationship`, `site`, `monitoring`; zusätzlich `stocktake:write`, `ticket:write` |

Präzise Quellbereiche: org_admin `:56–57`, engineer `:58–89`, viewer `:90–119`, client_technician `:120–136`; Upsert/Grants `:139–150`. Keine gesäten Benutzer-Role-Assignments oder `custom_role`-Datensätze.

Spätere Migrationen ergänzen **bereits vorhandene** Rollen nur über `role_permission`:

| Migration / Grant-Quelle | org_admin | engineer | viewer | client_technician |
|---|---|---|---|---|
| `backend/migrations/000045_consumables.up.sql:59–70` | consumable read/write | gleich | read | read |
| `backend/migrations/000046_internal_orders.up.sql:55–72` | order read/write/approve | read/write | read | read |
| `backend/migrations/000048_maintenance.up.sql:66–77` | maintenance read/write | gleich | read | read |
| `backend/migrations/000049_disposal.up.sql:42–53` | disposal read/write | gleich | read | – |
| `backend/migrations/000050_keys_training_desks.up.sql:109–121` | key/training/desk jeweils read/write | gleich | jeweils read | jeweils read |
| `backend/migrations/000052_agents.up.sql:34–45` | agent read/manage | gleich | read | – |
| `backend/migrations/000053_security_findings.up.sql:41–52` | security read/write | gleich | read | – |
| `backend/migrations/000055_cmdb_extensions.up.sql:639–667` | alle 13 neuen Keys | asset assign/move/reserve; reconciliation resolve; override write; saved_view read/write | saved_view read | saved_view read |

**Zeitabhängigkeit nicht übersehen:** `seed_standard_roles` wird danach nicht ersetzt. Neue Organisationen nach 000055 erhalten beim org_admin die dann 95 Keys, bei den drei anderen Rollen jedoch weiterhin nur die statischen Arrays aus 000032. Spätere einmalige Grant-INSERTs werden nicht nachträglich pro neuer Organisation wiederholt. Auch `role.permissions` bestehender Rollen wird durch diese ergänzenden INSERTs nicht mitgeführt. Bei schon vor 000052 vorhandenen org_admin-Rollen fehlt durch deren Grant-INSERT insbesondere `agent:ingest`; ein erneuter Funktionsaufruf oder spätere Neuanlage kann die Menge ändern. Die Demo-Organisation entsteht erst in 000054, nach den Modulerweiterungen bis 000053, und erhält daher andere Ausgangs-Grants als eine bereits in 000032 bestehende Organisation. Eine pauschale Aussage „alle vier Rollen haben überall denselben finalen Grant-Satz“ wäre falsch.

## Down-Migrationen und Nummerierung

- **Fehlende Down-Dateien: 0.** Jede der 57 Versionen hat eine gleichnamige `.down.sql`; keine leeren Down-Dateien.
- **Nummerierungsanomalien der Dateien: keine.** Jede sechsstellige Nummer von 000001 bis 000057 ist genau einmal pro Richtung vorhanden. Kommentare wie „spec Migration 0004“ in `backend/migrations/000015_network_ipam.up.sql:2` sind Referenzen auf eine Spezifikation, keine fehlenden oder doppelten Datei-Versionen.
- **000055 gesondert geprüft:** ausschließlich `backend/migrations/000055_cmdb_extensions.up.sql` und `backend/migrations/000055_cmdb_extensions.down.sql`; kein zweites Migrationspaar mit derselben Sequenznummer.
- „Down vorhanden“ bedeutet ausdrücklich **nicht** vollständige, verlustfreie oder für beliebige Daten ausführbare Umkehrung. Die Dateien wurden statisch gelesen, nicht gegen eine DB ausgeführt.

Konkrete Asymmetrien, die für das Ist-Inventar relevant sind:

| Stelle | Beobachtung |
|---|---|
| `backend/migrations/000020_metrics.down.sql:1–3` | Entfernt View/Tabelle, lässt TimescaleDB ausdrücklich bestehen. `btree_gist` wird durch `backend/migrations/000021_spec_alignment.down.sql` ebenfalls nicht entfernt. |
| `backend/migrations/000021_spec_alignment.down.sql:9–24` | Setzt `ci_type.organization_id` nicht zurück auf NOT NULL, löscht globale CI-Typ-Seeds nicht und entfernt `site_org_name_unique` nicht. |
| `backend/migrations/000023_rls_variable_unification.down.sql:29–72` | Stellt alte Policy-Variablen wieder her, nimmt die von Up ergänzten ENABLE/FORCE-Zustände nicht zurück. |
| `backend/migrations/000025_reconciliation_topology.down.sql:5`; `backend/migrations/000031_schema_corrections.down.sql:8–9` | Löschen bestimmte Beziehungstyp-Zeilen vor Einschränkung des Vokabulars; keine verlustfreie Rückkehr. |
| `backend/migrations/000031_schema_corrections.down.sql:18–23`; `backend/migrations/000035_export_job_formats.down.sql:8–10` | Engere Constraints werden ohne allgemeine Datenbereinigung wiederhergestellt; nach Up zulässige Daten können das Down scheitern lassen. |
| `backend/migrations/000039_audit_canonical_hash.down.sql:1–3` | Entfernt Funktionen/Trigger, macht die Neuverkettung vorhandener Audit-Daten nicht rückgängig. |
| `backend/migrations/000045_consumables.down.sql:1–2` | Entfernt Tabellen, nicht die consumable-Permission-Seeds/Grants. |
| `backend/migrations/000054_default_organization.down.sql:8–11` | „Leer“ wird nur anhand von app_user/client geprüft, nicht anhand aller abhängigen Tabellen. |
| `backend/migrations/000055_cmdb_extensions.down.sql:45–49` | Stellt nur das 9er-rel_type-Vokabular aus 000025 wieder her, nicht das vorherige 13er-Vokabular aus 000031; keine vorherige Bereinigung der übrigen Werte. |
| `backend/migrations/000055_cmdb_extensions.down.sql:79–89` | Entfernt den in Up angelegten `idx_asset_ci_unique` nicht; eingeschränkte asset.status-/ci_change-/Attribut-Typ-CHECKs können durch inzwischen zulässige Daten verletzt werden (`:62–63,82–89`). |
| `backend/migrations/000056_rls_enforcement.down.sql:42–58` | FORCE wird absichtlich nicht zurückgenommen; Grants/Default-Grants werden entzogen und Rolle entfernt. Gelöschte inkonsistente Referenzen und umbenannte doppelte CI-Typ-Keys aus Up werden nicht rekonstruiert. |

## Abgrenzung

Dies ist eine vollständige **statische Bestandsaufnahme der migrationsdefinierten Tabellen, Scope-Policies und angefragten Seeds/Vokabulare**, kein Nachweis des tatsächlich erreichten DB-Zustands, erfolgreicher Migrationen/Rollbacks, wirksamer Connection-Pool-Rollen oder ergänzender Anwendungsautorisierung. Insbesondere wurden keine fehlenden Artefakte (`owner_team_id`, `impact_direction`, `change_kind`) aus einer vermuteten Spezifikation ergänzt.
