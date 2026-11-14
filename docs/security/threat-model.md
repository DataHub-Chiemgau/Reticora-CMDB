# Threat-Model Phase 1 bis 3 (G1, G2, G3)

Grundlage der internen Sicherheitsreviews: G1 mit WP-209
(`docs/security/review-g1.md`), G2 mit WP-329 (`docs/security/review-phase2.md`).
Methode: STRIDE je Vertrauensgrenze. Jede Bedrohung verweist auf die
Anforderung, die sie abwehrt, und auf den Test, der die Abwehr belegt.

Umfang G1 (T1 bis T20): das G1-Release. Spätphasen-Module (P2+, Add-ons) waren
nach E-03 über WP-214 abgeschaltet; ihre Routen waren nicht registriert und
gehörten nicht zur Angriffsfläche. Umfang G2 (T21 ff., Abschnitt „Phase 2
(G2)“): der Release-Standard G2 aus WP-830. Die P2-Module aus `LateModules`
(`backend/internal/server/modules.go`) sind ohne `RETICORA_MODULES` aktiv,
dazu die P2-Funktionen ohne eigenes Modul nach E-105 (Metamodell, Tags,
Feldrechte, Lieferanten/Verträge, Import). P3, P4 und [A] bleiben
abgeschaltet.

## Systemgrenzen und Vertrauenszonen

```
Browser (SPA) ──TLS──▶ nginx ──▶ API-Server (REST, BFF /bff/graphql)
                                    │  WithTenant: app.org_id, Client/Site/Team-Scope
                                    ├──▶ PostgreSQL (RLS FORCE, App-Rolle ohne BYPASSRLS)
                                    ├──▶ Redis (Sessions, Rate-Limit, Idempotenz)
                                    ├──▶ NATS (ingest.>, Events, Collector-Edge mit mTLS)
                                    ├──▶ Keycloak (OIDC, Operator-MFA)
                                    ├──▶ Objektspeicher (Export-Jobs)
                                    └──▶ Egress: Webhooks, Benachrichtigungen (SEC-08)
Collector (Kundennetz) ──mTLS/NATS──▶ Collector-Edge ──▶ Ingest-Pipeline
Operator ──OIDC+MFA──▶ /api/v1/admin (eigene Audit-Kette)
```

Vertrauensgrenzen:

- **TB1:** Internet ↔ API, also Browser, API-Schlüssel und Self-Signup.
- **TB2:** Mandant ↔ Mandant. Organisationen und Clients, Sites und Teams
  innerhalb einer Organisation.
- **TB3:** Collector im Kundennetz ↔ Plattform.
- **TB4:** Plattform ↔ ausgehende Ziele (Webhooks, SMTP).
- **TB5:** Operator ↔ Mandantendaten.
- **TB6:** Anwendung ↔ Datenbank (Rolle, Migrationen, Laufzeit-DDL).

## Bedrohungen und Gegenmaßnahmen

