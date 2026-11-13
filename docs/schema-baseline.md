# Schema-Baseline

Diese Datei beschreibt den Datenbankschema-Stand nach allen Migrationen in
`backend/migrations/` (Anforderung DB-02). Sie ist die Referenz für Reviews von
Migrationen: Jede Änderung an Tabellen, Row Level Security (RLS) oder Policies
zeigt sich hier als Diff.

Stand: Migration 000346_probe_credential_version

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

## Indizes ohne lange Sperren

golang-migrate (und der psql-Fallback in `install-cloud.sh`) führt jede
Migrationsdatei in einer Transaktion aus; `CREATE INDEX CONCURRENTLY` ist dort
nicht möglich, und ein normales `CREATE INDEX` sperrt Schreibzugriffe auf die
Tabelle, solange der Index gebaut wird. Migrationen, die Indizes auf großen
Tabellen anlegen, verwenden deshalb `CREATE INDEX IF NOT EXISTS` mit festem
Namen. Bei großen Datenbeständen baut der Betrieb die Indizes vor dem Update
ohne lange Sperren vor und lässt die Migration sie dann nur noch erkennen.
Beispiel Migration 000109 (CI-02):

```sql
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_ci_org_location ON ci (organization_id, location_id) WHERE location_id IS NOT NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_ci_org_asset ON ci (organization_id, asset_id) WHERE asset_id IS NOT NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_ci_org_parent ON ci (organization_id, parent_ci_id) WHERE parent_ci_id IS NOT NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_ci_attributes_path ON ci USING gin (attributes jsonb_path_ops);
```

Ebenso der Index der Standardsortierung der CI-Liste (WP-605, NFR-02; seit
Migration 000276 mit `owner_team_id`, das `ci_isolation` seit 000237 liest,
damit die Zählung ein Index-only-Scan bleibt). Migration 000276 ersetzt den
Index unter demselben Namen; bei großen Datenbeständen baut der Betrieb ihn
vorher unter anderem Namen und tauscht die Namen:

```sql
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_ci_list_sort_next ON ci (organization_id, created_at DESC, id DESC) INCLUDE (client_id, site_id, owner_team_id) WHERE deleted_at IS NULL;
BEGIN;
DROP INDEX idx_ci_list_sort;
ALTER INDEX idx_ci_list_sort_next RENAME TO idx_ci_list_sort;
COMMIT;
```

Ebenso der Index der Standardsortierung der Ticketliste (Migration 000277):

```sql
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_ticket_list_sort ON ticket (organization_id, created_at DESC, id DESC);
```

Ebenso der Index der Standardsortierung der Asset-Liste (Migration 000278):

```sql
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_asset_list_sort ON asset (organization_id, created_at DESC, id DESC);
```

Ebenso der Index der Mengenartikel-Liste (Migration 000279):

```sql
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_consumable_list_name ON consumable (organization_id, name) INCLUDE (client_id);
```

Ebenso der Index der Feldzeilen, die die globale Suche abgleicht (Migration
000280):

```sql
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_search_document_field_match ON search_document_field (organization_id, document_id) WHERE starts_with(field, 'attributes.') OR field = 'core.tags';
```

Ebenso der Index der CI-Liste sortiert nach Name (WP-839, E-17, Migration
000196):

```sql
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_ci_list_name ON ci (organization_id, name, id) WHERE deleted_at IS NULL;
```

Ebenso der Index des Filters auf `warranty_days_left` (Befund WP-824,
Migration 000274; die gespeicherte Spalte `warranty_days_left_num` legt die
Migration an, der Index kann danach vor dem übrigen Update gebaut werden):

```sql
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_ci_warranty_days_left ON ci (organization_id, warranty_days_left_num) WHERE deleted_at IS NULL AND warranty_days_left_num IS NOT NULL;
```

Ebenso der Index der Adressen einer Schnittstelle (WP-608, NFR-03):

```sql
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_ip_address_interface ON ip_address (interface_id) WHERE interface_id IS NOT NULL;
```

Ebenso der Override-Index der Knotenlesung (WP-840, NFR-02):

```sql
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_ci_field_value_override ON ci_field_value (organization_id, ci_id) WHERE override_at IS NOT NULL;
```

Ebenso der Versionsindex der Änderungsspur (WP-740, Migration 000236; die
Spalte `entity_version` legt die Migration an, der Index kann danach vor dem
übrigen Update gebaut werden, wenn die Spalte vorab per `ALTER TABLE
entity_change ADD COLUMN IF NOT EXISTS entity_version BIGINT` angelegt
wird):

```sql
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_entity_change_version ON entity_change (entity_type, entity_id, entity_version);
```

Ebenso der Seriennummernindex der automatischen Verknüpfung im Ingest
(WP-741, Migration 000241; die gespeicherte Spalte `serial_number_norm`
muss dafür vorab angelegt sein, `ALTER TABLE asset ADD COLUMN IF NOT EXISTS
serial_number_norm TEXT GENERATED ALWAYS AS (NULLIF(lower(btrim(serial_number)), '')) STORED`,
was die Tabelle neu schreibt; ohne Vorbau legt die Migration Spalte und Index
selbst an):

```sql
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_asset_org_serial_norm ON asset (organization_id, serial_number_norm) WHERE serial_number_norm IS NOT NULL;
```

Jede Anweisung einzeln ausführen (nicht in einer Transaktion); ein
abgebrochener Lauf hinterlässt einen ungültigen Index, der mit `DROP INDEX
CONCURRENTLY` entfernt und neu gebaut wird. Die Migration entfernt danach nur
noch die ersetzten Indizes; `DROP INDEX` hält die Sperre nur kurz.

## Rollenvertrag (TEN-03, CH19)

Migration 000078 legt den Rollenvertrag fest; `backend/internal/database/role_check.go`
(`VerifyRoleContract`) prüft ihn bei jedem Start von `database.NewPool` gegen die
effektiven Rechte (inklusive geerbter Mitgliedschaften) und verweigert den
Start bei jeder Abweichung:

| Rolle | Eigenschaften | Rechte |
|---|---|---|
| Migrations-/Admin-Login (z. B. `reticora`) | führt die Migrationen aus | darf SUPERUSER sein; nie Login des Servers (`database.NewPool` weist ihn ab), nur Werkzeuge über `database.NewAdminPool` |
| Laufzeit-Login (z. B. `reticora_runtime`) | LOGIN, NOSUPERUSER, NOBYPASSRLS | nur Mitglied von `reticora_app`; der Pool wechselt per `SET ROLE`; erreicht keine SUPERUSER-/BYPASSRLS-Rolle |
| `reticora_app` | NOLOGIN, NOSUPERUSER, NOBYPASSRLS | DML/Sequenzen/EXECUTE; `SET ROLE reticora_owner` erlaubt, aber ohne Vererbung (`INHERIT FALSE`); kein eigenes `CREATE` auf `public` |
| `reticora_owner` | NOLOGIN, NOSUPERUSER, NOBYPASSRLS | Eigentümer aller Tabellen in `public` außer `schema_migrations` und TimescaleDB-Hypertables; `USAGE, CREATE` auf `public` |

- Jede Tabelle mit `organization_id` (plus `organization`), auf die
  `reticora_app` zugreifen darf, hat `ENABLE` und `FORCE ROW LEVEL SECURITY`
  und gehört `reticora_owner`; die Policies binden damit auch den Eigentümer.
  `metric_sample` ist für `reticora_app` nicht zugreifbar (View-Barriere,
  WP-040) und behält ihren Eigentümer; ebenso `probe_result` (View
  `probe_result_v`, WP-945, ADR 0001).
- Laufzeit-DDL (DB-04): Die Anwendung wechselt für Index-DDL ausdrücklich in
  `reticora_owner`. Der Event-Trigger `reticora_restrict_app_ddl` erlaubt
  beiden App-Rollen nur `CREATE/ALTER/DROP INDEX`; RLS abschalten, Policies,
  Grants oder neue Tabellen bleiben den Migrationen vorbehalten.
- Der Event-Trigger `reticora_assign_owner` übergibt Tabellen, die spätere
  Migrationen in `public` anlegen, an `reticora_owner`.
- Login (WP-352, F-057): Weder `session_user` noch `current_user` ist
  SUPERUSER oder BYPASSRLS, und keine von ihnen erreichbare Rolle (geerbt oder
  per `SET ROLE`) ist es; sonst gäbe `RESET ROLE` jeder Pool-Verbindung die
  Rechte zurück. `VerifyRLSEnforced` und `VerifyRoleContract` melden das als
  `RoleContractError` mit dem Session-User.
- TRUNCATE-Guard (Migration 000144, F-192): `TRUNCATE` umgeht RLS, und der
  Eigentümer `reticora_owner` darf es implizit. Jede Mandantentabelle (auch
  `metric_sample`, `probe_result` und die Materialisierungs-Hypertable von
  `metric_sample_1h`) trägt den Statement-Level-Trigger
  `reticora_truncate_guard` (`BEFORE TRUNCATE`), der für `reticora_app` und
  `reticora_owner` mit 42501 abbricht. Entfernen oder Abschalten des Guards ist
  DDL und für beide Rollen durch `reticora_restrict_app_ddl` gesperrt.
  `VerifyRoleContract` meldet jede Mandantentabelle ohne aktiven Guard und
  einen Login mit eigenem `TRUNCATE`-Recht; eine neue Mandantentabelle braucht
  den Trigger daher in ihrer Migration.

Nachweis: `backend/internal/database/role_check_integration_test.go`.

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
| `team-scope` | Tabellen mit `team_id` oder `owner_team_id` verwenden `app.team_scope` in `USING` und `WITH CHECK` (CH25) |
| `org-column` | jede Tabelle außer `organization`, `schema_migrations` und den globalen Katalogen hat `organization_id` |
| `read-only-catalog` | globale Kataloge ohne `organization_id` (`rls.GlobalCatalogTables`) gewähren `reticora_app` kein INSERT/UPDATE/DELETE |
| `system-only` | Systemtabellen ohne `organization_id` (`rls.SystemTables`) haben ENABLE + FORCE RLS und den TRUNCATE-Schutz, jede Policy verlangt `app.system` (USING und WITH CHECK), keine erlaubt DELETE, `reticora_app` hat kein DELETE/TRUNCATE |
| `located-scope` | Tabellen mit Standort- oder CI-Bezug (Spalte `location_id`, `room_id`, `rack_id` oder `ci_id`, auch mit Präfix wie `from_location_id` oder `child_ci_id`, oder Knoten des Standortbaums) tragen Client- und Site-Dimension (`client_id` und `site_id`, auch mit Präfix `source_`/`target_`) oder stehen in `rls.DerivedScopeTables` (TEN-02) |

Das Verhalten der Policies prüft `TestRLSMatrix`
(`backend/internal/tenant/rls/matrix_integration_test.go`, WP-065) für jede
Mandantentabelle mit echten Anweisungen der App-Rolle: Prinzipale mit Org-,
Client-, Site-, Team- und kombiniertem Scope sowie eine fremde Organisation ×
SELECT/INSERT/UPDATE/DELETE × Zeilen in und außerhalb des Scopes, org-weite
Zeilen (Scope-Spalte NULL) und globale Katalogzeilen. Die Lesesemantik ist
strikt gepinnt (TEN-05): Org-weite Zeilen sind für gescopte Prinzipale
sichtbar, globale Zeilen für alle (Ausnahme `job`: Systemjobs nur mit
`app.system`, `matrixHiddenGlobalRows`). Gescopte Prinzipale dürfen org-weite
Zeilen aber weder anlegen noch ändern, löschen oder durch Setzen ihres Scopes
übernehmen (TEN-05). Dafür ergänzt Migration 000079 je Tabelle, deren
`ALL`-Policy mehr zeigt als sie schreiben lässt, restriktive Policies
`<tabelle>_scoped_update` und `<tabelle>_scoped_delete` mit dem
WITH-CHECK-Ausdruck als USING. Tabellen ohne Client- und Site-Spalte, deren
Zeilen die ganze Organisation betreffen, schreiben nur Prinzipale ohne
Client- und Site-Einschränkung (E-09, `matrixOrgWideWrites`); gescopte
Prinzipale lesen sie. Heute ist das `vrf` (Migration 000148, WP-372, F-089):
die restriktiven Policies `vrf_scoped_insert`, `vrf_scoped_update` und
`vrf_scoped_delete` verlangen leeres `app.client_scope` und `app.site_scope`.
`job` trägt statt Client-, Site- und Team-Spalten den Scope-Snapshot seines
Erzeugers (`scope_recorded`, `scope_clients`, `scope_sites`, `scope_teams`,
Migration 000154, WP-404, F-163): `job_isolation` verlangt in `USING` und
`WITH CHECK` je Dimension leere Scope-GUC oder einen Snapshot, der vollständig
im Scope liegt; die restriktive `job_scope_select` wendet das auch auf die
Lesezugriffe an, die `job_system_select` sonst org-weit öffnen würde. Jobs ohne
Snapshot (Systemjobs, Altbestand) lesen und schreiben nur Prinzipale ohne
jede Scope-Einschränkung (`matrixSnapshotScoped`); die Snapshot-Regeln prüft
`TestJobScopeAgainstPostgres` (`backend/internal/jobs/scope_integration_test.go`).
`webhook_subscription` trägt denselben Snapshot (Migration 000155, WP-601,
E-98): `webhook_sub_isolation` prüft Organisation und Snapshot in `USING` und
`WITH CHECK`, die restriktive `webhook_sub_scope_select` jeden Lesezugriff
(außer mit `app.system`); Abos ohne Snapshot (Altbestand) lesen und verwalten
nur Prinzipale ohne jede Scope-Einschränkung, die Regeln prüft
`TestSubscriptionScopeAgainstPostgres`
(`backend/internal/webhook/subscription_scope_integration_test.go`).
Die Testzeilen werden mit
`session_replication_role = replica` synthetisiert (ohne Fremdschlüssel und
Trigger, RLS bleibt aktiv). Tabellen, deren Policy den Scope über eine
Unterabfrage erbt (`document`, `document_version`, `maintenance_window`,
`review_item`, `training_assignment`), prüft die Matrix ebenfalls mit allen Prinzipalen
(WP-353, F-354): Ihre Zeilen entstehen über echte Elternzeilen (CIs mit
Client und Standort, Benutzer mit Teammitgliedschaften) bei aktiven Triggern,
einschließlich geteilter, einseitig verlinkter, unverlinkter und nur mit
org-weiten CIs verlinkter Zeilen; eine neue Tabelle dieser Art lässt die
Matrix scheitern, bis sie in `inheritedModels` modelliert ist.

### Abgeleiteter Scope der Modultabellen (WP-028, WP-353)

`ticket`, `ticket_comment`, `document_link`, `maintenance_window_ci`
(Migration 000064) und `disposal_record` (Migration 000145) leiten
`client_id` und `site_id` per Trigger aus dem referenzierten Objekt ab
(Entsorgung: Client des Assets und Standort seiner Location, ohne Asset aus
dem CI) und folgen dessen Verschiebung; `disposal_record` bleibt
revisionssicher, der Append-only-Trigger lässt nur diese Neuableitung durch
die Propagationstrigger zu. Bewusste Abweichungen von WP-028 AK1:

- `key_assignment` hat nur `client_id`: `key_item` trägt keinen Standort
  (Freitext `location` aus Migration 000050), ein `site_id` ist nicht
  ableitbar.
- `document` und `maintenance_window` haben keine eigene `client_id`: Sie
  sind Mehrobjektzeilen, ihr Scope ergibt sich aus den Links
  (`document_link`, `maintenance_window_ci`). Gescopte Prinzipale ändern oder
  löschen sie nur, wenn jeder Link in ihrem Schreib-Scope liegt (Migrationen
  000139 und 000146; ein Link auf ein org-weites Objekt zählt nicht, E-09),
  und legen keine unverlinkten an (WP-341).

Ein Ticket ohne CI übernimmt seit Migration 000272 (WP-828) Client und
Standort seines Assets (`derive_ticket_scope`, vorher nur den Client):
Ohne Standort blieb das Ticket eines Assets für jeden Standort-Scope lesbar
(E-09), auch im Suchindex. Der Backfill leitet die Tickets eines Assets ohne
Standort neu ab, `updated_at` bleibt; die Down-Migration stellt die Funktion
wieder her und lässt die Werte stehen.

### Entsorgungsnachweis nach E-55 (WP-751)

Migration 000245 ergänzt `disposal_record` um `protection_class`
(`smallint`, `disposal_record_protection_class_check`: 1 bis 3),
`security_level` (`text`, `disposal_record_security_level_check`:
`^[POTEFH]-[1-7]$`), `supplier_id`, `evidence_document_id`,
`performer_surrogate` und `witness_surrogate` (`uuid`, Vault-Subjekte der Art
`person`, ohne FK) sowie `record_hash` (`text NOT NULL`). Die Spalte
`performed_by` bleibt für Altzeilen und wird nicht mehr geschrieben.
`record_hash` setzt der BEFORE-INSERT-Trigger `disposal_record_set_hash` über
`disposal_record_hash(…)` (SHA-256 hex über `convert_to(<kanonisches JSON>,
'UTF8')` aus `id`, `organization_id`, `asset_id`, `ci_id`, `method`,
`protection_class`, `security_level`, `data_carrier`, `certificate_ref`,
`supplier_id`, `evidence_document_id`, `performer_surrogate`,
`witness_surrogate`, `performed_at`, `notes`, `created_at`; ohne
`performed_by`, `client_id` und `site_id`). Altzeilen sind unter
abgeschaltetem `disposal_record_no_update` nachgefüllt. Verweise mit
Organisation (K19), alle ON DELETE RESTRICT: `disposal_record_asset_id_fkey`
→ `asset`, `disposal_record_ci_id_fkey` → `ci` (vorher SET NULL),
`disposal_record_supplier_id_fkey` → `supplier`,
`disposal_record_evidence_document_id_fkey` → `document`. Dazu
`disposal_record_object_check` (`asset_id` oder `ci_id`, NOT VALID) und die
Teilindizes `idx_disposal_record_asset`, `idx_disposal_record_ci`,
`idx_disposal_record_supplier` und `idx_disposal_record_evidence_document`
(je Spalte `WHERE … IS NOT NULL`). RLS, Policies und Scope-Trigger aus 000145
bleiben unverändert.

### Linkziele von Dokumenten (WP-267)

`document_link.entity_type` ist eines von `ci`, `asset`, `ticket`,
`location`, `disposal_record`, `stocktake`, `contract` und
`stock_movement` (`doc_link_entity_type_check`, Migrationen 000217, 000238,
000242 und 000266); die Registry in `backend/internal/document/links.go` hält
dieselben Werte (`stock_movement` seit WP-276). Für
`stock_movement` übernimmt `derive_document_link_scope` Client und Standort
der Bewegung (eingefroren, keine Propagation). `assignment` entfiel mit dem
Drop der Tabelle (000266, WP-843): Die Links der Zuweisungen hängen an der
Bewegung gleicher ID, Links ohne Bewegung liegen in `migration_quarantine`.
`derive_document_link_scope` leitet `client_id`/`site_id` auch aus
Location, Entsorgung und dem Standort des Assets ab und
`team_id` (Teilindex `idx_document_link_team`) aus dem Ticket. Neue
AFTER-UPDATE-Trigger auf `location` und `disposal_record`
(`propagate_document_link_scope`) sowie `asset_module_scope_propagation`
(auch bei `site_id`) und `ticket_scope_propagation` (auch bei
`team_id`) leiten die Links neu ab. Die Policy von `document_link` trägt
das Team-Prädikat wie `ticket`; ein team-beschränkter Prinzipal gilt für
`document` (Policies, `require_visible_parent`, `require_linked_row`)
nicht mehr als org-weit. `app_scope_widen`/`app_scope_restore` leeren
bzw. setzen auch `app.team_scope`.

Seit Migration 000275 (WP-827, ABN-03) überlebt kein Link sein Ziel: Die
AFTER-DELETE-Trigger `<tabelle>_document_links` auf `ci`, `asset`,
`ticket`, `location`, `disposal_record`, `stocktake` und `stock_movement`
rufen `delete_target_document_links(<entity_type>)` und entfernen die Links
des gelöschten Ziels in derselben Transaktion (wie
`contract_document_links` aus 000238; `document.link_count` folgt über
`count_document_links`, die Dokumente bleiben; erweiterter Scope über
`app_scope_widen`, fester `search_path`). Links, die beim Upgrade auf kein
Ziel zeigen, stehen mit Grund `dropped: link target missing` in
`migration_quarantine` und werden entfernt.

Seit Migration 000281 (WP-852, SR-56) wird ein Dokument durch den Wegfall
seiner Links nicht mehr org-weit: `document.orphaned` (boolean, Standard
`false`) setzt `count_document_links`, sobald `link_count` auf 0 fällt;
ein neuer Link setzt es nicht zurück, schreiben darf es nur der
Zähler-Trigger (`document_orphaned_guard`, `protect_link_counter`).
`document_tenant_isolation` zeigt ein Dokument ohne Link nur dann allen
Prinzipalen der Organisation, wenn es nie Links hatte
(`link_count = 0 AND NOT orphaned`); ein verwaistes sehen nur org-weite
Prinzipale. Backfill aus `migration_quarantine` (000275) und den
`audit_log`-Einträgen `document.linked`/`document.unlinked`.

### Dokumentversionen und Speichernutzung (WP-731)

