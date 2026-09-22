## Zusammenfassung

Audit Teil 2 gegen den in diesem Auftrag gelieferten Katalogauszug (Abschnitte 5–8) und die GRUNDLAGEN; Prüfstand `2f2bd1d8c170f3aa981160c14542885ccede8b01`, 2026-09-22. Ausschließlich dieser Bericht wird geändert, keine Produktkorrekturen.

Grundlage: `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/audit/00-schema-ist.md`, `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/audit/00-bestandsaufnahme.md`, `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/audit/README.md` und `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/spec/katalog-v3/00-grundlagen.md`. Das Schema-Inventar beschreibt Migrationen, kein geprüftes Produktionsschema.

**36/36 IDs erfasst; G1 nicht freigabefähig.** Keine vollständige PASS-Bewertung: vorhandene Funktionen sind widersprüchlich/unvollständig oder nicht durch reale Integrationen belegt.

| Primärer Tag / Zählgruppe | PASS | PARTIAL | ABWEICHEND | FAIL | OFFEN | N/P | Summe |
|---|---:|---:|---:|---:|---:|---:|---:|
| [B] | 0 | 3 | 21 | 1 | 0 | 2 | 27 |
| [Q] | 0 | 1 | 0 | 0 | 0 | 0 | 1 |
| [P4] | 0 | 1 | 0 | 0 | 3 | 0 | 4 |
| [P2] | 0 | 0 | 1 | 0 | 1 | 0 | 2 |
| [P2–P4]/[A] (RBA-05) | 0 | 1 | 0 | 0 | 0 | 0 | 1 |
| (V), gemischte Phasen (ENT-06) | 0 | 0 | 1 | 0 | 0 | 0 | 1 |
| **Gesamt** | **0** | **6** | **23** | **1** | **4** | **2** | **36** |

- Jede ID zählt einmal; spätere Teilumfänge stehen zusätzlich in ihrer Zeile. ENT-06-ABWEICHEND betrifft verbindliche CH14-Add-on-Trennung, **nicht** unverbindliche Paketwerte.
- **Critical:** TEN-02/04/05/06 (Scopes/Policies/DB-Pfade, Cross-Org-Blobzugriff); AUT-01/02/09 (Sperrungsumgehung, dauerhaft erneuerbare Altberechtigungen, bekanntes Default-Administratorkonto); RBA-02/03/04 (verlorene Rollen-Scopes, Viewer kann Credentials entschlüsseln).
- **High:** TEN-03/09/10, AUT-04/08/10, RBA-01/05/06/08, ENT-01–08. Kernlücken: VRFs, Service-Accounts, Operatorzugriff, Kontingente, Ablauf/Collector-Pause, Allkanal-/Integrationstestnachweis.
- **G1-Blocker:** TEN-02/03/04/05/06/09/10; AUT-01/02/04/09/10; RBA-01/02/03/04/08; ENT-01/02/03/04/05/07/08. TEN-01 und AUT-03 besitzen weitere offene Baseline-Nachweise/Abweichungen.
- Spätere fehlende Funktionen (TEN-07/08, AUT-07, RBA-07) sind OFFEN, nicht Baseline-FAIL. Vorhandener P2/P4/[A]-Code ist normal geprüft; seine Unvollständigkeit allein und ENT-06-Vorschlagswerte sperren G1 nicht.
- **Ungeprüfte IDs wegen fehlendem Wortlaut:** AUT-05, AUT-06 (N/P). Nur der nicht gelieferte v2-Restumfang von AUT-03/TEN-09 bleibt zusätzlich unbewertet; gelieferte Kriterien sind geprüft. Keine sonstige ID ausgelassen.
- Alle **105 Tabellen** einzeln erfasst; **80 direkte DB-Einstiege in 58 Dateien** klassifiziert, davon **70 produktiv in 55 Dateien**. **0 ungemappte registrierte Routen** bei 400 expliziten Operationen, aber falsche Aktionsrechte trotz vollständigem Mapping.
- Lokal nur drei Tenant-Kontext-/Quelltexttests grün; zehn Testpakete und Backendbuild am Offline-Modulcache blockiert. Kein behaupteter aktueller PG-/Keycloak-/S3-Isolationslauf; CI-Grenzen und Mocknachweise separat dokumentiert.

## Ergebnis je Anforderung

PASS setzt vollständige, korrekte und testbelegte Umsetzung voraus; Mocks allein genügen nicht. N/P bezeichnet fehlenden prüfbaren Wortlaut oder eine andere fehlende erforderliche Prüfbasis, nicht fehlenden Code. Aufwand bezieht sich auf die empfohlene Produktkorrektur: S ≤ 0,5 Tag, M 0,5–2 Tage, L > 2 Tage (aufzuteilen). Vorschläge und optionale/spätere Umfänge werden nicht als fehlende Baseline gegatet.

