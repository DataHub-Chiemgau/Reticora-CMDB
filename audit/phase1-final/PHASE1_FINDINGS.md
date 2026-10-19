# Phase-1-Befunde (Defektliste)

Eindeutige Defekte aus dem Abnahmeaudit WP-001 – WP-213 (dedupliziert über alle Einzelprüfungen, Real-Stack-E2E und die Defekttest-Welle). Prioritäten: P0 Sicherheitsrisiko, schwerer Datenverlust oder zentraler Prozess blockiert; P1 wesentliche Funktion fehlt/falsch/unzuverlässig; P2 relevante funktionale, Validierungs- oder UX-Einschränkung; P3 geringfügiger Mangel. „Ursache belegt“ heißt: durch einen ausgeführten Test oder eine reproduzierte Probe nachgewiesen; sonst Hypothese.

Defekttests laufen nur gezielt und schlagen fehl, bis der Befund behoben ist: Go `go test -tags auditdefect -run <Test> <Paket>` (mit `TEST_DATABASE_URL` u. a., siehe Kopfkommentar der Testdatei), Shell `bash tests/audit/<name>.test.sh`, Frontend `npx vitest run -c vitest.auditdefect.config.ts`, Real-Stack-E2E `npx playwright test -c e2e-real/playwright.real.config.ts`.

## Zählung

| Priorität | Anzahl |
|---|---:|
| P0 | 16 |
| P1 | 30 |
| P2 | 134 |
| P3 | 174 |
| **Summe** | **354** |

## Übersicht

| ID | Prio | Titel | Betroffene WPs | Ursache | Kodierender Test |
|---|---|---|---|---|---|
| [F-001](#f-001) | P0 | Compliance-Auswertung durch client-gescopten Principal liefert und überschreibt Ergebnisse anderer Clients | WP-020 | belegt | backend/internal/tenant/rls/audit_t1_cross_client_writes_auditdefect_… |
| [F-002](#f-002) | P0 | Site-Scope-Bypass im Rack-Kanal: site-fremde Racks lesbar und löschbar | WP-014, WP-208, WP-209, WP-213 | belegt | TestAuditT1RackChannelSiteScope (FAILS_AS_EXPECTED, SP/verify-tests-r… |
| [F-003](#f-003) | P0 | Review-Items ohne Kandidaten-CIs leaken über Clients; review_item ohne DB-seitigen Client/Site-Scope | WP-013, WP-025, WP-208 | belegt | TestAuditT1ReviewItemsWithoutCandidatesStayInClientScope, TestAuditT1… |
| [F-004](#f-004) | P0 | Geteilte Dokumente und Wartungsfenster: teilgescopter Principal ändert/löscht sie, FK-Kaskade entfernt Links … | WP-019, WP-020, WP-028 | belegt | TestAuditT1DocumentWritesOutsideScope, TestAuditT1MaintenanceWindowWr… |
| [F-005](#f-005) | P0 | MFA für org_admins aus Signup, Einladung, Operator-Pfad und Gruppe reticora-admin nicht erzwungen | WP-044 | belegt | backend/internal/signup/t2_mfa_auditdefect_test.go (2 Tests, FAILS_AS… |
| [F-006](#f-006) | P0 | API-Key-Rotation/-Widerruf durch Nicht-Owner: Rechteausweitung und Übernahme fremder Keys | WP-067 | belegt | backend/internal/identity/t2_apikey_authz_auditdefect_test.go (FAILS_… |
| [F-007](#f-007) | P0 | API-Key-Bindung an Service-Account ohne Scope-Prüfung: client-gebundener Ersteller erhält org-weiten Key | WP-067, WP-069 | belegt | backend/internal/identity/t2_apikey_authz_auditdefect_test.go (FAILS_… |
| [F-008](#f-008) | P0 | Service-Account-Rollenzuweisung ohne Rechte-/Scope-Prüfung; Route verlangt nur user:manage | WP-069 | belegt | identity/t2_apikey_authz_auditdefect_test.go, server/t2_auth_chain_au… |
| [F-009](#f-009) | P0 | Webhook-Filter liefert merge-/relationship-Events anderer Clients an client-gebundene Service-Accounts | WP-069 | belegt | backend/internal/webhook/t2_bound_filter_auditdefect_test.go (FAILS_A… |
| [F-010](#f-010) | P0 | WMI-Plugin sendet Credentials im Klartext (HTTP Basic über http://ziel:5985; HTTPS-Modus mit InsecureSkipVeri… | WP-048, WP-152 | belegt | collector/plugins/wmi/wmi_auditdefect_test.go:97 TestAuditWMICredenti… |
| [F-011](#f-011) | P0 | NAS-Plugin sendet Synology-Konto und Passwort in der URL über http://ziel:5000 und an ungeprüftes HTTPS (5001) | WP-152 | belegt | collector/plugins/nas/nas_auditdefect_test.go:60 TestAuditNASCredenti… |
| [F-012](#f-012) | P0 | Erneuerungsendpunkt ist nicht an den Collector gebunden: ein Principal mit discovery:ingest stellt sich ein C… | WP-145, WP-147 | belegt | backend/internal/discovery/collector_pki_auditdefect_test.go:109 Test… |
| [F-013](#f-013) | P0 | Collector ignoriert credential_ids je Scope: das erste Credential je Protokoll gilt für alle Scopes (auch übe… | WP-147, WP-153 | belegt | collector/collectorcmd/discovery_auditdefect_test.go:305 TestAuditPer… |
| [F-014](#f-014) | P0 | Site-Grant setzt Client-Scope auf NULL: Site-Nutzer lesen Kontakte, Credentials, Assets und unplatzierte CIs … | WP-109 | belegt | backend/internal/locations/sitescope_auditdefect_test.go TestAuditDef… |
| [F-015](#f-015) | P0 | Keine Grant-Obergrenze: client-gescopter user:manage-Inhaber erzeugt org-weite org_admin-Einladungen und -Rol… | WP-114 | belegt | backend/internal/user/invitation_auditdefect_test.go TestAuditDefectI… |
| [F-016](#f-016) | P0 | CI-Dialog 'CI erstellen' nutzt hart kodierte Typ-Keys: sendet 'server' als ci_type_id (400) und bietet keine … | WP-046, WP-117, WP-119, WP-120, WP-191, WP-194, WP-201, WP-239 | belegt | frontend/src/pages/CIFormModal.auditdefect.tsx (2 Fälle, fails-as-exp… |
| [F-017](#f-017) | P1 | Rollenzuweisung nicht entziehbar: kein DELETE /role-assignments/{id} | WP-009, WP-046, WP-066, WP-168 | belegt | frontend/e2e-real/roles-authz.spec.ts:1019 13 (FAIL) |
| [F-018](#f-018) | P1 | OpenAPI-Validator weist jeden SPA-Refresh (POST /auth/refresh ohne Body/Content-Type) mit 400 ab; gleiche Abw… | WP-009, WP-050, WP-066 | belegt | middleware/t2_auth_ratelimit_auditdefect_test.go (2), server/t2_auth_… |
| [F-019](#f-019) | P1 | Abgewiesene Requests einer Credential leeren das Org-Budget (eine Integration sperrt die ganze Org) | WP-166 | belegt | middleware/t2_auth_ratelimit_auditdefect_test.go (FAILS_AS_EXPECTED) |
| [F-020](#f-020) | P1 | Manuelle Massen-Statusänderung (POST /cis/bulk set_status) ist kein manueller Schreibvorgang: kein Override, … | WP-057, WP-061, WP-181 | belegt | backend/internal/ci/audit_t3_manual_writes_auditdefect_test.go TestAu… |
| [F-021](#f-021) | P1 | Override setzen/aufheben erhöht ci.version nicht: veralteter If-Match-PATCH überschreibt den neueren geschütz… | WP-061 | belegt | backend/internal/ci/audit_t3_manual_writes_auditdefect_test.go TestAu… |
| [F-022](#f-022) | P1 | Auflösung „new“ bei resurrected_device erzeugt org-weites CI ohne Identität: fremde Client-Sichtbarkeit und e… | WP-087 | belegt | backend/internal/discovery/audit_t3_review_resolution_auditdefect_tes… |
| [F-023](#f-023) | P1 | DELETE /sites\|/buildings\|/rooms/{id} löscht Standorte mit Kindern (204, Kaskade) statt 409 (LOC-11) | WP-055 | belegt | backend/internal/locations/audit_t3_delete_rules_auditdefect_test.go … |
| [F-024](#f-024) | P1 | Site-Löschung kaskadiert VLANs/Enrollment-Tokens und entzieht Subnetzen, Collectoren, Discovery-Scopes, ci_me… | WP-055 | belegt | backend/internal/locations/audit_t3_delete_rules_auditdefect_test.go … |
| [F-025](#f-025) | P1 | Deaktivierter Collector liefert weiter: offene NATS-Verbindung bleibt bestehen, EdgeIngest, scopes.result und… | WP-145, WP-153 | belegt | backend/internal/discovery/collector_pki_auditdefect_test.go:257 Test… |
| [F-026](#f-026) | P1 | Keine automatische Zertifikatserneuerung; Collector verlieren 90 Tage nach dem Enrollment die Konnektivität | WP-145 | belegt | collector/collectorcmd/lifecycle_auditdefect_test.go:213 TestAuditCol… |
| [F-027](#f-027) | P1 | Falsche Credential-Auswahl im Bundle: ohne aktive Scopes alle Client- und Org-Credentials, ein Scope ohne cre… | WP-147, WP-153, WP-156 | belegt | backend/internal/discovery/collector_pki_auditdefect_test.go:357 Test… |
| [F-028](#f-028) | P1 | Credentials der Art 'wmi' erreichen das WMI-Plugin nie; SSH-Credentials gehen an WMI- und NAS-Ziele | WP-152 | belegt | collector/collectorcmd/discovery_auditdefect_test.go:258 TestAuditCre… |
| [F-029](#f-029) | P1 | WMI Collect meldet Erfolg mit leeren Feldern bei 401, Transportfehler oder protokollkonformer WinRM-Antwort; … | WP-152, WP-155 | belegt | collector/plugins/wmi/wmi_auditdefect_test.go:169 TestAuditWMIFailure… |
| [F-030](#f-030) | P1 | Jedes gesweepte Gerät erhält ein (wiederkehrendes) falsches unclassified_device-Review, auch wenn SNMP, SSH o… | WP-151 | belegt | backend/internal/discovery/ingest_auditdefect_test.go:107 TestAuditUn… |
| [F-031](#f-031) | P1 | scope_suggested umgeht discovery:manage und ist über Client-Grenzen sichtbar und verwerfbar | WP-153, WP-156 | belegt | backend/internal/server/scope_suggested_authz_auditdefect_test.go:53 … |
| [F-032](#f-032) | P1 | Ausgelieferter Collector liefert keine LLDP-/CDP-/FDB-Nachbarn (TOP-01-Eingang fehlt in Produktion) | WP-158 | belegt | collector/plugins/snmp/neighbors_auditdefect_test.go:68 TestAuditSNMP… |
| [F-033](#f-033) | P1 | certbot certonly kann das Let's-Encrypt-Zertifikat nicht ablegen, weil der Bootstrap-Platzhalter letsencrypt/… | WP-094 | belegt | tests/audit/install-cloud-acme.test.sh – Fälle 'acme: fresh install s… |
| [F-034](#f-034) | P1 | Von certbot ausgestellter Schlüssel und Kette sind für den unprivilegierten nginx-Nutzer (uid 101) des Fronte… | WP-094 | belegt | tests/audit/install-cloud-acme.test.sh – Fall 'acme: issued key and c… |
| [F-035](#f-035) | P1 | OIDC-Issuer-Prüfung läuft vor dem Start des Frontends (Issuer-Proxy), daher brechen Domain-/TLS-Installatione… | WP-095 | belegt | tests/audit/install-cloud-issuer-order.test.sh – Fall 'issuer: domain… |
| [F-036](#f-036) | P1 | K8s-Manifeste liefern das durch WP-097 verpflichtende RETICORA_OIDC_ISSUER_URL nicht; Server beendet sich in … | WP-097, WP-099 | belegt | backend/internal/config/k8s_required_env_auditdefect_test.go TestAudi… |
| [F-037](#f-037) | P1 | CI-13-Reaktivierung über die Produktions-Decorator-Kette aus main.go liefert 501; gelöschtes, wiedergesehenes… | WP-087, WP-213 | belegt | backend/internal/tenant/rls/audit_t5_sr11_decorator_chain_auditdefect… |
| [F-038](#f-038) | P1 | Gate-Checkliste führt CI-13 [B] als PASS trotz offenem und reproduziertem Defekt SR-11 | WP-213 | belegt | tests/audit/g1-gate-evidence.test.sh – known_defect_SR-11 (Assertion … |
| [F-039](#f-039) | P1 | Kein Pentest-Bericht und keine Befundliste im Repository (Lieferung A-07 fehlt) | WP-210 | belegt | tests/audit/g1-gate-evidence.test.sh – pentest_summary, pentest_findi… |
| [F-040](#f-040) | P1 | Review und Gate führen TEN-09-Isolation als vollständig und 'kein offenes Critical', obwohl ein Site-Scope-By… | WP-209, WP-213 | belegt | tests/audit/g1-gate-evidence.test.sh – rack_site_scope (fails-as-expe… |
| [F-041](#f-041) | P1 | Einladungen versenden keine E-Mail: invitation.created (NTF-05 'Einladung') wird nie erzeugt | WP-114, WP-140 | belegt | backend/internal/user/invitation_auditdefect_test.go TestAuditDefectI… |
| [F-042](#f-042) | P1 | Manuelle verifizierte Port-Kanten werden bei Discovery-Interface-Entfernung per ON DELETE CASCADE still gelös… | WP-128 | belegt | backend/internal/server/audit_t6_router_auditdefect_test.go TestAudit… |
| [F-043](#f-043) | P1 | EVT-02-Typen relationship.created, relationship.removed (API), collector.enrolled, review.resolved werden nie… | WP-136 | belegt | backend/internal/server/audit_t6_router_auditdefect_test.go TestAudit… |
| [F-044](#f-044) | P1 | Entitlement-Warnung/-Ablauf und Zertifikatsablauf: JOB-02-Läufe nicht eingeplant, Events entitlement.warning/… | WP-131, WP-136, WP-140 | belegt | keiner (laut Welle nicht kodierbar ohne Produktionscode) |
| [F-045](#f-045) | P1 | Werte mit Anführungszeichen oder Steuerzeichen (Zeilenumbruch, Tab) brechen jeden auditierten Write mit Wert-… | WP-117, WP-120, WP-132, WP-133 | belegt | frontend/e2e-real/ci-lifecycle.spec.ts L14 (FAIL) |
| [F-046](#f-046) | P1 | Collector-Metrik-Uploads (Sweep, UPS, SNMP-Poll) werden ohne Credential gesendet, mit 401 abgewiesen und erre… | WP-190 | belegt | collector/collectorcmd/audit_t7_metrics_upload_auditdefect_test.go:39… |
| [F-047](#f-047) | P2 | Handler geben rohe pgx/PostgreSQL-Fehlertexte (SQLSTATE, Constraint-, Tabellen-, RLS-Policy-Namen) und falsch… | WP-010, WP-011, WP-012, WP-014, WP-015, WP-016, WP-017, WP-018, WP-02… | belegt | backend/internal/tenant/rls/audit_t1_error_sanitization_auditdefect_t… |
| [F-048](#f-048) | P2 | Unverlinkte (org-weite) Dokumente und Wartungsfenster durch client-gescopte Principals anleg-, änder-, lösch-… | WP-019, WP-020, WP-028 | belegt | TestAuditT1DocumentWritesOutsideScope, TestAuditT1MaintenanceWindowWr… |
| [F-049](#f-049) | P2 | Team-Scope kann nie vergeben werden: Rollenzuweisungen ohne Team-Dimension, app.team_scope für reale Nutzer i… | WP-009 | belegt | TestAuditT1TeamScopedAssignment (FAILS_AS_EXPECTED, SP/verify-tests-r… |
| [F-050](#f-050) | P2 | Abgeleiteter Kanten-Scope veraltet, wenn ein client- oder site-gescopter Principal ein CI verschiebt | WP-025 | belegt | TestAuditT1EdgeScopeFollowsScopedMove (FAILS_AS_EXPECTED); Probes pro… |
| [F-051](#f-051) | P2 | Metrik-Views nur org-begrenzt: metric_sample ohne Client-/Site-Scope auf DB-Ebene | WP-025, WP-039, WP-040 | belegt | TestAuditT1MetricViewsHonourScope (FAILS_AS_EXPECTED); positiv: TestA… |
| [F-052](#f-052) | P2 | Metrik-Serienamen eines fremden Client-CI über GET /api/v1/cis/{id}/metrics sichtbar | WP-016, WP-040 | belegt | TestAuditT1CIMetricsEndpointScope, TestAuditT1MetricNamesHonourScope … |
| [F-053](#f-053) | P2 | Assets, Rooms und Racks haben einen Standortbezug, aber keine site_id; site-gescopte Principals sehen sie in … | WP-027 | belegt | TestAuditT1SiteScopeOfLocatedTables (FAILS_AS_EXPECTED); kodiert das … |
| [F-054](#f-054) | P2 | OpenSearch-Dokumente behalten nach Rack-Platzierung eine veraltete Site; site-beschränkte Nutzer finden wegge… | WP-033 | belegt | TestAuditT1OpenSearchFollowsRackPlacement (FAILS_AS_EXPECTED); Probes… |
| [F-055](#f-055) | P2 | Strukturierte Suche (POST /api/v1/search/query) verlangt search:write; Leserollen erhalten 403, Saved Views n… | WP-034 | belegt | TestAuditT1StructuredSearchForReadRoles (FAILS_AS_EXPECTED); pinnt da… |
| [F-056](#f-056) | P2 | IGA-Handler ignoriert Repository-Schreibfehler (Genehmigung ohne Provisioning-Task) | WP-021 | belegt | TestAuditT1ApprovalNeverLosesTheProvisioningTask (FAILS_AS_EXPECTED);… |
| [F-057](#f-057) | P2 | Server meldet sich in der paketierten Installation als SUPERUSER an; Rollenprüfung prüft nur current_user, RE… | WP-064 | belegt | TestAuditT1StartupRefusesEscalatableLogin (FAILS_AS_EXPECTED); Positi… |
| [F-058](#f-058) | P2 | Kanalmatrix deckt Locations unterhalb von Sites, org-weite Schreibpfade, GraphQL-Felder außer cis und exakte … | WP-208 | belegt | keiner (Testlücke); ergänzend Wellen-Regressionstests (PASS) und Defe… |
| [F-059](#f-059) | P2 | In das generische Job-Modell gespiegelte Export-Jobs sind über GET /api/v1/jobs und /jobs/{id} für andere Nut… | WP-035, WP-208 | belegt | keiner (nur Probe TestProbeV05ExportJobsVisibleInGenericJobs). |
| [F-060](#f-060) | P2 | Deaktivierung/Anonymisierung eines fremden Benutzers widerruft dessen Sessions trotz 404 (mandantenübergreife… | WP-042 | belegt | backend/internal/user/t2_deactivation_tenant_auditdefect_test.go Test… |
| [F-061](#f-061) | P2 | Bestehende Installationen behalten nach Upgrade den alten Realm ohne neue Policies | WP-044 | Hypothese | keiner |
| [F-062](#f-062) | P2 | Webhook-Seite verschluckt Serverablehnungen (400 Egress/Validierung, 403): keine Meldung | WP-045, WP-046, WP-049, WP-191, WP-192 | belegt | frontend/e2e-real/bulk-jobs-export.spec.ts:1021 W01, roles-authz.spec… |
| [F-063](#f-063) | P2 | Erfolgreiche Refreshes verbrauchen das Pre-Auth-Budget je IP; SPA meldet bei 429 ab | WP-050 | belegt | middleware/t2_auth_ratelimit_auditdefect_test.go (FAILS_AS_EXPECTED) |
| [F-064](#f-064) | P2 | Agent folgt https→http-Redirects und sendet Bearer-Token im Klartext | WP-051 | belegt | backend/cmd/agent/t2_redirect_auditdefect_test.go (FAILS_AS_EXPECTED) |
| [F-065](#f-065) | P2 | Parallele 401 lösen parallele Refreshes aus, Reuse-Erkennung widerruft die Familie | WP-066 | belegt | frontend/src/auth/session.auditdefect.ts (FAIL wie erwartet) |
| [F-066](#f-066) | P2 | Abgelaufene gespeicherte Session führt ohne Refresh-Versuch zu /login | WP-066 | belegt | frontend/src/auth/bootRefresh.auditdefect.tsx; e2e-real roles-authz 1… |
| [F-067](#f-067) | P2 | Authz ignoriert die RBA-01-Schlüssel citype:manage und notification:manage | WP-068 | belegt | backend/internal/server/t2_auth_chain_auditdefect_test.go (FAILS_AS_E… |
| [F-068](#f-068) | P2 | Break-Glass-'Alarm' nur Logzeile und Zähler; keine Alarmregel ausgeliefert | WP-070 | belegt | tests/audit/t2-break-glass-alert.test.sh (FAILS) |
| [F-069](#f-069) | P2 | Personalisierter Operator-OIDC-Login mit ausgeliefertem Realm unmöglich (kein acr/amr, keine Gruppe operators) | WP-070 | belegt | keiner |
| [F-070](#f-070) | P2 | rack_view, api_access und notifications_email serverseitig nicht durchgesetzt (REST und GraphQL) | WP-072 | belegt | backend/internal/server/t2_entitlement_auditdefect_test.go (2, FAILS_… |
| [F-071](#f-071) | P2 | ENT-07-Ablaufwarnungen fehlen (Banner, 14/7/1-Tage-Benachrichtigung) | WP-072 | belegt | keiner |
| [F-072](#f-072) | P2 | Nur max_cis durchgesetzt; max_users, max_api_keys, max_collectors wirkungslos | WP-073 | belegt | backend/internal/server/t2_entitlement_auditdefect_test.go (FAILS_AS_… |
| [F-073](#f-073) | P2 | KI-Ask-Pfad sendet Mandantendaten ohne Org-Opt-in an den Provider | WP-076 | belegt | backend/internal/server/t2_ai_optin_auditdefect_test.go (FAILS_AS_EXP… |
| [F-074](#f-074) | P2 | Org-KI-Opt-in über keine API/UI/Operator-Schnittstelle setzbar | WP-076 | belegt | backend/internal/server/t2_ai_optin_auditdefect_test.go (FAILS_AS_EXP… |
| [F-075](#f-075) | P2 | Neun Listenoperationen als nackte Arrays ohne {data, next_cursor}; limit ignoriert | WP-164 | belegt | backend/internal/server/t2_pagination_auditdefect_test.go (2, FAILS_A… |
| [F-076](#f-076) | P2 | Session-Bucket je Access-Token, Reset bei jedem Refresh | WP-166 | belegt | middleware/t2_auth_ratelimit_auditdefect_test.go (FAILS_AS_EXPECTED) |
| [F-077](#f-077) | P2 | CI-Listen-Export nutzt deprecated /api/v1/export/cis als Link → 401 im Browser | WP-168, WP-191, WP-192, WP-201 | belegt | frontend/src/api/deprecatedPaths.auditdefect.ts (FAIL wie erwartet); … |
| [F-078](#f-078) | P2 | Audit-Seite liest Felder, die die API nicht liefert ('Invalid Date', Objekt '—') | WP-133, WP-168, WP-171, WP-191, WP-192 | belegt | frontend/e2e-real/tenant-isolation.spec.ts:1185 T17 (FAIL); frontend/… |
| [F-079](#f-079) | P2 | Standortbaum-UI ohne Umbenennen/Verschieben (LOC-07 CRUD unvollständig) | WP-053 | belegt | keiner |
| [F-080](#f-080) | P2 | PATCH /cis/{id} ignoriert null auf typisierten Feldern (200, unverändert) – nicht RFC 7396 | WP-057 | belegt | backend/internal/ci/audit_t3_manual_writes_auditdefect_test.go TestAu… |
| [F-081](#f-081) | P2 | Leeren eines optionalen Felds im CI-Bearbeiten-Dialog wird stillschweigend nicht gespeichert | WP-057, WP-201 | belegt | frontend/e2e-real/ci-lifecycle.spec.ts L05 |
| [F-082](#f-082) | P2 | Instanzattribut-Bearbeitung im CI-Detail sendet kein If-Match | WP-061 | belegt | keiner |
| [F-083](#f-083) | P2 | Agent-Telemetrie legt nach Merge/Löschung des verknüpften CIs ein Duplikat an | WP-059 | belegt | backend/internal/agent/audit_t3_agent_merge_auditdefect_test.go TestA… |
| [F-084](#f-084) | P2 | Spool-Verlustzähler nicht persistiert: Verluste vor Neustart erreichen den Server nie | WP-062 | belegt | edgecore/buffer/audit_t3_spool_loss_auditdefect_test.go TestAuditT3Sp… |
| [F-085](#f-085) | P2 | Head-of-Line-Blockade im Spool: korrupte Datei oder dauerhaft abgewiesener Batch stoppt jede Zustellung | WP-062 | belegt | edgecore/buffer/audit_t3_spool_loss_auditdefect_test.go TestAuditT3Co… |
| [F-086](#f-086) | P2 | Client-gescopter Principal kann parent_ci_id auf ein CI eines anderen Clients setzen | WP-079 | belegt | backend/internal/ci/audit_t3_integrity_auditdefect_test.go TestAuditT… |
| [F-087](#f-087) | P2 | ci.health wird in der G1-Standardkonfiguration nie berechnet (immer 'unknown') | WP-080 | belegt | backend/internal/server/audit_t3_health_worker_auditdefect_test.go Te… |
| [F-088](#f-088) | P2 | Subnetze und Interfaces können VLANs eines anderen Clients referenzieren | WP-081 | belegt | backend/internal/ipam/audit_t3_client_scope_auditdefect_test.go TestA… |
| [F-089](#f-089) | P2 | Client-gescopte Principals können org-weite VRFs anlegen, umbenennen (auch 'global') und löschen | WP-082 | belegt | backend/internal/ipam/audit_t3_client_scope_auditdefect_test.go TestA… |
| [F-090](#f-090) | P2 | Ein fehlschlagender Record verwirft (Queue) bzw. weist ab (HTTP) die gültigen Records derselben Gruppe/Spur | WP-085 | belegt | backend/internal/discovery/audit_t3_poison_record_auditdefect_test.go… |
| [F-091](#f-091) | P2 | resolved_by und unlicensed_ci-Audit-Actor per Request-Body fälschbar | WP-088 | belegt | backend/internal/server/audit_t3_router_auditdefect_test.go TestAudit… |
| [F-092](#f-092) | P2 | Override-Endpunkt akzeptiert reservierte Namensräume; _instance-Werte dauerhaft zerstört | WP-091 | belegt | backend/internal/server/audit_t3_router_auditdefect_test.go TestAudit… |
| [F-093](#f-093) | P2 | Einheitliche CI-Historie enthält keine location_change-Einträge | WP-171 | belegt | backend/internal/history/audit_t3_location_change_auditdefect_test.go… |
| [F-094](#f-094) | P2 | CI-Historie für alle Standardrollen außer org_admin unbenutzbar (audit:read) | WP-171 | belegt | keiner |
| [F-095](#f-095) | P2 | AUD-03-ci_change-Schema und -Vokabular nicht umgesetzt (change_kind, field_changes, Quellen-/Request-Korrelat… | WP-171, WP-172 | belegt | keiner |
| [F-096](#f-096) | P2 | Review-Auflösung merge bei probable_duplicate löscht weich statt CI-12-Merge | WP-173 | belegt | backend/internal/discovery/audit_t3_review_resolution_auditdefect_tes… |
| [F-097](#f-097) | P2 | Unmerge überschreibt nach dem Merge vorgenommene Survivor-Änderungen still | WP-174 | belegt | backend/internal/ci/audit_t3_integrity_auditdefect_test.go TestAuditT… |
| [F-098](#f-098) | P2 | Blast Radius zählt nur über connected_to erreichte CIs ohne Heuristik-Kennzeichen als betroffen | WP-176 | belegt | backend/internal/topology/audit_t3_blast_radius_auditdefect_test.go T… |
| [F-099](#f-099) | P2 | GET /cis/{id}/dependencies nutzt rohe Kantenrichtung, invers zu impact_direction (außerhalb WP-176, IMP-07/WP… | WP-031, WP-176 | belegt | keiner |
| [F-100](#f-100) | P2 | CI-Detail zeigt nach „Override aufheben“ bis zum Reload weiter den entfernten Override-Wert | WP-060 | belegt | frontend/e2e-real/discovery-override.spec.ts D07 |
| [F-101](#f-101) | P2 | Bootstrap über RETICORA_ENROLL_TOKEN / RETICORA_CLOUD_URL nicht implementiert | WP-145 | belegt | collector/collectorcmd/lifecycle_auditdefect_test.go:335 TestAuditCol… |
| [F-102](#f-102) | P2 | CA-Roll-over ist nicht auslösbar und bricht danach das Vertrauen enrollter Collector in das Edge-Serverzertif… | WP-145 | belegt | backend/internal/discovery/collector_pki_auditdefect_test.go:160 Test… |
| [F-103](#f-103) | P2 | Compose veröffentlicht REST-API (8080), Keycloak (8180) und Frontend (3000) auf allen Interfaces, entgegen Fi… | WP-146 | belegt | tests/audit/compose-port-policy.test.sh (FAILS) |
| [F-104](#f-104) | P2 | Paralleler Edge-PKI-Export mehrerer Server-Replikas hinterlässt ein nicht passendes oder fehlendes Server-Sch… | WP-146 | belegt | backend/internal/discovery/collector_pki_auditdefect_test.go:201 Test… |
| [F-105](#f-105) | P2 | SNMPv3-Credential-Fehler unsichtbar: usmStats-Reports werden als 'kein Gerät' behandelt | WP-152, WP-155 | belegt | collector/plugins/snmp/v3_auth_failure_auditdefect_test.go:85 TestAud… |
| [F-106](#f-106) | P2 | Scopes mit mehr als ~4096 Adressen werden vom Collector still gekappt | WP-153 | belegt | collector/collectorcmd/discovery_auditdefect_test.go:335 TestAuditSco… |
| [F-107](#f-107) | P2 | Scope-Formular kann Credentials, VRF, Site, Plugins und Namen nicht setzen | WP-153 | belegt | keiner (nicht kodifiziert: ohne Zieldesign wären die Bedienelemente g… |
| [F-108](#f-108) | P2 | Zoniertes IPv6-Scope-Ziel wird akzeptiert; Offline-Detektor und Kanten-Cleanup brechen für alle danach sortie… | WP-153, WP-157, WP-159 | belegt | backend/internal/discovery/zone_isolation_auditdefect_test.go:51 Test… |
| [F-109](#f-109) | P2 | Scope-Zeitpläne (Cron/Intervall) werden beim Collector-Start ignoriert: jeder Scope läuft sofort und nach jed… | WP-154 | belegt | collector/collectorcmd/discovery_auditdefect_test.go:376 TestAuditSco… |
| [F-110](#f-110) | P2 | Installierter Collector passt nicht zum Enroll-Befehl der UI (Binary-Name und Credentials-Pfad abweichend) | WP-156 | belegt | tests/audit/collector-install-cli.test.sh (FAILS) |
| [F-111](#f-111) | P2 | Cleanup behandelt Cron-Zeitpläne als 24 h; Kanten wöchentlicher Scopes werden nach 7 Tagen gelöscht | WP-159 | belegt | backend/internal/relationship/cleanup_cron_auditdefect_test.go:32 Tes… |
| [F-112](#f-112) | P2 | Redundanzflag von powered_by hängt von Batch- und Lane-Reihenfolge ab und wird nie korrigiert (im Real-Stack … | WP-161 | belegt | backend/internal/discovery/power_chain_auditdefect_test.go:47 TestAud… |
| [F-113](#f-113) | P2 | Der SIM-01-Demo-Admin admin@demo.local kann sich nicht anmelden; die geseedete Demo-Org ist über die UI nicht… | WP-203 | belegt | tests/audit/demo-realm-users.test.sh (FAILS) |
| [F-114](#f-114) | P2 | JSONB-Prädikate unter RLS nicht indexfähig: Attribut-Ausdrucksindex ungenutzt, Identitätssuche mit MACs als o… | WP-204, WP-205 | belegt | backend/internal/discovery/identity_plan_auditdefect_test.go:72 TestA… |
| [F-115](#f-115) | P2 | Die EXPLAIN-Prüfung nach ABN-02 in TestCIQueryBudget ist vakuös: sie akzeptiert einen Org-Index-Scan über die… | WP-204 | belegt | keiner (nicht kodifiziert: braucht ein DDL-Job-Fixture) |
| [F-116](#f-116) | P2 | Das Suchbudget nach NFR-02 wird am Namensfilter der CI-Liste gemessen statt an der globalen Suche; Szenarien … | WP-204, WP-206 | belegt | keiner (nicht kodifiziert: k6-Harness) |
| [F-117](#f-117) | P2 | Die p95-Budgets nach NFR-02 für list und search werden nicht robust eingehalten (2 von 3 Läufen auf der akzep… | WP-206 | Hypothese | keiner (k6-Schwellen in tests/load/api.js) |
| [F-118](#f-118) | P2 | Der Nachweis zu NFR-03 ist nicht repräsentativ: Ingest nur mit neuen Geräten ohne MACs und Interfaces, keine … | WP-205, WP-206 | Hypothese | keiner |
| [F-119](#f-119) | P2 | Internes Review stuft SR-11 als 'Low' ein, obwohl CI-13 [B] in der Produktionsverdrahtung 501 liefert | WP-209 | belegt | tests/audit/g1-gate-evidence.test.sh – known_defect_SR-11 (Assertion … |
| [F-120](#f-120) | P2 | Gate-Checkliste und Abnahmeprotokoll führen Anforderungen allein per Erklärung des Auftraggebers als PASS | WP-105, WP-210, WP-212, WP-213 | belegt | tests/audit/g1-gate-evidence.test.sh – pass_by_declaration (fails-as-… |
| [F-121](#f-121) | P2 | Traceability-Prüfung akzeptiert jeden existierenden Pfad als 'testdatei'; G1-Vollständigkeit 219/219 nur über… | WP-005, WP-213 | belegt | backend/internal/traceability/traceability_evidence_auditdefect_test.… |
| [F-122](#f-122) | P2 | Kein CI-, Install-Smoke- oder Scan-Lauf auf dem aktuellen Release-Kandidaten dokumentiert; G1-Nachweise bezie… | WP-209, WP-211, WP-213 | belegt | tests/audit/g1-gate-evidence.test.sh – install_evidence_current (fail… |
| [F-123](#f-123) | P2 | Lint-Baseline (new-from-rev) verdeckt 472 Altbefunde; das versprochene Folge-WP wurde nie angelegt | WP-001 | belegt | keiner (Plan-/Doku-Mangel, in der Defekttest-Welle bewusst nicht kodi… |
| [F-124](#f-124) | P2 | Ausgeliefertes Image reticora-postgres wird nie auf Schwachstellen gescannt | WP-004 | belegt | tests/audit/ops-config.test.sh – image_scan_coverage (fails-as-expect… |
| [F-125](#f-125) | P2 | Installer-Wiederholung wendet rotiertes Keycloak-Admin-Passwort, OIDC-Client-Secret und andere Realm-Änderung… | WP-096 | belegt | keiner (benötigt echten Keycloak und Installer-Wiederholung; in der D… |
| [F-126](#f-126) | P2 | /readyz prüft SMTP nicht, obwohl SMTP konfiguriert ist | WP-097 | belegt | backend/cmd/server/startup_readiness_auditdefect_test.go – TestAuditT… |
| [F-127](#f-127) | P2 | object_storage-Readiness wählt das rohe RETICORA_S3_ENDPOINT; URL-förmige oder portlose Endpunkte, die der S3… | WP-097 | belegt | backend/cmd/server/startup_readiness_auditdefect_test.go – TestAuditT… |
| [F-128](#f-128) | P2 | K8s-Backup-Komponente provisioniert keine WAL-Archivierung | WP-100 | belegt | tests/audit/ops-config.test.sh – k8s_wal_archive (fails-as-expected). |
| [F-129](#f-129) | P2 | Versionierung/Replikation des Backup-Buckets nicht erzwungen; Terraform-Regel 'production requires replica' i… | WP-100 | belegt | tests/audit/ops-config.test.sh – terraform_replica_missing, terraform… |
| [F-130](#f-130) | P2 | RPO-Grenzwertprüfung in restore-pitr.sh kann mit Default-Parametern nicht scheitern | WP-101 | belegt | tests/audit/ops-config.test.sh – restore_rpo_window (fails-as-expecte… |
| [F-131](#f-131) | P2 | Keine Signaturprüfung vor der Installation (TEC-15 'Signaturprüfung vor Installation') | WP-102 | belegt | tests/audit/ops-config.test.sh – install_signature_install-vm, instal… |
| [F-132](#f-132) | P2 | alertmanager.yaml wird von Alertmanagers Konfigurationsprüfung abgelehnt (kein globaler SMTP-Smarthost) | WP-103 | belegt | tests/audit/ops-config.test.sh – alertmanager_smtp, alertmanager_amto… |
| [F-133](#f-133) | P2 | Vier registrierte Worker exportieren keine Lauf-/Fehler-/Last-Success-Metriken; Worker-Backlog nur für 3 von … | WP-103 | belegt | keiner (nur Overlay-Probe SP/work/verify-B13/zz_vb13_jobs_metrics_pro… |
| [F-134](#f-134) | P2 | Betreiberalarmierung nicht an NTF-Kanäle angebunden, obwohl WP-137 geliefert ist | WP-103 | belegt | keiner (in der Defekttest-Welle nicht kodifiziert) |
| [F-135](#f-135) | P2 | SMTP-Ausfallszenario wird als N/A gemeldet, obwohl SMTP-Zustellung existiert | WP-104 | belegt | tests/audit/ops-config.test.sh – outage_smtp (fails-as-expected). |
| [F-136](#f-136) | P2 | 'Kein stiller Datenverlust' wird nur über eine vor dem Ausfall geschriebene psql-Markerzeile assertiert | WP-104 | belegt | keiner (in der Defekttest-Welle nicht kodifiziert) |
| [F-137](#f-137) | P2 | Subprozessorenliste leer; externe Lieferung (E-35/A-11) fehlt | WP-105 | belegt | tests/audit/g1-gate-evidence.test.sh – subprocessors (fails-as-expect… |
| [F-138](#f-138) | P2 | ABN-01 (a)/(b): v2-Originaltexte fehlen und sind nicht eingefroren (E-02/A-02 offen) | WP-212 | belegt | tests/audit/g1-gate-evidence.test.sh – abn01_texts (fails-as-expected… |
| [F-139](#f-139) | P2 | Erneuerte Zertifikate werden nie geladen: kein nginx-Reload nach 'certbot renew' | WP-094 | Hypothese | keiner |
| [F-140](#f-140) | P2 | PATCH height_units akzeptiert Höhe unterhalb bestehender Mounts | WP-108 | belegt | backend/internal/rack/height_auditdefect_test.go TestAuditDefectRackH… |
| [F-141](#f-141) | P2 | asset/quantity_item ohne denormalisierte site_id: Site-Scope greift nicht für platzierte Assets | WP-109 | belegt | backend/internal/locations/sitescope_auditdefect_test.go TestAuditDef… |
| [F-142](#f-142) | P2 | Keine UI zum Ausstellen, Erneuern oder Annehmen von Einladungen | WP-114 | belegt | frontend/src/pages/UserManagementPage.auditdefect.tsx (fails-as-expec… |
| [F-143](#f-143) | P2 | Discovery überspringt Datentypvalidierung typisierter CI-Spalten: ungültige Werte persistiert oder ganze Batc… | WP-117 | belegt | backend/internal/discovery/typedvalues_auditdefect_test.go TestAuditD… |
| [F-144](#f-144) | P2 | POST /cis akzeptiert privaten CI-Typ einer fremden Organisation | WP-120 | belegt | backend/internal/server/audit_t6_router_auditdefect_test.go TestAudit… |
| [F-145](#f-145) | P2 | POST /relationships mit CI einer anderen Organisation oder unbekannter CI-ID antwortet 500 statt 404/422 | WP-010, WP-025, WP-128, WP-160, WP-163, WP-208 | belegt | frontend/e2e-real/tenant-isolation.spec.ts T14 (FAIL); keiner (Real-S… |
| [F-146](#f-146) | P2 | Discovery-Jobs werden nicht im generischen Jobmodell registriert (Adapter nicht verdrahtet) | WP-129 | belegt | backend/internal/server/audit_t6_router_auditdefect_test.go TestAudit… |
| [F-147](#f-147) | P2 | Kein Produktionsjob nutzt die NATS-Work-Queue; Bulk/Snapshot laufen als Goroutinen und bleiben nach Crash 'ru… | WP-130 | belegt | keiner (statische Evidenz, Queue-Aufbau inline in main()) |
| [F-148](#f-148) | P2 | Route-Audit speichert nur Feldnamen, keine Vorher-/Nachher-Werte | WP-133 | belegt | backend/internal/server/audit_t6_router_auditdefect_test.go TestAudit… |
| [F-149](#f-149) | P2 | Route-Audit nach der Response in separater Transaktion; Fehler nur geloggt (nicht lückenlos) | WP-133 | belegt | backend/internal/server/audit_t6_router_auditdefect_test.go TestAudit… |
| [F-150](#f-150) | P2 | NFR-06-Log-Retention (30 Tage) in keiner Auslieferung konfiguriert | WP-135 | belegt | tests/audit/log-retention.test.sh (FAILS: 2 passed, 1 failed) |
| [F-151](#f-151) | P2 | Webhook-Consumer verwirft Events bei transienten Fehlern, Outbox markiert sie als veröffentlicht | WP-136 | belegt | backend/internal/webhook/consume_auditdefect_test.go TestAuditDefectW… |
| [F-152](#f-152) | P2 | Gespeichertes SMTP-Passwort wird nach PATCH mit neuem Host an diesen gesendet | WP-137 | belegt | backend/internal/notify/channel_secret_auditdefect_test.go TestAuditD… |
| [F-153](#f-153) | P2 | Eigener SMTP-Kanal umgeht die Egress-Policy (interne Ziele, Cloud-Metadaten) | WP-137 | belegt | backend/internal/notify/channel_secret_auditdefect_test.go TestAuditD… |
| [F-154](#f-154) | P2 | Benachrichtigungs-Administration unter webhook:read statt notification:manage; Webhook-URL-Token unredigiert … | WP-137, WP-139 | belegt | backend/internal/server/audit_t6_router_auditdefect_test.go TestAudit… |
| [F-155](#f-155) | P2 | Client-Kontakte sind keine Empfängerquelle (NTF-03) | WP-138 | belegt | keiner (Audience-Feld fehlt, Test nicht kompilierbar) |
| [F-156](#f-156) | P2 | NTF-04-Digest nicht implementiert | WP-139 | belegt | keiner |
| [F-157](#f-157) | P2 | Neue Organisationen erhalten keine Benachrichtigungen (kein Default-Kanal, keine Kanal-UI) | WP-139 | belegt | keiner |
| [F-158](#f-158) | P2 | Master-Key-Rotation ändert abgeleitete Secrets (NATS-Edge-Issuer/-Passwort, Cursor-Key); nats-edge lädt nicht… | WP-141 | belegt | keiner (e2e-real erforderlich) |
| [F-159](#f-159) | P2 | CI-Typ-Admin ignoriert fehlgeschlagenes searchable/unique-Update still | WP-142 | belegt | frontend/src/pages/CITypeAdminPage.auditdefect.tsx (fails-as-expected) |
| [F-160](#f-160) | P2 | Redundanz-Postfilter meldet ein redundant versorgtes CI als betroffen, wenn die Tiefe die alternative Versorg… | WP-179, WP-180 | belegt | keiner (nicht kodiert; nur Verifier-Probe f1) |
| [F-161](#f-161) | P2 | GET /topology mit unbekannter oder fremder root_ci_id antwortet 500 statt 404 | WP-010, WP-031, WP-180, WP-195, WP-208 | belegt | Real-Stack frontend/e2e-real/tenant-isolation.spec.ts:1148 T15 – FAIL… |
| [F-162](#f-162) | P2 | Job-Cancel stoppt laufende Arbeit nicht (ci.bulk, Export): Status 'cancelled', aber alle Änderungen werden au… | WP-181, WP-182, WP-197 | belegt | backend/internal/ci/audit_t7_bulk_jobs_auditdefect_test.go:256 TestAu… |
| [F-163](#f-163) | P2 | Job-API ignoriert den Client-Scope: ein client-beschränkter Principal listet und liest Jobs und Ergebnisse an… | WP-182 | belegt | backend/internal/ci/audit_t7_bulk_jobs_auditdefect_test.go:316 TestAu… |
| [F-164](#f-164) | P2 | State-Snapshot-Job (und GET /cis/{id}/state) liefert immer einen leeren Zustand | WP-183 | belegt | keiner (nicht kodiert; Probe f7) |
| [F-165](#f-165) | P2 | Numerischer AST-Vergleich auf einem dynamischen Attribut bricht die ganze Abfrage ab, sobald ein sichtbares C… | WP-187 | belegt | keiner (nicht kodiert; Verifier-Probe) |
| [F-166](#f-166) | P2 | PATCH ohne 'shared' hebt die Org-Freigabe einer gespeicherten Sicht still auf | WP-188 | belegt | keiner (nicht kodiert; die geplante Defektdatei audit_t7_savedview_au… |
| [F-167](#f-167) | P2 | GET /cis/{id}/metrics zeigt Metriknamen von CIs außerhalb des Client-Scopes und antwortet 200 für unsichtbare… | WP-190 | belegt | keiner (nicht kodiert; Verifier-Probe) |
| [F-168](#f-168) | P2 | PDU-Messwerte werden nicht erhoben (nur UPS-MIB) | WP-190 | belegt | keiner |
| [F-169](#f-169) | P2 | Detailseiten ignorieren den gewählten Client-/Site-Scope | WP-191 | belegt | keiner (ScopeSwitcher.test.tsx kodiert das Ist-Verhalten) |
| [F-170](#f-170) | P2 | Tabellen ohne Pfeiltasten-Bedienung, ohne Tabellenbaustein und ohne Test | WP-192 | belegt | keiner |
| [F-171](#f-171) | P2 | Command-Palette gibt den Fokus nach dem Schließen nicht zurück (Fokus landet auf <body>) | WP-192 | belegt | keiner |
| [F-172](#f-172) | P2 | WCAG-2.1-AA-Kontrastverstöße auf Hauptseiten, die a11y.spec.ts nicht abdeckt | WP-193 | belegt | keiner (bestehende a11y.spec.ts 12/12 PASS deckt die betroffenen Seit… |
| [F-173](#f-173) | P2 | Browsermatrix deckt keine Vorversionen ab; Nicht-Chromium-Engines unbelegt | WP-193 | belegt | keiner |
| [F-174](#f-174) | P2 | Status-/Enum-Schlüssel werden roh statt über den i18n-Katalog angezeigt | WP-194 | belegt | keiner |
| [F-175](#f-175) | P2 | CI-Detail zeigt die Typ-UUID statt der lokalisierten Typbezeichnung | WP-194 | belegt | frontend/src/pages/CITypeLabels.auditdefect.tsx:153 'shows the locali… |
| [F-176](#f-176) | P2 | Review-Inbox: 'Neu anlegen' (action new) für unclassified_device scheitert immer; Items mit unbekanntem Typ e… | WP-196 | belegt | keiner (nicht kodiert; Verifier-Probe) |
| [F-177](#f-177) | P2 | Onboarding-Abnahmesuite: Schritt 'Scope bestätigen' (ABN-01 d) ist optional und unbelegt | WP-200 | belegt | frontend/e2e/onboarding.spec.ts (6/6 PASS, kodiert den Defekt nicht) |
| [F-178](#f-178) | P2 | Permissives Mock-Backend der E2E-Suite (unbekannte Routen → 200, keine Vertragsprüfung) maskiert UI→API-Verdr… | WP-201 | belegt | keiner |
| [F-179](#f-179) | P2 | Filter-AST validiert Wertformate nicht; DB-Fehlertext mit SQLSTATE wird in 400-Antworten offengelegt | WP-187 | belegt | keiner |
| [F-180](#f-180) | P3 | GET /api/v1/racks/{id}/mounts eines Racks außerhalb des Scopes antwortet 200 (leer) statt 404 (Existenzorakel) | WP-014, WP-208 | belegt | TestAuditT1RackChannelSiteScope (FAILS_AS_EXPECTED). |
| [F-181](#f-181) | P3 | Client-Grant kombiniert mit Site-Grant schneidet die Dimensionen und verengt den Zugriff unter die Vereinigun… | WP-009 | belegt | keiner (TestResolveAccessUnitesScopesPerDimension asserted das aktuel… |
| [F-182](#f-182) | P3 | Client-gescopter Principal kann keine Beziehung zu einem sichtbaren org-weiten (client NULL) CI anlegen | WP-025 | belegt | keiner (nur Probe probe_edge_orgwide.sql). |
| [F-183](#f-183) | P3 | Webhook RotateSecret (und Delivery-Requestpfade) nutzen einen org-weiten Scope statt des Request-Scopes | WP-016 | belegt | keiner (nur Probe TestVerifyB02RotateSecretWithoutScope, SP/work/veri… |
| [F-184](#f-184) | P3 | Webhook-Retry verwirft bei Claim-Fehler einer Organisation bereits geleaste Datensätze; keine faire Org-Reihe… | WP-022 | Hypothese | keiner |
| [F-185](#f-185) | P3 | link_count/ci_count driften unter RLS und sind direkt schreibbar; Dokumente können für alle unsichtbar oder f… | WP-028 | belegt | keiner (nur Probe probe_doc_link_count.sql). |
| [F-186](#f-186) | P3 | Traceability meldet MGT-02 (Disposal) durch WP-028 geschlossen, disposal_record behält aber eine reine Org-Po… | WP-028 | belegt | keiner |
| [F-187](#f-187) | P3 | Migration 000066 meldet nicht konforme Storage-Keys nur als Anzahl und leert sie irreversibel | WP-030 | belegt | keiner (nur Probe probe_storage_key_cleanup.sql). |
| [F-188](#f-188) | P3 | Dokument-Formular verlangt weiterhin einen 'Storage-Key', den die API ignoriert; kein Upload/Download in der … | WP-030 | belegt | keiner |
| [F-189](#f-189) | P3 | Frontend agentApi.enroll ist tot und veraltet (ohne enrollment_token) | WP-038 | belegt | keiner |
| [F-190](#f-190) | P3 | Tenant-Guard erkennt Pool-Zugriffe über lokalen Alias, Method-Value oder direkte pgx-Verbindung nicht | WP-041 | belegt | keiner (nur Scratch-Kopie-Probe). |
| [F-191](#f-191) | P3 | WithSystem-Aufrufer sind entgegen E-08 und pool.go-Kommentar durch keine Freigabeliste geregelt | WP-041 | belegt | keiner |
| [F-192](#f-192) | P3 | reticora_owner (per SET ROLE aus reticora_app erreichbar) kann Mandantentabellen trotz FORCE RLS organisation… | WP-064 | belegt | keiner (nur Probe truncate_probe.sql). |
| [F-193](#f-193) | P3 | Down-Migration 000056 droppt die clusterweite Rolle reticora_app und scheitert, wenn andere Datenbanken des C… | WP-064 | belegt | keiner (Reproduktion über den bestehenden TestMigrationsRoundtrip im … |
| [F-194](#f-194) | P3 | TST-02-Meta-Test ordnet die Zertifikatserneuerung einem Unit-Test zu und prüft nur Funktionsnamen | WP-208 | belegt | keiner |
| [F-195](#f-195) | P3 | Deaktivierung widerruft auch die vom Benutzer erzeugten Service-Account-API-Keys | WP-042 | belegt | keiner |
| [F-196](#f-196) | P3 | Keine UI zum Deaktivieren, Reaktivieren oder Löschen von Benutzern | WP-042 | belegt | keiner |
| [F-197](#f-197) | P3 | Login-Abweisung zeigt rohen englischen Backend-Fehlertext | WP-043 | belegt | keiner |
| [F-198](#f-198) | P3 | Parallele Erstlogins in eine leere Organisation werden alle zugelassen | WP-043 | belegt | keiner |
| [F-199](#f-199) | P3 | Permission credential:decrypt ist geseedet, steuert aber keine Route | WP-045 | belegt | keiner |
| [F-200](#f-200) | P3 | Keycloak-Realm-Rollen erreichen das Backend nicht; README behauptet Mapping | WP-046 | belegt | keiner |
| [F-201](#f-201) | P3 | Rollen-Tab zeigt die vier RBA-02-Systemrollen als 'Custom' (API: is_system=false) | WP-046 | belegt | frontend/e2e-real/roles-authz.spec.ts:798 09b (FAIL) |
| [F-202](#f-202) | P3 | Log-Redaktion deckt Map/Struct/key=value nicht ab; Webhook-URLs für Viewer sichtbar | WP-047 | belegt | keiner |
| [F-203](#f-203) | P3 | IGA-Connector-Update speichert base_url ohne SSRF-/https-Validierung | WP-049 | belegt | backend/internal/iga/t2_egress_auditdefect_test.go (FAILS_AS_EXPECTED) |
| [F-204](#f-204) | P3 | Notification-Webhook-Kanal-URL beim Speichern nicht gegen Egress-Policy geprüft | WP-049 | belegt | keiner |
| [F-205](#f-205) | P3 | IPv6-Übergangspräfixe (6to4, Teredo, IPv4-kompatibel) mit internem IPv4 erlaubt | WP-049 | belegt | keiner |
| [F-206](#f-206) | P3 | Idempotenz-Replay liefert gespeicherte Fehler wörtlich (alte trace_id; 5xx 24 h festgeschrieben) | WP-050, WP-163 | belegt | keiner |
| [F-207](#f-207) | P3 | Kompiliertes Agent-Binary im Repository | WP-051 | belegt | keiner |
| [F-208](#f-208) | P3 | Operator-Aktion kann ohne operator_audit-Eintrag wirksam werden (Audit nicht atomar) | WP-070, WP-074 | belegt | keiner |
| [F-209](#f-209) | P3 | Entitlement-Grant akzeptiert beliebige Feature- und Quota-Schlüssel | WP-071 | belegt | keiner |
| [F-210](#f-210) | P3 | Entfernen des DefaultPlan-Fallbacks ohne Backfill lässt Orgs mit Teilzeilen ohne Planfeatures | WP-072 | belegt | keiner |
| [F-211](#f-211) | P3 | org_admin behält den toten Schlüssel entitlement:manage | WP-074 | belegt | keiner |
| [F-212](#f-212) | P3 | Datenkorrektur 000086 behält implizit provisionierte Add-on-Zeilen | WP-075 | belegt | keiner |
| [F-213](#f-213) | P3 | OpenAPI deklariert application/json für eine Fehlerantwort (forms submissions 400) | WP-163 | belegt | keiner |
| [F-214](#f-214) | P3 | Leere IPAM-Listen liefern "data": null | WP-164 | belegt | keiner |
| [F-215](#f-215) | P3 | CompleteList-Endpunkte ignorieren limit/max 200 | WP-164 | Hypothese | keiner |
| [F-216](#f-216) | P3 | Searchability/Typ von Attributfiltern org-weit pro Key aufgelöst | WP-165 | belegt | keiner |
| [F-217](#f-217) | P3 | X-RateLimit-Header und Retry-After fehlen bei vor dem Limiter abgewiesenen Antworten | WP-166 | belegt | keiner |
| [F-218](#f-218) | P3 | Deprecation-Header still deaktiviert, wenn die Spec-Datei fehlt | WP-167, WP-168 | belegt | keiner |
| [F-219](#f-219) | P3 | Successor-Link mit unexpandierten URI-Templates | WP-167 | belegt | keiner |
| [F-220](#f-220) | P3 | Keine Lifecycle-Header auf 401/403 deprecated Operationen | WP-167 | belegt | keiner |
| [F-221](#f-221) | P3 | Restore-Fenster fest 90 Tage statt konfigurierter Retention | WP-169 | belegt | keiner |
| [F-222](#f-222) | P3 | Restore-Konflikt 409 nicht in OpenAPI dokumentiert | WP-169 | belegt | keiner |
| [F-223](#f-223) | P3 | include kappt eingebettete Sammlungen still bei 200 | WP-169 | Hypothese | keiner |
| [F-224](#f-224) | P3 | 000056-down scheitert auf Altinstallationen ohne migration_quarantine; backup-dr.md:209 verspricht Rollback | WP-052 | belegt | keiner |
| [F-225](#f-225) | P3 | Standort-Löschen im Baum ohne Bestätigung; 409 ohne Abhängigkeitsarten | WP-053 | belegt | keiner |
| [F-226](#f-226) | P3 | History-Endpunkt akzeptiert entfernten Typ location_node und weist location ab | WP-053 | belegt | keiner |
| [F-227](#f-227) | P3 | location_change-Historie wird mit dem Standort gelöscht (ON DELETE CASCADE) | WP-055 | belegt | keiner |
| [F-228](#f-228) | P3 | Bearbeiten-Dialog sendet alle Felder: veralteter Editor erhält 409 trotz Änderung nur eines unberührten Felds | WP-057, WP-061 | belegt | frontend/e2e-real/ci-lifecycle.spec.ts L12b |
| [F-229](#f-229) | P3 | CI-Update-Handler bildet alle unerwarteten Fehler auf 404 'CI not found' ab | WP-057 | belegt | keiner |
| [F-230](#f-230) | P3 | source-policy-API liefert Altreihenfolge im Widerspruch zu REC-03 | WP-058 | belegt | keiner |
| [F-231](#f-231) | P3 | Override-Konflikte aus Workflow und Agent erzeugen kein override_conflict-Review (REC-12) | WP-059, WP-089 | belegt | keiner |
| [F-232](#f-232) | P3 | DELETE /cis/{id}/fields/{name}/override bildet jeden Fehler auf 404 ab | WP-060 | belegt | keiner |
| [F-233](#f-233) | P3 | Gespoolte Agent-Telemetrie bei 4xx/unlesbar ohne Verlustzähler verworfen | WP-062 | belegt | keiner |
| [F-234](#f-234) | P3 | Server protokolliert Spool-Verlustmeldungen nur; Metrik-Samples/Traps ungezählt verworfen | WP-062 | belegt | keiner |
| [F-235](#f-235) | P3 | Wiederholtes „Kunden benachrichtigen“ erzeugt doppelte pending-Benachrichtigungen | WP-063 | belegt | keiner |
| [F-236](#f-236) | P3 | Wartungs-UI kann keine CIs zuordnen und zeigt den gespeicherten Benachrichtigungsstatus nie | WP-063 | belegt | keiner |
| [F-237](#f-237) | P3 | CI-Detailseite zeigt Standort/Site/Raum als rohe UUIDs und Zeitstempel unformatiert | WP-077, WP-171 | belegt | keiner |
| [F-238](#f-238) | P3 | Asset-Karte ohne RFID/Kaufdatum/Rechnungsnummer; Seriennummer bei verknüpftem CI editierbar (422 erst nach Sp… | WP-077 | belegt | keiner |
| [F-239](#f-239) | P3 | Verknüpfen mit standortlosem Asset löscht still Standort, Site und Raum des CI | WP-077 | belegt | keiner |
| [F-240](#f-240) | P3 | Standortänderung von Geschwister-CIs eines gemeinsamen Assets ohne ci_change/Audit | WP-078 | belegt | keiner |
| [F-241](#f-241) | P3 | Rack-Platzierung ändert ci.location_id ohne Versionserhöhung (ETag) | WP-078 | belegt | keiner |
| [F-242](#f-242) | P3 | Beziehungsformular bietet strukturelle Typen mounted_in/located_in an (immer 422) | WP-079, WP-127 | belegt | keiner |
| [F-243](#f-243) | P3 | Migration 000089 übernimmt zyklische Alt-contains-Zeilen als parent_ci_id-Zyklus | WP-079 | belegt | keiner |
| [F-244](#f-244) | P3 | Saved-View-Beziehungsfilter sehen projiziertes Enthaltensein nicht | WP-079 | belegt | keiner |
| [F-245](#f-245) | P3 | last_seen_at/first_seen_at aus Ankunftszeit statt observed_at (GLO-12-Rest) | WP-084 | belegt | keiner |
| [F-246](#f-246) | P3 | Seriennummer: ResolveIdentity case-insensitiv, PG-Kandidatensuche case-sensitiv | WP-086 | belegt | keiner |
| [F-247](#f-247) | P3 | REC-08-Eskalation (>14 Tage) wird im Dashboard nicht angezeigt | WP-088 | belegt | keiner |
| [F-248](#f-248) | P3 | Verworfener override_conflict-Wert bleibt nach Wertwechsel unterdrückt | WP-089 | belegt | keiner |
| [F-249](#f-249) | P3 | Manuell gepflegte IPs (reserved/static) und Interfaces an entdeckten CIs nach drei Läufen gelöscht | WP-090 | belegt | keiner |
| [F-250](#f-250) | P3 | Discovery-Reklassifizierung läuft in separater Transaktion nach dem Record-Update | WP-172 | belegt | keiner |
| [F-251](#f-251) | P3 | ci_merge.job_id nie gesetzt; abgelaufene Snapshots/Tombstones nie bereinigt | WP-173 | belegt | keiner |
| [F-252](#f-252) | P3 | Rack-Seite lädt jedes montierte CI einzeln (1+N); putMount-Client ungenutzt | WP-175 | belegt | keiner |
| [F-253](#f-253) | P3 | GET /racks/{id}/layout liefert "mounts": null für leeres Rack (OpenAPI: Pflicht-Array) | WP-175 | belegt | frontend/e2e-real/locations-racks.spec.ts L08 |
| [F-254](#f-254) | P3 | Client-seitige Ausfallsimulation folgt roher source→target-Richtung; ihr Vitest kodiert das alte Verhalten | WP-176 | belegt | frontend/src/pages/TopologyPage.test.tsx (kodiert altes Verhalten) |
| [F-255](#f-255) | P3 | Impact-Traversierung per Ebenen-BFS statt rekursivem CTE ohne formale Entscheidung; ungültiger depth-Paramete… | WP-177 | belegt | keiner |
| [F-256](#f-256) | P3 | Der Collector-Statuslauf bricht beim ersten Org-Fehler für alle restlichen Orgs ab | WP-143 | belegt | keiner |
| [F-257](#f-257) | P3 | PATCH /collectors/{id} antwortet bei unbekanntem oder fremdem Collector mit 500 statt 404 | WP-143 | belegt | keiner |
| [F-258](#f-258) | P3 | Der Collector protokolliert 'collector NATS endpoint connected over mTLS', obwohl der Endpunkt ihn abgewiesen… | WP-145, WP-155 | belegt | keiner (in discovery-override.spec.ts D08 nur kommentiert) |
| [F-259](#f-259) | P3 | Die Firewall-Matrix beschreibt Collector-Verkehr, den es nicht gibt (Selbsterneuerung, Heartbeat und Ingest ü… | WP-146 | belegt | keiner |
| [F-260](#f-260) | P3 | Auto-Update braucht cosign auf dem Host, den der VM-Installer nicht bereitstellt | WP-148 | belegt | keiner |
| [F-261](#f-261) | P3 | Widersprüchliche Capability-Doku (icmp.go vs. Dockerfile) | WP-149 | belegt | keiner |
| [F-262](#f-262) | P3 | Der Sweep startet je Ziel eine Goroutine, bevor die Semaphore mit 256 Plätzen belegt wird | WP-149 | belegt | keiner |
| [F-263](#f-263) | P3 | Das Profilfeld systemTemplate ist ein totes Label; das Backend wertet es nie aus | WP-151 | belegt | keiner |
| [F-264](#f-264) | P3 | Scope-Credentials werden nur auf die Org geprüft: ein Credential von Client 2 lässt sich an einen Scope von C… | WP-153 | belegt | keiner |
| [F-265](#f-265) | P3 | Docker-/Bridge-Netze des Collector-Hosts werden als Scopes vorgeschlagen | WP-153, WP-156 | belegt | keiner |
| [F-266](#f-266) | P3 | Poll-Intervalle nur je Org und Protokoll einstellbar, nicht je Scope; Cron wird in UTC ausgewertet | WP-154 | belegt | keiner |
| [F-267](#f-267) | P3 | Discovery-Metriken undokumentiert; Scope-Aktionen ohne Fehlerrückmeldung, Löschen ohne Bestätigung | WP-153, WP-155 | belegt | keiner |
| [F-268](#f-268) | P3 | ci.online ist dokumentiert, wird bei der Rückkehr eines CIs aber praktisch nie gesendet | WP-157 | belegt | keiner |
| [F-269](#f-269) | P3 | LLDP-Nachbar wird mit FDB-Confidence 0,6 gespeichert, wenn ein FDB-Eintrag vorher gemeldet wird; der Wert kip… | WP-158 | belegt | keiner |
| [F-270](#f-270) | P3 | Die Provenienz einer Kante (Akteur, Zeit) ist per PATCH attributes überschreibbar | WP-159 | belegt | keiner |
| [F-271](#f-271) | P3 | POST /relationships erlaubt Nutzern Kanten mit source 'discovery' | WP-159, WP-160 | belegt | keiner |
| [F-272](#f-272) | P3 | DELETE /relationships bildet alle Fehler außer ErrForceRequired auf 404 ab | WP-160 | Hypothese | keiner |
| [F-273](#f-273) | P3 | Eine Suppression ist irreversibel und entsteht mit einem einzigen Klick | WP-160 | belegt | keiner |
| [F-274](#f-274) | P3 | Das Akzeptanzkriterium von WP-160 widerspricht REL-04 (wer force braucht) | WP-160 | belegt | keiner |
| [F-275](#f-275) | P3 | Ausgeschaltete Outlets zählen als Stromeinspeisung (Redundanz) | WP-161 | Hypothese | keiner |
| [F-276](#f-276) | P3 | Die Simulationssperre hängt an einem exakt gesetzten RETICORA_ENVIRONMENT (fail-open) | WP-162 | belegt | keiner |
| [F-277](#f-277) | P3 | Der Seed umgeht den Suchindex: geseedete CIs findet die globale Suche nicht | WP-203, WP-204 | belegt | keiner |
| [F-278](#f-278) | P3 | Geseedete CIs haben keinen Standort: site_id und room_id setzt der Trigger ci_derive_location auf NULL | WP-203 | belegt | keiner |
| [F-279](#f-279) | P3 | Der Lastbericht lässt 1.000 Records/s und die Volumina aus NFR-01 aus | WP-206 | belegt | keiner |
| [F-280](#f-280) | P3 | TestCIQueryBudget ist planner- und umgebungsabhängig instabil (falscher Fehlschlag 'sequential scan') | WP-204, WP-206 | Hypothese | backend/internal/ci/querybudget_integration_test.go TestCIQueryBudget… |
| [F-281](#f-281) | P3 | integrity-check.sql: vakuöse Duplikatprüfungen, keine Prüfung schwacher Identitäten, keine Cross-Tenant-Prüfu… | WP-207 | belegt | keiner |
| [F-282](#f-282) | P3 | Ohne RETICORA_NATS_URL nimmt der HTTP-Ingest keine Identitätssperren; zwei Instanzen können Geräte mit schwac… | WP-207 | belegt | keiner |
| [F-283](#f-283) | P3 | CI-Zusammenfassung des DB-Jobs scheitert nur an einer bestimmten Skip-Meldung | WP-003 | belegt | keiner (nur Probe SP/work/verify-B01/wp003/summary.sh) |
| [F-284](#f-284) | P3 | Ablauf von Scan-Ausnahmen wird nicht gegen den SEC-11-SLA-Deckel geprüft; Trivy behandelt Einträge ohne exp: … | WP-004 | belegt | keiner (nur Probe SP/work/verify-B01/wp004/go/eval.sh) |
| [F-285](#f-285) | P3 | README überzeichnet G1-Blockadeumfang und Vollständigkeit der Traceability | WP-005 | belegt | keiner |
| [F-286](#f-286) | P3 | WP-Bereiche in PR-Titeln werden vom Traceability-Job nicht expandiert | WP-005 | belegt | keiner |
| [F-287](#f-287) | P3 | Roundtrip-Test scheitert auf geteilten PostgreSQL-Clustern (clusterweite Rolle reticora_app wird gelöscht) | WP-006 | belegt | keiner |
| [F-288](#f-288) | P3 | Health-Prüfung wertet nur den ersten Container je Dienst aus; beendetes Replikat eines skalierten Dienstes be… | WP-092 | belegt | keiner |
| [F-289](#f-289) | P3 | Offline-Detector-Schalter umgeht die Worker-Konvention; dokumentierter Konventionsname wird stillschweigend i… | WP-098 | belegt | keiner |
| [F-290](#f-290) | P3 | Backup-Scheduler reticora-backup.sh stürzt bei RETICORA_BACKUP_HOUR=08/09 ab und plant ungültige Werte stills… | WP-100 | belegt | keiner |
| [F-291](#f-291) | P3 | pgBackRest-Tarball ohne Prüfsumme; Basisimage ungepinnt (auch im signierten Release-Image) | WP-100 | belegt | keiner |
| [F-292](#f-292) | P3 | Dokumentierte Restore-Messwerte stammen aus einem Nicht-Default-Lauf (30 s) auf Spielzeugdatenmenge | WP-101 | belegt | keiner |
| [F-293](#f-293) | P3 | K8s-Overlays referenzieren Images, die das Release nie veröffentlicht | WP-102 | belegt | keiner |
| [F-294](#f-294) | P3 | Release-Doku nennt drei Images, der Workflow baut und signiert vier | WP-102 | belegt | keiner |
| [F-295](#f-295) | P3 | ReticoraWorkerNoSuccess kann für einen seit Prozessstart nie erfolgreichen Worker nicht feuern | WP-103 | belegt | keiner (nur SP/work/verify-B13/prom/worker-rules.test.yaml) |
| [F-296](#f-296) | P3 | Collector-Szenario 'Server unerreichbar' ist nur komponentenbasiert, nicht Compose-basiert | WP-104 | belegt | keiner |
| [F-297](#f-297) | P3 | Internes Review bewertet die Critical/High-Advisories der Dev-Abhängigkeiten nicht | WP-209 | belegt | keiner |
| [F-298](#f-298) | P3 | ABN-01 (e)-Abnahmeprotokoll stützt sich auf gemockten Lauf; Real-Stack-Nachweis nicht referenziert | WP-212 | belegt | keiner |
| [F-299](#f-299) | P3 | Onboarding-Impact-Schritt prüft keine betroffenen CIs | WP-212 | belegt | keiner |
| [F-300](#f-300) | P3 | rep01-GATE-01-Prüfung erkennt Formatvarianten, Anhangzeilen und offene High-Befunde nicht | WP-210, WP-213 | belegt | tests/audit/g1-gate-evidence.test.sh – rep01_* (7 fails-as-expected, … |
| [F-301](#f-301) | P3 | KI-Chunk-Indexer (Add-on 'ai') ist in der G1-Voreinstellung aktiv | WP-213 | belegt | keiner (nur Probe) |
| [F-302](#f-302) | P3 | POST /locations beantwortet doppelte Namen mit generischem 409 ohne Feldbezug | WP-107 | belegt | frontend/e2e-real/locations-racks.spec.ts L02 (Soft-Assertion Z.434 F… |
| [F-303](#f-303) | P3 | Signup-Verifikation scheitert mit 500, wenn 20 Slug-Kandidaten belegt sind | WP-111 | belegt | keiner |
| [F-304](#f-304) | P3 | Signup-Limit je IP als festes Uhrzeit-Stundenfenster: 10 Signups in 61 s möglich | WP-112 | belegt | keiner |
| [F-305](#f-305) | P3 | Einladung akzeptiert Client-Rolle mit Org-Scope und erzeugt Nutzer ohne wirksame Rechte | WP-114 | belegt | keiner |
| [F-306](#f-306) | P3 | Instanzdefinitionen nicht in attribute_definition übernommen (zweite Definitionsquelle) | WP-116 | belegt | keiner |
| [F-307](#f-307) | P3 | attribute_definition.validation akzeptiert den MET-45-Regel-AST nicht; OpenAPI beschreibt Form nicht | WP-116 | belegt | keiner |
| [F-308](#f-308) | P3 | Org-globale Attributdefinitionen werden erzwungen, aber nicht materialisiert/versioniert | WP-119 | belegt | keiner |
| [F-309](#f-309) | P3 | ci.schema_version wird bei reinen Typed-Column-Writes nicht aktualisiert | WP-119 | belegt | keiner |
| [F-310](#f-310) | P3 | Migration 000110 bricht ab, wenn ein suffigierter Duplikatname bereits existiert | WP-123 | belegt | keiner |
| [F-311](#f-311) | P3 | Interface-Anlage auf fremdem CI antwortet 500 statt 404 | WP-123 | belegt | keiner |
| [F-312](#f-312) | P3 | Interface-Löschung scheitert (irreführende 404), wenn dieselbe Adresse ungebunden im selben VRF existiert | WP-124 | belegt | keiner |
| [F-313](#f-313) | P3 | allowed_source_types/allowed_target_types/cardinality werden gespeichert, aber nie durchgesetzt | WP-127 | belegt | keiner |
| [F-314](#f-314) | P3 | Beziehungsdialog bleibt nach Doppelklick trotz erfolgreicher Anlage mit Konfliktfehler offen (intermittierend) | WP-128, WP-201 | Hypothese | frontend/e2e-real/ci-lifecycle.spec.ts L08 (UI-Soft-Assertions interm… |
| [F-315](#f-315) | P3 | Export-Stale-Reclaim kann lange Exporte doppelt ausführen | WP-130 | Hypothese | keiner |
| [F-316](#f-316) | P3 | Audit-Kette synchron unter Org-Lock bis COMMIT statt asynchroner Outbox mit Single-Runner (AUD-04) | WP-132 | belegt | keiner |
| [F-317](#f-317) | P3 | Continuous Aggregate metric_sample_1h ohne Retention-Policy (400 Tage) | WP-135 | belegt | keiner |
| [F-318](#f-318) | P3 | Outbox-Cleanup behält das neueste Event jeder Entität dauerhaft | WP-135 | belegt | keiner |
| [F-319](#f-319) | P3 | Domain-Events nach Commit in separater Transaktion (review.created, export.completed, job.failed, webhook.dea… | WP-136 | Hypothese | keiner |
| [F-320](#f-320) | P3 | Webhook-Empfänger erhalten keine Event-ID/entity_seq; Redeliveries tragen neue Delivery-ID | WP-136 | belegt | keiner |
| [F-321](#f-321) | P3 | Stream-Retention nicht konfigurierbar (EVT-01 '30 Tage / 10 GB … konfigurierbar') | WP-136 | belegt | keiner |
| [F-322](#f-322) | P3 | Keine persistierten Abonnements, keine Empfänger-i18n, kein Org-Branding | WP-138 | belegt | keiner |
| [F-323](#f-323) | P3 | review.created erreicht client-gescopte Reviewer nicht | WP-140 | Hypothese | keiner |
| [F-324](#f-324) | P3 | Review-Items außerhalb reviews.Service (Merge-Konflikte) benachrichtigen nicht | WP-140 | Hypothese | keiner |
| [F-325](#f-325) | P3 | Flags bestehender Attribute in der UI nicht änderbar | WP-142 | belegt | keiner |
| [F-326](#f-326) | P3 | Topologie-Simulation zeigt bei /impact-Fehler still eine richtungsnaive Client-Berechnung | WP-180, WP-195 | Hypothese | keiner |
| [F-327](#f-327) | P3 | Bulk-Bericht meldet eine fehlerhafte CI-ID mit Code 500 'the CI could not be updated' statt der 400 der Einze… | WP-181 | belegt | frontend/e2e-real/bulk-jobs-export.spec.ts:582 B04 – FAIL (Real-Stack) |
| [F-328](#f-328) | P3 | UI-Bulk auf 25 sichtbare Zeilen begrenzt; der Job-Pfad (≥ 100 IDs) ist aus der CI-Liste unerreichbar | WP-181 | Hypothese | keiner |
| [F-329](#f-329) | P3 | Revertierter Job bleibt 'succeeded'; zweiter Revert liefert 409 mit Detail 'not found' | WP-182 | belegt | keiner |
| [F-330](#f-330) | P3 | Wiederholter Review-Merge lässt seinen Job im Status failed | WP-183 | belegt | keiner |
| [F-331](#f-331) | P3 | Review-Merge läuft synchron im Request; der Job spiegelt ihn nur | WP-183 | belegt | keiner |
| [F-332](#f-332) | P3 | BFF-Client bffQuery hat keinen Verwender in der UI | WP-184 | belegt | keiner |
| [F-333](#f-333) | P3 | Topology-Komplexität des BFF unterschätzt große Graphen | WP-185 | belegt | keiner |
| [F-334](#f-334) | P3 | Kontakt-Suchtreffer navigieren auf /contacts, eine nicht existente Route | WP-186 | belegt | keiner |
| [F-335](#f-335) | P3 | Jeder Inhaber von saved_view:write kann fremde org-geteilte Sichten umbenennen, entsperren oder löschen | WP-188 | belegt | backend/internal/savedview/audit_t7_savedview_rights_test.go:91 TestA… |
| [F-336](#f-336) | P3 | sort und columns gespeicherter Sichten werden persistiert, aber nicht angewendet; die UI kann sie und die Fre… | WP-188 | belegt | keiner |
| [F-337](#f-337) | P3 | shared_with_roles akzeptiert Rollen-IDs, die in der Organisation nicht existieren | WP-188 | belegt | keiner |
| [F-338](#f-338) | P3 | CSV-Export neutralisiert keine Tabellenkalkulationsformeln (CSV-Injection) | WP-189 | belegt | keiner |
| [F-339](#f-339) | P3 | Standard-Interface-Zähler nur für ifIndex 1 und nur mit RETICORA_METRICS_INTERVAL | WP-190 | belegt | keiner |
| [F-340](#f-340) | P3 | Client-/Site-Optionen des Scope-Umschalters auf 200 begrenzt; eine gültige gespeicherte Auswahl jenseits davo… | WP-191 | Hypothese | keiner |
| [F-341](#f-341) | P3 | Raum-Layout-Koordinaten werden serverseitig nicht validiert | WP-193 | belegt | keiner |
| [F-342](#f-342) | P3 | Tastaturverschiebung im Raumplan patcht das ganze Layout aus veraltetem Zustand | WP-193 | Hypothese | keiner |
| [F-343](#f-343) | P3 | Validierungsmeldungen des DynamicForm sind englische Literale | WP-194 | belegt | keiner |
| [F-344](#f-344) | P3 | Topologie-Knotenliste zeigt die CI-Typ-UUID statt des Typnamens | WP-195 | belegt | keiner |
| [F-345](#f-345) | P3 | Review-Inbox auf 100 Einträge ohne Paging begrenzt; 'Alle offenen auswählen' erfasst nur die geladene Seite | WP-196 | belegt | keiner |
| [F-346](#f-346) | P3 | Merge-Kandidatenauswahl der Review-Inbox listet rohe CI-UUIDs | WP-196 | belegt | keiner |
| [F-347](#f-347) | P3 | Jobcenter bietet 'Ergebnis' für jeden erfolgreichen Job an, gespiegelte Jobs haben aber nie ein Ergebnis (409) | WP-197 | belegt | keiner |
| [F-348](#f-348) | P3 | Benachrichtigungspanel verbirgt Ladefehler als 'keine Benachrichtigungen' | WP-197 | belegt | keiner |
| [F-349](#f-349) | P3 | Override-Autor wird im Provenienz-Tooltip als rohe User-ID gezeigt | WP-198 | belegt | keiner |
| [F-350](#f-350) | P3 | Provenienz-Abschnitt zeigt bei API-Fehler 'Noch keine Provenienz-Daten' | WP-198 | belegt | keiner |
| [F-351](#f-351) | P3 | Kein In-App-Einstieg zu Self-Signup bzw. Onboarding | WP-199 | belegt | keiner |
| [F-352](#f-352) | P3 | INS-04-Fehlerbild 'Localhost-Redirect' wird in der Onboarding-Suite nicht diskriminiert | WP-200 | belegt | keiner |
| [F-353](#f-353) | P3 | Onboarding-Schritt 'Export laden' prüft den synchronen CSV-Endpunkt statt der Datei des Exportjobs | WP-200 | belegt | keiner |
| [F-354](#f-354) | P2 | RLS-Matrix (TestRLSMatrix) überspringt client-, site- und team-gescopte Principals auf den vererbten Tabellen… | WP-065 | belegt | keiner (Testlücke; Rollback-Probe SP/review-t1/probe_doc_shared_delet… |

## Einzelbefunde

<a id="f-001"></a>
### F-001 (P0) – Compliance-Auswertung durch client-gescopten Principal liefert und überschreibt Ergebnisse anderer Clients

- **Betroffene WPs:** WP-020 · **Komponente:** backend/internal/compliance (evaluator.go, handler.go, pg_repository.go) · **Quelle:** Audit (B03) + Verifikation + Defekttest-Welle
- **Reproduktion:** Org A mit Client 1/2, je ein CI, eine aktive Regel, vorherige org-weite Auswertung. Principal mit Scope.Clients=[c1] und compliance:write (Standardrolle engineer, per role_assignment.scope_client_id zuweisbar): POST /api/v1/compliance/evaluations. Kodiert in TestAuditT1ComplianceEvaluationStaysInCallerScope (backend/internal/tenant/rls/audit_t1_cross_client_writes_auditdefect_test.go, Commit 0e02557); Verifier-Probe TestVerifyB03Compliance (audit_db_07).
- **Soll:** Auswertung und Antwort nur für CIs im Scope des Aufrufers (oder 403 für nicht org-weite Principals); Ergebnisse von Client 2 werden weder geliefert noch verändert (TEN-06, TEN-09, WP-020 AK4).
- **Ist:** 200; 'results' enthalten CI-ID, Typ, Status und Details des Client-2-CI; die compliance_result-Zeile von Client 2 wird gelöscht und mit neuer id/evaluated_at neu angelegt ('POST /compliance/evaluations as client 1 answered 200 and lists the client-2 CI'; Probe 'foreignCI-in-body=true', 'rewritten=true'). GET /compliance/results bleibt korrekt gescopt.
- **Nutzerwirkung:** Cross-Client-Offenlegung der Compliance-Lage anderer Kunden und Integritätsverlust ihrer Ergebnishistorie; nach TEN-09 ist jede Cross-Client-Exposition Critical; erreichbar mit der Standardrolle engineer auf Client-Ebene.
- **Ursache (belegt):** compliance/evaluator.go:28-35 ersetzt den Request-Scope durch database.OrgWideScope; compliance/handler.go:129-134 gibt die vollständige EvaluationResponse zurück; ReplaceResults (pg_repository.go:160-172) löscht alle Org-Ergebnisse. TestComplianceScope (compliance_scope_integration_test.go:32-42) kodiert den Cross-Client-Write als erwartet.
- **Empfehlung:** Scope des Aufrufers beibehalten (nur sichtbare CIs auswerten, liefern und ersetzen) oder POST /compliance/evaluations auf org-weite Principals beschränken (sonst 403) und org-weite Arbeit über einen allowgelisteten Systempfad führen; TestComplianceScope umschreiben.
- **Retest (verbindlich):** cd backend && TEST_DATABASE_URL='postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable' go test -tags auditdefect -count=1 -v -run '^TestAuditT1ComplianceEvaluationStaysInCallerScope$' ./internal/tenant/rls/ muss PASS liefern: Antwort ohne Client-2-CI, id/evaluated_at der Client-2-Zeile unverändert (oder 403); org-weite Positivkontrolle weiterhin 200 mit beiden CIs; TestComplianceScope angepasst und grün.
- **Kodierender Test:** backend/internal/tenant/rls/audit_t1_cross_client_writes_auditdefect_test.go TestAuditT1ComplianceEvaluationStaysInCallerScope (FAILS_AS_EXPECTED, SP/verify-tests-run2/defect-TestAuditT1ComplianceEvaluationStaysInCallerScope.log); Probe TestVerifyB03Compliance; kodiert das Fehlverhalten als erwartet: TestComplianceScope.

<a id="f-002"></a>
### F-002 (P0) – Site-Scope-Bypass im Rack-Kanal: site-fremde Racks lesbar und löschbar

- **Betroffene WPs:** WP-014, WP-208, WP-209, WP-213 · **Komponente:** backend/internal/rack/pg_repository.go (rackVisible; ListRacks, GetRack, DeleteRack, requireVisible); GraphQL rackLayout · **Quelle:** Verifikation (B27) + Defekttest-Welle
- **Reproduktion:** Org-Admin legt per POST /api/v1/racks Racks in Räumen von site1 (Client 1) und site2 (Client 2) an. Principal Sites=only(site2), Clients=all: GET /api/v1/racks?limit=200, GET /api/v1/racks/{id}, /racks/{id}/layout, POST /bff/graphql { rackLayout(rackId) }, DELETE /api/v1/racks/{id} für das site1-Rack. Kodiert in TestAuditT1RackChannelSiteScope (backend/internal/tenant/rls/audit_t1_review_rack_scope_auditdefect_test.go); Probe TestVerifyB27RackSiteScope.
- **Soll:** Racks außerhalb des Site-Scopes erscheinen nicht in Listen und liefern auf Detail, Layout, GraphQL und DELETE 404; die Zeile bleibt (TEN-05, TEN-09, CH25) – wie bei Rooms, Buildings und beim Client-Scope.
- **Ist:** 'GET /racks as site2: rack t1-rack-one listed true, want false'; 'GraphQL rackLayout(t1-rack-one) as site2: rack disclosed true'; 'DELETE rack t1-rack-one as site2: 204, want 404'; 'rack/location rows 0/1, want 1/1' (location-Eintrag verwaist). Client-2-Principal erhält korrekt 404.
- **Nutzerwirkung:** Ein auf eine Site beschränkter Nutzer (role_assignment.scope_site_id) sieht die Rack-Infrastruktur aller Sites seiner Organisation, auch die anderer Clients, und kann fremde leere Racks löschen (Datenverlust, verwaister location-Eintrag). Ein client- und site-gescopter Principal (clients=[c1], sites=[s1]) erfüllt rackVisible für Racks von Client 2 (Probe). Nach TEN-09 ist das eine Cross-Client-Exposition, daher P0.
- **Ursache (belegt):** rack/pg_repository.go:58-60 rackVisible = EXISTS(room LEFT JOIN building ... AND (building.site_id IS NULL OR EXISTS(site ...))): building_isolation filtert per app.site_scope, die fremde building-Zeile wird unsichtbar, der LEFT JOIN liefert NULL und die NULL-Klausel macht das Rack sichtbar; rack selbst hat nur eine Org-Policy (kein site_id, vgl. L-T1-11).
- **Empfehlung:** INNER JOIN auf building (bzw. 'room.building_id IS NULL OR EXISTS(building ... site ...)'), DELETE inkl. location-Sync korrigieren; gleiches Muster in desk/pg_repository.go:62 prüfen; mittelfristig DB-seitige site_id für rack/room (L-T1-11).
- **Retest (verbindlich):** cd backend && TEST_DATABASE_URL='postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable' go test -tags auditdefect -count=1 -v -run '^TestAuditT1RackChannelSiteScope$' ./internal/tenant/rls/ muss PASS liefern (alle 5 Principals: Liste/Detail/Layout/Mounts/GraphQL nach Scope, verweigertes DELETE 404 mit rack/location 1/1); TestAuditT1LocationChannelBelowSites bleibt grün.
- **Kodierender Test:** TestAuditT1RackChannelSiteScope (FAILS_AS_EXPECTED, SP/verify-tests-run2/defect-TestAuditT1RackChannelSiteScope.log); Probe TestVerifyB27RackSiteScope (SP/work/verify-B27/rack-site-scope.log).
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – prioritaet: „P1“ → „P0“. Sicherheitsrisiko mit Datenverlust in einem in G1 aktiven Modul (Racks sind in modules.go nicht gegatet). Die Begründung 'Org-Grenze bleibt gewahrt' trägt nicht, weil auch L-T1-01 (P0) org-intern ist. Maßstab nach TEN-09 ist die Client-Grenze, und die wird verletzt: (1) Im Defekttest löscht ein Principal, der nur für site2 (Client 2) zugewiesen ist, das Rack t1-rack-one in site1 von Client 1 (DELETE 204, rack/location 0/1). (2) Meine rollback-Probe auf audit_db_08 zeigt: ein Principal mit clients=[c1] und sites=[s1] erfüllt rackVisible für das Rack von Client 2/site2 ('rack of client2/site2 passes rackVisible|1'; Kontrolle clients=[c1], sites=all: 0). ListRacks, GetRack, Update und Delete nutzen nur rackVisible (rack/pg_repository.go:89, :121, :179, :197). Probe: /tmp/claude-0/-home-user-Reticora-CMDB/fbd8a132-b968-552a-84eb-515fd3e8f807/scratchpad/review-t1/probe_rack_cross_client.sql und .out. TEN-09: 'Jede Cross-Org- oder Cross-Client-Exposition ist Critical'.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – nutzerwirkung: „Ein auf eine Site beschränkter Nutzer (role_assignment.scope_site_id) sieht die Rack-Infrastruktur aller Sites seiner Organisation und kann fremde leere Racks löschen (Datenverlust, verwaister location-Eintrag). Die Org-Grenze bleibt gewahrt, daher P1 statt P0.“ → „Ein auf eine Site beschränkter Nutzer (role_assignment.scope_site_id) sieht die Rack-Infrastruktur aller Sites seiner Organisation, auch die anderer Clients, und kann fremde leere Racks löschen (Datenverlust, verwaister location-Eintrag). Ein client- und site-gescopter Principal (clients=[c1], sites=[s1]) erfüllt rackVisible für Racks von Client 2 (Probe). Nach TEN-09 ist das eine Cross-Client-Exposition, daher P0.“. Die Herleitung 'Org-Grenze gewahrt, daher P1' widerspricht der Einstufung von L-T1-01 und ist durch die Probe widerlegt.

<a id="f-003"></a>
### F-003 (P0) – Review-Items ohne Kandidaten-CIs leaken über Clients; review_item ohne DB-seitigen Client/Site-Scope

- **Betroffene WPs:** WP-013, WP-025, WP-208 · **Komponente:** backend/internal/reviews/pg_repository.go (visible); Migration 000061 (review_item ausgelassen); Erzeuger discovery.go:1174-1190, :1236-1253, unlicensed.go:223-236, scope_handler.go:212-222 · **Quelle:** Audit (B04) + Verifikation + Defekttest-Welle
- **Reproduktion:** Org mit Client 1/2; Collector von Client 2 erzeugt review_item (unclassified_device, candidate_ci_ids '{}', Payload name/management_ip/serial). Client-1-Principal: GET /api/v1/review-items, GET /review-items/{id}, POST /review-items/{id}/resolve {action: dismiss}, GraphQL reviewInbox; Direkt-SELECT auf review_item in WithTenant unter Client-1-Scope. Kodiert in TestAuditT1ReviewItemsWithoutCandidatesStayInClientScope (audit_t1_review_rack_scope_auditdefect_test.go) und TestAuditT1ReviewItemRowsCarryClientScope (audit_t1_team_scope_auditdefect_test.go).
- **Soll:** Ein client-gescopter Principal sieht und löst keine Review-Items zu Geräten, Collectoren oder Subnetzen anderer Clients; review_item erbt den Scope seiner Kandidaten bzw. seines discovery_result/Collectors (TEN-02, TEN-05, TEN-06; WP-025 AK1).
- **Ist:** 'GET /review-items as client 1 discloses t1rev-c2-firewall of client 2'; 'POST /review-items/{unclassified_device of client 2}/resolve as client 1: 200, want 404; status dismissed, want open'; 'a client-1 principal selects 1 review_item rows about the client-2 CI, want 0'.
- **Nutzerwirkung:** Kundenübergreifende Offenlegung von Discovery-Details (Gerätename, Management-IP, Seriennummer, raw_data, Collector/CIDR) innerhalb einer MSP-Organisation für Rollen mit discovery:read (viewer, engineer); fremde Items können zudem verworfen werden (Integritätsverlust).
- **Ursache (belegt):** review_item hat keine client_id/site_id (000061 ausgelassen; Policy review_item_isolation nur Org); reviews/pg_repository.go:29-32 'visible' (alle Kandidaten sichtbar) ist für leere candidate_ci_ids vakuum-wahr (fail-open). TestChannelMatrix seedet nur ein Review mit Kandidat.
- **Empfehlung:** Abgeleitete client_id/site_id an review_item (aus Kandidaten-CIs bzw. Collector/Discovery-Job wie derive_discovery_result_scope), Client-/Site-Policies mit E-09-Semantik; bis dahin Go-Prädikat für Items ohne Kandidaten bei gescopten Principals fail-closed; TestChannelMatrix um No-Candidate-Review erweitern.
- **Retest (verbindlich):** cd backend && TEST_DATABASE_URL='postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable' go test -tags auditdefect -count=1 -v -run '^(TestAuditT1ReviewItemsWithoutCandidatesStayInClientScope|TestAuditT1ReviewItemRowsCarryClientScope)$' ./internal/tenant/rls/ muss PASS liefern; probe_review_no_candidate.sql liefert 0; TestAuditT1OrgWideChannelWrites bleibt grün.
- **Kodierender Test:** TestAuditT1ReviewItemsWithoutCandidatesStayInClientScope, TestAuditT1ReviewItemRowsCarryClientScope (beide FAILS_AS_EXPECTED); Probes probe_review_no_candidate.sql, TestB04VerifierProbe P3.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – prioritaet: „P1“ → „P0“. Bestätigte Cross-Client-Offenlegung (Gerätename, Management-IP, Seriennummer, raw_data, Collector, CIDR) und Integritätsverlust (fremde Items verworfen). Erreichbar für Leserollen mit discovery:read (viewer) über die normale Review-Inbox und GraphQL reviewInbox, in einem in G1 aktiven Kernmodul (Discovery/Reviews nicht in LateModules). TEN-09 nennt Review-Items ausdrücklich und stuft jede Cross-Client-Exposition als Critical ein. Die Synthese stuft L-T1-01 mit genau dieser Begründung als P0 ein, obwohl L-T1-01 in einem in G1 gegateten Modul (compliance, P3) liegt. L-T1-03 ist mindestens gleich schwer.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – wps: „["WP-025"]“ → „["WP-025","WP-013","WP-208"]“. WP-013: Das fail-open-Prädikat stammt aus Commit 34b84ba (reviewVisible in discovery/pg_repository.go), die Queue liegt im eingebetteten Discovery-Repository, und die Erzeuger der kandidatenlosen Items sind Discovery-Code. WP-208: Review ist ein in AK1 und TEN-09 genannter Kanal, und der Leak läuft über REST und GraphQL.

<a id="f-004"></a>
### F-004 (P0) – Geteilte Dokumente und Wartungsfenster: teilgescopter Principal ändert/löscht sie, FK-Kaskade entfernt Links anderer Clients

- **Betroffene WPs:** WP-019, WP-020, WP-028 · **Komponente:** Migrationen 000064:275-287 und 000079 (document/maintenance_window-Policies); backend/internal/document/pg_repository.go:80-81 documentVisible; backend/internal/maintenance/pg_repository.go:41-43 windowVisible (UPDATE :181, DELETE :200); FK ON DELETE CASCADE document_link/maintenance_window_ci · **Quelle:** Audit (B03/B04) + Verifikation + Defekttest-Welle
- **Reproduktion:** Org-Admin legt ein Dokument an und verlinkt es mit einem CI von Client 1 und einem Asset von Client 2; ebenso ein Wartungsfenster mit CIs beider Clients. Client-1-Principal: PATCH /api/v1/documents/{id} {title}, DELETE /api/v1/documents/{id}; PATCH /api/v1/maintenance-windows/{id} {title, status: cancelled}, DELETE /api/v1/maintenance-windows/{id}. Kodiert in TestAuditT1DocumentWritesOutsideScope (Subtest 'shared document of client 1 and client 2') und TestAuditT1MaintenanceWindowWritesOutsideScope (audit_t1_cross_client_writes_auditdefect_test.go).
- **Soll:** Schreibzugriffe auf Mehrobjekt-Zeilen, die Objekte außerhalb des Scopes betreffen, werden abgewiesen (403/404), oder der Principal darf nur eigene Links lösen; Zeilen von Client 2 bleiben unverändert (WP-028 AK2; WP-019/WP-020 AK4; TEN-09).
- **Ist:** Dokument: 'PATCH as client 1: 200', 'title after PATCH by client 1: t1-renamed-by-c1', 'DELETE as client 1: 204', 'client-2 asset links after DELETE by client 1: 0, want 1'. Fenster: 'PATCH as client 1: 200', 'window after PATCH by client 1: t1-changed-by-c1|cancelled', 'DELETE as client 1: 204', 'client-2 link rows after DELETE by client 1: 0, want 1 (FK cascade removed another client's data)'.
- **Nutzerwirkung:** Ein Techniker von Kunde A löscht oder benennt Dokumente und Wartungsplanungen um, die Kunde B betreffen; B verliert seine Verweise still (Cross-Client-Datenverlust). G1-Exposition durch die Modul-Gates (WP-214) begrenzt; E-03 verlangt die Korrektur dennoch.
- **Ursache (belegt):** USING lässt die Zeile zu, sobald ein Link sichtbar ist; die restriktiven *_scoped_update/delete-Policies (000079) prüfen nur die Org; die FK-Kaskade löscht die Links anderer Clients; die Go-Prädikate documentVisible/windowVisible haben dieselbe Semantik und werden für UPDATE/DELETE wiederverwendet.
- **Empfehlung:** Restriktive UPDATE/DELETE-Policies, die alle Links sichtbar verlangen (link_count = Anzahl unter RLS sichtbarer Links; analog ci_count), alternativ für teilgescopte Principals nur das Lösen eigener Links erlauben; Go-Prädikate angleichen; Negativtests in module_object_integration_test.go.
- **Retest (verbindlich):** cd backend && TEST_DATABASE_URL='postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable' go test -tags auditdefect -count=1 -v -run '^(TestAuditT1DocumentWritesOutsideScope|TestAuditT1MaintenanceWindowWritesOutsideScope)$' ./internal/tenant/rls/ muss PASS liefern (Fremdzeilen 403/404, Client-2-Links intakt; Positivkontrollen eigenes Dokument/Fenster 200/204); Probes TestB04VerifierProbe P4, probe_doc_shared_delete.sql, probe_window_shared_delete.sql ohne Fremdlöschung.
- **Kodierender Test:** TestAuditT1DocumentWritesOutsideScope, TestAuditT1MaintenanceWindowWritesOutsideScope (FAILS_AS_EXPECTED); Probes TestB04VerifierProbe, TestVerifyB03maintenance, probe_doc_shared_delete.sql, probe_window_shared_delete.sql.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – prioritaet: „P1“ → „P0“. Bestätigter Cross-Client-Schreib- und Löschpfad mit Datenverlust eines anderen Clients: Client 1 benennt geteilte Dokumente und Wartungsfenster um, storniert und löscht sie, und die FK-Kaskade entfernt die Links von Client 2. Das ist per HTTP-Defekttest belegt und von mir DB-seitig als App-Rolle wiederholt ('UPDATE 1', 'DELETE 1', 'client2 asset links 0'). Nach Definition sind Sicherheitsrisiko und Datenverlust P0. Das Gate-Argument (WP-214) gilt gleichermaßen für L-T1-01 (compliance ist ebenfalls gegatet, P3) und wurde dort nicht mindernd gewertet. Konsistent ist daher P0.

<a id="f-005"></a>
### F-005 (P0) – MFA für org_admins aus Signup, Einladung, Operator-Pfad und Gruppe reticora-admin nicht erzwungen

- **Betroffene WPs:** WP-044 · **Komponente:** backend/internal/signup/keycloak.go:89-123; deploy/keycloak/realm-reticora.json; identity/oidc.go:54-57 · **Quelle:** Audit, Verifikation, Defekttest-Welle
- **Reproduktion:** Real-Stack onboarding Test 5 (Login nur Passwort); Live-Keycloak V/kc-signup-admin-mfa.txt (nur 'password', kein mfa_required); TestAuditT2FirstOrgAdminAccountRequiresMFA, TestAuditT2RealmAdminGroupRequiresMFA FAILS_AS_EXPECTED.
- **Soll:** AUT-09/E-14: MFA-Pflicht für Admin-Rollen.
- **Ist:** Nur Installer-Admin erhält OTP-Pflicht.
- **Nutzerwirkung:** Höchstprivilegierte Tenant-Konten nur passwortgeschützt.
- **Ursache (belegt):** MFA an KC-Rolle mfa_required gebunden; App-org_admin per DB/Gruppe ohne Sync; Backend prüft AMR nur für Operatoren.
- **Empfehlung:** mfa_required bei jeder org_admin-Vergabe zuweisen, Gruppe mit Realm-Rolle, ggf. Backend-Step-up.
- **Retest (verbindlich):** cd backend && go test -tags auditdefect -count=1 -v -run 'TestAuditT2FirstOrgAdminAccountRequiresMFA|TestAuditT2RealmAdminGroupRequiresMFA' ./internal/signup/ PASS; e2e-real: Signup-/Einladungs-Admin muss CONFIGURE_TOTP durchlaufen PASS.
- **Kodierender Test:** backend/internal/signup/t2_mfa_auditdefect_test.go (2 Tests, FAILS_AS_EXPECTED)
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – prioritaet: „P1“ → „P0“. Sicherheitsrisiko: AUT-09 [B] verlangt 'MFA für org_admin Pflicht'. Real belegt ist (onboarding Test 5; Live-Keycloak V/kc-signup-admin-mfa.txt: nur 'password'; TestAuditT2FirstOrgAdminAccountRequiresMFA/RealmAdminGroupRequiresMFA FAILS_AS_EXPECTED), dass das höchstprivilegierte Tenant-Konto jedes Signup-Mandanten standardmäßig nur per Passwort geschützt ist. Es fehlt also eine verbindliche Sicherheitskontrolle für alle Mandanten, kein bloßer Funktionsmangel.

<a id="f-006"></a>
### F-006 (P0) – API-Key-Rotation/-Widerruf durch Nicht-Owner: Rechteausweitung und Übernahme fremder Keys

- **Betroffene WPs:** WP-067 · **Komponente:** backend/internal/identity/apikey_handler.go:184-229; apikey_store.go:208-245 · **Quelle:** Audit, Verifikation, Defekttest-Welle
- **Reproduktion:** probe-v09.log: client-gebundener {ci:read, apikey:manage}-User rotiert Admin-Key → 201, Nachfolger mit user:manage/role:manage org-weit; Widerruf fremder Key 204; TestAuditT2APIKeyRotateRevokeOnlyByEntitledCaller FAILS_AS_EXPECTED.
- **Soll:** WP-067 Kriterium 2/AUT-04: nur Owner bzw. Rechte-Halter.
- **Ist:** Jeder apikey:manage-Halter rotiert/widerruft jeden Org-Key.
- **Nutzerwirkung:** Mandantenübernahme über Custom-Rolle möglich.
- **Ursache (belegt):** Keine Owner-/Rechteprüfung.
- **Empfehlung:** Nur Owner oder Abdeckung aller Key-Rechte im Owner-Scope.
- **Retest (verbindlich):** TEST_DATABASE_URL=… go test -tags auditdefect -count=1 -v -run '^TestAuditT2APIKeyRotateRevokeOnlyByEntitledCaller$' ./internal/identity/ PASS.
- **Kodierender Test:** backend/internal/identity/t2_apikey_authz_auditdefect_test.go (FAILS_AS_EXPECTED)
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – prioritaet: „P1“ → „P0“. Privilegieneskalation bis zur Mandantenübernahme: Ein client-gebundener Inhaber von {ci:read, apikey:manage} rotiert den org-weiten Admin-Key und erhält einen Nachfolger mit user:manage/role:manage (t2_apikey_authz_auditdefect_test.go:171, probe-v09.log V1b/V1c). Das ist ein Sicherheitsrisiko und zugleich eine Cross-Client-Exposition (TEN-09: 'Critical'). Die gleichartige fehlende Grant-Obergrenze L-T6-epic-b-02 ist als P0 eingestuft.

<a id="f-007"></a>
### F-007 (P0) – API-Key-Bindung an Service-Account ohne Scope-Prüfung: client-gebundener Ersteller erhält org-weiten Key

- **Betroffene WPs:** WP-067, WP-069 · **Komponente:** backend/internal/identity/apikey_handler.go:145-157; apikey.go:218-227 · **Quelle:** Verifikation, Defekttest-Welle
- **Reproduktion:** probe-v09.log V2a/V2c (201, org-wide=true); TestAuditT2KeyBindingNeverBroadensCreatorScope FAILS_AS_EXPECTED.
- **Soll:** RBA-08: gebundener Key nie breiter als Ersteller.
- **Ist:** service_account_id ungeprüft.
- **Nutzerwirkung:** Client-Trennung für MSP-Techniker aushebelbar.
- **Ursache (belegt):** Fehlende Bindungsautorisierung.
- **Empfehlung:** Bindung nur bei Scope-Abdeckung, sonst 403.
- **Retest (verbindlich):** go test -tags auditdefect -count=1 -v -run '^TestAuditT2KeyBindingNeverBroadensCreatorScope$' ./internal/identity/ PASS.
- **Kodierender Test:** backend/internal/identity/t2_apikey_authz_auditdefect_test.go (FAILS_AS_EXPECTED)
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – prioritaet: „P1“ → „P0“. Ein client-1-gebundener Ersteller erhält über die Bindung an einen org-weiten Service-Account einen org-weiten Key (t2_apikey_authz_auditdefect_test.go:250 'org-wide=true'). Das ist eine Cross-Client-Exposition; TEN-09: 'Jede Cross-Org- oder Cross-Client-Exposition ist Critical'. Analog sind L-T1-01 und L-T6-epic-b-01 als P0 eingestuft.

<a id="f-008"></a>
### F-008 (P0) – Service-Account-Rollenzuweisung ohne Rechte-/Scope-Prüfung; Route verlangt nur user:manage

- **Betroffene WPs:** WP-069 · **Komponente:** backend/internal/user/service_account.go:318-346; route_permissions.txt:503 · **Quelle:** Verifikation, Defekttest-Welle
- **Reproduktion:** probe-v09.log V4b/V4c (200, SA org-weit mit role:manage); TestAuditT2ServiceAccountRolesWithinCallerRights, TestAuditT2ServiceAccountRoleAssignmentNeedsRoleManage FAILS_AS_EXPECTED.
- **Soll:** RBA-08: wie Benutzer (role:manage, nie mehr als eigene Rechte).
- **Ist:** user:manage genügt, keine Rechteprüfung.
- **Nutzerwirkung:** user:manage-Halter erzeugen Admin-SAs.
- **Ursache (belegt):** Fehlende Prüfung, falsches Route-Recht.
- **Empfehlung:** role:manage + Grant-Ceiling für alle Grant-Pfade.
- **Retest (verbindlich):** go test -tags auditdefect -count=1 -v -run '^TestAuditT2ServiceAccountRolesWithinCallerRights$' ./internal/identity/ und -run '^TestAuditT2ServiceAccountRoleAssignmentNeedsRoleManage$' ./internal/server/ PASS.
- **Kodierender Test:** identity/t2_apikey_authz_auditdefect_test.go, server/t2_auth_chain_auditdefect_test.go (FAILS_AS_EXPECTED)
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – prioritaet: „P1“ → „P0“. Privilegieneskalation: Ein Inhaber von user:manage ohne role:manage weist einem Service-Account beliebige Rollen zu, bis zu org-weitem role:manage (identity :323, server :137, probe-v09.log V4b/V4c). Sicherheitsrisiko nach Definition; die gleichartige Grant-Obergrenze L-T6-epic-b-02 ist P0.

<a id="f-009"></a>
### F-009 (P0) – Webhook-Filter liefert merge-/relationship-Events anderer Clients an client-gebundene Service-Accounts

- **Betroffene WPs:** WP-069 · **Komponente:** backend/internal/webhook/dispatcher.go:105-150; user/service_account.go:89-92 · **Quelle:** Audit, Verifikation, Defekttest-Welle
- **Reproduktion:** Realer ci.merged-Payload zweier Client-2-CIs → Client-1-SA erhält 1 Zustellung (probe-v09.log V3); TestAuditT2BoundSubscriptionGetsOnlyReadableClientEvents FAILS_AS_EXPECTED.
- **Soll:** WP-069 Kriterium 2: nur lesbare Objekte.
- **Ist:** Scope-lose Payloads gelten als org-weit.
- **Nutzerwirkung:** Client-Trennung für Webhook-Konsumenten gebrochen.
- **Ursache (belegt):** Scope nur aus Top-Level-Feldern; ci.merged/relationship.removed ohne client_id.
- **Empfehlung:** Scope serverseitig auflösen oder fail-closed.
- **Retest (verbindlich):** go test -tags auditdefect -count=1 -v -run '^TestAuditT2BoundSubscriptionGetsOnlyReadableClientEvents$' ./internal/webhook/ PASS.
- **Kodierender Test:** backend/internal/webhook/t2_bound_filter_auditdefect_test.go (FAILS_AS_EXPECTED)
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – prioritaet: „P2“ → „P0“. Cross-Client-Exposition: Ereignisse ci.merged und relationship.removed über Client-2-CIs werden an die Subscription eines Client-1-Service-Accounts zugestellt (t2_bound_filter_auditdefect_test.go:130; probe-v09.log V3). TEN-09 stuft jede Cross-Client-Exposition als 'Critical' ein. T1 (L-T1-01) und T6 (L-T6-epic-b-01) stufen solche Fälle als P0 ein. P2 ('funktionale/Validierungs-Einschränkung') passt nicht zur Definition.

<a id="f-010"></a>
### F-010 (P0) – WMI-Plugin sendet Credentials im Klartext (HTTP Basic über http://ziel:5985; HTTPS-Modus mit InsecureSkipVerify)

- **Betroffene WPs:** WP-048, WP-152 · **Komponente:** collector/plugins/wmi/wmi.go · **Quelle:** Audit/Verifikation WP-152; Defekttest-Welle; zusammengeführt mit L-T2-13 (Audit, Verifikation, Defekttest-Welle)
- **Reproduktion:** cd /home/user/Reticora-CMDB/collector && go test -tags auditdefect -count=1 -v -run '^TestAuditWMICredentialTransport$' ./plugins/wmi/
- **Soll:** Gerätezugangsdaten gehen nie unverschlüsselt oder an ungeprüfte Endpunkte (Schutzziel COL-02/DIS-04).
- **Ist:** wmi_auditdefect_test.go:131: Der Endpunkt 5985 (tls=false) erhält das Passwort lesbar in 5 Requests; 5986 akzeptiert ein selbstsigniertes Zertifikat.
- **Nutzerwirkung:** Jeder Host, der in einem gescannten Bereich auf 5985 antwortet, sowie Mitschneider im Netz erhalten privilegierte Zugangsdaten.
- **Ursache (belegt):** wmi.go:42 UseHTTPS=false, :47 InsecureSkipVerify=true, :155-162 http-URL, :209 SetBasicAuth; kein Konfigurationspfad aktiviert TLS-Prüfung.
- **Empfehlung:** HTTPS 5986 mit Zertifikatsprüfung (Scope-CA/Pin wie Redfish), Negotiate/Kerberos mit Nachrichtenverschlüsselung; Plain-HTTP nur per expliziter Freigabe je Scope.
- **Retest (verbindlich):** cd /home/user/Reticora-CMDB/collector && go test -tags auditdefect -count=1 -run '^TestAuditWMICredentialTransport$' ./plugins/wmi/ → PASS; go test ./plugins/wmi/ ohne Tag bleibt PASS.
- **Kodierender Test:** collector/plugins/wmi/wmi_auditdefect_test.go:97 TestAuditWMICredentialTransport (FAILS_AS_EXPECTED); collector/plugins/t2_credentials_auditdefect_test.go TestAuditT2CredentialsArePerProtocol (FAILS_AS_EXPECTED)
- **Prüfvermerk:** Gruppenübergreifend dedupliziert: L-T2-13 („WMI-Plugin erhält das SSH-Konto (Alias wmi→ssh) und sendet es per HTTP Basic im Klartext“) ist dieselbe Ursache und hier zusammengeführt.

<a id="f-011"></a>
### F-011 (P0) – NAS-Plugin sendet Synology-Konto und Passwort in der URL über http://ziel:5000 und an ungeprüftes HTTPS (5001)

- **Betroffene WPs:** WP-152 · **Komponente:** collector/plugins/nas/nas.go · **Quelle:** Defekttest-Welle (neuer Defekt; im Audit nur als Robustheitsbefund und Lücke)
- **Reproduktion:** cd /home/user/Reticora-CMDB/collector && go test -tags auditdefect -count=1 -v -run '^TestAuditNASCredentialTransport$' ./plugins/nas/
- **Soll:** Zugangsdaten nie im Klartext, nie in der URL, nur an geprüftes TLS.
- **Ist:** nas_auditdefect_test.go:103: 'GET /webapi/auth.cgi?…&account=audit-nas-admin&passwd=S3cret-audit-nas' auf 5000 (tls=false); 5001 akzeptiert ein ungeprüftes Zertifikat.
- **Nutzerwirkung:** Die NAS-Admin-Zugangsdaten werden mitlesbar übertragen und landen in Access-Logs von Geräten und Proxys.
- **Ursache (belegt):** nas.go:47-60 (http/InsecureSkipVerify), :214-217 und :257 (Passwort im Query-String).
- **Empfehlung:** Login per POST-Body nur über TLS mit Zertifikatsprüfung (Scope-CA/Pin); Plain-HTTP nur per expliziter Freigabe. Hinweis: Die Testwelle ordnete den Defekt auch WP-147 zu. Die Kriterien von WP-147 (Bundle-Zustellung, Klartext nur im Collector-Speicher) betrifft er nicht unmittelbar, deshalb wird er wie L-T4-01 unter WP-152 geführt.
- **Retest (verbindlich):** cd /home/user/Reticora-CMDB/collector && go test -tags auditdefect -count=1 -run '^TestAuditNASCredentialTransport$' ./plugins/nas/ → PASS; TestCollectSynologyEndToEnd bleibt PASS.
- **Kodierender Test:** collector/plugins/nas/nas_auditdefect_test.go:60 TestAuditNASCredentialTransport (FAILS_AS_EXPECTED)

<a id="f-012"></a>
### F-012 (P0) – Erneuerungsendpunkt ist nicht an den Collector gebunden: ein Principal mit discovery:ingest stellt sich ein Collector-Zertifikat für den eigenen Schlüssel aus

- **Betroffene WPs:** WP-145, WP-147 · **Komponente:** backend/internal/discovery/ca.go:442-475, pg_repository.go:708-737; backend/internal/server/authz.go:262 · **Quelle:** Audit/Verifikation WP-145; Defekttest-Welle
- **Reproduktion:** cd /home/user/Reticora-CMDB/backend && TEST_DATABASE_URL=… go test -tags auditdefect -count=1 -p 1 -v -run '^TestAuditCertificateRenewalBoundToCollector$' ./internal/discovery/
- **Soll:** Nur der Collector selbst (Nachweis per aktuellem Zertifikat oder mTLS) kann erneuern. Bundles gehen nur an den Schlüssel des Collectors (COL-02/COL-04).
- **Ist:** collector_pki_auditdefect_test.go:150: HTTP 201 mit CN=<id> und dem Schlüssel des Aufrufers, für einen nie enrollten und für einen enrollten Collector. Das legitime Zertifikat bleibt gültig. Verifier-Probe: BundleGate akzeptiert das Zertifikat, das Bundle lässt sich mit dem Angreiferschlüssel öffnen.
- **Nutzerwirkung:** Privilegieneskalation: Ein auf ingest beschränkter API-Key oder eine Custom-Rolle kann sich als Collector ausgeben und die SSH-/SNMP-/Redfish-Credentials der Scopes entschlüsseln.
- **Ursache (belegt):** RenewCertificate prüft weder den Besitz des aktuellen Schlüssels noch die Bindung Aufrufer↔Collector; die Route verlangt nur discovery:ingest.
- **Empfehlung:** HTTP-Erneuerung entfernen oder die mTLS-Identität des Collectors bzw. eine Signatur mit dem aktuellen Schlüssel verlangen. Für Collector ohne gültiges Zertifikat nichts ausstellen. Vorgänger nach einer Karenzzeit sperren und jede Ausstellung auditieren.
- **Retest (verbindlich):** TestAuditCertificateRenewalBoundToCollector besteht. API-Test: API-Key mit [discovery:ingest] → 403.
- **Kodierender Test:** backend/internal/discovery/collector_pki_auditdefect_test.go:109 TestAuditCertificateRenewalBoundToCollector (FAILS_AS_EXPECTED)
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – prioritaet: „P1“ → „P0“. Die Rechteausweitung ist belegt: Defekttest collector_pki_auditdefect_test.go:150 mit HTTP 201 und CN=<fremde id>. Die Verifier-Probe öffnet das versiegelte Bundle mit dem Angreiferschlüssel (probe-b18-all.log). Die Route verlangt nur discovery:ingest (authz.go:262). API-Keys lassen sich auf genau [discovery:ingest] beschränken (identity/apikey_handler.go:99-155), und das ist der vorgesehene Least-Privilege-Fall für Integrationen. Ein solcher Key erhält im Klartext die SSH-/SNMP-/Redfish-Geheimnisse der Scopes und dazu eine dauerhafte Collector-Identität am öffentlichen Edge 4222, denn das legitime Zertifikat wird nicht gesperrt. Das bricht die Sicherheitszusage von COL-02. Der Verifier hielt P1, weil die Standardrollen engineer und org_admin bereits credential:decrypt haben (role_matrix.go:21-41). Das mindert das Risiko für eingeschränkte Keys und Custom-Rollen aber nicht. Nach Definition gilt: Sicherheitsrisiko ⇒ P0. Die Einstufung ist konsistent mit L-T6-epic-b-02 (P0, Rechteausweitung durch einen In-Tenant-Principal).

<a id="f-013"></a>
### F-013 (P0) – Collector ignoriert credential_ids je Scope: das erste Credential je Protokoll gilt für alle Scopes (auch über Client-Grenzen)

- **Betroffene WPs:** WP-147, WP-153 · **Komponente:** collector/collectorcmd/bundle.go:105-130, collectorcmd.go:449, scopes.go:38 · **Quelle:** Audit/Verifikation WP-153 (P1), WP-147 (P2); Defekttest-Welle
- **Reproduktion:** cd /home/user/Reticora-CMDB/collector && go test -tags auditdefect -count=1 -v -run '^TestAuditPerScopeCredentials$' ./collectorcmd/
- **Soll:** DIS-09: Die Ziele eines Scopes werden nur mit dessen Credentials abgefragt.
- **Ist:** discovery_auditdefect_test.go:326: Ziel 198.51.100.1 (Scope von Client 2) wird mit 'c1-secret' abgefragt statt mit 'c2-secret'.
- **Nutzerwirkung:** Die Discovery bei Client 2 scheitert, und die Geheimnisse von Client 1 werden dem Netz von Client 2 offengelegt.
- **Ursache (belegt):** Das Bundle wird auf protokollbezogene Schlüssel abgeflacht (erstes gewinnt); runDueDiscovery übergibt allen Pässen dieselbe Menge; CredentialIDs wird nie gelesen.
- **Empfehlung:** Bundle nach Credential-ID schlüsseln und die Credentials je Pass aus scope.CredentialIDs auflösen.
- **Retest (verbindlich):** TestAuditPerScopeCredentials besteht (10.2.0.1 erhält nur c2-secret).
- **Kodierender Test:** collector/collectorcmd/discovery_auditdefect_test.go:305 TestAuditPerScopeCredentials (FAILS_AS_EXPECTED)
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – prioritaet: „P1“ → „P0“. Die Preisgabe über Client-Grenzen ist belegt: TestAuditPerScopeCredentials (discovery_auditdefect_test.go:326) zeigt, dass das Ziel des Scopes von Client 2 mit 'c1-secret' abgefragt wird. Die Konfiguration ist unterstützt. Der Trigger discovery_scope_binding (migrations/000129_discovery_scope.up.sql:75-77) lehnt einen abweichenden Client nur ab, wenn der Collector selbst einen Client hat. Ein org-weiter Collector (client_id NULL) darf also Scopes mehrerer Clients tragen, wie im MSP-Betrieb. Die Geheimnisse eines Clients gehen dann bei jedem Lauf ohne Angreifer an Geräte und Netz eines anderen Mandanten, die SNMPv2c-Community sogar im Klartext. Das ist ein Sicherheitsrisiko, keine reine Funktionsstörung, also P0. Die Einstufung ist konsistent mit L-T6-epic-b-01 (P0, Credentials fremder Clients lesbar).

<a id="f-014"></a>
### F-014 (P0) – Site-Grant setzt Client-Scope auf NULL: Site-Nutzer lesen Kontakte, Credentials, Assets und unplatzierte CIs fremder Clients

- **Betroffene WPs:** WP-109 · **Komponente:** backend/internal/identity, permission, middleware (Scope-Auflösung), RLS · **Quelle:** Verifikation (B14) + Defekttest-Welle
- **Reproduktion:** role_assignment(viewer, scope_site_id=site1 von client1) → AccessGrants → ResolveAccess → TenantScopeFor; SELECT contact/asset/ci von client2 unter RLS (Probe TestV14ProbeSiteScopeChain, SP/work/verify-B14/probe-sitescope-chain.log). Produktiv erreichbar über POST /api/v1/invitations {scope_type:site}.
- **Soll:** Site-1-Principal sieht keine Client-2-Daten; nur org-weite Rollen setzen eine Scope-Dimension auf NULL (RBA-03).
- **Ist:** clients={All:true}; contact client2, asset client2@site2 und unplatziertes CI client2 sichtbar.
- **Nutzerwirkung:** In MSP-Orgs liest ein auf einen Kundenstandort beschränkter Techniker PII-Kontakte, Credential-Metadaten und Assets aller anderen Kunden.
- **Ursache (belegt):** identity/principal.go:213-232 unionDimension liefert All für unbeschränkte Dimension; permission/pg_repository.go:205-215 setzt nur Sites; middleware/auth.go:259-266 mappt All auf NULL-Client-Scope.
- **Empfehlung:** Site-Grant auf site.client_id einschränken bzw. unbeschränkte Dimensionen gescopter Grants als leer behandeln; TestHierarchyScopeOnPostgreSQL korrigieren.
- **Retest (verbindlich):** WT (/home/user/Reticora-CMDB/.claude/worktrees/wf_8bd1a626-4f0-2): cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_11?sslmode=disable go test -tags auditdefect -count=1 -run 'TestAuditDefectSiteGrant' ./internal/locations/ → PASS; plus e2e-real Site-Session ohne Client-B-Zeilen.
- **Kodierender Test:** backend/internal/locations/sitescope_auditdefect_test.go TestAuditDefectSiteGrantConfinedToClientAndSite (fails-as-expected, :136,:167)
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – prioritaet: „P1“ → „P0“. Es handelt sich um ein Sicherheitsrisiko, also P0 nach Definition. TEN-09 (docs/spec/katalog-v3/02-mandanten-auth-entitlements.md:25) sagt: 'Jede Cross-Org- oder Cross-Client-Exposition ist Critical'. Bestätigt durch den Defekttest (sitescope_auditdefect_test.go:167) und die Probe probe-sitescope-chain.log: Ein Site-gebundener Principal liest PII-Kontakte, Credential-Metadaten, Assets und CIs fremder Clients. Produktiv erreichbar über Einladungen mit scope_type site. Das Argument 'Rolle in der Org vorausgesetzt' trägt nicht, denn die Scope-Beschränkung ist genau die Sicherheitsgrenze. Konsistent mit T1-tenancy WP-020: Dort wurde eine Cross-Client-Offenlegung durch einen client-gescopten Engineer als P0 eingestuft.

<a id="f-015"></a>
### F-015 (P0) – Keine Grant-Obergrenze: client-gescopter user:manage-Inhaber erzeugt org-weite org_admin-Einladungen und -Rollenzuweisungen

- **Betroffene WPs:** WP-114 · **Komponente:** backend/internal/user (invitation.go, handler.go AssignRole) · **Quelle:** Verifikation (B14) + Defekttest-Welle
- **Reproduktion:** Delegate mit org_admin scope_client_id=client1: POST /api/v1/invitations {role_id: org_admin, scope_type: org} → 201, accept 201, neuer Nutzer org-weit mit user:manage (SP/work/verify-B14/probe-invite-ceiling.log). Ebenso POST /api/v1/role-assignments org-weit org_admin → 201.
- **Soll:** 403/422: Niemand vergibt Rollen/Scopes über die eigenen hinaus.
- **Ist:** 201, Eskalation in die Grants des neuen Nutzers wirksam.
- **Nutzerwirkung:** Delegierter Kundenadmin erlangt org-weiten Admin-Zugriff auf alle Clients, Entitlements, Audit und API-Keys (Privilegieneskalation; grenzt an P0, setzt aber bereits delegierte Admin-Rechte voraus).
- **Ursache (belegt):** user/invitation.go:235-262 checkInvitationTargets gibt für scope_type org nil zurück und vergleicht nie mit den Issuer-Grants; user/handler.go:712 AssignRole gleiches Muster.
- **Empfehlung:** Bei Issue, Renew und Rollenzuweisung prüfen: Zielberechtigungen ⊆ Issuer-Berechtigungen im Zielscope, Zielscope ⊆ Issuer-Scope; sonst 403.
- **Retest (verbindlich):** WT: cd backend && TEST_DATABASE_URL=…/audit_db_11 go test -tags auditdefect -count=1 -run 'TestAuditDefect(Invitation|RoleAssignment)GrantCeiling' ./internal/user/ → PASS.
- **Kodierender Test:** backend/internal/user/invitation_auditdefect_test.go TestAuditDefectInvitationGrantCeiling (:150), TestAuditDefectRoleAssignmentGrantCeiling (:211), beide fails-as-expected
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – prioritaet: „P1“ → „P0“. Privilegieneskalation ist ein Sicherheitsrisiko, also P0. Ein client-1-gescopter user:manage-Inhaber erzeugt org-weite org_admins: per Einladung (probe-invite-ceiling.log, invitation_auditdefect_test.go:150) und laut Defekttest :211 auch direkt per POST /role-assignments ('client-1 delegate grants org-wide org_admin to itself/others'). Ergebnis ist voller Zugriff auf alle Clients, Entitlements, Audit und API-Keys, also eine Cross-Client-Exposition, die TEN-09 als Critical einstuft. Die Synthese räumt selbst ein, dass dies 'an P0 grenzt'. Delegierte Kundenadmins sind im MSP-Modell der vorgesehene Normalfall und keine besondere Vorbedingung.

<a id="f-016"></a>
### F-016 (P0) – CI-Dialog 'CI erstellen' nutzt hart kodierte Typ-Keys: sendet 'server' als ci_type_id (400) und bietet keine Laufzeit-/Org-Typen

- **Betroffene WPs:** WP-046, WP-117, WP-119, WP-120, WP-191, WP-194, WP-201, WP-239 · **Komponente:** frontend/src/pages/CIFormModal.tsx · **Quelle:** Real-Stack-E2E (T16, D4, ci-lifecycle) + Verifikation (B15) + Defekttest-Welle; zusammengeführt mit L-T7-ui-03 (Verifikation (B24, B25) + Real-Stack-E2E + Defekttest-Welle)
- **Reproduktion:** /cmdb → 'CI erstellen' → Name, Typ 'Server' → 'Anlegen': POST /api/v1/cis {ci_type_id:'server'} → 400 'a field value has an invalid format or is out of range'; gleicher Request mit UUID → 201 (SP/work/verify-B15/probe-ci-ui-payload.log). Laufzeit-Typ 'Audit Custom PDU' erscheint nicht.
- **Soll:** CI-03/CI-01: Anlage mit gewähltem Typ (201, is_manual); MET-04: neue System-/Org-Typen ohne Release wählbar.
- **Ist:** Jede UI-Anlage scheitert für alle 8 Typen; nur 8 einkompilierte Keys; die meisten CI-01-Felder nicht erfassbar.
- **Nutzerwirkung:** Zentrale manuelle CMDB-Funktion blockiert; CIs nur über API, Import, Discovery. Gemockte e2e verdecken den Fehler.
- **Ursache (belegt):** CIFormModal.tsx:30 ciTypeValues=['server','switch',…], :66-75 Optionen daraus, :146 sendet Key; Backend speichert ci_type_id als uuid (22P02 → 400). Fix in WP-239 verschoben (E-03).
- **Empfehlung:** Typen über ciTypeApi.list (aktiv, System+Org) laden, type.id als Wert; CI-01-Felder bzw. dynamische Felder aus dem Schema; Backend 422 field ci_type_id für Nicht-UUID.
- **Retest (verbindlich):** WT: cd frontend && npx vitest run -c vitest.auditdefect.config.ts src/pages/CIFormModal.auditdefect.tsx → PASS; cd frontend && npx playwright test -c e2e-real/playwright.real.config.ts ci-lifecycle -g 'L01|L03a' sowie tenant-isolation -g 'T16' und roles-authz -g '12' → pass.
- **Kodierender Test:** frontend/src/pages/CIFormModal.auditdefect.tsx (2 Fälle, fails-as-expected); frontend/e2e-real/ci-lifecycle.spec.ts L01, L03a; tenant-isolation.spec.ts T16; roles-authz.spec.ts 12 (alle FAIL); frontend/src/pages/CITypeLabels.auditdefect.tsx:113 'posts the CI type UUID from GET /ci-types when the user picks "Server" (WP-201 P1)' und :140 'labels the type options with display_name in the UI language (WP-194 P2)' – failed as expected (SP/verify-tests-t357/results.tsv); Real-Stack frontend/e2e-real/ci-lifecycle.spec.ts:362 L01 FAIL (400); Querverweis T6: frontend/src/pages/CIFormModal.auditdefect.tsx:87, :100 – failed as expected
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – prioritaet: „P1“ → „P0“. 'Zentraler Prozess blockiert' ist P0 nach Definition, und die Synthese schreibt selbst 'Zentrale manuelle CMDB-Funktion blockiert'. Der Dialog ist der einzige UI-Weg zur manuellen CI-Anlage, und er scheitert immer (400) für alle Typen. Die SPA hat keine Import-Seite (App.tsx Routen 299-354), der Ausweg ist nur die rohe API. Die kodierte Liste (CIFormModal.tsx:30) enthält nur 8 physische Keys. Die 6 logischen MET-03-Typen (z. B. business_service, is_observable=false, nicht entdeckbar) lassen sich in der UI daher überhaupt nicht anlegen, Service-Modellierung ist in der UI blockiert. Hinweis zur Gruppenkonsistenz: Derselbe Defekt steht in T7-ui (WP-201) als P1 und sollte dort ebenfalls angehoben werden.
- **Prüfvermerk:** Gruppenübergreifend dedupliziert: L-T7-ui-03 („CI-Formular nutzt hart kodierte Typschlüssel statt GET /ci-types: POST /cis mit ci_type_id='server' → 400 (keine CI-Anlage per UI), keine Metamodell-Labels/Org-Typen; gemockte ci.spec.ts bleibt grün“) ist dieselbe Ursache und hier zusammengeführt.

<a id="f-017"></a>
### F-017 (P1) – Rollenzuweisung nicht entziehbar: kein DELETE /role-assignments/{id}

- **Betroffene WPs:** WP-009, WP-046, WP-066, WP-168 · **Komponente:** backend/internal/user/handler.go:88-120; user/model.go:97-101; api/openapi.yaml /api/v1/role-assignments · **Quelle:** Real-Stack-E2E (confirmed)
- **Reproduktion:** POST /role-assignments (engineer) 201; DELETE /role-assignments/{id} → 404; DB behält beide Rollen; roles-authz 13 FAIL.
- **Soll:** API-05/RBA-03/AUT-02: gewährte Rolle entziehbar, Wirkung beim nächsten Refresh.
- **Ist:** Kein Revoke-Pfad; nur Deaktivierung.
- **Nutzerwirkung:** Fälschlich vergebene Rollen (auch org_admin) nicht rücknehmbar.
- **Ursache (belegt):** Operation nicht implementiert. Keine verbindliche Kriterie der verknüpften WPs fordert DELETE explizit → keinem WP-Status zugerechnet.
- **Empfehlung:** DELETE mit role:manage, Grant-Ceiling, Audit, OpenAPI implementieren; einem WP zuordnen.
- **Retest (verbindlich):** npx playwright test -c e2e-real/playwright.real.config.ts roles-authz -g '13 an admin can revoke' PASS; API-Test DELETE 204, danach POST /cis 403, fremde Org 404.
- **Kodierender Test:** frontend/e2e-real/roles-authz.spec.ts:1019 13 (FAIL)

<a id="f-018"></a>
### F-018 (P1) – OpenAPI-Validator weist jeden SPA-Refresh (POST /auth/refresh ohne Body/Content-Type) mit 400 ab; gleiche Abweichung bei weiteren bodylosen POSTs

- **Betroffene WPs:** WP-009, WP-050, WP-066 · **Komponente:** backend/internal/middleware/openapi.go:93-100,233,301; cmd/server/main.go:447; frontend/src/auth/session.ts:22-31 · **Quelle:** Verifikation, Real-Stack-E2E (D2), Defekttest-Welle (neuer P3 zusammengeführt)
- **Reproduktion:** curl -X POST …/api/v1/auth/refresh -H 'Cookie: reticora_refresh=x' → 400 'Content-Type must be application/json' (refresh-curl.log); roles-authz 14 FAIL (final-run.log:269); TestAuditT2RefreshPassesOpenAPIValidation, TestAuditT2CookieOnlyRefreshThroughProductionChain, TestAuditT2ValidatorRegistryMatchesOpenAPIRequestBodies (auch /audit/verify, /webhooks/{id}/test) FAILS_AS_EXPECTED.
- **Soll:** AUT-02/WP-066: cookie-only POST erreicht Handler → 200, rotiertes Cookie; Validator nie strenger als OpenAPI.
- **Ist:** 400 vor dem Handler; SPA löscht Session.
- **Nutzerwirkung:** Abmeldung spätestens alle 15 min; Rollenänderungen erreichen offene Sessions nicht.
- **Ursache (belegt):** Handgepflegte Registry: refresh als bodyJSON (Altlast 41fe730, nicht angepasst in 2041fcb); Content-Type-Pflicht je registrierter POST.
- **Empfehlung:** Validierung aus OpenAPI ableiten; Konsistenztest; Router-Test über Produktionskette.
- **Retest (verbindlich):** cd backend && go test -tags auditdefect -count=1 -v -run 'TestAuditT2RefreshPassesOpenAPIValidation|TestAuditT2ValidatorRegistryMatchesOpenAPIRequestBodies' ./internal/middleware/ und -run '^TestAuditT2CookieOnlyRefreshThroughProductionChain$' ./internal/server/ PASS; roles-authz -g '14 the SPA renews' PASS.
- **Kodierender Test:** middleware/t2_auth_ratelimit_auditdefect_test.go (2), server/t2_auth_chain_auditdefect_test.go TestAuditT2CookieOnlyRefreshThroughProductionChain, e2e-real roles-authz 14 (FAIL)

<a id="f-019"></a>
### F-019 (P1) – Abgewiesene Requests einer Credential leeren das Org-Budget (eine Integration sperrt die ganze Org)

- **Betroffene WPs:** WP-166 · **Komponente:** backend/internal/middleware/ratelimit.go:111-143 (:123-138) · **Quelle:** Audit, Verifikation, Defekttest-Welle
- **Reproduktion:** vrun1 TestVB21OrgDrainRedis; TestAuditT2RefusedCredentialTrafficKeepsOrgBudget FAILS_AS_EXPECTED ('victim … [429 429 429], want [204 204 204]').
- **Soll:** API-04: Credential-Budget isoliert, Org-Budget zählt Zugelassenes.
- **Ist:** Jeder Versuch zählt gegen die Org.
- **Nutzerwirkung:** Org-weiter Ausfall durch einen Client.
- **Ursache (belegt):** Alle Budgets werden unbedingt inkrementiert.
- **Empfehlung:** Credential zuerst prüfen, Org nur bei Zulassung (Lua).
- **Retest (verbindlich):** TEST_REDIS_URL=redis://localhost:56379/0 go test -tags auditdefect -count=1 -v -run '^TestAuditT2RefusedCredentialTrafficKeepsOrgBudget$' ./internal/middleware/ PASS.
- **Kodierender Test:** middleware/t2_auth_ratelimit_auditdefect_test.go (FAILS_AS_EXPECTED)

<a id="f-020"></a>
### F-020 (P1) – Manuelle Massen-Statusänderung (POST /cis/bulk set_status) ist kein manueller Schreibvorgang: kein Override, keine Versionserhöhung, unter Status-Override still verworfen und als ok gemeldet

- **Betroffene WPs:** WP-057, WP-061, WP-181 · **Komponente:** backend/internal/ci/bulk.go:152; ci/pg_repository.go:783-856 · **Quelle:** Audit/Verifikation + Defekttest-Welle; zusammengeführt mit L-T7-ui-01 (Audit + Verifikation (B23, Probe f2) + Defekttest-Welle)
- **Reproduktion:** PG, echter ci-Handler: PATCH {status:inactive} (Override) → POST /api/v1/cis/bulk set_status active → 200 items[ok], ci.status bleibt inactive (PROBE3b, Kontrolle PROBE3c). Ohne Override: Status gesetzt, 0 Override-Zeilen, version 1→1, veralteter PATCH If-Match "1" → 200 (PROBE2, bulk_probe).
- **Soll:** CI-04/OVR-01: jede manuelle Änderung wird zum Override; API-07: version+1, veralteter If-Match → 409.
- **Ist:** Bulk ruft Update ohne Manual: verworfen unter Override, sonst ungeschützt und ohne Version.
- **Nutzerwirkung:** Die UI meldet einen Erfolg, der nicht eingetreten ist. Bulk-Werte können von Automation oder von veralteten Editoren still zurückgesetzt werden.
- **Ursache (belegt):** bulk.go:152 UpdateRequest{Status} ohne Manual → dropOverriddenFields (:783-788), leeres SET = Erfolg (:847-850), Version nur bei Manual/Authoritative (:852-856).
- **Empfehlung:** runBulk mit Manual{Author, Reason}. Vollständig verworfene Items als Konflikt/Fehler melden.
- **Retest (verbindlich):** cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<DB>?sslmode=disable go test -tags auditdefect -count=1 -v -run 'TestAuditT3BulkStatusIsManualWrite' ./internal/ci/ → PASS. Negativfall: Automation unter Override bleibt verworfen.
- **Kodierender Test:** backend/internal/ci/audit_t3_manual_writes_auditdefect_test.go TestAuditT3BulkStatusIsManualWrite (fails-as-expected); backend/internal/ci/audit_t7_bulk_jobs_auditdefect_test.go:191 TestAuditT7BulkReportMatchesStoredStatus (Subtests :194, :214, :231) – FAILS_AS_EXPECTED (SP/verify-tests-t357/results.tsv); Querverweis T3: backend/internal/ci/audit_t3_manual_writes_auditdefect_test.go:103 TestAuditT3BulkStatusIsManualWrite – FAILS_AS_EXPECTED
- **Prüfvermerk:** Gruppenübergreifend dedupliziert: L-T7-ui-01 („Bulk-Statusänderung ist kein manueller Schreibvorgang: CIs mit manuellem Status-Override werden still übersprungen und als ok gemeldet; keine Versionserhöhung, kein Override“) ist dieselbe Ursache und hier zusammengeführt.

<a id="f-021"></a>
### F-021 (P1) – Override setzen/aufheben erhöht ci.version nicht: veralteter If-Match-PATCH überschreibt den neueren geschützten Override

- **Betroffene WPs:** WP-061 · **Komponente:** backend/internal/override/pg_repository.go:323-450,613-645 (Endpunkte aus WP-060); ci/pg_repository.go:1585-1597 · **Quelle:** Audit/Verifikation + Defekttest-Welle
- **Reproduktion:** GET ETag "1" → PUT …/fields/model/override → ETag weiter "1", ci_change 0 → PATCH model mit If-Match "1" → 200, ci.model=override_value='stale-model'. Kontrolle PATCH/PATCH → 200 dann 409 (ciprobe).
- **Soll:** API-07: Override = Rang 100, version+1 mit ci_change; veralteter PATCH desselben Felds → 409. OVR-01: stiller Verlust ist Critical.
- **Ist:** Version unverändert, veralteter PATCH akzeptiert.
- **Nutzerwirkung:** Lost Update geschützter manueller Werte zwischen Provenienz-Dialog und offenem Bearbeiten-Dialog.
- **Ursache (belegt):** syncCIField aktualisiert die ci-Zeile ohne version+1 und ohne ci_change; checkPrecondition prüft nur ci_change.ci_version.
- **Empfehlung:** In SetOverride/ClearOverride Version erhöhen und ci_change schreiben, neues ETag zurückgeben.
- **Retest (verbindlich):** go test -tags auditdefect -count=1 -run 'TestAuditT3OverrideWritesRaiseVersion' ./internal/ci/ → PASS. Zusätzlich e2e-real: Override vs. offener Editor → 409.
- **Kodierender Test:** backend/internal/ci/audit_t3_manual_writes_auditdefect_test.go TestAuditT3OverrideWritesRaiseVersion

<a id="f-022"></a>
### F-022 (P1) – Auflösung „new“ bei resurrected_device erzeugt org-weites CI ohne Identität: fremde Client-Sichtbarkeit und endlose Review-Schleife

- **Betroffene WPs:** WP-087 · **Komponente:** backend/internal/discovery/review.go:164-187; identity.go:182 · **Quelle:** Audit/Verifikation + Defekttest-Welle
- **Reproduktion:** Client1-CI (MAC) soft-gelöscht → Record mit MAC und client_id=Client1 → Review → resolve {action:new} 200. Ergebnis: neues CI mit client_id NULL und primary_mac NULL, für einen Client2-Principal lesbar. Folgesichtungen: resurrected=1, das Review bleibt offen (TestVerifyB11ResurrectNew).
- **Soll:** CI-13 „neues CI“ für Client1 mit Identität, für Client2 unsichtbar (TEN-05). Die nächste Sichtung aktualisiert dieses CI.
- **Ist:** CI ohne Client und Identität. Das Gerät wird diesem CI nie wieder zugeordnet.
- **Nutzerwirkung:** Geräte werden für Nutzer anderer Kunden sichtbar. Das Gerät wird nicht mehr gepflegt, und ein späterer Merge erzeugt ein Duplikat.
- **Ursache (belegt):** ActionNew baut ci.Item ohne ClientID/PrimaryMAC/HardwareUUID/Fingerprint; Payload ohne client_id; Clientfilter identity.go:182.
- **Empfehlung:** Client- und Scope-Daten im Payload speichern und „new“ über den Discovery-Anlagepfad abwickeln. ambiguous_identity „new“ prüfen (gleicher Code, nicht separat reproduziert).
- **Retest (verbindlich):** go test -tags auditdefect -count=1 -run 'TestAuditT3ResurrectedNewKeepsClientAndIdentity' ./internal/discovery/ → PASS.
- **Kodierender Test:** backend/internal/discovery/audit_t3_review_resolution_auditdefect_test.go TestAuditT3ResurrectedNewKeepsClientAndIdentity

<a id="f-023"></a>
### F-023 (P1) – DELETE /sites|/buildings|/rooms/{id} löscht Standorte mit Kindern (204, Kaskade) statt 409 (LOC-11)

- **Betroffene WPs:** WP-055 · **Komponente:** backend/internal/tenantapi/pg_repository.go:288-305; locations/dependencies.go:46-63 · **Quelle:** Audit/Verifikation + Real-Stack-E2E (owned) + Defekttest-Welle
- **Reproduktion:** Real-Stack locations-racks.spec.ts:672 L05 (2 Läufe): site→building→room→rack ohne CIs; DELETE /rooms|/buildings|/sites → 204, Teilbaum in DB weg. Probe: /locations/{building} → 409 ['location'], /buildings/{gleich} → 204.
- **Soll:** LOC-11: 409 mit dependencies 'location' auf jedem Löschpfad; Teilbaum unverändert.
- **Ist:** 204 und Kaskade über location_parent_id_fkey. Kaskadierte Knoten erhalten keinen Audit-Eintrag.
- **Nutzerwirkung:** API-Clients verlieren ganze Standortstrukturen ohne Schutz und Audit (SPA nutzt /locations und ist geschützt).
- **Ursache (belegt):** CheckDeletable(…, subtree=true) zählt Kinder nur bei NOT $2; move_integration_test.go:202-204 kodiert Kaskade.
- **Empfehlung:** subtree=false bzw. 'location' melden oder dokumentierte Entscheidung; Test anpassen.
- **Retest (verbindlich):** go test -tags auditdefect -run 'TestAuditT3LocationDeleteRefusesChildren' ./internal/locations/ PASS und cd frontend && npx playwright test -c e2e-real/playwright.real.config.ts locations-racks -g "L05" grün.
- **Kodierender Test:** backend/internal/locations/audit_t3_delete_rules_auditdefect_test.go TestAuditT3LocationDeleteRefusesChildren; frontend/e2e-real/locations-racks.spec.ts L05
- **Prüfvermerk:** Auditleitung: Hochstufung P2 → P1 – das Löschen eines Standorts entfernt still den ganzen Teilbaum statt mit 409 abzulehnen (LOC-11); unbeabsichtigter Datenverlust.

<a id="f-024"></a>
### F-024 (P1) – Site-Löschung kaskadiert VLANs/Enrollment-Tokens und entzieht Subnetzen, Collectoren, Discovery-Scopes, ci_merge still die Site

- **Betroffene WPs:** WP-055 · **Komponente:** backend/internal/locations/dependencies.go:47-63; FKs auf site (CASCADE/SET NULL) · **Quelle:** Audit/Verifikation + Defekttest-Welle
- **Reproduktion:** Probe B1: Site mit VLAN und Subnetz → DELETE /api/v1/sites/{id} → 204, VLAN 0, Subnetz site_id NULL; pg_constraint 'c'/'n'.
- **Soll:** 409 mit vlan/subnet/collector/discovery_scope oder dokumentierte Entscheidung.
- **Ist:** 204, stiller Verlust.
- **Nutzerwirkung:** IPAM- und Discovery-Zuordnungen verloren, nur aus Backup wiederherstellbar.
- **Ursache (belegt):** dependencyQuery kennt diese Arten nicht; FKs CASCADE/SET NULL.
- **Empfehlung:** dependencyQuery erweitern und FKs auf RESTRICT setzen, oder Entscheidung dokumentieren.
- **Retest (verbindlich):** go test -tags auditdefect -run 'TestAuditT3SiteDeleteKeepsNetworkObjects' ./internal/locations/ PASS.
- **Kodierender Test:** backend/internal/locations/audit_t3_delete_rules_auditdefect_test.go TestAuditT3SiteDeleteKeepsNetworkObjects
- **Prüfvermerk:** Auditleitung: Hochstufung P2 → P1 – die Site-Löschung kaskadiert VLANs/Enrollment-Tokens und entzieht Zuordnungen; unbeabsichtigter Datenverlust (LOC-11).

<a id="f-025"></a>
### F-025 (P1) – Deaktivierter Collector liefert weiter: offene NATS-Verbindung bleibt bestehen, EdgeIngest, scopes.result und subnets.report ohne Statusprüfung

- **Betroffene WPs:** WP-145, WP-153 · **Komponente:** backend/internal/discovery (natsedge.go, edge_ingest.go, scope_handler.go) · **Quelle:** Audit/Verifikation WP-145; Defekttest-Welle
- **Reproduktion:** cd /home/user/Reticora-CMDB/backend && TEST_DATABASE_URL=… TEST_NATS_SERVER_BIN=SP/work/audit-B18/nats-server go test -tags auditdefect -count=1 -p 1 -v -run '^(TestAuditDisabledCollectorConnectionCut|TestAuditEdgeIngestRefusesDisabledOrUnknownCollector|TestAuditDisabledCollectorScopeChannelRefused)$' ./internal/discovery/
- **Soll:** COL-04: disabled → Zertifikat gesperrt, NATS-Account gesperrt, Verbindungen getrennt, weitere Daten abgewiesen.
- **Ist:** collector_pki_auditdefect_test.go:343: 5 s nach dem Deaktivieren ist der Collector noch CONNECTED, ein Publish auf .ingest wird zugestellt. ingest_auditdefect_test.go:98/101: EdgeIngest antwortet 202 und legt CIs an, auch für unbekannte Collector. disabled_collector_channel_auditdefect_test.go:73/79: scopes.result wird gespeichert, subnets.report legt einen Vorschlag an. Neue Verbindungen nach Neustart werden korrekt abgewiesen (Real-Stack D08 PASS).
- **Nutzerwirkung:** Ein kompromittierter oder fehlerhafter Collector lässt sich nicht sofort stoppen und kann bis zu 24 h (JWT-Laufzeit) Daten einspeisen.
- **Ursache (belegt):** Der Auth-Callout läuft nur bei CONNECT (natsedge.go:91-153). UpdateCollector sperrt nur DB-Zeilen (pg_repository.go:211-218). edge_ingest.go:74-101 und die Scope-/Subnet-Handler prüfen den Collector-Status nicht.
- **Empfehlung:** Beim Deaktivieren die Verbindung aktiv trennen (System-Account-Kick oder kurzlebige JWTs mit Revocation). Deaktivierte oder unbekannte Collector in EdgeIngest, scopes.result und subnets.report abweisen.
- **Retest (verbindlich):** Die drei Defekttests bestehen (Tag auditdefect, echter nats-server). Real-Stack-Variante von D08 ohne Neustart: innerhalb von 10 s kein weiterer persistierter Ingest.
- **Kodierender Test:** backend/internal/discovery/collector_pki_auditdefect_test.go:257 TestAuditDisabledCollectorConnectionCut; ingest_auditdefect_test.go:46 TestAuditEdgeIngestRefusesDisabledOrUnknownCollector; disabled_collector_channel_auditdefect_test.go:33 TestAuditDisabledCollectorScopeChannelRefused (alle FAILS_AS_EXPECTED)

<a id="f-026"></a>
### F-026 (P1) – Keine automatische Zertifikatserneuerung; Collector verlieren 90 Tage nach dem Enrollment die Konnektivität

- **Betroffene WPs:** WP-145 · **Komponente:** collector/collectorcmd; edgecore/enrollment/collector.go:127-150 · **Quelle:** Audit/Verifikation WP-145; Defekttest-Welle
- **Reproduktion:** cd /home/user/Reticora-CMDB/collector && TEST_NATS_SERVER_BIN=… go test -tags auditdefect -count=1 -v -run '^TestAuditCollectorRenewsCertificateAt60Percent$' ./collectorcmd/
- **Soll:** COL-04: automatische Erneuerung ab 60 % der Laufzeit über den bestehenden Kanal.
- **Ist:** lifecycle_auditdefect_test.go:330: Bei 61 % sendet der Collector innerhalb von 15 s keinen CSR, weder über HTTP noch über NATS. Renew hat keinen Produktionsaufrufer, und der HTTP-Pfad antwortet 401 (e2e-onboarding-real.log:15,19).
- **Nutzerwirkung:** Jeder Collector hört nach 90 Tagen still auf zu liefern und muss mit neuer ID neu enrollt werden; die Scope-Zuordnungen gehen verloren.
- **Ursache (belegt):** Renew existiert nur als Bibliothek mit Bearer-HTTP und ist in collectorcmd nicht verdrahtet.
- **Empfehlung:** Erneuerung über den mTLS-NATS-Kanal, gebunden an die Identität; täglicher Check; neuen Schlüssel persistieren (0600) und neu verbinden.
- **Retest (verbindlich):** TestAuditCollectorRenewsCertificateAt60Percent besteht; Unit-Test mit Fake-Clock: an Tag 53 keine Anfrage, an Tag 55 neues Zertifikat.
- **Kodierender Test:** collector/collectorcmd/lifecycle_auditdefect_test.go:213 TestAuditCollectorRenewsCertificateAt60Percent (FAILS_AS_EXPECTED)

<a id="f-027"></a>
### F-027 (P1) – Falsche Credential-Auswahl im Bundle: ohne aktive Scopes alle Client- und Org-Credentials, ein Scope ohne credential_ids leert das Bundle

- **Betroffene WPs:** WP-147, WP-153, WP-156 · **Komponente:** backend/internal/discovery/natsedge.go:446-458; backend/internal/credential/bundle.go:161-171 · **Quelle:** Audit/Verifikation WP-153, WP-147; Defekttest-Welle
- **Reproduktion:** cd /home/user/Reticora-CMDB/backend && TEST_DATABASE_URL=… go test -tags auditdefect -count=1 -p 1 -v -run '^TestAuditBundleCredentialSelection$' ./internal/discovery/
- **Soll:** Nur Scope-relevante Credentials; das Annehmen eines Vorschlags oder ein Scope aus der UI entzieht dem Collector seine Credentials nicht.
- **Ist:** collector_pki_auditdefect_test.go:416: Ein Collector ohne aktive Scopes erhält 2 Credentials (erwartet []). Ein Scope ohne Credentials liefert [] (erwartet das Org-Credential). Die Kontrolle mit expliziten IDs besteht.
- **Nutzerwirkung:** Ohne Scopes werden Geheimnisse überversorgt. Nach 'Scopes bestätigen' im Onboarding verschwinden binnen 15 min alle Vault-Credentials vom Collector, und SNMP/SSH/Redfish scheitern.
- **Ursache (belegt):** CredentialIDs wird nur bei count(enabled scopes)>0 gesetzt, und zwar als Vereinigung der Scope-IDs; nil und [] werden verwechselt.
- **Empfehlung:** Semantik festlegen: Ein Scope ohne Credentials nutzt die Client-/Org-Credentials, ohne aktive Scopes gibt es ein leeres Bundle. Credential-Auswahl in Formular und Bestätigungsablauf.
- **Retest (verbindlich):** TestAuditBundleCredentialSelection besteht. e2e-real: Vorschlag bestätigen → das Bundle enthält weiter die Credentials.
- **Kodierender Test:** backend/internal/discovery/collector_pki_auditdefect_test.go:357 TestAuditBundleCredentialSelection (FAILS_AS_EXPECTED)

<a id="f-028"></a>
### F-028 (P1) – Credentials der Art 'wmi' erreichen das WMI-Plugin nie; SSH-Credentials gehen an WMI- und NAS-Ziele

- **Betroffene WPs:** WP-152 · **Komponente:** collector/plugins/plugin.go:74-78; collector/collectorcmd/bundle.go:105-108 · **Quelle:** Audit/Verifikation WP-152; Defekttest-Welle
- **Reproduktion:** cd /home/user/Reticora-CMDB/collector && go test -tags auditdefect -count=1 -v -run '^TestAuditCredentialKindRouting$' ./collectorcmd/
- **Soll:** Die Art wmi wird vom WMI-Plugin genutzt; SSH-Geheimnisse erreichen nie andere Protokolle.
- **Ist:** discovery_auditdefect_test.go:288/296: Das WMI-Plugin erhält 'ssh-secret' statt 'wmi-secret'; das SSH-Credential geht an das wmi- und das nas-Plugin.
- **Nutzerwirkung:** WMI-Inventar mit dem dafür vorgesehenen Credential funktioniert nicht, und SSH-Root-Zugangsdaten gehen an Windows- und NAS-Ziele.
- **Ursache (belegt):** Die Aliasse wmi→ssh und nas→ssh in plugin.go widersprechen dem Namespace 'wmi' in bundle.go.
- **Empfehlung:** Aliasse entfernen (eigene Art nas oder explizites Mapping); Vertragstest von jeder Backend-Art zum Plugin.
- **Retest (verbindlich):** TestAuditCredentialKindRouting besteht.
- **Kodierender Test:** collector/collectorcmd/discovery_auditdefect_test.go:258 TestAuditCredentialKindRouting (FAILS_AS_EXPECTED)

<a id="f-029"></a>
### F-029 (P1) – WMI Collect meldet Erfolg mit leeren Feldern bei 401, Transportfehler oder protokollkonformer WinRM-Antwort; der Scope-Lauf zeigt ok

- **Betroffene WPs:** WP-152, WP-155 · **Komponente:** collector/plugins/wmi/wmi.go:204-255 · **Quelle:** Audit/Verifikation WP-152 (P1) und WP-155 (P2, gleiche Ursache); Defekttest-Welle
- **Reproduktion:** cd /home/user/Reticora-CMDB/collector && go test -tags auditdefect -count=1 -v -run '^TestAuditWMIFailuresAreErrors$' ./plugins/wmi/ && go test -tags auditdefect -count=1 -v -run '^TestAuditWMIAuthFailureReachesScopeRun$' ./collectorcmd/
- **Soll:** DIS-04: WMI liefert die DeviceRecord-Felder. DIS-08: Fehler werden mit Ursache (auth_failed, unreachable) gemeldet.
- **Ist:** wmi_auditdefect_test.go:211/213: Collect meldet bei 401, connection refused und Enumerate+Pull nach DSP0226 err=nil mit serial=''. discovery_auditdefect_test.go:438: WinRM antwortet immer 401, der Scope-Lauf meldet records=1, error_count=0.
- **Nutzerwirkung:** Windows-Server erscheinen ohne Seriennummer, Modell und OS, und der Scope zeigt ok. Betreiber erfahren nie, dass WMI scheitert.
- **Ursache (belegt):** wsmanEnumerate verschluckt Transportfehler und HTTP-Status; im Enumerate-Request fehlen OptimizeEnumeration, Pull und MessageID; die Fixture liefert die Daten direkt im Enumerate.
- **Empfehlung:** Bei Fehlern und Nicht-2xx einen Fehler zurückgeben (401 → auth_failed); Enumerate/Pull vollständig implementieren; protokolltreuer Fake.
- **Retest (verbindlich):** Beide Defekttests bestehen; die Scope-Health zeigt auth_failed mit Protokoll wmi und Ziel.
- **Kodierender Test:** collector/plugins/wmi/wmi_auditdefect_test.go:169 TestAuditWMIFailuresAreErrors; collector/collectorcmd/discovery_auditdefect_test.go:413 TestAuditWMIAuthFailureReachesScopeRun (beide FAILS_AS_EXPECTED)

<a id="f-030"></a>
### F-030 (P1) – Jedes gesweepte Gerät erhält ein (wiederkehrendes) falsches unclassified_device-Review, auch wenn SNMP, SSH oder Redfish es klassifiziert haben

- **Betroffene WPs:** WP-151 · **Komponente:** collector/plugins/sweep/sweep.go:249-262, collector/profiles/classify.go:218-283; backend/internal/discovery/discovery.go:1238-1256 · **Quelle:** Verifikation WP-151 (neuer P1-Befund); Defekttest-Welle
- **Reproduktion:** cd /home/user/Reticora-CMDB/backend && go test -tags auditdefect -count=1 -v -run '^TestAuditUnclassifiedReviewOnlyForUnclassifiedDevices$' ./internal/discovery/; cd ../collector && go test -tags auditdefect -count=1 -v -run '^TestAuditClassifiedDeviceNotUploadedAsFallback$' ./collectorcmd/
- **Soll:** DIS-02: Nur ein Gerät ohne jeden Profiltreffer wird generic_device mit Review; ein verworfenes Review bleibt verworfen.
- **Ist:** ingest_auditdefect_test.go:162: 1 offenes Review für eine als switch klassifizierte CI; :178: Das verworfene Review wird beim nächsten Sweep wieder geöffnet. discovery_auditdefect_test.go:502: Zusätzlich zu snmp:switch wird ein Record 'sweep:generic_device/fallback' hochgeladen.
- **Nutzerwirkung:** Der Review-Eingang füllt sich bei jedem Sweep mit einem Fehleintrag je Host; echte unklassifizierte Geräte gehen darin unter.
- **Ursache (belegt):** Klassifikation je Protokoll-Record ohne Evidenz. Der Server öffnet das Review vor der Identitätsauflösung und ohne Prüfung des CI-Typs, mit Open statt OpenUnlessDismissed.
- **Empfehlung:** Sweep-Records ohne Evidenz nicht als fallback markieren oder die Records je Ziel zusammenführen. Review nur, wenn die CI nach der Auflösung generic_device bleibt; OpenUnlessDismissed; Auto-Resolve bei Höherstufung.
- **Retest (verbindlich):** Beide Defekttests bestehen; TestClassificationChain und TestTST01Reconciliation bleiben PASS.
- **Kodierender Test:** backend/internal/discovery/ingest_auditdefect_test.go:107 TestAuditUnclassifiedReviewOnlyForUnclassifiedDevices; collector/collectorcmd/discovery_auditdefect_test.go:447 TestAuditClassifiedDeviceNotUploadedAsFallback (beide FAILS_AS_EXPECTED)

<a id="f-031"></a>
### F-031 (P1) – scope_suggested umgeht discovery:manage und ist über Client-Grenzen sichtbar und verwerfbar

- **Betroffene WPs:** WP-153, WP-156 · **Komponente:** backend/internal/reviews/handler.go:89-110; review_item-RLS; scope_handler.go:193-233 · **Quelle:** Audit/Verifikation WP-153; Defekttest-Welle
- **Reproduktion:** cd /home/user/Reticora-CMDB/backend && TEST_DATABASE_URL=… go test -tags auditdefect -count=1 -p 1 -v -run '^TestAuditScopeSuggestedAuthzAndClientIsolation$' ./internal/server/
- **Soll:** DIS-09: Scopes anlegen nur mit discovery:manage. TEN: Client-Principals sehen und ändern nur Einträge ihres Clients.
- **Ist:** scope_suggested_authz_auditdefect_test.go:160/168: client_technician kann annehmen (200, Scope angelegt) und verwerfen (200). :188: Ein Client-1-Principal listet einen Vorschlag von Client 2. :201: Er verwirft ihn dauerhaft (200 statt 404). Die org_admin-Kontrolle besteht.
- **Nutzerwirkung:** Nutzer ohne Discovery-Recht lösen Netzscans aus oder blockieren das Onboarding eines anderen Clients; Subnetze anderer Clients werden offengelegt.
- **Ursache (belegt):** review_item-RLS gilt nur auf Org-Ebene, der Vorschlag ist nicht an den Client gebunden, und resolve prüft keine Berechtigung je Art.
- **Empfehlung:** Vorschläge an den Client des Collectors binden (Spalte plus RLS); für scope_suggested discovery:manage verlangen.
- **Retest (verbindlich):** TestAuditScopeSuggestedAuthzAndClientIsolation besteht.
- **Kodierender Test:** backend/internal/server/scope_suggested_authz_auditdefect_test.go:53 TestAuditScopeSuggestedAuthzAndClientIsolation (FAILS_AS_EXPECTED)
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – prioritaet: „P2“ → „P1“. Belegt durch TestAuditScopeSuggestedAuthzAndClientIsolation. Bei :160/:168 legt client_technician ohne discovery:manage (role_matrix.go:56-65) per Annahme einen Scope an und löst damit Netzscans aus. Bei :188 listet ein Principal von Client 1 die Vorschläge von Client 2, bei :201 verwirft er sie dauerhaft. Das ist eine Umgehung der Autorisierung und ein lesender und schreibender Bruch der Client-Isolation. Unter die P2-Definition (funktionale, Validierungs- oder UX-Einschränkung) fällt das nicht. Ein vergleichbarer Scope-Bruch mit Lesen und Löschen ist mit P1 bewertet (L-T1-02). Nicht P0, weil nur Subnetz-Metadaten und Vorschläge betroffen sind, keine Geheimnisse.

<a id="f-032"></a>
### F-032 (P1) – Ausgelieferter Collector liefert keine LLDP-/CDP-/FDB-Nachbarn (TOP-01-Eingang fehlt in Produktion)

- **Betroffene WPs:** WP-158 · **Komponente:** collector/plugins/snmp · **Quelle:** Verifikation WP-158; Defekttest-Welle
- **Reproduktion:** cd /home/user/Reticora-CMDB/collector && go test -tags auditdefect -count=1 -v -run '^TestAuditSNMPCollectsNeighbors$' ./plugins/snmp/
- **Soll:** DIS-05/TOP-01: Die reale Discovery liefert Nachbarn (lldp/fdb mit LocalInterface).
- **Ist:** neighbors_auditdefect_test.go:101/104: LLDP-Nachbar dist-sw-2 und FDB-MAC nicht gefunden (neighbors=[]). Nur plugins/simulate/simulate.go:149 erzeugt DeviceNeighbor.
- **Nutzerwirkung:** Nutzer des echten Collectors bekommen keine automatische Netztopologie; die Logik aus WP-158 bis WP-160 bleibt ungenutzt.
- **Ursache (belegt):** Das SNMP-Plugin fragt weder lldpRemTable noch den CDP-Cache noch BRIDGE-/Q-BRIDGE-MIB ab.
- **Empfehlung:** Diese Tabellen abfragen und auf DeviceNeighbor abbilden; Zuständigkeit im Collector-Protokoll-WP (DIS-04/05) festhalten.
- **Retest (verbindlich):** TestAuditSNMPCollectsNeighbors besteht; die Kontrolle ohne MIB bleibt fehlerfrei.
- **Kodierender Test:** collector/plugins/snmp/neighbors_auditdefect_test.go:68 TestAuditSNMPCollectsNeighbors (FAILS_AS_EXPECTED)
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – prioritaet: „P2“ → „P1“. TestAuditSNMPCollectsNeighbors liefert neighbors=[]. Nur plugins/simulate erzeugt DeviceNeighbor. Mit dem ausgelieferten Collector entsteht damit keine einzige connected_to-Kante aus der Discovery. Die automatische Netztopologie fehlt in Produktion vollständig, also der Eingang für TOP-01 und die Nachbarn nach DIS-05, und mit ihr die Logik von WP-158 bis WP-160. Die Synthese schreibt selbst: 'läuft der Nutzerprozess Topologie leer'. Das ist 'wesentliche Funktion fehlt' (P1), keine bloße 'relevante Einschränkung' (P2).

<a id="f-033"></a>
### F-033 (P1) – certbot certonly kann das Let's-Encrypt-Zertifikat nicht ablegen, weil der Bootstrap-Platzhalter letsencrypt/live/<domain>/ belegt

- **Betroffene WPs:** WP-094 · **Komponente:** install-cloud.sh (bootstrap_tls_cert :815-863, issue_tls_certificate :923-961, validate_tls_material :947, Reihenfolge :1657-1661) · **Quelle:** Audit (B12) + Verifikation (echte certbot-2.11.0-CLI-Probe) + Defekttest-Welle T5 (in Synthese wiederholt)
- **Reproduktion:** SP/work/verify-B12/certbot/cli_probe.py mit certbot 2.11.0 (gepinnte Image-Version) unter python -I; nur _determine_account, Client.__init__ und Client.obtain_certificate gepatcht (kein Netz). Platzhalter exakt wie bootstrap_tls_cert in <tmp>/letsencrypt/live/cmdb.example.com/ anlegen, dann certbot._internal.main.main(['certonly','--webroot',…,'-d','cmdb.example.com','--config-dir',<tmp>/letsencrypt,'--non-interactive','--agree-tos','--register-unsafely-without-email']) zweimal. Lauf 1: ACME-Order verbraucht, 'CertStorageError: live directory exists for cmdb.example.com', renewal/cmdb.example.com.conf 0 Byte, Platzhalter bleibt. Lauf 2 (Installer-Wiederholung): Zertifikat in live/cmdb.example.com-0001/ (zweite Order), live/cmdb.example.com/fullchain.pem bleibt selbstsigniert (openssl verify error 18). Kodiert in tests/audit/install-cloud-acme.test.sh; in dieser Synthese erneut ausgeführt: 'fresh install serves the issued certificate' FAIL, 'installer re-run is idempotent' FAIL (SP/work/synth-T5/acme.log).
- **Soll:** Nach issue_tls_certificate liegt in live/<domain>/fullchain.pem die ausgestellte Kette, validate_tls_material <domain> 1 besteht, nginx liefert sie aus; ein erneuter Installerlauf ist idempotent (keine zweite Order, keine -0001-Lineage).
- **Ist:** Erstinstallation behält den selbstsignierten Platzhalter (nur Warnung, leere renewal-conf); Wiederholung legt -0001-Lineage an, verbraucht eine zweite Order und bricht in validate_tls_material (install-cloud.sh:947) ab.
- **Nutzerwirkung:** Domain-/TLS-Installationen erhalten nie ein vertrauenswürdiges Zertifikat (dauerhafte Browserwarnungen); Wiederholungsläufe des Installers brechen ab; jede Ausführung verbraucht Let's-Encrypt-Orders (Rate-Limits).
- **Ursache (belegt):** issue_tls_certificate ruft certbot certonly auf, ohne den Platzhalter in live/<domain> zu entfernen oder zu verschieben; certbot storage.py new_lineage (:1086-1089) verweigert nach obtain_certificate (client.py:517-528) ein nicht leeres live-Verzeichnis. Der Repo-Test tests/install-cloud-helpers.test.sh:199-204 ersetzt certbot durch eine Dateikopie und verdeckt den Fehler.
- **Empfehlung:** Vor certonly live/<domain> (sowie archive/<domain>, renewal/<domain>.conf) beiseite verschieben und bei Fehlschlag wiederherstellen; alternativ Platzhalter außerhalb des certbot-Baums ablegen und nginx über einen stabilen Pfad/Symlink nach erfolgreicher Ausstellung umschalten. Certbot-Stub durch Pebble-Integrationstest ersetzen und eine TLS-Domain-Variante in install-smoke.yml aufnehmen.
- **Retest (verbindlich):** ./tests/audit/install-cloud-acme.test.sh (Branch audit-defects-T5, Commit 917193d) meldet 4/4 ok; zusätzlich Pebble + certbot/certbot:v2.11.0 mit vorhandenem Bootstrap: issue_tls_certificate → live/<domain>/fullchain.pem verkettet zur Pebble-Root, validate_tls_material <domain> 1 besteht; zweiter Installerlauf ohne neue Order, ohne -0001, ohne Abbruch.
- **Kodierender Test:** tests/audit/install-cloud-acme.test.sh – Fälle 'acme: fresh install serves the issued certificate from live/<domain>' und 'acme: installer re-run is idempotent (no -0001 lineage, no 2nd order)' (fails-as-expected); Kontrolle 'acme: failed challenge keeps the placeholder' ok.

<a id="f-034"></a>
### F-034 (P1) – Von certbot ausgestellter Schlüssel und Kette sind für den unprivilegierten nginx-Nutzer (uid 101) des Frontends unlesbar

- **Betroffene WPs:** WP-094 · **Komponente:** install-cloud.sh (chmod nur für Bootstrap :848, :862), deploy/docker-compose/docker-compose.yml:370 (frontend user: nginx), :382, :415-426 (certbot ohne Deploy-Hook), nginx-tls.conf.template:47-48 · **Quelle:** Verifikation (B12, uid-101-Leseprobe) + Defekttest-Welle T5
- **Reproduktion:** Nach Lauf 2 von SP/work/verify-B12/certbot/cli_probe.py: stat zeigt letsencrypt/archive 700 root, archive/cmdb.example.com-0001/privkey1.pem 600 root. 'docker run --rm --user 101:101 -v <tmp>/letsencrypt:/etc/letsencrypt:ro redis:7-alpine cat …' → live/cmdb.example.com/privkey.pem (Installer-chmod 644) READ_OK; live/cmdb.example.com-0001/privkey.pem und fullchain.pem 'Permission denied'. Kodiert in tests/audit/install-cloud-acme.test.sh Fall 'issued key and chain are readable by nginx (uid 101)' (Rechteauswertung nach certbot-2.11.0-Modi), in dieser Synthese erneut FAIL.
- **Soll:** Nach Behebung von L-T5-01 lädt nginx das ausgestellte Zertifikat beim Reload und bei jedem Neustart.
- **Ist:** certbot (root im Container) legt archive/ 0700 und privkey 0600 an; kein chmod/chgrp/Deploy-Hook passt die Modi an. nginx -s reload scheitert im Container, der Installer prüft nur den Exit-Code des Signalversands; der nächste Frontend-Neustart scheitert am Schlüssel.
- **Nutzerwirkung:** Latent: Nach Behebung des Lineage-Fehlers würde HTTPS weiter den Platzhalter aus dem Speicher liefern, und das Frontend (gesamte UI) startet nach dem nächsten Neustart oder Host-Reboot nicht mehr.
- **Ursache (belegt):** Der Bootstrap-Pfad macht Dateien für uid 101 lesbar, certbot-verwaltetes archive/ und privkey behalten die restriktiven certbot-Standardmodi; nichts gleicht sie an.
- **Empfehlung:** certbot --deploy-hook (im Installer-certonly und in der Renew-Schleife), der gruppenlesbare Modi für eine gemeinsame GID setzt oder fullchain/privkey nginx-lesbar kopiert und anschließend nginx neu lädt; Lesbarkeit als uid 101 im Pebble-Test prüfen.
- **Retest (verbindlich):** tests/audit/install-cloud-acme.test.sh Fall uid 101 ok; Pebble-Integration: nach Ausstellung und nach 'certbot renew --force-renewal' gelingt 'docker compose exec frontend cat /etc/letsencrypt/live/<domain>/privkey.pem', 'nginx -t' als uid 101 besteht, 'docker compose restart frontend' wird healthy und liefert die Pebble-Kette.
- **Kodierender Test:** tests/audit/install-cloud-acme.test.sh – Fall 'acme: issued key and chain are readable by nginx (uid 101)' (fails-as-expected).

<a id="f-035"></a>
### F-035 (P1) – OIDC-Issuer-Prüfung läuft vor dem Start des Frontends (Issuer-Proxy), daher brechen Domain-/TLS-Installationen ab

- **Betroffene WPs:** WP-095 · **Komponente:** install-cloud.sh validate_keycloak_bootstrap :1057-1105 (check_issuer_from_host :1103, check_issuer_from_container :1104), start_stack :1385 vs. :1389, Default-Issuer :603; frontend/nginx.conf:111 und nginx-tls.conf.template:86 (/realms/-Proxy) · **Quelle:** Audit (B12) + Verifikation (Reihenfolgeprobe) + Defekttest-Welle T5 (in Synthese wiederholt)
- **Reproduktion:** SP/work/verify-B12/issuer/order.sh <dir>: sourct HEAD-install-cloud.sh ohne main (diff leer), stubbt compose/wait/sync/migrations/admin; curl beantwortet https://cmdb.example.test/* erst nach 'up -d server frontend', localhost:8180 immer. RETICORA_PUBLIC_BASE_URL=https://cmdb.example.test, RETICORA_TLS_DOMAIN=cmdb.example.test, RETICORA_OIDC_ISSUER_URL=https://cmdb.example.test/realms/reticora (Default :603), dann start_stack → rc=1 '✘ Error: The OIDC issuer https://cmdb.example.test/realms/reticora is not reachable from this host …'; Compose-Aufrufe enden nach 'up -d keycloak'. Kodiert in tests/audit/install-cloud-issuer-order.test.sh; in dieser Synthese erneut: '2 passed, 1 failed' (SP/work/synth-T5/issuer-order.log).
- **Soll:** Eine Erstinstallation mit öffentlicher Domain läuft durch; Host- und Container-Issuer-Prüfung bestehen, sobald der Issuer-Endpunkt bedient wird; unerreichbarer Issuer bricht weiterhin mit Doku-Hinweis ab.
- **Ist:** Jede Erstinstallation und jede Wiederholung mit Domain-Issuer (Installer-Default und in docs/troubleshooting-installation.md:132-133 empfohlene Form) bricht nach 'up -d keycloak' ab.
- **Nutzerwirkung:** Der produktive Installationsweg mit Domain/TLS (TEC-09) ist blockiert; Betreiber müssen auf http://<ip>:8180 ausweichen, entgegen der dokumentierten Reverse-Proxy-Form. Zusammen mit L-T5-01 ist eine TLS-Produktionsinstallation über den Installer nicht möglich.
- **Ursache (belegt):** check_issuer_from_host/check_issuer_from_container wurden an validate_keycloak_bootstrap angehängt, das start_stack (:1385) vor 'up -d server frontend' (:1389) aufruft; die Shell-Tests stubben curl und modellieren keinen frontend-bedienten Issuer.
- **Empfehlung:** Strikte Host- und Container-Issuer-Prüfung nach dem Start von server und frontend ausführen (bei Fehlschlag vor Admin-Anlage/Zusammenfassung abbrechen) oder Keycloak bei noch gestopptem Frontend direkt mit öffentlichem Host-/X-Forwarded-Proto-Header prüfen; Reihenfolge-Regressionstest beibehalten.
- **Retest (verbindlich):** ./tests/audit/install-cloud-issuer-order.test.sh meldet 3/3 ok (Domain-Issuer rc 0 mit beiden Prüfungen nach dem Frontend-Start; unerreichbar rc 1 mit Hinweis; IP-Issuer weiterhin geprüft); real: install-smoke-TLS-Domain-Variante läuft durch und protokolliert beide erfolgreichen Issuer-Prüfungen.
- **Kodierender Test:** tests/audit/install-cloud-issuer-order.test.sh – Fall 'issuer: domain issuer is checked after the frontend start (start_stack rc 0)' (fails-as-expected); Kontrollen ok.

<a id="f-036"></a>
### F-036 (P1) – K8s-Manifeste liefern das durch WP-097 verpflichtende RETICORA_OIDC_ISSUER_URL nicht; Server beendet sich in base, staging und prod beim Start

- **Betroffene WPs:** WP-097, WP-099 · **Komponente:** deploy/k8s/base/deployment.yaml:48-105, deploy/k8s/overlays/staging/kustomization.yaml:47-54 (ersetzt die env-Liste), deploy/README.md:239-259, backend/internal/config/config.go:382-387 (requiredOutsideDevelopment, Commit 75aa419), .github/workflows/ci.yml:544-574 (k8s-manifests prüft nur Build und master-key) · **Quelle:** Audit (B12) + Verifikation (Binary mit gerenderter Env) + Defekttest-Welle T5 (in Synthese mit kustomize wiederholt)
- **Reproduktion:** kustomize v5.8.1 build deploy/k8s/{base,overlays/staging,overlays/prod}: Server-Container-Env enthält kein RETICORA_OIDC_ISSUER_URL (auch keine OIDC-Client-ID/-Secret/-Redirect-URL), kein envFrom. HEAD-Binary mit exakt dieser Env (env -i): exit 1 'invalid configuration', 'RETICORA_ENVIRONMENT=staging requires RETICORA_OIDC_ISSUER_URL' (production ebenso; SP/work/verify-B12/k8s-env-staging.log, k8s-env-production.log). Kodiert: TestAuditT5K8sRenderedEnvSatisfiesStartCheck (backend/internal/config/k8s_required_env_auditdefect_test.go, -tags auditdefect); in dieser Synthese mit KUSTOMIZE=/root/go/bin/kustomize erneut: 3/3 Subtests FAIL auf der Assertion 'the server would exit at start (CrashLoopBackOff)' (SP/work/synth-T5/k8s-env.log). Hinweis: Die mechanische Verifikation meldete FAILS_AS_EXPECTED nur wegen fehlendem kustomize-Binary.
- **Soll:** Gerenderte Builds aller Overlays liefern eine Konfiguration, die der Server akzeptiert; Pflicht-Secrets/-Konfiguration sind in deploy/README.md dokumentiert; CI erkennt fehlende Pflichtschlüssel.
- **Ist:** Pods beenden sich beim Start mit exit 1 (CrashLoopBackOff) in base, staging und prod, trotz korrekt injiziertem Master-Key; deploy/README.md nennt nur database-url und master-key.
- **Nutzerwirkung:** Der Kubernetes-Deploymentpfad (OPS-03) ist out of the box unbenutzbar; ein Rollout nach deploy/README.md scheitert.
- **Ursache (belegt):** Commit 75aa419 ersetzte den localhost-Default durch einen Pflichtschlüssel (config.go:386), änderte deploy/k8s/base/deployment.yaml aber nur für die Probes; staging ersetzt die gesamte env-Liste; CI prüft nur Build-Erfolg und die master-key-Referenz.
- **Empfehlung:** OIDC-Issuer (plus Client-ID/-Secret/Redirect-URL) per ConfigMap/Secret-Referenz in base und in der staging-Env-Ersetzung ergänzen und dokumentieren; CI-Schritt, der für jedes Overlay alle requiredOutsideDevelopment-Schlüssel im gerenderten Env verlangt (Quelle: config-Paket), mit Negativfall.
- **Retest (verbindlich):** cd backend && KUSTOMIZE=$(go env GOPATH)/bin/kustomize go test -tags auditdefect -count=1 -v -run 'TestAuditT5K8sRenderedEnvSatisfiesStartCheck' ./internal/config/ → PASS (3/3); optional kind-Smoke: Secrets/Config anlegen, staging anwenden → Pod Ready, /readyz 200; Negativ: master-key entfernt → CreateContainerConfigError.
- **Kodierender Test:** backend/internal/config/k8s_required_env_auditdefect_test.go TestAuditT5K8sRenderedEnvSatisfiesStartCheck (Branch audit-defects-T5; fails-as-expected).

<a id="f-037"></a>
### F-037 (P1) – CI-13-Reaktivierung über die Produktions-Decorator-Kette aus main.go liefert 501; gelöschtes, wiedergesehenes Gerät bleibt gelöscht (SR-11)

- **Betroffene WPs:** WP-087, WP-213 · **Komponente:** backend/internal/ci/indexing_repository.go (implementiert nur RestoreDeleted, nicht Restore(ctx, org, id)); backend/cmd/server/main.go:327, :355 (search.NewCIIndexer, ai.NewCIChunkIndexer); backend/internal/discovery/review.go:134-137 (verlangt ciRestorer); Owner-WP WP-087 (Gruppe T3-data-ingest, dort nicht als Befund erfasst) · **Quelle:** Audit (B27) + Verifikation (Kontrollprobe) + Defekttest-Welle T5 (mechanisch verifiziert)
- **Reproduktion:** Probe TestVerifyB27SR11 (SP/work/verify-B27/tree/backend/internal/tenant/rls/zz_verify_b27_sr11_test.go) gegen audit_db_21, Router über server.NewRouter in zwei Varianten: (a) repos.CI nackt (PG), (b) wie main.go (Such- und KI-Decorator). Ablauf: CI per POST anlegen, DELETE, review_item resurrected_device mit Kandidat, POST /api/v1/review-items/{id}/resolve {action: merge, ci_id}. (a) 200, deleted=false, Review resolved, GET 200; (b) 'repos.CI implements Restore: false', 501 'this repository cannot reactivate CIs', deleted=true, Review open, GET /cis/{id} 404 (SP/work/verify-B27/sr11-verify.log). Kodiert: TestAuditT5CIReactivationThroughProductionDecoratorChain (backend/internal/tenant/rls/audit_t5_sr11_decorator_chain_auditdefect_test.go); mechanische Verifikation FAILS_AS_EXPECTED ('wiring main.go: the CI repository does not implement Restore(ctx, org, id)'), Kontrolle bare-pg PASS.
- **Soll:** CI-13 [B]: Auflösen eines resurrected_device-Reviews mit merge reaktiviert das CI (200, deleted_at IS NULL, Review resolved, Audit ci.restored) – auch in der Produktionsverdrahtung.
- **Ist:** In der Produktionsverdrahtung 501, CI bleibt gelöscht, Review bleibt offen; die UI bietet 'merge' an (frontend useReviews.ts:28) und erhält 501.
- **Nutzerwirkung:** Ein wiedergesehenes gelöschtes Gerät lässt sich in der Produktion nicht reaktivieren; der Ausweg 'neu' verliert Historie und Beziehungen. Die [B]-Funktion CI-13 ist defekt, damit ist GATE-02 ('alle [B] PASS') verletzt.
- **Ursache (belegt):** Der Indexing-Decorator reicht die optionale Schnittstelle Restore nicht an das PG-Repository durch; discovery/review.go prüft per Type-Assertion auf ciRestorer. resurrect_test.go:96-100 testet das Repository ohne die main.go-Decorator-Kette, daher bleibt der Fehler in der Testsuite unsichtbar.
- **Empfehlung:** Restore (und UniqueConflicts sowie weitere optionale Schnittstellen) im Indexing-Decorator durchreichen; Repo-Verdrahtung aus main.go in eine testbare Funktion extrahieren und per Type-Assertion aller optionalen Schnittstellen sowie über den Router (merge → 200) testen. Gruppe T3/WP-087 informieren.
- **Retest (verbindlich):** cd backend && TEST_DATABASE_URL='postgres://reticora:reticora@localhost:55432/<DB>?sslmode=disable' go test -tags auditdefect -count=1 -v -run 'TestAuditT5CIReactivationThroughProductionDecoratorChain' ./internal/tenant/rls/ → PASS (Variante main.go: 200, deleted_at NULL, Review resolved, GET 200).
- **Kodierender Test:** backend/internal/tenant/rls/audit_t5_sr11_decorator_chain_auditdefect_test.go TestAuditT5CIReactivationThroughProductionDecoratorChain (Branch audit-defects-T5; fails-as-expected, Kontrolle bare-pg PASS).

<a id="f-038"></a>
### F-038 (P1) – Gate-Checkliste führt CI-13 [B] als PASS trotz offenem und reproduziertem Defekt SR-11

- **Betroffene WPs:** WP-213 · **Komponente:** docs/acceptance/g1-gate.md:333 (CI-13 PASS), :38-62 ('G1 … bestanden'); tests/rep01-structure.test.sh (kein Abgleich offener SR-/PT-Befunde mit dem Gate-Status) · **Quelle:** Audit (B27) + Verifikation + Defekttest-Welle T5 (in Synthese wiederholt)
- **Reproduktion:** g1-gate.md:333 '| CI-13 | [B] | PASS | backend/internal/discovery/resurrect_test.go | WP-087 |' bei offenem SR-11 (docs/security/review-g1.md:145) und reproduziertem 501 in der Produktionsverdrahtung (L-T5-05). Kodiert: tests/audit/g1-gate-evidence.test.sh Fall 'known_defect_SR-11: CI-13 is not PASS while SR-11 is open' FAIL; in dieser Synthese erneut ausgeführt (SP/work/synth-T5/g1-gate-evidence.log).
- **Soll:** Nach GATE-02 steht eine [B]-ID mit bekanntem, reproduziertem Produktionsdefekt auf FAIL bzw. blockiert das Gate, oder der Defekt ist vorher behoben.
- **Ist:** CI-13 steht auf PASS, G1 gilt als bestanden.
- **Nutzerwirkung:** G1 wird mit einer defekten [B]-Funktion freigegeben; der Gate-Entscheid ist nicht belastbar.
- **Ursache (belegt):** Der Status je ID wird aus Traceability-Zeilen abgeleitet; offene SR-Befunde werden weder manuell noch maschinell gegen den Gate-Status abgeglichen.
- **Empfehlung:** L-T5-05 beheben; bis dahin CI-13 auf FAIL setzen und das Gate-Ergebnis anpassen; rep01-Abgleich ergänzen: offener SR-/PT-Befund mit genannter Katalog-ID verbietet PASS dieser ID.
- **Retest (verbindlich):** ./tests/audit/g1-gate-evidence.test.sh Fall 'known_defect_SR-11: CI-13 is not PASS while SR-11 is open' ok; tests/rep01-structure.test.sh mit neuem Abgleich grün.
- **Kodierender Test:** tests/audit/g1-gate-evidence.test.sh – known_defect_SR-11 (Assertion 1, fails-as-expected).

<a id="f-039"></a>
### F-039 (P1) – Kein Pentest-Bericht und keine Befundliste im Repository (Lieferung A-07 fehlt)

- **Betroffene WPs:** WP-210 · **Komponente:** docs/security/pentest-g1.md:6-27 (Status), :52-57 (Ablage); externe Aufgabe A-07, Entscheidung E-28 · **Quelle:** Audit (B27) + Verifikation + Defekttest-Welle T5 (in Synthese wiederholt)
- **Reproduktion:** cat docs/security/pentest-g1.md: alle Statuszeilen 'laut Erklärung des Auftraggebers vom 2026-10-15; nachzureichen', :8-13 'Diese Datei enthält keine Angaben aus dem Pentest', keine Zeile '| PT-nn | Severity | Status |' (grep -c 'PT-' = 0). Kodiert: tests/audit/g1-gate-evidence.test.sh Fälle pentest_summary, pentest_findings, pentest_field (Dienstleister, Zeitraum|Termin, Nachtest) FAIL; pentest_open ok nur mangels PT-Zeilen; in dieser Synthese erneut ausgeführt.
- **Soll:** Zusammenfassung (Dienstleister, Zeitraum, Umfang), Befundliste (ID, Severity, Status, Folge-WP) und Nachtest-Ergebnis ohne offene Critical/High sind abgelegt; Abschluss erst danach.
- **Ist:** Nur eine Erklärung des Auftraggebers; keine prüfbaren Pentest-Angaben.
- **Nutzerwirkung:** Die Sicherheitsfreigabe von G1 ist nicht überprüfbar; unbekannte High/Critical sind nicht ausgeschlossen – das Audit selbst fand einen Scope-Bypass (WP-208) und SR-11.
- **Ursache (belegt):** Die externe Lieferung A-07 (Pentest-Dienstleister/Auftraggeber) liegt nicht im Repository vor.
- **Empfehlung:** Zusammenfassung und Befundliste im Format 'Ablage' nachtragen, je Befund ein WP anlegen, Nachtest dokumentieren; bis dahin GATE-03 im Gate als offen bzw. begründete Ausnahme führen (siehe L-T5-10).
- **Retest (verbindlich):** ./tests/audit/g1-gate-evidence.test.sh Fälle pentest_summary, pentest_findings, pentest_field ×3 ok und pentest_open ok mit vorhandenen PT-Zeilen; erweiterte rep01-Prüfung (L-T5-47) grün; Sichtprüfung der Befundliste.
- **Kodierender Test:** tests/audit/g1-gate-evidence.test.sh – pentest_summary, pentest_findings, pentest_field ×3 (fails-as-expected).

<a id="f-040"></a>
### F-040 (P1) – Review und Gate führen TEN-09-Isolation als vollständig und 'kein offenes Critical', obwohl ein Site-Scope-Bypass im Rack-Kanal reproduziert ist

- **Betroffene WPs:** WP-209, WP-213 · **Komponente:** docs/security/review-g1.md (V4 'Scopes je Client/Site/Team, Kanalmatrix', SR-03 '13 Schreibpfade (Standorte, Racks, …)'); docs/acceptance/g1-gate.md:55 und GATE-03-Tabelle ('Isolationstest (TEN-09) vollständig | PASS'); backend/internal/tenant/rls/channel_matrix_integration_test.go (prüft Racks nur als Fremdverweis von Org B, nicht unter Site-Scope) · **Quelle:** Audit (B27) + Verifikation (Rack-Probe) + Defekttest-Welle T5 (in Synthese wiederholt)
- **Reproduktion:** Probe TestVerifyB27RackSiteScope (SP/work/verify-B27/rack-site-scope.log): Principal mit Sites=only(site2) liest site1-Racks (Liste, Detail, Layout, GraphQL) und löscht eines (204). review-g1.md V4/SR-03 zitieren die Kanalmatrix als Beleg für Racks; g1-gate.md:55 'Es gibt kein offenes Critical'. Kodiert: tests/audit/g1-gate-evidence.test.sh Fall rack_site_scope FAIL ('| Isolationstest (TEN-09) vollständig | PASS | …channel_matrix_integration_test.go (WP-208) |'); in dieser Synthese erneut ausgeführt. Produktdefekt selbst: Gruppe T1 (ext:Site-Scope-Bypass im Rack-Kanal: fremde Racks lesbar und löschbar).
- **Soll:** Review prüft die Scope-Isolation je Kanal (TEN-09 × Scope-Arten) und führt den Defekt als neuen Befund mit Severity (Schema: Scope-Isolation Critical); das Gate blockiert GATE-01/GATE-03 oder der Defekt ist vorher behoben.
- **Ist:** Defekt fehlt im Review und im Gate; die Matrixabdeckung wird als Nachweis zitiert; GATE-03 'Isolationstest vollständig' PASS.
- **Nutzerwirkung:** Die Gate- und Review-Freigabe überdeckt eine Scope-Isolationslücke mit Löschwirkung.
- **Ursache (belegt):** Kanalmatrix (WP-208) deckt Racks unter Site-Scope nicht ab; Review und Gate vertrauen der Matrix, ohne Kanalliste × Scope-Arten abzugleichen.
- **Empfehlung:** Neuen Befund SR-12 (Critical bzw. mindestens High) aufnehmen, V4 und SR-03 korrigieren; GATE-03 'Isolationstest vollständig' bis zum Fix (WP-208/T1) nicht auf PASS; nach dem Fix den WP-208-Test referenzieren.
- **Retest (verbindlich):** ./tests/audit/g1-gate-evidence.test.sh Fall rack_site_scope ok (Befund im Review geführt oder TEN-09 nicht PASS); nach dem Produktfix WP-208-Test für Racks unter Site-Scope grün und Gate führt den Befund als behoben.
- **Kodierender Test:** tests/audit/g1-gate-evidence.test.sh – rack_site_scope (fails-as-expected).
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – prioritaet: „P2“ → „P1“. Der Defekt lässt die Gate-Checkliste zwei falsche Aussagen treffen: 'Es gibt kein offenes Critical' (docs/acceptance/g1-gate.md:55, GATE-01 [Q]) und '| Isolationstest (TEN-09) vollständig | PASS |' (g1-gate.md:217). Dabei ist ein Site-Scope-Bypass reproduziert, der fremde Racks lesbar und löschbar macht (SP/work/verify-B27/rack-site-scope.log: site2 GET /racks/{site1 rack} 200, DELETE 204). Nach dem projekteigenen Schema (docs/audit/README.md:14, 'Critical (Mandanten-/Scope-Isolation …)') ist das ein offenes Critical. Gruppe T1 führt den Produktdefekt als P1 (WP-208). Der gleichartige Gate-Defekt L-T5-06 ist P1: CI-13 steht auf PASS trotz des Produktdefekts L-T5-05, der ebenfalls P1 ist. Eine falsche GATE-01-Aussage wiegt mindestens so schwer wie eine falsche GATE-02-Aussage. Die wesentliche Funktion der Gate-Checkliste ist damit falsch, also P1. Folgen: Die Zählung im gruppenfazit wird zu 8 P1 / 21 P2 / 19 P3, WP-209 wird P1 (siehe korrekturen), WP-213 bleibt P1.

<a id="f-041"></a>
### F-041 (P1) – Einladungen versenden keine E-Mail: invitation.created (NTF-05 'Einladung') wird nie erzeugt

- **Betroffene WPs:** WP-114, WP-140 · **Komponente:** backend/internal/user/invitation.go (IssueInvitation/RenewInvitation), notify/subscriptions.go:82-92 · **Quelle:** Real-Stack-E2E (D1) + Verifikation (B14, B17) + Defekttest-Welle
- **Reproduktion:** Real-Stack: POST /notification-channels {provider:central_smtp} 201, POST /invitations 201, Mailpit 60 s → keine Mail (roles-authz.spec.ts Test 05). DB: event_outbox ohne invitation.created, notification_delivery 0.
- **Soll:** Issue und Renew erzeugen invitation.created; Eingeladener erhält Link und Ablaufdatum per Mail.
- **Ist:** Kein Event, keine Mail; Token nur in der API-Antwort an den Admin.
- **Nutzerwirkung:** Eingeladene erfahren nichts; Einladen funktioniert nur durch manuelles Weitergeben des Rohtokens.
- **Ursache (belegt):** IssueInvitation (invitation.go:203-232) und RenewInvitation schreiben Zeile und Audit, aber kein events.Outbox.Append(InvitationCreated); Subscription ist toter Code.
- **Empfehlung:** InvitationCreated (email, organization_name, expires_at, Link aus konfigurierter Accept-URL) in den Issue-/Renew-Transaktionen anhängen; vorher L-T6-epic-b-23 beheben.
- **Retest (verbindlich):** WT: cd backend && TEST_DATABASE_URL=…/audit_db_11 go test -tags auditdefect -count=1 -run 'TestAuditDefectInvitationCreatedEvent' ./internal/user/ → PASS; cd frontend && npx playwright test -c e2e-real/playwright.real.config.ts roles-authz -g '05' → pass.
- **Kodierender Test:** backend/internal/user/invitation_auditdefect_test.go TestAuditDefectInvitationCreatedEvent (fails-as-expected :279,:307); frontend/e2e-real/roles-authz.spec.ts Test 05 (FAIL)

<a id="f-042"></a>
### F-042 (P1) – Manuelle verifizierte Port-Kanten werden bei Discovery-Interface-Entfernung per ON DELETE CASCADE still gelöscht

- **Betroffene WPs:** WP-128 · **Komponente:** backend/migrations/000114_relationship_rel02.up.sql; backend/internal/ipam/pg_repository.go:630-686 · **Quelle:** Verifikation (B16) + Defekttest-Welle
- **Reproduktion:** Interface Gi1/0/1 auf sw1; POST /relationships {sw1→sw2, manual, source_interface_id} 201; 3× SyncDiscoveredInterfaces ohne Gi1/0/1 → Interface und Kante gelöscht, kein ci_change/Audit (SP/work/verify-B16/probe/run-TestB16VManualEdgeInterfaceSync.log).
- **Soll:** REL-05: manuelle Kanten verschwinden nie durch Discovery; Entfernung wird protokolliert.
- **Ist:** Kante per FK-Kaskade in der Discovery-Transaktion gelöscht, spurlos.
- **Nutzerwirkung:** Kuratierte Port-Verbindungen (Impact/Topologie) verschwinden unbemerkt, z. B. bei Interface-Umbenennung.
- **Ursache (belegt):** source/target_interface_id REFERENCES network_interface ON DELETE CASCADE (000114) + missAndRemove DELETE (REC-07, zählt auch manuelle Interfaces).
- **Empfehlung:** FK auf SET NULL (Kollision mit uq_ci_relationship_edge zusammenführen) oder REC-07 überspringt referenzierte Interfaces; Historien-/Audit-Eintrag.
- **Retest (verbindlich):** WT: cd backend && TEST_DATABASE_URL=…/audit_db_11 go test -tags auditdefect -count=1 -run 'TestAuditDefectManualEdge' ./internal/server/ → PASS.
- **Kodierender Test:** backend/internal/server/audit_t6_router_auditdefect_test.go TestAuditDefectManualEdgeSurvivesInterfaceSync (fails-as-expected :89)

<a id="f-043"></a>
### F-043 (P1) – EVT-02-Typen relationship.created, relationship.removed (API), collector.enrolled, review.resolved werden nie erzeugt

- **Betroffene WPs:** WP-136 · **Komponente:** backend/internal/relationship, discovery (Enroll), reviews (Resolve) · **Quelle:** Verifikation (B17) + Defekttest-Welle
- **Reproduktion:** POST /api/v1/relationships 201, DELETE 204 → event_outbox total=0 (SP/work/verify-B17/probes-backend.log TestB17ProbeRelationshipEvents); Enroll und Resolve ohne Event (Defekttest).
- **Soll:** Jede dieser Mutationen schreibt ihr Event in der Mutations-Transaktion.
- **Ist:** Kein Event; Webhook-/NATS-Abonnenten erhalten nichts.
- **Nutzerwirkung:** Integrationen für Beziehungs-, Collector- und Review-Lebenszyklus bleiben still.
- **Ursache (belegt):** Outbox-Produzenten nur für CI (und einige Systemjobs) verdrahtet; server/repositories.go:142 nutzt Plain-PG-Repo ohne Events.
- **Empfehlung:** Events in Relationship-Create/Delete, Collector-Enroll und Review-Resolve-Transaktionen anhängen; Test je Typ.
- **Retest (verbindlich):** WT: cd backend && TEST_DATABASE_URL=…/audit_db_11 go test -tags auditdefect -count=1 -run 'TestAuditDefectDomainEvents' ./internal/server/ → PASS.
- **Kodierender Test:** backend/internal/server/audit_t6_router_auditdefect_test.go TestAuditDefectDomainEventsEmitted (fails-as-expected :115,:137,:157,:179)

<a id="f-044"></a>
### F-044 (P1) – Entitlement-Warnung/-Ablauf und Zertifikatsablauf: JOB-02-Läufe nicht eingeplant, Events entitlement.warning/expired und certificate.expiring nie erzeugt

- **Betroffene WPs:** WP-131, WP-136, WP-140 · **Komponente:** cmd/server/main.go:476-515 (systemJobs), Entitlement- und Zertifikatsdomäne · **Quelle:** Verifikation (B16, B17)
- **Reproduktion:** main.go:476-515 enthält keinen Entitlement-/Zertifikats-Kind; grep Produzenten von entitlement.warning/entitlement.expired/certificate.expiring → nur envelope.go:59-72, notify/subscriptions.go:60,71,138, webhook.go:134-137.
- **Soll:** JOB-02: Scheduler führt Entitlement-Warnungen und Zertifikatserneuerung aus; NTF-05/EVT-02 liefern die Ereignisse.
- **Ist:** Keine Läufe, keine Events; Benachrichtigungskategorien feuern nie.
- **Nutzerwirkung:** Keine Warnung vor Lizenzablauf/-limit oder ablaufenden Collector-Zertifikaten; Discovery kann unangekündigt stoppen.
- **Ursache (belegt):** Scheduler-Kinds und Event-Produzenten nie implementiert/registriert (Collector-seitige Erneuerung discovery/ca.go:443 ersetzt sie nicht).
- **Empfehlung:** Scheduler-Kinds für entitlement.warning/expired und certificate.expiring (Dedup je Org) implementieren und registrieren; Wiring-Test der Kinds.
- **Retest (verbindlich):** Neue PG-Tests: Entitlement valid_until=now+7d → genau eine entitlement.warning je Org, idempotent; Zertifikat nach Erneuerungspunkt → certificate.expiring; Wiring-Test Kinds ⊇ JOB-02-Liste.
- **Kodierender Test:** keiner (laut Welle nicht kodierbar ohne Produktionscode)

<a id="f-045"></a>
### F-045 (P1) – Werte mit Anführungszeichen oder Steuerzeichen (Zeilenumbruch, Tab) brechen jeden auditierten Write mit Wert-Snapshot (CI-Create/-Update/-Delete/-Restore, Metamodell) mit 400 ab: audit_entry_hash castet den JSON-Text nach bytea

- **Betroffene WPs:** WP-117, WP-120, WP-132, WP-133 · **Komponente:** backend/migrations/000118_audit_fields.up.sql:39-53 (audit_entry_hash, auch 000117:55, 000039:41); ci/pg_repository.go recordAudit · **Quelle:** Real-Stack-E2E (ci-lifecycle L14) + Ursachenverifikation in der adversarialen Prüfung (SP/work/review-T6-epic-b/verify-L12-bytea.log)
- **Reproduktion:** POST /api/v1/cis {name:'probe \"Rack 7\"', ci_type_id:<uuid>} → 400; PATCH model 'R \"7\"' bzw. Edit-Formular → 400; Apostroph, Backslash, Umlaut → 201 (e2e-real ci-lifecycle L14, final.log Traces 720663df…, e05d12e9…).
- **Soll:** Textwert wird gespeichert (CI-03, MET-11); Audit-Kette nimmt jeden Wert auf (AUD-04).
- **Ist:** 400 'a field value has an invalid format or is out of range'; nichts gespeichert, Editor bleibt offen.
- **Nutzerwirkung:** CI-Namen und -Felder mit Anführungszeichen oder Zeilenumbruch/Tab (z. B. Zollangaben im Modellnamen, mehrzeilige multiline-Attribute) sind weder anleg- noch editierbar; der Nutzer sieht nur einen generischen 400. CIs, die solche Werte schon enthalten, sind über API und UI nicht mehr änderbar, löschbar oder wiederherstellbar (before-Snapshot; Codepfad). Metamodell-Änderungen mit solchen Texten scheitern ebenso. Backslash-Werte werden zwar gespeichert, aber nicht AUD-04-konform gehasht.
- **Ursache (belegt):** BELEGT: audit_entry_hash (000039:41, 000117:55, 000118:53) baut das kanonische JSON als Text und castet es mit ::bytea. Der Text→bytea-I/O-Cast deutet Backslash-Sequenzen als bytea-Escape. Die JSON-Escapes \", \n, \t und \uXXXX sind ungültig (22P02), und \\ wird zu einem Byte zusammengezogen: Bei 'a\b' werden 14 statt 15 Bytes gehasht (same_digest=f), der Hash ist also nicht SHA-256(kanonisches JSON) nach AUD-04 und weicht von audit.ComputeHash ab. Der Trigger audit_log_chain rechnet BEFORE INSERT. Der CI-Recorder schreibt den vollständigen Item-Snapshot (ci/pg_repository.go:541, 722, 903 before/after, 976 before, 1056, 1775; merge.go:392, 748; produktiv mit Recorder laut server/repositories.go:140). Der Audit-Fehler rollt deshalb die fachliche Transaktion zurück.
- **Empfehlung:** Verifizieren; dann convert_to(text,'UTF8') statt ::bytea, Kettenprüfung identisch hashen, Migrationstest für Bestandsketten.
- **Retest (verbindlich):** cd frontend && npx playwright test -c e2e-real/playwright.real.config.ts ci-lifecycle -g 'L14' → pass. Dazu ein PG-Test: CI-Create, -PATCH und -DELETE mit Anführungszeichen, LF und TAB im Namen und in einem multiline-Attribut → 201/200/204; audit.Verify intakt; für einen Wert mit Backslash gleicht der gespeicherte Hash audit.ComputeHash. Außerdem verify-bytea.sql/verify-bytea2.sql gegen die korrigierte Funktion ohne ERROR und mit same_digest=t.
- **Kodierender Test:** frontend/e2e-real/ci-lifecycle.spec.ts L14 (FAIL)
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – ursache_belegt: „false“ → „true“. Reproduziert, Repository unverändert. Der Funktionskörper von audit_entry_hash wurde wörtlich aus backend/migrations/000118_audit_fields.up.sql (HEAD 219713f) extrahiert und auf einem Wegwerf-Cluster PostgreSQL 16.15 + pgcrypto ausgeführt (Cluster danach gelöscht). Klartext, Apostroph, Umlaut und Backslash liefern einen Hash. Ein Anführungszeichen im Wert, LF, TAB, ein Anführungszeichen in request_id und ein verschachtelter before/after-Snapshot mit LF liefern jeweils 'ERROR: invalid input syntax for type bytea'. dberrors.go:17,64 mappt 22P02 auf 400 'a field value has an invalid format or is out of range'; das ist exakt die Antwort aus ci-lifecycle L14 (final.log:380,396). Log: /tmp/claude-0/-home-user-Reticora-CMDB/fbd8a132-b968-552a-84eb-515fd3e8f807/scratchpad/work/review-T6-epic-b/verify-L12-bytea.log; Eingaben: …/scratchpad/verify-bytea.sql und verify-bytea2.sql.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – ursache: „HYPOTHESE (nicht verifiziert): (… || '}')::bytea interpretiert JSON-Escape \" als ungültige bytea-Escape-Sequenz (22P02), Audit-Insert rollt CI-Transaktion zurück. Code-Stelle 000118:53 bei Synthese gesichtet; adversariale Verifikation steht aus.“ → „BELEGT: audit_entry_hash (000039:41, 000117:55, 000118:53) baut das kanonische JSON als Text und castet es mit ::bytea. Der Text→bytea-I/O-Cast deutet Backslash-Sequenzen als bytea-Escape. Die JSON-Escapes \", \n, \t und \uXXXX sind ungültig (22P02), und \\ wird zu einem Byte zusammengezogen: Bei 'a\b' werden 14 statt 15 Bytes gehasht (same_digest=f), der Hash ist also nicht SHA-256(kanonisches JSON) nach AUD-04 und weicht von audit.ComputeHash ab. Der Trigger audit_log_chain rechnet BEFORE INSERT. Der CI-Recorder schreibt den vollständigen Item-Snapshot (ci/pg_repository.go:541, 722, 903 before/after, 976 before, 1056, 1775; merge.go:392, 748; produktiv mit Recorder laut server/repositories.go:140). Der Audit-Fehler rollt deshalb die fachliche Transaktion zurück.“. Die Ursache ist durch den ausgeführten SQL-Lauf und die Codepfade belegt.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – titel: „Werte mit doppeltem Anführungszeichen brechen CI-Create/-Update (400) – Hypothese: audit_entry_hash castet JSON-Text nach bytea“ → „Werte mit Anführungszeichen oder Steuerzeichen (Zeilenumbruch, Tab) brechen jeden auditierten Write mit Wert-Snapshot (CI-Create/-Update/-Delete/-Restore, Metamodell) mit 400 ab: audit_entry_hash castet den JSON-Text nach bytea“. Die Hypothese ist bestätigt. Der Umfang ist größer als im Titel: Steuerzeichen und alle Recorder mit Wert-Snapshot sind betroffen.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – prioritaet: „P2“ → „P1“. Nach Definition ist eine wesentliche Funktion unzuverlässig, nicht nur eingeschränkt. (1) Anführungszeichen sind in IT-Inventardaten alltäglich (Zollangaben wie 19-Zoll-Rack oder 24-Zoll-Monitor in Modellnamen). (2) Der Phase-1-Datentyp multiline (MET-11, DynamicForm.tsx:104) kann keine Zeilenumbrüche speichern, ist für seinen Zweck also unbrauchbar. (3) Weil Update, Delete und Restore den vollständigen before-Snapshot auditieren (pg_repository.go:903, 976), lässt sich ein CI, das einen solchen Wert schon enthält, über API und UI weder bearbeiten noch löschen noch wiederherstellen (Codepfad). Solche Werte gelangen etwa über Discovery hinein, die laut L-T6-epic-b-11 mehrzeilige hostname-Werte speichert. (4) Metamodell-Änderungen mit solchen Texten scheitern ebenso. Ein P0 liegt nicht vor, weil nur Eingaben mit diesen Zeichen betroffen sind.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – nutzerwirkung: „CI-Namen/-Felder mit '"' (z. B. Racklabel, Modellname) weder anleg- noch editierbar; vermutlich alle auditierten Writes mit '"' oder Steuerzeichen betroffen.“ → „CI-Namen und -Felder mit Anführungszeichen oder Zeilenumbruch/Tab (z. B. Zollangaben im Modellnamen, mehrzeilige multiline-Attribute) sind weder anleg- noch editierbar; der Nutzer sieht nur einen generischen 400. CIs, die solche Werte schon enthalten, sind über API und UI nicht mehr änderbar, löschbar oder wiederherstellbar (before-Snapshot; Codepfad). Metamodell-Änderungen mit solchen Texten scheitern ebenso. Backslash-Werte werden zwar gespeichert, aber nicht AUD-04-konform gehasht.“. Folgt aus der bestätigten Ursache und den Codepfaden.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – retest: „cd frontend && npx playwright test -c e2e-real/playwright.real.config.ts ci-lifecycle -g 'L14' → pass; PG-Test: CI mit '"' im Namen 201 und audit.Verify intakt.“ → „cd frontend && npx playwright test -c e2e-real/playwright.real.config.ts ci-lifecycle -g 'L14' → pass. Dazu ein PG-Test: CI-Create, -PATCH und -DELETE mit Anführungszeichen, LF und TAB im Namen und in einem multiline-Attribut → 201/200/204; audit.Verify intakt; für einen Wert mit Backslash gleicht der gespeicherte Hash audit.ComputeHash. Außerdem verify-bytea.sql/verify-bytea2.sql gegen die korrigierte Funktion ohne ERROR und mit same_digest=t.“. Das Retest-Kriterium muss alle belegten Fehlerklassen abdecken, nicht nur das Anführungszeichen.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – quelle: „Real-Stack-E2E (ci-lifecycle, Urteil unverified)“ → „Real-Stack-E2E (ci-lifecycle L14) + Ursachenverifikation in der adversarialen Prüfung (SP/work/review-T6-epic-b/verify-L12-bytea.log)“. Neue, ausgeführte Evidenz.

<a id="f-046"></a>
### F-046 (P1) – Collector-Metrik-Uploads (Sweep, UPS, SNMP-Poll) werden ohne Credential gesendet, mit 401 abgewiesen und erreichen den Server nie

- **Betroffene WPs:** WP-190 · **Komponente:** collector/collectorcmd (collectorcmd.go, edge.go); backend/internal/middleware/auth.go; backend/internal/monitoring · **Quelle:** Verifikation (B24, Real-Stack-curl und Collector-Log) + Defekttest-Welle
- **Reproduktion:** (a) Real-Stack: curl -X POST mit nur Content-Type, X-Organization-ID, X-Collector-ID und einem sweep_reachable-Sample an http://localhost:8080/api/v1/monitoring/metrics → 401 'missing bearer token'. (b) Realer Collector-Log /tmp/reticora-e2e-discovery-SuPITb/collector.log: HTTP-Uploads 'upload failed with status 401', Edge-Heartbeat 204. (c) TestAuditT7MetricSamplesReachTheServerAuthenticated: 'edge requests [], HTTP [/api/v1/monitoring/metrics] accepted=0 refused(401)=1, uploadMetricSamples error=metrics upload failed with status 401'.
- **Soll:** MON-02/WP-190: Collector-Samples (Sweep-Erreichbarkeit, UPS/PDU, Interface-Zähler) werden angenommen (202), mit ci_id gespeichert und auf der CI-Detailseite als Metriklinie gezeigt.
- **Ist:** 401; uploadSamples protokolliert nur 'metric sample upload failed' (collectorcmd.go:1625-1632) und verwirft die Samples; es gibt kein Edge-Subject für Metriken.
- **Nutzerwirkung:** In realen Installationen existieren keine MON-02-Basismetriken; die Metriklinie im CI-Detail bleibt immer leer.
- **Ursache (belegt):** postMetricsPayload (collectorcmd.go:1068-1115) setzt nur Content-Type, X-Organization-ID und X-Collector-ID, kein Authorization; Ingest/Heartbeat laufen über den mTLS-NATS-Edge (edge.go:61,86), Metriken nicht (edge.go:22-23; backend discovery/edge_ingest.go:23-24); die HTTP-API verlangt für jeden /api/-Pfad ein Bearer-Token (middleware/auth.go:295-331), keine Client-Zertifikats-Authentisierung. Die Collector-Tests posten an einen unauthentisierten httptest-Server (schedule_test.go:133).
- **Empfehlung:** '.metrics'-Edge-Subject serverseitig unter der Zertifikatsidentität des Collectors (Org aus dem Zertifikat) verarbeiten oder ein Ingest-Credential als Bearer ausstellen; 401 als Fehler/Metrik sichtbar machen und spoolen; Real-Collector-E2E.
- **Retest (verbindlich):** cd collector && go test -tags auditdefect -count=1 -v -run '^TestAuditT7MetricSamplesReachTheServerAuthenticated$' ./collectorcmd/ → PASS; zusätzlich Real-Stack mit simuliertem Collector: nach einem Sweep enthält metric_sample Zeilen mit ci_id der simulierten CIs, GET /api/v1/cis/{id}/metrics liefert sweep_reachable-Punkte, Collector-Log ohne 'metric sample upload failed'.
- **Kodierender Test:** collector/collectorcmd/audit_t7_metrics_upload_auditdefect_test.go:39 TestAuditT7MetricSamplesReachTheServerAuthenticated – FAILS_AS_EXPECTED (SP/verify-tests-t357/results.tsv)

<a id="f-047"></a>
### F-047 (P2) – Handler geben rohe pgx/PostgreSQL-Fehlertexte (SQLSTATE, Constraint-, Tabellen-, RLS-Policy-Namen) und falsche Statuscodes zurück

- **Betroffene WPs:** WP-010, WP-011, WP-012, WP-014, WP-015, WP-016, WP-017, WP-018, WP-020, WP-021, WP-208 · **Komponente:** Handler in relationshiptype, citype, lifecycle, override, privacy, user, permission, rack, savedview, search, monitoring, reservation, asset, movement, compliance, ticket, sla, form, workflow, iga · **Quelle:** Audit (B02/B03/B27) + Verifikation + Defekttest-Welle
- **Reproduktion:** 32 Fälle über den vollständigen PG-Router (TestAuditT1RepositoryErrorsAreSanitized, backend/internal/tenant/rls/audit_t1_error_sanitization_auditdefect_test.go:80-126), u.a.: POST /api/v1/relationship-types Duplikat; POST /api/v1/ci-types Name mit NUL; POST /api/v1/lifecycle-definitions Duplikat bzw. applies_to 'no-such-kind'; PUT /api/v1/reconciliation/source-policy NUL; PUT /api/v1/privacy/retention retention_days 3000000000; GET /api/v1/teams/not-a-uuid/members; doppeltes Team-Mitglied; POST/PUT/PATCH/DELETE /api/v1/roles/not-a-uuid…; POST /api/v1/racks/{id}/mounts mit ungültiger, unbekannter bzw. fremder ci_id; Saved-View-Duplikat; POST /api/v1/search/query upstream_of 'not-a-uuid'; GET /api/v1/search?q=a%00b; POST /api/v1/monitoring/alerts mit NUL bzw. duration 9223372036854775807; POST /api/v1/reservations unbekanntes Asset; Release ungültige bzw. fremde Reservation; PATCH /api/v1/assets/not-a-uuid; POST /api/v1/stock-movements, /inventory/items mit unbekannten FKs; POST /api/v1/compliance/rules unbekannter ci_type; Ticket-Kommentar/SLA mit ungültiger id; POST /api/v1/forms für Client 2 durch Client 1; GET /api/v1/workflow-runs?workflow_id=not-a-uuid; POST /api/v1/iga/access-requests unbekanntes Subjekt. Zusätzlich (WP-208): POST /api/v1/teams/{T2}/members durch Team-Principal (TestVerifyB27TeamScope).
- **Soll:** RFC-7807-Antworten über api.WriteRepoError bzw. feste Domänenmeldungen: 400/404/409/422 je Fall, 403/404 generisch bei RLS-Verstoß; keine SQLSTATE-, Tabellen-, Constraint-, Policy- oder Treiber-Texte (verbindliches AK2 der WPs 010–012, 014–018, 020, 021; API-03).
- **Ist:** Alle 32 Subtests FAIL, z.B. '409 create relationship type: ERROR: duplicate key value violates unique constraint idx_relationship_type_org_key (SQLSTATE 23505)'; '500 ... new row violates row-level security policy for table form_def (SQLSTATE 42501)'; '400 validation failed: ... row-level security policy for table rack_mount (SQLSTATE 42501)'; '409 create alert rule: can't scan into dest[6] ...'; '400 failed to encode args[1]: unable to encode 3000000000 into binary format for int4 (OID 23)'; Release einer unsichtbaren Reservation 409 statt 404 (neuer Wellenbefund, gleiche Ursache); Team-Mitglied 404 mit 'new row violates row-level security policy for table team_member (SQLSTATE 42501)'.
- **Nutzerwirkung:** Offenlegung von Schema-, Constraint-, RLS-Policy- und Treiberinterna an jeden Nutzer mit Schreibrecht (bei Cross-Client-Versuchen inkl. Policy-Namen); irreführende Statuscodes (409 für FK-/Syntaxfehler, 404 für Duplikate, 500 für Client-Fehler) in UI und Monitoring.
- **Ursache (belegt):** Handler schreiben err.Error() direkt per api.WriteError statt api.WriteRepoError, Repositories wrappen rohe pgx-Fehler, UUID-/NUL-/Wertebereichsvalidierung fehlt. Belegte Stellen: relationshiptype/handler.go writeError (Default); citype/handler.go:320-324; lifecycle/handler.go Create, :165-167; override/handler.go:176-178; privacy/handler.go:68-71, :87-90; user/handler.go:532-533 und weitere Team-/Rollen-Handler; permission/handler.go:95-98; rack/pg_repository.go:557-567 + rack/handler.go:65-77; savedview/handler.go:136-137, :222-225 + filter.go:123-150; search/handler.go:37-41; monitoring/monitoring.go:585-588; reservation/handler.go:161-163, :176-179; asset/handler.go:257-259; movement/handler.go:150-152, :221-223; compliance/handler.go:54,91,131,144,156,176; ticket/handler.go:250-252; sla/handler.go:143-146; form/handler.go:45,85,127,141,195; workflow/handler.go:44,81,150; iga/handler.go:142,270,287,322,371,460. Die Migrations-Commits (u.a. deaf5d3, a413ce0, ea88c47, 035b3e6) haben diese Handler nicht geändert.
- **Empfehlung:** Querschnittskorrektur: bekannte Sentinels explizit abbilden, alle übrigen Repository-Fehler über api.WriteRepoError (42501 -> 403/404 generisch, 23503 -> 422, 23505 -> 409, 22P02/22021 -> 400); UUID-, NUL- und Wertebereichsvalidierung vor SQL; Lint-/Testregel gegen err.Error() in Repository-Fehlerpfaden.
- **Retest (verbindlich):** cd backend && TEST_DATABASE_URL='postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable' go test -tags auditdefect -count=1 -v -run '^TestAuditT1RepositoryErrorsAreSanitized$' ./internal/tenant/rls/ muss PASS liefern (alle 32 Subtests mit erwartetem Status, Bodies ohne 'SQLSTATE', 'ERROR:', Constraint-/Policy-/Tabellennamen); Domänenmeldungen (z.B. 'asset already has an active reservation') bleiben; Router-Probes (SP/work/verify-B02/router-probe.log, SP/work/verify-B03/probes-all.log) ohne Leck; Team-Mitglied-Schreiben liefert generisches 404.
- **Kodierender Test:** backend/internal/tenant/rls/audit_t1_error_sanitization_auditdefect_test.go TestAuditT1RepositoryErrorsAreSanitized (FAILS_AS_EXPECTED, 32/32 Subtests FAIL, SP/verify-tests-run2/defect-TestAuditT1RepositoryErrorsAreSanitized.log); Probes TestVerifyB02RouterProbe, TestVerifyB03*, TestVerifyB27TeamScope.

<a id="f-048"></a>
### F-048 (P2) – Unverlinkte (org-weite) Dokumente und Wartungsfenster durch client-gescopte Principals anleg-, änder-, lösch- und kaperbar (E-09)

- **Betroffene WPs:** WP-019, WP-020, WP-028 · **Komponente:** Migrationen 000064:275-287/000079; document- und maintenance-Handler/Repositories · **Quelle:** Verifikation (B04) + Defekttest-Welle
- **Reproduktion:** Client-1-Principal: PATCH /api/v1/documents/{orgWideDoc} -> 200; POST /api/v1/documents ohne Link -> 201; POST /api/v1/documents/{orgWideDoc}/links auf eigenes CI -> 201, danach GET durch Client 2 -> 404; DELETE des org-weiten Dokuments; POST /api/v1/maintenance-windows ohne CIs -> 201. Kodiert in TestAuditT1DocumentWritesOutsideScope (Subtest 'org-wide unlinked document') und TestAuditT1MaintenanceWindowWritesOutsideScope (Subtest 'org-wide window created by a client-scoped principal (E-09)').
- **Soll:** E-09/TEN-05: ein client-gescopter Principal darf org-weite Zeilen lesen, aber nicht anlegen, ändern oder löschen; Verlinken entzieht anderen Clients die Sicht nicht.
- **Ist:** 'PATCH as client 1: 200' (org-weites Dokument umbenannt); 'GET the org-wide document as client 2 after client 1 linked it: 404, want 200'; 'POST an org-wide window (no CIs) as client 1: 201, want refused'.
- **Nutzerwirkung:** Client-gescopte Nutzer können org-weite Handbücher/Richtlinien umbenennen oder löschen, org-weite Dokumente und Fenster anlegen und anderen Clients geteilte Dokumente durch Verlinken entziehen.
- **Ursache (belegt):** Das Mehrobjekt-Policy-Design behandelt 'keine Links' (link_count = 0) als org-weit; WITH CHECK und die restriktiven Policies prüfen nur die Org; die E-09-Schreibbeschränkung fehlt für diese Zeilen.
- **Empfehlung:** Restriktive INSERT/UPDATE/DELETE-Policies für gescopte Principals: link_count > 0 und alle Links sichtbar; Verlinken org-weiter Dokumente nur durch org-weite Principals – oder PO-Entscheidung zur beabsichtigten Semantik.
- **Retest (verbindlich):** Dieselben Defekttests (TestAuditT1DocumentWritesOutsideScope, TestAuditT1MaintenanceWindowWritesOutsideScope, -tags auditdefect, ./internal/tenant/rls/) mit den org-weiten Subtests PASS: Client-1 PATCH/DELETE eines org-weiten Dokuments 403/404, POST ohne Link verweigert, Link auf org-weites Dokument verweigert; Org-Admin-Positivkontrolle 200.
- **Kodierender Test:** TestAuditT1DocumentWritesOutsideScope, TestAuditT1MaintenanceWindowWritesOutsideScope (FAILS_AS_EXPECTED); Probes probe_doc_link_count.sql Teil C, TestB04VerifierProbe.

<a id="f-049"></a>
### F-049 (P2) – Team-Scope kann nie vergeben werden: Rollenzuweisungen ohne Team-Dimension, app.team_scope für reale Nutzer immer 'alle'

- **Betroffene WPs:** WP-009 · **Komponente:** Schema role_assignment/user_custom_role; backend/internal/permission/pg_repository.go:165-227 (AccessGrants); backend/internal/identity/principal.go (unionDimension) · **Quelle:** Audit (B02) + Verifikation + Defekttest-Welle
- **Reproduktion:** POST /api/v1/role-assignments {scope_type: 'team', scope_id: <T1>} über den PG-Router; Auflösung des Team-Scopes eines Nutzers mit Team-Zuweisung. psql: role_assignment hat nur scope_client_id/scope_site_id, user_custom_role CHECK scope_type IN ('organization','client','site'). Kodiert in TestAuditT1TeamScopedAssignment (backend/internal/tenant/rls/audit_t1_team_scope_auditdefect_test.go).
- **Soll:** RBA-03: role_assignment(role, scope_kind org|client|site|team, scope_id); eine team-gescopte Zuweisung schränkt app.team_scope auf genau dieses Team ein (CH25, TEN-05).
- **Ist:** 'POST /role-assignments with scope_type team answered 404 unsupported scope_type team, want 201'; 'the resolved team scope of a user assigned for team 1 is {All:false IDs:[]} (grants []), want exactly team 1'. Produktiv gilt Teams=All für jeden Nutzer mit Grants.
- **Nutzerwirkung:** Team-basierte Einschränkung (TEN-05/CH25 und die Team-Prädikate aus WP-029) ist produktiv nicht nutzbar; Nutzer, die nur ihr Team sehen sollen, sehen alle Team-Zeilen ihres Client-/Site-Scopes.
- **Ursache (belegt):** Schema ohne Team-Scope-Spalte/-Art; AccessGrants/ServiceAccountGrants befüllen Grant.Teams nie; WP-009 plante 'keine Migration'.
- **Empfehlung:** PO-Entscheidung E-11 einholen; scope_team_id bzw. scope_kind/scope_id an role_assignment, 'team' in user_custom_role.scope_type, Grant.Teams befüllen, Tests ergänzen.
- **Retest (verbindlich):** cd backend && TEST_DATABASE_URL='postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable' go test -tags auditdefect -count=1 -v -run '^TestAuditT1TeamScopedAssignment$' ./internal/tenant/rls/ muss PASS liefern (201, aufgelöster Team-Scope = {T1}); Ticket-Sicht nur T1 plus team-lose Zeilen (read-only).
- **Kodierender Test:** TestAuditT1TeamScopedAssignment (FAILS_AS_EXPECTED, SP/verify-tests-run2/defect-TestAuditT1TeamScopedAssignment.log).

<a id="f-050"></a>
### F-050 (P2) – Abgeleiteter Kanten-Scope veraltet, wenn ein client- oder site-gescopter Principal ein CI verschiebt

- **Betroffene WPs:** WP-025 · **Komponente:** Migration 000061:131-148 (propagate_ci_scope, SECURITY INVOKER); savedview/filter.go:117 (has_relationship ohne Endpunktprüfung) · **Quelle:** Verifikation (B04) + Defekttest-Welle
- **Reproduktion:** CIs X (c1) und Y (c3), Kante X->Y org-weit angelegt. Principal mit Clients c1,c2 verschiebt X nach c2 per WithTenant; Beobachter mit Clients c1,c3 liest ci_relationship. Analog Site (s1->s2, Beobachter s1,s3). Kodiert in TestAuditT1EdgeScopeFollowsScopedMove (backend/internal/tenant/rls/audit_t1_scope_derivation_auditdefect_test.go).
- **Soll:** source_/target_-Scope entspricht stets dem Scope des Endpunkt-CI (WP-025 AK2); die Zwei-Endpunkt-Policy (AK3) verbirgt Kanten zu unsichtbaren CIs.
- **Ist:** 'edge source_client_id after the move = ...c1, want client 2'; 'observer clients 1+3 sees 1 edges naming the invisible CI X, want 0'; 'edge source_site_id after the move = <s1>, want site 2'; 'observer sites 1+3 sees 1 edges naming the invisible CI X'. Kontrolle: org-weite Verschiebung leitet korrekt neu ab.
- **Nutzerwirkung:** Eine DB-Isolationsgarantie bricht: Principals sehen Kanten mit IDs von CIs, die sie nicht sehen dürfen (z.B. über has_relationship-Filter); Listen- und Traversal-APIs maskieren das per Go-Endpunktprüfung.
- **Ursache (belegt):** Die Propagationstrigger laufen unter der RLS des Verschiebenden; die Kante zum Endpunkt außerhalb seines Scopes ist für ihn unsichtbar und wird übersprungen.
- **Empfehlung:** Propagation (bzw. Ableitung) als SECURITY DEFINER mit festem search_path und Org-Guard, oder Scope-Wechsel bei Kanten zu Out-of-Scope-CIs verbieten; Test mit gescoptem Verschieber.
- **Retest (verbindlich):** cd backend && TEST_DATABASE_URL='postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable' go test -tags auditdefect -count=1 -v -run '^TestAuditT1EdgeScopeFollowsScopedMove$' ./internal/tenant/rls/ muss PASS liefern (Kanten-Scope folgt c2/s2, Beobachter sieht 0 Kanten); Probes probe_edge_stale_after_move.sql und probe_edge_stale_site.sql bestätigen.
- **Kodierender Test:** TestAuditT1EdgeScopeFollowsScopedMove (FAILS_AS_EXPECTED); Probes probe_edge_stale_after_move.sql, probe_edge_stale_site.sql.

<a id="f-051"></a>
### F-051 (P2) – Metrik-Views nur org-begrenzt: metric_sample ohne Client-/Site-Scope auf DB-Ebene

- **Betroffene WPs:** WP-025, WP-039, WP-040 · **Komponente:** Migration 000071 (metric_sample_v, metric_sample_1h_v); docs/decisions/0001-timescale-rls.md Punkt 4 (Z. 98-99) · **Quelle:** Audit (B04/B05) + Verifikation + Defekttest-Welle
- **Reproduktion:** Samples für CIs von Client 1 und Client 2; ein Client-1- bzw. Site-1-Principal liest per WithTenant ohne CI-Filter aus metric_sample_v und metric_sample_1h_v. Kodiert in TestAuditT1MetricViewsHonourScope (backend/internal/monitoring/audit_t1_metric_scope_auditdefect_test.go).
- **Soll:** WP-039 AK1 (Zugriff auf metric_sample_1h über die ADR-Variante org- und scope-begrenzt), WP-025 AK1 (metric_sample mit abgeleiteter client_id/site_id), TEN-05.
- **Ist:** 'client1 selects 2 rows of the client-2 CI from metric_sample_v, want 0 (also metric_sample_1h_v, and for site1)'; pg_views zeigt nur 'organization_id = app.org_id'.
- **Nutzerwirkung:** Keine DB-seitige Defense-in-Depth: der Scope hängt an Prädikaten in jeder einzelnen Store-Abfrage; eine fehlende (L-T1-10) führt direkt zum Leck, neue Lesepfade (Export, Report) wären ungeschützt.
- **Ursache (belegt):** ADR 0001 Punkt 4 stellt Client/Site-Prädikate zurück; 000061 hat metric_sample nicht denormalisiert; die Barrier-Views filtern nur auf die Org.
- **Empfehlung:** ci-Sichtbarkeits-Subselect (oder denormalisierte client_id/site_id) in beide Views, Kosten messen, ADR ergänzen, Spike-Test um client- und site-gescopte Principals erweitern – oder PO-akzeptierte dokumentierte Ausnahme.
- **Retest (verbindlich):** cd backend && TEST_DATABASE_URL='postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable' go test -tags auditdefect -count=1 -v -run '^TestAuditT1MetricViewsHonourScope$' ./internal/monitoring/ muss PASS liefern; TestAuditT1MetricReadPathsHonourScope und TestMetricStoreUsesBarrierViews bleiben grün.
- **Kodierender Test:** TestAuditT1MetricViewsHonourScope (FAILS_AS_EXPECTED); positiv: TestAuditT1MetricReadPathsHonourScope (PASS, Store-Query-Pfade gescopt).

<a id="f-052"></a>
### F-052 (P2) – Metrik-Serienamen eines fremden Client-CI über GET /api/v1/cis/{id}/metrics sichtbar

- **Betroffene WPs:** WP-016, WP-040 · **Komponente:** backend/internal/monitoring/pg_store.go:230-236 (MetricNames), backend/internal/monitoring/monitoring.go:836-872 (CIMetrics) · **Quelle:** Audit (B05) + Verifikation + Defekttest-Welle
- **Reproduktion:** CI von Client 2 mit Metrik 't1_secret_metric'; Client-1-Principal über den vollständigen PG-Router: GET /api/v1/cis/{id} -> 404, GET /api/v1/cis/{id}/metrics -> 200. Store-Ebene: MetricNames(Client-2-CI) als client1/site1. Kodiert in TestAuditT1CIMetricsEndpointScope (backend/internal/tenant/rls/audit_t1_scope_derivation_auditdefect_test.go) und TestAuditT1MetricNamesHonourScope (backend/internal/monitoring/audit_t1_metric_scope_auditdefect_test.go).
- **Soll:** 404 oder leere Serienliste für ein CI außerhalb des Client-Scopes (TEN-06: ein falscher Client-Scope liefert 0 Zeilen).
- **Ist:** 'GET /cis/{client-2 CI}/metrics as client1: 200 and discloses the series name: {... series:[{name: t1_secret_metric, points:[]}]}'; 'client1 MetricNames(client-2 CI) = [t1_cpu t1_secret_series], want none (same for site1)'.
- **Nutzerwirkung:** Ein client-gescopter Nutzer, der eine CI-UUID eines anderen Clients kennt, kann dessen Existenz bestätigen und Metriknamen aufzählen (Metadaten-Leck innerhalb der Organisation).
- **Ursache (belegt):** MetricNames (eingeführt mit WP-190, Commit d80d173) fehlt das Prädikat 'ci_id IN (SELECT id FROM ci)' aus metricWhere (pg_store.go:189-191); CIMetrics prüft die CI-Sichtbarkeit nicht; die Views sind nur org-gescopt (L-T1-09).
- **Empfehlung:** Sichtbarkeitsprädikat in MetricNames; CIMetrics liefert 404 für unsichtbare CIs; Metrics-Kanal in TestChannelMatrix; tabellengetriebener Scope-Test über alle Store-Lesepfade.
- **Retest (verbindlich):** cd backend && TEST_DATABASE_URL='postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable' go test -tags auditdefect -count=1 -v -run '^TestAuditT1CIMetricsEndpointScope$' ./internal/tenant/rls/ sowie -run '^TestAuditT1MetricNamesHonourScope$' ./internal/monitoring/ müssen PASS liefern; ein org-weiter Principal sieht die Namen weiterhin.
- **Kodierender Test:** TestAuditT1CIMetricsEndpointScope, TestAuditT1MetricNamesHonourScope (FAILS_AS_EXPECTED); Probes TestProbeV05CIMetricsFullRouter, TestProbeB05MetricNamesClientScope.

<a id="f-053"></a>
### F-053 (P2) – Assets, Rooms und Racks haben einen Standortbezug, aber keine site_id; site-gescopte Principals sehen sie in allen Sites

- **Betroffene WPs:** WP-027 · **Komponente:** Schema asset/room/rack (cable); backend/internal/asset/pg_repository.go (kein Site-Prädikat); search/scope_integration_test.go:96-104 · **Quelle:** Verifikation (B04) + Defekttest-Welle
- **Reproduktion:** Org mit Client c1, Sites S1/S2, Building/Room/Rack in S2, Asset mit location_id in S2. Site-1-Principal: GET /api/v1/assets, GET /api/v1/assets/{id}; Direkt-SELECT room/rack/asset per WithTenant. Kodiert in TestAuditT1SiteScopeOfLocatedTables (backend/internal/tenant/rls/audit_t1_scope_derivation_auditdefect_test.go); Probe probe_site_scope_nosite_tables.sql.
- **Soll:** TEN-02 'Tabellen mit Standortbezug tragen denormalisiert site_id'; CH25: Site-Scope DB-seitig erzwungen – ein Site-1-Principal sieht nur Assets/Rooms/Racks von Site 1.
- **Ist:** 'GET /assets as site1 lists the asset located in site 2'; 'GET /assets/{site-2 asset} as site1: 200, want 404'; 'site-1 principal selects the room/rack/asset of site 2 (1 rows), want 0'.
- **Nutzerwirkung:** Ein auf eine Site beschränkter Techniker listet und sucht Assets aller Sites seiner Clients; Rooms/Racks sind nur durch fehleranfällige Go-Prädikate geschützt (vgl. L-T1-02).
- **Ursache (belegt):** Kein WP des Plans denormalisiert site_id auf asset/room/rack; WP-074 ergänzte asset.location_id ohne abgeleitete site_id; Asset-Repository ohne Site-Filter; room/rack-Policies nur Org. Liegt außerhalb der AK von WP-027 (Eigentümer-WP fehlt).
- **Empfehlung:** site_id (und client_id für room/rack) per Trigger aus location ableiten, Site-Prädikate und Katalogregel ergänzen; im Plan ein Eigentümer-WP festlegen – oder PO-Entscheidung 'Assets site-los' dokumentieren.
- **Retest (verbindlich):** cd backend && TEST_DATABASE_URL='postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable' go test -tags auditdefect -count=1 -v -run '^TestAuditT1SiteScopeOfLocatedTables$' ./internal/tenant/rls/ muss PASS liefern; probe_site_scope_nosite_tables.sql liefert 0/0/0; Erwartung in search/scope_integration_test.go:96-104 angepasst.
- **Kodierender Test:** TestAuditT1SiteScopeOfLocatedTables (FAILS_AS_EXPECTED); kodiert das Fehlverhalten als erwartet: search/scope_integration_test.go:96-104.

<a id="f-054"></a>
### F-054 (P2) – OpenSearch-Dokumente behalten nach Rack-Platzierung eine veraltete Site; site-beschränkte Nutzer finden weggezogene CIs

- **Betroffene WPs:** WP-033 · **Komponente:** backend/internal/rack/placement.go:59; backend/internal/search/hybrid.go:49-77, opensearch.go:284-321 (visibleTo); backend/internal/ci/indexing_repository.go:71-78 · **Quelle:** Verifikation (B05) + Defekttest-Welle
- **Reproduktion:** HybridBackend mit OpenSearch-Fake und PG-Quelle; CI über das indizierende CI-Repository in einem Raum von Site A anlegen; rack.CreateMount in ein Rack von Site B (Produktpfad POST/PUT /api/v1/racks/{id}/mounts); hybrid.Query als Site-A-Principal. Kodiert in TestAuditT1OpenSearchFollowsRackPlacement (backend/internal/search/audit_t1_opensearch_scope_auditdefect_test.go).
- **Soll:** Nach dem Site-Wechsel kein Treffer für Site-A-Principals, Treffer für Site-B-Principals; Remote-Dokument site_id = B (TEC-12).
- **Ist:** 'remote document of the moved CI keeps site 85f6acfe-..., want site B 3a2cf2ba-...'; 'CH25/TEN-05: a site-A principal still finds the CI that moved to site B through OpenSearch'. Der PG-Pfad liefert für denselben Principal keinen Treffer.
- **Nutzerwirkung:** In OpenSearch-Deployments (im k8s-Prod-Overlay aktiviert) sehen site-beschränkte Nutzer Treffer (Titel, Zusammenfassung, URL) zu CIs, die ihre Site verlassen haben, und verpassen hinzugezogene, bis zum nächsten CI-Schreibvorgang oder Reindex.
- **Ursache (belegt):** Der Remote-Index wird nur von ci.NewIndexingRepository und ReindexTenant geschrieben; rack/placement.go:59 setzt ci.location_id per SQL ohne Indexer-Aufruf; die PG-Propagation (000067) aktualisiert nur search_document; visibleTo prüft nur die veralteten Dokumentfelder.
- **Empfehlung:** Reindex des CI-Dokuments nach jeder scope-ändernden Schreiboperation (Outbox/Event aus dem Scope-Trigger oder Aufruf aus rack.recordPlacement), alternativ Remote-Treffer vor Rückgabe gegen PG-Sichtbarkeit prüfen.
- **Retest (verbindlich):** cd backend && TEST_DATABASE_URL='postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable' go test -tags auditdefect -count=1 -v -run '^TestAuditT1OpenSearchFollowsRackPlacement$' ./internal/search/ muss PASS liefern; ergänzend UnmountCI und Client-Wechsel über den CI-Service (noch nicht kodiert).
- **Kodierender Test:** TestAuditT1OpenSearchFollowsRackPlacement (FAILS_AS_EXPECTED); Probes TestProbeV05OpenSearchStaleSiteAfterRackMount, TestProbeV05OpenSearchStaleSiteAfterLocationMove.

<a id="f-055"></a>
### F-055 (P2) – Strukturierte Suche (POST /api/v1/search/query) verlangt search:write; Leserollen erhalten 403, Saved Views nicht ausführbar

- **Betroffene WPs:** WP-034 · **Komponente:** backend/internal/server/authz.go:170 (writePermissionOverrides['search']); backend/internal/server/testdata/route_permissions.txt:458; frontend/src/pages/SavedViewsPage.tsx:140-145 · **Quelle:** Audit (B05) + Verifikation + Defekttest-Welle
- **Reproduktion:** Principal mit Rolle viewer bzw. client_technician (ci:read, saved_view:read): POST /api/v1/search/query {entity_kind: 'ci'} über den vollständigen Router. Kodiert in TestAuditT1StructuredSearchForReadRoles (backend/internal/tenant/rls/audit_t1_search_query_roles_auditdefect_test.go); Probe TestProbeV05SearchQueryByRole.
- **Soll:** Nutzer mit ci:read (bzw. asset:read für asset) führen strukturierte Abfragen und Saved Views aus; der Handler prüft das Entity-Leserecht (WP-034 AK1); POST /search/reindex bleibt search:write.
- **Ist:** 'viewer (holds ci:read) POST /search/query {entity_kind: ci}: 403 missing required permission: search:write, want 200 with the CI (same for client_technician)'; die UI zeigt einen generischen Fehler, der Retry-Button ist ein No-op (onRetry={() => void 0}).
- **Nutzerwirkung:** Viewer und Client-Techniker sehen Saved Views, können sie aber nicht ausführen; Retry hilft nicht.
- **Ursache (belegt):** authz.go:170 mappt alle Nicht-GET-Routen unter /api/v1/search/* auf search:write ('Rebuild tenant search indexes'); es gibt keine routeRule für POST search/query.
- **Empfehlung:** routeRule {POST, 'search/query', search:read} (oder nur authentifiziert) und Handler-Leseprüfung nutzen; route_permissions.txt und api/openapi.yaml (403 dokumentieren) anpassen; echtes onRetry in SavedViewsPage.
- **Retest (verbindlich):** cd backend && TEST_DATABASE_URL='postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable' go test -tags auditdefect -count=1 -v -run '^TestAuditT1StructuredSearchForReadRoles$' ./internal/tenant/rls/ muss PASS liefern; TestEveryOperationRequiresItsExpectedPermission mit aktualisierter Erwartung grün; Real-Stack-Saved-View durch einen Viewer.
- **Kodierender Test:** TestAuditT1StructuredSearchForReadRoles (FAILS_AS_EXPECTED); pinnt das Fehlverhalten: TestEveryOperationRequiresItsExpectedPermission (route_permissions.txt:458).

<a id="f-056"></a>
### F-056 (P2) – IGA-Handler ignoriert Repository-Schreibfehler (Genehmigung ohne Provisioning-Task)

- **Betroffene WPs:** WP-021 · **Komponente:** backend/internal/iga/handler.go:346 (sowie :203, :207, :242, :404, :433, :452, :455) · **Quelle:** Audit (B03) + Verifikation + Defekttest-Welle
- **Reproduktion:** Connector und offene Access-Request mit diesem Connector anlegen; POST /api/v1/iga/access-requests/{id}/approve, während CreateTask fehlschlägt (Repository-Wrapper injiziert den Fehler, alle anderen Aufrufe gegen PostgreSQL). Kodiert in TestAuditT1ApprovalNeverLosesTheProvisioningTask (backend/internal/iga/audit_t1_approve_task_auditdefect_test.go); Probe TestVerifyB03igaSwallow.
- **Soll:** Nicht-2xx-Antwort (oder atomarer Rollback) mit geloggtem Fehler; die Access-Request bleibt pending oder der Task existiert.
- **Ist:** 'approve answered 200 while the provisioning task could not be written; DB status approved, tasks 0 (want non-2xx, or the task)'; kein Log, keine Metrik.
- **Nutzerwirkung:** Genehmigter Zugriff wird nie provisioniert, niemand wird benachrichtigt; gleiches Muster bei Drift-Findings und Task-Statusupdates.
- **Ursache (belegt):** '_ = h.repo.CreateTask(...)' in iga/handler.go:346; DecideAccessRequest und CreateTask laufen in getrennten WithTenant-Transaktionen.
- **Empfehlung:** Entscheidung und Task-Anlage in einer Transaktion (WithRequestTenantTx) oder Fehler über WriteRepoError zurückgeben und loggen; die übrigen ignorierten Writes gleich behandeln.
- **Retest (verbindlich):** cd backend && TEST_DATABASE_URL='postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable' go test -tags auditdefect -count=1 -v -run '^TestAuditT1ApprovalNeverLosesTheProvisioningTask$' ./internal/iga/ muss PASS liefern; ein normales Approve erzeugt genau einen add_group_member-Task.
- **Kodierender Test:** TestAuditT1ApprovalNeverLosesTheProvisioningTask (FAILS_AS_EXPECTED); Probe TestVerifyB03igaSwallow.

<a id="f-057"></a>
### F-057 (P2) – Server meldet sich in der paketierten Installation als SUPERUSER an; Rollenprüfung prüft nur current_user, RESET ROLE umgeht RLS

- **Betroffene WPs:** WP-064 · **Komponente:** backend/internal/database/role_check.go:42-43 (roleContractQuery), pool.go:61-99 (NewPool); deploy/docker-compose/docker-compose.yml:47-49, :287 · **Quelle:** Verifikation (B08) + Defekttest-Welle
- **Reproduktion:** NewPool mit einem Login, der nach 'RESET ROLE' SUPERUSER/BYPASSRLS zurückerhält (Compose-Default POSTGRES_USER=reticora, RETICORA_DATABASE_URL=postgres://reticora:...); auf einer Pool-Verbindung RESET ROLE ausführen. psql SP/work/verify-B08/reset_role_probe.sql. Kodiert in TestAuditT1StartupRefusesEscalatableLogin (backend/internal/database/audit_t1_role_contract_auditdefect_test.go).
- **Soll:** TEN-03/CH19: die DB-Rolle der Anwendung hat kein SUPERUSER/BYPASSRLS; die Startprüfung (WP-064 AK3) verweigert Sessions, die per RESET ROLE oder SET ROLE eskalieren können.
- **Ist:** 'NewPool accepted a session whose login role regains SUPERUSER/BYPASSRLS via RESET ROLE; ... after RESET ROLE on a pooled connection current_user has superuser=true bypassrls=true'; Probe: 'after RESET ROLE|reticora|t', 'org rows visible after RESET ROLE|1'. TestRoleContractStartupCheck:144 akzeptiert den Superuser-Login.
- **Nutzerwirkung:** Ohne SQL-Ausführung nicht ausnutzbar; jede SQL-Injection würde aber zu vollem Cross-Tenant-Lese-/Schreibzugriff und DB-Host-Kommandoausführung (z.B. COPY ... TO PROGRAM), statt durch RLS begrenzt zu sein.
- **Ursache (belegt):** role_check.go startet die Erreichbarkeitsrekursion bei current_user (nach SET ROLE); session_user/rolsuper wird nicht geprüft; VerifyRLSEnforced prüft ebenfalls current_user; der Installer verwendet den Bootstrap-Superuser als Runtime-Login.
- **Empfehlung:** VerifyRoleContract/VerifyRLSEnforced zusätzlich für session_user (rolsuper, rolbypassrls, erreichbare Rollen) und Start verweigern; Installer und k8s-Doku legen einen dedizierten LOGIN NOSUPERUSER NOBYPASSRLS-Login (nur Mitglied von reticora_app) an; Superuser nur für Migrationen.
- **Retest (verbindlich):** cd backend && TEST_DATABASE_URL='postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable' go test -tags auditdefect -count=1 -v -run '^TestAuditT1StartupRefusesEscalatableLogin$' ./internal/database/ muss PASS liefern (RoleContractError mit Nennung des Session-Users); TestAuditT1DedicatedLoginRolePassesContract bleibt grün; install-smoke: session_user des Servers ist kein Superuser (lokal NICHT TESTBAR, Docker-Build durch Netzwerkpolicy blockiert).
- **Kodierender Test:** TestAuditT1StartupRefusesEscalatableLogin (FAILS_AS_EXPECTED); Positivfall TestAuditT1DedicatedLoginRolePassesContract (PASS); kodiert das Fehlverhalten als erwartet: TestRoleContractStartupCheck:144.

<a id="f-058"></a>
### F-058 (P2) – Kanalmatrix deckt Locations unterhalb von Sites, org-weite Schreibpfade, GraphQL-Felder außer cis und exakte Negativstatus nicht ab

- **Betroffene WPs:** WP-208 · **Komponente:** backend/internal/tenant/rls/channel_matrix_integration_test.go (:244, :276-278, :294-309, :311-345, :339-342, :430-434); matrix_fixtures.go:16 (rls.Channels ungenutzt) · **Quelle:** Audit/Verifikation (B27)
- **Reproduktion:** Testcode-Inspektion und Verifier-Probes TestVerifyB27Matrix/TestVerifyB27NegativeStatus: TestChannelMatrix ist grün, obwohl L-T1-02 (Rack-Site-Bypass) und L-T1-17 (500 bei unsichtbarer Wurzel) bestehen.
- **Soll:** TEN-09/WP-208 AK1: Matrix über alle Kanäle × Scope-Arten (Org/Client/Site/Team/Mehrfach) × Lesen/Schreiben mit Positiv- und Negativkontrollen und exakten Statuscodes.
- **Ist:** Locations nur über /sites; /review-items, /webhooks, /jobs, /notifications nur GET; /jobs ohne Daten; GraphQL nur 'cis'; unsichtbare Wurzeln per continue übersprungen; Fremdverweise akzeptieren jeden Nicht-2xx-Code (500 eingeschlossen). Die Welle ergänzte Teile als separate, grüne Regressionstests (TestAuditT1OrgWideChannelWrites, TestAuditT1GraphQLFieldsHonourScope, TestAuditT1LocationChannelBelowSites, TestAuditT1TopologyChannelInvisibleRoots); die Matrix selbst ist unverändert.
- **Nutzerwirkung:** Regressionen und bestehende Defekte in nicht abgedeckten Kanälen bleiben unentdeckt; GATE-03 'Isolationstest vollständig' ist nicht belegt.
- **Ursache (belegt):** Testumfang auf CI-zentrierte Pfade begrenzt; die Fixture-Tabelle rls.Channels wird nicht verwendet.
- **Empfehlung:** Matrix tabellengetrieben aus rls.Channels aufbauen und um Buildings, Rooms, Racks, Mounts, rackLayout × alle Scopes, org-weite Schreibzugriffe, alle GraphQL-Felder und exakte 404/422-Erwartungen erweitern.
- **Retest (verbindlich):** cd backend && TEST_DATABASE_URL='postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable' go test -count=1 -run '^TestChannelMatrix$' ./internal/tenant/rls/ mit erweiterter Matrix: rot bis L-T1-02/L-T1-17 behoben, danach grün; bis dahin bleiben die vier Wellen-Regressionstests grün.
- **Kodierender Test:** keiner (Testlücke); ergänzend Wellen-Regressionstests (PASS) und Defekttests TestAuditT1RackChannelSiteScope, TestAuditT1ScopeRefusalsAreNot5xx (FAILS_AS_EXPECTED).

<a id="f-059"></a>
### F-059 (P2) – In das generische Job-Modell gespiegelte Export-Jobs sind über GET /api/v1/jobs und /jobs/{id} für andere Nutzer und Clients sichtbar

- **Betroffene WPs:** WP-035, WP-208 · **Komponente:** backend/internal/export/job_handler.go:98, worker.go:79-82 (Mirror); backend/internal/jobs/handler.go:69-122, jobs/pg_repository.go (nur Org-Filter) · **Quelle:** Verifikation (B05)
- **Reproduktion:** Probe TestProbeV05ExportJobsVisibleInGenericJobs (audit_db_05): Export-Worker mit jobs.Service-Tracker wie router.go:359; Nutzer A (Client-1-Scope) POST /api/v1/export/jobs -> 202, worker.ProcessOne; Nutzer B (anderer app_user, Client-2-Scope, job:read): GET /api/v1/export/jobs -> total 0, GET /api/v1/jobs -> 200 mit dem Export-Job, GET /api/v1/jobs/{id} -> 200, /jobs/{id}/result -> 409.
- **Soll:** WP-035 AK2: Export-Job-GET und -Liste nur für den Ersteller bzw. definierte Rollen; ein Nutzer eines anderen Clients sieht den Export-Job nicht.
- **Ist:** Die generische Job-API filtert nur nach Organisation und liefert den gespiegelten Export-Job (created_by A, ref_table export_job, ref_id, Status, Zeitstempel) jedem job:read-Inhaber.
- **Nutzerwirkung:** Nutzer sehen im Jobcenter, dass, wann und mit welchem Status/Fehler ein anderer Nutzer exportiert hat; Inhalte und Download-Links bleiben geschützt.
- **Ursache (belegt):** Mirror/Track legt eine generische Job-Zeile an; jobs/handler.go und jobs/pg_repository.go filtern nur per Org-RLS; die Ersteller-Regel ownJob (export/pg_job_repository.go:24) greift dort nicht.
- **Empfehlung:** Generische Jobs der Art export (bzw. jede ref_table mit Ersteller-Semantik) auf created_by = Aufrufer beschränken, sofern keine definierte Admin-Berechtigung vorliegt; Repo- und API-Test ergänzen.
- **Retest (verbindlich):** API-Test: A erstellt einen Export; B (job:read, anderer Client) sieht ihn nicht in GET /api/v1/jobs, GET /api/v1/jobs/{id} -> 404; A sieht ihn; Org-Admin mit definierter Berechtigung gemäß Festlegung.
- **Kodierender Test:** keiner (nur Probe TestProbeV05ExportJobsVisibleInGenericJobs).
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – prioritaet: „P3“ → „P2“. Bestätigte Cross-Client-Exposition (Probe: Nutzer B mit Client-2-Scope sieht den Export-Job von Nutzer A, Client 1, mit created_by, ref_id, Status, Zeitstempeln und Fehler) im ausdrücklich von TEN-09 genannten Job-Kanal. Das ist kein 'geringfügiger Qualitätsmangel'. Konsistent mit L-T1-10 (clientübergreifendes Metadaten-Leck, P2).
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – wps: „["WP-035"]“ → „["WP-035","WP-208"]“. Job ist ein in WP-208 AK1 genannter Kanal. Die Exposition ist eine Kanal-Isolationsverletzung, die die Matrix nicht erkennt (/jobs dort nur GET ohne Daten, L-T1-16).

<a id="f-060"></a>
### F-060 (P2) – Deaktivierung/Anonymisierung eines fremden Benutzers widerruft dessen Sessions trotz 404 (mandantenübergreifender Zwangs-Logout)

- **Betroffene WPs:** WP-042 · **Komponente:** backend/internal/user/handler.go:206-238 (UpdateUser), Anonymize; identity/session.go:63-74 · **Quelle:** Verifikation, Defekttest-Welle
- **Reproduktion:** Org-A-Admin: PATCH /api/v1/users/{Org-B-User} {"status":"inactive"} bzw. POST …/anonymize (V/xtenant-revoke.log; TestAuditT2ForeignDeactivationRevokesNothing, audit_db_02).
- **Soll:** 404 ohne Wirkung in Org B; Widerruf nur nach erfolgreicher org-gebundener Deaktivierung in derselben Transaktion (WP-042 Kriterium 3, TEN-04/09).
- **Ist:** 404, aber Blacklist-Eintrag für Org-B-User geschrieben; dessen Session wird abgewiesen; User bleibt aktiv; wiederholbar.
- **Nutzerwirkung:** Jeder Tenant-Admin kann Benutzer fremder Mandanten per UUID dauerhaft ausloggen.
- **Ursache (belegt):** revokeSessions (handler.go:219-225) vor repo.UpdateUser (org-Prüfung); RevokeUser schlüsselt nur nach User-ID; Anonymize gleiches Muster.
- **Empfehlung:** Benutzer org-gebunden laden/sperren, Blacklist erst nach erfolgreichem Update; oder org+user-Schlüssel mit Prüfung in Validate.
- **Retest (verbindlich):** cd backend && TEST_DATABASE_URL=… go test -tags auditdefect -count=1 -v -run '^TestAuditT2ForeignDeactivationRevokesNothing$' ./internal/user/ → PASS (alle Subtests); TestT2DeactivationFullChain weiter PASS.
- **Kodierender Test:** backend/internal/user/t2_deactivation_tenant_auditdefect_test.go TestAuditT2ForeignDeactivationRevokesNothing (FAILS_AS_EXPECTED)

<a id="f-061"></a>
### F-061 (P2) – Bestehende Installationen behalten nach Upgrade den alten Realm ohne neue Policies

- **Betroffene WPs:** WP-044 · **Komponente:** deploy/docker-compose/docker-compose.yml:217; install-cloud.sh create_initial_admin · **Quelle:** Audit (Hypothese)
- **Reproduktion:** Upgrade mit existierendem Realm: --import-realm überspringt ihn; kein kcadm update (nicht ausgeführt, Installationspfad blockiert).
- **Soll:** Policies nach Upgrade aktiv.
- **Ist:** Vermutlich alte Einstellungen.
- **Nutzerwirkung:** Upgrade-Kunden ohne Lockout/MFA.
- **Ursache (Hypothese):** Policies nur über Realm-Import (Hypothese, Keycloak-Import-Semantik).
- **Empfehlung:** Policies idempotent per kcadm im Re-Run anwenden.
- **Retest (verbindlich):** Shelltest mit fake_kcadm (neu) assertiert Realm-Updates und Idempotenz; Containertest mit altem Realm PASS.
- **Kodierender Test:** keiner

<a id="f-062"></a>
### F-062 (P2) – Webhook-Seite verschluckt Serverablehnungen (400 Egress/Validierung, 403): keine Meldung

- **Betroffene WPs:** WP-045, WP-046, WP-049, WP-191, WP-192 · **Komponente:** frontend/src/pages/WebhooksPage.tsx:51-82; api/hooks.ts:738-744; main.tsx:9-16 · **Quelle:** Real-Stack-E2E (W01 confirmed; D5 zusammengeführt)
- **Reproduktion:** Admin: URL http://127.0.0.1:<port>/hook speichern → 400 'egress: destination is not allowed', Seite unverändert (W01 FAIL, 3 Läufe); Viewer → 403 ohne Meldung (09c FAIL).
- **Soll:** Ablehnung mit Grund sichtbar (UI-01..10 Webhook-Seite, WP-049 Speichervalidierung).
- **Ist:** Kein Toast/Inline-Fehler.
- **Nutzerwirkung:** Sicherheitsblock nicht von Bug unterscheidbar. Zugerechnet WP-049; für WP-045/WP-046 nur verknüpft (Backend-403 korrekt).
- **Ursache (belegt):** mutate ohne onError; error/isError nie gerendert; kein globaler Handler.
- **Empfehlung:** Mutationsfehler rendern; Controls ohne webhook:manage ausblenden.
- **Retest (verbindlich):** npx playwright test -c e2e-real/playwright.real.config.ts bulk-jobs-export -g W01 und roles-authz -g 09c PASS.
- **Kodierender Test:** frontend/e2e-real/bulk-jobs-export.spec.ts:1021 W01, roles-authz.spec.ts:816 09c (FAIL)

<a id="f-063"></a>
### F-063 (P2) – Erfolgreiche Refreshes verbrauchen das Pre-Auth-Budget je IP; SPA meldet bei 429 ab

- **Betroffene WPs:** WP-050 · **Komponente:** backend/internal/middleware/ratelimit.go:242-305; frontend/src/auth/session.ts:28-31 · **Quelle:** Verifikation, Defekttest-Welle
- **Reproduktion:** TestAuditT2SuccessfulRefreshDoesNotConsumePreAuthBudget: 30 erfolgreiche Refreshes → Nr. 21-30 mit 429.
- **Soll:** AUT-10: nur fehlgeschlagene Versuche zählen.
- **Ist:** Alle Refreshes hinter einer IP teilen 20/min.
- **Nutzerwirkung:** Abmeldungen hinter NAT (derzeit von L-T2-18 verdeckt).
- **Ursache (belegt):** Pre-Auth-Pfade zählen jede Anfrage; SPA unterscheidet 429 nicht.
- **Empfehlung:** Nur Fehlschläge zählen, atomar prüfen; SPA-Backoff.
- **Retest (verbindlich):** go test -tags auditdefect -count=1 -v -run '^TestAuditT2SuccessfulRefreshDoesNotConsumePreAuthBudget$' ./internal/middleware/ PASS.
- **Kodierender Test:** middleware/t2_auth_ratelimit_auditdefect_test.go (FAILS_AS_EXPECTED)

<a id="f-064"></a>
### F-064 (P2) – Agent folgt https→http-Redirects und sendet Bearer-Token im Klartext

- **Betroffene WPs:** WP-051 · **Komponente:** backend/cmd/agent/main.go:147-165,266-298 · **Quelle:** Verifikation, Defekttest-Welle
- **Reproduktion:** TLS-Server antwortet 302/307 auf http:// → Klartextserver erhält 'Bearer …' (V/agent-probes.log); TestAuditT2AgentRefusesRedirectToPlainHTTP FAILS_AS_EXPECTED.
- **Soll:** WP-051 Kriterium 1: kein HTTP-Fallback.
- **Ist:** Default-Redirect-Policy.
- **Nutzerwirkung:** Fehlkonfigurierter Proxy legt 1-Jahres-Token offen.
- **Ursache (belegt):** Kein CheckRedirect.
- **Empfehlung:** Redirects auf Nicht-https verweigern.
- **Retest (verbindlich):** go test -tags auditdefect -count=1 -v -run '^TestAuditT2AgentRefusesRedirectToPlainHTTP$' ./cmd/agent/ PASS.
- **Kodierender Test:** backend/cmd/agent/t2_redirect_auditdefect_test.go (FAILS_AS_EXPECTED)

<a id="f-065"></a>
### F-065 (P2) – Parallele 401 lösen parallele Refreshes aus, Reuse-Erkennung widerruft die Familie

- **Betroffene WPs:** WP-066 · **Komponente:** frontend/src/api/client.ts:392-410; auth/session.ts · **Quelle:** Verifikation, Defekttest-Welle
- **Reproduktion:** Drei parallele 401 → 3 Refresh-Aufrufe (vprobe refreshCalls=3; session.auditdefect.ts 'expected 3 to be 1').
- **Soll:** Single-Flight-Refresh.
- **Ist:** Zweiter Aufruf mit altem Cookie widerruft Familie.
- **Nutzerwirkung:** Abmeldung bei Dashboard-Last nach Ablauf.
- **Ursache (belegt):** Kein geteiltes In-flight-Promise.
- **Empfehlung:** Single-Flight; ggf. Server-Grace-Window.
- **Retest (verbindlich):** cd frontend && npx vitest run -c vitest.auditdefect.config.ts src/auth/session.auditdefect.ts PASS.
- **Kodierender Test:** frontend/src/auth/session.auditdefect.ts (FAIL wie erwartet)

<a id="f-066"></a>
### F-066 (P2) – Abgelaufene gespeicherte Session führt ohne Refresh-Versuch zu /login

- **Betroffene WPs:** WP-066 · **Komponente:** frontend/src/auth/authStore.ts:29-71; session.ts:17-20 · **Quelle:** Verifikation, Real-Stack-E2E, Defekttest-Welle
- **Reproduktion:** roles-authz 14b FAIL: Reload mit abgelaufenem expiresAt → 0 Refresh-Aufrufe (final-run.log:300-323); bootRefresh.auditdefect.tsx 'expected 0 to be 1'.
- **Soll:** AUT-02: Session per Cookie fortgesetzt.
- **Ist:** Redirect nur anhand expiresAt.
- **Nutzerwirkung:** Nach 15 min Leerlauf endet die Session bei Reload/Navigation.
- **Ursache (belegt):** Kein Boot-/geplanter Refresh.
- **Empfehlung:** Vor Redirect refreshSession(); Refresh vor exp planen.
- **Retest (verbindlich):** npx vitest run -c vitest.auditdefect.config.ts src/auth/bootRefresh.auditdefect.tsx PASS; roles-authz -g 14b PASS.
- **Kodierender Test:** frontend/src/auth/bootRefresh.auditdefect.tsx; e2e-real roles-authz 14b (FAIL)

<a id="f-067"></a>
### F-067 (P2) – Authz ignoriert die RBA-01-Schlüssel citype:manage und notification:manage

- **Betroffene WPs:** WP-068 · **Komponente:** backend/internal/server/authz.go:80-81,139-140,185; permission/catalog.go:72,95 · **Quelle:** Audit, Verifikation, Defekttest-Welle
- **Reproduktion:** probe-v09-rba01.log; TestAuditT2RBA01KeysGateCITypesAndNotificationChannels FAILS_AS_EXPECTED (citype:manage → 403; webhook:manage → notification-channels 200).
- **Soll:** RBA-01-Schlüssel steuern ihre Aktionen, kein Duplikat.
- **Ist:** ci_type:manage und webhook:* steuern.
- **Nutzerwirkung:** Custom-Rollen nach RBA-01 wirkungslos; Webhook-Manager verwalten Kanäle.
- **Ursache (belegt):** authz.go nicht umgestellt; exakter Match.
- **Empfehlung:** Mapping umstellen, Duplikat migrieren, Guard-Test.
- **Retest (verbindlich):** go test -tags auditdefect -count=1 -v -run '^TestAuditT2RBA01KeysGateCITypesAndNotificationChannels$' ./internal/server/ PASS.
- **Kodierender Test:** backend/internal/server/t2_auth_chain_auditdefect_test.go (FAILS_AS_EXPECTED)

<a id="f-068"></a>
### F-068 (P2) – Break-Glass-'Alarm' nur Logzeile und Zähler; keine Alarmregel ausgeliefert

- **Betroffene WPs:** WP-070 · **Komponente:** backend/internal/operator/auth.go:84-100; deploy/monitoring/* · **Quelle:** Audit, Verifikation, Defekttest-Welle
- **Reproduktion:** grep operator_break_glass deploy/ leer; tests/audit/t2-break-glass-alert.test.sh FAILS.
- **Soll:** SEC-07: jede Nutzung alarmiert.
- **Ist:** Niemand wird benachrichtigt.
- **Nutzerwirkung:** Missbrauch unbemerkt.
- **Ursache (belegt):** Keine Prometheus-Regel/Benachrichtigung.
- **Empfehlung:** Regel increase(...[5m])>0 critical ausliefern.
- **Retest (verbindlich):** ./tests/audit/t2-break-glass-alert.test.sh PASS; promtool test rules PASS.
- **Kodierender Test:** tests/audit/t2-break-glass-alert.test.sh (FAILS)

<a id="f-069"></a>
### F-069 (P2) – Personalisierter Operator-OIDC-Login mit ausgeliefertem Realm unmöglich (kein acr/amr, keine Gruppe operators)

- **Betroffene WPs:** WP-070 · **Komponente:** deploy/keycloak/realm-reticora*.json; backend/internal/operator/handler.go:105-121 · **Quelle:** Audit, Verifikation (auf P2 hochgestuft)
- **Reproduktion:** Reale PKCE-Anmeldung: ID-Token ohne acr/amr (verify-B09/kc/idtoken-claims.txt); Gruppen nur reticora-admin → issue() immer 403.
- **Soll:** SEC-07: personalisierte MFA-Operatoren.
- **Ist:** Nur Break-Glass nutzbar.
- **Nutzerwirkung:** Geteiltes Token für Routinearbeit, keine persönliche Verantwortlichkeit.
- **Ursache (belegt):** Realm ohne operators-Gruppe, amr-Mapper, acr-Scope/LoA.
- **Empfehlung:** Realm ergänzen und dokumentieren.
- **Retest (verbindlich):** Neuer e2e-real-Test: Operator mit OTP → admin/auth/callback 200, admin/me 200; ohne OTP 403.
- **Kodierender Test:** keiner

<a id="f-070"></a>
### F-070 (P2) – rack_view, api_access und notifications_email serverseitig nicht durchgesetzt (REST und GraphQL)

- **Betroffene WPs:** WP-072 · **Komponente:** backend/internal/entitlement/middleware.go:17-58; graphqlbff/resolver.go:275 · **Quelle:** Audit, Verifikation, Defekttest-Welle
- **Reproduktion:** probe-v09-ent.log E2 (/racks 200 ohne rack_view); TestAuditT2EntitlementRackViewGated, TestAuditT2EntitlementAPIAccessGatesAPIKeys FAILS_AS_EXPECTED.
- **Soll:** ENT-05; WP-072 Kriterium 1.
- **Ist:** Racks/rackLayout/API-Keys/E-Mail ohne Feature nutzbar.
- **Nutzerwirkung:** Lizenzbedingungen nicht durchsetzbar; UI und API widersprechen sich.
- **Ursache (belegt):** Fehlende gatedRoutes, rackLayout auf cmdb_core, keine Checks.
- **Empfehlung:** Gates ergänzen, Mapping-Tests.
- **Retest (verbindlich):** go test -tags auditdefect -count=1 -v -run 'TestAuditT2EntitlementRackViewGated|TestAuditT2EntitlementAPIAccessGatesAPIKeys' ./internal/server/ PASS; neuer notifications_email-Test.
- **Kodierender Test:** backend/internal/server/t2_entitlement_auditdefect_test.go (2, FAILS_AS_EXPECTED)

<a id="f-071"></a>
### F-071 (P2) – ENT-07-Ablaufwarnungen fehlen (Banner, 14/7/1-Tage-Benachrichtigung)

- **Betroffene WPs:** WP-072 · **Komponente:** backend/internal/events/envelope.go:59 (kein Emitter); frontend/src (kein Banner) · **Quelle:** Verifikation
- **Reproduktion:** grep events.EntitlementWarning nur Template/Test; abdeckung.csv:81 ordnet Lücke WP-072 zu.
- **Soll:** ENT-07.
- **Ist:** Funktion fehlt.
- **Nutzerwirkung:** Keine Warnung vor Discovery-Stopp.
- **Ursache (belegt):** Nicht implementiert.
- **Empfehlung:** Täglicher Job + Banner.
- **Retest (verbindlich):** Neuer uhrgesteuerter Integrationstest (je Schwelle ein entitlement.warning) und e2e-real-Banner PASS.
- **Kodierender Test:** keiner

<a id="f-072"></a>
### F-072 (P2) – Nur max_cis durchgesetzt; max_users, max_api_keys, max_collectors wirkungslos

- **Betroffene WPs:** WP-073 · **Komponente:** backend/internal/ci/service.go:268-279; identity/apikey_handler.go Create; Benutzer-/Collector-Anlage · **Quelle:** Verifikation, Defekttest-Welle
- **Reproduktion:** probe-v09-ent.log E4 [201 201 201]; TestAuditT2EntitlementQuotasEnforced FAILS_AS_EXPECTED.
- **Soll:** ENT-03: Limitprüfung in REST-Schreibpfaden, 403 entitlement-limit.
- **Ist:** Anlage über Limit erfolgreich.
- **Nutzerwirkung:** Lizenzquoten nicht durchsetzbar.
- **Ursache (belegt):** AllowCreate nur für max_cis.
- **Empfehlung:** AllowCreate in Benutzer-, Key-, Collector-Anlage.
- **Retest (verbindlich):** go test -tags auditdefect -count=1 -v -run '^TestAuditT2EntitlementQuotasEnforced$' ./internal/server/ PASS; max_collectors-Test ergänzen.
- **Kodierender Test:** backend/internal/server/t2_entitlement_auditdefect_test.go (FAILS_AS_EXPECTED)

<a id="f-073"></a>
### F-073 (P2) – KI-Ask-Pfad sendet Mandantendaten ohne Org-Opt-in an den Provider

- **Betroffene WPs:** WP-076 · **Komponente:** backend/internal/ai/handler.go:60-110; ai/retrieval.go:71-72 · **Quelle:** Verifikation, Defekttest-Welle
- **Reproduktion:** Real-Binary mit fake LLM: ai_opt_in=false → 200, /embeddings und /chat/completions (SPV/fakellm/calls.log); TestAuditT2AIAskRequiresOrgOptIn FAILS_AS_EXPECTED.
- **Soll:** AI-02: keine externe Verarbeitung ohne Opt-in.
- **Ist:** Frage und CI-Kontext gehen raus.
- **Nutzerwirkung:** Datenabfluss an externes LLM ohne Zustimmung.
- **Ursache (belegt):** Consent nur im Indexer.
- **Empfehlung:** OrgConsent in Handler/Retriever.
- **Retest (verbindlich):** TEST_DATABASE_URL=… go test -tags auditdefect -count=1 -v -run '^TestAuditT2AIAskRequiresOrgOptIn$' ./internal/server/ PASS.
- **Kodierender Test:** backend/internal/server/t2_ai_optin_auditdefect_test.go (FAILS_AS_EXPECTED)

<a id="f-074"></a>
### F-074 (P2) – Org-KI-Opt-in über keine API/UI/Operator-Schnittstelle setzbar

- **Betroffene WPs:** WP-076 · **Komponente:** migrations/000087; ai/pg_repository.go:164 · **Quelle:** Verifikation, Defekttest-Welle
- **Reproduktion:** grep ai_opt_in nur Migration/Lesezugriff; TestAuditT2AIOptInSettableThroughAPI FAILS_AS_EXPECTED.
- **Soll:** Opt-in-Entscheidung durch org_admin/Operator, auditiert.
- **Ist:** Nur per SQL.
- **Nutzerwirkung:** Funktion ohne DB-Zugriff unbenutzbar.
- **Ursache (belegt):** Kein Handler/OpenAPI/UI.
- **Empfehlung:** Feld in Org-Settings-API mit Audit und UI.
- **Retest (verbindlich):** go test -tags auditdefect -count=1 -v -run '^TestAuditT2AIOptInSettableThroughAPI$' ./internal/server/ PASS; API-Test 200/403/404.
- **Kodierender Test:** backend/internal/server/t2_ai_optin_auditdefect_test.go (FAILS_AS_EXPECTED)

<a id="f-075"></a>
### F-075 (P2) – Neun Listenoperationen als nackte Arrays ohne {data, next_cursor}; limit ignoriert

- **Betroffene WPs:** WP-164 · **Komponente:** backend/internal/credential/handler.go:88-105; RBAC-Handler; server/pagination_test.go:91 · **Quelle:** Audit, Verifikation, Defekttest-Welle (neuer P3 zusammengeführt)
- **Reproduktion:** vrun2: /permissions?limit=1 → 103 Einträge; Real-Stack headers.log F/G; TestAuditT2ListsAreCursorPages, TestAuditT2ContractHasNoBareArrayLists (9 Ops inkl. /ai/conversations, /documents/{id}/links, /monitoring/*) FAILS_AS_EXPECTED.
- **Soll:** API-02/WP-164: alle Listen paginiert.
- **Ist:** Nackte Arrays; Architekturtest blind.
- **Nutzerwirkung:** Uneinheitliches, unbegrenztes Paging.
- **Ursache (belegt):** Slices direkt kodiert; Test prüft nur Schemas mit data.
- **Empfehlung:** ListResponse + ParsePagination; Architekturtest erweitern.
- **Retest (verbindlich):** go test -tags auditdefect -count=1 -v -run 'TestAuditT2ListsAreCursorPages|TestAuditT2ContractHasNoBareArrayLists' ./internal/server/ PASS.
- **Kodierender Test:** backend/internal/server/t2_pagination_auditdefect_test.go (2, FAILS_AS_EXPECTED)

<a id="f-076"></a>
### F-076 (P2) – Session-Bucket je Access-Token, Reset bei jedem Refresh

- **Betroffene WPs:** WP-166 · **Komponente:** backend/internal/middleware/auth.go:170-171; identity/handler.go:247,304-309 · **Quelle:** Verifikation, Defekttest-Welle
- **Reproduktion:** vrun1 TestVB21SessionBucketPerToken; TestAuditT2SessionBudgetSurvivesTokenRefresh FAILS_AS_EXPECTED ('-> 204, want 429').
- **Soll:** API-04: 600/min je Session.
- **Ist:** Budget je jti.
- **Nutzerwirkung:** Limit per Refresh umgehbar.
- **Ursache (belegt):** Schlüssel jti statt Session-ID.
- **Empfehlung:** sid im Token, Bucket darauf.
- **Retest (verbindlich):** go test -tags auditdefect -count=1 -v -run '^TestAuditT2SessionBudgetSurvivesTokenRefresh$' ./internal/middleware/ PASS.
- **Kodierender Test:** middleware/t2_auth_ratelimit_auditdefect_test.go (FAILS_AS_EXPECTED)

<a id="f-077"></a>
### F-077 (P2) – CI-Listen-Export nutzt deprecated /api/v1/export/cis als Link → 401 im Browser

- **Betroffene WPs:** WP-168, WP-191, WP-192, WP-201 · **Komponente:** frontend/src/pages/CIListPage.tsx:118-123; api/client.ts:529-531; frontend/e2e/onboarding.spec.ts:323 · **Quelle:** Verifikation (Real-Browser), Defekttest-Welle; zusammengeführt mit L-T7-ui-23 (Real-Stack-E2E (owned) + Verifikation (B25))
- **Reproduktion:** Real-Chromium: 'CSV exportieren' → GET ohne Authorization → 401 'missing bearer token', Seite zeigt Roh-JSON; deprecatedPaths.auditdefect.ts FAIL (client.ts:530, onboarding.spec.ts:323).
- **Soll:** WP-168 Kriterium 2: neue Pfade (/exports) bzw. authentifizierter Download.
- **Ist:** Export für niemanden nutzbar.
- **Nutzerwirkung:** CI-Export defekt, Pfad verschwindet zum Sunset 2027-10-15.
- **Ursache (belegt):** <a href> ohne Bearer; Session in sessionStorage.
- **Empfehlung:** Fetch+Blob oder /exports-Job.
- **Retest (verbindlich):** cd frontend && npx vitest run -c vitest.auditdefect.config.ts src/api/deprecatedPaths.auditdefect.ts PASS; e2e-real Download PASS.
- **Kodierender Test:** frontend/src/api/deprecatedPaths.auditdefect.ts (FAIL wie erwartet); frontend/e2e-real/ci-lifecycle.spec.ts:987 L10b – FAIL (Real-Stack)
- **Prüfvermerk:** Gruppenübergreifend dedupliziert: L-T7-ui-23 („Buttons 'CSV exportieren'/'JSON exportieren' der CI-Liste sind tot: reine Links auf einen Bearer-geschützten Endpunkt liefern 401“) ist dieselbe Ursache und hier zusammengeführt.

<a id="f-078"></a>
### F-078 (P2) – Audit-Seite liest Felder, die die API nicht liefert ('Invalid Date', Objekt '—')

- **Betroffene WPs:** WP-133, WP-168, WP-171, WP-191, WP-192 · **Komponente:** frontend/src/pages/AuditPage.tsx:113,118; api/client.ts:2085-2101; backend/internal/audit/audit.go:31-49 · **Quelle:** Real-Stack-E2E (confirmed, P3→P2); zusammengeführt mit L-T7-ui-24 (Real-Stack-E2E (owned, verifiziert))
- **Reproduktion:** /audit als Admin: Zeitpunkt 'Invalid Date', Objekt '—'; API liefert timestamp/resource_type/resource_id; tenant-isolation T17 FAIL (verify-tenant-iso/final.log:20).
- **Soll:** AUD-07/Audit-Viewer zeigt Zeit und Objekt.
- **Ist:** Felder created_at/entity_* fehlen.
- **Nutzerwirkung:** Audit-UI für Prüfungen kaum nutzbar. Nicht WP-168 zugerechnet (gleicher Handler für /audit und /audit-log, router.go:475).
- **Ursache (belegt):** Vertragsdrift handgeschriebener Typ; Test-Fixture mit falschen Namen.
- **Empfehlung:** Generierten OpenAPI-Typ nutzen.
- **Retest (verbindlich):** npx playwright test -c e2e-real/playwright.real.config.ts tenant -g T17 PASS.
- **Kodierender Test:** frontend/e2e-real/tenant-isolation.spec.ts:1185 T17 (FAIL); frontend/e2e-real/ci-lifecycle.spec.ts:746 L07 – FAIL (Real-Stack); Querverweis T6: frontend/src/pages/AuditPage.auditdefect.tsx:46 'shows the entry time and the affected resource' – failed as expected
- **Prüfvermerk:** Gruppenübergreifend dedupliziert: L-T7-ui-24 („Audit-Viewer zeigt als Zeit 'Invalid Date' und als Objekt '—' für jeden Eintrag: die UI liest Felder, die die API nicht liefert“) ist dieselbe Ursache und hier zusammengeführt.

<a id="f-079"></a>
### F-079 (P2) – Standortbaum-UI ohne Umbenennen/Verschieben (LOC-07 CRUD unvollständig)

- **Betroffene WPs:** WP-053 · **Komponente:** frontend/src/pages/LocationTreePage.tsx:239-254; api/cmdbHooks.ts:166-180 · **Quelle:** Audit/Verifikation
- **Reproduktion:** /locations: nur '+ Unterpunkt' und 'Löschen'; grep findet kein useUpdateLocation, locationApi.update ungenutzt.
- **Soll:** LOC-07 (von WP-053 geschlossen): CRUD für alle Ebenen inkl. Umbenennen/Umhängen.
- **Ist:** Kein UI-Pfad (die API PATCH funktioniert, siehe L10).
- **Nutzerwirkung:** Tippfehler und Umzüge lassen sich nicht im UI korrigieren. Löschen und Neuanlage scheitert an 409.
- **Ursache (belegt):** Kein Update-Hook bzw. Dialog implementiert.
- **Empfehlung:** Bearbeiten-Dialog mit Matrix-Filter, alternativ Entscheidung „Phase 1 API-only“.
- **Retest (verbindlich):** Vitest für den Dialog sowie e2e-real: Umbenennen und Verschieben, Reload, Baum, Pfad und genau eine location_change-Zeile prüfen.
- **Kodierender Test:** keiner

<a id="f-080"></a>
### F-080 (P2) – PATCH /cis/{id} ignoriert null auf typisierten Feldern (200, unverändert) – nicht RFC 7396

- **Betroffene WPs:** WP-057 · **Komponente:** backend/internal/ci/model.go:120-143; ci/pg_repository.go:795-835 · **Quelle:** Audit/Verifikation + Real-Stack-E2E (owned) + Defekttest-Welle
- **Reproduktion:** PATCH {hostname:null} → 200, Wert bleibt (PROBE1). Real-Stack L05b: firmware_version:null → bleibt '1.2.3'.
- **Soll:** WP-057 K1/CI-04: null löscht.
- **Ist:** null = unverändert, 200.
- **Nutzerwirkung:** API-Clients können Felder nicht leeren und erhalten keinen Fehler.
- **Ursache (belegt):** *string mit omitempty; addStringField überspringt nil; MergePatch nur für attributes.
- **Empfehlung:** Präsenzbewusstes Decoding; null → NULL plus Override, alternativ 422.
- **Retest (verbindlich):** go test -tags auditdefect -run 'TestAuditT3MergePatchNullClearsTypedField' ./internal/ci/ PASS; Real-Stack L05b grün.
- **Kodierender Test:** backend/internal/ci/audit_t3_manual_writes_auditdefect_test.go TestAuditT3MergePatchNullClearsTypedField; frontend/e2e-real/ci-lifecycle.spec.ts L05b

<a id="f-081"></a>
### F-081 (P2) – Leeren eines optionalen Felds im CI-Bearbeiten-Dialog wird stillschweigend nicht gespeichert

- **Betroffene WPs:** WP-057, WP-201 · **Komponente:** frontend/src/pages/CIFormModal.tsx:46-49,156-164 · **Quelle:** Real-Stack-E2E (owned) + Verifikation
- **Reproduktion:** Real-Stack L05: Firmware 9.9.9 leeren → Speichern 200 → Reload zeigt 9.9.9, DB 9.9.9 (version 2, kein ci_change).
- **Soll:** CI-04: geleertes Feld ist danach leer.
- **Ist:** Feld fehlt im PATCH-Body; Erfolg gemeldet.
- **Nutzerwirkung:** Falsche Werte lassen sich über die UI nicht entfernen, ohne Hinweis auf das Scheitern.
- **Ursache (belegt):** optionalValue('') → undefined.
- **Empfehlung:** Geleerte Felder als "" bzw. null senden.
- **Retest (verbindlich):** Vitest (Body enthält das geleerte Feld) und npx playwright test -c e2e-real/playwright.real.config.ts ci-lifecycle -g "L05 clearing" grün.
- **Kodierender Test:** frontend/e2e-real/ci-lifecycle.spec.ts L05

<a id="f-082"></a>
### F-082 (P2) – Instanzattribut-Bearbeitung im CI-Detail sendet kein If-Match

- **Betroffene WPs:** WP-061 · **Komponente:** frontend/src/pages/CIDetailPage.tsx:231; api/client.ts:506 · **Quelle:** Audit/Verifikation
- **Reproduktion:** updateCI.mutate ohne version → client.ts sendet kein If-Match (client.test.ts belegt das). TestOptimisticConcurrency belegt 412 ohne If-Match bei require_if_match. Die UI-Kette wurde nicht ausgeführt.
- **Soll:** WP-061 K3: jede CI-Bearbeitung sendet If-Match.
- **Ist:** Kein If-Match.
- **Nutzerwirkung:** Bei require_if_match schlägt jedes Speichern mit 412 fehl, sonst gilt last-write-wins.
- **Ursache (belegt):** version wird nicht durchgereicht.
- **Empfehlung:** version: ci.version übergeben und 409 anzeigen.
- **Retest (verbindlich):** Vitest: If-Match "7" bei ci.version=7. e2e-real mit require_if_match: Speichern ok.
- **Kodierender Test:** keiner

<a id="f-083"></a>
### F-083 (P2) – Agent-Telemetrie legt nach Merge/Löschung des verknüpften CIs ein Duplikat an

- **Betroffene WPs:** WP-059 · **Komponente:** backend/internal/agent/handler.go:414,455-463; ci/merge.go:239-267 · **Quelle:** Audit/Verifikation + Defekttest-Welle
- **Reproduktion:** Merge(survivor, victim) mit verknüpftem Agent → POST /api/v1/agents/telemetry → 202, neues CI, 2 aktive CIs mit Hostname (TestVerifyB08ProbeAgentMergedPG).
- **Soll:** OVR-02: Automation erzeugt keine Duplikate; merged_into_id folgen.
- **Ist:** Neues CI, der Merge wird damit faktisch rückgängig gemacht.
- **Nutzerwirkung:** Merges agentverwalteter Geräte verfallen. Inventar- und Lizenzzählung werden aufgebläht.
- **Ursache (belegt):** anchorCI liefert nil,nil bei jedem GetByID-Fehler; endpoint_agent.ci_id wird nicht umgehängt.
- **Empfehlung:** MergedInto folgen, Hostname-Fallback; endpoint_agent in mergeReferences aufnehmen; transiente Fehler zurückgeben.
- **Retest (verbindlich):** go test -tags auditdefect -run 'TestAuditT3AgentTelemetryFollowsMergedCI' ./internal/agent/ PASS.
- **Kodierender Test:** backend/internal/agent/audit_t3_agent_merge_auditdefect_test.go TestAuditT3AgentTelemetryFollowsMergedCI

<a id="f-084"></a>
### F-084 (P2) – Spool-Verlustzähler nicht persistiert: Verluste vor Neustart erreichen den Server nie

- **Betroffene WPs:** WP-062 · **Komponente:** edgecore/buffer/buffer.go; collector/collectorcmd/collectorcmd.go:939-955 · **Quelle:** Audit/Verifikation + Defekttest-Welle
- **Reproduktion:** Spool am Limit → DroppedMessages=1. Neue Instanz auf demselben Verzeichnis → 0 (spoolprobe).
- **Soll:** WP-062 K2: jeder Verlust erzeugt Zähler, Event und Meldung an den Server.
- **Ist:** Der Zähler liegt nur im Speicher.
- **Nutzerwirkung:** Datenverlust bei Ausfällen mit Neustart bleibt serverseitig unsichtbar.
- **Ursache (belegt):** Struct-Felder ohne Persistenz; Server loggt nur.
- **Empfehlung:** Verlustsummen atomar persistieren und nach Server-Quittung zurücksetzen.
- **Retest (verbindlich):** cd edgecore && go test -tags auditdefect -count=1 -v -run 'TestAuditT3SpoolLossSurvivesRestart' ./buffer/ PASS.
- **Kodierender Test:** edgecore/buffer/audit_t3_spool_loss_auditdefect_test.go TestAuditT3SpoolLossSurvivesRestart

<a id="f-085"></a>
### F-085 (P2) – Head-of-Line-Blockade im Spool: korrupte Datei oder dauerhaft abgewiesener Batch stoppt jede Zustellung

- **Betroffene WPs:** WP-062 · **Komponente:** edgecore/buffer/buffer.go:135,263-266; collectorcmd.go:660-674,824-853 · **Quelle:** Audit/Verifikation + Defekttest-Welle
- **Reproduktion:** Zerrissene Kopfdatei → PeekBatch-Fehler in jedem Zyklus. Ein Batch mit dauerhaftem 400 → nach 5 Zyklen delivered=0, spoolLen=3, geloggt als „unreachable“ (colprobe).
- **Soll:** Den fehlerhaften Eintrag quarantänisieren, zählen und melden; den Rest zustellen.
- **Ist:** Die Zustellung stockt bis MaxAge, potenziell dauerhaft.
- **Nutzerwirkung:** Ein schlechter Eintrag stoppt still die gesamte Discovery-Lieferung.
- **Ursache (belegt):** Abbruch beim ersten Decode-Fehler; jeder Fehler gilt als transient; 4xx wird gespoolt; nicht-atomares Schreiben.
- **Empfehlung:** Atomar schreiben, korrupte Dateien quarantänisieren, 4xx als Poison behandeln, Altersbereinigung im Flusher.
- **Retest (verbindlich):** cd edgecore && go test -tags auditdefect -run 'TestAuditT3CorruptHeadDoesNotBlockSpool' ./buffer/ PASS; Collector-Test 400/202 (noch nicht kodiert).
- **Kodierender Test:** edgecore/buffer/audit_t3_spool_loss_auditdefect_test.go TestAuditT3CorruptHeadDoesNotBlockSpool

<a id="f-086"></a>
### F-086 (P2) – Client-gescopter Principal kann parent_ci_id auf ein CI eines anderen Clients setzen

- **Betroffene WPs:** WP-079 · **Komponente:** backend/internal/ci/service.go, ci/pg_repository.go (Composite-FK ohne Sichtbarkeitsprüfung) · **Quelle:** Verifikation + Defekttest-Welle
- **Reproduktion:** Update(ClientCtx(Client1), c1, ParentCIID=Client2-CI) → err=nil, gespeichert (Overlay-Probe audit_db_13).
- **Soll:** TEN-05: unsichtbarer Parent → 422 parent_ci_id.
- **Ist:** Wird gespeichert.
- **Nutzerwirkung:** Integritätsbruch zwischen Clients (MSP).
- **Ursache (belegt):** FK-Prüfung umgeht RLS; keine Sichtbarkeitsprüfung (systemisches Muster wie L-T3-15).
- **Empfehlung:** Parent unter RLS lesen und gleichen Client oder org-weiten Parent verlangen.
- **Retest (verbindlich):** go test -tags auditdefect -run 'TestAuditT3ParentMustBeVisibleToScopedPrincipal' ./internal/ci/ PASS.
- **Kodierender Test:** backend/internal/ci/audit_t3_integrity_auditdefect_test.go TestAuditT3ParentMustBeVisibleToScopedPrincipal

<a id="f-087"></a>
### F-087 (P2) – ci.health wird in der G1-Standardkonfiguration nie berechnet (immer 'unknown')

- **Betroffene WPs:** WP-080 · **Komponente:** backend/cmd/server/main.go:606-619; server/modules.go:47-49 · **Quelle:** Audit/Verifikation + Defekttest-Welle
- **Reproduktion:** Real-Binary ohne RETICORA_MODULES, mit frischem Sample: 'worker disabled … monitoring', health=unknown. Mit alerting: 'ok' (SPV/server_g1.log).
- **Soll:** DB-05: health aus Monitoring berechnet (MON-02 gehört zu G1).
- **Ist:** Kein Aufrufer von ci_refresh_health in G1.
- **Nutzerwirkung:** Der Health-Badge zeigt in G1 für alle CIs 'unknown'.
- **Ursache (belegt):** Der Health-Refresh hängt am Worker des P3-Moduls 'alerting'.
- **Empfehlung:** Eigener G1-Worker für den Health-Refresh plus Verdrahtungstest.
- **Retest (verbindlich):** cd backend && go test -tags auditdefect -count=1 -v -run 'TestAuditT3HealthRefreshRunsInG1' ./internal/server/ PASS.
- **Kodierender Test:** backend/internal/server/audit_t3_health_worker_auditdefect_test.go TestAuditT3HealthRefreshRunsInG1

<a id="f-088"></a>
### F-088 (P2) – Subnetze und Interfaces können VLANs eines anderen Clients referenzieren

- **Betroffene WPs:** WP-081 · **Komponente:** backend/internal/ipam/pg_repository.go:135-138,172,428,481 · **Quelle:** Audit/Verifikation + Defekttest-Welle
- **Reproduktion:** ClientCtx(c1): GetVLAN(v2) not found, aber CreateSubnet bzw. CreateInterface mit v2 → err=nil (Overlay, SQL-Probe).
- **Soll:** TEN-05/NET-08: nur sichtbare VLANs, sonst 422/404.
- **Ist:** Wird gespeichert.
- **Nutzerwirkung:** Fremde Netzobjekte werden verknüpft; Berichte zwischen Clients werden inkonsistent.
- **Ursache (belegt):** FK ohne RLS; keine Sichtbarkeitsprüfung.
- **Empfehlung:** VLAN unter RLS lesen; Client muss passen (oder VLAN org-weit sein).
- **Retest (verbindlich):** go test -tags auditdefect -run 'TestAuditT3VLANReferenceMustBeVisible' ./internal/ipam/ PASS.
- **Kodierender Test:** backend/internal/ipam/audit_t3_client_scope_auditdefect_test.go TestAuditT3VLANReferenceMustBeVisible

<a id="f-089"></a>
### F-089 (P2) – Client-gescopte Principals können org-weite VRFs anlegen, umbenennen (auch 'global') und löschen

- **Betroffene WPs:** WP-082 · **Komponente:** backend/migrations/000092_vrf_model.up.sql:13-32; ipam/vrf.go:93-123 · **Quelle:** Verifikation + Defekttest-Welle
- **Reproduktion:** ClientCtx(Client1): CreateVRF, UpdateVRF(default) und DeleteVRF liefern jeweils err=nil (Overlay- und SQL-Probe).
- **Soll:** TEN-05: keine org-weiten Writes durch client-gescopte Nutzer.
- **Ist:** Alle Schreibvorgänge erfolgreich.
- **Nutzerwirkung:** Ein Client-Engineer verändert die Routing-Domänen aller Clients.
- **Ursache (belegt):** Nur die Policy vrf_isolation (Org-Ebene), keine RESTRICTIVE-Policies.
- **Empfehlung:** RESTRICTIVE-Write-Policies wie bei vlan (000091:48-80).
- **Retest (verbindlich):** go test -tags auditdefect -run 'TestAuditT3VRFWritesNeedOrgScope' ./internal/ipam/ PASS.
- **Kodierender Test:** backend/internal/ipam/audit_t3_client_scope_auditdefect_test.go TestAuditT3VRFWritesNeedOrgScope

<a id="f-090"></a>
### F-090 (P2) – Ein fehlschlagender Record verwirft (Queue) bzw. weist ab (HTTP) die gültigen Records derselben Gruppe/Spur

- **Betroffene WPs:** WP-085 · **Komponente:** backend/internal/discovery/consumer.go:975-990; discovery.go:1076 · **Quelle:** Audit/Verifikation + Defekttest-Welle
- **Reproduktion:** 4 Spuren, ein Record außerhalb des Scopes: gültige Records derselben Spur erhalten nak, ab Delivered=5 term, 0 CI-Zeilen. HTTP inline liefert 403, good=0 (TestVerifyB11PoisonRecordOnQueuePath).
- **Soll:** REC-01: jeder Record wird verarbeitet; nur der fehlerhafte wird abgewiesen.
- **Ist:** Gültige Records gehen nach 202 verloren.
- **Nutzerwirkung:** Bereits bestätigte Discovery-Daten werden still verworfen.
- **Ursache (belegt):** processItems stoppt beim ersten Fehler; consumeBatch entscheidet Nak/Term für die ganze Gruppe.
- **Empfehlung:** Fehler je Record isolieren; HTTP-Antwort 202 mit rejected-Zähler.
- **Retest (verbindlich):** go test -tags auditdefect -run 'TestAuditT3PoisonRecordDoesNotTakeValidRecordsDown' ./internal/discovery/ PASS.
- **Kodierender Test:** backend/internal/discovery/audit_t3_poison_record_auditdefect_test.go TestAuditT3PoisonRecordDoesNotTakeValidRecordsDown

<a id="f-091"></a>
### F-091 (P2) – resolved_by und unlicensed_ci-Audit-Actor per Request-Body fälschbar

- **Betroffene WPs:** WP-088 · **Komponente:** backend/internal/reviews/handler.go:101-103; model.go:142; pg_repository.go:177-184 · **Quelle:** Audit/Verifikation + Defekttest-Welle
- **Reproduktion:** U1 löst mit resolved_by=U2 auf → 200; gespeichert und auditiert wird U2 (Full-Router-Probe).
- **Soll:** resolved_by und Audit-Actor sind der Principal.
- **Ist:** Der vom Client gelieferte Wert wird übernommen.
- **Nutzerwirkung:** Der Audit-Trail wird verfälscht, auch bei lizenzrelevanten Entscheidungen.
- **Ursache (belegt):** ResolvedBy wird nur bei leerem Wert gesetzt; ActorID kommt aus dem Body.
- **Empfehlung:** Immer den Principal verwenden, Feld aus dem Request-Schema entfernen bzw. 400 liefern.
- **Retest (verbindlich):** go test -tags auditdefect -run 'TestAuditT3ResolvedByIsThePrincipal' ./internal/server/ PASS.
- **Kodierender Test:** backend/internal/server/audit_t3_router_auditdefect_test.go TestAuditT3ResolvedByIsThePrincipal

<a id="f-092"></a>
### F-092 (P2) – Override-Endpunkt akzeptiert reservierte Namensräume; _instance-Werte dauerhaft zerstört

- **Betroffene WPs:** WP-091 · **Komponente:** backend/internal/override/pg_repository.go:327,613; override/handler.go:67-127 · **Quelle:** Audit/Verifikation + Defekttest-Welle
- **Reproduktion:** PUT …/fields/_instance/override → 200, attributes._instance="x". DELETE → 200, Wert bleibt "x"; 2 Zeilen mit reserviertem Namen.
- **Soll:** GLO-13/E-23: 400 mit Feldbezug.
- **Ist:** 200, Namensräume werden überschrieben.
- **Nutzerwirkung:** Instanzattribute werden irreversibel zerstört.
- **Ursache (belegt):** Nur structuralFields werden abgewiesen; jsonb_set auf den Feldnamen.
- **Empfehlung:** ReservedNames in Repository und Handler ablehnen, CHECK-Constraint, OpenAPI-Pattern.
- **Retest (verbindlich):** go test -tags auditdefect -run 'TestAuditT3OverrideRefusesReservedNames' ./internal/server/ PASS.
- **Kodierender Test:** backend/internal/server/audit_t3_router_auditdefect_test.go TestAuditT3OverrideRefusesReservedNames

<a id="f-093"></a>
### F-093 (P2) – Einheitliche CI-Historie enthält keine location_change-Einträge

- **Betroffene WPs:** WP-171 · **Komponente:** backend/internal/history/pg_repository.go:63-148 · **Quelle:** Audit/Verifikation + Defekttest-Welle
- **Reproduktion:** Raum verschoben → 1 location_change-Zeile; CI-Historie und Location-Historie bleiben bei 0 (probe-history.log).
- **Soll:** WP-171 K1: location_change wird in die Historie vereint.
- **Ist:** UNION enthält nur ci_change, entity_change und audit.
- **Nutzerwirkung:** Ein durch Raum- oder Rack-Umhängen verursachter Umzug ist in der Historie unsichtbar.
- **Ursache (belegt):** location_change fehlt in der UNION; die Nicht-CI-Liste liest nur entity_change.
- **Empfehlung:** UNION-Zweig für location_change von Standort und Vorfahren (zusammen mit L-T3-30).
- **Retest (verbindlich):** go test -tags auditdefect -run 'TestAuditT3HistoryIncludesLocationChange' ./internal/history/ PASS; TestAuditT3HistoryIncludesOverrides bleibt grün.
- **Kodierender Test:** backend/internal/history/audit_t3_location_change_auditdefect_test.go TestAuditT3HistoryIncludesLocationChange

<a id="f-094"></a>
### F-094 (P2) – CI-Historie für alle Standardrollen außer org_admin unbenutzbar (audit:read)

- **Betroffene WPs:** WP-171 · **Komponente:** server/testdata/route_permissions.txt:154; migrations/000073:18-22; CIDetailSections.tsx:577-578 · **Quelle:** Verifikation
- **Reproduktion:** psql: audit:read nur für org_admin. Authz-Subtest belegt 403. Engineer sieht 'Die Historie konnte nicht geladen werden.'
- **Soll:** CI-06: Operatoren mit Lesezugriff auf das CI sehen dessen Historie.
- **Ist:** Für alle anderen Rollen erscheint die Fehlermeldung.
- **Nutzerwirkung:** Die Historie ist faktisch nur für Org-Admins nutzbar.
- **Ursache (belegt):** Die gesamte vereinigte Historie ist an audit:read gebunden.
- **Empfehlung:** Vertrag entscheiden: ci:read für den Änderungsanteil, oder Abschnitt ausblenden.
- **Retest (verbindlich):** API-Test mit engineer/viewer liefert den entschiedenen Inhalt; e2e-real als Engineer ohne Fehlertext.
- **Kodierender Test:** keiner

<a id="f-095"></a>
### F-095 (P2) – AUD-03-ci_change-Schema und -Vokabular nicht umgesetzt (change_kind, field_changes, Quellen-/Request-Korrelation, eine ci_change je Record; 'type_change' statt 'type_changed')

- **Betroffene WPs:** WP-171, WP-172 · **Komponente:** ci_change-Tabelle (chk_ci_change_type), Schreibpfade ci/discovery · **Quelle:** Verifikation (WP-171, WP-172)
- **Reproduktion:** \d ci_change (audit_db_24) zeigt keine AUD-03-Spalten. Ein Ingest-Record erzeugt +3 ci_change und +2 Events; Typwechsel schreibt 'type_change'.
- **Soll:** AUD-03 (06-audit…md:8); CI-11 'ci_change type_changed'.
- **Ist:** Legacy-Schema und -Vokabular.
- **Nutzerwirkung:** Keine Korrelation zu Collector, Request oder Record. Ein Filter auf type_changed findet nichts.
- **Ursache (belegt):** WP-171 hat nur die Lese-UNION umgesetzt (37f9bb5, ohne Migration).
- **Empfehlung:** Folge-WP mit Migration und Writern, oder Entscheidung, die AUD-03 eingrenzt.
- **Retest (verbindlich):** Migrationstest für die AUD-03-Spalten; PG-Ingest-Test: genau eine ci_change je Record; Typwechsel liefert 'type_changed'.
- **Kodierender Test:** keiner

<a id="f-096"></a>
### F-096 (P2) – Review-Auflösung merge bei probable_duplicate löscht weich statt CI-12-Merge

- **Betroffene WPs:** WP-173 · **Komponente:** backend/internal/discovery/review.go:118-146 · **Quelle:** Audit/Verifikation + Defekttest-Welle
- **Reproduktion:** resolveCIReview(merge) → dup gelöscht, merged_into leer, keine ci_merge-Zeile, Referenzen bleiben am Duplikat (probe-review.log).
- **Soll:** CI-12: Referenzen umhängen, Tombstone 301, Snapshot.
- **Ist:** Nur ciRepo.Delete.
- **Nutzerwirkung:** Beim einzigen UI-Merge gehen Beziehungen, Interfaces und Overrides verloren; kein Unmerge möglich.
- **Ursache (belegt):** merge ist als Delete implementiert; reviews/service.go wurde nicht geändert.
- **Empfehlung:** CI-12-Merger mit Job aufrufen.
- **Retest (verbindlich):** go test -tags auditdefect -run 'TestAuditT3ProbableDuplicateMergeIsCIMerge' ./internal/discovery/ PASS.
- **Kodierender Test:** backend/internal/discovery/audit_t3_review_resolution_auditdefect_test.go TestAuditT3ProbableDuplicateMergeIsCIMerge

<a id="f-097"></a>
### F-097 (P2) – Unmerge überschreibt nach dem Merge vorgenommene Survivor-Änderungen still

- **Betroffene WPs:** WP-174 · **Komponente:** backend/internal/ci/merge.go:624-770 · **Quelle:** Audit/Verifikation + Defekttest-Welle
- **Reproduktion:** Merge → Survivor umbenennen → Unmerge → alter Name ist wieder da (Auditor-Probe).
- **Soll:** Kein stiller Verlust späterer Änderungen (oder 409 bzw. Warnung nach Vertrag).
- **Ist:** Ganze Zeilen werden aus dem Snapshot überschrieben.
- **Nutzerwirkung:** Änderungen aus bis zu 30 Tagen gehen ohne Hinweis verloren.
- **Ursache (belegt):** restoreRow ohne Versions- oder Konfliktprüfung.
- **Empfehlung:** Versionsvergleich mit 409 (Option force), Vertrag in OpenAPI dokumentieren.
- **Retest (verbindlich):** go test -tags auditdefect -run 'TestAuditT3UnmergeKeepsLaterSurvivorEdits' ./internal/ci/ PASS.
- **Kodierender Test:** backend/internal/ci/audit_t3_integrity_auditdefect_test.go TestAuditT3UnmergeKeepsLaterSurvivorEdits

<a id="f-098"></a>
### F-098 (P2) – Blast Radius zählt nur über connected_to erreichte CIs ohne Heuristik-Kennzeichen als betroffen

- **Betroffene WPs:** WP-176 · **Komponente:** backend/internal/topology/impact.go:95-146 · **Quelle:** Verifikation + Defekttest-Welle
- **Reproduktion:** /impact/{switch} markiert heuristic=true; /cis/{switch}/blast-radius liefert affected_count=2 ohne Flag (probe-topo.log).
- **Soll:** IMP-03: Heuristik-Treffer überall gekennzeichnet.
- **Ist:** Ohne Kennzeichen und mitgezählt.
- **Nutzerwirkung:** Die Auswirkungsanalyse im CI-Detail überzeichnet den Blast Radius.
- **Ursache (belegt):** GetBlastRadius ignoriert impactHit.heuristic.
- **Empfehlung:** Flag führen oder separat ausweisen; Marker im UI rendern.
- **Retest (verbindlich):** go test -tags auditdefect -run 'TestAuditT3BlastRadiusMarksHeuristicCIs' ./internal/topology/ PASS.
- **Kodierender Test:** backend/internal/topology/audit_t3_blast_radius_auditdefect_test.go TestAuditT3BlastRadiusMarksHeuristicCIs

<a id="f-099"></a>
### F-099 (P2) – GET /cis/{id}/dependencies nutzt rohe Kantenrichtung, invers zu impact_direction (außerhalb WP-176, IMP-07/WP-031)

- **Betroffene WPs:** WP-031, WP-176 · **Komponente:** backend/internal/topology/impact.go:151-176 walkDirected · **Quelle:** Audit/Verifikation (außerhalb WP-Umfang, statusneutral für WP-176)
- **Reproduktion:** vm hosted_on server: upstream(vm)={}, downstream(server)={}, computeImpact(server)={vm} (Replay TestAuditProbeB22DependenciesDirection).
- **Soll:** upstream(vm)={server}.
- **Ist:** Invertiert.
- **Nutzerwirkung:** API-Konsumenten erhalten falsche upstream/downstream-Antworten (derzeit keine UI-Komponente betroffen).
- **Ursache (belegt):** Annahme Kante Provider→Abhängiger.
- **Empfehlung:** An WP-031 (Gruppe T1) übergeben; Richtung aus impactRules ableiten.
- **Retest (verbindlich):** Unit- und PG-Test /dependencies für hosted_on, powered_by, contains.
- **Kodierender Test:** keiner

<a id="f-100"></a>
### F-100 (P2) – CI-Detail zeigt nach „Override aufheben“ bis zum Reload weiter den entfernten Override-Wert

- **Betroffene WPs:** WP-060 · **Komponente:** frontend/src/api/cmdbHooks.ts:296-301 useClearOverride · **Quelle:** Real-Stack-E2E (owned) + Verifikation; WP-Verifikation P2, Real-Stack-Bewertung P3 – P2 übernommen, weil der Kernzweck von WP-060 betroffen ist
- **Reproduktion:** Real-Stack D07: Override setzen → 'Override aufheben' (DELETE 200). Die Übersicht 'Modell' zeigt 30 s lang den alten Wert, die Provenienz bereits den neuen; nach Reload korrekt.
- **Soll:** WP-060/OVR-01: konsistente effektive Sicht sofort nach dem Clear.
- **Ist:** Zwei widersprüchliche Werte auf derselben Seite.
- **Nutzerwirkung:** Nutzer halten das Aufheben für gescheitert oder handeln auf Basis des alten Werts.
- **Ursache (belegt):** Es wird nur ['ci-fields', ciId] invalidiert, nicht ['ci', ciId] bzw. ['cis'].
- **Empfehlung:** ['ci',id] und ['cis'] invalidieren; Vitest ergänzen.
- **Retest (verbindlich):** Vitest CIDetailPage; npx playwright test -c e2e-real/playwright.real.config.ts discovery-override -g "D07" vollständig grün.
- **Kodierender Test:** frontend/e2e-real/discovery-override.spec.ts D07

<a id="f-101"></a>
### F-101 (P2) – Bootstrap über RETICORA_ENROLL_TOKEN / RETICORA_CLOUD_URL nicht implementiert

- **Betroffene WPs:** WP-145 · **Komponente:** collector/collectorcmd (cli.go:43, 60-110) · **Quelle:** Audit/Verifikation WP-145; Defekttest-Welle
- **Reproduktion:** cd /home/user/Reticora-CMDB/collector && go test -tags auditdefect -count=1 -v -run '^TestAuditCollectorEnrollsFromEnvironment$' ./collectorcmd/
- **Soll:** COL-04: Ein Start mit RETICORA_ENROLL_TOKEN und RETICORA_CLOUD_URL führt das CSR-Enrollment durch.
- **Ist:** lifecycle_auditdefect_test.go:401: Ohne Keystore sendet der Collector binnen 15 s keine Enrollment-Anfrage; Enrollment geht nur über 'collector enroll --server --code'.
- **Nutzerwirkung:** Kein unbeaufsichtigtes Selbst-Enrollment für Container- oder automatisierte Deployments.
- **Ursache (belegt):** Der Env-Bootstrap wurde nie implementiert; nur ein Kommentar in edgecore/enrollment/collector.go:75.
- **Empfehlung:** Beim ersten Start ohne Keystore mit gesetztem Token enrollen und persistieren, oder eine Ersatzentscheidung in entscheidungen.md dokumentieren.
- **Retest (verbindlich):** TestAuditCollectorEnrollsFromEnvironment besteht; beim zweiten Start kein Enroll.
- **Kodierender Test:** collector/collectorcmd/lifecycle_auditdefect_test.go:335 TestAuditCollectorEnrollsFromEnvironment (FAILS_AS_EXPECTED)

<a id="f-102"></a>
### F-102 (P2) – CA-Roll-over ist nicht auslösbar und bricht danach das Vertrauen enrollter Collector in das Edge-Serverzertifikat

- **Betroffene WPs:** WP-145 · **Komponente:** backend/internal/discovery/ca.go:238-261, natsedge.go:303-333; collector/collectorcmd/natsconn.go:73 · **Quelle:** Audit/Verifikation WP-145; Defekttest-Welle
- **Reproduktion:** cd /home/user/Reticora-CMDB/backend && TEST_DATABASE_URL=… go test -tags auditdefect -count=1 -p 1 -v -run '^TestAuditCARolloverKeepsEnrolledCollectorTrust$' ./internal/discovery/
- **Soll:** COL-04: CA-Roll-over mit Übergangsphase (zwei Trust-Anker), ohne Ausfall der Collector.
- **Ist:** collector_pki_auditdefect_test.go:197: Nach Rollover und Export gilt 'x509: certificate signed by unknown authority' für einen vorher enrollten Collector. Rollover/RetirePrevious haben weder API noch CLI noch Job.
- **Nutzerwirkung:** Ein CA-Wechsel, etwa nach Schlüsselkompromittierung, ist nur mit Ausfall der ganzen Flotte möglich.
- **Ursache (belegt):** Das Serverzertifikat wird sofort aus der neuen CA ausgestellt, der neue Anker wird nie an die Collector verteilt, und es gibt keinen Bedienpfad.
- **Empfehlung:** Operator-Befehl für Roll-over und Retire. Edge-Zertifikat aus der alten CA weiter ausstellen, bis alle Collector das neue Bundle haben; Bundle über den Kanal verteilen.
- **Retest (verbindlich):** TestAuditCARolloverKeepsEnrolledCollectorTrust besteht. Integrationstest mit echtem nats-server: alter und neuer Collector verbinden sich.
- **Kodierender Test:** backend/internal/discovery/collector_pki_auditdefect_test.go:160 TestAuditCARolloverKeepsEnrolledCollectorTrust (FAILS_AS_EXPECTED)

<a id="f-103"></a>
### F-103 (P2) – Compose veröffentlicht REST-API (8080), Keycloak (8180) und Frontend (3000) auf allen Interfaces, entgegen Firewall-Matrix und COL-08

- **Betroffene WPs:** WP-146 · **Komponente:** deploy/docker-compose/docker-compose.yml:233, 343; docs/betrieb/firewall-matrix.md · **Quelle:** Audit/Verifikation WP-146; Defekttest-Welle
- **Reproduktion:** bash /home/user/Reticora-CMDB/tests/audit/compose-port-policy.test.sh
- **Soll:** COL-08: Inbound nur 443 und 4222 (plus 80); die Doku entspricht dem Deployment.
- **Ist:** FAIL: keycloak 8180->8080 und server 8080->8080 auf 0.0.0.0 (3 passed, 2 failed). Laut compose-ports.txt zusätzlich frontend 3000 auf 0.0.0.0 (vom Test nicht abgedeckt).
- **Nutzerwirkung:** API und Keycloak (inklusive Admin-Konsole) sind ohne TLS aus dem Netz erreichbar, während Betreiber nach der Matrix einen geschlossenen Host annehmen.
- **Ursache (belegt):** Port-Mappings ohne Host-IP; Matrix nicht mit Compose abgeglichen. 8180 wird für IP-Installationen absichtlich gebraucht (install-cloud.sh:583-598), ist aber nicht dokumentiert.
- **Empfehlung:** Mit TLS-Domain 8080/8180/3000 auf 127.0.0.1 binden, Opt-in-Variable für IP-Installationen, Ausnahme in der Matrix dokumentieren.
- **Retest (verbindlich):** compose-port-policy.test.sh → 0 failed (Test um Port 3000 erweitert).
- **Kodierender Test:** tests/audit/compose-port-policy.test.sh (FAILS)

<a id="f-104"></a>
### F-104 (P2) – Paralleler Edge-PKI-Export mehrerer Server-Replikas hinterlässt ein nicht passendes oder fehlendes Server-Schlüsselpaar; Schlüsseldatei 0644

- **Betroffene WPs:** WP-146 · **Komponente:** backend/internal/discovery/natsedge.go:303-333, 362-388 · **Quelle:** Audit/Verifikation WP-146; Defekttest-Welle
- **Reproduktion:** cd /home/user/Reticora-CMDB/backend && TEST_DATABASE_URL=… go test -tags auditdefect -count=1 -p 1 -v -run '^TestAuditConcurrentEdgePKIExportConsistent$' ./internal/discovery/
- **Soll:** Genau ein konsistentes Schlüsselpaar, Schlüssel ≤0640, nats-edge startet.
- **Ist:** collector_pki_auditdefect_test.go:250: 56–58 von 60 parallelen Exporten scheitern (rename/write .tmp); :253 server-key.pem hat Modus 644. Verifier: 3 von 200 Paaren passen nicht zusammen.
- **Nutzerwirkung:** Im k8s-Prod-Overlay (3 Replikas, RWX-PVC) können nach einem Rollout alle Collector offline sein, bis jemand das PKI-Volume manuell bereinigt.
- **Ursache (belegt):** Kein Lock und keine Leader-Wahl, fester .tmp-Name, Schlüssel und Zertifikat getrennt geschrieben, keine Konsistenzprüfung beim Weiterverwenden.
- **Empfehlung:** pg_advisory_lock, eindeutige Temp-Namen, atomarer Tausch von Schlüssel und Zertifikat, tls.X509KeyPair-Prüfung, Modus 0600/0640.
- **Retest (verbindlich):** TestAuditConcurrentEdgePKIExportConsistent besteht (0 Fehler, 0 Mismatches, Modus ≤0640).
- **Kodierender Test:** backend/internal/discovery/collector_pki_auditdefect_test.go:201 TestAuditConcurrentEdgePKIExportConsistent (FAILS_AS_EXPECTED)

<a id="f-105"></a>
### F-105 (P2) – SNMPv3-Credential-Fehler unsichtbar: usmStats-Reports werden als 'kein Gerät' behandelt

- **Betroffene WPs:** WP-152, WP-155 · **Komponente:** collector/plugins/snmp (Probe/v3.go) · **Quelle:** Verifikation WP-155 (SNMPv3-Teil); Defekttest-Welle
- **Reproduktion:** cd /home/user/Reticora-CMDB/collector && go test -tags auditdefect -count=1 -v -run '^TestAuditSNMPv3AuthFailureReported$' ./plugins/snmp/
- **Soll:** DIS-08: Ein Authentifizierungsfehler wird als auth_failed mit Ziel gemeldet.
- **Ist:** v3_auth_failure_auditdefect_test.go:128: Der Agent lehnt mit 2 usmStatsWrongDigests ab, Probe liefert 0 Treffer mit err=nil. Der bestehende Test snmp_test.go:430-434 schreibt dieses Verhalten sogar fest.
- **Nutzerwirkung:** Bei falschen v3-Zugangsdaten fehlen Geräte still; Health und Metrik bleiben ok.
- **Ursache (belegt):** Die Probe bildet usmStats-Reports auf 'nicht gefunden' ab.
- **Empfehlung:** usmStatsWrongDigests und UnknownUserNames als auth_failed-runError melden; snmp_test.go:430-434 anpassen.
- **Retest (verbindlich):** TestAuditSNMPv3AuthFailureReported besteht; die Scope-Health zeigt auth_failed.
- **Kodierender Test:** collector/plugins/snmp/v3_auth_failure_auditdefect_test.go:85 TestAuditSNMPv3AuthFailureReported (FAILS_AS_EXPECTED)

<a id="f-106"></a>
### F-106 (P2) – Scopes mit mehr als ~4096 Adressen werden vom Collector still gekappt

- **Betroffene WPs:** WP-153 · **Komponente:** collector/collectorcmd/collectorcmd.go:502-521, scopes.go:182; backend/internal/discovery/scope.go:39 · **Quelle:** Verifikation WP-153; Defekttest-Welle
- **Reproduktion:** cd /home/user/Reticora-CMDB/collector && go test -tags auditdefect -count=1 -v -run '^TestAuditScopeExpansionCompleteOrReported$' ./collectorcmd/
- **Soll:** Ein von der API akzeptierter Scope wird vollständig gescannt, oder die API lehnt ihn ab; eine Kappung wird als Scope-Fehler gemeldet.
- **Ist:** discovery_auditdefect_test.go:367: /16 → 4095 von 65534 Adressen, Bereich 10.0.0.0-10.0.255.255 → 4097 von 65536, ohne Fehler im Lauf.
- **Nutzerwirkung:** Rund 94 % eines /16-Scopes werden nie entdeckt, während die Health ok zeigt; das Onboarding schlägt sogar /16-Netze vor.
- **Ursache (belegt):** Backend erlaubt bis 65536 Adressen, Collector kappt bei 4096; die Kappung erzeugt keinen runError.
- **Empfehlung:** Grenzen angleichen und die Kappung als runError melden.
- **Retest (verbindlich):** TestAuditScopeExpansionCompleteOrReported besteht.
- **Kodierender Test:** collector/collectorcmd/discovery_auditdefect_test.go:335 TestAuditScopeExpansionCompleteOrReported (FAILS_AS_EXPECTED)

<a id="f-107"></a>
### F-107 (P2) – Scope-Formular kann Credentials, VRF, Site, Plugins und Namen nicht setzen

- **Betroffene WPs:** WP-153 · **Komponente:** frontend/src/pages/DiscoveryPage.tsx:263-346; frontend/src/api/client.ts:210-218 · **Quelle:** Audit/Verifikation WP-153
- **Reproduktion:** Auf /discovery 'Scope anlegen' öffnen: Es gibt nur collector/kind/target/schedule; der Vitest prüft genau diesen Payload. Eine Credential-Seite fehlt in frontend/src/pages.
- **Soll:** DIS-09: Die Scope-Felder site, VRF, credential_ids und plugins sind für Nutzer pflegbar.
- **Ist:** Nur per Roh-API möglich.
- **Nutzerwirkung:** Discovery mit Credentials oder VRF lässt sich in der UI nicht konfigurieren; zusammen mit L-T4-06 brechen Scopes aus der UI die Credential-Discovery.
- **Ursache (belegt):** UI auf ein Minimalformular beschränkt.
- **Empfehlung:** Formular erweitern (Site, VRF, Plugins, Credential-Auswahl), Löschbestätigung, Berechtigungsprüfung in der UI.
- **Retest (verbindlich):** Vitest sendet site_id/vrf_id/credential_ids/plugins; e2e-real prüft die DB-Zeile.
- **Kodierender Test:** keiner (nicht kodifiziert: ohne Zieldesign wären die Bedienelemente geraten)

<a id="f-108"></a>
### F-108 (P2) – Zoniertes IPv6-Scope-Ziel wird akzeptiert; Offline-Detektor und Kanten-Cleanup brechen für alle danach sortierten Orgs ab

- **Betroffene WPs:** WP-153, WP-157, WP-159 · **Komponente:** backend/internal/discovery/scope.go:269-280; backend/internal/lifecycle/offline.go:82-86, 144-147; backend/internal/relationship/cleanup.go:132-136, 180-183 · **Quelle:** Verifikation WP-157, WP-159; Defekttest-Welle
- **Reproduktion:** cd /home/user/Reticora-CMDB/backend && TEST_DATABASE_URL=… go test -tags auditdefect -count=1 -p 1 -v -run '^(TestAuditZonedScopeTargetRefused|TestAuditBadScopeIsolated)$' ./internal/discovery/
- **Soll:** Jeder Mandant wird unabhängig geprüft; die API lehnt Ziele ab, die das SQL nicht auswerten kann.
- **Ist:** zone_isolation_auditdefect_test.go:75: POST range 'fe80::1%eth0-fe80::5%eth0' → 201 (ebenso host). :133: Die 400 h alte CI von Org B bleibt nach Run() 'active' (22P02). :144: Die 30 Tage alte Kante von Org B bleibt bestehen. Die RunOrg-Kontrollen bestehen.
- **Nutzerwirkung:** Jeder Tenant-Admin kann, auch versehentlich, die Offline-Erkennung und den Topologie-Cleanup für einen unvorhersehbaren Teil anderer Mandanten still abschalten.
- **Ursache (belegt):** netip akzeptiert Zonen, PostgreSQL inet nicht; die inet-Casts sind ungeschützt; Run kehrt beim ersten Org-Fehler zurück.
- **Empfehlung:** Zonen in NormalizeScopeTarget ablehnen; in Run Fehler je Org sammeln (errors.Join) und weiterlaufen.
- **Retest (verbindlich):** Beide Defekttests bestehen.
- **Kodierender Test:** backend/internal/discovery/zone_isolation_auditdefect_test.go:51 TestAuditZonedScopeTargetRefused, :81 TestAuditBadScopeIsolated (FAILS_AS_EXPECTED)

<a id="f-109"></a>
### F-109 (P2) – Scope-Zeitpläne (Cron/Intervall) werden beim Collector-Start ignoriert: jeder Scope läuft sofort und nach jedem Neustart

- **Betroffene WPs:** WP-154 · **Komponente:** collector/collectorcmd/schedule.go:95-104, collectorcmd.go:396; backend/internal/discovery/scope_handler.go:289 · **Quelle:** Audit/Verifikation WP-154; Defekttest-Welle
- **Reproduktion:** cd /home/user/Reticora-CMDB/collector && go test -tags auditdefect -count=1 -v -run '^TestAuditScopeScheduleSurvivesRestart$' ./collectorcmd/
- **Soll:** DIS-09/10: Ein Cron-Scope '0 2 * * *' läuft um 02:00; ein 24-h-Scope läuft 24 h nach dem letzten Lauf.
- **Ist:** discovery_auditdefect_test.go:405: Der Cron-Scope ist um 10:07 fällig (swept=true); ein 24-h-Scope, der vor 1 h lief, ist nach dem Neustart fällig. Die Kontrolle (nie gelaufen) besteht.
- **Nutzerwirkung:** Unerwartete Scan-Last tagsüber in Produktionsnetzen, auch nach jedem Auto-Update-Neustart.
- **Ursache (belegt):** In-Memory-Scheduler ohne Initialisierung aus next_run_at; CollectorScope überträgt kein next_run_at/last_run_at.
- **Empfehlung:** next_run_at/last_run_at mitsenden und den Scheduler daraus initialisieren; Cron zuerst bei cron.next(now) ausführen.
- **Retest (verbindlich):** TestAuditScopeScheduleSurvivesRestart besteht.
- **Kodierender Test:** collector/collectorcmd/discovery_auditdefect_test.go:376 TestAuditScopeScheduleSurvivesRestart (FAILS_AS_EXPECTED)

<a id="f-110"></a>
### F-110 (P2) – Installierter Collector passt nicht zum Enroll-Befehl der UI (Binary-Name und Credentials-Pfad abweichend)

- **Betroffene WPs:** WP-156 · **Komponente:** install-vm.sh:456, 607; collector/Dockerfile:19, 31; edgecore/enrollment/enrollment.go:18; deploy/README.md:227 vs. docs/README.md:595 · **Quelle:** Audit/Verifikation WP-156; Defekttest-Welle
- **Reproduktion:** bash /home/user/Reticora-CMDB/tests/audit/collector-install-cli.test.sh
- **Soll:** INS-06/WP-156: ein einheitlicher Konfigurationspfad; der UI-Befehl funktioniert auf dem installierten Collector.
- **Ist:** FAIL: collector.env setzt /opt/reticora-collector/credentials.json, das Enroll schreibt nach /var/lib/…; kein 'reticora-collector' im PATH; das Image liefert /usr/local/bin/collector (3 ok, 3 FAIL).
- **Nutzerwirkung:** Zero-Config-Onboarding auf den unterstützten Installationspfaden scheitert ('command not found') oder die Identität landet dort, wo der Dienst sie nicht liest.
- **Ursache (belegt):** Installer, Image und Doku wurden nicht auf Pfad und Binary-Namen aus WP-156 angeglichen.
- **Empfehlung:** Einheitlich /var/lib/reticora-collector/credentials.json, /usr/local/bin/reticora-collector installieren bzw. verlinken (auch im Image), Doku angleichen.
- **Retest (verbindlich):** collector-install-cli.test.sh → 0 FAIL; e2e auf installierter VM.
- **Kodierender Test:** tests/audit/collector-install-cli.test.sh (FAILS)

<a id="f-111"></a>
### F-111 (P2) – Cleanup behandelt Cron-Zeitpläne als 24 h; Kanten wöchentlicher Scopes werden nach 7 Tagen gelöscht

- **Betroffene WPs:** WP-159 · **Komponente:** backend/internal/relationship/cleanup.go:237-252 · **Quelle:** Audit/Verifikation WP-159; Defekttest-Welle
- **Reproduktion:** cd /home/user/Reticora-CMDB/backend && TEST_DATABASE_URL=… go test -tags auditdefect -count=1 -p 1 -v -run '^TestAuditCleanupCronScopeDeadline$' ./internal/relationship/
- **Soll:** TOP-02: Frist max(7 d, 5× Intervall) = 35 d für wöchentliches Cron.
- **Ist:** cleanup_cron_auditdefect_test.go:93: Die 10 Tage alte Kante beim Cron '0 3 * * 0' wird entfernt (stale_after=168h); ebenso eine 36 Tage alte Kante beim monatlichen Cron. Die Kontrolle mit 168h-Duration behält die Kante.
- **Nutzerwirkung:** Kanten verschwinden zwischen den Sweeps und kommen mit neuer ID wieder; Notizen, Attribute und Historie gehen verloren, und es entstehen unechte removed/created-Events.
- **Ursache (belegt):** Jeder Nicht-Duration-Zeitplan wird auf defaultSweep (24 h) abgebildet; anders als offline.go:101-121 werden last_run_at/next_run_at nicht genutzt.
- **Empfehlung:** Die Run-Gap-Logik aus lifecycle/offline.go gemeinsam nutzen.
- **Retest (verbindlich):** TestAuditCleanupCronScopeDeadline besteht; TestAuditCleanupDeadlinePerScopeKind bleibt PASS.
- **Kodierender Test:** backend/internal/relationship/cleanup_cron_auditdefect_test.go:32 TestAuditCleanupCronScopeDeadline (FAILS_AS_EXPECTED)

<a id="f-112"></a>
### F-112 (P2) – Redundanzflag von powered_by hängt von Batch- und Lane-Reihenfolge ab und wird nie korrigiert (im Real-Stack falsch)

- **Betroffene WPs:** WP-161 · **Komponente:** backend/internal/discovery/power.go:112-150, topology.go:313-323, lanes.go · **Quelle:** Verifikation WP-161; Defekttest-Welle
- **Reproduktion:** cd /home/user/Reticora-CMDB/backend && TEST_DATABASE_URL=… go test -tags auditdefect -count=1 -p 1 -v -run '^TestAuditPowerRedundancyIndependentOfBatchOrder$' ./internal/discovery/
- **Soll:** Alle 32 Kanten Server→PDU der 16 doppelt gespeisten Server haben redundant=true, unabhängig von der Reihenfolge.
- **Ist:** power_chain_auditdefect_test.go:112: Server→sim-pdu-a 16 Kanten, redundant=true auf 0. Real-Stack: 0 bis 16 von 32 korrekt, im Onboarding-Org 0 von 32.
- **Nutzerwirkung:** Konsumenten von attributes.redundant (API, Export) sehen doppelt gespeiste Server als einfach gespeist; IMP-04 ist dank Typregel nicht betroffen.
- **Ursache (belegt):** Das Flag wird aus den Feeds des aktuellen Batches berechnet; upsertRelationship bestätigt nur und aktualisiert keine Attribute; parallele Lanes trennen die PDU-Records.
- **Empfehlung:** Flag je gespeister CI aus allen gespeicherten powered_by-Kanten neu berechnen (oder beim Lesen ableiten) und die Geschwisterkanten aktualisieren.
- **Retest (verbindlich):** TestAuditPowerRedundancyIndependentOfBatchOrder besteht; Real-Stack 32/32.
- **Kodierender Test:** backend/internal/discovery/power_chain_auditdefect_test.go:47 TestAuditPowerRedundancyIndependentOfBatchOrder (FAILS_AS_EXPECTED)

<a id="f-113"></a>
### F-113 (P2) – Der SIM-01-Demo-Admin admin@demo.local kann sich nicht anmelden; die geseedete Demo-Org ist über die UI nicht erreichbar

- **Betroffene WPs:** WP-203 · **Komponente:** deploy/keycloak/realm-reticora-dev.json:258-285; backend/cmd/seed/seed.go:166-170 · **Quelle:** Audit/Verifikation WP-203; Defekttest-Welle
- **Reproduktion:** bash /home/user/Reticora-CMDB/tests/audit/demo-realm-users.test.sh
- **Soll:** SIM-01: Die Demo-Org (Pro) ist als admin@demo.local bedienbar (ebenso acme).
- **Ist:** FAIL: Im Realm gibt es weder admin@demo.local noch admin@acme.local; einziger Nutzer ist admin@reticora.local (Org 000…001).
- **Nutzerwirkung:** Demos sowie manuelle und Real-E2E-Tests können die SIM-01-Daten nicht über die UI nutzen.
- **Ursache (belegt):** Der Seed kann keine IdP-Nutzer anlegen, und der Dev-Realm wurde nicht um Nutzer mit den deterministischen Org-IDs erweitert.
- **Empfehlung:** Beide Nutzer mit organization_id md5('/org/demo') bzw. md5('/org/acme') in den Realm aufnehmen oder eine Provisionierung nach make seed automatisieren.
- **Retest (verbindlich):** demo-realm-users.test.sh → 0 FAIL; e2e-real-Login als admin@demo.local sieht 40 CIs.
- **Kodierender Test:** tests/audit/demo-realm-users.test.sh (FAILS)

<a id="f-114"></a>
### F-114 (P2) – JSONB-Prädikate unter RLS nicht indexfähig: Attribut-Ausdrucksindex ungenutzt, Identitätssuche mit MACs als org-weiter Seq Scan

- **Betroffene WPs:** WP-204, WP-205 · **Komponente:** backend/internal/discovery/pg_repository.go:903-946; backend/internal/ci/pg_repository.go:207-233; citype/ddl_job.go:65-89 · **Quelle:** Audit/Verifikation WP-204, WP-205; Defekttest-Welle
- **Reproduktion:** cd /home/user/Reticora-CMDB/backend && TEST_DATABASE_URL=… go test -tags auditdefect -count=1 -p 1 -v -run '^TestAuditIdentitySearchWithMACsUsesIndexes$' ./internal/discovery/ (Attribut-Teil: SP/work/verify-B26/probe.sql)
- **Soll:** NFR-01/REC-02/ABN-02: Identitätssuche und indizierte Attributfilter nutzen Indizes auch für die App-Rolle.
- **Ist:** identity_plan_auditdefect_test.go:156: Identitäts-Lookup mit MAC → 'Seq Scan on ci' unter RLS (10k CIs); die Kontrolle mit Serial+IP bleibt Index-only. Attribut: Als Owner Index Scan (0,70 ms), als reticora_app Seq Scan (6,25 ms, 10.119 Zeilen verworfen).
- **Nutzerwirkung:** Kosten linear zur Org-Größe: Auf dem Queue-Pfad entsteht je wiederentdecktem Gerät mit MACs ein Org-Scan, und das Versprechen 'indizierter Attributfilter' gilt nicht. Das gefährdet die Reserve für NFR-03 und NFR-02.
- **Ursache (belegt):** jsonb_contains (@>) und jsonb_object_field_text (->>) sind nicht leakproof und werden unter der RLS-Barriere nicht zu Index-Quals; ein einziger solcher Term kippt das ganze OR (SR-04-Klasse).
- **Empfehlung:** MACs in eine Seitentabelle oder Array-Spalte mit nutzbarem Index auslagern; durchsuchbare Attribute als gespeicherte Spalte oder geprüften leakproof-Wrapper umsetzen; SR-04 und lasttest-g1.md korrigieren.
- **Retest (verbindlich):** TestAuditIdentitySearchWithMACsUsesIndexes besteht. Ein neuer EXPLAIN-Test auf dem Produkt-SQL des Attributfilters zeigt 'Index Scan using idx_attr_<id>' für die App-Rolle.
- **Kodierender Test:** backend/internal/discovery/identity_plan_auditdefect_test.go:72 TestAuditIdentitySearchWithMACsUsesIndexes (FAILS_AS_EXPECTED); Attribut-Teil: keiner

<a id="f-115"></a>
### F-115 (P2) – Die EXPLAIN-Prüfung nach ABN-02 in TestCIQueryBudget ist vakuös: sie akzeptiert einen Org-Index-Scan über die ganze Org und prüft kein Produkt-SQL

- **Betroffene WPs:** WP-204 · **Komponente:** backend/internal/ci/querybudget_integration_test.go:55-168 · **Quelle:** Audit/Verifikation WP-204
- **Reproduktion:** SP/work/verify-B26/probe-testform.sql: Fixture wie im Test, als reticora_app `attributes @> '{"rack":"R7"}'` → 'Index Scan using idx_ci_org … Rows Removed by Filter: 9790'; planUsesSeqScan meldet false, der Test besteht.
- **Soll:** ABN-02: Die EXPLAIN-Prüfung schlägt fehl, wenn ein zentraler Query keinen passenden Index nutzt.
- **Ist:** Die Prüfung besteht ohne jeden Attribut- oder Identitätsindex; die Hostname-Prüfung nutzt lower(hostname) statt der Produktspalte hostname_lc.
- **Nutzerwirkung:** Regressionen, die Identitäts- oder Attributindizes entfernen, fallen nicht auf.
- **Ursache (belegt):** planUsesSeqScan lehnt nur den Knoten 'Seq Scan' ab; die Abfragen sind nachgebaut statt aus dem Repository erfasst.
- **Empfehlung:** Produkt-SQL per pgx-Tracer erfassen, als App-Rolle EXPLAINen und konkrete Indexnamen sowie eine Obergrenze für gelesene Zeilen prüfen; Negativfall mit gelöschtem Index.
- **Retest (verbindlich):** Der neue EXPLAIN-Test schlägt bei gelöschtem Index fehl und besteht mit Index.
- **Kodierender Test:** keiner (nicht kodifiziert: braucht ein DDL-Job-Fixture)

<a id="f-116"></a>
### F-116 (P2) – Das Suchbudget nach NFR-02 wird am Namensfilter der CI-Liste gemessen statt an der globalen Suche; Szenarien für Attributfilter und Beziehungssuche fehlen

- **Betroffene WPs:** WP-204, WP-206 · **Komponente:** tests/load/api.js:74-87; .github/workflows/load.yml · **Quelle:** Audit/Verifikation WP-204
- **Reproduktion:** tests/load/api.js:74-77 ruft GET /cis?search= (name ILIKE) auf; die UI-Suche ruft /api/v1/search (frontend client.ts:1381-1384). Der Seed schreibt keine CI-Suchdokumente.
- **Soll:** Das Suchbudget (<500 ms bei 10k) wird an der Suche gemessen, die Nutzer verwenden, gegen einen indizierten Korpus; dazu JSONB-Attributfilter und Beziehungssuche.
- **Ist:** Die globale Suche wird nie unter Last getestet, ihr Korpus ist für geseedete CIs leer; der Attributfilter wird nur über Spaltenfilter gemessen.
- **Nutzerwirkung:** Die NFR-02-Konformität der globalen Suche ist unbelegt, obwohl SR-04 ein unter RLS unbrauchbares FTS-Risiko nennt.
- **Ursache (belegt):** Design des Harness; der Seed umgeht den Indexer (siehe L-T4-53).
- **Empfehlung:** In load.yml nach dem Seed reindexieren; Szenarien global_search, attr_filter und relationships mit p95<500 ms ergänzen.
- **Retest (verbindlich):** Die neuen k6-Szenarien bestehen wiederholt auf dem Referenzhost.
- **Kodierender Test:** keiner (nicht kodifiziert: k6-Harness)

<a id="f-117"></a>
### F-117 (P2) – Die p95-Budgets nach NFR-02 für list und search werden nicht robust eingehalten (2 von 3 Läufen auf der akzeptierten 4-vCPU-Klasse verfehlt)

- **Betroffene WPs:** WP-206 · **Komponente:** GET /api/v1/cis (backend/internal/ci), tests/load/api.js · **Quelle:** Audit/Verifikation WP-206; Lastlauf
- **Reproduktion:** Seed 10.000, ANALYZE, Server mit 4 Ingest-Lanes; k6 run -e DURATION=2m -e OBJECTS=10000 tests/load/api.js (SP/work/verify-B26/api-rerun.sh).
- **Soll:** p95 list < 500 ms und search < 500 ms.
- **Ist:** Lauf 1: 717/581 ms ✗; Replikat der Verifikation: 646/534 ms ✗; Lauf 2 (ruhiger Host): 450/348 ms ✓. Serverseitig GET /cis p95 518 ms im Fehlerfall. Alle übrigen Budgets werden in allen Läufen eingehalten.
- **Nutzerwirkung:** Die NFR-02-Konformität bei 10k Objekten hängt von der Hostlast ab; der G1-Nachweis stützt sich auf extern nicht prüfbare Läufe (E-58).
- **Ursache (Hypothese):** Hypothese: CPU-Sättigung durch den gemischten Szenario-Mix (k6, Server und PG auf demselben Host) plus COUNT(*) und Sortierung über die Org je Request; nicht profiliert.
- **Empfehlung:** GET /cis unter Mischlast profilieren (pg_stat_statements, auto_explain), COUNT/Sortierung beheben oder budgetieren (Folge-WP mit ID), Läufe auf dem Referenzhost (SR-06) mit Artefakten wiederholen.
- **Retest (verbindlich):** Drei aufeinanderfolgende saubere Läufe mit exit 0, list und search p95 < 500 ms, Artefakte unter audit/…/evidence/load/.
- **Kodierender Test:** keiner (k6-Schwellen in tests/load/api.js)

<a id="f-118"></a>
### F-118 (P2) – Der Nachweis zu NFR-03 ist nicht repräsentativ: Ingest nur mit neuen Geräten ohne MACs und Interfaces, keine Wiederentdeckung

- **Betroffene WPs:** WP-205, WP-206 · **Komponente:** tests/load/ingest.js:118-127 · **Quelle:** Audit/Verifikation WP-206
- **Reproduktion:** tests/load/ingest.js:118-127 baut Records nur mit {serial_number, hostname} für neue Geräte.
- **Soll:** NFR-03 (200 rec/s, <30 s) gilt für realistische Collector-Records einschließlich Updates bekannter Geräte.
- **Ist:** Gemessen wird nur der Batch-Pfad für neue Geräte ohne MACs (12.000/0 fehlend, max 2,25 s).
- **Nutzerwirkung:** Der Durchsatz im realen Wiederentdeckungsbetrieb kann mit wachsender Org unter 200 rec/s fallen (siehe L-T4-26); das Risiko ist unquantifiziert.
- **Ursache (Hypothese):** Design des Harness; die Auswirkung auf den Durchsatz ist eine Hypothese (die Plankosten je Record sind per EXPLAIN belegt).
- **Empfehlung:** Szenario 'rediscover' mit SNMP-artigen Records (primäre MAC, Interface-MACs, LLDP) für die geseedeten Geräte bei 200 rec/s.
- **Retest (verbindlich):** k6 ingest.js SCENARIO=rediscover besteht (delay < 30 s, 0 neue CIs, integrity-check = 0).
- **Kodierender Test:** keiner

<a id="f-119"></a>
### F-119 (P2) – Internes Review stuft SR-11 als 'Low' ein, obwohl CI-13 [B] in der Produktionsverdrahtung 501 liefert

- **Betroffene WPs:** WP-209 · **Komponente:** docs/security/review-g1.md:145 (SR-11 Low, offen); Bewertungsschema docs/audit/README.md:14 · **Quelle:** Audit (B27) + Verifikation + Defekttest-Welle T5
- **Reproduktion:** review-g1.md:145 führt SR-11 als Low/offen; nach docs/audit/README.md:14 ('High: Kernfunktion falsch …, blockiert ein Gate') wäre SR-11 High, da CI-13 [B] in der Produktion nicht funktioniert (Kontrollprobe SP/work/verify-B27/sr11-verify.log: bare-pg 200, main.go-Kette 501). Kodiert: tests/audit/g1-gate-evidence.test.sh Fall 'known_defect_SR-11: SR-11 (CI-13 [B] unusable in production) is rated High or Critical' FAIL ('severity: Low').
- **Soll:** Offene Punkte werden als Befunde mit Severity nach dem projekteigenen Schema geführt; SR-11 als High (gate-blockierend) oder behoben.
- **Ist:** SR-11 als Low; das Review ist freigegeben.
- **Nutzerwirkung:** Die Sicherheitsfreigabe unterschätzt einen Defekt in einer [B]-Kernfunktion; die Gate-Entscheidung stützt sich auf eine falsche Gewichtung.
- **Ursache (belegt):** Das Review hat den [B]-Charakter von CI-13 und die Produktionsverdrahtung (Decorator-Kette, L-T5-05) nicht gewichtet.
- **Empfehlung:** SR-11 auf High setzen und vor G1 beheben (L-T5-05) oder CI-13 im Gate als FAIL führen; Review um Verweis auf den main.go-Kettentest ergänzen.
- **Retest (verbindlich):** ./tests/audit/g1-gate-evidence.test.sh Fall known_defect_SR-11 (Assertion 2) ok bzw. SR-11 als behoben mit grünem TestAuditT5CIReactivationThroughProductionDecoratorChain.
- **Kodierender Test:** tests/audit/g1-gate-evidence.test.sh – known_defect_SR-11 (Assertion 2, fails-as-expected).

<a id="f-120"></a>
### F-120 (P2) – Gate-Checkliste und Abnahmeprotokoll führen Anforderungen allein per Erklärung des Auftraggebers als PASS

- **Betroffene WPs:** WP-105, WP-210, WP-212, WP-213 · **Komponente:** docs/acceptance/g1-gate.md:154-170 und Anhang (PASS per Erklärung); docs/acceptance/abn-01.md:11-12 · **Quelle:** Audit (B27, WP-213-Befund 'PASS-Status ohne Test-/Traceability-Nachweis') + Verifikation + Defekttest-Welle T5
- **Reproduktion:** tests/audit/g1-gate-evidence.test.sh Fall pass_by_declaration FAIL: 'ABN-01 | PASS (d, e; a, b durch Erklärung des Auftraggebers)', 'AUT-05 | PASS (Erklärung des Auftraggebers)', 'AUT-06 …', 'GATE-02 | PASS (Erklärung des Auftraggebers für B4, B6)', 'GATE-03 | PASS (Erklärung des Auftraggebers für den Pentest)', 'NET-04 …', 'NFR-08 …', 'NFR-09 …' (in dieser Synthese erneut ausgeführt, SP/work/synth-T5/g1-gate-evidence.log).
- **Soll:** WP-213 AK3: 'PASS-Nachweis per Test/Traceability'; nicht Belegtes steht auf offen bzw. als begründete Ausnahme außerhalb von PASS (GATE-03 erst nach Pentest-Nachweis, NFR-09 erst mit Subprozessorenliste, ABN-01 a/b erst mit eingefrorenen Texten).
- **Ist:** 8 [B]/[Q]-IDs stehen allein per Erklärung auf PASS; die Gate-Checkliste meldet 'G1 bestanden'.
- **Nutzerwirkung:** Der Gate-Entscheid ist für Dritte nicht überprüfbar; fehlende Lieferungen (Pentest, v2-Texte, Subprozessoren) sind hinter PASS verdeckt.
- **Ursache (belegt):** Die Freigabe des Auftraggebers ist als PASS modelliert; es gibt keinen eigenen Status für erklärungsbasierte Abnahmen.
- **Empfehlung:** Eigenen Status 'ACCEPTED-BY-DECLARATION' bzw. 'offen' einführen, Gate-Ergebnis als 'bedingt' kennzeichnen und die fehlenden Lieferungen (L-T5-07, L-T5-27, L-T5-28) explizit referenzieren.
- **Retest (verbindlich):** ./tests/audit/g1-gate-evidence.test.sh Fall pass_by_declaration ok (keine [B]/[Q]-ID allein per Erklärung auf PASS).
- **Kodierender Test:** tests/audit/g1-gate-evidence.test.sh – pass_by_declaration (fails-as-expected).

<a id="f-121"></a>
### F-121 (P2) – Traceability-Prüfung akzeptiert jeden existierenden Pfad als 'testdatei'; G1-Vollständigkeit 219/219 nur über Selbstverweise auf die Gate-Checkliste

- **Betroffene WPs:** WP-005, WP-213 · **Komponente:** backend/internal/traceability/traceability.go:333-369 (Validate; testdatei-Prüfung :363-368 nur Pfadform und os.Stat); docs/traceability.csv (5 Zeilen ';docs/acceptance/g1-gate.md;' für AUT-05, AUT-06, GATE-04, NET-04, NFR-08, eingefügt in fa76b16) · **Quelle:** Audit (B01, B27) + Verifikation (Overlay-Probes) + Defekttest-Welle T5 (mechanisch verifiziert)
- **Reproduktion:** Overlay-Probes (go test -overlay, SP/work/verify-B01/wp005/): CSV ohne g1-gate.md-Zeilen → TestTraceabilityFile FAIL 'gate G1: 214 of 219', '[B]/[Q] IDs without traceability row: AUT-05 AUT-06 GATE-04 NET-04 NFR-08'; CSV plus 'GLO-11;[Q];A;README.md;WP-005' und 'DOD-01;[Q];A;docs/;WP-005' → PASS. WP-213-Probe: TEN-09-Zeilen durch Verweis auf g1-gate.md ersetzt → PASS 219/219 (SP/work/verify-B27/trace-negative.log). Kodiert: TestAuditT5ValidateRejectsNonTestEvidence ('Validate accepted testdatei docs/x.md', ebenso 'docs/', 'README.md') und TestAuditT5GateIDsAreNotTracedByTheChecklistItself ('5 [B]/[Q] IDs … traced only by the gate checklist: AUT-05 AUT-06 GATE-04 NET-04 NFR-08'), mechanisch FAILS_AS_EXPECTED, Kontrolle mit Testdatei PASS. Zusätzlich 8 [B]/[Q]-IDs ohne Testdatei (AUT-05, AUT-06, NET-04, NFR-08, NFR-09, PRI-11, SEC-11, TST-04).
- **Soll:** Nach WP-005 AK1 und GLO-11 verweist jede Zeile auf eine existierende Testdatei (mindestens eine Testart); Nicht-Test-Nachweise nur über explizite Allow-List der planvorgeschriebenen Dokumente; erklärungsbasierte Abnahmen zählen nicht als getestet.
- **Ist:** Markdown-Dateien, Verzeichnisse und README bestehen die Prüfung; die G1-Zahl 219/219 enthält 5 IDs ohne jeden Test, die Blockade lässt sich per Selbstverweis erfüllen.
- **Nutzerwirkung:** Traceability-Vollständigkeit (G1-Gate-Kriterium) überzeichnet die reale Testabdeckung; per Erklärung abgenommene Anforderungen erscheinen 'traced' wie getestete.
- **Ursache (belegt):** Validate hat weder Testdatei-Muster noch Allow-List; WP-213 ergänzte ungeplante Erklärungszeilen in fa76b16. Belegt durch Code, Daten und Probes.
- **Empfehlung:** testdatei auf Testmuster beschränken (*_test.go, *.test.ts(x), *.spec.ts, tests/**/*.sh|js|ts) plus explizite Allow-List der 21 planvorgeschriebenen Nachweiszeilen; erklärungsbasierte Zeilen markieren, separat berichten und aus der 'traced'-Zahl ausnehmen; Verweise auf docs/acceptance/g1-gate.md als Nachweis verbieten.
- **Retest (verbindlich):** cd backend && go test -tags auditdefect -count=1 -v -run 'TestAuditT5ValidateRejectsNonTestEvidence|TestAuditT5GateIDsAreNotTracedByTheChecklistItself' ./internal/traceability/ → PASS; TestTraceabilityFile meldet die erklärungsbasierten IDs separat und zählt für G1 nur Testzeilen.
- **Kodierender Test:** backend/internal/traceability/traceability_evidence_auditdefect_test.go – TestAuditT5ValidateRejectsNonTestEvidence, TestAuditT5GateIDsAreNotTracedByTheChecklistItself (Branch audit-defects-T5; fails-as-expected).

<a id="f-122"></a>
### F-122 (P2) – Kein CI-, Install-Smoke- oder Scan-Lauf auf dem aktuellen Release-Kandidaten dokumentiert; G1-Nachweise beziehen sich auf ältere Commits

- **Betroffene WPs:** WP-209, WP-211, WP-213 · **Komponente:** docs/acceptance/g1-installation.md:9-21 (RC 1b6671f, Nachlauf 37752e1); docs/security/review-g1.md:60, :100, :113 (Run 38510025931 vor WP-208, RC-Lauf 'steht aus'); docs/acceptance/g1-gate.md:30-31 ('Der CI-Lauf auf dem Release-Kandidaten steht aus'); .github/workflows/install-smoke.yml:7-41 (Pfadfilter ohne backend/**, frontend/src/**); kein Git-Tag · **Quelle:** Audit (B27: WP-211, WP-209, WP-213) + Verifikation + Defekttest-Welle T5
- **Reproduktion:** git log fa76b16..3a9669a: #63 und #64 mit 68 geänderten Backend-/Frontend-Dateien (+12086/-808), keine Installer-/Compose-/Migrationsänderung; g1-installation.md seit fa76b16 unverändert; git tag leer; install-smoke löst bei Code-Änderungen per Pfadfilter nicht aus. Kodiert: tests/audit/g1-gate-evidence.test.sh Fall install_evidence_current FAIL ('last product change: 3a9669a G1-Nachbesserung: WP-332 – WP-335 …; commits named in g1-installation.md: 1b6671fa… 37752e1'); in dieser Synthese erneut ausgeführt.
- **Soll:** Grüner install-smoke-Lauf (3 Plattformen + Onboarding), grüne Scan-Jobs (govulncheck ×3, npm-audit, image-scan) und grüne traceability-/gate-Jobs auf dem RC-Commit, mit Run-IDs in g1-installation.md, review-g1.md und g1-gate.md.
- **Ist:** Läufe von älteren Ständen; die RC-Images sind weder installations- noch scangeprüft; die Dokumente vermerken 'steht aus'.
- **Nutzerwirkung:** Ein Start-/Laufzeitfehler oder eine neue Schwachstelle aus der G1-Nachbesserung (#63/#64) bliebe bis zur Auslieferung unentdeckt; Risiko begrenzt, da Migrationen, Installer und Compose unverändert sind.
- **Ursache (belegt):** Pfadfilter von install-smoke berücksichtigt keine Code-Änderungen; nach #64 wurde weder workflow_dispatch noch ein Release-Tag (v*) ausgelöst.
- **Empfehlung:** RC taggen (v*) bzw. workflow_dispatch für install-smoke, ci (inkl. Scans, traceability, gate) auf dem RC-Commit auslösen; Run-IDs und Zähler in den drei Dokumenten eintragen und 'steht aus' entfernen.
- **Retest (verbindlich):** ./tests/audit/g1-gate-evidence.test.sh Fall install_evidence_current ok; einsehbare Run-IDs: install-smoke 4 Jobs grün inkl. Onboarding, govulncheck/npm-audit/image-scan grün, traceability und gate grün auf dem RC-Commit.
- **Kodierender Test:** tests/audit/g1-gate-evidence.test.sh – install_evidence_current (fails-as-expected).

<a id="f-123"></a>
### F-123 (P2) – Lint-Baseline (new-from-rev) verdeckt 472 Altbefunde; das versprochene Folge-WP wurde nie angelegt

- **Betroffene WPs:** WP-001 · **Komponente:** backend/.golangci.yml:59-64 (Baseline-Kommentar, new-from-rev 257e6fa); docs/plan/implementierungsplan.md, docs/plan/phase2/* · **Quelle:** Audit (B01) + Verifikation (Lint-Lauf ohne Baseline)
- **Reproduktion:** 1. backend/.golangci.yml ohne new-from-rev kopieren (SP/work/verify-B01/nobaseline.yml). 2. In backend/ golangci-lint v2.14.0 run --config <Kopie> ./... → '429 issues', exit 1, 164 Dateien (SP/work/verify-B01/lint-backend-nobaseline.txt). 3. edgecore → 6, collector → 37 Befunde. 4. gofmt -l backend collector edgecore → 12 Dateien. 5. grep in docs/plan/implementierungsplan.md und docs/plan/phase2/* nach Lint-Folge-WP → kein Treffer; PR-#40-Merge-Nachricht nennt keines.
- **Soll:** Nach WP-001 AK4 existiert im Plan ein Folge-WP, das die Altlast abbaut und danach new-from-rev entfernt.
- **Ist:** new-from-rev 257e6fa ist auf HEAD aktiv, kein Folge-WP in Phase 1 oder 2. Verdeckt u. a.: ungeprüftes json.Unmarshal in internal/agent/pg_repository.go:38 und internal/desk/pg_repository.go:42, ungeprüfter Fehler cmd/server/main.go:94, ungeprüftes JSON-Encode internal/credential/handler.go:85/105/126/170, 22 ineffassign, 12 nicht gofmt-formatierte Dateien.
- **Nutzerwirkung:** Qualitätsschuld unsichtbar; DoD-01/PR-Vorlage 'Lint ohne Befunde' gilt nur für neuen Code; stille JSON-Decode-Fehler (mögliche stille Datenverluste beim Lesen korrupter JSONB) werden nie gemeldet.
- **Ursache (belegt):** Der Plan wurde beim Setzen der Baseline nicht ergänzt (belegt durch Config-Kommentar :59-63, fehlenden Planeintrag und die Merge-Nachricht von PR #40).
- **Empfehlung:** Folge-WP im Plan anlegen (Abbau nach Linter-Kategorie: errcheck, ineffassign, gofmt/goimports, unused, dann govet, gocritic), danach new-from-rev entfernen; bis dahin nicht-blockierenden CI-Schritt, der die Altlast je Modul zählt und berichtet.
- **Retest (verbindlich):** golangci-lint v2.14.0 mit Config ohne new-from-rev meldet für backend, collector und edgecore je 0 issues – oder das Folge-WP steht im Plan und CI berichtet die Altlastzahl je Modul.
- **Kodierender Test:** keiner (Plan-/Doku-Mangel, in der Defekttest-Welle bewusst nicht kodifiziert)

<a id="f-124"></a>
### F-124 (P2) – Ausgeliefertes Image reticora-postgres wird nie auf Schwachstellen gescannt

- **Betroffene WPs:** WP-004 · **Komponente:** .github/workflows/ci.yml:455-470 (image-scan-Matrix backend/collector/frontend); .github/workflows/release.yml:117-129 (build-push inkl. reticora-postgres), :44-64 (nur syft-SBOM); deploy/docker-compose/postgres/Dockerfile:7 · **Quelle:** Audit (B01) + Verifikation + Defekttest-Welle T5 (in Synthese wiederholt)
- **Reproduktion:** Build-push-Matrix in release.yml:117-129 (reticora-server, reticora-collector, reticora-frontend, reticora-postgres aus deploy/docker-compose/postgres/Dockerfile) mit image-scan-Matrix ci.yml:455-470 vergleichen; grep -n -i 'trivy|grype' release.yml → kein Treffer. Kodiert: tests/audit/ops-config.test.sh Fall image_scan_coverage FAIL ('not scanned: reticora-postgres(deploy/docker-compose/postgres/Dockerfile)'); in dieser Synthese erneut ausgeführt (SP/work/synth-T5/ops-config.log).
- **Soll:** Nach WP-004 AK3 und SEC-11 wird jedes gebaute Image mit Trivy gescannt; Critical/High mit verfügbarem Fix blockieren.
- **Ist:** Das postgres-Image (pgBackRest aus Quellen, Alpine-Bibliotheken, ungepinnte Basis latest-pg16) wird ohne Schwachstellenscan ausgeliefert.
- **Nutzerwirkung:** CVEs im sensibelsten Container (Datenbank) können unentdeckt ausgeliefert werden; das SEC-11-CVE-Fix-SLA ist für dieses Image nicht einhaltbar.
- **Ursache (belegt):** WP-100 (Commit ea1b6fe) fügte Dockerfile und Release-Matrixeintrag hinzu, ohne die CI-image-scan-Matrix zu erweitern; kein Strukturtest Release-Matrix ⊆ Scan-Matrix.
- **Empfehlung:** {image: postgres, context: deploy/docker-compose/postgres, dockerfile: deploy/docker-compose/postgres/Dockerfile} in die image-scan-Matrix aufnehmen; Release-Images in release.yml vor dem Push scannen; Basisimage per Digest pinnen; Strukturtest der Matrixabdeckung.
- **Retest (verbindlich):** KUSTOMIZE=$(go env GOPATH)/bin/kustomize ./tests/audit/ops-config.test.sh Fall image_scan_coverage ok; CI-Lauf zeigt ein trivy-postgres-Artefakt.
- **Kodierender Test:** tests/audit/ops-config.test.sh – image_scan_coverage (fails-as-expected).

<a id="f-125"></a>
### F-125 (P2) – Installer-Wiederholung wendet rotiertes Keycloak-Admin-Passwort, OIDC-Client-Secret und andere Realm-Änderungen nicht auf einen bestehenden Keycloak an

- **Betroffene WPs:** WP-096 · **Komponente:** install-cloud.sh create_initial_admin :1510-1516, configure_realm; deploy/docker-compose/docker-compose.yml:217 ('start --import-realm'); docs/troubleshooting-installation.md:152 und Abschnitt 6 · **Quelle:** Verifikation (B12, Keycloak-24-Probe)
- **Reproduktion:** SP/work/verify-B12/kcrot/run.sh: Wegwerf-Compose (timescale als keycloak-db, keycloak/keycloak:24.0 'start --import-realm --hostname-strict=false', Import-Verzeichnis wie docker-compose.yml:238). Phase 1: KEYCLOAK_ADMIN_PASSWORD=old-admin-1, Realm 'rot' mit Client 'app' Secret SECRET_A → kcadm-Login alt rc=0. Phase 2 (wie Wiederholung mit rotierten Werten): Realm-Datei mit SECRET_B neu gerendert, up -d mit KEYCLOAK_ADMIN_PASSWORD=new-admin-2 (Container neu erstellt). Ergebnis (run.log): 'Realm rot already exists. Import skipped', kcadm-Login new-admin-2 rc=1, old-admin-1 rc=0, Client-Secret weiterhin SECRET_A.
- **Soll:** Nach dem Wiederholungslauf akzeptiert Keycloak das rotierte Admin-Passwort, das Client-Secret entspricht RETICORA_OIDC_CLIENT_SECRET in der .env, neu gerenderte Realm-Einstellungen (Redirect-URIs) greifen (wie troubleshooting-installation.md:152 verspricht).
- **Ist:** Übertragen auf install-cloud.sh: create_initial_admin bricht mit 'Could not log in to the Keycloak admin API as admin' ab (:1514-1516); mit rotiertem Client-Secret nutzt der Server SECRET_B gegen Keycloaks SECRET_A (Login defekt); die dokumentierte Abhilfe 'Invalid redirect uri: re-run the installer' wirkt nicht.
- **Nutzerwirkung:** Betreiber, die nach INS-05 rotieren (oder ein Secret aus der .env löschen, das laut Doku neu erzeugt wird), erhalten einen abgebrochenen Installer oder eine defekte Anmeldung.
- **Ursache (belegt):** KEYCLOAK_ADMIN/KEYCLOAK_ADMIN_PASSWORD initialisieren den Master-Admin nur beim ersten Start; --import-realm überspringt bestehende Realms; der Installer hat keinen kcadm-Schritt für Admin-Passwort, Client-Secret oder Redirect-URIs.
- **Empfehlung:** Beim Wiederholungslauf Rotationen per kcadm anwenden (Anmeldung mit vorherigem Admin-Passwort aus .env-Historie oder Abfrage, dann neues setzen; 'kcadm update clients/<id> -s secret=…', redirectUris/webOrigins aus dem gerenderten Realm) – oder Rotation als manuelle Schritte dokumentieren und die Re-Run-Versprechen für Realm-Einstellungen streichen.
- **Retest (verbindlich):** Docker-gestützter Keycloak-24-Integrationstest (z. B. tests/install-reinstall.test.sh): Erstinstallation mit A; KEYCLOAK_ADMIN_PASSWORD, RETICORA_OIDC_CLIENT_SECRET und RETICORA_OIDC_REDIRECT_URL in .env rotieren; Keycloak-Schritte des Installers ausführen → kcadm-Login mit neuem Passwort OK, Client-Secret = .env, redirectUris enthalten neue URL, client_credentials-Token mit neuem Secret 200, altes 401.
- **Kodierender Test:** keiner (benötigt echten Keycloak und Installer-Wiederholung; in der Defekttest-Welle nicht kodifiziert)

<a id="f-126"></a>
### F-126 (P2) – /readyz prüft SMTP nicht, obwohl SMTP konfiguriert ist

- **Betroffene WPs:** WP-097 · **Komponente:** backend/cmd/server/main.go (readiness = append nur redis :163, database :220, object_storage :237, nats :257, ingest-Detail :420; centralSMTP :871-885); api/openapi.yaml:110-137 (SMTP nicht beschrieben) · **Quelle:** Audit (B12) + Verifikation (Live-/readyz) + Defekttest-Welle T5 (mechanisch verifiziert)
- **Reproduktion:** Geteilter Stack mit SP/stack/server.env:21-24 (RETICORA_SMTP_HOST=localhost, PORT=1025): curl -s http://localhost:8080/readyz → checks {database, nats, object_storage, redis}. Kodiert: TestAuditT5ReadinessCoversSMTP (backend/cmd/server/startup_readiness_auditdefect_test.go, -tags auditdefect): mit lauschendem SMTP '/readyz checks.smtp = "" (present false), want ok'; SMTP-Port geschlossen: '/readyz = 200, want 503'; mechanisch FAILS_AS_EXPECTED.
- **Soll:** Mit gesetztem RETICORA_SMTP_HOST enthält /readyz checks.smtp und antwortet 503, wenn SMTP unerreichbar ist (OPS-01, WP-097 AK1); ohne SMTP-Konfiguration kein smtp-Schlüssel.
- **Ist:** SMTP wird nie geprüft; ein SMTP-Ausfall lässt die Instanz 'ready'.
- **Nutzerwirkung:** Signup-Verifikation und E-Mail-Benachrichtigungen scheitern ohne Readiness-/Monitoring-Signal.
- **Ursache (belegt):** In 75aa419 kein SMTP-ReadinessCheck implementiert; OpenAPI-Beschreibung lässt SMTP ebenfalls aus.
- **Empfehlung:** ReadinessCheck{Name: smtp} registrieren, wenn cfg.SMTPHost gesetzt ist (Dial plus EHLO/NOOP innerhalb des Timeouts, TLS-Modus beachten); openapi.yaml und Doku aktualisieren.
- **Retest (verbindlich):** cd backend && TEST_DATABASE_URL='postgres://reticora:reticora@localhost:55432/<DB>?sslmode=disable' TEST_REDIS_URL=redis://localhost:56379/0 go test -tags auditdefect -count=1 -v -run 'TestAuditT5ReadinessCoversSMTP' ./cmd/server/ → PASS (checks.smtp ok; geschlossener Port → 503 checks.smtp=unavailable).
- **Kodierender Test:** backend/cmd/server/startup_readiness_auditdefect_test.go – TestAuditT5ReadinessCoversSMTP (Branch audit-defects-T5; fails-as-expected).

<a id="f-127"></a>
### F-127 (P2) – object_storage-Readiness wählt das rohe RETICORA_S3_ENDPOINT; URL-förmige oder portlose Endpunkte, die der S3-Client akzeptiert, halten /readyz dauerhaft auf 503

- **Betroffene WPs:** WP-097 · **Komponente:** backend/cmd/server/main.go:237 (TCPReadiness('object_storage', cfg.S3Endpoint)); backend/internal/server/health.go:164-173; backend/internal/platform/blob/blob.go:168-186 (NewS3Store parst http(s)://host[:port]) · **Quelle:** Verifikation (B12, Binary-Probe gegen MinIO) + Defekttest-Welle T5 (mechanisch verifiziert)
- **Reproduktion:** SP/work/verify-B12/probe-s3-readyz.sh: HEAD-Binary (Port 18915, audit_db_15, Redis db 15, geteiltes MinIO mit Bucket) mit RETICORA_S3_ENDPOINT=localhost:9000 → /readyz 200; mit http://localhost:9000 → Server startet normal, /readyz 503 {object_storage: unavailable}, Log 'dial tcp: address http://localhost:9000: too many colons in address'; portlos 's3.eu-central-1.amazonaws.com' → 'missing port in address' (SP/work/verify-B12/s3dial). Kodiert: TestAuditT5ReadinessObjectStorageURLEndpoint (-tags auditdefect): 'RETICORA_S3_ENDPOINT=http://127.0.0.1:…: /readyz -> 503 …, want 200'; Kontrolle host:port 200; mechanisch FAILS_AS_EXPECTED.
- **Soll:** Jede von NewS3Store akzeptierte Endpunktform wird am selben host:port geprüft; /readyz 200, wenn S3 funktioniert, 503 bei Ausfall.
- **Ist:** URL-Form → dauerhaft 503 trotz funktionierendem S3; portlose Endpunkte scheitern ebenso.
- **Nutzerwirkung:** Mit Managed-S3 als URL oder AWS-Hostname bleibt der Compose-Server unhealthy (Frontend startet wegen depends_on service_healthy nie), K8s-Pods werden nie Ready.
- **Ursache (belegt):** main.go:237 übergibt cfg.S3Endpoint unverändert an TCPReadiness; anders als bei NATS (hostPort mit Default-Port) keine Normalisierung von Schema/Port.
- **Empfehlung:** host:port wie NewS3Store ableiten (Schema entfernen, Default-Port 443/80 nach Schema bzw. S3_USE_SSL) oder BucketExists des S3-Clients als Readiness-Check verwenden.
- **Retest (verbindlich):** cd backend && TEST_DATABASE_URL=… TEST_REDIS_URL=redis://localhost:56379/0 go test -tags auditdefect -count=1 -v -run 'TestAuditT5ReadinessObjectStorageURLEndpoint' ./cmd/server/ → PASS; Unit-Tabelle 'http://h:9000', 'https://h', 'h' plus USE_SSL → h:9000, h:443, h:443.
- **Kodierender Test:** backend/cmd/server/startup_readiness_auditdefect_test.go – TestAuditT5ReadinessObjectStorageURLEndpoint (Branch audit-defects-T5; fails-as-expected, Kontrolle host:port PASS).

<a id="f-128"></a>
### F-128 (P2) – K8s-Backup-Komponente provisioniert keine WAL-Archivierung

- **Betroffene WPs:** WP-100 · **Komponente:** deploy/k8s/base/components/backup/ (pgbackrest-configmap.yaml:1-9 nur Kommentar, pgbackrest-cronjob, cronjob.yaml); deploy/k8s/overlays/prod, overlays/staging; docs/backup-dr.md:81-84 · **Quelle:** Audit (B13) + Verifikation (kustomize-Render) + Defekttest-Welle T5
- **Reproduktion:** kustomize build deploy/k8s/overlays/prod > out.yaml; grep -c 'archive_mode\|archive-push' out.yaml → 0; gerendert nur CronJob reticora-pgbackrest-full und reticora-backup (pg_dump), beide '0 2 * * *', kein Datenbank-Manifest; staging rendert keine Backup-Ressourcen (SP/work/verify-B13/k8s-prod.yaml, k8s-staging.yaml). Kodiert: tests/audit/ops-config.test.sh Fall k8s_wal_archive FAIL ('0 matches; backup resources: StatefulSet/opensearch CronJob/reticora-backup CronJob/reticora-pgbackrest-full'); in dieser Synthese erneut ausgeführt.
- **Soll:** WAL-Archivierung und Basisbackups auch in K8s (WP-100 AK1).
- **Ist:** Nur tägliches Full-Backup über TLS zu einem externen Host 'reticora-db'; WAL-Archivierung und pgbackrest-TLS-Server auf diesem Host bleiben dem Betreiber überlassen.
- **Nutzerwirkung:** Eine K8s-Installation nach den Manifesten erhält nur tägliche Backups (RPO bis 24 h statt 15 min); der CronJob scheitert, wenn der DB-Host keinen pgbackrest-Server betreibt.
- **Ursache (belegt):** Die Komponente deckt nur die Repository-Host-Seite ab; die DB-Host-Seite ist eine dokumentierte Vorbedingung.
- **Empfehlung:** DB-Host-Konfiguration bereitstellen (StatefulSet/ConfigMap mit archive_command und pgbackrest-TLS-Server) oder Managed-DB mit PITR verbindlich vorschreiben und prüfen; kustomize-Render-Assertion in CI.
- **Retest (verbindlich):** KUSTOMIZE=$(go env GOPATH)/bin/kustomize ./tests/audit/ops-config.test.sh Fall k8s_wal_archive ok; kind-Smoke: pgbackrest check OK, pg_stat_archiver.archived_count steigt nach pg_switch_wal().
- **Kodierender Test:** tests/audit/ops-config.test.sh – k8s_wal_archive (fails-as-expected).

<a id="f-129"></a>
### F-129 (P2) – Versionierung/Replikation des Backup-Buckets nicht erzwungen; Terraform-Regel 'production requires replica' ist nur eine Warnung

- **Betroffene WPs:** WP-100 · **Komponente:** deploy/terraform/main.tf:34-56 (backup_policy-Output mit literal versioning=true, check 'backup_replication' :53), variables.tf:99-129; tests/backup-config.test.sh:74 (greppt nur den check-Block); pgBackRest-S3-Bucket ohne Versionierungs-Code · **Quelle:** Audit (B13) + Verifikation (terraform-Ausführung) + Defekttest-Welle T5 (in Synthese wiederholt)
- **Reproduktion:** terraform 1.9.8 in Scratch-Kopie von deploy/terraform: init -backend=false; plan -var environment=production → exit 0 mit 'Warning: Check block assertion failed' (SP/work/verify-B13/tf-plan-prod-noreplica.log); plan mit region=eu-central-1 und backup_replica_region=eu-central-1 → exit 0 mit Warnung (tf-plan-prod-same.log). Kodiert: tests/audit/ops-config.test.sh Fälle terraform_replica_missing und terraform_replica_same FAIL, Kontrolle terraform_replica_eu ok; in dieser Synthese erneut ausgeführt. Der Versionierungsteil des Backup-Buckets ist nicht kodifiziert.
- **Soll:** Versionierte und replizierte Buckets (NFR-05), für production erzwungen; Plan ohne Replik oder mit Replik in der Backup-Region scheitert.
- **Ist:** Keine Bucket-Ressourcen; backup_policy behauptet versioning=true; der pgBackRest-S3-Bucket erhält weder Versionierung noch Replikation; Produktionsplan ohne Replik besteht.
- **Nutzerwirkung:** Backups und Dokumente können unversioniert in nur einer Region liegen, ohne dass ein Gate es bemerkt (NFR-05).
- **Ursache (belegt):** Provider-neutrales Skelett und check-Block (Warnsemantik) statt variable validation oder lifecycle precondition.
- **Empfehlung:** variable validation bzw. precondition, die den Plan scheitern lässt; Bucket-Ressourcen mit Versionierung und Replikation für mindestens einen Provider; backup-config.test.sh auf terraform plan statt grep umstellen.
- **Retest (verbindlich):** TERRAFORM=<terraform 1.9.8> KUSTOMIZE=$(go env GOPATH)/bin/kustomize ./tests/audit/ops-config.test.sh Fälle terraform_replica_missing und terraform_replica_same ok (Plan exit != 0), terraform_replica_eu weiterhin ok; zusätzlich Nachweis der Backup-Bucket-Versionierung (GET ?versioning = Enabled).
- **Kodierender Test:** tests/audit/ops-config.test.sh – terraform_replica_missing, terraform_replica_same (fails-as-expected), Kontrolle terraform_replica_eu.

<a id="f-130"></a>
### F-130 (P2) – RPO-Grenzwertprüfung in restore-pitr.sh kann mit Default-Parametern nicht scheitern

- **Betroffene WPs:** WP-101 · **Komponente:** tests/restore-pitr.sh:39 (WRITE_SECONDS=420), :153-170 (rpo = last_commit - restored_last, FAIL nur bei rpo > 900), :186; docs/backup-dr.md:150-157 · **Quelle:** Audit (B13) + Verifikation (Code-Pfad und dokumentierter Lauf) + Defekttest-Welle T5
- **Reproduktion:** restored_last ist durch die per WAL-Replay bis zum Szenario-1-Ziel wiederhergestellten Phase-A-Zeilen nach unten begrenzt, daher rpo <= ~2 s + rto_pitr + Overhead + 420 s < 900 s, sobald der Restore auf kleiner DB gelingt. Empirisch: docs/backup-dr.md:150-157 meldet RPO 52 s bei 30 s Schreibfenster (52 > 30, also kein Schreibphasen-Datensatz wiederhergestellt) und dennoch PASS. Kodiert: tests/audit/ops-config.test.sh Fall restore_rpo_window FAIL ('WRITE_SECONDS=420s <= RPO_LIMIT=900s: rpo … stays below the limit whenever the restore succeeds'); in dieser Synthese erneut ausgeführt.
- **Soll:** Der Workflow scheitert, wenn das erreichte RPO 15 min überschreitet, z. B. bei archive_timeout > 900 s (WP-101 AK2).
- **Ist:** Mit archive_timeout=3600 ergäbe das 420-s-Schreibfenster weiterhin rpo < 900 und PASS; nur das separate pgbackrest check fängt ein völlig defektes archive_command.
- **Nutzerwirkung:** Falsche Gewissheit, dass RPO 15 min nachgewiesen ist; Regressionen der Archivierungskadenz bleiben unentdeckt.
- **Ursache (belegt):** Das Messfenster ist kürzer als der Grenzwert.
- **Empfehlung:** Länger als der Grenzwert schreiben (RESTORE_WRITE_SECONDS >= 1000) oder RPO <= archive_timeout + Marge (<= 360 s) assertieren; Negativ-Selbsttest mit archive_timeout=1200, der scheitern muss.
- **Retest (verbindlich):** ./tests/audit/ops-config.test.sh Fall restore_rpo_window ok; Override-Lauf mit archive_timeout=1200 endet mit exit 1 und Zeile 'RPO … FAIL'; Default-Lauf besteht mit RPO < 360 s.
- **Kodierender Test:** tests/audit/ops-config.test.sh – restore_rpo_window (fails-as-expected; statische Prüfung, kein Restore-Lauf).

<a id="f-131"></a>
### F-131 (P2) – Keine Signaturprüfung vor der Installation (TEC-15 'Signaturprüfung vor Installation')

- **Betroffene WPs:** WP-102 · **Komponente:** install.sh, install-cloud.sh (:1185 compose_cmd pull server frontend ohne cosign verify), install-vm.sh:487-498 (Quell-Tarball/git clone ungeprüft), :550-556 (Go-Toolchain ohne Prüfsumme), :575 (lokaler Build); docker-compose.yml:33, :281, :362 (lokale Images reticora-*:latest) · **Quelle:** Verifikation (B13) + Defekttest-Welle T5 (in Synthese wiederholt)
- **Reproduktion:** grep -rln cosign --include=*.sh --include=*.go --include=*.yml . → nur .github/workflows/release.yml, edgecore/update/*, collector/collectorcmd/update.go und Doku. Kodiert: tests/audit/ops-config.test.sh Fälle install_signature_install-vm ('no cosign verify/verify-blob in install-vm.sh; 498: if curl -fsSL "$tarball" …') und install_signature_install-cloud ('no cosign verify in install-cloud.sh; 1185: compose_cmd pull server frontend') FAIL; in dieser Synthese erneut ausgeführt.
- **Soll:** Signierte Artefakte werden vor der Installation verifiziert (TEC-15), nicht nur beim Auto-Update.
- **Ist:** Installationen nutzen bzw. verifizieren die signierten Release-Artefakte nie; sie bauen aus ungeprüft heruntergeladenen Quellen.
- **Nutzerwirkung:** Die signierte Lieferkette wird bei jeder Neuinstallation umgangen; manipulierte Quell- oder Toolchain-Downloads bleiben unentdeckt.
- **Ursache (belegt):** WP-102 hat nur die Updater-Seite von TEC-15 umgesetzt.
- **Empfehlung:** Installationsmodus mit signierten Release-Binaries/-Images und cosign verify(-blob) vor Nutzung (fail-closed) anbieten, mindestens Prüfsummen für Quellen und Toolchain verifizieren; dokumentieren.
- **Retest (verbindlich):** ./tests/audit/ops-config.test.sh Fälle install_signature_install-vm und install_signature_install-cloud ok; install-vm-Test mit manipuliertem Artefakt oder Bundle bricht vor der Installation ab, gültiges Bundle installiert.
- **Kodierender Test:** tests/audit/ops-config.test.sh – install_signature_install-vm, install_signature_install-cloud (fails-as-expected).

<a id="f-132"></a>
### F-132 (P2) – alertmanager.yaml wird von Alertmanagers Konfigurationsprüfung abgelehnt (kein globaler SMTP-Smarthost)

- **Betroffene WPs:** WP-103 · **Komponente:** deploy/monitoring/alertmanager.yaml (email_configs :19-26 ohne global smtp_smarthost/smtp_from und ohne Receiver-Smarthost); keine amtool/promtool-Prüfung in CI · **Quelle:** Audit (B13) + Verifikation (amtool) + Defekttest-Welle T5 (in Synthese wiederholt)
- **Reproduktion:** amtool v0.27.0: amtool check-config deploy/monitoring/alertmanager.yaml → 'FAILED: no global SMTP smarthost set', exit 1 (SP/work/verify-B13/amtool-check.log). Kodiert: tests/audit/ops-config.test.sh Fälle alertmanager_smtp ('receiver operators: email_configs without smarthost and no global smtp_smarthost') und alertmanager_amtool FAIL; in dieser Synthese mit AMTOOL=SP/work/verify-B13/bin/amtool erneut ausgeführt.
- **Soll:** Funktionierende, in deploy/monitoring konfigurierte Betreiber-Alarmkette (WP-103 AK2).
- **Ist:** Alertmanager verweigert das Laden der Konfiguration; das Ersetzen der Empfängeradressen laut Dateikommentar reicht nicht.
- **Nutzerwirkung:** Eine Installation mit der ausgelieferten Kette stellt keine Alarme zu: Alertmanager scheitert beim Start oder Reload.
- **Ursache (belegt):** Vorlage ohne globale SMTP-Einstellungen und ohne Validierungsschritt.
- **Empfehlung:** Globales smtp_smarthost/smtp_from (templatisiert) ergänzen; amtool check-config sowie promtool check rules und promtool test rules (aus PrometheusRule-CRDs extrahiertes .spec) in CI aufnehmen.
- **Retest (verbindlich):** AMTOOL=<amtool v0.27.0> ./tests/audit/ops-config.test.sh Fälle alertmanager_smtp und alertmanager_amtool ok; amtool check-config exit 0; promtool-Regeltests (z. B. SP/work/verify-B13/prom/worker-rules.test.yaml) in CI grün.
- **Kodierender Test:** tests/audit/ops-config.test.sh – alertmanager_smtp, alertmanager_amtool (fails-as-expected).

<a id="f-133"></a>
### F-133 (P2) – Vier registrierte Worker exportieren keine Lauf-/Fehler-/Last-Success-Metriken; Worker-Backlog nur für 3 von 11

- **Betroffene WPs:** WP-103 · **Komponente:** backend/internal/discovery/natsedge.go, discovery/scope_handler.go, credential/bundle.go (kein ObserveWorkerRun, kein observability-Import); backend/cmd/server/main.go:582-593 (nur slog.Error), Backlog nur :525, :545, :604; jobs: nur reticora_job_failures_total/reticora_job_backlog (jobs/service.go:175, :316; router.go:337, :599) · **Quelle:** Audit (B13) + Verifikation (Laufzeitprobe)
- **Reproduktion:** Laufzeit: Overlay-Probe in backend/internal/jobs führt TestQueueRetriesAndRecoversLostWorkers aus (Handler liefern 'upstream down'/'broken', Versuche erschöpft) → reticora_worker_runs_total und errors_total{worker=jobs} delta 0 (SP/work/verify-B13/jobs-metrics-probe.log). Code: kein ObserveWorkerRun in natsedge.go, scope_handler.go, credential/bundle.go.
- **Soll:** Jeder Worker exportiert Lauf-, Fehler- und Rückstaumetriken (WP-103 AK1; docs/ops/workers.md:80 'Every worker exports these series').
- **Ist:** Fehler von nats_edge, credential_bundles und discovery_scopes werden nur geloggt (einziges Signal: worker_up fällt, wenn Run zurückkehrt); jobs-Fehler/-Backlog nur als reticora_job_*, die keine Regel und kein Dashboard nutzt; ReticoraWorkerErrors und NoSuccess können für diese 4 Worker nie feuern.
- **Nutzerwirkung:** Betreiber werden bei scheiternder Collector-Credential-/Scope-Auslieferung, beim Edge-NATS-Endpunkt oder bei asynchronen Jobs nicht alarmiert (OPS-05).
- **Ursache (belegt):** Nach WP-103 hinzugefügte Worker (WP-130/146/147/153) übernahmen den Vertrag nicht; kein registry-weiter Test erzwingt ihn.
- **Empfehlung:** Alle 4 Worker mit ObserveWorkerRun und Backlog instrumentieren (für jobs: ausstehende JOBS) oder Alarmregeln auf reticora_job_* ergänzen; registry-weiter Test über config.WorkerNames.
- **Retest (verbindlich):** Tabellengetriebener Test über config.WorkerNames: nach einem realen Zyklus mit Fakes enthält /metrics reticora_worker_runs_total{worker=<name>}, ein erzwungener Fehler erhöht errors_total – grün für alle 11 Worker.
- **Kodierender Test:** keiner (nur Overlay-Probe SP/work/verify-B13/zz_vb13_jobs_metrics_probe_test.go; in der Defekttest-Welle nicht kodifiziert)

<a id="f-134"></a>
### F-134 (P2) – Betreiberalarmierung nicht an NTF-Kanäle angebunden, obwohl WP-137 geliefert ist

- **Betroffene WPs:** WP-103 · **Komponente:** deploy/monitoring/alertmanager.yaml:1-5, docs/ops/workers.md:101 ('wird mit WP-137 ergänzt'); backend (keine NTF-Bridge; grep alertmanager findet nur monitoring.AlertManager) · **Quelle:** Audit (B13) + Verifikation
- **Reproduktion:** alertmanager.yaml:1-5 und docs/ops/workers.md:101 kündigen den NTF-Kanal 'mit WP-137' an; WP-137 ist 6da3c58 (2026-10-13). grep -ri alertmanager backend/internal → nur monitoring.AlertManager (unabhängiger In-Memory-Regelspeicher); kein Receiver, keine Bridge, kein Endpunkt.
- **Soll:** OPS-05 'Alarmierung des Betriebs über NTF-Kanäle'; WP-103-Kriterium 'NTF-Anbindung nach WP-137'.
- **Ist:** Keine NTF-Anbindung.
- **Nutzerwirkung:** Betreiber können Ops-Alarme nicht über die Benachrichtigungskanäle des Produkts empfangen.
- **Ursache (belegt):** Die Nacharbeit nach WP-137 wurde nie durchgeführt.
- **Empfehlung:** Alertmanager-Webhook-Receiver auf einen NTF-Ingest-Endpunkt bzw. Systemkanal implementieren oder formale Zurückstellungsentscheidung dokumentieren.
- **Retest (verbindlich):** Integrationstest: Alertmanager-Webhook-POST (firing, dann resolved) an die Bridge erzeugt eine notification_delivery auf dem Betreiberkanal und wird zugestellt; Dedup bei Wiederholung; fehlendes/ungültiges Token 401; Systemalarme für Kunden-Orgs unsichtbar.
- **Kodierender Test:** keiner (in der Defekttest-Welle nicht kodifiziert)

<a id="f-135"></a>
### F-135 (P2) – SMTP-Ausfallszenario wird als N/A gemeldet, obwohl SMTP-Zustellung existiert

- **Betroffene WPs:** WP-104 · **Komponente:** tests/outage/run.sh:112-118 (smtp) report N/A 'no SMTP delivery exists yet (TEC-13)'); backend/internal/notify/smtp.go (seit 6da3c58) · **Quelle:** Audit (B13) + Verifikation + Defekttest-Welle T5 (in Synthese wiederholt)
- **Reproduktion:** tests/outage/run.sh:117 meldet N/A; backend/internal/notify/smtp.go kam mit 6da3c58 (WP-137, 2026-10-13) nach bec175d (WP-104, 2026-10-12). Kodiert: tests/audit/ops-config.test.sh Fall outage_smtp FAIL ('117: smtp) report N/A …; SMTP delivery exists: backend/internal/notify/smtp.go'); in dieser Synthese erneut ausgeführt.
- **Soll:** Die Suite simuliert einen SMTP-Ausfall mit Retry und ohne stillen Verlust (WP-104 AK1, OPS-06).
- **Ist:** Szenario übersprungen und als nicht anwendbar gezählt; das Release-Gating-Protokoll besteht trotzdem.
- **Nutzerwirkung:** Verlust von Benachrichtigungen während eines Mailserver-Ausfalls ist vom Release-Gate nicht abgedeckt.
- **Ursache (belegt):** Hart kodiertes N/A, nach WP-137 nicht aktualisiert.
- **Empfehlung:** Mailpit in den Teststack aufnehmen; SMTP stoppen, Benachrichtigung auslösen, Zustellung pending mit attempts >= 1 und last_error assertieren; nach Neustart sent mit genau einer Mail.
- **Retest (verbindlich):** ./tests/audit/ops-config.test.sh Fall outage_smtp ok; ./tests/outage/run.sh smtp meldet PASS mit Zustellstatus-Evidenz.
- **Kodierender Test:** tests/audit/ops-config.test.sh – outage_smtp (fails-as-expected).

<a id="f-136"></a>
### F-136 (P2) – 'Kein stiller Datenverlust' wird nur über eine vor dem Ausfall geschriebene psql-Markerzeile assertiert

- **Betroffene WPs:** WP-104 · **Komponente:** tests/outage/run.sh:46-67 (outage_marker vor compose stop, danach nur diese Zeile geprüft) · **Quelle:** Audit (B13) + Verifikation
- **Reproduktion:** run.sh:46-67 fügt outage_marker per psql vor 'compose stop' ein und prüft danach nur diese Zeile; während des Ausfallfensters laufen kein API-, Ingest-, Event- oder Webhook-Verkehr.
- **Soll:** Während eines Ausfalls übermittelte Daten werden sichtbar abgelehnt oder dauerhaft gepuffert und nach dem Wiederanlauf verarbeitet (accepted == persisted).
- **Ist:** Eine committete Zeile, die einen Dienststopp übersteht, ist trivial; Backpressure wird nur aus /readyz 503 abgeleitet.
- **Nutzerwirkung:** Stiller Verlust in Ingest-, Outbox- oder Benachrichtigungspfaden während eines NATS-/PG-Ausfalls bliebe unentdeckt.
- **Ursache (belegt):** Keine Last während des Ausfalls.
- **Empfehlung:** Während jedes Ausfalls N API-Schreibvorgänge und Ingest-Batches senden, 503/429 oder Annahme assertieren; nach Wiederanlauf accepted == persisted, Outbox und Queues geleert, RestartCount unverändert.
- **Retest (verbindlich):** Erweitertes Szenario in tests/outage/ zeichnet die Zählwerte in outage-report.md auf und besteht für nats, postgres und redis (accepted == persisted, event_outbox leer, Ingest-Queue geleert).
- **Kodierender Test:** keiner (in der Defekttest-Welle nicht kodifiziert)

<a id="f-137"></a>
### F-137 (P2) – Subprozessorenliste leer; externe Lieferung (E-35/A-11) fehlt

- **Betroffene WPs:** WP-105 · **Komponente:** docs/legal/subprozessoren.md (Status offen, Platzhalterzeile); docs/plan/entscheidungen.md:959-975 (E-35 offen) · **Quelle:** Audit (B13) + Verifikation + Defekttest-Welle T5
- **Reproduktion:** docs/legal/subprozessoren.md: eine Platzhalterzeile '| – | *vom Betreiber zu liefern (A-11)* | – | – | – | – |'; E-35 Status offen, Nachtrag vom 2026-10-15: Liste nicht im Repository. Kodiert: tests/audit/g1-gate-evidence.test.sh Fall subprocessors FAIL; in dieser Synthese erneut ausgeführt.
- **Soll:** docs/legal/subprozessoren.md mit vom Betreiber gelieferter Liste (WP-105 AK2; NFR-09 'Subprozessoren dokumentiert und AVV-fähig').
- **Ist:** Keine Subprozessoren dokumentiert.
- **Nutzerwirkung:** Kunden können Art.-28-DSGVO-Subprozessoren nicht bewerten – Compliance-Lücke beim SaaS-Onboarding.
- **Ursache (belegt):** Externe Betreiberlieferung ausstehend (E-35/A-11).
- **Empfehlung:** Liste beschaffen und committen (Region und AVV-Status je Dienst des Referenzmodells: Hosting, Objektspeicher, E-Mail, Backup); NFR-09 bis dahin als bedingt führen (L-T5-10).
- **Retest (verbindlich):** ./tests/audit/g1-gate-evidence.test.sh Fall subprocessors ok (Nicht-Platzhalterzeilen mit Region und AVV für alle Dienste).
- **Kodierender Test:** tests/audit/g1-gate-evidence.test.sh – subprocessors (fails-as-expected).

<a id="f-138"></a>
### F-138 (P2) – ABN-01 (a)/(b): v2-Originaltexte fehlen und sind nicht eingefroren (E-02/A-02 offen)

- **Betroffene WPs:** WP-212 · **Komponente:** docs/acceptance/abn-01.md:11-12, :16-33; docs/acceptance/artefakte/ (SHA256SUMS; Namen laut artefakte/README.md abn-01-a-cmdb.md, abn-01-b-system.md) · **Quelle:** Verifikation (B27) + Defekttest-Welle T5
- **Reproduktion:** abn-01.md:11-12 und :16-33: '(a)/(b) PASS (Erklärung des Auftraggebers) … Originaltext nachzureichen (E-02, A-02)'; weder abn-01-a-cmdb.md noch abn-01-b-system.md existieren. Kodiert: tests/audit/g1-gate-evidence.test.sh Fall abn01_texts FAIL (beide Dateien '<missing>'); in dieser Synthese erneut ausgeführt.
- **Soll:** ABN-01: (a) 34 und (b) 20 Schritte unverändert aus V §65–66 vor G1 eingefroren, jeder Schritt einem Test oder einer Prüfung zugeordnet.
- **Ist:** Keine Texte, keine Zuordnung; Status PASS beruht auf Erklärung.
- **Nutzerwirkung:** Zwei von vier G1-relevanten Abnahmeszenarien sind inhaltlich nicht nachprüfbar.
- **Ursache (belegt):** Externe Lieferung (E-02, A-02) fehlt.
- **Empfehlung:** Texte nachreichen, einfrieren (SHA256SUMS), Schritte zuordnen und protokollieren; bis dahin (a)/(b) als 'nicht nachgewiesen' statt PASS führen (L-T5-10).
- **Retest (verbindlich):** ./tests/audit/g1-gate-evidence.test.sh Fall abn01_texts ok (beide Dateien vorhanden und in SHA256SUMS); jeder Schritt mit Ergebnis protokolliert.
- **Kodierender Test:** tests/audit/g1-gate-evidence.test.sh – abn01_texts (fails-as-expected).

<a id="f-139"></a>
### F-139 (P2) – Erneuerte Zertifikate werden nie geladen: kein nginx-Reload nach 'certbot renew'

- **Betroffene WPs:** WP-094 · **Komponente:** deploy/docker-compose/docker-compose.yml:415-426 (certbot-Schleife, :423 'certbot renew … --quiet' alle 12 h ohne --deploy-hook); frontend/docker-entrypoint.d, frontend/Dockerfile (kein Reload); install-cloud.sh:950 (Reload nur während der Installation) · **Quelle:** Verifikation (B12, unbestätigt)
- **Reproduktion:** Statisch: docker-compose.yml:423 ohne --deploy-hook; grep -rn reload frontend/docker-entrypoint.d frontend/Dockerfile deploy/docker-compose/docker-compose.yml → nichts; install-cloud.sh lädt nur bei der Installation neu (reload_frontend_after_bootstrap). Nicht ausgeführt (kein nginx-Image offline verfügbar).
- **Soll:** Ein erneuertes Zertifikat wird innerhalb des Erneuerungsfensters ohne manuellen Neustart ausgeliefert.
- **Ist:** nginx hält das beim Start/Reload geladene Zertifikat im Speicher; nach der Erneuerung (~Tag 60) wird bis zum Ablauf (Tag 90) das alte Zertifikat geliefert, sofern das Frontend nicht neu gestartet wird.
- **Nutzerwirkung:** Ausfall durch abgelaufenes Zertifikat etwa 90 Tage nach Ausstellung auf lange laufenden Hosts.
- **Ursache (Hypothese):** Hypothese (nicht ausgeführt): Kein Deploy-Hook und kein periodischer Reload verbinden die Erneuerung im certbot-Container mit dem nginx des Frontends.
- **Empfehlung:** Periodischer 'nginx -s reload' im Frontend-Entrypoint (übliches nginx-certbot-Muster) oder inotify-Watcher; alternativ Deploy-Hook mit Signalisierung ohne Docker-Socket.
- **Retest (verbindlich):** Pebble mit kurzlebigem Zertifikat: Erneuerung erzwingen und ohne Frontend-Neustart assertieren, dass sich die Seriennummer des ausgelieferten Zertifikats innerhalb des Reload-Intervalls ändert.
- **Kodierender Test:** keiner

<a id="f-140"></a>
### F-140 (P2) – PATCH height_units akzeptiert Höhe unterhalb bestehender Mounts

- **Betroffene WPs:** WP-108 · **Komponente:** backend/internal/rack/pg_repository.go:143 UpdateRack · **Quelle:** Verifikation (B14) + Defekttest-Welle
- **Reproduktion:** 42U-Rack, Mount U40 h2 front; PATCH /api/v1/racks/{id} {height_units:39} bzw. 10 → 200, gespeichert, Layout listet Mount außerhalb.
- **Soll:** 409 field height_units, da RCK-01 position_u + height_u − 1 ≤ height_units verlangt.
- **Ist:** 200, height_units geändert, Mount unverändert außerhalb des Racks.
- **Nutzerwirkung:** Rackansicht und Frei-HE-Berechnung inkonsistent; Geräte außerhalb des physischen Racks dokumentiert.
- **Ursache (belegt):** UpdateRack vergleicht nicht mit max(position_u+height_u−1) von rack_mount; kein Constraint/Trigger.
- **Empfehlung:** Mounts sperren (FOR UPDATE) und Höhe unter höchster belegter HE mit 409 ablehnen oder Constraint-Trigger.
- **Retest (verbindlich):** WT: cd backend && TEST_DATABASE_URL=…/audit_db_11 go test -tags auditdefect -count=1 -run 'TestAuditDefectRackHeight' ./internal/rack/ → PASS.
- **Kodierender Test:** backend/internal/rack/height_auditdefect_test.go TestAuditDefectRackHeightNotBelowMounts (fails-as-expected :88)

<a id="f-141"></a>
### F-141 (P2) – asset/quantity_item ohne denormalisierte site_id: Site-Scope greift nicht für platzierte Assets

- **Betroffene WPs:** WP-109 · **Komponente:** Schema asset/quantity_item, RLS-Policies · **Quelle:** Verifikation (B14) + Defekttest-Welle
- **Reproduktion:** information_schema: asset(location_id, client_id) ohne site_id; Defekttest: Client-1-Asset an Site 1b für Site-1-Principal sichtbar.
- **Soll:** TEN-02: Tabellen mit Location-Bezug tragen site_id, RLS filtert app.site_scope.
- **Ist:** Keine site_id; Asset-Policy prüft nur client_scope.
- **Nutzerwirkung:** Site-Principals sehen Assets aller Sites ihres Clients (auch nach Fix L-…-01).
- **Ursache (belegt):** Schema nicht wie ci um abgeleitete site_id plus Site-Policy erweitert.
- **Empfehlung:** site_id per Trigger aus location_id ableiten, Site-Scope-Klausel in Asset-Policies.
- **Retest (verbindlich):** Wie L-T6-epic-b-01 (gleicher Defekttest, Fall 'client-1 asset at site 1b' invisible) → PASS.
- **Kodierender Test:** backend/internal/locations/sitescope_auditdefect_test.go TestAuditDefectSiteGrantConfinedToClientAndSite (fails-as-expected)

<a id="f-142"></a>
### F-142 (P2) – Keine UI zum Ausstellen, Erneuern oder Annehmen von Einladungen

- **Betroffene WPs:** WP-114 · **Komponente:** frontend/src/pages/UserManagementPage.tsx, App.tsx (keine Accept-Route) · **Quelle:** Verifikation (B14) + Defekttest-Welle
- **Reproduktion:** grep -rni 'invitation|Einladung' frontend/src → nichts; UserManagementPage nur lesend.
- **Soll:** Admins verwalten Einladungen in der Nutzerverwaltung; Eingeladene nehmen auf einer Seite an.
- **Ist:** Nur per Roh-REST möglich.
- **Nutzerwirkung:** Selbst registrierter org_admin kann im Produkt keine Kollegen hinzufügen.
- **Ursache (belegt):** WP-Dateiumfang nur Backend; kein UI-WP für Einladungen im Plan.
- **Empfehlung:** Einladungsbereich in UserManagementPage und öffentliche Accept-Seite.
- **Retest (verbindlich):** WT: cd frontend && npx vitest run -c vitest.auditdefect.config.ts src/pages/UserManagementPage.auditdefect.tsx → PASS; e2e-real UI-Einladung inkl. Mail-Link.
- **Kodierender Test:** frontend/src/pages/UserManagementPage.auditdefect.tsx (fails-as-expected)

<a id="f-143"></a>
### F-143 (P2) – Discovery überspringt Datentypvalidierung typisierter CI-Spalten: ungültige Werte persistiert oder ganze Batches verworfen

- **Betroffene WPs:** WP-117 · **Komponente:** backend/internal/ci/validation.go:369-383; discovery/devicerecord.go:83-120, discovery.go:967-1060 · **Quelle:** Verifikation (B15) + Defekttest-Welle
- **Reproduktion:** POST /ingest/bulk und /discovery/ingest mit hostname 'line1\nline2' → 202 und gespeichert (manuell 422); Items [gültig, hardware_uuid 'not-a-uuid', gültig] → 400, Folge-Records verloren; Records-Vertrag → 422 für ganzen Batch.
- **Soll:** Gleiche Wertregeln wie manuell; ungültiger Wert wird verworfen und in invalid_values gezählt, Rest gespeichert.
- **Ist:** invalidObserved prüft nur Definitionen; Typed-Werte erreichen INSERT.
- **Nutzerwirkung:** Discovery speichert Daten, die manuelle Bearbeitung ablehnt; ein fehlerhaftes Feld kostet andere Geräte des Batches.
- **Ursache (belegt):** typedColumns/validateTypedColumns (:261,:318) nur in Service.Create/Update genutzt.
- **Empfehlung:** Typed-Column-Datentypen als implizite Definitionen in InvalidObserved/ObservedRules; PG-Tests beider Verträge.
- **Retest (verbindlich):** WT: cd backend && TEST_DATABASE_URL=…/audit_db_11 go test -tags auditdefect -count=1 -run 'TestAuditDefectDiscoveryTypedValues' ./internal/discovery/ → PASS; zusätzlich Queue-Poison-Test auf dediziertem NATS.
- **Kodierender Test:** backend/internal/discovery/typedvalues_auditdefect_test.go TestAuditDefectDiscoveryTypedValuesValidated (fails-as-expected :112,:125,:164)

<a id="f-144"></a>
### F-144 (P2) – POST /cis akzeptiert privaten CI-Typ einer fremden Organisation

- **Betroffene WPs:** WP-120 · **Komponente:** ci_ci_type_id_fkey (einspaltig), ci Create-Pfad · **Quelle:** Verifikation (B15) + Defekttest-Welle
- **Reproduktion:** Org A besitzt Typ; Org B POST /api/v1/cis {ci_type_id: A-Typ} → 201, Zeile referenziert A-Typ; nicht existente UUID → 422 (SP/work/verify-B15/probe-ci-foreign-type.log).
- **Soll:** Nur System- oder eigener Org-Typ; sonst 422 field ci_type_id, keine Zeile.
- **Ist:** 201; A-Regeln greifen nicht; Existenzorakel 201 vs 422.
- **Nutzerwirkung:** Mandantenübergreifende Integritätsverletzung, Typregeln umgehbar; keine Fremddaten offengelegt.
- **Ursache (belegt):** Einspaltiger FK (FKs umgehen RLS) ohne Sichtbarkeitsprüfung.
- **Empfehlung:** Sichtbarkeitsprüfung in Create/Update (422) und Trigger ci_type.organization_id IS NULL OR = ci.organization_id.
- **Retest (verbindlich):** WT: cd backend && TEST_DATABASE_URL=…/audit_db_11 go test -tags auditdefect -count=1 -run 'TestAuditDefectForeignPrivateCIType' ./internal/server/ → PASS.
- **Kodierender Test:** backend/internal/server/audit_t6_router_auditdefect_test.go TestAuditDefectForeignPrivateCITypeRejected (fails-as-expected :351)

<a id="f-145"></a>
### F-145 (P2) – POST /relationships mit CI einer anderen Organisation oder unbekannter CI-ID antwortet 500 statt 404/422

- **Betroffene WPs:** WP-010, WP-025, WP-128, WP-160, WP-163, WP-208 · **Komponente:** backend/internal/relationship/pg_repository.go:191-222, relationship.go:521-608; api/dberrors.go:83-87 · **Quelle:** Real-Stack-E2E (T14, confirmed); zusammengeführt mit L-T4-47 (Verifikation WP-160; Real-Stack-E2E T14)
- **Reproduktion:** Tenant B: POST /api/v1/relationships {source_ci_id:b1, target_ci_id:a1, rel_type:depends_on} → 500; ebenso a1→b1 und zufällige UUID (Traces 61650391…, 4604ecd5… in SP/stack/server.log).
- **Soll:** Client-Fehler 404/422 mit violations auf source_ci_id/target_ci_id (wie POST /cis).
- **Ist:** 500 'the request could not be completed'; nichts gespeichert (keine Isolationsverletzung).
- **Nutzerwirkung:** Beziehungsdialog zeigt generische englische Fehlermeldung statt 'Ziel-CI existiert nicht'; Monitoring zählt 5xx.
- **Ursache (belegt):** INSERT…WHERE EXISTS trifft keine Zeile (RLS), ErrNoRows wird zu fmt.Errorf('not found') (:222), WriteRepoError mappt Unbekanntes auf 500.
- **Empfehlung:** Existenzprüfung vor Insert oder Sentinel-Fehler → 422 mit Feldbezug.
- **Retest (verbindlich):** cd frontend && npx playwright test -c e2e-real/playwright.real.config.ts tenant-isolation -g 'T14' → pass.
- **Kodierender Test:** frontend/e2e-real/tenant-isolation.spec.ts T14 (FAIL); keiner (Real-Stack T14 FAIL)
- **Prüfvermerk:** Gruppenübergreifend dedupliziert: L-T4-47 („POST /relationships über Mandantengrenzen antwortet 500 statt 404/422“) ist dieselbe Ursache und hier zusammengeführt.

<a id="f-146"></a>
### F-146 (P2) – Discovery-Jobs werden nicht im generischen Jobmodell registriert (Adapter nicht verdrahtet)

- **Betroffene WPs:** WP-129 · **Komponente:** backend/internal/server/router.go:360-387; discovery/jobs.go:196-209 · **Quelle:** Verifikation (B16) + Defekttest-Welle
- **Reproduktion:** POST /api/v1/discovery/jobs → 201; SELECT count(*) FROM job WHERE ref_id=… → 0; GET /api/v1/jobs → total 0.
- **Soll:** Job kind discovery.sweep mit ref_table discovery_job, gespiegelt und in /jobs sowie Job Center sichtbar.
- **Ist:** Keine generische Jobzeile.
- **Nutzerwirkung:** Discovery-Jobs im Job Center/API nicht verfolgbar.
- **Ursache (belegt):** discoveryHandler ohne .WithJobTracker(jobTracker); kein Mirror im Discovery-Code.
- **Empfehlung:** WithJobTracker im Router aufrufen und Statusübergänge spiegeln.
- **Retest (verbindlich):** WT: cd backend && TEST_DATABASE_URL=…/audit_db_11 go test -tags auditdefect -count=1 -run 'TestAuditDefectDiscoveryJob' ./internal/server/ → PASS.
- **Kodierender Test:** backend/internal/server/audit_t6_router_auditdefect_test.go TestAuditDefectDiscoveryJobInGenericJobModel (fails-as-expected :273)

<a id="f-147"></a>
### F-147 (P2) – Kein Produktionsjob nutzt die NATS-Work-Queue; Bulk/Snapshot laufen als Goroutinen und bleiben nach Crash 'running'

- **Betroffene WPs:** WP-130 · **Komponente:** cmd/server/main.go:268-300,599-600; backend/internal/ci/bulk.go:113-125; jobs/service.go:216 ReapTimeouts · **Quelle:** Verifikation (B16)
- **Reproduktion:** grep: jobs.Queue nur main.go:270, kein Handle/Submit/ReapTimeouts-Aufrufer; Router ohne NATS: POST /cis/bulk (100 IDs) → 202 und succeeded in-process (SP/work/verify-B16/probe/run-main.log).
- **Soll:** TEC-14/JOB-01: Jobs über Work-Queue, Redelivery nach Timeout.
- **Ist:** Keine registrierten Kinds; nichts reapt abgelaufene running-Jobs.
- **Nutzerwirkung:** Nach Crash/Rolling-Deploy hängen Bulk-Änderungen und Snapshots dauerhaft in 'running'.
- **Ursache (belegt):** Kinds nicht per jobQueue.Handle registriert, Enqueue+Goroutine statt Queue.Submit; kein ReapTimeouts im Scheduler.
- **Empfehlung:** Kinds registrieren und über Queue.Submit einreichen; ohne NATS ReapTimeouts einplanen; Wiring-Test.
- **Retest (verbindlich):** Integration PG+NATS: Bulk-Job einreichen, Replika killen → zweite Replika beendet (attempts=2); Crash-Test: geclaimter Job nach Timeout requeued/failed.
- **Kodierender Test:** keiner (statische Evidenz, Queue-Aufbau inline in main())

<a id="f-148"></a>
### F-148 (P2) – Route-Audit speichert nur Feldnamen, keine Vorher-/Nachher-Werte

- **Betroffene WPs:** WP-133 · **Komponente:** backend/internal/middleware/middleware.go:116-127 · **Quelle:** Verifikation (B16) + Defekttest-Welle
- **Reproduktion:** PATCH /api/v1/vlans/{id} {name:'…-new'} → changes={route, fields:[name], status:200}.
- **Soll:** AUD-07/WP-Problem: alte und neue Werte je Änderung.
- **Ist:** Nur Feldnamen für alle Ressourcen ohne Domain-Recorder (vrfs, vlans, relationships, relationship-types, cables, contacts, webhooks …).
- **Nutzerwirkung:** Auditoren können Änderungen dieser Ressourcen nicht rekonstruieren.
- **Ursache (belegt):** changes aus writtenFields(r); kein Before-Image.
- **Empfehlung:** Domain-Recorder oder generisches Before-Image bei PATCH/PUT/DELETE (PII als Surrogat); AK-Bezug AUD-05/07 klären.
- **Retest (verbindlich):** WT: cd backend && TEST_DATABASE_URL=…/audit_db_11 go test -tags auditdefect -count=1 -run 'TestAuditDefectRouteAuditStores' ./internal/server/ → PASS.
- **Kodierender Test:** backend/internal/server/audit_t6_router_auditdefect_test.go TestAuditDefectRouteAuditStoresOldAndNewValues (fails-as-expected :228)

<a id="f-149"></a>
### F-149 (P2) – Route-Audit nach der Response in separater Transaktion; Fehler nur geloggt (nicht lückenlos)

- **Betroffene WPs:** WP-133 · **Komponente:** backend/internal/middleware/middleware.go:88-142; server/router.go:629-633 · **Quelle:** Verifikation (B16) + Defekttest-Welle
- **Reproduktion:** Router mit fehlschlagendem RouteAuditor: POST /api/v1/vrfs → 201, VRF-Zeile 1, Audit-Zeilen 0, nur Log 'audit entry of a write request failed'.
- **Soll:** Lückenlose Erfassung: Eintrag atomar mit Mutation oder per Outbox mit Retry.
- **Ist:** Best effort; DB-/Pool-Fehler oder Crash verliert den Eintrag still.
- **Nutzerwirkung:** Unentdeckbare Lücken im Audit-Trail (Kette bleibt intakt).
- **Ursache (belegt):** Post-Response-Middleware statt Repository-Transaktion.
- **Empfehlung:** Tx-gebundener Recorder oder Outbox mit Retry; mindestens Alarm-Metrik.
- **Retest (verbindlich):** WT: cd backend && TEST_DATABASE_URL=…/audit_db_11 go test -tags auditdefect -count=1 -run 'TestAuditDefectRouteAuditFailure' ./internal/server/ → PASS.
- **Kodierender Test:** backend/internal/server/audit_t6_router_auditdefect_test.go TestAuditDefectRouteAuditFailureLeavesNoUnauditedMutation (fails-as-expected :198-200)

<a id="f-150"></a>
### F-150 (P2) – NFR-06-Log-Retention (30 Tage) in keiner Auslieferung konfiguriert

- **Betroffene WPs:** WP-135 · **Komponente:** deploy/docker-compose/docker-compose.yml, deploy/k8s, install.sh · **Quelle:** Verifikation (B17) + Defekttest-Welle
- **Reproduktion:** grep -rniE 'max-file|max-size|logging:|logrotate|loki|promtail|720h' deploy/ install.sh → kein Treffer.
- **Soll:** Logs nach 30 Tagen gelöscht.
- **Ist:** Compose-Logs unbegrenzt (json-file); k8s nur größenbasierte Rotation.
- **Nutzerwirkung:** Logs mit möglichen personenbezogenen Daten zu lange gehalten; Platte kann volllaufen.
- **Ursache (belegt):** Log-Retention an Plattform delegiert ohne mitgelieferte Konfiguration (docs/ops/workers.md:56-58).
- **Empfehlung:** logging max-size/max-file je Compose-Service, dokumentierte journald/daemon.json-Policy, 30-Tage-Retention im Log-Backend.
- **Retest (verbindlich):** WT: ./tests/audit/log-retention.test.sh → alle Checks PASS.
- **Kodierender Test:** tests/audit/log-retention.test.sh (FAILS: 2 passed, 1 failed)

<a id="f-151"></a>
### F-151 (P2) – Webhook-Consumer verwirft Events bei transienten Fehlern, Outbox markiert sie als veröffentlicht

- **Betroffene WPs:** WP-136 · **Komponente:** backend/internal/webhook/dispatcher.go:256-268,281-284,332-337 · **Quelle:** Verifikation (B17) + Defekttest-Welle
- **Reproduktion:** Dispatcher mit fehlschlagendem ListByEvent bzw. Enqueue; PublishPending → published=true, attempts=1, last_error leer, 0 Deliveries.
- **Soll:** EVT-01 at-least-once: fehlgeschlagener Hand-off lässt Zeile pending, Retry.
- **Ist:** Event verloren, nur Logzeile.
- **Nutzerwirkung:** Webhook-Integrationen verlieren Events bei kurzen DB-Ausfällen.
- **Ursache (belegt):** Consume ignoriert die von dispatchAs nur geloggten Fehler.
- **Empfehlung:** Fehler propagieren; Enqueue idempotent per (Event-ID, Subscription).
- **Retest (verbindlich):** WT: cd backend && TEST_DATABASE_URL=…/audit_db_11 go test -tags auditdefect -count=1 -run 'TestAuditDefectWebhookConsumer' ./internal/webhook/ → PASS.
- **Kodierender Test:** backend/internal/webhook/consume_auditdefect_test.go TestAuditDefectWebhookConsumerKeepsEventsOnTransientErrors (fails-as-expected :165,:178)

<a id="f-152"></a>
### F-152 (P2) – Gespeichertes SMTP-Passwort wird nach PATCH mit neuem Host an diesen gesendet

- **Betroffene WPs:** WP-137 · **Komponente:** backend/internal/notify/handler.go UpdateChannel · **Quelle:** Verifikation (B17) + Defekttest-Welle
- **Reproduktion:** Kanal mit host mail.corp, password X; PATCH {host:127.0.0.1, password:'********'} → 200; Send → AUTH PLAIN mit X am neuen Host.
- **Soll:** Ziel-/TLS-/User-Änderung verlangt erneute Secret-Eingabe.
- **Ist:** Secret bleibt und geht an neues Ziel.
- **Nutzerwirkung:** webhook:manage-Inhaber ohne Secret-Kenntnis kann SMTP-Credentials abgreifen.
- **Ursache (belegt):** Platzhalter stellt Secret unabhängig von Zieländerungen wieder her (TestNotificationAPI:44-50 kodiert das sogar).
- **Empfehlung:** Platzhalter bei Änderung von host/port/tls/username/url ablehnen (400).
- **Retest (verbindlich):** WT: cd backend && go test -tags auditdefect -count=1 -run 'TestAuditDefectSMTPSecret' ./internal/notify/ → PASS.
- **Kodierender Test:** backend/internal/notify/channel_secret_auditdefect_test.go TestAuditDefectSMTPSecretNotReleasedToNewHost (fails-as-expected :114)

<a id="f-153"></a>
### F-153 (P2) – Eigener SMTP-Kanal umgeht die Egress-Policy (interne Ziele, Cloud-Metadaten)

- **Betroffene WPs:** WP-137 · **Komponente:** backend/internal/notify/smtp.go:187-200, channel.go:63-96; cmd/server/main.go:360 · **Quelle:** Verifikation (B17) + Defekttest-Welle
- **Reproduktion:** Channel smtp host 169.254.169.254 / fd00:ec2::254 / 10.0.0.5 / 127.0.0.1 port 22: Validate() = nil, Provider wählt plain net.Dialer.
- **Soll:** SEC-08: mandantengelieferte Ziele gegen private/link-local/Metadaten prüfen (mind. SaaS), Ports beschränken.
- **Ist:** Beliebiger Host/Port wird angewählt; last_error über /notification-deliveries lesbar.
- **Nutzerwirkung:** Org-Admins im SaaS können interne Dienste sondieren.
- **Ursache (belegt):** SMTP-Dial nutzt internal/egress nicht.
- **Empfehlung:** egress.Options auf SMTP anwenden (AllowPrivate nur konfiguriert), Ports 25/465/587.
- **Retest (verbindlich):** WT: cd backend && go test -tags auditdefect -count=1 -run 'TestAuditDefectSMTPChannelCloudMetadata' ./internal/notify/ → PASS; zusätzlicher Test für private/Loopback-Ziele im SaaS-Modus.
- **Kodierender Test:** backend/internal/notify/channel_secret_auditdefect_test.go TestAuditDefectSMTPChannelCloudMetadataBlocked (fails-as-expected :136; nur Metadaten-Ziel kodiert)

<a id="f-154"></a>
### F-154 (P2) – Benachrichtigungs-Administration unter webhook:read statt notification:manage; Webhook-URL-Token unredigiert für Viewer/Engineer lesbar

- **Betroffene WPs:** WP-137, WP-139 · **Komponente:** backend/internal/server/authz.go:80-81,139-140; notify/handler.go:171-185; permission/role_matrix.go:42,54 · **Quelle:** Verifikation (B17) + Defekttest-Welle
- **Reproduktion:** Viewer GET /api/v1/notification-channels und /notification-deliveries → 200; Liste enthält config.url mit Token.
- **Soll:** RBA-02: notification:manage (nur org_admin); Token-URLs als Secret redigiert.
- **Ist:** Viewer/Engineer lesen Kanal-Configs, Token, Empfänger-E-Mails, Betreffs und Inhalte.
- **Nutzerwirkung:** PII-Offenlegung, Posten in Chat-Kanäle; latent Einladungslink-Abgriff nach Fix von L-…-03.
- **Ursache (belegt):** Routen-Mapping nutzt Webhook-Rechte; PermNotificationManage ungenutzt; Redaktionsliste nur password/secret.
- **Empfehlung:** Routen auf notification:manage mappen; url bei provider webhook redigieren; Bodies/Links in Zustellliste ausblenden.
- **Retest (verbindlich):** WT: cd backend && TEST_DATABASE_URL=…/audit_db_11 go test -tags auditdefect -count=1 -run 'TestAuditDefectNotificationAdministration' ./internal/server/ → PASS.
- **Kodierender Test:** backend/internal/server/audit_t6_router_auditdefect_test.go TestAuditDefectNotificationAdministrationNeedsNotificationManage (fails-as-expected :299,:311)

<a id="f-155"></a>
### F-155 (P2) – Client-Kontakte sind keine Empfängerquelle (NTF-03)

- **Betroffene WPs:** WP-138 · **Komponente:** backend/internal/notify/recipients.go:18-47 · **Quelle:** Verifikation (B17)
- **Reproduktion:** Audience kennt Roles, TeamIDs, UserIDs, AllWithPermission, CIID+ContactRoles, Direct; kein Client-Kontakt; contact.client_id existiert.
- **Soll:** Audience adressiert Kontakte des Clients des Objekts.
- **Ist:** Nur CI-verknüpfte Kontakte auflösbar.
- **Nutzerwirkung:** Kundenkontakte erhalten keine Hinweise zu ihren Systemen.
- **Ursache (belegt):** Nicht implementiert.
- **Empfehlung:** ClientContacts (optional Rollen) in Audience mit Vault-Auflösung.
- **Retest (verbindlich):** Erweiterter TestRecipientsAgainstPostgres: C1-Kontakt erhält, C2-Kontakt nicht.
- **Kodierender Test:** keiner (Audience-Feld fehlt, Test nicht kompilierbar)

<a id="f-156"></a>
### F-156 (P2) – NTF-04-Digest nicht implementiert

- **Betroffene WPs:** WP-139 · **Komponente:** backend/internal/notify/notifier.go:189-221 · **Quelle:** Verifikation (B17)
- **Reproduktion:** grep -rni digest backend/internal/notify → kein Treffer.
- **Soll:** Nicht kritische Kategorien (z. B. Reviews) als Digest bündelbar.
- **Ist:** Je Event einzelne Zustellungen.
- **Nutzerwirkung:** Mailfluten in review-lastigen Orgs.
- **Ursache (belegt):** Nicht implementiert.
- **Empfehlung:** Digest-Modus je Kategorie/Präferenz mit Zeitfenster.
- **Retest (verbindlich):** Notifier-Test: 5 review.created in 1 h → 1 Digest-Mail mit 5 Einträgen.
- **Kodierender Test:** keiner

<a id="f-157"></a>
### F-157 (P2) – Neue Organisationen erhalten keine Benachrichtigungen (kein Default-Kanal, keine Kanal-UI)

- **Betroffene WPs:** WP-139 · **Komponente:** backend/internal/notify, Org-Anlage (signup/operator), Frontend · **Quelle:** Verifikation (B17)
- **Reproduktion:** Scopetest-Org, EventConsumer.Consume(export.completed) → delivery 0, inbox 0, channel 0; N01 muss Kanal per REST anlegen.
- **Soll:** In-App-Benachrichtigungen (UI-16) funktionieren ab Werk oder sind in der UI aktivierbar.
- **Ist:** Alle NTF-05-Events still verworfen, bis jemand POST /notification-channels sendet.
- **Nutzerwirkung:** In Standardinstallationen sehen Nutzer keine Review-/Export-/Job-/Webhook-Hinweise.
- **Ursache (belegt):** Keine Default-Provisionierung, keine Admin-UI.
- **Empfehlung:** In-App-(und central_smtp-)Kanal bei Org-Anlage plus Backfill-Migration; Kanal-Admin-Seite.
- **Retest (verbindlich):** Integration: neue Org via Signup + export.completed → ein Inbox-Eintrag ohne manuelle Einrichtung.
- **Kodierender Test:** keiner

<a id="f-158"></a>
### F-158 (P2) – Master-Key-Rotation ändert abgeleitete Secrets (NATS-Edge-Issuer/-Passwort, Cursor-Key); nats-edge lädt nicht neu

- **Betroffene WPs:** WP-141 · **Komponente:** backend/internal/platform/crypto/crypto.go:109-113; discovery/natsedge.go:176-190,302-316; deploy nats-edge · **Quelle:** Verifikation (B17)
- **Reproduktion:** NewEnvelopeEncryptor(old) vs (new, old): Derive('reticora/pagination-cursor/v1') und DeriveEdgeSecrets unterschiedlich; alter Cursor 'signature does not verify'; nats-edge startet einmal (docker-compose.yml:137-140, nats-edge.yaml:50-55).
- **Soll:** SEC-06: Master-Key-Rotation ohne Downtime (Runbook docs/ops/workers.md:68).
- **Ist:** Auth-Callout kann sich nach Server-Neustart nicht an nats-edge anmelden, bis dieser neu startet; Cursor → 400.
- **Nutzerwirkung:** Discovery und Heartbeats der Collectors fallen nach Rotation aus; Runbook schweigt.
- **Ursache (belegt):** Derive nutzt nur aktuellen Master-Key; keine stabile Quelle, kein Reload.
- **Empfehlung:** Aus dediziertem stabilen Secret ableiten oder vorherige Keys zur Verifikation zulassen; sonst nats-edge-Reload signalisieren und dokumentieren.
- **Retest (verbindlich):** Unit: DeriveEdgeSecrets/Cursor-Key gleich über Rotation; e2e-real: Rotation + nur Server-Neustart → Collector-Heartbeat läuft.
- **Kodierender Test:** keiner (e2e-real erforderlich)

<a id="f-159"></a>
### F-159 (P2) – CI-Typ-Admin ignoriert fehlgeschlagenes searchable/unique-Update still

- **Betroffene WPs:** WP-142 · **Komponente:** frontend/src/pages/CITypeAdminPage.tsx:283-293; main.tsx:9-16 · **Quelle:** Verifikation (B17) + Defekttest-Welle
- **Reproduktion:** Feld inventory_no mit 'Eindeutig' speichern; PUT /ci-types/t1/fields 200, PATCH /attribute-definitions/ad-1 409 'CIs already share a value of inventory_no' → Dialog offen, keine Meldung.
- **Soll:** 409-Grund sichtbar; Nutzer weiß, dass Eindeutigkeit nicht aktiv ist.
- **Ist:** Feld ohne Flag gespeichert, keine Fehlermeldung.
- **Nutzerwirkung:** Admins glauben an Eindeutigkeit, Duplikate bleiben möglich.
- **Ursache (belegt):** Nur onSuccess übergeben, upsertField.error nie gerendert; kein globaler MutationCache-onError.
- **Empfehlung:** Problem-Detail im Dialog rendern, Teilerfolg unterscheiden, Feldliste invalidieren.
- **Retest (verbindlich):** WT: cd frontend && npx vitest run -c vitest.auditdefect.config.ts src/pages/CITypeAdminPage.auditdefect.tsx → PASS.
- **Kodierender Test:** frontend/src/pages/CITypeAdminPage.auditdefect.tsx (fails-as-expected)

<a id="f-160"></a>
### F-160 (P2) – Redundanz-Postfilter meldet ein redundant versorgtes CI als betroffen, wenn die Tiefe die alternative Versorgung abschneidet

- **Betroffene WPs:** WP-179, WP-180 · **Komponente:** backend/internal/topology (redundancy.go, impact.go) · **Quelle:** Audit + Verifikation (B23, Probe f1)
- **Reproduktion:** Org mit server powered_by pdu-a, server powered_by pdu-b, vm hosted_on server. GET /api/v1/impact/{pdu-a}?depth=1, /api/v1/impact?ci_ids={pdu-a}&depth=1 oder /api/v1/topology/cis/{pdu-a}/impact?depth=1 → HTTP 200 impacted=[server]; ohne depth oder mit depth=2 → [] (Verifier-Probe f1 TestAuditProbeRedundancyDepthBoundary, audit_db_21, ov_topo.json).
- **Soll:** IMP-04: der Server ist bei keiner Tiefe betroffen, solange pdu-b ihn versorgt; ein Versorger außerhalb der Analyse hält den Abhängigen oben.
- **Ist:** Bei depth=1 wird der Server als sicher betroffen gemeldet.
- **Nutzerwirkung:** API-Nutzer und Integrationen mit begrenzter Tiefe (Wartungsfenster, Schnellprüfungen) sehen redundante Systeme als ausfallend – falsche Ausfallentscheidungen möglich. Die UI nutzt Default-Tiefe 20 und ist nur bei Ketten > 19 Hops betroffen.
- **Ursache (belegt):** writeImpact baut den Graphen mit buildFromRoot(depth) (impact.go:591); TraverseFrom lädt nur Kanten der ersten depth Hops; redundantProviders (redundancy.go:40-55) sammelt Versorger nur aus diesen Kanten, die Kante server–pdu-b fehlt, allDown([pdu-a]) ist wahr.
- **Empfehlung:** Für jeden erreichten Abhängigen redundanten Typs dessen redundante Kanten unabhängig von depth mit einer Zusatzabfrage laden oder unbekannte Versorger als 'up' werten; PG-Regressionstest.
- **Retest (verbindlich):** Committeter PG-Test in backend/internal/topology (Seed wie Reproduktion plus ups→pdu-a): /impact/{pdu-a}?depth=1, /impact?ci_ids={pdu-a}&depth=1, /topology/cis/{pdu-a}/impact?depth=1 und /impact/{ups}?depth=2 enthalten weder server noch vm; /impact?ci_ids=pdu-a,pdu-b&depth=1 enthält server und vm; go test -count=1 -run '<Testname>' ./internal/topology/ → PASS.
- **Kodierender Test:** keiner (nicht kodiert; nur Verifier-Probe f1)

<a id="f-161"></a>
### F-161 (P2) – GET /topology mit unbekannter oder fremder root_ci_id antwortet 500 statt 404

- **Betroffene WPs:** WP-010, WP-031, WP-180, WP-195, WP-208 · **Komponente:** backend/internal/topology/topology.go · **Quelle:** Verifikation (B23 Probe f1, B25) + Real-Stack-E2E; zusammengeführt mit L-T1-17 (Real-Stack-E2E (owned) + Verifikation (B02/B04/B27) + Defekttest-Welle)
- **Reproduktion:** Als OrgA GET /api/v1/topology?root_ci_id=00000000-0000-4000-8000-0000000000ff oder ?root_ci_id=<CI von OrgB> → 500 'the request could not be completed' (Probe f1 audit_db_21; TestProbeB25TopologyForeignRoot audit_db_23, SP/work/verify-B25/rerun-auditor-probes.log). Real-Stack frontend/e2e-real/tenant-isolation.spec.ts:1148 T15 FAIL ('GET /topology?root_ci_id={A} as B → 500'). In der UI: /topology?root=<gelöschtes/fremdes CI> → generischer Ladefehler.
- **Soll:** 404 wie /topology/cis/{id}/neighbors und /impact; die UI meldet, dass das CI nicht existiert.
- **Ist:** 500; keine Datenpreisgabe.
- **Nutzerwirkung:** Veraltete oder Deep-Links zeigen 'interner Fehler'; falsche 5xx in Fehlermetriken und SLO-Alerting.
- **Ursache (belegt):** buildFromRootViaTraversal gibt den rohen ciRepo.GetByID-Fehler zurück (topology.go:273-276, analog :308-311); GetTopology (:136-141) mappt nur FieldError, WriteRepoError fällt auf 500 zurück. Zusammengeführt aus WP-180 (P3) und WP-195 (P2).
- **Empfehlung:** ci-not-found in GetTopology bzw. buildFromRoot auf 404 mappen wie GetNeighbors (:153-157) oder Sentinel-Fehler, den WriteRepoError als 404 behandelt.
- **Retest (verbindlich):** API-Integrationstest: unbekannte UUID, CI fremder Org, CI fremden Clients als root_ci_id → 404 ohne Spur im Body, eigenes CI → 200, 'not-a-uuid' → 400; Real-Stack: npx playwright test -c e2e-real/playwright.real.config.ts tenant -g 'T15' → PASS.
- **Kodierender Test:** Real-Stack frontend/e2e-real/tenant-isolation.spec.ts:1148 T15 – FAIL (Test ist WP-031/WP-163/WP-208 anderer Gruppen zugeordnet); sonst keiner; frontend/e2e-real/tenant-isolation.spec.ts T15 (FAIL, SP/work/tenant-iso-final.log); TestAuditT1ScopeRefusalsAreNot5xx (FAILS_AS_EXPECTED); Probes TestB04VerifierProbe P2, TestVerifyB27NegativeStatus.
- **Prüfvermerk:** Gruppenübergreifend dedupliziert: L-T1-17 („GET /api/v1/topology?root_ci_id=<CI außerhalb des Scopes, fremde Org oder unbekannt> antwortet 500 statt 404“) ist dieselbe Ursache und hier zusammengeführt.

<a id="f-162"></a>
### F-162 (P2) – Job-Cancel stoppt laufende Arbeit nicht (ci.bulk, Export): Status 'cancelled', aber alle Änderungen werden ausgeführt bzw. der Export fertiggestellt, Teilbericht geht verloren

- **Betroffene WPs:** WP-181, WP-182, WP-197 · **Komponente:** backend/internal/jobs (service.go, handler.go), backend/internal/ci/bulk.go, backend/internal/export/worker.go; frontend JobCenterPage.tsx · **Quelle:** Audit + Verifikation (B23 Probe f2, B25) + Defekttest-Welle
- **Reproduktion:** (1) POST /api/v1/cis/bulk mit 500 IDs → 202; während des Laufs HTTP DELETE /api/v1/jobs/{id} → 200 'cancelled' (2–7 von 500 CIs geändert); danach alle 500 geändert, result_len=0, GET /jobs/{id}/result → 409 'the job has no result (status cancelled)', Log 'cancelled → succeeded' abgewiesen (Probe f2 audit_db_21, TestAuditT7BulkJobCancelStopsWork). (2) Export: POST /exports → generischer Job kind=export; DELETE /jobs/{id} (Button 'Abbrechen' im Jobcenter) → cancelled; worker.ProcessOne → export_job completed, Datei gespeichert (TestProbeB25ExportCancelViaJobCenter, audit_db_23; Produktionsverdrahtung router.go:359).
- **Soll:** JOB-03/UI-16: ein abgebrochener Job stoppt am nächsten Checkpoint und behält den Teilbericht, oder der Abbruch nicht unterbrechbarer Arten wird mit 409 abgelehnt bzw. in der UI nicht angeboten.
- **Ist:** Nur Statuswechsel auf cancelled; die Arbeit läuft vollständig weiter, der Bericht je ID geht verloren; generischer Job und export_job divergieren (cancelled vs. completed).
- **Nutzerwirkung:** Nutzer und Operatoren glauben, nichts weiter sei geschehen, während alle CIs geändert bzw. der Export fertiggestellt und herunterladbar wurde; die Änderungen bleiben unberichtet; Jobcenter und Exportseite widersprechen sich.
- **Ursache (belegt):** jobs.Service.Cancel (service.go:200-209) führt nur die Statustransition aus; runBulkJob/runBulk (ci/bulk.go:124-169), RunDetached (service.go:328-353) und export/worker.go:134-176 prüfen weder Jobstatus noch einen Cancel-Kontext; das spätere Succeed/Mirror scheitert still. Zusammengeführt aus WP-181, WP-182 und WP-197 (gleiche Ursache).
- **Empfehlung:** Cancel-Prüfung (z. B. Service.IsCancelled oder per Polling abgebrochener Kontext) in allen Runnern zwischen Items bzw. vor CompleteJob; Teilergebnis mit Status cancelled speichern; sonst 409 für laufende nicht unterbrechbare Arten und Cancel-Button nur für unterstützte Arten.
- **Retest (verbindlich):** cd backend && TEST_DATABASE_URL=… go test -tags auditdefect -count=1 -v -run '^TestAuditT7BulkJobCancelStopsWork$' ./internal/ci/ → PASS (< 500 CIs geändert, Job cancelled mit Teilbericht); zusätzlich PG-Integrationstest Export: DELETE /jobs/{id} vor ProcessOne → export_job nicht completed, kein Blob, kein export.completed; e2e-real Jobcenter-Cancel für Bulk und Export.
- **Kodierender Test:** backend/internal/ci/audit_t7_bulk_jobs_auditdefect_test.go:256 TestAuditT7BulkJobCancelStopsWork – FAILS_AS_EXPECTED (SP/verify-tests-t357/results.tsv: 'DELETE /api/v1/jobs/{id} -> 200 with 4 of 500 CIs changed … the job still changed all 500 CIs'); Export-Teil nicht kodiert (nur Probe)

<a id="f-163"></a>
### F-163 (P2) – Job-API ignoriert den Client-Scope: ein client-beschränkter Principal listet und liest Jobs und Ergebnisse anderer Clients

- **Betroffene WPs:** WP-182 · **Komponente:** backend/internal/jobs/handler.go; RLS-Policies job_isolation/job_system_select · **Quelle:** Verifikation (B23 Probe f8) + Defekttest-Welle; gleiche Ursache (jobs/handler.go:69-143 filtert nur nach Organisation, RLS job_isolation nur organization_id) wie T1-Befund WP-035 'Export jobs mirrored into the generic job model are listed and readable by other users and clients via GET /api/v1/jobs and /jobs/{id}' (P3, Teilaspekt) – im Gesamtbericht zusammenführen mit Priorität P2
- **Reproduktion:** Client-1-Nutzer startet einen Bulk-Job mit 100 IDs. Client-2-Principal (job:read, ci:read, ci:write) sieht in GET /api/v1/cis 0 dieser CIs (GET eines Client-1-CI → 404), aber GET /api/v1/jobs?kind=ci.bulk listet den Job, GET /jobs/{id} → 200, GET /jobs/{id}/result → 200 mit allen 100 Client-1-CI-IDs; DELETE → 409 statt 404 (Probe f8 TestVerifyProbeJobClientScope, TestAuditT7JobAPIHonoursClientScope).
- **Soll:** TEN-09 (Kanal 'job'): Jobs anderer Clients werden nicht gelistet; GET, result, DELETE und revert → 404 wie für die CIs selbst.
- **Ist:** Organisationsweite Sichtbarkeit von Parametern und Ergebnissen; Cancel für jeden Inhaber der Kind-Permission (mayControl). Auch die BFF-Query jobs (resolver.go:351-369 über jobs.Service.List, nur orgID) liefert einem Client-2-Principal die Client-1-Jobs (Metadaten id/kind/status/progress/error; Verifier-Probe f5 auf audit_db_21).
- **Nutzerwirkung:** In MSP-Setups erfährt ein client-beschränkter Nutzer CI-IDs und Ergebnisse anderer Kunden und kann deren Bulk-Jobs abbrechen (Isolationsverletzung innerhalb der Org).
- **Ursache (belegt):** List/Get/Result (handler.go:69-143) filtern nur nach Organisation; job_isolation/job_system_select prüfen nur organization_id (pg_policy, audit_db_21); mayControl (handler.go:147-157) ignoriert den Scope; TestChannelMatrix prüft für GET /api/v1/jobs nur HTTP 200.
- **Empfehlung:** Scope des Erzeugers am Job speichern und danach filtern (oder Ersteller plus org-weite Principals), RLS entsprechend; TestChannelMatrix auf den Inhalt des Job-Kanals erweitern.
- **Retest (verbindlich):** cd backend && TEST_DATABASE_URL=… go test -tags auditdefect -count=1 -v -run '^TestAuditT7JobAPIHonoursClientScope$' ./internal/ci/ → PASS (alle Subtests: GET /jobs ohne den Job, GET /jobs/{id}, /result, DELETE, /revert → 404 für Client 2; org-weiter Admin sieht ihn); zusätzlich POST /bff/graphql '{ jobs { id } }' als Client-2-Session enthält den Client-1-Job nicht (Probe f5 als PG-Test committen).
- **Kodierender Test:** backend/internal/ci/audit_t7_bulk_jobs_auditdefect_test.go:316 TestAuditT7JobAPIHonoursClientScope (Subtests :369) – FAILS_AS_EXPECTED ('a client-2 principal lists the client-1 bulk job …', 'GET /jobs/{id} by a client-2 principal = 200, want 404')
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – ist: „Organisationsweite Sichtbarkeit von Parametern und Ergebnissen; Cancel für jeden Inhaber der Kind-Permission (mayControl).“ → „Organisationsweite Sichtbarkeit von Parametern und Ergebnissen; Cancel für jeden Inhaber der Kind-Permission (mayControl). Auch die BFF-Query jobs (resolver.go:351-369 über jobs.Service.List, nur orgID) liefert einem Client-2-Principal die Client-1-Jobs (Metadaten id/kind/status/progress/error; Verifier-Probe f5 auf audit_db_21).“. Bestätigte Beobachtung aus der Eingabe (WP-185, Probe f5: 'jobs still lists the org's queued job (organization-wide …)'), die in keinem Defektdatensatz steht. Ursache ist dieselbe wie bei L-07: Der BFF-Resolver ruft denselben org-gefilterten jobs.Service.List auf (geprüft in backend/internal/graphqlbff/resolver.go). WP-185 wird nicht in wps aufgenommen, weil GQL-04 kein Kriterium von WP-185 ist und dessen Status deshalb unberührt bleibt. Der Fix von L-07 muss den BFF-Pfad aber abdecken.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – retest: „cd backend && TEST_DATABASE_URL=… go test -tags auditdefect -count=1 -v -run '^TestAuditT7JobAPIHonoursClientScope$' ./internal/ci/ → PASS (alle Subtests: GET /jobs ohne den Job, GET /jobs/{id}, /result, DELETE, /revert → 404 für Client 2; org-weiter Admin sieht ihn).“ → „cd backend && TEST_DATABASE_URL=… go test -tags auditdefect -count=1 -v -run '^TestAuditT7JobAPIHonoursClientScope$' ./internal/ci/ → PASS (alle Subtests: GET /jobs ohne den Job, GET /jobs/{id}, /result, DELETE, /revert → 404 für Client 2; org-weiter Admin sieht ihn); zusätzlich POST /bff/graphql '{ jobs { id } }' als Client-2-Session enthält den Client-1-Job nicht (Probe f5 als PG-Test committen).“. Ohne diese Ergänzung bestünde der Retest auch dann, wenn nur der REST-Pfad repariert wird und die BFF-Query jobs weiter org-weit liefert.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – quelle: „Verifikation (B23 Probe f8) + Defekttest-Welle“ → „Verifikation (B23 Probe f8) + Defekttest-Welle; gleiche Ursache (jobs/handler.go:69-143 filtert nur nach Organisation, RLS job_isolation nur organization_id) wie T1-Befund WP-035 'Export jobs mirrored into the generic job model are listed and readable by other users and clients via GET /api/v1/jobs and /jobs/{id}' (P3, Teilaspekt) – im Gesamtbericht zusammenführen mit Priorität P2“. Gruppenübergreifend bestätigter Teilaspekt mit identischer Ursache, ohne Querverweis. T1 hat nur den Exportfall bei Ergebnis 409 gesehen und P3 vergeben. L-07 belegt zusätzlich Ergebnisse mit 100 fremden CI-IDs, deshalb ist P2 für den zusammengeführten Defekt richtig.

<a id="f-164"></a>
### F-164 (P2) – State-Snapshot-Job (und GET /cis/{id}/state) liefert immer einen leeren Zustand

- **Betroffene WPs:** WP-183 · **Komponente:** backend/internal/history (pg_repository.go, handler.go) · **Quelle:** Audit + Verifikation (B23 Probe f7)
- **Reproduktion:** PG: CI über ci.Service.Update (Manual) umbenennen → ci_change 1 Zeile, entity_change 0. POST /api/v1/history/snapshots {entity_type:'ci', entity_id} → Job succeeded mit result fields={} – für jetzt, für at=T-2h, für einen Client-2-Principal und für eine unbekannte ID (Probe f7 TestAuditProbeSnapshotContent, audit_db_21; psql SELECT count(*) FROM entity_change → 0).
- **Soll:** Der Snapshot enthält den rekonstruierten Zustand (z. B. name='snap-renamed'), ein früherer Zeitpunkt den alten Wert; unbekannte/unsichtbare Entität → 404 oder fehlgeschlagener Job (Commit 84bc267: 'Ergebnis der rekonstruierte Zustand').
- **Ist:** fields={} in jedem Fall, Status succeeded.
- **Nutzerwirkung:** Snapshots und Zeitpunkt-Zustände wirken fertig, liefern aber keine Daten; Audits oder Entscheidungen darauf sind unmöglich, und nichts warnt.
- **Ursache (belegt):** TrailUpTo (history/pg_repository.go:150-171) liest nur entity_change; CI-Änderungen landen in ci_change (nur für List vereinheitlicht, :64-117); history.Record (:37-60) hat keinen produktiven Aufrufer, kein Trigger schreibt entity_change. TestLongOperationsRunAsJobsAgainstPostgres prüft nur result.entity_id (merge_job_integration_test.go:121-123).
- **Empfehlung:** Replay aus der vereinheitlichten CI-Historie (ci_change/relationship_change, AUD-08) oder entity_change bei jeder Änderung schreiben; 404/failed für unbekannte oder unsichtbare Entitäten; Test um Feldinhalte erweitern.
- **Retest (verbindlich):** PG-Integrationstest: CI per PATCH umbenennen → Snapshot enthält den neuen Namen, Zeitpunkt vor der Änderung den alten; Asset analog; unbekannte ID → 404/failed; Client-2 auf Client-1-CI → not found; go test -count=1 -run '<Testname>' → PASS.
- **Kodierender Test:** keiner (nicht kodiert; Probe f7)

<a id="f-165"></a>
### F-165 (P2) – Numerischer AST-Vergleich auf einem dynamischen Attribut bricht die ganze Abfrage ab, sobald ein sichtbares CI einen nicht-numerischen Wert unter dem Schlüssel speichert

- **Betroffene WPs:** WP-187 · **Komponente:** backend/internal/savedview/filter.go · **Quelle:** Verifikation (B24)
- **Reproduktion:** Probe VP/probe/zz_verify_b24_sv_test.go (go test -overlay VP/probe/overlay-sv.json -run TestVerifyB24SavedViews -v ./internal/savedview/, audit_db_22): CIs mit attributes.ram_gb 64, 32 und '16GB'; POST /api/v1/search/query {where:{field:'attributes.ram_gb', op:'gte', value:16}} → 400 'count query results: ERROR: invalid input syntax for type numeric: 16GB (SQLSTATE 22P02)'.
- **Soll:** 200 mit den CIs, deren numerisches ram_gb ≥ 16; nicht-numerische Werte matchen nicht (SRC-03).
- **Ist:** 400 für die ganze Abfrage; kombiniert mit einem weiteren Prädikat teils 200 (abhängig von der PG-Auswertungsreihenfolge).
- **Nutzerwirkung:** Gespeicherte Sichten und Abfragen wie 'RAM ≥ 16' oder 'vlan = 10' scheitern für die gesamte Organisation, sobald ein einziges CI unter dem Schlüssel einen Text trägt (Discovery-Daten, anderer Typ mit gleichem Schlüssel).
- **Ursache (belegt):** fieldKind (filter.go:342-351) wählt kindNumber nach dem JSON-Werttyp; comparison (filter.go:459-465) castet (attributes->>key)::numeric für jede Zeile ohne jsonb_typeof-Guard.
- **Empfehlung:** Cast absichern (CASE WHEN jsonb_typeof(attributes->key)='number' THEN … END, analog eq/ne/in) oder Safe-Cast-Funktion; PG-Test mit gemischten Typen.
- **Retest (verbindlich):** PG-Integrationstest in backend/internal/savedview: ram_gb 64, 8, '16GB' und ein CI ohne Schlüssel; gte 16 → 200 genau {64}; eq 8 → {8}; ne 8, in [8,64] und exists false korrekt; kein Fehler; go test -count=1 -run '<Testname>' ./internal/savedview/ → PASS.
- **Kodierender Test:** keiner (nicht kodiert; Verifier-Probe)

<a id="f-166"></a>
### F-166 (P2) – PATCH ohne 'shared' hebt die Org-Freigabe einer gespeicherten Sicht still auf

- **Betroffene WPs:** WP-188 · **Komponente:** backend/internal/savedview (model.go, pg_repository.go) · **Quelle:** Verifikation (B24)
- **Reproduktion:** Probe zz_verify_b24_sv_test.go (audit_db_22): Owner POST /api/v1/saved-views {name, entity_kind:'ci', shared:true, sort, columns} → 201 shared=true; Owner PATCH /saved-views/{id} {name:'Shared renamed'} → 200 shared=false; anderes Org-Mitglied GET → 404; SELECT shared FROM saved_view → false.
- **Soll:** PATCH ändert nur den Namen; die Sicht bleibt geteilt und für die Org lesbar (VIE-01 is_shared).
- **Ist:** Freigabe still aufgehoben (sort/columns bleiben, da nil-geprüft).
- **Nutzerwirkung:** Kollegen verlieren nach jedem PATCH durch API-Clients oder Integrationen still den Zugriff; die aktuelle UI patcht nicht (savedViewApi.update unbenutzt), eine künftige Edit-UI würde den Fehler auslösen.
- **Ursache (belegt):** UpsertRequest.Shared ist bool mit omitempty (model.go:40); Update hängt immer 'shared = $n' mit req.Shared an (pg_repository.go:153-155).
- **Empfehlung:** *bool für Shared (und weitere PATCH-Felder) verwenden und shared nur setzen, wenn gesendet.
- **Retest (verbindlich):** PG-Integrationstest: shared anlegen → PATCH nur name → GET als anderes Mitglied 200 mit shared=true; go test -count=1 -run '<Testname>' ./internal/savedview/ → PASS.
- **Kodierender Test:** keiner (nicht kodiert; die geplante Defektdatei audit_t7_savedview_auditdefect_test.go wurde nicht geschrieben, der Verweis wurde aus dem Header entfernt; backend/internal/savedview/audit_t7_savedview_rights_test.go:11-13 nennt den Befund ausdrücklich als nicht kodiert)
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – tests: „keiner (die im Header referenzierte Datei audit_t7_savedview_auditdefect_test.go wurde nicht geschrieben)“ → „keiner (nicht kodiert; die geplante Defektdatei audit_t7_savedview_auditdefect_test.go wurde nicht geschrieben, der Verweis wurde aus dem Header entfernt; backend/internal/savedview/audit_t7_savedview_rights_test.go:11-13 nennt den Befund ausdrücklich als nicht kodiert)“. Sachlich falsch. Laut defect_tests_wave.write.not_codified wurde der Verweis aus dem Header entfernt; der aktuelle Header (Zeilen 11-13) enthält ihn nicht mehr und sagt 'The finding "PATCH without 'shared' silently unshares an org-shared view" is not codified here.'

<a id="f-167"></a>
### F-167 (P2) – GET /cis/{id}/metrics zeigt Metriknamen von CIs außerhalb des Client-Scopes und antwortet 200 für unsichtbare oder nicht existente CIs

- **Betroffene WPs:** WP-190 · **Komponente:** backend/internal/monitoring (monitoring.go, pg_store.go) · **Quelle:** Verifikation (B24); identisch mit T1-Befund WP-040 (P2, 'Metric series names of a foreign-client CI are visible via GET /api/v1/cis/{id}/metrics (MetricNames without scope predicate)', Probe TestProbeV05CIMetricsFullRouter) – im Gesamtbericht nur einmal zählen
- **Reproduktion:** Probe VP/probe/zz_verify_b24_mon_test.go (go test -overlay VP/probe/overlay-mon.json -run TestVerifyB24CIMetrics -v ./internal/server/, audit_db_22): sweep_reachable für ein Client-2-CI ingestieren; GET /api/v1/cis/<client2-ci>/metrics im Client-1-Kontext → 200 series [{name:'sweep_reachable', points:[]}]; GET einer nicht existenten UUID → 200 series [].
- **Soll:** 404 für ein für den Aufrufer nicht sichtbares oder nicht existentes CI.
- **Ist:** 200 mit Seriennamen bzw. leerer Serie.
- **Nutzerwirkung:** Client-beschränkte Nutzer erfahren, welche Metriken für fremde CIs existieren (Metadaten-Leck innerhalb der Org).
- **Ursache (belegt):** MetricNames (pg_store.go:230-247) liest metric_sample_v ohne das ci-RLS-Prädikat von metricWhere (pg_store.go:187); CIMetrics (monitoring.go:836-878) prüft die CI-Sichtbarkeit nicht.
- **Empfehlung:** ci-Prädikat in MetricNames ergänzen und 404 liefern, wenn das CI nicht sichtbar ist; PG-Routentest.
- **Retest (verbindlich):** PG-Integrationstest der Route: Client-1-GET auf Client-2-CI → 404; org-weit → Serien vorhanden; unbekannte UUID → 404; ungültige ID → 400; go test -count=1 -run '<Testname>' ./internal/monitoring/ → PASS.
- **Kodierender Test:** keiner (nicht kodiert; Verifier-Probe)
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – quelle: „Verifikation (B24)“ → „Verifikation (B24); identisch mit T1-Befund WP-040 (P2, 'Metric series names of a foreign-client CI are visible via GET /api/v1/cis/{id}/metrics (MetricNames without scope predicate)', Probe TestProbeV05CIMetricsFullRouter) – im Gesamtbericht nur einmal zählen“. Ursache (MetricNames ohne das Prädikat aus metricWhere, CIMetrics ohne Sichtbarkeitsprüfung), Endpunkt und Priorität stimmen mit dem bestätigten T1-Befund WP-040 überein. Der Querverweis fehlt, deshalb würde der Defekt im Phase-1-Gesamtbericht doppelt gezählt.

<a id="f-168"></a>
### F-168 (P2) – PDU-Messwerte werden nicht erhoben (nur UPS-MIB)

- **Betroffene WPs:** WP-190 · **Komponente:** collector/collectorcmd/collectorcmd.go · **Quelle:** Audit + Verifikation (B24)
- **Reproduktion:** grep 'PollMetric{' im Collector: nur defaultPollMetrics (collectorcmd.go:1473) und upsPollMetrics (:1588); pollPowerValues fragt für jeden Power-Treffer, auch für PDUs, die UPS-OIDs ab.
- **Soll:** MON-02: UPS- und PDU-Werte aus den Power-Plugins.
- **Ist:** Kein PDU-OID-Satz; eine PDU liefert keine Samples.
- **Nutzerwirkung:** Keine Last-/Leistungslinie bei PDU-CIs.
- **Ursache (belegt):** Kein PDU-OID-Set in pollPowerValues (collectorcmd.go:1588-1622).
- **Empfehlung:** PDU-OIDs nach Gerätetyp ergänzen; Fake-Agent-Unittest.
- **Retest (verbindlich):** Collector-Unittest: Fake-SNMP-PDU-Agent → Samples kind=power mit Label target; go test -count=1 -run '<Testname>' ./collectorcmd/ → PASS (Upload zusätzlich abhängig von L-T7-ui-02).
- **Kodierender Test:** keiner

<a id="f-169"></a>
### F-169 (P2) – Detailseiten ignorieren den gewählten Client-/Site-Scope

- **Betroffene WPs:** WP-191 · **Komponente:** frontend/src/api/client.ts (withScope), Detailseiten (CIDetailPage u. a.) · **Quelle:** Audit + Verifikation (B24)
- **Reproduktion:** Als org-weiter Nutzer Scope Client A wählen, /cmdb/<ID eines Client-B-CI> öffnen (Suche oder URL) → Seite lädt unverändert ohne Hinweis; withScope wirkt nur auf SCOPED_LISTS (client.ts:364-387); ScopeSwitcher.test.tsx kodiert withScope('/cis/abc') unverändert.
- **Soll:** Verbindliches WP-191-Kriterium: Detailseiten berücksichtigen den gewählten Scope (Hinweis/Guard oder gescopte eingebettete Listen).
- **Ist:** Kein Hinweis, kein Guard, eingebettete Listen ungescoped.
- **Nutzerwirkung:** Nutzer handeln an Objekten außerhalb des gewählten Arbeitsbereichs, ohne es zu merken.
- **Ursache (belegt):** Designentscheidung in withScope (client.ts:364-387); keine Detailseitenlogik.
- **Empfehlung:** Verhalten festlegen (z. B. Banner 'außerhalb des aktuellen Scopes' plus gescopte Relationslisten), umsetzen und testen.
- **Retest (verbindlich):** Vitest und Playwright (Real-Stack): mit Scope Client A zeigt das Detail eines Client-B-CI den Hinweis und gescopte Unterlisten; Test PASS.
- **Kodierender Test:** keiner (ScopeSwitcher.test.tsx kodiert das Ist-Verhalten)

<a id="f-170"></a>
### F-170 (P2) – Tabellen ohne Pfeiltasten-Bedienung, ohne Tabellenbaustein und ohne Test

- **Betroffene WPs:** WP-192 · **Komponente:** frontend/src/pages/CIListPage.tsx (kein gemeinsamer Tabellenbaustein) · **Quelle:** Audit + Verifikation (B24)
- **Reproduktion:** Verifier-Playwright-Probe VP/pw/v-kbd.spec.ts 'CI table' (Chromium, built dist :4189, gemockte API): auf /cmdb ersten Zeilenlink fokussieren, ArrowDown → Fokus bleibt auf 'core-sw-01'; Zeilen tabIndex -1 mit je 2 Fokuszielen (Checkbox, Link).
- **Soll:** WP-192: Tabellen vollständig tastaturbedienbar (Pfeiltasten) mit Test je Baustein.
- **Ist:** Keine Pfeiltasten-Navigation, kein Test; nur Tab über Checkbox/Link und Enter auf dem Link.
- **Nutzerwirkung:** Tastaturnutzer müssen durch jede Checkbox und jeden Link tabben; das Akzeptanzkriterium ist unbelegt.
- **Ursache (belegt):** Kein Tabellenbaustein und keine Key-Handler (CIListPage.tsx:269-290).
- **Empfehlung:** Tabellenmuster definieren (Roving Tabindex, Pfeile zwischen Zeilen, Enter öffnet, Space wählt) und in Vitest und Playwright testen.
- **Retest (verbindlich):** Playwright (alle Projekte): Tab in die CI-Tabelle, ArrowDown wechselt die Zeile, Enter öffnet das Detail, Space toggelt die Checkbox; Vitest-Tastaturtest CIListPage PASS.
- **Kodierender Test:** keiner

<a id="f-171"></a>
### F-171 (P2) – Command-Palette gibt den Fokus nach dem Schließen nicht zurück (Fokus landet auf <body>)

- **Betroffene WPs:** WP-192 · **Komponente:** frontend/src/components/CommandPalette.tsx · **Quelle:** Audit + Verifikation (B24)
- **Reproduktion:** VP/pw/v-kbd.spec.ts 'palette returns focus after Escape': CI-Link auf /cmdb fokussieren, Ctrl+K, Escape → document.activeElement = BODY (vorher Fokus auf dem Link).
- **Soll:** Fokus kehrt zum zuvor fokussierten Element zurück (wie bei Modal implementiert); WCAG 2.4.3.
- **Ist:** Fokus auf <body>.
- **Nutzerwirkung:** Tastatur- und Screenreader-Nutzer verlieren nach jeder Palettennutzung ihre Position.
- **Ursache (belegt):** CommandPalette.tsx:433 rendert Dialog.Root ohne Dialog.Trigger und ohne onCloseAutoFocus; Radix DialogContentModal (react-dialog 1.1.18 dist/index.mjs:148-151) verhindert den Default und fokussiert den null-triggerRef.
- **Empfehlung:** returnFocusRef-Muster aus Modal.tsx:20-40 übernehmen (activeElement beim Öffnen merken, in onCloseAutoFocus fokussieren).
- **Retest (verbindlich):** Vitest und Playwright: Button fokussieren, Ctrl+K, Escape → activeElement ist dieser Button (nicht document.body); PASS.
- **Kodierender Test:** keiner

<a id="f-172"></a>
### F-172 (P2) – WCAG-2.1-AA-Kontrastverstöße auf Hauptseiten, die a11y.spec.ts nicht abdeckt

- **Betroffene WPs:** WP-193 · **Komponente:** frontend/src/pages (AssetListPage, StocktakeListPage, AssignmentListPage, DocumentListPage, TicketListPage), Dark-Mode-Primärfarbe; frontend/e2e/a11y.spec.ts · **Quelle:** Audit + Verifikation (B24)
- **Reproduktion:** VP/pw v-axe.spec.ts (node serve.cjs frontend/dist 4189; axe-Tags wcag2a/aa/21a/21aa, gemockte API) auf /assets, /stocktake, /assignments, /documents, /tickets (hell) und /cmdb/<id> (dunkel).
- **Soll:** NFR-07/UI-18: axe-Prüfung aller Hauptseiten ohne AA-Verstöße, in allen Farbschemata.
- **Ist:** color-contrast (serious): Leerzustandszellen text-gray-400 auf Weiß = 2,53:1 (5 Seiten); Dark-Mode-Zurücklink #2a6df4 auf #16181d = 3,88:1. a11y.spec.ts deckt 11 Routen mit leeren Daten nur hell ab, ohne CI-Detail.
- **Nutzerwirkung:** Sehbehinderte Nutzer können Leerzustandsmeldungen und den Zurücklink im CI-Detail nicht lesen; NFR-07 nicht erfüllt.
- **Ursache (belegt):** text-gray-400 in AssetListPage.tsx:267, StocktakeListPage.tsx:132, AssignmentListPage.tsx:120, DocumentListPage.tsx:121, TicketListPage.tsx:187; Primärfarbe im Dark Mode; MAIN_PAGES in a11y.spec.ts:23-35 lässt diese Routen, das CI-Detail und den Dark Mode aus.
- **Empfehlung:** text-gray-600/dark:text-gray-300 für Leerzustände und hellere Primärfarbe im Dark Mode; a11y.spec.ts auf alle Routen inkl. /cmdb/:id mit Daten und einen Dark-Pass erweitern.
- **Retest (verbindlich):** Erweiterte frontend/e2e/a11y.spec.ts (alle Hauptrouten, mit Daten, hell und dunkel) grün in allen sechs Playwright-Projekten (CI-Artefakt).
- **Kodierender Test:** keiner (bestehende a11y.spec.ts 12/12 PASS deckt die betroffenen Seiten nicht ab)

<a id="f-173"></a>
### F-173 (P2) – Browsermatrix deckt keine Vorversionen ab; Nicht-Chromium-Engines unbelegt

- **Betroffene WPs:** WP-193 · **Komponente:** frontend/playwright.config.ts, .github/workflows/ci.yml · **Quelle:** Audit + Verifikation (B24)
- **Reproduktion:** playwright.config.ts:23-25 (Kommentar: Vorversionen nicht abgedeckt); lokal nur chromium/mobile-chrome ausführbar (firefox/webkit/msedge-Downloads durch Netzwerk-Policy blockiert); CI-Ergebnisse von hier nicht beobachtbar.
- **Soll:** UI-18/NFR-07: aktuelle und Vorversion von Chrome, Edge, Firefox, Safari sowie Mobile Safari/Chrome mit ausgeführter Evidenz.
- **Ist:** Je Engine ein gebündeltes Build, keine Vorversionen; Evidenz nur für Chromium-basierte Projekte.
- **Nutzerwirkung:** Regressionen in Safari/Firefox oder älteren Browserversionen bleiben unentdeckt.
- **Ursache (belegt):** Playwright bündelt genau eine Browserversion; es gibt keinen Vorversions-Job.
- **Empfehlung:** Grüne CI-Läufe je Projekt als Evidenz archivieren; Vorversions-Job (älteres gepinntes Playwright oder Browser-Grid) oder PO-Entscheidung.
- **Retest (verbindlich):** CI-Artefakte playwright-report-{chromium,edge,firefox,webkit,mobile-chrome,mobile-safari} grün plus grüner Vorversions-Job.
- **Kodierender Test:** keiner

<a id="f-174"></a>
### F-174 (P2) – Status-/Enum-Schlüssel werden roh statt über den i18n-Katalog angezeigt

- **Betroffene WPs:** WP-194 · **Komponente:** frontend/src/pages (AssetListPage, TicketListPage, OrderListPage, KeyListPage, SecurityPage, RoomPlanPage u. a.), eslint.config.js · **Quelle:** Audit + Verifikation (B24)
- **Reproduktion:** VP/pw/v-i18n.spec.ts 'assets list status rendering (de)': gemocktes /api/v1/assets mit status 'in_stock', category 'laptop', UI-Sprache de → Zeile 'INV-1 Laptop 1 laptop in_stock — Löschen'.
- **Soll:** GLO-10: Anzeige nur über i18n (z. B. 'Auf Lager').
- **Ist:** Rohe Schlüssel per <Badge>{x.status}</Badge> u. a. an AssetListPage.tsx:255, TicketListPage.tsx:151, OrderListPage.tsx:87, KeyListPage.tsx:102, SecurityPage.tsx:143, RoomPlanPage.tsx:273.
- **Nutzerwirkung:** Deutsche und englische Nutzer sehen technische Schlüssel; GLO-10 nicht geschlossen.
- **Ursache (belegt):** Kein t() für status/severity/category; die Lint-Regel (eslint.config.js:22-40) gilt nur für 4 Dateien und erkennt Ausdrücke nicht.
- **Empfehlung:** Alle status-/severity-/category-Werte über t(`<domain>.statuses.${v}`, v) mit Katalogschlüsseln rendern; Katalogtest und Source-Scan auf src/pages/** ausweiten.
- **Retest (verbindlich):** Katalogtest für jedes Status-Enum und Source-Scan-Test gegen rohes Status-Rendering (Negativfixture schlägt an): cd frontend && npx vitest run src/i18n/catalog.test.ts → PASS; Probe zeigt übersetztes Label.
- **Kodierender Test:** keiner

<a id="f-175"></a>
### F-175 (P2) – CI-Detail zeigt die Typ-UUID statt der lokalisierten Typbezeichnung

- **Betroffene WPs:** WP-194 · **Komponente:** frontend/src/pages/CIDetailPage.tsx · **Quelle:** Verifikation (B24) + Defekttest-Welle
- **Reproduktion:** VP/pw/v-i18n.spec.ts 'CI detail type row' bzw. CITypeLabels.auditdefect.tsx:153: /ci-types mit display_name de 'Server (physisch)', /cmdb/<id> in de öffnen → die Detailzeile zeigt 7a0c1e2d-0000-4000-8000-0000000000a1, das de-Label fehlt ('expected 0 to be greater than 0').
- **Soll:** Typname über localizedText(display_name) aus den WP-115-Sprachmaps.
- **Ist:** Rohe ci_type_id-UUID im Badge (CIDetailPage.tsx:114) und in der Detailzeile (:136).
- **Nutzerwirkung:** Detailseite schwer lesbar, keine lokalisierten Typbezeichnungen.
- **Ursache (belegt):** CIDetailPage rendert ci.ci_type_id ohne useCITypes/localizedText (CIDetailPage.tsx:114, :136); separate Codestelle zum Formular-Defekt L-T7-ui-03.
- **Empfehlung:** Typ über useCITypes auflösen und localizedText(display_name) || name rendern.
- **Retest (verbindlich):** cd frontend && npx vitest run -c vitest.auditdefect.config.ts src/pages/CITypeLabels.auditdefect.tsx -t 'shows the localized type label on the CI detail page' → PASS.
- **Kodierender Test:** frontend/src/pages/CITypeLabels.auditdefect.tsx:153 'shows the localized type label on the CI detail page, not the type UUID' – failed as expected (SP/verify-tests-t357/results.tsv)

<a id="f-176"></a>
### F-176 (P2) – Review-Inbox: 'Neu anlegen' (action new) für unclassified_device scheitert immer; Items mit unbekanntem Typ erhalten ein kryptisches 400 und bleiben offen

- **Betroffene WPs:** WP-196 · **Komponente:** backend/internal/discovery/review.go; frontend ReviewInboxPage.tsx / hooks/useReviews.ts · **Quelle:** Verifikation (B25)
- **Reproduktion:** go test -overlay SP/work/verify-B25/overlay/overlay-review.json -run TestProbeV25ReviewEffects ./internal/discovery/ (TEST_DATABASE_URL=audit_db_23): SNMP-Record mit CIType 'pv-unknown-type' ingestieren → unclassified_device (reason unknown_ci_type); POST /api/v1/review-items/{id}/resolve {action:'new'} (wie der Inbox-Button) → 400 'a field value has an invalid format or is out of range'; Item bleibt open.
- **Soll:** UI-16: ein CI wird angelegt (z. B. nach Typwahl) oder ein klares 4xx erklärt den nächsten Schritt; die UI bietet keine Aktion an, die nicht gelingen kann.
- **Ist:** 'new' gelingt für unclassified_device nie (unknown_ci_type → 400; no_profile_match → 409 by design, review.go:165-167), wird aber über REVIEW_ACTIONS angeboten.
- **Nutzerwirkung:** Operatoren können aus der Inbox kein CI für ein unklassifiziertes Gerät anlegen und sehen eine nichtssagende Fehlermeldung; der Umweg über manuelle Anlage scheitert zusätzlich an L-T7-ui-03.
- **Ursache (belegt):** discovery/review.go:172 setzt CITypeID = payload 'ci_type_name' (unaufgelöster Typname) und übergibt ihn an ciRepo.Create; die PG-uuid-Spalte lehnt ab (22P02 → 400). TestResolveReviewItemCreate (Memory-Repo) akzeptiert jeden String, TestTST01ReviewResolution stubbt alle Effekte.
- **Empfehlung:** Typnamen wie beim Ingest auflösen (discovery.go:1157-1166) oder ci_type_id in der Auflösung annehmen und in der UI abfragen; sonst 'new' für unclassified_device nicht anbieten und den nächsten Schritt erklären.
- **Retest (verbindlich):** PG-Integrationstest über POST /api/v1/review-items/{id}/resolve mit discovery.NewHandler(PG-Repos): unclassified_device mit auflösbarem Typ oder ci_type_id → 200 und CI existiert; ohne auflösbaren Typ → 4xx mit klarer Meldung, die UI zeigt sie; zusätzlich e2e-real merge und new über die Inbox.
- **Kodierender Test:** keiner (nicht kodiert; Verifier-Probe)

<a id="f-177"></a>
### F-177 (P2) – Onboarding-Abnahmesuite: Schritt 'Scope bestätigen' (ABN-01 d) ist optional und unbelegt

- **Betroffene WPs:** WP-200 · **Komponente:** frontend/e2e/onboarding.spec.ts · **Quelle:** Audit + Verifikation (B25)
- **Reproduktion:** onboarding.spec.ts:243-255: if (await confirm.first().isVisible().catch(() => false)) click; danach optional 'Weiter'; keine Scope- oder Review-Assertion. Realer Lauf SP/runs/e2e-onboarding-real.log 6/6 PASS, obwohl der Collector 'suggested:3' meldete.
- **Soll:** ABN-01(d): Pflichtschritt 'Scope bestätigen' mit Nachweis (Scope existiert, Vorschlag resolved/accept).
- **Ist:** Eine defekte Scope-Bestätigung ließe die Suite trotzdem bestehen, weil 'Weiter' den Schritt überspringt.
- **Nutzerwirkung:** Regressionen in der Scope-Bestätigung bleiben in der Abnahmesuite unentdeckt; dadurch ist auch das WP-199-Teilkriterium 'Scope-Bestätigung' real unbelegt.
- **Ursache (belegt):** Defensive bedingte Klicks im Spec (onboarding.spec.ts:243-255).
- **Empfehlung:** Auf einen Vorschlag warten, unbedingt klicken und per GET /discovery-scopes und /review-items?kind=scope_suggested prüfen.
- **Retest (verbindlich):** Spec wartet auf einen Vorschlag, klickt unbedingt und prüft GET /discovery-scopes?collector_id=… sowie /review-items?kind=scope_suggested (resolved/accept) und den Fortschritt 3 → 4 → 5; ein absichtlich gebrochener Resolve-Endpunkt macht die Suite rot.
- **Kodierender Test:** frontend/e2e/onboarding.spec.ts (6/6 PASS, kodiert den Defekt nicht)

<a id="f-178"></a>
### F-178 (P2) – Permissives Mock-Backend der E2E-Suite (unbekannte Routen → 200, keine Vertragsprüfung) maskiert UI→API-Verdrahtungsfehler

- **Betroffene WPs:** WP-201 · **Komponente:** frontend/e2e/support/backend.ts, frontend/e2e/ci.spec.ts · **Quelle:** Audit + Verifikation (B25)
- **Reproduktion:** frontend/e2e/support/backend.ts:72-79: nicht gematchte /api/v1-Route → 200 mit EMPTY_LIST oder {}; ci.spec.ts:70 prüft im POST-Body nur name und discovery_source. Folge: ci.spec.ts ist in SP/runs/e2e-mocked.log grün, während Real-Stack L01 400 liefert (L-T7-ui-03) und die CI-Liste-Export-Buttons real 401 liefern (L-T7-ui-23).
- **Soll:** TST-03/TST-04 ('Mocks allein sind kein Nachweis'): unbekannte Routen schlagen fehl oder werden gemeldet; Requests und Responses werden gegen api/openapi.yaml validiert.
- **Ist:** Stiller Erfolg; Vertragsbrüche UI→API sind mit der Suite unentdeckbar.
- **Nutzerwirkung:** Die Kernablauf-Suite gibt falsche Sicherheit; reale Verdrahtungsfehler gelangen ungeprüft in Releases.
- **Ursache (belegt):** Permissiver Default-Handler (backend.ts:72-79) und schwache Body-Assertion (ci.spec.ts:70).
- **Empfehlung:** Strict-Modus (Fehler bei unbehandelter /api/v1-Route mit expliziter Allowlist) und OpenAPI-Schemavalidierung von Request- und Response-Bodies; ci.spec.ts prüft ci_type_id gegen gemockte /ci-types.
- **Retest (verbindlich):** Ein von einer Seite genutzter Endpunkt wird umbenannt → dessen Spec schlägt fehl; Negativfixture ci_type_id='server' lässt ci.spec.ts fehlschlagen; e2e-mocked-Lauf aller Projekte grün.
- **Kodierender Test:** keiner

<a id="f-179"></a>
### F-179 (P2) – Filter-AST validiert Wertformate nicht; DB-Fehlertext mit SQLSTATE wird in 400-Antworten offengelegt

- **Betroffene WPs:** WP-187 · **Komponente:** backend/internal/savedview (filter.go, handler.go) · **Quelle:** Audit + Verifikation (B24); gleicher Fehlerpfad (savedview/handler.go:222-225 err.Error() → 400 mit pgx-Text/SQLSTATE auf POST /api/v1/search/query) wie T1-Befund WP-015 'Filter query endpoint returns pgx error text for a non-UUID upstream_of/downstream_of' (P2) – im Gesamtbericht gemeinsam führen
- **Reproduktion:** Probe zz_verify_b24_sv_test.go (audit_db_22): where client_id eq 'not-a-uuid' → 400 '…invalid input syntax for type uuid… (SQLSTATE 22P02)'; created_at gt 'garbage' bzw. 2020 → 400 (22007); client_id in ['x'] → 400 22P02; attributes.ram in [{a:1}] → 200 data:null.
- **Soll:** 422 mit Feld where.* zum ungültigen Wert; kein interner Fehlertext.
- **Ist:** 400 mit PostgreSQL-Text und SQLSTATE; in [{}] → 200 data:null.
- **Nutzerwirkung:** Verwirrende Fehler für API- und künftige Klick-Builder-Nutzer; interne SQL-Details werden offengelegt.
- **Ursache (belegt):** validateComparison (filter.go:366-395) prüft nur den Go-Typ und überspringt in-Elemente; handler.go:224 mappt jeden Engine-Fehler auf 400 mit err.Error().
- **Empfehlung:** Werte je Feldart validieren (uuid, RFC3339/Datum, numerisch, bool) inkl. in-Elementen und ASTError → 422 liefern; DB-Fehler loggen, generische Meldung zurückgeben.
- **Retest (verbindlich):** API-Test POST /api/v1/search/query: ungültige uuid-/Zeit-/in-Werte → 422 mit Feld where.*, kein 'SQLSTATE' im Body; PASS.
- **Kodierender Test:** keiner
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – prioritaet: „P3“ → „P2“. Nach Definition ist P2 eine 'relevante … Validierungs-Einschränkung'. Der Defekt verletzt den verbindlichen Teil 'Validierung' des WP-187-Kriteriums; die Synthese selbst schreibt: 'Validierung nur teilweise erfüllt: … L-T7-ui-34'. Dazu kommt die Offenlegung von DB-Interna (PostgreSQL-Text mit SQLSTATE). Gruppenübergreifend konsistent: T1 stuft denselben Fehlerpfad auf demselben Endpunkt (POST /api/v1/search/query, savedview/handler.go:222-225 err.Error() → 400 mit 22P02) als P2 ein (WP-015 'Filter query endpoint returns pgx error text for a non-UUID upstream_of/downstream_of'), ebenso rund 15 gleichartige pgx-Leaks in T1. Ein geringfügiger Qualitätsmangel (P3) liegt damit nicht vor.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – quelle: „Audit + Verifikation (B24)“ → „Audit + Verifikation (B24); gleicher Fehlerpfad (savedview/handler.go:222-225 err.Error() → 400 mit pgx-Text/SQLSTATE auf POST /api/v1/search/query) wie T1-Befund WP-015 'Filter query endpoint returns pgx error text for a non-UUID upstream_of/downstream_of' (P2) – im Gesamtbericht gemeinsam führen“. Gruppenübergreifendes Duplikat derselben Ursache ohne Querverweis. Ohne ihn wird der Fehlerpfad im Gesamtbericht zweimal mit unterschiedlicher Priorität gezählt. Für L-01, L-03 und L-24 hat die Synthese solche Querverweise gesetzt, hier fehlt er.

<a id="f-180"></a>
### F-180 (P3) – GET /api/v1/racks/{id}/mounts eines Racks außerhalb des Scopes antwortet 200 (leer) statt 404 (Existenzorakel)

- **Betroffene WPs:** WP-014, WP-208 · **Komponente:** backend/internal/rack/pg_repository.go:247-252 (ListMounts) · **Quelle:** Defekttest-Welle (neuer Defekt)
- **Reproduktion:** Client-1-Principal: GET /api/v1/racks/<Rack von Client 2>/mounts, während GET /racks/{id} und /layout korrekt 404 liefern. Kodiert in TestAuditT1RackChannelSiteScope (Mounts-Pfad, audit_t1_review_rack_scope_auditdefect_test.go:178).
- **Soll:** 404 wie bei GET /racks/{id} und /racks/{id}/layout.
- **Ist:** 'GET /api/v1/racks/3b83463b-.../mounts (t1-rack-two) as client1: 200, want 404'; die Mount-Zeilen selbst bleiben durch mountVisible gefiltert.
- **Nutzerwirkung:** Verrät nur, dass ein fremdes Rack existiert; keine Inhaltsdaten.
- **Ursache (belegt):** ListMounts prüft die Rack-Existenz mit 'SELECT EXISTS(SELECT 1 FROM rack WHERE organization_id = $1 AND id = $2)' (pg_repository.go:252) statt mit rackVisible.
- **Empfehlung:** rackVisible in die Existenzprüfung von ListMounts aufnehmen (nach Korrektur von L-T1-02).
- **Retest (verbindlich):** TestAuditT1RackChannelSiteScope (-tags auditdefect, ./internal/tenant/rls/) grün, insbesondere Mounts-Pfad 404 für unsichtbare Racks.
- **Kodierender Test:** TestAuditT1RackChannelSiteScope (FAILS_AS_EXPECTED).

<a id="f-181"></a>
### F-181 (P3) – Client-Grant kombiniert mit Site-Grant schneidet die Dimensionen und verengt den Zugriff unter die Vereinigung der Rollen

- **Betroffene WPs:** WP-009 · **Komponente:** backend/internal/identity/principal.go (ResolveAccess, unionDimension); RLS-Dimensionen per AND · **Quelle:** Audit (B02) + Verifikation
- **Reproduktion:** ResolveAccess([{Clients:[C1]},{Sites:[S1]}]) -> Scope{Clients:[C1], Sites:[S1]} (als beabsichtigt asserted in TestResolveAccessUnitesScopesPerDimension, identity/principal_scope_test.go:56-62, PASS); RLS verknüpft die Dimensionen per AND.
- **Soll:** RBA-03 'Rechte = Vereinigung aller Rollen' legt Zugriff auf ganz C1 plus S1 nahe; RBA-03 sagt aber auch 'Scopes = Vereinigung der Scope-Mengen je Ebene', dem der Code wörtlich folgt (Spezifikationsambiguität).
- **Ist:** Zugriff nur auf Zeilen in C1 UND (Site S1 oder NULL); liegt S1 in einem anderen Client, sieht der Nutzer von keiner Rolle etwas (fail-closed, kein Leck).
- **Nutzerwirkung:** Ein Nutzer, der zusätzlich eine site-gescopte Rolle erhält, verliert den Großteil seines Client-Zugriffs.
- **Ursache (belegt):** Vereinigung je Dimension kombiniert mit per AND verknüpften RLS-Dimensionen; das GUC-Modell kann keine Vereinigung von (Client × Site)-Rechtecken ausdrücken.
- **Empfehlung:** Als Spezifikationsambiguität an den PO eskalieren; gemischte Zuweisungen in der Rollen-UI dokumentieren oder abweisen, oder das Modell auf Scope-Tupel je Grant erweitern.
- **Retest (verbindlich):** Nach PO-Entscheidung: Integrationstest mit Client-Rolle C1 und Site-Rolle S1, der die vereinbarte Sichtbarkeit von CIs in C1/S1, C1/S2 und C2/S1 assertet.
- **Kodierender Test:** keiner (TestResolveAccessUnitesScopesPerDimension asserted das aktuelle Verhalten als beabsichtigt).

<a id="f-182"></a>
### F-182 (P3) – Client-gescopter Principal kann keine Beziehung zu einem sichtbaren org-weiten (client NULL) CI anlegen

- **Betroffene WPs:** WP-025 · **Komponente:** Migration 000061:239-241, 000063:84-86 (WITH CHECK beider Endpunkte) · **Quelle:** Audit (B04) + Verifikation
- **Reproduktion:** probe_edge_orgwide.sql auf audit_db_08: 'client1 sees org-wide CI|1', danach INSERT ci_relationship eigenes CI -> org-weites CI: 'ERROR: new row violates row-level security policy for table ci_relationship'; analog network_interface am org-weiten CI.
- **Soll:** Nicht spezifiziert (PO-Entscheidung nötig): E-09 verbietet das Schreiben org-weiter Zeilen, eine Kante von einem eigenen CI zu geteilter Infrastruktur ist aber selbst nicht org-weit.
- **Ist:** WITH CHECK verlangt beide Endpunkt-Clients im Scope; die Kante wird abgewiesen.
- **Nutzerwirkung:** Auf einen Client beschränkte MSP-Techniker können Abhängigkeiten zu geteilter org-weiter Infrastruktur nicht modellieren.
- **Ursache (belegt):** Strikte Zwei-Endpunkt-WITH-CHECK-Bedingung.
- **Empfehlung:** PO-Entscheidung einholen; falls erlaubt: (source NULL oder im Scope) AND (target NULL oder im Scope) AND mindestens ein Endpunkt nicht NULL und im Scope, analog Site.
- **Retest (verbindlich):** probe_edge_orgwide.sql erneut ausführen und einen Fall in ci_children_integration_test.go gemäß Entscheidung ergänzen.
- **Kodierender Test:** keiner (nur Probe probe_edge_orgwide.sql).

<a id="f-183"></a>
### F-183 (P3) – Webhook RotateSecret (und Delivery-Requestpfade) nutzen einen org-weiten Scope statt des Request-Scopes

- **Betroffene WPs:** WP-016 · **Komponente:** backend/internal/webhook/pg_repository.go:277-293 (RotateSecret), pg_delivery_store.go:33-35 (orgScope) · **Quelle:** Audit (B02) + Verifikation
- **Reproduktion:** Verifier-Probe TestVerifyB02RotateSecretWithoutScope (audit_db_06): webhook.NewPGRepository(f.App).RotateSecret(context.Background(), orgA, id, 'new-no-scope', ...) -> err=nil, Secret geändert; GetByID mit context.Background() -> ErrNoTenantScope; RotateSecret(f.OrgCtx(orgB), orgA, id, ...) -> err=nil, Secret von Org A geändert.
- **Soll:** Requestpfade nutzen WithRequestTenant: fail-closed ohne Scope, ErrTenantMismatch bei fremder Org, app.user_id gesetzt.
- **Ist:** Org-weiter Scope mit leerem User, keine Fail-closed-Prüfung.
- **Nutzerwirkung:** Heute keine HTTP-Exposition (der Handler übergibt die Org des Principals); Defense-in-Depth-Lücke und fehlender Akteur im DB-Kontext.
- **Ursache (belegt):** RotateSecret baut OrgWideScope(orgID, ''); Deliveries sind dokumentiert org-eigen, RotateSecret undokumentiert.
- **Empfehlung:** RotateSecret und Request-Delivery-Lesepfade auf WithRequestTenant umstellen oder in einer Guard-Allowlist für OrgWideScope führen.
- **Retest (verbindlich):** Repo-Test: RotateSecret ohne Scope -> ErrNoTenantScope; mit Org-B-Scope auf eine Org-A-Subscription -> ErrTenantMismatch, Secret unverändert.
- **Kodierender Test:** keiner (nur Probe TestVerifyB02RotateSecretWithoutScope, SP/work/verify-B02/webhook-probe.log).

<a id="f-184"></a>
### F-184 (P3) – Webhook-Retry verwirft bei Claim-Fehler einer Organisation bereits geleaste Datensätze; keine faire Org-Reihenfolge

- **Betroffene WPs:** WP-022 · **Komponente:** backend/internal/webhook/pg_delivery_store.go:129-160 (ClaimDue), dispatcher.go:504-509 (ProcessDue) · **Quelle:** Audit (B03) + Verifikation
- **Reproduktion:** Codepfad: ClaimDue gibt beim ersten fehlschlagenden Org-Claim (records, err) zurück; ProcessDue loggt, zählt und kehrt bei err zurück, ohne die bereits geleasten Datensätze zu verarbeiten; Orgs stammen aus SELECT DISTINCT ohne ORDER BY und werden durch defaultClaimBatch begrenzt. Zur Laufzeit nicht reproduziert.
- **Soll:** Eine fehlschlagende Org verzögert andere Orgs nicht (wie ExpireDue mit errors.Join); geclaimte Datensätze werden verarbeitet; Orgs werden fair bedient.
- **Ist:** Geleaste Deliveries früherer Orgs warten bis Lease-Ablauf; eine dauerhaft fehlschlagende Org kann andere in jedem Zyklus verzögern; ein großer Rückstau kann andere Orgs im Batch-Limit verdrängen (Hypothese).
- **Nutzerwirkung:** Verzögerte Webhook-Zustellungen für unbeteiligte Mandanten (mandantenübergreifende Verfügbarkeit); kein Datenverlust.
- **Ursache (Hypothese):** Hypothese (nur Code-Lesung): frühes return in ClaimDue/ProcessDue und unsortierte DISTINCT-Org-Liste.
- **Empfehlung:** Mit den übrigen Orgs fortfahren und Fehler joinen; mit einem Fehler zurückgegebene Datensätze verarbeiten; Orgs nach ältestem next_retry_at ordnen und Anteil je Org begrenzen.
- **Retest (verbindlich):** Integrationstest mit 2 Orgs, bei dem der Claim von Org 1 fehlschlägt (injizierter Store-Fehler): Deliveries von Org 2 werden im selben Zyklus geclaimt und gesendet; Datensätze von Org 1 hängen nicht bis Lease-Ablauf.
- **Kodierender Test:** keiner

<a id="f-185"></a>
### F-185 (P3) – link_count/ci_count driften unter RLS und sind direkt schreibbar; Dokumente können für alle unsichtbar oder fremden Clients sichtbar werden

- **Betroffene WPs:** WP-028 · **Komponente:** Migration 000064:160-193 (count_document_links/count_maintenance_window_cis, SECURITY INVOKER) · **Quelle:** Verifikation (B04)
- **Reproduktion:** probe_doc_link_count.sql: Dokument mit Links auf C1-CI und C2-Asset (link_count 2); Client-1 entfernt eigenen Link -> 'actual links / link_count|1|2'; Client-2 entfernt letzten Link -> '|0|2', org-weiter Principal sieht es nicht ('A3 ...|0'). Teil B: Client-1 setzt UPDATE document SET link_count = 0 -> 'B1 client-3 principal (no link) sees the c1+c2 document after tampering|1'.
- **Soll:** 000064:20-23: 'die Zähler bleiben exakt'; Zähler dürfen von Mandanten nicht beschreibbar sein.
- **Ist:** Dekrement wird still übersprungen, wenn der Entfernende den Parent nicht mehr sieht; link_count/ci_count sind normale beschreibbare Spalten mit Org-only WITH CHECK.
- **Nutzerwirkung:** Derzeit nur DB-seitig (kein Unlink-Endpunkt, UpdateRequest ohne link_count); künftige Unlink-Funktionen oder interne Jobs würden Dokumente verwaisen lassen oder Clients ohne Link offenlegen.
- **Ursache (belegt):** SECURITY-INVOKER-Zählertrigger, kein Schutz gegen direkte Zählerschreibvorgänge.
- **Empfehlung:** Zählertrigger als SECURITY DEFINER (oder Zähler durch EXISTS-basierte Policy ersetzen) und direkte Änderungen an link_count/ci_count abweisen (BEFORE-UPDATE-Trigger oder Spaltenrecht).
- **Retest (verbindlich):** probe_doc_link_count.sql erneut: A2 link_count 0 und A3 = 1, Teil B abgewiesen; Integrationstest zur Zählerintegrität.
- **Kodierender Test:** keiner (nur Probe probe_doc_link_count.sql).

<a id="f-186"></a>
### F-186 (P3) – Traceability meldet MGT-02 (Disposal) durch WP-028 geschlossen, disposal_record behält aber eine reine Org-Policy

- **Betroffene WPs:** WP-028 · **Komponente:** disposal_record (Schema/Policy); docs/traceability.csv:124; docs/plan/abdeckung.csv:163 · **Quelle:** Verifikation (B04)
- **Reproduktion:** psql: disposal_record ohne client_id/site_id, Policy disposal_record_isolation USING nur organization_id; traceability.csv:124 ordnet MGT-02 module_object_integration_test.go zu, der disposal nicht berührt.
- **Soll:** Entweder DB-seitiger Scope für disposal_record oder korrigierte Traceability-Zeile.
- **Ist:** Scope nur in Go (disposal/pg_repository.go:38-39, 'no client column yet'), getestet durch TestDisposalRepositoryClientScope.
- **Nutzerwirkung:** Keine DB-Defense-in-Depth für Entsorgungsdatensätze; der Abdeckungsbericht überzeichnet den Abschluss von MGT-02.
- **Ursache (belegt):** disposal_record wurde in 000064 ausgelassen.
- **Empfehlung:** Abgeleitete client_id/site_id und Policies ergänzen oder traceability.csv/abdeckung.csv korrigieren.
- **Retest (verbindlich):** Integrationstest: Client-1-Principal selektiert disposal_record eines Client-2-Assets direkt -> 0; INSERT mit Bezug auf Client-2-Asset -> 42501.
- **Kodierender Test:** keiner

<a id="f-187"></a>
### F-187 (P3) – Migration 000066 meldet nicht konforme Storage-Keys nur als Anzahl und leert sie irreversibel

- **Betroffene WPs:** WP-030 · **Komponente:** backend/migrations/000066_document_storage_key_check.up.sql:10-27 (+ down) · **Quelle:** Audit (B04) + Verifikation
- **Reproduktion:** probe_storage_key_cleanup.sql (audit_db_08): Constraint in einer Tx droppen, Keys org/<fremde-org>/..., documents/<org>/<doc>/1 und org/<org>/documents/<doc>/1 einfügen, DO-Block ausführen -> 'NOTICE: cleared 1 document storage keys outside the organization prefix'; der fremde Key wird '' ohne id/Key-Protokoll.
- **Soll:** WP-030 AK3: abweichende Keys werden je Dokument identifizierbar gemeldet, damit Betreiber prüfen oder neu hochladen können.
- **Ist:** Nur aggregierte NOTICE; storage_key mit '' überschrieben; 000066.down.sql kann nicht wiederherstellen.
- **Nutzerwirkung:** Nach einem Upgrade verlieren Dokumente still ihren Inhaltsverweis, ohne dass Betreiber erkennen, welche.
- **Ursache (belegt):** Nur GET DIAGNOSTICS ROW_COUNT, In-place-Update.
- **Empfehlung:** (id, organization_id, storage_key) vor dem Leeren in eine Report-/Audit-Tabelle kopieren oder je Zeile RAISE NOTICE; in den Upgrade-Notes erwähnen.
- **Retest (verbindlich):** Probe erneut: Report enthält Dokument-id und alten Key; Migrationstest 65->66.
- **Kodierender Test:** keiner (nur Probe probe_storage_key_cleanup.sql).

<a id="f-188"></a>
### F-188 (P3) – Dokument-Formular verlangt weiterhin einen 'Storage-Key', den die API ignoriert; kein Upload/Download in der UI

- **Betroffene WPs:** WP-030 · **Komponente:** frontend/src/pages/DocumentListPage.tsx:47, :151-153; frontend/src/api/client.ts:948 · **Quelle:** Audit (B04) + Verifikation
- **Reproduktion:** DocumentListPage.tsx:47 'if (!form.title || !form.file_name || !form.storage_key) return;' und Pflichtfeld :151-153; client.ts:948 DocumentCreateRequest.storage_key: string.
- **Soll:** Die UI fragt keinen Storage-Key ab und bietet Upload über PUT /api/v1/documents/{id}/content sowie Download über /content.
- **Ist:** Nutzer müssen einen beliebigen, verworfenen Key eintippen; Inhalte können aus der UI nicht hochgeladen werden.
- **Nutzerwirkung:** Totes Pflichtfeld; der UI-Fluss legt nur einen Datensatz ohne Inhalt an (in G1 gering, Modul aus).
- **Ursache (belegt):** API-Vertrag geändert, handgeschriebener Client und Seite nicht nachgezogen.
- **Empfehlung:** storage_key aus Request-Typ und Formular entfernen; Upload- und Download-Aktionen ergänzen.
- **Retest (verbindlich):** Vitest DocumentListPage: Anlage ohne Storage-Key, Request-Body ohne storage_key; später e2e-real Upload/Download.
- **Kodierender Test:** keiner

<a id="f-189"></a>
### F-189 (P3) – Frontend agentApi.enroll ist tot und veraltet (ohne enrollment_token)

- **Betroffene WPs:** WP-038 · **Komponente:** frontend/src/api/client.ts:2521-2529 · **Quelle:** Audit (B05) + Verifikation
- **Reproduktion:** client.ts:2521-2529 sendet {agent_id, hostname, version, os, arch} ohne enrollment_token; grep findet keinen Aufrufer in pages/components; backend agent/handler.go:178-180 antwortet 400 'enrollment_token is required'.
- **Soll:** Die Client-API entspricht dem OpenAPI-Vertrag oder wird entfernt.
- **Ist:** Die veraltete Funktion würde, falls verdrahtet, immer mit 400 scheitern.
- **Nutzerwirkung:** Heute keine; Falle für künftige UI-Arbeit.
- **Ursache (belegt):** Die Vertragsänderung aus WP-038 wurde im handgeschriebenen Client nicht nachgezogen.
- **Empfehlung:** agentApi.enroll entfernen oder aus schema.d.ts inkl. enrollment_token typisieren.
- **Retest (verbindlich):** Typecheck gegen das generierte Schema; Vitest assertet enrollment_token im Body (oder Funktion entfernt).
- **Kodierender Test:** keiner

<a id="f-190"></a>
### F-190 (P3) – Tenant-Guard erkennt Pool-Zugriffe über lokalen Alias, Method-Value oder direkte pgx-Verbindung nicht

- **Betroffene WPs:** WP-041 · **Komponente:** backend/internal/database/tenantguard_test.go · **Quelle:** Audit (B05) + Verifikation
- **Reproduktion:** Getrackte Quellen in ein Scratch-Verzeichnis kopieren, backend/internal/zzprobe/probe.go mit 'db := r.pool; db.Exec(...)', 'exec := r.pool.Exec; exec(...)', 'conn, _ := pgx.Connect(ctx, dsn); conn.Exec(...)' und Kontrolle 'r.pool.Exec(...)' ergänzen; go test -run TestNoDatabaseAccessOutsideTenantContext ./internal/database/ meldet nur probe.go:28 (Kontrolle).
- **Soll:** Jeder DB-Zugriff außerhalb WithTenant/WithSystem und der Allowlist lässt den Test scheitern (WP-041 AK1).
- **Ist:** Aliasierte Pool-Variablen, Method-Values und direkt geöffnete pgx-Verbindungen passieren unbemerkt; aktuell existiert kein solcher Bypass im Code.
- **Nutzerwirkung:** Heute keine; eine künftige Änderung kann unbemerkt mandantenungebundenen DB-Zugriff einführen, ohne dass CI scheitert.
- **Ursache (belegt):** Receiver-Abgleich per Namen (lastName(sel.X) ~ /pool$/i) nur für CallExpr-Selektoren; Namensprüfung deckt ast.Field und typisierte ValueSpec, nicht ':=' oder Method-Values; pgx.Conn und database/sql unberücksichtigt.
- **Empfehlung:** Typbasierte go/analysis- bzw. go/types-Prüfung: jeden Methodenaufruf/Method-Value auf *pgxpool.Pool, *pgxpool.Conn, *pgx.Conn, *sql.DB sowie pgx.Connect, sql.Open, pgxpool.New außerhalb internal/database und Allowlist melden; Fixture-Selbsttest.
- **Retest (verbindlich):** Fixture-Test: Alias, Method-Value, direkter Aufruf, falsch benanntes Feld, eingebetteter Pool und pgx.Connect werden gemeldet, Tx-Nutzung in WithTenant und allowgelistete Dateien nicht.
- **Kodierender Test:** keiner (nur Scratch-Kopie-Probe).

<a id="f-191"></a>
### F-191 (P3) – WithSystem-Aufrufer sind entgegen E-08 und pool.go-Kommentar durch keine Freigabeliste geregelt

- **Betroffene WPs:** WP-041 · **Komponente:** backend/internal/database/tenantguard_allowlist.go; database/pool.go:289-290 · **Quelle:** Audit (B05) + Verifikation
- **Reproduktion:** grep -rn 'database.WithSystem(' backend/internal backend/cmd --include=*.go | grep -v _test -> 24 Aufrufstellen in 19 Dateien; keine davon in tenantguard_allowlist.go; pool.go:289-290 behauptet 'Every caller is listed in the allow-list of the architecture test (WP-041)'.
- **Soll:** E-08: 'Systempfade laufen über WithSystem mit kommentierter Freigabeliste (WP-041)'; mandantenübergreifende Systemleser sind mit Begründung gelistet und geprüft.
- **Ist:** Jedes Paket kann WithSystem ohne Review aufrufen; der Kommentar ist irreführend.
- **Nutzerwirkung:** Heute gering (WithSystem ist read-only, nur SELECT-Systemausnahmen werten app.system aus); Governance-Lücke für neue mandantenübergreifende Leser.
- **Ursache (belegt):** WP-041 implementierte die Allowlist nur für direkte Pool-Aufrufe.
- **Empfehlung:** Guard um eine kommentierte Allowlist je Datei für WithSystem-Aufrufe (mit Stale-Prüfung) erweitern oder E-08 und den pool.go-Kommentar formal korrigieren.
- **Retest (verbindlich):** Guard-Test, der WithSystem-Aufrufstellen aufzählt: nicht gelistete Datei, veralteter Eintrag und leere Begründung lassen den Test scheitern.
- **Kodierender Test:** keiner

<a id="f-192"></a>
### F-192 (P3) – reticora_owner (per SET ROLE aus reticora_app erreichbar) kann Mandantentabellen trotz FORCE RLS organisationsübergreifend per TRUNCATE leeren

- **Betroffene WPs:** WP-064 · **Komponente:** Migration 000078 (Ownership an reticora_owner); backend/internal/database/role_check.go (prüft TRUNCATE nicht) · **Quelle:** Audit (B08) + Verifikation
- **Reproduktion:** psql -f SP/work/audit-B08/truncate_probe.sql (eine Tx, Rollback): SET LOCAL ROLE reticora_app: TRUNCATE job -> 'permission denied'; SET LOCAL ROLE reticora_owner mit app.org_id = Org A: SELECT 1, DELETE 1, TRUNCATE job -> 'TRUNCATE TABLE'; danach 0 Zeilen beider Orgs.
- **Soll:** Der CH19-Owner-Pfad ist auf Index-DDL begrenzt; kein Recht auf dem SET-ROLE-Pfad umgeht die Mandantenisolation, und VerifyRoleContract würde es melden.
- **Ist:** Owner-TRUNCATE (implizites Owner-Recht) ignoriert RLS; weder Event-Trigger noch Rollenprüfung decken es ab.
- **Nutzerwirkung:** Nur mit beliebiger SQL-Ausführung auf einer App-Verbindung ausnutzbar: mandantenübergreifende Massenlöschung als Einzeiler (Defense-in-Depth).
- **Ursache (belegt):** Tabellenbesitz impliziert TRUNCATE; Event-Trigger feuern nicht auf TRUNCATE; roleContractQuery prüft es nicht.
- **Empfehlung:** Statement-Level BEFORE-TRUNCATE-Guard-Trigger auf Mandantentabellen, die für reticora_app/reticora_owner abbrechen; VerifyRoleContract meldet fehlende Guards.
- **Retest (verbindlich):** Integrationstest: als Owner nach SET ROLE TRUNCATE auf eine Mandantentabelle -> 42501, Zeilen beider Orgs bleiben; VerifyRoleContract meldet eine Tabelle ohne Guard.
- **Kodierender Test:** keiner (nur Probe truncate_probe.sql).

<a id="f-193"></a>
### F-193 (P3) – Down-Migration 000056 droppt die clusterweite Rolle reticora_app und scheitert, wenn andere Datenbanken des Clusters Grants an sie halten

- **Betroffene WPs:** WP-064 · **Komponente:** backend/migrations/000056_rls_enforcement.down.sql; backend/internal/database TestMigrationsRoundtrip · **Quelle:** Defekttest-Welle (neuer Defekt)
- **Reproduktion:** TestMigrationsRoundtrip (internal/database) auf audit_db_01 in einem Cluster mit mehreren Reticora-Datenbanken: 'apply 000056_rls_enforcement.down.sql: ERROR: role reticora_app cannot be dropped because some objects depend on it (SQLSTATE 2BP01)'; DB bleibt dirty auf Version 55, die Down-Migrationen hatten zudem Seed-Zeilen entfernt (permission 103->12, globale ci_types 23->6, Demo-Org).
- **Soll:** Down-Migrationen wirken nur auf die eigene Datenbank und sind in geteilten Clustern rückrollbar; clusterweite Rollen werden nicht gedroppt oder nur, wenn keine Abhängigkeiten bestehen.
- **Ist:** Rollback scheitert halb durchgeführt und hinterlässt eine dirty Datenbank; der Roundtrip-Test ist destruktiv.
- **Nutzerwirkung:** Betrieb: ein Rollback in einem PostgreSQL-Cluster mit mehreren Reticora-Datenbanken (z.B. Staging + Prod) bricht ab und hinterlässt einen inkonsistenten Stand; Testinfrastruktur: der Roundtrip-Test zerstört geteilte Test-DBs. Die Baseline-CI (eine DB) ist nicht betroffen.
- **Ursache (belegt):** 000056_rls_enforcement.down.sql entfernt die clusterweite Rolle reticora_app per DROP ROLE; Rollen sind in PostgreSQL clusterweit, Grants anderer Datenbanken blockieren den Drop.
- **Empfehlung:** DROP ROLE aus der Down-Migration entfernen (nur REVOKE in der eigenen DB) oder konditional ausführen; TestMigrationsRoundtrip auf eine dedizierte, temporäre Datenbank beschränken.
- **Retest (verbindlich):** TestMigrationsRoundtrip in einem Cluster mit einer zweiten Datenbank, die Grants an reticora_app hält: down -all und up ohne Fehler, keine dirty-Version, Seed-Zeilen nach up vollständig.
- **Kodierender Test:** keiner (Reproduktion über den bestehenden TestMigrationsRoundtrip im geteilten Cluster).

<a id="f-194"></a>
### F-194 (P3) – TST-02-Meta-Test ordnet die Zertifikatserneuerung einem Unit-Test zu und prüft nur Funktionsnamen

- **Betroffene WPs:** WP-208 · **Komponente:** backend/internal/server/e2e_scenarios_integration_test.go:149-156 (TestTST02ScenarioCoverage) · **Quelle:** Verifikation (B27)
- **Reproduktion:** e2e_scenarios_integration_test.go:156 ordnet 'certificate renewal' discovery/ca_test.go TestNeedsRenewal (Datumsarithmetik :28-39) zu; geprüft wird nur strings.Contains(src, 'func '+name+'(').
- **Soll:** Die Zuordnung zeigt auf TestCollectorPKI (PG, Renewal-Endpunkt :138-166), und die CI prüft, dass Zieltests als 'pass' und nicht als 'skip' laufen.
- **Ist:** Zuordnung auf einen Unit-Test; der Meta-Test bliebe grün, auch wenn der Zieltest übersprungen würde.
- **Nutzerwirkung:** Gering; die TST-02-Nachweiskette ist irreführend (die reale Erneuerung ist durch TestCollectorPKI belegt).
- **Ursache (belegt):** Falsche Zuordnung in der Szenarioliste.
- **Empfehlung:** Zuordnung auf TestCollectorPKI ändern; in der CI gegen das gotestsum-JSON prüfen, dass Zieltests 'pass' sind.
- **Retest (verbindlich):** cd backend && go test -count=1 -run '^TestTST02ScenarioCoverage$' ./internal/server/ mit korrigierter Zuordnung grün; Prüfung der Zieltests im gotestsum-JSON.
- **Kodierender Test:** keiner

<a id="f-195"></a>
### F-195 (P3) – Deaktivierung widerruft auch die vom Benutzer erzeugten Service-Account-API-Keys

- **Betroffene WPs:** WP-042 · **Komponente:** backend/internal/identity/apikey_store.go:262-271; apikey_handler.go:153-156 · **Quelle:** Verifikation
- **Reproduktion:** Admin A erzeugt Key für SA S; PATCH /users/A inactive → RevokeUserAPIKeys (organization_id, created_by) widerruft auch S-Keys (Code-Pfad).
- **Soll:** Dokumentierte Entscheidung (AUT-04/RBA-08: Key gehört Benutzer oder SA).
- **Ist:** Alle von A erzeugten Keys inkl. Integrations-Keys widerrufen.
- **Nutzerwirkung:** Integrationen fallen beim Ausscheiden des Erstellers unangekündigt aus.
- **Ursache (belegt):** Kein service_account_id-Filter.
- **Empfehlung:** Entscheiden und dokumentieren; ggf. SA-Keys ausnehmen und zur Rotation markieren.
- **Retest (verbindlich):** Neuer Integrationstest in internal/identity (User- und SA-Key, beide von A): entschiedenes Ergebnis je Key; go test -count=1 -run '<Test>' ./internal/identity/ PASS.
- **Kodierender Test:** keiner

<a id="f-196"></a>
### F-196 (P3) – Keine UI zum Deaktivieren, Reaktivieren oder Löschen von Benutzern

- **Betroffene WPs:** WP-042 · **Komponente:** frontend/src/pages/UserManagementPage.tsx:72-83 · **Quelle:** Audit, Verifikation
- **Reproduktion:** /users öffnen: nur Status-Badge, keine Aktionen.
- **Soll:** TLC-04-Aktionen im Produkt bedienbar.
- **Ist:** Nur per API.
- **Nutzerwirkung:** Admins brauchen API-Werkzeuge zum Sperren.
- **Ursache (belegt):** UI nicht implementiert (nicht in WP-042-Dateiliste).
- **Empfehlung:** Aktionen mit 409-Übergabemeldungen ergänzen oder UI-WP zuordnen.
- **Retest (verbindlich):** e2e-real (neu): Admin deaktiviert B in der UI, B erhält 401/Login, lokalisierte Meldung; npx playwright test -c e2e-real/playwright.real.config.ts PASS.
- **Kodierender Test:** keiner

<a id="f-197"></a>
### F-197 (P3) – Login-Abweisung zeigt rohen englischen Backend-Fehlertext

- **Betroffene WPs:** WP-043 · **Komponente:** frontend/src/pages/CallbackPage.tsx:23 · **Quelle:** Audit, Real-Stack-E2E (beobachtet)
- **Reproduktion:** Erstlogin ohne Einladung; Real-Stack roles-authz 10 zeigt 'identity: first login requires an invitation…'.
- **Soll:** Lokalisierte Meldung.
- **Ist:** Technischer Text mit 'identity:'-Präfix.
- **Nutzerwirkung:** Verwirrung bei abgewiesenen Nutzern.
- **Ursache (belegt):** problem.detail ungefiltert angezeigt.
- **Empfehlung:** 403-Problemtypen auf i18n-Schlüssel abbilden.
- **Retest (verbindlich):** Vitest CallbackPage (gemocktes 403) zeigt deutschen Text; roles-authz 10 auf deutschen Text verschärft PASS.
- **Kodierender Test:** keiner

<a id="f-198"></a>
### F-198 (P3) – Parallele Erstlogins in eine leere Organisation werden alle zugelassen

- **Betroffene WPs:** WP-043 · **Komponente:** backend/internal/user/pg_repository.go:1149-1156 · **Quelle:** Audit, Verifikation (reproduziert)
- **Reproduktion:** V/bootstrap_race_probe_test.go: 4 parallele EnsureUser auf leere Org → 3/1/4 app_user in 3 Läufen.
- **Soll:** Genau ein Bootstrap-Benutzer.
- **Ist:** count(*) ohne Sperre.
- **Nutzerwirkung:** Gering; erfordert admin-provisionierte KC-Konten.
- **Ursache (belegt):** Nicht serialisiertes Check-then-Insert.
- **Empfehlung:** Org-Zeile FOR UPDATE oder Advisory-Lock.
- **Retest (verbindlich):** Neuer Integrationstest: N parallele EnsureUser → genau 1 Erfolg, 20 Wiederholungen PASS.
- **Kodierender Test:** keiner

<a id="f-199"></a>
### F-199 (P3) – Permission credential:decrypt ist geseedet, steuert aber keine Route

- **Betroffene WPs:** WP-045 · **Komponente:** backend/internal/identity/types.go:120,229; Seed-Rollen · **Quelle:** Verifikation
- **Reproduktion:** route_permissions.txt ohne credential:decrypt; psql audit_db_06: engineer/org_admin halten den Schlüssel.
- **Soll:** Jeder Katalogschlüssel wirkt oder wird entfernt.
- **Ist:** Wirkungsloser Schlüssel.
- **Nutzerwirkung:** Rolleneditor suggeriert Entschlüsselungsrecht.
- **Ursache (belegt):** Route durch WP-047 entfernt, Schlüssel nicht.
- **Empfehlung:** Per Migration stilllegen oder als reserviert dokumentieren.
- **Retest (verbindlich):** TestRBA01KeysGateTheirActions erweitert (jeder Schlüssel genutzt/allow-listed): go test -count=1 -run TestRBA01KeysGateTheirActions ./internal/server/ PASS.
- **Kodierender Test:** keiner

<a id="f-200"></a>
### F-200 (P3) – Keycloak-Realm-Rollen erreichen das Backend nicht; README behauptet Mapping

- **Betroffene WPs:** WP-046 · **Komponente:** deploy/keycloak/realm-reticora.json (Mapper groups, organization_id); identity/oidc.go:268; deploy/keycloak/README.md:60-63 · **Quelle:** Audit, Verifikation
- **Reproduktion:** KC-Realm-Rolle 'engineer' zuweisen → keine Session-Wirkung.
- **Soll:** README/E-13: Gruppen und Rollen werden abgebildet.
- **Ist:** Nur Gruppennamen.
- **Nutzerwirkung:** KC-Rollenpflege wirkungslos.
- **Ursache (belegt):** Kein Realm-Rollen-Mapper/Parser.
- **Empfehlung:** Mapper ergänzen oder README korrigieren.
- **Retest (verbindlich):** Unit ValidateIDToken mit Rollen-Claim bzw. README-Fix; Real-Stack: Gruppe reticora-viewer → genau Viewer-Rechte.
- **Kodierender Test:** keiner

<a id="f-201"></a>
### F-201 (P3) – Rollen-Tab zeigt die vier RBA-02-Systemrollen als 'Custom' (API: is_system=false)

- **Betroffene WPs:** WP-046 · **Komponente:** backend/internal/user/pg_repository.go:594-596; frontend/src/pages/UserManagementPage.tsx:175-176 · **Quelle:** Real-Stack-E2E (confirmed)
- **Reproduktion:** /users → Rollen: org_admin, engineer, viewer, client_technician 'Custom'; roles-authz 09b FAIL.
- **Soll:** RBA-02 'Systemrollen'.
- **Ist:** is_builtin=true, is_system=false; UI wertet nur is_system.
- **Nutzerwirkung:** Admins falsch informiert.
- **Ursache (belegt):** 'false AS is_system' für Tabelle role.
- **Empfehlung:** is_system für Seed-Rollen true oder UI wertet is_builtin.
- **Retest (verbindlich):** cd frontend && npx playwright test -c e2e-real/playwright.real.config.ts roles-authz -g '09b' PASS.
- **Kodierender Test:** frontend/e2e-real/roles-authz.spec.ts:798 09b (FAIL)

<a id="f-202"></a>
### F-202 (P3) – Log-Redaktion deckt Map/Struct/key=value nicht ab; Webhook-URLs für Viewer sichtbar

- **Betroffene WPs:** WP-047 · **Komponente:** backend/internal/platform/httpx/redact.go:96-101 · **Quelle:** Audit, Verifikation
- **Reproduktion:** V/redact_probe_test.go: MAPLEAK, STRUCTLEAK, KVLEAK erscheinen im Log (V/redact-gaps.log); Viewer GET /webhooks liefert volle URL.
- **Soll:** SEC-01 robust.
- **Ist:** Nur secret-benannte Keys/URL-userinfo; keine aktuelle Aufrufstelle betroffen.
- **Nutzerwirkung:** Künftige Logaufrufe können Geheimnisse leaken.
- **Ursache (belegt):** Default-Zweig gibt KindAny unverändert zurück.
- **Empfehlung:** Maps/Structs und key=value redigieren; webhook:read für Viewer prüfen.
- **Retest (verbindlich):** Neue Unit-Tests platform/httpx (map, struct, key=value) PASS.
- **Kodierender Test:** keiner

<a id="f-203"></a>
### F-203 (P3) – IGA-Connector-Update speichert base_url ohne SSRF-/https-Validierung

- **Betroffene WPs:** WP-049 · **Komponente:** backend/internal/iga/handler.go:147-166 (Create validiert :137) · **Quelle:** Defekttest-Welle (neu)
- **Reproduktion:** PATCH /api/v1/iga/connectors/{id} mit https://169.254.169.254, 10.0.0.5, 127.0.0.1, [fd00::1], localhost, http:// → 200 (TestAuditT2IGAUpdateValidatesBaseURL FAILS_AS_EXPECTED).
- **Soll:** WP-049 Kriterium 2: Validierung auch beim Speichern.
- **Ist:** Ungeprüft gespeichert; Laufzeit blockiert weiter.
- **Nutzerwirkung:** Fehlkonfiguration erst beim Sync erkannt; kein SSRF.
- **Ursache (belegt):** UpdateConnector ruft registry.ValidateBaseURL nicht auf (Code gelesen).
- **Empfehlung:** ValidateBaseURL im Update-Pfad.
- **Retest (verbindlich):** cd backend && go test -tags auditdefect -count=1 -v -run '^TestAuditT2IGAUpdateValidatesBaseURL$' ./internal/iga/ PASS; TestT2IGAEgress* PASS.
- **Kodierender Test:** backend/internal/iga/t2_egress_auditdefect_test.go (FAILS_AS_EXPECTED)

<a id="f-204"></a>
### F-204 (P3) – Notification-Webhook-Kanal-URL beim Speichern nicht gegen Egress-Policy geprüft

- **Betroffene WPs:** WP-049 · **Komponente:** backend/internal/notify/channel.go:90-92 · **Quelle:** Audit, Verifikation
- **Reproduktion:** Kanal provider=webhook url=http://169.254.169.254/ wird gespeichert; Zustellung später blockiert (Code).
- **Soll:** Speichervalidierung für jede Webhook-URL.
- **Ist:** Nur Schema geprüft.
- **Nutzerwirkung:** Späte Fehlererkennung, kein SSRF.
- **Ursache (belegt):** Kein egress.ValidateURL.
- **Empfehlung:** Egress-Optionen in Kanalvalidierung injizieren.
- **Retest (verbindlich):** Neuer Unit-Test: private URL 400, public https 201 PASS.
- **Kodierender Test:** keiner

<a id="f-205"></a>
### F-205 (P3) – IPv6-Übergangspräfixe (6to4, Teredo, IPv4-kompatibel) mit internem IPv4 erlaubt

- **Betroffene WPs:** WP-049 · **Komponente:** backend/internal/platform/egress/egress.go:86 · **Quelle:** Verifikation
- **Reproduktion:** V/egress_probe_test.go: Allowed(2002:7f00:1::1), (2002:a9fe:a9fe::1), Teredo, (::127.0.0.1) = true.
- **Soll:** Eingebettetes IPv4 geprüft oder Präfixe gesperrt.
- **Ist:** Erlaubt.
- **Nutzerwirkung:** Nur mit Tunnel-Routing ausnutzbar.
- **Ursache (belegt):** Nur NAT64/IPv4-mapped entpackt.
- **Empfehlung:** 2002::/16, 2001::/32, ::/96 sperren.
- **Retest (verbindlich):** Unit: Allowed false für die vier Adressen PASS.
- **Kodierender Test:** keiner

<a id="f-206"></a>
### F-206 (P3) – Idempotenz-Replay liefert gespeicherte Fehler wörtlich (alte trace_id; 5xx 24 h festgeschrieben)

- **Betroffene WPs:** WP-050, WP-163 · **Komponente:** backend/internal/middleware/idempotency.go (executeAndStore, replayIdempotent) · **Quelle:** Verifikation (reproduziert)
- **Reproduktion:** vrun1 TestVB21IdempotentErrorReplay: Retry → 500, trace_id 1b37… ≠ X-Request-ID 4d5c…, handler_calls=1.
- **Soll:** trace_id = X-Request-ID; Retry nach 5xx erreicht Handler.
- **Ist:** Wörtlicher Replay.
- **Nutzerwirkung:** Verwirrende Korrelation; Retry nach transientem Fehler 24 h unmöglich.
- **Ursache (belegt):** Jede Antwort wird gespeichert.
- **Empfehlung:** 5xx nicht speichern; trace_id-Semantik dokumentieren.
- **Retest (verbindlich):** Neuer Middleware-Test: 5xx-Retry erreicht Handler (201), 4xx-Replay dokumentiert PASS.
- **Kodierender Test:** keiner

<a id="f-207"></a>
### F-207 (P3) – Kompiliertes Agent-Binary im Repository

- **Betroffene WPs:** WP-051 · **Komponente:** backend/agent · **Quelle:** Verifikation
- **Reproduktion:** git ls-files -s backend/agent → ELF; 911b45e: 9105196→25607072 Bytes.
- **Soll:** Keine Build-Artefakte; signierte Pakete (AGT-04).
- **Ist:** 25-MB-Binary versioniert.
- **Nutzerwirkung:** Repo-Ballast, Risiko veralteter Binaries.
- **Ursache (belegt):** Build-Output committet.
- **Empfehlung:** git rm --cached, .gitignore, CI-Build.
- **Retest (verbindlich):** git ls-files backend/agent leer.
- **Kodierender Test:** keiner

<a id="f-208"></a>
### F-208 (P3) – Operator-Aktion kann ohne operator_audit-Eintrag wirksam werden (Audit nicht atomar)

- **Betroffene WPs:** WP-070, WP-074 · **Komponente:** backend/internal/operator/auth.go:135-160; operator/entitlements.go:96-117 · **Quelle:** Verifikation (reproduziert), Audit
- **Reproduktion:** go test -count=1 -run 'TestEntitlementsAreWrittenByOperatorsOnly$' -v ./internal/operator/ → 'ERROR operator audit failed … action=entitlement.grant' bei 200 und wirksamem Grant.
- **Soll:** Änderung und Audit atomar.
- **Ist:** Audit nachgelagert, Fehler nur geloggt.
- **Nutzerwirkung:** Lücke im Audit-Trail bei Insert-Fehler.
- **Ursache (belegt):** Separate Transaktion, Fehler ignoriert.
- **Empfehlung:** RecordTx (audit.go:83) oder 500+Rollback.
- **Retest (verbindlich):** Neuer PG-Test mit fehlschlagendem Auditor: 5xx, Entitlement unverändert PASS.
- **Kodierender Test:** keiner

<a id="f-209"></a>
### F-209 (P3) – Entitlement-Grant akzeptiert beliebige Feature- und Quota-Schlüssel

- **Betroffene WPs:** WP-071 · **Komponente:** backend/internal/entitlement/entitlement.go:200-219 · **Quelle:** Verifikation
- **Reproduktion:** probe-v09-ent.log E3: Grant 'rack-view' mit 'max_cis_typo' → err=nil.
- **Soll:** Exakte ENT-02-Schlüssel, sonst 422.
- **Ist:** Still gespeichert.
- **Nutzerwirkung:** Tippfehler wirken wie erfolgreiche Lizenzänderung.
- **Ursache (belegt):** Keine Allowlist.
- **Empfehlung:** Validate gegen Phase1Features/Addons/Quotas.
- **Retest (verbindlich):** Neuer Unit-Test: unbekannter Schlüssel → ValidationError/422 PASS.
- **Kodierender Test:** keiner

<a id="f-210"></a>
### F-210 (P3) – Entfernen des DefaultPlan-Fallbacks ohne Backfill lässt Orgs mit Teilzeilen ohne Planfeatures

- **Betroffene WPs:** WP-072 · **Komponente:** backend/internal/entitlement/entitlement.go:368-385 · **Quelle:** Verifikation
- **Reproduktion:** probe-v09-ent.log E1: Org nur mit cmdb_core → provisioned=0, topology/export/ingest 403.
- **Soll:** Upgrade erhält effektive Features.
- **Ist:** Verlust nicht-core Features.
- **Nutzerwirkung:** Nur Altinstallationen (vermutlich keine).
- **Ursache (belegt):** Provision nur ohne Zeilen.
- **Empfehlung:** Fehlende Planfeatures nachprovisionieren.
- **Retest (verbindlich):** Neuer Integrationstest: Org mit cmdb_core → alle Planfeatures; deaktivierte bleiben PASS.
- **Kodierender Test:** keiner

<a id="f-211"></a>
### F-211 (P3) – org_admin behält den toten Schlüssel entitlement:manage

- **Betroffene WPs:** WP-074 · **Komponente:** backend/internal/permission/catalog.go:59; fehlende Migration _entitlement_manage_revoke · **Quelle:** Verifikation
- **Reproduktion:** psql audit_db_13: org_admin|entitlement:manage.
- **Soll:** Kein Schreibrecht-Schlüssel für org_admin.
- **Ist:** Schlüssel zugewiesen, keine Route.
- **Nutzerwirkung:** Irreführende Rollenanzeige; Risiko künftiger Routen.
- **Ursache (belegt):** Bewusst belassen (52924d7).
- **Empfehlung:** Entziehen oder Guard-Test.
- **Retest (verbindlich):** Rollenmatrix-/Routentest: keine Tenant-Route verlangt entitlement:manage PASS.
- **Kodierender Test:** keiner

<a id="f-212"></a>
### F-212 (P3) – Datenkorrektur 000086 behält implizit provisionierte Add-on-Zeilen

- **Betroffene WPs:** WP-075 · **Komponente:** migrations/000086_addon_entitlements.up.sql · **Quelle:** Verifikation
- **Reproduktion:** git show 3092583: Enterprise mit iga/ai; 000086 nur Umbenennung.
- **Soll:** Planabgeleitete Add-on-Zeilen entfernt.
- **Ist:** Beibehalten.
- **Nutzerwirkung:** Nur Zwischenbuild-Installationen.
- **Ursache (belegt):** Nur Rename.
- **Empfehlung:** Bereinigen oder Release-Note.
- **Retest (verbindlich):** Migrationstest: planabgeleitete Zeilen entfernt, Billing-Zeile 'ai' PASS.
- **Kodierender Test:** keiner

<a id="f-213"></a>
### F-213 (P3) – OpenAPI deklariert application/json für eine Fehlerantwort (forms submissions 400)

- **Betroffene WPs:** WP-163 · **Komponente:** api/openapi.yaml:9133 · **Quelle:** Audit, Verifikation
- **Reproduktion:** YAML-Scan: 1352 Fehlerantworten, nur diese und /readyz nicht problem+json.
- **Soll:** API-03: problem+json.
- **Ist:** application/json dokumentiert.
- **Nutzerwirkung:** Generierte Clients.
- **Ursache (belegt):** Spec nicht angepasst.
- **Empfehlung:** Korrigieren + Spec-Test.
- **Retest (verbindlich):** Spec-Test: 0 Nicht-problem-Fehler außer /readyz PASS.
- **Kodierender Test:** keiner

<a id="f-214"></a>
### F-214 (P3) – Leere IPAM-Listen liefern "data": null

- **Betroffene WPs:** WP-164 · **Komponente:** backend/internal/ipam/pg_repository.go:252 · **Quelle:** Verifikation (Real-Stack)
- **Reproduktion:** Real-Stack: GET /ips?limit=1 und /ip-addresses?limit=1 ohne IPs → data:null (headers.log A/B).
- **Soll:** data als Array (openapi.yaml:17694-17703).
- **Ist:** null.
- **Nutzerwirkung:** Strikte Clients scheitern.
- **Ursache (belegt):** nil-Slice, keine Normalisierung.
- **Empfehlung:** nil zentral auf [] normalisieren.
- **Retest (verbindlich):** Runtime-Listentest: leere Org → data == [] PASS.
- **Kodierender Test:** keiner

<a id="f-215"></a>
### F-215 (P3) – CompleteList-Endpunkte ignorieren limit/max 200

- **Betroffene WPs:** WP-164 · **Komponente:** identity/apikey_handler.go:181; user/invitation_handler.go:86; user/service_account_handler.go:54; citype/handler.go:110; operator/handler.go:161 · **Quelle:** Audit (Code)
- **Reproduktion:** Nur Code; nicht mit >200 Zeilen ausgeführt.
- **Soll:** limit ≤ 200.
- **Ist:** Vermutlich alle Zeilen.
- **Nutzerwirkung:** Große Antworten.
- **Ursache (Hypothese):** CompleteList-Muster (nicht reproduziert).
- **Empfehlung:** Paginieren oder dokumentiert ausnehmen.
- **Retest (verbindlich):** 250 API-Keys seeden; GET /api-keys ≤200 + next_cursor PASS.
- **Kodierender Test:** keiner

<a id="f-216"></a>
### F-216 (P3) – Searchability/Typ von Attributfiltern org-weit pro Key aufgelöst

- **Betroffene WPs:** WP-165 · **Komponente:** backend/internal/ci/pg_repository.go:155-171,210-230 · **Quelle:** Audit, Verifikation
- **Reproduktion:** vrun2 TestVB21FilterNonSearchableViaCollision: nicht-searchable Wert per exact/like gefunden; run3: gültiger gte-Filter 400.
- **Soll:** API-02: nur searchable Attribute, typisiert.
- **Ist:** Key-Kollision umgeht searchable.
- **Nutzerwirkung:** Falsche/fehlschlagende Filter.
- **Ursache (belegt):** bool_or(searchable) über alle Typen; Prädikat ohne Typbezug.
- **Empfehlung:** Auf CI-Typen einschränken oder mehrdeutige Keys 400.
- **Retest (verbindlich):** Neuer PG-Kollisionstest PASS.
- **Kodierender Test:** keiner

<a id="f-217"></a>
### F-217 (P3) – X-RateLimit-Header und Retry-After fehlen bei vor dem Limiter abgewiesenen Antworten

- **Betroffene WPs:** WP-166 · **Komponente:** backend/internal/middleware/ratelimit.go:279-283; cmd/server/main.go:667-676 · **Quelle:** Audit, Verifikation
- **Reproduktion:** vrun1 TestVB21PreAuth429Headers (429 ohne Header); Real-Stack 401 ohne X-RateLimit-*.
- **Soll:** WP-166: Header auf allen Antworten.
- **Ist:** Nur nach dem Limiter.
- **Nutzerwirkung:** Clients ohne Backoff-Info.
- **Ursache (belegt):** Kettenposition; Pre-Auth schreibt keine Header.
- **Empfehlung:** Kriterium klären, Pre-Auth-429 mit Retry-After.
- **Retest (verbindlich):** Neuer Kettentest: dokumentierte Header auf 401/403/Pre-Auth-429 PASS.
- **Kodierender Test:** keiner

<a id="f-218"></a>
### F-218 (P3) – Deprecation-Header still deaktiviert, wenn die Spec-Datei fehlt

- **Betroffene WPs:** WP-167, WP-168 · **Komponente:** backend/internal/middleware/openapi.go:136-150,364-386 · **Quelle:** Audit, Verifikation (Real-Stack)
- **Reproduktion:** Nativer Real-Stack ohne RETICORA_OPENAPI_SPEC_PATH: /ip-addresses, /audit, /auth/me ohne Deprecation; kein Log.
- **Soll:** Header vorhanden oder sichtbare Warnung.
- **Ist:** Stiller Verlust.
- **Nutzerwirkung:** Fehlkonfigurierte Bereitstellung ohne Hinweis (Image liefert Spec).
- **Ursache (belegt):** Kein Fehlerpfad, relativer Default.
- **Empfehlung:** go:embed oder Fehler/Readiness.
- **Retest (verbindlich):** Neuer Unit-Test (Warnung/eingebettet); Real-Stack mit Spec zeigt Header PASS.
- **Kodierender Test:** keiner

<a id="f-219"></a>
### F-219 (P3) – Successor-Link mit unexpandierten URI-Templates

- **Betroffene WPs:** WP-167 · **Komponente:** backend/internal/middleware/openapi.go:226-228 · **Quelle:** Audit
- **Reproduktion:** run4.log: Link </api/v1/impact/{ci_id}>.
- **Soll:** RFC 8288 URI.
- **Ist:** Template wörtlich.
- **Nutzerwirkung:** Link führt zu 4xx.
- **Ursache (belegt):** x-successor unverändert.
- **Empfehlung:** Pfadparameter einsetzen.
- **Retest (verbindlich):** Unit: /x/42 → Link </y/42> PASS.
- **Kodierender Test:** keiner

<a id="f-220"></a>
### F-220 (P3) – Keine Lifecycle-Header auf 401/403 deprecated Operationen

- **Betroffene WPs:** WP-167 · **Komponente:** backend/internal/server/router.go:203,316 · **Quelle:** Verifikation
- **Reproduktion:** vrun2: /ip-addresses ohne Recht → 403 ohne Deprecation/Sunset.
- **Soll:** Hinweis unabhängig vom Ergebnis.
- **Ist:** Nur nach Authz.
- **Nutzerwirkung:** Gering.
- **Ursache (belegt):** writeDeprecation in Post-Authz-Middleware.
- **Empfehlung:** Frühe Middleware oder dokumentieren.
- **Retest (verbindlich):** Kettentest 401/403 mit Header PASS.
- **Kodierender Test:** keiner

<a id="f-221"></a>
### F-221 (P3) – Restore-Fenster fest 90 Tage statt konfigurierter Retention

- **Betroffene WPs:** WP-169 · **Komponente:** backend/internal/ci/service.go:96-98,367-373; config.go:267,313 · **Quelle:** Audit, Verifikation (reproduziert)
- **Reproduktion:** vrun2 (c): Löschung vor 100 d, Retention 180 → Restore 410, CI existiert noch.
- **Soll:** CI-05: Restore innerhalb der Retention.
- **Ist:** Ab Tag 90 abgelehnt.
- **Nutzerwirkung:** Längere Retention nicht für Restore nutzbar.
- **Ursache (belegt):** Konstante RestoreRetention.
- **Empfehlung:** Konfigurierte Retention injizieren.
- **Retest (verbindlich):** Neuer Test: Retention 180, Tag 100 → 200, Tag 181 → 410/404 PASS.
- **Kodierender Test:** keiner

<a id="f-222"></a>
### F-222 (P3) – Restore-Konflikt 409 nicht in OpenAPI dokumentiert

- **Betroffene WPs:** WP-169 · **Komponente:** api/openapi.yaml:805-833 · **Quelle:** Verifikation
- **Reproduktion:** vrun2 (b): Seriennummernkonflikt → 409; Spec nur 200/401/403/404/410.
- **Soll:** API-01: alle Antworten dokumentiert.
- **Ist:** 409 fehlt.
- **Nutzerwirkung:** Clients erwarten Konflikt nicht.
- **Ursache (belegt):** Spec nicht ergänzt.
- **Empfehlung:** 409 ergänzen.
- **Retest (verbindlich):** Spec enthält 409; Response-Validierungstest PASS.
- **Kodierender Test:** keiner

<a id="f-223"></a>
### F-223 (P3) – include kappt eingebettete Sammlungen still bei 200

- **Betroffene WPs:** WP-169 · **Komponente:** backend/internal/server/router.go:648-662 · **Quelle:** Audit (Code)
- **Reproduktion:** Nur Code; nicht mit >200 Beziehungen ausgeführt.
- **Soll:** Vollständig oder markiert.
- **Ist:** Vermutlich stille Kappung.
- **Nutzerwirkung:** Unvollständige Daten unerkannt.
- **Ursache (Hypothese):** Fester includeLimit (nicht reproduziert).
- **Empfehlung:** truncated-Flag oder Doku.
- **Retest (verbindlich):** 201 Beziehungen seeden; include signalisiert Kappung PASS.
- **Kodierender Test:** keiner

<a id="f-224"></a>
### F-224 (P3) – 000056-down scheitert auf Altinstallationen ohne migration_quarantine; backup-dr.md:209 verspricht Rollback

- **Betroffene WPs:** WP-052 · **Komponente:** backend/migrations/000056_rls_enforcement.down.sql:14-21; docs/backup-dr.md:209 · **Quelle:** Audit/Verifikation
- **Reproduktion:** psql -d audit_db_07 -f SP/work/verify-B07/probe-052-down-legacy.sql → ERROR relation "migration_quarantine" does not exist.
- **Soll:** down läuft auch auf Altinstallationen.
- **Ist:** Abbruch.
- **Nutzerwirkung:** Rollback scheitert mit dirty-Status.
- **Ursache (belegt):** Kein to_regclass-Guard.
- **Empfehlung:** DO-Block mit Guard; Doku korrigieren.
- **Retest (verbindlich):** Probe läuft fehlerfrei; Migrationstest ohne Tabelle.
- **Kodierender Test:** keiner

<a id="f-225"></a>
### F-225 (P3) – Standort-Löschen im Baum ohne Bestätigung; 409 ohne Abhängigkeitsarten

- **Betroffene WPs:** WP-053 · **Komponente:** LocationTreePage.tsx:103,248-253; client.ts:274-289 · **Quelle:** Audit/Verifikation
- **Reproduktion:** L04: Klick sendet DELETE sofort; der Alert zeigt nur error.message.
- **Soll:** Bestätigung vor dem Löschen; Meldung nennt die Arten (LOC-11).
- **Ist:** Keine Bestätigung, keine Arten.
- **Nutzerwirkung:** Versehentliches Löschen möglich; Meldung nicht handlungsleitend.
- **Ursache (belegt):** Kein Confirm-Schritt; ApiError verwirft dependencies.
- **Empfehlung:** Bestätigungsdialog; dependencies parsen.
- **Retest (verbindlich):** Vitest: Dialog, Abbrechen sendet nichts, 409-Labels werden angezeigt.
- **Kodierender Test:** keiner

<a id="f-226"></a>
### F-226 (P3) – History-Endpunkt akzeptiert entfernten Typ location_node und weist location ab

- **Betroffene WPs:** WP-053 · **Komponente:** backend/internal/history/handler.go:176-182 · **Quelle:** Verifikation
- **Reproduktion:** GET /history/location/{id} → 400; /history/location_node/{id} → 200 leer (probe-hist.log).
- **Soll:** Historie eines Standorts lesbar.
- **Ist:** Veraltete Whitelist.
- **Nutzerwirkung:** Umzugshistorie nur im Audit-Log verfügbar.
- **Ursache (belegt):** Whitelist nicht nachgezogen.
- **Empfehlung:** 'location' zulassen, mit location_change hinterlegen (zusammen mit L-T3-20).
- **Retest (verbindlich):** API-Test: GET /history/location/{id} → 200 mit Umzug; location_node → 400.
- **Kodierender Test:** keiner

<a id="f-227"></a>
### F-227 (P3) – location_change-Historie wird mit dem Standort gelöscht (ON DELETE CASCADE)

- **Betroffene WPs:** WP-055 · **Komponente:** backend/migrations/000075_location_delete_rules.up.sql:31 · **Quelle:** Verifikation
- **Reproduktion:** Umzug → 1 Zeile; DELETE /sites → 0 Zeilen (Probe B0/B1).
- **Soll:** Historie überlebt die Löschung.
- **Ist:** Wird kaskadiert gelöscht.
- **Nutzerwirkung:** Umzugshistorie bleibt nur im Audit-Log.
- **Ursache (belegt):** FK mit CASCADE.
- **Empfehlung:** SET NULL plus Snapshot.
- **Retest (verbindlich):** Verschieben, löschen, die Zeile ist noch vorhanden.
- **Kodierender Test:** keiner

<a id="f-228"></a>
### F-228 (P3) – Bearbeiten-Dialog sendet alle Felder: veralteter Editor erhält 409 trotz Änderung nur eines unberührten Felds

- **Betroffene WPs:** WP-057, WP-061 · **Komponente:** frontend/src/pages/CIFormModal.tsx:156-164 · **Quelle:** Real-Stack-E2E (owned, Verdikt unverified) + Verifikation; WP-057-Verifikation P2, WP-061-Verifikation P3 – P3 übernommen
- **Reproduktion:** Real-Stack L12b: Sitzung 2 ändert Modell; Sitzung 1 ändert nur Hersteller → 409 fields=[model], Hersteller nicht gespeichert.
- **Soll:** API-07/CI-04: feldgranularer Merge.
- **Ist:** Konflikt; kein Datenverlust.
- **Nutzerwirkung:** Unnötige Konflikte und Nacharbeit.
- **Ursache (belegt):** Payload enthält alle Formularwerte statt eines Diffs; Backend korrekt (L13).
- **Empfehlung:** Nur geänderte Felder senden, inkl. geleerter (L-T3-08).
- **Retest (verbindlich):** Real-Stack L12b grün; Vitest: Body enthält nur die geänderten Felder.
- **Kodierender Test:** frontend/e2e-real/ci-lifecycle.spec.ts L12b

<a id="f-229"></a>
### F-229 (P3) – CI-Update-Handler bildet alle unerwarteten Fehler auf 404 'CI not found' ab

- **Betroffene WPs:** WP-057 · **Komponente:** backend/internal/ci/handler.go:507 · **Quelle:** Verifikation
- **Reproduktion:** Repo-Fehler 'connection reset' → PATCH 404 (probe-404.log).
- **Soll:** 500 bei internen Fehlern.
- **Ist:** 404.
- **Nutzerwirkung:** Irreführende Diagnose.
- **Ursache (belegt):** Pauschales Fallback-Mapping.
- **Empfehlung:** Sentinel ErrNotFound, sonst 500.
- **Retest (verbindlich):** Unit-Test: Stub-Fehler → 500.
- **Kodierender Test:** keiner

<a id="f-230"></a>
### F-230 (P3) – source-policy-API liefert Altreihenfolge im Widerspruch zu REC-03

- **Betroffene WPs:** WP-058 · **Komponente:** backend/internal/override/model.go:65-70 · **Quelle:** Verifikation
- **Reproduktion:** GET /api/v1/reconciliation/source-policy: manual und import stehen hinter sweep (probe-policy.log).
- **Soll:** REC-03-Reihenfolge.
- **Ist:** Altliste, die keine Wirkung hat.
- **Nutzerwirkung:** Falsches Bild der Quellprioritäten.
- **Ursache (belegt):** Rest von sourceTrust.
- **Empfehlung:** Aus SourceRanks ableiten oder deprecaten.
- **Retest (verbindlich):** Unit-Test: DefaultPriorities entspricht SourceRanks absteigend.
- **Kodierender Test:** keiner

<a id="f-231"></a>
### F-231 (P3) – Override-Konflikte aus Workflow und Agent erzeugen kein override_conflict-Review (REC-12)

- **Betroffene WPs:** WP-059, WP-089 · **Komponente:** workflow/executor.go:333-337; agent/handler.go:512-514 · **Quelle:** Verifikation
- **Reproduktion:** PG-Probes: reason=override_conflict, review_item 0.
- **Soll:** REC-12: jede Quelle erzeugt bei Konflikt ein Review.
- **Ist:** Nur _observed wird aktualisiert.
- **Nutzerwirkung:** Werte aus Workflow oder Agent sind nicht über die Inbox übernehmbar.
- **Ursache (belegt):** Konfliktzweig nur in Discovery implementiert.
- **Empfehlung:** OpenUnlessDismissed nutzen oder REC-12 formell auf Discovery begrenzen.
- **Retest (verbindlich):** PG-Tests: genau ein offenes Review je Feld, dedupliziert.
- **Kodierender Test:** keiner

<a id="f-232"></a>
### F-232 (P3) – DELETE /cis/{id}/fields/{name}/override bildet jeden Fehler auf 404 ab

- **Betroffene WPs:** WP-060 · **Komponente:** backend/internal/override/handler.go:118-121 · **Quelle:** Verifikation
- **Reproduktion:** Repo-Fehler 'connection refused' → 404 'field value not found' (ovrprobe).
- **Soll:** 5xx bei internen Fehlern.
- **Ist:** 404.
- **Nutzerwirkung:** Retries und Alerting werden fehlgeleitet.
- **Ursache (belegt):** Pauschales Mapping.
- **Empfehlung:** Sentinel plus WriteRepoError.
- **Retest (verbindlich):** Handler-Test: fehlerhaftes Repo → 500.
- **Kodierender Test:** keiner

<a id="f-233"></a>
### F-233 (P3) – Gespoolte Agent-Telemetrie bei 4xx/unlesbar ohne Verlustzähler verworfen

- **Betroffene WPs:** WP-062 · **Komponente:** collector/collectorcmd/collectorcmd.go:868-880 · **Quelle:** Verifikation
- **Reproduktion:** Replay mit 403 → Spool leer, dropped_messages=0 (colprobe).
- **Soll:** Jeder Verlust wird gezählt.
- **Ist:** Nur ein Log-Eintrag.
- **Nutzerwirkung:** Unsichtbarer Telemetrieverlust.
- **Ursache (belegt):** replayAgentTelemetry gibt nil zurück, recordLoss wird nie aufgerufen.
- **Empfehlung:** Vor dem Ack recordLoss aufrufen.
- **Retest (verbindlich):** Collector-Test: 403 → dropped_messages=1.
- **Kodierender Test:** keiner

<a id="f-234"></a>
### F-234 (P3) – Server protokolliert Spool-Verlustmeldungen nur; Metrik-Samples/Traps ungezählt verworfen

- **Betroffene WPs:** WP-062 · **Komponente:** discovery.go:954-962; edge_ingest.go:121-124; collectorcmd.go:1001-1015,1051-1054 · **Quelle:** Audit/Verifikation
- **Reproduktion:** Code: nur slog.Warn; Samples werden bei Upload-Fehler verworfen.
- **Soll:** Verluste serverseitig sichtbar (NFR-04-Umfang für Samples UNKLAR).
- **Ist:** Nur im Log.
- **Nutzerwirkung:** Verluste nur per Logsuche auffindbar.
- **Ursache (belegt):** Keine Persistenz und keine Metrik.
- **Empfehlung:** Metrik reticora_collector_spool_dropped_total bzw. Collector-Status; Umfang entscheiden.
- **Retest (verbindlich):** Server-Test: dropped_messages>0 ist über API oder Metrik sichtbar.
- **Kodierender Test:** keiner

<a id="f-235"></a>
### F-235 (P3) – Wiederholtes „Kunden benachrichtigen“ erzeugt doppelte pending-Benachrichtigungen

- **Betroffene WPs:** WP-063 · **Komponente:** backend/internal/maintenance/pg_repository.go:240-252 · **Quelle:** Verifikation
- **Reproduktion:** Zweimal notify → 4 Zeilen für 2 Clients (maintprobe).
- **Soll:** Idempotent je Fenster, Client und Kanal.
- **Ist:** N Zeilen pro Klick.
- **Nutzerwirkung:** Kunden würden doppelt benachrichtigt.
- **Ursache (belegt):** Kein Unique-Index, kein ON CONFLICT.
- **Empfehlung:** Unique-Index plus ON CONFLICT DO NOTHING.
- **Retest (verbindlich):** PG-Test: zweimal notify → eine Zeile je Client.
- **Kodierender Test:** keiner

<a id="f-236"></a>
### F-236 (P3) – Wartungs-UI kann keine CIs zuordnen und zeigt den gespeicherten Benachrichtigungsstatus nie

- **Betroffene WPs:** WP-063 · **Komponente:** frontend/src/pages/MaintenancePage.tsx:23-29 · **Quelle:** Verifikation
- **Reproduktion:** Vitest-Probe: Formular nur Titel/Beginn/Ende, POST ohne ci_ids; GET …/notifications ungenutzt.
- **Soll:** UI zeigt den echten, gespeicherten Status.
- **Ist:** Status nur transient; queued aus der UI unerreichbar.
- **Nutzerwirkung:** Ausstehende Benachrichtigungen später nicht prüfbar.
- **Ursache (belegt):** Formular und Seite unvollständig.
- **Empfehlung:** CI-Auswahl und Notifications-Liste ergänzen oder P3-Zurückstellung (E-03) dokumentieren.
- **Retest (verbindlich):** Vitest: ci_ids im POST, Statusliste nach Reload.
- **Kodierender Test:** keiner

<a id="f-237"></a>
### F-237 (P3) – CI-Detailseite zeigt Standort/Site/Raum als rohe UUIDs und Zeitstempel unformatiert

- **Betroffene WPs:** WP-077, WP-171 · **Komponente:** frontend/src/pages/CIDetailPage.tsx:146-149,174,259-266; CIDetailSections.tsx:599-602 · **Quelle:** Audit/Verifikation
- **Reproduktion:** Detailseite eines CI mit Standort: UUIDs und ISO-Strings; der Vitest prüft sogar 'site-1' roh.
- **Soll:** CI-06/DB-05: lesbarer Standort und formatierte Zeit.
- **Ist:** Rohwerte.
- **Nutzerwirkung:** Standortangabe für Operatoren unbrauchbar.
- **Ursache (belegt):** Keine Namensauflösung, keine Formatierung.
- **Empfehlung:** Namen auflösen, Daten formatieren.
- **Retest (verbindlich):** Vitest mit Namen und formatiertem Datum; e2e-real mit Site-Namen.
- **Kodierender Test:** keiner

<a id="f-238"></a>
### F-238 (P3) – Asset-Karte ohne RFID/Kaufdatum/Rechnungsnummer; Seriennummer bei verknüpftem CI editierbar (422 erst nach Speichern)

- **Betroffene WPs:** WP-077 · **Komponente:** CIDetailPage.tsx:170-178; CIFormModal.tsx:150,161,228 · **Quelle:** Audit/Verifikation
- **Reproduktion:** Die API liefert die Felder (Real-Binary), die Karte zeigt sie nicht; Seriennummer ist editierbar, Speichern → 422.
- **Soll:** Asset-Daten read-only und vollständig; keine Bearbeitung asset-eigener Felder.
- **Ist:** Unvollständig und editierbar.
- **Nutzerwirkung:** Fehlende Inventardaten, verwirrendes 422.
- **Ursache (belegt):** Minimale Karte; Formular berücksichtigt asset_id nicht.
- **Empfehlung:** Felder rendern, Inputs bei asset_id deaktivieren.
- **Retest (verbindlich):** Vitest für Karte und Formular.
- **Kodierender Test:** keiner

<a id="f-239"></a>
### F-239 (P3) – Verknüpfen mit standortlosem Asset löscht still Standort, Site und Raum des CI

- **Betroffene WPs:** WP-077 · **Komponente:** backend/migrations/000088:65; ci/service.go:297-363 · **Quelle:** Verifikation
- **Reproduktion:** Overlay: CI in room1 mit Asset ohne Standort verknüpft → location/site/room leer.
- **Soll:** Kein stiller Verlust (Asset übernimmt den Standort, oder 422).
- **Ist:** NULL.
- **Nutzerwirkung:** Standort- und Scope-Verlust, RLS-Sichtbarkeit ändert sich.
- **Ursache (belegt):** Trigger ohne den Schutz des Backfills.
- **Empfehlung:** Asset-Standort vom CI übernehmen oder 422.
- **Retest (verbindlich):** PG-Test: CI behält Standort, Site und Raum.
- **Kodierender Test:** keiner

<a id="f-240"></a>
### F-240 (P3) – Standortänderung von Geschwister-CIs eines gemeinsamen Assets ohne ci_change/Audit

- **Betroffene WPs:** WP-078 · **Komponente:** backend/internal/rack/placement.go:77-108; 000088:71-81 · **Quelle:** Verifikation
- **Reproduktion:** Mount X → Y (gleiches Asset) wechselt ins Rack, Y hat 0 ci_change und 0 Audit (Overlay).
- **Soll:** RCK-03: jede Standortänderung historisiert.
- **Ist:** Nur für das platzierte CI.
- **Nutzerwirkung:** Unvollständige Historie bei n:1-Assets.
- **Ursache (belegt):** Propagation per DB-Trigger ohne Historie.
- **Empfehlung:** Historie für alle betroffenen CIs schreiben.
- **Retest (verbindlich):** PG-Test: Historie für X und Y vorhanden.
- **Kodierender Test:** keiner

<a id="f-241"></a>
### F-241 (P3) – Rack-Platzierung ändert ci.location_id ohne Versionserhöhung (ETag)

- **Betroffene WPs:** WP-078 · **Komponente:** backend/internal/rack/placement.go:56-60 · **Quelle:** Verifikation
- **Reproduktion:** Version vor und nach dem Mount = 1 (Overlay).
- **Soll:** Version steigt, ci_change mit neuer Version.
- **Ist:** Keine Erhöhung.
- **Nutzerwirkung:** Veraltete ETags bleiben gültig.
- **Ursache (belegt):** Direktes SQL ohne version+1.
- **Empfehlung:** Version erhöhen.
- **Retest (verbindlich):** PG-Test: Version danach größer als vorher.
- **Kodierender Test:** keiner

<a id="f-242"></a>
### F-242 (P3) – Beziehungsformular bietet strukturelle Typen mounted_in/located_in an (immer 422)

- **Betroffene WPs:** WP-079, WP-127 · **Komponente:** frontend CIRelationshipsCard.tsx:34-47 · **Quelle:** Audit/Verifikation; zusammengeführt mit L-T6-epic-b-40 (Verifikation (B16))
- **Reproduktion:** mounted_in wählen und speichern → 422.
- **Soll:** Nur speicherbare Typen anbieten.
- **Ist:** Tote Optionen.
- **Nutzerwirkung:** Garantierte Fehler ohne Hinweis.
- **Ursache (belegt):** Optionsliste nicht angepasst.
- **Empfehlung:** Strukturelle Typen entfernen.
- **Retest (verbindlich):** Vitest der Typoptionen.
- **Kodierender Test:** keiner
- **Prüfvermerk:** Gruppenübergreifend dedupliziert: L-T6-epic-b-40 („Beziehungsformular bietet nicht speicherbare Strukturtypen und ignoriert den Typkatalog“) ist dieselbe Ursache und hier zusammengeführt.

<a id="f-243"></a>
### F-243 (P3) – Migration 000089 übernimmt zyklische Alt-contains-Zeilen als parent_ci_id-Zyklus

- **Betroffene WPs:** WP-079 · **Komponente:** backend/migrations/000089_ci_parent_projection.up.sql:20-31,68 · **Quelle:** Audit/Verifikation
- **Reproduktion:** probe_mig89_cycle.sql: A und B werden gegenseitig Parent.
- **Soll:** AST-05: keine Zyklen.
- **Ist:** Zyklus wird gespeichert.
- **Nutzerwirkung:** Selten; zyklische Parent-Kette.
- **Ursache (belegt):** UPDATE läuft vor CREATE TRIGGER.
- **Empfehlung:** Trigger zuerst anlegen oder Zeilen quarantänisieren.
- **Retest (verbindlich):** Migrationstest mit 2-Zyklus.
- **Kodierender Test:** keiner

<a id="f-244"></a>
### F-244 (P3) – Saved-View-Beziehungsfilter sehen projiziertes Enthaltensein nicht

- **Betroffene WPs:** WP-079 · **Komponente:** backend/internal/savedview/filter.go:116-121 · **Quelle:** Verifikation
- **Reproduktion:** Probe: Projektion hat 1 Kante, Filter findet 0.
- **Soll:** Filter nutzen die Projektion.
- **Ist:** Nur ci_relationship.
- **Nutzerwirkung:** Leere bzw. falsche Saved Views.
- **Ursache (belegt):** Filter nicht angepasst.
- **Empfehlung:** UNION mit ci_structure_edge.
- **Retest (verbindlich):** PG-Test has/lacks_relationship=contains.
- **Kodierender Test:** keiner

<a id="f-245"></a>
### F-245 (P3) – last_seen_at/first_seen_at aus Ankunftszeit statt observed_at (GLO-12-Rest)

- **Betroffene WPs:** WP-084 · **Komponente:** backend/internal/discovery/discovery.go:1291,1415; unlicensed.go:118-119 · **Quelle:** Verifikation
- **Reproduktion:** Record mit observed_at vor 20 h → last_seen_at age=0s, Status active (discovery.log).
- **Soll:** GLO-12: Zeitbezug ist observed_at.
- **Ist:** Serverzeit.
- **Nutzerwirkung:** Nach Ausfällen wirken Geräte frisch gesehen; Offline-Erkennung verzögert.
- **Ursache (belegt):** time.Now() statt observed_at.
- **Empfehlung:** GREATEST(last_seen_at, observed_at).
- **Retest (verbindlich):** PG-Test mit gespooltem Record.
- **Kodierender Test:** keiner

<a id="f-246"></a>
### F-246 (P3) – Seriennummer: ResolveIdentity case-insensitiv, PG-Kandidatensuche case-sensitiv

- **Betroffene WPs:** WP-086 · **Komponente:** backend/internal/discovery/pg_repository.go:920 · **Quelle:** Audit/Verifikation
- **Reproduktion:** CZJ9999E5 und czj9999e5 von verschiedenen IPs → 2 CIs; bei gleicher IP spurious conflicting_values.
- **Soll:** Konsistentes Matching.
- **Ist:** Duplikat bzw. unnötiges Review.
- **Nutzerwirkung:** Duplikate oder Review-Rauschen.
- **Ursache (belegt):** Seriennummer nicht normalisiert.
- **Empfehlung:** Normalisieren plus Index, oder einheitliche Entscheidung.
- **Retest (verbindlich):** PG-Test: 'AB-1' vs. 'ab-1' → Updated=1.
- **Kodierender Test:** keiner

<a id="f-247"></a>
### F-247 (P3) – REC-08-Eskalation (>14 Tage) wird im Dashboard nicht angezeigt

- **Betroffene WPs:** WP-088 · **Komponente:** frontend/src/pages/DashboardPage.tsx · **Quelle:** Audit/Verifikation
- **Reproduktion:** grep findet in DashboardPage nichts zu review/escalat.
- **Soll:** REC-08: Eskalation im Dashboard (abdeckung.csv:223 ordnet sie WP-088 zu).
- **Ist:** Nur in der Inbox.
- **Nutzerwirkung:** Alte Reviews fallen nicht auf.
- **Ursache (belegt):** Kein Widget.
- **Empfehlung:** Widget mit Link auf /reviews?escalated=true.
- **Retest (verbindlich):** Vitest und e2e-real mit Item älter als 15 Tage.
- **Kodierender Test:** keiner

<a id="f-248"></a>
### F-248 (P3) – Verworfener override_conflict-Wert bleibt nach Wertwechsel unterdrückt

- **Betroffene WPs:** WP-089 · **Komponente:** backend/internal/reviews/service.go:89-101 · **Quelle:** Verifikation
- **Reproduktion:** S1/S2 (reviews.log): nach zwischenzeitlichem Wertwechsel reviews=0; Kontrolle seen-9 → 1.
- **Soll:** REC-12: Unterdrückung endet mit der nächsten Änderung.
- **Ist:** Bleibt dauerhaft unterdrückt.
- **Nutzerwirkung:** Abweichungen werden nicht mehr geprüft.
- **Ursache (belegt):** Vergleich nur mit dem neuesten verworfenen Item.
- **Empfehlung:** Unterdrückung an die Beobachtungshistorie knüpfen.
- **Retest (verbindlich):** PG-Test S1/S2 plus direkter Wiederholungsfall.
- **Kodierender Test:** keiner

<a id="f-249"></a>
### F-249 (P3) – Manuell gepflegte IPs (reserved/static) und Interfaces an entdeckten CIs nach drei Läufen gelöscht

- **Betroffene WPs:** WP-090 · **Komponente:** backend/internal/ipam/pg_repository.go:619-640 · **Quelle:** Verifikation
- **Reproduktion:** reserved-IP nach 3 Records ohne sie → 0 Zeilen (discovery.log).
- **Soll:** UNKLAR (REC-07 schweigt; REC-03 spricht für Schutz manueller Daten), Produktentscheidung nötig.
- **Ist:** Gelöscht, mit ci_change.
- **Nutzerwirkung:** Manuelle IPAM-Daten verschwinden.
- **Ursache (belegt):** Kein Filter auf ip_address.type bzw. Quelle.
- **Empfehlung:** Entscheiden; ggf. nur type='discovered' zählen.
- **Retest (verbindlich):** PG-Test des entschiedenen Verhaltens.
- **Kodierender Test:** keiner

<a id="f-250"></a>
### F-250 (P3) – Discovery-Reklassifizierung läuft in separater Transaktion nach dem Record-Update

- **Betroffene WPs:** WP-172 · **Komponente:** backend/internal/discovery/discovery.go:1473-1486,1890-1912 · **Quelle:** Audit/Verifikation
- **Reproduktion:** Ein Record mit Typwechsel → +3 ci_change, +2 Events (probe-discovery2.log).
- **Soll:** Atomare Anwendung, eine Änderung je Record (AUD-03).
- **Ist:** Zwei Transaktionen.
- **Nutzerwirkung:** Gesplittete Historie; bei Absturz neue Attribute mit altem Typ.
- **Ursache (belegt):** raiseGenericType außerhalb der Transaktion.
- **Empfehlung:** In dieselbe Transaktion ziehen.
- **Retest (verbindlich):** PG-Ingest-Test: genau ein Event.
- **Kodierender Test:** keiner

<a id="f-251"></a>
### F-251 (P3) – ci_merge.job_id nie gesetzt; abgelaufene Snapshots/Tombstones nie bereinigt

- **Betroffene WPs:** WP-173 · **Komponente:** backend/internal/ci/merge.go (INSERT); migrations/000131:21 · **Quelle:** Audit/Verifikation
- **Reproduktion:** job_id ist NULL nach getracktem Merge; grep findet keine Retention.
- **Soll:** Verweis auf den Job; Datenminimierung.
- **Ist:** NULL, Snapshots unbegrenzt gespeichert.
- **Nutzerwirkung:** Kopien von Kontaktdaten werden dauerhaft aufbewahrt.
- **Ursache (belegt):** Fehlendes Feld und fehlende Retention-Task.
- **Empfehlung:** job_id setzen; Retention-Task ergänzen.
- **Retest (verbindlich):** Retention-Integrationstest.
- **Kodierender Test:** keiner

<a id="f-252"></a>
### F-252 (P3) – Rack-Seite lädt jedes montierte CI einzeln (1+N); putMount-Client ungenutzt

- **Betroffene WPs:** WP-175 · **Komponente:** frontend RackPage.tsx:62; hooks.ts:254-266; client.ts:751 · **Quelle:** Verifikation
- **Reproduktion:** Je Mount ein GET /cis/{id}; grep putMount findet nur die Definition.
- **Soll:** Ein bis zwei Requests; kein toter Code.
- **Ist:** 1+N Requests.
- **Nutzerwirkung:** Langsame Ansicht; kein Mount aus der UI möglich.
- **Ursache (belegt):** Layout ohne Anzeigefelder; kein Mount-UI.
- **Empfehlung:** Name und Typ ins Layout aufnehmen; Mount-UI bauen oder putMount entfernen.
- **Retest (verbindlich):** Vitest: höchstens 2 fetch-Aufrufe.
- **Kodierender Test:** keiner

<a id="f-253"></a>
### F-253 (P3) – GET /racks/{id}/layout liefert "mounts": null für leeres Rack (OpenAPI: Pflicht-Array)

- **Betroffene WPs:** WP-175 · **Komponente:** backend/internal/rack/handler.go:243-249; api/openapi.yaml:12252-12259 · **Quelle:** Real-Stack-E2E (owned)
- **Reproduktion:** Real-Stack L08: leeres Rack → mounts=null (auch GET /racks/{id}/mounts data:null).
- **Soll:** mounts: [].
- **Ist:** null.
- **Nutzerwirkung:** Strikt typisierte Clients können abstürzen; die SPA ist durch ?? [] geschützt.
- **Ursache (belegt):** nil-Slice wird als null serialisiert.
- **Empfehlung:** Auf []T{} normalisieren (api.CompleteList).
- **Retest (verbindlich):** npx playwright test -c e2e-real/playwright.real.config.ts locations-racks -g "L08" grün.
- **Kodierender Test:** frontend/e2e-real/locations-racks.spec.ts L08

<a id="f-254"></a>
### F-254 (P3) – Client-seitige Ausfallsimulation folgt roher source→target-Richtung; ihr Vitest kodiert das alte Verhalten

- **Betroffene WPs:** WP-176 · **Komponente:** frontend/src/pages/TopologyPage.tsx:80-153; TopologyPage.test.tsx:70-95 · **Quelle:** Verifikation
- **Reproduktion:** Scratch-Vitest: pdu fällt aus → [], vm fällt aus → [pdu, server].
- **Soll:** WP-176 K2: falsche Tests korrigiert; Ansicht konsistent mit dem Backend.
- **Ist:** Invers, solange /impact lädt oder fehlschlägt.
- **Nutzerwirkung:** Zeitweise falsche Impact-Menge.
- **Ursache (belegt):** computeOutageImpact ignoriert die Beziehungstypen.
- **Empfehlung:** Client-BFS entfernen oder Regeln nutzen; Test korrigieren.
- **Retest (verbindlich):** Vitest: pdu-Ausfall ergibt {server, vm}.
- **Kodierender Test:** frontend/src/pages/TopologyPage.test.tsx (kodiert altes Verhalten)

<a id="f-255"></a>
### F-255 (P3) – Impact-Traversierung per Ebenen-BFS statt rekursivem CTE ohne formale Entscheidung; ungültiger depth-Parameter still akzeptiert

- **Betroffene WPs:** WP-177 · **Komponente:** backend/internal/relationship/pg_repository.go:353-450; topology/topology.go:443-455 · **Quelle:** Audit/Verifikation (Abweichung)
- **Reproduktion:** Code und docs/acceptance/lasttest-g1.md:526 (WP-206-Korrektur); /impact/{pdu}?depth=abc → 200.
- **Soll:** IMP-02 nennt einen rekursiven CTE; ungültige Parameter sollten abgewiesen werden.
- **Ist:** BFS (Verhalten erfüllt); kein E-Eintrag.
- **Nutzerwirkung:** Gering; nur eine Dokumentationslücke.
- **Ursache (belegt):** Performance-Korrektur ohne Entscheidungseintrag.
- **Empfehlung:** Entscheidung dokumentieren bzw. IMP-02-Wortlaut anpassen; depth validieren.
- **Retest (verbindlich):** Entscheidung liegt vor; HTTP-Test depth=abc entsprechend dem Vertrag.
- **Kodierender Test:** keiner

<a id="f-256"></a>
### F-256 (P3) – Der Collector-Statuslauf bricht beim ersten Org-Fehler für alle restlichen Orgs ab

- **Betroffene WPs:** WP-143 · **Komponente:** backend/internal/discovery/collector_status.go:170-174 · **Quelle:** Verifikation WP-143
- **Reproduktion:** Probe TestVerifyB18StatusRunStopsAtFirstOrgError (go test -overlay SP/work/verify-B18/overlay.json -run TestVerifyB18 ./internal/discovery/): Licensed schlägt für Org 1 fehl → der stumme Collector der späteren Org bleibt active.
- **Soll:** Fehler je Org protokollieren und weiterlaufen.
- **Ist:** Run kehrt mit 'organization …: discovery license' zurück; spätere Orgs werden jede Minute übersprungen.
- **Nutzerwirkung:** Stumme Collector anderer Mandanten bleiben 'active', ohne collector.offline. Gleiches Muster wie L-T4-19, aber eigener Job.
- **Ursache (belegt):** return innerhalb der Org-Schleife.
- **Empfehlung:** Weiterlaufen und am Ende errors.Join zurückgeben.
- **Retest (verbindlich):** Integrationstest mit zwei Orgs: Licensed-Fehler in Org 1 → der Collector von Org 2 geht offline, Run liefert einen zusammengeführten Fehler.
- **Kodierender Test:** keiner

<a id="f-257"></a>
### F-257 (P3) – PATCH /collectors/{id} antwortet bei unbekanntem oder fremdem Collector mit 500 statt 404

- **Betroffene WPs:** WP-143 · **Komponente:** backend/internal/discovery/pg_repository.go:190; internal/api/dberrors.go:83-88 · **Quelle:** Verifikation WP-143
- **Reproduktion:** Probe TestVerifyB18CollectorCrossOrg: Org B sendet PATCH auf einen Collector von Org A → 500; der Heartbeat liefert 404, die DB bleibt unverändert.
- **Soll:** 404 für unbekannte oder mandantenfremde IDs.
- **Ist:** 500; die Isolation selbst hält.
- **Nutzerwirkung:** Irreführende Fehler und 5xx-Alarme bei Client-Fehlern.
- **Ursache (belegt):** fmt.Errorf("not found") statt eines typisierten Not-found-Fehlers; derselbe Muster in RenewCertificate.
- **Empfehlung:** api.ErrNotFound zurückgeben und auf 404 abbilden.
- **Retest (verbindlich):** API-Test: PATCH auf eine Zufalls-UUID und eine fremde ID → 404, DB unverändert.
- **Kodierender Test:** keiner

<a id="f-258"></a>
### F-258 (P3) – Der Collector protokolliert 'collector NATS endpoint connected over mTLS', obwohl der Endpunkt ihn abgewiesen hat (disabled/revoked)

- **Betroffene WPs:** WP-145, WP-155 · **Komponente:** collector/collectorcmd/natsconn.go:83, 101-107 · **Quelle:** Real-Stack-E2E (discovery-override.spec.ts D08, owned)
- **Reproduktion:** Simulierten Collector enrollen und starten; PATCH /api/v1/collectors/{id} {status:'disabled'}; Collector neu starten und stdout lesen (Real-Stack-Lauf D08).
- **Soll:** DIS-08: Ein abgewiesener Collector protokolliert einen Verbindungs- bzw. Autorisierungsfehler.
- **Ist:** 11:55:30.543 INFO 'connected over mTLS', 2 ms nachdem der Server 'collector refused … certificate revoked' protokolliert hat; erst 5 s später 'nats: connection closed'.
- **Nutzerwirkung:** Bei der Fehlersuche liest der Betreiber eine Erfolgsmeldung, die die wahre Ursache verdeckt; keine Auswirkung auf Daten oder Sicherheit.
- **Ursache (belegt):** RetryOnFailedConnect(true) lässt nats.Connect ohne Fehler zurückkehren (nats.go v1.54.0:3002-3007); die Erfolgsmeldung kommt unbedingt.
- **Empfehlung:** Auf ConnectedHandler bzw. ErrorHandler warten und Autorisierungsfehler mit Ursache protokollieren.
- **Retest (verbindlich):** Variante von D08 prüft im Collector-Log 'authorization' bzw. 'refused' und keine Erfolgsmeldung.
- **Kodierender Test:** keiner (in discovery-override.spec.ts D08 nur kommentiert)

<a id="f-259"></a>
### F-259 (P3) – Die Firewall-Matrix beschreibt Collector-Verkehr, den es nicht gibt (Selbsterneuerung, Heartbeat und Ingest über HTTPS)

- **Betroffene WPs:** WP-146 · **Komponente:** docs/betrieb/firewall-matrix.md · **Quelle:** Audit/Verifikation WP-146
- **Reproduktion:** Kundentabelle Zeile 1 nennt '443 … Zertifikatserneuerung, Heartbeat, Ingest'; e2e-onboarding-real.log:15/19 zeigt dafür HTTP 401; Renew hat keinen Aufrufer.
- **Soll:** 443 für Enrollment und Updates; 4222 für Heartbeat, Ingest, Bundles und Scopes.
- **Ist:** Ungenaue Aussagen; zudem werden ≤24 h nach disable akzeptiert, entgegen COL-04.
- **Nutzerwirkung:** Irreführende Betriebsdoku; der Ablauf nach 90 Tagen überrascht.
- **Ursache (belegt):** Doku vor der Implementierung geschrieben.
- **Empfehlung:** Nach den Fixes zu WP-145 korrigieren.
- **Retest (verbindlich):** Review gegen E2E-Log und Code.
- **Kodierender Test:** keiner

<a id="f-260"></a>
### F-260 (P3) – Auto-Update braucht cosign auf dem Host, den der VM-Installer nicht bereitstellt

- **Betroffene WPs:** WP-148 · **Komponente:** install-vm.sh; edgecore/update/update.go:272-291 · **Quelle:** Audit/Verifikation WP-148
- **Reproduktion:** grep -n cosign install-vm.sh → kein Treffer; ohne cosign scheitert jedes Apply mit 'executable file not found'.
- **Soll:** Ein per VM installierter Collector mit aktivierten Updates kann signierte Releases prüfen.
- **Ist:** Updates werden abgewiesen, bis cosign manuell installiert ist (fail-closed, dokumentiert).
- **Nutzerwirkung:** Auto-Update findet still nicht statt; keine Sicherheitsauswirkung.
- **Ursache (belegt):** Laufzeitabhängigkeit wird nicht bereitgestellt.
- **Empfehlung:** Gepinntes cosign installieren oder sigstore-go einbetten; beim Start warnen, wenn es fehlt.
- **Retest (verbindlich):** Shell-Test von install-vm.sh mit Update-URL: cosign vorhanden.
- **Kodierender Test:** keiner

<a id="f-261"></a>
### F-261 (P3) – Widersprüchliche Capability-Doku (icmp.go vs. Dockerfile)

- **Betroffene WPs:** WP-149 · **Komponente:** collector/plugins/sweep/icmp.go:19-20; collector/Dockerfile:20-23 · **Quelle:** Audit/Verifikation WP-149
- **Reproduktion:** icmp.go:19-20 spricht von 'file capability on the binary'; im Dockerfile steht 'No file capability is set'.
- **Soll:** Eine konsistente Aussage zur Capability.
- **Ist:** Der Kommentar widerspricht dem ausgelieferten Image.
- **Nutzerwirkung:** Betreiber suchen eine nicht existierende File-Capability.
- **Ursache (belegt):** Kommentar nach der Dockerfile-Entscheidung nicht nachgezogen.
- **Empfehlung:** Kommentar angleichen und einen Betreiberhinweis ergänzen (ping_group_range oder NET_RAW).
- **Retest (verbindlich):** Doku-Review.
- **Kodierender Test:** keiner

<a id="f-262"></a>
### F-262 (P3) – Der Sweep startet je Ziel eine Goroutine, bevor die Semaphore mit 256 Plätzen belegt wird

- **Betroffene WPs:** WP-149 · **Komponente:** collector/plugins/sweep/sweep.go:92-145 · **Quelle:** Verifikation WP-149
- **Reproduktion:** Probe TestVerifyB18GoroutinesForLargeScope (SP/work/verify-B18/overlay-sweep.json): 65.536 Ziele → Spitze von 63.356 Goroutinen.
- **Soll:** Ressourcenverbrauch unabhängig von der Scope-Größe begrenzt.
- **Ist:** Bei einem /16 rund 63k Goroutinen (≥124 MiB Stack).
- **Nutzerwirkung:** OOM-Risiko auf kleinen Collector-VMs.
- **Ursache (belegt):** Die Semaphore wird erst innerhalb der Goroutine belegt.
- **Empfehlung:** Semaphore vor dem go-Aufruf belegen oder einen festen Worker-Pool nutzen.
- **Retest (verbindlich):** Unit-Test: Spitze ≤ MaxConcurrency + 50.
- **Kodierender Test:** keiner

<a id="f-263"></a>
### F-263 (P3) – Das Profilfeld systemTemplate ist ein totes Label; das Backend wertet es nie aus

- **Betroffene WPs:** WP-151 · **Komponente:** collector/profiles/data/*.yaml; backend (kein Konsument) · **Quelle:** Audit/Verifikation WP-151
- **Reproduktion:** grep -rn 'system_template\|systemTemplate' backend frontend/src api → keine Treffer.
- **Soll:** DIS-02: Profile bilden auch auf das System-Template ab.
- **Ist:** Das Attribut wird gesetzt, aber ignoriert; die Namen weichen von den citype-Templates ab.
- **Nutzerwirkung:** Gering; der CI-Typ ist korrekt.
- **Ursache (belegt):** Kein Serverkonsument, Namen nicht abgeglichen.
- **Empfehlung:** Beim Ingest auf ein citype-Template abbilden oder das Feld streichen und dokumentieren.
- **Retest (verbindlich):** Collector-Test: jedes Template existiert im Serverkatalog.
- **Kodierender Test:** keiner

<a id="f-264"></a>
### F-264 (P3) – Scope-Credentials werden nur auf die Org geprüft: ein Credential von Client 2 lässt sich an einen Scope von Client 1 binden

- **Betroffene WPs:** WP-153 · **Komponente:** backend/internal/discovery/scope.go:418-431 · **Quelle:** Verifikation WP-153
- **Reproduktion:** pg.json-Probe: Ein Org-Principal führt CreateScope für den Collector von Client 1 mit dem SSH-Credential von Client 2 aus → err=nil.
- **Soll:** Scope-Credentials gehören zum Client des Scopes oder sind org-weit.
- **Ist:** Nur organization_id wird geprüft.
- **Nutzerwirkung:** Eine Fehlkonfiguration kann Geheimnisse eines Clients zu einem anderen Client tragen.
- **Ursache (belegt):** Prüfung auf Client-Konsistenz fehlt.
- **Empfehlung:** credential.client_id IS NULL OR = scope.client_id verlangen.
- **Retest (verbindlich):** PG-Test: fremdes Client-Credential → 422.
- **Kodierender Test:** keiner

<a id="f-265"></a>
### F-265 (P3) – Docker-/Bridge-Netze des Collector-Hosts werden als Scopes vorgeschlagen

- **Betroffene WPs:** WP-153, WP-156 · **Komponente:** collector/collectorcmd/scopes.go:126-151 · **Quelle:** Audit/Verifikation WP-153
- **Reproduktion:** SP/runs/e2e-onboarding-real.log: subnets ['192.0.2.0/24','172.17.0.0/16','172.18.0.0/16'], suggested:3.
- **Soll:** Nur sinnvolle LAN-Subnetze werden vorgeschlagen.
- **Ist:** Container-Bridges (/16) werden angeboten und würden gekappt (L-T4-17).
- **Nutzerwirkung:** Rauschen im Onboarding; ein angenommener Vorschlag löst einen großen, nutzlosen Sweep aus.
- **Ursache (belegt):** localSubnets filtert nur Loopback, Link-Local und Host-Routen.
- **Empfehlung:** docker*/br-*/veth*/cni* auslassen.
- **Retest (verbindlich):** TestLocalSubnetsForScopeSuggestions mit docker0/br-x → nicht gemeldet.
- **Kodierender Test:** keiner

<a id="f-266"></a>
### F-266 (P3) – Poll-Intervalle nur je Org und Protokoll einstellbar, nicht je Scope; Cron wird in UTC ausgewertet

- **Betroffene WPs:** WP-154 · **Komponente:** collector/collectorcmd/scopes.go:236-245, schedule.go:212 · **Quelle:** Audit/Verifikation WP-154
- **Reproduktion:** protocolSchedule nutzt pollInterval(protocol) org-weit; cronSpec.next nutzt t.UTC().
- **Soll:** DIS-10 'pro Scope und Org konfigurierbar'; Cron in der Zeitzone der Org erwartet.
- **Ist:** Je Scope ist nur der Sweep-Zeitplan einstellbar, Cron läuft in UTC.
- **Nutzerwirkung:** Kein Feintuning je Scope; Cron-Fenster verschieben sich für deutsche Orgs um 1–2 h.
- **Ursache (belegt):** Spezifikationsauslegung, keine Zeitzonenbehandlung.
- **Empfehlung:** DIS-10 klären; Cron in organization.timezone auswerten oder in der UI als UTC kennzeichnen.
- **Retest (verbindlich):** Unit-Tests für Poll-Override je Scope (nach Entscheidung) und Cron mit Europe/Berlin.
- **Kodierender Test:** keiner

<a id="f-267"></a>
### F-267 (P3) – Discovery-Metriken undokumentiert; Scope-Aktionen ohne Fehlerrückmeldung, Löschen ohne Bestätigung

- **Betroffene WPs:** WP-153, WP-155 · **Komponente:** frontend/src/pages/DiscoveryPage.tsx:300-351; docs · **Quelle:** Audit/Verifikation WP-155
- **Reproduktion:** grep discovery_errors_total in docs/deploy → keine Treffer; update/remove/resolve.mutate ohne onError.
- **Soll:** DIS-08: Betreiber kennen die Metriken; UI-Aktionen geben Rückmeldung.
- **Ist:** Fehlschläge bei Toggle, Löschen und Resolve bleiben still; gelöscht wird sofort.
- **Nutzerwirkung:** Kein Alerting; versehentliches Löschen möglich.
- **Ursache (belegt):** Doku und UX nicht abgeschlossen.
- **Empfehlung:** Metriken dokumentieren (mit Alert-Regeln), Löschbestätigung und Fehlermeldungen ergänzen.
- **Retest (verbindlich):** Vitest: Bestätigung und Fehlertext bei 500; Doku-Check.
- **Kodierender Test:** keiner

<a id="f-268"></a>
### F-268 (P3) – ci.online ist dokumentiert, wird bei der Rückkehr eines CIs aber praktisch nie gesendet

- **Betroffene WPs:** WP-157 · **Komponente:** backend/internal/discovery/discovery.go:1413-1419 · **Quelle:** Audit/Verifikation WP-157
- **Reproduktion:** Probe TestProbeB20IngestChain (SP/work/audit-B20/overlay.json): Nach erneuter Sichtung changes={Online:0}, im Outbox nur ci.offline.
- **Soll:** docs/README.md:890: Eine wieder gesehene CI wird active mit ci.online.
- **Ist:** Der Ingest setzt unknown→active ohne Event.
- **Nutzerwirkung:** Webhook-Konsumenten erhalten zu ci.offline kein Recovery-Event.
- **Ursache (belegt):** Der Übergang liegt im Ingest-Pfad ohne Outbox-Eintrag.
- **Empfehlung:** ci.online in der Ingest-Transaktion anhängen oder die Doku korrigieren.
- **Retest (verbindlich):** Probe: Outbox-Zeile ci.online nach dem Re-Ingest.
- **Kodierender Test:** keiner

<a id="f-269"></a>
### F-269 (P3) – LLDP-Nachbar wird mit FDB-Confidence 0,6 gespeichert, wenn ein FDB-Eintrag vorher gemeldet wird; der Wert kippt zwischen Ingests

- **Betroffene WPs:** WP-158 · **Komponente:** backend/internal/discovery/topology.go:144-194; relationship pg_repository.go:629-635 · **Quelle:** Audit/Verifikation WP-158
- **Reproduktion:** go test -overlay SP/work/verify-B20/overlay.json -run TestVerifyB20LLDPAfterFDB -v ./internal/discovery/ (TEST_DATABASE_URL gesetzt).
- **Soll:** TOP-01: LLDP ergibt 1.0; FDB zählt nur, wo kein LLDP existiert.
- **Ist:** sw1→peer 0,6, sw2→peer 1.0; nach erneutem Ingest mit FDB zuerst 0,6.
- **Nutzerwirkung:** Bestätigte Links werden unterbewertet und kippen still; aktuell nutzt niemand die Confidence.
- **Ursache (belegt):** FDB-Unterdrückung je Port statt je Nachbar, First-Wins-Dedup, Confirm überschreibt.
- **Empfehlung:** LLDP/CDP zuerst ableiten oder das Maximum behalten.
- **Retest (verbindlich):** Probe: beide Reihenfolgen und der Re-Ingest ergeben 1.0.
- **Kodierender Test:** keiner

<a id="f-270"></a>
### F-270 (P3) – Die Provenienz einer Kante (Akteur, Zeit) ist per PATCH attributes überschreibbar

- **Betroffene WPs:** WP-159 · **Komponente:** backend/internal/relationship/pg_repository.go:242-245 · **Quelle:** Verifikation WP-159
- **Reproduktion:** go test -overlay SP/work/verify-B20/overlay.json -run TestVerifyB20ProvenanceAndSource -v ./internal/relationship/
- **Soll:** REL-08: Akteur und Zeit werden zuverlässig vom System erfasst.
- **Ist:** 200; gespeichert wird {actor:'forged', at:'1999-01-01'}.
- **Nutzerwirkung:** Nutzer mit Schreibrecht können fälschen, wer eine Kante wann angelegt hat.
- **Ursache (belegt):** jsonb-Merge (||) ohne reservierten Schlüssel 'provenance'.
- **Empfehlung:** provenance bei PATCH ablehnen oder ignorieren; Änderungen in der Historie erfassen.
- **Retest (verbindlich):** API-Test: PATCH mit provenance → 422 oder ignoriert.
- **Kodierender Test:** keiner

<a id="f-271"></a>
### F-271 (P3) – POST /relationships erlaubt Nutzern Kanten mit source 'discovery'

- **Betroffene WPs:** WP-159, WP-160 · **Komponente:** backend/internal/relationship/relationship.go:551-556 · **Quelle:** Verifikation WP-160 (auch WP-159-Kriterium Quellen-Policy)
- **Reproduktion:** Probe TestVerifyB20ProvenanceAndSource: Nutzer-POST {source:'discovery'} → 201, verification_state=unverified.
- **Soll:** REL-04: POST erzeugt manuelle Kanten; Systemquellen sind ihren Schreibern vorbehalten.
- **Ist:** Gespeichert als discovery; das Löschen braucht dann force, und der Cleanup entfernt die Kante nach 7 d.
- **Nutzerwirkung:** Von Nutzern eingetragene Kanten können automatisch verschwinden; die Provenienz täuscht.
- **Ursache (belegt):** CanonicalSource akzeptiert alle sieben Quellen von jedem Aufrufer.
- **Empfehlung:** discovery/agent auf Service-Principals beschränken oder Nutzer-POSTs auf manual/api umschreiben.
- **Retest (verbindlich):** API-Test: Nutzer-POST mit source=discovery → 422 bzw. manual.
- **Kodierender Test:** keiner

<a id="f-272"></a>
### F-272 (P3) – DELETE /relationships bildet alle Fehler außer ErrForceRequired auf 404 ab

- **Betroffene WPs:** WP-160 · **Komponente:** backend/internal/relationship/relationship.go:687-690 · **Quelle:** Audit WP-160
- **Reproduktion:** Codepfad gelesen: jeder andere Fehler aus DeleteWithPolicy ergibt 404 'relationship not found'; nicht mit Daten reproduziert.
- **Soll:** 500 bei Serverfehlern, 422/403 bei Ablehnungen.
- **Ist:** Pauschal 404 (laut Code).
- **Nutzerwirkung:** Die Ursache eines gescheiterten Löschens bleibt unklar.
- **Ursache (Hypothese):** Hypothese: pauschale Fehlerabbildung (nur Code-Review).
- **Empfehlung:** not found → 404, abgelehnte Suppression → 422, Rest per api.WriteRepoError.
- **Retest (verbindlich):** API-Test mit fehlender relationship_type-Zeile → 422.
- **Kodierender Test:** keiner

<a id="f-273"></a>
### F-273 (P3) – Eine Suppression ist irreversibel und entsteht mit einem einzigen Klick

- **Betroffene WPs:** WP-160 · **Komponente:** frontend/src/components/cmdb/CIRelationshipsCard.tsx:165-185; api/openapi.yaml · **Quelle:** Audit/Verifikation WP-160
- **Reproduktion:** Klick 'Entfernen und unterdrücken' → DELETE ?force=true ohne Bestätigung; die OpenAPI hat keinen Endpunkt zum Auflisten oder Aufheben; relationship_suppression speichert weder Akteur noch Grund.
- **Soll:** Destruktive, dauerhafte Aktionen brauchen Bestätigung, Rücknahme und Nachvollziehbarkeit.
- **Ist:** Nichts davon vorhanden.
- **Nutzerwirkung:** Ein versehentlicher Klick verbirgt einen echten Link dauerhaft; reparierbar nur per DB.
- **Ursache (belegt):** Umfang auf das Anlegen beschränkt.
- **Empfehlung:** Bestätigungsdialog, auditierte Endpunkte zum Auflisten und Aufheben, Akteur und Zeit speichern.
- **Retest (verbindlich):** Vitest mit Bestätigung; API-Test: Suppression aufheben → Re-Ingest legt die Kante neu an.
- **Kodierender Test:** keiner

<a id="f-274"></a>
### F-274 (P3) – Das Akzeptanzkriterium von WP-160 widerspricht REL-04 (wer force braucht)

- **Betroffene WPs:** WP-160 · **Komponente:** docs/plan/implementierungsplan.md (Block WP-160) · **Quelle:** Audit/Verifikation WP-160
- **Reproduktion:** SP/wp/WP-160.md 'manuelle Kanten nur mit force' vs. docs/spec/katalog-v3/04-ci-netz-rack-beziehungen-impact.md:90 und api/openapi.yaml:1415-1423.
- **Soll:** Eine konsistente Regel; nach E-01 gilt der Anforderungstext.
- **Ist:** Implementierung und OpenAPI folgen REL-04, der WP-Text sagt das Gegenteil.
- **Nutzerwirkung:** Nur Mehrdeutigkeit in der Abnahme; das Verhalten ist korrekt.
- **Ursache (belegt):** Redaktionsfehler im Plan.
- **Empfehlung:** WP-Text an REL-04 anpassen.
- **Retest (verbindlich):** Doku-Review.
- **Kodierender Test:** keiner

<a id="f-275"></a>
### F-275 (P3) – Ausgeschaltete Outlets zählen als Stromeinspeisung (Redundanz)

- **Betroffene WPs:** WP-161 · **Komponente:** backend/internal/discovery/power.go:90-104 · **Quelle:** Audit WP-161
- **Reproduktion:** Auditor-Probe TestProbeB20PowerRedundancyStale: PSU2-Outlet 'off' → beide Kanten redundant=true.
- **Soll:** IMP-09 schweigt dazu; ein stromloses Outlet liefert vermutlich keine Redundanz.
- **Ist:** Der Zustand wird ignoriert, outlet_state=off steht nur als Attribut.
- **Nutzerwirkung:** Ein Gerät wirkt redundant versorgt, hängt aber womöglich an einer PDU.
- **Ursache (Hypothese):** Kein Zustandsfilter; ob es ein Defekt ist, hängt an einer Spezifikationsentscheidung (offen).
- **Empfehlung:** E-Entscheidung treffen: off-Outlets auslassen oder aus der Redundanzzählung nehmen.
- **Retest (verbindlich):** Unit-Test nach der Entscheidung.
- **Kodierender Test:** keiner

<a id="f-276"></a>
### F-276 (P3) – Die Simulationssperre hängt an einem exakt gesetzten RETICORA_ENVIRONMENT (fail-open)

- **Betroffene WPs:** WP-162 · **Komponente:** collector/collectorcmd/collectorcmd.go:149, 1341 · **Quelle:** Verifikation WP-162
- **Reproduktion:** Gebauter Collector mit RETICORA_SIMULATE=true: ENVIRONMENT leer oder 'prod' → collector.simulate.enabled; 'PRODUCTION' → exit 1 (SP/work/verify-B20/out/sim-*.log).
- **Soll:** SIM-02: robust nicht in Produktion aktivierbar.
- **Ist:** Simuliert, sofern der Wert nicht 'production' lautet; der Installer setzt ihn, ein manuell gestartetes Image nicht.
- **Nutzerwirkung:** Ein von Hand deployter Produktions-Collector könnte 30 Fake-CIs einspeisen.
- **Ursache (belegt):** Default 'development', Vergleich gegen nur einen Wert.
- **Empfehlung:** Simulation nur für explizite development/test-Werte erlauben; optional simulierte Records serverseitig ablehnen.
- **Retest (verbindlich):** Shell-Test: leer/'prod'/'staging' → Abweisung; 'development' simuliert.
- **Kodierender Test:** keiner

<a id="f-277"></a>
### F-277 (P3) – Der Seed umgeht den Suchindex: geseedete CIs findet die globale Suche nicht

- **Betroffene WPs:** WP-203, WP-204 · **Komponente:** backend/cmd/seed/seed.go:216-247 · **Quelle:** Audit/Verifikation WP-203
- **Reproduktion:** Seed ausführen; SELECT entity_type,count(*) FROM search_document … → nur 'location' (8 je Org).
- **Soll:** Geseedete CIs verhalten sich wie angelegte CIs (durchsuchbar).
- **Ist:** Keine CI-Dokumente bis POST /api/v1/search/reindex.
- **Nutzerwirkung:** Die Demo-Suche findet keine CIs; die globale Suche ist unter Last nicht messbar (L-T4-28).
- **Ursache (belegt):** Direkte Inserts in ci; die Indizierung lebt nur im CIIndexer.
- **Empfehlung:** Am Ende des Seeds reindexieren und in TestSeedSIM01 prüfen.
- **Retest (verbindlich):** TestSeedSIM01: count(search_document ci) == objects.
- **Kodierender Test:** keiner

<a id="f-278"></a>
### F-278 (P3) – Geseedete CIs haben keinen Standort: site_id und room_id setzt der Trigger ci_derive_location auf NULL

- **Betroffene WPs:** WP-203 · **Komponente:** backend/cmd/seed/seed.go:219-227; Trigger ci_derive_location (WP-054) · **Quelle:** Verifikation WP-203
- **Reproduktion:** Seed ausführen; SELECT count(*),count(site_id),count(room_id),count(location_id) FROM ci WHERE org=… → 40|0|0|0 (10k: 10000|0|0|0).
- **Soll:** SIM-01-Standortkette: Die 40 gerackten CIs liegen im HQ-Serverraum.
- **Ist:** Alle geseedeten CIs ohne Standort; Racks nur über rack_mount verknüpft.
- **Nutzerwirkung:** Standortfilter und Zählungen in der Demo sind leer; Site-Scope-Isolationstests auf dem Seed beweisen nichts.
- **Ursache (belegt):** Der Seed schreibt die Legacy-Spalten ohne location_id; der Trigger leitet nur aus location_id ab.
- **Empfehlung:** location_id setzen und in TestSeedSIM01 prüfen.
- **Retest (verbindlich):** TestSeedSIM01: location_id IS NOT NULL für alle; CI 21 im Serverraum hq.
- **Kodierender Test:** keiner

<a id="f-279"></a>
### F-279 (P3) – Der Lastbericht lässt 1.000 Records/s und die Volumina aus NFR-01 aus

- **Betroffene WPs:** WP-206 · **Komponente:** docs/acceptance/lasttest-g1.md · **Quelle:** Audit/Verifikation WP-206
- **Reproduktion:** grep '1.000' docs/acceptance/lasttest-g1.md → nur eine fremde Notiz (Zeile 353); der Seed hat 1 Interface/IP je CI und 2 Orgs.
- **Soll:** Das Kriterium nennt 200/1.000 rec/s; NFR-01 nennt 50k Interfaces, 200k IPs, 100k Beziehungen und 500 Orgs.
- **Ist:** Berichtet werden nur 200/s und 10k Objekte; 1.000/s ist weder berichtet noch ausdrücklich verschoben.
- **Nutzerwirkung:** Die Annahmen zu Reserve und Volumen sind undokumentiert.
- **Ursache (belegt):** Umfangsauslegung (NFR-03-Teilumfang [P5]) ohne Vermerk im Bericht.
- **Empfehlung:** Abschnitt ergänzen: gemessener Max-Drain vs. 1.000/s (P5); NFR-01 gemessen oder mit Verweis verschoben.
- **Retest (verbindlich):** Doku-Review von lasttest-g1.md.
- **Kodierender Test:** keiner

<a id="f-280"></a>
### F-280 (P3) – TestCIQueryBudget ist planner- und umgebungsabhängig instabil (falscher Fehlschlag 'sequential scan')

- **Betroffene WPs:** WP-204, WP-206 · **Komponente:** backend/internal/ci/querybudget_integration_test.go:165 · **Quelle:** Defekttest-Welle/mechanische Verifikation (audit/phase1-final/evidence/audit-tests/querybudget-flake.txt)
- **Reproduktion:** cd /home/user/Reticora-CMDB/backend && TEST_DATABASE_URL=… go test -count=1 -run '^TestCIQueryBudget$' ./internal/ci/ auf einer frischen Template-Kopie nach Container-Neustart.
- **Soll:** Eine deterministische Budgetprüfung.
- **Ist:** 15.10. PASS (12,1 s); 18.10. FAIL im Verifikationslauf ('hostname lookup reads ci with a sequential scan', 'attribute lookup …'), auch auf 3a9669a; finaler HEAD-Lauf PASS (9,88 s). Code unverändert.
- **Nutzerwirkung:** Zufällige CI-Fehlschläge und ein unzuverlässiger Nachweis für ABN-02 und NFR-02.
- **Ursache (Hypothese):** Hypothese: Der Planner wählt je nach Statistik zwischen Seq Scan und Org-Index-Scan (gleicher Mechanismus wie die Vakuität L-T4-27); nicht belegt.
- **Empfehlung:** Vor dem EXPLAIN ANALYZE ausführen, auf Produkt-SQL und konkrete Indexnamen umstellen (siehe L-T4-27).
- **Retest (verbindlich):** Zehn Läufe auf frischen DB-Kopien → 10/10 PASS, und nach dem Umbau ein deterministischer Negativtest.
- **Kodierender Test:** backend/internal/ci/querybudget_integration_test.go TestCIQueryBudget (bestehender Test, instabil)

<a id="f-281"></a>
### F-281 (P3) – integrity-check.sql: vakuöse Duplikatprüfungen, keine Prüfung schwacher Identitäten, keine Cross-Tenant-Prüfung CI→location/type, läuft nicht nach dem Lastlauf

- **Betroffene WPs:** WP-207 · **Komponente:** tests/integrity-check.sql:63-77; .github/workflows/load.yml · **Quelle:** Audit/Verifikation WP-207
- **Reproduktion:** SP/work/verify-B26/xtenant-probe3.txt: Als Owner eine CI per location_id an eine fremde Org binden → alle 15 Prüfungen ergeben 0.
- **Soll:** Das Skript erkennt Duplikate und Cross-Tenant-Verweise, die das Schema nicht schon verhindert, und läuft nach Last- und Konkurrenzläufen.
- **Ist:** Nur constraint-gestützte Duplikate und ein Teil der Mandantenbeziehungen werden geprüft; das Skript läuft nur in zwei Go-Tests.
- **Nutzerwirkung:** Regressionen über Wartungs- oder Owner-Pfade passieren das Integritäts-Gate (App-Schreibzugriffe schützt RLS).
- **Ursache (belegt):** Skriptdesign.
- **Empfehlung:** Prüfungen ergänzen (IP-/Hostname-Duplikate, CI→location/site/room/type, rack→room), Schritt in load.yml, Negativ-Selbsttest.
- **Retest (verbindlich):** Go-Test injiziert jede Verletzungsklasse → count > 0; load.yml zeigt den Integritätsschritt mit 0.
- **Kodierender Test:** keiner

<a id="f-282"></a>
### F-282 (P3) – Ohne RETICORA_NATS_URL nimmt der HTTP-Ingest keine Identitätssperren; zwei Instanzen können Geräte mit schwacher Identität duplizieren

- **Betroffene WPs:** WP-207 · **Komponente:** backend/cmd/server/main.go:251; backend/internal/discovery/lanes.go:115, consumer.go:655 · **Quelle:** Audit/Verifikation WP-207
- **Reproduktion:** Kontrollfall lanes_integration_test.go:229-233: zwei Instanzen ohne Queue → 2 CIs für ein Gerät mit IP+sysObjectID (der Test prüft das so).
- **Soll:** Doppel-Ingest erzeugt in keinem unterstützten Multi-Instanz-Deployment Duplikate.
- **Ist:** Ohne NATS sind Duplikate möglich (dokumentiert in ingest-spuren.md:27-30); das ausgelieferte Compose setzt NATS.
- **Nutzerwirkung:** Fehlkonfigurierte Multi-Instanz-Deployments erzeugen doppelte CIs.
- **Ursache (belegt):** Sperren sind an den Queue-Pfad gekoppelt.
- **Empfehlung:** Im Produktionsmodus ohne RETICORA_NATS_URL den Start verweigern bzw. laut warnen, oder auch auf dem HTTP-Pfad sperren.
- **Retest (verbindlich):** Config-Test: production ohne NATS-URL scheitert an der Validierung.
- **Kodierender Test:** keiner

<a id="f-283"></a>
### F-283 (P3) – CI-Zusammenfassung des DB-Jobs scheitert nur an einer bestimmten Skip-Meldung

- **Betroffene WPs:** WP-003 · **Komponente:** .github/workflows/ci.yml:152-167 (Grep-Muster :164 'TEST_DATABASE_URL not set') · **Quelle:** Audit (B01) + Verifikation (Probe)
- **Reproduktion:** Summary-Schritt ci.yml:152-167 in ein Skript kopieren (SP/work/verify-B01/wp003/summary.sh), test-db.json mit Skip von internal/jobs TestQueueIntegration und Ausgabe 'TEST_NATS_URL not set' einspeisen → 'passed=1 failed=0 skipped=1', exit 0.
- **Soll:** Jeder Skip eines infrastrukturabhängigen Tests im DB-Job lässt den Job scheitern, außer er steht auf einer expliziten Allow-List.
- **Ist:** Nur 'TEST_DATABASE_URL not set' scheitert; still durchgelassen würden 'TEST_REDIS_URL not set' (identity/revocation_integration_test.go:21), 'TEST_NATS_URL not set' (jobs/queue_integration_test.go:31, discovery/consumer_integration_test.go:197), 'TEST_DATABASE_URL and TEST_REDIS_URL required…' (entitlement/enforce_integration_test.go:107).
- **Nutzerwirkung:** Bei still defektem Redis-/NATS-Service-Setup würden Session-Revocation-, Ingest- und Entitlement-Integrationstests übersprungen und der Job bliebe grün (geringe Wahrscheinlichkeit, NATS-Start wird explizit geprüft).
- **Ursache (belegt):** Hart kodiertes Grep-Muster in ci.yml:164.
- **Empfehlung:** Bei skipped > 0 in db-tests.txt scheitern, außer der Test steht auf einer expliziten Allow-List (z. B. Login-Rollen-Guard isolation_integration_test.go:168); Zusammenfassung nach scripts/ci/ verschieben und testen.
- **Retest (verbindlich):** Synthetisches JSON mit NATS-Skip in das Summary-Skript → exit != 0; Skip 'TEST_DATABASE_URL not set' → exit != 0; 0 Skips → exit 0; Allow-List-Skip → exit 0.
- **Kodierender Test:** keiner (nur Probe SP/work/verify-B01/wp003/summary.sh)

<a id="f-284"></a>
### F-284 (P3) – Ablauf von Scan-Ausnahmen wird nicht gegen den SEC-11-SLA-Deckel geprüft; Trivy behandelt Einträge ohne exp: als dauerhaft

- **Betroffene WPs:** WP-004 · **Komponente:** .github/workflows/ci.yml:393-405 (awk-Filter exp >= heute), :486 (--ignorefile security/scan-exceptions.txt); security/scan-exceptions.txt; docs/security/abhaengigkeitsscans.md:39-45 · **Quelle:** Audit (B01) + Verifikation (Probe)
- **Reproduktion:** Wörtliche Auswertung aus ci.yml:393-405 (SP/work/verify-B01/wp004/go/eval.sh) auf Bericht mit erreichbarem GO-2099-0001 und Ausnahme 'GO-2099-0001 exp:2099-01-01 # far' → PASS (exit 0). Dieselbe Datei geht als --ignorefile an Trivy; im .trivyignore-Format ist eine Zeile ohne exp: eine unbefristete Ausnahme (dokumentiertes Trivy-Verhalten, hier nicht ausgeführt).
- **Soll:** Eine Ausnahme gilt höchstens 7 Tage (Critical) bzw. 30 Tage (High) ab Erstmeldung; ein Eintrag ohne exp: unterdrückt nichts (abhaengigkeitsscans.md:39-41).
- **Ist:** Jedes zukünftige Datum wird akzeptiert; für Trivy würde ein Eintrag ohne exp: eine CVE dauerhaft unterdrücken; Durchsetzung nur per Review (Doku :45).
- **Nutzerwirkung:** Schwachstellen können versehentlich weit über das SLA hinaus oder unbefristet unterdrückt werden.
- **Ursache (belegt):** awk-Filter vergleicht nur exp >= heute; gemeinsames Dateiformat überlässt Einträge ohne exp: der Trivy-Standardsemantik (govulncheck-Teil per Probe belegt, Trivy-Teil nach Dokumentation).
- **Empfehlung:** CI-Vorprüfung von security/scan-exceptions.txt: jede Nicht-Kommentarzeile mit exp:, since:<Datum> und Severity; exp <= since + 7 Tage (Critical) bzw. + 30 Tage (High); alternativ exp auf heute + 30 Tage deckeln.
- **Retest (verbindlich):** Shell-Test (z. B. tests/scan-exceptions.test.sh): Ausnahme ohne exp: oder mit exp > 30 Tage → Vorprüfung scheitert; gültige Zeile → besteht.
- **Kodierender Test:** keiner (nur Probe SP/work/verify-B01/wp004/go/eval.sh)

<a id="f-285"></a>
### F-285 (P3) – README überzeichnet G1-Blockadeumfang und Vollständigkeit der Traceability

- **Betroffene WPs:** WP-005 · **Komponente:** README.md:352 · **Quelle:** Audit (B01) + Verifikation
- **Reproduktion:** README.md:352: 'Jede Anforderung ist … rückführbar … die Vollständigkeit aller [B]/[Pn]/[A]-IDs wird berichtet und ist ab G1 blockierend'. TestTraceabilityFile blockiert nur [B]/[Q] (MissingForGate); [Pn]/[A] nur mit TRACEABILITY_REQUIRE_COMPLETE=1, das kein Workflow setzt; Verifier-Lauf besteht mit '79 without a row'.
- **Soll:** Doku entspricht der implementierten Regel (E-30, verfeinert durch WP-213: [B]/[Q] blockierend ab G1).
- **Ist:** README behauptet vollständige Rückführbarkeit und breitere Blockade als implementiert.
- **Nutzerwirkung:** Irreführende Aussage zu Gate-Regel und Abdeckung.
- **Ursache (belegt):** Formulierung nach Einführung von RequiredForGate nicht angepasst.
- **Empfehlung:** Umformulieren: '[B]/[Q] blockierend seit G1; [Pn]/[A] berichtet (derzeit 79 IDs ohne Zeile)'.
- **Retest (verbindlich):** Doku-Review: README.md:352 entspricht MissingForGate/RequiredForGate.
- **Kodierender Test:** keiner

<a id="f-286"></a>
### F-286 (P3) – WP-Bereiche in PR-Titeln werden vom Traceability-Job nicht expandiert

- **Betroffene WPs:** WP-005 · **Komponente:** .github/workflows/ci.yml:210-213 (grep -oE 'WP-[0-9]{3}'); backend/internal/traceability/traceability.go:418-428 WorkPackagesIn · **Quelle:** Audit (B01) + Verifikation
- **Reproduktion:** Shell aus ci.yml:210-213 mit PR_TITLE='G1-Nachbesserung: WP-332 – WP-335 (NFR-02, NFR-03) (#64)' → 'WP-332 WP-335'; mit 'Gate G1: WP-211 – WP-213 (#62)' → 'WP-211 WP-213' (WP-212 übersprungen). Zeilen der übersprungenen WPs zufällig vorhanden (TRACEABILITY_WPS='WP-212 WP-333 WP-334' → PASS).
- **Soll:** Alle WPs eines PRs werden auf ihre geplanten Zeilen geprüft.
- **Ist:** Zwischen-WPs werden übersprungen.
- **Nutzerwirkung:** Fehlende geplante Zeilen von PRs mit Bereichstitel könnten unbemerkt bleiben.
- **Ursache (belegt):** Grep in ci.yml und Regex in WorkPackagesIn erkennen nur wörtliche Tokens.
- **Empfehlung:** 'WP-a – WP-b' und 'WP-a bis WP-b' in WorkPackagesIn expandieren und in CI nutzen, oder explizite Listen in der Zeile 'Arbeitspakete:' verlangen.
- **Retest (verbindlich):** TestWorkPackagesIn mit Bereichseingabe liefert alle Mitglieder (WP-332 – WP-335 → 4 WPs; WP-010 bis WP-012 → 3).
- **Kodierender Test:** keiner

<a id="f-287"></a>
### F-287 (P3) – Roundtrip-Test scheitert auf geteilten PostgreSQL-Clustern (clusterweite Rolle reticora_app wird gelöscht)

- **Betroffene WPs:** WP-006 · **Komponente:** backend/migrations/000056_rls_enforcement.down.sql:70 (DROP ROLE IF EXISTS reticora_app); backend/internal/database/migrations_roundtrip_integration_test.go · **Quelle:** Audit (B01) + Verifikation (privater Cluster)
- **Reproduktion:** Privater Cluster mit rt2 (leer) und rt3 (auf 138 migriert): TEST_DATABASE_URL=…/rt2 go test -run 'TestMigrationsRoundtrip$' ./internal/database/ → FAIL nach 3.81s 'apply 000056_rls_enforcement.down.sql: ERROR: role reticora_app cannot be dropped because some objects depend on it (SQLSTATE 2BP01)' (SP/work/verify-B01/wp006/roundtrip-shared.log); auf dem Audit-Cluster in zurückgerollter Transaktion bestätigt ('614 objects in database audit_db_01').
- **Soll:** Entwickler- und CI-Testwerkzeuge funktionieren auf jeder migrierten Testdatenbank.
- **Ist:** Nur auf dediziertem Cluster lauffähig (wie in CI); bereits bekannt in docs/plan/implementierungsplan.md:10137; Test hinterlässt die DB auf Version 55.
- **Nutzerwirkung:** make test-db bzw. make migrate-roundtrip auf geteilten Clustern nicht nutzbar; Produktionswirkung gering (down auf 0 ist kein Betriebspfad).
- **Ursache (belegt):** Eine clusterweite Rolle wird in einer datenbankbezogenen Migration verwaltet.
- **Empfehlung:** Rollen-Drop bedingt machen (z. B. überspringen, wenn pg_shdepend Abhängigkeiten in anderen DBs zeigt) oder Roundtrip in temporärem dediziertem Cluster/Container ausführen; im Makefile dokumentieren.
- **Retest (verbindlich):** TestMigrationsRoundtrip auf einem Cluster mit zwei migrierten Datenbanken → PASS, und die zweite DB akzeptiert weiterhin SET ROLE reticora_app.
- **Kodierender Test:** keiner

<a id="f-288"></a>
### F-288 (P3) – Health-Prüfung wertet nur den ersten Container je Dienst aus; beendetes Replikat eines skalierten Dienstes besteht

- **Betroffene WPs:** WP-092 · **Komponente:** tests/install-smoke-health.sh:67 ('[.[] | select(.Service == $s)] | first') · **Quelle:** Verifikation (B12, Real-Compose-Probe)
- **Reproduktion:** SP/work/verify-B12/health/run.sh Fall C: Compose-Projekt 'web' (redis:7-alpine) mit --scale web=2, 'docker stop verifyb12health-web-2', dann tests/install-smoke-health.sh <dir> .env → 'OK: 1 services running and healthy', rc=0 (run.log).
- **Soll:** FAIL mit rc=1, weil ein Container eines erwarteten Dienstes beendet ist.
- **Ist:** rc=0; nur verifyb12health-web-1 wird ausgewertet.
- **Nutzerwirkung:** Gering für den ausgelieferten Installer (keine skalierten Dienste); künftig skalierte Dienste oder Betreiber-Overrides mit Replikaten könnten ein abgestürztes Replikat im Smoke-Gate verbergen.
- **Ursache (belegt):** Auswahl 'first' statt Auswertung aller Container des Dienstes.
- **Empfehlung:** Alle Nicht-One-off-Container je Dienst auswerten (com.docker.compose.oneoff=True ausschließen), FAIL wenn einer nicht running/healthy ist; 'missing' beibehalten, wenn keiner existiert.
- **Retest (verbindlich):** Docker-gestützter Repo-Test: scale web=2, einer gestoppt → rc=1; beide laufend → rc=0; beendeter One-off-Container neben laufendem Dienst → rc=0.
- **Kodierender Test:** keiner

<a id="f-289"></a>
### F-289 (P3) – Offline-Detector-Schalter umgeht die Worker-Konvention; dokumentierter Konventionsname wird stillschweigend ignoriert, Schalter in Compose/K8s nicht setzbar

- **Betroffene WPs:** WP-098 · **Komponente:** backend/internal/config/config.go:178-180, :284, :289-291; backend/cmd/server/main.go:496-499; docs/ops/workers.md:113; docs/README.md:890; deploy/docker-compose/docker-compose.yml, deploy/k8s/base/deployment.yaml (kein OFFLINE_DETECTOR) · **Quelle:** Audit (B12) + Verifikation (Binary-Probe)
- **Reproduktion:** HEAD-Binary mit aktivem Scheduler, übrige Worker aus, audit_db_15 (scheduler_run-Zeile ci.offline_detector vor jedem Lauf löschen, 25 s warten): RETICORA_OFFLINE_DETECTOR_ENABLED=false → kein Lauf; RETICORA_WORKER_OFFLINE_DETECTOR_ENABLED=false (Konvention; workers.md:113 'geplant') → Detector läuft (last_started_at 16:43:41), keine Warnung (SP/work/verify-B12/probe-offline-*.log). grep OFFLINE_DETECTOR in docker-compose.yml und deployment.yaml → kein Treffer; server-Dienst ohne env_file.
- **Soll:** Schalter RETICORA_WORKER_OFFLINE_DETECTOR_ENABLED in config.WorkerNames, in Compose/K8s durchgereicht, konsistent dokumentiert und getestet – oder dokumentierte Ausnahme, die durchgereicht wird.
- **Ist:** Funktionierender Schalter heißt RETICORA_OFFLINE_DETECTOR_ENABLED (nur docs/README.md:890), in der Compose-Installation nicht per .env setzbar, nicht namentlich getestet; Konventionsname ignoriert ohne Warnung.
- **Nutzerwirkung:** Betreiber, die docs/ops/workers.md folgen, können den Offline-Detector nicht abschalten; in der Standard-Compose-Installation nur durch Editieren von docker-compose.yml.
- **Ursache (belegt):** WP-157 führte ein separates cfg.OfflineDetector ein; config.go:289-291 liest nur WorkerNames-Schlüssel; workers.md nicht aktualisiert.
- **Empfehlung:** 'offline_detector' unter der Konvention registrieren (Scheduler-Job über cfg.WorkerEnabled gaten), alten Namen als Alias behalten, in Compose/K8s durchreichen, workers.md aktualisieren, Warnung bei unbekannten RETICORA_WORKER_*_ENABLED.
- **Retest (verbindlich):** Config-Test: RETICORA_WORKER_OFFLINE_DETECTOR_ENABLED=false → Zeitplan ci.offline_detector nicht registriert; statischer Test: jeder Schalter in docker-compose.yml und deployment.yaml; unbekanntes RETICORA_WORKER_FOO_ENABLED → Warnung oder Validate-Fehler.
- **Kodierender Test:** keiner

<a id="f-290"></a>
### F-290 (P3) – Backup-Scheduler reticora-backup.sh stürzt bei RETICORA_BACKUP_HOUR=08/09 ab und plant ungültige Werte stillschweigend auf Mitternacht

- **Betroffene WPs:** WP-100 · **Komponente:** deploy/docker-compose/postgres/reticora-backup.sh:38-43 (seconds_until_hour, $(( HOUR - … )) unter set -eu) · **Quelle:** Audit (B13) + Verifikation (busybox-Ausführung)
- **Reproduktion:** In timescale/timescaledb:2.17.2-pg16 (busybox sh) seconds_until_hour aus reticora-backup.sh:38-43 unter set -eu auswerten: HOUR=08 oder 09 → 'sh: arithmetic syntax error'; HOUR=24 und abc → gleiche Wartezeit wie 0 (Mitternacht), kein Fehler (SP/work/verify-B13/scheduler-hour-busybox.log).
- **Soll:** 0–23 beim Start validieren und '08' korrekt parsen.
- **Ist:** Bei 08/09 endet die Laufschleife nach dem Start; nach Neustart existiert bereits ein Backup, der Dienst crash-loopt ohne tägliche Basisbackups, Health wird erst nach 26 h unhealthy. Bei 24/abc laufen Backups unbemerkt um 00:00 UTC.
- **Nutzerwirkung:** Tägliche Basisbackups enden nach einem plausiblen Konfigurationswert (WAL-Archivierung läuft weiter, das Restore-Fenster wächst) oder laufen zu unerwarteter Zeit.
- **Ursache (belegt):** Führende Null wird als ungültige Oktalzahl interpretiert; nicht numerischer Wert ergibt 0; keine Validierung.
- **Empfehlung:** Führende Nullen entfernen, 0..23 beim Start validieren und mit klarer Meldung beenden.
- **Retest (verbindlich):** backup-config.test.sh unter busybox sh und dash: HOUR=0/08/23 → 0 < secs <= 86400; HOUR=24/-1/abc → exit != 0 beim Start.
- **Kodierender Test:** keiner
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – komponente: „deploy/docker-compose/reticora-backup.sh:38-43 (seconds_until_hour, $(( HOUR - … )) unter set -eu)“ → „deploy/docker-compose/postgres/reticora-backup.sh:38-43 (seconds_until_hour, $(( HOUR - … )) unter set -eu)“. Den angegebenen Pfad gibt es nicht (sed: No such file or directory). Das Skript liegt unter deploy/docker-compose/postgres/reticora-backup.sh, und seconds_until_hour steht dort tatsächlich in Zeilen 38-43. Mit dem falschen Pfad wäre ein Retest nicht direkt auffindbar.

<a id="f-291"></a>
### F-291 (P3) – pgBackRest-Tarball ohne Prüfsumme; Basisimage ungepinnt (auch im signierten Release-Image)

- **Betroffene WPs:** WP-100 · **Komponente:** deploy/docker-compose/postgres/Dockerfile:7 (ARG TIMESCALE_IMAGE=timescale/timescaledb:latest-pg16), :12-17 (curl | tar xz ohne sha256); .github/workflows/release.yml:127-129 (Build ohne build-args) · **Quelle:** Audit (B13) + Verifikation
- **Reproduktion:** Dockerfile:7 und :12-17 lesen; release.yml:127-129 baut reticora-postgres ohne build-args, das signierte Release-Image schwimmt daher ebenfalls auf latest-pg16.
- **Soll:** Reproduzierbares, integritätsgeprüftes DB-Image (im Sinne von TEC-15).
- **Ist:** Schwimmende Basis und ungeprüfter Download.
- **Nutzerwirkung:** Lieferketten- und Reproduzierbarkeitsrisiko; ein Neubau kann die TimescaleDB-Version unbemerkt ändern.
- **Ursache (belegt):** Kein Pinning, keine Prüfsumme.
- **Empfehlung:** Basis per Version oder Digest pinnen (2.17.2-pg16 wie in CI) und sha256 des Tarballs prüfen.
- **Retest (verbindlich):** Statischer Test assertiert Digest/sha256 im Dockerfile; CI-Image-Build besteht.
- **Kodierender Test:** keiner

<a id="f-292"></a>
### F-292 (P3) – Dokumentierte Restore-Messwerte stammen aus einem Nicht-Default-Lauf (30 s) auf Spielzeugdatenmenge

- **Betroffene WPs:** WP-101 · **Komponente:** docs/backup-dr.md:139-140, :150-157; tests/restore-pitr.sh:39 · **Quelle:** Audit (B13) + Verifikation
- **Reproduktion:** docs/backup-dr.md:150 'Measured on 2026-10-12 with a 50,000 row workload and 30 s of writes' (RPO 52 s, RTO 5 s); tests/restore-pitr.sh:39 Default 420 s; backup-dr.md:139-140 behauptet 7 Minuten Schreiblast.
- **Soll:** Doku-Tabelle entspricht dem committeten Nachweis und enthält eine RTO-Aussage für realistische Datenmenge.
- **Ist:** Veraltete Nicht-Default-Werte.
- **Nutzerwirkung:** Irreführende NFR-05-Abnahmeevidenz.
- **Ursache (belegt):** Messung nach Änderung des Defaults nicht wiederholt.
- **Empfehlung:** Mit Defaults neu messen, aus dem CI-Artefakt aktualisieren, zusätzlich Lauf mit repräsentativer Größe (z. B. seed -objects 10000).
- **Retest (verbindlich):** Doku-Tabelle entspricht dem neuesten restore-pitr-report.md-Artefakt.
- **Kodierender Test:** keiner

<a id="f-293"></a>
### F-293 (P3) – K8s-Overlays referenzieren Images, die das Release nie veröffentlicht

- **Betroffene WPs:** WP-102 · **Komponente:** deploy/k8s/overlays/prod (images ohne newName, newTag ohne 'v'); .github/workflows/release.yml:20, :150 · **Quelle:** Verifikation (B13, kustomize-Render)
- **Reproduktion:** kustomize build deploy/k8s/overlays/prod → image: reticora-cmdb/server:1.0.0 und reticora-cmdb/postgres:1.0.0 (SP/work/verify-B13/k8s-prod.yaml); release.yml pusht ghcr.io/datahub-chiemgau/reticora-<name>:<v-tag> (Name und Tag abweichend).
- **Soll:** Produktionsmanifeste nutzen die signierten Release-Images.
- **Ist:** Namen und Tags passen nicht; Pods erhalten ImagePullBackOff, sofern der Betreiber nicht auf unsignierte oder selbst gebaute Images umstellt.
- **Nutzerwirkung:** Die signierten Images werden vom K8s-Pfad nicht genutzt.
- **Ursache (belegt):** Kein kustomize-images newName und kein 'v'-Präfix in newTag.
- **Empfehlung:** images newName auf ghcr.io/datahub-chiemgau/reticora-server (und -postgres), newTag v1.0.0, idealerweise per Digest.
- **Retest (verbindlich):** kustomize build overlays/prod zeigt ghcr.io/datahub-chiemgau/reticora-*-Images, die im Release existieren.
- **Kodierender Test:** keiner

<a id="f-294"></a>
### F-294 (P3) – Release-Doku nennt drei Images, der Workflow baut und signiert vier

- **Betroffene WPs:** WP-102 · **Komponente:** docs/README.md:932; .github/workflows/release.yml:127 · **Quelle:** Audit (B13) + Verifikation
- **Reproduktion:** docs/README.md:932 nennt reticora-server, reticora-collector und reticora-frontend; release.yml:127 baut zusätzlich reticora-postgres.
- **Soll:** Doku entspricht der signierten Artefaktmenge.
- **Ist:** Das signierte reticora-postgres-Image ist undokumentiert.
- **Nutzerwirkung:** Betreiber verifizieren oder nutzen das signierte DB-Image möglicherweise nicht.
- **Ursache (belegt):** Doku mit WP-100 nicht aktualisiert.
- **Empfehlung:** docs/README.md um reticora-postgres ergänzen.
- **Retest (verbindlich):** Doku-Review.
- **Kodierender Test:** keiner
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – komponente: „docs/README.md:933; .github/workflows/release.yml:127“ → „docs/README.md:932; .github/workflows/release.yml:127“. In docs/README.md steht die Image-Liste ('It builds the images reticora-server, reticora-collector and reticora-frontend') in Zeile 932, geprüft auf 3a9669a und HEAD. Zeile 933 ist der Syft-SBOM-Satz. Der Fehler um eine Zeile stammt aus dem Input-Befund.
- **Prüfvermerk:** Korrektur der adversarialen Prüfung – reproduktion: „docs/README.md:933 nennt reticora-server, reticora-collector und reticora-frontend; release.yml:127 baut zusätzlich reticora-postgres.“ → „docs/README.md:932 nennt reticora-server, reticora-collector und reticora-frontend; release.yml:127 baut zusätzlich reticora-postgres.“. Gleiche Zeilenkorrektur wie bei komponente: Die Image-Aufzählung steht in docs/README.md:932.

<a id="f-295"></a>
### F-295 (P3) – ReticoraWorkerNoSuccess kann für einen seit Prozessstart nie erfolgreichen Worker nicht feuern

- **Betroffene WPs:** WP-103 · **Komponente:** backend/internal/observability/workers.go:61-66 (last_success nur bei Erfolg); deploy/monitoring/worker-rules.yaml (NoSuccess) · **Quelle:** Verifikation (B13, promtool)
- **Reproduktion:** promtool test rules (SP/work/verify-B13/prom/worker-rules.test.yaml, Test 3) mit up=1 und ohne reticora_worker_last_success_timestamp_seconds-Serie für credential_bundles → kein Alarm.
- **Soll:** Ein ab Start scheiternder Worker wird von NoSuccess erkannt.
- **Ist:** Nur ReticoraWorkerErrors (>5 Fehler in 15 m) kann ihn erkennen, und das setzt eine Fehlermetrik voraus.
- **Nutzerwirkung:** Worker mit langen Zyklen, die ab Boot scheitern, bleiben stumm.
- **Ursache (belegt):** last_success-Serie wird beim Worker-Start nicht initialisiert.
- **Empfehlung:** last_success in StartWorkers auf die Prozessstartzeit initialisieren oder absent()/unless in der Regel nutzen.
- **Retest (verbindlich):** promtool-Test: Worker mit up=1 und ohne Erfolg seit 35 m → NoSuccess feuert.
- **Kodierender Test:** keiner (nur SP/work/verify-B13/prom/worker-rules.test.yaml)

<a id="f-296"></a>
### F-296 (P3) – Collector-Szenario 'Server unerreichbar' ist nur komponentenbasiert, nicht Compose-basiert

- **Betroffene WPs:** WP-104 · **Komponente:** tests/outage/run.sh:101-103 (go test in collector/collectorcmd und edgecore/buffer) · **Quelle:** Audit (B13) + Verifikation
- **Reproduktion:** run.sh:101-103 führt nur go test der Spool-/Buffer-Komponenten aus; kein laufender Collector-Container gegen einen gestoppten Server.
- **Soll:** Compose-basierte Simulation der Server-Unerreichbarkeit für einen laufenden Collector (WP-104 AK1).
- **Ist:** Nur Unit-Nachweis für Spool und Backpressure (PASS).
- **Nutzerwirkung:** Der integrierte Spool-Flush nach einem echten Ausfall ist unbelegt.
- **Ursache (belegt):** Designentscheidung.
- **Empfehlung:** Enrolled Collector-Container betreiben, server/nats-edge 60 s stoppen, neu starten und Ingest aller Datensätze assertieren.
- **Retest (verbindlich):** Neues Szenario 'collector' in tests/outage/ besteht mit Datensatzzählern (Spool steigt, nach Neustart alles ingestiert, Verlustzähler 0).
- **Kodierender Test:** keiner

<a id="f-297"></a>
### F-297 (P3) – Internes Review bewertet die Critical/High-Advisories der Dev-Abhängigkeiten nicht

- **Betroffene WPs:** WP-209 · **Komponente:** docs/security/review-g1.md (Scanergebnisse :98-113); .github/workflows/ci.yml:430 (Dev-Abhängigkeiten nur berichtet) · **Quelle:** Audit (B27) + Verifikation
- **Reproduktion:** npm-audit-all.json (SP/work/verify-B01/wp004): critical=2 (tinypool GHSA-5gmw-xhrv-c9v3/GHSA-85c8-ppgw-ccpr, vitest GHSA-82fw-gwwq-j7x9), high=10; im Review nicht erwähnt.
- **Soll:** Dev-Funde werden als Lieferkettenrisiko (Threat T19) bewertet und über eine befristete Ausnahme oder Aktualisierung geführt.
- **Ist:** Nicht bewertet; CI meldet sie nur.
- **Nutzerwirkung:** Kein direkter Produktionseinfluss; das CI-Lieferkettenrisiko ist unbewertet.
- **Ursache (belegt):** Policy 'Dev nur berichtet' ohne Review-Bewertung.
- **Empfehlung:** Dev-Critical über eine Ausnahme mit Ablaufdatum führen oder vitest/tinypool aktualisieren; Bewertung im Review ergänzen.
- **Retest (verbindlich):** review-g1.md enthält die Bewertung; npm audit (alle Abhängigkeiten) ohne unbewertete Critical.
- **Kodierender Test:** keiner

<a id="f-298"></a>
### F-298 (P3) – ABN-01 (e)-Abnahmeprotokoll stützt sich auf gemockten Lauf; Real-Stack-Nachweis nicht referenziert

- **Betroffene WPs:** WP-212 · **Komponente:** docs/acceptance/abn-01.md:69-102 ('eingespielt' :90); frontend/e2e/conflict.spec.ts:80 (mockBackend) · **Quelle:** Audit (B27) + Verifikation (Real-Stack-Audit-Spec)
- **Reproduktion:** conflict.spec.ts:80 mockBackend liefert vorgefertigte override_conflict-Reviews ohne Ingest; abn-01.md:90 nennt für Schritt 2 'eingespielt'. Die Real-Stack-Spec frontend/e2e-real/discovery-override.spec.ts D03–D05 ist grün (SP/work/verify-discovery-override/full.log), wird im Protokoll aber nicht genannt.
- **Soll:** Protokoll (e) verweist auf einen Lauf gegen echte Dienste mit echtem abweichendem Ingest und accept/dismiss inklusive DB-/API-Assertions.
- **Ist:** Nur gemockter Lauf und separater Backend-Test referenziert.
- **Nutzerwirkung:** Gering: Funktion ist real belegt (D05); nur die Nachweiskette des eingefrorenen Protokolls ist schwach.
- **Ursache (belegt):** E2E-Suite des Protokolls läuft ohne echtes Backend; die Real-Stack-Spec entstand erst im Audit.
- **Empfehlung:** Protokoll (e) um den Real-Stack-Lauf ergänzen (mit Artefaktdaten srv-abn-01/02) und Prüfsumme neu erzeugen.
- **Retest (verbindlich):** abn-01.md (e) verweist auf grünen Real-Stack-Lauf; sha256sum -c docs/acceptance/artefakte/SHA256SUMS OK.
- **Kodierender Test:** keiner

<a id="f-299"></a>
### F-299 (P3) – Onboarding-Impact-Schritt prüft keine betroffenen CIs

- **Betroffene WPs:** WP-212 · **Komponente:** frontend/e2e/onboarding.spec.ts:292-297 · **Quelle:** Audit (B27) + Verifikation
- **Reproduktion:** onboarding.spec.ts:292-297: für impact = apiGet('/impact/{id}') nur expect(Array.isArray(impact.impacted)).toBe(true).
- **Soll:** Protokoll-Schritt 7 'betroffene CIs zum gewählten CI' wird inhaltlich assertiert.
- **Ist:** Auch eine leere Liste besteht; das Protokoll führt den Schritt als bestanden.
- **Nutzerwirkung:** Ein defekter Impact würde die Abnahme (d) nicht scheitern lassen.
- **Ursache (belegt):** Die simulierte Flotte erzeugt keine garantierten Beziehungen.
- **Empfehlung:** Vor dem Impact eine Beziehung anlegen (oder Simulationstopologie nutzen) und impacted.length >= 1 inklusive erwarteter ID prüfen; Prüfsumme neu erzeugen.
- **Retest (verbindlich):** onboarding.spec.ts real grün mit verschärfter Assertion (impacted enthält die erwartete CI-ID).
- **Kodierender Test:** keiner

<a id="f-300"></a>
### F-300 (P3) – rep01-GATE-01-Prüfung erkennt Formatvarianten, Anhangzeilen und offene High-Befunde nicht

- **Betroffene WPs:** WP-210, WP-213 · **Komponente:** tests/rep01-structure.test.sh:57-62 (Regex :60: grep -E ohne -i, exaktes Wort 'Critical', nur SR-/PT-IDs, Anhang nicht geprüft) · **Quelle:** Audit (B27) + Verifikation (Scratch-Kopie) + Defekttest-Welle T5 (in Synthese wiederholt)
- **Reproduktion:** Scratch-Kopie SP/work/verify-B27/rep01: '| PT-01 | High | offen |', '| PT-03 | **Critical** | offen |', '| PT-04 | Kritisch | offen |', '| PT-05 | critical | offen |' → je 47/0; nur '| PT-02 | Critical | offen |' → 46/1 (rep01-negative.log). Kodiert: tests/audit/g1-gate-evidence.test.sh rep01_pt_bold_critical, rep01_pt_kritisch, rep01_pt_lowercase, rep01_pt_high, rep01_pt_hoch, rep01_sr_bold_critical, rep01_annex_critical FAIL ('gate checker must fail … got: pass'); Kontrollen rep01_control, rep01_pt_critical, rep01_pt_fixed, rep01_pt_closed ok; in dieser Synthese erneut ausgeführt.
- **Soll:** GATE-01 erkennt jede offene Critical-Zeile unabhängig von Formatierung, inklusive Review-Anhang; GATE-03 (Pentest High/Critical) wird ebenfalls geprüft.
- **Ist:** Prüfung textuell eng und umgehbar.
- **Nutzerwirkung:** Eine nachgereichte Befundliste mit offenem High oder anders formatiertem Critical lässt das Gate nicht scheitern (heute keine solchen Zeilen vorhanden, daher P3).
- **Ursache (belegt):** Strikter Regex ohne Normalisierung; GATE-03 nicht automatisiert.
- **Empfehlung:** Severity normalisieren (Fettdruck, Groß-/Kleinschreibung, Kritisch/Hoch), alle Befundtabellen inkl. Anhang prüfen, eigene GATE-03-Prüfung für High und Critical der PT-Zeilen.
- **Retest (verbindlich):** ./tests/audit/g1-gate-evidence.test.sh: alle rep01_*-Fälle ok (Varianten → Gate-Checker FAIL, behoben/geschlossen → PASS); tests/rep01-structure.test.sh ohne Probezeilen weiterhin grün.
- **Kodierender Test:** tests/audit/g1-gate-evidence.test.sh – rep01_* (7 fails-as-expected, 4 Kontrollen ok).

<a id="f-301"></a>
### F-301 (P3) – KI-Chunk-Indexer (Add-on 'ai') ist in der G1-Voreinstellung aktiv

- **Betroffene WPs:** WP-213 · **Komponente:** backend/cmd/server/main.go:354-356 (KI-Decorator unbedingt), :404 (ParseModules erst danach); backend/internal/ai/ci_indexer.go IndexDocument (bei !mayEmbed immer repo.UpsertChunk) · **Quelle:** Audit (B27) + Verifikation (Probe)
- **Reproduktion:** Probe TestVerifyB27AIChunkInG1 (zz_verify_b27_sr11_test.go): Router wie main.go, kein Modul aktiv; POST /api/v1/cis → 201, danach ai_chunk-Zeilen für das CI: 1 (SP/work/verify-B27/ai-chunk.log).
- **Soll:** Nach E-03 ist das Add-on aus; ohne Modul 'ai' entstehen keine KI-Retrieval-Chunks.
- **Ist:** Jede CI-Änderung schreibt in G1 lexikalische Chunks in ai_chunk; KI-Routen sind nicht registriert.
- **Nutzerwirkung:** Zusätzliche Schreiblast im Ingest-Pfad (NFR-03-Reserve, SR-08) und eine Datenkopie in einem abgeschalteten Modul; keine Exposition über die API.
- **Ursache (belegt):** Decorator-Verdrahtung in main.go ignoriert das Modul-Gate.
- **Empfehlung:** Decorator nur bei modules.Enabled('ai') verdrahten.
- **Retest (verbindlich):** Integrationstest/Probe: G1-Voreinstellung plus CI anlegen → count(ai_chunk) = 0; mit RETICORA_MODULES=ai → > 0.
- **Kodierender Test:** keiner (nur Probe)

<a id="f-302"></a>
### F-302 (P3) – POST /locations beantwortet doppelte Namen mit generischem 409 ohne Feldbezug

- **Betroffene WPs:** WP-107 · **Komponente:** backend/internal/locations/handler.go:146-163, pg_repository.go:274-293 · **Quelle:** Real-Stack-E2E (L02, spec-unclear) + Verifikation (B14)
- **Reproduktion:** /locations 'Standort anlegen' zweimal mit gleichem Namen/Client → 409 {detail:'a record with the same unique value already exists'} ohne field; POST /sites liefert field=name.
- **Soll:** 409 mit field name (WP-107 'Konflikte liefern 409 mit Feldbezug').
- **Ist:** Kein Feld; Dialog zeigt englischen Generiktext, Name-Feld nicht markiert; Duplikat wird abgewiesen.
- **Nutzerwirkung:** Unpräzise Rückmeldung im einzigen UI-Anlagepfad für Standorte; kein Datenverlust.
- **Ursache (belegt):** writeError ohne Fall für Unique-Constraints → api.WriteRepoError (dberrors.go:50-52). Bindung des Kriteriums an diesen Endpunkt laut E2E-Verifikation spec-unklar (PO-Entscheid).
- **Empfehlung:** site/building/room/rack-Unique-Constraints auf FieldError name mappen (api.FieldDBError).
- **Retest (verbindlich):** cd frontend && npx playwright test -c e2e-real/playwright.real.config.ts locations-racks -g 'L02' → pass ohne Soft-Fehler; Integrationstest 409 field=name für alle vier Ebenen.
- **Kodierender Test:** frontend/e2e-real/locations-racks.spec.ts L02 (Soft-Assertion Z.434 FAIL)

<a id="f-303"></a>
### F-303 (P3) – Signup-Verifikation scheitert mit 500, wenn 20 Slug-Kandidaten belegt sind

- **Betroffene WPs:** WP-111 · **Komponente:** backend/internal/signup/pg_repository.go:174,215; service.go:523-525 · **Quelle:** Verifikation (B14)
- **Reproduktion:** Orgs b14-probe … b14-probe-20 anlegen, Signup 'B14 Probe', verify → 500 'signup failed' (TestB14ProbeSlugExhaustion).
- **Soll:** Signup mit freiem eindeutigem Slug oder klarer 409.
- **Ist:** 500, Anfrage bleibt pending; nicht-lateinische Namen teilen Basis 'org'.
- **Nutzerwirkung:** Ab 20 gleichen Basis-Slugs scheitern weitere Signups nach der Verifikation.
- **Ursache (belegt):** Kappung bei 20 Versuchen, plain error → 500.
- **Empfehlung:** Fallback auf Zufallssuffix, Erschöpfung → 409, ggf. Transliteration.
- **Retest (verbindlich):** Probe/Integrationstest: 20 belegte Slugs → 201 mit eindeutigem Slug.
- **Kodierender Test:** keiner

<a id="f-304"></a>
### F-304 (P3) – Signup-Limit je IP als festes Uhrzeit-Stundenfenster: 10 Signups in 61 s möglich

- **Betroffene WPs:** WP-112 · **Komponente:** backend/internal/signup/protect.go:187-189, writeProtectionError · **Quelle:** Verifikation (B14)
- **Reproduktion:** Protection.Check von einer IP 6× um 10:59:30 und 6× um 11:00:31 → 10 akzeptiert (probe-signup-window.log).
- **Soll:** Höchstens 5 je IP in jeder Stunde; Retry-After passend.
- **Ist:** Festes Fenster; Retry-After konstant 3600.
- **Nutzerwirkung:** Missbrauchsschutz an Stundengrenzen halbiert; legitime Nutzer warten unnötig.
- **Ursache (belegt):** ipKey Unix()/3600.
- **Empfehlung:** Gleitendes Fenster, Retry-After aus ältestem Zählereintrag; oder Näherung dokumentieren.
- **Retest (verbindlich):** Unit mit injizierter Uhr: max. 5 je 3600-s-Spanne; Retry-After ≤ Restzeit.
- **Kodierender Test:** keiner

<a id="f-305"></a>
### F-305 (P3) – Einladung akzeptiert Client-Rolle mit Org-Scope und erzeugt Nutzer ohne wirksame Rechte

- **Betroffene WPs:** WP-114 · **Komponente:** backend/internal/user/invitation.go:235-262 · **Quelle:** Verifikation (B14)
- **Reproduktion:** client_technician mit scope_type org → 201, accept 201, AccessGrants 0 (TestB14ProbeInvitationRoleScopeMismatch).
- **Soll:** 422 field scope_type bei Mismatch.
- **Ist:** Still akzeptiert.
- **Nutzerwirkung:** Verwirrendes Onboarding, Nutzer sieht nichts.
- **Ursache (belegt):** role.scope wird nicht mit scope_type verglichen.
- **Empfehlung:** role.scope gegen scope_type validieren (422).
- **Retest (verbindlich):** Integrationstest: Mismatch → 422, keine Zeile.
- **Kodierender Test:** keiner

<a id="f-306"></a>
### F-306 (P3) – Instanzdefinitionen nicht in attribute_definition übernommen (zweite Definitionsquelle)

- **Betroffene WPs:** WP-116 · **Komponente:** ci_instance_field_definition; backend/internal/citype/resolver.go:52-62 · **Quelle:** Verifikation (B15)
- **Reproduktion:** psql: ci_instance_field_definition neben attribute_definition; Resolver komponiert beide; Phase-2-Plan WP-256 (implementierungsplan.md:811) behält die Tabelle.
- **Soll:** Kriterium 1/DB-03: einzige Quelle oder dokumentierter Entscheid.
- **Ist:** Zwei Tabellen und APIs speisen die Validierung; kein E-nn.
- **Nutzerwirkung:** Gering: zwei Definitionsmodelle.
- **Ursache (belegt):** Designentscheid Commit f692492 ohne Entscheidungsdokument.
- **Empfehlung:** E-nn dokumentieren und Kriterium anpassen oder migrieren.
- **Retest (verbindlich):** Entscheidungsverweis plus Resolver-Test bzw. Schema-Assertion.
- **Kodierender Test:** keiner

<a id="f-307"></a>
### F-307 (P3) – attribute_definition.validation akzeptiert den MET-45-Regel-AST nicht; OpenAPI beschreibt Form nicht

- **Betroffene WPs:** WP-116 · **Komponente:** backend/internal/citype/attribute_definition.go:46,71; api/openapi.yaml AttributeDefinition · **Quelle:** Verifikation (B15)
- **Reproduktion:** POST /api/v1/attribute-definitions validation {op:range,min:1,max:5} → 400 'unknown field op'; {min:1,max:5} → 201 (probe-attrdef.log).
- **Soll:** MET-10: validation als JSON-AST (MET-45), in OpenAPI dokumentiert.
- **Ist:** Legacy-Struktur; OpenAPI additionalProperties:true, Server lehnt unbekannte Keys ab.
- **Nutzerwirkung:** Clients nach rules.schema.json können keine Regeln speichern.
- **Ursache (belegt):** Legacy-Validierungsstruct als Speicher-/API-Format behalten.
- **Empfehlung:** AST annehmen/speichern oder Legacy-Entscheid mit OpenAPI-Schema.
- **Retest (verbindlich):** API-Test: AST-POST 201 und Regel wirkt; ungültiger AST 400; OpenAPI-Paritätstest.
- **Kodierender Test:** keiner

<a id="f-308"></a>
### F-308 (P3) – Org-globale Attributdefinitionen werden erzwungen, aber nicht materialisiert/versioniert

- **Betroffene WPs:** WP-119 · **Komponente:** backend/migrations/000107 generate_attribute_schema · **Quelle:** Verifikation (B15)
- **Reproduktion:** Org-Typ v1, org-globale Pflichtdefinition einfügen → Typ bleibt v1 ohne Property (probe-orgglobal-materialization.log).
- **Soll:** Validierungsregelsatz materialisiert und versioniert.
- **Ist:** Nur Systembasis + Typ komponiert.
- **Nutzerwirkung:** Unvollständige Nachvollziehbarkeit bei org-globalen Attributen.
- **Ursache (belegt):** WHERE (organization_id IS NULL AND ci_type_id IS NULL) OR ci_type_id = p_type.
- **Empfehlung:** Entscheid 'Laufzeitschicht' dokumentieren oder Org-Schemarevision.
- **Retest (verbindlich):** Integrationstest oder Entscheidungsverweis.
- **Kodierender Test:** keiner

<a id="f-309"></a>
### F-309 (P3) – ci.schema_version wird bei reinen Typed-Column-Writes nicht aktualisiert

- **Betroffene WPs:** WP-119 · **Komponente:** backend/migrations/000107_ci_schema_version.up.sql:311-324 · **Quelle:** Verifikation (B15)
- **Reproduktion:** psql -f SP/work/verify-B15/schema-version-typed.sql: CI v2, Typ v4, UPDATE ci SET hostname → bleibt v2.
- **Soll:** Write gegen vN speichert vN.
- **Ist:** Trigger nur bei INSERT, ci_type_id- oder attributes-Änderung.
- **Nutzerwirkung:** Gering: veraltete schema_version.
- **Ursache (belegt):** Triggerbedingung ohne Typed Columns.
- **Empfehlung:** Bei jedem manuellen Write aktualisieren.
- **Retest (verbindlich):** PG-Test: nach Typ-Bump PATCH hostname → aktuelle Version.
- **Kodierender Test:** keiner

<a id="f-310"></a>
### F-310 (P3) – Migration 000110 bricht ab, wenn ein suffigierter Duplikatname bereits existiert

- **Betroffene WPs:** WP-123 · **Komponente:** backend/migrations/000110_interface_net01.up.sql:12-18 · **Quelle:** Verifikation (B15)
- **Reproduktion:** psql -f SP/work/verify-B15/net01-dup.sql: 'vfy-eth0' ×2 und 'vfy-eth0 (1)' → 'could not create unique index network_interface_ci_name_key'.
- **Soll:** Dedupe erzeugt immer eindeutige Namen.
- **Ist:** Kollision, Upgrade stoppt.
- **Nutzerwirkung:** Selten: manuelle Bereinigung beim Upgrade.
- **Ursache (belegt):** Suffix per row_number ohne Prüfung bestehender Namen.
- **Empfehlung:** Suffix bis eindeutig oder kollisionsfreies Suffix; Migrationsdatentest.
- **Retest (verbindlich):** Migrationstest wie Probe: Upgrade erfolgreich, Namen eindeutig.
- **Kodierender Test:** keiner

<a id="f-311"></a>
### F-311 (P3) – Interface-Anlage auf fremdem CI antwortet 500 statt 404

- **Betroffene WPs:** WP-123 · **Komponente:** backend/internal/ipam/handler.go:458-463; api/dberrors.go:83-88 · **Quelle:** Verifikation (B15)
- **Reproduktion:** Org B POST /api/v1/cis/{A-ci}/interfaces → 500; keine Zeile (probe-ipam-xtenant.log).
- **Soll:** 404.
- **Ist:** 500, Fehlerlog.
- **Nutzerwirkung:** Gering: irreführende 500; Isolation hält.
- **Ursache (belegt):** ipam.ErrNotFound nicht gemappt, WriteRepoError → 500.
- **Empfehlung:** ErrNotFound → 404 in CreateInterface und weiteren WriteRepoError-Aufrufern.
- **Retest (verbindlich):** API-Test: Org B → 404, keine Zeile.
- **Kodierender Test:** keiner

<a id="f-312"></a>
### F-312 (P3) – Interface-Löschung scheitert (irreführende 404), wenn dieselbe Adresse ungebunden im selben VRF existiert

- **Betroffene WPs:** WP-124 · **Komponente:** ip_address_interface_id_fkey ON DELETE SET NULL + uq_ip_address_org_vrf_address_no_interface; ipam/handler.go:514-516 · **Quelle:** Verifikation (B15)
- **Reproduktion:** eth0 mit 10.66.0.1 gebunden + 10.66.0.1 ungebunden; DELETE /network-interfaces/{eth0} → 404, GET 200 (probe-ipam-delete.log).
- **Soll:** Löschen nach definierter Regel oder 409.
- **Ist:** Unique-Verletzung, Rollback, 404 'not found'.
- **Nutzerwirkung:** Gering, nur API.
- **Ursache (belegt):** SET NULL erzeugt zweite ungebundene Zeile; Handler mappt jeden Fehler auf 404.
- **Empfehlung:** Entbindungsregel definieren; Nicht-NotFound über WriteRepoError.
- **Retest (verbindlich):** API-Test: 204 oder dokumentiertes 409, nie 404.
- **Kodierender Test:** keiner

<a id="f-313"></a>
### F-313 (P3) – allowed_source_types/allowed_target_types/cardinality werden gespeichert, aber nie durchgesetzt

- **Betroffene WPs:** WP-127 · **Komponente:** backend/internal/relationship (Create/validRelType), relationshiptype/model.go · **Quelle:** Verifikation (B16)
- **Reproduktion:** Typ mit allowed_source_types ['b16v-nonexistent-type'] → 201; Kante von CI anderen Typs → 201.
- **Soll:** Durchsetzung oder Kennzeichnung als informativ (REL-01 nennt nur die Felder – Auslegungsfrage).
- **Ist:** Wirkungslos, unbekannte Typ-Keys akzeptiert.
- **Nutzerwirkung:** Admins glauben an Einschränkungen.
- **Ursache (belegt):** Kein Konsument der Felder.
- **Empfehlung:** Mit Planverantwortlichem entscheiden: validieren (422) oder in OpenAPI als informativ markieren.
- **Retest (verbindlich):** PG-Test: unzulässiger Quelltyp → 422; unbekannter Key bei Typanlage → 400.
- **Kodierender Test:** keiner

<a id="f-314"></a>
### F-314 (P3) – Beziehungsdialog bleibt nach Doppelklick trotz erfolgreicher Anlage mit Konfliktfehler offen (intermittierend)

- **Betroffene WPs:** WP-128, WP-201 · **Komponente:** frontend/src/components/cmdb/CIRelationshipsCard.tsx:199 · **Quelle:** Real-Stack-E2E (ci-lifecycle, unverified)
- **Reproduktion:** CI-Detail → '+ Beziehung hinzufügen' → depends_on → Gegenstelle → Doppelklick 'Anlegen'; in run3/run4 Dialog offen mit 'a record with the same unique value already exists'.
- **Soll:** Eine Kante, Dialog schließt, kein Fehler.
- **Ist:** Daten korrekt (1 Kante, Wiederholung 409), UI meldet Fehlschlag.
- **Nutzerwirkung:** Nutzer glaubt an Fehlschlag; kein Datenschaden.
- **Ursache (Hypothese):** HYPOTHESE: mutate je Submit ohne Sperre; TanStack-Callbacks nur für letzten Aufruf → 409-Antwort verhindert done().
- **Empfehlung:** Submit während isPending sperren bzw. onSuccess am useMutation-Hook.
- **Retest (verbindlich):** cd frontend && npx playwright test -c e2e-real/playwright.real.config.ts ci-lifecycle -g 'L08' 5× in Folge ohne Soft-Fehler.
- **Kodierender Test:** frontend/e2e-real/ci-lifecycle.spec.ts L08 (UI-Soft-Assertions intermittierend FAIL)

<a id="f-315"></a>
### F-315 (P3) – Export-Stale-Reclaim kann lange Exporte doppelt ausführen

- **Betroffene WPs:** WP-130 · **Komponente:** backend/internal/export/pg_job_repository.go:270-300, worker.go:204-214 · **Quelle:** Verifikation (B16)
- **Reproduktion:** Statisch: ReclaimStale setzt running-Jobs mit started_at < now−15 min auf pending, ohne Heartbeat; nicht reproduziert.
- **Soll:** Nur Exporte toter Replikas werden wieder aufgenommen.
- **Ist:** Rein zeitbasiert.
- **Nutzerwirkung:** Doppelte Arbeit, ggf. verwaister Blob; Endstatus konsistent.
- **Ursache (Hypothese):** HYPOTHESE: keine Lease-Erneuerung.
- **Empfehlung:** started_at periodisch erneuern oder Lease-basiert reclaimen.
- **Retest (verbindlich):** PG-Test mit langsamem Renderer und Fake-Uhr: zweiter Worker ResumeStale → 0.
- **Kodierender Test:** keiner

<a id="f-316"></a>
### F-316 (P3) – Audit-Kette synchron unter Org-Lock bis COMMIT statt asynchroner Outbox mit Single-Runner (AUD-04)

- **Betroffene WPs:** WP-132 · **Komponente:** backend/migrations/000117/000118 audit_log_chain_trigger · **Quelle:** Audit/Verifikation (B16)
- **Reproduktion:** Trigger nimmt pg_advisory_xact_lock je Org in jeder schreibenden Transaktion; docs/plan/entscheidungen.md 'Audit-Kette und Outbox'.
- **Soll:** AUD-04-Architekturtext: Outbox, Single-Runner, Lock nur im Runner.
- **Ist:** Alle auditierten Writes einer Org serialisieren bis COMMIT.
- **Nutzerwirkung:** Kein Funktionsverlust; Durchsatz je Org begrenzt (dokumentiert).
- **Ursache (belegt):** Designentscheid, Folge-WP geplant.
- **Empfehlung:** Folge-WP verfolgen; Nebenläufigkeitstest als Regressionsschutz.
- **Retest (verbindlich):** Nach Umstellung: TestAuditChainPerOrganizationPG unverändert grün, Writer halten keinen Org-Lock (pg_locks).
- **Kodierender Test:** keiner

<a id="f-317"></a>
### F-317 (P3) – Continuous Aggregate metric_sample_1h ohne Retention-Policy (400 Tage)

- **Betroffene WPs:** WP-135 · **Komponente:** TimescaleDB-Policies (Migration 000020) · **Quelle:** Verifikation (B17)
- **Reproduktion:** psql audit_db_16: timescaledb_information.jobs → Retention nur 1001 metric_sample; _materialized_hypertable_3 nur Refresh 1002.
- **Soll:** Alle Metrikdaten inkl. Rollups nach 400 Tagen gelöscht.
- **Ist:** Stunden-Rollups wachsen unbegrenzt.
- **Nutzerwirkung:** Unbegrenztes Speicherwachstum.
- **Ursache (belegt):** add_retention_policy nur für Rohtabelle.
- **Empfehlung:** add_retention_policy('metric_sample_1h', INTERVAL '400 days') per Migration.
- **Retest (verbindlich):** Migrationstest: policy_retention für metric_sample_1h mit drop_after 400 days.
- **Kodierender Test:** keiner

<a id="f-318"></a>
### F-318 (P3) – Outbox-Cleanup behält das neueste Event jeder Entität dauerhaft

- **Betroffene WPs:** WP-135 · **Komponente:** backend/internal/events/publisher.go:224-227 · **Quelle:** Verifikation (B17)
- **Reproduktion:** DELETE behält max(sequence) je Org und max(entity_seq) je Entität (TestOutboxAgainstPostgres 'cleanup keeps newest').
- **Soll:** Events 30 Tage (NFR-06).
- **Ist:** Letztes Event mit Daten jeder je geänderten Entität bleibt.
- **Nutzerwirkung:** Eventdaten (z. B. CI-Snapshot bei ci.deleted) überdauern die Frist.
- **Ursache (belegt):** Nummerierungsanker als volle Zeilen.
- **Empfehlung:** Zähler in eigener Sequenztabelle oder Daten der Ankerzeilen nullen.
- **Retest (verbindlich):** Cleanup-Test: nach 31 Tagen keine Altzeile mit Daten, Nummerierung läuft weiter.
- **Kodierender Test:** keiner

<a id="f-319"></a>
### F-319 (P3) – Domain-Events nach Commit in separater Transaktion (review.created, export.completed, job.failed, webhook.dead)

- **Betroffene WPs:** WP-136 · **Komponente:** backend/internal/events/outbox.go:168-175; jobs/service.go:185-196; router.go:676-681; export/worker.go:170; webhook/dispatcher.go:611 · **Quelle:** Verifikation (B17)
- **Reproduktion:** Codepfad: PGEmitter.Emit öffnet eigene WithTenant-Tx nach dem Business-Commit; Fehler nur geloggt; Verlustszenario nicht reproduziert.
- **Soll:** WP-136-Kriterium: Outbox in derselben Transaktion wie die Mutation.
- **Ist:** Crash/DB-Fehler zwischen Commit und Emit verliert das Event.
- **Nutzerwirkung:** Seltene fehlende Benachrichtigungen/Webhooks.
- **Ursache (Hypothese):** Post-Commit-Emitter-API (Mechanismus belegt, Verlust nicht ausgeführt → nicht bestätigt).
- **Empfehlung:** Mutations-Tx an Outbox.Append übergeben (wie CI).
- **Retest (verbindlich):** Fault-Injection: Emit nach Commit schlägt fehl → Event vorhanden oder Business-Tx zurückgerollt.
- **Kodierender Test:** keiner

<a id="f-320"></a>
### F-320 (P3) – Webhook-Empfänger erhalten keine Event-ID/entity_seq; Redeliveries tragen neue Delivery-ID

- **Betroffene WPs:** WP-136 · **Komponente:** backend/internal/webhook/dispatcher.go:298-315,697-702; events/publisher.go:194-200 · **Quelle:** Verifikation (B17)
- **Reproduktion:** Body = env.Data; deliveryID je Dispatch; Header nur Event/Delivery/Signature.
- **Soll:** EVT-01: idempotente Konsumenten in entity_seq-Reihenfolge.
- **Ist:** Duplikate/Reihenfolge nicht erkennbar.
- **Nutzerwirkung:** Doppelte oder ungeordnete Verarbeitung in Integrationen.
- **Ursache (belegt):** Webhook-Body ist Eventdaten statt Envelope.
- **Empfehlung:** X-Reticora-Event-ID/Sequence/Entity-Seq-Header; Delivery-ID aus Event-ID + Subscription.
- **Retest (verbindlich):** Dispatcher-Test: Header vorhanden, doppeltes Consume → gleicher Idempotenzschlüssel.
- **Kodierender Test:** keiner

<a id="f-321"></a>
### F-321 (P3) – Stream-Retention nicht konfigurierbar (EVT-01 '30 Tage / 10 GB … konfigurierbar')

- **Betroffene WPs:** WP-136 · **Komponente:** backend/internal/events/envelope.go:38-39 · **Quelle:** Verifikation (B17)
- **Reproduktion:** Konstanten, kein Konfigschlüssel.
- **Soll:** Je Installation konfigurierbar.
- **Ist:** Hart kodiert 30 d/10 GB.
- **Nutzerwirkung:** Betreiber können Größe/Alter nicht anpassen.
- **Ursache (belegt):** Konstanten statt Konfiguration.
- **Empfehlung:** RETICORA_EVENTS_MAX_AGE/MAX_BYTES mit NFR-06-Minimum.
- **Retest (verbindlich):** Config- und Setup-Test mit konfiguriertem MaxAge.
- **Kodierender Test:** keiner

<a id="f-322"></a>
### F-322 (P3) – Keine persistierten Abonnements, keine Empfänger-i18n, kein Org-Branding

- **Betroffene WPs:** WP-138 · **Komponente:** backend/internal/notify/subscriptions.go (Render) · **Quelle:** Verifikation (B17)
- **Reproduktion:** Keine Notification-Abo-Tabelle; Render liefert deutschen Betreff + zweisprachigen Body für alle; grep locale/brand → nichts.
- **Soll:** NTF-03: Abos; Vorlagen in Empfängersprache mit Branding.
- **Ist:** Abos nur als Resolver-Parameter, ein Text für alle.
- **Nutzerwirkung:** Kein Opt-in, Mails nicht lokalisiert/gebrandet.
- **Ursache (belegt):** Umfangskürzung.
- **Empfehlung:** Abos persistieren, je Nutzer-Locale und Branding rendern, oder formal verschieben.
- **Retest (verbindlich):** Integration: Abo → Zustellung in Nutzersprache.
- **Kodierender Test:** keiner

<a id="f-323"></a>
### F-323 (P3) – review.created erreicht client-gescopte Reviewer nicht

- **Betroffene WPs:** WP-140 · **Komponente:** server/router.go:677-678; notify/subscriptions.go:39-40; permission/covers.go · **Quelle:** Verifikation (B17)
- **Reproduktion:** Codepfad: Eventdaten {id, kind, object_key} ohne client_id/site_id → Covers behandelt Item als org-weit; Review-Items sind für client-gescopte Nutzer sichtbar (reviews/pg_repository.go:27-28). Nicht ausgeführt.
- **Soll:** Wer ein Review-Item sehen/lösen darf, wird benachrichtigt.
- **Ist:** Nur org-weite review:resolve-Inhaber.
- **Nutzerwirkung:** Client-Techniker erfahren nichts von neuen Items.
- **Ursache (Hypothese):** HYPOTHESE: fehlende client_id/site_id in Eventdaten.
- **Empfehlung:** client_id/site_id der Kandidaten-CIs in review.created aufnehmen.
- **Retest (verbindlich):** Integration: Review für CI in C1 → C1-Reviewer erhält Inbox-Eintrag, C2 nicht.
- **Kodierender Test:** keiner

<a id="f-324"></a>
### F-324 (P3) – Review-Items außerhalb reviews.Service (Merge-Konflikte) benachrichtigen nicht

- **Betroffene WPs:** WP-140 · **Komponente:** backend/internal/ci/merge.go:574 · **Quelle:** Verifikation (B17)
- **Reproduktion:** Codepfad: direkter INSERT override_conflict review_item; nur reviews.Service.Open ruft den Notifier (reviews/service.go:71-72). Nicht ausgeführt.
- **Soll:** Jedes neue Review-Item meldet review.created.
- **Ist:** Merge-Konflikt-Reviews ohne Benachrichtigung.
- **Nutzerwirkung:** Reviewer übersehen Override-Konflikte.
- **Ursache (Hypothese):** HYPOTHESE: direkter SQL-Insert umgeht Service-Hook.
- **Empfehlung:** review.created in Merge-Tx anhängen oder über reviews.Service.
- **Retest (verbindlich):** Merge mit widersprüchlichen Overrides → review.created-Outbox-Zeile und Benachrichtigung.
- **Kodierender Test:** keiner

<a id="f-325"></a>
### F-325 (P3) – Flags bestehender Attribute in der UI nicht änderbar

- **Betroffene WPs:** WP-142 · **Komponente:** frontend/src/pages/CITypeAdminPage.tsx:195-198,466 · **Quelle:** Verifikation (B17)
- **Reproduktion:** Editor nur für neue Felder mit NO_INDEX; Feldliste bietet nur Löschen.
- **Soll:** searchable/unique/scope bestehender Attribute umschaltbar (Backend kann PATCH).
- **Ist:** Nur per REST.
- **Nutzerwirkung:** Index/Eindeutigkeit nach Anlage nicht in der UI verwaltbar.
- **Ursache (belegt):** UI-Umfang.
- **Empfehlung:** Bearbeiten-Dialog mit vorbelegten Flags.
- **Retest (verbindlich):** Vitest plus e2e-real: bestehendes Feld bearbeiten → PATCH → Job eingeplant.
- **Kodierender Test:** keiner

<a id="f-326"></a>
### F-326 (P3) – Topologie-Simulation zeigt bei /impact-Fehler still eine richtungsnaive Client-Berechnung

- **Betroffene WPs:** WP-180, WP-195 · **Komponente:** frontend/src/pages/TopologyPage.tsx · **Quelle:** Audit + Verifikation (B23, B25)
- **Reproduktion:** /topology?simulate=<ids> öffnen und /impact scheitern lassen (4xx/5xx, z. B. > 100 IDs → 422) oder noch laden lassen: useImpactMany (retry:false) – der Fehler wird ignoriert (TopologyPage.tsx:145); outageImpact fällt auf computeOutageImpact zurück (:147-153), das edge.source→edge.target folgt, während Backend-Kanten source=Abhängiger, target=Versorger sind (:51-52). Für 'server powered_by ups' markiert ein UPS-Ausfall nichts, ein Serverausfall die UPS.
- **Soll:** Fehler- bzw. 'Analyse nicht verfügbar'-Hinweis; nie eine abweichende Impact-Menge als autoritativ anzeigen.
- **Ist:** Client-BFS-Ergebnis ohne Fehlerhinweis; ignoriert Richtung, rel_type, Redundanz und Kanten außerhalb des geladenen Graphen.
- **Nutzerwirkung:** Nutzer können ohne es zu merken auf eine falsch gerichtete oder unvollständige Impact-Menge reagieren.
- **Ursache (Hypothese):** Hypothese aus Code und bestehender Vitest-Semantik (TopologyPage.test.tsx:70-83 'computeOutageImpact marks all transitively dependent nodes'); auf Seitenebene nicht reproduziert. Zusammengeführt aus WP-180 und WP-195.
- **Empfehlung:** Fehlerzustand von useImpactMany rendern; Client-Berechnung höchstens als gekennzeichnete Vorschau während des Ladens oder entfernen.
- **Retest (verbindlich):** Vitest: /impact-Stub mit 422 bzw. 500 → sichtbarer Fehlertext/Warnung, keine Impact-Badges aus dem Fallback; PASS.
- **Kodierender Test:** keiner

<a id="f-327"></a>
### F-327 (P3) – Bulk-Bericht meldet eine fehlerhafte CI-ID mit Code 500 'the CI could not be updated' statt der 400 der Einzeländerung

- **Betroffene WPs:** WP-181 · **Komponente:** backend/internal/ci/bulk.go · **Quelle:** Real-Stack-E2E (owned) + Verifikation (B23)
- **Reproduktion:** POST /api/v1/cis/bulk {ids:['not-a-uuid', <gültige ID>], status:'inactive'} als Org-Admin → Item {id:'not-a-uuid', result:'error', code:500, error:'the CI could not be updated'}; PATCH /api/v1/cis/not-a-uuid → 400 (frontend/e2e-real/bulk-jobs-export.spec.ts:582 B04, final.log; SP/stack/server.log).
- **Soll:** OpenAPI bulkCIs: jede ID mit dem HTTP-Status, den die Einzeländerung liefern würde (hier 400) und passender Meldung.
- **Ist:** 500 mit generischer Meldung; die gültige ID wird korrekt angewendet (kein Datenverlust).
- **Nutzerwirkung:** API-Clients und die UI-Liste 'Fehlgeschlagene CIs' stellen einen Eingabefehler als Serverfehler dar; automatische Clients könnten wiederholen oder alarmieren; falsche 5xx in Metriken.
- **Ursache (belegt):** validateBulk (bulk.go:74-100) prüft das ID-Format nicht; bulkError (bulk.go:173-192) mappt nur 'not found', FieldError, ValidationError und Vorbedingungen, alles andere → 500; der Einzel-PATCH nutzt api.ClassifyDBError (backend/internal/api/dberrors.go:61-64, 22P02 → 400). Zusammengeführt aus WP-181-Befund und eigenem Real-Stack-Defekt B04.
- **Empfehlung:** IDs in validateBulk als UUID validieren oder ClassifyDBError in bulkError verwenden.
- **Retest (verbindlich):** cd frontend && npx playwright test -c e2e-real/playwright.real.config.ts bulk-jobs-export -g 'B04' → PASS; API-Test: fehlerhafte ID → Item-Code 400, übrige IDs angewendet.
- **Kodierender Test:** frontend/e2e-real/bulk-jobs-export.spec.ts:582 B04 – FAIL (Real-Stack)

<a id="f-328"></a>
### F-328 (P3) – UI-Bulk auf 25 sichtbare Zeilen begrenzt; der Job-Pfad (≥ 100 IDs) ist aus der CI-Liste unerreichbar

- **Betroffene WPs:** WP-181 · **Komponente:** frontend/src/pages/CIListPage.tsx · **Quelle:** Audit + Verifikation (B23)
- **Reproduktion:** CIListPage.tsx:34 limit=25; Auswahl außerhalb der sichtbaren Seite wird verworfen (:46-54); Real-Stack J01 musste den 100-ID-Job über die API starten.
- **Soll:** Bulk für bis zu 500 IDs (API-08) aus der UI startbar, mit Einzelfehlern auch für Jobs.
- **Ist:** Höchstens 25 IDs je Aktion; der 'queued'-Zweig ist praktisch unerreichbar.
- **Nutzerwirkung:** Massenänderungen erfordern viele seitenweise Aktionen.
- **Ursache (Hypothese):** Feste Seitengröße und nur-sichtbare Auswahl (Code-Befund, nicht als UI-Lauf reproduziert).
- **Empfehlung:** 'Alle Treffer des Filters auswählen' (IDs vom Server), Job pollen und dessen Bericht je ID anzeigen.
- **Retest (verbindlich):** Vitest und e2e-real: 150 auswählen → 202 → Jobergebnis mit Fehlern wird gerendert; PASS.
- **Kodierender Test:** keiner

<a id="f-329"></a>
### F-329 (P3) – Revertierter Job bleibt 'succeeded'; zweiter Revert liefert 409 mit Detail 'not found'

- **Betroffene WPs:** WP-182 · **Komponente:** backend/internal/jobs/handler.go · **Quelle:** Audit + Verifikation (B23 Probe f6)
- **Reproduktion:** Probe f6 TestAuditProbeMergeJobRevert (audit_db_21): Revert durch den Ersteller → 200 {reverted:true}; Jobstatus bleibt succeeded; zweiter Revert → 409 {detail:'not found'}.
- **Soll:** Der Job vermerkt den Revert; der zweite Versuch erhält ein klares 409 ('already reverted').
- **Ist:** Keine Spur am Job, irreführendes Detail.
- **Nutzerwirkung:** Das Jobcenter kann revertierte Merges nicht anzeigen; die Fehlermeldung verwirrt.
- **Ursache (belegt):** Der Revert-Handler (handler.go:205-212) aktualisiert den Job nicht; der Unmerge-Fehlertext wird durchgereicht.
- **Empfehlung:** reverted_at/reverted_by am Job speichern und einen bereits revertierten Zustand auf ein klares 409 mappen.
- **Retest (verbindlich):** PG-Test: Revert → Job zeigt reverted; zweiter Revert → 409 'already reverted'; PASS.
- **Kodierender Test:** keiner

<a id="f-330"></a>
### F-330 (P3) – Wiederholter Review-Merge lässt seinen Job im Status failed

- **Betroffene WPs:** WP-183 · **Komponente:** backend/internal/jobs/service.go (Track/Mirror), backend/internal/reviews/service.go · **Quelle:** Audit + Verifikation (B23 Probe f3)
- **Reproduktion:** Probe f3 TestAuditProbeReviewMergeRetry (audit_db_21): resolve merge mit fehlschlagendem Effekt → Job failed; erneut mit erfolgreichem Effekt → Item resolved, Job bleibt failed ('transient failure'), 1 Job je Item.
- **Soll:** Der Job des erfolgreichen Retrys zeigt succeeded, oder jeder Versuch erhält einen eigenen Job.
- **Ist:** Ein einziger Job je Item (uq_job_ref), der failed bleibt.
- **Nutzerwirkung:** Das Jobcenter zeigt einen fehlgeschlagenen Merge, der tatsächlich gelungen ist.
- **Ursache (belegt):** Track ist je (ref_table, ref_id) idempotent; Mirror überspringt unzulässige Transitionen aus einem Terminalstatus (jobs/service.go:273-320).
- **Empfehlung:** Job bei neuem Versuch auf queued/running zurücksetzen (mit Versuchszähler) oder einen Job je Versuch anlegen.
- **Retest (verbindlich):** PG-Test: fail, dann Retry → Job succeeded mit attempts=2; PASS.
- **Kodierender Test:** keiner

<a id="f-331"></a>
### F-331 (P3) – Review-Merge läuft synchron im Request; der Job spiegelt ihn nur

- **Betroffene WPs:** WP-183 · **Komponente:** backend/internal/reviews/service.go · **Quelle:** Audit + Verifikation (B23)
- **Reproduktion:** reviews/service.go:172-192: Track, Mirror(running), effect(ctx) inline im HTTP-Request, Mirror(terminal); Probe f3 zeigt, dass Resolve erst nach terminalem Job zurückkehrt.
- **Soll:** JOB-04: große Operationen (Merge) laufen als asynchroner Job.
- **Ist:** Synchrone Ausführung mit gespiegeltem Jobdatensatz.
- **Nutzerwirkung:** Lange Merges blockieren den Request; der Merge erhält aus dem Jobmodell weder Retry noch Timeout.
- **Ursache (belegt):** Designentscheidung in runEffect.
- **Empfehlung:** Als Folge zu E-25 entscheiden, ob der Mirror genügt; sonst Merge einreihen und 202 antworten.
- **Retest (verbindlich):** Dokumentierte Entscheidung; falls asynchron: Test, dass eine Merge-Auflösung 202 mit Job-Location liefert.
- **Kodierender Test:** keiner

<a id="f-332"></a>
### F-332 (P3) – BFF-Client bffQuery hat keinen Verwender in der UI

- **Betroffene WPs:** WP-184 · **Komponente:** frontend/src/api/client.ts · **Quelle:** Audit + Verifikation (B23)
- **Reproduktion:** grep -rn bffQuery frontend/src (ohne Tests) → nur die Definition in api/client.ts:436.
- **Soll:** GQL-01 'BFF für das Frontend': die UI nutzt den BFF für aggregierte Lesezugriffe, oder der BFF ist als vorerst API-only dokumentiert.
- **Ist:** Toter Client-Code; alle UI-Lesezugriffe laufen über REST.
- **Nutzerwirkung:** Heute keine; der BFF wird ohne reale UI-Nutzung ausgeliefert.
- **Ursache (belegt):** Nicht in eine Seite verdrahtet.
- **Empfehlung:** In der ersten Aggregatansicht (z. B. CI-Detail) nutzen oder als API-only dokumentieren.
- **Retest (verbindlich):** UI-Seitentest plus e2e-real, das /bff/graphql aus einer Seite aufruft, oder dokumentierte Entscheidung.
- **Kodierender Test:** keiner

<a id="f-333"></a>
### F-333 (P3) – Topology-Komplexität des BFF unterschätzt große Graphen

- **Betroffene WPs:** WP-185 · **Komponente:** backend/internal/graphqlbff/graphqlbff.go · **Quelle:** Audit + Verifikation (B23)
- **Reproduktion:** Memory-Probe TestVerifyProbeTopologyComplexity (ov_bffc.json): 1500 CIs, Query '{ topology { nodes { id name ci { <18 Felder> } } } }' → HTTP 200 mit 1500 Knoten ohne Komplexitätsfehler, während '{ a: cis(first:100){…} b: … c: … }' (300 CIs) wegen Komplexität abgewiesen wird.
- **Soll:** Die Komplexität wächst mit der möglichen Knotenzahl (rootlos bis 2000), sodass das SEC-10-Budget über Queries konsistent ist.
- **Ist:** Topologie wird unabhängig von der Knotenzahl mit child*100 bewertet.
- **Nutzerwirkung:** Fünfmal schwerere Queries als die abgewiesene cis-Query passieren das Budget; nur die 10-s-Deadline begrenzt sie (schwächerer DoS-Schutz).
- **Ursache (belegt):** Konstanter Faktor maxPage in der Komplexitätsfunktion (graphqlbff.go:110).
- **Empfehlung:** Ohne Root child*MaxTopologyNodes, mit Root child*Fan-out-Schätzung nach Tiefe berechnen.
- **Retest (verbindlich):** Unittest: rootlose Topologie mit verschachtelten ci-Feldern überschreitet MaxComplexity; kleine gerootete Topologie passiert; PASS.
- **Kodierender Test:** keiner

<a id="f-334"></a>
### F-334 (P3) – Kontakt-Suchtreffer navigieren auf /contacts, eine nicht existente Route

- **Betroffene WPs:** WP-186 · **Komponente:** backend/migrations/000132_search_entity_kinds.up.sql; frontend CommandPalette.tsx, App.tsx · **Quelle:** Audit + Verifikation (B23 Probe f4)
- **Reproduktion:** Command-Palette: Kontaktnamen eingeben und Treffer wählen → window.location.assign('/contacts'); App.tsx hat keine /contacts-Route, der Catch-all (App.tsx:360) rendert CIListPage. Probe f4 bestätigt url='/contacts' bzw. '/locations'.
- **Soll:** Navigation zum Kontakt (oder seinem Client-/CI-Kontext), bei Locations zum Knoten.
- **Ist:** Die CI-Liste öffnet sich, der Kontakt wird nicht gezeigt; Location-Treffer öffnen die Baumwurzel.
- **Nutzerwirkung:** Nutzer finden einen Kontakt, können ihn aber aus der Suche nicht öffnen.
- **Ursache (belegt):** search_document.url ist konstant '/contacts' bzw. '/locations' (Migration 000132:39-40); das Frontend hat keine Kontaktansicht (nur api/client.ts:368).
- **Empfehlung:** Deep-Link speichern (z. B. /clients/{client_id}?contact={id}, /locations?node={id}) oder eine Kontakt-Detailroute ergänzen.
- **Retest (verbindlich):** e2e-real: Kontakt und Location suchen, Treffer anklicken → die Seite zeigt genau diese Entität; PASS.
- **Kodierender Test:** keiner

<a id="f-335"></a>
### F-335 (P3) – Jeder Inhaber von saved_view:write kann fremde org-geteilte Sichten umbenennen, entsperren oder löschen

- **Betroffene WPs:** WP-188 · **Komponente:** backend/internal/savedview/pg_repository.go · **Quelle:** Audit + Verifikation (B24) + Defekttest-Welle (Regressionstest)
- **Reproduktion:** Probe zz_verify_b24_sv_test.go (audit_db_22): Owner legt {shared:true} an; anderes Org-Mitglied (saved_view:write) PATCH {name:'hijacked', shared:true} → 200; DELETE → 204; DB rows = 0.
- **Soll:** Eine explizite, konsistente Regel; rollengeteilte Sichten sind für Mitglieder read-only (model.go:20-21, DELETE 404), VIE-01 definiert keine Edit-Rechte.
- **Ist:** Nicht-Owner mit saved_view:write (engineer, org_admin, Custom-Rollen) ändern und löschen org-geteilte Sichten; Viewer nicht (kein saved_view:write, route_permissions.txt:45,319).
- **Nutzerwirkung:** Geteilte Teamsichten können von jedem Engineer ohne Zustimmung des Owners umbenannt oder gelöscht werden (ohne Undo).
- **Ursache (belegt):** Update/Delete WHERE (shared OR owner_id = $n) (pg_repository.go:157, :175); als Absicht dokumentiert (Kommentare :112, :171; api/openapi.yaml PATCH /saved-views/{id}: org-weit geteilte Sicht für alle editierbar). Kein Anforderungsverstoß, sondern offene Entscheidung.
- **Empfehlung:** Entscheidung dokumentieren (Owner-only oder Admin-Permission); bei Owner-only Prädikat einschränken, Löschknopf für fremde Sichten ausblenden und den Regressionstest anpassen.
- **Retest (verbindlich):** Nach Entscheidung: TestAuditT7SavedViewEditDeleteRights an die Regel angepasst → PASS (cd backend && TEST_DATABASE_URL=… go test -count=1 -v -run '^TestAuditT7SavedViewEditDeleteRights$' ./internal/savedview/); bei Owner-only: Nicht-Owner PATCH/DELETE → 404/403 mit unveränderter DB.
- **Kodierender Test:** backend/internal/savedview/audit_t7_savedview_rights_test.go:91 TestAuditT7SavedViewEditDeleteRights – PASS (SP/final/test-db.log); kodiert den dokumentierten Ist-Vertrag (org-geteilt von allen editierbar, rollengeteilt nur Owner, keine Cross-Org-Zugriffe)

<a id="f-336"></a>
### F-336 (P3) – sort und columns gespeicherter Sichten werden persistiert, aber nicht angewendet; die UI kann sie und die Freigabe nicht setzen

- **Betroffene WPs:** WP-188 · **Komponente:** backend/internal/savedview/query.go; frontend SavedViewsPage.tsx · **Quelle:** Audit + Verifikation (B24)
- **Reproduktion:** Real-Stack V01 prüft, dass der Run-Request genau {…filter_spec, entity_kind} sendet (ohne sort/columns); PGQueryEngine nutzt immer ORDER BY name ASC (query.go:68-70); saveCurrent sendet nur name/entity_kind/filter_spec (SavedViewsPage.tsx:45-51).
- **Soll:** Ergebnisse folgen sort/columns der Sicht, oder die Einschränkung ist für Phase 1 dokumentiert.
- **Ist:** Gespeicherte Darstellungseinstellungen sind wirkungslos; Freigabe nur per API möglich.
- **Nutzerwirkung:** Darstellungseinstellungen sind tote Daten; Teilen nur über die API.
- **Ursache (belegt):** Query-Engine und UI sind nicht an die neuen Felder angebunden.
- **Empfehlung:** Whitelist-ORDER BY anwenden und columns in der UI umsetzen, oder als reine Daten dokumentieren.
- **Retest (verbindlich):** Vitest/Playwright: Sicht mit sort desc rendert Zeilen in dieser Reihenfolge und nur die gespeicherten Spalten; PASS.
- **Kodierender Test:** keiner

<a id="f-337"></a>
### F-337 (P3) – shared_with_roles akzeptiert Rollen-IDs, die in der Organisation nicht existieren

- **Betroffene WPs:** WP-188 · **Komponente:** backend/internal/savedview/view_rules.go · **Quelle:** Verifikation (B24)
- **Reproduktion:** Probe: POST /api/v1/saved-views {shared_with_roles:['c0def7b0-0000-4000-8000-00000000abcd']} → 201.
- **Soll:** 422 für IDs, die keine Rollen/Custom-Rollen der Org sind.
- **Ist:** Ungeprüft gespeichert (nur UUID-Format).
- **Nutzerwirkung:** Stille Fehlkonfiguration: eine Sicht wirkt geteilt, ist aber mit niemandem geteilt.
- **Ursache (belegt):** ValidateView prüft nur das UUID-Format (view_rules.go:15-62).
- **Empfehlung:** IDs im Repository gegen role/custom_role der Org prüfen.
- **Retest (verbindlich):** PG-Test: unbekannte oder fremde Rollen-ID → 422 shared_with_roles[0]; PASS.
- **Kodierender Test:** keiner

<a id="f-338"></a>
### F-338 (P3) – CSV-Export neutralisiert keine Tabellenkalkulationsformeln (CSV-Injection)

- **Betroffene WPs:** WP-189 · **Komponente:** backend/internal/export/export.go · **Quelle:** Verifikation (B24)
- **Reproduktion:** Probe VP/probe/zz_verify_b24_csv_test.go (go test -overlay VP/probe/overlay-csv.json -run TestVerifyB24CSVInjection -v ./internal/export/): CI-Name mit führendem '=HYPERLINK(…)' und Hersteller mit führendem '+cmd|' /C calc'!A0' → GET /api/v1/export/cis?format=csv schreibt beide Zellen unverändert.
- **Soll:** Zellen, die mit = + - @ (oder Tab/CR) beginnen, werden entschärft (z. B. mit ' präfixiert).
- **Ist:** Unverändert geschrieben.
- **Nutzerwirkung:** Angreiferbeeinflusste Werte (z. B. SNMP-sysName aus der Discovery) können beim Öffnen in Excel/LibreOffice, auf die das BOM-/Semikolon-Format zielt, zu Formeln oder Links werden.
- **Ursache (belegt):** rowWriter.row (export.go:191-206) hat keine Bereinigung.
- **Empfehlung:** Gefährliche führende Zeichen nach OWASP-CSV-Injection-Leitfaden für csv und datev präfixieren; Unittest.
- **Retest (verbindlich):** Unittest: Name '=1+1' wird in csv und datev mit Präfix ' ausgegeben, Zeilenzahl unverändert; go test -count=1 -run '<Testname>' ./internal/export/ → PASS.
- **Kodierender Test:** keiner

<a id="f-339"></a>
### F-339 (P3) – Standard-Interface-Zähler nur für ifIndex 1 und nur mit RETICORA_METRICS_INTERVAL

- **Betroffene WPs:** WP-190 · **Komponente:** collector/collectorcmd/collectorcmd.go · **Quelle:** Audit + Verifikation (B24)
- **Reproduktion:** collectorcmd.go:1470-1476 defaultPollMetrics: if_in/out_octets und if_oper_status nur für ifIndex 1, nur wenn RETICORA_METRICS_INTERVAL gesetzt ist.
- **Soll:** Interface-Zähler der Geräteschnittstellen (MON-02).
- **Ist:** Nur ifIndex 1, opt-in.
- **Nutzerwirkung:** Switch-Metriken fehlen weitgehend.
- **Ursache (belegt):** Statische OID-Liste.
- **Empfehlung:** ifTable/ifXTable mit ifIndex-Label walken oder dokumentieren.
- **Retest (verbindlich):** Unittest: Fake-Agent mit 3 Interfaces → 3×2 Zähler-Samples mit ifIndex-Label; PASS.
- **Kodierender Test:** keiner

<a id="f-340"></a>
### F-340 (P3) – Client-/Site-Optionen des Scope-Umschalters auf 200 begrenzt; eine gültige gespeicherte Auswahl jenseits davon wird verworfen

- **Betroffene WPs:** WP-191 · **Komponente:** frontend/src/components/ScopeSwitcher.tsx, api/cmdb.ts · **Quelle:** Audit + Verifikation (B24)
- **Reproduktion:** Org mit > 200 Clients, gespeicherte clientId von Client #201, Reload → der Effekt in ScopeSwitcher.tsx setzt die Auswahl zurück (clientApi.list limit 200, api/cmdb.ts:311).
- **Soll:** Auswahl bleibt erhalten, oder der Umschalter blättert/sucht.
- **Ist:** Gespeicherte Auswahl wird verworfen.
- **Nutzerwirkung:** Große MSP-Mandanten können spätere Clients nicht dauerhaft auswählen.
- **Ursache (Hypothese):** Feste Grenze ohne Paging/Suche (Code-Befund, nicht ausgeführt).
- **Empfehlung:** Paging oder Suche; gespeicherte ID vor dem Verwerfen per GET /clients/{id} prüfen.
- **Retest (verbindlich):** Vitest mit 201-Client-Stub behält den gespeicherten Client; PASS.
- **Kodierender Test:** keiner

<a id="f-341"></a>
### F-341 (P3) – Raum-Layout-Koordinaten werden serverseitig nicht validiert

- **Betroffene WPs:** WP-193 · **Komponente:** backend/internal/tenantapi (model.go, pg_repository.go) · **Quelle:** Verifikation (B24)
- **Reproduktion:** Probe VP/probe/zz_verify_b24_room_test.go (go test -overlay VP/probe/overlay-room.json -run TestVerifyB24RoomLayout -v ./internal/tenantapi/, audit_db_22): UpdateRoom layout {ci:{x:5,y:-3}} → gespeichert {5 -3}.
- **Soll:** 422 für Koordinaten außerhalb 0..1 bzw. für Schlüssel, die keine CI-IDs der Org sind.
- **Ist:** Persistiert.
- **Nutzerwirkung:** API-Clients können Objekte außerhalb des Plans platzieren (die UI begrenzt, die API nicht).
- **Ursache (belegt):** UpdateRoomRequest.Layout (model.go:161-170) ohne Bereichsprüfung in Handler/Repository (pg_repository.go:630-678).
- **Empfehlung:** 0 ≤ x,y ≤ 1 und CI-Zugehörigkeit validieren; 422 zurückgeben.
- **Retest (verbindlich):** API-Test: x=1.5 → 422; x=0.25 → 200 und GET liefert den Wert; PASS.
- **Kodierender Test:** keiner

<a id="f-342"></a>
### F-342 (P3) – Tastaturverschiebung im Raumplan patcht das ganze Layout aus veraltetem Zustand

- **Betroffene WPs:** WP-193 · **Komponente:** frontend/src/pages/RoomPlanPage.tsx · **Quelle:** Audit + Verifikation (B24)
- **Reproduktion:** Marker fokussieren und ArrowRight 5× vor dem Refetch drücken (RoomPlanPage.tsx:113-139): jeder Tastendruck sendet {…layout,[ci]:pos}, berechnet aus dem zuletzt geladenen Layout.
- **Soll:** Verschiebungen kumulieren und persistieren (oder werden entprellt); keine verlorenen Updates bei parallelen Bearbeitern.
- **Ist:** Verschiebungen kumulieren möglicherweise nicht; parallele Bearbeiter überschreiben sich (Hypothese).
- **Nutzerwirkung:** Tastaturpositionierung kann unzuverlässig wirken; mögliche verlorene Updates.
- **Ursache (Hypothese):** Hypothese aus Code: keine optimistische lokale Position, kein Debounce, Ganz-Layout-PATCH ohne If-Match; nicht ausgeführt.
- **Empfehlung:** Lokaler Positionszustand, entprellter PATCH, Objekt-PATCH oder If-Match.
- **Retest (verbindlich):** Vitest: 5× ArrowRight mit verzögertem Fetch-Stub → letzter PATCH x=0.55; PASS.
- **Kodierender Test:** keiner

<a id="f-343"></a>
### F-343 (P3) – Validierungsmeldungen des DynamicForm sind englische Literale

- **Betroffene WPs:** WP-194 · **Komponente:** frontend/src/lib/fieldmeta.ts · **Quelle:** Audit + Verifikation (B24)
- **Reproduktion:** Pflicht-Custom-Attribut im DynamicForm leer lassen, UI de → '<label> is required' bzw. 'must be an integer/number/boolean' (lib/fieldmeta.ts:241-255).
- **Soll:** Lokalisierte Meldung aus dem Katalog (GLO-10).
- **Ist:** Englische Literale.
- **Nutzerwirkung:** Gemischtsprachige Fehlermeldungen.
- **Ursache (belegt):** validateValue baut englische Strings.
- **Empfehlung:** Meldungsschlüssel plus Parameter zurückgeben und im DynamicForm übersetzen.
- **Retest (verbindlich):** Vitest: DynamicForm in de zeigt die deutsche Pflichtmeldung; PASS.
- **Kodierender Test:** keiner

<a id="f-344"></a>
### F-344 (P3) – Topologie-Knotenliste zeigt die CI-Typ-UUID statt des Typnamens

- **Betroffene WPs:** WP-195 · **Komponente:** frontend/src/pages/TopologyPage.tsx · **Quelle:** Verifikation (B25)
- **Reproduktion:** /topology öffnen: jedes Knoten-Badge rendert node.ci_type, also die UUID (Probe-Body ci_type 'c8b23944-…'; TopologyPage.tsx:341).
- **Soll:** Lokalisierter Typname (typeOptions enthält die Zuordnung id → Label bereits).
- **Ist:** Rohe UUID.
- **Nutzerwirkung:** Knotenliste schwer lesbar.
- **Ursache (belegt):** Keine id → Name-Zuordnung in der Knotenliste.
- **Empfehlung:** Label aus typeOptions per ID rendern, Fallback ID.
- **Retest (verbindlich):** Vitest: Knoten mit Typ-ID rendert 'Server'; PASS.
- **Kodierender Test:** keiner

<a id="f-345"></a>
### F-345 (P3) – Review-Inbox auf 100 Einträge ohne Paging begrenzt; 'Alle offenen auswählen' erfasst nur die geladene Seite

- **Betroffene WPs:** WP-196 · **Komponente:** frontend/src/hooks/useReviews.ts · **Quelle:** Audit + Verifikation (B25)
- **Reproduktion:** Tenant mit > 100 offenen Items: /reviews und 'Alle offenen auswählen' → useReviews.ts:62 sendet immer limit=100; has_more und total werden ignoriert.
- **Soll:** Paging oder Angabe des Umfangs für die Bulk-Auswahl.
- **Ist:** Feste 100.
- **Nutzerwirkung:** Große Rückstände nach einem Sweep sind aus der Inbox nicht vollständig abarbeitbar.
- **Ursache (belegt):** Feste Grenze ohne Paging-UI.
- **Empfehlung:** Paging ergänzen und 'x von total' anzeigen.
- **Retest (verbindlich):** Vitest: bei has_more=true fordert die nächste Seite offset=100 an; PASS.
- **Kodierender Test:** keiner

<a id="f-346"></a>
### F-346 (P3) – Merge-Kandidatenauswahl der Review-Inbox listet rohe CI-UUIDs

- **Betroffene WPs:** WP-196 · **Komponente:** frontend/src/pages/ReviewInboxPage.tsx · **Quelle:** Audit + Verifikation (B25)
- **Reproduktion:** ambiguous_identity mit 2 Kandidaten: Optionen der Auswahl 'Kandidat' sind UUIDs (ReviewInboxPage.tsx:295: candidates.map(id => ({value:id,label:id}))).
- **Soll:** Kandidatennamen und Schlüsselidentifikatoren.
- **Ist:** UUIDs.
- **Nutzerwirkung:** Erhöhtes Risiko, in das falsche CI zu mergen.
- **Ursache (belegt):** Keine Namensauflösung für Kandidaten.
- **Empfehlung:** Kandidatennamen per GET /cis?ids= oder useCI auflösen.
- **Retest (verbindlich):** Vitest: Optionstext = CI-Name; PASS.
- **Kodierender Test:** keiner

<a id="f-347"></a>
### F-347 (P3) – Jobcenter bietet 'Ergebnis' für jeden erfolgreichen Job an, gespiegelte Jobs haben aber nie ein Ergebnis (409)

- **Betroffene WPs:** WP-197 · **Komponente:** frontend/src/pages/JobCenterPage.tsx; backend/internal/jobs (service.go, handler.go) · **Quelle:** Audit + Verifikation (B25)
- **Reproduktion:** go test -overlay SP/work/audit-B25/overlay/overlay.json -run TestProbeB25MirroredJobResult ./internal/jobs/ → status=succeeded, result leer, GET /jobs/{id}/result → 409; JobCenterPage.tsx:129-133 zeigt den Button bei jedem succeeded.
- **Soll:** Ein Ergebnis (z. B. Export-Downloadlink, Merge-Zusammenfassung) oder kein Button.
- **Ist:** Das Modal zeigt den 409-Text für export-, review.merge-, ci_merge- und discovery-Jobs.
- **Nutzerwirkung:** Sackgassen-Button für häufige Jobarten.
- **Ursache (belegt):** Mirror (service.go:273-317) setzt nie Result; handler.go:127-140 antwortet 409; die UI ignoriert job.result.
- **Empfehlung:** Button nur bei vorhandenem job.result anzeigen und/oder für gespiegelte Arten ein Ergebnis speichern.
- **Retest (verbindlich):** Vitest: succeeded ohne result → kein Button; Integration: Export-Mirror-Erfolg speichert ein Ergebnis; PASS.
- **Kodierender Test:** keiner

<a id="f-348"></a>
### F-348 (P3) – Benachrichtigungspanel verbirgt Ladefehler als 'keine Benachrichtigungen'

- **Betroffene WPs:** WP-197 · **Komponente:** frontend/src/components/NotificationCenter.tsx · **Quelle:** Audit + Verifikation (B25)
- **Reproduktion:** /api/v1/notifications mit 500 oder 403 scheitern lassen, Glocke öffnen → Zähler 0 und 'Keine Benachrichtigungen' (NotificationCenter.tsx:22-23, :72-76; kein isError-Zweig).
- **Soll:** Fehlerzustand mit Retry.
- **Ist:** Stiller Leerzustand.
- **Nutzerwirkung:** Verpasste Meldungen ohne jeden Hinweis.
- **Ursache (belegt):** Kein isError-Zweig (Code gelesen).
- **Empfehlung:** Bei unread.isError oder all.isError Fehler und Retry rendern.
- **Retest (verbindlich):** Vitest mit 500-Stub: Fehlermeldung sichtbar; PASS.
- **Kodierender Test:** keiner

<a id="f-349"></a>
### F-349 (P3) – Override-Autor wird im Provenienz-Tooltip als rohe User-ID gezeigt

- **Betroffene WPs:** WP-198 · **Komponente:** frontend/src/components/cmdb/ProvenanceTooltip.tsx; backend/internal/ci/provenance.go · **Quelle:** Verifikation (B25)
- **Reproduktion:** Real-Stack D03: override_author = User-ID (im Spec geprüft); ProvenanceTooltip.tsx:66 rendert provenance.override_author unverändert.
- **Soll:** Anzeigename oder E-Mail des Autors.
- **Ist:** UUID.
- **Nutzerwirkung:** 'Wer hat entschieden' ist nur teilweise erklärbar.
- **Ursache (belegt):** Keine Namensauflösung in API (provenance.go:159) oder UI.
- **Empfehlung:** override_author_name serverseitig ergänzen oder über die Users-API auflösen.
- **Retest (verbindlich):** Vitest und e2e-real: Tooltip 'Autor' zeigt den Anzeigenamen; PASS.
- **Kodierender Test:** keiner

<a id="f-350"></a>
### F-350 (P3) – Provenienz-Abschnitt zeigt bei API-Fehler 'Noch keine Provenienz-Daten'

- **Betroffene WPs:** WP-198 · **Komponente:** frontend/src/components/cmdb/CIDetailSections.tsx · **Quelle:** Audit + Verifikation (B25)
- **Reproduktion:** GET /cis/{id}/provenance mit 500 → 'Noch keine Provenienz-Daten (erstellt bei der nächsten Discovery).' (CIDetailSections.tsx:298-305 prüft nur isLoading).
- **Soll:** Fehlerzustand mit Retry.
- **Ist:** Irreführender Leerzustand.
- **Nutzerwirkung:** Nutzer schließen, dass keine Provenienz existiert.
- **Ursache (belegt):** Kein isError-Zweig (Code gelesen).
- **Empfehlung:** Bei values.isError ErrorState rendern.
- **Retest (verbindlich):** Vitest mit 500-Stub zeigt den Fehlertext mit Retry; PASS.
- **Kodierender Test:** keiner

<a id="f-351"></a>
### F-351 (P3) – Kein In-App-Einstieg zu Self-Signup bzw. Onboarding

- **Betroffene WPs:** WP-199 · **Komponente:** frontend/src/pages/LoginPage.tsx, Navigation, CommandPalette · **Quelle:** Audit + Verifikation (B25)
- **Reproduktion:** /login hat keinen Link 'Registrieren'/'Konto anlegen'; grep -rn '/onboarding' frontend/src (ohne Tests) → nur App.tsx:303 und OnboardingPage.tsx:175; Navigation und Palette ohne Eintrag.
- **Soll:** Auffindbarer Signup vom Login (wenn aktiviert) und ein Weg zurück in ein unvollständiges Onboarding.
- **Ist:** Nutzer müssen die URL /onboarding kennen.
- **Nutzerwirkung:** Self-Service-Registrierung schwer auffindbar; unterbrochenes Onboarding schwer fortsetzbar.
- **Ursache (belegt):** Kein Link ergänzt.
- **Empfehlung:** Link 'Konto anlegen' auf der LoginPage und Dashboard-Einstieg bis zum Abschluss.
- **Retest (verbindlich):** Vitest und Playwright: /login zeigt den Link, der nach /onboarding navigiert; PASS.
- **Kodierender Test:** keiner

<a id="f-352"></a>
### F-352 (P3) – INS-04-Fehlerbild 'Localhost-Redirect' wird in der Onboarding-Suite nicht diskriminiert

- **Betroffene WPs:** WP-200 · **Komponente:** frontend/e2e/onboarding.spec.ts, frontend/e2e/fixtures/setup-onboarding.sh · **Quelle:** Audit + Verifikation (B25)
- **Reproduktion:** setup-onboarding.sh:89 BASE_URL=http://localhost:<port>; onboarding.spec.ts:145 vergleicht den Origin der redirect_uri mit BASE (beide localhost); :140-142 wird ohne E2E_ISSUER_URL übersprungen; in der Installation ist der Issuer ebenfalls localhost.
- **Soll:** INS-04: Test über einen nicht-localhost-Origin; redirect_uri und Issuer dürfen nicht localhost sein.
- **Ist:** Ein hart kodierter Localhost-Redirect bestünde den Test.
- **Nutzerwirkung:** Eine Regression des ursprünglichen Fehlers könnte ausgeliefert werden.
- **Ursache (belegt):** Testumgebung und Assertion-Design.
- **Empfehlung:** Einen Test über TLS-Host-IP oder Hostname fahren und 'localhost' in Authorize-URL und redirect_uri ausschließen.
- **Retest (verbindlich):** Injizierte redirect_uri=localhost lässt den Test fehlschlagen; Lauf über nicht-localhost-Origin grün.
- **Kodierender Test:** keiner

<a id="f-353"></a>
### F-353 (P3) – Onboarding-Schritt 'Export laden' prüft den synchronen CSV-Endpunkt statt der Datei des Exportjobs

- **Betroffene WPs:** WP-200 · **Komponente:** frontend/e2e/onboarding.spec.ts · **Quelle:** Audit + Verifikation (B25)
- **Reproduktion:** onboarding.spec.ts:302-328: POST /exports, Polling bis completed, dann GET /api/v1/export/cis?format=csv statt der Datei des Jobs.
- **Soll:** Download der Datei des abgeschlossenen Jobs und Zählung ihrer Zeilen (ABN-01 d).
- **Ist:** Der Job-Downloadpfad wird in dieser Suite nicht geprüft (anderweitig durch ci-lifecycle L10 und bulk-jobs-export E01 belegt).
- **Nutzerwirkung:** Gering, da andere reale Specs den Pfad abdecken; der ABN-01(d)-Schritt selbst ist in der Abnahmesuite aber nicht demonstriert.
- **Ursache (belegt):** Test-Abkürzung.
- **Empfehlung:** download_url laden bzw. 'Herunterladen' auf /export klicken und ≥ 30 Zeilen prüfen.
- **Retest (verbindlich):** Spec lädt die Jobdatei und prüft ≥ 30 Zeilen; eine gebrochene Blob-Signatur macht die Suite rot.
- **Kodierender Test:** keiner

<a id="f-354"></a>
### F-354 (P2) – RLS-Matrix (TestRLSMatrix) überspringt client-, site- und team-gescopte Principals auf den vererbten Tabellen document, maintenance_window und training_assignment und verdeckt damit F-004/F-048

- **Betroffene WPs:** WP-065 · **Komponente:** backend/internal/tenant/rls/matrix_integration_test.go:255 ('if tbl.inherited && p.scoped() { continue }'); fixtures_test.go:47-51, :107 (inherited-Flag) · **Quelle:** adversariale Prüfung der Abschlusssynthese T1 (Verifier-test_gap WP-065 'Scoped principals on inherited-scope tables')
- **Reproduktion:** Code-Inspektion; TestRLSMatrix ist grün (124× pass), während dieselben Tabellen den DB-Defekt zulassen. Rollback-Probe probe_doc_shared_delete.sql (audit_db_08) als reticora_app mit app.client_scope=c1: 'UPDATE 1', 'DELETE 1', 'after client1 delete: client2 asset links|0'.
- **Soll:** WP-065 AK1 – Matrix über alle Mandantentabellen × Org/Client/Site/Team/Mehrfach × SELECT/INSERT/UPDATE/DELETE × NULL; jede Policy-Lücke lässt die Matrix scheitern.
- **Ist:** Für 3 von 123 Tabellen werden nur org-weite und Org-B-Principals geprüft. Die Ersatztests TestModuleTablesInheritObjectScope und TestTeamRoomAndPersonScope prüfen Lesen und Link-Inserts, nicht UPDATE/DELETE/INSERT geteilter oder unverlinkter Zeilen durch gescopte Principals.
- **Nutzerwirkung:** Die Matrix meldet vollständige Erzwingung, obwohl Cross-Client-Schreib- und Löschpfade auf document/maintenance_window bestehen (F-004); Regressionen bleiben unentdeckt; der GATE-03-Nachweis ist überzeichnet.
- **Ursache (belegt):** Bewusstes Überspringen gescopter Principals für Tabellen mit Sub-Select-Policies (Seeding mit session_replication_role=replica ohne Trigger), matrix_integration_test.go:255.
- **Empfehlung:** Vererbte Tabellen mit aktiven Triggern und gescopten Principals in die Matrix aufnehmen (geteilte, einseitig verlinkte und unverlinkte Zeilen × UPDATE/DELETE/INSERT).
- **Retest (verbindlich):** cd backend && TEST_DATABASE_URL=... go test -count=1 -run '^TestRLSMatrix$' ./internal/tenant/rls/ ohne den Skip: rot, bis F-004/F-048 behoben sind, danach grün.
- **Kodierender Test:** keiner (Testlücke; Rollback-Probe SP/review-t1/probe_doc_shared_delete.out)
- **Prüfvermerk:** Nachgetragen am 2026-10-18: Die adversariale Prüfung der Synthese T1 hatte diesen Defekt neu angelegt und WP-065 zugeordnet; das Auswerteskript übernahm neue Defekte aus Prüfkorrekturen nicht, sodass er in der ursprünglichen Fassung der Befundliste fehlte (WP-065 verwies auf die interne ID L-T1-34). Die ID F-354 folgt der bestehenden Nummerierung, damit keine bestehende Referenz verschoben wird.

