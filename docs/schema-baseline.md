# Schema-Baseline

Diese Datei beschreibt den Datenbankschema-Stand nach allen Migrationen in
`backend/migrations/` (Anforderung DB-02). Sie ist die Referenz für Reviews von
Migrationen: Jede Änderung an Tabellen, Row Level Security (RLS) oder Policies
zeigt sich hier als Diff.

Stand: Migration 000066_document_storage_key_check

## Prüfung

`backend/internal/database/migrations_roundtrip_integration_test.go` enthält
zwei Integrationstests, die nur mit gesetztem `TEST_DATABASE_URL` laufen
(`make test-db`, CI-Job `migrations`):

- `TestMigrationsRoundtrip`: `up` bis zur neuesten Migration → Schema-Dump A →
  `down` bis Version 0 → für jede Migration einzeln `up`/`down`/`up` mit
  Schemavergleich → Schema-Dump B; verlangt A = B. Jede Down-Migration muss den
  Schemazustand vor ihrem `up` exakt wiederherstellen, jedes erneute `up` muss
  denselben Zustand wie das erste ergeben. Der letzte Schritt ist damit
  `down 1`/`up 1` der neuesten Migration. Der Dump umfasst Erweiterungen,
  Relationen mit RLS-/FORCE-Status, Spalten, Constraints, Indizes, Policies,
  Trigger, Funktionen, Views, Typen und die Rechte von `reticora_app`.
- `TestSchemaBaselineMatchesMigratedSchema`: vergleicht die Tabelle unten und
  die Zeile „Stand“ mit dem migrierten Schema.

Nach einer Schemaänderung wird die Tabelle gegen eine migrierbare Datenbank
(`TEST_DATABASE_URL`, Standard `DATABASE_URL`) neu erzeugt:

```bash
make schema-baseline     # setzt RETICORA_UPDATE_SCHEMA_BASELINE=1
make migrate-roundtrip   # golang-migrate up/down/up plus beide Tests
```

Bekannte, bewusst tolerierte Abweichungen einzelner Down-Migrationen stehen mit
Begründung in `knownDownDeviations` im Roundtrip-Test. Der Test schlägt fehl,
sobald ein Eintrag nicht mehr zutrifft, die Liste kann also nur schrumpfen:

| Migration | Abweichung | Status |
|---|---|---|
| 000056 | Down belässt `FORCE ROW LEVEL SECURITY` auf `alert_rule` und `webhook_dead_letter`. | bewusst, siehe Kommentar in der Migration |

## Konventionen für neue Migrationen

- Dateiname: nächste freie Nummer zum Umsetzungszeitpunkt,
  `nnnnnn_beschreibung.up.sql` und `nnnnnn_beschreibung.down.sql` – immer als
  Paar.
- Die Down-Migration stellt den vorherigen Schemazustand vollständig wieder her;
  der Roundtrip-Test deckt jede neue Migration automatisch ab.
- Bestehende Migrationen werden nicht nachträglich geändert; Ausnahmen sind
  unvollständige Down-Migrationen, die ein WP ausdrücklich nennt.
- Nach der Änderung wird diese Datei mit dem obigen Befehl aktualisiert und mit
  committet.

## RLS-Katalog und bekannte Lücken

`backend/internal/tenant/rls/catalog_integration_test.go` (`TestRLSCatalog`)
prüft jede Tabelle in `public` mit `organization_id` (plus `organization`)
gegen die Regeln aus `backend/internal/tenant/rls/known_gaps.go`. Es zählen
Policies für `PUBLIC` oder `reticora_app`:

| Regel | Anforderung |
|---|---|
| `rls-enabled` | `ENABLE ROW LEVEL SECURITY` |
| `rls-forced` | `FORCE ROW LEVEL SECURITY` |
| `policy-commands` | SELECT, INSERT, UPDATE und DELETE sind je durch eine permissive Policy abgedeckt |
| `using` | jede Policy für SELECT/UPDATE/DELETE/ALL hat `USING` |
| `with-check` | jede Policy für INSERT/UPDATE/ALL hat ein explizites `WITH CHECK` |
| `org-predicate` | `USING` und `WITH CHECK` jedes Kommandos verwenden `app.org_id` |
| `system-write` | `app.system` erweitert höchstens SELECT, nie INSERT/UPDATE/DELETE |
| `global-rows` | INSERT/UPDATE/DELETE erreichen keine globalen Zeilen (`organization_id IS NULL`) |
| `client-scope` | Tabellen mit `client_id` verwenden `app.client_scope` in `USING` und `WITH CHECK` |
| `site-scope` | Tabellen mit `site_id` verwenden `app.site_scope` in `USING` und `WITH CHECK` (CH25) |
| `team-scope` | Tabellen mit `team_id` verwenden `app.team_scope` in `USING` und `WITH CHECK` (CH25) |
| `org-column` | jede Tabelle außer `organization`, `schema_migrations` und den globalen Katalogen hat `organization_id` |
| `read-only-catalog` | globale Kataloge ohne `organization_id` (`rls.GlobalCatalogTables`) gewähren `reticora_app` kein INSERT/UPDATE/DELETE |

Globale Kataloge ohne Org-Spalte sind nach E-10 eine dokumentierte Ausnahme,
keine Lücke: Sie werden nur durch Migrationen gepflegt und sind für die
App-Rolle schreibgeschützt. Heute ist das nur `permission` (WP-024). Globale
Katalogzeilen in Tabellen mit Org-Spalte (`organization_id IS NULL`, z. B.
System-CI-Typen) sind über getrennte Policies je Kommando nur lesbar.

Ein Prädikat gilt als erzwungen, wenn alle permissiven Policies des Kommandos
oder eine restriktive Policy das GUC verwenden. Die folgende Liste der heute
bekannten Lücken muss exakt stimmen: Neue Verstöße und bereits geschlossene
Lücken lassen den Test fehlschlagen. Neue Tabellen dürfen nicht aufgenommen
werden (`TestKnownGapsList` verlangt, dass jede Tabelle bis Migration 000057
existierte). Das schließende WP entfernt seinen Eintrag in `known_gaps.go` und
erzeugt die Tabelle mit `RETICORA_UPDATE_SCHEMA_BASELINE=1 go test -run
TestKnownGapsDocumented ./internal/tenant/rls/` neu.

<!-- rls-known-gaps:begin -->

| Tabelle | Regel | Zuständiges WP |
|---|---|---|
| `metric_sample` | `rls-enabled` | WP-040 |
| `metric_sample` | `rls-forced` | WP-040 |
| `metric_sample` | `policy-commands` | WP-040 |

<!-- rls-known-gaps:end -->

## Tabellen, RLS und Policies

Spalten: Tabellenname, RLS aktiviert, RLS erzwungen (`FORCE ROW LEVEL
SECURITY`), Policies mit ihrem Kommando.

<!-- schema-baseline:tables:begin -->