`document_version` (Migration 000220) hält je Upload eine Version
(`UNIQUE (document_id, version)`, Verweis `(document_id, organization_id)`
auf `document (id, organization_id)` mit `ON DELETE CASCADE`), mit Größe,
MIME-Typ, `checksum_sha256` (NULL nur aus dem Backfill), `scan_status`
(`pending_scan`, `clean`, `infected`, `scan_failed`) und dem Uploader als
Surrogat ohne Fremdschlüssel. `document.version`, `storage_key`,
`file_size` und `mime_type` spiegeln die höchste Version. Die Policy
`document_version_isolation` prüft die Organisation und, sobald ein Client-,
Standort- oder Team-Scope gesetzt ist, die Sichtbarkeit des Dokuments; die
BEFORE-Trigger `document_version_parent_visible` (`require_visible_parent`
mit Team-Argument) und `document_version_storage_key` (Schlüssel unter
`org/<org>/documents/<dokument>/` oder Legacy `documents/<org>/<dokument>/`,
ohne `..`) prüfen jede neue Version. `organization_usage` zählt die
gespeicherten Bytes je Organisation (`storage_bytes >= 0`); ihre Policy
prüft nur `app.org_id`, die Nutzung zählt daher org-weit. Der Upload
prüft sie in der Transaktion der Version gegen `max_storage_mb`.

### Soft-Delete von Dokumenten (WP-732)

`document.deleted_at` (Migration 000221) markiert ein gelöschtes Dokument;
der Teilindex `idx_document_deleted` auf `(organization_id, deleted_at)
WHERE deleted_at IS NOT NULL` dient dem Retention-Lauf. Die Policies von
`document` bleiben unverändert: Repository (`documentVisible`,
`documentWritable`) und Suche (`hitVisible`, `ReindexTenant`) blenden
gelöschte Dokumente aus, damit `retention.soft_deleted` sie unter
`OrgWideScope` sieht. Versionen, Links und die Nutzung in
`organization_usage` bleiben bis zum Purge; erst dieser löscht das Dokument
(Versionen und Links per `ON DELETE CASCADE`) und verringert
`storage_bytes`.

### Scanstatus von Dokumentversionen (WP-733)

`document_version.scan_signature` (Migration 000222) hält den
Signaturnamen eines Malware-Befunds (NULL ohne Befund). Der Teilindex
`idx_document_version_scan_open` auf `(organization_id, scan_status)
WHERE scan_status <> 'clean'` dient den offenen Scans (`pending_scan`,
`scan_failed`) je Organisation (`PendingScans`, Sweep aus WP-734); er
folgt der Konvention „Indizes ohne lange Sperren“. Der Backfill setzt jede
Version ohne `scanned_at` auf `pending_scan` (fail-closed nach E-42).
Quarantäne ist der Status `infected`: Blob und `document_link` bleiben, die
Policies sind unverändert. Abrufbar ist nur eine Version mit `clean`.

### Standort von Assets und Mengenartikeln (WP-392)

`asset` und `quantity_item` tragen `site_id`, den Standort ihrer Location
(Migration 000151, F-141; `quantity_item` entfällt mit 000269, WP-758). Der Trigger `derive_location_site` setzt ihn bei
jedem INSERT und jeder Änderung von `location_id` oder `site_id` und
überschreibt geschriebene Werte; er liest die Location mit erweitertem
Client- und Site-Scope (`app_scope_widen`), eine Location einer anderen
Organisation gilt als nicht gefunden (23503). Ändert eine Location ihren
Standort (Verschieben, Umhängen), leitet `location_refresh_asset_site` die
Zeilen an ihr neu ab. Die Policies prüfen `app.site_scope` wie bei `ci`
(Migration 000063): Zeilen ohne Standort bleiben lesbar, schreiben dürfen
sie nur org-weite Prinzipale (E-09).

Ein neuer `location_id` eines Assets geht über `asset_propagate_location`
unter der RLS des Schreibenden an seine CIs. Behält danach ein CI des Assets
– geprüft mit erweitertem Client- und Site-Scope – einen anderen Standort,
weil der Schreibende es nicht sieht oder nicht ändern darf, lehnt der
Trigger die Änderung mit 42501 (`asset_location_ci_scope`) ab (Migration
000181, F-380); Asset und CIs weichen nicht still voneinander ab.

### Scope von Reservierungen (WP-750)

`reservation` trägt `client_id` und `site_id` (Migration 000258, TEN-02,
E-40) mit Fremdschlüsseln samt Organisation (K19):
`reservation_client_id_fkey` auf `client (id, organization_id)` und
`reservation_site_id_fkey` auf `site (id, organization_id)`, beide ohne
Löschaktion. Der Trigger `reservation_scope` (`derive_reservation_scope`)
leitet beide bei INSERT und bei einer Änderung von Objekt, Client oder
Standort aus dem Asset ab, sonst seit Migration 000264 (WP-284) aus der
`stock_level`-Zeile des Bins (`consumable_id`, `location_id`),
und überschreibt geschriebene Werte; er liest unter der RLS des Schreibenden,
ein Objekt außerhalb des Scopes ergibt NULL und scheitert für gescopte
Prinzipale an WITH CHECK (E-09). Ändert ein Asset oder eine Bestandszeile Client
oder Standort, leitet `propagate_reservation_scope` dessen Reservierungen mit
erweitertem Scope (`app_scope_widen`) neu ab. Die Policies folgen `asset`
(000151) mit den restriktiven UPDATE- und DELETE-Policies; die Unterabfrage
`reservationVisible` des Repositorys entfällt. Ein gelöschter Standort mit
Reservierungen meldet die Abhängigkeit `asset` (`locations.DeleteError`). Die
Migration setzt `expires_at = reserved_until` für aktive Reservierungen ohne
`expires_at` (Regel aus WP-277); die Down-Migration lässt diese Werte stehen.

### Standort von Räumen und Racks (WP-393)

`room` und `rack` tragen `client_id` und `site_id` ihrer 1:1-Location-Zeile
(Migration 000152, F-053), beide `NOT NULL`: Jeder Raum und jedes Rack hat
eine Location mit Client und Standort, org-weite Zeilen gibt es nicht. Der
Trigger `derive_location_row_scope` setzt die Werte bei jedem INSERT und
UPDATE und überschreibt geschriebene Werte; er liest die Location mit
erweitertem Client- und Site-Scope. Ändert die Location-Zeile eines Raums
oder Racks Client oder Standort (Verschieben im Baum, Client-Wechsel der
Site), leitet `location_refresh_room_rack_scope` die Zeile neu ab. Die
Policies `room_isolation` und `rack_isolation` verwenden das Prädikat von
`location` (Migration 000062) in USING und WITH CHECK. Die Suchzeilen
(`search_document`) von Assets und ihren Reservierungen übernehmen den
Standort des Assets, Reservierungen von Mengenartikeln Client und Standort
des Artikels, und folgen deren Änderung.

Tabellen mit Standort- oder CI-Bezug ohne eigene Scope-Spalten stehen mit
Ableitungspfad, Begründung und prüfendem Test in `rls.DerivedScopeTables`
(`backend/internal/tenant/rls/known_gaps.go`, Regel `located-scope`):
`form_submission` (Sichtbarkeit über das referenzierte Formular, CI und
Ticket per Repository-Prädikat), `building` (Standort per `site_id`, Client
über die Site) und `metric_sample` (über die Views `metric_sample_v` und
`metric_sample_1h_v`). `TestRLSCatalog` scheitert für einen Eintrag ohne
vorhandenen Test und für einen Eintrag, den die Regel nicht meldet.

Globale Kataloge ohne Org-Spalte sind nach E-10 eine dokumentierte Ausnahme,
keine Lücke: Sie werden nur durch Migrationen gepflegt und sind für die
App-Rolle schreibgeschützt. Heute ist das nur `permission` (WP-024). Globale
Katalogzeilen in Tabellen mit Org-Spalte (`organization_id IS NULL`, z. B.
System-CI-Typen) sind über getrennte Policies je Kommando nur lesbar. Ein CI
hat einen System-Typ oder einen Typ seiner Organisation: Der einspaltige
Fremdschlüssel `ci_ci_type_id_fkey` umgeht RLS, deshalb prüft der
Constraint-Trigger `trg_ci_type_organization` (Migration 000153, WP-396,
F-144) jedes INSERT und jede Änderung von `ci_type_id` (23514
`ci_type_organization`).

Die organisationsübergreifende Tabelle `operator_audit` (SEC-07, WP-070) ist
die zweite dokumentierte Ausnahme ohne Org-Spalte: ENABLE + FORCE RLS mit einer
Policy, die nur im Operator-Kontext (`app.operator`, `database.WithOperator`)
greift; Mandantentransaktionen lesen und schreiben nichts. `reticora_app` hat
kein UPDATE/DELETE, zusätzlich verhindern Trigger Änderungen
(`TestRLSCatalog` prüft die Regel `operator-only`). Nach demselben Muster ist
`signup_request` (WP-111) geschützt: Ein Signup-Antrag existiert vor seiner
Organisation und ist nur im Operator-Kontext (`database.WithOperator`,
`database.WithOrganizationSetup`) erreichbar.

`platform_secret` (Migration 000163, SEC-06, WP-459) ist eine Systemtabelle der
Installation ohne Org-Spalte, eine weitere dokumentierte Ausnahme
(`rls.SystemTables`): Sie hält die Ableitungswurzel der abgeleiteten Secrets,
mit dem Master-Key versiegelt. Jede Policy verlangt das System-Flag
(`app.system`); der Server liest mit `database.WithSystem` und legt die Wurzel
nur über `database.WithSystemWrite` an bzw. versiegelt sie neu (Aufrufer in
`systemWriteCallers`, `TestSystemWriteCallersAreListed`). Mandanten- und
Operator-Transaktionen lesen und schreiben nichts. Keine Policy erlaubt DELETE,
`reticora_app` hat kein DELETE, der TRUNCATE-Schutz gilt wie bei den
Mandantentabellen (`TestRLSCatalog` prüft die Regel `system-only`).

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

<!-- rls-known-gaps:end -->

### Automatische Ping-Probes (WP-949)

Migration 000345 (vorläufige Nummer) ergänzt `probe.origin` (`text NOT NULL
DEFAULT 'manual'`, `CONSTRAINT probe_origin_check CHECK (origin IN ('manual',
'auto'))`): `auto` markiert die Probes des Jobs `probe.autocreate`, nur sie
folgen einer Änderung der Management-IP. Die Tabelle
`probe_autocreate_suppression` (Primärschlüssel `(organization_id, ci_id,
probe_type)`) merkt sich gelöschte Auto-Probes. Ihre Verweise tragen die
Organisation (K19): `probe_autocreate_suppression_ci_fkey` `(ci_id,
organization_id) REFERENCES ci (id, organization_id) ON DELETE CASCADE`,
`probe_autocreate_suppression_client_fkey` auf `client` und
`probe_autocreate_suppression_site_fkey` auf `site`. `client_id` und
`site_id` leitet der Trigger `probe_autocreate_suppression_scope` wie
`probe_ci_scope` aus dem CI ab; ein verschobenes CI nimmt seine Einträge über
`propagate_monitoring_scope` mit (Version aus 000307 plus diese Tabelle). Die
Policies folgen Konvention 5 (located-scope, USING und WITH CHECK, RESTRICTIVE
für UPDATE und DELETE), dazu FORCE RLS und Truncate-Guard.

## Indizes unter RLS (SR-04, K22, WP-836)

Unter `FORCE ROW LEVEL SECURITY` übernimmt der Planer für die App-Rolle
`reticora_app` eine Bedingung nur dann als Indexbedingung vor die Policies,
wenn alle beteiligten Funktionen leakproof sind (`pg_proc.proleakproof`).
Sonst wertet er sie erst nach den Policies aus und liest die Tabelle. Regel K22
für jede Tabelle mit RLS:

- Kein Index, dessen Ausdruck oder Prädikat eine nicht-leakproof Funktion
  aufruft, direkt oder als Funktion eines Operators (Beispiele: `->>`
  (`jsonb_object_field_text`), `lower`, `to_tsvector`, `||` (`textcat`),
  `int4pl`, Umwandlung über die Textdarstellung wie `id::text`).
- Kein Index über eine Operatorklasse, deren Suchoperatoren für den
  Spaltentyp alle nicht leakproof sind (`pg_opclass`, `pg_amop`). In
  PostgreSQL 16 betrifft das u. a. `tsvector_ops` (`@@`, `ts_match_vq`),
  `jsonb_ops` und `jsonb_path_ops` (`@>`), `array_ops` (`@>`, `&&`),
  `range_ops`, die `ltree`-Klassen und `numeric`-Vergleiche. Leakproof sind
  u. a. `texteq`, `uuid_eq`, `macaddr_eq`, `network_eq` und die Vergleiche auf
  `date` und `timestamptz`.
- Suchfelder liegen deshalb als gespeicherte Spalte mit leakproof Operator
  (`GENERATED ALWAYS AS … STORED`, Muster 000134 `hostname_lc`), in einer
  Seitentabelle (`ci_identity_mac`, 000161) oder in `search_document` mit dem
  gespeicherten `search_vector`. Attributwerte vergleichen Filter und
  Attributindizes über die LEAKPROOF-Funktion `reticora_attr_text(jsonb,
  text)` (000162, E-73 Korrekturplan); die Indizes `idx_attr_*` sind damit
  unter RLS nutzbar. Nach E-133 setzt keine weitere Migration eine Funktion
  `LEAKPROOF`.
- Zahlenvergleiche (`numeric`) eines Attributs sind damit nicht indexfähig.
  Für das berechnete Systemattribut `warranty_days_left` (000225) legt
  Migration 000274 die gespeicherte Spalte `ci.warranty_days_left_num`
  (`double precision GENERATED ALWAYS AS … STORED`, NULL außer für eine
  JSON-Zahl, begrenzt auf ±Infinity bzw. 0, wirft nie) mit dem Index
  `idx_ci_warranty_days_left (organization_id, warranty_days_left_num)
  WHERE deleted_at IS NULL AND warranty_days_left_num IS NOT NULL` an. Der
  Filter der gespeicherten Sichten vergleicht die Spalte mit den leakproof
  `float8`-Operatoren als grobe Indexbedingung (Operand mit derselben
  monotonen Abbildung) und behält den exakten `numeric`-Vergleich daneben,
  die Treffer bleiben gleich (`TestWarrantyDaysLeftIndexKeepsResults`,
  `TestSavedViewQueryBudget`). `ne` und `exists` bleiben ohne Index.

Migration 000191 entfernt `idx_ci_fts` (`to_tsvector` über `ci`, 000021): Keine
Abfrage nutzte den Ausdruck, die Volltextsuche liest
`search_document.search_vector`. `ci` erhält keine zweite `tsvector`-Spalte.
Volltext (`idx_search_document_fts`) und `UniqueConflicts` (Schlüssel aus
`attribute_definition`, `c.attributes ->> d.key`) bleiben für die App-Rolle
ein dokumentierter Scan, linear im Budget bis 10.000 Objekte (E-133 Option 3,
E-17; `TestSearchPlanUnderRLS`, `TestUniqueConflictsPlanUnderRLS`). Der
Attributfilter der CI-Liste hat die Indexbedingung über `reticora_attr_text`
(`TestAttributeFilterUsesIndexUnderRLS`).

`rls.LeakyIndexQuery` (`backend/internal/tenant/rls/known_gaps.go`) findet
solche Indizes im Katalog (`pg_index.indexprs`/`indpred`, `pg_opclass`,
`pg_amop`, `pg_proc.proleakproof`). `TestRLSIndexesAreLeakproof` verlangt,
dass die gefundenen Indizes genau `rls.LeakyIndexes` entsprechen: Ein neuer
Index ohne Eintrag und ein Eintrag ohne Fund lassen den Test scheitern, die
Liste kann also nur schrumpfen. `TestIndexStatementLeakproof`
(`backend/internal/citype`) prüft mit derselben Abfrage die Indizes des
DDL-Jobs (`citype.IndexStatement`, `citype.KeyIndexStatement`). Die Gründe
je Eintrag stehen in `known_gaps.go`; die Tabelle wird mit
`RETICORA_UPDATE_SCHEMA_BASELINE=1 go test -run TestLeakyIndexesDocumented
./internal/tenant/rls/` neu erzeugt.

Die Feldzeilen der Volltextsuche (`search_document_field`, 000228, RBA-07,
WP-266) haben keinen GIN-Index auf `vector`: `tsvector_ops` hat keinen
leakproof Operator, die Liste oben würde wachsen. Die Suche filtert mit
eingeschränkten Feldrechten über `search_document.search_vector` vor und
liest die Feldzeilen ihrer Kandidaten über den Primärschlüssel
`(document_id, field)`.

Seit Migration 000280 tragen die Feldzeilen `client_id` und `site_id` ihres
Dokuments. Der Trigger `search_field_scope` (BEFORE INSERT/UPDATE der
Schlüssel- und Scope-Spalten) übernimmt sie aus `search_document`, ein
Schreiber kann keine anderen setzen; `search_field_scope_propagation` auf
`search_document` zieht die Feldzeilen nach, wenn sich Mandant oder Standort
des Dokuments ändern (mit erweitertem Scope, `app_scope_widen`). Die
Policies entsprechen denen von `search_document` (000067, 000079):
USING mit NULL-Semantik nach E-09, WITH CHECK ohne NULL für eingeschränkte
Principals, restriktive `_scoped_update`/`_scoped_delete`. Die frühere
Prüfung per `EXISTS` auf `search_document` je Feldzeile entfällt (Lastlauf
42582336513: rund 45.000 Puffer je globaler Suche). Die Attribut- und
Tag-Zeilen, die die Suche abgleicht, liest sie über den Teilindex
`idx_search_document_field_match` (Prädikat nur mit leakproof Funktionen).

Inventarnummer, IP und MAC (SRC-02, WP-308, Migration 000233) liegen in
Normalform in `search_identifier` (MAC zwölf Hex-Ziffern ohne Trenner, IP
als `host()`-Text und Hostadresse in `addr`, Inventarnummer
`lower(btrim())`). Gleichheit und Bereiche auf `norm` (`text_pattern_ops`:
`=`, `~>=~`, `~<~`) und `addr` (`inet`-Vergleiche) sind leakproof; die
Indizes `idx_search_identifier_norm` und `idx_search_identifier_addr`
bedienen die Suche deshalb auch für die App-Rolle
(`TestSearchFieldsPlanUnderRLS`). Die Policy prüft nur die Organisation,
gelesen wird die Tabelle nur im Join mit `search_document`. Gepflegt wird
sie vom Indexer (`core.management_ip`, `core.primary_mac`,
`asset.inventory_no`) und von den Triggern `trg_asset_sync_search_index`,
`search_identifier_interface_*` und `search_identifier_ip_*`. Feldzeilen
haben die Spalte `source` (`indexer`, `trigger`); die Asset-Dokumente mit
ihren Feldzeilen pflegt der Trigger.

Migration 000271 (WP-788) macht Tags und Nutzer durchsuchbar, ohne neue
Tabelle und ohne Policy-Änderung: Jedes CI- und Asset-Dokument erhält die
Feldzeile `core.tags` (`source = 'trigger'`, Key und alle Sprachwerte von
`tag.display_name`, `search_tag_text`), gepflegt von
`search_index_ci_tag_*` (`ci_tag`), `search_index_asset_tag_*`
(`asset_tag_link`) und `search_index_tag_update` (`tag`) über
`search_refresh_tags`. Nutzer stehen nur als Surrogat (`app_user.id`) in
`search_identifier`: `core.owner_user_id` an CI und Asset,
`core.assigned_user_id` am Asset und `asset.assigned_user_id` an den CIs
des Assets (`source_id` = Asset); `search_index_asset` und sein Trigger
(zusätzlich `UPDATE OF owner_user_id, assigned_user_id`) sowie
`search_identifier_rows` sind entsprechend erweitert.

<!-- rls-leaky-indexes:begin -->

| Index | Tabelle | Nicht leakproof |
|---|---|---|
| `asn_range_no_overlap` | `asn_range` | function int8range(bigint,bigint,text), operator class range_ops |
| `desk_booking_no_overlap` | `desk_booking` | function tstzrange(timestamp with time zone,timestamp with time zone), operator class range_ops |
| `idx_ci_attributes_path` | `ci` | operator class jsonb_path_ops |
| `idx_location_path` | `location` | operator class gist_ltree_ops |
| `idx_pii_subject_tokens` | `pii_subject` | operator class array_ops |
| `idx_review_item_candidates` | `review_item` | operator class array_ops |
| `idx_saved_view_shared_roles` | `saved_view` | operator class array_ops |
| `idx_search_document_fts` | `search_document` | operator class tsvector_ops |
| `idx_security_finding_cve_ids` | `security_finding` | operator class array_ops |
| `idx_signup_request_email` | `signup_request` | function lower(text) |
| `idx_vuln_advisory_cve_ids` | `vuln_advisory` | operator class array_ops |
| `location_path_key` | `location` | operator class ltree_ops |
| `rack_mount_no_overlap` | `rack_mount` | function int4pl(integer,integer), function int4range(integer,integer), function rack_face_range(text), operator class range_ops |
| `review_item_open_override_conflict` | `review_item` | function jsonb_object_field_text(jsonb,text) |
| `review_item_open_serial_mismatch` | `review_item` | function jsonb_object_field_text(jsonb,text) |
| `review_item_open_unlicensed_ci` | `review_item` | function jsonb_object_field_text(jsonb,text) |

