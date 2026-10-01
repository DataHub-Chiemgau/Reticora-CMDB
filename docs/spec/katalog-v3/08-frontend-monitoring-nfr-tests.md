KATALOGAUSZUG (Abschnitte 28, 38, 43, 44, 45, 46)
28. Monitoring &amp; Zeitreihen

MON-01 [B] metric_sample: Hypertable mit 1-Tages-Chunks, Kompression nach 7 Tagen, Retention 400 Tage, Continuous Aggregate metric_sample_1h, RLS (TEC-06 Spike).

MON-02 [B] /cis/{id}/metrics und Metrik-Linie in der Detailansicht. Baseline-Metriken: Erreichbarkeit (Sweep), Interface-Zähler aus dem SNMP-Poll (DIS-10), USV-/PDU-Werte aus den Power-Plugins.

MON-03 [P3] Konfigurierbares Polling (Intervalle, OIDs) für SNMP/Redfish; Charts mit Schwellwerten.

MON-04 [P3] Alerting-Regeln (MET-45) lösen Tickets, Webhooks, Benachrichtigungen (Abschnitt 41) oder Automationen aus; überwacht werden USV, PDU, Interface-Zähler, Erreichbarkeit; Health nach LCY-06.

38. Frontend &amp; UX

UI-01 bis UI-10 [B] wie v2 (Design-System, Login, Dashboard, CI-Liste, CI-Detail mit Provenienz-Sicht nach CI-10, Rack-SVG, Topologie, Seiten für Discovery-Scopes/Webhooks/Export/Einstellungen, Audit-Viewer, Command-Palette, Skeletons/Undo, Tastaturbedienbarkeit). UI-06 ergänzt: bei &gt; 2.000 Knoten Startdialog mit Standort-/Typfilter; Impact-Simulation mit Mehrfachauswahl (IMP-05).

UI-11 [P2] Admin-Oberflächen für Typen, Vererbung, Attribute, Sets, Templates, Regeln (Klick-Builder), Form Builder, Preview, Draft/Publish, Import-Mapping, Indexstatus (DB-04).

UI-12 [P2] Geführte Anlage mit Kacheln und Progressive Disclosure; normale Nutzer sehen keine Metamodell-Begriffe.

UI-13 [P4] Rollenbasierte Dashboards; Mobil/Feld mit Scan, GPS, Unterschrift; offline mit Synchronisation: Feld-granulare Zusammenführung, Konflikte als Liste zur manuellen Auflösung, kein stiller Verlust; Raumpläne, Heatmaps, GIS.

UI-14 [P2–P4, A] Oberflächen für alle Module der jeweiligen Phase; Self-Service-Portal und Access-Reviews [A].

UI-15 [P2] Alltagsflows ohne Metadaten-Wissen (finden/anlegen, empfangen, verschieben, zuweisen, zurücknehmen, reservieren, scannen, Beziehung anlegen, Historie).

UI-16 [B] Review-Inbox (REC-08) mit Diff-Ansicht, Einzel- und Bulk-Auflösung; Jobcenter (JOB-03); Benachrichtigungscenter (NTF-04).

UI-17 [B] Self-Signup- und Onboarding-Flow (TLC-01, INS-06): Registrierung → Verifikation → erster Collector → Scope bestätigen → erste CIs; Fortschrittsanzeige.

UI-18 [B] Browser: aktuelle und Vorversion von Chrome, Edge, Firefox, Safari; mobil Safari und Chrome; WCAG 2.1 AA; DE/EN-Kataloge inklusive Metamodell-Labels (i18n-Maps).

43. Nichtfunktionale Anforderungen

NFR-01 [B] Auslegung (CH15): 10.000 Objekte (CI + Asset) pro Org typisch, 50.000 Headroom; pro Org 50.000 Interfaces, 200.000 IPs, 100.000 Beziehungen; 500 Orgs pro SaaS-Instanz in Phase 1, 5.000 in Phase 5.

NFR-02 [B] Latenz p95 bei 10.000 Objekten: Listen und Suche &lt; 500 ms; CI-Detail &lt; 300 ms; Topologie 2.000 Knoten &lt; 3 s; Impact Tiefe 20 &lt; 2 s; indizierte Attributfilter &lt; 500 ms; Bulk 500 IDs &lt; 30 s; Rack-Layout &lt; 300 ms.

