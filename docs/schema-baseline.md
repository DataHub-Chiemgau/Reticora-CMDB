# Schema-Baseline

Diese Datei beschreibt den Datenbankschema-Stand nach allen Migrationen in
`backend/migrations/` (Anforderung DB-02). Sie ist die Referenz für Reviews von
Migrationen: Jede Änderung an Tabellen, Row Level Security (RLS) oder Policies
zeigt sich hier als Diff.

Stand: Migration 000222_document_scan_status

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

Ebenso der Index der Standardsortierung der CI-Liste (WP-605, NFR-02):

```sql
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_ci_list_sort ON ci (organization_id, created_at DESC, id DESC) INCLUDE (client_id, site_id) WHERE deleted_at IS NULL;
```

Ebenso der Index der CI-Liste sortiert nach Name (WP-839, E-17, Migration
000196):

```sql
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_ci_list_name ON ci (organization_id, name, id) WHERE deleted_at IS NULL;
```

Ebenso der Index der Adressen einer Schnittstelle (WP-608, NFR-03):

```sql
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_ip_address_interface ON ip_address (interface_id) WHERE interface_id IS NOT NULL;
```

Ebenso der Override-Index der Knotenlesung (WP-840, NFR-02):

```sql
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_ci_field_value_override ON ci_field_value (organization_id, ci_id) WHERE override_at IS NOT NULL;
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
  WP-040) und behält ihren Eigentümer.
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
  `metric_sample` und die Materialisierungs-Hypertable von
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
| `team-scope` | Tabellen mit `team_id` verwenden `app.team_scope` in `USING` und `WITH CHECK` (CH25) |
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

### Linkziele von Dokumenten (WP-267)

`document_link.entity_type` ist eines von `ci`, `asset`, `assignment`,
`ticket`, `location`, `disposal_record` und `stocktake`
(`doc_link_entity_type_check`, Migration 000217); die Registry in
`backend/internal/document/links.go` hält dieselben Werte.
`derive_document_link_scope` leitet `client_id`/`site_id` auch aus
Location, Entsorgung und dem Standort von Asset und Zuweisung ab und
`team_id` (Teilindex `idx_document_link_team`) aus dem Ticket. Neue
AFTER-UPDATE-Trigger auf `location` und `disposal_record`
(`propagate_document_link_scope`) sowie `asset_module_scope_propagation`
(auch bei `site_id`) und `ticket_scope_propagation` (auch bei
`team_id`) leiten die Links neu ab. Die Policy von `document_link` trägt
das Team-Prädikat wie `ticket`; ein team-beschränkter Prinzipal gilt für
`document` (Policies, `require_visible_parent`, `require_linked_row`)
nicht mehr als org-weit. `app_scope_widen`/`app_scope_restore` leeren
bzw. setzen auch `app.team_scope`.

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
(Migration 000151, F-141). Der Trigger `derive_location_site` setzt ihn bei
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
`asset_movement`, `assignment`, `composition`, `form_submission` und
`stock_scan` (Sichtbarkeit über das referenzierte Asset, CI, Formular bzw.
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

<!-- rls-leaky-indexes:begin -->

| Index | Tabelle | Nicht leakproof |
|---|---|---|
| `desk_booking_no_overlap` | `desk_booking` | function tstzrange(timestamp with time zone,timestamp with time zone), operator class range_ops |
| `idx_ci_attributes_path` | `ci` | operator class jsonb_path_ops |
| `idx_location_path` | `location` | operator class gist_ltree_ops |
| `idx_pii_subject_tokens` | `pii_subject` | operator class array_ops |
| `idx_review_item_candidates` | `review_item` | operator class array_ops |
| `idx_saved_view_shared_roles` | `saved_view` | operator class array_ops |
| `idx_search_document_fts` | `search_document` | operator class tsvector_ops |
| `idx_signup_request_email` | `signup_request` | function lower(text) |
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
Löschverhalten bleiben (SET NULL als `ON DELETE SET NULL (spalte)`).
Bestehende Zeilen mit einem Verweis über die Organisationsgrenze stehen danach
in `migration_quarantine` (Migration `000194`): Nullbare Verweise sind auf NULL
gesetzt, die ganze Zeile ist protokolliert; Reservierungen sind verschoben
(E-26). Knoten des Standortbaums mit fremdem Eltern- oder Site-Knoten lässt
`location_maintain` seit 000062 nicht zu; findet die Migration doch einen,
bricht sie mit der Liste ab (Muster 000177). Die Down-Migration stellt die
einfachen Fremdschlüssel und die Zeilen wieder her.

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
erzeugt. Rest von SR-03 vor GA (E-137, WP-329); Tabellen, die Phase-2-WPs
ersetzen, folgen K19 in ihren Nachfolgern.

<!-- rls-references-without-organization:begin -->

| Verweis | Fremdschlüssel | Ziel | Grund |
|---|---|---|---|
| `agent_enrollment_token.site_id` | `agent_enrollment_token_site_id_fkey` | `site` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `ai_conversation.user_id` | `ai_conversation_user_id_fkey` | `app_user` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `ai_message.conversation_id` | `ai_message_conversation_id_fkey` | `ai_conversation` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `api_key.rotated_from` | `api_key_rotated_from_fkey` | `api_key` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `asset.parent_asset_id` | `asset_parent_asset_id_fkey` | `asset` | composition and asset.parent_asset_id are dropped by WP-743 (K19) |
| `asset_location.asset_id` | `asset_location_asset_id_fkey` | `asset` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `asset_movement.asset_id` | `asset_movement_asset_id_fkey` | `asset` | asset_movement is replaced by the ledger stock_movement (WP-274, K19); the successor references with the organization |
| `asset_movement.from_location_id` | `asset_movement_from_location_id_fkey` | `location` | asset_movement is replaced by the ledger stock_movement (WP-274, K19); the successor references with the organization |
| `asset_movement.quantity_item_id` | `asset_movement_quantity_item_fk` | `quantity_item` | asset_movement is replaced by the ledger stock_movement (WP-274, K19); the successor references with the organization |
| `asset_movement.to_location_id` | `asset_movement_to_location_id_fkey` | `location` | asset_movement is replaced by the ledger stock_movement (WP-274, K19); the successor references with the organization |
| `assignment.asset_id` | `assignment_asset_id_fkey` | `asset` | assignment is replaced by the assignment service over stock_movement (WP-275, K19); the successor references with the organization |
| `assignment.assigned_by` | `assignment_assigned_by_fkey` | `app_user` | assignment is replaced by the assignment service over stock_movement (WP-275, K19); the successor references with the organization |
| `assignment.assigned_to` | `assignment_assigned_to_fkey` | `app_user` | assignment is replaced by the assignment service over stock_movement (WP-275, K19); the successor references with the organization |
| `assignment.ci_id` | `assignment_ci_id_fkey` | `ci` | assignment is replaced by the assignment service over stock_movement (WP-275, K19); the successor references with the organization |
| `building.id` | `building_location_fkey` | `location` | the specialist row shares its id with its node of the location tree (000157); rest of SR-03, fixed before GA (E-137, WP-329) |
| `building.site_id` | `building_site_id_fkey` | `site` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `cable.source_interface_id` | `cable_source_interface_id_fkey` | `network_interface` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `cable.target_interface_id` | `cable_target_interface_id_fkey` | `network_interface` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `ci.room_id` | `ci_room_id_fkey` | `room` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `ci.site_id` | `ci_site_id_fkey` | `site` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `ci_change.ci_id` | `ci_change_ci_id_fkey` | `ci` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `ci_contact.ci_id` | `ci_contact_ci_id_fkey` | `ci` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `ci_contact.contact_id` | `ci_contact_contact_id_fkey` | `contact` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `ci_field_value.ci_id` | `ci_field_value_ci_id_fkey` | `ci` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `ci_instance_field_definition.ci_id` | `ci_instance_field_definition_ci_id_fkey` | `ci` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `ci_merge.site_id` | `ci_merge_site_id_fkey` | `site` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `ci_relationship.source_ci_id` | `ci_relationship_source_ci_id_fkey` | `ci` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `ci_relationship.source_interface_id` | `ci_relationship_source_interface_id_fkey` | `network_interface` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `ci_relationship.target_ci_id` | `ci_relationship_target_ci_id_fkey` | `ci` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `ci_relationship.target_interface_id` | `ci_relationship_target_interface_id_fkey` | `network_interface` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `ci_relationship.verified_by` | `ci_relationship_verified_by_fkey` | `app_user` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `collector.site_id` | `collector_site_id_fkey` | `site` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `collector_certificate.collector_id` | `collector_certificate_collector_id_fkey` | `collector` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `collector_enrollment_code.used_by_collector_id` | `collector_enrollment_code_used_by_collector_id_fkey` | `collector` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `compliance_result.ci_id` | `compliance_result_ci_id_fkey` | `ci` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `compliance_result.rule_id` | `compliance_result_rule_id_fkey` | `compliance_rule` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `composition.child_asset_id` | `composition_child_asset_id_fkey` | `asset` | composition and asset.parent_asset_id are dropped by WP-743 (K19) |
| `composition.child_ci_id` | `composition_child_ci_id_fkey` | `ci` | composition and asset.parent_asset_id are dropped by WP-743 (K19) |
| `composition.parent_asset_id` | `composition_parent_asset_id_fkey` | `asset` | composition and asset.parent_asset_id are dropped by WP-743 (K19) |
| `contact.pii_subject_id` | `contact_pii_subject_id_fkey` | `pii_subject` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `discovery_job.collector_id` | `discovery_job_collector_id_fkey` | `collector` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `discovery_result.job_id` | `discovery_result_job_id_fkey` | `discovery_job` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `discovery_result.matched_ci_id` | `discovery_result_matched_ci_id_fkey` | `ci` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `discovery_scope.collector_id` | `discovery_scope_collector_id_fkey` | `collector` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `discovery_scope.site_id` | `discovery_scope_site_id_fkey` | `site` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `document.uploaded_by` | `document_uploaded_by_fkey` | `app_user` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `document_link.document_id` | `document_link_document_id_fkey` | `document` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `endpoint_agent.ci_id` | `endpoint_agent_ci_id_fkey` | `ci` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `form_submission.ci_id` | `form_submission_ci_id_fkey` | `ci` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `form_submission.form_id` | `form_submission_form_id_fkey` | `form_def` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `form_submission.submitted_by` | `form_submission_submitted_by_fkey` | `app_user` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `form_submission.ticket_id` | `form_submission_ticket_id_fkey` | `ticket` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `iga_access_request.connector_id` | `iga_access_request_connector_id_fkey` | `iga_connector` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `iga_access_request.decision_by` | `iga_access_request_decision_by_fkey` | `app_user` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `iga_access_request.requester_id` | `iga_access_request_requester_id_fkey` | `app_user` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `iga_access_request.subject_user_id` | `iga_access_request_subject_user_id_fkey` | `app_user` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `iga_access_request.workflow_run_id` | `iga_access_request_workflow_run_id_fkey` | `workflow_run` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `iga_access_review_item.connector_id` | `iga_access_review_item_connector_id_fkey` | `iga_connector` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `iga_access_review_item.decision_by` | `iga_access_review_item_decision_by_fkey` | `app_user` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `iga_access_review_item.review_id` | `iga_access_review_item_review_id_fkey` | `iga_access_review` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `iga_access_review_item.user_id` | `iga_access_review_item_user_id_fkey` | `app_user` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `iga_connector.collector_id` | `iga_connector_collector_id_fkey` | `collector` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `iga_connector.credential_id` | `iga_connector_credential_id_fkey` | `credential` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `iga_drift_finding.connector_id` | `iga_drift_finding_connector_id_fkey` | `iga_connector` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `iga_drift_finding.remediation_task_id` | `iga_drift_finding_remediation_task_id_fkey` | `iga_provisioning_task` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `iga_drift_finding.user_id` | `iga_drift_finding_user_id_fkey` | `app_user` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `iga_provisioning_task.connector_id` | `iga_provisioning_task_connector_id_fkey` | `iga_connector` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `iga_provisioning_task.user_id` | `iga_provisioning_task_user_id_fkey` | `app_user` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `internal_order_item.order_id` | `internal_order_item_order_id_fkey` | `internal_order` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `ip_address.interface_id` | `ip_address_interface_id_fkey` | `network_interface` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `ip_address.subnet_id` | `ip_address_subnet_id_fkey` | `subnet` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `maintenance_notification.maintenance_window_id` | `maintenance_notification_maintenance_window_id_fkey` | `maintenance_window` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `maintenance_window_ci.ci_id` | `maintenance_window_ci_ci_id_fkey` | `ci` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `maintenance_window_ci.maintenance_window_id` | `maintenance_window_ci_maintenance_window_id_fkey` | `maintenance_window` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `network_interface.ci_id` | `network_interface_ci_id_fkey` | `ci` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `notification_delivery.channel_id` | `notification_delivery_channel_id_fkey` | `notification_channel` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `notification_delivery.recipient_user_id` | `notification_delivery_recipient_user_id_fkey` | `app_user` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `notification_digest_item.channel_id` | `notification_digest_item_channel_id_fkey` | `notification_channel` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `notification_digest_item.delivery_id` | `notification_digest_item_delivery_id_fkey` | `notification_delivery` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `notification_digest_item.recipient_user_id` | `notification_digest_item_recipient_user_id_fkey` | `app_user` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `notification_inbox.user_id` | `notification_inbox_user_id_fkey` | `app_user` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `notification_preference.user_id` | `notification_preference_user_id_fkey` | `app_user` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `pii_subject.parent_id` | `pii_subject_parent_id_fkey` | `pii_subject` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `quantity_item.location_id` | `quantity_item_location_id_fkey` | `location` | quantity_item is dropped by WP-758 (E-40, K19); its successor references with the organization |
| `rack.id` | `rack_location_fkey` | `location` | the specialist row shares its id with its node of the location tree (000157); rest of SR-03, fixed before GA (E-137, WP-329) |
| `rack.room_id` | `rack_room_id_fkey` | `room` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `rack_mount.ci_id` | `rack_mount_ci_id_fkey` | `ci` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `rack_mount.rack_id` | `rack_mount_rack_id_fkey` | `rack` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `relationship_suppression.source_ci_id` | `relationship_suppression_source_ci_id_fkey` | `ci` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `relationship_suppression.target_ci_id` | `relationship_suppression_target_ci_id_fkey` | `ci` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `reservation.quantity_item_id` | `reservation_quantity_item_id_fkey` | `quantity_item` | quantity_item is dropped by WP-758 (E-40, K19); its successor references with the organization |
| `role_assignment.role_id` | `role_assignment_role_id_fkey` | `role` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `role_assignment.scope_site_id` | `role_assignment_scope_site_id_fkey` | `site` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `role_assignment.user_id` | `role_assignment_user_id_fkey` | `app_user` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `role_permission.granted_by` | `role_permission_granted_by_fkey` | `app_user` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `role_permission.role_id` | `role_permission_role_id_fkey` | `role` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `room.building_id` | `room_building_id_fkey` | `building` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `room.id` | `room_location_fkey` | `location` | the specialist row shares its id with its node of the location tree (000157); rest of SR-03, fixed before GA (E-137, WP-329) |
| `security_finding.ci_id` | `security_finding_ci_id_fkey` | `ci` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `service_account_role.role_id` | `service_account_role_role_id_fkey` | `role` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `service_account_role.scope_site_id` | `service_account_role_scope_site_id_fkey` | `site` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `site.id` | `site_location_fkey` | `location` | the specialist row shares its id with its node of the location tree (000157); rest of SR-03, fixed before GA (E-137, WP-329) |
| `stock_movement.consumable_id` | `stock_movement_consumable_id_fkey` | `consumable` | the former stock_movement is replaced by the ledger of WP-274 (K19); the successor references with the organization |
| `stock_scan.asset_id` | `stock_scan_asset_id_fkey` | `asset` | stock_scan is replaced by the stocktake scans of WP-287 (K19); the successor references with the organization |
| `stock_scan.ci_id` | `stock_scan_ci_id_fkey` | `ci` | stock_scan is replaced by the stocktake scans of WP-287 (K19); the successor references with the organization |
| `stock_scan.scanned_by` | `stock_scan_scanned_by_fkey` | `app_user` | stock_scan is replaced by the stocktake scans of WP-287 (K19); the successor references with the organization |
| `stock_scan.stocktake_id` | `stock_scan_stocktake_id_fkey` | `stocktake` | stock_scan is replaced by the stocktake scans of WP-287 (K19); the successor references with the organization |
| `stocktake.started_by` | `stocktake_started_by_fkey` | `app_user` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `subnet.site_id` | `subnet_site_id_fkey` | `site` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `team.lead_id` | `team_lead_id_fkey` | `app_user` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `team_member.team_id` | `team_member_team_id_fkey` | `team` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `team_member.user_id` | `team_member_user_id_fkey` | `app_user` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `ticket.assignee_id` | `ticket_assignee_id_fkey` | `app_user` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `ticket.reporter_id` | `ticket_reporter_id_fkey` | `app_user` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `ticket.team_id` | `ticket_team_id_fkey` | `team` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `ticket_comment.author_id` | `ticket_comment_author_id_fkey` | `app_user` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `ticket_comment.ticket_id` | `ticket_comment_ticket_id_fkey` | `ticket` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `ticket_sla.sla_id` | `ticket_sla_sla_id_fkey` | `sla` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `ticket_sla.ticket_id` | `ticket_sla_ticket_id_fkey` | `ticket` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `user_custom_role.custom_role_id` | `user_custom_role_custom_role_id_fkey` | `custom_role` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `user_custom_role.granted_by` | `user_custom_role_granted_by_fkey` | `app_user` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `user_custom_role.user_id` | `user_custom_role_user_id_fkey` | `app_user` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `user_invitation.role_id` | `user_invitation_role_id_fkey` | `role` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `vlan.site_id` | `vlan_site_id_fkey` | `site` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `webhook_dead_letter.delivery_id` | `webhook_dead_letter_delivery_id_fkey` | `webhook_delivery` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `webhook_dead_letter.subscription_id` | `webhook_dead_letter_subscription_id_fkey` | `webhook_subscription` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `webhook_delivery.subscription_id` | `webhook_delivery_subscription_id_fkey` | `webhook_subscription` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `workflow_run.workflow_id` | `workflow_run_workflow_id_fkey` | `workflow_def` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |
| `workflow_step.run_id` | `workflow_step_run_id_fkey` | `workflow_run` | G1 reference that phase 2 does not change; rest of SR-03, fixed before GA (E-137, WP-329) |

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
`location` oder `relationship` (`import_profile_entity_check`), `format`
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

## Tabellen, RLS und Policies

Spalten: Tabellenname, RLS aktiviert, RLS erzwungen (`FORCE ROW LEVEL
SECURITY`), Policies mit ihrem Kommando.

<!-- schema-baseline:tables:begin -->

| Tabelle | RLS | FORCE RLS | Policies (Kommando) |
|---|---|---|---|
| `agent_enrollment_token` | ja | ja | agent_enrollment_token_isolation (ALL), agent_enrollment_token_scoped_delete (DELETE), agent_enrollment_token_scoped_update (UPDATE) |
| `ai_chunk` | ja | ja | ai_chunk_scoped_delete (DELETE), ai_chunk_scoped_update (UPDATE), ai_chunk_tenant_isolation (ALL) |
| `ai_conversation` | ja | ja | ai_conversation_tenant_isolation (ALL) |
| `ai_message` | ja | ja | ai_message_tenant_isolation (ALL) |
| `alert_rule` | ja | ja | alert_rule_isolation (ALL), alert_rule_system_select (SELECT) |
| `api_key` | ja | ja | api_key_system_select (SELECT), org_isolation (ALL) |
| `app_user` | ja | ja | user_isolation (ALL) |
| `asset` | ja | ja | asset_scoped_delete (DELETE), asset_scoped_update (UPDATE), asset_tenant_isolation (ALL) |
| `asset_location` | ja | ja | asset_location_isolation (ALL) |
| `asset_movement` | ja | ja | asset_movement_isolation (ALL) |
| `assignment` | ja | ja | assignment_tenant_isolation (ALL) |
| `attribute_definition` | ja | ja | attribute_definition_isolation_delete (DELETE), attribute_definition_isolation_insert (INSERT), attribute_definition_isolation_select (SELECT), attribute_definition_isolation_update (UPDATE) |
| `attribute_set` | ja | ja | attribute_set_isolation_delete (DELETE), attribute_set_isolation_insert (INSERT), attribute_set_isolation_select (SELECT), attribute_set_isolation_update (UPDATE) |
| `audit_log` | ja | ja | audit_isolation (ALL) |
| `audit_route_intent` | ja | ja | audit_route_intent_isolation (ALL), audit_route_intent_system_select (SELECT) |
| `building` | ja | ja | building_isolation (ALL) |
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
| `collector` | ja | ja | collector_isolation (ALL), collector_scoped_delete (DELETE), collector_scoped_update (UPDATE) |
| `collector_ca` | ja | ja | collector_ca_operator (ALL) |
| `collector_certificate` | ja | ja | collector_certificate_isolation (ALL), collector_certificate_system_select (SELECT) |
| `collector_enrollment_code` | ja | ja | collector_enrollment_code_isolation (ALL), collector_enrollment_code_scope_delete (DELETE), collector_enrollment_code_scope_insert (INSERT), collector_enrollment_code_scope_select (SELECT), collector_enrollment_code_scope_update (UPDATE), collector_enrollment_code_system_select (SELECT) |
| `compliance_result` | ja | ja | compliance_result_scoped_delete (DELETE), compliance_result_scoped_update (UPDATE), compliance_result_tenant_isolation (ALL) |
| `compliance_rule` | ja | ja | compliance_rule_tenant_isolation (ALL) |
| `composition` | ja | ja | composition_isolation (ALL) |
| `consumable` | ja | ja | consumable_isolation (ALL), consumable_scoped_delete (DELETE), consumable_scoped_update (UPDATE) |
| `contact` | ja | ja | contact_scoped_delete (DELETE), contact_scoped_update (UPDATE), org_isolation (ALL) |
| `credential` | ja | ja | credential_isolation (ALL), credential_scoped_delete (DELETE), credential_scoped_update (UPDATE) |
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
| `entity_change` | ja | ja | entity_change_isolation (ALL) |
| `event_outbox` | ja | ja | event_outbox_isolation (ALL), event_outbox_system_select (SELECT) |
| `event_outbox_counter` | ja | ja | event_outbox_counter_isolation (ALL), event_outbox_counter_system_select (SELECT) |
| `export_job` | ja | ja | export_job_isolation (ALL), export_job_system_select (SELECT) |
| `form_def` | ja | ja | form_def_scoped_delete (DELETE), form_def_scoped_update (UPDATE), form_def_tenant_isolation (ALL) |
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
| `ingest_record` | ja | ja | ingest_record_isolation (ALL) |
| `internal_order` | ja | ja | internal_order_isolation (ALL), internal_order_scoped_delete (DELETE), internal_order_scoped_update (UPDATE) |
| `internal_order_item` | ja | ja | internal_order_item_isolation (ALL) |
| `ip_address` | ja | ja | ip_address_scoped_delete (DELETE), ip_address_scoped_update (UPDATE), org_isolation (ALL) |
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
| `network_interface` | ja | ja | network_interface_scoped_delete (DELETE), network_interface_scoped_update (UPDATE), org_isolation (ALL) |
| `notice_sent` | ja | ja | notice_sent_isolation (ALL) |
| `notification_channel` | ja | ja | notification_channel_isolation (ALL) |
| `notification_delivery` | ja | ja | notification_delivery_isolation (ALL), notification_delivery_system_select (SELECT) |
| `notification_digest_item` | ja | ja | notification_digest_item_isolation (ALL), notification_digest_item_system_select (SELECT) |
| `notification_inbox` | ja | ja | notification_inbox_isolation (ALL) |
| `notification_preference` | ja | ja | notification_preference_isolation (ALL) |
| `operator_audit` | ja | ja | operator_audit_operator (ALL) |
| `org_dek` | ja | ja | org_dek_isolation (ALL) |
| `organization` | ja | ja | org_isolation (ALL), organization_system_select (SELECT) |
| `organization_usage` | ja | ja | organization_usage_isolation (ALL) |
| `permission` | nein | nein | – |
| `pii_subject` | ja | ja | pii_subject_isolation (ALL) |
| `platform_secret` | ja | ja | platform_secret_system_insert (INSERT), platform_secret_system_select (SELECT), platform_secret_system_update (UPDATE) |
| `privacy_retention_policy` | ja | ja | privacy_retention_isolation (ALL) |
| `quantity_item` | ja | ja | quantity_item_isolation (ALL), quantity_item_scoped_delete (DELETE), quantity_item_scoped_update (UPDATE) |
| `rack` | ja | ja | rack_isolation (ALL) |
| `rack_mount` | ja | ja | rack_mount_isolation (ALL), rack_mount_scoped_delete (DELETE), rack_mount_scoped_update (UPDATE) |
| `relationship_suppression` | ja | ja | relationship_suppression_scoped_delete (DELETE), relationship_suppression_scoped_update (UPDATE), suppression_isolation (ALL) |
| `relationship_type` | ja | ja | relationship_type_isolation_delete (DELETE), relationship_type_isolation_insert (INSERT), relationship_type_isolation_select (SELECT), relationship_type_isolation_update (UPDATE) |
| `reservation` | ja | ja | reservation_isolation (ALL) |
| `review_item` | ja | ja | review_item_isolation (ALL), review_item_scoped_delete (DELETE), review_item_scoped_select (SELECT), review_item_scoped_update (UPDATE) |
| `role` | ja | ja | role_isolation (ALL) |
| `role_assignment` | ja | ja | role_assignment_isolation (ALL) |
| `role_permission` | ja | ja | role_permission_tenant_isolation (ALL) |
| `room` | ja | ja | room_isolation (ALL) |
| `saved_view` | ja | ja | saved_view_isolation (ALL) |
| `scheduler_run` | ja | ja | scheduler_run_operator (ALL) |
| `search_document` | ja | ja | search_document_scoped_delete (DELETE), search_document_scoped_update (UPDATE), search_document_tenant_isolation (ALL) |
| `security_finding` | ja | ja | security_finding_isolation (ALL), security_finding_scoped_delete (DELETE), security_finding_scoped_update (UPDATE) |
| `service_account` | ja | ja | service_account_isolation (ALL) |
| `service_account_role` | ja | ja | service_account_role_isolation (ALL) |
| `signup_request` | ja | ja | signup_request_operator (ALL) |
| `site` | ja | ja | site_isolation (ALL), site_scoped_delete (DELETE), site_scoped_update (UPDATE) |
| `sla` | ja | ja | sla_scoped_delete (DELETE), sla_scoped_update (UPDATE), sla_tenant_isolation (ALL) |
| `source_priority_policy` | ja | ja | source_priority_policy_isolation (ALL) |
| `stock_movement` | ja | ja | stock_movement_isolation (ALL) |
| `stock_scan` | ja | ja | stock_scan_tenant_isolation (ALL) |
| `stocktake` | ja | ja | stocktake_tenant_isolation (ALL) |
| `subnet` | ja | ja | org_isolation (ALL), subnet_scoped_delete (DELETE), subnet_scoped_update (UPDATE) |
| `tag` | ja | ja | tag_isolation (ALL) |
| `team` | ja | ja | team_tenant_isolation (ALL) |
| `team_member` | ja | ja | team_member_isolation (ALL) |
| `template` | ja | ja | template_isolation_delete (DELETE), template_isolation_insert (INSERT), template_isolation_select (SELECT), template_isolation_update (UPDATE) |
| `template_version` | ja | ja | template_version_isolation_delete (DELETE), template_version_isolation_insert (INSERT), template_version_isolation_select (SELECT), template_version_isolation_update (UPDATE) |
| `template_version_item` | ja | ja | template_version_item_isolation_delete (DELETE), template_version_item_isolation_insert (INSERT), template_version_item_isolation_select (SELECT), template_version_item_isolation_update (UPDATE) |
| `ticket` | ja | ja | ticket_scoped_delete (DELETE), ticket_scoped_update (UPDATE), ticket_tenant_isolation (ALL) |
| `ticket_comment` | ja | ja | ticket_comment_scoped_delete (DELETE), ticket_comment_scoped_update (UPDATE), ticket_comment_tenant_isolation (ALL) |
| `ticket_number_counter` | ja | ja | ticket_number_counter_isolation (ALL) |
| `ticket_sla` | ja | ja | ticket_sla_tenant_isolation (ALL) |
| `training` | ja | ja | training_isolation (ALL) |
| `training_assignment` | ja | ja | training_assignment_isolation (ALL) |
| `user_custom_role` | ja | ja | user_custom_role_isolation (ALL) |
| `user_invitation` | ja | ja | org_isolation (ALL) |
| `vlan` | ja | ja | vlan_isolation (ALL), vlan_scoped_delete (DELETE), vlan_scoped_update (UPDATE) |
| `vrf` | ja | ja | vrf_isolation (ALL), vrf_scoped_delete (DELETE), vrf_scoped_insert (INSERT), vrf_scoped_update (UPDATE) |
| `webhook_dead_letter` | ja | ja | webhook_dead_letter_isolation (ALL), webhook_dead_letter_system_select (SELECT) |
| `webhook_delivery` | ja | ja | webhook_delivery_isolation (ALL), webhook_delivery_system_select (SELECT) |
| `webhook_subscription` | ja | ja | webhook_sub_isolation (ALL), webhook_sub_scope_select (SELECT) |
| `workflow_def` | ja | ja | workflow_def_tenant_isolation (ALL) |
| `workflow_run` | ja | ja | workflow_run_tenant_isolation (ALL) |
| `workflow_step` | ja | ja | workflow_step_tenant_isolation (ALL) |

<!-- schema-baseline:tables:end -->