<!-- rls-leaky-indexes:end -->

## Objektverweise mit Organisation (SR-03, K19, WP-838)

PostgreSQL prüft Fremdschlüssel ohne Row Level Security. Ein Verweis
`REFERENCES … (id)` nimmt deshalb auch die ID eines Objekts einer fremden
Organisation an. Verweise zwischen zwei Mandantentabellen (Tabellen in
`public` mit `organization_id`) enthalten die Organisation:
`(spalte, organization_id) REFERENCES ziel (id, organization_id)` (Muster
000136, Regel K19). Ein Verweis in eine fremde Organisation scheitert dann wie
eine unbekannte ID mit 23503. Ausgenommen sind Ziele mit globalen Zeilen
(`organization_id` nullable, z. B. `ci_type`, E-10).

Migration 000194 legt `UNIQUE (id, organization_id)` auf `location` und
`consumable` an und stellt die Verweise `asset.ci_id`, `asset.location_id`,
`ci.location_id`, `ticket.related_ci_id`, `ticket.related_asset_id`,
`reservation.asset_id`, `disposal_record.asset_id`, `disposal_record.ci_id`,
`internal_order_item.asset_id`, `internal_order_item.consumable_id`,
`location.site_id` und `location.parent_id` um. Constraint-Namen und
Löschverhalten bleiben (SET NULL als `ON DELETE SET NULL (spalte)`);
`disposal_record.asset_id` und `disposal_record.ci_id` sind seit 000245
ON DELETE RESTRICT (WP-751).
Bestehende Zeilen mit einem Verweis über die Organisationsgrenze stehen danach
in `migration_quarantine` (Migration `000194`): Nullbare Verweise sind auf NULL
gesetzt, die ganze Zeile ist protokolliert; Reservierungen sind verschoben
(E-26). Knoten des Standortbaums mit fremdem Eltern- oder Site-Knoten lässt
`location_maintain` seit 000062 nicht zu; findet die Migration doch einen,
bricht sie mit der Liste ab (Muster 000177). Die Down-Migration stellt die
einfachen Fremdschlüssel und die Zeilen wieder her.

Migration 000287 (WP-876, SR-03 Rest I) stellt die 59 Verweise der Kern-,
Standort-, Netz-, Rack-, Rollen- und Discovery-Tabellen (37 Tabellen) um, u. a.
`ci_relationship.source_ci_id`, `network_interface.ci_id`,
`role_assignment.role_id`, `team_member.user_id`, `building.site_id` und die
Spezialzeilen `building.id`, `room.id`, `rack.id`, `site.id` auf ihren Knoten
in `location`. Wo das Ziel noch keinen hatte, legt sie
`<tabelle>_<spalte>_organization_key` an (`api_key`, `building`, `collector`,
`discovery_job`, `network_interface`, `rack`, `subnet` mit `id`,
`pii_subject` mit `surrogate_id`). Constraint-Namen und Löschverhalten bleiben
(SET NULL als `ON DELETE SET NULL (spalte)`). Fremde Verweise stehen in
`migration_quarantine` (Migration `000287`): nullbare auf NULL gesetzt,
Zeilen mit Pflichtverweis verschoben; eine Spezialzeile mit Knoten einer
anderen Organisation bricht die Migration mit der Liste ab. Die Down-Migration
stellt die einfachen Fremdschlüssel und die Zeilen wieder her und entfernt
einen angelegten Schlüssel nur, wenn kein Fremdschlüssel ihn mehr nutzt.

Migration 000288 (WP-877, SR-03 Rest II) stellt die übrigen 55 Verweise um
(28 Tabellen): Modul- und Ticket-Tabellen (`ticket`, `ticket_comment`,
`ticket_sla`, `stocktake`, `stock_scan`, `internal_order_item`, `document`,
`document_link`), Benachrichtigungen und Webhooks, die vorhandenen Tabellen
der Phase-3-Module (Wartung, Workflows, Formulare, Compliance), KI und IGA. Wo
das Ziel noch keinen hatte, legt sie `<tabelle>_id_organization_key` an (u. a.
`sla`, `webhook_subscription`, `workflow_def`, `maintenance_window`,
`notification_channel`). Quarantäne und Down-Migration wie 000287.

Migration 000275 (WP-827) legt `UNIQUE (id, organization_id)`
(`internal_order_id_organization_key`) auf `internal_order` an und gibt den
bis dahin ungebundenen Spalten `reservation.assignee_id`,
`reservation.ticket_id` und `reservation.order_id` zusammengesetzte
Fremdschlüssel (`reservation_assignee_id_fkey` auf `app_user`,
`reservation_ticket_id_fkey` auf `ticket`, `reservation_order_id_fkey` auf
`internal_order`, je `ON DELETE SET NULL (spalte)`). Bestehende Verweise ohne
Zeile in der eigenen Organisation stehen in `migration_quarantine`
(Migration `000275`) und sind auf NULL gesetzt.

Migration 000293 (WP-893) verweist von `vault_connection` mit Organisation
auf `client` (`vault_connection_client_fkey`) und auf die Anmeldedaten in
`credential` (`vault_connection_auth_credential_fkey`, ON DELETE RESTRICT),
von `vault_connection_collector` auf `vault_connection` (ON DELETE CASCADE)
und `collector`. Migration 000294 (WP-894) ergänzt
`credential_vault_connection_fkey`: `credential (vault_connection_id,
organization_id) REFERENCES vault_connection (id, organization_id)` mit ON
DELETE RESTRICT; eine referenzierte Tresorverbindung wird nicht gelöscht.

Migration 000329 (WP-1067, IPM-16) gibt `subnet` die Spalte
`origin_asn_id` (Origin-AS des Präfixes) mit dem zusammengesetzten
Fremdschlüssel `subnet_origin_asn_fkey`: `(origin_asn_id, organization_id)
REFERENCES asn (id, organization_id) ON DELETE SET NULL (origin_asn_id)`
und dem Teilindex `idx_subnet_origin_asn` auf `(organization_id,
origin_asn_id) WHERE origin_asn_id IS NOT NULL`. Die Funktion
`asn_next_free(org, lo, hi)` (SECURITY INVOKER, fester `search_path`)
liefert die kleinste freie AS-Nummer in `[lo, hi]` ohne 23456 und 65535 oder
NULL für eine volle Range; sie sucht mengenbasiert mit erweitertem Kunden-,
Standort- und Team-Scope (`app_scope_widen`/`app_scope_restore`), die
Organisation bleibt über `app.org_id` fest. Der Aufrufer serialisiert die
Vergabe je Range mit einem Transaktions-Advisory-Lock
(`pg_advisory_xact_lock`), weil ein Kunden-Principal die UPDATE-Policy
organisationsweiter Ranges nicht hat. Die Tabelle oben und die Zeile
„Stand:“ erzeugt `RETICORA_UPDATE_SCHEMA_BASELINE=1` beim Zusammenführen mit
den endgültigen Migrationsnummern neu.

Migration `nat_translation` (WP-1071, IPM-15, vorläufige Nummer 000336)
legt `nat_translation` an: Art (`kind` static, source, destination, pat),
Gerät, je genau eine Innen- und Außenseite als Adresse oder Subnetz
(`nat_translation_inside_check`, `nat_translation_outside_check`), Protokoll,
Ports nur bei tcp/udp (`nat_translation_ports_check`), pat verlangt den
Außenport (`nat_translation_pat_port_check`). Die Fremdschlüssel sind
zusammengesetzt mit Organisation (Konvention 19) und alle ON DELETE CASCADE:
`nat_translation_device_fkey` auf `ci`, `nat_translation_inside_address_fkey`
und `nat_translation_outside_address_fkey` auf `ip_address`,
`nat_translation_inside_subnet_fkey` und `nat_translation_outside_subnet_fkey`
auf `subnet`, dazu Teilindizes je Seite und `idx_nat_translation_device`.
`ip_address` und `subnet` erhalten `ip_address_id_organization_key` bzw.
`subnet_id_organization_key` (UNIQUE (id, organization_id)) nur, wenn noch
kein eindeutiger Schlüssel über genau diese Spalten besteht; Down entfernt sie
nur, solange kein Fremdschlüssel mehr auf sie verweist. `client_id` und
`site_id` folgen dem Gerät (Scope-Trigger mit erweitertem Scope);
`propagate_ci_scope` leitet sie bei einer Kunden- oder Standortänderung des CI
neu ab. ENABLE und FORCE RLS mit den Policies nach Konvention 5, `updated_at`
und `reticora_truncate_guard`. Die Tabelle oben und die Zeile „Stand:“
erzeugt `RETICORA_UPDATE_SCHEMA_BASELINE=1` beim Zusammenführen mit den
endgültigen Migrationsnummern neu.

Migration `bgp_peering` (WP-1068, IPM-17, vorläufige Nummer 000337) legt
`bgp_peering` als Soll-Inventar an: Gerät, VRF, lokale ASN, entfernte
AS-Nummer (`remote_asn_number`, 1 bis 4294967294, beim Lesen gegen `asn`
aufgelöst), lokale Adresse, entfernte Adresse (`inet`), `expected_state`
(established, admin_down) und Beschreibung. Eindeutig sind
`bgp_peering_remote_key` (organization_id, device_ci_id, vrf_id,
remote_address) und `bgp_peering_id_organization_key` (id, organization_id).
Die Fremdschlüssel sind zusammengesetzt mit Organisation (Konvention 19):
`bgp_peering_device_fkey` auf `ci` (ON DELETE CASCADE), `bgp_peering_vrf_fkey`
auf `vrf` und `bgp_peering_local_asn_fkey` auf `asn` (ON DELETE RESTRICT),
`bgp_peering_local_address_fkey` auf `ip_address` (ON DELETE SET NULL
(local_address_id)); dazu `idx_bgp_peering_remote_asn`, `idx_bgp_peering_vrf`
und Teilindizes für lokale ASN und Adresse. `ip_address_id_organization_key`
entsteht nur, wenn noch kein eindeutiger Schlüssel über (id,
organization_id) besteht; Down entfernt ihn nur ohne verbleibende Verweise.
`client_id` und `site_id` folgen dem Gerät (`trg_bgp_peering_scope`),
`propagate_ci_scope` leitet auch die Peerings eines CI neu ab. ENABLE und
FORCE RLS mit den Policies nach Konvention 5, `updated_at` und
`reticora_truncate_guard`. Die Tabelle oben und die Zeile „Stand:“ erzeugt
`RETICORA_UPDATE_SCHEMA_BASELINE=1` beim Zusammenführen neu.

`TestTenantReferencesCarryOrganization`
(`backend/internal/tenant/rls/object_reference_integration_test.go`) prüft
jeden Fremdschlüssel zwischen zwei Mandantentabellen im Katalog. Die
verbleibenden Verweise ohne Organisation stehen mit Grund in
`rls.ReferencesWithoutOrganization` (`known_gaps.go`): Ein neuer Verweis ohne
Organisation und ein behobener Eintrag lassen den Test scheitern, die Liste
kann also nur schrumpfen. Der Test schreibt außerdem als App-Rolle jede
umgestellte Spalte mit der ID eines Objekts einer fremden Organisation
(23503) und prüft die Fremdschlüssel bei abgeschalteten Triggern. Die Liste
wird mit `RETICORA_UPDATE_SCHEMA_BASELINE=1 go test -run
TestReferencesWithoutOrganizationDocumented ./internal/tenant/rls/` neu
erzeugt. Seit Migration 000288 (WP-877) ist die Liste leer: Jeder Verweis
zwischen zwei Mandantentabellen trägt die Organisation, SR-03 ist behoben, und
ein neuer Verweis ohne Organisation lässt den Test scheitern.

<!-- rls-references-without-organization:begin -->

| Verweis | Fremdschlüssel | Ziel | Grund |
|---|---|---|---|

<!-- rls-references-without-organization:end -->

## CI-Änderungen `ci_change` (AUD-03, WP-487)

Nach Entscheidung E-82 (Option 2) bleibt `ci_change` feldgranular: eine Zeile
je geändertem Feld, darauf beruhen die feldgranulare 409-Prüfung (API-07,
`ci_version`) und der Merge. Migration 000167 ergänzt die Spalten von AUD-03:

| Spalte | Inhalt |
|---|---|
| `change_kind` | `NOT NULL`, CHECK `chk_ci_change_kind`: `created`, `updated`, `deleted`, `restored`, `merged`, `type_changed`, `relationship_added`, `relationship_removed`, `status_changed`; gilt für die ganze Änderungsgruppe (ein Statuswechsel zusammen mit weiteren Feldern ist `updated`) |
| `change_set_id` | gruppiert die Feldzeilen eines Schreibvorgangs eines CI; Bestandszeilen vor 000167 haben `NULL` |
| `actor_user_id` | Benutzer-ID des Akteurs (Surrogat, keine Personendaten); `NULL` bei API-Schlüsseln, deren ID in `actor_id` steht |
| `collector_id`, `observed_at` | Collector und Beobachtungszeitpunkt einer Discovery-Änderung |
| `source` | Quelle wie in `audit_log` (CHECK `chk_ci_change_source`: `ui`, `api`, `collector`, `import`, `system`) |
| `request_id`, `correlation_id` | Request- und Correlation-ID der Anfrage (AUD-07) |

Die Schreibpfade des CI-Repositorys (`recordCIChange`, `recordCreates` in
`backend/internal/ci`) setzen `change_kind`, `change_set_id`,
`actor_user_id`, `source`, `request_id` und `correlation_id`;
`collector_id` und `observed_at` setzt der Ingest-Pfad (WP-489).

Der Typwechsel heißt `type_changed` (CI-11, `chk_ci_change_type`); die
Migration benennt bestehende `type_change`-Zeilen um und leitet
`change_kind` der Bestandszeilen aus `change_type`, Feld und Werten ab
(Funktion `ci_change_kind`; Zeilen desselben CI mit gleichem `created_at`
gelten als ein Schreibvorgang). Der Trigger `ci_change_kind` (`BEFORE INSERT
OR UPDATE`) setzt `change_kind` für Schreiber, die es nicht setzen (ipam,
rack, override), und speichert `type_change` als `type_changed`, auch wenn
der Unmerge Zeilen aus einem älteren Merge-Snapshot zurückschreibt. Die
Policies und `ENABLE` + `FORCE ROW LEVEL SECURITY` sind unverändert.

Migration 000210 (WP-253) setzt `chk_ci_change_type` neu: die Liste aus
000167 (`create`, `update`, `delete`, `status_change`,
`relationship_change`, `attribute_change`, `type_changed`,
`lifecycle_transition`, `location_change`, `movement`, `override`,
`reconciliation_decision`, `parent_child_change`, `assignment`) plus
`template_migration`. Eine Template-Migration eines CI (Zuordnung
`ci_template` auf eine andere veröffentlichte Version) schreibt genau eine
Zeile `template_migration` mit `field_name = 'attributes'`, der
Attribut-Map vor und nach der Migration und in `comment` JSON mit
`template_id`, `from_version`, `to_version`, `defaults`, `findings` und
`job_id`; `change_kind` ist `updated` in einer eigenen Änderungsgruppe.
`ci_change_kind` und `ci_schema_version()` (000183) bleiben unverändert.
Die Down-Migration scheitert, solange Zeilen `template_migration`
existieren. Keine neue Tabelle, RLS unverändert.

## CI-Snapshots `ci_snapshot` und Merge-Herkunft (AUD-08, WP-312)

Migration 000209 legt `ci_snapshot` an: der Zustand eines CI zu `taken_at`
(`state` mit `fields`, `deleted`, `merged_into_id`, `restore`; Formatversion
`schema_version`; Personendaten nur als Surrogat), `ci_version`, die Kanten
des CI beim Schreiben (`relationships`) und der Anlass `reason` (CHECK
`ci_snapshot_reason_check`: `weekly`, `merge`, `unmerge`, `initial`).
`ci_id` hat keinen Fremdschlüssel (wie `entity_change.entity_id`), damit ein
Snapshot Hard-Delete und Merge überdauert. `client_id` und `site_id` setzt
der Trigger `ci_snapshot_scope` (`derive_ci_scope('ci_id')`);
`propagate_ci_scope()` (Fassung 000141) zieht sie bei einem Client- oder
Standortwechsel nach. `ENABLE` + `FORCE ROW LEVEL SECURITY`, die Policy
`ci_snapshot_isolation` mit Org-, Client- und Site-Prädikat, die restriktiven
Policies `ci_snapshot_scoped_update` und `ci_snapshot_scoped_delete`, der
TRUNCATE-Schutz `reticora_truncate_guard` und der Index
`idx_ci_snapshot_ci_taken (organization_id, ci_id, taken_at DESC)`.
`ci_change.origin_ci_id` nennt nach einem Merge das CI, zu dem eine
umgehängte Zeile gehörte (Teilindex `idx_ci_change_origin
(organization_id, origin_ci_id) WHERE origin_ci_id IS NOT NULL`); der
Unmerge schreibt die Spalte aus dem Merge-Snapshot zurück.

## Asset-Snapshots `asset_snapshot` (AUD-08, WP-795)

Migration 000270 legt `asset_snapshot` an: der Zustand eines Assets zu
`taken_at` (`state` mit `fields` und `deleted`; Formatversion
`schema_version`; Personendaten nur als Surrogat) und der Anlass `reason`
(CHECK `asset_snapshot_reason_check`: `weekly`, `initial`). `asset_id` hat
keinen Fremdschlüssel (wie `entity_change.entity_id`), weil Assets hart
gelöscht werden; K19 gilt daher nicht. `client_id` und `site_id` setzt der
Trigger `asset_snapshot_scope` (BEFORE INSERT, Funktion
`asset_snapshot_derive_scope()`, mit erweitertem Scope aus `asset` gelesen;
ein Snapshot eines gelöschten Assets behält den geschriebenen Scope).
`propagate_entity_change_scope()` (Fassung 000250) zieht bei einem Client-
oder Standortwechsel eines Assets auch `asset_snapshot` nach. `ENABLE` +
`FORCE ROW LEVEL SECURITY`, die Policy `asset_snapshot_isolation` mit Org-,
Client- und Site-Prädikat wie `entity_change_isolation`, die restriktiven
Policies `asset_snapshot_scoped_update` und `asset_snapshot_scoped_delete`,
der TRUNCATE-Schutz `reticora_truncate_guard` und der Index
`idx_asset_snapshot_asset_taken (organization_id, asset_id, taken_at DESC)`.

## Schemaversion und Laufzeitschicht der Attribute (MET-16, WP-590)

`ci_type.attribute_schema` ist das materialisierte Schema eines Typs:
`generate_attribute_schema` komponiert die System-Basis
(`attribute_definition` mit `organization_id` und `ci_type_id` `NULL`) mit
den Definitionen des Typs; jede Änderung ergibt eine neue
`ci_type.schema_version` (000105, 000107). `ci.schema_version` nennt die
Version, gegen die ein CI zuletzt geschrieben wurde. Der Trigger
`ci_schema_version` (`BEFORE INSERT OR UPDATE`, Fassung 000183) setzt sie
bei jedem `INSERT` und bei jedem `UPDATE`, das `ci_type_id`, `attributes`
oder eine typisierte Spalte nach CI-01 ändert (`name`, `manufacturer`,
`model`, `serial_number`, `hardware_uuid`, `management_ip`, `primary_mac`,
`hostname`, `fqdn`, `os_name`, `os_version`, `firmware_version`,
`sys_object_id`), auf die aktuelle Version des Typs. Ein `UPDATE`, das nur
andere Felder ändert (Systemfelder wie `last_seen_at`, `first_seen_at`,
`health`, `version`, `updated_at`, `deleted_at`; Zuordnung und Status),
lässt sie unverändert; ein direktes Schreiben von `schema_version` wird
ignoriert.

Org-globale Attributdefinitionen (`organization_id` gesetzt, `ci_type_id`
`NULL`, MET-13) sind nach Entscheidung E-93 (entschieden: Option 2) eine
validierte, nicht materialisierte **Laufzeitschicht**: Der CI-Service
komponiert sie bei jedem Schreibvorgang der Organisation zwischen System-Basis
und Typ (`citype.ResolveFields`) und erzwingt sie (Pflichtdefinition ohne Wert
→ 422). `generate_attribute_schema` nimmt sie nicht auf, ihre Änderung erzeugt
keine neue Typversion, und `ci.schema_version` referenziert nur System-Basis
und Typ. Systemtypen sind organisationsübergreifend materialisiert und tragen
deshalb keine Schicht einer einzelnen Organisation. Eine Org-Schemarevision je
(Organisation, Typ) (Option 1) wäre ein eigenes WP mit Migration.

## Attributdefinitionen (DB-03, MET-10, MET-16, WP-589)

Nach Entscheidung E-92 (`docs/plan/g1-korrektur/entscheidungen.md`,
entschieden: Option 2) ist `attribute_definition` die einzige Quelle der
System-, Organisations- und Typdefinitionen (DB-03); `ci_type.attribute_schema`
wird daraus erzeugt. Die Definitionen der Instanzattribute bleiben in
`ci_instance_field_definition` (MET-14 [P2], WP-256), der letzten Schicht der
Komposition System → Organisation → Typ → Instanz (MET-16, `ResolveFields` in
`backend/internal/citype/resolver.go`). Weitere Tabellen mit
Attributdefinitionen gibt es nicht (`TestResolverLayerOrder`). Entscheidet
der Auftraggeber Option 1, übernimmt ein Folge-WP zusammen mit WP-256 die
Instanzdefinitionen.