| ID | Tag | Status | Evidenz | Tests | Befund (1–2 Sätze) | Severity | Aufwand |
|---|---|---|---|---|---|---|---|
| TEN-01 | [B], Reseller [P4] | PARTIAL | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000001_tenant_model.up.sql:6–66`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000055_cmdb_extensions.up.sql:286–320` | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/tenantapi/handler_test.go:114`: Memory-Hierarchie, kein erfolgreicher Gesamtroundtrip; T1. | Org/Client/klassische Standorthierarchie, Location-Baum und Assetbezug bestehen, aber durchgängige einheitliche Hierarchie mit Scopewirkung ist nicht nachgewiesen. Reseller fehlt als späterer Teil (TEN-08), vorhandene doppelte Standortstrukturen erfordern Konsolidierung. | Medium | L |
| TEN-02 | [B] | ABWEICHEND | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/audit/00-schema-ist.md:9–20,40–144`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000001_tenant_model.up.sql:38–66`; Einzeltabellen unten. | Schema-/RLS-Tests decken nicht alle 105 Tabellen/Spalten ab; T1. | Fünf Tabellen ohne eigene Org-Spalte und fünf nullable, darunter globale Kataloge/Org-Wurzel gesondert zu klären; Standort-/Kindtabellen tragen Client/Site nicht durchgängig denormalisiert. Daraus folgende fehlende Client-/Site-WITH-CHECK-Schranken werden einzeln als Critical geführt, nicht globale Metadaten pauschal als Tenant-Leak. | Critical | L |
| TEN-03 | [B] | PARTIAL | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/database/pool.go:52–76,112–130`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000056_rls_enforcement.up.sql:23–78` | `TestPoolRefusesRLSBypassingRole`, bedingter Skip; keine live geprüfte Owner-/DDL-Matrix. | SET ROLE und NOSUPERUSER/NOBYPASSRLS werden geprüft; explizite Grants liefern USAGE/DML statt vollständigem CH19-DDL-Vertrag. Effektive geerbte Rechte/Eigentümerschaft sind nicht live belegt, FORCE wird nur für bereits RLS-aktivierte Tabellen gesetzt. | High | M |
| TEN-04 | [B] | ABWEICHEND | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/database/pool.go:150–180`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/tenant/tenant.go:9–13`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/identity/handler.go:253–276` | Drei Kontext-/Quelltexttests grün, keine vollständige GUC-/Poolintegration; T1. | Produktive Helfer setzen Org und teils einen Client-Scope-String lokal, nicht alle fünf verlangten GUCs pro Request; SQL unterstützt Clientmengen, deren autoritative Ableitung fehlt aber schon in Sessions. Kein produktiver sessionsweiter Tenant-Setter gefunden, aber fehlende Scopes bedeuten org-weiten Zugriff. | Critical | L |
| TEN-05 | [B] | ABWEICHEND | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000033_client_scope_rls.up.sql:29–79`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000056_rls_enforcement.up.sql:62–105`; vollständige Policy-Matrix unten. | `TestClientScopeRLS` prüft andere benannte Clients, nicht NULL-Schreiben/Site/Team; T1. | Nur sieben Tabellen prüfen Client, neun weitere mit client_id nicht; Site/Team fehlen, metric_sample hat keine RLS. WITH CHECK erlaubt auch gescopten Schreibern NULL-Client, Systemausnahmen sind ebenfalls schreibfähig; jede fehlende Client-/Site-Schreibschranke ist Critical. | Critical | L |
| TEN-06 | [B] | ABWEICHEND | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/ci/pg_repository.go:66–78`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/export/pg_job_repository.go:174–198`; vollständiges DB-Einstiegsinventar unten. | Keine vollständige negative Pfadmatrix; lokale DB-Tests setupblockiert. | Drei zentrale WithTenant-Varianten werden produktiv nicht aufgerufen; 46 private Helfer und weitere Poolpfade liefern inkonsistenten Kontext statt zentraler Request-/Tenanttransaktion. Worker nutzen teils app.system global statt Mandanteniteration, Exporte/Suche/Lifecycle verlieren Scopes; Dokumentdownloads haben zusätzlich Cross-Org-Blobzugriff. | Critical | L |
| TEN-07 | [P4] | OFFEN | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/deploy/terraform/main.tf:1–24`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/deploy/k8s/overlays/prod/kustomization.yaml:15–33`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/credential/credential.go:64–70,137–147` | Kein Dedicated-/KRITIS-Provisionierungs-/Isolationstest. | Allgemeine Deployments und Org-DEKs sind Voraussetzungen, keine dedizierte Schema-/Instance-Funktion mit eigenem Betriebsprofil. Kein solcher P4-Code gefunden; keine erwiesene Architekturblockade und kein G1-Fehlen daraus abgeleitet. | – | – |
| TEN-08 | [P4] | OFFEN | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000001_tenant_model.up.sql:6–25`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/tenantapi/model.go:7–16` | Keine Reseller-Kontext-/Session-/Audit-Teststrecke gefunden. | Keine Resellertabelle/organization.reseller_id oder explizite Orgwechsel-Session; Client innerhalb Org ersetzt keine Reseller-Ebene. Spätere Erweiterung nicht als G1-fällig bewertet, keine unvermeidbare Architekturblockade belegt. | – | – |
| TEN-09 | [Q] | PARTIAL | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/database/rls_isolation_test.go:98,189`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/tenant/rls/isolation_integration_test.go:117–127,164`; Testmatrix unten. | Echte PG-Testfälle vorhanden, aber T1 blockiert; ein Fixture setzt Org sessionsweit über Pool statt Transaktion. | Repräsentative Org-/Client-Reads und INSERT-Abweisung bestehen als Tests, keine vollständige Site-/Team-/NULL-Schreib-/Job-/Notification-/Review-/Location-Abdeckung. v2-Restumfang fehlt; gefundene tatsächliche Expositionspfade bleiben Critical, auch wenn Testlücke hier High ist. | High | L |
| TEN-10 | [B] | FAIL | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000015_network_ipam.up.sql:22–52`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/permission/catalog.go:3–102`; Suchraum Migrationen/Backend/API. | Kein VRF-CRUD-/Org-Anlage-Defaulttest gefunden. | VRF-Modell, Org-Unique(name), is_default/global bei Org-Anlage sowie CRUD/vrf:manage fehlen. Bestehende Org+CIDR-/Adress-Eindeutigkeit ohne VRF erfüllt CH10 nicht. | High | L |
| AUT-01 | [B] | ABWEICHEND | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/identity/oidc.go:202–346`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/identity/handler.go:248–276`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/user/pg_repository.go:972–992` | `TestCallbackRejectsForgedIDToken`, PKCE-/JWKS-Tests mit Fake-IdP; kein echter Einladungs-/Provisionierungs-Roundtrip; T1. | PKCE und Signatur-/Issuer-/Audience-Prüfung bestehen, aber Org kommt aus UUID-Gruppe, Benutzer-Upsert aus oidc_subject statt zugelassenem Org/E-Mail-Erstlogin. Upsert reaktiviert auch lokal gesperrte Benutzer ohne autorisierten Reaktivierungsprozess. | Critical | L |
| AUT-02 | [B] | ABWEICHEND | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/identity/handler.go:15–18,155–175,415–418`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/identity/types.go:215–223`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/identity/session.go:17–21,60–140` | `TestRefreshAllowsRecentlyExpiredToken`; lokale RSA-/HTTP-Tests, keine echte Sperr-/Logout-/Refresh-Replay-Prüfung; T1. | RS256 besteht, aber 60 statt 15 Minuten, abweichende Claims/fehlendes kid, kein Refresh-Cookie/Redis-Widerruf. Refresh übernimmt alte Rechte ohne Statusprüfung wiederholt, sodass Deaktivierung/Rechteentzug keine zuverlässige Wirkung entfalten. | Critical | L |
| AUT-03 | [B] | ABWEICHEND | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/cmd/server/main.go:366–390`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/config/config.go:110–111`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/identity/session.go:94–97` | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/cmd/server/main_test.go` `TestLoadSessionIssuer*`: keine Production-Matrix. | Kein eigener Dev-Login; AllowInsecureDevAuth ist zwar opt-in, aber nicht ausdrücklich außerhalb production begrenzt. Der typisierte nil-Verifier weist Tokens ab, daher kein nachgewiesener produktiver Auth-Bypass; übriger v2-Wortlaut fehlt. | Medium | M |
| AUT-04 | [B] | ABWEICHEND | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/identity/apikey.go:15–20,74–97,130–185`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/identity/apikey_store.go:23–31,68–86` | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/middleware/principal_test.go:17–89`: fakeAPIKeys, kein echter Lifecycle-/RLS-Test. | Nur rk_live_ mit zusätzlichem Trenner zwischen 12/40 Base62-Zeichen; SHA-256, Constant-Time und Ablauf/Widerruf bestehen. rk_test_, Owner-Rechteschnitt, vollständige einmalige Ausgabe/Verwaltung und überlappende Rotation fehlen; PG-Lookup erfolgt vor Tenant-Kontext. | High | L |
| AUT-05 | [B] | N/P | Gelieferter Katalog: „AUT-05, AUT-06 [B] unverändert“; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/audit/00-bestandsaufnahme.md:210–225` dokumentiert fehlenden v2-Originaltext. | Ohne Wortlaut nicht zuordenbar. | Originalanforderung fehlt; keine Rekonstruktion aus Produktbeschreibungen. | – | – |
| AUT-06 | [B] | N/P | Gelieferter Katalog: „AUT-05, AUT-06 [B] unverändert“; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/audit/00-bestandsaufnahme.md:210–225` dokumentiert fehlenden v2-Originaltext. | Ohne Wortlaut nicht zuordenbar. | Originalanforderung fehlt; keine Rekonstruktion aus Produktbeschreibungen. | – | – |
| AUT-07 | [P4] | OFFEN | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/deploy/keycloak/realm-reticora.json:53–111`; Suchraum Backend/Deploy/Frontend: kein SAML-Broker-Verwaltungspfad. | Kein SAML-Org-Brokering-Test gefunden. | Noch keine organisationsbezogene SAML-Broker-Implementierung; vorhandener Keycloak/OIDC-Client ist nur Grundlage. Kein G1-Fehlen aufgrund der späteren Phase. | – | – |
| AUT-08 | [P4] | PARTIAL | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/iga/handler.go:64–76`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/iga/scim.go:175–223,280–300`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/iga/connector.go:82–189` | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/iga/iga_test.go`: InMemory-/HTTPS-Testserver, kein externer End-to-End-Roundtrip. | Inbound und Outbound bestehen, Group-PATCH ist aber PUT-Alias, User-PATCH beschränkt und TaskRunner nicht produktiv aufgerufen. Deprovisionierung entzieht keine Sessions; dieser vorhandene Pfad wird nicht pauschal OFFEN gesetzt. | High | L |
| AUT-09 | [B] | ABWEICHEND | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/deploy/keycloak/realm-reticora.json:2,95–107,122–149`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/install-cloud.sh:894–901`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/identity/handler.go:304–312` | Kein echter MFA-/Broker-/Lockout-Test; Realm-Helper aus Teil 1 prüft Struktur, keine Auth-Policy. | Ein Realm/Org-Mapper bestehen, aber Org-Attribut wird nicht verwendet, Admin-Brokering und verpflichtende MFA/Passwort-/Lockout-Policy fehlen. Produktiver Import enthält ein bekanntes nichttemporäres Demo-Administratorkonto, dessen Passwort nicht durch den Installer individualisiert wird. | Critical | L |
| AUT-10 | [B] | ABWEICHEND | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/cmd/server/main.go:307–319`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/config/config.go:123`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/middleware/auth.go:79–104`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/middleware/ratelimit.go:138–158` | Memory-Limitertests mit Budgets 2/3, keine Produktionsprüfung 20/min pro IP vor Auth. | Allgemeines Budget 600/min nach Auth statt eigenständigem Pre-Auth20/min; ungültige API-Keys erreichen den Limiter nicht. Signup-/Einladungsannahme fehlen, dedizierte Keycloak-Lockout-Anzeige ist nicht nachgewiesen. | High | M |
| RBA-01 | [B] | ABWEICHEND | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/permission/catalog.go:3–102`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000032_standard_role_seeds.up.sql:20–39`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/identity/types.go:11–110` | `TestRoutePermissionsExistInCatalog`/`TestIdentityCatalogCoversRoutePermissions` prüfen Ist-Katalog, nicht v3-Liste; T1. | Neun geforderte Schlüssel fehlen im SQL-/Go-Katalog; die vorhandenen site:*-Rechte ersetzen die geforderten location:*-Rechte nicht, collector:manage existiert nur als Identity-Konstante. Ähnliche oder ungenutzte Schlüssel erfüllen die exakten Aktionsrechte nicht. | High | M |
| RBA-02 | [B] | ABWEICHEND | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000032_standard_role_seeds.up.sql:48–150`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/identity/handler.go:270–277,304–378`; Matrix unten. | Rollen-/Authztests mit Memory/Principals, keine Prüfung der Sollmatrix beim echten Login; T1. | Seed und ausgegebene Session verwenden unterschiedliche Rollenmodelle, engineer/client_technician werden nicht als gleichnamige OIDC-Rollen erkannt; C-Semantik fehlt. Viewer erhält unter anderem credential:read und kann damit den Decrypt-Endpunkt nutzen. | Critical | L |
| RBA-03 | [B] | ABWEICHEND | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000005_users_roles.up.sql:17–38`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000014_phase2_users_teams_roles.up.sql:30–55`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/permission/pg_repository.go:115–158` | `TestMemoryRepositoryTeamsAndRoles`, Permissiontests ohne echte Scope-Union; T1. | Custom-Rollen und Rechtevereinigung bestehen, EffectivePermissions ignoriert aber Scope-Spalten und Sessionauflösung übernimmt DB-Rollen nicht. Team-Scope und durchgängige Org→NULL-/Mengenvereinigung fehlen, sodass Client-Rollenzuweisung keine verlässliche Begrenzung ergibt. | Critical | L |
| RBA-04 | [B] | ABWEICHEND | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/server/authz.go:17–159,181–248,264–359`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/credential/handler.go:23–29,125–143`; Routeninventar unten. | `TestEveryRouteHasAPermissionMapping`, `TestAuthorizationMiddlewareEnforcesPermissions`: vorhandenes Mapping/Memory, nicht richtige Fachrechte; T1. | Statisch alle 400 expliziten Operationen gemappt, unbekanntes Mapping wird verweigert; UNMAPPED-Liste leer. Falsche Aktionszuordnung lässt jedoch Viewer entschlüsseln, ci:write löschen und order:write freigeben; Mappingexistenz genügt nicht. | Critical | L |
| RBA-05 | [P2–P4], [A] | PARTIAL | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/server/authz.go:30–98,129–155`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000055_cmdb_extensions.up.sql:614–667` | Katalog-/Route-Tests vorhanden, keine vollständige Phasen-/Aktionsmatrix; T1. | Fast alle genannten Module einschließlich IGA/AI besitzen Code und Rechte, aber Schlüsselmodelle/Aktionszuordnung weichen ab; bestehende Org-Seeds und neue Orgs erhalten nicht durchgängig dieselben Ergänzungen. Contract ohne Modulcode OFFEN, nicht G1-fällig. | High | L |
| RBA-06 | [P2] | ABWEICHEND | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/server/authz.go:210–246`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/citype/handler.go:40–58`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/override/handler.go:34–40`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/lifecycle/handler.go:35–40` | Ist-Routenprüfung, keine positiven/negativen Aktionsrechtstests für alle Unterrouten; T1. | Spezielle Permissions existieren, werden für verschachtelte Overrides/Instanzattribute/Transitions/Reconciliation-Einstellungen durch erstes Ressourcenpfadsegment umgangen. Beziehungstyp- und Lifecycle-Definitionsverwaltung sind getrennt gemappt; vorhandene P2-Implementierung wird normal bewertet. | High | M |
| RBA-07 | [P2], KI [A] | OFFEN | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/fieldmeta/fieldmeta.go:71–108,175–212`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/graphqlbff/graphqlbff.go:146–172,205–263`; Kanalabgleich unten. | `TestEvaluateVisibility`/`TestEvaluateRequiredAndReadOnly` betreffen datenabhängige Metamodellregeln, keine Identitäts-/Rollenfeldrechte. | Kein entsprechendes Feldrechte-/Grant-System vorhanden; dynamische Feldbedingungen sind kein Sicherheitsmodell, daher fehlende spätere Funktion OFFEN statt Baseline-FAIL. Bestehende Kanalstrukturen benötigen Erweiterung; aktuelle unabhängige Objekt-/Scope-Fehler stehen bei TEN/RBA-04/08. | – | – |
| RBA-08 | [B] | PARTIAL | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000014_phase2_users_teams_roles.up.sql:6–26`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/identity/apikey.go:32–45,149–155`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/webhook/webhook.go:17–38` | Memory-Teams, Fake-Keyvalidator und Webhook-Signaturtests, kein realer Service-Account-Scope-/Auditfluss. | Team-Unique/Mitglieder bestehen, Service-Accounts mit Rollen/Scopes fehlen; Keys/Webhooks sind nicht gleichwertige Akteure. Subscriptions erhalten volle Ereignisse ohne an einen Service-Account gebundene Objektleserechte. | High | L |
| ENT-01 | [B] | ABWEICHEND | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000004_entitlements.up.sql:3–14`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/entitlement/entitlement.go:118–126` | Memory-Grant-/Isolationstests, kein erfolgreicher PG-Vertragsnachweis; T1. | Org+Feature ist eindeutig und enabled vorhanden, aber skalares limit_value/expires_at statt limits JSONB/valid_until; source samt erlaubten Werten fehlt. Die geforderte differenzierte Kontingent-/Herkunftsdarstellung ist damit nicht umgesetzt. | High | M |
| ENT-02 | [B] | ABWEICHEND | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/entitlement/entitlement.go:30–45,129–136,241–251,278–283`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/export/export.go:35–58` | Default-/Limit2-Memorytests bilden nicht die exakten Keys und vier benannten Limits ab; T1. | cmdb/export statt cmdb_core/export_csv; topology/rack_view/api_access/notifications_email und benannte max_*-Kontingente fehlen, Core ist deaktivier-/ablauffähig. DATEV-Code besteht bereits ohne export_datev-Gate und wird daher normal geprüft. | High | L |
| ENT-03 | [B] | ABWEICHEND | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/entitlement/middleware.go:18–66`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/server/router.go:212–216`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/entitlement/entitlement.go:185–208,303–332` | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/ci/limit_test.go:32–50`: StubLimitGuard; Discoverytest ohne CI-Repository, kein realer Kontingentnachweis; T1. | Ingest-Alias umgeht Gate und neue Discovery-CIs umgehen LimitGuard; unlicensed_ci/Snapshot-/Resolveprozess fehlt. Limitfehler nutzt generischen 403-Typ, Cache lokale Map/30 s statt Redis/60 s mit übergreifender Invalidierung. | High | L |
| ENT-04 | [B] | ABWEICHEND | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/entitlement/handler.go:81–93`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000032_standard_role_seeds.up.sql:35,56–57`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/entitlement/pg_repository.go:78–85` | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/entitlement/handler_test.go:19–29`: MemoryRepository mit eingesetztem Tenant, keine Betreibergrenze. | GET ist vorhanden, Schreiben erfolgt jedoch über POST /api/v1/entitlements mit tenantseitigem entitlement:manage statt gesonderter Operator-Identität. org_admin kann eigene Freigaben/Limits/Ablaufdaten verändern. | High | M |
| ENT-05 | [B] | ABWEICHEND | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/export/export.go:35–76`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/graphqlbff/graphqlbff.go:146–150,175–205,234–259`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/frontend/src/hooks/useEntitlements.ts:18–92` | Middleware-/UI-Mappingtests, keine echte PG/Redis-Cross-Tenant-/GraphQL-Enforcementkette; keine GraphQL-Testdatei. | REST-Gates verfehlen Export-/Ingestpfade, GraphQL hat keinen Entitlementdienst und fehlende Featurezeilen fallen auf globalen DefaultPlan zurück. UI-Navigation spiegelt Teilfreigaben, nicht Limits/Ablauf; kein kostenpflichtiger GraphQL-Schreibbypass behauptet. | High | L |
| ENT-06 | (V), [B–P4], [A] | ABWEICHEND | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/entitlement/entitlement.go:49–94,241–251,352–361`; CH14 in `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/spec/katalog-v3/00-grundlagen.md` | Plan-Memorytests, kein getrenntes Paket-/Add-on-Abnahmeszenario; T1. | Vorgeschlagene Matrix/Limits sind nicht umgesetzt: nur Hinweis, kein Gate aufgrund der Vorschlagswerte. Unabhängig davon enthält Pro/Enterprise AI und Enterprise IGA implizit, im Widerspruch zur verbindlichen CH14-Add-on-Trennung. | High | M |
| ENT-07 | [B] | ABWEICHEND | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/entitlement/entitlement.go:129–136,224–238`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/collectorcmd/collectorcmd.go:251–303,355–371,403–405,523–549` | Ablauf-Memorytest/Collector-Offlinetest, kein Lizenzablauf→Pause→kein-Spool-/Warnfristtest. | Ablauf sperrt generisch auch andere Featurezugriffe inklusive Lesen, während Ingest-Alias ungeprüft bleibt und Collector weiter scannt/Fehler spoolt. Paused-Zustand, spezifische Messung und Banner/14-7-1-Benachrichtigung fehlen; Trialwerte bleiben unverbindlicher Vorschlag. | High | L |
| ENT-08 | [B] | ABWEICHEND | `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/entitlement/pg_repository.go:73–97`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/ci/service.go:136–167`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/discovery/discovery.go:713–740` | CI-Limit-Stubtest, kein vollständiger Downgrade-/Bestandsdaten-/Ingesttest. | Grant löscht keine CIs und REST unterscheidet Create/Update, aber Ingest-Neuanlagen umgehen Grenzen statt unlicensed_ci zu erzeugen. Ein vollständiger Downgradeprozess mit kanalübergreifender Bestandserhaltung/-nutzbarkeit ist nicht belegt. | High | L |

### Spätere Teilumfänge und nicht-gatende Prüfabgrenzung

RBA-07: Kein rollenabhängiges Feldrechte-Modell gefunden; datenabhängiges VisibleWhen/ReadOnlyWhen ist kein Teilnachweis dafür. Bestehende REST-/GraphQL-Payloads sind nicht feldprojektionsfähig nach Benutzerrollen; Suche verwendet einen Vektor pro Dokument statt pro lesbarem Feld (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000030_search_ai.up.sql:8–24`). Export hat kein Auslassungsmanifest, Webhooks keinen Service-Account-Feldfilter und AI nur Org-/Entitätstypprüfung (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/ai/retrieval.go:38–112`). Kein sichtunabhängiger ETag-Mechanismus nachgewiesen; kein erfundenes ETag-Leak. Das sind Anschlussstellen für P2/[A], kein Nachweis, dass die Architektur grundsätzlich nicht erweiterbar wäre.

### Tests und Prüfgrenzen

- **T1 – lokal ausgeführt:** In `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/`: `go test -short ./internal/tenant ./internal/tenant/rls ./internal/database ./internal/identity ./internal/permission ./internal/middleware ./internal/entitlement ./internal/server ./internal/graphqlbff ./internal/user ./internal/tenantapi -v`. Go `/opt/hostedtoolcache/go/1.25.14/x64/bin/go`, `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOFLAGS=-mod=readonly`, TEST_DATABASE_URL nicht gesetzt.
- **T1 Ergebnis:** Gesamtprüfung **ROT/Setup blockiert**, zehn Pakete `[setup failed]` wegen fehlendem Modulcache (`github.com/go-chi/chi/v5@v5.3.1: module lookup disabled by GOPROXY=off`). Nur `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/tenant/` grün: `TestRepositoriesUseCanonicalSessionVariable`, `TestTenantContext`, `TestTenantContextEmpty`. Das sind drei Tests, kein Lauf einer echten RLS-/Authentifizierungsintegration; Setupfehler sind keine fehlgeschlagenen fachlichen Assertions.
- **T2 – Build:** `go build ./...` im selben Verzeichnis und derselben Umgebung: **ROT**, Exit 1, derselbe Offline-Cacheblocker. Keine Abhängigkeiten installiert und kein erneuter Lauf mit Downloads. Rohlogs während der Session: `/tmp/reticora-audit-02/test-run.log` und `/tmp/reticora-audit-02/build-run.log` (nicht dauerhaft versioniert).
- **T3 – CI-Kontext:** [CI 35731844252](https://github.com/DataHub-Chiemgau/Reticora-CMDB/actions/runs/35731844252), Produktstand `b0f0ee2302e3af5afa41fbeaf738687fe1492504`, am 2026-09-22 erneut über Actions-MCP geprüft: Gesamtstatus failure. Details der separat ausgewerteten Jobs stehen in `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/audit/01-installation-stack.md`, Abschnitt „CI-Nachweise“: Backend-Job grün, Migrationsjob nach erfolgreichem up/down/up in Integrationstest an URL-Parsing gescheitert; kein grüner vollständiger Isolationstestnachweis. Keine Workflows ausgelöst.
- Frontend-/Browser-Tests nicht ausgeführt: `/home/runner/work/Reticora-CMDB/Reticora-CMDB/frontend/node_modules/` fehlt. Keine neue Testharness, Datenbank, Migration oder Produktdatei angelegt/verändert; keine Laufzeit-Exploitation behauptet. Quelltextnachweise und bestehende Mock-/Integrationstests werden getrennt ausgewiesen.
- Dokumentprüfung: unabhängige read-only Gegenprüfung, 36-ID-/Spalten-/105-Tabellen-/Quellenkontrolle und Secretscan durchgeführt. Automatisierte Code-Review war wegen nicht verfügbarem Modell technisch nicht ausführbar (trotz erfolgreicher Toolhülle); CodeQL übersprang ausschließlich die Markdownänderungen. Kein Ersatz für fehlende Produktintegrationstests.

## Befunde im Detail

### TEN-02/05 — Critical: Vollständiger Tabellen-/Policy-Abgleich

**Quellen und Lesart:** Alle 105 migrationsdefinierten Tabellen wurden gegen den Index `/home/runner/work/Reticora-CMDB/Reticora-CMDB/docs/audit/00-schema-ist.md:40–144` abgeglichen; dort steht je Tabellensymbol die Erstanlagemigration. Maßgebliche spätere Policyersetzungen: `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000023_rls_variable_unification.up.sql:13–67`, `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000033_client_scope_rls.up.sql:29–79`, `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000040_ci_type_global_read.up.sql:8–11`, `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000041_rls_gap_closure.up.sql:18–72` und `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000056_rls_enforcement.up.sql:62–105`. Nachfolgend kumulativer Istzustand, nicht ungeprüfte Anfangspolicies.

- **Org-Spalte:** NOT NULL auf 95 Tabellen; fehlend auf organization, permission, ci_type_attribute, team_member, user_custom_role; nullable auf ci_type, relationship_type, lifecycle_definition, lifecycle_state, lifecycle_transition. Org-Wurzel/globaler Permissionkatalog sind sachliche Ausnahmen, nicht automatisch Fremdmandantendaten. Nullable Metamodellzeilen und indirekte Orgableitung widersprechen dem unqualifizierten TEN-02-Wortlaut und benötigen eine explizite Katalogentscheidung.
- **ENABLE/FORCE:** Auf 103 Tabellen vorhanden; permission (global) und metric_sample (Mandantendaten) ohne beides. metric_sample_1h ist zusätzlich eine nicht RLS-abgesicherte Continuous-Aggregate-View, keine 106. Anwendungstabelle. Metrikhandler enthalten Org-Prädikate; fehlende DB-RLS allein wird nicht als bereits reproduzierter HTTP-Cross-Org-Leak ausgegeben.
- **Client:** 15 Tabellen besitzen client_id; davon site NOT NULL, 14 nullable. Nur site/ci/collector/subnet/contact/credential plus client über eigene id besitzen Clientprüfung; die SQL-Policies unterstützen bereits Mengen über ANY(string_to_array(...)::uuid[]) (Migration 33:35,42,68,73). Es fehlen autoritative Ableitung/Weitergabe dieser Mengen, nicht die Mengenfähigkeit der SQL-Clientbedingung. Die neun anderen direkten Clienttabellen sind einzeln markiert; NULL-Schreiben bleibt bei den fünf nullable clientgeprüften Tabellen ohne geforderte Orgrolle erlaubt.
- **Site:** Nur building, ci, subnet, location_node besitzen site_id (building NOT NULL); alle vier ohne Site-Prädikat in USING **und** WITH CHECK. site prüft auch seine eigene id nicht gegen Site-Scope. Room/Rack und weitere physische/objektbezogene Kinder tragen nicht durchgängig die geforderte denormalisierte Site-/Client-Zuordnung. Standort-FKs übertragen Eltern-RLS nicht automatisch.
- **Team:** owner_team_id kommt in keiner Tabelle vor, app.team_scope in keiner Policy. Es gibt daher keine Liste vorhandener owner_team_id-Tabellen mit bestandener Prüfung; ticket.team_id/team_member.team_id sind kein Ersatz. Das obligatorische Team-Schreib-/Lesemodell ist nicht umgesetzt.

**Policycodes:** O = Orgvergleich; OID = organization.id; C = Org+Client, einschließlich NULL-Client; CID = Org+client.id; G = NULL-Org oder eigene Org; P = Eltern-Orgprüfung; S = Org **oder** app.system=on; – = keine Policy. Jede Zelle nennt **USING / WITH CHECK**. Bei keiner Tabelle existiert eigenständige Site-/Teamprüfung. „Kind-/Objekt-Scope fehlt“ bezeichnet fehlende direkte/über Eltern vermittelte Begrenzung für scopepflichtige Daten, nicht die Behauptung, jede Zeile jeder Org-Verwaltungstabelle müsse client_id besitzen. **Jede fehlende Client-/Site-Bedingung in WITH CHECK scopepflichtiger Daten und jedes unerlaubte NULL-Client-Schreiben ist Critical.** Kein Live-Test jeder einzelnen Tabelle behauptet.

| Tabelle | USING / WITH CHECK | Einzelbefund bzw. Abgrenzung |
|---|---|---|
| organization | OID / OID | Org-Wurzel über eigene id; keine eigene organization_id. |
| client | CID / CID | Client über eigene id und UUID-Menge; Scopeherkunft/-Weitergabe fehlt. |
| site | C / C | client_id NOT NULL; Site-Scope über eigene id fehlt. |
| building | O / O | site_id NOT NULL, keine Client-/Site-Schreibschranke; client_id fehlt. |
| room | O / O | Eltern Building/Site begrenzen nicht; Client-/Site-Denormalisierung fehlt. |
| rack | O / O | Eltern Room/Site begrenzen nicht; Client-/Site-Denormalisierung fehlt. |
| ci_type | G / O | nullable Org/globales Metamodell; globale Sichtbarkeit ist keine getrennte DELETE-Sperre. |
| ci_type_attribute | P(global/eigener Typ) / P(eigener Typ) | Eigene Org-Spalte fehlt; indirekter globaler/Org-Typ, DELETE-Grenze separat. |
| ci | C / C | NULL-Client schreibbar; vorhandenes site_id ungeprüft. |
| audit_log | O / O | Privilegiertes Org-Audit; keine pauschale Clientpflicht. Vergabe von audit:read ist separat zu prüfen. |
| entitlement | O / O | Org-Vertragsdaten; Operatorgrenze ist ENT-04, keine Clientspalte erfunden. |
| app_user | O / O | Org-Identitäten; Rechte-/Scopeauflösung nicht durch diese Policy erbracht. |
| role | O / O | Org-Rollenkatalog, kein Clientobjekt; keine zusätzliche Clientpolicy erforderlich. |
| role_assignment | O / O | Orgweite Grantverwaltung; Scopeattribute beschreiben erteilte Rechte. Deren fehlende Auswertung gehört RBA-03, nicht zur Sichtbarkeit des Grantkatalogs. |
| ci_relationship | O / O | Endpunkt-CI-/Client-/Site-Scope fehlt. |
| webhook_subscription | O / O | Orgweite Integrationskonfiguration; kein pauschaler Clientfilter. Service-Account-/Payloadrechte separat RBA-08. |
| webhook_delivery | S / S | Administrative Zustellhistorie mit System-Schreibausnahme; kein pauschaler Clientfilter für privilegierte Orgzustellung. |
| collector | C / C | NULL-Client schreibbar; Clientmengen im SQL unterstützt. |
| discovery_job | O / O | Collector-/Zielscope fehlt. |
| discovery_result | O / O | Job-/Device-Scope fehlt. |
| asset | O / O | client_id vorhanden, Clientbedingung in beiden Teilen fehlt; physischer Site-Scope fehlt. |
| assignment | O / O | Asset-/Client-/Standortscope fehlt. |
| document | O / O | Orgweite Bibliothek legitim; bei Objektbindung keine Ableitung von dessen Scope. Blobgrenze zusätzlich verletzt. |
| document_link | O / O | Verknüpfte Objekte vermitteln keinen Scope. |
| stocktake | O / O | Orgweiter Inventurkopf legitim; site/room/rack-Scope ohne verbindliche Zielberechtigung. Objektgrenzen zusätzlich bei Scans prüfen. |
| stock_scan | O / O | Inventur-/Assetscope fehlt. |
| ticket | O / O | Ungebundene Orgtickets möglich; bei CI-/Assetbindung keine Objekt-Scopeableitung. team_id ohne Team-RLS. |
| ticket_comment | O / O | Scope eines fachlich beschränkten Tickets wird nicht abgeleitet; Sichtbarkeit ungebundener Orgtickets separat zu entscheiden. |
| team | O / O | Org-Teamkatalog; owner_team-Modell fehlt. |
| team_member | P(Team-Org) / P(Team-Org) | Eigene Org fehlt, referenzierte User-Org nicht zusätzlich durch Policy geprüft. |
| custom_role | O / O | Org-Rollenkatalog, kein eigener Clientobjektanspruch. |
| user_custom_role | P(Rollen-Org) / P(Rollen-Org) | Eigene Org fehlt; Benutzer-Org nicht zusätzlich geprüft. Scopeattribute sind Grantmetadaten, deren Auswertung gehört RBA-03. |
| network_interface | O / O | CI-/Client-/Site-Scope fehlt. |
| subnet | C / C | NULL-Client schreibbar; vorhandenes site_id ungeprüft. |
| ip_address | O / O | Subnet-/CI-Scope fehlt. |
| cable | O / O | Endpunkt-/Standortscope fehlt. |
| contact | C / C | NULL-Client schreibbar; fehlendes site_id laut TEN-05 hier zulässig. |
| ci_contact | O / O | CI-/Contact-Scope fehlt. |
| export_job | S / S | System-Schreibausnahme, Initiatorscope fehlt bei Job/Download. |
| api_key | O / O | Org-Keydaten; Owner-/Service-Account-Scope nicht umgesetzt. |
| user_invitation | O / O | Org-Einladung; kein vollständiger Zulassungsfluss. |
| metric_sample | – / – | Mandantendaten ohne ENABLE/FORCE, ohne Org-/Client-/Site-WITH-CHECK. |
| rack_mount | O / O | Rack-/CI-/Standortscope fehlt. |
| relationship_suppression | O / O | Beziehungs-/Endpunktscope fehlt. |
| credential | C / C | NULL-Client schreibbar; zusätzliche Decrypt-Rechteabweichung RBA-04. |
| org_dek | O / O | Org-Schlüsselmaterial; kein Clientobjekt, keine automatische Dedicated-Instance-Erfüllung. |
| review_item | O / O | Device-/CI-/Collector-Scope fehlt. |
| ci_change | O / O | CI-/Client-/Site-Scope fehlt. |
| permission | – / – | Globaler Katalog ohne Org; nicht als ungeschützte Tenantdaten eingestuft. |
| role_permission | O / O | Org-Rollenkatalog; keine Scopeauflösung durch diese Policy. |
| sla | O / O | client_id vorhanden, Clientbedingung USING/CHECK fehlt. |
| ticket_sla | O / O | Ticket-/SLA-Scope fehlt. |
| form_def | O / O | client_id vorhanden, Clientbedingung USING/CHECK fehlt. |
| form_submission | O / O | Formular-/Bezugsobjektscope fehlt. |
| workflow_def | O / O | Org-Regeldefinition; Ausführung/Bezugsobjekte benötigen eigene Scopebindung. |
| workflow_run | O / O | Orgweite Laufverwaltung legitim; für Fachdaten im JSON-Kontext fehlt Herkunftsscope. Kein pauschaler Live-Bypass nachgewiesen. |
| workflow_step | O / O | Orgweite Ablaufverwaltung legitim; objektbezogene Inputs/Outputs erben keinen Quellobjektscope. |
| compliance_rule | O / O | Org-Regeldefinition; keine Objekt-Scopeprüfung dadurch. |
| compliance_result | O / O | Regelziel-/Objektscope fehlt. |
| iga_connector | O / O | Org-Connector, kein Clientobjekt aus Definition abgeleitet. |
| iga_provisioning_task | O / O | Orgweite Accountverwaltung, kein Inventar-Clientdatensatz; keine pauschale Clientpflicht. |
| iga_lifecycle_policy | O / O | Orgweite Identitätsregeln, keine zusätzliche Client-/Sitepolicy erforderlich. |
| iga_access_request | O / O | Accountrechteprozess ohne inhärenten Inventar-Clientscope; Personen-/Entscheiderrechte separat. |
| iga_access_review | O / O | Orgweiter Governanceprozess, keine zusätzliche Clientpolicy erforderlich. |
| iga_access_review_item | O / O | Benutzer-/Connector-/Entitlementprüfung; Reviewerzuständigkeit separat, kein inhärenter Inventar-Clientscope. |
| iga_drift_finding | O / O | Orgweite Accountabweichungen, kein Inventar-Clientdatensatz; keine pauschale Clientpflicht. |
| search_document | O / O | Kopierter Client-/Site-/Teamscope fehlt. |
| ai_conversation | O / O | Gesprächsverwaltung ohne zwingenden Client-/Sitebezug; Benutzerbesitz und Inhaltsrechte separat zu prüfen. |
| ai_message | O / O | Conversationnachrichten ohne pauschale Clientpflicht; Gesprächszugang und referenzierte Fachdaten separat. |
| ai_chunk | O / O | Quellobjekt-/Client-/Site-Scope fehlt. |
| webhook_dead_letter | S / S | Administrative Zustellfehler mit System-Schreibausnahme; kein pauschaler Clientfilter für privilegierte Orgverwaltung. |
| alert_rule | S / S | Orgweite Regel nach metric_name ohne konkrete CI-Bindung; System-Schreibausnahme. Kontextlose Workerupdates separat defekt. |
| privacy_retention_policy | O / O | Org-Regeldefinition; Scope der bearbeiteten Daten separat erforderlich. |
| collector_enrollment_code | S / S | System-Schreibausnahme für Bootstrap; kein normaler Tenantjob. |
| consumable | O / O | client_id vorhanden, Clientbedingung USING/CHECK fehlt. |
| stock_movement | O / O | Bestand-/Standort-/Clientscope fehlt. |
| internal_order | O / O | client_id vorhanden, Clientbedingung USING/CHECK fehlt. |
| internal_order_item | O / O | Order-/Bestandsobjektscope fehlt. |
| maintenance_window | O / O | Orgweite Wartungsplanung legitim; bei Ausgabe CI-bezogener Fachdaten keine abgeleitete Zugriffsbeschränkung. |
| maintenance_window_ci | O / O | CI-/Window-Scope fehlt. |
| maintenance_notification | O / O | client_id vorhanden, Clientbedingung USING/CHECK fehlt. |
| disposal_record | O / O | Bei Asset-/CI-Bindung kein Objektscope; ungebundene orgweite Nachweise möglich. Append-only ersetzt keine Leseberechtigung. |
| key_item | O / O | client_id vorhanden, Clientbedingung USING/CHECK fehlt. |
| key_assignment | O / O | Key-/Empfängerscope fehlt. |
| training | O / O | Org-Schulungskatalog; kein Clientobjekt aus Definition abgeleitet. |
| training_assignment | O / O | Personenbezogene Schulungszuweisung, kein Inventar-Clientdatensatz; persönlicher Zugriff ist separate Authz. |
| desk | O / O | Bei Raumzuordnung fehlt Standortscope; ungebundener orgweiter Arbeitsplatz möglich. |
| desk_booking | O / O | Ein gegebenenfalls vorhandener Standortscope des Desks wird nicht übernommen. |
| asset_location | O / O | Asset-/Standort-/Clientscope fehlt. |
| endpoint_agent | O / O | Orgweite ungebundene Agentverwaltung möglich; bei CI-Bindung fehlt dessen Scope. |
| security_finding | O / O | CI-/Assetscope fehlt. |
| ci_instance_field_definition | O / O | CI-/Client-/Site-Scope fehlt. |
| relationship_type | G / O | nullable Org/globaler Katalog; DELETE-Semantik gesondert. |
| lifecycle_definition | G / O | nullable Org/globaler Katalog; DELETE-Semantik gesondert. |
| lifecycle_state | G / O | nullable seit Migration 56; globale Sichtbarkeit, DELETE-Semantik gesondert. |
| lifecycle_transition | G / O | nullable seit Migration 56; globale Sichtbarkeit, DELETE-Semantik gesondert. |
| location_node | O / O | client_id und site_id vorhanden, beide Bedingungen USING/CHECK fehlen. |
| asset_movement | O / O | Asset-/Start-/Zielstandortscope fehlt. |
| quantity_item | O / O | client_id vorhanden, Clientbedingung USING/CHECK fehlt. |
| reservation | O / O | Ressourcen-/Client-/Standortscope fehlt. |
| composition | O / O | Komponenten-/Asset-/CI-Scope fehlt. |
| ci_field_value | O / O | CI-/Client-/Site-Scope fehlt. |
| source_priority_policy | O / O | Org-Regeldefinition; Scope der Ziel-CIs separat nötig. |
| entity_change | O / O | Bezugsobjekt-/Client-/Site-Scope fehlt. |
| saved_view | O / O | Gespeicherte Org-Ansicht; Abfrageengine verliert Zielobjektscope. |

Die fünf S-Policies stammen aus `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000024_durable_webhook_delivery.up.sql:21–30`, `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000034_webhook_dead_letter.up.sql:28–40`, `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000035_export_job_formats.up.sql:13–22`, `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000037_alert_rule.up.sql:27–37` und `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000043_enrollment_system_read.up.sql:8–17`. Diese globalen Systemzweige gelten auch schreibend; nicht als nachgewiesener extern auslösbarer Systemflag-Bypass missverstehen.

**NULL-Schreibpfad:** `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/ci/handler.go:140–143` übernimmt optionalen Client, `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/ci/pg_repository.go:262–265,321–333` setzt leeren Client auf NULL. Beide Policyhälften erlauben dies; keine zusätzliche Orgrollenprüfung im CI-Service. Tests prüfen shared Reads und fremden benannten INSERT, nicht shared INSERT/UPDATE/DELETE. Bei globalen G/P-Zeilen schützt WITH CHECK generell nicht DELETE; zusätzliche Repositoryprädikate und vorhandene Daten sind separat zu beachten, kein pauschaler API-Löschangriff behauptet.

**Empfohlene Korrektur:** Tenant-/globale Metadatenausnahmen verbindlich definieren; fehlende Spalten und direkte/abgeleitete Client-/Site-/Team-Policies einschließlich strengerer NULL-Schreibregeln ergänzen. Sichtbarkeit und Änderungsrecht ausdrücklich trennen, Systemjobs nach TEN-06 pro Org ausführen und alle Tabellen mit echten negativen Schreibtests abnehmen.

### TEN-03 — High: Rollenhärtung belegt, Owner-/DDL-Vertrag unvollständig

`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/cmd/server/main.go:156–167` nutzt den Produktpool; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/database/pool.go:52–76,112–130` führt pro Verbindung SET ROLE aus und verweigert effektiven Superuser/BYPASSRLS. Der Compose-Login darf deshalb nicht fälschlich mit der effektiven App-Rolle gleichgesetzt werden.

`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000056_rls_enforcement.up.sql:23–78` vergibt USAGE/DML/Funktionsrechte auf public; expliziter Schema-CREATE-/Tabelleneigentumsvertrag für Laufzeitindizes fehlt. Eigentümer bleiben ohne explizite Umwidmung die jeweiligen Ersteller; wer die Migrationen in einer konkreten Installation ausgeführt hat und welche geerbten/PUBLIC-Rechte bestehen, ist **N/P ohne Live-Katalogabfrage**. Keine Behauptung, die App-Rolle sei zwingend Tabellen-Owner oder sämtliche DDL-Rechte sicher entzogen. FORCE ist bei allen bereits RLS-aktivierten Anwendungstabellen gesetzt und würde auch einen Owner binden, nicht jedoch fehlende RLS auf Metriken ersetzen. Startup prüft Rollenattribute, nicht alle relrowsecurity/relforcerowsecurity-/Owner-/DDL-Eigenschaften.

**Empfohlene Korrektur:** Beschränkten Anwendungs-DDL-/Ownership-Vertrag definieren und effektive Schema-, Tabellen-, Mitgliedschaftsrechte sowie FORCE im echten Deployment unter App-Rolle nachweisen; keine Superuser-/BYPASSRLS-Abkürzung zur Reparatur fehlender Grants.

### TEN-04/06 — Critical: Vollständiges Inventar der PostgreSQL-Einstiege

**Zählweise:** 80 direkte Aufrufstellen in 58 Nicht-Test-Go-Dateien (Query/QueryRow/Exec, database/sql-Varianten, SendBatch, Begin/BeginTx/Acquire); SQL auf bereits geöffneter Transaktion nicht nochmals gezählt. 46 private withTenant-Helfer + 2 inTx-Helfer + 32 weitere Aufrufstellen. Keine weiteren PostgreSQL-Zugänge in Collector/Edgecore gefunden. GraphQL delegiert an dieselben Repositories, besitzt keinen zusätzlichen direkten Poolzugriff.

**Produktabgrenzung:** Davon 70 verdrahtete Produktaufrufstellen in 55 Dateien. Sieben Stellen unbenutzter zentraler WithTenant-/SetTenantContext-Varianten, zwei in cmd/audit-seed und PGAPIKeyStore.Save ohne produktiven Aufrufer sind unten separat gekennzeichnet, nicht als aktive Request-/Workerpfade gezählt. Orgweite Administrationshelfer benötigen nicht pauschal Clientfilter, erfüllen aber ebenfalls nicht den zentralen Fünf-GUC-Vertrag.

Alle privaten Helfer öffnen eigene Transaktionen mit `set_config(..., true)`, keine gemeinsame Requesttransaktion. **Produktiv kein sessionsweites Tenant-set_config(false) gefunden**; Schutz vor Poolleaks ist deshalb nicht fälschlich als vollständig fehlend dargestellt. Jedoch fehlen nötige Variablen/Scopeableitungen; leerer Client-Kontext bedeutet unbeschränkt. Drei zentrale WithTenant-Varianten existieren ohne produktive Aufrufer. Auch der unbenutzte SetTenantContext auf *sql.DB ist nicht sicher: mehrere lokale Autocommit-Statements garantieren weder gemeinsame Verbindung noch wirksame Folgetransaktion.

**Acht produktive `PGRepository.withTenant` mit Org und optionalem Client-Scope-String:** keiner setzt User/Site/Team. Die SQL-Policies können kommaseparierte UUID-Mengen auswerten; dies ersetzt nicht deren korrekte Herleitung.

| Direkte Begin-Stelle | Befund |
|---|---|
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/ci/pg_repository.go:68` | Org/Client lokal; unvollständiger Scopevertrag. |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/contact/pg_repository.go:25` | Ebenso. |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/credential/pg_repository.go:46` | Ebenso. |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/discovery/pg_repository.go:40` | Ebenso, nur Teile der Discoverytabellen werten Client aus. |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/ipam/pg_repository.go:26` | Ebenso, nur Subnet unter Clientpolicy. |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/privacy/pg_repository.go:23` | Ebenso. |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/relationship/pg_repository.go:45` | Client gesetzt, Relationshippolicy ignoriert ihn dennoch. |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/tenantapi/pg_repository.go:26` | Kindtabellen erben keine Client-/Site-Policy. |

**38 produktive private Tenanthelfer mit nur Org:** Alle folgenden Begin-Stellen setzen weder Client noch Site/Team/User. Bei reinen Org-Verwaltungsdaten ist das kein erfundener Client-Leak; bei den in der Policy-Matrix benannten scopepflichtigen Daten ist der Vertrag unvollständig.

| Direkte Begin-Stelle (`PGRepository.withTenant`, bei Alerts `PGAlertStore.withTenant`) |
|---|
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/agent/pg_repository.go:27` |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/ai/pg_repository.go:16` |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/asset/pg_repository.go:51` |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/assignment/pg_repository.go:48` |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/citype/pg_repository.go:53` |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/compliance/pg_repository.go:21` |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/composition/pg_repository.go:35` |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/consumable/pg_repository.go:26` |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/desk/pg_repository.go:29` |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/disposal/pg_repository.go:27` |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/document/pg_repository.go:68` |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/entitlement/pg_repository.go:23` |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/form/pg_repository.go:23` |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/history/pg_repository.go:31` |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/iga/pg_repository.go:18` |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/keymgmt/pg_repository.go:29` |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/lifecycle/pg_repository.go:25` |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/location/pg_repository.go:26` |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/locationnode/pg_repository.go:32` |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/maintenance/pg_repository.go:29` |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/monitoring/pg_alert_store.go:27` |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/movement/pg_repository.go:25` |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/order/pg_repository.go:28` |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/override/pg_repository.go:34` |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/permission/pg_repository.go:19` |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/rack/pg_repository.go:25` |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/relationshiptype/pg_repository.go:32` |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/reservation/pg_repository.go:33` |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/savedview/pg_repository.go:31` |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/search/pg_repository.go:19` |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/security/pg_repository.go:27` |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/sla/pg_repository.go:26` |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/stocktake/pg_repository.go:63` |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/ticket/pg_repository.go:60` |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/training/pg_repository.go:29` |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/user/pg_repository.go:81` |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/webhook/pg_repository.go:34` |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/workflow/pg_repository.go:24` |

**Zwei dynamische Helfer:** `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/export/pg_job_repository.go:30` `PGJobRepository.inTx` und `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/webhook/pg_delivery_store.go:28` `PGDeliveryStore.inTx` setzen **entweder** Org **oder** app.system lokal. ClaimPending/ClaimDue sind produktive globale Worker, keine Wartung und keine Erfüllung der geforderten Iteration pro Org.

**Übrige 32 direkte Aufrufstellen** (mehrere Zeilen pro Symbolgruppe explizit angegeben):

| Datei / Zeilen / Symbol | Klassifikation |
|---|---|
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/database/pool.go:57` AfterConnect; `:115` VerifyRLSEnforced | Zulässige Rolleninitialisierung/Metadatenprüfung, keine Tenantdaten. |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/database/pool.go:154,160` WithTenant | Unbenutzter zentraler Acquire-/Begin-Pfad, nur Org/Client. |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/platform/db/db.go:42` WithTenant | Unbenutzte Variante mit Org/Client/User, ohne Site/Team. |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/tenant/rls/rls.go:65` WithTenant | Unbenutzte database/sql-Variante, ebenso unvollständig. |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/tenant/rls/rls.go:25,34,44` SetTenantContext | Drei ExecContext auf *sql.DB; lokale Autocommit-GUCs ohne gebundene Folgetransaktion; unbenutzt, kein produktiver Session-Leak behauptet. |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/permission/pg_repository.go:34` ListPermissions | Globaler Katalog, sachlich begründbare Nicht-Tenant-Ausnahme. |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/identity/apikey_store.go:31,78,96` LookupByPrefix/Save/MarkUsed | Kontextlose RLS-Zugriffe; Lookup/MarkUsed Authpfad, Save ohne produktive Verwaltungsroute. Unter strikter App-Rolle kein Treffer/Fehler statt bewiesenem Bypass. |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/monitoring/pg_store.go:57,82,94,111` Ingest/queryRaw/queryBucketed/queryHourly | Batch/Reads ohne Tenanttransaktion; Org explizit gesetzt/gefiltert, keine Client-/Site-/CI-Prüfung und keine DB-RLS. |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/monitoring/pg_alert_store.go:174` ListEnabled | Globaler produktiver Worker-Read, lokale Systemausnahme. |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/monitoring/pg_alert_store.go:202,215,229` MarkPending/MarkFired/ClearPending | Produktive Updates ohne Org/System-GUC; erwartbar keine Änderung/Fehler unter RLS, kein bewiesenes Cross-Org-Schreiben. |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/discovery/pg_repository.go:584` RedeemEnrollmentCode | Öffentlicher Bootstrap mit lokaler Systemausnahme; Org erst aus Code, ausdrücklich gesondert zu prüfen. |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/audit/audit.go:203,328` Verify/Handler.list | Eigene Orgtransaktionen für privilegiertes Audit; Verify auch per HTTP/Complianceadapter produktiv, daneben Wartung. |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/lifecycle/states.go:38,61,96` CurrentState/SetState/LifecycleKeyFor | Nur Org bei direktem CI-/Assetzugriff; LifecycleKeyFor verwirft Request-Kontext zugunsten Background. |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/reservation/availability.go:29` Availability | Eigene Transaktion nur Org. |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/reservation/pg_repository.go:182` ExpireDue | Produktiver Worker ohne Tenant-/System-GUC, keine Mandanteniteration; Sweeper ignoriert Fehler (:40–44 in `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/reservation/sweeper.go`). |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/savedview/query.go:41` Query | Nur Org bei direkten CI-/Asset-Attributabfragen; Scope geht verloren. |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/cmd/audit-seed/main.go:37,49` main | Wartungs-/DR-Werkzeug, kein HTTP-Einstieg; eingeschränkter Pool ohne benötigte GUC, damit funktionale RLS-Problematik. |

Zusätzlich eingeordnet: NewMaintenancePool (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/database/pool.go:87`) nur Wartung/Fixtures, keine produktive API-Verwendung; Connect (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/database/database.go:22`) in Test-/Fixturezugängen; alternativer NewPool (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/platform/db/db.go:19`) ohne produktiven Aufrufer. `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/cmd/audit-verify/main.go:33–40` delegiert an org-lokales audit.Verify. Ping/Close/Release sind keine zusätzlichen Tenantdatenzugriffe.

