# Internes Sicherheitsreview Phase 2 (G2)

WP-329 (`g2-security`, SEC-11). Prüfrahmen: OWASP ASVS Level 2 nach SEC-11.
Grundlagen:

- Threat-Model `docs/security/threat-model.md`, Abschnitt „Phase 2 (G2)“
  (T21 bis T35)
- Vorbild und G1-Stand `docs/security/review-g1.md`
- Befundliste des Audits `docs/audit/befunde.csv`, Bewertungsschema
  `docs/audit/README.md` (Critical: Mandanten- und Scope-Isolation, stiller
  Datenverlust, Secrets/Krypto, Auth-Bypass)
- Scan-Jobs nach `docs/security/abhaengigkeitsscans.md`
- Nachweise der Phase 2: WP-326 (`docs/acceptance/tst-01-phase2.md`), WP-327
  (`docs/acceptance/lasttest-g2.md`), WP-328 und WP-827
  (`docs/acceptance/abn-03-phase2.md`), WP-828 (Kanalmatrix), WP-829
  (Sicherheitstests), WP-730 (Feldrechte-Kanalmatrix)

## Prüfstand

| Punkt | Stand |
|---|---|
| Commit | `f74831fc` (Kopf von PR #149: main nach PR #148 mit WP-831 und WP-832), Release-Standard G2 aus WP-830: P2-Module ohne `RETICORA_MODULES` aktiv, P3, P4 und [A] abgeschaltet |
| CI-Lauf-ID | offen. Dieses WP startet keine Workflows; den Lauf auf dem Release-Kandidaten (Jobs `govulncheck`, `npm-audit`, `image-scan`, `image-scan-thirdparty`) trägt WP-331 nach |
| Release-Kandidat | Ob `f74831fc` der Commit nach dem Merge aller WPs von M2.0, M2.0b und M2.1 bis M2.9 ist, bestätigt WP-331. Kommt danach Code in P2-Pfade, gilt der Abschnitt „Offene Punkte für WP-331“ |
| Lokale Läufe | PostgreSQL 16 ohne TimescaleDB (localpg), Redis 7; siehe Abschnitt „Prüfungen“ |

Geprüft wurden:

- alle Routen der P2-Module aus `LateModules` (`backend/internal/server/modules.go`):
  `inventory`, `documents`, `metamodel`, `import`, `stocktake`, `ticketing`,
  `disposal`, samt dem vorhandenen Phase-1-Code ihrer Pakete
  (`backend/internal/document`, `asset`, `assignment`, `reservation`,
  `movement`, `consumable`, `order`, `stocktake`, `ticket`, `sla`,
  `contract`, `labels`, `importer`, `metamodel`, `disposal`, `lifecycle`,
  `form`);
- die 188 Operationen auf 115 Pfaden mit `x-phase: P2` in `api/openapi.yaml`;
- die P2-Funktionen ohne eigenes Modul nach E-105 (Metamodell, Tags,
  Feldrechte, Lieferanten/Verträge, Import) und die P2-Felder auf G1-Pfaden
  (`computed` an Attributdefinitionen, Formularauflösung, `ci-types/{id}/form`,
  `cis/{id}/form`, GraphQL `ciForm`, `ciHistory`, `cis/{id}/as-of`,
  `assets/{id}/as-of`).

Methode: Für jede Angriffsfläche wurde der Code der Handler, Repositories und
Policies gelesen und gegen die vorhandenen Tests abgeglichen. Vorhandene
Nachweise werden zitiert statt neu geprüft. Wo ein Nachweis fehlte, ist ein
Test ergänzt (as-of und Tombstone eines Assets unter Feldrechten,
`TestAssetValueHistory`). Jede Schwachstelle steht mit Severity und Evidenz im
Abschnitt „Befunde“. Behoben wird in diesem WP nichts (Plan: „Nicht
Bestandteil: die Behebung der Befunde“); Critical und High erhalten ein
Folge-WP.

## Ergebnis

| Severity | offen | behoben |
|---|---|---|
| Critical | 0 | 2 (SR-51, WP-828; SR-56, WP-852) |
| High | 0 | 1 (SR-53) |
| Medium | 0 | 5 (SR-55, SR-57, SR-58, WP-871; SR-59, WP-872; SR-60, WP-873) |
| Low | 0 | 6 (SR-50, WP-879; SR-52, SR-54, SR-61 und SR-62, WP-874; SR-63, WP-875) |
| Info | 3 (SR-64 bis SR-66) | – |

**SR-56 (Critical) ist mit WP-852 behoben (Migration 000281); bis zu dessen Merge sperrte es G2 (GATE-01).**
Ein Dokument, das nur mit Objekten eines Clients verknüpft ist, wird für alle
Clients der Organisation sichtbar, sobald sein letzter Link entfällt: durch
Entlinken eines gescopten Principals oder durch das Löschen des Linkziels.
Die übrigen Angriffsflächen der Phase 2 sind mit Tests belegt. Die
Gate-Entscheidung trifft WP-331.

## Prüfungen

Lokal auf `f74831fc` mit den Änderungen dieses WP:

| Prüfung | Ergebnis |
|---|---|
| `go vet ./...` (backend) | grün |
| `golangci-lint run --config=.golangci.yml ./...` (backend) | 0 Befunde |
| `TestAssetValueHistory` (`backend/internal/asset`, ergänzt) gegen localpg | grün |
| Pakete `asset`, `history`, `document`, `importer`, `labels`, `jobs` gegen localpg | grün bis auf `TestAssetQueryBudget` (braucht TimescaleDB, nur CI) |
| `TestModuleTablesInheritObjectScope`, `TestRLSCatalog`, `TestKnownGaps*` (`tenant/rls`) gegen localpg | grün |
| `bash tests/rep01-structure.test.sh` | grün |
| `bash tests/ci-config.test.sh` | grün |
| `scripts/ci/check-scan-exceptions.sh` | `security/scan-exceptions.txt: 0 entries, 0 problems` |
| `TRACEABILITY_WPS=WP-329 go test ./internal/traceability/` | grün |
| `npm audit --omit=dev` (frontend, lokal) | 0 Critical, 0 High, 2 Moderate (`react-router`, `react-router-dom`; SR-62) → Job `npm-audit` grün |
| `npm audit` (alle, inkl. Entwicklung) | 0 Critical, 0 High, 9 Moderate (nicht blockierend) |
| `govulncheck` | lokal nicht ausführbar (kein Zugang zu `vuln.go.dev`); Lauf in der CI durch WP-331 |
| Trivy (`image-scan`, `image-scan-thirdparty`) | lokal nicht ausführbar; Lauf in der CI durch WP-331 |

Reproduktion von SR-56 (nicht eingecheckt, weil ein Test, der die Lücke
bestätigt, nach der Skip-Regel von WP-829 rot oder übersprungen wäre): Als
App-Rolle legt ein org-weiter Principal ein Dokument mit einem Link auf ein
Asset von Client 2 an; ein Principal von Client 1 sieht es nicht (0 Zeilen).
Nach `DELETE FROM document_link` bzw. `DELETE FROM asset` durch einen
Principal von Client 2 sieht der Principal von Client 1 das Dokument
(1 Zeile). Beide Wege lokal auf `f74831fc` reproduziert.

## ASVS Level 2 – Abgleich je Kapitel

| Kapitel | Umsetzung in G2 | Nachweis | Befunde |
|---|---|---|---|
| V1 Architektur | Threat-Model um Phase 2 erweitert (TB7 bis TB11, T21 bis T35); neue Mandantentabellen mit ENABLE + FORCE RLS, Katalogtest ohne Ausnahmen (`rls.KnownGaps` leer); Verweise auf `ci`, `asset`, `location`, `consumable`, `ticket`, `internal_order`, `app_user` mit Organisation (000194, 000275) | `docs/security/threat-model.md`; `backend/internal/tenant/rls/catalog_integration_test.go` (`TestRLSCatalog`); `backend/internal/tenant/rls/object_reference_integration_test.go` (`TestTenantReferencesCarryOrganization`) | SR-56 |
| V2 Authentisierung | unverändert gegenüber G1 (OIDC mit PKCE, API-Schlüssel); Import-Läufe mit API-Schlüssel laden Schlüssel und Rechte beim Lauf neu | `backend/internal/importer/principal_test.go` (`TestRunReloadsAPIKey`), `backend/internal/importer/apikey_integration_test.go` (`TestImportWithAPIKey`) | SR-62 (Rücksprung nach dem Login) |
| V3 Sessions | unverändert gegenüber G1 | wie G1 (T1) | – |
| V4 Zugriffskontrolle | Rollenmatrix der P2-Rechte nach E-106: jede P2-Route hat genau ein Recht (`route_permissions.txt`), Sonderrechte (`order:approve`, `metamodel:publish`, `field_permission:manage`, `ci_instance_attribute:manage`, Inventurabschluss); Standardrollen nach der Matrix; Kanalmatrix mit 20 P2-Kanälen für jede Scope-Art (WP-828); Cross-Tenant-Verweise wie unbekannte IDs (WP-827); Feldrechte in allen projizierten Kanälen (WP-730); Revert mit dem Recht der Ursprungsoperation | `backend/internal/server/authz_test.go` (`TestEveryOperationRequiresItsExpectedPermission`, `TestSpecialActionsRequireTheirOwnPermission`), `backend/internal/permission/role_matrix_integration_test.go` (`TestStandardRolesMatchTheRoleMatrix`), `backend/internal/tenant/rls/channel_matrix_integration_test.go` (`TestChannelMatrix` mit `channel_matrix_p2_integration_test.go`), `backend/internal/tenant/rls/foreign_refs_p2_integration_test.go` (`TestForeignReferencesP2`), `backend/internal/tenant/rls/fieldrights_matrix_integration_test.go` (`TestFieldRightsChannelMatrix`) | SR-56, SR-58, SR-59, SR-61, SR-63, SR-50 |
| V5 Validierung | OpenAPI-Validierung, typisierte Fehler; Import-Parser mit Grenzen (E-49); Regeln und Ausdrücke als AST nach `api/rules.schema.json` mit Tiefe ≤ 32, Regex nur RE2 ohne Lookaround und Rückverweise, nie SQL; strukturierte Abfrage mit Whitelist und Parametern | `backend/internal/importer/parse_test.go`, `backend/internal/importer/security_test.go`, `backend/internal/rules/rules_test.go`, `backend/internal/savedview/` | – |
| V6 Kryptografie | Signatur-Hash (WP-276): SHA-256 über PNG, Nullbyte und kanonisches JSON der Fachfelder (feste Schlüsselreihenfolge, UTC-Zeit), Verifikation WP-748; das ist eine Integritätsbindung ohne Schlüssel, Manipulationsschutz gegenüber der Datenbank gibt die Audit-Kette. SigV4-Presign: Signatur nur über Bucket, Key, Ablauf (900 s) und die Antwort-Header `response-content-disposition`, `response-content-type` | `backend/internal/assignment/signature_integration_test.go` (`TestSignatureHashVector`), `backend/internal/assignment/signature_verify_integration_test.go` (`TestSignatureVerification`), `backend/internal/platform/blob/presign_test.go` (`TestPresignedGetURLSignsOnlyTheRequestedObject`, `TestPresignedGetURLForcesDownload`) | – |
| V7 Logging | Audit-Kette für alle P2-Schreibwege (Routen-Audit, Domänen-Audit), Scope-Regel je Typ; Import-Läufe, Reverts, Dokument-Links und Unterschriften auditiert | `backend/internal/server/audit_coverage_test.go` (`TestEveryWriteRouteHasASnapshotEntry`), `backend/internal/ticket/ticket_external_ref_integration_test.go` (`TestTicketProviderAudit`) | SR-64 |
| V8 Datenschutz | Feldrechte (RBA-07) für CI, Asset, Vertrag, Ticket und Dokument; Unterzeichner als Surrogat (PII-Vault); Webhooks mit der Feldpolitik des Service-Accounts | `backend/internal/tenant/rls/fieldrights_matrix_integration_test.go` (`TestFieldRightsChannelMatrixObjectKinds`), `backend/internal/assignment/signature_integration_test.go` (`TestSignatureScopeAndPII`) | SR-58, SR-59, SR-64, SR-65 |
| V9 Kommunikation | unverändert gegenüber G1; clamd im internen Netz (Port nicht veröffentlicht) | `deploy/docker-compose/docker-compose.yml` | – |
| V10 Schadcode/Lieferkette | neue Abhängigkeiten der Phase 2 in `govulncheck` bzw. `npm-audit`; neuer Job `image-scan-thirdparty` für clamd | `docs/security/abhaengigkeitsscans.md`, `.github/workflows/ci.yml` | SR-60, SR-62 |
| V11 Geschäftslogik | kein negativer Bestand und keine Überreservierung (`stock_level_on_hand_check`, `stock_level_reserved_check`, Zeilensperren, Nebenläufigkeitsmatrix); Bestellgenehmigung nur durch einen anderen Nutzer als den Antragsteller (`checkFourEyes`, fail-closed ohne Antragsteller), Positionen nur im Entwurf; Inventurabschluss einmalig, bucht Korrekturen in einer Transaktion, danach unveränderlich | `backend/internal/tenant/integrity/phase2_matrix_integration_test.go` (`TestPhase2ConcurrencyMatrix`), `backend/internal/reservation/` (`TestReservationConcurrency`, `TestConsumableReservation`), `backend/internal/order/order_integration_test.go` (`TestOrderFourEyes`, `TestOrderParallelApprove`, `TestOrderDraftOnlyChanges`), `backend/internal/stocktake/complete_integration_test.go` (`TestStocktakeCompletionBooksCorrections`, `TestStocktakeCompletionConcurrency`, `TestStocktakeImmutableTrigger`) | – |
| V12 Dateien | erstmals vollständig geprüft: Magic-Bytes und Whitelist, Größenlimit, Scan fail-closed (E-42), Ersatz-Upload, Key-Bindung, Presign 15 min mit `attachment` und geprüftem Typ, Proxy nur GET auf `exports` und `reticora-documents` mit `nosniff` und Sandbox-CSP; Import-Reports, Export-Jobs und Etiketten-PDFs im Bucket `exports` über denselben Presign; Import-Parser mit Entpackgrenzen | Abschnitt „Upload und Auslieferung“ | SR-56, SR-57 |
| V13 API | Rate-Limits je Principal und Organisation gelten für alle P2-Routen (globale Middleware); Upload-Parallelität je Instanz (503), Import 5.000 Zeilen je Aufruf, Etiketten-Batch begrenzt; Idempotenz an den Principal gebunden; GraphQL mit Komplexitätsbudget auch für die P2-Felder | `backend/internal/middleware/ratelimit_test.go` (`TestRateLimitPerCredential`), `backend/internal/middleware/idempotency_integration_test.go`, `backend/internal/graphqlbff/assets_test.go` (`TestAssetDetailComplexity`), `backend/internal/graphqlbff/ci_aggregates_test.go` (`TestCIAggregatesComplexity`) | SR-50 |
| V14 Konfiguration | Release-Standard G2 (WP-830); Modul- und Feature-Gates je Route (WP-705, WP-706); Startprüfung für clamd und NATS bei eingeschaltetem Modul `documents`, seit WP-853 auch beim leeren Release-Standard | `backend/internal/config/config_test.go` (`TestValidateDocumentScanner`), `backend/internal/entitlement/enforce_integration_test.go` (`TestPhase2RouteGates`) | SR-57, SR-60, SR-61 |

## Prüfung der Angriffsflächen

### Upload und Auslieferung (V12, SEC-09)

- **Magic-Bytes und Whitelist:** `document.VerifyUpload` (`sniff.go`) liest
  den Kopf der Datei und lässt nur die Whitelist zu; der gespeicherte Typ ist
  der geprüfte, eine Polyglot-Datei wird nie `text/html`. Nachweis:
  `sniff_test.go` (WP-268), `content_test.go`
  (`TestUploadRejectsDisallowedMimeType`, `TestUploadEnforcesSizeLimit`,
  WP-030), `security_test.go` (`TestPolyglotIsStoredWithTheVerifiedType`,
  WP-829).
- **Scan-Bypass:** Kein Abruf in `pending_scan`, `infected` und
  `scan_failed` (409 `document-pending-scan` bzw. `document-quarantined`,
  WP-733); ein Ersatz-Upload auf ein freigegebenes Dokument legt eine neue
  Version mit `pending_scan` an. Der Import liest Dokumente nur mit
  `scan_status = clean` (`importer.checkVersion`, `scanRefusal`). Nachweis:
  `scan_status_integration_test.go`, `scan_test.go`,
  `scan_integration_test.go` (WP-734), `clamd_integration_test.go`
  (WP-735), `security_test.go`
  (`TestReplacementUploadOnCleanDocumentIsNotDelivered`).
- **Pfad- und Key-Manipulation:** Der Key entsteht serverseitig
  (`TestCreateIgnoresRequestedStorageKey`), jeder Abruf prüft ihn
  (`TestCheckOrgKey`, `TestOwnsStorageKey`,
  `TestStorageKeyRejectedOutsideOrganization` in `blob_isolation_test.go`;
  `TestPresignOnlyForTheRequestedDocument`).
- **Presign:** `blob.Store.PresignedGetURL` signiert für Dokumente,
  Import-Reports (`GET /imports/{id}/report`), Export-Jobs
  (`GET /api/v1/exports/{id}`), DATEV-Fehlerberichte und Etiketten-PDFs nur
  Bucket und Key der Anfrage, TTL 15 min (`s3PresignTTL`), mit
  `response-content-disposition=attachment` und festem
  `response-content-type` (HTML, SVG und Skripttypen nie). Der Befund aus
  WP-829 ist behoben (SR-53). `frontend/objects-proxy.conf` leitet nur GET
  auf `exports` und `reticora-documents` weiter und setzt
  `Content-Disposition`, `X-Content-Type-Options: nosniff` und
  `Content-Security-Policy: sandbox; default-src 'none'`. Import-Reports und
  Staging-Zeilen der API-Imports liegen im Bucket `exports` und sind damit
  nur per GET mit Signatur erreichbar. Nachweis: `presign_test.go`,
  `public_url_test.go` (`TestPresignedGetURLNamesThePublicOrigin`),
  `frontend/e2e-real/fixtures/setup-real-stack.sh`.
- **Sichtbarkeit von Dokumenten:** SR-56 (Critical, behoben mit WP-852).
- **Betrieb des Scans:** SR-57 (Medium, behoben mit WP-853).

### Feldrechte (V4, V8, RBA-07, E-43)

Kanäle mit Projektion und ihr Nachweis:

| Kanal | Nachweis |
|---|---|
| REST CI, Asset, Vertrag, Ticket, Dokument | `TestFieldRightsChannelMatrixObjectKinds`, `TestTicketFieldRights` (`backend/internal/ticket/attachments_integration_test.go`) |
| GraphQL `ci`, `cis`, `searchEverything`, `ciHistory`, `ciForm`, `asset` | `TestFieldRightsChannelMatrix`, `backend/internal/graphqlbff/fieldrights_test.go` (`TestBFFProjectsCIFields`, `TestBFFFieldsFailClosed`) |
| Suche, strukturierte Abfrage | `TestFieldRightsChannelMatrix`, `backend/internal/search/fieldrights_integration_test.go` (`TestSearchFieldPermissions`) |
| Export CSV, JSON, DATEV, Export-Job | `backend/internal/export/fieldrights_test.go` (`TestExportFieldRightsDATEV`, `TestExportJobFieldRights`) |
| Webhooks (Service-Account, ungebundene Abos) | `TestFieldRightsChannelMatrix` (gespeicherte Zustellungen), `backend/internal/webhook/project.go` fail-closed für Ereignistypen ohne Regel |
| Historie, `cis/{id}/as-of`, Snapshot-Job | `backend/internal/history/fieldrights_integration_test.go` (`TestCIHistoryFieldRightsAgainstPostgres`) |
| Asset-Historie, `assets/{id}/as-of` mit Tombstone | `backend/internal/asset/history_integration_test.go` (`TestAssetValueHistory`): Historie, `state`, seit WP-329 auch `as-of` und der Tombstone eines gelöschten Assets (unbeschränkt mit Werten, beschränkt nur strukturelle Felder) |
| Formular „als Rolle X“ (`ci-types/{id}/form`, `cis/{id}/form`, `ciForm`) | `backend/internal/citype/form_integration_test.go` (`TestResolvedFormFieldRightsAgainstPostgres`): eine fremde Rolle verlangt `citype:manage` (403 bzw. `FORBIDDEN`), die Vorschau schreibt nichts |
| Etiketten-PDF | `labels.BatchItem.subject` mit der Politik des Erstellers (`TestLabelBatch`) |

ETag-Seitenkanal: Der ETag ist die Version des Objekts; eine Änderung eines
unlesbaren Felds erhöht sie, ohne den Wert zu nennen (SR-65, Info).
Audit-Log und Review-Payloads bleiben nach E-43 ohne Projektion (SR-64,
Info). Ableitungen verborgener Werte: SR-58 und SR-59. Die Suche mit
Feldrechten: SR-66 (Info).

### Import und Export (IMP-IO-01, EXP-01)

- **XLSX:** Formelzellen werden über `File.Rows()` als gespeicherter Wert
  gelesen, nie ausgewertet (`TestXLSXFormulaCellsAreNotEvaluated`).
  `UnzipSizeLimit` = 200 MiB begrenzt die entpackte Gesamtgröße,
  `UnzipXMLSizeLimit` = 16 MiB den Speicher je Blatt bzw.
  `sharedStrings.xml`; darüber lagert excelize ins temporäre Verzeichnis aus
  (`TestXLSXLargeSharedStringsStayOutOfMemory`). Bewertung: E-49 legt 50 MB je
  Datei fest; 200 MiB erlauben ein Entpackverhältnis von 4:1 bei voller
  Dateigröße und werden zusätzlich durch das Entpackverhältnis und die
  Zeilengrenze (50.000) begrenzt (`TestXLSXTooComplex`). Der Speicher je
  Import bleibt damit unter 16 MiB je XML-Teil plus Zeilenpuffer; die
  Plattenlast der Auslagerung ist durch 200 MiB begrenzt. Angemessen, kein
  Befund.
- **ZIP-Eintrag größer als angegeben:** fataler Abbruch ohne vollständiges
  Entpacken (`TestXLSXEntryLargerThanDeclaredIsRefused`).
- **Metamodell-Import (`POST /api/v1/metamodel/import`, WP-722):** Eingabe
  nicht vertrauenswürdig; 10 MiB, strikte Dekodierung, Format-Version, jedes
  Item mit dem Recht seiner Ressourcenroute, Feldrechte-Abschnitte werden
  übersprungen, Ergebnis nur ein Entwurf (T28). Kein Befund.
- **Cross-Tenant-IDs:** API-Import (`POST /api/v1/imports` mit `rows`) belegt
  `TestForeignReferencesP2`. Der Dateiimport (WP-775) läuft über denselben
  Zeilenpfad (`importer/run.go`: Parser → Zeilen → Schreiber der Entität mit
  denselben Prüfungen); das gilt als belegt. Zweite Linie sind die
  zusammengesetzten Fremdschlüssel (SR-03, Nachverfolgung G1).
- **Export:** `neutralizeFormula` (F-338) läuft für CSV und DATEV
  (`rowWriter.writeCells`, `formatDATEVCell`, Kopfzeilen und Fehlerbericht von
  DATEV), den Asset-Export (`renderAssetCSV`) und den Import-Fehlerbericht
  (`importer/report.go`, `neutralize`). Der Bulk-Export der CIs (WP-780)
  läuft als Export-Job über `export.JobHandler` und denselben Renderer.
  `csv.NewWriter` kommt in `backend/internal` nur an diesen Stellen vor. Keine
  Lücke.

### Bulk, Revert und Etiketten

- Revert prüft Sichtbarkeit (404), dann das Recht der Ursprungsoperation
  (403), dann den Zustand (409); zweiter Revert, fremde Organisation und
  Replay mit gleichem `Idempotency-Key` sind belegt
  (`TestBulkRevertNegativeCasesAgainstPostgres`, `TestJobAPIRevert`,
  `TestJobRevertAgainstPostgres`, `TestJobAPIHidesJobsOutsideTheScope`).
  Die Idempotenz-Middleware bindet den Schlüssel an Principal und Body
  (`TestIdempotencyIsAtomicAndPrincipalBound`,
  `TestIdempotencyHashesTheWholeBody`).
- Etiketten-Lookup: identische 404 für fremde Organisation, fremden Client
  und unbekannten Code (`TestLabelLookupUniformNotFound`); das PDF erhält
  nur der Ersteller, auch im Job-Center (`TestLabelBatch`).

### Ticketing, SLA, Unterschrift, Bestellungen, DATEV

- Tickets und SLAs unter RLS mit Client- und Standort-Scope
  (`TestTicketRepositoryClientScope`, `TestSLARepositoryClientScope`,
  `TestBusinessCalendarAPIClientScope`, `TestSLABreachCheck`); externe
  Referenzen eindeutig je Organisation (`TestTicketExternalRefScope`). Die
  Standortableitung der Tickets von Assets ist seit WP-828 behoben (SR-51).
- Unterschrift: siehe V6; Unterzeichner als Surrogat, Dokument nach der
  Unterschrift gesperrt (`TestSignatureDocumentLocked`).
- Bestellungen: Vier-Augen-Regel und Zustandsautomat (V11).
- DATEV: Feature-Gate auch im Worker und nach Entzug
  (`TestDATEVGateMatrix`, `TestDATEVJobFailsAfterRevoke`,
  `TestDATEVJobFailsWithModuleOff`, `TestDATEVWorkerChecksInJobScope`).
- `GET /api/v1/tickets/{id}/attachments`: SR-61.

### Entitlement-Umgehung (ENT-06, GQL-04)

`entitlement.(*Service).Middleware` mit `featureRules`
(`backend/internal/entitlement/middleware.go`) ordnet jedes Präfix der
P2-Module einem Feature zu, auch `/api/v1/reservations`,
`/api/v1/stock-movements` (Methodenregeln: GET `assets_basic`, sonst
`stock`, Signatur `signature`), `/api/v1/compositions`, `/api/v1/inventory`
und `/api/v1/slas` (Behebung aus WP-705). Ohne Feature-Key sind nach E-105
nur die Metamodell-Routen (`cmdb_core`, E-44); sie haben das Modul-Gate
`metamodel`. GraphQL prüft Modul und Feature je Resolver
(`authorizeModule`), die Suche blendet P2-Arten ohne Feature aus, der Import
prüft das Feature der Entität (`TestImportAssetModuleDisabled`). `max_cis`
gilt für Import und Bulk-Anlage (`TestPhase2AutomatedWriters`, WP-835, SR-09).
Lücke: SR-61 (Ticket-Anhänge ohne `documents`).

Kanalmatrix (Konvention 11): `rls.Channels` enthält die P2-Objektarten Asset,
Reservierung, Zuweisung, Lieferant, Vertrag, Dokument, Verbrauchsmaterial,
Bestandsbuchung, Bestellung, Inventur, Ticket, Import, Bulk, Etiketten-Lookup,
P2-Suche, DATEV-Export und die GraphQL-Felder `asset`, `ciForm`, `ciHistory`,
`ciMetrics`. Lücken: SR-63.

### Mandantentrennung, RLS und Referenzen

- RLS: Jede neue Tabelle der Phase 2 hat ENABLE + FORCE RLS mit den Regeln
  des Katalogtests; `rls.KnownGaps` ist leer (`TestRLSCatalog`), Indizes
  unter RLS sind leakproof (`TestRLSIndexesAreLeakproof`).
- Cross-Tenant-Referenzen (ABN-03, WP-827): jeder P2-Schreibpfad weist
  fremde Verweise wie unbekannte ab, ohne Existenzorakel; zweite Linie
  000275. Abweichung im Status: SR-50.
- Dokument-Sichtbarkeit nach Wegfall des letzten Links: SR-56.

### SSRF, Injection, Rate Limits, Fehlerausgaben

- **SSRF:** Die Phase 2 bringt keinen neuen ausgehenden Aufruf. Webhooks
  und Benachrichtigungskanäle laufen über den Egress-Client
  (`TestClientBlocksInternalDestinations`, `TestDialerChecksTheResolvedAddress`);
  der Import liest nur hochgeladene Dokumente und JSON-Zeilen, keine URLs;
  die Veröffentlichung eines Metamodell-Entwurfs ruft die Ressourcenrouten
  im Prozess auf (`metamodel.routeApplier`), nicht über das Netz; der
  Ticket-Provider (WP-293) ist intern, Fremdsysteme rufen ihn später erst
  nach dem Commit auf.
- **Injection in Regeln und Ausdrücken:** Regeln, Bedingungen und berechnete
  Attribute sind ein AST (`backend/internal/rules`), ausgewertet in Go; kein
  Pfad baut SQL aus Regeln. Regex nur RE2 (keine Rückverfolgung), Lookaround
  und Rückverweise werden abgewiesen; Ausdrucksbäume höchstens 32 tief.
  Strukturierte Abfrage und gespeicherte Sichten nutzen Whitelist-Spalten und
  Parameter (`savedview.presentationSQL`). Kein Befund.
- **Rate Limits:** global je Credential und Organisation, auch vor der
  Authentisierung (G1, T3); dazu Upload-Parallelität und Zeitlimits je
  Upload (WP-268), Etiketten-Batch höchstens `MaxBatchIDs`.
- **Fehlerausgaben:** Der Architekturtest
  `TestHandlersDoNotEchoRepositoryErrors` deckt jedes Paket ab
  (`handlerErrorAllowlist` mit Begründung je Fundstelle);
  `TestRepositoryErrorsAreSanitized` und `TestScopeRefusalsAreNot5xx` prüfen
  zur Laufzeit. Die P2-Handler geben `err.Error()` nur für Ergebnisse von
  `api.ReadJSON` und Validierungsfunktionen aus. Kein Befund.

## Befunde

Nummerierung SR-50 ff. (SR-12 bis SR-49 in `review-g1.md`). Übernommen sind
die SR-Befunde, die WP-827 (SR-P2-01), WP-828 und WP-829 übergeben haben,
und die Befunde F-1 aus WP-326 mit Sicherheitswirkung; WP-328 hat keinen
SR-Befund übergeben (`tests/integrity-check.sql` ohne Verstoß).

| ID | Severity | Status | Befund | Maßnahme |
|---|---|---|---|---|
| SR-56 | Critical | **behoben** (WP-852, Migration 000281) | **Dokument wird nach Wegfall des letzten Links org-weit sichtbar (Cross-Client-Exposition, TEN-05, TEN-09).** Die Policy von `document` (000064, 000139, 000217) zeigt ein Dokument, wenn es keinen Link hat oder mindestens einen sichtbaren. Die Schreibregel E-09 (kein org-weites Objekt durch gescopte Principals) prüft nur die Anlage (`require_linked_row`, Constraint-Trigger auf INSERT). Entfernt ein Principal von Client 2 den letzten Link eines Dokuments, das nur mit einem Objekt von Client 2 verknüpft ist (`DELETE /api/v1/documents/{id}/links/{link_id}`, `document.PGRepository.UnlinkDocument`, restriktive DELETE-Policy erfüllt), sinkt `link_count` auf 0, und jeder Principal mit `document:read` in jedem Client sieht Metadaten, Versionen und Inhalt (signierte URL). Seit 000275 (WP-827) geschieht dasselbe ohne Absicht: `delete_target_document_links` entfernt beim Löschen eines Assets, Tickets, Standorts, einer Inventur, eines Entsorgungsnachweises oder eines CIs (Retention) die Links; die Ticket-Anhänge eines gelöschten Tickets werden org-weit. Vorher blieb ein verwaister Link stehen und das Dokument für gescopte Principals unsichtbar. Evidenz: lokal reproduziert (Abschnitt „Prüfungen“); `TestModuleTablesInheritObjectScope` (`backend/internal/tenant/rls/module_object_integration_test.go`, Teil „probe_doc_link_count.sql part A“) hält den heutigen Stand fest („the document becomes org-wide again“). Gleiches Muster bei Wartungsfenstern (`ci_count`), Modul `maintenance` ist P3 und in G2 abgeschaltet. | Folge-WP (vorgeschlagen WP-852, `g2-doc-link-scope`, Planungs-PR nach Konvention 18 vor WP-331): Ein Dokument, das Links hatte, wird nicht durch Wegfall der Links org-weit. Vorschlag: Spalte `document.scope_client_id`/`scope_site_id` bzw. Kennzeichen `orphaned` aus dem letzten Link, sichtbar dann nur org-weit; ein gescopter Principal darf den letzten Link nicht entfernen (deferred Constraint-Trigger wie `require_linked_row` auf DELETE); `delete_target_document_links` markiert statt freizugeben. Test als App-Rolle für Entlinken und jedes Linkziel von 000275, HTTP-Lauf in der Kanalmatrix (`document`), `TestModuleTablesInheritObjectScope` angepasst. Gleiches Muster für `maintenance_window` vor dem Freischalten von P3. Umgesetzt mit WP-852: Spalte `document.orphaned` (000281), gesetzt von `count_document_links`, sobald der Zähler auf 0 fällt; `document_tenant_isolation` zeigt ein Dokument ohne Link nur dann allen, wenn es nie Links hatte, ein verwaistes nur org-weiten Principals; Backfill aus `migration_quarantine` (000275) und `audit_log`. Nachweis: `backend/internal/tenant/rls/document_link_scope_integration_test.go` (`TestDocumentStaysScopedWithoutLinks`, Entlinken und jedes löschbare Linkziel), `TestModuleTablesInheritObjectScope` (Teil A umgestellt), Kanalzeile `document-link` in `TestChannelMatrix`. Ein Verweigern des letzten Entlinkens wurde verworfen, weil es die Löschung des Ziels nicht abdeckt. |
| SR-51 | Critical | **behoben** (WP-828, PR #143) | Ticket eines Assets ohne Standort (Nachtrag zu WP-290, WP-392): `derive_ticket_scope` übernahm ohne CI nur den Client des Assets; das Ticket hatte keinen Standort und war über Suche und Liste für jeden Standort-Scope des Clients sichtbar (Scope-Isolation). Gefunden von der Kanalmatrix P2. | Migration 000272 leitet Client und Standort aus dem Asset ab und füllt den Bestand nach. Nachweis: Kanalmatrix `ticket`, `search-p2` (`TestChannelMatrix`). |
| SR-53 | High | **behoben** (`1f065ad5`, PR #144; Befund aus WP-829) | Signierte Downloads wurden nicht als Anhang erzwungen: `S3Store.PresignedGetURL` setzte nur `X-Amz-*`-Parameter, `objects-proxy.conf` weder `Content-Disposition` noch `nosniff`. Eine hochgeladene Polyglot-Datei hätte über `/objects` auf dem Ursprung der SPA gerendert werden können (gespeichertes XSS). | `blob.Download` signiert `response-content-disposition=attachment` und `response-content-type` (nie HTML, SVG, Skripttypen); Proxy setzt `Content-Disposition`, `nosniff`, Sandbox-CSP. Nachweis: `TestPresignedGetURLForcesDownload` (ohne Skip), `TestPresignedGetURLNamesTheDownload`, `TestDownloadIsAnAttachmentWithTheVerifiedType`; T21. |
| SR-55 | Medium | **behoben** (`f4c1b204`, PR #145; F-1 aus WP-326) | Dokumente liefen nicht durch die Feldrechte der Objektart `document`: REST, GraphQL und Ticket-Anhänge zeigten Felder, die die Rolle nicht lesen durfte (RBA-07). | Projektion und Schreibprüfung für `document` (`document/fieldrights.go`), GraphQL-Felder nullable. Nachweis: `backend/internal/document/fieldrights_test.go`, `TestTicketFieldRights`, `TestFieldRightsChannelMatrixObjectKinds`. |
| SR-57 | Medium | **behoben** (WP-853) | **clamd fehlt im Kubernetes-Standard, die Startprüfung greift nicht.** Seit WP-830 ist `documents` ohne `RETICORA_MODULES` aktiv. `deploy/k8s/base` und das Overlay `staging` enthalten die Komponente `clamav` nicht und setzen `RETICORA_CLAMD_ADDR` nicht; nur `overlays/prod` bindet sie ein (Kommentar dort noch „the module itself stays off until RETICORA_MODULES enables it“). `Config.documentScanErrors` prüft clamd und NATS nur, wenn `documentsEnabled()` das Modul ausdrücklich nennt (`documents`, `P2`, `all`), nicht beim leeren Standard. Folge: Uploads bleiben `pending_scan` und enden in `scan_failed`; fail-closed, also kein Scan-Bypass, aber die Funktion SEC-09 ist ohne Hinweis beim Start nicht nutzbar (Verfügbarkeit, V14). | Folge-WP (vorgeschlagen WP-853, `g2-clamd-default`): `documentsEnabled()` wertet den leeren Release-Standard wie `P2`; Komponente `clamav` in `staging` oder Startfehler mit Hinweis; Kommentar in `overlays/prod` korrigieren; Test in `backend/internal/config/config_test.go` und `k8s-manifests`. Umgesetzt mit WP-853: `documentsEnabled()` wertet den leeren Release-Standard wie `server.ParseModules` (documents an), die Startprüfung verlangt dann `RETICORA_CLAMD_ADDR` und `RETICORA_NATS_URL`; `deploy/k8s/base` bindet die Komponente `clamav` ein, `staging` und `prod` erben sie, der Kommentar in `overlays/prod` ist berichtigt. Nachweis: `TestValidateDocumentScanner`, `TestAuditT5K8sRenderedEnvSatisfiesStartCheck` (base, staging, prod; Fixture ohne `RETICORA_CLAMD_ADDR`). Compose startet clamd weiter nur mit dem Profil `documents`. |
| SR-58 | Medium | **behoben** (WP-871, `p3-sr58-computed-fieldrights`) | **Berechnete CI-Attribute legen Asset-Felder an den Feldrechten vorbei offen.** Ein Ausdruck darf `asset.purchase_price`, `asset.serial_number`, `asset.inventory_no` u. a. lesen (`rules.AssetFields`); der Wert wird beim Schreiben bzw. vom Refresh-Job in `ci.attributes` gespeichert (`ci/computed.go`, `loadComputeEnv`) und ist danach für jeden lesbar, der das CI-Attribut lesen darf. Feldrechte des Assets (z. B. `core.purchase_cost` mit Stufe `none` oder sensitiv) und das Modul- bzw. Feature-Gate `assets_basic` gelten dafür nicht. Anlegen verlangt `ci_attribute:manage`, nicht `field_permission:manage`; eine Rolle mit Metamodellrechten kann so Feldrechte unterlaufen. | Behoben mit WP-871: Der Feldrechte-Snapshot führt je berechnetem Attribut die gelesenen Asset-Felder (`permission.ComputedSource`, `attribute_definition.computed`); `projectConfig.readable` und die Listenprüfungen (`CheckListFilter`, `TypeUnreadableKeys`, Export-Manifest) machen das Attribut nur lesbar, wenn es selbst und jedes Quellfeld am verknüpften Asset lesbar ist (ohne bekanntes Asset bei eingeschränkter Asset-Politik unlesbar, sensitive Quelle macht es sensitiv); die Suche wertet Attributzeilen über dieselbe Regel aus. Ohne `assets_basic` ist es für jeden Leser unlesbar und `loadComputeEnv` liest keine Asset-Felder. Anlegen, Ändern und Löschen einer Definition mit Asset-Quellen verlangen zusätzlich `field_permission:manage` (REST, Entwurf, Import); jede Änderung invalidiert die Feldpolitik. Nachweis: `backend/internal/permission/computed_test.go`, `TestComputedAttributeDefinitions`, `TestComputedAttributesWritePath`, `TestFieldRightsChannelMatrix` (Zeile `computed-asset-source`). |
| SR-59 | Medium | **behoben** (WP-872, `p3-sr59-form-oracle`) | **Formularzustände sind ein Orakel für unlesbare Felder.** `citype.FormResolver` wertet Bedingungen der Attribute, Katalogregeln und `visible_when` der Layout-Abschnitte mit allen gespeicherten Werten des CIs aus (`subject.Values`), auch mit Feldern, die die Politik ausblendet (`fieldStates`, `sectionVisible`). `visible`, `required` und `read_only` eines lesbaren Felds bzw. die Sichtbarkeit eines Abschnitts verraten so, ob eine Bedingung über ein verborgenes Feld gilt (`GET /cis/{id}/form`, GraphQL `ciForm`). Die Schreibprüfung vermeidet ein solches Gleichheitsorakel bereits (WP-728). | Behoben mit WP-872: Der Resolver wertet nur die Werte aus, die die Politik des Lesers lesen lässt (`readableValues`, `citype/form_unknown.go`). Eine Katalogregel, Attributbedingung oder `visible_when`, die ein unlesbares Feld referenziert, gilt als unbekannt und wirkt konservativ (Feld sichtbar, nicht pflichtig, nicht `read_only` aufgrund dieser Bedingung, Abschnitt sichtbar); die Vorschau folgt der Politik der Vorschaurolle, ein Leser ohne eingeschränkte Felder erhält das Formular unverändert. Nachweis: `TestFormStatesOverUnreadableFields`, `TestResolvedFormFieldRightsAgainstPostgres` (zwei Werte des unlesbaren Felds ergeben dasselbe Formular), `TestFieldRightsChannelMatrix/ci-form` (REST und GraphQL `ciForm` gleich). |
| SR-60 | Medium | **behoben** (WP-873, `p3-sr60-thirdparty-pins`) | **Fremdimages des Betriebs ohne Digest und ohne Scan.** `nats:2.10-alpine`, `redis:7-alpine`, `postgres:16-alpine` (`keycloak-db`), `quay.io/keycloak/keycloak:24.0`, `opensearchproject/opensearch:2.13.0` und `certbot/certbot:v2.11.0` (`deploy/docker-compose/docker-compose.yml`, teils `deploy/k8s/base/components`) sind gleitende Tags ohne Digest und laufen in keinem Scan. Keycloak ist der Identity Provider (T1); die Major-Version 24 wird vom Hersteller nicht mehr gepflegt. SEC-11 verlangt den Container-Scan mit CVE-SLA; die begründeten Ausnahmen stehen in `docs/security/abhaengigkeitsscans.md`. | Behoben mit WP-873: Jede Image-Referenz des Betriebs in `deploy/docker-compose/docker-compose.yml` und `deploy/k8s/base/components/**` trägt Version und Digest (nats 2.14.9, redis 7.4.12, postgres 16.15, keycloak 26.8.2, opensearch 2.19.7, certbot v5.9.0; minio, clamd und timescaledb wie bisher); Compose und Kubernetes nennen dieselbe Referenz. Keycloak läuft auf der gepflegten Major-Version 26 (`KC_PROXY_HEADERS` statt `KC_PROXY`, Bootstrap-Admin, Health auf Port 9000); `tests/keycloak-realm-upgrade.test.sh` führt Schritt 1 auf Keycloak 24 und die folgenden auf Keycloak 26 mit derselben Datenbank aus. `image-scan-thirdparty` scannt alle neun Fremdimages, der Abgleich liest Deployment, StatefulSet und CronJob. Nachweis: `tests/ci-config.test.sh` (`thirdparty_image_pins` mit Gegenprobe), Job `image-scan-thirdparty`, `install-smoke.yml`. |
| SR-50 | Low | **behoben** (WP-879, `p3-sr50-foreign-ref-422`) | Einige Pfade antworten auf einen fremden Verweis im Body mit 404 statt 422 (Matrixzeile `reference`): `POST /api/v1/reservations` (`asset_id`, `consumable_id`, `location_id`), `POST /api/v1/stock-movements` (`consumable_id`, `asset_id`), `POST /api/v1/assets/{id}/cis` (`ci_id`), `POST /api/v1/documents/{id}/links` (fremdes Ziel), `POST /api/v1/tickets/{id}/links` (fremdes Objekt). Fremder und unbekannter Verweis antworten gleich; kein Existenzorakel, keine Exposition. Abweichung vom API-Vertrag (API-03), keine Sicherheitslücke. | Behoben mit WP-879: Reservierungen (`asset_id`, `consumable_id`, `location_id`), Lagerbewegungen (`asset_id`, `consumable_id`), Asset-CI-Link (`ci_id`) sowie Dokument- und Ticket-Links (`entity_id`) antworten auf einen fremden und einen unbekannten Verweis im Body gleich mit 422 und Feldfehler; das Objekt im Pfad bleibt 404 (eigene Fehlerwerte `document.ErrTargetNotFound`, `ticket.ErrLinkTargetNotFound`). Nachweis: `TestForeignReferencesP2` ohne Abweichungen. |
| SR-52 | Low | **behoben** (`9d855679`, PR #144; aus WP-828) | `GET /api/v1/imports/{id}` zeigte einen Import über JSON-Zeilen nur seinem Starter, `GET /api/v1/jobs/{id}` denselben Job jedem Principal, dessen Scope den Snapshot deckte (keine Org- oder Client-Grenze). Ebenso Etiketten-Batches im Job-Center. | Lesebedingung je Job-Art (`jobs.PGRepository.WithReadCondition`, `importer.JobReadSQL`, `labels.JobReadSQL`). Nachweis: Kanalmatrix `job`, `import`; `TestLabelBatch`. |
| SR-54 | Low | **behoben** (`9d855679`, PR #144; aus WP-829) | `POST /api/v1/jobs/{id}/revert` prüfte den Zustand vor dem Recht: ohne Recht 409 statt 403, ein Orakel über den Zustand fremder Bulk-Jobs derselben Organisation. | Reihenfolge Sichtbarkeit (404), Recht (403), Zustand (409). Nachweis: `TestBulkRevertNegativeCasesAgainstPostgres`, `TestJobAPIBulkRevertNeedsKindPermission`. |
| SR-61 | Low | **behoben** (WP-874, `p3-sr61-62`) | `GET /api/v1/tickets/{id}/attachments` liefert Dokument-Metadaten (projiziert, aber Name, Typ, Größe) mit dem Feature `ticketing_essential` allein (`featureRules`, Präfix `/api/v1/tickets`) und auch bei abgeschaltetem Modul `documents`; der GraphQL-Weg prüft dagegen Modul und Feature `documents` (`authorizeModule`, GQL-04). Wirkung nur nach Entzug des Features `documents` bei vorhandenen Dokumenten; der Inhalt bleibt hinter `/api/v1/documents/{id}/content` gesperrt. | Behoben mit WP-874: Suffixregel `/api/v1/tickets` + `/attachments` mit `ticketing_essential` und `documents` (`entitlement/middleware.go`); ohne `documents` 403 `feature-not-entitled`. Ist das Release-Modul `documents` aus, erhält der Ticket-Handler kein Dokument-Repository und registriert die Route nicht (404). Nachweis: `TestPhase2RouteGates`, `TestTicketAttachments`. |
| SR-62 | Low | **behoben** (WP-874, `p3-sr61-62`; Restrisiko nach E-245) | Rücksprung nach dem Login: `startAuthorizationCodeFlow` (`frontend/src/auth/oidc.ts`) akzeptiert jedes `returnTo`, das mit `/` beginnt, auch `//host` und `/\host`; `CallbackPage` navigiert damit. `npm audit --omit=dev` meldet für `react-router`/`react-router-dom` 6.30.x zwei Moderate-Advisories zu Open Redirects über Backslash-Pfade (GHSA-wrjc-x8rr-h8h6, GHSA-jjmj-jmhj-qwj2; nicht blockierend). `pushState` verweigert fremde Ursprünge; ausnutzbar wäre der Weg nur über eine Navigation als Dokument. | Behoben mit WP-874: `safeReturnTo` (`frontend/src/auth/oidc.ts`) übernimmt `returnTo` nur als lokalen Pfad mit genau einem führenden `/`, ohne `//` und Backslash (auch prozentkodiert) und mit eigenem Ursprung, sonst `/dashboard`; der Callback prüft das gespeicherte Ziel erneut. `react-router-dom` 6.30.6 schließt GHSA-jjmj-jmhj-qwj2. Restrisiko ([E-245](../plan/phase3/entscheidungen.md#e-245)): GHSA-wrjc-x8rr-h8h6 (Backslash in `<Link>`/`useNavigate`) und GHSA-337j-9hxr-rhxg (nur SSR) sind erst in React Router 7.18 behoben (Moderate); der einzige Navigationspfad mit Eingaben von außen ist `returnTo` und durch `safeReturnTo` geschlossen; die Migration auf React Router 7 folgt als eigenes WP. Nachweis: `frontend/src/auth/oidc.test.ts`. |
| SR-63 | Low | **behoben** (WP-875, `p3-sr63-channel-rows`) | Kanalmatrix (Konvention 11) ohne eigene Zeilen für Entsorgungsnachweise, SLAs und Geschäftskalender, Ticket-Anhänge und Etiketten-Batches sowie für das Entlinken von Dokumenten. Ihre Scope-Tests liegen auf Repository- bzw. Paketebene (`TestDisposalRepositoryClientScope`, `TestSLARepositoryClientScope`, `TestBusinessCalendarAPIClientScope`, `TestTicketAttachments`, `TestLabelBatch`); einen HTTP-Lauf je Scope-Art haben sie nicht. Die fehlende Zeile für das Entlinken verdeckte SR-56. | Behoben mit WP-875: `rls.Channels` führt die Zeilen `disposal`, `sla` (mit `GET /api/v1/slas/breaches`), `business-calendar`, `ticket-attachment` und `label-batch`; `TestChannelMatrix` liest und schreibt sie über HTTP für jede Scope-Art aus `rls.ScopeKinds` (Zeilenfunktionen in `channel_matrix_sr63_integration_test.go`, Seed je Client und Standort). Ergebnis: keine Exposition; Objekte außerhalb des Scopes antworten wie unbekannte (404, bzw. 400 `client_id` bei Kalendern), Schreiben mit fremdem Client oder Asset wird abgewiesen, die andere Organisation sieht nichts; ein Etiketten-Batch druckt nur die sichtbaren Assets und ist nur für seinen Ersteller abrufbar. |
| SR-64 | Info | akzeptiert (E-43) | Audit-Log (`/api/v1/audit-log`, `audit:read`), Review-Payloads und Konfliktliste der Reconciliation bleiben ohne Feldrechte-Projektion; `audit:read` legt alle Feldwerte offen (Entscheidung E-43, A-30). | Keine; Rollenvergabe von `audit:read` ist Betriebsentscheidung. |
| SR-65 | Info | akzeptiert | ETag und `version` bzw. `updated_at` eines Objekts ändern sich auch, wenn sich nur ein für den Leser unlesbares Feld ändert. Der Leser erfährt, dass sich etwas geändert hat, nicht was; die projizierte Historie nennt das Feld nicht. | Keine. |
| SR-66 | Info | Stand dokumentiert | Suche mit Feldrechten: Auf `f74831fc` filtert `restrictedQuery` (`backend/internal/search/pg_repository.go`) Treffer eingeschränkter Typen nach dem lesbaren Text und liest die Feldzeilen `search_document_field` einmal je Anfrage; belegt durch `TestSearchFieldPermissions` und `TestFieldRightsChannelMatrix` (Suche, strukturierte Abfrage, `searchEverything`). Ein Umbau mit Sicherheitsspalten direkt in der Tabelle ist in Arbeit. | WP-331 wiederholt beide Tests auf dem Release-Kandidaten, falls der Umbau vorher gemergt wird. |

## Folge-WPs

Critical und High erhalten ein Folge-WP. Es wird nach Konvention 18 als
Planungs-PR ohne WP-Nummer in `docs/plan/phase2/implementierungsplan.md`
eingebracht und vor WP-331 gemergt. Die Nummern sind Vorschläge; der
Planungs-PR vergibt sie.

| Vorschlag | Schlüssel | Befunde | Pflicht vor G2 |
|---|---|---|---|
| WP-852 | `g2-doc-link-scope` | SR-56 (Critical), Zeile `document-link` aus SR-63 | ja (GATE-01) |
| WP-853 | `g2-clamd-default` | SR-57 (Medium) | nein, empfohlen vor G2 |
| WP-854 | `g2-fieldrights-derived` | SR-58, SR-59 (Medium) | nein, vor GA |
| WP-855 | `g2-thirdparty-pins` | SR-60 (Medium) | nein, vor GA |

## Nachverfolgung G1

Status der offenen G1-Befunde aus `docs/security/review-g1.md` (`:433–438`):

| ID | Severity | Status | Nachweis bzw. Folge-WP |
|---|---|---|---|
| SR-03 | Medium | **behoben** (WP-876, WP-877) | WP-838 (PR #113, Migration 000194) stellt die Verweise der Phase-2-Module auf `ci`, `asset`, `location` und `consumable` um; WP-827 (PR #145, Migration 000275) die Verweise von Reservierung, Bestand, Ticket-Link, Bestellposition und Inventurbereich. Katalogtest `TestTenantReferencesCarryOrganization`. Cross-Tenant-IDs im Datei- und API-Import: `TestForeignReferencesP2` (Abschnitt „Import und Export“). Rest: 129 Verweise in `rls.ReferencesWithoutOrganization`, Termin vor GA mit WPs einer späteren Phase (E-137, Option 2); blockiert G2 nicht. Nachtrag WP-876 (Migration 000287): 59 Verweise der Kern-, Standort-, Netz-, Rack-, Rollen- und Discovery-Tabellen zusammengesetzt, Nachweis `TestTenantReferencesRest1`; Nachtrag WP-877 (Migration 000288): die übrigen 55 Verweise zusammengesetzt, `rls.ReferencesWithoutOrganization` leer; Nachweis `TestTenantReferencesRest2`. |
| SR-04 | Medium | **behoben** | MAC- und Attributklasse mit WP-434 (PR #93) und WP-435 (PR #93); `idx_ci_fts` und Katalogtest mit WP-836 (PR #113); Volltext und `UniqueConflicts` nach E-133 als dokumentierter Scan. |
| SR-06 | Info | offen | Dedizierter Referenzhost für den Lastnachweis; bleibt nach E-58 bis vor GA offen. |
| SR-08 | Low | **behoben** | WP-834 (PR #113), Migration 000193; Kontrollmessung in `docs/acceptance/lasttest-g1.md`. |
| SR-09 | Low | **behoben** | WP-835 (PR #113). Bewertung bei der Entitlement-Umgehung: `max_cis` gilt für REST, Import und Bulk-Anlage unter der Zulassungssperre (`TestPhase2AutomatedWriters`); Rest wie in G1 (HTTP-Ingest ohne Queue mit einer Spur). |
| SR-10 | Low | **behoben** (WP-878) | Obergrenze von 256 Identitätswerten je Record im Vertrag; ein Record darüber wird einzeln verworfen (`rejected_identity_limit`). Nachweis `TestIngestRejectsRecordOverIdentityCap`. |
| SR-11 | High | **behoben** | WP-418 (PR #91). |

Externer Pentest: Für G1 gilt die Erklärung des Auftraggebers vom 2026-10-15
(`docs/security/pentest-g1.md`) als Nachweis; nach E-103 gilt sie auch für
G2. Eine Befundliste wird nicht nachgereicht. Ein externer Pentest für G2 ist
nicht gefordert (SEC-11 verlangt ihn vor G1 und GA; E-28 betrifft WP-210).

## Anhang: Critical/High-Befunde

### Nach E-03 verschobene [P2]-Befunde

Die 25 in `review-g1.md` als „verschoben (E-03)“ oder „G1-Anteil
geschlossen, Rest verschoben (E-03)“ geführten [P2]-Befunde mit dem
ersetzenden Phase-2-WP nach der Tabelle „Verschobene Arbeitspakete“ (E-46).
„geschlossen“ heißt: jedes ersetzende WP ist auf `f74831fc` mit einem Commit
„WP-nnn:“ gemergt, und der Test seiner Traceability-Zeile liegt vor.

| ID | Tag | Severity | Status | WPs | Nachweis |
|---|---|---|---|---|---|
| AST-01 | [P2] | Critical | geschlossen | G1: WP-017, WP-023; Phase 2: WP-271, WP-272 | `backend/internal/asset/asset_integration_test.go`, `backend/internal/asset/link_integration_test.go` |
| AST-05 | [P2] | Critical | geschlossen | G1: WP-052, WP-079; Phase 2: WP-273 | `backend/internal/ci/composition_integration_test.go` |
| MGT-02 | [P2] | Critical | geschlossen | G1: WP-028; Phase 2: WP-274, WP-278 | `backend/internal/disposal/disposal_tx_integration_test.go`, `backend/internal/movement/service_integration_test.go` |
| SRC-03 | [P2]; Textsyntax [O] | Critical | geschlossen | G1: WP-034, WP-187; Phase 2: WP-315 | `frontend/src/components/rules/RuleBuilder.test.tsx` |
| AST-02 | [P2] | High | geschlossen | WP-275, WP-277 | `backend/internal/assignment/transfer_integration_test.go` |
| AST-04 | [P2] | High | geschlossen | G1: WP-077; Phase 2: WP-272, WP-273 | `backend/internal/asset/link_integration_test.go`, `backend/internal/ci/composition_integration_test.go` |
| AST-06 | [P2] | High | geschlossen | G1: WP-022; Phase 2: WP-275, WP-277 | `backend/internal/reservation/concurrency_integration_test.go` |
| AST-07 | [P2] | High | geschlossen | WP-274, WP-278 (dazu WP-275) | `backend/internal/movement/service_integration_test.go`, `backend/internal/assignment/transfer_integration_test.go` |
| IMP-08 | [P2] | High | geschlossen | WP-297 | `backend/internal/topology/blast_test.go` |
| INV-01 | [P2] | High | geschlossen | WP-287, WP-289 | `backend/internal/stocktake/stocktake_scope_integration_test.go` |
| INV-03 | [P2]; RFID/Direct-Access [P4] | High | geschlossen (P2-Anteil) | WP-287, WP-289 | `backend/internal/locations/barcode_integration_test.go` |
| LCY-02 | [P2] | High | geschlossen | WP-263, WP-279 | `backend/internal/lifecycle/model_integration_test.go` |
| LCY-03 | [P2] | High | geschlossen | WP-263, WP-279 | `backend/internal/lifecycle/guards_integration_test.go` |
| NET-09 | [P2] | High | geschlossen | WP-299 | `backend/internal/ipam/reservation_integration_test.go` |
| OVR-03 | [P2] | High | geschlossen | WP-306 | `backend/internal/ci/bulk_override_integration_test.go` |
| REL-09 | [P2] | High | geschlossen | WP-296 | `backend/internal/relationship/history_integration_test.go` |
| STK-01 | [P2] | High | geschlossen | WP-283, WP-284, WP-285 | `backend/internal/consumable/stock_integration_test.go`, `backend/internal/consumable/minlevel_integration_test.go` |
| STK-02 | [P2] | High | geschlossen | WP-283, WP-284, WP-285 | `backend/internal/consumable/stock_integration_test.go` |
| STK-03 | [P2] | High | geschlossen | WP-283, WP-284, WP-285 | `backend/internal/consumable/stock_integration_test.go`, `backend/internal/reservation/consumable_reservation_integration_test.go` |
| STK-04 | [P2] | High | geschlossen | WP-274, WP-278 | `backend/internal/movement/service_integration_test.go` |
| STK-05 | [P2] | High | geschlossen | WP-286 | `backend/internal/order/order_integration_test.go` |
| TKT-02 | [P2] | High | geschlossen | WP-292 | `backend/internal/sla/sla_integration_test.go` |
| UI-11 | [P2] | High | geschlossen | WP-313, WP-314, WP-315, WP-316 | `frontend/src/pages/CITypeAdminPage.test.tsx`, `frontend/src/pages/TemplatesPage.test.tsx`, `frontend/src/pages/FormBuilderPage.test.tsx` |
| UI-14 | [P2–P4, A] | High | geschlossen (P2-Anteil); P3, P4 und [A] späteren Phasen zugeordnet | WP-319, WP-320, WP-321, WP-322 | `frontend/src/pages/AssetListPage.test.tsx`, `frontend/src/pages/ConsumableDetailPage.test.tsx`, `frontend/src/pages/DocumentDetailPage.test.tsx`, `frontend/src/pages/ImportPage.test.tsx` |
| UI-15 | [P2] | High | geschlossen | WP-318 | `frontend/src/components/pickers/pickers.test.tsx` |

### Teil 03: Datenmodell, Standorte, Metamodell (WP-832)

Befunde aus Teil 03 des Audits zu P2-IDs mit dem Stand ihrer Konsolidierung
durch WP-832 (`docs/audit/befunde.csv`, Anhang von `review-g1.md`).

| ID | Tag | Severity | Status | WPs | Nachweis |
|---|---|---|---|---|---|
| MET-14 | P2/G2 | Critical | geschlossen | G1-Anteil: WP-045, WP-056; Rest Phase 2: WP-256, WP-714 | Begründung des Critical geprüft: (1) Instanzdefinitionen verlangen `ci_instance_attribute:manage` statt `ci:write` (WP-045, `route_permissions.txt`: `PUT`/`DELETE /api/v1/cis/{id}/field-definitions`; `TestSpecialActionsRequireTheirOwnPermission`); (2) der Rang `manual` = 100 in der fail-closed Schreibentscheidung schützt manuelle und Instanzwerte vor Discovery (WP-058, `backend/internal/override/decide.go`; `backend/internal/discovery/instance_field_test.go`). Behoben. Phase 2: `backend/internal/ci/instance_integration_test.go`, `backend/internal/ci/instance_lifecycle_integration_test.go`. Metamodell-Entwürfe können keine Instanzdefinition enthalten (`metamodel.ItemKinds`). |
| DB-05 | nicht geliefert; CH12/PRI-10 | High | G1- und P2-Anteil geschlossen; P3/P4 späteren Phasen zugeordnet (E-36) | wie `review-g1.md`; Phase 2: WP-271, WP-280, WP-737 | `backend/internal/asset/asset_integration_test.go`, `backend/internal/ci/owner_integration_test.go`, `backend/internal/contract/reference_integration_test.go` |
| LOC-08 | P2/G2 | High | geschlossen | WP-026, WP-053, WP-282 | `backend/internal/locations/loc08_integration_test.go` |
| MET-01 | B/G1; Teil P2/G2 | High | geschlossen | WP-115; Phase 2: WP-248, WP-263 | `backend/internal/citype/inherit_integration_test.go`, `backend/internal/lifecycle/model_integration_test.go` |
| MET-10 | B/G1; Teil P2/G2 | High | geschlossen | WP-116, WP-589; Phase 2: WP-264 | `backend/internal/permission/fields_integration_test.go` |
| MET-11 | B/G1; Teil P2/G2 | High | geschlossen | WP-117, WP-501; Phase 2: WP-254, WP-737 | `backend/internal/ci/reference_integration_test.go`, `backend/internal/contract/reference_integration_test.go` |
| MET-12 | P2/G2 | High | geschlossen | WP-254, WP-713, WP-737 | `backend/internal/ci/reference_integration_test.go`, `backend/internal/ci/reference_view_integration_test.go` |
| MET-16 | B/G1; Teil P2/G2 | High | geschlossen | WP-119, WP-590; Phase 2: WP-252 | `backend/internal/citype/compose_integration_test.go` |
| MET-45 | Phase 1 im Fokus | High | geschlossen | WP-118, WP-589; Phase 2: WP-715, WP-326 | `backend/internal/rules/rules_test.go`, `backend/internal/citype/inherit_test.go` |
| MET-51 | P2/G2 | High | geschlossen | WP-804, WP-317 | `frontend/src/pages/CIFormModal.test.tsx`, `frontend/src/components/create/GuidedCreate.test.tsx` |
| MET-54 | B/G1; Teil P2/G2 | High | geschlossen | WP-134; Phase 2: WP-262, WP-722, WP-726 | `backend/internal/citype/export_v2_integration_test.go`, `backend/internal/metamodel/import_roundtrip_integration_test.go`, `backend/internal/citype/export_grants_test.go` |

Offene High-Befunde aus diesem Anhang: keine. GATE-02 wertet den Status der
IDs aus.

## Offene Punkte für WP-331

- SR-56 (Critical): mit WP-852 behoben (eingeplant per Planungs-PR nach
  Konvention 18); GATE-01 für G2 ist erfüllbar, sobald WP-852 gemergt ist.
- CI-Lauf-IDs auf dem Release-Kandidaten nachtragen: `govulncheck`
  (Matrix `backend`, `collector`, `edgecore`), `npm-audit`, `image-scan`,
  `image-scan-thirdparty` (neu, erster Lauf), dazu Commit-Hash des
  Release-Kandidaten, falls er von `f74831fc` abweicht.
- `tests/rep01-structure.test.sh` um die Auswertung von SR-50 ff. erweitern
  (GATE-01, laut Plan Teil von WP-331).
- Bei Merge des Umbaus der Suche vor dem Release-Kandidaten
  `TestSearchFieldPermissions` und `TestFieldRightsChannelMatrix` erneut
  laufen lassen (SR-66).