`attribute_definition.validation` speichert seit WP-589 die Regel als AST nach
MET-45 (`api/rules.schema.json`); `backend/internal/rules` wertet sie bei jedem
CI-Schreibvorgang verbindlich aus. Die frühere Struktur (`min`, `max`,
`min_length`, `max_length`, `pattern`, `patterns`, `options`) bleibt in
Bestandszeilen stehen (keine Datenmigration) und wird beim Lesen in den AST
übersetzt. Für die Komposition des materialisierten Schemas übersetzt die
Funktion `reticora_validation_legacy` (Migration 000184) einen AST in diese
Struktur, wie `fieldmeta.ValidationFromRule`; die Down-Migration schreibt
AST-Zeilen in die frühere Struktur zurück.

Attribute Sets (MET-16, WP-250, Migration 000199): `attribute_set` hält die
Sets (`organization_id` `NULL` = schreibgeschütztes Systemset,
`attribute_set_org_or_global`), ihre Attribute sind Zeilen von
`attribute_definition` mit `scope = 'set'` und `attribute_set_id` (eindeutig
je Set und Schlüssel, Organisation aus dem Set). `scope` trennt die Ebenen:
`global` ohne Typ und Set, `type` mit Typ, `set` mit Set
(`attribute_definition_scope_type_check`). System-Basis und org-globale Ebene
sind nur `scope = 'global'` (`attribute_definition_global_key`,
`attribute_definition_system_global_key`, `reticora_chain_attribute_schema`,
`attribute_definition_regenerate`); Set-Attribute sind keine globalen
Attribute. `ci_type_attribute_set`
ordnet einem Typ der Organisation Sets geordnet zu (`position`); der Trigger
`ci_type_attribute_set_binding` übernimmt die Organisation des Typs und weist
ein unter RLS unsichtbares Set oder ein Set einer anderen Organisation ab
(23503 unter `ci_type_attribute_set_attribute_set_id_fkey`).

Sets in der Komposition (MET-16, WP-709, Migration 000200):
`reticora_chain_attribute_schema` (aufgerufen von `generate_attribute_schema`
und `ci_type_schema_guard`) komponiert System-Basis → Typkette → aktive Sets
der Typkette: je Typ der Kette von Root nach Blatt seine Sets in
`position`-Reihenfolge, aus Sets eines Vorfahren nur Set-Attribute mit
`inheritable`. Neu erzeugt werden die zugeordneten Typen samt Untertypen bei
Änderungen an Set-Attributen (`attribute_definition_regenerate`, Hilfsfunktion
`regenerate_attribute_set_types`), an `attribute_set.is_active`
(`trg_attribute_set_regenerate`) und an `ci_type_attribute_set` (verzögerter
Constraint-Trigger `trg_ci_type_attribute_set_regenerate` am Ende der
Transaktion, damit ein PUT der Liste `schema_version` je Typ höchstens einmal
erhöht). Org-globale Attribute bleiben Laufzeitebene (E-93, E-108).

Anwendbarkeit globaler Attribute (MET-13, WP-255, Migration 000203):
`attribute_definition.applies_to` (`all` | `categories` | `types`, Standard
`all`, `attribute_definition_applies_to_check`) mit `applies_to_categories`
(`TEXT[]`, Werte wie `ci_type.category`) und `applies_to_type_ids` (`UUID[]`,
Untertypen eingeschlossen; kein Fremdschlüssel, der Server prüft die IDs beim
Schreiben). Einschränkbar sind nur Org-Globale
(`attribute_definition_applies_to_scope_check`: `scope = 'global'` und
`organization_id` gesetzt); die Liste des Modus ist nicht leer, die andere leer
(`attribute_definition_applies_to_list_check`). Bestehende Zeilen stehen auf
`all`. Materialisierung, Trigger, Indizes und RLS-Policies bleiben
unverändert: Die Anwendbarkeit wertet die Laufzeitkomposition aus
(`ResolveFields`, Kompositionsprüfung), Index und Eindeutigkeit bleiben
organisationsweit.

Berechnete Attribute (MET-10 computed, WP-258, Migration 000211):
`attribute_definition.computed` (`JSONB`, `NULL` = erfasstes Attribut) hält
den Ausdruck nach `$defs/expr` aus `api/rules.schema.json`
(`rules.ParseExpr`). `attribute_definition_computed_check`: Ein berechnetes
Attribut ist weder `required` noch `is_unique` und hat keinen
`default_value`. `compose_attribute_definition` behält den Ausdruck einer
Ebene; abweichende Ausdrücke (`computed-conflict`) sowie `required`, Default
oder `is_unique` einer anderen Ebene (`computed-constraint`) sind
Kompositionsfehler unter `attribute_definition_composition`.
`attribute_json_schema` schreibt für berechnete Attribute `readOnly: true`
und `x-computed`; Schemata ohne berechnete Attribute und ihre
`schema_version` bleiben unverändert. Trigger, Indizes und RLS-Policies
bleiben unverändert; die Werte berechnet der Schreibpfad (WP-717).

Lifecycle-Modelle (MET-01, MET-06, LCY-02, WP-263, Migration 000201): Die
Tabellen `lifecycle_definition`, `lifecycle_state` und `lifecycle_transition`
behalten ihre Namen. `ci_type.lifecycle_model_id` (vorher
`lifecycle_definition_id`) verweist über `ci_type_lifecycle_model_fk` ohne
ON DELETE-Aktion auf das Modell; ein gebundenes Modell lässt sich nicht
löschen, die Kaskade einer Organisationslöschung läuft durch.
`lifecycle_definition.applies_to` ist nur `asset` oder `ci`
(`lifecycle_definition_applies_to_check`), `version` (Standard 1) dient If-Match.
`lifecycle_model_effective(ci_type_id, applies_to)` (STABLE, SECURITY
INVOKER) liefert das blattnächste gesetzte Modell der Typkette
(`ci_type_ancestors`) mit passendem `applies_to`; `ci_check_lifecycle_model`
prüft `ci.lifecycle_state` gegen das wirksame `ci`-Modell. System-Seeds:
`physical_asset` (`…0101`, `asset`) und `software` (`…0102`, `ci`, gebunden an
die Systemtypen `application` und `software`), Zustände und Übergänge mit
`organization_id` `NULL`.

Templates (MET-16, MET-53, WP-251, Migration 000205): `template` hält
versionierte, geordnete Attributbündel je CI-Typ (`organization_id` `NULL` =
schreibgeschütztes Systemtemplate, `template_org_or_global`; Schlüssel über die
partiellen Unique-Indizes `template_org_key` und `template_system_key`,
`current_version` = zuletzt veröffentlichte Version, `cloned_from_id`).
`template_version` (Status `draft`, `published`, `retired`, eindeutig je
`template_id` und `version`, `published_by` ohne Fremdschlüssel) und
`template_version_item` (Verweis auf `attribute_definition`, eindeutig je
Version und Definition sowie je Version und `position`, Overrides
`required_override`, `default_override`, `validation_override`) übernehmen die
Organisation des Templates (`template_version_set_org`,
`template_version_item_binding`). `template_binding` und
`template_version_item_binding` weisen einen unter RLS unsichtbaren oder
fremden CI-Typ, eine fremde Klonquelle und eine fremde Attributdefinition mit
23503 ab. Seed: neun Systemtemplates der Collector-Profile (`physical_server`,
`switch`, `router`, `firewall`, `access_point`, `hypervisor`, `storage`, `ups`,
`pdu`) mit veröffentlichter Version 1 ohne Items (E-37). Das CI hat keine
Template-Spalten; die Zuordnung folgt mit `ci_template` (WP-710).

Template-Zuordnung (MET-16, API-06, WP-710, Migration 000206): `ci_template`
hält die Templates eines CI in definierter Reihenfolge (Primärschlüssel
`ci_id`, `template_id`; `ci_template_ci_id_position_key` eindeutig je CI und
`position`) mit der verwendeten veröffentlichten `version`. Das CI wird über
`ci_template_ci_fkey` (`ci_id`, `organization_id`) → `ci` (`id`,
`organization_id`) mit ON DELETE CASCADE referenziert (K19); Template und
Version über `ci_template_template_id_fkey` und `ci_template_version_fkey`
(`template_id`, `version`) → `template_version` ohne ON DELETE-Aktion, ein
zugeordnetes Template lässt sich also nicht löschen (23503). Die
Zuordnungsregeln (veröffentlichte Version eines aktiven Templates der
Organisation oder eines Systemtemplates, Typ des CI oder seiner Typkette)
prüft die Anwendung unter RLS. `client_id` und `site_id` setzt
`derive_ci_scope('ci_id')` (`ci_template_scope`); `ci_template_scope_propagation`
(AFTER UPDATE auf `ci` bei Wechsel von `client_id` oder `site_id`) leitet sie
über `propagate_ci_template_scope()` mit erweitertem Scope
(`app_scope_widen`/`app_scope_restore`) neu ab. Policies wie
`ci_field_value` (Organisation, Client und Site; restriktive Update- und
Delete-Policies), TRUNCATE-Schutz `reticora_truncate_guard`. Das wirksame
Schema eines CI ergibt sich aus `ci.schema_version` und den referenzierten
`ci_template.version` (E-108).

Regelkatalog (MET-45, API-06, AUD-07, WP-716, Migration 000207):
`metamodel_rule` hält benannte Regeln für das Attribut `attribute_key` genau
eines Ziels (`metamodel_rule_one_target`: `ci_type_id`, `attribute_set_id` oder
`template_version_id`, je ON DELETE CASCADE und eigener Index; Template-Regeln
binden an die Version, E-111). `kind` ist `validation`, `required`,
`readonly` oder `visible`; `ast` ist genau bei `validation` gesetzt
(`metamodel_rule_ast_check`), `condition` NULL gilt immer, `message` prüft
`reticora_i18n_text_valid`, `version` beginnt bei 1. `organization_id` `NULL`
ist eine schreibgeschützte Systemregel. Schlüssel: `metamodel_rule_org_key` und
`metamodel_rule_system_key` für Typ- und Set-Regeln,
`metamodel_rule_template_version_key` je Template-Version.
`metamodel_rule_binding` weist ein unter RLS unsichtbares oder fremdes Ziel mit
23503 ab. Der Trigger `metamodel_rule_copy_template_version` (AFTER INSERT auf
`template_version`) kopiert die Regeln der Vorversion mit ihren Schlüsseln in
eine neue Template-Version.

Formular-Layouts (MET-50, API-06, AUD-07, WP-259, Migration 000212):
`form_layout` hält das Layout eines Formulars für genau ein Ziel
(`form_layout_one_target`: `ci_type_id` oder `template_id`, je ON DELETE
CASCADE und eigener Index; Template-Layouts gelten für die Template-ID
unabhängig von der Version, E-111), optional gebunden an eine Rolle
(`role_id` aus `role` oder `custom_role`, kein Fremdschlüssel, Index
`idx_form_layout_role`). `layout` (`jsonb`) hält die Abschnitte, `priority`,
`is_active` und `version` (beginnt bei 1). Es gibt keine globalen Zeilen
(`organization_id` NOT NULL). Schlüssel: `form_layout_org_key`
(`organization_id`, `key`); je Ziel und Rolle höchstens ein Layout über die
Teil-Unique-Indizes `form_layout_type_role` und `form_layout_template_role`
(NULLS NOT DISTINCT). Die AFTER-DELETE-Trigger `trg_role_form_layout` und
`trg_custom_role_form_layout` (Funktion `form_layout_delete_role`) entfernen
die Layouts einer gelöschten Rolle unter RLS. RLS mit ENABLE/FORCE und der
Policy `form_layout_isolation` (FOR ALL, `app.org_id`), TRUNCATE-Schutz
`reticora_truncate_guard`.

Metamodell-Entwürfe (MET-53, API-06, WP-260, Migration 000215):
`metamodel_draft` hält den unveröffentlichten Arbeitsstand von
Metamodelländerungen einer Organisation. `items` (`jsonb`, Standard `[]`) ist
die Liste typisierter Operationen mit Item-ID und Ausgangsstand `base` je
Item; eine Spalte für den Ausgangsstand des Drafts gibt es nicht. `status`
ist `draft`, `tested`, `published` oder `discarded`
(`metamodel_draft_status_check`); `published_at` ist genau bei `published`
gesetzt (`metamodel_draft_published_at_check`). `created_by` ist ein Surrogat
ohne Fremdschlüssel wie `job.created_by`, `test_report` (`jsonb`) ist
optional, `version` beginnt bei 1 und steigt mit jeder Änderung. Index
`idx_metamodel_draft_org_status` (`organization_id`, `status`, `created_at`
DESC), Trigger `trg_metamodel_draft_updated_at`. RLS mit ENABLE/FORCE und der
Policy `metamodel_draft_isolation` (FOR ALL, `app.org_id`), TRUNCATE-Schutz
`reticora_truncate_guard`.

Import-Mapping-Profile (IMP-IO-01, API-06, WP-774, Migration 000219):
`import_profile` speichert die Mapping-Profile des Dateiimports je
Organisation. `entity` ist `ci`, `asset`, `contract`, `consumable`,
`location`, `relationship`, `subnet` oder `ip_address`
(`import_profile_entity_check`; `subnet` und `ip_address` seit WP-920,
Migration 000343_import_profile_ipam, vorläufige Nummer), `format`
`csv`, `xlsx` oder `json` (`import_profile_format_check`), `mode`
`create_missing`, `update_existing` oder `both` (`import_profile_mode_check`).
`mapping` (`jsonb`) muss ein Objekt sein (`import_profile_mapping_check`; je
Quellspalte Ziel und Transformationen, Defaults je Ziel, Struktur prüft die
Anwendung), `identity_keys` (`text[]`, Standard `{}`), `description` und
`sheet` sind optional. Der Name ist je Organisation eindeutig
(`import_profile_organization_id_name_key`). `created_by` ist ein Surrogat
ohne Fremdschlüssel wie `job.created_by`; die Tabelle verweist weder auf
Client- noch auf Objekttabellen. Trigger `trg_import_profile_updated_at`. RLS
mit ENABLE/FORCE und der Policy `import_profile_isolation` (FOR ALL,
`app.org_id`), TRUNCATE-Schutz `reticora_truncate_guard`.

IP-Reservierungen (NET-09, WP-299, Migration 000208): `ip_address` erhält
`reserved_until` (`timestamptz`, informativ, keine automatische Freigabe) und
`reserved_by` (`uuid`, Surrogat des reservierenden Nutzers ohne Fremdschlüssel,
Konvention 16 wie `ci_change.actor_id`). Eine Reservierung ist eine Zeile mit
`type = 'reserved'`, `subnet_id` und ohne `interface_id`; VRF, Client und
Standort setzen `trg_ip_address_vrf` und `ip_address_scope`. RLS, Policies und
Indizes von `ip_address` bleiben unverändert.

Host-Adresse (NET-06, WP-300, Migration 000213): `ip_address` erhält die
gespeicherte Spalte `address_host` (`text GENERATED ALWAYS AS (host(address))
STORED`) und den Index `idx_ip_address_org_vrf_host` auf `(organization_id,
vrf_id, address_host)`. Die Duplikatsuche vergleicht damit die Host-Adresse
ohne Präfixlänge mit der leakproof Textgleichheit, auch unter RLS; ein
Ausdrucksindex über `host(address)` wäre als `reticora_app` nicht nutzbar.
RLS und Policies von `ip_address` bleiben unverändert.

Ticket-Zustände und Nummer je Organisation (TKT-01, WP-290, Migration 000214):
`ticket_status_check` erlaubt zusätzlich `new`, der Default von `status` ist
`new`; die Übergänge prüft die Anwendung (`backend/internal/ticket/states.go`).
`ticket_number` hat keinen Default mehr (die Sequenz
`ticket_ticket_number_seq` bleibt für die Down-Migration) und ist über
`ticket_organization_number_key` (`organization_id`, `ticket_number`)
eindeutig; der nicht eindeutige Index `idx_ticket_number` entfällt. Der
BEFORE-INSERT-Trigger `ticket_number_assign` (Funktion
`assign_ticket_number`) vergibt die Nummer aus `ticket_number_counter`
(`organization_id` PRIMARY KEY, ON DELETE CASCADE, `last_number`) per
`INSERT … ON CONFLICT (organization_id) DO UPDATE … RETURNING`; eine
mitgegebene Nummer bleibt und hebt den Zähler höchstens an. Der Zähler
startete je Organisation bei `max(ticket_number)`. RLS mit ENABLE/FORCE und
der Policy `ticket_number_counter_isolation` (FOR ALL, nur `app.org_id`, damit
gescopte Prinzipale denselben Zähler nutzen), TRUNCATE-Schutz
`reticora_truncate_guard`.

Business-Kalender (TKT-02, LOC-01/02, WP-292, Migration 000216):
`business_calendar` hält die Arbeitszeiten einer Organisation oder eines
Clients (`client_id` NULL ist org-weit): `weekly_hours` (`jsonb`, Standard
Mo–Fr 08:00–18:00, `business_calendar_weekly_hours_check`: Array) als Fenster
`{weekday 1=Mo…7=So, start, end}` in lokaler Wandzeit, `holidays` (`jsonb`,
Standard `[]`, `business_calendar_holidays_check`: Array) als
`{date, name}`, optional `timezone` (`business_calendar_timezone_check` mit
`reticora_is_iana_timezone`), `is_default` (Standard false). Die Inhalte prüft
die Anwendung (`sla.Calendar.Validate`). Schlüssel
`business_calendar_id_organization_key` (`id`, `organization_id`); der
Client-Verweis `business_calendar_client_id_fkey` trägt die Organisation
(`client_id`, `organization_id`, ON DELETE CASCADE, K19). Indizes
`idx_business_calendar_client` (`organization_id`, `client_id`) und der
Teil-Unique-Index `business_calendar_default` (`organization_id`,
`client_id`) NULLS NOT DISTINCT WHERE `is_default`: ein Default je
Organisation und Client. Trigger `trg_business_calendar_updated_at`. RLS mit
ENABLE/FORCE: `business_calendar_isolation` (FOR ALL; USING lässt org-weite
Kalender für Client-gescopte Prinzipale zu, WITH CHECK nur Zeilen im
Client-Scope, E-09) und die RESTRICTIVE-Policies
`business_calendar_scoped_update` und `business_calendar_scoped_delete`,
TRUNCATE-Schutz `reticora_truncate_guard`. `sla` erhält `calendar_id`
(`sla_calendar_id_fkey` auf (`calendar_id`, `organization_id`), ON DELETE SET
NULL (`calendar_id`), Index `idx_sla_calendar`). `ticket_sla` erhält
`calendar_id` (`ticket_sla_calendar_id_fkey` wie bei `sla`, Index
`idx_ticket_sla_calendar`), `sla_explicit` (Standard false),
`response_breach_notified_at` und `resolution_breach_notified_at` mit den
Teilindizes `idx_ticket_sla_response_open` (`organization_id`,
`response_due_at`) WHERE `first_response_at` und
`response_breach_notified_at` NULL sowie `idx_ticket_sla_resolution_open`
(`organization_id`, `resolution_due_at`) WHERE `resolved_at` und
`resolution_breach_notified_at` NULL (Lauf aus WP-769).

Tag-Katalog und Tag-Zuordnung (CI-07, CI-08, CI-12, WP-281, E-125, Migration
000218): `tag` ist der Katalog einer Organisation (`organization_id` NOT NULL,
ON DELETE CASCADE): `key` (`tag_key_check`: `^[a-z0-9][a-z0-9_-]{0,62}$`),
`display_name` (`jsonb`, `tag_display_name_i18n` wie
`ci_type_display_name_i18n`: nur `de`/`en`), optional `color`
(`tag_color_check`: `^#[0-9a-f]{6}$`), `is_system` (Standard false),
`created_at`, `updated_at` (Trigger `trg_tag_updated_at`). Schlüssel
`tag_organization_key_key` (`organization_id`, `key`) und
`tag_id_organization_key` (`id`, `organization_id`); entitätsspezifische
Spalten gibt es nicht. RLS mit ENABLE/FORCE und der Policy `tag_isolation`
(FOR ALL, `app.org_id`), TRUNCATE-Schutz `reticora_truncate_guard`. Den
System-Tag `archived` (de „Archiviert“, en „Archived“, `is_system`) legt die
Migration für jede Organisation an, für neue Organisationen der Trigger
`trg_organization_default_tags` (AFTER INSERT auf `organization`, Funktion
`organization_default_tags`, setzt `app.org_id` wie
`organization_default_vrf`). `tag_normalize_key(text)` ist die Key-Regel
(Kleinschreibung, andere Zeichen als `a-z`, `0-9`, `_`, `-` werden `_`,
führende `_`/`-` entfallen, höchstens 63 Zeichen; `ci.TagKey`). `ci_tag`
ordnet Tags CIs zu (`id` PRIMARY KEY, `organization_id`, `ci_id`, `tag_id`,
`client_id`, `site_id`, `created_by` ohne Fremdschlüssel, `created_at`):
`ci_tag_ci_fkey` (`ci_id`, `organization_id`) → `ci` (`id`,
`organization_id`) ON DELETE CASCADE (K19), `ci_tag_tag_fkey` (`tag_id`,
`organization_id`) → `tag` (`id`, `organization_id`) ohne ON-DELETE-Aktion
(zugeordnete Tags lassen sich nicht löschen, die Organisation schon),
`ci_tag_ci_id_tag_id_key` (`ci_id`, `tag_id`), Index `idx_ci_tag_org_tag`
(`organization_id`, `tag_id`). `client_id` und `site_id` setzt
`derive_ci_scope('ci_id')` (`ci_tag_scope`); `propagate_ci_scope()` (Fassung
aus 000209) leitet sie bei Client- oder Site-Wechsel des CI neu ab. Policies
wie `ci_template` (`ci_tag_isolation`, `ci_tag_scoped_update`,
`ci_tag_scoped_delete`), TRUNCATE-Schutz `reticora_truncate_guard`.
`ci_field_value_no_tags` (`field_name <> 'tags'`) schließt `tags` als
Feldwert aus. Die Datenmigration übernimmt den wirksamen Wert jedes CI
(Override aus `ci_field_value`, sonst `ci.attributes.tags`, nur
String-Elemente) nach `ci_tag`, entfernt `attributes.tags` und die Feldwerte
`tags` ohne Änderung von `version`, `schema_version` und `updated_at`, schließt
offene `override_conflict`-Reviews zu `tags`, entfernt `tags` aus offenen
Merge-Snapshots und normalisiert `saved_view.filter_spec.tags` von CI-Sichten.