**Empfohlene Korrektur:** Einen kanonischen, gebundenen Tenanttransaktionseinstieg mit allen fünf lokal gesetzten/zurückgesetzten Werten verwenden; echte mengenwertige Scopes, NULL explizit. Systemjobs nach TEN-06 pro Org, privilegierte Bootstrap-/Wartungsausnahmen eng begrenzen. Alle oben genannten Produktpfade einschließlich GraphQLdelegation/Worker über dieselben Regeln führen und Commit/Rollback/Fehler/Poolreuse testen.

### TEN-09 — High: Isolationstests sind weder vollständig noch aktuell grün belegt

| Quelle / Test | Belegte Absicht / Prüflücke |
|---|---|
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/tenant/tenant_test.go:8,26` TestTenantContext* | Go-Kontext, keine DB; in T1 grün. |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/tenant/rls_variable_test.go:15` TestRepositoriesUseCanonicalSessionVariable | Prüft veraltete GUC-Namen, nicht vollständige Werte oder Datenpfade; in T1 grün. |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/tenant/rls/rls_test.go:25,36` | Fehlende Org vor Query, kein erfolgreicher DB-/Lokalitätsbeweis. |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/database/integration_test.go:30` TestMigrationsApplied | Existenz von sieben Tabellen, keine komplette Policyprüfung. |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/database/rls_isolation_test.go:98` TestTenantIsolationRLS | CI/Subnet/Ticket-Orgtrennung und Cross-Org-INSERT; nicht alle Tabellen/UPDATE/DELETE. |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/database/rls_isolation_test.go:189` TestClientScopeRLS | Eigene/shared CI und Clientliste, fremder benannter INSERT abgewiesen; kein NULL-Schreiben/Site/Team. |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/tenant/rls/isolation_integration_test.go:134,164` | Rollenstartprüfung, ausgewählte Cross-Org-Reads/Writes/Relationship-FK; kein Owner-/DDL-/Allpfadbeweis. |
| `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/database/traversal_dlq_test.go:13,142` | Org-Traversal/Dead-Letter; keine vollständige Client-/Site-/Team-Workerprüfung. |

DB-Tests überspringen ohne TEST_DATABASE_URL; Rollenstarttest zusätzlich bei bereits eingeschränkter Loginrolle. Besonders relevant: `scoped()` in `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/tenant/rls/isolation_integration_test.go:117–127` setzt app.org_id mit **false sessionsweit über pool.Exec**; spätere Poolqueries sind nicht an dieselbe Verbindung gebunden. Das ist eine Testschwäche, kein nachgewiesener produktiver Pool-Leak.

Fehlende Abdeckung einzeln: Cross-Site-INSERT/UPDATE/DELETE; Cross-Client-Updates/Deletes und gemeinsame NULL-Zeilen; Team/NULL-Team; Poolreuse aller fünf GUCs; Exportjobs/Download; Benachrichtigungen (kein SMTP-Modul); Review-Items/Resolve; Location-Kinder/Bewegungen; Suchindex/strukturierte Suche; Lifecycle; Alert-/Reservation-Worker; fremde Dokument-Blobkeys. **Empfohlene Korrektur:** Echte App-Rollen-Integration über alle diese Pfade mit Mengen-/Schreibabweisung und gebundenen Transaktionen; keine vorhandenen Tests hier verändert.

### TEN-10 — High: Organisations-VRF fehlt

`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000015_network_ipam.up.sql:22–52` modelliert Org+CIDR bzw. Org+Adresse ohne VRF. Weder Org-VRF-Tabelle/name-Unique/description/is_default noch Anlage von global bei neuer Org, CRUD oder vrf:manage im Katalog vorhanden. **Empfohlene Korrektur:** VRF als Org-Ressource mit Defaultanlage und eindeutiger Subnetzzuordnung pro Org/VRF/CIDR ergänzen und Org-Anlage-/CRUD-/Scopefälle testen; keine Ausweichlösung über Client-VRFs (CH10).

### TEN-06/09 — Critical: Metadatenbesitz schützt nicht den signierten Blobzugriff

- **Beschreibung / Dateien:** `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/document/handler.go:167–174` übernimmt einen vom Aufrufer angegebenen storage_key beim Anlegen eines eigenen Org-Dokuments; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/document/pg_repository.go:190–218` speichert ihn unverändert. Der Content-Endpunkt (:297–324 im Handler) prüft die Metadatenzeile, signiert danach aber diesen Key; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/platform/blob/blob.go:250–269` kennt dafür keine Tenantprüfung.
- Voraussetzung ist gemeinsamer konfigurierter S3-Speicher, document:write/read und Kenntnis eines existierenden fremden Objektkeys. Ein Angreifer kann ihn an eigene Metadaten binden und einen neuen gültigen Downloadlink erhalten, ohne einen noch gültigen fremden Link besitzen zu müssen. Keine Behauptung, zufällige Objekt-IDs seien erratbar, und kein fremdes Objekt wurde hier tatsächlich abgerufen. Der serverseitig erzeugte Uploadkey (:280) schützt den getrennten Metadaten-Anlagepfad nicht.
- **Empfohlene Korrektur:** Storagekeys serverseitig vergeben und Objektbesitz/-namespace auch für bestehende Metadaten vor Signierung prüfen; reale Zwei-Org-Tests für Anlage/Download/Versionen und manipulierte Keys. Mandantenisolation umfasst alle Zugriffspfade, nicht nur RLS der Dokumentzeile.

### TEN-04/05/06 — Critical: Scopeherkunft, Export und Suchkopien

- **Beschreibung / Dateien:** `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/identity/handler.go:253–276` erzeugt Sessionrechte aus IdP-Gruppen, nicht DB-Rollenzuweisungen; ClientScope bleibt leer. `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000033_client_scope_rls.up.sql:64–73` interpretiert leeren Client-Scope als org-weit. Ein DB-clientbeschränkter Nutzer mit IdP-reader/editor-Gruppe kann daher eine org-weite Session erhalten; frischer Login behebt den verlorenen DB-Scope nicht.
- Exportjobs speichern keine verbindlichen Aufruferscopes (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/export/job.go:20–43`), nur aufrufergesteuerte Filter. Der Worker startet mit Background-Kontext und rendert org-weit, wenn der optionale Clientfilter fehlt (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/cmd/server/main.go:268–272`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/export/worker.go:95–97`). Jobliste und signierte URLs prüfen nur Org, nicht Initiator-/Inhaltsscope (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/export/pg_job_repository.go:81–119`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/export/job_handler.go:95–103,133–145`).
- Suche kopiert Inhalte ohne Client-/Site-/Team-Zuordnung und filtert nach Entitätstyprecht, nicht Einzelobjektscope (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/search/model.go:11–32`, `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/search/pg_repository.go:18–30,60–82`, `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/search/handler.go:37–43,65–80`). Dies ist ein Client-Scope-Befund, kein nachgewiesener Bypass des Org-Prädikats.
- **Prüfgrenze:** Quelltextbestätigte Verlustpfade; keine Laufzeitreproduktion mit künstlich erzeugten clientbeschränkten Produktionstokens. Der aktuelle Login gibt solche verlässlich beschränkten Tokens gerade nicht aus, der strikte API-Key-Pfad ist zusätzlich vor Auth an RLS blockiert. Diese Einschränkungen widerlegen nicht den nachgewiesenen Scopeverlust bei DB-Rollen oder die fehlende Begrenzung der Job-/Indexmodelle.
- **Empfohlene Korrektur:** Autoritative Scopes durchgängig aus Rollen ableiten; bei Jobs speichern und beim Rendern/Download anwenden, Suchkopien scopefähig machen bzw. gegen lesbare Quellobjekte validieren. Zwei-Client-/Zwei-Site-/Team-Tests einschließlich Exportfremdjobs und Suchtreffern, nicht nur Filterparameter prüfen.

### AUT-01/02 — Critical: Lokale Sperrung und Rechteentzug werden umgangen

- **Beschreibung / Dateien:** PKCE/State im Browser (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/frontend/src/auth/oidc.ts:128–175`) und kryptografische ID-Tokenprüfung (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/identity/oidc.go:202–346`) sind vorhanden. Trotzdem erzeugt `EnsureUser` beim Callback einen Upsert nach oidc_subject, ersetzt fehlende E-Mail synthetisch und setzt is_active=true, auch bei bewusst gesperrten Bestandskonten (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/user/pg_repository.go:972–992`). Gültige Einladung/Self-Signup-Zulassung und verifizierte Org/E-Mail-Zuordnung werden nicht verlangt.
- Ein lokal bzw. per SCIM deaktivierter Benutzer mit weiterhin gültigem IdP-Konto erhält so wieder eine Session. Bereits ausgegebene Sessions behalten alte Permissions: `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/identity/handler.go:155–175,415–418` erneuert signierte Tokens ohne aktuellen Status/Rollen zu laden. Öffentliches /auth/refresh (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/middleware/auth.go:217–219`) eröffnet bei jeder Erneuerung das nächste Fenster; die nachgelagerte Autorisierung vertraut dem Token.
- RS256 ist korrekt als Teilfunktion, aber `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/identity/types.go:215–223` verwendet sub/org_id/permissions/skalares client_scope/iat/exp statt verlangter Claims einschließlich cls/sts/tms/name/email. TTL ist 1 h, kein kid, kein geschütztes rotierendes Refresh-Cookie und keine Redis-Blacklist. Logout in `/home/runner/work/Reticora-CMDB/Reticora-CMDB/frontend/src/App.tsx:189–202` entfernt Browserzustand/führt zu Keycloak, widerruft nicht das interne JWT.
- **Empfohlene Korrektur:** Aktive Bestandsidentität getrennt von zugelassenem Erstlogin behandeln, Sperrstatus niemals im Login-Upsert zurücksetzen. 15-min-Access-Token mit vollständigen Claims/kid; getrennte serverseitig registrierte rotierende Refresh-Credentials im HttpOnly/Secure/SameSite-Cookie, aktuelle Rechte beim Refresh, unmittelbarer Widerruf bei Deaktivierung/Logout. Echte IdP/DB-Tests für Einladung, Sperrung, Rollenänderung, Logout und Replay; Vorschlag/Phase SCIM nicht mit Baseline-Zulassung vermischen.

### AUT-04 — High: API-Key-Lebenszyklus und Owner-Begrenzung fehlen

- **Beschreibung / Dateien:** `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/identity/apikey.go:74–97,130–185` nutzt crypto/rand, SHA-256, Constant-Time-Hashvergleich und prüft Ablauf/Widerruf. Format ist jedoch rk_live_ + 12 Base62 + Unterstrich + 40 Base62; rk_test_ fehlt. Generate allein persistiert nicht; vollständige Verwaltungsrouten/Rotation/Überlappung fehlen. PostgreSQL speichert Hash/Metadaten statt Klartext (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/identity/apikey_store.go:68–86`), doch einmalige Klartextausgabe über einen produktiven Erstellungsfluss ist nicht nachgewiesen.
- Validate gibt gespeicherte Permissions unverändert zurück (:149–154), keine Schnittmenge mit aktuellen Inhaberrechten. created_by ersetzt weder Owner-Modell noch Service-Account-Identität. Vor-Tenant-Poollookup in `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/identity/apikey_store.go:23–31` trifft auf RLS; ohne erfolgreichen PG-Test kein Nachweis funktionierender produktiver Key-Authentifizierung. Daraus wird kein erfundener externer RLS-Bypass abgeleitet.
- **Empfohlene Korrektur:** Exaktes Format/Versionspräfix, vollständigen Erstellungs-/Widerrufs-/Rotationsfluss mit Überlappung und einmaliger Ausgabe; Owner-Rechte live begrenzen, Tenant-/Scopebindung erhalten und Pre-Auth-Lookup sicher unter realer App-Rolle testen. Trennzeichenfrage mit Katalog klären, nicht still normalisieren.

