KATALOGAUSZUG (Abschnitte 35, 36, 37, 40, 41, 42)
35. REST-API v1

API-01 [B] /api/v1; OpenAPI deckt alle Endpunkte ab; Parity-Test; Änderungen nur additiv; Deprecation mit Sunset-Header und mindestens 12 Monaten Vorlauf.

API-02 [B] Listen {data, next_cursor}; HMAC-signierter Cursor; limit Default 50, maximal 200; sort per Whitelist; filter[field] mit exact/gte/lte/like/in; Filter auf JSONB-Attribute nur für searchable Attribute (sonst 400 mit Hinweis); fields; q.

API-03 [B] Fehler als RFC 7807 mit trace_id (Tabelle wie v2, zusätzlich precondition-failed 412 für fehlenden If-Match bei aktivierter Pflichtprüfung).

API-04 [B] Middleware-Reihenfolge: RequestID/Tracing → Recovery → Pre-Auth-Rate-Limit (pro IP, AUT-10) → Auth → Tenant → Entitlement → Rate-Limit (600 Requests/min pro API-Key bzw. Session, 6.000/min pro Org, konfigurierbar; Header X-RateLimit-*) → Idempotenz (24 h) → Validierung → Handler.

API-05 [B] Ressourcen: org, clients, sites, buildings, rooms, racks, mounts, locations (Baum), vrfs, vlans, ci-types, attribute-definitions, cis (mit include, Unterressourcen, merge, restore, unmerge), relationships, relationship-types, subnets, ips, cables, contacts, topology, impact, collectors, discovery-scopes, credentials, review-items, webhooks, exports, jobs, notifications, notification-channels, me, users, teams, service-accounts, invitations, roles, role-assignments, api-keys, audit-log, entitlements, admin-entitlements, signup.

API-06 REST für alle Module und das Metamodell nach Phase: [P2] Sets, Templates/Versionen, Regeln, Layouts, Publish, Tags, Lifecycles, Assets, Verträge, Lager, Stocktake, Reservierungen, Bewegungen, Tickets, Dokumente, Importe; [P3] Workflows, Compliance, Findings; [P4] Agent; [A] IGA, KI.

API-07 [B] Optimistische Nebenläufigkeit: version (ETag) erhöht sich nur bei Schreibpfaden mit Rang ≥ 92 (manual, import, workflow); beobachtete Discovery-Updates ändern die Version nicht, da sie in _observed/_provenance und nicht-manuelle Felder gehen. PATCH ist Feld-granular; 409 nur bei zwischenzeitlicher manueller Änderung. If-Match optional in Phase 1, per Org-Setting erzwingbar.

API-08 [B] Bulk: maximal 500 IDs pro Request; ab 100 IDs asynchron als Job (JOB-04); Partial-Failure-Report mit Fehlern pro ID.

API-09 [B] /jobs (Abschnitt 42) für Export, Import, Bulk, Merge, Template-Migration, Org-Export.

36. GraphQL-BFF

GQL-01 [B] /bff/graphql mit gqlgen, nur mit Session-JWT, nicht öffentlich dokumentiert; Limits nach SEC-10.

GQL-02 [B] Queries ci, cis, topology, rackLayout, searchEverything, reviewInbox, jobs; Dataloader gegen N+1; Mutationen nur über REST.

GQL-03 [P2] Aggregate: Asset-Detail mit Children, aufgelöstes Formular (Layout + Regeln + Rolle) für ein CI, Historie, Metriken.

GQL-04 [B] Kein Bypass von RBAC/ABAC, Feldrechten, RLS, Scopes oder Entitlements.

37. Suche, Views, Bulk, Import &amp; Export

SRC-01 [B] Volltextsuche über Postgres-FTS für CIs, Locations, Kontakte; Feldrechte nach RBA-07.

SRC-02 [P2] Zusätzlich Inventarnummer, IP, MAC, Nutzer, Tags, Custom-Felder.

SRC-03 [P2] Strukturierte Abfragen als JSON-Filter-AST (MET-45) mit AND/OR/NOT, Vergleichsoperatoren und Attributen (inkl. Asset-Feldern), gepflegt im Klick-Builder (CH18); Text-Syntax [O].