Standardkunde (UI-15, TEN-01, WP-854, Migration 000282): Jede Organisation
erhält beim Anlegen einen Kunden (`client`) mit `slug` `default` und dem
Namen der Organisation über den Trigger `trg_organization_default_client`
(AFTER INSERT auf `organization`, Funktion `organization_default_client`,
fester `search_path`; setzt `app.org_id` wie `organization_default_tags` und
erweitert den Client- und Site-Scope über `app_scope_widen`, ON CONFLICT DO
NOTHING). Der Backfill gibt jeder Organisation ohne Kunden diesen Kunden,
auch `00000000-0000-0000-0000-000000000001` („Reticora Demo“);
Organisationen mit Kunden erhalten keinen weiteren. `client` behält ENABLE +
FORCE RLS und `client_isolation`. Die Down-Migration entfernt Trigger und
Funktion; die angelegten Kunden bleiben.

Ausgeblendete CI-Typen je Kunde (MET-05, WP-858, Migration 000283):
`client_ci_type_hidden` (`organization_id`, `client_id`, `ci_type_id`,
`created_at`, `created_by`) mit dem Primärschlüssel (`organization_id`,
`client_id`, `ci_type_id`), `client_ci_type_hidden_client_id_fkey`
(`client_id`, `organization_id`) → `client` ON DELETE CASCADE,
`ci_type_id` → `ci_type` ON DELETE CASCADE und dem Index
`idx_client_ci_type_hidden_type` (`ci_type_id`). Der Trigger
`client_ci_type_hidden_type` (BEFORE INSERT OR UPDATE OF `ci_type_id`,
`organization_id`, fester `search_path`) lässt nur einen Systemtyp oder einen
Typ derselben Organisation zu (23503). ENABLE + FORCE RLS mit
`client_ci_type_hidden_isolation` (Organisation und `app.client_scope`;
`client_id` ist NOT NULL, USING gleich WITH CHECK) und TRUNCATE-Schutz. Ein
ausgeblendeter Typ bleibt aktiv: `GET /api/v1/ci-types` lässt ihn für
`client_id` (oder für einen Principal, dessen Kunden ihn alle ausblenden)
weg, die Neuanlage durch eine Person und der manuelle Typwechsel für CIs des
Kunden antworten 422; bestehende CIs, Discovery und Ingest bleiben
unberührt. Die Down-Migration entfernt Tabelle und Funktion.

Lieferanten (MGT-12, API-06, WP-736, E-41, Migration 000223): `supplier` hält
die Stammdaten eines Lieferanten der Organisation oder eines Clients
(`client_id` NULL ist org-weit): `name` (Firmenname, keine PII,
`supplier_name_check`: nicht leer), optional `external_ref`, `contact_id` und
`notes`, `version` (Standard 1, ETag), `created_at`, `updated_at` (Trigger
`trg_supplier_updated_at`). Die Kontaktperson steht nur über `contact_id`
(PII im Vault, WP-242). Schlüssel `supplier_organization_id_name_key`
(`organization_id`, `name`) und `supplier_id_organization_key` (`id`,
`organization_id`) als Ziel für `contract`, `asset` und `internal_order`.
Verweise mit Organisation (K19): `supplier_client_id_fkey` (`client_id`,
`organization_id`) → `client` ON DELETE CASCADE und `supplier_contact_id_fkey`
(`contact_id`, `organization_id`) → `contact` ON DELETE SET NULL
(`contact_id`); dafür erhält `contact` den Schlüssel
`contact_id_organization_key` (`id`, `organization_id`). Indizes
`idx_supplier_client` (`organization_id`, `client_id`),
`idx_supplier_contact` (`organization_id`, `contact_id`) und
`idx_supplier_created` (`organization_id`, `created_at`, `id`). RLS mit
ENABLE/FORCE: `supplier_isolation` (FOR ALL; USING lässt org-weite Lieferanten
für Client-gescopte Prinzipale zu, WITH CHECK nur Zeilen im Client-Scope,
E-09) und die RESTRICTIVE-Policies `supplier_scoped_update` und
`supplier_scoped_delete`, TRUNCATE-Schutz `reticora_truncate_guard`.

Verträge (MGT-12, API-06, WP-269, E-41, E-119, Migration 000227): `contract`
hält die Verträge der Organisation oder eines Clients (`client_id` NULL ist
org-weit): `number` (`contract_number_check`: nicht leer), optional
`supplier_id`, `kind` (`contract_kind_check`: `maintenance`, `support`,
`lease`, `license`, `purchase`, `service`, `other`), `start_date`, `end_date`
(`contract_dates_check`: `end_date >= start_date`), `notice_period_days`
(`contract_notice_period_days_check`: `>= 0`), `value` (`numeric`),
`currency` (NOT NULL, `contract_currency_check`: `^[A-Z]{3}$`; der Service
setzt fehlende Werte aus `organization.currency`), `status` (Standard
`active`, `contract_status_check`: `draft`, `active`, `terminated`, `ended`),
`notes`, `version` (Standard 1, ETag), `created_at`, `updated_at` (Trigger
`trg_contract_updated_at`). Schlüssel `contract_organization_id_number_key`
(`organization_id`, `number`) und `contract_id_organization_key` (`id`,
`organization_id`) als Ziel für `asset.contract_id` (WP-271). Verweise mit
Organisation (K19) ohne CASCADE: `contract_client_id_fkey` (`client_id`,
`organization_id`) → `client` und `contract_supplier_id_fkey`
(`supplier_id`, `organization_id`) → `supplier`. Indizes
`idx_contract_end_date` (`organization_id`, `end_date`),
`idx_contract_supplier` (`supplier_id`, `organization_id`),
`idx_contract_client` (`organization_id`, `client_id`) und
`idx_contract_created` (`organization_id`, `created_at`, `id`). Der
AFTER-DELETE-Trigger `contract_object_grants` ruft
`delete_object_grants('contract')` (000226) und entfernt die
`object_grant`-Zeilen eines gelöschten Vertrags. RLS mit ENABLE/FORCE:
`contract_isolation` (FOR ALL; USING lässt org-weite Verträge für
Client-gescopte Prinzipale zu, WITH CHECK nur Zeilen im Client-Scope, E-09)
und die RESTRICTIVE-Policies `contract_scoped_update` und
`contract_scoped_delete`, TRUNCATE-Schutz `reticora_truncate_guard`.

Verträge als Linkziel von Dokumenten (MGT-12, WP-737, Migration 000238):
`derive_document_link_scope` übernimmt für `entity_type = 'contract'` den
`client_id` des Vertrags (org-weit bei NULL; kein Standort, kein Team). Der
AFTER-UPDATE-Trigger `contract_scope_propagation` (bei geändertem
`client_id`) ruft `propagate_contract_scope()` und leitet die Links neu ab;
der AFTER-DELETE-Trigger `contract_document_links` ruft
`delete_contract_document_links()` und entfernt die Links eines gelöschten
Vertrags in derselben Transaktion (`document.link_count` folgt über
`count_document_links`, die Dokumente bleiben). Beide Funktionen laufen mit
erweitertem Scope (`app_scope_widen`) und festem `search_path`.

Attributschema der Beziehungstypen (REL-07, WP-295, Migration 000232):
`relationship_type` erhält `metadata_schema` (jsonb NOT NULL DEFAULT `'[]'`,
CHECK `relationship_type_metadata_schema_array`: `jsonb_typeof` = `array`),
die Liste der Definitionen für `ci_relationship.attributes`. Systemzeilen
und bestehende Mandantentypen erhalten `[]` über den Default; RLS bleibt
unverändert (Policies `relationship_type_isolation_*` aus 000059).

Objektbezug und Tags von Tickets (TKT-01, WP-764, E-39, E-127, Migration
000230): `ticket` und `review_item` erhalten `ticket_id_organization_key` bzw.
`review_item_id_organization_key` (`id`, `organization_id`) als Ziel der
zusammengesetzten Verweise (K19). `ticket_link` (`id`, `organization_id`,
`ticket_id`, `entity_type`, `ci_id`, `asset_id`, `location_id`,
`contract_id`, `review_item_id`, `client_id`, `site_id`, `team_id`,
`created_at`) nimmt neben dem Primärobjekt (`related_ci_id`,
`related_asset_id`, Scope-Anker unverändert) weitere Objekte auf:
`ticket_link_entity_type_check` (`ci`, `asset`, `location`, `contract`,
`review_item`), `ticket_link_target_check` (genau eine Zielspalte, passend
zu `entity_type`), `ticket_link_ticket_fkey` (`ticket_id`,
`organization_id`) → `ticket` und je Ziel `ticket_link_ci_fkey`,
`ticket_link_asset_fkey`, `ticket_link_location_fkey`,
`ticket_link_contract_fkey`, `ticket_link_review_item_fkey` (Spalte,
`organization_id`) → Ziel (`id`, `organization_id`), alle ON DELETE
CASCADE; je Zielspalte `UNIQUE (ticket_id, <spalte>)`
(`ticket_link_ticket_id_<spalte>_key`) und Teilindex
`idx_ticket_link_<ziel>` (`<spalte>`) WHERE `<spalte>` IS NOT NULL, Index
`idx_ticket_link_ticket` (`ticket_id`, `created_at`, `id`). `ticket_tag`
(`ticket_id`, `tag_id`, `organization_id`, `client_id`, `site_id`,
`team_id`, `created_at`; `ticket_tag_pkey` (`ticket_id`, `tag_id`)) ordnet
Tickets Tags des Katalogs zu: `ticket_tag_ticket_fkey` → `ticket` ON DELETE
CASCADE, `ticket_tag_tag_fkey` → `tag` ohne ON-DELETE-Aktion, Index
`idx_ticket_tag_org_tag` (`organization_id`, `tag_id`). `client_id`,
`site_id` und `team_id` beider Tabellen leiten `derive_ticket_link_scope`
(`ticket_link_scope`) und `derive_ticket_tag_scope` (`ticket_tag_scope`,
BEFORE INSERT OR UPDATE) aus dem Ticket ab; `propagate_ticket_scope()`
(Rumpf aus 000064) zieht beide Tabellen nach. RLS mit ENABLE/FORCE:
`ticket_link_tenant_isolation` bzw. `ticket_tag_tenant_isolation` mit Org-,
Client-, Site- und Team-Prädikat wie `ticket_tenant_isolation` (USING mit
NULL-Ausnahme, WITH CHECK ohne) und die RESTRICTIVE-Policies
`*_scoped_update` und `*_scoped_delete`, TRUNCATE-Schutz
`reticora_truncate_guard`. Die Datenmigration übernimmt `ticket.tags` je
Organisation in `tag` (Schlüssel nach `tag_normalize_key`, leere Schlüssel
entfallen, `display_name` de/en = Originalwert, bestehende Schlüssel
bleiben) und `ticket_tag`; danach entfällt die Spalte `ticket.tags`.

Asset-Kern nach AST-01 (WP-271, E-41, E-120, E-124, Migration 000229):
`asset` erhält `manufacturer`, `model`, `cost_center`, `supplier_id`,
`contract_id`, `owner_user_id` und `owner_team_id`. Verweise mit
Organisation (K19): `asset_supplier_id_fkey` (`supplier_id`,
`organization_id`) → `supplier`, `asset_contract_id_fkey` (`contract_id`,
`organization_id`) → `contract`, `asset_owner_user_id_fkey`
(`owner_user_id`, `organization_id`) → `app_user` ohne Löschaktion (E-124;
das Löschen des Nutzers ergibt 409) und `asset_owner_team_id_fkey`
(`owner_team_id`, `organization_id`) → `team` ON DELETE SET NULL
(`owner_team_id`); die Fremdschlüssel sind Rückfallebene, Scope und Client
prüft das Repository. Indizes `idx_asset_supplier`, `idx_asset_contract`,
`idx_asset_owner_user`, `idx_asset_owner_team` (je Spalte mit
`organization_id`, partiell auf NOT NULL). `barcode` ist je Organisation
eindeutig (`idx_asset_barcode_org` auf `organization_id`, `barcode` WHERE
`barcode IS NOT NULL`); Bestandsdubletten behält die älteste Zeile, die
übrigen stehen mit Grund `duplicate_barcode` in `migration_quarantine`.
`currency` hat keinen Standard mehr: der Trigger `asset_default_currency`
(BEFORE INSERT OR UPDATE OF `currency`, Funktion `asset_default_currency()`)
setzt bei NULL oder leerem Wert `organization.currency`. Datenmigration:
Lieferanten aus dem Freitext `supplier` (org-weit, gleichnamige Zeilen
werden wiederverwendet), `lifecycle_state` aus `status` (E-120 (a); ohne
Zustand, z. B. `lost`, als Kopie in `migration_quarantine`) und
`location_id` aus `custom_fields.location_id`.

Asset-Spaltennamen nach AST-01 (WP-738, E-41, E-120, Migration 000231):
`asset.asset_tag` heißt `inventory_no`, `purchase_cost` heißt
`purchase_price` (jetzt NUMERIC(14,2)) und `warranty_end` heißt
`warranty_until`; `NOT NULL` und der Index `idx_asset_tag_org` (Name
unverändert, jetzt auf `organization_id`, `inventory_no`) gehen mit. Die
Altnamen bestehen als abgekündigte Spiegelspalten `asset_tag` (TEXT),
`purchase_cost` (NUMERIC(14,2)) und `warranty_end` (DATE); der Trigger
`asset_mirror_legacy_columns` (BEFORE INSERT OR UPDATE, Funktion
`asset_mirror_legacy_columns()`) hält beide Seiten gleich und setzt
`supplier` auf `supplier.name` des gesetzten `supplier_id`;
`supplier_name_asset_mirror` (AFTER UPDATE OF `name` ON `supplier`) zieht
den Spiegel nach einer Umbenennung nach. `asset_status_from_lifecycle`
(BEFORE INSERT OR UPDATE OF `lifecycle_state`) leitet `status` aus
`lifecycle_state` ab (`deployed` → `assigned`, `repair` → `maintenance`,
`retired`, `disposed`, sonst `in_stock`; bei NULL unverändert).
Datenmigration: `saved_view.filter_spec` (`where`, `sort`, `columns`) und
`attribute_definition.computed` verwenden die neuen Namen.

Systemkommentare von Tickets (TKT-01, WP-766, Migration 000234):
`ticket_comment.author_id` ist nullable, dazu kommt `author_type` (TEXT NOT
NULL DEFAULT `user`, `ticket_comment_author_type_check`: `user`, `system`);
`ticket_comment_author_check` verlangt `author_id` genau bei `user`
(`(author_type = 'user') = (author_id IS NOT NULL)`). Bestandskommentare
erhalten `user` über den Default. Der Fremdschlüssel
`ticket_comment_author_id_fkey`, RLS (`ticket_comment_tenant_isolation` und
die scoped Policies) und `derive_ticket_comment_scope` bleiben unverändert.
Die Down-Migration löscht die Systemkommentare und stellt NOT NULL wieder
her.

Asset-Version und Änderungsspur (AST-01, API-07, EVT-03, WP-740, Migration
000236): `asset.version` (BIGINT NOT NULL DEFAULT 1) ist die Version hinter
ETag und If-Match. Der Trigger `asset_version_bump` (BEFORE UPDATE, Funktion
`asset_version_bump()`) erhöht sie, sobald sich eine Spalte außer
`updated_at` und `version` ändert (Vergleich `to_jsonb(NEW)` mit
`to_jsonb(OLD)`); ohne Änderung bleibt sie. Er läuft nach
`asset_default_currency`, `asset_mirror_legacy_columns` und
`asset_status_from_lifecycle` (Reihenfolge der Namen), so zählen auch die
Schreiber außerhalb des Asset-Pakets (Bewegung, Rack-Platzierung,
Lifecycle, Inventur, CI-Merge, CI-Löschen/-Wiederherstellen,
Standortübernahme, Hard-Delete-Job). `entity_change.entity_version`
(BIGINT, nullable) trägt die Version, die ein Asset-Schreibvorgang
hinterlassen hat; Index `idx_entity_change_version` auf (`entity_type`,
`entity_id`, `entity_version`). RLS und Policies bleiben unverändert.

CI-Owner (CI-07, CI-01, TEN-05, DB-05, TLC-04, WP-280, E-11, E-124,
Migration 000237): `ci` erhält `owner_user_id` und `owner_team_id` (UUID,
nullable). `ci_owner_user_fkey` (`owner_user_id`, `organization_id`) →
`app_user` (`id`, `organization_id`) ohne Löschaktion (NO ACTION; der
Owner eines CI lässt sich nicht löschen), `ci_owner_team_fkey`
(`owner_team_id`, `organization_id`) → `team` ON DELETE SET NULL
(`owner_team_id`); Teilindizes `idx_ci_owner_user` (`organization_id`,
`owner_user_id`) und `idx_ci_owner_team` (`organization_id`,
`owner_team_id`), je WHERE NOT NULL. Spiegelung nach DB-05: Bestands-CIs
mit `asset_id` übernehmen den Owner des Assets; der Trigger
`trg_ci_asset_owner` (BEFORE INSERT OR UPDATE OF `owner_user_id`,
`owner_team_id`, `asset_id`, Funktion `ci_mirror_asset_owner()`) setzt bei
einem verknüpften CI den Owner des Assets und lehnt einen direkt
geschriebenen, abweichenden Owner mit `ci_owner_from_asset` (23514, COLUMN
nennt das Feld) ab; `trg_asset_propagate_owner` (AFTER UPDATE OF
`owner_user_id`, `owner_team_id` ON `asset`, Funktion
`asset_propagate_owner()`) überträgt einen Owner-Wechsel unter der RLS des
Schreibers auf die verknüpften CIs und lehnt mit 42501
(`asset_owner_ci_scope`) ab, wenn ein CI außerhalb des Scopes abweichen
würde. RLS nach TEN-05: `ci_isolation` und `asset_tenant_isolation` tragen
neben Org-, Client- und Site-Prädikat das Team-Prädikat auf
`owner_team_id` (USING mit NULL-Ausnahme, WITH CHECK ohne), `ci_scoped_*`
und `asset_scoped_*` folgen dem neuen WITH CHECK.

Fremdreferenz von Tickets und Kommentaren (TKT-03 [O], CH17, WP-293, E-47,
Migration 000239): `ticket` und `ticket_comment` erhalten
`external_provider` und `external_ref` (TEXT, nullable, benannt wie
`client.external_ref`). Je Tabelle verlangt
`ticket_external_ref_pair_check` bzw.
`ticket_comment_external_ref_pair_check` beide Spalten gemeinsam oder
keine (23514); die Teil-Unique-Indizes `uq_ticket_org_external_ref` bzw.
`uq_ticket_comment_org_external_ref` (`organization_id`,
`external_provider`, `external_ref`) WHERE `external_ref` IS NOT NULL
machen die Referenz je Organisation und Provider eindeutig (23505, ohne
Ausdruck, K22). RLS und Policies bleiben unverändert.

Interne Bestellungen (STK-05, WP-286, E-41, Migration 000240):
`internal_order_status_check` erlaubt `draft`, `submitted`, `approved`,
`rejected`, `ordered`, `partially_received`, `received`, `closed` und
`cancelled`. `internal_order` erhält `supplier_id` (UUID, nullable) mit
`internal_order_supplier_id_fkey` (`supplier_id`, `organization_id`) →
`supplier` ohne Löschaktion und Teilindex `idx_internal_order_supplier`
(`supplier_id`, `organization_id`) WHERE NOT NULL; die Freitextspalte
`supplier` entfällt nach der Überführung in org-weite Lieferanten
(gleichnamige Zeilen werden wiederverwendet). `currency` hat keinen
Standard mehr (das Repository setzt `organization.currency`).
`internal_order_item` erhält `received_quantity` (`numeric(12,2)`, NOT
NULL, Standard 0, `internal_order_item_received_quantity_check`: 0 bis
`quantity`; Positionen empfangener Bestellungen sind voll empfangen),
`internal_order_item_reference_check` (nicht `consumable_id` und
`asset_id` zugleich; Bestandszeilen mit beiden stehen in
`migration_quarantine`, `asset_id` geleert) und `client_id` (UUID) mit
`internal_order_item_client_id_fkey` (`client_id`, `organization_id`) →
`client` und Index `idx_internal_order_item_client` (`organization_id`,
`client_id`). Den Client leitet `internal_order_item_client` (BEFORE INSERT
OR UPDATE, Funktion `internal_order_item_client()`) aus der Bestellung ab;
`internal_order_client_items` (AFTER UPDATE OF `client_id` ON
`internal_order`, Funktion `internal_order_client_items()`) führt einen
Client-Wechsel der Bestellung nach. RLS: `internal_order_item_isolation`
trägt das Client-Prädikat (USING mit NULL-Ausnahme, WITH CHECK ohne, E-09),
dazu die RESTRICTIVE-Policies `internal_order_item_scoped_update` und
`internal_order_item_scoped_delete`.