### AUT-08 — High, [P4]: SCIM-Bausteine ohne vollständigen Provisionierungszyklus

- **Beschreibung / Dateien:** Users/Groups-Routen in `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/iga/handler.go:64–76`; Outbound-CRUD/Paging/Disable/Groups in `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/iga/connector.go:82–189`. Group-PATCH ist PUT-Alias statt SCIM-Operations (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/iga/scim.go:280–300`), User-PATCH deckt nur active/displayName. Der TaskRunner wird konstruiert, aber RunDue/RunTask nicht produktiv aufgerufen (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/iga/jml.go:60–92`, Handler :29–37); TestConnector prüft nur Konfiguration.
- **Empfohlene Korrektur:** Protokollsemantik und produktive Ausführung ergänzen; Deprovisionierung mit Session-/Key-Widerruf verbinden und echten Provider-Roundtrip testen. P4-Vollständigkeit allein ist kein G1-Blocker, aktuelle Sperrungsumgehung gehört zusätzlich AUT-01/02.

### AUT-09 — Critical: Bekanntes Administratorkonto in produktivem Realm-Import

- **Beschreibung / Dateien:** `/home/runner/work/Reticora-CMDB/Reticora-CMDB/deploy/keycloak/realm-reticora.json:122–149` enthält ein aktiviertes Anwendungskonto mit Administrationsgruppe und festem nichttemporärem Demo-Passwort; der Passwortwert wird hier absichtlich nicht reproduziert. `/home/runner/work/Reticora-CMDB/Reticora-CMDB/install-cloud.sh:894–901` individualisiert URLs/Client-Secret, nicht dieses Benutzerpasswort. Compose importiert den Realm (:110–137 in `/home/runner/work/Reticora-CMDB/Reticora-CMDB/deploy/docker-compose/docker-compose.yml`), die Default-Org existiert per `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000054_default_organization.up.sql:21–23`; Admin-Gruppe erhält alle Permissions im Callback (:304–312 in `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/identity/handler.go`).
- Somit kein bloß ungenutztes Beispiel: Bei erreichbarer frischer Installation ohne nachträgliche Kontohärtung ist der Default-Mandant administrierbar. Das separate Keycloak-Administrationspasswort behebt dieses Anwendungskonto nicht. Ein Realm/Org-Claim-Mapper existiert, aber der Callback nutzt UUID-Gruppen; explizite Broker-Admin-API, Pflicht-MFA für org_admin und Passwort-/Brute-Force-Policy fehlen im gelieferten Realm.
- **Empfohlene Korrektur:** Demo-Konto aus produktiven Imports entfernen, individuell einmaligen Bootstrap mit Passwortwechsel/MFA verwenden, bestehende Installationen prüfen. Org-Attribut/Broker-/MFA-/Lockout-Regeln tatsächlich verdrahten und testen. AUT-09 fordert dies ausdrücklich; CH26 als Vorschlag allein wäre kein FAIL-Grund.