| # | Grenze | STRIDE | Bedrohung | Gegenmaßnahme | Anforderung | Nachweis |
|---|---|---|---|---|---|---|
| T1 | TB1 | S | Gefälschtes oder abgelaufenes Session-Token | RS256-JWT mit `kid`, 15 min, Rotation mit Überlappung; Dev-Login nur in development | AUT-02, AUT-03, SEC-06 | `backend/internal/identity/session_test.go`, `backend/internal/identity/revocation_integration_test.go`, `backend/internal/middleware/auth_verifier_test.go` |
| T2 | TB1 | S | Gestohlener API-Schlüssel | Präfix-Lookup, SHA-256-Vergleich in konstanter Zeit, Ablauf, Widerruf; Rechte = Schnitt mit dem Besitzer | AUT-04 | `backend/internal/identity/apikey_integration_test.go` |
| T3 | TB1 | D | Überlast oder Brute Force | Rate-Limit je Principal und je Organisation sowie vor der Authentisierung; Captcha und IP-Limit beim Signup | API-04, TLC-01 | `backend/internal/middleware/ratelimit_test.go`, `backend/internal/signup/protect_test.go` |
| T4 | TB1 | T | Doppelte oder manipulierte Schreibanfragen | Idempotency-Key an Principal gebunden, atomar; If-Match/Versionen | API-03, API-07 | `backend/internal/middleware/idempotency_integration_test.go`, `backend/internal/ci/concurrency_integration_test.go` |
| T5 | TB1 | D | Teure GraphQL-Abfragen | Tiefe 10, Komplexitätsbudget, Timeout, keine Introspection in production, nur lesend | SEC-10, GQL-01 | `backend/internal/graphqlbff/graphqlbff_test.go` |
| T6 | TB2 | I | Lesen fremder Mandantendaten über irgendeinen Kanal | RLS ENABLE+FORCE auf allen Mandantentabellen; Kanalmatrix REST/GraphQL/Suche/Export/Topologie/Impact/Standorte/Reviews/Webhooks/Benachrichtigungen/Jobs/Ingest | TEN-05, TEN-09 | `backend/internal/tenant/rls/matrix_integration_test.go`, `backend/internal/tenant/rls/channel_matrix_integration_test.go` |
| T7 | TB2 | T/E | Schreiben außerhalb des eigenen Scopes oder Verweis auf fremde Objekte | WITH-CHECK-Policies; Verweise auf Clients mit Organisation im Fremdschlüssel (Migration 000136); Anwendung prüft referenzierte Objekte unter RLS | TEN-05 | `backend/internal/tenant/rls/channel_matrix_integration_test.go` (Abschnitt Fremdverweise), `TestClientReferencesCarryOrganization` |
| T8 | TB2 | I | Impact- und Topologie-Graph führt über unsichtbare Knoten | Traversierung nur über sichtbare, nicht gelöschte CIs | IMP-07 | `backend/internal/topology/scope_integration_test.go` |
| T9 | TB2 | E | Hintergrundjobs ohne Mandantenkontext | System-Worker iterieren je Organisation mit eigenem Kontext | TEN-06 | `backend/internal/tenant/rls/system_worker_integration_test.go` |
| T10 | TB3 | S | Fremder Collector liefert Daten | Enrollment-Code einmalig; mTLS-Zertifikat der internen CA; NATS-Berechtigung je Collector-Subject; Erneuerung vor Ablauf | COL-04, COL-08 | `backend/internal/discovery/enroll_integration_test.go`, `backend/internal/discovery/natsedge_integration_test.go`, `backend/internal/discovery/ca_test.go`, `edgecore/transport/transport_test.go` |
| T11 | TB3 | T | Manipulierte Discovery-Daten überschreiben manuelle Werte | Zentrale Schreibentscheidung, Overrides, Rangfolge, observed_at, Zukunftsgrenze | REC-03, OVR-01, GLO-12 | `backend/internal/discovery/observed_at_integration_test.go`, `backend/internal/discovery/concurrency_integration_test.go` |
| T12 | TB3 | D | Ingest-Flut | Queue (`ingest.>`) entkoppelt die Annahme; Dedupe-Ledger; Lizenzgrenzen (unlicensed_ci); Identitätssperren der Spuren mit höchstens 256 Schlüsseln je Transaktion, in eigenen Lock-Klassen und mit Wartegrenze 20 s, sodass gemeldete Werte keine fremde Sperre treffen (WP-334) | REC-01, ENT-05 | `backend/internal/discovery/consumer_integration_test.go`, `backend/internal/entitlement/unlicensed_integration_test.go`, `backend/internal/discovery/lanes_integration_test.go` |
| T13 | TB4 | I/E | SSRF über Webhook- oder Benachrichtigungs-URLs | Egress-Client blockiert private, link-local und Metadata-Ziele (DNS-Rebinding-fest) | SEC-08 | `backend/internal/platform/egress/egress_test.go` |
| T14 | TB4 | S | Gefälschte Webhook-Zustellungen beim Empfänger | HMAC-Signatur, Secret-Rotation mit Überlappung | API-10 | `backend/internal/server/e2e_scenarios_integration_test.go` (`TestIngestToWebhook`) |
| T15 | TB5 | E/R | Missbrauch des Operator-Zugriffs | Keycloak-Gruppe `operators` mit MFA; Break-glass-Token; eigene Audit-Kette `operator_audit` | SEC-07 | `backend/internal/operator/operator_test.go`, `backend/internal/operator/audit_integration_test.go` |
| T16 | alle | R | Abstreiten oder Manipulieren von Änderungen | Hash-verkettetes Audit-Log, append-only auch für die Laufzeitrolle (kein UPDATE, keine Neuverkettung; personenbezogene Werte nur über `audit_seal_pii` versiegelt, Digest des Originals bleibt prüfbar, WP-680), Verifikation | AUD-01, AUD-04, AUD-07 | `backend/internal/audit/pg_roundtrip_integration_test.go`, `backend/internal/audit/append_only_integration_test.go`, `backend/internal/audit/seal_integration_test.go`, `backend/internal/events/tst01_integration_test.go` |
| T17 | alle | I | Personenbezogene Daten in Logs, Historie oder Exporten | PII-Vault mit Surrogaten, Redaktion im Log-Handler, keine Secrets in UI oder Logs | SEC-01, SEC-05 | `backend/internal/pii/pii_integration_test.go`, `backend/internal/platform/httpx/redact_test.go` |
| T18 | TB6 | E | Anwendung umgeht RLS | App-Rolle ohne Superuser und ohne BYPASSRLS; Pool verweigert solche Rollen; Laufzeit-DDL nur über den Job | TEN-03, MET-14 | `backend/internal/tenant/rls/isolation_integration_test.go`, `backend/internal/database/role_check_integration_test.go` |
| T19 | Lieferkette | T | Verwundbare Abhängigkeiten oder Images | govulncheck, npm audit, Trivy blockierend in CI; signierte Releases | SEC-11, TST-04 | `docs/security/abhaengigkeitsscans.md`, `.github/workflows/ci.yml` |
| T20 | TB1 | D | Teure Analysen (Impact/Topologie über die ganze Org) | Knotenlimit 10.000; Breitensuche mit höchstens zwei Abfragen je Ebene, ab einer Ebene mit mehr als 1.000 CIs eine Lesung aller Kanten der Organisation (WP-335); kompakte Impact-Antwort über `fields=`; Budgets im Lasttest | NFR-02, IMP-06 | `backend/internal/topology/querybudget_integration_test.go`, `docs/acceptance/lasttest-g1.md` |
| T21 | TB1 | T/E | Hochgeladene Polyglot-Datei (z. B. gültiges PDF mit eingebettetem HTML) wird über die signierte Download-URL im Browser auf dem Ursprung des Frontend-Proxys gerendert (XSS) | Signierte GET-URLs (Dokumente, Exporte, DATEV-Fehlerberichte, Import-Reports, Etiketten-PDFs) tragen `response-content-disposition=attachment` mit sicherem Dateinamen (RFC 6266/5987) und einen festen `response-content-type` (geprüfter Typ, sonst `application/octet-stream`; HTML, SVG und Skripttypen nie) in der SigV4-Signatur (`blob.Download`); `frontend/objects-proxy.conf` setzt zusätzlich `Content-Disposition: attachment`, `X-Content-Type-Options: nosniff` und `Content-Security-Policy: sandbox; default-src 'none'`. Befund aus WP-829 (`TestPresignedGetURLForcesDownload` stand mit `t.Skip`) ist behoben | SEC-09, SEC-11 | `backend/internal/platform/blob/presign_test.go`, `backend/internal/document/security_test.go` (`TestDownloadIsAnAttachmentWithTheVerifiedType`), `frontend/e2e-real/fixtures/setup-real-stack.sh` |