Kanonisches Bestandsledger (STK-04, AST-07, WP-744, Migration 000242):
`stock_movement` ersetzt das Consumable-Ledger aus 000045 (umbenannt in
`stock_movement_legacy`, übernommen und gelöscht). Spalten `id`,
`organization_id`, `client_id`, `site_id`, `item_kind`
(`stock_movement_item_kind_check`: `asset`, `consumable`; seit 000256 ohne
`quantity_item`), `asset_id`, `consumable_id`
(`stock_movement_object_check`: genau eine, passend zu `item_kind`; die
Übergangsspalte `quantity_item_id` entfällt mit 000256),
`movement_type` (`stock_movement_movement_type_check`: `receipt`,
`transfer`, `assignment`, `return`, `deployment`, `retrieval`,
`reservation`, `release`, `repair`, `disposal`, `correction`),
`from_location_id`, `to_location_id`, `quantity` (`numeric(12,2)`,
`stock_movement_quantity_check`: NULL bei Assets, sonst > 0), `actor_id`
(ohne FK), `assignee_user_id`, `assignee_team_id`
(`stock_movement_assignee_check`: höchstens einer), `occurred_at`
(Standard `now()`), `reason`, `notes`, `ticket_id`, `order_id`,
`workflow_run_id`, `document_id` (alle vier ohne FK), `signature_hash`
(`text`, leer bis WP-276) und `created_at`. Schlüssel
`stock_movement_id_organization_key` (`id`, `organization_id`). Verweise
mit Organisation (K19), alle ON DELETE RESTRICT:
`stock_movement_asset_id_fkey` → `asset`,
`stock_movement_consumable_id_fkey` → `consumable` (der neue Schlüssel
`quantity_item_id_organization_key` von 000242 fällt mit `quantity_item`
in 000269),
`stock_movement_from_location_id_fkey` und
`stock_movement_to_location_id_fkey` → `location`,
`stock_movement_assignee_user_id_fkey` → `app_user`,
`stock_movement_assignee_team_id_fkey` → `team`; `organization_id` →
`organization` ON DELETE CASCADE. Indizes `idx_stock_movement_org`
(`organization_id`, `occurred_at` DESC), Teilindizes
`idx_stock_movement_asset`, `idx_stock_movement_consumable` (Objekt,
`occurred_at` DESC),
`idx_stock_movement_from_location`, `idx_stock_movement_to_location` und
`idx_stock_movement_type` (`organization_id`, `movement_type`). Trigger
`stock_movement_append_only` (BEFORE UPDATE OR DELETE,
`reject_stock_movement_mutation()`) und `stock_movement_derive_scope`
(BEFORE INSERT, `derive_stock_movement_scope()` unter der RLS des
Schreibers: Client des Objekts, Standort von `to_location_id`, sonst
`from_location_id`, sonst des Objekts). RLS mit ENABLE/FORCE,
TRUNCATE-Schutz `reticora_truncate_guard` und `stock_movement_isolation`
mit Org-, Client- und Site-Prädikat (USING mit NULL-Ausnahme, WITH CHECK
ohne, E-09) sowie die RESTRICTIVE-Policies `stock_movement_scoped_update` und
`stock_movement_scoped_delete` (Muster 000151). Die Zeilen von
`asset_movement`, des alten `stock_movement` und die Historie von
`assignment` sind übernommen; ungültige Zeilen stehen in
`migration_quarantine` (Migration `000242`).

Drop der Alttabelle (AST-07, WP-842, Migration 000246): `asset_movement`
(000055) und die Triggerfunktion `reject_asset_movement_mutation()` sind
entfernt. Die Migration bricht ab, solange eine Zeile von `asset_movement`
weder ID-gleich in `stock_movement` noch in `migration_quarantine`
(Migration `000242`) steht. LOC-11 meldet Bewegungen als Art
`stock_movement`.

Ziel-Bin interner Bestellungen (STK-05, LOC-11, WP-847, Migration 000247):
`internal_order` erhält `target_location_id` (UUID, nullable) mit
`internal_order_target_location_fkey` (`target_location_id`,
`organization_id`) → `location` (Schlüssel
`location_id_organization_key`, 000194) ON DELETE RESTRICT und
Teilindex `idx_internal_order_target_location` (`target_location_id`,
`organization_id`) WHERE NOT NULL. Art `bin` und Client der Bestellung
prüft das Order-Repository; RLS und Policies bleiben unverändert.

Asset-Tags (API-06, WP-755, E-38, Migration 000249): `asset_tag_link`
ordnet Assets Tags des Katalogs zu (`id` PRIMARY KEY, `organization_id`
→ `organization` ON DELETE CASCADE, `asset_id`, `tag_id`, `client_id`,
`site_id`, `created_by` ohne Fremdschlüssel, `created_at`):
`asset_tag_link_asset_fkey` (`asset_id`, `organization_id`) → `asset`
(`id`, `organization_id`) ON DELETE CASCADE (K19),
`asset_tag_link_tag_fkey` (`tag_id`, `organization_id`) → `tag` (`id`,
`organization_id`) ohne ON-DELETE-Aktion (zugeordnete Tags lassen sich
nicht löschen), `asset_tag_link_asset_id_tag_id_key` (`asset_id`,
`tag_id`), Index `idx_asset_tag_link_org_tag` (`organization_id`,
`tag_id`). `client_id` und `site_id` setzt `derive_asset_tag_link_scope`
(`asset_tag_link_scope`, BEFORE INSERT OR UPDATE) aus dem Asset;
`propagate_asset_module_scope()` (Fassung aus 000217 plus
`asset_tag_link`, erweiterter Scope) zieht sie bei Client- oder
Standortwechsel des Assets nach. RLS mit ENABLE/FORCE und den Policies
wie `ci_tag` (`asset_tag_link_isolation`, `asset_tag_link_scoped_update`,
`asset_tag_link_scoped_delete`), TRUNCATE-Schutz
`reticora_truncate_guard`. `asset_version_bump()` (Fassung aus 000241)
erhöht die Version zusätzlich, wenn die Transaktion
`app.asset_version_touch` auf `on` setzt (Tag-Änderung ohne geänderte
Spalte). Die Datenmigration übernimmt die String-Elemente von
`asset.custom_fields.tags` (nur Arrays, Schlüssel nach
`tag_normalize_key`, leere entfallen, fehlende Tags legt sie mit
`display_name` de/en = erstem Originalwert an), entfernt `tags` aus
`custom_fields` ohne Änderung von `updated_at` und `version` und
normalisiert `saved_view.filter_spec.tags` der Sichten mit `entity_kind =
'asset'`.
Asset-Wertehistorie mit Client-Scope (AUD-08, SEC-05, TEN-02, TEN-05,
WP-794, Migration 000250): `entity_change` erhält `client_id` und `site_id`
(UUID, nullable, ohne Fremdschlüssel, denormalisiert wie bei `ci_change`)
und den Index `idx_entity_change_org_entity` (`organization_id`,
`entity_type`, `entity_id`, `created_at`; bei großem Bestand vorab mit
`CREATE INDEX CONCURRENTLY IF NOT EXISTS` baubar, wenn die Spalten vorab per
`ADD COLUMN IF NOT EXISTS` angelegt werden). Der Trigger `asset_history`
(AFTER INSERT OR UPDATE OR DELETE ON `asset`, FOR EACH ROW, Funktion
`asset_history()` mit `asset_history_form(asset)`) schreibt die Spur der
typisierten Spalten für jeden Schreiber in der Form des Asset-DTO
(`ci_id` als `primary_ci_id`, `purchase_price` als Dezimal-String): eine
create-Zeile mit der ganzen Zeile, eine delete-Zeile mit der ganzen
Vorgängerzeile (nicht beim Löschen der Organisation), bei UPDATE je
geänderter Spalte eine Zeile, jeweils mit `actor_id` aus `app.user_id`,
`entity_version` aus `version` und `client_id`/`site_id` aus NEW bzw. OLD.
Ausgenommen sind `custom_fields` (versiegelt vom Asset-Repository),
`created_at`, `updated_at`, `version`, die Spiegelspalten `asset_tag`,
`purchase_cost`, `warranty_end`, `supplier` und `serial_number_norm`;
`lifecycle_state` nur in UPDATE-Zeilen (Zeile `lifecycle_transition` aus
WP-724). `entity_change_derive_scope` (BEFORE INSERT ON `entity_change`)
setzt bei Zeilen von Assets und CIs Client und Standort der Entität (mit
erweitertem Scope gelesen); die Zeile eines gelöschten Assets behält den
geschriebenen Scope. `asset_history_scope_propagation` und
`ci_history_scope_propagation` (AFTER UPDATE bei geändertem `client_id`
oder `site_id`, Funktion `propagate_entity_change_scope()`) ziehen die
Zeilen mit erweitertem Scope nach. Backfill aus Asset bzw. CI, für
gelöschte Assets aus ihrer letzten delete-Zeile. `entity_change_isolation`
trägt das Client- und Site-Prädikat von `asset_tenant_isolation` (000151)
in USING und WITH CHECK, dazu die restriktiven Policies
`entity_change_scoped_update` und `entity_change_scoped_delete`; Zeilen
anderer Typen ohne Scope schreiben nur org-weite Prinzipale (E-09).
Halter-Cache und Fälligkeit (AST-02, E-124, WP-745, Migration 000251):
`asset` erhält `assigned_user_id`, `assigned_team_id` (UUID) und
`assignment_due_at` (TIMESTAMPTZ) mit `asset_assigned_user_id_fkey`
(`assigned_user_id`, `organization_id`) → `app_user` (Schlüssel
`app_user_id_organization_key`, 000177) und `asset_assigned_team_id_fkey`
(`assigned_team_id`, `organization_id`) → `team` (Schlüssel
`team_id_organization_key`, 000150), beide ohne ON-DELETE-Aktion (ein
haltendes Team lässt sich nicht löschen, 409), CHECK `asset_single_holder`
(`num_nonnulls(assigned_user_id, assigned_team_id) <= 1`), CHECK
`asset_holder_due` (`assignment_due_at` nur mit Halter) und Teilindizes
`idx_asset_assigned_user` (`organization_id`, `assigned_user_id`) und
`idx_asset_assigned_team` (`organization_id`, `assigned_team_id`), je WHERE
NOT NULL. `assigned_team_id` ist kein Scope-Merkmal; Policies unverändert.
`asset_history` schreibt die drei Spalten wie `location_id` in die Spur
(kein Spiegel einer anderen Spalte). `stock_movement` erhält `due_at`
(TIMESTAMPTZ) mit CHECK `stock_movement_due_at_assignment` (`due_at IS
NULL OR movement_type = 'assignment'`). Keine Daten (Backfill WP-275).
Backfill des Halter-Caches (AST-02, WP-275, Migration 000254, nur Daten):
je Asset bestimmt die jüngste offene `assignment`-Zeile (`active`/`overdue`)
`assigned_user_id` und `assignment_due_at` (Tagesende von `due_date` in
`organization.timezone`), Zeilen nur mit `ci_id` über `ci.asset_id`, nur
Nutzer derselben Organisation; Down leert die drei Spalten.

Bestand von Mengenartikeln je Bin (STK-01, STK-02, STK-03, API-06, WP-283,
E-40, E-41, Migration 000252): `consumable` erhält `barcode` (TEXT, partieller
UNIQUE-Index `consumable_organization_id_barcode_key` (`organization_id`,
`barcode`) WHERE NOT NULL) und `supplier_id` (UUID) mit
`consumable_supplier_id_fkey` (`supplier_id`, `organization_id`) →
`supplier` (ohne ON-DELETE-Aktion) und Teilindex `idx_consumable_supplier`;
die Spalten `stock_level` und `location` entfallen. `stock_level` (`id`,
`organization_id` → `organization` ON DELETE CASCADE, `client_id` und
`site_id` NOT NULL ohne Fremdschlüssel, `consumable_id`, `location_id`,
`on_hand` und `reserved` `numeric(12,2)` NOT NULL DEFAULT 0 mit
`stock_level_on_hand_check` (`on_hand >= 0`) und
`stock_level_reserved_check` (`reserved >= 0 AND reserved <= on_hand`),
`updated_at`) mit `stock_level_consumable_id_location_id_key`,
`stock_level_consumable_fkey` (`consumable_id`, `organization_id`) →
`consumable` und `stock_level_location_fkey` (`location_id`,
`organization_id`) → `location`, beide ON DELETE RESTRICT (K19), sowie
den Indizes `idx_stock_level_location` (`location_id`) und
`idx_stock_level_org_consumable` (`organization_id`, `consumable_id`).
`stock_level_derive_location` (BEFORE INSERT OR UPDATE OF `location_id`,
`consumable_id`, `client_id`, `site_id`, Funktion
`stock_level_derive_location()` mit erweitertem Scope) setzt Client und
Standort des Bins und lehnt Nicht-Bins (`stock_level_location_bin`) und
Artikel eines anderen Clients (`stock_level_location_client`) mit 23514
ab; `location_refresh_stock_level` (AFTER UPDATE ON `location` bei
geändertem `path`, `site_id` oder `client_id`, Funktion
`location_refresh_stock_level()`) leitet nach; `consumable_client_stock`
(BEFORE UPDATE OF `client_id` ON `consumable`, Funktion
`consumable_client_stock()`) lehnt einen Client ab, der nicht zu den Bins
passt (`consumable_stock_level_client`, 23514). RLS mit ENABLE/FORCE,
TRUNCATE-Schutz `reticora_truncate_guard` und `stock_level_isolation` mit
Org-, Client- und Site-Prädikat (USING = WITH CHECK). Der Altbestand
(`stock_level <> 0`, Gründe `stock_without_bin`/`negative_stock`) und
Freitext-Standorte ohne Bestand (`location_without_bin`) stehen in
`migration_quarantine` (Migration `000252`).

Barcode-Register (INV-03, WP-289, E-26, Migration 000255): `location` erhält
`barcode` (TEXT). `barcode_registry` (`id`, `organization_id` →
`organization` ON DELETE CASCADE, `client_id` und `site_id` (UUID, NULL bei
Assets/Mengenartikeln ohne Client bzw. bei Mengenartikeln ohne Standort),
`code` TEXT NOT NULL, `source` mit `barcode_registry_source_check`
(`barcode`, `inventory_no`), `asset_id`, `consumable_id`, `location_id`) mit
`barcode_registry_organization_id_code_key` UNIQUE (`organization_id`,
`code`), CHECK `barcode_registry_one_object`
(`num_nonnulls(asset_id, consumable_id, location_id) = 1`), CHECK
`barcode_registry_inventory_no_asset` (`source = 'barcode' OR asset_id IS NOT
NULL`), den Fremdschlüsseln `barcode_registry_client_fkey` → `client`,
`barcode_registry_asset_fkey` → `asset`, `barcode_registry_consumable_fkey`
→ `consumable` und `barcode_registry_location_fkey` → `location`, je
(`…_id`, `organization_id`) und ON DELETE CASCADE (K19), sowie den
Teilindizes `idx_barcode_registry_asset`, `idx_barcode_registry_consumable`,
`idx_barcode_registry_location` und `idx_barcode_registry_client` (je WHERE
NOT NULL). Die Zeilentrigger `barcode_registry_sync` (AFTER INSERT OR UPDATE
ON `asset`, `consumable` und `location`, Funktionen
`barcode_registry_sync_asset()`, `barcode_registry_sync_consumable()`,
`barcode_registry_sync_location()` mit erweitertem Scope) bauen die
Registerzeilen eines Objekts neu auf, wenn sich `barcode`, bei Assets auch
`inventory_no`, `client_id` oder `site_id` ändert; gleiche Werte von
`barcode` und `inventory_no` ergeben eine Zeile (`inventory_no`). Eine
Dublette über alle Objektarten ist 23505 auf dem UNIQUE-Constraint. RLS mit
ENABLE/FORCE, TRUNCATE-Schutz `reticora_truncate_guard`,
`barcode_registry_isolation` (Org-, Client- und Site-Prädikat, NULL lesbar
wie `asset_tag_link`) sowie die restriktiven
`barcode_registry_scoped_update` und `barcode_registry_scoped_delete`.
Übernommen werden `asset.inventory_no`, `asset.barcode`,
`consumable.barcode` und der Code eines direkt abgebildeten Knotens aus
`location_node_retired` (`node_type` = `location.kind`, genau ein Knoten mit
Code); belegte Codes stehen mit Grund `barcode_conflict` (Asset und
Mengenartikel mit `barcode` NULL; Knoten) bzw. `barcode_ambiguous` (mehrere
Knoten) in `migration_quarantine` (Migration `000255`).

Übernahme der Mengenartikel (STK-03, API-06, WP-757, E-40, Migration
000256): jede Zeile von `quantity_item` steht ID-gleich in `consumable`
(`id`, `organization_id`, `client_id`, `sku`, `name`, `category`, `unit`,
`min_level`, `notes`, `created_at`, `updated_at`). Bestand > 0 in einem Bin
mit verträglichem Client ergibt eine `stock_level`-Zeile; Bestand ohne Bin
(`stock_without_bin`), in einem Bin eines fremden Clients
(`bin_client_mismatch`), negative Mengen (`negative_stock`, als 0
behandelt), eine mit einem bestehenden `consumable` kollidierende SKU
(`sku_conflict`, SKU NULL) und nicht leere `attributes`
(`attributes_dropped`) stehen mit der ganzen Zeile in `migration_quarantine`
(Migration `000256`). Die Ledger-Zeilen mit `item_kind` `quantity_item` sind
bei ausgesetztem `stock_movement_append_only` auf `consumable_id` und
`item_kind` `consumable` umgeschrieben; `stock_movement.quantity_item_id`
mit Fremdschlüssel und Index ist entfernt, `derive_stock_movement_scope()`
kennt nur noch `asset` und `consumable`. `quantity_item` selbst bleibt
unverändert bis WP-758 (000269).