### AUT-10 — High: Rate-Limit liegt hinter der Authentifizierung

- **Beschreibung / Dateien:** `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/cmd/server/main.go:307–319` ordnet Auth→Tenant→Entitlement→RateLimiter; Default ist 600/min (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/config/config.go:123`). Ungültige API-Keys werden zuvor zurückgewiesen (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/middleware/auth.go:79–104`). Öffentliche Callback-/Refresh-Pfade erreichen zwar den allgemeinen IP-Limiter, jedoch nicht ein eigenes Auth-Budget von 20/min.
- `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/middleware/ratelimit.go:138–158` nimmt RemoteAddr; Proxy-/Client-IP-Vertrauensgrenze ist zu klären. Signup/Einladungsannahme fehlen; generische OAuth-Fehleranzeigen in `/home/runner/work/Reticora-CMDB/Reticora-CMDB/frontend/src/pages/auth/LoginPage.tsx:24–32,50–53` belegen kein Lockout-Signal.
- **Empfohlene Korrektur:** Separaten Pre-Auth-Limiter vor Credential-Prüfung für alle geforderten Pfade mit 20/min/IP einsetzen, vertrauenswürdige Proxyauflösung definieren; Lockout-Anzeige real gegen Keycloak prüfen.

### RBA-01/02 — High/Critical: Permission-Katalog und Rollenmatrix weichen ab