## Phase 2 (G2)

Neue Systemgrenzen mit G2 (WP-329). T21 in der Tabelle oben gehört bereits
zur Phase 2 (Auslieferung über signierte URLs, Befund aus WP-829, SR-53).

```
Browser ──Upload──▶ API (Magic-Bytes, Whitelist, Größe) ──▶ Objektspeicher (reticora-documents)
                        └─ Job document.scan ──INSTREAM──▶ clamd (parst nicht vertrauenswürdige Dateien)
Browser ◀── signierte GET-URL (15 min, attachment) ── /objects (nginx: nur GET, Buckets exports und reticora-documents)
Browser/API-Schlüssel ──CSV, XLSX, JSON-Zeilen──▶ Import-Parser ──▶ Zeilenpfad der Entitäten
Browser ──Metamodell-Datei (v2)──▶ Metamodell-Import ──▶ Entwurf ──metamodel:publish──▶ Ressourcenrouten
Kamera/Scanner ──Code──▶ GET /labels/lookup ──▶ Asset, Verbrauchsmaterial, Standort
Browser ──Bulk/Revert──▶ Job-Queue (ci.bulk, asset.bulk) ──▶ Revert mit dem Recht der Ursprungsoperation
Browser ──Historie, as-of, Snapshot──▶ entity_change, ci_snapshot, asset_snapshot (Feldrechte, Tombstones)
```