| Tabelle | RLS | FORCE RLS | Policies (Kommando) |
|---|---|---|---|
| `ai_chunk` | ja | ja | ai_chunk_tenant_isolation (ALL) |
| `ai_conversation` | ja | ja | ai_conversation_tenant_isolation (ALL) |
| `ai_message` | ja | ja | ai_message_tenant_isolation (ALL) |
| `alert_rule` | ja | ja | alert_rule_isolation (ALL), alert_rule_system_select (SELECT) |
| `api_key` | ja | ja | org_isolation (ALL) |
| `app_user` | ja | ja | user_isolation (ALL) |
| `asset` | ja | ja | asset_tenant_isolation (ALL) |
| `asset_location` | ja | ja | asset_location_isolation (ALL) |
| `asset_movement` | ja | ja | asset_movement_isolation (ALL) |
| `assignment` | ja | ja | assignment_tenant_isolation (ALL) |
| `audit_log` | ja | ja | audit_isolation (ALL) |
| `building` | ja | ja | building_isolation (ALL) |
| `cable` | ja | ja | org_isolation (ALL) |
| `ci` | ja | ja | ci_isolation (ALL) |
| `ci_change` | ja | ja | org_isolation (ALL) |
| `ci_contact` | ja | ja | org_isolation (ALL) |
| `ci_field_value` | ja | ja | ci_field_value_isolation (ALL) |
| `ci_instance_field_definition` | ja | ja | ci_instance_field_definition_isolation (ALL) |
| `ci_relationship` | ja | ja | ci_relationship_isolation (ALL) |
| `ci_type` | ja | ja | ci_type_isolation_delete (DELETE), ci_type_isolation_insert (INSERT), ci_type_isolation_select (SELECT), ci_type_isolation_update (UPDATE) |
| `ci_type_attribute` | ja | ja | ci_type_attribute_isolation_delete (DELETE), ci_type_attribute_isolation_insert (INSERT), ci_type_attribute_isolation_select (SELECT), ci_type_attribute_isolation_update (UPDATE) |
| `client` | ja | ja | client_isolation (ALL) |
| `collector` | ja | ja | collector_isolation (ALL) |
| `collector_enrollment_code` | ja | ja | collector_enrollment_code_isolation (ALL), collector_enrollment_code_system_select (SELECT) |
| `compliance_result` | ja | ja | compliance_result_tenant_isolation (ALL) |
| `compliance_rule` | ja | ja | compliance_rule_tenant_isolation (ALL) |
| `composition` | ja | ja | composition_isolation (ALL) |
| `consumable` | ja | ja | consumable_isolation (ALL) |
| `contact` | ja | ja | org_isolation (ALL) |
| `credential` | ja | ja | credential_isolation (ALL) |
| `custom_role` | ja | ja | custom_role_tenant_isolation (ALL) |
| `desk` | ja | ja | desk_isolation (ALL) |
| `desk_booking` | ja | ja | desk_booking_isolation (ALL) |
| `discovery_job` | ja | ja | discovery_job_isolation (ALL) |
| `discovery_result` | ja | ja | discovery_result_isolation (ALL) |
| `disposal_record` | ja | ja | disposal_record_isolation (ALL) |
| `document` | ja | ja | document_tenant_isolation (ALL) |
| `document_link` | ja | ja | document_link_tenant_isolation (ALL) |
| `endpoint_agent` | ja | ja | endpoint_agent_isolation (ALL) |
| `entitlement` | ja | ja | entitlement_isolation (ALL) |
| `entity_change` | ja | ja | entity_change_isolation (ALL) |
| `export_job` | ja | ja | export_job_isolation (ALL), export_job_system_select (SELECT) |
| `form_def` | ja | ja | form_def_tenant_isolation (ALL) |
| `form_submission` | ja | ja | form_submission_tenant_isolation (ALL) |
| `iga_access_request` | ja | ja | iga_access_request_tenant_isolation (ALL) |
| `iga_access_review` | ja | ja | iga_review_tenant_isolation (ALL) |
| `iga_access_review_item` | ja | ja | iga_review_item_tenant_isolation (ALL) |
| `iga_connector` | ja | ja | iga_connector_tenant_isolation (ALL) |
| `iga_drift_finding` | ja | ja | iga_drift_tenant_isolation (ALL) |
| `iga_lifecycle_policy` | ja | ja | iga_lifecycle_tenant_isolation (ALL) |
| `iga_provisioning_task` | ja | ja | iga_task_tenant_isolation (ALL) |
| `internal_order` | ja | ja | internal_order_isolation (ALL) |
| `internal_order_item` | ja | ja | internal_order_item_isolation (ALL) |
| `ip_address` | ja | ja | org_isolation (ALL) |
| `key_assignment` | ja | ja | key_assignment_isolation (ALL) |
| `key_item` | ja | ja | key_item_isolation (ALL) |
| `lifecycle_definition` | ja | ja | lifecycle_definition_isolation_delete (DELETE), lifecycle_definition_isolation_insert (INSERT), lifecycle_definition_isolation_select (SELECT), lifecycle_definition_isolation_update (UPDATE) |
| `lifecycle_state` | ja | ja | lifecycle_state_isolation_delete (DELETE), lifecycle_state_isolation_insert (INSERT), lifecycle_state_isolation_select (SELECT), lifecycle_state_isolation_update (UPDATE) |
| `lifecycle_transition` | ja | ja | lifecycle_transition_isolation_delete (DELETE), lifecycle_transition_isolation_insert (INSERT), lifecycle_transition_isolation_select (SELECT), lifecycle_transition_isolation_update (UPDATE) |
| `location` | ja | ja | location_isolation (ALL) |
| `location_node` | ja | ja | location_node_isolation (ALL) |
| `maintenance_notification` | ja | ja | maintenance_notification_isolation (ALL) |
| `maintenance_window` | ja | ja | maintenance_window_isolation (ALL) |
| `maintenance_window_ci` | ja | ja | maintenance_window_ci_isolation (ALL) |
| `metric_sample` | nein | nein | – |
| `migration_quarantine` | ja | ja | migration_quarantine_isolation (ALL) |
| `network_interface` | ja | ja | org_isolation (ALL) |
| `org_dek` | ja | ja | org_dek_isolation (ALL) |
| `organization` | ja | ja | org_isolation (ALL), organization_system_select (SELECT) |
| `permission` | nein | nein | – |
| `privacy_retention_policy` | ja | ja | privacy_retention_isolation (ALL) |
| `quantity_item` | ja | ja | quantity_item_isolation (ALL) |
| `rack` | ja | ja | rack_isolation (ALL) |
| `rack_mount` | ja | ja | rack_mount_isolation (ALL) |
| `relationship_suppression` | ja | ja | suppression_isolation (ALL) |
| `relationship_type` | ja | ja | relationship_type_isolation_delete (DELETE), relationship_type_isolation_insert (INSERT), relationship_type_isolation_select (SELECT), relationship_type_isolation_update (UPDATE) |
| `reservation` | ja | ja | reservation_isolation (ALL) |
| `review_item` | ja | ja | review_item_isolation (ALL) |
| `role` | ja | ja | role_isolation (ALL) |
| `role_assignment` | ja | ja | role_assignment_isolation (ALL) |
| `role_permission` | ja | ja | role_permission_tenant_isolation (ALL) |
| `room` | ja | ja | room_isolation (ALL) |
| `saved_view` | ja | ja | saved_view_isolation (ALL) |
| `search_document` | ja | ja | search_document_tenant_isolation (ALL) |
| `security_finding` | ja | ja | security_finding_isolation (ALL) |
| `site` | ja | ja | site_isolation (ALL) |
| `sla` | ja | ja | sla_tenant_isolation (ALL) |
| `source_priority_policy` | ja | ja | source_priority_policy_isolation (ALL) |
| `stock_movement` | ja | ja | stock_movement_isolation (ALL) |
| `stock_scan` | ja | ja | stock_scan_tenant_isolation (ALL) |
| `stocktake` | ja | ja | stocktake_tenant_isolation (ALL) |
| `subnet` | ja | ja | org_isolation (ALL) |
| `team` | ja | ja | team_tenant_isolation (ALL) |
| `team_member` | ja | ja | team_member_isolation (ALL) |
| `ticket` | ja | ja | ticket_tenant_isolation (ALL) |
| `ticket_comment` | ja | ja | ticket_comment_tenant_isolation (ALL) |
| `ticket_sla` | ja | ja | ticket_sla_tenant_isolation (ALL) |
| `training` | ja | ja | training_isolation (ALL) |
| `training_assignment` | ja | ja | training_assignment_isolation (ALL) |
| `user_custom_role` | ja | ja | user_custom_role_isolation (ALL) |
| `user_invitation` | ja | ja | org_isolation (ALL) |
| `webhook_dead_letter` | ja | ja | webhook_dead_letter_isolation (ALL), webhook_dead_letter_system_select (SELECT) |
| `webhook_delivery` | ja | ja | webhook_delivery_isolation (ALL), webhook_delivery_system_select (SELECT) |
| `webhook_subscription` | ja | ja | webhook_sub_isolation (ALL) |
| `workflow_def` | ja | ja | workflow_def_tenant_isolation (ALL) |
| `workflow_run` | ja | ja | workflow_run_tenant_isolation (ALL) |
| `workflow_step` | ja | ja | workflow_step_tenant_isolation (ALL) |

<!-- schema-baseline:tables:end -->
