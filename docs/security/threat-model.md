# Threat-Model Phase 1 (G1)

Grundlage des internen Sicherheitsreviews (WP-209, `docs/security/review-g1.md`).
Methode: STRIDE je Vertrauensgrenze. Jede Bedrohung verweist auf die
Anforderung, die sie abwehrt, und auf den Test, der die Abwehr belegt. Der
Umfang ist das G1-Release: Spätphasen-Module (P2+, Add-ons) sind nach E-03 über
WP-214 abgeschaltet. Ihre Routen sind nicht registriert, und sie gehören nicht
zur Angriffsfläche.

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
| T16 | alle | R | Abstreiten oder Manipulieren von Änderungen | Hash-verkettetes Audit-Log, append-only, Verifikation | AUD-01, AUD-07 | `backend/internal/audit/pg_roundtrip_integration_test.go`, `backend/internal/events/tst01_integration_test.go` |
| T17 | alle | I | Personenbezogene Daten in Logs, Historie oder Exporten | PII-Vault mit Surrogaten, Redaktion im Log-Handler, keine Secrets in UI oder Logs | SEC-01, SEC-05 | `backend/internal/pii/pii_integration_test.go`, `backend/internal/platform/httpx/redact_test.go` |
| T18 | TB6 | E | Anwendung umgeht RLS | App-Rolle ohne Superuser und ohne BYPASSRLS; Pool verweigert solche Rollen; Laufzeit-DDL nur über den Job | TEN-03, MET-14 | `backend/internal/tenant/rls/isolation_integration_test.go`, `backend/internal/database/role_check_integration_test.go` |
| T19 | Lieferkette | T | Verwundbare Abhängigkeiten oder Images | govulncheck, npm audit, Trivy blockierend in CI; signierte Releases | SEC-11, TST-04 | `docs/security/abhaengigkeitsscans.md`, `.github/workflows/ci.yml` |
| T20 | TB1 | D | Teure Analysen (Impact/Topologie über die ganze Org) | Knotenlimit 10.000; Breitensuche mit höchstens zwei Abfragen je Ebene, ab einer Ebene mit mehr als 1.000 CIs eine Lesung aller Kanten der Organisation (WP-335); kompakte Impact-Antwort über `fields=`; Budgets im Lasttest | NFR-02, IMP-06 | `backend/internal/topology/querybudget_integration_test.go`, `docs/acceptance/lasttest-g1.md` |

## Restrisiken

Restrisiken mit Bewertung stehen als Befunde in `docs/security/review-g1.md`
(Abschnitt „Neue Befunde“).