Vertrauensgrenzen:

- **TB7:** Hochgeladene Datei ↔ Plattform (Upload, Speicherung, Scan,
  Auslieferung).
- **TB8:** clamd ↔ Plattform. clamd parst nicht vertrauenswürdige Dateien in
  einem eigenen Container ohne Zugang zu Datenbank und Objektspeicher.
- **TB9:** Importdatei bzw. Metamodell-Datei ↔ Plattform (Parser,
  Zeilenpfad, Entwurf).
- **TB10:** Feldrechte innerhalb eines Scopes (RBA-07, E-43): Leser ↔ Felder,
  die seine Rolle nicht lesen darf, in jedem Ausgabekanal.
- **TB11:** Lizenz und Modul ↔ Funktion (ENT-06, E-44, E-105).

| # | Grenze | STRIDE | Bedrohung | Gegenmaßnahme | Anforderung | Nachweis |
|---|---|---|---|---|---|---|
| T22 | TB7 | T/E | Upload mit falschem Typ (ausführbare Datei als PDF, Polyglot) oder Abruf vor dem Scan | Typ aus den Magic-Bytes gegen die Whitelist (`document/sniff.go`), Größenlimit, gespeichert wird nur der geprüfte Typ; Abruf nur mit `scan_status = clean` (fail-closed nach E-42: 409 `document-pending-scan` bzw. `document-quarantined`); ein Ersatz-Upload setzt die neue Version auf `pending_scan` | SEC-09 | `backend/internal/document/sniff_test.go` (`TestVerifyUploadRejectsMismatch`), `backend/internal/document/scan_status_integration_test.go` (`TestDocumentScanStatus`), `backend/internal/document/security_test.go` (`TestReplacementUploadOnCleanDocumentIsNotDelivered`, `TestPolyglotIsStoredWithTheVerifiedType`) |
| T23 | TB7 | I/T | Pfad- und Key-Manipulation: Abruf eines Objekts einer anderen Organisation oder eines anderen Dokuments | Storage-Key serverseitig aus Organisation und Dokument, Prüfung vor jedem Abruf (`OwnsStorageKey`); die SigV4-Signatur deckt nur Bucket und Key der Anfrage; TTL 15 min (`s3PresignTTL`) | SEC-09, TEN-05 | `backend/internal/document/blob_isolation_test.go` (`TestCheckOrgKey`, `TestOwnsStorageKey`, `TestStorageKeyRejectedOutsideOrganization`), `backend/internal/platform/blob/presign_test.go` (`TestPresignedGetURLSignsOnlyTheRequestedObject`), `backend/internal/platform/blob/public_url_test.go` (`TestPresignedGetURLNamesThePublicOrigin`), `backend/internal/document/security_test.go` (`TestPresignOnlyForTheRequestedDocument`) |
| T24 | TB8 | D/E | Präparierte Datei greift clamd an oder hält den Scan auf; clamd fällt aus oder fehlt | clamd im eigenen Container ohne Zugangsdaten, Größengrenzen (`StreamMaxLength`, `MaxScanSize`), Zeitlimit `RETICORA_CLAMD_TIMEOUT`; ohne clamd bleiben Versionen in `pending_scan` und enden nach den Wiederholungen in `scan_failed` (fail-closed); gepinntes Image mit Trivy-Scan (`image-scan-thirdparty`). die Startprüfung verlangt clamd und NATS auch beim leeren Release-Standard, `deploy/k8s/base` enthält clamd (SR-57 behoben, WP-853) | SEC-09, SEC-11 | `backend/internal/document/clamd_integration_test.go` (`TestClamdScansEICARAndPDF`, `TestClamdDocumentScanJob`), `backend/internal/document/scan_test.go`, `backend/internal/config/config_test.go` (`TestValidateDocumentScanner`), `backend/internal/config/k8s_required_env_test.go` (`TestAuditT5K8sRenderedEnvSatisfiesStartCheck`), `.github/workflows/ci.yml` (Job `image-scan-thirdparty`) |
| T25 | TB9 | D | Zip-Bombe, übergroße `sharedStrings.xml`, ZIP-Eintrag größer als angegeben, zu viele Zeilen | `UnzipSizeLimit` 200 MiB, `UnzipXMLSizeLimit` 16 MiB (darüber lagert excelize ins Temp-Verzeichnis aus), 50 MB je Datei, 50.000 Zeilen, 5.000 Zeilen je API-Aufruf (E-49) | IMP-IO-01 | `backend/internal/importer/parse_test.go` (`TestXLSXTooComplex`, `TestCSVRowLimit`), `backend/internal/importer/security_test.go` (`TestXLSXLargeSharedStringsStayOutOfMemory`, `TestXLSXEntryLargerThanDeclaredIsRefused`) |
| T26 | TB9 | T | Formelinhalte im Import (`=HYPERLINK`, DDE) bzw. Formel-Injektion im Export | XLSX-Formelzellen werden als gespeicherter Wert gelesen, nie ausgewertet; CSV-, DATEV-, Asset- und Bulk-Export sowie der Import-Fehlerbericht setzen vor `=`, `+`, `-`, `@`, Tab und CR ein `'` | IMP-IO-01, EXP-01 | `backend/internal/importer/security_test.go` (`TestXLSXFormulaCellsAreNotEvaluated`, `TestCSVFormulaCellsAreTakenAsText`), `backend/internal/export/export_test.go` (`TestCSVNeutralizesFormulas`) |
| T27 | TB9/TB2 | T/E | Import mit IDs einer anderen Organisation oder eines fremden Clients; Import umgeht Rechte, Feldrechte oder `max_cis` | Datei- und API-Import laufen über denselben Zeilenpfad mit den Rechten des Starters (beim Lauf neu geladen), Feldrechten und Scope; fremde Verweise wie unbekannte (Zeile im Fehlerbericht); `max_cis` unter der Zulassungssperre | IMP-IO-01, TEN-05, ENT-03 | `backend/internal/tenant/rls/foreign_refs_p2_integration_test.go` (`TestForeignReferencesP2`), `backend/internal/importer/run_integration_test.go` (`TestImportJobHidesStarterRightsAndReloadsThem`), `backend/internal/importer/apikey_integration_test.go` (`TestImportWithAPIKeyScopesAndFieldRights`), `backend/internal/tenant/integrity/writers_integration_test.go` (`TestPhase2AutomatedWriters`) |
| T28 | TB9 | E/D | Metamodell-Import als Rechteausweitung (Feldrechte, Attributdefinitionen ohne `ci_attribute:manage`) oder als übergroße Eingabe | Body höchstens 10 MiB, strikte Dekodierung mit Format-Version; jedes Item verlangt das Recht seiner Ressourcenroute (`ItemPermission`), Feldrechte-Abschnitte werden übersprungen; Ergebnis ist ein Entwurf, die Veröffentlichung verlangt `metamodel:publish` | MET-54, RBA-05 | `backend/internal/metamodel/import_test.go` (`TestImportHandler`, `TestImportAcceptsFieldPermissionSections`), `backend/internal/metamodel/import_roundtrip_integration_test.go` (`TestMetamodelImportRoundtripAgainstPostgres`) |
| T29 | TB2 | I | Etiketten-Lookup (Kamera-Scan) als Orakel für Objekte anderer Organisationen oder Clients | Lookup unter RLS mit dem Leserecht der Art; fremder, unsichtbarer und unbekannter Code geben dieselbe 404-Antwort; Etiketten-PDF nur für den Ersteller | INV-03, TEN-09 | `backend/internal/labels/lookup_integration_test.go` (`TestLabelLookupUniformNotFound`), `backend/internal/labels/batch_integration_test.go` (`TestLabelBatch`) |
| T30 | TB1/TB2 | T/E | Bulk oder Revert ohne das Recht der Ursprungsoperation, doppelt oder für fremde Jobs; Replay | Revert prüft Sichtbarkeit (404), Recht (403), dann Zustand (409); Idempotency-Key an den Principal gebunden | BLK-02, API-03 | `backend/internal/jobs/revert_integration_test.go` (`TestBulkRevertNegativeCasesAgainstPostgres`), `backend/internal/middleware/idempotency_integration_test.go` (`TestIdempotencyIsAtomicAndPrincipalBound`) |
| T31 | TB10 | I | Feldwert-Leak über einen Ausgabekanal: REST, GraphQL (`ci`, `cis`, `searchEverything`, `ciHistory`, `ciForm`), Suche, strukturierte Abfrage, Export, Webhooks, Historie, as-of mit Tombstone, Snapshot-Job | Projektion je Kanal mit der Feldpolitik des Principals (Webhooks: des Service-Accounts, Jobs: des Erstellers), fail-closed für nicht gefundene Objekte; Audit-Log und Review-Payloads nach E-43 bewusst ohne Projektion (SR-64) | RBA-07, AUD-08 | `backend/internal/tenant/rls/fieldrights_matrix_integration_test.go` (`TestFieldRightsChannelMatrix`, `TestFieldRightsChannelMatrixObjectKinds`), `backend/internal/history/fieldrights_integration_test.go` (`TestCIHistoryFieldRightsAgainstPostgres`), `backend/internal/asset/history_integration_test.go` (`TestAssetValueHistory`, as-of und Tombstone seit WP-329), `backend/internal/citype/form_integration_test.go` (`TestResolvedFormFieldRightsAgainstPostgres`) |
| T32 | TB10 | I | Ableitung verborgener Feldwerte: berechnete CI-Attribute über Asset-Felder, Feldzustände des Formulars aus Bedingungen über verborgene Felder | Offen: SR-58 und SR-59 (`docs/security/review-phase2.md`) | RBA-07 | – (Befund) |
| T33 | TB2 | I | Ein Dokument eines Clients wird org-weit sichtbar, sobald sein letzter Link entfällt (Entlinken durch einen gescopten Principal, Löschen des Ziels) | `document.orphaned` (000281, WP-852): ein Dokument, das Links hatte, sehen ohne Link nur org-weite Principals; nur die Zähler-Trigger setzen die Spalte (SR-56 behoben) | TEN-05, TEN-09 | `backend/internal/tenant/rls/document_link_scope_integration_test.go` (`TestDocumentStaysScopedWithoutLinks`), `backend/internal/tenant/rls/module_object_integration_test.go` (`TestModuleTablesInheritObjectScope`), Kanalzeile `document-link` in `backend/internal/tenant/rls/channel_matrix_p2_integration_test.go` |
| T34 | TB11 | E | P2-Daten ohne Lizenz-Feature oder bei abgeschaltetem Modul über REST, GraphQL, Suche, Bulk oder Import | Org-Gate je Route (`featureRules`, WP-705) mit Methoden- und Suffixregeln, Modul-Gate (WP-706), GraphQL-Resolver mit Modul und Feature (GQL-04). Lücke: SR-61 | ENT-06, GQL-04 | `backend/internal/entitlement/entitlement_test.go` (`TestRequiredFeatureMapping`), `backend/internal/entitlement/enforce_integration_test.go` (`TestPhase2RouteGates`), `backend/internal/importer/handler_test.go` (`TestImportRoutesRequireTheImportFeature`) |
| T35 | TB2 | I | Historie und as-of als Kanal in fremde Scopes oder auf gelöschte Objekte | Lesen nur mit dem Leserecht der Entität und unter RLS; ein gelöschtes Objekt bleibt nur sichtbar, solange der Aufrufer Snapshot oder Spur sieht | AUD-08, TEN-05 | `backend/internal/history/replay_integration_test.go` (`TestCIAsOfAgainstPostgres`), `backend/internal/history/asset_replay_integration_test.go` (`TestAssetAsOfAgainstPostgres`) |