SRC-04 [P2] Suche über Beziehungen (z. B. „VMs auf Hosts in Rack R12"); Tiefe maximal 5; RLS-konform.

VIE-01 [B] saved_view(org, owner, name, filter (JSON-AST), sort, columns, is_shared, shared_with_roles); eigene Tabelle statt app_user.settings.

VIE-02 [P2] Teilbare Views auf Org-/Rollenebene; vordefinierte Views „Meine Geräte", „Garantie läuft bald ab" (MET-44), „Server ohne Monitoring".

BLK-01 [B] Bulk-Statusaktionen (API-08).

BLK-02 [P2] Bulk für Felder, Standort, Tags, Template-Wechsel, Beziehungen, Zuweisung, Lifecycle, Archivierung (= Status inactive + Tag), Export; Partial-Failure-Report; Rechte- und Scope-Prüfung; Audit; Undo: jede Bulk-Operation speichert ein Change-Set und ist innerhalb 24 h serverseitig per POST /jobs/{id}/revert umkehrbar (Overrides nach OVR-03).

EXP-01 [B] POST /exports liefert 202 (Job); Download über Presign-URL (15 Minuten); CSV UTF-8 mit BOM, Semikolon; nur sichtbare Daten und Felder; Manifest listet ausgelassene Felder. [P2] DATEV-Mapping mit Default für die Anlagenbuchhaltung (Assets).

IMP-IO-01 [P2] Import (CSV, Excel, API) als Job mit Mapping-Layer (Spalte → Attribut/Relation, Transformationen, Defaults, Validierung), Dry Run, Fehlerreport, Duplikaterkennung über Identity-Resolution, Update Existing oder Create Missing, Provenienz import (Rang 95, respektiert Overrides), speicherbare Mapping-Profile; Dateien nach SEC-09.

40. Mandantenlebenszyklus

TLC-01 [B] Self-Signup (CH23): Formular (Org-Name, E-Mail, Passwort über Keycloak), E-Mail-Verifikation, Captcha und Pre-Auth-Rate-Limit (AUT-10), Blocklist für Wegwerf-Domains; erzeugt Org mit Slug, Default-VRF, ersten org_admin, Trial-Entitlement (ENT-07); auditiert im operator_audit.

TLC-02 [B] Anlage durch Operator (SEC-07) und [P4] durch Reseller; Plan und Limits werden bei Anlage gesetzt.

TLC-03 [B] Einladungen: 7 Tage gültig, einmalig, mit Rolle und Scope vordefiniert; Annahme erzeugt app_user; abgelaufene Einladungen erneuerbar.

TLC-04 [B] Nutzer deaktivieren/reaktivieren: Sessions, Refresh-Cookies und API-Keys sofort ungültig; Owner-Referenzen bleiben über Surrogat erhalten; Nutzer können nicht gelöscht werden, solange sie Owner offener Objekte sind (Übergabe erforderlich).

TLC-05 [P3] Vollexport der Org (alle Mandantentabellen als JSONL, Dokumente als ZIP) als Job; Download 24 h; für Portabilität und AVV.

TLC-06 [P3] Org-Löschung: Antrag durch org_admin mit MFA, 30 Tage Karenz (Org read-only, Collectors paused), danach Hard-Delete inklusive S3, Vault und Suchindex; Backups laufen nach Retention aus; Löschprotokoll (Zeitpunkt, Umfang, Hashes) wird dem Antragsteller per E-Mail und im operator_audit bereitgestellt.

TLC-07 [P4] Reseller-Wechsel und Übernahme einer Org mit Bestätigung beider Seiten.

41. Benachrichtigungen

NTF-01 [B] notification_channel(org, kind email|sms|pager, provider, config verschlüsselt, enabled); Provider-Abstraktion (Interface Send(message) → delivery). E-Mail: SaaS zentraler SMTP mit Org-Absender (DKIM/SPF), Dedicated/On-Prem/Air-Gapped kundeneigener SMTP.

NTF-02 [P3] SMS und Pager über dieselbe Abstraktion (CH24); Referenz-Provider (V): SMS Twilio und Sipgate; Pager ilert, PagerDuty, Opsgenie sowie SMS-Pager-Gateway; Eskalationsketten mit Bestätigung.

NTF-03 [B] Empfängerauflösung: Nutzer, Team, Kontaktrolle (owner, escalation, operations), Client-Kontakte; Vorlagen pro Ereignistyp, i18n, Org-Branding.

NTF-04 [B] notification_delivery mit Status und Retries; Ruhezeiten pro Nutzer; Opt-out für nicht-kritische Kategorien; Deduplizierung und Digest (z. B. Reviews stündlich gebündelt); Benachrichtigungscenter im UI (UI-16).

NTF-05 [B] Ereignisse Phase 1: Review erstellt, Collector offline, Entitlement-Warnung/-Ablauf, Einladung, Export fertig, Job fehlgeschlagen, Webhook dead, Zertifikat läuft ab. [P2] Mindestbestand, Garantie/Vertrag läuft ab; [P3] Alerts, Wartungsfenster, Workflow.

42. Jobs &amp; Scheduler

JOB-01 [B] job(org nullable, kind, status queued|running|succeeded|failed|cancelled, progress, params, result, error, idempotency_key, created_by (Surrogat), started_at, finished_at); Persistenz in Postgres, Ausführung über NATS-Work-Queue; Retry mit Backoff; Timeout pro Kind; Ergebnisse mit RLS.

JOB-02 [B] Scheduler mit Cron-Definitionen für Systemjobs: Offline-Detector, Kanten-Cleanup (TOP-02), Hard-Delete (CI-05), Audit-Runner (AUD-04), Index-DDL (DB-04), Retention (AUD-09), Zertifikatserneuerung, Entitlement-Warnungen, Computed-Attribute-Refresh [P2]; [P3] Workflow-Zeitpläne. Single-Runner-Garantie je Job-Kind über Advisory-Lock; HPA-sicher; Verpasste Läufe werden nachgeholt (catch-up begrenzt).

JOB-03 [B] GET /jobs, GET /jobs/{id}, DELETE /jobs/{id} (Cancel), POST /jobs/{id}/revert (BLK-02); Jobcenter im UI; Metriken (Dauer, Fehler, Rückstau, Alter des ältesten queued Jobs).

JOB-04 [B] Große Operationen laufen als Job: Export, Import, Bulk ab 100 IDs, Merge, Template-Migration, Org-Export/-Löschung, Index-Anlage, Snapshot.