NFR-03 [B] Ingest: 200 DeviceRecords/s pro Instanz in Phase 1, 1.000/s in Phase 5; Verzögerung Record → sichtbar &lt; 30 s; Spool-Flush 10.000 Records &lt; 5 min.

NFR-04 [B] Verfügbarkeit SaaS 99,5 % (Phase 1), 99,9 % (Phase 5); angekündigte Wartungsfenster; Collector puffert Ausfälle bis 24 h ohne Verlust.

NFR-05 [B] RPO 15 min, RTO 4 h; Dokumente in S3 versioniert und repliziert.

NFR-06 [B] Retention: audit_log 10 Jahre, ci_change/Snapshots 3 Jahre, Events 30 Tage, Backups 35 Tage, Logs 30 Tage, Metriken 400 Tage, Soft-Delete 90 Tage.

NFR-07 [B] Browser und Barrierefreiheit nach UI-18.

NFR-08 [B] Sicherheit nach SEC-11.

NFR-09 [B] Datenresidenz: SaaS-Daten inklusive Backups, Logs und Suchindex in der EU; Subprozessoren dokumentiert und AVV-fähig.

NFR-10 [Q] Lasttest gegen NFR-01 bis NFR-03 vor G1 mit Seed-Daten (SIM-01 skaliert auf 10.000 Objekte); Ergebnisse im Repo.

44. Seed, Simulation, Tests, Definition of Done, Reihenfolge, Gates
Seed und Simulation

SIM-01 [B] Seed: Demo-Org (Pro, admin@demo.local) und Org „acme" für Isolationstests, jeweils mit zwei Clients; Standortkette mit 2 Racks, 40 CIs mit LLDP, PDU, Mounts; 2.000 Metriken; Collector mit Scopes; 3 Reviews; Echo-Webhook; VRF-Beispiel. [P2] zusätzlich Templates, Sets, Regeln, Layouts, Assets, Lager, Tickets. Skalierungs-Seed für NFR-10.

SIM-02 [B] RETICORA_SIMULATE=true erzeugt 30 deterministische Geräte über den echten Ingest.

Tests

TST-01 [B] Unit-Tests: mindestens 20 Reconciliation-Fälle (inkl. Blocklists, observed_at-Ordering, Override-Konflikt, Resurrect); Topologie mit LLDP, FDB, Trunk; Audit-Kette und Outbox; Cursor, Crypto, CSV; Regel-AST-Konformität (MET-45); Face-aware Rack-Exclusion; [P2] Vererbung, Template-Komposition (MET-16), Feldrechte, DATEV.

TST-02 [B] Integrationstests gegen echte Dienste: RLS inkl. Client-/Site-/Team-Scope und WITH CHECK, Ingest bis Webhook, Idempotenz, mTLS und Zertifikatserneuerung, Impact mit Mehrfachausfall, Entitlement inkl. unlicensed_ci und Ablauf, Offline-Detector, Jobs/Scheduler-Single-Runner, Benachrichtigungen (SMTP-Stub), Laufzeit-DDL.

TST-03 [B] Playwright: Signup, Login, CI, Review-Inbox, Drag &amp; Drop mit Undo, Topologie, Export, Palette, Onboarding; [P2] geführte Anlage mit Conditional Fields, Form Builder, Import.

TST-04 [Q] Repository-Tests (sqlc bzw. pgx), Handler-Tests, Vitest, golangci-lint, eslint/prettier, GitHub Actions, Abhängigkeits-Scans; Mocks allein sind kein Nachweis.

Definition of Done und Reihenfolge

DOD-01 [Q] Pro Feature: OpenAPI, Implementierung, RLS, Audit/Events, i18n, Tests, keine Lint-Befunde, Dokumentation, Traceability-Eintrag (Abschnitt 46) im selben PR. Ein Arbeitspaket = ein PR mit 0,5–2 Tagen.

SEQ-01 [Q] Reihenfolge:

Phase 1: Epic A → Epic B parallel zu Epic D → Epic C → Lasttest (NFR-10) → Sicherheitsreview → G1.
Phase 2: Metamodell (Abschnitt 11) → Verträge, Asset/Lager/Stocktake, Dokumente → Ticketing Essential/Standard → Import, Bulk, Views → G2.
Phase 3: Epic E (Monitoring) → Workflows → Epic F (Compliance, DSGVO-Workflows, SMS/Pager) → G3.
Phase 4: Agent, Mobile, SAML/SCIM, Reseller, KMS, Management-Module → G4.
Phase 5: Epic H → G5.
Add-ons IGA und KI (Epic G) jeweils nach G2, unabhängig voneinander → GA.
Gates

GATE-01 [Q] Kein offenes Critical im jeweiligen Gate.

GATE-02 [Q] Alle Anforderungen mit dem Tag der Phase sind PASS; SOLL-Punkte dürfen WARN sein; [Q]-Punkte der Phase sind PASS.

GATE-03 [Q] G1 zusätzlich: Lasttest (NFR-10) bestanden, Pentest ohne offene High/Critical, Isolationstest (TEN-09) vollständig, Install-Smoke-Test auf beiden Plattformen.

GATE-04 [Q] GA zusätzlich: Sicherheitsreview der Connectoren (COL-07) bzw. Provider-Prüfung (AI-02).

GATE-05 [Q] Kein Gate wird durch [O]-Punkte beeinflusst.

45. Abnahme

ABN-01 [Q] Szenarien: (a) CMDB-Szenario (34 Schritte) und (b) System-Szenario (20 Schritte) werden aus V §65–66 unverändert nach docs/acceptance/ übernommen und sind Bestandteil dieses Katalogs (Anhang, vor G1 einzufrieren). (c) Metamodell-Szenario [P2]: Typ mit Vererbung anlegen; Attribute Set zuordnen; zwei Templates kombinieren; Regel im Klick-Builder definieren; Layout pro Rolle erstellen; Preview prüfen; publizieren; Anlage durch einen normalen Nutzer; Template-Version migrieren, Overrides bleiben erhalten. (d) Onboarding-Szenario [B]: Self-Signup, Collector-Enrollment, Scope bestätigen, 30 simulierte Geräte, Review auflösen, Impact abfragen, Export laden. (e) Konflikt-Szenario [B]: Override setzen, abweichende Discovery, override_conflict sichtbar, accept und dismiss.

ABN-02 [Q] Performance-Sanity gegen NFR-02 für Listen, Suche, Attributfilter, Beziehungssuche, Topologie, Impact, Bulk; Prüfung auf N+1 und fehlende Indizes.

ABN-03 [Q] Nebenläufigkeit (Reservierung, Discovery während Bearbeitung, Transfer, doppelter Ingest, paralleler Merge) und Datenintegrität (keine Orphans, Duplikate, negativen Bestände, Cross-Tenant-Referenzen, Typ-Zyklen, Location-Zyklen, Kompositions-Zyklen).

46. Traceability

| Abschnitt | Epic | Testarten |
|---|---|---|
| 3, 39 Installation/Betrieb | A, H | Smoke, E2E, Ausfalltests |
| 5–8 Mandanten, Auth, Authz, Entitlements | B | Integration (RLS), Handler, Unit |
| 9–18 Datenmodell, Metamodell, CI, Netz, Rack, Beziehungen, Impact, Audit | B, C | Unit, Repository, Integration, Playwright |
| 19–22 Collector, Discovery, Reconciliation, Overrides | D | Unit (Reconciliation), Integration (mTLS, Ingest), Simulation |
| 23–27 Asset, Lager, Stocktake, Module, Tickets | B (Phase 2) | Unit, Integration, Playwright |
| 28 Monitoring, 30 Workflows, 31 Sicherheit/Compliance | E, F | Integration, Ausfalltests, Pentest |
| 29 Agent | F | Integration, Paketsignatur-Test |
| 32–33 IGA, KI | G | Integration, Sicherheitsreview |
| 34–37 Events, API, GraphQL, Suche/Export/Import | B, C | Parity, Handler, Integration |
| 38 Frontend | C | Vitest, Playwright, axe |
| 40–43 Mandantenlebenszyklus, Benachrichtigungen, Jobs, NFR | B, H | Integration, Lasttest |

Jede Anforderungs-ID wird in docs/traceability.csv (ID, Tag, Epic, Testdatei) geführt; die CI prüft, dass jede ID mit Tag [B]/[Pn]/[A] einen Eintrag hat (GLO-11).
