# Entscheidungen – Phase 0/1

Stand: 2026-10-01. Gehört zu [`implementierungsplan.md`](implementierungsplan.md). Die Empfehlung ist ein Vorschlag des Plans und keine neue Anforderung.

**Freigabe vom 2026-10-01:** Der Auftraggeber hat angewiesen, nach den Empfehlungen vorzugehen. Daraus folgt:

- **entschieden (22):** Die Empfehlung legt eine Option fest; diese gilt jetzt verbindlich für den Plan.
- **vorläufig entschieden (4):** Die Empfehlung legt eine Übergangsregel bis zum Eingang eines fehlenden Textes fest; diese gilt jetzt.
- **offen (10):** Die Empfehlung verlangt eine Lieferung oder Bestätigung durch Dritte. Die Übergangsregel aus der Empfehlung gilt; die Lieferung steht als Aufgabe im Abschnitt [Offene Aufgaben](#offene-aufgaben).

| Nr. | Thema | Status | Sperrt / betrifft | WPs mit Verweis |
|---|---|---|---|---:|
| [E-01](#e-01) | Fehlende Anforderungstexte der Katalogteile 02, 04, 05 und 06 | offen | Blockiert alle WPs mit diesem Verweis (Start ab M0.1). | 130 |
| [E-02](#e-02) | Fehlende v2-Originaltexte (Verweise „wie v2“, „V §…“) | offen | Teilumfänge von c-ui-*, WP-152 (`d-protocols`), WP-212 (`g1-abnahme`), WP-231 (`m-workflow`), WP-232 (`m-iga`), WP-237 (`m-ui-mods`), WP-219 (`m-relay`). | 9 |
| [E-03](#e-03) | Umgang mit vorhandenem Code späterer Phasen im G1-Release | entschieden | Steuert den gesamten Meilenstein M-S und den G1-Umfang. | 37 |
| [E-04](#e-04) | Teilbericht 03 nicht konsolidiert; N/P-IDs des Metamodells | offen | Keiner für M0; Namensschema für WP-142 (`b-met-ddl`). | 3 |
| [E-05](#e-05) | Fehlende oder abweichende Tags | entschieden | Keiner (Annahme konservativ). | 1 |
| [E-06](#e-06) | Zuordnung der IDs zu Epics A–D | entschieden | Spalte `epic` in `docs/traceability.csv` (WP-005 (`trace`)). | 1 |
| [E-07](#e-07) | Vorziehen von M0 vor Epic A (Abweichung von SEQ-01) | entschieden | Nachweis SEQ-01 in WP-213 (`g1-gate`). | 0 |
| [E-08](#e-08) | TEN-04: GUC-Namen, Semantik leerer Scopes, Systempfade | entschieden | WP-008 (`tenant-core`) und alle WithTenant-WPs. | 2 |
| [E-09](#e-09) | Schreiben von Zeilen mit `client_id`/`site_id` NULL | entschieden | WP-023 (`rls-client`), WP-027 (`rls-site`). | 2 |
| [E-10](#e-10) | Tabellen ohne Org-Spalte und globale Katalogzeilen | entschieden | WP-024 (`rls-global`), WP-065 (`rls-matrix`). | 1 |
| [E-11](#e-11) | Semantik des Team-Scopes | vorläufig entschieden | WP-009 (`scope-resolve`), WP-029 (`rls-team`). | 2 |
| [E-12](#e-12) | ENT-04: Wer darf Entitlements schreiben? | entschieden | WP-074 (`ent-operator`). | 1 |
| [E-13](#e-13) | RBA-02-Rollenmatrix und AUT-04-Schlüsselformat | vorläufig entschieden | WP-046 (`role-model`), WP-067 (`apikeys`). | 2 |
| [E-14](#e-14) | CH26 (V): Keycloak-Org-Attribut, MFA-Pflicht, Brokering | entschieden | WP-043 (`auth-org`), WP-044 (`kc-admin`). | 2 |
| [E-15](#e-15) | CH16 (V): PII-Vault, Paket `internal/pii`, Schlüsselrotation | offen | WP-141 (`b-sec-rotation`), WP-213 (`g1-gate`) (REP-01-Strukturtest). | 2 |
| [E-16](#e-16) | SEC-10: Zahlenwerte für GraphQL-Limits | vorläufig entschieden | WP-184 (`c-gql-bff`). | 1 |
| [E-17](#e-17) | CH15: Skalierungsziel bestätigen | offen | WP-093 (`a-preflight`), WP-203 (`lt-seed`), WP-206 (`lt-run`). | 3 |
| [E-18](#e-18) | CH27 (V): impact_direction | entschieden | WP-127 (`b-rel-types`), WP-176 (`c-imp-direction`). | 2 |
| [E-19](#e-19) | CH28 (V): gemeinsamer Location-Baum | entschieden | WP-026 (`loc-model`) und Folge-WPs. | 1 |
| [E-20](#e-20) | CH29 (V): Hostname-Treffer nur als Review | entschieden | WP-086 (`identity`). | 1 |
| [E-21](#e-21) | CH30 (V)/DIS-02: Geräteprofilliste | entschieden | WP-151 (`d-classify`). | 1 |
| [E-22](#e-22) | CH21 (V): Ausgestaltung `unlicensed_ci` | entschieden | WP-073 (`ent-unlic`). | 1 |
| [E-23](#e-23) | Abgrenzung GLO-13 (reservierte Namensräume) zu CI-10 (Speichermodell) | offen | WP-091 (`glo13-ns`). | 1 |
| [E-24](#e-24) | NET-08: Eindeutigkeit bei VLAN ohne Site | entschieden | WP-081 (`vlan`). | 1 |
| [E-25](#e-25) | API-05/JOB: verbindliche Pfade, Revert, Snapshot | entschieden | WP-053 (`loc-api`), WP-168 (`c-paths`), WP-181 (`c-bulk`), WP-182 (`c-jobs-api`), WP-183 (`c-jobs-adopt`). | 6 |
| [E-26](#e-26) | Änderung der bereits ausgelieferten Migration 000056 | entschieden | WP-052 (`mig56`). | 1 |
| [E-27](#e-27) | GATE-03 „beide Plattformen“ und Umgang mit WARN (GATE-02) | entschieden | WP-092 (`a-matrix`), WP-211 (`g1-install`), WP-213 (`g1-gate`). | 3 |
| [E-28](#e-28) | Pentest: Dienstleister, Umfang, Termin; NFR-08 | entschieden | WP-210 (`sr-pentest`). | 1 |
| [E-29](#e-29) | OPS-04: Werkzeug für WAL-Archivierung und Basisbackups | offen | WP-100 (`a-backup`), WP-101 (`a-restore`). | 1 |
| [E-30](#e-30) | Traceability-Prüfung: ab wann blockierend? | entschieden | WP-005 (`trace`), WP-213 (`g1-gate`). | 2 |
| [E-31](#e-31) | Werkzeuge für Abhängigkeits-/Container-Scan, Signatur und Lasttest | entschieden | WP-004 (`dep-scan`), WP-102 (`a-release`), WP-204 (`lt-harness`), WP-209 (`sr-review`). | 4 |
| [E-32](#e-32) | REC-03: Rang der Quelle IPMI | offen | WP-058 (`rec-decide`), WP-220 (`m-srcpolicy`). | 2 |
| [E-33](#e-33) | TLC-01: Captcha-Anbieter, Wegwerf-Domain-Liste, Trial-Werte | offen | WP-111 (`b-signup`), WP-112 (`b-signup-protect`). | 2 |
| [E-34](#e-34) | ENT-06 (V)/SIM-01: Planmatrix und Demo-Plan | vorläufig entschieden | Folgeänderung zu WP-071 (`ent-model`), WP-203 (`lt-seed`). | 2 |
| [E-35](#e-35) | NFR-09: Inhalt der Subprozessorenliste | offen | WP-105 (`a-reference`). | 1 |
| [E-36](#e-36) | DB-05: Zeilen für Vertrag, installierte Software und Health-Findings | entschieden | WP-077 (`db05-asset`), WP-080 (`db05-lcy`), WP-230 (`m-agent`). | 3 |

## E-01

**Thema:** Fehlende Anforderungstexte der Katalogteile 02, 04, 05 und 06

**Status:** offen (2026-10-01)

**Festlegung:** Keine Festlegung möglich: Die Texte kann nur der Auftraggeber liefern. Die Übergangsregel aus der Empfehlung gilt ab sofort: Nur M0.0-WPs ohne Verweis auf E-01 starten (WP-001, WP-002, WP-003, WP-005, WP-006, WP-007). WP-004 und alle WPs ab M0.1 warten auf die Texte.

**Aufgabe:** Katalogteile 02, 04, 05 und 06 in `docs/spec/katalog-v3/` nachliefern. Zuständig: Auftraggeber/Product Owner. Termin: vor Beginn von M0.1 (WP-008).

**Frage:** Für die IDs der Bereiche Mandanten/Auth/Entitlements (TEN, AUT, RBA, ENT, TLC), CI/Netz/Rack/Beziehungen/Impact (CI, NET, RCK, REL, IMP, LCY), Collector/Discovery/Reconciliation (COL, DIS, REC, OVR, TOP) sowie Audit/Sicherheit/Events (AUD, SEC, EVT) liegen in `docs/spec/katalog-v3/` keine Anforderungstexte vor. Die WPs stützen sich daher nur auf Befundtexte und Teilberichte des Audits.

**Optionen:**

1. Fehlende Dateien `02-…`, `04-…`, `05-…`, `06-…` in `docs/spec/katalog-v3/` nachliefern (bevorzugt).
2. WPs ausschließlich anhand der Befundtexte umsetzen (Risiko: Auslegung statt Anforderung).

**Empfehlung:** Texte vor Beginn von M0.1 nachliefern. Bis dahin dürfen nur WPs ohne diesen Verweis (M0.0) starten; WPs mit Verweis auf diesen Eintrag prüfen ihre Akzeptanzkriterien gegen den nachgelieferten Text, bevor sie umgesetzt werden.

**Blockiert / Auswirkung:** Blockiert alle WPs mit diesem Verweis (Start ab M0.1).

**Quelle:** docs/audit/99-gesamtbericht.md (Quellenlage); Aufgabenstellung „Anforderungstexte in docs/spec/katalog-v3/ referenziert“

**Betroffene IDs:** AGT-05, AI-02, API-04, API-05, API-06, API-07, AST-01, AST-05, AST-06, AUD-03, AUD-04, AUD-05, AUD-07, AUD-09, AUT-01, AUT-02, AUT-03, AUT-04, AUT-05, AUT-06, AUT-08, AUT-09, AUT-10, CI-01, CI-02, CI-03, CI-04, CI-05, CI-06, CI-09, CI-10, CI-11, CI-12, CI-13, COL-01, COL-02, COL-04, COL-05, COL-06, COL-07, COL-08, DB-01, DB-04, DB-05, DIS-01, DIS-02, DIS-03, DIS-04, DIS-05, DIS-08, DIS-09, DIS-10, ENT-01, ENT-02, ENT-03, ENT-04, ENT-05, ENT-06, ENT-07, ENT-08, EVT-01, EVT-02, EXP-01, GATE-03, GLO-12, GLO-13, GQL-01, IGA-01, IGA-02, IGA-03, IGA-04, IMP-01, IMP-02, IMP-03, IMP-04, IMP-05, IMP-06, IMP-07, IMP-08, IMP-09, LCY-01, LCY-02, LCY-03, LCY-05, LOC-01, LOC-02, LOC-03, LOC-10, MET-11, MET-14, MET-20, MET-30, MET-31, MET-32, MET-33, MET-34, MET-40, MET-41, MET-42, MET-43, MET-44, MET-54, MGT-01, MGT-03, MGT-04, MGT-05, MGT-06, MGT-07, MGT-11, MON-04, NET-01, NET-02, NET-03, NET-04, NET-05, NET-08, NET-09, NFR-01, NFR-02, NFR-03, NFR-04, OPS-05, OPS-06, OVR-01, OVR-02, OVR-03, PRI-07, RBA-01, RBA-02, RBA-03, RBA-04, RBA-05, RBA-06, RBA-08, RCK-01, RCK-02, RCK-03, REC-01, REC-02, REC-03, REC-04, REC-05, REC-06, REC-07, REC-08, REC-09, REC-10, REC-11, REC-12, REL-01, REL-02, REL-03, REL-04, REL-05, REL-08, REL-09, SEC-01, SEC-06, SEC-07, SEC-08, SEC-10, SEC-11, SRC-01, TEC-06, TEC-12, TEC-15, TEN-01, TEN-02, TEN-03, TEN-04, TEN-05, TEN-06, TEN-09, TEN-10, TKT-01, TLC-04, TOP-01, TOP-02, TST-02, TST-04, WFL-01, WFL-02

**Betroffene WPs:** WP-004 (`dep-scan`), WP-007 (`rls-cat`), WP-008 (`tenant-core`), WP-009 (`scope-resolve`), WP-010 (`wt-cmdb-a`), WP-011 (`wt-cmdb-b`), WP-012 (`wt-ident`), WP-013 (`wt-net`), WP-014 (`wt-loc`), WP-015 (`wt-search`), WP-016 (`wt-ops`), WP-017 (`wt-asset`), WP-018 (`wt-stock`), WP-019 (`wt-mod-a`), WP-020 (`wt-mod-b`), WP-021 (`wt-mod-c`), WP-022 (`wt-workers`), WP-023 (`rls-client`), WP-024 (`rls-global`), WP-025 (`denorm-ci`), WP-026 (`loc-model`), WP-027 (`rls-site`), WP-029 (`rls-team`), WP-030 (`blob`), WP-031 (`topo-scope`), WP-035 (`export-scope`), WP-041 (`wt-guard`), WP-042 (`auth-status`), WP-043 (`auth-org`), WP-044 (`kc-admin`), WP-045 (`authz-map`), WP-046 (`role-model`), WP-047 (`cred-secrets`), WP-048 (`redfish-tls`), WP-049 (`egress`), WP-050 (`api-chain`), WP-057 (`manual-ovr`), WP-058 (`rec-decide`), WP-059 (`auto-paths`), WP-060 (`eff-view`), WP-062 (`spool`), WP-064 (`ddl-contract`), WP-065 (`rls-matrix`), WP-066 (`token`), WP-067 (`apikeys`), WP-068 (`rba-catalog`), WP-069 (`svc-accounts`), WP-070 (`operator`), WP-071 (`ent-model`), WP-072 (`ent-enforce`), WP-073 (`ent-unlic`), WP-074 (`ent-operator`), WP-075 (`addon`), WP-078 (`db05-rack`), WP-079 (`db05-struct`), WP-081 (`vlan`), WP-082 (`vrf`), WP-083 (`ingest-contract`), WP-084 (`obs-time`), WP-085 (`ingest-pipe`), WP-086 (`identity`), WP-087 (`resurrect`), WP-088 (`review-model`), WP-089 (`conflict`), WP-090 (`last-seen`), WP-091 (`glo13-ns`), WP-102 (`a-release`), WP-104 (`a-outage`), WP-107 (`b-org`), WP-109 (`b-tenant-scope`), WP-110 (`b-devauth`), WP-117 (`b-met-types-val`), WP-120 (`b-ci-fields`), WP-121 (`b-ci-idx`), WP-122 (`b-lcy01`), WP-123 (`b-net-if`), WP-124 (`b-net-ip`), WP-125 (`b-net-api`), WP-126 (`b-rack-rules`), WP-127 (`b-rel-types`), WP-128 (`b-rel-edges`), WP-132 (`b-audit-chain`), WP-133 (`b-audit-fields`), WP-134 (`b-met-export`), WP-136 (`b-events`), WP-141 (`b-sec-rotation`), WP-143 (`d-col-status`), WP-144 (`d-col-token`), WP-145 (`d-col-cert`), WP-146 (`d-col-nats`), WP-147 (`d-col-bundle`), WP-148 (`d-col-update`), WP-149 (`d-sweep`), WP-150 (`d-plugin`), WP-151 (`d-classify`), WP-152 (`d-protocols`), WP-153 (`d-scope`), WP-154 (`d-schedule`), WP-155 (`d-errors`), WP-157 (`d-offline`), WP-158 (`d-lldp`), WP-159 (`d-topo-refresh`), WP-160 (`d-suppress`), WP-161 (`d-power`), WP-166 (`c-ratelimit`), WP-169 (`c-ci-include`), WP-170 (`c-ci-delete`), WP-171 (`c-ci-history`), WP-172 (`c-ci-typechange`), WP-173 (`c-ci-merge`), WP-174 (`c-ci-unmerge`), WP-175 (`c-rack-api`), WP-176 (`c-imp-direction`), WP-177 (`c-imp-paths`), WP-178 (`c-ci-assetfree`), WP-179 (`c-imp-redundancy`), WP-180 (`c-imp-multi`), WP-184 (`c-gql-bff`), WP-198 (`c-ui-provenance`), WP-208 (`sr-matrix`), WP-209 (`sr-review`), WP-214 (`m-gates`), WP-215 (`m-lcy`), WP-216 (`m-net-res`), WP-217 (`m-rel-hist`), WP-218 (`m-blast`), WP-219 (`m-relay`), WP-220 (`m-srcpolicy`), WP-221 (`m-ovr-bulk`), WP-233 (`m-scim`)

## E-02

**Thema:** Fehlende v2-Originaltexte (Verweise „wie v2“, „V §…“)

**Status:** offen (2026-10-01)

**Festlegung:** Keine Festlegung möglich: Die Texte kann nur der Auftraggeber liefern. Die Übergangsregel gilt ab sofort: WPs setzen nur ausdrücklich genannte Bestandteile um und erfinden keine Funktionen.

**Aufgabe:** v2-Originaltexte (UI-01–UI-10, ABN-01, DIS-04, IGA, WFL-04, AUT-05/06, NET-04, REP-01) nachliefern. Zuständig: Auftraggeber/Product Owner. Termin: vor Epic C (UI) bzw. vor WP-212 (ABN-01).

**Frage:** Mehrere IDs verweisen auf nicht vorliegende v2-Texte: individuelle Verträge UI-01–UI-10, ABN-01 (a)/(b) aus „V §65–66“, DIS-04-Umfang „wie v2“, IGA-Details, WFL-04-Ketten, AUT-05, AUT-06, NET-04 sowie der v2-Bestandteil von REP-01.

**Optionen:**

1. v2-Abschnitte nachliefern und nach `docs/acceptance/` bzw. in den Katalog übernehmen.
2. Betroffene Teilumfänge für G1 ausdrücklich als nicht bewertet (N/P) führen.

**Empfehlung:** Nachliefern vor Epic C (UI) bzw. vor G1 (ABN-01). Bis dahin setzen die WPs nur ausdrücklich genannte Bestandteile um und erfinden keine Funktionen.

**Blockiert / Auswirkung:** Teilumfänge von c-ui-*, WP-152 (`d-protocols`), WP-212 (`g1-abnahme`), WP-231 (`m-workflow`), WP-232 (`m-iga`), WP-237 (`m-ui-mods`), WP-219 (`m-relay`).

**Quelle:** docs/audit/befunde.csv (Status N/P, Hinweise „v2-Text fehlt“)

**Betroffene IDs:** ABN-01, AUT-05, AUT-06, COL-07, DIS-04, IGA-01, IGA-02, IGA-03, IGA-04, NET-04, UI-01, UI-02, UI-03, UI-04, UI-05, UI-06, UI-07, UI-08, UI-09, UI-10, UI-13, UI-14, WFL-01, WFL-02, WFL-04

**Betroffene WPs:** WP-152 (`d-protocols`), WP-191 (`c-ui-scope`), WP-192 (`c-ui-keyboard`), WP-195 (`c-ui-topology`), WP-212 (`g1-abnahme`), WP-219 (`m-relay`), WP-231 (`m-workflow`), WP-232 (`m-iga`), WP-237 (`m-ui-mods`)

## E-03

**Thema:** Umgang mit vorhandenem Code späterer Phasen im G1-Release

**Status:** entschieden (2026-10-01)

**Festlegung:** Option 2 (Deaktivieren). WP-214 (`m-gates`) gehört zum G1-Umfang und schaltet alle Spätphasen-Module im G1-Release standardmäßig ab. WP-215 bis WP-239 werden in die jeweilige Folgephase verschoben und sind nicht G1-Umfang. Alle M0-WPs mit Verweis auf E-03 bleiben verpflichtend (WP-028, WP-029, WP-030, WP-033, WP-037, WP-038, WP-051, WP-056, WP-059, WP-063, WP-076), weil sie Mandantengrenzen, Transportsicherheit, den Override-Schutz oder [B]-IDs betreffen. Ihre Akzeptanzkriterien „Alternative nach E-03“ entfallen.

**Frage:** Module der Phasen P2–P5 und Add-ons sind bereits angebunden und haben Critical/High-Befunde (u. a. Scope-Fehler). Sollen diese vor G1 behoben oder im G1-Release deaktiviert werden?

**Optionen:**

1. Beheben: alle M-S-WPs werden vor G1 umgesetzt.
2. Deaktivieren: WP-214 (`m-gates`) schaltet die Module ab; M-S-WPs werden in die jeweilige Phase verschoben. Scope-/RLS-Fehler in M0 werden in beiden Fällen behoben, weil sie Mandantengrenzen betreffen.
3. Gemischt je Modul.

**Empfehlung:** Deaktivieren über WP-214 (`m-gates`) (geringstes G1-Risiko); Scope-/RLS-Korrekturen aus M0 bleiben verpflichtend. Bei „beheben“ zählen die M-S-WPs zum G1-Umfang.

**Blockiert / Auswirkung:** Steuert den gesamten Meilenstein M-S und den G1-Umfang.

**Quelle:** docs/audit/99-gesamtbericht.md; GATE-01 (kein offenes Critical)

**Betroffene IDs:** AGT-01, AGT-02, AGT-03, AGT-04, AGT-05, AGT-06, AI-01, AI-02, API-06, AST-01, AST-02, AST-04, AST-05, AST-06, AST-07, AUT-08, COL-07, GATE-04, IGA-01, IGA-02, IGA-03, IGA-04, IMP-08, INV-01, INV-03, LCY-02, LCY-03, LOC-08, MET-12, MET-14, MET-51, MGT-01, MGT-02, MGT-03, MGT-04, MGT-05, MGT-06, MGT-07, MON-03, MON-04, NET-09, NFR-04, NTF-05, OVR-02, OVR-03, RBA-05, REC-04, REC-11, REL-09, SRC-03, STK-01, STK-02, STK-03, STK-04, STK-05, TEC-12, TEN-05, TEN-06, TKT-01, TKT-02, UI-11, UI-13, UI-14, UI-15, WFL-01, WFL-02, WFL-04

**Betroffene WPs:** WP-028 (`rls-mod-obj`), WP-029 (`rls-team`), WP-030 (`blob`), WP-033 (`search-os`), WP-037 (`ai-scope`), WP-038 (`agent-enroll`), WP-051 (`agent-transport`), WP-056 (`inst-attr`), WP-059 (`auto-paths`), WP-063 (`maint-sent`), WP-076 (`ai-optin`), WP-214 (`m-gates`), WP-215 (`m-lcy`), WP-216 (`m-net-res`), WP-217 (`m-rel-hist`), WP-218 (`m-blast`), WP-219 (`m-relay`), WP-220 (`m-srcpolicy`), WP-221 (`m-ovr-bulk`), WP-222 (`m-asset-core`), WP-223 (`m-assign`), WP-224 (`m-movement`), WP-225 (`m-stock`), WP-226 (`m-order`), WP-227 (`m-inventory`), WP-228 (`m-maint`), WP-229 (`m-sla`), WP-230 (`m-agent`), WP-231 (`m-workflow`), WP-232 (`m-iga`), WP-233 (`m-scim`), WP-234 (`m-monitoring`), WP-235 (`m-ui-p2`), WP-236 (`m-ui-meta`), WP-237 (`m-ui-mods`), WP-238 (`m-loc-ext`), WP-239 (`m-met-ref`)

## E-04

**Thema:** Teilbericht 03 nicht konsolidiert; N/P-IDs des Metamodells

**Status:** offen (2026-10-01)

**Festlegung:** Vorläufig gilt Option 2: Die Teil-03-Tabelle bleibt direkte Quelle, gekennzeichnet in `abdeckung.csv`. Die empfohlene Konsolidierung ist eine Audit-Aufgabe und nicht Teil dieses Plans. Die fehlenden Texte werden zusammen mit E-01 angefordert.

**Aufgabe:** Teil 03 in `docs/audit/befunde.csv` und `docs/audit/99-gesamtbericht.md` konsolidieren; Texte DB-01, MET-20, MET-30–34, MET-40–44 mit E-01 anfordern. Zuständig: Audit. Termin: vor WP-142 (`b-met-ddl`), spätestens vor G1.

**Frage:** Die Ergebnisse aus Teil 03 (DB-02–DB-05, LOC-*, MET-*) sind nicht in `befunde.csv` übernommen. DB-01, MET-20, MET-30–MET-34 und MET-40–MET-44 sind N/P (kein Wortlaut). Das Namenssuffix nach `idx_attr_` (DB-04) ist nicht spezifiziert.

**Optionen:**

1. Teil 03 in `befunde.csv` nachtragen und fehlende Texte nachliefern.
2. Teil-03-Tabelle direkt als Quelle verwenden (so in diesem Plan).

**Empfehlung:** Konsolidierung nachholen (Abdeckung ist in `abdeckung.csv` mit Quelle „Teil 03“ gekennzeichnet); fehlende Texte mit E-01 gemeinsam anfordern.

**Blockiert / Auswirkung:** Keiner für M0; Namensschema für WP-142 (`b-met-ddl`).

**Quelle:** docs/audit/03-datenmodell-standorte-metamodell.md

**Betroffene IDs:** DB-01, DB-04, LOC-08, MET-12, MET-15, MET-20, MET-30, MET-31, MET-32, MET-33, MET-34, MET-40, MET-41, MET-42, MET-43, MET-44, MET-51

**Betroffene WPs:** WP-142 (`b-met-ddl`), WP-238 (`m-loc-ext`), WP-239 (`m-met-ref`)

## E-05

**Thema:** Fehlende oder abweichende Tags

**Status:** entschieden (2026-10-01)

**Festlegung:** Option 2: Die Planannahme [B] ist bestätigt. Liefert der Katalog später ein abweichendes Tag, wird das WP in den passenden Meilenstein verschoben.

**Frage:** Mehrere IDs haben im Befundkorpus kein Tag („nicht angegeben“: AUD-*, SEC-*, EVT-*) oder nur „nicht geliefert“ (DB-02–DB-05); PRI-07 ist ohne Phasenbezug. Der Plan behandelt sie als Phase 1 [B], wenn das Thema ein Fundament betrifft.

**Optionen:**

1. Tags im Katalog festlegen.
2. Planannahme [B] bestätigen.

**Empfehlung:** Planannahme bestätigen; bei abweichendem Tag WP in den passenden Meilenstein verschieben.

**Blockiert / Auswirkung:** Keiner (Annahme konservativ).

**Quelle:** docs/audit/befunde.csv (Spalte tag)

**Betroffene IDs:** COL-08, PRI-07

**Betroffene WPs:** WP-146 (`d-col-nats`)

## E-06

**Thema:** Zuordnung der IDs zu Epics A–D

**Status:** entschieden (2026-10-01)

**Festlegung:** Option 1: Zuordnung bestätigt (A = Installation/Betrieb, B = Fundament, C = API/Kernfunktionen/UI/E2E, D = Collector/Discovery). Die Traceability-Spalte `epic` übernimmt sie.

**Frage:** SEQ-01 nennt Epic A–D, der Katalog definiert deren Inhalt aber nicht (einzige Erwähnung: TEC-06-Spike „in Epic A“). Der Plan ordnet zu: A = Installation/Betrieb, B = Fundament (Mandanten, Auth, Daten-/Metamodell, Jobs, Audit, Events, Benachrichtigungen), D = Collector/Discovery, C = API, Kernfunktionen, UI und E2E.

**Optionen:**

1. Zuordnung bestätigen.
2. Verbindliche Epic-Definition nachliefern.

**Empfehlung:** Zuordnung bestätigen; die Traceability-Spalte `epic` übernimmt sie.

**Blockiert / Auswirkung:** Spalte `epic` in `docs/traceability.csv` (WP-005 (`trace`)).

**Quelle:** docs/spec/katalog-v3/08-frontend-monitoring-nfr-tests.md:77; docs/spec/katalog-v3/01-installation-stack.md:6

**Betroffene IDs:** DOD-01, GLO-11

**Betroffene WPs:** WP-005 (`trace`)

## E-07

**Thema:** Vorziehen von M0 vor Epic A (Abweichung von SEQ-01)

**Status:** entschieden (2026-10-01)

**Festlegung:** Option 2: M0 wird formal als Teil von Epic A/B/D geführt. M0-WPs tragen in der Traceability ihr fachliches Epic (Spalte „Epic (Traceability)“ der WP-Blöcke). WP-213 weist die SEQ-01-Reihenfolge je Epic nach.

**Frage:** Die Aufgabenstellung verlangt M0 vor der SEQ-01-Reihenfolge. Dadurch liegen Fundament-Arbeiten (u. a. RLS, Auth, DB-05, Ingest) vor Epic A, und der TEC-06-Spike (laut Katalog in Epic A) liegt in M0.1a.

**Optionen:**

1. Abweichung akzeptieren und im G1-Nachweis zu SEQ-01 dokumentieren.
2. M0 formal als Teil von Epic A/B/D führen.

**Empfehlung:** Option 2: M0-WPs tragen in der Traceability ihr fachliches Epic; der G1-Nachweis zeigt die Reihenfolge je Epic.

**Blockiert / Auswirkung:** Nachweis SEQ-01 in WP-213 (`g1-gate`).

**Quelle:** Aufgabenstellung; SEQ-01

**Betroffene IDs:** –

**Betroffene WPs:** –

## E-08

**Thema:** TEN-04: GUC-Namen, Semantik leerer Scopes, Systempfade

**Status:** entschieden (2026-10-01)

**Festlegung:** Option 1 (fail-closed): Ein leerer Client-/Site-/Team-Scope bedeutet keinen Zugriff; org-weiter Zugriff nur über ein explizites Merkmal. Systempfade laufen über `WithSystem` mit kommentierter Freigabeliste (WP-041). Die GUC-Namen sind nicht Teil der Empfehlung; sie folgen dem TEN-04-Text (E-01).

**Frage:** Namen der Sitzungsvariablen für Org/Client/Site/Team und die Bedeutung eines leeren Scopes sind ohne TEN-04-Text nicht festgelegt. Ebenso ist offen, welche Systempfade (Migration, Scheduler, Operator) org-übergreifend lesen dürfen.

**Optionen:**

1. Leerer Scope = kein Zugriff, org-weit nur per explizitem Merkmal (fail-closed).
2. Leerer Scope = org-weit (fail-open; widerspricht CH11/CH25).

**Empfehlung:** Fail-closed; Systempfade über `WithSystem` mit kommentierter Freigabeliste (WP-041 (`wt-guard`)).

**Blockiert / Auswirkung:** WP-008 (`tenant-core`) und alle WithTenant-WPs.

**Quelle:** TEN-04, TEN-06; CH11, CH25

**Betroffene IDs:** TEN-04, TEN-06

**Betroffene WPs:** WP-008 (`tenant-core`), WP-041 (`wt-guard`)

## E-09

**Thema:** Schreiben von Zeilen mit `client_id`/`site_id` NULL

**Status:** entschieden (2026-10-01)

**Festlegung:** Option 1: Nur Principals mit org-weitem Scope dürfen Zeilen mit `client_id`/`site_id` NULL schreiben.

**Frage:** Darf ein auf Clients/Sites eingeschränkter Principal Zeilen ohne Client/Site anlegen oder ändern?

**Optionen:**

1. Nein; nur org-weite Principals dürfen NULL schreiben.
2. Ja, NULL bedeutet org-weit sichtbar.

**Empfehlung:** Nein (Option 1), da sonst gescopte Nutzer Daten außerhalb ihres Bereichs erzeugen.

**Blockiert / Auswirkung:** WP-023 (`rls-client`), WP-027 (`rls-site`).

**Quelle:** TEN-05-Befund; CH11, CH25

**Betroffene IDs:** AST-01, MGT-03, MGT-11, TEC-12, TEN-04, TEN-05, WFL-01

**Betroffene WPs:** WP-023 (`rls-client`), WP-027 (`rls-site`)

## E-10

**Thema:** Tabellen ohne Org-Spalte und globale Katalogzeilen

**Status:** entschieden (2026-10-01)

**Festlegung:** Regel nach Empfehlung: Globale Kataloge sind für die App-Rolle schreibgeschützt; alle anderen Tabellen erhalten eine Org-Spalte mit RLS. WP-024 dokumentiert die Einordnung je Tabelle im PR.

**Frage:** Fünf Tabellen haben keine eigene Org-Spalte, fünf eine nullable; globale Katalogzeilen (z. B. System-CI-Typen) sind schreibbar.

**Optionen:**

1. Org-Spalte ergänzen und RLS anwenden.
2. Als globale, schreibgeschützte Tabelle mit dokumentierter Ausnahme führen.

**Empfehlung:** Je Tabelle entscheiden; globale Kataloge read-only für die App-Rolle, sonst Org-Spalte.

**Blockiert / Auswirkung:** WP-024 (`rls-global`), WP-065 (`rls-matrix`).

**Quelle:** TEN-02/TEN-05-Befund; docs/audit/00-schema-ist.md

**Betroffene IDs:** TEN-02, TEN-05

**Betroffene WPs:** WP-024 (`rls-global`)

## E-11

**Thema:** Semantik des Team-Scopes

**Status:** vorläufig entschieden (2026-10-01)

**Festlegung:** Option 1 (Schnittmenge, restriktiver) gilt, bis der TEN-Text vorliegt (E-01). Weicht der Text ab, passt ein Folge-WP das Prädikat aus WP-029 an.

**Frage:** CH25 verlangt Team-Scopes in RLS; welche Objekte einem Team gehören (Ticket-Team, Trainingszuweisung, Desk über Raum) und ob Team-Scope zusätzlich oder alternativ zu Client/Site wirkt, ist ohne TEN-Text offen.

**Optionen:**

1. Team wirkt zusätzlich (Schnittmenge).
2. Team wirkt alternativ (Vereinigung).

**Empfehlung:** Schnittmenge (restriktiver), bis der Text vorliegt.

**Blockiert / Auswirkung:** WP-009 (`scope-resolve`), WP-029 (`rls-team`).

**Quelle:** CH25; TEN-*-Befunde

**Betroffene IDs:** MGT-05, MGT-06, MGT-07, RBA-03, TEN-04, TEN-05, TKT-01

**Betroffene WPs:** WP-009 (`scope-resolve`), WP-029 (`rls-team`)

## E-12

**Thema:** ENT-04: Wer darf Entitlements schreiben?

**Status:** entschieden (2026-10-01)

**Festlegung:** Option 1: Nur der Operator schreibt Entitlements; org_admin liest.

**Frage:** Der Befund fordert Schreiben nur über den Operator. Darf org_admin weiterhin Plan/Limits sehen bzw. ändern?

**Optionen:**

1. Nur Operator schreibt; org_admin liest.
2. org_admin darf zusätzlich bestimmte Felder ändern.

**Empfehlung:** Option 1.

**Blockiert / Auswirkung:** WP-074 (`ent-operator`).

**Quelle:** ENT-04-Befund

**Betroffene IDs:** API-05, ENT-04

**Betroffene WPs:** WP-074 (`ent-operator`)

## E-13

**Thema:** RBA-02-Rollenmatrix und AUT-04-Schlüsselformat

**Status:** vorläufig entschieden (2026-10-01)

**Festlegung:** Bis zum RBA-02-/AUT-04-Text (E-01) werden keine Rechte erweitert und kein neues Schlüsselformat eingeführt. Danach gilt Option 1 (Übernahme aus dem Text).

**Frage:** Die exakte Rollen-/Rechtematrix (inkl. `discovery:ingest`) und das Trennzeichen im API-Key-Format sind ohne Katalogtext nicht eindeutig.

**Optionen:**

1. Matrix und Format aus dem nachgelieferten Text übernehmen.
2. Ist-Stand als Matrix festschreiben.

**Empfehlung:** Nachliefern (E-01); bis dahin keine Rechte erweitern.

**Blockiert / Auswirkung:** WP-046 (`role-model`), WP-067 (`apikeys`).

**Quelle:** RBA-02-, AUT-04-Befund

**Betroffene IDs:** API-05, AUT-04, RBA-02

**Betroffene WPs:** WP-046 (`role-model`), WP-067 (`apikeys`)

## E-14

**Thema:** CH26 (V): Keycloak-Org-Attribut, MFA-Pflicht, Brokering

**Status:** entschieden (2026-10-01)

**Festlegung:** Option 1: CH26 ist bestätigt (Org-Zuordnung über Nutzerattribut, MFA-Pflicht für org_admin/Operator, Brokering je Org). Umsetzung in WP-043 und WP-044.

**Frage:** CH26 ist als Vorschlag (V) markiert. Soll die Org-Zuordnung über ein Nutzerattribut, die MFA-Pflicht für org_admin/Operator und Identity-Brokering je Org verbindlich sein?

**Optionen:**

1. CH26 bestätigen.
2. Abweichende Lösung festlegen.

**Empfehlung:** Bestätigen; WP-043 (`auth-org`) und WP-044 (`kc-admin`) setzen es um.

**Blockiert / Auswirkung:** WP-043 (`auth-org`), WP-044 (`kc-admin`).

**Quelle:** docs/spec/katalog-v3/00-grundlagen.md (CH26)

**Betroffene IDs:** AUT-01, AUT-09

**Betroffene WPs:** WP-043 (`auth-org`), WP-044 (`kc-admin`)

## E-15

**Thema:** CH16 (V): PII-Vault, Paket `internal/pii`, Schlüsselrotation

**Status:** offen (2026-10-01)

**Festlegung:** Laut Empfehlung ist vor Epic B eine Grundsatzentscheidung zu treffen; der Plan kann sie nicht vorwegnehmen. Bei Bestätigung von CH16 werden Folge-WPs (AUD-01/02, SEC-05) ergänzt.

**Aufgabe:** Option zu CH16/PII-Vault wählen (eigenes WP-Paket oder nur Gerüst und Konzept). Zuständig: Product Owner/Datenschutz. Termin: vor Beginn von Epic B (WP-107).

**Frage:** CH16 (Vorschlag) verlangt Surrogat-IDs aus einem PII-Vault bereits in Phase 1 (SEC-05); REP-01 nennt `internal/pii`. Umfang und Bezug zur Schlüsselrotation (SEC-06) sind offen.

**Optionen:**

1. CH16 bestätigen und eigenes WP-Paket „PII-Vault“ vor G1 planen.
2. Für G1 nur Paketgerüst und Crypto-Shredding-Konzept.

**Empfehlung:** Entscheidung vor Epic B; bei Bestätigung werden Folge-WPs ergänzt (AUD-01/02, SEC-05).

**Blockiert / Auswirkung:** WP-141 (`b-sec-rotation`), WP-213 (`g1-gate`) (REP-01-Strukturtest).

**Quelle:** CH16; REP-01

**Betroffene IDs:** DOD-01, GATE-01, GATE-02, GATE-05, GLO-11, REP-01, SEC-06, SEQ-01

**Betroffene WPs:** WP-141 (`b-sec-rotation`), WP-213 (`g1-gate`)

## E-16

**Thema:** SEC-10: Zahlenwerte für GraphQL-Limits

**Status:** vorläufig entschieden (2026-10-01)

**Festlegung:** Die Werte aus dem Befund sind vorbelegt: Abfragetiefe 10, Komplexitätsbudget, 10 s Deadline. Mit dem SEC-10-Text (E-01) werden sie bestätigt oder angepasst.

**Frage:** Befund nennt Tiefe 10, Komplexitätsbudget und 10 s Deadline; der Katalogtext liegt nicht vor.

**Optionen:**

1. Werte aus dem Befund übernehmen.
2. Werte aus nachgeliefertem SEC-10-Text.

**Empfehlung:** Nachgelieferten Text abwarten (E-01); Befundwerte als Vorbelegung.

**Blockiert / Auswirkung:** WP-184 (`c-gql-bff`).

**Quelle:** SEC-10-Befund

**Betroffene IDs:** GQL-01, SEC-10

**Betroffene WPs:** WP-184 (`c-gql-bff`)

## E-17

**Thema:** CH15: Skalierungsziel bestätigen

**Status:** offen (2026-10-01)

**Festlegung:** Laut Empfehlung ist eine Bestätigung vor dem Lasttest nötig; der Plan kann den Zielwert nicht festlegen.

**Aufgabe:** Skalierungsziel CH15 bestätigen (1.000–10.000 Objekte, Headroom 50.000) oder anderen Wert festlegen. Zuständig: Product Owner. Termin: vor WP-093 (`a-preflight`), spätestens vor WP-203 (`lt-seed`).

**Frage:** CH15 vermerkt, dass die Antwort „1000-1000“ als 1.000–10.000 Objekte interpretiert wurde und zu bestätigen ist. Davon hängen Sizing-Tabelle, Seed-Größe und Lasttest ab.

**Optionen:**

1. 1.000–10.000 (Headroom 50.000) bestätigen.
2. Anderen Zielwert festlegen.

**Empfehlung:** Vor Lasttest bestätigen.

**Blockiert / Auswirkung:** WP-093 (`a-preflight`), WP-203 (`lt-seed`), WP-206 (`lt-run`).

**Quelle:** docs/spec/katalog-v3/00-grundlagen.md (CH15)

**Betroffene IDs:** ABN-02, INS-07, NFR-01, NFR-02, NFR-03, NFR-10, SIM-01, TEC-09

**Betroffene WPs:** WP-093 (`a-preflight`), WP-203 (`lt-seed`), WP-206 (`lt-run`)

## E-18

**Thema:** CH27 (V): impact_direction

**Status:** entschieden (2026-10-01)

**Festlegung:** Option 1: CH27 ist bestätigt. WP-127 legt die `impact_direction` je Seed-Beziehungstyp fest und dokumentiert sie im PR zur Abnahme.

**Frage:** CH27 (Vorschlag) führt eine explizite Impact-Richtung je Beziehungstyp ein; die Werte je Seed-Typ sind festzulegen.

**Optionen:**

1. CH27 bestätigen und Werte je Typ festlegen.
2. Beibehalten Source→Target (widerspricht IMP-01-Befund).

**Empfehlung:** Bestätigen.

**Blockiert / Auswirkung:** WP-127 (`b-rel-types`), WP-176 (`c-imp-direction`).

**Quelle:** CH27; IMP-01

**Betroffene IDs:** IMP-01, IMP-03, REL-01

**Betroffene WPs:** WP-127 (`b-rel-types`), WP-176 (`c-imp-direction`)

## E-19

**Thema:** CH28 (V): gemeinsamer Location-Baum

**Status:** entschieden (2026-10-01)

**Festlegung:** Option 1: CH28 ist bestätigt (gemeinsamer Location-Baum Site bis Bin). Grundlage für WP-026 und die Folge-WPs.

**Frage:** CH28 (Vorschlag) verlangt einen gemeinsamen Baum für Site bis Bin (LOC-10).

**Optionen:**

1. Bestätigen.
2. Getrennte Strukturen beibehalten.

**Empfehlung:** Bestätigen; Grundlage für WP-026 (`loc-model`).

**Blockiert / Auswirkung:** WP-026 (`loc-model`) und Folge-WPs.

**Quelle:** CH28; LOC-10

**Betroffene IDs:** LOC-10, TEC-06, TEN-02

**Betroffene WPs:** WP-026 (`loc-model`)

## E-20

**Thema:** CH29 (V): Hostname-Treffer nur als Review

**Status:** entschieden (2026-10-01)

**Festlegung:** Option 1: CH29 ist bestätigt. Ein Hostname-Treffer erzeugt nur ein Review, keinen Auto-Merge.

**Frage:** CH29 (Vorschlag): kein Auto-Merge allein über Hostname.

**Optionen:**

1. Bestätigen.
2. Ablehnen.

**Empfehlung:** Bestätigen.

**Blockiert / Auswirkung:** WP-086 (`identity`).

**Quelle:** CH29

**Betroffene IDs:** NFR-01, REC-02

**Betroffene WPs:** WP-086 (`identity`)

## E-21

**Thema:** CH30 (V)/DIS-02: Geräteprofilliste

**Status:** entschieden (2026-10-01)

**Festlegung:** Option 1: Die Profilliste nach CH30/DIS-02 ist bestätigt. WP-151 gleicht die 21 vorhandenen Repository-Profile mit dieser Liste ab.

**Frage:** Die Top-20-Profile in DIS-02 sind zu bestätigen; im Repository liegen 21 Profile.

**Optionen:**

1. Liste bestätigen.
2. Liste anpassen.

**Empfehlung:** Liste bestätigen, bevor WP-151 (`d-classify`) die Profile migriert.

**Blockiert / Auswirkung:** WP-151 (`d-classify`).

**Quelle:** CH30; DIS-02-Befund

**Betroffene IDs:** DIS-02

**Betroffene WPs:** WP-151 (`d-classify`)

## E-22

**Thema:** CH21 (V): Ausgestaltung `unlicensed_ci`

**Status:** entschieden (2026-10-01)

**Festlegung:** Option 1: CH21 ist bestätigt (Review-Item `unlicensed_ci`). Die Details zu Freigabe und Ablauf folgen dem ENT-03-Text (E-01).

**Frage:** CH21 (Vorschlag, analog) beschreibt Discovery-CIs über dem Limit als Review-Item `unlicensed_ci`; Details (Freigabe, Ablauf) fehlen.

**Optionen:**

1. Bestätigen und Details festlegen.
2. Ablehnen.

**Empfehlung:** Bestätigen.

**Blockiert / Auswirkung:** WP-073 (`ent-unlic`).

**Quelle:** CH21

**Betroffene IDs:** ENT-03, ENT-08

**Betroffene WPs:** WP-073 (`ent-unlic`)

## E-23

**Thema:** Abgrenzung GLO-13 (reservierte Namensräume) zu CI-10 (Speichermodell)

**Status:** offen (2026-10-01)

**Festlegung:** Laut Empfehlung ist der Text nachzuliefern (E-01); eine vorläufige Regel ist nicht vorgesehen.

**Aufgabe:** Abgrenzung GLO-13/CI-10 mit den Texten aus E-01 nachliefern. Zuständig: Auftraggeber/Product Owner. Termin: vor WP-091 (`glo13-ns`).

**Frage:** Welche Namensräume reserviert sind und wie `_instance` gegenüber `attributes` liegt, ist ohne Text nicht eindeutig.

**Optionen:**

1. Abgrenzung nachliefern.
2. Befundtexte als Vorgabe.

**Empfehlung:** Nachliefern (E-01).

**Blockiert / Auswirkung:** WP-091 (`glo13-ns`).

**Quelle:** GLO-13-, CI-10-Befund

**Betroffene IDs:** CI-10, GLO-13

**Betroffene WPs:** WP-091 (`glo13-ns`)

## E-24

**Thema:** NET-08: Eindeutigkeit bei VLAN ohne Site

**Status:** entschieden (2026-10-01)

**Festlegung:** Option 2: Zwei Teilindizes (`site_id IS NOT NULL` bzw. `IS NULL`), keine Sentinel-Werte.

**Frage:** UNIQUE über (org, site, nummer) mit nullable site benötigt einen Sentinel (COALESCE) oder einen Teilindex; die Vorgabe ist offen.

**Optionen:**

1. COALESCE-Sentinel.
2. Zwei Teilindizes.

**Empfehlung:** Zwei Teilindizes (keine Sentinel-Werte im Datenmodell).

**Blockiert / Auswirkung:** WP-081 (`vlan`).

**Quelle:** NET-08-Befund

**Betroffene IDs:** DB-05, NET-01, NET-02, NET-08

**Betroffene WPs:** WP-081 (`vlan`)

## E-25

**Thema:** API-05/JOB: verbindliche Pfade, Revert, Snapshot

**Status:** entschieden (2026-10-01)

**Festlegung:** Option 1: Pfade nach API-05 wörtlich; alte Pfade nur mit Deprecation/Sunset (WP-167). JOB-03-Revert nur, wo er fachlich definiert ist. Der Snapshot-Umfang folgt JOB-04.

**Frage:** Für die zehn abweichenden Ressourcen ist die verbindliche Pfadliste festzulegen; ferner, ob JOB-03-Revert für Bulk-Jobs gilt und welchen Umfang der JOB-04-State-Snapshot hat.

**Optionen:**

1. Pfade aus API-05 wörtlich übernehmen; Revert nur wo fachlich definiert.
2. Bestehende Pfade als Alias dauerhaft behalten.

**Empfehlung:** API-05 wörtlich, alte Pfade nur mit Deprecation/Sunset.

**Blockiert / Auswirkung:** WP-053 (`loc-api`), WP-168 (`c-paths`), WP-181 (`c-bulk`), WP-182 (`c-jobs-api`), WP-183 (`c-jobs-adopt`).

**Quelle:** docs/spec/katalog-v3/07-api-suche-jobs-lebenszyklus.md (API-05, API-09, JOB-03, JOB-04)

**Betroffene IDs:** API-05, API-08, API-09, BLK-01, JOB-03, JOB-04, LOC-07, LOC-10, REC-08

**Betroffene WPs:** WP-053 (`loc-api`), WP-088 (`review-model`), WP-168 (`c-paths`), WP-181 (`c-bulk`), WP-182 (`c-jobs-api`), WP-183 (`c-jobs-adopt`)

## E-26

**Thema:** Änderung der bereits ausgelieferten Migration 000056

**Status:** entschieden (2026-10-01)

**Festlegung:** Option 1: 000056 wird angepasst (Quarantäne statt Löschung). Dazu kommen ein Release-Hinweis für bereits migrierte Installationen und eine Prüfung vorhandener Backups.

**Frage:** 000056 löscht inkonsistente Composition-Zeilen. Eine Änderung wirkt nur auf noch nicht migrierte Installationen; bereits migrierte Daten sind ggf. verloren.

**Optionen:**

1. 000056 anpassen (Quarantäne) und Hinweis für migrierte Installationen.
2. Neue Migration vor 000056 nicht möglich – nur Dokumentation.

**Empfehlung:** Option 1 mit Release-Hinweis und Prüfung vorhandener Backups.

**Blockiert / Auswirkung:** WP-052 (`mig56`).

**Quelle:** AST-05-Befund; DB-02

**Betroffene IDs:** AST-05

**Betroffene WPs:** WP-052 (`mig56`)

## E-27

**Thema:** GATE-03 „beide Plattformen“ und Umgang mit WARN (GATE-02)

**Status:** entschieden (2026-10-01)

**Festlegung:** Option 1: Plattformen nach CH22 (Ubuntu 22.04/24.04, Debian 12); Debian 12 läuft per Container/VM im Workflow. WARN ist nach GATE-02-Wortlaut nur für SOLL-Punkte zulässig und wird im G1-Nachweis mit Begründung geführt.

**Frage:** Welche zwei Plattformen sind gemeint (Ubuntu und Debian nach CH22, oder Compose und Kubernetes, oder VM und Cloud)? Auf welchem Runner wird Debian 12 geprüft? Wie werden SOLL-Punkte mit WARN im G1-Nachweis geführt?

**Optionen:**

1. Ubuntu 22.04/24.04 und Debian 12 (CH22).
2. Compose und Kubernetes.

**Empfehlung:** CH22-Systeme; Debian 12 per Container/VM im Workflow.

**Blockiert / Auswirkung:** WP-092 (`a-matrix`), WP-211 (`g1-install`), WP-213 (`g1-gate`).

**Quelle:** GATE-02, GATE-03; CH22

**Betroffene IDs:** DOD-01, GATE-01, GATE-02, GATE-03, GATE-05, GLO-11, INS-01, REP-01, SEQ-01, TEC-09

**Betroffene WPs:** WP-092 (`a-matrix`), WP-211 (`g1-install`), WP-213 (`g1-gate`)

## E-28

**Thema:** Pentest: Dienstleister, Umfang, Termin; NFR-08

**Status:** entschieden (2026-10-01)

**Festlegung:** Option 1: Externer Pentest nach dem Sicherheitsreview. NFR-08 bleibt N/P.

**Aufgabe:** Pentest-Dienstleister beauftragen, Umfang und Termin festlegen. Zuständig: Auftraggeber. Termin: parallel zu Epic C, vor WP-210.

**Frage:** GATE-03 verlangt einen Pentest ohne offene High/Critical. Dienstleister, Umfang und Zeitfenster sind festzulegen; NFR-08 ist N/P.

**Optionen:**

1. Externer Dienstleister nach Sicherheitsreview.
2. Interner Pentest.

**Empfehlung:** Extern, Beauftragung parallel zu Epic C.

**Blockiert / Auswirkung:** WP-210 (`sr-pentest`).

**Quelle:** GATE-03; NFR-08

**Betroffene IDs:** GATE-03, NFR-08

**Betroffene WPs:** WP-210 (`sr-pentest`)

## E-29

**Thema:** OPS-04: Werkzeug für WAL-Archivierung und Basisbackups

**Status:** offen (2026-10-01)

**Festlegung:** Laut Empfehlung entscheidet der Betrieb; der Plan legt kein Werkzeug fest.

**Aufgabe:** Werkzeug für WAL-Archivierung und Basisbackups wählen (pgBackRest, WAL-G oder Managed-Postgres). Zuständig: Betrieb. Termin: vor WP-100 (`a-backup`).

**Frage:** Für die Backup-Kette ist ein Werkzeug zu wählen (z. B. pgBackRest, WAL-G oder Operator-eigene Lösung in Kubernetes).

**Optionen:**

1. pgBackRest.
2. WAL-G.
3. Managed-Postgres-Funktion des Hosters.

**Empfehlung:** Entscheidung durch Betrieb vor WP-100 (`a-backup`).

**Blockiert / Auswirkung:** WP-100 (`a-backup`), WP-101 (`a-restore`).

**Quelle:** OPS-04, NFR-05

**Betroffene IDs:** NFR-05, OPS-04

**Betroffene WPs:** WP-100 (`a-backup`)

## E-30

**Thema:** Traceability-Prüfung: ab wann blockierend?

**Status:** entschieden (2026-10-01)

**Festlegung:** Option 1: Die Prüfung ist ab WP-005 berichtend und blockiert je PR für neu berührte IDs. Zu G1 (WP-213) blockiert sie vollständig.

**Frage:** Die Prüfung kann nicht sofort blockieren, weil 0/261 IDs eingetragen sind.

**Optionen:**

1. Berichtend ab WP-005 (`trace`), blockierend für neu berührte IDs je PR, vollständig blockierend zu G1.
2. Sofort blockierend.

**Empfehlung:** Option 1.

**Blockiert / Auswirkung:** WP-005 (`trace`), WP-213 (`g1-gate`).

**Quelle:** GLO-11, DOD-01

**Betroffene IDs:** DOD-01, GATE-01, GATE-02, GATE-05, GLO-11, REP-01, SEQ-01

**Betroffene WPs:** WP-005 (`trace`), WP-213 (`g1-gate`)

## E-31

**Thema:** Werkzeuge für Abhängigkeits-/Container-Scan, Signatur und Lasttest

**Status:** entschieden (2026-10-01)

**Festlegung:** Option 1: govulncheck, npm audit, Trivy (Container-Scan), cosign keyless (Signatur), k6 (Lasttest).

**Frage:** Festzulegen sind: Go-/npm-Scan-Schwellen, Container-Scanner, Signaturverfahren (cosign keyless oder Schlüssel) und Lasttest-Werkzeug.

**Optionen:**

1. govulncheck, npm audit, Trivy, cosign keyless, k6.
2. Andere Werkzeuge nach Vorgabe des Betriebs.

**Empfehlung:** Option 1 (keine neuen Laufzeitabhängigkeiten im Produkt).

**Blockiert / Auswirkung:** WP-004 (`dep-scan`), WP-102 (`a-release`), WP-204 (`lt-harness`), WP-209 (`sr-review`).

**Quelle:** SEC-11, TEC-15, NFR-10

**Betroffene IDs:** ABN-02, COL-06, GATE-03, NFR-10, SEC-11, TEC-15, TST-04

**Betroffene WPs:** WP-004 (`dep-scan`), WP-102 (`a-release`), WP-204 (`lt-harness`), WP-209 (`sr-review`)

## E-32

**Thema:** REC-03: Rang der Quelle IPMI

**Status:** offen (2026-10-01)

**Festlegung:** Laut Empfehlung ist der Text nachzuliefern (E-01); eine vorläufige Regel ist nicht vorgesehen.

**Aufgabe:** REC-03-Rangtabelle (IPMI) mit den Texten aus E-01 nachliefern. Zuständig: Auftraggeber/Product Owner. Termin: vor WP-058 (`rec-decide`).

**Frage:** Die Rangtabelle nennt IPMI nicht eindeutig.

**Optionen:**

1. Rang aus nachgeliefertem Text.
2. Rang wie Redfish.

**Empfehlung:** Nachliefern (E-01).

**Blockiert / Auswirkung:** WP-058 (`rec-decide`), WP-220 (`m-srcpolicy`).

**Quelle:** REC-03-Befund

**Betroffene IDs:** AGT-05, API-07, CI-10, OVR-01, REC-03, REC-04, REC-11, REC-12

**Betroffene WPs:** WP-058 (`rec-decide`), WP-220 (`m-srcpolicy`)

## E-33

**Thema:** TLC-01: Captcha-Anbieter, Wegwerf-Domain-Liste, Trial-Werte

**Status:** offen (2026-10-01)

**Festlegung:** Laut Empfehlung geben Produkt und Datenschutz Captcha-Anbieter, Blockliste und Trial-Werte vor.

**Aufgabe:** Captcha-Anbieter, Wegwerf-Domain-Liste und Trial-Werte festlegen. Zuständig: Produkt/Datenschutz. Termin: vor WP-111 (`b-signup`).

**Frage:** Anbieter für Captcha (Datenschutz/EU), Quelle der Blocklist und Trial-Dauer/-Limits sind festzulegen.

**Optionen:**

1. EU-Anbieter bzw. selbst gehostete Lösung; gepflegte Open-Source-Liste; Trial-Werte aus Plan.
2. Andere Vorgabe.

**Empfehlung:** Vorgabe durch Produkt/Datenschutz vor WP-111 (`b-signup`).

**Blockiert / Auswirkung:** WP-111 (`b-signup`), WP-112 (`b-signup-protect`).

**Quelle:** TLC-01; CH23

**Betroffene IDs:** API-05, TLC-01

**Betroffene WPs:** WP-111 (`b-signup`), WP-112 (`b-signup-protect`)

## E-34

**Thema:** ENT-06 (V)/SIM-01: Planmatrix und Demo-Plan

**Status:** vorläufig entschieden (2026-10-01)

**Festlegung:** Die vorgeschlagene Planmatrix (V) hat bis zu ihrer Bestätigung keine Gate-Wirkung; der Demo-Datensatz verwendet Ist-Pläne.

**Frage:** Die vorgeschlagene Planmatrix (V) und der für den Demo-Datensatz zu verwendende Plan sind nicht verbindlich.

**Optionen:**

1. Matrix bestätigen.
2. Nur Ist-Pläne verwenden.

**Empfehlung:** Bis zur Bestätigung keine Gate-Wirkung der Matrix.

**Blockiert / Auswirkung:** Folgeänderung zu WP-071 (`ent-model`), WP-203 (`lt-seed`).

**Quelle:** ENT-06; SIM-01; CH14

**Betroffene IDs:** AI-02, API-06, ENT-06, IGA-01, IGA-02, IGA-03, IGA-04, NFR-10, SIM-01

**Betroffene WPs:** WP-075 (`addon`), WP-203 (`lt-seed`)

## E-35

**Thema:** NFR-09: Inhalt der Subprozessorenliste

**Status:** offen (2026-10-01)

**Festlegung:** Die Liste kann nur der Betreiber liefern.

**Aufgabe:** Subprozessorenliste liefern. Zuständig: Betreiber. Termin: vor WP-105 (`a-reference`).

**Frage:** Die Liste der Subprozessoren kann nur der Betreiber liefern.

**Optionen:**

1. Liste liefern.

**Empfehlung:** Vor WP-105 (`a-reference`) liefern.

**Blockiert / Auswirkung:** WP-105 (`a-reference`).

**Quelle:** NFR-09; PRI-11

**Betroffene IDs:** NFR-09, PRI-11

**Betroffene WPs:** WP-105 (`a-reference`)

## E-36

**Thema:** DB-05: Zeilen für Vertrag, installierte Software und Health-Findings

**Status:** entschieden (2026-10-01)

**Festlegung:** Option 1: Für G1 nur die [B]-relevanten DB-05-Zeilen (Asset-Hoheit, Standort, Lifecycle, Rack, Enthaltensein, VLAN/Subnetz). `installed_software` kommt mit Agent (P4), `contract` mit P2; der Health-Findings-Anteil ist nicht G1-Umfang.

**Frage:** Die DB-05-Tabelle verlangt kanonische Orte u. a. für Vertrag (P2), installierte Software (CH20) und Health; Tabellen `contract` und `installed_software` fehlen. Ist das für G1 relevant oder phasengebunden?

**Optionen:**

1. Für G1 nur die [B]-relevanten Zeilen (Asset-Hoheit, Standort, Lifecycle, Rack, Enthaltensein, VLAN/Subnetz).
2. Alle Zeilen vor G1.

**Empfehlung:** Option 1; `installed_software` mit Agent (P4), `contract` mit P2.

**Blockiert / Auswirkung:** WP-077 (`db05-asset`), WP-080 (`db05-lcy`), WP-230 (`m-agent`).

**Quelle:** docs/audit/03-datenmodell-standorte-metamodell.md (DB-05); CH12, CH20

**Betroffene IDs:** AGT-01, AGT-02, AGT-04, AST-04, DB-05

**Betroffene WPs:** WP-077 (`db05-asset`), WP-080 (`db05-lcy`), WP-230 (`m-agent`)

## Offene Aufgaben

Diese Aufgaben folgen aus den Empfehlungen. Die betroffenen WPs starten erst, wenn die Aufgabe erledigt ist (oder sie nutzen die Übergangsregel der Entscheidung).

| Nr. | Entscheidung | Aufgabe | Zuständig | Termin |
|---|---|---|---|---|
| A-01 | [E-01](#e-01) | Katalogteile 02, 04, 05 und 06 in `docs/spec/katalog-v3/` nachliefern | Auftraggeber/Product Owner | vor Beginn von M0.1 (WP-008) |
| A-02 | [E-02](#e-02) | v2-Originaltexte (UI-01–UI-10, ABN-01, DIS-04, IGA, WFL-04, AUT-05/06, NET-04, REP-01) nachliefern | Auftraggeber/Product Owner | vor Epic C (UI) bzw. vor WP-212 (ABN-01) |
| A-03 | [E-04](#e-04) | Teil 03 in `docs/audit/befunde.csv` und `docs/audit/99-gesamtbericht.md` konsolidieren; Texte DB-01, MET-20, MET-30–34, MET-40–44 mit E-01 anfordern | Audit | vor WP-142 (`b-met-ddl`), spätestens vor G1 |
| A-04 | [E-15](#e-15) | Option zu CH16/PII-Vault wählen (eigenes WP-Paket oder nur Gerüst und Konzept) | Product Owner/Datenschutz | vor Beginn von Epic B (WP-107) |
| A-05 | [E-17](#e-17) | Skalierungsziel CH15 bestätigen (1.000–10.000 Objekte, Headroom 50.000) oder anderen Wert festlegen | Product Owner | vor WP-093 (`a-preflight`), spätestens vor WP-203 (`lt-seed`) |
| A-06 | [E-23](#e-23) | Abgrenzung GLO-13/CI-10 mit den Texten aus E-01 nachliefern | Auftraggeber/Product Owner | vor WP-091 (`glo13-ns`) |
| A-07 | [E-28](#e-28) | Pentest-Dienstleister beauftragen, Umfang und Termin festlegen | Auftraggeber | parallel zu Epic C, vor WP-210 |
| A-08 | [E-29](#e-29) | Werkzeug für WAL-Archivierung und Basisbackups wählen (pgBackRest, WAL-G oder Managed-Postgres) | Betrieb | vor WP-100 (`a-backup`) |
| A-09 | [E-32](#e-32) | REC-03-Rangtabelle (IPMI) mit den Texten aus E-01 nachliefern | Auftraggeber/Product Owner | vor WP-058 (`rec-decide`) |
| A-10 | [E-33](#e-33) | Captcha-Anbieter, Wegwerf-Domain-Liste und Trial-Werte festlegen | Produkt/Datenschutz | vor WP-111 (`b-signup`) |
| A-11 | [E-35](#e-35) | Subprozessorenliste liefern | Betreiber | vor WP-105 (`a-reference`) |