- **Beschreibung / Dateien:** `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/permission/catalog.go:3–102` und `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000032_standard_role_seeds.up.sql:20–39` fehlen location:read, location:write, discovery:manage, collector:manage, vrf:manage, review:resolve, job:read, notification:manage, team:manage. collector:manage steht nur im Identity-Katalog; site:* bleibt. citype:manage ist vorhanden, Typ-Routen verwenden jedoch ci_type:manage.
- Alle Sollmatrixzeilen sind unten pro enthaltenem Schlüssel aufgelöst. Vierergruppen sind **org_admin / engineer / viewer / client_technician**; J = enthalten, – = nicht enthalten, C = nur Client-Scope. „Session“ meint jeweils genau die gleichnamige OIDC-Rollengruppe; weitere Gruppen können Rechte ergänzen. SQL-J allein beweist keine Scopewirkung. Quellen: Seed :48–150 sowie `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/identity/handler.go:304–378`.

| Permission der RBA-02-Matrix | Soll | SQL-Seed | Session | Abweichung |
|---|---|---|---|---|
| ci:read | JJJC | JJJJ | J–J– | C-Begrenzung fehlt. |
| location:read | JJJC | –––– | –––– | Schlüssel fehlt, site:read/asset:read sind kein exakter Ersatz. |
| topology:read | JJJC | JJJJ | J–J– | C fehlt, teils nur ci:read für Unterrouten. |
| ci:write | JJ–C | JJ–– | J––– | Client-Technician fehlt. |
| rack:write | JJ–C | JJ–– | J––– | Client-Technician fehlt. |
| relationship:write | JJ–C | JJ–– | J––– | Client-Technician fehlt. |
| contact:write | JJ–C | JJ–– | J––– | Client-Technician fehlt. |
| ci:delete | JJ–– | JJ–– | J––– | DELETE ist nur an ci:write gebunden. |
| location:write | JJ–– | –––– | –––– | site:write/inventory:manage statt gefordertem Recht. |
| review:resolve | JJ–C | –––– | –––– | Resolve verlangt discovery:write. |
| export:run | JJ–C | JJ–– | J––– | Client-Technician fehlt; Streaming benötigt nur ci:read. |
| job:read | JJ–C | –––– | –––– | Jobs verwenden ci:read bzw. discovery:read. |
| discovery:manage | JJ–– | –––– | –––– | discovery:write statt Manage. |
| collector:manage | JJ–– | –––– | J––– | Nur Identity-Konstante, kein SQL-/Routenrecht. |
| credential:manage | JJ–– | J––– | J––– | Engineer nur veraltetes credential:write im Seed. |
| vrf:manage | JJ–– | –––– | –––– | Fehlt. |
| citype:manage | J––– | J––– | J––– | Vorhanden, Route verwendet anderen Schlüssel. |
| webhook:manage | J––– | J––– | J––– | Schlüssel vorhanden. |
| notification:manage | J––– | –––– | –––– | Fehlt. |
| user:manage | J––– | J––– | J––– | Schlüssel vorhanden. |
| team:manage | J––– | –––– | –––– | Teamänderung verlangt user:manage. |
| role:manage | J––– | J––– | J––– | Schlüssel vorhanden. |
| entitlement:manage | J––– | J––– | J––– | Schlüssel vorhanden, ersetzt nicht Operatortrennung ENT-04. |
| audit:read | J––– | JJJ– | J–J– | Engineer/Viewer zusätzlich im Seed, Viewer in Session. |
| apikey:manage | J––– | J––– | J––– | Schlüssel vorhanden, keine HTTP-Verwaltung. |