Zuweisungen als Projektion des Ledgers (AST-02, AST-07, WP-747, E-26,
Migration 000257): Index `idx_stock_movement_holder` auf `stock_movement`
(`organization_id`, `asset_id`, `movement_type`, `occurred_at`). Jede Zeile
von `assignment` ohne Bewegung gleicher ID ist ID-gleich als
`assignment`-Bewegung übernommen (`occurred_at` = `assigned_at`,
`assignee_user_id` = `assigned_to`, `actor_id` = `assigned_by`, `notes`,
`due_at` = Tagesende von `due_date` in `organization.timezone`), geschlossene
Zeilen mit einer `return`-Bewegung mit Halter (`returned`: `returned_at`,
`reason` = `return_condition`; `transferred`: Zeitpunkt der Folgezuweisung,
sonst `updated_at`; `cancelled`: `updated_at`, `reason` `cancelled`).
Best-effort-Bewegungen ohne Halter (unter 60 s) und die Kopien aus 000242
(`notes` `assignment:<id>`) sind bei ausgesetztem
`stock_movement_append_only` durch die vollständige Bewegung ersetzt,
`document_link`-Zeilen (`stock_movement`) umgehängt; mehrere offene
Zuweisungen eines Assets sind bis auf die jüngste (die des Halter-Caches
zuerst) mit `return` (`reason` `migration: superseded`) geschlossen. Zeilen
ohne Asset der Organisation (`assignment_without_asset`) oder mit Nutzer
einer anderen Organisation (`assignment_foreign_user`) stehen ohne Signaturen
in `migration_quarantine` (Migration `000257`) und bleiben in `assignment`.
View `assignment_projection` (`security_invoker = true`, RLS von
`stock_movement` gilt für den Aufrufer): je `assignment`-Bewegung eines
Assets `id`, `organization_id`, `asset_id`, `assigned_to`,
`assigned_team_id`, `assigned_by` (`actor_id`), `assignment_type`
(`transfer` bei gleichzeitiger Rückgabe, sonst `assignment`), `status`
(`transferred`, `returned`, `overdue`, `active`), `assigned_at`, `due_at`,
`due_date`, `returned_at`, `return_condition` (erste `return`-Bewegung
desselben Assets und Halters danach), `notes`, `created_at`, `updated_at`.
Die Tabelle `assignment` bleibt unverändert bis WP-843; Down entfernt View,
Index und Quarantäne-Einträge, die Bewegungen bleiben.
Drop der Tabelle `assignment` (AST-07, WP-843, E-26, E-51, Migration 000266):
`assignment_signature_legacy` (`id`, `organization_id` → `organization` ON
DELETE CASCADE, `movement_id`, `kind` `checkout|return`, `payload` JSONB,
`payload_sha256` (Hex-SHA-256 von `payload::text`), `created_at`) mit
`assignment_signature_legacy_movement_id_fkey` (`movement_id`,
`organization_id`) → `stock_movement` ON DELETE RESTRICT,
`assignment_signature_legacy_movement_kind_key` (`movement_id`, `kind`) und
Index `idx_assignment_signature_legacy_org` hält `checkout_signature` und
`return_signature` unverändert (Unterzeichner als Vault-Surrogat, WP-656) an
der `assignment`-Bewegung gleicher ID; `stock_movement.signature_hash` bleibt
NULL. ENABLE + FORCE RLS, TRUNCATE-Guard, Policy
`assignment_signature_legacy_isolation` (Organisation; bei gesetztem
`app.client_scope` oder `app.site_scope` `EXISTS` auf die sichtbare
Bewegung, USING und WITH CHECK); Trigger `assignment_signature_legacy_guard`
(`guard_assignment_signature_legacy()`) verbietet DELETE und jedes UPDATE
außer `payload := NULL`; `reticora_app` hat kein DELETE-Recht. `document_link`-Zeilen mit `entity_type`
`assignment` hängen an `stock_movement` (gleiche `entity_id`), Links ohne
Bewegung stehen in `migration_quarantine` (`document_link`, `dropped:
assignment without movement`). `derive_document_link_scope()` und
`propagate_asset_module_scope()` haben keinen `assignment`-Zweig mehr. Alle
Zeilen von `assignment` stehen in `migration_quarantine` (`dropped:
projection`, Migration `000266`; ohne Signaturen, wenn das Archiv sie hält,
mit `document_link_ids`), danach DROP TABLE `assignment`. Down baut
`assignment` exakt wieder auf und stellt Zeilen, Signaturen, Links, CHECK und
Funktionen wieder her.
Altsignaturen als Unterschriftsdokumente (AST-02, WP-749, E-51, Migration
000268): `guard_assignment_signature_legacy()` erlaubt zusätzlich, `payload`
einmalig durch die bildfreie Form zu ersetzen: der alte Wert ein Objekt ohne
`document_id`, der neue ein Objekt mit genau `document_id` (UUID),
`signer_surrogate` (UUID oder null), `signed_at` und `method` (Text bis 64
Zeichen oder null); alle übrigen Spalten bleiben. DELETE bleibt verboten,
RLS und Policies unverändert. Den Austausch schreibt der Systemlauf
`signature.migrate`. Down stellt die Funktion aus 000266 wieder her, ersetzte
Werte bleiben.
Gesamtbestand von Mengenartikeln (STK-01, NTF-05, WP-285, E-128, Migration
000259): `consumable` erhält `on_hand_total` (`numeric(12,2)` NOT NULL
DEFAULT 0) mit `consumable_on_hand_total_check` (`on_hand_total >= 0`),
befüllt mit Σ `stock_level.on_hand` je Artikel über alle Bins. Fortgeschrieben
wird die Spalte nur von `consumable.ApplyBooking` unter der Zeilensperre des
Artikels; Tabelle, Indizes und RLS bleiben unverändert. Down entfernt Spalte
und Constraint.
Inventurbereich und Soll-Snapshot (INV-01, API-06, WP-848, Migration
000260): `stocktake` erhält `stocktake_id_organization_key` (`id`,
`organization_id`), den Bereich `location_id` mit
`stocktake_location_fkey` (`location_id`, `organization_id`) → `location`
ON DELETE RESTRICT sowie `client_id` und `site_id` ohne Fremdschlüssel,
Indizes `idx_stocktake_location` und `idx_stocktake_client`.
`stocktake_derive_location` (BEFORE INSERT OR UPDATE OF `location_id`,
`client_id`, `site_id`) setzt Client und Standort des Knotens; ein nicht
sichtbarer Knoten ergibt 23503 auf `stocktake_location_fkey`, ohne Bereich
bleiben beide NULL (Altinventuren). `location_refresh_stocktake` (AFTER
UPDATE ON `location` bei geändertem `site_id`/`client_id`, erweiterter
Scope) leitet nach. `stocktake_item` (`id`, `organization_id`,
`client_id`/`site_id` NOT NULL, `stocktake_id`, `item_kind`
`asset|consumable`, `asset_id`, `consumable_id`, `location_id`,
`expected_quantity`, `counted_quantity`, `classification`
`found|missing|unexpected`, `created_at`) mit `stocktake_item_object_check`,
den Teil-UNIQUE-Indizes `stocktake_item_asset_key` (`stocktake_id`,
`asset_id`) und `stocktake_item_consumable_key` (`stocktake_id`,
`consumable_id`, `location_id`), `stocktake_item_stocktake_fkey` → `stocktake`
ON DELETE CASCADE und `stocktake_item_asset_fkey`,
`stocktake_item_consumable_fkey`, `stocktake_item_location_fkey` (jeweils mit
`organization_id`, ON DELETE RESTRICT); `stocktake_item_derive_scope`
übernimmt Client und Standort der Inventur, `stocktake_refresh_items` folgt
ihrer Änderung. `stock_scan.location_id` (Fundort) mit
`stock_scan_location_fkey` (ON DELETE RESTRICT) und
`idx_stock_scan_location`. RLS: `stocktake_isolation` (Org-, Client- und
Site-Prädikat, NULL lesbar) mit den restriktiven `stocktake_scoped_update`
und `stocktake_scoped_delete`; `stocktake_item` mit ENABLE/FORCE,
TRUNCATE-Schutz `reticora_truncate_guard` und `stocktake_item_isolation`.
Kantenhistorie (REL-09, EVT-03, AUD-07, WP-296, Migration 000261):
`relationship_change` (`id`, `organization_id` → `organization` ON DELETE
CASCADE, `relationship_id` UUID NOT NULL ohne Fremdschlüssel, `source_ci_id`,
`target_ci_id`, `rel_type` TEXT NOT NULL, `change_kind` TEXT NOT NULL,
`changed_fields` TEXT[] NOT NULL DEFAULT `'{}'`, `before` und `after` JSONB,
`actor_id` UUID ohne Fremdschlüssel, `actor_type` und `source` TEXT NOT
NULL, `reason`, `request_id`, `correlation_id` TEXT, `observed_at`
TIMESTAMPTZ NOT NULL, `created_at` DEFAULT now() sowie die Scope-Spalten
`source_client_id`, `source_site_id`, `target_client_id`, `target_site_id`)
mit `relationship_change_source_tenant_fkey` und
`relationship_change_target_tenant_fkey` (je (`…_ci_id`,
`organization_id`) → `ci` (`id`, `organization_id`) ON DELETE CASCADE, K19),
CHECK `chk_relationship_change_kind` (`created`, `updated`,
`verification_changed`, `refreshed`, `removed`, `suppressed`, `rewired`,
`restored`), `chk_relationship_change_source` (`ui`, `api`, `collector`,
`import`, `system`) und `chk_relationship_change_reason` (NULL, `stale`,
`suppressed`, `merge`, `unmerge`, `interface_removed`) sowie den Indizes
`idx_relationship_change_relationship`, `idx_relationship_change_source`
und `idx_relationship_change_target` (`organization_id`, `relationship_id`
bzw. `source_ci_id` bzw. `target_ci_id`, `observed_at` DESC). Trigger
`relationship_change_scope` (BEFORE INSERT OR UPDATE, `derive_edge_scope()`)
setzt den Scope beider Endpunkte; `relationship_change_scope_propagation`
(AFTER UPDATE ON `ci` bei geändertem `client_id` oder `site_id`, Funktion
`propagate_relationship_change_scope()` mit erweitertem Scope) leitet nach.
RLS mit ENABLE/FORCE, TRUNCATE-Schutz `reticora_truncate_guard`,
`relationship_change_isolation` (USING wie `ci_relationship_isolation`,
WITH CHECK mit dem Schreibprädikat aus 000142) und die restriktiven
`relationship_change_scoped_update` und `relationship_change_scoped_delete`.
Digitale Unterschrift an Bewegungen (AST-02, E-51, E-124, WP-276, Migration
000262): `document_category_check` erlaubt neben den acht Werten aus 000011
die Kategorie `signature`. `stock_movement` erhält `signer_user_id` und
`signer_surrogate` (beide `uuid`, ohne Fremdschlüssel wie `actor_id`, damit
Crypto-Shredding möglich bleibt) mit `stock_movement_signer_check`
(`num_nonnulls(signer_user_id, signer_surrogate) <= 1`) und
`stock_movement_signature_check` (`signature_hash` nur mit `document_id` und
genau einem Unterzeichner). Append-only-Trigger, Policies und RLS bleiben
unverändert. Down bricht ab, solange Dokumente der Kategorie `signature` oder
Bewegungen mit `signature_hash` existieren.
Mengenreservierung auf Artikel und Bin (STK-03, E-40, WP-284, Migration
000264): `reservation` erhält `consumable_id` und `location_id` (UUID) mit
`reservation_stock_level_fkey` (`consumable_id`, `location_id`,
`organization_id`) → `stock_level` ON DELETE RESTRICT (K19; Schlüssel
`stock_level_consumable_location_organization_key` neu) und dem Teilindex
`idx_reservation_consumable_bin_active` (`consumable_id`, `location_id`)
WHERE `state = 'active'`; `quantity_item_id` samt
`reservation_quantity_item_id_fkey` entfällt. CHECK
`reservation_item_kind_check` (`asset`, `consumable`) und
`reservation_item_check` (genau Asset ohne Artikel/Bin oder Artikel mit Bin
ohne Asset). `derive_reservation_scope()` (Trigger `reservation_scope`, nun
BEFORE INSERT OR UPDATE OF `organization_id`, `asset_id`, `consumable_id`,
`location_id`, `client_id`, `site_id`) leitet Client und Standort einer
Mengenreservierung aus der `stock_level`-Zeile ab, also aus dem Bin;
`stock_level_reservation_scope_propagation` (AFTER UPDATE ON `stock_level`
bei geändertem `client_id` oder `site_id`, `propagate_reservation_scope()`)
ersetzt `quantity_item_reservation_scope_propagation`.
`derive_search_document_scope()` liest im Zweig `reservation` die Spalten
der Reservierung, `reservation_search_scope_propagation` reagiert auf
`client_id`/`site_id`, `propagate_search_scope()` verliert den Zweig
`quantity_item`. `derive_stock_movement_scope()` gibt den Bewegungen
`reservation` und `release` eines Artikels ohne (sichtbaren) Client den
Client ihres Bins. Datenübernahme: Reservierungen mit `quantity_item_id`
erhalten den einzigen Bin des Artikels, sonst `migration_quarantine`
(`reservation_without_bin`); Überreservierungen je Bin werden von der
jüngsten an freigegeben und kopiert (`over_reservation`);
`stock_level.reserved` = Σ der aktiven Mengen. Down stellt den Stand von
000263 wieder her; Reservierungen von Artikeln ohne `quantity_item`-Zeile
legt es in `migration_quarantine` (`reservation_without_quantity_item`), das
nächste Up holt sie zurück.
Klassifizierte Inventur-Scans (INV-01, TEN-06, WP-287, Migration 000265):
`stock_scan` erhält `consumable_id` mit `stock_scan_consumable_fkey`
(`consumable_id`, `organization_id`) → `consumable` ON DELETE RESTRICT,
`quantity` NUMERIC(12,2) mit `stock_scan_quantity_check` (gesetzt und > 0
genau dann, wenn `consumable_id` gesetzt ist), `code`, `legacy_scan_result`
sowie `client_id` und `site_id` ohne Fremdschlüssel; Indizes
`idx_stock_scan_consumable`, `idx_stock_scan_client` und
`idx_stock_scan_stocktake_scanned` (`stocktake_id`, `scanned_at`, `id`).
`scan_result_check` erlaubt `found`, `missing`, `unexpected`; Altwerte
`surplus`/`wrong_location` wurden `unexpected`, `damaged` wurde `found`, der
Originalwert steht in `legacy_scan_result`. `stock_scan_derive_scope`
(BEFORE INSERT OR UPDATE OF `stocktake_id`, `asset_id`, `consumable_id`,
`client_id`, `site_id`, erweiterter Scope) setzt Client und Standort der
Inventur, ohne Bereich die des Assets, Consumables bzw. CIs;
`stocktake_refresh_scans` (AFTER UPDATE OF `client_id`, `site_id`,
`location_id` ON `stocktake`) leitet nach. RLS: `stock_scan_isolation`
(Org-, Client- und Site-Prädikat, NULL lesbar) mit den restriktiven
`stock_scan_scoped_update` und `stock_scan_scoped_delete`.
Drop von `quantity_item` (STK-03, E-40, E-26, WP-758, Migration 000269):
Das Up bricht ab, solange ein Fremdschlüssel auf `quantity_item` zeigt
(Meldung mit den Constraints), sichert jede Zeile als `row_data` in
`migration_quarantine` (`source_table` `quantity_item`, Migration
`000269`; `quantity_item_archived` mit ID-gleicher `consumable`-Zeile,
sonst `quantity_item_not_merged`) und löscht die Tabelle ohne CASCADE samt
Policies, Triggern (`trg_quantity_item_updated_at`,
`trg_quantity_item_derive_site`, `quantity_item_search_scope_propagation`,
`reticora_truncate_guard`), Index `idx_quantity_item_site` und
Fremdschlüsseln. `location_refresh_asset_site()` aktualisiert nur noch
`asset`. Die Abhängigkeitsart der Standortlöschung heißt `stock_level`
(Bestand eines Artikels im Bin). Down baut die Tabelle im Stand vor 000269
wieder auf, stellt die gesicherten Zeilen unverändert her (ein seither
gelöschter Client, Standort oder Ort wird NULL) und entfernt deren
Einträge.

## Tabellen, RLS und Policies

Spalten: Tabellenname, RLS aktiviert, RLS erzwungen (`FORCE ROW LEVEL
SECURITY`), Policies mit ihrem Kommando.

<!-- schema-baseline:tables:begin -->