## Phase 3 (G3)

Umfang: die Datenschutz-Workflows der Phase 3 (SEC-05 [P3], TLC-05, TLC-06):
Auskunft und Löschung einer Person (WP-1158), Vollexport (WP-1149 bis
WP-1151) und Löschung einer Organisation (WP-1152 bis WP-1156). Die weiteren
Bedrohungen der Phase 3 ergänzt das Sicherheitsreview G3 (WP-1175).

Vertrauensgrenze:

- **TB12:** Gelöschte Person bzw. Organisation ↔ Historie und Exporte (Audit,
  CI-Historie, Ereignisse, Zustellungen, ADMP-Cache, Vollexporte, Backups).

| # | Grenze | STRIDE | Bedrohung | Gegenmaßnahme | Anforderung | Nachweis |
|---|---|---|---|---|---|---|
| T36 | TB12 | I | Gelöschte PII in Historie und Exporten: Nach der Löschung einer Person oder Organisation zeigen Audit-Änderungen, `ci_change`, `event_outbox`, Zustellungen, der ADMP-Cache oder ein Export den Klartext weiter | Personenbezogene Werte der Historie stehen nur als Surrogat (`{"$pii": …}`) im Vault; `privacy.subject_erase` (shred, WP-1158) löscht die Wert-Subjekte, das Subjekt der Person und die Zustelladressen, jeder Leseweg löst danach zu `null` auf, die Hash-Kette des `audit_log` bleibt prüfbar (`audit-verify`). Der Org-Purge löscht zuerst `org_dek`, dann die Subjekte (Crypto-Shredding, WP-1155); Vollexporte sind 24 h abrufbar und werden danach gelöscht (WP-1151); Backups laufen mit ihrer Aufbewahrung aus (Löschprotokoll, WP-1156) | SEC-05, CH16, TLC-06, AUD-04 | `backend/internal/pii/history_redaction_integration_test.go` (`TestShreddedPersonInvisibleInHistory`, `TestPurgedOrganizationUnresolvable`), `backend/internal/privacy/subject_integration_test.go` (`TestSubjectShredHidesHistory`), `backend/internal/tenantapi/purge_external_test.go` |

## Restrisiken

Restrisiken mit Bewertung stehen als Befunde in `docs/security/review-g1.md`
(Abschnitt „Neue Befunde“, G1) und `docs/security/review-phase2.md`
(Abschnitt „Befunde“, G2).