Die Sollmatrix enthält discovery:ingest aus RBA-01 nicht ausdrücklich; dessen Rollenverteilung bleibt Katalogfrage, statt eine zusätzliche Sollzeile zu erfinden. Seedrechte für spätere Module sind von Baseline-Rechten zu unterscheiden. Viewer erhält zusätzlich credential:read und kann dadurch Secrets entschlüsseln (RBA-04). Login nimmt nicht EffectivePermissions, sondern hart codierte Gruppenklassen admin/owner, editor/writer, viewer/reader/readonly und Permission-Textfragmente; engineer/client_technician sind keine erkannten Rollenklassen.

- **Empfohlene Korrektur:** Einen konsistenten Schlüssel-/Rollenkatalog für Seeds, neue Orgs, IdP-Auflösung und Routenzuordnung herstellen; Matrix inklusive C-Semantik am echten Login nachweisen. Kein Metadaten-Leserecht darf Credential-Klartext freigeben.

### RBA-03 — Critical: Scopeinformationen gehen in Rollenauflösung verloren

- **Beschreibung / Dateien:** Standardzuweisungen verwenden scope_client_id/scope_site_id, Custom-Zuweisungen scope_type organization/client/site; Team fehlt (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000005_users_roles.up.sql:17–38`, `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000014_phase2_users_teams_roles.up.sql:30–55`). EffectivePermissions vereinigt Rechte, ignoriert Scope-Spalten (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/permission/pg_repository.go:115–158`). Principal hat nur einen ClientScope-String, keine Site-/Team-Mengen (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/identity/principal.go:20–30`).
- Standard-Org-Zuweisung setzt NULL, Custom-Org-Zuweisung kann eine mitgegebene ScopeID übernehmen (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/user/pg_repository.go:725–805`). Der Login übernimmt diese DB-Zuweisungen ohnehin nicht; ein gesetztes app.client_scope einzelner Repositories ersetzt keine vertrauenswürdige Scopeherkunft.
- **Empfohlene Korrektur:** Gemeinsames Zuweisungsmodell und explizite Rechte-/Scopevereinigung pro Ebene, Orgrolle→NULL; Site-/Team-Mengen bis JWT/Principal/WithTenant/RLS erhalten. Gemischte Org-/Client-/Site-/Teamrollen mit echten DB-Reads/Writes testen.

### RBA-04/06 — Critical/High: Vollständiges Mapping schützt falsche Aktionen

- **Routenabgleich:** `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/server/router.go:203–275` und sämtliche Domainregistrierungen ergeben statisch **400 eindeutige explizite Operationen** (inklusive zwei optionaler Auditrouten; ohne Audit 398), gleiche Menge in `/home/runner/work/Reticora-CMDB/Reticora-CMDB/api/openapi.yaml`. /metrics ist separat als Handle registriert. Kein erfolgreicher dynamischer chi.Walk-Lauf behauptet (T1).
- **Vollständige Liste registrierter ungemappter Routen: leer (0).** Öffentlich sind GET /healthz, GET /api/v1/auth/config, POST /api/v1/auth/callback, POST /api/v1/auth/refresh, POST /api/v1/collectors/enroll (Enrollment-Code), zusätzlich /metrics. GET /api/v1/auth/me ist authentifiziert ohne einzelnes Fachrecht; die übrigen 394 expliziten Operationen sind permissiongeschützt. Signup-, separate CMDB-Admin- und API-Key-Verwaltungsrouten fehlen, sind also keine ungemappten öffentlichen Bypässe.
- `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/server/authz.go:264–359` verweigert unbekanntes API-Mapping (403), fehlenden Principal (401) und fehlendes Recht (403); vorgelagerte Middleware kann früher ablehnen. SCIM, /ingest, /agents/telemetry und /me haben Sonderzuordnungen (:181–248). Nicht-API-Pfade außer SCIM gelten grundsätzlich als öffentlich; daraus wird keine pauschale Sicherheitsgarantie für beliebige zukünftige Routermethoden abgeleitet.
- **Konkreter Critical-Befund:** GET /api/v1/credentials/{id}/decrypt verlangt nur credential:read (authz.go:54). Viewer erhält sämtliche :read-Rechte (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/identity/handler.go:315–336`); `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/credential/handler.go:125–143` liefert nach Orgprüfung Klartext ohne stärkeres Recht.
- **Weitere falsche Zuordnungen:** DELETE CI verwendet ci:write statt ci:delete; Order-Approve order:write statt order:approve. Explizite Permissiongruppen können solche begrenzten Sessions tatsächlich erzeugen. Verschachtelte CI-Overrides/Instanzfelder/Transitions sowie Source-Policy verfehlen passende Sonderrechte und fallen auf Ressourcenrechte zurück. Beziehungstyp-/Lifecycle-Definitionsrouten sind hingegen dediziert gemappt. POST GraphQL benötigt ci:write, ohne resolverweise Fachberechtigungen.
- **Testgrenze:** `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/server/authz_test.go:38–128` prüft das Ist-Mapping mit eingesetzten Principals/Memory; positive Fälle akzeptieren auch 400/404/500 und überspringen Audit-Happy-Paths. Ein grüner Mappingtest beweist deshalb nicht die Sollrechte.
- **Empfohlene Korrektur:** Mapping nach vollständigem Routenpattern/Methode/Aktion, Decrypt separat privilegieren, echte Aktions-Negativtests und resolverweise Fachrechte. Alle aktuellen Routen weiterhin fail-closed halten.

### RBA-05/08 — High: Modulrechte und Service-Accounts unvollständig

- **Phaseninventar:** Vorhanden [P2]: Asset, Assignment, Stock/Consumables/Inventory, Stocktake, Reservation, Order, Document, Ticket; Contract ohne Code OFFEN. [P3]: Workflow, Form, Maintenance, Compliance, Finding (security:*). [P4]: Key, Desk, Training, Agent, Disposition (disposal:*). [A]: IGA/SCIM und AI. Quellen: `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/server/authz.go:30–98,129–155` und `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000055_cmdb_extensions.up.sql:614–667`. Rechtefamilien existieren, gesonderte Aktionen wie Approval/Assignment sind nicht durchgängig wirksam; neue und bestehende Orgs erhalten nicht dieselben nachträglichen Grants.
- **Service-Account-Lücke:** Teams haben UNIQUE(org,name)/Mitglieder; API-Keys besitzen nur direkte Permissionlisten, Webhooks keinen Service-Account-/Scopebezug. CI-Audit interpretiert nichtleere UserID als user, während Key-Subject der nicht-UUID-Prefix ist (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/ci/pg_repository.go:588–608`, `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/identity/apikey.go:149–155`). Ein möglicher UUID-Castfehler ist ein Funktions-/Auditgap, kein behaupteter erfolgreicher Audit-Bypass.
- Ein Principal mit webhook:manage ohne ci:read kann CI-Ereignisse abonnieren; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/webhook/webhook.go:369–409` bindet keine Objektleserechte. `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/webhook/dispatcher.go:140–180,441–456` verwendet vollständigen einmal serialisierten Payload für alle Org-Abonnenten.
- **Empfohlene Korrektur:** Modul-/Aktionskatalog phasenkonsistent anwenden, zukünftige fehlende Module nicht vorziehen. Service-Accounts als eigene auditierbare UUID-Identitäten mit denselben Rollen/Scopes wie Benutzer modellieren; Empfängerrechte vor Webhook-Persistierung/Versand prüfen. Teammitgliedschaft einschließlich Orgzugehörigkeit des angegebenen Users und Key-/Webhook-Audit echt testen.

### ENT-01/02/03 — High: Datenvertrag, Kontingente und Cache weichen ab

- **Beschreibung / Dateien:** `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000004_entitlements.up.sql:3–14` hat Org-FK/NOT NULL und UNIQUE(org,feature_key), jedoch limit_value BIGINT, expires_at und plan je Feature; kein limits JSONB/valid_until/source enum. Aktuelle RLS-Variable/Policies stammen aus `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000023_rls_variable_unification.up.sql:35,57–64`, nicht aus überholten Anfangspolicies.
- Exakter Ist-Katalog in `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/entitlement/entitlement.go:30–45`: cmdb, discovery, inventory, documents, stocktake, ticketing, webhooks, export, monitoring, iga, endpoint_agent, workflow_forms, compliance, ai_assistant. Kein stets aktives cmdb_core und keine geforderten max_cis/max_collectors/max_users/max_api_keys. Nil ist unbegrenzt; numerisch 0 sperrt jede Neuanlage (:278–283), entgegen `/home/runner/work/Reticora-CMDB/Reticora-CMDB/api/openapi.yaml:8514–8516`.
- Nur CI-Service ruft AllowCreate auf (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/ci/service.go:136–150`), Collector-/User-Erzeugung nicht (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/discovery/discovery.go:463–491,545–582`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/user/handler.go:137–173`). Fehlerantwort aus CI-Handler :161–164 nutzt generischen Problemtyp über `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/api/api.go:84–90`; vorhandener entitlement-limit-Helper in `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/platform/httpx/problems.go:54–63` ist dort ungenutzt.
- Cache in `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/entitlement/entitlement.go:185–208,303–332` ist lokale Map/Mutex mit 30-s-Default und lokaler Invalidierung. `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/cmd/server/main.go:227–230` verdrahtet weder Redis noch 60 s.
- **Empfohlene Korrektur:** Katalog/Datenvertrag samt Herkunft und benannten Kontingenten wortlautkonform, Limits atomar über alle Erzeugungspfade; korrekter Problemtyp, Redis60s mit Invalidierung aller Instanzen und echte Vertrags-/Grenzwerttests. Bestehende CI-Updates nicht als Neuanlage zählen.