| Tabelle | RLS | FORCE RLS | Policies (Kommando) |
|---|---|---|---|
| `admp_access_port` | ja | ja | admp_access_port_isolation (ALL), admp_access_port_scoped_delete (DELETE), admp_access_port_scoped_update (UPDATE) |
| `admp_account` | ja | ja | admp_account_isolation (ALL), admp_account_scoped_delete (DELETE), admp_account_scoped_update (UPDATE) |
| `admp_connection` | ja | ja | admp_connection_isolation (ALL), admp_connection_scoped_delete (DELETE), admp_connection_scoped_update (UPDATE) |
| `admp_service` | ja | ja | admp_service_isolation (ALL), admp_service_scoped_delete (DELETE), admp_service_scoped_update (UPDATE) |
| `admp_service_port` | ja | ja | admp_service_port_isolation (ALL), admp_service_port_scoped_delete (DELETE), admp_service_port_scoped_update (UPDATE) |
| `agent_enrollment_token` | ja | ja | agent_enrollment_token_isolation (ALL), agent_enrollment_token_scoped_delete (DELETE), agent_enrollment_token_scoped_update (UPDATE) |
| `ai_chunk` | ja | ja | ai_chunk_scoped_delete (DELETE), ai_chunk_scoped_update (UPDATE), ai_chunk_tenant_isolation (ALL) |
| `ai_conversation` | ja | ja | ai_conversation_tenant_isolation (ALL) |
| `ai_message` | ja | ja | ai_message_tenant_isolation (ALL) |
| `alarm` | ja | ja | alarm_isolation (ALL), alarm_scoped_delete (DELETE), alarm_scoped_update (UPDATE) |
| `alarm_transition` | ja | ja | alarm_transition_isolation (ALL), alarm_transition_scoped_delete (DELETE), alarm_transition_scoped_update (UPDATE) |
| `alert_rule` | ja | ja | alert_rule_isolation (ALL), alert_rule_system_select (SELECT) |
| `api_key` | ja | ja | api_key_system_select (SELECT), org_isolation (ALL) |
| `app_user` | ja | ja | user_isolation (ALL) |
| `asn` | ja | ja | asn_isolation (ALL), asn_scoped_delete (DELETE), asn_scoped_update (UPDATE) |
| `asn_range` | ja | ja | asn_range_isolation (ALL), asn_range_scoped_delete (DELETE), asn_range_scoped_update (UPDATE) |
| `asset` | ja | ja | asset_scoped_delete (DELETE), asset_scoped_update (UPDATE), asset_tenant_isolation (ALL) |
| `asset_location` | ja | ja | asset_location_isolation (ALL) |
| `asset_snapshot` | ja | ja | asset_snapshot_isolation (ALL), asset_snapshot_scoped_delete (DELETE), asset_snapshot_scoped_update (UPDATE) |
| `asset_tag_link` | ja | ja | asset_tag_link_isolation (ALL), asset_tag_link_scoped_delete (DELETE), asset_tag_link_scoped_update (UPDATE) |
| `assignment_signature_legacy` | ja | ja | assignment_signature_legacy_isolation (ALL) |
| `attribute_definition` | ja | ja | attribute_definition_isolation_delete (DELETE), attribute_definition_isolation_insert (INSERT), attribute_definition_isolation_select (SELECT), attribute_definition_isolation_update (UPDATE) |
| `attribute_set` | ja | ja | attribute_set_isolation_delete (DELETE), attribute_set_isolation_insert (INSERT), attribute_set_isolation_select (SELECT), attribute_set_isolation_update (UPDATE) |
| `audit_log` | ja | ja | audit_isolation (ALL) |
| `audit_route_intent` | ja | ja | audit_route_intent_isolation (ALL), audit_route_intent_system_select (SELECT) |
| `barcode_registry` | ja | ja | barcode_registry_isolation (ALL), barcode_registry_scoped_delete (DELETE), barcode_registry_scoped_update (UPDATE) |
| `bgp_peering` | ja | ja | bgp_peering_isolation (ALL), bgp_peering_scoped_delete (DELETE), bgp_peering_scoped_update (UPDATE) |
| `building` | ja | ja | building_isolation (ALL) |
| `bulk_change_set` | ja | ja | bulk_change_set_isolation (ALL), bulk_change_set_scoped_delete (DELETE), bulk_change_set_scoped_update (UPDATE) |
| `business_calendar` | ja | ja | business_calendar_isolation (ALL), business_calendar_scoped_delete (DELETE), business_calendar_scoped_update (UPDATE) |
| `cable` | ja | ja | org_isolation (ALL) |
| `ci` | ja | ja | ci_isolation (ALL), ci_scoped_delete (DELETE), ci_scoped_update (UPDATE) |
| `ci_change` | ja | ja | ci_change_scoped_delete (DELETE), ci_change_scoped_update (UPDATE), org_isolation (ALL) |
| `ci_contact` | ja | ja | ci_contact_scoped_delete (DELETE), ci_contact_scoped_update (UPDATE), org_isolation (ALL) |
| `ci_field_value` | ja | ja | ci_field_value_isolation (ALL), ci_field_value_scoped_delete (DELETE), ci_field_value_scoped_update (UPDATE) |
| `ci_identity_mac` | ja | ja | ci_identity_mac_isolation (ALL), ci_identity_mac_scoped_delete (DELETE), ci_identity_mac_scoped_update (UPDATE) |
| `ci_instance_field_definition` | ja | ja | ci_instance_field_definition_isolation (ALL), ci_instance_field_definition_scoped_delete (DELETE), ci_instance_field_definition_scoped_update (UPDATE) |
| `ci_merge` | ja | ja | ci_merge_isolation (ALL), ci_merge_scoped_delete (DELETE), ci_merge_scoped_update (UPDATE) |
| `ci_relationship` | ja | ja | ci_relationship_isolation (ALL), ci_relationship_scoped_delete (DELETE), ci_relationship_scoped_update (UPDATE) |
| `ci_snapshot` | ja | ja | ci_snapshot_isolation (ALL), ci_snapshot_scoped_delete (DELETE), ci_snapshot_scoped_update (UPDATE) |
| `ci_tag` | ja | ja | ci_tag_isolation (ALL), ci_tag_scoped_delete (DELETE), ci_tag_scoped_update (UPDATE) |
| `ci_template` | ja | ja | ci_template_isolation (ALL), ci_template_scoped_delete (DELETE), ci_template_scoped_update (UPDATE) |
| `ci_type` | ja | ja | ci_type_isolation_delete (DELETE), ci_type_isolation_insert (INSERT), ci_type_isolation_select (SELECT), ci_type_isolation_update (UPDATE) |
| `ci_type_attribute_set` | ja | ja | ci_type_attribute_set_isolation_delete (DELETE), ci_type_attribute_set_isolation_insert (INSERT), ci_type_attribute_set_isolation_select (SELECT), ci_type_attribute_set_isolation_update (UPDATE) |
| `client` | ja | ja | client_isolation (ALL) |
| `client_ci_type_hidden` | ja | ja | client_ci_type_hidden_isolation (ALL) |
| `collector` | ja | ja | collector_isolation (ALL), collector_scoped_delete (DELETE), collector_scoped_update (UPDATE) |
| `collector_ca` | ja | ja | collector_ca_operator (ALL) |
| `collector_certificate` | ja | ja | collector_certificate_isolation (ALL), collector_certificate_system_select (SELECT) |
| `collector_enrollment_code` | ja | ja | collector_enrollment_code_isolation (ALL), collector_enrollment_code_scope_delete (DELETE), collector_enrollment_code_scope_insert (INSERT), collector_enrollment_code_scope_select (SELECT), collector_enrollment_code_scope_update (UPDATE), collector_enrollment_code_system_select (SELECT) |
| `collector_group` | ja | ja | collector_group_isolation (ALL), collector_group_scoped_delete (DELETE), collector_group_scoped_update (UPDATE) |
| `collector_group_member` | ja | ja | collector_group_member_isolation (ALL), collector_group_member_scoped_delete (DELETE), collector_group_member_scoped_update (UPDATE) |
| `compliance_result` | ja | ja | compliance_result_scoped_delete (DELETE), compliance_result_scoped_update (UPDATE), compliance_result_tenant_isolation (ALL) |
| `compliance_rule` | ja | ja | compliance_rule_tenant_isolation (ALL) |
| `config_snapshot` | ja | ja | config_snapshot_isolation (ALL), config_snapshot_scoped_delete (DELETE), config_snapshot_scoped_update (UPDATE) |
| `consumable` | ja | ja | consumable_isolation (ALL), consumable_scoped_delete (DELETE), consumable_scoped_update (UPDATE) |
| `contact` | ja | ja | contact_scoped_delete (DELETE), contact_scoped_update (UPDATE), org_isolation (ALL) |
| `contract` | ja | ja | contract_isolation (ALL), contract_scoped_delete (DELETE), contract_scoped_update (UPDATE) |
| `credential` | ja | ja | credential_isolation (ALL), credential_scoped_delete (DELETE), credential_scoped_update (UPDATE) |
| `credential_binding` | ja | ja | credential_binding_isolation (ALL), credential_binding_scoped_delete (DELETE), credential_binding_scoped_update (UPDATE) |
| `credential_usage` | ja | ja | credential_usage_isolation (ALL), credential_usage_scoped_delete (DELETE), credential_usage_scoped_update (UPDATE) |
| `custom_role` | ja | ja | custom_role_tenant_isolation (ALL) |
| `desk` | ja | ja | desk_isolation (ALL), desk_scoped_delete (DELETE), desk_scoped_update (UPDATE) |
| `desk_booking` | ja | ja | desk_booking_isolation (ALL), desk_booking_scoped_delete (DELETE), desk_booking_scoped_update (UPDATE) |
| `discovery_job` | ja | ja | discovery_job_isolation (ALL) |
| `discovery_result` | ja | ja | discovery_result_isolation (ALL), discovery_result_scoped_delete (DELETE), discovery_result_scoped_update (UPDATE) |
| `discovery_scope` | ja | ja | discovery_scope_isolation (ALL), discovery_scope_scoped_delete (DELETE), discovery_scope_scoped_update (UPDATE) |
| `disposal_record` | ja | ja | disposal_record_isolation (ALL), disposal_record_scoped_delete (DELETE), disposal_record_scoped_update (UPDATE) |
| `document` | ja | ja | document_scoped_delete (DELETE), document_scoped_insert (INSERT), document_scoped_update (UPDATE), document_tenant_isolation (ALL) |
| `document_link` | ja | ja | document_link_scoped_delete (DELETE), document_link_scoped_update (UPDATE), document_link_tenant_isolation (ALL) |
| `document_version` | ja | ja | document_version_isolation (ALL) |
| `endpoint_agent` | ja | ja | endpoint_agent_isolation (ALL), endpoint_agent_scoped_delete (DELETE), endpoint_agent_scoped_update (UPDATE) |
| `entitlement` | ja | ja | entitlement_isolation (ALL) |
| `entity_change` | ja | ja | entity_change_isolation (ALL), entity_change_scoped_delete (DELETE), entity_change_scoped_update (UPDATE) |
| `event_outbox` | ja | ja | event_outbox_isolation (ALL), event_outbox_system_select (SELECT) |
| `event_outbox_counter` | ja | ja | event_outbox_counter_isolation (ALL), event_outbox_counter_system_select (SELECT) |
| `export_job` | ja | ja | export_job_isolation (ALL), export_job_system_select (SELECT) |
| `field_grant` | ja | ja | field_grant_isolation (ALL) |
| `field_sensitivity` | ja | ja | field_sensitivity_isolation (ALL) |
| `form_def` | ja | ja | form_def_scoped_delete (DELETE), form_def_scoped_update (UPDATE), form_def_tenant_isolation (ALL) |
| `form_def_version` | ja | ja | form_def_version_isolation (ALL), form_def_version_scoped_delete (DELETE), form_def_version_scoped_update (UPDATE) |
| `form_layout` | ja | ja | form_layout_isolation (ALL) |
| `form_submission` | ja | ja | form_submission_tenant_isolation (ALL) |
| `iga_access_request` | ja | ja | iga_access_request_tenant_isolation (ALL) |
| `iga_access_review` | ja | ja | iga_review_tenant_isolation (ALL) |
| `iga_access_review_item` | ja | ja | iga_review_item_tenant_isolation (ALL) |
| `iga_connector` | ja | ja | iga_connector_tenant_isolation (ALL) |
| `iga_drift_finding` | ja | ja | iga_drift_tenant_isolation (ALL) |
| `iga_lifecycle_policy` | ja | ja | iga_lifecycle_tenant_isolation (ALL) |
| `iga_provisioning_task` | ja | ja | iga_task_tenant_isolation (ALL) |
| `import_profile` | ja | ja | import_profile_isolation (ALL) |
| `incident` | ja | ja | incident_isolation (ALL), incident_scoped_delete (DELETE), incident_scoped_update (UPDATE) |
| `incident_transition` | ja | ja | incident_transition_isolation (ALL), incident_transition_scoped_delete (DELETE), incident_transition_scoped_update (UPDATE) |
| `ingest_record` | ja | ja | ingest_record_isolation (ALL) |
| `internal_order` | ja | ja | internal_order_isolation (ALL), internal_order_scoped_delete (DELETE), internal_order_scoped_update (UPDATE) |
| `internal_order_item` | ja | ja | internal_order_item_isolation (ALL), internal_order_item_scoped_delete (DELETE), internal_order_item_scoped_update (UPDATE) |
| `ip_address` | ja | ja | ip_address_scoped_delete (DELETE), ip_address_scoped_update (UPDATE), ip_address_section_acl_delete (DELETE), ip_address_section_acl_insert (INSERT), ip_address_section_acl_read (SELECT), ip_address_section_acl_update (UPDATE), org_isolation (ALL) |
| `ipam_change` | ja | ja | ipam_change_isolation (ALL), ipam_change_scoped_delete (DELETE) |
| `ipam_section` | ja | ja | ipam_section_acl_read (SELECT), ipam_section_isolation (ALL), ipam_section_scoped_delete (DELETE), ipam_section_scoped_update (UPDATE) |
| `ipam_section_permission` | ja | ja | ipam_section_permission_isolation (ALL), ipam_section_permission_scoped_delete (DELETE), ipam_section_permission_scoped_insert (INSERT), ipam_section_permission_scoped_update (UPDATE) |
| `job` | ja | ja | job_isolation (ALL), job_scope_select (SELECT), job_system_select (SELECT) |
| `key_assignment` | ja | ja | key_assignment_isolation (ALL), key_assignment_scoped_delete (DELETE), key_assignment_scoped_update (UPDATE) |
| `key_item` | ja | ja | key_item_isolation (ALL), key_item_scoped_delete (DELETE), key_item_scoped_update (UPDATE) |
| `lifecycle_definition` | ja | ja | lifecycle_definition_isolation_delete (DELETE), lifecycle_definition_isolation_insert (INSERT), lifecycle_definition_isolation_select (SELECT), lifecycle_definition_isolation_update (UPDATE) |
| `lifecycle_state` | ja | ja | lifecycle_state_isolation_delete (DELETE), lifecycle_state_isolation_insert (INSERT), lifecycle_state_isolation_select (SELECT), lifecycle_state_isolation_update (UPDATE) |
| `lifecycle_transition` | ja | ja | lifecycle_transition_isolation_delete (DELETE), lifecycle_transition_isolation_insert (INSERT), lifecycle_transition_isolation_select (SELECT), lifecycle_transition_isolation_update (UPDATE) |
| `location` | ja | ja | location_isolation (ALL) |
| `location_change` | ja | ja | location_change_isolation (ALL) |
| `location_node_retired` | ja | ja | location_node_retired_isolation (ALL), location_node_retired_scoped_delete (DELETE), location_node_retired_scoped_update (UPDATE) |
| `location_ref_migration` | ja | ja | location_ref_migration_isolation (ALL) |
| `maintenance_notification` | ja | ja | maintenance_notification_isolation (ALL), maintenance_notification_scoped_delete (DELETE), maintenance_notification_scoped_update (UPDATE) |
| `maintenance_window` | ja | ja | maintenance_window_isolation (ALL), maintenance_window_scoped_delete (DELETE), maintenance_window_scoped_insert (INSERT), maintenance_window_scoped_update (UPDATE) |
| `maintenance_window_ci` | ja | ja | maintenance_window_ci_isolation (ALL), maintenance_window_ci_scoped_delete (DELETE), maintenance_window_ci_scoped_update (UPDATE) |
| `metamodel_draft` | ja | ja | metamodel_draft_isolation (ALL) |
| `metamodel_rule` | ja | ja | metamodel_rule_isolation_delete (DELETE), metamodel_rule_isolation_insert (INSERT), metamodel_rule_isolation_select (SELECT), metamodel_rule_isolation_update (UPDATE) |
| `metric_sample` | nein | nein | – |
| `migration_quarantine` | ja | ja | migration_quarantine_isolation (ALL) |
| `monitoring_group` | ja | ja | monitoring_group_isolation (ALL), monitoring_group_scoped_delete (DELETE), monitoring_group_scoped_update (UPDATE) |
| `monitoring_group_member` | ja | ja | monitoring_group_member_isolation (ALL), monitoring_group_member_scoped_delete (DELETE), monitoring_group_member_scoped_update (UPDATE) |
| `monitoring_profile` | ja | ja | monitoring_profile_isolation (ALL), monitoring_profile_scoped_delete (DELETE), monitoring_profile_scoped_update (UPDATE) |
| `nameserver_set` | ja | ja | nameserver_set_isolation (ALL), nameserver_set_scoped_delete (DELETE), nameserver_set_scoped_insert (INSERT), nameserver_set_scoped_update (UPDATE) |
| `nat_translation` | ja | ja | nat_translation_isolation (ALL), nat_translation_scoped_delete (DELETE), nat_translation_scoped_update (UPDATE) |
| `network_interface` | ja | ja | network_interface_scoped_delete (DELETE), network_interface_scoped_update (UPDATE), org_isolation (ALL) |
| `notice_sent` | ja | ja | notice_sent_isolation (ALL) |
| `notification_channel` | ja | ja | notification_channel_isolation (ALL) |
| `notification_delivery` | ja | ja | notification_delivery_isolation (ALL), notification_delivery_system_select (SELECT) |
| `notification_digest_item` | ja | ja | notification_digest_item_isolation (ALL), notification_digest_item_system_select (SELECT) |
| `notification_inbox` | ja | ja | notification_inbox_isolation (ALL) |
| `notification_preference` | ja | ja | notification_preference_isolation (ALL) |
| `object_grant` | ja | ja | object_grant_isolation (ALL) |
| `operator_audit` | ja | ja | operator_audit_operator (ALL) |
| `org_dek` | ja | ja | org_dek_isolation (ALL) |
| `organization` | ja | ja | org_isolation (ALL), organization_system_select (SELECT) |
| `organization_usage` | ja | ja | organization_usage_isolation (ALL) |
| `permission` | nein | nein | – |
| `pii_subject` | ja | ja | pii_subject_isolation (ALL) |
| `platform_secret` | ja | ja | platform_secret_system_insert (INSERT), platform_secret_system_select (SELECT), platform_secret_system_update (UPDATE) |
| `privacy_retention_policy` | ja | ja | privacy_retention_isolation (ALL) |
| `probe` | ja | ja | probe_isolation (ALL), probe_scoped_delete (DELETE), probe_scoped_update (UPDATE) |
| `probe_assignment` | ja | ja | probe_assignment_isolation (ALL), probe_assignment_scoped_delete (DELETE), probe_assignment_scoped_update (UPDATE) |
| `probe_autocreate_suppression` | ja | ja | probe_autocreate_suppression_isolation (ALL), probe_autocreate_suppression_scoped_delete (DELETE), probe_autocreate_suppression_scoped_update (UPDATE) |
| `probe_epoch` | ja | ja | probe_epoch_isolation (ALL) |
| `probe_ingest_cursor` | ja | ja | probe_ingest_cursor_isolation (ALL), probe_ingest_cursor_system_select (SELECT) |
| `probe_result` | nein | nein | – |
| `probe_state` | ja | ja | probe_state_isolation (ALL), probe_state_scoped_delete (DELETE), probe_state_scoped_update (UPDATE) |
| `probe_state_change` | ja | ja | probe_state_change_isolation (ALL), probe_state_change_scoped_delete (DELETE), probe_state_change_scoped_update (UPDATE) |
| `rack` | ja | ja | rack_isolation (ALL) |
| `rack_mount` | ja | ja | rack_mount_isolation (ALL), rack_mount_scoped_delete (DELETE), rack_mount_scoped_update (UPDATE) |
| `relationship_change` | ja | ja | relationship_change_isolation (ALL), relationship_change_scoped_delete (DELETE), relationship_change_scoped_update (UPDATE) |
| `relationship_suppression` | ja | ja | relationship_suppression_scoped_delete (DELETE), relationship_suppression_scoped_update (UPDATE), suppression_isolation (ALL) |
| `relationship_type` | ja | ja | relationship_type_isolation_delete (DELETE), relationship_type_isolation_insert (INSERT), relationship_type_isolation_select (SELECT), relationship_type_isolation_update (UPDATE) |
| `reservation` | ja | ja | reservation_isolation (ALL), reservation_scoped_delete (DELETE), reservation_scoped_update (UPDATE) |
| `review_item` | ja | ja | review_item_isolation (ALL), review_item_scoped_delete (DELETE), review_item_scoped_select (SELECT), review_item_scoped_update (UPDATE) |
| `role` | ja | ja | role_isolation (ALL) |
| `role_assignment` | ja | ja | role_assignment_isolation (ALL) |
| `role_permission` | ja | ja | role_permission_tenant_isolation (ALL) |
| `room` | ja | ja | room_isolation (ALL) |
| `saved_view` | ja | ja | saved_view_isolation (ALL) |
| `scheduler_run` | ja | ja | scheduler_run_operator (ALL) |
| `search_document` | ja | ja | search_document_scoped_delete (DELETE), search_document_scoped_update (UPDATE), search_document_section_acl (SELECT), search_document_tenant_isolation (ALL) |
| `search_document_field` | ja | ja | search_document_field_scoped_delete (DELETE), search_document_field_scoped_update (UPDATE), search_document_field_tenant_isolation (ALL) |
| `search_identifier` | ja | ja | search_identifier_tenant_isolation (ALL) |
| `security_finding` | ja | ja | security_finding_isolation (ALL), security_finding_scoped_delete (DELETE), security_finding_scoped_update (UPDATE) |
| `service_account` | ja | ja | service_account_isolation (ALL) |
| `service_account_role` | ja | ja | service_account_role_isolation (ALL) |
| `signup_request` | ja | ja | signup_request_operator (ALL) |
| `site` | ja | ja | site_isolation (ALL), site_scoped_delete (DELETE), site_scoped_update (UPDATE) |
| `sla` | ja | ja | sla_scoped_delete (DELETE), sla_scoped_update (UPDATE), sla_tenant_isolation (ALL) |
| `source_priority_policy` | ja | ja | source_priority_policy_isolation (ALL) |
| `source_rank_policy` | ja | ja | source_rank_policy_isolation (ALL) |
| `stock_level` | ja | ja | stock_level_isolation (ALL) |
| `stock_movement` | ja | ja | stock_movement_isolation (ALL), stock_movement_scoped_delete (DELETE), stock_movement_scoped_update (UPDATE) |
| `stock_scan` | ja | ja | stock_scan_isolation (ALL), stock_scan_scoped_delete (DELETE), stock_scan_scoped_update (UPDATE) |
| `stocktake` | ja | ja | stocktake_isolation (ALL), stocktake_scoped_delete (DELETE), stocktake_scoped_update (UPDATE) |
| `stocktake_item` | ja | ja | stocktake_item_isolation (ALL) |
| `subnet` | ja | ja | org_isolation (ALL), subnet_scoped_delete (DELETE), subnet_scoped_update (UPDATE), subnet_section_acl_delete (DELETE), subnet_section_acl_insert (INSERT), subnet_section_acl_read (SELECT), subnet_section_acl_update (UPDATE) |
| `supplier` | ja | ja | supplier_isolation (ALL), supplier_scoped_delete (DELETE), supplier_scoped_update (UPDATE) |
| `tag` | ja | ja | tag_isolation (ALL) |
| `team` | ja | ja | team_tenant_isolation (ALL) |
| `team_member` | ja | ja | team_member_isolation (ALL) |
| `template` | ja | ja | template_isolation_delete (DELETE), template_isolation_insert (INSERT), template_isolation_select (SELECT), template_isolation_update (UPDATE) |
| `template_version` | ja | ja | template_version_isolation_delete (DELETE), template_version_isolation_insert (INSERT), template_version_isolation_select (SELECT), template_version_isolation_update (UPDATE) |
| `template_version_item` | ja | ja | template_version_item_isolation_delete (DELETE), template_version_item_isolation_insert (INSERT), template_version_item_isolation_select (SELECT), template_version_item_isolation_update (UPDATE) |
| `ticket` | ja | ja | ticket_scoped_delete (DELETE), ticket_scoped_update (UPDATE), ticket_tenant_isolation (ALL) |
| `ticket_comment` | ja | ja | ticket_comment_scoped_delete (DELETE), ticket_comment_scoped_update (UPDATE), ticket_comment_tenant_isolation (ALL) |
| `ticket_link` | ja | ja | ticket_link_scoped_delete (DELETE), ticket_link_scoped_update (UPDATE), ticket_link_tenant_isolation (ALL) |
| `ticket_number_counter` | ja | ja | ticket_number_counter_isolation (ALL) |
| `ticket_sla` | ja | ja | ticket_sla_tenant_isolation (ALL) |
| `ticket_tag` | ja | ja | ticket_tag_scoped_delete (DELETE), ticket_tag_scoped_update (UPDATE), ticket_tag_tenant_isolation (ALL) |
| `training` | ja | ja | training_isolation (ALL) |
| `training_assignment` | ja | ja | training_assignment_isolation (ALL) |
| `user_custom_role` | ja | ja | user_custom_role_isolation (ALL) |
| `user_invitation` | ja | ja | org_isolation (ALL) |
| `vault_connection` | ja | ja | vault_connection_isolation (ALL), vault_connection_scoped_delete (DELETE), vault_connection_scoped_update (UPDATE) |
| `vault_connection_collector` | ja | ja | vault_connection_collector_isolation (ALL), vault_connection_collector_scoped_delete (DELETE), vault_connection_collector_scoped_update (UPDATE) |
| `vlan` | ja | ja | vlan_isolation (ALL), vlan_scoped_delete (DELETE), vlan_scoped_update (UPDATE) |
| `vlan_domain` | ja | ja | vlan_domain_isolation (ALL), vlan_domain_scoped_delete (DELETE), vlan_domain_scoped_insert (INSERT), vlan_domain_scoped_update (UPDATE) |
| `vlan_domain_site` | ja | ja | vlan_domain_site_isolation (ALL) |
| `vrf` | ja | ja | vrf_isolation (ALL), vrf_scoped_delete (DELETE), vrf_scoped_insert (INSERT), vrf_scoped_update (UPDATE) |
| `vrf_section` | ja | ja | vrf_section_isolation (ALL), vrf_section_scoped_delete (DELETE), vrf_section_scoped_insert (INSERT), vrf_section_scoped_update (UPDATE) |
| `vuln_advisory` | ja | ja | vuln_advisory_read (SELECT), vuln_advisory_system_delete (DELETE), vuln_advisory_system_insert (INSERT), vuln_advisory_system_update (UPDATE) |
| `vuln_affected` | ja | ja | vuln_affected_read (SELECT), vuln_affected_system_delete (DELETE), vuln_affected_system_insert (INSERT), vuln_affected_system_update (UPDATE) |
| `vuln_source` | ja | ja | vuln_source_read (SELECT), vuln_source_system_delete (DELETE), vuln_source_system_insert (INSERT), vuln_source_system_update (UPDATE) |
| `webhook_dead_letter` | ja | ja | webhook_dead_letter_isolation (ALL), webhook_dead_letter_system_select (SELECT) |
| `webhook_delivery` | ja | ja | webhook_delivery_isolation (ALL), webhook_delivery_system_select (SELECT) |
| `webhook_subscription` | ja | ja | webhook_sub_isolation (ALL), webhook_sub_scope_select (SELECT) |
| `workflow_def` | ja | ja | workflow_def_tenant_isolation (ALL) |
| `workflow_def_version` | ja | ja | workflow_def_version_tenant_isolation (ALL) |
| `workflow_run` | ja | ja | workflow_run_tenant_isolation (ALL) |
| `workflow_step` | ja | ja | workflow_step_tenant_isolation (ALL) |

<!-- schema-baseline:tables:end -->