### ENT-03/07/08 — High: Ingest umgeht Freigabe und CI-Kontingent

- **Beschreibung / Dateien:** `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/discovery/discovery.go:424–426` registriert zwei Ingest-URLs. `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/entitlement/middleware.go:18–50,61–66` schützt /api/v1/discovery, nicht /api/v1/ingest/bulk. Der Alias verlangt discovery:ingest (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/server/authz.go:212–215`), umgeht aber abgelaufene/deaktivierte Discovery-Freigaben.
- Nur der REST-CI-Handler erhält den Limit-Service; Discovery erhält direkt repos.CI (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/server/router.go:212–216`). Klassifizierte neue Geräte werden direkt aktive CIs (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/discovery/discovery.go:713–740`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/ci/pg_repository.go:218–299`). Weitere Erzeugungswege liegen in `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/discovery/review.go:311–332` und `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/agent/handler.go:231–252`.
- `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000021_spec_alignment.up.sql:192` erlaubt ambiguous_identity/conflicting_values/unclassified_device, nicht unlicensed_ci. Der Discoverytest `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/discovery/discovery_test.go:109–129` injiziert kein CI-Repository; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/ci/limit_test.go:32–50` nur einen Limit-Stub. Keine produktive End-to-End-Kontingentprüfung.
- **Empfohlene Korrektur:** Alle Ingest-Aliase zentral auf Discovery-Ablauf prüfen und sämtliche CI-Neuanlagen atomar am gleichen Kontingent messen. Bestehende CIs weiter aktualisieren; neue Überlimitgeräte samt Snapshot als unlicensed_ci halten, Metrik/Benachrichtigung und auditiertes Resolve/Dismiss implementieren. ENT-03 macht diese Behandlung ausdrücklich verbindlich, unabhängig vom Vorschlagsvorbehalt in CH21.

### ENT-04 — High: Geforderte Operatorroute und -identität fehlen

- **Beschreibung / Dateien:** org_admin bekommt entitlement:manage über `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/migrations/000032_standard_role_seeds.up.sql:35,56–57`; `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/server/authz.go:115,279–288` fordert nur diese Mandantenberechtigung. `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/entitlement/handler.go:81–93` akzeptiert Featurefreigaben und fehlende Limit-/Ablaufwerte, der Upsert überschreibt damit vorhandene Werte (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/entitlement/pg_repository.go:78–85`). Nil-Limit bedeutet unbegrenzt (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/entitlement/entitlement.go:238,278–279`).
- Die reale Laufzeitberechtigung folgt nicht aus dem Seed allein: `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/identity/handler.go:270–279,304–311` erteilt OIDC-admin/owner sämtliche allPermissions einschließlich entitlement:manage (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/identity/types.go:148–149`). RLS beschränkt dies auf die eigene Org, ersetzt aber keine Operatoridentität; keine Cross-Org-Schreibmöglichkeit behauptet. Der Handler-Memorytest prüft diese Grenze nicht.
- RBA-02 gewährt org_admin ausdrücklich entitlement:manage. Deshalb ist Selbstfreigabe allein **keine nachgewiesene Privilegieneskalation**; die fachliche Abweichung betrifft die fehlende verlangte Admin-Org-Route/Operatoridentität. Eine davon unabhängige Billing-Sicherheitsgrenze wird nicht unterstellt.
- **Empfohlene Korrektur:** Verlangte Admin-Org-Route mit verifizierter Operatoridentität umsetzen; parallel zulässige tenantseitige Grantverwaltung gegenüber RBA-02 verbindlich klären. Änderungen mit authentifiziertem Akteur auditieren und die danach festgelegten Betreiber-/Mandantengrenzen testen.

### ENT-02/03/05 — High: Export-Gate und produktive Route stimmen nicht überein

- **Beschreibung / Dateien:** `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/export/export.go:35–76` liefert GET /api/v1/export/cis mit CSV/DATEV, das Gate in `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/entitlement/middleware.go:40,61–66` erfasst /api/v1/exports. Die tatsächliche Route benötigt nur ci:read (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/server/authz.go:55,235–238`), nicht die Exportfreigabe; der Handler prüft sie ebenfalls nicht.
- **Empfohlene Korrektur:** Freigaben an reale Route und Exportformat binden; DATEV ausdrücklich export_datev zuordnen, Baseline-CSV und Ablaufsemantik gemäß CH21 erhalten. Tests müssen genau den registrierten Singularpfad abdecken; die vorgeschlagene ENT-06-Paketmatrix ist nicht Grundlage dieses Pflichtbefunds.

### ENT-05 — High: Kein einheitlicher Planvertrag über alle Kanäle

- **Beschreibung / Dateien:** `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/graphqlbff/graphqlbff.go:146–150,175–205,234–259` hat nur cis/ci/relationships/currentUser und übergibt Org an Repositories; keinen Entitlementdienst/Resolvergate. Keine kostenpflichtige Mutation vorhanden, daher kein erfundener GraphQL-Schreibbypass. Fehlende Featurezeilen verwenden globalen DefaultPlan statt separat pro Org verwalteten Plan (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/entitlement/entitlement.go:241–251,352–361`).
- `/home/runner/work/Reticora-CMDB/Reticora-CMDB/frontend/src/hooks/useEntitlements.ts:18–92` lädt Freigaben mit Query-Key ['entitlements'] ohne Org-ID, 60-s-staleTime und Boolean-Navigation; die Benutzeroberfläche ist tatsächlich angebunden, verarbeitet aber keine Kontingent-/Ablaufanzeige. Der Query-Key allein ist hier kein Nachweis eines reproduzierten Cross-Tenant-Leaks.
- **Empfohlene Korrektur:** Gemeinsames tenantbezogenes Plan-/Grant-Modell und kanalübergreifende Featureprüfung, Org-bezogene Cachekeys/Invalidierung und vollständige UI-Anzeige; Tests über echte REST/GraphQL/PG/Redis-Kette statt nur Navigationsmapping.

### ENT-06 — High ausschließlich für CH14; Paketvorschlag ohne Gate

- **Beschreibung / Dateien:** `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/entitlement/entitlement.go:49–94` enthält Essential=cmdb/discovery/inventory, Standard zusätzlich documents/stocktake/ticketing/export/webhooks, Pro zusätzlich monitoring/workflow_forms/ai_assistant, Enterprise zusätzlich iga/endpoint_agent/compliance. Die detaillierten vorgeschlagenen Unterfeaturekeys und Limits 500/2.500/10.000/50.000 bzw. 2/5/20/unbegrenzt bzw. 5/25/100/unbegrenzt sind nicht implementiert; implizite Planfeatures haben nil-Limit. **Diese Vorschlagsabweichung ist nur Hinweis.**
- IGA/AI haben eigene Gates und können zusätzlich explizit freigegeben werden, werden aber auch implizit mit dem Paket gewährt. Das widerspricht unabhängig von Paketwerten CH14 „nicht Bestandteil der Pakete“.
- **Empfohlene Korrektur:** Add-on-Freigaben unabhängig von planFeatures behandeln und nachweisen; Paketmatrix/Defaults vor Umsetzung bestätigen lassen. Keine G1-Sperre allein wegen unverbindlicher oder späterer Paketbestandteile.

### ENT-07/08 — High: Ablauf stoppt falsche Funktionen, Collector läuft weiter

- **Beschreibung / Dateien:** `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/entitlement/entitlement.go:129–136,224–238` deaktiviert jede abgelaufene Featurezeile; Middleware prüft alle Methoden, damit auch GETs statt nur Discovery/Ingest. Der Aliasbefund oben zeigt zugleich einen ungesperrten Ingestpfad.
- `/home/runner/work/Reticora-CMDB/Reticora-CMDB/collector/collectorcmd/collectorcmd.go:251–303` scannt weiter, :355–371/403–405 spoolt auch Upload-HTTP-Fehler, :523–549 Heartbeat loggt Status. Serverheartbeat liefert 204/setzt online (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/discovery/discovery.go:595–608`, `/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/discovery/pg_repository.go:143–147`). Kein lizenzbedingtes paused, Warnbanner/14-7-1-Zustellung oder spezifischer Reject-Zähler nachgewiesen.
- Downgrade: Grant ist nur Upsert, keine automatische CI-Löschung gefunden. REST Create/Update sind getrennt (`/home/runner/work/Reticora-CMDB/Reticora-CMDB/backend/internal/ci/service.go:136–167`), Limit gilt nur für Create; direkter Ingest unterläuft es trotzdem. Datenlöschung wird nicht behauptet.
- **Empfohlene Korrektur:** Ablaufsemantik zentral auf Discovery/Ingest beschränken, Collector vor weiteren Scans pausieren und Lizenzablehnung ohne Spoolaufbau erkennen; übrige Funktionen inklusive Lesen/Ändern/Export erhalten. Warnfristen und Downgrade-Bestandserhaltung real testen; Trial30Tage/100CI/Standard→Essential nur nach Bestätigung des Vorschlags.

## Offene Fragen

- AUT-05/AUT-06 benötigen den v2-Wortlaut. AUT-03 kann nur gegen die ausdrücklich gelieferte Aussage „Dev-Login nur außerhalb production“ geprüft werden.
- TEN-05 erlaubt in seiner allgemeinen WITH-CHECK-Formel `client_id IS NULL`, verbietet danach aber org-weite Schreibzugriffe für ausschließlich client-gescopte Rollen. Maßgeblich für die Prüfung ist zusätzlich dieses ausdrückliche Schreibverbot; die formale Policy-Spezifikation muss beide Aussagen widerspruchsfrei zusammenführen.
- ENT-06 ist insgesamt ein Vorschlag (V); dessen Paketwerte sind kein verbindliches G1-Kriterium. Die ausdrücklichen Baseline-Feature-Keys aus ENT-02 und Add-on-Trennung nach CH14 bleiben eigenständig verbindlich.
- ENT-04 fordert Operatoridentität, während RBA-02 org_admin entitlement:manage gibt: Darf diese Mandantenrolle Grants selbst ändern, oder nur eine eingeschränkte Verwaltungsfunktion nutzen? Kein ausdrückliches Operator-only-Verbot oder zusätzliche Billing-Grenze ohne Entscheidung annehmen.
- RBA-02 nennt discovery:ingest aus RBA-01 nicht in seiner vollständigen Matrix: Rollenverteilung bestätigen. AUT-04 nennt zwischen Prefix und Secret keinen zusätzlichen Trenner; gewünschtes exaktes Format bestätigen, der aktuelle zusätzliche Unterstrich wurde nicht still übernommen.
- Referenzierte Abschnitte SEC-06/07, TLC-01/03, API-07 und der v2-Umfang von TEN-09 liegen nicht vollständig vor; nur gelieferte Kriterien werden bewertet.
- Welche globalen Metamodell-/Katalogtabellen sind verbindliche Ausnahmen von TEN-02/05? Der Istbestand globaler NULL-Org-Zeilen und indirekter Orgableitung ist einzeln benannt, nicht still als konform angenommen.
- Effektive Owner-/DDL-/Mitgliedschaftsrechte im Zielcluster sowie manuell gehärtete Keycloak-Konten/Policies sind nicht aus dem Checkout nachweisbar; Nachlieferung echter Deploymentbelege nötig. Der Bericht bewertet ausgelieferte Vorlagen, nicht unbekannte Betreiberänderungen.

## Stand

VOLLSTÄNDIG

36 eindeutige IDs in vorgegebener Reihenfolge mit acht Spalten, 105 Tabellen im Policyabgleich und Statuszählung abgeglichen. Vollständigkeit bezeichnet die Bearbeitung des gelieferten Wortlauts, nicht Konformität oder Gatefreigabe; AUT-05/06 sowie ausdrücklich genannte Laufzeit-/v2-Teilgrenzen bleiben N/P beziehungsweise unbewertet. Produktivcode, Migrationen, Tests und Abhängigkeiten unverändert.
